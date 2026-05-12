/**
 * SSE (Server-Sent Events) streaming support for the Scion SDK.
 *
 * Provides a low-level SSE parser built on `fetch()` + `ReadableStream`,
 * and a high-level {@link ScionStream} wrapper that exposes parsed events
 * as `AsyncIterable<T>` or via callbacks.
 *
 * @packageDocumentation
 */

import { StreamError, ConnectionError } from './errors.js';
import type { Transport } from './transport.js';
import type {
  StreamOptions,
  StreamCallbackOptions,
  StreamEvent,
} from './types/streaming.js';

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

/** Default SSE connection timeout (ms) — only for the initial handshake. */
const DEFAULT_SSE_TIMEOUT_MS = 0; // no timeout for SSE by default

/** Default reconnection settings. */
const DEFAULT_RECONNECT = true;
const DEFAULT_MAX_RECONNECT_ATTEMPTS = 5;
const DEFAULT_INITIAL_RECONNECT_DELAY = 1_000;
const DEFAULT_MAX_RECONNECT_DELAY = 30_000;

// ---------------------------------------------------------------------------
// SSE line parser — TransformStream
// ---------------------------------------------------------------------------

/**
 * Raw SSE field parsed from a single line.
 * @internal
 */
interface SSEField {
  name: string;
  value: string;
}

/**
 * Accumulated SSE event built from one or more data lines.
 * @internal
 */
interface RawSSEEvent {
  event: string;
  data: string;
  id: string;
  retry?: number;
}

/**
 * Creates a `TransformStream` that converts a stream of text lines into
 * parsed {@link StreamEvent} objects per the SSE specification.
 *
 * The SSE spec: https://html.spec.whatwg.org/multipage/server-sent-events.html
 *
 * - Lines starting with `:` are comments (heartbeats) and are skipped.
 * - Empty lines dispatch the accumulated event.
 * - `event:`, `data:`, `id:`, and `retry:` fields are recognised.
 *
 * @returns A TransformStream that takes string lines and emits StreamEvents.
 * @internal
 */
export function createSSEParser(): TransformStream<string, StreamEvent> {
  let eventType = '';
  let dataBuffer = '';
  let lastId = '';

  return new TransformStream<string, StreamEvent>({
    transform(line: string, controller: TransformStreamDefaultController<StreamEvent>) {
      // Empty line → dispatch event if we have data
      if (line === '') {
        if (dataBuffer) {
          // Remove trailing newline from data buffer
          const data = dataBuffer.endsWith('\n')
            ? dataBuffer.slice(0, -1)
            : dataBuffer;

          controller.enqueue({
            event: eventType || 'message',
            id: lastId || undefined,
            raw: data,
          });
        }
        // Reset for next event
        eventType = '';
        dataBuffer = '';
        return;
      }

      // Comment line (includes heartbeats like `:heartbeat 1234`)
      if (line.startsWith(':')) {
        return;
      }

      // Parse field
      const field = parseSSEField(line);
      if (!field) return;

      switch (field.name) {
        case 'event':
          eventType = field.value;
          break;
        case 'data':
          dataBuffer += field.value + '\n';
          break;
        case 'id':
          // Per spec, ignore ids with null bytes
          if (!field.value.includes('\0')) {
            lastId = field.value;
          }
          break;
        case 'retry': {
          // Retry is informational only — we handle reconnect ourselves
          break;
        }
        // Unknown fields are ignored per spec
      }
    },

    flush(controller: TransformStreamDefaultController<StreamEvent>) {
      // If there's a partial event at stream end, dispatch it
      if (dataBuffer) {
        const data = dataBuffer.endsWith('\n')
          ? dataBuffer.slice(0, -1)
          : dataBuffer;

        controller.enqueue({
          event: eventType || 'message',
          id: lastId || undefined,
          raw: data,
        });
      }
    },
  });
}

/**
 * Parse a single SSE line into field name + value.
 *
 * Per the spec:
 * - If the line contains `:`, split on the first `:`.
 * - If the value starts with a space after the colon, strip it.
 * - If no colon, the entire line is the field name with an empty value.
 *
 * @internal
 */
