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

package secret

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/sqlite"
)

func createTestStore(t *testing.T) store.SecretStore {
	t.Helper()
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate test store: %v", err)
	}
	return s
}

func createTestBackend(t *testing.T) (*LocalBackend, store.SecretStore) {
	t.Helper()
	s := createTestStore(t)
	return NewLocalBackend(s, "test-hub-id"), s
}

// seedSecret inserts a secret directly into the store for testing read operations.
func seedSecret(t *testing.T, s store.SecretStore, sec *store.Secret) {
	t.Helper()
	if err := s.CreateSecret(context.Background(), sec); err != nil {
		t.Fatalf("failed to seed secret %s: %v", sec.Key, err)
	}
}

func TestLocalBackend_Set(t *testing.T) {
	backend, _ := createTestBackend(t)
	ctx := context.Background()

	input := &SetSecretInput{
		Name:       "API_KEY",
		Value:      "sk-test-123",
		SecretType: TypeEnvironment,
		Scope:      ScopeUser,
		ScopeID:    "user-1",
	}

	created, meta, err := backend.Set(ctx, input)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if !created {
		t.Error("expected created=true for new secret")
	}
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if meta.Name != "API_KEY" {
		t.Errorf("expected name %q, got %q", "API_KEY", meta.Name)
	}
	if meta.SecretType != TypeEnvironment {
		t.Errorf("expected type %q, got %q", TypeEnvironment, meta.SecretType)
	}

	// Verify the value was stored by reading it back
	sv, err := backend.Get(ctx, "API_KEY", ScopeUser, "user-1")
	if err != nil {
		t.Fatalf("Get after Set failed: %v", err)
	}
	if sv.Value != "sk-test-123" {
		t.Errorf("expected value %q, got %q", "sk-test-123", sv.Value)
	}

	// Update the same secret
	input.Value = "sk-updated-456"
	created, meta, err = backend.Set(ctx, input)
	if err != nil {
		t.Fatalf("Set (update) failed: %v", err)
	}
	if created {
		t.Error("expected created=false for update")
	}

	// Verify updated value
	sv, err = backend.Get(ctx, "API_KEY", ScopeUser, "user-1")
	if err != nil {
		t.Fatalf("Get after update failed: %v", err)
	}
	if sv.Value != "sk-updated-456" {
		t.Errorf("expected updated value %q, got %q", "sk-updated-456", sv.Value)
	}
}

func TestLocalBackend_SetAndResolveRoundTrip(t *testing.T) {
	backend, _ := createTestBackend(t)
	ctx := context.Background()

	// Set a secret via Set()
	_, _, err := backend.Set(ctx, &SetSecretInput{
		Name:       "GEMINI_API_KEY",
		Value:      "gemini-key-value",
		SecretType: TypeEnvironment,
		Scope:      ScopeUser,
		ScopeID:    "user-1",
	})
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Resolve should find it
	resolved, err := backend.Resolve(ctx, "user-1", "", "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved secret, got %d", len(resolved))
	}
	if resolved[0].Name != "GEMINI_API_KEY" {
		t.Errorf("expected name %q, got %q", "GEMINI_API_KEY", resolved[0].Name)
	}
	if resolved[0].Value != "gemini-key-value" {
		t.Errorf("expected value %q, got %q", "gemini-key-value", resolved[0].Value)
	}
}

func TestLocalBackend_SetUpdateIncrementsVersion(t *testing.T) {
	backend, _ := createTestBackend(t)
	ctx := context.Background()

	input := &SetSecretInput{
		Name:       "VERSION_KEY",
		Value:      "v1",
		SecretType: TypeEnvironment,
		Scope:      ScopeUser,
		ScopeID:    "user-1",
	}

	_, meta1, err := backend.Set(ctx, input)
	if err != nil {
		t.Fatalf("Set (create) failed: %v", err)
	}

	input.Value = "v2"
	_, meta2, err := backend.Set(ctx, input)
	if err != nil {
		t.Fatalf("Set (update) failed: %v", err)
	}

	if meta2.Version <= meta1.Version {
		t.Errorf("expected version to increment: v1=%d, v2=%d", meta1.Version, meta2.Version)
	}
}

