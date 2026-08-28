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

//go:build !no_sqlite

package hub

// Startup validation tests for ptone/scion#1316, phase 1.
//
// These tests verify that ValidateStartupDefaults logs the correct errors
// when the hub's defaults are misconfigured or when bundled resources are
// missing. The tests exercise acceptance criteria 1 and 2 from the issue.

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/resources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureResourceLogs swaps the server's resource logger for a capturing one
// and returns the handler.
func captureResourceLogs(srv *Server) *levelCapturingHandler {
	h := &levelCapturingHandler{}
	srv.resourceLog = slog.New(h)
	return h
}

// errorRecords returns all records at ERROR level.
func errorRecords(h *levelCapturingHandler) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Level == slog.LevelError {
			out = append(out, r)
		}
	}
	return out
}

// errorMessages returns the messages of all ERROR-level records.
func errorMessages(h *levelCapturingHandler) []string {
	recs := errorRecords(h)
	msgs := make([]string, len(recs))
	for i, r := range recs {
		msgs[i] = r.Message
	}
	return msgs
}

// seedHarnessConfig creates a harness config in the store with global scope.
func seedHarnessConfig(t *testing.T, s store.Store, slug string) {
	t.Helper()
	hc := &store.HarnessConfig{
		ID:      tid("hc-" + slug),
		Name:    slug,
		Slug:    slug,
		Harness: "claude",
		Scope:   store.HarnessConfigScopeGlobal,
		Status:  store.HarnessConfigStatusActive,
	}
	require.NoError(t, s.CreateHarnessConfig(context.Background(), hc))
}

// seedTemplate creates a template in the store with global scope.
func seedTemplate(t *testing.T, s store.Store, slug string) {
	t.Helper()
	tmpl := &store.Template{
		ID:    tid("tmpl-" + slug),
		Name:  slug,
		Slug:  slug,
		Scope: "global",
	}
	require.NoError(t, s.CreateTemplate(context.Background(), tmpl))
}

// --- AC 1: default_harness_config does not resolve ---

func TestValidateStartupDefaults_HarnessConfigResolves(t *testing.T) {
	srv, s := testServer(t)
	logs := captureResourceLogs(srv)
	ctx := context.Background()

	seedHarnessConfig(t, s, "antigravity")
	setHubAgentDefaults(srv, opsettings.AgentDefaultsSettings{
		DefaultHarnessConfig: "antigravity",
	})

	srv.ValidateStartupDefaults(ctx, true)

	errs := errorMessages(logs)
	for _, msg := range errs {
		assert.NotContains(t, msg, "default_harness_config",
			"should not error when harness config resolves")
	}
}

func TestValidateStartupDefaults_HarnessConfigDoesNotResolve(t *testing.T) {
	srv, _ := testServer(t)
	logs := captureResourceLogs(srv)
	ctx := context.Background()

	setHubAgentDefaults(srv, opsettings.AgentDefaultsSettings{
		DefaultHarnessConfig: "nonexistent-config",
	})

	srv.ValidateStartupDefaults(ctx, true)

	errs := errorMessages(logs)
	found := false
	for _, msg := range errs {
		if strings.Contains(msg, "nonexistent-config") &&
			strings.Contains(msg, "does not resolve") {
			found = true
		}
	}
	assert.True(t, found,
		"should log ERROR naming the unresolvable value; got: %v", errs)
}

func TestValidateStartupDefaults_NoDefaultHarnessConfig_HostedMode(t *testing.T) {
	srv, _ := testServer(t)
	logs := captureResourceLogs(srv)
	ctx := context.Background()

	// No agent_defaults set — empty DefaultHarnessConfig
	srv.ValidateStartupDefaults(ctx, true /* hostedMode */)

	errs := errorMessages(logs)
	found := false
	for _, msg := range errs {
		if strings.Contains(msg, "no default_harness_config configured") &&
			strings.Contains(msg, "hosted mode") {
			found = true
		}
	}
	assert.True(t, found,
		"should warn about missing default in hosted mode; got: %v", errs)
}

func TestValidateStartupDefaults_NoDefaultHarnessConfig_WorkstationMode(t *testing.T) {
	srv, _ := testServer(t)
	logs := captureResourceLogs(srv)
	ctx := context.Background()

	// No agent_defaults set — empty DefaultHarnessConfig, workstation mode
	srv.ValidateStartupDefaults(ctx, false /* not hostedMode */)

	errs := errorMessages(logs)
	for _, msg := range errs {
		assert.NotContains(t, msg, "no default_harness_config configured",
			"should NOT warn about missing default in workstation mode")
	}
}

