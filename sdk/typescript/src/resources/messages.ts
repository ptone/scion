/**
 * Resource class for managing messages via the Scion Hub API.
 *
 * @packageDocumentation
 */

import { BaseResource } from './base.js';
import { Page } from '../pagination.js';
import type { Message, ListMessagesOptions } from '../types/messages.js';

/** Raw list response from the API. */
interface ListMessagesApiResponse {
  items: Message[];
  nextCursor?: string;
  totalCount?: number;
}

/**
 * Provides operations on the authenticated user's message inbox.
 *
 * @example
 * ```ts
 * const client = new ScionClient({ hubUrl: 'https://hub.example.com' });
 *
 * // List unread messages
 * const page = await client.messages.list({ onlyUnread: true });
 * for await (const msg of page) {
 *   console.log(`${msg.sender}: ${msg.msg}`);
 * }
 *
 * // Mark a single message as read
 * await client.messages.markRead(page.data[0].id);
 * ```
 */
export class MessagesResource extends BaseResource {
  private static readonly BASE_PATH = '/api/v1/messages';

  /**
   * List messages for the authenticated user.
   *
   * @param params - Optional filtering and pagination parameters.
   * @returns A page of messages.
   */
  async list(params?: ListMessagesOptions): Promise<Page<Message>> {
    return this.fetchPage(params);
  }

  /**
   * Get a single message by ID.
   *
   * @param messageId - The unique message identifier.
   * @returns The requested message.
   * @throws {NotFoundError} If the message is not found.
   */
  async get(messageId: string): Promise<Message> {
    return this.transport.request<Message>(
      'GET',
      `${MessagesResource.BASE_PATH}/${encodeURIComponent(messageId)}`,
    );
  }

  /**
   * Mark a single message as read.
   *
   * @param messageId - The unique message identifier.
   */
  async markRead(messageId: string): Promise<void> {
    await this.transport.request<void>(
      'POST',
      `${MessagesResource.BASE_PATH}/${encodeURIComponent(messageId)}/read`,
    );
  }

  /**
   * Mark all messages as read for the authenticated user.
   */
  async markAllRead(): Promise<void> {
    await this.transport.request<void>('POST', `${MessagesResource.BASE_PATH}/read-all`);
  }

  /** Internal: fetch a single page of messages. */
  private async fetchPage(params?: ListMessagesOptions): Promise<Page<Message>> {
    const query: Record<string, string> = {};

    if (params) {
      if (params.onlyUnread) query.unread = 'true';
      if (params.agentId) query.agent = params.agentId;
      if (params.projectId) query.project = params.projectId;
      if (params.type) query.type = params.type;
      if (params.limit !== undefined && params.limit > 0) query.limit = String(params.limit);
      if (params.cursor) query.cursor = params.cursor;
    }

    const result = await this.transport.request<ListMessagesApiResponse>(
      'GET',
      MessagesResource.BASE_PATH,
      { query },
    );

    const items = result.items ?? [];

    return new Page<Message>(
      items,
      result.nextCursor,
      (cursor) => this.fetchPage({ ...params, cursor }),
      result.totalCount,
    );
  }
}
