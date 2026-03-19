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

package sqlite

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
)

func TestSecretCRUDWithType(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	t.Run("create secret with default type", func(t *testing.T) {
		secret := &store.Secret{
			ID:             uuid.New().String(),
			Key:            "API_KEY",
			EncryptedValue: "encrypted-value",
			Scope:          store.ScopeUser,
			ScopeID:        "user-1",
		}
		if err := s.CreateSecret(ctx, secret); err != nil {
			t.Fatalf("CreateSecret failed: %v", err)
		}
		if secret.SecretType != store.SecretTypeEnvironment {
			t.Errorf("expected default SecretType %q, got %q", store.SecretTypeEnvironment, secret.SecretType)
		}
		if secret.Target != "API_KEY" {
			t.Errorf("expected default Target %q, got %q", "API_KEY", secret.Target)
		}
	})

	t.Run("get secret returns type and target", func(t *testing.T) {
		got, err := s.GetSecret(ctx, "API_KEY", store.ScopeUser, "user-1")
		if err != nil {
			t.Fatalf("GetSecret failed: %v", err)
		}
		if got.SecretType != store.SecretTypeEnvironment {
			t.Errorf("expected SecretType %q, got %q", store.SecretTypeEnvironment, got.SecretType)
		}
		if got.Target != "API_KEY" {
			t.Errorf("expected Target %q, got %q", "API_KEY", got.Target)
		}
	})

	t.Run("create file secret with explicit type and target", func(t *testing.T) {
		secret := &store.Secret{
			ID:             uuid.New().String(),
			Key:            "TLS_CERT",
			EncryptedValue: "cert-data",
			SecretType:     store.SecretTypeFile,
			Target:         "/etc/ssl/certs/cert.pem",
			Scope:          store.ScopeUser,
			ScopeID:        "user-1",
		}
		if err := s.CreateSecret(ctx, secret); err != nil {
			t.Fatalf("CreateSecret failed: %v", err)
		}

		got, err := s.GetSecret(ctx, "TLS_CERT", store.ScopeUser, "user-1")
		if err != nil {
			t.Fatalf("GetSecret failed: %v", err)
		}
		if got.SecretType != store.SecretTypeFile {
			t.Errorf("expected SecretType %q, got %q", store.SecretTypeFile, got.SecretType)
		}
		if got.Target != "/etc/ssl/certs/cert.pem" {
			t.Errorf("expected Target %q, got %q", "/etc/ssl/certs/cert.pem", got.Target)
		}
	})

	t.Run("create variable secret", func(t *testing.T) {
		secret := &store.Secret{
			ID:             uuid.New().String(),
			Key:            "CONFIG_JSON",
			EncryptedValue: `{"key":"value"}`,
			SecretType:     store.SecretTypeVariable,
			Target:         "config",
			Scope:          store.ScopeUser,
			ScopeID:        "user-1",
		}
		if err := s.CreateSecret(ctx, secret); err != nil {
			t.Fatalf("CreateSecret failed: %v", err)
		}

		got, err := s.GetSecret(ctx, "CONFIG_JSON", store.ScopeUser, "user-1")
		if err != nil {
			t.Fatalf("GetSecret failed: %v", err)
		}
		if got.SecretType != store.SecretTypeVariable {
			t.Errorf("expected SecretType %q, got %q", store.SecretTypeVariable, got.SecretType)
		}
		if got.Target != "config" {
			t.Errorf("expected Target %q, got %q", "config", got.Target)
		}
	})

	t.Run("update secret preserves type and target", func(t *testing.T) {
		got, err := s.GetSecret(ctx, "TLS_CERT", store.ScopeUser, "user-1")
		if err != nil {
			t.Fatalf("GetSecret failed: %v", err)
		}

		got.EncryptedValue = "updated-cert-data"
		got.Target = "/etc/ssl/certs/new-cert.pem"
		if err := s.UpdateSecret(ctx, got); err != nil {
			t.Fatalf("UpdateSecret failed: %v", err)
		}

		updated, err := s.GetSecret(ctx, "TLS_CERT", store.ScopeUser, "user-1")
		if err != nil {
			t.Fatalf("GetSecret after update failed: %v", err)
		}
		if updated.SecretType != store.SecretTypeFile {
			t.Errorf("expected SecretType %q after update, got %q", store.SecretTypeFile, updated.SecretType)
		}
		if updated.Target != "/etc/ssl/certs/new-cert.pem" {
			t.Errorf("expected Target %q after update, got %q", "/etc/ssl/certs/new-cert.pem", updated.Target)
		}
		if updated.Version != 2 {
			t.Errorf("expected Version 2, got %d", updated.Version)
		}
	})

	t.Run("list secrets returns type and target", func(t *testing.T) {
		secrets, err := s.ListSecrets(ctx, store.SecretFilter{
			Scope:   store.ScopeUser,
			ScopeID: "user-1",
		})
		if err != nil {
			t.Fatalf("ListSecrets failed: %v", err)
		}
		if len(secrets) != 3 {
			t.Fatalf("expected 3 secrets, got %d", len(secrets))
		}

		// Secrets should be ordered by key
		for _, s := range secrets {
			if s.SecretType == "" {
				t.Errorf("secret %q has empty SecretType", s.Key)
			}
			if s.Target == "" {
				t.Errorf("secret %q has empty Target", s.Key)
			}
		}
	})

	t.Run("list secrets with type filter", func(t *testing.T) {
		secrets, err := s.ListSecrets(ctx, store.SecretFilter{
			Scope:   store.ScopeUser,
			ScopeID: "user-1",
			Type:    store.SecretTypeFile,
		})
		if err != nil {
			t.Fatalf("ListSecrets with type filter failed: %v", err)
		}
		if len(secrets) != 1 {
			t.Fatalf("expected 1 file secret, got %d", len(secrets))
		}
		if secrets[0].Key != "TLS_CERT" {
			t.Errorf("expected key %q, got %q", "TLS_CERT", secrets[0].Key)
		}
	})

	t.Run("upsert secret with type", func(t *testing.T) {
		secret := &store.Secret{
			ID:             uuid.New().String(),
			Key:            "NEW_SECRET",
			EncryptedValue: "value",
			SecretType:     store.SecretTypeVariable,
			Target:         "new_key",
			Scope:          store.ScopeUser,
			ScopeID:        "user-1",
		}

		created, err := s.UpsertSecret(ctx, secret)
		if err != nil {
			t.Fatalf("UpsertSecret (create) failed: %v", err)
		}
		if !created {
			t.Error("expected UpsertSecret to create a new secret")
		}

		// Upsert again (update)
		secret.EncryptedValue = "updated-value"
		created, err = s.UpsertSecret(ctx, secret)
		if err != nil {
			t.Fatalf("UpsertSecret (update) failed: %v", err)
		}
		if created {
			t.Error("expected UpsertSecret to update existing secret")
		}

		got, err := s.GetSecret(ctx, "NEW_SECRET", store.ScopeUser, "user-1")
		if err != nil {
			t.Fatalf("GetSecret after upsert failed: %v", err)
		}
		if got.SecretType != store.SecretTypeVariable {
			t.Errorf("expected SecretType %q, got %q", store.SecretTypeVariable, got.SecretType)
		}
	})
}
