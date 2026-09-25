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

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

// errScionUserLookupDisabledInTests is what scionUserLookup/lookupUserByID
// return by default for the lifetime of this test binary (see TestMain).
var errScionUserLookupDisabledInTests = errors.New("scionUserLookup/lookupUserByID: real user lookups are disabled by TestMain; a test that needs a resolved user must override the var itself (scoped with t.Cleanup)")

// TestMain makes this package's tests hermetic against the *real* machine
// they happen to run on, for the whole test binary — not just the tests
// that remember to sandbox themselves. See
// .design/project-log/2026-09-25-substrate-phase1-substrate-serve.md
// ("Privilege drop" and "Test hermeticity") for what motivated each layer
// below.
//
// Layers, all required:
//
//  1. Every SCION_HUB*/token/agent-identity env var, plus SCION_HOST_UID/GID
//     and SCION_KEEPID_UID, is cleared for the entire process: removes the
//     *input* hub.NewClient()'s own testing.Testing() guard depends on, for
//     every test here, not just ones that remember to call scrubHubEnv.
//  2. scionUserLookup and lookupUserByID default to "not found" for the
//     whole test binary: no lookup here can resolve a real account, even
//     when a test overrides the var back to a fake one (scoped with
//     t.Cleanup). defaultScionUserLookup/defaultLookupUserByID's own
//     testing.Testing() gate (init.go) is this layer's independent
//     backstop, not a replacement for it.
//  3. HOME, the XDG base-directory variables, and SCION_WORKSPACE_PATH are
//     redirected to one per-binary temp directory: every remaining input
//     this package's code uses to derive a real filesystem path when
//     nothing more specific is available.
//  4. startReaper is stubbed to a no-op for the whole test binary: a test
//     driving RunInit must never install the process-wide zombie reaper
//     that steals another test's exec.Command child.
//  5. log.SetLogPath redirects pkg/sciontool/log's own file target to the
//     same per-binary temp directory, before any log call in this binary
//     can lazily Init() itself against the real path.
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
	startReaper = func() {}

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
	log.SetLogPath(filepath.Join(tmpHome, "agent.log"))

	// hub.ReadTokenFile resolves its own home directory independently of
	// $HOME: resolveTokenHome (pkg/sciontool/hub) prefers a real
	// user.Lookup("scion") result over $HOME, so on a machine where
	// "scion" is a real account, redirecting $HOME above does not stop it
	// from reading — or, if a test ever called WriteTokenFile, writing —
	// the real ~/.scion/scion-token. SetTokenHome overrides that resolver
	// directly.
	restoreTokenHome := hub.SetTokenHome(tmpHome)

	code := m.Run()
	restoreTokenHome()
	_ = os.RemoveAll(tmpHome)
	os.Exit(code)
}
