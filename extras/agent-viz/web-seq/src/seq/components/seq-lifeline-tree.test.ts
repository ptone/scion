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
 * Tests for <scion-seq-lifeline-tree>. Asserted here:
 *   - the ancestry forest is rebuilt from parentId/ancestry and rendered as
 *     indented rows in depth-first `order`;
 *   - collapsing a row hides its subtree and surfaces a descendant count;
 *   - the chevron dispatches `seq-toggle-collapse` and the eye button
 *     dispatches `seq-solo` (toggling back to null when already soloed);
 *   - clicking a row dispatches `seq-select-lifeline`;
 *   - lifelines absent from `activeIds` render dimmed;
 *   - a lifeline whose parent is missing from the digest is still rendered.
 */

// @vitest-environment happy-dom

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import './seq-lifeline-tree.js';
import type { ScionSeqLifelineTree } from './seq-lifeline-tree.js';
import type { Lifeline } from '../core/types.js';

function lifeline(
  id: string,
  name: string,
  ancestry: string[],
  order: number,
  extra: Partial<Lifeline> = {}
): Lifeline {
  const parentId = ancestry[ancestry.length - 1];
  return {
    id,
    name,
    color: '#3b82f6',
    ancestry,
    depth: ancestry.length,
    order,
    slot: order,
    birthMs: 0,
    deathMs: 1000,
    died: true,
    ...(parentId === undefined ? {} : { parentId }),
    ...extra,
  };
}

/** root → child-a → grandchild; root → child-b */
function forest(): Lifeline[] {
  return [
    lifeline('root', 'root', [], 0),
    lifeline('a', 'child-a', ['root'], 1),
    lifeline('g', 'grandchild', ['root', 'a'], 2),
    lifeline('b', 'child-b', ['root'], 3),
  ];
}

function shadow(el: ScionSeqLifelineTree): ShadowRoot {
  const root = el.shadowRoot;
  if (!root) throw new Error('tree has no shadow root');
  return root;
}

function rows(el: ScionSeqLifelineTree): HTMLElement[] {
  return Array.from(shadow(el).querySelectorAll<HTMLElement>('.row'));
}

function ids(el: ScionSeqLifelineTree): (string | null)[] {
  return rows(el).map((r) => r.getAttribute('data-lifeline-id'));
}

