package previewserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

const projectDataLimitBytes int64 = 256 * 1024 * 1024
const projectDataLimitItems = 20_000
const projectDataMaxVersions = 20

// This small local-data pointer is preview storage metadata, not a workflow or
// acceptance state. Only the existing completed-result preview service reaches
// it. It never changes main, a Stage baseline, SQLite control facts or approvals.
type projectDataPointer struct {
	Version      int    `json:"version"`
	ProjectID    string `json:"projectId"`
	Repository   string `json:"repository"`
	CandidateSHA string `json:"candidateSha"`
	Directory    string `json:"directory"`
}

type projectDataPreparation struct {
	directory string
	publish   func() error
}

// prepareProjectData is called under the project+session operation locks, after
// the old managed process has stopped. A new version migrates a COPY of the
// last selected data. The old directory is retained even after successful
// startup; failed startup never publishes the new pointer. This is deliberately
// bounded local continuation, not an external or production database platform.
func (m *Manager) prepareProjectData(ctx context.Context, contract core.ProjectExecutionContract, candidate string) (projectDataPreparation, error) {
	var out projectDataPreparation
	if m.registryPath == "" || !validProjectPreviewSHA(candidate) {
		return out, errors.New("persistent project data storage is unavailable")
	}
	digest := sha256.Sum256([]byte(contract.Selection.AOProjectID))
	root := filepath.Join(filepath.Dir(m.registryPath), "cleardev-project-data", hex.EncodeToString(digest[:]))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return out, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return out, errors.New("project data root may not be redirected")
	}
	versions := filepath.Join(root, "versions")
	if err := os.MkdirAll(versions, 0o700); err != nil {
		return out, err
	}
	if info, err := os.Lstat(versions); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return out, errors.New("project data versions must be a real local directory")
	}
	pointerPath := filepath.Join(root, "selected.json")
	previous, selected, err := readProjectDataPointer(pointerPath, contract)
	if err != nil {
		return out, err
	}
	if selected {
		if previous.CandidateSHA == candidate {
			data := filepath.Join(versions, previous.Directory)
			if err := validateProjectDataTree(ctx, data); err != nil {
				return out, err
			}
			return projectDataPreparation{directory: data, publish: func() error { return nil }}, nil
		}
		if contract.BaseCommitSHA != previous.CandidateSHA {
			return out, errors.New("RESULT_DATA_BASELINE_CHANGED: select the last delivered data version as the next Stage baseline; an implicit downgrade or external database import is not supported")
		}
	}
	entries, err := os.ReadDir(versions)
	if err != nil || len(entries) >= projectDataMaxVersions {
		return out, errors.New("RESULT_DATA_CAPACITY: retained local data versions reached the bounded limit; no existing data was removed")
	}
	data, err := os.MkdirTemp(versions, "data-")
	if err != nil {
		return out, err
	}
	// A failed copy/upgrade is retained for inspection, but never selected.
	if previous.Directory != "" {
		if err := copyProjectData(ctx, filepath.Join(versions, previous.Directory), data); err != nil {
			return out, err
		}
	}
	next := projectDataPointer{Version: 1, ProjectID: contract.Selection.AOProjectID, Repository: contract.Selection.RepositoryPath,
		CandidateSHA: candidate, Directory: filepath.Base(data)}
	out.directory = data
	out.publish = func() error {
		encoded, err := json.Marshal(next)
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(root, ".selected-")
		if err != nil {
			return err
		}
		name := file.Name()
		defer func() { _ = os.Remove(name) }()
		_, writeErr := file.Write(encoded)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
		if err := os.Rename(name, pointerPath); err != nil {
			return err
		}
		directory, err := os.Open(root)
		if err != nil {
			return err
		}
		defer func() { _ = directory.Close() }()
		return directory.Sync()
	}
	return out, nil
}

// ProjectDataReady reports whether this exact completed delivery has been
// opened successfully and owns the retained data version. A later Stage may
// select it only after its migration has run; merely completing checks does
// not advance the data pointer.
func (m *Manager) ProjectDataReady(ctx context.Context, contract core.ProjectExecutionContract, candidate string) (bool, error) {
	pointer, selected, err := m.projectDataVersion(ctx, contract, candidate)
	return selected && pointer.CandidateSHA == candidate, err
}

