package cleardevlocal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNPMPreparationTimeoutIndependent(t *testing.T) {
	t.Setenv("CLEARDEV_DEPENDENCY_PREPARATION_TIMEOUT", "")
	if d, e := dependencyPreparationTimeout(); e != nil || d != 30*time.Minute {
		t.Fatalf("default %v %v", d, e)
	}
	t.Setenv("CLEARDEV_DEPENDENCY_PREPARATION_TIMEOUT", "2h")
	if d, e := dependencyPreparationTimeout(); e != nil || d != 2*time.Hour {
		t.Fatalf("override %v %v", d, e)
	}
	t.Setenv("CLEARDEV_DEPENDENCY_PREPARATION_TIMEOUT", "bad")
	if _, e := dependencyPreparationTimeout(); e == nil {
		t.Fatal("invalid override accepted")
	}
	if projectNPMDownloadClient().Timeout != 0 {
		t.Fatal("download retained a short wall timeout")
	}
}

func TestNPMDownloadProgressAndRetry(t *testing.T) {
	for _, mode := range []string{"progress", "stall", "transient", "corrupt", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			payload := []byte("1234567890")
			_, artifact := npmArtifactFixture(t, payload)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if mode == "transient" && n < 3 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if mode == "stall" || mode == "cancel" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				if mode == "corrupt" {
					_, _ = w.Write([]byte("bad"))
					return
				}
				if mode == "progress" {
					for _, b := range payload {
						_, _ = w.Write([]byte{b})
						w.(http.Flusher).Flush()
						time.Sleep(20 * time.Millisecond)
					}
					return
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			client := projectNPMDownloadClient()
			client.Transport = localRegistryTransport{target: target}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if mode == "cancel" {
				time.AfterFunc(20*time.Millisecond, cancel)
			}
			remaining := int64(100)
			err := downloadNPMSourceWithRetry(ctx, client, artifact, t.TempDir(), &remaining, 100*time.Millisecond, time.Millisecond)
			switch mode {
			case "progress", "transient":
				if err != nil || remaining != 90 {
					t.Fatalf("download %v remaining %d", err, remaining)
				}
			case "stall":
				if !errors.Is(err, errNPMDownloadIdle) || calls.Load() != 3 {
					t.Fatalf("stall %v calls %d", err, calls.Load())
				}
			case "corrupt":
				if err == nil || calls.Load() != 1 {
					t.Fatalf("checksum retried/accepted: %v calls %d", err, calls.Load())
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
					t.Fatalf("cancel %v calls %d", err, calls.Load())
				}
			}
			if mode == "transient" && calls.Load() != 3 {
				t.Fatal("did not retry")
			}
		})
	}
}

func TestNPMArchiveCacheSurvivesFailedPreparation(t *testing.T) {
	first := []byte("first verified package")
	second := []byte("second verified package")
	_, a := npmArtifactFixture(t, first)
	_, b := npmArtifactFixture(t, second)
	b.URL = "https://registry.npmjs.org/second/-/second-1.tgz"
	var fail atomic.Bool
	fail.Store(true)
	var firstCalls, secondCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/second/-/second-1.tgz" {
			secondCalls.Add(1)
			if fail.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(second)
			return
		}
		firstCalls.Add(1)
		_, _ = w.Write(first)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	client := projectNPMDownloadClient()
	client.Transport = localRegistryTransport{target: target}
	root := t.TempDir()
	run := func() error {
		return downloadCachedNPMArtifacts(context.Background(), client, []lockedNPMArtifact{a, b}, filepath.Join(t.TempDir(), "downloads"), root, 1000, 1000, time.Second, time.Millisecond)
	}
	if err := run(); err == nil {
		t.Fatal("failure accepted")
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 6 {
		t.Fatal("unexpected attempts")
	}
	fail.Store(false)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 7 {
		t.Fatal("verified package was downloaded again")
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 7 {
		t.Fatal("cache did not survive fresh caller/staging")
	}
	// Damaged raw bytes are not trusted. This cache holds downloads, not bound check results.
	if err := os.Chmod(filepath.Join(root, a.Filename), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, a.Filename), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if firstCalls.Load() != 2 {
		t.Fatal("corrupt cached content reused")
	}
	if err := pruneNPMArchives(root, 10, 30); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	var size int64
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tgz" {
			i, _ := e.Info()
			size += i.Size()
		}
	}
	if size+10 > 30 {
		t.Fatal("cache capacity exceeded")
	}
}

