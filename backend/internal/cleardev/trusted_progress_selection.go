package cleardev

import (
	"sort"
	"strings"
)

// SelectTrustedProgressFacts excludes historical executions without changing stored evidence.
func SelectTrustedProgressFacts(f TrustedProgressFacts) TrustedProgressFacts {
	current, pending, _ := requirementVersions(f.Snapshot.RequirementVersions)
	matches := func(version string, accepted, expected int64) bool {
		return current != nil && pending == nil && version == current.ID && (accepted == current.TaskSetVersion || accepted == 0 && expected == current.TaskSetVersion)
	}
	if f.ComplexExecution != nil && !matches(f.ComplexExecution.Run.RequirementVersionID, f.ComplexExecution.Run.TaskSetVersion, f.ComplexExecution.Run.ExpectedTaskSetVersion) {
		f.ComplexExecution = nil
	}
	if f.QuickExecution != nil && !matches(f.QuickExecution.Run.RequirementVersionID, f.QuickExecution.Run.TaskSetVersion, f.QuickExecution.Run.ExpectedTaskSetVersion) {
		f.QuickExecution = nil
	}
	if f.ComplexExecution != nil && f.QuickExecution != nil {
		if f.QuickExecution.Run.SourceExecutionRunID == f.ComplexExecution.Run.ID {
			f.ComplexExecution = nil
		} else {
			f.QuickExecution = nil
		}
	}
	if f.StandardFlow != nil && (current == nil || pending != nil || f.StandardFlow.RequirementVersionID != current.ID || f.ComplexExecution != nil || f.QuickExecution != nil) {
		f.StandardFlow = nil
	}
	f.LatestControlledPreflight = nil
	return f
}

