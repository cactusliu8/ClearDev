package cleardevlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var errMailScope = ports.ErrClearDevMailScopeViolation

func mailDeliveryRequestPolicy(request ports.ClearDevDeliveryRequest) (string, error) {
	policy := request.DeliveryPolicy
	if policy == "" {
		// Historical direct adapter callers predate the explicit policy field.
		policy = core.MailDeliveryPolicyV1
	}
	if policy != core.MailDeliveryPolicyV1 && policy != core.MailDeliveryPolicyV2 {
		return "", errors.New("unsupported mail delivery policy")
	}
	return policy, nil
}

// IdentifyMailProject inspects immutable Git identity without executing code.
func (r *Runner) IdentifyMailProject(ctx context.Context, workspace string) (ports.ClearDevBaselineResult, error) {
	return r.CheckDevelopmentBaseline(ctx, ports.ClearDevBaselineRequest{RunID: "mail-policy-identity", WorkspacePath: workspace, IdentifyOnly: true})
}

// CheckMailCandidateScope compares real immutable trees against the original
// starting SHA, not just the latest rework base or Agent-declared changed files.
func (r *Runner) CheckMailCandidateScope(ctx context.Context, request ports.ClearDevDeliveryRequest) (proof core.MailScopeProof, retErr error) {
	policy, err := mailDeliveryRequestPolicy(request)
	if err != nil || request.RunID == "" || !validCommit(request.BaseSHA) || !validCommit(request.CandidateSHA) {
		if err != nil {
			return proof, err
		}
		return proof, ports.ErrClearDevCandidateInvalid
	}
	inspection, err := r.InspectCandidate(ctx, request.WorkspacePath, request.BaseSHA)
	if err != nil {
		return proof, err
	}
	if inspection.CandidateSHA != request.CandidateSHA {
		return proof, ports.ErrClearDevCandidateInvalid
	}
	temporary, err := acquireCheckTemporary(ctx, request.RunID+":scope", "source-inspection", checkSourceReservationBytes)
	if err != nil {
		return proof, err
	}
	defer func() {
		if err := temporary.Release(context.WithoutCancel(ctx)); retErr == nil {
			retErr = err
		}
	}()
	base, err := inspectCandidateSource(ctx, request.WorkspacePath, request.BaseSHA, filepath.Join(temporary.Root(), "base-git"), defaultCandidateSourceLimits())
	if err != nil {
		return proof, err
	}
	candidate, err := inspectCandidateSource(ctx, request.WorkspacePath, request.CandidateSHA, filepath.Join(temporary.Root(), "candidate-git"), defaultCandidateSourceLimits())
	if err != nil {
		return proof, err
	}
	before, after := mailSourceFiles(base.Manifest), mailSourceFiles(candidate.Manifest)
	proof = core.MailScopeProof{Policy: policy, BaseSHA: request.BaseSHA, CandidateSHA: request.CandidateSHA, SourceManifestID: candidate.ManifestID, SourceTreeOID: candidate.Manifest.RootTreeOID, ExtraTestPaths: []string{}}
	for name, old := range before {
		current, exists := after[name]
		if core.MailTestPath(name) {
			proof.OldTestCount++
			if !exists || old.Mode != current.Mode {
				return proof, fmt.Errorf("%w: old test removed, renamed or mode changed: %s", errMailScope, name)
			}
			if old.ObjectOID != current.ObjectOID {
				original, err := baselineGit(ctx, request.WorkspacePath, "cat-file", "blob", old.ObjectOID)
				if err != nil {
					return proof, err
				}
				updated, err := baselineGit(ctx, request.WorkspacePath, "cat-file", "blob", current.ObjectOID)
				if err != nil {
					return proof, err
				}
				if !bytes.HasPrefix([]byte(updated), []byte(original)) {
					return proof, fmt.Errorf("%w: old test bytes changed: %s", errMailScope, name)
				}
				// Appended tests need execution too: npm test's frozen list
				// does not include every existing test or helper file.
				if core.MailExtraTestPath(name) {
					proof.ExtraTestPaths = append(proof.ExtraTestPaths, name)
				} else if !strings.HasPrefix(name, "test/") || !slices.Contains([]string{".json", ".txt", ".csv", ".md"}, filepath.Ext(name)) {
					return proof, fmt.Errorf("%w: appended test has no approved executable entry: %s", errMailScope, name)
				}
			}
		}
		if (!exists || old.ObjectOID != current.ObjectOID || old.Mode != current.Mode) && !core.MailWritePathAllowed(name) {
			return proof, fmt.Errorf("%w: forbidden modification: %s", errMailScope, name)
		}
	}
	for name := range after {
		if _, exists := before[name]; exists {
			continue
		}
		if !core.MailWritePathAllowed(name) {
			return proof, fmt.Errorf("%w: forbidden new file: %s", errMailScope, name)
		}
		if core.MailTestPath(name) {
			if core.MailExtraTestPath(name) {
				proof.ExtraTestPaths = append(proof.ExtraTestPaths, name)
			} else if !strings.HasPrefix(name, "test/") || !slices.Contains([]string{".json", ".txt", ".csv", ".md"}, filepath.Ext(name)) {
				return proof, fmt.Errorf("%w: new test has no approved executable entry: %s", errMailScope, name)
			}
		}
	}
	sort.Strings(proof.ExtraTestPaths)
	if err := core.ValidateMailScopeProofForPolicy(proof, policy, request.BaseSHA, request.CandidateSHA); err != nil {
		return proof, err
	}
	if err := r.inspectUnchangedBaseline(ctx, request.WorkspacePath, request.CandidateSHA); err != nil {
		return proof, err
	}
	return proof, nil
}

