package processdiagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const diagnosticLimit = 1024 * 1024
const diagnosticLineLimit = 16 * 1024
const diagnosticRetainedRuns = 8

// Capture retains local diagnostics, never execution or recovery authority.
// Keep the last two MiB per process, and eight completed processes per session.
type Capture struct {
	mu          sync.Mutex
	dir         string
	file        *os.File
	size        int
	stderrBytes uint64
	line        []byte
	oversized   bool
	pem         bool
	closed      bool
	writeErr    error
	redact      func(string) string
	log         *slog.Logger
}

var secretField = regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|passwd|secret|authorization|cookie)["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;&}]+)`)
var bearerValue = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
var urlCredential = regexp.MustCompile(`(https?://)[^\s/@]+:[^\s/@]+@`)

func diagnosticRedactor(env []string) func(string) string {
	values := map[string]bool{}
	secretKey := func(k string) bool {
		k = strings.ToLower(k)
		return strings.Contains(k, "token") || strings.Contains(k, "secret") || strings.Contains(k, "password") || strings.Contains(k, "api_key") || strings.Contains(k, "apikey") || strings.Contains(k, "authorization")
	}
	var collect func(any)
	collect = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, value := range x {
				if str, ok := value.(string); ok && secretKey(k) && len(str) >= 4 {
					values[str] = true
				}
				collect(value)
			}
		case []any:
			for _, value := range x {
				collect(value)
			}
		}
	}
	for _, item := range env {
		k, v, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if secretKey(k) && len(v) >= 4 {
			values[v] = true
		}
		if strings.Contains(strings.ToLower(k), "config") {
			var obj any
			if json.Unmarshal([]byte(v), &obj) == nil {
				collect(obj)
			}
		}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	return func(s string) string {
		for _, k := range keys {
			s = strings.ReplaceAll(s, k, "[redacted]")
		}
		s = bearerValue.ReplaceAllString(s, "$1 [redacted]")
		s = urlCredential.ReplaceAllString(s, "${1}[redacted]@")
		return secretField.ReplaceAllString(s, "${1}[redacted]")
	}
}

// New opens a private, bounded capture under the original AO data directory.
func New(dataDir, provider, session string, env []string, log *slog.Logger) *Capture {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	d := &Capture{redact: diagnosticRedactor(env), log: log}
	if !filepath.IsAbs(dataDir) || session == "" || (provider != "acp" && provider != "codex") {
		d.fail(errors.New("worker diagnostic data directory or session is unavailable"))
		return d
	}
	sum := sha256.Sum256([]byte(session))
	parent := dataDir
	for _, name := range []string{"diagnostics", provider, hex.EncodeToString(sum[:])} {
		parent = filepath.Join(parent, name)
		if err := os.Mkdir(parent, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			d.fail(err)
			return d
		}
		info, err := os.Lstat(parent)
		if err != nil {
			d.fail(err)
			return d
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			d.fail(errors.New("worker diagnostic directory is not a private directory"))
			return d
		}
		// Directory traversal requires owner execute; no group/other access.
		if err := os.Chmod(parent, 0o700); //nolint:gosec // G302: this is a verified directory, not a regular file.
		err != nil {
			d.fail(err)
			return d
		}
	}
	dir, err := os.MkdirTemp(parent, "run-")
	if err != nil {
		d.fail(err)
		return d
	}
	d.dir = dir
	file, err := os.OpenFile(filepath.Join(dir, "stderr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		d.fail(err)
		return d
	}
	d.file = file
	d.Record("process", "session="+session+"; stderr capture started")
	d.log.Info("worker diagnostic capture started", "session_id", session, "diagnostic_path", dir)
	pruneDiagnostics(parent)
	return d
}

func (d *Capture) fail(err error) {
	if d.writeErr != nil {
		return
	}
	d.writeErr = err
	// No stderr, environment or error-supplied text is copied into daemon logs.
	d.log.Warn("worker diagnostic capture unavailable", "diagnostic_path", d.dir)
}

// Write always drains the subprocess, including after a full disk or an overlong
// line. A fragmented credential is processed only after the complete line arrives.
func (d *Capture) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stderrBytes += uint64(len(p))
	for _, b := range p {
		if b == '\n' {
			d.flushLine()
			continue
		}
		if !d.oversized {
			if len(d.line) >= diagnosticLineLimit {
				d.line = nil
				d.oversized = true
			} else {
				d.line = append(d.line, b)
			}
		}
	}
	return len(p), nil
}
func (d *Capture) flushLine() {
	if d.oversized {
		d.writeLine("stderr", "[overlong line omitted]")
		d.oversized = false
		d.line = nil
		return
	}
	text := strings.TrimSuffix(string(d.line), "\r")
	d.line = nil
	if strings.Contains(text, "-----BEGIN ") {
		d.pem = true
	}
	if d.pem {
		if strings.Contains(text, "-----END ") {
			d.pem = false
		}
		text = "[private key block omitted]"
	}
	d.writeLine("stderr", text)
}
func (d *Capture) writeLine(kind, text string) {
	if d.writeErr != nil || d.dir == "" {
		return
	}
	if d.file == nil && d.closed {
		// A transport failure can arrive just after OS wait. Append its correlation
		// to the closed process log instead of losing this final diagnostic.
		f, err := os.OpenFile(filepath.Join(d.dir, "stderr.log"), os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			d.fail(err)
			return
		}
		d.file = f
		defer func() {
			if d.file != nil {
				if err := d.file.Close(); err != nil {
					d.fail(err)
				}
				d.file = nil
			}
		}()
	}
	if d.file == nil {
		return
	}
	text = d.redact(text)
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r", "\\r"), "\n", "\\n")
	text = strings.Map(func(r rune) rune {
		if r < 32 && r != '\t' {
			return -1
		}
		return r
	}, text)
	if len(text) > diagnosticLineLimit {
		text = text[:diagnosticLineLimit] + " [truncated]"
	}
	data := []byte(time.Now().UTC().Format(time.RFC3339Nano) + " " + kind + " " + text + "\n")
	if d.size+len(data) > diagnosticLimit {
		if err := d.file.Close(); err != nil {
			d.fail(err)
			return
		}
		d.file = nil
		previous := filepath.Join(d.dir, "stderr.previous.log")
		if err := os.Remove(previous); err != nil && !errors.Is(err, os.ErrNotExist) {
			d.fail(err)
			return
		}
		if err := os.Rename(filepath.Join(d.dir, "stderr.log"), previous); err != nil {
			d.fail(err)
			return
		}
		f, err := os.OpenFile(filepath.Join(d.dir, "stderr.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			d.fail(err)
			return
		}
		d.file = f
		d.size = 0
	}
	n, err := d.file.Write(data)
	d.size += n
	if err != nil {
		d.fail(err)
	}
}

// Record appends a redacted correlation or diagnostic fact.
func (d *Capture) Record(kind, text string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writeLine(kind, text)
}

// Finish records termination, flushes partial stderr and allows retention cleanup.
func (d *Capture) Finish(err error) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.line) > 0 || d.oversized {
		d.flushLine()
	}
	outcome := "process exited successfully"
	if err != nil {
		outcome = err.Error()
	}
	d.writeLine("capture", fmt.Sprintf("stderr_bytes=%d", d.stderrBytes))
	d.writeLine("exit", outcome)
	if d.file != nil {
		if e := d.file.Close(); e != nil {
			d.fail(e)
		}
		d.file = nil
	}
	d.closed = true
	if d.dir != "" {
		// Only completed directories are eligible for pruning; active captures survive.
		if e := os.WriteFile(filepath.Join(d.dir, "closed"), nil, 0o600); e != nil {
			d.fail(e)
		}
		pruneDiagnostics(filepath.Dir(d.dir))
	}
}

