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

//go:build !windows

package hub

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// localLinkedConfigYAMLTree returns a parent directory with two
// harness-config directories: claude-real, whose config.yaml is a regular
// file naming it real-cfg, and claude-linked, whose config.yaml is a link to
// a valid config.yaml outside the parent that names it outside-cfg and
// declares an unusable provisioner. A reader that follows the link sees a
// valid config, a name and the provisioner refusal.
func localLinkedConfigYAMLTree(t *testing.T) string {
	t.Helper()
	parent := writeCollectedTestTree(t, map[string]string{
		"claude-real/config.yaml":    "harness: claude\nname: real-cfg\n",
		"claude-linked/home/.rc":     "# rc\n",
		"claude-real/home/.rc":       "# rc\n",
		"claude-other/home/.rc":      "# rc\n",
		"claude-other/README.md":     "not a harness-config\n",
		"claude-other/config.yaml/x": "a directory named config.yaml\n",
	})
	outside := writeCollectedTestTree(t, map[string]string{
		"config.yaml": "harness: claude\nname: outside-cfg\nprovisioner:\n  type: builtin\n",
	})
	if err := os.Symlink(filepath.Join(outside, "config.yaml"), filepath.Join(parent, "claude-linked", "config.yaml")); err != nil {
		t.Fatal(err)
	}
	// The link target is valid: the unconfined loader accepts it.
	if _, err := config.LoadHarnessConfigDir(filepath.Join(parent, "claude-linked")); err != nil {
		t.Fatalf("fixture: the link target does not load: %v", err)
	}
	return parent
}

func TestIsHarnessConfigDir_DoesNotFollowLinkedConfigYAML(t *testing.T) {
	parent := localLinkedConfigYAMLTree(t)
	for name, want := range map[string]bool{
		"claude-real":   true,
		"claude-linked": false,
		"claude-other":  false,
	} {
		if got := isHarnessConfigDir(filepath.Join(parent, name)); got != want {
			t.Errorf("isHarnessConfigDir(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestLoadLocalHarnessConfigDir(t *testing.T) {
	parent := localLinkedConfigYAMLTree(t)

	t.Run("regular config.yaml loads like LoadHarnessConfigDir", func(t *testing.T) {
		dir := filepath.Join(parent, "claude-real")
		got, err := loadLocalHarnessConfigDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		want, err := config.LoadHarnessConfigDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != want.Name || got.Path != want.Path || got.Config.Harness != want.Config.Harness {
			t.Errorf("loaded %q %q %q, want %q %q %q", got.Name, got.Path, got.Config.Harness, want.Name, want.Path, want.Config.Harness)
		}
	})
	t.Run("linked config.yaml fails", func(t *testing.T) {
		hc, err := loadLocalHarnessConfigDir(filepath.Join(parent, "claude-linked"))
		if err == nil {
			t.Fatalf("loaded %+v through a linked config.yaml", hc)
		}
		if !errors.Is(err, errNotRegularLocalFile) {
			t.Errorf("error = %v, want the not-a-regular-file refusal", err)
		}
	})
	t.Run("missing config.yaml fails", func(t *testing.T) {
		if _, err := loadLocalHarnessConfigDir(filepath.Join(parent, "claude-other")); err == nil {
			t.Fatal("loaded a directory whose config.yaml is a directory")
		}
	})
	t.Run("linked directory fails", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "claude-dirlink")
		if err := os.Symlink(filepath.Join(parent, "claude-real"), link); err != nil {
			t.Fatal(err)
		}
		if _, err := loadLocalHarnessConfigDir(link); err == nil {
			t.Fatal("loaded a harness-config through a linked directory")
		}
	})
	t.Run("linked sidecar reads as absent", func(t *testing.T) {
		dir := filepath.Join(parent, "claude-real")
		outside := filepath.Join(t.TempDir(), config.AgentPluginsSidecarFile)
		if err := os.WriteFile(outside, []byte("{{ not yaml"), 0o644); err != nil {
			t.Fatal(err)
		}
		sidecar := filepath.Join(dir, config.AgentPluginsSidecarFile)
		if err := os.Symlink(outside, sidecar); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(sidecar) })
		// The unconfined loader follows the link and records the invalid
		// sidecar; the confined one does not open it.
		if followed, err := config.LoadHarnessConfigDir(dir); err != nil || followed.Config.AgentPluginsSidecar == nil {
			t.Fatalf("fixture: the unconfined loader did not read the linked sidecar (%v)", err)
		}
		got, err := loadLocalHarnessConfigDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got.Config.AgentPluginsSidecar != nil {
			t.Errorf("the linked sidecar was read: %+v", got.Config.AgentPluginsSidecar)
		}
	})
}

func TestReadLocalHarnessConfigFile_LinkReadsAsAbsent(t *testing.T) {
	parent := localLinkedConfigYAMLTree(t)
	if _, err := readLocalHarnessConfigFile(filepath.Join(parent, "claude-real"), "config.yaml"); err != nil {
		t.Fatalf("regular config.yaml: %v", err)
	}
	_, err := readLocalHarnessConfigFile(filepath.Join(parent, "claude-linked"), "config.yaml")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("linked config.yaml: err = %v, want absent", err)
	}
}

// Import discovery does not follow a linked config.yaml: the directory is
// reported as skipped, and its name is not bound from the link target.
func TestDiscoverResourceDirs_LinkedHarnessConfigYAMLNotFollowed(t *testing.T) {
	parent := localLinkedConfigYAMLTree(t)
	kind := (&Server{}).harnessConfigImportKind()

	dirs, skipped, err := discoverResourceDirs(parent, "", kind)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0].name != "real-cfg" || !dirs[0].nameFromConfig {
		t.Errorf("dirs = %+v, want only real-cfg named from its config.yaml", dirs)
	}
	linkedSkipped := false
	for _, sd := range skipped {
		if sd.name == "claude-linked" {
			linkedSkipped = true
		}
	}
	if !linkedSkipped {
		t.Errorf("skipped = %+v, want claude-linked reported as skipped", skipped)
	}

	t.Run("name is not bound from the link target", func(t *testing.T) {
		// Admit every directory, so the name binding is tested on its own.
		admitAll := kind
		admitAll.isResourceDir = func(string) bool { return true }
		leaf := filepath.Join(parent, "claude-linked")
		dirs, _, err := discoverResourceDirs(leaf, "", admitAll)
		if err != nil {
			t.Fatal(err)
		}
		if len(dirs) != 1 || dirs[0].name != "claude-linked" || dirs[0].nameFromConfig {
			t.Errorf("dirs = %+v, want claude-linked not named from the link target", dirs)
		}
	})
}

// The import's store selection and provisioner check do not read a linked
// config.yaml.
func TestHarnessConfigImportKind_LinkedConfigYAMLNotRead(t *testing.T) {
	parent := localLinkedConfigYAMLTree(t)
	kind := (&Server{}).harnessConfigImportKind()
	linked := resourceDir{name: "claude-linked", path: filepath.Join(parent, "claude-linked")}

	if _, err := kind.newStore(linked.path); err == nil {
		t.Error("newStore accepted a directory whose config.yaml is a link")
	}
	if err := kind.checkContent([]resourceDir{linked}); err != nil {
		t.Errorf("checkContent read the linked config.yaml: %v", err)
	}
}
