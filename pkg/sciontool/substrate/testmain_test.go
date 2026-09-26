// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package substrate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

// sandboxHomeDir is the throwaway directory TestMain points HOME and the
// XDG base directories at, for the lifetime of this test binary. Tests that
// need to check what did or didn't get written under it (see
// TestGoCachesEscapeSandboxHOME) read this instead of recomputing it.
var sandboxHomeDir string

// resolveRealGoCaches finds this machine's real GOCACHE, GOMODCACHE and
// GOPATH before TestMain redirects HOME and XDG_CACHE_HOME to a throwaway
// directory, and exports them explicitly so any child `go` process this
// test binary launches keeps writing to the real caches instead of
// deriving a fresh module cache and build cache under the throwaway
// directory. Go's module cache is written read-only, so a child `go`
// process that populated one there would survive TestMain's own cleanup.
// Each variable is taken from the environment if already set; otherwise
// this runs `go env` once for whichever ones are still missing. If that
// fails, it reports the failure to stderr and leaves the missing variables
// unset, so a child `go` process falls back to deriving its caches from the
// about-to-be-sandboxed HOME/XDG_CACHE_HOME — the pre-existing behaviour —
// rather than aborting the test binary.
func resolveRealGoCaches() {
	names := []string{"GOCACHE", "GOMODCACHE", "GOPATH"}
	var missing []string
	for _, name := range names {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	out, err := exec.Command("go", append([]string{"env"}, missing...)...).Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: failed to resolve real %s via `go env`: %v; child go processes may derive caches from the sandboxed HOME instead\n", strings.Join(missing, ", "), err)
		return
	}
	values := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(values) != len(missing) {
		fmt.Fprintf(os.Stderr, "TestMain: unexpected `go env %s` output (%d line(s), want %d); child go processes may derive caches from the sandboxed HOME instead\n", strings.Join(missing, " "), len(values), len(missing))
		return
	}
	for i, name := range missing {
		if values[i] == "" {
			continue
		}
		_ = os.Setenv(name, values[i])
	}
}

// removeSandboxHome makes every file and directory under dir writable by
// its owner, then removes the tree. A real module cache directory (which
// resolveRealGoCaches exists to prevent from ever being written here, but
// this is the backstop) contains read-only files and directories by
// design, so a plain os.RemoveAll can silently leave them behind. Any
// error still remaining after the chmod pass is reported instead of
// ignored.
func removeSandboxHome(dir string) {
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // best effort; the RemoveAll below reports what's left.
		}
		if info.Mode()&0o200 == 0 {
			_ = os.Chmod(path, info.Mode()|0o200)
		}
		return nil
	})
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: failed to remove sandbox home %s: %v\n", dir, err)
	}
}

