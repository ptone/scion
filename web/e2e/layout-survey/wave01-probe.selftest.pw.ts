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
import {
  clip,
  clipAndHit,
  evalAC1,
  evalAN2,
  pressChecks,
  renderedTextEquals,
} from './wave01/evaluate.js';
import { probe, type MeasureResult, type ProbeRequest, type RawElement } from './wave01/probe.js';
import { navRaw, sampleFocusBaseline, twoFrames, type SubstepCtx } from './wave01/runner-lib.js';

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
    'POL-SR caption is exempt (rev 2+)'
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
  const baseline = await sampleFocusBaseline(page);
  let reached = false;
  for (let i = 0; i < 10 && !reached; i++) {
    await page.keyboard.press('Tab');
    await twoFrames(page);
    const a = await run<{
      outside: boolean;
      element: RawElement | null;
      innerWidth: number;
      innerHeight: number;
      runningAnimations?: Array<{ path: string; kind: string; name: string | null }>;
      focusNodes?: Record<string, Record<string, string>>;
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
          runningAnimations: a.runningAnimations ?? [],
          focusNodes: a.focusNodes ?? {},
        },
        baseline
      );
      expect(c.indicator, `focus-visible outline appears (${JSON.stringify(c)})`).toBe('present');
      expect(c.sampledNodes.some((k) => k.endsWith('::after'))).toBe(true);
      expect(c.sampledNodes.some((k) => k === hostPath)).toBe(true);
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

// ─── Real-Chromium controls for review1 B1, B5 (rev 3) and N4 ─────────────

