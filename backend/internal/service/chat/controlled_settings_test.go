package chat_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

type fixedChoiceSessions struct {
	record     domain.SessionRecord
	project    domain.ProjectRecord
	projectErr error
}

func (f fixedChoiceSessions) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	return f.record, true, nil
}

func (f fixedChoiceSessions) GetProject(context.Context, string) (domain.ProjectRecord, bool, error) {
	return f.project, f.projectErr == nil, f.projectErr
}

func TestControlledSettingsRejectBeforeWakingProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings domain.ConversationSettings
	}{
		{"different model", domain.ConversationSettings{Model: "provider/other", ApprovalMode: domain.PermissionModeAuto}},
		{"provider default", domain.ConversationSettings{ApprovalMode: domain.PermissionModeAuto}},
		{"different effort", domain.ConversationSettings{Model: "provider/model", ReasoningEffort: "high", ApprovalMode: domain.PermissionModeAuto}},
		{"bypass permissions", domain.ConversationSettings{Model: "provider/model", ApprovalMode: domain.PermissionModeBypassPermissions}},
		{"unset permissions", domain.ConversationSettings{Model: "provider/model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wakes := 0
			svc := chatsvc.New(chatsvc.Options{
				Sessions: fixedChoiceSessions{
					record:  domain.SessionRecord{ID: "p-1", ProjectID: "p", Mode: domain.SessionModeChat, Harness: domain.HarnessOpenCode, PermissionMode: domain.PermissionModeAuto, CreationIdempotencyKey: "cleardev:role", Metadata: domain.SessionMetadata{Model: "provider/model"}},
					project: domain.ProjectRecord{ID: "p", Config: domain.ProjectConfig{ClearDev: &domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "provider/model"}}},
				},
				Wake: func(context.Context, domain.SessionID) error { wakes++; return nil },
			})
			_, err := svc.SetTurnSettings(context.Background(), "p-1", tc.settings)
			if !errors.Is(err, domain.ErrClearDevExecutionFrozen) || wakes != 0 {
				t.Fatalf("settings reached provider: err=%v wakes=%d", err, wakes)
			}
			_, err = svc.SetConfigOption(context.Background(), "p-1", "model", ports.ChatConfigOptionValue{Select: "provider/other"})
			if !errors.Is(err, domain.ErrClearDevExecutionFrozen) || wakes != 0 {
				t.Fatalf("native setter reached provider: err=%v wakes=%d", err, wakes)
			}
		})
	}
}

func TestControlledSettingsKeepOrdinaryLegacyAndSameValuePaths(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      string
		explicit bool
		model    string
		approval domain.PermissionMode
	}{
		{"ordinary", "", true, "provider/other", domain.PermissionModeBypassPermissions},
		{"legacy", "cleardev:legacy", false, "other-model", domain.PermissionModeAuto},
		{"same value initialization", "cleardev:role", true, "provider/model", domain.PermissionModeAuto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := fixedChoiceSessions{record: domain.SessionRecord{ID: "p-1", ProjectID: "p", Mode: domain.SessionModeChat, Harness: domain.HarnessOpenCode, CreationIdempotencyKey: tc.key, PermissionMode: domain.PermissionModeAuto, Metadata: domain.SessionMetadata{Model: "provider/model"}}, project: domain.ProjectRecord{ID: "p"}}
			if tc.explicit {
				reader.project.Config.ClearDev = &domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "provider/model"}
			}
			wakes := 0
			svc := chatsvc.New(chatsvc.Options{Sessions: reader, Wake: func(context.Context, domain.SessionID) error { wakes++; return nil }})
			_, err := svc.SetTurnSettings(context.Background(), "p-1", domain.ConversationSettings{Model: tc.model, ApprovalMode: tc.approval})
			if !errors.Is(err, chatsvc.ErrNoController) || wakes != 1 {
				t.Fatalf("existing path changed: err=%v wakes=%d", err, wakes)
			}
		})
	}
}

func TestNativeCodexEffortInitializationPreservesFrozenSelection(t *testing.T) {
	for _, effort := range []string{"max", "ultra"} {
		for _, requested := range []string{effort, "high"} {
			wakes := 0
			reader := fixedChoiceSessions{
				record:  domain.SessionRecord{ID: "p-1", ProjectID: "p", Mode: domain.SessionModeChat, Harness: domain.HarnessCodex, CreationIdempotencyKey: "cleardev:role", PermissionMode: domain.PermissionModeAuto, Metadata: domain.SessionMetadata{Model: "native-model"}},
				project: domain.ProjectRecord{ID: "p", Config: domain.ProjectConfig{ClearDev: &domain.ClearDevExecutionConfig{Harness: domain.HarnessCodex, Model: "native-model", Effort: effort}}},
			}
			svc := chatsvc.New(chatsvc.Options{Sessions: reader, Wake: func(context.Context, domain.SessionID) error { wakes++; return nil }})
			_, err := svc.SetTurnSettings(context.Background(), "p-1", domain.ConversationSettings{Model: "native-model", ReasoningEffort: requested, ApprovalMode: domain.PermissionModeAuto})
			if requested == effort {
				if !errors.Is(err, chatsvc.ErrNoController) || wakes != 1 {
					t.Fatalf("native effort %s could not reach its unchanged initialization path: %v, wakes=%d", effort, err, wakes)
				}
			} else if !errors.Is(err, domain.ErrClearDevExecutionFrozen) || wakes != 0 {
				t.Fatalf("fixed effort %s changed to %s: %v, wakes=%d", effort, requested, err, wakes)
			}
		}
	}
}
