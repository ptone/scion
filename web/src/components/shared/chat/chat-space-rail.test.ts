/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * Tests for what clicking a collapsed space in the rail does.
 *
 * On desktop the rail sits beside the conversation, so the click can open
 * #general as a shortcut. On mobile the rail is a screen of its own and
 * selecting a thread slides it away — which would hide the thread list the
 * click was asking to see — so there the click only expands the space.
 *
 * The element is created but never appended, so connectedCallback (and its
 * network calls) never runs.
 */

// @vitest-environment happy-dom

import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { apiFetch } from '../../../client/api.js';

/* eslint-disable @typescript-eslint/no-explicit-any */

vi.mock('../../../client/api.js', () => ({
  apiFetch: vi.fn(() => Promise.resolve(new Response('{}', { status: 200 }))),
}));

const SPACE = {
  projectId: 'proj-1',
  projectName: 'Chat Test',
  projectSlug: 'chat-test',
  unreadCount: 0,
  hasUnreadMention: false,
};

const GENERAL = {
  id: 'topic-general',
  name: 'general',
  isGeneral: true,
  pinned: false,
  hasUnread: false,
  hasUnreadMention: false,
};

/** A rail holding one collapsed space with a #general thread in it. */
function createRail(): any {
  const el = document.createElement('scion-chat-space-rail') as any;
  el.spaces = [SPACE];
  el.threadsBySpace = new Map([[SPACE.projectId, [GENERAL]]]);
  el.collapsedSpaces = new Set([SPACE.projectId]);
  return el;
}

/** Click the collapsed space and report the thread-select it did or did not fire. */
function clickCollapsedSpace(el: any): CustomEvent | null {
  let selected: CustomEvent | null = null;
  el.addEventListener('thread-select', (e: Event) => {
    selected = e as CustomEvent;
  });
  el.handleCollapsedSpaceClick(SPACE);
  return selected;
}

beforeAll(async () => {
  await import('./chat-space-rail.js');
});

afterEach(() => {
  (window as any).innerWidth = 1024;
  localStorage.clear();
  vi.mocked(apiFetch).mockClear();
  vi.restoreAllMocks();
});

describe('space rail — expanding a space', () => {
  it('expands the space and opens #general on desktop', () => {
    (window as any).innerWidth = 1400;
    const el = createRail();

    const selected = clickCollapsedSpace(el);

    expect(el.collapsedSpaces.has(SPACE.projectId)).toBe(false);
    expect(selected?.detail).toMatchObject({
      conversationKey: 'topic-general',
      projectId: 'proj-1',
      projectSlug: 'chat-test',
      threadName: 'general',
    });
  });

  it('only expands the space on mobile, leaving the thread list on screen', () => {
    (window as any).innerWidth = 400;
    const el = createRail();

    const selected = clickCollapsedSpace(el);

    expect(el.collapsedSpaces.has(SPACE.projectId)).toBe(false);
    expect(selected).toBeNull();
  });

  it('expands on request without selecting a thread', () => {
    const el = createRail();
    let selected: CustomEvent | null = null;
    el.addEventListener('thread-select', (e: Event) => {
      selected = e as CustomEvent;
    });

    el.expandSpace(SPACE.projectId);

    expect(el.collapsedSpaces.has(SPACE.projectId)).toBe(false);
    expect(selected).toBeNull();
  });

  it('expands a space that has no #general to open', () => {
    (window as any).innerWidth = 1400;
    const el = createRail();
    el.threadsBySpace = new Map();

    const selected = clickCollapsedSpace(el);

    expect(el.collapsedSpaces.has(SPACE.projectId)).toBe(false);
    expect(selected).toBeNull();
  });
});

describe('space rail — pinning threads', () => {
  const THREAD = {
    id: 'topic-design',
    name: 'design',
    isGeneral: false,
    pinned: false,
    hasUnread: false,
    hasUnreadMention: false,
  };

  function createRailWithThread(pinned: boolean): any {
    const el = document.createElement('scion-chat-space-rail') as any;
    el.spaces = [SPACE];
    el.threadsBySpace = new Map([[SPACE.projectId, [GENERAL, { ...THREAD, pinned }]]]);
    return el;
  }

  function threadState(el: any): any {
    return el.threadsBySpace.get(SPACE.projectId).find((t: any) => t.id === THREAD.id);
  }

  it('asks the server to pin an unpinned thread and reflects it locally', async () => {
    const el = createRailWithThread(false);

    await el.handleTogglePin(threadState(el), SPACE.projectId);

    expect(apiFetch).toHaveBeenCalledWith(
      '/api/v1/chat/conversations/topic-design/pin',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ pinned: true }) })
    );
    expect(threadState(el).pinned).toBe(true);
  });

  it('unpins a pinned thread', async () => {
    const el = createRailWithThread(true);

    await el.handleTogglePin(threadState(el), SPACE.projectId);

    expect(apiFetch).toHaveBeenCalledWith(
      '/api/v1/chat/conversations/topic-design/pin',
      expect.objectContaining({ body: JSON.stringify({ pinned: false }) })
    );
    expect(threadState(el).pinned).toBe(false);
  });

  it('leaves local state alone when the server rejects the change', async () => {
    const el = createRailWithThread(false);
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response('{}', { status: 500 }));

    await el.handleTogglePin(threadState(el), SPACE.projectId);

    expect(threadState(el).pinned).toBe(false);
  });
});

describe('space rail — pinning spaces', () => {
  const OTHER_SPACE = {
    projectId: 'proj-2',
    projectName: 'Another Project',
    projectSlug: 'another',
    unreadCount: 0,
    hasUnreadMention: false,
  };

  function createRailWithSpaces(): any {
    const el = document.createElement('scion-chat-space-rail') as any;
    el.spaces = [SPACE, OTHER_SPACE];
    el.threadsBySpace = new Map();
    return el;
  }

  it('hoists a pinned space above the others and persists the pin', () => {
    const el = createRailWithSpaces();

    el.toggleSpacePin(OTHER_SPACE.projectId);

    expect(el.getSortedSpaces()[0].projectId).toBe(OTHER_SPACE.projectId);
    expect(JSON.parse(localStorage.getItem('scion-chat-pinned-spaces') ?? '[]')).toEqual([
      OTHER_SPACE.projectId,
    ]);
  });

  it('unpins a pinned space', () => {
    const el = createRailWithSpaces();

    el.toggleSpacePin(OTHER_SPACE.projectId);
    el.toggleSpacePin(OTHER_SPACE.projectId);

    expect(el.pinnedSpaces.size).toBe(0);
    expect(el.getSortedSpaces()[0].projectId).toBe(SPACE.projectId);
  });

  it('restores pins from localStorage', () => {
    localStorage.setItem('scion-chat-pinned-spaces', JSON.stringify([OTHER_SPACE.projectId]));
    const el = createRailWithSpaces();

    el.loadPinnedSpaces();

    expect(el.getSortedSpaces()[0].projectId).toBe(OTHER_SPACE.projectId);
  });

  it('ignores a corrupt localStorage entry', () => {
    localStorage.setItem('scion-chat-pinned-spaces', 'not json');
    const el = createRailWithSpaces();

    el.loadPinnedSpaces();

    expect(el.pinnedSpaces.size).toBe(0);
  });
});
