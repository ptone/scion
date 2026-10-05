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
	"net/http"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// runtimeSelectionOpts returns the options the start/restart runtime
// selection (resolveManagerForOpts and the checks that re-resolve it) runs
// against. opts.Profile there is the agent-info.json profile
// (agent.GetSavedProfile), which the container can write. The selected
// runtime decides whether the bare-image local-exists check runs and so
// whether the image_registry prefix is applied, so for an agent with
// broker-side image provenance the provisioned profile recorded there is
// used instead (ptone/scion#1799). An agent without a provenance file keeps
// the saved profile; an unusable provenance file is an error, never a
// fallback. Only the runtime selection changes: opts itself, and every
// other use of opts.Profile, is left as it was.
func runtimeSelectionOpts(opts api.StartOptions, agentID string) (api.StartOptions, error) {
	if opts.ProjectPath == "" {
		return opts, nil
	}
	profile, ok, err := agent.ProvisionedProfile(opts.ProjectPath, agentID, opts.SharedWorkspace)
	if err != nil {
		return opts, err
	}
	if ok {
		opts.Profile = profile
	}
	return opts, nil
}

// writeImageProvenanceError reports an unusable image provenance record. It
// is a conflict with the agent's on-disk state, fixed by re-provisioning.
func (s *Server) writeImageProvenanceError(w http.ResponseWriter, err error, op, agentID string) {
	s.agentLifecycleLog.Error("image provenance unusable", "operation", op, "agent_id", agentID, "error", err)
	writeError(w, http.StatusConflict, ErrCodeConflict, "Failed to "+op+": "+err.Error(), nil)
}
