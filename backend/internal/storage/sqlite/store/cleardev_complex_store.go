package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// CreateClearDevComplexRequirement creates the development container, immutable
// original PRD, complex marker, preallocated target v1 identity, and Steward
// binding. It does not create a requirement version.
func (s *Store) CreateClearDevComplexRequirement(ctx context.Context, command cleardev.CreateComplexRequirementCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "create ClearDev complex requirement", func(q *gen.Queries) error {
		return createComplexRequirementTx(ctx, q, command)
	})
}

// Shared by ordinary requirements and atomic product/stage registration.
func createComplexRequirementTx(ctx context.Context, q *gen.Queries, command cleardev.CreateComplexRequirementCommand) error {
	requirement := command.Requirement
	binding := command.StewardRoleBinding
	if strings.TrimSpace(requirement.ID) == "" || strings.TrimSpace(requirement.Name) == "" || requirement.CancelledAt != nil ||
		requirement.CancelReason != cleardev.ReasonNone || requirement.CancelReasonText != "" ||
		strings.TrimSpace(command.OriginalPRDText) == "" || !requirementDigestMatches(command.OriginalPRDText, command.OriginalPRDSHA256) ||
		strings.TrimSpace(command.TargetRequirementVersionID) == "" {
		return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "invalid complex ClearDev requirement"}
	}
	if binding.DevelopmentRequirementID != requirement.ID || binding.Role != cleardev.StandardRoleSteward ||
		binding.Status != cleardev.RoleBindingStatusRequested || strings.TrimSpace(binding.ID) == "" ||
		strings.TrimSpace(binding.SessionCreationIdempotencyKey) == "" || binding.AOSessionID != "" {
		return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "invalid complex Steward binding"}
	}
	aoProject, err := q.GetProject(ctx, domain.ProjectID(requirement.AOProjectID))
	if err != nil {
		return fmt.Errorf("get AO project %s: %w", requirement.AOProjectID, err)
	}
	if domain.ProjectKind(aoProject.Kind).WithDefault() != domain.ProjectKindSingleRepo {
		return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "ClearDev S01 requires a single_repo AO project"}
	}
	if command.BenchmarkBinding != nil {
		benchmark := *command.BenchmarkBinding
		if benchmark.DevelopmentRequirementID != requirement.ID || benchmark.AOProjectID != requirement.AOProjectID ||
			filepath.Clean(benchmark.ProjectRoot) != filepath.Clean(aoProject.Path) || benchmark.ManifestSHA256 == "" ||
			benchmark.CheckProfileSHA256 != cleardev.BenchmarkCheckProfileSHA256(benchmark.CheckPrefix) || benchmark.CreatedAt.IsZero() {
			return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "invalid ClearDev benchmark binding"}
		}
		if _, existingErr := q.GetClearDevBenchmarkBindingByManifest(ctx, benchmark.ManifestSHA256); existingErr == nil {
			return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "benchmark manifest is already bound"}
		} else if !errors.Is(existingErr, sql.ErrNoRows) {
			return existingErr
		}
	}
	if err := q.InsertClearDevRequirement(ctx, gen.InsertClearDevRequirementParams{
		ID: requirement.ID, AoProjectID: requirement.AOProjectID, Name: requirement.Name,
		CreatedAt: requirement.CreatedAt, UpdatedAt: requirement.UpdatedAt,
	}); err != nil {
		return fmt.Errorf("insert ClearDev requirement: %w", err)
	}
	if benchmark := command.BenchmarkBinding; benchmark != nil {
		if err := q.InsertClearDevBenchmarkBinding(ctx, gen.InsertClearDevBenchmarkBindingParams{
			DevelopmentRequirementID: benchmark.DevelopmentRequirementID, AoProjectID: benchmark.AOProjectID,
			ProjectRoot: benchmark.ProjectRoot, ManifestPath: benchmark.ManifestPath, ManifestSha256: benchmark.ManifestSHA256,
			FacilityVersion: benchmark.FacilityVersion, Purpose: benchmark.Purpose, PlanUnitID: benchmark.PlanUnitID, Scene: int64(benchmark.Scene),
			GroupID: benchmark.Group, ModePolicy: string(benchmark.Policy), CheckProfile: benchmark.CheckProfile, CheckPrefix: benchmark.CheckPrefix,
			CheckProfileSha256: benchmark.CheckProfileSHA256, PublicInputSha256: benchmark.PublicInputSHA256, MaterialSha256: benchmark.MaterialSHA256,
			CreatedAt: benchmark.CreatedAt,
		}); err != nil {
			return fmt.Errorf("insert ClearDev benchmark binding: %w", err)
		}
	}
	if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectDevelopmentRequirement, SubjectID: requirement.ID,
		Action: cleardev.ActionCreateRequirement, Outcome: cleardev.EventAccepted,
		Source: cleardev.EventSourceControlPlane, CreatedAt: requirement.CreatedAt,
	}); err != nil {
		return err
	}
	if err := q.InsertClearDevComplexRequirement(ctx, gen.InsertClearDevComplexRequirementParams{
		DevelopmentProjectID: requirement.ID, OriginalPrdText: command.OriginalPRDText,
		OriginalPrdSha256: command.OriginalPRDSHA256, TargetRequirementVersionID: command.TargetRequirementVersionID,
		CreatedAt: command.Requirement.CreatedAt,
	}); err != nil {
		return fmt.Errorf("insert complex requirement: %w", err)
	}
	if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectComplexRequirement, SubjectID: requirement.ID,
		Action: cleardev.ActionCreateComplexRequirement, Outcome: cleardev.EventAccepted,
		Source: cleardev.EventSourceControlPlane, CreatedAt: requirement.CreatedAt,
	}); err != nil {
		return err
	}
	if err := insertClearDevComplexRoleBinding(ctx, q, binding); err != nil {
		return err
	}
	return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectComplexRoleBinding, SubjectID: binding.ID,
		Action: cleardev.ActionBindComplexRole, Outcome: cleardev.EventAccepted,
		Source: cleardev.EventSourceControlPlane, CreatedAt: binding.RequestedAt,
	})
}

