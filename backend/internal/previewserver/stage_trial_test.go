package previewserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestStageTrialRunsFrozenProjectAndKeepsRestartDataSeparate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := New(nil, t.TempDir())
	t.Cleanup(m.Close)
	contract := projectDataTestContract(t)
	base, candidate := strings.Repeat("a", 40), strings.Repeat("b", 40)
	delivered, err := m.prepareProjectData(ctx, contract, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(delivered.directory, "note"), []byte("user note"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := delivered.publish(); err != nil {
		t.Fatal(err)
	}
	contract = nextProjectDataTestContract(t, contract, base, "trial")
	digest, _ := core.ProjectExecutionContractDigest(contract)
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "app"), 0700); err != nil {
		t.Fatal(err)
	}
	server := `const http=require('node:http'),fs=require('node:fs'),path=require('node:path');
const note=path.join(process.env.CLEARDEV_DATA_DIR,'note');
http.createServer((req,res)=>{if(req.method==='POST'){fs.writeFileSync(note,'trial note');res.end('saved');}else res.end(fs.readFileSync(note));}).listen(Number(process.env.PORT),process.env.HOST);`
	if err := os.WriteFile(filepath.Join(workspace, "app/server.js"), []byte(server), 0600); err != nil {
		t.Fatal(err)
	}
	projectHash := sha256.Sum256([]byte(contract.Selection.AOProjectID))
	pointerPath := filepath.Join(filepath.Dir(m.registryPath), "cleardev-project-data", hex.EncodeToString(projectHash[:]), "selected.json")
	pointer, _ := os.ReadFile(pointerPath)
	releases := 0
	prepare := func(context.Context) (ports.ClearDevProjectResultSource, error) {
		return ports.ClearDevProjectResultSource{WorkspacePath: workspace, CandidateSHA: candidate, ContractSHA256: digest,
			Environment: ports.ClearDevCheckEnvironment{CandidateSHA: candidate, ProjectExecutionSHA256: digest, SourceManifestID: "explicit-test-source"},
			Release:     func(context.Context) error { releases++; return nil }}, nil
	}
	started, err := m.StartStageTrial(ctx, "stage-reviewer", workspace, candidate, contract, prepare)
	if err != nil || started.State != StateReady {
		t.Fatalf("real trial start: %+v %v", started, err)
	}
	if _, err := m.StartProject(ctx, "other-delivery", workspace, candidate, contract, prepare); err == nil {
		t.Fatal("concurrent live data process accepted")
	}
	response, err := http.Post(started.URL, "text/plain", strings.NewReader("write"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if _, err := m.Stop(ctx, "stage-reviewer"); err != nil {
		t.Fatal(err)
	}
	started, err = m.StartStageTrial(ctx, "stage-reviewer", workspace, candidate, contract, prepare)
	if err != nil || started.State != StateReady {
		t.Fatalf("trial restart: %+v %v", started, err)
	}
	response, err = http.Get(started.URL)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(raw) != "trial note" {
		t.Fatalf("trial restart lost data: %s", raw)
	}
	if _, err := m.Stop(ctx, "stage-reviewer"); err != nil {
		t.Fatal(err)
	}
	if releases != 2 {
		t.Fatalf("prepared runtime release count %d", releases)
	}
	user, _ := os.ReadFile(filepath.Join(delivered.directory, "note"))
	after, _ := os.ReadFile(pointerPath)
	if string(user) != "user note" || string(pointer) != string(after) {
		t.Fatal("trial changed delivered data or its pointer")
	}
}

func TestStageTrialDataRejectsRedirectedCopyAndOwnerReuse(t *testing.T) {
	ctx := context.Background()
	m := New(nil, t.TempDir())
	t.Cleanup(m.Close)
	contract := projectDataTestContract(t)
	digest, _ := core.ProjectExecutionContractDigest(contract)
	candidate := strings.Repeat("b", 40)
	one, err := m.prepareStageTrialData(ctx, contract, candidate, digest, "review-one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.prepareStageTrialData(ctx, contract, candidate, digest, "review-two")
	if err != nil || two.directory == one.directory {
		t.Fatal("two reviewers share trial data")
	}
	if err := os.RemoveAll(one.directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), one.directory); err != nil {
		t.Fatal(err)
	}
	if _, err := m.prepareStageTrialData(ctx, contract, candidate, digest, "review-one"); err == nil {
		t.Fatal("redirected trial accepted")
	}
}
