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

package config

// Static guards for remote template fetching and template resolution.
//
// They parse source files only (go/parser); nothing is compiled or run. Each
// check is a function over parsed files so that the negative fixtures at the
// bottom can show that a bypass fails it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fetchCallTree is the closed call tree of remote template fetching. For
// each guarded symbol it lists the only declarations in this package that
// may reference it (as a call or as a value). A declaration is keyed by its
// function name ("Type.Method" for a method) or "var:<name>" for a
// package-level variable's initializer.
//
// Every fetch starts at FetchRemoteTemplate, whose first statement is
// ValidateRemoteFetchSource (checkValidatorFirst).
var fetchCallTree = map[string][]string{
	// Entry points below FetchRemoteTemplate.
	"fetchGitHubFolder": {"FetchRemoteTemplate"},
	"fetchArchive":      {"FetchRemoteTemplate"},
	"fetchRclone":       {"FetchRemoteTemplate"},

	// GitHub folder sources.
	"fetchGitHubTarball":    {"fetchGitHubFolder"},
	"resolveGitHubRef":      {"fetchGitHubFolder"},
	"gitLsRemote":           {"resolveGitHubRef", "SetGitLsRemoteForTest"},
	"execGitLsRemote":       {"var:gitLsRemote"},
	"sparseGitCheckout":     {"fetchGitHubFolder", "SetGitSparseCheckoutForTest"},
	"execSparseGitCheckout": {"var:sparseGitCheckout", "ProductionGitSparseCheckoutForTest"},
	"copyDirExcludingGit":   {"execSparseGitCheckout", "fetchGitHubTarball"},
	"githubCheckRedirect":   {"fetchGitHubTarball"},

	// Archive sources.
	"newArchiveHTTPClient":   {"fetchArchive"},
	"archiveCheckRedirect":   {"newArchiveHTTPClient", "githubCheckRedirect"},
	"resolvePublic":          {"newArchiveHTTPClient"},
	"archiveLookup":          {"resolvePublic"},
	"archiveDial":            {"newArchiveHTTPClient"},
	"archiveProxyDial":       {"newArchiveHTTPClient"},
	"archiveEnvProxy":        {"newArchiveHTTPClient"},
	"checkPublicDialAddress": {"var:archiveDial"},
	"extractZip":             {"fetchArchive"},
	"extractTarGz":           {"fetchArchive", "fetchGitHubTarball"},

	// rclone sources.
	"rcloneFetch":       {"fetchRclone", "SetRcloneFetchForTest"},
	"execRcloneFetch":   {"var:rcloneFetch"},
	"newRcloneSourceFs": {"execRcloneFetch"},
	"rcloneFetchConfig": {"newRcloneSourceFs"},
	"rcloneCopyLimited": {"execRcloneFetch"},
	"rcloneCopyObjects": {"rcloneCopyLimited"},
	"listRcloneSource":  {"rcloneCopyLimited"},

	// Template resolution entry points that accept absolute directories or
	// caller credentials: the local CLI's only (see templateFinderGuard for
	// references from other packages).
	"FindTemplate":                      {"FindTemplateInProjectPath", "GetTemplateChain", "CloneTemplate"},
	"FindTemplateWithContext":           {"FindTemplate"},
	"FindTemplateWithCallerCredentials": {},
	"findLocalTemplate":                 {"FindTemplateWithContext", "FindTemplateWithCallerCredentials"},
	"CallerCredentialFetchOptions":      {"FindTemplateWithCallerCredentials"},
	"TemplateFromDir":                   {"findLocalTemplate", "GetTemplateChainForDir"},
}

// primitiveAllow lists declarations outside the fetch tree that use a
// network, process or rclone primitive, with the reason each is not a remote
// template fetch. Any other user fails checkPrimitives.
var primitiveAllow = map[string]string{
	"gitTracked":       "runs a local `git ls-files` on a settings file; no network source",
	"var:lookPathFunc": "looks up a local container runtime binary on PATH",
	"var:runCheckFunc": "runs a local container runtime binary to check that it works",
}

