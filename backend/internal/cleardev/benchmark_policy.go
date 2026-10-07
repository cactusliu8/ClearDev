package cleardev

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
	"slices"
	"strings"
	"time"
)

// BenchmarkBackendManifestKind and the related exported constants freeze the S12B v1 benchmark binding vocabulary.
const (
	BenchmarkBackendManifestKind = "CLEARDEV_BENCHMARK_BACKEND_MANIFEST"
	BenchmarkPurposeOffline      = "OFFLINE_FIXTURE"
	BenchmarkFacilityVersionV1   = "s12b-v1"
	BenchmarkCheckProfileNodeTS  = "node-ts-http-v1"

	BenchmarkGroupG3 = "G3"
	BenchmarkGroupG4 = "G4"

	BenchmarkModeStandardOnly BenchmarkModePolicy = "STANDARD_ONLY"
	BenchmarkModeDynamic      BenchmarkModePolicy = "DYNAMIC"
)

var (
	benchmarkSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	benchmarkGitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// BenchmarkModePolicy is persisted with one benchmark-bound requirement. It is
// deliberately narrower than WorkMode: callers cannot choose QUICK/PARALLEL.
type BenchmarkModePolicy string

// BenchmarkBackendManifest is the immutable, per-daemon benchmark scene
// binding loaded from CLEARDEV_BENCHMARK_MANIFEST. Runtime-only fields are not
// supplied by the file and are filled by LoadBenchmarkBackendManifest.
type BenchmarkBackendManifest struct {
	SchemaVersion     int                 `json:"schemaVersion"`
	Kind              string              `json:"kind"`
	Purpose           string              `json:"purpose"`
	FacilityVersion   string              `json:"facilityVersion"`
	RepositoryCommit  string              `json:"repositoryCommit"`
	FacilityCommit    string              `json:"facilityCommit"`
	ProtocolCommit    string              `json:"protocolCommit"`
	MaterialCommit    string              `json:"materialCommit"`
	MaterialSHA256    string              `json:"materialSha256"`
	PublicInputSHA256 string              `json:"publicInputSha256"`
	PlanUnitID        string              `json:"planUnitId"`
	Scene             int                 `json:"scene"`
	Group             string              `json:"group"`
	Policy            BenchmarkModePolicy `json:"policy"`
	CheckProfile      string              `json:"checkProfile"`
	CheckPrefix       string              `json:"checkPrefix"`
	ProjectRoot       string              `json:"projectRoot"`
	IsolationRoot     string              `json:"isolationRoot"`

	Path           string `json:"-"`
	ManifestSHA256 string `json:"-"`
}

// BenchmarkBinding is the durable copy written atomically with requirement
// creation. A daemon restart must present the same manifest digest before any
// benchmark-controlled planning or execution is allowed to continue.
type BenchmarkBinding struct {
	DevelopmentRequirementID string
	AOProjectID              string
	ProjectRoot              string
	ManifestPath             string
	ManifestSHA256           string
	FacilityVersion          string
	Purpose                  string
	PlanUnitID               string
	Scene                    int
	Group                    string
	Policy                   BenchmarkModePolicy
	CheckProfile             string
	CheckPrefix              string
	CheckProfileSHA256       string
	PublicInputSHA256        string
	MaterialSHA256           string
	CreatedAt                time.Time
}

// LoadBenchmarkBackendManifest validates a strict per-scene manifest. An empty
// path means the ordinary, non-benchmark daemon mode.
func LoadBenchmarkBackendManifest(path string) (*BenchmarkBackendManifest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("CLEARDEV_BENCHMARK_MANIFEST must be an absolute path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read benchmark manifest: %w", err)
	}
	var manifest BenchmarkBackendManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode benchmark manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("benchmark manifest contains trailing JSON values")
		}
		return nil, fmt.Errorf("decode benchmark manifest trailing data: %w", err)
	}
	if err := ValidateBenchmarkBackendManifest(manifest); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	manifest.Path = filepath.Clean(path)
	manifest.ManifestSHA256 = hex.EncodeToString(digest[:])
	return &manifest, nil
}

