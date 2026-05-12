import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { ScionClient } from '../src/client.js';
import { MessagesResource } from '../src/resources/messages.js';
import { NotFoundError, ServerError } from '../src/errors.js';
import type { Message } from '../src/types/index.js';

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const BASE_URL = 'http://hub.test:9999';

const server = setupServer();

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  server.resetHandlers();
  vi.unstubAllEnvs();
});
afterAll(() => server.close());

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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('MessagesResource', () => {
  describe('list()', () => {
    it('returns a page of messages with no params', async () => {
      const messages = [fakeMessage()];

      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, () => {
          return HttpResponse.json({
            items: messages,
            nextCursor: 'cursor-2',
            totalCount: 1,
          });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'test-token' });
      const page = await client.messages.list();

      expect(page.data).toHaveLength(1);
      expect(page.data[0].id).toBe('msg-001');
      expect(page.nextCursor).toBe('cursor-2');
      expect(page.totalCount).toBe(1);
    });

    it('passes onlyUnread as query param', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ items: [] });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.messages.list({ onlyUnread: true });

      expect(capturedUrl).toContain('unread=true');
    });

    it('passes agentId as query param', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ items: [] });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.messages.list({ agentId: 'code-reviewer' });

      expect(capturedUrl).toContain('agent=code-reviewer');
    });

    it('passes projectId as query param', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ items: [] });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.messages.list({ projectId: 'proj-xyz' });

      expect(capturedUrl).toContain('project=proj-xyz');
    });

    it('passes type as query param', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ items: [] });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.messages.list({ type: 'instruction' });

      expect(capturedUrl).toContain('type=instruction');
    });

    it('passes limit and cursor', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ items: [] });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.messages.list({ limit: 25, cursor: 'abc123' });

      expect(capturedUrl).toContain('limit=25');
      expect(capturedUrl).toContain('cursor=abc123');
    });

    it('combines multiple filter parameters', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ items: [] });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.messages.list({
        onlyUnread: true,
        agentId: 'deploy-bot',
        projectId: 'proj-1',
        type: 'input-needed',
        limit: 10,
        cursor: 'page-2',
      });

      expect(capturedUrl).toContain('unread=true');
      expect(capturedUrl).toContain('agent=deploy-bot');
      expect(capturedUrl).toContain('project=proj-1');
      expect(capturedUrl).toContain('type=input-needed');
      expect(capturedUrl).toContain('limit=10');
      expect(capturedUrl).toContain('cursor=page-2');
    });

    it('defaults items to empty array when null/undefined', async () => {
      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, () => {
          return HttpResponse.json({ nextCursor: '' });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.messages.list();

      expect(result.data).toEqual([]);
    });

    it('skips zero limit', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ items: [] });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.messages.list({ limit: 0 });

      expect(capturedUrl).not.toContain('limit');
    });

    it('supports async iteration across pages', async () => {
      let callCount = 0;

      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, ({ request }) => {
          callCount++;
          const url = new URL(request.url);
          if (url.searchParams.get('cursor') === 'page2') {
            return HttpResponse.json({
              items: [fakeMessage({ id: 'msg-3' })],
            });
          }
          return HttpResponse.json({
            items: [fakeMessage({ id: 'msg-1' }), fakeMessage({ id: 'msg-2' })],
            nextCursor: 'page2',
            totalCount: 3,
          });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const ids: string[] = [];
      const page = await client.messages.list();

      for await (const msg of page) {
        ids.push(msg.id);
      }

      expect(ids).toEqual(['msg-1', 'msg-2', 'msg-3']);
      expect(callCount).toBe(2);
    });
  });

  describe('get()', () => {
    it('returns a single message', async () => {
      const msg = fakeMessage({ id: 'msg-42' });

      server.use(
        http.get(`${BASE_URL}/api/v1/messages/msg-42`, () => {
          return HttpResponse.json(msg);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.messages.get('msg-42');

      expect(result.id).toBe('msg-42');
      expect(result.sender).toBe('agent:code-reviewer');
    });

    it('throws NotFoundError on 404', async () => {
      server.use(
        http.get(`${BASE_URL}/api/v1/messages/missing`, () => {
          return HttpResponse.json(
            { error: { code: 'not_found', message: 'Message not found' } },
            { status: 404 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.messages.get('missing')).rejects.toThrow(NotFoundError);
    });
  });

  describe('markRead()', () => {
    it('sends POST to mark a message as read', async () => {
      let capturedPath = '';

      server.use(
        http.post(`${BASE_URL}/api/v1/messages/msg-99/read`, ({ request }) => {
          capturedPath = new URL(request.url).pathname;
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.messages.markRead('msg-99');

      expect(capturedPath).toBe('/api/v1/messages/msg-99/read');
    });
  });

  describe('markAllRead()', () => {
    it('sends POST to mark all messages as read', async () => {
      let capturedPath = '';

      server.use(
        http.post(`${BASE_URL}/api/v1/messages/read-all`, ({ request }) => {
          capturedPath = new URL(request.url).pathname;
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.messages.markAllRead();

      expect(capturedPath).toBe('/api/v1/messages/read-all');
    });

    it('throws ServerError on 500', async () => {
      server.use(
        http.post(`${BASE_URL}/api/v1/messages/read-all`, () => {
          return HttpResponse.json(
            { error: { code: 'internal_error', message: 'Internal server error' } },
            { status: 500 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.messages.markAllRead()).rejects.toThrow(ServerError);
    });
  });

  describe('authentication', () => {
    it('includes Bearer token in requests', async () => {
      let capturedAuth: string | null = null;

      server.use(
        http.get(`${BASE_URL}/api/v1/messages`, ({ request }) => {
          capturedAuth = request.headers.get('Authorization');
          return HttpResponse.json({ items: [] });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'test-token' });
      await client.messages.list();

      expect(capturedAuth).toBe('Bearer test-token');
    });
  });

  describe('ScionClient integration', () => {
    it('exposes messages as a property', () => {
      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      expect(client.messages).toBeDefined();
      expect(client.messages).toBeInstanceOf(MessagesResource);
      expect(typeof client.messages.list).toBe('function');
      expect(typeof client.messages.get).toBe('function');
      expect(typeof client.messages.markRead).toBe('function');
      expect(typeof client.messages.markAllRead).toBe('function');
    });
  });
});
