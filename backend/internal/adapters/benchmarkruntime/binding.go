// Package benchmarkruntime owns the private, startup-only live experiment
// assembly. A chat request cannot select a launcher or supply this binding.
package benchmarkruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// ControlRoot is the owner-only root of v17 experiment authority.
const ControlRoot = "/home/cleardev/.ao/benchmark-s12b-v17-control"

// FormalControlRoot is the S12C formal experiment root. A binding must live in
// the root that matches its record class, so a purpose can never be smuggled
// from one control root into the other.
const FormalControlRoot = "/home/cleardev/.ao/benchmark-s12c-control"

// FormalBatchControlRoot is the only S12C v7 clean formal batch root. The old
// formal root above remains readable under its original no-batch contract.
const FormalBatchControlRoot = "/home/cleardev/.ao/benchmark-s12c-formal-01"

// FormalBatchID is the fixed identity of the only S12C v7 clean formal batch.
const FormalBatchID = "s12c-formal-01"

// S12C v15 keeps authority and the unique ledger on the old control disk while
// new attempt-local runtime material lives on the fixed /data filesystem.
const formalBatchDataRoot = "/data/cleardev/s12c-formal-01"
const formalBatchAttemptsRoot = formalBatchDataRoot + "/attempts"
const formalBatchTmpRoot = formalBatchDataRoot + "/tmp"
const formalBatchRuntimeCacheRoot = "/data/cleardev/runtime-cache-v14"
const formalBatchStorageBindingPath = FormalBatchControlRoot + "/evidence/storage-v14/binding.json"
const formalBatchRecoveryIndexPath = FormalBatchControlRoot + "/evidence/recovery-v14/index.json"

const formalStorageUUID = "b99eb535-d927-430b-aaac-84637452b651"
const formalStorageDeviceSerial = "50026B72822461B2"
const formalStoragePlanCommit = "b6a6da7e8762ed9dd8dce9e05b3fa9b0f06eb9f8"
const formalRequestKind = "CLEARDEV_FORMAL_RUN_REQUEST"
const formalStorageBindingKind = "CLEARDEV_FORMAL_STORAGE_V14_BINDING"
const formalStorageRequestKind = "CLEARDEV_FORMAL_V14_STORAGE_AND_RECOVERY"

// PlanCommit binds this driver to the frozen v21 plan.
const PlanCommit = "0fffdca4f3f8d3640253e99f8afb998d728e327f"

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var attemptDirPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

type runtimePathPolicy struct {
	controlRoot        string
	formalControlRoot  string
	batchControlRoot   string
	batchDataRoot      string
	batchAttemptsRoot  string
	batchTmpRoot       string
	batchRuntimeCache  string
	storageBindingPath string
	recoveryIndexPath  string
}

var activeRuntimePathPolicy = runtimePathPolicy{
	controlRoot:        ControlRoot,
	formalControlRoot:  FormalControlRoot,
	batchControlRoot:   FormalBatchControlRoot,
	batchDataRoot:      formalBatchDataRoot,
	batchAttemptsRoot:  formalBatchAttemptsRoot,
	batchTmpRoot:       formalBatchTmpRoot,
	batchRuntimeCache:  formalBatchRuntimeCacheRoot,
	storageBindingPath: formalBatchStorageBindingPath,
	recoveryIndexPath:  formalBatchRecoveryIndexPath,
}

type runtimeLocation struct {
	runtimeRoot   string
	authorityRoot string
	v15DataRoot   bool
}

// runtimeRecordClasses are the two live purposes this private binding serves.
// Both keep the same schema, kind, frozen plan commit and digest checks; only
// the authorised unit namespace differs, and the product manifest must declare
// exactly the same purpose.
var runtimeRecordClasses = map[string]bool{
	core.BenchmarkPurposeDevLive: true,
	core.BenchmarkPurposeFormal:  true,
}

// formalRuntimeUnit matches the frozen formal plan ids. All four groups run
// their roles through this private runtime binding; only G3/G4 additionally
// carry a product manifest.
var formalRuntimeUnit = regexp.MustCompile(`^R[1-4]-(T1|T2|T2F|T3|Q)-(G1|G2|G3|G4)$`)

func authorizedRuntimeUnit(recordClass, group, unitID string) bool {
	switch recordClass {
	case core.BenchmarkPurposeDevLive:
		return unitID == "DEV-SENSOR-"+group || unitID == "DEV-SENSOR-"+group+"-F" || unitID == "DEV-DIAG-STOP" || unitID == "DEV-DIAG-DEBUG"
	case core.BenchmarkPurposeFormal:
		match := formalRuntimeUnit.FindStringSubmatch(unitID)
		return len(match) == 3 && match[2] == group
	default:
		return false
	}
}

