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
 * W01-S01 / W01-S02 adapter (/admin/groups default and fixture filter).
 * Groups-table clauses are the pilot finding rev 2 B-* definitions with
 * fixture IDs bound from Wave01 in-batch readback; shell clauses and A-F1
 * follow contract FROZEN rev 2.
 */

import { AF1_N, NAV_TIMEOUT_MS, TOL, type ProfileId } from './contract.js';
import {
  clip,
  clipAndHit,
  evalAC1,
  evalAD1,
  evalAF1,
  evalAN2,
  evalAS1,
  evalAS2,
  evalBA1,
  evalBA2,
  evalBA3,
  evalBC1,
  evalBOVR,
  hitTest,
  resolveObservations,
  type ClauseResult,
  type FixtureRowObs,
  type Observation,
  type Outcome,
  type PressRecord,
} from './evaluate.js';
import type { StateDef, TargetDef } from './manifest.js';
import { probe, type MeasureResult, type ProbeQuery, type RawElement } from './probe.js';
import { sha256 } from './release.mjs';
import {
  CaptureError,
  accessibleName,
  fingerprint,
  measure,
  navRaw,
  navigate,
  now,
  pe,
  pointerClick,
  positionStep,
  scrollerForPolicy,
  screenshot,
  twoFrames,
  waitR0,
  waitStable,
  writeRaw,
  type PositionStep,
  type Profile,
  type ScrollerName,
  type SubstepCtx,
} from './runner-lib.js';
import { ACTIONABLE, POLICIES } from './contract.js';

export const PAGE_SIZE = 25; // admin-groups.ts:51 (UI's default list request limit)
const ROW_LINK = 'a.group-name-link';

export interface GroupsFixtureMap {
  id: string;
  searchTerm: string;
  resources: Array<{
    key: string;
    id: string;
    slug: string;
    name: string;
    description: string;
    labels: Record<string, string>;
  }>;
}

export interface Readback {
  endpoint: string;
  status: number;
  at: string;
  principalId: string;
  bodySha256: string | null;
  ok: boolean;
  /** Row ids returned (page order). */
  ids: string[];
  /** Fixture rows bound from readback by slug (id/name from the API). */
  fixture: Array<{ key: string; id: string; name: string; slug: string }>;
  problems: string[];
}

interface ApiGroup {
  id: string;
  name: string;
  slug: string;
  description?: string;
  labels?: Record<string, string>;
}

const sortKeys = (o: Record<string, string>) =>
  JSON.stringify(Object.fromEntries(Object.entries(o).sort(([a], [b]) => (a < b ? -1 : 1))));

/**
 * In-batch API readback for the state's own filter/scope (§2a C-FX3).
 * S01 = the UI's default request (limit PAGE_SIZE); S02 = search=<tag>.
 * Fixture rows are bound by slug and must equal the recipe content; the
 * steward map's ids must equal the readback ids (drift ⇒ capture error).
 */
