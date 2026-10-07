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

func productPlanBinding(ctx context.Context, q *gen.Queries, productID, discussionID string) (core.ProductPlanBinding, error) {
	var b core.ProductPlanBinding
	if err := rejectActiveSourcePreparation(ctx, q, productID); err != nil {
		return b, err
	}
	rounds, e := q.ListClearDevProductDiscussions(ctx, productID)
	if e != nil {
		return b, e
	}
	if len(rounds) == 0 || rounds[len(rounds)-1].ID != discussionID {
		return b, productConflict("product plan was superseded")
	}
	d := rounds[len(rounds)-1]
	var result core.ProductDiscoveryResult
	if !d.SettledAt.Valid || !d.ResultJson.Valid || json.Unmarshal([]byte(d.ResultJson.String), &result) != nil || result.Outcome != "READY" || len(result.Stages) == 0 {
		return b, productConflict("product plan is not ready")
	}
	for _, s := range result.Stages {
		if s.ExecutionBasis == nil {
			return b, productConflict("only generic project plans support automatic progression")
		}
	}
	parent, e := q.GetClearDevRequirement(ctx, productID)
	if e != nil {
		return b, e
	}
	if parent.CancelledAt.Valid || parent.PausedFromState.Valid || parent.State == "PAUSED" {
		return b, productConflict("product is stopped")
	}
	c, e := q.GetClearDevProductDiscussionContext(ctx, discussionID)
	if e != nil {
		return b, e
	}
	if !c.SelectionSha256.Valid {
		return b, productConflict("product selection is missing")
	}
	b = core.ProductPlanBinding{ProductID: productID, DiscussionID: discussionID, ResultSHA256: d.ResultSha256.String, SelectionSHA256: c.SelectionSha256.String}
	return b, nil
}

