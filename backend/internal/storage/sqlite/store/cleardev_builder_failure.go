package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func projectBlockedFinalReview(ctx context.Context, q *gen.Queries, review gen.CleardevRequirementFinalReview) bool {
	if review.Verdict.String != "BLOCKED" {
		return false
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, review.ExecutionRunID)
	if err != nil || run.Mode != "STANDARD" || run.Status != "ACCEPTED" {
		return false
	}
	requirement, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
	if err != nil || requirement.PausedFromState.Valid || requirement.CancelledAt.Valid {
		return false
	}
	_, project, err := core.ProjectContractFromRun(complexExecutionRunFromGen(run))
	return err == nil && project
}

// BuilderFirstFailureCurrent reads the persisted stop and version facts before
// continuing an automatically returned repair. It does not create authority.
func (s *Store) BuilderFirstFailureCurrent(ctx context.Context, runID string) (bool, error) {
	run, err := s.qr.GetClearDevComplexExecutionRun(ctx, runID)
	if err != nil {
		return false, err
	}
	return builderFirstFailureCurrent(ctx, s.qr, run)
}

func builderFirstFailureCurrent(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun) (bool, error) {
	_, project, contractErr := core.ProjectContractFromRun(complexExecutionRunFromGen(run))
	if (run.Status != "ACCEPTED" && run.Status != "PENDING") || contractErr != nil || !project {
		return false, nil
	}
	requirement, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
	if err != nil {
		return false, err
	}
	if requirement.PausedFromState.Valid || requirement.CancelledAt.Valid {
		return false, nil
	}
	version, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, run.DevelopmentProjectID)
	if err != nil {
		return false, err
	}
	tasks, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return false, err
	}
	expectedTaskSet := run.ExpectedTaskSetVersion
	if len(tasks) > 0 {
		if run.Status != "ACCEPTED" {
			return false, nil
		}
		expectedTaskSet = run.AcceptedTaskSetVersion
	}
	return version.ID == run.RequirementVersionID && version.Sha256 == run.RequirementSha256 && !version.SupersededByID.Valid && version.TaskSetVersion == expectedTaskSet, nil
}

// ReturnClearDevFinalFailureToBuilder spends an existing task rework round once
// for the exact durable final-review result. The original review is immutable.
func (s *Store) ReturnClearDevFinalFailureToBuilder(ctx context.Context, reviewID string, at time.Time) (changed bool, retErr error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	retErr = s.inTx(ctx, "return final failure to original Builder", func(q *gen.Queries) error {
		row, err := q.GetClearDevRequirementFinalReviewByID(ctx, reviewID)
		if err != nil {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, row.ExecutionRunID)
		if err != nil {
			return err
		}
		if !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) || row.Status != "SETTLED" || (row.Verdict.String != "BLOCKED" && row.Verdict.String != "REWORK") {
			return complexExecutionRule("failure has no frozen Builder-first grant")
		}
		latest, err := q.GetClearDevRequirementFinalReview(ctx, run.ID)
		if err != nil {
			return err
		}
		if latest.ID != row.ID {
			return complexExecutionRule("failure review is no longer current")
		}
		marker := "builder-first-final:" + row.ID
		events, err := q.ListClearDevRequirementEvents(ctx, run.DevelopmentProjectID)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.ReasonText == marker && event.Outcome == "ACCEPTED" {
				return nil
			}
		}
		current, err := builderFirstFailureCurrent(ctx, q, run)
		if err != nil {
			return err
		}
		if !current {
			return complexExecutionRule("Builder diagnosis requirement is stopped")
		}
		review, err := requirementFinalReviewFromGen(row)
		if err != nil {
			return err
		}
		if err := validateFinalReviewPrerequisites(ctx, q, run, review); err != nil {
			return err
		}
		tasks, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			return complexExecutionRule("failure has no task")
		}
		task := tasks[len(tasks)-1]
		item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, task.ID)
		if err != nil {
			return err
		}
		var dispatchID string
		for _, verification := range reviewPacketVerifications(review) {
			if verification.ComplexExecutionTaskID == task.ID {
				dispatchID = verification.DispatchID
			}
		}
		attempt, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, dispatchID)
		if err != nil {
			return err
		}
		binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, attempt.BuilderRoleBindingID)
		if err != nil {
			return err
		}
		session, err := q.GetSession(ctx, domain.SessionID(binding.AoSessionID.String))
		if err != nil {
			return err
		}
		if binding.Status != "BOUND" || session.IsTerminated || session.ActivityState != domain.ActivityIdle || session.WorkspacePath != row.SourceWorkspacePath {
			return complexExecutionRule("original Builder identity or activity changed")
		}
		if item.State != "REVIEW" || item.ReworkCount >= item.MaxReworkCount {
			return complexExecutionRule("Builder repair state or budget unavailable")
		}
		if available, err := builderRepairBudgetAvailable(ctx, q, run.ID, task.ID, item.ReworkCount+1); err != nil {
			return err
		} else if !available {
			return complexExecutionRule("Builder repair message budget unavailable")
		}
		for _, action := range []core.Action{core.ActionRequestDevelopmentTaskRework, core.ActionRestartDevelopmentTask} {
			rule, err := applyClearDevDevelopmentTaskAction(ctx, q, core.ActionRequest{Action: action, SubjectID: item.ID, Source: core.EventSourceControlPlane, ReasonText: marker, At: at})
			if err != nil {
				return err
			}
			if rule != nil {
				return rule
			}
		}
		changed = true
		return nil
	})
	return changed, retErr
}

