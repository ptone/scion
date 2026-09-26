/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"context"
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
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/metadata"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/services"
	"gopkg.in/yaml.v3"
)

// -----------------------------------------------------------------------
// InitRunOptions.ResolveWorkingDir: ordering and fail-closed contract.
//
// All tests drive the real RunInit rather than a fake, with
// runGitCloneWorkspace, runPostPreStartOwnershipFixup, runServicesStart,
// runMetadataServerStart and runFetchSecretOverrides substituted (see each
// var's own doc comment for why the seam exists instead of a real clone,
// chown, sidecar process, listener socket or hub request) so the
// workspace-preparation steps ResolveWorkingDir must run after, and the
// harness-adjacent steps it must run before, are directly observable.
// -----------------------------------------------------------------------

// withRunGitCloneWorkspace temporarily overrides the runGitCloneWorkspace
// package var.
func withRunGitCloneWorkspace(t *testing.T, f func(uid, gid int, agentHome string) error) {
	t.Helper()
	orig := runGitCloneWorkspace
	runGitCloneWorkspace = f
	t.Cleanup(func() { runGitCloneWorkspace = orig })
}

// withRunPostPreStartOwnershipFixup temporarily overrides the
// runPostPreStartOwnershipFixup package var.
func withRunPostPreStartOwnershipFixup(t *testing.T, f func(uid, gid int, agentHome string)) {
	t.Helper()
	orig := runPostPreStartOwnershipFixup
	runPostPreStartOwnershipFixup = f
	t.Cleanup(func() { runPostPreStartOwnershipFixup = orig })
}

// withRunServicesStart temporarily overrides the runServicesStart package
// var.
func withRunServicesStart(t *testing.T, f func(ctx context.Context, m *services.Manager, specs []api.ServiceSpec, uid, gid int, username string) error) {
	t.Helper()
	orig := runServicesStart
	runServicesStart = f
	t.Cleanup(func() { runServicesStart = orig })
}

// withRunMetadataServerStart temporarily overrides the
// runMetadataServerStart package var.
func withRunMetadataServerStart(t *testing.T, f func(ctx context.Context, s *metadata.Server) error) {
	t.Helper()
	orig := runMetadataServerStart
	runMetadataServerStart = f
	t.Cleanup(func() { runMetadataServerStart = orig })
}

// withRunFetchSecretOverrides temporarily overrides the
// runFetchSecretOverrides package var.
func withRunFetchSecretOverrides(t *testing.T, f func(client *hub.Client, keys []string) map[string]string) {
	t.Helper()
	orig := runFetchSecretOverrides
	runFetchSecretOverrides = f
	t.Cleanup(func() { runFetchSecretOverrides = orig })
}

// writeServicesYAML stages a scion-services.yaml with one service spec in
// agentHome, so RunInit's sidecar-start block finds specs to start —
// without a real sidecar process ever running, since the test also
// overrides runServicesStart.
func writeServicesYAML(t *testing.T, agentHome string) {
	t.Helper()
	dir := filepath.Join(agentHome, ".scion")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	specs := []api.ServiceSpec{{Name: "order-probe", Command: []string{"true"}}}
	data, err := yaml.Marshal(specs)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scion-services.yaml"), data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
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
	// SCION_METADATA_MODE and SCION_SECRET_KEYS are deliberately left
	// untouched here (not even set to ""): os.LookupEnv treats "present but
	// empty" as a real, if malformed, value — for SCION_METADATA_MODE that
	// still starts a metadata server in the default "block" mode — so a
	// case that needs either path enabled sets it itself, and every other
	// case relies on the ambient environment (scrubbed of SCION_* before
	// the test binary ever runs) genuinely not having it set.
	t.Setenv("HOME", agentHome)
	withScionUserLookup(t, func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: agentHome}, nil
	})
}

