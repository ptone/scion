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
 * Geometry probes evaluated inside the page. They walk open shadow roots
 * (Lit/Shoelace) because document-level metrics alone miss overflow inside
 * components. All coordinates are CSS pixels in the layout viewport.
 *
 * The functions passed to page.evaluate must be self-contained.
 */

import type { Page } from '@playwright/test';

/** Geometry rounding tolerance (design: 1 CSS px). */
export const TOLERANCE_PX = 1;

export interface Box {
  x: number;
  y: number;
  width: number;
  height: number;
  right: number;
  bottom: number;
}

export interface DocumentMetrics {
  innerWidth: number;
  innerHeight: number;
  devicePixelRatio: number;
  docScrollWidth: number;
  docClientWidth: number;
  bodyScrollWidth: number;
  overflowingScrollContainers: Array<{
    path: string;
    scrollWidth: number;
    clientWidth: number;
    overflowX: string;
  }>;
  clippedOverflow: Array<{ path: string; scrollWidth: number; clientWidth: number }>;
  offViewportElements: Array<{ path: string; right: number }>;
}

export interface RowMetrics {
  key: string;
  found: boolean;
  link: Box | null;
  linkTruncated: boolean | null;
  linkHitTestOk: boolean | null;
  badge: Box | null;
  badgeHitTestOk: boolean | null;
  clipContainer: Box | null;
  row: Box | null;
}

/** Document + deep shadow-root overflow scan. */
export async function measureDocument(page: Page): Promise<DocumentMetrics> {
  return page.evaluate(() => {
    const MAX = 40;
    const describe = (el: Element): string => {
      const parts: string[] = [];
      let cur: Element | null = el;
      for (let i = 0; cur && i < 6; i++) {
        const cls =
          typeof cur.className === 'string' && cur.className.trim()
            ? '.' + cur.className.trim().split(/\s+/).slice(0, 2).join('.')
            : '';
        parts.unshift(cur.tagName.toLowerCase() + cls);
        const parent: Element | null = cur.parentElement;
        cur = parent ?? (cur.getRootNode() as ShadowRoot).host ?? null;
      }
      return parts.join(' > ');
    };
    const all: Element[] = [];
    const walk = (root: Document | ShadowRoot) => {
      for (const el of Array.from(root.querySelectorAll('*'))) {
        all.push(el);
        if (el.shadowRoot) walk(el.shadowRoot);
      }
    };
    walk(document);

    const overflowingScrollContainers: DocumentMetrics['overflowingScrollContainers'] = [];
    const clippedOverflow: DocumentMetrics['clippedOverflow'] = [];
    const offViewportElements: DocumentMetrics['offViewportElements'] = [];
    const vw = window.innerWidth;
    for (const el of all) {
      const cs = getComputedStyle(el);
      if (cs.display === 'none' || cs.visibility === 'hidden') continue;
      const he = el as HTMLElement;
      if (he.scrollWidth > he.clientWidth + 1 && he.clientWidth > 0) {
        if (cs.overflowX === 'auto' || cs.overflowX === 'scroll') {
          if (overflowingScrollContainers.length < MAX) {
            overflowingScrollContainers.push({
              path: describe(el),
              scrollWidth: he.scrollWidth,
              clientWidth: he.clientWidth,
              overflowX: cs.overflowX,
            });
          }
        } else if (cs.overflowX === 'hidden' || cs.overflowX === 'clip') {
          // Text ellipsis on a single inline run is expected; record block clipping.
          if (cs.textOverflow !== 'ellipsis' && clippedOverflow.length < MAX) {
            clippedOverflow.push({
              path: describe(el),
              scrollWidth: he.scrollWidth,
              clientWidth: he.clientWidth,
            });
          }
        }
      }
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.height > 0 && r.right > vw + 1 && offViewportElements.length < MAX) {
        offViewportElements.push({ path: describe(el), right: Math.round(r.right * 100) / 100 });
      }
    }
    return {
      innerWidth: window.innerWidth,
      innerHeight: window.innerHeight,
      devicePixelRatio: window.devicePixelRatio,
      docScrollWidth: document.documentElement.scrollWidth,
      docClientWidth: document.documentElement.clientWidth,
      bodyScrollWidth: document.body.scrollWidth,
      overflowingScrollContainers,
      clippedOverflow,
      offViewportElements,
    };
  });
}

/**
 * Measure one fixture row identified by its group id (the row's name link
 * points at /admin/groups/<id>). Hit tests resolve through shadow roots.
 */
