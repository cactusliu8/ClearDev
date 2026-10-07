package cleardev

import (
	"context"
	"strings"
	"sync"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

type memoryParseCorrections struct {
	mu   sync.Mutex
	byID map[string]core.ParseCorrection
}

func newMemoryParseCorrections() *memoryParseCorrections {
	return &memoryParseCorrections{byID: map[string]core.ParseCorrection{}}
}

func (m *memoryParseCorrections) GetClearDevParseCorrection(_ context.Context, stepID string) (core.ParseCorrection, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.byID[strings.TrimSpace(stepID)]
	return item, ok, nil
}

func (m *memoryParseCorrections) RecordClearDevParseCorrection(_ context.Context, correction core.ParseCorrection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if correction.AttemptNumber == 0 {
		correction.AttemptNumber = agentFirstAttemptNumber
	}
	if m.byID == nil {
		m.byID = map[string]core.ParseCorrection{}
	}
	id := strings.TrimSpace(correction.StepID)
	if _, exists := m.byID[id]; !exists {
		m.byID[id] = correction
	}
	return nil
}
