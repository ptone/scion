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
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawFSCalls are the os functions that read, stat, write or create by path
// and follow symbolic links.
var rawFSCalls = map[string]bool{
	"ReadFile": true, "Stat": true, "Lstat": true, "Open": true, "OpenFile": true,
	"WriteFile": true, "MkdirAll": true, "Mkdir": true, "Remove": true, "RemoveAll": true,
	"ReadDir": true, "Create": true,
}

// untrustedPathNames are identifiers whose value is a path from a harness-config
// bundle (which a template can supply): the harness-config directory and the
// config.yaml fields that name paths in the agent home.
var untrustedPathNames = map[string]bool{
	"configDirPath": true, "SystemPromptFile": true, "InstructionsFile": true,
	"SkillsDir": true, "ConfigDir": true, "TargetSuffix": true,
}

// checkNoRawFSOnUntrustedPaths reports every os.<raw call> whose arguments
// mention an untrusted path identifier. Such files must be read through
// config.ReadConfinedFile, or the agent home through pkg/agenthome.
func checkNoRawFSOnUntrustedPaths(f *ast.File) []string {
	var problems []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !rawFSCalls[sel.Sel.Name] {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "os" {
			return true
		}
		for _, arg := range call.Args {
			ast.Inspect(arg, func(m ast.Node) bool {
				var name string
				switch m := m.(type) {
				case *ast.Ident:
					name = m.Name
				case *ast.SelectorExpr:
					name = m.Sel.Name
				}
				if untrustedPathNames[name] {
					problems = append(problems, "os."+sel.Sel.Name+" on "+name)
				}
				return true
			})
		}
		return true
	})
	sort.Strings(problems)
	return problems
}

// The container-script and declarative harnesses never pass a harness-config
// path (the directory, or a config.yaml path field) to a link-following os
// call.
func TestHarnessFilesNeverUseRawFSOnHarnessConfigPaths(t *testing.T) {
	for _, name := range []string{"container_script_harness.go", "declarative_generic.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		assert.Empty(t, checkNoRawFSOnUntrustedPaths(f), name)
	}
}

func TestHarnessPathGuard_NegativeFixtures(t *testing.T) {
	for name, src := range map[string]string{
		"read under configDirPath": `package harness
import ("os"; "path/filepath")
func f(c *ContainerScriptHarness) { _, _ = os.ReadFile(filepath.Join(c.configDirPath, "provision.py")) }`,
		"stat of system_prompt_file": `package harness
import ("os"; "path/filepath")
func f(d *DeclarativeGenericHarness, h string) { _, _ = os.Stat(filepath.Join(h, d.entry.SystemPromptFile)) }`,
		"write of instructions_file": `package harness
import "os"
func f(p string, d *DeclarativeGenericHarness) { _ = os.WriteFile(p+d.entry.InstructionsFile, nil, 0o644) }`,
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, parser.SkipObjectResolution)
		require.NoError(t, err, name)
		problems := checkNoRawFSOnUntrustedPaths(f)
		require.Len(t, problems, 1, name)
		assert.True(t, strings.HasPrefix(problems[0], "os."), name)
	}
}
