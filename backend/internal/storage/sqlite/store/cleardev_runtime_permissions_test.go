package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

var errRuntimePolicyObserved = errors.New("test observed native launch without starting a provider")

// Real Chat and SQLite supply the identity and runtime policy. This driver only
// records the final native configuration then refuses to start any process.
type runtimePolicyDriver struct {
	ports.ChatDriver
	starts  []ports.ChatStartConfig
	resumes []ports.ChatResumeConfig
}

func (*runtimePolicyDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	return ports.ChatCapabilities{ports.ChatCapabilityStreaming: true, ports.ChatCapabilityApprovals: true, ports.ChatCapabilityInterrupt: true, ports.ChatCapabilityResume: true}, nil
}
func (d *runtimePolicyDriver) Start(_ context.Context, c ports.ChatStartConfig) (ports.ChatConversation, error) {
	d.starts = append(d.starts, c)
	return nil, errRuntimePolicyObserved
}
func (d *runtimePolicyDriver) Resume(_ context.Context, c ports.ChatResumeConfig) (ports.ChatConversation, error) {
	d.resumes = append(d.resumes, c)
	return nil, errRuntimePolicyObserved
}

type runtimePolicyRegistry struct{ driver *runtimePolicyDriver }

func (r runtimePolicyRegistry) Driver(domain.AgentHarness) (ports.ChatDriver, error) {
	return r.driver, nil
}
func (runtimePolicyRegistry) SupportsChat(domain.AgentHarness) bool { return true }

func TestControlledRuntimeUsesSQLiteRoleIdentityWithoutChangingIt(t *testing.T) {
	ctx := context.Background()
	s, state := seedComplexExecutionFlow(t)
	ordinary, err := s.CreateSession(ctx, domain.SessionRecord{
		ProjectID: domain.ProjectID(state.projectID), Kind: domain.KindWorker,
		Harness: domain.HarnessOpenCode, Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto,
		CreationIdempotencyKey: "cleardev-planner:unbound-lookalike", CreationRequestFingerprint: "ordinary-fingerprint",
		CreatedAt: state.now, UpdatedAt: state.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{state.prep.StewardSessionID, state.prep.PlannerSessionID, state.builder.AOSessionID, string(ordinary.ID)} {
		t.Run(id, func(t *testing.T) {
			before, found, err := s.GetSession(ctx, domain.SessionID(id))
			if err != nil || !found {
				t.Fatal("missing real role session", found, err)
			}
			driver := &runtimePolicyDriver{}
			var sequence atomic.Int64
			svc := chatsvc.New(chatsvc.Options{Store: s, Sessions: s, Drivers: runtimePolicyRegistry{driver},
				NewID: func() string { return fmt.Sprintf("policy-%s-%d", id, sequence.Add(1)) },
			})
			cfg := chatsvc.StartConfig{SessionID: before.ID, ProjectID: before.ProjectID, Kind: before.Kind,
				Harness: before.Harness, Permissions: before.PermissionMode, Model: before.Metadata.Model,
				CreationIdempotencyKey: before.CreationIdempotencyKey, WorkspacePath: before.Metadata.WorkspacePath,
			}
			if _, err := svc.Start(ctx, cfg); !errors.Is(err, errRuntimePolicyObserved) {
				t.Fatal("real durable role failed before native policy observation", err)
			}
			cfg.ProviderConversationID = "original-native"
			if _, err := svc.Start(ctx, cfg); !errors.Is(err, errRuntimePolicyObserved) {
				t.Fatal("real role restore changed admission", err)
			}
			want := ports.PermissionModeBypassPermissions
			if before.ID == ordinary.ID {
				want = ports.PermissionModeAuto
			}
			if len(driver.starts) != 1 || len(driver.resumes) != 1 || driver.starts[0].Permissions != want || driver.resumes[0].Permissions != want || driver.resumes[0].ProviderConversationID != "original-native" {
				t.Fatal("SQLite membership was ignored or inferred from key prefix", driver.starts, driver.resumes)
			}
			after, found, err := s.GetSession(ctx, before.ID)
			if err != nil || !found || !reflect.DeepEqual(before, after) {
				t.Fatal("policy observation rewrote persistent role identity", found, err)
			}
		})
	}
}
