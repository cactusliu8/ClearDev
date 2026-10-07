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
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// RequestClearDevStoppedCheckRecovery only records a pending native decision.
// It does not run checks, consume another task round or clear the Planner STOP.
func (s *Store) RequestClearDevStoppedCheckRecovery(ctx context.Context, id, supplement string, b core.StoppedCheckRecoveryBinding, at time.Time) error {
	if id == "" || len(id) > 100 || strings.TrimSpace(id) != id || strings.TrimSpace(supplement) == "" || len(supplement) > 16000 {
		return complexExecutionRule("invalid stopped check recovery request")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "request same-candidate checker recovery", func(q *gen.Queries) error {
		binding, err := json.Marshal(b)
		if err != nil {
			return err
		}
		if _, err := core.ParseStoppedCheckRecoveryBinding(binding); err != nil {
			return err
		}
		if old, err := q.GetClearDevStoppedCheckRequest(ctx, id); err == nil {
			if old.BindingJson != string(binding) || old.Supplement != supplement {
				return complexExecutionRule("same check recovery intent changed on replay")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p, err := stoppedCheckEvidence(ctx, q, b.DevelopmentRequirementID, nil)
		if err != nil {
			return err
		}
		if p.state.Option.Action == "" || p.state.Option.UnavailableReason != "" || p.state.Binding != b {
			return complexExecutionRule("same candidate check source is not current")
		}
		if _, err := q.GetClearDevStoppedCheckRequestForRun(ctx, b.ExecutionRunID); err == nil {
			return complexExecutionRule("this run already requested its single stopped check recovery")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		humanID := uuid.NewString()
		display := core.HumanDecisionDisplay{Title: "恢复原候选检查（保留 Planner STOP）", Summary: "只对当前候选重跑一次已明确结束的不可用检查，然后沿原检查和审核链继续。",
			FullContent:   "原 Planner 的 STOP、不可用检查和已经消耗的协调、返工及消息全部保留。批准只恢复这个候选的一个检查，不允许修改源码、改变命令/超时/容器资源限制，不重新发送 Builder 或 Planner。任务累计返工数不回退，所有角色额度不增加。重检失败不自动循环；只有新检查真正通过并完成原独立任务审核、试用和终审后才可能完成。此操作不把原 STOP 改写为 CONTINUE，不代表任何检查或 ACC-005 已通过。拒绝和稍后不恢复检查。",
			ChangeSummary: fmt.Sprintf("事件 %s；原检查 %s；候选 %s；原 Builder %s；累计返工数保持 %d；上下文 %s。", b.EventID, b.CheckRunID, b.CandidateSHA, b.AOSessionID, b.ReworkCount, b.ContextSHA256),
		}
		display.FullContent += "\n\n原 Planner STOP（保留原结论）：\n" + p.decision.Summary + "\n\n检查停止原因：CHECKER_UNAVAILABLE。完整原始检查日志仍在原记录中。"
		rawDisplay, err := json.Marshal(display)
		if err != nil {
			return err
		}
		digest, err := core.HumanDecisionContentSHA256(core.HumanDecisionKindStoppedCheckRecovery, binding, display)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{ID: humanID, DevelopmentProjectID: b.DevelopmentRequirementID, DecisionKind: core.HumanDecisionKindStoppedCheckRecovery, BindingSchemaVersion: 1, BindingJson: string(binding), DisplayJson: string(rawDisplay), ContentSha256: digest, CreatedAt: at}); err != nil {
			return err
		}
		if err := q.InsertClearDevStoppedCheckRequest(ctx, gen.InsertClearDevStoppedCheckRequestParams{ID: id, ExecutionRunID: b.ExecutionRunID, EventID: b.EventID, OriginalCheckID: b.CheckRunID, RetryCheckID: b.RetryCheckRunID, DecisionRequestID: humanID, BindingJson: string(binding), ContextJson: p.contextJSON, Supplement: supplement, CreatedAt: at}); err != nil {
			return err
		}
		req, err := q.GetClearDevRequirement(ctx, b.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		return insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: req.AoProjectID, DevelopmentRequirementID: req.ID, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: humanID, Action: core.ActionCreateHumanDecisionRequest, TargetState: "PENDING", Outcome: core.EventAccepted, Source: core.EventSourceControlPlane, CreatedAt: at})
	})
}

func validateReopenStoppedCheck(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest) error {
	_, _, err := currentStoppedCheckOffer(ctx, q, result, request)
	return err
}

func currentStoppedCheckOffer(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest) (gen.CleardevStoppedCheckRequest, stoppedCheckProof, error) {
	var p stoppedCheckProof
	b, err := core.ParseStoppedCheckRecoveryBinding(result.Binding)
	if err != nil {
		return gen.CleardevStoppedCheckRequest{}, p, err
	}
	offer, err := q.GetClearDevStoppedCheckRequestForEvent(ctx, b.EventID)
	if err != nil {
		return offer, p, err
	}
	if offer.DecisionRequestID != request.ID || offer.BindingJson != request.BindingJson || offer.BindingJson != string(result.Binding) {
		return offer, p, complexExecutionRule("stopped check human binding changed")
	}
	p, err = stoppedCheckEvidence(ctx, q, b.DevelopmentRequirementID, nil)
	if err != nil {
		return offer, p, err
	}
	if p.state.Option.UnavailableReason != "" || p.state.Binding != b || p.contextJSON != offer.ContextJson {
		return offer, p, complexExecutionRule("stopped check source or contract changed")
	}
	if _, err := q.GetClearDevStoppedCheckGrant(ctx, b.EventID); err == nil {
		return offer, p, complexExecutionRule("stopped check recovery already granted")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return offer, p, err
	}
	return offer, p, nil
}

func settleStoppedCheckRecovery(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*core.RuleError, error) {
	offer, p, err := currentStoppedCheckOffer(ctx, q, result, request)
	if err != nil {
		return nil, err
	}
	b := p.state.Binding
	changed, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{ID: request.ID, Decision: string(result.Decision), ResolvedAt: nullableTime(at)})
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, complexExecutionRule("stopped check decision already settled")
	}
	outcome := core.HumanDecisionDispatchRejected
	if result.Decision == core.HumanDecisionApprove {
		outcome = core.HumanDecisionDispatchApproved
		if err := q.InsertClearDevStoppedCheckGrant(ctx, gen.InsertClearDevStoppedCheckGrantParams{EventID: b.EventID, ExecutionRunID: b.ExecutionRunID, DecisionRequestID: request.ID, CreatedAt: at}); err != nil {
			return nil, err
		}
		if err := applyStoppedCheckRecovery(ctx, q, offer, p, at); err != nil {
			return nil, err
		}
	}
	if err := consumeDispatchCAS(ctx, q, result.Nonce, result.DesktopRunID, outcome, at); err != nil {
		return nil, err
	}
	req, err := q.GetClearDevRequirement(ctx, b.DevelopmentRequirementID)
	if err != nil {
		return nil, err
	}
	if err := insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: req.AoProjectID, DevelopmentRequirementID: req.ID, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: request.ID, Action: core.ActionSettleHumanDecision, PreviousState: "PENDING", TargetState: "RESOLVED", Outcome: core.EventAccepted, Source: core.EventSourceHumanDecision, CreatedAt: at}); err != nil {
		return nil, err
	}
	seq, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: request.ID, Action: string(core.ActionSettleHumanDecision)})
	if err != nil {
		return nil, err
	}
	return nil, q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: request.ID, Decision: string(result.Decision), EventSequence: seq, CreatedAt: at})
}

