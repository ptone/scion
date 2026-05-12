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

/**
 * A persisted structured message between agents and humans.
 *
 * Mirrors the Go `store.Message` type.
 */
export interface Message {
  /** Unique message identifier. */
  id: string;
  /** Project that the message belongs to. */
  projectId: string;
  /** Sender identity (e.g. "user:alice", "agent:code-reviewer"). */
  sender: string;
  /** UUID or identity key of the sender. */
  senderId: string;
  /** Recipient identity (e.g. "user:alice", "agent:code-reviewer"). */
  recipient: string;
  /** UUID or identity key of the recipient. */
  recipientId: string;
  /** Message body text. */
  msg: string;
  /** Message type (e.g. "instruction", "input-needed", "state-change", "assistant-reply"). */
  type: string;
  /** Whether the message is urgent. */
  urgent?: boolean;
  /** Whether the message was broadcasted. */
  broadcasted?: boolean;
  /** Whether the recipient has read/acknowledged the message. */
  read: boolean;
  /** The agent involved (sender or recipient). */
  agentId: string;
  /** ISO-8601 timestamp when the message was created. */
  createdAt: string;
}

/**
 * Options for listing messages.
 *
 * Mirrors the Go `hubclient.ListMessagesOptions` struct.
 */
export interface ListMessagesOptions {
  /** When true, only return unread messages. */
  onlyUnread?: boolean;
  /** Filter by agent ID. */
  agentId?: string;
  /** Filter by project ID. */
  projectId?: string;
  /** Filter by message type. */
  type?: string;
  /** Maximum number of results to return. */
  limit?: number;
  /** Pagination cursor from a previous response. */
  cursor?: string;
}
