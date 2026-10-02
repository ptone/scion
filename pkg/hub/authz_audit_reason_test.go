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

package hub

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/auditevent"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/authzop"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type projectAuthorityFailureStage string

const (
	failDirectOwnerBindings projectAuthorityFailureStage = "direct owner binding lookup"
	failDirectOwnerRole     projectAuthorityFailureStage = "direct owner role resolution"
	failMembership          projectAuthorityFailureStage = "membership lookup"
	failEffectiveGroups     projectAuthorityFailureStage = "effective-group lookup"
	failGroupBindings       projectAuthorityFailureStage = "group binding lookup"
	failGroupRole           projectAuthorityFailureStage = "group role resolution"
)

type projectAuthorityFailureStore struct {
	store.Store
	stage projectAuthorityFailureStage
}

func (s *projectAuthorityFailureStore) ListRoleBindingsForPrincipal(ctx context.Context, principalType, principalID string) ([]*store.RoleBinding, error) {
	switch s.stage {
	case failDirectOwnerBindings:
		return nil, errors.New("injected direct owner binding failure")
	case failDirectOwnerRole:
		return []*store.RoleBinding{{RoleDefinitionID: "owner-role", ScopeType: store.RoleScopeProject, ScopeID: "project-1"}}, nil
	default:
		return s.Store.ListRoleBindingsForPrincipal(ctx, principalType, principalID)
	}
}

func (s *projectAuthorityFailureStore) GetRoleDefinitionByName(ctx context.Context, name, scopeType string) (*store.RoleDefinition, error) {
	if s.stage == failDirectOwnerRole {
		return nil, errors.New("injected direct owner role failure")
	}
	return s.Store.GetRoleDefinitionByName(ctx, name, scopeType)
}

func (s *projectAuthorityFailureStore) GetProjectMembership(ctx context.Context, projectID, userID string) (*store.ProjectMembership, error) {
	if s.stage == failMembership {
		return nil, errors.New("injected membership failure")
	}
	return s.Store.GetProjectMembership(ctx, projectID, userID)
}

func (s *projectAuthorityFailureStore) GetEffectiveGroups(ctx context.Context, userID string) ([]string, error) {
	if s.stage == failEffectiveGroups {
		return nil, errors.New("injected effective-group failure")
	}
	if s.stage == failGroupBindings || s.stage == failGroupRole {
		return []string{"group-1"}, nil
	}
	return s.Store.GetEffectiveGroups(ctx, userID)
}

func (s *projectAuthorityFailureStore) ListRoleBindingsForPrincipals(ctx context.Context, principals []store.PrincipalRef, scopeTypes, scopeIDs []string) ([]*store.RoleBinding, error) {
	if s.stage == failGroupBindings {
		return nil, errors.New("injected group binding failure")
	}
	if s.stage == failGroupRole {
		return []*store.RoleBinding{{RoleDefinitionID: "group-role", ScopeType: store.RoleScopeProject, ScopeID: "project-1"}}, nil
	}
	return s.Store.ListRoleBindingsForPrincipals(ctx, principals, scopeTypes, scopeIDs)
}

func (s *projectAuthorityFailureStore) GetRoleDefinition(ctx context.Context, id string) (*store.RoleDefinition, error) {
	if s.stage == failGroupRole {
		return nil, errors.New("injected group role failure")
	}
	return s.Store.GetRoleDefinition(ctx, id)
}

func TestAuthorizationProducerContractFields(t *testing.T) {
	requestField, ok := reflect.TypeOf(AuthzRequest{}).FieldByName("OperationID")
	require.True(t, ok, "AuthzRequest must carry the canonical operation ID")
	assert.Equal(t, reflect.TypeOf(authzop.OperationID("")), requestField.Type)
	assert.Equal(t, "-", requestField.Tag.Get("json"), "P1 must not change request wire behavior")

	decisionField, ok := reflect.TypeOf(Decision{}).FieldByName("AuditReason")
	require.True(t, ok, "Decision must carry a structural audit reason")
	assert.Equal(t, reflect.TypeOf(auditevent.ReasonCode("")), decisionField.Type)
	assert.Equal(t, "-", decisionField.Tag.Get("json"), "P1 must not change decision wire behavior")
}

func TestEveryProductionDecisionLiteralAssignsAuditReason(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var violations []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		require.NoError(t, err, entry.Name())
		violations = append(violations, authorizationContractViolations(fset, file)...)
	}

	sort.Strings(violations)
	assert.Empty(t, violations, "production authorization metadata contract violations")
}

