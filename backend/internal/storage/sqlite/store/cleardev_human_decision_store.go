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

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func (s *Store) BackfillClearDevHumanDecisionRequests(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "backfill ClearDev human decision requests", func(q *gen.Queries) error {
		versions, err := q.ListClearDevPendingConfirmationVersionsMissingHumanDecision(ctx)
		if err != nil {
			return err
		}
		for _, row := range versions {
			version := clearDevRequirementVersionFromGen(row)
			requirementRow, err := q.GetClearDevRequirement(ctx, version.DevelopmentRequirementID)
			if err != nil {
				return err
			}
			if err := ensureConfirmHumanDecisionRequest(ctx, q, clearDevRequirementFromGen(requirementRow), version, version.CreatedAt); err != nil {
				return err
			}
		}
		if err := ensurePlanningRecoveryRequests(ctx, q, time.Now().UTC()); err != nil {
			return err
		}
		if err := ensureFinalRecheckRequests(ctx, q, time.Now().UTC()); err != nil {
			return err
		}
		return ensureCoordinationRepairRequests(ctx, q, time.Now().UTC())
	})
}

func (s *Store) ListPendingClearDevHumanDecisionRequests(ctx context.Context) ([]cleardev.HumanDecisionRequest, error) {
	rows, err := s.qr.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cleardev.HumanDecisionRequest, 0, len(rows))
	for _, row := range rows {
		current, err := productPlanDecisionCurrent(ctx, s.qr, row)
		if err != nil {
			return nil, err
		}
		if current {
			superseded, err := supersededLegacyEngineChange(ctx, s.qr, row)
			if err != nil {
				return nil, err
			}
			if superseded {
				continue
			}
			out = append(out, humanDecisionRequestFromGen(row))
		}
	}
	return out, nil
}

func (s *Store) GetClearDevHumanDecisionRequest(ctx context.Context, id string) (cleardev.HumanDecisionRequest, bool, error) {
	row, err := s.qr.GetClearDevHumanDecisionRequest(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.HumanDecisionRequest{}, false, nil
	}
	if err != nil {
		return cleardev.HumanDecisionRequest{}, false, err
	}
	return humanDecisionRequestFromGen(row), true, nil
}

func (s *Store) GetClearDevHumanDecisionEffect(ctx context.Context, requestID string) (cleardev.HumanDecisionEffect, bool, error) {
	row, err := s.qr.GetClearDevHumanDecisionEffect(ctx, requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.HumanDecisionEffect{}, false, nil
	}
	if err != nil {
		return cleardev.HumanDecisionEffect{}, false, err
	}
	return cleardev.HumanDecisionEffect{
		RequestID: row.RequestID, Decision: cleardev.HumanDecisionChoice(row.Decision),
		EventSequence: row.EventSequence, CreatedAt: row.CreatedAt,
	}, true, nil
}

func (s *Store) IssueClearDevHumanDecisionDispatch(ctx context.Context, command cleardev.IssueHumanDecisionDispatchCommand) (cleardev.HumanDecisionOffer, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var offer cleardev.HumanDecisionOffer
	err := s.inTx(ctx, "issue ClearDev human decision dispatch", func(q *gen.Queries) error {
		built, err := issueHumanDecisionDispatch(ctx, q, command)
		if err != nil {
			return err
		}
		offer = built
		return nil
	})
	return offer, err
}

func (s *Store) DismissClearDevHumanDecisionDispatch(ctx context.Context, command cleardev.DismissHumanDecisionDispatchCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "dismiss ClearDev human decision dispatch", func(q *gen.Queries) error {
		return consumeHumanDecisionDispatch(ctx, q, command.Nonce, command.DesktopRunID, command.Outcome, command.At)
	})
}

