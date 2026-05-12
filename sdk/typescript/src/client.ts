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

import { Transport, type TransportOptions } from './transport.js';
import { MessagesResource } from './resources/messages.js';

/** Options for constructing a {@link ScionClient}. */
export interface ScionClientOptions {
  /** Base URL of the Scion Hub API (e.g. "https://hub.scion.dev"). */
  baseUrl: string;
  /** Bearer token for authentication. */
  token?: string;
  /** Custom headers applied to every request. */
  headers?: Record<string, string>;
}

/**
 * Client for the Scion Hub API.
 *
 * Provides access to Hub resources such as messages. Additional resources
 * (agents, projects, etc.) will be added in future releases.
 *
 * @example
 * ```ts
 * import { ScionClient } from '@scion/typescript-sdk';
 *
 * const client = new ScionClient({
 *   baseUrl: 'https://hub.scion.dev',
 *   token: 'your-bearer-token',
 * });
 *
 * const page = await client.messages.list({ onlyUnread: true });
 * console.log(`You have ${page.items.length} unread messages`);
 * ```
 */
export class ScionClient {
  /** Message inbox operations. */
  readonly messages: MessagesResource;

  /** @internal */
  private readonly transport: Transport;

  constructor(options: ScionClientOptions) {
    const transportOpts: TransportOptions = {
      baseUrl: options.baseUrl,
      token: options.token,
      headers: options.headers,
    };
    this.transport = new Transport(transportOpts);
    this.messages = new MessagesResource(this.transport);
  }
}