func TestAuthorizationContractGuardRejectsMutations(t *testing.T) {
	tests := map[string]string{
		"omitted reason":              `func f() Decision { return Decision{Allowed: false} }`,
		"invalid converted reason":    `func f() Decision { return Decision{Allowed: false, AuditReason: auditevent.ReasonCode("bogus")} }`,
		"incompatible allow reason":   `func f() Decision { return Decision{Allowed: true, AuditReason: auditevent.ReasonPolicyDenied} }`,
		"nonliteral zero return":      `func f() Decision { var d Decision; return d }`,
		"unproven variable return":    `func f(input Decision) Decision { return input }`,
		"metadata read in other file": `func f(d Decision) bool { return d.AuditReason == auditevent.ReasonAllowed }`,
		"operation ID callsite":       `func f() { _ = AuthzRequest{OperationID: authzop.OperationID("route.op")} }`,
		"operation ID read":           `func f(request AuthzRequest) bool { return request.OperationID != "" }`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "unlisted_production_file.go", "package hub\n"+body, 0)
			require.NoError(t, err)
			assert.NotEmpty(t, authorizationContractViolations(fset, file), "mutation must be rejected")
		})
	}
}

func TestAuthorizationAuditReasonMappings(t *testing.T) {
	t.Run("allow", func(t *testing.T) {
		decision := kernelDecisionToDecision(KernelDecision{Allowed: true}, "agent.read")
		assertDecisionAuditReason(t, decision, auditevent.ReasonAllowed)
	})

	t.Run("permission denied", func(t *testing.T) {
		decision := kernelDecisionToDecision(KernelDecision{Allowed: false}, "agent.read")
		assertDecisionAuditReason(t, decision, auditevent.ReasonPermissionMissing)
	})

	t.Run("policy denied", func(t *testing.T) {
		decision := kernelDecisionToDecision(KernelDecision{
			Allowed: false,
			Provenance: KernelProvenance{Restrictions: []RestrictionResult{{
				Kind:    "credential_scope",
				Applied: true,
			}}},
		}, "agent.read")
		assertDecisionAuditReason(t, decision, auditevent.ReasonPolicyDenied)
	})

	t.Run("inherited", func(t *testing.T) {
		service := &AuthzService{}
		principal := PrincipalContext{Kind: PrincipalKindUser, ID: "user-1"}
		candidates := service.relationshipCandidates(principal, Resource{Type: "agent", ID: "agent-1", OwnerID: "user-1"}, ActionRead, "agent.read")
		require.Len(t, candidates, 1)
		assertDecisionAuditReason(t, candidates[0].decision, auditevent.ReasonInherited)
	})

	t.Run("missing identity", func(t *testing.T) {
		service := NewAuthzService(nil, slog.Default())
		decision := service.Decide(context.Background(), AuthzRequest{})
		assertDecisionAuditReason(t, decision, auditevent.ReasonNotAuthenticated)
	})

	t.Run("invalid input", func(t *testing.T) {
		service := NewAuthzService(nil, slog.Default())
		identity := NewAuthenticatedUser("user-1", "user@example.com", "User", "member", "api")
		decision := service.Decide(context.Background(), AuthzRequest{
			Principal: PrincipalContext{Identity: identity},
			Resource:  Resource{Type: "unknown-resource"},
			Action:    Action("unknown-action"),
		})
		assertDecisionAuditReason(t, decision, auditevent.ReasonInvalidRequest)
	})

	t.Run("unknown delegation input", func(t *testing.T) {
		service := NewAuthzService(nil, slog.Default())
		identity := NewAuthenticatedUser("user-1", "user@example.com", "User", "member", "api")
		decision := service.CanDelegate(context.Background(), identity, GrantDescriptor{Type: GrantType("unknown")})
		assertDecisionAuditReason(t, decision, auditevent.ReasonInvalidRequest)
	})

	t.Run("insufficient credential scope", func(t *testing.T) {
		service := NewAuthzService(nil, slog.Default())
		identity := NewScopedUserIdentity(
			NewAuthenticatedUser("user-1", "user@example.com", "User", "member", "api"),
			"project-1",
			[]string{"agent:read"},
		)
		decision := service.enforceUATDelegation(identity, GrantDescriptor{ScopeType: "project", ScopeID: "project-2"})
		require.NotNil(t, decision)
		assertDecisionAuditReason(t, *decision, auditevent.ReasonPolicyDenied)
	})

	t.Run("insufficient permission", func(t *testing.T) {
		service, _ := authzTestSetup(t)
		identity := NewAuthenticatedUser(tid("reason-no-permission"), "user@example.com", "User", "member", "api")
		decision := service.actorHoldsAllPermissions(context.Background(), identity, []string{"agent.read"}, "system", "")
		assertDecisionAuditReason(t, decision, auditevent.ReasonPermissionMissing)
	})

	t.Run("not authorized", func(t *testing.T) {
		service, _ := authzTestSetup(t)
		identity := NewAuthenticatedUser(tid("reason-not-authorized"), "user@example.com", "User", "member", "api")
		decision := service.canDelegateProjectMembership(context.Background(), identity, GrantDescriptor{ProjectID: tid("reason-project")})
		assertDecisionAuditReason(t, decision, auditevent.ReasonNotAuthorized)
	})

	t.Run("dependency unavailable", func(t *testing.T) {
		service, backingStore := authzTestSetup(t)
		require.NoError(t, backingStore.Close())
		identity := NewAuthenticatedUser(tid("reason-store-error"), "user@example.com", "User", "member", "api")
		decision := service.actorHoldsAllPermissions(context.Background(), identity, []string{"agent.read"}, "system", "")
		assertDecisionAuditReason(t, decision, auditevent.ReasonDependencyUnavailable)
	})

	t.Run("check unavailable", func(t *testing.T) {
		server := &Server{}
		decision, err := server.projectReadDecision(context.Background(), nil, &TargetFacts{}, &projectDecisionCache{})
		require.Error(t, err)
		assertDecisionAuditReason(t, decision, auditevent.ReasonCheckUnavailable)
	})
}

