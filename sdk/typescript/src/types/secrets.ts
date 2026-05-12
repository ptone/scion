/**
 * Type definitions for the Secret resource.
 *
 * @packageDocumentation
 */

/** Secret metadata as returned by the Scion Hub API. Values are never returned. */
export interface Secret {
  /** Hub UUID. */
  id: string;
  /** Secret key name. */
  key: string;
  /** External secret reference (e.g. "gcpsm:projects/123/secrets/name"). */
  secretRef?: string;
  /** Secret type: environment, variable, or file. */
  type: string;
  /** Projection target (env var name, JSON key, or file path). */
  target?: string;
  /** Scope: user, project, or runtime_broker. */
  scope: string;
  /** ID of the scoped entity. */
  scopeId: string;
  /** Human-readable description. */
  description?: string;
  /** Injection mode: always or as_needed. */
  injectionMode?: string;
  /** Whether creator's progeny agents can access this secret. */
  allowProgeny?: boolean;
  /** Secret version number. */
  version: number;
  /** Creation timestamp (ISO 8601). */
  created: string;
  /** Last-updated timestamp (ISO 8601). */
  updated: string;
  /** ID of the user who created this secret. */
  createdBy?: string;
  /** ID of the user who last updated this secret. */
  updatedBy?: string;
}

/** Request body for setting (creating or updating) a secret. */
export interface SetSecretRequest {
  /** The secret value (write-only, never returned). */
  value: string;
  /** Scope type. Defaults to "user". */
  scope?: string;
  /** Scope entity ID. Required for project/runtime_broker scopes. */
  scopeId?: string;
  /** Human-readable description. */
  description?: string;
  /** Injection mode: "always" or "as_needed". Defaults to "as_needed". */
  injectionMode?: string;
  /** Secret type: environment (default), variable, or file. */
  type?: string;
  /** Projection target. Defaults to the key name. */
  target?: string;
  /** Allow creator's progeny agents to access (user scope only). */
  allowProgeny?: boolean;
}

/** Response from setting a secret. */
export interface SetSecretResponse {
  /** Metadata of the created/updated secret (no value). */
  secret: Secret;
  /** Whether a new secret was created (vs. updated). */
  created: boolean;
}

/** Scope parameters for get/delete operations. */
export interface SecretScopeOptions {
  /** Scope type: user, project, or runtime_broker. Defaults to "user". */
  scope?: string;
  /** Scope entity ID. Required for project/runtime_broker scopes. */
  scopeId?: string;
}

/** Options for listing secrets. */
export interface ListSecretsOptions extends SecretScopeOptions {
  /** Filter by secret type: environment, variable, or file. */
  type?: string;
}

/** Response from listing secrets. */
export interface ListSecretResponse {
  /** Secret metadata entries (no values). */
  secrets: Secret[];
  /** The scope these secrets belong to. */
  scope: string;
  /** The scope entity ID. */
  scopeId: string;
}
