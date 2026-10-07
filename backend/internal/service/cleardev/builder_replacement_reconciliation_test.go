package cleardev

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type builderReplacementCaptureObservation struct {
	*builderReplacementHarness
	cancelAtEntry context.CancelFunc
	err           error
	contextErr    error
}

func (h *builderReplacementCaptureObservation) CaptureBuilderHandoff(ctx context.Context, request ports.ClearDevBuilderHandoffSnapshotRequest) (ports.ClearDevBuilderHandoffSnapshot, error) {
	if h.cancelAtEntry != nil {
		h.cancelAtEntry()
	}
	snapshot, err := h.builderReplacementHarness.CaptureBuilderHandoff(ctx, request)
	h.err, h.contextErr = err, ctx.Err()
	return snapshot, err
}

// Every user table is compared, including SQLite rowids where supported and
// CDC, rather than only the new handoff facts.
func builderReplacementReconciliationDatabase(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	type table struct{ name, schema string }
	tables := func() []table {
		rows, err := db.Query(`SELECT name,coalesce(sql,'') FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var tables []table
		for rows.Next() {
			var current table
			if err := rows.Scan(&current.name, &current.schema); err != nil {
				t.Fatal(err)
			}
			tables = append(tables, current)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return tables
	}()
	out := map[string][][]any{}
	for _, table := range tables {
		out[table.name] = func() [][]any {
			columns := "rowid,*"
			if strings.Contains(strings.ToUpper(table.schema), "WITHOUT ROWID") {
				columns = "*"
			}
			name := `"` + strings.ReplaceAll(table.name, `"`, `""`) + `"`
			rows, err := db.Query("SELECT " + columns + " FROM " + name + " ORDER BY 1")
			if err != nil {
				t.Fatal("full database snapshot", table.name, err)
			}
			defer func() { _ = rows.Close() }()
			cols, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			var evidence [][]any
			for rows.Next() {
				values, pointers := make([]any, len(cols)), make([]any, len(cols))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err := rows.Scan(pointers...); err != nil {
					t.Fatal(err)
				}
				for i, value := range values {
					if raw, ok := value.([]byte); ok {
						values[i] = append([]byte(nil), raw...)
					}
				}
				evidence = append(evidence, values)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return evidence
		}()
	}
	return out
}

func assertBuilderReplacementRequestHasNoEffect(t *testing.T, f *projectPlanningFixture, h *builderReplacementHarness, db *sql.DB, before map[string][][]any, spawns, copies, sends int) {
	t.Helper()
	if h.spawnCalls != spawns || h.restoreCalls != copies || len(h.relays) != sends || !reflect.DeepEqual(before, builderReplacementReconciliationDatabase(t, db)) {
		t.Fatal("rejected request changed database/CDC, created a successor, copied or sent")
	}
	for _, table := range builderReplacementFactTables {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed REQUEST produced handoff authority or successor facts", table, count, err)
		}
	}
}

func TestBuilderReplacementReconciliationRequestRejectsLargeAllowedHistoryBeforeAuthority(t *testing.T) {
	f, h, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	name := filepath.Join(h.oldWorkspace, "src", "storage.ts")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(65 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	replacementGit(t, h.oldWorkspace, "add", "src/storage.ts")
	replacementGit(t, h.oldWorkspace, "-c", "commit.gpgsign=false", "commit", "-qm", "large allowed historical blob")
	largeHead := replacementGit(t, h.oldWorkspace, "rev-parse", "HEAD")
	if size := replacementGit(t, h.oldWorkspace, "cat-file", "-s", largeHead+":src/storage.ts"); size != strconv.FormatInt(65<<20, 10) {
		t.Fatal("fixture lacks a real 65 MiB allowed-path blob", size)
	}
	replacementGit(t, h.oldWorkspace, "rm", "src/storage.ts")
	replacementGit(t, h.oldWorkspace, "-c", "commit.gpgsign=false", "commit", "-qm", "small current tree")
	head := replacementGit(t, h.oldWorkspace, "rev-parse", "HEAD")
	if tree := replacementGit(t, h.oldWorkspace, "ls-tree", "-r", "HEAD"); tree != "" {
		t.Fatal("current HEAD is not the tiny allowed tree", tree)
	}
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("export const preserved = 'original';\n"), 0644); err != nil {
		t.Fatal(err)
	}
	observer := &builderReplacementCaptureObservation{builderReplacementHarness: h}
	f.s.inspector = observer
	input := builderReplacementInput(t, f, id, "request-large-history", core.RecoveryRequestBuilderReplacement)
	db := builderReplacementStorageDB(t, f)
	before := builderReplacementReconciliationDatabase(t, db)
	spawns, copies, sends, captures := h.spawnCalls, h.restoreCalls, len(h.relays), h.captureCalls
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err == nil || !errors.Is(observer.err, ports.ErrBuilderHandoffLimit) || h.captureCalls != captures+1 {
		t.Fatal("real Service capture did not reject large historical object material", err, observer.err)
	}
	assertBuilderReplacementRequestHasNoEffect(t, f, h, db, before, spawns, copies, sends)
	if actual := replacementGit(t, h.oldWorkspace, "rev-parse", "HEAD"); actual != head {
		t.Fatal("failed capture rewrote old HEAD")
	}
}