// primitiveSelectors are the package members that count as a network,
// process or rclone primitive, keyed by import path. A nil set means every
// member of the package.
var primitiveSelectors = map[string]map[string]bool{
	"os/exec": nil,
	"net/http": set("Client", "Transport", "RoundTripper", "DefaultClient", "DefaultTransport",
		"Get", "Post", "PostForm", "Head", "NewRequest", "NewRequestWithContext"),
	"net": set("Dial", "DialTimeout", "Dialer", "Resolver", "DefaultResolver",
		"LookupHost", "LookupIP", "LookupIPAddr", "LookupNetIP", "LookupAddr", "Listen"),
	"github.com/rclone/rclone/fs":            set("Find", "NewFs"),
	"github.com/rclone/rclone/fs/operations": nil,
	"github.com/rclone/rclone/fs/walk":       nil,
	"github.com/rclone/rclone/fs/sync":       nil,
}

// credentialModeUsers are the declarations in this package that may name
// RemoteFetchCallerCredentials: its declaration, the only constructor that
// sets it, and the functions that implement the mode (comparisons only).
var credentialModeUsers = set("const:RemoteFetchCallerCredentials", "CallerCredentialFetchOptions",
	"newArchiveHTTPClient", "rcloneFetchConfig")

// credentialSetters are the declarations that may write the unexported
// credentials field of RemoteFetchOptions.
var credentialSetters = set("CallerCredentialFetchOptions")

// localOnlyExports are this package's exports that accept absolute template
// directories or caller credentials. Outside this package, only cmd/ may
// reference them.
var localOnlyExports = set("FindTemplate", "FindTemplateWithContext", "FindTemplateWithCallerCredentials",
	"GetTemplateChain", "TemplateFromDir", "CloneTemplate", "FindTemplateInScope",
	"CallerCredentialFetchOptions", "RemoteFetchCallerCredentials")

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// declInfo is what a declaration references.
type declInfo struct {
	idents     map[string]bool // unqualified identifiers
	primitives []string        // primitive members, as "path.Member"
}

// parsePackageFiles parses the non-test .go files of the package in dir.
func parsePackageFiles(t *testing.T, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		files = append(files, f)
	}
	require.NotEmpty(t, files)
	return files
}

func parseSource(t *testing.T, name, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	require.NoError(t, err)
	return f
}

// importNames maps each import's local name to its path.
func importNames(f *ast.File) map[string]string {
	m := map[string]string{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		m[name] = p
	}
	return m
}

// funcKey is a function declaration's key.
func funcKey(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if st, ok := t.(*ast.StarExpr); ok {
		t = st.X
	}
	if ix, ok := t.(*ast.IndexExpr); ok {
		t = ix.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// forEachDecl calls fn for every top-level declaration, with its key and the
// nodes to inspect.
func forEachDecl(files []*ast.File, fn func(f *ast.File, key string, nodes []ast.Node)) {
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				nodes := []ast.Node{d.Type}
				if d.Body != nil {
					nodes = append(nodes, d.Body)
				}
				fn(f, funcKey(d), nodes)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					prefix := "var:"
					if d.Tok == token.CONST {
						prefix = "const:"
					}
					for _, n := range vs.Names {
						var nodes []ast.Node
						if vs.Type != nil {
							nodes = append(nodes, vs.Type)
						}
						for _, v := range vs.Values {
							nodes = append(nodes, v)
						}
						fn(f, prefix+n.Name, nodes)
					}
				}
			}
		}
	}
}

// collectDecls returns what every declaration references.
func collectDecls(files []*ast.File) map[string]*declInfo {
	out := map[string]*declInfo{}
	forEachDecl(files, func(f *ast.File, key string, nodes []ast.Node) {
		imports := importNames(f)
		info := out[key]
		if info == nil {
			info = &declInfo{idents: map[string]bool{}}
			out[key] = info
		}
		var visit func(n ast.Node) bool
		visit = func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if x, ok := n.X.(*ast.Ident); ok {
					if path, isPkg := imports[x.Name]; isPkg {
						if members, guarded := primitiveSelectors[path]; guarded && (members == nil || members[n.Sel.Name]) {
							info.primitives = append(info.primitives, path+"."+n.Sel.Name)
						}
						return false
					}
				}
				// A method value or call named NewFs on any receiver
				// (e.g. an rclone backend's RegInfo) opens a backend.
				if n.Sel.Name == "NewFs" {
					info.primitives = append(info.primitives, "<value>.NewFs")
				}
				// The selected name is a field or method, not a
				// declaration; the receiver expression is walked with
				// the same rules (so http.DefaultClient.Get is seen).
				ast.Inspect(n.X, visit)
				return false
			case *ast.KeyValueExpr:
				// A struct literal key names a field, not a declaration.
				if _, ok := n.Key.(*ast.Ident); ok {
					ast.Inspect(n.Value, visit)
					return false
				}
			case *ast.Ident:
				info.idents[n.Name] = true
			}
			return true
		}
		for _, node := range nodes {
			ast.Inspect(node, visit)
		}
	})
	return out
}

