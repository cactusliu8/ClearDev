package cleardev

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Direction-change message limits and event vocabulary.
const (
	MaxDirectionMessageRunes                  = 8000
	MaxDirectionAffectedIDs                   = 50
	MaxDirectionChangeSummary                 = 500
	DirectionChangeDecisionStop               = "REQUEST_STOP_AND_REVISE"
	DirectionAgentStepChange                  = AgentStepKind("DIRECTION_CHANGE_REQUEST")
	SubjectDirectionIntent        SubjectType = "DIRECTION_INTENT"
	SubjectDirectionRequest       SubjectType = "DIRECTION_REQUEST"
	SubjectDirectionStopGate      SubjectType = "DIRECTION_STOP_GATE"
	SubjectDirectionSnapshot      SubjectType = "DIRECTION_TASK_SNAPSHOT"
	SubjectDirectionProcessing    SubjectType = "DIRECTION_TASK_PROCESSING"
	SubjectDirectionCheckpoint    SubjectType = "DIRECTION_CHECKPOINT"
	SubjectDirectionCompilation   SubjectType = "DIRECTION_COMPILATION"
	ActionCreateDirectionIntent   Action      = "CREATE_DIRECTION_INTENT"
	ActionCreateDirectionRequest  Action      = "CREATE_DIRECTION_REQUEST"
	ActionCreateDirectionGate     Action      = "CREATE_DIRECTION_STOP_GATE"
	ActionCloseDirectionGate      Action      = "CLOSE_DIRECTION_STOP_GATE"
	ActionOccupyDirectionTask     Action      = "OCCUPY_DIRECTION_TASK"
	ActionFinalizeDirectionTask   Action      = "FINALIZE_DIRECTION_TASK"
	ActionSaveDirectionCheckpoint Action      = "SAVE_DIRECTION_CHECKPOINT"
)

// Stable reason codes recorded on direction-change checkpoints and cancellations.
const (
	ReasonDirectionUnknownAfterRestart ReasonCode = "UNKNOWN_AFTER_RESTART"
	ReasonDirectionInterruptFailed     ReasonCode = "INTERRUPT_FAILED"
	ReasonDirectionObserveFailed       ReasonCode = "OBSERVE_FAILED"
	ReasonDirectionNoSession           ReasonCode = "NO_SESSION"
	ReasonDirectionNoBaseline          ReasonCode = "NO_BASELINE"
	ReasonDirectionNoWorktree          ReasonCode = "NO_WORKTREE"
	ReasonDirectionDirtyWorktree       ReasonCode = "DIRTY_WORKTREE"
	ReasonDirectionCandidateInvalid    ReasonCode = "CANDIDATE_PROOF_FAILED"
	ReasonDirectionPlannedUnstarted    ReasonCode = "PLANNED_UNSTARTED"
	ReasonDirectionTerminalUnchanged   ReasonCode = "TERMINAL_UNCHANGED"
)

// DirectionChangePhase is derived from durable S05 facts on every read.
type DirectionChangePhase string

// Derived direction-change phases returned to readers. They are not stored.
const (
	DirectionChangeNone                  DirectionChangePhase = ""
	DirectionChangeAwaitingSteward       DirectionChangePhase = "AWAITING_STEWARD"
	DirectionChangeAwaitingDecision      DirectionChangePhase = "AWAITING_DECISION"
	DirectionChangeProcessing            DirectionChangePhase = "PROCESSING"
	DirectionChangeCompiling             DirectionChangePhase = "COMPILING"
	DirectionChangeAwaitingClarification DirectionChangePhase = "AWAITING_CLARIFICATION"
	DirectionChangeAwaitingConfirmation  DirectionChangePhase = "AWAITING_CONFIRMATION"
	DirectionChangePlanning              DirectionChangePhase = "PLANNING"
	DirectionChangeAwaitingPlanReview    DirectionChangePhase = "AWAITING_PLAN_REVIEW"
	DirectionChangeApproved              DirectionChangePhase = "APPROVED"
	DirectionChangeRejected              DirectionChangePhase = "REJECTED"
	DirectionChangeNeedsHuman            DirectionChangePhase = "NEEDS_HUMAN"
)