// Binding contains only paths to owner-controlled inputs, never a command or
// process identity supplied by a participant. File SHA values bind exact bytes.
type Binding struct {
	SchemaVersion       int    `json:"schemaVersion"`
	Kind                string `json:"kind"`
	RecordClass         string `json:"recordClass"`
	BatchID             string `json:"batchID,omitempty"`
	BatchManifestSHA256 string `json:"batchManifestSHA256,omitempty"`
	PlanCommit          string `json:"planCommit"`
	CandidateCommit     string `json:"candidateCommit"`
	Group               string `json:"group"`
	DataRoot            string `json:"dataRoot"`
	ProjectRoot         string `json:"projectRoot"`
	BindingPath         string `json:"bindingPath"`
	BindingSHA256       string `json:"bindingSHA256"`
	AuthorizationPath   string `json:"authorizationPath"`
	AuthorizationSHA256 string `json:"authorizationSHA256"`
	// A FORMAL runtime binding must also point at the owner registration that
	// authorises this exact request and scope.
	RegistrationPath   string `json:"registrationPath,omitempty"`
	RegistrationSHA256 string `json:"registrationSHA256,omitempty"`
	SocketPath         string `json:"socketPath"`
}

// Load validates the private startup material before a driver or credential
// reader is assembled. Empty means an ordinary, unbound daemon.
func Load(path, dataRoot string, product *core.BenchmarkBackendManifest) (*Binding, error) {
	if path == "" {
		return nil, nil
	}
	location, err := runtimeLocationFor(path)
	if err != nil {
		return nil, err
	}
	binding, err := loadAt(path, dataRoot, location, product)
	if err != nil {
		return nil, err
	}
	policy := activeRuntimePathPolicy
	if location.v15DataRoot {
		if binding.RecordClass != core.BenchmarkPurposeFormal || binding.BatchID != FormalBatchID || !shaPattern.MatchString(binding.BatchManifestSHA256) {
			return nil, fmt.Errorf("runtime data-root purpose or batch mismatch")
		}
		return binding, nil
	}
	if binding.RecordClass == core.BenchmarkPurposeDevLive && location.runtimeRoot != policy.controlRoot {
		return nil, fmt.Errorf("runtime purpose does not match its control root")
	}
	if binding.RecordClass == core.BenchmarkPurposeFormal {
		switch location.runtimeRoot {
		case policy.formalControlRoot:
			if binding.BatchID != "" || binding.BatchManifestSHA256 != "" {
				return nil, fmt.Errorf("legacy formal runtime must not claim a batch")
			}
		case policy.batchControlRoot:
			if binding.BatchID != FormalBatchID || !shaPattern.MatchString(binding.BatchManifestSHA256) {
				return nil, fmt.Errorf("runtime batch identity mismatch")
			}
		default:
			return nil, fmt.Errorf("runtime purpose does not match its control root")
		}
	}
	return binding, nil
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func runtimeLocationFor(path string) (runtimeLocation, error) {
	policy := activeRuntimePathPolicy
	for _, candidate := range []string{policy.controlRoot, policy.formalControlRoot, policy.batchControlRoot} {
		if withinRoot(candidate, path) {
			return runtimeLocation{runtimeRoot: candidate, authorityRoot: candidate}, nil
		}
	}
	rel, err := filepath.Rel(policy.batchAttemptsRoot, path)
	if err == nil {
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) == 2 && attemptDirPattern.MatchString(parts[0]) && parts[1] == "runtime.json" {
			attemptRoot := filepath.Join(policy.batchAttemptsRoot, parts[0])
			return runtimeLocation{runtimeRoot: attemptRoot, authorityRoot: policy.batchControlRoot, v15DataRoot: true}, nil
		}
	}
	return runtimeLocation{}, fmt.Errorf("runtime path outside private control root")
}

func privatePath(root, path string, file bool) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("runtime path outside private control root")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return fmt.Errorf("runtime path missing or indirect: %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 || (file && !info.Mode().IsRegular()) || (!file && !info.IsDir()) {
		return fmt.Errorf("runtime path must be private and owned: %s", path)
	}
	return nil
}

func exactFile(root, path, expected string) ([]byte, error) {
	if !shaPattern.MatchString(expected) {
		return nil, fmt.Errorf("runtime file digest invalid")
	}
	if err := privatePath(root, path, true); err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(contents)
	if hex.EncodeToString(sum[:]) != expected {
		return nil, fmt.Errorf("runtime file changed: %s", path)
	}
	return contents, nil
}

func load(path, dataRoot, root string, product *core.BenchmarkBackendManifest) (*Binding, error) {
	return loadAt(path, dataRoot, runtimeLocation{runtimeRoot: root, authorityRoot: root}, product)
}

