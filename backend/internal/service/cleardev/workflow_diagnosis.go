package cleardev

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// GetWorkflowRecovery keeps the existing action eligibility unchanged. A
// diagnostic read failure is reported separately: it must not turn an already
// accepted POST into a failed write or manufacture an additional retry grant.
func (s *Service) GetWorkflowRecovery(ctx context.Context, requirementID string) (core.WorkflowRecoveryView, error) {
	out, err := s.getWorkflowRecovery(ctx, requirementID)
	if err != nil {
		return out, err
	}
	now := s.now().UTC()
	if out.Diagnosis != nil && out.Diagnosis.ReadError != "" {
		return out, nil
	}
	view, err := s.getProgressRequirement(ctx, requirementID, now, true)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		out.Diagnosis = &core.WorkflowDiagnosis{ObservedAt: now, ReadError: "DIAGNOSIS_UNAVAILABLE", Issues: []core.WorkflowDiagnosisIssue{}}
		return out, nil
	}
	out.Diagnosis = workflowDiagnosisFromFacts(view, out, now)
	if out.Diagnosis.Current {
		s.addProductWorkflowDiagnosis(ctx, requirementID, out.Diagnosis)
		s.addWorkflowDiagnosisPreflights(ctx, out.Diagnosis)
	}
	// Recovery admission and progress are separate read models. Detect a target
	// changing between them rather than attaching a new step's facts to an old
	// button. This is only a sample; actual writes still repeat all their gates.
	latest, readErr := s.getWorkflowRecovery(ctx, requirementID)
	if readErr != nil || latest.ExecutionRunID != out.ExecutionRunID || !reflect.DeepEqual(latest.Options, out.Options) || !reflect.DeepEqual(latest.History, out.History) || !reflect.DeepEqual(latest.BuilderSessionChecks, out.BuilderSessionChecks) || !reflect.DeepEqual(latest.BuilderReplacementSource, out.BuilderReplacementSource) || latest.Diagnosis != nil && latest.Diagnosis.ReadError != "" {
		out.Diagnosis.Current = false
		out.Diagnosis.ReadError = "CURRENT_BINDINGS_CHANGED"
		out.Diagnosis.Issues = []core.WorkflowDiagnosisIssue{}
	}
	if !out.Diagnosis.Current {
		return out, nil
	}
	// Session activity is a present observation, not a retroactive explanation
	// of why a historical spawn failed. Never invoke restore, preflight or Chat.
	sessions := map[string][]core.WorkflowDiagnosisEvidence{}
	for i := range out.Diagnosis.Issues {
		issue := &out.Diagnosis.Issues[i]
		if issue.Relationship != "CURRENT" || issue.AOSessionID == "" {
			continue
		}
		evidence, found := sessions[issue.AOSessionID]
		if !found {
			evidence = s.workflowDiagnosisSession(ctx, issue.AOSessionID, now)
			sessions[issue.AOSessionID] = evidence
		}
		issue.Evidence = append(issue.Evidence, evidence...)
	}
	s.addFailureHandlingView(ctx, requirementID, view, &out)
	return out, nil
}

