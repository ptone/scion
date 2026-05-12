/**
 * Type definitions for SSE streaming.
 *
 * @packageDocumentation
 */

// ---------------------------------------------------------------------------
// Base SSE event
// ---------------------------------------------------------------------------

/** Base shape for all SSE-delivered events. */
export interface StreamEvent {
  /** SSE event type (e.g. "update", "message"). */
  event: string;
  /** SSE event ID, if the server provides one. */
  id?: string;
  /** Raw `data:` payload before JSON parsing. */
  raw: string;
}

// ---------------------------------------------------------------------------
// Agent events (GET /events?sub=project.*.agent.*)
// ---------------------------------------------------------------------------

/** Detail payload embedded in agent status SSE events. */
export interface AgentDetail {
  /** Name of the tool currently being executed. */
  toolName?: string;
  /** Human-readable status message. */
  message?: string;
  /** Short summary of the agent's current task. */
  taskSummary?: string;
  /** Current number of harness turns. */
  currentTurns?: number;
  /** Current number of model API calls. */
  currentModelCalls?: number;
  /** ISO 8601 timestamp when the agent session started. */
  startedAt?: string;
}

/** An agent lifecycle / status event received via SSE. */
export interface AgentEvent {
  /** SSE subject (e.g. "project.xxx.agent.status"). */
  subject: string;
  /** The event category derived from the subject suffix. */
  type: 'created' | 'status' | 'deleted' | string;
  /** Agent data snapshot (present for created/status events). */
  data: {
    /** Agent unique identifier. */
    id?: string;
    /** Agent slug. */
    slug?: string;
    /** Agent name. */
    name?: string;
    /** Lifecycle phase. */
    phase?: string;
    /** Runtime activity. */
    activity?: string;
    /** Legacy status field. */
    status?: string;
    /** Project ID. */
    projectId?: string;
    /** Freeform detail blob. */
    detail?: AgentDetail;
    /** Arbitrary additional fields from the server. */
    [key: string]: unknown;
  };
}

// ---------------------------------------------------------------------------
// Cloud log entries (GET /agents/:id/cloud-logs/stream)
// ---------------------------------------------------------------------------

/** Source code location attached to a log entry. */
export interface SourceLocation {
  /** Source file path. */
  file?: string;
  /** Line number (string per Cloud Logging convention). */
  line?: string;
  /** Function name. */
  function?: string;
}

/** A single structured log entry from Cloud Logging, delivered via SSE. */
export interface LogEntry {
  /** ISO 8601 timestamp. */
  timestamp: string;
  /** Log severity (e.g. "INFO", "ERROR"). */
  severity: string;
  /** Human-readable log message. */
  message: string;
  /** Key-value labels from Cloud Logging. */
  labels?: Record<string, string>;
  /** Resource metadata. */
  resource?: Record<string, unknown>;
  /** Structured JSON payload. */
  jsonPayload?: Record<string, unknown>;
  /** Unique identifier for deduplication. */
  insertId: string;
  /** Source code location. */
  sourceLocation?: SourceLocation;
}

// ---------------------------------------------------------------------------
// Stream configuration
// ---------------------------------------------------------------------------

/** Options for opening an SSE stream. */
export interface StreamOptions {
  /**
   * AbortSignal for cancelling the stream.
   * When aborted, the stream closes gracefully.
   */
  signal?: AbortSignal;

  /**
   * Whether to automatically reconnect on transient errors.
   * @defaultValue true
   */
  reconnect?: boolean;

  /**
   * Maximum number of reconnection attempts before giving up.
   * @defaultValue 5
   */
  maxReconnectAttempts?: number;

  /**
   * Initial backoff delay in milliseconds for reconnection.
   * Doubles on each subsequent attempt (exponential backoff).
   * @defaultValue 1000
   */
  initialReconnectDelay?: number;

  /**
   * Maximum backoff delay in milliseconds.
   * @defaultValue 30000
   */
  maxReconnectDelay?: number;

  /**
   * Request timeout in milliseconds for the initial SSE connection.
   * SSE connections are long-lived, so this only applies to the
   * initial HTTP handshake. Set to 0 to disable.
   */
  timeout?: number;

  /** Extra query parameters merged into the request URL. */
  query?: Record<string, string>;
}

/** Callback-style options for consuming a stream. */
export interface StreamCallbackOptions<T> extends StreamOptions {
  /** Called for each parsed event. */
  onEvent: (event: T) => void;
  /** Called when the stream encounters an error. */
  onError?: (error: Error) => void;
  /** Called when the stream closes (after final retry exhaustion or abort). */
  onClose?: () => void;
}