// GetClearDevComplexPlanning loads the raw S04 history. Display phase is derived
// by the service, not stored here.
func (s *Store) GetClearDevComplexPlanning(ctx context.Context, requirementID string) (cleardev.ComplexPlanningSnapshot, bool, error) {
	row, err := s.qr.GetClearDevComplexRequirement(ctx, requirementID)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.ComplexPlanningSnapshot{}, false, nil
	}
	if err != nil {
		return cleardev.ComplexPlanningSnapshot{}, false, fmt.Errorf("get complex requirement %s: %w", requirementID, err)
	}
	snapshot := cleardev.ComplexPlanningSnapshot{
		Requirement:         clearDevComplexRequirementFromGen(row),
		RoleBindings:        []cleardev.ComplexRoleBinding{},
		AgentSteps:          []cleardev.AgentStep{},
		CompilationRequests: []cleardev.ComplexCompilationRequest{},
		Questions:           []cleardev.ComplexClarificationQuestion{},
		Answers:             []cleardev.ComplexClarificationAnswer{},
		Compilations:        []cleardev.ComplexCompilation{},
		IDMaps:              []cleardev.ComplexIDMap{},
		Plans:               []cleardev.ComplexEngineeringPlan{},
		Reviews:             []cleardev.ComplexPlanReview{},
		Validations:         []cleardev.ComplexPlanValidation{},
	}
	bindings, err := s.qr.ListClearDevComplexRoleBindings(ctx, requirementID)
	if err != nil {
		return cleardev.ComplexPlanningSnapshot{}, false, err
	}
	for _, bindingRow := range bindings {
		binding := clearDevComplexRoleBindingFromGen(bindingRow)
		snapshot.RoleBindings = append(snapshot.RoleBindings, binding)
		steps, listErr := s.qr.ListClearDevComplexAgentSteps(ctx, binding.ID)
		if listErr != nil {
			return cleardev.ComplexPlanningSnapshot{}, false, listErr
		}
		for _, step := range steps {
			snapshot.AgentSteps = append(snapshot.AgentSteps, clearDevComplexAgentStepFromGen(step))
		}
	}
	requests, err := s.qr.ListClearDevComplexCompilationRequests(ctx, requirementID)
	if err != nil {
		return cleardev.ComplexPlanningSnapshot{}, false, err
	}
	for _, requestRow := range requests {
		request := clearDevComplexCompilationRequestFromGen(requestRow)
		snapshot.CompilationRequests = append(snapshot.CompilationRequests, request)
		questions, listErr := s.qr.ListClearDevComplexClarificationQuestions(ctx, request.ID)
		if listErr != nil {
			return cleardev.ComplexPlanningSnapshot{}, false, listErr
		}
		for _, question := range questions {
			converted, convertErr := clearDevComplexQuestionFromGen(question)
			if convertErr != nil {
				return cleardev.ComplexPlanningSnapshot{}, false, convertErr
			}
			snapshot.Questions = append(snapshot.Questions, converted)
		}
		answers, listErr := s.qr.ListClearDevComplexClarificationAnswers(ctx, request.ID)
		if listErr != nil {
			return cleardev.ComplexPlanningSnapshot{}, false, listErr
		}
		for _, answer := range answers {
			snapshot.Answers = append(snapshot.Answers, clearDevComplexAnswerFromGen(answer))
		}
	}
	compilations, err := s.qr.ListClearDevComplexCompilations(ctx, requirementID)
	if err != nil {
		return cleardev.ComplexPlanningSnapshot{}, false, err
	}
	for _, compilation := range compilations {
		snapshot.Compilations = append(snapshot.Compilations, clearDevComplexCompilationFromGen(compilation))
		maps, listErr := s.qr.ListClearDevComplexIDMaps(ctx, compilation.ID)
		if listErr != nil {
			return cleardev.ComplexPlanningSnapshot{}, false, listErr
		}
		for _, item := range maps {
			snapshot.IDMaps = append(snapshot.IDMaps, clearDevComplexIDMapFromGen(item))
		}
	}
	plans, err := s.qr.ListClearDevComplexEngineeringPlans(ctx, requirementID)
	if err != nil {
		return cleardev.ComplexPlanningSnapshot{}, false, err
	}
	for _, plan := range plans {
		snapshot.Plans = append(snapshot.Plans, clearDevComplexPlanFromGen(plan))
	}
	reviews, err := s.qr.ListClearDevComplexPlanReviews(ctx, requirementID)
	if err != nil {
		return cleardev.ComplexPlanningSnapshot{}, false, err
	}
	for _, review := range reviews {
		snapshot.Reviews = append(snapshot.Reviews, clearDevComplexPlanReviewFromGen(review))
	}
	validations, err := s.qr.ListClearDevComplexPlanValidations(ctx, requirementID)
	if err != nil {
		return cleardev.ComplexPlanningSnapshot{}, false, err
	}
	for _, validation := range validations {
		snapshot.Validations = append(snapshot.Validations, complexPlanValidationFromGen(validation))
	}
	answerRows, err := s.qr.ListClearDevPlannerAnswers(ctx, requirementID)
	if err != nil {
		return cleardev.ComplexPlanningSnapshot{}, false, err
	}
	snapshot.PlannerAnswers = []cleardev.PlannerClarificationAnswer{}
	for _, row := range answerRows {
		answer, err := plannerAnswerFromRow(row)
		if err != nil {
			return cleardev.ComplexPlanningSnapshot{}, false, err
		}
		for _, plan := range snapshot.Plans {
			if plan.ID == answer.PlanID {
				if question, ok := cleardev.PlannerClarificationForPlan(plan); ok {
					answer.Questions = question.Questions
				}
			}
		}
		snapshot.PlannerAnswers = append(snapshot.PlannerAnswers, answer)
	}

	return snapshot, true, nil
}

func (s *Store) ListClearDevRunnableComplexFlows(ctx context.Context) ([]string, error) {
	return s.qr.ListClearDevRunnableComplexFlows(ctx)
}

