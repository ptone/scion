/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/services"
	"github.com/GoogleCloudPlatform/scion/pkg/stagedsecrets"
	"github.com/GoogleCloudPlatform/scion/pkg/substratecaps"
)

// doSubstrateServeJSON drives an HTTP request through a *substrate.Server's
// Handler() the same way pkg/sciontool/substrate's own tests do, without
// this package importing that type by name (Go infers it from
// newSubstrateServeServer's return value).
func doSubstrateServeJSON(t *testing.T, srv interface{ Handler() http.Handler }, method, path, bearer string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// -----------------------------------------------------------------------
// checkPrivilegeDropFeasible: the synchronous /bootstrap precondition.
// -----------------------------------------------------------------------

// TestParseCapBit covers parseCapBit generically (the function hasCapBit
// uses to check any capability in substratecaps.Required, not just
// SETUID) — bit 6 (CAP_SETGID), bit 1 (CAP_DAC_OVERRIDE) and bit 0
// (CAP_CHOWN) alongside a few edge cases, complementing TestParseCapSetUID's
// bit-7-specific coverage.
func TestParseCapBit(t *testing.T) {
	tests := []struct {
		name  string
		input string
		bit   uint
		want  bool
	}{
		{
			name:  "bit 6 (SETGID) present among limited caps",
			input: "Name:\tinit\nCapEff:\t00000000000000ff\n",
			bit:   6,
			want:  true,
		},
		{
			name:  "bit 6 (SETGID) absent (only SETUID bit set)",
			input: "Name:\tinit\nCapEff:\t0000000000000080\n",
			bit:   6,
			want:  false,
		},
		{
			name:  "only bit 6 set",
			input: "CapEff:\t0000000000000040\n",
			bit:   6,
			want:  true,
		},
		{
			name:  "bit 0 (CHOWN) set",
			input: "CapEff:\t0000000000000001\n",
			bit:   0,
			want:  true,
		},
		{
			name:  "bit 1 (DAC_OVERRIDE) absent (SETUID/SETGID/CHOWN set, DAC_OVERRIDE not)",
			input: "CapEff:\t00000000000000c1\n",
			bit:   1,
			want:  false,
		},
		{
			name:  "bit 1 (DAC_OVERRIDE) present",
			input: "CapEff:\t0000000000000002\n",
			bit:   1,
			want:  true,
		},
		{
			name:  "no capabilities",
			input: "CapEff:\t0000000000000000\n",
			bit:   6,
			want:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCapBit(tc.input, tc.bit); got != tc.want {
				t.Errorf("parseCapBit(%q, %d) = %v, want %v", tc.input, tc.bit, got, tc.want)
			}
		})
	}
}

// fakeFileInfo is a minimal fs.FileInfo whose Sys() returns a
// *syscall.Stat_t, so canSearchDir/homeOwnedAndWritable's type assertion
// succeeds against a value that was never really stat'd.
type fakeFileInfo struct {
	mode     fs.FileMode
	uid, gid uint32
}

func (f fakeFileInfo) Name() string       { return "" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid, Gid: f.gid} }

// fakeStatAllTraversableAndOwned is checkPrivilegeDropFeasible's statPath
// default for tests: every path is a directory, mode 0755, owned by
// uid:gid 1000:1000 — traversable by everyone, owned and writable by the
// target uid used throughout these tests.
func fakeStatAllTraversableAndOwned(string) (fs.FileInfo, error) {
	return fakeFileInfo{mode: fs.ModeDir | 0o755, uid: 1000, gid: 1000}, nil
}

// fakePrivilegeDropDeps builds privilegeDropPreconditionDeps with every
// dependency controllable, defaulting to a fully-feasible environment (every
// capability present, every path traversable and correctly owned) so each
// test case only needs to override the one thing it's testing.
func fakePrivilegeDropDeps(t *testing.T) privilegeDropPreconditionDeps {
	t.Helper()
	env := map[string]string{"SCION_HOST_UID": "1000", "SCION_HOST_GID": "1000"}
	return privilegeDropPreconditionDeps{
		hasCapBit: func(uint) bool { return true },
		lookupUser: func(string) (*user.User, error) {
			return &user.User{Username: "scion", Uid: "1000", Gid: "1000", HomeDir: "/home/scion"}, nil
		},
		getenv:   func(k string) string { return env[k] },
		statPath: fakeStatAllTraversableAndOwned,
	}
}

// fakeHasCapBitMissing returns a hasCapBit fake that reports every bit
// present except the ones listed.
func fakeHasCapBitMissing(missing ...uint) func(uint) bool {
	missingSet := make(map[uint]bool, len(missing))
	for _, b := range missing {
		missingSet[b] = true
	}
	return func(bit uint) bool { return !missingSet[bit] }
}

// TestDefaultScionUserLookup_RefusesUnderTest and
// TestDefaultLookupUserByID_RefusesUnderTest prove the second, independent
// defense in defaultScionUserLookup/defaultLookupUserByID's own doc
// comment: called directly (as if TestMain's own override of the
// scionUserLookup/lookupUserByID vars had been accidentally removed —
// exactly the incident these exist to catch a second time), neither may
// ever resolve a real account while running under `go test`.
func TestDefaultScionUserLookup_RefusesUnderTest(t *testing.T) {
	if _, err := defaultScionUserLookup("scion"); !errors.Is(err, errRealUserLookupDisabledUnderTest) {
		t.Errorf("defaultScionUserLookup(%q) error = %v, want errRealUserLookupDisabledUnderTest", "scion", err)
	}
}

