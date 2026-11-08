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

package runtimebroker

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// The broker installs its served-project set on every runtime that enters
// it, through one helper: New, SwapRuntime and the three auxiliary
// inserts, which all build their entry through newAuxiliaryRuntime. The
// test below covers each path; the source guard after it keeps a future
// entry path from skipping the install.

func TestServedProjects_InstalledOnEveryRuntimeEntryPath(t *testing.T) {
	rt := runtime.NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	mgr := agent.NewManager(rt)
	t.Cleanup(mgr.Close)
	srv := New(testServerConfig(), mgr, rt, config.RuntimeBuildRecord{})
	if srv.servedProjects == nil {
		t.Fatal("New did not build the served-project set")
	}
	if got := rt.ProjectSetForTest(); got != srv.servedProjects {
		t.Errorf("New: runtime ProjectSet = %v, want the broker's served-project set", got)
	}

	swapped := runtime.NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	srv.SwapRuntime(swapped, config.RuntimeBuildRecord{})
	if got := swapped.ProjectSetForTest(); got != srv.servedProjects {
		t.Errorf("SwapRuntime: runtime ProjectSet = %v, want the broker's served-project set", got)
	}

	aux := runtime.NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	auxMgr := agent.NewManager(aux)
	t.Cleanup(auxMgr.Close)
	entry, err := srv.newAuxiliaryRuntime(aux, auxMgr, config.RuntimeBuildRecord{})
	if err != nil {
		t.Fatalf("newAuxiliaryRuntime() error = %v", err)
	}
	if got := aux.ProjectSetForTest(); got != srv.servedProjects {
		t.Errorf("newAuxiliaryRuntime: runtime ProjectSet = %v, want the broker's served-project set", got)
	}
	if entry.Runtime != aux || entry.Manager != auxMgr {
		t.Errorf("newAuxiliaryRuntime entry = %+v, want the given runtime and manager", entry)
	}
}

// auxEntryViolations reports, for one function body, the ways it could
// install a runtime into an auxiliaryRuntime entry without
// newAuxiliaryRuntime:
//   - a non-empty auxiliaryRuntime literal (only newAuxiliaryRuntime may
//     build one);
//   - an empty auxiliaryRuntime literal that is not a return value (the
//     zero-value returns of lookups are allowed);
//   - an assignment to .Runtime or .Manager on a value the function binds
//     to an auxiliaryRuntime (a var, a literal, a map entry of
//     auxiliaryRuntimes, a range value over them, a newAuxiliaryRuntime
//     result) or on an embedded .auxiliaryRuntime field.
//
// isBuilder is true for newAuxiliaryRuntime itself.
func auxEntryViolations(body *ast.BlockStmt, isBuilder bool) []string {
	var out []string
	// isAuxType names the types a value can be bound to; isEntryLit the
	// one literal type that builds an entry. A namedAuxiliaryRuntime
	// literal only wraps an entry (sortedAuxiliaryRuntimes copies existing
	// ones); a fresh auxiliaryRuntime literal inside it is still caught.
	isAuxType := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && (id.Name == "auxiliaryRuntime" || id.Name == "namedAuxiliaryRuntime")
	}
	isEntryLit := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == "auxiliaryRuntime"
	}
	mentionsAux := func(e ast.Node) bool {
		found := false
		ast.Inspect(e, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if x.Sel.Name == "auxiliaryRuntimes" || x.Sel.Name == "sortedAuxiliaryRuntimes" || x.Sel.Name == "newAuxiliaryRuntime" {
					found = true
				}
			case *ast.CompositeLit:
				if isAuxType(x.Type) {
					found = true
				}
			}
			return !found
		})
		return found
	}
	returned := map[ast.Node]bool{}
	bound := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ReturnStmt:
			for _, r := range x.Results {
				returned[r] = true
			}
		case *ast.ValueSpec:
			if x.Type != nil && isAuxType(x.Type) {
				for _, nm := range x.Names {
					bound[nm.Name] = true
				}
			}
		case *ast.AssignStmt:
			for _, rhs := range x.Rhs {
				if mentionsAux(rhs) {
					for _, lhs := range x.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
							bound[id.Name] = true
						}
					}
				}
			}
		case *ast.RangeStmt:
			if mentionsAux(x.X) {
				if id, ok := x.Value.(*ast.Ident); ok && id.Name != "_" {
					bound[id.Name] = true
				}
			}
		}
		return true
	})
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CompositeLit:
			if isEntryLit(x.Type) {
				if len(x.Elts) > 0 && !isBuilder {
					out = append(out, "builds a non-empty auxiliaryRuntime literal")
				}
				if len(x.Elts) == 0 && !returned[x] {
					out = append(out, "builds an empty auxiliaryRuntime literal outside a return")
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range x.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Runtime" && sel.Sel.Name != "Manager") {
					continue
				}
				if id, ok := sel.X.(*ast.Ident); ok && bound[id.Name] {
					out = append(out, "assigns ."+sel.Sel.Name+" on an auxiliaryRuntime value")
				}
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "auxiliaryRuntime" {
					out = append(out, "assigns ."+sel.Sel.Name+" on an embedded auxiliaryRuntime")
				}
			}
		}
		return true
	})
	return out
}

