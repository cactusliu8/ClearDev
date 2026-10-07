package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClearDevExecutionChoiceIsExplicitAndLegacySafe(t *testing.T) {
	legacy := ProjectConfig{Worker: RoleOverride{Harness: HarnessOpenCode}, Orchestrator: RoleOverride{Harness: HarnessOpenCode}}
	if legacy.ClearDevHarness() != HarnessCodex || !legacy.SameClearDevExecution(ProjectConfig{}) {
		t.Fatal("ordinary AO role overrides changed historical ClearDev execution")
	}
	selected := ProjectConfig{ClearDev: &ClearDevExecutionConfig{Harness: HarnessOpenCode, Model: "user/model"}}
	if selected.Validate() != nil || selected.ClearDevHarness() != HarnessOpenCode || selected.SameClearDevExecution(legacy) {
		t.Fatal("explicit OpenCode choice was ignored")
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	var restored ProjectConfig
	if err := json.Unmarshal(data, &restored); err != nil || !selected.SameClearDevExecution(restored) {
		t.Fatalf("choice does not survive config serialization: %s %v", data, err)
	}
	for _, invalid := range []ClearDevExecutionConfig{
		{Harness: HarnessOpenCode},
		{Harness: HarnessOpenCode, Model: "model"},
		{Harness: HarnessOpenCode, Model: "/model"},
		{Harness: HarnessOpenCode, Model: "provider/"},
		{Harness: HarnessOpenCode, Model: "provider/model\n"},
		{Harness: HarnessOpenCode, Model: "provider/model", Effort: "bad value"},
		{Harness: HarnessCodex, Model: "model", Effort: "bad value"},
		{Harness: HarnessClaudeCode, Model: "model"},
	} {
		if invalid.Validate() == nil {
			t.Fatalf("accepted unsupported project choice: %+v", invalid)
		}
	}
}

func TestControlledOpenCodeEffortPersistsUserSelection(t *testing.T) {
	c := ClearDevExecutionConfig{Harness: HarnessOpenCode, Model: "provider/model"}
	c.Effort = "max"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	var restored ClearDevExecutionConfig
	if err := json.Unmarshal(raw, &restored); err != nil || restored.Effort != "max" {
		t.Fatal("max was lost", err)
	}
}

func TestNativeModelEffortIdentifiersAreNotAStaticCodexWhitelist(t *testing.T) {
	for _, harness := range []AgentHarness{HarnessCodex, HarnessOpenCode} {
		for _, effort := range []string{"", "max", "ultra", "future-native-variant"} {
			choice := ClearDevExecutionConfig{Harness: harness, Model: "provider/model", Effort: effort}
			if err := choice.Validate(); err != nil {
				t.Fatalf("native option %s/%s rejected: %v", harness, effort, err)
			}
			raw, err := json.Marshal(choice)
			if err != nil {
				t.Fatal(err)
			}
			var restored ClearDevExecutionConfig
			if err := json.Unmarshal(raw, &restored); err != nil || restored != choice {
				t.Fatalf("native choice lost: %+v %v", restored, err)
			}
		}
		for _, effort := range []string{" bad", "bad ", "bad value", "bad\nvalue", "bad\x00value", strings.Repeat("x", 65)} {
			choice := ClearDevExecutionConfig{Harness: harness, Model: "provider/model", Effort: effort}
			if choice.Validate() == nil {
				t.Fatalf("malformed native effort accepted for %s", harness)
			}
		}
	}
}