const HTML2 = `<!doctype html><html><head><style>
  html,body{margin:0;font:14px sans-serif}
  body>a{display:inline-block;margin:2px}
  #bg:focus-visible{outline:none;background:rgb(255,255,0)}
  #fw:focus-visible{outline:none;font-weight:700}
  #blw{border-left:0 solid black}
  #blw:focus-visible{outline:none;border-left-width:4px}
  @keyframes pulse{from{color:rgb(0,0,0)}to{color:rgb(0,0,255)}}
  #anim{outline:none;animation:pulse 0.7s infinite alternate}
  #animfw{outline:none;animation:pulse 0.7s infinite alternate}
  #animfw:focus-visible{font-weight:700}
  @keyframes step{0%{color:rgb(0,0,0)}50%{color:rgb(0,0,255)}}
  #step{outline:none;animation:step 2s steps(1) infinite}
  @keyframes slow{from{color:rgb(0,0,0)}to{color:rgb(0,0,255)}}
  #slow{outline:none;animation:slow 60s linear infinite}
  #ring:focus-visible{outline:2px solid rgb(0,0,255)}
  #shadow:focus-visible{outline:none;box-shadow:0 0 0 3px rgb(0,0,255)}
  #permring{outline:2px solid rgb(0,128,0)}
  #permshadow{outline:none;box-shadow:0 0 0 1px rgb(204,204,204)}
  #fwwrap{display:inline-block}
  #fwwrap:focus-within{outline:3px solid rgb(0,128,0)}
  #fwl{outline:none}
  #under{outline:none;position:relative}
  #under::after{content:'';position:absolute;left:0;right:0;bottom:-2px;height:2px;background:transparent}
  #under:focus-visible::after{background:rgb(0,0,255)}
  #rmring{outline:2px solid rgb(0,128,0)}
  #rmring:focus-visible{outline:none}
  @keyframes pstep{0%{opacity:1}50%{opacity:0}}
  #pseudoanim{position:relative}
  #pseudoanim::after{content:'';display:inline-block;width:4px;height:4px;background:red;animation:pstep 2s steps(1) infinite}
  #pseudoanim:focus-visible{outline:2px solid rgb(0,0,255)}
  @keyframes dspin{from{transform:rotate(0deg)}to{transform:rotate(360deg)}}
  #descanim .spin{display:inline-block;animation:dspin 20s linear infinite}
  #descanim:focus-visible{outline:2px solid rgb(0,0,255)}
  #pbt{position:relative}
  #pbt::before{content:'';display:inline-block;width:4px;height:4px;background:rgb(255,255,255);transition:background-color 5s linear}
  #pbt:focus-visible::before{background:rgb(255,0,0)}
  #pbt:focus-visible{outline:2px solid rgb(0,0,255)}
  #apwrap{display:inline-block;position:relative}
  #apwrap::after{content:'';display:inline-block;width:4px;height:4px;background:red;animation:pstep 2s steps(1) infinite}
  #apl:focus-visible{outline:2px solid rgb(0,0,255)}
</style></head><body>
<a id="noring" href="#a" style="outline:none">No ring</a>
<a id="zero" href="#b" style="outline:0">Zero outline</a>
<a id="bg" href="#c">Background indicator</a>
<a id="fw" href="#d">Font-weight indicator</a>
<a id="blw" href="#e">Border-left indicator</a>
<a id="anim" href="#f">Animated, no focus style</a>
<a id="animfw" href="#g">Animated, stable font-weight focus style</a>
<a id="step" href="#h">Stepped animation, no focus style</a>
<a id="slow" href="#i">Slow animation, no focus style</a>
<a id="ring" href="#j">Outline focus style</a>
<a id="shadow" href="#k">Box-shadow focus style</a>
<a id="permring" href="#l">Permanent outline</a>
<a id="permshadow" href="#m">Permanent box-shadow</a>
<span id="fwwrap"><a id="fwl" href="#n">Focus-within wrapper indicator</a></span>
<a id="under" href="#o">::after focus underline</a>
<a id="rmring" href="#p">Outline removed on focus</a>
<a id="pseudoanim" href="#q">Animated ::after + ring</a>
<a id="descanim" href="#r"><span class="spin">*</span> Animated descendant + ring</a>
<a id="pbt" href="#s">::before transition on focus + ring</a>
<span id="apwrap"><a id="apl" href="#t">Ring inside a wrapper with an animated ::after</a></span>
<span id="ws-normal" style="white-space:normal">Double  Space
  wrapped</span>
<span id="ws-pre" style="white-space:pre-wrap">A  B</span>
<span id="ws-nbsp" style="white-space:normal">A&nbsp;B&#x3000;C</span>
<scion-app></scion-app>
<script>
  customElements.define('scion-nav', class extends HTMLElement {
    constructor(){super();const r=this.attachShadow({mode:'open'});
      r.innerHTML='<nav class="nav-container">'
        +'<sl-tooltip content="Projects"><a class="nav-link" href="/projects"><span aria-hidden="true">*</span><span class="nav-link-text">Projects</span></a></sl-tooltip>'
        +'<sl-tooltip content="Say \\u0022hi\\u0022 \\\\ there"><a class="nav-link" href="/q"><span class="nav-link-text">Say "hi" \\\\ there</span></a></sl-tooltip>'
        +'<sl-tooltip content="Groups"><a class="nav-link" href="/g" aria-label="Override"><span class="nav-link-text">Groups</span></a></sl-tooltip>'
        +'<a class="nav-link" href="/hidden" style="display:none"><span class="nav-link-text">Hidden</span></a>'
        +'</nav>';}
  });
  customElements.define('scion-app', class extends HTMLElement {
    constructor(){super();const r=this.attachShadow({mode:'open'});
      r.innerHTML='<aside class="sidebar"><scion-nav></scion-nav></aside>';}
  });
</script></body></html>`;

async function tabTo(page: Page, id: string) {
  const baseline = await sampleFocusBaseline(page);
  for (let i = 0; i < 40; i++) {
    await page.keyboard.press('Tab');
    await twoFrames(page); // same post-Tab settle as the runner (review5 O-3)
    const a = await run<{
      outside: boolean;
      element: RawElement | null;
      innerWidth: number;
      innerHeight: number;
      runningAnimations?: Array<{ path: string; kind: string; name: string | null }>;
      focusNodes?: Record<string, Record<string, string>>;
    }>(page, req({ op: 'active', policies: POLICIES, policyContext: [], actionable: ACTIONABLE }));
    if (a.element?.idAttr === id) {
      const c = pressChecks(
        {
          direction: 'forward',
          press: i + 1,
          outside: false,
          innerWidth: a.innerWidth,
          innerHeight: a.innerHeight,
          element: a.element,
          reached: [],
          runningAnimations: a.runningAnimations ?? [],
          focusNodes: a.focusNodes ?? {},
        },
        baseline
      );
      return { ...c, path: a.element.path, elDiffU2F: c.nodeDiffsU2F?.[a.element.path] ?? [] };
    }
  }
  throw new Error(`never focused #${id}`);
}

