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

import { describe, expect, it } from 'vitest';
import {
  clip,
  clipAndHit,
  evalAC1,
  evalAD1,
  evalAF1,
  evalAN2,
  evalAS1,
  evalAS2,
  evalBA2,
  evalBA3,
  evalBC1,
  evalBOVR,
  hitTest,
  pressChecks,
  resolveObservations,
  type PressRecord,
} from '../wave01/evaluate.js';
import type { RawChainEntry, RawElement, RawOverflowEntry } from '../wave01/probe.js';

const VW = 390;
const VH = 844;

function chain(over: Partial<RawChainEntry> & { path: string }): RawChainEntry {
  return {
    tag: 'div',
    viewport: false,
    overflowX: 'visible',
    overflowY: 'visible',
    padBox: { left: 0, top: 0, right: VW, bottom: VH },
    scrollLeft: 0,
    scrollTop: 0,
    scrollWidth: VW,
    scrollHeight: VH,
    clientWidth: VW,
    clientHeight: VH,
    policies: [],
    ...over,
  };
}
const viewport = (over: Partial<RawChainEntry> = {}) =>
  chain({ path: '#viewport', tag: '#viewport', viewport: true, policies: ['POL-V-DOC'], ...over });

function el(
  over: Omit<Partial<RawElement>, 'box'> & { box?: Partial<RawElement['box']> } = {}
): RawElement {
  const b = { x: 10, y: 10, width: 50, height: 20, ...(over.box ?? {}) };
  const box = { ...b, right: b.x + b.width, bottom: b.y + b.height };
  return {
    path: 'html>body>x',
    tag: 'a',
    idAttr: null,
    classes: '',
    role: null,
    rendered: true,
    visible: true,
    display: 'inline',
    visibility: 'visible',
    opacity: '1',
    position: 'static',
    text: 'Name',
    title: null,
    ariaLabel: null,
    scrollWidth: b.width,
    clientWidth: b.width,
    scrollHeight: b.height,
    clientHeight: b.height,
    overflowX: 'visible',
    overflowY: 'visible',
    textOverflow: 'clip',
    whiteSpace: 'normal',
    overflowWrap: 'normal',
    srOnly: false,
    hideMobile: false,
    actionable: true,
    policies: [],
    chain: [viewport()],
    hit: {
      cx: box.x + b.width / 2,
      cy: box.y + b.height / 2,
      inViewport: true,
      hitPath: 'html>body>x',
      ok: true,
    },
    clipC: {
      path: '#viewport',
      viewport: true,
      clientWidth: VW,
      contentBox: { left: 0, top: 0, right: VW, bottom: VH },
    },
    style: { outlineStyle: 'none', outlineWidth: '0px', boxShadow: 'none', color: 'rgb(0, 0, 0)' },
    ...(over as Partial<RawElement>),
    box,
  };
}

