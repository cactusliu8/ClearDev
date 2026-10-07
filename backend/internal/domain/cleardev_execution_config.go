package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrClearDevExecutionFrozen means controlled work has fixed its tool or model.
var ErrClearDevExecutionFrozen = errors.New("cleardev execution choice is frozen")

// ClearDevExecutionConfig is an explicit project-wide choice, independent of
// ordinary AO worker/orchestrator overrides. It is frozen when controlled work
// starts. A missing choice preserves the historical Codex behaviour.
type ClearDevExecutionConfig struct {
	Harness AgentHarness `json:"agent"`
	Model   string       `json:"model"`
	// Effort is the user-selected, project-wide provider reasoning level. Empty retains the provider default for legacy configurations.
	Effort string `json:"effort,omitempty"`
}

// Validate rejects incomplete or unsupported controlled execution choices.
func (c ClearDevExecutionConfig) Validate() error {
	if c.Harness != HarnessCodex && c.Harness != HarnessOpenCode {
		return fmt.Errorf("cleardev.agent: select codex or opencode")
	}
	if c.Model == "" || strings.TrimSpace(c.Model) != c.Model || len(c.Model) > 240 || strings.ContainsFunc(c.Model, unicode.IsSpace) || strings.ContainsFunc(c.Model, unicode.IsControl) {
		return fmt.Errorf("cleardev.model: an explicit model identifier is required")
	}
	// Models advertise their own effort/variant identifiers. Validate the input
	// shape here, not a stale Codex allowlist; the native protocol remains the
	// authority for support and the fixed project selection is still immutable.
	if len(c.Effort) > 64 || strings.TrimSpace(c.Effort) != c.Effort || strings.ContainsFunc(c.Effort, unicode.IsSpace) || strings.ContainsFunc(c.Effort, unicode.IsControl) {
		return fmt.Errorf("cleardev.effort: invalid reasoning effort")
	}
	if c.Harness == HarnessOpenCode {
		provider, model, ok := strings.Cut(c.Model, "/")
		if !ok || provider == "" || model == "" {
			return fmt.Errorf("cleardev.model: OpenCode requires provider/model")
		}
	}
	return nil
}

// SameClearDevExecution permits historical implicit Codex projects to keep
// their existing per-role model behaviour. Explicit selections must match in
// full before another registered repository can host a product's Stage.
func (c ProjectConfig) SameClearDevExecution(other ProjectConfig) bool {
	if c.ClearDev == nil || other.ClearDev == nil {
		return c.ClearDev == nil && other.ClearDev == nil
	}
	return *c.ClearDev == *other.ClearDev
}

// ClearDevHarness never inherits AO's possibly mixed role settings. Historical
// projects without an explicit controlled choice continue to use Codex.
func (c ProjectConfig) ClearDevHarness() AgentHarness {
	if c.ClearDev == nil {
		return HarnessCodex
	}
	return c.ClearDev.Harness
}