func reviewPacketVerifications(review core.RequirementFinalReview) []core.ComplexExecutionVerification {
	var packet core.RequirementFinalReviewPacket
	if json.Unmarshal([]byte(review.ReviewPacketJSON), &packet) != nil {
		return nil
	}
	return packet.Verifications
}

// ReturnClearDevIntegrationFailureToBuilder registers one repair for an exact
// terminal integration check, retaining its verification and failure history.
func (s *Store) ReturnClearDevIntegrationFailureToBuilder(ctx context.Context, checkID string, at time.Time) (changed bool, retErr error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	retErr = s.inTx(ctx, "return integration failure to original Builder", func(q *gen.Queries) error {
		check, err := q.GetClearDevComplexExecutionCheckRun(ctx, checkID)
		if err != nil {
			return err
		}
		spec, err := q.GetClearDevProjectCheckSpec(ctx, check.CheckSpecID)
		if err != nil {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, spec.ExecutionRunID)
		if err != nil {
			return err
		}
		if !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) || run.Status != "ACCEPTED" || spec.CheckKind != string(core.CandidateCheckIntegration) || !check.SettledAt.Valid || (check.Status != "FAILED" && (check.Status != "SETTLED" || check.Result.String != "FAIL")) {
			return complexExecutionRule("check is not a terminal project integration failure")
		}
		marker := "builder-first-check:" + check.ID
		events, err := q.ListClearDevRequirementEvents(ctx, run.DevelopmentProjectID)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.ReasonText == marker && event.Outcome == "ACCEPTED" {
				return nil
			}
		}
		current, err := builderFirstFailureCurrent(ctx, q, run)
		if err != nil {
			return err
		}
		if !current {
			return complexExecutionRule("Builder diagnosis requirement is stopped")
		}
		if stopped, err := activeDirectionStop(ctx, q, run.RequirementVersionID); err != nil {
			return err
		} else if stopped {
			return directionStoppedError()
		}
		tasks, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			return complexExecutionRule("check failure has no current task")
		}
		attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, run.ID)
		if err != nil {
			return err
		}
		latest := make(map[string]gen.CleardevComplexExecutionTaskAttempt)
		for _, attempt := range attempts {
			previous, exists := latest[attempt.TaskMappingID]
			if !exists || attempt.Round > previous.Round {
				latest[attempt.TaskMappingID] = attempt
			}
		}
		verified, err := q.ListClearDevComplexExecutionVerifiedCandidates(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			attempt, exists := latest[task.ID]
			if !exists || attempt.Status != "VERIFIED" {
				return complexExecutionRule("integration task is no longer verified")
			}
			present := false
			for _, v := range verified {
				present = present || v.TaskMappingID == task.ID && v.TaskAttemptID == attempt.ID
			}
			if !present {
				return complexExecutionRule("integration task verification missing")
			}
		}
		task := tasks[len(tasks)-1]
		attempt := latest[task.ID]
		candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(attempt.ID))
		if err != nil {
			return err
		}
		if check.TaskAttemptID != attempt.ID || check.CandidateCommitID != candidate.ID || check.CandidateCommitSha != candidate.CommitSha {
			return complexExecutionRule("integration failure candidate is no longer current")
		}
		checks, err := q.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, newer := range checks {
			if newer.TaskAttemptID != attempt.ID {
				continue
			}
			if newer.CheckSpecID == check.CheckSpecID && newer.ID != check.ID && (newer.RetryOrdinal > check.RetryOrdinal || newer.CreatedAt.After(check.CreatedAt)) {
				return complexExecutionRule("integration failure check was replaced")
			}
			if newer.Status == "STARTED" || newer.Status == "PENDING" {
				return complexExecutionRule("integration has unsettled check work")
			}
		}
		binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, attempt.BuilderRoleBindingID)
		if err != nil {
			return err
		}
		session, err := q.GetSession(ctx, domain.SessionID(binding.AoSessionID.String))
		if err != nil {
			return err
		}
		if binding.Status != "BOUND" || binding.Role != "BUILDER" || session.IsTerminated || session.ActivityState != domain.ActivityIdle || session.WorkspacePath != binding.WorkspacePath {
			return complexExecutionRule("original Builder identity or activity changed")
		}
		item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, task.ID)
		if err != nil {
			return err
		}
		if item.State != "REVIEW" || item.ReworkCount != attempt.Round || item.ReworkCount >= item.MaxReworkCount {
			return complexExecutionRule("Builder repair state or budget unavailable")
		}
		if available, err := builderRepairBudgetAvailable(ctx, q, run.ID, task.ID, item.ReworkCount+1); err != nil {
			return err
		} else if !available {
			return complexExecutionRule("Builder repair message budget unavailable")
		}
		for _, action := range []core.Action{core.ActionRequestDevelopmentTaskRework, core.ActionRestartDevelopmentTask} {
			rule, err := applyClearDevDevelopmentTaskAction(ctx, q, core.ActionRequest{Action: action, SubjectID: item.ID, Source: core.EventSourceControlPlane, ReasonText: marker, At: at})
			if err != nil {
				return err
			}
			if rule != nil {
				return rule
			}
		}
		changed = true
		return nil
	})
	return changed, retErr
}

