package cleardev

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectInlineNodeCommand(t *testing.T) {
	for _, flag := range []string{"-e", "--eval"} {
		basis := projectBasisFixture()
		basis.Checks[0].Argv = []string{"node", flag, "const fs=require('fs');\nif(!fs.existsSync('package.json')) process.exit(7)"}
		if _, err := ResolveProjectRuntime(basis); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
		run := func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, basis.Checks[0].Argv[0], basis.Checks[0].Argv[1:]...)
			cmd.Dir = dir
			return cmd.Run()
		}
		if err := run(); err != nil {
			t.Fatalf("real file check: %v", err)
		}
		if err := os.Remove(filepath.Join(dir, "package.json")); err != nil {
			t.Fatal(err)
		}
		if err := run(); err == nil {
			t.Fatal("missing file passed")
		} else {
			var e *exec.ExitError
			if !errors.As(err, &e) || e.ExitCode() != 7 {
				t.Fatalf("wrong failure: %v", err)
			}
		}
	}
}

func TestProjectInlineNodeCommandRejectsInvalidArguments(t *testing.T) {
	for _, argv := range [][]string{
		{"node", "-e"}, {"node", "-e", ""}, {"node", "-e", " \n"},
		{"node", "--eval", "process.exit(0)", "--inspect"},
		{"node", "-e", "--inspect"}, {"node", "-e", "x\x00y"},
		{"node", "-e", strings.Repeat("x", 2001)},
		{"node", "--inspect", "app.js"}, {"node", "--require", "app.js"},
		{"sh", "-c", "node -e 'process.exit(0)'"}, {"npx", "vite"}, {"npm", "install"},
	} {
		if err := ValidateProjectNodeCommand(argv); err == nil {
			t.Fatalf("accepted %q", argv)
		}
	}
}

func TestProjectInlinePreservesExistingArgumentLimits(t *testing.T) {
	basis := projectBasisFixture()
	basis.Checks[0].Argv = []string{"node", "check.js", strings.Repeat("中", 700)}
	if _, err := ResolveProjectRuntime(basis); err != nil {
		t.Fatalf("historical multibyte check rejected: %v", err)
	}
	for _, argv := range [][]string{
		{"node", "prepare.js", strings.Repeat("x", 3000)},
		{"npm", "test", "--", strings.Repeat("中", 700)},
	} {
		if err := ValidateProjectNodeCommand(argv); err != nil {
			t.Fatalf("historical argument rejected: %v", err)
		}
	}
}