// TestServedProjects_AuxEntryGuardCatchesBypasses proves the guard flags
// the bypasses it exists for, on synthetic source.
func TestServedProjects_AuxEntryGuardCatchesBypasses(t *testing.T) {
	cases := map[string]string{
		"non-empty literal":           `e := auxiliaryRuntime{Runtime: rt}; _ = e`,
		"empty literal then .Runtime": `e := auxiliaryRuntime{}; e.Runtime = rt; s.auxiliaryRuntimes[k] = e`,
		"var then .Runtime":           `var e auxiliaryRuntime; e.Runtime = rt; _ = e`,
		"map entry then .Runtime":     `e := s.auxiliaryRuntimes[k]; e.Runtime = rt; s.auxiliaryRuntimes[k] = e`,
		"range value then .Manager":   `for _, e := range s.sortedAuxiliaryRuntimes() { e.Manager = m }`,
		"embedded field":              `n.auxiliaryRuntime.Runtime = rt`,
		"wrapper of a fresh entry":    `e := namedAuxiliaryRuntime{identity: k, auxiliaryRuntime: auxiliaryRuntime{Runtime: rt}}; _ = e`,
	}
	for name, stmt := range cases {
		f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package p\nfunc f() {\n"+stmt+"\n}\n", 0)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		body := f.Decls[0].(*ast.FuncDecl).Body
		if got := auxEntryViolations(body, false); len(got) == 0 {
			t.Errorf("%s: guard found nothing", name)
		}
	}
	ok := `entries = append(entries, namedAuxiliaryRuntime{identity: k, auxiliaryRuntime: s.auxiliaryRuntimes[k]}); if x { return auxiliaryRuntime{}, false }; return nil, auxiliaryRuntime{}, nil`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package p\nfunc f() {\n"+ok+"\n}\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := auxEntryViolations(f.Decls[0].(*ast.FuncDecl).Body, false); len(got) != 0 {
		t.Errorf("zero-value returns or a wrapper of an existing entry flagged: %v", got)
	}
}

// TestServedProjects_RuntimeEntryPathsGoThroughInstall is a source guard
// over this package's non-test files:
//   - only newAuxiliaryRuntime builds an auxiliaryRuntime entry, and no code
//     installs a runtime into one any other way (auxEntryViolations);
//   - the default runtime is set only by New (the Server literal's runtime
//     field) and SwapRuntime (an assignment to .runtime), and both call
//     installProjectSet.
//
// A new entry path fails here until it installs the served-project set.
func TestServedProjects_RuntimeEntryPathsGoThroughInstall(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	auxBuilders := map[string]bool{}
	runtimeSetters := map[string]bool{}
	installers := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			where := name + ":" + fn.Name.Name
			for _, v := range auxEntryViolations(fn.Body, fn.Name.Name == "newAuxiliaryRuntime") {
				t.Errorf("%s %s; install runtimes into auxiliaryRuntime entries only through newAuxiliaryRuntime", where, v)
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CompositeLit:
					if id, ok := x.Type.(*ast.Ident); ok && id.Name == "auxiliaryRuntime" && len(x.Elts) > 0 {
						auxBuilders[where] = true
					}
					if id, ok := x.Type.(*ast.Ident); ok && id.Name == "Server" {
						for _, el := range x.Elts {
							if kv, ok := el.(*ast.KeyValueExpr); ok {
								if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "runtime" {
									runtimeSetters[where] = true
								}
							}
						}
					}
				case *ast.AssignStmt:
					for _, lhs := range x.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "runtime" {
							runtimeSetters[where] = true
						}
					}
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "installProjectSet" {
						installers[where] = true
					}
				}
				return true
			})
		}
	}

	for where := range auxBuilders {
		if !strings.HasSuffix(where, ":newAuxiliaryRuntime") {
			t.Errorf("%s builds an auxiliaryRuntime entry directly; use newAuxiliaryRuntime so the served-project set is installed", where)
		}
	}
	if len(auxBuilders) != 1 {
		t.Errorf("auxiliaryRuntime entry builders = %v, want exactly newAuxiliaryRuntime", auxBuilders)
	}
	for where := range runtimeSetters {
		if !installers[where] {
			t.Errorf("%s sets the broker's default runtime without calling installProjectSet", where)
		}
	}
	for _, want := range []string{"server.go:New", "server.go:SwapRuntime"} {
		if !runtimeSetters[want] {
			t.Errorf("guard found no default-runtime set in %s; update the guard if the entry path moved", want)
		}
	}
	if !installers["served_projects.go:newAuxiliaryRuntime"] {
		t.Error("newAuxiliaryRuntime does not call installProjectSet")
	}
}
