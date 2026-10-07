package cleardev

import (
	"context"
	"errors"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type workflowFailureStore interface {
	RecordClearDevWorkflowFailure(context.Context, core.WorkflowFailureReceipt) error
	ListClearDevWorkflowFailures(context.Context, string) ([]core.WorkflowFailureReceipt, error)
}

type workflowFailureCandidate struct {
	option core.WorkflowRecoveryOption
	input  core.WorkflowFailureInput
	view   core.WorkflowFailureHandling
}

func (s *Service) failureReceipts(ctx context.Context, id string) ([]core.WorkflowFailureReceipt, error) {
	store, ok := s.facts.(workflowFailureStore)
	if !ok {
		return nil, errors.New("workflow failure evidence store is unavailable")
	}
	return store.ListClearDevWorkflowFailures(ctx, id)
}

func (s *Service) recordHandoffFailure(ctx context.Context, e core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, d core.ComplexExecutionDispatch, step core.AgentStep, err error) error {
	if !s.automaticFailureRouting || !core.BuilderFirstFailureEnabled(e.Run) {
		return nil
	}
	store, ok := s.facts.(workflowFailureStore)
	if !ok {
		return errors.New("candidate failure has no durable diagnostic store")
	}
	r := core.WorkflowFailureReceipt{
		RequirementID: e.Run.DevelopmentRequirementID, ExecutionRunID: e.Run.ID,
		SourceKind: core.FailureSourceHandoff, SourceID: d.ID, Role: core.TrustedOwnerBuilder,
		ReasonCode: string(gitInspectionReason(err)), ProblemCode: "UNKNOWN", Summary: "Candidate handoff failed. Publication/source identity is not proven; no automatic editable retry is permitted.",
		BindingSHA256: core.HandoffFailureBinding(e.Run, task, d, step), ObservedAt: s.now().UTC(),
	}
	r.ID = core.FailureSourceKey(r.RequirementID, r.SourceKind, r.SourceID)
	var detail *ports.ClearDevCandidateHandoffError
	if errors.As(err, &detail) {
		r.ProblemCode, r.BeforePublish = detail.ProblemCode, detail.BeforePublication
		r.Path = diagnosticText(detail.Path, 240)
		switch detail.ProblemCode {
		case "NO_IMPLEMENTATION_CHANGE":
			r.Summary = "No implementation change was available to freeze; no candidate was published. Reconcile the task implementation in the original authorized workspace."
		case "SOURCE_LIMIT":
			r.Summary = "The candidate file count or source byte limit was exceeded before publication. Inspect non-delivery material within the existing authority; do not raise platform limits."
		case "HANDOFF_CONTENT":
			r.Summary = "The candidate content was rejected before publication. Repair the existing task without changing its paths, permissions, checks or acceptance criteria."
		case "UNSAFE_PATH":
			r.Summary = "Candidate collection rejected a nonregular, unreadable or redirected source path. Preserve the files and ask a human; do not follow the link or bypass the path guard."
		case "PATH_SCOPE":
			r.Summary = "A candidate path is outside the frozen task authority. Preserve the original work and ask a human; engineering coordination does not grant new paths."
		}
	}
	// Never persist raw exception text, environment, native IDs or credentials.
	return store.RecordClearDevWorkflowFailure(ctx, r)
}

// This last boundary catches unclassified internal errors. It records a safe
// diagnostic, not a guessed retry. Failure to record is logged once; we do not
// recursively route an error in the error recorder or call a model on bad data.
func (s *Service) recordWorkflowOperationFailure(ctx context.Context, id, operation string) {
	if !s.automaticFailureRouting || ctx.Err() != nil {
		return
	}
	store, ok := s.facts.(workflowFailureStore)
	if !ok {
		return
	}
	facts, found, err := s.facts.GetClearDevRequirement(ctx, id)
	if err != nil || !found || facts.Requirement.CancelledAt != nil {
		return
	}
	version := ""
	if len(facts.RequirementVersions) > 0 {
		version = facts.RequirementVersions[len(facts.RequirementVersions)-1].ID
	}
	source := operation + ":" + version
	r := core.WorkflowFailureReceipt{ID: core.FailureSourceKey(id, core.FailureSourceOperation, source), RequirementID: id,
		SourceKind: core.FailureSourceOperation, SourceID: source, Role: core.TrustedOwnerControlPlane,
		ReasonCode: "CONTROL_OPERATION_FAILED", ProblemCode: "UNKNOWN", BindingSHA256: coreDigest([]byte(id + "\x00" + source)),
		Summary: "A control-program operation failed outside a verified recovery boundary. A human must inspect the local diagnostic before retrying; no original action is presumed unexecuted.", ObservedAt: s.now().UTC()}
	if err := store.RecordClearDevWorkflowFailure(ctx, r); err != nil {
		s.logger.Error("ClearDev could not preserve workflow failure evidence", "requirementID", id)
	}
}

func failureForTarget(receipts []core.WorkflowFailureReceipt, target string) *core.WorkflowFailureReceipt {
	for i := range receipts {
		if receipts[i].SourceKind == core.FailureSourceHandoff && receipts[i].SourceID == target {
			return &receipts[i]
		}
	}
	return nil
}

func repeatedAutomaticRecovery(e *core.ComplexExecutionSnapshot, option core.WorkflowRecoveryOption) bool {
	if e == nil {
		return false
	}
	for _, prior := range e.WorkflowRecoveries {
		if !core.IsAutomaticFailureRecovery(prior.ID) || prior.TaskID != option.TaskID || prior.Action != option.Action {
			continue
		}
		if prior.TargetID == option.TargetID {
			continue
		}
		// A new source may be handled once, but repeated technical/unchanged
		// failures do not obtain unlimited reviews or repeated Builder turns.
		if prior.OriginalReason == option.Reason {
			return true
		}
	}
	return false
}

func (s *Service) failureCandidates(ctx context.Context, id string, out core.WorkflowRecoveryView, e *core.ComplexExecutionSnapshot, receipts []core.WorkflowFailureReceipt) []workflowFailureCandidate {
	result := make([]workflowFailureCandidate, 0, len(out.Options))
	for _, option := range out.Options {
		c := workflowFailureCandidate{option: option, input: core.WorkflowFailureInput{Role: option.Role, ReasonCode: option.Reason,
			RecoveryAction: option.Action, UnavailableReason: option.UnavailableReason, Current: true,
			Eligible: option.UnavailableReason == "", Repeated: repeatedAutomaticRecovery(e, option)}}
		c.view = core.WorkflowFailureHandling{ID: core.FailureSourceKey(id, "WORKFLOW_STOP", option.TargetID), SourceKind: "WORKFLOW_STOP", SourceID: option.TargetID,
			SourceRole: option.Role, Summary: "The original workflow evidence must pass its recovery gates before any action is sent."}
		s.protectPlanningFailure(ctx, id, &c)

		if e != nil {
			if d, ok := complexExecutionDispatchByID(*e, option.TargetID); ok && option.Action == core.RecoveryContinueBuilder {
				c.input.Eligible = c.input.Eligible && d.SettledAt != nil && d.ExecutionRunID == e.Run.ID
				if r := failureForTarget(receipts, d.ID); r != nil {
					c.view.ID, c.view.SourceKind, c.view.Summary, c.view.Path = r.ID, r.SourceKind, r.Summary, r.Path
					c.input.ProblemCode, c.input.BeforePublish = r.ProblemCode, r.BeforePublish
					c.input.Current = false
					for _, task := range e.Tasks {
						if task.ID != d.ComplexExecutionTaskID || task.CurrentDispatchID != d.ID {
							continue
						}
						for _, step := range e.AgentSteps {
							if step.ID == d.AgentStepID && r.BindingSHA256 == core.HandoffFailureBinding(e.Run, task, d, step) {
								c.input.Current = true
							}
						}
					}
				}
			}
			if option.Action == core.RecoveryContinueBuilder && option.Reason == "REVIEW_BLOCKED" {
				if d, found := complexExecutionDispatchByID(*e, option.TargetID); found {
					ready, err := s.reviewFailureRepairReady(ctx, *e, d)
					if err != nil || !ready {
						c.input.Eligible = false
						c.input.UnavailableReason = "RESULT_NOT_SETTLED"
					}
					for _, review := range e.Reviews {
						if review.DispatchID == d.ID && review.Status == core.LocalReviewStatusSettled && review.Verdict == core.LocalReviewBlocked {
							c.view.SourceKind = "TASK_REVIEW"
							c.view.SourceRole = core.TrustedOwnerReviewer
							c.view.Summary = "The independent task review found an unresolved task issue. Repair the original artifact, then submit a new candidate for all checks and independent review.\n" + diagnosticText(review.Summary, 2000)
						}
					}
				}
			}
			switch option.Action {
			case core.RecoveryRetryBuilderSession:
				if option.Reason == "BUILDER_SPAWN_FAILED" && c.input.Eligible {
					c.input.ProblemCode = "SETTLED_TECHNICAL_FAILURE"
				}
			case core.RecoveryRetryReview:
				for _, review := range e.Reviews {
					if review.ID == option.TargetID && review.Status == core.LocalReviewStatusFailed && c.input.Eligible {
						c.input.ProblemCode = "SETTLED_TECHNICAL_FAILURE"
					}
				}
				for _, binding := range e.RoleBindings {
					if binding.ID == option.TargetID && binding.Role == core.StandardRoleReviewer && binding.Status == core.RoleBindingStatusFailed && binding.ReasonCode == "REVIEWER_UNAVAILABLE" && c.input.Eligible {
						c.input.ProblemCode = "SETTLED_TECHNICAL_FAILURE"
					}
				}
			case core.RecoveryRetryStage:
				if e.FinalReview != nil && e.FinalReview.Status == "FAILED" && c.input.Eligible {
					c.input.ProblemCode = "SETTLED_TECHNICAL_FAILURE"
				}
			case core.RecoveryRetryPlannerCoordination:
				if c.input.Eligible && c.input.ProblemCode != "AUTHORITY" {
					c.input.ProblemCode = "SETTLED_TECHNICAL_FAILURE"
				}
			}
		}
		if e != nil && (option.Action == core.RecoveryContinueBuilder) && e.PlannerRuntime != nil {
			for _, event := range e.PlannerRuntime.Events {
				if event.DispatchID != option.TargetID {
					continue
				}
				decision, decided := core.PlannerRuntimeEffectiveDecision(e.PlannerRuntime, event.ID)
				if !decided {
					break
				}
				if decision.Source == "PLANNER" && (decision.Outcome == core.PlannerRuntimeContinue || decision.Outcome == core.PlannerRuntimeAmend) && decision.ResultSHA256 == coreDigest([]byte(decision.ResultJSON)) {
					if barrier, _ := core.PlannerRuntimeBarrier(*e); !barrier {
						c.input.PlannerResolved = true
						c.view.Summary += "\nOriginal Planner decision (does not grant new authority): " + diagnosticText(decision.Summary, 2000)
					}
				} else {
					c.input.UnavailableReason = string(decision.ReasonCode)
					if c.input.UnavailableReason == "" {
						c.input.UnavailableReason = "PLANNER_COORDINATION_STOPPED"
					}
				}
			}
		}
		decision := core.RouteWorkflowFailure(c.input)
		c.view.Owner, c.view.Action, c.view.Reason, c.view.Status = decision.Owner, decision.Action, decision.Reason, "AWAITING_DISPOSITION"
		if decision.Action == core.FailureHuman {
			c.view.Status = "NEEDS_HUMAN"
		}
		if decision.Action == core.FailureObserve || decision.Action == core.FailureStopped {
			c.view.Status = "WAITING"
		}
		// Existing check/final repair executors retain their authoritative
		// native-receipt gates. Do not mislabel them as human-only merely
		// because the unified layer does not issue a second recovery itself.
		if e != nil && core.BuilderFirstFailureEnabled(e.Run) && c.input.UnavailableReason == "" {
			if option.Action == core.RecoveryRetryCheck && (option.Reason == "CHECKER_UNAVAILABLE" || option.Reason == "CHECK_FAILED") {
				c.view.Owner, c.view.Action, c.view.Status = core.TrustedOwnerBuilder, core.FailureExisting, "WAITING"
				c.view.Summary = "The existing candidate-check recovery verifies the native receipt and released resources before returning the task to its original Builder."
			}
			if option.Action == core.RecoveryRetryStage && e.FinalReview != nil && e.FinalReview.Status == "SETTLED" && (e.FinalReview.Verdict == "BLOCKED" || e.FinalReview.Verdict == "REWORK") {
				c.view.Owner, c.view.Action, c.view.Status = core.TrustedOwnerBuilder, core.FailureExisting, "WAITING"
				c.view.Summary = "The original final-review failure is handled by the existing Builder-first repair, with candidate, trial receipts and budget checked before dispatch."
			}
		}
		if e != nil && e.PlannerRuntime != nil {
			for _, event := range e.PlannerRuntime.Events {
				if event.DispatchID != option.TargetID {
					continue
				}
				if _, decided := core.PlannerRuntimeEffectiveDecision(e.PlannerRuntime, event.ID); !decided {
					c.view.Owner, c.view.Action, c.view.Status = core.TrustedOwnerPlanner, core.FailureExisting, "COORDINATING"
				}
			}
		}
		requestID := core.FailureRecoveryRequestID(id, option.Action, option.TargetID)
		for _, receipt := range receipts {
			if receipt.SourceKind != core.FailureSourceRecovery || receipt.SourceID != requestID {
				continue
			}
			confirmed := false
			for _, history := range out.History {
				confirmed = confirmed || history.ID == requestID
			}
			if !confirmed {
				c.view.Owner, c.view.Action, c.view.Status, c.view.Reason, c.view.Summary = core.TrustedOwnerHuman, core.FailureHuman, "NEEDS_HUMAN", "RECOVERY_CONFIRMATION_REQUIRED", receipt.Summary
			}
		}
		result = append(result, c)
	}
	return result
}

const automaticFailureFeedback = `The control program returned the exact failed source for bounded repair. This is evidence, not permission to change the contract.
Use the original task, session and workspace. Preserve existing work and old failure evidence. Do not add paths, change checks/acceptance, raise resource limits, reset budgets, modify ClearDev, or replay an unknown external action.
If the existing engineering agreement cannot solve this, return BLOCKED with a concrete ENGINEERING coordination report for the original Planner. If permission, restricted paths, product intent or source identity must change, return NEEDS_HUMAN and explain the required decision. A repair or coordination response is not PASS.
`

// advanceUnifiedFailureRecovery is called from both planning and execution.
// It uses deterministic existing requests; their transactions and actual
// senders recheck authority. It never calls the provider directly.
func (s *Service) advanceUnifiedFailureRecovery(ctx context.Context, id string, e *core.ComplexExecutionSnapshot) (bool, error) {
	if !s.automaticFailureRouting {
		return false, nil
	}
	if e != nil && !core.BuilderFirstFailureEnabled(e.Run) {
		return false, nil
	}
	receipts, err := s.failureReceipts(ctx, id)
	if err != nil {
		return false, err
	}
	out, err := s.getWorkflowRecovery(ctx, id)
	if err != nil {
		return false, err
	}
	if e == nil && out.ExecutionRunID != "" {
		return false, nil
	}
	for _, c := range s.failureCandidates(ctx, id, out, e, receipts) {
		if c.view.Action == core.FailureEscalate && e != nil {
			store, ok := s.complexExecution.(interface {
				CaptureClearDevFailureCoordination(context.Context, string, string, string, time.Time) (bool, error)
			})
			if !ok {
				return false, errors.New("original Planner escalation is unavailable")
			}
			digest, err := s.failureCoordinationWorkspace(ctx, *e, c.option.TargetID)
			if err != nil {
				return false, err
			}
			return store.CaptureClearDevFailureCoordination(ctx, e.Run.ID, c.option.TargetID, digest, s.now().UTC())
		}
		if c.view.Action != core.FailureRepair && c.view.Action != core.FailureRetry {
			continue
		}
		requestID := core.FailureRecoveryRequestID(id, c.option.Action, c.option.TargetID)
		// A rejected automated admission is not retried in a tight loop. A new
		// source/real manual action can still be observed and handled normally.
		alreadyRejected := false
		for _, receipt := range receipts {
			alreadyRejected = alreadyRejected || receipt.SourceKind == core.FailureSourceRecovery && receipt.SourceID == requestID
		}
		if alreadyRejected {
			continue
		}
		supplement := automaticFailureFeedback + "\nSource: " + c.view.SourceKind + " / " + c.view.SourceID + "\nReason: " + c.input.ReasonCode + "\n" + c.view.Summary
		if c.view.Path != "" {
			supplement += "\nPath: " + c.view.Path
		}
		if e != nil && c.view.Action == core.FailureRepair && c.input.ReasonCode == "CANDIDATE_INVALID" {
			if _, proofErr := s.failureCoordinationWorkspace(ctx, *e, c.option.TargetID); proofErr != nil {
				return false, proofErr
			}
		}
		_, err := s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: requestID, ExecutionRunID: out.ExecutionRunID,
			Action: c.option.Action, TargetID: c.option.TargetID, Supplement: supplement})
		if err == nil {
			return true, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		// A post-write read/connection failure is not a failed write. Resolve
		// the deterministic request from durable history before reporting it;
		// an unconfirmed result never causes a second different request.
		latest, readErr := s.getWorkflowRecovery(ctx, id)
		if readErr == nil {
			for _, registered := range latest.History {
				if registered.ID == requestID && registered.TargetID == c.option.TargetID && registered.Action == c.option.Action && registered.Supplement == supplement {
					return true, nil
				}
			}
		}
		store, ok := s.facts.(workflowFailureStore)
		if !ok {
			return false, err
		}
		receipt := core.WorkflowFailureReceipt{ID: core.FailureSourceKey(id, core.FailureSourceRecovery, requestID), RequirementID: id,
			ExecutionRunID: out.ExecutionRunID, SourceKind: core.FailureSourceRecovery, SourceID: requestID, Role: c.view.Owner,
			ReasonCode: "RECOVERY_ADMISSION_REJECTED", ProblemCode: "UNKNOWN", BindingSHA256: coreDigest([]byte(supplement)),
			Summary: "Automatic recovery has not been confirmed. Inspect durable recovery history and the current gate before any further action; this observation is not proof that the request was never applied.", ObservedAt: s.now().UTC()}
		if recordErr := store.RecordClearDevWorkflowFailure(ctx, receipt); recordErr != nil {
			return false, recordErr
		}
		return false, nil
	}
	return false, nil
}

