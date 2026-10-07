package cli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestClearDevMessageBudgetHTTPAndCLIShareRealFacts(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "measured", true: "legacy"}[legacy], func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC()
			dir := t.TempDir()
			store := sqlitetest.MustOpenAt(t, dir)
			if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "budget-project", Path: dir, Kind: domain.ProjectKindSingleRepo, RegisteredAt: now}); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte("requirement"))
			if err := store.CreateClearDevRequirement(ctx, core.InitialRequirement{Requirement: core.DevelopmentRequirement{ID: "budget-root", AOProjectID: "budget-project", Name: "budget", CreatedAt: now, UpdatedAt: now}, Version: core.RequirementVersion{ID: "budget-v1", DevelopmentRequirementID: "budget-root", Version: 1, RequirementText: "requirement", SHA256: hex.EncodeToString(digest[:]), Status: core.RequirementVersionStatusDraft, CreatedAt: now}}); err != nil {
				t.Fatal(err)
			}
			a := core.AgentStepAttempt{ID: "a", DevelopmentRequirementID: "budget-root", LogicalStepID: "step", StepCategory: core.AgentStepCategoryStandard, StepKind: core.AgentStepBuilderResult, AttemptNumber: 1, RoleBindingID: "binding", AOSessionID: "session", ClientMessageID: "message", PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation}
			if _, _, err := store.EnsureClearDevAgentStepAttempt(ctx, a); err != nil {
				t.Fatal(err)
			}
			if legacy {
				db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "ao.db"))
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec(`DROP TRIGGER cleardev_message_budget_versions_update_forbidden; UPDATE cleardev_message_budget_versions SET version='LEGACY_UNMEASURED'`)
				_ = db.Close()
				if err != nil {
					t.Fatal(err)
				}
			} else {
				e := core.AgentAttemptEvent{ID: "boundary", AttemptID: a.ID, ClientMessageID: a.ClientMessageID, PromptSHA256: a.PromptSHA256, Status: core.AgentAttemptDeliveryUnknown, FailureCategory: domain.AgentFailureDeliveryUnknown, RecordedAt: now}
				if _, err := store.ReserveClearDevAgentMessage(ctx, core.ReserveAgentMessageCommand{Attempt: a, Source: core.AgentMessageOriginal, Boundary: e}); err != nil {
					t.Fatal(err)
				}
				e.ID = "sent"
				e.Status = core.AgentAttemptSent
				e.TurnID = "turn"
				e.FailureCategory = ""
				if err := store.ConfirmClearDevAgentMessage(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			service := cleardevsvc.New(cleardevsvc.Deps{Facts: store, AgentAttempts: store})
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, logger, nil, httpd.APIDeps{ClearDev: service}, httpd.ControlDeps{}))
			defer srv.Close()
			cfg := setConfigEnv(t)
			writeRunFileFor(t, cfg, srv)
			res, err := http.Get(srv.URL + "/api/v1/cleardev/requirements/budget-root")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			var direct map[string]any
			if err := json.NewDecoder(res.Body).Decode(&direct); err != nil {
				t.Fatal(err)
			}
			out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "requirement", "show", "budget-root")
			if err != nil {
				t.Fatalf("CLI: %v %s", err, stderr)
			}
			var cli map[string]any
			if err := json.Unmarshal([]byte(out), &cli); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cli, direct) {
				t.Fatalf("HTTP and CLI differ: %v / %v", direct, cli)
			}
			budget, ok := direct["messageBudget"].(map[string]any)
			if !ok {
				t.Fatalf("missing budget: %v", direct)
			}
			step := budget["steps"].([]any)[0].(map[string]any)
			if legacy {
				for _, key := range []string{"reservedMessages", "confirmedSentMessages", "remainingMessages"} {
					if step[key] != nil {
						t.Fatalf("legacy %s=%v", key, step[key])
					}
				}
				if step["unknownReason"] == nil {
					t.Fatal("missing legacy reason")
				}
			} else if step["reservedMessages"] != float64(1) || step["confirmedSentMessages"] != float64(1) || step["remainingMessages"] != float64(2) {
				t.Fatalf("measured=%v", step)
			}
			role := budget["roles"].([]any)[0].(map[string]any)
			if role["maxMessages"] != nil || role["unknownReason"] != "ROLE_BUDGET_NOT_APPLICABLE" {
				t.Fatalf("not applicable=%v", role)
			}
		})
	}
}
