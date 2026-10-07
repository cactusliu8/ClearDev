package cleardevlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func stageProjectNPMArchives(ctx context.Context, source, root string) (string, time.Time, error) {
	timeout, err := dependencyPreparationTimeout()
	if err != nil {
		return "", time.Time{}, err
	}
	deadline := time.Now().Add(timeout)
	downloadCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	raw, err := os.ReadFile(filepath.Join(source, "package-lock.json"))
	if os.IsNotExist(err) {
		return "", deadline, nil
	}
	if err != nil {
		return "", deadline, err
	}
	artifacts, err := lockedNPMArtifacts(raw)
	if err != nil {
		return "", deadline, err
	}
	cache, err := npmArchiveCacheRoot()
	if err != nil {
		return "", deadline, err
	}
	directory := filepath.Join(root, "npm-archives")
	err = downloadCachedNPMArtifacts(downloadCtx, projectNPMDownloadClient(), artifacts, directory, cache, checkDependencyBytesLimit, npmArchiveCacheBytes, dependencyDownloadIdle, time.Second)
	return directory, deadline, err
}

func installProjectInCheckContainer(ctx context.Context, cid string, request CheckContainerRequest) (string, error) {
	deadline := request.PreparationDeadline
	if deadline.IsZero() {
		d, err := dependencyPreparationTimeout()
		if err != nil {
			return "", err
		}
		deadline = time.Now().Add(d)
	}
	installCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	collector := newOutputCollector(checkPreparationOutputLimit, cancel)
	events := make(chan checkContainerCapacityEvent, 1)
	monitorCtx, stop := context.WithCancel(installCtx)
	var wait sync.WaitGroup
	wait.Add(1)
	go monitorCheckContainerCapacity(monitorCtx, cid, cancel, events, &wait, request)
	defer func() { stop(); wait.Wait() }()
	run := func(argv ...string) error {
		args, err := CheckContainerExecArgs(cid, argv)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(installCtx, "docker", args...) //nolint:gosec // fixed installer argv, verified archives and admitted preparation command.
		cmd.Stdout, cmd.Stderr = collector, collector
		err = cmd.Run()
		if err != nil {
			if cause := installCtx.Err(); cause != nil {
				if collector.LimitExceeded() {
					return errors.New("dependency installation output limit exceeded")
				}
				select {
				case event := <-events:
					if event.Full {
						return errCheckCapacityExceeded
					}
					if event.Err != nil {
						return event.Err
					}
				default:
				}
				return fmt.Errorf("dependency installation interrupted: %w", cause)
			}
			return fmt.Errorf("dependency installation command failed: %w", err)
		}
		return nil
	}
	if err := run("mkdir", "-p", "/tmp/cleardev-project-data", "/workspace/node_modules"); err != nil {
		return collector.String(), err
	}
	if request.NPMArchives != "" {
		entries, err := os.ReadDir(request.NPMArchives)
		if err != nil {
			return collector.String(), err
		}
		script := strings.ReplaceAll(projectNPMCacheImportScript, "/prepare/.download-cache/_cacache", "/install-cache/_cacache")
		script = strings.Replace(script, "process.argv.slice(1)", "require('node:fs').readdirSync('/npm-archives').sort().map(name => '/npm-archives/' + name)", 1)
		argv := []string{"node", "-e", script}
		for _, e := range entries {
			if !e.Type().IsRegular() || filepath.Ext(e.Name()) != ".tgz" {
				return collector.String(), errors.New("invalid staged npm archive")
			}
		}
		if err := run(argv...); err != nil {
			return collector.String(), err
		}
		if err := run("npm", "ci", "--offline", "--ignore-scripts", "--no-audit", "--no-fund", "--cache=/install-cache"); err != nil {
			return collector.String(), err
		}
	} else if err := run("mkdir", "-p", "/workspace/node_modules"); err != nil {
		return collector.String(), err
	}
	// Validate declared native archives against the packages just installed in this container.
	items, err := readProjectDownloads(request.SourceDir, "")
	if err != nil {
		return collector.String(), err
	}
	metadata := filepath.Join(filepath.Dir(request.CIDFile), "download-metadata")
	for _, item := range items {
		args, _ := CheckContainerExecArgs(cid, []string{"node", "-e", "const p=require(process.argv[1]);process.stdout.write(JSON.stringify({name:p.name,version:p.version}))", "/workspace/node_modules/" + item.Package + "/package.json"})
		data, err := exec.CommandContext(installCtx, "docker", args...).Output()
		if err != nil {
			return collector.String(), fmt.Errorf("read installed dependency identity: %w", err)
		} //nolint:gosec // validated package path and fixed identity reader.
		var pkg struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &pkg) != nil || pkg.Name != item.Package || pkg.Version != item.Version {
			return collector.String(), errors.New("installed dependency differs from pinned archive")
		}
		path := filepath.Join(metadata, filepath.FromSlash(item.Package))
		if err := os.MkdirAll(path, 0o700); err != nil {
			return collector.String(), err
		}
		if err := os.WriteFile(filepath.Join(path, "package.json"), data, 0o600); err != nil {
			return collector.String(), err
		}
	}
	buildFromSource := false
	if len(items) > 0 {
		seed, err := seedProjectDownloads(installCtx, request.SourceDir, metadata, filepath.Dir(request.CIDFile), collector)
		if err != nil {
			if installCtx.Err() == nil && (strings.Contains(err.Error(), "sources exhausted") || errors.Is(err, context.DeadlineExceeded)) {
				buildFromSource = true
				_, _ = fmt.Fprintln(collector, "verified download unavailable; compiling from dependency sources")
			} else {
				return collector.String(), err
			}
		}
		if seed != "" {
			if err := run("mkdir", "-p", "/workspace/node_modules/.cleardev-npm-cache"); err != nil {
				return collector.String(), err
			}
			entries, err := os.ReadDir(filepath.Join(seed, "_prebuilds"))
			if err != nil {
				return collector.String(), err
			}
			if err := run("mkdir", "-p", "/workspace/node_modules/.cleardev-npm-cache/_prebuilds"); err != nil {
				return collector.String(), err
			}
			for _, entry := range entries {
				if !entry.Type().IsRegular() {
					return collector.String(), errors.New("invalid native download seed")
				}
				f, err := os.Open(filepath.Join(seed, "_prebuilds", entry.Name()))
				if err != nil {
					return collector.String(), err
				}
				args := []string{"exec", "-i", "--user=65532:65532", cid, "node", "-e", "process.stdin.pipe(require('fs').createWriteStream(process.argv[1],{flags:'wx'})).on('error',e=>{console.error(e);process.exitCode=1})", "/workspace/node_modules/.cleardev-npm-cache/_prebuilds/" + entry.Name()}
				cmd := exec.CommandContext(installCtx, "docker", args...) //nolint:gosec // validated local seed basename, fixed pipe writer and container identity.
				cmd.Stdin = f
				cmd.Stdout, cmd.Stderr = collector, collector
				err = cmd.Run()
				_ = f.Close()
				if err != nil {
					return collector.String(), err
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(request.SourceDir, "package.json")); err == nil {
		argv := []string{"npm", "rebuild", "--foreground-scripts", "--ignore-scripts=false", "--offline=false", "--no-audit", "--no-fund"}
		if buildFromSource {
			argv = append(argv, "--build-from-source=true")
		}
		if err := run(argv...); err != nil {
			return collector.String(), err
		}
	}
	if len(request.ProjectRuntime.PrepareArgv) > 0 && request.DependencyExport == "" {
		if err := run(request.ProjectRuntime.PrepareArgv...); err != nil {
			return collector.String(), err
		}
	}
	if collector.LimitExceeded() {
		return collector.String(), errors.New("dependency installation output limit exceeded")
	}
	stop()
	wait.Wait()
	select {
	case event := <-events:
		if event.Full {
			return collector.String(), errCheckCapacityExceeded
		}
		if event.Err != nil {
			return collector.String(), event.Err
		}
	default:
	}
	full, err := checkContainerCapacityForRequest(installCtx, cid, request)
	if err != nil {
		return collector.String(), err
	}
	if full {
		return collector.String(), errCheckCapacityExceeded
	}
	return collector.String(), nil
}

// Anonymous Docker volumes use disk and are removed with their container. This
// observes logical usage, not a hard filesystem quota; global leases reserve
// the budget and unknown cleanup retains the original container identity.
func checkContainerCapacityForRequest(ctx context.Context, cid string, request CheckContainerRequest) (bool, error) {
	if !request.FreshInstall {
		return checkContainerCapacity(ctx, cid)
	}
	if !validDockerID(cid) {
		return false, errors.New("invalid disk capacity container")
	}
	probeCtx, cancel := context.WithTimeout(ctx, checkContainerProbeTimeout)
	defer cancel()
	script := `const fs=require('fs');let bytes=0,items=0;const maxBytes=Number(process.argv[1]),maxItems=Number(process.argv[2]);const stack=['/workspace','/install-cache'];while(stack.length){const p=stack.pop();let s;try{s=fs.lstatSync(p)}catch(e){if(e.code==='ENOENT')continue;throw e}items++;if(s.isDirectory()){let names;try{names=fs.readdirSync(p)}catch(e){if(e.code==='ENOENT'||e.code==='ENOTDIR')continue;throw e}if(names.length+stack.length+items>maxItems){process.stdout.write('FULL');process.exit(0)}for(const n of names)stack.push(p+'/'+n)}else bytes+=s.size;if(bytes>maxBytes||items>maxItems){process.stdout.write('FULL');process.exit(0)}}process.stdout.write('OK');`
	out, err := exec.CommandContext(probeCtx, "docker", "exec", cid, "node", "-e", script, strconv.FormatInt(request.SourceBytes+2*checkDependencyBytesLimit+checkOutputBytesLimit, 10), strconv.FormatInt(request.SourceItems+2*checkDependencyItemsLimit+checkOutputItemsLimit, 10)).CombinedOutput() //nolint:gosec // fixed probe and bounded trusted numeric limits.
	if err != nil {
		return false, fmt.Errorf("disk usage probe: %w: %s", err, out)
	}
	switch string(out) {
	case "FULL":
		return true, nil
	case "OK":
		return false, nil
	default:
		return false, errors.New("invalid disk usage probe result")
	}
}
