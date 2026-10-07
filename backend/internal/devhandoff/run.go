package devhandoff

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Main is the development-handoff command. It is not part of the ao product CLI.
func Main(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cleardev-handoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", ModeAuto, "plan, candidate, accept, close, auto, toolchain, or print-start")
	stage := fs.String("stage", "", "stage id such as S12A7; default is the current roadmap stage")
	root := fs.String("root", "", "repository root; default is the Git top-level")
	requireGo := fs.Bool("require-go", false, "require the exact pinned Go version")
	requireNode := fs.Bool("require-node", false, "require the exact pinned Node.js and npm versions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repoRoot := strings.TrimSpace(*root)
	if repoRoot == "" {
		found, err := gitTopLevel()
		if err != nil {
			return err
		}
		repoRoot = found
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return err
	}
	switch *mode {
	case "toolchain":
		if !*requireGo && !*requireNode {
			*requireGo = true
		}
		return runToolchain(abs, *requireGo, *requireNode)
	case "print-start":
		sha, err := currentStartSHA(abs, *stage)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(stdout, sha); err != nil {
			return err
		}
		return nil
	case ModePlan, ModeCandidate, ModeAccept, ModeClose, ModeAuto:
		if *requireGo || *requireNode {
			if err := runToolchain(abs, *requireGo, *requireNode); err != nil {
				return err
			}
		}
		if err := checkMode(abs, *mode, *stage); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "cleardev handoff %s: ok\n", *mode); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("未知交接模式 %q", *mode)
	}
}

func runToolchain(root string, requireGo, requireNode bool) error {
	pin, err := loadPin(root)
	if err != nil {
		return fmt.Errorf("读取工具版本声明失败: %w", err)
	}
	got := Versions{}
	if requireGo {
		got.Go = commandOutput(root, "go", "version")
	}
	if requireNode {
		got.Node = commandOutput(root, "node", "-v")
		got.NPM = commandOutput(root, "npm", "-v")
	}
	if err := checkVersions(pin, got, requireGo, requireNode); err != nil {
		return err
	}
	return nil
}

func currentStartSHA(root, stageID string) (string, error) {
	roadmapRaw, err := os.ReadFile(filepath.Join(root, "docs", "cleardev", "development", "roadmap.md"))
	if err != nil {
		return "", err
	}
	roadmap := parseRoadmap(string(roadmapRaw))
	if stageID == "" {
		stageID = roadmap.Stage
	}
	docs := loadStageDocs(root, stageID)
	plan := parsePlan(docs.Plan)
	if plan.StartSHA == "" {
		return "", fmt.Errorf("当前阶段计划没有起始 Git 提交")
	}
	return plan.StartSHA, nil
}

func gitTopLevel() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("不在 Git 仓库中")
	}
	return strings.TrimSpace(string(out)), nil
}

func commandOutput(dir, name string, args ...string) string {
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	_ = cmd.Run()
	return buf.String()
}
