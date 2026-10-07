package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func productConflict(message string) error {
	return &core.RuleError{Code: core.ReasonPreconditionNotMet, Message: message}
}

// CreateClearDevProductGoal atomically registers a product container and its first discussion.
func (s *Store) CreateClearDevProductGoal(ctx context.Context, command core.CreateProductGoalCommand) (string, bool, error) {
	requestedExecution, err := json.Marshal(command.Goal.RequestedExecution)
	if err != nil {
		return "", false, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	id, created := command.Goal.ID, false
	err = s.inTx(ctx, "create ClearDev product goal", func(q *gen.Queries) error {
		goal, c, d := command.Goal, command.Container, command.Discussion
		if oldID, err := q.GetClearDevProductGoalByRequest(ctx, goal.RequestID); err == nil {
			old, err := q.GetClearDevProductGoal(ctx, oldID)
			if err != nil {
				return err
			}
			if old.AoProjectID != goal.AOProjectID || old.Name != goal.Name || old.OriginalPrdText != goal.GoalText || old.RequestedExecution != string(requestedExecution) {
				return productConflict("product request id already belongs to different input")
			}
			id = oldID
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if strings.TrimSpace(goal.RequestID) == "" || len(goal.RequestID) > 160 ||
			goal.ID != c.Requirement.ID || goal.AOProjectID != c.Requirement.AOProjectID || goal.Name != c.Requirement.Name ||
			goal.GoalText != c.OriginalPRDText || c.BenchmarkBinding != nil ||
			d.ProductID != goal.ID || d.Ordinal != 0 || d.UserMessage != goal.GoalText || d.Result != nil || d.SettledAt != nil ||
			strings.TrimSpace(d.ID) == "" || utf8.RuneCountInString(d.UserMessage) > 16000 {
			return productConflict("invalid product registration")
		}
		if err := selectProductExecutionTx(ctx, q, goal.AOProjectID, goal.RequestedExecution); err != nil {
			return err
		}
		if err := createComplexRequirementTx(ctx, q, c); err != nil {
			return err
		}
		if err := q.InsertClearDevProductGoal(ctx, gen.InsertClearDevProductGoalParams{ID: goal.ID, RequestID: goal.RequestID, CreatedAt: goal.CreatedAt, RequestedExecution: string(requestedExecution)}); err != nil {
			return err
		}
		if err := insertProductDiscussion(ctx, q, d); err != nil {
			return err
		}
		created = true
		return nil
	})
	return id, created, err
}

func insertProductDiscussion(ctx context.Context, q *gen.Queries, d core.ProductDiscussion) error {
	if err := q.InsertClearDevProductDiscussion(ctx, gen.InsertClearDevProductDiscussionParams{
		ID: d.ID, ProductID: d.ProductID, Ordinal: int64(d.Ordinal), UserMessage: d.UserMessage, CreatedAt: d.CreatedAt,
	}); err != nil {
		return err
	}
	return insertProductContext(ctx, q, d)
}

// ListClearDevProductGoalIDs returns product goal ids for one AO project.
func (s *Store) ListClearDevProductGoalIDs(ctx context.Context, projectID string) ([]string, error) {
	return s.qr.ListClearDevProductGoalIDs(ctx, projectID)
}

func productGoalFromRow(row gen.GetClearDevProductGoalRow) core.ProductGoal {
	return core.ProductGoal{ID: row.ID, AOProjectID: row.AoProjectID, Name: row.Name, GoalText: row.OriginalPrdText, RequestID: row.RequestID, CreatedAt: row.CreatedAt}
}

// GetClearDevProduct loads the full immutable product snapshot with verified digests.
func (s *Store) GetClearDevProduct(ctx context.Context, id string) (core.ProductSnapshot, bool, error) {
	row, err := s.qr.GetClearDevProductGoal(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ProductSnapshot{}, false, nil
	}
	if err != nil {
		return core.ProductSnapshot{}, false, err
	}
	out := core.ProductSnapshot{Goal: productGoalFromRow(row), Discussions: []core.ProductDiscussion{}, Stages: []core.ProductStage{}}
	if err := json.Unmarshal([]byte(row.RequestedExecution), &out.Goal.RequestedExecution); err != nil {
		return out, false, errors.New("product has invalid stored execution request")
	}
	rounds, err := s.qr.ListClearDevProductDiscussions(ctx, id)
	if err != nil {
		return out, false, err
	}
	for _, r := range rounds {
		d := core.ProductDiscussion{ID: r.ID, ProductID: r.ProductID, Ordinal: int(r.Ordinal), UserMessage: r.UserMessage,
			AgentStepID: r.AgentStepID.String, ResultSHA256: r.ResultSha256.String, CreatedAt: r.CreatedAt}
		if r.SettledAt.Valid && r.FailureReason.Valid {
			d.FailureReason, d.SettledAt = r.FailureReason.String, &r.SettledAt.Time
		} else if r.SettledAt.Valid {
			parsed, err := core.ParseProductDiscoveryResult([]byte(r.ResultJson.String))
			if err != nil || !requirementDigestMatches(r.ResultJson.String, r.ResultSha256.String) {
				return out, false, errors.New("product response has invalid stored content")
			}
			d.Result, d.SettledAt = &parsed, &r.SettledAt.Time
		}
		d.ProtocolVersion, d.Selection, err = loadProductContext(ctx, s.qr, d.ID)
		if err != nil {
			return out, false, err
		}
		if d.Result != nil {
			if err := core.ValidateProductDiscussionSelection(*d.Result, d.ProtocolVersion, d.Selection); err != nil {
				return out, false, err
			}
		}
		out.Discussions = append(out.Discussions, d)
	}
	stages, err := s.qr.ListClearDevProductStages(ctx, id)
	if err != nil {
		return out, false, err
	}
	for _, stage := range stages {
		v, err := productStageFromRow(stage)
		if err != nil {
			return out, false, err
		}
		v.Selection, err = loadStageSelection(ctx, s.qr, v.ID)
		if err != nil {
			return out, false, err
		}
		out.Stages = append(out.Stages, v)
	}
	return out, true, nil
}

func productStageFromRow(row gen.CleardevProductStage) (core.ProductStage, error) {
	v := core.ProductStage{ID: row.ID, ProductID: row.ProductID, DiscussionID: row.DiscussionID, Ordinal: int(row.Ordinal),
		DefinitionSHA256: row.DefinitionSha256, DevelopmentRequirementID: row.DevelopmentRequirementID.String,
		BaseCommitSHA: row.BaseCommitSha.String, CreatedAt: row.CreatedAt}
	if !requirementDigestMatches(row.DefinitionJson, row.DefinitionSha256) {
		return v, errors.New("stage definition digest mismatch")
	}
	if err := json.Unmarshal([]byte(row.DefinitionJson), &v.Definition); err != nil {
		return v, err
	}
	return v, nil
}

// GetClearDevProductStageByRequirement finds the stage linked to a child requirement.
func (s *Store) GetClearDevProductStageByRequirement(ctx context.Context, requirementID string) (core.ProductStage, bool, error) {
	row, err := s.qr.GetClearDevProductStageByRequirement(ctx, nullableString(requirementID))
	if errors.Is(err, sql.ErrNoRows) {
		return core.ProductStage{}, false, nil
	}
	if err != nil {
		return core.ProductStage{}, false, err
	}
	stage, err := productStageFromRow(row)
	if err == nil {
		stage.Selection, err = loadStageSelection(ctx, s.qr, stage.ID)
	}
	return stage, err == nil, err
}

// AppendClearDevProductDiscussion adds one user message after the settled previous round.
func (s *Store) AppendClearDevProductDiscussion(ctx context.Context, command core.AppendProductDiscussionCommand) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	changed := false
	err := s.inTx(ctx, "append ClearDev product discussion", func(q *gen.Queries) error {
		d := command.Discussion
		if old, err := q.GetClearDevProductDiscussion(ctx, d.ID); err == nil {
			return verifyProductDiscussionReplay(ctx, q, old, command)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		parent, err := q.GetClearDevRequirement(ctx, d.ProductID)
		if err != nil {
			return err
		}
		if parent.CancelledAt.Valid {
			return productConflict("cancelled product cannot continue discussion")
		}
		if source, err := q.GetLatestClearDevProductSourcePreparation(ctx, d.ProductID); err == nil {
			if source.Status == "PENDING" || source.Status == "READY" {
				if source.ID != d.ID || source.Status != "READY" {
					return productConflict("wait for source preparation before another discussion")
				}
				p, err := sourcePreparationFromRow(source)
				if err != nil {
					return err
				}
				if p.Prepared == nil || command.Selection == nil || !sameSourceSelection(*p.Prepared, *command.Selection) {
					return productConflict("discussion does not match prepared source")
				}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if strings.TrimSpace(d.ID) == "" || len(d.ID) > 160 || strings.TrimSpace(d.UserMessage) == "" ||
			utf8.RuneCountInString(d.UserMessage) > 16000 || d.AgentStepID != "" || d.Result != nil || d.SettledAt != nil {
			return productConflict("invalid product message")
		}
		rounds, err := q.ListClearDevProductDiscussions(ctx, d.ProductID)
		if err != nil {
			return err
		}
		if len(rounds) == 0 || len(rounds) >= core.ProductMaxDiscussions {
			return productConflict("product discussion limit reached; no new model turn was sent")
		}
		previous := rounds[len(rounds)-1]
		if !previous.SettledAt.Valid || previous.ID != command.ExpectedPreviousID {
			return productConflict("wait for the current Steward reply or refresh the stale discussion")
		}
		if err := checkProductRevision(ctx, q, d.ProductID); err != nil {
			return err
		}
		protocol, selection, err := loadProductContext(ctx, q, previous.ID)
		if err != nil {
			return err
		}
		proposalID := previous.ID
		if previous.FailureReason.Valid {
			if protocol != core.ProjectDiscoveryVersion || selection == nil {
				return productConflict("a failed discussion needs a previously saved project choice")
			}
			switch previous.FailureReason.String {
			case "PRODUCT_STEWARD_WORKSPACE_CHANGED":
				if command.Selection == nil {
					return productConflict("an interrupted project discussion requires an explicit new source choice")
				}
			case "PRODUCT_DISCOVERY_INVALID":
				// Append a new budgeted user discussion; never rewrite the failed
				// result. The Service rechecks inherited source/data provenance.
			default:
				return productConflict("this failed discussion does not support a new round")
			}
			proposalID = selection.SourceDiscussionID
		}
		if command.Selection != nil {
			if protocol != core.ProjectDiscoveryVersion || command.Selection.SourceDiscussionID != proposalID ||
				(command.Selection.ChoiceDiscussionID != "" && command.Selection.ChoiceDiscussionID != d.ID) ||
				(previous.FailureReason.Valid && command.Selection.ChoiceDiscussionID != d.ID) {
				return productConflict("choose an option from the exact saved project proposal")
			}
			selection = command.Selection
		}
		if d.ProtocolVersion == core.ProjectDiscoveryVersion && protocol != core.ProjectDiscoveryVersion {
			return productConflict("a discussion cannot silently upgrade its inherited protocol")
		}
		d.ProtocolVersion, d.Selection = protocol, selection
		d.Ordinal = len(rounds)
		if err := insertProductDiscussion(ctx, q, d); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// SettleClearDevProductDiscussion binds one exact settled Steward step and stages its proposal.
func (s *Store) SettleClearDevProductDiscussion(ctx context.Context, command core.SettleProductDiscussionCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "settle ClearDev product discussion", func(q *gen.Queries) error {
		d, err := q.GetClearDevProductDiscussion(ctx, command.DiscussionID)
		if err != nil {
			return err
		}
		if d.ProductID != command.ProductID {
			return productConflict("product response belongs to another goal")
		}
		if d.SettledAt.Valid {
			if d.AgentStepID.String != command.AgentStepID {
				return productConflict("product response belongs to another model step")
			}
			return nil
		}
		step, err := q.GetClearDevComplexAgentStep(ctx, command.AgentStepID)
		if err != nil {
			return err
		}
		binding, err := q.GetClearDevComplexRoleBinding(ctx, step.RoleBindingID)
		if err != nil {
			return err
		}
		if binding.DevelopmentProjectID != d.ProductID || binding.Role != string(core.StandardRoleSteward) ||
			step.RequestID != d.ID || step.StepKind != string(core.ComplexAgentStepCompilation) || step.SendStatus != "SETTLED" ||
			!step.CompletedAt.Valid || !step.TurnID.Valid || !step.FinalMessageID.Valid ||
			!requirementDigestMatches(step.FinalMessageText.String, step.MessageSha256.String) {
			return productConflict("product response has no exact settled Steward source")
		}
		parent, err := q.GetClearDevRequirement(ctx, d.ProductID)
		if err != nil {
			return err
		}
		if parent.CancelledAt.Valid {
			return productConflict("product goal is cancelled")
		}
		result, err := core.ParseProductDiscoveryResult([]byte(step.FinalMessageText.String))
		if err != nil {
			return err
		}
		protocol, selection, err := loadProductContext(ctx, q, d.ID)
		if err != nil {
			return err
		}
		if err := core.ValidateProductDiscussionSelection(result, protocol, selection); err != nil {
			return productConflict(err.Error())
		}
		if _, err := q.SettleClearDevProductDiscussion(ctx, gen.SettleClearDevProductDiscussionParams{
			ID: d.ID, AgentStepID: nullableString(step.ID), ResultJson: step.FinalMessageText,
			ResultSha256: step.MessageSha256, SettledAt: nullableTime(command.At),
		}); err != nil {
			return err
		}
		if result.Outcome != "READY" {
			return nil
		}
		for ordinal, stage := range result.Stages {
			data, digest, err := core.ProductStageJSON(stage)
			if err != nil {
				return err
			}
			if err := q.InsertClearDevProductStage(ctx, gen.InsertClearDevProductStageParams{
				ID: d.ID + ":stage:" + stage.Key, ProductID: d.ProductID, DiscussionID: d.ID, Ordinal: int64(ordinal),
				DefinitionJson: string(data), DefinitionSha256: digest, CreatedAt: command.At,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// FailClearDevProductInvestigation records a trusted worktree-integrity failure.
// It cannot turn a failed or completed response into a new model opportunity.
func (s *Store) FailClearDevProductInvestigation(ctx context.Context, productID, discussionID string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "stop changed product investigation", func(q *gen.Queries) error {
		d, err := q.GetClearDevProductDiscussion(ctx, discussionID)
		if err != nil {
			return err
		}
		if d.ProductID != productID || (d.SettledAt.Valid && !d.FailureReason.Valid) {
			return productConflict("product investigation failure does not match pending discussion")
		}
		_, err = q.FailClearDevProductDiscussion(ctx, gen.FailClearDevProductDiscussionParams{ID: discussionID, ProductID: productID, SettledAt: nullableTime(at)})
		return err
	})
}

// FailClearDevProductReply settles a rejected protocol reply so the failed
// round stays failed and only a new user message can spend another discussion
// budget. A settled successful reply is never rewritten as failure.
func (s *Store) FailClearDevProductReply(ctx context.Context, productID, discussionID string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "fail ClearDev product reply", func(q *gen.Queries) error {
		d, err := q.GetClearDevProductDiscussion(ctx, discussionID)
		if err != nil {
			return err
		}
		if d.ProductID != productID || (d.SettledAt.Valid && !d.FailureReason.Valid) {
			return productConflict("product reply failure does not match pending discussion")
		}
		_, err = q.FailInvalidClearDevProductDiscussion(ctx, gen.FailInvalidClearDevProductDiscussionParams{ID: discussionID, ProductID: productID, SettledAt: nullableTime(at)})
		return err
	})
}

// PrepareClearDevProductStage binds one supported stage to its child requirement and baseline.
func (s *Store) PrepareClearDevProductStage(ctx context.Context, command core.PrepareProductStageCommand) (string, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	id, created := command.Container.Requirement.ID, false
	err := s.inTx(ctx, "prepare ClearDev product stage", func(q *gen.Queries) error {
		if err := rejectActiveSourcePreparation(ctx, q, command.ProductID); err != nil {
			return err
		}
		row, err := q.GetClearDevProductStage(ctx, command.StageID)
		if err != nil {
			return err
		}
		stage, err := productStageFromRow(row)
		if err != nil {
			return err
		}
		if stage.ProductID != command.ProductID || stage.DefinitionSHA256 != command.DefinitionSHA256 {
			return productConflict("stage source or definition changed")
		}
		if stage.DevelopmentRequirementID != "" {
			if stage.Definition.ExecutionBasis != nil {
				if err := requireProductPredecessor(ctx, q, stage, stage.BaseCommitSHA); err != nil {
					return err
				}
			}
			id = stage.DevelopmentRequirementID
			return nil
		}
		product, err := q.GetClearDevProductGoal(ctx, stage.ProductID)
		if err != nil {
			return err
		}
		rounds, err := q.ListClearDevProductDiscussions(ctx, stage.ProductID)
		if err != nil {
			return err
		}
		if len(rounds) == 0 || rounds[len(rounds)-1].ID != stage.DiscussionID ||
			!rounds[len(rounds)-1].SettledAt.Valid || (stage.Definition.ExecutionBasis == nil && stage.Definition.Feasibility != "SUPPORTED") {
			return productConflict("only the latest ready, supported stage may be prepared")
		}
		if !complexExecutionSHA1(command.BaseCommitSHA) {
			return productConflict("stage preparation needs an observed baseline SHA")
		}
		selection, err := loadStageSelection(ctx, q, stage.ID)
		if err != nil {
			return err
		}
		targetProject := product.AoProjectID
		if stage.Definition.ExecutionBasis != nil {
			if selection == nil || selection.BaseCommitSHA != command.BaseCommitSHA {
				return productConflict("project stage requires its selected repository version")
			}
			targetProject = selection.AOProjectID
		}
		if err := requireProductPredecessor(ctx, q, stage, command.BaseCommitSHA); err != nil {
			return err
		}
		c := command.Container
		if c.Requirement.AOProjectID != targetProject || c.Requirement.ID == product.ID || c.BenchmarkBinding != nil ||
			c.OriginalPRDText != core.ProductStagePRD(productGoalFromRow(product), stage.Definition) {
			return productConflict("stage requirement does not match its immutable product definition")
		}
		if err := createComplexRequirementTx(ctx, q, c); err != nil {
			return err
		}
		if n, err := q.BindClearDevProductStage(ctx, gen.BindClearDevProductStageParams{
			ID: stage.ID, DevelopmentRequirementID: nullableString(id), BaseCommitSha: nullableString(command.BaseCommitSHA),
		}); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return fmt.Errorf("stage bind compare-and-swap lost")
		}
		created = true
		return nil
	})
	return id, created, projectExecutionError(err)
}