// RequestClearDevProductPlan requests one native decision without granting execution.
func (s *Store) RequestClearDevProductPlan(ctx context.Context, b core.ProductPlanBinding, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "request product plan authorization", func(q *gen.Queries) error {
		actual, e := productPlanBinding(ctx, q, b.ProductID, b.DiscussionID)
		if e != nil {
			return e
		}
		if actual != b {
			return productConflict("product plan binding changed")
		}
		if _, e := q.GetClearDevProductPlanDecision(ctx, gen.GetClearDevProductPlanDecisionParams{ProductID: b.ProductID, DiscussionID: b.DiscussionID}); e == nil {
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		d, e := q.GetClearDevProductDiscussion(ctx, b.DiscussionID)
		if e != nil {
			return e
		}
		parent, e := q.GetClearDevRequirement(ctx, b.ProductID)
		if e != nil {
			return e
		}
		selection, e := q.GetClearDevProductDiscussionContext(ctx, b.DiscussionID)
		if e != nil {
			return e
		}
		fullContent, e := json.Marshal(struct {
			Plan            json.RawMessage `json:"plan"`
			SelectedProject json.RawMessage `json:"selectedProject"`
		}{json.RawMessage(d.ResultJson.String), json.RawMessage(selection.SelectionJson.String)})
		if e != nil {
			return e
		}
		display := core.HumanDecisionDisplay{Title: "确认整体计划并自动逐阶段开发", Summary: "只在下面已列明的阶段、功能验收和非目标内自动推进。每阶段仍须真实独立验收；新增功能或改变目标和验收时再次询问。", FullContent: string(fullContent), ChangeSummary: "一次确认后无需逐阶段批准。已完成阶段保留；未完成阶段依次使用上一阶段的准确交付，不重置预算。"}
		raw, e := json.Marshal(b)
		if e != nil {
			return e
		}
		shown, e := json.Marshal(display)
		if e != nil {
			return e
		}
		digest, e := core.HumanDecisionContentSHA256(core.HumanDecisionKindProductPlan, raw, display)
		if e != nil {
			return e
		}
		id := "product-plan:" + b.DiscussionID
		if e := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{ID: id, DevelopmentProjectID: b.ProductID, DecisionKind: core.HumanDecisionKindProductPlan, BindingSchemaVersion: 1, BindingJson: string(raw), DisplayJson: string(shown), ContentSha256: digest, CreatedAt: at}); e != nil {
			return e
		}
		return insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: parent.AoProjectID, DevelopmentRequirementID: parent.ID, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: id, Action: core.ActionCreateHumanDecisionRequest, TargetState: "PENDING", Outcome: core.EventAccepted, Source: core.EventSourceControlPlane, CreatedAt: at})
	})
}
func settleProductPlan(ctx context.Context, q *gen.Queries, r core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*core.RuleError, error) {
	b, e := core.ParseProductPlanBinding(r.Binding)
	if e != nil {
		return nil, e
	}
	actual, e := productPlanBinding(ctx, q, b.ProductID, b.DiscussionID)
	if e != nil {
		return nil, e
	}
	if actual != b || request.DevelopmentProjectID != b.ProductID {
		return nil, productConflict("plan authorization changed")
	}
	n, e := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{ID: request.ID, Decision: string(r.Decision), ResolvedAt: nullableTime(at)})
	if e != nil {
		return nil, e
	}
	if n != 1 {
		return nil, productConflict("plan already decided")
	}
	outcome := core.HumanDecisionDispatchRejected
	if r.Decision == core.HumanDecisionApprove {
		outcome = core.HumanDecisionDispatchApproved
	}
	if e := consumeDispatchCAS(ctx, q, r.Nonce, r.DesktopRunID, outcome, at); e != nil {
		return nil, e
	}
	parent, e := q.GetClearDevRequirement(ctx, b.ProductID)
	if e != nil {
		return nil, e
	}
	if e := insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: parent.AoProjectID, DevelopmentRequirementID: b.ProductID, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: request.ID, Action: core.ActionSettleHumanDecision, PreviousState: "PENDING", TargetState: "RESOLVED", Outcome: core.EventAccepted, Source: core.EventSourceHumanDecision, CreatedAt: at}); e != nil {
		return nil, e
	}
	seq, e := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: request.ID, Action: string(core.ActionSettleHumanDecision)})
	if e != nil {
		return nil, e
	}
	return nil, q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: request.ID, Decision: string(r.Decision), EventSequence: seq, CreatedAt: at})
}

// GetClearDevProductPlanAuthorization reads current authority without advancing work.
func (s *Store) GetClearDevProductPlanAuthorization(ctx context.Context, productID, discussionID string) (*core.ProductPlanAuthorization, error) {
	r, e := s.qr.GetClearDevProductPlanDecision(ctx, gen.GetClearDevProductPlanDecisionParams{ProductID: productID, DiscussionID: discussionID})
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	b, e := core.ParseProductPlanBinding([]byte(r.BindingJson))
	if e != nil {
		return nil, e
	}
	status := r.Status
	if r.Decision == "APPROVE" {
		if _, e := s.qr.GetClearDevApprovedProductPlan(ctx, discussionID); errors.Is(e, sql.ErrNoRows) {
			status = "STALE"
		} else if e != nil {
			return nil, e
		}
	}
	out := &core.ProductPlanAuthorization{RequestID: r.ID, Binding: b, Status: status, Decision: r.Decision}
	events, e := s.qr.ListClearDevRequirementEvents(ctx, productID)
	if e != nil {
		return nil, e
	}
	for _, event := range events {
		if event.SubjectID == r.ID && event.Action == "ADVANCE_PRODUCT_PLAN" {
			out.ReasonCode = event.ReasonCode
			out.Reason = event.ReasonText
		}
	}
	return out, nil
}

