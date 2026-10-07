package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func activeDirectionStop(ctx context.Context, q *gen.Queries, versionID string) (bool, error) {
	if strings.TrimSpace(versionID) == "" {
		return false, nil
	}
	_, err := q.GetActiveClearDevDirectionStopGateByVersion(ctx, versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func directionStoppedError() *cleardev.RuleError {
	return &cleardev.RuleError{
		Code:    cleardev.ReasonDirectionChangeStopped,
		Message: "ClearDev writes are stopped by an active direction-change gate",
	}
}

func sameDirectionIntent(existing, intent cleardev.DirectionIntent) bool {
	return existing.DevelopmentRequirementID == intent.DevelopmentRequirementID &&
		existing.RequirementVersionID == intent.RequirementVersionID &&
		existing.RequirementSHA256 == intent.RequirementSHA256 &&
		existing.Message == intent.Message &&
		existing.MessageSHA256 == intent.MessageSHA256 &&
		existing.StewardRoleBindingID == intent.StewardRoleBindingID
}

func rejectIfDirectionStopped(
	ctx context.Context,
	q *gen.Queries,
	requirement cleardev.DevelopmentRequirement,
	versionID string,
	subjectType cleardev.SubjectType,
	subjectID string,
	action cleardev.Action,
	from, to string,
	at cleardev.ActionRequest,
) (*cleardev.RuleError, error) {
	stopped, err := activeDirectionStop(ctx, q, versionID)
	if err != nil {
		return nil, err
	}
	if !stopped {
		return nil, nil
	}
	return rejectClearDevAction(ctx, q, requirement, subjectType, subjectID, action, from, to,
		cleardev.ReasonDirectionChangeStopped, cleardev.EventSourceControlPlane, at)
}
