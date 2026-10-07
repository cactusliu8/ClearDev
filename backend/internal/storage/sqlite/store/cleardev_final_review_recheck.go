package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func finalRecheckRequestID(run string) string { return "final-review-recheck:" + run }

// prepareFinalRecheck adds executor evidence to the original immutable packet.
// It never changes the original task/plan/acceptance or allocates a Builder slot.
func prepareFinalRecheck(ctx context.Context, q *gen.Queries, row gen.CleardevRequirementFinalReview, at time.Time) (core.RequirementFinalReview, core.FinalReviewRecheckBinding, error) {
	var b core.FinalReviewRecheckBinding
	old, err := requirementFinalReviewFromGen(row)
	if err != nil {
		return old, b, err
	}
	// This narrow evidence-only recovery cannot coexist with a direction intent.
	if _, err := q.GetClearDevDirectionIntentByVersion(ctx, old.RequirementVersionID); err == nil {
		return old, b, errors.New("direction intent excludes final evidence recheck")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return old, b, err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, old.ExecutionRunID)
	if err != nil {
		return old, b, err
	}
	if err := validateFinalReviewPrerequisites(ctx, q, run, old); err != nil {
		return old, b, err
	}
	slots, err := mailAttemptSlots(ctx, q, run.ID)
	if err != nil {
		return old, b, err
	}
	return buildFinalRecheckEvidence(old, slots, at)
}

// buildFinalRecheckEvidence validates immutable provenance independently of live
// recovery eligibility. Completed transaction replays must not require ACCEPTED.
func buildFinalRecheckEvidence(old core.RequirementFinalReview, slots []core.MailAttemptSlot, at time.Time) (core.RequirementFinalReview, core.FinalReviewRecheckBinding, error) {
	var b core.FinalReviewRecheckBinding
	var packet core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(old.ReviewPacketJSON), &packet); err != nil {
		return old, b, err
	}
	if old.Status != "SETTLED" || old.Verdict != "NEEDS_HUMAN" || packet.AttemptEvidence != nil || packet.PreviousReview != nil || !core.BoundedMailAttempts(packet.Run) || len(packet.Tasks) != 1 {
		return old, b, errors.New("final review not eligible for evidence recheck")
	}
	if len(slots) == 0 {
		return old, b, errors.New("missing executor attempt evidence")
	}
	packet.AttemptEvidence = core.NewFinalReviewAttemptEvidence(slots)
	evidence, _ := json.Marshal(packet.AttemptEvidence)
	b = core.FinalReviewRecheckBinding{DevelopmentRequirementID: old.DevelopmentRequirementID, ExecutionRunID: old.ExecutionRunID, PreviousReviewID: old.ID, PreviousResultID: old.ResultID, PreviousPacketSHA256: old.ReviewPacketSHA256, CandidateSHA: old.CandidateCommitSHA, AttemptEvidenceSHA256: sha256Hex(string(evidence))}
	requestID := finalRecheckRequestID(old.ExecutionRunID)
	packet.PreviousReview = &core.FinalReviewPriorConclusion{ReviewID: old.ID, ResultID: old.ResultID, PacketSHA256: old.ReviewPacketSHA256, Verdict: old.Verdict, Summary: old.Summary, AOSessionID: old.AOSessionID, WorkspacePath: old.WorkspacePath, AuthorityRequestID: requestID}
	r := old
	r.ID = old.ID + ":evidence-recheck"
	r.PreviousReviewID = old.ID
	r.AuthorityRequestID = requestID
	r.Status = "REQUESTED"
	r.AOSessionID = ""
	r.WorkspacePath = ""
	r.ResultID = ""
	r.Verdict = ""
	r.Summary = ""
	r.ReasonCode = ""
	r.CreatedAt = at
	r.BoundAt = nil
	r.SentAt = nil
	r.SettledAt = nil
	packet.ReviewID = r.ID
	raw, err := json.Marshal(packet)
	if err != nil {
		return r, b, err
	}
	r.ReviewPacketJSON = string(raw)
	r.ReviewPacketSHA256 = sha256Hex(string(raw))
	r.PromptSHA256 = sha256Hex(core.RequirementFinalReviewPrompt(r))
	return r, b, nil
}

func ensureFinalRecheckRequests(ctx context.Context, q *gen.Queries, at time.Time) error {
	rows, err := q.ListClearDevFinalReviewRecheckCandidates(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		id := finalRecheckRequestID(row.ExecutionRunID)
		if _, err := q.GetClearDevHumanDecisionRequest(ctx, id); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, b, err := prepareFinalRecheck(ctx, q, row, at)
		if err != nil {
			continue
		} // ineligible histories remain stopped
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		display := core.HumanDecisionDisplay{Title: "补充证据并复核最终候选", Summary: "授权原最终 Reviewer 会话，对同一候选再审核一次。", FullContent: fmt.Sprintf("需求：%s\n候选：%s\n原审核：%s\n原结论：%s\n补充可信执行器的尝试分类与次数规则。保留原 NEEDS_HUMAN 与全部失败，标准、候选和检查不变。仍须独立 Reviewer 判断；不保证 PASS。", b.DevelopmentRequirementID, b.CandidateSHA, b.PreviousReviewID, row.Summary), ChangeSummary: "仅一次补证复核；不增加 Builder 次数，不改变验收标准。再次不通过则停止。"}
		draw, err := json.Marshal(display)
		if err != nil {
			return err
		}
		digest, err := core.HumanDecisionContentSHA256(core.HumanDecisionKindFinalReviewRecheck, raw, display)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{ID: id, DevelopmentProjectID: b.DevelopmentRequirementID, DecisionKind: core.HumanDecisionKindFinalReviewRecheck, BindingSchemaVersion: 1, BindingJson: string(raw), DisplayJson: string(draw), ContentSha256: digest, CreatedAt: at}); err != nil {
			return err
		}
	}
	return nil
}