func (s *Store) DismissOpenClearDevHumanDecisionDispatches(ctx context.Context, desktopRunID string, outcome cleardev.HumanDecisionDispatchOutcome, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "dismiss open ClearDev human decision dispatches", func(q *gen.Queries) error {
		rows, err := q.ListOpenClearDevHumanDecisionDispatchesForDesktop(ctx, desktopRunID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if _, err := q.ConsumeClearDevHumanDecisionDispatchCAS(ctx, gen.ConsumeClearDevHumanDecisionDispatchCASParams{
				ConsumedAt: sql.NullTime{Time: at, Valid: true}, Outcome: string(outcome),
				NonceSha256: row.NonceSha256, DesktopRunID: desktopRunID,
			}); err != nil {
				return err
			}
			request, err := q.GetClearDevHumanDecisionRequest(ctx, row.RequestID)
			if err != nil {
				return err
			}
			requirement, err := q.GetClearDevRequirement(ctx, request.DevelopmentProjectID)
			if err != nil {
				return err
			}
			if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
				AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
				SubjectType: cleardev.SubjectHumanDecisionDispatch, SubjectID: row.ID,
				Action: cleardev.ActionDismissHumanDecisionDispatch, Outcome: cleardev.EventAccepted,
				ReasonText: string(outcome), Source: cleardev.EventSourceHumanDecision, CreatedAt: at,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) SettleClearDevHumanDecision(ctx context.Context, result cleardev.HumanDecisionResult, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "settle ClearDev human decision", func(q *gen.Queries) error {
		rejected, err := settleHumanDecision(ctx, q, result, at)
		if err != nil {
			return err
		}
		if rejected != nil {
			return rejected
		}
		return nil
	})
}

func issueHumanDecisionDispatch(ctx context.Context, q *gen.Queries, command cleardev.IssueHumanDecisionDispatchCommand) (cleardev.HumanDecisionOffer, error) {
	requestRow, err := q.GetClearDevHumanDecisionRequest(ctx, command.RequestID)
	if err != nil {
		return cleardev.HumanDecisionOffer{}, err
	}
	if requestRow.Status != string(cleardev.HumanDecisionRequestPending) {
		return cleardev.HumanDecisionOffer{}, errors.New("human decision request is not pending")
	}
	current, err := productPlanDecisionCurrent(ctx, q, requestRow)
	if err != nil {
		return cleardev.HumanDecisionOffer{}, err
	}
	if !current {
		return cleardev.HumanDecisionOffer{}, productConflict("a newer product discussion superseded this decision")
	}
	if command.InitialOnly || command.ReopenRequestID != "" || requestRow.DecisionKind == cleardev.HumanDecisionKindBuilderReplacement {
		if err := validateDecisionReopen(ctx, q, requestRow, command.IssuedAt); err != nil {
			return cleardev.HumanDecisionOffer{}, err
		}
	}
	if err := expireOpenHumanDecisionDispatches(ctx, q, requestRow, command.IssuedAt); err != nil {
		return cleardev.HumanDecisionOffer{}, err
	}
	open, err := q.ListOpenClearDevHumanDecisionDispatchesForRequest(ctx, requestRow.ID)
	if err != nil {
		return cleardev.HumanDecisionOffer{}, err
	}
	for _, row := range open {
		if row.DesktopRunID == command.DesktopRunID {
			return cleardev.HumanDecisionOffer{}, errors.New("an open human decision dispatch already exists for this desktop")
		}
	}
	nonceSHA, err := cleardev.HumanDecisionNonceSHA256(command.Nonce)
	if err != nil {
		return cleardev.HumanDecisionOffer{}, err
	}
	if command.InitialOnly {
		history, err := q.ListClearDevHumanDecisionDispatchHistory(ctx, requestRow.ID)
		if err != nil {
			return cleardev.HumanDecisionOffer{}, err
		}
		for _, d := range history {
			if d.DesktopRunID == command.DesktopRunID {
				return cleardev.HumanDecisionOffer{}, errors.New("a decision display already exists for this desktop")
			}
		}
	}
	dispatchID := uuid.NewString()
	if command.ReopenRequestID != "" {
		reopen, err := q.GetClearDevHumanDecisionReopen(ctx, command.ReopenRequestID)
		if err != nil {
			return cleardev.HumanDecisionOffer{}, err
		}
		if reopen.DecisionRequestID != requestRow.ID || reopen.DesktopRunID != command.DesktopRunID || reopen.ContentSha256 != requestRow.ContentSha256 {
			return cleardev.HumanDecisionOffer{}, errors.New("decision reopen binding changed")
		}
		history, err := q.ListClearDevHumanDecisionDispatchHistory(ctx, requestRow.ID)
		if err != nil {
			return cleardev.HumanDecisionOffer{}, err
		}
		if len(history) == 0 || history[len(history)-1].ID != reopen.PreviousDispatchID || !history[len(history)-1].ConsumedAt.Valid {
			return cleardev.HumanDecisionOffer{}, errors.New("decision reopen predecessor changed")
		}
		dispatchID = reopen.NextDispatchID
	}
	if err := q.InsertClearDevHumanDecisionDispatch(ctx, gen.InsertClearDevHumanDecisionDispatchParams{
		ID: dispatchID, RequestID: requestRow.ID, DesktopRunID: command.DesktopRunID,
		NonceSha256: nonceSHA, IssuedAt: command.IssuedAt, ExpiresAt: command.ExpiresAt,
	}); err != nil {
		return cleardev.HumanDecisionOffer{}, err
	}
	requirement, err := q.GetClearDevRequirement(ctx, requestRow.DevelopmentProjectID)
	if err != nil {
		return cleardev.HumanDecisionOffer{}, err
	}
	if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectHumanDecisionDispatch, SubjectID: dispatchID,
		Action: cleardev.ActionIssueHumanDecisionDispatch, Outcome: cleardev.EventAccepted,
		Source: cleardev.EventSourceControlPlane, CreatedAt: command.IssuedAt,
	}); err != nil {
		return cleardev.HumanDecisionOffer{}, err
	}
	var display cleardev.HumanDecisionDisplay
	if err := json.Unmarshal([]byte(requestRow.DisplayJson), &display); err != nil {
		return cleardev.HumanDecisionOffer{}, err
	}
	return cleardev.HumanDecisionOffer{
		ProtocolVersion: cleardev.HumanDecisionProtocolVersion, Kind: cleardev.HumanDecisionOfferKind,
		DesktopRunID: command.DesktopRunID, RequestID: requestRow.ID, DecisionKind: requestRow.DecisionKind,
		BindingSchemaVersion: int(requestRow.BindingSchemaVersion), Binding: json.RawMessage(requestRow.BindingJson),
		ContentSHA256: requestRow.ContentSha256, Nonce: command.Nonce,
		ExpiresAt: command.ExpiresAt.UTC().Format(time.RFC3339), Display: display,
	}, nil
}