func TestDefaultLookupUserByID_RefusesUnderTest(t *testing.T) {
	if _, err := defaultLookupUserByID("0"); !errors.Is(err, errRealUserLookupDisabledUnderTest) {
		t.Errorf("defaultLookupUserByID(%q) error = %v, want errRealUserLookupDisabledUnderTest", "0", err)
	}
}

func TestCheckPrivilegeDropFeasible_AllPresent_Passes(t *testing.T) {
	if err := checkPrivilegeDropFeasible(fakePrivilegeDropDeps(t)); err != nil {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want nil", err)
	}
}

// TestCheckPrivilegeDropFeasible_EveryRequiredCapabilityIsChecked is (A)'s
// direct proof that checkPrivilegeDropFeasible verifies the FULL committed
// capability set (substratecaps.Required), not a hardcoded SETUID/SETGID
// pair: for each capability in that shared list, simulating just that one
// missing must trip the precondition. Because both this loop and
// checkPrivilegeDropFeasible's own loop iterate the same substratecaps.
// Required slice, a capability added there is automatically covered here
// too — this is what "can never drift apart" means in practice.
//
// Mutation check performed: temporarily hardcoding
// checkPrivilegeDropFeasible's loop to only
// `!d.hasCapBit(7) || !d.hasCapBit(6)` (the old SETUID/SETGID-only check)
// makes this test's "CHOWN" subtest fail (confirmed locally, reverted).
func TestCheckPrivilegeDropFeasible_EveryRequiredCapabilityIsChecked(t *testing.T) {
	for _, c := range substratecaps.Required {
		t.Run(c.Name, func(t *testing.T) {
			d := fakePrivilegeDropDeps(t)
			d.hasCapBit = fakeHasCapBitMissing(c.EffBit)
			if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
				t.Errorf("checkPrivilegeDropFeasible() = %v with only %s (bit %d) missing, want errPrivilegeDropPrecondition", err, c.Name, c.EffBit)
			}
		})
	}
}

func TestCheckPrivilegeDropFeasible_ScionUserUnresolvable_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	d.lookupUser = func(string) (*user.User, error) { return nil, errors.New("unknown user scion") }
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition", err)
	}
}

func TestCheckPrivilegeDropFeasible_HostUIDGIDMissing_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	d.getenv = func(string) string { return "" }
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition", err)
	}
}

func TestCheckPrivilegeDropFeasible_HostUIDGIDUnparseable_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	d.getenv = func(k string) string {
		if k == "SCION_HOST_UID" {
			return "not-a-number"
		}
		return "1000"
	}
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition", err)
	}
}

// -----------------------------------------------------------------------
// checkPrivilegeDropFeasible's rootfs traversability checks.
// -----------------------------------------------------------------------

// statPathOverride builds a statPath fake that returns override for the
// given path and fakeStatAllTraversableAndOwned's default for everything
// else.
func statPathOverride(path string, override fakeFileInfo) func(string) (fs.FileInfo, error) {
	return func(p string) (fs.FileInfo, error) {
		if p == path {
			return override, nil
		}
		return fakeStatAllTraversableAndOwned(p)
	}
}

func TestCheckPrivilegeDropFeasible_RootNotTraversable_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	// '/' owned by root (uid 0), mode 0700: the target uid (1000) is
	// neither owner nor group, and other has no x bit.
	d.statPath = statPathOverride("/", fakeFileInfo{mode: fs.ModeDir | 0o700, uid: 0, gid: 0})
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition (root not traversable)", err)
	}
}

func TestCheckPrivilegeDropFeasible_HomeParentNotTraversable_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	// /home (a parent of /home/scion, the fake user's HomeDir) not
	// traversable by the target uid/gid.
	d.statPath = statPathOverride("/home", fakeFileInfo{mode: fs.ModeDir | 0o700, uid: 0, gid: 0})
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition (home parent not traversable)", err)
	}
}

func TestCheckPrivilegeDropFeasible_WorkspaceParentNotTraversable_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	d.getenv = func(k string) string {
		switch k {
		case "SCION_HOST_UID", "SCION_HOST_GID":
			return "1000"
		case "SCION_WORKSPACE_PATH":
			return "/srv/workspace"
		}
		return ""
	}
	// /srv (a parent of the configured workspace path) not traversable.
	d.statPath = statPathOverride("/srv", fakeFileInfo{mode: fs.ModeDir | 0o700, uid: 0, gid: 0})
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition (workspace parent not traversable)", err)
	}
}

func TestCheckPrivilegeDropFeasible_HomeNotOwnedByTarget_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	// $HOME (/home/scion) owned by root, not the target uid.
	d.statPath = statPathOverride("/home/scion", fakeFileInfo{mode: fs.ModeDir | 0o755, uid: 0, gid: 0})
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition (home not owned by target)", err)
	}
}

func TestCheckPrivilegeDropFeasible_HomeNotWritable_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	// $HOME owned by the target uid, but with no write bit for anyone
	// (0555 — readable/traversable, never writable).
	d.statPath = statPathOverride("/home/scion", fakeFileInfo{mode: fs.ModeDir | 0o555, uid: 1000, gid: 1000})
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition (home not writable)", err)
	}
}

