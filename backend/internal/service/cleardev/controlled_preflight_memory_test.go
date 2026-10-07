package cleardev

import (
	"context"
	"strings"
	"sync"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type alwaysPassControlledPreflight struct{}

func (alwaysPassControlledPreflight) CheckControlledPreflight(_ context.Context, harness domain.AgentHarness, requestedModel string) (ports.ChatControlledPreflight, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	models := []ports.ChatModel{}
	if requestedModel != "" {
		models = []ports.ChatModel{{ID: requestedModel, Default: true}}
	}
	catalogJSON, digest := normalizePreflightCatalog(models)
	return ports.ChatControlledPreflight{
		RequestedModel: requestedModel,
		ResolvedModel:  requestedModel,
		Provider:       string(harness),
		Models:         models,
		CatalogJSON:    catalogJSON,
		CatalogSHA256:  digest,
	}, nil
}

type memoryControlledPreflights struct {
	mu      sync.Mutex
	records []core.ControlledPreflight
}

func newMemoryControlledPreflights() *memoryControlledPreflights {
	return &memoryControlledPreflights{}
}

func (m *memoryControlledPreflights) RecordClearDevControlledPreflight(_ context.Context, record core.ControlledPreflight) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, record)
	return nil
}

func (m *memoryControlledPreflights) GetLatestClearDevControlledPreflight(_ context.Context, requirementID string) (core.ControlledPreflight, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	latest, found := latestControlledPreflight(m.records, func(record core.ControlledPreflight) bool {
		return record.DevelopmentRequirementID == requirementID
	})
	return latest, found, nil
}

func (m *memoryControlledPreflights) GetLatestClearDevControlledPreflightForBinding(_ context.Context, roleBindingID string) (core.ControlledPreflight, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	latest, found := latestControlledPreflight(m.records, func(record core.ControlledPreflight) bool {
		return record.RoleBindingID == roleBindingID
	})
	return latest, found, nil
}

func latestControlledPreflight(records []core.ControlledPreflight, match func(core.ControlledPreflight) bool) (core.ControlledPreflight, bool) {
	var latest core.ControlledPreflight
	found := false
	for _, record := range records {
		if !match(record) {
			continue
		}
		if !found || record.CheckedAt.After(latest.CheckedAt) || (record.CheckedAt.Equal(latest.CheckedAt) && record.ID > latest.ID) {
			latest = record
			found = true
		}
	}
	return latest, found
}