func settleHumanDecision(ctx context.Context, q *gen.Queries, result cleardev.HumanDecisionResult, at time.Time) (*cleardev.RuleError, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	parsed, err := cleardev.ParseHumanDecisionResult(raw, cleardev.ProductionDecisionRegistry())
	if err != nil {
		return nil, err
	}
	nonceSHA, err := cleardev.HumanDecisionNonceSHA256(parsed.Nonce)
	if err != nil {
		return nil, err
	}
	dispatch, err := q.GetClearDevHumanDecisionDispatchByNonceSHA256(ctx, nonceSHA)
	if err != nil {
		return nil, err
	}
	requestRow, err := q.GetClearDevHumanDecisionRequest(ctx, parsed.RequestID)
	if err != nil {
		return nil, err
	}
	if dispatch.RequestID != requestRow.ID || dispatch.DesktopRunID != parsed.DesktopRunID {
		return nil, errors.New("human decision result is bound to a different request or desktop")
	}
	if dispatch.ConsumedAt.Valid {
		return nil, errors.New("human decision nonce has already been consumed")
	}
	if requestRow.Status != string(cleardev.HumanDecisionRequestPending) {
		if _, err := q.ConsumeClearDevHumanDecisionDispatchCAS(ctx, gen.ConsumeClearDevHumanDecisionDispatchCASParams{
			ConsumedAt: sql.NullTime{Time: at, Valid: true}, Outcome: string(cleardev.HumanDecisionDispatchInvalid),
			NonceSha256: nonceSHA, DesktopRunID: parsed.DesktopRunID,
		}); err != nil {
			return nil, err
		}
		return nil, errors.New("human decision request is already resolved")
	}
	if !at.Before(dispatch.ExpiresAt) {
		if err := consumeHumanDecisionDispatch(ctx, q, parsed.Nonce, parsed.DesktopRunID, cleardev.HumanDecisionDispatchExpired, at); err != nil {
			return nil, err
		}
		return nil, errors.New("human decision offer has expired")
	}
	if parsed.DecisionKind != requestRow.DecisionKind || parsed.BindingSchemaVersion != int(requestRow.BindingSchemaVersion) ||
		parsed.ContentSHA256 != requestRow.ContentSha256 || string(parsed.Binding) != requestRow.BindingJson {
		return nil, errors.New("human decision result does not match the stored request")
	}
	current, err := productPlanDecisionCurrent(ctx, q, requestRow)
	if err != nil {
		return nil, err
	}
	if !current {
		return nil, productConflict("a newer product discussion superseded this decision")
	}
	if parsed.DecisionKind == cleardev.HumanDecisionKindBuilderReplacement {
		if err := validateReopenBuilderReplacement(ctx, q, parsed, requestRow); err != nil {
			return nil, err
		}
	}
	if parsed.Decision == cleardev.HumanDecisionLater {
		return nil, consumeHumanDecisionDispatch(ctx, q, parsed.Nonce, parsed.DesktopRunID, cleardev.HumanDecisionDispatchLater, at)
	}
	switch parsed.DecisionKind {
	case cleardev.HumanDecisionKindProductPlan:
		return settleProductPlan(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindConfirmVersion:
		return settleConfirmRequirementDecision(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindExtraReviewBudget:
		return settleExtraReviewBudget(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindExtraBuilderTurn:
		return settleExtraBuilderTurn(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindStoppedCheckRecovery:
		return settleStoppedCheckRecovery(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindExtraCoordination:
		return settleExtraCoordination(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindCoordinationRepair:
		return settleCoordinationRepair(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindControlledEngineChange:
		return settleControlledEngineChange(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindApproveDirectionChange:
		return settleApproveDirectionChangeDecision(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindFinalReviewRecheck:
		return settleFinalRecheck(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindPlanningRecovery:
		return settlePlanningRecovery(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindExtraPlanningAttempt:
		return settleExtraPlanningAttempt(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindBuilderReplacement:
		return settleBuilderReplacement(ctx, q, parsed, requestRow, at)
	case cleardev.HumanDecisionKindExtraMailAttempt:
		return settleExtraMailAttempt(ctx, q, parsed, requestRow, at)
	default:
		return nil, fmt.Errorf("human decision kind %q is not executable", parsed.DecisionKind)
	}
}

func settleConfirmRequirementDecision(ctx context.Context, q *gen.Queries, parsed cleardev.HumanDecisionResult, requestRow gen.CleardevHumanDecisionRequest, at time.Time) (*cleardev.RuleError, error) {
	binding, err := cleardev.ParseConfirmRequirementBinding(parsed.Binding)
	if err != nil {
		return nil, err
	}
	versionRow, err := q.GetClearDevRequirementVersion(ctx, binding.RequirementVersionID)
	if err != nil {
		return nil, err
	}
	version := clearDevRequirementVersionFromGen(versionRow)
	if version.DevelopmentRequirementID != binding.DevelopmentRequirementID ||
		version.SHA256 != binding.RequirementVersionSHA256 || version.TaskSetVersion != binding.TaskSetVersion ||
		version.Status != cleardev.RequirementVersionStatusPendingConfirmation {
		return nil, errors.New("human decision binding no longer matches the requirement version")
	}
	rows, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{
		Decision: string(parsed.Decision), ResolvedAt: sql.NullTime{Time: at, Valid: true}, ID: requestRow.ID,
	})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, errors.New("human decision request was already settled")
	}
	outcome := cleardev.HumanDecisionDispatchApproved
	action := cleardev.ActionConfirmRequirementVersion
	reason := cleardev.ReasonNone
	reasonText := ""
	if parsed.Decision == cleardev.HumanDecisionReject {
		outcome = cleardev.HumanDecisionDispatchRejected
		action = cleardev.ActionRejectRequirementVersion
		reason = cleardev.ReasonRejectedByDesktopHuman
		reasonText = cleardev.RejectedByDesktopHumanReason
	}
	if err := consumeDispatchCAS(ctx, q, parsed.Nonce, parsed.DesktopRunID, outcome, at); err != nil {
		return nil, err
	}
	rejected, err := applyClearDevRequirementVersionAction(ctx, q, cleardev.ActionRequest{
		Action: action, SubjectID: version.ID, Reason: reason, ReasonText: reasonText,
		TrustedHumanDecision: true, Source: cleardev.EventSourceHumanDecision, At: at,
	})
	if err != nil {
		return nil, err
	}
	if rejected != nil {
		return rejected, nil
	}
	sequence, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{
		SubjectID: version.ID, Action: string(action),
	})
	if err != nil {
		return nil, err
	}
	if err := q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{
		RequestID: requestRow.ID, Decision: string(parsed.Decision), EventSequence: sequence, CreatedAt: at,
	}); err != nil {
		return nil, err
	}
	requirement, err := q.GetClearDevRequirement(ctx, requestRow.DevelopmentProjectID)
	if err != nil {
		return nil, err
	}
	return nil, insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectHumanDecisionRequest, SubjectID: requestRow.ID,
		Action: cleardev.ActionSettleHumanDecision, PreviousState: string(cleardev.HumanDecisionRequestPending),
		TargetState: string(cleardev.HumanDecisionRequestResolved), Outcome: cleardev.EventAccepted,
		Reason: reason, ReasonText: reasonText, Source: cleardev.EventSourceHumanDecision, CreatedAt: at,
	})
}

func settleApproveDirectionChangeDecision(ctx context.Context, q *gen.Queries, parsed cleardev.HumanDecisionResult, requestRow gen.CleardevHumanDecisionRequest, at time.Time) (*cleardev.RuleError, error) {
	binding, err := cleardev.ParseApproveDirectionChangeBinding(parsed.Binding)
	if err != nil {
		return nil, err
	}
	gateRow, err := q.GetClearDevDirectionStopGateByRequest(ctx, binding.DirectionRequestID)
	if err != nil {
		return nil, err
	}
	gate := clearDevDirectionStopGateFromGen(gateRow)
	if gate.Status != cleardev.DirectionStopGateActive ||
		gate.DevelopmentRequirementID != binding.DevelopmentRequirementID ||
		gate.RequirementVersionID != binding.RequirementVersionID ||
		gate.TaskSetVersion != binding.TaskSetVersion ||
		gate.SnapshotSHA256 != binding.SnapshotSHA256 {
		return nil, errors.New("human decision binding no longer matches the active direction stop gate")
	}
	versionRow, err := q.GetClearDevRequirementVersion(ctx, binding.RequirementVersionID)
	if err != nil {
		return nil, err
	}
	version := clearDevRequirementVersionFromGen(versionRow)
	if version.SHA256 != binding.RequirementVersionSHA256 {
		return nil, errors.New("human decision binding no longer matches the requirement version")
	}
	rows, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{
		Decision: string(parsed.Decision), ResolvedAt: sql.NullTime{Time: at, Valid: true}, ID: requestRow.ID,
	})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, errors.New("human decision request was already settled")
	}
	outcome := cleardev.HumanDecisionDispatchApproved
	reason := cleardev.ReasonNone
	reasonText := ""
	action := cleardev.ActionCreateDirectionGate
	if parsed.Decision == cleardev.HumanDecisionReject {
		outcome = cleardev.HumanDecisionDispatchRejected
		reason = cleardev.ReasonRejectedByDesktopHuman
		reasonText = cleardev.RejectedByDesktopHumanReason
		action = cleardev.ActionCloseDirectionGate
		closed, closeErr := q.CloseClearDevDirectionStopGateCAS(ctx, gen.CloseClearDevDirectionStopGateCASParams{
			ClosedAt: nullableTimePtr(&at), CloseReason: string(reason), ID: gate.ID,
		})
		if closeErr != nil {
			return nil, closeErr
		}
		if closed != 1 {
			return nil, errors.New("direction stop gate could not be closed")
		}
	}
	if err := consumeDispatchCAS(ctx, q, parsed.Nonce, parsed.DesktopRunID, outcome, at); err != nil {
		return nil, err
	}
	requirement, err := q.GetClearDevRequirement(ctx, requestRow.DevelopmentProjectID)
	if err != nil {
		return nil, err
	}
	if parsed.Decision == cleardev.HumanDecisionApprove {
		snapshots, listErr := q.ListClearDevDirectionTaskSnapshots(ctx, gate.ID)
		if listErr != nil {
			return nil, listErr
		}
		for _, item := range snapshots {
			if err := q.InsertClearDevDirectionTaskProcessing(ctx, gen.InsertClearDevDirectionTaskProcessingParams{
				ID: uuid.NewString(), DirectionRequestID: gate.DirectionRequestID, TaskID: item.TaskID,
			}); err != nil {
				if isUniqueConstraint(err) {
					continue
				}
				return nil, err
			}
		}
		action = cleardev.ActionOccupyDirectionTask
		if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionStopGate, SubjectID: gate.ID,
			Action: cleardev.ActionOccupyDirectionTask, PreviousState: string(cleardev.DirectionStopGateActive),
			TargetState: string(cleardev.DirectionOccupancyPending), Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceHumanDecision, CreatedAt: at,
		}); err != nil {
			return nil, err
		}
	} else if parsed.Decision == cleardev.HumanDecisionReject {
		if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionStopGate, SubjectID: gate.ID,
			Action: cleardev.ActionCloseDirectionGate, PreviousState: string(cleardev.DirectionStopGateActive),
			TargetState: string(cleardev.DirectionStopGateClosed), Outcome: cleardev.EventAccepted,
			Reason: reason, ReasonText: reasonText, Source: cleardev.EventSourceHumanDecision, CreatedAt: at,
		}); err != nil {
			return nil, err
		}
	}
	sequence, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{
		SubjectID: gate.ID, Action: string(action),
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err := q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{
		RequestID: requestRow.ID, Decision: string(parsed.Decision), EventSequence: sequence, CreatedAt: at,
	}); err != nil {
		return nil, err
	}
	return nil, insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectHumanDecisionRequest, SubjectID: requestRow.ID,
		Action: cleardev.ActionSettleHumanDecision, PreviousState: string(cleardev.HumanDecisionRequestPending),
		TargetState: string(cleardev.HumanDecisionRequestResolved), Outcome: cleardev.EventAccepted,
		Reason: reason, ReasonText: reasonText, Source: cleardev.EventSourceHumanDecision, CreatedAt: at,
	})
}

