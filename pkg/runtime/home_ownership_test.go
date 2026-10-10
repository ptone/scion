// Copyright 2026 The Scion Authors.

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
)

var (
	_ AgentHomeOwnershipRepairer = (*DockerRuntime)(nil)
	_ AgentHomeOwnershipRepairer = (*PodmanRuntime)(nil)
)

// rootfulDockerSecurityOptions is `docker info` SecurityOptions output of
// a rootful daemon without user-namespace remapping.
const rootfulDockerSecurityOptions = `["name=apparmor","name=seccomp,profile=builtin","name=cgroupns"]`

// writeArgRecorder writes a fake runtime binary that answers `info` with
// securityOptions and otherwise records its argv, one argument per line,
// and exits with exitCode.
func writeArgRecorder(t *testing.T, exitCode int, securityOptions string) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "fake-runtime")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = info ]; then printf '%%s\\n' %q; exit 0; fi\nprintf '%%s\\n' \"$@\" > %q\necho helper-output\nexit %d\n", securityOptions, argsFile, exitCode)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

func readRecordedArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("runtime binary not invoked: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestAgentHomeRepairArgs(t *testing.T) {
	args, err := agentHomeRepairArgs(AgentHomeOwnershipRepair{HomeDir: "/srv/agents/a1/home/", Image: "img:1", UID: 1002, GID: 1003})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"run", "--rm", "--pull=never", "--network=none", "--user=0:0",
		"--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=DAC_OVERRIDE",
		"--security-opt=no-new-privileges", "--read-only", "--pids-limit=64",
		"/srv/agents/a1/home:" + agentHomeRepairMount,
		"img:1", "-xdev", "1002:1003",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("helper args %q lack %q", args, want)
		}
	}
	if i := slices.Index(args, "--entrypoint"); i < 0 || args[i+1] != "find" {
		t.Errorf("helper entrypoint is not find: %q", args)
	}
	// Symlinks are changed themselves, never their targets.
	if i := slices.Index(args, "chown"); i < 0 || args[i+1] != "-h" {
		t.Errorf("chown does not use -h: %q", args)
	}
	// Only the agent home is mounted, and nothing widens the helper's
	// privileges beyond a normal start's: in particular no
	// DAC_READ_SEARCH (open_by_handle_at).
	joined := strings.Join(args, " ")
	if n := strings.Count(joined, "--volume"); n != 1 {
		t.Errorf("helper mounts %d volumes, want exactly 1", n)
	}
	// The one mount is the agent home, read-write, from the path given.
	if i := slices.Index(args, "--volume"); i < 0 || args[i+1] != "/srv/agents/a1/home:"+agentHomeRepairMount {
		t.Errorf("helper's only mount is not the agent home: %q", args)
	}
	for _, forbidden := range []string{"DAC_READ_SEARCH", "SYS_ADMIN", "--privileged", "unconfined", "--mount", "--device", "-v ", "--cap-add=ALL", "--userns", "--pid=host", "--network=host"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("helper args contain forbidden %q: %q", forbidden, args)
		}
	}
	var capAdds []string
	for _, a := range args {
		if strings.HasPrefix(a, "--cap-add") {
			capAdds = append(capAdds, a)
		}
	}
	if !slices.Equal(capAdds, []string{"--cap-add=CHOWN", "--cap-add=DAC_OVERRIDE"}) {
		t.Errorf("helper capabilities = %q, want only CHOWN and DAC_OVERRIDE", capAdds)
	}

	for _, tc := range []struct {
		name string
		req  AgentHomeOwnershipRepair
		uid  int
	}{
		{"empty home", AgentHomeOwnershipRepair{Image: "img"}, 1},
		{"relative home", AgentHomeOwnershipRepair{HomeDir: "home", Image: "img"}, 1},
		{"root", AgentHomeOwnershipRepair{HomeDir: "/", Image: "img"}, 1},
		{"colon", AgentHomeOwnershipRepair{HomeDir: "/a:b", Image: "img"}, 1},
		{"comma", AgentHomeOwnershipRepair{HomeDir: "/a,b", Image: "img"}, 1},
		{"no image", AgentHomeOwnershipRepair{HomeDir: "/a"}, 1},
		{"option-like image", AgentHomeOwnershipRepair{HomeDir: "/a", Image: "--privileged"}, 1},
		{"negative uid", AgentHomeOwnershipRepair{HomeDir: "/a", Image: "img"}, -1},
	} {
		tc.req.UID, tc.req.GID = tc.uid, 1
		if _, err := agentHomeRepairArgs(tc.req); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}

