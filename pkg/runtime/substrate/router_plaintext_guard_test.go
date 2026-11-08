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

package substrate

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// plaintextRouterCtor is the test-only plaintext constructor production
// code must never refer to.
const plaintextRouterCtor = "NewRouterClient"

// substratePkgSuffix is this package's import path below the module path.
const substratePkgSuffix = "/pkg/runtime/substrate"

// plaintextRouterRefs returns a description of every reference to
// NewRouterClient in one parsed file: a call, a function value or an alias
// all count. pkgPath is the importing file's own package import path, so
// references inside this package (unqualified) are found too.
func plaintextRouterRefs(fset *token.FileSet, file *ast.File, pkgPath, substratePath string) []string {
	var refs []string
	report := func(n ast.Node) {
		refs = append(refs, fset.Position(n.Pos()).String())
	}

	if pkgPath == substratePath {
		// Inside the package: any identifier NewRouterClient other than
		// the declaration's own name.
		var declName *ast.Ident
		for _, d := range file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == plaintextRouterCtor {
				declName = fd.Name
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == plaintextRouterCtor && id != declName {
				report(id)
			}
			return true
		})
		return refs
	}

	// Elsewhere: find the local name(s) the substrate package is imported as.
	names := map[string]bool{}
	dot := false
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != substratePath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["substrate"] = true
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	if len(names) == 0 && !dot {
		return nil
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && names[id.Name] && x.Sel.Name == plaintextRouterCtor {
				report(x)
			}
		case *ast.Ident:
			if dot && x.Name == plaintextRouterCtor {
				report(x)
			}
		}
		return true
	})
	return refs
}

// routerClientConstructors are the only functions in this package allowed to
// build a RouterClient composite literal.
var routerClientConstructors = map[string]bool{"NewRouterClient": true, "newMTLSRouterClient": true}

// routerClientLiterals returns the position of every composite literal whose
// type expression names RouterClient (a value, a pointer element, or a slice
// or map element type) in one parsed file, outside the constructors.
func routerClientLiterals(fset *token.FileSet, file *ast.File, pkgPath, substratePath string) []string {
	names := map[string]bool{}
	local := pkgPath == substratePath
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != substratePath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["substrate"] = true
		case imp.Name.Name == ".":
			local = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	namesType := func(expr ast.Expr) bool {
		found := false
		ast.Inspect(expr, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && names[id.Name] && x.Sel.Name == "RouterClient" {
					found = true
				}
				return false
			case *ast.Ident:
				if local && x.Name == "RouterClient" {
					found = true
				}
			}
			return true
		})
		return found
	}
	var refs []string
	var visit func(n ast.Node, inCtor bool)
	visit = func(n ast.Node, inCtor bool) {
		ast.Inspect(n, func(m ast.Node) bool {
			if fd, ok := m.(*ast.FuncDecl); ok && m != n {
				visit(fd, local && fd.Recv == nil && routerClientConstructors[fd.Name.Name])
				return false
			}
			if cl, ok := m.(*ast.CompositeLit); ok && cl.Type != nil && !inCtor && namesType(cl.Type) {
				refs = append(refs, fset.Position(cl.Pos()).String())
			}
			return true
		})
	}
	visit(file, false)
	return refs
}