func consumeDispatchCAS(ctx context.Context, q *gen.Queries, nonce, desktopRunID string, outcome cleardev.HumanDecisionDispatchOutcome, at time.Time) error {
	nonceSHA, err := cleardev.HumanDecisionNonceSHA256(nonce)
	if err != nil {
		return err
	}
	rows, err := q.ConsumeClearDevHumanDecisionDispatchCAS(ctx, gen.ConsumeClearDevHumanDecisionDispatchCASParams{
		ConsumedAt: sql.NullTime{Time: at, Valid: true}, Outcome: string(outcome),
		NonceSha256: nonceSHA, DesktopRunID: desktopRunID,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("human decision dispatch could not be consumed")
	}
	return nil
}

func consumeHumanDecisionDispatch(ctx context.Context, q *gen.Queries, nonce, desktopRunID string, outcome cleardev.HumanDecisionDispatchOutcome, at time.Time) error {
	nonceSHA, err := cleardev.HumanDecisionNonceSHA256(nonce)
	if err != nil {
		return err
	}
	dispatch, err := q.GetClearDevHumanDecisionDispatchByNonceSHA256(ctx, nonceSHA)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if dispatch.ConsumedAt.Valid {
		return nil
	}
	if err := consumeDispatchCAS(ctx, q, nonce, desktopRunID, outcome, at); err != nil {
		return err
	}
	request, err := q.GetClearDevHumanDecisionRequest(ctx, dispatch.RequestID)
	if err != nil {
		return err
	}
	requirement, err := q.GetClearDevRequirement(ctx, request.DevelopmentProjectID)
	if err != nil {
		return err
	}
	return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectHumanDecisionDispatch, SubjectID: dispatch.ID,
		Action: cleardev.ActionDismissHumanDecisionDispatch, Outcome: cleardev.EventAccepted,
		ReasonText: string(outcome), Source: cleardev.EventSourceHumanDecision, CreatedAt: at,
	})
}

