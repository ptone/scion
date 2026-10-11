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

package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
)

const categoryLogging = "moved function calls log/slog: log sourceLocation (file/function) and ERROR stack traces change; check log-based metrics and Error Reporting grouping"

// loggingPkgs are the packages whose calls record the caller's file and
// function (slog AddSource, log Lshortfile/Llongfile, Cloud Logging
// sourceLocation, Error Reporting stack grouping).
var loggingPkgs = map[string]bool{"log": true, "log/slog": true}

// loggingFindings warns once per moved function (or package-level var
// initialiser) that calls a package-level function of log or log/slog, or a
// method of log.Logger or slog.Logger: the logged source location and the
// stack traces of ERROR entries now name the target package and file, which
// moves log-based metrics filters and Error Reporting groups.
func (a *analysis) loggingFindings() {
	for _, f := range a.files {
		if !f.Moved {
			continue
		}
		typed := f.Included && !f.XTest
		var logImports map[string]string // external tests: import name -> path
		if !typed {
			logImports = map[string]string{}
			for _, spec := range f.AST.Imports {
				if p := importPath(spec); loggingPkgs[p] {
					if n := importName(spec); n != "_" && n != "." {
						logImports[n] = p
					}
				}
			}
			if len(logImports) == 0 {
				continue
			}
		}
		report := func(pos token.Pos, label string, bodies ...ast.Node) {
			var calls []string
			for _, body := range bodies {
				ast.Inspect(body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					var c string
					if typed {
						c = a.loggingCall(call)
					} else if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
						if x, ok := sel.X.(*ast.Ident); ok && logImports[x.Name] != "" {
							c = x.Name + "." + sel.Sel.Name
						}
					}
					if c != "" {
						calls = append(calls, c)
					}
					return true
				})
			}
			if len(calls) == 0 {
				return
			}
			sort.Strings(calls)
			a.plan.add(levelWarn, categoryLogging, a.posOf(pos), "%s calls %s", label, strings.Join(dedupStrings(calls), ", "))
		}
		for _, d := range f.AST.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				label := "func " + d.Name.Name
				if d.Recv != nil && len(d.Recv.List) == 1 {
					if id := embeddedIdent(d.Recv.List[0].Type); id != nil {
						label = "method " + id.Name + "." + d.Name.Name
					}
				}
				report(d.Name.Pos(), label, d.Body)
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, s := range d.Specs {
					vs := s.(*ast.ValueSpec)
					var values []ast.Node
					for _, v := range vs.Values {
						values = append(values, v)
					}
					report(vs.Pos(), "var "+identNames(vs.Names), values...)
				}
			}
		}
	}
}

// loggingCall names the logging function or method that call invokes, or "".
func (a *analysis) loggingCall(call *ast.CallExpr) string {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	var fn *types.Func
	if s, ok := a.info.Selections[sel]; ok {
		fn, _ = s.Obj().(*types.Func)
	} else {
		fn, _ = a.info.Uses[sel.Sel].(*types.Func)
	}
	if fn == nil || fn.Pkg() == nil || !loggingPkgs[fn.Pkg().Path()] {
		return ""
	}
	recv := fn.Signature().Recv()
	if recv == nil {
		return fn.Pkg().Name() + "." + fn.Name()
	}
	t := recv.Type()
	ptr := ""
	if p, ok := t.(*types.Pointer); ok {
		t, ptr = p.Elem(), "*"
	}
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Name() != "Logger" {
		return ""
	}
	return "(" + ptr + fn.Pkg().Name() + ".Logger)." + fn.Name()
}