// checkCallTree reports every reference to a guarded symbol from a
// declaration that the table does not allow, and every table entry whose
// symbol is no longer declared (so the table cannot go stale silently).
func checkCallTree(decls map[string]*declInfo, table map[string][]string) []string {
	var problems []string
	for sym, allowed := range table {
		if _, ok := decls[sym]; !ok {
			if _, ok := decls["var:"+sym]; !ok {
				problems = append(problems, "guarded symbol "+sym+" is not declared; update the table")
			}
		}
		ok := set(allowed...)
		for key, info := range decls {
			if key == sym || key == "var:"+sym {
				continue
			}
			if info.idents[sym] && !ok[key] {
				problems = append(problems, key+" references guarded "+sym)
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// checkPrimitives reports every declaration that uses a primitive and is
// neither a guarded symbol of the fetch tree nor allowed by allow.
func checkPrimitives(decls map[string]*declInfo, table map[string][]string, allow map[string]string) []string {
	var problems []string
	for key, info := range decls {
		if len(info.primitives) == 0 {
			continue
		}
		name := strings.TrimPrefix(key, "var:")
		if _, ok := table[name]; ok {
			continue
		}
		if _, ok := allow[key]; ok {
			continue
		}
		problems = append(problems, key+" uses "+strings.Join(info.primitives, ", ")+" outside the fetch tree")
	}
	sort.Strings(problems)
	return problems
}

// checkValidatorFirst reports a problem unless FetchRemoteTemplate's first
// statement is `if err := ValidateRemoteFetchSource(...); err != nil`.
func checkValidatorFirst(files []*ast.File) []string {
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name.Name != "FetchRemoteTemplate" || fd.Body == nil {
				continue
			}
			if len(fd.Body.List) == 0 {
				return []string{"FetchRemoteTemplate has no statements"}
			}
			ifs, ok := fd.Body.List[0].(*ast.IfStmt)
			if !ok {
				return []string{"FetchRemoteTemplate's first statement is not the source check"}
			}
			as, ok := ifs.Init.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 {
				return []string{"FetchRemoteTemplate's first statement is not the source check"}
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				return []string{"FetchRemoteTemplate's first statement is not the source check"}
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "ValidateRemoteFetchSource" {
				return []string{"FetchRemoteTemplate's first statement does not call ValidateRemoteFetchSource"}
			}
			return nil
		}
	}
	return []string{"FetchRemoteTemplate is not declared"}
}

// checkTestHooks reports every exported function named *ForTest whose first
// statement is not `if !testing.Testing() { panic(...) }`.
func checkTestHooks(files []*ast.File) []string {
	var problems []string
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !fd.Name.IsExported() || !strings.HasSuffix(fd.Name.Name, "ForTest") {
				continue
			}
			if !startsWithTestingGuard(fd) {
				problems = append(problems, fd.Name.Name+" does not panic outside a test binary")
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func startsWithTestingGuard(fd *ast.FuncDecl) bool {
	if fd.Body == nil || len(fd.Body.List) == 0 {
		return false
	}
	ifs, ok := fd.Body.List[0].(*ast.IfStmt)
	if !ok || ifs.Init != nil {
		return false
	}
	not, ok := ifs.Cond.(*ast.UnaryExpr)
	if !ok || not.Op != token.NOT {
		return false
	}
	call, ok := not.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Testing" {
		return false
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "testing" {
		return false
	}
	for _, st := range ifs.Body.List {
		if es, ok := st.(*ast.ExprStmt); ok {
			if c, ok := es.X.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "panic" {
					return true
				}
			}
		}
	}
	return false
}

// checkCredentialMode reports, inside this package: a reference to
// RemoteFetchCallerCredentials outside credentialModeUsers; a write of the
// unexported credentials field (keyed literal or assignment) outside
// credentialSetters; and a function with a RemoteFetchCredentials parameter
// that is not in the fetch tree.
func checkCredentialMode(files []*ast.File, decls map[string]*declInfo, table map[string][]string) []string {
	var problems []string
	for key, info := range decls {
		if info.idents["RemoteFetchCallerCredentials"] && !credentialModeUsers[key] {
			problems = append(problems, key+" names RemoteFetchCallerCredentials")
		}
	}
	forEachDecl(files, func(_ *ast.File, key string, nodes []ast.Node) {
		for _, node := range nodes {
			ast.Inspect(node, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.KeyValueExpr:
					if id, ok := n.Key.(*ast.Ident); ok && id.Name == "credentials" && !credentialSetters[key] {
						problems = append(problems, key+" sets the credential mode in a literal")
					}
				case *ast.AssignStmt:
					for _, lhs := range n.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "credentials" && !credentialSetters[key] {
							problems = append(problems, key+" assigns the credential mode")
						}
					}
				}
				return true
			})
		}
	})
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Type.Params == nil {
				continue
			}
			for _, p := range fd.Type.Params.List {
				if id, ok := p.Type.(*ast.Ident); ok && id.Name == "RemoteFetchCredentials" {
					if _, inTree := table[funcKey(fd)]; !inTree {
						problems = append(problems, funcKey(fd)+" takes a RemoteFetchCredentials parameter outside the fetch tree")
					}
				}
			}
		}
	}
	sort.Strings(problems)
	return problems
}

