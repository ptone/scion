/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"errors"
	"os"
	"os/user"
	"testing"
)

// errScionUserLookupDisabledInTests is what scionUserLookup/lookupUserByID
// return by default for the lifetime of this test binary (see TestMain).
var errScionUserLookupDisabledInTests = errors.New("scionUserLookup/lookupUserByID: real user lookups are disabled by TestMain; a test that needs a resolved user must override the var itself (scoped with t.Cleanup)")

// TestMain makes this package's tests hermetic against the *real* machine
// they happen to run on, for the whole test binary — not just the tests
// that remember to sandbox themselves.
//
// Incident (round-13 hermeticity follow-up): a test that drove the real
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
// Two independent layers, both required:
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

	os.Exit(m.Run())
}