// DirectionStopGateStatus is the durable state of a v1 stop gate.
type DirectionStopGateStatus string

// Direction stop-gate status values.
const (
	DirectionStopGateActive DirectionStopGateStatus = "ACTIVE"
	DirectionStopGateClosed DirectionStopGateStatus = "CLOSED"
)

// DirectionOccupancy tracks whether a snapshot task is waiting, in hand, or done.
type DirectionOccupancy string

// Occupancy values for one frozen v1 task.
const (
	DirectionOccupancyPending  DirectionOccupancy = "PENDING"
	DirectionOccupancyOccupied DirectionOccupancy = "OCCUPIED"
	DirectionOccupancyFinal    DirectionOccupancy = "FINAL"
)

// DirectionCheckpointKind classifies the Git/session evidence saved for a task.
type DirectionCheckpointKind string

// Checkpoint kinds recorded after observing a task worktree.
const (
	DirectionCheckpointCleanCandidate DirectionCheckpointKind = "CLEAN_CANDIDATE"
	DirectionCheckpointDirtyPreserved DirectionCheckpointKind = "DIRTY_PRESERVED"
	DirectionCheckpointNoWorktree     DirectionCheckpointKind = "NO_WORKTREE"
	DirectionCheckpointNoSession      DirectionCheckpointKind = "NO_SESSION"
	DirectionCheckpointUnstarted      DirectionCheckpointKind = "UNSTARTED"
	DirectionCheckpointTerminal       DirectionCheckpointKind = "TERMINAL"
	DirectionCheckpointFailedObserve  DirectionCheckpointKind = "OBSERVE_FAILED"
)

// DirectionCancelResult records whether cancel changed a non-terminal task.
type DirectionCancelResult string

// Cancel results stored on a finalized direction-task processing row.
const (
	DirectionCancelCancelled         DirectionCancelResult = "CANCELLED"
	DirectionCancelUnchangedTerminal DirectionCancelResult = "UNCHANGED_TERMINAL"
)

// DirectionIntent is the immutable user message handed to the original Steward.
type DirectionIntent struct {
	RequestID                string    `json:"requestId"`
	DevelopmentRequirementID string    `json:"developmentRequirementId"`
	RequirementVersionID     string    `json:"requirementVersionId"`
	RequirementSHA256        string    `json:"requirementSha256"`
	Message                  string    `json:"message"`
	MessageSHA256            string    `json:"messageSha256"`
	StewardRoleBindingID     string    `json:"stewardRoleBindingId"`
	DirectionRequestID       string    `json:"directionRequestId"`
	AgentStepID              string    `json:"agentStepId"`
	CreatedAt                time.Time `json:"createdAt"`
}

// DirectionRequest is the Steward's validated stop-and-revise request.
type DirectionRequest struct {
	ID                       string    `json:"id"`
	IntentRequestID          string    `json:"intentRequestId"`
	DevelopmentRequirementID string    `json:"developmentRequirementId"`
	RequirementVersionID     string    `json:"requirementVersionId"`
	Summary                  string    `json:"summary"`
	AffectedRequirementIDs   []string  `json:"affectedRequirementIds"`
	ResultSHA256             string    `json:"resultSha256"`
	AgentStepID              string    `json:"agentStepId"`
	CreatedAt                time.Time `json:"createdAt"`
}

// DirectionStopGate is the v1 write barrier installed with a direction request.
type DirectionStopGate struct {
	ID                       string                  `json:"id"`
	DirectionRequestID       string                  `json:"directionRequestId"`
	DevelopmentRequirementID string                  `json:"developmentRequirementId"`
	RequirementVersionID     string                  `json:"requirementVersionId"`
	TaskSetVersion           int64                   `json:"taskSetVersion"`
	SnapshotSHA256           string                  `json:"snapshotSha256"`
	Status                   DirectionStopGateStatus `json:"status"`
	ClosedAt                 *time.Time              `json:"closedAt,omitempty"`
	CloseReason              ReasonCode              `json:"closeReason,omitempty"`
	CreatedAt                time.Time               `json:"createdAt"`
}

