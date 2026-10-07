package devhandoff

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type gitRepo struct {
	root string
}

func (g gitRepo) run(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", g.root}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = out
		}
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

func (g gitRepo) head() (string, error) {
	return g.run("rev-parse", "HEAD")
}

func (g gitRepo) porcelain() (string, error) {
	return g.run("status", "--porcelain")
}

func (g gitRepo) tracked(rel string) bool {
	_, err := g.run("ls-files", "--error-unmatch", filepath.ToSlash(rel))
	return err == nil
}

func (g gitRepo) commitExists(sha string) bool {
	kind, err := g.run("cat-file", "-t", sha)
	return err == nil && kind == "commit"
}

func (g gitRepo) isAncestor(anc, desc string) bool {
	_, err := g.run("merge-base", "--is-ancestor", anc, desc)
	return err == nil
}

func (g gitRepo) changedFiles(from, to string) ([]string, error) {
	out, err := g.run("diff", "--name-only", from+".."+to)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (g gitRepo) touchedFiles(from, to string) ([]string, error) {
	out, err := g.run("log", "--format=", "--name-only", from+".."+to, "--")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var files []string
	for _, file := range strings.Split(out, "\n") {
		file = strings.TrimSpace(file)
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		files = append(files, file)
	}
	return files, nil
}

func (g gitRepo) lastCommitForFile(rel string) (string, error) {
	return g.run("log", "-1", "--format=%H", "--", filepath.ToSlash(rel))
}

func (g gitRepo) fileAt(sha, rel string) (string, error) {
	cmd := exec.Command("git", "-C", g.root, "show", sha+":"+filepath.ToSlash(rel))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git show %s:%s: %s", sha, rel, msg)
	}
	return stdout.String(), nil
}

func (g gitRepo) commitFiles(sha string) ([]string, error) {
	out, err := g.run("diff-tree", "--no-commit-id", "--name-only", "-r", sha)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}