export async function readback(
  baseURL: string,
  token: string,
  principalId: string,
  state: StateDef,
  map: GroupsFixtureMap
): Promise<Readback> {
  const q =
    state.id === 'W01-S02'
      ? `search=${encodeURIComponent(map.searchTerm)}&limit=${PAGE_SIZE}`
      : `limit=${PAGE_SIZE}`;
  const endpoint = `/api/v1/groups?${q}`;
  const at = now();
  const res = await fetch(`${baseURL}${endpoint}`, {
    headers: { Authorization: `Bearer ${token}` },
    redirect: 'manual',
  });
  const rb: Readback = {
    endpoint,
    status: res.status,
    at,
    principalId,
    bodySha256: null,
    ok: false,
    ids: [],
    fixture: [],
    problems: [],
  };
  if (!res.ok) {
    rb.problems.push(`HTTP ${res.status}`);
    return rb;
  }
  const bytes = Buffer.from(await res.arrayBuffer());
  rb.bodySha256 = sha256(bytes);
  const groups = ((JSON.parse(bytes.toString('utf-8')) as { groups?: ApiGroup[] }).groups ??
    []) as ApiGroup[];
  rb.ids = groups.map((g) => g.id);
  // Fixture binding is always from the fixture-filter readback (all three rows).
  let all = groups;
  if (state.id !== 'W01-S02') {
    const r2 = await fetch(
      `${baseURL}/api/v1/groups?search=${encodeURIComponent(map.searchTerm)}&limit=${PAGE_SIZE}`,
      {
        headers: { Authorization: `Bearer ${token}` },
        redirect: 'manual',
      }
    );
    if (!r2.ok) {
      rb.problems.push(`fixture readback HTTP ${r2.status}`);
      return rb;
    }
    all = ((await r2.json()) as { groups?: ApiGroup[] }).groups ?? [];
  }
  for (const r of map.resources) {
    const g = all.find((x) => x.slug === r.slug);
    if (!g) {
      rb.problems.push(`fixture ${r.key} (slug ${r.slug}) missing from readback`);
      continue;
    }
    if (g.id !== r.id)
      rb.problems.push(`fixture ${r.key}: readback id differs from steward map id (drift)`);
    if (
      g.name !== r.name ||
      (g.description ?? '') !== r.description ||
      sortKeys(g.labels ?? {}) !== sortKeys(r.labels)
    )
      rb.problems.push(`fixture ${r.key}: readback content differs from recipe`);
    rb.fixture.push({ key: r.key, id: g.id, name: g.name, slug: g.slug });
  }
  if (state.id === 'W01-S02') {
    const extra = rb.ids.filter((id) => !rb.fixture.some((f) => f.id === id));
    if (extra.length)
      rb.problems.push(
        `fixture-filter readback has ${extra.length} non-fixture rows (state not deterministic)`
      );
  }
  rb.ok = rb.problems.length === 0 && rb.fixture.length === map.resources.length;
  return rb;
}

export function routeFor(state: StateDef, map: GroupsFixtureMap): string {
  return state.id === 'W01-S02'
    ? `/admin/groups?q=${encodeURIComponent(map.searchTerm)}`
    : '/admin/groups';
}

const linkCss = (id: string) => `${ROW_LINK}[href="/admin/groups/${encodeURIComponent(id)}"]`;
const badgeCss = (id: string) => `tr:has(${linkCss(id)}) .type-badge`;

function shellQueries(): ProbeQuery[] {
  return [
    { key: 'sidebar', op: 'one', css: '.sidebar', within: 'scion-app-shell' },
    { key: 'content', op: 'one', css: '.content', within: 'scion-app-shell' },
    { key: 'header', op: 'one', css: 'scion-header' },
    { key: 'menuBtn', op: 'one', css: '.mobile-menu-btn', within: 'scion-header' },
    { key: 'headerTargets', op: 'actionable', within: 'scion-header' },
  ];
}

function stateQueries(state: StateDef, rb: Readback): ProbeQuery[] {
  const q: ProbeQuery[] = [
    { key: 'table', op: 'one', css: 'table[aria-label="Groups"]', within: '[data-scion-page]' },
    {
      key: 'hiddenColumns',
      op: 'all',
      css: 'th.hide-mobile, td.hide-mobile',
      within: '[data-scion-page]',
    },
  ];
  if (state.id === 'W01-S02') {
    for (const f of rb.fixture) {
      q.push({ key: `link:${f.key}`, op: 'one', css: linkCss(f.id), within: '[data-scion-page]' });
      q.push({
        key: `badge:${f.key}`,
        op: 'one',
        css: badgeCss(f.id),
        within: '[data-scion-page]',
      });
    }
  }
  return q;
}

const GRADED = ['scion-header', '[data-scion-page] table', `[data-scion-page] ${ROW_LINK}`];

async function r0(ctx: SubstepCtx, state: StateDef, rb: Readback): Promise<void> {
  const expected = state.id === 'W01-S02' ? rb.fixture.length : rb.ids.length;
  await waitR0(ctx, {
    countCss: ROW_LINK,
    expectCount: (n) => n === expected,
    graded: GRADED,
    label: `${state.id} rows==${expected} (readback)`,
  });
}

/** Elements whose positional clauses are not yet resolved at the current offsets. */
function pendingScrollers(m: MeasureResult, keys: string[]): ScrollerName[] {
  const out = new Set<ScrollerName>();
  for (const k of keys) {
    for (const el of m.elements[k] ?? []) {
      const c = clip(el);
      if (c.status !== 'pending') continue;
      // innermost declared scroller that is pending on some axis
      const a = c.ancestors.find((x) => x.status === 'pending' && x.policy);
      const name = a?.policy ? scrollerForPolicy(a.policy) : null;
      if (name) out.add(name);
    }
  }
  return [...out];
}