const configImportPath = "github.com/GoogleCloudPlatform/scion/pkg/config"

// checkLocalOnlyExports reports every reference to a localOnlyExports member
// of this package from a file outside cmd/. path is the file's path relative
// to the repository root.
func checkLocalOnlyExports(path string, f *ast.File) []string {
	if strings.HasPrefix(filepath.ToSlash(path), "cmd/") {
		return nil
	}
	var local string
	for name, p := range importNames(f) {
		if p == configImportPath {
			local = name
		}
	}
	if local == "" {
		return nil
	}
	var problems []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == local && localOnlyExports[sel.Sel.Name] {
			problems = append(problems, filepath.ToSlash(path)+" references config."+sel.Sel.Name+" (local CLI only)")
		}
		return true
	})
	return problems
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "go.mod"))
	require.NoError(t, err, "repository root not found")
	return dir
}

func TestFetchCallTreeIsClosed(t *testing.T) {
	files := parsePackageFiles(t, ".")
	decls := collectDecls(files)
	assert.Empty(t, checkCallTree(decls, fetchCallTree))
	assert.Empty(t, checkPrimitives(decls, fetchCallTree, primitiveAllow))
	assert.Empty(t, checkValidatorFirst(files))
}

func TestExportedTestHooksPanicOutsideTests(t *testing.T) {
	assert.Empty(t, checkTestHooks(parsePackageFiles(t, ".")))
}

func TestCredentialModeIsSetOnlyByTheCallerConstructor(t *testing.T) {
	files := parsePackageFiles(t, ".")
	assert.Empty(t, checkCredentialMode(files, collectDecls(files), fetchCallTree))
}

// Outside this package, only cmd/ may reference the exports that accept
// absolute template directories or caller credentials. Test files and the
// separate extras/ modules are not scanned.
func TestLocalOnlyTemplateExportsAreUsedOnlyByTheCLI(t *testing.T) {
	root := repoRoot(t)
	var problems []string
	scanned := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor", ".scion", "extras", "web", "docs-site":
				return filepath.SkipDir
			}
			if rel == filepath.Join("pkg", "config") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanned++
		problems = append(problems, checkLocalOnlyExports(rel, f)...)
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 100, "the scan must cover the repository")
	assert.Empty(t, problems)
}

