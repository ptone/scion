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
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ---------------------------------------------------------------------------
// Variable trust classification
// ---------------------------------------------------------------------------

// VarTrust represents the trust class of a substitution variable.
type VarTrust int

const (
	// Trusted variables are admin/platform-fixed: hook config values,
	// project metadata, hub-controlled agent identity fields.
	Trusted VarTrust = iota

	// Untrusted variables are agent/runtime-derived: AGENT_NAME,
	// TASK_SUMMARY, or anything influenced by the LLM/agent.
	Untrusted
)

// TrustedVars is the set of variables classified as TRUSTED (admin/platform-fixed).
// Unknown variables default to UNTRUSTED.
var TrustedVars = map[string]VarTrust{
	// Hook config (admin-set at creation time)
	"HOOK_ID":   Trusted,
	"HOOK_NAME": Trusted,
	"TRIGGER":   Trusted,

	// Project metadata (Hub-controlled)
	"PROJECT_ID":   Trusted,
	"PROJECT_NAME": Trusted,

	// Hub-controlled agent identity (set by Hub, not agent)
	"AGENT_ID":   Trusted,
	"AGENT_SLUG": Trusted,

	// Execution identity (Hub-resolved SA)
	"SA_EMAIL": Trusted,
}

// UntrustedVars is the set of variables explicitly classified as UNTRUSTED
// (agent/runtime-derived, LLM-influenced).
var UntrustedVars = map[string]VarTrust{
	"AGENT_NAME":   Untrusted,
	"TASK_SUMMARY": Untrusted,
	"AGENT_STATUS": Untrusted,
	"ERROR_MSG":    Untrusted,
}

// ClassifyVar returns the trust class for a variable name. Unknown variables
// default to Untrusted.
func ClassifyVar(name string) VarTrust {
	if trust, ok := TrustedVars[name]; ok {
		return trust
	}
	if trust, ok := UntrustedVars[name]; ok {
		return trust
	}
	// Unknown variables default to UNTRUSTED — security-conservative default.
	return Untrusted
}

// ---------------------------------------------------------------------------
// Variable pattern
// ---------------------------------------------------------------------------

// varPattern matches ${VARIABLE_NAME} substitution placeholders.
var varPattern = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// extractVars returns all unique variable names found in s.
func extractVars(s string) []string {
	matches := varPattern.FindAllStringSubmatch(s, -1)
	seen := make(map[string]bool, len(matches))
	var vars []string
	for _, m := range matches {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			vars = append(vars, name)
		}
	}
	return vars
}

// ---------------------------------------------------------------------------
// Static validation (create/update time)
// ---------------------------------------------------------------------------

// ValidateActionVariables checks that no untrusted variable appears in a
// disallowed position within the action template. This is the static
// (create/update time) half of the untrusted-variable guard.
//
// Rules:
//   - Untrusted vars NEVER in URL host or path.
//   - Untrusted vars NEVER in any auth header value.
//   - Untrusted vars NEVER in any header name (injected headers).
//   - Untrusted vars allowed ONLY in body (will be JSON-encoded at render) and
//     URL query params (will be percent-encoded at render).
func ValidateActionVariables(a *store.LifecycleHookAction) []FieldError {
	var errs []FieldError

	if a.URL != "" {
		errs = append(errs, validateURLVariables(a.URL)...)
	}

	// Header names must never contain variables (any trust level could be
	// used to inject new headers).
	for name := range a.Headers {
		for _, v := range extractVars(name) {
			errs = append(errs, FieldError{
				Field:   fmt.Sprintf("action.headers[%s]", name),
				Message: fmt.Sprintf("variable ${%s} not allowed in header name", v),
			})
		}
	}

	// Auth header values must never contain untrusted variables.
	for name, value := range a.Headers {
		if authHeaderNames[strings.ToLower(strings.TrimSpace(name))] {
			for _, v := range extractVars(value) {
				if ClassifyVar(v) == Untrusted {
					errs = append(errs, FieldError{
						Field:   fmt.Sprintf("action.headers[%s]", name),
						Message: fmt.Sprintf("untrusted variable ${%s} not allowed in authentication header value", v),
					})
				}
			}
		}
	}

	// Body: untrusted variables are allowed (they will be JSON-encoded at
	// render time). No static restriction needed — encoding is enforced by
	// the renderer.

	return errs
}

// validateURLVariables checks variable placement within the URL template.
// Untrusted variables are forbidden in the host and path components but
// allowed in query parameters (where they will be percent-encoded at render).
func validateURLVariables(rawURL string) []FieldError {
	var errs []FieldError

	// Split on '?' to separate host+path from query string.
	parts := strings.SplitN(rawURL, "?", 2)
	hostAndPath := parts[0]

	// Check host+path for untrusted variables.
	for _, v := range extractVars(hostAndPath) {
		if ClassifyVar(v) == Untrusted {
			errs = append(errs, FieldError{
				Field:   "action.url",
				Message: fmt.Sprintf("untrusted variable ${%s} not allowed in URL host or path (SSRF risk)", v),
			})
		}
	}

	// Query params: untrusted variables are allowed (percent-encoded at render).
	// No static check needed.

	return errs
}

