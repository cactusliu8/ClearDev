package cleardev

import (
	"strconv"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func workflowDiagnosisOptionWork(view RequirementView, option core.WorkflowRecoveryOption, replacement *core.BuilderReplacementState) (core.ControlledWork, *core.ComplexExecutionCheckRun) {
	work := core.ControlledWork{Role: option.Role, TaskMappingID: option.TaskID}
	if e := view.ComplexExecution; e != nil {
		work.ExecutionRunID = e.Run.ID
		for _, task := range e.Tasks {
			if task.ID != option.TaskID {
				continue
			}
			for _, dispatch := range e.Dispatches {
				if dispatch.ID != task.CurrentDispatchID {
					continue
				}
				work.DevelopmentTaskID, work.DispatchID = task.DevelopmentTaskID, dispatch.ID
				if option.Role == "BUILDER" {
					matches := option.Action == core.RecoveryContinueBuilder && option.TargetID == dispatch.ID
					if option.Action == core.RecoveryRetryBuilderSession && dispatch.SettledAt != nil {
						matches = option.TargetID == core.WorkflowBuilderRetryTarget(dispatch.ID, *dispatch.SettledAt)
					}
					// Registration reopens this dispatch and clears its stopped time.
					// Bind replay to the saved request and exact original step instead.
					if option.Action == core.RecoveryRetryBuilderSession && option.UnavailableReason == "BUILDER_RECHECK_REGISTERED" {
						if registered := builderSessionRetryForDispatch(*e, dispatch); registered != nil {
							matches = registered.ExecutionRunID == e.Run.ID && registered.TargetID == option.TargetID
						}
					}
					if option.Action == core.RecoveryRequestBuilderReplacement || option.Action == core.RecoveryContinueBuilderReplacement {
						matches = replacement != nil && replacement.Execution.Run.ID == e.Run.ID && replacement.Option.Action == option.Action && replacement.Option.TargetID == option.TargetID && replacement.Task.ID == task.ID && replacement.Dispatch.ID == dispatch.ID && replacement.Step.ID == dispatch.AgentStepID && replacement.OldBinding.ID == dispatch.BuilderRoleBindingID
					}
					if !matches {
						return core.ControlledWork{Role: option.Role}, nil
					}
					work.LogicalStepID, work.RoleBindingID = dispatch.AgentStepID, dispatch.BuilderRoleBindingID
					if option.Action == core.RecoveryContinueBuilderReplacement && replacement != nil && replacement.Handoff != nil && replacement.Handoff.NewAOSessionID != "" {
						h := replacement.Handoff
						for _, proof := range e.BuilderReplacementTransitions {
							if proof.AliasBound && proof.HandoffID == h.ID && proof.DispatchID == dispatch.ID && proof.LogicalStepID == dispatch.AgentStepID && proof.OldRoleBindingID == dispatch.BuilderRoleBindingID && proof.NewRoleBindingID == h.Binding.NewRoleBindingID && proof.NewAOSessionID == h.NewAOSessionID {
								work.RoleBindingID = proof.NewRoleBindingID
							}
						}
					}
				}
			}
		}
		for _, review := range e.Reviews {
			if option.Role == "REVIEWER" && review.ID == option.TargetID {
				work.LogicalStepID, work.RoleBindingID = review.AgentStepID, review.ReviewerRoleBindingID
			}
		}
		if option.Role == "REVIEWER" && work.RoleBindingID == "" {
			for _, binding := range e.RoleBindings {
				if binding.ID == option.TargetID && string(binding.Role) == option.Role {
					work.RoleBindingID = binding.ID
				}
			}
		}
		for _, binding := range e.RoleBindings {
			if binding.ID == work.RoleBindingID {
				work.AOSessionID = binding.AOSessionID
			}
		}
		if final := e.FinalReview; final != nil && final.ID == option.TargetID && option.Action == core.RecoveryRetryStage {
			work.AOSessionID, work.LogicalStepID = final.AOSessionID, final.Step().ID
			work.RoleBindingID, work.CandidateSHA = final.ID, final.CandidateCommitSHA
		}
		for i := range e.CheckRuns {
			check := &e.CheckRuns[i]
			if option.Action == core.RecoveryRetryCheck && check.ID == option.TargetID {
				work.CandidateSHA = check.CandidateCommitSHA
				return work, check
			}
		}
		for _, current := range view.TrustedProgress.ControlledWork {
			if work.LogicalStepID != "" && current.LogicalStepID == work.LogicalStepID && current.RoleBindingID == work.RoleBindingID {
				work.AttemptID, work.AttemptNumber, work.EvidenceID = current.AttemptID, current.AttemptNumber, current.EvidenceID
				work.FailureEventID, work.PreflightID = current.FailureEventID, current.PreflightID
				break
			}
		}
		return work, nil
	}
	// A planning recovery's target includes its immutable binding digest.
	// Never attach a different current planning step merely because roles match.
	for _, current := range view.TrustedProgress.ControlledWork {
		if current.LogicalStepID != "" && (option.TargetID == current.LogicalStepID || strings.HasPrefix(option.TargetID, current.LogicalStepID+":")) {
			return current, nil
		}
	}
	return work, nil
}

func addWorkflowWorkEvidence(issue *core.WorkflowDiagnosisIssue, view RequirementView, work core.ControlledWork) {
	issue.Role, issue.AOSessionID = work.Role, work.AOSessionID
	addDiagnosisEvidence(issue, "ROLE_BINDING", work.RoleBindingID, work.RoleBindingID, nil)
	addDiagnosisEvidence(issue, "PREFLIGHT_ID", work.PreflightID, work.PreflightID, nil)
	if work.LogicalStepID != "" {
		addDiagnosisEvidence(issue, "STEP_ID", work.LogicalStepID, work.LogicalStepID, nil)
	}
	if work.AOSessionID != "" {
		addDiagnosisEvidence(issue, "SESSION_ID", work.AOSessionID, work.AOSessionID, nil)
	}
	for _, task := range view.TrustedProgress.Tasks {
		if task.DevelopmentTaskID == work.DevelopmentTaskID {
			addDiagnosisEvidence(issue, "TASK", task.DevelopmentTaskID, task.Title, nil)
		}
	}
	if e := view.ComplexExecution; e != nil {
		for _, binding := range e.RoleBindings {
			if binding.ID == work.RoleBindingID && work.RoleBindingID != "" {
				addDiagnosisEvidence(issue, "EXPECTED_WORKSPACE", binding.ID, binding.WorkspacePath, nil)
			}
		}
		for _, dispatch := range e.Dispatches {
			if dispatch.ID == work.DispatchID {
				addDiagnosisEvidence(issue, "ROUND", dispatch.ID, strconv.Itoa(dispatch.Round), dispatch.SettledAt)
				addDiagnosisEvidence(issue, "DISPATCH_STATUS", dispatch.ID, string(dispatch.Status), dispatch.SettledAt)
				addDiagnosisEvidence(issue, "CANDIDATE", dispatch.ID, dispatch.CandidateCommitSHA, dispatch.SettledAt)
			}
		}
		for _, step := range e.AgentSteps {
			addWorkflowStepEvidence(issue, step, work.LogicalStepID)
		}
		if final := e.FinalReview; final != nil && final.Step().ID == work.LogicalStepID {
			addWorkflowStepEvidence(issue, final.Step(), work.LogicalStepID)
		}
	}
	if planning := view.ComplexPlanning; planning != nil {
		for _, step := range planning.AgentSteps {
			addWorkflowStepEvidence(issue, step, work.LogicalStepID)
		}
	}
	if flow := view.StandardFlow; flow != nil {
		for _, step := range flow.AgentSteps {
			addWorkflowStepEvidence(issue, step, work.LogicalStepID)
		}
	}
	if flow := view.QuickExecution; flow != nil {
		for _, step := range flow.AgentSteps {
			addWorkflowStepEvidence(issue, step, work.LogicalStepID)
		}
	}
	if flow := view.DirectionChange; flow != nil {
		for _, step := range flow.AgentSteps {
			addWorkflowStepEvidence(issue, step, work.LogicalStepID)
		}
	}
	var selected *core.AgentStepAttemptView
	for i := range view.AgentStepAttempts {
		attempt := &view.AgentStepAttempts[i]
		if work.AttemptID != "" && attempt.ID == work.AttemptID && attempt.LogicalStepID == work.LogicalStepID && attempt.RoleBindingID == work.RoleBindingID && attempt.AOSessionID == work.AOSessionID {
			selected = attempt
		}
	}
	if selected != nil {
		addDiagnosisEvidence(issue, "MESSAGE_STATE", selected.ID, string(selected.SendStatus), selected.LastObservedAt)
		addDiagnosisEvidence(issue, "FAILURE_CATEGORY", selected.LastEventID, string(selected.FailureCategory), selected.LastObservedAt)
		addDiagnosisEvidence(issue, "PROVIDER_CODE", selected.LastEventID, selected.ProviderErrorCode, selected.LastObservedAt)
		addDiagnosisEvidence(issue, "FAILURE_SUMMARY", selected.LastEventID, selected.ErrorSummary, selected.LastObservedAt)
		if selected.RetryAt != nil {
			addDiagnosisEvidence(issue, "RETRY_AT", selected.LastEventID, selected.RetryAt.Format(time.RFC3339), nil)
		}
		for _, result := range selected.Results {
			if result.ParseConclusion == core.AgentResultParseInvalid {
				addDiagnosisEvidence(issue, "PARSE_ERROR", result.FinalMessageID, result.ParseErrorSummary, nil)
			}
		}
	}
	if p := view.LatestControlledPreflight; p != nil && work.PreflightID == "" && work.RoleBindingID != "" && p.RoleBindingID == work.RoleBindingID {
		addDiagnosisEvidence(issue, "PREFLIGHT_STATUS", p.ID, p.Outcome, nil)
		addDiagnosisEvidence(issue, "PREFLIGHT_SUMMARY", p.ID, p.ErrorSummary, nil)
		addDiagnosisEvidence(issue, "PROVIDER_CODE", p.ID, p.ProviderErrorCode, nil)
	}
	if budget := view.MessageBudget; budget != nil {
		for _, usage := range budget.Steps {
			if usage.LogicalStepID == work.LogicalStepID && work.LogicalStepID != "" {
				addDiagnosticCount(issue, "RESERVED_MESSAGES", work.LogicalStepID, usage.ReservedMessages)
				addDiagnosticCount(issue, "CONFIRMED_MESSAGES", work.LogicalStepID, usage.ConfirmedSentMessages)
				addDiagnosticCount(issue, "REMAINING_MESSAGES", work.LogicalStepID, usage.RemainingMessages)
			}
		}
		for _, usage := range budget.Roles {
			if usage.ExecutionRunID == work.ExecutionRunID && usage.ComplexExecutionTaskID == work.TaskMappingID && usage.RoleKind == work.Role {
				addDiagnosticCount(issue, "ROLE_USED", usage.BudgetID, usage.ReservedSteps)
				addDiagnosticCount(issue, "ROLE_MAX", usage.BudgetID, usage.MaxSteps)
				addDiagnosticCount(issue, "ROLE_REMAINING", usage.BudgetID, usage.RemainingSteps)
			}
		}
	}
}

// Full attempt history is loaded after the compact progress snapshot. An exact
// step/role/session and event match is required; a later attempt is not evidence
// for an earlier failure title. Legacy missing evidence is never filled in.
func workflowDiagnosisAttemptCurrent(view RequirementView, work core.ControlledWork) bool {
	if work.AttemptID == "" {
		return true
	}
	matched := false
	for _, attempt := range view.AgentStepAttempts {
		if attempt.LogicalStepID != work.LogicalStepID || attempt.LegacyEvidenceMissing {
			continue
		}
		if work.AttemptNumber != nil && attempt.AttemptNumber > *work.AttemptNumber {
			return false
		}
		if attempt.ID != work.AttemptID {
			continue
		}
		if attempt.RoleBindingID != work.RoleBindingID || attempt.AOSessionID != work.AOSessionID || (work.EvidenceID != "" && work.EvidenceID != work.PreflightID && work.EvidenceID != attempt.LastEventID) {
			return false
		}
		matched = true
	}
	return matched
}

func addDiagnosticCount(issue *core.WorkflowDiagnosisIssue, kind, id string, count *int64) {
	if count != nil {
		addDiagnosisEvidence(issue, kind, id, strconv.FormatInt(*count, 10), nil)
	}
}

func addWorkflowStepEvidence(issue *core.WorkflowDiagnosisIssue, step core.AgentStep, selectedID string) {
	if selectedID == "" || step.ID != selectedID {
		return
	}
	addDiagnosisEvidence(issue, "STEP_STATUS", step.ID, string(step.SendStatus), step.FailedAt)
	if step.FailedAt != nil {
		addDiagnosisEvidence(issue, "STOPPED_AT", step.ID, step.FailedAt.Format(time.RFC3339), step.FailedAt)
	}
}
