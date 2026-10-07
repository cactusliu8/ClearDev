package chat_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type preSendErrorStore struct {
	*sqlite.Store
	err error
}

func (s *preSendErrorStore) BeginClearDevBuilderSessionOperation(context.Context, domain.SessionID, string, string, time.Time) (bool, error) {
	return false, s.err
}

func TestPreSendChatMarksOnlyKnownLocalAdmissionRefusal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		proof bool
	}{
		{"busy lifecycle", ports.ErrBuilderSessionOperationBusy, true},
		{"unknown store error", errors.New("commit response lost"), false},
		{"retired identity", ports.ErrBuilderSessionFenced, false},
		{"login", ports.ErrChatAuthRequired, false},
		{"quota", ports.ErrChatQuotaExhausted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWithConversationAndStore(t, nil, func(st *sqlite.Store) chatsvc.Store { return &preSendErrorStore{Store: st, err: tc.err} })
			_, err := h.svc.RelayChatTurnWithID(context.Background(), testSession, "original work", "original-message")
			if !errors.Is(err, tc.err) || errors.Is(err, ports.ErrChatSendNotStarted) != tc.proof {
				t.Fatal("incorrect local proof or lost original error", err)
			}
			if len(h.conv.sentMessages()) != 0 {
				t.Fatal("local refusal reached provider")
			}
			snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
			if err != nil || len(snapshot.Messages) != 0 || len(snapshot.Turns) != 0 {
				t.Fatal("local refusal created message/turn", snapshot, err)
			}
		})
	}
}

func TestPreSendChatControllerFailureNeverProducesLocalProof(t *testing.T) {
	h := newHarness(t)
	h.conv.sendErr = errors.New("provider may have accepted before losing response")
	_, err := h.svc.RelayChatTurnWithID(context.Background(), testSession, "original work", "original-message")
	if err == nil || errors.Is(err, ports.ErrChatSendNotStarted) {
		t.Fatal("controller error became pre-send proof", err)
	}
	failure, ok := ports.ChatFailureFromError(err)
	if !ok || failure.Category != domain.AgentFailureDeliveryUnknown || failure.Retryable {
		t.Fatal("unknown failure lost fail-closed classification", failure)
	}
	snapshot, readErr := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if readErr != nil || len(snapshot.Messages) != 1 || len(snapshot.Turns) != 1 {
		t.Fatal("expected controller's durable uncertain turn", snapshot, readErr)
	}
}
