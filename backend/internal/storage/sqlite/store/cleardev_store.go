package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// CreateClearDevRequirement atomically creates the implementation container
// and its first DRAFT requirement version. It intentionally creates no tasks.
func (s *Store) CreateClearDevRequirement(ctx context.Context, initial cleardev.InitialRequirement) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "create ClearDev requirement", func(q *gen.Queries) error {
		requirement := initial.Requirement
		version := initial.Version
		if strings.TrimSpace(requirement.ID) == "" || strings.TrimSpace(requirement.Name) == "" || requirement.CancelledAt != nil ||
			requirement.CancelReason != cleardev.ReasonNone || requirement.CancelReasonText != "" {
			return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "invalid initial ClearDev requirement"}
		}
		aoProject, err := q.GetProject(ctx, domain.ProjectID(requirement.AOProjectID))
		if err != nil {
			return fmt.Errorf("get AO project %s: %w", requirement.AOProjectID, err)
		}
		if domain.ProjectKind(aoProject.Kind).WithDefault() != domain.ProjectKindSingleRepo {
			return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "ClearDev S01 requires a single_repo AO project"}
		}
		if initial.BenchmarkBinding != nil {
			binding := *initial.BenchmarkBinding
			if binding.DevelopmentRequirementID != requirement.ID || binding.AOProjectID != requirement.AOProjectID ||
				filepath.Clean(binding.ProjectRoot) != filepath.Clean(aoProject.Path) || binding.ManifestSHA256 == "" ||
				binding.CheckProfileSHA256 != cleardev.BenchmarkCheckProfileSHA256(binding.CheckPrefix) || binding.CreatedAt.IsZero() {
				return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "invalid ClearDev benchmark binding"}
			}
			if _, existingErr := q.GetClearDevBenchmarkBindingByManifest(ctx, binding.ManifestSHA256); existingErr == nil {
				return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "benchmark manifest is already bound"}
			} else if !errors.Is(existingErr, sql.ErrNoRows) {
				return existingErr
			}
		}
		if version.DevelopmentRequirementID != requirement.ID || version.Version != 1 ||
			version.Status != cleardev.RequirementVersionStatusDraft || version.TaskSetVersion != 0 ||
			version.SupersededByID != "" || version.ConfirmedAt != nil ||
			strings.TrimSpace(version.RequirementText) == "" || !requirementDigestMatches(version.RequirementText, version.SHA256) {
			return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: "invalid initial ClearDev requirement version"}
		}
		if err := q.InsertClearDevRequirement(ctx, gen.InsertClearDevRequirementParams{
			ID: requirement.ID, AoProjectID: requirement.AOProjectID, Name: requirement.Name,
			CreatedAt: requirement.CreatedAt, UpdatedAt: requirement.UpdatedAt,
		}); err != nil {
			return fmt.Errorf("insert ClearDev requirement: %w", err)
		}
		if binding := initial.BenchmarkBinding; binding != nil {
			if err := q.InsertClearDevBenchmarkBinding(ctx, gen.InsertClearDevBenchmarkBindingParams{
				DevelopmentRequirementID: binding.DevelopmentRequirementID, AoProjectID: binding.AOProjectID,
				ProjectRoot: binding.ProjectRoot, ManifestPath: binding.ManifestPath, ManifestSha256: binding.ManifestSHA256,
				FacilityVersion: binding.FacilityVersion, Purpose: binding.Purpose, PlanUnitID: binding.PlanUnitID, Scene: int64(binding.Scene),
				GroupID: binding.Group, ModePolicy: string(binding.Policy), CheckProfile: binding.CheckProfile, CheckPrefix: binding.CheckPrefix,
				CheckProfileSha256: binding.CheckProfileSHA256, PublicInputSha256: binding.PublicInputSHA256, MaterialSha256: binding.MaterialSHA256,
				CreatedAt: binding.CreatedAt,
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
		if err := insertClearDevRequirementVersion(ctx, q, version); err != nil {
			return err
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectRequirementVersion, SubjectID: version.ID,
			Action: cleardev.ActionCreateRequirementVersion, TargetState: string(version.Status),
			Outcome: cleardev.EventAccepted, Source: cleardev.EventSourceControlPlane,
			CreatedAt: version.CreatedAt,
		})
	})
}

// GetClearDevBenchmarkBinding returns the immutable S12B binding for one
// requirement. Ordinary requirements intentionally return found=false.
func (s *Store) GetClearDevBenchmarkBinding(ctx context.Context, id string) (cleardev.BenchmarkBinding, bool, error) {
	row, err := s.qr.GetClearDevBenchmarkBinding(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.BenchmarkBinding{}, false, nil
	}
	if err != nil {
		return cleardev.BenchmarkBinding{}, false, fmt.Errorf("get ClearDev benchmark binding %s: %w", id, err)
	}
	return cleardev.BenchmarkBinding{
		DevelopmentRequirementID: row.DevelopmentRequirementID, AOProjectID: row.AoProjectID,
		ProjectRoot: row.ProjectRoot, ManifestPath: row.ManifestPath, ManifestSHA256: row.ManifestSha256,
		FacilityVersion: row.FacilityVersion, Purpose: row.Purpose, PlanUnitID: row.PlanUnitID, Scene: int(row.Scene),
		Group: row.GroupID, Policy: cleardev.BenchmarkModePolicy(row.ModePolicy), CheckProfile: row.CheckProfile, CheckPrefix: row.CheckPrefix,
		CheckProfileSHA256: row.CheckProfileSha256, PublicInputSHA256: row.PublicInputSha256, MaterialSHA256: row.MaterialSha256,
		CreatedAt: row.CreatedAt,
	}, true, nil
}

// GetClearDevRequirement loads the complete durable fact history. Derived
// progress is calculated by the domain read model, not persisted here.
func (s *Store) GetClearDevRequirement(ctx context.Context, id string) (cleardev.RequirementSnapshot, bool, error) {
	requirementRow, err := s.qr.GetClearDevRequirement(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.RequirementSnapshot{}, false, nil
	}
	if err != nil {
		return cleardev.RequirementSnapshot{}, false, fmt.Errorf("get ClearDev requirement %s: %w", id, err)
	}
	snapshot := cleardev.RequirementSnapshot{Requirement: clearDevRequirementFromGen(requirementRow)}
	versions, err := s.qr.ListClearDevRequirementVersions(ctx, id)
	if err != nil {
		return cleardev.RequirementSnapshot{}, false, fmt.Errorf("list requirement versions: %w", err)
	}
	for _, row := range versions {
		snapshot.RequirementVersions = append(snapshot.RequirementVersions, clearDevRequirementVersionFromGen(row))
	}
	tasks, err := s.qr.ListClearDevDevelopmentTasks(ctx, id)
	if err != nil {
		return cleardev.RequirementSnapshot{}, false, fmt.Errorf("list development tasks: %w", err)
	}
	for _, row := range tasks {
		task := clearDevDevelopmentTaskFromListGen(row)
		snapshot.DevelopmentTasks = append(snapshot.DevelopmentTasks, task)
		permissions, listErr := s.qr.ListClearDevPermissionVersions(ctx, task.ID)
		if listErr != nil {
			return cleardev.RequirementSnapshot{}, false, fmt.Errorf("list permissions for %s: %w", task.ID, listErr)
		}
		for _, permission := range permissions {
			converted, convertErr := clearDevPermissionFromListGen(permission)
			if convertErr != nil {
				return cleardev.RequirementSnapshot{}, false, convertErr
			}
			snapshot.PermissionVersions = append(snapshot.PermissionVersions, converted)
		}
		checks, listErr := s.qr.ListClearDevRequiredChecks(ctx, task.ID)
		if listErr != nil {
			return cleardev.RequirementSnapshot{}, false, fmt.Errorf("list checks for %s: %w", task.ID, listErr)
		}
		for _, check := range checks {
			snapshot.RequiredChecks = append(snapshot.RequiredChecks, clearDevCheckFromListGen(check))
		}
		candidates, listErr := s.qr.ListClearDevCandidateCommits(ctx, task.ID)
		if listErr != nil {
			return cleardev.RequirementSnapshot{}, false, fmt.Errorf("list candidates for %s: %w", task.ID, listErr)
		}
		for _, candidate := range candidates {
			snapshot.Candidates = append(snapshot.Candidates, clearDevCandidateFromListGen(candidate))
		}
	}
	integrationCandidates, err := s.qr.ListClearDevIntegrationCandidates(ctx, id)
	if err != nil {
		return cleardev.RequirementSnapshot{}, false, fmt.Errorf("list integration candidates: %w", err)
	}
	for _, candidate := range integrationCandidates {
		snapshot.IntegrationCandidates = append(snapshot.IntegrationCandidates, clearDevIntegrationCandidateFromGen(candidate))
	}
	evidence, err := s.qr.ListClearDevEvidence(ctx, id)
	if err != nil {
		return cleardev.RequirementSnapshot{}, false, fmt.Errorf("list evidence: %w", err)
	}
	for _, record := range evidence {
		snapshot.Evidence = append(snapshot.Evidence, clearDevEvidenceFromListGen(record))
	}
	events, err := s.qr.ListClearDevRequirementEvents(ctx, id)
	if err != nil {
		return cleardev.RequirementSnapshot{}, false, fmt.Errorf("list events: %w", err)
	}
	for _, event := range events {
		snapshot.Events = append(snapshot.Events, clearDevEventFromGen(event))
	}
	normalizeSnapshotSlices(&snapshot)
	return snapshot, true, nil
}

