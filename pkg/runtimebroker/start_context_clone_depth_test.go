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

package runtimebroker

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// Row 5 (task #49): Depth: 0 through start_context.go → no SCION_GIT_DEPTH
// emitted. This is the "third reading of 0": emit nothing, defer to the
// in-container default.
func TestCloneDepth_Row5_DepthZeroNoEnvVar(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.StateDir = t.TempDir()
	srv := newTestServerForStartContext(t, cfg)

	r := httptest.NewRequest("POST", "/api/v1/agents", nil)
	sc, err := srv.buildStartContext(context.Background(), startContextInputs{
		Name:        "agent-depth0",
		ProjectPath: "/some/path",
		Config: &CreateAgentConfig{
			GitClone: &api.GitCloneConfig{
				URL:   "https://github.com/org/repo.git",
				Depth: 0,
			},
		},
		HTTPRequest: r,
	})
	if err != nil {
		t.Fatal(err)
	}

	if val, present := sc.Opts.Env["SCION_GIT_DEPTH"]; present {
		t.Errorf("Depth: 0 should NOT emit SCION_GIT_DEPTH, got %q", val)
	} else {
		t.Logf("CONFIRMED: Depth: 0 → no SCION_GIT_DEPTH emitted (defers to in-container default)")
	}
}

// Task #49 R1: Depth: -1 through start_context.go → SCION_GIT_DEPTH="-1".
// The sentinel must survive the guard so init.go can distinguish full clone
// from the shallow default. Without this, -1 is silently dropped and the
// operator's clone-depth label is ignored.
func TestCloneDepth_DepthNegOneForwarded(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.StateDir = t.TempDir()
	srv := newTestServerForStartContext(t, cfg)

	r := httptest.NewRequest("POST", "/api/v1/agents", nil)
	sc, err := srv.buildStartContext(context.Background(), startContextInputs{
		Name:        "agent-fullclone",
		ProjectPath: "/some/path",
		Config: &CreateAgentConfig{
			GitClone: &api.GitCloneConfig{
				URL:   "https://github.com/org/repo.git",
				Depth: -1,
			},
		},
		HTTPRequest: r,
	})
	if err != nil {
		t.Fatal(err)
	}

	val, present := sc.Opts.Env["SCION_GIT_DEPTH"]
	if !present {
		t.Fatal("Depth: -1 should emit SCION_GIT_DEPTH, but it was absent — sentinel dropped by guard")
	}
	if val != "-1" {
		t.Errorf("SCION_GIT_DEPTH = %q, want \"-1\"", val)
	}
	t.Logf("CONFIRMED: Depth: -1 → SCION_GIT_DEPTH=\"-1\" forwarded")
}
