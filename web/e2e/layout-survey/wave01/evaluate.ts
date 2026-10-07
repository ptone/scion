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
 * Pure Wave01 clause evaluators over RAW probe output (contract FROZEN
 * rev 3). No browser access: every function maps stored measurements to an
 * outcome, so the assessor can recompute from the raw JSON. Each outcome
 * carries its clause ID, the policy IDs it relied on and its raw inputs.
 */

import { POLICIES, TOL, type PolicyKind, type ProfileId } from './contract.js';
import type { RawChainEntry, RawElement, RawOverflowEntry, MeasureResult } from './probe.js';

export type Outcome = 'pass' | 'fail' | 'pending' | 'inconclusive' | 'not-applicable' | 'blocked';

export interface ClauseResult {
  clause: string;
  /** Source of the clause definition. */
  source: 'contract-rev8' | 'pilot-finding-rev2';
  outcome: Outcome;
  policyIds: string[];
  details: unknown;
}

const POLICY_KIND = new Map<string, PolicyKind>(POLICIES.map((p) => [p.id, p.kind]));
const kindOf = (id: string) => POLICY_KIND.get(id);

// ─── CLIP / reachability ─────────────────────────────────────────────────

export type ClipStatus = 'pass' | 'fail' | 'pending' | 'exempt';

export interface ClipAncestorResult {
  path: string;
  axis: 'x' | 'y';
  status: 'pass' | 'fail' | 'pending';
  scroller: boolean;
  policy: string | null;
  reason: string;
}

const isScrollValue = (v: string) => v === 'auto' || v === 'scroll';

/**
 * Is chain entry `c` a DECLARED scroller on `axis`? Declared = a named V
 * (y) or H (x) policy on that entry. The viewport is POL-V-DOC on y only;
 * no horizontal document scroll is permitted (§1b).
 */
export function declaredScroller(c: RawChainEntry, axis: 'x' | 'y'): string | null {
  const want: PolicyKind = axis === 'y' ? 'V' : 'H';
  if (c.viewport) return axis === 'y' ? 'POL-V-DOC' : null;
  const overflow = axis === 'x' ? c.overflowX : c.overflowY;
  if (!isScrollValue(overflow)) return null;
  return c.policies.find((p) => kindOf(p) === want) ?? null;
}

/**
 * §1 CLIP(e) with reachability: for every ancestor in K(e) clipping on an
 * axis, e's border box must lie within its padding box ±TOL. An element
 * outside a DECLARED scroller's visible area but inside its scrollable
 * range is `pending` (to be shown reachable by M0-pos-k), never a defect
 * by itself. POL-SR and POL-COLLAPSED-NAV-LABEL elements are exempt.
 */
export function clip(el: RawElement): { status: ClipStatus; ancestors: ClipAncestorResult[] } {
  if (el.srOnly) return { status: 'exempt', ancestors: [] };
  if (el.policies.some((p) => kindOf(p) === 'COLLAPSED'))
    return { status: 'exempt', ancestors: [] };
  const ancestors: ClipAncestorResult[] = [];
  // Once an inner declared scroller is pending on an axis, the element's
  // position relative to OUTER ancestors on that axis depends on that inner
  // scroll offset, so outer failures on that axis are deferred (pending)
  // until an observation shows the element in the inner scroller's view.
  const deferred = { x: false, y: false };
  for (const c of el.chain) {
    for (const axis of ['x', 'y'] as const) {
      const overflow = axis === 'x' ? c.overflowX : c.overflowY;
      if (!c.viewport && overflow === 'visible') continue;
      const lo = axis === 'x' ? el.box.x : el.box.y;
      const hi = axis === 'x' ? el.box.right : el.box.bottom;
      const plo = axis === 'x' ? c.padBox.left : c.padBox.top;
      const phi = axis === 'x' ? c.padBox.right : c.padBox.bottom;
      const policy = declaredScroller(c, axis);
      if (lo >= plo - TOL && hi <= phi + TOL) {
        ancestors.push({
          path: c.path,
          axis,
          status: 'pass',
          scroller: !!policy,
          policy,
          reason: 'within padding box',
        });
        continue;
      }
      if (policy) {
        const scroll = axis === 'x' ? c.scrollLeft : c.scrollTop;
        const size = axis === 'x' ? c.scrollWidth : c.scrollHeight;
        const cLo = lo - plo + scroll;
        const cHi = hi - plo + scroll;
        if (cLo >= -TOL && cHi <= size + TOL) {
          ancestors.push({
            path: c.path,
            axis,
            status: 'pending',
            scroller: true,
            policy,
            reason: 'outside visible area, inside declared scroll range',
          });
          deferred[axis] = true;
          continue;
        }
        if (deferred[axis]) {
          ancestors.push({
            path: c.path,
            axis,
            status: 'pending',
            scroller: true,
            policy,
            reason: 'deferred: inner declared scroller pending on this axis',
          });
          continue;
        }
        ancestors.push({
          path: c.path,
          axis,
          status: 'fail',
          scroller: true,
          policy,
          reason: 'outside declared scroll range',
        });
        continue;
      }
      if (deferred[axis]) {
        ancestors.push({
          path: c.path,
          axis,
          status: 'pending',
          scroller: isScrollValue(overflow),
          policy: null,
          reason: 'deferred: inner declared scroller pending on this axis',
        });
        continue;
      }
      ancestors.push({
        path: c.path,
        axis,
        status: 'fail',
        scroller: isScrollValue(overflow),
        policy: null,
        reason: isScrollValue(overflow)
          ? 'outside undeclared scroller (no policy)'
          : 'cut by clip container',
      });
    }
  }
  const status: ClipStatus = ancestors.some((a) => a.status === 'fail')
    ? 'fail'
    : ancestors.some((a) => a.status === 'pending')
      ? 'pending'
      : 'pass';
  return { status, ancestors };
}

