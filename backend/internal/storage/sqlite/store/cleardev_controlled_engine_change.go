package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// controlledEngineChangeDisplay explains the narrowly bound human grant: one
// deliberate mid-project engine change for one controlled project. Every
// recorded session, task, budget, candidate and check stays unchanged.
func controlledEngineChangeDisplay(b core.ControlledEngineChangeBinding) core.HumanDecisionDisplay {
	return core.HumanDecisionDisplay{
		Title:   "Authorize a controlled engine change",
		Summary: "Switch this controlled project's workers from their current engine to " + b.TargetHarness + ".",
		FullContent: "Approving authorizes one deliberate engine change for this project: every controlled worker session may switch its underlying provider agent to " +
			b.TargetHarness + " (" + b.Model + "), and the project's frozen cleardev execution choice follows the same target. AO session identities, tasks, budgets, rounds, candidates and checks stay unchanged; the transfer keeps the session and hands the new engine its context. Rejecting keeps the frozen engine choice.",
		ChangeSummary: "Authorize " + b.TargetHarness + " (" + b.Model + ") for the controlled workers of project " + b.AOProjectID + ".",
	}
}

// CreateControlledEngineChangeRequest records the pending human decision offer
// for one deliberate engine change. Nothing is granted here.
func (s *Store) CreateControlledEngineChangeRequest(ctx context.Context, requirement core.DevelopmentRequirement, binding core.ControlledEngineChangeBinding, at time.Time) (string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	requestID := ""
	err := s.inTx(ctx, "create controlled engine change request", func(q *gen.Queries) error {
		prior, err := q.ListClearDevHumanDecisionRequestsByKind(ctx, core.HumanDecisionKindControlledEngineChange)
		if err != nil {
			return err
		}
		for _, row := range prior {
			// Only a request whose binding parses under the current schema can
			// ever be offered or settled; a stale pre-fix binding is a dead
			// row and must not absorb the live request.
			if existing, err := core.ParseControlledEngineChangeBinding([]byte(row.BindingJson)); err == nil &&
				existing.AOProjectID == binding.AOProjectID && existing.TargetHarness == binding.TargetHarness &&
				existing.Model == binding.Model {
				requestID = row.ID
				return nil
			}
		}
		bindingJSON, err := json.Marshal(binding)
		if err != nil {
			return err
		}
		normalized, err := core.ParseControlledEngineChangeBinding(bindingJSON)
		if err != nil {
			return err
		}
		bindingJSON, err = json.Marshal(normalized)
		if err != nil {
			return err
		}
		display := controlledEngineChangeDisplay(normalized)
		displayJSON, err := json.Marshal(display)
		if err != nil {
			return err
		}
		digest, err := core.HumanDecisionContentSHA256(core.HumanDecisionKindControlledEngineChange, bindingJSON, display)
		if err != nil {
			return err
		}
		requestID = uuid.NewString()
		if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{
			ID: requestID, DevelopmentProjectID: requirement.ID, DecisionKind: core.HumanDecisionKindControlledEngineChange,
			BindingSchemaVersion: 1, BindingJson: string(bindingJSON), DisplayJson: string(displayJSON), ContentSha256: digest, CreatedAt: at,
		}); err != nil {
			if isUniqueConstraint(err) {
				return nil
			}
			return err
		}
		return insertClearDevEvent(ctx, q, core.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: core.SubjectHumanDecisionRequest, SubjectID: requestID,
			Action: core.ActionCreateHumanDecisionRequest, TargetState: string(core.HumanDecisionRequestPending),
			Outcome: core.EventAccepted, Source: core.EventSourceControlPlane, CreatedAt: at,
		})
	})
	return requestID, err
}

// ControlledEngineChangeAuthorized reports whether a human approved exactly
// this engine change for the project. It is the durable permission the agent
// switch checks before touching a controlled session's frozen engine choice.
func (s *Store) ControlledEngineChangeAuthorized(ctx context.Context, aoProjectID string, targetHarness domain.AgentHarness, model string) (bool, error) {
	rows, err := s.qr.ListClearDevHumanDecisionRequestsByKind(ctx, core.HumanDecisionKindControlledEngineChange)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Status != string(core.HumanDecisionRequestResolved) || row.Decision != string(core.HumanDecisionApprove) {
			continue
		}
		b, err := core.ParseControlledEngineChangeBinding([]byte(row.BindingJson))
		if err != nil {
			continue
		}
		if b.AOProjectID == aoProjectID && b.TargetHarness == string(targetHarness) && b.Model == model {
			return true, nil
		}
	}
	return false, nil
}

