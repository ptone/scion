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

import type { Transport } from '../transport.js';
import type { Page } from '../types/common.js';
import type { ListMessagesOptions, Message } from '../types/messages.js';

/**
 * Provides operations on the authenticated user's message inbox.
 *
 * Mirrors the Go `hubclient.MessageService` interface.
 *
 * @example
 * ```ts
 * const client = new ScionClient({ baseUrl: 'https://hub.scion.dev', token: '...' });
 *
 * // List unread messages
 * const page = await client.messages.list({ onlyUnread: true });
 * for (const msg of page.items) {
 *   console.log(`${msg.sender}: ${msg.msg}`);
 * }
 *
 * // Mark a single message as read
 * await client.messages.markRead(page.items[0].id);
 *
 * // Mark all messages as read
 * await client.messages.markAllRead();
 * ```
 */
export class MessagesResource {
  /** @internal */
  private readonly transport: Transport;

  /** @internal */
  constructor(transport: Transport) {
    this.transport = transport;
  }

  /**
   * List messages for the authenticated user.
   *
   * Results are returned as a paginated {@link Page} of {@link Message} objects.
   * Use the `nextCursor` field from the response to fetch subsequent pages.
   *
   * @param params - Optional filtering and pagination parameters.
   * @returns A page of messages.
   *
   * @example
   * ```ts
   * // List all messages
   * const all = await client.messages.list();
   *
   * // List only unread messages for a specific agent
   * const unread = await client.messages.list({
   *   onlyUnread: true,
   *   agentId: 'code-reviewer',
   * });
   * ```
   */
  async list(params?: ListMessagesOptions): Promise<Page<Message>> {
    const query: Record<string, string> = {};

    if (params) {
      if (params.onlyUnread) {
        query.unread = 'true';
      }
      if (params.agentId) {
        query.agent = params.agentId;
      }
      if (params.projectId) {
        query.project = params.projectId;
      }
      if (params.type) {
        query.type = params.type;
      }
      if (params.limit !== undefined && params.limit > 0) {
        query.limit = String(params.limit);
      }
      if (params.cursor) {
        query.cursor = params.cursor;
      }
    }

    const result = await this.transport.get<Page<Message>>(
      '/api/v1/messages',
      query,
    );

    // Ensure items is always an array, matching Go implementation behavior.
    if (!result.items) {
      result.items = [];
    }

    return result;
  }

  /**
   * Get a single message by ID.
   *
   * @param messageId - The unique message identifier.
   * @returns The requested message.
   * @throws {ScionAPIError} If the message is not found (404).
   */
  async get(messageId: string): Promise<Message> {
    return this.transport.get<Message>(
      `/api/v1/messages/${encodeURIComponent(messageId)}`,
    );
  }

  /**
   * Mark a single message as read.
   *
   * @param messageId - The unique message identifier.
   * @throws {ScionAPIError} If the message is not found (404).
   */
  async markRead(messageId: string): Promise<void> {
    await this.transport.postEmpty(
      `/api/v1/messages/${encodeURIComponent(messageId)}/read`,
    );
  }

  /**
   * Mark all messages as read for the authenticated user.
   */
  async markAllRead(): Promise<void> {
    await this.transport.postEmpty('/api/v1/messages/read-all');
  }
}
