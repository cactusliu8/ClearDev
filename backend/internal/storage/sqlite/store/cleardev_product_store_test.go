package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sqlitedb "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func productStoreCommand(id string, now time.Time) core.CreateProductGoalCommand {
	goal := "A local mailbox with searchable saved contacts"
	c := core.CreateComplexRequirementCommand{
		Requirement:     core.DevelopmentRequirement{ID: id, AOProjectID: "ao-product", Name: "Mail", CreatedAt: now, UpdatedAt: now},
		OriginalPRDText: goal, OriginalPRDSHA256: requirementDigest(goal), TargetRequirementVersionID: id + "-unused-target",
		StewardRoleBinding: core.ComplexRoleBinding{ID: id + "-steward", DevelopmentRequirementID: id, Role: core.StandardRoleSteward, SessionCreationIdempotencyKey: "product-steward:" + id, Status: core.RoleBindingStatusRequested, RequestedAt: now},
	}
	return core.CreateProductGoalCommand{
		Goal:      core.ProductGoal{ID: id, AOProjectID: "ao-product", Name: "Mail", GoalText: goal, RequestID: "stable-product-request", CreatedAt: now},
		Container: c, Discussion: core.ProductDiscussion{ID: id + "-initial", ProductID: id, UserMessage: goal, CreatedAt: now},
	}
}

func TestProductStoreConcurrentCreateAndReopenKeepOneImmutableGoal(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "ao-product", Path: "/tmp/ao-product", Kind: domain.ProjectKindSingleRepo, RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids, errs := make(chan string, 8), make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, _, err := store.CreateClearDevProductGoal(ctx, productStoreCommand(fmt.Sprintf("goal-%d", i), now))
			ids <- id
			errs <- err
		}(i)
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	canonical := ""
	for id := range ids {
		if canonical != "" && canonical != id {
			t.Fatal("one idempotency key created multiple products")
		}
		canonical = id
	}
	view, ok, err := store.GetClearDevProduct(ctx, canonical)
	if err != nil || !ok || len(view.Discussions) != 1 || len(view.Stages) != 0 {
		t.Fatalf("created product: %+v found=%v err=%v", view, ok, err)
	}
	wrong := productStoreCommand("different", now)
	wrong.Goal.GoalText = "different goal"
	if _, _, err := store.CreateClearDevProductGoal(ctx, wrong); err == nil {
		t.Fatal("same request key accepted different goal")
	}
	if _, err := store.AppendClearDevProductDiscussion(ctx, core.AppendProductDiscussionCommand{ExpectedPreviousID: view.Discussions[0].ID, Discussion: core.ProductDiscussion{ID: "too-early", ProductID: canonical, UserMessage: "new input", CreatedAt: now}}); err == nil {
		t.Fatal("new discussion accepted while the previous reply is pending")
	}
	if err := store.FailClearDevProductInvestigation(ctx, canonical, view.Discussions[0].ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlitedb.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	loaded, ok, err := store.GetClearDevProduct(ctx, canonical)
	if err != nil || !ok || len(loaded.Discussions) != 1 || loaded.Discussions[0].FailureReason != "PRODUCT_STEWARD_WORKSPACE_CHANGED" || loaded.Discussions[0].Result != nil {
		t.Fatalf("reopened product lost failure: %+v found=%v err=%v", loaded, ok, err)
	}
	if _, err := store.AppendClearDevProductDiscussion(ctx, core.AppendProductDiscussionCommand{ExpectedPreviousID: loaded.Discussions[0].ID, Discussion: core.ProductDiscussion{ID: "after-failure", ProductID: canonical, UserMessage: "retry by resetting history", CreatedAt: now}}); err == nil {
		t.Fatal("failed investigation was converted into another round")
	}
	raw := openClearDevRawDB(t, dir)
	defer raw.Close()
	for _, statement := range []string{
		`DELETE FROM cleardev_product_goals`,
		`DELETE FROM cleardev_product_discussions`,
		`UPDATE cleardev_product_discussions SET user_message='rewritten'`,
		`UPDATE cleardev_product_goals SET request_id='rewritten'`,
	} {
		if _, err := raw.ExecContext(ctx, statement); err == nil {
			t.Fatalf("immutable history accepted %s", statement)
		}
	}
	var cdc, contracts, tasks int
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM change_log WHERE event_type='cleardev_project_updated' AND json_extract(payload,'$.productId')=?`, canonical).Scan(&cdc); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM cleardev_contract_versions WHERE development_project_id=?`, canonical).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM cleardev_work_items WHERE development_project_id=?`, canonical).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if cdc < 3 || contracts != 0 || tasks != 0 {
		t.Fatalf("product leaked execution or omitted CDC: cdc=%d contracts=%d tasks=%d", cdc, contracts, tasks)
	}
}

func TestProductStoreInvalidRegistrationRollsBackContainerAndHistory(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "ao-product", Path: "/tmp/ao-product", Kind: domain.ProjectKindSingleRepo, RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	seed := productStoreCommand("existing-product", now)
	if _, _, err := store.CreateClearDevProductGoal(ctx, seed); err != nil {
		t.Fatal(err)
	}
	c := productStoreCommand("invalid-product", now)
	c.Goal.RequestID = "different-create-request"
	// Fails at the final discussion INSERT, after container and marker INSERTs.
	c.Discussion.ID = seed.Discussion.ID
	if _, _, err := store.CreateClearDevProductGoal(ctx, c); err == nil {
		t.Fatal("invalid registration succeeded")
	}
	if _, found, err := store.GetClearDevRequirement(ctx, c.Goal.ID); err != nil || found {
		t.Fatalf("partial container survived rejection: found=%v err=%v", found, err)
	}
}