func workflowDiagnosisFromFacts(view RequirementView, recovery core.WorkflowRecoveryView, now time.Time) *core.WorkflowDiagnosis {
	summary := view.TrustedProgress
	diagnosis := &core.WorkflowDiagnosis{Phase: string(summary.Phase), FactSummarySHA256: summary.FactSummarySHA256,
		ObservedAt: now, Current: summary.ReasonCode != "CURRENT_BINDINGS_CHANGED", NextOwner: summary.NextOwner, Issues: []core.WorkflowDiagnosisIssue{}}
	if recovery.ExecutionRunID != "" && (view.ComplexExecution == nil || view.ComplexExecution.Run.ID != recovery.ExecutionRunID) {
		diagnosis.Current = false
	}
	if !diagnosis.Current {
		diagnosis.ReadError = "CURRENT_BINDINGS_CHANGED"
		return diagnosis
	}
	seen := map[string]bool{}
	add := func(issue core.WorkflowDiagnosisIssue) {
		key := issue.Relationship + ":" + issue.ReasonCode + ":" + issue.SubjectID
		if seen[key] {
			return
		}
		seen[key] = true
		issue.ID = key
		issue.Category = core.WorkflowBlockerCategory(issue.ReasonCode)
		if issue.Evidence == nil {
			issue.Evidence = []core.WorkflowDiagnosisEvidence{}
		}
		diagnosis.Issues = append(diagnosis.Issues, issue)
	}
	staleExecution := false
	for _, option := range recovery.Options {
		staleExecution = staleExecution || option.UnavailableReason == "EXECUTION_NOT_CURRENT"
	}
	if staleExecution {
		add(core.WorkflowDiagnosisIssue{ReasonCode: "EXECUTION_NOT_CURRENT", Relationship: "CURRENT", SubjectType: "REQUIREMENT", SubjectID: view.Requirement.ID})
	}
	for _, option := range recovery.Options {
		reason := option.Reason
		if reason == "" {
			reason = option.UnavailableReason
		}
		if reason == "" {
			reason = "UNKNOWN"
		}
		issue := core.WorkflowDiagnosisIssue{ReasonCode: reason, Relationship: "CURRENT", Role: option.Role, SubjectType: "RECOVERY_TARGET", SubjectID: option.TargetID, Summary: diagnosticText(option.Summary, 2000)}
		if staleExecution {
			issue.Relationship = "HISTORICAL"
		}
		work, check := workflowDiagnosisOptionWork(view, option, recovery.BuilderReplacementSource)
		if !workflowDiagnosisAttemptCurrent(view, work) {
			diagnosis.Current, diagnosis.ReadError = false, "CURRENT_BINDINGS_CHANGED"
			diagnosis.Issues = []core.WorkflowDiagnosisIssue{}
			return diagnosis
		}
		if option.Role == "BUILDER" && view.ComplexExecution != nil && work.LogicalStepID == "" {
			diagnosis.Current = false
			diagnosis.ReadError = "CURRENT_BINDINGS_CHANGED"
			diagnosis.Issues = []core.WorkflowDiagnosisIssue{}
			return diagnosis
		}
		addWorkflowWorkEvidence(&issue, view, work)
		if check != nil {
			addWorkflowCheckEvidence(&issue, *check)
		}
		add(issue)
		if option.UnavailableReason != "" && option.UnavailableReason != reason {
			add(core.WorkflowDiagnosisIssue{ReasonCode: option.UnavailableReason, Relationship: "LIMIT", SubjectType: "RECOVERY_TARGET", SubjectID: option.TargetID})
		}
	}
	if execution := view.ComplexExecution; execution != nil {
		if failed, found := core.CurrentIntegrationCheckFailure(*execution); found {
			issue := core.WorkflowDiagnosisIssue{ReasonCode: string(failed.ReasonCode), Relationship: "CURRENT", Role: "CHECKER", SubjectType: "CHECK_RUN", SubjectID: failed.ID, Summary: clipBuilderFailureReceiptOutput(failed.OutputSummary)}
			addWorkflowCheckEvidence(&issue, failed)
			add(issue)
		}
	}
	routineObservation := false
	for _, work := range summary.ControlledWork {
		if work.ReasonCode == "" || staleExecution && work.ExecutionRunID != "" {
			continue
		}
		covered := false
		for _, issue := range diagnosis.Issues {
			if issue.Relationship == "CURRENT" && issue.ReasonCode == string(work.ReasonCode) {
				for _, evidence := range issue.Evidence {
					covered = covered || evidence.Kind == "STEP_STATUS" && evidence.FactID == work.LogicalStepID
				}
			}
		}
		if covered {
			continue
		}
		subject := work.LogicalStepID
		if subject == "" {
			subject = work.RoleBindingID
		}
		if !workflowDiagnosisAttemptCurrent(view, work) {
			diagnosis.Current, diagnosis.ReadError = false, "CURRENT_BINDINGS_CHANGED"
			diagnosis.Issues = []core.WorkflowDiagnosisIssue{}
			return diagnosis
		}
		if work.State == "OBSERVING" && work.AttemptID != "" && work.TurnID != "" && (work.ReasonCode == "SENT" || work.ReasonCode == "CORRECTION_SENT") {
			// A confirmed running turn is work in progress, not a stop. Keep
			// stale-binding checks above and actual blockers below unchanged.
			routineObservation = true
			continue
		}
		issue := core.WorkflowDiagnosisIssue{ReasonCode: string(work.ReasonCode), Relationship: "CURRENT", SubjectType: "CONTROLLED_WORK", SubjectID: subject}
		addWorkflowWorkEvidence(&issue, view, work)
		add(issue)
	}
	for _, issue := range workflowPlanningDiagnosis(view) {
		add(issue)
	}
	for _, blocker := range summary.Blockers {
		if staleExecution && (strings.HasPrefix(blocker.Kind, "EXECUTION") || blocker.Kind == "TASK_BLOCKED") {
			continue
		}
		reason := string(blocker.ReasonCode)
		covered := false
		for _, issue := range diagnosis.Issues {
			if issue.Relationship != "CURRENT" {
				continue
			}
			if reason != "" && issue.ReasonCode == reason && (blocker.SubjectID == "" || blocker.SubjectID == issue.SubjectID) {
				covered = true
			}
			for _, fact := range issue.Evidence {
				if blocker.SubjectID != "" && fact.FactID == blocker.SubjectID && ((reason == "" && blocker.Kind == "TASK_BLOCKED" && fact.Kind == "TASK") || (reason != "" && issue.ReasonCode == reason)) {
					covered = true
				}
			}
		}
		if covered {
			continue
		}
		if reason == "" {
			reason = blocker.Kind
		}
		add(core.WorkflowDiagnosisIssue{ReasonCode: reason, Relationship: "CURRENT", SubjectType: blocker.SubjectType, SubjectID: blocker.SubjectID})
	}
	for _, decision := range summary.PendingDecisions {
		if decision.Kind == "CLARIFICATION_ANSWERS" {
			found := false
			for _, issue := range diagnosis.Issues {
				found = found || issue.ReasonCode == "AWAITING_CLARIFICATION"
			}
			if !found {
				add(core.WorkflowDiagnosisIssue{ReasonCode: "AWAITING_CLARIFICATION", Relationship: "CURRENT", SubjectType: decision.SubjectType, SubjectID: decision.SubjectID})
			}
			continue
		}
		issue := core.WorkflowDiagnosisIssue{ReasonCode: "PENDING_DECISION", Relationship: "CURRENT", SubjectType: decision.SubjectType, SubjectID: decision.SubjectID}
		addDiagnosisEvidence(&issue, "PENDING_DECISION", decision.SubjectID, decision.Kind, nil)
		add(issue)
	}
	if len(diagnosis.Issues) == 0 {
		reason := string(summary.ReasonCode)
		if routineObservation && (reason == "SENT" || reason == "CORRECTION_SENT") {
			reason = ""
		}
		if reason == "" && (summary.Phase == "CANCELLED" || summary.Phase == "PAUSED" || summary.Phase == "BLOCKED" || summary.Phase == "NEEDS_HUMAN" || summary.Phase == "AWAITING_CONFIRMATION" || summary.Phase == "AWAITING_CLARIFICATION") {
			reason = string(summary.Phase)
		}
		if reason != "" {
			add(core.WorkflowDiagnosisIssue{ReasonCode: reason, Relationship: "CURRENT", SubjectType: "REQUIREMENT", SubjectID: view.Requirement.ID})
		}
	}
	// Expose only the immediate predecessor of each currently blocked Builder
	// dispatch, never an arbitrary "latest failed check" from another task/run.
	if e := view.ComplexExecution; e != nil && !staleExecution && (summary.Phase == "BLOCKED" || summary.Phase == "NEEDS_HUMAN" || summary.Phase == "REWORKING") {
		for _, task := range e.Tasks {
			var current, previous *core.ComplexExecutionDispatch
			for i := range e.Dispatches {
				d := &e.Dispatches[i]
				if d.ID == task.CurrentDispatchID {
					current = d
				}
			}
			if current == nil || current.Round < 1 || current.Status != core.ComplexExecutionDispatchBlocked && current.Status != core.ComplexExecutionDispatchNeedsHuman {
				continue
			}
			for i := range e.Dispatches {
				d := &e.Dispatches[i]
				if d.ComplexExecutionTaskID == task.ID && d.Round == current.Round-1 {
					previous = d
				}
			}
			if previous == nil || previous.CandidateCommitSHA == "" {
				continue
			}
			latest := map[string]core.ComplexExecutionCheckRun{}
			for _, check := range e.CheckRuns {
				if check.DispatchID != previous.ID || check.CandidateCommitSHA != previous.CandidateCommitSHA {
					continue
				}
				old, exists := latest[check.CheckSpecFactID]
				if !exists || check.CreatedAt.After(old.CreatedAt) {
					latest[check.CheckSpecFactID] = check
				}
			}
			// Preserve database order for stable UI and reproducible evidence.
			for _, check := range e.CheckRuns {
				if latest[check.CheckSpecFactID].ID != check.ID || check.Result != core.EvidenceResultFail && check.Status != core.ComplexExecutionCheckRunFailed {
					continue
				}
				reason := string(check.ReasonCode)
				if reason == "" {
					reason = "CHECK_FAILED"
				}
				issue := core.WorkflowDiagnosisIssue{ReasonCode: reason, Relationship: "PRECEDING_FAILURE", SubjectType: "CHECK_RUN", SubjectID: check.ID, Role: "CHECKER"}
				addDiagnosisEvidence(&issue, "TASK", task.ID, task.TaskKey, nil)
				addWorkflowCheckEvidence(&issue, check)
				add(issue)
			}
		}
	}
	return diagnosis
}

