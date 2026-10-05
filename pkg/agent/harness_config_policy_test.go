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

// TestHarnessConfigPolicy_ResolveCallSitesAreHooked guards completeness: every
// harness construction from a harness-config (harness.Resolve,
// harness.NewContainerScriptHarness) in pkg/agent and pkg/runtimebroker must
// be one of the listed call sites, and the enclosing function must evaluate
// the policy (CheckHarnessConfigPolicy). A new call site fails this test
// until it is hooked and listed here.
func TestHarnessConfigPolicy_ResolveCallSitesAreHooked(t *testing.T) {
	allowed := map[string]bool{
		"pkg/agent/provision.go:ProvisionAgent": true,
		"pkg/agent/run.go:Start":                true,
	}
	resolvers := map[string]bool{"Resolve": true, "NewContainerScriptHarness": true}

	found := map[string]bool{}
	for _, dir := range []string{".", "../runtimebroker"} {
		pkgName := "pkg/" + filepath.Base(mustAbs(t, dir))
		fset := token.NewFileSet()
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var resolves, checks bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch f := call.Fun.(type) {
					case *ast.SelectorExpr:
						if x, ok := f.X.(*ast.Ident); ok && x.Name == "harness" && resolvers[f.Sel.Name] {
							resolves = true
						}
						if f.Sel.Name == "CheckHarnessConfigPolicy" {
							checks = true
						}
					case *ast.Ident:
						if f.Name == "CheckHarnessConfigPolicy" {
							checks = true
						}
					}
					return true
				})
				if !resolves {
					continue
				}
				site := pkgName + "/" + filepath.Base(path) + ":" + fn.Name.Name
				found[site] = true
				if !allowed[site] {
					t.Errorf("unlisted harness construction at %s: evaluate the harness-config policy there (CheckHarnessConfigPolicy) and list it in this test", site)
				} else if !checks {
					t.Errorf("%s constructs a harness without CheckHarnessConfigPolicy", site)
				}
			}
		}
	}
	for site := range allowed {
		if !found[site] {
			t.Errorf("listed call site %s no longer constructs a harness; update this test", site)
		}
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
