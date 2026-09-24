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
	"os"
	"testing"
)

// TestMain clears every Hub- and privilege-drop-related environment
// variable before any test in this package runs, for the whole process
// lifetime — see cmd/sciontool/commands's TestMain (testmain_test.go) for
// the incident this defends against. This package doesn't import
// pkg/sciontool/hub or resolve a fixed real home directory itself, so the
// blast radius here is smaller, but handleBootstrap does read
// SCION_HOST_UID/GID out of the real process environment (set there by a
// test's own req.Env, via os.Setenv, exactly like a real bootstrap
// request), so a test that forgets to reset them could otherwise leak a
// previous test's values into a later one.
func TestMain(m *testing.M) {
	for _, v := range []string{
		"SCION_HUB_ENDPOINT", "SCION_HUB_URL", "SCION_AUTH_TOKEN",
		"SCION_AGENT_ID", "SCION_AGENT_MODE",
		"SCION_HOST_UID", "SCION_HOST_GID", "SCION_KEEPID_UID",
	} {
		_ = os.Unsetenv(v)
	}
	os.Exit(m.Run())
}
