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
