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

	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
)

// handleAdminMessagingDivergence handles GET /api/v1/admin/messaging/divergence.
//
// Returns the current divergence counters and the conversation read-switch flag.
// Admin-gated via requireAdminHandler at route registration.
func (s *Server) handleAdminMessagingDivergence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	readSwitchEnabled := false
	if ops := s.GetOperationalSettings(); ops != nil {
		readSwitchEnabled = ops.ConversationReadSwitch()
	}

	matches := messaging.DivergenceMetrics.Matches()
	mismatches := messaging.DivergenceMetrics.Mismatches()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"matches":              matches,
		"mismatches":           mismatches,
		"total":                matches + mismatches,
		"read_switch_enabled":  readSwitchEnabled,
	})
}