func loadAt(path, dataRoot string, location runtimeLocation, product *core.BenchmarkBackendManifest) (*Binding, error) {
	runtimeRoot := location.runtimeRoot
	authorityRoot := location.authorityRoot
	if err := privatePath(runtimeRoot, path, true); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var binding Binding
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return nil, fmt.Errorf("runtime binding: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("runtime binding trailing data")
	}
	if binding.SchemaVersion != 3 || binding.Kind != "CLEARDEV_ROLE_RUNTIME" || !runtimeRecordClasses[binding.RecordClass] || binding.PlanCommit != PlanCommit || !commitPattern.MatchString(binding.CandidateCommit) {
		return nil, fmt.Errorf("runtime plan or purpose not authorized")
	}
	if binding.Group != "G1" && binding.Group != "G2" && binding.Group != "G3" && binding.Group != "G4" {
		return nil, fmt.Errorf("runtime group invalid")
	}
	if binding.DataRoot != dataRoot {
		return nil, fmt.Errorf("runtime AO data root mismatch")
	}
	if err := privatePath(runtimeRoot, binding.DataRoot, false); err != nil {
		return nil, err
	}
	if location.v15DataRoot {
		if binding.DataRoot != filepath.Join(runtimeRoot, "ao") || binding.SocketPath != filepath.Join(runtimeRoot, "runtime.sock") ||
			binding.BindingPath != filepath.Join(runtimeRoot, "binding.json") || binding.AuthorizationPath != filepath.Join(runtimeRoot, "runtime-authorization.json") {
			return nil, fmt.Errorf("runtime data-root attempt binding mismatch")
		}
	}
	if binding.BatchID == FormalBatchID {
		if err := validateFormalBatchIdentity(authorityRoot, binding); err != nil {
			return nil, err
		}
	}
	// The product path is external to the authority root but must be a real owned
	// directory; the container owner separately validates each role's mounts.
	resolved, err := filepath.EvalSymlinks(binding.ProjectRoot)
	if err != nil || !filepath.IsAbs(binding.ProjectRoot) || resolved != binding.ProjectRoot {
		return nil, fmt.Errorf("runtime project root invalid")
	}
	if location.v15DataRoot {
		if err := privatePath(activeRuntimePathPolicy.batchTmpRoot, binding.ProjectRoot, false); err != nil {
			return nil, fmt.Errorf("runtime project root: %w", err)
		}
	}
	prepared, err := exactFile(runtimeRoot, binding.BindingPath, binding.BindingSHA256)
	if err != nil {
		return nil, err
	}
	authorization, err := exactFile(runtimeRoot, binding.AuthorizationPath, binding.AuthorizationSHA256)
	if err != nil {
		return nil, err
	}
	if err := validateDocuments(binding, prepared, authorization, location); err != nil {
		return nil, err
	}
	if filepath.Dir(binding.SocketPath) != filepath.Dir(path) || filepath.Base(binding.SocketPath) != "runtime.sock" {
		return nil, fmt.Errorf("runtime socket location invalid")
	}
	if binding.Group == "G1" || binding.Group == "G2" {
		if product != nil {
			return nil, fmt.Errorf("G1/G2 must not manufacture a product binding")
		}
	} else if product == nil || product.Purpose != binding.RecordClass || product.Group != binding.Group || product.ProjectRoot != binding.ProjectRoot || product.FacilityCommit != binding.CandidateCommit {
		return nil, fmt.Errorf("runtime product binding mismatch")
	}
	return &binding, nil
}

