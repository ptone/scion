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
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// explicitDispatchImage returns the image the USER explicitly chose for this
// agent — the request-level image (`--image`, or `image` in the create
// request's config), or a later explicit edit of it — and "" when the user
// never chose one. It is what a dispatch sends as the broker's top-tier
// image (opts.Image).
//
// It deliberately does NOT read AppliedConfig.Image. That field is
// dual-purpose: resolveDerivedConfig fills it from the template when the
// request set no image, and applyBrokerAgentConfig overwrites it with
// whatever image the broker resolved. Sending it as the top tier made a
// template's image unbeatable (even by an explicit profile
// harness_overrides.<hc>.image) and froze a once-resolved image across later
// dispatches (ptone/scion#1799). A template's image instead reaches the
// broker through the template itself, which the broker hydrates and resolves
// at the template tier on create and restart alike.
//
// Source of truth: CreateInputs.InlineConfig.Image, which buildAppliedConfig
// captures from the request and recordExplicitEdits updates only on a
// genuine (non-echo) edit. Agents written before CreateInputs existed fall
// back to the live InlineConfig.Image, which buildAppliedConfig set from the
// same request config.
func explicitDispatchImage(ac *store.AgentAppliedConfig) string {
	if ac == nil {
		return ""
	}
	if ac.CreateInputs != nil {
		if ac.CreateInputs.InlineConfig != nil {
			return ac.CreateInputs.InlineConfig.Image
		}
		return ""
	}
	if ac.InlineConfig != nil {
		return ac.InlineConfig.Image
	}
	return ""
}

// dispatchImageForBroker is explicitDispatchImage rewritten to the
// dispatcher's image registry, the form every dispatch sends.
func (d *HTTPAgentDispatcher) dispatchImageForBroker(ac *store.AgentAppliedConfig) string {
	image := explicitDispatchImage(ac)
	if image != "" && d.imageRegistry != "" {
		image = config.RewriteImageRegistry(image, d.imageRegistry)
	}
	return image
}
