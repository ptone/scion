import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { ScionClient } from '../src/client.js';
import { ScionStream, createSSEParser, createLineSplitter } from '../src/streaming.js';
import { StreamError } from '../src/errors.js';
import type { AgentEvent, LogEntry, StreamEvent } from '../src/types/streaming.js';

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const BASE_URL = 'http://hub.test:9999';

const server = setupServer();

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  server.resetHandlers();
  vi.restoreAllMocks();
});
afterAll(() => server.close());

/**
 * Helper: convert a string of SSE lines into a ReadableStream of Uint8Array
 * chunks, simulating a streaming HTTP response body.
 */
function sseBody(lines: string): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      controller.enqueue(encoder.encode(lines));
      controller.close();
    },
  });
}

/**
 * Helper: convert a string of SSE lines into a ReadableStream that sends
 * data in multiple small chunks to test buffering.
 */
function sseBodyChunked(lines: string, chunkSize = 10): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  const data = encoder.encode(lines);
  let offset = 0;

  return new ReadableStream({
    pull(controller) {
      if (offset >= data.length) {
        controller.close();
        return;
      }
      const end = Math.min(offset + chunkSize, data.length);
      controller.enqueue(data.slice(offset, end));
      offset = end;
    },
  });
}

/**
 * Helper: create an SSE response with the correct headers.
 */
function sseResponse(body: string | ReadableStream<Uint8Array>): Response {
  const stream = typeof body === 'string' ? sseBody(body) : body;
  return new Response(stream, {
    status: 200,
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
    },
  });
}

/**
 * Helper: collect all events from an async iterable.
 */
async function collect<T>(iterable: AsyncIterable<T>, maxEvents = 100): Promise<T[]> {
  const results: T[] = [];
  for await (const item of iterable) {
    results.push(item);
    if (results.length >= maxEvents) break;
  }
  return results;
}

// ---------------------------------------------------------------------------
// Helpers for TransformStream unit tests
// ---------------------------------------------------------------------------

/**
 * Feed an array of string chunks through a TransformStream and collect results.
 * Avoids backpressure deadlocks by using a pipeline approach.
 */
async function pipeThrough<I, O>(
  transform: TransformStream<I, O>,
  chunks: I[],
): Promise<O[]> {
  const source = new ReadableStream<I>({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(chunk);
      }
      controller.close();
    },
  });

  const reader = source.pipeThrough(transform).getReader();
  const results: O[] = [];
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    results.push(value);
  }
  return results;
}

// ---------------------------------------------------------------------------
// SSE line parser tests
// ---------------------------------------------------------------------------

describe('createLineSplitter', () => {
  it('splits text into lines on \\n', async () => {
    const lines = await pipeThrough(
      createLineSplitter(),
      ['line1\nline2\nline3\n'],
    );
    // Trailing \n leaves an empty buffer — flush does not emit it
    expect(lines).toEqual(['line1', 'line2', 'line3']);
  });

  it('handles \\r\\n line endings', async () => {
    const lines = await pipeThrough(
      createLineSplitter(),
      ['line1\r\nline2\r\n'],
    );
    expect(lines).toEqual(['line1', 'line2']);
  });

  it('buffers partial lines across chunks', async () => {
    const lines = await pipeThrough(
      createLineSplitter(),
      ['par', 'tial\ncom', 'plete\n'],
    );
    expect(lines).toEqual(['partial', 'complete']);
  });
});

// ---------------------------------------------------------------------------
// SSE parser tests
// ---------------------------------------------------------------------------

