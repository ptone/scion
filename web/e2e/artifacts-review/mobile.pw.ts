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
 * The graphical review on a phone (ptone/scion#6929): in a long document
 * the review toolbar stays pinned to the top of the content area, Comment
 * there opens the note dialog, a caret moved under the toolbar is scrolled
 * clear of it, and a refusal hint stays in view with the toolbar.
 *
 * Phone viewports and touch are emulated (Pixel and iPhone), in whichever
 * engine runs the suite. Firefox has no mobile emulation, so it skips.
 * Emulation shows no soft keyboard: how a pinned toolbar behaves with the
 * iOS keyboard open is a device check.
 */

import { devices, expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

import {
  createProject,
  editorText,
  openArtifact,
  publish,
  setExperiments,
  startGraphicalReview,
} from './fixtures.js';

/** A device's viewport, touch and user agent, without its default engine. */
function phone(name: string): Omit<(typeof devices)[string], 'defaultBrowserType'> {
  const { defaultBrowserType: _engine, ...rest } = devices[name];
  return rest;
}

const PARAGRAPHS = 60;
const DOC = [
  '# A long review',
  '',
  ...Array.from({ length: PARAGRAPHS }, (_, i) => [`Paragraph ${i + 1} line.`, '']).flat(),
].join('\n');

const editor = (page: Page): Locator => page.locator('scion-review-editor .ProseMirror');
const toolbar = (page: Page): Locator =>
  page.locator('scion-page-artifact-detail .g-sticky .g-toolbar');
const sticky = (page: Page): Locator => page.locator('scion-page-artifact-detail .g-sticky');
const hint = (page: Page): Locator =>
  page.locator('scion-page-artifact-detail .g-sticky .hint-strip');

/**
 * Scrolls the page's scroll container (the app shell's content area,
 * through the shadow roots) so paragraph n sits under the pinned toolbar.
 * Returns the container's top edge in viewport pixels.
 */
async function scrollUnderToolbar(page: Page, n: number): Promise<number> {
  return page.locator('scion-review-editor').evaluate((host, n) => {
    let scroller: Element | null = null;
    for (let node: Node | null = host; node; ) {
      if (node instanceof Element) {
        const o = getComputedStyle(node).overflowY;
        if ((o === 'auto' || o === 'scroll') && node.scrollHeight > node.clientHeight) {
          scroller = node;
          break;
        }
      }
      // Up the flattened tree: slotted content into its slot, a shadow
      // root to its host.
      node =
        (node instanceof Element ? node.assignedSlot : null) ??
        node.parentNode ??
        (node instanceof ShadowRoot ? node.host : null);
    }
    if (!scroller) throw new Error('no scroll container');
    const p = Array.from(host.shadowRoot!.querySelectorAll('.ProseMirror p')).find(
      (e) => e.textContent === `Paragraph ${n} line.`
    )!;
    const top = scroller.getBoundingClientRect().top;
    scroller.scrollTop += p.getBoundingClientRect().top - top - 4;
    return top;
  }, n);
}

/** The viewport rectangle of text in the editor. */
async function textBox(page: Page, text: string): Promise<{ x: number; y: number; top: number }> {
  const box = await page.locator('scion-review-editor').evaluate((host, t) => {
    const root = host.shadowRoot!.querySelector('.ProseMirror')!;
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const i = n.textContent!.indexOf(t);
      if (i < 0) continue;
      const r = document.createRange();
      r.setStart(n, i);
      r.setEnd(n, i + t.length);
      const b = r.getBoundingClientRect();
      return { x: b.x + 2, y: b.y + b.height / 2, top: b.top };
    }
    return null;
  }, text);
  if (!box) throw new Error(`${text} not in the editor`);
  return box;
}

/** The bottom edge of the pinned block, in viewport pixels. */
async function stickyBottom(page: Page): Promise<number> {
  return sticky(page).evaluate((e) => e.getBoundingClientRect().bottom);
}

let project = '';
const errors: string[] = [];

test.describe.configure({ mode: 'serial' });

test.beforeAll(async () => {
  await setExperiments(true);
  project = await createProject(`e2e-review-mobile-${Date.now().toString(36)}`);
});

test.afterAll(async () => {
  await setExperiments(null);
});

test.beforeEach(({ page, browserName }) => {
  test.skip(browserName === 'firefox', 'Firefox has no mobile emulation');
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() !== 'error') return;
    if (m.text().includes('review editor') || m.location().url.includes('review-editor')) {
      errors.push(`console: ${m.text()}`);
    }
  });
});

