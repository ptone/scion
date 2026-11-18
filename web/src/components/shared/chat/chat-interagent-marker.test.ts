/**
 * Tests for <scion-chat-interagent-marker> cross-project display.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { setPreferredTimeZone } from '../../../utils/time.js';

vi.mock('../../../utils/markdown.js', () => {
  const render = (markdown: string): string => `<p>${markdown}</p>`;
  return {
    getMarkdownRenderer: () =>
      Promise.resolve({
        render,
        renderFragment: (markdown: string) => {
          const tpl = document.createElement('template');
          tpl.innerHTML = render(markdown);
          return tpl.content;
        },
      }),
  };
});

await import('./chat-interagent-marker.js');
type ScionChatInteragentMarker = import('./chat-interagent-marker.js').ScionChatInteragentMarker;
type Message = import('../../../shared/types.js').Message;

function makeMessage(overrides: Partial<Message> = {}): Message {
  return {
    id: 'msg-1',
    projectId: 'proj-a',
    sender: 'agent:alpha',
    senderId: 'agent-alpha-id',
    recipient: 'agent:beta',
    recipientId: 'agent-beta-id',
    msg: 'Hello',
    type: 'agent-message',
    agentId: 'agent-alpha-id',
    createdAt: new Date().toISOString(),
    ...overrides,
  };
}

describe('scion-chat-interagent-marker', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
  });

  afterEach(() => {
    document.body.innerHTML = '';
  });

  it('renders collapsed pill with message count', async () => {
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    el.messageCount = 3;
    el.messages = [makeMessage(), makeMessage({ id: 'msg-2' }), makeMessage({ id: 'msg-3' })];
    document.body.appendChild(el);
    await el.updateComplete;

    const pill = el.shadowRoot?.querySelector('.marker-pill');
    expect(pill).toBeTruthy();
    expect(pill?.textContent).toContain('3');
    expect(pill?.textContent).toContain('messages');
  });

  it('shows cross-project count in collapsed pill', async () => {
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    el.messageCount = 2;
    el.messages = [
      makeMessage({ senderProjectId: 'proj-b', recipientProjectId: 'proj-a' }),
      makeMessage({ id: 'msg-2', senderProjectId: 'proj-a', recipientProjectId: 'proj-a' }),
    ];
    document.body.appendChild(el);
    await el.updateComplete;

    const pill = el.shadowRoot?.querySelector('.marker-pill');
    expect(pill?.textContent).toContain('1');
    expect(pill?.textContent).toContain('cross-project');
  });

  it('formats participant with project prefix for cross-project messages when expanded', async () => {
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    el.messageCount = 1;
    el.messages = [
      makeMessage({
        sender: 'agent:remote-bot',
        senderProjectId: 'proj-bbbb-cccc-dddd',
        recipient: 'agent:local-bot',
        recipientProjectId: 'proj-aaaa-1111-2222',
      }),
    ];
    el.expanded = true;
    document.body.appendChild(el);
    await el.updateComplete;

    const sender = el.shadowRoot?.querySelector('.ia-sender');
    expect(sender?.textContent).toBe('proj-bbb / remote-bot');
    const recipient = el.shadowRoot?.querySelector('.ia-recipient');
    expect(recipient?.textContent).toBe('proj-aaa / local-bot');
  });

  it('does not add prefix for same-project participants', async () => {
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    el.messageCount = 1;
    el.messages = [
      makeMessage({
        sender: 'agent:local-bot',
        senderProjectId: 'proj-a',
        recipient: 'agent:other-bot',
        recipientProjectId: 'proj-a',
      }),
    ];
    el.expanded = true;
    document.body.appendChild(el);
    await el.updateComplete;

    const sender = el.shadowRoot?.querySelector('.ia-sender');
    expect(sender?.textContent).toBe('local-bot');
    const recipient = el.shadowRoot?.querySelector('.ia-recipient');
    expect(recipient?.textContent).toBe('other-bot');
  });

  it('does not tag same-project messages as cross-project when current project differs (ptone/scion#8273)', async () => {
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    el.setAttribute('current-project-id', 'proj-other');
    el.messageCount = 1;
    el.messages = [
      makeMessage({
        sender: 'agent:local-bot',
        senderProjectId: 'proj-aaaa-1111',
        recipient: 'agent:other-bot',
        recipientProjectId: 'proj-aaaa-1111',
      }),
    ];
    document.body.appendChild(el);
    await el.updateComplete;

    // Collapsed pill must not show cross-project badge.
    expect(el.shadowRoot?.querySelector('.ia-cross-project')).toBeNull();
    expect(el.shadowRoot?.textContent).not.toContain('cross-project');

    // Expanded view must not show cross-project badge or project prefixes.
    el.expanded = true;
    await el.updateComplete;

    expect(el.shadowRoot?.querySelector('.ia-cross-project')).toBeNull();
    expect(el.shadowRoot?.querySelector('.ia-sender')?.textContent).toBe('local-bot');
    expect(el.shadowRoot?.querySelector('.ia-recipient')?.textContent).toBe('other-bot');

    // Full-content preview dialog must also omit cross-project badge and prefixes.
    const internals = el as unknown as {
      openMessagePreview(msg: Message, e: Event): Promise<void>;
    };
    await internals.openMessagePreview(el.messages[0], new Event('click'));
    await el.updateComplete;

    const previewHeader = el.shadowRoot?.querySelector('.ia-full-header');
    expect(previewHeader).toBeTruthy();
    expect(previewHeader?.querySelector('.ia-cross-project')).toBeNull();
    expect(previewHeader?.querySelector('.ia-sender')?.textContent).toBe('local-bot');
    expect(previewHeader?.querySelector('.ia-recipient')?.textContent).toBe('other-bot');
  });

  it('tags messages with different sender and recipient projects as cross-project regardless of current project (ptone/scion#8273)', async () => {
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    // No current-project-id set (e.g. cold-loaded DM or hub view), then switched across projects.
    el.messageCount = 1;
    el.messages = [
      makeMessage({
        sender: 'agent:sender-bot',
        senderProjectId: 'proj-aaaa-1111',
        recipient: 'agent:recip-bot',
        recipientProjectId: 'proj-bbbb-2222',
      }),
    ];
    document.body.appendChild(el);
    await el.updateComplete;

    // Collapsed pill shows cross-project count even without current-project-id.
    expect(el.shadowRoot?.querySelector('.ia-cross-project')?.textContent).toMatch(
      /1\s+cross-project/
    );

    // Expanded view shows cross-project badge and project prefixes on both participants.
    el.expanded = true;
    await el.updateComplete;

    expect(el.shadowRoot?.querySelector('.ia-cross-project')).toBeTruthy();
    expect(el.shadowRoot?.querySelector('.ia-sender')?.textContent).toBe('proj-aaa / sender-bot');
    expect(el.shadowRoot?.querySelector('.ia-recipient')?.textContent).toBe('proj-bbb / recip-bot');

    // Full-content preview dialog also shows cross-project badge and prefixes.
    const internals = el as unknown as {
      openMessagePreview(msg: Message, e: Event): Promise<void>;
    };
    await internals.openMessagePreview(el.messages[0], new Event('click'));
    await el.updateComplete;

    const previewHeader = el.shadowRoot?.querySelector('.ia-full-header');
    expect(previewHeader?.querySelector('.ia-cross-project')).toBeTruthy();
    expect(previewHeader?.querySelector('.ia-sender')?.textContent).toBe('proj-aaa / sender-bot');
    expect(previewHeader?.querySelector('.ia-recipient')?.textContent).toBe('proj-bbb / recip-bot');
  });

  it('does not tag unstamped legacy messages as cross-project', async () => {
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    el.setAttribute('current-project-id', 'proj-other');
    el.messageCount = 2;
    el.messages = [
      makeMessage({ id: 'legacy-none' }),
      makeMessage({ id: 'legacy-partial', senderProjectId: 'proj-a' }),
    ];
    el.expanded = true;
    document.body.appendChild(el);
    await el.updateComplete;

    expect(el.shadowRoot?.querySelector('.ia-cross-project')).toBeNull();
  });

  it('renders the shared date divider for each subsequent day when messages span multiple days', async () => {
    // Defensive case: the main timeline splits runs by day before handing
    // messages to a marker, but the marker itself must still fall back to
    // the shared separator (not a bespoke style) if it ever spans days.
    // The first day's divider is the main timeline's own — rendered directly
    // above the marker — so the marker itself must not repeat it; only the
    // day change to Jan 16 gets an internal divider.
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    el.messageCount = 2;
    el.messages = [
      makeMessage({ id: 'msg-1', createdAt: new Date(2026, 0, 15, 9, 0).toISOString() }),
      makeMessage({ id: 'msg-2', createdAt: new Date(2026, 0, 16, 10, 0).toISOString() }),
    ];
    el.expanded = true;
    document.body.appendChild(el);
    await el.updateComplete;

    expect(el.shadowRoot?.querySelectorAll('.ia-date-divider').length).toBe(0);
    const dividers = el.shadowRoot?.querySelectorAll('.date-divider');
    expect(dividers?.length).toBe(1);
    expect(dividers?.[0].textContent).toContain('Jan 16');
  });

  it('renders no internal date divider when all messages fall on the same day', async () => {
    // The main timeline already renders the shared separator above the
    // marker for this day, so a single-day marker must add nothing of its
    // own — otherwise every marker would show the date twice.
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    el.messageCount = 2;
    el.messages = [
      makeMessage({ id: 'msg-1', createdAt: new Date(2026, 0, 15, 9, 0).toISOString() }),
      makeMessage({ id: 'msg-2', createdAt: new Date(2026, 0, 15, 10, 0).toISOString() }),
    ];
    el.expanded = true;
    document.body.appendChild(el);
    await el.updateComplete;

    const dividers = el.shadowRoot?.querySelectorAll('.date-divider');
    expect(dividers?.length).toBe(0);
  });

  it('renders a time label in the header for each message', async () => {
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    el.messageCount = 1;
    el.messages = [makeMessage({ createdAt: new Date(2026, 0, 15, 14, 30).toISOString() })];
    el.expanded = true;
    document.body.appendChild(el);
    await el.updateComplete;

    const time = el.shadowRoot?.querySelector('.ia-time');
    expect(time).toBeTruthy();
    expect(time?.textContent).toMatch(/^\d{2}:\d{2}$/);
    expect(time?.textContent).toContain('14:30');
  });

  it('renders an empty time label for an invalid createdAt', async () => {
    const el = document.createElement('scion-chat-interagent-marker') as ScionChatInteragentMarker;
    el.messageCount = 1;
    el.messages = [makeMessage({ createdAt: 'not-a-date' })];
    el.expanded = true;
    document.body.appendChild(el);
    await el.updateComplete;

    const time = el.shadowRoot?.querySelector('.ia-time');
    expect(time).toBeTruthy();
    expect(time?.textContent).toBe('');
  });

  // AC4, review R2-3: the expanded row's time carries a title with the
  // full instant and zone label.
  it('titles the time label with the full instant and zone (AC4, review R2-3)', async () => {
    setPreferredTimeZone('Asia/Tokyo');
    try {
      const el = document.createElement(
        'scion-chat-interagent-marker'
      ) as ScionChatInteragentMarker;
      el.messageCount = 1;
      el.messages = [makeMessage({ createdAt: '2026-09-23T15:00:00Z' })]; // -> 2026-09-24T00:00 JST
      el.expanded = true;
      document.body.appendChild(el);
      await el.updateComplete;

      const time = el.shadowRoot?.querySelector('.ia-time');
      expect(time?.textContent).toBe('00:00');
      expect(time?.getAttribute('title')).toBe('Sep 24, 2026, 00:00 (Asia/Tokyo)');
    } finally {
      setPreferredTimeZone('');
    }
  });

  // Review R2-1/R3-3: the marker's DisplayZoneController re-renders the
  // expanded time when the preference changes after mount.
  it('re-renders in the new zone after a mounted marker outlives a preference change', async () => {
    try {
      const el = document.createElement(
        'scion-chat-interagent-marker'
      ) as ScionChatInteragentMarker;
      el.messageCount = 1;
      el.messages = [makeMessage({ createdAt: '2026-09-23T15:00:00Z' })];
      el.expanded = true;
      document.body.appendChild(el);
      await el.updateComplete;

      expect(el.shadowRoot?.querySelector('.ia-time')?.textContent).toBe('15:00'); // UTC (Auto)

      setPreferredTimeZone('Asia/Tokyo');
      await el.updateComplete;

      const time = el.shadowRoot?.querySelector('.ia-time');
      expect(time?.textContent).toBe('00:00');
      expect(time?.getAttribute('title')).toBe('Sep 24, 2026, 00:00 (Asia/Tokyo)');
    } finally {
      setPreferredTimeZone('');
    }
  });
});