function obs(
  m: MeasureResult,
  key: string,
  step: string,
  method: Observation['method']
): Observation[] {
  const el = m.elements[key]?.[0];
  return el ? [{ step, method, innerWidth: m.innerWidth, el }] : [];
}

/** Resolve a shell clause across steps: first non-pending result wins; programmatic-only ⇒ inconclusive. */
function resolveClause(
  steps: Array<{ step: string; method: Observation['method']; r: ClauseResult }>
): ClauseResult {
  const first = steps[0]!.r;
  for (const s of steps) {
    if (s.r.outcome === 'pending') continue;
    if (s.method === 'programmatic' && s.r.outcome === 'pass')
      return {
        ...s.r,
        outcome: 'inconclusive',
        details: {
          decidedAt: s.step,
          via: 'programmatic positioning only',
          trail: steps.map((x) => ({ step: x.step, outcome: x.r.outcome })),
        },
      };
    return {
      ...s.r,
      details: {
        decidedAt: s.step,
        result: s.r.details,
        trail: steps.map((x) => ({ step: x.step, outcome: x.r.outcome })),
      },
    };
  }
  return {
    ...first,
    outcome: 'fail',
    details: {
      reason: 'never brought into view',
      trail: steps.map((x) => ({ step: x.step, outcome: x.r.outcome })),
    },
  };
}

export interface M0Result {
  outcomes: ClauseResult[];
  positioning: Array<{ k: number; step: PositionStep; files: string[] }>;
  loadedScripts: SubstepCtx['scripts'];
  loadedMainEntry: { url: string; sha256: string | null } | null;
  nav: unknown;
}

