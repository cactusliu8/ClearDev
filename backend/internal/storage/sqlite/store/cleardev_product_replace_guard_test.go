package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sqlitedb "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func productReplaceGuardResult() core.ProductDiscoveryResult {
	return core.ProductDiscoveryResult{SchemaVersion: 1, Kind: "PRODUCT_DISCOVERY", Outcome: "READY", Message: "先本地搜索，再考虑账号。", FeasibilitySummary: "已有本地联系人页面。", Questions: []core.ProductQuestion{},
		Features: []core.ProductFeature{{Key: "search", Title: "搜索", Description: "按邮箱查找"}, {Key: "accounts", Title: "账号", Description: "连接邮箱"}},
		Stages: []core.ProductStageDefinition{
			{Key: "local", Title: "本地搜索", Goal: "用户能搜索联系人", FeatureKeys: []string{"search"}, AcceptanceCriteria: []string{"输入邮箱子串时展示匹配联系人。"}, NonGoals: []string{"本阶段不连接外部邮箱。"}, Feasibility: "SUPPORTED", FeasibilityReason: "已有本地接口"},
			{Key: "remote", Title: "账号连接", Goal: "连接邮箱账号", FeatureKeys: []string{"accounts"}, AcceptanceCriteria: []string{"用户能连接自己的邮箱。"}, NonGoals: []string{}, Feasibility: "SUPPORTED", FeasibilityReason: "已具备"},
		}}
}

