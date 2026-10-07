package store_test

import (
	"context"
	"testing"
)

func TestClearDevControlledSessionLookupUsesPersistedBindings(t *testing.T) {
	store, state := seedComplexExecutionFlow(t)
	for _, sessionID := range []string{state.prep.StewardSessionID, state.prep.PlannerSessionID, state.builder.AOSessionID} {
		if sessionID == "" {
			t.Fatal("fixture has no bound session")
		}
		controlled, err := store.IsClearDevControlledSession(context.Background(), sessionID)
		if err != nil || !controlled {
			t.Fatalf("bound session %q: controlled=%t err=%v", sessionID, controlled, err)
		}
	}
	controlled, err := store.IsClearDevControlledSession(context.Background(), "ordinary-ao-session")
	if err != nil || controlled {
		t.Fatalf("ordinary session: controlled=%t err=%v", controlled, err)
	}
}