func addDiagnosisEvidence(issue *core.WorkflowDiagnosisIssue, kind, id, value string, at *time.Time) {
	if value == "" {
		return
	}
	text := diagnosticText(value, 3000)
	issue.Evidence = append(issue.Evidence, core.WorkflowDiagnosisEvidence{Kind: kind, FactID: id, Value: text, RecordedAt: at, Truncated: text != value})
}

func diagnosticText(value string, maximum int) string {
	value = strings.ToValidUTF8(value, "�")
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	return string([]rune(value)[:maximum]) + "…"
}

func (s *Service) workflowDiagnosisSession(ctx context.Context, id string, now time.Time) []core.WorkflowDiagnosisEvidence {
	issue := core.WorkflowDiagnosisIssue{}
	if s.ao == nil {
		addDiagnosisEvidence(&issue, "SESSION_READ", id, "unavailable", &now)
		return issue.Evidence
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(id))
	if err != nil || !found {
		addDiagnosisEvidence(&issue, "SESSION_READ", id, "unavailable", &now)
		return issue.Evidence
	}
	addDiagnosisEvidence(&issue, "SESSION_STATE", id, string(record.Activity.State), &now)
	addDiagnosisEvidence(&issue, "SESSION_TERMINATED", id, strconv.FormatBool(record.IsTerminated), &now)
	addDiagnosisEvidence(&issue, "NATIVE_IDENTITY_PRESENT", id, strconv.FormatBool(record.Metadata.ProviderConversationID != ""), &now)
	addDiagnosisEvidence(&issue, "WORKSPACE", id, record.Metadata.WorkspacePath, &now)
	addDiagnosisEvidence(&issue, "MODEL", id, record.Metadata.Model, &now)
	return issue.Evidence
}