// The negative fixtures: each bypass must fail the check that guards it.
func TestTemplateRefGuards_NegativeFixtures(t *testing.T) {
	pkgFiles := parsePackageFiles(t, ".")
	withFixture := func(src string) ([]*ast.File, map[string]*declInfo) {
		files := append(append([]*ast.File{}, pkgFiles...), parseSource(t, "fixture.go", src))
		return files, collectDecls(files)
	}

	t.Run("call outside the tree", func(t *testing.T) {
		_, decls := withFixture(`package config
func sneakyFetch() { _ = fetchArchive(nil, "", "", 0) }
`)
		assert.Contains(t, checkCallTree(decls, fetchCallTree), "sneakyFetch references guarded fetchArchive")
	})
	t.Run("seam reassigned outside the tree", func(t *testing.T) {
		_, decls := withFixture(`package config
func sneakySeam() { rcloneFetch = nil }
`)
		assert.Contains(t, checkCallTree(decls, fetchCallTree), "sneakySeam references guarded rcloneFetch")
	})
	t.Run("default HTTP client", func(t *testing.T) {
		_, decls := withFixture(`package config
import "net/http"
func sneakyHTTP() { _, _ = http.DefaultClient.Get("https://example.com/x.tgz") }
`)
		assert.NotEmpty(t, checkPrimitives(decls, fetchCallTree, primitiveAllow))
	})
	t.Run("default transport", func(t *testing.T) {
		_, decls := withFixture(`package config
import "net/http"
var sneakyTransport = http.DefaultTransport
`)
		assert.NotEmpty(t, checkPrimitives(decls, fetchCallTree, primitiveAllow))
	})
	t.Run("rclone backend opened directly", func(t *testing.T) {
		_, decls := withFixture(`package config
import (
	"context"
	"github.com/rclone/rclone/fs"
)
func sneakyRclone(ctx context.Context) {
	info, _ := fs.Find("gcs")
	_, _ = info.NewFs(ctx, ":gcs", "b/x", nil)
}
`)
		problems := checkPrimitives(decls, fetchCallTree, primitiveAllow)
		require.Len(t, problems, 1)
		assert.Contains(t, problems[0], "github.com/rclone/rclone/fs.Find")
		assert.Contains(t, problems[0], "<value>.NewFs")
	})
	t.Run("process started", func(t *testing.T) {
		_, decls := withFixture(`package config
import "os/exec"
func sneakyExec() { _ = exec.Command("git", "clone") }
`)
		assert.NotEmpty(t, checkPrimitives(decls, fetchCallTree, primitiveAllow))
	})
	t.Run("validator not first", func(t *testing.T) {
		f := parseSource(t, "fixture.go", `package config
import "context"
func FetchRemoteTemplate(ctx context.Context, uri string, opts RemoteFetchOptions) (string, error) {
	dir, err := remoteCacheDir()
	if err := ValidateRemoteFetchSource(uri); err != nil {
		return "", err
	}
	return dir, err
}
`)
		assert.NotEmpty(t, checkValidatorFirst([]*ast.File{f}))
	})
	t.Run("unguarded test hook", func(t *testing.T) {
		f := parseSource(t, "fixture.go", `package config
func SetSomethingForTest() (restore func()) { return func() {} }
`)
		assert.Equal(t, []string{"SetSomethingForTest does not panic outside a test binary"}, checkTestHooks([]*ast.File{f}))
	})
	t.Run("credential mode in a literal", func(t *testing.T) {
		files, decls := withFixture(`package config
func sneakyLiteral() RemoteFetchOptions { return RemoteFetchOptions{credentials: 1} }
`)
		assert.Contains(t, checkCredentialMode(files, decls, fetchCallTree), "sneakyLiteral sets the credential mode in a literal")
	})
	t.Run("credential mode assigned from a variable", func(t *testing.T) {
		files, decls := withFixture(`package config
func sneakyAssign(o *RemoteFetchOptions, mode RemoteFetchCredentials) { o.credentials = mode }
`)
		problems := checkCredentialMode(files, decls, fetchCallTree)
		assert.Contains(t, problems, "sneakyAssign assigns the credential mode")
		assert.Contains(t, problems, "sneakyAssign takes a RemoteFetchCredentials parameter outside the fetch tree")
	})
	t.Run("caller mode named", func(t *testing.T) {
		files, decls := withFixture(`package config
var sneakyMode = RemoteFetchCallerCredentials
`)
		assert.Contains(t, checkCredentialMode(files, decls, fetchCallTree), "var:sneakyMode names RemoteFetchCallerCredentials")
	})
	t.Run("local-only export used by the broker", func(t *testing.T) {
		f := parseSource(t, "pkg/runtimebroker/fixture.go", `package runtimebroker
import (
	"context"
	cfg "github.com/GoogleCloudPlatform/scion/pkg/config"
)
func sneaky(ctx context.Context) {
	_, _ = cfg.FindTemplateWithCallerCredentials(ctx, "x")
	_, _ = cfg.TemplateFromDir("/etc")
	_ = cfg.CallerCredentialFetchOptions("")
}
`)
		assert.Len(t, checkLocalOnlyExports("pkg/runtimebroker/fixture.go", f), 3)
		assert.Empty(t, checkLocalOnlyExports("cmd/fixture.go", f))
	})
}
