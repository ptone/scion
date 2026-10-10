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
	"testing"
)

var (
	_ AgentHomeOwnershipRepairer = (*DockerRuntime)(nil)
	_ AgentHomeOwnershipRepairer = (*PodmanRuntime)(nil)
)

// writeArgRecorder writes a fake runtime binary that records its argv, one
// argument per line, and exits with exitCode.
func writeArgRecorder(t *testing.T, exitCode int) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "fake-runtime")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\necho helper-output\nexit %d\n", argsFile, exitCode)
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
		"--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=DAC_READ_SEARCH",
		"--security-opt=no-new-privileges", "--read-only",
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
	// Only the agent home is mounted.
	if n := strings.Count(strings.Join(args, " "), "--volume"); n != 1 {
		t.Errorf("helper mounts %d volumes, want 1", n)
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
	bin, argsFile := writeArgRecorder(t, 0)
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
	bin, _ := writeArgRecorder(t, 3)
	r := &DockerRuntime{Command: bin}
	err := r.RepairAgentHomeOwnership(context.Background(), AgentHomeOwnershipRepair{HomeDir: t.TempDir(), Image: "img"})
	if err == nil || !strings.Contains(err.Error(), "helper-output") {
		t.Fatalf("expected the helper failure with its output, got %v", err)
	}
}

func TestPodmanRuntime_RepairAgentHomeOwnership(t *testing.T) {
	t.Run("rootful shares the docker helper", func(t *testing.T) {
		bin, argsFile := writeArgRecorder(t, 0)
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
		bin, argsFile := writeArgRecorder(t, 0)
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