// DirectionTaskSnapshotItem is one frozen v1 task captured when the gate is installed.
type DirectionTaskSnapshotItem struct {
	TaskID           string                `json:"taskId"`
	Status           DevelopmentTaskStatus `json:"status"`
	PausedFromStatus DevelopmentTaskStatus `json:"pausedFromStatus,omitempty"`
	Ordinal          int                   `json:"ordinal"`
}

// DirectionTaskProcessing is the unique handler row for one snapshot task.
type DirectionTaskProcessing struct {
	ID                 string                `json:"id"`
	DirectionRequestID string                `json:"directionRequestId"`
	TaskID             string                `json:"taskId"`
	Occupancy          DirectionOccupancy    `json:"occupancy"`
	InterruptResult    string                `json:"interruptResult,omitempty"`
	CheckpointID       string                `json:"checkpointId,omitempty"`
	CancelResult       DirectionCancelResult `json:"cancelResult,omitempty"`
	ReasonCode         ReasonCode            `json:"reasonCode,omitempty"`
	OccupiedAt         *time.Time            `json:"occupiedAt,omitempty"`
	FinalizedAt        *time.Time            `json:"finalizedAt,omitempty"`
}

// DirectionCheckpoint is the Git/session evidence saved before cancel.
type DirectionCheckpoint struct {
	ID                string                  `json:"id"`
	ProcessingID      string                  `json:"processingId"`
	TaskID            string                  `json:"taskId"`
	SessionID         string                  `json:"sessionId,omitempty"`
	WorktreePath      string                  `json:"worktreePath,omitempty"`
	BaselineSHA       string                  `json:"baselineSha,omitempty"`
	HeadSHA           string                  `json:"headSha,omitempty"`
	Dirty             bool                    `json:"dirty"`
	Staged            bool                    `json:"staged"`
	Untracked         bool                    `json:"untracked"`
	ChangeSummaryJSON string                  `json:"changeSummaryJson"`
	CandidateCommitID string                  `json:"candidateCommitId,omitempty"`
	Kind              DirectionCheckpointKind `json:"kind"`
	CreatedAt         time.Time               `json:"createdAt"`
}

// DirectionRevision binds the approved direction request to the target v2 identity.
type DirectionRevision struct {
	DirectionRequestID           string    `json:"directionRequestId"`
	PreviousRequirementVersionID string    `json:"previousRequirementVersionId"`
	TargetRequirementVersionID   string    `json:"targetRequirementVersionId"`
	CreatedAt                    time.Time `json:"createdAt"`
}

// DirectionChangeSnapshot is the durable S05 fact set loaded for one requirement.
type DirectionChangeSnapshot struct {
	Intent                *DirectionIntent            `json:"intent,omitempty"`
	Request               *DirectionRequest           `json:"request,omitempty"`
	Gate                  *DirectionStopGate          `json:"gate,omitempty"`
	Snapshots             []DirectionTaskSnapshotItem `json:"snapshots,omitempty"`
	Processings           []DirectionTaskProcessing   `json:"processings,omitempty"`
	Checkpoints           []DirectionCheckpoint       `json:"checkpoints,omitempty"`
	Revision              *DirectionRevision          `json:"revision,omitempty"`
	DecisionRequestID     string                      `json:"decisionRequestId,omitempty"`
	DecisionContentSHA256 string                      `json:"decisionContentSha256,omitempty"`
	// OccupancyApproved is loaded from the settled desktop APPROVE. It is not a stored phase.
	OccupancyApproved   bool                           `json:"-"`
	AgentSteps          []AgentStep                    `json:"agentSteps,omitempty"`
	CompilationRequests []ComplexCompilationRequest    `json:"compilationRequests,omitempty"`
	Questions           []ComplexClarificationQuestion `json:"questions,omitempty"`
	Answers             []ComplexClarificationAnswer   `json:"answers,omitempty"`
	Compilations        []ComplexCompilation           `json:"compilations,omitempty"`
	IDMaps              []ComplexIDMap                 `json:"idMaps,omitempty"`
}

