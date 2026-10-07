package cleardev

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// RequirementFinalReviewPolicyV1 is frozen into NEW production executions.
// Historical executions keep their original contract. It is not a UI switch.
const RequirementFinalReviewPolicyV1 = "REQUIREMENT_FINAL_REVIEW_V1"

// RequirementFinalReviewerRole distinguishes whole-requirement progress from
// the existing task-local REVIEWER role without changing task role contracts.
const RequirementFinalReviewerRole = "REQUIREMENT_FINAL_REVIEWER"

// AgentStepRequirementFinalReview uses the existing delivery/attempt ledger,
// but is deliberately not a task LOCAL_REVIEW step.
const AgentStepRequirementFinalReview AgentStepKind = "REQUIREMENT_FINAL_REVIEW"

// RequirementFinalReview is one requirement-wide, independent review of the
// final integrated commit. Request fields and terminal results are immutable.
// A stopped V1 review may receive one native-authorized evidence recheck; old rows remain immutable.
type RequirementFinalReview struct {
	ID                       string     `json:"id"`
	PreviousReviewID         string     `json:"previousReviewId,omitempty"`
	AuthorityRequestID       string     `json:"authorityRequestId,omitempty"`
	ExecutionRunID           string     `json:"executionRunId"`
	DevelopmentRequirementID string     `json:"developmentRequirementId"`
	RequirementVersionID     string     `json:"requirementVersionId"`
	RequirementSHA256        string     `json:"requirementSha256"`
	PlanID                   string     `json:"planId"`
	PlanSHA256               string     `json:"planSha256"`
	CandidateCommitSHA       string     `json:"candidateCommitSha"`
	BaseCommitSHA            string     `json:"baseCommitSha"`
	SourceWorkspacePath      string     `json:"sourceWorkspacePath"`
	ReviewPacketJSON         string     `json:"reviewPacketJson"`
	ReviewPacketSHA256       string     `json:"reviewPacketSha256"`
	PromptSHA256             string     `json:"promptSha256"`
	CheckRunIDs              []string   `json:"checkRunIds"`
	Status                   string     `json:"status"`
	AOSessionID              string     `json:"aoSessionId,omitempty"`
	WorkspacePath            string     `json:"workspacePath,omitempty"`
	ResultID                 string     `json:"resultId,omitempty"`
	Verdict                  string     `json:"verdict,omitempty"`
	ReasonCode               ReasonCode `json:"reasonCode,omitempty"`
	Summary                  string     `json:"summary,omitempty"`
	FailureResultJSON        string     `json:"-"`
	CreatedAt                time.Time  `json:"createdAt"`
	BoundAt                  *time.Time `json:"boundAt,omitempty"`
	SentAt                   *time.Time `json:"sentAt,omitempty"`
	SettledAt                *time.Time `json:"settledAt,omitempty"`
}

// Step reconstructs the single stable transport identity from the request.
func (r RequirementFinalReview) Step() AgentStep {
	status := AgentStepSendStatusPending
	switch r.Status {
	case "SENT":
		status = AgentStepSendStatusSent
	case "SETTLED":
		status = AgentStepSendStatusSettled
	case "FAILED":
		status = AgentStepSendStatusFailed
	}
	return AgentStep{ID: r.ID + ":step", RoleBindingID: r.ID, Kind: AgentStepRequirementFinalReview,
		RequestID: r.ID, ClientMessageID: r.ID + ":message", PromptSHA256: r.PromptSHA256,
		SendStatus: status, ReasonCode: r.ReasonCode, RequestedAt: r.CreatedAt, SentAt: r.SentAt}
}

// RequirementFinalReviewReviewerChecks preserves one task Reviewer's bounded
// supplemental-check request and the trusted receipts returned to that Reviewer.
// It is review evidence only; the requirement-level Reviewer cannot request or run
// commands through this packet.
type RequirementFinalReviewReviewerChecks struct {
	ReviewID string                `json:"reviewId"`
	Request  ReviewCheckRequest    `json:"request"`
	Results  []ReviewCheckEvidence `json:"results"`
}

