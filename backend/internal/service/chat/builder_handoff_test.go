package chat_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type observedHandoffConversation struct {
	*fakeConversation
	stopped atomic.Bool
}

func (c *observedHandoffConversation) ProcessStopped() bool { return c.stopped.Load() }

func TestBuilderHandoffObservationBindsExactChatProcessAndGeneration(t *testing.T) {
	conv := &observedHandoffConversation{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, conv)
	ctx := context.Background()
	if stopped, err := h.svc.ObserveBuilderHandoffChatRuntime(ctx, testSession, conv.ProviderConversationID(), h.ctrl.Generation()); err != nil || stopped {
		t.Fatal("live provider misobserved", stopped, err)
	}
	// Closing normalized events removes the registry, but the provider process
	// observer remains false and therefore supplies no stop proof.
	if err := conv.Close(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool { return !h.svc.HasLiveChatController(testSession) })
	if stopped, err := h.svc.ObserveBuilderHandoffChatRuntime(ctx, testSession, conv.ProviderConversationID(), h.ctrl.Generation()); err != nil || stopped {
		t.Fatal("missing registry became stop proof", stopped, err)
	}
	conv.stopped.Store(true)
	if stopped, err := h.svc.ObserveBuilderHandoffChatRuntime(ctx, testSession, conv.ProviderConversationID(), h.ctrl.Generation()); err != nil || !stopped {
		t.Fatal("exact process completion was not retained", stopped, err)
	}
	for _, identity := range [][2]string{{"other-provider", h.ctrl.Generation()}, {conv.ProviderConversationID(), "other-generation"}, {"", ""}} {
		if stopped, err := h.svc.ObserveBuilderHandoffChatRuntime(ctx, testSession, identity[0], identity[1]); err != nil || stopped {
			t.Fatal("mismatched identity became stop proof", identity, err)
		}
	}
	fresh := chatsvc.New(chatsvc.Options{})
	if stopped, err := fresh.ObserveBuilderHandoffChatRuntime(ctx, testSession, conv.ProviderConversationID(), h.ctrl.Generation()); err != nil || stopped {
		t.Fatal("new daemon invented old process evidence", stopped, err)
	}
}

type handoffFenceChatStore struct {
	*sqlite.Store
	mu           sync.Mutex
	fenced       bool
	open         string
	starts, ends int
}

func (s *handoffFenceChatStore) IsClearDevBuilderSessionFenced(context.Context, domain.SessionID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fenced, nil
}
func (s *handoffFenceChatStore) BeginClearDevBuilderSessionOperation(_ context.Context, _ domain.SessionID, id, _ string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fenced {
		return false, ports.ErrBuilderSessionFenced
	}
	if s.open != "" {
		return false, ports.ErrBuilderSessionOperationBusy
	}
	s.open = id
	s.starts++
	return true, nil
}
func (s *handoffFenceChatStore) EndClearDevBuilderSessionOperation(_ context.Context, id, outcome string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open != id || outcome != "COMPLETED" {
		return errors.New("wrong settled operation")
	}
	s.open = ""
	s.ends++
	return nil
}

func TestBuilderHandoffFencedChatSendAndAutomaticWakeDoNoProviderAction(t *testing.T) {
	var fence *handoffFenceChatStore
	h := newHarnessWithConversationAndStore(t, nil, func(st *sqlite.Store) chatsvc.Store { fence = &handoffFenceChatStore{Store: st}; return fence })
	fence.fenced = true
	if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{Text: "forbidden old worker send"}); !errors.Is(err, ports.ErrBuilderSessionFenced) {
		t.Fatal("fenced send admitted", err)
	}
	if len(h.conv.sent) != 0 || fence.starts != 0 {
		t.Fatal("fenced send touched provider")
	}
	if err := h.svc.Stop(context.Background(), testSession); err != nil {
		t.Fatal(err)
	}
	var wakes atomic.Int32
	h.svc.SetControllerWake(func(context.Context, domain.SessionID) error { wakes.Add(1); return nil })
	if _, _, err := h.svc.Models(context.Background(), testSession); !errors.Is(err, ports.ErrBuilderSessionFenced) {
		t.Fatal("fenced automatic wake admitted", err)
	}
	if wakes.Load() != 0 {
		t.Fatal("fenced identity was automatically resumed")
	}
}

func TestBuilderHandoffRelayReusesOnlyExactSessionOperation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		owner   domain.SessionID
		fenced  bool
		allowed bool
	}{
		{name: "same session inherits outer switch", owner: testSession, allowed: true},
		{name: "ordinary relay cannot enter"},
		{name: "other session claim cannot enter", owner: "other-session"},
		{name: "retired session remains fenced", owner: testSession, fenced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fence *handoffFenceChatStore
			h := newHarnessWithConversationAndStore(t, nil, func(st *sqlite.Store) chatsvc.Store {
				fence = &handoffFenceChatStore{Store: st, open: "outer-switch", fenced: tc.fenced}
				return fence
			})
			ctx := context.Background()
			if tc.owner != "" {
				ctx = ports.WithBuilderSessionOperation(ctx, tc.owner)
			}
			_, err := h.svc.RelayChatTurnWithID(ctx, testSession, "continue authorized work", "agent-switch:test:activation")
			if tc.allowed && err != nil {
				t.Fatal("inherited operation was rejected", err)
			}
			if !tc.allowed && err == nil {
				t.Fatal("unowned or fenced relay was admitted")
			}
			if tc.fenced && !errors.Is(err, ports.ErrBuilderSessionFenced) {
				t.Fatal("retired identity did not fail at its fence", err)
			}
			wantSends := 0
			if tc.allowed {
				wantSends = 1
			}
			if len(h.conv.sent) != wantSends || fence.starts != 0 || fence.ends != 0 || fence.open != "outer-switch" {
				t.Fatal("relay duplicated the claim, released its owner, or touched an unauthorized provider", len(h.conv.sent), fence.starts, fence.ends, fence.open)
			}
		})
	}
}

func TestBuilderHandoffUnknownChatDeliveryRetainsDurableOperation(t *testing.T) {
	var fence *handoffFenceChatStore
	h := newHarnessWithConversationAndStore(t, nil, func(st *sqlite.Store) chatsvc.Store { fence = &handoffFenceChatStore{Store: st}; return fence })
	h.conv.sendErr = errors.New("provider response lost")
	if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{Text: "uncertain delivery"}); err == nil {
		t.Fatal("expected uncertain send failure")
	}
	if fence.starts != 1 || fence.ends != 0 || fence.open == "" {
		t.Fatal("unknown delivery was released", fence.starts, fence.ends)
	}
}
