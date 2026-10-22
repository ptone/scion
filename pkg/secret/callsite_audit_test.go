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

package secret

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// moduleRoot is the repository root, two levels above pkg/secret.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	return root
}

// selectorCallSites returns "<file>:<enclosing func>" for every call of a
// method or function named name in the non-test Go files of the module
// (extras, which are separate modules, and web are excluded).
func selectorCallSites(t *testing.T, name string) []string {
	t.Helper()
	root := moduleRoot(t)
	var sites []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			switch rel {
			case ".git", "extras", "web", "node_modules", "docs-site", ".scion":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if fun.Sel.Name == name {
						sites = append(sites, filepath.ToSlash(rel)+":"+fn.Name.Name)
					}
				case *ast.Ident:
					if fun.Name == name {
						sites = append(sites, filepath.ToSlash(rel)+":"+fn.Name.Name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(sites)
	return sites
}

// decryptForMigrationCallers is the allow-list of non-test call sites of
// ValueCipher.DecryptForMigration ("<file>:<func>"). Plaintext admission is
// migration-only (ptone/scion#1530 §7): only the administrative migration
// reader (phase 6) may call it, never a runtime path. Adding an entry is a
// security-relevant change.
var decryptForMigrationCallers = map[string]bool{}

func TestDecryptForMigrationCallers(t *testing.T) {
	for _, site := range selectorCallSites(t, "DecryptForMigration") {
		if !decryptForMigrationCallers[site] {
			t.Errorf("DecryptForMigration called outside the migration allow-list: %s", site)
		}
	}
}

// The scanner itself must see calls: a known caller of a known name.
func TestSelectorCallSitesFindsKnownCaller(t *testing.T) {
	sites := selectorCallSites(t, "decryptRawValue")
	found := false
	for _, s := range sites {
		if s == "pkg/secret/localbackend.go:Resolve" {
			found = true
		}
	}
	if !found {
		t.Fatalf("scanner did not find LocalBackend.Resolve calling decryptRawValue: %v", sites)
	}
}
