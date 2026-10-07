package cli

import (
	"io"
	"strings"
	"testing"
)

func TestDirectionChangeCLIRequiresFileAndID(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, 202, `{}`)
	writeRunFileFor(t, cfg, srv)
	err := executeWithDeps(Deps{Out: io.Discard, Err: io.Discard, ProcessAlive: func(int) bool { return true }}, []string{"cleardev", "requirement", "propose-direction", "req-1"})
	if err == nil || !strings.Contains(err.Error(), "--file is required") {
		t.Fatalf("missing file error = %v", err)
	}
	_, _, _, count := capture.snapshot()
	if count != 0 {
		t.Fatalf("usage error called the API %d times", count)
	}
}
