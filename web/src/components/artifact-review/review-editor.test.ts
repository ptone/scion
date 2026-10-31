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

import { afterEach, describe, expect, it } from 'vitest';

import './review-editor.js';
import { REVIEW_CHANGE_DELAY_MS } from './review-editor.js';
import type { ScionReviewEditor } from './review-editor.js';
import type { ReviewController } from './editor/controller.js';

afterEach(() => {
  document.body.replaceChildren();
});

async function mount(content: string): Promise<{ el: ScionReviewEditor; changes: string[] }> {
  const el = document.createElement('scion-review-editor') as ScionReviewEditor;
  el.content = content;
  el.entryPath = 'doc.md';
  const changes: string[] = [];
  el.addEventListener('review-change', (e) =>
    changes.push((e as CustomEvent<{ text: string }>).detail.text)
  );
  document.body.append(el);
  await el.updateComplete;
  await el.ready;
  return { el, changes };
}

const controllerOf = (el: ScionReviewEditor): ReviewController =>
  (el as unknown as { controller: ReviewController }).controller;

const pause = (): Promise<void> => new Promise((r) => setTimeout(r, REVIEW_CHANGE_DELAY_MS + 50));

/** A composition that replaces "dog" (refused: compositions may only insert). */
function refusedComposition(c: ReviewController): void {
  const view = c.view;
  let p = -1;
  view.state.doc.descendants((n, pos) => {
    if (p < 0 && n.isText && n.text!.includes('dog')) p = pos + n.text!.indexOf('dog');
  });
  c.beginComposition();
  const tr = view.state.tr.insertText('犬', p, p + 3);
  tr.setMeta('composition', 1);
  view.dispatch(tr);
  c.endComposition();
  c.settleComposition();
}

describe('scion-review-editor review-change', () => {
  it('is not sent for a refused composition in an unedited document', async () => {
    const { el, changes } = await mount('a dog\n');
    const c = controllerOf(el);
    refusedComposition(c);
    await pause();
    expect(c.dirty).toBe(false);
    expect(changes).toEqual([]);
  });

  it('is sent after an edit (positive control)', async () => {
    const { el, changes } = await mount('a dog\n');
    const c = controllerOf(el);
    const view = c.view;
    view.dispatch(view.state.tr.insertText('big ', 3));
    await pause();
    expect(changes).toEqual(['a {++big ++}dog\n']);
  });
});

describe('scion-review-editor scrollInset', () => {
  type Sides = { top: number; right: number; bottom: number; left: number };
  const props = (el: ScionReviewEditor): { margin: Sides; threshold: Sides } => {
    const view = controllerOf(el).view;
    return {
      margin: view.someProp('scrollMargin') as Sides,
      threshold: view.someProp('scrollThreshold') as Sides,
    };
  };

  it('keeps a caret scrolled into view clear of the pinned toolbar', async () => {
    const { el } = await mount('a dog\n');
    el.scrollInset = 100;
    await el.updateComplete;
    // A caret under the toolbar counts as out of view, and lands below it.
    expect(props(el).margin.top).toBe(108);
    expect(props(el).threshold.top).toBe(108);
    el.scrollInset = 40.4;
    await el.updateComplete;
    expect(props(el).margin.top).toBe(49);
    expect(props(el).margin.bottom).toBe(5);
  });

  it('keeps focused comment badges and links clear of the pinned toolbar', async () => {
    const { el } = await mount('a {==dog==}{>>Why?<<} and [a link](https://example.com)\n');
    el.scrollInset = 100;
    await el.updateComplete;
    // Focus scrolling reads scroll-margin, which reads this height.
    const root = el.shadowRoot!.querySelector<HTMLElement>('.editor-root')!;
    expect(root.style.getPropertyValue('--review-scroll-inset')).toBe('108px');
    const badge = root.querySelector<HTMLElement>('.critic-comment')!;
    expect(badge.getAttribute('tabindex')).toBe('0');
    const css = Array.from(el.shadowRoot!.adoptedStyleSheets ?? [])
      .flatMap((sheet) => Array.from(sheet.cssRules))
      .map((r) => r.cssText)
      .join('\n');
    expect(css).toMatch(
      /\.editor-root \[tabindex\],\s*\.editor-root a\[href\]\s*\{\s*scroll-margin-top: var\(--review-scroll-inset, 0px\);/
    );
    // 0, nothing pinned: today's behaviour, ProseMirror's defaults and no margin.
    el.scrollInset = 0;
    await el.updateComplete;
    expect(root.style.getPropertyValue('--review-scroll-inset')).toBe('');
    expect(props(el).margin).toEqual({ top: 5, right: 5, bottom: 5, left: 5 });
    expect(props(el).threshold).toEqual({ top: 0, right: 0, bottom: 0, left: 0 });
  });

  it('treats a negative, NaN or infinite inset as nothing pinned', async () => {
    const { el } = await mount('a dog\n');
    const root = el.shadowRoot!.querySelector<HTMLElement>('.editor-root')!;
    for (const bad of [-40, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY]) {
      el.scrollInset = 100;
      await el.updateComplete;
      el.scrollInset = bad;
      await el.updateComplete;
      expect(root.style.getPropertyValue('--review-scroll-inset'), String(bad)).toBe('');
      expect(props(el).margin.top, String(bad)).toBe(5);
      expect(props(el).threshold.top, String(bad)).toBe(0);
    }
  });

  it('applies an inset set before the editor started', async () => {
    const el = document.createElement('scion-review-editor') as ScionReviewEditor;
    el.content = 'a dog\n';
    el.entryPath = 'doc.md';
    el.scrollInset = 60;
    document.body.append(el);
    await el.updateComplete;
    await el.ready;
    expect(props(el).margin.top).toBe(68);
  });
});
