package previewserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestMailPreviewRetainsProjectDataAcrossDeliveredWorkspaces(t *testing.T) {
	data := t.TempDir()
	m := New(nil, data)
	t.Cleanup(m.Close)
	first, second := t.TempDir(), t.TempDir()
	for _, workspace := range []string{first, second} {
		if err := core.CopyDemoBaseline(workspace); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	started, err := m.StartMail(ctx, "delivered-one", first, "same-project", strings.Repeat("a", 40))
	if err != nil || started.State != StateReady {
		t.Fatalf("first start: %+v %v", started, err)
	}
	response, err := http.Post(started.URL+"api/import", "application/json", strings.NewReader(`{"emails":["Zeta@example.com","alpha@example.com","ZETA@example.com","invalid"]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"accepted":2`) {
		t.Fatalf("import: %s %v", body, err)
	}
	for range 2 {
		stopped, err := m.Stop(ctx, "delivered-one")
		if err != nil || stopped.State != StateStopped {
			t.Fatal("stop did not settle", stopped, err)
		}
	}
	started, err = m.StartMail(ctx, "delivered-two", second, "same-project", strings.Repeat("b", 40))
	if err != nil || started.State != StateReady {
		t.Fatalf("next version: %+v %v", started, err)
	}
	response, err = http.Get(started.URL + "api/contacts")
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || !strings.Contains(string(body), "zeta@example.com") || !strings.Contains(string(body), "alpha@example.com") {
		t.Fatalf("project contacts were lost: %s %v", body, err)
	}
	for _, workspace := range []string{first, second} {
		if _, err := os.Stat(filepath.Join(workspace, "data", "app.sqlite")); !os.IsNotExist(err) {
			t.Fatal("preview wrote into delivered source", err)
		}
		if _, err := os.Stat(filepath.Join(workspace, ConfigPath)); !os.IsNotExist(err) {
			t.Fatal("preview created candidate-controlled configuration", err)
		}
	}
	if _, err := m.Stop(ctx, "delivered-two"); err != nil {
		t.Fatal(err)
	}
	m.Close()
	// Recreate the actual manager as on daemon restart; no second state store.
	m = New(nil, data)
	defer m.Close()
	started, err = m.StartMail(ctx, "delivered-two", second, "same-project", strings.Repeat("b", 40))
	if err != nil || started.State != StateReady {
		t.Fatal("manager restart", started, err)
	}
	response, err = http.Get(started.URL + "api/contacts")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var contacts struct {
		Contacts []struct {
			Email string `json:"email"`
		} `json:"contacts"`
	}
	if json.NewDecoder(response.Body).Decode(&contacts) != nil || len(contacts.Contacts) != 2 {
		t.Fatal("restart lost persistent contacts")
	}
}

func TestMailPreviewRejectsIncorrectHealthAndStartup(t *testing.T) {
	for _, mode := range []string{"health-404", "health-500", "wrong-json", "extra-json", "wrong-page", "missing-200", "redirect", "ok"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/health":
					if mode == "health-404" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if mode == "health-500" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if mode == "redirect" {
						http.Redirect(w, r, "/", http.StatusFound)
						return
					}
					if mode == "wrong-json" {
						_, _ = w.Write([]byte(`{"status":"ok"}`))
						return
					}
					if mode == "extra-json" {
						_, _ = w.Write([]byte(`{"status":"ok","application":"complex-mail-app","other":"value"}`))
						return
					}
					_, _ = w.Write([]byte(`{"status":"ok","application":"complex-mail-app"}`))
				case "/":
					if mode != "wrong-page" {
						_, _ = w.Write([]byte(`<form id="import-form"></form><ul id="contacts"></ul>`))
					}
				default:
					if mode != "missing-200" {
						w.WriteHeader(http.StatusNotFound)
					}
				}
			}))
			defer server.Close()
			err := New(nil).probeMail(context.Background(), server.URL)
			if (err == nil) != (mode == "ok") {
				t.Fatalf("%s accepted=%v", mode, err == nil)
			}
		})
	}
	m := New(nil, t.TempDir())
	defer m.Close()
	if _, err := m.StartMail(context.Background(), "bad-start", t.TempDir(), "p", strings.Repeat("a", 40)); err == nil {
		t.Fatal("missing app started")
	}
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(workspace, "data", "app.sqlite")
	if err := os.WriteFile(file, []byte("preserve existing user data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartMail(context.Background(), "legacy-data", workspace, "p", strings.Repeat("a", 40)); err == nil {
		t.Fatal("silently abandoned existing user data")
	}
	body, err := os.ReadFile(file)
	if err != nil || string(body) != "preserve existing user data" {
		t.Fatal("existing user data changed")
	}
}
