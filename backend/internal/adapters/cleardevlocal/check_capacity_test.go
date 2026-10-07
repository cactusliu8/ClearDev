package cleardevlocal

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type checkTarFixture struct {
	name     string
	typeflag byte
	body     string
	linkname string
	mode     int64
}

func TestProductionCheckCapacityLimits(t *testing.T) {
	limits := productionCheckCapacityLimits()
	if limits.Source != (checkUsageLimit{Bytes: 1024 * 1024 * 1024, Items: 100_000}) ||
		limits.Dependency != (checkUsageLimit{Bytes: 10 * 1024 * 1024 * 1024, Items: 100_000}) ||
		limits.Output != (checkUsageLimit{Bytes: 256 * 1024 * 1024, Items: 100_000}) ||
		limits.ActiveTemporaryBytes != 32*1024*1024*1024 || limits.PersistentBytes != 2*1024*1024*1024 {
		t.Fatalf("production limits = %#v", limits)
	}
}

func TestCheckUsageCounterChargesLogicalBytesAndItemsBeforeOverflow(t *testing.T) {
	counter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 4, Items: 3})
	if err := counter.ChargeDirectory(); err != nil {
		t.Fatal(err)
	}
	if err := counter.ChargeRegularFile(4); err != nil {
		t.Fatal(err)
	}
	if err := counter.ChargeSymlink(); err != nil {
		t.Fatal(err)
	}
	if got := counter.Usage(); got != (checkUsage{Bytes: 4, Items: 3}) {
		t.Fatalf("usage = %#v", got)
	}
	if err := counter.ChargeRegularFile(0); !errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("item overflow error = %v", err)
	}
	if got := counter.Usage(); got != (checkUsage{Bytes: 4, Items: 3}) {
		t.Fatalf("item overflow changed usage = %#v", got)
	}

	bytesCounter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 3, Items: 10})
	if err := bytesCounter.ChargeRegularFile(3); err != nil {
		t.Fatal(err)
	}
	if err := bytesCounter.ChargeRegularFile(1); !errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("byte overflow error = %v", err)
	}
	if got := bytesCounter.Usage(); got != (checkUsage{Bytes: 3, Items: 1}) {
		t.Fatalf("byte overflow changed usage = %#v", got)
	}
}

func TestExtractCheckTarEnforcesLogicalByteLimitBeforeWrite(t *testing.T) {
	archive := checkTarBytes(t, checkTarFixture{name: "package.bin", typeflag: tar.TypeReg, body: "1234", mode: 0o644})
	destination := t.TempDir()
	counter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 4, Items: 1})
	if err := extractCheckTar(tar.NewReader(bytes.NewReader(archive)), destination, counter); err != nil {
		t.Fatalf("extract at limit: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(destination, "package.bin")); err != nil || string(got) != "1234" {
		t.Fatalf("extracted file = %q, err=%v", got, err)
	}

	overDestination := t.TempDir()
	overCounter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 3, Items: 1})
	err := extractCheckTar(tar.NewReader(bytes.NewReader(archive)), overDestination, overCounter)
	if !errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("limit+1 error = %v", err)
	}
	if entries, readErr := os.ReadDir(overDestination); readErr != nil || len(entries) != 0 {
		t.Fatalf("limit+1 wrote entries=%d err=%v", len(entries), readErr)
	}
	if got := overCounter.Usage(); got != (checkUsage{}) {
		t.Fatalf("limit+1 usage = %#v", got)
	}
}

func TestExtractCheckTarCountsImplicitDirectoriesFilesAndSymlinks(t *testing.T) {
	archive := checkTarBytes(t,
		checkTarFixture{name: "pkg/bin/tool", typeflag: tar.TypeReg, body: "x", mode: 0o755},
		checkTarFixture{name: ".bin", typeflag: tar.TypeDir, mode: 0o755},
		checkTarFixture{name: ".bin/tool", typeflag: tar.TypeSymlink, linkname: "../pkg/bin/tool"},
	)
	destination := t.TempDir()
	counter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 1, Items: 5})
	if err := extractCheckTar(tar.NewReader(bytes.NewReader(archive)), destination, counter); err != nil {
		t.Fatalf("extract exact item limit: %v", err)
	}
	if got := counter.Usage(); got != (checkUsage{Bytes: 1, Items: 5}) {
		t.Fatalf("usage = %#v", got)
	}
	if target, err := os.Readlink(filepath.Join(destination, ".bin", "tool")); err != nil || target != "../pkg/bin/tool" {
		t.Fatalf("safe symlink target=%q err=%v", target, err)
	}

	overDestination := t.TempDir()
	overCounter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 1, Items: 4})
	err := extractCheckTar(tar.NewReader(bytes.NewReader(archive)), overDestination, overCounter)
	if !errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("item limit+1 error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(overDestination, ".bin", "tool")); !os.IsNotExist(statErr) {
		t.Fatalf("item limit+1 created symlink: %v", statErr)
	}
	if got := overCounter.Usage(); got != (checkUsage{Bytes: 1, Items: 4}) {
		t.Fatalf("item limit+1 usage = %#v", got)
	}
}