// GetClearDevTaskContext resolves a task and its owning requirement for trusted
// service operations such as worktree observation and evidence recording.
func (s *Store) GetClearDevTaskContext(ctx context.Context, id string) (cleardev.DevelopmentTask, cleardev.DevelopmentRequirement, bool, error) {
	taskRow, err := s.qr.GetClearDevDevelopmentTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.DevelopmentTask{}, cleardev.DevelopmentRequirement{}, false, nil
	}
	if err != nil {
		return cleardev.DevelopmentTask{}, cleardev.DevelopmentRequirement{}, false, err
	}
	requirementRow, err := s.qr.GetClearDevRequirement(ctx, taskRow.DevelopmentProjectID)
	if err != nil {
		return cleardev.DevelopmentTask{}, cleardev.DevelopmentRequirement{}, false, err
	}
	return clearDevDevelopmentTaskFromGetGen(taskRow), clearDevRequirementFromGen(requirementRow), true, nil
}

// CreateClearDevRequirementVersion creates the next DRAFT version. The next
// number and the absence of another open version are checked in this transaction.
func (s *Store) CreateClearDevRequirementVersion(ctx context.Context, version cleardev.RequirementVersion) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev requirement version", func(q *gen.Queries) error {
		requirementRow, err := q.GetClearDevRequirement(ctx, version.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		if _, complexErr := q.GetClearDevComplexRequirement(ctx, version.DevelopmentRequirementID); complexErr == nil {
			rejected = &cleardev.RuleError{Code: cleardev.ReasonComplexPlanRequired, Message: "complex requirements cannot create extra versions through the simple path"}
			return nil
		} else if !errors.Is(complexErr, sql.ErrNoRows) {
			return complexErr
		}
		if requirement.CancelledAt != nil {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectRequirementVersion, version.ID,
				cleardev.ActionCreateRequirementVersion, "", string(cleardev.RequirementVersionStatusDraft),
				cleardev.ReasonRequirementCancelled, cleardev.EventSourceControlPlane,
				cleardev.ActionRequest{At: version.CreatedAt})
			return err
		}
		if _, openErr := q.GetOpenClearDevRequirementVersion(ctx, requirement.ID); openErr == nil {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectRequirementVersion, version.ID,
				cleardev.ActionCreateRequirementVersion, "", string(cleardev.RequirementVersionStatusDraft),
				cleardev.ReasonPreconditionNotMet, cleardev.EventSourceControlPlane,
				cleardev.ActionRequest{At: version.CreatedAt})
			return err
		} else if !errors.Is(openErr, sql.ErrNoRows) {
			return openErr
		}
		next, err := q.NextClearDevRequirementVersion(ctx, requirement.ID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(version.ID) == "" || version.Status != cleardev.RequirementVersionStatusDraft ||
			version.Version != 0 || version.TaskSetVersion != 0 || version.SupersededByID != "" || version.ConfirmedAt != nil ||
			strings.TrimSpace(version.RequirementText) == "" ||
			!requirementDigestMatches(version.RequirementText, version.SHA256) {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectRequirementVersion, version.ID,
				cleardev.ActionCreateRequirementVersion, "", string(cleardev.RequirementVersionStatusDraft),
				cleardev.ReasonPreconditionNotMet, cleardev.EventSourceControlPlane,
				cleardev.ActionRequest{At: version.CreatedAt})
			return err
		}
		version.Version = next
		if err := insertClearDevRequirementVersion(ctx, q, version); err != nil {
			return err
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectRequirementVersion, SubjectID: version.ID,
			Action: cleardev.ActionCreateRequirementVersion, TargetState: string(cleardev.RequirementVersionStatusDraft),
			Outcome: cleardev.EventAccepted, Source: cleardev.EventSourceControlPlane, CreatedAt: version.CreatedAt,
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

// CreateClearDevTask binds a new task to the current confirmed requirement
// version and atomically writes its permission, checks, task-set increment and events.
func (s *Store) CreateClearDevTask(ctx context.Context, initial cleardev.InitialDevelopmentTask) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev development task", func(q *gen.Queries) error {
		task := initial.Task
		requirementRow, err := q.GetClearDevRequirement(ctx, task.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		if requirement.CancelledAt != nil {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID,
				cleardev.ActionCreateDevelopmentTask, "", string(cleardev.DevelopmentTaskStatusPlanned),
				cleardev.ReasonRequirementCancelled, cleardev.EventSourceControlPlane,
				cleardev.ActionRequest{At: task.CreatedAt})
			return err
		}
		confirmed, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
		if errors.Is(err, sql.ErrNoRows) {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID,
				cleardev.ActionCreateDevelopmentTask, "", string(cleardev.DevelopmentTaskStatusPlanned),
				cleardev.ReasonPreconditionNotMet, cleardev.EventSourceControlPlane,
				cleardev.ActionRequest{At: task.CreatedAt})
			return err
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Title) == "" ||
			(task.Mode != cleardev.WorkModeQuick && task.Mode != cleardev.WorkModeStandard) ||
			task.Status != cleardev.DevelopmentTaskStatusPlanned || task.PausedFromStatus != "" ||
			task.MaxReworkCount < 0 || task.ReworkCount != 0 ||
			initial.Permission.DevelopmentTaskID != task.ID || initial.Permission.Version != 1 || len(initial.Checks) == 0 {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID,
				cleardev.ActionCreateDevelopmentTask, "", string(cleardev.DevelopmentTaskStatusPlanned),
				cleardev.ReasonPreconditionNotMet, cleardev.EventSourceControlPlane,
				cleardev.ActionRequest{At: task.CreatedAt})
			return err
		}
		if err := initial.Permission.Rules.Validate(); err != nil {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID,
				cleardev.ActionCreateDevelopmentTask, "", string(cleardev.DevelopmentTaskStatusPlanned),
				cleardev.ReasonPreconditionNotMet, cleardev.EventSourceControlPlane,
				cleardev.ActionRequest{At: task.CreatedAt, ReasonText: err.Error()})
			return err
		}
		for _, check := range initial.Checks {
			if check.DevelopmentTaskID != task.ID || strings.TrimSpace(check.ID) == "" || strings.TrimSpace(check.Name) == "" || strings.TrimSpace(check.Kind) == "" {
				rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID,
					cleardev.ActionCreateDevelopmentTask, "", string(cleardev.DevelopmentTaskStatusPlanned),
					cleardev.ReasonPreconditionNotMet, cleardev.EventSourceControlPlane,
					cleardev.ActionRequest{At: task.CreatedAt})
				return err
			}
		}
		task.RequirementVersionID = confirmed.ID
		if rejected, err = rejectIfDirectionStopped(ctx, q, requirement, confirmed.ID, cleardev.SubjectDevelopmentTask, task.ID,
			cleardev.ActionCreateDevelopmentTask, "", string(cleardev.DevelopmentTaskStatusPlanned),
			cleardev.ActionRequest{At: task.CreatedAt}); err != nil || rejected != nil {
			return err
		}
		if err := insertClearDevDevelopmentTask(ctx, q, task); err != nil {
			return err
		}
		if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectDevelopmentTask, SubjectID: task.ID,
			Action: cleardev.ActionCreateDevelopmentTask, TargetState: string(task.Status),
			Outcome: cleardev.EventAccepted, Source: cleardev.EventSourceControlPlane, CreatedAt: task.CreatedAt,
		}); err != nil {
			return err
		}
		if err := insertClearDevPermission(ctx, q, initial.Permission); err != nil {
			return err
		}
		if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectPermission, SubjectID: initial.Permission.ID,
			Action: cleardev.ActionCreatePermissionVersion, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, CreatedAt: initial.Permission.CreatedAt,
		}); err != nil {
			return err
		}
		for _, check := range initial.Checks {
			if err := q.InsertClearDevRequiredCheck(ctx, gen.InsertClearDevRequiredCheckParams{
				ID: check.ID, WorkItemID: check.DevelopmentTaskID, Name: check.Name,
				CheckKind: check.Kind, CreatedAt: check.CreatedAt,
			}); err != nil {
				return fmt.Errorf("insert ClearDev required check: %w", err)
			}
			if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
				AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
				SubjectType: cleardev.SubjectRequiredCheck, SubjectID: check.ID,
				Action: cleardev.ActionCreateRequiredCheck, Outcome: cleardev.EventAccepted,
				Source: cleardev.EventSourceControlPlane, CreatedAt: check.CreatedAt,
			}); err != nil {
				return err
			}
		}
		rows, err := q.IncrementClearDevTaskSetVersionCAS(ctx, gen.IncrementClearDevTaskSetVersionCASParams{
			ID: confirmed.ID, ExpectedTaskSetVersion: confirmed.TaskSetVersion,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("increment task set version: CAS affected %d rows", rows)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// AppendClearDevPermissionVersion appends immutable path rules while the task
// is still current and PLANNED.
func (s *Store) AppendClearDevPermissionVersion(ctx context.Context, permission cleardev.PermissionVersion) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "append ClearDev permission version", func(q *gen.Queries) error {
		taskRow, err := q.GetClearDevDevelopmentTask(ctx, permission.DevelopmentTaskID)
		if err != nil {
			return err
		}
		task := clearDevDevelopmentTaskFromGetGen(taskRow)
		requirementRow, err := q.GetClearDevRequirement(ctx, task.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		confirmed, confirmedErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
		if confirmedErr != nil && !errors.Is(confirmedErr, sql.ErrNoRows) {
			return confirmedErr
		}
		next, nextErr := q.NextClearDevPermissionVersion(ctx, task.ID)
		if nextErr != nil {
			return nextErr
		}
		reason := cleardev.ReasonPreconditionNotMet
		if requirement.CancelledAt != nil {
			reason = cleardev.ReasonRequirementCancelled
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, task.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectPermission, permission.ID,
				cleardev.ActionCreatePermissionVersion, "", "", cleardev.ReasonDirectionChangeStopped,
				cleardev.EventSourceControlPlane, cleardev.ActionRequest{At: permission.CreatedAt})
			return err
		}
		if requirement.CancelledAt != nil || confirmedErr != nil || confirmed.ID != task.RequirementVersionID ||
			task.Status != cleardev.DevelopmentTaskStatusPlanned || permission.Version != next {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectPermission, permission.ID,
				cleardev.ActionCreatePermissionVersion, "", "", reason, cleardev.EventSourceControlPlane,
				cleardev.ActionRequest{At: permission.CreatedAt})
			return err
		}
		if err := permission.Rules.Validate(); err != nil {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectPermission, permission.ID,
				cleardev.ActionCreatePermissionVersion, "", "", cleardev.ReasonPreconditionNotMet,
				cleardev.EventSourceControlPlane, cleardev.ActionRequest{At: permission.CreatedAt, ReasonText: err.Error()})
			return err
		}
		if err := insertClearDevPermission(ctx, q, permission); err != nil {
			return err
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectPermission, SubjectID: permission.ID,
			Action: cleardev.ActionCreatePermissionVersion, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, CreatedAt: permission.CreatedAt,
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

// ApplyClearDevAction performs one typed lifecycle action and records accepted
// or rejected results in the same transaction as its compare-and-swap update.
func (s *Store) ApplyClearDevAction(ctx context.Context, request cleardev.ActionRequest) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "apply ClearDev action", func(q *gen.Queries) error {
		var err error
		switch request.Action {
		case cleardev.ActionSubmitRequirementConfirmation, cleardev.ActionConfirmRequirementVersion,
			cleardev.ActionRejectRequirementVersion:
			rejected, err = applyClearDevRequirementVersionAction(ctx, q, request)
		case cleardev.ActionCancelRequirement:
			rejected, err = applyClearDevRequirementCancellation(ctx, q, request)
		case cleardev.ActionStartDevelopmentTask, cleardev.ActionSubmitDevelopmentTaskReview,
			cleardev.ActionCompleteDevelopmentTask, cleardev.ActionRequestDevelopmentTaskRework,
			cleardev.ActionEscalateDevelopmentTask, cleardev.ActionRestartDevelopmentTask,
			cleardev.ActionPauseDevelopmentTaskNeedsHuman, cleardev.ActionBlockDevelopmentTask,
			cleardev.ActionResumeDevelopmentTask, cleardev.ActionCancelDevelopmentTask:
			rejected, err = applyClearDevDevelopmentTaskActionGated(ctx, q, request, true)
		default:
			return fmt.Errorf("unsupported ClearDev action %q", request.Action)
		}
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

func applyClearDevRequirementVersionAction(ctx context.Context, q *gen.Queries, request cleardev.ActionRequest) (*cleardev.RuleError, error) {
	row, err := q.GetClearDevRequirementVersion(ctx, request.SubjectID)
	if err != nil {
		return nil, err
	}
	version := clearDevRequirementVersionFromGen(row)
	requirementRow, err := q.GetClearDevRequirement(ctx, version.DevelopmentRequirementID)
	if err != nil {
		return nil, err
	}
	requirement := clearDevRequirementFromGen(requirementRow)
	from := version.Status
	var to cleardev.RequirementVersionStatus
	source := cleardev.EventSourceControlPlane
	switch request.Action {
	case cleardev.ActionSubmitRequirementConfirmation:
		to = cleardev.RequirementVersionStatusPendingConfirmation
	case cleardev.ActionConfirmRequirementVersion:
		to = cleardev.RequirementVersionStatusConfirmed
		if request.TrustedHumanDecision {
			source = cleardev.EventSourceHumanDecision
		}
	case cleardev.ActionRejectRequirementVersion:
		to = cleardev.RequirementVersionStatusRejected
		if request.TrustedHumanDecision {
			source = cleardev.EventSourceHumanDecision
		}
	}
	if requirement.CancelledAt != nil {
		return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectRequirementVersion, version.ID,
			request.Action, string(from), string(to), cleardev.ReasonRequirementCancelled, source, request)
	}
	decision := cleardev.ValidateRequirementVersionTransition(from, to, cleardev.RequirementVersionTransitionInput{
		HasContent: strings.TrimSpace(version.RequirementText) != "", HasDigest: requirementDigestMatches(version.RequirementText, version.SHA256),
		TrustedHumanDecision: request.TrustedHumanDecision,
	})
	if request.Action == cleardev.ActionRejectRequirementVersion && strings.TrimSpace(request.ReasonText) == "" {
		decision = cleardev.TransitionDecision{Reason: cleardev.ReasonPreconditionNotMet}
	}
	if !decision.Allowed {
		return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectRequirementVersion, version.ID,
			request.Action, string(from), string(to), decision.Reason, source, request)
	}
	if request.Action == cleardev.ActionConfirmRequirementVersion {
		current, currentErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
		if currentErr != nil && !errors.Is(currentErr, sql.ErrNoRows) {
			return nil, currentErr
		}
		if currentErr == nil {
			if current.ID == version.ID || current.Version >= version.Version {
				return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectRequirementVersion, version.ID,
					request.Action, string(from), string(to), cleardev.ReasonPreconditionNotMet, source, request)
			}
			rows, updateErr := q.UpdateClearDevRequirementVersionStateCAS(ctx, gen.UpdateClearDevRequirementVersionStateCASParams{
				NextState:      physicalRequirementVersionStatus(cleardev.RequirementVersionStatusSuperseded),
				SupersededByID: nullableString(version.ID), ApprovedAt: current.ApprovedAt,
				ID: current.ID, ExpectedState: "APPROVED",
			})
			if updateErr != nil {
				return nil, updateErr
			}
			if rows != 1 {
				return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectRequirementVersion, version.ID,
					request.Action, string(from), string(to), cleardev.ReasonInvalidTransition, source, request)
			}
			if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
				AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
				SubjectType: cleardev.SubjectRequirementVersion, SubjectID: current.ID,
				Action:        cleardev.ActionSupersedeRequirementVersion,
				PreviousState: string(cleardev.RequirementVersionStatusConfirmed),
				TargetState:   string(cleardev.RequirementVersionStatusSuperseded), Outcome: cleardev.EventAccepted,
				Source: source, CreatedAt: request.At,
			}); err != nil {
				return nil, err
			}
		}
	}
	confirmedAt := sql.NullTime{}
	if to == cleardev.RequirementVersionStatusConfirmed {
		confirmedAt = sql.NullTime{Time: request.At, Valid: true}
	} else if version.ConfirmedAt != nil {
		confirmedAt = sql.NullTime{Time: *version.ConfirmedAt, Valid: true}
	}
	rows, err := q.UpdateClearDevRequirementVersionStateCAS(ctx, gen.UpdateClearDevRequirementVersionStateCASParams{
		NextState: physicalRequirementVersionStatus(to), SupersededByID: nullableString(version.SupersededByID),
		ApprovedAt: confirmedAt, ID: version.ID, ExpectedState: physicalRequirementVersionStatus(from),
	})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectRequirementVersion, version.ID,
			request.Action, string(from), string(to), cleardev.ReasonInvalidTransition, source, request)
	}
	err = insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectRequirementVersion, SubjectID: version.ID, Action: request.Action,
		PreviousState: string(from), TargetState: string(to), Outcome: cleardev.EventAccepted,
		Reason: request.Reason, ReasonText: request.ReasonText, Source: source,
		SourceAOSessionID: request.SourceAOSessionID, CreatedAt: request.At,
	})
	if err != nil {
		return nil, err
	}
	if request.Action == cleardev.ActionSubmitRequirementConfirmation {
		return nil, ensureConfirmHumanDecisionRequest(ctx, q, requirement, version, request.At)
	}
	if request.Action == cleardev.ActionConfirmRequirementVersion {
		return nil, trySettleMatchingConfirmRequest(ctx, q, version.ID, cleardev.HumanDecisionApprove, request.At)
	}
	if request.Action == cleardev.ActionRejectRequirementVersion {
		return nil, trySettleMatchingConfirmRequest(ctx, q, version.ID, cleardev.HumanDecisionReject, request.At)
	}
	return nil, nil
}

