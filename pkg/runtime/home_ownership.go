// Copyright 2026 The Scion Authors.

package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/util/fsutil"
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

// agentHomeRepairTimeout bounds one helper run; on expiry (or when the
// caller's context ends) the helper container is removed by name. A
// variable only so tests can shorten it.
var agentHomeRepairTimeout = 5 * time.Minute

// agentHomeRepairCleanupTimeout bounds the removal of a helper container
// after a failed or timed-out run.
const agentHomeRepairCleanupTimeout = 30 * time.Second

// agentHomeRepairNamePrefix prefixes every helper container's name; the
// rest is 16 random hex digits (agentHomeRepairNameRE).
const agentHomeRepairNamePrefix = "scion-home-repair-"

var agentHomeRepairNameRE = regexp.MustCompile(`^scion-home-repair-[0-9a-f]{16}$`)

// agentHomeRepairHelper is one planned helper run: the request (its
// HomeDir already validated and resolved), the container's name, and
// whether the runtime must ask for the host user namespace explicitly.
type agentHomeRepairHelper struct {
	req        AgentHomeOwnershipRepair
	name       string
	userNSHost bool
}

func newAgentHomeRepairName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("agent home ownership repair: helper name: %w", err)
	}
	return agentHomeRepairNamePrefix + hex.EncodeToString(b[:]), nil
}

// resolveAgentHomeRepairHome validates the agent home before it becomes
// the root of a recursive chown: the same agent-home check every agent
// home bind mount goes through (ValidateAgentHomeSource, which resolves
// symlinks and refuses system paths, $HOME and its ancestors, and
// anything under ~/.scion that is not an agent home), then the shared
// recursive-chown root check (fsutil.CheckRoot). It returns the resolved
// path, which is what the helper mounts.
func resolveAgentHomeRepairHome(home string) (string, error) {
	if home == "" {
		return "", errors.New("agent home ownership repair: no agent home path")
	}
	resolved, err := ValidateAgentHomeSource(home, "")
	if err != nil {
		return "", fmt.Errorf("agent home ownership repair: %w", err)
	}
	if err := fsutil.CheckRoot(resolved); err != nil {
		return "", fmt.Errorf("agent home ownership repair: %w", err)
	}
	return resolved, nil
}

// agentHomeRepairArgs returns the argv (after the runtime binary) of the
// one-shot helper that gives h.req.UID:h.req.GID ownership of
// h.req.HomeDir, which must already be resolved by
// resolveAgentHomeRepairHome.
//
// The helper runs code from the agent's own image as root, which every
// normal start of the agent already does: the agent container's entrypoint
// (sciontool init) runs as uid 0 with the runtime's default capabilities
// and the same agent home mounted read-write. The helper's privileges are
// a strict subset of that: every capability is dropped except CHOWN and
// DAC_OVERRIDE (both in the default set; DAC_OVERRIDE lets root walk
// directories the harness made private), it cannot gain privileges, keeps
// the default seccomp and AppArmor profiles, has a read-only root
// filesystem, no network, memory, CPU and process limits, and mounts the
// agent home and nothing else. In particular it never gets
// DAC_READ_SEARCH, which is not in the default set and which the kernel
// requires for open_by_handle_at, the call that could reach host files
// outside the mount. On Podman it also asks for the host user namespace
// explicitly, so a containers.conf default user namespace cannot shift the
// ids it sets.
//
// The argv is all this code controls: the container CLI's and daemon's own
// configuration (DOCKER_HOST and contexts, daemon.json, containers.conf
// default volumes or profiles) is trusted host administration and can
// still shape the helper. A trusted helper that does not depend on that
// is part of ptone/scion#4402.
//
// The walk changes symbolic links themselves (never their targets), does
// not cross into other filesystems, and changes only directories,
// symbolic links and regular files with a single link: a regular file
// with more than one hard link may share its inode with a file outside the
// agent home, and FIFOs, sockets and device nodes are never wanted.
func agentHomeRepairArgs(h agentHomeRepairHelper) ([]string, error) {
	req := h.req
	if req.HomeDir == "" || !strings.HasPrefix(req.HomeDir, "/") || req.HomeDir == "/" {
		return nil, fmt.Errorf("agent home ownership repair: invalid agent home path %q", req.HomeDir)
	}
	// The --volume form splits on ':'; ',' is refused as well so the path
	// is safe in a --mount form too.
	if strings.ContainsAny(req.HomeDir, ":,") {
		return nil, fmt.Errorf("agent home ownership repair: agent home path %q contains ':' or ','", req.HomeDir)
	}
	if req.Image == "" || strings.HasPrefix(req.Image, "-") {
		return nil, fmt.Errorf("agent home ownership repair: invalid image %q", req.Image)
	}
	if req.UID < 0 || req.GID < 0 {
		return nil, fmt.Errorf("agent home ownership repair: invalid owner %d:%d", req.UID, req.GID)
	}
	if !agentHomeRepairNameRE.MatchString(h.name) {
		return nil, fmt.Errorf("agent home ownership repair: invalid helper name %q", h.name)
	}
	args := []string{"run"}
	for _, f := range agentHomeRepairFlags(h) {
		args = append(args, f.flag)
		if f.value != "" {
			args = append(args, f.value)
		}
	}
	args = append(args, req.Image)
	return append(args, agentHomeRepairCommand(req)...), nil
}

