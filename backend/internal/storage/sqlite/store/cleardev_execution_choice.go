package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// SetClearDevProjectExecution changes only the explicit execution key. Native
// project configuration, unknown future keys and AO role settings are preserved.
// Database guards reject changes after the relevant admission/binding boundary.
func (s *Store) SetClearDevProjectExecution(ctx context.Context, projectID string, choice domain.ClearDevExecutionConfig) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "set ClearDev project execution", func(q *gen.Queries) error {
		return selectProductExecutionTx(ctx, q, projectID, &choice)
	})
}

// GetClearDevExecutionChoiceLocks reports durable facts, not an in-memory guess.
func (s *Store) GetClearDevExecutionChoiceLocks(ctx context.Context, projectID string) (bool, bool, error) {
	row, err := s.qr.GetClearDevExecutionChoiceLocks(ctx, projectID)
	if err != nil {
		return false, false, err
	}
	return row.ToolLocked != 0, row.ModelLocked != 0, nil
}

// selectProductExecutionTx changes only the explicit ClearDev key, preserving
// unrelated project settings (including unknown future keys). The same write
// transaction then creates the product container, which freezes tool choice.
func selectProductExecutionTx(ctx context.Context, q *gen.Queries, projectID string, choice *domain.ClearDevExecutionConfig) error {
	if choice == nil {
		return nil // Old clients retain their project's existing behaviour.
	}
	if err := choice.Validate(); err != nil {
		return productConflict("invalid ClearDev execution choice")
	}
	project, err := q.GetProject(ctx, domain.ProjectID(projectID))
	if err != nil {
		return err
	}
	if project.ArchivedAt.Valid {
		return productConflict("cannot start a product in an archived project")
	}
	config := map[string]json.RawMessage{}
	if project.Config.Valid && strings.TrimSpace(project.Config.String) != "" {
		if err := json.Unmarshal([]byte(project.Config.String), &config); err != nil {
			return productConflict("project configuration is invalid; repair it before selecting an execution tool")
		}
	}
	if config == nil {
		config = map[string]json.RawMessage{}
	}
	var current *domain.ClearDevExecutionConfig
	if raw := config["cleardev"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &current); err != nil {
			return productConflict("stored ClearDev execution choice is invalid")
		}
	}
	if current != nil && *current == *choice {
		return nil
	}
	encodedChoice, err := json.Marshal(choice)
	if err != nil {
		return err
	}
	config["cleardev"] = encodedChoice
	encodedConfig, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = q.UpdateProjectSettings(ctx, gen.UpdateProjectSettingsParams{
		ID: project.ID, DisplayName: project.DisplayName,
		Config: sql.NullString{String: string(encodedConfig), Valid: true},
	})
	return projectExecutionError(err)
}

// Internal SQLite trigger text is a stable store contract, never parsed from a
// model/provider response. Public APIs map this sentinel to a Chinese conflict.
func projectExecutionError(err error) error {
	if err != nil && strings.Contains(err.Error(), "cleardev execution project mismatch") {
		return &core.RuleError{Code: "EXECUTION_PROJECT_MISMATCH", Message: "目标项目的工具和模型与本产品不同；请使用相同配置的项目，不能在阶段绑定时切换工具"}
	}
	if err != nil && strings.Contains(err.Error(), "cleardev execution choice is frozen") {
		return domain.ErrClearDevExecutionFrozen
	}
	return err
}

// bindPreflightChoiceTx checks admission against the current project choice in
// the same transaction as its immutable record. A successful record freezes
// the model before a session can be created; a stale inspection remains a
// recorded, retryable failure and does not consume a role or message budget.
func bindPreflightChoiceTx(ctx context.Context, q *gen.Queries, record core.ControlledPreflight) (core.ControlledPreflight, bool, error) {
	if record.Outcome != core.ControlledPreflightPassed {
		return record, false, nil
	}
	project, err := q.GetProject(ctx, domain.ProjectID(record.AOProjectID))
	if err != nil {
		return record, false, err
	}
	var config struct {
		Choice *domain.ClearDevExecutionConfig `json:"cleardev"`
	}
	invalid := false
	if project.Config.Valid && strings.TrimSpace(project.Config.String) != "" {
		invalid = json.Unmarshal([]byte(project.Config.String), &config) != nil
	}
	if config.Choice != nil && config.Choice.Validate() != nil {
		invalid = true
	}
	harness := domain.HarnessCodex
	if config.Choice != nil {
		harness = config.Choice.Harness
	}
	changed := record.Provider != string(harness) || (config.Choice != nil && (record.RequestedModel != config.Choice.Model || record.ResolvedModel != config.Choice.Model))
	if !invalid && !changed {
		return record, false, nil
	}
	record.Outcome = core.ControlledPreflightFailed
	record.ReasonCode = core.ReasonCode("EXECUTION_CHOICE_CHANGED")
	record.Retryable = true
	record.ProviderErrorCode = ""
	record.ErrorSummary = "project execution choice changed during inspection; retry with its current selection"
	if invalid {
		record.ReasonCode = core.ReasonCode("EXECUTION_TOOL_CONFIG_INVALID")
		record.ErrorSummary = "stored project execution configuration is invalid"
	}
	return record, true, nil
}
