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
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// imageProvenanceFile is the broker-side agent state file that records each
// image input ProvisionAgent saw. It lives in the agent dir next to
// scion-agent.json — not in the agent home (agent-info.json), which is
// mounted into the container — so the container cannot influence the image
// a later Start selects (ptone/scion#1799).
const imageProvenanceFile = "image-provenance.json"

// imageProvenance is the per-source image record. See settings-precedence.md,
// "Container image and Kubernetes image pull policy".
type imageProvenance struct {
	// RequestImage is the user's explicit request image at provision time
	// (CLI --image, a local --config image the CLI promotes to it, or the
	// Hub's request image). Start replays it at the top tier when the
	// current request sets no image, so a first start and a later plain
	// restart rank it the same way.
	RequestImage string `json:"requestImage,omitempty"`
	// TemplateImage and TemplateImagePullPolicy are the template chain's
	// own image / kubernetes.imagePullPolicy at provision time, with no
	// inline, profile, settings or file contribution folded in. Start uses
	// them as the template tier only when the template can no longer be
	// resolved (e.g. a hub agent restarted on a broker with no local copy),
	// so a profile or settings pin removed since provision never lingers
	// disguised as the template's value.
	TemplateImage           string `json:"templateImage,omitempty"`
	TemplateImagePullPolicy string `json:"templateImagePullPolicy,omitempty"`
}

func writeImageProvenance(agentDir string, p imageProvenance) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(agentDir, imageProvenanceFile), data, 0644)
}

// readImageProvenance returns the recorded provenance, or nil when the agent
// was provisioned before it was recorded (or the file is unreadable), in
// which case Start falls back to its legacy behaviour.
func readImageProvenance(agentDir string) *imageProvenance {
	if agentDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(agentDir, imageProvenanceFile))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logImageProvenanceReadError(agentDir, err)
		}
		return nil
	}
	var p imageProvenance
	if err := json.Unmarshal(data, &p); err != nil {
		logImageProvenanceReadError(agentDir, err)
		return nil
	}
	return &p
}

func logImageProvenanceReadError(agentDir string, err error) {
	slog.Warn("image resolution: ignoring unreadable image provenance; falling back to legacy precedence",
		"path", filepath.Join(agentDir, imageProvenanceFile), "error", err)
}
