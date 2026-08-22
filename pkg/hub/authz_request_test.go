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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAuthzDecideUATCannotUseAdminBypass(t *testing.T) {
	authz, _ := authzTestSetup(t)
	admin := NewAuthenticatedUser("admin-1", "admin@example.com", "Admin", "admin", "api")

	decision := authz.Decide(context.Background(), AuthzRequest{
		Principal:  PrincipalContext{Identity: admin},
		Credential: CredentialContext{Kind: CredentialKindUAT, ID: "uat-1", ProjectID: "project-1", Scopes: []string{"agent:read"}},
		Resource:   Resource{Type: "agent", ID: "agent-1", ParentType: "project", ParentID: "project-1"},
		Action:     ActionRead,
	})

	assert.False(t, decision.Allowed)
	assert.Equal(t, "default deny", decision.Reason)
	assert.Equal(t, "uat-1", decision.CredentialID)
	assert.Equal(t, string(CredentialKindUAT), decision.CredentialKind)
}

func TestAuthzDecideFederatedIdentitiesHaveExplicitOutcomes(t *testing.T) {
	authz, _ := authzTestSetup(t)
	ctx := context.Background()

	federatedUser := NewFederatedUserIdentity("https://issuer.example", "user-1", "user@example.com", "User", "member", nil)

	userDecision := authz.Decide(ctx, AuthzRequest{
		Principal:  PrincipalContext{Identity: federatedUser},
		Credential: CredentialContext{Kind: CredentialKindFederation},
		Resource:   Resource{Type: "agent", ID: "agent-1", OwnerID: federatedUser.ID()},
		Action:     ActionRead,
	})
	assert.True(t, userDecision.Allowed)
	assert.Equal(t, PrincipalKindFederatedUser, userDecision.PrincipalKind)
	assert.Equal(t, string(CredentialKindFederation), userDecision.CredentialKind)

	federatedAgent := NewFederatedAgentIdentity("https://issuer.example", "agent-1", "remote-project", "Agent", "root-user", nil, nil)
	agentDecision := authz.Decide(ctx, AuthzRequest{
		Principal:  PrincipalContext{Identity: federatedAgent},
		Credential: CredentialContext{Kind: CredentialKindFederation},
		Resource:   Resource{Type: "agent", ID: "agent-2", Ancestry: []string{federatedAgent.ID()}},
		Action:     ActionRead,
	})
	assert.True(t, agentDecision.Allowed)
	assert.Equal(t, PrincipalKindFederatedAgent, agentDecision.PrincipalKind)

	federatedService := NewFederatedServiceIdentity("https://issuer.example", "service-1", "service@example.com", nil)
	serviceDecision := authz.CheckAccess(ctx, federatedService, Resource{Type: "agent", ID: "agent-1"}, ActionRead)
	assert.False(t, serviceDecision.Allowed)
	assert.Equal(t, "federated service identities are not supported", serviceDecision.Reason)

	spoofedServiceDecision := authz.Decide(ctx, AuthzRequest{
		Principal:  PrincipalContext{Kind: PrincipalKindUser, Identity: federatedService},
		Credential: CredentialContext{Kind: CredentialKindFederation},
		Resource:   Resource{Type: "agent", ID: "agent-1", OwnerID: federatedService.ID()},
		Action:     ActionRead,
	})
	assert.False(t, spoofedServiceDecision.Allowed)
	assert.Equal(t, "principal kind does not match identity", spoofedServiceDecision.Reason)
}

func TestCheckAccessUsesInteractiveCredentialCompatibilityAdapter(t *testing.T) {
	authz, _ := authzTestSetup(t)
	admin := NewAuthenticatedUser("admin-1", "admin@example.com", "Admin", "admin", "api")

	decision := authz.CheckAccess(context.Background(), admin, Resource{Type: "agent", ID: "agent-1"}, ActionRead)

	assert.True(t, decision.Allowed)
	assert.Equal(t, string(CredentialKindInteractive), decision.CredentialKind)
}