// ValidateBenchmarkBackendManifest checks the strict immutable per-scene manifest contract before any benchmark work is accepted.
func ValidateBenchmarkBackendManifest(manifest BenchmarkBackendManifest) error {
	if err := validateBenchmarkPurpose(manifest); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"repositoryCommit": manifest.RepositoryCommit,
		"facilityCommit":   manifest.FacilityCommit,
		"protocolCommit":   manifest.ProtocolCommit,
		"materialCommit":   manifest.MaterialCommit,
	} {
		if !benchmarkGitSHAPattern.MatchString(value) {
			return fmt.Errorf("benchmark manifest %s is not a full Git SHA", name)
		}
	}
	for name, value := range map[string]string{
		"materialSha256":    manifest.MaterialSHA256,
		"publicInputSha256": manifest.PublicInputSHA256,
	} {
		if !benchmarkSHA256Pattern.MatchString(value) {
			return fmt.Errorf("benchmark manifest %s is not SHA-256", name)
		}
	}
	if strings.TrimSpace(manifest.PlanUnitID) == "" || manifest.Scene < 1 {
		return fmt.Errorf("benchmark manifest plan unit or scene is invalid")
	}
	switch manifest.Group {
	case BenchmarkGroupG3:
		if manifest.Policy != BenchmarkModeStandardOnly {
			return fmt.Errorf("G3 benchmark manifest must use STANDARD_ONLY")
		}
	case BenchmarkGroupG4:
		if manifest.Policy != BenchmarkModeDynamic {
			return fmt.Errorf("G4 benchmark manifest must use DYNAMIC")
		}
	default:
		return fmt.Errorf("benchmark backend manifest group must be G3 or G4")
	}
	if manifest.CheckProfile != BenchmarkCheckProfileNodeTS {
		return fmt.Errorf("unsupported benchmark check profile %q", manifest.CheckProfile)
	}
	if _, err := BenchmarkComplexCheckCatalog(manifest.CheckPrefix); err != nil {
		return err
	}
	if !filepath.IsAbs(manifest.ProjectRoot) || !filepath.IsAbs(manifest.IsolationRoot) {
		return fmt.Errorf("benchmark projectRoot and isolationRoot must be absolute")
	}
	project := filepath.Clean(manifest.ProjectRoot)
	isolation := filepath.Clean(manifest.IsolationRoot)
	if project == isolation || !strings.HasPrefix(project+string(filepath.Separator), isolation+string(filepath.Separator)) {
		return fmt.Errorf("benchmark projectRoot must be inside isolationRoot")
	}
	return nil
}

// NewBenchmarkBinding creates the durable requirement-bound copy of one validated startup manifest.
func NewBenchmarkBinding(manifest BenchmarkBackendManifest, requirementID, projectID string, at time.Time) (BenchmarkBinding, error) {
	if err := ValidateBenchmarkBackendManifest(manifest); err != nil {
		return BenchmarkBinding{}, err
	}
	if !benchmarkSHA256Pattern.MatchString(manifest.ManifestSHA256) || !filepath.IsAbs(manifest.Path) {
		return BenchmarkBinding{}, fmt.Errorf("benchmark manifest runtime binding is incomplete")
	}
	if strings.TrimSpace(requirementID) == "" || strings.TrimSpace(projectID) == "" {
		return BenchmarkBinding{}, fmt.Errorf("benchmark requirement/project binding is empty")
	}
	return BenchmarkBinding{
		DevelopmentRequirementID: requirementID,
		AOProjectID:              projectID,
		ProjectRoot:              filepath.Clean(manifest.ProjectRoot),
		ManifestPath:             filepath.Clean(manifest.Path),
		ManifestSHA256:           manifest.ManifestSHA256,
		FacilityVersion:          manifest.FacilityVersion,
		Purpose:                  manifest.Purpose,
		PlanUnitID:               manifest.PlanUnitID,
		Scene:                    manifest.Scene,
		Group:                    manifest.Group,
		Policy:                   manifest.Policy,
		CheckProfile:             manifest.CheckProfile,
		CheckPrefix:              manifest.CheckPrefix,
		CheckProfileSHA256:       BenchmarkCheckProfileSHA256(manifest.CheckPrefix),
		PublicInputSHA256:        manifest.PublicInputSHA256,
		MaterialSHA256:           manifest.MaterialSHA256,
		CreatedAt:                at.UTC(),
	}, nil
}