func projectCheckFailureEvidence(ctx context.Context, q *gen.Queries, task core.DevelopmentTask, candidateSHA string) (bool, error) {
	runs, err := q.ListClearDevComplexExecutionRuns(ctx, task.DevelopmentRequirementID)
	if err != nil || len(runs) == 0 {
		return false, err
	}
	run := runs[len(runs)-1]
	if run.Status != "ACCEPTED" || !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) || run.RequirementVersionID != task.RequirementVersionID {
		return false, nil
	}
	tasks, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil || len(tasks) == 0 {
		return false, err
	}
	var currentTask gen.CleardevComplexExecutionTaskMapping
	for _, mapping := range tasks {
		if mapping.WorkItemID == task.ID {
			currentTask = mapping
		}
	}
	if currentTask.ID == "" {
		return false, nil
	}
	attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, run.ID)
	if err != nil {
		return false, err
	}
	var latest gen.CleardevComplexExecutionTaskAttempt
	for _, attempt := range attempts {
		if attempt.TaskMappingID == currentTask.ID && (latest.ID == "" || attempt.Round > latest.Round) {
			latest = attempt
		}
	}
	if task.ReworkCount != int(latest.Round) || latest.Status != "VERIFIED" && latest.Status != "BLOCKED" {
		return false, nil
	}
	candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(latest.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if candidate.CommitSha != candidateSHA {
		return false, nil
	}
	checks, err := q.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
	if err != nil {
		return false, err
	}
	latestChecks := make(map[string]gen.CleardevComplexExecutionCheckRun)
	for _, check := range checks {
		if check.TaskAttemptID != latest.ID || check.CandidateCommitID != candidate.ID {
			continue
		}
		previous, found := latestChecks[check.CheckSpecID]
		if !found || check.RetryOrdinal > previous.RetryOrdinal || check.RetryOrdinal == previous.RetryOrdinal && check.CreatedAt.After(previous.CreatedAt) {
			latestChecks[check.CheckSpecID] = check
		}
	}
	for _, check := range latestChecks {
		if !check.SettledAt.Valid || check.Status != "FAILED" && (check.Status != "SETTLED" || check.Result.String != "FAIL") {
			continue
		}
		spec, err := q.GetClearDevProjectCheckSpec(ctx, check.CheckSpecID)
		if err != nil {
			return false, err
		}
		if spec.CheckKind == string(core.CandidateCheckIntegration) && latest.Status == "VERIFIED" && currentTask.ID == tasks[len(tasks)-1].ID || spec.CheckKind == string(core.CandidateCheckRequired) && latest.Status == "BLOCKED" && check.ReasonCode == latest.ReasonCode {
			return true, nil
		}
	}
	return false, nil
}