func expireOpenHumanDecisionDispatches(ctx context.Context, q *gen.Queries, request gen.CleardevHumanDecisionRequest, at time.Time) error {
	open, err := q.ListOpenClearDevHumanDecisionDispatchesForRequest(ctx, request.ID)
	if err != nil {
		return err
	}
	requirement, err := q.GetClearDevRequirement(ctx, request.DevelopmentProjectID)
	if err != nil {
		return err
	}
	for _, row := range open {
		if at.Before(row.ExpiresAt) {
			continue
		}
		if _, err := q.ConsumeClearDevHumanDecisionDispatchCAS(ctx, gen.ConsumeClearDevHumanDecisionDispatchCASParams{
			ConsumedAt: sql.NullTime{Time: at, Valid: true}, Outcome: string(cleardev.HumanDecisionDispatchExpired),
			NonceSha256: row.NonceSha256, DesktopRunID: row.DesktopRunID,
		}); err != nil {
			return err
		}
		if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectHumanDecisionDispatch, SubjectID: row.ID,
			Action: cleardev.ActionDismissHumanDecisionDispatch, Outcome: cleardev.EventAccepted,
			ReasonText: string(cleardev.HumanDecisionDispatchExpired), Source: cleardev.EventSourceControlPlane, CreatedAt: at,
		}); err != nil {
			return err
		}
	}
	return nil
}

