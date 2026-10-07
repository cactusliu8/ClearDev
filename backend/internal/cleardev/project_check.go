package cleardev

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
)

// ProjectCheckPolicyV1 identifies candidate-bound backend execution receipts.
const ProjectCheckPolicyV1 = "PROJECT_CHECK_V1"

// ProjectCheckPolicyV2 installs dependencies fresh inside the actual check container.
const ProjectCheckPolicyV2 = "PROJECT_CHECK_FRESH_INSTALL_V2"

// ProjectCandidateCheckImage includes the compiler used for fresh dependency installation.
const ProjectCandidateCheckImage = "node@sha256:fbe64f0a038c6117d58d6a160c52994ee586cab9e36812802b564ac2d464bb24"

// ProjectCheckMemoryBytes is the current project container memory and swap-total limit.
const ProjectCheckMemoryBytes int64 = 2 * 1024 * 1024 * 1024

// ProjectCheckPidsLimit includes both processes and threads.
const ProjectCheckPidsLimit int64 = 128

// ProjectCheckOutputLimit bounds captured project check output.
const ProjectCheckOutputLimit = 1024 * 1024

// ProjectCheckReceipt is stored in the existing append-only check evidence.
// It records what the backend actually ran; it is not a claim that the product
// works, an approval, or a substitute for independent Task and Final Review.
// Historical check output remains interpreted by its historical policy.
type ProjectCheckReceipt struct {
	SchemaVersion         int      `json:"schemaVersion"`
	Policy                string   `json:"policy"`
	ExecutionRunID        string   `json:"executionRunId"`
	ContractSHA256        string   `json:"contractSha256"`
	CheckRunID            string   `json:"checkRunId"`
	CheckID               string   `json:"checkId"`
	Argv                  []string `json:"argv"`
	TimeoutSeconds        int      `json:"timeoutSeconds"`
	CandidateSHA          string   `json:"candidateSha"`
	Image                 string   `json:"image"`
	ImageID               string   `json:"imageId"`
	SourceManifestID      string   `json:"sourceManifestId"`
	SourceRootTreeOID     string   `json:"sourceRootTreeOid"`
	CheckEnvironmentID    string   `json:"checkEnvironmentId"`
	ApprovedArgvSHA256    string   `json:"approvedArgvSha256"`
	NodeVersion           string   `json:"nodeVersion"`
	NPMVersion            string   `json:"npmVersion,omitempty"`
	PackageJSONSHA256     string   `json:"packageJsonSha256,omitempty"`
	PackageLockSHA256     string   `json:"packageLockSha256,omitempty"`
	DependencyCacheKey    string   `json:"dependencyCacheKey,omitempty"`
	DependencyEnvironment string   `json:"dependencyEnvironment,omitempty"`
	DependencyTreeSHA256  string   `json:"dependencyTreeSha256,omitempty"`
	Outcome               string   `json:"outcome"`
	ExitCode              int      `json:"exitCode"`
	TimedOut              bool     `json:"timedOut"`
	OutputTruncated       bool     `json:"outputTruncated"`
	OutputSummary         string   `json:"outputSummary"`
	OutputSHA256          string   `json:"outputSha256"`
}

// ProjectExecutionContractDigest is the contract's identity digest: the same
// canonical encoding used for durable contract text, so one contract has one
// digest however it is spelled in a stored JSON text.
// rather than changing the frozen V3 source's canonical serialization.
func ProjectExecutionContractDigest(contract ProjectExecutionContract) (string, error) {
	if err := ValidateProjectExecutionContract(contract); err != nil {
		return "", err
	}
	raw, err := CanonicalJSONBytes(contract)
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}

// ProjectExecutionDigestMatches accepts a receipt digest of either spelling of
// the same contract: canonical or the historical default-encoder digest.
func ProjectExecutionDigestMatches(stored string, contract ProjectExecutionContract) bool {
	current, legacy, err := projectExecutionDigestForms(contract)
	return err == nil && (stored == current || stored == legacy)
}

// LegacyProjectExecutionContractDigest is the pre-canonical digest of the same
// contract, used to verify receipts that were stored before the canonical
// encoder became the single spelling.
func LegacyProjectExecutionContractDigest(contract ProjectExecutionContract) string {
	raw, _ := json.Marshal(contract)
	return sha256Hex(raw)
}

// projectExecutionDigestForms accepts a digest made before the canonical
// encoder: receipts already stored carry the default-encoder digest of the same
// contract, and that historical spelling must keep verifying.
func projectExecutionDigestForms(contract ProjectExecutionContract) (string, string, error) {
	canonical, err := ProjectExecutionContractDigest(contract)
	if err != nil {
		return "", "", err
	}
	legacy, err := json.Marshal(contract)
	if err != nil {
		return "", "", err
	}
	return canonical, sha256Hex(legacy), nil
}