// --- AC 2: seeding was skipped ---

func TestValidateStartupDefaults_BundledConfigMissing_SkipIfAnyExist(t *testing.T) {
	srv, s := testServer(t)
	logs := captureResourceLogs(srv)
	ctx := context.Background()

	// Create a non-bundled harness config so SkipIfAnyExist would fire
	seedHarnessConfig(t, s, "custom-config")
	// Do NOT seed bundled configs (e.g., "antigravity")

	srv.ValidateStartupDefaults(ctx, true)

	errs := errorMessages(logs)
	found := false
	for _, msg := range errs {
		if strings.Contains(msg, "SkipIfAnyExist") {
			found = true
		}
	}
	assert.True(t, found,
		"should log ERROR about SkipIfAnyExist when bundled configs are missing; got: %v", errs)
}

func TestValidateStartupDefaults_AllBundledConfigsPresent(t *testing.T) {
	srv, s := testServer(t)
	logs := captureResourceLogs(srv)
	ctx := context.Background()

	// Seed ALL bundled harness configs so none are reported missing.
	for _, name := range resources.BuiltinHarnessConfigNames() {
		seedHarnessConfig(t, s, name)
	}

	srv.ValidateStartupDefaults(ctx, true)

	errs := errorMessages(logs)
	for _, msg := range errs {
		assert.NotContains(t, msg, "SkipIfAnyExist",
			"should not warn about SkipIfAnyExist when all bundled configs are present")
	}
}

// --- Template validation ---

func TestValidateStartupDefaults_DefaultTemplateResolves(t *testing.T) {
	srv, s := testServer(t)
	logs := captureResourceLogs(srv)
	ctx := context.Background()

	seedTemplate(t, s, "default")
	seedHarnessConfig(t, s, "antigravity")
	setHubAgentDefaults(srv, opsettings.AgentDefaultsSettings{
		DefaultTemplate:      "default",
		DefaultHarnessConfig: "antigravity",
	})

	srv.ValidateStartupDefaults(ctx, true)

	errs := errorMessages(logs)
	for _, msg := range errs {
		assert.NotContains(t, msg, "default_template",
			"should not error when template resolves")
	}
}

func TestValidateStartupDefaults_DefaultTemplateDoesNotResolve(t *testing.T) {
	srv, _ := testServer(t)
	logs := captureResourceLogs(srv)
	ctx := context.Background()

	setHubAgentDefaults(srv, opsettings.AgentDefaultsSettings{
		DefaultTemplate:      "nonexistent-template",
		DefaultHarnessConfig: "antigravity",
	})

	srv.ValidateStartupDefaults(ctx, true)

	errs := errorMessages(logs)
	found := false
	for _, msg := range errs {
		if strings.Contains(msg, "nonexistent-template") &&
			strings.Contains(msg, "does not resolve") {
			found = true
		}
	}
	assert.True(t, found,
		"should log ERROR when default template does not resolve; got: %v", errs)
}

// --- Template harness config fallback ---

func TestValidateStartupDefaults_TemplateHarnessConfigFallback(t *testing.T) {
	srv, s := testServer(t)
	logs := captureResourceLogs(srv)
	ctx := context.Background()

	// Template has DefaultHarnessConfig but no agent_defaults.default_harness_config
	tmpl := &store.Template{
		ID:                   tid("tmpl-with-hc"),
		Name:                 "tmpl-with-hc",
		Slug:                 "tmpl-with-hc",
		Scope:                "global",
		DefaultHarnessConfig: "from-template",
	}
	require.NoError(t, s.CreateTemplate(ctx, tmpl))

	setHubAgentDefaults(srv, opsettings.AgentDefaultsSettings{
		DefaultTemplate: "tmpl-with-hc",
		// DefaultHarnessConfig deliberately empty — should fall back to template
	})

	srv.ValidateStartupDefaults(ctx, true)

	errs := errorMessages(logs)
	found := false
	for _, msg := range errs {
		if strings.Contains(msg, "from-template") &&
			strings.Contains(msg, "does not resolve") {
			found = true
		}
	}
	assert.True(t, found,
		"should check template's harness config when agent_defaults is empty; got: %v", errs)
}
