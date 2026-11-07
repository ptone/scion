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

package control

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImports_NoAgentManager: the control package uses the shared
// delivery package and pkg/agent/state, never pkg/agent itself (the agent
// manager) or any other pkg/agent subpackage (design r3 §3.4).
func TestImports_NoAgentManager(t *testing.T) {
	const agentPkg = "github.com/GoogleCloudPlatform/scion/pkg/agent"
	allowed := map[string]bool{agentPkg + "/tmuxdeliver": true, agentPkg + "/state": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked, sawDeliver := 0, false
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range af.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path == agentPkg+"/tmuxdeliver" {
				sawDeliver = true
			}
			if (path == agentPkg || strings.HasPrefix(path, agentPkg+"/")) && !allowed[path] {
				t.Errorf("%s imports %q", f, path)
			}
		}
	}
	if checked == 0 || !sawDeliver {
		t.Fatalf("checked %d files, tmuxdeliver imported %v", checked, sawDeliver)
	}
}
