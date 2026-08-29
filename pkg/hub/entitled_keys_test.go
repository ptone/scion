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
	"fmt"
	"sort"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// fakeSecretStoreForEntitlement is a minimal SecretStore that returns
// pre-configured secrets from ListSecrets/ListProgenySecrets. It does NOT
// implement value retrieval — the point is that entitlement comes from
// the listing, not from values.
type fakeSecretStoreForEntitlement struct {
	store.SecretStore
	secrets       map[string][]store.Secret // scope:scopeID -> secrets
	progeny       []store.Secret
	listErr       error
	progenyErr    error
}

func (f *fakeSecretStoreForEntitlement) ListSecrets(_ context.Context, filter store.SecretFilter) ([]store.Secret, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	key := filter.Scope + ":" + filter.ScopeID
	return f.secrets[key], nil
}

func (f *fakeSecretStoreForEntitlement) ListProgenySecrets(_ context.Context, _ []string) ([]store.Secret, error) {
	if f.progenyErr != nil {
		return nil, f.progenyErr
	}
	return f.progeny, nil
}

// fakeSecretBackendForEntitlement wraps a SecretStore and provides a
// minimal SecretBackend that returns a hubID but whose Resolve deliberately
// DROPS a secret (simulating a decryption failure). This is the case that
// distinguishes listing-based entitlement from resolution-based entitlement.
type fakeSecretBackendForEntitlement struct {
	hubID         string
	store         *fakeSecretStoreForEntitlement
	dropOnResolve map[string]bool // keys to silently drop in Resolve
}

func (f *fakeSecretBackendForEntitlement) HubID() string { return f.hubID }

func (f *fakeSecretBackendForEntitlement) List(_ context.Context, filter secret.Filter) ([]secret.SecretMeta, error) {
	ss, err := f.store.ListSecrets(context.Background(), store.SecretFilter{
		Scope:   filter.Scope,
		ScopeID: filter.ScopeID,
	})
	if err != nil {
		return nil, err
	}
	result := make([]secret.SecretMeta, len(ss))
	for i, s := range ss {
		result[i] = secret.SecretMeta{
			ID:   s.ID,
			Name: s.Key,
		}
	}
	return result, nil
}

func (f *fakeSecretBackendForEntitlement) Resolve(_ context.Context, userID, projectID, brokerID string, _ *secret.ResolveOpts) ([]secret.SecretWithValue, error) {
	// Simulate Resolve's best-effort behavior: list all secrets, but
	// silently skip any whose key is in dropOnResolve (as if decryption
	// failed).
	var result []secret.SecretWithValue
	for _, secrets := range f.store.secrets {
		for _, s := range secrets {
			if f.dropOnResolve[s.Key] {
				continue // simulates decryptRawValue failure
			}
			result = append(result, secret.SecretWithValue{
				SecretMeta: secret.SecretMeta{Name: s.Key},
				Value:      "value-of-" + s.Key,
			})
		}
	}
	return result, nil
}

// Unused SecretBackend methods — satisfy the interface.
func (f *fakeSecretBackendForEntitlement) Get(context.Context, string, string, string) (*secret.SecretWithValue, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeSecretBackendForEntitlement) Set(context.Context, *secret.SetSecretInput) (bool, *secret.SecretMeta, error) {
	return false, nil, fmt.Errorf("not implemented")
}
func (f *fakeSecretBackendForEntitlement) Delete(context.Context, string, string, string) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeSecretBackendForEntitlement) GetMeta(context.Context, string, string, string) (*secret.SecretMeta, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeSecretBackendForEntitlement) UpdateMeta(context.Context, *secret.UpdateMetaInput) (*secret.SecretMeta, error) {
	return nil, fmt.Errorf("not implemented")
}

// TestComputeEntitledSecretKeys_ListingNotResolution is the R7 distinguishing
// test. It verifies that a secret which EXISTS but whose VALUE fails to
// resolve is still present in the entitled key set. Under the old
// resolution-based computation, this key would be absent.
func TestComputeEntitledSecretKeys_ListingNotResolution(t *testing.T) {
	fakeStore := &fakeSecretStoreForEntitlement{
		secrets: map[string][]store.Secret{
			"project:proj-1": {
				{Key: "API_KEY", SecretType: store.SecretTypeEnvironment},
				{Key: "BROKEN_KEY", SecretType: store.SecretTypeEnvironment}, // value will fail to resolve
				{Key: "DB_PASSWORD", SecretType: store.SecretTypeEnvironment},
			},
		},
	}
	backend := &fakeSecretBackendForEntitlement{
		hubID:         "hub-1",
		store:         fakeStore,
		dropOnResolve: map[string]bool{"BROKEN_KEY": true},
	}
	agent := &store.Agent{
		ID:        "agent-1",
		ProjectID: "proj-1",
		OwnerID:   "",
	}

	// computeEntitledSecretKeys derives from the listing — all three keys.
	keys, err := computeEntitledSecretKeys(context.Background(), backend, fakeStore, nil, agent)
	if err != nil {
		t.Fatalf("computeEntitledSecretKeys: %v", err)
	}
	sort.Strings(keys)
	if len(keys) != 3 {
		t.Fatalf("expected 3 entitled keys, got %d: %v", len(keys), keys)
	}
	expected := []string{"API_KEY", "BROKEN_KEY", "DB_PASSWORD"}
	for i, k := range expected {
		if keys[i] != k {
			t.Errorf("keys[%d] = %q, want %q", i, keys[i], k)
		}
	}

	// Contrast: Resolve would have dropped BROKEN_KEY.
	resolved, resolveErr := backend.Resolve(context.Background(), "", "proj-1", "", nil)
	if resolveErr != nil {
		t.Fatalf("Resolve: %v", resolveErr)
	}
	resolvedNames := make(map[string]bool)
	for _, sv := range resolved {
		resolvedNames[sv.Name] = true
	}
	if resolvedNames["BROKEN_KEY"] {
		t.Fatal("expected Resolve to drop BROKEN_KEY (simulated decryption failure)")
	}
	if !resolvedNames["API_KEY"] || !resolvedNames["DB_PASSWORD"] {
		t.Fatal("expected Resolve to include API_KEY and DB_PASSWORD")
	}
}