type agentHomeRepairFlag struct{ flag, value string }

// agentHomeRepairFlags is the allow-list of the helper's flags (before the
// image) for h: each flag with the exact value it must have, or "" for a
// flag that takes none. Every one of them must be present exactly once.
func agentHomeRepairFlags(h agentHomeRepairHelper) []agentHomeRepairFlag {
	flags := []agentHomeRepairFlag{
		{"--rm", ""},
		{"--name", h.name},
		{"--pull=never", ""},
		{"--network=none", ""},
		{"--user=0:0", ""},
		{"--cap-drop=ALL", ""},
		{"--cap-add=CHOWN", ""},
		{"--cap-add=DAC_OVERRIDE", ""},
		{"--security-opt=no-new-privileges", ""},
		{"--read-only", ""},
		{"--pids-limit=64", ""},
		{"--memory=256m", ""},
		{"--cpus=1", ""},
	}
	if h.userNSHost {
		flags = append(flags, agentHomeRepairFlag{"--userns=host", ""})
	}
	return append(flags,
		agentHomeRepairFlag{"--label", "scion.helper=agent-home-ownership"},
		agentHomeRepairFlag{"--volume", h.req.HomeDir + ":" + agentHomeRepairMount},
		agentHomeRepairFlag{"--entrypoint", "find"},
	)
}

// agentHomeRepairCommand is the helper's command after the image.
func agentHomeRepairCommand(req AgentHomeOwnershipRepair) []string {
	return []string{
		agentHomeRepairMount, "-xdev",
		"(", "-type", "d", "-o", "-type", "l", "-o", "(", "-type", "f", "-links", "1", ")", ")",
		"-exec", "chown", "-h", fmt.Sprintf("%d:%d", req.UID, req.GID), "{}", "+",
	}
}

// validateAgentHomeRepairArgs checks args, fail-closed, against the exact
// helper command for h before it is run: "run", then exactly the flags in
// agentHomeRepairFlags(h) (each once, with its exact value; the only mount
// is the resolved agent home; the name has the helper's prefix and shape),
// then the image, then exactly the expected find expression. Anything
// unknown, missing, repeated or extra refuses the launch, so a change that
// widens the helper's privileges (another capability, --privileged, an
// unconfined profile, another mount, host network) cannot reach the
// runtime.
func validateAgentHomeRepairArgs(args []string, h agentHomeRepairHelper) error {
	refuse := func(format string, a ...any) error {
		return fmt.Errorf("agent home ownership repair: refusing helper command: "+format, a...)
	}
	if !agentHomeRepairNameRE.MatchString(h.name) {
		return refuse("invalid helper name %q", h.name)
	}
	if len(args) == 0 || args[0] != "run" {
		return refuse("does not start with run")
	}
	allowed := agentHomeRepairFlags(h)
	seen := make(map[string]bool, len(allowed))
	i := 1
	for ; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		flag := args[i]
		var f *agentHomeRepairFlag
		for j := range allowed {
			if allowed[j].flag == flag {
				f = &allowed[j]
				break
			}
		}
		if f == nil {
			return refuse("flag %q is not allowed", flag)
		}
		if seen[flag] {
			return refuse("flag %q repeated", flag)
		}
		seen[flag] = true
		if f.value != "" {
			if i+1 >= len(args) || args[i+1] != f.value {
				return refuse("flag %q must have the value %q", flag, f.value)
			}
			i++
		}
	}
	for _, f := range allowed {
		if !seen[f.flag] {
			return refuse("required flag %q missing", f.flag)
		}
	}
	if i >= len(args) || args[i] != h.req.Image {
		return refuse("image is not %q", h.req.Image)
	}
	wantTail := agentHomeRepairCommand(h.req)
	tail := args[i+1:]
	if len(tail) != len(wantTail) {
		return refuse("unexpected command after the image")
	}
	for j := range tail {
		if tail[j] != wantTail[j] {
			return refuse("unexpected command after the image")
		}
	}
	return nil
}

