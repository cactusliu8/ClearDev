package previewserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Trial writes never select or mutate delivered data. The caller holds the
// project operation lock and excludes another running project process.
func (m *Manager) prepareStageTrialData(ctx context.Context, contract core.ProjectExecutionContract, candidate, contractSHA string, owner domain.SessionID) (projectDataPreparation, error) {
	var out projectDataPreparation
	if m.registryPath == "" || owner == "" || !validProjectPreviewSHA(candidate) {
		return out, errors.New("stage trial data binding is incomplete")
	}
	projectDigest := sha256.Sum256([]byte(contract.Selection.AOProjectID))
	projectKey := hex.EncodeToString(projectDigest[:])
	root := filepath.Join(filepath.Dir(m.registryPath), "cleardev-stage-trials", projectKey)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return out, err
	}
	if resolved, err := filepath.EvalSymlinks(root); err != nil || resolved != root {
		return out, errors.New("stage trial data root may not be redirected")
	}
	digest := sha256.Sum256([]byte(string(owner) + "\x00" + candidate + "\x00" + contractSHA))
	data := filepath.Join(root, hex.EncodeToString(digest[:]))
	if _, err := os.Lstat(data); err == nil {
		if err := validateProjectDataTree(ctx, data); err != nil {
			return out, err
		}
		return projectDataPreparation{directory: data, publish: func() error { return nil }}, nil
	} else if !os.IsNotExist(err) {
		return out, err
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) >= projectDataMaxVersions {
		return out, errors.New("STAGE_TRIAL_CAPACITY: retained trial data reached the bounded limit")
	}
	deliveredRoot := filepath.Join(filepath.Dir(m.registryPath), "cleardev-project-data", projectKey)
	previous, selected, err := readProjectDataPointer(filepath.Join(deliveredRoot, "selected.json"), contract)
	if err != nil {
		return out, err
	}
	if selected && previous.CandidateSHA != contract.BaseCommitSHA && previous.CandidateSHA != candidate {
		return out, errors.New("STAGE_TRIAL_BASELINE_CHANGED: delivered data no longer matches the stage baseline")
	}
	temporary, err := os.MkdirTemp(root, ".preparing-")
	if err != nil {
		return out, err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	if selected {
		source := filepath.Join(deliveredRoot, "versions", previous.Directory)
		if resolved, err := filepath.EvalSymlinks(source); err != nil || resolved != source {
			return out, errors.New("delivered data root may not be redirected")
		}
		if err := copyProjectData(ctx, source, temporary); err != nil {
			return out, err
		}
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := os.Rename(temporary, data); err != nil {
		return out, err
	}
	return projectDataPreparation{directory: data, publish: func() error { return nil }}, nil
}
