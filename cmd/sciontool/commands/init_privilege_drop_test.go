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
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/services"
	"github.com/GoogleCloudPlatform/scion/pkg/stagedsecrets"
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

// TestParseCapBit covers parseCapBit generically (the function hasCapBit,
// substrate_privilege_drop.go, uses to check any capability in
// substratecaps.Required, not just
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
	if !opts.DisablePortForwarding {
		t.Error("substrateServeInitOptions(...).DisablePortForwarding = false, want true — Substrate's egress cannot reach the hub port-forward tunnel")
	}
	if opts.PrivilegeDropPrecheck == nil {
		t.Error("substrateServeInitOptions(...).PrivilegeDropPrecheck = nil, want substrateServePrivilegeDropChecker")
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

// TestRunInit_Enforced_NilPrecheck_StillFailsClosed proves the fail-closed
// requirement directly: InitRunOptions.PrivilegeDropPrecheck left at its
// zero value (nil) — the default for every caller except substrate-serve —
// must not weaken RunInit's existing enforced-mode fail-closed behaviour.
// requirePrivilegeDropOrFail (unchanged by this seam) is what actually
// catches the failed drop here; this test pins that adding the nil-tolerant
// precheck call site ahead of it did not accidentally short-circuit that
// existing check.
func TestRunInit_Enforced_NilPrecheck_StillFailsClosed(t *testing.T) {
	scrubHubEnv(t)
	t.Setenv("HOME", t.TempDir())

	orig := runSetupHostUser
	// A failed drop (still root): the one outcome requirePrivilegeDropOrFail
	// must catch regardless of PrivilegeDropPrecheck.
	runSetupHostUser = func(bool) (int, int, bool) { return 0, 0, false }
	t.Cleanup(func() { runSetupHostUser = orig })

	got := RunInit([]string{"true"}, InitRunOptions{
		ForwardTermSignal:     false,
		RequirePrivilegeDrop:  true,
		PrivilegeDropPrecheck: nil,
	})
	if got != exitCodePrivilegeDropRequired {
		t.Fatalf("RunInit() = %d, want exitCodePrivilegeDropRequired (%d) — a nil PrivilegeDropPrecheck must not bypass requirePrivilegeDropOrFail's own fail-closed check", got, exitCodePrivilegeDropRequired)
	}
}

// TestRunInit_Enforced_PrecheckInvokedWhenPresent proves the other half of
// the same contract: when InitRunOptions.PrivilegeDropPrecheck is set,
// RunInit actually calls it and honors an error from it — even when
// setupHostUser's own result (targetUID/targetGID) would otherwise pass
// requirePrivilegeDropOrFail cleanly. If RunInit ever stopped calling the
// precheck, this test would see RunInit proceed past the privilege-drop
// gate (exit 0, or a later failure) instead of failing closed here, and
// "called" would stay false.
func TestRunInit_Enforced_PrecheckInvokedWhenPresent(t *testing.T) {
	scrubHubEnv(t)
	t.Setenv("HOME", t.TempDir())

	orig := runSetupHostUser
	// A drop that succeeded on its own terms: requirePrivilegeDropOrFail
	// alone would let RunInit through.
	runSetupHostUser = func(bool) (int, int, bool) { return 1000, 1000, false }
	t.Cleanup(func() { runSetupHostUser = orig })

	var called bool
	precheckErr := errors.New("precheck: fake feasibility failure")
	got := RunInit([]string{"true"}, InitRunOptions{
		ForwardTermSignal:    false,
		RequirePrivilegeDrop: true,
		PrivilegeDropPrecheck: func() error {
			called = true
			return precheckErr
		},
	})
	if !called {
		t.Fatal("RunInit did not call InitRunOptions.PrivilegeDropPrecheck")
	}
	if got != exitCodePrivilegeDropRequired {
		t.Fatalf("RunInit() = %d, want exitCodePrivilegeDropRequired (%d) when PrivilegeDropPrecheck returns an error despite a successful setupHostUser drop", got, exitCodePrivilegeDropRequired)
	}
}

// TestRunInit_PrecheckNotCalledWhenPrivilegeDropNotRequired proves the hook
// is gated on RequirePrivilegeDrop, the same as requirePrivilegeDropOrFail's
// own check: no caller other than substrate-serve sets RequirePrivilegeDrop
// today, but a hypothetical caller that leaves it false must not have its
// precheck invoked either. Uses the same deterministic early-failure trick
// as TestRunInit_StagedSecretsDecodeFailure_ReportsInitFailure below so this
// stays fast and independent of the real setupHostUser environment.
func TestRunInit_PrecheckNotCalledWhenPrivilegeDropNotRequired(t *testing.T) {
	scrubHubEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(stagedsecrets.EnvVar, "not valid base64 or json!!!")

	var called bool
	RunInit([]string{"true"}, InitRunOptions{
		ForwardTermSignal: false,
		PrivilegeDropPrecheck: func() error {
			called = true
			return nil
		},
	})
	if called {
		t.Error("RunInit called PrivilegeDropPrecheck even though RequirePrivilegeDrop was false")
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
