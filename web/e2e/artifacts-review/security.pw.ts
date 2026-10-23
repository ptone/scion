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
 * The graphical review editor renders artifact text in the page, so it
 * must render that text exactly as the rules say, in every engine: raw
 * HTML as literal text, a fixed set of elements and attributes, links and
 * images only to the targets the read-only preview allows, and a paste
 * read from its plain text. Every check is paired with a positive control
 * in the same document, so a blank page cannot pass.
 */

import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';

import {
  DOT_PNG,
  createProject,
  editorText,
  hub,
  openArtifact,
  publish,
  setExperiments,
  startGraphicalReview,
} from './fixtures.js';

const MARKER = 'window.__pwned=1';

/**
 * Request types only the app shell makes (its web font stylesheet and font
 * files); artifact content can request images at most, so an artifact
 * image on a font host still counts.
 */
const SHELL_TYPES = new Set(['font', 'stylesheet']);

const FIXTURE = [
  '# Fixture',
  '',
  `Inline <img src=x onerror="${MARKER}"> and <svg onload="${MARKER}"></svg> beside *emphasis*.`,
  '',
  `<script>${MARKER}</script>`,
  '',
  '<iframe src="https://frame.invalid/"></iframe>',
  '',
  `[js](javascript:${MARKER}) [ok1](https://ok.invalid/1)`,
  '',
  `[case](JaVaScRiPt:${MARKER}) [ok2](https://ok.invalid/2)`,
  '',
  `[entity](&#106;avascript:${MARKER}) [ok3](https://ok.invalid/3)`,
  '',
  `[data](data:text/html,x) [proto](//proto.invalid/x) [tel](tel:+1555)`,
  '',
  `[ref][r] <https://auto.invalid/>`,
  '',
  `<javascript:${MARKER}> [ok4](https://ok.invalid/4)`,
  '',
  `<JaVaScRiPt:${MARKER}> [ok5](https://ok.invalid/5)`,
  '',
  `<data:text/html,x> [ok6](https://ok.invalid/6)`,
  '',
  `[refdata][rd] [refcase][rc] [refent][re] [ok7](https://ok.invalid/7)`,
  '',
  `[ctl1](java&#x09;script:${MARKER}) [ctl2](&#x0A;javascript:${MARKER}) [ok8](https://ok.invalid/8)`,
  '',
  `[r]: javascript:${MARKER}`,
  `[rd]: data:text/html,x`,
  `[rc]: JaVaScRiPt:${MARKER}`,
  `[re]: &#106;avascript:${MARKER}`,
  '',
  '![ext](https://img.invalid/a.png) ![proto](//img.invalid/b.png) ![dot](img/dot.png)',
  '',
  'Sentinels ⸀⸁⸂⸃ stay text, and {++real++} marks stay marks.',
  '',
  // A note can hide references from the parser in the source (raw HTML,
  // indented code); they must stay text in the editor too.
  '{>><b title="<<} &#x2E00;hidden&#x2E01; &#x2E11; ">',
  '',
  'x {>>a',
  '',
  '    <<} &#x2E00;indented&#x2E01;',
  '',
  'References &#x2E00;fake&#x2E01; and &#x2E11; stay text, with {==hl==}{>>first note<<} and {>>second note<<}.',
  '',
].join('\n');

interface Facts {
  badTags: string[];
  onAttrs: string[];
  badHrefs: string[];
  badSrcs: string[];
  em: number;
  okLinks: string[];
  dotLoaded: boolean;
  rawHtml: number;
  ins: number;
  comments: string[];
  text: string;
}

