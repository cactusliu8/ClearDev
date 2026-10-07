package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func complexExecutionRule(message string) error {
	return &cleardev.RuleError{Code: cleardev.ReasonPreconditionNotMet, Message: message}
}

// REQUESTED Steward bindings may omit the dispatch step until preflight passes
// and a session is bound. A provided step must still be a pending dispatch.
func requireOptionalDispatchStep(binding cleardev.ComplexExecutionRoleBinding, step cleardev.AgentStep, runID string) error {
	if step.ID == "" {
		if binding.Status != cleardev.RoleBindingStatusRequested {
			return complexExecutionRule("invalid initial Steward binding or step")
		}
		return nil
	}
	if step.RoleBindingID != binding.ID || step.Kind != cleardev.AgentStepDispatchRequest || step.RequestID != runID ||
		step.SendStatus != cleardev.AgentStepSendStatusPending {
		return complexExecutionRule("invalid initial Steward binding or step")
	}
	return nil
}

func nullableTime(value time.Time) sql.NullTime { return sql.NullTime{Time: value, Valid: true} }

func nullableComplexExecutionInt(value *int) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*value), Valid: true}
}

func complexExecutionSHA1(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func complexExecutionDigest(argv []string) string {
	raw, _ := json.Marshal(argv)
	return complexExecutionRawDigest(raw)
}

func complexExecutionRawDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}

func complexExecutionScopeDigest(task cleardev.ComplexPlanTask) string {
	raw, _ := json.Marshal(struct {
		WritePaths                 []string `json:"writePaths"`
		GeneratedPaths             []string `json:"generatedPaths"`
		SharedPathsRequireApproval []string `json:"sharedPathsRequireApproval"`
		ForbiddenPaths             []string `json:"forbiddenPaths"`
	}{
		WritePaths:                 task.WritePaths,
		GeneratedPaths:             task.GeneratedPaths,
		SharedPathsRequireApproval: task.SharedPathsRequireApproval,
		ForbiddenPaths:             task.ForbiddenPaths,
	})
	return complexExecutionRawDigest(raw)
}

func validComplexExecutionRun(r cleardev.ComplexExecutionRun) error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.DevelopmentRequirementID) == "" || strings.TrimSpace(r.RequirementVersionID) == "" ||
		len(r.RequirementSHA256) != 64 || len(r.PlanSHA256) != 64 || len(r.ExecutionPackageSHA256) != 64 || r.ExecutionPackageJSON == "" ||
		r.ExpectedTaskSetVersion != 0 || r.TaskSetVersion != cleardev.ComplexStandardTaskSetVersion ||
		strings.TrimSpace(r.ModeReason) == "" {
		return complexExecutionRule("invalid complex execution run")
	}
	switch r.Mode {
	case cleardev.WorkModeStandard:
		if r.FixedBuilderCount != 1 {
			return complexExecutionRule("invalid complex execution run")
		}
	case cleardev.WorkModeParallel:
		if r.FixedBuilderCount < cleardev.ComplexParallelMinBuilders || r.FixedBuilderCount > cleardev.ComplexParallelMaxBuilders {
			return complexExecutionRule("invalid complex execution run")
		}
	default:
		return complexExecutionRule("invalid complex execution run")
	}
	return nil
}

func benchmarkExecutionPolicyAndCatalog(ctx context.Context, q *gen.Queries, requirementID string) (cleardev.BenchmarkModePolicy, []cleardev.ComplexCheckSpec, error) {
	row, err := q.GetClearDevBenchmarkBinding(ctx, requirementID)
	if errors.Is(err, sql.ErrNoRows) {
		if catalog, project, err := projectExecutionCatalog(ctx, q, requirementID); err != nil {
			return "", nil, err
		} else if project {
			return "", catalog, nil
		}
		return "", cleardev.FrozenComplexCheckCatalog(), nil
	}
	if err != nil {
		return "", nil, err
	}
	policy := cleardev.BenchmarkModePolicy(row.ModePolicy)
	if (row.GroupID == cleardev.BenchmarkGroupG3 && policy != cleardev.BenchmarkModeStandardOnly) ||
		(row.GroupID == cleardev.BenchmarkGroupG4 && policy != cleardev.BenchmarkModeDynamic) {
		return "", nil, complexExecutionRule("benchmark binding group/policy is invalid")
	}
	catalog, catalogErr := cleardev.BenchmarkComplexCheckCatalog(row.CheckPrefix)
	if catalogErr != nil || row.CheckProfile != cleardev.BenchmarkCheckProfileNodeTS || row.CheckProfileSha256 != cleardev.BenchmarkCheckProfileSHA256(row.CheckPrefix) {
		return "", nil, complexExecutionRule("benchmark check profile is invalid")
	}
	return policy, catalog, nil
}

func validateComplexExecutionRunPolicy(ctx context.Context, q *gen.Queries, run cleardev.ComplexExecutionRun) error {
	policy, _, err := benchmarkExecutionPolicyAndCatalog(ctx, q, run.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	planRow, err := q.GetApprovedClearDevComplexPlanForVersion(ctx, run.RequirementVersionID)
	if err != nil {
		return err
	}
	if planRow.ID != run.PlanID || planRow.PlanSha256 != run.PlanSHA256 || planRow.RequirementSha256 != run.RequirementSHA256 {
		return complexExecutionRule("run does not match approved complex plan")
	}
	if err := validatePlannerContractRun(ctx, q, run, planRow); err != nil {
		return err
	}
	var plan cleardev.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(planRow.PlanJson), &plan); err != nil {
		return complexExecutionRule("approved complex plan is invalid")
	}
	if _, mailPolicy, err := cleardev.MailPolicyFromRun(run); err != nil {
		return err
	} else if mailPolicy {
		if err := cleardev.ValidateMailPlan(plan); err != nil {
			return err
		}
	}
	var selection cleardev.ComplexModeSelection
	if _, project, projectErr := cleardev.ProjectContractFromRun(run); projectErr != nil {
		return projectErr
	} else if project {
		selection = cleardev.ProjectExecutionMode(plan)
	} else {
		selection, err = cleardev.SelectComplexExecutionModeForPolicy(plan.Tasks, plan.ParallelSuggestion.RecommendedBuilderCount, policy)
	}
	if err != nil || selection.Mode != run.Mode || selection.BuilderCount != run.FixedBuilderCount || string(selection.ReasonCode) != run.ModeReason {
		return complexExecutionRule("complex execution run violates the persisted mode policy")
	}
	return nil
}

func complexExecutionRunParams(r cleardev.ComplexExecutionRun) gen.InsertClearDevComplexExecutionRunParams {
	return gen.InsertClearDevComplexExecutionRunParams{
		ID: r.ID, DevelopmentProjectID: r.DevelopmentRequirementID, RequirementVersionID: r.RequirementVersionID, RequirementSha256: r.RequirementSHA256,
		PlanID: r.PlanID, PlanReviewID: nullableString(r.PlanReviewID), PlanSha256: r.PlanSHA256, StewardRoleBindingID: r.StewardRoleBindingID, Mode: string(r.Mode),
		SelectionReasonCode: r.ModeReason, FixedBuilderCount: int64(r.FixedBuilderCount), ExpectedTaskSetVersion: r.ExpectedTaskSetVersion, AcceptedTaskSetVersion: r.TaskSetVersion,
		ExecutionPackageJson: r.ExecutionPackageJSON, ExecutionPackageSha256: r.ExecutionPackageSHA256, Status: "PENDING", RequestedAt: r.CreatedAt,
	}
}

func complexExecutionRoleBindingParams(b cleardev.ComplexExecutionRoleBinding) gen.InsertClearDevComplexExecutionRoleBindingParams {
	return gen.InsertClearDevComplexExecutionRoleBindingParams{
		ID: b.ID, ExecutionRunID: b.ExecutionRunID, Role: string(b.Role), SourceComplexRoleBindingID: nullableString(b.SourceComplexRoleBindingID), ContinuationOfRoleBindingID: nullableString(b.ContinuationOfRoleBindingID),
		BuilderSlot:   sql.NullInt64{Int64: int64(b.BuilderSlot), Valid: b.BuilderSlot > 0},
		TaskMappingID: nullableString(b.TaskMappingID), CandidateCommitID: nullableString(b.CandidateCommitID), SessionCreationIdempotencyKey: b.SessionCreationIdempotencyKey, AoSessionID: nullableString(b.AOSessionID), WorkspacePath: b.WorkspacePath, BaseCommitSha: b.BaseCommitSHA,
		Status: string(b.Status), ReasonCode: string(b.ReasonCode), RequestedAt: b.RequestedAt, BoundAt: nullableTimePtr(b.BoundAt), EndedAt: nullableTimePtr(b.EndedAt),
	}
}

func complexExecutionAgentStepParams(step cleardev.AgentStep) gen.InsertClearDevComplexExecutionAgentStepParams {
	return gen.InsertClearDevComplexExecutionAgentStepParams{
		ID: step.ID, RoleBindingID: step.RoleBindingID, StepKind: string(step.Kind), RequestID: step.RequestID, ClientMessageID: step.ClientMessageID, PromptSha256: step.PromptSHA256, SendStatus: string(step.SendStatus),
		TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID), FinalMessageText: nullableString(step.FinalMessageText), MessageSha256: nullableString(step.MessageSHA256), RequestedAt: step.RequestedAt, SentAt: nullableTimePtr(step.SentAt), CompletedAt: nullableTimePtr(step.CompletedAt), FailedAt: nullableTimePtr(step.FailedAt), ReasonCode: string(step.ReasonCode),
	}
}

func complexExecutionRunFromGen(row gen.CleardevComplexExecutionRun) cleardev.ComplexExecutionRun {
	r := cleardev.ComplexExecutionRun{ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, RequirementVersionID: row.RequirementVersionID, RequirementSHA256: row.RequirementSha256, PlanID: row.PlanID, PlanReviewID: row.PlanReviewID.String, PlanSHA256: row.PlanSha256, Mode: cleardev.WorkMode(row.Mode), ModeReason: row.SelectionReasonCode, FixedBuilderCount: int(row.FixedBuilderCount), ExpectedTaskSetVersion: row.ExpectedTaskSetVersion, TaskSetVersion: row.AcceptedTaskSetVersion, ExecutionPackageJSON: row.ExecutionPackageJson, ExecutionPackageSHA256: row.ExecutionPackageSha256, StewardRoleBindingID: row.StewardRoleBindingID, ReasonCode: cleardev.ReasonCode(row.ReasonCode), CreatedAt: row.RequestedAt}
	if row.Status == "ACCEPTED" || row.Status == "COMPLETED" {
		r.Decision = cleardev.ComplexExecutionDecisionDispatch
	}
	if row.Status == "NEEDS_HUMAN" {
		r.Decision = cleardev.ComplexExecutionDecisionNeedsHuman
	}
	if row.SettledAt.Valid {
		v := row.SettledAt.Time
		r.CompletedAt = &v
	}
	return r
}
func complexExecutionRoleBindingFromGen(row gen.CleardevComplexExecutionRoleBinding) cleardev.ComplexExecutionRoleBinding {
	b := cleardev.ComplexExecutionRoleBinding{ID: row.ID, ExecutionRunID: row.ExecutionRunID, SourceComplexRoleBindingID: row.SourceComplexRoleBindingID.String, ContinuationOfRoleBindingID: row.ContinuationOfRoleBindingID.String, Role: cleardev.StandardRole(row.Role), TaskMappingID: row.TaskMappingID.String, CandidateCommitID: row.CandidateCommitID.String, SessionCreationIdempotencyKey: row.SessionCreationIdempotencyKey, AOSessionID: row.AoSessionID.String, WorkspacePath: row.WorkspacePath, BaseCommitSHA: row.BaseCommitSha, Status: cleardev.RoleBindingStatus(row.Status), ReasonCode: cleardev.ReasonCode(row.ReasonCode), RequestedAt: row.RequestedAt}
	if row.BuilderSlot.Valid {
		b.BuilderSlot = int(row.BuilderSlot.Int64)
	}
	if row.BoundAt.Valid {
		v := row.BoundAt.Time
		b.BoundAt = &v
	}
	if row.EndedAt.Valid {
		v := row.EndedAt.Time
		b.EndedAt = &v
	}
	return b
}

func complexExecutionAgentStepTimes(sentAt, completedAt, failedAt sql.NullTime) (sent, completed, failed *time.Time) {
	if sentAt.Valid {
		sentTime := sentAt.Time
		sent = &sentTime
	}
	if completedAt.Valid {
		completedTime := completedAt.Time
		completed = &completedTime
	}
	if failedAt.Valid {
		failedTime := failedAt.Time
		failed = &failedTime
	}
	return sent, completed, failed
}

