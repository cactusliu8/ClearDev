package cleardev

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ComplexParallelMinBuilders and ComplexParallelMaxBuilders freeze the only
// S07 parallel team sizes. Four or more builders stay out of scope.
const (
	ComplexParallelMinBuilders = 2
	ComplexParallelMaxBuilders = 3
)

// Selection reason codes persisted on the run. ONE_BUILDER_REQUIRED keeps the
// S06 meaning; the other two are added by S07.
const (
	ReasonOneBuilderRequired    ReasonCode = "ONE_BUILDER_REQUIRED"
	ReasonParallelPlanApproved  ReasonCode = "PARALLEL_PLAN_APPROVED"
	ReasonParallelUnsafeDegrade ReasonCode = "PARALLEL_UNSAFE_DEGRADED"
	ReasonCompositionConflict   ReasonCode = "COMPOSITION_CONFLICT"
	ReasonCompositionInvalid    ReasonCode = "COMPOSITION_INVALID"
)

// ComplexModeSelection is the pure decision the control plane persists before
// any task exists. Neither callers nor agents can influence it directly.
type ComplexModeSelection struct {
	Mode           WorkMode
	BuilderCount   int
	ReasonCode     ReasonCode
	SuggestedCount int
	SafeConcurrent int
	Batches        [][]string
}

// SelectComplexExecutionMode derives the frozen team choice from an approved
// complex plan. The suggested count alone never selects PARALLEL: the planner
// is only believed as far as provably disjoint write paths allow.
func SelectComplexExecutionMode(tasks []ComplexPlanTask, suggestedCount int) (ComplexModeSelection, error) {
	if len(tasks) == 0 {
		return ComplexModeSelection{}, fmt.Errorf("complex mode selection requires tasks")
	}
	if err := validateComplexParallelPlanTasks(tasks); err != nil {
		return ComplexModeSelection{}, err
	}
	batches := PlanComplexExecutionBatches(tasks)
	safeConcurrent := 1
	for _, batch := range batches {
		if len(batch) > safeConcurrent {
			safeConcurrent = len(batch)
		}
	}
	selection := ComplexModeSelection{
		Mode:           WorkModeStandard,
		BuilderCount:   1,
		SuggestedCount: suggestedCount,
		SafeConcurrent: safeConcurrent,
	}
	switch {
	case suggestedCount == 1:
		selection.ReasonCode = ReasonOneBuilderRequired
	case suggestedCount >= ComplexParallelMinBuilders && suggestedCount <= ComplexParallelMaxBuilders && safeConcurrent >= ComplexParallelMinBuilders:
		selection.Mode = WorkModeParallel
		selection.BuilderCount = suggestedCount
		if safeConcurrent < suggestedCount {
			selection.BuilderCount = safeConcurrent
		}
		selection.ReasonCode = ReasonParallelPlanApproved
		// A later task must not reuse a dirty slot within the same common-base
		// wave. Every slot is used at most once before trusted composition.
		for _, batch := range batches {
			for len(batch) > 0 {
				count := min(selection.BuilderCount, len(batch))
				selection.Batches = append(selection.Batches, append([]string(nil), batch[:count]...))
				batch = batch[count:]
			}
		}
	default:
		selection.ReasonCode = ReasonParallelUnsafeDegrade
	}
	return selection, nil
}

// ComplexCompositionBranch identifies one immutable mail composition delivery.
// The same name is used by the trusted Git adapter and the result consumer.
func ComplexCompositionBranch(requestID string) string {
	digest := sha256.Sum256([]byte(requestID))
	return fmt.Sprintf("cleardev-composed-%x", digest[:16])
}

