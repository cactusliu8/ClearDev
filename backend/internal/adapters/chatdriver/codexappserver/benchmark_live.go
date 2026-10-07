package codexappserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// RoleContainerProcess is the already-bound transport supplied by the private
// benchmark runtime. It grants no choice of command, environment or host path to
// ordinary chat requests. The runtime owns container registration and stopping.
type RoleContainerProcess struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Stop   func() error
}

// NewRoleContainer is only for the daemon's validated benchmark assembly. It
// forces the shared ROLE_CONTAINER_V1 posture on start, resume and every turn.
// Merely choosing Auto or setting chat Env cannot select this constructor.
func NewRoleContainer(plugin codexPlugin, log *slog.Logger, launch func(context.Context, string) (*RoleContainerProcess, error)) *Driver {
	d := New(plugin, log)
	d.roleContainer = true
	d.spawn = func(ctx context.Context, _ string, workdir string, _ []string, _ []string) (*process, error) {
		if launch == nil {
			return nil, fmt.Errorf("role container launcher missing")
		}
		p, err := launch(ctx, workdir)
		if err != nil {
			if p != nil && p.Stop != nil {
				err = errors.Join(err, p.Stop())
			}
			return nil, err
		}
		if p == nil || p.Stdin == nil || p.Stdout == nil || p.Stop == nil {
			err = fmt.Errorf("role container transport incomplete")
			if p != nil && p.Stop != nil {
				err = errors.Join(err, p.Stop())
			}
			return nil, err
		}
		return &process{stdin: p.Stdin, stdout: p.Stdout, stop: p.Stop}, nil
	}
	return d
}

func applyRoleContainerSettings(params map[string]any, turn bool) {
	params["approvalPolicy"] = "never"
	delete(params, "approvalsReviewer")
	params["model"] = "gpt-5.6-terra"
	if turn {
		params["sandboxPolicy"] = map[string]any{"type": "dangerFullAccess"}
		params["effort"] = "high"
	} else {
		params["sandbox"] = "danger-full-access"
	}
}

func roleContainerCapabilities() ports.ChatCapabilities {
	caps := capabilities()
	for _, capability := range []ports.ChatCapability{ports.ChatCapabilityFork, ports.ChatCapabilitySkills, ports.ChatCapabilityMCPReload, ports.ChatCapabilityRateLimits} {
		delete(caps, capability)
	}
	return caps
}
