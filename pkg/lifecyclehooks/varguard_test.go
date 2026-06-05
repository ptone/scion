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

package lifecyclehooks

import (
	"context"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ---------------------------------------------------------------------------
// ClassifyVar
// ---------------------------------------------------------------------------

func TestClassifyVar(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		want     VarTrust
	}{
		// Trusted
		{"trusted: HOOK_ID", "HOOK_ID", Trusted},
		{"trusted: PROJECT_ID", "PROJECT_ID", Trusted},
		{"trusted: AGENT_ID", "AGENT_ID", Trusted},
		{"trusted: SA_EMAIL", "SA_EMAIL", Trusted},

		// Untrusted
		{"untrusted: AGENT_NAME", "AGENT_NAME", Untrusted},
		{"untrusted: TASK_SUMMARY", "TASK_SUMMARY", Untrusted},
		{"untrusted: ERROR_MSG", "ERROR_MSG", Untrusted},

		// Unknown defaults to untrusted
		{"unknown: CUSTOM_VAR", "CUSTOM_VAR", Untrusted},
		{"unknown: RANDOM_THING", "RANDOM_THING", Untrusted},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyVar(tc.variable)
			if got != tc.want {
				t.Errorf("ClassifyVar(%q) = %d, want %d", tc.variable, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateActionVariables — static validation (security-critical tests)
// ---------------------------------------------------------------------------

func TestValidateActionVariables_SSRF(t *testing.T) {
	// SSRF / path manipulation: untrusted var in host or path → REJECTED.
	tests := []struct {
		name    string
		action  *store.LifecycleHookAction
		wantErr bool
		errMsg  string
	}{
		{
			name: "REJECTED: untrusted var in URL host (SSRF)",
			action: &store.LifecycleHookAction{
				Type:           "http",
				Method:         "POST",
				URL:            "https://${AGENT_NAME}.evil.com/api/register",
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "SSRF risk",
		},
		{
			name: "REJECTED: untrusted var in URL path",
			action: &store.LifecycleHookAction{
				Type:           "http",
				Method:         "POST",
				URL:            "https://registry.example.com/${TASK_SUMMARY}/register",
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "SSRF risk",
		},
		{
			name: "REJECTED: untrusted var as entire host",
			action: &store.LifecycleHookAction{
				Type:           "http",
				Method:         "POST",
				URL:            "https://${AGENT_NAME}/api",
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "SSRF risk",
		},
		{
			name: "REJECTED: unknown var in path defaults to untrusted",
			action: &store.LifecycleHookAction{
				Type:           "http",
				Method:         "POST",
				URL:            "https://registry.example.com/${UNKNOWN_VAR}/register",
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "SSRF risk",
		},
		{
			name: "REJECTED: ERROR_MSG in path (untrusted)",
			action: &store.LifecycleHookAction{
				Type:           "http",
				Method:         "POST",
				URL:            "https://registry.example.com/agents/${ERROR_MSG}",
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "SSRF risk",
		},
		{
			name: "PASSES: trusted var in URL path",
			action: &store.LifecycleHookAction{
				Type:           "http",
				Method:         "POST",
				URL:            "https://registry.example.com/${PROJECT_ID}/agents",
				TimeoutSeconds: 10,
			},
			wantErr: false,
		},
		{
			name: "PASSES: trusted var in URL host",
			action: &store.LifecycleHookAction{
				Type:           "http",
				Method:         "POST",
				URL:            "https://${AGENT_SLUG}.registry.example.com/agents",
				TimeoutSeconds: 10,
			},
			wantErr: false,
		},
		{
			name: "PASSES: untrusted var only in query (allowed position)",
			action: &store.LifecycleHookAction{
				Type:           "http",
				Method:         "POST",
				URL:            "https://registry.example.com/agents?name=${AGENT_NAME}",
				TimeoutSeconds: 10,
			},
			wantErr: false,
		},
		{
			name: "PASSES: no variables at all",
			action: &store.LifecycleHookAction{
				Type:           "http",
				Method:         "POST",
				URL:            "https://registry.example.com/agents",
				TimeoutSeconds: 10,
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateActionVariables(tc.action)
			if tc.wantErr {
				if len(errs) == 0 {
					t.Fatalf("expected validation error containing %q, got none", tc.errMsg)
				}
				found := false
				for _, e := range errs {
					if strings.Contains(e.Message, tc.errMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q, got: %v", tc.errMsg, errs)
				}
			} else if len(errs) > 0 {
				t.Errorf("unexpected errors: %v", errs)
			}
		})
	}
}

func TestValidateActionVariables_AuthHeaderInjection(t *testing.T) {
	// Auth-header injection: untrusted var in auth header value → REJECTED.
	tests := []struct {
		name    string
		action  *store.LifecycleHookAction
		wantErr bool
		errMsg  string
	}{
		{
			name: "REJECTED: untrusted var in Authorization header",
			action: &store.LifecycleHookAction{
				Type:   "http",
				Method: "POST",
				URL:    "https://registry.example.com/agents",
				Headers: map[string]string{
					"Authorization": "Bearer ${AGENT_NAME}",
				},
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "untrusted variable ${AGENT_NAME} not allowed in authentication header",
		},
		{
			name: "REJECTED: untrusted var in X-Api-Key",
			action: &store.LifecycleHookAction{
				Type:   "http",
				Method: "POST",
				URL:    "https://registry.example.com/agents",
				Headers: map[string]string{
					"X-Api-Key": "${TASK_SUMMARY}",
				},
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "untrusted variable ${TASK_SUMMARY} not allowed in authentication header",
		},
		{
			name: "REJECTED: untrusted var in X-Auth-Token",
			action: &store.LifecycleHookAction{
				Type:   "http",
				Method: "POST",
				URL:    "https://registry.example.com/agents",
				Headers: map[string]string{
					"X-Auth-Token": "${ERROR_MSG}",
				},
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "untrusted variable ${ERROR_MSG} not allowed in authentication header",
		},
		{
			name: "REJECTED: unknown var in auth header (defaults to untrusted)",
			action: &store.LifecycleHookAction{
				Type:   "http",
				Method: "POST",
				URL:    "https://registry.example.com/agents",
				Headers: map[string]string{
					"Authorization": "Bearer ${UNKNOWN_VAR}",
				},
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "untrusted variable ${UNKNOWN_VAR} not allowed in authentication header",
		},
		{
			name: "PASSES: trusted var in Authorization header",
			action: &store.LifecycleHookAction{
				Type:   "http",
				Method: "POST",
				URL:    "https://registry.example.com/agents",
				Headers: map[string]string{
					"Authorization": "Bearer ${SA_EMAIL}",
				},
				TimeoutSeconds: 10,
			},
			wantErr: false,
		},
		{
			name: "PASSES: untrusted var in non-auth header",
			action: &store.LifecycleHookAction{
				Type:   "http",
				Method: "POST",
				URL:    "https://registry.example.com/agents",
				Headers: map[string]string{
					"X-Agent-Name": "${AGENT_NAME}",
				},
				TimeoutSeconds: 10,
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateActionVariables(tc.action)
			if tc.wantErr {
				if len(errs) == 0 {
					t.Fatalf("expected validation error containing %q, got none", tc.errMsg)
				}
				found := false
				for _, e := range errs {
					if strings.Contains(e.Message, tc.errMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q, got: %v", tc.errMsg, errs)
				}
			} else if len(errs) > 0 {
				t.Errorf("unexpected errors: %v", errs)
			}
		})
	}
}

func TestValidateActionVariables_HeaderNameInjection(t *testing.T) {
	// Header-name injection: any var in header name → REJECTED.
	tests := []struct {
		name    string
		action  *store.LifecycleHookAction
		wantErr bool
		errMsg  string
	}{
		{
			name: "REJECTED: variable in header name",
			action: &store.LifecycleHookAction{
				Type:   "http",
				Method: "POST",
				URL:    "https://registry.example.com/agents",
				Headers: map[string]string{
					"X-${AGENT_NAME}": "value",
				},
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "not allowed in header name",
		},
		{
			name: "REJECTED: trusted variable in header name (still not allowed)",
			action: &store.LifecycleHookAction{
				Type:   "http",
				Method: "POST",
				URL:    "https://registry.example.com/agents",
				Headers: map[string]string{
					"X-${PROJECT_ID}": "value",
				},
				TimeoutSeconds: 10,
			},
			wantErr: true,
			errMsg:  "not allowed in header name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateActionVariables(tc.action)
			if tc.wantErr {
				if len(errs) == 0 {
					t.Fatalf("expected validation error containing %q, got none", tc.errMsg)
				}
				found := false
				for _, e := range errs {
					if strings.Contains(e.Message, tc.errMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q, got: %v", tc.errMsg, errs)
				}
			} else if len(errs) > 0 {
				t.Errorf("unexpected errors: %v", errs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RenderAction — execution-time encoding tests
// ---------------------------------------------------------------------------

func TestRenderAction_URLParamInjection(t *testing.T) {
	// URL param injection: untrusted var in query → PERCENT-ENCODED.
	action := &store.LifecycleHookAction{
		Type:           "http",
		Method:         "POST",
		URL:            "https://registry.example.com/agents?name=${AGENT_NAME}&project=${PROJECT_ID}",
		TimeoutSeconds: 10,
	}

	vars := RenderVars{
		"AGENT_NAME": "evil&other=injected",
		"PROJECT_ID": "proj-123",
	}

	rendered := RenderAction(action, vars)

	// AGENT_NAME (untrusted) should be percent-encoded.
	if !strings.Contains(rendered.URL, "name=evil%26other%3Dinjected") {
		t.Errorf("expected percent-encoded AGENT_NAME in URL query, got: %s", rendered.URL)
	}

	// PROJECT_ID (trusted) should be verbatim.
	if !strings.Contains(rendered.URL, "project=proj-123") {
		t.Errorf("expected verbatim PROJECT_ID in URL query, got: %s", rendered.URL)
	}
}

func TestRenderAction_JSONBodyInjection(t *testing.T) {
	// JSON field/annotation injection: untrusted var in body → JSON-ENCODED.
	action := &store.LifecycleHookAction{
		Type:           "http",
		Method:         "POST",
		URL:            "https://registry.example.com/agents",
		Body:           `{"name": "${AGENT_NAME}", "project": "${PROJECT_ID}"}`,
		TimeoutSeconds: 10,
	}

	vars := RenderVars{
		"AGENT_NAME": `evil", "admin": true, "x": "`,
		"PROJECT_ID": "proj-123",
	}

	rendered := RenderAction(action, vars)

	// AGENT_NAME (untrusted) should be JSON-encoded, preventing structure injection.
	// The malicious value should have its quotes escaped.
	if strings.Contains(rendered.Body, `"admin": true`) {
		t.Errorf("JSON injection succeeded — untrusted value broke out of JSON string: %s", rendered.Body)
	}

	// The rendered body should be valid JSON when the template is valid.
	// Specifically, the escaped value should contain backslash-escaped quotes.
	if !strings.Contains(rendered.Body, `evil\", \"admin\": true, \"x\": \"`) {
		t.Errorf("expected JSON-escaped AGENT_NAME in body, got: %s", rendered.Body)
	}

	// PROJECT_ID (trusted) should be verbatim.
	if !strings.Contains(rendered.Body, `"project": "proj-123"`) {
		t.Errorf("expected verbatim PROJECT_ID in body, got: %s", rendered.Body)
	}
}

func TestRenderAction_BodyWithNewlinesAndSpecialChars(t *testing.T) {
	action := &store.LifecycleHookAction{
		Type:           "http",
		Method:         "POST",
		URL:            "https://registry.example.com/agents",
		Body:           `{"summary": "${TASK_SUMMARY}"}`,
		TimeoutSeconds: 10,
	}

	vars := RenderVars{
		"TASK_SUMMARY": "line1\nline2\ttab\"quote\\backslash",
	}

	rendered := RenderAction(action, vars)

	// Should not contain raw newline or tab (they'd be JSON-encoded).
	if strings.Contains(rendered.Body, "\n") {
		t.Errorf("raw newline found in rendered body (should be JSON-encoded): %s", rendered.Body)
	}
	if strings.Contains(rendered.Body, "\t") {
		t.Errorf("raw tab found in rendered body (should be JSON-encoded): %s", rendered.Body)
	}
}

func TestRenderAction_TrustedHeaderSubstitution(t *testing.T) {
	action := &store.LifecycleHookAction{
		Type:   "http",
		Method: "POST",
		URL:    "https://registry.example.com/agents",
		Headers: map[string]string{
			"Authorization": "Bearer ${SA_EMAIL}",
			"X-Project":     "${PROJECT_ID}",
		},
		TimeoutSeconds: 10,
	}

	vars := RenderVars{
		"SA_EMAIL":   "hooks@example.iam.gserviceaccount.com",
		"PROJECT_ID": "proj-123",
	}

	rendered := RenderAction(action, vars)

	if rendered.Headers["Authorization"] != "Bearer hooks@example.iam.gserviceaccount.com" {
		t.Errorf("expected SA_EMAIL substituted in auth header, got: %s", rendered.Headers["Authorization"])
	}
	if rendered.Headers["X-Project"] != "proj-123" {
		t.Errorf("expected PROJECT_ID substituted in header, got: %s", rendered.Headers["X-Project"])
	}
}

func TestRenderAction_UnresolvedVarsLeftAsIs(t *testing.T) {
	action := &store.LifecycleHookAction{
		Type:           "http",
		Method:         "POST",
		URL:            "https://registry.example.com/${PROJECT_ID}/agents?name=${AGENT_NAME}",
		Body:           `{"hook": "${HOOK_ID}"}`,
		TimeoutSeconds: 10,
	}

	// Provide no vars — all should remain as-is.
	rendered := RenderAction(action, RenderVars{})

	if !strings.Contains(rendered.URL, "${PROJECT_ID}") {
		t.Errorf("expected unresolved ${PROJECT_ID} in URL, got: %s", rendered.URL)
	}
	if !strings.Contains(rendered.URL, "${AGENT_NAME}") {
		t.Errorf("expected unresolved ${AGENT_NAME} in URL query, got: %s", rendered.URL)
	}
	if !strings.Contains(rendered.Body, "${HOOK_ID}") {
		t.Errorf("expected unresolved ${HOOK_ID} in body, got: %s", rendered.Body)
	}
}

func TestRenderAction_PreservesNonVarFields(t *testing.T) {
	action := &store.LifecycleHookAction{
		Type:           "webhook",
		Method:         "POST",
		URL:            "https://hooks.slack.com/services/T00/B00/xxx",
		TimeoutSeconds: 5,
		OnError:        "log",
	}

	rendered := RenderAction(action, RenderVars{})

	if rendered.Type != "webhook" {
		t.Errorf("expected type 'webhook', got: %s", rendered.Type)
	}
	if rendered.Method != "POST" {
		t.Errorf("expected method 'POST', got: %s", rendered.Method)
	}
	if rendered.TimeoutSeconds != 5 {
		t.Errorf("expected timeout 5, got: %d", rendered.TimeoutSeconds)
	}
	if rendered.OnError != "log" {
		t.Errorf("expected onError 'log', got: %s", rendered.OnError)
	}
}

// ---------------------------------------------------------------------------
// RenderAction — legitimate allow-listed body usage PASSES
// ---------------------------------------------------------------------------

func TestRenderAction_LegitimateBodyUsage(t *testing.T) {
	// This is the motivating use case: register an agent in an external
	// registry. The body includes both trusted and untrusted variables.
	action := &store.LifecycleHookAction{
		Type:   "http",
		Method: "POST",
		URL:    "https://registry.example.com/v1/agents?project=${PROJECT_ID}",
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body:           `{"agentId": "${AGENT_ID}", "agentName": "${AGENT_NAME}", "project": "${PROJECT_ID}", "trigger": "${TRIGGER}"}`,
		TimeoutSeconds: 10,
	}

	vars := RenderVars{
		"AGENT_ID":   "agent-uuid-123",
		"AGENT_NAME": "My Test Agent",
		"PROJECT_ID": "proj-456",
		"TRIGGER":    "running",
	}

	// Static validation should pass.
	errs := ValidateActionVariables(action)
	if len(errs) > 0 {
		t.Fatalf("static validation failed for legitimate hook: %v", errs)
	}

	rendered := RenderAction(action, vars)

	// Trusted vars substituted verbatim.
	if !strings.Contains(rendered.Body, `"agentId": "agent-uuid-123"`) {
		t.Errorf("AGENT_ID not substituted in body: %s", rendered.Body)
	}
	if !strings.Contains(rendered.Body, `"project": "proj-456"`) {
		t.Errorf("PROJECT_ID not substituted in body: %s", rendered.Body)
	}
	if !strings.Contains(rendered.Body, `"trigger": "running"`) {
		t.Errorf("TRIGGER not substituted in body: %s", rendered.Body)
	}

	// Untrusted var (AGENT_NAME) is JSON-encoded — for this benign value,
	// encoding is transparent (no special chars to escape).
	if !strings.Contains(rendered.Body, `"agentName": "My Test Agent"`) {
		t.Errorf("AGENT_NAME not properly encoded in body: %s", rendered.Body)
	}

	// URL: trusted var in query substituted verbatim.
	if !strings.Contains(rendered.URL, "project=proj-456") {
		t.Errorf("PROJECT_ID not substituted in URL query: %s", rendered.URL)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: full hook validation + render pipeline
// ---------------------------------------------------------------------------

func TestEndToEnd_RegisterHookValidateAndRender(t *testing.T) {
	// Simulate the full flow: create a hook, validate it, then render it.
	hook := &store.LifecycleHook{
		ID:        "hook-e2e",
		Name:      "register-agent",
		ScopeType: "hub",
		Trigger:   "running",
		Action: &store.LifecycleHookAction{
			Type:   "http",
			Method: "POST",
			URL:    "https://registry.corp.internal/v1/agents/${AGENT_ID}?summary=${AGENT_NAME}",
			Headers: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer ${SA_EMAIL}",
			},
			Body:           `{"id": "${AGENT_ID}", "name": "${AGENT_NAME}", "project": "${PROJECT_ID}", "error": "${ERROR_MSG}"}`,
			OnError:        "retry",
			TimeoutSeconds: 15,
		},
		ExecutionIdentity: "sa-001",
		Enabled:           true,
	}

	// Step 1: validate
	err := ValidateHook(context.Background(), hook, defaultResolver())
	if err != nil {
		t.Fatalf("hook validation failed: %v", err)
	}

	// Step 2: render
	vars := RenderVars{
		"AGENT_ID":   "agt-789",
		"AGENT_NAME": `Agent "Foo" & <Bar>`,
		"PROJECT_ID": "proj-abc",
		"SA_EMAIL":   "hooks@example.iam.gserviceaccount.com",
		"ERROR_MSG":  `crash: "null pointer"`,
	}

	rendered := RenderAction(hook.Action, vars)

	// Trusted vars in path → verbatim
	if !strings.Contains(rendered.URL, "/v1/agents/agt-789") {
		t.Errorf("AGENT_ID not in path: %s", rendered.URL)
	}

	// Untrusted var in query → percent-encoded
	if strings.Contains(rendered.URL, `Agent "Foo"`) {
		t.Errorf("AGENT_NAME not percent-encoded in query: %s", rendered.URL)
	}

	// Trusted var in auth header → verbatim
	if rendered.Headers["Authorization"] != "Bearer hooks@example.iam.gserviceaccount.com" {
		t.Errorf("SA_EMAIL not in auth header: %s", rendered.Headers["Authorization"])
	}

	// Untrusted vars in body → JSON-encoded (no structure injection)
	if strings.Contains(rendered.Body, `"Foo" &`) {
		t.Errorf("AGENT_NAME not JSON-encoded in body: %s", rendered.Body)
	}
}

// ---------------------------------------------------------------------------
// extractVars
// ---------------------------------------------------------------------------

func TestExtractVars(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"no vars", nil},
		{"${FOO}", []string{"FOO"}},
		{"${FOO} and ${BAR}", []string{"FOO", "BAR"}},
		{"${FOO} ${FOO}", []string{"FOO"}}, // deduplication
		{"${A_B_C}", []string{"A_B_C"}},
		{"$FOO", nil},   // no braces
		{"${}", nil},    // empty
		{"${123}", nil}, // starts with digit
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := extractVars(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("extractVars(%q) = %v, want %v", tc.input, got, tc.want)
			}
			for i, v := range got {
				if v != tc.want[i] {
					t.Errorf("extractVars(%q)[%d] = %q, want %q", tc.input, i, v, tc.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// jsonEncodeValue
// ---------------------------------------------------------------------------

func TestJSONEncodeValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "hello", "hello"},
		{"with quotes", `say "hello"`, `say \"hello\"`},
		{"with backslash", `path\to\file`, `path\\to\\file`},
		{"with newline", "line1\nline2", `line1\nline2`},
		{"with tab", "col1\tcol2", `col1\tcol2`},
		{"json injection attempt", `", "admin": true, "x": "`, `\", \"admin\": true, \"x\": \"`},
		{"unicode", "café ☕", "café ☕"},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := jsonEncodeValue(tc.input)
			if got != tc.want {
				t.Errorf("jsonEncodeValue(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
