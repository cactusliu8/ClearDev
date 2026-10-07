package chat_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// The role lookup is a boundary double here. The store package separately runs
// this same Chat launch against real durable ClearDev roles and unbound seeds.
type unattendedRoleStore struct {
	*sqlite.Store
	controlled bool
	lookupErr  error
	lookedUp   []string
}

func (s *unattendedRoleStore) IsClearDevControlledSession(_ context.Context, id string) (bool, error) {
	s.lookedUp = append(s.lookedUp, id)
	return s.controlled, s.lookupErr
}

func unattendedSessionFixture(t *testing.T, mode domain.PermissionMode) (*sqlite.Store, domain.SessionRecord, chatsvc.StartConfig) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st := sqlitetest.MustOpenAt(t, dir)
	if err := st.UpsertProject(ctx, domain.ProjectRecord{ID: "unattended", Path: dir, RegisteredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "unattended", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, PermissionMode: mode,
		CreationIdempotencyKey: "cleardev-worker:exact-role", CreationRequestFingerprint: "original-fingerprint",
		Metadata:  domain.SessionMetadata{WorkspacePath: dir, Model: "provider/model"},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, record, chatsvc.StartConfig{
		SessionID: record.ID, ProjectID: record.ProjectID, Kind: record.Kind,
		Harness: record.Harness, WorkspacePath: dir, Model: record.Metadata.Model,
		Permissions: mode, CreationIdempotencyKey: record.CreationIdempotencyKey,
		SystemPrompt: "Unchanged role instructions.", Env: map[string]string{"KEEP": "value"},
	}
}

func TestControlledRuntimeFullForNewRestoreAndSwitchWithoutIdentityRewrite(t *testing.T) {
	for _, mode := range []string{"new-codex", "restore-codex", "new-opencode", "restore-opencode", "switch-to-opencode"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st, original, cfg := unattendedSessionFixture(t, domain.PermissionModeAuto)
			if mode == "new-opencode" || mode == "restore-opencode" || mode == "switch-to-opencode" {
				cfg.Harness = domain.HarnessOpenCode
			}
			if mode == "restore-codex" || mode == "restore-opencode" {
				cfg.ProviderConversationID = "original-native-thread"
			}
			if mode == "switch-to-opencode" {
				// The lifecycle coordinator has not committed the target harness yet.
				// It still owns authorization; the Chat boundary does not rewrite it.
				cfg.ControllerGeneration = "target-generation"
				cfg.SkipNativeHistoryImport = true
			}
			roleStore := &unattendedRoleStore{Store: st, controlled: true}
			conv := newFakeConversation()
			var starts ports.ChatStartConfig
			var resumes ports.ChatResumeConfig
			var ids atomic.Int64
			svc := chatsvc.New(chatsvc.Options{Store: roleStore, Sessions: st,
				Drivers: fakeRegistry{driver: fakeDriver{conv: conv, startCfg: &starts, resumeCfg: &resumes}},
				NewID:   func() string { return fmt.Sprintf("unattended-%d", ids.Add(1)) },
			})
			controller, err := svc.Start(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = svc.Stop(context.Background(), original.ID) })
			got := starts.Permissions
			if cfg.ProviderConversationID != "" {
				got = resumes.Permissions
				if resumes.ProviderConversationID != cfg.ProviderConversationID {
					t.Fatal("restore replaced the native identity")
				}
			}
			if got != ports.PermissionModeBypassPermissions {
				t.Fatalf("native permission=%q, want full without changing stored Auto", got)
			}
			if !reflect.DeepEqual(roleStore.lookedUp, []string{string(original.ID)}) {
				t.Fatal("runtime posture did not use the exact durable session", roleStore.lookedUp)
			}
			settings := domain.ConversationSettings{Model: "provider/model", ReasoningEffort: "high", ApprovalMode: domain.PermissionModeAuto}
			if _, err := svc.SetTurnSettings(ctx, original.ID, settings); err != nil {
				t.Fatal(err)
			}
			if _, err := controller.Send(ctx, ports.ChatUserMessage{Text: "ordinary work", ClientMessageID: "one-work-message"}); err != nil {
				t.Fatal(err)
			}
			messages := conv.sentMessages()
			if len(messages) != 1 || messages[0].Settings.Approval != ports.PermissionModeBypassPermissions || messages[0].Settings.Model != settings.Model || messages[0].Settings.Effort != settings.ReasoningEffort {
				t.Fatal("per-turn settings reverted full or changed the model", messages)
			}
			if controller.Settings() != settings {
				t.Fatal("runtime policy rewrote stored conversation choices")
			}
			saved, found, err := st.GetSession(ctx, original.ID)
			if err != nil || !found || saved.PermissionMode != original.PermissionMode || saved.CreationRequestFingerprint != original.CreationRequestFingerprint || saved.CreationIdempotencyKey != original.CreationIdempotencyKey || saved.Metadata.Model != original.Metadata.Model || saved.Harness != original.Harness {
				t.Fatal("native posture changed durable identity", saved, err)
			}
			if cfg.Permissions != domain.PermissionModeAuto || cfg.Env["KEEP"] != "value" {
				t.Fatal("caller configuration was mutated")
			}
		})
	}
}

func TestControlledRuntimePermissionNegativeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       domain.PermissionMode
		controlled bool
		lookupErr  error
		wrong      string
		wantError  bool
	}{
		{name: "ordinary auto even with a convincing creation key", mode: domain.PermissionModeAuto},
		{name: "ordinary default", mode: domain.PermissionModeDefault},
		{name: "explicit restricted role", mode: domain.PermissionModeAcceptEdits, controlled: true},
		{name: "lookup failure", mode: domain.PermissionModeAuto, controlled: true, lookupErr: errors.New("lookup failed"), wantError: true},
		{name: "wrong project", mode: domain.PermissionModeAuto, controlled: true, wrong: "project", wantError: true},
		{name: "wrong role kind", mode: domain.PermissionModeAuto, controlled: true, wrong: "kind", wantError: true},
		{name: "wrong stored permission", mode: domain.PermissionModeAcceptEdits, controlled: true, wrong: "permission", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, original, cfg := unattendedSessionFixture(t, tc.mode)
			roleStore := &unattendedRoleStore{Store: st, controlled: tc.controlled, lookupErr: tc.lookupErr}
			switch tc.wrong {
			case "project":
				cfg.ProjectID = "other"
			case "kind":
				cfg.Kind = domain.KindOrchestrator
			case "permission":
				cfg.Permissions = domain.PermissionModeAuto
			}
			calls := 0
			var received ports.ChatStartConfig
			conv := newFakeConversation()
			var ids atomic.Int64
			svc := chatsvc.New(chatsvc.Options{Store: roleStore, Sessions: st,
				Drivers: fakeRegistry{driver: fakeDriver{start: func(c ports.ChatStartConfig) (ports.ChatConversation, error) { calls++; received = c; return conv, nil }}},
				NewID:   func() string { return fmt.Sprintf("negative-%d", ids.Add(1)) },
			})
			_, err := svc.Start(context.Background(), cfg)
			t.Cleanup(func() { _ = svc.Stop(context.Background(), original.ID); _ = conv.Close() })
			if tc.wantError {
				if err == nil || calls != 0 {
					t.Fatal("unverified identity reached native launch", calls, err)
				}
				return
			}
			if err != nil || calls != 1 || received.Permissions != tc.mode {
				t.Fatal("ordinary or restricted policy changed", calls, received.Permissions, err)
			}
		})
	}
}