func TestBuilderReplacementReconciliationRequestCancellationCreatesNoAuthority(t *testing.T) {
	for _, mode := range []string{"already-cancelled", "cancel-at-capture"} {
		t.Run(mode, func(t *testing.T) {
			f, h, id := newBuilderReplacementFixture(t)
			input := builderReplacementInput(t, f, id, "request-cancelled-capture", core.RecoveryRequestBuilderReplacement)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observer := &builderReplacementCaptureObservation{builderReplacementHarness: h}
			f.s.inspector = observer
			if mode == "already-cancelled" {
				cancel()
			} else {
				observer.cancelAtEntry = cancel
			}
			db := builderReplacementStorageDB(t, f)
			before := builderReplacementReconciliationDatabase(t, db)
			spawns, copies, sends, captures := h.spawnCalls, h.restoreCalls, len(h.relays), h.captureCalls
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err == nil {
				t.Fatal("cancelled request succeeded")
			}
			if mode == "cancel-at-capture" {
				if h.captureCalls != captures+1 || observer.err == nil || !errors.Is(observer.contextErr, context.Canceled) {
					t.Fatal("real capture was not entered and cancelled", observer.err, observer.contextErr)
				}
			} else if h.captureCalls != captures {
				t.Fatal("already-cancelled request unexpectedly entered capture")
			}
			assertBuilderReplacementRequestHasNoEffect(t, f, h, db, before, spawns, copies, sends)
		})
	}
}

