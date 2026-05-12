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

import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
import { ScionClient } from '../src/client.js';
import { ScionAPIError } from '../src/errors.js';
import type { Message } from '../src/types/messages.js';
import type { Page } from '../src/types/common.js';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Build a fake Message with sensible defaults. */
function fakeMessage(overrides: Partial<Message> = {}): Message {
  return {
    id: 'msg-001',
    projectId: 'proj-abc',
    sender: 'agent:code-reviewer',
    senderId: 'agent-uuid-1',
    recipient: 'user:alice',
    recipientId: 'user-uuid-1',
    msg: 'Build succeeded',
    type: 'state-change',
    read: false,
    agentId: 'agent-uuid-1',
    createdAt: '2026-05-12T10:00:00Z',
    ...overrides,
  };
}

/** Construct a successful JSON Response. */
function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** Construct an error JSON Response. */
function errorResponse(
  status: number,
  code: string,
  message: string,
): Response {
  return new Response(
    JSON.stringify({ code, message }),
    { status, headers: { 'Content-Type': 'application/json' } },
  );
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('MessagesResource', () => {
  let client: ScionClient;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    client = new ScionClient({
      baseUrl: 'https://hub.test',
      token: 'test-token',
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // -------------------------------------------------------------------------
  // list()
  // -------------------------------------------------------------------------

  describe('list()', () => {
    it('returns a page of messages with no params', async () => {
      const page: Page<Message> = {
        items: [fakeMessage()],
        nextCursor: 'cursor-2',
        totalCount: 1,
      };
      fetchMock.mockResolvedValueOnce(jsonResponse(page));

      const result = await client.messages.list();

      expect(result.items).toHaveLength(1);
      expect(result.items[0].id).toBe('msg-001');
      expect(result.nextCursor).toBe('cursor-2');

      // Verify request
      const [url, init] = fetchMock.mock.calls[0];
      expect(url).toBe('https://hub.test/api/v1/messages');
      expect(init.method).toBe('GET');
      expect(init.headers).toHaveProperty('Authorization', 'Bearer test-token');
    });

    it('passes onlyUnread as query param', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await client.messages.list({ onlyUnread: true });

      const [url] = fetchMock.mock.calls[0];
      expect(url).toContain('unread=true');
    });

    it('passes agentId as query param', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await client.messages.list({ agentId: 'code-reviewer' });

      const [url] = fetchMock.mock.calls[0];
      expect(url).toContain('agent=code-reviewer');
    });

    it('passes projectId as query param', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await client.messages.list({ projectId: 'proj-xyz' });

      const [url] = fetchMock.mock.calls[0];
      expect(url).toContain('project=proj-xyz');
    });

    it('passes type as query param', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await client.messages.list({ type: 'instruction' });

      const [url] = fetchMock.mock.calls[0];
      expect(url).toContain('type=instruction');
    });

    it('passes limit as query param', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await client.messages.list({ limit: 25 });

      const [url] = fetchMock.mock.calls[0];
      expect(url).toContain('limit=25');
    });

    it('passes cursor as query param', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await client.messages.list({ cursor: 'abc123' });

      const [url] = fetchMock.mock.calls[0];
      expect(url).toContain('cursor=abc123');
    });

    it('combines multiple filter parameters', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await client.messages.list({
        onlyUnread: true,
        agentId: 'deploy-bot',
        projectId: 'proj-1',
        type: 'input-needed',
        limit: 10,
        cursor: 'page-2',
      });

      const [url] = fetchMock.mock.calls[0];
      expect(url).toContain('unread=true');
      expect(url).toContain('agent=deploy-bot');
      expect(url).toContain('project=proj-1');
      expect(url).toContain('type=input-needed');
      expect(url).toContain('limit=10');
      expect(url).toContain('cursor=page-2');
    });

    it('defaults items to empty array when null', async () => {
      fetchMock.mockResolvedValueOnce(
        jsonResponse({ items: null, nextCursor: '' }),
      );

      const result = await client.messages.list();

      expect(result.items).toEqual([]);
    });

    it('defaults items to empty array when undefined', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ nextCursor: '' }));

      const result = await client.messages.list();

      expect(result.items).toEqual([]);
    });

    it('skips zero limit', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await client.messages.list({ limit: 0 });

      const [url] = fetchMock.mock.calls[0];
      expect(url).not.toContain('limit');
    });

    it('does not set onlyUnread when false', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await client.messages.list({ onlyUnread: false });

      const [url] = fetchMock.mock.calls[0];
      expect(url).not.toContain('unread');
    });
  });

  // -------------------------------------------------------------------------
  // get()
  // -------------------------------------------------------------------------

  describe('get()', () => {
    it('returns a single message', async () => {
      const msg = fakeMessage({ id: 'msg-42' });
      fetchMock.mockResolvedValueOnce(jsonResponse(msg));

      const result = await client.messages.get('msg-42');

      expect(result.id).toBe('msg-42');
      expect(result.sender).toBe('agent:code-reviewer');

      const [url, init] = fetchMock.mock.calls[0];
      expect(url).toBe('https://hub.test/api/v1/messages/msg-42');
      expect(init.method).toBe('GET');
    });

    it('URL-encodes the message ID', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse(fakeMessage()));

      await client.messages.get('msg/special chars');

      const [url] = fetchMock.mock.calls[0];
      expect(url).toContain('/api/v1/messages/msg%2Fspecial%20chars');
    });

    it('throws ScionAPIError on 404', async () => {
      fetchMock.mockResolvedValueOnce(
        errorResponse(404, 'not_found', 'Message not found'),
      );

      try {
        await client.messages.get('missing');
        expect.fail('Expected an error');
      } catch (e) {
        expect(e).toBeInstanceOf(ScionAPIError);
        const err = e as ScionAPIError;
        expect(err.isNotFound()).toBe(true);
        expect(err.code).toBe('not_found');
      }
    });
  });

  // -------------------------------------------------------------------------
  // markRead()
  // -------------------------------------------------------------------------

  describe('markRead()', () => {
    it('sends POST to mark a message as read', async () => {
      fetchMock.mockResolvedValueOnce(
        new Response(null, { status: 204 }),
      );

      await client.messages.markRead('msg-99');

      const [url, init] = fetchMock.mock.calls[0];
      expect(url).toBe('https://hub.test/api/v1/messages/msg-99/read');
      expect(init.method).toBe('POST');
    });

    it('URL-encodes the message ID', async () => {
      fetchMock.mockResolvedValueOnce(
        new Response(null, { status: 204 }),
      );

      await client.messages.markRead('msg/special');

      const [url] = fetchMock.mock.calls[0];
      expect(url).toContain('/api/v1/messages/msg%2Fspecial/read');
    });

    it('throws ScionAPIError on failure', async () => {
      fetchMock.mockResolvedValueOnce(
        errorResponse(404, 'not_found', 'Message not found'),
      );

      await expect(client.messages.markRead('missing')).rejects.toThrow(
        ScionAPIError,
      );
    });
  });

  // -------------------------------------------------------------------------
  // markAllRead()
  // -------------------------------------------------------------------------

  describe('markAllRead()', () => {
    it('sends POST to mark all messages as read', async () => {
      fetchMock.mockResolvedValueOnce(
        new Response(null, { status: 204 }),
      );

      await client.messages.markAllRead();

      const [url, init] = fetchMock.mock.calls[0];
      expect(url).toBe('https://hub.test/api/v1/messages/read-all');
      expect(init.method).toBe('POST');
    });

    it('throws ScionAPIError on server error', async () => {
      fetchMock.mockResolvedValueOnce(
        errorResponse(500, 'internal_error', 'Internal server error'),
      );

      try {
        await client.messages.markAllRead();
        expect.fail('Expected an error');
      } catch (e) {
        expect(e).toBeInstanceOf(ScionAPIError);
        const err = e as ScionAPIError;
        expect(err.isServerError()).toBe(true);
      }
    });
  });

  // -------------------------------------------------------------------------
  // Authentication
  // -------------------------------------------------------------------------

  describe('authentication', () => {
    it('includes Bearer token in requests', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await client.messages.list();

      const [, init] = fetchMock.mock.calls[0];
      expect(init.headers).toHaveProperty('Authorization', 'Bearer test-token');
    });

    it('works without a token', async () => {
      const noAuthClient = new ScionClient({ baseUrl: 'https://hub.test' });
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [] }));

      await noAuthClient.messages.list();

      const [, init] = fetchMock.mock.calls[0];
      expect(init.headers).not.toHaveProperty('Authorization');
    });

    it('handles 401 Unauthorized', async () => {
      fetchMock.mockResolvedValueOnce(
        errorResponse(401, 'unauthorized', 'Invalid token'),
      );

      try {
        await client.messages.list();
        expect.fail('Expected an error');
      } catch (e) {
        expect(e).toBeInstanceOf(ScionAPIError);
        const err = e as ScionAPIError;
        expect(err.isUnauthorized()).toBe(true);
      }
    });
  });

  // -------------------------------------------------------------------------
  // Error handling
  // -------------------------------------------------------------------------

  describe('error handling', () => {
    it('parses structured API errors', async () => {
      fetchMock.mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            code: 'rate_limited',
            message: 'Too many requests',
            requestId: 'req-123',
          }),
          { status: 429, headers: { 'Content-Type': 'application/json' } },
        ),
      );

      try {
        await client.messages.list();
        expect.fail('Expected an error');
      } catch (e) {
        const err = e as ScionAPIError;
        expect(err).toBeInstanceOf(ScionAPIError);
        expect(err.statusCode).toBe(429);
        expect(err.code).toBe('rate_limited');
        expect(err.message).toBe('Too many requests');
        expect(err.requestId).toBe('req-123');
        expect(err.isRateLimited()).toBe(true);
      }
    });

    it('handles non-JSON error bodies gracefully', async () => {
      fetchMock.mockResolvedValueOnce(
        new Response('Bad Gateway', {
          status: 502,
          statusText: 'Bad Gateway',
        }),
      );

      try {
        await client.messages.list();
        expect.fail('Expected an error');
      } catch (e) {
        const err = e as ScionAPIError;
        expect(err).toBeInstanceOf(ScionAPIError);
        expect(err.statusCode).toBe(502);
        expect(err.isServerError()).toBe(true);
      }
    });
  });

  // -------------------------------------------------------------------------
  // ScionClient wiring
  // -------------------------------------------------------------------------

  describe('ScionClient integration', () => {
    it('exposes messages as a property', () => {
      expect(client.messages).toBeDefined();
      expect(typeof client.messages.list).toBe('function');
      expect(typeof client.messages.get).toBe('function');
      expect(typeof client.messages.markRead).toBe('function');
      expect(typeof client.messages.markAllRead).toBe('function');
    });
  });
});
