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

import {
  Transport,
  BearerAuth,
  AgentTokenAuth,
  Authenticator,
} from "./transport";
import { SecretsResource } from "./resources/secrets";

/**
 * Options for configuring the ScionClient.
 */
export interface ScionClientOptions {
  /** Bearer token for user authentication. */
  token?: string;

  /** Agent token for agent authentication. */
  agentToken?: string;

  /** Custom authenticator instance. */
  auth?: Authenticator;

  /** Custom fetch implementation (defaults to globalThis.fetch). */
  fetch?: typeof fetch;
}

/**
 * ScionClient is the main entry point for the Scion Hub API.
 *
 * @example
 * ```typescript
 * const client = new ScionClient("https://hub.example.com", {
 *   token: "your-bearer-token",
 * });
 *
 * // Access secrets
 * const secrets = await client.secrets.list();
 * ```
 */
export class ScionClient {
  private readonly transport: Transport;

  /** Secrets resource for managing secrets. */
  public readonly secrets: SecretsResource;

  /**
   * Creates a new ScionClient.
   *
   * @param baseURL - The base URL of the Scion Hub API.
   * @param options - Client configuration options.
   */
  constructor(baseURL: string, options?: ScionClientOptions) {
    this.transport = new Transport(baseURL, options?.fetch);

    // Configure authentication
    if (options?.auth) {
      this.transport.auth = options.auth;
    } else if (options?.token) {
      this.transport.auth = new BearerAuth(options.token);
    } else if (options?.agentToken) {
      this.transport.auth = new AgentTokenAuth(options.agentToken);
    }

    // Initialize resources
    this.secrets = new SecretsResource(this.transport);
  }
}