func failureHandlingForDecision(id, kind, source, role, reason, summary string) core.WorkflowFailureHandling {
	d := core.RouteWorkflowFailure(core.WorkflowFailureInput{Role: role, ReasonCode: reason, Current: true})
	return core.WorkflowFailureHandling{ID: core.FailureSourceKey(id, kind, source), SourceKind: kind, SourceID: source, SourceRole: role,
		Owner: d.Owner, Action: d.Action, Reason: d.Reason, Status: "NEEDS_HUMAN", Summary: diagnosticText(summary, 2000)}
}

// Reads only: diagnosis and history cannot trigger any repair or approval.
func (s *Service) addFailureHandlingView(ctx context.Context, id string, view RequirementView, out *core.WorkflowRecoveryView) {
	if !s.automaticFailureRouting {
		return
	}
	receipts, err := s.failureReceipts(ctx, id)
	if err != nil {
		out.FailureHandling = []core.WorkflowFailureHandling{{ID: "failure-evidence-unavailable", SourceRole: core.TrustedOwnerControlPlane,
			Owner: core.TrustedOwnerHuman, Action: core.FailureHuman, Reason: "FAILURE_EVIDENCE_UNAVAILABLE", Status: "NEEDS_HUMAN"}}
		return
	}
	seen := map[string]bool{}
	for _, candidate := range s.failureCandidates(ctx, id, *out, view.ComplexExecution, receipts) {
		out.FailureHandling = append(out.FailureHandling, candidate.view)
		seen[candidate.view.SourceID] = true
	}
	for _, r := range receipts {
		if seen[r.SourceID] {
			continue
		}
		item := failureHandlingForDecision(id, r.SourceKind, r.SourceID, r.Role, r.ReasonCode, r.Summary)
		item.ID, item.Path = r.ID, r.Path
		for _, recovery := range out.History {
			if recovery.TargetID == r.SourceID || recovery.ID == r.SourceID {
				item.Historical, item.Owner, item.Action, item.Status = true, failureRecoveryRole(recovery.Action), core.FailureExisting, "RECOVERY_REGISTERED"
			}
		}
		// A historical diagnostic does not replace today's task owner or gate.
		if r.SourceKind == core.FailureSourceHandoff && view.ComplexExecution != nil {
			for _, task := range view.ComplexExecution.Tasks {
				if task.CurrentDispatchID != r.SourceID {
					continue
				}
				item.Historical = task.Status != core.DevelopmentTaskStatusBlocked && task.Status != core.DevelopmentTaskStatusNeedsHuman
			}
		}
		out.FailureHandling = append(out.FailureHandling, item)
		seen[r.SourceID] = true
	}
	// Every registered recovery has an ownership trail, including stages
	// whose original error already lives in native attempt/check/review tables.
	for _, recovery := range out.History {
		if seen[recovery.TargetID] {
			continue
		}
		item := core.WorkflowFailureHandling{ID: core.FailureSourceKey(id, "RECOVERY", recovery.ID), SourceKind: "RECOVERY", SourceID: recovery.TargetID,
			SourceRole: failureRecoveryRole(recovery.Action), Owner: failureRecoveryRole(recovery.Action), Action: core.FailureExisting, Reason: recovery.OriginalReason, Status: "RECOVERY_REGISTERED", Historical: true}
		if core.IsAutomaticFailureRecovery(recovery.ID) {
			item.Summary = "An automatic bounded recovery references the original failure. Its existence does not imply delivery or a successful review."
		}
		out.FailureHandling = append(out.FailureHandling, item)
		seen[recovery.TargetID] = true
	}
	for i := range out.FailureHandling {
		item := &out.FailureHandling[i]
		if !item.Historical {
			continue
		}
		if execution := view.ComplexExecution; execution != nil && execution.Run.CompletedAt != nil && execution.FinalReview != nil && execution.FinalReview.Status == "SETTLED" && execution.FinalReview.Verdict == "PASS" {
			item.Status = "VERIFIED"
		}
	}
	if out.Diagnosis == nil {
		return
	}
	for i := range out.Diagnosis.Issues {
		issue := &out.Diagnosis.Issues[i]
		if receipt := failureForTarget(receipts, issue.SubjectID); receipt != nil {
			issue.Summary = receipt.Summary
			if receipt.Path != "" {
				issue.Summary += "\nPath: " + receipt.Path
			}
		}
		if issue.Relationship != "CURRENT" || seen[issue.SubjectID] || issue.SubjectID == "" {
			continue
		}
		// Normal prerequisites and requested approvals are not new errors.
		if issue.Category == "PREREQUISITE" || issue.Category == "REGISTERED" || issue.Category == "HUMAN" || issue.Category == "STOPPED" {
			continue
		}
		item := failureHandlingForDecision(id, issue.SubjectType, issue.SubjectID, issue.Role, issue.ReasonCode, issue.Summary)
		if issue.Category == "OBSERVING" || issue.Category == "UNKNOWN_DELIVERY" {
			item.Owner, item.Action, item.Status = core.TrustedOwnerControlPlane, core.FailureObserve, "WAITING"
		}
		out.FailureHandling = append(out.FailureHandling, item)
		seen[issue.SubjectID] = true
	}
}

