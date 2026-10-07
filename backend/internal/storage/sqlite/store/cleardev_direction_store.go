package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// FailNextDirectionTaskFinalizeForTest injects a finalize failure used only by tests.
func (s *Store) FailNextDirectionTaskFinalizeForTest(err error) {
	s.directionTestFailFinalize = err
}

// HasActiveClearDevDirectionStop is true when the named version has an open gate.
func (s *Store) HasActiveClearDevDirectionStop(ctx context.Context, versionID string) (bool, error) {
	return activeDirectionStop(ctx, s.qr, versionID)
}

// ListClearDevRunnableDirectionRequirements lists requirements boot must resume.
func (s *Store) ListClearDevRunnableDirectionRequirements(ctx context.Context) ([]string, error) {
	return s.qr.ListClearDevRunnableDirectionRequirements(ctx)
}

// GetClearDevTaskDispatchFacts returns dispatch, baseline, and builder session for a task.
func (s *Store) GetClearDevTaskDispatchFacts(ctx context.Context, taskID string) (dispatchID, baseSHA, builderSessionID string, err error) {
	binding, err := s.qr.GetClearDevWorkItemDispatchBinding(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	if !binding.AcceptedDispatchID.Valid {
		return "", binding.DispatchBaseCommitSha.String, "", nil
	}
	dispatch, err := s.qr.GetClearDevStandardDispatch(ctx, binding.AcceptedDispatchID.String)
	if errors.Is(err, sql.ErrNoRows) {
		return binding.AcceptedDispatchID.String, binding.DispatchBaseCommitSha.String, "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	builder, err := s.qr.GetClearDevStandardRoleBinding(ctx, dispatch.BuilderRoleBindingID)
	if errors.Is(err, sql.ErrNoRows) {
		return dispatch.ID, binding.DispatchBaseCommitSha.String, "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	return dispatch.ID, binding.DispatchBaseCommitSha.String, builder.AoSessionID.String, nil
}

// GetClearDevDirectionChange loads the durable S05 fact set for one requirement.
func (s *Store) GetClearDevDirectionChange(ctx context.Context, requirementID string) (cleardev.DirectionChangeSnapshot, bool, error) {
	intentRow, err := s.qr.GetClearDevDirectionIntentByRequirement(ctx, requirementID)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.DirectionChangeSnapshot{}, false, nil
	}
	if err != nil {
		return cleardev.DirectionChangeSnapshot{}, false, err
	}
	snapshot := cleardev.DirectionChangeSnapshot{Intent: ptrDirectionIntent(clearDevDirectionIntentFromGen(intentRow))}
	if requestRow, reqErr := s.qr.GetClearDevDirectionRequestByRequirement(ctx, requirementID); reqErr == nil {
		request, parseErr := clearDevDirectionRequestFromGen(requestRow)
		if parseErr != nil {
			return cleardev.DirectionChangeSnapshot{}, false, parseErr
		}
		snapshot.Request = &request
		if gateRow, gateErr := s.qr.GetClearDevDirectionStopGateByRequest(ctx, request.ID); gateErr == nil {
			gate := clearDevDirectionStopGateFromGen(gateRow)
			snapshot.Gate = &gate
			items, listErr := s.qr.ListClearDevDirectionTaskSnapshots(ctx, gate.ID)
			if listErr != nil {
				return cleardev.DirectionChangeSnapshot{}, false, listErr
			}
			for _, item := range items {
				snapshot.Snapshots = append(snapshot.Snapshots, cleardev.DirectionTaskSnapshotItem{
					TaskID: item.TaskID, Status: cleardev.DevelopmentTaskStatus(item.Status),
					PausedFromStatus: cleardev.DevelopmentTaskStatus(item.PausedFromStatus), Ordinal: int(item.Ordinal),
				})
			}
		} else if !errors.Is(gateErr, sql.ErrNoRows) {
			return cleardev.DirectionChangeSnapshot{}, false, gateErr
		}
		processings, listErr := s.qr.ListClearDevDirectionTaskProcessings(ctx, request.ID)
		if listErr != nil {
			return cleardev.DirectionChangeSnapshot{}, false, listErr
		}
		for _, item := range processings {
			snapshot.Processings = append(snapshot.Processings, clearDevDirectionProcessingFromGen(item))
		}
		checkpoints, listErr := s.qr.ListClearDevDirectionCheckpoints(ctx, request.ID)
		if listErr != nil {
			return cleardev.DirectionChangeSnapshot{}, false, listErr
		}
		for _, item := range checkpoints {
			snapshot.Checkpoints = append(snapshot.Checkpoints, clearDevDirectionCheckpointFromGen(item))
		}
		if decision, decisionErr := s.qr.GetClearDevHumanDecisionRequestByDirectionRequestID(ctx, request.ID); decisionErr == nil {
			snapshot.DecisionRequestID = decision.ID
			snapshot.DecisionContentSHA256 = decision.ContentSha256
			snapshot.OccupancyApproved = decision.Status == string(cleardev.HumanDecisionRequestResolved) &&
				decision.Decision == string(cleardev.HumanDecisionApprove)
		} else if !errors.Is(decisionErr, sql.ErrNoRows) {
			return cleardev.DirectionChangeSnapshot{}, false, decisionErr
		}
		if revisionRow, revErr := s.qr.GetClearDevDirectionRevision(ctx, request.ID); revErr == nil {
			revision := cleardev.DirectionRevision{
				DirectionRequestID: revisionRow.DirectionRequestID, PreviousRequirementVersionID: revisionRow.PreviousRequirementVersionID,
				TargetRequirementVersionID: revisionRow.TargetRequirementVersionID, CreatedAt: revisionRow.CreatedAt,
			}
			snapshot.Revision = &revision
			if err := loadDirectionCompilationFacts(ctx, s.qr, request.ID, &snapshot); err != nil {
				return cleardev.DirectionChangeSnapshot{}, false, err
			}
		} else if !errors.Is(revErr, sql.ErrNoRows) {
			return cleardev.DirectionChangeSnapshot{}, false, revErr
		}
	} else if !errors.Is(reqErr, sql.ErrNoRows) {
		return cleardev.DirectionChangeSnapshot{}, false, reqErr
	}
	steps, err := s.qr.ListClearDevDirectionAgentStepsForRequirement(ctx, requirementID)
	if err != nil {
		return cleardev.DirectionChangeSnapshot{}, false, err
	}
	for _, step := range steps {
		snapshot.AgentSteps = append(snapshot.AgentSteps, clearDevDirectionAgentStepFromGen(step))
	}
	return snapshot, true, nil
}

// CreateClearDevDirectionIntent writes the idempotent user message and Steward step.
func (s *Store) CreateClearDevDirectionIntent(ctx context.Context, command cleardev.CreateDirectionIntentCommand) (cleardev.DirectionIntent, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.DirectionIntent
	var created bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev direction intent", func(q *gen.Queries) error {
		intent := command.Intent
		requirementRow, err := q.GetClearDevRequirement(ctx, intent.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		if existing, getErr := q.GetClearDevDirectionIntent(ctx, intent.RequestID); getErr == nil {
			loaded := clearDevDirectionIntentFromGen(existing)
			if sameDirectionIntent(loaded, intent) {
				result = loaded
				return nil
			}
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction intent id already belongs to another request")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		if _, complexErr := q.GetClearDevComplexRequirement(ctx, requirement.ID); errors.Is(complexErr, sql.ErrNoRows) {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction change requires a complex requirement")
			return nil
		} else if complexErr != nil {
			return complexErr
		}
		confirmed, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
		if errors.Is(err, sql.ErrNoRows) {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction change requires a current confirmed version")
			return nil
		}
		if err != nil {
			return err
		}
		if requirement.CancelledAt != nil || confirmed.ID != intent.RequirementVersionID || confirmed.Sha256 != intent.RequirementSHA256 ||
			strings.TrimSpace(intent.Message) == "" || intent.MessageSHA256 != cleardev.DirectionMessageSHA256(intent.Message) ||
			strings.TrimSpace(intent.StewardRoleBindingID) == "" || strings.TrimSpace(intent.DirectionRequestID) == "" ||
			strings.TrimSpace(intent.AgentStepID) == "" || strings.TrimSpace(command.Step.ID) == "" {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid direction intent")
			return nil
		}
		if _, planErr := q.GetApprovedClearDevComplexPlanForVersion(ctx, confirmed.ID); errors.Is(planErr, sql.ErrNoRows) {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction change requires an approved plan")
			return nil
		} else if planErr != nil {
			return planErr
		}
		stewardRow, err := q.GetClearDevComplexRoleBinding(ctx, intent.StewardRoleBindingID)
		if err != nil {
			return err
		}
		steward := clearDevComplexRoleBindingFromGen(stewardRow)
		if steward.DevelopmentRequirementID != requirement.ID || steward.Role != cleardev.StandardRoleSteward ||
			steward.Status != cleardev.RoleBindingStatusBound || steward.AOSessionID == "" {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction change requires the original bound Steward")
			return nil
		}
		if history, countErr := q.CountClearDevDirectionHistoryForVersion(ctx, confirmed.ID); countErr != nil {
			return countErr
		} else if history != 0 {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "this requirement version already has a direction change")
			return nil
		}
		if _, revErr := q.GetClearDevDirectionRevisionByRequirement(ctx, requirement.ID); revErr == nil {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "this requirement already has a target v2")
			return nil
		} else if !errors.Is(revErr, sql.ErrNoRows) {
			return revErr
		}
		if _, reqErr := q.GetClearDevDirectionRequestByRequirement(ctx, requirement.ID); reqErr == nil {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "this requirement already has a direction request")
			return nil
		} else if !errors.Is(reqErr, sql.ErrNoRows) {
			return reqErr
		}
		step := command.Step
		if step.ID != intent.AgentStepID || step.RoleBindingID != steward.ID || step.Kind != cleardev.DirectionAgentStepChange ||
			step.RequestID != intent.DirectionRequestID || step.SendStatus != cleardev.AgentStepSendStatusPending ||
			!validSHA256Digest(step.PromptSHA256) {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid direction steward step")
			return nil
		}
		if err := q.InsertClearDevDirectionIntent(ctx, gen.InsertClearDevDirectionIntentParams{
			RequestID: intent.RequestID, DevelopmentProjectID: requirement.ID, RequirementVersionID: confirmed.ID,
			RequirementSha256: intent.RequirementSHA256, Message: intent.Message, MessageSha256: intent.MessageSHA256,
			StewardRoleBindingID: intent.StewardRoleBindingID, DirectionRequestID: intent.DirectionRequestID,
			AgentStepID: intent.AgentStepID, CreatedAt: intent.CreatedAt,
		}); err != nil {
			if isUniqueConstraint(err) {
				rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "this requirement version already has a direction change")
				return nil
			}
			return err
		}
		if err := insertClearDevDirectionAgentStep(ctx, q, step); err != nil {
			return err
		}
		result = intent
		created = true
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionIntent, SubjectID: intent.RequestID,
			Action: cleardev.ActionCreateDirectionIntent, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, CreatedAt: intent.CreatedAt,
		})
	})
	if err != nil {
		return cleardev.DirectionIntent{}, false, err
	}
	if rejected != nil {
		return cleardev.DirectionIntent{}, false, rejected
	}
	return result, created, nil
}