func applyClearDevRequirementCancellation(ctx context.Context, q *gen.Queries, request cleardev.ActionRequest) (*cleardev.RuleError, error) {
	row, err := q.GetClearDevRequirement(ctx, request.SubjectID)
	if err != nil {
		return nil, err
	}
	requirement := clearDevRequirementFromGen(row)
	source := cleardev.EventSourceControlPlane
	if request.TrustedHumanDecision {
		source = cleardev.EventSourceHumanDecision
	}
	if requirement.CancelledAt != nil {
		return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentRequirement, requirement.ID,
			request.Action, "CANCELLED", "CANCELLED", cleardev.ReasonRequirementCancelled,
			source, request)
	}
	if !request.TrustedHumanDecision || strings.TrimSpace(request.ReasonText) == "" {
		reason := cleardev.ReasonHumanDecisionRequired
		if request.TrustedHumanDecision {
			reason = cleardev.ReasonPreconditionNotMet
		}
		return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentRequirement, requirement.ID,
			request.Action, "", "CANCELLED", reason, source, request)
	}
	rows, err := q.CancelClearDevRequirementCAS(ctx, gen.CancelClearDevRequirementCASParams{
		CancelledAt:      sql.NullTime{Time: request.At, Valid: true},
		CancelReasonCode: string(cleardev.ReasonUserCancelled), CancelReasonText: request.ReasonText,
		UpdatedAt: request.At, ID: requirement.ID,
	})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentRequirement, requirement.ID,
			request.Action, "", "CANCELLED", cleardev.ReasonInvalidTransition,
			source, request)
	}
	return nil, insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectDevelopmentRequirement, SubjectID: requirement.ID,
		Action: request.Action, TargetState: "CANCELLED", Outcome: cleardev.EventAccepted,
		Reason: cleardev.ReasonUserCancelled, ReasonText: request.ReasonText,
		Source: source, CreatedAt: request.At,
	})
}

