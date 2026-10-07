package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestClearDevFixedRecoveryCLIReadsExactEvidence(t *testing.T) {
	cfg := setConfigEnv(t)
	response := `{"complexExecution":{"exception":{"recoveryActions":[{"id":"old-proposal","outcome":"PASS","executionResult":null}]},"fixedRecoveries":[{"request":{"id":"unknown","sessionId":"old"},"claim":{"proposalId":"proposal"},"result":null,"reviewResult":null},{"request":{"id":"rebuild","reviewId":"original-review","sessionId":"old"},"result":{"outcome":"PASS","sessionId":"new"},"reviewResult":{"originalReviewId":"original-review","attemptId":"attempt-2","resultId":"new-result"}}]}}`
	server, capture := clearDevServer(t, http.StatusOK, response)
	writeRunFileFor(t, cfg, server)
	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "requirement", "show", "req-1")
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(response), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("CLI altered recovery evidence")
	}
	method, _, _, count := capture.snapshot()
	if method != http.MethodGet || count != 1 {
		t.Fatal("CLI evidence read caused additional requests")
	}
}
