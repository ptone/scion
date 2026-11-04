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
 * A graph card opens the agent page (ptone/scion#6542), with web.agent_badges
 * off and on, when the click lands on the name, the body, the status or the
 * badge mark.
 *
 * A press that moves a few pixels before release used to start a native
 * drag, of the card link (any browser) or of the badge's vendor mark image
 * (WebKit, after about 5 px), and the browser then fires no click. The fix
 * makes neither draggable. happy-dom has no native drag, so the drag half
 * is asserted on the draggable attributes the browser acts on; the click
 * half runs the press, nudge, release and click sequence through the
 * component and the router's own anchor lookup.
 */

import { describe, it, expect, beforeAll, afterEach, vi } from 'vitest';
import './agent-tree-view.js';
import type { ScionAgentTreeView } from './agent-tree-view.js';
import type { ScionAgentBadges } from './agent-badges.js';
import type { Agent } from '../../shared/types.js';

beforeAll(() => {
  const store = new Map<string, string>();
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  });
});

function agent(id: string, overrides: Partial<Agent> = {}): Agent {
  return {
    id,
    name: id,
    projectId: 'p1',
    template: 't',
    phase: 'running',
    ancestry: ['user-1'],
    harnessVendor: 'anthropic',
    modelSize: 'medium',
    ...overrides,
  } as Agent;
}

async function mount(agents: Agent[]): Promise<ScionAgentTreeView> {
  const el = document.createElement('scion-agent-tree-view');
  el.agents = agents;
  document.body.appendChild(el);
  await el.updateComplete;
  const badges = el.shadowRoot!.querySelectorAll<ScionAgentBadges>('scion-agent-badges');
  await Promise.all([...badges].map((b) => b.updateComplete));
  return el;
}

function card(el: ScionAgentTreeView, id: string): HTMLAnchorElement {
  return el.shadowRoot!.querySelector<HTMLAnchorElement>(`a.node[data-agent-id="${id}"]`)!;
}

function badgeMark(el: ScionAgentTreeView, id: string): HTMLImageElement | null {
  const badges = card(el, id).querySelector('scion-agent-badges');
  return badges?.shadowRoot?.querySelector<HTMLImageElement>('img.mark') ?? null;
}

/**
 * The link a document-level click resolves to, found the way setupRouter's
 * onLinkClick in src/client/main.ts finds it (the first anchor on the
 * composed path), and whether something cancelled the click first.
 */
function captureRouterClick(): { seen: { href: string | null; prevented: boolean } | null } {
  const capture: { seen: { href: string | null; prevented: boolean } | null } = { seen: null };
  const onClick = (e: MouseEvent): void => {
    const anchor = e.composedPath().find((n) => n instanceof HTMLAnchorElement) as
      | HTMLAnchorElement
      | undefined;
    capture.seen = { href: anchor?.getAttribute('href') ?? null, prevented: e.defaultPrevented };
    e.preventDefault();
  };
  document.addEventListener('click', onClick, { once: true });
  return capture;
}

/** Press, nudge 6 px, release and click on `target`, as a slightly moving hand does. */
function nudgeClick(el: ScionAgentTreeView, target: Element): void {
  const canvas = el.shadowRoot!.querySelector('.canvas')!;
  const init = { bubbles: true, composed: true, cancelable: true, pointerId: 1, button: 0 };
  target.dispatchEvent(new PointerEvent('pointerdown', { ...init, clientX: 50, clientY: 20 }));
  canvas.dispatchEvent(new PointerEvent('pointermove', { ...init, clientX: 56, clientY: 21 }));
  target.dispatchEvent(new PointerEvent('pointerup', { ...init, clientX: 56, clientY: 21 }));
  target.dispatchEvent(new MouseEvent('click', { ...init, clientX: 56, clientY: 21 }));
}

describe.each([
  ['off', false],
  ['on', true],
])('graph card click with agent badges %s (ptone/scion#6542)', (_label, badgesOn) => {
  afterEach(() => {
    document.body.innerHTML = '';
    delete window.__SCION_FEATURES__;
  });

  async function mountOne(): Promise<ScionAgentTreeView> {
    window.__SCION_FEATURES__ = { 'web.agent_badges': badgesOn };
    return mount([agent('a1')]);
  }

  it('renders the card link as not draggable, so a nudged press still clicks', async () => {
    const el = await mountOne();
    expect(card(el, 'a1').getAttribute('draggable')).toBe('false');
  });

  if (badgesOn) {
    it('renders the badge vendor mark as not draggable', async () => {
      const el = await mountOne();
      const mark = badgeMark(el, 'a1');
      expect(mark).not.toBeNull();
      expect(mark!.getAttribute('draggable')).toBe('false');
    });
  }

  const targets: [string, (el: ScionAgentTreeView) => Element | null][] = [
    ['name', (el): Element | null => card(el, 'a1').querySelector('.name')],
    ['body', (el): Element | null => card(el, 'a1').querySelector('.meta')],
    ['status', (el): Element | null => card(el, 'a1').querySelector('scion-status-badge')],
    ['card', (el): Element | null => card(el, 'a1')],
  ];
  if (badgesOn) targets.push(['badge mark', (el): Element | null => badgeMark(el, 'a1')]);

  for (const [where, find] of targets) {
    it(`opens the agent page on a nudged click on the ${where}`, async () => {
      const el = await mountOne();
      const target = find(el);
      expect(target).not.toBeNull();
      const capture = captureRouterClick();

      nudgeClick(el, target!);

      expect(capture.seen).toEqual({ href: '/agents/a1', prevented: false });
      // The press began on the card, so the graph did not start a pan.
      expect(el.shadowRoot!.querySelector('.canvas')!.classList.contains('dragging')).toBe(false);
    });
  }
});
