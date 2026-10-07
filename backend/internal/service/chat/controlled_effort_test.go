package chat_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type effortConversation struct {
	*fakeConversation
	value   string
	support bool
	ignore  bool
	sets    int
}

func (c *effortConversation) ListConfigOptions(context.Context) ([]ports.ChatConfigOption, error) {
	choices := []ports.ChatConfigOptionChoice{{Value: "low"}}
	if c.support {
		choices = append(choices, ports.ChatConfigOptionChoice{Value: "max"}, ports.ChatConfigOptionChoice{Value: "high"})
	}
	return []ports.ChatConfigOption{{ID: "model", Current: ports.ChatConfigOptionValue{Select: "provider/model"}}, {ID: "effort", Type: ports.ChatConfigOptionSelect, Current: ports.ChatConfigOptionValue{Select: c.value}, Choices: choices}}, nil
}
func (c *effortConversation) SetConfigOption(ctx context.Context, id string, v ports.ChatConfigOptionValue) ([]ports.ChatConfigOption, error) {
	c.sets++
	if !c.ignore {
		c.value = v.Select
	}
	return c.ListConfigOptions(ctx)
}
func TestControlledOpenCodeEffortBeforeEveryRelay(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		support, ignore, controlled bool
		wantError                   bool
	}{
		{"bind selected high", true, false, true, false}, {"unsupported", false, false, true, true}, {"readback mismatch", true, true, true, true}, {"ordinary unaffected", false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &effortConversation{fakeConversation: newFakeConversation(), value: "low", support: tc.support, ignore: tc.ignore}
			h := newHarnessWithConversation(t, c)
			ctx := context.Background()
			project, _, err := h.st.GetProject(ctx, string(testProject))
			if err != nil {
				t.Fatal(err)
			}
			project.Config.ClearDev = &domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "provider/model", Effort: "high"}
			if err = h.st.UpsertProject(ctx, project); err != nil {
				t.Fatal(err)
			}
			record, _, err := h.st.GetSession(ctx, testSession)
			if err != nil {
				t.Fatal(err)
			}
			record.Harness = domain.HarnessOpenCode
			record.Metadata.Model = "provider/model"
			if tc.controlled {
				record.CreationIdempotencyKey = "cleardev:effort"
			}
			if err = h.st.UpdateSession(ctx, record); err != nil {
				t.Fatal(err)
			}
			db, e := sql.Open("sqlite", "file:"+filepath.Join(project.Path, "ao.db"))
			if e != nil {
				t.Fatal(e)
			}
			_, e = db.Exec("UPDATE sessions SET harness='opencode', creation_idempotency_key=?, creation_request_fingerprint=? WHERE id=?", record.CreationIdempotencyKey, record.CreationIdempotencyKey, testSession)
			_ = db.Close()
			if e != nil {
				t.Fatal(e)
			}
			_, err = h.svc.RelayChatTurnWithID(ctx, testSession, "work", "effort-message")
			if (err != nil) != tc.wantError {
				t.Fatal(err)
			}
			if tc.wantError {
				if !errors.Is(err, ports.ErrChatSendNotStarted) || len(c.sentMessages()) != 0 {
					t.Fatal("failed effort reached provider", err)
				}
				snap, e := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
				if e != nil || len(snap.Messages) != 0 {
					t.Fatal("created message before effort", e)
				}
			} else if tc.controlled && (c.value != "high" || c.sets != 1) {
				t.Fatal("did not bind selected high", c.value, c.sets)
			}
		})
	}
}
