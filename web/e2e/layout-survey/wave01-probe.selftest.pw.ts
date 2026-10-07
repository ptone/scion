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

/**
 * LOCAL NON-EVIDENCE self-test of the in-page probe against a synthetic
 * shadow/slot DOM (page.setContent; no Hub, no network, no route mocks).
 * Proves the raw walk the evaluators rely on: K(e) across slot + shadow
 * boundaries, policy matching by host, POL-SR detection, deep hit tests
 * under an overlay, deep activeElement for native Tab focus, wheel
 * positioning on a named scroller, and that measuring never moves focus
 * or scroll. Run: npx playwright test -c e2e/layout-survey/playwright.wave01-selftest.config.ts
 */

import { test, expect, type Page } from '@playwright/test';
import { ACTIONABLE, POLICIES } from './wave01/contract.js';
import { clip, clipAndHit, evalAC1, pressChecks } from './wave01/evaluate.js';
import { probe, type MeasureResult, type ProbeRequest, type RawElement } from './wave01/probe.js';

const HTML = `<!doctype html><html><head><style>
  html,body{margin:0;height:100%;font:14px sans-serif}
  #cover{position:fixed;left:0;top:700px;width:390px;height:80px;background:rgba(0,0,0,.5)}
</style></head><body>
<scion-app>
  <div data-scion-page>
    <table aria-label="Groups"><caption class="sr-only">Hidden caption with a long sentence</caption>
      <tr><td><a class="group-name-link" href="/g/1">First</a></td></tr></table>
    <fake-button id="create-group-btn"></fake-button>
    <a class="far" href="/far" style="display:block;margin-top:1500px">Far link</a>
    <button id="covered" style="position:absolute;top:720px;left:10px">Covered</button>
    <div class="wide-clip" style="width:200px;overflow:hidden"><div style="width:600px">wide</div></div>
  </div>
</scion-app>
<div id="cover"></div>
<script>
  customElements.define('scion-app', class extends HTMLElement {
    constructor(){super();const r=this.attachShadow({mode:'open'});
      r.innerHTML='<style>.content{height:600px;overflow:auto;padding:8px}.sr-only{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap}</style><header><button class="mobile-menu-btn">Menu</button></header><div class="content"><slot></slot></div>';}
  });
  customElements.define('fake-button', class extends HTMLElement {
    constructor(){super();const r=this.attachShadow({mode:'open'});
      r.innerHTML='<style>button:focus-visible{outline:2px solid blue}</style><button part="base">Create group</button>';}
  });
  const s=document.createElement('style');s.textContent='.sr-only{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap}';document.head.appendChild(s);
</script></body></html>`;

const req = (r: ProbeRequest) => r;
const run = <T>(page: Page, r: ProbeRequest) => page.evaluate(probe, r) as Promise<T>;
const measure = (
  page: Page,
  queries: Extract<ProbeRequest, { op: 'measure' }>['queries'],
  overflowScan = false
) =>
  run<MeasureResult>(
    page,
    req({
      op: 'measure',
      queries,
      overflowScan,
      policies: POLICIES,
      policyContext: ['W01-S01'],
      actionable: ACTIONABLE,
    })
  );

test.use({ viewport: { width: 390, height: 844 } });

test('probe walks slot + shadow chain, matches policies by host, detects POL-SR', async ({
  page,
}) => {
  await page.setContent(HTML);
  const m = await measure(
    page,
    [
      { key: 'far', op: 'one', css: 'a.far' },
      { key: 'link', op: 'one', css: 'a.group-name-link' },
      { key: 'caption', op: 'one', css: 'caption' },
    ],
    true
  );
  const far = m.elements.far![0]!;
  const content = far.chain.find((c) => c.path.includes('div.content'));
  expect(content, 'K(e) crosses the slot into the shell shadow root').toBeTruthy();
  expect(content!.policies).toContain('POL-V-APP-CONTENT');
  expect(far.chain[far.chain.length - 1]!.viewport).toBe(true);
  expect(clip(far).status, 'offscreen inside the declared .content scroller is pending').toBe(
    'pending'
  );
  expect(clipAndHit(m.elements.link![0]!, 390).status).toBe('pass');
  expect(m.elements.caption![0]!.srOnly).toBe(true);
  const ac1 = evalAC1(m.overflow);
  const failing = (ac1.details as { failing: Array<{ path: string }> }).failing.map((f) => f.path);
  expect(
    failing.some((p) => p.includes('wide-clip')),
    'deliberate overflow negative control is caught'
  ).toBe(true);
  expect(
    failing.some((p) => p.includes('caption')),
    'POL-SR caption is exempt (rev 2)'
  ).toBe(false);
});