// TestCheckPrivilegeDropFeasible_HomeWritableButNotTraversable_Fails covers
// a $HOME with the owner write bit but not the owner execute bit (0600):
// writable in principle, but not reachable, so it must still fail closed.
func TestCheckPrivilegeDropFeasible_HomeWritableButNotTraversable_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	d.statPath = statPathOverride("/home/scion", fakeFileInfo{mode: fs.ModeDir | 0o600, uid: 1000, gid: 1000})
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition (home writable but not traversable)", err)
	}
}

func TestCheckPrivilegeDropFeasible_TraversableAndOwned_Passes(t *testing.T) {
	// The happy path: every default from fakePrivilegeDropDeps already
	// satisfies traversability and home ownership/writability, so this is
	// the same as TestCheckPrivilegeDropFeasible_AllPresent_Passes,
	// restated here to anchor it explicitly against the traversability
	// requirement rather than only the capability/user/env one.
	d := fakePrivilegeDropDeps(t)
	if err := checkPrivilegeDropFeasible(d); err != nil {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want nil", err)
	}
}

// TestDefaultPrivilegeDropPreconditionDeps_LookupUserGoesThroughScionUserLookup
// pins defaultPrivilegeDropPreconditionDeps.lookupUser's routing through the
// scionUserLookup var (see its own doc comment for why this must be a
// closure, not the var's value bound at package-init time): reverting to a
// direct user.Lookup call would pass every other test in this file, so
// nothing else catches that regression.
func TestDefaultPrivilegeDropPreconditionDeps_LookupUserGoesThroughScionUserLookup(t *testing.T) {
	var called bool
	withScionUserLookup(t, func(username string) (*user.User, error) {
		called = true
		return &user.User{Username: username, Uid: "1000", Gid: "1000", HomeDir: "/home/scion"}, nil
	})
	if _, err := defaultPrivilegeDropPreconditionDeps.lookupUser("scion"); err != nil {
		t.Fatalf("lookupUser(%q) = %v, want nil", "scion", err)
	}
	if !called {
		t.Error("defaultPrivilegeDropPreconditionDeps.lookupUser did not go through the scionUserLookup var")
	}
}

// -----------------------------------------------------------------------
// substrate-serve's wiring (extracted so a mutation is caught by a test).
// -----------------------------------------------------------------------

// TestSubstrateServeInitOptions_RequiresPrivilegeDrop asserts that
// substrate-serve's InitRunner passes RequirePrivilegeDrop: true to RunInit.
// Mutation check performed: flipping this function's literal to
// RequirePrivilegeDrop: false makes this test fail (confirmed locally by
// editing substrate_serve.go and re-running `go test -run
// TestSubstrateServeInitOptions_RequiresPrivilegeDrop`; reverted after).
func TestSubstrateServeInitOptions_RequiresPrivilegeDrop(t *testing.T) {
	withScionUserLookup(t, func(username string) (*user.User, error) {
		return &user.User{Username: username, Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: t.TempDir()}, nil
	})

	opts := substrateServeInitOptions(true)
	if !opts.RequirePrivilegeDrop {
		t.Error("substrateServeInitOptions(...).RequirePrivilegeDrop = false, want true — substrate must never start the harness as root")
	}
	if !opts.ForwardTermSignal {
		t.Error("substrateServeInitOptions(true).ForwardTermSignal = false, want true (passthrough)")
	}
	if opts.ResolveWorkingDir == nil {
		t.Error("substrateServeInitOptions(true).ResolveWorkingDir = nil, want a resolver — RunInit calls it after preparing the workspace")
	}
	if opts.WorkingDir != "" {
		t.Errorf("substrateServeInitOptions(true).WorkingDir = %q, want \"\" — resolution is deferred to ResolveWorkingDir", opts.WorkingDir)
	}
	opts2 := substrateServeInitOptions(false)
	if opts2.ForwardTermSignal {
		t.Error("substrateServeInitOptions(false).ForwardTermSignal = true, want false (passthrough)")
	}
}

// TestNewSubstrateServeServer_PrivilegeDropPreconditionRejectsBootstrap
// drives the exact server construction runSubstrateServe uses (real
// PrivilegeDropChecker; a stubbed init runner — see newSubstrateServeServer's
// doc comment for why it's never the real RunInit in a test) and proves the
// wiring itself: with SCION_HOST_UID/GID absent from the process
// environment, the deterministic branch of checkPrivilegeDropFeasible, the
// bootstrap must be rejected without ever invoking the init runner. If
// WithPrivilegeDropChecker were ever dropped from newSubstrateServeServer,
// this test would instead see 200 and its own assertions would fail —
// cleanly, as a normal test failure, not by driving a real RunInit — this
// is the "disabling the precondition must fail a test" mutation check.
func TestNewSubstrateServeServer_PrivilegeDropPreconditionRejectsBootstrap(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	t.Setenv("SCION_HOST_GID", "")
	scrubHubEnv(t)

	var initCalled bool
	stubRunInit := func(argv []string, opts InitRunOptions) int {
		initCalled = true
		return 0
	}

	srv := newSubstrateServeServer(stubRunInit)
	rec := doSubstrateServeJSON(t, srv, "POST", "/scion/v1/bootstrap", "any-token", map[string]any{
		"env":           map[string]string{},
		"files":         []any{},
		"start_cmd":     "true",
		"control_token": "tok",
	})

	if rec.Code == 200 || rec.Code < 400 {
		t.Errorf("status = %d, want a non-2xx rejection (SCION_HOST_UID/GID are unset)", rec.Code)
	}
	// Give any wrongly-started goroutine a moment to flip the flag before
	// asserting it never did (the real init runner call happens
	// asynchronously — see handleBootstrap).
	time.Sleep(20 * time.Millisecond)
	if initCalled {
		t.Error("the init runner was invoked despite the privilege-drop precondition failing; the harness must never start")
	}
}