// ListClearDevAutomaticProducts lists only current approved products for boot resume.
func (s *Store) ListClearDevAutomaticProducts(ctx context.Context) ([]string, error) {
	rows, e := s.qr.ListClearDevApprovedProductPlans(ctx)
	if e != nil {
		return nil, e
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ProductID)
	}
	return ids, nil
}
func loadStageSelection(ctx context.Context, q *gen.Queries, stageID string) (*core.ProductSelection, error) {
	row, e := q.GetClearDevEffectiveStageContext(ctx, stageID)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if row.SelectionJson == "" {
		return nil, nil
	}
	if !requirementDigestMatches(row.SelectionJson, row.SelectionSha256) {
		return nil, productConflict("stage source digest mismatch")
	}
	var selection core.ProductSelection
	if e := json.Unmarshal([]byte(row.SelectionJson), &selection); e != nil {
		return nil, e
	}
	if e := core.ValidateProductDeliveryBaseline(selection); e != nil {
		return nil, e
	}
	return &selection, nil
}

// BindClearDevAutomaticStageSource freezes the exact preceding final delivery.
func (s *Store) BindClearDevAutomaticStageSource(ctx context.Context, stageID, authorizationID string, selection core.ProductSelection, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "freeze automatic stage source", func(q *gen.Queries) error {
		raw, e := json.Marshal(selection)
		if e != nil {
			return e
		}
		if old, e := q.GetClearDevProductStageSource(ctx, stageID); e == nil {
			var saved core.ProductSelection
			if json.Unmarshal([]byte(old.SelectionJson), &saved) != nil {
				return productConflict("invalid saved source")
			}
			saved.CreatedAt = selection.CreatedAt
			oldRaw, _ := json.Marshal(saved)
			if old.AuthorizationID != authorizationID || !bytes.Equal(oldRaw, raw) {
				return productConflict("stage source already frozen differently")
			}
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		return q.InsertClearDevProductStageSource(ctx, gen.InsertClearDevProductStageSourceParams{StageID: stageID, AuthorizationID: authorizationID, SelectionJson: string(raw), SelectionSha256: complexExecutionRawDigest(raw), CreatedAt: at})
	})
}

// ConfirmClearDevAutomaticStage confirms unchanged scope under the original whole-plan grant.
func (s *Store) ConfirmClearDevAutomaticStage(ctx context.Context, requirementID, versionID string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "confirm stage under approved product plan", func(q *gen.Queries) error {
		stage, e := projectPlanningStage(ctx, q, requirementID)
		if e != nil {
			return e
		}
		grant, e := q.GetClearDevApprovedProductPlan(ctx, stage.DiscussionID)
		if e != nil {
			return e
		}
		parent, e := q.GetClearDevRequirement(ctx, requirementID)
		if e != nil {
			return e
		}
		if parent.CancelledAt.Valid || parent.PausedFromState.Valid || parent.State == "PAUSED" {
			return productConflict("stage is stopped")
		}
		v, e := q.GetClearDevRequirementVersion(ctx, versionID)
		if e != nil {
			return e
		}
		if v.DevelopmentProjectID != requirementID || v.TaskSetVersion != 0 {
			return productConflict("stage version changed")
		}
		if v.State == "APPROVED" {
			return nil
		}
		if v.State != "IN_REVIEW" {
			return productConflict("stage is not awaiting confirmation")
		}
		// Existing per-stage offers remain unchanged history. The control-plane
		// confirmation below cites the later whole-plan grant, never a fake human effect.
		if _, e := q.GetActiveClearDevDirectionStopGateByVersion(ctx, versionID); e == nil {
			return productConflict("stage direction is stopped")
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var doc core.NormalizedRequirementDocument
		if !requirementDigestMatches(v.ContractText, v.Sha256) || json.Unmarshal([]byte(v.ContractText), &doc) != nil || !core.ProductPlanCoversDocument(stage.Definition, doc) {
			return productConflict("compiled stage changes approved product acceptance")
		}
		n, e := q.UpdateClearDevRequirementVersionStateCAS(ctx, gen.UpdateClearDevRequirementVersionStateCASParams{ID: versionID, ExpectedState: "IN_REVIEW", NextState: "APPROVED", ApprovedAt: nullableTime(at)})
		if e != nil {
			return e
		}
		if n != 1 {
			return productConflict("stage confirmation changed")
		}
		return insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: parent.AoProjectID, DevelopmentRequirementID: requirementID, SubjectType: core.SubjectRequirementVersion, SubjectID: versionID, Action: core.ActionConfirmRequirementVersion, PreviousState: string(core.RequirementVersionStatusPendingConfirmation), TargetState: string(core.RequirementVersionStatusConfirmed), Outcome: core.EventAccepted, Source: core.EventSourceControlPlane, ReasonText: "Approved product plan: " + grant.RequestID, CreatedAt: at})
	})
}