func mailSourceFiles(manifest candidateSourceManifest) map[string]candidateSourceManifestEntry {
	files := make(map[string]candidateSourceManifestEntry)
	for _, entry := range manifest.Entries {
		if entry.Mode != "040000" {
			files[entry.Path] = entry
		}
	}
	return files
}

func mailCheckProof(id string, argv []string, result ports.ClearDevCheckResult) core.MailCheckProof {
	return core.MailCheckProof{RunID: id, Argv: append([]string(nil), argv...), CandidateSHA: result.CandidateSHA, SourceManifestID: result.SourceManifestID, SourceTreeOID: result.SourceRootTreeOID, ImageID: result.ImageID, EnvironmentID: result.CheckEnvironmentID, OutputSHA256: result.OutputSHA256, Passed: result.Outcome == ports.ClearDevCheckPass, ExitCode: result.ExitCode, TimedOut: result.TimedOut, Truncated: result.OutputTruncated}
}

// RunMailDeliveryCheck extends the existing integration check. Durable runner
// IDs bind every actual external action to this immutable candidate. The JSON
// receipt is assembled by Go; a project's printed PASS cannot create it.
func (r *Runner) RunMailDeliveryCheck(ctx context.Context, request ports.ClearDevDeliveryRequest) (final ports.ClearDevCheckResult, retErr error) {
	policy, err := mailDeliveryRequestPolicy(request)
	if err != nil {
		return ports.ClearDevCheckResult{}, err
	}
	scope, err := r.CheckMailCandidateScope(ctx, request)
	if err != nil {
		if errors.Is(err, errMailScope) {
			return mailDeliveryFailure(request.CandidateSHA, err.Error()), nil
		}
		return ports.ClearDevCheckResult{}, err
	}
	if sha256Hex([]byte(baselineHealthProbe)) != core.MailHealthProbeSHA256 {
		return ports.ClearDevCheckResult{}, errors.New("trusted health probe changed without a policy version")
	}
	proof := core.MailDeliveryProof{Scope: scope, HealthProbeSHA256: core.MailHealthProbeSHA256}
	commands := [][]string{{"npm", "test"}}
	if len(scope.ExtraTestPaths) != 0 {
		commands = append(commands, core.MailExtraTestArgv(scope.ExtraTestPaths))
	}
	commands = append(commands, []string{"node", "--input-type=module", "-e", baselineHealthProbe})
	preflightID := request.RunID + ":delivery-environment"
	environment, err := r.PrepareCandidateChecks(ctx, ports.ClearDevCheckPreflightRequest{RunID: preflightID, WorkspacePath: request.WorkspacePath, CandidateSHA: request.CandidateSHA, Image: core.StandardCandidateCheckImage, Argv: commands, Timeout: 2 * time.Minute, MemoryBytes: baselineMemoryBytes, PidsLimit: 64})
	if err != nil {
		return ports.ClearDevCheckResult{}, err
	}
	defer func() {
		if err := r.ReleaseCandidateChecks(context.WithoutCancel(ctx), preflightID); retErr == nil {
			retErr = err
		}
	}()
	var last ports.ClearDevCheckResult
	for i, argv := range commands {
		suffix, timeout := ":npm-test", time.Minute
		if i == len(commands)-1 {
			suffix, timeout = ":health", 20*time.Second
		} else if i != 0 {
			suffix = ":extra-tests"
		}
		id := request.RunID + suffix
		result, err := r.RunCandidateCheck(ctx, ports.ClearDevCheckRequest{RunID: id, WorkspacePath: request.WorkspacePath, CandidateSHA: request.CandidateSHA, Image: core.StandardCandidateCheckImage, Argv: argv, Timeout: timeout, MemoryBytes: baselineMemoryBytes, PidsLimit: 64, OutputLimit: 64 * 1024})
		if err != nil {
			return result, err
		}
		last = result
		if result.CandidateSHA != request.CandidateSHA || result.SourceManifestID != scope.SourceManifestID || result.SourceRootTreeOID != scope.SourceTreeOID || result.ImageID != environment.ImageID {
			return result, errors.New("mail delivery checker returned stale source evidence")
		}
		if result.Outcome != ports.ClearDevCheckPass || result.ExitCode != 0 || result.TimedOut || result.OutputTruncated {
			result.Outcome = ports.ClearDevCheckFail
			if last.Outcome == ports.ClearDevCheckInfraError {
				result.Outcome = last.Outcome
			}
			return result, nil
		}
		switch suffix {
		case ":npm-test":
			proof.Tests = mailCheckProof(id, argv, result)
		case ":extra-tests":
			extra := mailCheckProof(id, argv, result)
			proof.ExtraTests = &extra
		case ":health":
			proof.Health = mailCheckProof(id, []string{"trusted-mail-health-v1"}, result)
		}
	}
	if err := r.inspectUnchangedBaseline(ctx, request.WorkspacePath, request.CandidateSHA); err != nil {
		return last, err
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return last, err
	}
	if err := core.ValidateMailDeliveryProofForPolicy(string(raw), policy, request.BaseSHA, request.CandidateSHA, request.RunID, last.ImageID); err != nil {
		return last, err
	}
	last.OutputSummary, last.OutputSHA256 = string(raw), sha256Hex(raw)
	return last, nil
}

func mailDeliveryFailure(sha, message string) ports.ClearDevCheckResult {
	message = strings.TrimSpace(message)
	return ports.ClearDevCheckResult{CandidateSHA: sha, Outcome: ports.ClearDevCheckFail, ExitCode: 1, OutputSummary: message, OutputSHA256: sha256Hex([]byte(message))}
}
