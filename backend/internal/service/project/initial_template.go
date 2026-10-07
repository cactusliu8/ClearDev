package project

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// commitInitialRepository preserves the ordinary initializer but gives the
// frozen starting product a reproducible commit, independent of wall clock or
// inherited Git author settings. This is provisioning, not a delivery verdict.
func commitInitialRepository(ctx context.Context, path, template string) error {
	args := []string{"-c", "user.name=Agent Orchestrator", "-c", "user.email=ao@example.com", "commit", "--allow-empty", "-m", "initial commit"}
	if template == "" {
		_, err := gitOutput(ctx, path, args...)
		return err
	}
	args = append([]string{"-C", path, "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // fixed Git arguments and a validated empty project directory.
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z", "GIT_AUTHOR_NAME=Agent Orchestrator", "GIT_COMMITTER_NAME=Agent Orchestrator", "GIT_AUTHOR_EMAIL=ao@example.com", "GIT_COMMITTER_EMAIL=ao@example.com")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fixed baseline commit: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Only the existing project preparation service may select a starting product.
// This is provisioning, not a check result or an execution/approval shortcut.
func prepareInitialTemplate(path, template string, target repositorySetupTarget) error {
	if template == "" {
		return nil
	}
	if template != "complex-mail-app" {
		return apierr.Invalid("PROJECT_TEMPLATE_UNSUPPORTED", "Only the frozen complex-mail-app starting product is supported.", nil)
	}
	if target != repositorySetupPlainFolder {
		return apierr.Conflict("PROJECT_TEMPLATE_REQUIRES_EMPTY_FOLDER", "The starting product requires a new empty folder, not an existing repository.", nil)
	}
	if err := core.CopyDemoBaseline(path); err != nil {
		return apierr.Invalid("PROJECT_TEMPLATE_PREPARATION_FAILED", "Could not prepare the starting product. The destination must be empty; existing files are never replaced.", map[string]any{"error": err.Error()})
	}
	return nil
}
