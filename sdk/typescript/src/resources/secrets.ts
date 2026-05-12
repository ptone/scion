/**
 * Resource class for managing secrets via the Scion Hub API.
 *
 * @packageDocumentation
 */

import { BaseResource } from './base.js';
import type {
  Secret,
  SetSecretRequest,
  SetSecretResponse,
  SecretScopeOptions,
  ListSecretsOptions,
  ListSecretResponse,
} from '../types/secrets.js';

/** Result from listing secrets, including scope metadata. */
export interface SecretsPage {
  /** Secret metadata entries (no values). */
  data: Secret[];
  /** The scope these secrets belong to. */
  scope: string;
  /** The scope entity ID. */
  scopeId: string;
}

/**
 * Provides operations on Scion secrets.
 *
 * Secrets are scoped to a user, project, or runtime broker. Secret values
 * are write-only and are never returned by the API — only metadata is
 * accessible via list and get operations.
 *
 * @example
 * ```ts
 * const client = new ScionClient({ hubUrl: 'https://hub.example.com' });
 *
 * // List user-scoped secrets
 * const page = await client.secrets.list();
 *
 * // Set a secret
 * const result = await client.secrets.set('MY_API_KEY', {
 *   value: 'sk-...',
 *   description: 'API key for external service',
 * });
 * ```
 */
export class SecretsResource extends BaseResource {
  private static readonly BASE_PATH = '/api/v1/secrets';

  /**
   * List secret metadata for the specified scope.
   *
   * Returns metadata only — secret values are never included.
   *
   * @param params - Optional filtering parameters.
   * @returns A page of secret metadata.
   */
  async list(params?: ListSecretsOptions): Promise<SecretsPage> {
    const query = this.buildQuery({
      scope: params?.scope,
      scopeId: params?.scopeId,
      type: params?.type,
    });

    const response = await this.transport.request<ListSecretResponse>(
      'GET',
      SecretsResource.BASE_PATH,
      query ? { query } : undefined,
    );

    return {
      data: response.secrets ?? [],
      scope: response.scope,
      scopeId: response.scopeId,
    };
  }

  /**
   * Get metadata for a specific secret by key.
   *
   * The secret value is never returned.
   *
   * @param key - The secret key name.
   * @param params - Optional scope parameters.
   * @returns The secret metadata.
   * @throws {NotFoundError} If the secret is not found.
   */
  async get(key: string, params?: SecretScopeOptions): Promise<Secret> {
    const query = this.buildQuery({
      scope: params?.scope,
      scopeId: params?.scopeId,
    });

    return this.transport.request<Secret>(
      'GET',
      `${SecretsResource.BASE_PATH}/${encodeURIComponent(key)}`,
      query ? { query } : undefined,
    );
  }

  /**
   * Create or update a secret.
   *
   * @param key - The secret key name.
   * @param params - The secret parameters including value and optional metadata.
   * @returns The set secret response containing metadata and created flag.
   */
  async set(key: string, params: SetSecretRequest): Promise<SetSecretResponse> {
    return this.transport.request<SetSecretResponse>(
      'PUT',
      `${SecretsResource.BASE_PATH}/${encodeURIComponent(key)}`,
      { body: params },
    );
  }

  /**
   * Delete a secret by key.
   *
   * @param key - The secret key name.
   * @param params - Optional scope parameters.
   * @throws {NotFoundError} If the secret is not found.
   */
  async delete(key: string, params?: SecretScopeOptions): Promise<void> {
    const query = this.buildQuery({
      scope: params?.scope,
      scopeId: params?.scopeId,
    });

    await this.transport.request<void>(
      'DELETE',
      `${SecretsResource.BASE_PATH}/${encodeURIComponent(key)}`,
      query ? { query } : undefined,
    );
  }
}