func failureRecoveryRole(action string) string {
	switch action {
	case core.RecoveryContinueBuilder, core.RecoveryRetryBuilderSession:
		return core.TrustedOwnerBuilder
	case core.RecoveryRetryReview, core.RecoveryRetryStage:
		return core.TrustedOwnerReviewer
	case core.RecoveryRetryPlanningStep:
		return core.TrustedOwnerSteward
	case core.RecoveryRetryPlannerCoordination:
		return core.TrustedOwnerPlanner
	default:
		return core.TrustedOwnerControlPlane
	}
}

// Optional project-specific runtime exclusion. A validated admitted run, not
// model text or a display option, selects it. Other runtimes remain strict.
func (s *Service) inspectExecutionCandidate(ctx context.Context, run core.ComplexExecutionRun, workspace, base string) (ports.ClearDevCandidateInspection, error) {
	if run.Mode == core.WorkModeStandard {
		if _, project, err := core.ProjectContractFromRun(run); err != nil {
			return ports.ClearDevCandidateInspection{}, err
		} else if project {
			if inspector, ok := s.inspector.(ports.ClearDevProjectCandidateInspector); ok {
				return inspector.InspectProjectCandidate(ctx, workspace, base)
			}
		}
	}
	return s.inspector.InspectCandidate(ctx, workspace, base)
}