func ensureConfirmHumanDecisionRequest(ctx context.Context, q *gen.Queries, requirement cleardev.DevelopmentRequirement, version cleardev.RequirementVersion, at time.Time) error {
	if stage, e := q.GetClearDevProductStageByRequirement(ctx, nullableString(requirement.ID)); e == nil {
		if _, e := q.GetClearDevApprovedProductPlan(ctx, stage.DiscussionID); e == nil {
			var definition cleardev.ProductStageDefinition
			var document cleardev.NormalizedRequirementDocument
			if json.Unmarshal([]byte(stage.DefinitionJson), &definition) == nil && json.Unmarshal([]byte(version.RequirementText), &document) == nil && cleardev.ProductPlanCoversDocument(definition, document) {
				return nil
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}

	_, err := q.GetClearDevHumanDecisionRequestByRequirementVersionID(ctx, version.ID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	binding := cleardev.ConfirmRequirementBinding{
		DevelopmentRequirementID: requirement.ID, RequirementVersionID: version.ID,
		RequirementVersionSHA256: version.SHA256, TaskSetVersion: version.TaskSetVersion,
	}
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	normalized, err := cleardev.ParseConfirmRequirementBinding(bindingJSON)
	if err != nil {
		return err
	}
	bindingJSON, err = json.Marshal(normalized)
	if err != nil {
		return err
	}
	display := cleardev.ConfirmRequirementDisplay(requirement, version)
	displayJSON, err := json.Marshal(display)
	if err != nil {
		return err
	}
	digest, err := cleardev.HumanDecisionContentSHA256(cleardev.HumanDecisionKindConfirmVersion, bindingJSON, display)
	if err != nil {
		return err
	}
	requestID := uuid.NewString()
	if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{
		ID: requestID, DevelopmentProjectID: requirement.ID, DecisionKind: cleardev.HumanDecisionKindConfirmVersion,
		BindingSchemaVersion: int64(cleardev.ConfirmRequirementBindingVersion), BindingJson: string(bindingJSON),
		DisplayJson: string(displayJSON), ContentSha256: digest, CreatedAt: at,
	}); err != nil {
		if isUniqueConstraint(err) {
			return nil
		}
		return err
	}
	return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectHumanDecisionRequest, SubjectID: requestID,
		Action: cleardev.ActionCreateHumanDecisionRequest, TargetState: string(cleardev.HumanDecisionRequestPending),
		Outcome: cleardev.EventAccepted, Source: cleardev.EventSourceControlPlane, CreatedAt: at,
	})
}

func trySettleMatchingConfirmRequest(ctx context.Context, q *gen.Queries, versionID string, decision cleardev.HumanDecisionChoice, at time.Time) error {
	row, err := q.GetClearDevHumanDecisionRequestByRequirementVersionID(ctx, versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.Status != string(cleardev.HumanDecisionRequestPending) {
		return nil
	}
	rows, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{
		Decision: string(decision), ResolvedAt: sql.NullTime{Time: at, Valid: true}, ID: row.ID,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return nil
	}
	requirement, err := q.GetClearDevRequirement(ctx, row.DevelopmentProjectID)
	if err != nil {
		return err
	}
	return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectHumanDecisionRequest, SubjectID: row.ID,
		Action: cleardev.ActionSettleHumanDecision, PreviousState: string(cleardev.HumanDecisionRequestPending),
		TargetState: string(cleardev.HumanDecisionRequestResolved), Outcome: cleardev.EventAccepted,
		Source: cleardev.EventSourceHumanDecision, CreatedAt: at,
	})
}

func humanDecisionRequestFromGen(row gen.CleardevHumanDecisionRequest) cleardev.HumanDecisionRequest {
	request := cleardev.HumanDecisionRequest{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, DecisionKind: row.DecisionKind,
		BindingSchemaVersion: int(row.BindingSchemaVersion), BindingJSON: row.BindingJson,
		DisplayJSON: row.DisplayJson, ContentSHA256: row.ContentSha256,
		Status:   cleardev.HumanDecisionRequestStatus(row.Status),
		Decision: cleardev.HumanDecisionChoice(row.Decision), CreatedAt: row.CreatedAt,
	}
	if row.ResolvedAt.Valid {
		resolved := row.ResolvedAt.Time
		request.ResolvedAt = &resolved
	}
	return request
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique")
}

// CreateExtraReviewBudgetRequest records the human decision request that
// authorizes exactly one more reviewer turn on one task's reviewer budget. A
// repeat call while such a request is still pending keeps the original one.
// createExtraBudgetTurnRequest records the pending offer for one extra turn
// on one task budget; kind and wording differ per role, the record does not.
func (s *Store) createExtraBudgetTurnRequest(ctx context.Context, requirement cleardev.DevelopmentRequirement, kind string, binding cleardev.ExtraReviewBudgetBinding, display cleardev.HumanDecisionDisplay, at time.Time) (string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	requestID := ""
	err := s.inTx(ctx, "create extra budget turn request", func(q *gen.Queries) error {
		pending, err := q.ListPendingClearDevHumanDecisionRequests(ctx)
		if err != nil {
			return err
		}
		for _, row := range pending {
			if row.DecisionKind == kind && strings.Contains(row.BindingJson, binding.BudgetID) {
				requestID = row.ID
				return nil
			}
		}
		bindingJSON, err := json.Marshal(binding)
		if err != nil {
			return err
		}
		normalized, err := cleardev.ParseExtraReviewBudgetBinding(bindingJSON)
		if err != nil {
			return err
		}
		bindingJSON, err = json.Marshal(normalized)
		if err != nil {
			return err
		}
		displayJSON, err := json.Marshal(display)
		if err != nil {
			return err
		}
		digest, err := cleardev.HumanDecisionContentSHA256(kind, bindingJSON, display)
		if err != nil {
			return err
		}
		requestID = uuid.NewString()
		if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{
			ID: requestID, DevelopmentProjectID: requirement.ID, DecisionKind: kind,
			BindingSchemaVersion: 1, BindingJson: string(bindingJSON), DisplayJson: string(displayJSON), ContentSha256: digest, CreatedAt: at,
		}); err != nil {
			if isUniqueConstraint(err) {
				return nil
			}
			return err
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectHumanDecisionRequest, SubjectID: requestID,
			Action: cleardev.ActionCreateHumanDecisionRequest, TargetState: string(cleardev.HumanDecisionRequestPending),
			Outcome: cleardev.EventAccepted, Source: cleardev.EventSourceControlPlane, CreatedAt: at,
		})
	})
	return requestID, err
}

// CreateExtraReviewBudgetRequest records the human decision request that
// offers one extra reviewer turn on one task's reviewer budget.
func (s *Store) CreateExtraReviewBudgetRequest(ctx context.Context, requirement cleardev.DevelopmentRequirement, binding cleardev.ExtraReviewBudgetBinding, at time.Time) (string, error) {
	display := cleardev.HumanDecisionDisplay{
		Title:         "Authorize an extra review round",
		Summary:       "A rework round of one ClearDev task needs one more reviewer turn than its budget grants.",
		FullContent:   "MESSAGE_BUDGET_V1 grants one reviewer budget per task. The rework round follows a requirement final review REWORK, so its review is a second turn. Approving grants exactly one extra reviewer turn on this task's reviewer budget; every other budget, task and recorded occupancy stays unchanged.",
		ChangeSummary: "Authorize one extra reviewer turn on budget " + binding.BudgetID + " for round " + fmt.Sprint(binding.Round) + " of task " + binding.TaskID + ".",
	}
	return s.createExtraBudgetTurnRequest(ctx, requirement, cleardev.HumanDecisionKindExtraReviewBudget, binding, display, at)
}

// CreateExtraBuilderTurnRequest records the pending controlled-recovery offer
// for one extra builder turn on one task's builder budget.
func (s *Store) CreateExtraBuilderTurnRequest(ctx context.Context, requirement cleardev.DevelopmentRequirement, binding cleardev.ExtraBuilderTurnBinding, at time.Time) (string, error) {
	display := cleardev.HumanDecisionDisplay{
		Title:         "Authorize the controlled recovery of one builder round",
		Summary:       "A builder round of one ClearDev task stopped because its rework budget ran out.",
		FullContent:   "MESSAGE_BUDGET_V1 grants one builder turn per round. Approving grants exactly one extra builder turn on this task's builder budget so the stopped round can run once more; every other budget, task and recorded occupancy stays unchanged, and the original failures stay recorded.",
		ChangeSummary: "Authorize one extra builder turn on budget " + binding.BudgetID + " for round " + fmt.Sprint(binding.Round) + " of task " + binding.TaskID + ".",
	}
	return s.createExtraBudgetTurnRequest(ctx, requirement, cleardev.HumanDecisionKindExtraBuilderTurn, binding, display, at)
}

// AuthorizeExtraReviewBudget grants exactly one more reviewer turn on one
// task budget. It never touches other budgets and never rewrites occupancies.
func (s *Store) AuthorizeExtraReviewBudget(ctx context.Context, budgetID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "authorize extra review budget", func(q *gen.Queries) error {
		rows, err := q.AuthorizeClearDevComplexExceptionBudgetExtra(ctx, budgetID)
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("extra review budget authorization did not apply to budget %s", budgetID)
		}
		return nil
	})
}

// RecoverClearDevComplexExecutionBuilderAttempt reopens one blocked
// budget-exhausted builder attempt as its rework successor. The settlement
// calls it inside its own transaction; the standalone form exists for
// administrative verification.
func (s *Store) RecoverClearDevComplexExecutionBuilderAttempt(ctx context.Context, executionRunID, taskMappingID string, round int64) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows := int64(0)
	err := s.inTx(ctx, "recover ClearDev builder attempt", func(q *gen.Queries) error {
		var err error
		rows, err = q.RecoverClearDevComplexExecutionBuilderAttempt(ctx, gen.RecoverClearDevComplexExecutionBuilderAttemptParams{
			ExecutionRunID: executionRunID, TaskMappingID: taskMappingID, Round: round,
		})
		return err
	})
	return rows, err
}

// AuthorizeExtraBuilderTurn grants exactly one more builder turn on one
// task budget. It never touches other budgets and never rewrites occupancies.
func (s *Store) AuthorizeExtraBuilderTurn(ctx context.Context, budgetID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "authorize extra builder turn", func(q *gen.Queries) error {
		rows, err := q.AuthorizeClearDevComplexExceptionBudgetExtra(ctx, budgetID)
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("extra builder turn authorization did not apply to budget %s", budgetID)
		}
		return nil
	})
}