// CreateClearDevDirectionAgentStep inserts a pending 0111 Agent step if the key is new.
func (s *Store) CreateClearDevDirectionAgentStep(ctx context.Context, step cleardev.AgentStep) (cleardev.AgentStep, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.AgentStep
	var created bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev direction agent step", func(q *gen.Queries) error {
		bindingRow, err := q.GetClearDevComplexRoleBinding(ctx, step.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevComplexRoleBindingFromGen(bindingRow)
		requirement, _, err := loadComplexRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if existing, getErr := q.GetClearDevDirectionAgentStep(ctx, step.ID); getErr == nil {
			loaded := clearDevDirectionAgentStepFromGen(existing)
			if sameComplexAgentStepRequest(loaded, step) {
				result = loaded
				return nil
			}
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction agent step already belongs to another request")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		if binding.Status != cleardev.RoleBindingStatusBound || step.SendStatus != cleardev.AgentStepSendStatusPending ||
			(step.Kind != cleardev.DirectionAgentStepChange && step.Kind != cleardev.ComplexAgentStepCompilation) ||
			!validSHA256Digest(step.PromptSHA256) {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid pending direction agent step")
			return nil
		}
		if err := insertClearDevDirectionAgentStep(ctx, q, step); err != nil {
			return err
		}
		result = step
		created = true
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionCompilation, SubjectID: step.ID,
			Action: cleardev.ActionCreateComplexAgentStep, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, SourceAOSessionID: binding.AOSessionID, CreatedAt: step.RequestedAt,
		})
	})
	if err != nil {
		return cleardev.AgentStep{}, false, err
	}
	if rejected != nil {
		return cleardev.AgentStep{}, false, rejected
	}
	return result, created, nil
}

// MarkClearDevDirectionAgentStepSent records that the exact ClientMessageID was sent.
func (s *Store) MarkClearDevDirectionAgentStepSent(ctx context.Context, stepID string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "mark ClearDev direction agent step sent", func(q *gen.Queries) error {
		row, err := q.GetClearDevDirectionAgentStep(ctx, stepID)
		if err != nil {
			return err
		}
		step := clearDevDirectionAgentStepFromGen(row)
		bindingRow, err := q.GetClearDevComplexRoleBinding(ctx, step.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevComplexRoleBindingFromGen(bindingRow)
		requirement, _, err := loadComplexRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if step.SendStatus == cleardev.AgentStepSendStatusSent {
			return nil
		}
		if step.SendStatus != cleardev.AgentStepSendStatusPending {
			rejected = directionRejected(requirement.ID, cleardev.ReasonInvalidTransition, "direction agent step is no longer pending")
			return nil
		}
		rows, err := q.MarkClearDevDirectionAgentStepSentCAS(ctx, gen.MarkClearDevDirectionAgentStepSentCASParams{SentAt: nullableTimePtr(&at), ID: stepID})
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = directionRejected(requirement.ID, cleardev.ReasonInvalidTransition, "direction agent-step compare-and-swap lost")
			return nil
		}
		changed = true
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionCompilation, SubjectID: stepID,
			Action: cleardev.ActionSendComplexAgentStep, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, SourceAOSessionID: binding.AOSessionID, CreatedAt: at,
		})
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