func validateFormalBatchIdentity(root string, binding Binding) error {
	path := filepath.Join(root, "batch.json")
	if err := privatePath(root, path, true); err != nil {
		return fmt.Errorf("runtime batch manifest: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var batch struct {
		Kind           string `json:"kind"`
		BatchID        string `json:"batchID"`
		Root           string `json:"root"`
		ManifestSHA256 string `json:"manifestSHA256"`
		Plan           struct {
			Commit string `json:"commit"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(raw, &batch); err != nil {
		return fmt.Errorf("runtime batch manifest: %w", err)
	}
	if batch.Kind != "CLEARDEV_FORMAL_BATCH" || batch.BatchID != FormalBatchID || batch.Root != root ||
		batch.ManifestSHA256 != binding.BatchManifestSHA256 || batch.Plan.Commit != "35c0e6f22c6d92c81790d1da0e8ab6fe7d9551fc" {
		return fmt.Errorf("runtime batch manifest mismatch")
	}
	return nil
}

func validateDocuments(binding Binding, prepared, authorization []byte, location runtimeLocation) error {
	var document struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		Manifest      struct {
			RecordClass      string `json:"recordClass"`
			BatchID          string `json:"batchID,omitempty"`
			PlanCommit       string `json:"planCommit"`
			CandidateCommit  string `json:"candidateCommit"`
			Group            string `json:"group"`
			PlanUnitID       string `json:"planUnitId"`
			Scene            int    `json:"scene"`
			RunRequestSHA256 string `json:"runRequestSHA256,omitempty"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal(prepared, &document); err != nil {
		return err
	}
	m := document.Manifest
	if document.SchemaVersion != 3 || document.Kind != "CLEARDEV_LIVE_BINDING" || m.RecordClass != binding.RecordClass || m.BatchID != binding.BatchID || m.PlanCommit != binding.PlanCommit || m.CandidateCommit != binding.CandidateCommit || m.Group != binding.Group {
		return fmt.Errorf("runtime prepared binding mismatch")
	}
	if !authorizedRuntimeUnit(binding.RecordClass, binding.Group, m.PlanUnitID) {
		return fmt.Errorf("runtime unit not authorized")
	}
	var authority struct {
		Kind               string `json:"kind"`
		RecordClass        string `json:"recordClass"`
		PlanCommit         string `json:"planCommit"`
		CandidateCommit    string `json:"candidateCommit"`
		BindingSHA256      string `json:"bindingSHA256"`
		RequestSHA256      string `json:"requestSHA256,omitempty"`
		RegistrationSHA256 string `json:"registrationSha256,omitempty"`
		BatchID            string `json:"batchID,omitempty"`
		AttemptID          string `json:"attemptID,omitempty"`
		PlanUnitID         string `json:"planUnitId,omitempty"`
		Group              string `json:"group,omitempty"`
		Scene              int    `json:"scene,omitempty"`
	}
	if err := json.Unmarshal(authorization, &authority); err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, prepared); err != nil {
		return err
	}
	digest := sha256.Sum256(compact.Bytes())
	if authority.Kind != "CLEARDEV_LIVE_AUTHORIZATION" || authority.RecordClass != binding.RecordClass || authority.BatchID != binding.BatchID || authority.PlanCommit != binding.PlanCommit || authority.CandidateCommit != binding.CandidateCommit || authority.BindingSHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("runtime authorization mismatch")
	}
	if location.v15DataRoot {
		if authority.AttemptID == "" || authority.PlanUnitID != m.PlanUnitID || authority.Group != m.Group || authority.Scene != m.Scene ||
			filepath.Base(location.runtimeRoot) != authority.AttemptID {
			return fmt.Errorf("runtime data-root authorization mismatch")
		}
		if !shaPattern.MatchString(m.RunRequestSHA256) || m.RunRequestSHA256 != authority.RequestSHA256 {
			return fmt.Errorf("runtime prepared request mismatch")
		}
	}
	// A FORMAL binding additionally has to name the owner registration that
	// authorises this request, unit, group and scene. Without it the private
	// runtime has no authority even though every digest matches.
	if binding.RecordClass == core.BenchmarkPurposeFormal {
		if !shaPattern.MatchString(authority.RequestSHA256) || !shaPattern.MatchString(authority.RegistrationSHA256) ||
			authority.RegistrationSHA256 != binding.RegistrationSHA256 || binding.RegistrationPath == "" {
			return fmt.Errorf("runtime owner registration missing or changed")
		}
		if err := validateFormalRegistration(binding, location.authorityRoot, authority.RequestSHA256, m.PlanUnitID, m.Group, m.Scene, location.v15DataRoot); err != nil {
			return err
		}
		if location.v15DataRoot {
			if err := validateFormalV15Request(binding, location, authority.RequestSHA256, m.PlanUnitID, m.Group, m.Scene); err != nil {
				return err
			}
		}
	} else if binding.RegistrationPath != "" || binding.RegistrationSHA256 != "" {
		return fmt.Errorf("runtime owner registration is only valid for FORMAL")
	}
	return nil
}

// validateFormalRegistration re-reads the private owner registration and checks
// that it approves this exact request and covers this unit, group and scene.
func validateFormalRegistration(binding Binding, root, requestSHA256, unitID, group string, scene int, strictCanonical bool) error {
	if err := privatePath(root, binding.RegistrationPath, true); err != nil {
		return fmt.Errorf("runtime owner registration path: %w", err)
	}
	contents, err := os.ReadFile(binding.RegistrationPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(contents)
	if hex.EncodeToString(sum[:]) != binding.RegistrationSHA256 {
		return fmt.Errorf("runtime owner registration changed")
	}
	var registration struct {
		SchemaVersion       int    `json:"schemaVersion"`
		Kind                string `json:"kind"`
		Source              string `json:"source"`
		DecidedBy           string `json:"decidedBy"`
		DecidedAt           int64  `json:"decidedAt"`
		Approved            bool   `json:"approved"`
		Revoked             bool   `json:"revoked"`
		RequestSHA256       string `json:"requestSHA256"`
		RequestPath         string `json:"requestPath"`
		BatchID             string `json:"batchID,omitempty"`
		BatchManifestSHA256 string `json:"batchManifestSHA256,omitempty"`
		Scope               struct {
			PlanUnitIDs []string `json:"planUnitIds"`
			Groups      []string `json:"groups"`
			Scenes      []int    `json:"scenes"`
		} `json:"scope"`
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registration); err != nil {
		return fmt.Errorf("runtime owner registration: %w", err)
	}
	if registration.SchemaVersion != 1 || registration.Kind != "CLEARDEV_FORMAL_OWNER_REGISTRATION" || registration.Source != "USER_DECISION" ||
		registration.DecidedBy == "" || registration.DecidedAt <= 0 || registration.RequestPath == "" ||
		!registration.Approved || registration.Revoked || registration.RequestSHA256 != requestSHA256 {
		return fmt.Errorf("runtime owner registration is not an approval for this request")
	}
	if binding.BatchID == FormalBatchID {
		if registration.BatchID != binding.BatchID || registration.BatchManifestSHA256 != binding.BatchManifestSHA256 ||
			registration.RequestPath != filepath.Join(root, "requests", requestSHA256, "request.json") {
			return fmt.Errorf("runtime owner registration batch mismatch")
		}
		if strictCanonical && binding.RegistrationPath != filepath.Join(root, "authority", requestSHA256+".json") {
			return fmt.Errorf("runtime owner registration path is not canonical")
		}
	} else if registration.BatchID != "" || registration.BatchManifestSHA256 != "" {
		return fmt.Errorf("legacy runtime owner registration must not claim a batch")
	}
	covered := func(unit string, allowed []string) bool {
		for _, item := range allowed {
			if item == unit {
				return true
			}
		}
		return false
	}
	if !covered(unitID, registration.Scope.PlanUnitIDs) || !covered(group, registration.Scope.Groups) {
		return fmt.Errorf("runtime owner registration does not cover this unit")
	}
	if scene < 1 || scene > 2 || !containsScene(registration.Scope.Scenes, scene) {
		return fmt.Errorf("runtime owner registration does not cover this scene")
	}
	return nil
}

type formalV15Request struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	CandidateCommit string `json:"candidateCommit"`
	RequestSHA256   string `json:"requestSHA256"`
	Batch           struct {
		ID             string `json:"id"`
		Root           string `json:"root"`
		ManifestSHA256 string `json:"manifestSHA256"`
	} `json:"batch"`
	ExecutionScope struct {
		Sendable         int      `json:"sendable"`
		Main             int      `json:"main"`
		Followup         int      `json:"followup"`
		ThresholdTokens  int      `json:"thresholdTokens"`
		ThresholdMinutes int      `json:"thresholdMinutes"`
		PlanUnitIDs      []string `json:"planUnitIds"`
	} `json:"executionScope"`
	Control struct {
		Root      string `json:"root"`
		Requests  string `json:"requests"`
		Attempts  string `json:"attempts"`
		Archives  string `json:"archives"`
		Authority string `json:"authority"`
		Registry  string `json:"registry"`
	} `json:"control"`
	Registry struct {
		Path                string `json:"path"`
		BatchManifestSHA256 string `json:"batchManifestSHA256"`
	} `json:"registry"`
	StorageV14 struct {
		Kind    string `json:"kind"`
		Storage struct {
			Path          string `json:"path"`
			FileSHA256    string `json:"fileSHA256"`
			BindingSHA256 string `json:"bindingSHA256"`
		} `json:"storage"`
		Roots struct {
			Control            string `json:"control"`
			Registry           string `json:"registry"`
			HistoricalAttempts string `json:"historicalAttempts"`
			NewAttempts        string `json:"newAttempts"`
			Tmp                string `json:"tmp"`
			RuntimeCache       string `json:"runtimeCache"`
		} `json:"roots"`
		Recovery struct {
			Path        string `json:"path"`
			FileSHA256  string `json:"fileSHA256"`
			IndexSHA256 string `json:"indexSHA256"`
		} `json:"recovery"`
	} `json:"storageV14"`
}

type formalV15RecoveryIndex struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Kind            string `json:"kind"`
	PlanCommit      string `json:"planCommit"`
	SourceCandidate string `json:"sourceCandidate"`
	SourceRequest   struct {
		RequestSHA256 string `json:"requestSHA256"`
		Request       struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		} `json:"request"`
	} `json:"sourceRequest"`
	NoParent struct {
		PlanUnitID string `json:"planUnitId"`
	} `json:"noParent"`
	Used []struct {
		PlanUnitID string `json:"planUnitId"`
	} `json:"used"`
	ExcludedPlanUnitIDs []string `json:"excludedPlanUnitIds"`
	NextPlanUnitID      string   `json:"nextPlanUnitId"`
	IndexSHA256         string   `json:"indexSHA256"`
}

