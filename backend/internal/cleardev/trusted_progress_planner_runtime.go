package cleardev

import (
	"encoding/json"
	"slices"
)

// TrustedPlannerCoordination projects persisted runtime facts into the existing
// progress summary. Reported observations remain attributed to the Builder;
// only applied packet rows are presented as effective contract revisions.
type TrustedPlannerCoordination struct {
	CheckRecovery             *StoppedCheckRecovery            `json:"checkRecovery,omitempty"`
	ExtraCoordinationGrant    *ExtraCoordinationGrant          `json:"extraCoordinationGrant,omitempty"`
	ExtraCoordinationDecision *PlannerCoordinationDecision     `json:"extraCoordinationDecision,omitempty"`
	Recovery                  *WorkflowRecovery                `json:"recovery,omitempty"`
	RecoveryDecision          *PlannerCoordinationDecision     `json:"recoveryDecision,omitempty"`
	EventID                   string                           `json:"eventId"`
	DispatchID                string                           `json:"dispatchId"`
	SourceTaskID              string                           `json:"sourceTaskId"`
	SourceCandidateSHA        string                           `json:"sourceCandidateSha,omitempty"`
	Category                  string                           `json:"category"`
	ReportedSummary           string                           `json:"reportedSummary"`
	ReportedEvidence          []string                         `json:"reportedEvidence"`
	AffectedTaskKeys          []string                         `json:"affectedTaskKeys"`
	CoordinationRound         int                              `json:"coordinationRound"`
	MaxCoordinationRounds     int                              `json:"maxCoordinationRounds"`
	PlannerRoleBindingID      string                           `json:"plannerRoleBindingId,omitempty"`
	DecisionSource            string                           `json:"decisionSource,omitempty"`
	Decision                  string                           `json:"decision,omitempty"`
	DecisionSHA256            string                           `json:"decisionSha256,omitempty"`
	Summary                   string                           `json:"summary,omitempty"`
	Questions                 []string                         `json:"questions,omitempty"`
	ReasonCode                ReasonCode                       `json:"reasonCode,omitempty"`
	AppliedContracts          []TrustedPlannerContractRevision `json:"appliedContracts"`
}

// TrustedPlannerContractRevision is an actual append-only contract result,
// never a proposed amendment that failed deterministic application.
type TrustedPlannerContractRevision struct {
	ID                     string `json:"id"`
	TaskMappingID          string `json:"taskMappingId"`
	TaskKey                string `json:"taskKey"`
	PreviousPackageSHA256  string `json:"previousPackageSha256"`
	EffectivePackageSHA256 string `json:"effectivePackageSha256"`
}

