package cleardevlocal

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCheckContainerRunArgsFreezeIsolationAndHardTmpfsLimits(t *testing.T) {
	request := CheckContainerRequest{
		Image:         "sha256:immutable",
		Argv:          []string{"node", "--test", "test/email.test.js"},
		SourceDir:     "/trusted/candidate",
		SourceBytes:   1234,
		SourceItems:   12,
		DependencyDir: "/trusted/dependencies/node_modules",
		Timeout:       time.Minute,
		MemoryBytes:   256 * 1024 * 1024,
		PidsLimit:     64,
		OutputLimit:   1024,
	}
	args, err := CheckContainerRunArgs(request, "/tmp/check.cid")
	if err != nil {
		t.Fatalf("CheckContainerRunArgs: %v", err)
	}
	if len(args) < 3 || args[0] != "run" || args[len(args)-3] != "sha256:immutable" || !reflect.DeepEqual(args[len(args)-2:], []string{"sleep", "infinity"}) {
		t.Fatalf("container command tail = %#v", args)
	}
	for _, required := range []string{
		"--stop-timeout=1", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--user=65532:65532",
	} {
		if !containsString(args, required) {
			t.Fatalf("container args missing %q: %#v", required, args)
		}
	}
	workspaceTmpfs := findArgPrefix(args, "--tmpfs=/workspace:")
	if workspaceTmpfs == "" || !strings.Contains(workspaceTmpfs, "mode=1777") {
		t.Fatalf("workspace tmpfs is not writable/sticky: %q", workspaceTmpfs)
	}
	imageIndex := indexOf(args, "sha256:immutable")
	sourceMount := "--mount=type=bind,src=/trusted/candidate,dst=/candidate-source,readonly"
	dependencyMount := "--mount=type=bind,src=/trusted/dependencies/node_modules,dst=/workspace/node_modules,readonly"
	if imageIndex < 0 || indexOf(args, sourceMount) < 0 || indexOf(args, dependencyMount) < 0 || indexOf(args, sourceMount) > imageIndex || indexOf(args, dependencyMount) > imageIndex {
		t.Fatalf("trusted source or dependency mount is not read-only and before image: %#v", args)
	}
	if containsString(args, "--mount=type=bind,src=/trusted/candidate,dst=/workspace") {
		t.Fatal("workspace must not be a host bind mount")
	}

	// Keep the exact expected arithmetic visible without relying on a Docker
	// engine: source 1234 + the fixed 256 MiB writable allowance, with no
	// extra candidate-writable overhead.
	wantTmpfs := "--tmpfs=/workspace:rw,mode=1777,size=" +
		itoa(1234+CheckContainerMaxOutputBytes) +
		",nr_inodes=" + itoa(12+CheckContainerMaxOutputItems)
	if workspaceTmpfs != wantTmpfs {
		t.Fatalf("workspace tmpfs = %q, want %q", workspaceTmpfs, wantTmpfs)
	}
}

func TestCheckContainerRunArgsUsesCallerCIDFileWithoutCreatingIt(t *testing.T) {
	request := CheckContainerRequest{
		Image: "sha256:immutable", Argv: []string{"node"}, SourceDir: "/trusted/candidate",
		SourceBytes: 1, SourceItems: 1, CIDFile: filepath.Join(t.TempDir(), "check.cid"),
		Timeout: time.Minute, MemoryBytes: 64 * 1024 * 1024, PidsLimit: 1, OutputLimit: 1024,
	}
	args, err := CheckContainerRunArgs(request, "")
	if err != nil {
		t.Fatalf("CheckContainerRunArgs: %v", err)
	}
	if !containsString(args, "--cidfile="+request.CIDFile) {
		t.Fatalf("caller CIDFile missing from args: %#v", args)
	}
	if _, err := os.Lstat(request.CIDFile); !os.IsNotExist(err) {
		t.Fatalf("CheckContainerRunArgs created caller CIDFile: err=%v", err)
	}
}

