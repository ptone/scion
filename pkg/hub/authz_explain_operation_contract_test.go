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
	var matchingResp explainResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &matchingResp))
	assert.True(t, matchingResp.Allowed)
	require.NotNil(t, matchingResp.Provenance)
	assert.Equal(t, "user.read", matchingResp.Provenance.Permission)
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
	base := explainBoundaryTestSources()
	mutations := map[string]map[string][]byte{
		"interface same-file value receiver": addSameFileBoundaryDeclarations(
			mutateBoundarySource(base, "s.authzService.introspectAuthorization()", "var runner boundaryRunner = boundaryValueRunner{}; runner.Run(); s.authzService.introspectAuthorization()"),
			"type boundaryRunner interface { Run() }\ntype boundaryValueRunner struct{}\nfunc (boundaryValueRunner) Run() { ordinary.Decide() }"),
		"interface other-file pointer receiver": addBoundaryFile(
			addSameFileBoundaryDeclarations(
				mutateBoundarySource(base, "s.authzService.introspectAuthorization()", "var runner boundaryRunner = &boundaryPointerRunner{}; runner.Run(); s.authzService.introspectAuthorization()"),
				"type boundaryRunner interface { Run() }"),
			"runner.go", "package hub\ntype boundaryPointerRunner struct{}\nfunc (*boundaryPointerRunner) Run() { ordinary.Decide() }"),
		"interface multiple implementors": addSameFileBoundaryDeclarations(
			mutateBoundarySource(base, "s.authzService.introspectAuthorization()", "var runner boundaryRunner = boundarySafeRunner{}; runner.Run(); s.authzService.introspectAuthorization()"),
			"type boundaryRunner interface { Run() }\ntype boundarySafeRunner struct{}\nfunc (boundarySafeRunner) Run() {}\ntype boundaryUnsafeRunner struct{}\nfunc (*boundaryUnsafeRunner) Run() { ordinary.Decide() }"),
	}
	for name, sources := range map[string]map[string][]byte{
		"direct Decide": mutateBoundarySource(base,
			"s.authzService.introspectAuthorization()",
			"s.authzService.Decide(); s.authzService.introspectAuthorization()"),
		"indirect same-file helper": addSameFileReachableBoundaryHelper(base,
			"func forbiddenHelper() { ordinary.Decide() }"),
		"indirect other-file helper": addBoundaryFile(
			mutateBoundarySource(base, "s.authzService.introspectAuthorization()", "otherFileHelper(); s.authzService.introspectAuthorization()"),
			"other.go", "package hub\nfunc otherFileHelper() { ordinary.Decide() }"),
		"indirect other-file function value": addBoundaryFile(
			mutateBoundarySource(base, "s.authzService.introspectAuthorization()", "next := otherFileHelper; next(); s.authzService.introspectAuthorization()"),
			"other.go", "package hub\nfunc otherFileHelper() { ordinary.Decide() }"),
		"ordinary AuthzRequest construction": addReachableBoundaryHelper(base,
			"func forbiddenHelper() { _ = AuthzRequest{} }"),
		"ordinary AuthzRequest allocation": addReachableBoundaryHelper(base,
			"func forbiddenHelper() { _ = new(AuthzRequest) }"),
		"ordinary AuthzRequest typed declaration": addReachableBoundaryHelper(base,
			"func forbiddenHelper() { var request AuthzRequest; _ = request }"),
		"ordinary AuthzRequest alias construction": addReachableBoundaryHelper(base,
			"type requestAlias = AuthzRequest\nfunc forbiddenHelper() { _ = requestAlias{} }"),
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
	} {
		mutations[name] = sources
	}
	for name, sources := range mutations {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateExplainIntrospectionBoundary(sources))
		})
	}
}

func TestEffectivePermissionIntrospectionBoundaryAllowsSafeInterfaceDispatch(t *testing.T) {
	sources := addBoundaryFile(
		addSameFileBoundaryDeclarations(
			mutateBoundarySource(explainBoundaryTestSources(), "s.authzService.introspectAuthorization()", "var runner boundaryRunner = boundarySafeValueRunner{}; runner.Run(); s.authzService.introspectAuthorization()"),
			"type boundaryRunner interface { Run() }\ntype boundarySafeValueRunner struct{}\nfunc (boundarySafeValueRunner) Run() { safeBoundaryHelper() }"),
		"runner.go", "package hub\ntype boundarySafePointerRunner struct{}\nfunc (*boundarySafePointerRunner) Run() { safeBoundaryHelper() }\nfunc safeBoundaryHelper() {}")
	require.NoError(t, validateExplainIntrospectionBoundary(sources))
}

