package opencodeacp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type inspectionPlugin struct{ err error }

func (p inspectionPlugin) ResolveBinary(context.Context) (string, error) {
	return "/user/opencode", p.err
}
func (p inspectionPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusAuthorized, nil
}

func TestOpenCodePreflightPreservesProjectContextAndUnknownRemoteFacts(t *testing.T) {
	cfg := ports.ChatPreflightContext{Harness: domain.HarnessOpenCode, RequestedModel: "local/model", WorkspacePath: "/project/source", Env: map[string]string{"OPENCODE_CONFIG": "/user/config.json"}}
	var commands []string
	d := &Driver{plugin: inspectionPlugin{}, run: func(_ context.Context, binary string, got ports.ChatPreflightContext, args ...string) ([]byte, error) {
		if binary != "/user/opencode" || !reflect.DeepEqual(got, cfg) {
			t.Fatalf("native context changed: %s %#v", binary, got)
		}
		command := strings.Join(args, " ")
		commands = append(commands, command)
		switch command {
		case "debug config":
			return []byte(`{"provider":{"local":{"options":{"apiKey":"private-test-value"}}}}`), nil
		case "models":
			return []byte("local/other\nlocal/model\n"), nil
		default:
			t.Fatalf("unexpected command, must not start a session or paid probe: %q", command)
			return nil, nil
		}
	}}
	got, err := d.InspectProjectAccount(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commands, []string{"debug config", "models"}) || len(got.Models) != 2 || got.Evidence.Model != "LISTED" || got.Evidence.Authentication != "CONFIGURED" || got.Evidence.Scope != "LOCAL_CONFIGURATION" || got.Evidence.Service != "UNKNOWN" || got.Evidence.Quota != "UNKNOWN" || got.RateLimits.QuotaExhausted || got.RateLimits.PrimaryUsedPercent != -1 {
		t.Fatalf("inaccurate inspection: %#v, %#v", got, got.Evidence)
	}
	for _, model := range got.Models {
		if model.Default {
			t.Fatal("an unset native default must not select the first provider")
		}
	}
}

func TestOpenCodePreflightFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, config, catalog string
		binaryErr, want       error
	}{
		{name: "not installed", binaryErr: errors.New("missing"), want: ports.ErrChatToolNotInstalled},
		{name: "invalid native config", config: "broken secret-bearing output", want: ports.ErrChatConfigurationInvalid},
		{name: "missing selected model", config: `{}`, catalog: "other/model\n", want: ports.ErrChatModelNotAvailable},
		{name: "empty native catalog", config: `{}`, catalog: "", want: ports.ErrChatModelNotAvailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Driver{plugin: inspectionPlugin{err: tc.binaryErr}, run: func(_ context.Context, _ string, _ ports.ChatPreflightContext, args ...string) ([]byte, error) {
				if args[0] == "debug" {
					return []byte(tc.config), nil
				}
				return []byte(tc.catalog), nil
			}}
			got, err := d.InspectProjectAccount(context.Background(), ports.ChatPreflightContext{RequestedModel: "local/model"})
			if !errors.Is(err, tc.want) || got.Evidence.Authentication != "UNKNOWN" || got.Evidence.Service != "UNKNOWN" || got.Evidence.Quota != "UNKNOWN" {
				t.Fatalf("result=%#v evidence=%#v error=%v", got, got.Evidence, err)
			}
			if strings.Contains(err.Error(), "secret-bearing") {
				t.Fatal("raw configuration leaked into an error")
			}
		})
	}
}