func TestProductStoreReplacementCannotRewriteOrUnfreezeHistory(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "ao-product", Path: "/tmp/ao-product", Kind: domain.ProjectKindSingleRepo, RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	command := productStoreCommand("replace-guard", now)
	productID, _, err := store.CreateClearDevProductGoal(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	raw := openClearDevRawDB(t, dir)
	defer raw.Close()
	at := "2026-09-22 00:00:00"
	// The one-pending-discussion unique index is another occupied key: replacing
	// the pending round through a different id would silently delete it.
	if _, err := raw.ExecContext(ctx, `INSERT OR REPLACE INTO cleardev_product_discussions (id, product_id, ordinal, user_message, created_at) VALUES ('replacement', 'replace-guard', 1, 'Rewritten goal', '`+at+`')`); err == nil {
		t.Fatal("replacement deleted the pending discussion")
	}
	pending, found, err := store.GetClearDevProduct(ctx, productID)
	if err != nil || !found || len(pending.Discussions) != 1 || pending.Discussions[0].ID != command.Discussion.ID || pending.Discussions[0].Result != nil {
		t.Fatalf("pending discussion changed: %+v found=%v err=%v", pending, found, err)
	}
	result := productReplaceGuardResult()
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	stepID := "step-replace-guard"
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO cleardev_complex_agent_steps (id, role_binding_id, step_kind, request_id, client_message_id, prompt_sha256, send_status, turn_id, final_message_id, final_message_text, message_sha256, requested_at, sent_at, completed_at) VALUES ('%s', 'replace-guard-steward', 'REQUIREMENT_COMPILATION', '%s', 'replace-guard-client', '%s', 'SETTLED', 'turn-1', 'message-1', '%s', '%s', '%s', '%s', '%s')`,
			stepID, command.Discussion.ID, strings.Repeat("a", 64), strings.ReplaceAll(string(resultJSON), "'", "''"), requirementDigest(string(resultJSON)), at, at, at),
	} {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed settled discussion: %v", err)
		}
	}
	if err := store.SettleClearDevProductDiscussion(ctx, core.SettleProductDiscussionCommand{ProductID: productID, DiscussionID: command.Discussion.ID, AgentStepID: stepID, At: now}); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := store.GetClearDevProduct(ctx, productID)
	if err != nil || !found || len(snapshot.Stages) != 2 {
		t.Fatalf("ready proposal: %+v found=%v err=%v", snapshot, found, err)
	}
	stage := snapshot.Stages[0]
	original := stage
	prd := core.ProductStagePRD(core.ProductGoal{ID: productID, AOProjectID: "ao-product", Name: command.Goal.Name, GoalText: command.Goal.GoalText, RequestID: command.Goal.RequestID, CreatedAt: now}, stage.Definition)
	child := core.CreateComplexRequirementCommand{
		Requirement:     core.DevelopmentRequirement{ID: "replace-guard-child", AOProjectID: "ao-product", Name: stage.Definition.Title, CreatedAt: now, UpdatedAt: now},
		OriginalPRDText: prd, OriginalPRDSHA256: requirementDigest(prd), TargetRequirementVersionID: "replace-guard-target",
		StewardRoleBinding: core.ComplexRoleBinding{ID: "replace-guard-child-steward", DevelopmentRequirementID: "replace-guard-child", Role: core.StandardRoleSteward, SessionCreationIdempotencyKey: "replace-guard-child-key", Status: core.RoleBindingStatusRequested, RequestedAt: now},
	}
	if _, created, err := store.PrepareClearDevProductStage(ctx, core.PrepareProductStageCommand{ProductID: productID, StageID: stage.ID, DefinitionSHA256: stage.DefinitionSHA256, BaseCommitSHA: strings.Repeat("a", 40), Container: child}); err != nil || !created {
		t.Fatalf("prepare stage: created=%v err=%v", created, err)
	}
	weakened, err := json.Marshal(core.ProductStageDefinition{Key: stage.Definition.Key, Title: stage.Definition.Title, Goal: stage.Definition.Goal, FeatureKeys: stage.Definition.FeatureKeys,
		AcceptanceCriteria: []string{"Weakened criterion"}, NonGoals: stage.Definition.NonGoals, Feasibility: "SUPPORTED", FeasibilityReason: stage.Definition.FeasibilityReason})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO cleardev_development_projects (id, ao_project_id, name, state, created_at, updated_at) VALUES ('replace-guard-child-2', 'ao-product', 'Second child', 'INTAKE', '`+at+`', '`+at+`')`); err != nil {
		t.Fatal(err)
	}
	attacks := map[string]string{
		"replace-goal-identity":    `INSERT OR REPLACE INTO cleardev_product_goals (id, request_id, created_at) VALUES ('replace-guard', 'hijacked', '` + at + `')`,
		"replace-goal-request-key": `INSERT OR REPLACE INTO cleardev_product_goals (id, request_id, created_at) VALUES ('other-goal', 'stable-product-request', '` + at + `')`,
		"replace-discussion":       `INSERT OR REPLACE INTO cleardev_product_discussions (id, product_id, ordinal, user_message, created_at) VALUES ('` + command.Discussion.ID + `', 'replace-guard', 0, 'rewritten history', '` + at + `')`,
		"replace-prepared-stage": fmt.Sprintf(`INSERT OR REPLACE INTO cleardev_product_stages (id, product_id, discussion_id, ordinal, definition_json, definition_sha256, created_at) VALUES ('%s', '%s', '%s', 0, '%s', '%s', '%s')`,
			stage.ID, productID, command.Discussion.ID, strings.ReplaceAll(string(weakened), "'", "''"), strings.Repeat("b", 64), at),
		"rebind-with-replace": fmt.Sprintf(`UPDATE OR REPLACE cleardev_product_stages SET development_requirement_id='replace-guard-child', base_commit_sha='%s' WHERE id='%s'`, strings.Repeat("a", 40), snapshot.Stages[1].ID),
		"rebind-with-rowid-steal": fmt.Sprintf(`UPDATE OR REPLACE cleardev_product_stages SET rowid=(SELECT rowid FROM cleardev_product_stages WHERE id='%s'), development_requirement_id='replace-guard-child-2', base_commit_sha='%s' WHERE id='%s'`,
			stage.ID, strings.Repeat("a", 40), snapshot.Stages[1].ID),
	}
	for name, statement := range attacks {
		if _, err := raw.ExecContext(ctx, statement); err == nil {
			t.Fatalf("%s rewrote immutable product history", name)
		}
	}
	after, found, err := store.GetClearDevProduct(ctx, productID)
	if err != nil || !found {
		t.Fatalf("read after attacks: found=%v err=%v", found, err)
	}
	if after.Goal != snapshot.Goal || len(after.Discussions) != 1 || after.Discussions[0].UserMessage != snapshot.Discussions[0].UserMessage {
		t.Fatalf("history changed after rejected attacks: %+v", after)
	}
	for i, current := range after.Stages {
		if current.DefinitionSHA256 != original.DefinitionSHA256 && i == 0 {
			t.Fatalf("prepared stage definition changed: %+v", current)
		}
		if i == 0 && (current.DevelopmentRequirementID != "replace-guard-child" || current.BaseCommitSHA != strings.Repeat("a", 40)) {
			t.Fatalf("prepared stage link was cleared: %+v", current)
		}
	}
	if _, err := store.AppendClearDevProductDiscussion(ctx, core.AppendProductDiscussionCommand{ExpectedPreviousID: command.Discussion.ID, Discussion: core.ProductDiscussion{ID: "reopen-frozen", ProductID: productID, UserMessage: "reopen", CreatedAt: now.Add(time.Second)}}); err == nil {
		t.Fatal("frozen proposal accepted a new discussion after attacks")
	}
	reopened, err := sqlitedb.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, found, err := reopened.GetClearDevProduct(ctx, productID)
	if err != nil || !found || loaded.Stages[0].DevelopmentRequirementID != "replace-guard-child" || loaded.Stages[0].DefinitionSHA256 != original.DefinitionSHA256 {
		t.Fatalf("reopen lost replacement protection state: %+v found=%v err=%v", loaded, found, err)
	}
}

