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

package hub

import (
	"net/http"

	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
)

// experimentsResponse is the response shape for GET /api/v1/experiments.
type experimentsResponse struct {
	Experiments map[string]bool `json:"experiments"`
}

// namedExperimentProbeAllowlist lists the server-layer experiments that any
// authenticated principal (including project visitors) may probe individually
// via GET /api/v1/experiments?name=<flag>. Keeping this allowlist explicit
// avoids disclosing unrelated server-layer rollout posture.
var namedExperimentProbeAllowlist = map[string]bool{
	experiments.MCPServer: true,
}

// handleExperiments handles GET /api/v1/experiments (ptone/scion#2217).
//
// It is the client's source of truth for the hub-wide experiment map,
// fetched at boot in parallel with GET /api/v1/settings/public. The route is
// classified RouteAuthenticated: any signed-in identity (user, agent, or
// broker token) receives the web-layer map; there is no per-experiment
// authorization.
//
// Passing ?name=<flag> for a flag in namedExperimentProbeAllowlist (currently
// only hub.mcp_server) returns only that single flag's resolved value, and is
// also admitted for project visitors (*VisitorIdentity) so standalone MCP
// servers can probe hub.mcp_server on a visitor's behalf without exposing the
// full map. Unlisted or unknown flag names are refused with 400. A visitor
// calling GET /api/v1/experiments without ?name= is refused with 403.
func (s *Server) handleExperiments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, http.MethodGet)
		return
	}
	if name := r.URL.Query().Get("name"); name != "" {
		if !namedExperimentProbeAllowlist[name] {
			BadRequest(w, "unknown or unsupported experiment name")
			return
		}
		writeJSON(w, http.StatusOK, experimentsResponse{
			Experiments: map[string]bool{name: s.experimentEnabled(name)},
		})
		return
	}
	if _, isVisitor := GetIdentityFromContext(r.Context()).(*VisitorIdentity); isVisitor {
		writeError(w, http.StatusForbidden, ErrCodeForbidden, visitorRouteRefusalMessage, nil)
		return
	}
	writeJSON(w, http.StatusOK, experimentsResponse{Experiments: s.resolvedExperiments()})
}