func TestProjectMembershipDependencyFailuresRetainDenyAndProse(t *testing.T) {
	stages := []projectAuthorityFailureStage{
		failDirectOwnerBindings,
		failDirectOwnerRole,
		failMembership,
		failEffectiveGroups,
		failGroupBindings,
		failGroupRole,
	}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			_, backingStore := authzTestSetup(t)
			service := NewAuthzService(&projectAuthorityFailureStore{Store: backingStore, stage: stage}, slog.Default())
			identity := NewAuthenticatedUser("user-1", "user@example.com", "User", "member", "api")

			decision := service.canDelegateProjectMembership(context.Background(), identity, GrantDescriptor{ProjectID: "project-1"})

			assert.False(t, decision.Allowed)
			assert.Equal(t, "only project owners and admins can manage project membership", decision.Reason)
			assert.Equal(t, auditevent.ReasonDependencyUnavailable, decision.AuditReason)
		})
	}
}

func TestAuthorizationAuditMetadataCannotInfluenceBranching(t *testing.T) {
	service := NewAuthzService(nil, slog.Default())
	request := AuthzRequest{}
	withoutOperation := service.Decide(context.Background(), request)
	request.OperationID = authzop.OperationID("audit-metadata-only")
	withOperation := service.Decide(context.Background(), request)

	assert.Equal(t, withoutOperation.Allowed, withOperation.Allowed)
	assert.Equal(t, withoutOperation.Reason, withOperation.Reason)
	assert.Equal(t, withoutOperation.AuditReason, withOperation.AuditReason)

}

func assertDecisionAuditReason(t *testing.T, decision Decision, want auditevent.ReasonCode) {
	t.Helper()
	assert.Equal(t, want, decision.AuditReason)
	if decision.Allowed {
		assert.Contains(t, []auditevent.ReasonCode{auditevent.ReasonAllowed, auditevent.ReasonInherited}, decision.AuditReason)
	} else {
		assert.NotContains(t, []auditevent.ReasonCode{auditevent.ReasonAllowed, auditevent.ReasonInherited}, decision.AuditReason)
	}
}

var approvedDecisionReasons = map[string]bool{
	"ReasonAllowed":               true,
	"ReasonInherited":             true,
	"ReasonPermissionMissing":     true,
	"ReasonPolicyDenied":          true,
	"ReasonNotAuthenticated":      true,
	"ReasonNotAuthorized":         true,
	"ReasonInvalidRequest":        true,
	"ReasonDependencyUnavailable": true,
	"ReasonCheckUnavailable":      true,
}