// RequirementFinalReviewPacket exposes the entire confirmed requirement and
// approved plan, not merely the final task's task-local review packet.
type RequirementFinalReviewPacket struct {
	WorkflowRecoveries []WorkflowRecovery                     `json:"workflowRecoveries,omitempty"`
	EvidenceFile       string                                 `json:"evidenceFile,omitempty"`
	Recovery           *WorkflowRecovery                      `json:"recovery,omitempty"`
	SchemaVersion      int                                    `json:"schemaVersion"`
	ReviewID           string                                 `json:"reviewId"`
	Requirement        RequirementVersion                     `json:"requirement"`
	Plan               ComplexEngineeringPlan                 `json:"plan"`
	Run                ComplexExecutionRun                    `json:"run"`
	CandidateCommitSHA string                                 `json:"candidateCommitSha"`
	BaseCommitSHA      string                                 `json:"baseCommitSha"`
	CheckRunIDs        []string                               `json:"checkRunIds"`
	Tasks              []ComplexExecutionTask                 `json:"tasks"`
	Dispatches         []ComplexExecutionDispatch             `json:"dispatches"`
	Verifications      []ComplexExecutionVerification         `json:"verifications"`
	TaskReviews        []ComplexExecutionReview               `json:"taskReviews"`
	ReviewerChecks     []RequirementFinalReviewReviewerChecks `json:"reviewerChecks"`
	CheckSpecs         []ComplexExecutionCheckSpecFact        `json:"checkSpecs"`
	CheckRuns          []ComplexExecutionCheckRun             `json:"checkRuns"`
	Compositions       []ComplexExecutionComposition          `json:"compositions"`
	Exception          *ComplexExceptionFacts                 `json:"exception,omitempty"`
	FixedRecoveries    []FixedRecoveryEvidence                `json:"fixedRecoveries"`
	AttemptEvidence    *FinalReviewAttemptEvidence            `json:"attemptEvidence,omitempty"`
	PreviousReview     *FinalReviewPriorConclusion            `json:"previousReview,omitempty"`
	PlannerRuntime     *PlannerRuntimeSnapshot                `json:"plannerRuntime,omitempty"`
	FunctionalTrial    bool                                   `json:"functionalTrial,omitempty"`
	TrialCLIExecutable string                                 `json:"trialCliExecutable,omitempty"`
	TrialHostExecution bool                                   `json:"trialHostExecution,omitempty"`
}

// RequirementFinalReviewResult contains ONLY the reviewer's permitted choices.
// ADR-0011 bindings come from the saved packet and actual provider turn, never
// from model-echoed requirement IDs, session IDs or hashes.
type RequirementFinalReviewResult struct {
	SchemaVersion      int    `json:"schemaVersion"`
	Kind               string `json:"kind"`
	Verdict            string `json:"verdict"`
	Summary            string `json:"summary"`
	AcceptanceSummary  string `json:"acceptanceSummary"`
	ConsistencySummary string `json:"consistencySummary"`
	ScopeSummary       string `json:"scopeSummary"`
	RegressionSummary  string `json:"regressionSummary"`
	EvidenceSummary    string `json:"evidenceSummary"`
}

// ParseRequirementFinalReviewResult does not accept task-review PASS, unknown
// fields, duplicate keys, nulls, or empty requirement-level reasoning.
func ParseRequirementFinalReviewResult(raw []byte) (RequirementFinalReviewResult, error) {
	var result RequirementFinalReviewResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if result.SchemaVersion != 1 || result.Kind != "REQUIREMENT_FINAL_REVIEW_RESULT" {
		return result, errors.New("requirement final review result kind/version is invalid")
	}
	switch result.Verdict {
	case "PASS", "REWORK", "BLOCKED", "NEEDS_HUMAN":
	default:
		return result, errors.New("requirement final review verdict is invalid")
	}
	for _, text := range []string{result.Summary, result.AcceptanceSummary, result.ConsistencySummary, result.ScopeSummary, result.RegressionSummary, result.EvidenceSummary} {
		if strings.TrimSpace(text) == "" || len(text) > 16000 {
			return result, errors.New("requirement final review needs nonempty bounded summaries for all five review dimensions")
		}
	}
	return result, nil
}

