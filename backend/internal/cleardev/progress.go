package cleardev

import "time"

// OverallPhase is the derived execution position of a development requirement.
type OverallPhase string

const (
	OverallPhaseCancelled            OverallPhase = "CANCELLED"
	OverallPhaseAwaitingConfirmation OverallPhase = "AWAITING_CONFIRMATION"
	OverallPhaseDefiningRequirement  OverallPhase = "DEFINING_REQUIREMENT"
	OverallPhasePlanningTasks        OverallPhase = "PLANNING_TASKS"
	OverallPhaseDeveloping           OverallPhase = "DEVELOPING"
	OverallPhaseVerifying            OverallPhase = "VERIFYING"
	OverallPhaseIntegrating          OverallPhase = "INTEGRATING"
	OverallPhaseCompleted            OverallPhase = "COMPLETED"
)

// OverallAttention is a derived exceptional condition, separate from phase.
type OverallAttention string

const (
	OverallAttentionNone              OverallAttention = "NONE"
	OverallAttentionNeedsHuman        OverallAttention = "NEEDS_HUMAN"
	OverallAttentionBlocked           OverallAttention = "BLOCKED"
	OverallAttentionRework            OverallAttention = "REWORK"
	OverallAttentionIntegrationFailed OverallAttention = "INTEGRATION_FAILED"
)

// TaskCounts contains only non-cancelled tasks bound to the current confirmed
// requirement version.
type TaskCounts struct {
	Planned    int `json:"planned"`
	Running    int `json:"running"`
	Review     int `json:"review"`
	Rework     int `json:"rework"`
	NeedsHuman int `json:"needsHuman"`
	Blocked    int `json:"blocked"`
	Done       int `json:"done"`
}

// Total returns the number of tasks represented by this count.
func (c TaskCounts) Total() int {
	return c.Planned + c.Running + c.Review + c.Rework + c.NeedsHuman + c.Blocked + c.Done
}

// MissingEvidence identifies a concrete proof still needed for a derived
// completion result. Kind is intentionally a stable machine value rather than
// a human summary.
type MissingEvidence struct {
	Kind                   string `json:"kind" enum:"INTEGRATION_CANDIDATE,INTEGRATION_RESULT"`
	DevelopmentTaskID      string `json:"developmentTaskId,omitempty"`
	IntegrationCandidateID string `json:"integrationCandidateId,omitempty"`
}

const (
	MissingEvidenceIntegrationCandidate = "INTEGRATION_CANDIDATE"
	MissingEvidenceIntegrationResult    = "INTEGRATION_RESULT"
)

// OverallProgress is computed at read time and is never independently stored.
type OverallProgress struct {
	Phase                       OverallPhase      `json:"phase" enum:"CANCELLED,AWAITING_CONFIRMATION,DEFINING_REQUIREMENT,PLANNING_TASKS,DEVELOPING,VERIFYING,INTEGRATING,COMPLETED"`
	Attention                   OverallAttention  `json:"attention" enum:"NONE,NEEDS_HUMAN,BLOCKED,REWORK,INTEGRATION_FAILED"`
	CurrentRequirementVersionID string            `json:"currentRequirementVersionId,omitempty"`
	PendingRequirementVersionID string            `json:"pendingRequirementVersionId,omitempty"`
	TaskSetVersion              *int64            `json:"taskSetVersion,omitempty"`
	TaskCounts                  TaskCounts        `json:"taskCounts"`
	MissingEvidence             []MissingEvidence `json:"missingEvidence"`
}

