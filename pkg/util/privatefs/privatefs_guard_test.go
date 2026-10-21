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

package privatefs

// Static guards. They parse source with go/parser, so they run (and must
// pass) on every OS, Linux included.

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// repoRoot walks up from the package directory to the directory holding
// go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the package directory")
		}
		dir = parent
	}
}

// windowsTestPackages reads .github/windows-test-packages.txt: one package
// per line, '#' starts a comment.
func windowsTestPackages(t *testing.T, root string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(root, ".github", "windows-test-packages.txt"))
	if err != nil {
		t.Fatalf("read the Windows test allowlist: %v", err)
	}
	defer func() { _ = f.Close() }()
	var pkgs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimSuffix(sc.Text(), "\r"))
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" {
			pkgs = append(pkgs, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return pkgs
}

// skipOnLinkPath reports, per top-level function in src, whether it both
// creates a symlink or junction (os.Symlink, MustSymlink, MustJunction, or
// a "mklink" string) and can skip (t.Skip, t.Skipf, t.SkipNow, or a
// testing.Short() check). It also returns how many functions create links.
func skipOnLinkPath(fset *token.FileSet, file *ast.File) (offenders []string, linkFuncs int) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		links, skips := false, false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				switch x.Sel.Name {
				case "Symlink", "MustSymlink", "MustJunction":
					links = true
				case "Skip", "Skipf", "SkipNow":
					skips = true
				case "Short":
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "testing" {
						skips = true
					}
				}
			case *ast.Ident:
				if x.Name == "MustSymlink" || x.Name == "MustJunction" {
					links = true
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING && strings.Contains(strings.ToLower(x.Value), "mklink") {
					links = true
				}
			}
			return true
		})
		if links {
			linkFuncs++
			if skips {
				offenders = append(offenders, fset.Position(fn.Pos()).String()+" "+fn.Name.Name)
			}
		}
	}
	return offenders, linkFuncs
}

// TestNoSkipOnSymlinkPaths fails if any function in a test file of an
// allowlisted Windows package (or in internal/testutil/wintest) that
// creates a symlink or junction can also skip. A symlink test that skips
// silently on the runner reports green while testing nothing (security
// checklist §5, §7.4); the helpers fail instead.
func TestNoSkipOnSymlinkPaths(t *testing.T) {
	// Positive control: the detector flags a skip on a link path and
	// accepts a link path without one.
	const sample = `package p
func TestBad(t *testing.T) { if err := os.Symlink("a", "b"); err != nil { t.Skip("no privilege") } }
func TestShort(t *testing.T) { if testing.Short() { return }; wintest.MustJunction(t, "a", "b") }
func TestGood(t *testing.T) { wintest.MustSymlink(t, "a", "b") }
func TestUnrelatedSkip(t *testing.T) { t.Skip("no link here") }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "sample_test.go", sample, 0)
	if err != nil {
		t.Fatal(err)
	}
	if off, n := skipOnLinkPath(fset, f); len(off) != 2 || n != 3 {
		t.Fatalf("detector on the sample: offenders %v, link functions %d; want TestBad and TestShort, 3", off, n)
	}

	root := repoRoot(t)
	pkgs := windowsTestPackages(t, root)
	if len(pkgs) == 0 {
		t.Fatal("the Windows test allowlist is empty")
	}
	dirs := map[string]bool{filepath.Join(root, "internal", "testutil", "wintest"): true}
	self := false
	for _, p := range pkgs {
		if strings.Contains(p, "...") {
			t.Fatalf("allowlist line %q: wildcards are not allowed", p)
		}
		dirs[filepath.Join(root, filepath.FromSlash(p))] = true
		if filepath.ToSlash(p) == "./pkg/util/privatefs" {
			self = true
		}
	}
	if !self {
		t.Fatal("the Windows test allowlist does not include ./pkg/util/privatefs, which holds this guard")
	}
	var offenders []string
	linkFuncs := 0
	for dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no Go files in %s", dir)
		}
		for _, name := range files {
			if !strings.HasSuffix(name, "_test.go") && !strings.Contains(dir, "wintest") {
				continue
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			off, n := skipOnLinkPath(fset, f)
			offenders = append(offenders, off...)
			linkFuncs += n
		}
	}
	if linkFuncs == 0 {
		t.Fatal("no function creating a symlink or junction was found: the guard would pass vacuously")
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("functions that create a symlink or junction must fail, never skip (use wintest.MustSymlink/MustJunction):\n%s", strings.Join(offenders, "\n"))
	}
}

// accountNames are names a verifier must never compare or look up: they
// are localised, and a local group may be created with any name.
var accountNames = []string{"Administrators", "SYSTEM", "Everyone", "Users", "Administrator", "LocalSystem"}

// nameLookups returns the name-based account APIs called, and the account
// names used as string literals, in file.
func nameLookups(fset *token.FileSet, file *ast.File) []string {
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			name := x.Sel.Name
			if name == "LookupAccountName" || name == "LookupAccountSid" || name == "LookupSID" ||
				name == "LookupAccount" || strings.HasPrefix(name, "NetLocalGroup") || strings.HasPrefix(name, "NetUser") {
				found = append(found, fset.Position(x.Pos()).String()+" "+name)
			}
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(x.Value)
			if err != nil {
				return true
			}
			upper := strings.ToUpper(v)
			if strings.Contains(upper, `BUILTIN\`) || strings.Contains(upper, `NT AUTHORITY\`) {
				found = append(found, fset.Position(x.Pos()).String()+" "+x.Value)
			}
			for _, a := range accountNames {
				if strings.EqualFold(strings.TrimSpace(v), a) {
					found = append(found, fset.Position(x.Pos()).String()+" "+x.Value)
				}
			}
		}
		return true
	})
	return found
}

// TestVerifierUsesNoNameLookup fails if the Windows create or verify code
// looks up or compares account names. SIDs come only from the token,
// CreateWellKnownSid and StringToSid (security lead ruling C).
func TestVerifierUsesNoNameLookup(t *testing.T) {
	// Positive control: the detector flags a lookup and a name literal.
	const sample = `package p
func f() { windows.LookupSID("", "Administrators"); _ = name == "BUILTIN\\Users" }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "sample.go", sample, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := nameLookups(fset, f); len(got) != 3 {
		t.Fatalf("detector on the sample found %v, want the call and both literals", got)
	}

	fset = token.NewFileSet()
	f, err = parser.ParseFile(fset, "privatefs_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := nameLookups(fset, f); len(got) > 0 {
		t.Fatalf("privatefs_windows.go uses account names:\n%s", strings.Join(got, "\n"))
	}
}