// SettleClearDevDirectionAgentStep stores the final send status and message binding.
func (s *Store) SettleClearDevDirectionAgentStep(ctx context.Context, step cleardev.AgentStep) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "settle ClearDev direction agent step", func(q *gen.Queries) error {
		return settleDirectionAgentStep(ctx, q, step, &changed, &rejected)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

// AcceptClearDevDirectionChange installs the request, snapshots, gate, and decision atomically.
func (s *Store) AcceptClearDevDirectionChange(ctx context.Context, command cleardev.AcceptDirectionChangeCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "accept ClearDev direction change", func(q *gen.Queries) error {
		intentRow, err := q.GetClearDevDirectionIntent(ctx, command.Intent.RequestID)
		if err != nil {
			return err
		}
		intent := clearDevDirectionIntentFromGen(intentRow)
		requirementRow, err := q.GetClearDevRequirement(ctx, intent.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		if existing, getErr := q.GetClearDevDirectionRequest(ctx, intent.DirectionRequestID); getErr == nil {
			if existing.IntentRequestID == intent.RequestID {
				return nil
			}
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction request already exists")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		confirmed, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
		if err != nil {
			return err
		}
		if requirement.CancelledAt != nil || confirmed.ID != intent.RequirementVersionID || confirmed.Sha256 != intent.RequirementSHA256 {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction change no longer matches the current confirmed version")
			return nil
		}
		if _, planErr := q.GetApprovedClearDevComplexPlanForVersion(ctx, confirmed.ID); planErr != nil {
			if errors.Is(planErr, sql.ErrNoRows) {
				rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction change requires an approved plan")
				return nil
			}
			return planErr
		}
		if strings.TrimSpace(command.Gate.ID) == "" {
			return directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction stop gate id is required")
		}
		step := command.AgentStep
		step.ID = intent.AgentStepID
		var changed bool
		if err := settleDirectionAgentStep(ctx, q, step, &changed, &rejected); err != nil {
			return err
		}
		if rejected != nil {
			return rejected
		}
		request := command.Request
		request.ID = intent.DirectionRequestID
		request.IntentRequestID = intent.RequestID
		request.DevelopmentRequirementID = requirement.ID
		request.RequirementVersionID = confirmed.ID
		request.AgentStepID = intent.AgentStepID
		if strings.TrimSpace(request.Summary) == "" || strings.TrimSpace(request.ResultSHA256) == "" {
			return directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid direction request")
		}
		if err := insertClearDevDirectionRequest(ctx, q, request); err != nil {
			return err
		}
		tasks, err := q.ListClearDevDevelopmentTasksForVersion(ctx, confirmed.ID)
		if err != nil {
			return err
		}
		items := make([]cleardev.DirectionTaskSnapshotItem, 0, len(tasks))
		for _, task := range tasks {
			items = append(items, cleardev.DirectionTaskSnapshotItem{
				TaskID: task.ID, Status: publicDevelopmentTaskStatus(task.State),
				PausedFromStatus: publicDevelopmentTaskStatus(task.PausedFromState.String),
			})
		}
		slices.SortFunc(items, func(a, b cleardev.DirectionTaskSnapshotItem) int {
			return strings.Compare(a.TaskID, b.TaskID)
		})
		for index := range items {
			items[index].Ordinal = index
		}
		digest, err := cleardev.HashDirectionTaskSnapshot(items)
		if err != nil {
			return err
		}
		gate := command.Gate
		gate.DirectionRequestID = request.ID
		gate.DevelopmentRequirementID = requirement.ID
		gate.RequirementVersionID = confirmed.ID
		gate.TaskSetVersion = confirmed.TaskSetVersion
		gate.SnapshotSHA256 = digest
		gate.Status = cleardev.DirectionStopGateActive
		if err := q.InsertClearDevDirectionStopGate(ctx, gen.InsertClearDevDirectionStopGateParams{
			ID: gate.ID, DirectionRequestID: gate.DirectionRequestID, DevelopmentProjectID: requirement.ID,
			RequirementVersionID: confirmed.ID, TaskSetVersion: confirmed.TaskSetVersion, SnapshotSha256: digest, CreatedAt: command.At,
		}); err != nil {
			return err
		}
		for _, item := range items {
			if err := q.InsertClearDevDirectionTaskSnapshot(ctx, gen.InsertClearDevDirectionTaskSnapshotParams{
				GateID: gate.ID, TaskID: item.TaskID, Status: string(item.Status),
				PausedFromStatus: string(item.PausedFromStatus), Ordinal: int64(item.Ordinal),
			}); err != nil {
				return err
			}
		}
		if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionRequest, SubjectID: request.ID,
			Action: cleardev.ActionCreateDirectionRequest, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, CreatedAt: command.At,
		}); err != nil {
			return err
		}
		if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionStopGate, SubjectID: gate.ID,
			Action: cleardev.ActionCreateDirectionGate, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, CreatedAt: command.At,
		}); err != nil {
			return err
		}
		return insertDirectionDecisionRequest(ctx, q, requirement, intent, request, gate, len(items), command.DecisionRequestID, command.At)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// OccupyClearDevDirectionTask claims one snapshot task for exclusive cancel handling.
func (s *Store) OccupyClearDevDirectionTask(ctx context.Context, command cleardev.OccupyDirectionTaskCommand) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "occupy ClearDev direction task", func(q *gen.Queries) error {
		row, err := q.GetClearDevDirectionTaskProcessing(ctx, command.ProcessingID)
		if err != nil {
			return err
		}
		if row.Occupancy != string(cleardev.DirectionOccupancyPending) {
			return nil
		}
		rows, err := q.OccupyClearDevDirectionTaskProcessingCAS(ctx, gen.OccupyClearDevDirectionTaskProcessingCASParams{
			OccupiedAt: nullableTimePtr(&command.At), InterruptResult: "", ID: command.ProcessingID,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return nil
		}
		changed = true
		request, err := q.GetClearDevDirectionRequest(ctx, row.DirectionRequestID)
		if err != nil {
			return err
		}
		requirement, err := q.GetClearDevRequirement(ctx, request.DevelopmentProjectID)
		if err != nil {
			return err
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionProcessing, SubjectID: command.ProcessingID,
			Action: cleardev.ActionOccupyDirectionTask, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, CreatedAt: command.At,
		})
	})
	return changed, err
}

// MarkClearDevDirectionInterruptUnknown records that interrupt did not prove death.
func (s *Store) MarkClearDevDirectionInterruptUnknown(ctx context.Context, processingID, result string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "mark ClearDev direction interrupt unknown", func(q *gen.Queries) error {
		_, err := q.MarkClearDevDirectionTaskInterruptUnknownCAS(ctx, gen.MarkClearDevDirectionTaskInterruptUnknownCASParams{
			InterruptResult: result, ID: processingID,
		})
		return err
	})
}

// FinalizeClearDevDirectionTask writes checkpoint, optional candidate, and cancel together.
func (s *Store) FinalizeClearDevDirectionTask(ctx context.Context, command cleardev.FinalizeDirectionTaskCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "finalize ClearDev direction task", func(q *gen.Queries) error {
		if err := s.directionTestFailFinalize; err != nil {
			s.directionTestFailFinalize = nil
			return err
		}
		processingRow, err := q.GetClearDevDirectionTaskProcessing(ctx, command.Processing.ID)
		if err != nil {
			return err
		}
		if processingRow.Occupancy == string(cleardev.DirectionOccupancyFinal) {
			return nil
		}
		request, err := q.GetClearDevDirectionRequest(ctx, processingRow.DirectionRequestID)
		if err != nil {
			return err
		}
		requirementRow, err := q.GetClearDevRequirement(ctx, request.DevelopmentProjectID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		checkpointID := ""
		if command.Checkpoint != nil {
			checkpoint := *command.Checkpoint
			checkpoint.ProcessingID = processingRow.ID
			checkpoint.TaskID = processingRow.TaskID
			if command.Candidate != nil {
				candidateID, insertErr := insertDirectionCandidate(ctx, q, requirement, processingRow.TaskID, *command.Candidate)
				if insertErr != nil {
					return insertErr
				}
				checkpoint.CandidateCommitID = candidateID
			}
			if err := insertClearDevDirectionCheckpoint(ctx, q, checkpoint); err != nil {
				return err
			}
			checkpointID = checkpoint.ID
			if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
				AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
				SubjectType: cleardev.SubjectDirectionCheckpoint, SubjectID: checkpoint.ID,
				Action: cleardev.ActionSaveDirectionCheckpoint, Outcome: cleardev.EventAccepted,
				Source: cleardev.EventSourceControlPlane, CreatedAt: command.At,
			}); err != nil {
				return err
			}
		}
		occupiedAt := processingRow.OccupiedAt
		if !occupiedAt.Valid {
			occupiedAt = nullableTimePtr(&command.At)
		}
		interrupt := processingRow.InterruptResult
		if command.Processing.InterruptResult != "" {
			interrupt = command.Processing.InterruptResult
		}
		rows, err := q.FinalizeClearDevDirectionTaskProcessingCAS(ctx, gen.FinalizeClearDevDirectionTaskProcessingCASParams{
			OccupiedAt: occupiedAt, FinalizedAt: nullableTimePtr(&command.At), CheckpointID: nullableString(checkpointID),
			CancelResult: string(command.Processing.CancelResult), ReasonCode: string(command.Processing.ReasonCode),
			NextInterruptResult: interrupt, ID: processingRow.ID,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return errors.New("direction task processing could not be finalized")
		}
		if command.CancelTask {
			rejected, err = applyClearDevDevelopmentTaskActionGated(ctx, q, cleardev.ActionRequest{
				Action: cleardev.ActionCancelDevelopmentTask, SubjectID: processingRow.TaskID, At: command.At,
			}, false)
			if err != nil {
				return err
			}
			if rejected != nil {
				return rejected
			}
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionProcessing, SubjectID: processingRow.ID,
			Action: cleardev.ActionFinalizeDirectionTask, Outcome: cleardev.EventAccepted,
			Reason: command.Processing.ReasonCode, Source: cleardev.EventSourceControlPlane, CreatedAt: command.At,
		})
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// CreateClearDevDirectionRevision records the v1-to-v2 identity mapping.
// An occupancy-approved freeze with no snapshot tasks is already final; that
// empty set may compile v2 without fabricating task-processing rows.
func (s *Store) CreateClearDevDirectionRevision(ctx context.Context, command cleardev.CreateDirectionRevisionCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "create ClearDev direction revision", func(q *gen.Queries) error {
		if existing, err := q.GetClearDevDirectionRevision(ctx, command.Revision.DirectionRequestID); err == nil {
			if existing.TargetRequirementVersionID == command.Revision.TargetRequirementVersionID {
				return nil
			}
			return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "direction revision already exists"}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		processings, err := q.ListClearDevDirectionTaskProcessings(ctx, command.Revision.DirectionRequestID)
		if err != nil {
			return err
		}
		if len(processings) == 0 {
			gate, gateErr := q.GetClearDevDirectionStopGateByRequest(ctx, command.Revision.DirectionRequestID)
			if gateErr != nil {
				if errors.Is(gateErr, sql.ErrNoRows) {
					return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "v2 compilation requires finalized task processings"}
				}
				return gateErr
			}
			snapshots, snapErr := q.ListClearDevDirectionTaskSnapshots(ctx, gate.ID)
			if snapErr != nil {
				return snapErr
			}
			if len(snapshots) != 0 {
				return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "v2 compilation requires finalized task processings"}
			}
		} else {
			for _, item := range processings {
				if item.Occupancy != string(cleardev.DirectionOccupancyFinal) {
					return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "v2 compilation requires every snapshot task to be final"}
				}
			}
		}
		return q.InsertClearDevDirectionRevision(ctx, gen.InsertClearDevDirectionRevisionParams{
			DirectionRequestID: command.Revision.DirectionRequestID, PreviousRequirementVersionID: command.Revision.PreviousRequirementVersionID,
			TargetRequirementVersionID: command.Revision.TargetRequirementVersionID, CreatedAt: command.Revision.CreatedAt,
		})
	})
}