describe('CLIP: axis-aware chain', () => {
  it('passes inside every clipping ancestor', () => {
    expect(clip(el()).status).toBe('pass');
  });

  it('an overflow-x:hidden ancestor clips x but not y (single-axis rule)', () => {
    const anc = chain({
      path: 'nav',
      overflowX: 'hidden',
      padBox: { left: 0, top: 0, right: 100, bottom: 50 },
    });
    // below the ancestor on y (not clipped on y) but inside on x
    expect(clip(el({ box: { x: 10, y: 60 }, chain: [anc, viewport()] })).status).toBe('pass');
    // cut on x
    const r = clip(el({ box: { x: 80, y: 10 }, chain: [anc, viewport()] }));
    expect(r.status).toBe('fail');
    expect(r.ancestors.find((a) => a.status === 'fail')).toMatchObject({
      axis: 'x',
      reason: 'cut by clip container',
    });
  });

  it('honours the 1px tolerance', () => {
    const anc = chain({
      path: 'c',
      overflowX: 'hidden',
      overflowY: 'hidden',
      padBox: { left: 0, top: 0, right: 60, bottom: 100 },
    });
    expect(clip(el({ box: { x: 10, width: 51 }, chain: [anc, viewport()] })).status).toBe('pass');
    expect(clip(el({ box: { x: 10, width: 52 }, chain: [anc, viewport()] })).status).toBe('fail');
  });

  it('offscreen inside a DECLARED scroller is pending (reachability), not a defect', () => {
    const content = chain({
      path: '.content',
      overflowX: 'auto',
      overflowY: 'auto',
      padBox: { left: 0, top: 60, right: VW, bottom: VH },
      scrollHeight: 2000,
      clientHeight: 784,
      policies: ['POL-V-APP-CONTENT'],
    });
    const r = clip(el({ box: { y: 1200 }, chain: [content, viewport()] }));
    expect(r.status).toBe('pending');
    // the viewport check is deferred behind the pending inner scroller
    expect(r.ancestors.filter((a) => a.path === '#viewport' && a.axis === 'y')[0]?.status).toBe(
      'pending'
    );
  });

  it('beyond a declared scroller range is a clip defect', () => {
    const content = chain({
      path: '.content',
      overflowY: 'auto',
      padBox: { left: 0, top: 0, right: VW, bottom: 800 },
      scrollHeight: 900,
      policies: ['POL-V-APP-CONTENT'],
    });
    expect(clip(el({ box: { y: 1000 }, chain: [content, viewport()] })).status).toBe('fail');
  });

  it('an UNDECLARED scroller never excuses offscreen content (no policy)', () => {
    const anon = chain({
      path: '.random',
      overflowX: 'auto',
      padBox: { left: 0, top: 0, right: 200, bottom: VH },
      scrollWidth: 800,
    });
    const r = clip(el({ box: { x: 300 }, chain: [anon, viewport()] }));
    expect(r.status).toBe('fail');
    expect(r.ancestors[0]?.reason).toContain('undeclared');
  });

  it('no horizontal document scroll is ever pending', () => {
    expect(clip(el({ box: { x: 380, width: 40 } })).status).toBe('fail');
  });

  it('a horizontal policy only declares x (POL-H) and a vertical policy only y', () => {
    const tbl = chain({
      path: '.agent-table-container',
      overflowX: 'auto',
      overflowY: 'hidden',
      padBox: { left: 0, top: 0, right: 300, bottom: 400 },
      scrollWidth: 900,
      scrollHeight: 400,
      policies: ['POL-H-PD-AGENT-TABLE'],
    });
    expect(clip(el({ box: { x: 500 }, chain: [tbl, viewport()] })).status).toBe('pending');
    expect(clip(el({ box: { y: 500 }, chain: [tbl, viewport()] })).status).toBe('fail');
  });

  it('POL-SR and collapsed nav labels are exempt from CLIP and hit tests', () => {
    expect(clip(el({ srOnly: true, box: { x: -500 } })).status).toBe('exempt');
    expect(hitTest(el({ srOnly: true, hit: null }))).toBe('exempt');
    expect(clip(el({ policies: ['POL-COLLAPSED-NAV-LABEL'], box: { x: 900 } })).status).toBe(
      'exempt'
    );
  });
});

describe('hit test and active overlay', () => {
  it('occluded target (deepest element not inside it) fails', () => {
    const covered = el({
      hit: { cx: 35, cy: 20, inViewport: true, hitPath: 'sl-drawer>#shadow>div.panel', ok: false },
    });
    expect(hitTest(covered)).toBe('fail');
    expect(clipAndHit(covered, VW).status).toBe('fail');
  });
  it('centre outside the viewport is pending, an unrendered target fails', () => {
    expect(
      hitTest(el({ hit: { cx: 10, cy: 900, inViewport: false, hitPath: null, ok: false } }))
    ).toBe('pending');
    expect(hitTest(el({ hit: null }))).toBe('fail');
  });
  it('a pending CLIP gates the hit test (no false occlusion while scrolled away)', () => {
    const content = chain({
      path: '.content',
      overflowY: 'auto',
      padBox: { left: 0, top: 60, right: VW, bottom: VH },
      scrollHeight: 3000,
      policies: ['POL-V-APP-CONTENT'],
    });
    const scrolledUnderHeader = el({
      box: { y: 20 },
      chain: [content, viewport()],
      hit: { cx: 35, cy: 30, inViewport: true, hitPath: 'scion-header', ok: false },
    });
    // above .content's visible area but inside its scroll range ⇒ pending
    expect(
      clipAndHit(
        { ...scrolledUnderHeader, chain: [{ ...content, scrollTop: 500 }, viewport()] },
        VW
      ).status
    ).toBe('pending');
  });
});

