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
	"encoding/json"
	"net/http"
)

// APIError represents a standardized error response.
type APIError struct {
	Code      string                 `json:"code"`
	Message   string                 `json:"message"`
	Details   map[string]interface{} `json:"details,omitempty"`
	RequestID string                 `json:"requestId,omitempty"`
}

// ErrorResponse wraps an APIError for JSON responses.
type ErrorResponse struct {
	Error APIError `json:"error"`
}

// Error codes matching the Runtime Broker API specification.
const (
	ErrCodeInvalidRequest     = "invalid_request"
	ErrCodeValidationError    = "validation_error"
	ErrCodeUnauthorized       = "unauthorized"
	ErrCodeForbidden          = "forbidden"
	ErrCodeAgentNotFound      = "agent_not_found"
	ErrCodeNotFound           = "not_found"
	ErrCodeConflict           = "conflict"
	ErrCodeMethodNotAllowed   = "method_not_allowed"
	ErrCodeInternalError      = "internal_error"
	ErrCodeRuntimeError       = "runtime_error"
	ErrCodeRuntimeUnavailable = "runtime_unavailable"
	ErrCodeHubUnreachable     = "hub_unreachable"
	ErrCodeTemplateError      = "template_error"

	// ErrCodeAgentIdentityUnknown marks a delete/stop that could not be
	// verified as safe because a runtime process restart dropped the
	// in-memory record needed to tell "not found" apart from "exists, but
	// unidentifiable." Stable within this broker's own HTTP API, but what a
	// hub or CLI caller sees differs by which endpoint triggered it:
	//   - delete: the hub re-codes this broker's 409 as its own generic
	//     "conflict" error (pkg/hub/handlers_agents_core.go, the
	//     errors.As(err, &se) && se.StatusCode == http.StatusConflict check
	//     around its DispatchAgentDelete call), so a caller sees this code's
	//     message text, not the code itself;
	//   - stop: the hub's stop dispatch (pkg/hub/handlers_agent_lifecycle.go,
	//     the AgentActionStop case) has no equivalent check — every
	//     DispatchAgentStop error, this one included, becomes a generic 502
	//     "runtime_error" before the CLI sees it, so this 409 is not even
	//     distinguishable as a conflict there today.
	// Either way this constant lets broker-level callers and tests branch on
	// it, not (yet) the hub or the CLI.
	ErrCodeAgentIdentityUnknown = "agent_identity_unknown"

	// ErrCodeRuntimeLogsUnsupported marks a logs request that a runtime
	// declines to serve at all, rather than one that failed. The broker uses
	// this for pkg/runtime.ErrLogsNotSupported (pkg/runtime/capabilities.go),
	// which a runtime returns when serving logs at all would be unsafe or
	// impossible (for example, when reading them would expose another
	// tenant's output), so the runtime never makes the underlying call and
	// this code is the client-visible signal that the feature, not the
	// request, is the reason.
	ErrCodeRuntimeLogsUnsupported = "runtime_logs_unsupported"
)

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, statusCode int, code, message string, details map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	resp := ErrorResponse{
		Error: APIError{
			Code:    code,
			Message: message,
			Details: details,
		},
	}

	_ = json.NewEncoder(w).Encode(resp)
}

// NotFound writes a 404 Not Found response.
func NotFound(w http.ResponseWriter, resource string) {
	code := ErrCodeNotFound
	if resource == "Agent" {
		code = ErrCodeAgentNotFound
	}
	writeError(w, http.StatusNotFound, code, resource+" not found", nil)
}

// BadRequest writes a 400 Bad Request response.
func BadRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, ErrCodeInvalidRequest, message, nil)
}

// ValidationError writes a 400 Bad Request response for validation failures.
func ValidationError(w http.ResponseWriter, message string, details map[string]interface{}) {
	writeError(w, http.StatusBadRequest, ErrCodeValidationError, message, details)
}

// Unauthorized writes a 401 Unauthorized response.
func Unauthorized(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized,
		"Authentication required", nil)
}

// Forbidden writes a 403 Forbidden response.
func Forbidden(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, ErrCodeForbidden,
		"Insufficient permissions", nil)
}

// MethodNotAllowed writes a 405 Method Not Allowed response.
func MethodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed,
		"Method not allowed", nil)
}

// Conflict writes a 409 Conflict response.
func Conflict(w http.ResponseWriter, message string) {
	writeError(w, http.StatusConflict, ErrCodeConflict, message, nil)
}

// AgentIdentityUnknown writes a 409 Conflict response with the stable
// ErrCodeAgentIdentityUnknown code for a delete/stop that a runtime process
// restart made impossible to verify as safe. message must stay generic —
// it must not name any runtime-specific scope (e.g. a namespace) or point
// at a runtime-specific document; a caller's own log line carries those
// details instead (see agentIdentityUnknownError).
func AgentIdentityUnknown(w http.ResponseWriter, message string) {
	writeError(w, http.StatusConflict, ErrCodeAgentIdentityUnknown, message, nil)
}

// RuntimeLogsUnsupported writes a 501 Not Implemented response with the
// stable ErrCodeRuntimeLogsUnsupported code for a runtime that declines to
// serve logs at all (pkg/runtime.ErrLogsNotSupported). message must not
// name any runtime-specific scope, worker, pod, namespace, actor or
// agent — see pkg/runtime.ErrLogsNotSupported for the fixed text this is
// meant to carry.
func RuntimeLogsUnsupported(w http.ResponseWriter, message string) {
	writeError(w, http.StatusNotImplemented, ErrCodeRuntimeLogsUnsupported, message, nil)
}

// InternalError writes a 500 Internal Server Error response.
func InternalError(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, ErrCodeInternalError,
		"Internal server error", nil)
}

// RuntimeError writes a 500 error for runtime failures.
func RuntimeError(w http.ResponseWriter, message string) {
	writeError(w, http.StatusInternalServerError, ErrCodeRuntimeError, message, nil)
}

// RuntimeUnavailable writes a 503 error for a transient container-runtime
// failure (e.g. `docker ps` still failing after internal retries). Unlike
// RuntimeError, this signals the caller should retry rather than treating
// the failure as permanent.
func RuntimeUnavailable(w http.ResponseWriter, message string) {
	writeError(w, http.StatusServiceUnavailable, ErrCodeRuntimeUnavailable, message, nil)
}

// HubUnreachableError writes a 503 Service Unavailable response for Hub connectivity issues.
// This indicates that the Hub is temporarily unreachable and the operation should be retried.
func HubUnreachableError(w http.ResponseWriter, details string) {
	writeError(w, http.StatusServiceUnavailable, ErrCodeHubUnreachable,
		"Hub is unreachable. Check Hub connectivity or use solo mode.", map[string]interface{}{
			"details": details,
		})
}

// TemplateError writes a 500 error for template-related failures.
func TemplateError(w http.ResponseWriter, message string) {
	writeError(w, http.StatusInternalServerError, ErrCodeTemplateError, message, nil)
}

// Unprocessable writes a 422 Unprocessable Entity response.
func Unprocessable(w http.ResponseWriter, message string) {
	writeError(w, http.StatusUnprocessableEntity, ErrCodeValidationError, message, nil)
}
