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

import { AgentsResource } from "./resources/agents.js";
import { Transport, type AuthProvider } from "./transport.js";

/** Options for constructing a {@link ScionClient}. */
export interface ScionClientOptions {
  /** Base URL of the Scion Hub API (e.g. "https://hub.example.com"). */
  baseUrl: string;
  /** Bearer token for authentication. */
  token?: string;
  /** Custom auth provider (takes precedence over token). */
  auth?: AuthProvider;
  /** Custom fetch implementation. */
  fetch?: typeof globalThis.fetch;
  /** Request timeout in milliseconds (default: 30000). */
  timeoutMs?: number;
  /** Scope all operations to this project ID. */
  projectId?: string;
}

/**
 * Client for the Scion Hub API.
 *
 * @example
 * ```ts
 * const client = new ScionClient({
 *   baseUrl: "https://hub.example.com",
 *   token: "my-api-token",
 * });
 *
 * // List running agents
 * for await (const agent of client.agents.list({ phase: "running" })) {
 *   console.log(agent.name);
 * }
 * ```
 */
export class ScionClient {
  /** Agent operations. */
  readonly agents: AgentsResource;

  private readonly transport: Transport;

  constructor(opts: ScionClientOptions) {
    let auth: AuthProvider | undefined = opts.auth;
    if (!auth && opts.token) {
      const token = opts.token;
      auth = () => ({ Authorization: `Bearer ${token}` });
    }

    this.transport = new Transport({
      baseUrl: opts.baseUrl,
      auth,
      fetch: opts.fetch,
      timeoutMs: opts.timeoutMs,
    });

    this.agents = new AgentsResource(this.transport, opts.projectId);
  }
}