describe('observation resolution (M0-primary + M0-pos-k)', () => {
  const content = chain({
    path: '.content',
    overflowY: 'auto',
    padBox: { left: 0, top: 0, right: VW, bottom: 800 },
    scrollHeight: 2000,
    policies: ['POL-V-APP-CONTENT'],
  });
  const offscreen = el({
    box: { y: 1200 },
    chain: [content, viewport()],
    hit: { cx: 35, cy: 1210, inViewport: false, hitPath: null, ok: false },
  });
  const inView = el({ box: { y: 400 }, chain: [{ ...content, scrollTop: 800 }, viewport()] });
  it('pending then resolved by wheel ⇒ pass', () => {
    const r = resolveObservations(
      [
        { step: 'primary', method: 'initial', innerWidth: VW, el: offscreen },
        { step: 'pos-1', method: 'wheel', innerWidth: VW, el: inView },
      ],
      clipAndHit
    );
    expect(r).toMatchObject({ outcome: 'pass', decidedAt: 'pos-1' });
  });
  it('resolved only by programmatic positioning ⇒ inconclusive (never reachability evidence)', () => {
    const r = resolveObservations(
      [
        { step: 'primary', method: 'initial', innerWidth: VW, el: offscreen },
        { step: 'pos-1', method: 'programmatic', innerWidth: VW, el: inView },
      ],
      clipAndHit
    );
    expect(r.outcome).toBe('inconclusive');
  });
  it('never brought into view ⇒ fail', () => {
    expect(
      resolveObservations(
        [{ step: 'primary', method: 'initial', innerWidth: VW, el: offscreen }],
        clipAndHit
      ).outcome
    ).toBe('fail');
  });
  it('an in-view failing observation is a fail even if another passes', () => {
    const occluded = { ...inView, hit: { ...inView.hit!, ok: false } };
    expect(
      resolveObservations(
        [
          { step: 'primary', method: 'initial', innerWidth: VW, el: occluded },
          { step: 'pos-1', method: 'wheel', innerWidth: VW, el: inView },
        ],
        clipAndHit
      ).outcome
    ).toBe('fail');
  });
});

describe('A-C1 / A-D1', () => {
  const entry = (over: Partial<RawOverflowEntry>): RawOverflowEntry => ({
    path: 'x',
    tag: 'div',
    rendered: true,
    visibility: 'visible',
    overflowX: 'hidden',
    overflowY: 'visible',
    scrollWidth: 100,
    clientWidth: 100,
    scrollHeight: 10,
    clientHeight: 10,
    srOnly: false,
    policies: [],
    hideMobile: false,
    ...over,
  });
  it('deliberate overflow negative control: an overflowing clip container with no policy FAILS', () => {
    const r = evalAC1([entry({ path: '.table-container', scrollWidth: 868, clientWidth: 356 })]);
    expect(r.outcome).toBe('fail');
  });
  it('1px tolerance', () => {
    expect(evalAC1([entry({ scrollWidth: 101 })]).outcome).toBe('pass');
    expect(evalAC1([entry({ scrollWidth: 102 })]).outcome).toBe('fail');
  });
  it('POL-H scroller, POL-E element and POL-SR (rev 2) are exempt; the exemption is recorded', () => {
    const r = evalAC1([
      entry({
        path: '.agent-table-container',
        overflowX: 'auto',
        scrollWidth: 900,
        policies: ['POL-H-PD-AGENT-TABLE'],
      }),
      entry({ path: '.nav-link-text', scrollWidth: 300, policies: ['POL-E-NAV-LABEL'] }),
      entry({ path: 'caption.sr-only', scrollWidth: 400, clientWidth: 1, srOnly: true }),
    ]);
    expect(r.outcome).toBe('pass');
    expect(r.policyIds.sort()).toEqual(['POL-E-NAV-LABEL', 'POL-H-PD-AGENT-TABLE', 'POL-SR']);
  });
  it('a V policy does not exempt horizontal overflow', () => {
    expect(
      evalAC1([
        entry({
          path: '.content',
          overflowX: 'auto',
          scrollWidth: 500,
          policies: ['POL-V-APP-CONTENT'],
        }),
      ]).outcome
    ).toBe('fail');
  });
  it('no scan ⇒ inconclusive, never pass', () => {
    expect(evalAC1(null).outcome).toBe('inconclusive');
  });
  it('A-D1', () => {
    expect(evalAD1({ innerWidth: 390, doc: { scrollWidth: 391 } as never }).outcome).toBe('pass');
    expect(evalAD1({ innerWidth: 390, doc: { scrollWidth: 392 } as never }).outcome).toBe('fail');
  });
});

