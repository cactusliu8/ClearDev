package sessionmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
)

type observingChatCapabilityLauncher struct {
	*recordingLauncher
	beforeStart func(ChatStart)
}

func (l *observingChatCapabilityLauncher) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	if l.beforeStart != nil {
		l.beforeStart(cfg)
	}
	return l.recordingLauncher.StartChat(ctx, cfg)
}

func TestChatBrowserCapabilityPersistsBeforeLaunchAndRotatesOnRestore(t *testing.T) {
	ctx := context.Background()
	launcher := &observingChatCapabilityLauncher{recordingLauncher: &recordingLauncher{}}
	mgr, store, _ := newChatManager(launcher)
	authority := browser.NewAuthority()
	mgr.browserCapabilities = authority
	project := store.projects[string(chatTestProject)]
	project.Config.Env = map[string]string{
		EnvBrowserCapability:   "project-must-not-choose",
		EnvHumanAuthorityToken: "private-human-channel",
		EnvBrowserRuntimeToken: "private-browser-runtime",
	}
	store.projects[string(chatTestProject)] = project
	launcher.beforeStart = func(cfg ChatStart) {
		stored, found, err := store.GetSession(ctx, cfg.SessionID)
		if err != nil || !found {
			t.Fatalf("read before launch: found %v err %v", found, err)
		}
		if !authority.Valid(cfg.SessionID, cfg.Env[EnvBrowserCapability], stored.Metadata.BrowserCapabilityVerifier) {
			t.Fatal("provider started without its already-persisted session capability")
		}
		if cfg.Env[EnvHumanAuthorityToken] != "" || cfg.Env[EnvBrowserRuntimeToken] != "" {
			t.Fatal("private desktop credentials reached the provider")
		}
	}
	rec, _, _, err := mgr.Spawn(ctx, ports.SpawnConfig{
		ProjectID: chatTestProject, Kind: domain.KindWorker, Harness: domain.HarnessCodex, RequestedMode: domain.SessionModeChat,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := launcher.started[0].Env[EnvBrowserCapability]
	if !authority.Valid(rec.ID, first, rec.Metadata.BrowserCapabilityVerifier) {
		t.Fatal("controller-ready commit lost the verifier")
	}
	if _, err := mgr.Kill(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	result, err := mgr.RestoreWithMode(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := launcher.started[1].Env[EnvBrowserCapability]
	if second == first || authority.Valid(rec.ID, first, result.Session.Metadata.BrowserCapabilityVerifier) ||
		!authority.Valid(rec.ID, second, result.Session.Metadata.BrowserCapabilityVerifier) {
		t.Fatal("restore did not replace the old capability")
	}
	if launcher.started[1].ProviderConversationID != rec.Metadata.ProviderConversationID {
		t.Fatal("capability rotation replaced the provider conversation")
	}
}

type unavailableChatCapabilityIssuer struct{}

func (unavailableChatCapabilityIssuer) Issue(domain.SessionID) (string, string, error) {
	return "", "", errors.New("capability unavailable")
}

type chatCapabilityWriteFailureStore struct{ *fakeStore }

func (s chatCapabilityWriteFailureStore) UpdateSession(ctx context.Context, rec domain.SessionRecord) error {
	if rec.Metadata.BrowserCapabilityVerifier != "" {
		return errors.New("verifier persistence failed")
	}
	return s.fakeStore.UpdateSession(ctx, rec)
}

func TestChatBrowserCapabilityFailureNeverStartsProvider(t *testing.T) {
	for _, failure := range []string{"issue", "persist"} {
		t.Run(failure, func(t *testing.T) {
			launcher := &recordingLauncher{}
			mgr, store, _ := newChatManager(launcher)
			mgr.browserCapabilities = browser.NewAuthority()
			if failure == "issue" {
				mgr.browserCapabilities = unavailableChatCapabilityIssuer{}
			} else {
				mgr.store = chatCapabilityWriteFailureStore{store}
			}
			_, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
				ProjectID: chatTestProject, Kind: domain.KindWorker, Harness: domain.HarnessCodex, RequestedMode: domain.SessionModeChat,
			})
			if err == nil || len(launcher.started) != 0 || len(launcher.turns) != 0 {
				t.Fatal("capability failure started an unauthorized provider")
			}
		})
	}
}

func TestChatBrowserCapabilityRestoreFailureNeverStartsProvider(t *testing.T) {
	for _, failure := range []string{"issue", "persist"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			launcher := &recordingLauncher{}
			mgr, store, _ := newChatManager(launcher)
			mgr.browserCapabilities = browser.NewAuthority()
			rec, _, _, err := mgr.Spawn(ctx, ports.SpawnConfig{
				ProjectID: chatTestProject, Kind: domain.KindWorker, Harness: domain.HarnessCodex, RequestedMode: domain.SessionModeChat,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.Kill(ctx, rec.ID); err != nil {
				t.Fatal(err)
			}
			if failure == "issue" {
				mgr.browserCapabilities = unavailableChatCapabilityIssuer{}
			} else {
				mgr.store = chatCapabilityWriteFailureStore{store}
			}
			_, err = mgr.RestoreWithMode(ctx, rec.ID)
			if err == nil || len(launcher.started) != 1 || len(launcher.turns) != 0 {
				t.Fatal("failed capability restore reached the provider")
			}
		})
	}
}