// -----------------------------------------------------------------------
// RunInit's defence-in-depth failure reporting (real integration —
// drives the actual RunInit, not a stub).
// -----------------------------------------------------------------------

// TestReportInitFailure_WritesPhaseErrorAndMessage proves the shared
// reporting helper every RunInit failure path (including the privilege-drop
// defence-in-depth check) calls: it must report PhaseError to local
// agent-info state (the same way the git-clone failure path pioneered),
// not just log it. Driven directly against a temp directory rather than
// through RunInit's real setupHostUser/resolveAgentHome, which resolve
// against whatever "scion" user (if any) actually exists on the machine
// running the test.
func TestReportInitFailure_WritesPhaseErrorAndMessage(t *testing.T) {
	scrubHubEnv(t)
	tmpHome := t.TempDir()

	reportInitFailure(tmpHome, errPrivilegeDropRequired)

	raw, err := os.ReadFile(filepath.Join(tmpHome, "agent-info.json"))
	if err != nil {
		t.Fatalf("expected agent-info.json to be written: %v", err)
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
	if info.Detail.Message != errPrivilegeDropRequired.Error() {
		t.Errorf("agent-info.json detail.message = %q, want %q", info.Detail.Message, errPrivilegeDropRequired.Error())
	}
}

// TestRunInit_PrivilegeDropFailure_ReturnsSentinel drives the real RunInit
// with RequirePrivilegeDrop: true and SCION_HOST_UID/GID unset — the one
// combination that fails closed regardless of whether this test happens to
// run as root, with real capabilities, or as a machine account literally
// named "scion" (setupHostUser's rootless-shortcut branch), since none of
// those branches ever produce a non-zero targetUID without
// SCION_HOST_UID/GID set. It asserts only the sentinel return code, which
// is independent of where resolveAgentHome happens to land on this
// particular machine (see TestReportInitFailure_* for that part).
func TestRunInit_PrivilegeDropFailure_ReturnsSentinel(t *testing.T) {
	scrubHubEnv(t)
	t.Setenv("SCION_HOST_UID", "")
	t.Setenv("SCION_HOST_GID", "")
	t.Setenv("HOME", t.TempDir())

	got := RunInit([]string{"true"}, InitRunOptions{ForwardTermSignal: false, RequirePrivilegeDrop: true})
	if got != exitCodePrivilegeDropRequired {
		t.Fatalf("RunInit() = %d, want exitCodePrivilegeDropRequired (%d)", got, exitCodePrivilegeDropRequired)
	}
}

// TestRunInit_StagedSecretsDecodeFailure_ReportsInitFailure drives the real
// RunInit through a *different* pre-launch failure path than the
// privilege-drop gate, to prove reportInitFailure was actually wired at
// this call site too, not just the privilege-drop one — every RunInit
// failure path that returns before the harness launches must report, not
// just the one this package's tests happen to exercise most. SCION_STAGED_SECRETS
// holding undecodable data makes stagedsecrets.Decode fail before anything
// else in RunInit runs.
func TestRunInit_StagedSecretsDecodeFailure_ReportsInitFailure(t *testing.T) {
	scrubHubEnv(t)
	t.Setenv("SCION_HOST_UID", "")
	t.Setenv("SCION_HOST_GID", "")
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv(stagedsecrets.EnvVar, "not valid base64 or json!!!")

	got := RunInit([]string{"true"}, InitRunOptions{ForwardTermSignal: false})
	if got == 0 {
		t.Fatal("RunInit() = 0, want non-zero for an undecodable SCION_STAGED_SECRETS payload")
	}

	raw, err := os.ReadFile(filepath.Join(tmpHome, "agent-info.json"))
	if err != nil {
		t.Fatalf("expected agent-info.json to be written: %v", err)
	}
	var info struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("unmarshal agent-info.json %q: %v", raw, err)
	}
	if info.Phase != string(state.PhaseError) {
		t.Errorf("agent-info.json phase = %q, want %q", info.Phase, state.PhaseError)
	}
}

// -----------------------------------------------------------------------
// adjustScionUser's requirePrivilegeDrop-gated fail-closed checks.
// -----------------------------------------------------------------------

// withScionUserLookup temporarily overrides the scionUserLookup package var.
func withScionUserLookup(t *testing.T, f func(string) (*user.User, error)) {
	t.Helper()
	orig := scionUserLookup
	scionUserLookup = f
	t.Cleanup(func() { scionUserLookup = orig })
}

// withRunDirectSetUID temporarily overrides the runDirectSetUID package var.
func withRunDirectSetUID(t *testing.T, f func(string, string, string) error) {
	t.Helper()
	orig := runDirectSetUID
	runDirectSetUID = f
	t.Cleanup(func() { runDirectSetUID = orig })
}