func (s *Store) CreateClearDevComplexRoleBinding(ctx context.Context, command cleardev.CreateComplexRoleBindingCommand) (cleardev.ComplexRoleBinding, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.ComplexRoleBinding
	var created bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev complex role binding", func(q *gen.Queries) error {
		requirement, _, err := loadComplexRequirement(ctx, q, command.Binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if confirmed, confirmedErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID); confirmedErr == nil {
			if stopped, stopErr := activeDirectionStop(ctx, q, confirmed.ID); stopErr != nil {
				return stopErr
			} else if stopped {
				rejected = directionStoppedError()
				return nil
			}
		} else if !errors.Is(confirmedErr, sql.ErrNoRows) {
			return confirmedErr
		}
		if requirement.CancelledAt != nil || (command.Binding.Role != cleardev.StandardRoleSteward && command.Binding.Role != cleardev.StandardRoleEngineeringPlanner) ||
			command.Binding.Status != cleardev.RoleBindingStatusRequested || strings.TrimSpace(command.Binding.ID) == "" ||
			strings.TrimSpace(command.Binding.SessionCreationIdempotencyKey) == "" {
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid complex role binding")
			return nil
		}
		bindings, err := q.ListClearDevComplexRoleBindings(ctx, requirement.ID)
		if err != nil {
			return err
		}
		for _, row := range bindings {
			existing := clearDevComplexRoleBindingFromGen(row)
			if existing.Role != command.Binding.Role && existing.ID != command.Binding.ID {
				continue
			}
			if existing.ID == command.Binding.ID && existing.Role == command.Binding.Role &&
				existing.SessionCreationIdempotencyKey == command.Binding.SessionCreationIdempotencyKey {
				result = existing
				return nil
			}
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "complex role binding already exists")
			return nil
		}
		result = command.Binding
		if err := insertClearDevComplexRoleBinding(ctx, q, result); err != nil {
			return err
		}
		created = true
		return insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexRoleBinding, result.ID, cleardev.ActionBindComplexRole, cleardev.EventAccepted, cleardev.ReasonNone, "", result.RequestedAt)
	})
	if err != nil {
		return cleardev.ComplexRoleBinding{}, false, err
	}
	if rejected != nil {
		return cleardev.ComplexRoleBinding{}, false, rejected
	}
	return result, created, nil
}