func TestBuilderReplacementReconciliationRequestDeadlineExpiresDuringObjectEnumeration(t *testing.T) {
	f, h, id := newBuilderReplacementFixture(t)
	input := builderReplacementInput(t, f, id, "request-enumeration-deadline", core.RecoveryRequestBuilderReplacement)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	marker := filepath.Join(shimDir, "enumeration-entered")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	// Completed operations delegate to actual Git. The rev-list invocation is
	// held after its entry marker proves material collection was entered, so
	// the caller deadline is tested rather than guessed from elapsed time.
	script := "#!/bin/sh\nfor arg do\n  if [ \"$arg\" = rev-list ]; then\n    printf entered > " + quote(marker) + "\n    sleep 0.6\n  fi\ndone\nexec " + quote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	observer := &builderReplacementCaptureObservation{builderReplacementHarness: h}
	f.s.inspector = observer
	db := builderReplacementStorageDB(t, f)
	before := builderReplacementReconciliationDatabase(t, db)
	spawns, copies, sends, captures := h.spawnCalls, h.restoreCalls, len(h.relays), h.captureCalls
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err == nil || observer.err == nil || !errors.Is(observer.contextErr, context.DeadlineExceeded) || h.captureCalls != captures+1 {
		t.Fatal("capture did not stop at the actual caller deadline", err, observer.err, observer.contextErr)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "entered" {
		t.Fatal("deadline expired before object enumeration; cannot claim enumeration timeout", err)
	}
	assertBuilderReplacementRequestHasNoEffect(t, f, h, db, before, spawns, copies, sends)
}

// SQLite, authorization, creation identity and Git workspaces remain real.
// Only one observation persistence call fails after its external action.
type builderReplacementObservationSaveFault struct {
	*sqlite.Store
	stage string
	left  int
}

func (s *builderReplacementObservationSaveFault) RecordClearDevBuilderReplacementObservation(ctx context.Context, o core.BuilderReplacementObservation) error {
	if o.Stage == s.stage && o.Outcome == "CONFIRMED" && s.left > 0 {
		s.left--
		return errors.New("explicit fixture: confirmed observation save interrupted")
	}
	return s.Store.RecordClearDevBuilderReplacementObservation(ctx, o)
}

type builderReplacementCopyFault struct {
	*builderReplacementHarness
	mode                         string
	restoreAttempts, targetReads int
	partialPath                  string
}

func (h *builderReplacementCopyFault) RestoreBuilderHandoff(ctx context.Context, snapshot ports.ClearDevBuilderHandoffSnapshot, target ports.ClearDevBuilderHandoffTarget) error {
	h.restoreAttempts++
	switch h.mode {
	case "before-copy":
		return errors.Join(ports.ErrBuilderHandoffBeforeCopy, errors.New("explicit fixture: no copy action occurred"))
	case "unknown":
		return errors.New("explicit fixture: copy outcome is unknown")
	case "partial":
		// A controlled partial write in the new target is retained for inspection.
		h.partialPath = filepath.Join(target.WorkspacePath, "src", "storage.ts")
		if err := os.MkdirAll(filepath.Dir(h.partialPath), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(h.partialPath, []byte("partial copy remains for inspection\n"), 0644); err != nil {
			return err
		}
		return errors.New("explicit fixture: copy stopped after a partial write")
	default:
		return h.builderReplacementHarness.RestoreBuilderHandoff(ctx, snapshot, target)
	}
}

func (h *builderReplacementCopyFault) VerifyBuilderHandoffTarget(ctx context.Context, snapshot ports.ClearDevBuilderHandoffSnapshot, target ports.ClearDevBuilderHandoffTarget) error {
	h.targetReads++
	return h.builderReplacementHarness.VerifyBuilderHandoffTarget(ctx, snapshot, target)
}

func approvedBuilderReplacementReconciliationFixture(t *testing.T) (*projectPlanningFixture, *builderReplacementHarness, string, WorkflowRecoveryInput) {
	t.Helper()
	f, h, id := newBuilderReplacementFixture(t)
	request := builderReplacementInput(t, f, id, "reconciliation-request", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), id, request); err != nil {
		t.Fatal(err)
	}
	approveBuilderReplacementStorage(t, f)
	return f, h, id, builderReplacementInput(t, f, id, "reconciliation-continue", core.RecoveryContinueBuilderReplacement)
}

func builderReplacementReconciliationSession(t *testing.T, f *projectPlanningFixture, creationKey string) (string, string, string) {
	t.Helper()
	db := builderReplacementStorageDB(t, f)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE creation_idempotency_key=?`, creationKey).Scan(&count); err != nil || count != 1 {
		t.Fatal("distinct keyed replacement sessions", count, err)
	}
	var id, fingerprint, path string
	if err := db.QueryRow(`SELECT id,creation_request_fingerprint,workspace_path FROM sessions WHERE creation_idempotency_key=?`, creationKey).Scan(&id, &fingerprint, &path); err != nil {
		t.Fatal(err)
	}
	var contexts int
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_builder_handoff_contexts WHERE ao_session_id=?`, id).Scan(&contexts); err != nil || contexts != 1 {
		t.Fatal("distinct private launch contexts", contexts, err)
	}
	var contextFingerprint, referencePath, contextSHA, snapshotSHA, systemPrompt string
	if err := db.QueryRow(`SELECT launch_fingerprint,reference_path,context_sha256,snapshot_sha256,system_prompt FROM cleardev_builder_handoff_contexts WHERE ao_session_id=?`, id).Scan(&contextFingerprint, &referencePath, &contextSHA, &snapshotSHA, &systemPrompt); err != nil || contextFingerprint != fingerprint || referencePath == "" || len(contextSHA) != 64 || len(snapshotSHA) != 64 || systemPrompt == "" {
		t.Fatal("private system context lost its exact persisted launch fingerprint", err)
	}
	return id, fingerprint, path
}

func builderReplacementReconciliationEmptyLaunches(t *testing.T, h *builderReplacementHarness, creationKey string) {
	t.Helper()
	launches := 0
	for _, cfg := range h.spawnConfigs {
		if cfg.CreationIdempotencyKey != creationKey {
			continue
		}
		launches++
		if cfg.Prompt != "" || cfg.IssueContext != "" || len(cfg.Attachments) != 0 || cfg.BuilderHandoffContext == nil {
			t.Fatal("reconciliation acquired an unmetered initial model message")
		}
	}
	if launches != 2 {
		t.Fatal("expected one initial and one exact-key reconciliation launch call", launches)
	}
}

