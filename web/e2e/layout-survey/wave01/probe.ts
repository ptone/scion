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
 * In-page RAW measurement probe (Wave01).
 *
 * `probe` is passed to page.evaluate and must stay self-contained (no
 * imports, no outer references). It only READS layout: it never focuses,
 * clicks, mutates the DOM or adds attributes. The single exception is the
 * explicitly labelled `programmatic-scroll` op, the §2b geometry-positioning
 * fallback, whose use is recorded and never counted as reachability. Grading happens in
 * Node (evaluate.ts) from the raw JSON this returns, so the assessor can
 * recompute every outcome from the stored measurements.
 *
 * Walk rules (contract §1): open shadow roots and slot assignment are
 * traversed; the clipping chain K(e) follows the flat tree (assignedSlot →
 * parentElement → shadow host) up to and including the viewport; hit tests
 * recurse elementFromPoint into shadow roots.
 */

import type { PolicyDef } from './contract.js';

export interface RawBox {
  x: number;
  y: number;
  width: number;
  height: number;
  right: number;
  bottom: number;
}

export interface RawEdges {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

export interface RawChainEntry {
  path: string;
  tag: string;
  viewport: boolean;
  overflowX: string;
  overflowY: string;
  /** Padding box in viewport coordinates (border box minus borders minus scrollbar). */
  padBox: RawEdges;
  scrollLeft: number;
  scrollTop: number;
  scrollWidth: number;
  scrollHeight: number;
  clientWidth: number;
  clientHeight: number;
  policies: string[];
}

export interface RawHit {
  cx: number;
  cy: number;
  inViewport: boolean;
  hitPath: string | null;
  ok: boolean;
}

export interface RawElement {
  path: string;
  tag: string;
  idAttr: string | null;
  classes: string;
  role: string | null;
  /** checkVisibility() — "rendered" (has a layout box). */
  rendered: boolean;
  /** checkVisibility({opacityProperty, visibilityProperty}) — visible. */
  visible: boolean;
  display: string;
  visibility: string;
  opacity: string;
  position: string;
  box: RawBox;
  /** textContent with whitespace runs collapsed (diagnostic only). */
  text: string;
  /** Rendered text (HTMLElement.innerText), unmodified (contract rev 3 rendered-text rule). */
  innerText: string | null;
  title: string | null;
  ariaLabel: string | null;
  scrollWidth: number;
  clientWidth: number;
  scrollHeight: number;
  clientHeight: number;
  overflowX: string;
  overflowY: string;
  textOverflow: string;
  whiteSpace: string;
  overflowWrap: string;
  srOnly: boolean;
  hideMobile: boolean;
  actionable: boolean;
  policies: string[];
  chain: RawChainEntry[];
  hit: RawHit | null;
  /** Nearest flat ancestor with overflow-x hidden|clip|auto|scroll (pilot finding C(e)). */
  clipC: { path: string; contentBox: RawEdges; clientWidth: number; viewport: boolean } | null;
  /** Focus-indicator style vector (always captured; compared in Node). */
  style: Record<string, string>;
}

export interface RawOverflowEntry {
  path: string;
  tag: string;
  rendered: boolean;
  visibility: string;
  overflowX: string;
  overflowY: string;
  scrollWidth: number;
  clientWidth: number;
  scrollHeight: number;
  clientHeight: number;
  srOnly: boolean;
  policies: string[];
  hideMobile: boolean;
}

export type ProbeQuery =
  | { key: string; op: 'one'; css: string; within?: string }
  | { key: string; op: 'all'; css: string; within?: string }
  | { key: string; op: 'actionable'; within: string };

export type ProbeRequest =
  | {
      op: 'ready';
      pageSelector: string;
      countCss: string | null;
    }
  | { op: 'frame-stable'; cssList: string[]; maxFrames: number }
  | {
      op: 'measure';
      queries: ProbeQuery[];
      overflowScan: boolean;
      policies: readonly PolicyDef[];
      policyContext: string[];
      actionable: { tags: readonly string[]; roles: readonly string[] };
    }
  | {
      op: 'active';
      policies: readonly PolicyDef[];
      policyContext: string[];
      actionable: { tags: readonly string[]; roles: readonly string[] };
    }
  | { op: 'focus-baseline' }
  | { op: 'nav'; within: string }
  | { op: 'path' }
  | {
      op: 'programmatic-scroll';
      name: string;
      dx: number;
      dy: number;
      policies: readonly PolicyDef[];
      policyContext: string[];
    }
  | { op: 'scrollers'; names: string[]; policies: readonly PolicyDef[]; policyContext: string[] };

export interface MeasureResult {
  at: number;
  url: string;
  innerWidth: number;
  innerHeight: number;
  devicePixelRatio: number;
  doc: {
    scrollWidth: number;
    scrollHeight: number;
    clientWidth: number;
    clientHeight: number;
    scrollX: number;
    scrollY: number;
    htmlOverflowX: string;
    htmlOverflowY: string;
    bodyOverflowX: string;
    bodyOverflowY: string;
  };
  elements: Record<string, RawElement[]>;
  overflow: RawOverflowEntry[] | null;
  overflowSkippedUnrendered: number;
}

/**
 * The probe. Every helper is declared inside so Playwright can serialize it.
 */
export function probe(a: ProbeRequest | Element, b?: ProbeRequest): unknown {
  // page.evaluate(probe, req) → probe(req); locator.evaluate(probe, req) → probe(el, req).
  const target: Element | null = a instanceof Element ? a : null;
  const req = (a instanceof Element ? b : a) as ProbeRequest;
  const round = (n: number) => Math.round(n * 100) / 100;
  const boxOf = (r: DOMRect): RawBox => ({
    x: round(r.x),
    y: round(r.y),
    width: round(r.width),
    height: round(r.height),
    right: round(r.right),
    bottom: round(r.bottom),
  });

  const hostOf = (n: Node): Element | null => {
    const root = n.getRootNode();
    return root instanceof ShadowRoot ? root.host : null;
  };
  /** Flat-tree parent (slot assignment first). */
  const flatParent = (el: Element): Element | null =>
    (el.assignedSlot as Element | null) ?? el.parentElement ?? hostOf(el);
  /** Shadow-including parent (no slot hop). */
  const treeParent = (el: Element): Element | null => el.parentElement ?? hostOf(el);

  const deepPath = (el: Element): string => {
    const segs: string[] = [];
    let cur: Element | null = el;
    while (cur) {
      const parent: Node | null = cur.parentNode;
      let idx = 1;
      let sib = cur.previousElementSibling;
      while (sib) {
        idx++;
        sib = sib.previousElementSibling;
      }
      const id = cur.id ? `#${cur.id}` : '';
      const cls =
        typeof cur.className === 'string' && cur.className.trim()
          ? '.' + cur.className.trim().split(/\s+/).slice(0, 3).join('.')
          : '';
      segs.unshift(`${cur.localName}${id}${cls}:nth-child(${idx})`);
      if (parent instanceof ShadowRoot) {
        segs.unshift('#shadow');
        cur = parent.host;
      } else {
        cur = parent instanceof Element ? parent : null;
      }
    }
    return segs.join('>');
  };

  const composedContains = (anc: Element, node: Element | null): boolean => {
    for (let n = node; n; n = treeParent(n)) if (n === anc) return true;
    for (let n = node; n; n = flatParent(n)) if (n === anc) return true;
    return false;
  };

  const deepPoint = (x: number, y: number): Element | null => {
    let el = document.elementFromPoint(x, y);
    while (el && el.shadowRoot) {
      const inner = el.shadowRoot.elementFromPoint(x, y);
      if (!inner || inner === el) break;
      el = inner;
    }
    return el;
  };

  const deepAll = (root: ParentNode, sel: string, out: Element[] = []): Element[] => {
    for (const el of Array.from(root.querySelectorAll('*'))) {
      if (el.matches(sel)) out.push(el);
      if (el.shadowRoot) deepAll(el.shadowRoot, sel, out);
    }
    if (root instanceof Element && root.shadowRoot) deepAll(root.shadowRoot, sel, out);
    return out;
  };
  const allDeep = (root: ParentNode, out: Element[] = []): Element[] => {
    for (const el of Array.from(root.querySelectorAll('*'))) {
      out.push(el);
      if (el.shadowRoot) allDeep(el.shadowRoot, out);
    }
    return out;
  };

  const policiesFor = (
    el: Element,
    cs: CSSStyleDeclaration,
    policies: readonly PolicyDef[],
    ctx: string[]
  ): string[] => {
    const host = hostOf(el);
    const hostTag = host ? host.localName : '#document';
    const ids: string[] = [];
    for (const p of policies) {
      if (p.host === '#viewport') continue;
      if (p.host !== '*' && p.host !== hostTag) continue;
      if (p.states && !p.states.some((s) => ctx.includes(s))) continue;
      if (p.hostAttr && !(host && host.hasAttribute(p.hostAttr))) continue;
      if (p.part) {
        const parts = (el.getAttribute('part') ?? '').split(/\s+/);
        if (!parts.includes(p.part)) continue;
        if (p.host === 'sl-drawer' && !(host && host.classList.contains('mobile-drawer'))) continue;
      } else if (p.selector) {
        try {
          if (!el.matches(p.selector)) continue;
        } catch {
          continue;
        }
      } else continue;
      if (p.requiresScrollableX && !(cs.overflowX === 'auto' || cs.overflowX === 'scroll'))
        continue;
      if (!ids.includes(p.id)) ids.push(p.id);
    }
    return ids;
  };

  /** §1b POL-SR exact pattern. */
  const isSrOnly = (el: Element, cs: CSSStyleDeclaration): boolean => {
    const r = el.getBoundingClientRect();
    return (
      r.width <= 1 &&
      r.height <= 1 &&
      (cs.clip !== 'auto' || cs.clipPath !== 'none') &&
      cs.overflowX === 'hidden' &&
      cs.overflowY === 'hidden' &&
      cs.position === 'absolute' &&
      (el.textContent ?? '').trim() !== ''
    );
  };

  const isActionable = (
    el: Element,
    def: { tags: readonly string[]; roles: readonly string[] }
  ): boolean => {
    if (def.tags.includes(el.localName)) return true;
    const role = el.getAttribute('role');
    if (role && def.roles.includes(role)) return true;
    if (el.getAttribute('slot') === 'trigger' && el.parentElement?.localName === 'sl-dropdown')
      return true;
    return false;
  };

  // Full computed style (every property, kebab-case names) for A-F1 rule (c)
  // (assessor/owner 16:56Z, review3 O2): used for the focused element and the
  // same-context unfocused baseline. The small vector below is kept for the
  // geometry records, where indicator grading does not apply.
  const fullStyle = (cs: CSSStyleDeclaration): Record<string, string> => {
    const out: Record<string, string> = {};
    for (let i = 0; i < cs.length; i++) {
      const name = cs.item(i);
      out[name] = cs.getPropertyValue(name);
    }
    return out;
  };

  /**
   * Rev 8 A-F1 sampled nodes of a focus candidate: the element, its
   * ::before/::after pseudo-elements and every flat-tree ancestor up to the
   * document root, keyed by node. Adds into `out` (shared nodes deduplicated).
   */
  const sampleNodes = (
    el: Element,
    out: Record<string, Record<string, string>> = {}
  ): Record<string, Record<string, string>> => {
    const p0 = deepPath(el);
    if (!out[p0]) out[p0] = fullStyle(getComputedStyle(el));
    if (!out[p0 + '::before']) out[p0 + '::before'] = fullStyle(getComputedStyle(el, '::before'));
    if (!out[p0 + '::after']) out[p0 + '::after'] = fullStyle(getComputedStyle(el, '::after'));
    for (let a = flatParent(el); a; a = flatParent(a)) {
      const pa = deepPath(a);
      if (!out[pa]) out[pa] = fullStyle(getComputedStyle(a));
    }
    return out;
  };

  const styleVector = (cs: CSSStyleDeclaration): Record<string, string> => ({
    outlineStyle: cs.outlineStyle,
    outlineWidth: cs.outlineWidth,
    outlineColor: cs.outlineColor,
    outlineOffset: cs.outlineOffset,
    boxShadow: cs.boxShadow,
    backgroundColor: cs.backgroundColor,
    borderTopColor: cs.borderTopColor,
    borderBottomColor: cs.borderBottomColor,
    color: cs.color,
    textDecorationLine: cs.textDecorationLine,
  });

  const viewportEntry = (): RawChainEntry => {
    const de = document.documentElement;
    const se = document.scrollingElement ?? de;
    const hcs = getComputedStyle(de);
    return {
      path: '#viewport',
      tag: '#viewport',
      viewport: true,
      overflowX: hcs.overflowX,
      overflowY: hcs.overflowY,
      padBox: { left: 0, top: 0, right: de.clientWidth, bottom: de.clientHeight },
      scrollLeft: se.scrollLeft,
      scrollTop: se.scrollTop,
      scrollWidth: se.scrollWidth,
      scrollHeight: se.scrollHeight,
      clientWidth: de.clientWidth,
      clientHeight: de.clientHeight,
      policies: ['POL-V-DOC'],
    };
  };

  const chainEntry = (
    anc: Element,
    policies: readonly PolicyDef[],
    ctx: string[]
  ): RawChainEntry | null => {
    const cs = getComputedStyle(anc);
    if (cs.overflowX === 'visible' && cs.overflowY === 'visible') return null;
    // html always propagates overflow to the viewport; body does when html is visible.
    if (anc === document.documentElement) return null;
    if (anc === document.body) {
      const hcs = getComputedStyle(document.documentElement);
      if (hcs.overflowX === 'visible' && hcs.overflowY === 'visible') return null;
    }
    const he = anc as HTMLElement;
    const r = anc.getBoundingClientRect();
    const left = r.left + he.clientLeft;
    const top = r.top + he.clientTop;
    return {
      path: deepPath(anc),
      tag: anc.localName,
      viewport: false,
      overflowX: cs.overflowX,
      overflowY: cs.overflowY,
      padBox: {
        left: round(left),
        top: round(top),
        right: round(left + he.clientWidth),
        bottom: round(top + he.clientHeight),
      },
      scrollLeft: he.scrollLeft,
      scrollTop: he.scrollTop,
      scrollWidth: he.scrollWidth,
      scrollHeight: he.scrollHeight,
      clientWidth: he.clientWidth,
      clientHeight: he.clientHeight,
      policies: policiesFor(anc, cs, policies, ctx),
    };
  };

  const clipC = (el: Element): RawElement['clipC'] => {
    for (let a = flatParent(el); a; a = flatParent(a)) {
      if (a === document.documentElement) break;
      const cs = getComputedStyle(a);
      if (['hidden', 'clip', 'auto', 'scroll'].includes(cs.overflowX)) {
        const he = a as HTMLElement;
        const r = a.getBoundingClientRect();
        const pl = parseFloat(cs.paddingLeft) || 0;
        const pr = parseFloat(cs.paddingRight) || 0;
        const pt = parseFloat(cs.paddingTop) || 0;
        const pb = parseFloat(cs.paddingBottom) || 0;
        const left = r.left + he.clientLeft;
        const top = r.top + he.clientTop;
        return {
          path: deepPath(a),
          viewport: false,
          clientWidth: he.clientWidth,
          contentBox: {
            left: round(left + pl),
            top: round(top + pt),
            right: round(left + he.clientWidth - pr),
            bottom: round(top + he.clientHeight - pb),
          },
        };
      }
    }
    const de = document.documentElement;
    return {
      path: '#viewport',
      viewport: true,
      clientWidth: de.clientWidth,
      contentBox: { left: 0, top: 0, right: de.clientWidth, bottom: de.clientHeight },
    };
  };

  const measureEl = (
    el: Element,
    policies: readonly PolicyDef[],
    ctx: string[],
    actionableDef: { tags: readonly string[]; roles: readonly string[] },
    full = false
  ): RawElement => {
    const cs = getComputedStyle(el);
    const he = el as HTMLElement;
    const r = el.getBoundingClientRect();
    const chain: RawChainEntry[] = [];
    for (let a = flatParent(el); a; a = flatParent(a)) {
      const e = chainEntry(a, policies, ctx);
      if (e) chain.push(e);
    }
    chain.push(viewportEntry());
    const srOnly = isSrOnly(el, cs);
    const rendered = el.checkVisibility();
    let hit: RawHit | null = null;
    if (rendered && r.width > 0 && r.height > 0) {
      const cx = r.x + r.width / 2;
      const cy = r.y + r.height / 2;
      const inViewport = cx >= 0 && cy >= 0 && cx < window.innerWidth && cy < window.innerHeight;
      const at = inViewport ? deepPoint(cx, cy) : null;
      hit = {
        cx: round(cx),
        cy: round(cy),
        inViewport,
        hitPath: at ? deepPath(at) : null,
        ok: !!at && composedContains(el, at),
      };
    }
    return {
      path: deepPath(el),
      tag: el.localName,
      idAttr: el.getAttribute('id'),
      classes: typeof el.className === 'string' ? el.className : '',
      role: el.getAttribute('role'),
      rendered,
      // Both option spellings (Chromium accepts the legacy and current names).
      visible: el.checkVisibility({
        opacityProperty: true,
        visibilityProperty: true,
        checkOpacity: true,
        checkVisibilityCSS: true,
      } as CheckVisibilityOptions),
      display: cs.display,
      visibility: cs.visibility,
      opacity: cs.opacity,
      position: cs.position,
      box: boxOf(r),
      text: (el.textContent ?? '').replace(/\s+/g, ' ').trim(),
      innerText: el instanceof HTMLElement ? el.innerText : null,
      title: el.getAttribute('title'),
      ariaLabel: el.getAttribute('aria-label'),
      scrollWidth: he.scrollWidth,
      clientWidth: he.clientWidth,
      scrollHeight: he.scrollHeight,
      clientHeight: he.clientHeight,
      overflowX: cs.overflowX,
      overflowY: cs.overflowY,
      textOverflow: cs.textOverflow,
      whiteSpace: cs.whiteSpace,
      overflowWrap: cs.overflowWrap,
      srOnly,
      hideMobile: el.classList.contains('hide-mobile'),
      actionable: isActionable(el, actionableDef),
      policies: policiesFor(el, cs, policies, ctx),
      chain,
      hit,
      clipC: clipC(el),
      style: full ? fullStyle(cs) : styleVector(cs),
    };
  };

  const resolveWithin = (within: string | undefined): ParentNode[] => {
    if (!within) return [document];
    return deepAll(document, within);
  };

  if (req.op === 'ready') {
    const pages = deepAll(document, req.pageSelector);
    const page = pages[0] ?? null;
    const spinners = page ? deepAll(page, 'sl-spinner').length : null;
    const count = page && req.countCss ? deepAll(page, req.countCss).length : null;
    return {
      url: location.pathname + location.search,
      pagePresent: !!page,
      pageTag: page?.localName ?? null,
      spinnersInPage: spinners,
      count,
    };
  }

  if (req.op === 'frame-stable') {
    const sample = () =>
      JSON.stringify(
        req.cssList.map((sel) =>
          deepAll(document, sel).map((e) => {
            const r = e.getBoundingClientRect();
            return [round(r.x), round(r.y), round(r.width), round(r.height)];
          })
        )
      );
    return new Promise((resolve) => {
      let prev = sample();
      let same = 0;
      let frames = 0;
      const tick = () => {
        frames++;
        const cur = sample();
        same = cur === prev ? same + 1 : 0;
        prev = cur;
        if (same >= 2) resolve({ stable: true, frames, fingerprint: cur.length });
        else if (frames >= req.maxFrames)
          resolve({ stable: false, frames, fingerprint: cur.length });
        else requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
    });
  }

  if (req.op === 'measure') {
    const elements: Record<string, RawElement[]> = {};
    for (const q of req.queries) {
      const roots = resolveWithin(q.within);
      let found: Element[] = [];
      for (const root of roots) {
        if (q.op === 'actionable') {
          const all = allDeep(root);
          if (root instanceof Element && root.shadowRoot) allDeep(root.shadowRoot, all);
          found.push(...all.filter((e) => isActionable(e, req.actionable)));
        } else {
          found.push(...deepAll(root, q.css));
        }
      }
      found = Array.from(new Set(found));
      if (q.op === 'one') found = found.slice(0, 1);
      elements[q.key] = found.map((e) =>
        measureEl(e, req.policies, req.policyContext, req.actionable)
      );
    }
    let overflow: RawOverflowEntry[] | null = null;
    let skipped = 0;
    if (req.overflowScan) {
      overflow = [];
      const all = [document.documentElement, ...allDeep(document)];
      for (const el of all) {
        const cs = getComputedStyle(el);
        if (cs.overflowX === 'visible') continue;
        const rendered = el.checkVisibility();
        if (!rendered) {
          skipped++;
          continue;
        }
        const he = el as HTMLElement;
        overflow.push({
          path: deepPath(el),
          tag: el.localName,
          rendered,
          visibility: cs.visibility,
          overflowX: cs.overflowX,
          overflowY: cs.overflowY,
          scrollWidth: he.scrollWidth,
          clientWidth: he.clientWidth,
          scrollHeight: he.scrollHeight,
          clientHeight: he.clientHeight,
          srOnly: isSrOnly(el, cs),
          policies: policiesFor(el, cs, req.policies, req.policyContext),
          hideMobile: el.classList.contains('hide-mobile'),
        });
      }
    }
    const de = document.documentElement;
    const se = document.scrollingElement ?? de;
    const hcs = getComputedStyle(de);
    const bcs = getComputedStyle(document.body);
    const result: MeasureResult = {
      at: performance.now(),
      url: location.pathname + location.search,
      innerWidth: window.innerWidth,
      innerHeight: window.innerHeight,
      devicePixelRatio: window.devicePixelRatio,
      doc: {
        scrollWidth: de.scrollWidth,
        scrollHeight: de.scrollHeight,
        clientWidth: de.clientWidth,
        clientHeight: de.clientHeight,
        scrollX: se.scrollLeft,
        scrollY: se.scrollTop,
        htmlOverflowX: hcs.overflowX,
        htmlOverflowY: hcs.overflowY,
        bodyOverflowX: bcs.overflowX,
        bodyOverflowY: bcs.overflowY,
      },
      elements,
      overflow,
      overflowSkippedUnrendered: skipped,
    };
    return result;
  }

  if (req.op === 'active') {
    let a: Element | null = document.activeElement;
    while (a && a.shadowRoot && a.shadowRoot.activeElement) a = a.shadowRoot.activeElement;
    if (!a || a === document.body || a === document.documentElement) {
      return {
        outside: true,
        tag: a ? a.localName : null,
        element: null,
        innerWidth: window.innerWidth,
        innerHeight: window.innerHeight,
      };
    }
    // Rev 8 A-F1: CSS animations/transitions in play state `running` on
    // the focused element or any flat-tree ancestor at the time F is sampled.
    const runningAnimations: Array<{ path: string; kind: string; name: string | null }> = [];
    for (let e: Element | null = a; e; e = flatParent(e)) {
      for (const an of e.getAnimations()) {
        if (an.playState !== 'running') continue;
        const x = an as Animation & { animationName?: string; transitionProperty?: string };
        runningAnimations.push({
          path: deepPath(e),
          kind: an.constructor?.name ?? 'Animation',
          name: x.animationName ?? x.transitionProperty ?? (an.id || null),
        });
      }
    }
    return {
      outside: false,
      innerWidth: window.innerWidth,
      innerHeight: window.innerHeight,
      tag: a.localName,
      element: measureEl(a, req.policies, req.policyContext, req.actionable, true),
      runningAnimations,
      // Rev 8: F for every sampled node (element, ::before/::after, flat-tree ancestors).
      focusNodes: sampleNodes(a),
    };
  }

  if (req.op === 'focus-baseline') {
    // Rev 8 A-F1: unfocused full computed style of every sampled node of every
    // focusable element — the element, its ::before/::after, and every
    // flat-tree ancestor — keyed by node (path, path::before, path::after),
    // captured before the first key press in the same context.
    const out: Record<string, Record<string, string>> = {};
    for (const el of allDeep(document)) {
      if ((el as HTMLElement).tabIndex >= 0 || el.localName === 'a') sampleNodes(el, out);
    }
    return out;
  }

  if (req.op === 'nav') {
    // A-N2 raw: every a.nav-link under `within`, its label text, enclosing
    // sl-tooltip content, and whether its scion-nav is collapsed.
    const out: Array<{
      path: string;
      rendered: boolean;
      href: string | null;
      labelText: string | null;
      tooltipContent: string | null;
      collapsed: boolean;
    }> = [];
    for (const root of resolveWithin(req.within)) {
      for (const a of deepAll(root, 'a.nav-link')) {
        let tip: Element | null = null;
        for (let p = flatParent(a); p; p = flatParent(p)) {
          if (p.localName === 'sl-tooltip') {
            tip = p;
            break;
          }
          if (p.localName === 'scion-nav') break;
        }
        const label = a.querySelector('.nav-link-text');
        const nav = hostOf(a);
        out.push({
          path: deepPath(a),
          rendered: a.checkVisibility(),
          href: a.getAttribute('href'),
          labelText: label ? (label.textContent ?? '').trim() : null,
          tooltipContent: tip ? tip.getAttribute('content') : null,
          collapsed: !!nav && nav.hasAttribute('collapsed'),
        });
      }
    }
    return out;
  }

  if (req.op === 'path') {
    return target ? deepPath(target) : null;
  }

  if (req.op === 'programmatic-scroll') {
    // LABELLED geometry-positioning fallback (§2b): only when wheel input
    // cannot move the named scroller. Never reachability evidence.
    let el: Element | null = null;
    if (req.name === 'document') el = document.scrollingElement;
    else
      el =
        deepAll(document, req.name).find(
          (e) => policiesFor(e, getComputedStyle(e), req.policies, req.policyContext).length > 0
        ) ?? null;
    if (!el) return { moved: false, reason: 'scroller not found' };
    const before = { left: el.scrollLeft, top: el.scrollTop };
    el.scrollBy(req.dx, req.dy);
    return {
      moved: el.scrollLeft !== before.left || el.scrollTop !== before.top,
      before,
      after: { left: el.scrollLeft, top: el.scrollTop },
    };
  }

  if (req.op === 'scrollers') {
    const out: Record<
      string,
      {
        path: string;
        scrollLeft: number;
        scrollTop: number;
        scrollWidth: number;
        scrollHeight: number;
        clientWidth: number;
        clientHeight: number;
        box: RawBox;
      } | null
    > = {};
    for (const name of req.names) {
      let el: Element | null = null;
      if (name === 'document') {
        const se = document.scrollingElement ?? document.documentElement;
        out[name] = {
          path: '#viewport',
          scrollLeft: se.scrollLeft,
          scrollTop: se.scrollTop,
          scrollWidth: se.scrollWidth,
          scrollHeight: se.scrollHeight,
          clientWidth: document.documentElement.clientWidth,
          clientHeight: document.documentElement.clientHeight,
          box: boxOf(
            new DOMRect(
              0,
              0,
              document.documentElement.clientWidth,
              document.documentElement.clientHeight
            )
          ),
        };
        continue;
      }
      if (name === 'sl-tab-group::part(nav)') {
        el =
          deepAll(document, '[part~="nav"]').find((e) => hostOf(e)?.localName === 'sl-tab-group') ??
          null;
      } else {
        // Named scroller = the element carrying the declaring policy.
        el =
          deepAll(document, name).find(
            (e) => policiesFor(e, getComputedStyle(e), req.policies, req.policyContext).length > 0
          ) ?? null;
      }
      if (!el) {
        out[name] = null;
        continue;
      }
      const he = el as HTMLElement;
      out[name] = {
        path: deepPath(el),
        scrollLeft: he.scrollLeft,
        scrollTop: he.scrollTop,
        scrollWidth: he.scrollWidth,
        scrollHeight: he.scrollHeight,
        clientWidth: he.clientWidth,
        clientHeight: he.clientHeight,
        box: boxOf(el.getBoundingClientRect()),
      };
    }
    return out;
  }

  return null;
}
