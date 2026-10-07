package cleardev

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestBuilderSessionRecheckNormalizationIsNarrowAndStable(t *testing.T) {
	if got := NormalizeWorkflowRecoverySupplement(RecoveryRetryBuilderSession, " \n\t "); got != BuilderSessionRecheckContext || NormalizeWorkflowRecoverySupplement(RecoveryRetryBuilderSession, got) != got {
		t.Fatal("empty system recheck is not canonically replayable")
	}
	for _, action := range []string{RecoveryContinueBuilder, RecoveryRetryCheck, RecoveryRetryReview, RecoveryRetryStage, RecoveryRetryPlanningStep, "UNKNOWN"} {
		if NormalizeWorkflowRecoverySupplement(action, " ") != "" {
			t.Fatalf("recheck default broadened %s", action)
		}
	}
	if NormalizeWorkflowRecoverySupplement(RecoveryRetryBuilderSession, "  保留原工作，不确认环境已修复。  ") != "保留原工作，不确认环境已修复。" {
		t.Fatal("provided context was overwritten")
	}
}

func TestBuilderSessionRecheckBindingExcludesActivityNotIdentity(t *testing.T) {
	r := domain.SessionRecord{ID: "original", ProjectID: "project", Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, CreationIdempotencyKey: "role-creation", PermissionMode: domain.PermissionModeAuto,
		Metadata: domain.SessionMetadata{ProviderConversationID: "private-native-id", Model: "fixed-model", WorkspacePath: "/original/work"}}
	digest := BuilderSessionBindingDigest(r)
	if len(digest) != 64 {
		t.Fatal("missing binding digest")
	}
	r.Activity.State = domain.ActivityIdle
	if BuilderSessionBindingDigest(r) != digest {
		t.Fatal("transient activity incorrectly changed the frozen identity")
	}
	for _, modify := range []func(*domain.SessionRecord){
		func(r *domain.SessionRecord) { r.Metadata.ProviderConversationID = "different" },
		func(r *domain.SessionRecord) { r.Metadata.Model = "different" },
		func(r *domain.SessionRecord) { r.Metadata.WorkspacePath = "/different" },
		func(r *domain.SessionRecord) { r.CreationIdempotencyKey = "other-role" },
		func(r *domain.SessionRecord) { r.Harness = domain.HarnessOpenCode },
	} {
		changed := r
		modify(&changed)
		if BuilderSessionBindingDigest(changed) == digest {
			t.Fatal("different original identity retained a readiness fingerprint")
		}
	}
	raw, err := json.Marshal(BuilderSessionCheck{RecoveryID: "request", BindingSHA256: digest})
	if err != nil || strings.Contains(string(raw), digest) || strings.Contains(string(raw), "private-native-id") {
		t.Fatal("public checkpoints expose internal identity")
	}
}