/** Hit test of e (§1): deepest element at the box centre is e or a composed descendant. */
export function hitTest(el: RawElement): 'pass' | 'fail' | 'pending' | 'exempt' {
  if (el.srOnly) return 'exempt';
  if (!el.hit) return 'fail';
  if (!el.hit.inViewport) return 'pending';
  return el.hit.ok ? 'pass' : 'fail';
}

const withinX = (el: RawElement, innerWidth: number) =>
  el.box.x >= -TOL && el.box.right <= innerWidth + TOL;

// ─── Observations across M0-primary and M0-pos-k ─────────────────────────

export interface Observation {
  /** 'primary' or 'pos-<k>' */
  step: string;
  method: 'initial' | 'wheel' | 'programmatic';
  innerWidth: number;
  el: RawElement;
}

export type ElementCheck = (
  el: RawElement,
  innerWidth: number
) => {
  status: 'pass' | 'fail' | 'pending';
  details: unknown;
};

/**
 * Resolve one element's outcome over its observations (§2b): any in-view
 * failing observation ⇒ fail; first resolved pass via initial/wheel ⇒ pass;
 * resolved only via programmatic positioning ⇒ inconclusive; never
 * brought into view ⇒ fail.
 */
export function resolveObservations(
  obs: Observation[],
  check: ElementCheck
): { outcome: Outcome; decidedAt: string | null; trail: unknown[] } {
  const trail: unknown[] = [];
  let passVia: Observation | null = null;
  let programmaticPass: Observation | null = null;
  for (const o of obs) {
    const r = check(o.el, o.innerWidth);
    trail.push({ step: o.step, method: o.method, status: r.status, details: r.details });
    if (r.status === 'fail') return { outcome: 'fail', decidedAt: o.step, trail };
    if (r.status === 'pass') {
      if (o.method === 'programmatic') programmaticPass ??= o;
      else passVia ??= o;
    }
  }
  if (passVia) return { outcome: 'pass', decidedAt: passVia.step, trail };
  if (programmaticPass) return { outcome: 'inconclusive', decidedAt: programmaticPass.step, trail };
  return { outcome: 'fail', decidedAt: null, trail: [...trail, 'never brought into view'] };
}

/** CLIP + hit test (actionable targets). */
export const clipAndHit: ElementCheck = (el) => {
  const c = clip(el);
  if (c.status === 'fail') return { status: 'fail', details: { clip: c, hit: el.hit } };
  // Not yet in view of a declared scroller: the hit test at this offset is
  // meaningless (it may land on whatever covers the scroller edge).
  if (c.status === 'pending') return { status: 'pending', details: { clip: c, hit: el.hit } };
  const h = hitTest(el);
  if (h === 'pending') return { status: 'pending', details: { clip: c, hit: el.hit } };
  if (h === 'fail') return { status: 'fail', details: { clip: c, hit: el.hit } };
  return { status: 'pass', details: { clip: c.status, hit: h } };
};

/** CLIP only (non-actionable cells). */
export const clipOnly: ElementCheck = (el) => {
  const c = clip(el);
  if (c.status === 'fail') return { status: 'fail', details: { clip: c } };
  if (c.status === 'pending') return { status: 'pending', details: { clip: c } };
  return { status: 'pass', details: { clip: c.status } };
};