func TestLocalBackend_Get(t *testing.T) {
	backend, s := createTestBackend(t)
	ctx := context.Background()

	seedSecret(t, s, &store.Secret{
		ID:             "s1",
		Key:            "API_KEY",
		EncryptedValue: "sk-test-123",
		SecretType:     store.SecretTypeEnvironment,
		Target:         "API_KEY",
		Scope:          store.ScopeUser,
		ScopeID:        "user-1",
		Description:    "Test API key",
	})

	sv, err := backend.Get(ctx, "API_KEY", ScopeUser, "user-1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if sv.Value != "sk-test-123" {
		t.Errorf("expected value %q, got %q", "sk-test-123", sv.Value)
	}
	if sv.SecretType != TypeEnvironment {
		t.Errorf("expected type %q, got %q", TypeEnvironment, sv.SecretType)
	}
}

func TestLocalBackend_Delete(t *testing.T) {
	backend, s := createTestBackend(t)
	ctx := context.Background()

	seedSecret(t, s, &store.Secret{
		ID:             "s1",
		Key:            "TO_DELETE",
		EncryptedValue: "value",
		SecretType:     store.SecretTypeEnvironment,
		Target:         "TO_DELETE",
		Scope:          store.ScopeUser,
		ScopeID:        "user-1",
	})

	if err := backend.Delete(ctx, "TO_DELETE", ScopeUser, "user-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err := backend.Get(ctx, "TO_DELETE", ScopeUser, "user-1")
	if err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestLocalBackend_DeleteNotFound(t *testing.T) {
	backend, _ := createTestBackend(t)
	ctx := context.Background()

	err := backend.Delete(ctx, "NONEXISTENT", ScopeUser, "user-1")
	if err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestLocalBackend_List(t *testing.T) {
	backend, s := createTestBackend(t)
	ctx := context.Background()

	for i, name := range []string{"A_KEY", "B_KEY", "C_KEY"} {
		seedSecret(t, s, &store.Secret{
			ID:             "s" + string(rune('1'+i)),
			Key:            name,
			EncryptedValue: "val-" + name,
			SecretType:     store.SecretTypeEnvironment,
			Target:         name,
			Scope:          store.ScopeUser,
			ScopeID:        "user-1",
		})
	}

	metas, err := backend.List(ctx, Filter{Scope: ScopeUser, ScopeID: "user-1"})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(metas) != 3 {
		t.Errorf("expected 3 secrets, got %d", len(metas))
	}
}

func TestLocalBackend_ListFilterByType(t *testing.T) {
	backend, s := createTestBackend(t)
	ctx := context.Background()

	seedSecret(t, s, &store.Secret{
		ID:             "s1",
		Key:            "ENV_KEY",
		EncryptedValue: "val",
		SecretType:     store.SecretTypeEnvironment,
		Target:         "ENV_KEY",
		Scope:          store.ScopeUser,
		ScopeID:        "user-1",
	})
	seedSecret(t, s, &store.Secret{
		ID:             "s2",
		Key:            "FILE_KEY",
		EncryptedValue: "data",
		SecretType:     store.SecretTypeFile,
		Target:         "/tmp/file",
		Scope:          store.ScopeUser,
		ScopeID:        "user-1",
	})

	metas, err := backend.List(ctx, Filter{Scope: ScopeUser, ScopeID: "user-1", Type: TypeFile})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(metas) != 1 {
		t.Errorf("expected 1 file secret, got %d", len(metas))
	}
	if metas[0].Name != "FILE_KEY" {
		t.Errorf("expected FILE_KEY, got %s", metas[0].Name)
	}
}

func TestLocalBackend_GetMeta(t *testing.T) {
	backend, s := createTestBackend(t)
	ctx := context.Background()

	seedSecret(t, s, &store.Secret{
		ID:             "s1",
		Key:            "META_KEY",
		EncryptedValue: "secret-value",
		SecretType:     store.SecretTypeVariable,
		Target:         "config",
		Scope:          store.ScopeGrove,
		ScopeID:        "grove-1",
	})

	meta, err := backend.GetMeta(ctx, "META_KEY", ScopeGrove, "grove-1")
	if err != nil {
		t.Fatalf("GetMeta failed: %v", err)
	}
	if meta.Name != "META_KEY" {
		t.Errorf("expected name %q, got %q", "META_KEY", meta.Name)
	}
	if meta.SecretType != TypeVariable {
		t.Errorf("expected type %q, got %q", TypeVariable, meta.SecretType)
	}
}

func TestLocalBackend_Resolve(t *testing.T) {
	backend, s := createTestBackend(t)
	ctx := context.Background()

	// User-level secrets
	seedSecret(t, s, &store.Secret{
		ID:             "s1",
		Key:            "API_KEY",
		EncryptedValue: "user-api-key",
		SecretType:     store.SecretTypeEnvironment,
		Target:         "API_KEY",
		Scope:          store.ScopeUser,
		ScopeID:        "user-1",
	})
	seedSecret(t, s, &store.Secret{
		ID:             "s2",
		Key:            "TLS_CERT",
		EncryptedValue: "cert-data",
		SecretType:     store.SecretTypeFile,
		Target:         "/etc/ssl/cert.pem",
		Scope:          store.ScopeUser,
		ScopeID:        "user-1",
	})

	// Grove-level override
	seedSecret(t, s, &store.Secret{
		ID:             "s3",
		Key:            "API_KEY",
		EncryptedValue: "grove-api-key",
		SecretType:     store.SecretTypeEnvironment,
		Target:         "API_KEY",
		Scope:          store.ScopeGrove,
		ScopeID:        "grove-1",
	})
	seedSecret(t, s, &store.Secret{
		ID:             "s4",
		Key:            "DB_PASS",
		EncryptedValue: "grove-db-pass",
		SecretType:     store.SecretTypeEnvironment,
		Target:         "DATABASE_PASSWORD",
		Scope:          store.ScopeGrove,
		ScopeID:        "grove-1",
	})

	resolved, err := backend.Resolve(ctx, "user-1", "grove-1", "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	byName := make(map[string]SecretWithValue)
	for _, sv := range resolved {
		byName[sv.Name] = sv
	}

	// API_KEY overridden by grove
	apiKey, ok := byName["API_KEY"]
	if !ok {
		t.Fatal("expected API_KEY in resolved secrets")
	}
	if apiKey.Value != "grove-api-key" {
		t.Errorf("expected grove API_KEY value %q, got %q", "grove-api-key", apiKey.Value)
	}
	if apiKey.Scope != ScopeGrove {
		t.Errorf("expected API_KEY scope %q, got %q", ScopeGrove, apiKey.Scope)
	}

	// TLS_CERT from user (no override)
	cert, ok := byName["TLS_CERT"]
	if !ok {
		t.Fatal("expected TLS_CERT in resolved secrets")
	}
	if cert.SecretType != TypeFile {
		t.Errorf("expected TLS_CERT type %q, got %q", TypeFile, cert.SecretType)
	}
	if cert.Target != "/etc/ssl/cert.pem" {
		t.Errorf("expected TLS_CERT target %q, got %q", "/etc/ssl/cert.pem", cert.Target)
	}

	// DB_PASS from grove
	dbPass, ok := byName["DB_PASS"]
	if !ok {
		t.Fatal("expected DB_PASS in resolved secrets")
	}
	if dbPass.Target != "DATABASE_PASSWORD" {
		t.Errorf("expected DB_PASS target %q, got %q", "DATABASE_PASSWORD", dbPass.Target)
	}

	if len(resolved) != 3 {
		t.Errorf("expected 3 resolved secrets, got %d", len(resolved))
	}
}

func TestLocalBackend_ResolveNoScopes(t *testing.T) {
	backend, _ := createTestBackend(t)
	ctx := context.Background()

	resolved, err := backend.Resolve(ctx, "", "", "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("expected 0 resolved secrets, got %d", len(resolved))
	}
}

func TestLocalBackend_ResolveBrokerOverride(t *testing.T) {
	backend, s := createTestBackend(t)
	ctx := context.Background()

	seedSecret(t, s, &store.Secret{
		ID:             "s1",
		Key:            "API_KEY",
		EncryptedValue: "user-key",
		SecretType:     store.SecretTypeEnvironment,
		Target:         "API_KEY",
		Scope:          store.ScopeUser,
		ScopeID:        "user-1",
	})
	seedSecret(t, s, &store.Secret{
		ID:             "s2",
		Key:            "API_KEY",
		EncryptedValue: "broker-key",
		SecretType:     store.SecretTypeEnvironment,
		Target:         "API_KEY",
		Scope:          store.ScopeRuntimeBroker,
		ScopeID:        "broker-1",
	})

	resolved, err := backend.Resolve(ctx, "user-1", "", "broker-1")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved secret, got %d", len(resolved))
	}
	if resolved[0].Value != "broker-key" {
		t.Errorf("expected broker override %q, got %q", "broker-key", resolved[0].Value)
	}
	if resolved[0].Scope != ScopeRuntimeBroker {
		t.Errorf("expected scope %q, got %q", ScopeRuntimeBroker, resolved[0].Scope)
	}
}

func TestLocalBackend_ResolveExcludesInternalSecrets(t *testing.T) {
	backend, s := createTestBackend(t)
	ctx := context.Background()

	// Seed an internal signing key at hub scope (simulates hub signing keys)
	seedSecret(t, s, &store.Secret{
		ID:             "signing-1",
		Key:            "agent_signing_key",
		EncryptedValue: "super-secret-key-material",
		SecretType:     store.SecretTypeInternal,
		Target:         "agent_signing_key",
		Scope:          store.ScopeHub,
		ScopeID:        "test-hub-id",
	})

	// Seed a normal hub-scoped environment secret
	seedSecret(t, s, &store.Secret{
		ID:             "hub-env-1",
		Key:            "HUB_API_TOKEN",
		EncryptedValue: "hub-token-value",
		SecretType:     store.SecretTypeEnvironment,
		Target:         "HUB_API_TOKEN",
		Scope:          store.ScopeHub,
		ScopeID:        "test-hub-id",
	})

	// Seed a user-scoped secret
	seedSecret(t, s, &store.Secret{
		ID:             "user-env-1",
		Key:            "USER_KEY",
		EncryptedValue: "user-key-value",
		SecretType:     store.SecretTypeEnvironment,
		Target:         "USER_KEY",
		Scope:          store.ScopeUser,
		ScopeID:        "user-1",
	})

	resolved, err := backend.Resolve(ctx, "user-1", "", "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	byName := make(map[string]SecretWithValue)
	for _, sv := range resolved {
		byName[sv.Name] = sv
	}

	// Internal signing key must NOT be present
	if _, ok := byName["agent_signing_key"]; ok {
		t.Error("internal secret 'agent_signing_key' must not be included in resolved secrets")
	}

	// Normal hub secret should be present
	if _, ok := byName["HUB_API_TOKEN"]; !ok {
		t.Error("expected HUB_API_TOKEN in resolved secrets")
	}

	// User secret should be present
	if _, ok := byName["USER_KEY"]; !ok {
		t.Error("expected USER_KEY in resolved secrets")
	}

	if len(resolved) != 2 {
		t.Errorf("expected 2 resolved secrets, got %d", len(resolved))
	}
}
