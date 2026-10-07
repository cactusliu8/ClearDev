package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func projectCheckReceiptFixture(t *testing.T) (ProjectExecutionContract, ProjectCheckReceipt) {
	t.Helper()
	contract := projectExecutionContractFixture(t)
	check := contract.Basis.Checks[0]
	contractDigest, err := ProjectExecutionContractDigest(contract)
	if err != nil {
		t.Fatal(err)
	}
	argv, _ := json.Marshal([][]string{check.Argv})
	receipt := ProjectCheckReceipt{
		SchemaVersion: 1, Policy: ProjectCheckPolicyV1, ExecutionRunID: contract.ExecutionRunID, ContractSHA256: contractDigest,
		CheckRunID: "check-run", CheckID: check.ID, Argv: check.Argv, TimeoutSeconds: check.TimeoutSeconds,
		CandidateSHA: strings.Repeat("e", 40), Image: StandardCandidateCheckImage, ImageID: "sha256:" + strings.Repeat("f", 64),
		SourceManifestID: strings.Repeat("1", 64), SourceRootTreeOID: strings.Repeat("2", 40), CheckEnvironmentID: strings.Repeat("3", 64),
		ApprovedArgvSHA256: sha256Hex(argv), NodeVersion: "v22.23.2", NPMVersion: "10.9.8",
		PackageJSONSHA256: strings.Repeat("4", 64), PackageLockSHA256: strings.Repeat("5", 64), DependencyCacheKey: strings.Repeat("6", 64),
		DependencyEnvironment: strings.Repeat("7", 64), DependencyTreeSHA256: strings.Repeat("8", 64),
		Outcome: "PASS", OutputSummary: "explicit unit fixture; no claim of actual execution", OutputSHA256: sha256Hex([]byte("explicit unit fixture; no claim of actual execution")),
	}
	return contract, receipt
}

func TestProjectCheckReceiptRejectsUnboundAndInconsistentEvidence(t *testing.T) {
	contract, receipt := projectCheckReceiptFixture(t)
	validate := func(r ProjectCheckReceipt) error {
		return ValidateProjectCheckReceipt(r, contract, receipt.CheckRunID, receipt.CheckID, receipt.CandidateSHA)
	}
	if err := validate(receipt); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ProjectCheckReceipt){
		"other-run":             func(r *ProjectCheckReceipt) { r.ExecutionRunID = "other-run" },
		"other-contract":        func(r *ProjectCheckReceipt) { r.ContractSHA256 = strings.Repeat("a", 64) },
		"other-check-run":       func(r *ProjectCheckReceipt) { r.CheckRunID = "another-check" },
		"other-check":           func(r *ProjectCheckReceipt) { r.CheckID = "not-approved" },
		"other-candidate":       func(r *ProjectCheckReceipt) { r.CandidateSHA = strings.Repeat("a", 40) },
		"changed-command":       func(r *ProjectCheckReceipt) { r.Argv = []string{"node", "always-green.js"} },
		"shorter-timeout":       func(r *ProjectCheckReceipt) { r.TimeoutSeconds = 1 },
		"floating-image":        func(r *ProjectCheckReceipt) { r.Image = "node:latest" },
		"missing-image":         func(r *ProjectCheckReceipt) { r.ImageID = "" },
		"missing-source":        func(r *ProjectCheckReceipt) { r.SourceManifestID = "" },
		"missing-environment":   func(r *ProjectCheckReceipt) { r.CheckEnvironmentID = "" },
		"missing-argv-proof":    func(r *ProjectCheckReceipt) { r.ApprovedArgvSHA256 = "" },
		"missing-node":          func(r *ProjectCheckReceipt) { r.NodeVersion = "" },
		"missing-lock":          func(r *ProjectCheckReceipt) { r.PackageLockSHA256 = "" },
		"changed-output":        func(r *ProjectCheckReceipt) { r.OutputSummary = "all green" },
		"missing-output-digest": func(r *ProjectCheckReceipt) { r.OutputSHA256 = "" },
		"nonzero-pass":          func(r *ProjectCheckReceipt) { r.ExitCode = 3 },
		"timed-out-pass":        func(r *ProjectCheckReceipt) { r.TimedOut = true },
		"truncated-pass":        func(r *ProjectCheckReceipt) { r.OutputTruncated = true },
		"unexecuted":            func(r *ProjectCheckReceipt) { r.Outcome = "INFRA_ERROR" },
	} {
		t.Run(name, func(t *testing.T) {
			r := receipt
			mutate(&r)
			if err := validate(r); err == nil {
				t.Fatal("invalid project evidence was accepted")
			}
		})
	}
	for _, outcome := range []string{"FAIL", "TIMED_OUT"} {
		r := receipt
		r.Outcome, r.ExitCode, r.TimedOut = outcome, 3, outcome == "TIMED_OUT"
		if err := validate(r); err != nil {
			t.Fatalf("actual failure history was rejected: %v", err)
		}
	}
}

