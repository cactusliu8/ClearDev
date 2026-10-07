package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func loadProductContext(ctx context.Context, q *gen.Queries, discussionID string) (int, *core.ProductSelection, error) {
	row, err := q.GetClearDevProductDiscussionContext(ctx, discussionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, nil // Historical discussions retain their original protocol.
	}
	if err != nil {
		return 0, nil, err
	}
	if !row.SelectionJson.Valid {
		return int(row.ProtocolVersion), nil, nil
	}
	var selection core.ProductSelection
	if !requirementDigestMatches(row.SelectionJson.String, row.SelectionSha256.String) {
		return 0, nil, errors.New("project selection digest mismatch")
	}
	if err := json.Unmarshal([]byte(row.SelectionJson.String), &selection); err != nil {
		return 0, nil, err
	}
	if err := core.ValidateProductDeliveryBaseline(selection); err != nil {
		return 0, nil, err
	}
	return int(row.ProtocolVersion), &selection, nil
}

func insertProductContext(ctx context.Context, q *gen.Queries, d core.ProductDiscussion) error {
	if d.ProtocolVersion == 0 || d.ProtocolVersion == core.ProductDiscoveryVersion {
		if d.Selection != nil {
			return productConflict("legacy discussion cannot select a generic project")
		}
		return nil
	}
	if d.ProtocolVersion != core.ProjectDiscoveryVersion {
		return productConflict("unknown project discussion protocol")
	}
	var data, digest sql.NullString
	if d.Selection != nil {
		s := d.Selection
		if err := validateProjectDeliverySelection(ctx, q, d.ProductID, *s); err != nil {
			return err
		}
		source, err := q.GetClearDevProductDiscussion(ctx, s.SourceDiscussionID)
		if err != nil {
			return err
		}
		if source.ProductID != d.ProductID || int(source.Ordinal) >= d.Ordinal || !source.SettledAt.Valid ||
			!complexExecutionSHA1(s.BaseCommitSHA) || s.AOProjectID == "" || s.RepositoryPath == "" || s.Reason == "" || s.CreatedAt.IsZero() {
			return productConflict("project choice must bind an earlier option and observed repository")
		}
		if s.ChoiceDiscussionID != "" {
			choice, err := q.GetClearDevProductDiscussion(ctx, s.ChoiceDiscussionID)
			if err != nil {
				return err
			}
			if choice.ProductID != d.ProductID || choice.Ordinal <= source.Ordinal || int(choice.Ordinal) > d.Ordinal {
				return productConflict("choice must bind its actual user discussion after the proposal")
			}
		}
		proposal, err := core.ParseProductDiscoveryResult([]byte(source.ResultJson.String))
		if err != nil {
			return err
		}
		chosen, _ := json.Marshal(s.Option)
		matched := false
		for _, option := range proposal.Options {
			encoded, _ := json.Marshal(option)
			matched = matched || bytes.Equal(encoded, chosen)
		}
		if !matched {
			return productConflict("selected option differs from the saved proposal")
		}
		encoded, err := json.Marshal(s)
		if err != nil {
			return err
		}
		data, digest = nullableString(string(encoded)), nullableString(sha256Hex(string(encoded)))
	}
	return q.InsertClearDevProductDiscussionContext(ctx, gen.InsertClearDevProductDiscussionContextParams{
		DiscussionID: d.ID, ProtocolVersion: int64(d.ProtocolVersion), SelectionJson: data, SelectionSha256: digest,
	})
}

// A replay must retain both the exact predecessor and whether this user message
// made a new choice. Observer timestamps are not caller input and may differ
// when identical requests race before either transaction commits.
func verifyProductDiscussionReplay(ctx context.Context, q *gen.Queries, old gen.CleardevProductDiscussion, command core.AppendProductDiscussionCommand) error {
	if old.ProductID != command.Discussion.ProductID || old.UserMessage != command.Discussion.UserMessage || old.Ordinal < 1 {
		return productConflict("discussion id already belongs to different input")
	}
	rounds, err := q.ListClearDevProductDiscussions(ctx, old.ProductID)
	if err != nil {
		return err
	}
	if int(old.Ordinal) >= len(rounds) || rounds[old.Ordinal-1].ID != command.ExpectedPreviousID {
		return productConflict("discussion replay has a different predecessor")
	}
	protocol, selection, err := loadProductContext(ctx, q, old.ID)
	if err != nil {
		return err
	}
	if command.Discussion.ProtocolVersion != 0 && command.Discussion.ProtocolVersion != protocol {
		return productConflict("discussion replay changes its protocol")
	}
	explicit := core.ProductDiscussionHasChoice(selection, old.ID, command.ExpectedPreviousID)
	if command.Selection == nil {
		if explicit {
			return productConflict("discussion replay dropped the user choice")
		}
		return nil
	}
	if !explicit {
		return productConflict("discussion replay added a user choice")
	}
	left, right := *selection, *command.Selection
	left.CreatedAt, right.CreatedAt = time.Time{}, time.Time{}
	a, err := json.Marshal(left)
	if err != nil {
		return err
	}
	b, err := json.Marshal(right)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return productConflict("discussion replay changed the user choice or repository binding")
	}
	return nil
}