// RecordClearDevDirectionClarification stores 0111 compile questions for this request.
func (s *Store) RecordClearDevDirectionClarification(ctx context.Context, command cleardev.RecordDirectionClarificationCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "record ClearDev direction clarification", func(q *gen.Queries) error {
		request, err := q.GetClearDevDirectionRequest(ctx, command.DirectionRequestID)
		if err != nil {
			return err
		}
		requirementRow, err := q.GetClearDevRequirement(ctx, request.DevelopmentProjectID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		if existing, getErr := q.GetClearDevDirectionCompilationRequest(ctx, command.Request.ID); getErr == nil {
			if existing.AgentStepID == command.Request.AgentStepID && int(existing.ClarificationRound) == command.Request.ClarificationRound {
				return nil
			}
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction compilation request already exists")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		if err := insertClearDevDirectionCompilationRequest(ctx, q, command.DirectionRequestID, command.Request); err != nil {
			return err
		}
		for _, question := range command.Questions {
			if err := insertClearDevDirectionClarificationQuestion(ctx, q, question); err != nil {
				return err
			}
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionCompilation, SubjectID: command.Request.ID,
			Action: cleardev.ActionRecordComplexClarification, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, CreatedAt: command.Request.CreatedAt,
		})
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// SubmitClearDevDirectionClarificationAnswers records answers for an 0111 compile round.
func (s *Store) SubmitClearDevDirectionClarificationAnswers(ctx context.Context, command cleardev.SubmitComplexClarificationCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "submit ClearDev direction clarification answers", func(q *gen.Queries) error {
		requestRow, err := q.GetClearDevDirectionCompilationRequest(ctx, command.CompilationRequestID)
		if errors.Is(err, sql.ErrNoRows) {
			rejected = directionRejected(command.DevelopmentRequirementID, cleardev.ReasonPreconditionNotMet, "direction compilation request was not found")
			return nil
		}
		if err != nil {
			return err
		}
		requirementRow, err := q.GetClearDevRequirement(ctx, requestRow.DevelopmentProjectID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		if requestRow.DevelopmentProjectID != command.DevelopmentRequirementID || int(requestRow.ClarificationRound) != command.ClarificationRound {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "answers do not bind the exact direction compilation request")
			return nil
		}
		questions, err := q.ListClearDevDirectionClarificationQuestions(ctx, command.CompilationRequestID)
		if err != nil {
			return err
		}
		existing, err := q.ListClearDevDirectionClarificationAnswers(ctx, command.CompilationRequestID)
		if err != nil {
			return err
		}
		if len(existing) == len(questions) && len(questions) > 0 {
			return nil
		}
		wanted := map[string]struct{}{}
		for _, question := range questions {
			wanted[question.QuestionKey] = struct{}{}
		}
		if len(command.Answers) != len(wanted) {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "answers must cover every question once")
			return nil
		}
		for _, answer := range command.Answers {
			if _, ok := wanted[answer.QuestionKey]; !ok || strings.TrimSpace(answer.Text) == "" {
				rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid direction clarification answer")
				return nil
			}
			delete(wanted, answer.QuestionKey)
			if err := q.InsertClearDevDirectionClarificationAnswer(ctx, gen.InsertClearDevDirectionClarificationAnswerParams{
				CompilationRequestID: command.CompilationRequestID, QuestionKey: answer.QuestionKey, Text: answer.Text, CreatedAt: command.At,
			}); err != nil {
				return err
			}
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionCompilation, SubjectID: command.CompilationRequestID,
			Action: cleardev.ActionRecordComplexAnswers, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, CreatedAt: command.At,
		})
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// GetClearDevDirectionCompilationRequest loads one 0111 compile request by id.
func (s *Store) GetClearDevDirectionCompilationRequest(ctx context.Context, id string) (cleardev.ComplexCompilationRequest, bool, error) {
	row, err := s.qr.GetClearDevDirectionCompilationRequest(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.ComplexCompilationRequest{}, false, nil
	}
	if err != nil {
		return cleardev.ComplexCompilationRequest{}, false, err
	}
	return cleardev.ComplexCompilationRequest{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, AgentStepID: row.AgentStepID,
		ClarificationRound: int(row.ClarificationRound), CompilationContextSHA256: row.CompilationContextSha256,
		AdditionalRoundReason: row.AdditionalRoundReason, CreatedAt: row.CreatedAt,
	}, true, nil
}

// SettleClearDevDirectionCompilation records a v2 compile result and optional draft version.
func (s *Store) SettleClearDevDirectionCompilation(ctx context.Context, command cleardev.SettleDirectionCompilationCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "settle ClearDev direction compilation", func(q *gen.Queries) error {
		request, err := q.GetClearDevDirectionRequest(ctx, command.DirectionRequestID)
		if err != nil {
			return err
		}
		requirementRow, err := q.GetClearDevRequirement(ctx, request.DevelopmentProjectID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		revision, err := q.GetClearDevDirectionRevision(ctx, command.DirectionRequestID)
		if err != nil {
			return err
		}
		existing, err := q.ListClearDevDirectionCompilations(ctx, command.DirectionRequestID)
		if err != nil {
			return err
		}
		for _, item := range existing {
			if item.CompilationRequestID == command.Compilation.CompilationRequestID {
				return nil
			}
		}
		if _, getErr := q.GetClearDevDirectionCompilationRequest(ctx, command.Compilation.CompilationRequestID); errors.Is(getErr, sql.ErrNoRows) {
			if err := insertClearDevDirectionCompilationRequest(ctx, q, command.DirectionRequestID, command.Request); err != nil {
				return err
			}
		} else if getErr != nil {
			return getErr
		}
		if err := q.InsertClearDevDirectionCompilation(ctx, gen.InsertClearDevDirectionCompilationParams{
			ID: command.Compilation.ID, DirectionRequestID: command.DirectionRequestID, DevelopmentProjectID: requirement.ID,
			CompilationRequestID: command.Compilation.CompilationRequestID, AgentStepID: command.Compilation.AgentStepID,
			Outcome: command.Compilation.Outcome, Summary: command.Compilation.Summary,
			NormalizedRequirementJson: command.Compilation.NormalizedRequirementJSON, CompilationSha256: command.Compilation.CompilationSHA256,
			TurnID: command.Compilation.TurnID, FinalMessageID: command.Compilation.FinalMessageID,
			RawMessageText: command.Compilation.RawMessageText, RawMessageSha256: command.Compilation.RawMessageSHA256,
			CreatedAt: command.Compilation.CreatedAt,
		}); err != nil {
			return err
		}
		if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDirectionCompilation, SubjectID: command.Compilation.ID,
			Action: cleardev.ActionRecordComplexCompilation, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, CreatedAt: command.Compilation.CreatedAt,
		}); err != nil {
			return err
		}
		if command.Compilation.Outcome != "READY" {
			return nil
		}
		for _, item := range command.IDMaps {
			if err := q.InsertClearDevDirectionIDMap(ctx, gen.InsertClearDevDirectionIDMapParams{
				CompilationID: command.Compilation.ID, Kind: item.Kind, TemporaryKey: item.TemporaryKey,
				StableID: item.StableID, Ordinal: int64(item.Ordinal),
			}); err != nil {
				return err
			}
		}
		version := command.Version
		if version.ID != revision.TargetRequirementVersionID || version.DevelopmentRequirementID != requirement.ID ||
			version.Status != cleardev.RequirementVersionStatusDraft || version.TaskSetVersion != 0 ||
			!requirementDigestMatches(version.RequirementText, version.SHA256) {
			rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid compiled v2 requirement version")
			return nil
		}
		if err := insertClearDevRequirementVersion(ctx, q, version); err != nil {
			return err
		}
		if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectRequirementVersion, SubjectID: version.ID,
			Action: cleardev.ActionCreateRequirementVersion, TargetState: string(cleardev.RequirementVersionStatusDraft),
			Outcome: cleardev.EventAccepted, Source: cleardev.EventSourceControlPlane, CreatedAt: command.At,
		}); err != nil {
			return err
		}
		rejected, err = applyClearDevRequirementVersionAction(ctx, q, cleardev.ActionRequest{
			Action: cleardev.ActionSubmitRequirementConfirmation, SubjectID: version.ID, At: command.At,
		})
		return err
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

func settleDirectionAgentStep(ctx context.Context, q *gen.Queries, step cleardev.AgentStep, changed *bool, rejected **cleardev.RuleError) error {
	row, err := q.GetClearDevDirectionAgentStep(ctx, step.ID)
	if err != nil {
		return err
	}
	existing := clearDevDirectionAgentStepFromGen(row)
	bindingRow, err := q.GetClearDevComplexRoleBinding(ctx, existing.RoleBindingID)
	if err != nil {
		return err
	}
	binding := clearDevComplexRoleBindingFromGen(bindingRow)
	requirement, _, err := loadComplexRequirement(ctx, q, binding.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	if existing.SendStatus == step.SendStatus && sameComplexAgentStepTerminal(existing, step) {
		return nil
	}
	var rows int64
	switch step.SendStatus {
	case cleardev.AgentStepSendStatusSettled:
		if existing.SendStatus != cleardev.AgentStepSendStatusSent {
			*rejected = directionRejected(requirement.ID, cleardev.ReasonInvalidTransition, "direction agent step is not awaiting a sent result")
			return nil
		}
		rows, err = q.SettleClearDevDirectionAgentStepCAS(ctx, gen.SettleClearDevDirectionAgentStepCASParams{
			TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID),
			FinalMessageText: nullableString(step.FinalMessageText), MessageSha256: nullableString(step.MessageSHA256),
			CompletedAt: nullableTimePtr(step.CompletedAt), ID: step.ID,
		})
	case cleardev.AgentStepSendStatusFailed:
		rows, err = q.FailClearDevDirectionAgentStepCAS(ctx, gen.FailClearDevDirectionAgentStepCASParams{
			FailedAt: nullableTimePtr(step.FailedAt), ReasonCode: string(step.ReasonCode), ID: step.ID,
		})
	default:
		*rejected = directionRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "direction agent step must settle or fail")
		return nil
	}
	if err != nil {
		return err
	}
	if rows != 1 {
		*rejected = directionRejected(requirement.ID, cleardev.ReasonInvalidTransition, "direction agent-step compare-and-swap lost")
		return nil
	}
	*changed = true
	outcome := cleardev.EventAccepted
	if step.SendStatus == cleardev.AgentStepSendStatusFailed {
		outcome = cleardev.EventRejected
	}
	return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectDirectionCompilation, SubjectID: step.ID,
		Action: cleardev.ActionSettleComplexAgentStep, Outcome: outcome, Reason: step.ReasonCode,
		Source: cleardev.EventSourceControlPlane, SourceAOSessionID: binding.AOSessionID, CreatedAt: terminalComplexAgentStepTime(step),
	})
}