// ProjectDataBaselineCurrent also permits a delivery that has never opened:
// there is no user data to migrate yet. Once a version is published, an older
// saved selection cannot be reused to branch around that data version.
func (m *Manager) ProjectDataBaselineCurrent(ctx context.Context, contract core.ProjectExecutionContract, candidate string) (bool, error) {
	pointer, selected, err := m.projectDataVersion(ctx, contract, candidate)
	return (!selected || pointer.CandidateSHA == candidate) && err == nil, err
}

func (m *Manager) projectDataVersion(ctx context.Context, contract core.ProjectExecutionContract, candidate string) (projectDataPointer, bool, error) {
	var empty projectDataPointer
	if m.registryPath == "" || !validProjectPreviewSHA(candidate) || core.ValidateProjectExecutionContract(contract) != nil {
		return empty, false, errors.New("persistent project data binding is invalid")
	}
	digest := sha256.Sum256([]byte(contract.Selection.AOProjectID))
	root := filepath.Join(filepath.Dir(m.registryPath), "cleardev-project-data", hex.EncodeToString(digest[:]))
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return empty, false, nil
	} else if err != nil {
		return empty, false, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return empty, false, errors.New("project data root may not be redirected")
	}
	versions := filepath.Join(root, "versions")
	if info, err := os.Lstat(versions); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return empty, false, errors.New("project data versions must be a real local directory")
	}
	pointer, selected, err := readProjectDataPointer(filepath.Join(root, "selected.json"), contract)
	if err != nil || !selected {
		return pointer, selected, err
	}
	if err := validateProjectDataTree(ctx, filepath.Join(versions, pointer.Directory)); err != nil {
		return empty, false, err
	}
	return pointer, true, nil
}

func readProjectDataPointer(path string, contract core.ProjectExecutionContract) (projectDataPointer, bool, error) {
	var pointer projectDataPointer
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return pointer, false, nil
	}
	if err != nil {
		return pointer, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return pointer, false, errors.New("project data selection is not a regular bounded record")
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &pointer) != nil || pointer.Version != 1 ||
		pointer.ProjectID != contract.Selection.AOProjectID || pointer.Repository != contract.Selection.RepositoryPath ||
		!validProjectPreviewSHA(pointer.CandidateSHA) || !validProjectDataDirectory(pointer.Directory) {
		return pointer, false, errors.New("project data selection no longer matches its original repository")
	}
	return pointer, true, nil
}

func validProjectDataDirectory(value string) bool {
	return strings.HasPrefix(value, "data-") && len(value) <= 100 && value == filepath.Base(value) && !strings.ContainsAny(value, "/\\\x00\r\n")
}

func validateProjectDataTree(ctx context.Context, source string) error {
	return walkProjectData(ctx, source, nil)
}

func copyProjectData(ctx context.Context, source, destination string) error {
	output, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	return walkProjectData(ctx, source, output)
}

func walkProjectData(ctx context.Context, source string, output *os.Root) error {
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("project data must remain in its managed local directory")
	}
	input, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	var bytes int64
	items := 0
	return fs.WalkDir(input.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		items++
		if items > projectDataLimitItems || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("project data has redirected paths or exceeds the bounded item limit")
		}
		if entry.IsDir() {
			if output != nil {
				return output.Mkdir(name, 0o700)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > projectDataLimitBytes-bytes {
			return errors.New("project data is not regular or exceeds the bounded byte limit")
		}
		bytes += info.Size()
		if output == nil {
			return nil
		}
		reader, err := input.Open(name)
		if err != nil {
			return err
		}
		writer, err := output.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = reader.Close()
			return err
		}
		written, copyErr := io.Copy(writer, io.LimitReader(reader, info.Size()+1))
		closeErr := errors.Join(reader.Close(), writer.Close())
		if written != info.Size() || copyErr != nil || closeErr != nil {
			return errors.Join(errors.New("project data changed while preparing its next version"), copyErr, closeErr)
		}
		return nil
	})
}