// DirectionChangeRequestResult is the exact Steward JSON accepted for a gate.
type DirectionChangeRequestResult struct {
	SchemaVersion               int      `json:"schemaVersion"`
	Kind                        string   `json:"kind"`
	DirectionRequestID          string   `json:"directionRequestId"`
	DevelopmentRequirementID    string   `json:"developmentRequirementId"`
	CurrentRequirementVersionID string   `json:"currentRequirementVersionId"`
	CurrentRequirementSHA256    string   `json:"currentRequirementSha256"`
	UserMessageSHA256           string   `json:"userMessageSha256"`
	Decision                    string   `json:"decision"`
	Summary                     string   `json:"summary"`
	AffectedRequirementIDs      []string `json:"affectedRequirementIds"`
}

// CreateDirectionIntentCommand writes the idempotent user message and Steward step.
type CreateDirectionIntentCommand struct {
	Intent DirectionIntent
	Step   AgentStep
}

// AcceptDirectionChangeCommand installs the request, snapshots, gate, and decision.
type AcceptDirectionChangeCommand struct {
	Intent            DirectionIntent
	Request           DirectionRequest
	Gate              DirectionStopGate
	AgentStep         AgentStep
	DecisionRequestID string
	At                time.Time
}

// RecordDirectionClarificationCommand stores v2 compile questions for this request.
type RecordDirectionClarificationCommand struct {
	DirectionRequestID string
	Request            ComplexCompilationRequest
	Questions          []ComplexClarificationQuestion
}

// SettleDirectionCompilationCommand records a v2 compile result and optional draft version.
type SettleDirectionCompilationCommand struct {
	DirectionRequestID string
	Request            ComplexCompilationRequest
	Compilation        ComplexCompilation
	IDMaps             []ComplexIDMap
	Version            RequirementVersion
	At                 time.Time
}

// OccupyDirectionTaskCommand claims one snapshot task so cancel cannot run twice.
type OccupyDirectionTaskCommand struct {
	ProcessingID string
	At           time.Time
}

// FinalizeDirectionTaskCommand writes checkpoint, optional candidate, and cancel together.
type FinalizeDirectionTaskCommand struct {
	Processing     DirectionTaskProcessing
	Checkpoint     *DirectionCheckpoint
	Candidate      *CandidateCommit
	CancelTask     bool
	TaskFromStatus DevelopmentTaskStatus
	At             time.Time
}

// CreateDirectionRevisionCommand records the v1-to-v2 identity mapping.
type CreateDirectionRevisionCommand struct {
	Revision DirectionRevision
}

// ValidateDirectionMessage trims the user message and rejects empty or oversized text.
func ValidateDirectionMessage(message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" || utf8.RuneCountInString(message) > MaxDirectionMessageRunes {
		return "", errors.New("direction message is empty or too long")
	}
	return message, nil
}

// DirectionMessageSHA256 hashes the exact accepted user message.
func DirectionMessageSHA256(message string) string {
	return sha256Hex([]byte(message))
}

// ParseDirectionChangeRequest accepts only the frozen Steward stop-and-revise JSON.
func ParseDirectionChangeRequest(
	raw []byte,
	directionRequestID, requirementID, versionID, versionSHA, messageSHA string,
) (DirectionChangeRequestResult, error) {
	var result DirectionChangeRequestResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw,
		"schemaVersion", "kind", "decision", "summary", "affectedRequirementIds"); err != nil {
		return result, err
	}
	result.DirectionRequestID = strings.TrimSpace(directionRequestID)
	result.DevelopmentRequirementID = strings.TrimSpace(requirementID)
	result.CurrentRequirementVersionID = strings.TrimSpace(versionID)
	result.CurrentRequirementSHA256 = strings.TrimSpace(versionSHA)
	result.UserMessageSHA256 = strings.TrimSpace(messageSHA)
	result.Decision = strings.TrimSpace(result.Decision)
	result.Summary = strings.TrimSpace(result.Summary)
	if result.SchemaVersion != ComplexProtocolVersion || result.Kind != string(DirectionAgentStepChange) {
		return result, errors.New("direction change result kind is not supported")
	}
	if result.Decision != DirectionChangeDecisionStop {
		return result, errors.New("direction change decision is not REQUEST_STOP_AND_REVISE")
	}
	if !validText(result.Summary, MaxComplexAnswerRunes) {
		return result, errors.New("direction change summary is empty or too long")
	}
	if !validProtocolSHA256(result.CurrentRequirementSHA256) || !validProtocolSHA256(result.UserMessageSHA256) {
		return result, errors.New("direction change hashes are invalid")
	}
	if len(result.AffectedRequirementIDs) > MaxDirectionAffectedIDs {
		return result, errors.New("direction change affected requirement list is too long")
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(result.AffectedRequirementIDs))
	for _, id := range result.AffectedRequirementIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return result, errors.New("direction change affected requirement id is empty")
		}
		if _, exists := seen[id]; exists {
			return result, errors.New("direction change affected requirement ids are duplicated")
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	result.AffectedRequirementIDs = normalized
	return result, nil
}