// BindRequirementFinalReviewPolicy freezes the contract before execution starts.
func BindRequirementFinalReviewPolicy(raw []byte) ([]byte, string, error) {
	var pkg ComplexExecutionRunPackage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, "", err
	}
	pkg.FinalReviewPolicy = RequirementFinalReviewPolicyV1
	encoded, err := marshalCanonicalJSON(pkg)
	return encoded, sha256Hex(encoded), err
}

// RequirementFinalReviewRequired rejects unknown policies and a changed envelope.
func RequirementFinalReviewRequired(run ComplexExecutionRun) (bool, error) {
	var pkg ComplexExecutionRunPackage
	if err := json.Unmarshal([]byte(run.ExecutionPackageJSON), &pkg); err != nil {
		return false, err
	}
	if pkg.FinalReviewPolicy == "" {
		return false, nil
	}
	if pkg.FinalReviewPolicy != RequirementFinalReviewPolicyV1 || sha256Hex([]byte(run.ExecutionPackageJSON)) != run.ExecutionPackageSHA256 ||
		pkg.ExecutionRunID != run.ID || pkg.RequirementVersionID != run.RequirementVersionID || pkg.RequirementSHA256 != run.RequirementSHA256 || pkg.PlanID != run.PlanID || pkg.PlanSHA256 != run.PlanSHA256 {
		return false, errors.New("requirement final review policy binding is invalid")
	}
	return true, nil
}

// ValidateRequirementFinalReviewBinding is shared by request, settlement and
// completion. Exact run, requirement, plan, SHA and check-set binding is mandatory.
func ValidateRequirementFinalReviewBinding(r RequirementFinalReview, run ComplexExecutionRun, sha string, checkIDs []string) error {
	if r.ID == "" || r.ExecutionRunID != run.ID || r.DevelopmentRequirementID != run.DevelopmentRequirementID ||
		r.RequirementVersionID != run.RequirementVersionID || r.RequirementSHA256 != run.RequirementSHA256 ||
		r.PlanID != run.PlanID || r.PlanSHA256 != run.PlanSHA256 || r.CandidateCommitSHA != sha ||
		!validProtocolSHA256(r.ReviewPacketSHA256) || sha256Hex([]byte(r.ReviewPacketJSON)) != r.ReviewPacketSHA256 ||
		!sameFinalReviewCheckSet(r.CheckRunIDs, checkIDs) {
		return errors.New("requirement final review identity or evidence changed")
	}
	var packet RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(r.ReviewPacketJSON), &packet); err != nil {
		return err
	}
	canonical, err := json.Marshal(packet)
	if err != nil || string(canonical) != r.ReviewPacketJSON {
		return errors.New("requirement final review packet is not canonical or contains unknown fields")
	}
	if r.PreviousReviewID != "" {
		if packet.PreviousReview == nil || packet.AttemptEvidence == nil || packet.PreviousReview.ReviewID != r.PreviousReviewID || packet.PreviousReview.AuthorityRequestID != r.AuthorityRequestID || r.ID != r.PreviousReviewID+":evidence-recheck" {
			return errors.New("final recheck predecessor binding invalid")
		}
	} else if packet.PreviousReview != nil || r.AuthorityRequestID != "" {
		return errors.New("unexpected final recheck predecessor")
	}
	if packet.SchemaVersion != 1 || packet.ReviewID != r.ID || packet.CandidateCommitSHA != sha || packet.BaseCommitSHA != r.BaseCommitSHA ||
		packet.Requirement.ID != r.RequirementVersionID || packet.Requirement.SHA256 != r.RequirementSHA256 ||
		packet.Requirement.DevelopmentRequirementID != run.DevelopmentRequirementID || sha256Hex([]byte(packet.Requirement.RequirementText)) != r.RequirementSHA256 ||
		packet.Requirement.TaskSetVersion != run.TaskSetVersion || packet.Requirement.Status != "CONFIRMED" || packet.Requirement.SupersededByID != "" ||
		packet.Plan.ID != r.PlanID || packet.Plan.PlanSHA256 != r.PlanSHA256 || sha256Hex([]byte(packet.Plan.PlanJSON)) != r.PlanSHA256 || packet.Run.ID != run.ID ||
		packet.Plan.DevelopmentRequirementID != run.DevelopmentRequirementID || packet.Plan.RequirementVersionID != r.RequirementVersionID || packet.Plan.RequirementSHA256 != r.RequirementSHA256 ||
		!reflect.DeepEqual(packet.Run.RuntimeProjectExecution, run.RuntimeProjectExecution) ||
		packet.Run.DevelopmentRequirementID != run.DevelopmentRequirementID || packet.Run.ExecutionPackageSHA256 != run.ExecutionPackageSHA256 || packet.Run.ExecutionPackageJSON != run.ExecutionPackageJSON ||
		!sameFinalReviewCheckSet(packet.CheckRunIDs, checkIDs) || validateRequirementFinalReviewerChecks(run, packet.TaskReviews, packet.ReviewerChecks) != nil {
		return errors.New("requirement final review packet does not match its immutable request")
	}
	if packet.EvidenceFile != "" && (!filepath.IsAbs(packet.EvidenceFile) || strings.ContainsAny(packet.EvidenceFile, "\x00\r\n")) {
		return errors.New("final review evidence requires an absolute file path")
	}
	if packet.TrialHostExecution && (!packet.FunctionalTrial || packet.TrialCLIExecutable == "") {
		return errors.New("host trial execution requires a functional trial and its frozen CLI")
	}
	if packet.TrialCLIExecutable != "" && (!packet.FunctionalTrial || !filepath.IsAbs(packet.TrialCLIExecutable) || strings.ContainsAny(packet.TrialCLIExecutable, "\x00\r\n")) {
		return errors.New("trial CLI executable requires a functional trial and an absolute program path")
	}
	if packet.FunctionalTrial {
		if _, project, err := ProjectContractFromRun(run); err != nil || !project {
			return errors.New("functional trial requires an admitted project runtime")
		}
	}
	if blocked, _ := PlannerRuntimeBarrier(ComplexExecutionSnapshot{Run: run, PlannerRuntime: packet.PlannerRuntime, Tasks: packet.Tasks, Dispatches: packet.Dispatches}); blocked {
		return errors.New("requirement final review requires complete, resolved Planner coordination history")
	}
	return nil
}

