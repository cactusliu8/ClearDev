package cleardev

import (
	"encoding/json"
	"time"
)

// WorkflowRecovery binds supplementary context to a stopped step. It is not an
// approval, a scope change, or evidence that any check passed.
type WorkflowRecovery struct {
	WorkingTreeSHA256      string    `json:"workingTreeSha256,omitempty"`
	ProviderConversationID string    `json:"providerConversationId,omitempty"`
	OriginalStoppedAt      time.Time `json:"originalStoppedAt"`
	ID                     string    `json:"id"`
	ExecutionRunID         string    `json:"executionRunId"`
	Action                 string    `json:"action"`
	TargetID               string    `json:"targetId"`
	DispatchID             string    `json:"dispatchId,omitempty"`
	TaskID                 string    `json:"taskId,omitempty"`
	StepID                 string    `json:"stepId,omitempty"`
	BindingID              string    `json:"bindingId,omitempty"`
	SuccessorID            string    `json:"successorId"`
	CandidateSHA           string    `json:"candidateSha,omitempty"`
	OriginalStatus         string    `json:"originalStatus"`
	OriginalReason         string    `json:"originalReason"`
	OriginalSummary        string    `json:"originalSummary"`
	Supplement             string    `json:"supplement"`
	CreatedAt              time.Time `json:"createdAt"`
}

// Recovery actions identify the original workflow operation to continue.
const (
	RecoveryContinueBuilder     = "CONTINUE_BUILDER"
	RecoveryRetryBuilderSession = "RETRY_BUILDER_SESSION"
	RecoveryRetryReview         = "RETRY_REVIEW"
	RecoveryRetryCheck          = "RETRY_CHECK"
	RecoveryRetryStage          = "RETRY_STAGE"
)

// WorkflowRecoveryResolvesStep only removes an old failed transport barrier;
// the successor still has to execute and satisfy all existing checks.
func WorkflowRecoveryResolvesStep(snapshot ComplexExecutionSnapshot, step AgentStep) bool {
	for _, r := range snapshot.WorkflowRecoveries {
		if r.StepID == step.ID && r.Action == RecoveryRetryReview {
			return true
		}
	}
	return false
}

// WorkflowRecoveryResolvesBinding identifies a replaced historical reviewer.
func WorkflowRecoveryResolvesBinding(snapshot ComplexExecutionSnapshot, binding ComplexExecutionRoleBinding) bool {
	for _, r := range snapshot.WorkflowRecoveries {
		if r.BindingID == binding.ID && r.Action == RecoveryRetryReview {
			return true
		}
	}
	return false
}