// DeriveOverallProgress calculates the complete requirement read model from
// durable facts. In particular, it never consults the legacy project state.
func DeriveOverallProgress(snapshot RequirementSnapshot, now time.Time) OverallProgress {
	if snapshot.Requirement.CancelledAt != nil {
		return OverallProgress{Phase: OverallPhaseCancelled, Attention: OverallAttentionNone}
	}

	current, pending, latest := requirementVersions(snapshot.RequirementVersions)
	progress := OverallProgress{Attention: OverallAttentionNone}
	if current != nil {
		progress.CurrentRequirementVersionID = current.ID
		taskSetVersion := current.TaskSetVersion
		progress.TaskSetVersion = &taskSetVersion
	}
	if pending != nil {
		progress.PendingRequirementVersionID = pending.ID
	}

	if current == nil {
		if latest != nil && latest.Status == RequirementVersionStatusPendingConfirmation {
			progress.Phase = OverallPhaseAwaitingConfirmation
		} else {
			progress.Phase = OverallPhaseDefiningRequirement
		}
		if pending != nil {
			progress.Attention = OverallAttentionNeedsHuman
		}
		return progress
	}

	currentTasks := currentDevelopmentTasks(snapshot.DevelopmentTasks, current.ID)
	progress.TaskCounts = countTasks(currentTasks)
	matchingCandidate, integrationResult := currentIntegrationResult(snapshot, *current, now)
	if progress.TaskCounts.Total() == 0 {
		progress.Phase = OverallPhasePlanningTasks
	} else if hasDevelopmentTask(currentTasks) {
		progress.Phase = OverallPhaseDeveloping
	} else if progress.TaskCounts.Review > 0 {
		progress.Phase = OverallPhaseVerifying
	} else if integrationResult == EvidenceResultPass {
		progress.Phase = OverallPhaseCompleted
	} else {
		progress.Phase = OverallPhaseIntegrating
		if matchingCandidate == nil {
			progress.MissingEvidence = []MissingEvidence{{Kind: MissingEvidenceIntegrationCandidate}}
		} else if integrationResult == "" {
			progress.MissingEvidence = []MissingEvidence{{Kind: MissingEvidenceIntegrationResult, IntegrationCandidateID: matchingCandidate.ID}}
		}
	}

	// Attention intentionally has a different fixed priority from phase.
	if pending != nil || progress.TaskCounts.NeedsHuman > 0 {
		progress.Attention = OverallAttentionNeedsHuman
	} else if progress.TaskCounts.Blocked > 0 {
		progress.Attention = OverallAttentionBlocked
	} else if progress.TaskCounts.Rework > 0 {
		progress.Attention = OverallAttentionRework
	} else if integrationResult == EvidenceResultFail {
		progress.Attention = OverallAttentionIntegrationFailed
	}
	return progress
}

func requirementVersions(versions []RequirementVersion) (current, pending, latest *RequirementVersion) {
	for index := range versions {
		version := &versions[index]
		if latest == nil || version.Version > latest.Version {
			latest = version
		}
		if version.Status == RequirementVersionStatusConfirmed && (current == nil || version.Version > current.Version) {
			current = version
		}
		if version.Status == RequirementVersionStatusPendingConfirmation && (pending == nil || version.Version > pending.Version) {
			pending = version
		}
	}
	return current, pending, latest
}

func currentDevelopmentTasks(tasks []DevelopmentTask, requirementVersionID string) []DevelopmentTask {
	current := make([]DevelopmentTask, 0, len(tasks))
	for _, task := range tasks {
		if task.RequirementVersionID == requirementVersionID && task.Status != DevelopmentTaskStatusCancelled {
			current = append(current, task)
		}
	}
	return current
}

func countTasks(tasks []DevelopmentTask) TaskCounts {
	var counts TaskCounts
	for _, task := range tasks {
		switch task.Status {
		case DevelopmentTaskStatusPlanned:
			counts.Planned++
		case DevelopmentTaskStatusRunning:
			counts.Running++
		case DevelopmentTaskStatusReview:
			counts.Review++
		case DevelopmentTaskStatusRework:
			counts.Rework++
		case DevelopmentTaskStatusNeedsHuman:
			counts.NeedsHuman++
		case DevelopmentTaskStatusBlocked:
			counts.Blocked++
		case DevelopmentTaskStatusDone:
			counts.Done++
		}
	}
	return counts
}

func hasDevelopmentTask(tasks []DevelopmentTask) bool {
	for _, task := range tasks {
		switch task.Status {
		case DevelopmentTaskStatusPlanned, DevelopmentTaskStatusRunning, DevelopmentTaskStatusRework, DevelopmentTaskStatusBlocked, DevelopmentTaskStatusNeedsHuman:
			return true
		}
	}
	return false
}

func currentIntegrationResult(snapshot RequirementSnapshot, current RequirementVersion, now time.Time) (*IntegrationCandidate, EvidenceResult) {
	var candidate *IntegrationCandidate
	for index := range snapshot.IntegrationCandidates {
		value := &snapshot.IntegrationCandidates[index]
		if value.RequirementVersionID != current.ID || value.TaskSetVersion == nil || *value.TaskSetVersion != current.TaskSetVersion {
			continue
		}
		if candidate == nil || value.Sequence > candidate.Sequence {
			candidate = value
		}
	}
	if candidate == nil {
		return nil, ""
	}
	var latest *EvidenceRecord
	for index := range snapshot.Evidence {
		evidence := &snapshot.Evidence[index]
		if evidence.Kind != EvidenceKindIntegration || evidence.IntegrationCandidateID != candidate.ID || evidence.CommitSHA != candidate.CommitSHA || evidenceExpiredRecord(*evidence, now) {
			continue
		}
		if latest == nil || evidence.Sequence > latest.Sequence {
			latest = evidence
		}
	}
	if latest == nil {
		return candidate, ""
	}
	return candidate, latest.Result
}

func evidenceExpiredRecord(record EvidenceRecord, now time.Time) bool {
	return record.ExpiresAt != nil && !now.Before(*record.ExpiresAt)
}