describe('createSSEParser', () => {
  it('parses a simple data event', async () => {
    const events = await pipeThrough(
      createSSEParser(),
      ['data: hello world', ''],
    );
    expect(events).toHaveLength(1);
    expect(events[0]).toEqual({
      event: 'message',
      id: undefined,
      raw: 'hello world',
    });
  });

  it('parses named event types', async () => {
    const events = await pipeThrough(
      createSSEParser(),
      ['event: update', 'data: {"key":"value"}', ''],
    );
    expect(events).toHaveLength(1);
    expect(events[0]).toEqual({
      event: 'update',
      id: undefined,
      raw: '{"key":"value"}',
    });
  });

  it('includes event ID when present', async () => {
    const events = await pipeThrough(
      createSSEParser(),
      ['id: 42', 'event: update', 'data: test', ''],
    );
    expect(events).toHaveLength(1);
    expect(events[0]).toEqual({
      event: 'update',
      id: '42',
      raw: 'test',
    });
  });

  it('concatenates multiple data lines', async () => {
    const events = await pipeThrough(
      createSSEParser(),
      ['data: line1', 'data: line2', 'data: line3', ''],
    );
    expect(events).toHaveLength(1);
    expect(events[0].raw).toBe('line1\nline2\nline3');
  });

  it('skips comment lines (heartbeats)', async () => {
    const events = await pipeThrough(
      createSSEParser(),
      [':heartbeat 1715500000000', '', 'data: actual-event', ''],
    );
    expect(events).toHaveLength(1);
    expect(events[0].raw).toBe('actual-event');
  });

  it('handles data field with no space after colon', async () => {
    const events = await pipeThrough(
      createSSEParser(),
      ['data:no-space', ''],
    );
    expect(events).toHaveLength(1);
    expect(events[0].raw).toBe('no-space');
  });

  it('handles empty data value', async () => {
    const events = await pipeThrough(
      createSSEParser(),
      ['data:', ''],
    );
    expect(events).toHaveLength(1);
    expect(events[0].raw).toBe('');
  });

  it('parses multiple events in sequence', async () => {
    const events = await pipeThrough(
      createSSEParser(),
      ['data: event1', '', 'data: event2', ''],
    );
    expect(events).toHaveLength(2);
    expect(events[0].raw).toBe('event1');
    expect(events[1].raw).toBe('event2');
  });

  it('does not emit on empty lines alone (no data)', async () => {
    const events = await pipeThrough(
      createSSEParser(),
      ['', '', ':comment', '', 'data: actual', ''],
    );
    expect(events).toHaveLength(1);
    expect(events[0].raw).toBe('actual');
  });

  it('flushes partial event at stream end', async () => {
    const events = await pipeThrough(
      createSSEParser(),
      ['data: partial'],
    );
    expect(events).toHaveLength(1);
    expect(events[0].raw).toBe('partial');
  });
});

// ---------------------------------------------------------------------------
// Full pipeline tests (line splitter + SSE parser)
// ---------------------------------------------------------------------------

describe('Full SSE pipeline', () => {
  it('parses SSE from a raw text stream', async () => {
    const text = [
      'event: update',
      'id: 1',
      'data: {"subject":"project.p1.agent.created","data":{"id":"a1","name":"test"}}',
      '',
      ':heartbeat 1715500000000',
      '',
      'event: update',
      'id: 2',
      'data: {"subject":"project.p1.agent.status","data":{"id":"a1","phase":"running"}}',
      '',
      '',
    ].join('\n');

    const body = sseBody(text);
    const textStream = body.pipeThrough(new TextDecoderStream());
    const lineStream = textStream.pipeThrough(createLineSplitter());
    const eventStream = lineStream.pipeThrough(createSSEParser());

    const reader = eventStream.getReader();
    const events: StreamEvent[] = [];
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      events.push(value);
    }

    expect(events).toHaveLength(2);
    expect(events[0].event).toBe('update');
    expect(events[0].id).toBe('1');
    expect(JSON.parse(events[0].raw)).toEqual({
      subject: 'project.p1.agent.created',
      data: { id: 'a1', name: 'test' },
    });
    expect(events[1].id).toBe('2');
  });

  it('handles chunked delivery', async () => {
    const text =
      'data: chunk1\n\ndata: chunk2\n\n';

    const body = sseBodyChunked(text, 5);
    const textStream = body.pipeThrough(new TextDecoderStream());
    const lineStream = textStream.pipeThrough(createLineSplitter());
    const eventStream = lineStream.pipeThrough(createSSEParser());

    const reader = eventStream.getReader();
    const events: StreamEvent[] = [];
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      events.push(value);
    }

    expect(events).toHaveLength(2);
    expect(events[0].raw).toBe('chunk1');
    expect(events[1].raw).toBe('chunk2');
  });
});

