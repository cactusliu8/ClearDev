package cleardevlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Persist anonymous volume identities before removing a container. If the app
// exits between container deletion and volume deletion, its original lease
// retains this sidecar and cleanup resumes against exactly these volumes.
func retainCheckVolumes(ctx context.Context, cidFile, id string) ([]string, error) {
	path := cidFile + ".volumes.json"
	if raw, err := os.ReadFile(path); err == nil {
		var names []string
		if json.Unmarshal(raw, &names) != nil {
			return nil, errors.New("invalid check volume evidence")
		}
		for _, name := range names {
			if !validDockerID(name) {
				return nil, errors.New("invalid check volume identity")
			}
		}
		return names, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Mounts}}", id).CombinedOutput() //nolint:gosec // exact validated container id.
	if err != nil {
		// An already removed legacy container has no new disk-volume evidence.
		listed, e := exec.CommandContext(ctx, "docker", "ps", "-a", "--no-trunc", "--filter=id="+id, "--format={{.ID}}").Output() //nolint:gosec // validated exact id.
		if e == nil && strings.TrimSpace(string(listed)) == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect check volume ownership: %w", err)
	}
	var mounts []struct{ Type, Name, Destination string }
	if json.Unmarshal(out, &mounts) != nil {
		return nil, errors.New("invalid check mounts")
	}
	var names []string
	for _, mount := range mounts {
		if mount.Type != "volume" {
			continue
		}
		if (mount.Destination != "/workspace" && mount.Destination != "/install-cache") || !validDockerID(mount.Name) {
			return nil, errors.New("unexpected check volume ownership")
		}
		names = append(names, mount.Name)
	}
	if len(names) == 0 {
		return nil, nil
	}
	if err := writeJSONState(path, "check volumes", names); err != nil {
		return nil, err
	}
	if err := syncCheckRunDirectory(path); err != nil {
		return nil, err
	}
	return names, nil
}

func removeRetainedCheckVolumes(ctx context.Context, names []string) error {
	for _, name := range names {
		if !validDockerID(name) {
			return errors.New("invalid retained check volume")
		}
		out, removeErr := exec.CommandContext(ctx, "docker", "volume", "rm", name).CombinedOutput()                                  //nolint:gosec // recorded anonymous volume belonging to this check.
		listed, err := exec.CommandContext(ctx, "docker", "volume", "ls", "--filter=name=^"+name+"$", "--format={{.Name}}").Output() //nolint:gosec // validated fixed-length hexadecimal identity.
		if err != nil {
			return fmt.Errorf("confirm check volume cleanup: %w", err)
		}
		if strings.TrimSpace(string(listed)) != "" {
			if removeErr != nil {
				return fmt.Errorf("check volume remains: %w: %s", removeErr, out)
			}
			return errors.New("check volume remains after successful removal command")
		}
	}
	return nil
}
