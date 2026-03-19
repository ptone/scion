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

// Package secret provides the SecretBackend abstraction layer for secret storage.
// It sits between Hub handlers/dispatcher and the underlying secret storage,
// enabling pluggable backends (local SQLite, GCP Secret Manager, etc.).
package secret

import (
	"context"
	"errors"
	"time"
)

// ErrNoSecretBackend is returned when a secret operation requires a production
// secrets backend (e.g., GCP Secret Manager) but only the local backend is configured.
// The local backend does not encrypt secret values, so write operations are rejected.
var ErrNoSecretBackend = errors.New("secret storage requires a configured secrets backend; set SCION_SERVER_SECRETS_BACKEND=gcpsm")

// Secret type constants define how a secret is projected into the agent container.
const (
	TypeEnvironment = "environment" // Injected as environment variable (default)
	TypeVariable    = "variable"    // Written to ~/.scion/secrets.json for programmatic access
	TypeFile        = "file"        // Written to a file at the specified Target path
)

// Scope constants define the visibility of a secret.
const (
	ScopeUser          = "user"
	ScopeGrove         = "grove"
	ScopeRuntimeBroker = "runtime_broker"
)

// Filter specifies criteria for listing secrets.
type Filter struct {
	Scope   string // Required: user, grove, runtime_broker
	ScopeID string // Required: ID of the scoped entity
	Type    string // Optional: filter by secret type (environment, variable, file)
	Name    string // Optional: filter by specific key name
}

// SecretMeta holds secret metadata without the secret value.
type SecretMeta struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`    // Secret key name (e.g., "API_KEY")
	SecretType    string    `json:"type"`    // environment, variable, file
	Target        string    `json:"target"`  // Projection target
	Scope         string    `json:"scope"`   // user, grove, runtime_broker
	ScopeID       string    `json:"scopeId"` // ID of the scoped entity
	Description   string    `json:"description,omitempty"`
	InjectionMode string    `json:"injectionMode,omitempty"` // "always" or "as_needed"
	SecretRef     string    `json:"secretRef,omitempty"`     // External reference (e.g., GCP SM resource path)
	Version       int       `json:"version"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`
	CreatedBy     string    `json:"createdBy,omitempty"`
	UpdatedBy     string    `json:"updatedBy,omitempty"`
}

// SecretWithValue embeds SecretMeta and adds the plaintext secret value.
type SecretWithValue struct {
	SecretMeta
	Value string `json:"-"` // Never serialized
}

// SetSecretInput provides the data needed to create or update a secret.
type SetSecretInput struct {
	Name          string // Secret key name
	Value         string // Plaintext secret value
	SecretType    string // environment, variable, file
	Target        string // Projection target
	Scope         string // user, grove, runtime_broker
	ScopeID       string // ID of the scoped entity
	Description   string // Optional description
	InjectionMode string // "always" or "as_needed"
	CreatedBy     string // User ID of creator (for new secrets)
	UpdatedBy     string // User ID of updater
	UserEmail     string // Email of the user (for labeling user-scoped secrets)
}

// SecretBackend defines the interface for secret storage operations.
// Implementations include local (wrapping store.SecretStore) and GCP Secret Manager.
type SecretBackend interface {
	// Get retrieves a secret including its value.
	Get(ctx context.Context, name, scope, scopeID string) (*SecretWithValue, error)

	// Set creates or updates a secret. Returns whether a new secret was created.
	Set(ctx context.Context, input *SetSecretInput) (created bool, meta *SecretMeta, err error)

	// Delete removes a secret.
	Delete(ctx context.Context, name, scope, scopeID string) error

	// List returns secret metadata matching the filter. Values are not included.
	List(ctx context.Context, filter Filter) ([]SecretMeta, error)

	// GetMeta retrieves secret metadata without the value.
	GetMeta(ctx context.Context, name, scope, scopeID string) (*SecretMeta, error)

	// Resolve collects and merges secrets from all applicable scopes for an agent.
	// Scopes are resolved in order: user < grove < runtime_broker (later overrides earlier).
	Resolve(ctx context.Context, userID, groveID, brokerID string) ([]SecretWithValue, error)
}