describe('A-S1 / A-S2 (nested controls)', () => {
  it('P1 needs the sidebar display:none and a hit-testable menu button', () => {
    const sidebar = el({ display: 'none', visible: false });
    expect(evalAS1('P1', VW, sidebar, el(), el()).outcome).toBe('pass');
    expect(evalAS1('P1', VW, el({ display: 'block' }), el(), el()).outcome).toBe('fail');
  });
  it('P2 sidebar/content overlap > 1px fails', () => {
    const sidebar = el({ box: { x: 0, y: 0, width: 260, height: 900 } });
    const contentOk = el({ box: { x: 260, y: 0, width: 560, height: 900 } });
    const contentBad = el({ box: { x: 250, y: 0, width: 570, height: 900 } });
    expect(evalAS1('P2', 820, sidebar, contentOk, undefined).outcome).toBe('pass');
    expect(evalAS1('P2', 820, sidebar, contentBad, undefined).outcome).toBe('fail');
  });
  it('a target nested inside another target is not sibling overlap; true siblings overlapping >1px fail', () => {
    const header = el({ box: { x: 0, y: 0, width: VW, height: 60 } });
    const host = el({
      path: 'scion-header>#shadow>sl-button',
      box: { x: 300, y: 10, width: 80, height: 40 },
    });
    const inner = el({
      path: 'scion-header>#shadow>sl-button>#shadow>button',
      box: { x: 300, y: 10, width: 80, height: 40 },
    });
    expect(evalAS2(VW, header, [host, inner]).outcome).toBe('pass');
    const sib = el({
      path: 'scion-header>#shadow>sl-icon-button',
      box: { x: 360, y: 15, width: 30, height: 30 },
    });
    const r = evalAS2(VW, header, [host, sib]);
    expect(r.outcome).toBe('fail');
    expect((r.details as { siblingOverlaps: unknown[] }).siblingOverlaps).toHaveLength(1);
  });
});

describe('A-N2 (rev 2)', () => {
  const nav = (o: Partial<Parameters<typeof evalAN2>[0][number]>) => ({
    path: 'a',
    rendered: true,
    href: '/projects',
    labelText: 'Projects',
    tooltipContent: 'Projects',
    collapsed: false,
    accessibleName: 'Projects',
    ...o,
  });
  it('zero rendered entries is NOT-APPLICABLE with the DOM count, never pass', () => {
    const r = evalAN2([nav({ rendered: false }), nav({ rendered: false })]);
    expect(r.outcome).toBe('not-applicable');
    expect(r.details).toMatchObject({ rendered: 0, domCount: 2 });
  });
  it('name must equal the label; collapsed tooltip must equal the label', () => {
    expect(evalAN2([nav({})]).outcome).toBe('pass');
    expect(evalAN2([nav({ accessibleName: 'Proj' })]).outcome).toBe('fail');
    expect(evalAN2([nav({ collapsed: true, tooltipContent: 'X' })]).outcome).toBe('fail');
  });
});