func TestExtractCheckTarRejectsUnsafeAndDuplicatePaths(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		fixtures []checkTarFixture
	}{
		{name: "absolute", fixtures: []checkTarFixture{{name: "/outside", typeflag: tar.TypeReg, body: "x"}}},
		{name: "parent", fixtures: []checkTarFixture{{name: "../outside", typeflag: tar.TypeReg, body: "x"}}},
		{name: "embedded parent", fixtures: []checkTarFixture{{name: "inside/../outside", typeflag: tar.TypeReg, body: "x"}}},
		{name: "duplicate canonical path", fixtures: []checkTarFixture{
			{name: "./same", typeflag: tar.TypeReg, body: "a"},
			{name: "same", typeflag: tar.TypeReg, body: "b"},
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			destination := t.TempDir()
			counter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 16, Items: 16})
			err := extractCheckTar(tar.NewReader(bytes.NewReader(checkTarBytes(t, testCase.fixtures...))), destination, counter)
			if err == nil {
				t.Fatal("unsafe archive unexpectedly passed")
			}
		})
	}
}

func TestExtractCheckTarRejectsHardlinksDevicesAndFIFOs(t *testing.T) {
	for _, typeflag := range []byte{tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo} {
		t.Run(string([]byte{typeflag}), func(t *testing.T) {
			fixture := checkTarFixture{name: "forbidden", typeflag: typeflag, linkname: "target"}
			destination := t.TempDir()
			counter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 1, Items: 1})
			if err := extractCheckTar(tar.NewReader(bytes.NewReader(checkTarBytes(t, fixture))), destination, counter); err == nil {
				t.Fatalf("archive type %d unexpectedly passed", typeflag)
			}
			if got := counter.Usage(); got != (checkUsage{}) {
				t.Fatalf("forbidden type %d usage = %#v", typeflag, got)
			}
		})
	}
}

func TestExtractCheckTarRejectsSymlinksOutsideNodeModules(t *testing.T) {
	for _, target := range []string{"../../outside", "/etc/passwd"} {
		t.Run(target, func(t *testing.T) {
			archive := checkTarBytes(t,
				checkTarFixture{name: ".bin", typeflag: tar.TypeDir, mode: 0o755},
				checkTarFixture{name: ".bin/tool", typeflag: tar.TypeSymlink, linkname: target},
			)
			destination := t.TempDir()
			counter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 1, Items: 2})
			if err := extractCheckTar(tar.NewReader(bytes.NewReader(archive)), destination, counter); err == nil {
				t.Fatal("escaping symlink unexpectedly passed")
			}
			if _, err := os.Lstat(filepath.Join(destination, ".bin", "tool")); !os.IsNotExist(err) {
				t.Fatalf("escaping symlink was created: %v", err)
			}
		})
	}
}

func TestExtractCheckTarNeverTraversesAnArchiveSymlinkParent(t *testing.T) {
	archive := checkTarBytes(t,
		checkTarFixture{name: "pkg", typeflag: tar.TypeDir, mode: 0o755},
		checkTarFixture{name: "alias", typeflag: tar.TypeSymlink, linkname: "pkg"},
		checkTarFixture{name: "alias/file", typeflag: tar.TypeReg, body: "x"},
	)
	destination := t.TempDir()
	counter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 1, Items: 3})
	if err := extractCheckTar(tar.NewReader(bytes.NewReader(archive)), destination, counter); err == nil {
		t.Fatal("archive symlink parent unexpectedly passed")
	}
	if _, err := os.Lstat(filepath.Join(destination, "pkg", "file")); !os.IsNotExist(err) {
		t.Fatalf("extractor traversed archive symlink: %v", err)
	}
	if got := counter.Usage(); got != (checkUsage{Items: 2}) {
		t.Fatalf("symlink parent rejection usage = %#v", got)
	}
}

func TestExtractCheckTarRequiresEmptyRealDirectory(t *testing.T) {
	archive := checkTarBytes(t, checkTarFixture{name: "file", typeflag: tar.TypeReg, body: "x"})
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(destination, "existing"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	counter := newTestCheckUsageCounter(t, checkUsageLimit{Bytes: 1, Items: 1})
	if err := extractCheckTar(tar.NewReader(bytes.NewReader(archive)), destination, counter); err == nil {
		t.Fatal("non-empty destination unexpectedly passed")
	}

	realDestination := t.TempDir()
	symlink := filepath.Join(t.TempDir(), "destination")
	if err := os.Symlink(realDestination, symlink); err != nil {
		t.Fatal(err)
	}
	if err := extractCheckTar(tar.NewReader(bytes.NewReader(archive)), symlink, counter); err == nil {
		t.Fatal("symlink destination unexpectedly passed")
	}
}

func newTestCheckUsageCounter(t *testing.T, limit checkUsageLimit) *checkUsageCounter {
	t.Helper()
	counter, err := newCheckUsageCounter(limit)
	if err != nil {
		t.Fatal(err)
	}
	return counter
}

func checkTarBytes(t *testing.T, fixtures ...checkTarFixture) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, fixture := range fixtures {
		mode := fixture.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name: fixture.name, Typeflag: fixture.typeflag, Linkname: fixture.linkname,
			Mode: mode, Size: int64(len(fixture.body)),
		}
		if fixture.typeflag != tar.TypeReg && fixture.typeflag != 0 {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if header.Size > 0 {
			if _, err := writer.Write([]byte(fixture.body)); err != nil {
				t.Fatalf("write tar body: %v", err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buffer.Bytes()
}
