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

	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// Image tier names, as they appear in the image-resolution log lines.
const (
	imageTierHarnessConfigFile = "harness-config file"
	imageTierSettings          = "settings harness_configs"
	imageTierTemplate          = "template"
	imageTierTemplateSnapshot  = "recorded template snapshot"
	imageTierInline            = "inline config"
	imageTierProfileOverride   = "profile harness_overrides"
	imageTierRequest           = "explicit --image / request image"
)

// imageCandidate is one tier's contribution to the container image. An
// empty image means the tier does not set one.
type imageCandidate struct {
	source string
	image  string
}

// pickImage resolves the container image from candidates ordered from the
// LOWEST tier to the HIGHEST: each non-empty candidate replaces whatever a
// lower tier supplied. Every time that replaces a different, non-empty image
// from a lower tier, it logs at Info what replaced what and why, so a pinned
// image losing to a higher tier is never silent (ptone/scion#1799).
//
// It returns the winning image and the tier that supplied it ("" when no
// tier set an image).
func pickImage(logger *slog.Logger, agentName string, candidates []imageCandidate) (image, source string) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, c := range candidates {
		if c.image == "" {
			continue
		}
		if image != "" && image != c.image {
			logger.Info("image resolution: lower-tier image replaced",
				"agent", agentName,
				"replaced_image", image,
				"replaced_source", source,
				"image", c.image,
				"source", c.source,
				"reason", c.source+" outranks "+source)
		}
		image, source = c.image, c.source
		util.Debugf("image resolution: from %s image=%s", source, image)
	}
	return image, source
}
