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

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// The CLI resolves templates on its own host: an absolute template directory
// is still accepted there (it is uploaded or compared, never sent as a path),
// while a relative reference stays inside the template directories.
func TestFindLocalTemplate_AcceptsAbsoluteDirOnTheCLIHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	tplDir := filepath.Join(t.TempDir(), "my-template")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tplDir, "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tpl, err := findLocalTemplate(tplDir)
	if err != nil {
		t.Fatalf("findLocalTemplate(%q): %v", tplDir, err)
	}
	if tpl.Path != tplDir || tpl.Name != "my-template" {
		t.Fatalf("got Name=%q Path=%q, want my-template at %q", tpl.Name, tpl.Path, tplDir)
	}

	rel, err := filepath.Rel(filepath.Join(home, ".scion", "templates"), tplDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := findLocalTemplate(rel); !errors.Is(err, config.ErrTemplateRefNotAllowed) {
		t.Fatalf("findLocalTemplate(%q) error = %v, want ErrTemplateRefNotAllowed", rel, err)
	}
}
