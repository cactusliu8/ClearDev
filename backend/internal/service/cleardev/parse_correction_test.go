package cleardev

import (
	"errors"
	"strings"
	"testing"
)

func TestMechanicalParseCorrectionPromptContainsParserError(t *testing.T) {
	prompt := mechanicalParseCorrectionPrompt(errors.New("need 1-20 items, got 25"))
	if !strings.HasPrefix(prompt, parseCorrectionPromptPrefix) {
		t.Fatalf("prompt prefix = %q", prompt)
	}
	if !strings.Contains(prompt, "need 1-20 items, got 25") {
		t.Fatalf("prompt omitted parser error: %q", prompt)
	}
	if !strings.Contains(prompt, "Return exactly one complete valid JSON object") {
		t.Fatalf("prompt omitted rewrite instruction: %q", prompt)
	}
}

func TestAgentObservationReasonDoesNotTreatParseErrorsAsTimeout(t *testing.T) {
	if got := agentObservationReason(errors.New("need 1-20 items, got 25"), "TIMEOUT", "UNAVAILABLE"); got != "UNAVAILABLE" {
		t.Fatalf("parse error observation reason = %s", got)
	}
	if got := agentObservationReason(errComplexAgentStepTimeout, "TIMEOUT", "UNAVAILABLE"); got != "TIMEOUT" {
		t.Fatalf("step timeout observation reason = %s", got)
	}
}
