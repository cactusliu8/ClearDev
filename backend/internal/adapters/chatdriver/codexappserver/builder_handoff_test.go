package codexappserver

import (
	"context"
	"io"
	"runtime"
	"testing"
	"time"
)

func TestBuilderHandoffProcessStoppedRequiresActualWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local Unix process fixture")
	}
	proc, err := spawnAppServer(context.Background(), "/bin/sh", t.TempDir(), nil, []string{"-c", "exec 1>&-; read value"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.stop() })
	if _, err := io.ReadAll(proc.stdout); err != nil {
		t.Fatal(err)
	}
	conv := &conversation{proc: proc}
	if conv.ProcessStopped() {
		t.Fatal("closed transport was mistaken for stopped provider")
	}
	if err := proc.stop(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !conv.ProcessStopped() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !conv.ProcessStopped() {
		t.Fatal("exact OS wait completion was not observed")
	}
	if (&conversation{proc: &process{}}).ProcessStopped() {
		t.Fatal("missing process observation became positive proof")
	}
}