func applyStoppedCheckRecovery(ctx context.Context, q *gen.Queries, offer gen.CleardevStoppedCheckRequest, p stoppedCheckProof, at time.Time) error {
	b := p.state.Binding
	r := core.WorkflowRecovery{ID: offer.ID + ":check", ExecutionRunID: b.ExecutionRunID, Action: core.RecoveryRetryCheck, TargetID: b.CheckRunID, DispatchID: b.DispatchID, TaskID: b.TaskID, StepID: p.attempt.AgentStepID, BindingID: b.BuilderRoleBindingID, SuccessorID: b.RetryCheckRunID, CandidateSHA: b.CandidateSHA, ProviderConversationID: b.ProviderConversationID, OriginalStatus: p.attempt.Status, OriginalReason: p.attempt.ReasonCode, OriginalSummary: p.check.OutputSummary.String, OriginalStoppedAt: p.attempt.SettledAt.Time, Supplement: offer.Supplement, CreatedAt: at}
	if err := insertWorkflowRecovery(ctx, q, r); err != nil {
		return err
	}
	if err := q.InsertClearDevComplexExecutionCheckRun(ctx, gen.InsertClearDevComplexExecutionCheckRunParams{ID: b.RetryCheckRunID, CheckSpecID: p.check.CheckSpecID, TaskAttemptID: b.DispatchID, CandidateCommitID: p.check.CandidateCommitID, CandidateCommitSha: b.CandidateSHA, Status: "PENDING", RetryOrdinal: 1, CreatedAt: at}); err != nil {
		return err
	}
	changed, err := q.AdvanceClearDevComplexExecutionTaskAttemptCAS(ctx, gen.AdvanceClearDevComplexExecutionTaskAttemptCASParams{ID: b.DispatchID, ExpectedStatus: p.attempt.Status, Status: "OBSERVED", ReasonCode: ""})
	if err != nil {
		return err
	}
	if changed != 1 {
		return complexExecutionRule("stopped check attempt changed before restore")
	}
	changed, err = q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{ID: p.item.ID, ExpectedState: p.item.State, ExpectedReworkCount: p.item.ReworkCount, NextState: "RUNNING", ReworkCount: p.item.ReworkCount, UpdatedAt: at})
	if err != nil {
		return err
	}
	if changed != 1 {
		return complexExecutionRule("stopped check task changed before restore")
	}
	return nil
}