func complexExecutionAgentStepFromGen(row gen.CleardevComplexExecutionAgentStep) cleardev.AgentStep {
	sent, completed, failed := complexExecutionAgentStepTimes(row.SentAt, row.CompletedAt, row.FailedAt)
	return cleardev.AgentStep{ID: row.ID, RoleBindingID: row.RoleBindingID, Kind: cleardev.AgentStepKind(row.StepKind), RequestID: row.RequestID, ClientMessageID: row.ClientMessageID, PromptSHA256: row.PromptSha256, SendStatus: cleardev.AgentStepSendStatus(row.SendStatus), TurnID: row.TurnID.String, FinalMessageID: row.FinalMessageID.String, FinalMessageText: row.FinalMessageText.String, MessageSHA256: row.MessageSha256.String, RequestedAt: row.RequestedAt, SentAt: sent, CompletedAt: completed, FailedAt: failed, ReasonCode: cleardev.ReasonCode(row.ReasonCode)}
}
func complexExecutionDispatchFromGen(row gen.CleardevComplexExecutionTaskAttempt) cleardev.ComplexExecutionDispatch {
	d := cleardev.ComplexExecutionDispatch{ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.TaskMappingID, Round: int(row.Round), BaseCommitSHA: row.BaseCommitSha, AgentStepID: row.AgentStepID, Status: cleardev.ComplexExecutionDispatchStatus(row.Status), ReasonCode: cleardev.ReasonCode(row.ReasonCode), BatchID: row.BatchID.String, BuilderRoleBindingID: row.BuilderRoleBindingID}
	if row.DispatchedAt.Valid {
		d.CreatedAt = row.DispatchedAt.Time
	}
	if row.SettledAt.Valid {
		v := row.SettledAt.Time
		d.SettledAt = &v
	}
	return d
}
func complexExecutionCheckRunFromGen(row gen.CleardevComplexExecutionCheckRun) cleardev.ComplexExecutionCheckRun {
	r := cleardev.ComplexExecutionCheckRun{ID: row.ID, DispatchID: row.TaskAttemptID, CandidateCommitID: row.CandidateCommitID, CandidateCommitSHA: row.CandidateCommitSha, CheckSpecFactID: row.CheckSpecID, Status: cleardev.ComplexExecutionCheckRunStatus(row.Status), Result: cleardev.EvidenceResult(row.Result.String), ReasonCode: cleardev.ReasonCode(row.ReasonCode), ContainerImageID: row.ContainerImageID.String, TimedOut: row.TimedOut.Bool, OutputSummary: row.OutputSummary.String, OutputSHA256: row.OutputSha256.String, ChangedPathsJSON: row.ChangedPathsJson.String, RetryOrdinal: int(row.RetryOrdinal), CreatedAt: row.CreatedAt}
	if row.ExitCode.Valid {
		v := int(row.ExitCode.Int64)
		r.ExitCode = &v
	}
	if row.StartedAt.Valid {
		v := row.StartedAt.Time
		r.StartedAt = &v
	}
	if row.SettledAt.Valid {
		v := row.SettledAt.Time
		r.SettledAt = &v
	}
	return r
}
func complexExecutionReviewFromGen(row gen.CleardevComplexExecutionReview) cleardev.ComplexExecutionReview {
	r := cleardev.ComplexExecutionReview{ID: row.ID, DispatchID: row.TaskAttemptID, CandidateCommitID: row.CandidateCommitID, ReviewerRoleBindingID: row.ReviewerRoleBindingID, AgentStepID: row.AgentStepID, ReviewPacketJSON: row.ReviewPacketJson, ReviewPacketSHA256: row.ReviewPacketSha256, CandidateWorkspacePath: row.CandidateWorktreePath, Status: cleardev.LocalReviewStatus(row.Status), Verdict: cleardev.LocalReviewVerdict(row.Verdict.String), ReasonCode: cleardev.ReasonCode(row.ReasonCode), Summary: row.Summary.String, CreatedAt: row.CreatedAt}
	if row.SettledAt.Valid {
		v := row.SettledAt.Time
		r.SettledAt = &v
	}
	return r
}

func byID(tasks []cleardev.ComplexExecutionTask, id string) cleardev.ComplexExecutionTask {
	for _, t := range tasks {
		if t.ID == id {
			return t
		}
	}
	return cleardev.ComplexExecutionTask{}
}
func taskKeyForID(tasks []cleardev.ComplexExecutionTask, id string) string {
	return byID(tasks, id).TaskKey
}
func complexExecutionBoundBuilder(ctx context.Context, q *gen.Queries, runID string) (cleardev.ComplexExecutionRoleBinding, error) {
	rows, e := q.ListClearDevComplexExecutionRoleBindings(ctx, runID)
	if e != nil {
		return cleardev.ComplexExecutionRoleBinding{}, e
	}
	for _, r := range rows {
		b := complexExecutionRoleBindingFromGen(r)
		if b.Role == cleardev.StandardRoleBuilder && b.Status == cleardev.RoleBindingStatusBound {
			return b, nil
		}
	}
	return cleardev.ComplexExecutionRoleBinding{}, complexExecutionRule("bound Builder missing")
}

// ListClearDevRunnableComplexExecutions returns requirement IDs, never AO IDs.
func (s *Store) ListClearDevRunnableComplexExecutions(ctx context.Context) ([]string, error) {
	return s.qr.ListClearDevRunnableComplexExecutions(ctx)
}

