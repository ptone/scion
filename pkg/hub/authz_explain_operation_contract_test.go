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
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
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

	// The legacy permission field remains compatible only when its non-empty
	// value exactly matches the selected operation's reviewed BasePermission.
	body, err = json.Marshal(explainOperationRequest("user.read", "user.read", "user", "read"))
	require.NoError(t, err)
	req = newRequestWithIdentity(t, http.MethodPost, "/api/v1/authz/explain", body, identity)
	rec = httptest.NewRecorder()
	srv.handleAuthzExplain(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
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
	require.NoError(t, validateExplainIntrospectionBoundary(productionHubSources(t)))
}

func TestEffectivePermissionIntrospectionBoundaryRejectsMutations(t *testing.T) {
	base := map[string][]byte{
		"boundary.go": []byte(`package hub
		type AuthzService struct{}
		type Server struct{ authzService *AuthzService }
		type AuthzRequest struct{ OperationID string }
		type ordinaryAuthorization struct{}
		type operationCatalog struct{}
		type auditService struct{}
		type auditSink struct{}
		var ordinary ordinaryAuthorization
		var authzop operationCatalog
		var service auditService
		var sink auditSink
		var permission string
		var operation string
		func (ordinaryAuthorization) Decide() {}
		func (operationCatalog) OperationID(string) string { return "" }
		func (operationCatalog) CatalogBasePermissions() []string { return nil }
		func (auditService) emitDecisionAudit() {}
		func (auditSink) Emit() {}
		func (a *AuthzService) Decide() {}
		func (a *AuthzService) decide() {}
		func (a *AuthzService) introspectAuthorization() { a.decide() }
		func (s *Server) handleExplainEffectivePermissions() { s.authzService.introspectAuthorization() }`),
	}
	mutations := map[string]map[string][]byte{
		"direct Decide": mutateBoundarySource(base,
			"s.authzService.introspectAuthorization()",
			"s.authzService.Decide(); s.authzService.introspectAuthorization()"),
		"indirect same-file helper": addSameFileReachableBoundaryHelper(base,
			"func forbiddenHelper() { ordinary.Decide() }"),
		"indirect other-file helper": addBoundaryFile(
			mutateBoundarySource(base, "s.authzService.introspectAuthorization()", "otherFileHelper(); s.authzService.introspectAuthorization()"),
			"other.go", "package hub\nfunc otherFileHelper() { ordinary.Decide() }"),
		"ordinary AuthzRequest construction": addReachableBoundaryHelper(base,
			"func forbiddenHelper() { _ = AuthzRequest{} }"),
		"OperationID synthesis": addReachableBoundaryHelper(base,
			"func forbiddenHelper() { _ = authzop.OperationID(permission) }"),
		"OperationID population": addReachableBoundaryHelper(base,
			"func forbiddenHelper() { _ = AuthzRequest{OperationID: operation} }"),
		"permission-to-operation mapping": addReachableBoundaryHelper(base,
			"func forbiddenHelper() { _ = authzop.CatalogBasePermissions() }"),
		"ordinary decision emitter": addReachableBoundaryHelper(base,
			"func forbiddenHelper() { service.emitDecisionAudit() }"),
		"audit sink": addReachableBoundaryHelper(base,
			"func forbiddenHelper() { sink.Emit() }"),
	}
	for name, sources := range mutations {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateExplainIntrospectionBoundary(sources))
		})
	}
}

func productionHubSources(t *testing.T) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	sources := make(map[string][]byte)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		matched, err := build.Default.MatchFile(".", name)
		require.NoError(t, err, name)
		if !matched {
			continue
		}
		sources[name], err = os.ReadFile(name)
		require.NoError(t, err, name)
	}
	return sources
}

func cloneBoundarySources(sources map[string][]byte) map[string][]byte {
	clone := make(map[string][]byte, len(sources))
	for name, source := range sources {
		clone[name] = append([]byte(nil), source...)
	}
	return clone
}

func mutateBoundarySource(base map[string][]byte, old, replacement string) map[string][]byte {
	mutated := cloneBoundarySources(base)
	mutated["boundary.go"] = []byte(strings.Replace(string(mutated["boundary.go"]), old, replacement, 1))
	return mutated
}

func addBoundaryFile(base map[string][]byte, name, source string) map[string][]byte {
	mutated := cloneBoundarySources(base)
	mutated[name] = []byte(source)
	return mutated
}

func addReachableBoundaryHelper(base map[string][]byte, helper string) map[string][]byte {
	mutated := mutateBoundarySource(base,
		"s.authzService.introspectAuthorization()",
		"forbiddenHelper(); s.authzService.introspectAuthorization()")
	mutated["helper.go"] = []byte("package hub\n" + helper)
	return mutated
}

func addSameFileReachableBoundaryHelper(base map[string][]byte, helper string) map[string][]byte {
	mutated := mutateBoundarySource(base,
		"s.authzService.introspectAuthorization()",
		"forbiddenHelper(); s.authzService.introspectAuthorization()")
	mutated["boundary.go"] = append(mutated["boundary.go"], []byte("\n"+helper)...)
	return mutated
}

type explainBoundaryFunction struct {
	name string
	file string
	decl *ast.FuncDecl
}

type explainBoundaryVisit struct {
	fn    *explainBoundaryFunction
	chain []string
}

