package cleardev

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// demoBaselineFS supplies the initial, working product, not the answer to an
// incremental requirement. The legacy unfinished template remains unchanged.
//
//go:embed all:testdata/demo-baseline
var demoBaselineFS embed.FS

const demoBaselineRoot = "testdata/demo-baseline"

// CopyDemoBaseline creates a working complex-mail-app in an existing EMPTY
// directory. It only provisions source; it never starts agents, approves a plan,
// changes ClearDev state, or claims completion. Callers must commit and verify
// this baseline before registering a requirement through the normal product.
// The initial schema is part of the baseline; incremental tasks cannot change it.
func CopyDemoBaseline(dest string) error {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("demo baseline destination must be empty")
	}
	if err := CopyDemoTemplate(dest); err != nil {
		return err
	}
	return fs.WalkDir(demoBaselineFS, demoBaselineRoot, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := strings.TrimPrefix(name, demoBaselineRoot+"/")
		if name == demoBaselineRoot {
			return nil
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := demoBaselineFS.ReadFile(name)
		if err != nil {
			return err
		}
		// Baseline checks extend an existing npm test entry, preserving every
		// byte of its original tests rather than installing a parallel runner.
		if strings.HasSuffix(rel, ".append") {
			target = strings.TrimSuffix(target, ".append")
			original, err := os.ReadFile(target)
			if err != nil {
				return err
			}
			data = append(original, data...)
		}
		return os.WriteFile(target, data, 0o600)
	})
}