func applyClearDevDevelopmentTaskAction(ctx context.Context, q *gen.Queries, request cleardev.ActionRequest) (*cleardev.RuleError, error) {
	return applyClearDevDevelopmentTaskActionGated(ctx, q, request, true)
}

func applyClearDevDevelopmentTaskActionGated(ctx context.Context, q *gen.Queries, request cleardev.ActionRequest, enforceDirectionGate bool) (*cleardev.RuleError, error) {
	row, err := q.GetClearDevDevelopmentTask(ctx, request.SubjectID)
	if err != nil {
		return nil, err
	}
	task := clearDevDevelopmentTaskFromGetGen(row)
	requirementRow, err := q.GetClearDevRequirement(ctx, task.DevelopmentRequirementID)
	if err != nil {
		return nil, err
	}
	requirement := clearDevRequirementFromGen(requirementRow)
	from := task.Status
	var to cleardev.DevelopmentTaskStatus
	switch request.Action {
	case cleardev.ActionStartDevelopmentTask, cleardev.ActionRestartDevelopmentTask:
		to = cleardev.DevelopmentTaskStatusRunning
	case cleardev.ActionSubmitDevelopmentTaskReview:
		to = cleardev.DevelopmentTaskStatusReview
	case cleardev.ActionCompleteDevelopmentTask:
		to = cleardev.DevelopmentTaskStatusDone
	case cleardev.ActionRequestDevelopmentTaskRework:
		to = cleardev.DevelopmentTaskStatusRework
	case cleardev.ActionEscalateDevelopmentTask, cleardev.ActionPauseDevelopmentTaskNeedsHuman:
		to = cleardev.DevelopmentTaskStatusNeedsHuman
	case cleardev.ActionBlockDevelopmentTask:
		to = cleardev.DevelopmentTaskStatusBlocked
	case cleardev.ActionResumeDevelopmentTask:
		to = task.PausedFromStatus
	case cleardev.ActionCancelDevelopmentTask:
		to = cleardev.DevelopmentTaskStatusCancelled
	}
	if requirement.CancelledAt != nil {
		return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID,
			request.Action, string(from), string(to), cleardev.ReasonRequirementCancelled,
			cleardev.EventSourceControlPlane, request)
	}
	if enforceDirectionGate {
		if stopped, err := activeDirectionStop(ctx, q, task.RequirementVersionID); err != nil {
			return nil, err
		} else if stopped {
			return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID,
				request.Action, string(from), string(to), cleardev.ReasonDirectionChangeStopped,
				cleardev.EventSourceControlPlane, request)
		}
	}
	// S02 dispatch-bound tasks have a narrower, source-bound completion path.
	// The generic S01 transition can evaluate only generic evidence, so it must
	// never mark one of those tasks DONE.  The dedicated STANDARD completion
	// transaction records the integration candidate/evidence before its final
	// CAS, and the database trigger independently enforces the same boundary.
	if request.Action == cleardev.ActionCompleteDevelopmentTask {
		dispatches, listErr := q.ListClearDevStandardDispatches(ctx, task.RequirementVersionID)
		if listErr != nil {
			return nil, listErr
		}
		for _, row := range dispatches {
			dispatch := clearDevStandardDispatchFromGen(row)
			if dispatch.Status == cleardev.DispatchStatusAccepted && dispatch.PreallocatedDevelopmentTaskID == task.ID {
				return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID,
					request.Action, string(from), string(to), cleardev.ReasonPreconditionNotMet,
					cleardev.EventSourceControlPlane, request)
			}
		}
	}
	facts, err := clearDevDevelopmentTaskTransitionFacts(ctx, q, requirement, task, request.At)
	if err != nil {
		return nil, err
	}
	decision := cleardev.ValidateDevelopmentTaskTransition(from, to, facts)
	if request.Action == cleardev.ActionEscalateDevelopmentTask &&
		(!facts.EvidenceFailed || facts.CurrentReworkCount < facts.MaxReworkCount) {
		decision = cleardev.TransitionDecision{Reason: cleardev.ReasonPreconditionNotMet}
	}
	if (request.Action == cleardev.ActionPauseDevelopmentTaskNeedsHuman || request.Action == cleardev.ActionBlockDevelopmentTask) && strings.TrimSpace(request.ReasonText) == "" {
		decision = cleardev.TransitionDecision{Reason: cleardev.ReasonPreconditionNotMet}
	}
	if !decision.Allowed {
		return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID,
			request.Action, string(from), string(to), decision.Reason, cleardev.EventSourceControlPlane, request)
	}
	nextReworkCount := task.ReworkCount
	if request.Action == cleardev.ActionRestartDevelopmentTask {
		nextReworkCount = decision.NextReworkCount
	}
	rawFrom := row.State
	rawTarget := string(to)
	pausedFrom := ""
	if to == cleardev.DevelopmentTaskStatusNeedsHuman || to == cleardev.DevelopmentTaskStatusBlocked {
		pausedFrom = rawFrom
	}
	if request.Action == cleardev.ActionResumeDevelopmentTask {
		rawTarget = row.PausedFromState.String
	}
	rows, err := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
		NextState: rawTarget, PausedFromState: nullableString(pausedFrom), ReworkCount: int64(nextReworkCount),
		UpdatedAt: request.At, ID: task.ID, ExpectedState: rawFrom, ExpectedReworkCount: int64(task.ReworkCount),
	})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return rejectClearDevAction(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID,
			request.Action, string(from), string(to), cleardev.ReasonInvalidTransition,
			cleardev.EventSourceControlPlane, request)
	}
	if request.Action == cleardev.ActionCancelDevelopmentTask {
		version, getErr := q.GetClearDevRequirementVersion(ctx, task.RequirementVersionID)
		if getErr != nil {
			return nil, getErr
		}
		rows, updateErr := q.IncrementClearDevTaskSetVersionCAS(ctx, gen.IncrementClearDevTaskSetVersionCASParams{
			ID: version.ID, ExpectedTaskSetVersion: version.TaskSetVersion,
		})
		if updateErr != nil {
			return nil, updateErr
		}
		if rows != 1 {
			return nil, fmt.Errorf("increment task set version: CAS affected %d rows", rows)
		}
	}
	return nil, insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: cleardev.SubjectDevelopmentTask, SubjectID: task.ID, Action: request.Action,
		PreviousState: string(from), TargetState: string(to), Outcome: cleardev.EventAccepted,
		Reason: decision.Reason, ReasonText: request.ReasonText, Source: cleardev.EventSourceControlPlane, CreatedAt: request.At,
	})
}