func validateComplexParallelPlanTasks(tasks []ComplexPlanTask) error {
	byKey := make(map[string]ComplexPlanTask, len(tasks))
	for _, task := range tasks {
		if task.Key == "" {
			return fmt.Errorf("complex plan task lacks a key")
		}
		if _, exists := byKey[task.Key]; exists {
			return fmt.Errorf("complex plan task %q is duplicated", task.Key)
		}
		byKey[task.Key] = task
	}
	if err := ValidateComplexExecutionGeneratedPaths(tasks); err != nil {
		return err
	}
	for _, task := range tasks {
		for _, dependency := range task.DependencyKeys {
			if dependency == task.Key {
				return fmt.Errorf("complex plan task %q depends on itself", task.Key)
			}
			if _, exists := byKey[dependency]; !exists {
				return fmt.Errorf("complex plan task %q depends on missing task %q", task.Key, dependency)
			}
		}
	}
	visiting := make(map[string]bool, len(tasks))
	visited := make(map[string]bool, len(tasks))
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return fmt.Errorf("complex plan dependency graph contains a cycle at %q", key)
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		for _, dependency := range byKey[key].DependencyKeys {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		return nil
	}
	for _, task := range tasks {
		if err := visit(task.Key); err != nil {
			return err
		}
	}
	return nil
}