// The helper's find expression, run directly on a tree, selects
// directories, symlinks and singly-linked files, and skips a regular file
// with a second hard link.
func TestAgentHomeRepairArgs_FindSelection(t *testing.T) {
	if _, err := exec.LookPath("find"); err != nil {
		t.Skip("find not available")
	}
	home := t.TempDir()
	mustWrite := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(home, ".scion", "hooks", "pre-start.d", "20-harness-provision"))
	mustWrite(filepath.Join(home, "agent-info.json"))
	outside := filepath.Join(t.TempDir(), "outside")
	mustWrite(outside)
	if err := os.Link(outside, filepath.Join(home, "hardlink")); err != nil {
		t.Skipf("hard links not supported here: %v", err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(home, "symlink")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(home, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}

	uid, gid := os.Getuid(), os.Getgid()
	args, err := agentHomeRepairArgs(AgentHomeOwnershipRepair{HomeDir: home, Image: "img", UID: uid, GID: gid})
	if err != nil {
		t.Fatal(err)
	}
	findArgs := slices.Clone(args[slices.Index(args, "img")+1:])
	findArgs[0] = home
	// The real expression, chowning to the current ids (a no-op the test
	// process is allowed), succeeds.
	if out, err := exec.Command("find", findArgs...).CombinedOutput(); err != nil {
		t.Fatalf("find with chown failed: %v\n%s", err, out)
	}
	// The same selection, printed.
	sel := append(slices.Clone(findArgs[:slices.Index(findArgs, "-exec")]), "-print")
	out, err := exec.Command("find", sel...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		rel, _ := filepath.Rel(home, l)
		got = append(got, rel)
	}
	sort.Strings(got)
	want := []string{".", ".scion", ".scion/hooks", ".scion/hooks/pre-start.d", ".scion/hooks/pre-start.d/20-harness-provision", "agent-info.json", "symlink"}
	if !slices.Equal(got, want) {
		t.Errorf("selected %q, want %q", got, want)
	}
}

func TestDockerRuntime_RepairAgentHomeOwnership(t *testing.T) {
	bin, argsFile := writeArgRecorder(t, 0, rootfulDockerSecurityOptions)
	req := AgentHomeOwnershipRepair{HomeDir: t.TempDir(), Image: "scion-claude:latest", UID: 1002, GID: 1003}
	r := &DockerRuntime{Command: bin}
	if err := r.RepairAgentHomeOwnership(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	args := readRecordedArgs(t, argsFile)
	want, _ := agentHomeRepairArgs(req)
	if !slices.Equal(args, want) {
		t.Errorf("docker args = %q, want %q", args, want)
	}
	// The helper chowns to the requested owner, not to the test process.
	if !slices.Contains(args, "1002:1003") {
		t.Errorf("docker args %q do not chown to the requested 1002:1003", args)
	}
}

// The repair target and SCION_HOST_UID/GID come from the same function:
// for every backend, the ids buildCommonRunArgs advertises are
// AdvertisedHostOwnerIDs'.
func TestAdvertisedHostOwnerIDs_MatchesRunArgs(t *testing.T) {
	for _, tc := range []struct {
		backend          string
		nfsUID, nfsGID   int
		wantUID, wantGID int
	}{
		{"", 0, 0, os.Getuid(), os.Getgid()},
		{"local", 2000, 2000, os.Getuid(), os.Getgid()},
		{"nfs", 0, 0, 1000, 1000},
		{"nfs", 2001, 2002, 2001, 2002},
	} {
		uid, gid := AdvertisedHostOwnerIDs(tc.backend, tc.nfsUID, tc.nfsGID)
		if uid != tc.wantUID || gid != tc.wantGID {
			t.Errorf("AdvertisedHostOwnerIDs(%q, %d, %d) = %d:%d, want %d:%d", tc.backend, tc.nfsUID, tc.nfsGID, uid, gid, tc.wantUID, tc.wantGID)
		}
		cfg := minimalRunConfig()
		cfg.WorkspaceBackendName, cfg.NFSUID, cfg.NFSGID = tc.backend, tc.nfsUID, tc.nfsGID
		args, err := buildCommonRunArgs(cfg)
		if err != nil {
			t.Fatalf("buildCommonRunArgs(%q): %v", tc.backend, err)
		}
		assertEnvInArgs(t, args, fmt.Sprintf("SCION_HOST_UID=%d", uid), "advertised uid")
		assertEnvInArgs(t, args, fmt.Sprintf("SCION_HOST_GID=%d", gid), "advertised gid")
	}
}

func TestDockerRuntime_RepairAgentHomeOwnershipFailure(t *testing.T) {
	bin, _ := writeArgRecorder(t, 3, rootfulDockerSecurityOptions)
	r := &DockerRuntime{Command: bin}
	err := r.RepairAgentHomeOwnership(context.Background(), AgentHomeOwnershipRepair{HomeDir: t.TempDir(), Image: "img"})
	if err == nil || !strings.Contains(err.Error(), "helper-output") {
		t.Fatalf("expected the helper failure with its output, got %v", err)
	}
}

func TestPodmanRuntime_RepairAgentHomeOwnership(t *testing.T) {
	t.Run("rootful shares the docker helper", func(t *testing.T) {
		bin, argsFile := writeArgRecorder(t, 0, rootfulDockerSecurityOptions)
		req := AgentHomeOwnershipRepair{HomeDir: t.TempDir(), Image: "img", UID: 1002, GID: 1003}
		r := &PodmanRuntime{Command: bin}
		if err := r.RepairAgentHomeOwnership(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		want, _ := agentHomeRepairArgs(req)
		if got := readRecordedArgs(t, argsFile); !slices.Equal(got, want) {
			t.Errorf("podman args = %q, want %q", got, want)
		}
	})
	t.Run("rootless is unsupported", func(t *testing.T) {
		bin, argsFile := writeArgRecorder(t, 0, rootfulDockerSecurityOptions)
		r := &PodmanRuntime{Command: bin, Rootless: true}
		err := r.RepairAgentHomeOwnership(context.Background(), AgentHomeOwnershipRepair{HomeDir: t.TempDir(), Image: "img"})
		if !errors.Is(err, ErrAgentHomeRepairUnsupported) {
			t.Fatalf("err = %v, want ErrAgentHomeRepairUnsupported", err)
		}
		if _, statErr := os.Stat(argsFile); statErr == nil {
			t.Error("rootless podman ran the helper")
		}
	})
}

// Rootless Docker and Docker with user-namespace remapping map container
// uids to other host uids, so the repair is refused there without running
// the helper.
func TestDockerRuntime_RepairAgentHomeOwnershipUnsupportedModes(t *testing.T) {
	for _, tc := range []struct{ opts, mode string }{
		{`["name=seccomp,profile=builtin","name=rootless","name=cgroupns"]`, "(rootless docker)"},
		{`["name=apparmor","name=seccomp,profile=builtin","name=userns"]`, "(docker userns-remap)"},
	} {
		bin, argsFile := writeArgRecorder(t, 0, tc.opts)
		r := &DockerRuntime{Command: bin}
		err := r.RepairAgentHomeOwnership(context.Background(), AgentHomeOwnershipRepair{HomeDir: t.TempDir(), Image: "img", UID: 1, GID: 1})
		if !errors.Is(err, ErrAgentHomeRepairUnsupported) || !strings.Contains(err.Error(), tc.mode) {
			t.Errorf("security options %s: err = %v, want ErrAgentHomeRepairUnsupported naming %s", tc.opts, err, tc.mode)
		}
		if _, statErr := os.Stat(argsFile); statErr == nil {
			t.Errorf("security options %s: the helper ran", tc.opts)
		}
	}
}

// When the daemon's mode cannot be determined, the repair is not run.
func TestDockerRuntime_RepairAgentHomeOwnershipModeDetectionFails(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "fake-runtime")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = info ]; then echo daemon down >&2; exit 1; fi\ntouch %q\n", argsFile)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err := (&DockerRuntime{Command: bin}).RepairAgentHomeOwnership(context.Background(), AgentHomeOwnershipRepair{HomeDir: t.TempDir(), Image: "img", UID: 1, GID: 1})
	if err == nil || !strings.Contains(err.Error(), "detect docker security options") {
		t.Fatalf("err = %v, want a detection error", err)
	}
	if _, statErr := os.Stat(argsFile); statErr == nil {
		t.Error("the helper ran")
	}
}

// The runtime validates the helper's argv against a fail-closed allow-list
// before running it: the built command passes, and any change that adds,
// removes, repeats or alters a flag, the mount, the image or the command
// is refused.
func TestValidateAgentHomeRepairArgs(t *testing.T) {
	req := AgentHomeOwnershipRepair{HomeDir: "/srv/agents/a1/home", Image: "img:1", UID: 1002, GID: 1003}
	args, err := agentHomeRepairArgs(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAgentHomeRepairArgs(args, req); err != nil {
		t.Fatalf("the built helper command is refused: %v", err)
	}
	imageAt := slices.Index(args, "img:1")
	insertBeforeImage := func(extra ...string) []string {
		out := slices.Clone(args[:imageAt])
		out = append(out, extra...)
		return append(out, args[imageAt:]...)
	}
	replace := func(old, new string) []string {
		out := slices.Clone(args)
		out[slices.Index(out, old)] = new
		return out
	}
	without := func(flag string) []string {
		out := slices.Clone(args)
		return slices.Delete(out, slices.Index(out, flag), slices.Index(out, flag)+1)
	}
	for name, bad := range map[string][]string{
		"privileged":          insertBeforeImage("--privileged"),
		"SYS_ADMIN":           insertBeforeImage("--cap-add=SYS_ADMIN"),
		"DAC_READ_SEARCH":     replace("--cap-add=DAC_OVERRIDE", "--cap-add=DAC_READ_SEARCH"),
		"seccomp unconfined":  insertBeforeImage("--security-opt=seccomp=unconfined"),
		"apparmor unconfined": insertBeforeImage("--security-opt", "apparmor=unconfined"),
		"second mount":        insertBeforeImage("--volume", "/:/host"),
		"--mount":             insertBeforeImage("--mount", "type=bind,src=/,dst=/host"),
		"host network":        replace("--network=none", "--network=host"),
		"no cap-drop":         without("--cap-drop=ALL"),
		"no network flag":     without("--network=none"),
		"repeated cap":        insertBeforeImage("--cap-add=CHOWN"),
		"other entrypoint":    replace("find", "sh"),
		"other home":          replace("/srv/agents/a1/home:"+agentHomeRepairMount, "/srv:"+agentHomeRepairMount),
		"read-only home":      replace("/srv/agents/a1/home:"+agentHomeRepairMount, "/srv/agents/a1/home:"+agentHomeRepairMount+":ro"),
		"other image":         replace("img:1", "img:2"),
		"other owner":         replace("1002:1003", "0:0"),
		"extra command":       append(slices.Clone(args), "-delete"),
		"not run":             replace("run", "exec"),
	} {
		if err := validateAgentHomeRepairArgs(bad, req); err == nil {
			t.Errorf("%s: helper command %q was not refused", name, bad)
		}
	}
}