// CurrentControlledTargets identifies current business steps before reading their attempt evidence.
func CurrentControlledTargets(input TrustedProgressFacts) []ControlledProgressFacts {
	f := SelectTrustedProgressFacts(input)
	if controlledDirectionActive(f) {
		f.ComplexExecution, f.QuickExecution, f.StandardFlow = nil, nil, nil
	}
	current, pending, _ := requirementVersions(f.Snapshot.RequirementVersions)
	version := ""
	if current != nil {
		version = current.ID
	}
	if pending != nil {
		version = pending.ID
	}
	targets := []ControlledProgressFacts{}
	if f.Snapshot.Requirement.CancelledAt != nil {
		return targets
	}
	add := func(w ControlledWork, step *AgentStep) {
		w.RequirementVersionID = version
		targets = append(targets, ControlledProgressFacts{Target: w, Step: step})
	}
	activeTask := func(id string) bool {
		for _, t := range f.Snapshot.DevelopmentTasks {
			if t.ID == id {
				return t.RequirementVersionID == version && t.Status != DevelopmentTaskStatusDone && t.Status != DevelopmentTaskStatusCancelled
			}
		}
		return false
	}
	addSteps := func(steps []AgentStep, bindings map[string]ControlledWork, category AgentStepCategory) {
		for i := range steps {
			s := steps[i]
			w, ok := bindings[s.RoleBindingID]
			if !ok || s.SendStatus == AgentStepSendStatusSettled {
				continue
			}
			w.LogicalStepID, w.StepCategory = s.ID, category
			add(w, &s)
		}
		for id, w := range bindings {
			has := false
			for _, s := range steps {
				if s.RoleBindingID == id {
					has = true
				}
			}
			if !has && w.AOSessionID == "" {
				add(w, nil)
			}
		}
	}
	if e := f.ComplexExecution; e != nil {
		if e.Run.CompletedAt != nil {
			return targets
		}
		runtimeSteps, runtimeBindings := plannerRuntimeControlledSteps(*e, f.ComplexPlanning)
		addSteps(runtimeSteps, runtimeBindings, AgentStepCategoryComplexPlanning)
		if review := e.FinalReview; review != nil && review.Status != "SETTLED" {
			if required, err := RequirementFinalReviewRequired(e.Run); err == nil && required &&
				ValidateRequirementFinalReviewBinding(*review, e.Run, review.CandidateCommitSHA, review.CheckRunIDs) == nil {
				step := review.Step()
				add(ControlledWork{ExecutionRunID: e.Run.ID, RoleBindingID: review.ID,
					Role: RequirementFinalReviewerRole, AOSessionID: review.AOSessionID,
					LogicalStepID: step.ID, StepCategory: AgentStepCategoryComplexExecution,
					ReviewID: review.ID, CandidateSHA: review.CandidateCommitSHA}, &step)
			}
		}
		tasks := map[string]ComplexExecutionTask{}
		dispatches := map[string]ComplexExecutionDispatch{}
		for _, t := range e.Tasks {
			if activeTask(t.DevelopmentTaskID) {
				tasks[t.ID] = t
			}
		}
		for _, d := range e.Dispatches {
			if t, ok := tasks[d.ComplexExecutionTaskID]; ok && t.CurrentDispatchID == d.ID && t.CurrentRound == d.Round {
				dispatches[d.ID] = d
			}
		}
		bindings := map[string]ControlledWork{}
		for _, b := range e.RoleBindings {
			if b.Status == RoleBindingStatusEnded {
				continue
			}
			if b.TaskMappingID != "" {
				if _, ok := tasks[b.TaskMappingID]; !ok {
					continue
				}
			}
			if b.CandidateCommitID != "" {
				currentCandidate := false
				for _, d := range dispatches {
					if d.CandidateCommitID == b.CandidateCommitID && (b.TaskMappingID == "" || b.TaskMappingID == d.ComplexExecutionTaskID) {
						currentCandidate = true
					}
				}
				if !currentCandidate {
					continue
				}
			}
			bindings[b.ID] = ControlledWork{ExecutionRunID: e.Run.ID, TaskMappingID: b.TaskMappingID, RoleBindingID: b.ID, Role: string(b.Role), AOSessionID: b.AOSessionID, CandidateID: b.CandidateCommitID}
			if b.Status == RoleBindingStatusFailed {
				w := bindings[b.ID]
				w.State, w.ReasonCode = "FAILED", b.ReasonCode
				bindings[b.ID] = w
			}
		}
		for i := range e.AgentSteps {
			step := e.AgentSteps[i]
			if step.SendStatus == AgentStepSendStatusSettled || mailReplacementHasFollowup(*e, step) || MailReplacementResolvedOriginalStep(*e, step) {
				continue
			}
			w, ok := bindings[step.RoleBindingID]
			// The original Reviewer may have ended; its request is retained for the authorized second attempt.
			if !ok {
				for _, r := range e.FixedRecoveries {
					if r.Request.LogicalStepID == step.ID && r.ReviewResult == nil {
						w = ControlledWork{ExecutionRunID: e.Run.ID, RoleBindingID: r.Request.RoleBindingID, Role: string(StandardRoleReviewer), AOSessionID: r.Request.SessionID}
						ok = true
					}
				}
			}
			if !ok {
				continue
			}
			selected := step.Kind == AgentStepDispatchRequest && e.Run.Decision == ""
			for _, d := range dispatches {
				if d.AgentStepID != step.ID {
					continue
				}
				selected = true
				w.DispatchID, w.TaskMappingID = d.ID, d.ComplexExecutionTaskID
				w.CandidateID, w.CandidateSHA = d.CandidateCommitID, d.CandidateCommitSHA
			}
			for _, r := range e.Reviews {
				d, yes := dispatches[r.DispatchID]
				if !yes || (r.AgentStepID != step.ID && (ReviewCheckFollowupID(r.ID) != step.ID || step.RoleBindingID != r.ReviewerRoleBindingID && !MailReplacementCheckFollowup(*e, r, step))) || d.CandidateCommitID != r.CandidateCommitID || r.Verdict != "" {
					continue
				}
				selected = true
				w.DispatchID, w.TaskMappingID = d.ID, d.ComplexExecutionTaskID
				w.CandidateID, w.CandidateSHA, w.ReviewID = r.CandidateCommitID, r.CandidateCommitSHA, r.ID
			}

			if !selected {
				continue
			}
			if t, yes := tasks[w.TaskMappingID]; yes {
				w.DevelopmentTaskID = t.DevelopmentTaskID
			}
			w.LogicalStepID, w.StepCategory = step.ID, AgentStepCategoryComplexExecution
			entry := ControlledProgressFacts{Target: w, Step: &step}
			entry.Target.RequirementVersionID = version
			for i := range e.FixedRecoveries {
				r := e.FixedRecoveries[i]
				if r.Request.LogicalStepID == step.ID && r.Request.DispatchID == w.DispatchID && r.Request.CandidateID == w.CandidateID {
					entry.Recovery = &r
					entry.ProposalID = controlledRecoveryProposal(*e, r.Request)
				}
			}
			targets = append(targets, entry)
		}
		for id, w := range bindings {
			if w.AOSessionID != "" {
				continue
			}
			has := false
			for _, t := range targets {
				if t.Target.RoleBindingID == id {
					has = true
				}
			}
			if !has {
				if t, ok := tasks[w.TaskMappingID]; ok {
					w.DevelopmentTaskID = t.DevelopmentTaskID
				}
				add(w, nil)
			}
		}
		if e.Exception != nil {
			eb := map[string]ControlledWork{}
			for _, b := range e.Exception.OnDemandBindings {
				if b.Status == RoleBindingStatusEnded {
					continue
				}
				if b.ComplexExecutionTaskID != "" {
					if _, ok := tasks[b.ComplexExecutionTaskID]; !ok {
						continue
					}
				}
				w := ControlledWork{ExecutionRunID: e.Run.ID, TaskMappingID: b.ComplexExecutionTaskID, RoleBindingID: b.ID, Role: b.Mode, AOSessionID: b.AOSessionID}
				if t, ok := tasks[b.ComplexExecutionTaskID]; ok {
					w.DevelopmentTaskID = t.DevelopmentTaskID
				}
				eb[b.ID] = w
			}
			addSteps(e.Exception.OnDemandSteps, eb, AgentStepCategoryException)
			for _, step := range e.Exception.OnDemandSteps {
				d, ok := dispatches[step.RequestID]
				if !ok || step.Kind != ComplexExceptionAgentStepBuilderContinue || step.SendStatus == AgentStepSendStatusSettled {
					continue
				}
				w, ok := bindings[step.RoleBindingID]
				if !ok {
					continue
				}
				w.LogicalStepID, w.StepCategory = step.ID, AgentStepCategoryException
				w.DispatchID, w.TaskMappingID, w.DevelopmentTaskID = d.ID, d.ComplexExecutionTaskID, d.DevelopmentTaskID
				w.CandidateID, w.CandidateSHA, w.RequirementVersionID = d.CandidateCommitID, d.CandidateCommitSHA, version
				entry := ControlledProgressFacts{Target: w, Step: &step}
				for _, r := range e.FixedRecoveries {
					if r.Request.LogicalStepID == step.ID && r.Request.DispatchID == d.ID {
						entry.Recovery = &r
						entry.ProposalID = controlledRecoveryProposal(*e, r.Request)
					}
				}
				targets = append(targets, entry)
			}

		}
		for i := range e.FixedRecoveries {
			r := e.FixedRecoveries[i]
			if r.Request.CheckRunID == "" || r.Result != nil && r.Result.Outcome == "PASS" {
				continue
			}
			targets = append(targets, ControlledProgressFacts{Target: ControlledWork{RequirementVersionID: version, ExecutionRunID: e.Run.ID, TaskMappingID: r.Request.TaskID, CandidateID: r.Request.CandidateID, CandidateSHA: r.Request.CandidateSHA, RecoveryRequestID: r.Request.ID}, Recovery: &r, ProposalID: controlledRecoveryProposal(*e, r.Request)})
		}
	} else if q := f.QuickExecution; q != nil {
		if q.Run.CompletedAt != nil {
			return targets
		}
		bindings := map[string]ControlledWork{}
		for _, b := range q.RoleBindings {
			if b.Status != RoleBindingStatusEnded {
				w := ControlledWork{ExecutionRunID: q.Run.ID, RoleBindingID: b.ID, Role: string(b.Role), AOSessionID: b.AOSessionID}
				if q.Task != nil {
					w.TaskMappingID = q.Task.ID
					w.DevelopmentTaskID = q.Task.DevelopmentTaskID
				}
				bindings[b.ID] = w
			}
		}
		steps := []AgentStep{}
		for _, s := range q.AgentSteps {
			selected := q.Task == nil
			for _, d := range q.Dispatches {
				if q.Task != nil && d.ID == q.Task.CurrentDispatchID && d.AgentStepID == s.ID {
					selected = true
				}
			}
			if selected {
				steps = append(steps, s)
			}
		}
		addSteps(steps, bindings, AgentStepCategoryQuickExecution)
	} else if st := f.StandardFlow; st != nil {
		bindings := map[string]ControlledWork{}
		candidates := currentTaskCandidateIDs(f.Snapshot, version)
		for _, b := range st.RoleBindings {
			if b.Status == RoleBindingStatusEnded || b.RequirementVersionID != version {
				continue
			}
			if b.DevelopmentTaskID != "" && !activeTask(b.DevelopmentTaskID) {
				continue
			}
			if b.CandidateCommitID != "" && candidates[b.DevelopmentTaskID] != b.CandidateCommitID {
				continue
			}
			w := ControlledWork{RoleBindingID: b.ID, Role: string(b.Role), AOSessionID: b.AOSessionID, DevelopmentTaskID: b.DevelopmentTaskID, DispatchID: b.DispatchID, CandidateID: b.CandidateCommitID}
			// A baseline failure precedes the first Builder step (and usually
			// its session). Derive the blocker from the existing failed dispatch
			// rather than displaying a requested role as READY indefinitely.
			for _, d := range st.Dispatches {
				if b.Role == StandardRoleBuilder && d.ID == b.DispatchID && d.BuilderRoleBindingID == b.ID && d.Status == DispatchStatusFailed && strings.HasPrefix(string(d.ReasonCode), "BASELINE_") {
					w.State, w.ReasonCode = "FAILED", d.ReasonCode
				}
			}
			if w.State == "FAILED" {
				add(w, nil)
				continue
			}
			bindings[b.ID] = w
		}
		addSteps(st.AgentSteps, bindings, AgentStepCategoryStandard)
	}
	if f.ComplexExecution == nil && f.QuickExecution == nil && f.ComplexPlanning != nil {
		bindings := map[string]ControlledWork{}
		for _, b := range f.ComplexPlanning.RoleBindings {
			if b.Status != RoleBindingStatusEnded {
				w := ControlledWork{RoleBindingID: b.ID, Role: string(b.Role), AOSessionID: b.AOSessionID}
				if b.Status == RoleBindingStatusFailed {
					w.State, w.ReasonCode = "FAILED", b.ReasonCode
				}
				bindings[b.ID] = w
			}
		}
		if !controlledDirectionActive(f) {
			addSteps(f.ComplexPlanning.AgentSteps, bindings, AgentStepCategoryComplexPlanning)
		}
		if controlledDirectionActive(f) {
			addSteps(f.DirectionChange.AgentSteps, bindings, AgentStepCategoryDirection)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		a, b := targets[i].Target, targets[j].Target
		if a.LogicalStepID != b.LogicalStepID {
			return a.LogicalStepID < b.LogicalStepID
		}
		return a.RoleBindingID < b.RoleBindingID
	})
	return targets
}