function combine(outcomes: Outcome[]): Outcome {
  if (outcomes.includes('fail')) return 'fail';
  if (outcomes.includes('inconclusive')) return 'inconclusive';
  if (outcomes.includes('pending')) return 'pending';
  if (outcomes.includes('blocked')) return 'blocked';
  return 'pass';
}

// ─── Shell clauses (§2) ──────────────────────────────────────────────────

export function evalAD1(m: Pick<MeasureResult, 'doc' | 'innerWidth'>): ClauseResult {
  return {
    clause: 'A-D1',
    source: 'contract-rev8',
    outcome: m.doc.scrollWidth <= m.innerWidth + TOL ? 'pass' : 'fail',
    policyIds: [],
    details: { docScrollWidth: m.doc.scrollWidth, innerWidth: m.innerWidth },
  };
}

/** A-C1 over the raw overflow scan. */
export function evalAC1(overflow: RawOverflowEntry[] | null): ClauseResult {
  if (!overflow) {
    return {
      clause: 'A-C1',
      source: 'contract-rev8',
      outcome: 'inconclusive',
      policyIds: [],
      details: 'no overflow scan',
    };
  }
  const exempt: Array<RawOverflowEntry & { exemptBy: string }> = [];
  const failing: RawOverflowEntry[] = [];
  for (const e of overflow) {
    if (e.scrollWidth <= e.clientWidth + TOL) continue;
    const pol = e.policies.find((p) => ['H', 'E', 'CLAMP', 'COLLAPSED'].includes(kindOf(p) ?? ''));
    if (pol) exempt.push({ ...e, exemptBy: pol });
    else if (e.srOnly) exempt.push({ ...e, exemptBy: 'POL-SR' });
    else failing.push(e);
  }
  return {
    clause: 'A-C1',
    source: 'contract-rev8',
    outcome: failing.length === 0 ? 'pass' : 'fail',
    policyIds: Array.from(new Set(exempt.map((e) => e.exemptBy))),
    details: { scanned: overflow.length, failing, exempt },
  };
}

const overlapW = (a: RawElement['box'], b: RawElement['box']) =>
  Math.max(0, Math.min(a.right, b.right) - Math.max(a.x, b.x));
const overlapH = (a: RawElement['box'], b: RawElement['box']) =>
  Math.max(0, Math.min(a.bottom, b.bottom) - Math.max(a.y, b.y));
const contains = (o: RawElement['box'], i: RawElement['box']) =>
  i.x >= o.x - TOL && i.right <= o.right + TOL && i.y >= o.y - TOL && i.bottom <= o.bottom + TOL;

/** A-S1 on the page layer. */
export function evalAS1(
  profile: ProfileId,
  innerWidth: number,
  sidebar: RawElement | undefined,
  content: RawElement | undefined,
  menuBtn: RawElement | undefined
): ClauseResult {
  if (profile === 'P1') {
    const sidebarHidden = !!sidebar && sidebar.display === 'none';
    const btn = menuBtn ? clipAndHit(menuBtn, innerWidth) : null;
    const ok = sidebarHidden && !!menuBtn && menuBtn.visible && btn?.status === 'pass';
    return {
      clause: 'A-S1',
      source: 'contract-rev8',
      outcome: ok ? 'pass' : btn?.status === 'pending' && sidebarHidden ? 'pending' : 'fail',
      policyIds: [],
      details: {
        sidebarDisplay: sidebar?.display ?? null,
        menuBtnVisible: menuBtn?.visible ?? null,
        menuBtn: btn,
      },
    };
  }
  const visible = !!sidebar && sidebar.visible;
  const inX = !!sidebar && withinX(sidebar, innerWidth);
  const ow =
    sidebar && content && overlapH(sidebar.box, content.box) > 0
      ? overlapW(sidebar.box, content.box)
      : 0;
  return {
    clause: 'A-S1',
    source: 'contract-rev8',
    outcome: visible && inX && !!content && ow <= TOL ? 'pass' : 'fail',
    policyIds: [],
    details: {
      sidebarVisible: visible,
      sidebarBox: sidebar?.box ?? null,
      contentBox: content?.box ?? null,
      overlapWidth: ow,
    },
  };
}

