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
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const resolverTestSettings = `
schema_version: "1"
profiles:
  default:
    runtime: mock
`

// TestEnvGather_HydratedAuthWinsOverOnDiskHarnessOnly covers
// ptone/scion#618/#619: an on-disk dir of the same name declaring only
// `harness: claude` (no auth block, no auth type) must not shadow the
// hydrated hub bundle's vertex-ai auth metadata. Launch uses the hydrated
// copy, so required keys come from its vertex-ai auth type.
func TestEnvGather_HydratedAuthWinsOverOnDiskHarnessOnly(t *testing.T) {
	srv, _, projectDir := newTestServerWithHarnessConfig(t, "claude",
		"harness: claude\nimage: test-image\nuser: scion\n", resolverTestSettings)
	hydratedDir := filepath.Join(t.TempDir(), "claude")
	writeHarnessConfigDirAt(t, hydratedDir,
		"harness: claude\nimage: test-image\nuser: scion\nauth_selected_type: vertex-ai\n"+claudeAuthBlock)

	var req CreateAgentRequest
	req.ProjectPath = projectDir
	req.Config = &CreateAgentConfig{HarnessConfig: "claude", Profile: "default"}
	required, _, _, _ := srv.extractRequiredEnvKeys(req, "", hydratedDir)
	if !slices.Contains(required, "GOOGLE_CLOUD_PROJECT") {
		t.Errorf("hydrated vertex-ai auth was shadowed by the on-disk harness-only dir: required=%v", required)
	}
	if slices.Contains(required, "ANTHROPIC_API_KEY") {
		t.Errorf("api-key requirement leaked from the on-disk copy: required=%v", required)
	}
}

// TestEnvGather_OnDiskAuthIgnoredWhenHydratedExists is the reverse: the
// on-disk dir declares vertex-ai auth, the hydrated copy declares none.
// Launch reads the hydrated copy, so the on-disk auth must be ignored.
func TestEnvGather_OnDiskAuthIgnoredWhenHydratedExists(t *testing.T) {
	srv, _, projectDir := newTestServerWithHarnessConfig(t, "claude",
		"harness: claude\nimage: test-image\nuser: scion\nauth_selected_type: vertex-ai\n"+claudeAuthBlock,
		resolverTestSettings)
	hydratedDir := filepath.Join(t.TempDir(), "claude")
	writeHarnessConfigDirAt(t, hydratedDir, "harness: claude\nimage: test-image\nuser: scion\n")

	var req CreateAgentRequest
	req.ProjectPath = projectDir
	req.Config = &CreateAgentConfig{HarnessConfig: "claude", Profile: "default"}
	required, _, _, _ := srv.extractRequiredEnvKeys(req, "", hydratedDir)
	if slices.Contains(required, "GOOGLE_CLOUD_PROJECT") {
		t.Errorf("on-disk vertex-ai auth was used although a hydrated copy exists: required=%v", required)
	}
}

// TestEnvGather_OnDiskAuthUsedWithoutHydrated: with no hydrated copy the
// on-disk dir is the fallback and its auth applies.
func TestEnvGather_OnDiskAuthUsedWithoutHydrated(t *testing.T) {
	srv, _, projectDir := newTestServerWithHarnessConfig(t, "claude",
		"harness: claude\nimage: test-image\nuser: scion\nauth_selected_type: vertex-ai\n"+claudeAuthBlock,
		resolverTestSettings)

	var req CreateAgentRequest
	req.ProjectPath = projectDir
	req.Config = &CreateAgentConfig{HarnessConfig: "claude", Profile: "default"}
	required, _, _, _ := srv.extractRequiredEnvKeys(req, "")
	if !slices.Contains(required, "GOOGLE_CLOUD_PROJECT") {
		t.Errorf("on-disk vertex-ai auth not used as fallback: required=%v", required)
	}
}

const scriptedHarnessYAML = `harness: claude
image: scion-claude:test
provisioner:
  type: container-script
  interface_version: 1
  command: ["python3", "/home/scion/.scion/harness/provision.py"]
`

