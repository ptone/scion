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
	"fmt"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/agenthome"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// harnessSkillsDir returns the harness's skills directory, a path relative
// to the agent home. It comes from harness-config config.yaml, which a
// template can bundle (or from settings), so a value that is not a local
// relative path (absolute, or leaving the home) is an error rather than a
// directory outside the home.
func harnessSkillsDir(h api.Harness) (string, error) {
	dir := h.SkillsDir()
	if dir == "" {
		return "", nil
	}
	if !filepath.IsLocal(dir) || filepath.Clean(dir) == "." {
		return "", fmt.Errorf("harness skills_dir %q must be a relative path inside the agent home", dir)
	}
	return dir, nil
}

// mkdirInHome creates rel (a local path relative to agentHome) and its
// parents through a handle confined to the agent home (pkg/agenthome). The
// agent can write its home, so a component that is a symbolic link is
// refused instead of followed.
func mkdirInHome(agentHome, rel string) error {
	home, err := agenthome.OpenCreate(agentHome, 0755)
	if err != nil {
		return err
	}
	defer func() { _ = home.Close() }()
	return home.MkdirAll(filepath.ToSlash(filepath.Clean(rel)), 0755)
}
