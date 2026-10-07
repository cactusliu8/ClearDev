package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// RequestClearDevExtraCoordination creates a pending native decision only.
// Neither this transaction nor an idempotent replay sends a Planner message.
func (s *Store) RequestClearDevExtraCoordination(ctx context.Context, id, supplement string, b core.ExtraCoordinationBinding, at time.Time) error {
	if id == "" || len(id) > 100 || strings.TrimSpace(id) != id || strings.TrimSpace(supplement) == "" || len(supplement) > 16000 {
		return complexExecutionRule("invalid extra coordination request")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "request one extra Planner coordination", func(q *gen.Queries) error {
		if old, err := q.GetClearDevExtraCoordinationRequest(ctx, id); err == nil {
			if old.Supplement != supplement || !sameExtraCoordinationBinding(old.BindingJson, b) {
				return complexExecutionRule("extra coordination request changed on replay")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p, err := extraCoordinationEvidence(ctx, q, b.DevelopmentRequirementID, false)
		if err != nil {
			return err
		}
		if p.state.Option.Action == "" || p.state.Option.UnavailableReason != "" || p.state.Binding != b {
			return complexExecutionRule("extra coordination is no longer current")
		}
		if _, err := q.GetClearDevExtraCoordinationRequestForRun(ctx, b.ExecutionRunID); err == nil {
			return complexExecutionRule("an extra coordination decision already exists for this run")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		binding, err := json.Marshal(b)
		if err != nil {
			return err
		}
		if _, err := core.ParseExtraCoordinationBinding(binding); err != nil {
			return err
		}
		humanID := uuid.NewString()
		display := core.HumanDecisionDisplay{
			Title:         "授权一次额外工程协调（第 3 轮）",
			Summary:       "原两次协调已消耗；只允许原 Planner 对当前上限事件再作一次工程判断。",
			FullContent:   "批准只开放本事件的第3次业务协调，仍使用原Planner、冻结候选和上下文。原STOP/LIMIT、两次请求、消息预留和失败历史全部保留。不会增加Builder/Reviewer轮次、任务返工次数或工程修订上限2；每步消息仍按原有界规则。协调失败不退款，没有第4次。批准不代表检查、审核或ACC-005通过；需要其它资源或范围时仍单独停人。拒绝保持停止；稍后不授权也不发送。",
			ChangeSummary: "仅事件 " + b.EventID + "；原会话 " + b.AOSessionID + "；上下文 " + b.ContextSHA256 + "。追加一次协调，其它额度不变。",
		}
		display.FullContent += "\n\n涉及任务：" + strings.Join(p.event.Report.AffectedTaskKeys, ", ") + "\nBuilder观察（不构成许可或可信检查）：\n" + p.event.Report.Summary
		displayJSON, err := json.Marshal(display)
		if err != nil {
			return err
		}
		digest, err := core.HumanDecisionContentSHA256(core.HumanDecisionKindExtraCoordination, binding, display)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{
			ID: humanID, DevelopmentProjectID: b.DevelopmentRequirementID, DecisionKind: core.HumanDecisionKindExtraCoordination, BindingSchemaVersion: 1,
			BindingJson: string(binding), DisplayJson: string(displayJSON), ContentSha256: digest, CreatedAt: at,
		}); err != nil {
			return err
		}
		prompt := core.BuildExtraPlannerRuntimePrompt(p.event, p.contextJSON, b.ContextSHA256, humanID)
		if err := q.InsertClearDevExtraCoordinationRequest(ctx, gen.InsertClearDevExtraCoordinationRequestParams{
			ID: id, ExecutionRunID: b.ExecutionRunID, EventID: b.EventID, DecisionRequestID: humanID, BindingJson: string(binding), ContextJson: p.contextJSON,
			Prompt: prompt, PromptSha256: complexExecutionRawDigest([]byte(prompt)), Supplement: supplement, CreatedAt: at,
		}); err != nil {
			return err
		}
		req, err := q.GetClearDevRequirement(ctx, b.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		return insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: req.AoProjectID, DevelopmentRequirementID: req.ID, SubjectType: core.SubjectHumanDecisionRequest,
			SubjectID: humanID, Action: core.ActionCreateHumanDecisionRequest, TargetState: "PENDING", Outcome: core.EventAccepted, Source: core.EventSourceControlPlane, CreatedAt: at})
	})
}

func validateReopenExtraCoordination(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest) error {
	b, err := core.ParseExtraCoordinationBinding(result.Binding)
	if err != nil {
		return err
	}
	offer, err := q.GetClearDevExtraCoordinationRequestForEvent(ctx, b.EventID)
	if err != nil {
		return err
	}
	if offer.DecisionRequestID != request.ID || offer.BindingJson != request.BindingJson || !sameExtraCoordinationBinding(offer.BindingJson, b) {
		return complexExecutionRule("extra coordination human binding changed")
	}
	p, err := extraCoordinationEvidence(ctx, q, b.DevelopmentRequirementID, false)
	if err != nil {
		return err
	}
	if p.state.Binding != b || p.state.Option.UnavailableReason != "" || p.contextJSON != offer.ContextJson {
		return complexExecutionRule("extra coordination context or original identity changed")
	}
	if _, err := q.GetClearDevExtraCoordinationGrant(ctx, b.EventID); err == nil {
		return complexExecutionRule("extra coordination was already granted")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func settleExtraCoordination(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*core.RuleError, error) {
	if err := validateReopenExtraCoordination(ctx, q, result, request); err != nil {
		return nil, err
	}
	b, err := core.ParseExtraCoordinationBinding(result.Binding)
	if err != nil {
		return nil, err
	}
	changed, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{ID: request.ID, Decision: string(result.Decision), ResolvedAt: nullableTime(at)})
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, complexExecutionRule("extra coordination decision already settled")
	}
	outcome := core.HumanDecisionDispatchRejected
	if result.Decision == core.HumanDecisionApprove {
		outcome = core.HumanDecisionDispatchApproved
		if err := q.InsertClearDevExtraCoordinationGrant(ctx, gen.InsertClearDevExtraCoordinationGrantParams{EventID: b.EventID, ExecutionRunID: b.ExecutionRunID, DecisionRequestID: request.ID, CreatedAt: at}); err != nil {
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
	if err := insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: req.AoProjectID, DevelopmentRequirementID: req.ID, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: request.ID,
		Action: core.ActionSettleHumanDecision, PreviousState: "PENDING", TargetState: "RESOLVED", Outcome: core.EventAccepted, Source: core.EventSourceHumanDecision, CreatedAt: at}); err != nil {
		return nil, err
	}
	seq, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: request.ID, Action: string(core.ActionSettleHumanDecision)})
	if err != nil {
		return nil, err
	}
	return nil, q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: request.ID, Decision: string(result.Decision), EventSequence: seq, CreatedAt: at})
}