/** A-S2: header within x; every visible actionable target CLIP + hit; no sibling overlap >1px in both axes. */
export function evalAS2(
  innerWidth: number,
  header: RawElement | undefined,
  targets: RawElement[]
): ClauseResult {
  const visible = targets.filter((t) => t.visible && t.box.width > 0 && t.box.height > 0);
  const per = visible.map((t) => ({ path: t.path, ...clipAndHit(t, innerWidth) }));
  const overlaps: Array<{ a: string; b: string; w: number; h: number }> = [];
  for (let i = 0; i < visible.length; i++) {
    for (let j = i + 1; j < visible.length; j++) {
      const a = visible[i]!;
      const b = visible[j]!;
      const nested =
        a.path.startsWith(b.path + '>') ||
        b.path.startsWith(a.path + '>') ||
        contains(a.box, b.box) ||
        contains(b.box, a.box);
      if (nested) continue;
      const w = overlapW(a.box, b.box);
      const h = overlapH(a.box, b.box);
      if (w > TOL && h > TOL) overlaps.push({ a: a.path, b: b.path, w, h });
    }
  }
  const headerOk = !!header && withinX(header, innerWidth);
  const statuses = per.map((p) => p.status as Outcome);
  const outcome: Outcome = !headerOk || overlaps.length ? 'fail' : combine(statuses);
  return {
    clause: 'A-S2',
    source: 'contract-rev8',
    outcome,
    policyIds: [],
    details: { headerBox: header?.box ?? null, targets: per, siblingOverlaps: overlaps },
  };
}

export interface NavRaw {
  path: string;
  rendered: boolean;
  href: string | null;
  labelText: string | null;
  tooltipContent: string | null;
  collapsed: boolean;
  /** Computed accessible name (Playwright accessibility snapshot), null if unavailable. */
  accessibleName: string | null;
}

/** A-N2 (rev 3): rendered = checkVisibility(); zero rendered ⇒ not-applicable. */
export function evalAN2(nav: NavRaw[]): ClauseResult {
  const rendered = nav.filter((n) => n.rendered);
  if (rendered.length === 0) {
    return {
      clause: 'A-N2',
      source: 'contract-rev8',
      outcome: 'not-applicable',
      policyIds: [],
      details: { rendered: 0, domCount: nav.length },
    };
  }
  const per = rendered.map((n) => {
    const nameOk = n.labelText !== null && n.accessibleName === n.labelText;
    const tipOk = !n.collapsed || n.tooltipContent === n.labelText;
    return { ...n, nameOk, tipOk };
  });
  return {
    clause: 'A-N2',
    source: 'contract-rev8',
    outcome: per.every((p) => p.nameOk && p.tipOk) ? 'pass' : 'fail',
    policyIds: [],
    details: { rendered: rendered.length, domCount: nav.length, entries: per },
  };
}

// ─── Groups table B-* (pilot finding rev 2 §1–§2) ─────────────────────────

/** B-C1: table.scrollWidth ≤ C(table).clientWidth + 1. */
export function evalBC1(table: RawElement | undefined): ClauseResult {
  const ok = !!table && !!table.clipC && table.scrollWidth <= table.clipC.clientWidth + TOL;
  return {
    clause: 'B-C1',
    source: 'pilot-finding-rev2',
    outcome: ok ? 'pass' : 'fail',
    policyIds: [],
    details: table
      ? { tableScrollWidth: table.scrollWidth, container: table.clipC, tablePath: table.path }
      : { found: false },
  };
}

/** B-A1: documentElement.scrollWidth ≤ innerWidth + 1. */
export function evalBA1(m: Pick<MeasureResult, 'doc' | 'innerWidth'>): ClauseResult {
  return { ...evalAD1(m), clause: 'B-A1', source: 'pilot-finding-rev2' };
}

export interface FixtureRowObs {
  key: string;
  id: string;
  name: string;
  link: Observation[];
  badge: Observation[];
  /** Computed accessible name of the link (Playwright), null if unavailable. */
  accessibleName: string | null;
}

/** B-A2 element check: badge within viewport x, within C(name-link) content box ±1, hit test. */
export function badgeCheck(
  linkClipCFor: (badge: RawElement) => RawElement['clipC'] | null
): ElementCheck {
  return (badge, innerWidth) => {
    // C(name-link) is taken from the SAME observation step as the badge, so
    // both boxes are at the same scroll offsets.
    const linkClipC = linkClipCFor(badge);
    const inVp = badge.box.x >= -TOL && badge.box.right <= innerWidth + TOL;
    const cb = linkClipC?.contentBox;
    const inC =
      !!cb &&
      badge.box.x >= cb.left - TOL &&
      badge.box.right <= cb.right + TOL &&
      badge.box.y >= cb.top - TOL &&
      badge.box.bottom <= cb.bottom + TOL;
    // Hit test is only meaningful once the element is in view (§2b pending).
    const h = clip(badge).status === 'pending' ? 'pending' : hitTest(badge);
    const details = { box: badge.box, linkClipC, hit: badge.hit, inViewportX: inVp, inC };
    if (!inVp || !inC) {
      // Out of C vertically only because the row is scrolled out of a
      // declared scroller is still a geometry failure under B-A2's literal
      // definition, so it is not softened to pending.
      return { status: 'fail', details };
    }
    if (h === 'pending') return { status: 'pending', details };
    return { status: h === 'pass' ? 'pass' : 'fail', details };
  };
}

