package cleardev

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type productSourceStore interface {
	GetClearDevProductSourcePreparation(context.Context, string) (*core.ProductSourcePreparation, error)
	GetLatestClearDevProductSourcePreparation(context.Context, string) (*core.ProductSourcePreparation, error)
	BeginClearDevProductSourcePreparation(context.Context, core.ProductSourcePreparation) error
	SetClearDevProductSourcePreparation(context.Context, string, string, string, string, *core.ProductSelection, time.Time) error
	CheckClearDevProductSourcePreparation(context.Context, string) error
	ListClearDevPendingProductSources(context.Context) ([]string, error)
}

// ProductSourcePreparationView exposes the saved request for exact retry after
// reload. Failure contains retained preparation errors, never a model diagnosis.
type ProductSourcePreparationView struct {
	State     string                 `json:"state"`
	Failure   string                 `json:"failure,omitempty"`
	SourceURL string                 `json:"sourceUrl"`
	Input     ProductDiscussionInput `json:"input"`
}

func (s *Service) submitSourcePreparation(ctx context.Context, product core.ProductSnapshot, input ProductDiscussionInput) (bool, error) {
	store, ok := s.complex.(productSourceStore)
	if !ok {
		return false, nil
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return true, err
	}
	old, err := store.GetClearDevProductSourcePreparation(ctx, input.RequestID)
	if err != nil {
		return true, err
	}
	if old != nil {
		if old.ProductID != product.Goal.ID || old.InputJSON != string(raw) {
			return true, apierr.Conflict("PRODUCT_REQUEST_CHANGED", "This source preparation request belongs to different input", nil)
		}
		if old.Status == "FAILED" {
			if err := store.SetClearDevProductSourcePreparation(ctx, old.ID, "FAILED", "PENDING", "", nil, s.now().UTC()); err != nil {
				return true, err
			}
		}
		s.scheduleComplexFlow(product.Goal.ID)
		return true, nil
	}
	for _, d := range product.Discussions {
		if d.ID == input.RequestID {
			return false, nil
		}
	}
	if input.Choice == nil || input.Choice.DeliveryRequirementID != "" {
		return false, nil
	}
	proposal := core.ProductChoiceProposal(product.Discussions)
	if proposal == nil {
		return false, nil
	}
	discovered := false
	for _, option := range proposal.Result.Options {
		if option.Key == input.Choice.OptionKey && option.Origin == "DISCOVERED" {
			discovered = true
		}
	}
	if !discovered {
		return false, nil
	}
	if _, ok := s.inspector.(ports.ClearDevProjectSourcePreparer); !ok {
		return true, apierr.Conflict("PRODUCT_SOURCE_PREPARATION_UNAVAILABLE", "Source preparation is unavailable", nil)
	}
	selection, err := s.observeProductChoiceForPreparation(ctx, product, input.ExpectedPreviousID, *input.Choice, true)
	if err != nil {
		return true, err
	}
	selection.ChoiceDiscussionID = input.RequestID
	project, found, err := s.ao.GetProject(ctx, selection.AOProjectID)
	if err != nil || !found {
		return true, fmt.Errorf("selected source project is unavailable")
	}
	err = store.BeginClearDevProductSourcePreparation(ctx, core.ProductSourcePreparation{ID: input.RequestID, ProductID: product.Goal.ID, PreviousID: input.ExpectedPreviousID, InputJSON: string(raw), Selection: *selection, Branch: project.Config.DefaultBranch, CreatedAt: s.now().UTC()})
	if err != nil {
		return true, err
	}
	s.scheduleComplexFlow(product.Goal.ID)
	return true, nil
}

