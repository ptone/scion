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

package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

const outsideMarker = "outside-marker"

// outsideFile writes a marker file in a temporary directory outside every
// harness-config directory and agent home used by the test.
func outsideFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "marker.txt")
	writeFile(t, p, outsideMarker)
	return p
}

// A harness-config file staged into the agent home that is a symbolic link is
// refused, never copied.
func TestContainerScriptHarness_ProvisionRefusesLinkedHarnessConfigFile(t *testing.T) {
	h, dir := newTestContainerScriptHarness(t)
	if err := os.Remove(filepath.Join(dir, "provision.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile(t), filepath.Join(dir, "provision.py")); err != nil {
		t.Fatal(err)
	}
	agentHome := t.TempDir()

	err := h.Provision(context.Background(), "a", agentHome, agentHome, "/workspace")
	if !errors.Is(err, config.ErrTemplateContentRefused) {
		t.Fatalf("Provision error = %v, want ErrTemplateContentRefused", err)
	}
	if data, rerr := os.ReadFile(filepath.Join(agentHome, ".scion", "harness", "provision.py")); rerr == nil && strings.Contains(string(data), outsideMarker) {
		t.Fatal("the linked file's content was staged into the agent home")
	}
}

// A system_prompt_file that leaves the agent home is never found or read.
func TestContainerScriptHarness_SystemPromptFileOutsideHomeIgnored(t *testing.T) {
	h, _ := newTestContainerScriptHarness(t)
	base := t.TempDir()
	agentHome := filepath.Join(base, "home")
	if err := os.MkdirAll(agentHome, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(base, "outside.md"), outsideMarker)
	h.entry.SystemPromptFile = "../outside.md"
	h.entry.Command.SystemPromptFlag = "--system-prompt"
	h.agentHome = agentHome

	if h.HasSystemPrompt(agentHome) {
		t.Error("HasSystemPrompt reported a file outside the agent home")
	}
	if got := strings.Join(h.GetCommand("task", false, nil), " "); strings.Contains(got, outsideMarker) {
		t.Errorf("command carries the content of a file outside the agent home: %q", got)
	}
}

// The staged system prompt in the agent home (which the agent can write) is
// not read through a symbolic link.
func TestContainerScriptHarness_StagedSystemPromptLinkNotFollowed(t *testing.T) {
	h, _ := newTestContainerScriptHarness(t)
	agentHome := t.TempDir()
	inputs := filepath.Join(agentHome, ".scion", "harness", "inputs")
	if err := os.MkdirAll(inputs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile(t), filepath.Join(inputs, "system-prompt.md")); err != nil {
		t.Fatal(err)
	}
	h.entry.Command.SystemPromptFlag = "--system-prompt"
	h.agentHome = agentHome

	if got := strings.Join(h.GetCommand("task", false, nil), " "); strings.Contains(got, outsideMarker) {
		t.Errorf("command carries the content of a linked staged prompt: %q", got)
	}
}

// The declarative harness refuses a system_prompt_file or instructions_file
// that leaves the agent home, and writes nothing outside it.
func TestDeclarativeGeneric_InjectRefusesPathsOutsideHome(t *testing.T) {
	base := t.TempDir()
	agentHome := filepath.Join(base, "home")
	if err := os.MkdirAll(agentHome, 0o755); err != nil {
		t.Fatal(err)
	}
	d := NewDeclarativeGenericHarness(config.HarnessConfigEntry{
		Harness:          "decl",
		SystemPromptFile: "../written-prompt.md",
		InstructionsFile: "../written-instructions.md",
	})
	if err := d.InjectSystemPrompt(agentHome, []byte("prompt")); err == nil {
		t.Error("InjectSystemPrompt accepted a path outside the agent home")
	}
	if err := d.InjectAgentInstructions(agentHome, []byte("instructions")); err == nil {
		t.Error("InjectAgentInstructions accepted a path outside the agent home")
	}
	for _, name := range []string{"written-prompt.md", "written-instructions.md"} {
		if _, err := os.Lstat(filepath.Join(base, name)); !os.IsNotExist(err) {
			t.Errorf("%s was written outside the agent home", name)
		}
	}
}

// A symbolic link already at the declarative harness's target in the agent
// home is not written through.
func TestDeclarativeGeneric_InjectDoesNotWriteThroughLink(t *testing.T) {
	agentHome := t.TempDir()
	target := outsideFile(t)
	if err := os.MkdirAll(filepath.Join(agentHome, ".decl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(agentHome, ".decl", "system.md")); err != nil {
		t.Fatal(err)
	}
	d := NewDeclarativeGenericHarness(config.HarnessConfigEntry{
		Harness:          "decl",
		SystemPromptFile: ".decl/system.md",
	})
	_ = d.InjectSystemPrompt(agentHome, []byte("new prompt"))
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != outsideMarker {
		t.Fatalf("the file behind the link was changed: %q", data)
	}
}