// TestHarnessPolicy_EvaluatesHydratedBundle covers ptone/scion#621: the
// policy gate evaluates the hydrated hub bundle when present, not the
// broker-local copy of the same name.
func TestHarnessPolicy_EvaluatesHydratedBundle(t *testing.T) {
	srv, _, dotScion := dispatchTestEnv(t, false)
	// Broker-local copy is declarative (would pass the gate)...
	writeHarnessConfig(t, dotScion, "hc", "harness: claude\nimage: scion-claude:test\n")
	// ...but the hub bundle launch will run is a container-script harness.
	hydratedDir := filepath.Join(t.TempDir(), "hc")
	writeHarnessConfigDirAt(t, hydratedDir, scriptedHarnessYAML)

	req := CreateAgentRequest{Config: &CreateAgentConfig{HarnessConfig: "hc"}}

	name, entry, ok := srv.lookupHarnessConfigForPolicy(req, "", hydratedDir)
	if !ok || name != "hc" {
		t.Fatalf("lookup failed: name=%q ok=%v", name, ok)
	}
	if entry.Provisioner == nil {
		t.Fatal("policy gate evaluated the broker-local copy, not the hydrated bundle")
	}
	if d := srv.evaluateHarnessConfigPolicy(name, entry); d.OK {
		t.Error("hydrated container-script bundle should be refused with allow=false")
	}

	// Reverse: a scripted broker-local copy is ignored when the hydrated
	// bundle is declarative.
	writeHarnessConfig(t, dotScion, "hc2", scriptedHarnessYAML)
	hydrated2 := filepath.Join(t.TempDir(), "hc2")
	writeHarnessConfigDirAt(t, hydrated2, "harness: claude\nimage: scion-claude:test\n")
	req.Config.HarnessConfig = "hc2"
	_, entry, ok = srv.lookupHarnessConfigForPolicy(req, "", hydrated2)
	if !ok || entry.Provisioner != nil {
		t.Errorf("broker-local scripted copy shadowed the declarative hydrated bundle: ok=%v provisioner=%v", ok, entry.Provisioner)
	}

	// No hydrated copy: the broker-local copy is evaluated.
	_, entry, ok = srv.lookupHarnessConfigForPolicy(req, "", "")
	if !ok || entry.Provisioner == nil {
		t.Errorf("broker-local fallback not evaluated: ok=%v", ok)
	}
}

// TestHarnessPolicy_EvaluatesTemplateBundled: a template-bundled
// harness-config outranks the project/global one at launch, so the gate
// must see it too.
func TestHarnessPolicy_EvaluatesTemplateBundled(t *testing.T) {
	srv, _, dotScion := dispatchTestEnv(t, false)
	writeHarnessConfig(t, dotScion, "hc", "harness: claude\nimage: scion-claude:test\n")
	// A hydrated template (absolute path) bundling a scripted "hc".
	tplDir := filepath.Join(t.TempDir(), "tpl")
	writeHarnessConfigDirAt(t, filepath.Join(tplDir, "harness-configs", "hc"), scriptedHarnessYAML)

	req := CreateAgentRequest{Config: &CreateAgentConfig{HarnessConfig: "hc"}}
	_, entry, ok := srv.lookupHarnessConfigForPolicy(req, tplDir, "")
	if !ok || entry.Provisioner == nil {
		t.Errorf("template-bundled scripted harness-config not evaluated by the gate: ok=%v", ok)
	}
}

// TestHydrateHarnessConfig_HashOnlyWarns covers the ptone/scion#620
// hash-only/no-ID branch: the broker falls back to disk, but says so.
func TestHydrateHarnessConfig_HashOnlyWarns(t *testing.T) {
	creds := makeTestCreds("local", "broker-1", "http://localhost:8080")
	srv := newTestServerWithInMemoryCreds(creds)
	var logBuf bytes.Buffer
	srv.agentLifecycleLog = slog.New(slog.NewTextHandler(&logBuf, nil))

	srv.hubMu.RLock()
	conn := srv.hubConnections["local"]
	srv.hubMu.RUnlock()
	conn.LocalStorage = nil
	if conn.HCResolver == nil {
		t.Skip("test hub connection has no harness-config resolver")
	}

	cfg := &CreateAgentConfig{HarnessConfig: "claude", HarnessConfigHash: "abc"}
	path, err := srv.hydrateHarnessConfig(context.Background(), cfg, conn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "" {
		t.Errorf("expected disk fallback (empty path), got %q", path)
	}
	out := logBuf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "hash but no config ID") {
		t.Errorf("expected hash-only WARN, got log: %s", out)
	}
}

// TestHydrateHarnessConfig_NoResolverWarns: a stamped dispatch on a
// connection with no resolver also falls back to disk, with a WARN.
func TestHydrateHarnessConfig_NoResolverWarns(t *testing.T) {
	creds := makeTestCreds("local", "broker-1", "http://localhost:8080")
	srv := newTestServerWithInMemoryCreds(creds)
	var logBuf bytes.Buffer
	srv.agentLifecycleLog = slog.New(slog.NewTextHandler(&logBuf, nil))

	srv.hubMu.RLock()
	conn := srv.hubConnections["local"]
	srv.hubMu.RUnlock()
	conn.HCResolver = nil
	conn.LocalStorage = nil

	cfg := &CreateAgentConfig{HarnessConfig: "claude", HarnessConfigID: "hc-1", HarnessConfigHash: "abc"}
	if _, err := srv.hydrateHarnessConfig(context.Background(), cfg, conn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out := logBuf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "no harness-config resolver") {
		t.Errorf("expected no-resolver WARN, got log: %s", out)
	}
}