// A historical stopped attempt stays immutable; the existing task events
// prove that its exact failed check has been returned into the next round.
func builderFirstRequiredCheckReturned(ctx context.Context, q *gen.Queries, attempt gen.CleardevComplexExecutionTaskAttempt) (bool, error) {
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, attempt.TaskMappingID)
	if err != nil || item.State != "RUNNING" || item.ReworkCount != attempt.Round+1 {
		return false, err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, attempt.ExecutionRunID)
	if err != nil {
		return false, err
	}
	events, err := q.ListClearDevRequirementEvents(ctx, run.DevelopmentProjectID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		checkID, marked := strings.CutPrefix(event.ReasonText, "builder-first-check:")
		if !marked || event.SubjectID != item.ID || event.Action != string(core.ActionRestartDevelopmentTask) || event.Outcome != "ACCEPTED" {
			continue
		}
		check, err := q.GetClearDevComplexExecutionCheckRun(ctx, checkID)
		if err != nil {
			return false, err
		}
		if check.TaskAttemptID == attempt.ID {
			return true, nil
		}
	}
	return false, nil
}

// ReturnClearDevRequiredFailureToBuilder reuses task resume/rework/restart,
// keeping the old stopped attempt, candidate, delivery and failed check intact.
func (s *Store) ReturnClearDevRequiredFailureToBuilder(ctx context.Context, checkID string, at time.Time) (changed bool, retErr error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	retErr = s.inTx(ctx, "return historical required failure to original Builder", func(q *gen.Queries) error {
		check, err := q.GetClearDevComplexExecutionCheckRun(ctx, checkID)
		if err != nil {
			return err
		}
		spec, err := q.GetClearDevProjectCheckSpec(ctx, check.CheckSpecID)
		if err != nil {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, spec.ExecutionRunID)
		if err != nil {
			return err
		}
		if !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) || run.Status != "ACCEPTED" || spec.CheckKind != string(core.CandidateCheckRequired) || !check.SettledAt.Valid || (check.Status != "FAILED" && (check.Status != "SETTLED" || check.Result.String != "FAIL")) {
			return complexExecutionRule("check is not a terminal project required failure")
		}
		marker := "builder-first-check:" + check.ID
		events, err := q.ListClearDevRequirementEvents(ctx, run.DevelopmentProjectID)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.ReasonText == marker && event.Outcome == "ACCEPTED" {
				return nil
			}
		}
		current, err := builderFirstFailureCurrent(ctx, q, run)
		if err != nil {
			return err
		}
		if !current {
			return complexExecutionRule("Builder diagnosis requirement is stopped")
		}
		if stopped, err := activeDirectionStop(ctx, q, run.RequirementVersionID); err != nil {
			return err
		} else if stopped {
			return directionStoppedError()
		}
		attempt, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, check.TaskAttemptID)
		if err != nil {
			return err
		}
		if attempt.ExecutionRunID != run.ID || attempt.Status != "BLOCKED" || !attempt.SettledAt.Valid || attempt.ReasonCode != check.ReasonCode || (attempt.ReasonCode != "CHECKER_UNAVAILABLE" && attempt.ReasonCode != "CHECK_FAILED" && attempt.ReasonCode != "CHECK_TIMEOUT") {
			return complexExecutionRule("required check is not the known stopped attempt")
		}
		attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, newer := range attempts {
			if newer.TaskMappingID == attempt.TaskMappingID && newer.Round > attempt.Round {
				return complexExecutionRule("required failure attempt was replaced")
			}
		}
		step, err := q.GetClearDevComplexExecutionAgentStep(ctx, attempt.AgentStepID)
		if err != nil {
			return err
		}
		if step.SendStatus != "SETTLED" || !step.TurnID.Valid {
			return complexExecutionRule("original Builder delivery is unsettled")
		}
		candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(attempt.ID))
		if err != nil {
			return err
		}
		if candidate.ID != check.CandidateCommitID || candidate.CommitSha != check.CandidateCommitSha {
			return complexExecutionRule("required failure candidate changed")
		}
		checks, err := q.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, newer := range checks {
			if newer.TaskAttemptID != attempt.ID {
				continue
			}
			if newer.Status == "PENDING" || newer.Status == "STARTED" || newer.CheckSpecID == check.CheckSpecID && newer.ID != check.ID && (newer.RetryOrdinal > check.RetryOrdinal || newer.CreatedAt.After(check.CreatedAt)) {
				return complexExecutionRule("required check work is unsettled or replaced")
			}
		}
		binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, attempt.BuilderRoleBindingID)
		if err != nil {
			return err
		}
		session, err := q.GetSession(ctx, domain.SessionID(binding.AoSessionID.String))
		if err != nil {
			return err
		}
		if binding.Status != "BOUND" || binding.Role != "BUILDER" || session.IsTerminated || session.ActivityState != domain.ActivityIdle || session.WorkspacePath != binding.WorkspacePath {
			return complexExecutionRule("original Builder identity or activity changed")
		}
		item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, attempt.TaskMappingID)
		if err != nil {
			return err
		}
		if item.State != "BLOCKED" || !item.PausedFromState.Valid || (item.PausedFromState.String != "REVIEW" && item.PausedFromState.String != "RUNNING") || item.ReworkCount != attempt.Round || item.ReworkCount >= item.MaxReworkCount {
			return complexExecutionRule("Builder repair state or budget unavailable")
		}
		if available, err := builderRepairBudgetAvailable(ctx, q, run.ID, attempt.TaskMappingID, item.ReworkCount+1); err != nil {
			return err
		} else if !available {
			return complexExecutionRule("Builder repair message budget unavailable")
		}
		actions := []core.Action{core.ActionResumeDevelopmentTask}
		if item.PausedFromState.String == "RUNNING" {
			actions = append(actions, core.ActionSubmitDevelopmentTaskReview)
		}
		actions = append(actions, core.ActionRequestDevelopmentTaskRework, core.ActionRestartDevelopmentTask)
		for _, action := range actions {
			rule, err := applyClearDevDevelopmentTaskAction(ctx, q, core.ActionRequest{Action: action, SubjectID: item.ID, Source: core.EventSourceControlPlane, ReasonText: marker, At: at})
			if err != nil {
				return err
			}
			if rule != nil {
				return rule
			}
		}
		changed = true
		return nil
	})
	return changed, retErr
}

func builderRepairBudgetAvailable(ctx context.Context, q *gen.Queries, runID, taskID string, nextRound int64) (bool, error) {
	budgets, err := q.ListClearDevComplexExceptionBudgets(ctx, runID)
	if err != nil {
		return false, err
	}
	for _, budget := range budgets {
		if budget.RoleKind == "BUILDER" && budget.ComplexExecutionTaskID.String == taskID && budget.UsedTurns < budget.MaxTurns+budget.AuthorizedExtraTurns && nextRound <= budget.MaxReworkCount+budget.AuthorizedExtraTurns {
			return true, nil
		}
	}
	return false, nil
}