// moduleRoot walks up from the test's working directory to go.mod and
// returns the directory and the module path.
func moduleRoot(t *testing.T) (dir, module string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir = wd; ; {
		f, err := os.Open(filepath.Join(dir, "go.mod"))
		if err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if line := strings.TrimSpace(sc.Text()); strings.HasPrefix(line, "module ") {
					module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
					break
				}
			}
			_ = f.Close()
			if module == "" {
				t.Fatal("go.mod has no module line")
			}
			return dir, module
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// TestNoProductionCodeUsesPlaintextRouterClient fails when any non-test Go
// file under pkg/ or cmd/ refers to NewRouterClient. Production must build
// its router client with NewMTLSRouterClient.
func TestNoProductionCodeUsesPlaintextRouterClient(t *testing.T) {
	root, module := moduleRoot(t)
	substratePath := module + substratePkgSuffix

	fset := token.NewFileSet()
	scanned := 0
	sawRuntimeBuilder := false
	var refs, lits []string
	for _, top := range []string{"pkg", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name == "testdata" || name == "vendor" || (strings.HasPrefix(name, ".") && path != filepath.Join(root, top)) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			scanned++
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			pkgPath := module + "/" + filepath.ToSlash(rel)
			if filepath.ToSlash(filepath.Join(rel, filepath.Base(path))) == "pkg/runtime/substrate_runtime.go" {
				sawRuntimeBuilder = true
			}
			refs = append(refs, plaintextRouterRefs(fset, file, pkgPath, substratePath)...)
			lits = append(lits, routerClientLiterals(fset, file, pkgPath, substratePath)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}

	// The walk must have covered the tree, including the production builder.
	if scanned < 50 || !sawRuntimeBuilder {
		t.Fatalf("scanned %d files (production builder seen: %v); the walk did not cover pkg/ and cmd/", scanned, sawRuntimeBuilder)
	}
	for _, r := range refs {
		t.Errorf("non-test code refers to %s at %s; production must use NewMTLSRouterClient", plaintextRouterCtor, r)
	}
	for _, r := range lits {
		t.Errorf("non-test code builds a RouterClient literal at %s; use NewMTLSRouterClient", r)
	}
}

// TestRouterClientLiterals_Controls checks the literal detector: each
// positive source must be caught, each negative one must not.
func TestRouterClientLiterals_Controls(t *testing.T) {
	const module = "example.test/mod"
	substratePath := module + substratePkgSuffix
	parse := func(t *testing.T, src string) (*token.FileSet, *ast.File) {
		t.Helper()
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		return fset, file
	}
	positive := map[string]string{
		"value literal": `package p
import "example.test/mod/pkg/runtime/substrate"
var _ = substrate.RouterClient{Endpoint: "http://x"}`,
		"pointer literal, aliased import": `package p
import sub "example.test/mod/pkg/runtime/substrate"
var _ = &sub.RouterClient{}`,
		"slice element type": `package p
import "example.test/mod/pkg/runtime/substrate"
var _ = []*substrate.RouterClient{{}}`,
		"dot import": `package p
import . "example.test/mod/pkg/runtime/substrate"
var _ = RouterClient{}`,
	}
	for name, src := range positive {
		t.Run("positive "+name, func(t *testing.T) {
			fset, file := parse(t, src)
			if refs := routerClientLiterals(fset, file, module+"/pkg/p", substratePath); len(refs) == 0 {
				t.Error("detector missed the literal")
			}
		})
	}
	t.Run("positive inside the package, outside the constructors", func(t *testing.T) {
		fset, file := parse(t, `package substrate
type RouterClient struct{ Endpoint string }
func NewRouterClient(e string) *RouterClient { return &RouterClient{Endpoint: e} }
func newMTLSRouterClient() *RouterClient { return &RouterClient{} }
func other() *RouterClient { return &RouterClient{} }`)
		if refs := routerClientLiterals(fset, file, substratePath, substratePath); len(refs) != 1 {
			t.Errorf("refs = %v, want exactly the literal in other()", refs)
		}
	})
	negative := map[string]string{
		"pointer type only": `package p
import "example.test/mod/pkg/runtime/substrate"
var _ *substrate.RouterClient`,
		"another package's RouterClient": `package p
import other "example.test/mod/pkg/other"
var _ = other.RouterClient{}`,
	}
	for name, src := range negative {
		t.Run("negative "+name, func(t *testing.T) {
			fset, file := parse(t, src)
			if refs := routerClientLiterals(fset, file, module+"/pkg/p", substratePath); len(refs) != 0 {
				t.Errorf("detector flagged %v", refs)
			}
		})
	}
}

// TestPlaintextRouterRefs_Controls checks the detector itself: each
// positive source must be caught, each negative one must not.
func TestPlaintextRouterRefs_Controls(t *testing.T) {
	const module = "example.test/mod"
	substratePath := module + substratePkgSuffix

	positive := map[string]string{
		"call": `package p
import "example.test/mod/pkg/runtime/substrate"
var _ = substrate.NewRouterClient("http://x")`,
		"aliased import": `package p
import sub "example.test/mod/pkg/runtime/substrate"
func f() { _ = sub.NewRouterClient("http://x") }`,
		"function value": `package p
import "example.test/mod/pkg/runtime/substrate"
var ctor = substrate.NewRouterClient`,
		"dot import": `package p
import . "example.test/mod/pkg/runtime/substrate"
var ctor = NewRouterClient`,
	}
	for name, src := range positive {
		t.Run("positive "+name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if refs := plaintextRouterRefs(fset, file, module+"/pkg/p", substratePath); len(refs) == 0 {
				t.Error("detector missed the reference")
			}
		})
	}

	t.Run("positive inside the package", func(t *testing.T) {
		src := `package substrate
func NewRouterClient(string) *RouterClient { return nil }
var alias = NewRouterClient
type RouterClient struct{}`
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "r.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		if refs := plaintextRouterRefs(fset, file, substratePath, substratePath); len(refs) != 1 {
			t.Errorf("refs = %v, want exactly the alias (the declaration itself is not a use)", refs)
		}
	})

	negative := map[string]string{
		"mTLS constructor": `package p
import "example.test/mod/pkg/runtime/substrate"
var _, _ = substrate.NewMTLSRouterClient(nil, nil, substrate.RouterTLSConfig{})`,
		"another package's NewRouterClient": `package p
import other "example.test/mod/pkg/other"
var _ = other.NewRouterClient()`,
		"blank import": `package p
import _ "example.test/mod/pkg/runtime/substrate"`,
	}
	for name, src := range negative {
		t.Run("negative "+name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if refs := plaintextRouterRefs(fset, file, module+"/pkg/p", substratePath); len(refs) != 0 {
				t.Errorf("detector flagged %v", refs)
			}
		})
	}
}
