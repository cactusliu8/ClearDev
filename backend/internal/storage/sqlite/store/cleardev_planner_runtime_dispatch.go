package store

import (
	"context"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// validatePlannerRuntimeDispatch closes the read-to-dispatch race: a scheduler
// that read a packet before an amendment committed must reload, not send its
// old prompt against the new effective contract. No attempt is reserved first.
func validatePlannerRuntimeDispatch(ctx context.Context, q *gen.Queries, dispatch core.ComplexExecutionDispatch) error {
	run, err := q.GetClearDevComplexExecutionRun(ctx, dispatch.ExecutionRunID)
	if err != nil {
		return err
	}
	enabled, err := core.PlannerRuntimeRun(complexExecutionRunFromGen(run))
	if err != nil || !enabled {
		return err
	}
	barriers, err := q.CountClearDevPlannerRuntimeBarriers(ctx, run.ID)
	if err != nil {
		return err
	}
	if barriers != 0 {
		return complexExecutionRule("Planner coordination must resolve before another dispatch")
	}
	task, err := q.GetClearDevPlannerRuntimeEffectiveTask(ctx, dispatch.ComplexExecutionTaskID)
	if err != nil {
		return err
	}
	if task.ExecutionRunID != run.ID || task.WorkItemID != dispatch.DevelopmentTaskID ||
		task.TaskPacketSha256 != dispatch.ExecutionPackageSHA256 ||
		complexExecutionRawDigest([]byte(task.TaskPacketJson)) != task.TaskPacketSha256 {
		return complexExecutionRule("dispatch no longer matches the effective immutable task contract")
	}
	return nil
}