func clearDevDevelopmentTaskTransitionFacts(ctx context.Context, q *gen.Queries, requirement cleardev.DevelopmentRequirement, task cleardev.DevelopmentTask, now time.Time) (cleardev.DevelopmentTaskTransitionInput, error) {
	facts := cleardev.DevelopmentTaskTransitionInput{
		Mode: task.Mode, RequirementCancelled: requirement.CancelledAt != nil,
		MaxReworkCount: task.MaxReworkCount, CurrentReworkCount: task.ReworkCount,
		SuspendedFrom: task.PausedFromStatus,
	}
	confirmed, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
	if err == nil {
		facts.IsCurrentRequirementVersion = confirmed.ID == task.RequirementVersionID
	} else if !errors.Is(err, sql.ErrNoRows) {
		return facts, err
	}
	candidate, err := q.GetCurrentClearDevCandidateCommit(ctx, task.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return facts, nil
	}
	if err != nil {
		return facts, err
	}
	// A settled requirement final review REWORK is failed evidence for this
	// exact candidate: it is the human's basis for sending the reviewed task
	// back to its Builder within the existing rework budget. It must be
	// evaluated before any round-gated early return below.
	if review, reviewErr := q.GetLatestClearDevRequirementFinalReview(ctx, task.DevelopmentRequirementID); reviewErr == nil {
		if review.Status == "SETTLED" && review.Verdict.Valid && (review.Verdict.String == "REWORK" || projectBlockedFinalReview(ctx, q, review)) && review.CandidateCommitSha == candidate.CommitSha {
			facts.EvidenceFailed = true
		}
	} else if !errors.Is(reviewErr, sql.ErrNoRows) {
		return facts, reviewErr
	}
	if failed, err := projectCheckFailureEvidence(ctx, q, task, candidate.CommitSha); err != nil {
		return facts, err
	} else if failed {
		facts.EvidenceFailed = true
		// Complex candidates use their source-bound attempt rather than the
		// generic REGISTER_CANDIDATE event. The exact current failed attempt
		// also proves a candidate for resume -> review -> rework, never PASS.
		facts.HasCurrentCandidate = true
	}
	currentRound, err := q.IsClearDevCandidateCurrentRound(ctx, gen.IsClearDevCandidateCurrentRoundParams{
		CandidateID: candidate.ID, DevelopmentTaskID: task.ID,
	})
	if err != nil || currentRound == 0 {
		return facts, err
	}
	facts.HasCurrentCandidate = true
	checks, err := q.ListClearDevRequiredChecks(ctx, task.ID)
	if err != nil {
		return facts, err
	}
	records, err := q.ListClearDevCandidateEvidence(ctx, nullableString(candidate.ID))
	if err != nil {
		return facts, err
	}
	projection := make([]cleardev.CompletionEvidence, 0, len(records))
	latest := make(map[string]cleardev.CompletionEvidence)
	for _, record := range records {
		expiresAt := time.Time{}
		if record.ExpiresAt.Valid {
			expiresAt = record.ExpiresAt.Time
		}
		projected := cleardev.CompletionEvidence{
			CandidateID: record.CandidateCommitID.String, CommitSHA: record.CommitSha,
			Kind: cleardev.EvidenceKind(record.EvidenceKind), RequiredCheck: record.EvidenceKey,
			Result: cleardev.EvidenceResult(record.Result), Source: cleardev.EvidenceSource(record.SourceType),
			SourceSessionID: record.SourceAoSessionID.String, Sequence: record.Sequence, ExpiresAt: expiresAt,
		}
		projection = append(projection, projected)
		latest[record.EvidenceKind+"\x00"+record.EvidenceKey] = projected
	}
	requiredNames := make([]string, 0, len(checks))
	for _, check := range checks {
		requiredNames = append(requiredNames, check.Name)
	}
	completion := cleardev.EvaluateCompletion(task.Mode, cleardev.CompletionCandidate{
		ID: candidate.ID, CommitSHA: candidate.CommitSha, BuilderSessionID: candidate.AoSessionID,
	}, requiredNames, projection, now)
	facts.CompletionSatisfied = completion.Satisfied
	for _, result := range latest {
		if (result.ExpiresAt.IsZero() || now.Before(result.ExpiresAt)) && result.Result == cleardev.EvidenceResultFail {
			facts.EvidenceFailed = true
			break
		}
	}
	return facts, nil
}

// AppendClearDevCandidate appends a trusted observation for a current RUNNING
// task and binds it to the latest immutable permission version.
func (s *Store) AppendClearDevCandidate(ctx context.Context, observation cleardev.CandidateObservation) (cleardev.CandidateCommit, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.CandidateCommit
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "append ClearDev candidate", func(q *gen.Queries) error {
		taskRow, err := q.GetClearDevDevelopmentTask(ctx, observation.DevelopmentTaskID)
		if err != nil {
			return err
		}
		task := clearDevDevelopmentTaskFromGetGen(taskRow)
		requirementRow, err := q.GetClearDevRequirement(ctx, task.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		confirmed, confirmedErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
		if confirmedErr != nil && !errors.Is(confirmedErr, sql.ErrNoRows) {
			return confirmedErr
		}
		sha, shaErr := cleardev.NormalizeCommitSHA(observation.CommitSHA)
		session, sessionErr := q.GetSession(ctx, domain.SessionID(observation.AOSessionID))
		if sessionErr != nil && !errors.Is(sessionErr, sql.ErrNoRows) {
			return sessionErr
		}
		validSession := sessionErr == nil && string(session.ProjectID) == requirement.AOProjectID && !session.IsTerminated
		reason := cleardev.ReasonPreconditionNotMet
		valid := requirement.CancelledAt == nil && confirmedErr == nil && confirmed.ID == task.RequirementVersionID &&
			task.Status == cleardev.DevelopmentTaskStatusRunning && shaErr == nil && validSession
		if requirement.CancelledAt != nil {
			reason = cleardev.ReasonRequirementCancelled
		} else if shaErr != nil || !validSession {
			reason = cleardev.ReasonCandidateMismatch
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, task.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			reason = cleardev.ReasonDirectionChangeStopped
			valid = false
		}
		if !valid {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectCandidate, observation.ID,
				cleardev.ActionRegisterCandidate, string(task.Status), "", reason, cleardev.EventSourceControlPlane,
				cleardev.ActionRequest{At: observation.ObservedAt, SourceAOSessionID: observation.AOSessionID})
			return err
		}
		permission, err := q.GetLatestClearDevPermissionVersion(ctx, task.ID)
		if err != nil {
			return err
		}
		sequence, err := q.NextClearDevCandidateSequence(ctx, task.ID)
		if err != nil {
			return err
		}
		result = cleardev.CandidateCommit{
			ID: observation.ID, DevelopmentTaskID: task.ID, Sequence: sequence,
			AOSessionID: observation.AOSessionID, PermissionVersionID: permission.ID,
			CommitSHA: sha, CreatedAt: observation.ObservedAt,
		}
		if err := q.InsertClearDevCandidateCommit(ctx, gen.InsertClearDevCandidateCommitParams{
			ID: result.ID, WorkItemID: result.DevelopmentTaskID, Sequence: result.Sequence,
			AoSessionID: result.AOSessionID, PermissionVersionID: result.PermissionVersionID,
			CommitSha: result.CommitSHA, CreatedAt: result.CreatedAt,
		}); err != nil {
			return err
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectCandidate, SubjectID: result.ID,
			Action: cleardev.ActionRegisterCandidate, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, SourceAOSessionID: result.AOSessionID, CreatedAt: result.CreatedAt,
		})
	})
	if err != nil {
		return cleardev.CandidateCommit{}, err
	}
	if rejected != nil {
		return cleardev.CandidateCommit{}, rejected
	}
	return result, nil
}

// AppendClearDevIntegrationCandidate binds an observed clean HEAD to the
// current confirmed requirement version and its exact task-set version.
func (s *Store) AppendClearDevIntegrationCandidate(ctx context.Context, observation cleardev.IntegrationCandidateObservation) (cleardev.IntegrationCandidate, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.IntegrationCandidate
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "append ClearDev integration candidate", func(q *gen.Queries) error {
		requirementRow, err := q.GetClearDevRequirement(ctx, observation.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		confirmed, confirmedErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
		if confirmedErr != nil && !errors.Is(confirmedErr, sql.ErrNoRows) {
			return confirmedErr
		}
		if confirmedErr == nil {
			if stopped, stopErr := activeDirectionStop(ctx, q, confirmed.ID); stopErr != nil {
				return stopErr
			} else if stopped {
				rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectIntegrationCandidate,
					observation.ID, cleardev.ActionRegisterIntegrationCandidate, "", "", cleardev.ReasonDirectionChangeStopped,
					cleardev.EventSourceControlPlane, cleardev.ActionRequest{At: observation.ObservedAt, SourceAOSessionID: observation.AOSessionID})
				return err
			}
		}
		sha, shaErr := cleardev.NormalizeCommitSHA(observation.CommitSHA)
		session, sessionErr := q.GetSession(ctx, domain.SessionID(observation.AOSessionID))
		if sessionErr != nil && !errors.Is(sessionErr, sql.ErrNoRows) {
			return sessionErr
		}
		validSession := sessionErr == nil && string(session.ProjectID) == requirement.AOProjectID && !session.IsTerminated
		allDone := false
		if confirmedErr == nil {
			tasks, listErr := q.ListClearDevDevelopmentTasksForVersion(ctx, confirmed.ID)
			if listErr != nil {
				return listErr
			}
			activeCount := 0
			allDone = true
			for _, task := range tasks {
				if task.State == "CANCELLED" {
					continue
				}
				activeCount++
				if task.State != "DONE" {
					allDone = false
				}
			}
			allDone = allDone && activeCount > 0
		}
		reason := cleardev.ReasonPreconditionNotMet
		if requirement.CancelledAt != nil {
			reason = cleardev.ReasonRequirementCancelled
		} else if shaErr != nil || !validSession {
			reason = cleardev.ReasonCandidateMismatch
		}
		if requirement.CancelledAt != nil || confirmedErr != nil || !allDone || shaErr != nil || !validSession {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectIntegrationCandidate,
				observation.ID, cleardev.ActionRegisterIntegrationCandidate, "", "", reason,
				cleardev.EventSourceControlPlane,
				cleardev.ActionRequest{At: observation.ObservedAt, SourceAOSessionID: observation.AOSessionID})
			return err
		}
		sequence, err := q.NextClearDevIntegrationCandidateSequence(ctx, requirement.ID)
		if err != nil {
			return err
		}
		taskSet := confirmed.TaskSetVersion
		result = cleardev.IntegrationCandidate{
			ID: observation.ID, DevelopmentRequirementID: requirement.ID, RequirementVersionID: confirmed.ID,
			TaskSetVersion: &taskSet, Sequence: sequence, AOSessionID: observation.AOSessionID,
			CommitSHA: sha, CreatedAt: observation.ObservedAt,
		}
		if err := q.InsertClearDevIntegrationCandidate(ctx, gen.InsertClearDevIntegrationCandidateParams{
			ID: result.ID, DevelopmentProjectID: result.DevelopmentRequirementID,
			RequirementVersionID: nullableString(result.RequirementVersionID), TaskSetVersion: nullableInt64Ptr(result.TaskSetVersion),
			Sequence: result.Sequence, AoSessionID: result.AOSessionID, CommitSha: result.CommitSHA, CreatedAt: result.CreatedAt,
		}); err != nil {
			return err
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectIntegrationCandidate, SubjectID: result.ID,
			Action: cleardev.ActionRegisterIntegrationCandidate, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, SourceAOSessionID: result.AOSessionID, CreatedAt: result.CreatedAt,
		})
	})
	if err != nil {
		return cleardev.IntegrationCandidate{}, err
	}
	if rejected != nil {
		return cleardev.IntegrationCandidate{}, rejected
	}
	return result, nil
}