// TestMain clears every Hub- and privilege-drop-related environment
// variable, and redirects HOME/XDG_*/SCION_WORKSPACE_PATH/the Hub token
// home/pkg/sciontool/log's own log file to one per-binary temp directory,
// before any test in this package runs — see cmd/sciontool/commands's
// TestMain (testmain_test.go) for the incidents this defends against. This
// package's own production code doesn't import pkg/sciontool/hub or
// resolve a fixed real home directory itself (every test here drives
// WithInitRunner/WithPrivilegeDropChecker with local fakes, never the real
// RunInit — see cmd/sciontool/commands's newSubstrateServeServer for where
// the real ones are wired instead), so the blast radius here is smaller.
// But handleBootstrap does read SCION_HOST_UID/GID out of the real process
// environment (set there by a test's own req.Env, via os.Setenv, exactly
// like a real bootstrap request), so a test that forgets to reset them
// could otherwise leak a previous test's values into a later one; server.go
// does call pkg/sciontool/log directly, which would otherwise lazily
// default to this container's real /home/scion/agent.log the same way
// cmd/sciontool/commands's incident 4 did; and the HOME/XDG/workspace
// redirection is cheap insurance against any future test in this package
// that does end up resolving a real path. resolveRealGoCaches runs before
// that redirection, so any future test that shells out to `go` keeps using
// the real GOCACHE/GOMODCACHE/GOPATH instead of writing fresh ones under
// the throwaway sandbox home.
func TestMain(m *testing.M) {
	for _, v := range []string{
		"SCION_HUB_ENDPOINT", "SCION_HUB_URL", "SCION_AUTH_TOKEN",
		"SCION_AGENT_ID", "SCION_AGENT_MODE",
		"SCION_HOST_UID", "SCION_HOST_GID", "SCION_KEEPID_UID",
	} {
		_ = os.Unsetenv(v)
	}

	resolveRealGoCaches()

	tmpHome, err := os.MkdirTemp("", "sciontool-substrate-test-home-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: failed to create sandbox home: %v\n", err)
		os.Exit(1)
	}
	sandboxHomeDir = tmpHome
	_ = os.Setenv("HOME", tmpHome)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	_ = os.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, ".local", "share"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(tmpHome, ".cache"))
	_ = os.Setenv("XDG_STATE_HOME", filepath.Join(tmpHome, ".local", "state"))
	_ = os.Setenv("SCION_WORKSPACE_PATH", filepath.Join(tmpHome, "workspace"))
	log.SetLogPath(filepath.Join(tmpHome, "agent.log"))
	restoreTokenHome := hub.SetTokenHome(tmpHome)

	code := m.Run()
	restoreTokenHome()
	removeSandboxHome(tmpHome)
	os.Exit(code)
}

// TestGoCachesEscapeSandboxHOME proves resolveRealGoCaches works: a child
// `go` process launched under this test binary's own sandboxed
// HOME/XDG_CACHE_HOME still reports the real machine's GOCACHE and
// GOMODCACHE, not paths under the throwaway sandbox home TestMain created
// and will remove. It also checks the sandbox home directly, so a cache
// that got written there by some other path (not just this test's own `go
// env` call) is still caught.
func TestGoCachesEscapeSandboxHOME(t *testing.T) {
	if sandboxHomeDir == "" {
		t.Fatal("sandboxHomeDir was not set by TestMain")
	}

	// The child `go env` call below must not write into sandboxHomeDir
	// itself: go's own telemetry counters live under
	// $XDG_CONFIG_HOME/go/telemetry regardless of GOCACHE/GOMODCACHE (go
	// treats GOTELEMETRY/GOTELEMETRYDIR as read-only, derived values, not
	// settable env vars), and that write can outlive this call — go's
	// telemetry uploader can run detached in the background. Pointing only
	// XDG_CONFIG_HOME at this test's own t.TempDir() keeps that unrelated
	// write out of the directory this test (and TestMain's cleanup)
	// actually cares about, and lets the testing package's own
	// unconditional cleanup remove it. HOME must stay exactly what TestMain
	// set it to (sandboxHomeDir): GOMODCACHE/GOPATH default to paths under
	// $HOME, so overriding HOME here would make this test check a directory
	// that resolveRealGoCaches never had a chance to protect.
	childConfigHome := t.TempDir()
	cmd := exec.Command("go", "env", "GOCACHE", "GOMODCACHE")
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+childConfigHome)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOCACHE GOMODCACHE: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("go env GOCACHE GOMODCACHE: got %d line(s), want 2: %q", len(lines), out)
	}
	for name, path := range map[string]string{"GOCACHE": lines[0], "GOMODCACHE": lines[1]} {
		if path == "" {
			t.Errorf("child go process reports an empty %s", name)
			continue
		}
		if strings.HasPrefix(path, sandboxHomeDir) {
			t.Errorf("child go process's %s = %q is inside the sandbox home %q; the real cache was not exported to it", name, path, sandboxHomeDir)
		}
	}

	for _, rel := range []string{filepath.Join(".cache", "go-build"), filepath.Join("go", "pkg", "mod")} {
		if _, err := os.Stat(filepath.Join(sandboxHomeDir, rel)); err == nil {
			t.Errorf("sandbox home contains %s after a child go invocation; a real cache was written there instead of the real machine's", rel)
		}
	}
}
