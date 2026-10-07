package previewserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// StartMail is called only by the completed-result service after verifying its
// exact candidate. It does not read or create candidate-controlled launch
// configuration, and never contributes a check receipt or completion fact.
func (m *Manager) StartMail(ctx context.Context, sessionID domain.SessionID, workspace, projectID, candidateSHA string) (Status, error) {
	if m.registryPath == "" || projectID == "" || candidateSHA == "" {
		return stoppedStatus(sessionID), serviceError("RESULT_DATA_UNAVAILABLE", "A persistent project data directory is required")
	}
	// Never quietly replace an older, manually launched application's database
	// with an empty one. Such data needs an explicit, separate migration choice.
	if _, err := os.Lstat(filepath.Join(workspace, "data", "app.sqlite")); !errors.Is(err, os.ErrNotExist) {
		return stoppedStatus(sessionID), serviceError("RESULT_EXISTING_LOCAL_DATA", "This workspace has local application data; preserve it and choose its migration before using the project viewer")
	}
	digest := sha256.Sum256([]byte(projectID))
	dataDir := filepath.Join(filepath.Dir(m.registryPath), "cleardev-contact-data", hex.EncodeToString(digest[:]))
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return stoppedStatus(sessionID), serviceError("RESULT_DATA_UNAVAILABLE", "Cannot prepare persistent project contact data")
	}
	resolved, err := filepath.EvalSymlinks(dataDir)
	if err != nil || resolved != dataDir {
		return stoppedStatus(sessionID), serviceError("RESULT_DATA_UNAVAILABLE", "Project contact data must not be redirected")
	}
	if info, err := os.Lstat(filepath.Join(dataDir, "app.sqlite")); err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
		return stoppedStatus(sessionID), serviceError("RESULT_DATA_UNAVAILABLE", "Project contact database must be a regular local file")
	}
	cfg := Configuration{
		Name: "cleardev-completed-mail", RuntimeExecutable: "node",
		RuntimeArgs: []string{"--experimental-sqlite", "--experimental-strip-types", "backend/src/server.ts"},
		Cwd:         ".", AutoPort: true, URL: "http://127.0.0.1:${PORT}/", TargetKind: TargetApp,
		Env:        map[string]string{"MAIL_APP_DB": filepath.Join(dataDir, "app.sqlite")},
		mailHealth: true, mailCandidateSHA: candidateSHA,
	}
	return m.startConfigured(ctx, sessionID, workspace, cfg)
}

// MailStatus cannot mistake a generic session preview (even one with the same
// configuration name) for the program-selected delivered application.
func (m *Manager) MailStatus(sessionID domain.SessionID, candidateSHA string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[sessionID]
	if run == nil || run.mailCandidateSHA != candidateSHA {
		return stoppedStatus(sessionID)
	}
	return m.statusForLocked(run)
}

// probeMail verifies the application's fixed health contract rather than
// accepting an arbitrary HTTP response. It is separate from delivery evidence.
func (m *Manager) probeMail(ctx context.Context, target string) error {
	read := func(path string, status int) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(target, "/")+path, http.NoBody)
		if err != nil {
			return nil, err
		}
		response, err := m.client.Do(request)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != status || response.Request.URL.String() != request.URL.String() {
			return nil, errors.New("mail application returned an unexpected status or redirect")
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
		if err != nil || len(body) > 64*1024 {
			return nil, errors.New("mail application response is unavailable or exceeds the limit")
		}
		return body, nil
	}
	body, err := read("/api/health", http.StatusOK)
	if err != nil {
		return err
	}
	var health map[string]string
	if json.Unmarshal(body, &health) != nil || len(health) != 2 || health["status"] != "ok" || health["application"] != "complex-mail-app" {
		return errors.New("mail health response does not match the fixed contract")
	}
	page, err := read("/", http.StatusOK)
	if err != nil || !strings.Contains(string(page), `id="import-form"`) || !strings.Contains(string(page), `id="contacts"`) {
		return errors.New("mail application page is not available")
	}
	_, err = read("/__cleardev_missing_result__", http.StatusNotFound)
	return err
}
