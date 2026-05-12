/**
 * Common types shared across the Scion SDK.
 *
 * @packageDocumentation
 */

/** Response from the `/healthz` endpoint. */
export interface HealthResponse {
  /** Overall health status (e.g. "ok"). */
  status: string;
  /** Server version string. */
  version: string;
  /** Scion platform version. */
  scionVersion: string;
  /** Server uptime as a human-readable duration. */
  uptime: string;
  /** Per-component health checks. */
  checks?: Record<string, string>;
  /** Composite web health (present in combo mode). */
  web?: unknown;
  /** Composite hub health (present in combo mode). */
  hub?: unknown;
  /** Composite broker health (present in combo mode). */
  broker?: unknown;
}

/** Cursor-based pagination parameters for list requests. */
export interface PageParams {
  /** Maximum number of results per page. */
  limit?: number;
  /** Opaque cursor from a previous response to fetch the next page. */
  cursor?: string;
}

/** Generic paginated response envelope. */
export interface PaginatedResponse<T> {
  /** The page of results. */
  items: T[];
  /** Opaque cursor for the next page. Empty/absent when no more pages. */
  nextCursor?: string;
  /** Total count of matching items (if the server provides it). */
  totalCount?: number;
}

/** Structured message for inter-agent and user-to-agent communication. */
export interface StructuredMessage {
  /** Schema version (currently 1). */
  version: number;
  /** ISO 8601 timestamp. */
  timestamp: string;
  /** Sender identity (e.g. "user:alice", "agent:code-reviewer"). */
  sender: string;
  /** Sender UUID. */
  senderId?: string;
  /** Recipient identity. */
  recipient: string;
  /** Recipient UUID. */
  recipientId?: string;
  /** Message content. */
  msg: string;
  /** Message type. */
  type: 'instruction' | 'input-needed' | 'state-change' | 'assistant-reply';
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
