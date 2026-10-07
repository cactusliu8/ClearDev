package cleardevlocal

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	checkSourceBytesLimit          int64 = 1024 * 1024 * 1024
	checkSourceItemsLimit          int64 = 100_000
	checkDependencyBytesLimit      int64 = 10 * 1024 * 1024 * 1024
	checkDependencyItemsLimit      int64 = 100_000
	checkOutputBytesLimit          int64 = 256 * 1024 * 1024
	checkOutputItemsLimit          int64 = 100_000
	checkActiveTemporaryBytesLimit int64 = 32 * 1024 * 1024 * 1024
	checkPersistentBytesLimit      int64 = 2 * 1024 * 1024 * 1024
)

var errCheckCapacityExceeded = errors.New("ClearDev check capacity exceeded")

// checkUsageLimit is passed by value so focused tests can use small limits
// without changing process-wide production settings.
type checkUsageLimit struct {
	Bytes int64
	Items int64
}

type checkCapacityLimits struct {
	Source               checkUsageLimit
	Dependency           checkUsageLimit
	Output               checkUsageLimit
	ActiveTemporaryBytes int64
	PersistentBytes      int64
}

func productionCheckCapacityLimits() checkCapacityLimits {
	return checkCapacityLimits{
		Source:               checkUsageLimit{Bytes: checkSourceBytesLimit, Items: checkSourceItemsLimit},
		Dependency:           checkUsageLimit{Bytes: checkDependencyBytesLimit, Items: checkDependencyItemsLimit},
		Output:               checkUsageLimit{Bytes: checkOutputBytesLimit, Items: checkOutputItemsLimit},
		ActiveTemporaryBytes: checkActiveTemporaryBytesLimit,
		PersistentBytes:      checkPersistentBytesLimit,
	}
}

// checkUsage charges logical bytes for regular files. Directories, regular
// files, and symbolic links each charge one item. The extraction root is not
// an item.
type checkUsage struct {
	Bytes int64
	Items int64
}

type checkUsageCounter struct {
	limit checkUsageLimit
	usage checkUsage
}

func newCheckUsageCounter(limit checkUsageLimit) (*checkUsageCounter, error) {
	if limit.Bytes < 0 || limit.Items < 0 {
		return nil, errors.New("ClearDev check usage limit cannot be negative")
	}
	return &checkUsageCounter{limit: limit}, nil
}

func (c *checkUsageCounter) Usage() checkUsage {
	if c == nil {
		return checkUsage{}
	}
	return c.usage
}

func (c *checkUsageCounter) ChargeDirectory() error {
	return c.charge(0, 1)
}

func (c *checkUsageCounter) ChargeRegularFile(size int64) error {
	if size < 0 {
		return errors.New("ClearDev check regular file size cannot be negative")
	}
	return c.charge(size, 1)
}

func (c *checkUsageCounter) ChargeSymlink() error {
	return c.charge(0, 1)
}

func (c *checkUsageCounter) charge(bytes, items int64) error {
	if c == nil {
		return errors.New("ClearDev check usage counter is unavailable")
	}
	if bytes < 0 || items < 0 {
		return errors.New("ClearDev check usage charge cannot be negative")
	}
	if bytes > c.limit.Bytes-c.usage.Bytes {
		return fmt.Errorf("%w: logical bytes would exceed %d", errCheckCapacityExceeded, c.limit.Bytes)
	}
	if items > c.limit.Items-c.usage.Items {
		return fmt.Errorf("%w: item count would exceed %d", errCheckCapacityExceeded, c.limit.Items)
	}
	c.usage.Bytes += bytes
	c.usage.Items += items
	return nil
}

// extractCheckTar extracts a dependency archive into a trusted empty
// directory. destination is also the allowed node_modules root for symlink
// targets. Capacity is charged before every filesystem creation or file write.
func extractCheckTar(reader *tar.Reader, destination string, counter *checkUsageCounter) error {
	if reader == nil || counter == nil {
		return errors.New("ClearDev check tar extractor is unavailable")
	}
	root, err := validateEmptyCheckTarDestination(destination)
	if err != nil {
		return err
	}

	seen := make(map[string]struct{})
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			return nil
		}
		if nextErr != nil {
			return fmt.Errorf("read ClearDev check archive: %w", nextErr)
		}

		relative, rootEntry, cleanErr := cleanCheckTarPath(header.Name)
		if cleanErr != nil {
			return cleanErr
		}
		if _, duplicate := seen[relative]; duplicate {
			return fmt.Errorf("ClearDev check archive contains duplicate path %q", relative)
		}
		seen[relative] = struct{}{}
		if rootEntry {
			if header.Typeflag != tar.TypeDir || header.Size != 0 {
				return errors.New("ClearDev check archive root entry is not a directory")
			}
			continue
		}

		if err := ensureCheckTarParents(root, relative, counter); err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(relative))
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return fmt.Errorf("ClearDev check archive directory %q has content", relative)
			}
			if err := createCheckTarDirectory(target, counter); err != nil {
				return fmt.Errorf("extract ClearDev check directory %q: %w", relative, err)
			}
		case tar.TypeReg, 0: // A zero type flag is the legacy tar regular-file marker.
			if err := extractCheckTarRegularFile(reader, target, relative, header, counter); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if header.Size != 0 {
				return fmt.Errorf("ClearDev check archive symlink %q has content", relative)
			}
			if err := validateCheckTarSymlink(relative, header.Linkname); err != nil {
				return err
			}
			if err := requireMissingCheckTarTarget(target); err != nil {
				return fmt.Errorf("extract ClearDev check symlink %q: %w", relative, err)
			}
			if err := counter.ChargeSymlink(); err != nil {
				return fmt.Errorf("extract ClearDev check symlink %q: %w", relative, err)
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("extract ClearDev check symlink %q: %w", relative, err)
			}
		case tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			return fmt.Errorf("ClearDev check archive path %q has forbidden type %d", relative, header.Typeflag)
		default:
			return fmt.Errorf("ClearDev check archive path %q has unsupported type %d", relative, header.Typeflag)
		}
	}
}

