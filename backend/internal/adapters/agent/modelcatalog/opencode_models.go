package modelcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// OpenCode --verbose emits a model identifier followed by one JSON object.
// Decode each object as JSON (not by braces/lines: metadata contains nested objects).
func parseOpenCodeModels(raw []byte) ([]ports.AgentModelInfo, error) {
	rest := bytes.TrimSpace(raw)
	var models []ports.AgentModelInfo
	for len(rest) > 0 {
		line, body, ok := bytes.Cut(rest, []byte("\n"))
		if !ok {
			return nil, fmt.Errorf("OpenCode model metadata missing")
		}
		id := strings.TrimSpace(string(line))
		if !strings.Contains(id, "/") {
			return nil, fmt.Errorf("OpenCode model identifier invalid")
		}
		var entry struct {
			ID       string                     `json:"id"`
			Provider string                     `json:"providerID"`
			Name     string                     `json:"name"`
			Variants map[string]json.RawMessage `json:"variants"`
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		if err := decoder.Decode(&entry); err != nil {
			return nil, fmt.Errorf("OpenCode model metadata invalid: %w", err)
		}
		if entry.Provider+"/"+entry.ID != id {
			return nil, fmt.Errorf("OpenCode metadata model mismatch")
		}
		m := ports.AgentModelInfo{ID: id, Label: entry.Name, Provider: entry.Provider}
		for effort := range entry.Variants {
			m.Efforts = append(m.Efforts, effort)
		}
		sort.Strings(m.Efforts)
		models = append(models, m)
		rest = bytes.TrimSpace(body[decoder.InputOffset():])
	}
	return models, nil
}
