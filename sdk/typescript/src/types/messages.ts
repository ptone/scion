/**
 * Type definitions for the Message resource.
 *
 * @packageDocumentation
 */

import type { PageParams, PaginatedResponse } from './common.js';

/** A message in the user's inbox. */
export interface Message {
  /** Message UUID. */
  id: string;
  /** Project ID this message relates to. */
  projectId: string;
  /** Display name of the sender. */
  sender: string;
  /** Sender user/agent ID. */
  senderId: string;
  /** Display name of the recipient. */
  recipient: string;
  /** Recipient user/agent ID. */
  recipientId: string;
  /** Message body text. */
  msg: string;
  /** Message type (e.g. "instruction", "input-needed", "state-change", "assistant-reply"). */
  type: string;
  /** Whether this message is urgent. */
  urgent?: boolean;
  /** Whether this message was broadcasted. */
  broadcasted?: boolean;
  /** Whether this message has been read. */
  read: boolean;
  /** Agent ID if this message is from/to an agent. */
  agentId?: string;
  /** Creation timestamp (ISO 8601). */
  createdAt: string;
}

/** Options for listing messages. */
export interface ListMessagesOptions extends PageParams {
  /** Only return unread messages. */
  onlyUnread?: boolean;
  /** Filter by agent ID. */
  agentId?: string;
  /** Filter by project ID. */
  projectId?: string;
  /** Filter by message type. */
  type?: string;
}

/** Response from listing messages. */
export type ListMessagesResponse = PaginatedResponse<Message>;