func TestAdjustScionUser_AlreadyCorrect_ShortCircuitsRegardlessOfFlag(t *testing.T) {
	withScionUserLookup(t, func(string) (*user.User, error) {
		return &user.User{Uid: "1000", Gid: "1000"}, nil
	})
	var directCalled bool
	withRunDirectSetUID(t, func(string, string, string) error {
		directCalled = true
		return nil
	})

	for _, requirePrivilegeDrop := range []bool{true, false} {
		uid, gid, rootless := adjustScionUser(1000, 1000, "1000", "1000", requirePrivilegeDrop)
		if uid != 1000 || gid != 1000 || rootless != false {
			t.Errorf("requirePrivilegeDrop=%v: adjustScionUser() = (%d,%d,%v), want (1000,1000,false)", requirePrivilegeDrop, uid, gid, rootless)
		}
	}
	if directCalled {
		t.Error("directSetUID was called even though the scion user already had the correct UID/GID")
	}
}

// TestAdjustScionUser_ScionUserNotFound covers the first fail-closed case: under
// RequirePrivilegeDrop, a missing scion user must fail closed; every other
// runtime (RequirePrivilegeDrop: false) must see the exact same
// (uid, gid, false) it always has, since those runtimes depend on the
// historical fallback and must not be changed here.
func TestAdjustScionUser_ScionUserNotFound(t *testing.T) {
	withScionUserLookup(t, func(string) (*user.User, error) {
		return nil, errors.New("user: unknown user scion")
	})
	// Force the direct-edit branch (skip a real usermod/groupmod exec call).
	t.Setenv("SCION_ALT_USERMOD", "1")

	t.Run("RequirePrivilegeDrop=true fails closed", func(t *testing.T) {
		var calls int
		withRunDirectSetUID(t, func(string, string, string) error {
			calls++
			return nil
		})
		uid, gid, rootless := adjustScionUser(1000, 1000, "1000", "1000", true)
		if uid != 0 || gid != 0 || rootless != false {
			t.Errorf("adjustScionUser(..., true) = (%d,%d,%v), want (0,0,false)", uid, gid, rootless)
		}
		// Mutation check: runDirectSetUID must never be called once the
		// early return fires — this is what actually pins "never even try
		// the rewrite," not just "the return value happens to come out
		// right."
		if calls != 0 {
			t.Errorf("runDirectSetUID was called %d time(s) after a failed scion user lookup with requirePrivilegeDrop=true, want 0 — the early return must skip the rewrite attempt entirely, not just fail closed on its result", calls)
		}
	})

	t.Run("RequirePrivilegeDrop=false is unchanged", func(t *testing.T) {
		withRunDirectSetUID(t, func(string, string, string) error { return nil })
		uid, gid, rootless := adjustScionUser(1000, 1000, "1000", "1000", false)
		if uid != 1000 || gid != 1000 || rootless != false {
			t.Errorf("adjustScionUser(..., false) = (%d,%d,%v), want (1000,1000,false) — non-substrate runtimes must stay byte-identical", uid, gid, rootless)
		}
	})
}

// TestAdjustScionUser_DirectSetUIDRewroteNothing covers the second
// fail-closed case.
func TestAdjustScionUser_DirectSetUIDRewroteNothing(t *testing.T) {
	// An existing (but mismatched) scion user, so adjustScionUser proceeds
	// past the "already correct" shortcut into the rewrite attempt.
	withScionUserLookup(t, func(string) (*user.User, error) {
		return &user.User{Uid: "2000", Gid: "2000"}, nil
	})
	withRunDirectSetUID(t, func(string, string, string) error {
		return wrapPasswdEntryNotRewritten(errPasswdEntryNotRewritten)
	})
	t.Setenv("SCION_ALT_USERMOD", "1")

	t.Run("RequirePrivilegeDrop=true fails closed", func(t *testing.T) {
		uid, gid, rootless := adjustScionUser(1000, 1000, "1000", "1000", true)
		if uid != 0 || gid != 0 || rootless != false {
			t.Errorf("adjustScionUser(..., true) = (%d,%d,%v), want (0,0,false)", uid, gid, rootless)
		}
	})

	t.Run("RequirePrivilegeDrop=false is unchanged", func(t *testing.T) {
		uid, gid, rootless := adjustScionUser(1000, 1000, "1000", "1000", false)
		if uid != 1000 || gid != 1000 || rootless != false {
			t.Errorf("adjustScionUser(..., false) = (%d,%d,%v), want (1000,1000,false) — non-substrate runtimes must stay byte-identical", uid, gid, rootless)
		}
	})
}