func TestProductStoreRowidAndNullIdentityCannotBypassGuards(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "ao-product", Path: "/tmp/ao-product", Kind: domain.ProjectKindSingleRepo, RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	command := productStoreCommand("rowid-guard", now)
	productID, _, err := store.CreateClearDevProductGoal(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	result := productReplaceGuardResult()
	result.Stages = result.Stages[:1]
	result.Features = result.Features[:1]
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	raw := openClearDevRawDB(t, dir)
	defer raw.Close()
	at := "2026-09-22 00:00:00"
	stepID := "step-rowid-guard"
	statement := fmt.Sprintf(`INSERT INTO cleardev_complex_agent_steps (id, role_binding_id, step_kind, request_id, client_message_id, prompt_sha256, send_status, turn_id, final_message_id, final_message_text, message_sha256, requested_at, sent_at, completed_at) VALUES ('%s', 'rowid-guard-steward', 'REQUIREMENT_COMPILATION', '%s', 'rowid-guard-client', '%s', 'SETTLED', 'turn-1', 'message-1', '%s', '%s', '%s', '%s', '%s')`,
		stepID, command.Discussion.ID, strings.Repeat("a", 64), strings.ReplaceAll(string(resultJSON), "'", "''"), requirementDigest(string(resultJSON)), at, at, at)
	if _, err := raw.ExecContext(ctx, statement); err != nil {
		t.Fatalf("seed settled discussion: %v", err)
	}
	if err := store.SettleClearDevProductDiscussion(ctx, core.SettleProductDiscussionCommand{ProductID: productID, DiscussionID: command.Discussion.ID, AgentStepID: stepID, At: now}); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := store.GetClearDevProduct(ctx, productID)
	if err != nil || !found || len(snapshot.Stages) != 1 {
		t.Fatalf("ready proposal: %+v found=%v err=%v", snapshot, found, err)
	}
	definition := snapshot.Stages[0].Definition
	stageJSON, stageDigest, err := core.ProductStageJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	attacks := map[string]string{
		"replace-discussion-by-rowid": fmt.Sprintf(`INSERT OR REPLACE INTO cleardev_product_discussions (rowid, id, product_id, ordinal, user_message, created_at) VALUES ((SELECT rowid FROM cleardev_product_discussions WHERE id='%s'), 'fresh-discussion', '%s', 1, 'stolen', '%s')`,
			command.Discussion.ID, productID, at),
		"replace-stage-by-rowid": fmt.Sprintf(`INSERT OR REPLACE INTO cleardev_product_stages (rowid, id, product_id, discussion_id, ordinal, definition_json, definition_sha256, created_at) VALUES ((SELECT rowid FROM cleardev_product_stages WHERE id='%s'), 'fresh-stage', '%s', '%s', 1, '%s', '%s', '%s')`,
			snapshot.Stages[0].ID, productID, command.Discussion.ID, strings.ReplaceAll(string(stageJSON), "'", "''"), stageDigest, at),
		"insert-null-goal-id":    `INSERT INTO cleardev_product_goals (id, request_id, created_at) VALUES (NULL, 'fresh-request', '` + at + `')`,
		"insert-null-stage-id":   `INSERT INTO cleardev_product_stages (id, product_id, discussion_id, ordinal, definition_json, definition_sha256, created_at) VALUES (NULL, '` + productID + `', '` + command.Discussion.ID + `', 1, '` + strings.ReplaceAll(string(stageJSON), "'", "''") + `', '` + stageDigest + `', '` + at + `')`,
		"steal-link-via-null-id": fmt.Sprintf(`UPDATE OR REPLACE cleardev_product_stages SET development_requirement_id='rowid-guard-child', base_commit_sha='%s' WHERE id IS NULL`, strings.Repeat("a", 40)),
	}
	for name, statement := range attacks {
		if _, err := raw.ExecContext(ctx, statement); err == nil && name != "steal-link-via-null-id" {
			t.Fatalf("%s rewrote immutable product history", name)
		}
	}
	after, found, err := store.GetClearDevProduct(ctx, productID)
	if err != nil || !found || len(after.Discussions) != 1 || after.Discussions[0].ID != command.Discussion.ID ||
		len(after.Stages) != 1 || after.Stages[0].ID != snapshot.Stages[0].ID || after.Stages[0].DevelopmentRequirementID != "" {
		t.Fatalf("history changed after identity attacks: %+v found=%v err=%v", after, found, err)
	}
}

func TestProductStoreSettlementCannotReplaceOccupiedStep(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "ao-product", Path: "/tmp/ao-product", Kind: domain.ProjectKindSingleRepo, RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	command := productStoreCommand("rowid-guard", now)
	productID, _, err := store.CreateClearDevProductGoal(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	result := productReplaceGuardResult()
	result.Outcome = "DISCUSS"
	result.Stages = []core.ProductStageDefinition{}
	result.Features = []core.ProductFeature{}
	result.Questions = []core.ProductQuestion{{Key: "audience", Text: "Who uses it?", Reason: "Set product scope"}}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	raw := openClearDevRawDB(t, dir)
	defer raw.Close()
	at := "2026-09-22 00:00:00"
	stepID := "step-rowid-guard"
	statement := fmt.Sprintf(`INSERT INTO cleardev_complex_agent_steps (id, role_binding_id, step_kind, request_id, client_message_id, prompt_sha256, send_status, turn_id, final_message_id, final_message_text, message_sha256, requested_at, sent_at, completed_at) VALUES ('%s', 'rowid-guard-steward', 'REQUIREMENT_COMPILATION', '%s', 'rowid-guard-client', '%s', 'SETTLED', 'turn-1', 'message-1', '%s', '%s', '%s', '%s', '%s')`,
		stepID, command.Discussion.ID, strings.Repeat("a", 64), strings.ReplaceAll(string(resultJSON), "'", "''"), requirementDigest(string(resultJSON)), at, at, at)
	if _, err := raw.ExecContext(ctx, statement); err != nil {
		t.Fatalf("seed settled discussion: %v", err)
	}
	if err := store.SettleClearDevProductDiscussion(ctx, core.SettleProductDiscussionCommand{ProductID: productID, DiscussionID: command.Discussion.ID, AgentStepID: stepID, At: now}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.AppendClearDevProductDiscussion(ctx, core.AppendProductDiscussionCommand{ExpectedPreviousID: command.Discussion.ID, Discussion: core.ProductDiscussion{ID: "next-discussion", ProductID: productID, UserMessage: "next round", CreatedAt: now.Add(time.Second)}}); err != nil {
		t.Fatal(err)
	}
	// Reuse the legacy step identity with a new request. Product history must
	// still refuse an otherwise valid settlement using the occupied step.
	if _, err := raw.ExecContext(ctx, strings.ReplaceAll(strings.Replace(statement, "INSERT INTO", "INSERT OR REPLACE INTO", 1), command.Discussion.ID, "next-discussion")); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE OR REPLACE cleardev_product_discussions SET agent_step_id=?, result_json=?, result_sha256=?, settled_at=? WHERE id='next-discussion'`, stepID, string(resultJSON), requirementDigest(string(resultJSON)), at); err == nil {
		t.Fatal("settlement replaced the previous discussion through its occupied agent step")
	} else {
		t.Logf("settlement rejected: %v", err)
	}
	var count int
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM cleardev_product_discussions WHERE product_id=?`, productID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("discussion history lost: count=%d err=%v", count, err)
	}
}
