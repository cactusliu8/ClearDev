package session

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type handoffObserverCommander struct {
	*fakeCommander
	calls  int
	result ports.BuilderHandoffSessionObservation
	err    error
}

func (m *handoffObserverCommander) ObserveBuilderHandoffSession(_ context.Context, id domain.SessionID) (ports.BuilderHandoffSessionObservation, error) {
	m.calls++
	if m.result.Session.ID != id {
		return ports.BuilderHandoffSessionObservation{}, errors.New("wrong exact session")
	}
	return m.result, m.err
}

func TestBuilderHandoffObservationIsExplicitAndNeverCalledByReads(t *testing.T) {
	store := newFakeStore()
	rec := domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex, Mode: domain.SessionModeChat}
	store.sessions[rec.ID] = rec
	manager := &handoffObserverCommander{fakeCommander: &fakeCommander{}, result: ports.BuilderHandoffSessionObservation{Session: rec, RuntimeStopped: true, NoOpenTurn: true, OperationIdle: true, NativeAvailability: ports.NativeSessionAvailabilityUnavailable}}
	service := &Service{store: store, manager: manager}
	if _, err := service.Get(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(context.Background(), ListFilter{}); err != nil {
		t.Fatal(err)
	}
	if manager.calls != 0 {
		t.Fatal("ordinary read probed Builder recovery")
	}
	observed, err := service.ObserveBuilderHandoffSession(context.Background(), rec.ID)
	if err != nil || !reflect.DeepEqual(observed, manager.result) || manager.calls != 1 {
		t.Fatal("explicit observation did not preserve evidence", observed, err)
	}
	manager.err = errors.New("exact runtime probe failed")
	if _, err := service.ObserveBuilderHandoffSession(context.Background(), rec.ID); !errors.Is(err, manager.err) {
		t.Fatal("observer error was converted to evidence", err)
	}
	service.manager = &fakeCommander{}
	if _, err := service.ObserveBuilderHandoffSession(context.Background(), rec.ID); err == nil {
		t.Fatal("missing observer was inferred available")
	}
}