test('B1 negative control: static outline:none / outline:0 with no change on any sampled node ⇒ FAIL (rev 8 / R-16)', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const none = await tabTo(page, 'noring');
  expect(none, JSON.stringify(none)).toMatchObject({
    indicator: 'absent',
    decidedBy: 'static-no-indicator',
    result: 'fail',
    runningAnimations: [],
  });
  const zero = await tabTo(page, 'zero');
  expect(zero, JSON.stringify(zero)).toMatchObject({ indicator: 'absent', result: 'fail' });
});

test('rev 8: a focus ring or box-shadow that APPEARS on focus ⇒ PASS', async ({ page }) => {
  await page.setContent(HTML2);
  const ring = await tabTo(page, 'ring');
  expect(ring, JSON.stringify(ring)).toMatchObject({
    decidedBy: 'outline-appears',
    result: 'pass',
  });
  expect(ring.indicatorValuesU2).toMatchObject({ 'outline-style': 'none' });
  expect(ring.indicatorValuesF).toMatchObject({ 'outline-style': 'solid', 'outline-width': '2px' });
  await page.setContent(HTML2);
  const shadow = await tabTo(page, 'shadow');
  expect(shadow, JSON.stringify(shadow)).toMatchObject({
    decidedBy: 'box-shadow-appears',
    result: 'pass',
  });
});

test('rev 8 / R-16: a permanent outline or box-shadow unchanged on focus ⇒ INCONCLUSIVE (never PASS or FAIL)', async ({
  page,
}) => {
  for (const id of ['permring', 'permshadow']) {
    await page.setContent(HTML2);
    const c = await tabTo(page, id);
    expect(c, JSON.stringify(c)).toMatchObject({ result: 'inconclusive', indicatorBy: null });
    expect(c.undeterminableReasons).toContain(
      'outline or box-shadow present in both U2 and F (persistent)'
    );
  }
});

test('rev 8: a :focus-within wrapper outline (element static) ⇒ INCONCLUSIVE, change recorded on the ancestor', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const c = await tabTo(page, 'fwl');
  expect(c, JSON.stringify(c)).toMatchObject({ result: 'inconclusive' });
  const anc = Object.keys(c.nodeDiffsU2F ?? {}).filter((k) => k !== c.path);
  expect(anc.some((k) => k.includes('#fwwrap'))).toBe(true);
});

test('rev 8: an ::after focus underline (element static) ⇒ INCONCLUSIVE, change recorded on ::after', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const c = await tabTo(page, 'under');
  expect(c, JSON.stringify(c)).toMatchObject({ result: 'inconclusive' });
  expect(c.nodeDiffsU2F?.[`${c.path}::after`]).toContain('background-color');
});

test('rev 8: no baseline (element inserted after U1/U2) + a ring on F ⇒ INCONCLUSIVE', async ({
  page,
}) => {
  await page.setContent(
    '<!doctype html><style>a{display:inline-block}#late:focus-visible{outline:2px solid blue}</style><div id="w"></div>'
  );
  const baseline = await sampleFocusBaseline(page);
  await page.evaluate(() => {
    const a = document.createElement('a');
    a.id = 'late';
    a.href = '#l';
    a.textContent = 'late';
    document.getElementById('w')!.appendChild(a);
  });
  await page.keyboard.press('Tab');
  await twoFrames(page);
  const a = await run<{
    outside: boolean;
    element: RawElement | null;
    innerWidth: number;
    innerHeight: number;
    runningAnimations?: Array<{ path: string; kind: string; name: string | null }>;
    focusNodes?: Record<string, Record<string, string>>;
  }>(page, req({ op: 'active', policies: POLICIES, policyContext: [], actionable: ACTIONABLE }));
  expect(a.element?.idAttr).toBe('late');
  const c = pressChecks(
    {
      direction: 'forward',
      press: 1,
      outside: false,
      innerWidth: a.innerWidth,
      innerHeight: a.innerHeight,
      element: a.element,
      reached: [],
      runningAnimations: a.runningAnimations ?? [],
      focusNodes: a.focusNodes ?? {},
    },
    baseline
  );
  expect(c, JSON.stringify(c)).toMatchObject({ result: 'inconclusive', baselineMissing: true });
  expect(c.indicatorValuesF).toMatchObject({ 'outline-style': 'solid' });
});