export function evalBA2(rows: FixtureRowObs[]): ClauseResult {
  if (rows.length === 0) {
    return {
      clause: 'B-A2',
      source: 'pilot-finding-rev2',
      outcome: 'fail',
      policyIds: [],
      details: 'zero fixture rows measured',
    };
  }
  const per = rows.map((r) => {
    if (r.badge.length === 0)
      return { key: r.key, outcome: 'fail' as Outcome, trail: ['no .type-badge'] };
    const byBadge = new Map<RawElement, RawElement['clipC'] | null>();
    for (const b of r.badge) {
      byBadge.set(b.el, r.link.find((l) => l.step === b.step)?.el.clipC ?? null);
    }
    const res = resolveObservations(
      r.badge,
      badgeCheck((badge) => byBadge.get(badge) ?? null)
    );
    return { key: r.key, ...res };
  });
  return {
    clause: 'B-A2',
    source: 'pilot-finding-rev2',
    outcome: combine(per.map((p) => p.outcome)),
    policyIds: [],
    details: { rows: per },
  };
}

/** B-A3 element check for one row. */
/**
 * Rev 3 §1 rendered-text comparison with assessor ruling R-5 (16:03Z): apply
 * the element's computed white-space collapsing to v, then compare with the
 * element's innerText. Collapsible white space is EXACTLY U+0020 space,
 * U+0009 tab, U+000A line feed and U+000D carriage return (CSS Text).
 * Form feed, NBSP (U+00A0), U+2000–U+200A, U+202F, U+3000 and every other
 * character stay significant (JS `\s` / String#trim are NOT used).
 * normal/nowrap: collapse runs of the set to one space and trim the set;
 * pre/pre-wrap/break-spaces: exact; pre-line: collapse runs of space/tab
 * only. Returns the transformed v for the record.
 */
const CSS_WS_RUN = /[ \u0009\u000A\u000D]+/g;
const CSS_WS_EDGE = /^[ \u0009\u000A\u000D]+|[ \u0009\u000A\u000D]+$/g;
const CSS_SPACE_TAB_RUN = /[ \u0009]+/g;

export function renderedTextEquals(
  innerText: string | null,
  v: string,
  whiteSpace: string
): { equal: boolean; expected: string; rule: string } {
  let expected: string;
  let rule: string;
  if (whiteSpace === 'pre' || whiteSpace === 'pre-wrap' || whiteSpace === 'break-spaces') {
    expected = v;
    rule = 'exact';
  } else if (whiteSpace === 'pre-line') {
    expected = v.replace(CSS_SPACE_TAB_RUN, ' ');
    rule = 'collapse-space-tab (R-5)';
  } else {
    expected = v.replace(CSS_WS_RUN, ' ').replace(CSS_WS_EDGE, '');
    rule = 'collapse-css-whitespace-trim (R-5: U+0020 U+0009 U+000A U+000D only)';
  }
  return { equal: innerText !== null && innerText === expected, expected, rule };
}

export function nameCheck(name: string, accessibleName: string | null): ElementCheck {
  return (link, innerWidth) => {
    const onScreen = link.box.x >= -TOL && link.box.right <= innerWidth + TOL;
    const rendered = renderedTextEquals(link.innerText, name, link.whiteSpace);
    const shapeI = rendered.equal && link.scrollWidth <= link.clientWidth + TOL;
    const shapeII =
      link.scrollWidth > link.clientWidth &&
      (link.title === name || link.ariaLabel === name || accessibleName === name);
    // Hit test is only meaningful once the element is in view (§2b pending).
    const h = clip(link).status === 'pending' ? 'pending' : hitTest(link);
    const details = {
      box: link.box,
      innerText: link.innerText,
      whiteSpace: link.whiteSpace,
      readbackName: name,
      renderedTextComparison: rendered,
      scrollWidth: link.scrollWidth,
      clientWidth: link.clientWidth,
      title: link.title,
      ariaLabel: link.ariaLabel,
      accessibleName,
      shape: shapeI ? 'i' : shapeII ? 'ii' : null,
      hit: link.hit,
    };
    if (!onScreen || !(shapeI || shapeII)) return { status: 'fail', details };
    if (h === 'pending') return { status: 'pending', details };
    return { status: h === 'pass' ? 'pass' : 'fail', details };
  };
}