export async function runM0(
  ctx: SubstepCtx,
  state: StateDef,
  profile: Profile,
  rb: Readback,
  route: string
): Promise<M0Result> {
  await navigate(ctx, route);
  await r0(ctx, state, rb);
  // No measured interactions for S01/S02 (§4a).
  const queries = [...shellQueries(), ...stateQueries(state, rb)];
  const fpBefore = await fingerprint(ctx.page, GRADED);
  const primary = await measure(ctx, queries, true);
  const nav = await navRaw(ctx);
  const accNames: Record<string, string | null> = {};
  for (const f of rb.fixture) {
    if (state.id !== 'W01-S02') break;
    accNames[f.key] = await accessibleName(
      ctx.page.locator(`[data-scion-page] ${linkCss(f.id)}`),
      'link'
    );
  }
  await screenshot(ctx, 'M0-primary.fullpage', true, 'M0-primary-screenshot-fullpage');
  await screenshot(ctx, 'M0-primary.viewport', false, 'M0-primary-screenshot-viewport');
  const fpAfter = await fingerprint(ctx.page, GRADED);
  ctx.readiness.push({
    id: 'M0:one-stability-window (graded boxes unchanged across measurement+screenshot)',
    ok: fpBefore === fpAfter,
    at: now(),
  });
  if (fpBefore !== fpAfter)
    throw new CaptureError('graded layout changed during the M0 stability window');
  writeRaw(
    ctx,
    'M0-primary.measure',
    { offsets: await scrollOffsets(ctx), measure: primary, nav, accessibleNames: accNames },
    'M0-primary-raw'
  );

  // M0-pos-k: positioning steps for pending elements (same context).
  const positional = [
    'menuBtn',
    'headerTargets',
    ...rb.fixture.flatMap((f) => [`link:${f.key}`, `badge:${f.key}`]),
  ];
  const steps: Array<{ step: string; method: Observation['method']; m: MeasureResult }> = [
    { step: 'primary', method: 'initial', m: primary },
  ];
  const positioning: M0Result['positioning'] = [];
  let k = 0;
  for (let guard = 0; guard < 60; guard++) {
    const cur = steps[steps.length - 1]!.m;
    const unresolved = pendingAll(steps, positional);
    if (unresolved.length === 0) break;
    const scrollersNeeded = pendingScrollers(cur, unresolved);
    if (scrollersNeeded.length === 0) break;
    const ps = await positionStep(ctx, scrollersNeeded[0]!, profile.width, profile.height);
    if (!ps) break;
    k++;
    const m = await measure(ctx, queries, false);
    const shot = await screenshot(ctx, `M0-pos-${k}.viewport`, false, 'M0-pos-screenshot');
    const raw = writeRaw(
      ctx,
      `M0-pos-${k}.measure`,
      {
        k,
        scroller: ps.scroller,
        method: ps.method,
        offsets: { before: ps.before, after: ps.after },
        measure: m,
      },
      'M0-pos-raw'
    );
    positioning.push({ k, step: ps, files: [shot.path, raw.path] });
    steps.push({ step: `pos-${k}`, method: ps.method === 'wheel' ? 'wheel' : 'programmatic', m });
    if (
      ps.atEnd &&
      pendingAll(steps, positional).length > 0 &&
      pendingScrollers(m, pendingAll(steps, positional)).every((s) => s === ps.scroller)
    )
      break;
  }

  const outcomes: ClauseResult[] = [];
  // Shell (§2) — A-D1/A-C1/A-N2 are evaluated on the primary state only.
  outcomes.push(evalAD1(primary));
  outcomes.push(evalAC1(primary.overflow));
  outcomes.push(
    resolveClause(
      steps.map((s) => ({
        step: s.step,
        method: s.method,
        r: evalAS1(
          profile.id as ProfileId,
          s.m.innerWidth,
          s.m.elements.sidebar?.[0],
          s.m.elements.content?.[0],
          s.m.elements.menuBtn?.[0]
        ),
      }))
    )
  );
  outcomes.push(
    resolveClause(
      steps.map((s) => ({
        step: s.step,
        method: s.method,
        r: evalAS2(s.m.innerWidth, s.m.elements.header?.[0], s.m.elements.headerTargets ?? []),
      }))
    )
  );
  outcomes.push(evalAN2(nav));
  // Groups table (pilot finding rev 2).
  const bc1 = evalBC1(primary.elements.table?.[0]);
  outcomes.push(bc1, evalBA1(primary));
  if (state.id === 'W01-S02') {
    const rows: FixtureRowObs[] = rb.fixture.map((f) => ({
      key: f.key,
      id: f.id,
      name: f.name,
      accessibleName: accNames[f.key] ?? null,
      link: steps.flatMap((s) => obs(s.m, `link:${f.key}`, s.step, s.method)),
      badge: steps.flatMap((s) => obs(s.m, `badge:${f.key}`, s.step, s.method)),
    }));
    outcomes.push(
      evalBA2(rows),
      evalBA3(rows),
      evalBOVR(bc1, primary.elements.hiddenColumns ?? [])
    );
  }
  const main = ctx.scripts.find((s) => new URL(s.url).pathname === '/assets/main.js') ?? null;
  return {
    outcomes,
    positioning,
    loadedScripts: ctx.scripts,
    loadedMainEntry: main ? { url: main.url, sha256: main.sha256 } : null,
    nav,
  };
}

/** Keys still pending in every observation so far. */
function pendingAll(steps: Array<{ m: MeasureResult }>, keys: string[]): string[] {
  return keys.filter((k) => {
    const all = steps.map((s) => s.m.elements[k] ?? []);
    if (all.every((els) => els.length === 0)) return false;
    // pending if, for some element under this key, no step resolved it
    const n = Math.max(...all.map((e) => e.length));
    for (let i = 0; i < n; i++) {
      const resolved = all.some((els) => els[i] && clipAndHit(els[i]!, 0).status !== 'pending');
      if (!resolved) return true;
    }
    return false;
  });
}

async function scrollOffsets(ctx: SubstepCtx) {
  return pe(ctx.page, {
    op: 'scrollers',
    names: ['document', '.content', '.nav-container'],
    policies: POLICIES,
    policyContext: ctx.policyContext,
  });
}

// ─── M1 keyboard (A-F1) ──────────────────────────────────────────────────