test('rev 8: a stable background focus change ⇒ INCONCLUSIVE (candidate indicator, never PASS)', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const bg = await tabTo(page, 'bg');
  expect(bg, JSON.stringify(bg)).toMatchObject({
    indicator: 'undeterminable',
    result: 'inconclusive',
    indicatorBy: null,
  });
  expect(bg.elDiffU2F).toContain('background-color');
  // Per-node names are recorded raw (outline-* included, e.g. the UA
  // outline-offset); the only non-outline element change is the background.
  expect(bg.elDiffU2F.filter((k) => !k.startsWith('outline'))).toEqual(['background-color']);
});

test('rev 3 rendered-text rule against real innerText and computed white-space', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const m = await measure(page, [
    { key: 'n', op: 'one', css: '#ws-normal' },
    { key: 'p', op: 'one', css: '#ws-pre' },
  ]);
  const n = m.elements.n![0]!;
  const p = m.elements.p![0]!;
  expect(n.whiteSpace).toBe('normal');
  expect(renderedTextEquals(n.innerText, 'Double  Space\n  wrapped', n.whiteSpace).equal).toBe(
    true
  );
  expect(renderedTextEquals(n.innerText, 'Double Spaced wrapped', n.whiteSpace).equal).toBe(false);
  expect(p.whiteSpace).toBe('pre-wrap');
  expect(renderedTextEquals(p.innerText, 'A  B', p.whiteSpace).equal).toBe(true);
  expect(renderedTextEquals(p.innerText, 'A B', p.whiteSpace).equal).toBe(false);
});

test('N4: nav op + computed accessible names through shadow roots (quotes, backslash, aria-label override)', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const nav = await navRaw({ page, policyContext: [] } as unknown as SubstepCtx);
  const byHref = Object.fromEntries(nav.map((n) => [n.href, n]));
  expect(nav).toHaveLength(4);
  expect(byHref['/projects']).toMatchObject({
    rendered: true,
    labelText: 'Projects',
    tooltipContent: 'Projects',
    accessibleName: 'Projects',
  });
  expect(byHref['/q']).toMatchObject({
    labelText: 'Say "hi" \\ there',
    accessibleName: 'Say "hi" \\ there',
  });
  expect(byHref['/g']).toMatchObject({ labelText: 'Groups', accessibleName: 'Override' });
  expect(byHref['/hidden']).toMatchObject({ rendered: false, accessibleName: null });
  const an2 = evalAN2(nav);
  expect(an2.outcome, 'aria-label override ≠ label ⇒ A-N2 fails').toBe('fail');
  expect((an2.details as { domCount: number }).domCount).toBe(4);
});

test('R-5 real-Chromium control: NBSP and U+3000 stay significant in innerText and in the comparison', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const m = await measure(page, [{ key: 'n', op: 'one', css: '#ws-nbsp' }]);
  const e = m.elements.n![0]!;
  expect(e.innerText).toBe('A\u00a0B\u3000C');
  expect(renderedTextEquals(e.innerText, 'A\u00a0B\u3000C', e.whiteSpace).equal).toBe(true);
  expect(renderedTextEquals(e.innerText, 'A B C', e.whiteSpace).equal).toBe(false);
});

test('O2 (rev 8): stable font-weight / border-left-width focus changes ⇒ INCONCLUSIVE (open coverage)', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const fw = await tabTo(page, 'fw');
  expect(fw, JSON.stringify(fw)).toMatchObject({ result: 'inconclusive', indicatorBy: null });
  expect(fw.elDiffU2F).toContain('font-weight');
  await page.setContent(HTML2);
  const blw = await tabTo(page, 'blw');
  expect(blw, JSON.stringify(blw)).toMatchObject({ result: 'inconclusive', indicatorBy: null });
  expect(blw.elDiffU2F).toContain('border-left-width');
});

// ─── rev 8 A-F1 animation controls (review4 O-b, review5 O-1) ────────────

