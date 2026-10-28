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

package brokercontrol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// floorOnlyNames are names joined onto the Scion global directory (or onto
// "<home>/.scion" inside an agent) that are reviewed as not being Runtime
// Broker control files. They need no named entry: the global-directory floor
// already refuses every one of them as a mount source, because none is on
// VolumeAllowed's list.
var floorOnlyNames = map[string]string{
	"agents":                         "global project agent directories (agent data)",
	"attachments":                    "Hub attachment uploads (user data)",
	"cache":                          "shared caches (rebuildable data)",
	"default":                        "default template (embedded content)",
	"harness-configs":                "harness configuration templates",
	"templates":                      "agent templates",
	"storage":                        "Hub local storage fallback (user data)",
	"groves":                         "legacy name of projects",
	"grove-configs":                  "legacy name of project-configs",
	"harness":                        "agent-home harness files (inside an agent)",
	"hooks":                          "agent-home hooks (inside an agent)",
	"resolved-skills.json":           "agent-home skills record (inside an agent)",
	"system_prompt.md":               "agent-home prompt (inside an agent)",
	"telemetry-gcp-credentials.json": "agent-home telemetry credentials (inside an agent; the agent's own)",
}

// allowListRoots are the top-level names VolumeAllowed admits subtrees of.
var allowListRoots = map[string]bool{"workspace": true, "projects": true, "project-configs": true}

// TestGlobalDirNamesAreClassified requires every name the product code joins
// onto the Scion global directory to be a named control entry, an
// allow-list root, or a reviewed floor-only name. A new file or directory in
// the global directory fails this test until it is classified: add it to
// namedEntries if it is a Runtime Broker or Hub control file (credentials,
// settings, identity, state, executables), otherwise to floorOnlyNames with
// a reason.
func TestGlobalDirNamesAreClassified(t *testing.T) {
	named := map[string]bool{}
	for _, n := range namedEntries {
		named[n.rel] = true
	}
	found := globalDirNames(t, filepath.Join("..", ".."))
	var unclassified []string
	for name, where := range found {
		if named[name] || allowListRoots[name] {
			continue
		}
		if _, ok := floorOnlyNames[name]; ok {
			continue
		}
		unclassified = append(unclassified, name+" ("+where+")")
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("names joined onto the Scion global directory are not classified for the mount guard:\n  %s\n"+
			"add each to namedEntries (a control file) or floorOnlyNames (with a reason)", strings.Join(unclassified, "\n  "))
	}
	for _, must := range []string{"broker-credentials.json", "hub-credentials", "settings.yaml", "hub.db", "dev-token"} {
		if _, ok := found[must]; !ok {
			t.Errorf("scanner no longer finds %q: the matcher has drifted from the code", must)
		}
	}
}

// globalDirNames scans non-test Go files under root/cmd and root/pkg
// (excluding code that runs inside an agent container) for
// filepath.Join(<global dir>, "<name>", ...) and
// filepath.Join(<x>, ".scion" | GlobalDir, "<name>", ...), returning each
// first path element with one file that uses it.
func globalDirNames(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "testdata", "sciontool":
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
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isFilepathJoin(call.Fun) {
					return true
				}
				for i := 0; i+1 < len(call.Args); i++ {
					if !isGlobalDirExpr(call.Args[i]) {
						continue
					}
					if name, ok := stringLit(call.Args[i+1]); ok {
						first := strings.SplitN(filepath.ToSlash(name), "/", 2)[0]
						if _, seen := out[first]; !seen {
							out[first] = filepath.ToSlash(path)
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scanning %s: %v", dir, err)
		}
	}
	return out
}

func isFilepathJoin(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "filepath"
}

// isGlobalDirExpr reports whether e names the Scion global directory: an
// identifier, field or call whose name contains "global" (globalDir,
// cfg.GlobalDir, config.GetGlobalDir()), the literal ".scion", or the
// GlobalDir constant.
func isGlobalDirExpr(e ast.Expr) bool {
	if s, ok := stringLit(e); ok {
		return s == ".scion"
	}
	if c, ok := e.(*ast.CallExpr); ok {
		e = c.Fun
	}
	var name string
	switch v := e.(type) {
	case *ast.Ident:
		name = v.Name
	case *ast.SelectorExpr:
		name = v.Sel.Name
	default:
		return false
	}
	return strings.Contains(strings.ToLower(name), "global")
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}
