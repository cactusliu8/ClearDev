package codexappserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRoleContainerStartResumeAndTurnForceCommonPosture(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "resume"}[resume], func(t *testing.T) {
			original, srv := newTestDriver(t)
			launches := 0
			d := NewRoleContainer(original.plugin, original.log, func(ctx context.Context, workdir string) (*RoleContainerProcess, error) {
				launches++
				p, err := original.spawn(ctx, "", workdir, nil, []string{"app-server"})
				if err != nil {
					return nil, err
				}
				return &RoleContainerProcess{Stdin: p.stdin, Stdout: p.stdout, Stop: p.stop}, nil
			})
			var conv ports.ChatConversation
			var err error
			method := "thread/start"
			if resume {
				method = "thread/resume"
				conv, err = d.Resume(context.Background(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-1", Permissions: ports.PermissionModeAuto})
			} else {
				conv, err = d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", Permissions: ports.PermissionModeAuto, Env: map[string]string{"CLEARDEV_BENCHMARK_RUNTIME": "ignored"}})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conv.Close() }()
			assertRolePosture(t, srv.awaitFrame(func(f frame) bool { return f.Method == method }), false)
			_, err = conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "public input", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAuto, Effort: "low"}})
			if err != nil {
				t.Fatal(err)
			}
			assertRolePosture(t, srv.awaitFrame(func(f frame) bool { return f.Method == "turn/start" }), true)
			if launches != 1 {
				t.Fatalf("launches=%d", launches)
			}
		})
	}
}
func assertRolePosture(t *testing.T, f frame, turn bool) {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(f.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p["approvalPolicy"] != "never" || p["model"] != "gpt-5.6-terra" || p["approvalsReviewer"] != nil {
		t.Fatalf("unexpected posture: %v", p)
	}
	if turn {
		policy, ok := p["sandboxPolicy"].(map[string]any)
		if !ok || policy["type"] != "dangerFullAccess" || p["effort"] != "high" {
			t.Fatalf("unexpected turn: %v", p)
		}
	} else if p["sandbox"] != "danger-full-access" {
		t.Fatalf("unexpected thread: %v", p)
	}
}

func TestRoleContainerIncompleteTransportStillStopsLaunchedActivity(t *testing.T) {
	original, _ := newTestDriver(t)
	stops := 0
	driver := NewRoleContainer(original.plugin, original.log, func(context.Context, string) (*RoleContainerProcess, error) {
		return &RoleContainerProcess{Stop: func() error { stops++; return nil }}, nil
	})
	_, err := driver.spawn(context.Background(), "", "/unused", nil, []string{"app-server"})
	if err == nil || stops != 1 {
		t.Fatalf("incomplete launch: err=%v stops=%d", err, stops)
	}
}
