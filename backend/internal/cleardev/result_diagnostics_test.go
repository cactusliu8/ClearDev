package cleardev

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func diagnosticBuilderJSON(t *testing.T, summary string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"schemaVersion": StandardProtocolVersion, "kind": "BUILDER_RESULT", "outcome": "CANDIDATE_READY", "summary": summary})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func requireDiagnosticText(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("invalid result was accepted")
	}
	for _, part := range parts {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("diagnostic %q does not identify %q", err, part)
		}
	}
}

func TestAgentResultDiagnosticsSeparateEnvelopeFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
		want []string
	}{
		{"empty", nil, []string{"RESULT_EMPTY", "0 bytes"}},
		{"encoding", []byte{0xff}, []string{"RESULT_ENCODING_INVALID", "UTF-8"}},
		{"oversize", bytes.Repeat([]byte("x"), 32769), []string{"RESULT_TOO_LARGE", "32769", "32768", "bytes"}},
		{"syntax", []byte(`{"summary":!}`), []string{"RESULT_JSON_INVALID", "byte"}},
		{"truncated", []byte(`{"summary":"unfinished`), []string{"RESULT_JSON_INCOMPLETE", "byte"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseBuilderResult(tc.raw, "dispatch", "task", 0)
			requireDiagnosticText(t, err, tc.want...)
		})
	}
}

func TestBuilderResultDiagnosticsKeepRuneAndByteLimitsSeparate(t *testing.T) {
	for _, text := range []string{strings.Repeat("中", 10000), strings.Repeat("a", 10000)} {
		raw := diagnosticBuilderJSON(t, text)
		got, err := ParseBuilderResult(raw, "dispatch", "task", 2)
		if err != nil || got.Summary != text || got.DispatchID != "dispatch" || got.Round != 2 {
			t.Fatalf("legal boundary changed: %+v %v", got, err)
		}
	}
	_, err := ParseBuilderResult(diagnosticBuilderJSON(t, strings.Repeat("中", 10001)), "dispatch", "task", 0)
	requireDiagnosticText(t, err, "summary", "10001", "10000", "Unicode characters")
	_, err = ParseBuilderResult(diagnosticBuilderJSON(t, "  "), "dispatch", "task", 0)
	requireDiagnosticText(t, err, "summary", "RESULT_TEXT_EMPTY")
	_, err = ParseBuilderResult(diagnosticBuilderJSON(t, strings.Repeat("😀", 9000)), "dispatch", "task", 0)
	requireDiagnosticText(t, err, "RESULT_TOO_LARGE", "32768", "bytes")
}

func TestBuilderResultDiagnosticsIdentifyWrongEnvelopeField(t *testing.T) {
	valid := diagnosticBuilderJSON(t, "done")
	for _, tc := range []struct{ old, replacement, field string }{
		{`"schemaVersion":1`, `"schemaVersion":99`, "schemaVersion"},
		{`"kind":"BUILDER_RESULT"`, `"kind":"WRONG"`, "kind"},
		{`"outcome":"CANDIDATE_READY"`, `"outcome":"PASS"`, "outcome"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			raw := bytes.Replace(valid, []byte(tc.old), []byte(tc.replacement), 1)
			if bytes.Equal(raw, valid) {
				t.Fatal("fixture did not change the field")
			}
			_, err := ParseBuilderResult(raw, "dispatch", "task", 0)
			requireDiagnosticText(t, err, tc.field)
			if strings.Contains(err.Error(), "active execution round") {
				t.Fatal("result content error was misreported as a binding mismatch")
			}
		})
	}
}

func TestProductResultDiagnosticsIdentifyTextAndTotalLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ProductDiscoveryResult)
		want   []string
	}{
		{"message", func(r *ProductDiscoveryResult) { r.Message = strings.Repeat("中", 10001) }, []string{"message", "10001", "10000"}},
		{"stage-title", func(r *ProductDiscoveryResult) { r.Stages[0].Title = strings.Repeat("中", 201) }, []string{"stages[0].title", "201", "200"}},
		{"acceptance", func(r *ProductDiscoveryResult) { r.Stages[0].AcceptanceCriteria[0] = strings.Repeat("a", 10001) }, []string{"stages[0].acceptanceCriteria[0]", "10001", "10000"}},
		{"combined", func(r *ProductDiscoveryResult) {
			r.Message, r.FeasibilitySummary = strings.Repeat("中", 6000), strings.Repeat("中", 6000)
		}, []string{"RESULT_TOO_LARGE", "32768", "bytes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := productDiscoveryFixture()
			tc.mutate(&r)
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseProductDiscoveryResult(raw)
			requireDiagnosticText(t, err, tc.want...)
		})
	}
}