func TestProjectCheckReceiptAcceptsBoundNoDependencyNPMEnvironment(t *testing.T) {
	contract, receipt := projectCheckReceiptFixture(t)
	contract.Basis.Checks[0].Argv = []string{"npm", "test"}
	contract.Basis.Checks[0].TimeoutSeconds = receipt.TimeoutSeconds
	digest, err := ProjectExecutionContractDigest(contract)
	if err != nil {
		t.Fatal(err)
	}
	argv, _ := json.Marshal([][]string{contract.Basis.Checks[0].Argv})
	receipt.ContractSHA256 = digest
	receipt.Argv = append([]string(nil), contract.Basis.Checks[0].Argv...)
	receipt.ApprovedArgvSHA256 = sha256Hex(argv)
	receipt.PackageLockSHA256, receipt.DependencyCacheKey, receipt.DependencyEnvironment, receipt.DependencyTreeSHA256 = "", "", "", ""
	if err := ValidateProjectCheckReceipt(receipt, contract, receipt.CheckRunID, receipt.CheckID, receipt.CandidateSHA); err != nil {
		t.Fatal(err)
	}
	receipt.PackageJSONSHA256 = ""
	if err := ValidateProjectCheckReceipt(receipt, contract, receipt.CheckRunID, receipt.CheckID, receipt.CandidateSHA); err == nil {
		t.Fatal("npm receipt without a package manifest passed")
	}
}

func TestProjectCheckReceiptRequiresExactBackendBytes(t *testing.T) {
	contract, receipt := projectCheckReceiptFixture(t)
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProjectCheckReceipt(string(raw), contract, receipt.CheckRunID, receipt.CheckID, receipt.CandidateSHA); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		string(raw) + " {}",
		string(raw[:len(raw)-1]) + `,"unknown":true}`,
		string(raw[:len(raw)-1]) + `,"outcome":"PASS"}`,
		`{"outcome":"FAIL",` + string(raw[1:]),
		"all checks passed",
	} {
		if _, err := ParseProjectCheckReceipt(changed, contract, receipt.CheckRunID, receipt.CheckID, receipt.CandidateSHA); err == nil {
			t.Fatal("ambiguous or self-described project output was accepted")
		}
	}
}

func TestProjectFreshInstallReceiptKeepsLegacySeparate(t *testing.T) {
	contract, receipt := projectCheckReceiptFixture(t)
	receipt.Policy = ProjectCheckPolicyV2
	receipt.Image = ProjectCandidateCheckImage
	receipt.DependencyCacheKey, receipt.DependencyEnvironment, receipt.DependencyTreeSHA256 = "", "", ""
	validate := func(r ProjectCheckReceipt) error {
		return ValidateProjectCheckReceipt(r, contract, r.CheckRunID, r.CheckID, r.CandidateSHA)
	}
	if err := validate(receipt); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ProjectCheckReceipt){
		func(r *ProjectCheckReceipt) { r.DependencyCacheKey = strings.Repeat("a", 64) },
		func(r *ProjectCheckReceipt) { r.Image = StandardCandidateCheckImage },
		func(r *ProjectCheckReceipt) { r.Policy = ProjectCheckPolicyV1 },
		func(r *ProjectCheckReceipt) { r.PackageLockSHA256 = "bad" },
	} {
		r := receipt
		mutate(&r)
		if validate(r) == nil {
			t.Fatal("mixed fresh/legacy evidence accepted")
		}
	}
}

func TestProjectCheckReceiptOneMiBOutput(t *testing.T) {
	contract, receipt := projectCheckReceiptFixture(t)
	for _, size := range []int{ProjectCheckOutputLimit, ProjectCheckOutputLimit + 1} {
		receipt.OutputSummary = strings.Repeat("\x00", size)
		receipt.OutputSHA256 = sha256Hex([]byte(receipt.OutputSummary))
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ParseProjectCheckReceipt(string(raw), contract, receipt.CheckRunID, receipt.CheckID, receipt.CandidateSHA)
		if (err == nil) != (size == ProjectCheckOutputLimit) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
}
