/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
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

func TestHasCapSetGID_ParsesEffectiveCapabilities(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "CAP_SETGID present among limited caps",
			input: "Name:\tinit\nCapEff:\t00000000000000ff\n",
			want:  true,
		},
		{
			name:  "CAP_SETGID absent (only SETUID bit set)",
			input: "Name:\tinit\nCapEff:\t0000000000000080\n",
			want:  false,
		},
		{
			name:  "only CAP_SETGID bit set",
			input: "CapEff:\t0000000000000040\n",
			want:  true,
		},
		{
			name:  "no capabilities",
			input: "CapEff:\t0000000000000000\n",
			want:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCapSetGID(tc.input); got != tc.want {
				t.Errorf("parseCapSetGID(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// fakePrivilegeDropDeps builds privilegeDropPreconditionDeps with every
// dependency controllable, defaulting to a fully-feasible environment so
// each test case only needs to override the one thing it's testing.
func fakePrivilegeDropDeps(t *testing.T) privilegeDropPreconditionDeps {
	t.Helper()
	env := map[string]string{"SCION_HOST_UID": "1000", "SCION_HOST_GID": "1000"}
	return privilegeDropPreconditionDeps{
		hasCapSetUID: func() bool { return true },
		hasCapSetGID: func() bool { return true },
		lookupUser:   func(string) (*user.User, error) { return &user.User{Username: "scion", Uid: "1000", Gid: "1000"}, nil },
		getenv:       func(k string) string { return env[k] },
	}
}

func TestCheckPrivilegeDropFeasible_AllPresent_Passes(t *testing.T) {
	if err := checkPrivilegeDropFeasible(fakePrivilegeDropDeps(t)); err != nil {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want nil", err)
	}
}

func TestCheckPrivilegeDropFeasible_MissingCapSetUID_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	d.hasCapSetUID = func() bool { return false }
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition", err)
	}
}

func TestCheckPrivilegeDropFeasible_MissingCapSetGID_Fails(t *testing.T) {
	d := fakePrivilegeDropDeps(t)
	d.hasCapSetGID = func() bool { return false }
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition", err)
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
// substrate-serve's wiring (extracted so a mutation is caught by a test).
// -----------------------------------------------------------------------

// TestSubstrateServeInitOptions_RequiresPrivilegeDrop asserts that
// substrate-serve's InitRunner passes RequirePrivilegeDrop: true to RunInit.
// Mutation check performed: flipping this function's literal to
// RequirePrivilegeDrop: false makes this test fail (confirmed locally by
// editing substrate_serve.go and re-running `go test -run
// TestSubstrateServeInitOptions_RequiresPrivilegeDrop`; reverted after).
func TestSubstrateServeInitOptions_RequiresPrivilegeDrop(t *testing.T) {
	opts := substrateServeInitOptions(true)
	if !opts.RequirePrivilegeDrop {
		t.Error("substrateServeInitOptions(...).RequirePrivilegeDrop = false, want true — substrate must never start the harness as root")
	}
	if !opts.ForwardTermSignal {
		t.Error("substrateServeInitOptions(true).ForwardTermSignal = false, want true (passthrough)")
	}
	opts2 := substrateServeInitOptions(false)
	if opts2.ForwardTermSignal {
		t.Error("substrateServeInitOptions(false).ForwardTermSignal = true, want false (passthrough)")
	}
}

func TestExitOnPrivilegeDropSentinel_SentinelExits(t *testing.T) {
	var gotCode int
	var called bool
	exitOnPrivilegeDropSentinel(exitCodePrivilegeDropRequired, func(code int) {
		called = true
		gotCode = code
	})
	if !called {
		t.Fatal("exit was not called for exitCodePrivilegeDropRequired")
	}
	if gotCode != exitCodePrivilegeDropRequired {
		t.Errorf("exit called with %d, want %d", gotCode, exitCodePrivilegeDropRequired)
	}
}

// TestExitOnPrivilegeDropSentinel_OtherCodesDoNotExit is the counterpart
// proving this is NOT "any non-zero code" — see
// exitCodePrivilegeDropRequired's doc comment for why 0 (success) and any
// other code (the harness's own exit code, once one actually launched) must
// never trigger os.Exit here.
func TestExitOnPrivilegeDropSentinel_OtherCodesDoNotExit(t *testing.T) {
	for _, code := range []int{0, 1, 2, 137} {
		exitOnPrivilegeDropSentinel(code, func(int) {
			t.Errorf("exit was called for code %d, want no call", code)
		})
	}
}

// TestNewSubstrateServeServer_PrivilegeDropPreconditionRejectsBootstrap
// drives the exact server construction runSubstrateServe uses (real
// PrivilegeDropChecker, real InitRunner) and proves the wiring itself: with
// SCION_HOST_UID/GID absent from the process environment, the deterministic
// branch of checkPrivilegeDropFeasible, the bootstrap must be rejected
// without ever invoking RunInit. If WithPrivilegeDropChecker were ever
// dropped from newSubstrateServeServer, this test would instead see 200 and
// fail — this is the "disabling the precondition must fail a test" mutation
// check.
func TestNewSubstrateServeServer_PrivilegeDropPreconditionRejectsBootstrap(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	t.Setenv("SCION_HOST_GID", "")
	scrubHubEnv(t)

	srv := newSubstrateServeServer()
	rec := doSubstrateServeJSON(t, srv, "POST", "/scion/v1/bootstrap", "any-token", map[string]any{
		"env":           map[string]string{},
		"files":         []any{},
		"start_cmd":     "true",
		"control_token": "tok",
	})

	if rec.Code == 200 || rec.Code < 400 {
		t.Fatalf("status = %d, want a non-2xx rejection (SCION_HOST_UID/GID are unset)", rec.Code)
	}
}

// -----------------------------------------------------------------------
// RunInit's defence-in-depth failure reporting (real integration —
// drives the actual RunInit, not a stub).
// -----------------------------------------------------------------------

// TestReportPrivilegeDropFailure_WritesPhaseErrorAndMessage proves the
// defence-in-depth reporting requirement: on a
// requirePrivilegeDropOrFail failure, RunInit must report PhaseError to
// local agent-info state (the same way the git-clone failure path does),
// not just log it. Driven directly against a temp directory rather than
// through RunInit's real setupHostUser/resolveAgentHome, which resolve
// against whatever "scion" user (if any) actually exists on the machine
// running the test.
func TestReportPrivilegeDropFailure_WritesPhaseErrorAndMessage(t *testing.T) {
	scrubHubEnv(t)
	tmpHome := t.TempDir()

	reportPrivilegeDropFailure(tmpHome, errPrivilegeDropRequired)

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
// particular machine (see TestReportPrivilegeDropFailure_* for that part).
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
// (uid, gid, false) it always has, since the brief's rule is not to change
// other runtimes' behaviour.
func TestAdjustScionUser_ScionUserNotFound(t *testing.T) {
	withScionUserLookup(t, func(string) (*user.User, error) {
		return nil, errors.New("user: unknown user scion")
	})
	withRunDirectSetUID(t, func(string, string, string) error { return nil })
	// Force the direct-edit branch (skip a real usermod/groupmod exec call).
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

	err := directSetUIDAt("scion", "1000", "1000", groupPath, passwdPath, filepath.Join(dir, "home"))
	if !errors.Is(err, errPasswdEntryNotRewritten) {
		t.Fatalf("directSetUIDAt() = %v, want an error wrapping errPasswdEntryNotRewritten", err)
	}

	// Confirm nothing was rewritten as a side effect of detecting this.
	groupContent, _ := os.ReadFile(groupPath)
	if !strings.Contains(string(groupContent), "other:x:2000:") {
		t.Errorf("group file was modified despite no matching entry: %q", groupContent)
	}
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

// wrapPasswdEntryNotRewritten wraps err the way a real call site would
// (directSetUIDAt wraps errPasswdEntryNotRewritten with the path via
// fmt.Errorf("%w", ...)), so errors.Is still matches after runDirectSetUID
// is faked.
func wrapPasswdEntryNotRewritten(err error) error {
	return fmt.Errorf("/etc/group: %w", err)
}