export function evalBA3(rows: FixtureRowObs[]): ClauseResult {
  if (rows.length === 0) {
    return {
      clause: 'B-A3',
      source: 'pilot-finding-rev2',
      outcome: 'fail',
      policyIds: [],
      details: 'zero fixture rows measured',
    };
  }
  const per = rows.map((r) => ({
    key: r.key,
    ...resolveObservations(r.link, nameCheck(r.name, r.accessibleName)),
  }));
  return {
    clause: 'B-A3',
    source: 'pilot-finding-rev2',
    outcome: combine(per.map((p) => p.outcome)),
    policyIds: [],
    details: { rows: per },
  };
}

/** B-OVR: satisfied collectively by B-C1 (pilot finding §2); hidden columns out of scope. */
export function evalBOVR(bc1: ClauseResult, hiddenColumns: RawElement[]): ClauseResult {
  return {
    clause: 'B-OVR',
    source: 'pilot-finding-rev2',
    outcome: bc1.outcome,
    policyIds: [],
    details: {
      derivedFrom: 'B-C1 (pilot finding rev 2 §2: B-OVR is satisfied collectively by B-C1)',
      intentionallyHiddenColumns: hiddenColumns.map((h) => ({ path: h.path, display: h.display })),
    },
  };
}

// ─── A-F1 keyboard (§2, rev 8 rules) ─────────────────────────────────────

export interface PressRecord {
  direction: 'forward' | 'backward';
  press: number;
  outside: boolean;
  innerWidth: number;
  innerHeight: number;
  element: RawElement | null;
  /** Declared target keys this press reached. */
  reached: string[];
  /** Rev 8: F for every sampled node (element, ::before/::after, flat-tree ancestors), keyed by node. */
  focusNodes?: Record<string, Record<string, string>>;
  /** Rev 8: running CSS animations/transitions on the element or flat-tree ancestors at F. */
  runningAnimations?: Array<{ path: string; kind: string; name: string | null }>;
}

/**
 * Rev 8 §2 A-F1 unfocused samples, taken in the same context before
 * the first key press: U1, then the inter-press settle (two animation frames,
 * the same settle used after every Tab), then U2. Maps are keyed by sampled node: path, path::before, path::after (flat-tree ancestors included).
 */
export interface FocusBaseline {
  u1: Record<string, Record<string, string>>;
  u2: Record<string, Record<string, string>>;
  u1At?: string;
  u2At?: string;
  settle?: string;
}

export const EMPTY_BASELINE: FocusBaseline = Object.freeze({ u1: {}, u2: {} }) as FocusBaseline;

const isOutlineProp = (k: string) => k.startsWith('outline');
const diffNames = (a: Record<string, string>, b: Record<string, string>) =>
  [...new Set([...Object.keys(a), ...Object.keys(b)])].filter((k) => a[k] !== b[k]).sort();

export type IndicatorState = 'present' | 'absent' | 'undeterminable';

type Style = Record<string, string>;
const sv = (st: Style | undefined, kebab: string, camel: string) => st?.[kebab] ?? st?.[camel];
const outlinePresent = (st: Style | undefined) => {
  const os = sv(st, 'outline-style', 'outlineStyle');
  return (
    os !== undefined &&
    os !== 'none' &&
    parseFloat(sv(st, 'outline-width', 'outlineWidth') ?? '0') > 0
  );
};
const shadowPresent = (st: Style | undefined) => {
  const bs = sv(st, 'box-shadow', 'boxShadow');
  return !!bs && bs !== 'none';
};
const indicatorValues = (st: Style | undefined) =>
  st
    ? {
        'outline-style': sv(st, 'outline-style', 'outlineStyle') ?? null,
        'outline-width': sv(st, 'outline-width', 'outlineWidth') ?? null,
        'outline-color': sv(st, 'outline-color', 'outlineColor') ?? null,
        'outline-offset': sv(st, 'outline-offset', 'outlineOffset') ?? null,
        'box-shadow': sv(st, 'box-shadow', 'boxShadow') ?? null,
      }
    : null;

export type AF1Decision =
  | 'outline-appears'
  | 'box-shadow-appears'
  | 'static-no-indicator'
  | 'undeterminable';