// AppendClearDevEvidence validates candidate identity and trusted source before
// appending a result. Critical identity checks are repeated by SQLite triggers.
func (s *Store) AppendClearDevEvidence(ctx context.Context, record cleardev.EvidenceRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "append ClearDev evidence", func(q *gen.Queries) error {
		requirementRow, err := q.GetClearDevRequirement(ctx, record.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		if requirement.CancelledAt != nil {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectEvidence, record.ID,
				cleardev.ActionRecordEvidence, "", "", cleardev.ReasonRequirementCancelled,
				eventSourceForEvidence(record.Source), cleardev.ActionRequest{At: record.CreatedAt})
			return err
		}
		confirmed, confirmedErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
		if confirmedErr != nil && !errors.Is(confirmedErr, sql.ErrNoRows) {
			return confirmedErr
		}
		if confirmedErr == nil {
			if stopped, stopErr := activeDirectionStop(ctx, q, confirmed.ID); stopErr != nil {
				return stopErr
			} else if stopped {
				rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectEvidence, record.ID,
					cleardev.ActionRecordEvidence, "", "", cleardev.ReasonDirectionChangeStopped,
					eventSourceForEvidence(record.Source), cleardev.ActionRequest{At: record.CreatedAt})
				return err
			}
		}
		sha, shaErr := cleardev.NormalizeCommitSHA(record.CommitSHA)
		if shaErr != nil {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectEvidence, record.ID,
				cleardev.ActionRecordEvidence, "", "", cleardev.ReasonCandidateMismatch,
				eventSourceForEvidence(record.Source), cleardev.ActionRequest{At: record.CreatedAt})
			return err
		}
		record.CommitSHA = sha
		valid := false
		var taskCandidate gen.GetCurrentClearDevCandidateCommitRow
		if record.SubjectType == cleardev.SubjectDevelopmentTask {
			task, taskErr := q.GetClearDevDevelopmentTask(ctx, record.SubjectID)
			candidate, candidateErr := q.GetCurrentClearDevCandidateCommit(ctx, record.SubjectID)
			confirmed, confirmedErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
			if confirmedErr != nil && !errors.Is(confirmedErr, sql.ErrNoRows) {
				return confirmedErr
			}
			if taskErr != nil && !errors.Is(taskErr, sql.ErrNoRows) {
				return taskErr
			}
			if candidateErr != nil && !errors.Is(candidateErr, sql.ErrNoRows) {
				return candidateErr
			}
			taskCandidate = candidate
			valid = taskErr == nil && candidateErr == nil && confirmedErr == nil &&
				task.DevelopmentProjectID == requirement.ID && task.ContractVersionID == confirmed.ID &&
				candidate.ID == record.CandidateCommitID && candidate.CommitSha == sha && record.IntegrationCandidateID == ""
			if valid {
				currentRound, roundErr := q.IsClearDevCandidateCurrentRound(ctx, gen.IsClearDevCandidateCurrentRoundParams{
					CandidateID: candidate.ID, DevelopmentTaskID: record.SubjectID,
				})
				if roundErr != nil {
					return roundErr
				}
				valid = currentRound != 0
			}
			if valid && record.Kind == cleardev.EvidenceKindRequiredCheck {
				checks, listErr := q.ListClearDevRequiredChecks(ctx, record.SubjectID)
				if listErr != nil {
					return listErr
				}
				valid = false
				for _, check := range checks {
					if check.Name == record.Key {
						valid = true
						break
					}
				}
			}
		} else if record.SubjectType == cleardev.SubjectDevelopmentRequirement && record.SubjectID == requirement.ID && record.Kind == cleardev.EvidenceKindIntegration {
			confirmed, confirmedErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID)
			if confirmedErr != nil && !errors.Is(confirmedErr, sql.ErrNoRows) {
				return confirmedErr
			}
			if confirmedErr == nil {
				candidate, candidateErr := q.GetCurrentMatchingClearDevIntegrationCandidate(ctx, gen.GetCurrentMatchingClearDevIntegrationCandidateParams{
					DevelopmentRequirementID: requirement.ID, RequirementVersionID: nullableString(confirmed.ID),
					TaskSetVersion: nullableInt64(confirmed.TaskSetVersion),
				})
				if candidateErr != nil && !errors.Is(candidateErr, sql.ErrNoRows) {
					return candidateErr
				}
				valid = candidateErr == nil && candidate.ID == record.IntegrationCandidateID && candidate.CommitSha == sha && record.CandidateCommitID == ""
			}
		}
		if record.Kind == cleardev.EvidenceKindReview {
			sourceSession, sourceErr := q.GetSession(ctx, domain.SessionID(record.SourceAOSessionID))
			if sourceErr != nil && !errors.Is(sourceErr, sql.ErrNoRows) {
				return sourceErr
			}
			valid = valid && record.Source == cleardev.EvidenceSourceReviewAdapter && sourceErr == nil &&
				string(sourceSession.ProjectID) == requirement.AOProjectID && string(sourceSession.ID) != taskCandidate.AoSessionID
		} else {
			valid = valid && record.Source == cleardev.EvidenceSourceControlPlaneChecker && record.SourceAOSessionID == ""
		}
		if !valid {
			rejected, err = rejectClearDevAction(ctx, q, requirement, cleardev.SubjectEvidence, record.ID,
				cleardev.ActionRecordEvidence, "", "", cleardev.ReasonCandidateMismatch,
				eventSourceForEvidence(record.Source), cleardev.ActionRequest{At: record.CreatedAt})
			return err
		}
		if err := q.InsertClearDevEvidence(ctx, gen.InsertClearDevEvidenceParams{
			ID: record.ID, DevelopmentProjectID: record.DevelopmentRequirementID,
			SubjectType: physicalEvidenceSubject(record.SubjectType), SubjectID: record.SubjectID,
			EvidenceKind: string(record.Kind), EvidenceKey: record.Key, Result: string(record.Result),
			CandidateCommitID: nullableString(record.CandidateCommitID), IntegrationCandidateID: nullableString(record.IntegrationCandidateID),
			CommitSha: record.CommitSHA, SourceType: string(record.Source), SourceAoSessionID: nullableString(record.SourceAOSessionID),
			CreatedAt: record.CreatedAt, ExpiresAt: nullableTimePtr(record.ExpiresAt),
		}); err != nil {
			return err
		}
		return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectEvidence, SubjectID: record.ID, Action: cleardev.ActionRecordEvidence,
			Outcome: cleardev.EventAccepted, Source: eventSourceForEvidence(record.Source),
			SourceAOSessionID: record.SourceAOSessionID, CreatedAt: record.CreatedAt,
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

func rejectClearDevAction(ctx context.Context, q *gen.Queries, requirement cleardev.DevelopmentRequirement,
	subjectType cleardev.SubjectType, subjectID string, action cleardev.Action,
	previousState, targetState string, reason cleardev.ReasonCode, source cleardev.EventSource,
	request cleardev.ActionRequest,
) (*cleardev.RuleError, error) {
	if reason == cleardev.ReasonNone {
		reason = cleardev.ReasonInvalidTransition
	}
	if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: subjectType, SubjectID: subjectID, Action: action,
		PreviousState: previousState, TargetState: targetState, Outcome: cleardev.EventRejected,
		Reason: reason, ReasonText: request.ReasonText, Source: source,
		SourceAOSessionID: request.SourceAOSessionID, CreatedAt: request.At,
	}); err != nil {
		return nil, err
	}
	return &cleardev.RuleError{Code: reason, Message: fmt.Sprintf("ClearDev action %s rejected: %s", action, reason)}, nil
}

func insertClearDevRequirementVersion(ctx context.Context, q *gen.Queries, version cleardev.RequirementVersion) error {
	return q.InsertClearDevRequirementVersion(ctx, gen.InsertClearDevRequirementVersionParams{
		ID: version.ID, DevelopmentProjectID: version.DevelopmentRequirementID,
		Version: version.Version, ContractText: version.RequirementText, Sha256: version.SHA256,
		State: physicalRequirementVersionStatus(version.Status), SupersededByID: nullableString(version.SupersededByID),
		TaskSetVersion: version.TaskSetVersion, CreatedAt: version.CreatedAt, ApprovedAt: nullableTimePtr(version.ConfirmedAt),
	})
}