func explainBoundaryTestSources() map[string][]byte {
	return map[string][]byte{
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

func addSameFileBoundaryDeclarations(base map[string][]byte, declarations string) map[string][]byte {
	mutated := cloneBoundarySources(base)
	mutated["boundary.go"] = append(mutated["boundary.go"], []byte("\n"+declarations)...)
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
	node ast.Node
	body *ast.BlockStmt
}

type explainBoundaryVisit struct {
	fn    *explainBoundaryFunction
	chain []string
}

type explainBoundaryFunctionValue struct {
	targets      map[*types.Func]struct{}
	literals     map[*ast.FuncLit]struct{}
	dependencies map[*types.Var]struct{}
	unsupported  bool
}

type explainBoundaryResolvedFunctionValue struct {
	targets  map[*types.Func]struct{}
	literals map[*ast.FuncLit]struct{}
}

func validateExplainIntrospectionBoundary(sources map[string][]byte) error {
	fset := token.NewFileSet()
	functions := make(map[string][]*explainBoundaryFunction)
	functionLiterals := make(map[*ast.FuncLit]*explainBoundaryFunction)
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
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.FuncLit)
			if ok {
				functionLiterals[literal] = &explainBoundaryFunction{
					name: "function value",
					file: name,
					node: literal,
					body: literal.Body,
				}
			}
			return true
		})
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			functions[fn.Name.Name] = append(functions[fn.Name.Name], &explainBoundaryFunction{
				name: fn.Name.Name,
				file: name,
				decl: fn,
				node: fn,
				body: fn.Body,
			})
		}
	}
	typeInfo := &types.Info{
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Types:      make(map[ast.Expr]types.TypeAndValue),
	}
	typeConfig := types.Config{
		Importer: importer.Default(),
		// The standard importer cannot load unexported sibling module packages in
		// every worktree environment. Local declarations and selections are still
		// resolved, which is the graph this package-boundary guard traverses.
		Error: func(error) {},
	}
	checkedPackage, _ := typeConfig.Check("github.com/GoogleCloudPlatform/scion/pkg/hub", fset, files, typeInfo)
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
	if checkedPackage == nil {
		return &explainBoundaryError{message: "effective-permissions boundary package could not be resolved"}
	}
	authzRequestObject := checkedPackage.Scope().Lookup("AuthzRequest")
	if authzRequestObject == nil {
		return &explainBoundaryError{message: "ordinary AuthzRequest type is missing"}
	}
	functionValues := explainBoundaryFunctionValues(files, typeInfo)

	queue := []explainBoundaryVisit{{
		fn:    functions["handleExplainEffectivePermissions"][0],
		chain: []string{"handleExplainEffectivePermissions"},
	}}
	visited := make(map[ast.Node]bool)
	reached := make(map[string]bool)
	for len(queue) > 0 {
		visit := queue[0]
		queue = queue[1:]
		if visited[visit.fn.node] {
			continue
		}
		visited[visit.fn.node] = true
		reached[visit.fn.name] = true
		var violation string
		var callees []*explainBoundaryFunction
		ast.Inspect(visit.fn.body, func(node ast.Node) bool {
			if violation != "" {
				return false
			}
			if expr, ok := node.(ast.Expr); ok && explainBoundaryHasAuthzRequestType(typeInfo.TypeOf(expr), authzRequestObject.Type()) {
				violation = "constructs or carries ordinary AuthzRequest"
				return false
			}
			switch n := node.(type) {
			case *ast.CompositeLit:
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
				called, callViolation := explainBoundaryCalledFunctions(n.Fun, typeInfo, checkedPackage, functionsByObject, functionLiterals, functionValues)
				if callViolation != "" {
					violation = callViolation
				} else {
					callees = append(callees, called...)
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

func explainBoundaryFunctionValues(files []*ast.File, info *types.Info) map[*types.Var]*explainBoundaryFunctionValue {
	values := make(map[*types.Var]*explainBoundaryFunctionValue)
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.AssignStmt:
				if len(value.Lhs) != len(value.Rhs) {
					for _, lhs := range value.Lhs {
						explainBoundaryRecordFunctionValue(values, explainBoundaryAssignedVar(lhs, info), nil, info)
					}
					return true
				}
				for index := range value.Lhs {
					explainBoundaryRecordFunctionValue(values, explainBoundaryAssignedVar(value.Lhs[index], info), value.Rhs[index], info)
				}
			case *ast.ValueSpec:
				for index, name := range value.Names {
					variable, _ := info.Defs[name].(*types.Var)
					if variable == nil || index >= len(value.Values) {
						continue
					}
					explainBoundaryRecordFunctionValue(values, variable, value.Values[index], info)
				}
			case *ast.KeyValueExpr:
				field, _ := explainBoundaryObject(value.Key, info).(*types.Var)
				explainBoundaryRecordFunctionValue(values, field, value.Value, info)
			case *ast.CallExpr:
				function, _ := explainBoundaryObject(value.Fun, info).(*types.Func)
				if function == nil {
					break
				}
				signature := explainBoundarySignature(function.Type())
				if signature == nil {
					break
				}
				parameters := signature.Params()
				for index, argument := range value.Args {
					parameterIndex := index
					if parameterIndex >= parameters.Len() {
						if !signature.Variadic() {
							break
						}
						parameterIndex = parameters.Len() - 1
					}
					if parameterIndex >= 0 {
						explainBoundaryRecordFunctionValue(values, parameters.At(parameterIndex), argument, info)
					}
				}
			}
			return true
		})
	}
	return values
}

func explainBoundaryAssignedVar(expr ast.Expr, info *types.Info) *types.Var {
	if identifier, ok := expr.(*ast.Ident); ok {
		object := info.Defs[identifier]
		if object == nil {
			object = info.Uses[identifier]
		}
		variable, _ := object.(*types.Var)
		return variable
	}
	variable, _ := explainBoundaryObject(expr, info).(*types.Var)
	return variable
}

func explainBoundaryRecordFunctionValue(values map[*types.Var]*explainBoundaryFunctionValue, variable *types.Var, expr ast.Expr, info *types.Info) {
	if variable == nil {
		return
	}
	if explainBoundarySignature(variable.Type()) == nil {
		return
	}
	value := values[variable]
	if value == nil {
		value = &explainBoundaryFunctionValue{
			targets:      make(map[*types.Func]struct{}),
			literals:     make(map[*ast.FuncLit]struct{}),
			dependencies: make(map[*types.Var]struct{}),
		}
		values[variable] = value
	}
	if expr == nil {
		value.unsupported = true
		return
	}
	if literal, ok := expr.(*ast.FuncLit); ok {
		value.literals[literal] = struct{}{}
		return
	}
	switch object := explainBoundaryObject(expr, info).(type) {
	case *types.Func:
		value.targets[object] = struct{}{}
	case *types.Var:
		value.dependencies[object] = struct{}{}
	default:
		value.unsupported = true
	}
}

func explainBoundaryCalledFunctions(expr ast.Expr, info *types.Info, checkedPackage *types.Package, functions map[*types.Func]*explainBoundaryFunction, literals map[*ast.FuncLit]*explainBoundaryFunction, values map[*types.Var]*explainBoundaryFunctionValue) ([]*explainBoundaryFunction, string) {
	object := explainBoundaryObject(expr, info)
	if function, ok := object.(*types.Func); ok {
		if called := functions[function]; called != nil {
			return []*explainBoundaryFunction{called}, ""
		}
		if explainBoundaryInterfaceReceiver(function) != nil && function.Pkg() == checkedPackage {
			called, complete := explainBoundaryInterfaceImplementations(function, checkedPackage, functions)
			if !complete {
				return nil, "cannot resolve complete package-local interface dispatch " + function.FullName()
			}
			return called, ""
		}
		return nil, ""
	}
	if _, builtin := object.(*types.Builtin); builtin {
		return nil, ""
	}
	if _, conversion := object.(*types.TypeName); conversion {
		return nil, ""
	}
	variable, ok := object.(*types.Var)
	if !ok {
		exprType := info.TypeOf(expr)
		if explainBoundarySignature(exprType) != nil {
			return nil, "calls unsupported dynamic function value"
		}
		return nil, ""
	}
	resolved, supported := explainBoundaryResolveFunctionValue(variable, values, make(map[*types.Var]bool))
	if !supported {
		return nil, "calls unsupported dynamic function value " + variable.Name()
	}
	called := make([]*explainBoundaryFunction, 0, len(resolved.targets)+len(resolved.literals))
	for target := range resolved.targets {
		if explainBoundaryForbiddenCall(target.Name()) {
			return nil, "calls forbidden authorization/audit surface " + target.Name()
		}
		if fn := functions[target]; fn != nil {
			called = append(called, fn)
		}
	}
	for literal := range resolved.literals {
		if fn := literals[literal]; fn != nil {
			called = append(called, fn)
		} else {
			return nil, "calls unresolved function literal"
		}
	}
	return called, ""
}

func explainBoundaryInterfaceReceiver(method *types.Func) *types.Interface {
	signature := explainBoundarySignature(method.Type())
	if signature == nil || signature.Recv() == nil {
		return nil
	}
	receiver := types.Unalias(signature.Recv().Type())
	interfaceType, _ := receiver.Underlying().(*types.Interface)
	if interfaceType != nil {
		interfaceType.Complete()
	}
	return interfaceType
}

func explainBoundaryInterfaceImplementations(method *types.Func, checkedPackage *types.Package, functions map[*types.Func]*explainBoundaryFunction) ([]*explainBoundaryFunction, bool) {
	interfaceType := explainBoundaryInterfaceReceiver(method)
	if interfaceType == nil {
		return nil, false
	}
	seen := make(map[*types.Func]bool)
	var implementations []*explainBoundaryFunction
	for _, name := range checkedPackage.Scope().Names() {
		typeName, ok := checkedPackage.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		candidate := types.Unalias(typeName.Type())
		if _, isInterface := candidate.Underlying().(*types.Interface); isInterface {
			continue
		}
		candidates := []types.Type{candidate}
		if _, isPointer := candidate.(*types.Pointer); !isPointer {
			candidates = append(candidates, types.NewPointer(candidate))
		}
		for _, receiver := range candidates {
			if !types.Implements(receiver, interfaceType) {
				continue
			}
			selected, _, _ := types.LookupFieldOrMethod(receiver, true, checkedPackage, method.Name())
			implementation, ok := selected.(*types.Func)
			if !ok {
				return nil, false
			}
			if explainBoundaryInterfaceReceiver(implementation) != nil {
				// A concrete wrapper can promote a method from an embedded
				// interface. It contributes no executable body of its own; every
				// package-local concrete target is enumerated independently below.
				// An external interface target cannot be proven complete here.
				if implementation.Pkg() != checkedPackage {
					return nil, false
				}
				continue
			}
			if seen[implementation] {
				continue
			}
			wrapped := functions[implementation]
			if wrapped == nil {
				return nil, false
			}
			seen[implementation] = true
			implementations = append(implementations, wrapped)
		}
	}
	return implementations, len(implementations) > 0
}

func explainBoundarySignature(candidate types.Type) *types.Signature {
	if candidate == nil {
		return nil
	}
	signature, _ := types.Unalias(candidate).Underlying().(*types.Signature)
	return signature
}

func explainBoundaryResolveFunctionValue(variable *types.Var, values map[*types.Var]*explainBoundaryFunctionValue, visiting map[*types.Var]bool) (explainBoundaryResolvedFunctionValue, bool) {
	value := values[variable]
	if value == nil || value.unsupported || visiting[variable] {
		return explainBoundaryResolvedFunctionValue{}, false
	}
	visiting[variable] = true
	resolved := explainBoundaryResolvedFunctionValue{
		targets:  make(map[*types.Func]struct{}, len(value.targets)),
		literals: make(map[*ast.FuncLit]struct{}, len(value.literals)),
	}
	for target := range value.targets {
		resolved.targets[target] = struct{}{}
	}
	for literal := range value.literals {
		resolved.literals[literal] = struct{}{}
	}
	for dependency := range value.dependencies {
		dependencyValue, supported := explainBoundaryResolveFunctionValue(dependency, values, visiting)
		if !supported {
			return explainBoundaryResolvedFunctionValue{}, false
		}
		for target := range dependencyValue.targets {
			resolved.targets[target] = struct{}{}
		}
		for literal := range dependencyValue.literals {
			resolved.literals[literal] = struct{}{}
		}
	}
	delete(visiting, variable)
	return resolved, len(resolved.targets)+len(resolved.literals) > 0
}

func explainBoundaryObject(expr ast.Expr, info *types.Info) types.Object {
	switch value := expr.(type) {
	case *ast.Ident:
		return info.Uses[value]
	case *ast.SelectorExpr:
		if selection := info.Selections[value]; selection != nil {
			return selection.Obj()
		}
		return info.Uses[value.Sel]
	case *ast.ParenExpr:
		return explainBoundaryObject(value.X, info)
	case *ast.IndexExpr:
		return explainBoundaryObject(value.X, info)
	case *ast.IndexListExpr:
		return explainBoundaryObject(value.X, info)
	default:
		return nil
	}
}

func explainBoundaryHasAuthzRequestType(candidate, authzRequest types.Type) bool {
	if candidate == nil || authzRequest == nil {
		return false
	}
	candidate = types.Unalias(candidate)
	for {
		pointer, ok := candidate.(*types.Pointer)
		if !ok {
			break
		}
		candidate = types.Unalias(pointer.Elem())
	}
	return types.Identical(candidate, types.Unalias(authzRequest))
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
