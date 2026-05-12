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

import { Transport, type TransportOptions } from './transport';
import { ProjectsResource } from './resources/projects';

/** Configuration options for the ScionClient. */
export interface ScionClientOptions {
  /** Base URL of the Scion Hub API (e.g. "https://hub.scion.dev"). */
  baseUrl: string;
  /** Bearer token for authentication. */
  token?: string;
  /** Custom fetch implementation (defaults to global fetch). */
  fetch?: typeof fetch;
  /** Default request timeout in milliseconds (default: 30000). */
  timeout?: number;
  /** Custom headers to include on every request. */
  headers?: Record<string, string>;
}

/**
 * Client for the Scion Hub API.
 *
 * Provides typed access to all Scion API resources through namespaced
 * properties (e.g. `client.projects`).
 *
 * @example
 * ```typescript
 * import { ScionClient } from '@scion/sdk';
 *
 * const client = new ScionClient({
 *   baseUrl: 'https://hub.scion.dev',
 *   token: 'your-api-token',
 * });
 *
 * const projects = await client.projects.list();
 * ```
 */
export class ScionClient {
  private readonly transport: Transport;

  /** Resource for managing projects. */
  readonly projects: ProjectsResource;

  constructor(options: ScionClientOptions) {
    const transportOpts: TransportOptions = {
      baseUrl: options.baseUrl,
      token: options.token,
      fetch: options.fetch,
      timeout: options.timeout,
      headers: options.headers,
    };

    this.transport = new Transport(transportOpts);
    this.projects = new ProjectsResource(this.transport);
  }
}