// TestComputeEntitledSecretKeys_NilBackend verifies that a nil secret
// backend returns nil entitled keys (not an error).
func TestComputeEntitledSecretKeys_NilBackend(t *testing.T) {
	keys, err := computeEntitledSecretKeys(context.Background(), nil, nil, nil, &store.Agent{})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if keys != nil {
		t.Fatalf("expected nil keys, got: %v", keys)
	}
}

// TestComputeEntitledSecretKeys_EmptyProject verifies that a project with
// no secrets configured returns an empty (non-nil) entitled key set.
func TestComputeEntitledSecretKeys_EmptyProject(t *testing.T) {
	fakeStore := &fakeSecretStoreForEntitlement{
		secrets: map[string][]store.Secret{},
	}
	backend := &fakeSecretBackendForEntitlement{
		hubID: "hub-1",
		store: fakeStore,
	}
	agent := &store.Agent{
		ID:        "agent-1",
		ProjectID: "proj-1",
	}

	keys, err := computeEntitledSecretKeys(context.Background(), backend, fakeStore, nil, agent)
	if err != nil {
		t.Fatalf("computeEntitledSecretKeys: %v", err)
	}
	if keys == nil {
		t.Fatal("expected non-nil (empty) keys, got nil")
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d: %v", len(keys), keys)
	}
}

// TestComputeEntitledSecretKeys_ListingError verifies that a listing
// failure returns an error (callers must NOT record an empty set).
func TestComputeEntitledSecretKeys_ListingError(t *testing.T) {
	fakeStore := &fakeSecretStoreForEntitlement{
		secrets: map[string][]store.Secret{},
		listErr: fmt.Errorf("database connection lost"),
	}
	backend := &fakeSecretBackendForEntitlement{
		hubID: "hub-1",
		store: fakeStore,
	}
	agent := &store.Agent{
		ID:        "agent-1",
		ProjectID: "proj-1",
	}

	keys, err := computeEntitledSecretKeys(context.Background(), backend, fakeStore, nil, agent)
	if err == nil {
		t.Fatal("expected error on listing failure, got nil")
	}
	if keys != nil {
		t.Fatalf("expected nil keys on error, got: %v", keys)
	}
}

// TestComputeEntitledSecretKeys_ExcludesInternal verifies that internal
// secrets (e.g. signing keys) are excluded from the entitled set.
func TestComputeEntitledSecretKeys_ExcludesInternal(t *testing.T) {
	fakeStore := &fakeSecretStoreForEntitlement{
		secrets: map[string][]store.Secret{
			"project:proj-1": {
				{Key: "USER_SECRET", SecretType: store.SecretTypeEnvironment},
				{Key: "SIGNING_KEY", SecretType: store.SecretTypeInternal},
			},
		},
	}
	backend := &fakeSecretBackendForEntitlement{
		hubID: "hub-1",
		store: fakeStore,
	}
	agent := &store.Agent{
		ID:        "agent-1",
		ProjectID: "proj-1",
	}

	keys, err := computeEntitledSecretKeys(context.Background(), backend, fakeStore, nil, agent)
	if err != nil {
		t.Fatalf("computeEntitledSecretKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "USER_SECRET" {
		t.Fatalf("expected [USER_SECRET], got: %v", keys)
	}
}

// TestComputeEntitledSecretKeys_MultiScope verifies deduplication across
// scopes: a key present in both project and hub scope appears once.
func TestComputeEntitledSecretKeys_MultiScope(t *testing.T) {
	fakeStore := &fakeSecretStoreForEntitlement{
		secrets: map[string][]store.Secret{
			"hub:hub-1": {
				{Key: "SHARED_KEY", SecretType: store.SecretTypeEnvironment},
				{Key: "HUB_ONLY", SecretType: store.SecretTypeEnvironment},
			},
			"project:proj-1": {
				{Key: "SHARED_KEY", SecretType: store.SecretTypeEnvironment}, // same key, higher precedence
				{Key: "PROJECT_ONLY", SecretType: store.SecretTypeEnvironment},
			},
		},
	}
	backend := &fakeSecretBackendForEntitlement{
		hubID: "hub-1",
		store: fakeStore,
	}
	agent := &store.Agent{
		ID:        "agent-1",
		ProjectID: "proj-1",
	}

	keys, err := computeEntitledSecretKeys(context.Background(), backend, fakeStore, nil, agent)
	if err != nil {
		t.Fatalf("computeEntitledSecretKeys: %v", err)
	}
	sort.Strings(keys)
	expected := []string{"HUB_ONLY", "PROJECT_ONLY", "SHARED_KEY"}
	if len(keys) != len(expected) {
		t.Fatalf("expected %d keys, got %d: %v", len(expected), len(keys), keys)
	}
	for i, k := range expected {
		if keys[i] != k {
			t.Errorf("keys[%d] = %q, want %q", i, keys[i], k)
		}
	}
}