func insertClearDevDevelopmentTask(ctx context.Context, q *gen.Queries, task cleardev.DevelopmentTask) error {
	return q.InsertClearDevDevelopmentTask(ctx, gen.InsertClearDevDevelopmentTaskParams{
		ID: task.ID, DevelopmentProjectID: task.DevelopmentRequirementID, ContractVersionID: task.RequirementVersionID,
		Title: task.Title, Mode: string(task.Mode), State: string(task.Status),
		PausedFromState: nullableString(string(task.PausedFromStatus)), MaxReworkCount: int64(task.MaxReworkCount),
		ReworkCount: int64(task.ReworkCount), CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
	})
}

func insertClearDevPermission(ctx context.Context, q *gen.Queries, permission cleardev.PermissionVersion) error {
	writePaths, err := json.Marshal(nonNilStrings(permission.Rules.WritePaths))
	if err != nil {
		return err
	}
	forbiddenPaths, err := json.Marshal(nonNilStrings(permission.Rules.ForbiddenPaths))
	if err != nil {
		return err
	}
	sharedPaths, err := json.Marshal(nonNilStrings(permission.Rules.SharedPathsRequireApproval))
	if err != nil {
		return err
	}
	generatedPaths, err := json.Marshal(nonNilStrings(permission.Rules.GeneratedPaths))
	if err != nil {
		return err
	}
	return q.InsertClearDevPermissionVersion(ctx, gen.InsertClearDevPermissionVersionParams{
		ID: permission.ID, WorkItemID: permission.DevelopmentTaskID, Version: permission.Version,
		WritePaths: string(writePaths), ForbiddenPaths: string(forbiddenPaths),
		SharedPathsRequireApproval: string(sharedPaths), GeneratedPaths: string(generatedPaths), CreatedAt: permission.CreatedAt,
	})
}

func insertClearDevEvent(ctx context.Context, q *gen.Queries, event cleardev.RequirementEvent) error {
	return q.InsertClearDevRequirementEvent(ctx, gen.InsertClearDevRequirementEventParams{
		AoProjectID: event.AOProjectID, DevelopmentProjectID: event.DevelopmentRequirementID,
		SubjectType: string(event.SubjectType), SubjectID: event.SubjectID, Action: string(event.Action),
		PreviousState: nullableString(event.PreviousState), TargetState: nullableString(event.TargetState),
		Outcome: string(event.Outcome), ReasonCode: string(event.Reason), ReasonText: event.ReasonText,
		Source: string(event.Source), SourceAoSessionID: nullableString(event.SourceAOSessionID), CreatedAt: event.CreatedAt,
	})
}

