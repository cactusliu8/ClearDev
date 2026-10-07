package processdiagnostics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestACPDiagnosticsRedactsFragmentedSecretsAndPrivateKeys(t *testing.T) {
	d := newTestDiagnostics(t.TempDir(), "a/session", []string{"API_KEY=known-secret", "OPENCODE_CONFIG_CONTENT={\"provider\":{\"apiKey\":\"nested-secret\"}}"}, nil)
	for _, part := range []string{"trace known-", "secret nested-secret\n", `{"apiKey":"other-secret", "password":"two words"}`, "\nhttps://user:pass@host/path?token=query-secret\n", "Bearer unrelated-token\n", "-----BEGIN PRIVATE KEY-----\nbase64-private-material\n-----END PRIVATE KEY-----\n", "normal stack at file:42\n"} {
		if _, err := d.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	d.Finish(nil)
	b, err := os.ReadFile(filepath.Join(d.dir, "stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"known-secret", "nested-secret", "other-secret", "two words", "user:pass", "query-secret", "unrelated-token", "base64-private-material"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret persisted: %s", secret)
		}
	}
	if !strings.Contains(string(b), "normal stack at file:42") {
		t.Fatal("useful stack removed")
	}
}

func TestACPDiagnosticsSeparateRunsAndBoundCompletedRetention(t *testing.T) {
	root := t.TempDir()
	active := newTestDiagnostics(root, "same-session", nil, nil)
	t.Cleanup(func() { active.Finish(nil) })
	var latest *Capture
	for range diagnosticRetainedRuns + 3 {
		d := newTestDiagnostics(root, "same-session", nil, nil)
		if d.dir == active.dir {
			t.Fatal("capture overwritten")
		}
		d.Record("failure", "retained error")
		d.Finish(nil)
		latest = d
	}
	entries, err := os.ReadDir(filepath.Dir(active.dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != diagnosticRetainedRuns+1 {
		t.Fatalf("retention: got %d", len(entries))
	}
	if _, err := os.Stat(active.dir); err != nil {
		t.Fatal("active capture removed")
	}
	if _, err := os.Stat(latest.dir); err != nil {
		t.Fatal("latest failure removed")
	}
	other := newTestDiagnostics(root, "different-session", nil, nil)
	defer other.Finish(nil)
	if filepath.Dir(other.dir) == filepath.Dir(active.dir) {
		t.Fatal("sessions mixed")
	}
}

func TestACPDiagnosticsWriteFailureStillDrainsAndReportsMissingLog(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "diagnostics"), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	d := newTestDiagnostics(root, "session", nil, nil)
	input := []byte(strings.Repeat("noisy line\n", 10000))
	n, err := d.Write(input)
	if err != nil || n != len(input) {
		t.Fatal("failed log stopped drainage", n, err)
	}
	cause := errors.New("original failure")
	e := d.Failure("prompt", cause)
	if !errors.Is(e, cause) || !strings.Contains(e.Error(), "unavailable") {
		t.Fatal("missing failure disclosure", e)
	}
	d.Finish(cause)
}

func TestACPDiagnosticsConcurrentFailureAndStderr(t *testing.T) {
	d := newTestDiagnostics(t.TempDir(), "session", nil, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			_, _ = d.Write([]byte("stack line\n"))
		}
	}()
	for range 200 {
		if err := d.Failure("prompt", errors.New("test failure")); err == nil {
			t.Fatal("missing error")
		}
	}
	<-done
	d.Finish(nil)
}

func TestACPDiagnosticsMidstreamWriteFailureStillDrains(t *testing.T) {
	d := newTestDiagnostics(t.TempDir(), "session", nil, nil)
	if err := d.file.Close(); err != nil {
		t.Fatal(err)
	}
	input := []byte("later stderr\n")
	if n, err := d.Write(input); n != len(input) || err != nil {
		t.Fatal("drain stopped", n, err)
	}
	e := d.Failure("prompt", errors.New("original error"))
	if !strings.Contains(e.Error(), "capture incomplete") {
		t.Fatal("log failure hidden", e)
	}
	d.Finish(nil)
}

func newTestDiagnostics(root, session string, env []string, _ any) *Capture {
	return New(root, "acp", session, env, nil)
}

func TestProviderRetentionAndAgeLeaveActiveAndOtherProviderUntouched(t *testing.T) {
	root := t.TempDir()
	active := New(root, "acp", "active", nil, nil)
	defer active.Finish(nil)
	other := New(root, "codex", "other", nil, nil)
	other.Finish(nil)
	expired := New(root, "acp", "expired", nil, nil)
	expired.Finish(nil)
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(expired.Path(), "closed"), old, old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 130; i++ {
		d := New(root, "acp", fmt.Sprint(i), nil, nil)
		d.Finish(nil)
	}
	if _, err := os.Stat(expired.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired capture retained", err)
	}
	for _, path := range []string{active.Path(), other.Path()} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("unrelated/active capture removed", err)
		}
	}
	completed := 0
	err := filepath.WalkDir(filepath.Join(root, "diagnostics", "acp"), func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Name() == "closed" {
			completed++
		}
		return nil
	})
	if err != nil || completed != 128 {
		t.Fatal("provider retention not bounded", completed, err)
	}
}

func TestEmptyStderrIsExplicit(t *testing.T) {
	d := New(t.TempDir(), "codex", "empty", nil, nil)
	d.Finish(nil)
	b, err := os.ReadFile(filepath.Join(d.Path(), "stderr.log"))
	if err != nil || !strings.Contains(string(b), "stderr_bytes=0") {
		t.Fatal("empty stderr not disclosed", err)
	}
}
