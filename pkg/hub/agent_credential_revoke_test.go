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
	"fmt"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// insertTestAgentCredential records an active credential for agentID under
// jti, the same way production's storeCredentialRecorder does on a real
// mint. Tests use it to seed a credential outside the dispatch path under
// test (e.g. a sibling agent's credential that must survive the test's
// revoke call).
func insertTestAgentCredential(t *testing.T, s store.AgentCredentialStore, agentID, projectID, jti string) {
	t.Helper()
	now := time.Now()
	cred := &store.AgentCredential{
		AgentID:      agentID,
		ProjectID:    projectID,
		TokenJTIHash: hashJTI(jti),
		IssuedAt:     now,
		ExpiresAt:    now.Add(10 * time.Hour),
	}
	if err := s.CreateAgentCredential(context.Background(), cred); err != nil {
		t.Fatalf("failed to insert test agent credential: %v", err)
	}
}

// getTestAgentCredential looks a credential back up by its plaintext jti
// (hashing it the same way the production code does) so a test can assert
// on RevokedAt/RevokeReason after exercising a dispatch or handler path.
func getTestAgentCredential(t *testing.T, s store.AgentCredentialStore, jti string) *store.AgentCredential {
	t.Helper()
	cred, err := s.GetAgentCredentialByJTIHash(context.Background(), hashJTI(jti))
	if err != nil {
		t.Fatalf("failed to look up test agent credential for jti %q: %v", jti, err)
	}
	return cred
}

// fakeMintingTokenGenerator implements AgentTokenGenerator for dispatcher
// tests. Each call mints a fake token and records a credential to the store
// exactly as production's AgentTokenService + storeCredentialRecorder do on
// a real GenerateAgentToken call, and remembers every jti it issued (in
// call order) so a test can look up what it minted afterward.
type fakeMintingTokenGenerator struct {
	store    store.AgentCredentialStore
	jtis     []string
	failWith error
}

func (f *fakeMintingTokenGenerator) GenerateAgentToken(agentID, projectID string, ancestry []string, role AgentRole, additionalScopes []AgentTokenScope) (string, error) {
	if f.failWith != nil {
		return "", f.failWith
	}
	jti := fmt.Sprintf("test-jti-%s-%d", agentID, len(f.jtis)+1)
	f.jtis = append(f.jtis, jti)
	now := time.Now()
	cred := &store.AgentCredential{
		AgentID:      agentID,
		ProjectID:    projectID,
		TokenJTIHash: hashJTI(jti),
		IssuedAt:     now,
		ExpiresAt:    now.Add(10 * time.Hour),
	}
	if err := f.store.CreateAgentCredential(context.Background(), cred); err != nil {
		return "", err
	}
	return "faketoken-" + jti, nil
}

// lastJTI returns the most recently minted jti, for a test that only expects
// a single GenerateAgentToken call.
func (f *fakeMintingTokenGenerator) lastJTI() string {
	return f.jtis[len(f.jtis)-1]
}

// revokeFailingCredentialStore wraps a real store.Store and makes
// RevokeAgentCredentialsByAgent always fail, so a test can verify a
// revoke-store error is logged and swallowed rather than masking or
// replacing the original dispatch/handler error.
type revokeFailingCredentialStore struct {
	store.Store
	failWith error
}

func (s *revokeFailingCredentialStore) RevokeAgentCredentialsByAgent(ctx context.Context, agentID, revokedBy, reason string) (int, error) {
	return 0, s.failWith
}
