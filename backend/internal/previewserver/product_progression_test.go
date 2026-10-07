package previewserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestProductPlanPreparesDataWithoutStoppingUserPreview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := New(nil, t.TempDir())
	t.Cleanup(m.Close)
	original := projectDataTestContract(t)
	base, candidate := strings.Repeat("a", 40), strings.Repeat("b", 40)
	data, e := m.prepareProjectData(ctx, original, base)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(data.directory, "note"), []byte("retained user note"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = data.publish(); e != nil {
		t.Fatal(e)
	}
	contract := nextProjectDataTestContract(t, original, base, "next")
	digest, _ := core.ProjectExecutionContractDigest(contract)
	workspace := t.TempDir()
	if e = os.Mkdir(filepath.Join(workspace, "app"), 0700); e != nil {
		t.Fatal(e)
	}
	server := `const http=require('node:http'),fs=require('node:fs'),path=require('node:path');const p=path.join(process.env.CLEARDEV_DATA_DIR,'note');if(fs.readFileSync(p,'utf8')!=='retained user note')process.exit(2);http.createServer((req,res)=>res.end('ready')).listen(Number(process.env.PORT),process.env.HOST);`
	if e = os.WriteFile(filepath.Join(workspace, "app/server.js"), []byte(server), 0600); e != nil {
		t.Fatal(e)
	}
	calls, releases := 0, 0
	prepare := func(context.Context) (ports.ClearDevProjectResultSource, error) {
		calls++
		return ports.ClearDevProjectResultSource{WorkspacePath: workspace, CandidateSHA: candidate, ContractSHA256: digest, Environment: ports.ClearDevCheckEnvironment{CandidateSHA: candidate, ProjectExecutionSHA256: digest, SourceManifestID: "local-test"}, Release: func(context.Context) error { releases++; return nil }}, nil
	}
	// A running user process is retained even when automation needs its project.
	opened, e := m.StartProject(ctx, "user", workspace, candidate, contract, prepare)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.PrepareProjectProgression(ctx, "automatic", workspace, candidate, contract, prepare); e != nil {
		t.Fatal(e)
	}
	if m.ProjectStatus("user", candidate, digest).State != StateReady {
		t.Fatal("automation stopped user preview")
	}
	if _, e = m.Stop(ctx, "user"); e != nil {
		t.Fatal(e)
	}
	// A fresh manager/project data directory proves migration actually runs.
	m2 := New(nil, t.TempDir())
	t.Cleanup(m2.Close)
	source, e := m2.prepareProjectData(ctx, original, base)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(source.directory, "note"), []byte("retained user note"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = source.publish(); e != nil {
		t.Fatal(e)
	}
	priorCalls, priorReleases := calls, releases
	if e = m2.PrepareProjectProgression(ctx, "automatic", workspace, candidate, contract, prepare); e != nil {
		t.Fatal(e)
	}
	if ready, e := m2.ProjectDataReady(ctx, contract, candidate); e != nil || !ready {
		t.Fatal("migration was not published", e)
	}
	if m2.ProjectStatus("automatic", candidate, digest).State != StateStopped || calls != priorCalls+1 || releases != priorReleases+1 {
		t.Fatal("temporary runtime not cleaned up")
	}
	if e = m2.PrepareProjectProgression(ctx, "automatic", workspace, candidate, contract, prepare); e != nil || calls != priorCalls+1 {
		t.Fatal("repeated wake reran prepared delivery", e)
	}
	content, e := os.ReadFile(filepath.Join(source.directory, "note"))
	if e != nil || string(content) != "retained user note" {
		t.Fatal("original data changed", e)
	}
	if opened.State != StateReady {
		t.Fatal("user start was not real")
	}
}