// ValidateProjectCheckReceipt verifies the exact command, candidate, environment and outcome bindings.
func ValidateProjectCheckReceipt(receipt ProjectCheckReceipt, contract ProjectExecutionContract, checkRunID, checkID, candidateSHA string) error {
	digest, legacyDigest, err := projectExecutionDigestForms(contract)
	if err != nil {
		return err
	}
	check, found := ComplexCheckByID(contract.Basis.CheckCatalog(), checkID)
	if !found || !validExecutionIdentifier(checkRunID) || !mailSHA1(candidateSHA) ||
		receipt.SchemaVersion != 1 || (receipt.Policy != ProjectCheckPolicyV1 && receipt.Policy != ProjectCheckPolicyV2) || receipt.ExecutionRunID != contract.ExecutionRunID || (receipt.ContractSHA256 != digest && receipt.ContractSHA256 != legacyDigest) ||
		receipt.CheckRunID != checkRunID || receipt.CheckID != checkID || receipt.CandidateSHA != candidateSHA ||
		!slices.Equal(receipt.Argv, check.Argv) || receipt.TimeoutSeconds != check.TimeoutSeconds ||
		!validProjectReceiptImage(receipt) || !strings.HasPrefix(receipt.ImageID, "sha256:") || !validProtocolSHA256(strings.TrimPrefix(receipt.ImageID, "sha256:")) ||
		!validProtocolSHA256(receipt.SourceManifestID) || !mailSHA1(receipt.SourceRootTreeOID) || !validProtocolSHA256(receipt.CheckEnvironmentID) ||
		receipt.NodeVersion == "" || len(receipt.NodeVersion) > 256 || len(receipt.NPMVersion) > 256 || len(receipt.OutputSummary) > ProjectCheckOutputLimit ||
		!validProtocolSHA256(receipt.OutputSHA256) || sha256Hex([]byte(receipt.OutputSummary)) != receipt.OutputSHA256 {
		return errors.New("project check receipt lost its exact candidate, command or environment binding")
	}
	argv, err := json.Marshal([][]string{check.Argv})
	if err != nil || receipt.ApprovedArgvSHA256 != sha256Hex(argv) {
		return errors.New("project check receipt ran a different command")
	}
	dependencies := []string{receipt.PackageJSONSHA256, receipt.PackageLockSHA256, receipt.DependencyCacheKey, receipt.DependencyEnvironment, receipt.DependencyTreeSHA256}
	anyDependency := false
	for _, value := range dependencies {
		anyDependency = anyDependency || value != ""
	}
	if anyDependency {
		freshInstall := receipt.Policy == ProjectCheckPolicyV2 && validProtocolSHA256(receipt.PackageJSONSHA256) && (receipt.PackageLockSHA256 == "" || validProtocolSHA256(receipt.PackageLockSHA256)) && receipt.DependencyCacheKey == "" && receipt.DependencyEnvironment == "" && receipt.DependencyTreeSHA256 == ""
		noDependencyProject := validProtocolSHA256(receipt.PackageJSONSHA256) && receipt.PackageLockSHA256 == "" && receipt.DependencyCacheKey == "" && receipt.DependencyEnvironment == "" && receipt.DependencyTreeSHA256 == ""
		if !noDependencyProject && !freshInstall {
			for _, value := range dependencies {
				if !validProtocolSHA256(value) {
					return errors.New("project check receipt lost its exact manifest, lockfile or dependency environment")
				}
			}
		}
		if receipt.NPMVersion == "" {
			return errors.New("project dependencies have no observed npm version")
		}
	} else if check.Argv[0] == "npm" {
		return errors.New("an npm check has no candidate package manifest")
	}
	switch receipt.Outcome {
	case "PASS":
		if receipt.ExitCode != 0 || receipt.TimedOut || receipt.OutputTruncated {
			return errors.New("a failed, timed-out or truncated project check cannot pass")
		}
	case "TIMED_OUT":
		if !receipt.TimedOut {
			return errors.New("project timeout receipt has no timeout")
		}
	case "FAIL":
		if receipt.TimedOut || receipt.ExitCode == 0 && !receipt.OutputTruncated {
			return errors.New("project failure receipt has inconsistent process evidence")
		}
	default:
		return errors.New("an unknown or unexecuted check cannot acquire an execution receipt")
	}
	return nil
}

// ParseProjectCheckReceipt is used again at settlement, candidate verification
// and final completion. Successful task status alone cannot stand in for it.
func ParseProjectCheckReceipt(raw string, contract ProjectExecutionContract, checkRunID, checkID, candidateSHA string) (ProjectCheckReceipt, error) {
	var receipt ProjectCheckReceipt
	if raw == "" || len(raw) > 8*ProjectCheckOutputLimit {
		return receipt, errors.New("project check receipt is missing or too large")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return receipt, errors.New("project check receipt contains trailing data")
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || string(canonical) != raw {
		return receipt, errors.New("project check receipt is not the exact backend serialization")
	}
	return receipt, ValidateProjectCheckReceipt(receipt, contract, checkRunID, checkID, candidateSHA)
}

func validProjectReceiptImage(receipt ProjectCheckReceipt) bool {
	if receipt.Policy == ProjectCheckPolicyV2 {
		return receipt.Image == ProjectCandidateCheckImage && receipt.DependencyCacheKey == "" && receipt.DependencyEnvironment == "" && receipt.DependencyTreeSHA256 == ""
	}
	return receipt.Image == StandardCandidateCheckImage
}