// requireProductPredecessor checks persisted completion inside the write
// transaction. Task PASS and an uncompleted integration cannot satisfy it.
func requireProductPredecessor(ctx context.Context, q *gen.Queries, stage core.ProductStage, baseline string) error {
	if stage.Ordinal == 0 {
		return nil
	}
	sha, err := q.GetClearDevProductStagePredecessorDelivery(ctx, gen.GetClearDevProductStagePredecessorDeliveryParams{
		DiscussionID: stage.DiscussionID, Ordinal: int64(stage.Ordinal - 1),
	})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && sha != baseline) {
		return productConflict("previous stage must complete and its delivery must be explicitly selected as the baseline")
	}
	return err
}

// projectPlanningStage rechecks the immutable Stage source under the caller's
// transaction. A newer discussion invalidates all earlier planning inputs.
func projectPlanningStage(ctx context.Context, q *gen.Queries, requirementID string) (core.ProductStage, error) {
	row, err := q.GetClearDevProductStageByRequirement(ctx, nullableString(requirementID))
	if err != nil {
		return core.ProductStage{}, err
	}
	stage, err := productStageFromRow(row)
	if err != nil {
		return stage, err
	}
	if stage.Definition.ExecutionBasis == nil {
		return stage, productConflict("planning-only protocol requires a project execution basis")
	}
	selection, err := loadStageSelection(ctx, q, stage.ID)
	if err != nil {
		return stage, err
	}
	if selection == nil || selection.BaseCommitSHA != stage.BaseCommitSHA {
		return stage, productConflict("project plan lost its selected repository version")
	}
	stage.Selection = selection
	rounds, err := q.ListClearDevProductDiscussions(ctx, stage.ProductID)
	if err != nil {
		return stage, err
	}
	if len(rounds) == 0 || rounds[len(rounds)-1].ID != stage.DiscussionID {
		return stage, productConflict("project plan has been superseded by a newer discussion")
	}
	parent, err := q.GetClearDevRequirement(ctx, stage.ProductID)
	if err != nil {
		return stage, err
	}
	if parent.CancelledAt.Valid || parent.PausedFromState.Valid || parent.State == "PAUSED" {
		return stage, productConflict("project plan belongs to a cancelled product")
	}
	if err := requireProductPredecessor(ctx, q, stage, stage.BaseCommitSHA); err != nil {
		return stage, err
	}
	return stage, nil
}

// projectDecisionCurrent prevents a superseded generic Stage from reappearing
// in the native confirmation queue. Historical requests remain audit records.
func projectDecisionCurrent(ctx context.Context, q *gen.Queries, requirementID string) (bool, error) {
	row, err := q.GetClearDevProductStageByRequirement(ctx, nullableString(requirementID))
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	stage, err := productStageFromRow(row)
	if err != nil {
		return false, err
	}
	if stage.Definition.ExecutionBasis == nil {
		return true, nil
	}
	_, err = projectPlanningStage(ctx, q, requirementID)
	var rule *core.RuleError
	if errors.As(err, &rule) && rule.Code == core.ReasonPreconditionNotMet {
		return false, nil
	}
	return err == nil, err
}

// A revision is allowed only before any execution, and never unfreezes legacy
// prepared products. Old records remain immutable and become non-current.
func checkProductRevision(ctx context.Context, q *gen.Queries, productID string) error {
	stages, err := q.ListClearDevProductStages(ctx, productID)
	if err != nil {
		return err
	}
	for _, row := range stages {
		if !row.DevelopmentRequirementID.Valid {
			continue
		}
		stage, err := productStageFromRow(row)
		if err != nil {
			return err
		}
		if stage.Definition.ExecutionBasis == nil {
			return productConflict("legacy product proposal is frozen after stage preparation")
		}
	}
	running, err := q.HasClearDevProductExecution(ctx, productID)
	if err != nil {
		return err
	}
	if running {
		return productConflict("an executing product cannot be revised by discussion")
	}
	return nil
}
