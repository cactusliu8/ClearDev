package benchmarkruntime

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/runtimeselect"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// RestrictedTerminals prevents a live-bound daemon's terminal fallback from
// launching an unregistered host agent. Role chat uses the private Driver.
// Unknown terminal liveness is an error, never evidence that a session died.
type RestrictedTerminals struct{}

var _ runtimeselect.Runtime = RestrictedTerminals{}

// Create rejects the unregistered host-terminal path.
func (RestrictedTerminals) Create(context.Context, ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	return ports.RuntimeHandle{}, fmt.Errorf("live benchmark requires registered role chat; host terminal unavailable")
}

// Destroy rejects the unregistered host-terminal path.
func (RestrictedTerminals) Destroy(context.Context, ports.RuntimeHandle) error {
	return fmt.Errorf("unregistered benchmark terminal")
}

// IsAlive rejects the unregistered host-terminal path.
func (RestrictedTerminals) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return false, fmt.Errorf("unregistered benchmark terminal identity")
}

// GetOutput rejects the unregistered host-terminal path.
func (RestrictedTerminals) GetOutput(context.Context, ports.RuntimeHandle, int) (string, error) {
	return "", fmt.Errorf("unregistered benchmark terminal")
}

// Attach rejects the unregistered host-terminal path.
func (RestrictedTerminals) Attach(context.Context, ports.RuntimeHandle, uint16, uint16) (ports.Stream, error) {
	return nil, fmt.Errorf("unregistered benchmark terminal")
}

// Interrupt rejects the unregistered host-terminal path.
func (RestrictedTerminals) Interrupt(context.Context, ports.RuntimeHandle) error {
	return fmt.Errorf("unregistered benchmark terminal")
}

// SendInput rejects the unregistered host-terminal path.
func (RestrictedTerminals) SendInput(context.Context, ports.RuntimeHandle, string) error {
	return fmt.Errorf("unregistered benchmark terminal")
}

// SendMessage rejects the unregistered host-terminal path.
func (RestrictedTerminals) SendMessage(context.Context, ports.RuntimeHandle, string) error {
	return fmt.Errorf("unregistered benchmark terminal")
}
