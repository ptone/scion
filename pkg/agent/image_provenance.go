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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// imageProvenanceFile is the broker-side agent state file that records each
// image input ProvisionAgent saw. It lives in the agent dir next to
// scion-agent.json — not in the agent home (agent-info.json), which is
// mounted into the container — so the container cannot influence the image
// a later Start selects (ptone/scion#1799). Image provenance (including the
// provisioned profile) is recorded broker-side.
const imageProvenanceFile = "image-provenance.json"

// imageProvenanceVersion is written into every record. A record without it
// (or with another value) is rejected rather than half-trusted.
const imageProvenanceVersion = 1

// imageProvenance is the per-source image record. See settings-precedence.md,
// "Container image and Kubernetes image pull policy".
type imageProvenance struct {
	// Version is always imageProvenanceVersion; see readImageProvenance.
	Version int `json:"version"`
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
	// InlineImage and InlineImagePullPolicy are the create-time inline
	// config's own image / kubernetes.imagePullPolicy (the broker-side
	// counterparts of agent-info.json's display copies ExplicitImage /
	// ExplicitImagePullPolicy). Start falls back to them when the current
	// request's inline config does not set the field.
	InlineImage           string `json:"inlineImage,omitempty"`
	InlineImagePullPolicy string `json:"inlineImagePullPolicy,omitempty"`
	// Profile is the settings profile the agent was provisioned with
	// (after the active-profile fallback). Start uses only this profile to
	// look up the profile harness_overrides image and pull policy and the
	// profile image_registry, so no agent-info.json field can steer image
	// selection.
	//
	// An empty Profile means no profile was set or active at provision; the
	// profile active at start time then applies, as for any settings
	// lookup with no profile.
	Profile string `json:"profile,omitempty"`
	// Template is the template the agent was provisioned from (the same
	// value recorded, for display, as agent-info.json's template). Start
	// resolves the template-tier image and pull policy from this template
	// when the start request carries no absolute template path, so editing
	// agent-info.json cannot move the agent onto another template's image.
	Template string `json:"template,omitempty"`
}

func writeImageProvenance(agentDir string, p imageProvenance) error {
	p.Version = imageProvenanceVersion
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	// Mode 0600, written atomically (temp file + rename) so a reader never
	// sees a partial record.
	return writeAgentInfoFile(filepath.Join(agentDir, imageProvenanceFile), data, 0o600)
}

// readImageProvenance returns the recorded provenance. It returns (nil, nil)
// only when the file is genuinely absent — an agent provisioned before
// provenance was recorded — in which case Start uses its legacy behaviour.
// A file that exists but cannot be read, does not parse, or lacks the
// version marker is an error: Start must fail rather than fall back to
// agent-info.json, which the container can write.
func readImageProvenance(agentDir string) (*imageProvenance, error) {
	if agentDir == "" {
		return nil, nil
	}
	path := filepath.Join(agentDir, imageProvenanceFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, imageProvenanceError(path, err)
	}
	var p imageProvenance
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, imageProvenanceError(path, err)
	}
	if p.Version != imageProvenanceVersion {
		return nil, imageProvenanceError(path, fmt.Errorf("unsupported or missing version %d (want %d)", p.Version, imageProvenanceVersion))
	}
	return &p, nil
}

func imageProvenanceError(path string, err error) error {
	return fmt.Errorf("image provenance %s is unusable (%w); refusing to fall back to agent-info.json: re-provision the agent (scion reincarnate, or delete and re-create it)", path, err)
}