// GetClearDevComplexExecution rebuilds the S06 scheduler input from immutable
// facts. It deliberately does not save or return a second display state.
func (s *Store) GetClearDevComplexExecution(ctx context.Context, requirementID string) (cleardev.ComplexExecutionSnapshot, bool, error) {
	runs, err := s.qr.ListClearDevComplexExecutionRuns(ctx, requirementID)
	if err != nil {
		return cleardev.ComplexExecutionSnapshot{}, false, err
	}
	if len(runs) == 0 {
		return cleardev.ComplexExecutionSnapshot{}, false, nil
	}
	run := runs[len(runs)-1]
	snapshot := cleardev.ComplexExecutionSnapshot{Run: complexExecutionRunFromGen(run), RoleBindings: []cleardev.ComplexExecutionRoleBinding{}, AgentSteps: []cleardev.AgentStep{}, Tasks: []cleardev.ComplexExecutionTask{}, Dispatches: []cleardev.ComplexExecutionDispatch{}, CheckSpecs: []cleardev.ComplexExecutionCheckSpecFact{}, CheckRuns: []cleardev.ComplexExecutionCheckRun{}, Reviews: []cleardev.ComplexExecutionReview{}, Verifications: []cleardev.ComplexExecutionVerification{}, Batches: []cleardev.ComplexExecutionBatch{}, Compositions: []cleardev.ComplexExecutionComposition{}}
	recoveries, recoveryErr := s.qr.ListClearDevWorkflowRecoveries(ctx, run.ID)
	if recoveryErr != nil {
		return snapshot, false, recoveryErr
	}
	snapshot.WorkflowRecoveries = []cleardev.WorkflowRecovery{}
	for _, r := range recoveries {
		snapshot.WorkflowRecoveries = append(snapshot.WorkflowRecoveries, workflowRecoveryFromRow(r))
	}
	finalReviewRequired, policyErr := cleardev.RequirementFinalReviewRequired(snapshot.Run)
	if policyErr != nil {
		return snapshot, false, policyErr
	}
	// Legacy runs remain readable after a no-history compatibility downgrade.
	// Only a run that froze the new contract may depend on its new fact table.
	if finalReviewRequired {
		if row, err := s.qr.GetClearDevRequirementFinalReview(ctx, run.ID); err == nil {
			review, decodeErr := requirementFinalReviewFromGen(row)
			if decodeErr != nil {
				return snapshot, false, decodeErr
			}
			if review.Status == "SETTLED" && cleardev.BuilderFirstFailureEnabled(snapshot.Run) && review.Verdict != "PASS" {
				result, resultErr := s.qr.GetClearDevFinalReviewSavedResult(ctx, review.ID)
				if resultErr != nil {
					return snapshot, false, resultErr
				}
				review.FailureResultJSON = result.RawMessageText
			}
			snapshot.FinalReview = &review
		} else if !errors.Is(err, sql.ErrNoRows) {
			return snapshot, false, err
		}
		// The stage final review's send-back cap counts settled REWORK
		// verdicts directly in SQL; history rows never block this read.
		count, countErr := s.qr.CountClearDevRequirementFinalReviewReworks(ctx, run.ID)
		if countErr != nil {
			return snapshot, false, countErr
		}
		snapshot.FinalReviewReworkCount = int(count)
	}
	bindings, err := s.qr.ListClearDevComplexExecutionRoleBindings(ctx, run.ID)
	if err != nil {
		return snapshot, false, err
	}
	for _, row := range bindings {
		b := complexExecutionRoleBindingFromGen(row)
		snapshot.RoleBindings = append(snapshot.RoleBindings, b)
		if b.Role == cleardev.StandardRoleBuilder && b.Status == cleardev.RoleBindingStatusBound {
			if snapshot.Run.Mode == cleardev.WorkModeParallel {
				// The slot-1 Builder keeps the run-level compatibility fields.
				if b.BuilderSlot == 1 {
					snapshot.Run.BuilderRoleBindingID = b.ID
					snapshot.Run.BuilderAOSessionID = b.AOSessionID
					if snapshot.Run.InitialBaseCommitSHA == "" {
						snapshot.Run.InitialBaseCommitSHA = b.BaseCommitSHA
					}
				}
				continue
			}
			snapshot.Run.BuilderRoleBindingID = b.ID
			snapshot.Run.BuilderAOSessionID = b.AOSessionID
			snapshot.Run.InitialBaseCommitSHA = b.BaseCommitSHA
		}
	}
	snapshot.BuilderReplacementTransitions, err = builderReplacementTransitions(ctx, s.qr, snapshot.Run)
	if err != nil {
		return snapshot, false, err
	}
	if snapshot.Run.Mode == cleardev.WorkModeParallel {
		batches, e := s.qr.ListClearDevComplexExecutionBatches(ctx, run.ID)
		if e != nil {
			return snapshot, false, e
		}
		for _, row := range batches {
			batch, batchErr := complexExecutionBatchFromGen(row)
			if batchErr != nil {
				return snapshot, false, batchErr
			}
			snapshot.Batches = append(snapshot.Batches, batch)
			if row.Ordinal == 0 && row.CommonBaseSha != "" {
				snapshot.Run.InitialBaseCommitSHA = row.CommonBaseSha
			}
		}
		compositions, e := s.qr.ListClearDevComplexExecutionCompositions(ctx, run.ID)
		if e != nil {
			return snapshot, false, e
		}
		for _, row := range compositions {
			composition, compositionErr := complexExecutionCompositionFromGen(row)
			if compositionErr != nil {
				return snapshot, false, compositionErr
			}
			snapshot.Compositions = append(snapshot.Compositions, composition)
		}
	}
	steps, err := s.qr.ListClearDevComplexExecutionAgentSteps(ctx, run.ID)
	if err != nil {
		return snapshot, false, err
	}
	for _, row := range steps {
		snapshot.AgentSteps = append(snapshot.AgentSteps, complexExecutionAgentStepFromGen(row))
	}
	tasks, err := s.qr.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return snapshot, false, err
	}
	taskByID := map[string]int{}
	for _, row := range tasks {
		item, e := s.qr.GetClearDevComplexExecutionTaskByWorkItem(ctx, row.ID)
		if e != nil {
			return snapshot, false, e
		}
		task := cleardev.ComplexExecutionTask{ID: row.ID, ExecutionRunID: row.ExecutionRunID, TaskKey: row.PlanTaskKey, DevelopmentTaskID: row.WorkItemID, Ordinal: int(row.Ordinal), ExecutionPackageJSON: row.TaskPacketJson, ExecutionPackageSHA256: row.TaskPacketSha256, Status: cleardev.DevelopmentTaskStatus(item.State), ReworkCount: int(item.ReworkCount), CurrentRound: 0}
		snapshot.Tasks = append(snapshot.Tasks, task)
		taskByID[row.ID] = len(snapshot.Tasks) - 1
	}
	snapshot.PlannerRuntime, err = loadPlannerRuntime(ctx, s.qr, snapshot.Run)
	if err != nil {
		return snapshot, false, err
	}
	if err := overlayPlannerRuntimePackages(&snapshot); err != nil {
		return snapshot, false, err
	}
	deps, err := s.qr.ListClearDevComplexExecutionDependencies(ctx, run.ID)
	if err != nil {
		return snapshot, false, err
	}
	for _, d := range deps {
		if taskIndex, ok := taskByID[d.TaskMappingID]; ok {
			if dependencyIndex, dependencyOK := taskByID[d.DependsOnMappingID]; dependencyOK {
				snapshot.Tasks[taskIndex].DependencyTaskKeys = append(snapshot.Tasks[taskIndex].DependencyTaskKeys, snapshot.Tasks[dependencyIndex].TaskKey)
			}
		}
	}
	attempts, err := s.qr.ListClearDevComplexExecutionTaskAttempts(ctx, run.ID)
	if err != nil {
		return snapshot, false, err
	}
	for _, row := range attempts {
		d := complexExecutionDispatchFromGen(row)
		if taskIndex, ok := taskByID[row.TaskMappingID]; ok {
			task := snapshot.Tasks[taskIndex]
			d.DevelopmentTaskID = task.DevelopmentTaskID
			d.ExecutionPackageSHA256 = task.ExecutionPackageSHA256
			if snapshot.PlannerRuntime != nil {
				for i := len(snapshot.PlannerRuntime.Amendments) - 1; i >= 0; i-- {
					a := snapshot.PlannerRuntime.Amendments[i]
					if a.TaskMappingID != task.ID {
						continue
					}
					var packet cleardev.ComplexStandardExecutionPackage
					if json.Unmarshal([]byte(a.ExecutionPackageJSON), &packet) != nil {
						return snapshot, false, complexExecutionRule("invalid historical revision")
					}
					if packet.RuntimeRevision != nil && int(row.Round) < packet.RuntimeRevision.FirstRound {
						d.ExecutionPackageSHA256 = a.PreviousPackageSHA256
					}
				}
			}
		}
		for _, step := range snapshot.AgentSteps {
			if step.ID == row.AgentStepID {
				d.ClientMessageID = step.ClientMessageID
				break
			}
		}
		candidate, candidateErr := s.qr.GetClearDevComplexExecutionCandidate(ctx, nullableString(row.ID))
		if candidateErr == nil {
			d.CandidateCommitID = candidate.ID
			d.CandidateCommitSHA = candidate.CommitSha
		} else if !errors.Is(candidateErr, sql.ErrNoRows) {
			return snapshot, false, candidateErr
		}
		snapshot.Dispatches = append(snapshot.Dispatches, d)
		if taskIndex, ok := taskByID[row.TaskMappingID]; ok {
			task := &snapshot.Tasks[taskIndex]
			task.CurrentRound = int(row.Round)
			task.CurrentDispatchID = row.ID
			if task.Status == cleardev.DevelopmentTaskStatusDone {
				continue
			}
			switch row.Status {
			case "PENDING", "RUNNING", "OBSERVED":
				task.Status = cleardev.DevelopmentTaskStatusRunning
			case "REVIEWING":
				task.Status = cleardev.DevelopmentTaskStatusReview
			case "VERIFIED":
				if task.Status != cleardev.DevelopmentTaskStatusDone {
					task.Status = cleardev.DevelopmentTaskStatusReview
				}
			case "REWORK":
				task.Status = cleardev.DevelopmentTaskStatusRework
				task.CurrentRound = int(row.Round) + 1
			case "BLOCKED":
				task.Status = cleardev.DevelopmentTaskStatusBlocked
				if cleardev.BuilderFirstFailureEnabled(snapshot.Run) && task.ReworkCount == int(row.Round)+1 {
					returned, returnErr := builderFirstRequiredCheckReturned(ctx, s.qr, row)
					if returnErr != nil {
						return snapshot, false, returnErr
					}
					if !returned {
						returned, returnErr = projectPlannerTaskReturned(ctx, s.qr, row)
						if returnErr != nil {
							return snapshot, false, returnErr
						}
					}
					if returned {
						task.Status, task.CurrentRound = cleardev.DevelopmentTaskStatusRework, int(row.Round)+1
					}
				}
			case "NEEDS_HUMAN":
				task.Status = cleardev.DevelopmentTaskStatusNeedsHuman
				if cleardev.BoundedMailAttempts(snapshot.Run) && row.ReasonCode == string(cleardev.MailAttemptLimitReason) {
					item, readErr := s.qr.GetClearDevComplexExecutionTaskByWorkItem(ctx, task.ID)
					if readErr != nil {
						return snapshot, false, readErr
					}
					if item.State == "REWORK" {
						task.Status, task.CurrentRound = cleardev.DevelopmentTaskStatusRework, int(row.Round)+1
					}
				} else if cleardev.BoundedMailAttempts(snapshot.Run) {
					canRetry, retryErr := mailInvalidCandidateCanRetry(ctx, s.qr, snapshot.Run.ID, task.ID, row.ReasonCode)
					if retryErr != nil {
						return snapshot, false, retryErr
					}
					if canRetry {
						task.Status, task.CurrentRound = cleardev.DevelopmentTaskStatusRework, int(row.Round)+1
					}
				}
			case "FAILED":
				task.Status = cleardev.DevelopmentTaskStatusBlocked
			}
			if cleardev.BuilderFirstFailureEnabled(snapshot.Run) && task.ReworkCount == int(row.Round)+1 {
				returned, err := projectPlannerTaskReturned(ctx, s.qr, row)
				if err != nil {
					return snapshot, false, err
				}
				if returned {
					task.Status, task.CurrentRound = cleardev.DevelopmentTaskStatusRework, int(row.Round)+1
				}
			}
			for _, recovery := range snapshot.WorkflowRecoveries {
				if recovery.Action == cleardev.RecoveryContinueBuilder && recovery.DispatchID == row.ID && task.ReworkCount == int(row.Round)+1 {
					task.Status, task.CurrentRound = cleardev.DevelopmentTaskStatusRework, int(row.Round)+1
				}
			}

		}
	}
	specs, err := s.qr.ListClearDevComplexExecutionCheckSpecs(ctx, run.ID)
	if err != nil {
		return snapshot, false, err
	}
	specByID := map[string]gen.CleardevComplexExecutionCheckSpec{}
	specArgv := map[string][]string{}
	for _, row := range specs {
		argv, e := decodeJSONStringSlice(row.ArgvJson, "execution check spec argv")
		if e != nil {
			return snapshot, false, e
		}
		checkID := row.CheckName
		if row.CheckKind == string(cleardev.CandidateCheckScope) {
			checkID = "SCOPE"
		}
		snapshot.CheckSpecs = append(snapshot.CheckSpecs, cleardev.ComplexExecutionCheckSpecFact{ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.TaskMappingID.String, CheckID: checkID, Kind: cleardev.CandidateCheckKind(row.CheckKind), CheckSpecSHA256: row.CheckSpecSha256, Argv: argv, TimeoutSeconds: int(row.TimeoutSeconds), CreatedAt: row.CreatedAt})
		specByID[row.ID] = row
		specArgv[row.ID] = argv
	}
	checkRuns, err := s.qr.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
	if err != nil {
		return snapshot, false, err
	}
	for _, row := range checkRuns {
		r := complexExecutionCheckRunFromGen(row)
		if taskIndex, ok := taskByID[attemptTaskID(attempts, row.TaskAttemptID)]; ok {
			a := snapshot.Tasks[taskIndex]
			r.ExecutionRunID = run.ID
			r.ComplexExecutionTaskID = a.ID
			r.BaseCommitSHA = attemptBase(attempts, row.TaskAttemptID)
		}
		if spec, ok := specByID[row.CheckSpecID]; ok {
			r.Kind = cleardev.CandidateCheckKind(spec.CheckKind)
			r.Argv = append([]string(nil), specArgv[row.CheckSpecID]...)
		}
		snapshot.CheckRuns = append(snapshot.CheckRuns, r)
	}
	reviews, err := s.qr.ListClearDevComplexExecutionReviews(ctx, run.ID)
	if err != nil {
		return snapshot, false, err
	}
	for _, row := range reviews {
		r := complexExecutionReviewFromGen(row)
		r.ExecutionRunID = run.ID
		r.ComplexExecutionTaskID = attemptTaskID(attempts, row.TaskAttemptID)
		r.CandidateCommitSHA = attemptCandidateSHA(ctx, s.qr, row.TaskAttemptID)
		r.BaseCommitSHA = attemptBase(attempts, row.TaskAttemptID)
		snapshot.Reviews = append(snapshot.Reviews, r)
	}
	verified, err := s.qr.ListClearDevComplexExecutionVerifiedCandidates(ctx, run.ID)
	if err != nil {
		return snapshot, false, err
	}
	for _, row := range verified {
		var required []string
		if e := json.Unmarshal([]byte(row.RequiredCheckRunsJson), &required); e != nil {
			return snapshot, false, e
		}
		candidate, e := s.qr.GetClearDevComplexExecutionCandidate(ctx, nullableString(row.TaskAttemptID))
		if e != nil {
			return snapshot, false, e
		}
		snapshot.Verifications = append(snapshot.Verifications, cleardev.ComplexExecutionVerification{ID: row.ID, ExecutionRunID: run.ID, ComplexExecutionTaskID: row.TaskMappingID, DispatchID: row.TaskAttemptID, CandidateCommitID: row.CandidateCommitID, CandidateCommitSHA: candidate.CommitSha, Round: int(attemptRound(attempts, row.TaskAttemptID)), ScopeEvidenceID: row.ScopeCheckRunID, RequiredCheckRunIDs: required, LocalReviewID: row.ReviewID, ReplacementRecoveryID: row.ReplacementRecoveryID.String, ReplacementAttemptID: row.ReplacementAttemptID.String, ReplacementResultID: row.ReplacementResultID.String, VerifiedAt: row.VerifiedAt})
	}
	if result, err := s.qr.GetClearDevComplexExecutionResult(ctx, run.ID); err == nil && result.CompletionStatus == "COMPLETED" {
		checks, e := s.qr.ListClearDevComplexExecutionResultChecks(ctx, result.ID)
		if e != nil {
			return snapshot, false, e
		}
		integrationCandidate, e := s.qr.GetClearDevIntegrationCandidate(ctx, result.IntegrationCandidateID)
		if e != nil {
			return snapshot, false, e
		}
		ids := make([]string, 0, len(checks))
		for _, x := range checks {
			ids = append(ids, x.CheckRunID)
		}
		snapshot.Integration = &cleardev.ComplexExecutionIntegration{ID: result.ID, ExecutionRunID: run.ID, IntegrationCandidateID: result.IntegrationCandidateID, CandidateCommitSHA: integrationCandidate.CommitSha, CheckRunIDs: ids, CompletedAt: result.CompletedAt.Time}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, false, err
	}
	facts, e := loadComplexExceptionFacts(ctx, s.qr, run.ID)
	if e != nil {
		return snapshot, false, e
	}
	snapshot.Exception = facts
	snapshot.FixedRecoveries, e = fixedRecoveryEvidence(ctx, s.qr, run.ID)
	if e != nil {
		return snapshot, false, e
	}
	if snapshot.Exception != nil {
		for i := range snapshot.Exception.RecoveryActions {
			for _, fixed := range snapshot.FixedRecoveries {
				if fixed.Claim != nil && fixed.Claim.ProposalID == snapshot.Exception.RecoveryActions[i].ID {
					snapshot.Exception.RecoveryActions[i].ExecutionResult = fixed.Result
				}
			}
		}
	}

	return snapshot, true, nil
}

func attemptTaskID(rows []gen.CleardevComplexExecutionTaskAttempt, id string) string {
	for _, r := range rows {
		if r.ID == id {
			return r.TaskMappingID
		}
	}
	return ""
}
func attemptBase(rows []gen.CleardevComplexExecutionTaskAttempt, id string) string {
	for _, r := range rows {
		if r.ID == id {
			return r.BaseCommitSha
		}
	}
	return ""
}
func attemptRound(rows []gen.CleardevComplexExecutionTaskAttempt, id string) int64 {
	for _, r := range rows {
		if r.ID == id {
			return r.Round
		}
	}
	return 0
}
func attemptCandidateSHA(ctx context.Context, q *gen.Queries, id string) string {
	row, e := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(id))
	if e != nil {
		return ""
	}
	return row.CommitSha
}