func (s *Store) BindClearDevComplexRoleBinding(ctx context.Context, roleBindingID, aoSessionID string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "bind ClearDev complex role", func(q *gen.Queries) error {
		bindingRow, err := q.GetClearDevComplexRoleBinding(ctx, roleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevComplexRoleBindingFromGen(bindingRow)
		requirement, _, err := loadComplexRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if binding.Status == cleardev.RoleBindingStatusBound && binding.AOSessionID == aoSessionID {
			return nil
		}
		if binding.Status != cleardev.RoleBindingStatusRequested || strings.TrimSpace(aoSessionID) == "" {
			rejected = complexRejected(requirement.ID, cleardev.ReasonInvalidTransition, "role binding cannot be bound")
			return nil
		}
		bindings, err := q.ListClearDevComplexRoleBindings(ctx, requirement.ID)
		if err != nil {
			return err
		}
		for _, row := range bindings {
			other := clearDevComplexRoleBindingFromGen(row)
			if other.ID != binding.ID && other.AOSessionID == aoSessionID {
				rejected = complexRejected(requirement.ID, cleardev.ReasonSessionMismatch, "AO session already belongs to another complex role")
				return nil
			}
		}
		rows, err := q.BindClearDevComplexRoleBindingCAS(ctx, gen.BindClearDevComplexRoleBindingCASParams{
			AoSessionID: nullableString(aoSessionID), BoundAt: nullableTimePtr(&at), ID: roleBindingID,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = complexRejected(requirement.ID, cleardev.ReasonInvalidTransition, "role binding compare-and-swap lost")
			return nil
		}
		changed = true
		return insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexRoleBinding, roleBindingID, cleardev.ActionBindComplexRole, cleardev.EventAccepted, cleardev.ReasonNone, aoSessionID, at)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

func (s *Store) FailClearDevComplexRoleBinding(ctx context.Context, command cleardev.FailComplexRoleBindingCommand) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "fail ClearDev complex role", func(q *gen.Queries) error {
		bindingRow, err := q.GetClearDevComplexRoleBinding(ctx, command.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevComplexRoleBindingFromGen(bindingRow)
		requirement, _, err := loadComplexRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if binding.Status == cleardev.RoleBindingStatusFailed && binding.ReasonCode == command.ReasonCode {
			return nil
		}
		if binding.Status != cleardev.RoleBindingStatusRequested || command.ReasonCode == cleardev.ReasonNone {
			rejected = complexRejected(requirement.ID, cleardev.ReasonInvalidTransition, "role binding cannot be failed")
			return nil
		}
		rows, err := q.FailClearDevComplexRoleBindingCAS(ctx, gen.FailClearDevComplexRoleBindingCASParams{
			ReasonCode: string(command.ReasonCode), EndedAt: nullableTimePtr(&command.At), ID: command.RoleBindingID,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = complexRejected(requirement.ID, cleardev.ReasonInvalidTransition, "role binding compare-and-swap lost")
			return nil
		}
		changed = true
		return insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexRoleBinding, command.RoleBindingID, cleardev.ActionBindComplexRole, cleardev.EventRejected, command.ReasonCode, "", command.At)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

func (s *Store) CreateClearDevComplexAgentStep(ctx context.Context, step cleardev.AgentStep) (cleardev.AgentStep, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.AgentStep
	var created bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev complex agent step", func(q *gen.Queries) error {
		bindingRow, err := q.GetClearDevComplexRoleBinding(ctx, step.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevComplexRoleBindingFromGen(bindingRow)
		requirement, _, err := loadComplexRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if confirmed, confirmedErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID); confirmedErr == nil {
			if stopped, stopErr := activeDirectionStop(ctx, q, confirmed.ID); stopErr != nil {
				return stopErr
			} else if stopped {
				rejected = directionStoppedError()
				return nil
			}
			// A new planning request is admitted only while the confirmed
			// version still has automatic revisions left. The count comes from
			// valid completed REPLAN reviews whose plan hash matches, so a
			// duplicate advance or restart cannot create a fifth round.
			if step.Kind == cleardev.ComplexAgentStepEngineeringPlan {
				replans, countErr := q.CountClearDevComplexValidReplans(ctx, confirmed.ID)
				if countErr != nil {
					return countErr
				}
				if replans > cleardev.MaxComplexAutomaticReplans {
					rejected = complexRejected(requirement.ID, cleardev.ReasonComplexReplanLimitReached, "complex planning replan limit reached")
					return nil
				}
			}
		} else if !errors.Is(confirmedErr, sql.ErrNoRows) {
			return confirmedErr
		}
		if existingRow, getErr := q.GetClearDevComplexAgentStep(ctx, step.ID); getErr == nil {
			existing := clearDevComplexAgentStepFromGen(existingRow)
			if sameComplexAgentStepRequest(existing, step) {
				result = existing
				return nil
			}
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "agent step ID already belongs to another request")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		if binding.Status != cleardev.RoleBindingStatusBound || step.SendStatus != cleardev.AgentStepSendStatusPending ||
			!validComplexAgentStepKind(step.Kind) || strings.TrimSpace(step.ID) == "" ||
			strings.TrimSpace(step.RequestID) == "" || strings.TrimSpace(step.ClientMessageID) == "" ||
			!validSHA256Digest(step.PromptSHA256) || step.TurnID != "" || step.FinalMessageID != "" ||
			step.FinalMessageText != "" || step.MessageSHA256 != "" || step.SentAt != nil ||
			step.CompletedAt != nil || step.FailedAt != nil || step.ReasonCode != cleardev.ReasonNone {
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid pending agent step")
			return nil
		}
		result = step
		if err := insertClearDevComplexAgentStep(ctx, q, result); err != nil {
			return err
		}
		created = true
		return insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexAgentStep, step.ID, cleardev.ActionCreateComplexAgentStep, cleardev.EventAccepted, cleardev.ReasonNone, binding.AOSessionID, step.RequestedAt)
	})
	if err != nil {
		return cleardev.AgentStep{}, false, err
	}
	if rejected != nil {
		return cleardev.AgentStep{}, false, rejected
	}
	return result, created, nil
}

func (s *Store) MarkClearDevComplexAgentStepSent(ctx context.Context, stepID string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "mark ClearDev complex agent step sent", func(q *gen.Queries) error {
		row, err := q.GetClearDevComplexAgentStep(ctx, stepID)
		if err != nil {
			return err
		}
		step := clearDevComplexAgentStepFromGen(row)
		bindingRow, err := q.GetClearDevComplexRoleBinding(ctx, step.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevComplexRoleBindingFromGen(bindingRow)
		requirement, _, err := loadComplexRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if confirmed, confirmedErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID); confirmedErr == nil {
			if stopped, stopErr := activeDirectionStop(ctx, q, confirmed.ID); stopErr != nil {
				return stopErr
			} else if stopped {
				rejected = directionStoppedError()
				return nil
			}
		} else if !errors.Is(confirmedErr, sql.ErrNoRows) {
			return confirmedErr
		}
		if step.SendStatus == cleardev.AgentStepSendStatusSent {
			return nil
		}
		if step.SendStatus != cleardev.AgentStepSendStatusPending {
			rejected = complexRejected(requirement.ID, cleardev.ReasonInvalidTransition, "agent step is no longer pending")
			return nil
		}
		rows, err := q.MarkClearDevComplexAgentStepSentCAS(ctx, gen.MarkClearDevComplexAgentStepSentCASParams{SentAt: nullableTimePtr(&at), ID: stepID})
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = complexRejected(requirement.ID, cleardev.ReasonInvalidTransition, "agent-step compare-and-swap lost")
			return nil
		}
		changed = true
		return insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexAgentStep, stepID, cleardev.ActionSendComplexAgentStep, cleardev.EventAccepted, cleardev.ReasonNone, binding.AOSessionID, at)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

func (s *Store) SettleClearDevComplexAgentStep(ctx context.Context, step cleardev.AgentStep) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "settle ClearDev complex agent step", func(q *gen.Queries) error {
		row, err := q.GetClearDevComplexAgentStep(ctx, step.ID)
		if err != nil {
			return err
		}
		existing := clearDevComplexAgentStepFromGen(row)
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
				rejected = complexRejected(requirement.ID, cleardev.ReasonInvalidTransition, "agent step is not awaiting a sent result")
				return nil
			}
			if strings.TrimSpace(step.TurnID) == "" || strings.TrimSpace(step.FinalMessageID) == "" || strings.TrimSpace(step.FinalMessageText) == "" ||
				!requirementDigestMatches(step.FinalMessageText, step.MessageSHA256) || step.CompletedAt == nil || step.FailedAt != nil || step.ReasonCode != cleardev.ReasonNone {
				rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid settled agent step")
				return nil
			}
			rows, err = q.SettleClearDevComplexAgentStepCAS(ctx, gen.SettleClearDevComplexAgentStepCASParams{
				TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID),
				FinalMessageText: nullableString(step.FinalMessageText), MessageSha256: nullableString(step.MessageSHA256),
				CompletedAt: nullableTimePtr(step.CompletedAt), ID: step.ID,
			})
		case cleardev.AgentStepSendStatusFailed:
			if existing.SendStatus != cleardev.AgentStepSendStatusPending && existing.SendStatus != cleardev.AgentStepSendStatusSent {
				rejected = complexRejected(requirement.ID, cleardev.ReasonInvalidTransition, "agent step cannot fail from its current state")
				return nil
			}
			if step.FailedAt == nil || step.ReasonCode == cleardev.ReasonNone || step.TurnID != "" || step.FinalMessageID != "" ||
				step.FinalMessageText != "" || step.MessageSHA256 != "" || step.CompletedAt != nil {
				rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid failed agent step")
				return nil
			}
			rows, err = q.FailClearDevComplexAgentStepCAS(ctx, gen.FailClearDevComplexAgentStepCASParams{
				ReasonCode: string(step.ReasonCode), FailedAt: nullableTimePtr(step.FailedAt), ID: step.ID,
			})
		default:
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "agent step must settle or fail")
			return nil
		}
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = complexRejected(requirement.ID, cleardev.ReasonInvalidTransition, "agent-step compare-and-swap lost")
			return nil
		}
		changed = true
		outcome := cleardev.EventAccepted
		if step.SendStatus == cleardev.AgentStepSendStatusFailed {
			outcome = cleardev.EventRejected
		}
		return insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexAgentStep, step.ID, cleardev.ActionSettleComplexAgentStep, outcome, step.ReasonCode, binding.AOSessionID, terminalComplexAgentStepTime(step))
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