func addWorkflowCheckEvidence(issue *core.WorkflowDiagnosisIssue, check core.ComplexExecutionCheckRun) {
	addDiagnosisEvidence(issue, "CANDIDATE", check.ID, check.CandidateCommitSHA, check.SettledAt)
	addDiagnosisEvidence(issue, "CHECK_COMMAND", check.ID, strings.Join(check.Argv, " "), check.StartedAt)
	addDiagnosisEvidence(issue, "CHECK_STATUS", check.ID, string(check.Status)+" / "+string(check.Result), check.SettledAt)
	if check.ExitCode != nil {
		addDiagnosisEvidence(issue, "CHECK_EXIT", check.ID, strconv.Itoa(*check.ExitCode), check.SettledAt)
	}
	addDiagnosisEvidence(issue, "CHECK_TIMED_OUT", check.ID, strconv.FormatBool(check.TimedOut), check.SettledAt)
	output := check.OutputSummary
	// Some checks carry a signed receipt. Display its log, not a wall of JSON,
	// while retaining the precise check ID and candidate that supplied it.
	var receipt struct {
		OutputSummary string `json:"outputSummary"`
	}
	if json.Unmarshal([]byte(output), &receipt) == nil && receipt.OutputSummary != "" {
		output = receipt.OutputSummary
	}
	if output != "" {
		// Failures are commonly at the tail of a long dependency listing. Keep
		// both ends instead of cutting off the only actionable error message.
		clean := strings.ToValidUTF8(output, "�")
		runes := []rune(clean)
		if len(runes) > 3000 {
			clean = string(runes[:1000]) + "\n…\n" + string(runes[len(runes)-1997:])
		}
		issue.Evidence = append(issue.Evidence, core.WorkflowDiagnosisEvidence{Kind: "CHECK_OUTPUT", FactID: check.ID, Value: clean, RecordedAt: check.SettledAt, Truncated: check.OutputTruncated || clean != output})
	}
}
