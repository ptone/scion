/**
 * Main client entrypoint for the Scion SDK.
 *
 * Provides {@link ScionClient} — the top-level class through which all
 * Scion Hub API operations are accessed.
 *
 * @packageDocumentation
 */

import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { homedir } from 'node:os';
import { Transport } from './transport.js';
import type { HealthResponse } from './types/common.js';

/** Options for constructing a {@link ScionClient}. */
export interface ScionClientOptions {
  /**
   * Base URL of the Scion Hub (e.g. `https://hub.example.com`).
   * Falls back to `SCION_HUB_URL` env var if not provided.
   */
  hubUrl?: string;
  /**
   * Bearer token for authentication.
   * If omitted, the client resolves a token via the standard chain:
   * `SCION_API_TOKEN` env -> `SCION_DEV_TOKEN` env -> `~/.scion/dev-token` file.
   */
  token?: string;
  /** Request timeout in milliseconds. Defaults to 30 000 (30 s). */
  timeout?: number;
}

/**
 * Placeholder stub for a resource service that will be implemented later.
 * Each resource module (agents, projects, etc.) will replace its stub.
 */
class ResourceStub {
  constructor(readonly _name: string) {}
}

/**
 * The main Scion SDK client.
 *
 * Provides access to all Scion Hub API resources through lazily-initialised
 * service accessors (`client.agents`, `client.projects`, etc.).
 *
 * @example
 * ```ts
 * import { ScionClient } from '@scion/sdk';
 *
 * const client = new ScionClient({ hubUrl: 'https://hub.example.com' });
 * const health = await client.health();
 * console.log(health.status);
 * ```
 */
export class ScionClient {
  /** @internal */
  readonly transport: Transport;

  // Lazy-init backing fields
  private _agents?: ResourceStub;
  private _projects?: ResourceStub;
  private _secrets?: ResourceStub;
  private _messages?: ResourceStub;

  constructor(options: ScionClientOptions = {}) {
    const hubUrl = options.hubUrl ?? resolveHubUrl();
    const token = options.token ?? resolveToken();

    if (!hubUrl) {
      throw new Error(
        'hubUrl is required. Provide it via ScionClientOptions.hubUrl or set SCION_HUB_URL.',
      );
    }

    this.transport = new Transport({
      baseUrl: hubUrl,
      token,
      timeout: options.timeout,
    });
  }

  /**
   * Create a client configured for use inside an agent container.
   *
   * Reads `SCION_HUB_URL` and `SCION_AGENT_TOKEN` from the environment
   * and authenticates via the `X-Scion-Agent-Token` header instead of
   * the standard `Authorization: Bearer` header.
   *
   * @returns A ScionClient configured for agent-to-hub communication.
   * @throws If the required environment variables are not set.
   */
  static fromAgentEnv(): ScionClient {
    const hubUrl = process.env.SCION_HUB_URL;
    const agentToken = process.env.SCION_AGENT_TOKEN;

    if (!hubUrl) {
      throw new Error('SCION_HUB_URL environment variable is required for agent mode.');
    }
    if (!agentToken) {
      throw new Error('SCION_AGENT_TOKEN environment variable is required for agent mode.');
    }

    const client = new ScionClient({ hubUrl, token: undefined });

    // Replace the transport to use X-Scion-Agent-Token instead of Bearer
    const agentTransport = new Transport({
      baseUrl: hubUrl,
      headers: { 'X-Scion-Agent-Token': agentToken },
    });

    // Override the transport (readonly from external perspective)
    Object.defineProperty(client, 'transport', {
      value: agentTransport,
      writable: false,
      configurable: false,
    });

    return client;
  }

  /**
   * Check API availability.
   *
   * @returns Health status of the Scion Hub.
   */
  async health(): Promise<HealthResponse> {
    return this.transport.request<HealthResponse>('GET', '/healthz');
  }

  // ---------------------------------------------------------------------------
  // Resource service accessors (lazy-init stubs — replaced in Phase B)
  // ---------------------------------------------------------------------------

  /** Access agent operations. */
  get agents(): ResourceStub {
    if (!this._agents) {
      this._agents = new ResourceStub('agents');
    }
    return this._agents;
  }

  /** Access project operations. */
  get projects(): ResourceStub {
    if (!this._projects) {
      this._projects = new ResourceStub('projects');
    }
    return this._projects;
  }

  /** Access secret operations. */
  get secrets(): ResourceStub {
    if (!this._secrets) {
      this._secrets = new ResourceStub('secrets');
    }
    return this._secrets;
  }

  /** Access message operations. */
  get messages(): ResourceStub {
    if (!this._messages) {
      this._messages = new ResourceStub('messages');
    }
    return this._messages;
  }
}

// ---------------------------------------------------------------------------
// Token resolution helpers (internal)
// ---------------------------------------------------------------------------

/** Resolve the hub URL from environment. */
function resolveHubUrl(): string | undefined {
  return process.env.SCION_HUB_URL;
}

/**
 * Resolve a bearer token via the standard chain:
 * 1. SCION_API_TOKEN env var
 * 2. SCION_DEV_TOKEN env var
 * 3. ~/.scion/dev-token file
 */
function resolveToken(): string | undefined {
  // 1. Explicit API token
  const apiToken = process.env.SCION_API_TOKEN;
  if (apiToken) return apiToken;

  // 2. Dev token env var
  const devToken = process.env.SCION_DEV_TOKEN;
  if (devToken) return devToken;

  // 3. Dev token file
  try {
    const tokenPath = join(homedir(), '.scion', 'dev-token');
    const content = readFileSync(tokenPath, 'utf-8').trim();
    if (content) return content;
  } catch {
    // File doesn't exist or is unreadable — no token
  }

  return undefined;
}