func TestNPMArchiveCacheConcurrentAndCancelledWaiter(t *testing.T) {
	payload := []byte("shared archive")
	_, artifact := npmArtifactFixture(t, payload)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			_, _ = w.Write(payload)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	client := projectNPMDownloadClient()
	client.Transport = localRegistryTransport{target: target}
	root := t.TempDir()
	firstDir := filepath.Join(t.TempDir(), "downloads")
	secondDir := filepath.Join(t.TempDir(), "downloads")
	cancelDir := filepath.Join(t.TempDir(), "downloads")
	run := func(ctx context.Context, dir string) error {
		return downloadCachedNPMArtifacts(ctx, client, []lockedNPMArtifact{artifact}, dir, root, 100, 100, time.Second, time.Millisecond)
	}
	done := make(chan error, 2)
	go func() { done <- run(context.Background(), firstDir) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, cancelDir); !errors.Is(err, context.Canceled) {
		close(release)
		t.Fatalf("waiter cancel %v", err)
	}
	go func() { done <- run(context.Background(), secondDir) }()
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate fetch %d", calls.Load())
	}
}

type npmMirrorTestTransport struct{ target *url.URL }

func (tr npmMirrorTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("X-Test-Origin", r.URL.Host)
	clone.URL.Scheme, clone.URL.Host = tr.target.Scheme, tr.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func TestNPMMirrorPreferredAndFallback(t *testing.T) {
	for _, mode := range []string{"success", "missing", "corrupt", "transient"} {
		t.Run(mode, func(t *testing.T) {
			payload := []byte("same locked bytes")
			_, artifact := npmArtifactFixture(t, payload)
			var origins []string
			var originsMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				origin := r.Header.Get("X-Test-Origin")
				originsMu.Lock()
				origins = append(origins, origin)
				originsMu.Unlock()
				if origin == "registry.npmmirror.com" {
					switch mode {
					case "missing":
						w.WriteHeader(http.StatusNotFound)
						return
					case "corrupt":
						_, _ = w.Write([]byte("wrong bytes"))
						return
					case "transient":
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			client := projectNPMDownloadClient()
			client.Transport = npmMirrorTestTransport{target}
			remaining := int64(100)
			if err := downloadNPMWithRetry(context.Background(), client, artifact, t.TempDir(), &remaining, time.Second, time.Millisecond); err != nil {
				t.Fatal(err)
			}
			originsMu.Lock()
			defer originsMu.Unlock()
			if origins[0] != "registry.npmmirror.com" {
				t.Fatalf("not mirror first: %v", origins)
			}
			expected := 1
			if mode == "missing" || mode == "corrupt" {
				expected = 2
			}
			if mode == "transient" {
				expected = 4
			}
			if len(origins) != expected || (expected > 1 && origins[len(origins)-1] != "registry.npmjs.org") {
				t.Fatalf("wrong source order %v", origins)
			}
			if remaining != 100-int64(len(payload)) {
				t.Fatal("failed source consumed retained-file allowance")
			}
		})
	}
	for _, raw := range []string{"https://evil.npmmirror.com/x.tgz", "https://registry.npmmirror.com.evil.test/x.tgz", "http://registry.npmmirror.com/x.tgz", "https://user:secret@registry.npmmirror.com/x.tgz"} {
		if validateNPMTransferURL(raw) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if err := validateNPMTransferURL("https://cdn.npmmirror.com/packages/x/x.tgz"); err != nil {
		t.Fatal(err)
	}
}
