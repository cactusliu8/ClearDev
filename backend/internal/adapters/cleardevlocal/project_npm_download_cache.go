package cleardevlocal

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

const npmArchiveCacheBytes int64 = 10 * 1024 * 1024 * 1024
const dependencyDownloadIdle = 90 * time.Second

func dependencyPreparationTimeout() (time.Duration, error) {
	raw := os.Getenv("CLEARDEV_DEPENDENCY_PREPARATION_TIMEOUT")
	if raw == "" {
		return 30 * time.Minute, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, errors.New("CLEARDEV_DEPENDENCY_PREPARATION_TIMEOUT must be a positive duration, for example 30m or 2h")
	}
	return d, nil
}

var errNPMDownloadIdle = errors.New("dependency download made no progress before idle timeout")

type npmHTTPError int

func (e npmHTTPError) Error() string { return "registry returned HTTP " + strconv.Itoa(int(e)) }

type npmProgressReader struct {
	io.Reader
	timer *time.Timer
	idle  time.Duration
}

func (r npmProgressReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.timer.Reset(r.idle)
	}
	return n, err
}

func retryableNPMDownload(err error) bool {
	var status npmHTTPError
	var network *net.OpError
	var timeout net.Error
	return errors.Is(err, errNPMDownloadIdle) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &network) || (errors.As(err, &timeout) && timeout.Timeout()) ||
		(errors.As(err, &status) && (status == 408 || status == 429 || status >= 500))
}

// Only completed, digest-verified archives survive an attempt. Partial files
// remain in the disposable staging area, never at a persistent cache key.
func downloadNPMWithRetry(ctx context.Context, client *http.Client, artifact lockedNPMArtifact, directory string, remaining *int64, idle, backoff time.Duration) error {
	if err := validateNPMArtifactURL(artifact.URL); err != nil {
		return err
	}
	mirror, _ := url.Parse(artifact.URL)
	mirror.Host = "registry.npmmirror.com"
	var failures []error
	for _, source := range []string{mirror.String(), artifact.URL} {
		item := artifact
		item.URL = source
		err := downloadNPMSourceWithRetry(ctx, client, item, directory, remaining, idle, backoff)
		if err == nil {
			return nil
		}
		failures = append(failures, err)
		if ctx.Err() != nil || errors.Is(err, errCheckCapacityExceeded) {
			break
		}
	}
	return errors.Join(failures...)
}

func downloadNPMSourceWithRetry(ctx context.Context, client *http.Client, artifact lockedNPMArtifact, directory string, remaining *int64, idle, backoff time.Duration) error {
	for attempt := 1; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		available := *remaining
		attemptCtx, cancel := context.WithCancelCause(ctx)
		timer := time.AfterFunc(idle, func() { cancel(errNPMDownloadIdle) })
		err := downloadLockedNPMArtifact(attemptCtx, client, artifact, directory, &available, timer, idle)
		timer.Stop()
		cause := context.Cause(attemptCtx)
		cancel(nil)
		if err == nil {
			*remaining = available
			return nil
		}
		if cause != nil {
			err = cause
		}
		if removeErr := os.Remove(filepath.Join(directory, artifact.Filename)); removeErr != nil && !os.IsNotExist(removeErr) {
			return errors.Join(err, removeErr)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == 3 || !retryableNPMDownload(err) {
			return fmt.Errorf("download %s attempt %d/3: %w", artifact.URL, attempt, err)
		}
		wait := time.NewTimer(time.Duration(attempt) * backoff)
		select {
		case <-ctx.Done():
			wait.Stop()
			return ctx.Err()
		case <-wait.C:
		}
	}
	return nil
}

func npmArchiveCacheRoot() (string, error) {
	root, err := checkTemporaryRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(root), "cleardev-npm-archives"), nil
}

func copyNPMArchive(ctx context.Context, source, destination string, limit int64, integrity string) (int64, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return 0, errors.New("invalid or oversized npm archive cache entry")
	}
	in, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() { _ = out.Close() }()
	hash := sha512.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(in, limit+1))
	if err == nil && (n > limit || "sha512-"+base64.StdEncoding.EncodeToString(hash.Sum(nil)) != integrity) {
		err = errors.New("npm archive cache SHA512 mismatch")
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = out.Sync()
	}
	if err == nil {
		err = out.Chmod(0o444)
	}
	return n, err
}

func pruneNPMArchives(root string, incoming, limit int64) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	type entry struct {
		name     string
		size     int64
		modified time.Time
	}
	var files []entry
	var total int64
	for _, e := range entries {
		if e.Name() == ".lock" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("invalid npm archive cache entry")
		}
		if filepath.Ext(e.Name()) == ".tmp" {
			if err := os.Remove(filepath.Join(root, e.Name())); err != nil {
				return err
			}
			continue
		}
		if filepath.Ext(e.Name()) != ".tgz" {
			return errors.New("unexpected npm archive cache file")
		}
		files = append(files, entry{e.Name(), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
	count := len(files)
	for _, f := range files {
		if total+incoming <= limit && count < 4096 {
			break
		}
		if err := os.Remove(filepath.Join(root, f.name)); err != nil {
			return err
		}
		total -= f.size
		count--
	}
	return nil
}

func downloadCachedNPMArtifacts(ctx context.Context, client *http.Client, artifacts []lockedNPMArtifact, directory, root string, maxBytes, cacheLimit int64, idle, backoff time.Duration) error {
	if maxBytes <= 0 || len(artifacts) > int(productionCheckCapacityLimits().Dependency.Items)-4 {
		return fmt.Errorf("%w: dependency download allowance is exhausted", errCheckCapacityExceeded)
	}
	if err := os.Mkdir(directory, 0o755); err != nil { //nolint:gosec // verified public archives must be readable by the isolated installer.
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid npm archive cache root")
	}
	unlock, err := lockCheckFile(ctx, filepath.Join(root, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err := pruneNPMArchives(root, 0, cacheLimit); err != nil {
		return err
	}
	remaining := maxBytes
	for i, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("prepare npm archive %d/%d: %w", i+1, len(artifacts), err)
		}
		if err := validateNPMArtifactURL(artifact.URL); err != nil {
			return err
		}
		source := filepath.Join(root, artifact.Filename)
		destination := filepath.Join(directory, artifact.Filename)
		if info, err := os.Lstat(source); err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("redirected npm archive cache entry")
			}
			n, copyErr := copyNPMArchive(ctx, source, destination, remaining, artifact.Integrity)
			if copyErr == nil {
				remaining -= n
				_ = os.Chtimes(source, time.Now(), time.Now())
				continue
			}
			if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := os.Remove(source); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := downloadNPMWithRetry(ctx, client, artifact, directory, &remaining, idle, backoff); err != nil {
			return fmt.Errorf("prepare npm archive %d/%d: %w", i+1, len(artifacts), err)
		}
		info, err := os.Stat(destination)
		if err != nil {
			return err
		}
		if info.Size() > cacheLimit {
			continue
		}
		if err := pruneNPMArchives(root, info.Size(), cacheLimit); err != nil {
			return err
		}
		temp := source + ".tmp"
		if _, err := copyNPMArchive(ctx, destination, temp, info.Size(), artifact.Integrity); err != nil {
			_ = os.Remove(temp)
			return err
		}
		if err := os.Rename(temp, source); err != nil {
			return err
		}
	}
	return nil
}
