package cleardevdemo

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func initGitRepo(dir string) (string, error) {
	if err := core.CopyDemoBaseline(dir); err != nil {
		return "", err
	}
	commands := [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "ClearDev Demo"},
		{"config", "user.email", "cleardev-demo@example.invalid"},
		{"add", "-A"},
		{"commit", "-m", "test: freeze ClearDev demo template"},
		{"remote", "add", "origin", "."},
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"},
	}
	for _, args := range commands {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
	}
	cmd := exec.Command("git", "rev-parse", "--verify", "HEAD^{commit}")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read template commit: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func checkoutCommit(repo, sha, dest string) error {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", repo, "archive", "--format=tar", sha)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git archive %s: %w", sha, err)
	}
	if err := extractTar(stdout, dest); err != nil {
		_ = cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git archive %s: %w: %s", sha, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeArchivePath(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			const maxArchiveFile = 32 << 20
			_, copyErr := io.CopyN(file, tr, maxArchiveFile+1)
			closeErr := file.Close()
			if copyErr != nil && !errors.Is(copyErr, io.EOF) {
				return copyErr
			}
			if copyErr == nil {
				return fmt.Errorf("archive file %s exceeds %d bytes", hdr.Name, maxArchiveFile)
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

func safeArchivePath(root, name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if cleaned == "." {
		return root, nil
	}
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("refusing archive path %s", name)
	}
	return filepath.Join(root, cleaned), nil
}

func gitRevParse(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", "HEAD^{commit}")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