type formalV15SourceRequest struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	CandidateCommit string `json:"candidateCommit"`
	RequestSHA256   string `json:"requestSHA256"`
	ExecutionScope  struct {
		Sendable         int      `json:"sendable"`
		Main             int      `json:"main"`
		Followup         int      `json:"followup"`
		ThresholdTokens  int      `json:"thresholdTokens"`
		ThresholdMinutes int      `json:"thresholdMinutes"`
		PlanUnitIDs      []string `json:"planUnitIds"`
	} `json:"executionScope"`
}

type formalV15StorageBinding struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	PlanCommit    string `json:"planCommit"`
	Mount         struct {
		Target       string `json:"target"`
		FileSystem   string `json:"fileSystem"`
		UUID         string `json:"uuid"`
		DeviceSerial string `json:"deviceSerial"`
	} `json:"mount"`
	Roots struct {
		Control      string `json:"control"`
		Data         string `json:"data"`
		Attempts     string `json:"attempts"`
		Tmp          string `json:"tmp"`
		RuntimeCache string `json:"runtimeCache"`
	} `json:"roots"`
	Devices struct {
		MountDev        uint64 `json:"mountDev"`
		AttemptsDev     uint64 `json:"attemptsDev"`
		TmpDev          uint64 `json:"tmpDev"`
		RuntimeCacheDev uint64 `json:"runtimeCacheDev"`
	} `json:"devices"`
	BindingSHA256 string `json:"bindingSHA256"`
}

