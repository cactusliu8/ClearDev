package cleardev

import (
	"context"
	"net/url"
	"strings"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ProductChoiceInput chooses a saved option and a registered project. The
// current selected branch is observed by the backend, never supplied by a model.
type ProductChoiceInput struct {
	OptionKey             string `json:"optionKey"`
	AOProjectID           string `json:"aoProjectId"`
	Reason                string `json:"reason"`
	ExpectedBaseCommitSHA string `json:"expectedBaseCommitSha,omitempty"`
	DeliveryRequirementID string `json:"deliveryRequirementId,omitempty"`
}

func (s *Service) observeProductChoice(ctx context.Context, product core.ProductSnapshot, previousID string, choice ProductChoiceInput) (*core.ProductSelection, error) {
	return s.observeProductChoiceForPreparation(ctx, product, previousID, choice, false)
}

func (s *Service) observeProductChoiceForPreparation(ctx context.Context, product core.ProductSnapshot, previousID string, choice ProductChoiceInput, preparing bool) (*core.ProductSelection, error) {
	if len(product.Discussions) == 0 || product.Discussions[len(product.Discussions)-1].ID != previousID {
		return nil, apierr.Conflict("PRODUCT_DISCUSSION_CHANGED", "Refresh the current proposal before choosing a project", nil)
	}
	previous := core.ProductChoiceProposal(product.Discussions)
	if previous == nil || previous.ProtocolVersion != core.ProjectDiscoveryVersion {
		return nil, apierr.Conflict("PRODUCT_OPTION_UNAVAILABLE", "A settled project proposal is required", nil)
	}
	choice.Reason = strings.TrimSpace(choice.Reason)
	if choice.Reason == "" || utf8.RuneCountInString(choice.Reason) > 4000 {
		return nil, apierr.Invalid("PRODUCT_CHOICE_REASON_REQUIRED", "Record the reason for this project choice", nil)
	}
	var selected *core.ProductOption
	for i := range previous.Result.Options {
		if previous.Result.Options[i].Key == choice.OptionKey {
			selected = &previous.Result.Options[i]
		}
	}
	if selected == nil {
		return nil, apierr.Conflict("PRODUCT_OPTION_UNAVAILABLE", "The selected option is not in the saved proposal", nil)
	}
	project, found, err := s.ao.GetProject(ctx, choice.AOProjectID)
	if err != nil {
		return nil, err
	}
	if !found || !project.ArchivedAt.IsZero() || project.Kind.WithDefault() != domain.ProjectKindSingleRepo {
		return nil, apierr.Invalid("PRODUCT_PROJECT_UNAVAILABLE", "Select an active registered single-repository project; empty folders and clones use the existing project creation flow", nil)
	}
	owner, err := s.controlledProject(ctx, product.Goal.AOProjectID)
	if err != nil {
		return nil, err
	}
	if !owner.Config.SameClearDevExecution(project.Config) {
		return nil, apierr.Conflict("EXECUTION_PROJECT_MISMATCH", "目标项目的工具和模型与本产品不同。请先将未启动的目标项目设为相同配置；已有流程的项目不能切换工具", nil)
	}
	if choice.DeliveryRequirementID != "" {
		return s.observeDeliveredProductChoice(ctx, product.Goal.ID, previous.ID, choice, *selected, project)
	}
	inspector, ok := s.inspector.(ports.ClearDevProjectSourceInspector)
	if !ok {
		return nil, apierr.Conflict("PRODUCT_SOURCE_INSPECTION_UNAVAILABLE", "Project source inspection is unavailable", nil)
	}
	observed, err := inspector.InspectProjectSource(ctx, project.Path, project.Config.DefaultBranch)
	if err != nil {
		return nil, apierr.Conflict("PRODUCT_SOURCE_UNAVAILABLE", "The selected repository must be clean and have a committed selected baseline", nil)
	}
	if !validComplexExecutionCommitSHA(observed.BaseCommitSHA) || (choice.ExpectedBaseCommitSHA != "" && choice.ExpectedBaseCommitSHA != observed.BaseCommitSHA) {
		return nil, apierr.Conflict("PRODUCT_BASELINE_CHANGED", "The selected repository version changed; refresh and choose again", nil)
	}
	if selected.Origin == "EMPTY" && !observed.Empty {
		return nil, apierr.Conflict("PRODUCT_SOURCE_NOT_EMPTY", "This option requires an empty committed project, not an existing code tree", nil)
	}
	identity := repositoryIdentity(observed.RepositoryURL)
	if !preparing && selected.Origin == "DISCOVERED" && (identity == "" || identity != repositoryIdentity(selected.RepositoryURL)) {
		return nil, apierr.Conflict("PRODUCT_SOURCE_MISMATCH", "The registered repository origin does not match the discovered option", nil)
	}
	return &core.ProductSelection{SourceDiscussionID: previous.ID, Option: *selected, Reason: choice.Reason,
		AOProjectID: choice.AOProjectID, RepositoryPath: project.Path, RepositoryURL: identity,
		BaseCommitSHA: observed.BaseCommitSHA, CreatedAt: s.now().UTC()}, nil
}

// repositoryIdentity strips transport differences and never returns credentials.
func repositoryIdentity(raw string) string {
	if strings.HasPrefix(raw, "git@") {
		parts := strings.SplitN(strings.TrimPrefix(raw, "git@"), ":", 2)
		if len(parts) == 2 {
			raw = "https://" + parts[0] + "/" + parts[1]
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Path == "" {
		return ""
	}
	return strings.ToLower(u.Host) + "/" + strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
}

func (s *Service) selectedProjectCurrent(ctx context.Context, selection *core.ProductSelection) bool {
	if selection == nil {
		return false
	}
	project, found, err := s.ao.GetProject(ctx, selection.AOProjectID)
	if err != nil || !found || !project.ArchivedAt.IsZero() || project.Kind.WithDefault() != domain.ProjectKindSingleRepo || project.Path != selection.RepositoryPath {
		return false
	}
	if selection.Delivery != nil {
		return s.complexExecution != nil && s.selectedDeliveryCurrent(ctx, selection, project)
	}
	inspector, ok := s.inspector.(ports.ClearDevProjectSourceInspector)
	if !ok {
		return false
	}
	observed, err := inspector.InspectProjectSource(ctx, project.Path, project.Config.DefaultBranch)
	return err == nil && observed.BaseCommitSHA == selection.BaseCommitSHA && repositoryIdentity(observed.RepositoryURL) == selection.RepositoryURL
}

func productChoiceReplay(d core.ProductDiscussion, input ProductDiscussionInput) bool {
	if d.UserMessage != strings.TrimSpace(input.Message) {
		return false
	}
	s := d.Selection
	explicit := core.ProductDiscussionHasChoice(s, d.ID, input.ExpectedPreviousID)
	if input.Choice == nil {
		return !explicit
	}
	deliveryMatches := s != nil && s.Delivery == nil && input.Choice.DeliveryRequirementID == ""
	if s != nil && s.Delivery != nil {
		deliveryMatches = input.Choice.DeliveryRequirementID == s.Delivery.RequirementID && input.Choice.ExpectedBaseCommitSHA == s.Delivery.CandidateSHA
	}
	return explicit && deliveryMatches && s.Option.Key == strings.TrimSpace(input.Choice.OptionKey) &&
		s.AOProjectID == strings.TrimSpace(input.Choice.AOProjectID) && s.Reason == strings.TrimSpace(input.Choice.Reason) &&
		(input.Choice.ExpectedBaseCommitSHA == "" || input.Choice.ExpectedBaseCommitSHA == s.BaseCommitSHA)
}
