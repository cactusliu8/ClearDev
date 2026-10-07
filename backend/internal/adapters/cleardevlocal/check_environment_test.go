package cleardevlocal

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDependencyTreeRootRejectsEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation needs privileges on Windows")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "node_modules")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupPreparedTree(root) })
	if err := os.WriteFile(filepath.Join(parent, "outside"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := dependencyTreeSHA256(root); err == nil {
		t.Fatal("escaping dependency symlink was hashed")
	}
	if err := sealDependencyTree(root); err == nil {
		t.Fatal("escaping dependency symlink was sealed")
	}
}

func TestDependencyTreeRootAllowsInternalSymlinkAndSealsModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation needs privileges on Windows")
	}
	root := filepath.Join(t.TempDir(), "node_modules")
	bin := filepath.Join(root, "package", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(bin, "tool")
	if err := os.WriteFile(executable, []byte("tool"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("package/bin/tool", filepath.Join(root, "tool")); err != nil {
		t.Fatal(err)
	}
	if err := sealDependencyTree(root); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		root:                           0o555,
		filepath.Join(root, "package"): 0o555,
		bin:                            0o555,
		executable:                     0o555,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
		}
	}
	first, err := dependencyTreeSHA256(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := dependencyTreeSHA256(root)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("dependency tree digest is unstable: first=%q second=%q", first, second)
	}
	cleanupPreparedTree(root)
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("prepared tree still exists after cleanup: %v", err)
	}
}