func canonicalJSONString(value string) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

func canonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	var write func(any) error
	write = func(value any) error {
		switch typed := value.(type) {
		case nil:
			out.WriteString("null")
		case bool:
			if typed {
				out.WriteString("true")
			} else {
				out.WriteString("false")
			}
		case string:
			raw, err := canonicalJSONString(typed)
			if err != nil {
				return err
			}
			out.Write(raw)
		case json.Number:
			if _, err := typed.Int64(); err != nil {
				if _, err := typed.Float64(); err != nil {
					return err
				}
			}
			out.WriteString(typed.String())
		case int:
			out.WriteString(strconv.Itoa(typed))
		case int64:
			out.WriteString(strconv.FormatInt(typed, 10))
		case uint64:
			out.WriteString(strconv.FormatUint(typed, 10))
		case float64:
			out.WriteString(strconv.FormatFloat(typed, 'g', -1, 64))
		case []any:
			out.WriteByte('[')
			for index, item := range typed {
				if index > 0 {
					out.WriteByte(',')
				}
				if err := write(item); err != nil {
					return err
				}
			}
			out.WriteByte(']')
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			out.WriteByte('{')
			for index, key := range keys {
				if index > 0 {
					out.WriteByte(',')
				}
				raw, err := canonicalJSONString(key)
				if err != nil {
					return err
				}
				out.Write(raw)
				out.WriteByte(':')
				if err := write(typed[key]); err != nil {
					return err
				}
			}
			out.WriteByte('}')
		default:
			return fmt.Errorf("unsupported canonical json type %T", value)
		}
		return nil
	}
	if err := write(value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func selfDigest(contents []byte, field string) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("trailing json data")
	}
	delete(value, field)
	canonical, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func fileSHA256(path string) (string, []byte, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:]), contents, nil
}

func privateExactDirectory(path string) (uint64, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return 0, fmt.Errorf("runtime path missing or indirect: %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return 0, fmt.Errorf("runtime path must be private and owned: %s", path)
	}
	return stat.Dev, nil
}

func validateFormalV15Request(binding Binding, location runtimeLocation, requestSHA256, unitID, group string, scene int) error {
	policy := activeRuntimePathPolicy
	requestPath := filepath.Join(location.authorityRoot, "requests", requestSHA256, "request.json")
	if err := privatePath(location.authorityRoot, requestPath, true); err != nil {
		return fmt.Errorf("runtime request path: %w", err)
	}
	_, requestBytes, err := fileSHA256(requestPath)
	if err != nil {
		return err
	}
	// The formal request uses its canonical self digest, not its raw file digest.
	var request formalV15Request
	if err := json.Unmarshal(requestBytes, &request); err != nil {
		return fmt.Errorf("runtime request: %w", err)
	}
	self, err := selfDigest(requestBytes, "requestSHA256")
	if err != nil || self != requestSHA256 || request.RequestSHA256 != requestSHA256 {
		return fmt.Errorf("runtime request digest mismatch")
	}
	if request.SchemaVersion != 1 || request.Kind != formalRequestKind || request.Status != "PENDING_USER_APPROVAL" ||
		request.CandidateCommit != binding.CandidateCommit || request.Batch.ID != FormalBatchID ||
		request.Batch.Root != location.authorityRoot || request.Batch.ManifestSHA256 != binding.BatchManifestSHA256 ||
		request.ExecutionScope.Sendable != 49 || request.ExecutionScope.Main != 40 || request.ExecutionScope.Followup != 9 ||
		request.ExecutionScope.ThresholdTokens != 64_250_000 || request.ExecutionScope.ThresholdMinutes != 2_010 ||
		!containsString(request.ExecutionScope.PlanUnitIDs, unitID) {
		return fmt.Errorf("runtime request binding mismatch")
	}
	if request.Control.Root != location.authorityRoot ||
		request.Control.Requests != filepath.Join(location.authorityRoot, "requests") ||
		request.Control.Attempts != filepath.Join(location.authorityRoot, "attempts") ||
		request.Control.Archives != filepath.Join(location.authorityRoot, "stop-archives") ||
		request.Control.Authority != filepath.Join(location.authorityRoot, "authority") ||
		request.Control.Registry != filepath.Join(location.authorityRoot, "formal-attempts.sqlite") ||
		request.Registry.Path != filepath.Join(location.authorityRoot, "formal-attempts.sqlite") ||
		request.Registry.BatchManifestSHA256 != binding.BatchManifestSHA256 {
		return fmt.Errorf("runtime request control root mismatch")
	}
	storage := request.StorageV14
	if storage.Kind != formalStorageRequestKind || storage.Storage.Path != policy.storageBindingPath ||
		storage.Roots.Control != location.authorityRoot ||
		storage.Roots.Registry != filepath.Join(location.authorityRoot, "formal-attempts.sqlite") ||
		storage.Roots.HistoricalAttempts != filepath.Join(location.authorityRoot, "attempts") ||
		storage.Roots.NewAttempts != policy.batchAttemptsRoot || storage.Roots.Tmp != policy.batchTmpRoot ||
		storage.Roots.RuntimeCache != policy.batchRuntimeCache || storage.Recovery.Path != policy.recoveryIndexPath {
		return fmt.Errorf("runtime request storage binding mismatch")
	}
	if err := validateFormalV15StorageSource(binding, location, storage.Storage.Path, storage.Storage.FileSHA256, storage.Storage.BindingSHA256); err != nil {
		return err
	}
	if err := validateFormalV15RecoveryScope(location, request); err != nil {
		return err
	}
	if group != binding.Group || scene < 1 || scene > 2 {
		return fmt.Errorf("runtime request unit mismatch")
	}
	return nil
}

