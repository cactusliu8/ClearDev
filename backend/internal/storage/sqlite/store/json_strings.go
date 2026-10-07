package store

import (
	"encoding/json"
	"fmt"
	"strings"
)

func decodeJSONStringSlice(raw, label string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("corrupt %s JSON: empty", label)
	}
	var out []string
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, fmt.Errorf("corrupt %s JSON: %w", label, err)
	}
	if out == nil {
		return nil, fmt.Errorf("corrupt %s JSON: expected array", label)
	}
	return out, nil
}