// PlanComplexExecutionBatches returns the static dispatch waves. Every task in
// one wave has all dependencies in strictly earlier waves and provably disjoint
// write-path domains from its wave peers. Waves keep plan order.
func PlanComplexExecutionBatches(tasks []ComplexPlanTask) [][]string {
	ordered := append([]ComplexPlanTask(nil), tasks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Key != ordered[j].Key {
			return ordered[i].Key < ordered[j].Key
		}
		return false
	})
	level := make(map[string]int, len(ordered))
	for _, task := range ordered {
		level[task.Key] = 0
	}
	for pass := 0; pass < len(ordered); pass++ {
		changed := false
		for _, task := range ordered {
			current := 0
			for _, dependency := range task.DependencyKeys {
				if dependencyLevel, seen := level[dependency]; seen && dependencyLevel >= current {
					current = dependencyLevel + 1
				}
			}
			if current > level[task.Key] {
				level[task.Key] = current
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	byLevel := make(map[int][]ComplexPlanTask)
	maxLevel := 0
	for _, task := range ordered {
		byLevel[level[task.Key]] = append(byLevel[level[task.Key]], task)
		if level[task.Key] > maxLevel {
			maxLevel = level[task.Key]
		}
	}
	batches := make([][]string, 0, maxLevel+1)
	for depth := 0; depth <= maxLevel; depth++ {
		levelTasks := byLevel[depth]
		sort.SliceStable(levelTasks, func(i, j int) bool {
			return complexPlanTaskOrder(tasks, levelTasks[i].Key) < complexPlanTaskOrder(tasks, levelTasks[j].Key)
		})
		for len(levelTasks) > 0 {
			batch := []ComplexPlanTask{levelTasks[0]}
			rest := levelTasks[1:]
			for _, candidate := range rest {
				disjoint := true
				for _, chosen := range batch {
					if !ComplexTaskDispatchPathsDisjoint(chosen, candidate) {
						disjoint = false
						break
					}
				}
				if disjoint {
					batch = append(batch, candidate)
				}
			}
			inBatch := make(map[string]bool, len(batch))
			keys := make([]string, 0, len(batch))
			for _, task := range batch {
				inBatch[task.Key] = true
				keys = append(keys, task.Key)
			}
			next := levelTasks[:0]
			for _, task := range levelTasks {
				if !inBatch[task.Key] {
					next = append(next, task)
				}
			}
			levelTasks = next
			batches = append(batches, keys)
		}
	}
	return batches
}

func complexPlanTaskOrder(tasks []ComplexPlanTask, key string) int {
	for index, task := range tasks {
		if task.Key == key {
			return index
		}
	}
	return len(tasks)
}

// ComplexTaskWritePathsDisjoint reports whether two tasks provably cannot write
// the same repository path. The proof is conservative: patterns are safe only
// when their fixed directory prefixes are neither equal nor a prefix of one
// another. Anything unprovable counts as a conflict.
func ComplexTaskWritePathsDisjoint(a, b ComplexPlanTask) bool {
	if len(a.WritePaths) == 0 || len(b.WritePaths) == 0 {
		return false
	}
	for _, left := range a.WritePaths {
		for _, right := range b.WritePaths {
			if !complexPatternDomainsDisjoint(left, right) {
				return false
			}
		}
	}
	return true
}

func complexPatternDomainsDisjoint(left, right string) bool {
	leftDomain := complexPatternDomain(left)
	rightDomain := complexPatternDomain(right)
	// A pattern that starts with a glob has no fixed prefix. That is
	// unprovable and therefore a conflict, including against a concrete path.
	if leftDomain == "" || rightDomain == "" || leftDomain == rightDomain {
		return false
	}
	return !strings.HasPrefix(leftDomain+"/", rightDomain+"/") && !strings.HasPrefix(rightDomain+"/", leftDomain+"/")
}

// complexPatternDomain returns the fixed path prefix a pattern can touch: the
// longest leading segments that contain no glob metacharacters. Any path
// matched by the pattern starts with this prefix.
func complexPatternDomain(pattern string) string {
	segments := strings.Split(pattern, "/")
	fixed := make([]string, 0, len(segments))
	for _, segment := range segments {
		if strings.ContainsAny(segment, "*?[") {
			break
		}
		fixed = append(fixed, segment)
	}
	if len(fixed) == 0 {
		return ""
	}
	return strings.Join(fixed, "/")
}

// ComplexExecutionBatch is one persisted dispatch wave of a PARALLEL run. Its
// common base is frozen before any task in the wave is dispatched.
type ComplexExecutionBatch struct {
	ID             string                      `json:"id"`
	ExecutionRunID string                      `json:"executionRunId"`
	Ordinal        int                         `json:"ordinal"`
	TaskKeys       []string                    `json:"taskKeys"`
	CommonBaseSHA  string                      `json:"commonBaseSha"`
	Status         ComplexExecutionBatchStatus `json:"status"`
	CreatedAt      time.Time                   `json:"createdAt"`
	ComposedAt     *time.Time                  `json:"composedAt,omitempty"`
}

// ComplexExecutionBatchStatus is the durable lifecycle of one dispatch wave.
const (
	ComplexExecutionBatchPending   ComplexExecutionBatchStatus = "PENDING"
	ComplexExecutionBatchRunning   ComplexExecutionBatchStatus = "RUNNING"
	ComplexExecutionBatchComposing ComplexExecutionBatchStatus = "COMPOSING"
	ComplexExecutionBatchComposed  ComplexExecutionBatchStatus = "COMPOSED"
	ComplexExecutionBatchBlocked   ComplexExecutionBatchStatus = "BLOCKED"
)

// ComplexExecutionBatchStatus enumerates the wave states. A PENDING wave has no
// frozen common base yet; RUNNING waves dispatch; COMPOSING waves are being
// integrated; BLOCKED waves keep every candidate and workspace untouched.
type ComplexExecutionBatchStatus string

// ComplexExecutionComposition is the stable request and outcome of combining
// one batch's verified candidates in plan order inside a separate managed
// worktree.
type ComplexExecutionComposition struct {
	ID                 string                            `json:"id"`
	ExecutionRunID     string                            `json:"executionRunId"`
	BatchID            string                            `json:"batchId"`
	RequestID          string                            `json:"requestId"`
	InputBaseSHA       string                            `json:"inputBaseSha"`
	InputCandidateIDs  []string                          `json:"inputCandidateIds"`
	InputCandidateSHAs []string                          `json:"inputCandidateShas"`
	WorkspacePath      string                            `json:"workspacePath,omitempty"`
	OutputCommitSHA    string                            `json:"outputCommitSha,omitempty"`
	Status             ComplexExecutionCompositionStatus `json:"status"`
	ConflictPaths      []string                          `json:"conflictPaths,omitempty"`
	ReasonCode         ReasonCode                        `json:"reasonCode,omitempty"`
	CreatedAt          time.Time                         `json:"createdAt"`
	SettledAt          *time.Time                        `json:"settledAt,omitempty"`
}

// ComplexExecutionCompositionStatus is the durable lifecycle of one combine.
const (
	ComplexExecutionCompositionPending  ComplexExecutionCompositionStatus = "PENDING"
	ComplexExecutionCompositionRunning  ComplexExecutionCompositionStatus = "RUNNING"
	ComplexExecutionCompositionComposed ComplexExecutionCompositionStatus = "COMPOSED"
	ComplexExecutionCompositionBlocked  ComplexExecutionCompositionStatus = "BLOCKED"
	ComplexExecutionCompositionFailed   ComplexExecutionCompositionStatus = "FAILED"
)

// ComplexExecutionCompositionStatus enumerates the combine states. FAILED is an
// infrastructure outcome; BLOCKED means a Git conflict stopped the whole run.
type ComplexExecutionCompositionStatus string
