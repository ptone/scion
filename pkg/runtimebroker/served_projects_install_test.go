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
// it (substrate-lead ruling Q2): New, SwapRuntime and the three auxiliary
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
	entry := srv.newAuxiliaryRuntime(aux, auxMgr, config.RuntimeBuildRecord{})
	if got := aux.ProjectSetForTest(); got != srv.servedProjects {
		t.Errorf("newAuxiliaryRuntime: runtime ProjectSet = %v, want the broker's served-project set", got)
	}
	if entry.Runtime != aux || entry.Manager != auxMgr {
		t.Errorf("newAuxiliaryRuntime entry = %+v, want the given runtime and manager", entry)
	}
}

// TestServedProjects_RuntimeEntryPathsGoThroughInstall is a source guard
// over this package's non-test files:
//   - a non-empty auxiliaryRuntime composite literal appears only in
//     newAuxiliaryRuntime (the three auxiliary inserts call it);
//   - the default runtime is set only by New (the Server literal's runtime
//     field) and SwapRuntime (an assignment to .runtime), and both call
//     installProjectSet.
//
// A sixth entry path fails here until it installs the served-project set.
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
