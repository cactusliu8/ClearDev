package cleardevlocal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// FinalReviewEvidencePath locates the single immutable file for a frozen review.
func (r *Runner) FinalReviewEvidencePath(ctx context.Context, id string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validRunID(id) {
		return "", errors.New("invalid final review evidence identity")
	}
	path, err := durableRunStatePath("cleardev-review-packets", id, "review evidence")
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) || resolved != dir {
		return "", errors.New("review evidence directory must not contain symlinks")
	}
	return path, nil
}

// WriteFinalReviewEvidence publishes once and verifies exact bytes on every reuse.
func (r *Runner) WriteFinalReviewEvidence(ctx context.Context, id, path, packet, digest string) error {
	expected, err := r.FinalReviewEvidencePath(ctx, id)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(packet))
	if path != expected || hex.EncodeToString(sum[:]) != digest {
		return errors.New("final review evidence binding mismatch")
	}
	if len(packet) > 32*1024*1024 {
		return errors.New("final review evidence exceeds supported file size")
	}
	dir, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	name := filepath.Base(path)
	verify := func() error {
		info, statErr := dir.Lstat(name)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 || info.Mode()&os.ModeSymlink != 0 || info.Size() != int64(len(packet)) {
			return errors.New("final review evidence file changed")
		}
		data, readErr := dir.ReadFile(name)
		if readErr != nil {
			return readErr
		}
		if string(data) != packet {
			return errors.New("final review evidence content changed")
		}
		return nil
	}
	if _, err = dir.Lstat(name); err == nil {
		return verify()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := ".packet-" + rand.Text()
	temp, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Remove(tmp) }()
	if _, err = temp.WriteString(packet); err == nil {
		err = temp.Chmod(0o400)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err = dir.Link(tmp, name); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return verify()
}
