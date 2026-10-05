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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

const (
	policyTestScripted = "harness: generic\nimage: scion-base:test\nuser: scion\nprovisioner:\n  type: container-script\n  interface_version: 1\n  command: [\"python3\", \"/home/scion/.scion/harness/provision.py\"]\n"
	policyTestDecl     = "harness: generic\nimage: scion-base:test\nuser: scion\n"
)

// policyTestEnv is an initialized machine and project for harness-config
// policy tests. root is the project root; scion is <root>/.scion.
type policyTestEnv struct {
	root, scion string
}

func newPolicyTestEnv(t *testing.T) *policyTestEnv {
	t.Helper()
	mockRuntimeForTest(t)
	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	t.Setenv("HOME", tmp)
	if err := config.InitMachine(getTestHarnesses()); err != nil {
		t.Fatalf("InitMachine: %v", err)
	}
	root := filepath.Join(tmp, "project")
	scion := filepath.Join(root, ".scion")
	if err := config.InitProject(scion, getTestHarnesses()); err != nil {
		t.Fatalf("InitProject: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	return &policyTestEnv{root: root, scion: scion}
}

func writePolicyHC(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "provisioner:") {
		if err := os.WriteFile(filepath.Join(dir, "provision.py"), []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// projectHC writes harness-configs/<name> in the project's resolved dir.
func (e *policyTestEnv) projectHC(t *testing.T, name, body string) {
	writePolicyHC(t, filepath.Join(e.scion, "harness-configs", name), body)
}

// template writes templates/<name>/scion-agent.yaml in the project.
func (e *policyTestEnv) template(t *testing.T, name, agentYAML string) string {
	t.Helper()
	dir := filepath.Join(e.scion, "templates", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scion-agent.yaml"), []byte(agentYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// recordingPolicy records each evaluation and refuses container-script
// entries when refuse is set.
type recordingPolicy struct {
	mu     sync.Mutex
	refuse bool
	calls  []string // "<name>:<scripted|declarative>"
}

var errTestPolicyRefusal = errors.New("test policy refusal")

func (p *recordingPolicy) fn(name string, entry config.HarnessConfigEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	kind := "declarative"
	if entry.Provisioner != nil {
		kind = "scripted"
	}
	p.calls = append(p.calls, name+":"+kind)
	if p.refuse && entry.Provisioner != nil {
		return errTestPolicyRefusal
	}
	return nil
}

func (p *recordingPolicy) ctx() context.Context {
	return config.ContextWithHarnessConfigPolicy(context.Background(), p.fn)
}

func (p *recordingPolicy) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func assertPolicyRefused(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrHarnessConfigPolicy) || !errors.Is(err, errTestPolicyRefusal) {
		t.Fatalf("expected a refusal wrapping ErrHarnessConfigPolicy and the policy's error, got %v", err)
	}
}

func policyTestManager(runs *int) Manager {
	return NewManager(&runtime.MockRuntime{
		ListFunc: func(ctx context.Context, labelFilter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{}, nil
		},
		RunFunc: func(ctx context.Context, cfg runtime.RunConfig) (string, error) {
			if runs != nil {
				*runs++
			}
			return "mock-id", nil
		},
	})
}

// With no policy attached (the solo CLI path), a container-script
// harness-config launches as usual and its bundle is staged.
func TestHarnessConfigPolicy_NoPolicyLaunchesContainerScript(t *testing.T) {
	e := newPolicyTestEnv(t)
	e.projectHC(t, "hc-scripted", policyTestScripted)
	runs := 0
	if _, err := policyTestManager(&runs).Start(context.Background(), api.StartOptions{
		Name: "solo", ProjectPath: e.scion, HarnessConfig: "hc-scripted", NoAuth: true,
	}); err != nil {
		t.Fatalf("Start without a policy: %v", err)
	}
	if runs != 1 {
		t.Errorf("expected the container to run once, got %d", runs)
	}
	staged := filepath.Join(config.GetAgentHomePath(e.scion, "solo"), ".scion", "harness", "provision.py")
	if _, err := os.Stat(staged); err != nil {
		t.Errorf("container-script bundle not staged: %v", err)
	}
}

// The policy is evaluated at every point launch resolves the harness-config:
// template and harness-config resolution, ProvisionAgent's harness
// construction, and Start's harness construction.
func TestHarnessConfigPolicy_EvaluatedAtEveryResolutionPoint(t *testing.T) {
	e := newPolicyTestEnv(t)
	e.projectHC(t, "hc-decl", policyTestDecl)

	p := &recordingPolicy{}
	if _, err := policyTestManager(nil).Start(p.ctx(), api.StartOptions{
		Name: "all-points", ProjectPath: e.scion, HarnessConfig: "hc-decl", NoAuth: true,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := []string{"hc-decl:declarative", "hc-decl:declarative", "hc-decl:declarative"}
	if got := p.recorded(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("policy evaluations = %v, want %v (resolution, provisioning harness, Start harness)", got, want)
	}
}

// C1a: the staged bundle comes from the resolved project dir.
func TestHarnessConfigPolicy_StagedBundleFromResolvedProjectDir(t *testing.T) {
	e := newPolicyTestEnv(t)
	writePolicyHC(t, filepath.Join(e.scion, "harness-configs", "hc"), policyTestScripted+"# copy: resolved\n")
	writePolicyHC(t, filepath.Join(e.root, "harness-configs", "hc"), policyTestScripted+"# copy: raw\n")

	if _, _, _, err := ProvisionAgent(context.Background(), "staged", "", "", "hc", e.root, "", "", "", ""); err != nil {
		t.Fatalf("ProvisionAgent: %v", err)
	}
	staged, err := os.ReadFile(filepath.Join(config.GetAgentHomePath(e.scion, "staged"), ".scion", "harness", "config.yaml"))
	if err != nil {
		t.Fatalf("read staged config.yaml: %v", err)
	}
	if !strings.Contains(string(staged), "# copy: resolved") {
		t.Errorf("staged bundle is not the <root>/.scion copy:\n%s", staged)
	}
}

// C1b: with no template on the request, launch resolves the default
// template's chain; a harness-config bundled there is what runs, and the
// policy evaluates it.
func TestHarnessConfigPolicy_DefaultTemplateBundledConfig(t *testing.T) {
	e := newPolicyTestEnv(t)
	e.projectHC(t, "hc", policyTestDecl)
	chain, err := config.GetTemplateChainInProject("default", e.scion)
	if err != nil || len(chain) == 0 {
		t.Fatalf("default template chain: %v", err)
	}
	writePolicyHC(t, filepath.Join(chain[len(chain)-1].Path, "harness-configs", "hc"), policyTestScripted)

	p := &recordingPolicy{refuse: true}
	assertPolicyRefused(t, PreflightResolve(p.ctx(), api.StartOptions{Name: "c1b", ProjectPath: e.scion, HarnessConfig: "hc"}))
}

// C2: the template names the harness-config; the request does not.
func TestHarnessConfigPolicy_TemplateNamedConfig(t *testing.T) {
	e := newPolicyTestEnv(t)
	e.projectHC(t, "hc-scripted", policyTestScripted)
	e.template(t, "tplx", "harness_config: hc-scripted\n")

	p := &recordingPolicy{refuse: true}
	assertPolicyRefused(t, PreflightResolve(p.ctx(), api.StartOptions{Name: "c2", ProjectPath: e.scion, Template: "tplx"}))
	if got := p.recorded(); len(got) == 0 || got[0] != "hc-scripted:scripted" {
		t.Errorf("policy evaluated %v, want hc-scripted first", got)
	}
}

// C3: the dispatch's inline config names the harness-config.
func TestHarnessConfigPolicy_InlineConfigNamedConfig(t *testing.T) {
	e := newPolicyTestEnv(t)
	e.projectHC(t, "hc-scripted", policyTestScripted)

	p := &recordingPolicy{refuse: true}
	err := PreflightResolve(p.ctx(), api.StartOptions{
		Name: "c3", ProjectPath: e.scion,
		InlineConfig: &api.ScionConfig{HarnessConfig: "hc-scripted"},
	})
	assertPolicyRefused(t, err)
}

// provisionExisting provisions agent name with harness-config hcName (no
// policy), leaving an existing agent for start/restart tests.
func (e *policyTestEnv) provisionExisting(t *testing.T, name, hcName string) {
	t.Helper()
	if _, _, _, err := ProvisionAgent(context.Background(), name, "", "", hcName, e.scion, "", "", "", ""); err != nil {
		t.Fatalf("ProvisionAgent: %v", err)
	}
}

// setStoredHarnessConfig rewrites scion-agent.json's harness_config, as a
// start's inline config update (applyInlineConfigUpdate) does.
func (e *policyTestEnv) setStoredHarnessConfig(t *testing.T, name, hcName string) {
	t.Helper()
	path := filepath.Join(e.scion, "agents", name, "scion-agent.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["harness_config"] = hcName
	out, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// S2: a start whose request names no harness-config uses the stored config,
// as updated by the start's inline config; the policy evaluates that name.
func TestHarnessConfigPolicy_StartUsesStoredConfig(t *testing.T) {
	e := newPolicyTestEnv(t)
	e.projectHC(t, "hc-decl", policyTestDecl)
	e.projectHC(t, "hc-scripted", policyTestScripted)
	e.provisionExisting(t, "s2", "hc-decl")
	e.setStoredHarnessConfig(t, "s2", "hc-scripted")

	runs := 0
	p := &recordingPolicy{refuse: true}
	_, err := policyTestManager(&runs).Start(p.ctx(), api.StartOptions{Name: "s2", ProjectPath: e.scion, NoAuth: true})
	assertPolicyRefused(t, err)
	if runs != 0 {
		t.Error("the container must not run when the policy refuses")
	}
}

// R1: a restart-style start (no harness-config on the request) of an agent
// whose agent-info.json carries no harness-config uses scion-agent.json's.
func TestHarnessConfigPolicy_RestartUsesStoredConfigWithoutAgentInfo(t *testing.T) {
	e := newPolicyTestEnv(t)
	e.projectHC(t, "hc-scripted", policyTestScripted)
	e.provisionExisting(t, "r1", "hc-scripted")
	if err := os.Remove(filepath.Join(config.GetAgentHomePath(e.scion, "r1"), "agent-info.json")); err != nil {
		t.Fatal(err)
	}

	runs := 0
	p := &recordingPolicy{refuse: true}
	_, err := policyTestManager(&runs).Start(p.ctx(), api.StartOptions{Name: "r1", ProjectPath: e.scion, NoAuth: true})
	assertPolicyRefused(t, err)
	if runs != 0 {
		t.Error("the container must not run when the policy refuses")
	}
}

// S3/R2: starting an agent whose directory does not exist provisions it
// through the create resolution, which evaluates the policy.
func TestHarnessConfigPolicy_StartOfMissingAgentProvisions(t *testing.T) {
	e := newPolicyTestEnv(t)
	e.projectHC(t, "hc-scripted", policyTestScripted)
	e.template(t, "tplx", "harness_config: hc-scripted\n")

	runs := 0
	p := &recordingPolicy{refuse: true}
	_, err := policyTestManager(&runs).Start(p.ctx(), api.StartOptions{Name: "s3", ProjectPath: e.scion, Template: "tplx", NoAuth: true})
	assertPolicyRefused(t, err)
	if runs != 0 {
		t.Error("the container must not run when the policy refuses")
	}
}

const harnessImportPath = "github.com/GoogleCloudPlatform/scion/pkg/harness"

// TestHarnessConfigPolicy_ResolveCallSitesAreHooked guards completeness. It
// scans every non-test Go file under pkg/ and cmd/ (except pkg/harness
// itself) for harness constructions from a harness-config: calls to
// pkg/harness's Resolve or NewContainerScriptHarness, found by import path,
// so an aliased or dot import is covered. Each one must be a listed call site
// (NewContainerScriptHarness has none outside pkg/harness), and the policy
// check must be tied to it: a CheckHarnessConfigPolicy call later in the same
// function whose entry argument is the constructed harness's .Config (the
// variable the Resolve result is assigned to). resolveTemplateAndHarnessConfig
// must likewise evaluate EffectiveConfig of the dir it resolves.
//
// The tie is syntactic (position and argument), not control-flow: it does not
// prove the check is on every path, which the behavioural tests in this file
// cover. A new call site fails this test until it is hooked and listed.
func TestHarnessConfigPolicy_ResolveCallSitesAreHooked(t *testing.T) {
	allowed := map[string]bool{
		"pkg/agent/provision.go:ProvisionAgent": true,
		"pkg/agent/run.go:Start":                true,
	}
	root := mustAbs(t, filepath.Join("..", ".."))
	found := map[string]bool{}
	sawTemplateResolution := false

	for _, top := range []string{"pkg", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path == filepath.Join(root, "pkg", "harness") || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			local, dot := harnessImportName(file)
			if local == "" && !dot && !strings.HasPrefix(rel, filepath.Join("pkg", "agent")+string(filepath.Separator)) {
				return nil
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				site := filepath.ToSlash(rel) + ":" + fn.Name.Name
				checkFunctionHooks(t, fn, site, local, dot, allowed, found)
				if fn.Name.Name == "resolveTemplateAndHarnessConfig" {
					sawTemplateResolution = true
					checkTemplateResolutionHooked(t, fn)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for site := range allowed {
		if !found[site] {
			t.Errorf("listed call site %s does not construct a harness; update this test", site)
		}
	}
	if !sawTemplateResolution {
		t.Error("resolveTemplateAndHarnessConfig not found; update this test")
	}
}

// harnessImportName returns the identifier file uses for pkg/harness ("" if
// it does not import it) and whether it is dot-imported.
func harnessImportName(file *ast.File) (string, bool) {
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != harnessImportPath {
			continue
		}
		if imp.Name == nil {
			return "harness", false
		}
		switch imp.Name.Name {
		case ".":
			return "", true
		case "_":
			return "", false
		default:
			return imp.Name.Name, false
		}
	}
	return "", false
}

// isHarnessCall reports whether call invokes pkg/harness's function name,
// given how the file imports pkg/harness.
func isHarnessCall(call *ast.CallExpr, local string, dot bool, names ...string) (string, bool) {
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok && local != "" && x.Name == local {
			for _, n := range names {
				if f.Sel.Name == n {
					return n, true
				}
			}
		}
	case *ast.Ident:
		if dot {
			for _, n := range names {
				if f.Name == n {
					return n, true
				}
			}
		}
	}
	return "", false
}

func isPolicyCheck(call *ast.CallExpr) bool {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name == "CheckHarnessConfigPolicy"
	case *ast.SelectorExpr:
		return f.Sel.Name == "CheckHarnessConfigPolicy"
	}
	return false
}

// assignedVar returns the first left-hand identifier of the assignment whose
// right-hand side is call, or "".
func assignedVar(body *ast.BlockStmt, call *ast.CallExpr) string {
	name := ""
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || as.Rhs[0] != call || len(as.Lhs) == 0 {
			return true
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			name = id.Name
		}
		return false
	})
	return name
}

func checkFunctionHooks(t *testing.T, fn *ast.FuncDecl, site, local string, dot bool, allowed, found map[string]bool) {
	t.Helper()
	var checks []*ast.CallExpr
	type construction struct {
		call *ast.CallExpr
		name string
	}
	var constructions []construction
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isPolicyCheck(call) {
			checks = append(checks, call)
		}
		if name, ok := isHarnessCall(call, local, dot, "Resolve", "NewContainerScriptHarness"); ok {
			constructions = append(constructions, construction{call, name})
		}
		return true
	})
	for _, c := range constructions {
		found[site] = true
		if c.name == "NewContainerScriptHarness" {
			t.Errorf("%s constructs a container-script harness directly; build it through harness.Resolve at a hooked call site", site)
			continue
		}
		if !allowed[site] {
			t.Errorf("unlisted harness construction at %s: evaluate the harness-config policy there (CheckHarnessConfigPolicy) and list it in this test", site)
			continue
		}
		v := assignedVar(fn.Body, c.call)
		var tied *ast.CallExpr
		for _, chk := range checks {
			if chk.Pos() <= c.call.End() || len(chk.Args) != 3 {
				continue
			}
			if sel, ok := chk.Args[2].(*ast.SelectorExpr); ok && sel.Sel.Name == "Config" {
				if x, ok := sel.X.(*ast.Ident); ok && v != "" && x.Name == v && tied == nil {
					tied = chk
				}
			}
		}
		if tied == nil {
			t.Errorf("%s: harness.Resolve result %q is not followed by CheckHarnessConfigPolicy(ctx, name, %s.Config)", site, v, v)
			continue
		}
		checkWrapperStagedOnlyWhenAllowed(t, fn, site, tied)
	}
}

// checkWrapperStagedOnlyWhenAllowed requires, at a hooked call site, that
// the harness is provisioned (which is where a container-script harness
// stages its provisioner wrapper) only after the policy check, and that a
// stale wrapper is cleared for a non-container-script harness
// (clearProvisionHookUnlessContainerScript) after the check. Start must also
// route a Resolve error through harnessAfterResolveError.
func checkWrapperStagedOnlyWhenAllowed(t *testing.T, fn *ast.FuncDecl, site string, check *ast.CallExpr) {
	t.Helper()
	var cleared, routedErr bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			switch f.Name {
			case "clearProvisionHookUnlessContainerScript":
				if call.Pos() > check.End() {
					cleared = true
				}
			case "harnessAfterResolveError":
				routedErr = true
			}
		case *ast.SelectorExpr:
			if x, ok := f.X.(*ast.Ident); ok && x.Name == "h" && f.Sel.Name == "Provision" && call.Pos() < check.End() {
				t.Errorf("%s provisions the harness (staging any provisioner wrapper) before the policy check", site)
			}
		}
		return true
	})
	if !cleared {
		t.Errorf("%s does not clear a stale provisioner wrapper (clearProvisionHookUnlessContainerScript) after the policy check", site)
	}
	if fn.Name.Name == "Start" && !routedErr {
		t.Errorf("%s does not route a harness.Resolve error through harnessAfterResolveError", site)
	}
}

// TestHarnessConfigPolicy_WrapperWrittenOnlyByContainerScriptProvision
// guards that pkg/harness stages the provisioner wrapper only from
// (*ContainerScriptHarness).Provision, the harness a hooked call site builds
// after the policy allowed it.
func TestHarnessConfigPolicy_WrapperWrittenOnlyByContainerScriptProvision(t *testing.T) {
	dir := filepath.Join("..", "harness")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var callers []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "writeHookWrapper" {
						recv := ""
						if fn.Recv != nil && len(fn.Recv.List) == 1 {
							if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
								if id, ok := star.X.(*ast.Ident); ok {
									recv = id.Name
								}
							}
						}
						callers = append(callers, recv+"."+fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	if len(callers) != 1 || callers[0] != "ContainerScriptHarness.Provision" {
		t.Errorf("writeHookWrapper callers = %v, want only ContainerScriptHarness.Provision", callers)
	}
}

// checkTemplateResolutionHooked requires resolveTemplateAndHarnessConfig to
// evaluate the policy, after resolving the harness-config dir, on
// EffectiveConfig of that dir.
func checkTemplateResolutionHooked(t *testing.T, fn *ast.FuncDecl) {
	t.Helper()
	var resolveCall *ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "resolveHarnessConfigDir" {
				resolveCall = call
			}
		}
		return true
	})
	if resolveCall == nil {
		t.Fatal("resolveTemplateAndHarnessConfig no longer calls resolveHarnessConfigDir; update this test")
	}
	dirVar := assignedVar(fn.Body, resolveCall)
	tied := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isPolicyCheck(call) || call.Pos() <= resolveCall.End() || len(call.Args) != 3 {
			return true
		}
		inner, ok := call.Args[2].(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := inner.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "EffectiveConfig" {
			return true
		}
		for _, a := range inner.Args {
			if id, ok := a.(*ast.Ident); ok && id.Name == dirVar {
				tied = true
			}
		}
		return true
	})
	if !tied {
		t.Errorf("resolveTemplateAndHarnessConfig does not evaluate CheckHarnessConfigPolicy on EffectiveConfig(..., %s, ...) after resolving it", dirVar)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func wrapperPath(e *policyTestEnv, agentName string) string {
	return filepath.Join(config.GetAgentHomePath(e.scion, agentName), ".scion", "hooks", "pre-start.d", "20-harness-provision")
}

func wrapperStaged(e *policyTestEnv, agentName string) bool {
	_, err := os.Stat(wrapperPath(e, agentName))
	return err == nil
}

// (iii) With no policy (the solo CLI), a container-script launch stages its
// provisioner wrapper as before.
func TestHarnessConfigPolicy_NoPolicyStagesWrapper(t *testing.T) {
	e := newPolicyTestEnv(t)
	e.projectHC(t, "hc-scripted", policyTestScripted)
	if _, err := policyTestManager(nil).Start(context.Background(), api.StartOptions{
		Name: "solo-wrap", ProjectPath: e.scion, HarnessConfig: "hc-scripted", NoAuth: true,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !wrapperStaged(e, "solo-wrap") {
		t.Error("container-script launch without a policy must stage the provisioner wrapper")
	}
}

// (ii) A relaunch whose resolved harness is not container-script clears a
// wrapper staged by an earlier container-script run, on start and on a
// restart-style relaunch, with or without a policy.
func TestHarnessConfigPolicy_NonContainerScriptRelaunchClearsWrapper(t *testing.T) {
	for _, tc := range []struct {
		name      string
		withPol   bool
		relaunch2 bool // relaunch a second time (restart after a start)
	}{
		{"start, policy attached", true, false},
		{"restart, policy attached", true, true},
		{"start, no policy", false, false},
		{"restart, no policy", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPolicyTestEnv(t)
			e.projectHC(t, "hc-scripted", policyTestScripted)
			e.projectHC(t, "hc-decl", policyTestDecl)
			mgr := policyTestManager(nil)
			if _, err := mgr.Start(context.Background(), api.StartOptions{Name: "relaunch", ProjectPath: e.scion, HarnessConfig: "hc-scripted", NoAuth: true}); err != nil {
				t.Fatalf("first Start: %v", err)
			}
			if !wrapperStaged(e, "relaunch") {
				t.Fatal("fixture: wrapper should be staged by the container-script run")
			}
			ctx := context.Background()
			if tc.withPol {
				ctx = (&recordingPolicy{refuse: true}).ctx()
			}
			opts := api.StartOptions{Name: "relaunch", ProjectPath: e.scion, HarnessConfig: "hc-decl", NoAuth: true}
			if _, err := mgr.Start(ctx, opts); err != nil {
				t.Fatalf("relaunch: %v", err)
			}
			if tc.relaunch2 {
				if _, err := mgr.Start(ctx, opts); err != nil {
					t.Fatalf("second relaunch: %v", err)
				}
			}
			if wrapperStaged(e, "relaunch") {
				t.Error("a non-container-script relaunch must clear the stale provisioner wrapper")
			}
		})
	}
}

// (i) After a harness.Resolve error, with a policy attached and a wrapper
// staged, Start refuses; with no policy, or nothing staged, it falls back to
// harness.New as before. (harness.Resolve does not currently fail for a
// named harness-config Start resolves, so the decision is exercised
// directly; the call-site guard requires Start's error branch to use it.)
func TestHarnessConfigPolicy_ResolveErrorWithStagedWrapper(t *testing.T) {
	home := t.TempDir()
	stage := func() {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(home, ".scion", "hooks", "pre-start.d"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".scion", "hooks", "pre-start.d", "20-harness-provision"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resolveErr := errors.New("resolve failed")
	policyCtx := (&recordingPolicy{refuse: true}).ctx()

	stage()
	if _, err := harnessAfterResolveError(policyCtx, home, "hc", "generic", resolveErr); !errors.Is(err, ErrHarnessConfigPolicy) || !errors.Is(err, ErrHarnessConfigNotEvaluated) {
		t.Errorf("policy attached + wrapper staged: expected refusal, got %v", err)
	}

	h, err := harnessAfterResolveError(context.Background(), home, "hc", "generic", resolveErr)
	if err != nil || h == nil {
		t.Errorf("no policy: expected the harness.New fallback, got h=%v err=%v", h, err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".scion", "hooks", "pre-start.d", "20-harness-provision")); statErr != nil {
		t.Errorf("no policy: the fallback must leave the agent home as it is: %v", statErr)
	}

	if err := os.Remove(filepath.Join(home, ".scion", "hooks", "pre-start.d", "20-harness-provision")); err != nil {
		t.Fatal(err)
	}
	if h, err := harnessAfterResolveError(policyCtx, home, "hc", "generic", resolveErr); err != nil || h == nil {
		t.Errorf("policy attached, nothing staged: expected the fallback, got h=%v err=%v", h, err)
	}
}
