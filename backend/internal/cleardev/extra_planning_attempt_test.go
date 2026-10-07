package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtraPlanningAttemptPrivateBindingAndMessageIdentity(t *testing.T) {
	b := ExtraPlanningAttemptBinding{DevelopmentRequirementID: "requirement", SecondAttemptID: "step:attempt:2", FailureEventID: "second-failure", MessagesSHA256: strings.Repeat("a", 64), Source: PlanningStepRecoveryBinding{RequirementID: "requirement", ProductID: "product", DiscussionID: "discussion", LogicalStepID: "step", StepRequestID: "request", FirstAttemptID: "step:attempt:1", FailureEventID: "first-failure", RoleBindingID: "role", AOSessionID: "session", ProviderConversationID: "native", WorkspacePath: "/test/workspace", SessionCreationKey: "creation", Harness: "fake", Model: "model", PromptSHA256: strings.Repeat("b", 64), ClientMessageID: "original"}}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := ProductionDecisionRegistry().Lookup(HumanDecisionKindExtraPlanningAttempt)
	if !ok {
		t.Fatal("new private decision unreachable")
	}
	if _, err := spec.ParseBinding(raw); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{strings.Replace(string(raw), `"source":{`, `"source":{"extraAttempts":100,`, 1), strings.Replace(string(raw), `"developmentRequirementId":"requirement"`, `"developmentRequirementId":"other"`, 1), strings.Replace(string(raw), `"messagesSha256":"`+b.MessagesSHA256+`"`, `"messagesSha256":"bad"`, 1)} {
		if _, err := ParseExtraPlanningAttemptBinding([]byte(changed)); err == nil {
			t.Fatal("changed binding accepted")
		}
	}
	target, _ := ExtraPlanningAttemptTarget(b)
	b.FailureEventID = "new-failure"
	other, _ := ExtraPlanningAttemptTarget(b)
	if target == other {
		t.Fatal("failure change kept public target")
	}
	a := AgentStepAttempt{AttemptNumber: 3, ClientMessageID: "original:attempt:3", PromptSHA256: strings.Repeat("b", 64)}
	if !ValidAgentMessageIdentity(a, AgentMessageHumanAuthorizedOriginal, a.ClientMessageID, a.PromptSHA256) {
		t.Fatal("bound third original rejected")
	}
	for _, source := range []AgentMessageSource{AgentMessageOriginal, AgentMessageRecoveryOriginal, AgentMessageParseCorrection} {
		if ValidAgentMessageIdentity(a, source, a.ClientMessageID+":parse-correction", a.PromptSHA256) {
			t.Fatal("third acquired another message source")
		}
	}
}