func insertDirectionDecisionRequest(
	ctx context.Context,
	q *gen.Queries,
	requirement cleardev.DevelopmentRequirement,
	intent cleardev.DirectionIntent,
	request cleardev.DirectionRequest,
	gate cleardev.DirectionStopGate,
	taskCount int,
	requestID string,
	at time.Time,
) error {
	if _, err := q.GetClearDevHumanDecisionRequestByDirectionRequestID(ctx, request.ID); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	binding := cleardev.ApproveDirectionChangeBinding{
		DevelopmentRequirementID: requirement.ID, DirectionRequestID: request.ID,
		RequirementVersionID: intent.RequirementVersionID, RequirementVersionSHA256: intent.RequirementSHA256,
		TaskSetVersion: gate.TaskSetVersion, SnapshotSHA256: gate.SnapshotSHA256,
	}
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	normalized, err := cleardev.ParseApproveDirectionChangeBinding(bindingJSON)
	if err != nil {
		return err
	}
	bindingJSON, err = json.Marshal(normalized)
	if err != nil {
		return err
	}
	display := cleardev.ApproveDirectionChangeDisplay(requirement, intent.Message, request.Summary, intent.RequirementSHA256, gate.SnapshotSHA256, taskCount)
	displayJSON, err := json.Marshal(display)
	if err != nil {
		return err
	}
	digest, err := cleardev.HumanDecisionContentSHA256(cleardev.HumanDecisionKindApproveDirectionChange, bindingJSON, display)
	if err != nil {
		return err
	}
	if strings.TrimSpace(requestID) == "" {
		requestID = uuid.NewString()
	}
	if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{
		ID: requestID, DevelopmentProjectID: requirement.ID, DecisionKind: cleardev.HumanDecisionKindApproveDirectionChange,
		BindingSchemaVersion: int64(cleardev.ApproveDirectionChangeBindingVersion), BindingJson: string(bindingJSON),
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

func insertDirectionCandidate(ctx context.Context, q *gen.Queries, requirement cleardev.DevelopmentRequirement, taskID string, candidate cleardev.CandidateCommit) (string, error) {
	existing, err := q.ListClearDevCandidateCommits(ctx, taskID)
	if err != nil {
		return "", err
	}
	for _, item := range existing {
		if item.CommitSha == candidate.CommitSHA {
			return item.ID, nil
		}
	}
	binding, err := q.GetClearDevWorkItemDispatchBinding(ctx, taskID)
	if err != nil {
		return "", err
	}
	permission, err := q.GetLatestClearDevPermissionVersion(ctx, taskID)
	if err != nil {
		return "", err
	}
	sequence, err := q.NextClearDevCandidateSequence(ctx, taskID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(candidate.ID) == "" {
		candidate.ID = uuid.NewString()
	}
	if binding.AcceptedDispatchID.Valid && binding.DispatchBaseCommitSha.Valid {
		if err := q.InsertClearDevStandardCandidateCommit(ctx, gen.InsertClearDevStandardCandidateCommitParams{
			ID: candidate.ID, WorkItemID: taskID, Sequence: sequence, AoSessionID: candidate.AOSessionID,
			PermissionVersionID: permission.ID, DispatchID: binding.AcceptedDispatchID, BaseCommitSha: binding.DispatchBaseCommitSha,
			CommitSha: candidate.CommitSHA, CreatedAt: candidate.CreatedAt,
		}); err != nil {
			return "", err
		}
	} else {
		if err := q.InsertClearDevCandidateCommit(ctx, gen.InsertClearDevCandidateCommitParams{
			ID: candidate.ID, WorkItemID: taskID, Sequence: sequence, AoSessionID: candidate.AOSessionID,
			PermissionVersionID: permission.ID, CommitSha: candidate.CommitSHA, CreatedAt: candidate.CreatedAt,
		}); err != nil {
			return "", err
		}
	}
	if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectCandidate, SubjectID: candidate.ID,
		Action: cleardev.ActionRegisterCandidate, Outcome: cleardev.EventAccepted,
		Source: cleardev.EventSourceControlPlane, SourceAOSessionID: candidate.AOSessionID, CreatedAt: candidate.CreatedAt,
	}); err != nil {
		return "", err
	}
	return candidate.ID, nil
}

func loadDirectionCompilationFacts(ctx context.Context, q *gen.Queries, directionRequestID string, snapshot *cleardev.DirectionChangeSnapshot) error {
	requests, err := q.ListClearDevDirectionCompilationRequests(ctx, directionRequestID)
	if err != nil {
		return err
	}
	for _, request := range requests {
		snapshot.CompilationRequests = append(snapshot.CompilationRequests, cleardev.ComplexCompilationRequest{
			ID: request.ID, DevelopmentRequirementID: request.DevelopmentProjectID, AgentStepID: request.AgentStepID,
			ClarificationRound: int(request.ClarificationRound), CompilationContextSHA256: request.CompilationContextSha256,
			AdditionalRoundReason: request.AdditionalRoundReason, CreatedAt: request.CreatedAt,
		})
		questions, listErr := q.ListClearDevDirectionClarificationQuestions(ctx, request.ID)
		if listErr != nil {
			return listErr
		}
		for _, question := range questions {
			keys := []string{}
			if err := json.Unmarshal([]byte(question.RequirementKeysJson), &keys); err != nil {
				return err
			}
			snapshot.Questions = append(snapshot.Questions, cleardev.ComplexClarificationQuestion{
				CompilationRequestID: question.CompilationRequestID, QuestionKey: question.QuestionKey,
				Text: question.Text, Reason: question.Reason, RequirementKeys: keys, Ordinal: int(question.Ordinal),
			})
		}
		answers, listErr := q.ListClearDevDirectionClarificationAnswers(ctx, request.ID)
		if listErr != nil {
			return listErr
		}
		for _, answer := range answers {
			snapshot.Answers = append(snapshot.Answers, cleardev.ComplexClarificationAnswer{
				CompilationRequestID: answer.CompilationRequestID, QuestionKey: answer.QuestionKey,
				Text: answer.Text, CreatedAt: answer.CreatedAt,
			})
		}
	}
	compilations, err := q.ListClearDevDirectionCompilations(ctx, directionRequestID)
	if err != nil {
		return err
	}
	for _, item := range compilations {
		snapshot.Compilations = append(snapshot.Compilations, cleardev.ComplexCompilation{
			ID: item.ID, DevelopmentRequirementID: item.DevelopmentProjectID, CompilationRequestID: item.CompilationRequestID,
			AgentStepID: item.AgentStepID, Outcome: item.Outcome, Summary: item.Summary,
			NormalizedRequirementJSON: item.NormalizedRequirementJson, CompilationSHA256: item.CompilationSha256,
			TurnID: item.TurnID, FinalMessageID: item.FinalMessageID, RawMessageText: item.RawMessageText,
			RawMessageSHA256: item.RawMessageSha256, CreatedAt: item.CreatedAt,
		})
		maps, listErr := q.ListClearDevDirectionIDMaps(ctx, item.ID)
		if listErr != nil {
			return listErr
		}
		for _, mapped := range maps {
			snapshot.IDMaps = append(snapshot.IDMaps, cleardev.ComplexIDMap{
				CompilationID: mapped.CompilationID, Kind: mapped.Kind, TemporaryKey: mapped.TemporaryKey,
				StableID: mapped.StableID, Ordinal: int(mapped.Ordinal),
			})
		}
	}
	return nil
}

func insertClearDevDirectionAgentStep(ctx context.Context, q *gen.Queries, step cleardev.AgentStep) error {
	return q.InsertClearDevDirectionAgentStep(ctx, gen.InsertClearDevDirectionAgentStepParams{
		ID: step.ID, RoleBindingID: step.RoleBindingID, StepKind: string(step.Kind), RequestID: step.RequestID,
		ClientMessageID: step.ClientMessageID, PromptSha256: step.PromptSHA256, SendStatus: string(step.SendStatus),
		TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID),
		FinalMessageText: nullableString(step.FinalMessageText), MessageSha256: nullableString(step.MessageSHA256),
		RequestedAt: step.RequestedAt, SentAt: nullableTimePtr(step.SentAt), CompletedAt: nullableTimePtr(step.CompletedAt),
		FailedAt: nullableTimePtr(step.FailedAt), ReasonCode: string(step.ReasonCode),
	})
}