async function resolveTargets(
  ctx: SubstepCtx,
  targets: readonly TargetDef[],
  rb: Readback
): Promise<Record<string, string>> {
  const out: Record<string, string> = {};
  for (const t of targets) {
    let css: string;
    if ('css' in t.locate) css = t.locate.css;
    else {
      // first rendered fixture row link in DOM order
      const m = await measure(
        ctx,
        rb.fixture.map((f) => ({
          key: f.key,
          op: 'one' as const,
          css: linkCss(f.id),
          within: '[data-scion-page]',
        })),
        false
      );
      const rendered = rb.fixture
        .map((f) => m.elements[f.key]?.[0])
        .filter((e): e is RawElement => !!e && e.rendered)
        .sort((a, b) => a.box.y - b.box.y || a.box.x - b.box.x);
      if (!rendered[0])
        throw new CaptureError(
          `declared A-F1 target "${t.label}" not rendered (recorded at readiness; dropping needs a contract revision)`
        );
      out[t.key] = rendered[0].path;
      continue;
    }
    const m = await measure(
      ctx,
      [{ key: 't', op: 'one', css, within: '[data-scion-page]' }],
      false
    );
    const el = m.elements.t?.[0];
    if (!el || !el.rendered)
      throw new CaptureError(
        `declared A-F1 target "${t.label}" (${css}) not rendered (recorded at readiness; dropping needs a contract revision)`
      );
    out[t.key] = el.path;
  }
  ctx.readiness.push({ id: 'M1:declared-targets-rendered', ok: true, at: now(), details: out });
  return out;
}

const reachedBy = (path: string | undefined, targets: Record<string, string>) =>
  path
    ? Object.entries(targets)
        .filter(([, p]) => path === p || path.startsWith(p + '>'))
        .map(([k]) => k)
    : [];

export async function traverse(
  ctx: SubstepCtx,
  direction: 'forward' | 'backward',
  targets: Record<string, string>,
  limit: number
): Promise<{
  presses: PressRecord[];
  baseline: Record<string, Record<string, string>>;
  startPath: string | null;
  startReached: string[];
}> {
  const baseline = (await pe(ctx.page, { op: 'focus-baseline' })) as Record<
    string,
    Record<string, string>
  >;
  const start = (await pe(ctx.page, {
    op: 'active',
    policies: POLICIES,
    policyContext: ctx.policyContext,
    actionable: ACTIONABLE,
  })) as {
    outside: boolean;
    element: RawElement | null;
  };
  const startPath = start.element?.path ?? null;
  const startReached = reachedBy(startPath ?? undefined, targets);
  const remaining = new Set(Object.keys(targets).filter((k) => !startReached.includes(k)));
  const presses: PressRecord[] = [];
  for (let i = 1; i <= limit && (Object.keys(targets).length === 0 || remaining.size > 0); i++) {
    const key = direction === 'forward' ? 'Tab' : 'Shift+Tab';
    await ctx.page.keyboard.press(key);
    ctx.actions.push({ kind: 'key', target: key, detail: { press: i, direction }, at: now() });
    await twoFrames(ctx.page);
    const a = (await pe(ctx.page, {
      op: 'active',
      policies: POLICIES,
      policyContext: ctx.policyContext,
      actionable: ACTIONABLE,
    })) as {
      outside: boolean;
      element: RawElement | null;
      innerWidth: number;
      innerHeight: number;
    };
    const reached = reachedBy(a.element?.path, targets);
    reached.forEach((r) => remaining.delete(r));
    presses.push({
      direction,
      press: i,
      outside: a.outside,
      innerWidth: a.innerWidth,
      innerHeight: a.innerHeight,
      element: a.element,
      reached,
    });
    if (a.outside) break; // rev 2: focus leaving the page ends this direction
  }
  return { presses, baseline, startPath, startReached };
}

export async function runM1Forward(ctx: SubstepCtx, state: StateDef, rb: Readback, route: string) {
  await navigate(ctx, route);
  await r0(ctx, state, rb);
  const targets = await resolveTargets(ctx, state.af1Targets, rb);
  const limit = state.af1Targets.length ? AF1_N : 20;
  const t = await traverse(ctx, 'forward', targets, limit);
  writeRaw(ctx, 'M1-forward.presses', { targets, ...t }, 'M1-raw');
  await screenshot(ctx, 'M1-forward.end.viewport', false, 'M1-screenshot');
  return { targets, ...t };
}