describe('B-* groups clauses (pilot finding rev 2)', () => {
  it('B-C1 compares table.scrollWidth with C(table).clientWidth + 1', () => {
    const t = (sw: number) =>
      el({
        tag: 'table',
        scrollWidth: sw,
        clipC: {
          path: '.table-container',
          viewport: false,
          clientWidth: 356,
          contentBox: { left: 0, top: 0, right: 356, bottom: 900 },
        },
      });
    expect(evalBC1(t(357)).outcome).toBe('pass');
    expect(evalBC1(t(868)).outcome).toBe('fail');
    expect(evalBC1(undefined).outcome).toBe('fail');
  });
  it('B-OVR is derived from B-C1 and records hidden columns', () => {
    const bc1 = evalBC1(
      el({
        scrollWidth: 900,
        clipC: {
          path: 'c',
          viewport: false,
          clientWidth: 356,
          contentBox: { left: 0, top: 0, right: 356, bottom: 9 },
        },
      })
    );
    expect(evalBOVR(bc1, [el({ display: 'none' })]).outcome).toBe('fail');
  });
  const C = {
    path: '.table-container',
    viewport: false,
    clientWidth: 356,
    contentBox: { left: 16, top: 0, right: 372, bottom: 900 },
  };
  const row = (badge: RawElement, link: RawElement = el({ clipC: C })) => ({
    key: 'long',
    id: 'g1',
    name: 'Name',
    accessibleName: null,
    link: [{ step: 'primary', method: 'initial' as const, innerWidth: VW, el: link }],
    badge: [{ step: 'primary', method: 'initial' as const, innerWidth: VW, el: badge }],
  });
  it('B-A2: badge outside C(name-link) content box fails; zero rows fails', () => {
    expect(evalBA2([row(el({ box: { x: 20 } }))]).outcome).toBe('pass');
    expect(evalBA2([row(el({ box: { x: 360, width: 40 } }))]).outcome).toBe('fail');
    expect(evalBA2([]).outcome).toBe('fail');
  });
  it('B-A3: shape (i) untruncated or (ii) ellipsized with exact title/accessible name', () => {
    const name = 'Infrastructure Reliability';
    const mk = (link: RawElement, acc: string | null = null) => [
      { ...row(el(), link), name, accessibleName: acc },
    ];
    expect(evalBA3(mk(el({ text: name }))).outcome).toBe('pass');
    const truncated = el({ text: name, scrollWidth: 300, clientWidth: 120 });
    expect(evalBA3(mk(truncated)).outcome).toBe('fail');
    expect(evalBA3(mk({ ...truncated, title: name })).outcome).toBe('pass');
    expect(evalBA3(mk(truncated, name)).outcome).toBe('pass');
    expect(evalBA3(mk({ ...truncated, title: name.slice(0, 5) })).outcome).toBe('fail');
  });
});