test.afterEach(() => {
  const found = errors.splice(0);
  expect(found, 'no page errors or console errors').toEqual([]);
});

for (const device of ['Pixel 7', 'iPhone 13']) {
  test.describe(device, () => {
    test.use(phone(device));

    test('the review toolbar stays pinned while the document scrolls under it', async ({
      page,
    }) => {
      const a = await publish({
        project,
        title: `Mobile ${device}`,
        entry: 'index.md',
        files: { 'index.md': Buffer.from(DOC) },
      });
      await openArtifact(page, project, a.id);
      await startGraphicalReview(page);
      // One toolbar, never a second, pinned copy.
      await expect(page.locator('scion-page-artifact-detail [role="toolbar"]')).toHaveCount(1);
      const comment = toolbar(page).locator('sl-button.comment');
      const startTop = await toolbar(page).evaluate((e) => e.getBoundingClientRect().top);

      // Scrolled well down, the toolbar sits at the top of the content area.
      const scrollerTop = await scrollUnderToolbar(page, 30);
      await expect
        .poll(() => toolbar(page).evaluate((e) => Math.round(e.getBoundingClientRect().top)))
        .toBe(Math.round(scrollerTop));
      expect(startTop).toBeGreaterThan(scrollerTop);
      await expect(editor(page).locator('h1')).not.toBeInViewport();
      await expect(comment).toBeInViewport();
      // A tap target of at least 44px.
      const height = await comment.evaluate((e) => e.getBoundingClientRect().height);
      expect(height).toBeGreaterThanOrEqual(44);

      // Select by touch and keyboard, then tap the pinned Comment.
      const word = await textBox(page, 'Paragraph 34 line.');
      await page.touchscreen.tap(word.x, word.y);
      for (let i = 0; i < 'Paragraph'.length; i++) await page.keyboard.press('Shift+ArrowRight');
      await comment.tap();
      const dialog = page.locator('scion-review-note-dialog sl-dialog');
      await expect(dialog).toHaveAttribute('open', '');
      await expect(page.locator('scion-review-note-dialog .quote')).toHaveText('Paragraph');
      await page.locator('scion-review-note-dialog textarea').fill('Pinned');
      await page.keyboard.press('Enter');
      await expect(dialog.locator('div.dialog[part="base"]')).toBeHidden();
      expect(await editorText(page)).toContain('{==Paragraph==}{>>Pinned<<} 34 line.');

      // A caret moved up under the toolbar is scrolled clear of it.
      await scrollUnderToolbar(page, 40);
      const below = await stickyBottom(page);
      let n = 40;
      while ((await textBox(page, `Paragraph ${n} line.`)).top < below + 2) n++;
      const start = await textBox(page, `Paragraph ${n} line.`);
      await page.touchscreen.tap(start.x, start.y);
      await page.keyboard.press('Home');
      await page.keyboard.press('ArrowUp');
      await page.keyboard.press('ArrowUp');
      await page.keyboard.type('ZQ');
      await expect.poll(() => editorText(page)).toContain(`{++ZQ++}Paragraph ${n - 2} line.`);
      const typed = await textBox(page, 'ZQ');
      expect(typed.top).toBeGreaterThanOrEqual(await stickyBottom(page));

      // A refusal hint shows in the pinned block, in view.
      await page.keyboard.press('End');
      await page.keyboard.press('Enter');
      await expect(hint(page)).toContainText("Paragraph breaks can't be suggested here yet.");
      await expect(hint(page)).toBeInViewport();
      await expect(toolbar(page)).toBeInViewport();
    });
  });
}