func clearDevRequirementFromGen(row gen.CleardevDevelopmentProject) cleardev.DevelopmentRequirement {
	requirement := cleardev.DevelopmentRequirement{
		ID: row.ID, AOProjectID: row.AoProjectID, Name: row.Name,
		CancelReason: cleardev.ReasonCode(row.CancelReasonCode), CancelReasonText: row.CancelReasonText,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.CancelledAt.Valid {
		cancelledAt := row.CancelledAt.Time
		requirement.CancelledAt = &cancelledAt
	}
	return requirement
}

func clearDevRequirementVersionFromGen(row gen.CleardevContractVersion) cleardev.RequirementVersion {
	version := cleardev.RequirementVersion{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, Version: row.Version,
		RequirementText: row.ContractText, SHA256: row.Sha256, Status: publicRequirementVersionStatus(row.State),
		SupersededByID: row.SupersededByID.String, TaskSetVersion: row.TaskSetVersion, CreatedAt: row.CreatedAt,
	}
	if row.ApprovedAt.Valid {
		confirmedAt := row.ApprovedAt.Time
		version.ConfirmedAt = &confirmedAt
	}
	return version
}

func clearDevDevelopmentTaskFromGen(row gen.CleardevWorkItem) cleardev.DevelopmentTask {
	return cleardev.DevelopmentTask{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, RequirementVersionID: row.ContractVersionID,
		Title: row.Title, Mode: cleardev.WorkMode(row.Mode), Status: publicDevelopmentTaskStatus(row.State),
		PausedFromStatus: publicDevelopmentTaskStatus(row.PausedFromState.String),
		MaxReworkCount:   int(row.MaxReworkCount), ReworkCount: int(row.ReworkCount), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

// The S01 reads intentionally select their original columns.  After 0108 adds
// optional S02 binding columns, sqlc correctly represents those projections as
// dedicated row types rather than the wider table model.
func clearDevDevelopmentTaskFromListGen(row gen.ListClearDevDevelopmentTasksRow) cleardev.DevelopmentTask {
	return clearDevDevelopmentTaskFromGen(gen.CleardevWorkItem{
		ID: row.ID, DevelopmentProjectID: row.DevelopmentProjectID, ContractVersionID: row.ContractVersionID,
		Title: row.Title, Mode: row.Mode, State: row.State, PausedFromState: row.PausedFromState,
		MaxReworkCount: row.MaxReworkCount, ReworkCount: row.ReworkCount, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func clearDevPermissionFromGen(row gen.CleardevPathPermissionVersion) (cleardev.PermissionVersion, error) {
	permission := cleardev.PermissionVersion{
		ID: row.ID, DevelopmentTaskID: row.WorkItemID, Version: row.Version, CreatedAt: row.CreatedAt,
	}
	fields := []struct {
		encoded string
		target  *[]string
	}{
		{row.WritePaths, &permission.Rules.WritePaths},
		{row.GeneratedPaths, &permission.Rules.GeneratedPaths},
		{row.SharedPathsRequireApproval, &permission.Rules.SharedPathsRequireApproval},
		{row.ForbiddenPaths, &permission.Rules.ForbiddenPaths},
	}
	for _, field := range fields {
		if err := json.Unmarshal([]byte(field.encoded), field.target); err != nil {
			return cleardev.PermissionVersion{}, fmt.Errorf("decode permission version %s: %w", row.ID, err)
		}
	}
	return permission, nil
}

func clearDevPermissionFromListGen(row gen.ListClearDevPermissionVersionsRow) (cleardev.PermissionVersion, error) {
	return clearDevPermissionFromGen(gen.CleardevPathPermissionVersion{
		ID: row.ID, WorkItemID: row.WorkItemID, Version: row.Version,
		WritePaths: row.WritePaths, ForbiddenPaths: row.ForbiddenPaths,
		SharedPathsRequireApproval: row.SharedPathsRequireApproval,
		GeneratedPaths:             row.GeneratedPaths, CreatedAt: row.CreatedAt,
	})
}

func clearDevCheckFromGen(row gen.CleardevRequiredCheck) cleardev.RequiredCheck {
	return cleardev.RequiredCheck{
		ID: row.ID, DevelopmentTaskID: row.WorkItemID, Name: row.Name, Kind: row.CheckKind, CreatedAt: row.CreatedAt,
	}
}

func clearDevCheckFromListGen(row gen.ListClearDevRequiredChecksRow) cleardev.RequiredCheck {
	return clearDevCheckFromGen(gen.CleardevRequiredCheck{
		ID: row.ID, WorkItemID: row.WorkItemID, Name: row.Name, CheckKind: row.CheckKind, CreatedAt: row.CreatedAt,
	})
}

func clearDevCandidateFromGen(row gen.CleardevCandidateCommit) cleardev.CandidateCommit {
	return cleardev.CandidateCommit{
		ID: row.ID, DevelopmentTaskID: row.WorkItemID, Sequence: row.Sequence, AOSessionID: row.AoSessionID,
		PermissionVersionID: row.PermissionVersionID, CommitSHA: row.CommitSha, CreatedAt: row.CreatedAt,
	}
}

func clearDevCandidateFromListGen(row gen.ListClearDevCandidateCommitsRow) cleardev.CandidateCommit {
	return clearDevCandidateFromGen(gen.CleardevCandidateCommit{
		ID: row.ID, WorkItemID: row.WorkItemID, Sequence: row.Sequence, AoSessionID: row.AoSessionID,
		PermissionVersionID: row.PermissionVersionID, CommitSha: row.CommitSha, CreatedAt: row.CreatedAt,
	})
}

func clearDevIntegrationCandidateFromGen(row gen.CleardevIntegrationCandidate) cleardev.IntegrationCandidate {
	candidate := cleardev.IntegrationCandidate{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID,
		RequirementVersionID: row.RequirementVersionID.String, Sequence: row.Sequence,
		AOSessionID: row.AoSessionID, CommitSHA: row.CommitSha, CreatedAt: row.CreatedAt,
	}
	if row.TaskSetVersion.Valid {
		taskSetVersion := row.TaskSetVersion.Int64
		candidate.TaskSetVersion = &taskSetVersion
	}
	return candidate
}

func clearDevEvidenceFromGen(row gen.CleardevEvidence) cleardev.EvidenceRecord {
	record := cleardev.EvidenceRecord{
		Sequence: row.Sequence, ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID,
		SubjectType: publicSubjectType(row.SubjectType), SubjectID: row.SubjectID,
		Kind: cleardev.EvidenceKind(row.EvidenceKind), Key: row.EvidenceKey, Result: cleardev.EvidenceResult(row.Result),
		CandidateCommitID: row.CandidateCommitID.String, IntegrationCandidateID: row.IntegrationCandidateID.String,
		CommitSHA: row.CommitSha, Source: cleardev.EvidenceSource(row.SourceType),
		SourceAOSessionID: row.SourceAoSessionID.String, CreatedAt: row.CreatedAt,
	}
	if row.ExpiresAt.Valid {
		expiresAt := row.ExpiresAt.Time
		record.ExpiresAt = &expiresAt
	}
	return record
}

func clearDevEvidenceFromListGen(row gen.ListClearDevEvidenceRow) cleardev.EvidenceRecord {
	return clearDevEvidenceFromGen(gen.CleardevEvidence{
		Sequence: row.Sequence, ID: row.ID, DevelopmentProjectID: row.DevelopmentProjectID,
		SubjectType: row.SubjectType, SubjectID: row.SubjectID, EvidenceKind: row.EvidenceKind,
		EvidenceKey: row.EvidenceKey, Result: row.Result, CandidateCommitID: row.CandidateCommitID,
		IntegrationCandidateID: row.IntegrationCandidateID, CommitSha: row.CommitSha, SourceType: row.SourceType,
		SourceAoSessionID: row.SourceAoSessionID, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
	})
}

func clearDevEventFromGen(row gen.CleardevProjectEvent) cleardev.RequirementEvent {
	subjectType := publicSubjectType(row.SubjectType)
	action := publicAction(row.Action)
	previousState := publicState(row.PreviousState.String)
	targetState := publicState(row.TargetState.String)
	if row.SubjectType == "DEVELOPMENT_PROJECT" && row.Action != "CANCEL_PROJECT" && row.Action != "MIGRATE_LEGACY_CANCELLATION" {
		// The 0106 implementation-container state machine is historical only.
		previousState = ""
		targetState = ""
	}
	return cleardev.RequirementEvent{
		Sequence: row.Sequence, AOProjectID: row.AoProjectID, DevelopmentRequirementID: row.DevelopmentProjectID,
		SubjectType: subjectType, SubjectID: row.SubjectID, Action: action,
		PreviousState: previousState, TargetState: targetState,
		Outcome: cleardev.EventOutcome(row.Outcome), Reason: cleardev.ReasonCode(row.ReasonCode), ReasonText: row.ReasonText,
		Source: cleardev.EventSource(row.Source), SourceAOSessionID: row.SourceAoSessionID.String, CreatedAt: row.CreatedAt,
	}
}

func publicRequirementVersionStatus(value string) cleardev.RequirementVersionStatus {
	switch value {
	case "IN_REVIEW":
		return cleardev.RequirementVersionStatusPendingConfirmation
	case "APPROVED":
		return cleardev.RequirementVersionStatusConfirmed
	default:
		return cleardev.RequirementVersionStatus(value)
	}
}

func physicalRequirementVersionStatus(value cleardev.RequirementVersionStatus) string {
	switch value {
	case cleardev.RequirementVersionStatusPendingConfirmation:
		return "IN_REVIEW"
	case cleardev.RequirementVersionStatusConfirmed:
		return "APPROVED"
	default:
		return string(value)
	}
}

func publicDevelopmentTaskStatus(value string) cleardev.DevelopmentTaskStatus {
	if value == "READY" {
		return cleardev.DevelopmentTaskStatusPlanned
	}
	return cleardev.DevelopmentTaskStatus(value)
}

func physicalEvidenceSubject(value cleardev.SubjectType) string {
	switch value {
	case cleardev.SubjectDevelopmentTask:
		return "WORK_ITEM"
	case cleardev.SubjectDevelopmentRequirement:
		return "DEVELOPMENT_PROJECT"
	default:
		return string(value)
	}
}

func publicSubjectType(value string) cleardev.SubjectType {
	switch value {
	case "DEVELOPMENT_PROJECT":
		return cleardev.SubjectDevelopmentRequirement
	case "CONTRACT", "CONTRACT_VERSION":
		return cleardev.SubjectRequirementVersion
	case "WORK_ITEM":
		return cleardev.SubjectDevelopmentTask
	default:
		return cleardev.SubjectType(value)
	}
}

func publicState(value string) string {
	switch value {
	case "IN_REVIEW":
		return string(cleardev.RequirementVersionStatusPendingConfirmation)
	case "APPROVED":
		return string(cleardev.RequirementVersionStatusConfirmed)
	case "READY":
		return string(cleardev.DevelopmentTaskStatusPlanned)
	default:
		return value
	}
}

func publicAction(value string) cleardev.Action {
	mapping := map[string]cleardev.Action{
		"CREATE_PROJECT":              cleardev.ActionCreateRequirement,
		"CREATE_CONTRACT":             cleardev.ActionCreateRequirementVersion,
		"SUBMIT_CONTRACT_REVIEW":      cleardev.ActionSubmitRequirementConfirmation,
		"APPROVE_CONTRACT":            cleardev.ActionConfirmRequirementVersion,
		"REJECT_CONTRACT":             cleardev.ActionRejectRequirementVersion,
		"SUPERSEDE_CONTRACT":          cleardev.ActionSupersedeRequirementVersion,
		"CANCEL_PROJECT":              cleardev.ActionCancelRequirement,
		"CREATE_WORK_ITEM":            cleardev.ActionCreateDevelopmentTask,
		"MARK_WORK_ITEM_READY":        cleardev.ActionStartDevelopmentTask,
		"START_WORK_ITEM":             cleardev.ActionStartDevelopmentTask,
		"SUBMIT_WORK_ITEM_REVIEW":     cleardev.ActionSubmitDevelopmentTaskReview,
		"COMPLETE_WORK_ITEM":          cleardev.ActionCompleteDevelopmentTask,
		"REQUEST_WORK_ITEM_REWORK":    cleardev.ActionRequestDevelopmentTaskRework,
		"ESCALATE_WORK_ITEM":          cleardev.ActionEscalateDevelopmentTask,
		"RESTART_WORK_ITEM":           cleardev.ActionRestartDevelopmentTask,
		"PAUSE_WORK_ITEM_NEEDS_HUMAN": cleardev.ActionPauseDevelopmentTaskNeedsHuman,
		"BLOCK_WORK_ITEM":             cleardev.ActionBlockDevelopmentTask,
		"RESUME_WORK_ITEM":            cleardev.ActionResumeDevelopmentTask,
		"CANCEL_WORK_ITEM":            cleardev.ActionCancelDevelopmentTask,
		"BEGIN_CONTRACTING":           cleardev.Action("LEGACY_PROGRESS_EVENT"),
		"BEGIN_PLANNING":              cleardev.Action("LEGACY_PROGRESS_EVENT"),
		"MARK_PROJECT_READY":          cleardev.Action("LEGACY_PROGRESS_EVENT"),
		"START_PROJECT":               cleardev.Action("LEGACY_PROGRESS_EVENT"),
		"BEGIN_INTEGRATION":           cleardev.Action("LEGACY_PROGRESS_EVENT"),
		"COMPLETE_PROJECT":            cleardev.Action("LEGACY_PROGRESS_EVENT"),
		"PAUSE_PROJECT_NEEDS_HUMAN":   cleardev.Action("LEGACY_PROGRESS_EVENT"),
		"BLOCK_PROJECT":               cleardev.Action("LEGACY_PROGRESS_EVENT"),
		"RESUME_PROJECT":              cleardev.Action("LEGACY_PROGRESS_EVENT"),
	}
	if action, ok := mapping[value]; ok {
		return action
	}
	return cleardev.Action(value)
}

func normalizeSnapshotSlices(snapshot *cleardev.RequirementSnapshot) {
	if snapshot.RequirementVersions == nil {
		snapshot.RequirementVersions = []cleardev.RequirementVersion{}
	}
	if snapshot.DevelopmentTasks == nil {
		snapshot.DevelopmentTasks = []cleardev.DevelopmentTask{}
	}
	if snapshot.PermissionVersions == nil {
		snapshot.PermissionVersions = []cleardev.PermissionVersion{}
	}
	if snapshot.RequiredChecks == nil {
		snapshot.RequiredChecks = []cleardev.RequiredCheck{}
	}
	if snapshot.Candidates == nil {
		snapshot.Candidates = []cleardev.CandidateCommit{}
	}
	if snapshot.IntegrationCandidates == nil {
		snapshot.IntegrationCandidates = []cleardev.IntegrationCandidate{}
	}
	if snapshot.Evidence == nil {
		snapshot.Evidence = []cleardev.EvidenceRecord{}
	}
	if snapshot.Events == nil {
		snapshot.Events = []cleardev.RequirementEvent{}
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nullableTimePtr(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *value, Valid: true}
}

func nullableInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func nullableInt64Ptr(value *int64) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return nullableInt64(*value)
}

func validSHA256Digest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func requirementDigestMatches(text, digest string) bool {
	if !validSHA256Digest(digest) {
		return false
	}
	sum := sha256.Sum256([]byte(text))
	return digest == hex.EncodeToString(sum[:])
}

func eventSourceForEvidence(source cleardev.EvidenceSource) cleardev.EventSource {
	if source == cleardev.EvidenceSourceReviewAdapter {
		return cleardev.EventSourceReviewAdapter
	}
	return cleardev.EventSourceChecker
}
