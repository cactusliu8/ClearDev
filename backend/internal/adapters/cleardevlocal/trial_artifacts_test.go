package cleardevlocal

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestTrialArtifactReaderPinsParentDirectoryDuringReplacement(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	root := t.TempDir()
	for _, dir := range []string{"output", "other"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(root, "output/report.txt"), "original")
	writeTestFile(t, filepath.Join(root, "other/report.txt"), "redirected")
	encoded, _ := json.Marshal(root)
	// Replace the parent exactly after it is opened. The next file open must
	// continue through the pinned descriptor, never the replaced pathname.
	hook := `const fs0=require('node:fs'),open0=fs0.openSync;let swapped=false;fs0.openSync=function(p,flags){const fd=open0(p,flags);if(!swapped&&String(p).endsWith('/output')){swapped=true;fs0.renameSync(` + string(encoded) + `+'/output',` + string(encoded) + `+'/saved');fs0.symlinkSync(` + string(encoded) + `+'/other',` + string(encoded) + `+'/output');}return fd;};`
	reader := strings.ReplaceAll(trialArtifactReader, "'/workspace'", string(encoded))
	out, err := exec.Command("node", "-e", hook+reader, `["output/report.txt"]`).CombinedOutput()
	if err != nil {
		t.Fatalf("reader: %s %v", out, err)
	}
	var artifacts []core.TrialArtifact
	if err := json.Unmarshal(out, &artifacts); err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts: %s %v", out, err)
	}
	bytes, err := base64.StdEncoding.DecodeString(artifacts[0].Base64)
	if err != nil || string(bytes) != "original" {
		t.Fatalf("read replaced parent: %q %v", bytes, err)
	}
	if out, err := exec.Command("node", "-e", reader, `["output/report.txt"]`).CombinedOutput(); err == nil {
		t.Fatalf("existing directory symlink accepted: %s", out)
	}
}