// TestRunInit_ResolveWorkingDir_CalledAfterCloneAndOverridesWorkingDir is the
// ordering fix's own regression test: it proves RunInit calls
// InitRunOptions.ResolveWorkingDir only after runGitCloneWorkspace and the
// post-pre-start-hook ownership fixup (the two workspace-preparation steps
// InitRunOptions.ResolveWorkingDir's doc comment says it must follow) have
// run, and before the earliest of the harness-adjacent steps that follow it
// (sidecar services, here — the earliest of the three in RunInit's own
// order). It also proves the resolved value — not the static WorkingDir
// field, which this test also sets, to a directory that must never be used —
// is what reaches the harness the real supervisor starts. It launches a real
// child (`sh -c 'pwd > ...'`) rather than asserting on supervisor.Config
// directly, so it also proves the resolved value actually reaches
// exec.Cmd.Dir end to end, the same way TestHarnessSupervisorConfig proves
// the narrower WorkingDir join.
func TestRunInit_ResolveWorkingDir_CalledAfterCloneAndOverridesWorkingDir(t *testing.T) {
	agentHome := t.TempDir()
	setupRunInitAsRootlessScion(t, agentHome)
	writeServicesYAML(t, agentHome)

	var order []string
	withRunGitCloneWorkspace(t, func(uid, gid int, home string) error {
		order = append(order, "clone")
		return nil
	})
	withRunPostPreStartOwnershipFixup(t, func(uid, gid int, home string) {
		order = append(order, "fixup")
	})
	withRunServicesStart(t, func(_ context.Context, _ *services.Manager, _ []api.ServiceSpec, _, _ int, _ string) error {
		order = append(order, "sidecars")
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

	wantOrder := []string{"clone", "fixup", "resolve", "sidecars"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("call order = %v, want %v (ResolveWorkingDir must run after the workspace clone step and the post-pre-start-hook ownership fixup, and before sidecar services start)", order, wantOrder)
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

// TestRunInit_ResolveWorkingDirError_NeverStartsSidecarsMetadataOrSecretFetch
// covers the other half of the fail-closed contract: not just that the
// harness child never starts (the previous test), but that none of the
// three components RunInit can start on its behalf before building the
// supervisor config — sidecar services, the metadata server, and the hub
// secret fetch — start either. All three preconditions are satisfied here
// (a staged scion-services.yaml, SCION_METADATA_MODE, and a configured hub
// client plus SCION_SECRET_KEYS), so if a regression moved ResolveWorkingDir
// to run after any of them, this test would see that one's seam called
// despite the resolver error.
func TestRunInit_ResolveWorkingDirError_NeverStartsSidecarsMetadataOrSecretFetch(t *testing.T) {
	agentHome := t.TempDir()
	setupRunInitAsRootlessScion(t, agentHome)
	writeServicesYAML(t, agentHome)
	t.Setenv("SCION_METADATA_MODE", "block")
	t.Setenv("SCION_HUB_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("SCION_AUTH_TOKEN", "test-token")
	t.Setenv("SCION_AGENT_ID", "test-agent")
	t.Setenv("SCION_SECRET_KEYS", "some-key")

	withRunGitCloneWorkspace(t, func(uid, gid int, home string) error { return nil })

	var sidecarsRan, metadataRan, secretsRan bool
	withRunServicesStart(t, func(context.Context, *services.Manager, []api.ServiceSpec, int, int, string) error {
		sidecarsRan = true
		return nil
	})
	withRunMetadataServerStart(t, func(context.Context, *metadata.Server) error {
		metadataRan = true
		return nil
	})
	withRunFetchSecretOverrides(t, func(*hub.Client, []string) map[string]string {
		secretsRan = true
		return nil
	})

	resolverErr := errors.New("no usable harness working directory for the harness child")
	opts := InitRunOptions{
		ForwardTermSignal: false,
		ResolveWorkingDir: func() (string, error) {
			return "", resolverErr
		},
	}

	got := RunInit([]string{"sh", "-c", "true"}, opts)
	if got != exitCodeNoUsableHarnessCwd {
		t.Fatalf("RunInit() = %d, want exitCodeNoUsableHarnessCwd (%d)", got, exitCodeNoUsableHarnessCwd)
	}
	if sidecarsRan {
		t.Error("sidecar services started despite a ResolveWorkingDir error; they must never start")
	}
	if metadataRan {
		t.Error("the metadata server started despite a ResolveWorkingDir error; it must never start")
	}
	if secretsRan {
		t.Error("the hub secret fetch ran despite a ResolveWorkingDir error; it must never run")
	}
}

// TestRunInit_NilResolveWorkingDir_UsesStaticWorkingDirUnchanged pins the
// non-substrate path: with ResolveWorkingDir nil (every caller except
// substrate-serve), RunInit must not call it and must hand the static
// WorkingDir to the harness unchanged.
func TestRunInit_NilResolveWorkingDir_UsesStaticWorkingDirUnchanged(t *testing.T) {
	agentHome := t.TempDir()
	setupRunInitAsRootlessScion(t, agentHome)
	withRunGitCloneWorkspace(t, func(uid, gid int, home string) error { return nil })

	staticDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	outFile := filepath.Join(t.TempDir(), "pwd.out")

	got := RunInit([]string{"sh", "-c", "pwd >" + outFile}, InitRunOptions{WorkingDir: staticDir})
	if got != 0 {
		t.Fatalf("RunInit() = %d, want 0", got)
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("reading harness output: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != staticDir {
		t.Errorf("harness ran with cwd %q, want the static WorkingDir %q", got, staticDir)
	}
}
