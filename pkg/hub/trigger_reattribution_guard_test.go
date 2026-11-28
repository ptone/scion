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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTriggerReattribution_OnlyViaGuardedHelper is a structural AST guard
// ensuring that no trigger re-attribution site in pkg/hub can call
// reattributeInitiator or reassign .AuthorityCeiling directly without going
// through Server.reattributeTriggerRevision (which enforces §11.6 System One
// re-attribution authority; ptone/scion#9916).
//
// Schedule handlers in handlers_schedules.go do not carry match_rules or
// system_one elements and are explicitly allowlisted.
func TestTriggerReattribution_OnlyViaGuardedHelper(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	var violations []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "handlers_schedules.go" {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, err, "parse %s", name)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if name == "trigger_rules.go" && fn.Name.Name == "reattributeTriggerRevision" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "reattributeInitiator" {
						pos := fset.Position(node.Pos())
						violations = append(violations, pos.String()+": direct call to reattributeInitiator in "+fn.Name.Name+" (must use Server.reattributeTriggerRevision)")
					}
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "AuthorityCeiling" {
							pos := fset.Position(sel.Pos())
							violations = append(violations, pos.String()+": direct assignment to .AuthorityCeiling in "+fn.Name.Name+" (must use Server.reattributeTriggerRevision)")
						}
					}
				}
				return true
			})
		}
	}

	require.Empty(t, violations, "trigger re-attribution must go through Server.reattributeTriggerRevision:\n%s", strings.Join(violations, "\n"))
}
