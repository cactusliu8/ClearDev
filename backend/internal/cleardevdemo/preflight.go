package cleardevdemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func (o Options) lookPath(name string) (string, error) {
	if o.LookPath != nil {
		return o.LookPath(name)
	}
	return exec.LookPath(name)
}

func (o Options) commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	if o.CommandOutput != nil {
		return o.CommandOutput(ctx, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// Preflight checks host tools before any demonstration directory is created.
func Preflight(ctx context.Context, opts Options) error {
	ensureLocalBinOnPath()
	for _, bin := range []string{"git", "docker", "node", "npm"} {
		if _, err := opts.lookPath(bin); err != nil {
			return fmt.Errorf("preflight: %s is required: %w", bin, err)
		}
	}
	if _, err := opts.commandOutput(ctx, "git", "version"); err != nil {
		return fmt.Errorf("preflight: git is not usable: %w", err)
	}
	imageOut, err := opts.commandOutput(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", core.StandardCandidateCheckImage)
	if err != nil {
		return fmt.Errorf("preflight: frozen check image %s is missing: %w", core.StandardCandidateCheckImage, err)
	}
	if strings.TrimSpace(string(imageOut)) == "" {
		return fmt.Errorf("preflight: frozen check image %s has no digest", core.StandardCandidateCheckImage)
	}
	codex := "codex"
	if path, err := opts.lookPath(codex); err == nil {
		codex = path
	}
	if _, err := opts.commandOutput(ctx, codex, "login", "status"); err != nil {
		return fmt.Errorf("preflight: Codex is not logged in: %w", err)
	}
	if _, err := inspectPackagedRuntime(opts); err != nil {
		return fmt.Errorf("preflight: packaged Electron build is not verifiable: %w", err)
	}
	if runtime.GOOS == "linux" {
		hostDisplay := strings.TrimSpace(opts.HostDisplay)
		if hostDisplay == "" {
			hostDisplay = strings.TrimSpace(os.Getenv("DISPLAY"))
		}
		if hostDisplay == "" {
			return fmt.Errorf("preflight: a real desktop DISPLAY is required; refusing an invisible demonstration")
		}
		for _, bin := range []string{"Xvfb", "x11vnc", "vncviewer", "xdotool", "xwd"} {
			if _, err := opts.lookPath(bin); err != nil {
				return fmt.Errorf("preflight: %s is required for the visible desktop demonstration: %w", bin, err)
			}
		}
		if _, err := opts.commandOutput(ctx, "xdotool", "getmouselocation", "--shell"); err != nil {
			return fmt.Errorf("preflight: current desktop DISPLAY=%s is not reachable: %w", hostDisplay, err)
		}
	}
	return nil
}

func packagedElectronBinary(opts Options) (string, error) {
	if configured := strings.TrimSpace(opts.ElectronBin); configured == "" {
		configured = strings.TrimSpace(os.Getenv("AO_ELECTRON_BIN"))
		if configured != "" {
			opts.ElectronBin = configured
		}
	}
	if configured := strings.TrimSpace(opts.ElectronBin); configured != "" {
		resolved, err := filepath.Abs(configured)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(resolved); err != nil {
			return "", err
		}
		return resolved, nil
	}
	root, err := repoRoot(opts)
	if err != nil {
		return "", err
	}
	patterns := []string{
		filepath.Join(root, "frontend", "out") + "/*/agent-orchestrator",
		filepath.Join(root, "frontend", "out") + "/*/*/agent-orchestrator",
	}
	var matches []string
	for _, pattern := range patterns {
		found, _ := filepath.Glob(pattern)
		matches = append(matches, found...)
	}
	if len(matches) == 0 {
		return "", errors.New("set AO_ELECTRON_BIN or run npm --prefix frontend run package")
	}
	return matches[0], nil
}

func ensureLocalBinOnPath() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	localBin := filepath.Join(home, ".local", "bin")
	if _, err := os.Stat(localBin); err != nil {
		return
	}
	current := os.Getenv("PATH")
	for _, dir := range strings.Split(current, string(os.PathListSeparator)) {
		if dir == localBin {
			return
		}
	}
	_ = os.Setenv("PATH", localBin+string(os.PathListSeparator)+current)
}

func repoRoot(opts Options) (string, error) {
	if strings.TrimSpace(opts.RepoRoot) != "" {
		return opts.RepoRoot, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := filepath.Clean(wd); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "frontend", "package.json")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not locate the ClearDev repository root")
		}
	}
}
