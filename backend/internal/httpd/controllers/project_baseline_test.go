package controllers_test

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectsAPIInitializesFrozenMailProduct(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()
	raw, err := json.Marshal(map[string]string{"path": dir, "template": "complex-mail-app"})
	if err != nil {
		t.Fatal(err)
	}
	body, status, headers := doRequest(t, srv, http.MethodPost, "/api/v1/projects/initialize", string(raw))
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("initialize status=%d body=%s", status, body)
	}
	for _, name := range []string{"backend/src/server.ts", "frontend/index.html", "test/app.test.js", "migrations/001_contacts.sql"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("template option was not applied: %s: %v", name, err)
		}
	}
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=all").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("API returned an uncommitted baseline: %v %s", err, out)
	}
	// The existing registration endpoint accepts the prepared repository.
	raw, err = json.Marshal(map[string]string{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/projects", string(raw))
	if status != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", status, body)
	}
	// Re-preparation cannot reset a registered existing project.
	raw, err = json.Marshal(map[string]string{"path": dir, "template": "complex-mail-app"})
	if err != nil {
		t.Fatal(err)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/projects/initialize", string(raw))
	assertErrorCode(t, body, status, http.StatusConflict, "PROJECT_ALREADY_INITIALIZED")
}

func TestProjectsAPIRejectsUnknownTemplateWithoutFiles(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()
	raw, err := json.Marshal(map[string]string{"path": dir, "template": "agent-defined-template"})
	if err != nil {
		t.Fatal(err)
	}
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/projects/initialize", string(raw))
	assertErrorCode(t, body, status, http.StatusBadRequest, "PROJECT_TEMPLATE_UNSUPPORTED")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid template modified directory: %v %v", entries, err)
	}
}
