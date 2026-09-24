/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

// errScionUserLookupDisabledInTests is what scionUserLookup/lookupUserByID
// return by default for the lifetime of this test binary (see TestMain).
var errScionUserLookupDisabledInTests = errors.New("scionUserLookup/lookupUserByID: real user lookups are disabled by TestMain; a test that needs a resolved user must override the var itself (scoped with t.Cleanup)")

// TestMain makes this package's tests hermetic against the *real* machine
// they happen to run on, for the whole test binary — not just the tests
// that remember to sandbox themselves.
//
// Incident 1 (round-13 hermeticity follow-up): a test that drove the real
// RunInit wrote agent-info.json with phase "error" to this container's own,
// real /home/scion — because this dev/test environment's actual system user
// is named "scion", so setupHostUser's rootless shortcut and
// resolveAgentHome's fallback both resolved a genuine user.Lookup("scion")
// to the real account, regardless of what $HOME a single test had set with
// t.Setenv. Some component outside this test process (the real agent
// supervision for this container) reads that file and forwarded the
// contamination to the real Hub, which then rejected this agent's own
// inbound messages for about 35 minutes. A per-test t.Setenv cannot fix
// this: the hazard is a real syscall-backed lookup, not an environment
// variable.
//
// Incident 2: TestNewSubstrateServeServer_PrivilegeDropPreconditionRejectsBootstrap
// drove the real newSubstrateServeServer() wiring (real RunInit, real
// os.Exit) and set SCION_HOST_UID/GID but never set HOME. Under the mutation
// that removes WithPrivilegeDropChecker (exactly the regression this test
// exists to catch), bootstrap wrongly returns 200, the real RunInit
// goroutine runs for real, requirePrivilegeDropOrFail fails, and
// reportInitFailure resolves agentHome via resolveAgentHome's
// os.Getenv("HOME") fallback — the real, ambient $HOME of whoever's machine
// runs this test, not a temp directory, because neither the test nor
// TestMain (at the time) redirected it. This happened on a *different*
// agent's container (the round-14 reviewer's), not just this one: it wrote
// that container's real agent-info.json and then called the real
// exitOnNonZeroInit, which calls the real os.Exit and kills the test binary
// itself. "No test can touch the real account or hub even on a regression"
// did not hold merely by clearing env vars and disabling user lookups; a
// still-real $HOME is enough on its own to reach a real file.
//
// Layers, all required:
//
//  1. Every SCION_HUB*/token/agent-identity env var, plus SCION_HOST_UID/GID
//     and SCION_KEEPID_UID, is cleared for the entire process before any
//     test runs. hub.NewClient() already refuses a non-localhost hub under
//     `go test` (see its own doc comment), but that guard depends on
//     testing.Testing() and a hubURL read from the environment; clearing
//     the env here removes the *input* to that decision entirely, for
//     every test in this package, not just ones that remember to call
//     scrubHubEnv.
//  2. scionUserLookup and lookupUserByID (the two package vars every
//     "scion"/by-UID lookup in this file goes through — see their own doc
//     comments) default to "not found" for the whole test binary. A test
//     that needs a *resolved* fake user (e.g. adjustScionUser's tests)
//     overrides the var itself, scoped with t.Cleanup so it reverts to this
//     safe default afterward — it never falls through to a real syscall.
//  3. HOME, the XDG base-directory variables, and SCION_WORKSPACE_PATH are
//     all redirected to one per-binary temp directory before any test
//     runs, and removed afterward. These are every remaining input this
//     package's code uses to derive a real filesystem path when nothing
//     more specific (targetUID, an explicit agentHome parameter) is
//     available — resolveAgentHome's os.Getenv("HOME") fallback,
//     hooks.NewLifecycleManager's default hooks dir, gitCloneWorkspace's
//     SCION_WORKSPACE_PATH default ("/workspace" — a real, precious path in
//     any dev container this runs in). A test's own t.Setenv("HOME", ...)
//     is necessary but not sufficient on its own (incident 2): it protects
//     only that one test, not a regression that reaches this fallback from
//     a code path the test author didn't anticipate.
func TestMain(m *testing.M) {
	envVarsToClear := append(append([]string{}, hubEnvVars...),
		"SCION_HOST_UID", "SCION_HOST_GID", "SCION_KEEPID_UID")
	for _, v := range envVarsToClear {
		_ = os.Unsetenv(v)
	}

	scionUserLookup = func(string) (*user.User, error) {
		return nil, errScionUserLookupDisabledInTests
	}
	lookupUserByID = func(string) (*user.User, error) {
		return nil, errScionUserLookupDisabledInTests
	}

	tmpHome, err := os.MkdirTemp("", "sciontool-test-home-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: failed to create sandbox home: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("HOME", tmpHome)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	_ = os.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, ".local", "share"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(tmpHome, ".cache"))
	_ = os.Setenv("XDG_STATE_HOME", filepath.Join(tmpHome, ".local", "state"))
	_ = os.Setenv("SCION_WORKSPACE_PATH", filepath.Join(tmpHome, "workspace"))

	code := m.Run()
	_ = os.RemoveAll(tmpHome)
	os.Exit(code)
}