// Verify native completion and source bytes before asking another role to act.
// Unknown turns are observed, never replaced. The database repeats its own
// current-source checks when the bridge is inserted.
func (s *Service) failureCoordinationWorkspace(ctx context.Context, e core.ComplexExecutionSnapshot, dispatchID string) (string, error) {
	if err := s.workflowRecoveryCurrent(ctx, e); err != nil {
		return "", err
	}
	d, found := complexExecutionDispatchByID(e, dispatchID)
	if !found || d.SettledAt == nil {
		return "", errors.New("failure coordination source is not settled")
	}
	record, reason := s.workflowRecoverySession(ctx, e, d, core.RecoveryContinueBuilder)
	if reason != "" || record.Activity.State != domain.ActivityIdle || s.chat == nil {
		return "", errors.New("failure coordination original Builder is unavailable or busy")
	}
	var step core.AgentStep
	for _, candidate := range e.AgentSteps {
		if candidate.ID == d.AgentStepID {
			step = candidate
		}
	}
	if step.SendStatus != core.AgentStepSendStatusSettled || coreDigest([]byte(step.FinalMessageText)) != step.MessageSHA256 {
		return "", errors.New("failure coordination has no exact original reply")
	}
	snap, err := s.chat.Snapshot(ctx, record.ID)
	if err != nil || snap.SessionID != record.ID {
		return "", errors.New("failure coordination native delivery is unknown")
	}
	completed, replied := false, false
	for _, turn := range snap.Turns {
		if !turn.State.Terminal() {
			return "", errors.New("failure coordination source still has an active turn")
		}
		completed = completed || turn.ID == step.TurnID && turn.State == domain.TurnStateCompleted
	}
	for _, message := range snap.Messages {
		replied = replied || message.ID == step.FinalMessageID && message.TurnID == step.TurnID && message.Role == domain.MessageRoleAssistant && message.Text == step.FinalMessageText
	}
	if !completed || !replied {
		return "", errors.New("failure coordination reply was not confirmed by the original session")
	}
	expected := d.CandidateCommitSHA
	if expected == "" {
		expected = d.BaseCommitSHA
		for _, old := range e.Dispatches {
			if old.ComplexExecutionTaskID == d.ComplexExecutionTaskID && old.Round < d.Round && old.CandidateCommitSHA != "" {
				expected = old.CandidateCommitSHA
			}
		}
	}
	return s.workflowWorkingTreeDigest(ctx, e, d, expected, record.Metadata.WorkspacePath)
}

func (s *Service) failureCoordinationStillCurrent(ctx context.Context, e core.ComplexExecutionSnapshot, eventID string) error {
	store, ok := s.complexExecution.(interface {
		GetClearDevFailureCoordinationSource(context.Context, string) (core.FailureCoordinationSource, bool, error)
	})
	if !ok {
		return nil
	} // Historical/non-project coordination does not use bridges.
	source, found, err := store.GetClearDevFailureCoordinationSource(ctx, eventID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if source.ExecutionRunID != e.Run.ID {
		return errors.New("failure coordination execution changed")
	}
	digest, err := s.failureCoordinationWorkspace(ctx, e, source.DispatchID)
	if err != nil {
		return err
	}
	if digest != source.WorkingTreeSHA256 {
		return errors.New("original Builder files changed during engineering coordination")
	}
	return nil
}