export async function runM1Backward(
  ctx: SubstepCtx,
  state: StateDef,
  rb: Readback,
  route: string,
  targets: Record<string, string>
) {
  await navigate(ctx, route);
  await r0(ctx, state, rb);
  const resolved = await resolveTargets(ctx, state.af1Targets, rb);
  if (JSON.stringify(resolved) !== JSON.stringify(targets))
    throw new CaptureError(
      'A-F1 retry: declared targets resolved differently than in the forward context'
    );
  const t = await traverse(ctx, 'backward', targets, AF1_N);
  writeRaw(ctx, 'M1-backward.presses', { targets, ...t }, 'M1-raw');
  await screenshot(ctx, 'M1-backward.end.viewport', false, 'M1-screenshot');
  return t;
}

export function gradeAF1(
  state: StateDef,
  fwd: Awaited<ReturnType<typeof runM1Forward>>,
  bwd: Awaited<ReturnType<typeof runM1Backward>> | null
): ClauseResult {
  return evalAF1({
    targets: state.af1Targets.map((t) => t.key),
    forward: fwd.presses,
    backward: bwd?.presses ?? null,
    baselineForward: fwd.baseline,
    baselineBackward: bwd?.baseline ?? null,
    startReached: fwd.startReached,
    startBackwardReached: bwd?.startReached ?? [],
  });
}

// ─── M3 row navigation (B-I1) ────────────────────────────────────────────

export async function runM3(
  ctx: SubstepCtx,
  state: StateDef,
  profile: Profile,
  rb: Readback,
  route: string
): Promise<ClauseResult> {
  await navigate(ctx, route);
  await r0(ctx, state, rb);
  const long = rb.fixture.find((f) => f.key === 'long');
  if (!long) throw new CaptureError('readback has no "long" fixture row');
  const q: ProbeQuery[] = [
    { key: 'link', op: 'one', css: linkCss(long.id), within: '[data-scion-page]' },
  ];
  let m = await measure(ctx, q, false);
  const trail: unknown[] = [];
  for (let i = 0; i < 40; i++) {
    const el = m.elements.link?.[0];
    if (!el) throw new CaptureError('long-name link not rendered');
    const c = clip(el);
    trail.push({ i, clip: c.status, hit: el.hit });
    if (c.status !== 'pending') break;
    const a = c.ancestors.find((x) => x.status === 'pending' && x.policy);
    const name = a?.policy ? scrollerForPolicy(a.policy) : null;
    if (!name) break;
    const ps = await positionStep(ctx, name, profile.width, profile.height);
    if (!ps) break;
    trail.push({
      positioning: ps.method,
      scroller: name,
      after: { top: ps.after.scrollTop, left: ps.after.scrollLeft },
    });
    m = await measure(ctx, q, false);
  }
  const el = m.elements.link![0]!;
  writeRaw(ctx, 'M3.pre-click', { measure: m, trail }, 'M3-raw');
  const usedProgrammatic = trail.some(
    (t) => (t as { positioning?: string }).positioning === 'programmatic'
  );
  const h = hitTest(el);
  if (h !== 'pass') {
    return {
      clause: 'B-I1',
      source: 'pilot-finding-rev2',
      outcome: 'fail',
      policyIds: [],
      details: { reason: 'long-name link not hit-testable; no click sent', hit: el.hit, trail },
    };
  }
  const expected = `/admin/groups/${long.id}`;
  const t0 = now();
  await pointerClick(ctx, el, 'long-name row link');
  let navOk = false;
  try {
    await ctx.page.waitForURL(
      (u) =>
        u.pathname === expected || u.pathname === `/admin/groups/${encodeURIComponent(long.id)}`,
      { timeout: NAV_TIMEOUT_MS }
    );
    navOk = true;
  } catch {
    navOk = false;
  }
  await screenshot(ctx, 'M3.after-click.viewport', false, 'M3-screenshot');
  const outcome: Outcome = navOk ? (usedProgrammatic ? 'inconclusive' : 'pass') : 'fail';
  return {
    clause: 'B-I1',
    source: 'pilot-finding-rev2',
    outcome,
    policyIds: [],
    details: {
      expected,
      finalPath: new URL(ctx.page.url()).pathname,
      clickedAt: t0,
      timeoutMs: NAV_TIMEOUT_MS,
      positioningUsedProgrammatic: usedProgrammatic,
      trail,
    },
  };
}

export { waitStable, TOL };