func controlledDirectionActive(f TrustedProgressFacts) bool {
	if f.DirectionChange == nil || f.ComplexPlanning == nil {
		return false
	}
	p := DeriveDirectionChangePhase(*f.DirectionChange, f.Snapshot.RequirementVersions, f.ComplexPlanning.Plans, f.ComplexPlanning.Reviews)
	return p != DirectionChangeNone && p != DirectionChangeApproved && p != DirectionChangeRejected
}

// A proposal is bound to the failed fact, not the recovery request ID.
// This only projects the pending owner; the existing claim transaction authorizes execution.
func controlledRecoveryProposal(e ComplexExecutionSnapshot, r FixedRecoveryRequest) string {
	if e.Exception == nil || r.ExecutionRunID != e.Run.ID {
		return ""
	}
	trigger := r.FailureEventID
	if r.CheckRunID != "" {
		trigger = r.CheckRunID
	}
	if trigger == "" {
		return ""
	}
	for _, p := range e.Exception.RecoveryActions {
		allowed := p.Action == ComplexRecoveryActionRestoreSession || p.Action == ComplexRecoveryActionRebuildReviewer && r.ReviewID != ""
		if r.CheckRunID != "" {
			allowed = p.Action == ComplexRecoveryActionRetryInfraCheck
		}
		if p.ID != "" && p.ExecutionRunID == r.ExecutionRunID && p.ComplexExecutionTaskID == r.TaskID && p.TriggerFactID == trigger && p.Outcome == "PASS" && allowed {
			return p.ID
		}
	}
	return ""
}