describe('A-F1 keyboard (rev 2 rules, no repair)', () => {
  const focused = (path: string, over: Parameters<typeof el>[0] = {}) =>
    el({
      path,
      style: {
        outlineStyle: 'solid',
        outlineWidth: '2px',
        boxShadow: 'none',
        color: 'rgb(0, 0, 0)',
      },
      ...over,
    });
  const press = (
    n: number,
    element: RawElement | null,
    reached: string[] = [],
    direction: 'forward' | 'backward' = 'forward'
  ): PressRecord => ({
    direction,
    press: n,
    outside: element === null,
    innerWidth: VW,
    innerHeight: VH,
    element,
    reached,
  });
  it('all targets reached with visible indicator ⇒ pass', () => {
    const r = evalAF1({
      targets: ['a', 'b'],
      forward: [
        press(1, focused('x')),
        press(2, focused('btn'), ['a']),
        press(3, focused('link'), ['b']),
      ],
      backward: null,
      baselineForward: {},
      baselineBackward: null,
      startReached: [],
    });
    expect(r.outcome).toBe('pass');
  });
  it('focused element without indicator fails; style diff from unfocused baseline counts as indicator', () => {
    const plain = el({ path: 'p' });
    expect(pressChecks(press(1, plain), {}).indicator).toBe(false);
    expect(
      pressChecks(press(1, plain), { p: { ...plain.style, color: 'rgb(9, 9, 9)' } })
    ).toMatchObject({ indicator: true, indicatorBy: 'style-diff-from-unfocused' });
    const r = evalAF1({
      targets: [],
      forward: [press(1, plain)],
      backward: null,
      baselineForward: {},
      baselineBackward: null,
      startReached: [],
    });
    expect(r.outcome).toBe('fail');
  });
  it('focus offscreen (no native scroll repair) fails', () => {
    const off = focused('o', {
      box: { y: 2000 },
      hit: { cx: 35, cy: 2010, inViewport: false, hitPath: null, ok: false },
    });
    expect(pressChecks(press(1, off), {})).toMatchObject({ visible: false, ok: false });
  });
  it('focus hidden by an intentional scroller (outside its visible area) fails', () => {
    const content = chain({
      path: '.content',
      overflowY: 'auto',
      padBox: { left: 0, top: 60, right: VW, bottom: 400 },
      policies: ['POL-V-APP-CONTENT'],
    });
    const hidden = focused('h', { box: { y: 500 }, chain: [content, viewport()] });
    expect(pressChecks(press(1, hidden), {}).visible).toBe(false);
  });
  it('focus-outside-document ends the direction ungraded; Shift+Tab retry can still reach the target', () => {
    const r = evalAF1({
      targets: ['a'],
      forward: [press(1, focused('x')), press(2, null)],
      backward: [press(1, focused('btn'), ['a'], 'backward')],
      baselineForward: {},
      baselineBackward: {},
      startReached: [],
    });
    expect(r.outcome).toBe('pass');
    expect(JSON.stringify(r.details)).toContain('focus-outside-document');
  });
  it('target unreached in both directions ⇒ fail', () => {
    const r = evalAF1({
      targets: ['a'],
      forward: [press(1, focused('x'))],
      backward: [press(1, null, [], 'backward')],
      baselineForward: {},
      baselineBackward: {},
      startReached: [],
    });
    expect(r.outcome).toBe('fail');
    expect(r.details).toMatchObject({ unreached: ['a'] });
  });
  it('a target that is the start element counts as reached at press 0', () => {
    expect(
      evalAF1({
        targets: ['a'],
        forward: [],
        backward: null,
        baselineForward: {},
        baselineBackward: null,
        startReached: ['a'],
      }).outcome
    ).toBe('pass');
  });
});

describe('B-A2 pairs C(name-link) with the badge observation of the same step', () => {
  it('a badge resolved at pos-1 is checked against the link container measured at pos-1', () => {
    const cAt = (top: number) => ({
      path: '.table-container',
      viewport: false,
      clientWidth: 356,
      contentBox: { left: 16, top, right: 372, bottom: top + 300 },
    });
    const content = chain({
      path: '.content',
      overflowY: 'auto',
      padBox: { left: 0, top: 0, right: VW, bottom: 800 },
      scrollHeight: 3000,
      policies: ['POL-V-APP-CONTENT'],
    });
    const offBadge = el({
      box: { x: 20, y: 1500 },
      chain: [content, viewport()],
      hit: { cx: 45, cy: 1510, inViewport: false, hitPath: null, ok: false },
    });
    const onBadge = el({
      box: { x: 20, y: 300 },
      chain: [{ ...content, scrollTop: 1200 }, viewport()],
    });
    const r = evalBA2([
      {
        key: 'long',
        id: 'g',
        name: 'n',
        accessibleName: null,
        link: [
          { step: 'primary', method: 'initial', innerWidth: VW, el: el({ clipC: cAt(1400) }) },
          { step: 'pos-1', method: 'wheel', innerWidth: VW, el: el({ clipC: cAt(200) }) },
        ],
        badge: [
          { step: 'primary', method: 'initial', innerWidth: VW, el: offBadge },
          { step: 'pos-1', method: 'wheel', innerWidth: VW, el: onBadge },
        ],
      },
    ]);
    expect(r.outcome).toBe('pass');
  });
});