func applyTrustedPlannerRuntime(summary *TrustedProgressSummary, facts TrustedProgressFacts) {
	execution := facts.ComplexExecution
	if execution == nil || execution.PlannerRuntime == nil {
		return
	}
	enabled, err := PlannerRuntimeRun(execution.Run)
	if err != nil || !enabled {
		return
	}
	history := execution.PlannerRuntime
	for _, event := range history.Events {
		item := TrustedPlannerCoordination{EventID: event.ID, DispatchID: event.DispatchID, Category: event.Report.Category,
			ReportedSummary: event.Report.Summary, ReportedEvidence: event.Report.Evidence, AffectedTaskKeys: event.Report.AffectedTaskKeys,
			MaxCoordinationRounds: PlannerRuntimeMaxRounds, ReasonCode: ReasonPlannerRuntimePending, AppliedContracts: []TrustedPlannerContractRevision{}}
		for _, dispatch := range execution.Dispatches {
			if dispatch.ID == event.DispatchID {
				item.SourceTaskID = dispatch.DevelopmentTaskID
				item.SourceCandidateSHA = dispatch.CandidateCommitSHA
				break
			}
		}
		for _, request := range history.Requests {
			if request.EventID == event.ID {
				item.CoordinationRound, item.PlannerRoleBindingID = request.Ordinal, request.PlannerRoleBindingID
				break
			}
		}
		for _, decision := range history.Decisions {
			if decision.EventID != event.ID {
				continue
			}
			item.DecisionSource, item.Decision, item.DecisionSHA256 = decision.Source, decision.Outcome, decision.ResultSHA256
			item.ReasonCode, item.Summary = decision.ReasonCode, decision.Summary
			var result PlannerCoordinationResult
			if decision.Source == "PLANNER" && decision.Outcome == PlannerRuntimeProduct && json.Unmarshal([]byte(decision.ResultJSON), &result) == nil {
				item.Questions = result.Questions
			}
			break
		}
		for _, amendment := range history.Amendments {
			if amendment.EventID != event.ID {
				continue
			}
			key := ""
			for _, task := range execution.Tasks {
				if task.ID == amendment.TaskMappingID {
					key = task.TaskKey
					break
				}
			}
			item.AppliedContracts = append(item.AppliedContracts, TrustedPlannerContractRevision{ID: amendment.ID,
				TaskMappingID: amendment.TaskMappingID, TaskKey: key, PreviousPackageSHA256: amendment.PreviousPackageSHA256,
				EffectivePackageSHA256: amendment.ExecutionPackageSHA256})
		}
		for _, r := range history.Recoveries {
			if r.EventID == event.ID {
				recovery := r.Recovery
				item.Recovery = &recovery
			}
		}
		for _, d := range history.RecoveryDecisions {
			if d.EventID == event.ID {
				decision := d
				item.RecoveryDecision = &decision
			}
		}
		for _, grant := range history.ExtraCoordinationGrants {
			if grant.EventID == event.ID {
				g := grant
				item.ExtraCoordinationGrant = &g
				item.MaxCoordinationRounds = ExtraCoordinationOrdinal
			}
		}
		for _, d := range history.ExtraCoordinationDecisions {
			if d.EventID == event.ID {
				decision := d
				item.ExtraCoordinationDecision = &decision
			}
		}
		for _, g := range history.CheckRecoveries {
			if g.EventID == event.ID {
				recovery := g
				item.CheckRecovery = &recovery
			}
		}
		summary.PlannerCoordination = append(summary.PlannerCoordination, item)
		issue := TrustedIssue{Kind: "PLANNER_RUNTIME_COORDINATION", SubjectType: "PLANNER_RUNTIME_EVENT", SubjectID: event.ID, ReasonCode: item.ReasonCode}
		effective, decided := PlannerRuntimeEffectiveDecision(history, event.ID)
		if !decided {
			issue.ReasonCode = ReasonPlannerRuntimePending
			summary.CurrentWork = append(summary.CurrentWork, issue)
		} else if !StoppedCheckRecoveryResolvesStop(*execution, effective) && effective.ReasonCode != ReasonNone && (effective.Source != "CONTROL_PLANE" || effective.Outcome != PlannerRuntimeStop || !slices.Contains(history.RepairAuthorizations, event.ID)) {
			issue.ReasonCode = effective.ReasonCode
			summary.Blockers = append(summary.Blockers, issue)
		}
	}
}

func plannerRuntimeCitableFacts(items []TrustedPlannerCoordination) []TrustedCitableFact {
	out := []TrustedCitableFact{}
	for _, item := range items {
		out = append(out, TrustedCitableFact{ID: item.EventID, Kind: "PLANNER_RUNTIME_EVENT"})
		for _, amendment := range item.AppliedContracts {
			out = append(out, TrustedCitableFact{ID: amendment.ID, Kind: "PLANNER_RUNTIME_CONTRACT_REVISION"})
		}
	}
	return out
}

// plannerRuntimeControlledSteps excludes historical initial-planning work and
// resolved runtime requests. It retains the exact logical-step/session binding
// so the shared progress code can show bounded retries and delivery uncertainty.
func plannerRuntimeControlledSteps(execution ComplexExecutionSnapshot, planning *ComplexPlanningSnapshot) ([]AgentStep, map[string]ControlledWork) {
	steps := []AgentStep{}
	bindings := map[string]ControlledWork{}
	if planning == nil || execution.PlannerRuntime == nil {
		return steps, bindings
	}
	for _, request := range execution.PlannerRuntime.Requests {
		_, resolved := PlannerRuntimeEffectiveDecision(execution.PlannerRuntime, request.EventID)
		if resolved {
			continue
		}
		for _, binding := range planning.RoleBindings {
			if binding.ID == request.PlannerRoleBindingID && binding.AOSessionID == request.AOSessionID && binding.Role == StandardRoleEngineeringPlanner {
				bindings[binding.ID] = ControlledWork{ExecutionRunID: execution.Run.ID, RoleBindingID: binding.ID, Role: string(binding.Role), AOSessionID: binding.AOSessionID}
				break
			}
		}
		for _, step := range planning.AgentSteps {
			if step.ID == request.AgentStepID && step.RequestID == request.EventID && step.Kind == ComplexAgentStepEngineeringPlan {
				steps = append(steps, step)
				break
			}
		}
	}
	return steps, bindings
}