// StartClearDevComplexExecution creates an idempotent complex execution run.
func (s *Store) StartClearDevComplexExecution(ctx context.Context, c cleardev.StartComplexExecutionCommand) (cleardev.ComplexExecutionRun, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.ComplexExecutionRun
	var created bool
	err := s.inTx(ctx, "start ClearDev complex execution", func(q *gen.Queries) error {
		r := c.Run
		if err := validComplexExecutionRun(r); err != nil {
			return err
		}
		if existing, err := prepareProjectExecutionAdmission(ctx, q, r); err != nil {
			return err
		} else if existing != nil {
			out = *existing
			return nil
		}
		if err := validateComplexExecutionRunPolicy(ctx, q, r); err != nil {
			return err
		}
		if old, err := q.GetClearDevComplexExecutionRunForPlan(ctx, gen.GetClearDevComplexExecutionRunForPlanParams{RequirementVersionID: r.RequirementVersionID, PlanID: r.PlanID}); err == nil {
			out = complexExecutionRunFromGen(old)
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		contract, err := cleardev.PlannerTaskContractRun(r)
		if err != nil {
			return err
		}
		if contract {
			if c.StewardRoleBinding.ID != "" || c.AgentStep.ID != "" {
				return complexExecutionRule("a deterministic contract must not fabricate a Steward dispatch")
			}
		} else {
			if c.StewardRoleBinding.ExecutionRunID != r.ID || c.StewardRoleBinding.Role != cleardev.StandardRoleSteward {
				return complexExecutionRule("invalid initial Steward binding or step")
			}
			if err := requireOptionalDispatchStep(c.StewardRoleBinding, c.AgentStep, r.ID); err != nil {
				return err
			}
		}
		if err := q.InsertClearDevComplexExecutionRun(ctx, complexExecutionRunParams(r)); err != nil {
			return err
		}
		if !contract {
			if err := q.InsertClearDevComplexExecutionRoleBinding(ctx, complexExecutionRoleBindingParams(c.StewardRoleBinding)); err != nil {
				return err
			}
		}
		if c.AgentStep.ID != "" {
			if err := q.InsertClearDevComplexExecutionAgentStep(ctx, complexExecutionAgentStepParams(c.AgentStep)); err != nil {
				return err
			}
		}
		requirement, err := q.GetClearDevRequirement(ctx, r.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if err := insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectComplexExecutionRun, r.ID, cleardev.ActionRequestComplexExecution, cleardev.EventAccepted, cleardev.ReasonNone, c.StewardRoleBinding.AOSessionID, r.CreatedAt); err != nil {
			return err
		}
		out, created = r, true
		return nil
	})
	return out, created, err
}

// CreateClearDevComplexExecutionRoleBinding creates an idempotent execution role binding.
func (s *Store) CreateClearDevComplexExecutionRoleBinding(ctx context.Context, b cleardev.ComplexExecutionRoleBinding) (cleardev.ComplexExecutionRoleBinding, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.ComplexExecutionRoleBinding
	var created bool
	err := s.inTx(ctx, "create ClearDev complex execution role", func(q *gen.Queries) error {
		if old, err := q.GetClearDevComplexExecutionRoleBinding(ctx, b.ID); err == nil {
			out = complexExecutionRoleBindingFromGen(old)
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := q.GetClearDevComplexExecutionRun(ctx, b.ExecutionRunID); err != nil {
			return err
		}
		if b.Status != cleardev.RoleBindingStatusRequested || strings.TrimSpace(b.ID) == "" || strings.TrimSpace(b.SessionCreationIdempotencyKey) == "" {
			return complexExecutionRule("invalid execution role binding")
		}
		if err := q.InsertClearDevComplexExecutionRoleBinding(ctx, complexExecutionRoleBindingParams(b)); err != nil {
			return err
		}
		out, created = b, true
		return nil
	})
	return out, created, err
}

// ContinueClearDevComplexExecutionSteward replaces a conclusively ended
// original Steward with a REQUESTED continuation. The dispatch step is omitted
// until preflight passes and a session is bound.
func (s *Store) ContinueClearDevComplexExecutionSteward(ctx context.Context, priorID string, binding cleardev.ComplexExecutionRoleBinding, step cleardev.AgentStep, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "continue ClearDev complex execution Steward", func(q *gen.Queries) error {
		prior, err := q.GetClearDevComplexExecutionRoleBinding(ctx, priorID)
		if err != nil {
			return err
		}
		if prior.Role != string(cleardev.StandardRoleSteward) || prior.Status != string(cleardev.RoleBindingStatusBound) ||
			binding.ID == "" || binding.ExecutionRunID != prior.ExecutionRunID || binding.Role != cleardev.StandardRoleSteward ||
			binding.SourceComplexRoleBindingID != prior.SourceComplexRoleBindingID.String || binding.ContinuationOfRoleBindingID != prior.ID ||
			binding.Status != cleardev.RoleBindingStatusRequested {
			return complexExecutionRule("invalid Steward continuation")
		}
		if err := requireOptionalDispatchStep(binding, step, binding.ExecutionRunID); err != nil {
			return err
		}
		rows, err := q.EndClearDevComplexExecutionRoleBindingCAS(ctx, gen.EndClearDevComplexExecutionRoleBindingCASParams{ID: prior.ID, ReasonCode: "STEWARD_SESSION_ENDED", EndedAt: nullableTime(at)})
		if err != nil {
			return err
		}
		if rows != 1 {
			return complexExecutionRule("Steward continuation lost its prior binding")
		}
		if err := q.InsertClearDevComplexExecutionRoleBinding(ctx, complexExecutionRoleBindingParams(binding)); err != nil {
			return err
		}
		if step.ID != "" {
			if err := q.InsertClearDevComplexExecutionAgentStep(ctx, complexExecutionAgentStepParams(step)); err != nil {
				return err
			}
		}
		changed = true
		return nil
	})
	return changed, err
}

// BindClearDevComplexExecutionRoleBinding records a bound execution role session.
func (s *Store) BindClearDevComplexExecutionRoleBinding(ctx context.Context, id, sessionID, workspacePath, baseSHA string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "bind ClearDev complex execution role", func(q *gen.Queries) error {
		b, err := q.GetClearDevComplexExecutionRoleBinding(ctx, id)
		if err != nil {
			return err
		}
		mailReuse := strings.HasPrefix(b.SessionCreationIdempotencyKey, "cleardev-mail-review-rework:")
		if b.Status == "BOUND" || mailReuse && b.Status == "ENDED" {
			if mailReuse && (b.AoSessionID.String != sessionID || b.WorkspacePath != workspacePath || b.BaseCommitSha != baseSHA) {
				return complexExecutionRule("mail Reviewer reuse replay changed its session, workspace or candidate")
			}
			return nil
		}
		if (b.Role == string(cleardev.StandardRoleBuilder) || b.Role == string(cleardev.StandardRoleReviewer)) && (strings.TrimSpace(workspacePath) == "" || !complexExecutionSHA1(baseSHA)) {
			return complexExecutionRule("Builder and Reviewer require exact workspace and base")
		}
		rows, err := q.BindClearDevComplexExecutionRoleBindingCAS(ctx, gen.BindClearDevComplexExecutionRoleBindingCASParams{ID: id, AoSessionID: nullableString(sessionID), WorkspacePath: workspacePath, BaseCommitSha: baseSHA, BoundAt: nullableTime(at)})
		if err != nil {
			return err
		}
		changed = rows == 1
		return nil
	})
	return changed, err
}