for (const [id, label] of [
  ['anim', 'review4 O-b: running alternate animation, no focus style'],
  ['step', 'review5 O-1: steps(1) animation, no focus style'],
  ['slow', 'review5 O-1: 60 s linear animation, no focus style'],
  ['animfw', 'animation AND a stable font-weight focus style'],
] as const)
  test(`rev 8 ${label} ⇒ INCONCLUSIVE (never PASS); running animation recorded`, async ({
    page,
  }) => {
    await page.setContent(HTML2);
    const c = await tabTo(page, id);
    expect(c, JSON.stringify(c)).toMatchObject({
      indicator: 'undeterminable',
      result: 'inconclusive',
      indicatorBy: null,
    });
    expect(c.runningAnimations.length).toBeGreaterThan(0);
    expect(c.undeterminableReasons).toContain('animation/transition running at F');
  });

// ─── ruling R-18 controls (assessor 18:36Z) ──────────────────────────────

test('R-18 Q-A: an outline present unfocused and removed on focus ⇒ INCONCLUSIVE (not FAIL)', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const c = await tabTo(page, 'rmring');
  expect(c, JSON.stringify(c)).toMatchObject({ result: 'inconclusive' });
  expect(c.undeterminableReasons).toContain(
    'outline-* change while an outline is present in U2 or F'
  );
});

test("R-18 Q-B: a steps(1) animation on the element's own ::after + an appearing outline ⇒ INCONCLUSIVE, animation recorded", async ({
  page,
}) => {
  await page.setContent(HTML2);
  const c = await tabTo(page, 'pseudoanim');
  expect(c, JSON.stringify(c)).toMatchObject({ result: 'inconclusive' });
  expect(c.runningAnimations.some((x) => x.path.endsWith('::after'))).toBe(true);
});

test('R-18: an animation on a DESCENDANT of the focused element + an appearing outline ⇒ PASS (out of scope)', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const c = await tabTo(page, 'descanim');
  expect(c, JSON.stringify(c)).toMatchObject({ decidedBy: 'outline-appears', result: 'pass' });
  expect(c.runningAnimations).toEqual([]);
});

test('R-18 Q-B: a ::before TRANSITION running at F + an appearing outline ⇒ INCONCLUSIVE, transition recorded', async ({
  page,
}) => {
  await page.setContent(HTML2);
  const c = await tabTo(page, 'pbt');
  expect(c, JSON.stringify(c)).toMatchObject({ result: 'inconclusive' });
  expect(
    c.runningAnimations.some((x) => x.path.endsWith('::before') && x.kind === 'CSSTransition')
  ).toBe(true);
});

test("R-18: an animation on an ANCESTOR's pseudo-element + an appearing outline ⇒ PASS (out of scope)", async ({
  page,
}) => {
  await page.setContent(HTML2);
  const c = await tabTo(page, 'apl');
  expect(c, JSON.stringify(c)).toMatchObject({ decidedBy: 'outline-appears', result: 'pass' });
  expect(c.runningAnimations).toEqual([]);
});

// ─── review8 RB8-1 (R-20 + addendum): script animations, registry union ────
// Real probe → pressChecks with a ring APPEARING on focus: each animated case
// must be INCONCLUSIVE because of the running animation (recorded), and each
// unanimated twin must PASS, so the collector — not some other difference —
// decides.

const HTML3 = `<!doctype html><html><head><style>
  html,body{margin:0;font:14px sans-serif}
  body>a,x-sh,x-slot,x-slot>a{display:inline-block;margin:2px}
  a:focus-visible{outline:2px solid rgb(0,0,255)}
  a::after{content:var(--c,none)}
</style></head><body>
<a id="prebox" href="#1">light pre-box</a>
<a id="recreate" href="#2">light removed/recreated</a>
<a id="lightctl" href="#3">light control</a>
<a id="desc" href="#4"><span id="descspan">*</span> descendant animation</a>
<x-sh id="sh"></x-sh>
<x-slot id="slothost"><a id="slotpre" href="#5">slotted pre-box</a> <a id="slotre" href="#6">slotted removed/recreated</a> <a id="slotctl" href="#7">slotted control</a></x-slot>
<script>
  customElements.define('x-sh', class extends HTMLElement { constructor(){ super();
    this.attachShadow({mode:'open'}).innerHTML =
      '<style>a{display:inline-block;margin:2px} a:focus-visible{outline:2px solid rgb(0,0,255)} a::after{content:var(--c,none)}</style>'
      + '<a id="shpre" href="#8">shadow pre-box</a> <a id="shre" href="#9">shadow removed/recreated</a>'
      + ' <span id="shwrap"><a id="shinner" href="#10">shadow ancestor animated</a></span> <a id="shctl" href="#11">shadow control</a>'; } });
  customElements.define('x-slot', class extends HTMLElement { constructor(){ super();
    this.attachShadow({mode:'open'}).innerHTML = '<span id="sw"><slot></slot></span>'; } });
</script></body></html>`;

