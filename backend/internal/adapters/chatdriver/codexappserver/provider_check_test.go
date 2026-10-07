package codexappserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProviderReadOnlySequence(t *testing.T) {
	account := `{"id":2,"result":{"account":{"type":"chatgpt","email":"private@example.invalid"},"requiresOpenaiAuth":true}}`
	model := `{"id":3,"result":{"data":[{"id":"gpt-5.6-terra","supportedReasoningEfforts":[{"reasoningEffort":"high"}]}],"nextCursor":null}}`
	quota := `{"id":4,"result":{"rateLimits":{"primary":{"usedPercent":10},"secondary":null}}}`
	init := `{"id":1,"result":{}}`
	for _, tc := range []struct {
		name, input, outcome string
		sent                 int
	}{
		{"normal", init + "\n" + account + "\n" + model + "\n" + quota, "READ_SUCCESS", 5},
		{"logged out", init + "\n" + `{"id":2,"result":{"account":null}}`, "UNAVAILABLE", 3},
		{"RPC rejected", `{"id":1,"error":{"code":-32601,"message":"private"}}`, "UNAVAILABLE", 1},
		{"unknown quota", init + "\n" + account + "\n" + model + "\n" + `{"id":4,"result":{}}`, "UNAVAILABLE", 5},
		{"server request", `{"id":8,"method":"thread/start","params":{}}`, "INCOMPLETE", 1},
		{"wrong id", `{"id":99,"result":{}}`, "INCOMPLETE", 1},
		{"disconnect", init + "\n" + account, "INCOMPLETE", 4},
		{"oversized", strings.Repeat("x", (1<<20)+1), "INCOMPLETE", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			e, err := CheckProvider(context.Background(), strings.NewReader(tc.input+"\n"), &out)
			if e.Outcome != tc.outcome || e.Complete != (tc.outcome != "INCOMPLETE") {
				t.Fatalf("evidence %#v, err %v", e, err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != tc.sent {
				t.Fatalf("sent %s", out.String())
			}
			for _, line := range lines {
				var f struct {
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if json.Unmarshal([]byte(line), &f) != nil {
					t.Fatal("bad frame")
				}
				switch f.Method {
				case "initialize", "initialized", "model/list", "account/rateLimits/read":
				case "account/read":
					if f.Params["refreshToken"] != false {
						t.Fatal("refresh enabled")
					}
				default:
					t.Fatalf("unexpected method %s", f.Method)
				}
			}
		})
	}
}

func TestProviderPaginationIsBoundedAndPreservesCursor(t *testing.T) {
	input := `{"id":1,"result":{}}
{"id":2,"result":{"account":{"type":"apiKey"}}}
{"id":3,"result":{"data":[],"nextCursor":"first"}}
{"id":4,"result":{"data":[],"nextCursor":"second"}}
{"id":5,"result":{"data":[],"nextCursor":"third"}}
{"id":6,"result":{"rateLimits":{"primary":{"usedPercent":10}}}}
`
	var out bytes.Buffer
	e, err := CheckProvider(context.Background(), strings.NewReader(input), &out)
	if err != nil || !e.Complete || e.CatalogComplete || e.Pages != 3 || e.Outcome != "UNAVAILABLE" {
		t.Fatalf("%#v %v", e, err)
	}
	if strings.Count(out.String(), `"method":"model/list"`) != 3 || !strings.Contains(out.String(), `"cursor":"first"`) || !strings.Contains(out.String(), `"cursor":"second"`) || strings.Contains(out.String(), `"cursor":"third"`) {
		t.Fatal(out.String())
	}
}

func TestProviderCancelledBeforeSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	e, err := CheckProvider(ctx, strings.NewReader(""), &out)
	if err == nil || e.Complete || out.Len() != 0 {
		t.Fatal("cancelled request sent")
	}
}