// FailClearDevComplexExecutionRoleBinding marks an execution role binding failed.
func (s *Store) FailClearDevComplexExecutionRoleBinding(ctx context.Context, id string, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "fail ClearDev complex execution role", func(q *gen.Queries) error {
		rows, e := q.FailClearDevComplexExecutionRoleBindingCAS(ctx, gen.FailClearDevComplexExecutionRoleBindingCASParams{ID: id, ReasonCode: string(reason), EndedAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// EndClearDevComplexExecutionRoleBinding marks an execution role binding ended.
func (s *Store) EndClearDevComplexExecutionRoleBinding(ctx context.Context, id string, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "end ClearDev complex execution role", func(q *gen.Queries) error {
		rows, err := q.EndClearDevComplexExecutionRoleBindingCAS(ctx, gen.EndClearDevComplexExecutionRoleBindingCASParams{ID: id, ReasonCode: string(reason), EndedAt: nullableTime(at)})
		changed = rows == 1
		return err
	})
	return changed, err
}

// CreateClearDevComplexExecutionAgentStep creates an idempotent execution agent step.
func (s *Store) CreateClearDevComplexExecutionAgentStep(ctx context.Context, step cleardev.AgentStep) (cleardev.AgentStep, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.AgentStep
	var created bool
	err := s.inTx(ctx, "create ClearDev complex execution agent step", func(q *gen.Queries) error {
		if old, e := q.GetClearDevComplexExecutionAgentStep(ctx, step.ID); e == nil {
			out = complexExecutionAgentStepFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if step.SendStatus != cleardev.AgentStepSendStatusPending {
			return complexExecutionRule("agent step must start pending")
		}
		if e := q.InsertClearDevComplexExecutionAgentStep(ctx, complexExecutionAgentStepParams(step)); e != nil {
			return e
		}
		out, created = step, true
		return nil
	})
	return out, created, err
}

// MarkClearDevComplexExecutionAgentStepSent records that an agent step was sent.
func (s *Store) MarkClearDevComplexExecutionAgentStepSent(ctx context.Context, id string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "mark ClearDev complex execution agent step sent", func(q *gen.Queries) error {
		rows, e := q.MarkClearDevComplexExecutionAgentStepSentCAS(ctx, gen.MarkClearDevComplexExecutionAgentStepSentCASParams{ID: id, SentAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// SettleClearDevComplexExecutionAgentStep records a settled execution agent step.
func (s *Store) SettleClearDevComplexExecutionAgentStep(ctx context.Context, step cleardev.AgentStep) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "settle ClearDev complex execution agent step", func(q *gen.Queries) error {
		if step.SendStatus != cleardev.AgentStepSendStatusSettled || step.CompletedAt == nil {
			return complexExecutionRule("invalid settled agent step")
		}
		rows, e := q.SettleClearDevComplexExecutionAgentStepCAS(ctx, gen.SettleClearDevComplexExecutionAgentStepCASParams{ID: step.ID, TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID), FinalMessageText: nullableString(step.FinalMessageText), MessageSha256: nullableString(step.MessageSHA256), CompletedAt: nullableTimePtr(step.CompletedAt)})
		changed = rows == 1
		if e != nil || !changed {
			return e
		}
		return capturePlannerRuntimeEvent(ctx, q, step.ID)
	})
	return changed, err
}

// FailClearDevComplexExecutionAgentStep marks an execution agent step failed.
func (s *Store) FailClearDevComplexExecutionAgentStep(ctx context.Context, id string, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if strings.TrimSpace(id) == "" || reason == cleardev.ReasonNone {
		return false, complexExecutionRule("agent step failure requires an id and reason")
	}
	var changed bool
	err := s.inTx(ctx, "fail ClearDev complex execution agent step", func(q *gen.Queries) error {
		rows, err := q.FailClearDevComplexExecutionAgentStepCAS(ctx, gen.FailClearDevComplexExecutionAgentStepCASParams{ID: id, ReasonCode: string(reason), FailedAt: nullableTime(at)})
		changed = rows == 1
		return err
	})
	return changed, err
}

// SettleClearDevComplexExecutionRequest settles a request that needs a human decision.
func (s *Store) SettleClearDevComplexExecutionRequest(ctx context.Context, runID string, decision cleardev.ComplexExecutionDecision, reason cleardev.ReasonCode, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "settle ClearDev complex execution request", func(q *gen.Queries) error {
		run, err := q.GetClearDevComplexExecutionRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.Status == "NEEDS_HUMAN" && run.ReasonCode == string(reason) {
			return nil
		}
		if run.Status != "PENDING" || decision != cleardev.ComplexExecutionDecisionNeedsHuman || reason != cleardev.ReasonHumanDecisionRequired {
			return complexExecutionRule("invalid complex execution request settlement")
		}
		rows, err := q.SettleClearDevComplexExecutionRunCAS(ctx, gen.SettleClearDevComplexExecutionRunCASParams{ID: runID, Status: "NEEDS_HUMAN", ReasonCode: string(reason), SettledAt: nullableTime(at)})
		if err != nil {
			return err
		}
		if rows != 1 {
			return complexExecutionRule("complex execution request settlement lost its compare-and-swap")
		}
		return nil
	})
}

// validateComplexExecutionMaterialization reconstructs every transient input
// from the reviewed plan and the current requirement version.  The command is
// intentionally not trusted: it may only carry backend-assigned IDs around
// those immutable facts, not change their order, text, dependencies, or fixed
// command catalogue.
func validateComplexExecutionMaterialization(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, c cleardev.MaterializeComplexExecutionCommand) error {
	if err := validateComplexExecutionRunPolicy(ctx, q, complexExecutionRunFromGen(run)); err != nil {
		return err
	}
	if len(c.Tasks) < cleardev.MinComplexPlanTasks || len(c.Tasks) > cleardev.MaxComplexPlanTasks {
		return complexExecutionRule("invalid materialization")
	}
	mode := cleardev.WorkMode(run.Mode)
	if len(c.BuilderBindings) == 0 || int(run.FixedBuilderCount) != len(c.BuilderBindings) {
		return complexExecutionRule("invalid materialization")
	}
	for slot, binding := range c.BuilderBindings {
		if binding.ExecutionRunID != run.ID || binding.Role != cleardev.StandardRoleBuilder ||
			binding.Status != cleardev.RoleBindingStatusRequested || binding.BuilderSlot != slot+1 {
			return complexExecutionRule("invalid materialization")
		}
	}
	version, err := q.GetClearDevRequirementVersion(ctx, run.RequirementVersionID)
	if err != nil {
		return err
	}
	if version.DevelopmentProjectID != run.DevelopmentProjectID || version.Sha256 != run.RequirementSha256 {
		return complexExecutionRule("run does not match current requirement version")
	}
	planRow, err := q.GetApprovedClearDevComplexPlanForVersion(ctx, run.RequirementVersionID)
	if err != nil {
		return err
	}
	if planRow.ID != run.PlanID || planRow.RequirementVersionID != run.RequirementVersionID ||
		planRow.RequirementSha256 != run.RequirementSha256 || planRow.PlanSha256 != run.PlanSha256 ||
		complexExecutionRawDigest([]byte(planRow.PlanJson)) != run.PlanSha256 {
		return complexExecutionRule("run does not match approved complex plan")
	}
	var plan cleardev.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(planRow.PlanJson), &plan); err != nil {
		return complexExecutionRule("approved complex plan is invalid")
	}
	policy, catalog, policyErr := benchmarkExecutionPolicyAndCatalog(ctx, q, run.DevelopmentProjectID)
	if policyErr != nil {
		return policyErr
	}
	suggested := plan.ParallelSuggestion.RecommendedBuilderCount
	selection, selectionErr := cleardev.SelectComplexExecutionModeForPolicy(plan.Tasks, suggested, policy)
	contract, project, projectErr := cleardev.ProjectContractFromRun(complexExecutionRunFromGen(run))
	if projectErr != nil {
		return projectErr
	}
	var projectContract *cleardev.ProjectExecutionContract
	if project {
		projectContract = &contract
		catalog = contract.Basis.CheckCatalog()
		selection, selectionErr = cleardev.ProjectExecutionMode(plan), nil
		plan = cleardev.ProjectExecutionPlan(plan, contract.Basis)
	}
	if selectionErr != nil {
		return complexExecutionRule("approved complex plan is not supported by complex execution")
	}
	if plan.RequirementVersionID != run.RequirementVersionID || plan.RequirementVersionSHA256 != run.RequirementSha256 ||
		len(plan.Tasks) != len(c.Tasks) ||
		len(plan.Tasks) < cleardev.MinComplexPlanTasks || len(plan.Tasks) > cleardev.MaxComplexPlanTasks ||
		selection.Mode != mode || selection.BuilderCount != int(run.FixedBuilderCount) ||
		string(selection.ReasonCode) != run.SelectionReasonCode {
		return complexExecutionRule("approved complex plan is not supported by this execution mode")
	}
	if mode == cleardev.WorkModeParallel {
		if len(c.Batches) != len(selection.Batches) {
			return complexExecutionRule("materialized batches do not match the frozen mode selection")
		}
		for index, batch := range c.Batches {
			if batch.ExecutionRunID != run.ID || batch.Ordinal != index || batch.Status != cleardev.ComplexExecutionBatchPending ||
				batch.CommonBaseSHA != "" || batch.ComposedAt != nil || !slices.Equal(batch.TaskKeys, selection.Batches[index]) {
				return complexExecutionRule("materialized batches do not match the frozen mode selection")
			}
		}
	} else if len(c.Batches) != 0 {
		return complexExecutionRule("STANDARD materialization cannot carry batches")
	}

	tasksByKey := make(map[string]cleardev.ComplexExecutionTask, len(c.Tasks))
	for ordinal, task := range c.Tasks {
		if task.ExecutionRunID != run.ID || task.ID == "" || task.DevelopmentTaskID == "" ||
			task.Ordinal != ordinal || task.Status != cleardev.DevelopmentTaskStatusPlanned || task.ReworkCount != 0 || task.CurrentRound != 0 ||
			task.TaskKey != plan.Tasks[ordinal].Key || !slices.Equal(task.DependencyTaskKeys, plan.Tasks[ordinal].DependencyKeys) {
			return complexExecutionRule("materialized tasks do not match approved plan order")
		}
		if _, exists := tasksByKey[task.TaskKey]; exists {
			return complexExecutionRule("materialized task keys are duplicated")
		}
		tasksByKey[task.TaskKey] = task
	}

	for ordinal, task := range c.Tasks {
		planTask := plan.Tasks[ordinal]
		dependencyIDs := make([]string, 0, len(planTask.DependencyKeys))
		for _, dependencyKey := range planTask.DependencyKeys {
			dependency, ok := tasksByKey[dependencyKey]
			if !ok {
				return complexExecutionRule("approved plan dependency is missing from materialization")
			}
			dependencyIDs = append(dependencyIDs, dependency.DevelopmentTaskID)
		}
		_, expectedPackage, expectedDigest, err := cleardev.BuildComplexStandardExecutionPackageWithCatalog(mode, cleardev.ComplexStandardExecutionPackageInput{
			ExecutionRunID: run.ID, RequirementVersionID: version.ID, RequirementSHA256: version.Sha256,
			RequirementText: version.ContractText, PlanID: run.PlanID, PlanSHA256: run.PlanSha256,
			TaskSetVersion: cleardev.ComplexStandardTaskSetVersion, TaskID: task.DevelopmentTaskID,
			DependencyTaskIDs: dependencyIDs, Task: planTask,
			PlanSchemaVersion: plan.SchemaVersion, InterfaceContracts: plan.InterfaceContracts, ProjectExecution: projectContract,
		}, catalog)
		if err != nil || task.ExecutionPackageSHA256 != expectedDigest ||
			complexExecutionRawDigest([]byte(task.ExecutionPackageJSON)) != expectedDigest ||
			task.ExecutionPackageJSON != string(expectedPackage) {
			return complexExecutionRule("task package does not exactly match approved plan")
		}
	}

	if err := validateComplexExecutionCheckSpecsWithCatalog(run.ID, plan, c.Tasks, c.CheckSpecs, catalog); err != nil {
		return err
	}
	return nil
}

func validateComplexExecutionCheckSpecs(runID string, plan cleardev.ComplexEngineeringPlanResult, tasks []cleardev.ComplexExecutionTask, specs []cleardev.ComplexExecutionCheckSpecFact) error {
	return validateComplexExecutionCheckSpecsWithCatalog(runID, plan, tasks, specs, cleardev.FrozenComplexCheckCatalog())
}

func validateComplexExecutionCheckSpecsWithCatalog(runID string, plan cleardev.ComplexEngineeringPlanResult, tasks []cleardev.ComplexExecutionTask, specs []cleardev.ComplexExecutionCheckSpecFact, catalog []cleardev.ComplexCheckSpec) error {
	tasksByID := make(map[string]cleardev.ComplexExecutionTask, len(tasks))
	planByKey := make(map[string]cleardev.ComplexPlanTask, len(plan.Tasks))
	for _, task := range tasks {
		tasksByID[task.ID] = task
	}
	for _, task := range plan.Tasks {
		if plan.SchemaVersion != cleardev.ProjectPlanningVersion && (len(task.GeneratedPaths) != 0 || len(task.SharedPathsRequireApproval) != 0) {
			if err := cleardev.ValidateComplexExecutionGeneratedPaths([]cleardev.ComplexPlanTask{task}); err != nil {
				return complexExecutionRule("STANDARD execution cannot materialize generated or shared paths")
			}
		}
		planByKey[task.Key] = task
	}

	expected := len(plan.IntegrationCheckIDs)
	for _, task := range plan.Tasks {
		expected += 1 + len(task.RequiredCheckIDs)
	}
	if len(specs) != expected {
		return complexExecutionRule("materialized check catalog has missing or extra facts")
	}
	seenIDs := make(map[string]struct{}, len(specs))
	seenScopes := make(map[string]bool, len(tasks))
	seenRequired := make(map[string]bool, expected)
	seenIntegration := make(map[string]bool, len(plan.IntegrationCheckIDs))
	for _, spec := range specs {
		if spec.ID == "" || spec.ExecutionRunID != runID || spec.CreatedAt.IsZero() {
			return complexExecutionRule("invalid materialized check spec")
		}
		if _, exists := seenIDs[spec.ID]; exists {
			return complexExecutionRule("materialized check spec IDs are duplicated")
		}
		seenIDs[spec.ID] = struct{}{}
		switch spec.Kind {
		case cleardev.CandidateCheckScope:
			task, ok := tasksByID[spec.ComplexExecutionTaskID]
			if !ok || spec.CheckID != "SCOPE" || len(spec.Argv) != 0 || spec.TimeoutSeconds != 0 || seenScopes[task.ID] {
				return complexExecutionRule("scope check does not exactly match task")
			}
			if spec.CheckSpecSHA256 != complexExecutionScopeDigest(planByKey[task.TaskKey]) {
				return complexExecutionRule("scope check digest does not match task path rules")
			}
			seenScopes[task.ID] = true
		case cleardev.CandidateCheckRequired:
			task, ok := tasksByID[spec.ComplexExecutionTaskID]
			planTask, planOK := planByKey[task.TaskKey]
			check, checkOK := cleardev.ComplexCheckByID(catalog, spec.CheckID)
			key := spec.ComplexExecutionTaskID + "\x00" + spec.CheckID
			if !ok || !planOK || !checkOK || !slices.Contains(planTask.RequiredCheckIDs, spec.CheckID) || seenRequired[key] ||
				!slices.Equal(spec.Argv, check.Argv) || spec.TimeoutSeconds != check.TimeoutSeconds || spec.CheckSpecSHA256 != complexExecutionDigest(check.Argv) {
				return complexExecutionRule("required check does not match frozen task catalog")
			}
			seenRequired[key] = true
		case cleardev.CandidateCheckIntegration:
			check, ok := cleardev.ComplexCheckByID(catalog, spec.CheckID)
			if spec.ComplexExecutionTaskID != "" || !ok || !slices.Contains(plan.IntegrationCheckIDs, spec.CheckID) || seenIntegration[spec.CheckID] ||
				!slices.Equal(spec.Argv, check.Argv) || spec.TimeoutSeconds != check.TimeoutSeconds || spec.CheckSpecSHA256 != complexExecutionDigest(check.Argv) {
				return complexExecutionRule("integration check does not match frozen plan catalog")
			}
			seenIntegration[spec.CheckID] = true
		default:
			return complexExecutionRule("unknown materialized check kind")
		}
	}
	for _, task := range tasks {
		if !seenScopes[task.ID] {
			return complexExecutionRule("task scope check is missing")
		}
		for _, checkID := range planByKey[task.TaskKey].RequiredCheckIDs {
			if !seenRequired[task.ID+"\x00"+checkID] {
				return complexExecutionRule("required task check is missing")
			}
		}
	}
	for _, checkID := range plan.IntegrationCheckIDs {
		if !seenIntegration[checkID] {
			return complexExecutionRule("integration check is missing")
		}
	}
	return nil
}

// MaterializeClearDevComplexExecution is one transaction: accepting the exact
// Steward request cannot leave a partial task set or a Builder without frozen
// work items, permissions, and check specifications.
func (s *Store) MaterializeClearDevComplexExecution(ctx context.Context, c cleardev.MaterializeComplexExecutionCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "materialize ClearDev complex execution", func(q *gen.Queries) error {
		run, err := q.GetClearDevComplexExecutionRun(ctx, c.ExecutionRunID)
		if err != nil {
			return err
		}
		if err := validateComplexExecutionMaterialization(ctx, q, run, c); err != nil {
			return err
		}
		if run.Status == "PENDING" {
			if _, err = q.AcceptClearDevComplexExecutionRunCAS(ctx, gen.AcceptClearDevComplexExecutionRunCASParams{ID: run.ID, AcceptedAt: nullableTime(c.At)}); err != nil {
				return err
			}
		}
		run, err = q.GetClearDevComplexExecutionRun(ctx, c.ExecutionRunID)
		if err != nil {
			return err
		}
		if run.Status != "ACCEPTED" {
			return complexExecutionRule("execution is not accepted")
		}
		byKey := map[string]cleardev.ComplexExecutionTask{}
		for _, task := range c.Tasks {
			byKey[task.TaskKey] = task
		}
		for _, task := range c.Tasks {
			pkg, _ := cleardev.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
			paths, e := json.Marshal(pkg.WritePaths)
			if e != nil {
				return e
			}
			generated, _ := json.Marshal(pkg.GeneratedPaths)
			shared, _ := json.Marshal(pkg.SharedPathsRequireApproval)
			forbidden, _ := json.Marshal(pkg.ForbiddenPaths)
			if e := q.InsertClearDevComplexExecutionTaskMapping(ctx, gen.InsertClearDevComplexExecutionTaskMappingParams{ID: task.ID, ExecutionRunID: run.ID, PlanTaskKey: task.TaskKey, WorkItemID: task.DevelopmentTaskID, Ordinal: int64(task.Ordinal), TaskPacketJson: task.ExecutionPackageJSON, TaskPacketSha256: task.ExecutionPackageSHA256, CreatedAt: c.At}); e != nil {
				return e
			}
			if e := q.InsertClearDevComplexExecutionWorkItem(ctx, gen.InsertClearDevComplexExecutionWorkItemParams{ID: task.DevelopmentTaskID, DevelopmentProjectID: run.DevelopmentProjectID, ContractVersionID: run.RequirementVersionID, Title: pkg.Title, Mode: string(cleardev.WorkModeStandard), State: string(cleardev.DevelopmentTaskStatusPlanned), MaxReworkCount: mailWorkItemAttemptLimit(run, pkg.MaxReworkCount), ReworkCount: 0, ComplexExecutionTaskID: nullableString(task.ID), CreatedAt: c.At, UpdatedAt: c.At}); e != nil {
				return e
			}
			if e := q.InsertClearDevComplexExecutionPermission(ctx, gen.InsertClearDevComplexExecutionPermissionParams{ID: task.ID + ":permission", WorkItemID: task.DevelopmentTaskID, Version: 1, WritePaths: string(paths), GeneratedPaths: string(generated), SharedPathsRequireApproval: string(shared), ForbiddenPaths: string(forbidden), ComplexExecutionTaskID: nullableString(task.ID), CreatedAt: c.At}); e != nil {
				return e
			}
		}
		for _, task := range c.Tasks {
			for dependencyOrdinal, key := range task.DependencyTaskKeys {
				dep, ok := byKey[key]
				if !ok {
					return complexExecutionRule("unknown materialized dependency")
				}
				if err := q.InsertClearDevComplexExecutionDependency(ctx, gen.InsertClearDevComplexExecutionDependencyParams{TaskMappingID: task.ID, DependsOnMappingID: dep.ID, DependencyOrdinal: int64(dependencyOrdinal)}); err != nil {
					return err
				}
			}
		}
		for _, spec := range c.CheckSpecs {
			if spec.ExecutionRunID != run.ID || spec.ID == "" {
				return complexExecutionRule("invalid check spec")
			}
			taskID := nullableString(spec.ComplexExecutionTaskID)
			checkName := spec.CheckID
			argv, e := json.Marshal(spec.Argv)
			if e != nil {
				return e
			}
			required := sql.NullString{}
			if spec.Kind == cleardev.CandidateCheckRequired {
				if _, ok := byKey[taskKeyForID(c.Tasks, spec.ComplexExecutionTaskID)]; !ok {
					return complexExecutionRule("required check has no task")
				}
				required = nullableString(spec.ID + ":required")
			} else if spec.Kind == cleardev.CandidateCheckScope {
				checkName = ""
				argv = []byte("[]")
			} else if spec.Kind != cleardev.CandidateCheckIntegration {
				return complexExecutionRule("unknown check kind")
			}
			if e := q.InsertClearDevComplexExecutionCheckSpec(ctx, gen.InsertClearDevComplexExecutionCheckSpecParams{ID: spec.ID, ExecutionRunID: run.ID, TaskMappingID: taskID, RequiredCheckID: required, CheckKind: string(spec.Kind), CheckName: checkName, CheckSpecSha256: spec.CheckSpecSHA256, ArgvJson: string(argv), TimeoutSeconds: int64(spec.TimeoutSeconds), CreatedAt: c.At}); e != nil {
				return e
			}
			if spec.Kind == cleardev.CandidateCheckRequired {
				if e := q.InsertClearDevComplexExecutionRequiredCheck(ctx, gen.InsertClearDevComplexExecutionRequiredCheckParams{ID: required.String, WorkItemID: byID(c.Tasks, spec.ComplexExecutionTaskID).DevelopmentTaskID, Name: spec.CheckID, CheckKind: string(spec.Kind), ComplexExecutionCheckSpecID: nullableString(spec.ID), CreatedAt: c.At}); e != nil {
					return e
				}
			}
		}
		for _, binding := range c.BuilderBindings {
			if err := q.InsertClearDevComplexExecutionRoleBinding(ctx, complexExecutionRoleBindingParams(binding)); err != nil {
				return err
			}
		}
		for _, batch := range c.Batches {
			keys, e := json.Marshal(batch.TaskKeys)
			if e != nil {
				return e
			}
			if e := q.InsertClearDevComplexExecutionBatch(ctx, gen.InsertClearDevComplexExecutionBatchParams{ID: batch.ID, ExecutionRunID: run.ID, Ordinal: int64(batch.Ordinal), TaskKeysJson: string(keys), CreatedAt: batch.CreatedAt}); e != nil {
				return e
			}
		}
		approvedPlan, e := q.GetApprovedClearDevComplexPlanForVersion(ctx, run.RequirementVersionID)
		if e != nil {
			return e
		}
		var plan cleardev.ComplexEngineeringPlanResult
		if e := json.Unmarshal([]byte(approvedPlan.PlanJson), &plan); e != nil {
			return complexExecutionRule("approved complex plan is invalid")
		}
		if e := insertComplexExceptionMaterialization(ctx, q, run, c.Tasks, plan, c.At); e != nil {
			return e
		}
		rows, err := q.IncrementClearDevTaskSetVersionCAS(ctx, gen.IncrementClearDevTaskSetVersionCASParams{ID: run.RequirementVersionID, ExpectedTaskSetVersion: run.ExpectedTaskSetVersion})
		if err != nil {
			return err
		}
		if rows != 1 {
			return complexExecutionRule("current task set changed during materialization")
		}
		requirement, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
		if err != nil {
			return err
		}
		if err := insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectComplexExecutionRun, run.ID, cleardev.ActionAcceptComplexExecution, cleardev.EventAccepted, cleardev.ReasonNone, "", c.At); err != nil {
			return err
		}
		return nil
	})
}

// CreateClearDevComplexExecutionDispatch creates and starts an idempotent task dispatch.
func (s *Store) CreateClearDevComplexExecutionDispatch(ctx context.Context, c cleardev.CreateComplexExecutionDispatchCommand) (cleardev.ComplexExecutionDispatch, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.ComplexExecutionDispatch
	var created bool
	err := s.inTx(ctx, "create ClearDev complex dispatch", func(q *gen.Queries) error {
		d := c.Dispatch
		if old, e := q.GetClearDevComplexExecutionTaskAttempt(ctx, d.ID); e == nil {
			out = complexExecutionDispatchFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if d.Status != cleardev.ComplexExecutionDispatchPending || d.ExecutionRunID == "" || !complexExecutionSHA1(d.BaseCommitSHA) || c.AgentStep.ID != d.AgentStepID || c.AgentStep.RequestID != d.ID || c.AgentStep.Kind != cleardev.ComplexExecutionAgentStepBuilderTask || c.AgentStep.RoleBindingID == "" {
			return complexExecutionRule("invalid dispatch")
		}
		incomplete, e := q.CountClearDevComplexExecutionIncompleteDependencies(ctx, d.ComplexExecutionTaskID)
		if e != nil {
			return e
		}
		if incomplete != 0 {
			return complexExecutionRule("dispatch dependencies are not verified")
		}
		if err := validatePlannerRuntimeDispatch(ctx, q, d); err != nil {
			return err
		}
		if e := q.InsertClearDevComplexExecutionAgentStep(ctx, complexExecutionAgentStepParams(c.AgentStep)); e != nil {
			return e
		}
		if e := insertMailAttemptSlot(ctx, q, d); e != nil {
			return e
		}
		if e := q.InsertClearDevComplexExecutionTaskAttempt(ctx, gen.InsertClearDevComplexExecutionTaskAttemptParams{ID: d.ID, ExecutionRunID: d.ExecutionRunID, TaskMappingID: d.ComplexExecutionTaskID, BuilderRoleBindingID: c.AgentStep.RoleBindingID, AgentStepID: d.AgentStepID, Round: int64(d.Round), BaseCommitSha: d.BaseCommitSHA, Status: string(d.Status), BatchID: nullableString(c.BatchID)}); e != nil {
			return e
		}
		mapping, mappingErr := q.GetClearDevComplexExecutionTaskMapping(ctx, d.ComplexExecutionTaskID)
		if mappingErr != nil {
			return mappingErr
		}
		if e := acquireComplexExceptionPathLeases(ctx, q, d.ExecutionRunID, d.ComplexExecutionTaskID, d.ID, exclusiveTaskPathsFromPackage(mapping.TaskPacketJson), d.CreatedAt); e != nil {
			return e
		}
		if _, e := q.StartClearDevComplexExecutionTaskAttemptCAS(ctx, gen.StartClearDevComplexExecutionTaskAttemptCASParams{ID: d.ID, DispatchedAt: nullableTime(d.CreatedAt)}); e != nil {
			return e
		}
		item, e := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, d.ComplexExecutionTaskID)
		if e != nil {
			return e
		}
		if _, e = q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount, NextState: string(cleardev.DevelopmentTaskStatusRunning), ReworkCount: item.ReworkCount, UpdatedAt: d.CreatedAt}); e != nil {
			return e
		}
		d.Status = cleardev.ComplexExecutionDispatchRunning
		run, e := q.GetClearDevComplexExecutionRun(ctx, d.ExecutionRunID)
		if e != nil {
			return e
		}
		requirement, e := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
		if e != nil {
			return e
		}
		if e := insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectComplexExecutionDispatch, d.ID, cleardev.ActionDispatchComplexTask, cleardev.EventAccepted, cleardev.ReasonNone, "", d.CreatedAt); e != nil {
			return e
		}
		out, created = d, true
		return nil
	})
	return out, created, err
}

