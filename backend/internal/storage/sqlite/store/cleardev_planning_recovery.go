package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func planningRecoveryBinding(row gen.ListClearDevPlanningRecoveryCandidatesRow) core.PlanningRecoveryBinding {
	a, p := row.CleardevAgentStepAttempt, row.CleardevComplexEngineeringPlan
	return core.PlanningRecoveryBinding{DevelopmentRequirementID: a.DevelopmentProjectID, RequirementVersionID: p.RequirementVersionID, RequirementVersionSHA256: p.RequirementSha256, PlanID: p.ID, PlanSHA256: p.PlanSha256, LogicalStepID: a.LogicalStepID, FirstAttemptID: a.ID, FailureEventID: row.CleardevAgentAttemptEvent.ID, RoleBindingID: a.RoleBindingID, AOSessionID: a.AoSessionID, PromptSHA256: a.PromptSha256}
}

func planningRecoveryRequestID(requirementID string) string {
	return "planning-recovery:" + requirementID
}

func ensurePlanningRecoveryRequests(ctx context.Context, q *gen.Queries, at time.Time) error {
	rows, err := q.ListClearDevPlanningRecoveryCandidates(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		b := planningRecoveryBinding(row)
		id := planningRecoveryRequestID(b.DevelopmentRequirementID)
		if _, err := q.GetClearDevHumanDecisionRequest(ctx, id); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		display := core.HumanDecisionDisplay{Title: "恢复中断的技术计划复核", Summary: "允许原 Steward 会话对同一计划再复核一次。通过后继续原需求开发。", FullContent: fmt.Sprintf("需求：%s\n计划：%s\n计划 SHA256：%s\n会话：%s\n原失败：TURN_INTERRUPTED\n仅增加一次复核尝试，保留原批准、失败和预算记录。不会改变规格、计划或验收标准。再次失败则停止。", b.DevelopmentRequirementID, b.PlanID, b.PlanSHA256, b.AOSessionID), ChangeSummary: "尝试 1 保留，授权尝试 2；拒绝则保持停止。"}
		displayRaw, err := json.Marshal(display)
		if err != nil {
			return err
		}
		digest, err := core.HumanDecisionContentSHA256(core.HumanDecisionKindPlanningRecovery, raw, display)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{ID: id, DevelopmentProjectID: b.DevelopmentRequirementID, DecisionKind: core.HumanDecisionKindPlanningRecovery, BindingSchemaVersion: 1, BindingJson: string(raw), DisplayJson: string(displayRaw), ContentSha256: digest, CreatedAt: at}); err != nil {
			return err
		}
	}
	return nil
}

func settlePlanningRecovery(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*core.RuleError, error) {
	b, err := core.ParsePlanningRecoveryBinding(result.Binding)
	if err != nil {
		return nil, err
	}
	if request.ID != planningRecoveryRequestID(b.DevelopmentRequirementID) {
		return nil, errors.New("invalid planning recovery request identity")
	}
	candidates, err := q.ListClearDevPlanningRecoveryCandidates(ctx)
	if err != nil {
		return nil, err
	}
	var match *gen.ListClearDevPlanningRecoveryCandidatesRow
	for i := range candidates {
		if planningRecoveryBinding(candidates[i]) == b {
			match = &candidates[i]
			break
		}
	}
	if match == nil {
		return nil, errors.New("interrupted planning review is no longer eligible for recovery")
	}
	rows, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{ID: request.ID, Decision: string(result.Decision), ResolvedAt: nullableTime(at)})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, errors.New("planning recovery request already settled")
	}
	outcome := core.HumanDecisionDispatchRejected
	if result.Decision == core.HumanDecisionApprove {
		outcome = core.HumanDecisionDispatchApproved
		a := match.CleardevAgentStepAttempt
		rows, err = q.InsertClearDevAgentStepAttempt(ctx, gen.InsertClearDevAgentStepAttemptParams{ID: a.LogicalStepID + ":attempt:2", DevelopmentProjectID: a.DevelopmentProjectID, LogicalStepID: a.LogicalStepID, StepCategory: a.StepCategory, StepKind: a.StepKind, AttemptNumber: 2, RoleBindingID: a.RoleBindingID, AoSessionID: a.AoSessionID, ClientMessageID: a.ClientMessageID + ":attempt:2", PromptSha256: a.PromptSha256, TriggerFailureEventID: nullableString(b.FailureEventID), RequestedAt: at, CreatedAt: nullableTime(at), RequestedAtSemantics: string(core.AttemptTimeActualCreation)})
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			return nil, errors.New("planning recovery attempt already exists")
		}
		rows, err = q.ReopenClearDevInterruptedPlanningReview(ctx, b.LogicalStepID)
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			return nil, errors.New("planning recovery step changed")
		}
	}
	if err := consumeDispatchCAS(ctx, q, result.Nonce, result.DesktopRunID, outcome, at); err != nil {
		return nil, err
	}
	project, err := q.GetClearDevRequirement(ctx, b.DevelopmentRequirementID)
	if err != nil {
		return nil, err
	}
	event := core.RequirementEvent{AOProjectID: project.AoProjectID, DevelopmentRequirementID: project.ID, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: request.ID, Action: core.ActionSettleHumanDecision, PreviousState: "PENDING", TargetState: "RESOLVED", Outcome: core.EventAccepted, Source: core.EventSourceHumanDecision, CreatedAt: at}
	if err := insertClearDevEvent(ctx, q, event); err != nil {
		return nil, err
	}
	sequence, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: request.ID, Action: string(core.ActionSettleHumanDecision)})
	if err != nil {
		return nil, err
	}
	return nil, q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: request.ID, Decision: string(result.Decision), EventSequence: sequence, CreatedAt: at})
}
