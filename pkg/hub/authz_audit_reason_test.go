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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	var unassigned []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		require.NoError(t, err, entry.Name())
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok || !isDecisionType(literal.Type) {
				return true
			}
			for _, element := range literal.Elts {
				field, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if ident, ok := field.Key.(*ast.Ident); ok && ident.Name == "AuditReason" {
					return true
				}
			}
			pos := fset.Position(literal.Pos())
			unassigned = append(unassigned, filepath.ToSlash(pos.String()))
			return true
		})
	}

	sort.Strings(unassigned)
	assert.Empty(t, unassigned, "every production Decision construction must assign AuditReason")
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

func TestAuthorizationAuditMetadataCannotInfluenceBranching(t *testing.T) {
	service := NewAuthzService(nil, slog.Default())
	request := AuthzRequest{}
	withoutOperation := service.Decide(context.Background(), request)
	request.OperationID = authzop.OperationID("audit-metadata-only")
	withOperation := service.Decide(context.Background(), request)

	assert.Equal(t, withoutOperation.Allowed, withOperation.Allowed)
	assert.Equal(t, withoutOperation.Reason, withOperation.Reason)
	assert.Equal(t, withoutOperation.AuditReason, withOperation.AuditReason)

	assertProductionFieldNeverRead(t, "OperationID", map[string]bool{"authz.go": true})
	assertProductionFieldNeverRead(t, "AuditReason", map[string]bool{
		"authz.go":                    true,
		"authz_candelegate.go":        true,
		"authz_relationship_rules.go": true,
		"material_runtime.go":         true,
	})
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

func assertProductionFieldNeverRead(t *testing.T, fieldName string, files map[string]bool) {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var reads []string
	for _, entry := range entries {
		if entry.IsDir() || !files[entry.Name()] {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		require.NoError(t, err, entry.Name())

		assigned := map[token.Pos]bool{}
		ast.Inspect(file, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assignment.Lhs {
				if selector, ok := lhs.(*ast.SelectorExpr); ok && selector.Sel.Name == fieldName {
					assigned[selector.Pos()] = true
				}
			}
			return true
		})
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == fieldName && !assigned[selector.Pos()] && !isPackageSelector(selector) {
				reads = append(reads, filepath.ToSlash(fset.Position(selector.Pos()).String()))
			}
			return true
		})
	}

	sort.Strings(reads)
	assert.Empty(t, reads, "%s is metadata and must not be read by production authorization code", fieldName)
}

func isPackageSelector(selector *ast.SelectorExpr) bool {
	ident, ok := selector.X.(*ast.Ident)
	return ok && (ident.Name == "auditevent" || ident.Name == "authzop")
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
