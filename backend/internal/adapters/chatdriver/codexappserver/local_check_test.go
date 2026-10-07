package codexappserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestLocalHandshakeBoundedProtocol(t *testing.T) {
	cases := []struct {
		name, input string
		ok          bool
		sent        int
	}{
		{"normal", `{"method":"notice"}` + "\n" + `{"id":1,"result":{"userAgent":"local"}}` + "\n", true, 2},
		{"wrong ID", `{"id":2,"result":{}}` + "\n", false, 1},
		{"error", `{"id":1,"error":{"code":-1,"message":"failed"}}` + "\n", false, 1},
		{"empty result", `{"id":1}` + "\n", false, 1},
		{"null", `{"id":1,"result":null}` + "\n", false, 1},
		{"approval", `{"id":7,"method":"requestApproval"}` + "\n", false, 1},
		{"malformed", "not json\n", false, 1},
		{"EOF", "", false, 1},
		{"oversize", strings.Repeat("x", (1<<20)+1) + "\n", false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			evidence, err := CheckLocalHandshake(context.Background(), strings.NewReader(tc.input), &out)
			if (err == nil) != tc.ok {
				t.Fatalf("error=%v", err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != tc.sent {
				t.Fatalf("actual sent=%q", out.String())
			}
			for i, line := range lines {
				var f frame
				if err := json.Unmarshal([]byte(line), &f); err != nil {
					t.Fatal(err)
				}
				if i == 0 && f.Method != "initialize" || i == 1 && f.Method != "initialized" {
					t.Fatalf("unexpected send %s", line)
				}
			}
			if tc.ok && (evidence.Initialize != 1 || evidence.Initialized != 1 || evidence.ResponseAt == 0) {
				t.Fatalf("bad evidence %+v", evidence)
			}
		})
	}
}
func TestLocalHandshakeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	_, err := CheckLocalHandshake(ctx, strings.NewReader(""), &out)
	if err == nil {
		t.Fatal("cancelled handshake passed")
	}
}
