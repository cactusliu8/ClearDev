package cleardevlocal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// PrepareProjectResult reuses the candidate materializer, pinned environment,
// dependency template and temporary-capacity ledger. Installation lifecycles
// run in an isolated container, never on the host, and grant no completion.
// The preview manager calls it only after
// stopping its previous process, and owns Release until the new process exits.
func (r *Runner) PrepareProjectResult(ctx context.Context, workspace, candidate string, contract core.ProjectExecutionContract) (out ports.ClearDevProjectResultSource, retErr error) {
	if err := core.ValidateProjectExecutionContract(contract); err != nil {
		return out, err
	}
	if !validCommit(candidate) || !filepath.IsAbs(workspace) {
		return out, errors.New("project result requires the exact delivered source")
	}
	commands := make([][]string, 0, len(contract.Basis.Checks))
	for _, check := range contract.Basis.Checks {
		commands = append(commands, check.Argv)
	}
	request, err := normalizeCheckPreflightRequest(ports.ClearDevCheckPreflightRequest{
		WorkspacePath: workspace, CandidateSHA: candidate, Image: core.StandardCandidateCheckImage,
		Argv: commands, ProjectExecution: &contract, Timeout: checkPreparationTimeout,
		MemoryBytes: core.ProjectCheckMemoryBytes, PidsLimit: core.ProjectCheckPidsLimit,
	})
	if err != nil {
		return out, err
	}
	prepared, err := r.prepareCheckEnvironment(ctx, request)
	if prepared.dependencyReference != "" {
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), CheckCleanupTimeout)
			defer cancel()
			manager, releaseErr := openDependencyCacheManager(cleanupCtx)
			if releaseErr == nil {
				releaseErr = manager.releaseReference(cleanupCtx, prepared.dependencyReference)
			}
			if retErr == nil && releaseErr != nil {
				retErr = releaseErr
				if out.Release != nil {
					_ = out.Release(cleanupCtx)
				}
			}
		}()
	}
	if err != nil {
		return out, err
	}
	// Reserve actual immutable inputs plus a bounded build-output allowance.
	// This reservation shares the existing global cap with checks; a caller
	// receives an explicit capacity error instead of exhausting the host.
	reservation := prepared.public.SourceBytes + checkDependencyBytesLimit + checkOutputBytesLimit + 128*1024*1024
	temporary, err := acquireCheckTemporary(ctx, "project-preview:"+contract.ExecutionRunID+":"+newOpaqueCheckID(), "project-preview", reservation)
	if err != nil {
		return out, err
	}
	owned := false
	defer func() {
		if !owned {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), CheckCleanupTimeout)
			defer cancel()
			_ = temporary.Release(cleanupCtx)
		}
	}()
	source := filepath.Join(temporary.Root(), "candidate")
	facts, err := materializeCandidateSource(ctx, workspace, candidate, filepath.Join(temporary.Root(), "git"), source,
		filepath.Join(temporary.Root(), "candidate-manifest.json"), defaultCandidateSourceLimits())
	if err != nil {
		return out, err
	}
	if facts.ManifestID != prepared.public.SourceManifestID || facts.Manifest.CandidateSHA != candidate || facts.Manifest.RootTreeOID != prepared.public.SourceRootTreeOID {
		return out, errors.New("project result source changed after exact candidate preparation")
	}
	dependencySource := ""
	if prepared.public.PackageJSONSHA256 != "" {
		dependencySource, err = prepareProjectResultDependencies(ctx, prepared, source, temporary.Root(), contract.Runtime)
		if err != nil {
			return out, err
		}
	}
	// Only the disposable copy is writable for the project's own build and
	// migration preparation. The original delivered Git worktree stays clean.
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return out, err
	}
	defer func() { _ = sourceRoot.Close() }()
	if err := fs.WalkDir(sourceRoot.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("project result source may not contain redirected paths")
		}
		mode := fs.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		} else if info, err := entry.Info(); err != nil {
			return err
		} else if info.Mode()&0o111 != 0 {
			mode = 0o700
		}
		// Root-scoped chmod cannot follow a concurrently substituted link
		// outside the disposable source tree.
		return sourceRoot.Chmod(name, mode)
	}); err != nil {
		return out, err
	}
	if dependencySource != "" {
		// Move this preparation's output, not the shared template, into the
		// private delivered workspace. The extraction validated links/quotas.
		if err := os.Rename(dependencySource, filepath.Join(source, "node_modules")); err != nil {
			return out, err
		}
	}
	if prepared.dependencyDirectory != "" {
		if _, err := verifyDependencyEnvironment(prepared.dependencyDirectory, prepared.dependencyCacheKey); err != nil {
			return out, err
		}
	}
	if err := os.Mkdir(filepath.Join(temporary.Root(), "home"), 0o700); err != nil {
		return out, err
	}
	if err := os.Mkdir(filepath.Join(temporary.Root(), "tmp"), 0o700); err != nil {
		return out, err
	}
	digest, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		return out, err
	}
	out = ports.ClearDevProjectResultSource{
		WorkspacePath: source, CandidateSHA: candidate, ContractSHA256: digest,
		Environment: prepared.public, Release: temporary.Release,
	}
	owned = true
	return out, nil
}