type diagnosticError struct {
	cause error
	text  string
}

func (e *diagnosticError) Error() string { return e.text }
func (e *diagnosticError) Unwrap() error { return e.cause }

// Failure retains an error and attaches its log reference without losing its cause.
func (d *Capture) Failure(scope string, err error) error {
	if d == nil || err == nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writeLine("failure", scope+": "+err.Error())
	reference := d.dir
	if reference == "" {
		reference = "unavailable"
	} else if d.writeErr != nil {
		reference += " (capture incomplete: log write failed)"
	}
	d.log.Warn("worker request failed", "scope", scope, "diagnostic_path", reference)
	return &diagnosticError{cause: err, text: fmt.Sprintf("%s [worker diagnostics: %s]", d.redact(err.Error()), reference)}
}

var diagnosticPruneMu sync.Mutex

// Prune only completed, owned captures; active writers are never removed.
func pruneDiagnostics(parent string) {
	diagnosticPruneMu.Lock()
	defer diagnosticPruneMu.Unlock()
	type closedRun struct {
		path, session string
		at            time.Time
	}
	var runs []closedRun
	root := filepath.Dir(parent)
	sessions, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, session := range sessions {
		if !session.IsDir() || len(session.Name()) != 64 {
			continue
		}
		if _, err := hex.DecodeString(session.Name()); err != nil {
			continue
		}
		dir := filepath.Join(root, session.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "run-") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			info, err := os.Lstat(filepath.Join(path, "closed"))
			if err == nil && info.Mode().IsRegular() {
				runs = append(runs, closedRun{path, session.Name(), info.ModTime()})
			}
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].at.After(runs[j].at) })
	counts := map[string]int{}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	retained := 0
	for _, run := range runs {
		counts[run.session]++
		if counts[run.session] <= diagnosticRetainedRuns && retained < 128 && !run.at.Before(cutoff) {
			retained++
			continue
		}
		for _, name := range []string{"stderr.log", "stderr.previous.log", "closed"} {
			_ = os.Remove(filepath.Join(run.path, name))
		}
		_ = os.Remove(run.path)
		_ = os.Remove(filepath.Dir(run.path)) // succeeds only when empty
	}
}

// Path identifies the private capture directory, or is empty when unavailable.
func (d *Capture) Path() string {
	if d == nil {
		return ""
	}
	return d.dir
}