func validateRequirementFinalReviewerChecks(run ComplexExecutionRun, reviews []ComplexExecutionReview, bundles []RequirementFinalReviewReviewerChecks) error {
	reviewIDs := make(map[string]bool, len(reviews))
	for _, review := range reviews {
		if review.ID == "" || reviewIDs[review.ID] {
			return errors.New("requirement final review task review identity is invalid")
		}
		reviewIDs[review.ID] = true
	}
	seen := make(map[string]bool, len(bundles))
	for _, bundle := range bundles {
		if bundle.ReviewID == "" || !reviewIDs[bundle.ReviewID] || seen[bundle.ReviewID] || bundle.Request.ReviewID != bundle.ReviewID || len(bundle.Request.CheckIDs) == 0 {
			return errors.New("requirement final review Reviewer-check identity is invalid")
		}
		seen[bundle.ReviewID] = true
		if err := ValidateReviewCheckRequestForRun(bundle.Request, run); err != nil {
			return err
		}
		for _, review := range reviews {
			if review.ID == bundle.ReviewID && (review.CandidateCommitID != bundle.Request.CandidateID || review.CandidateCommitSHA != bundle.Request.CandidateSHA || review.ReviewPacketSHA256 != bundle.Request.PacketSHA256 || review.AgentStepID != bundle.Request.RequestStepID) {
				return errors.New("requirement final review supplemental checks belong to another task candidate")
			}
		}
		checkIDs := make(map[string]bool, len(bundle.Request.CheckIDs))
		for _, id := range bundle.Request.CheckIDs {
			if _, ok := ReviewCheckSpec(bundle.Request, id); !ok || checkIDs[id] {
				return errors.New("requirement final review Reviewer-check request is invalid")
			}
			checkIDs[id] = true
		}
		resultIDs := make(map[string]bool, len(bundle.Results))
		for _, result := range bundle.Results {
			if resultIDs[result.CheckID] || result.ReviewID != bundle.ReviewID || ValidateReviewCheckEvidence(bundle.Request, result) != nil {
				return errors.New("requirement final review Reviewer-check evidence is invalid")
			}
			resultIDs[result.CheckID] = true
		}
	}
	return nil
}