// ---------------------------------------------------------------------------
// ScionStream tests
// ---------------------------------------------------------------------------

describe('ScionStream', () => {
  it('yields parsed events as AsyncIterable', async () => {
    const sseText = [
      'data: {"value": 1}',
      '',
      'data: {"value": 2}',
      '',
      'data: {"value": 3}',
      '',
    ].join('\n');

    server.use(
      http.get(`${BASE_URL}/test/stream`, () => {
        return sseResponse(sseText);
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<{ value: number }>(
      client.transport,
      '/test/stream',
      (evt) => JSON.parse(evt.raw) as { value: number },
      { reconnect: false },
    );

    const events = await collect(stream);
    expect(events).toEqual([{ value: 1 }, { value: 2 }, { value: 3 }]);
  });

  it('filters null parse results', async () => {
    const sseText = [
      'data: good',
      '',
      'data: skip-me',
      '',
      'data: also-good',
      '',
    ].join('\n');

    server.use(
      http.get(`${BASE_URL}/test/filter`, () => {
        return sseResponse(sseText);
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/filter',
      (evt) => (evt.raw === 'skip-me' ? null : evt.raw),
      { reconnect: false },
    );

    const events = await collect(stream);
    expect(events).toEqual(['good', 'also-good']);
  });

  it('supports callback-style subscription', async () => {
    const sseText = [
      'data: event1',
      '',
      'data: event2',
      '',
    ].join('\n');

    server.use(
      http.get(`${BASE_URL}/test/callback`, () => {
        return sseResponse(sseText);
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/callback',
      (evt) => evt.raw,
      { reconnect: false },
    );

    const received: string[] = [];
    let closed = false;

    await stream.subscribe({
      onEvent: (event) => received.push(event),
      onClose: () => { closed = true; },
      reconnect: false,
    });

    expect(received).toEqual(['event1', 'event2']);
    expect(closed).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// AbortSignal cancellation tests
// ---------------------------------------------------------------------------

describe('AbortSignal cancellation', () => {
  it('stops iteration when signal is aborted', async () => {
    // Create an SSE stream that sends many events
    const lines: string[] = [];
    for (let i = 0; i < 50; i++) {
      lines.push(`data: event${i}`, '');
    }
    const sseText = lines.join('\n') + '\n';

    server.use(
      http.get(`${BASE_URL}/test/abort`, () => {
        return sseResponse(sseText);
      }),
    );

    const controller = new AbortController();
    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/abort',
      (evt) => evt.raw,
      { signal: controller.signal, reconnect: false },
    );

    const received: string[] = [];
    for await (const event of stream) {
      received.push(event);
      if (received.length >= 3) {
        controller.abort();
        break;
      }
    }

    expect(received.length).toBeLessThanOrEqual(4);
    expect(received[0]).toBe('event0');
  });

  it('pre-aborted signal does not start iteration', async () => {
    server.use(
      http.get(`${BASE_URL}/test/pre-abort`, () => {
        return sseResponse('data: should-not-see\n\n');
      }),
    );

    const controller = new AbortController();
    controller.abort(); // Pre-abort

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/pre-abort',
      (evt) => evt.raw,
      { signal: controller.signal, reconnect: false },
    );

    const received: string[] = [];
    for await (const event of stream) {
      received.push(event);
    }

    expect(received).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// Error handling tests
// ---------------------------------------------------------------------------

describe('Error handling', () => {
  it('throws StreamError on non-retryable failure with reconnect disabled', async () => {
    server.use(
      http.get(`${BASE_URL}/test/error`, () => {
        return HttpResponse.json(
          { error: { code: 'not_found', message: 'not found' } },
          { status: 404 },
        );
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/error',
      (evt) => evt.raw,
      { reconnect: false },
    );

    await expect(collect(stream)).rejects.toThrow();
  });

  it('calls onError callback on stream failure', async () => {
    server.use(
      http.get(`${BASE_URL}/test/cb-error`, () => {
        return HttpResponse.json(
          { error: { code: 'internal_error', message: 'boom' } },
          { status: 500 },
        );
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/cb-error',
      (evt) => evt.raw,
      { reconnect: false },
    );

    let capturedError: Error | undefined;
    let closed = false;

    await stream.subscribe({
      onEvent: () => {},
      onError: (err) => { capturedError = err; },
      onClose: () => { closed = true; },
      reconnect: false,
    });

    expect(capturedError).toBeDefined();
    expect(closed).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Heartbeat handling tests
// ---------------------------------------------------------------------------

describe('Heartbeat handling', () => {
  it('skips heartbeat comments and only yields data events', async () => {
    const sseText = [
      ':heartbeat 1715500000000',
      '',
      'data: real-event-1',
      '',
      ':heartbeat 1715500030000',
      '',
      ':heartbeat 1715500060000',
      '',
      'data: real-event-2',
      '',
    ].join('\n');

    server.use(
      http.get(`${BASE_URL}/test/heartbeat`, () => {
        return sseResponse(sseText);
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/heartbeat',
      (evt) => evt.raw,
      { reconnect: false },
    );

    const events = await collect(stream);
    expect(events).toEqual(['real-event-1', 'real-event-2']);
  });
});

// ---------------------------------------------------------------------------
// Reconnection logic tests
// ---------------------------------------------------------------------------

describe('Reconnection logic', () => {
  it('reconnects on server-initiated close and continues streaming', async () => {
    let callCount = 0;

    server.use(
      http.get(`${BASE_URL}/test/reconnect`, () => {
        callCount++;
        if (callCount === 1) {
          return sseResponse('data: batch1\n\n');
        }
        // Second connection returns more data, then closes
        return sseResponse('data: batch2\n\n');
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/reconnect',
      (evt) => evt.raw,
      {
        reconnect: true,
        maxReconnectAttempts: 2,
        initialReconnectDelay: 10, // Fast for tests
        maxReconnectDelay: 50,
      },
    );

    const events = await collect(stream, 10);

    // Should get events from both connections
    expect(events.length).toBeGreaterThanOrEqual(2);
    expect(events).toContain('batch1');
    expect(events).toContain('batch2');
    expect(callCount).toBeGreaterThanOrEqual(2);
  });

  it('gives up after maxReconnectAttempts on repeated failure', async () => {
    let callCount = 0;

    server.use(
      http.get(`${BASE_URL}/test/max-retry`, () => {
        callCount++;
        // Use 400 so transport does not retry (only 5xx triggers transport retry)
        return HttpResponse.json(
          { error: { code: 'invalid_request', message: 'bad request' } },
          { status: 400 },
        );
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/max-retry',
      (evt) => evt.raw,
      {
        reconnect: true,
        maxReconnectAttempts: 2,
        initialReconnectDelay: 10,
        maxReconnectDelay: 20,
      },
    );

    await expect(collect(stream)).rejects.toThrow();
    // Each attempt triggers one call (no transport-level retry for 4xx)
    expect(callCount).toBeGreaterThanOrEqual(2);
  });
});

// ---------------------------------------------------------------------------
// AgentsResource streaming integration tests
// ---------------------------------------------------------------------------

describe('AgentsResource streaming methods', () => {
  describe('streamEvents', () => {
    it('subscribes to agent events via /events endpoint', async () => {
      let capturedUrl = '';

      const sseText = [
        'event: update',
        'id: 1',
        'data: {"subject":"agent.a1.created","data":{"id":"a1","name":"test-agent","phase":"created"}}',
        '',
        'event: update',
        'id: 2',
        'data: {"subject":"agent.a1.status","data":{"id":"a1","phase":"running","activity":"idle"}}',
        '',
      ].join('\n');

      server.use(
        http.get(`${BASE_URL}/events`, ({ request }) => {
          capturedUrl = request.url;
          return sseResponse(sseText);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const stream = client.agents.streamEvents('a1', { reconnect: false });

      const events = await collect(stream);

      expect(events).toHaveLength(2);
      expect(events[0].type).toBe('created');
      expect(events[0].subject).toBe('agent.a1.created');
      expect(events[0].data.name).toBe('test-agent');
      expect(events[1].type).toBe('status');
      expect(events[1].data.phase).toBe('running');
      expect(capturedUrl).toContain('sub=agent.a1.*');
    });

    it('uses project-scoped subject pattern when projectId is set', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/events`, ({ request }) => {
          capturedUrl = request.url;
          return sseResponse('data: {"subject":"project.proj-1.agent.a1.status","data":{"id":"a1"}}\n\n');
        }),
      );

      const client = new ScionClient({
        hubUrl: BASE_URL,
        token: 'tok',
        projectId: 'proj-1',
      });
      const stream = client.agents.streamEvents('a1', { reconnect: false });
      await collect(stream);

      expect(capturedUrl).toContain('sub=project.proj-1.agent.a1.*');
    });

    it('supports callback-style streamEvents', async () => {
      const sseText = [
        'event: update',
        'data: {"subject":"agent.a1.status","data":{"id":"a1","phase":"running"}}',
        '',
      ].join('\n');

      server.use(
        http.get(`${BASE_URL}/events`, () => {
          return sseResponse(sseText);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const received: AgentEvent[] = [];

      await client.agents.streamEvents('a1', {
        onEvent: (event) => received.push(event),
        reconnect: false,
      });

      expect(received).toHaveLength(1);
      expect(received[0].data.phase).toBe('running');
    });

    it('handles malformed JSON gracefully (skips event)', async () => {
      const sseText = [
        'data: {"subject":"agent.a1.status","data":{"id":"a1"}}',
        '',
        'data: not-valid-json',
        '',
        'data: {"subject":"agent.a1.status","data":{"id":"a1","phase":"stopped"}}',
        '',
      ].join('\n');

      server.use(
        http.get(`${BASE_URL}/events`, () => {
          return sseResponse(sseText);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const stream = client.agents.streamEvents('a1', { reconnect: false });
      const events = await collect(stream);

      // Malformed JSON event should be skipped
      expect(events).toHaveLength(2);
      expect(events[1].data.phase).toBe('stopped');
    });
  });

  describe('streamCloudLogs', () => {
    it('streams log entries from the cloud-logs/stream endpoint', async () => {
      let capturedUrl = '';

      const sseText = [
        'data: {"timestamp":"2026-05-12T10:00:00Z","severity":"INFO","message":"Starting agent","insertId":"id1"}',
        '',
        'data: {"timestamp":"2026-05-12T10:00:01Z","severity":"ERROR","message":"Something failed","insertId":"id2"}',
        '',
      ].join('\n');

      server.use(
        http.get(`${BASE_URL}/api/v1/agents/a1/cloud-logs/stream`, ({ request }) => {
          capturedUrl = new URL(request.url).pathname;
          return sseResponse(sseText);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const stream = client.agents.streamCloudLogs('a1', { reconnect: false });

      const entries = await collect(stream);

      expect(capturedUrl).toBe('/api/v1/agents/a1/cloud-logs/stream');
      expect(entries).toHaveLength(2);
      expect(entries[0].severity).toBe('INFO');
      expect(entries[0].message).toBe('Starting agent');
      expect(entries[1].severity).toBe('ERROR');
      expect(entries[1].insertId).toBe('id2');
    });

    it('uses project-scoped path for cloud logs', async () => {
      let capturedUrl = '';

      server.use(
        http.get(
          `${BASE_URL}/api/v1/projects/proj-1/agents/a1/cloud-logs/stream`,
          ({ request }) => {
            capturedUrl = new URL(request.url).pathname;
            return sseResponse('data: {"timestamp":"2026-05-12T10:00:00Z","severity":"INFO","message":"log","insertId":"id1"}\n\n');
          },
        ),
      );

      const client = new ScionClient({
        hubUrl: BASE_URL,
        token: 'tok',
        projectId: 'proj-1',
      });
      const stream = client.agents.streamCloudLogs('a1', { reconnect: false });
      await collect(stream);

      expect(capturedUrl).toBe('/api/v1/projects/proj-1/agents/a1/cloud-logs/stream');
    });

    it('skips log entries without required fields', async () => {
      const sseText = [
        'data: {"timestamp":"2026-05-12T10:00:00Z","severity":"INFO","message":"valid","insertId":"id1"}',
        '',
        'data: {"severity":"INFO"}',  // missing timestamp and message
        '',
        'data: {"timestamp":"2026-05-12T10:00:01Z","severity":"WARN","message":"also valid","insertId":"id2"}',
        '',
      ].join('\n');

      server.use(
        http.get(`${BASE_URL}/api/v1/agents/a1/cloud-logs/stream`, () => {
          return sseResponse(sseText);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const stream = client.agents.streamCloudLogs('a1', { reconnect: false });
      const entries = await collect(stream);

      expect(entries).toHaveLength(2);
      expect(entries[0].insertId).toBe('id1');
      expect(entries[1].insertId).toBe('id2');
    });
  });
});

// ---------------------------------------------------------------------------
// Stream teardown tests
// ---------------------------------------------------------------------------

describe('Stream teardown', () => {
  it('cleans up when breaking out of for-await-of', async () => {
    const sseText = [
      'data: event1',
      '',
      'data: event2',
      '',
      'data: event3',
      '',
      'data: event4',
      '',
    ].join('\n');

    server.use(
      http.get(`${BASE_URL}/test/teardown`, () => {
        return sseResponse(sseText);
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/teardown',
      (evt) => evt.raw,
      { reconnect: false },
    );

    const received: string[] = [];
    for await (const event of stream) {
      received.push(event);
      if (received.length >= 2) break;
    }

    expect(received).toEqual(['event1', 'event2']);
    // No error thrown, no hanging promises
  });

  it('handles server returning empty body', async () => {
    server.use(
      http.get(`${BASE_URL}/test/empty`, () => {
        return sseResponse('');
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/empty',
      (evt) => evt.raw,
      { reconnect: false },
    );

    const events = await collect(stream);
    expect(events).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// Accept header test
// ---------------------------------------------------------------------------

describe('SSE connection headers', () => {
  it('sends Accept: text/event-stream header', async () => {
    let capturedAccept: string | null = null;

    server.use(
      http.get(`${BASE_URL}/test/headers`, ({ request }) => {
        capturedAccept = request.headers.get('Accept');
        return sseResponse('data: ok\n\n');
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/headers',
      (evt) => evt.raw,
      { reconnect: false },
    );

    await collect(stream);

    expect(capturedAccept).toBe('text/event-stream');
  });

  it('sends Authorization header', async () => {
    let capturedAuth: string | null = null;

    server.use(
      http.get(`${BASE_URL}/test/auth-headers`, ({ request }) => {
        capturedAuth = request.headers.get('Authorization');
        return sseResponse('data: ok\n\n');
      }),
    );

    const client = new ScionClient({ hubUrl: BASE_URL, token: 'my-token' });
    const stream = new ScionStream<string>(
      client.transport,
      '/test/auth-headers',
      (evt) => evt.raw,
      { reconnect: false },
    );

    await collect(stream);

    expect(capturedAuth).toBe('Bearer my-token');
  });
});