/** Reads the editor's DOM: everything outside the rules, and the positive controls. */
async function facts(page: Page): Promise<Facts> {
  return page.locator('scion-review-editor').evaluate((host) => {
    const root = host.shadowRoot!.querySelector('.ProseMirror')!;
    const allowedTags = new Set(
      'p h1 h2 h3 h4 h5 h6 blockquote pre code hr br ul ol li em strong a del ins mark span sl-icon img table thead tbody tr th td colgroup col sup dl dt dd div'.split(
        ' '
      )
    );
    const out = {
      badTags: [] as string[],
      onAttrs: [] as string[],
      badHrefs: [] as string[],
      badSrcs: [] as string[],
      em: root.querySelectorAll('em').length,
      okLinks: [] as string[],
      dotLoaded: false,
      rawHtml: root.querySelectorAll('code.raw-html').length,
      ins: root.querySelectorAll('ins.critic-ins').length,
      comments: Array.from(root.querySelectorAll('.critic-comment')).map(
        (e) => e.getAttribute('aria-label') ?? ''
      ),
      text: root.textContent ?? '',
    };
    const hubFile = /^\/api\/v1\/artifacts\/[^/]+\/versions\/\d+\/files\/[^?#]+(\?stream=1)?$/;
    for (const el of Array.from(root.querySelectorAll('*'))) {
      const tag = el.tagName.toLowerCase();
      if (!allowedTags.has(tag)) out.badTags.push(tag);
      for (const a of Array.from(el.attributes)) {
        if (a.name.toLowerCase().startsWith('on')) out.onAttrs.push(`${tag}[${a.name}]`);
      }
    }
    for (const a of Array.from(root.querySelectorAll('a[href]'))) {
      const href = a.getAttribute('href')!;
      if (!/^(https?:|mailto:)/.test(href) && !hubFile.test(href)) out.badHrefs.push(href);
      if (/^https:\/\/ok\.invalid\//.test(href)) out.okLinks.push(href);
    }
    for (const img of Array.from(root.querySelectorAll('img:not(.ProseMirror-separator)'))) {
      const src = img.getAttribute('src') ?? '';
      if (!hubFile.test(src)) out.badSrcs.push(src);
      if (img.getAttribute('alt') === 'dot') {
        out.dotLoaded =
          (img as HTMLImageElement).complete && (img as HTMLImageElement).naturalWidth > 0;
      }
    }
    return out;
  });
}

let project = '';

test.describe.configure({ mode: 'serial' });

test.beforeAll(async () => {
  await setExperiments(true);
  project = await createProject(`e2e-review-sec-${Date.now().toString(36)}`);
});

test.afterAll(async () => {
  await setExperiments(null);
});

test('artifact text renders only as the rules allow, and a paste is read as plain text', async ({
  page,
  browserName,
}) => {
  const origin = new URL(hub().baseURL).origin;
  const foreign: string[] = [];
  const dialogs: string[] = [];
  page.on('request', (r) => {
    const u = r.url();
    if (u.startsWith('data:') || u.startsWith('blob:') || u.startsWith('about:')) return;
    const url = new URL(u);
    // The app shell's own web fonts are not artifact content.
    if (SHELL_TYPES.has(r.resourceType())) return;
    if (url.origin !== origin) foreign.push(u);
  });
  page.on('dialog', (d) => {
    dialogs.push(d.type());
    void d.dismiss();
  });
  await page.addInitScript(() => {
    const w = window as unknown as { __violations: string[] };
    w.__violations = [];
    document.addEventListener('securitypolicyviolation', (e) => {
      w.__violations.push(
        `${e.disposition} ${e.effectiveDirective} ${e.sourceFile}:${e.lineNumber}`
      );
    });
  });

  const a = await publish({
    project,
    title: 'Fixture',
    entry: 'index.md',
    files: { 'index.md': Buffer.from(FIXTURE), 'img/dot.png': DOT_PNG },
  });
  await openArtifact(page, project, a.id);
  await startGraphicalReview(page);
  await expect
    .poll(async () => (await facts(page)).dotLoaded, { message: 'the version image loads' })
    .toBe(true);

  const f = await facts(page);
  expect(f.badTags).toEqual([]);
  expect(f.onAttrs).toEqual([]);
  expect(f.badHrefs).toEqual([]);
  expect(f.badSrcs).toEqual([]);
  // Positive controls.
  expect(f.em).toBeGreaterThanOrEqual(1);
  expect(f.okLinks).toEqual([1, 2, 3, 4, 5, 6, 7, 8].map((n) => `https://ok.invalid/${n}`));
  expect(f.rawHtml).toBeGreaterThanOrEqual(3);
  expect(f.ins).toBe(1);
  // Character references of sentinel candidates stay text, and the real
  // comments keep their own notes (positive control in the same line).
  expect(f.comments).toEqual([
    'Comment: <b title="',
    'Comment: a\n\n    ',
    'Comment: first note',
    'Comment: second note',
  ]);
  expect(f.text).toContain('\u2E00fake\u2E01 and \u2E11 stay text');
  expect(f.text).toContain('\u2E00hidden\u2E01 \u2E11');
  expect(f.text).toContain('\u2E00indented\u2E01');
  // The document round-trips unchanged.
  expect(await editorText(page)).toBe(FIXTURE);

  // A paste whose HTML and plain text disagree inserts the plain text.
  await page.locator('scion-review-editor .ProseMirror p', { hasText: 'Sentinels' }).click();
  await page.keyboard.press('End');
  const html = `<img src="https://paste.invalid/x" onerror="${MARKER}"><a href="javascript:${MARKER}">evil</a>`;
  if (browserName === 'firefox') {
    // Firefox hides the data of a script-made paste event, so the paste is
    // a real one: a copy handler puts the two disagreeing readings on the
    // clipboard, then Ctrl+V pastes them.
    await page.evaluate((h) => {
      document.addEventListener(
        'copy',
        (e) => {
          e.clipboardData!.setData('text/html', h);
          e.clipboardData!.setData('text/plain', 'pasted words');
          e.preventDefault();
        },
        { once: true }
      );
    }, html);
    await page.keyboard.press('Control+c');
    await page.keyboard.press('Control+v');
  } else {
    await page.locator('scion-review-editor').evaluate((host, h) => {
      const target = host.shadowRoot!.querySelector('.ProseMirror')!;
      const dt = new DataTransfer();
      dt.setData('text/html', h);
      dt.setData('text/plain', 'pasted words');
      let event: Event;
      try {
        event = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true });
      } catch {
        event = new Event('paste', { bubbles: true, cancelable: true });
      }
      if (!(event as ClipboardEvent).clipboardData) {
        Object.defineProperty(event, 'clipboardData', { value: dt });
      }
      target.dispatchEvent(event);
    }, html);
  }
  await expect.poll(() => editorText(page)).toContain('marks stay marks.{++pasted words++}');
  const after = await facts(page);
  expect(after.onAttrs).toEqual([]);
  expect(after.badHrefs).toEqual([]);
  expect(after.badSrcs).toEqual([]);

  // Nothing ran, nothing opened, nothing left the hub.
  expect(
    await page.evaluate(() => (window as unknown as { __pwned?: unknown }).__pwned)
  ).toBeUndefined();
  expect(dialogs).toEqual([]);
  expect(foreign).toEqual([]);
  const violations = await page.evaluate(
    () => (window as unknown as { __violations: string[] }).__violations
  );
  expect(violations.filter((v) => v.includes('review-editor'))).toEqual([]);
  expect(violations.filter((v) => v.startsWith('enforce'))).toEqual([]);
});