func validateExplainIntrospectionBoundary(sources map[string][]byte) error {
	fset := token.NewFileSet()
	functions := make(map[string][]*explainBoundaryFunction)
	var files []*ast.File
	filenames := make([]string, 0, len(sources))
	for name := range sources {
		filenames = append(filenames, name)
	}
	sort.Strings(filenames)
	for _, name := range filenames {
		file, err := parser.ParseFile(fset, name, sources[name], 0)
		if err != nil {
			return err
		}
		files = append(files, file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			functions[fn.Name.Name] = append(functions[fn.Name.Name], &explainBoundaryFunction{
				name: fn.Name.Name,
				file: name,
				decl: fn,
			})
		}
	}
	typeInfo := &types.Info{
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	typeConfig := types.Config{
		Importer: importer.Default(),
		// The standard importer cannot load unexported sibling module packages in
		// every worktree environment. Local declarations and selections are still
		// resolved, which is the graph this package-boundary guard traverses.
		Error: func(error) {},
	}
	_, _ = typeConfig.Check("github.com/GoogleCloudPlatform/scion/pkg/hub", fset, files, typeInfo)
	functionsByObject := make(map[*types.Func]*explainBoundaryFunction)
	for _, namedFunctions := range functions {
		for _, fn := range namedFunctions {
			if object, ok := typeInfo.Defs[fn.decl.Name].(*types.Func); ok {
				functionsByObject[object] = fn
			}
		}
	}
	if len(functions["handleExplainEffectivePermissions"]) != 1 || len(functions["introspectAuthorization"]) != 1 || len(functions["decide"]) != 1 {
		return &explainBoundaryError{message: "required effective-permissions boundary functions are missing or ambiguous"}
	}

	queue := []explainBoundaryVisit{{
		fn:    functions["handleExplainEffectivePermissions"][0],
		chain: []string{"handleExplainEffectivePermissions"},
	}}
	visited := make(map[*ast.FuncDecl]bool)
	reached := make(map[string]bool)
	for len(queue) > 0 {
		visit := queue[0]
		queue = queue[1:]
		if visited[visit.fn.decl] {
			continue
		}
		visited[visit.fn.decl] = true
		reached[visit.fn.name] = true
		var violation string
		var callees []*explainBoundaryFunction
		ast.Inspect(visit.fn.decl.Body, func(node ast.Node) bool {
			if violation != "" {
				return false
			}
			switch n := node.(type) {
			case *ast.CompositeLit:
				if explainBoundaryExprName(n.Type) == "AuthzRequest" {
					violation = "constructs ordinary AuthzRequest"
				}
				for _, element := range n.Elts {
					if kv, ok := element.(*ast.KeyValueExpr); ok && explainBoundaryExprName(kv.Key) == "OperationID" {
						violation = "populates OperationID"
					}
				}
			case *ast.SelectorExpr:
				if explainBoundaryForbiddenSelector(n) {
					violation = "reaches forbidden authorization/audit surface " + n.Sel.Name
				}
			case *ast.CallExpr:
				if name := explainBoundaryCallName(n.Fun); name != "" {
					if explainBoundaryForbiddenCall(name) {
						violation = "calls forbidden authorization/audit surface " + name
					}
				}
				if called := explainBoundaryCalledFunction(n.Fun, typeInfo, functionsByObject); called != nil {
					callees = append(callees, called)
				}
			}
			return true
		})
		if violation != "" {
			return &explainBoundaryError{message: fmt.Sprintf("%s (%s): %s", strings.Join(visit.chain, " -> "), visit.fn.file, violation)}
		}
		for _, callee := range callees {
			queue = append(queue, explainBoundaryVisit{fn: callee, chain: append(append([]string(nil), visit.chain...), callee.name)})
		}
	}
	if !reached["introspectAuthorization"] || !reached["decide"] {
		return &explainBoundaryError{message: "effective-permissions handler must transitively reach the operation-free introspection boundary and pure kernel"}
	}
	return nil
}

func explainBoundaryCalledFunction(expr ast.Expr, info *types.Info, functions map[*types.Func]*explainBoundaryFunction) *explainBoundaryFunction {
	var object types.Object
	switch value := expr.(type) {
	case *ast.Ident:
		object = info.Uses[value]
	case *ast.SelectorExpr:
		if selection := info.Selections[value]; selection != nil {
			object = selection.Obj()
		} else {
			object = info.Uses[value.Sel]
		}
	}
	fn, _ := object.(*types.Func)
	return functions[fn]
}

func explainBoundaryExprName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	case *ast.StarExpr:
		return explainBoundaryExprName(value.X)
	default:
		return ""
	}
}

func explainBoundaryCallName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	default:
		return ""
	}
}

func explainBoundaryForbiddenCall(name string) bool {
	switch name {
	case "Decide", "emitDecisionAudit", "EmitDecisionAudit", "BuildDecisionAuditRecord", "BuildAuthorizationDecision", "Emit", "NewSlogSink":
		return true
	default:
		return false
	}
}

func explainBoundaryForbiddenSelector(selector *ast.SelectorExpr) bool {
	switch selector.Sel.Name {
	case "OperationID", "decisionAuditEmitter", "Catalog", "CatalogBasePermissions", "CatalogOperationIDs", "SlogSink":
		return true
	case "Lookup":
		return explainBoundaryExprName(selector.X) == "authzop"
	default:
		return false
	}
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