// HashDirectionChangeRequest is the digest bound into the desktop decision offer.
func HashDirectionChangeRequest(result DirectionChangeRequestResult) (string, error) {
	raw, err := marshalCanonicalJSON(result)
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}

// HashDirectionTaskSnapshot is the SHA-256 stored on the stop gate.
func HashDirectionTaskSnapshot(items []DirectionTaskSnapshotItem) (string, error) {
	cloned := append([]DirectionTaskSnapshotItem(nil), items...)
	slices.SortFunc(cloned, func(a, b DirectionTaskSnapshotItem) int {
		return strings.Compare(a.TaskID, b.TaskID)
	})
	material := make([]map[string]string, 0, len(cloned))
	for _, item := range cloned {
		material = append(material, map[string]string{
			"pausedFromStatus": string(item.PausedFromStatus),
			"status":           string(item.Status),
			"taskId":           item.TaskID,
		})
	}
	raw, err := marshalCanonicalJSON(material)
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}

// BoundChangeSummary keeps a dirty-worktree summary inside the stored size limit.
func BoundChangeSummary(changes []map[string]string) (string, error) {
	if len(changes) > MaxDirectionChangeSummary {
		changes = append([]map[string]string(nil), changes[:MaxDirectionChangeSummary]...)
	}
	if changes == nil {
		changes = []map[string]string{}
	}
	raw, err := json.Marshal(changes)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DeriveDirectionChangePhase computes the reader phase from durable S05 facts.
func DeriveDirectionChangePhase(
	snapshot DirectionChangeSnapshot,
	versions []RequirementVersion,
	plans []ComplexEngineeringPlan,
	reviews []ComplexPlanReview,
) DirectionChangePhase {
	if snapshot.Intent == nil {
		return DirectionChangeNone
	}
	if snapshot.Request == nil {
		return DirectionChangeAwaitingSteward
	}
	if snapshot.Gate != nil && snapshot.Gate.Status == DirectionStopGateClosed {
		return DirectionChangeRejected
	}
	if !directionOccupancyComplete(snapshot) {
		return DirectionChangeAwaitingDecision
	}
	if !AllDirectionProcessingsFinal(snapshot) || snapshot.Revision == nil {
		return DirectionChangeProcessing
	}
	if unanswered := unansweredDirectionRequest(snapshot); unanswered != nil {
		return DirectionChangeAwaitingClarification
	}
	target := versionByID(versions, snapshot.Revision.TargetRequirementVersionID)
	if target == nil {
		if latest, ok := latestDirectionCompilation(snapshot); ok && latest.Outcome == "NEEDS_HUMAN" {
			return DirectionChangeNeedsHuman
		}
		return DirectionChangeCompiling
	}
	if target.Status == RequirementVersionStatusRejected {
		return DirectionChangeNeedsHuman
	}
	if target.Status == RequirementVersionStatusPendingConfirmation {
		return DirectionChangeAwaitingConfirmation
	}
	if target.Status != RequirementVersionStatusConfirmed {
		return DirectionChangeCompiling
	}
	plan, hasPlan := planForVersion(plans, target.ID)
	if !hasPlan {
		return DirectionChangePlanning
	}
	review, hasReview := reviewForPlan(reviews, plan.ID)
	if !hasReview {
		return DirectionChangeAwaitingPlanReview
	}
	if review.Verdict == PlanReviewApproved {
		return DirectionChangeApproved
	}
	if review.Verdict == PlanReviewNeedsHuman {
		return DirectionChangeNeedsHuman
	}
	return DirectionChangePlanning
}

func unansweredDirectionRequest(snapshot DirectionChangeSnapshot) *ComplexCompilationRequest {
	for i := len(snapshot.CompilationRequests) - 1; i >= 0; i-- {
		request := snapshot.CompilationRequests[i]
		questions := 0
		answers := 0
		for _, question := range snapshot.Questions {
			if question.CompilationRequestID == request.ID {
				questions++
			}
		}
		for _, answer := range snapshot.Answers {
			if answer.CompilationRequestID == request.ID {
				answers++
			}
		}
		if questions > 0 && answers < questions {
			cloned := request
			return &cloned
		}
	}
	return nil
}

func latestDirectionCompilation(snapshot DirectionChangeSnapshot) (ComplexCompilation, bool) {
	if len(snapshot.Compilations) == 0 {
		return ComplexCompilation{}, false
	}
	latest := snapshot.Compilations[0]
	for _, item := range snapshot.Compilations[1:] {
		if item.CreatedAt.After(latest.CreatedAt) || (item.CreatedAt.Equal(latest.CreatedAt) && item.ID > latest.ID) {
			latest = item
		}
	}
	return latest, true
}

func versionByID(versions []RequirementVersion, id string) *RequirementVersion {
	for i := range versions {
		if versions[i].ID == id {
			return &versions[i]
		}
	}
	return nil
}

func planForVersion(plans []ComplexEngineeringPlan, versionID string) (ComplexEngineeringPlan, bool) {
	var latest ComplexEngineeringPlan
	found := false
	for _, plan := range plans {
		if plan.RequirementVersionID != versionID {
			continue
		}
		if !found || plan.Version > latest.Version {
			latest = plan
			found = true
		}
	}
	return latest, found
}

func reviewForPlan(reviews []ComplexPlanReview, planID string) (ComplexPlanReview, bool) {
	for _, review := range reviews {
		if review.PlanID == planID {
			return review, true
		}
	}
	return ComplexPlanReview{}, false
}

// DirectionCompilationContext is the Steward prompt payload for compiling v2.
func DirectionCompilationContext(
	requirementID, targetVersionID, previousVersionID, previousSHA, originalPRDSHA string,
	intent DirectionIntent,
	request DirectionRequest,
	decisionRequestID, decisionContentSHA string,
	existingRequirementIDs, existingAcceptanceIDs []string,
	previousRounds []CompilationContextRound,
) CompilationContext {
	return CompilationContext{
		SchemaVersion:                  ComplexProtocolVersion,
		DevelopmentRequirementID:       requirementID,
		TargetRequirementVersionID:     targetVersionID,
		SourcePRDSHA256:                originalPRDSHA,
		PreviousRequirementVersionID:   previousVersionID,
		PreviousRequirementSHA256:      previousSHA,
		DirectionIntentID:              intent.RequestID,
		UserMessageSHA256:              intent.MessageSHA256,
		DirectionRequestID:             request.ID,
		DirectionRequestSHA256:         request.ResultSHA256,
		DirectionDecisionRequestID:     decisionRequestID,
		DirectionDecisionContentSHA256: decisionContentSHA,
		ExistingRequirementIDs:         append([]string(nil), existingRequirementIDs...),
		ExistingAcceptanceIDs:          append([]string(nil), existingAcceptanceIDs...),
		PreviousRounds:                 previousRounds,
	}
}

// StableIDsFromDocument lists requirement and acceptance IDs already assigned.
func StableIDsFromDocument(doc NormalizedRequirementDocument) (requirementIDs, acceptanceIDs []string) {
	for _, item := range doc.Requirements {
		requirementIDs = append(requirementIDs, item.ID)
	}
	for _, item := range doc.AcceptanceScenarios {
		acceptanceIDs = append(acceptanceIDs, item.ID)
	}
	return requirementIDs, acceptanceIDs
}

// DirectionAgentStepByRequest finds the step for one kind and request identity.
func DirectionAgentStepByRequest(snapshot DirectionChangeSnapshot, kind AgentStepKind, requestID string) (AgentStep, bool) {
	for _, step := range snapshot.AgentSteps {
		if step.Kind == kind && step.RequestID == requestID {
			return step, true
		}
	}
	return AgentStep{}, false
}

// NextDirectionClarificationRound is the next 0111 compile round number.
func NextDirectionClarificationRound(snapshot DirectionChangeSnapshot) int {
	if unanswered := unansweredDirectionRequest(snapshot); unanswered != nil {
		return unanswered.ClarificationRound
	}
	if len(snapshot.CompilationRequests) == 0 {
		return 0
	}
	latest := snapshot.CompilationRequests[len(snapshot.CompilationRequests)-1]
	return latest.ClarificationRound + 1
}

// DirectionChangeNeedsResume is true when boot must continue an unfinished change.
func DirectionChangeNeedsResume(snapshot DirectionChangeSnapshot, versions []RequirementVersion, plans []ComplexEngineeringPlan, reviews []ComplexPlanReview) bool {
	phase := DeriveDirectionChangePhase(snapshot, versions, plans, reviews)
	switch phase {
	case DirectionChangeNone, DirectionChangeApproved, DirectionChangeRejected, DirectionChangeNeedsHuman,
		DirectionChangeAwaitingDecision, DirectionChangeAwaitingClarification, DirectionChangeAwaitingConfirmation:
		return false
	default:
		return true
	}
}

// DirectionProcessingByTask returns the unique processing row for a snapshot task.
func DirectionProcessingByTask(snapshot DirectionChangeSnapshot, taskID string) (DirectionTaskProcessing, bool) {
	for _, item := range snapshot.Processings {
		if item.TaskID == taskID {
			return item, true
		}
	}
	return DirectionTaskProcessing{}, false
}

// directionOccupancyComplete is true after desktop approve has claimed every
// frozen v1 task. A direction that froze no tasks is complete once approved.
func directionOccupancyComplete(snapshot DirectionChangeSnapshot) bool {
	if len(snapshot.Snapshots) == 0 {
		return snapshot.OccupancyApproved
	}
	return len(snapshot.Processings) > 0
}

// AllDirectionProcessingsFinal is true when every snapshot task has a final row.
func AllDirectionProcessingsFinal(snapshot DirectionChangeSnapshot) bool {
	if len(snapshot.Processings) == 0 {
		return len(snapshot.Snapshots) == 0 && snapshot.OccupancyApproved
	}
	for _, item := range snapshot.Processings {
		if item.Occupancy != DirectionOccupancyFinal {
			return false
		}
	}
	return true
}

// FormatDirectionStopReason is the stable reader string for a checkpoint reason.
func FormatDirectionStopReason(code ReasonCode) string {
	if code == ReasonNone {
		return ""
	}
	return fmt.Sprintf("direction change stopped this requirement version (%s)", code)
}

// DirectionCompilationRounds rebuilds compile-round context from 0111 facts.
func DirectionCompilationRounds(snapshot DirectionChangeSnapshot) []CompilationContextRound {
	rounds := make([]CompilationContextRound, 0, len(snapshot.CompilationRequests))
	for _, request := range snapshot.CompilationRequests {
		questions := []CompilationContextQuestion{}
		answers := []CompilationContextAnswer{}
		for _, question := range snapshot.Questions {
			if question.CompilationRequestID != request.ID {
				continue
			}
			questions = append(questions, CompilationContextQuestion{
				Key: question.QuestionKey, Text: question.Text, Reason: question.Reason,
				RequirementKeys: append([]string(nil), question.RequirementKeys...),
			})
		}
		for _, answer := range snapshot.Answers {
			if answer.CompilationRequestID != request.ID {
				continue
			}
			answers = append(answers, CompilationContextAnswer{QuestionKey: answer.QuestionKey, Text: answer.Text})
		}
		if len(questions) == 0 || len(answers) < len(questions) {
			continue
		}
		rounds = append(rounds, CompilationContextRound{
			ClarificationRound: request.ClarificationRound, CompilationRequestID: request.ID,
			AdditionalRoundReason: request.AdditionalRoundReason, Questions: questions, Answers: answers,
		})
	}
	return rounds
}