/**
 * Rev 8 §2 A-F1 per-press checks, first match wins (ruling R-16):
 * 1. PASS — stable complete baseline (U1 = U2 on every sampled node), nothing
 *    running at F, and on the element itself an outline (a) or box-shadow (b)
 *    ABSENT in U2 and PRESENT in F;
 * 2. INCONCLUSIVE — no baseline; any U1 ≠ U2; a running animation/transition;
 *    an outline or box-shadow present in BOTH U2 and F; any non-outline U2→F
 *    difference on the element; any U2→F difference on a pseudo-element or
 *    ancestor;
 * 3. FAIL — F = U2 on every sampled node except the element's own outline-*;
 * 4. otherwise INCONCLUSIVE.
 * Visibility/hit failures FAIL the press independently of the indicator.
 */
export function pressChecks(
  p: PressRecord,
  baseline: FocusBaseline
): {
  graded: boolean;
  result: 'pass' | 'fail' | 'inconclusive' | 'not-graded';
  ok: boolean;
  visible: boolean;
  hit: boolean;
  indicator: IndicatorState;
  decidedBy: AF1Decision | null;
  indicatorBy: string | null;
  /** Differing property names per sampled node (only nodes with differences). */
  nodeDiffsU1U2: Record<string, string[]> | null;
  nodeDiffsU2F: Record<string, string[]> | null;
  /** The element's own outline-* and box-shadow VALUES in U2 and F (assessor 18:14Z). */
  indicatorValuesU2: Record<string, string | null> | null;
  indicatorValuesF: Record<string, string | null> | null;
  sampledNodes: string[];
  undeterminableReasons: string[];
  baselineMissing: boolean;
  runningAnimations: Array<{ path: string; kind: string; name: string | null }>;
} {
  if (p.outside || !p.element)
    return {
      graded: false,
      result: 'not-graded',
      ok: true,
      visible: false,
      hit: false,
      indicator: 'absent',
      decidedBy: null,
      indicatorBy: null,
      nodeDiffsU1U2: null,
      nodeDiffsU2F: null,
      indicatorValuesU2: null,
      indicatorValuesF: null,
      sampledNodes: [],
      undeterminableReasons: [],
      baselineMissing: false,
      runningAnimations: [],
    };
  const e = p.element;
  const b = e.box;
  const vp = { left: 0, top: 0, right: p.innerWidth, bottom: p.innerHeight };
  const intersects = (r: { left: number; top: number; right: number; bottom: number }) =>
    b.right > r.left && b.x < r.right && b.bottom > r.top && b.y < r.bottom;
  const scrollers = e.chain.filter(
    (c) => !c.viewport && (isScrollValue(c.overflowX) || isScrollValue(c.overflowY))
  );
  const visible = intersects(vp) && scrollers.every((c) => intersects(c.padBox));
  const hit = !!e.hit && e.hit.inViewport && e.hit.ok;
  // F per sampled node; the element's own F is its full computed style.
  const fNodes: Record<string, Style> = { ...(p.focusNodes ?? {}), [e.path]: e.style };
  const sampledNodes = Object.keys(fNodes).sort();
  const baselineMissing = sampledNodes.some((k) => !baseline.u1[k] || !baseline.u2[k]);
  const running = p.runningAnimations ?? [];
  const nodeDiffsU1U2: Record<string, string[]> = {};
  const nodeDiffsU2F: Record<string, string[]> = {};
  for (const k of sampledNodes) {
    const u1 = baseline.u1[k];
    const u2 = baseline.u2[k];
    if (u1 && u2) {
      const d = diffNames(u1, u2);
      if (d.length) nodeDiffsU1U2[k] = d;
    }
    if (u2) {
      const d = diffNames(u2, fNodes[k]!);
      if (d.length) nodeDiffsU2F[k] = d;
    }
  }
  const u2el = baseline.u2[e.path];
  const fel = e.style;
  const stable = !baselineMissing && Object.keys(nodeDiffsU1U2).length === 0;
  const elDiffs = nodeDiffsU2F[e.path] ?? [];
  const otherNodeDiffs = Object.keys(nodeDiffsU2F).filter((k) => k !== e.path);
  let decidedBy: AF1Decision;
  const reasons: string[] = [];
  if (stable && !running.length && outlinePresent(fel) && !outlinePresent(u2el))
    decidedBy = 'outline-appears';
  else if (stable && !running.length && shadowPresent(fel) && !shadowPresent(u2el))
    decidedBy = 'box-shadow-appears';
  else {
    if (baselineMissing) reasons.push('no U1/U2 baseline for every sampled node');
    if (Object.keys(nodeDiffsU1U2).length) reasons.push('U1 differs from U2');
    if (running.length) reasons.push('animation/transition running at F');
    if (
      u2el &&
      ((outlinePresent(u2el) && outlinePresent(fel)) || (shadowPresent(u2el) && shadowPresent(fel)))
    )
      reasons.push('outline or box-shadow present in both U2 and F (persistent)');
    if (elDiffs.some((k) => !isOutlineProp(k)))
      reasons.push('non-outline difference U2/F on the element');
    if (otherNodeDiffs.length) reasons.push('U2/F difference on a pseudo-element or ancestor');
    decidedBy = reasons.length ? 'undeterminable' : 'static-no-indicator';
    // Rule 3 needs F = U2 on every node except the element's own outline-*;
    // with no listed condition this holds by construction (stable, no other
    // node diffs, only outline-* element diffs).
  }
  const indicator: IndicatorState =
    decidedBy === 'outline-appears' || decidedBy === 'box-shadow-appears'
      ? 'present'
      : decidedBy === 'static-no-indicator'
        ? 'absent'
        : 'undeterminable';
  const result: 'pass' | 'fail' | 'inconclusive' =
    !visible || !hit || indicator === 'absent'
      ? 'fail'
      : indicator === 'undeterminable'
        ? 'inconclusive'
        : 'pass';
  return {
    graded: true,
    result,
    ok: result === 'pass',
    visible,
    hit,
    indicator,
    decidedBy,
    indicatorBy: indicator === 'present' ? decidedBy : null,
    nodeDiffsU1U2,
    nodeDiffsU2F,
    indicatorValuesU2: indicatorValues(u2el),
    indicatorValuesF: indicatorValues(fel),
    sampledNodes,
    undeterminableReasons: reasons,
    baselineMissing,
    runningAnimations: running,
  };
}