func insertClearDevDirectionRequest(ctx context.Context, q *gen.Queries, request cleardev.DirectionRequest) error {
	ids, err := json.Marshal(nonNilStrings(request.AffectedRequirementIDs))
	if err != nil {
		return err
	}
	return q.InsertClearDevDirectionRequest(ctx, gen.InsertClearDevDirectionRequestParams{
		ID: request.ID, IntentRequestID: request.IntentRequestID, DevelopmentProjectID: request.DevelopmentRequirementID,
		RequirementVersionID: request.RequirementVersionID, Summary: request.Summary,
		AffectedRequirementIdsJson: string(ids), ResultSha256: request.ResultSHA256,
		AgentStepID: request.AgentStepID, CreatedAt: request.CreatedAt,
	})
}

func insertClearDevDirectionCheckpoint(ctx context.Context, q *gen.Queries, checkpoint cleardev.DirectionCheckpoint) error {
	return q.InsertClearDevDirectionCheckpoint(ctx, gen.InsertClearDevDirectionCheckpointParams{
		ID: checkpoint.ID, ProcessingID: checkpoint.ProcessingID, TaskID: checkpoint.TaskID,
		SessionID: nullableString(checkpoint.SessionID), WorktreePath: checkpoint.WorktreePath,
		BaselineSha: checkpoint.BaselineSHA, HeadSha: checkpoint.HeadSHA,
		Dirty: boolToInt64(checkpoint.Dirty), Staged: boolToInt64(checkpoint.Staged), Untracked: boolToInt64(checkpoint.Untracked),
		ChangeSummaryJson: checkpoint.ChangeSummaryJSON, CandidateCommitID: nullableString(checkpoint.CandidateCommitID),
		Kind: string(checkpoint.Kind), CreatedAt: checkpoint.CreatedAt,
	})
}

func insertClearDevDirectionCompilationRequest(ctx context.Context, q *gen.Queries, directionRequestID string, request cleardev.ComplexCompilationRequest) error {
	return q.InsertClearDevDirectionCompilationRequest(ctx, gen.InsertClearDevDirectionCompilationRequestParams{
		ID: request.ID, DirectionRequestID: directionRequestID, DevelopmentProjectID: request.DevelopmentRequirementID,
		AgentStepID: request.AgentStepID, ClarificationRound: int64(request.ClarificationRound),
		CompilationContextSha256: request.CompilationContextSHA256, AdditionalRoundReason: request.AdditionalRoundReason,
		CreatedAt: request.CreatedAt,
	})
}

