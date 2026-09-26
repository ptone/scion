/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
)

// -----------------------------------------------------------------------
// InitRunOptions.ResolveWorkingDir: ordering and fail-closed contract.
//
// Both tests drive the real RunInit rather than a fake, with
// runGitCloneWorkspace substituted (see its own doc comment for why that
// seam exists instead of a real git clone) so the workspace-preparation
// step ResolveWorkingDir must run after is directly observable.
// -----------------------------------------------------------------------

// withRunGitCloneWorkspace temporarily overrides the runGitCloneWorkspace
// package var.
func withRunGitCloneWorkspace(t *testing.T, f func(uid, gid int, agentHome string) error) {
	t.Helper()
	orig := runGitCloneWorkspace
	runGitCloneWorkspace = f
	t.Cleanup(func() { runGitCloneWorkspace = orig })
}

// setupRunInitAsRootlessScion configures the environment a single RunInit
// call in this file needs to reach past setupHostUser as the same "rootless,
// already the scion user" case substrate-serve runs under in production,
// without requiring the test process to actually be root: scionUserLookup is
// faked to report the test process's own real uid/gid (so setupHostUser's
// "already running as scion user" shortcut applies) with agentHome as its
// home directory, and every env var RunInit reads before reaching
// ResolveWorkingDir is set to a harmless no-op value.
func setupRunInitAsRootlessScion(t *testing.T, agentHome string) {
	t.Helper()
	scrubHubEnv(t)
	t.Setenv("SCION_HOST_UID", "")
	t.Setenv("SCION_HOST_GID", "")
	t.Setenv("SCION_GIT_CLONE_URL", "")
	t.Setenv("HOME", agentHome)
	withScionUserLookup(t, func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: agentHome}, nil
	})
}

// TestRunInit_ResolveWorkingDir_CalledAfterCloneAndOverridesWorkingDir is the
// ordering fix's own regression test: it proves RunInit calls
// InitRunOptions.ResolveWorkingDir only after runGitCloneWorkspace (the
// workspace-preparation step InitRunOptions.ResolveWorkingDir's doc comment
// says it must follow) has run, and that the resolved value — not the
// static WorkingDir field, which this test also sets, to a directory that
// must never be used — is what reaches the harness the real supervisor
// starts. It launches a real child (`sh -c 'pwd > ...'`) rather than
// asserting on supervisor.Config directly, so it also proves the resolved
// value actually reaches exec.Cmd.Dir end to end, the same way
// TestHarnessSupervisorConfig proves the narrower WorkingDir join.
func TestRunInit_ResolveWorkingDir_CalledAfterCloneAndOverridesWorkingDir(t *testing.T) {
	agentHome := t.TempDir()
	setupRunInitAsRootlessScion(t, agentHome)

	var order []string
	withRunGitCloneWorkspace(t, func(uid, gid int, home string) error {
		order = append(order, "clone")
		return nil
	})

	resolvedDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	outFile := filepath.Join(t.TempDir(), "pwd.out")

	opts := InitRunOptions{
		ForwardTermSignal: false,
		// Must never be used: ResolveWorkingDir is also set, and its result
		// must supersede this field outright — see WorkingDir's own doc
		// comment for the documented precedence this pins.
		WorkingDir: "/this-directory-must-never-be-used",
		ResolveWorkingDir: func() (string, error) {
			order = append(order, "resolve")
			return resolvedDir, nil
		},
	}

	got := RunInit([]string{"sh", "-c", "pwd >" + outFile}, opts)
	if got != 0 {
		t.Fatalf("RunInit() = %d, want 0", got)
	}

	wantOrder := []string{"clone", "resolve"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("call order = %v, want %v (ResolveWorkingDir must run after the workspace clone step and before the harness starts)", order, wantOrder)
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("reading harness output: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != resolvedDir {
		t.Errorf("harness ran with cwd %q, want %q (ResolveWorkingDir's result, not the static WorkingDir)", got, resolvedDir)
	}
}

// TestRunInit_ResolveWorkingDirError_ReturnsExitCode18AndNeverStartsHarness
// covers InitRunOptions.ResolveWorkingDir's fail-closed contract: an error
// makes RunInit return exitCodeNoUsableHarnessCwd, report the failure the
// same way every other RunInit failure path does (reportInitFailure), and —
// the property that matters most — never start the harness at all, proven
// by a sentinel file the child would have created never appearing.
func TestRunInit_ResolveWorkingDirError_ReturnsExitCode18AndNeverStartsHarness(t *testing.T) {
	agentHome := t.TempDir()
	setupRunInitAsRootlessScion(t, agentHome)

	var cloneCalled bool
	withRunGitCloneWorkspace(t, func(uid, gid int, home string) error {
		cloneCalled = true
		return nil
	})

	sentinel := filepath.Join(t.TempDir(), "harness-started")
	resolverErr := errors.New("no usable harness working directory for the harness child")

	opts := InitRunOptions{
		ForwardTermSignal: false,
		ResolveWorkingDir: func() (string, error) {
			if !cloneCalled {
				t.Error("ResolveWorkingDir was called before the workspace clone step")
			}
			return "", resolverErr
		},
	}

	got := RunInit([]string{"sh", "-c", "touch " + sentinel}, opts)

	if got != exitCodeNoUsableHarnessCwd {
		t.Fatalf("RunInit() = %d, want exitCodeNoUsableHarnessCwd (%d)", got, exitCodeNoUsableHarnessCwd)
	}
	if !cloneCalled {
		t.Error("the workspace clone step never ran")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Error("the harness process ran despite a ResolveWorkingDir error; it must never start")
	}

	raw, readErr := os.ReadFile(filepath.Join(agentHome, "agent-info.json"))
	if readErr != nil {
		t.Fatalf("expected agent-info.json to be written: %v", readErr)
	}
	var info struct {
		Phase  string `json:"phase"`
		Detail struct {
			Message string `json:"message"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("unmarshal agent-info.json %q: %v", raw, err)
	}
	if info.Phase != string(state.PhaseError) {
		t.Errorf("agent-info.json phase = %q, want %q", info.Phase, state.PhaseError)
	}
	if info.Detail.Message != resolverErr.Error() {
		t.Errorf("agent-info.json detail.message = %q, want %q", info.Detail.Message, resolverErr.Error())
	}
}
