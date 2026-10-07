package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Keep the switch store and the durable Builder-operation contract together:
// ordinary switch fixtures have no managed Builder claim and miss nested sends.
type builderSwitchTestStore struct {
	*switchTestStore
	builderSessionOperationStore
}

type builderSwitchChatLauncher struct {
	*switchAgentChatLauncher
	operations builderSessionOperationStore
	entered    chan context.Context
	release    chan struct{}
}

func (l *builderSwitchChatLauncher) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, text, messageID string) (string, error) {
	l.entered <- ctx
	select {
	case <-l.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	// Match the production Chat boundary: only an exact inherited claim skips
	// admission. A plain context attempts another claim and must fail closed.
	if !ports.BuilderSessionOperationAdmitted(ctx, id) {
		if _, err := l.operations.BeginClearDevBuilderSessionOperation(ctx, id, "nested-chat-send", "chat-send", time.Now()); err != nil {
			return "", err
		}
		return "", errors.New("nested Chat delivery unexpectedly acquired another Builder operation")
	}
	return l.recordingLauncher.RelayChatTurnWithID(ctx, id, text, messageID)
}

func TestSwitchAgentChatBuilderOperationOwnership(t *testing.T) {
	for _, deliveryFails := range []bool{false, true} {
		name := "completed"
		if deliveryFails {
			name = "unknown_delivery_stays_open"
		}
		t.Run(name, func(t *testing.T) {
			manager, switchStore, _ := newSwitchTestManager(t, &fakeRestartRuntime{fakeRuntime: &fakeRuntime{}})
			rec := switchStore.sessions["proj-1"]
			rec.Mode = domain.SessionModeChat
			rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now().UTC()}
			rec.Metadata.RuntimeHandleID = ""
			rec.Metadata.RuntimeLaunchID = ""
			rec.Metadata.AgentSessionID = ""
			rec.Metadata.ProviderConversationID = "source-chat-native"
			rec.Metadata.ControllerGeneration = "source-chat-generation"
			switchStore.sessions[rec.ID] = rec
			operations := newHandoffManagerStore(switchStore.fakeStore)
			manager.store = &builderSwitchTestStore{switchTestStore: switchStore, builderSessionOperationStore: operations}
			launcher := &builderSwitchChatLauncher{
				switchAgentChatLauncher: &switchAgentChatLauncher{
					recordingLauncher: &recordingLauncher{}, store: switchStore, live: true,
				},
				operations: operations, entered: make(chan context.Context, 1), release: make(chan struct{}),
			}
			if deliveryFails {
				launcher.turnErr = errors.New("provider response lost")
			}
			manager.chat = launcher
			cfg := SwitchAgentConfig{TargetHarness: domain.HarnessCodex, Model: "target-provider-model", IdempotencyKey: "builder-chat-switch"}
			sw, err := manager.SwitchAgent(context.Background(), rec.ID, cfg)
			if err != nil {
				t.Fatal(err)
			}
			var deliveryCtx context.Context
			select {
			case deliveryCtx = <-launcher.entered:
			case <-time.After(5 * time.Second):
				close(launcher.release)
				waitForSwitchWorkers(t, manager)
				t.Fatal("switch never reached continuation delivery")
			}
			// Check both the in-memory input gate and the durable exclusion while
			// the internal send is paused; neither may open before acknowledgement.
			if release, ok := manager.AcquireSessionInput(rec.ID); ok {
				release()
				t.Error("external input admitted during switch continuation")
			}
			if claimed, claimErr := operations.BeginClearDevBuilderSessionOperation(context.Background(), rec.ID, "concurrent-resume", "resume", time.Now()); claimErr == nil || claimed {
				t.Error("external lifecycle operation admitted during switch continuation")
			}
			if ports.BuilderSessionOperationAdmitted(deliveryCtx, "other-session") {
				t.Error("operation context authorized a different session")
			}
			inherited := ports.BuilderSessionOperationAdmitted(deliveryCtx, rec.ID)
			close(launcher.release)
			// WaitAgentSwitchWorkers starts shutdown; keep this manager accepting
			// requests until the idempotent replay below has also been exercised.
			manager.agentSwitchWorkers.Wait()
			if !inherited {
				t.Error("continuation did not inherit the exact admitted Builder operation")
			}
			current, found, err := switchStore.GetAgentSwitch(context.Background(), sw.ID)
			if err != nil || !found {
				t.Fatal("missing switch", found, err)
			}
			if len(launcher.relayed) != 1 || len(launcher.relayIDs) != 1 || launcher.relayIDs[0] != chatSwitchActivationMessageID(sw.ID) {
				t.Fatalf("continuation calls = %v / %v, want one exact activation", launcher.relayed, launcher.relayIDs)
			}
			open, err := operations.HasOpenClearDevBuilderSessionOperation(context.Background(), rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if deliveryFails {
				if current.State != domain.AgentSwitchFailed || !open || len(operations.ends) != 0 {
					t.Fatal("unknown delivery was released", current.State, open, operations.ends)
				}
				// A fresh manager must still observe the same unresolved operation.
				fresh, _, _ := newSwitchTestManager(t, &fakeRestartRuntime{fakeRuntime: &fakeRuntime{}})
				fresh.store = manager.store
				if err := fresh.beginAgentOperation(context.Background(), rec.ID, agentOperationResume); err == nil {
					t.Fatal("restart erased an unknown external outcome")
				}
			} else if current.State != domain.AgentSwitchCompleted || open || len(operations.ends) != 1 || operations.ends[0] != "COMPLETED" {
				t.Fatal("successful switch did not settle exactly once", current.State, open, operations.ends)
			}
			// Replaying the durable request is observation, not a second send or end.
			replayed, err := manager.SwitchAgent(context.Background(), rec.ID, cfg)
			if err != nil || replayed.ID != sw.ID || len(launcher.relayed) != 1 {
				t.Fatal("switch replay repeated delivery or changed identity", replayed, err)
			}
			wantEnds := 1
			if deliveryFails {
				wantEnds = 0
			}
			if len(operations.ends) != wantEnds {
				t.Fatal("switch replay repeated settlement", operations.ends)
			}
			waitForSwitchWorkers(t, manager)
		})
	}
}
