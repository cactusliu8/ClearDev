package cleardevlocal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProjectDownloadRetriesMirrorAndWarmCache(t *testing.T) {
	data := []byte("verified archive bytes")
	item := projectDownload{SHA256: sha256Hex(data)}
	var original, mirror atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/original" {
			original.Add(1)
			http.Error(w, "temporary unavailable", http.StatusServiceUnavailable)
			return
		}
		if mirror.Add(1) < 2 {
			http.Error(w, "temporary unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	root := t.TempDir()
	var report bytes.Buffer
	got, err := cachedProjectArchive(context.Background(), root, item, server.Client(), []string{server.URL + "/original", server.URL + "/mirror"}, &report)
	if err != nil || !bytes.Equal(got, data) || original.Load() != 3 || mirror.Load() != 2 {
		t.Fatalf("fallback counts %d/%d: %s %v", original.Load(), mirror.Load(), report.String(), err)
	}
	server.Close()
	// A new caller after network shutdown must use independently checked bytes.
	got, err = cachedProjectArchive(context.Background(), root, item, server.Client(), []string{server.URL}, io.Discard)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("offline cache: %v", err)
	}
	if original.Load() != 3 || mirror.Load() != 2 {
		t.Fatal("warm read made a network request")
	}
}

func TestProjectDownloadRejectsCorruptionAndHonorsCancellation(t *testing.T) {
	good := []byte("good")
	item := projectDownload{SHA256: sha256Hex(good)}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = w.Write([]byte("wrong")) }))
	defer server.Close()
	root := t.TempDir()
	path := filepath.Join(root, item.SHA256+".tar.gz")
	if err := os.WriteFile(path, []byte("corrupt cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := cachedProjectArchive(context.Background(), root, item, server.Client(), []string{server.URL}, io.Discard)
	if err == nil || requests.Load() != 3 {
		t.Fatalf("corrupt archive accepted: %d %v", requests.Load(), err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("bad cache published")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = cachedProjectArchive(ctx, root, item, server.Client(), []string{server.URL}, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if requests.Load() != 3 {
		t.Fatal("canceled operation sent request")
	}
}

func TestProjectDownloadConcurrentPublication(t *testing.T) {
	data := []byte("same")
	item := projectDownload{SHA256: sha256Hex(data)}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = w.Write(data) }))
	defer server.Close()
	root := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := cachedProjectArchive(context.Background(), root, item, server.Client(), []string{server.URL}, io.Discard)
			if err == nil && sha256Hex(data) != item.SHA256 {
				err = errors.New("corrupt bytes")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("duplicate downloads: %d", requests.Load())
	}
}

func TestProjectDownloadSizeAndIncompleteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer server.Close()
	if _, err := fetchProjectArchive(context.Background(), server.Client(), []string{server.URL}, sha256Hex([]byte("short")), 20, io.Discard); err == nil {
		t.Fatal("partial body accepted")
	}
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("oversize")) }))
	defer server2.Close()
	if _, err := fetchProjectArchive(context.Background(), server2.Client(), []string{server2.URL}, sha256Hex([]byte("oversize")), 3, io.Discard); err == nil {
		t.Fatal("oversize accepted")
	}
}

func TestProjectDownloadManifestAndMirrorPolicy(t *testing.T) {
	source, deps := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(deps, "better-sqlite3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, ".cleardev"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(deps, "better-sqlite3", "package.json"), `{"name":"better-sqlite3","version":"12.9.0"}`)
	manifest := `[{"package":"better-sqlite3","packageVersion":"12.9.0","archiveUrl":"https://github.com/WiseLibs/better-sqlite3/releases/download/v12.9.0/driver.tar.gz","archiveSha256":"` + strings.Repeat("a", 64) + `"}]`
	writeTestFile(t, filepath.Join(source, ".cleardev", "downloads.json"), manifest)
	items, err := readProjectDownloads(source, deps)
	if err != nil || len(items) != 1 {
		t.Fatal(err)
	}
	if sources := projectDownloadSources(items[0]); len(sources) != 2 || !strings.Contains(sources[0], "registry.npmmirror.com") {
		t.Fatal(sources)
	}
	for _, bad := range []string{strings.Replace(manifest, "https://github.com", "http://127.0.0.1", 1), strings.Replace(manifest, `"packageVersion":"12.9.0"`, `"packageVersion":"13.0.0"`, 1), strings.Replace(manifest, `"package":"better-sqlite3"`, `"package":"../escape"`, 1)} {
		writeTestFile(t, filepath.Join(source, ".cleardev", "downloads.json"), bad)
		if _, err := readProjectDownloads(source, deps); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
}

func TestProjectDownloadWaitCancellation(t *testing.T) {
	root := t.TempDir()
	unlock, err := lockCheckFile(context.Background(), filepath.Join(root, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = cachedProjectArchive(ctx, root, projectDownload{SHA256: strings.Repeat("a", 64)}, http.DefaultClient, nil, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait ignored deadline: %v", err)
	}
}

func TestProjectDownloadCacheEviction(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, strings.Repeat("0", 64)+".tar.gz")
	f, err := os.Create(old)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(projectArchiveCacheLimit); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	data := []byte("new")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	if _, err := cachedProjectArchive(context.Background(), root, projectDownload{SHA256: sha256Hex(data)}, server.Client(), []string{server.URL}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("capacity was not reclaimed")
	}
}