func insertClearDevDirectionClarificationQuestion(ctx context.Context, q *gen.Queries, question cleardev.ComplexClarificationQuestion) error {
	keys, err := json.Marshal(nonNilStrings(question.RequirementKeys))
	if err != nil {
		return err
	}
	return q.InsertClearDevDirectionClarificationQuestion(ctx, gen.InsertClearDevDirectionClarificationQuestionParams{
		CompilationRequestID: question.CompilationRequestID, QuestionKey: question.QuestionKey,
		Text: question.Text, Reason: question.Reason, RequirementKeysJson: string(keys), Ordinal: int64(question.Ordinal),
	})
}

func directionRejected(requirementID string, reason cleardev.ReasonCode, message string) *cleardev.RuleError {
	return &cleardev.RuleError{Code: reason, Message: fmt.Sprintf("ClearDev direction change %s: %s", requirementID, message)}
}

func ptrDirectionIntent(intent cleardev.DirectionIntent) *cleardev.DirectionIntent {
	cloned := intent
	return &cloned
}

func clearDevDirectionIntentFromGen(row gen.CleardevDirectionIntent) cleardev.DirectionIntent {
	return cleardev.DirectionIntent{
		RequestID: row.RequestID, DevelopmentRequirementID: row.DevelopmentProjectID, RequirementVersionID: row.RequirementVersionID,
		RequirementSHA256: row.RequirementSha256, Message: row.Message, MessageSHA256: row.MessageSha256,
		StewardRoleBindingID: row.StewardRoleBindingID, DirectionRequestID: row.DirectionRequestID,
		AgentStepID: row.AgentStepID, CreatedAt: row.CreatedAt,
	}
}

func clearDevDirectionRequestFromGen(row gen.CleardevDirectionRequest) (cleardev.DirectionRequest, error) {
	ids := []string{}
	if err := json.Unmarshal([]byte(row.AffectedRequirementIdsJson), &ids); err != nil {
		return cleardev.DirectionRequest{}, err
	}
	return cleardev.DirectionRequest{
		ID: row.ID, IntentRequestID: row.IntentRequestID, DevelopmentRequirementID: row.DevelopmentProjectID,
		RequirementVersionID: row.RequirementVersionID, Summary: row.Summary, AffectedRequirementIDs: ids,
		ResultSHA256: row.ResultSha256, AgentStepID: row.AgentStepID, CreatedAt: row.CreatedAt,
	}, nil
}

func clearDevDirectionStopGateFromGen(row gen.CleardevDirectionStopGate) cleardev.DirectionStopGate {
	gate := cleardev.DirectionStopGate{
		ID: row.ID, DirectionRequestID: row.DirectionRequestID, DevelopmentRequirementID: row.DevelopmentProjectID,
		RequirementVersionID: row.RequirementVersionID, TaskSetVersion: row.TaskSetVersion, SnapshotSHA256: row.SnapshotSha256,
		Status: cleardev.DirectionStopGateStatus(row.Status), CloseReason: cleardev.ReasonCode(row.CloseReason), CreatedAt: row.CreatedAt,
	}
	if row.ClosedAt.Valid {
		closed := row.ClosedAt.Time
		gate.ClosedAt = &closed
	}
	return gate
}

func clearDevDirectionProcessingFromGen(row gen.CleardevDirectionTaskProcessing) cleardev.DirectionTaskProcessing {
	item := cleardev.DirectionTaskProcessing{
		ID: row.ID, DirectionRequestID: row.DirectionRequestID, TaskID: row.TaskID,
		Occupancy: cleardev.DirectionOccupancy(row.Occupancy), InterruptResult: row.InterruptResult,
		CheckpointID: row.CheckpointID.String, CancelResult: cleardev.DirectionCancelResult(row.CancelResult),
		ReasonCode: cleardev.ReasonCode(row.ReasonCode),
	}
	if row.OccupiedAt.Valid {
		value := row.OccupiedAt.Time
		item.OccupiedAt = &value
	}
	if row.FinalizedAt.Valid {
		value := row.FinalizedAt.Time
		item.FinalizedAt = &value
	}
	return item
}

func clearDevDirectionCheckpointFromGen(row gen.CleardevDirectionCheckpoint) cleardev.DirectionCheckpoint {
	return cleardev.DirectionCheckpoint{
		ID: row.ID, ProcessingID: row.ProcessingID, TaskID: row.TaskID, SessionID: row.SessionID.String,
		WorktreePath: row.WorktreePath, BaselineSHA: row.BaselineSha, HeadSHA: row.HeadSha,
		Dirty: row.Dirty != 0, Staged: row.Staged != 0, Untracked: row.Untracked != 0,
		ChangeSummaryJSON: row.ChangeSummaryJson, CandidateCommitID: row.CandidateCommitID.String,
		Kind: cleardev.DirectionCheckpointKind(row.Kind), CreatedAt: row.CreatedAt,
	}
}