// settleExtraReviewBudget resolves the request for one extra reviewer turn on
// one task's reviewer budget after rechecking that the offer still names a
// recorded reviewer budget of that exact task. The extra turn itself is
// granted by the service after this settlement.
func settleExtraBudgetTurn(ctx context.Context, q *gen.Queries, result cleardev.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time, roleKind, label string) (*cleardev.RuleError, error) {
	b, err := cleardev.ParseExtraReviewBudgetBinding(result.Binding)
	if err != nil {
		return nil, err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, b.ExecutionRunID)
	if err != nil {
		return nil, err
	}
	if run.Status != "ACCEPTED" || run.DevelopmentProjectID != b.DevelopmentRequirementID {
		return nil, complexExecutionRule(label + " authorization is stale")
	}
	// The desktop names tasks by work item; older offers may carry the
	// mapping id instead, so accept either identifier.
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, b.ExecutionRunID)
	if err != nil {
		return nil, err
	}
	mappingID := ""
	for _, mapping := range mappings {
		if mapping.ID == b.TaskID || mapping.WorkItemID == b.TaskID {
			mappingID = mapping.ID
		}
	}
	if mappingID == "" {
		return nil, complexExecutionRule(label + " authorization names no task of this execution")
	}
	budgets, err := q.ListClearDevComplexExceptionBudgets(ctx, b.ExecutionRunID)
	if err != nil {
		return nil, err
	}
	matched := false
	for _, budget := range budgets {
		if budget.ID == b.BudgetID && budget.RoleKind == roleKind &&
			budget.ComplexExecutionTaskID.Valid && budget.ComplexExecutionTaskID.String == mappingID {
			matched = true
		}
	}
	if !matched {
		return nil, complexExecutionRule(label + " authorization does not match a recorded task budget")
	}
	if item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mappingID); err == nil {
		if item.State == "DONE" || item.State == "CANCELLED" {
			return nil, complexExecutionRule(label + " authorization target is finished")
		}
	}
	attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, b.ExecutionRunID)
	if err != nil {
		return nil, err
	}
	currentRound := -1
	for _, attempt := range attempts {
		if attempt.TaskMappingID == mappingID && int(attempt.Round) > currentRound {
			currentRound = int(attempt.Round)
		}
	}
	if currentRound != b.Round {
		return nil, complexExecutionRule(label + " authorization names a stale round")
	}
	changed, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{ID: request.ID, Decision: string(result.Decision), ResolvedAt: nullableTime(at)})
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errors.New(label + " authorization already decided")
	}
	outcome := cleardev.HumanDecisionDispatchRejected
	if result.Decision == cleardev.HumanDecisionApprove {
		outcome = cleardev.HumanDecisionDispatchApproved
		// The budget effect commits with the settlement: an approval and its
		// extra turn can never split across transactions.
		rows, err := q.AuthorizeClearDevComplexExceptionBudgetExtra(ctx, b.BudgetID)
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			return nil, complexExecutionRule("the budget is no longer authorizable")
		}
		needsSupplement := false
		if roleKind == cleardev.ComplexExceptionBudgetBuilder {
			var contextErr error
			needsSupplement, contextErr = workflowBuilderAwaitingSupplement(ctx, q, b.ExecutionRunID, mappingID, b.Round)
			if contextErr != nil {
				return nil, contextErr
			}
		}
		if roleKind == cleardev.ComplexExceptionBudgetBuilder && !needsSupplement {
			// The authorized turn alone cannot run a blocked round: the
			// settlement also records the rework successor of the exact
			// stopped attempt, so the selector re-dispatches the next round
			// while every failure record stays untouched.
			attempt, err := q.RecoverClearDevComplexExecutionBuilderAttempt(ctx, gen.RecoverClearDevComplexExecutionBuilderAttemptParams{
				ExecutionRunID: b.ExecutionRunID, TaskMappingID: mappingID, Round: int64(b.Round),
			})
			if err != nil {
				return nil, err
			}
			if attempt != 1 {
				return nil, complexExecutionRule("builder turn authorization names a round that is no longer recoverable")
			}
			item, itemErr := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mappingID)
			if itemErr != nil {
				return nil, itemErr
			}
			if item.State != "BLOCKED" {
				return nil, complexExecutionRule("builder turn authorization target is not blocked")
			}
			reopened, itemErr := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
				ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount,
				NextState: "REWORK", ReworkCount: item.ReworkCount + 1, UpdatedAt: at,
			})
			if itemErr != nil {
				return nil, itemErr
			}
			if reopened != 1 {
				return nil, complexExecutionRule("builder turn authorization target changed concurrently")
			}
		}
	}
	if err := consumeDispatchCAS(ctx, q, result.Nonce, result.DesktopRunID, outcome, at); err != nil {
		return nil, err
	}
	project, err := q.GetClearDevRequirement(ctx, b.DevelopmentRequirementID)
	if err != nil {
		return nil, err
	}
	event := cleardev.RequirementEvent{AOProjectID: project.AoProjectID, DevelopmentRequirementID: project.ID, SubjectType: cleardev.SubjectHumanDecisionRequest, SubjectID: request.ID, Action: cleardev.ActionSettleHumanDecision, PreviousState: "PENDING", TargetState: "RESOLVED", Outcome: cleardev.EventAccepted, Source: cleardev.EventSourceHumanDecision, CreatedAt: at}
	if err := insertClearDevEvent(ctx, q, event); err != nil {
		return nil, err
	}
	seq, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: request.ID, Action: string(cleardev.ActionSettleHumanDecision)})
	if err != nil {
		return nil, err
	}
	return nil, q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: request.ID, Decision: string(result.Decision), EventSequence: seq, CreatedAt: at})
}

// settleExtraReviewBudget resolves the request for one extra reviewer turn on
// one task's reviewer budget after rechecking that the offer still names a
// recorded reviewer budget of that exact task. The extra turn itself is
// granted by the service after this settlement.
func settleExtraReviewBudget(ctx context.Context, q *gen.Queries, result cleardev.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*cleardev.RuleError, error) {
	return settleExtraBudgetTurn(ctx, q, result, request, at, cleardev.ComplexExceptionBudgetReviewer, "review budget")
}

// settleExtraBuilderTurn resolves the controlled-recovery request for one
// extra builder turn on one task's builder budget. The extra turn itself is
// granted inside this settlement.
func settleExtraBuilderTurn(ctx context.Context, q *gen.Queries, result cleardev.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*cleardev.RuleError, error) {
	return settleExtraBudgetTurn(ctx, q, result, request, at, cleardev.ComplexExceptionBudgetBuilder, "builder turn")
}