// settleControlledEngineChange resolves the human engine change decision.
// Approval updates the project's frozen cleardev execution choice to the exact
// authorized target inside the settlement transaction; sessions switch through
// the ordinary agent switch entry, which re-checks this authorization. Rejection
// changes nothing.
func settleControlledEngineChange(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*core.RuleError, error) {
	binding, err := core.ParseControlledEngineChangeBinding(result.Binding)
	if err != nil {
		return nil, err
	}
	project, err := q.GetProject(ctx, domain.ProjectID(binding.AOProjectID))
	if err != nil {
		return nil, err
	}
	record := projectRowFromGen(project)
	if record.ID == "" || binding.DevelopmentRequirementID != request.DevelopmentProjectID {
		return nil, complexExecutionRule("engine change authorization names another project")
	}
	config := record.Config
	choice := config.ClearDev
	current := domain.HarnessCodex
	currentModel := ""
	if choice != nil {
		current, currentModel = choice.Harness, choice.Model
	}
	if current == domain.AgentHarness(binding.TargetHarness) && currentModel == binding.Model {
		return nil, complexExecutionRule("engine change target already in use")
	}
	changed, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{
		ID: request.ID, Decision: string(result.Decision), ResolvedAt: nullableTime(at)})
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errors.New("engine change authorization already decided")
	}
	outcome := core.HumanDecisionDispatchRejected
	if result.Decision == core.HumanDecisionApprove {
		outcome = core.HumanDecisionDispatchApproved
		config.ClearDev = &domain.ClearDevExecutionConfig{
			Harness: domain.AgentHarness(binding.TargetHarness), Model: binding.Model, Effort: binding.Effort,
		}
		if err := config.Validate(); err != nil {
			return nil, fmt.Errorf("engine change target is not a valid controlled choice: %w", err)
		}
		encoded, err := marshalProjectConfig(config)
		if err != nil {
			return nil, err
		}
		rows, err := q.UpdateProjectSettings(ctx, gen.UpdateProjectSettingsParams{
			ID: domain.ProjectID(binding.AOProjectID), DisplayName: record.DisplayName, Config: encoded})
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			return nil, complexExecutionRule("engine change project is no longer current")
		}
	}
	if err := consumeDispatchCAS(ctx, q, result.Nonce, result.DesktopRunID, outcome, at); err != nil {
		return nil, err
	}
	requirementRow, err := q.GetClearDevRequirement(ctx, request.DevelopmentProjectID)
	if err != nil {
		return nil, err
	}
	settleEvent := core.RequirementEvent{AOProjectID: requirementRow.AoProjectID, DevelopmentRequirementID: request.DevelopmentProjectID,
		SubjectType: core.SubjectHumanDecisionRequest, SubjectID: request.ID, Action: core.ActionSettleHumanDecision,
		PreviousState: "PENDING", TargetState: "RESOLVED", Outcome: core.EventAccepted, Source: core.EventSourceHumanDecision, CreatedAt: at}
	if err := insertClearDevEvent(ctx, q, settleEvent); err != nil {
		return nil, err
	}
	seq, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: request.ID, Action: string(core.ActionSettleHumanDecision)})
	if err != nil {
		return nil, err
	}
	return nil, q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: request.ID, Decision: string(result.Decision), EventSequence: seq, CreatedAt: at})
}

// supersededLegacyEngineChange is only a read-time exclusion for the old
// developmentProjectId spelling. It grants nothing and never rewrites history.
// An exact later human approval and the still-matching project choice are required.
func supersededLegacyEngineChange(ctx context.Context, q *gen.Queries, old gen.CleardevHumanDecisionRequest) (bool, error) {
	if old.DecisionKind != core.HumanDecisionKindControlledEngineChange || old.Status != "PENDING" || old.BindingSchemaVersion != 1 ||
		strings.Count(old.BindingJson, `"developmentProjectId"`) != 1 || strings.Contains(old.BindingJson, `"developmentRequirementId"`) {
		return false, nil
	}
	normalized := strings.Replace(old.BindingJson, `"developmentProjectId"`, `"developmentRequirementId"`, 1)
	binding, err := core.ParseControlledEngineChangeBinding([]byte(normalized))
	if err != nil || binding.DevelopmentRequirementID != old.DevelopmentProjectID {
		return false, nil //nolint:nilerr // Invalid legacy bindings stay pending; they are not a database read failure.
	}
	requirement, err := q.GetClearDevRequirement(ctx, old.DevelopmentProjectID)
	if err != nil {
		return false, err
	}
	if requirement.AoProjectID != binding.AOProjectID {
		return false, nil
	}
	project, err := q.GetProject(ctx, domain.ProjectID(binding.AOProjectID))
	if err != nil {
		return false, err
	}
	choice := projectRowFromGen(project).Config.ClearDev
	if choice == nil || string(choice.Harness) != binding.TargetHarness || choice.Model != binding.Model || choice.Effort != binding.Effort {
		return false, nil
	}
	requests, err := q.ListClearDevHumanDecisionRequestsByKind(ctx, core.HumanDecisionKindControlledEngineChange)
	if err != nil {
		return false, err
	}
	for _, next := range requests {
		if next.DevelopmentProjectID != old.DevelopmentProjectID || next.BindingSchemaVersion != 1 || next.Status != "RESOLVED" || next.Decision != "APPROVE" || !next.CreatedAt.After(old.CreatedAt) {
			continue
		}
		target, err := core.ParseControlledEngineChangeBinding([]byte(next.BindingJson))
		if err != nil || target != binding {
			continue
		}
		effect, err := q.GetClearDevHumanDecisionEffect(ctx, next.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if effect.Decision == "APPROVE" && effect.EventSequence > 0 {
			return true, nil
		}
	}
	return false, nil
}