// ---------------------------------------------------------------------------
// Renderer (execution time)
// ---------------------------------------------------------------------------

// RenderVars is the variable values to substitute at execution time.
type RenderVars map[string]string

// RenderAction renders a LifecycleHookAction template by substituting
// variables with their values from vars. Untrusted variable values are
// strictly encoded:
//   - In query positions: percent-encoded.
//   - In body: JSON-string-encoded (escaped for safe embedding in JSON).
//
// Trusted variables are substituted verbatim.
//
// This is the execution-time half of the untrusted-variable guard. The
// static validator (ValidateActionVariables) has already rejected any hook
// that places an untrusted variable in a disallowed position, so this
// function only needs to encode values in their allowed positions.
//
// Returns a new LifecycleHookAction with all variables resolved. Variables
// not present in vars are left as-is (the caller decides whether to treat
// that as an error).
func RenderAction(a *store.LifecycleHookAction, vars RenderVars) *store.LifecycleHookAction {
	rendered := &store.LifecycleHookAction{
		Type:           a.Type,
		Method:         a.Method,
		OnError:        a.OnError,
		TimeoutSeconds: a.TimeoutSeconds,
	}

	// Render URL with position-aware encoding.
	rendered.URL = renderURL(a.URL, vars)

	// Render headers — only trusted vars are allowed in auth headers
	// (enforced by static validator); substitute all verbatim.
	if a.Headers != nil {
		rendered.Headers = make(map[string]string, len(a.Headers))
		for name, value := range a.Headers {
			rendered.Headers[name] = renderTrustedSubstitution(value, vars)
		}
	}

	// Render body — untrusted vars are JSON-string-encoded.
	rendered.Body = renderBody(a.Body, vars)

	return rendered
}

// renderURL substitutes variables in a URL template. Query-parameter values
// containing untrusted variables are percent-encoded; host/path variables
// (which must be trusted, per static validation) are substituted verbatim.
func renderURL(rawURL string, vars RenderVars) string {
	parts := strings.SplitN(rawURL, "?", 2)
	hostAndPath := parts[0]

	// Host+path: only trusted vars are allowed (enforced statically).
	// Substitute verbatim.
	hostAndPath = renderTrustedSubstitution(hostAndPath, vars)

	if len(parts) == 1 {
		return hostAndPath
	}

	// Query string: untrusted vars are percent-encoded.
	query := parts[1]
	query = varPattern.ReplaceAllStringFunc(query, func(match string) string {
		name := varPattern.FindStringSubmatch(match)[1]
		value, ok := vars[name]
		if !ok {
			return match // Leave unresolved.
		}
		if ClassifyVar(name) == Untrusted {
			return url.QueryEscape(value)
		}
		return value
	})

	return hostAndPath + "?" + query
}

// renderTrustedSubstitution substitutes all variables verbatim. This is used
// for positions where only trusted variables are allowed (enforced at
// validation time).
func renderTrustedSubstitution(s string, vars RenderVars) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := varPattern.FindStringSubmatch(match)[1]
		value, ok := vars[name]
		if !ok {
			return match
		}
		return value
	})
}

// renderBody substitutes variables in a body template. Untrusted variable
// values are JSON-string-encoded (double-quote-escaped) to prevent JSON
// structure injection. Trusted variables are substituted verbatim.
func renderBody(body string, vars RenderVars) string {
	return varPattern.ReplaceAllStringFunc(body, func(match string) string {
		name := varPattern.FindStringSubmatch(match)[1]
		value, ok := vars[name]
		if !ok {
			return match
		}
		if ClassifyVar(name) == Untrusted {
			return jsonEncodeValue(value)
		}
		return value
	})
}

// jsonEncodeValue JSON-encodes a string value for safe embedding in a JSON
// body. It marshals the value as a JSON string and strips the surrounding
// quotes so the result can be placed inside an existing JSON string literal.
// This prevents JSON structure injection (e.g., closing a string and adding
// new fields via \" or similar).
func jsonEncodeValue(s string) string {
	b, _ := json.Marshal(s)
	// json.Marshal wraps in quotes: "value". Strip them so the result
	// can be embedded inside a JSON string literal in the template.
	encoded := string(b)
	if len(encoded) >= 2 && encoded[0] == '"' && encoded[len(encoded)-1] == '"' {
		return encoded[1 : len(encoded)-1]
	}
	return encoded
}