describe('scion-seq-lifeline-tree', () => {
  let el: ScionSeqLifelineTree;

  beforeEach(async () => {
    el = document.createElement('scion-seq-lifeline-tree') as ScionSeqLifelineTree;
    el.lifelines = forest();
    el.activeIds = new Set(['root', 'a', 'g', 'b']);
    document.body.appendChild(el);
    await el.updateComplete;
  });

  afterEach(() => {
    el.remove();
    document.body.innerHTML = '';
  });

  it('renders the ancestry forest depth-first with increasing indentation', () => {
    expect(ids(el)).toEqual(['root', 'a', 'g', 'b']);

    const levels = rows(el).map((r) => r.getAttribute('aria-level'));
    expect(levels).toEqual(['1', '2', '3', '2']);

    // Indentation is applied inline and grows with nesting depth.
    const padding = rows(el).map((r) => parseFloat(r.style.paddingLeft));
    expect(padding).toEqual([0, 0.75, 1.5, 0.75]);
  });

  it('shows a collapse chevron only on rows that have children', () => {
    const chevrons = rows(el).map((r) => !!r.querySelector('.chevron:not(.spacer)'));
    expect(chevrons).toEqual([true, true, false, false]);
  });

  it('dispatches seq-toggle-collapse from the chevron without selecting the row', () => {
    let collapsedId: string | null = null;
    let selected = 0;
    el.addEventListener('seq-toggle-collapse', (e) => {
      collapsedId = (e as CustomEvent<{ lifelineId: string }>).detail.lifelineId;
    });
    el.addEventListener('seq-select-lifeline', () => selected++);

    rows(el)[1]?.querySelector<HTMLButtonElement>('.chevron')?.click();

    expect(collapsedId).toBe('a');
    // The chevron stops propagation: collapsing is not a selection.
    expect(selected).toBe(0);
  });

  it('hides the subtree of a collapsed row and shows its descendant count', async () => {
    el.collapsed = new Set(['a']);
    await el.updateComplete;

    expect(ids(el)).toEqual(['root', 'a', 'b']);
    const badge = rows(el)[1]?.querySelector('.count');
    expect(badge?.textContent?.trim()).toBe('+1');
    expect(rows(el)[1]?.getAttribute('aria-expanded')).toBe('false');

    el.collapsed = new Set(['root']);
    await el.updateComplete;
    expect(ids(el)).toEqual(['root']);
    expect(shadow(el).querySelector('.count')?.textContent?.trim()).toBe('+3');
  });

  it('dispatches seq-solo with the lifeline id, and null when already soloed', async () => {
    const seen: (string | null)[] = [];
    el.addEventListener('seq-solo', (e) => {
      seen.push((e as CustomEvent<{ lifelineId: string | null }>).detail.lifelineId);
    });

    rows(el)[2]?.querySelector<HTMLButtonElement>('.solo-btn')?.click();
    expect(seen).toEqual(['g']);

    el.solo = 'g';
    await el.updateComplete;
    expect(rows(el)[2]?.querySelector('.solo-btn')?.classList.contains('active')).toBe(true);

    rows(el)[2]?.querySelector<HTMLButtonElement>('.solo-btn')?.click();
    expect(seen).toEqual(['g', null]);
  });

  it('offers a clear-solo control only while a lifeline is soloed', async () => {
    expect(shadow(el).querySelector('.clear-solo')).toBeNull();

    el.solo = 'a';
    await el.updateComplete;
    const clear = shadow(el).querySelector<HTMLButtonElement>('.clear-solo');
    expect(clear).toBeTruthy();

    let cleared: string | null | undefined;
    el.addEventListener('seq-solo', (e) => {
      cleared = (e as CustomEvent<{ lifelineId: string | null }>).detail.lifelineId;
    });
    clear?.click();
    expect(cleared).toBeNull();
  });

  it('dispatches seq-select-lifeline when a row is clicked', () => {
    let selectedId: string | null = null;
    el.addEventListener('seq-select-lifeline', (e) => {
      selectedId = (e as CustomEvent<{ lifelineId: string }>).detail.lifelineId;
    });
    rows(el)[3]?.click();
    expect(selectedId).toBe('b');
  });

  it('marks the selected row', async () => {
    el.selectedId = 'g';
    await el.updateComplete;
    const selected = rows(el).filter((r) => r.classList.contains('selected'));
    expect(selected).toHaveLength(1);
    expect(selected[0]?.getAttribute('data-lifeline-id')).toBe('g');
  });

  it('dims lifelines that are absent from activeIds', async () => {
    el.activeIds = new Set(['root', 'g']);
    await el.updateComplete;

    const dimmed = rows(el).map((r) => r.classList.contains('dimmed'));
    expect(dimmed).toEqual([false, true, false, true]);

    // An empty active set dims everything rather than hiding anything.
    el.activeIds = new Set();
    await el.updateComplete;
    expect(rows(el).every((r) => r.classList.contains('dimmed'))).toBe(true);
    expect(rows(el)).toHaveLength(4);
  });

  it('promotes a lifeline whose parent is missing from the digest to a root', async () => {
    el.lifelines = [lifeline('orphan', 'orphan', ['ghost'], 0)];
    await el.updateComplete;
    expect(ids(el)).toEqual(['orphan']);
    expect(rows(el)[0]?.getAttribute('aria-level')).toBe('1');
  });

  it('renders an empty-state message with no lifelines', async () => {
    el.lifelines = [];
    await el.updateComplete;
    expect(rows(el)).toHaveLength(0);
    expect(shadow(el).querySelector('.empty')?.textContent).toContain('No lifelines');
  });
});
