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
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hub"
)

// TestMain clears every Hub- and privilege-drop-related environment
// variable, and redirects HOME/XDG_*/SCION_WORKSPACE_PATH/the Hub token
// home to one per-binary temp directory, before any test in this package
// runs — see cmd/sciontool/commands's TestMain (testmain_test.go) for the
// two incidents this defends against. This package's own production code
// doesn't import pkg/sciontool/hub or resolve a fixed real home directory
// itself (every test here drives WithInitRunner/WithPrivilegeDropChecker
// with local fakes, never the real RunInit — see cmd/sciontool/commands's
// newSubstrateServeServer for where the real ones are wired instead), so
// the blast radius here is smaller. But handleBootstrap does read
// SCION_HOST_UID/GID out of the real process environment (set there by a
// test's own req.Env, via os.Setenv, exactly like a real bootstrap
// request), so a test that forgets to reset them could otherwise leak a
// previous test's values into a later one; and the HOME/XDG/workspace/hub
// token home redirection is cheap insurance against any future test in
// this package that does end up resolving a real path.
func TestMain(m *testing.M) {
	for _, v := range []string{
		"SCION_HUB_ENDPOINT", "SCION_HUB_URL", "SCION_AUTH_TOKEN",
		"SCION_AGENT_ID", "SCION_AGENT_MODE",
		"SCION_HOST_UID", "SCION_HOST_GID", "SCION_KEEPID_UID",
	} {
		_ = os.Unsetenv(v)
	}

	tmpHome, err := os.MkdirTemp("", "sciontool-substrate-test-home-*")
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
	restoreTokenHome := hub.SetTokenHome(tmpHome)

	code := m.Run()
	restoreTokenHome()
	_ = os.RemoveAll(tmpHome)
	os.Exit(code)
}