// AppendClearDevComplexExecutionCandidate records the candidate observed for a dispatch.
func (s *Store) AppendClearDevComplexExecutionCandidate(ctx context.Context, c cleardev.AppendComplexExecutionCandidateCommand) (cleardev.CandidateCommit, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.CandidateCommit
	err := s.inTx(ctx, "append ClearDev complex candidate", func(q *gen.Queries) error {
		a, e := q.GetClearDevComplexExecutionTaskAttempt(ctx, c.DispatchID)
		if e != nil {
			return e
		}
		if a.ExecutionRunID != c.ExecutionRunID || a.TaskMappingID != c.ComplexExecutionTaskID || a.Round != int64(c.Round) || a.BaseCommitSha != c.BaseCommitSHA {
			return complexExecutionRule("candidate does not match dispatch")
		}
		task, e := q.GetClearDevComplexExecutionTaskMapping(ctx, a.TaskMappingID)
		if e != nil {
			return e
		}
		if old, e := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(a.ID)); e == nil {
			out = clearDevCandidateFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if !complexExecutionSHA1(c.Candidate.CommitSHA) {
			return complexExecutionRule("invalid candidate sha")
		}
		sequence, e := q.NextClearDevCandidateSequence(ctx, task.WorkItemID)
		if e != nil {
			return e
		}
		permissionID, permErr := latestPermissionID(ctx, q, task.WorkItemID)
		if permErr != nil {
			return permErr
		}
		if e := q.InsertClearDevComplexExecutionCandidate(ctx, gen.InsertClearDevComplexExecutionCandidateParams{ID: c.Candidate.ID, WorkItemID: task.WorkItemID, Sequence: sequence, AoSessionID: c.Candidate.AOSessionID, PermissionVersionID: permissionID, ComplexExecutionTaskAttemptID: nullableString(a.ID), ComplexExecutionBaseCommitSha: nullableString(a.BaseCommitSha), CommitSha: c.Candidate.CommitSHA, CreatedAt: c.Candidate.ObservedAt}); e != nil {
			return e
		}
		if _, e = q.AdvanceClearDevComplexExecutionTaskAttemptCAS(ctx, gen.AdvanceClearDevComplexExecutionTaskAttemptCASParams{ID: a.ID, ExpectedStatus: "RUNNING", Status: "OBSERVED", ReasonCode: ""}); e != nil {
			return e
		}
		out = cleardev.CandidateCommit{ID: c.Candidate.ID, DevelopmentTaskID: task.WorkItemID, Sequence: sequence, AOSessionID: c.Candidate.AOSessionID, PermissionVersionID: permissionID, CommitSHA: c.Candidate.CommitSHA, CreatedAt: c.Candidate.ObservedAt}
		return nil
	})
	return out, err
}