func validateFormalV15RecoveryScope(location runtimeLocation, request formalV15Request) error {
	ref := request.StorageV14.Recovery
	if ref.Path != activeRuntimePathPolicy.recoveryIndexPath || !shaPattern.MatchString(ref.FileSHA256) || !shaPattern.MatchString(ref.IndexSHA256) {
		return fmt.Errorf("runtime recovery binding mismatch")
	}
	if err := privatePath(location.authorityRoot, ref.Path, true); err != nil {
		return fmt.Errorf("runtime recovery source: %w", err)
	}
	fileDigest, recoveryBytes, err := fileSHA256(ref.Path)
	if err != nil || fileDigest != ref.FileSHA256 {
		return fmt.Errorf("runtime recovery source changed")
	}
	var recovery formalV15RecoveryIndex
	if err := json.Unmarshal(recoveryBytes, &recovery); err != nil {
		return fmt.Errorf("runtime recovery source: %w", err)
	}
	self, err := selfDigest(recoveryBytes, "indexSHA256")
	if err != nil || self != recovery.IndexSHA256 || recovery.IndexSHA256 != ref.IndexSHA256 ||
		recovery.SchemaVersion != 1 || recovery.Kind != "CLEARDEV_FORMAL_RECOVERY_V14_INDEX" || recovery.PlanCommit != formalStoragePlanCommit ||
		!commitPattern.MatchString(recovery.SourceCandidate) || recovery.NextPlanUnitID != "R1-T3-G1" {
		return fmt.Errorf("runtime recovery binding mismatch")
	}
	if !shaPattern.MatchString(recovery.SourceRequest.RequestSHA256) ||
		recovery.SourceRequest.Request.Path != filepath.Join(location.authorityRoot, "requests", recovery.SourceRequest.RequestSHA256, "request.json") ||
		!shaPattern.MatchString(recovery.SourceRequest.Request.SHA256) {
		return fmt.Errorf("runtime recovery base request mismatch")
	}
	if err := privatePath(location.authorityRoot, recovery.SourceRequest.Request.Path, true); err != nil {
		return fmt.Errorf("runtime recovery base request: %w", err)
	}
	sourceFileSHA, sourceBytes, err := fileSHA256(recovery.SourceRequest.Request.Path)
	if err != nil || sourceFileSHA != recovery.SourceRequest.Request.SHA256 ||
		(recovery.SourceRequest.Request.Bytes > 0 && len(sourceBytes) != recovery.SourceRequest.Request.Bytes) {
		return fmt.Errorf("runtime recovery base request changed")
	}
	var source formalV15SourceRequest
	if err := json.Unmarshal(sourceBytes, &source); err != nil {
		return fmt.Errorf("runtime recovery base request: %w", err)
	}
	sourceSelf, err := selfDigest(sourceBytes, "requestSHA256")
	if err != nil || sourceSelf != recovery.SourceRequest.RequestSHA256 || source.RequestSHA256 != recovery.SourceRequest.RequestSHA256 ||
		source.SchemaVersion != 1 || source.Kind != formalRequestKind || source.Status != "PENDING_USER_APPROVAL" || source.CandidateCommit != recovery.SourceCandidate ||
		source.ExecutionScope.Sendable != 54 || source.ExecutionScope.Main != 43 || source.ExecutionScope.Followup != 11 ||
		source.ExecutionScope.ThresholdTokens != 70_750_000 || source.ExecutionScope.ThresholdMinutes != 2_210 ||
		len(source.ExecutionScope.PlanUnitIDs) != 54 {
		return fmt.Errorf("runtime recovery base scope mismatch")
	}
	excluded := make(map[string]bool, len(recovery.Used)+1)
	for _, used := range recovery.Used {
		if used.PlanUnitID == "" || excluded[used.PlanUnitID] {
			return fmt.Errorf("runtime recovery excluded scope mismatch")
		}
		excluded[used.PlanUnitID] = true
	}
	if recovery.NoParent.PlanUnitID == "" || excluded[recovery.NoParent.PlanUnitID] {
		return fmt.Errorf("runtime recovery excluded scope mismatch")
	}
	excluded[recovery.NoParent.PlanUnitID] = true
	if len(excluded) != 5 || len(recovery.ExcludedPlanUnitIDs) != 5 {
		return fmt.Errorf("runtime recovery excluded scope mismatch")
	}
	listed := make(map[string]bool, len(recovery.ExcludedPlanUnitIDs))
	for _, planUnitID := range recovery.ExcludedPlanUnitIDs {
		if !excluded[planUnitID] || listed[planUnitID] {
			return fmt.Errorf("runtime recovery excluded scope mismatch")
		}
		listed[planUnitID] = true
	}
	expected := make([]string, 0, 49)
	seenSource := make(map[string]bool, len(source.ExecutionScope.PlanUnitIDs))
	for _, planUnitID := range source.ExecutionScope.PlanUnitIDs {
		if planUnitID == "" || seenSource[planUnitID] {
			return fmt.Errorf("runtime recovery base scope mismatch")
		}
		seenSource[planUnitID] = true
		if !excluded[planUnitID] {
			expected = append(expected, planUnitID)
		}
	}
	if len(expected) != 49 || expected[0] != recovery.NextPlanUnitID || !equalStrings(expected, request.ExecutionScope.PlanUnitIDs) {
		return fmt.Errorf("runtime request execution scope mismatch")
	}
	return nil
}