// repairAgentHomeOwnership validates and resolves the agent home, runs the
// helper with command (docker or podman) after validating its argv against
// the allow-list, and, if the run fails, times out or is cancelled, removes
// the helper container by name: killing the CLI alone would leave the
// container running.
func repairAgentHomeOwnership(ctx context.Context, command string, req AgentHomeOwnershipRepair, userNSHost bool) error {
	resolved, err := resolveAgentHomeRepairHome(req.HomeDir)
	if err != nil {
		return err
	}
	req.HomeDir = resolved
	name, err := newAgentHomeRepairName()
	if err != nil {
		return err
	}
	h := agentHomeRepairHelper{req: req, name: name, userNSHost: userNSHost}
	args, err := agentHomeRepairArgs(h)
	if err != nil {
		return err
	}
	if err := validateAgentHomeRepairArgs(args, h); err != nil {
		return err
	}
	runtimeLog.Warn("Repairing agent home ownership with a one-shot helper",
		"home", req.HomeDir, "uid", req.UID, "gid", req.GID, "image", req.Image, "helper", name)
	runCtx, cancel := context.WithTimeout(ctx, agentHomeRepairTimeout)
	defer cancel()
	out, runErr := runSimpleCommand(runCtx, command, args...)
	if runErr == nil {
		return nil
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), agentHomeRepairCleanupTimeout)
	defer cleanupCancel()
	if _, rmErr := runSimpleCommand(cleanupCtx, command, "rm", "-f", name); rmErr != nil {
		runtimeLog.Warn("Could not remove the agent home ownership repair helper", "helper", name, "error", rmErr)
	}
	if ctxErr := runCtx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) && ctx.Err() == nil {
			return fmt.Errorf("ownership repair helper %s timed out after %s and was removed: %w", name, agentHomeRepairTimeout, ctxErr)
		}
		return fmt.Errorf("ownership repair helper %s was cancelled and removed: %w", name, ctxErr)
	}
	if trimmed := strings.TrimSpace(out); trimmed != "" {
		return fmt.Errorf("ownership repair helper failed: %w: %s", runErr, trimmed)
	}
	return fmt.Errorf("ownership repair helper failed: %w", runErr)
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
// Docker and Docker with user-namespace remapping are not supported, and
// neither is a daemon whose mode cannot be determined.
func (r *DockerRuntime) RepairAgentHomeOwnership(ctx context.Context, req AgentHomeOwnershipRepair) error {
	mode, err := dockerUnsupportedRepairMode(ctx, r.Command)
	if err != nil {
		return fmt.Errorf("%w (docker mode undetectable): %w", ErrAgentHomeRepairUnsupported, err)
	}
	if mode != "" {
		return fmt.Errorf("%w (%s)", ErrAgentHomeRepairUnsupported, mode)
	}
	return repairAgentHomeOwnership(ctx, r.Command, req, false)
}

// RepairAgentHomeOwnership implements AgentHomeOwnershipRepairer. Rootful
// Podman shares Docker's helper, run in the host user namespace. Rootless
// Podman maps container uids to subordinate host uids, so a container-side
// chown to the runtime's host uid does not mean the same thing; it is not
// supported there. The mode is probed at repair time and anything but a
// definite rootful answer refuses (the construction-time Rootless flag
// fails open when detection fails).
func (r *PodmanRuntime) RepairAgentHomeOwnership(ctx context.Context, req AgentHomeOwnershipRepair) error {
	if r.Rootless {
		return fmt.Errorf("%w (rootless podman)", ErrAgentHomeRepairUnsupported)
	}
	out, err := runSimpleCommand(ctx, r.Command, "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil {
		return fmt.Errorf("%w (podman mode undetectable): %w", ErrAgentHomeRepairUnsupported, err)
	}
	switch strings.TrimSpace(out) {
	case "false":
		return repairAgentHomeOwnership(ctx, r.Command, req, true)
	case "true":
		return fmt.Errorf("%w (rootless podman)", ErrAgentHomeRepairUnsupported)
	default:
		return fmt.Errorf("%w (podman mode undetectable: %q)", ErrAgentHomeRepairUnsupported, strings.TrimSpace(out))
	}
}