// CreateClearDevComplexExecutionCheckRun creates an idempotent check run.
func (s *Store) CreateClearDevComplexExecutionCheckRun(ctx context.Context, r cleardev.ComplexExecutionCheckRun) (cleardev.ComplexExecutionCheckRun, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.ComplexExecutionCheckRun
	var created bool
	err := s.inTx(ctx, "create ClearDev complex check run", func(q *gen.Queries) error {
		if old, e := q.GetClearDevComplexExecutionCheckRun(ctx, r.ID); e == nil {
			out = complexExecutionCheckRunFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if r.Status != cleardev.ComplexExecutionCheckRunPending || r.DispatchID == "" || r.CandidateCommitID == "" || !complexExecutionSHA1(r.CandidateCommitSHA) {
			return complexExecutionRule("invalid check run")
		}
		if e := q.InsertClearDevComplexExecutionCheckRun(ctx, gen.InsertClearDevComplexExecutionCheckRunParams{ID: r.ID, CheckSpecID: r.CheckSpecFactID, TaskAttemptID: r.DispatchID, CandidateCommitID: r.CandidateCommitID, CandidateCommitSha: r.CandidateCommitSHA, Status: string(r.Status), CreatedAt: r.CreatedAt, RetryOrdinal: int64(r.RetryOrdinal)}); e != nil {
			return e
		}
		out, created = r, true
		return nil
	})
	return out, created, err
}

// StartClearDevComplexExecutionCheckRun marks a check run started.
func (s *Store) StartClearDevComplexExecutionCheckRun(ctx context.Context, id string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "start ClearDev complex check run", func(q *gen.Queries) error {
		rows, e := q.MarkClearDevComplexExecutionCheckRunStartedCAS(ctx, gen.MarkClearDevComplexExecutionCheckRunStartedCASParams{ID: id, StartedAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// SettleClearDevComplexExecutionCheckRun records a check run result.
func (s *Store) SettleClearDevComplexExecutionCheckRun(ctx context.Context, c cleardev.SettleComplexExecutionCheckCommand) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "settle ClearDev complex check run", func(q *gen.Queries) error {
		if err := validateProjectCheckSettlement(ctx, q, c); err != nil {
			return err
		}
		status := "SETTLED"
		reason := string(c.ReasonCode)
		result := nullableString(string(c.Result))
		if c.Result != "PASS" && c.Result != "FAIL" {
			status = "FAILED"
			result = sql.NullString{}
		}
		rows, e := q.SettleClearDevComplexExecutionCheckRunCAS(ctx, gen.SettleClearDevComplexExecutionCheckRunCASParams{ID: c.CheckRunID, ContainerImageID: nullableString(c.ContainerImageID), ExitCode: nullableComplexExecutionInt(c.ExitCode), Status: status, TimedOut: sql.NullBool{Bool: c.TimedOut, Valid: status == "SETTLED"}, OutputSummary: nullableString(c.OutputSummary), OutputSha256: nullableString(c.OutputSHA256), ChangedPathsJson: nullableString(c.ChangedPathsJSON), Result: result, SettledAt: nullableTime(c.At), ReasonCode: reason})
		changed = rows == 1
		return e
	})
	return changed, err
}

// CreateClearDevComplexExecutionReview creates an independent reviewer request.
func (s *Store) CreateClearDevComplexExecutionReview(ctx context.Context, c cleardev.CreateComplexExecutionReviewCommand) (cleardev.ComplexExecutionReview, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.ComplexExecutionReview
	var created bool
	err := s.inTx(ctx, "create ClearDev complex review", func(q *gen.Queries) error {
		r := c.Review
		if old, e := q.GetClearDevComplexExecutionReview(ctx, r.ID); e == nil {
			out = complexExecutionReviewFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if r.Status != cleardev.LocalReviewStatusPending || c.ReviewerBinding.Role != cleardev.StandardRoleReviewer || c.ReviewerBinding.ExecutionRunID != r.ExecutionRunID || c.AgentStep.ID != r.AgentStepID || c.AgentStep.RoleBindingID != c.ReviewerBinding.ID || c.AgentStep.Kind != cleardev.AgentStepLocalReview || c.AgentStep.RequestID != r.ID {
			return complexExecutionRule("invalid reviewer request")
		}
		binding, e := q.GetClearDevComplexExecutionRoleBinding(ctx, c.ReviewerBinding.ID)
		if e != nil {
			return e
		}
		if binding.Status != "BOUND" || binding.WorkspacePath != r.CandidateWorkspacePath {
			return complexExecutionRule("review requires its bound independent Reviewer workspace")
		}
		if _, e = q.AdvanceClearDevComplexExecutionTaskAttemptCAS(ctx, gen.AdvanceClearDevComplexExecutionTaskAttemptCASParams{ID: r.DispatchID, ExpectedStatus: "OBSERVED", Status: "REVIEWING", ReasonCode: ""}); e != nil {
			return e
		}
		item, e := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, r.ComplexExecutionTaskID)
		if e != nil {
			return e
		}
		rows, e := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount, NextState: string(cleardev.DevelopmentTaskStatusReview), ReworkCount: item.ReworkCount, UpdatedAt: r.CreatedAt})
		if e != nil {
			return e
		}
		if rows != 1 {
			return complexExecutionRule("review task state changed concurrently")
		}
		if e := q.InsertClearDevComplexExecutionAgentStep(ctx, complexExecutionAgentStepParams(c.AgentStep)); e != nil {
			return e
		}
		if e := q.InsertClearDevComplexExecutionReview(ctx, gen.InsertClearDevComplexExecutionReviewParams{ID: r.ID, TaskAttemptID: r.DispatchID, CandidateCommitID: r.CandidateCommitID, ReviewerRoleBindingID: r.ReviewerRoleBindingID, AgentStepID: r.AgentStepID, ReviewPacketJson: r.ReviewPacketJSON, ReviewPacketSha256: r.ReviewPacketSHA256, CandidateWorktreePath: r.CandidateWorkspacePath, Status: string(r.Status), CreatedAt: r.CreatedAt}); e != nil {
			return e
		}
		out, created = r, true
		return nil
	})
	return out, created, err
}

// SettleClearDevComplexExecutionReview records a review verdict and ends its reviewer.
func (s *Store) SettleClearDevComplexExecutionReview(ctx context.Context, c cleardev.SettleComplexExecutionReviewCommand) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "settle ClearDev complex review", func(q *gen.Queries) error {
		r, e := q.GetClearDevComplexExecutionReview(ctx, c.ReviewID)
		if e != nil {
			return e
		}
		if e := validateReviewerChecks(ctx, q, r, string(c.Verdict)); e != nil {
			return e
		}
		rows, e := q.SettleClearDevComplexExecutionReviewCAS(ctx, gen.SettleClearDevComplexExecutionReviewCASParams{ID: c.ReviewID, Status: "SETTLED", TurnID: nullableString(c.TurnID), FinalMessageID: nullableString(c.FinalMessageID), Verdict: nullableString(string(c.Verdict)), ReasonCode: string(c.ReasonCode), Summary: nullableString(c.Summary), SettledAt: nullableTime(c.At)})
		if e != nil {
			return e
		}
		if rows == 1 {
			if _, e = q.EndClearDevComplexExecutionRoleBindingCAS(ctx, gen.EndClearDevComplexExecutionRoleBindingCASParams{ID: r.ReviewerRoleBindingID, ReasonCode: string(c.ReasonCode), EndedAt: nullableTime(c.At)}); e != nil {
				return e
			}
			changed = true
		}
		return nil
	})
	return changed, err
}

// ApplyClearDevComplexExecutionFailure records a terminal failure or rework transition.
func (s *Store) ApplyClearDevComplexExecutionFailure(ctx context.Context, runID, taskID, dispatchID string, infrastructure bool, reason cleardev.ReasonCode, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "apply ClearDev complex execution failure", func(q *gen.Queries) error {
		a, e := q.GetClearDevComplexExecutionTaskAttempt(ctx, dispatchID)
		if e != nil {
			return e
		}
		if a.ExecutionRunID != runID || a.TaskMappingID != taskID {
			return complexExecutionRule("failure does not match dispatch")
		}
		next := "REWORK"
		switch reason {
		case cleardev.ReasonHumanDecisionRequired, "REVIEW_NEEDS_HUMAN", "BUILDER_NEEDS_HUMAN":
			next = "NEEDS_HUMAN"
		case "REVIEW_BLOCKED", "BUILDER_BLOCKED":
			next = "BLOCKED"
		}
		run, runErr := q.GetClearDevComplexExecutionRun(ctx, a.ExecutionRunID)
		if runErr != nil {
			return runErr
		}
		boundedMail := cleardev.BoundedMailAttempts(complexExecutionRunFromGen(run))
		boundedCheck, boundedErr := stoppedCheckFailureIsBounded(ctx, q, a)
		if boundedErr != nil {
			return boundedErr
		}
		if boundedCheck {
			// Human recovery of one existing candidate grants no new repair
			// round. Preserve the true check/review reason and spent counter.
			if next == "REWORK" {
				next = "BLOCKED"
			}
		} else if infrastructure {
			next = "BLOCKED"
		} else if next == "REWORK" && a.Status == string(cleardev.ComplexExecutionDispatchRunning) {
			// A RUNNING attempt has no observed candidate to use as the durable
			// hand-off into a rework round. Stop for human recovery instead of
			// leaving an impossible RUNNING -> REWORK transition.
			next = "NEEDS_HUMAN"
		} else if next == "REWORK" && a.Round >= 1 {
			// An observed candidate hands off into the next rework round while
			// the task's frozen rework budget covers it. Exhaustion stops the
			// run with the recoverable budget-exhausted block instead of a
			// business stop, so the person's controlled recovery reopens
			// exactly this round. Bounded mail runs keep their own slots.
			if boundedMail {
				next = "NEEDS_HUMAN"
			} else {
				budgetMax := int64(0)
				budgets, budgetErr := q.ListClearDevComplexExceptionBudgets(ctx, a.ExecutionRunID)
				if budgetErr != nil {
					return budgetErr
				}
				for _, budget := range budgets {
					if budget.RoleKind == "BUILDER" && budget.ComplexExecutionTaskID.Valid && budget.ComplexExecutionTaskID.String == a.TaskMappingID {
						budgetMax = budget.MaxReworkCount + budget.AuthorizedExtraTurns
					}
				}
				if a.Round >= budgetMax {
					next, reason = "BLOCKED", "BUILDER_BUDGET_EXHAUSTED"
				}
			}
		}
		next, reason, e = boundedMailFailure(ctx, q, a, infrastructure, reason, next)
		if e != nil {
			return e
		}
		candidate, candidateErr := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(a.ID))
		if candidateErr != nil && !errors.Is(candidateErr, sql.ErrNoRows) {
			return candidateErr
		}
		if candidateErr == nil {
			bindings, listErr := q.ListClearDevComplexExecutionRoleBindings(ctx, runID)
			if listErr != nil {
				return listErr
			}
			for _, binding := range bindings {
				if binding.Role != string(cleardev.StandardRoleReviewer) || !binding.TaskMappingID.Valid || binding.TaskMappingID.String != taskID || !binding.CandidateCommitID.Valid || binding.CandidateCommitID.String != candidate.ID {
					continue
				}
				switch binding.Status {
				case string(cleardev.RoleBindingStatusRequested):
					if _, e = q.FailClearDevComplexExecutionRoleBindingCAS(ctx, gen.FailClearDevComplexExecutionRoleBindingCASParams{ID: binding.ID, ReasonCode: string(reason), EndedAt: nullableTime(at)}); e != nil {
						return e
					}
				case string(cleardev.RoleBindingStatusBound):
					if _, e = q.EndClearDevComplexExecutionRoleBindingCAS(ctx, gen.EndClearDevComplexExecutionRoleBindingCASParams{ID: binding.ID, ReasonCode: string(reason), EndedAt: nullableTime(at)}); e != nil {
						return e
					}
				}
			}
		}
		reviews, e := q.ListClearDevComplexExecutionReviews(ctx, runID)
		if e != nil {
			return e
		}
		for _, review := range reviews {
			if review.TaskAttemptID != dispatchID || review.Status != "PENDING" {
				continue
			}
			recoveryBinding, replacementErr := mailReplacementRecoveryBinding(ctx, q, review)
			if replacementErr != nil {
				return replacementErr
			}
			if _, e = q.FailClearDevComplexExecutionAgentStepCAS(ctx, gen.FailClearDevComplexExecutionAgentStepCASParams{ID: cleardev.ReviewCheckFollowupID(review.ID), ReasonCode: string(reason), FailedAt: nullableTime(at)}); e != nil {
				return e
			}
			if recoveryBinding != "" {
				// Preserve original provider history. Close only the role that
				// already performed this replacement, in the same transaction,
				// so the stopped dispatch cannot still appear to be recovering.
				if _, e = q.EndClearDevComplexExceptionOnDemandCAS(ctx, gen.EndClearDevComplexExceptionOnDemandCASParams{ID: recoveryBinding, ReasonCode: string(reason), EndedAt: nullableTime(at)}); e != nil {
					return e
				}
				continue
			}
			if _, e = q.FailClearDevComplexExecutionAgentStepCAS(ctx, gen.FailClearDevComplexExecutionAgentStepCASParams{ID: review.AgentStepID, ReasonCode: string(reason), FailedAt: nullableTime(at)}); e != nil {
				return e
			}
			if _, e = q.FailClearDevComplexExecutionReviewCAS(ctx, gen.FailClearDevComplexExecutionReviewCASParams{ID: review.ID, ReasonCode: string(reason), SettledAt: nullableTime(at)}); e != nil {
				return e
			}
			if _, e = q.EndClearDevComplexExecutionRoleBindingCAS(ctx, gen.EndClearDevComplexExecutionRoleBindingCASParams{ID: review.ReviewerRoleBindingID, ReasonCode: string(reason), EndedAt: nullableTime(at)}); e != nil {
				return e
			}
		}
		if _, e = q.AdvanceClearDevComplexExecutionTaskAttemptCAS(ctx, gen.AdvanceClearDevComplexExecutionTaskAttemptCASParams{ID: a.ID, ExpectedStatus: a.Status, Status: next, ReasonCode: string(reason), SettledAt: nullableTime(at)}); e != nil {
			return e
		}
		item, e := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, taskID)
		if e != nil {
			return e
		}
		state := cleardev.DevelopmentTaskStatusRework
		switch next {
		case "BLOCKED":
			state = cleardev.DevelopmentTaskStatusBlocked
		case "NEEDS_HUMAN":
			state = cleardev.DevelopmentTaskStatusNeedsHuman
		}
		reworkCount := item.ReworkCount
		if state == cleardev.DevelopmentTaskStatusRework {
			reworkCount++
		}
		pausedFrom := sql.NullString{}
		if state == cleardev.DevelopmentTaskStatusBlocked || state == cleardev.DevelopmentTaskStatusNeedsHuman {
			pausedFrom = item.PausedFromState
			if !pausedFrom.Valid {
				pausedFrom = nullableString(item.State)
			}
		}
		rows, e := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount, NextState: string(state), PausedFromState: pausedFrom, ReworkCount: reworkCount, UpdatedAt: at})
		if e != nil {
			return e
		}
		if rows != 1 {
			return complexExecutionRule("failure task state changed concurrently")
		}
		if reason == cleardev.MailAttemptLimitReason {
			if err := ensureMailExtraRequest(ctx, q, runID, taskID, dispatchID, at); err != nil {
				return err
			}
		}
		return releaseComplexExceptionPathLeases(ctx, q, runID, taskID, at)
	})
}

