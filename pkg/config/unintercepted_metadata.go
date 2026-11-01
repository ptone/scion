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

package config

// UninterceptedMetadataOptOut returns runtime.allow_unintercepted_metadata
// from vs. The caller must pass the broker's global-only settings
// (LoadGlobalSettings), never project-merged settings: the opt-out is
// operator-only, so a project or agent creator cannot enable it. A nil
// settings value, or an unset block, returns both false.
func UninterceptedMetadataOptOut(vs *VersionedSettings) (enabled, acknowledgeCloudHost bool) {
	if vs == nil || vs.Runtime == nil || vs.Runtime.AllowUninterceptedMetadata == nil {
		return false, false
	}
	o := vs.Runtime.AllowUninterceptedMetadata
	return o.Enabled, o.AcknowledgeCloudHost
}