func sameFinalReviewCheckSet(a, b []string) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, id := range a {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	for _, id := range b {
		if !seen[id] {
			return false
		}
		delete(seen, id)
	}
	return len(seen) == 0
}

func requirementFinalReviewPassed(review *RequirementFinalReview) bool {
	return review != nil && review.Status == "SETTLED" && review.Verdict == "PASS" && review.ResultID != ""
}

func requirementFinalReviewStop(snapshot ComplexExecutionSnapshot) (ComplexExecutionPhase, ReasonCode, bool) {
	// Unmarked historical snapshots retain their original derived contract.
	if snapshot.Run.ExecutionPackageJSON == "" {
		return "", ReasonNone, false
	}
	required, err := RequirementFinalReviewRequired(snapshot.Run)
	if err != nil {
		return ComplexExecutionBlocked, "REQUIREMENT_FINAL_REVIEW_BINDING_INVALID", true
	}
	if !required {
		return "", ReasonNone, false
	}
	if snapshot.Run.CompletedAt == nil {
		for _, binding := range snapshot.RoleBindings {
			if binding.Role == StandardRoleBuilder && (binding.Status == RoleBindingStatusEnded || binding.Status == RoleBindingStatusFailed) &&
				(strings.HasPrefix(string(binding.ReasonCode), "REQUIREMENT_FINAL_") || binding.ReasonCode == "MAIL_FINAL_CANDIDATE_CHANGED") {
				return ComplexExecutionBlocked, binding.ReasonCode, true
			}
		}
	}
	if review := snapshot.FinalReview; review != nil {
		if review.Status == "FAILED" {
			return ComplexExecutionBlocked, review.ReasonCode, true
		}
		if review.Status == "SETTLED" && review.Verdict != "PASS" {
			if review.Verdict == "REWORK" || review.Verdict == "NEEDS_HUMAN" || (review.Verdict == "BLOCKED" && (BuilderFirstFailureEnabled(snapshot.Run) || finalReviewManuallyReturned(snapshot))) {
				// The verdict judges the candidate its review bound to. Once
				// the human dispatched a rework round, that round must run to
				// produce a fresh candidate instead of being held by the old
				// verdict; a first round with no rework budget spent still
				// stops here for the human decision.
				// The verdict judges the final candidate, which the last task
				// produces. Only that task's rework changes it; earlier tasks
				// finishing their own rounds never release the stop.
				finalTaskID := ""
				finalOrdinal := -1
				for _, task := range snapshot.Tasks {
					if task.Status == DevelopmentTaskStatusDone || task.Status == DevelopmentTaskStatusCancelled {
						continue
					}
					if task.Ordinal >= finalOrdinal {
						finalOrdinal, finalTaskID = task.Ordinal, task.ID
					}
				}
				for _, task := range snapshot.Tasks {
					if task.Status == DevelopmentTaskStatusDone || task.Status == DevelopmentTaskStatusCancelled {
						continue
					}
					if task.ID != finalTaskID {
						continue
					}
					if task.ReworkCount > task.CurrentRound && (!BuilderFirstFailureEnabled(snapshot.Run) || FinalReviewTaskReturned(snapshot, task)) {
						return "", ReasonNone, false
					}
					currentCandidate := ""
					activeDispatch := false
					for _, dispatch := range snapshot.Dispatches {
						if dispatch.ComplexExecutionTaskID == task.ID {
							currentCandidate = dispatch.CandidateCommitSHA
							if dispatch.Status == ComplexExecutionDispatchPending || dispatch.Status == ComplexExecutionDispatchRunning || dispatch.Status == ComplexExecutionDispatchObserved || ((BuilderFirstFailureEnabled(snapshot.Run) || FinalReviewTaskReturned(snapshot, task)) && finalReviewPredatesDispatch(*review, dispatch)) {
								activeDispatch = true
							}
						}
					}
					// A rework round that is already dispatched (its pending
					// or running attempt exists) is exactly the signal that
					// the old verdict no longer holds for the coming candidate.
					if task.CurrentRound > 0 && (activeDispatch || (currentCandidate != "" && currentCandidate != review.CandidateCommitSHA)) {
						return "", ReasonNone, false
					}
				}
				if snapshot.FinalReviewReworkCount > ComplexFinalReviewMaxReworkCount {
					return ComplexExecutionBlocked, ReasonFinalReviewReworkLimit, true
				}
				return ComplexExecutionNeedsHuman, review.ReasonCode, true
			}
			return ComplexExecutionBlocked, review.ReasonCode, true
		}
	}
	if snapshot.Integration != nil && snapshot.Run.CompletedAt != nil {
		if !requirementFinalReviewPassed(snapshot.FinalReview) || ValidateRequirementFinalReviewBinding(*snapshot.FinalReview, snapshot.Run, snapshot.Integration.CandidateCommitSHA, snapshot.Integration.CheckRunIDs) != nil {
			return ComplexExecutionBlocked, "REQUIREMENT_FINAL_REVIEW_PASS_REQUIRED", true
		}
	}
	return "", ReasonNone, false
}

