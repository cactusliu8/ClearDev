package cleardevdemo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This is an explicit Electron process double, not a browser or Agent run.
// It observes the actual environment passed by startPackagedElectron.
func TestPackagedElectronPinsHostNPMCacheBeforeIsolatingHome(t *testing.T) {
	for _, kind := range []string{"default", "explicit", "relative", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			hostHome := t.TempDir()
			t.Setenv("HOME", hostHome)
			t.Setenv("CLEARDEV_NPM_CACHE_DIR", "")
			t.Chdir(hostHome)
			cache := filepath.Join(hostHome, ".npm")
			if kind != "default" {
				cache = filepath.Join(hostHome, "custom cache")
			}
			if err := os.Mkdir(cache, 0o700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "explicit":
				t.Setenv("CLEARDEV_NPM_CACHE_DIR", cache)
			case "relative":
				t.Setenv("CLEARDEV_NPM_CACHE_DIR", "custom cache")
			case "symlink":
				link := filepath.Join(hostHome, "cache-link")
				if err := os.Symlink(cache, link); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CLEARDEV_NPM_CACHE_DIR", link)
			}
			if err := os.Mkdir(filepath.Join(hostHome, ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(hostHome, ".codex", "auth.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			layout, err := AllocateLayout(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(t.TempDir(), "electron-double")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$HOME\" \"$CLEARDEV_NPM_CACHE_DIR\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd, err := startPackagedElectron(Options{ElectronBin: binary}, layout, ":test", layout.EvidenceDir, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			output, err := os.ReadFile(filepath.Join(layout.EvidenceDir, "electron.log"))
			if err != nil {
				t.Fatal(err)
			}
			if want := layout.HomeDir + "\n" + cache + "\n"; string(output) != want {
				t.Fatalf("child environment = %q, want %q", output, want)
			}
			if _, err := os.Stat(filepath.Join(layout.HomeDir, ".npm")); !os.IsNotExist(err) {
				t.Fatalf("cache was copied or linked into isolated HOME: %v", err)
			}
			if got := os.Getenv("HOME"); got != hostHome {
				t.Fatalf("launcher HOME changed: %q", got)
			}
		})
	}
}

func TestPackagedElectronRejectsInvalidCacheBeforeCredentialOrProcessSetup(t *testing.T) {
	for _, kind := range []string{"missing-default", "missing-explicit", "file", "broken-symlink"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLEARDEV_NPM_CACHE_DIR", "")
			if kind != "missing-default" {
				// A valid fallback must not hide a broken explicit configuration.
				if err := os.Mkdir(filepath.Join(home, ".npm"), 0o700); err != nil {
					t.Fatal(err)
				}
				invalid := filepath.Join(home, "invalid-cache")
				t.Setenv("CLEARDEV_NPM_CACHE_DIR", invalid)
				switch kind {
				case "file":
					if err := os.WriteFile(invalid, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				case "broken-symlink":
					if err := os.Symlink(filepath.Join(home, "missing"), invalid); err != nil {
						t.Fatal(err)
					}
				}
			}
			layout, err := AllocateLayout(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd, err := startPackagedElectron(Options{ElectronBin: binary}, layout, ":test", layout.EvidenceDir, 0)
			if cmd != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("invalid cache started Electron")
			}
			if err == nil || !strings.Contains(err.Error(), "npm cache") {
				t.Fatalf("want actionable npm cache error before credentials, got %v", err)
			}
			for _, file := range []string{filepath.Join(layout.HomeDir, ".codex"), filepath.Join(layout.EvidenceDir, "electron.log")} {
				if _, err := os.Lstat(file); !os.IsNotExist(err) {
					t.Fatalf("invalid cache created %s: %v", file, err)
				}
			}
		})
	}
}
