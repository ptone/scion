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

/**
 * Secret represents secret metadata from the Hub API.
 * Note: Secret values are never returned by the API.
 */
export interface Secret {
  /** Unique identifier. */
  id: string;

  /** Secret key name. */
  key: string;

  /** External secret reference (e.g., "gcpsm:projects/123/secrets/name"). */
  secretRef?: string;

  /** Secret type: "environment" (default), "variable", or "file". */
  type: string;

  /** Projection target (defaults to key). */
  target?: string;

  /** Scope type: "user", "project", or "runtime_broker". */
  scope: string;

  /** ID of the scoped entity. */
  scopeId: string;

  /** Optional description. */
  description?: string;

  /** Injection mode: "always" or "as_needed". */
  injectionMode?: string;

  /** Whether creator's progeny agents can access this secret (user scope only). */
  allowProgeny?: boolean;

  /** Secret version number. */
  version: number;

  /** Creation timestamp. */
  created: string;

  /** Last update timestamp. */
  updated: string;

  /** User ID who created the secret. */
  createdBy?: string;

  /** User ID who last updated the secret. */
  updatedBy?: string;
}

/**
 * Options for listing secrets.
 */
export interface ListSecretsParams {
  /** Scope type: "user", "project", or "runtime_broker". Defaults to "user". */
  scope?: string;

  /** ID of the scoped entity (required for project/runtime_broker scope). */
  scopeId?: string;

  /** Filter by secret type: "environment", "variable", or "file". */
  type?: string;
}

/**
 * Options for get/delete operations on a specific secret.
 */
export interface SecretScopeParams {
  /** Scope type: "user", "project", or "runtime_broker". Defaults to "user". */
  scope?: string;

  /** ID of the scoped entity (required for project/runtime_broker scope). */
  scopeId?: string;
}

/**
 * Parameters for creating or updating a secret.
 */
export interface SetSecretParams {
  /** Secret value (write-only, required). */
  value: string;

  /** Scope type: "user", "project", or "runtime_broker". Defaults to "user". */
  scope?: string;

  /** ID of the scoped entity (required for project/runtime_broker scope). */
  scopeId?: string;

  /** Optional description. */
  description?: string;

  /** Injection mode: "always" or "as_needed" (default: "as_needed"). */
  injectionMode?: string;

  /** Secret type: "environment" (default), "variable", or "file". */
  type?: string;

  /** Projection target (defaults to key). */
  target?: string;

  /** Allow creator's progeny agents to access this secret (user scope only). */
  allowProgeny?: boolean;
}

/**
 * Response from creating or updating a secret.
 */
export interface SetSecretResponse {
  /** Secret metadata (no value). */
  secret: Secret;

  /** Whether this was a newly created secret. */
  created: boolean;
}

/**
 * Response from listing secrets.
 */
export interface ListSecretsResponse {
  /** Secret metadata items (no values). */
  secrets: Secret[];

  /** Scope of the listed secrets. */
  scope: string;

  /** Scope ID of the listed secrets. */
  scopeId: string;
}