function parseSSEField(line: string): SSEField | null {
  const colonIdx = line.indexOf(':');

  if (colonIdx === 0) {
    // Comment — already handled above, but be safe
    return null;
  }

  if (colonIdx === -1) {
    // Entire line is the field name
    return { name: line, value: '' };
  }

  const name = line.slice(0, colonIdx);
  let value = line.slice(colonIdx + 1);
  // Strip leading space after colon (per spec)
  if (value.startsWith(' ')) {
    value = value.slice(1);
  }

  return { name, value };
}

// ---------------------------------------------------------------------------
// Line splitter — TransformStream
// ---------------------------------------------------------------------------

/**
 * Creates a `TransformStream` that splits incoming text chunks into
 * individual lines (splitting on `\n`, `\r\n`, or `\r`).
 *
 * @returns A TransformStream that takes text chunks and emits lines.
 * @internal
 */
export function createLineSplitter(): TransformStream<string, string> {
  let buffer = '';

  return new TransformStream<string, string>({
    transform(chunk: string, controller: TransformStreamDefaultController<string>) {
      buffer += chunk;
      const lines = buffer.split(/\r\n|\r|\n/);
      // Keep the last (possibly incomplete) segment in the buffer
      buffer = lines.pop() ?? '';
      for (const line of lines) {
        controller.enqueue(line);
      }
    },

    flush(controller: TransformStreamDefaultController<string>) {
      // Emit remaining buffer content as a final line
      if (buffer) {
        controller.enqueue(buffer);
      }
    },
  });
}

// ---------------------------------------------------------------------------
// ScionStream — high-level SSE wrapper
// ---------------------------------------------------------------------------

/**
 * A managed SSE stream that supports async iteration and callback consumption.
 *
 * Wraps the low-level SSE parser with reconnection logic, abort signal
 * support, and typed event parsing.
 *
 * @typeParam T - The type of parsed events yielded to the consumer.
 *
 * @example
 * ```ts
 * // AsyncIterable usage
 * const stream = new ScionStream(transport, '/events', parseEvent);
 * for await (const event of stream) {
 *   console.log(event);
 * }
 *
 * // Callback usage
 * stream.subscribe({
 *   onEvent: (event) => console.log(event),
 *   onError: (err) => console.error(err),
 *   signal: controller.signal,
 * });
 * ```
 */
export class ScionStream<T> implements AsyncIterable<T> {
  private readonly transport: Transport;
  private readonly path: string;
  private readonly parse: (event: StreamEvent) => T | null;
  private readonly options: Required<
    Pick<
      StreamOptions,
      'reconnect' | 'maxReconnectAttempts' | 'initialReconnectDelay' | 'maxReconnectDelay'
    >
  > &
    StreamOptions;

  /**
   * Create a new SSE stream.
   *
   * @param transport - The HTTP transport for making requests.
   * @param path - API path for the SSE endpoint (e.g. `/events`).
   * @param parse - Function to parse raw SSE events into typed objects.
   *                Return `null` to skip/filter events.
   * @param options - Stream configuration options.
   */
  constructor(
    transport: Transport,
    path: string,
    parse: (event: StreamEvent) => T | null,
    options?: StreamOptions,
  ) {
    this.transport = transport;
    this.path = path;
    this.parse = parse;
    this.options = {
      reconnect: options?.reconnect ?? DEFAULT_RECONNECT,
      maxReconnectAttempts: options?.maxReconnectAttempts ?? DEFAULT_MAX_RECONNECT_ATTEMPTS,
      initialReconnectDelay: options?.initialReconnectDelay ?? DEFAULT_INITIAL_RECONNECT_DELAY,
      maxReconnectDelay: options?.maxReconnectDelay ?? DEFAULT_MAX_RECONNECT_DELAY,
      ...options,
    };
  }