// MatchesManifest reports whether a durable binding exactly matches the daemon's current immutable startup manifest.
func (binding BenchmarkBinding) MatchesManifest(manifest *BenchmarkBackendManifest) bool {
	if manifest == nil {
		return false
	}
	return binding.ManifestSHA256 == manifest.ManifestSHA256 &&
		binding.ManifestPath == filepath.Clean(manifest.Path) &&
		binding.ProjectRoot == filepath.Clean(manifest.ProjectRoot) &&
		binding.PlanUnitID == manifest.PlanUnitID && binding.Scene == manifest.Scene &&
		binding.Group == manifest.Group && binding.Policy == manifest.Policy &&
		binding.CheckProfile == manifest.CheckProfile && binding.CheckPrefix == manifest.CheckPrefix &&
		binding.CheckProfileSHA256 == BenchmarkCheckProfileSHA256(manifest.CheckPrefix) &&
		binding.PublicInputSHA256 == manifest.PublicInputSHA256 && binding.MaterialSHA256 == manifest.MaterialSHA256
}

// BenchmarkComplexCheckCatalog returns one of the four frozen benchmark check catalogs with exact argv and timeouts.
func BenchmarkComplexCheckCatalog(prefix string) ([]ComplexCheckSpec, error) {
	prefix = strings.TrimSpace(prefix)
	if !slices.Contains([]string{"CHK-LIB", "CHK-INV", "CHK-TKT", "CHK-DEV"}, prefix) {
		return nil, fmt.Errorf("unsupported benchmark check prefix %q", prefix)
	}
	return []ComplexCheckSpec{
		{ID: prefix + "-TYPE", Argv: []string{"npm", "run", "typecheck"}, TimeoutSeconds: 60, MainPaths: []string{"src/**", "test/**"}},
		{ID: prefix + "-BACKEND", Argv: []string{"npm", "run", "test:backend"}, TimeoutSeconds: 60, MainPaths: []string{"src/**"}},
		{ID: prefix + "-DATABASE", Argv: []string{"npm", "run", "test:database"}, TimeoutSeconds: 60, MainPaths: []string{"src/**", "migrations/**"}},
		{ID: prefix + "-API", Argv: []string{"npm", "run", "test:api"}, TimeoutSeconds: 60, MainPaths: []string{"src/**"}},
		{ID: prefix + "-ALL", Argv: []string{"npm", "test"}, TimeoutSeconds: 60, MainPaths: []string{"src/**", "migrations/**", "test/**"}},
	}, nil
}

// BenchmarkCheckProfileSHA256 returns the stable digest of a frozen benchmark check catalog.
func BenchmarkCheckProfileSHA256(prefix string) string {
	catalog, err := BenchmarkComplexCheckCatalog(prefix)
	if err != nil {
		return ""
	}
	raw, _ := json.Marshal(catalog)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// ComplexCheckByID resolves an exact check identifier from an explicitly supplied frozen catalog.
func ComplexCheckByID(catalog []ComplexCheckSpec, id string) (ComplexCheckSpec, bool) {
	for _, spec := range catalog {
		if spec.ID == id {
			return spec, true
		}
	}
	return ComplexCheckSpec{}, false
}

// SelectComplexExecutionModeForPolicy applies the persisted G3/G4 benchmark policy to the ordinary production mode selection.
func SelectComplexExecutionModeForPolicy(tasks []ComplexPlanTask, suggestedCount int, policy BenchmarkModePolicy) (ComplexModeSelection, error) {
	selection, err := SelectComplexExecutionMode(tasks, suggestedCount)
	if err != nil {
		return ComplexModeSelection{}, err
	}
	switch policy {
	case "", BenchmarkModeDynamic:
		return selection, nil
	case BenchmarkModeStandardOnly:
		wasParallel := selection.Mode == WorkModeParallel
		selection.Mode = WorkModeStandard
		selection.BuilderCount = 1
		// Preserve the ordinary reason when the approved graph already selects
		// STANDARD. Only a graph that would otherwise run PARALLEL needs the
		// historical degraded reason encoding; the durable BenchmarkBinding is the
		// authoritative evidence that this is the G3 fixed-policy case.
		if wasParallel {
			selection.ReasonCode = ReasonParallelUnsafeDegrade
		}
		selection.Batches = nil
		return selection, nil
	default:
		return ComplexModeSelection{}, fmt.Errorf("unsupported benchmark mode policy %q", policy)
	}
}