const KF = { opacity: [1, 0.99] };
const LONG = { duration: 1e6, iterations: Infinity };

async function setupScriptAnimations(page: Page) {
  await page.evaluate(
    ({ KF, LONG }) => {
      const sh = document.getElementById('sh')!.shadowRoot!;
      const el = (id: string) =>
        (document.getElementById(id) ?? sh.getElementById(id)) as HTMLElement;
      // own ::after animated BEFORE its box exists, then the box is created
      for (const id of ['prebox', 'shpre', 'slotpre']) {
        el(id).animate(KF, { ...LONG, pseudoElement: '::after' });
        el(id).style.setProperty('--c', "'x'");
      }
      // own ::after box created, animated, removed, recreated
      for (const id of ['recreate', 'shre', 'slotre']) {
        el(id).style.setProperty('--c', "'x'");
        el(id).animate(KF, { ...LONG, pseudoElement: '::after' });
        el(id).style.setProperty('--c', 'none');
        el(id).style.setProperty('--c', "'y'");
      }
      // unanimated twins with the same ::after box
      for (const id of ['lightctl', 'shctl', 'slotctl']) el(id).style.setProperty('--c', "'x'");
      // in-scope ancestor inside the shadow root; descendant (out of scope)
      el('shwrap').animate(KF, LONG);
      el('descspan').animate(KF, LONG);
    },
    { KF, LONG }
  );
}

for (const [id, label, suffix] of [
  ['prebox', 'light DOM: own ::after animated before its box exists', '::after'],
  ['recreate', 'light DOM: own ::after box removed and recreated', '::after'],
  ['shpre', 'shadow-internal: own ::after animated before its box exists', '::after'],
  ['shre', 'shadow-internal: own ::after box removed and recreated', '::after'],
  ['shinner', 'shadow-internal: script animation on an in-scope shadow ancestor', '#shwrap'],
  ['slotpre', 'slotted light DOM: own ::after animated before its box exists', '::after'],
  ['slotre', 'slotted light DOM: own ::after box removed and recreated', '::after'],
] as const)
  test(`RB8-1: ${label} + appearing ring ⇒ INCONCLUSIVE, animation recorded once`, async ({
    page,
  }) => {
    await page.setContent(HTML3);
    await setupScriptAnimations(page);
    const c = await tabTo(page, id);
    expect(c, JSON.stringify(c)).toMatchObject({ result: 'inconclusive' });
    expect(c.indicatorValuesU2).toMatchObject({ 'outline-style': 'none' });
    expect(c.indicatorValuesF).toMatchObject({ 'outline-style': 'solid' });
    expect(c.undeterminableReasons).toContain('animation/transition running at F');
    expect(
      c.runningAnimations.some((x) => x.kind === 'Animation' && x.path.includes(suffix)),
      JSON.stringify(c.runningAnimations)
    ).toBe(true);
    // registry ∪ element collectors are deduplicated
    expect(new Set(c.runningAnimations.map((x) => x.path)).size).toBe(c.runningAnimations.length);
  });

for (const [id, label] of [
  ['lightctl', 'light DOM control (same ::after box, no animation)'],
  ['shctl', 'shadow-internal control (no animation on it or its ancestors)'],
  ['slotctl', 'slotted light DOM control (no animation)'],
  ['desc', 'script animation on a DESCENDANT only (out of scope)'],
] as const)
  test(`RB8-1: ${label} + appearing ring ⇒ PASS, nothing recorded`, async ({ page }) => {
    await page.setContent(HTML3);
    await setupScriptAnimations(page);
    const c = await tabTo(page, id);
    expect(c, JSON.stringify(c)).toMatchObject({ decidedBy: 'outline-appears', result: 'pass' });
    expect(c.runningAnimations).toEqual([]);
  });
