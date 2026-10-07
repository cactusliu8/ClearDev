package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// Normalize the recovery's own task/attempt state against the original context.
// The root receipt, budgets, counters, other attempts and contracts remain exact.
// Every appended retry is separately checked against the immutable successor
// chain before removing those rows from the original pre-recovery context.
func stoppedCheckContext(raw []byte, offer *gen.CleardevStoppedCheckRequest, a gen.CleardevComplexExecutionTaskAttempt, item gen.CleardevWorkItem, original gen.CleardevComplexExecutionCheckRun, planner gen.GetSessionRow, retryIDs []string) ([]byte, error) {
	var current map[string]json.RawMessage
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, err
	}
	identity, err := json.Marshal(map[string]any{"id": planner.ID, "projectId": planner.ProjectID, "kind": planner.Kind, "harness": planner.Harness, "mode": planner.SessionMode, "permission": planner.PermissionMode, "key": planner.CreationIdempotencyKey, "fingerprint": planner.CreationRequestFingerprint, "nativeId": planner.ProviderConversationID, "model": planner.Model, "workspace": planner.WorkspacePath})
	if err != nil {
		return nil, err
	}
	current["checkRecoveryPlannerIdentity"] = identity
	if offer != nil {
		var saved map[string]json.RawMessage
		if err := json.Unmarshal([]byte(offer.ContextJson), &saved); err != nil {
			return nil, err
		}
		if err := normalizeStoppedContextRow(current, saved, "tasks", "mappingId", a.TaskMappingID, []string{"state"}); err != nil {
			return nil, err
		}
		if err := normalizeStoppedContextRow(current, saved, "dispatchHistory", "ID", a.ID, []string{"Status", "ReasonCode", "SettledAt"}); err != nil {
			return nil, err
		}
		var checks []map[string]json.RawMessage
		if err := json.Unmarshal(current["trustedChecks"], &checks); err != nil {
			return nil, err
		}
		found := 0
		kept := make([]map[string]json.RawMessage, 0, len(checks))
		for _, check := range checks {
			var id string
			if err := json.Unmarshal(check["ID"], &id); err != nil {
				return nil, err
			}
			if slices.Contains(retryIDs, id) {
				found++
				continue
			}
			kept = append(kept, check)
		}
		if found != len(retryIDs) || len(retryIDs) == 0 || offer.OriginalCheckID != original.ID || item.ReworkCount < a.Round {
			return nil, errors.New("stopped check retry lost its unique source")
		}
		current["trustedChecks"], err = json.Marshal(kept)
		if err != nil {
			return nil, err
		}
	}
	data, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}
	// Canonicalize embedded structs/maps equally, without float conversion of
	// integer fields or silently dropping parts of the frozen context.
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func normalizeStoppedContextRow(current, saved map[string]json.RawMessage, collection, key, id string, fields []string) error {
	var rows, oldRows []map[string]json.RawMessage
	if err := json.Unmarshal(current[collection], &rows); err != nil {
		return err
	}
	if err := json.Unmarshal(saved[collection], &oldRows); err != nil {
		return err
	}
	var old map[string]json.RawMessage
	oldCount, newCount := 0, 0
	for _, r := range oldRows {
		var value string
		if err := json.Unmarshal(r[key], &value); err != nil {
			return err
		}
		if value == id {
			old = r
			oldCount++
		}
	}
	if oldCount != 1 {
		return errors.New("stopped check source context is not unique")
	}
	for _, r := range rows {
		var value string
		if err := json.Unmarshal(r[key], &value); err != nil {
			return err
		}
		if value != id {
			continue
		}
		newCount++
		for _, f := range fields {
			original, exists := old[f]
			if !exists {
				return errors.New("stopped check source context lacks original state")
			}
			r[f] = original
		}
	}
	if newCount != 1 {
		return errors.New("stopped check current context is not unique")
	}
	raw, err := json.Marshal(rows)
	if err == nil {
		current[collection] = raw
	}
	return err
}
