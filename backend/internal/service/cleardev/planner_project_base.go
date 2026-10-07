package cleardev

import (
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// A project's first dispatch under an applied revision starts at the current
// frozen serial workspace head. Reopening an earlier task must not rewind the
// shared workspace or include later tasks' old changes in that task's new diff.
func plannerProjectDispatchBase(snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, round int) (string, bool, error) {
	if !core.BuilderFirstFailureEnabled(snapshot.Run) || snapshot.Run.Mode != core.WorkModeStandard {
		return "", false, nil
	}
	pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
	if err != nil {
		return "", false, err
	}
	if pkg.RuntimeRevision == nil || pkg.RuntimeRevision.FirstRound != round {
		return "", false, nil
	}
	if err := core.ProjectTaskMatchesRun(pkg, snapshot.Run); err != nil {
		return "", true, err
	}
	bound := false
	if snapshot.PlannerRuntime != nil {
		for _, amendment := range snapshot.PlannerRuntime.Amendments {
			if amendment.EventID == pkg.RuntimeRevision.EventID && amendment.TaskMappingID == task.ID && amendment.ExecutionRunID == snapshot.Run.ID && amendment.ExecutionPackageSHA256 == task.ExecutionPackageSHA256 && amendment.ExecutionPackageJSON == task.ExecutionPackageJSON {
				bound = true
			}
		}
	}
	if !bound {
		return "", true, errors.New("engineering dispatch lost its applied task revision")
	}
	base := snapshot.Run.InitialBaseCommitSHA
	var latest *core.ComplexExecutionDispatch
	ambiguous := false
	for i := range snapshot.Dispatches {
		prior := &snapshot.Dispatches[i]
		if prior.ExecutionRunID != snapshot.Run.ID || prior.BuilderRoleBindingID != snapshot.Run.BuilderRoleBindingID || prior.BatchID != "" || prior.CandidateCommitSHA == "" || prior.ComplexExecutionTaskID == task.ID && prior.Round >= round {
			continue
		}
		if prior.SettledAt == nil || prior.CreatedAt.IsZero() || !validComplexExecutionCommitSHA(prior.CandidateCommitSHA) {
			return "", true, errors.New("engineering dispatch requires settled frozen workspace evidence")
		}
		if latest == nil || prior.CreatedAt.After(latest.CreatedAt) {
			latest = prior
			ambiguous = false
		} else if prior.CreatedAt.Equal(latest.CreatedAt) && prior.CandidateCommitSHA != latest.CandidateCommitSHA {
			ambiguous = true
		}
	}
	if ambiguous {
		return "", true, errors.New("engineering dispatch has ambiguous frozen workspace heads")
	}
	if latest != nil {
		base = latest.CandidateCommitSHA
	}
	if !validComplexExecutionCommitSHA(base) {
		return "", true, errors.New("engineering dispatch has no frozen base")
	}
	return base, true, nil
}

// Sending and freezing must use the same evidence-bound parent. A Planner
// revision can precede the first candidate, or reopen an older serial task.
func complexExecutionDispatchParent(snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch) (string, error) {
	expectedHead := dispatch.BaseCommitSHA
	if revisedBase, revised, err := plannerProjectDispatchBase(snapshot, task, dispatch.Round); err != nil {
		return "", err
	} else if revised {
		if dispatch.BaseCommitSHA != revisedBase {
			return "", ports.ErrClearDevCandidateInvalid
		}
		expectedHead = revisedBase
	} else if dispatch.Round > 0 {
		var found bool
		expectedHead, found = lastFrozenDispatchCandidate(snapshot, task.ID, dispatch.Round)
		if !found {
			return "", ports.ErrClearDevCandidateInvalid
		}
	}
	return expectedHead, nil
}