func validateEmptyCheckTarDestination(destination string) (string, error) {
	root, err := filepath.Abs(filepath.Clean(strings.TrimSpace(destination)))
	if err != nil || strings.TrimSpace(destination) == "" || filepath.Clean(strings.TrimSpace(destination)) == "." {
		return "", errors.New("invalid ClearDev check archive destination")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect ClearDev check archive destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("ClearDev check archive destination is not a trusted directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read ClearDev check archive destination: %w", err)
	}
	if len(entries) != 0 {
		return "", errors.New("ClearDev check archive destination is not empty")
	}
	return root, nil
}

func cleanCheckTarPath(name string) (relative string, root bool, err error) {
	if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') || strings.ContainsRune(name, '\\') {
		return "", false, errors.New("ClearDev check archive contains an invalid path")
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return "", false, errors.New("ClearDev check archive contains an invalid path")
		}
	}
	if path.IsAbs(name) {
		return "", false, fmt.Errorf("ClearDev check archive path %q is absolute", name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".." {
			return "", false, fmt.Errorf("ClearDev check archive path %q contains parent traversal", name)
		}
	}
	relative = path.Clean(name)
	if relative == "." {
		return relative, true, nil
	}
	if relative == ".." || strings.HasPrefix(relative, "../") {
		return "", false, fmt.Errorf("ClearDev check archive path %q escapes its root", name)
	}
	native := filepath.FromSlash(relative)
	if filepath.IsAbs(native) || filepath.VolumeName(native) != "" {
		return "", false, fmt.Errorf("ClearDev check archive path %q is absolute", name)
	}
	return relative, false, nil
}

func ensureCheckTarParents(root, relative string, counter *checkUsageCounter) error {
	parent := path.Dir(relative)
	if parent == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(parent, "/") {
		current = filepath.Join(current, filepath.FromSlash(component))
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("ClearDev check archive parent %q is not a directory", component)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect ClearDev check archive parent %q: %w", component, err)
		}
		if err := counter.ChargeDirectory(); err != nil {
			return fmt.Errorf("create ClearDev check archive parent %q: %w", component, err)
		}
		if err := os.Mkdir(current, 0o700); err != nil {
			return fmt.Errorf("create ClearDev check archive parent %q: %w", component, err)
		}
	}
	return nil
}

func createCheckTarDirectory(target string, counter *checkUsageCounter) error {
	info, err := os.Lstat(target)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("path already exists with another type")
		}
		// A preceding child may have caused this parent to be created and
		// charged already. Its later explicit tar header is not a second item.
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := counter.ChargeDirectory(); err != nil {
		return err
	}
	return os.Mkdir(target, 0o700)
}

func extractCheckTarRegularFile(reader *tar.Reader, target, relative string, header *tar.Header, counter *checkUsageCounter) error {
	if header.Size < 0 {
		return fmt.Errorf("ClearDev check archive file %q has a negative size", relative)
	}
	if err := requireMissingCheckTarTarget(target); err != nil {
		return fmt.Errorf("create ClearDev check file %q: %w", relative, err)
	}
	if err := counter.ChargeRegularFile(header.Size); err != nil {
		return fmt.Errorf("extract ClearDev check file %q: %w", relative, err)
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create ClearDev check file %q: %w", relative, err)
	}
	_, copyErr := io.CopyN(file, reader, header.Size)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("write ClearDev check file %q: %w", relative, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close ClearDev check file %q: %w", relative, closeErr)
	}
	mode := os.FileMode(0o644)
	if header.FileInfo().Mode().Perm()&0o111 != 0 {
		mode = 0o755
	}
	if err := os.Chmod(target, mode); err != nil {
		return fmt.Errorf("set ClearDev check file mode %q: %w", relative, err)
	}
	return nil
}

func requireMissingCheckTarTarget(target string) error {
	_, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("path already exists")
}

func validateCheckTarSymlink(relative, linkname string) error {
	if linkname == "" || !utf8.ValidString(linkname) || strings.ContainsRune(linkname, '\x00') || strings.ContainsRune(linkname, '\\') {
		return fmt.Errorf("ClearDev check archive symlink %q has an invalid target", relative)
	}
	for _, character := range linkname {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("ClearDev check archive symlink %q has an invalid target", relative)
		}
	}
	if path.IsAbs(linkname) {
		return fmt.Errorf("ClearDev check archive symlink %q has an absolute target", relative)
	}
	native := filepath.FromSlash(linkname)
	if filepath.IsAbs(native) || filepath.VolumeName(native) != "" {
		return fmt.Errorf("ClearDev check archive symlink %q has an absolute target", relative)
	}
	resolved := path.Clean(path.Join(path.Dir(relative), linkname))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("ClearDev check archive symlink %q escapes node_modules", relative)
	}
	return nil
}
