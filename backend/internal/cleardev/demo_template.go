package cleardev

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DemoTemplateFS is the frozen full-stack mail-list template copied into a
// new Git repository by the in-repo demonstration command.
//
//go:embed all:testdata/complex-mail-app
var DemoTemplateFS embed.FS

const demoTemplateRoot = "testdata/complex-mail-app"

// FrozenDemoPRD returns the fixed product-requirements document used by the
// in-repo demonstration. Callers cannot replace it.
func FrozenDemoPRD() (string, error) {
	raw, err := DemoTemplateFS.ReadFile(demoTemplateRoot + "/PRD.md")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// CopyDemoTemplate writes the frozen template into dest. dest must exist.
func CopyDemoTemplate(dest string) error {
	return fs.WalkDir(DemoTemplateFS, demoTemplateRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := strings.TrimPrefix(path, demoTemplateRoot)
		rel = strings.TrimPrefix(rel, "/")
		target := dest
		if rel != "" {
			target = filepath.Join(dest, rel)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := DemoTemplateFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read template %s: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}