func validateFormalV15StorageSource(binding Binding, location runtimeLocation, path, expectedFileSHA256, expectedBindingSHA256 string) error {
	policy := activeRuntimePathPolicy
	if path != policy.storageBindingPath {
		return fmt.Errorf("runtime storage source path mismatch")
	}
	if err := privatePath(location.authorityRoot, path, true); err != nil {
		return fmt.Errorf("runtime storage source: %w", err)
	}
	fileDigest, contents, err := fileSHA256(path)
	if err != nil || fileDigest != expectedFileSHA256 {
		return fmt.Errorf("runtime storage source changed")
	}
	var storage formalV15StorageBinding
	if err := json.Unmarshal(contents, &storage); err != nil {
		return fmt.Errorf("runtime storage source: %w", err)
	}
	self, err := selfDigest(contents, "bindingSHA256")
	if err != nil || self != storage.BindingSHA256 || storage.BindingSHA256 != expectedBindingSHA256 {
		return fmt.Errorf("runtime storage binding digest mismatch")
	}
	if storage.SchemaVersion != 1 || storage.Kind != formalStorageBindingKind || storage.PlanCommit != formalStoragePlanCommit ||
		storage.Mount.Target != "/data" || storage.Mount.FileSystem != "ext4" || storage.Mount.UUID != formalStorageUUID ||
		storage.Mount.DeviceSerial != formalStorageDeviceSerial ||
		storage.Roots.Control != location.authorityRoot || storage.Roots.Data != policy.batchDataRoot ||
		storage.Roots.Attempts != policy.batchAttemptsRoot || storage.Roots.Tmp != policy.batchTmpRoot ||
		storage.Roots.RuntimeCache != policy.batchRuntimeCache {
		return fmt.Errorf("runtime storage binding mismatch")
	}
	mountDev, err := privateExactDirectory(policy.batchDataRoot)
	if err != nil {
		return err
	}
	attemptsDev, err := privateExactDirectory(policy.batchAttemptsRoot)
	if err != nil {
		return err
	}
	tmpDev, err := privateExactDirectory(policy.batchTmpRoot)
	if err != nil {
		return err
	}
	cacheDev, err := privateExactDirectory(policy.batchRuntimeCache)
	if err != nil {
		return err
	}
	attemptDev, err := privateExactDirectory(location.runtimeRoot)
	if err != nil {
		return err
	}
	projectDev, err := privateExactDirectory(binding.ProjectRoot)
	if err != nil {
		return err
	}
	if mountDev != storage.Devices.MountDev || attemptsDev != storage.Devices.AttemptsDev ||
		tmpDev != storage.Devices.TmpDev || cacheDev != storage.Devices.RuntimeCacheDev ||
		attemptDev != storage.Devices.AttemptsDev || projectDev != storage.Devices.TmpDev {
		return fmt.Errorf("runtime storage device mismatch")
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsScene(scenes []int, scene int) bool {
	for _, item := range scenes {
		if item == scene {
			return true
		}
	}
	return false
}