  /**
   * Open the SSE connection and yield parsed events.
   *
   * Implements `AsyncIterable<T>` so the stream can be consumed with
   * `for await...of`. Automatically reconnects on transient errors
   * unless reconnection is disabled or the abort signal fires.
   */
  async *[Symbol.asyncIterator](): AsyncGenerator<T, void, undefined> {
    let attempt = 0;
    let lastEventId: string | undefined;

    while (true) {
      try {
        const response = await this.connect(lastEventId);
        attempt = 0; // Reset attempt counter on successful connection

        const body = response.body;
        if (!body) {
          throw new StreamError('Response body is null — streaming not supported');
        }

        // Build the processing pipeline:
        // ReadableStream<Uint8Array> → TextDecoderStream → LineSplitter → SSEParser
        const textStream = body.pipeThrough(new TextDecoderStream());
        const lineStream = textStream.pipeThrough(createLineSplitter());
        const eventStream = lineStream.pipeThrough(createSSEParser());

        const reader = eventStream.getReader();
        try {
          while (true) {
            const { done, value } = await reader.read();
            if (done) break;

            // Track last event ID for reconnection
            if (value.id) {
              lastEventId = value.id;
            }

            // Parse and yield — skip nulls (filtered events)
            const parsed = this.parse(value);
            if (parsed !== null) {
              yield parsed;
            }
          }
        } finally {
          reader.releaseLock();
        }

        // Stream ended cleanly (server closed) — reconnect if enabled
        if (!this.shouldReconnect(attempt)) {
          return;
        }
      } catch (error) {
        // AbortError means deliberate cancellation — don't reconnect
        if (this.isAborted(error)) {
          return;
        }

        if (!this.shouldReconnect(attempt)) {
          if (error instanceof StreamError || error instanceof ConnectionError) {
            throw error;
          }
          throw new StreamError(
            `Stream failed after ${attempt + 1} attempts: ${(error as Error).message}`,
            error instanceof Error ? error : undefined,
          );
        }
      }

      // Exponential backoff before reconnect
      const delay = this.backoffDelay(attempt);
      await this.sleep(delay);
      attempt++;
    }
  }

  /**
   * Consume the stream using callbacks instead of async iteration.
   *
   * This is a convenience wrapper around the async iterator that calls
   * the provided callbacks for each event, error, and stream close.
   *
   * @param callbacks - Event, error, and close callbacks plus stream options.
   * @returns A promise that resolves when the stream closes.
   */
  async subscribe(callbacks: StreamCallbackOptions<T>): Promise<void> {
    // Create a new stream instance with merged options so that
    // per-subscription signal / query overrides are respected.
    const stream = new ScionStream<T>(this.transport, this.path, this.parse, {
      ...this.options,
      ...callbacks,
    });

    try {
      for await (const event of stream) {
        callbacks.onEvent(event);
      }
    } catch (error) {
      if (callbacks.onError && error instanceof Error) {
        callbacks.onError(error);
      } else {
        throw error;
      }
    } finally {
      callbacks.onClose?.();
    }
  }

  // ---------------------------------------------------------------------------
  // Internal helpers
  // ---------------------------------------------------------------------------

  /**
   * Open an SSE HTTP connection using the transport.
   * @internal
   */
  private async connect(lastEventId?: string): Promise<Response> {
    const headers: Record<string, string> = {
      Accept: 'text/event-stream',
      'Cache-Control': 'no-cache',
    };

    if (lastEventId) {
      headers['Last-Event-ID'] = lastEventId;
    }

    const timeout = this.options.timeout ?? DEFAULT_SSE_TIMEOUT_MS;

    return this.transport.requestRaw('GET', this.path, {
      headers,
      query: this.options.query,
      signal: this.options.signal,
      // Use 0 timeout for SSE (long-lived) unless caller specified one
      timeout: timeout || 120_000, // 2min handshake timeout as fallback
    });
  }

  /** Whether we should attempt reconnection. */
  private shouldReconnect(attempt: number): boolean {
    if (!this.options.reconnect) return false;
    if (this.options.signal?.aborted) return false;
    return attempt < this.options.maxReconnectAttempts;
  }

  /** Check if an error is an abort error. */
  private isAborted(error: unknown): boolean {
    if (this.options.signal?.aborted) return true;
    if (error instanceof DOMException && error.name === 'AbortError') return true;
    if (error instanceof Error && error.name === 'AbortError') return true;
    return false;
  }

  /** Calculate exponential backoff delay with jitter. */
  private backoffDelay(attempt: number): number {
    const base = this.options.initialReconnectDelay;
    const max = this.options.maxReconnectDelay;
    const delay = Math.min(base * Math.pow(2, attempt), max);
    // Add 0-25% jitter
    const jitter = delay * 0.25 * Math.random();
    return delay + jitter;
  }

  /** Sleep for the specified duration, respecting the abort signal. */
  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      if (this.options.signal?.aborted) {
        resolve();
        return;
      }

      const timer = setTimeout(resolve, ms);

      // If we have an abort signal, resolve early on abort
      this.options.signal?.addEventListener(
        'abort',
        () => {
          clearTimeout(timer);
          resolve();
        },
        { once: true },
      );
    });
  }
}