func prepareProjectResultDependencies(ctx context.Context, prepared preparedCheckEnvironment, source, root string, runtime core.ProjectRuntime) (destination string, retErr error) {
	temporary, err := acquireCheckTemporary(ctx, "project-dependencies:"+newOpaqueCheckID(), "candidate-check", checkRunReservationBytes+checkDependencyBytesLimit)
	if err != nil {
		return "", err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CheckCleanupTimeout)
		defer cancel()
		retErr = errors.Join(retErr, temporary.Release(cleanupCtx))
	}()
	destination = filepath.Join(root, "installed-dependencies")
	if err := os.Mkdir(destination, 0o700); err != nil {
		return "", err
	}
	runtime.PrepareArgv = nil
	archives, deadline, err := stageProjectNPMArchives(ctx, source, temporary.Root())
	if err != nil {
		return "", err
	}
	result, err := RunCheckContainer(ctx, CheckContainerRequest{
		ExecutionProfile: core.NodeCheckSmallThreadsV1, Image: projectInstallImage, FreshInstall: true, NPMArchives: archives, PreparationDeadline: deadline,
		SourceDir: source, SourceBytes: prepared.public.SourceBytes, SourceItems: prepared.public.SourceItems, ProjectRuntime: &runtime, DependencyExport: destination, CIDFile: temporary.CIDFile(), Timeout: 30 * time.Minute, MemoryBytes: core.ProjectCheckMemoryBytes, PidsLimit: core.ProjectCheckPidsLimit, OutputLimit: checkPreparationOutputLimit,
	})
	if writeErr := os.WriteFile(filepath.Join(root, "dependency-download.log"), []byte(result.Output), 0o600); writeErr != nil {
		return "", errors.Join(err, writeErr)
	}
	if err != nil {
		return "", err
	}
	if result.Outcome != CheckContainerPass {
		return "", fmt.Errorf("project dependency installation failed (%s): %s", result.Outcome, result.Output)
	}
	return destination, nil
}

// npm's .bin links are preserved only when their relative targets stay inside
// the verified dependency tree. No source or data outside the cache is followed.
func copyProjectDependencyTree(ctx context.Context, source, destination string) error {
	input, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	output, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	var bytes, items int64
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
		if items > checkDependencyItemsLimit {
			return errors.New("project runtime dependency copy exceeds its item limit")
		}
		if entry.IsDir() {
			return output.Mkdir(name, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := input.Readlink(name)
			if err != nil {
				return err
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(name), target))
			if filepath.IsAbs(target) || strings.ContainsAny(target, "\\\x00\r\n") || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
				return errors.New("project dependency link escapes its exact environment")
			}
			if _, err := input.Stat(name); err != nil {
				return fmt.Errorf("invalid project dependency link: %w", err)
			}
			return output.Symlink(target, name)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("project dependencies must be regular files or contained links")
		}
		if info.Size() < 0 || info.Size() > checkDependencyBytesLimit-bytes {
			return errors.New("project runtime dependency copy exceeds its byte limit")
		}
		bytes += info.Size()
		reader, err := input.Open(name)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o600)
		if info.Mode()&0o111 != 0 {
			mode = 0o700
		}
		writer, err := output.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			_ = reader.Close()
			return err
		}
		count, copyErr := io.Copy(writer, io.LimitReader(reader, info.Size()+1))
		closeErr := errors.Join(reader.Close(), writer.Close())
		if copyErr != nil || closeErr != nil || count != info.Size() {
			return errors.Join(errors.New("project dependency changed while copying"), copyErr, closeErr)
		}
		return nil
	})
}