func clearDevDirectionAgentStepFromGen(row gen.CleardevDirectionAgentStep) cleardev.AgentStep {
	step := cleardev.AgentStep{
		ID: row.ID, RoleBindingID: row.RoleBindingID, Kind: cleardev.AgentStepKind(row.StepKind), RequestID: row.RequestID,
		ClientMessageID: row.ClientMessageID, PromptSHA256: row.PromptSha256, SendStatus: cleardev.AgentStepSendStatus(row.SendStatus),
		TurnID: row.TurnID.String, FinalMessageID: row.FinalMessageID.String, FinalMessageText: row.FinalMessageText.String,
		MessageSHA256: row.MessageSha256.String, ReasonCode: cleardev.ReasonCode(row.ReasonCode), RequestedAt: row.RequestedAt,
	}
	if row.SentAt.Valid {
		value := row.SentAt.Time
		step.SentAt = &value
	}
	if row.CompletedAt.Valid {
		value := row.CompletedAt.Time
		step.CompletedAt = &value
	}
	if row.FailedAt.Valid {
		value := row.FailedAt.Time
		step.FailedAt = &value
	}
	return step
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

// BindClearDevRunningTaskWorkspaceCommand is the test-only input that binds a
// RUNNING work item to an accepted STANDARD dispatch and Builder session.
type BindClearDevRunningTaskWorkspaceCommand struct {
	DevelopmentRequirementID string
	RunningTaskID            string
	BuilderSessionID         string
	StewardSessionID         string
	PlannerSessionID         string
	BuilderSpawnKey          string
	PlannerSpawnKey          string
	StewardSpawnKey          string
	BaseCommitSHA            string
	At                       time.Time
}

// BindClearDevRunningTaskWorkspaceForTest inserts settled S02 dispatch facts
// and binds the RUNNING work item to that accepted dispatch. Tests use it so
// production ObserveWorkspace can read the Builder session's managed worktree.
// The Steward binding is then ended so daemon boot does not resume STANDARD
// Codex on this requirement.
func (s *Store) BindClearDevRunningTaskWorkspaceForTest(ctx context.Context, command BindClearDevRunningTaskWorkspaceCommand) error {
	if !validS02SHA1(command.BaseCommitSHA) {
		return fmt.Errorf("test dispatch base commit SHA is not a 40-character SHA-1")
	}
	if strings.TrimSpace(command.RunningTaskID) == "" || strings.TrimSpace(command.BuilderSessionID) == "" ||
		strings.TrimSpace(command.StewardSessionID) == "" || strings.TrimSpace(command.PlannerSessionID) == "" ||
		strings.TrimSpace(command.BuilderSpawnKey) == "" || strings.TrimSpace(command.PlannerSpawnKey) == "" ||
		strings.TrimSpace(command.StewardSpawnKey) == "" {
		return fmt.Errorf("test dispatch binding is missing a required identity")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "bind ClearDev running task workspace for test", func(q *gen.Queries) error {
		requirement, confirmed, err := currentStandardRequirement(ctx, q, command.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		task, err := q.GetClearDevDevelopmentTask(ctx, command.RunningTaskID)
		if err != nil {
			return err
		}
		if task.DevelopmentProjectID != requirement.ID || task.ContractVersionID != confirmed.ID ||
			task.State != string(cleardev.DevelopmentTaskStatusRunning) {
			return fmt.Errorf("running task %s is not bound to the confirmed version", command.RunningTaskID)
		}
		if confirmed.TaskSetVersion < 1 {
			return fmt.Errorf("confirmed version has no task-set increment to bind")
		}
		at := command.At
		boundAt := at
		stewardID := uuid.NewString()
		plannerID := uuid.NewString()
		builderID := uuid.NewString()
		dispatchID := uuid.NewString()
		if err := insertClearDevStandardRoleBinding(ctx, q, cleardev.RoleSessionBinding{
			ID: stewardID, DevelopmentRequirementID: requirement.ID, RequirementVersionID: confirmed.ID,
			Role: cleardev.StandardRoleSteward, SessionCreationIdempotencyKey: command.StewardSpawnKey,
			AOSessionID: command.StewardSessionID, Status: cleardev.RoleBindingStatusBound,
			RequestedAt: at, BoundAt: &boundAt,
		}); err != nil {
			return err
		}
		if err := insertClearDevStandardRoleBinding(ctx, q, cleardev.RoleSessionBinding{
			ID: plannerID, DevelopmentRequirementID: requirement.ID, RequirementVersionID: confirmed.ID,
			Role: cleardev.StandardRoleEngineeringPlanner, SessionCreationIdempotencyKey: command.PlannerSpawnKey,
			AOSessionID: command.PlannerSessionID, Status: cleardev.RoleBindingStatusBound,
			RequestedAt: at, BoundAt: &boundAt,
		}); err != nil {
			return err
		}
		planStep := settledStandardAgentStep(plannerID, cleardev.AgentStepEngineeringPlan, at, `{"ok":true}`)
		if err := insertClearDevStandardAgentStep(ctx, q, planStep); err != nil {
			return err
		}
		planJSON := `{"schemaVersion":1,"kind":"ENGINEERING_PLAN"}`
		plan := cleardev.EngineeringPlan{
			ID: uuid.NewString(), RequirementVersionID: confirmed.ID, RequirementSHA256: confirmed.Sha256,
			Version: 1, PlannerRoleBindingID: plannerID, AgentStepID: planStep.ID,
			TurnID: planStep.TurnID, FinalMessageID: planStep.FinalMessageID, PlanJSON: planJSON,
			PlanSHA256: sha256Hex(planJSON), CreatedAt: at,
		}
		if err := q.InsertClearDevStandardEngineeringPlan(ctx, standardEngineeringPlanInsertParams(plan)); err != nil {
			return err
		}
		reviewStep := settledStandardAgentStep(stewardID, cleardev.AgentStepPlanReview, at, "PLAN_ACCEPTABLE")
		if err := insertClearDevStandardAgentStep(ctx, q, reviewStep); err != nil {
			return err
		}
		review := cleardev.PlanReview{
			ID: uuid.NewString(), EngineeringPlanID: plan.ID, StewardRoleBindingID: stewardID,
			AgentStepID: reviewStep.ID, TurnID: reviewStep.TurnID, FinalMessageID: reviewStep.FinalMessageID,
			Verdict: cleardev.PlanReviewApproved, ReasonCode: "PLAN_ACCEPTABLE",
			Summary: "S05 test dispatch binding", CreatedAt: at,
		}
		if err := q.InsertClearDevStandardPlanReview(ctx, standardPlanReviewInsertParams(review)); err != nil {
			return err
		}
		dispatchStep := settledStandardAgentStep(stewardID, cleardev.AgentStepDispatchRequest, at, "DISPATCH")
		if err := insertClearDevStandardAgentStep(ctx, q, dispatchStep); err != nil {
			return err
		}
		if err := insertClearDevStandardRoleBinding(ctx, q, cleardev.RoleSessionBinding{
			ID: builderID, DevelopmentRequirementID: requirement.ID, RequirementVersionID: confirmed.ID,
			Role: cleardev.StandardRoleBuilder, DispatchID: dispatchID, DevelopmentTaskID: command.RunningTaskID,
			SessionCreationIdempotencyKey: command.BuilderSpawnKey, Status: cleardev.RoleBindingStatusRequested,
			RequestedAt: at,
		}); err != nil {
			return err
		}
		packJSON := `{"schemaVersion":1,"mode":"STANDARD"}`
		dispatch := cleardev.Dispatch{
			ID: dispatchID, RequirementVersionID: confirmed.ID, EngineeringPlanID: plan.ID,
			PlanReviewID: review.ID, StewardRoleBindingID: stewardID, AgentStepID: dispatchStep.ID,
			Mode: cleardev.WorkModeStandard, PreallocatedDevelopmentTaskID: command.RunningTaskID,
			ExpectedTaskSetVersion: confirmed.TaskSetVersion - 1, ExecutionPackageJSON: packJSON,
			ExecutionPackageSHA256: sha256Hex(packJSON), BuilderSessionIdempotencyKey: command.BuilderSpawnKey,
			BuilderRoleBindingID: builderID, Status: cleardev.DispatchStatusPending, RequestedAt: at,
		}
		if err := q.InsertClearDevStandardDispatch(ctx, standardDispatchInsertParams(dispatch)); err != nil {
			return err
		}
		boundRows, err := q.BindClearDevStandardRoleBindingCAS(ctx, gen.BindClearDevStandardRoleBindingCASParams{
			AoSessionID: nullableString(command.BuilderSessionID), BoundAt: nullableTimePtr(&boundAt), ID: builderID,
		})
		if err != nil {
			return err
		}
		if boundRows != 1 {
			return fmt.Errorf("bind Builder session: CAS affected %d rows", boundRows)
		}
		taskRows, err := q.BindClearDevWorkItemAcceptedDispatch(ctx, gen.BindClearDevWorkItemAcceptedDispatchParams{
			AcceptedDispatchID: nullableString(dispatchID), DispatchBaseCommitSha: nullableString(command.BaseCommitSHA),
			UpdatedAt: at, ID: command.RunningTaskID,
		})
		if err != nil {
			return err
		}
		if taskRows != 1 {
			return fmt.Errorf("bind running task dispatch: CAS affected %d rows", taskRows)
		}
		acceptRows, err := q.AcceptClearDevStandardDispatchCAS(ctx, gen.AcceptClearDevStandardDispatchCASParams{
			BaseCommitSha: nullableString(command.BaseCommitSHA), DecidedAt: nullableTimePtr(&at), ID: dispatchID,
		})
		if err != nil {
			return err
		}
		if acceptRows != 1 {
			return fmt.Errorf("accept test dispatch: CAS affected %d rows", acceptRows)
		}
		endedRows, err := q.EndClearDevStandardRoleBindingCAS(ctx, gen.EndClearDevStandardRoleBindingCASParams{
			ReasonCode: "DIRECTION_PREP_STOP", EndedAt: nullableTimePtr(&at), ID: stewardID,
		})
		if err != nil {
			return err
		}
		if endedRows != 1 {
			return fmt.Errorf("end Steward after test dispatch: CAS affected %d rows", endedRows)
		}
		return nil
	})
}

func settledStandardAgentStep(roleBindingID string, kind cleardev.AgentStepKind, at time.Time, body string) cleardev.AgentStep {
	sentAt := at
	completedAt := at
	return cleardev.AgentStep{
		ID: uuid.NewString(), RoleBindingID: roleBindingID, Kind: kind,
		RequestID: uuid.NewString(), ClientMessageID: uuid.NewString(),
		PromptSHA256: sha256Hex("s05-prep-" + string(kind)), SendStatus: cleardev.AgentStepSendStatusSettled,
		TurnID: uuid.NewString(), FinalMessageID: uuid.NewString(), FinalMessageText: body,
		MessageSHA256: sha256Hex(body), RequestedAt: at, SentAt: &sentAt, CompletedAt: &completedAt,
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