export async function measureRow(page: Page, key: string, groupId: string): Promise<RowMetrics> {
  return page.evaluate(
    ({ key, groupId }) => {
      const box = (r: DOMRect) => ({
        x: r.x,
        y: r.y,
        width: r.width,
        height: r.height,
        right: r.right,
        bottom: r.bottom,
      });
      const findDeep = (root: Document | ShadowRoot, sel: string): Element | null => {
        const hit = root.querySelector(sel);
        if (hit) return hit;
        for (const el of Array.from(root.querySelectorAll('*'))) {
          if (el.shadowRoot) {
            const r = findDeep(el.shadowRoot, sel);
            if (r) return r;
          }
        }
        return null;
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
      const hits = (target: Element, r: DOMRect) => {
        const vw = window.innerWidth;
        const vh = window.innerHeight;
        const cx = r.x + r.width / 2;
        const cy = r.y + r.height / 2;
        if (cx < 0 || cy < 0 || cx > vw || cy > vh) return false;
        const at = deepPoint(cx, cy);
        return !!at && (at === target || target.contains(at));
      };
      const href = `/admin/groups/${encodeURIComponent(groupId)}`;
      const link = findDeep(document, `a.group-name-link[href="${href}"]`) as HTMLElement | null;
      if (!link) {
        return {
          key,
          found: false,
          link: null,
          linkTruncated: null,
          linkHitTestOk: null,
          badge: null,
          badgeHitTestOk: null,
          clipContainer: null,
          row: null,
        };
      }
      const row = link.closest('tr');
      const badge = row?.querySelector('.type-badge') ?? null;
      // nearest ancestor that clips horizontally
      let clip: Element | null = link.parentElement;
      while (clip) {
        const ox = getComputedStyle(clip).overflowX;
        if (ox === 'hidden' || ox === 'clip' || ox === 'auto' || ox === 'scroll') break;
        clip = clip.parentElement;
      }
      const lr = link.getBoundingClientRect();
      const br = badge?.getBoundingClientRect() ?? null;
      return {
        key,
        found: true,
        link: box(lr),
        linkTruncated: link.scrollWidth > link.clientWidth + 1,
        linkHitTestOk: hits(link, lr),
        badge: br ? box(br) : null,
        badgeHitTestOk: badge && br ? hits(badge, br) : null,
        clipContainer: clip ? box(clip.getBoundingClientRect()) : null,
        row: row ? box(row.getBoundingClientRect()) : null,
      };
    },
    { key, groupId }
  );
}

export interface AssertionOutcome {
  id: string;
  description: string;
  outcome: 'pass' | 'fail' | 'not-applicable';
  details: unknown;
}

/** LS-A1: page does not scroll horizontally. */
export function assertNoDocumentOverflow(m: DocumentMetrics): AssertionOutcome {
  const overflow = m.docScrollWidth - m.innerWidth;
  return {
    id: 'LS-A1-no-document-horizontal-overflow',
    description: `documentElement.scrollWidth <= innerWidth + ${TOLERANCE_PX}px`,
    outcome: overflow <= TOLERANCE_PX ? 'pass' : 'fail',
    details: { docScrollWidth: m.docScrollWidth, innerWidth: m.innerWidth, overflowPx: overflow },
  };
}

const within = (inner: Box, outer: { x: number; right: number }) =>
  inner.x >= outer.x - TOLERANCE_PX && inner.right <= outer.right + TOLERANCE_PX;

/** LS-A2: each fixture row's Type badge is fully visible (viewport + clip ancestor) and hit-testable. */
export function assertBadgesVisible(rows: RowMetrics[], innerWidth: number): AssertionOutcome {
  const failures = rows
    .filter((r) => {
      if (!r.found || !r.badge) return true;
      const vp = { x: 0, right: innerWidth };
      return (
        !within(r.badge, vp) ||
        (r.clipContainer && !within(r.badge, r.clipContainer)) ||
        r.badgeHitTestOk !== true
      );
    })
    .map((r) => r.key);
  return {
    id: 'LS-A2-type-badge-unclipped',
    description: `every fixture row's .type-badge lies within the viewport and its clipping ancestor (±${TOLERANCE_PX}px) and its centre hit-tests to itself`,
    outcome: failures.length === 0 && rows.length > 0 ? 'pass' : 'fail',
    details: { failingRows: failures },
  };
}

/** LS-A3: each fixture row's name link is on-screen horizontally and hit-testable (not occluded). */
export function assertLinksReachable(rows: RowMetrics[], innerWidth: number): AssertionOutcome {
  const failures = rows
    .filter(
      (r) =>
        !r.found ||
        !r.link ||
        r.link.x < -TOLERANCE_PX ||
        r.link.x > innerWidth ||
        r.linkHitTestOk !== true
    )
    .map((r) => r.key);
  return {
    id: 'LS-A3-name-link-reachable',
    description:
      'every fixture row name link starts inside the viewport and its centre hit-tests to the link',
    outcome: failures.length === 0 && rows.length > 0 ? 'pass' : 'fail',
    details: { failingRows: failures },
  };
}