func settleFinalRecheck(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*core.RuleError, error) {
	b, err := core.ParseFinalReviewRecheckBinding(result.Binding)
	if err != nil {
		return nil, err
	}
	if request.ID != finalRecheckRequestID(b.ExecutionRunID) {
		return nil, errors.New("invalid final recheck request")
	}
	row, err := q.GetClearDevRequirementFinalReview(ctx, b.ExecutionRunID)
	if err != nil {
		return nil, err
	}
	r, current, err := prepareFinalRecheck(ctx, q, row, at)
	if err != nil {
		return nil, err
	}
	if current != b {
		return nil, errors.New("final recheck evidence changed since native offer")
	}
	changed, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{ID: request.ID, Decision: string(result.Decision), ResolvedAt: nullableTime(at)})
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errors.New("final recheck already decided")
	}
	outcome := core.HumanDecisionDispatchRejected
	if result.Decision == core.HumanDecisionApprove {
		outcome = core.HumanDecisionDispatchApproved
		checks, err := json.Marshal(r.CheckRunIDs)
		if err != nil {
			return nil, err
		}
		err = q.InsertClearDevFinalReviewRecheck(ctx, gen.InsertClearDevFinalReviewRecheckParams{ID: r.ID, ExecutionRunID: r.ExecutionRunID, DevelopmentProjectID: r.DevelopmentRequirementID, RequirementVersionID: r.RequirementVersionID, RequirementSha256: r.RequirementSHA256, PlanID: r.PlanID, PlanSha256: r.PlanSHA256, CandidateCommitSha: r.CandidateCommitSHA, BaseCommitSha: r.BaseCommitSHA, SourceWorkspacePath: r.SourceWorkspacePath, ReviewPacketJson: r.ReviewPacketJSON, ReviewPacketSha256: r.ReviewPacketSHA256, PromptSha256: r.PromptSHA256, CheckRunIdsJson: string(checks), CreatedAt: at})
		if err != nil {
			return nil, err
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
	seq, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: request.ID, Action: string(core.ActionSettleHumanDecision)})
	if err != nil {
		return nil, err
	}
	return nil, q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: request.ID, Decision: string(result.Decision), EventSequence: seq, CreatedAt: at})
}

func validateFinalAttemptEvidence(ctx context.Context, q *gen.Queries, r core.RequirementFinalReview) error {
	var p core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(r.ReviewPacketJSON), &p); err != nil {
		return err
	}
	if p.AttemptEvidence != nil {
		if !core.BoundedMailAttempts(p.Run) {
			return errors.New("attempt evidence without frozen policy")
		}
		slots, err := mailAttemptSlots(ctx, q, r.ExecutionRunID)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(core.NewFinalReviewAttemptEvidence(slots))
		given, _ := json.Marshal(p.AttemptEvidence)
		if !bytes.Equal(raw, given) {
			return errors.New("final review attempt evidence differs from executor ledger")
		}
	}
	if r.PreviousReviewID == "" {
		if p.PreviousReview != nil || r.AuthorityRequestID != "" {
			return errors.New("unbound prior review evidence")
		}
		return nil
	}
	old, err := q.GetClearDevRequirementFinalReviewByID(ctx, r.PreviousReviewID)
	if err != nil {
		return err
	}
	prior, err := requirementFinalReviewFromGen(old)
	if err != nil {
		return err
	}
	slots, err := mailAttemptSlots(ctx, q, r.ExecutionRunID)
	if err != nil {
		return err
	}
	expected, b, err := buildFinalRecheckEvidence(prior, slots, r.CreatedAt)
	if err != nil {
		return err
	}
	if r.ID != expected.ID || r.AuthorityRequestID != expected.AuthorityRequestID || r.ReviewPacketJSON != expected.ReviewPacketJSON || r.PromptSHA256 != expected.PromptSHA256 {
		return errors.New("final recheck changed original candidate or supplement")
	}
	request, err := q.GetClearDevHumanDecisionRequest(ctx, r.AuthorityRequestID)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(b)
	if request.Status != "RESOLVED" || request.Decision != "APPROVE" || request.BindingJson != string(raw) || request.DecisionKind != core.HumanDecisionKindFinalReviewRecheck {
		return errors.New("final recheck lacks exact native authority")
	}
	return nil
}
