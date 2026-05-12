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

import { BaseResource, Page } from "../resource";
import {
  Secret,
  ListSecretsParams,
  ListSecretsResponse,
  SecretScopeParams,
  SetSecretParams,
  SetSecretResponse,
} from "../types/secrets";

/**
 * SecretsResource provides methods for managing secrets in the Scion Hub API.
 *
 * Secrets are scoped to a user, project, or runtime broker. Secret values are
 * write-only and are never returned by the API — only metadata is accessible
 * via list and get operations.
 *
 * @example
 * ```typescript
 * const client = new ScionClient("https://hub.example.com", { token: "..." });
 *
 * // List user-scoped secrets
 * const page = await client.secrets.list();
 *
 * // Set a secret
 * const result = await client.secrets.set("MY_API_KEY", {
 *   value: "sk-...",
 *   description: "API key for external service",
 * });
 *
 * // Get secret metadata
 * const secret = await client.secrets.get("MY_API_KEY");
 *
 * // Delete a secret
 * await client.secrets.delete("MY_API_KEY");
 * ```
 */
export class SecretsResource extends BaseResource {
  /**
   * Lists secret metadata for the specified scope.
   *
   * Returns metadata only — secret values are never included in list responses.
   *
   * @param params - Optional parameters to filter the list.
   * @param params.scope - Scope type: "user", "project", or "runtime_broker". Defaults to "user".
   * @param params.scopeId - ID of the scoped entity (required for project/runtime_broker scope).
   * @param params.type - Filter by secret type: "environment", "variable", or "file".
   * @returns A page of secret metadata.
   *
   * @example
   * ```typescript
   * // List all user secrets
   * const userSecrets = await client.secrets.list();
   *
   * // List project-scoped secrets
   * const projectSecrets = await client.secrets.list({
   *   scope: "project",
   *   scopeId: "proj-123",
   * });
   *
   * // List only environment-type secrets
   * const envSecrets = await client.secrets.list({ type: "environment" });
   * ```
   */
  async list(params?: ListSecretsParams): Promise<Page<Secret>> {
    const query = this.buildQuery({
      scope: params?.scope,
      scopeId: params?.scopeId,
      type: params?.type,
    });

    const response = await this.transport.get<ListSecretsResponse>(
      "/api/v1/secrets",
      query,
    );

    return {
      data: response.secrets,
      scope: response.scope,
      scopeId: response.scopeId,
    };
  }

  /**
   * Returns metadata for a specific secret by key.
   *
   * The secret value is never returned — only metadata is accessible.
   *
   * @param key - The secret key name.
   * @param params - Optional scope parameters.
   * @param params.scope - Scope type: "user", "project", or "runtime_broker". Defaults to "user".
   * @param params.scopeId - ID of the scoped entity (required for project/runtime_broker scope).
   * @returns The secret metadata.
   * @throws {ScionError} If the secret is not found (404).
   *
   * @example
   * ```typescript
   * const secret = await client.secrets.get("MY_API_KEY");
   * console.log(secret.key, secret.scope, secret.version);
   *
   * // Get a project-scoped secret
   * const projectSecret = await client.secrets.get("DB_PASSWORD", {
   *   scope: "project",
   *   scopeId: "proj-123",
   * });
   * ```
   */
  async get(key: string, params?: SecretScopeParams): Promise<Secret> {
    const query = this.buildQuery({
      scope: params?.scope,
      scopeId: params?.scopeId,
    });

    return this.transport.get<Secret>(
      `/api/v1/secrets/${encodeURIComponent(key)}`,
      query,
    );
  }

  /**
   * Creates or updates a secret.
   *
   * If the secret already exists for the given scope, it is updated. Otherwise,
   * a new secret is created. The response indicates whether the secret was newly
   * created via the `created` field.
   *
   * @param key - The secret key name.
   * @param params - The secret parameters including value and optional metadata.
   * @param params.value - The secret value (write-only, required).
   * @param params.scope - Scope type: "user", "project", or "runtime_broker". Defaults to "user".
   * @param params.scopeId - ID of the scoped entity (required for project/runtime_broker scope).
   * @param params.description - Optional description of the secret.
   * @param params.injectionMode - "always" or "as_needed" (default: "as_needed").
   * @param params.type - Secret type: "environment" (default), "variable", or "file".
   * @param params.target - Projection target (defaults to key).
   * @param params.allowProgeny - Allow creator's progeny agents to access (user scope only).
   * @returns The set secret response containing metadata and created flag.
   *
   * @example
   * ```typescript
   * // Create a simple secret
   * const result = await client.secrets.set("MY_API_KEY", {
   *   value: "sk-...",
   *   description: "API key for external service",
   * });
   * console.log(result.created); // true if new, false if updated
   *
   * // Create a project-scoped secret
   * await client.secrets.set("DB_PASSWORD", {
   *   value: "s3cret",
   *   scope: "project",
   *   scopeId: "proj-123",
   *   injectionMode: "always",
   * });
   * ```
   */
  async set(key: string, params: SetSecretParams): Promise<SetSecretResponse> {
    return this.transport.put<SetSecretResponse>(
      `/api/v1/secrets/${encodeURIComponent(key)}`,
      params,
    );
  }

  /**
   * Deletes a secret by key.
   *
   * @param key - The secret key name.
   * @param params - Optional scope parameters.
   * @param params.scope - Scope type: "user", "project", or "runtime_broker". Defaults to "user".
   * @param params.scopeId - ID of the scoped entity (required for project/runtime_broker scope).
   * @throws {ScionError} If the secret is not found (404).
   *
   * @example
   * ```typescript
   * // Delete a user-scoped secret
   * await client.secrets.delete("MY_API_KEY");
   *
   * // Delete a project-scoped secret
   * await client.secrets.delete("DB_PASSWORD", {
   *   scope: "project",
   *   scopeId: "proj-123",
   * });
   * ```
   */
  async delete(key: string, params?: SecretScopeParams): Promise<void> {
    const query = this.buildQuery({
      scope: params?.scope,
      scopeId: params?.scopeId,
    });

    await this.transport.delete<void>(
      `/api/v1/secrets/${encodeURIComponent(key)}`,
      query,
    );
  }
}
