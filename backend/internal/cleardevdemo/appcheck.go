package cleardevdemo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite" // SQLite driver for the frozen sample check
)

func verifyFinalApp(ctx context.Context, layout Layout, commitSHA string, group *ProcessGroup, client *apiClient, projectID, sessionID string) (map[string]int, error) {
	if err := checkoutCommit(layout.RepoDir, commitSHA, layout.AppDir); err != nil {
		return nil, err
	}
	dbFile := filepath.Join(layout.AppDir, "data", "app.sqlite")
	if err := os.MkdirAll(filepath.Dir(dbFile), 0o700); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "npm", "start")
	cmd.Dir = layout.AppDir
	cmd.Env = overlayEnv(map[string]string{
		"PORT":                       fmt.Sprintf("%d", layout.AppPort),
		"MAIL_APP_DB":                dbFile,
		"npm_config_update_notifier": "false",
	})
	logFile, err := os.Create(filepath.Join(layout.EvidenceDir, "final-app.log"))
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := attachProcessLog(cmd, logFile); err != nil {
		return nil, fmt.Errorf("start final app: %w", err)
	}
	group.add(cmd.Process.Pid)
	if err := waitListening(ctx, layout.AppPort, 20*time.Second); err != nil {
		return nil, err
	}

	openPreview := func(ctx context.Context, appURL string) error {
		if client == nil || strings.TrimSpace(sessionID) == "" {
			return fmt.Errorf("no session available for browser preview")
		}
		return client.post(ctx, "/sessions/"+url.PathEscape(sessionID)+"/preview", map[string]any{"url": appURL}, nil, 200)
	}
	pageCounts, apiCounts, err := submitFrozenSampleInBrowser(ctx, layout.DebugPort, layout.AppPort, sampleBrowserOpen{
		ProjectID: projectID,
		SessionID: sessionID,
		Preview:   openPreview,
	})
	if err != nil {
		return nil, err
	}
	apiPayload, err := json.Marshal(apiCounts)
	if err != nil {
		return nil, err
	}
	pagePayload, err := json.Marshal(pageCounts)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(layout.EvidenceDir, "sample-api.json"), append(apiPayload, '\n'), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(layout.EvidenceDir, "sample-page.json"), append(pagePayload, '\n'), 0o600); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, "SELECT normalized_email FROM contacts ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("query SQLite contacts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(emails) != FrozenSampleCounts["accepted"] || emails[0] != "user@example.com" || emails[1] != "foo@example.com" {
		return nil, fmt.Errorf("SQLite contacts = %#v, want [user@example.com foo@example.com]", emails)
	}
	return pageCounts, nil
}

func waitListening(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), http.NoBody)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err == nil {
			_ = res.Body.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("final app did not listen on 127.0.0.1:%d", port)
}
