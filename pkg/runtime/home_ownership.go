// Copyright 2026 The Scion Authors.

package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// AgentHomeOwnershipRepairer is implemented by runtimes that can give the
// agent runtime process back ownership of an agent home it can no longer
// write (ptone/scion#4330). An agent home is bind-mounted into the agent
// container, and the files the harness writes there are owned by the uid
// the harness ran as. When that uid differs from the agent runtime's own
// uid, the runtime cannot update its state in the home on the next start
// (for example, removing a stale provisioner hook).
//
// This is a stopgap. The intended end state is that the agent runtime no
// longer writes its own state into the agent home at all
// (ptone/scion#4402), which removes the need for this repair; do not
// extend it to other uses.
type AgentHomeOwnershipRepairer interface {
	// RepairAgentHomeOwnership changes the ownership of every entry under
	// req.HomeDir (the directory itself included) to req.UID and req.GID,
	// through a one-shot helper container run from req.Image. It returns
	// ErrAgentHomeRepairUnsupported when this runtime cannot do so in its
	// current mode.
	RepairAgentHomeOwnership(ctx context.Context, req AgentHomeOwnershipRepair) error
}

// AgentHomeOwnershipRepair describes one ownership repair.
type AgentHomeOwnershipRepair struct {
	// HomeDir is the absolute host path of the agent home.
	HomeDir string
	// Image is the agent's image. It must already be present locally; the
	// helper never pulls.
	Image string
	// UID and GID are the new owner: the ids the runtime advertises to the
	// agent container (AdvertisedHostOwnerIDs), which are also the agent
	// runtime process's own ids whenever a repair is allowed.
	UID, GID int
}

// ErrAgentHomeRepairUnsupported is returned (wrapped) when the runtime
// cannot repair agent home ownership in its current mode.
var ErrAgentHomeRepairUnsupported = errors.New("agent home ownership repair is not supported by this runtime")

// agentHomeRepairMount is where the helper sees the agent home.
const agentHomeRepairMount = "/scion-agent-home"

// agentHomeRepairTimeout bounds one helper run.
const agentHomeRepairTimeout = 5 * time.Minute

// agentHomeRepairArgs returns the argv (after the runtime binary) of the
// one-shot helper that gives req.UID:req.GID ownership of req.HomeDir.
//
// The helper runs code from the agent's own image as root, which every
// normal start of the agent already does: the agent container's entrypoint
// (sciontool init) runs as uid 0 with the runtime's default capabilities
// and the same agent home mounted read-write. The helper's privileges are
// a strict subset of that: every capability is dropped except CHOWN and
// DAC_OVERRIDE (both in the default set; DAC_OVERRIDE lets root walk
// directories the harness made private), it cannot gain privileges, keeps
// the default seccomp and AppArmor profiles, has a read-only root
// filesystem, no network and a process limit, and mounts the agent home and
// nothing else. In particular it never gets DAC_READ_SEARCH, which is not
// in the default set and would allow open_by_handle_at to reach host files
// outside the mount.
//
// The walk changes symbolic links themselves (never their targets), does
// not cross into other filesystems, and changes only directories,
// symbolic links and regular files with a single link: a regular file
// with more than one hard link may share its inode with a file outside the
// agent home, and FIFOs, sockets and device nodes are never wanted.
func agentHomeRepairArgs(req AgentHomeOwnershipRepair) ([]string, error) {
	home := filepath.Clean(req.HomeDir)
	if req.HomeDir == "" || !filepath.IsAbs(home) || home == "/" {
		return nil, fmt.Errorf("agent home ownership repair: invalid agent home path %q", req.HomeDir)
	}
	// The --volume form splits on ':'; ',' is refused as well so the path
	// is safe in a --mount form too.
	if strings.ContainsAny(home, ":,") {
		return nil, fmt.Errorf("agent home ownership repair: agent home path %q contains ':' or ','", home)
	}
	if req.Image == "" || strings.HasPrefix(req.Image, "-") {
		return nil, fmt.Errorf("agent home ownership repair: invalid image %q", req.Image)
	}
	if req.UID < 0 || req.GID < 0 {
		return nil, fmt.Errorf("agent home ownership repair: invalid owner %d:%d", req.UID, req.GID)
	}
	owner := fmt.Sprintf("%d:%d", req.UID, req.GID)
	return []string{
		"run", "--rm",
		"--pull=never",
		"--network=none",
		"--user=0:0",
		"--cap-drop=ALL",
		"--cap-add=CHOWN",
		"--cap-add=DAC_OVERRIDE",
		"--security-opt=no-new-privileges",
		"--read-only",
		"--pids-limit=64",
		"--label", "scion.helper=agent-home-ownership",
		"--volume", home + ":" + agentHomeRepairMount,
		"--entrypoint", "find",
		req.Image,
		agentHomeRepairMount, "-xdev",
		"(", "-type", "d", "-o", "-type", "l", "-o", "(", "-type", "f", "-links", "1", ")", ")",
		"-exec", "chown", "-h", owner, "{}", "+",
	}, nil
}

// repairAgentHomeOwnership runs the helper with command (docker or podman).
func repairAgentHomeOwnership(ctx context.Context, command string, req AgentHomeOwnershipRepair) error {
	args, err := agentHomeRepairArgs(req)
	if err != nil {
		return err
	}
	runtimeLog.Warn("Repairing agent home ownership with a one-shot helper",
		"home", req.HomeDir, "uid", req.UID, "gid", req.GID, "image", req.Image)
	ctx, cancel := context.WithTimeout(ctx, agentHomeRepairTimeout)
	defer cancel()
	out, err := runSimpleCommand(ctx, command, args...)
	if err != nil {
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			return fmt.Errorf("ownership repair helper failed: %w: %s", err, trimmed)
		}
		return fmt.Errorf("ownership repair helper failed: %w", err)
	}
	return nil
}

// dockerUnsupportedRepairMode returns a non-empty mode name when the
// Docker daemon runs rootless or with user-namespace remapping. In both,
// container uids map to other host uids, so a chown inside the helper
// would not give the agent runtime's host uid ownership.
func dockerUnsupportedRepairMode(ctx context.Context, command string) (string, error) {
	out, err := runSimpleCommand(ctx, command, "info", "--format", "{{json .SecurityOptions}}")
	if err != nil {
		return "", fmt.Errorf("detect docker security options: %w", err)
	}
	switch {
	case strings.Contains(out, "name=rootless"):
		return "rootless docker", nil
	case strings.Contains(out, "name=userns"):
		return "docker userns-remap", nil
	}
	return "", nil
}

// RepairAgentHomeOwnership implements AgentHomeOwnershipRepairer. Rootless
// Docker and Docker with user-namespace remapping are not supported.
func (r *DockerRuntime) RepairAgentHomeOwnership(ctx context.Context, req AgentHomeOwnershipRepair) error {
	mode, err := dockerUnsupportedRepairMode(ctx, r.Command)
	if err != nil {
		return err
	}
	if mode != "" {
		return fmt.Errorf("%w (%s)", ErrAgentHomeRepairUnsupported, mode)
	}
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