func (s *Store) RecordClearDevComplexClarification(ctx context.Context, command cleardev.RecordComplexClarificationCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "record ClearDev complex clarification", func(q *gen.Queries) error {
		requirement, _, err := loadComplexRequirement(ctx, q, command.Request.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if existing, getErr := q.GetClearDevComplexCompilationRequest(ctx, command.Request.ID); getErr == nil {
			if existing.AgentStepID == command.Request.AgentStepID && int(existing.ClarificationRound) == command.Request.ClarificationRound {
				return nil
			}
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "compilation request already exists")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		if err := insertClearDevComplexCompilationRequest(ctx, q, command.Request); err != nil {
			return err
		}
		for _, question := range command.Questions {
			if err := insertClearDevComplexClarificationQuestion(ctx, q, question); err != nil {
				return err
			}
		}
		return insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexClarification, command.Request.ID, cleardev.ActionRecordComplexClarification, cleardev.EventAccepted, cleardev.ReasonNone, "", command.Request.CreatedAt)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

func (s *Store) SubmitClearDevComplexClarificationAnswers(ctx context.Context, command cleardev.SubmitComplexClarificationCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "submit ClearDev complex clarification answers", func(q *gen.Queries) error {
		requirement, _, err := loadComplexRequirement(ctx, q, command.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		requestRow, err := q.GetClearDevComplexCompilationRequest(ctx, command.CompilationRequestID)
		if errors.Is(err, sql.ErrNoRows) {
			rejected = complexRejected(command.DevelopmentRequirementID, cleardev.ReasonPreconditionNotMet, "compilation request was not found")
			return nil
		}
		if err != nil {
			return err
		}
		if requestRow.DevelopmentProjectID != command.DevelopmentRequirementID || int(requestRow.ClarificationRound) != command.ClarificationRound {
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "answers do not bind the exact compilation request and round")
			return nil
		}
		questions, err := q.ListClearDevComplexClarificationQuestions(ctx, command.CompilationRequestID)
		if err != nil {
			return err
		}
		if len(questions) == 0 {
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "compilation request has no unanswered questions")
			return nil
		}
		existingAnswers, err := q.ListClearDevComplexClarificationAnswers(ctx, command.CompilationRequestID)
		if err != nil {
			return err
		}
		if len(existingAnswers) == len(questions) {
			return nil
		}
		if len(existingAnswers) != 0 {
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "clarification answers are already partially saved")
			return nil
		}
		wanted := map[string]struct{}{}
		for _, question := range questions {
			wanted[question.QuestionKey] = struct{}{}
		}
		seen := map[string]struct{}{}
		if len(command.Answers) != len(questions) {
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "answers must cover every question key exactly once")
			return nil
		}
		for _, answer := range command.Answers {
			key := strings.TrimSpace(answer.QuestionKey)
			text := strings.TrimSpace(answer.Text)
			if _, ok := wanted[key]; !ok {
				rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "answer names an unknown question key")
				return nil
			}
			if _, dup := seen[key]; dup {
				rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "answer question keys must be unique")
				return nil
			}
			if text == "" || utf8.RuneCountInString(text) > cleardev.MaxComplexAnswerRunes {
				rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "answer text is empty or too long")
				return nil
			}
			seen[key] = struct{}{}
			if err := q.InsertClearDevComplexClarificationAnswer(ctx, gen.InsertClearDevComplexClarificationAnswerParams{
				CompilationRequestID: command.CompilationRequestID, QuestionKey: key, Text: text, CreatedAt: command.At,
			}); err != nil {
				return err
			}
		}
		if len(seen) != len(wanted) {
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "answers must cover every question key exactly once")
			return nil
		}
		return insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexClarification, command.CompilationRequestID, cleardev.ActionRecordComplexAnswers, cleardev.EventAccepted, cleardev.ReasonNone, "", command.At)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