/** Raw-record form of a press: per-node F maps replaced by the press checks' per-node names. */
export function pressForRaw(p: PressRecord, baseline: FocusBaseline) {
  const { focusNodes: _omit, ...rest } = p;
  void _omit;
  const c = pressChecks(p, baseline);
  return {
    ...rest,
    sampledNodes: c.sampledNodes,
    nodeDiffsU1U2: c.nodeDiffsU1U2,
    nodeDiffsU2F: c.nodeDiffsU2F,
    indicatorValuesU2: c.indicatorValuesU2,
    indicatorValuesF: c.indicatorValuesF,
  };
}

export function evalAF1(input: {
  targets: string[];
  forward: PressRecord[];
  backward: PressRecord[] | null;
  baselineForward: FocusBaseline;
  baselineBackward: FocusBaseline | null;
  startReached: string[];
  startBackwardReached?: string[];
}): ClauseResult {
  const reachedF = new Set(input.startReached);
  const reachedB = new Set(input.startBackwardReached ?? []);
  const perPress: unknown[] = [];
  let anyBad = false;
  let anyInconclusive = false;
  for (const [presses, base, set] of [
    [input.forward, input.baselineForward, reachedF],
    [input.backward ?? [], input.baselineBackward ?? EMPTY_BASELINE, reachedB],
  ] as const) {
    for (const p of presses) {
      const c = pressChecks(p, base);
      if (p.outside) {
        perPress.push({ direction: p.direction, press: p.press, result: 'focus-outside-document' });
        continue;
      }
      for (const t of p.reached) set.add(t);
      if (c.result === 'fail') anyBad = true;
      else if (c.result === 'inconclusive') anyInconclusive = true;
      perPress.push({
        direction: p.direction,
        press: p.press,
        path: p.element?.path,
        reached: p.reached,
        ...c,
      });
    }
  }
  const unreached = input.targets.filter((t) => !reachedF.has(t) && !reachedB.has(t));
  return {
    clause: 'A-F1',
    source: 'contract-rev8',
    // Rev 8: FAIL dominates; an undeterminable indicator on any graded press
    // makes A-F1 INCONCLUSIVE (never PASS, never a defect by itself).
    outcome: anyBad || unreached.length > 0 ? 'fail' : anyInconclusive ? 'inconclusive' : 'pass',
    policyIds: [],
    details: {
      targets: input.targets,
      baselineSamples: {
        forward: {
          u1At: input.baselineForward.u1At,
          u2At: input.baselineForward.u2At,
          settle: input.baselineForward.settle,
        },
        backward: input.baselineBackward
          ? {
              u1At: input.baselineBackward.u1At,
              u2At: input.baselineBackward.u2At,
              settle: input.baselineBackward.settle,
            }
          : null,
      },
      reachedForward: [...reachedF],
      reachedBackward: [...reachedB],
      unreached,
      presses: perPress,
    },
  };
}

/** Merge many clause results; any capture-level problem is decided by the runner, not here. */
export function summarize(results: ClauseResult[]): Record<string, Outcome> {
  return Object.fromEntries(results.map((r) => [r.clause, r.outcome]));
}