// RecordClearDevProductPlanAdvance retains stop information in the existing event history.
func (s *Store) RecordClearDevProductPlanAdvance(ctx context.Context, requestID, code, message string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record product progression", func(q *gen.Queries) error {
		request, e := q.GetClearDevHumanDecisionRequest(ctx, requestID)
		if e != nil {
			return e
		}
		if request.DecisionKind != core.HumanDecisionKindProductPlan {
			return productConflict("not a product plan")
		}
		events, e := q.ListClearDevRequirementEvents(ctx, request.DevelopmentProjectID)
		if e != nil {
			return e
		}
		for i := len(events) - 1; i >= 0; i-- {
			event := events[i]
			if event.SubjectID == requestID && event.Action == "ADVANCE_PRODUCT_PLAN" {
				if event.ReasonCode == code && event.ReasonText == message {
					return nil
				}
				break
			}
		}
		parent, e := q.GetClearDevRequirement(ctx, request.DevelopmentProjectID)
		if e != nil {
			return e
		}
		return insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: parent.AoProjectID, DevelopmentRequirementID: parent.ID, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: requestID, Action: core.Action("ADVANCE_PRODUCT_PLAN"), Outcome: core.EventAccepted, Source: core.EventSourceControlPlane, Reason: core.ReasonCode(code), ReasonText: message, CreatedAt: at})
	})
}

func productPlanDecisionCurrent(ctx context.Context, q *gen.Queries, request gen.CleardevHumanDecisionRequest) (bool, error) {
	if request.DecisionKind == core.HumanDecisionKindProductPlan {
		binding, e := core.ParseProductPlanBinding([]byte(request.BindingJson))
		if e != nil {
			return false, e
		}
		actual, e := productPlanBinding(ctx, q, binding.ProductID, binding.DiscussionID)
		if e != nil {
			var conflict *core.RuleError
			if errors.As(e, &conflict) {
				return false, nil
			}
			return false, e
		}
		return actual == binding, nil
	}
	if request.DecisionKind == core.HumanDecisionKindConfirmVersion {
		stage, e := q.GetClearDevProductStageByRequirement(ctx, nullableString(request.DevelopmentProjectID))
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return false, e
		}
		if e == nil {
			if _, e = q.GetClearDevApprovedProductPlan(ctx, stage.DiscussionID); e == nil {
				binding, e := core.ParseConfirmRequirementBinding([]byte(request.BindingJson))
				if e != nil {
					return false, e
				}
				version, e := q.GetClearDevRequirementVersion(ctx, binding.RequirementVersionID)
				if e != nil {
					return false, e
				}
				var definition core.ProductStageDefinition
				var doc core.NormalizedRequirementDocument
				if json.Unmarshal([]byte(stage.DefinitionJson), &definition) == nil && json.Unmarshal([]byte(version.ContractText), &doc) == nil && core.ProductPlanCoversDocument(definition, doc) {
					return false, nil
				}
			} else if !errors.Is(e, sql.ErrNoRows) {
				return false, e
			}
		}
	}
	return projectDecisionCurrent(ctx, q, request.DevelopmentProjectID)
}