func (s *Store) SettleClearDevComplexCompilation(ctx context.Context, command cleardev.SettleComplexCompilationCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "settle ClearDev complex compilation", func(q *gen.Queries) error {
		requirement, marker, err := loadComplexRequirement(ctx, q, command.Compilation.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		compilations, err := q.ListClearDevComplexCompilations(ctx, requirement.ID)
		if err != nil {
			return err
		}
		for _, existing := range compilations {
			if existing.CompilationRequestID == command.Compilation.CompilationRequestID {
				return nil
			}
		}
		if command.Compilation.Outcome == "READY" {
			versions, listErr := q.ListClearDevRequirementVersions(ctx, requirement.ID)
			if listErr != nil {
				return listErr
			}
			if len(versions) != 0 {
				rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "complex compilation cannot create v1 after a version already exists")
				return nil
			}
			version := command.Version
			if version.ID != marker.TargetRequirementVersionID || version.DevelopmentRequirementID != requirement.ID ||
				version.Version != 1 || version.Status != cleardev.RequirementVersionStatusDraft || version.TaskSetVersion != 0 ||
				!requirementDigestMatches(version.RequirementText, version.SHA256) ||
				strings.TrimSpace(command.Compilation.NormalizedRequirementJSON) == "" ||
				!requirementDigestMatches(command.Compilation.NormalizedRequirementJSON, command.Compilation.CompilationSHA256) {
				rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid compiled requirement version")
				return nil
			}
		} else if command.Compilation.Outcome != "NEEDS_HUMAN" || command.Compilation.NormalizedRequirementJSON != "" || command.Compilation.CompilationSHA256 != "" {
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid complex compilation outcome")
			return nil
		}
		if existing, getErr := q.GetClearDevComplexCompilationRequest(ctx, command.Compilation.CompilationRequestID); getErr == nil {
			if existing.AgentStepID != command.Compilation.AgentStepID {
				rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "compilation request already belongs to another step")
				return nil
			}
		} else if errors.Is(getErr, sql.ErrNoRows) {
			request := command.Request
			if request.ID == "" {
				request.ID = command.Compilation.CompilationRequestID
			}
			if request.DevelopmentRequirementID == "" {
				request.DevelopmentRequirementID = requirement.ID
			}
			if request.AgentStepID == "" {
				request.AgentStepID = command.Compilation.AgentStepID
			}
			if request.CreatedAt.IsZero() {
				request.CreatedAt = command.Compilation.CreatedAt
			}
			if err := insertClearDevComplexCompilationRequest(ctx, q, request); err != nil {
				return err
			}
		} else {
			return getErr
		}
		if err := q.InsertClearDevComplexCompilation(ctx, gen.InsertClearDevComplexCompilationParams{
			ID: command.Compilation.ID, DevelopmentProjectID: requirement.ID,
			CompilationRequestID: command.Compilation.CompilationRequestID, AgentStepID: command.Compilation.AgentStepID,
			Outcome: command.Compilation.Outcome, Summary: command.Compilation.Summary,
			NormalizedRequirementJson: command.Compilation.NormalizedRequirementJSON,
			CompilationSha256:         command.Compilation.CompilationSHA256,
			TurnID:                    command.Compilation.TurnID, FinalMessageID: command.Compilation.FinalMessageID,
			RawMessageText: command.Compilation.RawMessageText, RawMessageSha256: command.Compilation.RawMessageSHA256,
			CreatedAt: command.Compilation.CreatedAt,
		}); err != nil {
			return err
		}
		if err := insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexCompilation, command.Compilation.ID, cleardev.ActionRecordComplexCompilation, cleardev.EventAccepted, cleardev.ReasonNone, "", command.Compilation.CreatedAt); err != nil {
			return err
		}
		if command.Compilation.Outcome != "READY" {
			return nil
		}
		for _, item := range command.IDMaps {
			if err := q.InsertClearDevComplexIDMap(ctx, gen.InsertClearDevComplexIDMapParams{
				CompilationID: command.Compilation.ID, Kind: item.Kind, TemporaryKey: item.TemporaryKey,
				StableID: item.StableID, Ordinal: int64(item.Ordinal),
			}); err != nil {
				return err
			}
		}
		version := command.Version
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