// TestAdjustScionUser_DirectSetUIDOtherError_AlwaysFailsClosed proves the
// requirePrivilegeDrop gate only widens what fails closed; it does not
// narrow the pre-existing behaviour where ANY real directSetUID error
// (e.g. the sed command itself failing) already failed closed unconditionally
// for every runtime, before this change.
func TestAdjustScionUser_DirectSetUIDOtherError_AlwaysFailsClosed(t *testing.T) {
	withScionUserLookup(t, func(string) (*user.User, error) {
		return &user.User{Uid: "2000", Gid: "2000"}, nil
	})
	withRunDirectSetUID(t, func(string, string, string) error {
		return errors.New("sed /etc/group: exit status 1 (output: sed: -e expression #1, char 1: unknown option to `s')")
	})
	t.Setenv("SCION_ALT_USERMOD", "1")

	for _, requirePrivilegeDrop := range []bool{true, false} {
		uid, gid, rootless := adjustScionUser(1000, 1000, "1000", "1000", requirePrivilegeDrop)
		if uid != 0 || gid != 0 || rootless != false {
			t.Errorf("requirePrivilegeDrop=%v: adjustScionUser() = (%d,%d,%v), want (0,0,false) (a real command failure, pre-existing behaviour)", requirePrivilegeDrop, uid, gid, rootless)
		}
	}
}

// TestAdjustScionUser_PostAdjustVerifyMismatch covers the third fail-closed case.
func TestAdjustScionUser_PostAdjustVerifyMismatch(t *testing.T) {
	var calls int
	withScionUserLookup(t, func(string) (*user.User, error) {
		calls++
		if calls == 1 {
			// Pre-adjustment: mismatched, so we proceed to rewrite.
			return &user.User{Uid: "2000", Gid: "2000"}, nil
		}
		// Post-adjustment verify: still shows the OLD uid/gid — the sed/
		// usermod step silently did not take effect.
		return &user.User{Uid: "2000", Gid: "2000"}, nil
	})
	withRunDirectSetUID(t, func(string, string, string) error { return nil })
	t.Setenv("SCION_ALT_USERMOD", "1")

	t.Run("RequirePrivilegeDrop=true fails closed", func(t *testing.T) {
		calls = 0
		uid, gid, rootless := adjustScionUser(1000, 1000, "1000", "1000", true)
		if uid != 0 || gid != 0 || rootless != false {
			t.Errorf("adjustScionUser(..., true) = (%d,%d,%v), want (0,0,false)", uid, gid, rootless)
		}
	})

	t.Run("RequirePrivilegeDrop=false is unchanged", func(t *testing.T) {
		calls = 0
		uid, gid, rootless := adjustScionUser(1000, 1000, "1000", "1000", false)
		if uid != 1000 || gid != 1000 || rootless != false {
			t.Errorf("adjustScionUser(..., false) = (%d,%d,%v), want (1000,1000,false) — non-substrate runtimes must stay byte-identical", uid, gid, rootless)
		}
	})
}

// -----------------------------------------------------------------------
// directSetUID's "no entry to rewrite" detection.
// -----------------------------------------------------------------------