// VerifyClearDevComplexExecutionCandidate records a verified candidate and its evidence.
func (s *Store) VerifyClearDevComplexExecutionCandidate(ctx context.Context, c cleardev.VerifyComplexExecutionCandidateCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "verify ClearDev complex candidate", func(q *gen.Queries) error {
		v := c.Verification
		a, e := q.GetClearDevComplexExecutionTaskAttempt(ctx, v.DispatchID)
		if e != nil {
			return e
		}
		if a.ExecutionRunID != v.ExecutionRunID || a.TaskMappingID != v.ComplexExecutionTaskID || a.Round != int64(v.Round) {
			return complexExecutionRule("verification does not match dispatch")
		}
		if err := validateProjectCandidateVerification(ctx, q, v); err != nil {
			return err
		}
		raw, e := json.Marshal(v.RequiredCheckRunIDs)
		if e != nil {
			return e
		}
		if e := q.InsertClearDevComplexExecutionVerifiedCandidate(ctx, gen.InsertClearDevComplexExecutionVerifiedCandidateParams{ID: v.ID, TaskMappingID: v.ComplexExecutionTaskID, TaskAttemptID: v.DispatchID, CandidateCommitID: v.CandidateCommitID, ScopeCheckRunID: v.ScopeEvidenceID, RequiredCheckRunsJson: string(raw), ReviewID: v.LocalReviewID, ReplacementRecoveryID: nullableString(v.ReplacementRecoveryID), ReplacementAttemptID: nullableString(v.ReplacementAttemptID), ReplacementResultID: nullableString(v.ReplacementResultID), VerifiedAt: v.VerifiedAt}); e != nil {
			return e
		}
		_, e = q.AdvanceClearDevComplexExecutionTaskAttemptCAS(ctx, gen.AdvanceClearDevComplexExecutionTaskAttemptCASParams{ID: a.ID, ExpectedStatus: "REVIEWING", Status: "VERIFIED", ReasonCode: "", SettledAt: nullableTime(v.VerifiedAt)})
		if e != nil {
			return e
		}
		run, e := q.GetClearDevComplexExecutionRun(ctx, v.ExecutionRunID)
		if e != nil {
			return e
		}
		requirement, e := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
		if e != nil {
			return e
		}
		if e := insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectComplexExecutionVerification, v.ID, cleardev.ActionVerifyComplexCandidate, cleardev.EventAccepted, cleardev.ReasonNone, "", v.VerifiedAt); e != nil {
			return e
		}
		return releaseComplexExceptionPathLeases(ctx, q, v.ExecutionRunID, v.ComplexExecutionTaskID, v.VerifiedAt)
	})
}

// CompleteClearDevComplexExecution atomically records the final integration result.
func (s *Store) CompleteClearDevComplexExecution(ctx context.Context, c cleardev.CompleteComplexExecutionCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "complete ClearDev complex execution", func(q *gen.Queries) error {
		run, e := q.GetClearDevComplexExecutionRun(ctx, c.ExecutionRunID)
		if e != nil {
			return e
		}
		if err := validateProjectCompletion(ctx, q, run, c.Integration); err != nil {
			return err
		}
		if run.Status == "COMPLETED" {
			if err := validateRequirementFinalReviewCompletion(ctx, q, run, c.Integration); err != nil {
				return err
			}
			return validateMailCompletionReplay(ctx, q, run, c.Integration)
		}
		if run.Status != "ACCEPTED" {
			return complexExecutionRule("execution is not accepted")
		}
		version, e := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, run.DevelopmentProjectID)
		if e != nil {
			return e
		}
		if version.ID != run.RequirementVersionID || version.State != "APPROVED" || version.SupersededByID.Valid || version.Sha256 != run.RequirementSha256 || version.TaskSetVersion != run.AcceptedTaskSetVersion {
			return complexExecutionRule("execution no longer owns the current approved task set")
		}
		if stopped, e := activeDirectionStop(ctx, q, run.RequirementVersionID); e != nil {
			return e
		} else if stopped {
			return directionStoppedError()
		}
		verified, e := q.ListClearDevComplexExecutionVerifiedCandidates(ctx, run.ID)
		if e != nil {
			return e
		}
		tasks, e := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
		if e != nil {
			return e
		}
		latestByTask := map[string]string{}
		for _, row := range verified {
			latestByTask[row.TaskMappingID] = row.ID
		}
		if len(tasks) == 0 || len(latestByTask) != len(tasks) {
			return complexExecutionRule("all tasks must be verified")
		}
		finalMapping := tasks[len(tasks)-1]
		var last gen.CleardevComplexExecutionVerifiedCandidate
		foundVerified := false
		for _, row := range verified {
			if row.TaskMappingID == finalMapping.ID {
				last = row
				foundVerified = true
			}
		}
		if !foundVerified {
			return complexExecutionRule("all tasks must be verified")
		}
		candidate, e := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(last.TaskAttemptID))
		if e != nil {
			return e
		}
		builder, e := complexExecutionBoundBuilderForRun(ctx, q, run)
		if e != nil {
			return e
		}
		integrationSHA := c.Integration.CandidateCommitSHA
		if run.Mode == "PARALLEL" {
			composition, e := finalParallelComposition(ctx, q, run.ID)
			if e != nil {
				return e
			}
			if composition.Status != "COMPOSED" || composition.OutputCommitSha == "" || composition.OutputCommitSha != integrationSHA {
				return complexExecutionRule("integration must use the final composed commit")
			}
		} else if integrationSHA != candidate.CommitSha {
			return complexExecutionRule("integration must use the final verified candidate")
		}
		if e := validateMailCompletion(ctx, q, run, last, c.Integration); e != nil {
			return e
		}
		if e := validateRequirementFinalReviewCompletion(ctx, q, run, c.Integration); e != nil {
			return e
		}
		resultID := c.Integration.ID
		if resultID == "" {
			return complexExecutionRule("integration id required")
		}
		if e := q.InsertClearDevComplexExecutionResult(ctx, gen.InsertClearDevComplexExecutionResultParams{ID: resultID, ExecutionRunID: run.ID, IntegrationCandidateID: c.Integration.IntegrationCandidateID, CompletionStatus: "PENDING", CreatedAt: c.At}); e != nil {
			return e
		}
		sequence, e := q.NextClearDevIntegrationCandidateSequence(ctx, run.DevelopmentProjectID)
		if e != nil {
			return e
		}
		if e := q.InsertClearDevComplexExecutionIntegrationCandidate(ctx, gen.InsertClearDevComplexExecutionIntegrationCandidateParams{ID: c.Integration.IntegrationCandidateID, DevelopmentProjectID: run.DevelopmentProjectID, Sequence: sequence, AoSessionID: builder.AOSessionID, CommitSha: integrationSHA, RequirementVersionID: nullableString(run.RequirementVersionID), TaskSetVersion: sql.NullInt64{Int64: run.AcceptedTaskSetVersion, Valid: true}, ComplexExecutionResultID: nullableString(resultID), ComplexSourceCandidateCommitID: nullableString(candidate.ID), CreatedAt: c.At}); e != nil {
			return e
		}
		for _, id := range c.Integration.CheckRunIDs {
			if e := q.InsertClearDevComplexExecutionResultCheck(ctx, gen.InsertClearDevComplexExecutionResultCheckParams{ResultID: resultID, CheckRunID: id}); e != nil {
				return e
			}
		}
		if len(c.Integration.CheckRunIDs) == 0 {
			return complexExecutionRule("integration requires a passing fixed check")
		}
		evidenceID := resultID + ":integration-evidence"
		if e := q.InsertClearDevComplexExecutionEvidence(ctx, gen.InsertClearDevComplexExecutionEvidenceParams{
			ID: evidenceID, DevelopmentProjectID: run.DevelopmentProjectID,
			SubjectType: physicalEvidenceSubject(cleardev.SubjectDevelopmentRequirement), SubjectID: run.DevelopmentProjectID,
			EvidenceKind: string(cleardev.EvidenceKindIntegration), EvidenceKey: "", Result: string(cleardev.EvidenceResultPass),
			IntegrationCandidateID: nullableString(c.Integration.IntegrationCandidateID), CommitSha: integrationSHA,
			SourceType: string(cleardev.EvidenceSourceControlPlaneChecker), ComplexExecutionCheckRunID: nullableString(c.Integration.CheckRunIDs[0]), CreatedAt: c.At,
		}); e != nil {
			return e
		}
		rows, e := q.StartClearDevComplexExecutionCompletionCAS(ctx, gen.StartClearDevComplexExecutionCompletionCASParams{ID: resultID, CommittingAt: nullableTime(c.At)})
		if e != nil {
			return e
		}
		if rows != 1 {
			return complexExecutionRule("atomic completion lost its compare-and-swap")
		}
		requirement, e := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
		if e != nil {
			return e
		}
		if e := insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectEvidence, evidenceID, cleardev.ActionRecordEvidence, cleardev.EventAccepted, cleardev.ReasonNone, "", c.At); e != nil {
			return e
		}
		return insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectComplexExecutionIntegration, c.Integration.ID, cleardev.ActionCompleteComplexExecution, cleardev.EventAccepted, cleardev.ReasonNone, builder.AOSessionID, c.At)
	})
}