func (s *Store) CreateClearDevComplexEngineeringPlan(ctx context.Context, command cleardev.CreateComplexPlanCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev complex engineering plan", func(q *gen.Queries) error {
		requirement, _, err := loadComplexRequirement(ctx, q, command.Plan.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, command.Plan.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		plans, err := q.ListClearDevComplexEngineeringPlans(ctx, requirement.ID)
		if err != nil {
			return err
		}
		for _, existing := range plans {
			if existing.ID == command.Plan.ID || existing.PlanningRequestID == command.Plan.PlanningRequestID {
				return verifyPlannerContractReplay(ctx, q, existing, command)
			}
		}
		if err := validatePlannerContractCommand(ctx, q, command, true); err != nil {
			return err
		}
		next := int64(len(plans) + 1)
		if command.Plan.Version != next || strings.TrimSpace(command.Plan.PlanJSON) == "" ||
			!requirementDigestMatches(command.Plan.PlanJSON, command.Plan.PlanSHA256) {
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid complex engineering plan")
			return nil
		}
		if err := q.InsertClearDevComplexEngineeringPlan(ctx, gen.InsertClearDevComplexEngineeringPlanParams{
			ID: command.Plan.ID, PlanningRequestID: command.Plan.PlanningRequestID,
			DevelopmentProjectID: requirement.ID, RequirementVersionID: command.Plan.RequirementVersionID,
			RequirementSha256: command.Plan.RequirementSHA256, CompilationSha256: command.Plan.CompilationSHA256,
			Version: command.Plan.Version, PlannerRoleBindingID: command.Plan.PlannerRoleBindingID,
			AgentStepID: command.Plan.AgentStepID, TurnID: command.Plan.TurnID, FinalMessageID: command.Plan.FinalMessageID,
			PlanJson: command.Plan.PlanJSON, PlanSha256: command.Plan.PlanSHA256, CreatedAt: command.Plan.CreatedAt,
		}); err != nil {
			return err
		}
		if err := insertPlannerContractValidation(ctx, q, command.Validation); err != nil {
			return err
		}
		return insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexEngineeringPlan, command.Plan.ID, cleardev.ActionCreateComplexEngineeringPlan, cleardev.EventAccepted, cleardev.ReasonNone, "", command.Plan.CreatedAt)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

func (s *Store) RecordClearDevComplexPlanReview(ctx context.Context, command cleardev.RecordComplexPlanReviewCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "record ClearDev complex plan review", func(q *gen.Queries) error {
		review := command.Review
		stewardRow, err := q.GetClearDevComplexRoleBinding(ctx, review.StewardRoleBindingID)
		if err != nil {
			return err
		}
		steward := clearDevComplexRoleBindingFromGen(stewardRow)
		requirement, _, err := loadComplexRequirement(ctx, q, steward.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		plans, err := q.ListClearDevComplexEngineeringPlans(ctx, requirement.ID)
		if err != nil {
			return err
		}
		matched := false
		var matchedPlan gen.CleardevComplexEngineeringPlan
		for _, plan := range plans {
			if plan.ID == review.PlanID && plan.PlanSha256 == review.PlanSHA256 {
				matched = true
				matchedPlan = plan
				break
			}
		}
		if !matched {
			rejected = complexRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "plan review must bind the exact immutable plan")
			return nil
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, matchedPlan.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		existingReviews, err := q.ListClearDevComplexPlanReviews(ctx, requirement.ID)
		if err != nil {
			return err
		}
		for _, existing := range existingReviews {
			if existing.PlanID == review.PlanID {
				return nil
			}
		}
		findings := review.FindingsJSON
		if strings.TrimSpace(findings) == "" {
			findings = "[]"
		}
		if err := q.InsertClearDevComplexPlanReview(ctx, gen.InsertClearDevComplexPlanReviewParams{
			ID: review.ID, PlanID: review.PlanID, StewardRoleBindingID: review.StewardRoleBindingID,
			AgentStepID: review.AgentStepID, ReviewRequestID: review.ReviewRequestID,
			Verdict: string(review.Verdict), ReasonCode: string(review.ReasonCode), Summary: review.Summary,
			FindingsJson: findings, PlanSha256: review.PlanSHA256, TurnID: review.TurnID,
			FinalMessageID: review.FinalMessageID, CreatedAt: review.CreatedAt,
		}); err != nil {
			return err
		}
		return insertComplexFactEvent(ctx, q, requirement, cleardev.SubjectComplexPlanReview, review.ID, cleardev.ActionRecordComplexPlanReview, cleardev.EventAccepted, review.ReasonCode, steward.AOSessionID, review.CreatedAt)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

func loadComplexRequirement(ctx context.Context, q *gen.Queries, requirementID string) (cleardev.DevelopmentRequirement, cleardev.ComplexRequirement, error) {
	row, err := q.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return cleardev.DevelopmentRequirement{}, cleardev.ComplexRequirement{}, err
	}
	marker, err := q.GetClearDevComplexRequirement(ctx, requirementID)
	if err != nil {
		return cleardev.DevelopmentRequirement{}, cleardev.ComplexRequirement{}, err
	}
	return clearDevRequirementFromGen(row), clearDevComplexRequirementFromGen(marker), nil
}

func insertClearDevComplexRoleBinding(ctx context.Context, q *gen.Queries, binding cleardev.ComplexRoleBinding) error {
	return q.InsertClearDevComplexRoleBinding(ctx, gen.InsertClearDevComplexRoleBindingParams{
		ID: binding.ID, DevelopmentProjectID: binding.DevelopmentRequirementID, Role: string(binding.Role),
		SessionCreationIdempotencyKey: binding.SessionCreationIdempotencyKey, AoSessionID: nullableString(binding.AOSessionID),
		Status: string(binding.Status), ReasonCode: string(binding.ReasonCode), RequestedAt: binding.RequestedAt,
		BoundAt: nullableTimePtr(binding.BoundAt), EndedAt: nullableTimePtr(binding.EndedAt),
	})
}

func insertClearDevComplexAgentStep(ctx context.Context, q *gen.Queries, step cleardev.AgentStep) error {
	return q.InsertClearDevComplexAgentStep(ctx, gen.InsertClearDevComplexAgentStepParams{
		ID: step.ID, RoleBindingID: step.RoleBindingID, StepKind: string(step.Kind), RequestID: step.RequestID,
		ClientMessageID: step.ClientMessageID, PromptSha256: step.PromptSHA256, SendStatus: string(step.SendStatus),
		TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID),
		FinalMessageText: nullableString(step.FinalMessageText), MessageSha256: nullableString(step.MessageSHA256),
		RequestedAt: step.RequestedAt, SentAt: nullableTimePtr(step.SentAt), CompletedAt: nullableTimePtr(step.CompletedAt),
		FailedAt: nullableTimePtr(step.FailedAt), ReasonCode: string(step.ReasonCode),
	})
}

func insertClearDevComplexCompilationRequest(ctx context.Context, q *gen.Queries, request cleardev.ComplexCompilationRequest) error {
	return q.InsertClearDevComplexCompilationRequest(ctx, gen.InsertClearDevComplexCompilationRequestParams{
		ID: request.ID, DevelopmentProjectID: request.DevelopmentRequirementID, AgentStepID: request.AgentStepID,
		ClarificationRound: int64(request.ClarificationRound), CompilationContextSha256: request.CompilationContextSHA256,
		AdditionalRoundReason: request.AdditionalRoundReason, CreatedAt: request.CreatedAt,
	})
}

func insertClearDevComplexClarificationQuestion(ctx context.Context, q *gen.Queries, question cleardev.ComplexClarificationQuestion) error {
	keys, err := json.Marshal(nonNilStrings(question.RequirementKeys))
	if err != nil {
		return err
	}
	return q.InsertClearDevComplexClarificationQuestion(ctx, gen.InsertClearDevComplexClarificationQuestionParams{
		CompilationRequestID: question.CompilationRequestID, QuestionKey: question.QuestionKey,
		Text: question.Text, Reason: question.Reason, RequirementKeysJson: string(keys), Ordinal: int64(question.Ordinal),
	})
}

func insertComplexFactEvent(ctx context.Context, q *gen.Queries, requirement cleardev.DevelopmentRequirement, subject cleardev.SubjectType, subjectID string, action cleardev.Action, outcome cleardev.EventOutcome, reason cleardev.ReasonCode, sourceSessionID string, at time.Time) error {
	return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: subject, SubjectID: subjectID, Action: action, Outcome: outcome,
		Reason: reason, Source: cleardev.EventSourceControlPlane,
		SourceAOSessionID: sourceSessionID, CreatedAt: at,
	})
}

func complexRejected(requirementID string, reason cleardev.ReasonCode, message string) *cleardev.RuleError {
	return &cleardev.RuleError{Code: reason, Message: fmt.Sprintf("ClearDev complex flow %s: %s", requirementID, message)}
}

func validComplexAgentStepKind(kind cleardev.AgentStepKind) bool {
	return kind == cleardev.ComplexAgentStepCompilation || kind == cleardev.ComplexAgentStepEngineeringPlan || kind == cleardev.ComplexAgentStepPlanReview
}

func sameComplexAgentStepRequest(existing, step cleardev.AgentStep) bool {
	return existing.RoleBindingID == step.RoleBindingID && existing.Kind == step.Kind && existing.RequestID == step.RequestID &&
		existing.ClientMessageID == step.ClientMessageID && existing.PromptSHA256 == step.PromptSHA256
}

func sameComplexAgentStepTerminal(existing, step cleardev.AgentStep) bool {
	if existing.SendStatus == cleardev.AgentStepSendStatusSettled {
		return existing.TurnID == step.TurnID && existing.FinalMessageID == step.FinalMessageID &&
			existing.FinalMessageText == step.FinalMessageText && existing.MessageSHA256 == step.MessageSHA256
	}
	return existing.ReasonCode == step.ReasonCode
}

func terminalComplexAgentStepTime(step cleardev.AgentStep) time.Time {
	if step.CompletedAt != nil {
		return *step.CompletedAt
	}
	if step.FailedAt != nil {
		return *step.FailedAt
	}
	return step.RequestedAt
}

