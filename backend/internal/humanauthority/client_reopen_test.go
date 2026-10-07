package humanauthority

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

type reopenClientHandler struct {
	issued    atomic.Bool
	dismissed chan core.HumanDecisionDispatchOutcome
	now       *atomic.Int64
}

func (*reopenClientHandler) BackfillHumanDecisionRequests(context.Context) error { return nil }
func (h *reopenClientHandler) IssueHumanDecisionOffer(context.Context, string) (core.HumanDecisionOffer, bool, error) {
	if !h.issued.CompareAndSwap(false, true) {
		return core.HumanDecisionOffer{}, false, nil
	}
	nonce, _ := NewToken()
	return core.HumanDecisionOffer{Nonce: nonce, ExpiresAt: time.Unix(h.now.Load(), 0).Add(time.Minute).UTC().Format(time.RFC3339)}, true, nil
}
func (*reopenClientHandler) ApplyHumanDecisionResult(context.Context, core.HumanDecisionResult) error {
	return nil
}
func (h *reopenClientHandler) DismissHumanDecisionDispatch(_ context.Context, c core.DismissHumanDecisionDispatchCommand) error {
	h.dismissed <- c.Outcome
	return nil
}
func (*reopenClientHandler) DismissOpenHumanDecisionDispatches(context.Context, string, core.HumanDecisionDispatchOutcome, time.Time) error {
	return nil
}

func TestClientUnattendedExpiryKeepsReconnecting(t *testing.T) {
	if testing.Short() {
		t.Skip("local private transport lifecycle")
	}
	endpoint := filepath.Join(t.TempDir(), "authority.sock")
	listener, err := Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	token := mustToken(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var now atomic.Int64
	now.Store(time.Now().Unix())
	h := &reopenClientHandler{dismissed: make(chan core.HumanDecisionDispatchOutcome, 4), now: &now}
	client := NewClient(endpoint, token, "desktop", h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	client.now = func() time.Time { return time.Unix(now.Load(), 0).UTC() }
	connected := make(chan net.Conn, 2)
	go func() {
		for i := 0; i < 2; i++ {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			if e := PerformServerHandshake(c, token, "desktop"); e != nil {
				_ = c.Close()
				return
			}
			connected <- c
		}
	}()
	done := make(chan struct{})
	go func() { defer close(done); client.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("client did not stop")
		}
	}()
	var first net.Conn
	select {
	case first = <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("first private handshake failed")
	}
	defer func() { _ = first.Close() }()
	if _, err := ReadFrame(first); err != nil {
		t.Fatal(err)
	}
	now.Add(int64((core.HumanDecisionOfferTTL + time.Second) / time.Second))
	select {
	case outcome := <-h.dismissed:
		if outcome != core.HumanDecisionDispatchExpired {
			t.Fatal("expiry outcome changed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expiry was not recorded")
	}
	select {
	case second := <-connected:
		_ = second.Close()
	case <-done:
		t.Fatal("unattended expiry stopped the permanent client")
	case <-time.After(3 * time.Second):
		t.Fatal("client did not reconnect after expiry")
	}
}

func TestClientUsesIssuedOfferExpiryAndDismissesFailedWrite(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	var clock atomic.Int64
	clock.Store(now.Unix())
	h := &reopenClientHandler{dismissed: make(chan core.HumanDecisionDispatchOutcome, 4), now: &clock}
	client := NewClient("socket", "token", "desktop", h, nil)
	client.now = func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	a, b := net.Pipe()
	_ = b.Close()
	defer func() { _ = a.Close() }()
	if err := client.loop(context.Background(), a); err == nil {
		t.Fatal("failed write was accepted")
	}
	select {
	case outcome := <-h.dismissed:
		if outcome != core.HumanDecisionDispatchDisconnected {
			t.Fatal(outcome)
		}
	default:
		t.Fatal("issued nonce survived failed write")
	}
}