// FinalReviewTaskReturned compares the current rework count with the counts
// frozen in the review. It grants no automatic return to old runs.
func FinalReviewTaskReturned(snapshot ComplexExecutionSnapshot, task ComplexExecutionTask) bool {
	review := snapshot.FinalReview
	if review == nil || review.Status != "SETTLED" || (review.Verdict != "BLOCKED" && review.Verdict != "REWORK") || snapshot.Run.CompletedAt != nil {
		return false
	}
	if _, project, err := ProjectContractFromRun(snapshot.Run); err != nil || !project || snapshot.Run.Mode != WorkModeStandard ||
		ValidateRequirementFinalReviewBinding(*review, snapshot.Run, review.CandidateCommitSHA, review.CheckRunIDs) != nil {
		return false
	}
	var packet RequirementFinalReviewPacket
	if json.Unmarshal([]byte(review.ReviewPacketJSON), &packet) != nil {
		return false
	}
	// Counts can already exceed the verified round before this review (for
	// example after a technical recovery). Only a change after its frozen
	// snapshot means the task was returned by this review.
	frozenReworkCount := -1
	for _, other := range packet.Tasks {
		if other.Ordinal > task.Ordinal {
			return false
		}
		if other.ID == task.ID {
			frozenReworkCount = other.ReworkCount
		}
	}
	if frozenReworkCount < 0 {
		return false
	}
	for _, verification := range packet.Verifications {
		if verification.ComplexExecutionTaskID == task.ID && verification.CandidateCommitSHA == review.CandidateCommitSHA {
			return task.ReworkCount > max(frozenReworkCount, verification.Round)
		}
	}
	return false
}

func finalReviewManuallyReturned(snapshot ComplexExecutionSnapshot) bool {
	for _, task := range snapshot.Tasks {
		if FinalReviewTaskReturned(snapshot, task) {
			return true
		}
	}
	return false
}

// A returned Builder's terminal diagnosis is newer than the review that caused
// the return; expose that diagnosis instead of retaining the old trial stop.
func finalReviewPredatesDispatch(review RequirementFinalReview, dispatch ComplexExecutionDispatch) bool {
	var packet RequirementFinalReviewPacket
	if json.Unmarshal([]byte(review.ReviewPacketJSON), &packet) != nil {
		return false
	}
	for _, task := range packet.Tasks {
		if task.ID == dispatch.ComplexExecutionTaskID {
			return dispatch.Round > task.CurrentRound
		}
	}
	return false
}
