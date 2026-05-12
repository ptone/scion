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

/** Structured message for inter-agent and user-to-agent communication. */
export interface StructuredMessage {
  /** Schema version (currently 1). */
  version: number;
  /** ISO 8601 timestamp. */
  timestamp: string;
  /** Sender identity (e.g. "user:alice", "agent:code-reviewer"). */
  sender: string;
  /** Sender ID. */
  senderId?: string;
  /** Recipient identity. */
  recipient: string;
  /** Recipient ID. */
  recipientId?: string;
  /** Message content. */
  msg: string;
  /** Message type: "instruction", "input-needed", "state-change", or "assistant-reply". */
  type: "instruction" | "input-needed" | "state-change" | "assistant-reply";
  /** If true, deliver as plain text without envelope formatting. */
  plain?: boolean;
  /** If true, deliver raw without any wrapping. */
  raw?: boolean;
  /** If true, mark as urgent. */
  urgent?: boolean;
  /** Whether this message was broadcasted. */
  broadcasted?: boolean;
  /** Status hint. */
  status?: string;
  /** File attachments (paths or references). */
  attachments?: string[];
  /** Arbitrary key-value metadata. */
  metadata?: Record<string, string>;
}