func clearDevComplexRequirementFromGen(row gen.CleardevComplexRequirement) cleardev.ComplexRequirement {
	return cleardev.ComplexRequirement{
		DevelopmentRequirementID: row.DevelopmentProjectID, OriginalPRDText: row.OriginalPrdText,
		OriginalPRDSHA256: row.OriginalPrdSha256, TargetRequirementVersionID: row.TargetRequirementVersionID,
		CreatedAt: row.CreatedAt,
	}
}

func clearDevComplexRoleBindingFromGen(row gen.CleardevComplexRoleBinding) cleardev.ComplexRoleBinding {
	binding := cleardev.ComplexRoleBinding{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, Role: cleardev.StandardRole(row.Role),
		SessionCreationIdempotencyKey: row.SessionCreationIdempotencyKey, AOSessionID: row.AoSessionID.String,
		Status: cleardev.RoleBindingStatus(row.Status), ReasonCode: cleardev.ReasonCode(row.ReasonCode), RequestedAt: row.RequestedAt,
	}
	if row.BoundAt.Valid {
		value := row.BoundAt.Time
		binding.BoundAt = &value
	}
	if row.EndedAt.Valid {
		value := row.EndedAt.Time
		binding.EndedAt = &value
	}
	return binding
}

func clearDevComplexAgentStepFromGen(row gen.CleardevComplexAgentStep) cleardev.AgentStep {
	step := cleardev.AgentStep{
		ID: row.ID, RoleBindingID: row.RoleBindingID, Kind: cleardev.AgentStepKind(row.StepKind), RequestID: row.RequestID,
		ClientMessageID: row.ClientMessageID, PromptSHA256: row.PromptSha256, SendStatus: cleardev.AgentStepSendStatus(row.SendStatus),
		TurnID: row.TurnID.String, FinalMessageID: row.FinalMessageID.String, FinalMessageText: row.FinalMessageText.String,
		MessageSHA256: row.MessageSha256.String, RequestedAt: row.RequestedAt, ReasonCode: cleardev.ReasonCode(row.ReasonCode),
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

func clearDevComplexCompilationRequestFromGen(row gen.CleardevComplexCompilationRequest) cleardev.ComplexCompilationRequest {
	return cleardev.ComplexCompilationRequest{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, AgentStepID: row.AgentStepID,
		ClarificationRound: int(row.ClarificationRound), CompilationContextSHA256: row.CompilationContextSha256,
		AdditionalRoundReason: row.AdditionalRoundReason, CreatedAt: row.CreatedAt,
	}
}

func clearDevComplexQuestionFromGen(row gen.CleardevComplexClarificationQuestion) (cleardev.ComplexClarificationQuestion, error) {
	var keys []string
	if err := json.Unmarshal([]byte(row.RequirementKeysJson), &keys); err != nil {
		return cleardev.ComplexClarificationQuestion{}, err
	}
	return cleardev.ComplexClarificationQuestion{
		CompilationRequestID: row.CompilationRequestID, QuestionKey: row.QuestionKey, Text: row.Text,
		Reason: row.Reason, RequirementKeys: nonNilStrings(keys), Ordinal: int(row.Ordinal),
	}, nil
}

func clearDevComplexAnswerFromGen(row gen.CleardevComplexClarificationAnswer) cleardev.ComplexClarificationAnswer {
	return cleardev.ComplexClarificationAnswer{
		CompilationRequestID: row.CompilationRequestID, QuestionKey: row.QuestionKey, Text: row.Text, CreatedAt: row.CreatedAt,
	}
}

func clearDevComplexCompilationFromGen(row gen.CleardevComplexCompilation) cleardev.ComplexCompilation {
	return cleardev.ComplexCompilation{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, CompilationRequestID: row.CompilationRequestID,
		AgentStepID: row.AgentStepID, Outcome: row.Outcome, Summary: row.Summary,
		NormalizedRequirementJSON: row.NormalizedRequirementJson, CompilationSHA256: row.CompilationSha256,
		TurnID: row.TurnID, FinalMessageID: row.FinalMessageID, RawMessageText: row.RawMessageText,
		RawMessageSHA256: row.RawMessageSha256, CreatedAt: row.CreatedAt,
	}
}

func clearDevComplexIDMapFromGen(row gen.CleardevComplexIDMap) cleardev.ComplexIDMap {
	return cleardev.ComplexIDMap{
		CompilationID: row.CompilationID, Kind: row.Kind, TemporaryKey: row.TemporaryKey,
		StableID: row.StableID, Ordinal: int(row.Ordinal),
	}
}

func clearDevComplexPlanFromGen(row gen.CleardevComplexEngineeringPlan) cleardev.ComplexEngineeringPlan {
	return cleardev.ComplexEngineeringPlan{
		ID: row.ID, PlanningRequestID: row.PlanningRequestID, DevelopmentRequirementID: row.DevelopmentProjectID,
		RequirementVersionID: row.RequirementVersionID, RequirementSHA256: row.RequirementSha256,
		CompilationSHA256: row.CompilationSha256, Version: row.Version, PlannerRoleBindingID: row.PlannerRoleBindingID,
		AgentStepID: row.AgentStepID, TurnID: row.TurnID, FinalMessageID: row.FinalMessageID,
		PlanJSON: row.PlanJson, PlanSHA256: row.PlanSha256, CreatedAt: row.CreatedAt,
	}
}

func clearDevComplexPlanReviewFromGen(row gen.CleardevComplexPlanReview) cleardev.ComplexPlanReview {
	return cleardev.ComplexPlanReview{
		ID: row.ID, PlanID: row.PlanID, StewardRoleBindingID: row.StewardRoleBindingID, AgentStepID: row.AgentStepID,
		ReviewRequestID: row.ReviewRequestID, Verdict: cleardev.PlanReviewVerdict(row.Verdict),
		ReasonCode: cleardev.ReasonCode(row.ReasonCode), Summary: row.Summary, FindingsJSON: row.FindingsJson,
		PlanSHA256: row.PlanSha256, TurnID: row.TurnID, FinalMessageID: row.FinalMessageID, CreatedAt: row.CreatedAt,
	}
}