// WorkflowRecoveryOption describes one current stop, including an unavailable
// action's concrete reason. Clients cannot choose commands, candidates or scope.
type WorkflowRecoveryOption struct {
	Action            string `json:"action"`
	TargetID          string `json:"targetId"`
	TaskID            string `json:"taskId,omitempty"`
	Role              string `json:"role"`
	Reason            string `json:"reason"`
	Summary           string `json:"summary"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

// WorkflowRecoveryView exposes current actions and preserved recovery history.
type WorkflowRecoveryView struct {
	FailureHandling      []WorkflowFailureHandling `json:"failureHandling,omitempty"`
	ExecutionRunID       string                    `json:"executionRunId"`
	Options              []WorkflowRecoveryOption  `json:"options"`
	History              []WorkflowRecovery        `json:"history"`
	Diagnosis            *WorkflowDiagnosis        `json:"diagnosis,omitempty"`
	BuilderSessionChecks []BuilderSessionCheck     `json:"builderSessionChecks,omitempty"`
	BuilderReplacements  []BuilderReplacementView  `json:"builderReplacements,omitempty"`
	// Exact daemon-only source used to attach diagnosis to this sampled option.
	// It is excluded from HTTP and model JSON, including sealed handoff material.
	BuilderReplacementSource *BuilderReplacementState `json:"-"`
}

// WorkflowRecoveryOptions derives stops that have a supported continuation.
func WorkflowRecoveryOptions(s ComplexExecutionSnapshot) []WorkflowRecoveryOption {
	options := []WorkflowRecoveryOption{}
	_, project, err := ProjectContractFromRun(s.Run)
	if err != nil || !project || s.Run.Mode != WorkModeStandard || s.Run.CompletedAt != nil {
		return options
	}
	recovered := func(id string) bool {
		for _, r := range s.WorkflowRecoveries {
			if r.TargetID == id {
				return true
			}
		}
		return false
	}
	budget := func(task, role string) bool {
		if s.Exception == nil {
			return false
		}
		for _, b := range s.Exception.Budgets {
			if b.ComplexExecutionTaskID == task && b.RoleKind == role {
				return b.UsedTurns < b.MaxTurns+b.AuthorizedExtraTurns
			}
		}
		return false
	}
	for _, task := range s.Tasks {
		if task.Status != DevelopmentTaskStatusBlocked && task.Status != DevelopmentTaskStatusNeedsHuman {
			continue
		}
		for _, d := range s.Dispatches {
			if d.ID != task.CurrentDispatchID {
				continue
			}
			if (d.ReasonCode == ReasonBuilderSpawnFailed || d.ReasonCode == "BUILDER_WORKTREE_DIRTY") && d.CandidateCommitID == "" && d.SettledAt != nil {
				for _, step := range s.AgentSteps {
					if step.ID == d.AgentStepID && step.SendStatus == AgentStepSendStatusPending {
						options = append(options, WorkflowRecoveryOption{Action: RecoveryRetryBuilderSession, TargetID: WorkflowBuilderRetryTarget(d.ID, *d.SettledAt), TaskID: task.ID, Role: "BUILDER", Reason: string(d.ReasonCode)})
					}
				}
			}
			if (d.ReasonCode == "BUILDER_RESULT_INVALID" || d.ReasonCode == "BUILDER_UNAVAILABLE") && !recovered(d.ID) {
				// Invalid protocol output does not settle the business step.
				// Only the service/store's immutable result proof may enable it.
				options = append(options, WorkflowRecoveryOption{Action: RecoveryContinueBuilder, TargetID: d.ID, TaskID: task.ID, Role: "BUILDER", Reason: string(d.ReasonCode), UnavailableReason: "RESULT_NOT_SETTLED"})
			}
			if (d.ReasonCode == "BUILDER_BLOCKED" || d.ReasonCode == "BUILDER_NEEDS_HUMAN" || d.ReasonCode == ReasonHumanDecisionRequired || (d.ReasonCode == "CANDIDATE_INVALID" && d.CandidateCommitID == "" && d.SettledAt != nil)) && !recovered(d.ID) {
				o := WorkflowRecoveryOption{Action: RecoveryContinueBuilder, TargetID: d.ID, TaskID: task.ID, Role: "BUILDER", Reason: string(d.ReasonCode)}
				settled := false
				for _, step := range s.AgentSteps {
					if step.ID == d.AgentStepID {
						o.Summary = step.FinalMessageText
						settled = step.SendStatus == AgentStepSendStatusSettled
					}
				}
				if !settled {
					o.UnavailableReason = "RESULT_NOT_SETTLED"
				} else if !budget(task.ID, "BUILDER") {
					o.UnavailableReason = "BUILDER_BUDGET_EXHAUSTED"
				}
				options = append(options, o)
			}
			// A completed Reviewer finding is a failure of the task artifact,
			// not a request to keep asking Reviewers until one returns PASS.
			if d.ReasonCode == "REVIEW_BLOCKED" && d.CandidateCommitID != "" && d.SettledAt != nil && !recovered(d.ID) {
				for _, review := range s.Reviews {
					if review.DispatchID != d.ID || review.Status != LocalReviewStatusSettled || review.Verdict != LocalReviewBlocked || review.CandidateCommitID != d.CandidateCommitID || review.CandidateCommitSHA != d.CandidateCommitSHA {
						continue
					}
					o := WorkflowRecoveryOption{Action: RecoveryContinueBuilder, TargetID: d.ID, TaskID: task.ID, Role: "BUILDER", Reason: string(d.ReasonCode), UnavailableReason: "RESULT_NOT_SETTLED"}
					for _, step := range s.AgentSteps {
						if step.ID == d.AgentStepID && step.SendStatus == AgentStepSendStatusSettled {
							o.Summary = step.FinalMessageText
							o.UnavailableReason = ""
						}
					}
					if !budget(task.ID, "BUILDER") {
						o.UnavailableReason = "BUILDER_BUDGET_EXHAUSTED"
					}
					options = append(options, o)
					break
				}
			}
			for _, r := range s.Reviews {
				if r.DispatchID != d.ID || recovered(r.ID) {
					continue
				}
				if r.Verdict != LocalReviewBlocked && r.Verdict != LocalReviewNeedsHuman && (r.Status != LocalReviewStatusFailed || r.ReasonCode != "REVIEWER_UNAVAILABLE") {
					continue
				}
				o := WorkflowRecoveryOption{Action: RecoveryRetryReview, TargetID: r.ID, TaskID: task.ID, Role: "REVIEWER", Reason: string(r.ReasonCode), Summary: r.Summary}
				if !budget(task.ID, "REVIEWER") {
					o.UnavailableReason = "REVIEWER_BUDGET_EXHAUSTED"
				}
				options = append(options, o)
			}
			if d.ReasonCode == "REVIEWER_UNAVAILABLE" || d.ReasonCode == "REVIEW_BRANCH_UNAVAILABLE" {
				for _, binding := range s.RoleBindings {
					if binding.Role != StandardRoleReviewer || binding.CandidateCommitID != d.CandidateCommitID || binding.Status != RoleBindingStatusFailed || recovered(binding.ID) {
						continue
					}
					hasReview := false
					for _, review := range s.Reviews {
						if review.ReviewerRoleBindingID == binding.ID {
							hasReview = true
						}
					}
					if hasReview {
						continue
					}
					o := WorkflowRecoveryOption{Action: RecoveryRetryReview, TargetID: binding.ID, TaskID: task.ID, Role: "REVIEWER", Reason: string(binding.ReasonCode)}
					if !budget(task.ID, "REVIEWER") {
						o.UnavailableReason = "REVIEWER_BUDGET_EXHAUSTED"
					}
					options = append(options, o)
				}
			}
			if (d.ReasonCode == "CHECKER_UNAVAILABLE" || d.ReasonCode == "CHECK_FAILED" || d.ReasonCode == "BUILDER_BUDGET_EXHAUSTED") && !stoppedPlannerCheckRequiresNative(s, d.ID) {
				for _, check := range s.CheckRuns {
					retryable := (check.Status == ComplexExecutionCheckRunFailed && check.ReasonCode == "CHECKER_UNAVAILABLE") || projectCheckTerminated(check)
					if check.DispatchID != d.ID || !retryable || recovered(check.ID) {
						continue
					}
					latest := true
					for _, newer := range s.CheckRuns {
						if newer.DispatchID == d.ID && newer.CheckSpecFactID == check.CheckSpecFactID && newer.RetryOrdinal > check.RetryOrdinal {
							latest = false
						}
					}
					if !latest {
						continue
					}
					o := WorkflowRecoveryOption{Action: RecoveryRetryCheck, TargetID: check.ID, TaskID: task.ID, Role: "CHECKER", Reason: string(check.ReasonCode), Summary: check.OutputSummary}
					options = append(options, o)
				}
			}
		}
	}
	if r := s.FinalReview; r != nil && currentFinalReviewRecovery(s, *r) && !recovered(r.ID) && (r.Verdict == "BLOCKED" || r.Verdict == "NEEDS_HUMAN" || r.Status == "FAILED" && (r.ReasonCode == "REQUIREMENT_FINAL_REVIEWER_UNAVAILABLE" || r.ReasonCode == "REQUIREMENT_FINAL_REVIEW_BRANCH_UNAVAILABLE" || r.ReasonCode == "STAGE_TRIAL_REQUIRED" || r.ReasonCode == "REQUIREMENT_FINAL_REVIEW_RESULT_INVALID")) {
		o := WorkflowRecoveryOption{Action: RecoveryRetryStage, TargetID: r.ID, Role: "STAGE_REVIEWER", Reason: string(r.ReasonCode), Summary: r.Summary}
		if r.Status == "FAILED" && r.ReasonCode != "STAGE_TRIAL_REQUIRED" && r.SentAt != nil {
			o.UnavailableReason = "RESULT_NOT_SETTLED"
		}

		options = append(options, o)
	}
	for i := range options {
		o := &options[i]
		if o.UnavailableReason != "BUILDER_BUDGET_EXHAUSTED" && o.UnavailableReason != "REVIEWER_BUDGET_EXHAUSTED" {
			continue
		}
		if s.Exception != nil {
			for _, b := range s.Exception.Budgets {
				if b.ComplexExecutionTaskID == o.TaskID && b.RoleKind == o.Role && b.AuthorizedExtraTurns >= 2 {
					o.UnavailableReason = "ROLE_BUDGET_LIMIT"
				}
			}
		}
	}
	return options
}

// WorkflowBuilderRetryTarget identifies a single never-sent stop, not merely a
// reusable dispatch ID. A later explicit retry cannot replay an older stop.
func WorkflowBuilderRetryTarget(id string, at time.Time) string {
	return id + ":resume:" + at.UTC().Format(time.RFC3339Nano)
}

func currentFinalReviewRecovery(snapshot ComplexExecutionSnapshot, review RequirementFinalReview) bool {
	if !BuilderFirstFailureEnabled(snapshot.Run) {
		return true
	}
	if _, failed := CurrentIntegrationCheckFailure(snapshot); failed {
		return false
	}
	for _, task := range snapshot.Tasks {
		if FinalReviewTaskReturned(snapshot, task) && task.ReworkCount > task.CurrentRound {
			return false
		}
		for _, dispatch := range snapshot.Dispatches {
			if dispatch.ID == task.CurrentDispatchID && finalReviewPredatesDispatch(review, dispatch) {
				return false
			}
		}
	}
	return true
}

// A settled 137 exit permits an explicit same-candidate retry, not an OOM claim.
func projectCheckTerminated(check ComplexExecutionCheckRun) bool {
	if check.Status != ComplexExecutionCheckRunSettled || check.Result != EvidenceResultFail || check.SettledAt == nil || check.ReasonCode != "CHECK_FAILED" || check.ExitCode == nil || *check.ExitCode != 137 || check.TimedOut || check.OutputTruncated {
		return false
	}
	var receipt ProjectCheckReceipt
	return json.Unmarshal([]byte(check.OutputSummary), &receipt) == nil && receipt.Policy == ProjectCheckPolicyV2 && receipt.Image == ProjectCandidateCheckImage && receipt.Outcome == "FAIL" && receipt.ExitCode == 137 && !receipt.TimedOut && !receipt.OutputTruncated && receipt.CheckRunID == check.ID && receipt.CandidateSHA == check.CandidateCommitSHA
}