func TestBuilderReplacementReconciliationConfirmedObservationSaveFailureKeepsOneIdentity(t *testing.T) {
	for _, stage := range []string{"CREATE", "COPY"} {
		t.Run(stage, func(t *testing.T) {
			f, h, id, cont := approvedBuilderReplacementReconciliationFixture(t)
			ctx := context.Background()
			fault := &builderReplacementObservationSaveFault{Store: f.store, stage: stage, left: 1}
			copyPort := &builderReplacementCopyFault{builderReplacementHarness: h}
			f.s.complexExecution, f.s.inspector = fault, copyPort
			spawns, copies, sends := h.spawnCalls, h.restoreCalls, len(h.relays)
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err == nil || fault.left != 0 {
				t.Fatal("confirmed save failure did not interrupt continuation", err)
			}
			state, err := f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
			if err != nil || state.Handoff == nil || state.Handoff.NewAOSessionID != "" {
				t.Fatal("interrupted handoff should remain registered without alias", err)
			}
			newID, fingerprint, workspace := builderReplacementReconciliationSession(t, f, state.Binding.SessionCreationKey)
			if h.spawnCalls != spawns+1 || len(h.relays) != sends {
				t.Fatal("save failure created multiple workers or sent a model message")
			}
			if stage == "CREATE" && (h.restoreCalls != copies || copyPort.restoreAttempts != 0) {
				t.Fatal("CREATE save failure crossed the copy boundary")
			}
			if stage == "COPY" && (h.restoreCalls != copies+1 || copyPort.restoreAttempts != 1 || copyPort.targetReads != 1) {
				t.Fatal("COPY save failure was not after one real copy and verification")
			}
			for _, o := range state.Observations {
				if o.Stage == stage && o.Outcome == "CONFIRMED" {
					t.Fatal("failed confirmation was persisted")
				}
			}
			originalBudget := state.Budget
			reopenBuilderReplacementStorage(t, f, h)
			fault.Store = f.store
			f.s.complexExecution, f.s.inspector = fault, copyPort
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil {
				t.Fatal("same-key reconciliation after SQLite reopen", err)
			}
			againID, againFingerprint, againWorkspace := builderReplacementReconciliationSession(t, f, state.Binding.SessionCreationKey)
			builderReplacementReconciliationEmptyLaunches(t, h, state.Binding.SessionCreationKey)
			if againID != newID || againFingerprint != fingerprint || againWorkspace != workspace || h.spawnCalls != spawns+2 || h.restoreCalls != copies+1 || copyPort.restoreAttempts != 1 || len(h.relays) != sends {
				t.Fatal("reconciliation changed creation identity, repeated copy or sent without normal dispatch")
			}
			if stage == "COPY" && copyPort.targetReads != 2 {
				t.Fatal("completed COPY replay did not only verify its existing target")
			}
			state, err = f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
			if err != nil || state.Handoff == nil || state.Handoff.NewAOSessionID != newID || !reflect.DeepEqual(state.Budget, originalBudget) {
				t.Fatal("reconciliation lost the grant, original budget or exact alias", err)
			}
			for _, required := range []string{"CREATE", "COPY"} {
				confirmed := 0
				for _, o := range state.Observations {
					if o.Stage == required && o.Outcome == "CONFIRMED" {
						confirmed++
					}
				}
				if confirmed != 1 {
					t.Fatal("unique coordinated confirmation", required, confirmed)
				}
			}
			if _, _, err := f.s.advanceComplexStandardExecution(ctx, id); err != nil || len(h.relays) != sends+1 {
				t.Fatal("coordinated alias did not send once through normal dispatch", err)
			}
		})
	}
}