func TestCompilationResultDiagnosticsIdentifyNestedText(t *testing.T) {
	for _, tc := range []struct {
		field string
		patch map[string]any
	}{
		{"summary", map[string]any{"summary": strings.Repeat("中", 10001)}},
		{"requirements[0].text", map[string]any{"requirements": []any{map[string]any{"key": "normalize-email", "priority": "MUST", "text": strings.Repeat("a", 10001), "acceptanceKeys": []string{"normalize-scenario"}}}}},
		{"constraints[0]", map[string]any{"constraints": []string{strings.Repeat("a", 10001)}}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			raw := compilationJSON(t, tc.patch)
			if len(raw) > 32768 {
				t.Fatalf("field fixture accidentally exceeds envelope limit: %d", len(raw))
			}
			_, err := ParseRequirementCompilationResult(raw, "req-1", "ver-1", strings.Repeat("a", 64), 0, false)
			requireDiagnosticText(t, err, tc.field, "10001", "10000")
		})
	}
}

func TestAgentResultDiagnosticsReportExactRawJSONOffset(t *testing.T) {
	for _, raw := range []string{
		strings.Repeat(" ", 100) + `{"summary":!}`,
		"\n\t " + `{"nested":{"value":!}}`,
		`{"中文":"已经写好","nested":[0, {"value":!}]}`,
	} {
		_, err := ParseBuilderResult([]byte(raw), "dispatch", "task", 0)
		var diagnostic *AgentResultValidationError
		if !errors.As(err, &diagnostic) {
			t.Fatalf("missing structured syntax diagnosis: %v", err)
		}
		want := int64(strings.Index(raw, "!") + 1)
		if diagnostic.Offset != want || diagnostic.Code != "RESULT_JSON_INVALID" {
			t.Fatalf("syntax offset = %d, want raw byte %d: %v", diagnostic.Offset, want, err)
		}
	}
}

func TestProductResultDiagnosticsKeepCompleteNestedPaths(t *testing.T) {
	for _, tc := range []struct {
		field  string
		mutate func(*ProductDiscoveryResult)
	}{
		{"stages[0].executionBasis.writePaths[0]", func(r *ProductDiscoveryResult) { r.Stages[0].ExecutionBasis.WritePaths[0] = strings.Repeat("a", 10001) }},
		{"stages[0].executionBasis.checks[0].mainPaths[0]", func(r *ProductDiscoveryResult) {
			r.Stages[0].ExecutionBasis.Checks[0].MainPaths[0] = strings.Repeat("a", 10001)
		}},
		{"stages[0].executionBasis.trial.steps[0].observe", func(r *ProductDiscoveryResult) {
			r.Stages[0].ExecutionBasis.Trial.Steps[0].Observe = strings.Repeat("a", 10001)
		}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			fixture, marshalErr := json.Marshal(projectProposalFixture(t))
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var r ProductDiscoveryResult
			if err := json.Unmarshal(fixture, &r); err != nil {
				t.Fatal(err)
			}
			r.Evidence = []ProductEvidence{{Status: "OBSERVED", Claim: "Diagnostic fixture", Source: "test"}}
			r.Stages[0].ExecutionBasis.Trial = &ProjectTrial{SchemaVersion: 1, Service: true, Steps: []ProjectTrialStep{{ID: "verify", Kind: "HTTP", Observe: "observe", AcceptanceCriteria: append([]string(nil), r.Stages[0].AcceptanceCriteria...)}}}
			valid, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseProductDiscoveryResult(valid); err != nil {
				t.Fatalf("unmodified fixture must be valid: %v", err)
			}
			tc.mutate(&r)
			raw, err := json.Marshal(r)
			if err != nil || len(raw) > 32768 {
				t.Fatalf("invalid diagnostic fixture: bytes=%d, err=%v", len(raw), err)
			}
			_, err = ParseProductDiscoveryResult(raw)
			requireDiagnosticText(t, err, tc.field, "10001", "10000")
			if strings.Contains(tc.field, "trial.steps") && !strings.Contains(err.Error(), "STAGE_TRIAL_UNSUPPORTED") {
				t.Fatalf("trial policy code was lost: %v", err)
			}
		})
	}
}

func TestAgentResultDiagnosticsKeepExactEnvelopeBoundary(t *testing.T) {
	raw := diagnosticBuilderJSON(t, "valid")
	for _, extra := range []int{0, 1} {
		padded := append(bytes.Clone(raw), bytes.Repeat([]byte(" "), 32768-len(raw)+extra)...)
		t.Run(fmt.Sprintf("bytes=%d", len(padded)), func(t *testing.T) {
			_, err := ParseBuilderResult(padded, "dispatch", "task", 0)
			if extra == 0 && err != nil {
				t.Fatalf("exact byte boundary rejected: %v", err)
			}
			if extra == 1 {
				requireDiagnosticText(t, err, "32769", "32768")
			}
		})
	}
}