func TestCheckContainerExecArgsUsesDirectCandidateArgv(t *testing.T) {
	got, err := CheckContainerExecArgs(strings.Repeat("a", 64), []string{"node", "-e", "console.log(1)"})
	if err != nil {
		t.Fatalf("CheckContainerExecArgs: %v", err)
	}
	want := []string{"exec", "--user=65532:65532", "--workdir=/workspace", strings.Repeat("a", 64), "node", "-e", "console.log(1)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exec args = %#v, want %#v", got, want)
	}
	if _, err := CheckContainerExecArgs(strings.Repeat("a", 63), []string{"-e", "bad"}); err == nil {
		t.Fatal("option-like candidate command was accepted")
	}
}

func TestValidateCheckMaterializedSourceRequiresManifestAndSafeModes(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { cleanupPreparedTree(root) })
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.js"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(filepath.Join(root, "src"), 0o555)
	_ = os.Chmod(filepath.Join(root, "src", "main.js"), 0o444)
	if err := validateCheckMaterializedSource(root, 3, 2); err != nil {
		t.Fatalf("safe source rejected: %v", err)
	}
	if err := validateCheckMaterializedSource(root, 4, 2); err == nil {
		t.Fatal("manifest byte mismatch was accepted")
	}
	if err := os.Chmod(filepath.Join(root, "src", "main.js"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateCheckMaterializedSource(root, 3, 2); err == nil {
		t.Fatal("writable source file was accepted")
	}
}

func TestCheckContainerOutputCollectorCapsAndHashesOnlyBoundedOutput(t *testing.T) {
	var stops int
	collector := newCheckContainerOutputCollector(4, func() { stops++ })
	if n, err := collector.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("Write returned n=%d err=%v, want original length and no writer error", n, err)
	}
	if !collector.Exceeded() || collector.String() != "abcd" || stops != 1 {
		t.Fatalf("collector = exceeded=%v output=%q stops=%d", collector.Exceeded(), collector.String(), stops)
	}
	if n, err := collector.Write([]byte("ignored")); err != nil || n != 7 || stops != 1 {
		t.Fatalf("post-cap Write returned n=%d err=%v stops=%d", n, err, stops)
	}
	if collector.SHA256() == "" {
		t.Fatal("bounded output hash is empty")
	}
}

func TestCheckContainerOutputCollectorSupportsConcurrentStreams(t *testing.T) {
	collector := newCheckContainerOutputCollector(4096, nil)
	var writers sync.WaitGroup
	for index := 0; index < 8; index++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			_, _ = collector.Write([]byte(strings.Repeat("x", 256)))
		}()
	}
	writers.Wait()
	if len(collector.String()) > 4096 || collector.SHA256() == "" {
		t.Fatalf("concurrent collector output is invalid: len=%d hash=%q", len(collector.String()), collector.SHA256())
	}
}

func TestRunCheckContainerRejectsInvalidOrOverLimitRequestsBeforeDocker(t *testing.T) {
	result, err := RunCheckContainer(context.Background(), CheckContainerRequest{
		Image: "approved", Argv: []string{"node"}, SourceDir: "/trusted/source",
		SourceBytes: CheckContainerMaxSourceBytes + 1, SourceItems: 1,
		Timeout: time.Second, MemoryBytes: 64 * 1024 * 1024, PidsLimit: 1, OutputLimit: 1,
	})
	if err == nil || !result.InfraError || result.Outcome != CheckContainerInfraError {
		t.Fatalf("invalid request result=%#v err=%v", result, err)
	}
}

func containsString(values []string, want string) bool { return indexOf(values, want) >= 0 }

func indexOf(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}

func indexOfPrefix(values []string, prefix string) int {
	for index, value := range values {
		if strings.HasPrefix(value, prefix) {
			return index
		}
	}
	return -1
}

func findArgPrefix(values []string, prefix string) string {
	index := indexOfPrefix(values, prefix)
	if index < 0 {
		return ""
	}
	return values[index]
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }
