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
 * Tests for the Created and Updated stats in the broker-detail header. The hub
 * serializes store.RuntimeBroker timestamps as `created` / `updated`
 * (ptone/scion#3344).
 */

import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';

import type { PageData } from '../../shared/types.js';

/** happy-dom has no EventSource; setScope opens one. */
class FakeEventSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = FakeEventSource.CONNECTING;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  constructor(readonly url: string) {
    super();
  }
  close(): void {
    this.readyState = FakeEventSource.CLOSED;
  }
}

const BROKER_ID = 'b-dates';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

async function renderBroker(broker: Record<string, unknown>): Promise<HTMLElement> {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string | URL | Request): Promise<Response> => {
      const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
      if (path.endsWith(`/api/v1/runtime-brokers/${BROKER_ID}`)) {
        return Promise.resolve(
          jsonResponse({
            id: BROKER_ID,
            name: 'dated-broker',
            slug: 'dated-broker',
            version: '1.0.0',
            status: 'online',
            connectionState: 'connected',
            lastHeartbeat: '2026-09-30T12:00:00Z',
            autoProvide: false,
            ...broker,
          })
        );
      }
      if (path.endsWith(`/api/v1/runtime-brokers/${BROKER_ID}/projects`)) {
        return Promise.resolve(jsonResponse({ projects: [] }));
      }
      if (path.includes('/api/v1/agents')) {
        return Promise.resolve(jsonResponse({ agents: [] }));
      }
      return Promise.resolve(jsonResponse({}, 404));
    })
  );
  const el = document.createElement('scion-page-broker-detail') as HTMLElement & {
    updateComplete: Promise<boolean>;
    pageData: PageData | null;
    brokerId: string;
  };
  el.brokerId = BROKER_ID;
  el.pageData = {
    path: `/brokers/${BROKER_ID}`,
    title: 'Broker',
    user: { id: 'u', email: 'u@example.com', name: 'U', role: 'member' },
  };
  document.body.appendChild(el);
  await el.updateComplete;
  await new Promise((resolve) => setTimeout(resolve, 50));
  await el.updateComplete;
  return el;
}

/** Text of the stat value under the given label. */
function statValue(el: HTMLElement, label: string): string {
  const stats = Array.from(el.shadowRoot?.querySelectorAll('.stat') ?? []);
  const stat = stats.find((s) => s.querySelector('.stat-label')?.textContent?.trim() === label);
  return stat?.querySelector('.stat-value, .stat-value-sm')?.textContent?.trim() ?? '';
}

describe('scion-page-broker-detail — Created and Updated stats', () => {
  let element: HTMLElement | null = null;

  beforeAll(async () => {
    await import('./broker-detail.js');
  }, 60_000);

  beforeEach(() => {
    vi.stubGlobal('EventSource', FakeEventSource);
  });

  afterEach(() => {
    element?.remove();
    element = null;
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('shows the date the hub sends as created', async () => {
    element = await renderBroker({
      created: '2026-08-14T12:00:00Z',
      updated: '2026-09-28T12:00:00Z',
    });
    expect(statValue(element, 'Created')).toMatch(/Aug 14, 2026/);
  });

  it('shows the date the hub sends as updated', async () => {
    element = await renderBroker({
      created: '2026-08-14T12:00:00Z',
      updated: '2026-09-28T12:00:00Z',
    });
    expect(statValue(element, 'Updated')).toMatch(/Sep 28, 2026/);
  });

  it('shows a dash for the Go zero time', async () => {
    element = await renderBroker({
      created: '0001-01-01T00:00:00Z',
      updated: '0001-01-01T00:00:00Z',
    });
    expect(statValue(element, 'Created')).toBe('—');
    expect(statValue(element, 'Updated')).toBe('—');
  });

  it('shows a dash instead of a blank when the date is missing or invalid', async () => {
    element = await renderBroker({ created: 'not-a-date' });
    expect(statValue(element, 'Created')).toBe('—');
  });
});