// TestDirectSetUIDAt_NoEntryToRewrite_ReturnsError proves a parity
// requirement: the historical (pre-substrate) directSetUID, run against a
// passwd file with no entry for username, still chowned the home
// directory — the sed simply matched nothing and exited 0. That side
// effect must happen exactly the same way now, with the pre-check only
// changing what directSetUIDAt *reports*, not what it *does*: home gets
// chowned either way, and only substrate's requirePrivilegeDrop=true
// caller treats the report as fatal (adjustScionUser, tested separately).
func TestDirectSetUIDAt_NoEntryToRewrite_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	groupPath := filepath.Join(dir, "group")
	passwdPath := filepath.Join(dir, "passwd")
	if err := os.WriteFile(groupPath, []byte("other:x:2000:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwdPath, []byte("other:x:2000:2000:Other User:/home/other:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	homeDir := filepath.Join(dir, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	chowns := recordDirectSetUIDAtChowns(t)

	// newUID/newGID are the test process's own uid/gid, not "1000": os.Chown
	// to a *different* uid/gid requires CAP_CHOWN, which this test process
	// doesn't have, but a self-chown still exercises the call this test is
	// checking for.
	self := strconv.Itoa(os.Getuid())
	selfGID := strconv.Itoa(os.Getgid())
	err := directSetUIDAt("scion", self, selfGID, groupPath, passwdPath, homeDir)
	if !errors.Is(err, errPasswdEntryNotRewritten) {
		t.Fatalf("directSetUIDAt() = %v, want an error wrapping errPasswdEntryNotRewritten", err)
	}

	// Confirm nothing was rewritten in the passwd/group files as a side
	// effect of detecting this (the sed's substitute matched nothing, as
	// intended).
	groupContent, _ := os.ReadFile(groupPath)
	if !strings.Contains(string(groupContent), "other:x:2000:") {
		t.Errorf("group file was modified despite no matching entry: %q", groupContent)
	}

	// But the home chown must still have happened — see the doc comment
	// above.
	assertHomeChowned(t, *chowns, homeDir)
}

// TestDirectSetUIDAt_PasswdEntryDisabledAccount_HomeStillChownedButReportsError
// covers a "scion:*:" entry: it exists, but is disabled (no leading "x" —
// see passwdEntryExists's own doc comment on why it anchors on
// "username:x:" and not just "username:"), so it still doesn't match the
// sed pattern either (which also anchors on ":x:"). This is the case
// passwdEntryExists's tightened prefix check made newly reachable, and
// pins that check against loosening back to a bare "username:" prefix.
// Home must still be chowned, exactly like the missing-entry case above.
func TestDirectSetUIDAt_PasswdEntryDisabledAccount_HomeStillChownedButReportsError(t *testing.T) {
	dir := t.TempDir()
	groupPath := filepath.Join(dir, "group")
	passwdPath := filepath.Join(dir, "passwd")
	if err := os.WriteFile(groupPath, []byte("scion:x:2000:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwdPath, []byte("scion:*:2000:2000:Scion (disabled):/home/scion:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	homeDir := filepath.Join(dir, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	chowns := recordDirectSetUIDAtChowns(t)

	self := strconv.Itoa(os.Getuid())
	selfGID := strconv.Itoa(os.Getgid())
	err := directSetUIDAt("scion", self, selfGID, groupPath, passwdPath, homeDir)
	if !errors.Is(err, errPasswdEntryNotRewritten) {
		t.Fatalf("directSetUIDAt() = %v, want an error wrapping errPasswdEntryNotRewritten (a \"scion:*:\" line is not a rewritable entry)", err)
	}

	passwdContent, _ := os.ReadFile(passwdPath)
	if !strings.Contains(string(passwdContent), "scion:*:2000:2000:") {
		t.Errorf("passwd file was modified despite no \":x:\" entry to match: %q", passwdContent)
	}

	assertHomeChowned(t, *chowns, homeDir)
}

// recordDirectSetUIDAtChowns overrides directSetUIDAtChown for the rest of
// the test and returns the paths it's called with, in order — the portable,
// deterministic replacement for a filesystem-ctime-based chown detector
// (see directSetUIDAtChown's own doc comment for why ctime doesn't work
// here). The recorded fake still calls through to the real os.Chown, so
// these tests keep exercising the real syscall, not just the recording.
func recordDirectSetUIDAtChowns(t *testing.T) *[]string {
	t.Helper()
	orig := directSetUIDAtChown
	var calls []string
	directSetUIDAtChown = func(path string, uid, gid int) error {
		calls = append(calls, path)
		return orig(path, uid, gid)
	}
	t.Cleanup(func() { directSetUIDAtChown = orig })
	return &calls
}

// assertHomeChowned fails the test unless calls (as recorded by
// recordDirectSetUIDAtChowns) includes homeDir.
func assertHomeChowned(t *testing.T, calls []string, homeDir string) {
	t.Helper()
	for _, c := range calls {
		if c == homeDir {
			return
		}
	}
	t.Errorf("directSetUIDAtChown was never called with %s — the home chown must run even when there's no passwd entry to rewrite (calls: %v)", homeDir, calls)
}

func TestDirectSetUIDAt_RewritesExistingEntry(t *testing.T) {
	dir := t.TempDir()
	groupPath := filepath.Join(dir, "group")
	passwdPath := filepath.Join(dir, "passwd")
	if err := os.WriteFile(groupPath, []byte("scion:x:2000:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwdPath, []byte("scion:x:2000:2000:Scion:/home/scion:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	homeDir := filepath.Join(dir, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := directSetUIDAt("scion", "1000", "1000", groupPath, passwdPath, homeDir); err != nil {
		t.Fatalf("directSetUIDAt() = %v, want nil", err)
	}

	groupContent, err := os.ReadFile(groupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(groupContent), "scion:x:1000:") {
		t.Errorf("group file = %q, want it rewritten to GID 1000", groupContent)
	}
	passwdContent, err := os.ReadFile(passwdPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(passwdContent), "scion:x:1000:1000:") {
		t.Errorf("passwd file = %q, want it rewritten to UID/GID 1000", passwdContent)
	}
}

// TestDirectSetUIDAt_PasswdEntryNoMatchingGroupLine_GroupIsBestEffort proves
// a passwd entry that exists but whose primary group isn't literally named
// "scion" (e.g. `useradd -g users scion`) is a legitimate, real-world case
// — the group sed matching nothing must not fail the whole operation, and
// the passwd rewrite (the one that actually matters) must still succeed.
// This is the historical non-substrate behaviour, preserved exactly: only a
// real command failure or a missing *passwd* entry is an error.
func TestDirectSetUIDAt_PasswdEntryNoMatchingGroupLine_GroupIsBestEffort(t *testing.T) {
	dir := t.TempDir()
	groupPath := filepath.Join(dir, "group")
	passwdPath := filepath.Join(dir, "passwd")
	// No "scion:" line in the group file at all — a user whose primary
	// group has a different name.
	if err := os.WriteFile(groupPath, []byte("users:x:100:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwdPath, []byte("scion:x:2000:100:Scion:/home/scion:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	homeDir := filepath.Join(dir, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := directSetUIDAt("scion", "1000", "1000", groupPath, passwdPath, homeDir); err != nil {
		t.Fatalf("directSetUIDAt() = %v, want nil — a group sed matching nothing must not be an error", err)
	}

	groupContent, err := os.ReadFile(groupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(groupContent), "users:x:100:") {
		t.Errorf("group file = %q, want unchanged (no scion group entry to rewrite)", groupContent)
	}

	passwdContent, err := os.ReadFile(passwdPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(passwdContent), "scion:x:1000:1000:") {
		t.Errorf("passwd file = %q, want it rewritten to UID/GID 1000 despite the group not matching", passwdContent)
	}
}

// wrapPasswdEntryNotRewritten wraps err the way a real call site would
// (directSetUIDAt wraps errPasswdEntryNotRewritten with the path via
// fmt.Errorf("%w", ...)), so errors.Is still matches after runDirectSetUID
// is faked.
func wrapPasswdEntryNotRewritten(err error) error {
	return fmt.Errorf("/etc/group: %w", err)
}

// TestRunInit_Enforced_RootGIDFromSetupHostUser_FailsClosed pins RunInit's
// only production call to requirePrivilegeDropOrFail (in RunInit itself): it
// must forward setupHostUser's real targetUID and targetGID, unchanged, not
// a swapped argument or a hardcoded value. Each pair below fails the gate's
// UID>0 && GID>0 predicate for a different reason (a root GID, or a negative
// UID/GID), so a call site that substitutes the wrong value, or a predicate
// that only checks for a zero UID/GID instead of a non-positive one, lets at
// least one of them through.
func TestRunInit_Enforced_RootGIDFromSetupHostUser_FailsClosed(t *testing.T) {
	for _, pair := range [][2]int{{1000, 0}, {1000, -1}, {-1, 1000}} {
		scrubHubEnv(t)
		t.Setenv("HOME", t.TempDir())
		orig := runSetupHostUser
		uid, gid := pair[0], pair[1]
		runSetupHostUser = func(bool) (int, int, bool) { return uid, gid, false }
		t.Cleanup(func() { runSetupHostUser = orig })
		got := RunInit([]string{"true"}, InitRunOptions{ForwardTermSignal: false, RequirePrivilegeDrop: true})
		if got != exitCodePrivilegeDropRequired {
			t.Errorf("setupHostUser=(%d,%d): RunInit() = %d, want exitCodePrivilegeDropRequired (%d)", uid, gid, got, exitCodePrivilegeDropRequired)
		}
	}
}

// TestRunInit_Enforced_RootlessFromSetupHostUser_FailsClosedBeforeServices
// pins that RunInit's enforced gate does not exempt setupHostUser's rootless
// result: setupHostUser returns (0, 0, true) both for a root process
// without CAP_SETUID and for an unmapped target UID, and in both cases PID 1
// is still real root. The gate must refuse (exit
// exitCodePrivilegeDropRequired) before git clone or services start ever
// run: those two downstream steps receive the same targetUID/targetGID the
// gate itself saw, and the supervisor's own independent refusal one layer
// down only protects the harness child, not git clone or the sidecar
// services started before it.
func TestRunInit_Enforced_RootlessFromSetupHostUser_FailsClosedBeforeServices(t *testing.T) {
	agentHome := t.TempDir()
	setupRunInitAsRootlessScion(t, agentHome)
	writeServicesYAMLWithInvalidEntry(t, agentHome)

	orig := runSetupHostUser
	runSetupHostUser = func(bool) (int, int, bool) { return 0, 0, true }
	t.Cleanup(func() { runSetupHostUser = orig })

	cloneReached, servicesReached := false, false
	withRunGitCloneWorkspace(t, func(int, int, string) error { cloneReached = true; return nil })
	withRunServicesStart(t, func(_ context.Context, _ *services.Manager, _ []api.ServiceSpec, uid, gid int, _ string, _ bool) error {
		servicesReached = true
		t.Logf("services started with uid=%d gid=%d in enforced mode", uid, gid)
		return nil
	})

	got := RunInit([]string{"true"}, InitRunOptions{ForwardTermSignal: false, RequirePrivilegeDrop: true})
	if got != exitCodePrivilegeDropRequired {
		t.Errorf("setupHostUser=(0,0,rootless=true): RunInit() = %d, want exitCodePrivilegeDropRequired (%d)", got, exitCodePrivilegeDropRequired)
	}
	if cloneReached || servicesReached {
		t.Errorf("enforced + rootless root: gitClone reached=%v, servicesStart reached=%v; want neither (they would run as uid 0)", cloneReached, servicesReached)
	}
}

// TestSeamDefaultsAreRealFunctions pins that every test-only seam RunInit's
// setup path exposes (see runAdjustScionUser, runChownTreeRootOwned,
// setupHostUserHasCapSetUID, setupHostUserIsUIDMapped, setupHostUserGetuid,
// postPreStartGeteuid, runSetupHostUser, and runDirectSetUID's doc comments)
// still defaults to the real function it wraps. A default that silently
// became a stub would skip the checks or side effects those functions
// perform in production, while every test — which only ever reassigns the
// var for the duration of its own run, never inspects its starting value —
// would keep passing.
func TestSeamDefaultsAreRealFunctions(t *testing.T) {
	ptr := func(f any) uintptr { return reflect.ValueOf(f).Pointer() }
	for name, pair := range map[string][2]any{
		"runAdjustScionUser":        {runAdjustScionUser, adjustScionUser},
		"runChownTreeRootOwned":     {runChownTreeRootOwned, chownTreeRootOwned},
		"setupHostUserHasCapSetUID": {setupHostUserHasCapSetUID, hasCapSetUID},
		"setupHostUserIsUIDMapped":  {setupHostUserIsUIDMapped, isUIDMapped},
		"setupHostUserGetuid":       {setupHostUserGetuid, os.Getuid},
		"postPreStartGeteuid":       {postPreStartGeteuid, os.Geteuid},
		"runSetupHostUser":          {runSetupHostUser, setupHostUser},
		"runDirectSetUID":           {runDirectSetUID, directSetUID},
	} {
		if ptr(pair[0]) != ptr(pair[1]) {
			t.Errorf("%s default is not the real function", name)
		}
	}
}