// authorizationContractViolations is deliberately package-wide and
// syntax-closed. Decision literals must use keyed, literal outcomes and exact
// approved auditevent constants. The only post-construction allowance is an
// assignment of one of those exact constants; reads remain forbidden.
func authorizationContractViolations(fset *token.FileSet, file *ast.File) []string {
	var violations []string
	report := func(node ast.Node, message string) {
		violations = append(violations, fmt.Sprintf("%s: %s", filepath.ToSlash(fset.Position(node.Pos()).String()), message))
	}

	assignedAuditReasons := map[token.Pos]bool{}
	authzRequestVars := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for i, lhs := range value.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && i < len(value.Rhs) {
					if literal, ok := value.Rhs[i].(*ast.CompositeLit); ok && isAuthzRequestType(literal.Type) {
						authzRequestVars[ident.Name] = true
					}
				}
				selector, ok := lhs.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "AuditReason" {
					continue
				}
				assignedAuditReasons[selector.Pos()] = true
				if i >= len(value.Rhs) || !isApprovedReasonSelector(value.Rhs[i]) {
					report(selector, "AuditReason assignment must use an exact approved auditevent constant")
				}
			}
		case *ast.ValueSpec:
			if isAuthzRequestType(value.Type) {
				for _, name := range value.Names {
					authzRequestVars[name.Name] = true
				}
			}
			if isDecisionType(value.Type) && len(value.Values) == 0 {
				report(value, "zero-value Decision declaration is forbidden")
			}
		case *ast.Field:
			if isAuthzRequestType(value.Type) {
				for _, name := range value.Names {
					authzRequestVars[name.Name] = true
				}
			}
		}
		return true
	})

	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.CompositeLit:
			if isDecisionType(value.Type) {
				checkDecisionLiteral(value, report)
			}
			if isAuthzRequestType(value.Type) && hasKey(value, "OperationID") {
				report(value, "AuthzRequest.OperationID population is forbidden in P1")
			}
		case *ast.CallExpr:
			if isDecisionType(value.Fun) {
				report(value, "Decision conversions are forbidden")
			}
			if ident, ok := value.Fun.(*ast.Ident); ok && ident.Name == "new" && len(value.Args) == 1 && isDecisionType(value.Args[0]) {
				report(value, "new(Decision) is forbidden")
			}
		case *ast.FuncType:
			if value.Results != nil {
				for _, result := range value.Results.List {
					if isDecisionType(result.Type) && len(result.Names) > 0 {
						report(result, "named Decision result permits an implicit zero return")
					}
				}
			}
		case *ast.SelectorExpr:
			if value.Sel.Name == "AuditReason" && !assignedAuditReasons[value.Pos()] {
				report(value, "AuditReason is write-only authorization metadata")
			}
			if value.Sel.Name == "OperationID" && isAuthzRequestReceiver(value.X, authzRequestVars) {
				report(value, "AuthzRequest.OperationID may not be read or written in P1")
			}
		}
		return true
	})
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil || !returnsDecision(function.Type) {
			continue
		}
		checkDecisionReturns(function, report)
	}
	return violations
}

func checkDecisionReturns(function *ast.FuncDecl, report func(ast.Node, string)) {
	safeOrigins := map[string]bool{}
	unsafeOrigins := map[string]bool{}
	_, pointerResult := function.Type.Results.List[0].Type.(*ast.StarExpr)
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for i, lhs := range value.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || i >= len(value.Rhs) {
					continue
				}
				if isDecisionOrigin(value.Rhs[i]) {
					safeOrigins[ident.Name] = true
				} else if safeOrigins[ident.Name] {
					unsafeOrigins[ident.Name] = true
				}
			}
		case *ast.DeclStmt:
			declaration, ok := value.Decl.(*ast.GenDecl)
			if !ok {
				return true
			}
			for _, spec := range declaration.Specs {
				values, ok := spec.(*ast.ValueSpec)
				if !ok || !isDecisionType(values.Type) {
					continue
				}
				for i, name := range values.Names {
					if i < len(values.Values) && isDecisionOrigin(values.Values[i]) {
						safeOrigins[name.Name] = true
					} else {
						unsafeOrigins[name.Name] = true
					}
				}
			}
		}
		return true
	})

	ast.Inspect(function.Body, func(node ast.Node) bool {
		statement, ok := node.(*ast.ReturnStmt)
		if !ok || len(statement.Results) == 0 {
			return true
		}
		expr := statement.Results[0]
		if isDecisionOrigin(expr) {
			return true
		}
		if ident, ok := expr.(*ast.Ident); ok {
			if pointerResult && ident.Name == "nil" {
				return true
			}
			if safeOrigins[ident.Name] && !unsafeOrigins[ident.Name] {
				return true
			}
			// decorateDecision is the single audited by-value pass-through: all
			// of its call sites must themselves supply a checked origin.
			if function.Name.Name == "decorateDecision" && ident.Name == "decision" {
				return true
			}
		}
		if address, ok := expr.(*ast.UnaryExpr); ok && pointerResult && address.Op == token.AND {
			if ident, ok := address.X.(*ast.Ident); ok && safeOrigins[ident.Name] && !unsafeOrigins[ident.Name] {
				return true
			}
		}
		// projectReadDecision's cache field is assigned only from Decide; its
		// unavailable branch returns a checked literal directly.
		if selector, ok := expr.(*ast.SelectorExpr); ok && function.Name.Name == "projectReadDecision" && selector.Sel.Name == "decision" {
			return true
		}
		report(expr, "Decision variable return has no structurally checked origin")
		return true
	})
}

