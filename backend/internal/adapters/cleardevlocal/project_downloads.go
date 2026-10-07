package cleardevlocal

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

const projectInstallImage = core.ProjectCandidateCheckImage
const projectArchiveLimit int64 = 32 * 1024 * 1024
const projectArchiveCacheLimit int64 = 256 * 1024 * 1024

// Only raw, candidate-checksummed downloads cross installations. No script or
// generated node_modules tree is ever promoted back into this cache.
type projectDownload struct {
	Package string `json:"package"`
	Version string `json:"packageVersion"`
	URL     string `json:"archiveUrl"`
	SHA256  string `json:"archiveSha256"`
}

var projectReleaseURL = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/releases/download/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+\.tar\.gz)$`)
var projectPackageName = regexp.MustCompile(`^(@[a-z0-9_.-]+/)?[a-z0-9_.-]+$`)

func readProjectDownloadFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("dependency download metadata exceeds limit")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("dependency download metadata exceeds limit")
	}
	return data, err
}

func readProjectDownloads(source, dependencies string) ([]projectDownload, error) {
	var items []projectDownload
	data, err := readProjectDownloadFile(filepath.Join(source, ".cleardev", "downloads.json"), 64*1024)
	if err == nil {
		if len(data) > 64*1024 {
			return nil, errors.New("dependency download manifest too large")
		}
		if err = json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("dependency download manifest: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		data, err = readProjectDownloadFile(filepath.Join(source, "scripts", "vendor", "manifest.json"), 64*1024)
		if err == nil {
			if len(data) > 64*1024 {
				return nil, errors.New("vendor download manifest too large")
			}
			var item projectDownload
			if json.Unmarshal(data, &item) == nil && item.URL != "" {
				items = []projectDownload{item}
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if len(items) > 8 {
		return nil, errors.New("at most eight dependency downloads are supported")
	}
	seen := map[string]bool{}
	for _, item := range items {
		digest, e := hex.DecodeString(item.SHA256)
		if !projectPackageName.MatchString(item.Package) || strings.Contains(item.Package, "..") || item.Version == "" || len(digest) != 32 || e != nil || strings.ToLower(item.SHA256) != item.SHA256 || !projectReleaseURL.MatchString(item.URL) || seen[item.URL] {
			return nil, errors.New("invalid pinned dependency download")
		}
		seen[item.URL] = true
		if dependencies == "" {
			continue
		} // The fresh installer validates the installed manifest inside its own container.
		// Read only the sealed dependency tree, never a Builder's working dependency.
		raw, e := readProjectDownloadFile(filepath.Join(dependencies, filepath.FromSlash(item.Package), "package.json"), 1024*1024)
		if e != nil {
			return nil, fmt.Errorf("download package missing from dependency template: %w", e)
		}
		var pkg struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if len(raw) > 1024*1024 || json.Unmarshal(raw, &pkg) != nil || pkg.Name != item.Package || pkg.Version != item.Version {
			return nil, errors.New("download package version differs from template")
		}
	}
	return items, nil
}

func projectDownloadSources(item projectDownload) []string {
	sources := []string{item.URL}
	parts := projectReleaseURL.FindStringSubmatch(item.URL)
	if len(parts) == 5 && strings.EqualFold(parts[1], "WiseLibs") && parts[2] == "better-sqlite3" && item.Package == "better-sqlite3" && parts[3] == "v"+item.Version {
		sources = append([]string{"https://registry.npmmirror.com/-/binary/better-sqlite3/" + parts[3] + "/" + parts[4]}, sources...)
	}
	return sources
}

func projectDownloadClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		h := req.URL.Hostname()
		approved := h == "github.com" || h == "release-assets.githubusercontent.com" || h == "objects.githubusercontent.com" || h == "registry.npmmirror.com" || h == "cdn.npmmirror.com" || strings.HasSuffix(h, ".npmmirror.com")
		if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil || !approved {
			return errors.New("dependency redirect outside approved HTTPS sources")
		}
		return nil
	}}
}

func fetchProjectArchive(ctx context.Context, client *http.Client, sources []string, digest string, limit int64, report io.Writer) ([]byte, error) {
	var last error
	for _, source := range sources {
		for attempt := 1; attempt <= 3; attempt++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			_, _ = fmt.Fprintf(report, "dependency download source=%s attempt=%d/3\n", source, attempt)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, http.NoBody)
			if err != nil {
				return nil, err
			}
			response, err := client.Do(req)
			if err == nil {
				var data []byte
				if response.StatusCode != http.StatusOK {
					err = fmt.Errorf("HTTP %d", response.StatusCode)
				} else {
					data, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
				}
				_ = response.Body.Close()
				if err == nil && int64(len(data)) > limit {
					err = errors.New("dependency archive exceeds limit")
				}
				if err == nil && sha256Hex(data) != digest {
					err = errors.New("dependency archive checksum mismatch")
				}
				if err == nil {
					return data, nil
				}
			}
			// Do not print transport URLs/credentials from error strings.
			last = err
			_, _ = fmt.Fprintln(report, "dependency download failed; no unverified archive accepted")
			if attempt < 3 {
				timer := time.NewTimer(time.Duration(attempt) * 200 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
			}
		}
	}
	return nil, fmt.Errorf("dependency download sources exhausted: %w", last)
}

func cachedProjectArchive(ctx context.Context, root string, item projectDownload, client *http.Client, sources []string, report io.Writer) ([]byte, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid download cache root")
	}
	unlock, err := lockCheckFile(ctx, filepath.Join(root, ".lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	destination := filepath.Join(root, item.SHA256+".tar.gz")
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("redirected dependency archive cache")
		}
		if info.Size() <= projectArchiveLimit {
			data, e := os.ReadFile(destination)
			if e == nil && sha256Hex(data) == item.SHA256 {
				_, _ = fmt.Fprintln(report, "dependency download cache hit (SHA256 verified)")
				return data, nil
			}
		}
		if err := os.Remove(destination); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Serialize fetch and publication: waiting callers re-check the published
	// archive, and never receive another container's mutable cache directory.
	data, err := fetchProjectArchive(ctx, client, sources, item.SHA256, projectArchiveLimit, report)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var total int64
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			_ = os.Remove(filepath.Join(root, e.Name()))
			continue
		}
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			v, err := e.Info()
			if err != nil {
				return nil, err
			}
			if !v.Mode().IsRegular() {
				return nil, errors.New("invalid download cache entry")
			}
			total += v.Size()
			count++
		}
	}
	for _, e := range entries {
		if total+int64(len(data)) <= projectArchiveCacheLimit && count < 128 {
			break
		}
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			v, err := e.Info()
			if err != nil {
				return nil, err
			}
			if err := os.Remove(filepath.Join(root, e.Name())); err != nil {
				return nil, err
			}
			total -= v.Size()
			count--
		}
	}
	f, err := os.CreateTemp(root, "archive-*.tmp")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(name, destination); err != nil {
		return nil, err
	}
	return data, nil
}

// seedProjectDownloads produces only private files for docker cp. Installation
// receives no host-writable cache mount and can overwrite only its own copy.
func seedProjectDownloads(ctx context.Context, source, dependencies, root string, report io.Writer) (string, error) {
	items, err := readProjectDownloads(source, dependencies)
	if err != nil || len(items) == 0 {
		return "", err
	}
	temporaryRoot, err := checkTemporaryRoot()
	if err != nil {
		return "", err
	}
	cache := filepath.Join(filepath.Dir(temporaryRoot), "cleardev-download-archives")
	destination := filepath.Join(root, "download-seed", "_prebuilds")
	if err := os.MkdirAll(destination, 0o755); err != nil { //nolint:gosec // private temporary parent; non-root installer must read the raw archive seed.
		return "", err
	}
	downloadCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for _, item := range items {
		data, err := cachedProjectArchive(downloadCtx, cache, item, projectDownloadClient(), projectDownloadSources(item), report)
		if err != nil {
			return "", err
		}
		parsed, _ := url.Parse(item.URL)
		key := sha512.Sum512([]byte(item.URL))
		name := hex.EncodeToString(key[:])[:6] + "-" + regexp.MustCompile(`[^a-zA-Z0-9.]+`).ReplaceAllString(filepath.Base(parsed.Path), "-")
		if err := os.WriteFile(filepath.Join(destination, name), data, 0o644); err != nil { //nolint:gosec // public checksummed archive, copied into the non-root installer.
			return "", err
		}
	}
	return filepath.Dir(destination), nil
}