test('deep hit test fails under an overlay; probe does not move focus or scroll', async ({
  page,
}) => {
  await page.setContent(HTML);
  const before = await page.evaluate(() => ({
    a: document.activeElement?.localName,
    y: document.scrollingElement!.scrollTop,
  }));
  const m = await measure(page, [{ key: 'covered', op: 'one', css: '#covered' }]);
  const covered = m.elements.covered![0]! as RawElement;
  expect(covered.hit?.inViewport).toBe(true);
  expect(covered.hit?.ok, 'overlay intercepts the centre').toBe(false);
  const after = await page.evaluate(() => ({
    a: document.activeElement?.localName,
    y: document.scrollingElement!.scrollTop,
  }));
  expect(after).toEqual(before);
});

test('native Tab focus resolves to the deep element inside a shadow-hosted target', async ({
  page,
}) => {
  await page.setContent(HTML);
  const host = await measure(page, [{ key: 'h', op: 'one', css: '#create-group-btn' }]);
  const hostPath = host.elements.h![0]!.path;
  const baseline = await run<Record<string, Record<string, string>>>(
    page,
    req({ op: 'focus-baseline' })
  );
  let reached = false;
  for (let i = 0; i < 10 && !reached; i++) {
    await page.keyboard.press('Tab');
    const a = await run<{
      outside: boolean;
      element: RawElement | null;
      innerWidth: number;
      innerHeight: number;
    }>(page, req({ op: 'active', policies: POLICIES, policyContext: [], actionable: ACTIONABLE }));
    if (a.element && a.element.path.startsWith(hostPath + '>')) {
      reached = true;
      expect(a.element.tag).toBe('button');
      const c = pressChecks(
        {
          direction: 'forward',
          press: i + 1,
          outside: false,
          innerWidth: a.innerWidth,
          innerHeight: a.innerHeight,
          element: a.element,
          reached: [],
        },
        baseline
      );
      expect(c.indicator, 'focus-visible outline detected').toBe(true);
      expect(c.hit).toBe(true);
    }
  }
  expect(reached).toBe(true);
});

test('wheel on the named .content scroller moves it (positioning evidence)', async ({ page }) => {
  await page.setContent(HTML);
  const s0 = await run<Record<string, { scrollTop: number; box: RawElement['box'] } | null>>(
    page,
    req({ op: 'scrollers', names: ['.content', 'document'], policies: POLICIES, policyContext: [] })
  );
  expect(s0['.content']).toBeTruthy();
  const b = s0['.content']!.box;
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
  await page.mouse.wheel(0, 400);
  await page.waitForFunction(() => true);
  await page.evaluate(
    () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))
  );
  const s1 = await run<Record<string, { scrollTop: number } | null>>(
    page,
    req({ op: 'scrollers', names: ['.content'], policies: POLICIES, policyContext: [] })
  );
  expect(s1['.content']!.scrollTop).toBeGreaterThan(0);
});

test('nav op and element-bound path op', async ({ page }) => {
  await page.setContent(HTML);
  const loc = page.locator('a.group-name-link');
  const p = await loc.evaluate(
    probe as (el: Element, r: ProbeRequest) => unknown,
    req({ op: 'path' })
  );
  const m = await measure(page, [{ key: 'l', op: 'one', css: 'a.group-name-link' }]);
  expect(p).toBe(m.elements.l![0]!.path);
});
