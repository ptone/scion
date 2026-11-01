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

package agent

import (
	"log/slog"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// loadOperatorSettings loads the broker's global-only settings for the
// runtime.allow_unintercepted_metadata opt-out. Tests replace it.
var loadOperatorSettings = config.LoadGlobalSettings

// uninterceptedMetadataOptOut returns the operator's
// runtime.allow_unintercepted_metadata opt-out for a start that requests the
// metadata interception. It reads only the broker's global settings file,
// never the project-merged settings, agent env or start options, so a
// project or agent creator cannot enable it. Without the interception, or
// when the global settings cannot be loaded, it returns both false (the
// runtime then refuses a start it cannot intercept).
func uninterceptedMetadataOptOut(agentName string, interception bool) (enabled, acknowledgeCloudHost bool) {
	if !interception {
		return false, false
	}
	vs, _, err := loadOperatorSettings()
	if err != nil {
		slog.Warn("Start: failed to load global settings; the runtime.allow_unintercepted_metadata opt-out is treated as unset",
			"agent", agentName, "error", err)
		return false, false
	}
	return config.UninterceptedMetadataOptOut(vs)
}
