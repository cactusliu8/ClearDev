package benchmarkruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestPrivateRuntimeBindingRejectsPurposeAndChangedAuthority(t *testing.T) {
	root := t.TempDir()
	scene := filepath.Join(root, "scene")
	data := filepath.Join(scene, "ao")
	project := filepath.Join(t.TempDir(), "product")
	for _, path := range []string{scene, data, project} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(scene, "runtime.json")
	candidate := strings.Repeat("a", 40)
	digest := func(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
	prepared, err := json.Marshal(map[string]any{"schemaVersion": 3, "kind": "CLEARDEV_LIVE_BINDING", "manifest": map[string]any{
		"recordClass": "DEV_LIVE", "planCommit": PlanCommit, "candidateCommit": candidate, "group": "G1", "planUnitId": "DEV-SENSOR-G1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := json.Marshal(map[string]any{"kind": "CLEARDEV_LIVE_AUTHORIZATION", "recordClass": "DEV_LIVE", "planCommit": PlanCommit, "candidateCommit": candidate, "bindingSHA256": digest(prepared)})
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{SchemaVersion: 3, Kind: "CLEARDEV_ROLE_RUNTIME", RecordClass: "DEV_LIVE", PlanCommit: PlanCommit, CandidateCommit: candidate, Group: "G1",
		DataRoot: data, ProjectRoot: project, BindingPath: filepath.Join(scene, "binding.json"), BindingSHA256: digest(prepared),
		AuthorizationPath: filepath.Join(scene, "authorization.json"), AuthorizationSHA256: digest(authority), SocketPath: filepath.Join(scene, "runtime.sock")}
	write := func(path string, raw []byte) {
		t.Helper()
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(binding.BindingPath, prepared)
	write(binding.AuthorizationPath, authority)
	save := func() {
		t.Helper()
		raw, err := json.Marshal(binding)
		if err != nil {
			t.Fatal(err)
		}
		write(path, raw)
	}
	save()
	if _, err := load(path, data, root, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := load(path, project, root, nil); err == nil {
		t.Fatal("wrong AO data root accepted")
	}
	// A record class outside the two live purposes is refused before the
	// authority file is read: the frozen FORMAL purpose is now a supported
	// record class, an arbitrary one must never become authority.
	binding.RecordClass = "OFFLINE_FIXTURE"
	save()
	if err := os.Remove(binding.AuthorizationPath); err != nil {
		t.Fatal(err)
	}
	if _, err := load(path, data, root, nil); err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("unknown purpose must reject before authority read: %v", err)
	}
	binding.RecordClass = "DEV_LIVE"
	save()
	write(binding.AuthorizationPath, []byte("{}"))
	if _, err := load(path, data, root, nil); err == nil {
		t.Fatal("changed authorization accepted")
	}
	binding.AuthorizationSHA256 = digest([]byte("{}"))
	save()
	if _, err := load(path, data, root, nil); err == nil {
		t.Fatal("self-rehashed empty authorization accepted")
	}
	write(binding.AuthorizationPath, authority)
	binding.AuthorizationSHA256 = digest(authority)
	save()
	if err := os.Chmod(binding.AuthorizationPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := load(path, data, root, nil); err == nil {
		t.Fatal("public authority file accepted")
	}
}

// The FORMAL record class is the frozen S12C purpose. It keeps every DEV guard
// (schema, kind, plan commit, digests, group, product binding) and differs only
// in the authorised unit namespace: only the frozen formal plan ids for the
// product groups are accepted, DEV units are not.
func TestFormalBatchRuntimeBindingRequiresMatchingBatchIdentity(t *testing.T) {
	root := t.TempDir()
	scene := filepath.Join(root, "scene")
	data := filepath.Join(scene, "ao")
	project := filepath.Join(t.TempDir(), "product")
	for _, path := range []string{scene, data, project, filepath.Join(root, "authority"), filepath.Join(root, "requests")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	candidate := strings.Repeat("d", 40)
	batchManifestSHA256 := strings.Repeat("e", 64)
	digest := func(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
	batchRaw, err := json.Marshal(map[string]any{
		"kind": "CLEARDEV_FORMAL_BATCH", "batchID": FormalBatchID, "root": root,
		"manifestSHA256": batchManifestSHA256, "plan": map[string]any{"commit": "35c0e6f22c6d92c81790d1da0e8ab6fe7d9551fc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "batch.json"), batchRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := json.Marshal(map[string]any{"schemaVersion": 3, "kind": "CLEARDEV_LIVE_BINDING", "manifest": map[string]any{
		"recordClass": core.BenchmarkPurposeFormal, "batchID": FormalBatchID, "planCommit": PlanCommit, "candidateCommit": candidate,
		"group": "G1", "planUnitId": "R1-T1-G1", "scene": 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	requestSHA256 := strings.Repeat("f", 64)
	requestPath := filepath.Join(root, "requests", requestSHA256, "request.json")
	registration := map[string]any{
		"schemaVersion": 1, "kind": "CLEARDEV_FORMAL_OWNER_REGISTRATION", "source": "USER_DECISION",
		"decidedBy": "test", "decidedAt": 1, "approved": true, "revoked": false,
		"requestSHA256": requestSHA256, "requestPath": requestPath,
		"batchID": FormalBatchID, "batchManifestSHA256": batchManifestSHA256,
		"scope": map[string]any{"planUnitIds": []string{"R1-T1-G1"}, "groups": []string{"G1"}, "scenes": []int{1}},
	}
	registrationBytes, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	registrationPath := filepath.Join(root, "authority", "registration.json")
	if err := os.WriteFile(registrationPath, registrationBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	authority, err := json.Marshal(map[string]any{
		"kind": "CLEARDEV_LIVE_AUTHORIZATION", "recordClass": core.BenchmarkPurposeFormal, "batchID": FormalBatchID,
		"planCommit": PlanCommit, "candidateCommit": candidate, "bindingSHA256": digest(prepared),
		"requestSHA256": requestSHA256, "registrationSha256": digest(registrationBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	bindingPath := filepath.Join(scene, "binding.json")
	authorizationPath := filepath.Join(scene, "authorization.json")
	if err := os.WriteFile(bindingPath, prepared, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authorizationPath, authority, 0o600); err != nil {
		t.Fatal(err)
	}
	binding := Binding{
		SchemaVersion: 3, Kind: "CLEARDEV_ROLE_RUNTIME", RecordClass: core.BenchmarkPurposeFormal,
		BatchID: FormalBatchID, BatchManifestSHA256: batchManifestSHA256,
		PlanCommit: PlanCommit, CandidateCommit: candidate, Group: "G1", DataRoot: data, ProjectRoot: project,
		BindingPath: bindingPath, BindingSHA256: digest(prepared), AuthorizationPath: authorizationPath,
		AuthorizationSHA256: digest(authority), RegistrationPath: registrationPath,
		RegistrationSHA256: digest(registrationBytes), SocketPath: filepath.Join(scene, "runtime.sock"),
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(scene, "runtime.json")
	if err := os.WriteFile(runtimePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(runtimePath, data, root, nil); err != nil {
		t.Fatalf("matching batch binding rejected: %v", err)
	}
	binding.BatchManifestSHA256 = strings.Repeat("a", 64)
	raw, _ = json.Marshal(binding)
	if err := os.WriteFile(runtimePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(runtimePath, data, root, nil); err == nil || !strings.Contains(err.Error(), "batch manifest mismatch") {
		t.Fatalf("wrong batch manifest accepted: %v", err)
	}
}

func TestFormalRuntimeBindingUnitNamespace(t *testing.T) {
	root := t.TempDir()
	scene := filepath.Join(root, "scene")
	data := filepath.Join(scene, "ao")
	project := filepath.Join(t.TempDir(), "product")
	for _, path := range []string{scene, data, project} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	candidate := strings.Repeat("b", 40)
	digest := func(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
	preparedFor := func(recordClass, group, unitID string) []byte {
		raw, err := json.Marshal(map[string]any{"schemaVersion": 3, "kind": "CLEARDEV_LIVE_BINDING", "manifest": map[string]any{
			"recordClass": recordClass, "planCommit": PlanCommit, "candidateCommit": candidate, "group": group, "planUnitId": unitID, "scene": 1}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// G3/G4 require a product manifest with exactly the same purpose.
	productManifest := func(purpose, group string) *core.BenchmarkBackendManifest {
		return &core.BenchmarkBackendManifest{Purpose: purpose, Group: group, ProjectRoot: project, FacilityCommit: candidate}
	}
	// A FORMAL binding must also name the owner registration that approves this
	// request and covers this unit, group and scene; it lives under the formal
	// control root, so the test writes a private registration there and removes
	// it again.
	registrationRoot, err := os.MkdirTemp(root, "test-registration-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(registrationRoot)
	requestSHA256 := strings.Repeat("c", 64)
	registration := map[string]any{
		"schemaVersion": 1, "kind": "CLEARDEV_FORMAL_OWNER_REGISTRATION", "source": "USER_DECISION",
		"decidedBy": "test", "decidedAt": 1, "approved": true, "revoked": false,
		"requestSHA256": requestSHA256, "requestPath": filepath.Join(registrationRoot, "request.json"),
		"scope": map[string]any{"planUnitIds": []string{"R1-T1-G3", "R4-T2F-G4"}, "groups": []string{"G3", "G4"}, "scenes": []int{1, 2}},
	}
	registrationBytes, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	registrationPath := filepath.Join(registrationRoot, "registration.json")
	if err := os.WriteFile(registrationPath, registrationBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	loadCase := func(purpose, group, unitID string, product *core.BenchmarkBackendManifest) error {
		prepared := preparedFor(purpose, group, unitID)
		authorityFields := map[string]any{"kind": "CLEARDEV_LIVE_AUTHORIZATION", "recordClass": purpose, "planCommit": PlanCommit, "candidateCommit": candidate, "bindingSHA256": digest(prepared)}
		if purpose == core.BenchmarkPurposeFormal {
			authorityFields["requestSHA256"] = requestSHA256
			authorityFields["registrationSha256"] = digest(registrationBytes)
		}
		authority, err := json.Marshal(authorityFields)
		if err != nil {
			t.Fatal(err)
		}
		caseRoot, err := os.MkdirTemp(root, "case-")
		if err != nil {
			t.Fatal(err)
		}
		bindingPath := filepath.Join(caseRoot, "binding.json")
		authorizationPath := filepath.Join(caseRoot, "authorization.json")
		if err := os.WriteFile(bindingPath, prepared, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authorizationPath, authority, 0o600); err != nil {
			t.Fatal(err)
		}
		binding := Binding{SchemaVersion: 3, Kind: "CLEARDEV_ROLE_RUNTIME", RecordClass: purpose, PlanCommit: PlanCommit, CandidateCommit: candidate, Group: group,
			DataRoot: data, ProjectRoot: project, BindingPath: bindingPath, BindingSHA256: digest(prepared),
			AuthorizationPath: authorizationPath, AuthorizationSHA256: digest(authority), SocketPath: filepath.Join(caseRoot, "runtime.sock")}
		if purpose == core.BenchmarkPurposeFormal {
			binding.RegistrationPath = registrationPath
			binding.RegistrationSHA256 = digest(registrationBytes)
		}
		raw, err := json.Marshal(binding)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(caseRoot, "runtime.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = load(path, data, root, product)
		return err
	}
	if err := loadCase(core.BenchmarkPurposeFormal, "G3", "R1-T1-G3", productManifest(core.BenchmarkPurposeFormal, "G3")); err != nil {
		t.Fatalf("formal product unit rejected: %v", err)
	}
	if err := loadCase(core.BenchmarkPurposeFormal, "G4", "R4-T2F-G4", productManifest(core.BenchmarkPurposeFormal, "G4")); err != nil {
		t.Fatalf("formal follow-up unit rejected: %v", err)
	}
	for name, testCase := range map[string]struct {
		purpose, group, unit string
		product              *core.BenchmarkBackendManifest
	}{
		"DEV unit under FORMAL":       {core.BenchmarkPurposeFormal, "G3", "DEV-SENSOR-G3", productManifest(core.BenchmarkPurposeFormal, "G3")},
		"unit group mismatch":         {core.BenchmarkPurposeFormal, "G3", "R1-T1-G4", productManifest(core.BenchmarkPurposeFormal, "G3")},
		"base group under FORMAL":     {core.BenchmarkPurposeFormal, "G3", "R1-T1-G1", productManifest(core.BenchmarkPurposeFormal, "G3")},
		"purpose mismatch on product": {core.BenchmarkPurposeFormal, "G3", "R1-T1-G3", productManifest(core.BenchmarkPurposeDevLive, "G3")},
		"missing product binding":     {core.BenchmarkPurposeFormal, "G3", "R1-T1-G3", nil},
	} {
		if err := loadCase(testCase.purpose, testCase.group, testCase.unit, testCase.product); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

type formalV15Fixture struct {
	t                 *testing.T
	policy            runtimePathPolicy
	attemptID         string
	unitID            string
	group             string
	candidate         string
	batchDigest       string
	runtimeRoot       string
	dataRoot          string
	projectRoot       string
	runtimePath       string
	bindingPath       string
	authorizationPath string
	registrationPath  string
	requestPath       string
	product           *core.BenchmarkBackendManifest
	prepared          []byte
	runtimeAuth       map[string]any
	runtimeBinding    Binding
}

func testDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func mustPrivateDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func canonicalMapDigest(t *testing.T, value map[string]any) string {
	t.Helper()
	raw, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return testDigest(raw)
}

func stringValues(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

func formalV15TestBaseScope() []string {
	return []string{
		"R1-T2-G2",
		"R1-T2F-G2",
		"R1-T2-G3",
		"R1-T2F-G3",
		"R1-T2-G4",
		"R1-T3-G1",
		"R1-T3-G2",
		"R1-T3-G3",
		"R1-T3-G4",
		"R2-T1-G2",
		"R2-T1-G3",
		"R2-T1-G4",
		"R2-T1-G1",
		"R2-T2-G2",
		"R2-T2F-G2",
		"R2-T2-G3",
		"R2-T2F-G3",
		"R2-T2-G4",
		"R2-T2-G1",
		"R2-T2F-G1",
		"R2-T3-G2",
		"R2-T3-G3",
		"R2-T3-G4",
		"R2-T3-G1",
		"R3-T1-G3",
		"R3-T1-G4",
		"R3-T1-G1",
		"R3-T1-G2",
		"R3-T2-G3",
		"R3-T2F-G3",
		"R3-T2-G4",
		"R3-T2-G1",
		"R3-T2F-G1",
		"R3-T2-G2",
		"R3-T2F-G2",
		"R3-T3-G3",
		"R3-T3-G4",
		"R3-T3-G1",
		"R3-T3-G2",
		"R4-T1-G4",
		"R4-T1-G1",
		"R4-T1-G2",
		"R4-T1-G3",
		"R4-T2-G4",
		"R4-T2-G1",
		"R4-T2F-G1",
		"R4-T2-G2",
		"R4-T2F-G2",
		"R4-T2-G3",
		"R4-T2F-G3",
		"R4-T3-G4",
		"R4-T3-G1",
		"R4-T3-G2",
		"R4-T3-G3",
	}
}

func formalV15TestExcludedScope() []string {
	return []string{"R1-T2-G2", "R1-T2F-G2", "R1-T2-G3", "R1-T2F-G3", "R1-T2-G4"}
}

func formalV15TestAllowedScope() []string {
	excluded := map[string]bool{}
	for _, planUnitID := range formalV15TestExcludedScope() {
		excluded[planUnitID] = true
	}
	allowed := make([]string, 0, 49)
	for _, planUnitID := range formalV15TestBaseScope() {
		if !excluded[planUnitID] {
			allowed = append(allowed, planUnitID)
		}
	}
	return allowed
}

func setRuntimePolicyForTest(t *testing.T, policy runtimePathPolicy) {
	t.Helper()
	previous := activeRuntimePathPolicy
	activeRuntimePathPolicy = policy
	t.Cleanup(func() { activeRuntimePathPolicy = previous })
}

func newFormalV15Fixture(t *testing.T, group, unitID string) *formalV15Fixture {
	return newFormalV15FixtureWithScope(t, group, unitID, nil)
}

func newFormalV15FixtureWithScope(t *testing.T, group, unitID string, scopeOverride []string) *formalV15Fixture {
	t.Helper()
	base := t.TempDir()
	policy := runtimePathPolicy{
		controlRoot:       filepath.Join(base, "dev-control"),
		formalControlRoot: filepath.Join(base, "formal-control"),
		batchControlRoot:  filepath.Join(base, "batch-control"),
		batchDataRoot:     filepath.Join(base, "data", "s12c-formal-01"),
		batchAttemptsRoot: filepath.Join(base, "data", "s12c-formal-01", "attempts"),
		batchTmpRoot:      filepath.Join(base, "data", "s12c-formal-01", "tmp"),
		batchRuntimeCache: filepath.Join(base, "data", "runtime-cache-v14"),
	}
	policy.storageBindingPath = filepath.Join(policy.batchControlRoot, "evidence", "storage-v14", "binding.json")
	policy.recoveryIndexPath = filepath.Join(policy.batchControlRoot, "evidence", "recovery-v14", "index.json")
	for _, path := range []string{
		policy.controlRoot, policy.formalControlRoot, policy.batchControlRoot,
		filepath.Join(policy.batchControlRoot, "requests"), filepath.Join(policy.batchControlRoot, "authority"),
		filepath.Dir(policy.storageBindingPath), filepath.Dir(policy.recoveryIndexPath),
		policy.batchDataRoot, policy.batchAttemptsRoot, policy.batchTmpRoot, policy.batchRuntimeCache,
	} {
		mustPrivateDir(t, path)
	}
	setRuntimePolicyForTest(t, policy)

	attemptID := "f0e9d8c7-1111-4222-8333-444455556666"
	runtimeRoot := filepath.Join(policy.batchAttemptsRoot, attemptID)
	dataRoot := filepath.Join(runtimeRoot, "ao")
	productRoot := filepath.Join(policy.batchTmpRoot, "formal-product-test")
	projectRoot := filepath.Join(productRoot, "project")
	for _, path := range []string{runtimeRoot, dataRoot, productRoot, projectRoot} {
		mustPrivateDir(t, path)
	}

	candidate := strings.Repeat("d", 40)
	batchDigest := strings.Repeat("e", 64)
	writeJSONFile(t, filepath.Join(policy.batchControlRoot, "batch.json"), map[string]any{
		"kind": "CLEARDEV_FORMAL_BATCH", "batchID": FormalBatchID, "root": policy.batchControlRoot,
		"manifestSHA256": batchDigest, "plan": map[string]any{"commit": "35c0e6f22c6d92c81790d1da0e8ab6fe7d9551fc"},
	})

	mountDev, err := privateExactDirectory(policy.batchDataRoot)
	if err != nil {
		t.Fatal(err)
	}
	attemptsDev, _ := privateExactDirectory(policy.batchAttemptsRoot)
	tmpDev, _ := privateExactDirectory(policy.batchTmpRoot)
	cacheDev, _ := privateExactDirectory(policy.batchRuntimeCache)
	storage := map[string]any{
		"schemaVersion": 1, "kind": formalStorageBindingKind, "planCommit": formalStoragePlanCommit,
		"mount":      map[string]any{"target": "/data", "fileSystem": "ext4", "uuid": formalStorageUUID, "deviceSerial": formalStorageDeviceSerial},
		"roots":      map[string]any{"control": policy.batchControlRoot, "data": policy.batchDataRoot, "attempts": policy.batchAttemptsRoot, "tmp": policy.batchTmpRoot, "runtimeCache": policy.batchRuntimeCache},
		"devices":    map[string]any{"mountDev": mountDev, "attemptsDev": attemptsDev, "tmpDev": tmpDev, "runtimeCacheDev": cacheDev},
		"thresholds": map[string]any{"dataStartAvailableBytes": 21474836480, "dataFinalReserveBytes": 5368709120, "tempMinimumAvailableBytes": 2147483648, "systemMinimumAvailableBytes": 16106127360},
	}
	storage["bindingSHA256"] = canonicalMapDigest(t, storage)
	storageRaw := writeJSONFile(t, policy.storageBindingPath, storage)

	baseScope := formalV15TestBaseScope()
	sourceCandidate := strings.Repeat("c", 40)
	sourceRequest := map[string]any{
		"schemaVersion": 1, "kind": formalRequestKind, "status": "PENDING_USER_APPROVAL", "candidateCommit": sourceCandidate,
		"executionScope": map[string]any{
			"sendable": 54, "main": 43, "followup": 11, "thresholdTokens": 70_750_000, "thresholdMinutes": 2_210,
			"planUnitIds": stringValues(baseScope),
		},
	}
	sourceRequestSHA := canonicalMapDigest(t, sourceRequest)
	sourceRequest["requestSHA256"] = sourceRequestSHA
	sourceRequestDir := filepath.Join(policy.batchControlRoot, "requests", sourceRequestSHA)
	mustPrivateDir(t, sourceRequestDir)
	sourceRequestPath := filepath.Join(sourceRequestDir, "request.json")
	sourceRequestRaw := writeJSONFile(t, sourceRequestPath, sourceRequest)

	excludedScope := formalV15TestExcludedScope()
	recovery := map[string]any{
		"schemaVersion": 1, "kind": "CLEARDEV_FORMAL_RECOVERY_V14_INDEX", "planCommit": formalStoragePlanCommit,
		"sourceCandidate": sourceCandidate,
		"sourceRequest": map[string]any{
			"requestSHA256": sourceRequestSHA,
			"request":       map[string]any{"path": sourceRequestPath, "sha256": testDigest(sourceRequestRaw), "bytes": len(sourceRequestRaw)},
		},
		"noParent": map[string]any{"planUnitId": "R1-T2F-G3"},
		"used": []any{
			map[string]any{"planUnitId": "R1-T2-G2"},
			map[string]any{"planUnitId": "R1-T2F-G2"},
			map[string]any{"planUnitId": "R1-T2-G3"},
			map[string]any{"planUnitId": "R1-T2-G4"},
		},
		"excludedPlanUnitIds": stringValues(excludedScope),
		"nextPlanUnitId":      "R1-T3-G1",
	}
	recovery["indexSHA256"] = canonicalMapDigest(t, recovery)
	recoveryRaw := writeJSONFile(t, policy.recoveryIndexPath, recovery)

	scopeIDs := formalV15TestAllowedScope()
	if scopeOverride != nil {
		scopeIDs = append([]string(nil), scopeOverride...)
	} else if !containsString(scopeIDs, unitID) {
		scopeIDs = []string{unitID}
	}
	request := map[string]any{
		"schemaVersion": 1, "kind": formalRequestKind, "status": "PENDING_USER_APPROVAL", "candidateCommit": candidate,
		"batch": map[string]any{"id": FormalBatchID, "root": policy.batchControlRoot, "manifestSHA256": batchDigest},
		"executionScope": map[string]any{
			"sendable": 49, "main": 40, "followup": 9, "thresholdTokens": 64_250_000, "thresholdMinutes": 2_010,
			"planUnitIds": stringValues(scopeIDs),
		},
		"control": map[string]any{
			"root": policy.batchControlRoot, "requests": filepath.Join(policy.batchControlRoot, "requests"),
			"attempts": filepath.Join(policy.batchControlRoot, "attempts"), "archives": filepath.Join(policy.batchControlRoot, "stop-archives"),
			"authority": filepath.Join(policy.batchControlRoot, "authority"), "registry": filepath.Join(policy.batchControlRoot, "formal-attempts.sqlite"),
		},
		"registry": map[string]any{"path": filepath.Join(policy.batchControlRoot, "formal-attempts.sqlite"), "batchManifestSHA256": batchDigest},
		"storageV14": map[string]any{
			"kind":    formalStorageRequestKind,
			"storage": map[string]any{"path": policy.storageBindingPath, "fileSHA256": testDigest(storageRaw), "bindingSHA256": storage["bindingSHA256"]},
			"roots": map[string]any{
				"control": policy.batchControlRoot, "registry": filepath.Join(policy.batchControlRoot, "formal-attempts.sqlite"),
				"historicalAttempts": filepath.Join(policy.batchControlRoot, "attempts"), "newAttempts": policy.batchAttemptsRoot,
				"tmp": policy.batchTmpRoot, "runtimeCache": policy.batchRuntimeCache,
			},
			"recovery": map[string]any{"path": policy.recoveryIndexPath, "fileSHA256": testDigest(recoveryRaw), "indexSHA256": recovery["indexSHA256"]},
		},
	}
	requestSHA := canonicalMapDigest(t, request)
	request["requestSHA256"] = requestSHA
	requestDir := filepath.Join(policy.batchControlRoot, "requests", requestSHA)
	mustPrivateDir(t, requestDir)
	requestPath := filepath.Join(requestDir, "request.json")
	writeJSONFile(t, requestPath, request)

	registration := map[string]any{
		"schemaVersion": 1, "kind": "CLEARDEV_FORMAL_OWNER_REGISTRATION", "source": "USER_DECISION",
		"decidedBy": "test-v15", "decidedAt": int64(1), "approved": true, "revoked": false,
		"requestSHA256": requestSHA, "requestPath": requestPath, "batchID": FormalBatchID, "batchManifestSHA256": batchDigest,
		"scope": map[string]any{"planUnitIds": []string{unitID}, "groups": []string{group}, "scenes": []int{1}},
	}
	registrationPath := filepath.Join(policy.batchControlRoot, "authority", requestSHA+".json")
	registrationRaw := writeJSONFile(t, registrationPath, registration)

	prepared := writeJSONFile(t, filepath.Join(runtimeRoot, "binding.json"), map[string]any{
		"schemaVersion": 3, "kind": "CLEARDEV_LIVE_BINDING",
		"manifest": map[string]any{
			"recordClass": core.BenchmarkPurposeFormal, "batchID": FormalBatchID, "planCommit": PlanCommit,
			"candidateCommit": candidate, "group": group, "planUnitId": unitID, "scene": 1, "runRequestSHA256": requestSHA,
		},
	})
	preparedCompact := new(bytes.Buffer)
	if err := json.Compact(preparedCompact, prepared); err != nil {
		t.Fatal(err)
	}
	preparedDigest := testDigest(preparedCompact.Bytes())
	runtimeAuth := map[string]any{
		"kind": "CLEARDEV_LIVE_AUTHORIZATION", "recordClass": core.BenchmarkPurposeFormal, "batchID": FormalBatchID,
		"planCommit": PlanCommit, "candidateCommit": candidate, "bindingSHA256": preparedDigest,
		"requestSHA256": requestSHA, "registrationSha256": testDigest(registrationRaw),
		"attemptID": attemptID, "planUnitId": unitID, "group": group, "scene": 1,
	}
	authorizationPath := filepath.Join(runtimeRoot, "runtime-authorization.json")
	authorizationRaw := writeJSONFile(t, authorizationPath, runtimeAuth)
	runtimeBinding := Binding{
		SchemaVersion: 3, Kind: "CLEARDEV_ROLE_RUNTIME", RecordClass: core.BenchmarkPurposeFormal,
		BatchID: FormalBatchID, BatchManifestSHA256: batchDigest, PlanCommit: PlanCommit, CandidateCommit: candidate, Group: group,
		DataRoot: dataRoot, ProjectRoot: projectRoot, BindingPath: filepath.Join(runtimeRoot, "binding.json"), BindingSHA256: testDigest(prepared),
		AuthorizationPath: authorizationPath, AuthorizationSHA256: testDigest(authorizationRaw), RegistrationPath: registrationPath,
		RegistrationSHA256: testDigest(registrationRaw), SocketPath: filepath.Join(runtimeRoot, "runtime.sock"),
	}
	runtimePath := filepath.Join(runtimeRoot, "runtime.json")
	writeJSONFile(t, runtimePath, runtimeBinding)
	var product *core.BenchmarkBackendManifest
	if group == "G3" || group == "G4" {
		product = &core.BenchmarkBackendManifest{Purpose: core.BenchmarkPurposeFormal, Group: group, ProjectRoot: projectRoot, FacilityCommit: candidate}
	}
	return &formalV15Fixture{
		t: t, policy: policy, attemptID: attemptID, unitID: unitID, group: group, candidate: candidate, batchDigest: batchDigest,
		runtimeRoot: runtimeRoot, dataRoot: dataRoot, projectRoot: projectRoot, runtimePath: runtimePath,
		bindingPath: filepath.Join(runtimeRoot, "binding.json"), authorizationPath: authorizationPath, registrationPath: registrationPath,
		requestPath: requestPath, product: product, prepared: prepared, runtimeAuth: runtimeAuth, runtimeBinding: runtimeBinding,
	}
}

func (fixture *formalV15Fixture) rewriteRuntimeAuthorization() {
	fixture.t.Helper()
	raw := writeJSONFile(fixture.t, fixture.authorizationPath, fixture.runtimeAuth)
	fixture.runtimeBinding.AuthorizationSHA256 = testDigest(raw)
	writeJSONFile(fixture.t, fixture.runtimePath, fixture.runtimeBinding)
}

func (fixture *formalV15Fixture) load() (*Binding, error) {
	return Load(fixture.runtimePath, fixture.dataRoot, fixture.product)
}

func TestCanonicalJSONKeepsNodeStringEscaping(t *testing.T) {
	raw, err := canonicalJSON(map[string]any{"requirementVersion": "v1->v2"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"requirementVersion":"v1->v2"}`; got != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}
}

func TestLoadFormalV15DataRoot(t *testing.T) {
	for _, testCase := range []struct {
		name, group, unit string
	}{
		{"G1 no product", "G1", "R2-T1-G1"},
		{"G2 no product", "G2", "R2-T1-G2"},
		{"G3 product", "G3", "R2-T1-G3"},
		{"G4 product", "G4", "R2-T1-G4"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newFormalV15Fixture(t, testCase.group, testCase.unit)
			got, err := fixture.load()
			if err != nil {
				t.Fatalf("v15 runtime rejected: %v", err)
			}
			if got.DataRoot != fixture.dataRoot || got.ProjectRoot != fixture.projectRoot || got.CandidateCommit != fixture.candidate {
				t.Fatalf("unexpected runtime binding: %#v", got)
			}
		})
	}
}

func TestLoadFormalV15DataRootRejectsInvalidAuthorityAndPaths(t *testing.T) {
	t.Run("wrong purpose", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		fixture.runtimeBinding.RecordClass = core.BenchmarkPurposeDevLive
		writeJSONFile(t, fixture.runtimePath, fixture.runtimeBinding)
		if _, err := fixture.load(); err == nil {
			t.Fatal("DEV purpose accepted from v15 data root")
		}
	})
	t.Run("wrong candidate", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		fixture.runtimeBinding.CandidateCommit = strings.Repeat("f", 40)
		writeJSONFile(t, fixture.runtimePath, fixture.runtimeBinding)
		if _, err := fixture.load(); err == nil {
			t.Fatal("changed candidate accepted")
		}
	})
	t.Run("wrong batch manifest", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		fixture.runtimeBinding.BatchManifestSHA256 = strings.Repeat("f", 64)
		writeJSONFile(t, fixture.runtimePath, fixture.runtimeBinding)
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "batch manifest mismatch") {
			t.Fatalf("changed batch manifest accepted: %v", err)
		}
	})
	t.Run("missing approval", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		if err := os.Remove(fixture.registrationPath); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.load(); err == nil {
			t.Fatal("missing owner registration accepted")
		}
	})
	t.Run("revoked approval", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		var registration map[string]any
		raw, err := os.ReadFile(fixture.registrationPath)
		if err != nil || json.Unmarshal(raw, &registration) != nil {
			t.Fatal(err)
		}
		registration["revoked"] = true
		changed := writeJSONFile(t, fixture.registrationPath, registration)
		fixture.runtimeBinding.RegistrationSHA256 = testDigest(changed)
		fixture.runtimeAuth["registrationSha256"] = testDigest(changed)
		fixture.rewriteRuntimeAuthorization()
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "not an approval") {
			t.Fatalf("revoked owner registration accepted: %v", err)
		}
	})
	t.Run("cross scene", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		fixture.runtimeAuth["scene"] = 2
		fixture.rewriteRuntimeAuthorization()
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "data-root authorization mismatch") {
			t.Fatalf("cross-scene authorization accepted: %v", err)
		}
	})
	t.Run("cross attempt", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		fixture.runtimeAuth["attemptID"] = "aaaaaaaa-1111-4222-8333-bbbbbbbbbbbb"
		fixture.rewriteRuntimeAuthorization()
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "data-root authorization mismatch") {
			t.Fatalf("cross-attempt authorization accepted: %v", err)
		}
	})
	t.Run("indirect runtime", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		realPath := filepath.Join(fixture.runtimeRoot, "runtime-real.json")
		if err := os.Rename(fixture.runtimePath, realPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realPath, fixture.runtimePath); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "indirect") {
			t.Fatalf("runtime symlink accepted: %v", err)
		}
	})
	t.Run("public runtime file", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		if err := os.Chmod(fixture.runtimePath, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "private") {
			t.Fatalf("public runtime file accepted: %v", err)
		}
	})
	t.Run("changed storage source", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		if err := os.WriteFile(fixture.policy.storageBindingPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "storage source changed") {
			t.Fatalf("changed storage source accepted: %v", err)
		}
	})
	t.Run("G3 missing product", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G3", "R2-T1-G3")
		fixture.product = nil
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "product binding") {
			t.Fatalf("G3 without product accepted: %v", err)
		}
	})
	t.Run("G1 manufactured product", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
		fixture.product = &core.BenchmarkBackendManifest{Purpose: core.BenchmarkPurposeFormal, Group: "G1", ProjectRoot: fixture.projectRoot, FacilityCommit: fixture.candidate}
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "must not manufacture") {
			t.Fatalf("G1 product binding accepted: %v", err)
		}
	})
}

func TestLoadFormalV15RejectsPreparedRequestMismatch(t *testing.T) {
	fixture := newFormalV15Fixture(t, "G1", "R2-T1-G1")
	if _, err := fixture.load(); err != nil {
		t.Fatalf("matching v15 runtime rejected: %v", err)
	}

	var prepared map[string]any
	if err := json.Unmarshal(fixture.prepared, &prepared); err != nil {
		t.Fatal(err)
	}
	prepared["manifest"].(map[string]any)["runRequestSHA256"] = strings.Repeat("9", 64)
	fixture.prepared = writeJSONFile(t, fixture.bindingPath, prepared)
	fixture.runtimeBinding.BindingSHA256 = testDigest(fixture.prepared)
	var compact bytes.Buffer
	if err := json.Compact(&compact, fixture.prepared); err != nil {
		t.Fatal(err)
	}
	fixture.runtimeAuth["bindingSHA256"] = testDigest(compact.Bytes())
	fixture.rewriteRuntimeAuthorization()

	if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "prepared request mismatch") {
		t.Fatalf("prepared request from a different approval accepted: %v", err)
	}
}

func TestLoadFormalV15RejectsScopeThatDoesNotMatchRecovery(t *testing.T) {
	t.Run("closed sparse scope", func(t *testing.T) {
		fixture := newFormalV15Fixture(t, "G1", "R1-T1-G1")
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "execution scope mismatch") {
			t.Fatalf("closed sparse 49-labelled scope accepted: %v", err)
		}
	})

	t.Run("duplicate position", func(t *testing.T) {
		scope := formalV15TestAllowedScope()
		scope[len(scope)-1] = scope[0]
		fixture := newFormalV15FixtureWithScope(t, "G1", "R2-T1-G1", scope)
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "execution scope mismatch") {
			t.Fatalf("duplicate 49-position scope accepted: %v", err)
		}
	})

	t.Run("closed position replacement", func(t *testing.T) {
		scope := formalV15TestAllowedScope()
		scope[len(scope)-1] = "R1-T1-G1"
		fixture := newFormalV15FixtureWithScope(t, "G1", "R2-T1-G1", scope)
		if _, err := fixture.load(); err == nil || !strings.Contains(err.Error(), "execution scope mismatch") {
			t.Fatalf("closed position replacement accepted: %v", err)
		}
	})
}

func TestLoadKeepsLegacyPrivateRuntimePath(t *testing.T) {
	base := t.TempDir()
	policy := activeRuntimePathPolicy
	policy.controlRoot = filepath.Join(base, "dev-control")
	mustPrivateDir(t, policy.controlRoot)
	setRuntimePolicyForTest(t, policy)
	scene := filepath.Join(policy.controlRoot, "scene")
	data := filepath.Join(scene, "ao")
	project := filepath.Join(base, "product")
	for _, path := range []string{scene, data, project} {
		mustPrivateDir(t, path)
	}
	candidate := strings.Repeat("a", 40)
	prepared := writeJSONFile(t, filepath.Join(scene, "binding.json"), map[string]any{
		"schemaVersion": 3, "kind": "CLEARDEV_LIVE_BINDING",
		"manifest": map[string]any{"recordClass": "DEV_LIVE", "planCommit": PlanCommit, "candidateCommit": candidate, "group": "G1", "planUnitId": "DEV-SENSOR-G1", "scene": 1},
	})
	var compact bytes.Buffer
	if err := json.Compact(&compact, prepared); err != nil {
		t.Fatal(err)
	}
	auth := writeJSONFile(t, filepath.Join(scene, "authorization.json"), map[string]any{
		"kind": "CLEARDEV_LIVE_AUTHORIZATION", "recordClass": "DEV_LIVE", "planCommit": PlanCommit,
		"candidateCommit": candidate, "bindingSHA256": testDigest(compact.Bytes()),
	})
	binding := Binding{
		SchemaVersion: 3, Kind: "CLEARDEV_ROLE_RUNTIME", RecordClass: "DEV_LIVE", PlanCommit: PlanCommit, CandidateCommit: candidate, Group: "G1",
		DataRoot: data, ProjectRoot: project, BindingPath: filepath.Join(scene, "binding.json"), BindingSHA256: testDigest(prepared),
		AuthorizationPath: filepath.Join(scene, "authorization.json"), AuthorizationSHA256: testDigest(auth), SocketPath: filepath.Join(scene, "runtime.sock"),
	}
	runtimePath := filepath.Join(scene, "runtime.json")
	writeJSONFile(t, runtimePath, binding)
	if _, err := Load(runtimePath, data, nil); err != nil {
		t.Fatalf("legacy DEV runtime rejected: %v", err)
	}
}