func TestBuilderReplacementReconciliationUnknownCopyNeverAutomaticallyRewrites(t *testing.T) {
	for _, mode := range []string{"unknown", "partial"} {
		t.Run(mode, func(t *testing.T) {
			f, h, id, cont := approvedBuilderReplacementReconciliationFixture(t)
			ctx := context.Background()
			copyPort := &builderReplacementCopyFault{builderReplacementHarness: h, mode: mode}
			f.s.inspector = copyPort
			sends := len(h.relays)
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err == nil {
				t.Fatal("unknown copy continued")
			}
			state, err := f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
			if err != nil || state.Handoff == nil || state.Handoff.NewAOSessionID != "" || copyPort.restoreAttempts != 1 {
				t.Fatal("unknown copy did not remain unbound", err)
			}
			newID, fingerprint, workspace := builderReplacementReconciliationSession(t, f, state.Binding.SessionCreationKey)
			copyPort.mode = "" // The environment is repaired; persisted UNKNOWN still controls.
			spawns := h.spawnCalls
			for i := 0; i < 2; i++ {
				if _, err := f.s.GetWorkflowRecovery(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			if h.spawnCalls != spawns || copyPort.restoreAttempts != 1 {
				t.Fatal("GET retried an external action")
			}
			reopenBuilderReplacementStorage(t, f, h)
			f.s.inspector = copyPort
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err == nil {
				t.Fatal("persisted unknown copy became an automatic retry")
			}
			againID, againFingerprint, againWorkspace := builderReplacementReconciliationSession(t, f, state.Binding.SessionCreationKey)
			builderReplacementReconciliationEmptyLaunches(t, h, state.Binding.SessionCreationKey)
			if againID != newID || againFingerprint != fingerprint || againWorkspace != workspace || copyPort.restoreAttempts != 1 || copyPort.targetReads != 1 || len(h.relays) != sends {
				t.Fatal("unknown replay changed worker, copied again or sent")
			}
			state, err = f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
			if err != nil || state.Handoff == nil || state.Handoff.NewAOSessionID != "" {
				t.Fatal("unknown copy acquired a bound alias", err)
			}
			if mode == "partial" {
				raw, err := os.ReadFile(copyPort.partialPath)
				if err != nil || string(raw) != "partial copy remains for inspection\n" {
					t.Fatal("partial target was overwritten or deleted", err)
				}
			}
		})
	}
}

func TestBuilderReplacementReconciliationBeforeCopyFailureNeedsExplicitContinue(t *testing.T) {
	f, h, id, cont := approvedBuilderReplacementReconciliationFixture(t)
	ctx := context.Background()
	copyPort := &builderReplacementCopyFault{builderReplacementHarness: h, mode: "before-copy"}
	f.s.inspector = copyPort
	sends := len(h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err == nil {
		t.Fatal("known before-copy failure did not stop")
	}
	state, err := f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
	if err != nil || state.Handoff == nil || state.Handoff.NewAOSessionID != "" || copyPort.restoreAttempts != 1 || h.restoreCalls != 0 {
		t.Fatal("before-copy failure crossed the external write boundary", err)
	}
	failed := false
	for _, o := range state.Observations {
		if o.Stage == "COPY" && o.Outcome == "FAILED" && o.ReasonCode == "BUILDER_COPY_NOT_STARTED" {
			failed = true
		}
	}
	if !failed {
		t.Fatal("known no-action copy failure not persisted")
	}
	newID, fingerprint, workspace := builderReplacementReconciliationSession(t, f, state.Binding.SessionCreationKey)
	copyPort.mode = ""
	if _, err := f.s.GetWorkflowRecovery(ctx, id); err != nil || copyPort.restoreAttempts != 1 {
		t.Fatal("GET retried known before-copy failure", err)
	}
	reopenBuilderReplacementStorage(t, f, h)
	f.s.inspector = copyPort
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil {
		t.Fatal("explicit continuation of known no-action failure", err)
	}
	againID, againFingerprint, againWorkspace := builderReplacementReconciliationSession(t, f, state.Binding.SessionCreationKey)
	builderReplacementReconciliationEmptyLaunches(t, h, state.Binding.SessionCreationKey)
	if againID != newID || againFingerprint != fingerprint || againWorkspace != workspace || copyPort.restoreAttempts != 2 || h.restoreCalls != 1 || len(h.relays) != sends {
		t.Fatal("explicit retry changed identity, copied more than once or sent")
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil || copyPort.restoreAttempts != 2 || h.restoreCalls != 1 || len(h.relays) != sends {
		t.Fatal("bound replay repeated copy", err)
	}
	if _, _, err := f.s.advanceComplexStandardExecution(ctx, id); err != nil || len(h.relays) != sends+1 {
		t.Fatal("confirmed copy did not enter normal one-message dispatch", err)
	}
}
