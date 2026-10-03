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

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/authzop"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func explainOperationRequest(operationID, permission, resourceType, action string) map[string]interface{} {
	body := map[string]interface{}{
		"operationId": operationID,
		"resource": map[string]interface{}{
			"type": resourceType,
			"id":   "contract-target",
		},
		"action": action,
	}
	if permission != "" {
		body["permission"] = permission
	}
	return body
}

func TestExplainAPI_RegisteredOperationUsesReviewedBasePermission(t *testing.T) {
	srv, s := testServer(t)
	memberID := tid("explain-operation-member")
	require.NoError(t, s.CreateUser(t.Context(), &store.User{
		ID: memberID, Email: "operation@test.com", DisplayName: "Operation Contract", Role: "member", Status: "active",
	}))
	ensureHubMembership(t.Context(), s, memberID)
	identity := NewAuthenticatedUser(memberID, "operation@test.com", "Operation Contract", "member", "api")

	body, err := json.Marshal(explainOperationRequest("user.read", "", "user", "read"))
	require.NoError(t, err)
	req := newRequestWithIdentity(t, http.MethodPost, "/api/v1/authz/explain", body, identity)
	rec := httptest.NewRecorder()
	srv.handleAuthzExplain(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp explainResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Allowed)
	require.NotNil(t, resp.Provenance)
	assert.Equal(t, "user.read", resp.Provenance.Permission)
}

func TestExplainAPI_OperationValidationFailsClosedWithoutValueEcho(t *testing.T) {
	srv, _ := testServer(t)
	canary := "scion_pat_SECRET_OPERATION_CANARY"
	tests := []struct {
		name       string
		operation  string
		permission string
		resource   string
		action     string
	}{
		{name: "empty operation", resource: "user", action: "read"},
		{name: "unknown operation", operation: canary, resource: "user", action: "read"},
		{name: "unsafe operation", operation: "user.read\n" + canary, resource: "user", action: "read"},
		{name: "permission mismatch", operation: "user.read", permission: canary, resource: "user", action: "read"},
		{name: "known permission mismatch", operation: "user.read", permission: "user.delete", resource: "user", action: "read"},
		{name: "resource mismatch", operation: "user.read", resource: "agent", action: "read"},
		{name: "action mismatch", operation: "user.read", resource: "user", action: "delete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(explainOperationRequest(tt.operation, tt.permission, tt.resource, tt.action))
			require.NoError(t, err)
			rec := doRequest(t, srv, http.MethodPost, "/api/v1/authz/explain", json.RawMessage(body))
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), canary)
		})
	}
	t.Run("malformed operation type", func(t *testing.T) {
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/authz/explain", map[string]interface{}{
			"operationId": 42,
			"resource":    map[string]interface{}{"type": "user", "id": "contract-target"},
			"action":      "read",
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})
	t.Run("project scope mismatch", func(t *testing.T) {
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/authz/explain", map[string]interface{}{
			"operationId": "project.read",
			"resource": map[string]interface{}{
				"type": "project", "id": "project-a", "projectId": "project-b",
			},
			"action": "read",
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})
}

func TestExplainAPI_DoesNotInferOperation(t *testing.T) {
	srv, _ := testServer(t)
	for _, body := range []map[string]interface{}{
		explainOperationRequest("", "user.read", "user", "read"),
		explainOperationRequest("", "", "user", "read"),
		explainOperationRequest("", "", "hub.user", "user.read"),
	} {
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/authz/explain", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestExplainAPI_EffectivePermissionsUsesNonEmittingIntrospection(t *testing.T) {
	srv, _ := testServer(t)
	emitter := &recordingDecisionAuditEmitter{}
	srv.authzService.SetDecisionAuditEmitter(emitter)
	body := map[string]interface{}{
		"resource": map[string]interface{}{"type": "project", "id": tid("effective-project")},
		"action":   "read",
		"mode":     "effective_permissions",
	}
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/authz/explain", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// The single event is the unchanged hub.audit.read gate. The N permission
	// introspections must not emit ordinary authorization-decision records.
	assert.Len(t, emitter.records, 1)
}

func TestEffectivePermissionIntrospectionBoundaryStructure(t *testing.T) {
	source, err := os.ReadFile("audit_authz.go")
	require.NoError(t, err)
	require.NoError(t, validateExplainIntrospectionBoundary(source))
}

func TestEffectivePermissionIntrospectionBoundaryRejectsMutations(t *testing.T) {
	base := `package hub
	func (a *AuthzService) introspectAuthorization() { a.decide() }
	func (s *Server) handleExplainEffectivePermissions() { s.authzService.introspectAuthorization() }`
	mutations := map[string]string{
		"ordinary emitter path": strings.Replace(base, "introspectAuthorization() }", "Decide() }", 1),
		"operation synthesis":   strings.Replace(base, "a.decide()", "_ = AuthzRequest{OperationID: authzop.OperationID(permission)}; a.decide()", 1),
		"permission mapping":    strings.Replace(base, "s.authzService.introspectAuthorization()", "_ = authzop.CatalogBasePermissions(); s.authzService.introspectAuthorization()", 1),
	}
	for name, source := range mutations {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateExplainIntrospectionBoundary([]byte(source)))
		})
	}
}

func validateExplainIntrospectionBoundary(source []byte) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "audit_authz.go", source, 0)
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || (fn.Name.Name != "introspectAuthorization" && fn.Name.Name != "handleExplainEffectivePermissions") {
			continue
		}
		found[fn.Name.Name] = true
		seenPureKernel := false
		seenBoundaryCall := false
		var violation string
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.CompositeLit:
				if id, ok := n.Type.(*ast.Ident); ok && id.Name == "AuthzRequest" {
					violation = "constructs ordinary AuthzRequest"
				}
			case *ast.SelectorExpr:
				switch n.Sel.Name {
				case "Decide", "emitDecisionAudit", "decisionAuditEmitter", "OperationID", "CatalogBasePermissions":
					violation = "reaches forbidden authorization/audit surface " + n.Sel.Name
				case "decide":
					seenPureKernel = true
				case "introspectAuthorization":
					seenBoundaryCall = true
				}
			}
			return violation == ""
		})
		if violation != "" {
			return &explainBoundaryError{message: fn.Name.Name + " " + violation}
		}
		if fn.Name.Name == "introspectAuthorization" && !seenPureKernel {
			return &explainBoundaryError{message: "introspection must invoke only the pure authorization kernel"}
		}
		if fn.Name.Name == "handleExplainEffectivePermissions" && !seenBoundaryCall {
			return &explainBoundaryError{message: "effective-permissions handler bypasses introspection boundary"}
		}
	}
	if !found["introspectAuthorization"] || !found["handleExplainEffectivePermissions"] {
		return &explainBoundaryError{message: "required introspection boundary is missing"}
	}
	return nil
}

type explainBoundaryError struct{ message string }

func (e *explainBoundaryError) Error() string { return e.message }

func TestAuthzOperationLookupIsClosed(t *testing.T) {
	spec, ok := authzop.Lookup("user.read")
	require.True(t, ok)
	assert.Equal(t, "user.read", spec.BasePermission)
	_, ok = authzop.Lookup(authzop.OperationID("user.read\nunsafe"))
	assert.False(t, ok)
}
