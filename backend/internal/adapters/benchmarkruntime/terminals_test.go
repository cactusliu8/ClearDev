package benchmarkruntime

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestBoundRuntimeRefusesHostTerminalAndPreservesUnknownLiveness(t *testing.T) {
	runtime := RestrictedTerminals{}
	ctx := context.Background()
	if _, err := runtime.Create(ctx, ports.RuntimeConfig{}); err == nil {
		t.Fatal("host terminal creation accepted")
	}
	if alive, err := runtime.IsAlive(ctx, ports.RuntimeHandle{}); err == nil || alive {
		t.Fatalf("unknown terminal became a liveness fact: %v %v", alive, err)
	}
	if err := runtime.SendMessage(ctx, ports.RuntimeHandle{}, "run"); err == nil {
		t.Fatal("host message accepted")
	}
	if _, err := runtime.Attach(ctx, ports.RuntimeHandle{}, 24, 80); err == nil {
		t.Fatal("host terminal attach accepted")
	}
}