func (s *Service) sourcePreparationCurrent(ctx context.Context, p *core.ProductSourcePreparation) error {
	store, ok := s.complex.(productSourceStore)
	if !ok {
		return fmt.Errorf("source preparation storage unavailable")
	}
	if err := store.CheckClearDevProductSourcePreparation(ctx, p.ID); err != nil {
		return err
	}
	project, found, err := s.ao.GetProject(ctx, p.Selection.AOProjectID)
	if err != nil || !found || !project.ArchivedAt.IsZero() || project.Path != p.Selection.RepositoryPath || project.Config.DefaultBranch != p.Branch {
		return fmt.Errorf("selected source project changed")
	}
	inspector, ok := s.inspector.(ports.ClearDevProjectSourceInspector)
	if !ok {
		return fmt.Errorf("source inspection unavailable")
	}
	observed, err := inspector.InspectProjectSource(ctx, project.Path, p.Branch)
	if err != nil {
		return fmt.Errorf("source target cannot be inspected: %w", err)
	}
	if repositoryIdentity(observed.RepositoryURL) != p.Selection.RepositoryURL {
		return fmt.Errorf("source target origin changed")
	}
	products, err := s.productStore()
	if err != nil {
		return err
	}
	product, found, err := products.GetClearDevProduct(ctx, p.ProductID)
	if err != nil || !found {
		return fmt.Errorf("product is unavailable")
	}
	owner, err := s.controlledProject(ctx, product.Goal.AOProjectID)
	if err != nil {
		return err
	}
	if !owner.Config.SameClearDevExecution(project.Config) {
		return fmt.Errorf("selected project execution changed")
	}
	return nil
}

// Returns handled=true while preparation owns the next discussion. The original
// scheduler resumes PENDING/READY after restart; errors never dispatch Steward.
func (s *Service) advanceSourcePreparation(ctx context.Context, product core.ProductSnapshot) (handled bool, err error) {
	store, ok := s.complex.(productSourceStore)
	if !ok {
		return false, nil
	}
	p, err := store.GetLatestClearDevProductSourcePreparation(ctx, product.Goal.ID)
	if err != nil {
		return true, err
	}
	if p == nil || p.Status == "APPLIED" {
		return false, nil
	}
	if len(product.Discussions) == 0 {
		return true, fmt.Errorf("source preparation has no product discussion")
	}
	latest := product.Discussions[len(product.Discussions)-1]
	if p.Status == "FAILED" {
		return latest.ID == p.PreviousID, nil
	}
	// An interrupted append is proven by the store before marking APPLIED.
	if latest.ID == p.ID && p.Status == "READY" {
		return true, store.SetClearDevProductSourcePreparation(ctx, p.ID, "READY", "APPLIED", "", nil, s.now().UTC())
	}
	fail := func(cause error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return store.SetClearDevProductSourcePreparation(ctx, p.ID, p.Status, "FAILED", cause.Error(), nil, s.now().UTC())
	}
	if err := s.sourcePreparationCurrent(ctx, p); err != nil {
		return true, fail(err)
	}
	if p.Status == "PENDING" {
		preparer, ok := s.inspector.(ports.ClearDevProjectSourcePreparer)
		if !ok {
			return true, fail(fmt.Errorf("source preparation adapter unavailable"))
		}
		downloadCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		result, prepareErr := preparer.PrepareProjectSource(downloadCtx, ports.ClearDevSourcePreparation{RequestID: p.ID, Workspace: p.Selection.RepositoryPath, Branch: p.Branch, RepositoryURL: p.Selection.Option.RepositoryURL, ExpectedBase: p.Selection.BaseCommitSHA, Current: func(checkCtx context.Context) error { return s.sourcePreparationCurrent(checkCtx, p) }})
		cancel()
		if prepareErr != nil {
			return true, fail(prepareErr)
		}
		prepared := p.Selection
		prepared.BaseCommitSHA = result.BaseCommitSHA
		prepared.RepositoryURL = repositoryIdentity(result.RepositoryURL)
		if err := store.SetClearDevProductSourcePreparation(ctx, p.ID, "PENDING", "READY", "", &prepared, s.now().UTC()); err != nil {
			return true, err
		}
		p.Status, p.Prepared = "READY", &prepared
	}
	if p.Prepared == nil || !s.selectedProjectCurrent(ctx, p.Prepared) {
		return true, fail(fmt.Errorf("prepared project baseline changed before discussion"))
	}
	var input ProductDiscussionInput
	if err := json.Unmarshal([]byte(p.InputJSON), &input); err != nil {
		return true, fail(err)
	}
	products, err := s.productStore()
	if err != nil {
		return true, err
	}
	if _, err := products.AppendClearDevProductDiscussion(ctx, core.AppendProductDiscussionCommand{ExpectedPreviousID: p.PreviousID, Selection: p.Prepared, Discussion: core.ProductDiscussion{ID: p.ID, ProductID: p.ProductID, UserMessage: strings.TrimSpace(input.Message), CreatedAt: s.now().UTC()}}); err != nil {
		return true, fail(err)
	}
	return true, store.SetClearDevProductSourcePreparation(ctx, p.ID, "READY", "APPLIED", "", nil, s.now().UTC())
}
