// Copyright 2026 The Scion Authors.

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentHomeOwnershipRepairer is implemented by runtimes that can give the
// agent runtime process back ownership of an agent home it can no longer
// write (ptone/scion#4330). An agent home is bind-mounted into the agent
// container, and the files the harness writes there are owned by the uid
// the harness ran as. When that uid differs from the agent runtime's own
// uid, the runtime cannot update its state in the home on the next start
// (for example, removing a stale provisioner hook).
type AgentHomeOwnershipRepairer interface {
	// RepairAgentHomeOwnership changes the ownership of every entry under
	// req.HomeDir (the directory itself included) to the agent runtime
	// process's uid and gid, through a one-shot privileged helper run from
	// req.Image. It returns ErrAgentHomeRepairUnsupported when this runtime
	// cannot do so in its current mode.
	RepairAgentHomeOwnership(ctx context.Context, req AgentHomeOwnershipRepair) error
}

// AgentHomeOwnershipRepair describes one ownership repair.
type AgentHomeOwnershipRepair struct {
	// HomeDir is the absolute host path of the agent home.
	HomeDir string
	// Image is the agent's image. It must already be present locally; the
	// helper never pulls.
	Image string
}

// ErrAgentHomeRepairUnsupported is returned (wrapped) when the runtime
// cannot repair agent home ownership in its current mode.
var ErrAgentHomeRepairUnsupported = errors.New("agent home ownership repair is not supported by this runtime")

// agentHomeRepairMount is where the helper sees the agent home.
const agentHomeRepairMount = "/scion-agent-home"

// agentHomeRepairArgs returns the argv (after the runtime binary) of the
// one-shot helper that gives uid:gid ownership of req.HomeDir.
//
// The helper mounts only the agent home, has no network, runs as root with
// every capability dropped except the two it needs (CHOWN, and
// DAC_READ_SEARCH to walk directories the harness made private), and
// cannot gain privileges. It changes symbolic links themselves (never
// their targets), does not cross into other filesystems, and leaves
// regular files with more than one hard link unchanged, since such a link
// may share its inode with a file outside the agent home.
func agentHomeRepairArgs(req AgentHomeOwnershipRepair, uid, gid int) ([]string, error) {
	home := filepath.Clean(req.HomeDir)
	if req.HomeDir == "" || !filepath.IsAbs(home) || home == "/" {
		return nil, fmt.Errorf("agent home ownership repair: invalid agent home path %q", req.HomeDir)
	}
	// The -v form splits on ':'; a ',' keeps the path safe for --mount too.
	if strings.ContainsAny(home, ":,") {
		return nil, fmt.Errorf("agent home ownership repair: agent home path %q contains ':' or ','", home)
	}
	if req.Image == "" {
		return nil, fmt.Errorf("agent home ownership repair: no image")
	}
	if uid < 0 || gid < 0 {
		return nil, fmt.Errorf("agent home ownership repair: invalid owner %d:%d", uid, gid)
	}
	owner := fmt.Sprintf("%d:%d", uid, gid)
	return []string{
		"run", "--rm",
		"--pull=never",
		"--network=none",
		"--user=0:0",
		"--cap-drop=ALL",
		"--cap-add=CHOWN",
		"--cap-add=DAC_READ_SEARCH",
		"--security-opt=no-new-privileges",
		"--read-only",
		"--label", "scion.helper=agent-home-ownership",
		"--volume", home + ":" + agentHomeRepairMount,
		"--entrypoint", "find",
		req.Image,
		agentHomeRepairMount, "-xdev",
		"(", "-type", "d", "-o", "-type", "l", "-o", "-links", "1", ")",
		"-exec", "chown", "-h", owner, "{}", "+",
	}, nil
}

// repairAgentHomeOwnership runs the helper with command (docker or podman)
// as the current process's uid and gid.
func repairAgentHomeOwnership(ctx context.Context, command string, req AgentHomeOwnershipRepair) error {
	uid, gid := os.Getuid(), os.Getgid()
	args, err := agentHomeRepairArgs(req, uid, gid)
	if err != nil {
		return err
	}
	runtimeLog.Info("Repairing agent home ownership with a one-shot helper",
		"home", req.HomeDir, "uid", uid, "gid", gid, "image", req.Image)
	out, err := runSimpleCommand(ctx, command, args...)
	if err != nil {
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			return fmt.Errorf("ownership repair helper failed: %w: %s", err, trimmed)
		}
		return fmt.Errorf("ownership repair helper failed: %w", err)
	}
	return nil
}

// RepairAgentHomeOwnership implements AgentHomeOwnershipRepairer.
func (r *DockerRuntime) RepairAgentHomeOwnership(ctx context.Context, req AgentHomeOwnershipRepair) error {
	return repairAgentHomeOwnership(ctx, r.Command, req)
}

// RepairAgentHomeOwnership implements AgentHomeOwnershipRepairer. Rootful
// Podman shares Docker's helper. Rootless Podman maps container uids to
// subordinate host uids, so a container-side chown to the runtime's host
// uid does not mean the same thing; it is not supported there.
func (r *PodmanRuntime) RepairAgentHomeOwnership(ctx context.Context, req AgentHomeOwnershipRepair) error {
	if r.Rootless {
		return fmt.Errorf("%w (rootless podman)", ErrAgentHomeRepairUnsupported)
	}
	return repairAgentHomeOwnership(ctx, r.Command, req)
}