func returnsDecision(function *ast.FuncType) bool {
	if function.Results == nil || len(function.Results.List) == 0 {
		return false
	}
	return isDecisionType(function.Results.List[0].Type)
}

func isDecisionOrigin(expr ast.Expr) bool {
	switch value := expr.(type) {
	case *ast.CompositeLit:
		return isDecisionType(value.Type)
	case *ast.CallExpr:
		return !isDecisionType(value.Fun)
	case *ast.StarExpr:
		return true
	case *ast.UnaryExpr:
		return value.Op == token.AND && isDecisionOrigin(value.X)
	default:
		return false
	}
}

func checkDecisionLiteral(literal *ast.CompositeLit, report func(ast.Node, string)) {
	allowedExpr, okAllowed := keyedValue(literal, "Allowed")
	reasonExpr, okReason := keyedValue(literal, "AuditReason")
	if !okAllowed {
		report(literal, "Decision literal must explicitly assign Allowed")
	}
	if !okReason {
		report(literal, "Decision literal must explicitly assign AuditReason")
		return
	}
	reason, ok := approvedReasonName(reasonExpr)
	if !ok {
		report(reasonExpr, "AuditReason must be an exact approved auditevent constant")
		return
	}
	allowed, literalAllowed := boolLiteral(allowedExpr)
	if !literalAllowed {
		report(literal, "Decision Allowed must be a boolean literal at construction")
		return
	}
	allowReason := reason == "ReasonAllowed" || reason == "ReasonInherited"
	if allowed != allowReason {
		report(reasonExpr, "Decision Allowed and AuditReason are incompatible")
	}
}

func keyedValue(literal *ast.CompositeLit, name string) (ast.Expr, bool) {
	for _, element := range literal.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if ident, ok := field.Key.(*ast.Ident); ok && ident.Name == name {
			return field.Value, true
		}
	}
	return nil, false
}

func hasKey(literal *ast.CompositeLit, name string) bool {
	_, ok := keyedValue(literal, name)
	return ok
}

func boolLiteral(expr ast.Expr) (bool, bool) {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return false, false
	}
	switch ident.Name {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func approvedReasonName(expr ast.Expr) (string, bool) {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "auditevent" || !approvedDecisionReasons[selector.Sel.Name] {
		return "", false
	}
	return selector.Sel.Name, true
}

func isApprovedReasonSelector(expr ast.Expr) bool {
	_, ok := approvedReasonName(expr)
	return ok
}

func isAuthzRequestReceiver(expr ast.Expr, vars map[string]bool) bool {
	switch value := expr.(type) {
	case *ast.Ident:
		return vars[value.Name]
	case *ast.CompositeLit:
		return isAuthzRequestType(value.Type)
	case *ast.ParenExpr:
		return isAuthzRequestReceiver(value.X, vars)
	default:
		return false
	}
}

func isAuthzRequestType(expr ast.Expr) bool {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name == "AuthzRequest"
	case *ast.StarExpr:
		return isAuthzRequestType(value.X)
	default:
		return false
	}
}

func isDecisionType(expr ast.Expr) bool {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name == "Decision"
	case *ast.StarExpr:
		return isDecisionType(value.X)
	default:
		return false
	}
}
