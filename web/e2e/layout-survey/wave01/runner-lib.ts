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
 * Playwright-side Wave01 runner primitives. Inputs are only native pointer,
 * wheel and keyboard events (CDP Input.*). There is no locator.click()
 * auto-scroll, no element.focus(), no scrollIntoView and no route
 * interception. Every action is appended to the substep's action log.
 */

import type { Browser, BrowserContext, Page } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import {
  ACTIONABLE,
  APP_SHELL_TAG,
  ENVIRONMENT,
  NAMED_SCROLLERS,
  POLICIES,
  type ProfileId,
} from './contract.js';
import {
  probe,
  type MeasureResult,
  type ProbeQuery,
  type ProbeRequest,
  type RawElement,
} from './probe.js';
import type { FocusBaseline, NavRaw } from './evaluate.js';
import { sha256 } from './release.mjs';

export interface Profile {
  id: ProfileId;
  width: number;
  height: number;
}

export interface Action {
  kind: 'navigate' | 'pointer-click' | 'wheel' | 'key' | 'programmatic-scroll' | 'pointer-move';
  target?: string;
  detail?: unknown;
  at: string;
  outcome?: string;
}

export interface ReadyCheck {
  id: string;
  ok: boolean;
  at: string;
  details?: unknown;
}

export interface FileEntry {
  kind: string;
  path: string;
  sha256: string;
}

export class CaptureError extends Error {}

export interface SubstepCtx {
  context: BrowserContext;
  page: Page;
  actions: Action[];
  readiness: ReadyCheck[];
  files: FileEntry[];
  consoleErrors: string[];
  failedRequests: Array<{ path: string; status: number }>;
  scripts: Array<{ url: string; status: number; sha256: string | null }>;
  /** Every request URL path, for the no-mutation safety check. */
  mutatingRequests: Array<{ method: string; path: string }>;
  runDir: string;
  prefix: string;
  policyContext: string[];
}

export const now = () => new Date().toISOString();

/** Typed page.evaluate(probe, req). */
export const pe = <T = unknown>(page: Page, req: ProbeRequest): Promise<T> =>
  page.evaluate(probe, req) as Promise<T>;

/** Fresh context per (state, profile, substep) (§1, §2b). */
export async function openSubstep(
  browser: Browser,
  opts: {
    baseURL: string;
    storageStatePath: string;
    profile: Profile;
    runDir: string;
    prefix: string;
    policyContext: string[];
  }
): Promise<SubstepCtx> {
  const context = await browser.newContext({
    baseURL: opts.baseURL,
    storageState: opts.storageStatePath,
    viewport: { width: opts.profile.width, height: opts.profile.height },
    deviceScaleFactor: ENVIRONMENT.deviceScaleFactor,
    colorScheme: ENVIRONMENT.colorScheme,
    locale: ENVIRONMENT.locale,
    timezoneId: ENVIRONMENT.timezoneId,
    isMobile: false,
    hasTouch: false,
  });
  const page = await context.newPage();
  const ctx: SubstepCtx = {
    context,
    page,
    actions: [],
    readiness: [],
    files: [],
    consoleErrors: [],
    failedRequests: [],
    scripts: [],
    mutatingRequests: [],
    runDir: opts.runDir,
    prefix: opts.prefix,
    policyContext: opts.policyContext,
  };
  page.on('console', (m) => {
    if (m.type() === 'error' && ctx.consoleErrors.length < 30)
      ctx.consoleErrors.push(m.text().slice(0, 300));
  });
  // Context-wide (popups/new pages included; review2 N-e).
  context.on('request', (r) => {
    const method = r.method();
    if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
      ctx.mutatingRequests.push({ method, path: new URL(r.url()).pathname });
    }
  });
  context.on('response', async (r) => {
    if (r.status() >= 400 && ctx.failedRequests.length < 30)
      ctx.failedRequests.push({ path: new URL(r.url()).pathname, status: r.status() });
    if (r.request().resourceType() === 'script') {
      let digest: string | null = null;
      try {
        digest = sha256(await r.body());
      } catch {
        digest = null;
      }
      ctx.scripts.push({ url: r.url(), status: r.status(), sha256: digest });
    }
  });
  return ctx;
}

export async function closeSubstep(ctx: SubstepCtx): Promise<void> {
  await ctx.context.close().catch(() => undefined);
}

/**
 * Evidence immutability policy (owner O-3, review5): every run artifact is
 * write-once. JSON records are created exclusively (`wx`, never overwritten)
 * with mode 0444; screenshots are made 0444 immediately after capture; the
 * batch end seals every regular file in the run directory to 0444. The run
 * directory itself stays writable by the operator only so a separate
 * validation record can be added; integrity is established by the sha256
 * bindings checked by validate-run, not by file modes.
 */
export const EVIDENCE_FILE_MODE = 0o444;

/** Write-once evidence file (exclusive create, read-only mode). */
export function writeEvidenceFile(file: string, text: string): void {
  fs.writeFileSync(file, text, { flag: 'wx', mode: EVIDENCE_FILE_MODE });
}

/** Seal every regular file in a run directory read-only (idempotent). */
export function sealRunDir(runDir: string): void {
  for (const f of fs.readdirSync(runDir)) {
    const abs = path.join(runDir, f);
    if (fs.lstatSync(abs).isFile()) fs.chmodSync(abs, EVIDENCE_FILE_MODE);
  }
}

export function writeRaw(
  ctx: SubstepCtx,
  name: string,
  data: unknown,
  kind = 'raw-json'
): FileEntry {
  const file = path.join(ctx.runDir, `${ctx.prefix}.${name}.json`);
  writeEvidenceFile(file, JSON.stringify(data, null, 2) + '\n');
  const e = { kind, path: path.basename(file), sha256: sha256(fs.readFileSync(file)) };
  ctx.files.push(e);
  return e;
}

export async function screenshot(
  ctx: SubstepCtx,
  name: string,
  fullPage: boolean,
  kind: string
): Promise<FileEntry> {
  const file = path.join(ctx.runDir, `${ctx.prefix}.${name}.png`);
  await ctx.page.screenshot({ path: file, fullPage, animations: 'allow', caret: 'initial' });
  fs.chmodSync(file, EVIDENCE_FILE_MODE);
  const e = { kind, path: path.basename(file), sha256: sha256(fs.readFileSync(file)) };
  ctx.files.push(e);
  return e;
}

export async function navigate(ctx: SubstepCtx, route: string): Promise<void> {
  ctx.actions.push({ kind: 'navigate', target: route, at: now() });
  await ctx.page.goto(route, { waitUntil: 'domcontentloaded', timeout: 30_000 });
  ctx.readiness.push({ id: 'R0:domcontentloaded', ok: true, at: now() });
}

/**
 * R0 (§2a): [data-scion-page] present + no sl-spinner in the page + the
 * readback-bound expected-count predicate + 2 consecutive animation frames
 * with no layout change in the graded elements. Never networkidle.
 */
export async function waitR0(
  ctx: SubstepCtx,
  opts: {
    countCss: string | null;
    expectCount: ((n: number | null) => boolean) | null;
    graded: string[];
    label: string;
    timeoutMs?: number;
  }
): Promise<void> {
  const deadline = Date.now() + (opts.timeoutMs ?? 30_000);
  let last: unknown = null;
  for (;;) {
    last = await pe(ctx.page, {
      op: 'ready',
      pageSelector: '[data-scion-page]',
      countCss: opts.countCss,
    });
    const r = last as { pagePresent: boolean; spinnersInPage: number | null; count: number | null };
    const countOk = opts.expectCount ? opts.expectCount(r.count) : true;
    if (r.pagePresent && r.spinnersInPage === 0 && countOk) break;
    if (Date.now() > deadline) {
      ctx.readiness.push({
        id: `R0:${opts.label}:page+spinner+count`,
        ok: false,
        at: now(),
        details: last,
      });
      throw new CaptureError(`R0 not reached (${opts.label}): ${JSON.stringify(last)}`);
    }
    await ctx.page.waitForTimeout(100);
  }
  ctx.readiness.push({
    id: `R0:${opts.label}:page+spinner+count`,
    ok: true,
    at: now(),
    details: last,
  });
  await ctx.page.evaluate(() => document.fonts.ready.then(() => undefined));
  ctx.readiness.push({ id: `R0:${opts.label}:fonts-ready (supporting)`, ok: true, at: now() });
  await waitStable(ctx, opts.graded, `R0:${opts.label}:2-frame-stable`);
}

export async function waitStable(ctx: SubstepCtx, graded: string[], id: string): Promise<void> {
  const res = (await pe(ctx.page, { op: 'frame-stable', cssList: graded, maxFrames: 300 })) as {
    stable: boolean;
    frames: number;
  };
  ctx.readiness.push({ id, ok: res.stable, at: now(), details: res });
  if (!res.stable) throw new CaptureError(`${id}: layout not stable within ${res.frames} frames`);
}

/** Stability fingerprint of graded elements (for the one-stability-window check). */
export async function fingerprint(page: Page, graded: string[]): Promise<string> {
  const r = (await pe(page, {
    op: 'measure',
    queries: graded.map((css, i) => ({ key: `g${i}`, op: 'all' as const, css })),
    overflowScan: false,
    policies: POLICIES,
    policyContext: [],
    actionable: ACTIONABLE,
  })) as MeasureResult;
  return JSON.stringify(Object.values(r.elements).map((els) => els.map((e) => e.box)));
}

export async function measure(
  ctx: SubstepCtx,
  queries: ProbeQuery[],
  overflowScan: boolean
): Promise<MeasureResult> {
  return (await pe(ctx.page, {
    op: 'measure',
    queries,
    overflowScan,
    policies: POLICIES,
    policyContext: ctx.policyContext,
    actionable: ACTIONABLE,
  })) as MeasureResult;
}

export async function navRaw(ctx: SubstepCtx): Promise<NavRaw[]> {
  const raw = (await pe(ctx.page, { op: 'nav', within: APP_SHELL_TAG })) as Array<
    Omit<NavRaw, 'accessibleName'>
  >;
  const loc = ctx.page.locator(`${APP_SHELL_TAG} a.nav-link`);
  const n = await loc.count();
  const names = new Map<string, string | null>();
  for (let i = 0; i < n; i++) {
    const p = (await loc
      .nth(i)
      .evaluate(
        probe as (el: Element, r: ProbeRequest) => unknown,
        { op: 'path' } as ProbeRequest
      )) as string;
    if (!raw.find((r) => r.path === p)?.rendered) continue;
    names.set(p, await accessibleName(loc.nth(i), 'link'));
  }
  return raw.map((r) => ({ ...r, accessibleName: names.get(r.path) ?? null }));
}

/** Computed accessible name via Playwright's accessibility snapshot of the element. */
export async function accessibleName(
  loc: ReturnType<Page['locator']>,
  role: string
): Promise<string | null> {
  const snap = await loc.ariaSnapshot({ timeout: 5_000 }).catch(() => '');
  const m = new RegExp(`^- ${role} "((?:[^"\\\\]|\\\\.)*)"`, 'm').exec(snap);
  if (m) {
    try {
      return JSON.parse(`"${m[1]}"`) as string;
    } catch {
      // Escapes outside JSON (e.g. \xNN) — keep the raw snapshot text (review2 N-h).
      return m[1]!;
    }
  }
  const plain = new RegExp(`^- ${role} ([^:\\n"][^:\\n]*?)(?::|$)`, 'm').exec(snap);
  return plain ? plain[1]!.trim() : null;
}

// ─── Positioning (M0-pos-k, M3) ──────────────────────────────────────────

export type ScrollerName = keyof typeof NAMED_SCROLLERS;

export function scrollerForPolicy(policy: string): ScrollerName | null {
  for (const [name, def] of Object.entries(NAMED_SCROLLERS))
    if (def.policy === policy) return name as ScrollerName;
  return null;
}

export interface ScrollerState {
  path: string;
  scrollLeft: number;
  scrollTop: number;
  scrollWidth: number;
  scrollHeight: number;
  clientWidth: number;
  clientHeight: number;
  box: RawElement['box'];
}

export async function scrollers(ctx: SubstepCtx): Promise<Record<string, ScrollerState | null>> {
  return (await pe(ctx.page, {
    op: 'scrollers',
    names: Object.keys(NAMED_SCROLLERS),
    policies: POLICIES,
    policyContext: ctx.policyContext,
  })) as Record<string, ScrollerState | null>;
}

export interface PositionStep {
  method: 'wheel' | 'programmatic';
  scroller: ScrollerName;
  axis: 'x' | 'y';
  before: ScrollerState;
  after: ScrollerState;
  atEnd: boolean;
}

/**
 * One positioning step on a NAMED scroller: user-equivalent wheel at the
 * centre of the scroller's visible area; if the scroller did not move and is
 * not at its end, a LABELLED programmatic fallback (§2b). Returns null if
 * the scroller is already at its end.
 */
export async function positionStep(
  ctx: SubstepCtx,
  name: ScrollerName,
  vw: number,
  vh: number
): Promise<PositionStep | null> {
  const axis = NAMED_SCROLLERS[name].axis;
  const s0 = (await scrollers(ctx))[name];
  if (!s0) throw new CaptureError(`named scroller ${name} not found`);
  const atEnd = (s: ScrollerState) =>
    axis === 'y'
      ? s.scrollTop + s.clientHeight >= s.scrollHeight - 1
      : s.scrollLeft + s.clientWidth >= s.scrollWidth - 1;
  if (atEnd(s0)) return null;
  const left = Math.max(0, s0.box.x);
  const top = Math.max(0, s0.box.y);
  const right = Math.min(vw, s0.box.right);
  const bottom = Math.min(vh, s0.box.bottom);
  const cx = (left + right) / 2;
  const cy = (top + bottom) / 2;
  const delta = Math.max(40, Math.floor((axis === 'y' ? s0.clientHeight : s0.clientWidth) * 0.6));
  await ctx.page.mouse.move(cx, cy);
  ctx.actions.push({ kind: 'pointer-move', target: name, detail: { x: cx, y: cy }, at: now() });
  await ctx.page.mouse.wheel(axis === 'x' ? delta : 0, axis === 'y' ? delta : 0);
  ctx.actions.push({
    kind: 'wheel',
    target: name,
    detail: { dx: axis === 'x' ? delta : 0, dy: axis === 'y' ? delta : 0 },
    at: now(),
  });
  await waitStable(ctx, ['[data-scion-page]'], `pos:${name}:2-frame-stable`);
  let s1 = (await scrollers(ctx))[name]!;
  let method: PositionStep['method'] = 'wheel';
  const moved = axis === 'y' ? s1.scrollTop !== s0.scrollTop : s1.scrollLeft !== s0.scrollLeft;
  if (!moved) {
    method = 'programmatic';
    const r = await pe(ctx.page, {
      op: 'programmatic-scroll',
      name: name === 'document' ? 'document' : name,
      dx: axis === 'x' ? delta : 0,
      dy: axis === 'y' ? delta : 0,
      policies: POLICIES,
      policyContext: ctx.policyContext,
    });
    ctx.actions.push({
      kind: 'programmatic-scroll',
      target: name,
      detail: { label: 'geometry-positioning fallback (not reachability evidence)', result: r },
      at: now(),
    });
    await waitStable(ctx, ['[data-scion-page]'], `pos:${name}:programmatic:2-frame-stable`);
    s1 = (await scrollers(ctx))[name]!;
  }
  return { method, scroller: name, axis, before: s0, after: s1, atEnd: atEnd(s1) };
}

/** Pointer click at a hit-tested centre (measured interaction, §2a). */
export async function pointerClick(ctx: SubstepCtx, el: RawElement, label: string): Promise<void> {
  if (!el.hit || !el.hit.inViewport || !el.hit.ok) {
    throw new Error(`refusing pointer click on ${label}: hit test failed`);
  }
  await ctx.page.mouse.click(el.hit.cx, el.hit.cy);
  ctx.actions.push({
    kind: 'pointer-click',
    target: label,
    detail: { x: el.hit.cx, y: el.hit.cy, hitPath: el.hit.hitPath },
    at: now(),
  });
}

export async function twoFrames(page: Page): Promise<void> {
  await page.evaluate(
    () => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
  );
}

/**
 * Rev 7 §2 A-F1 rule (c): two unfocused full-style samples U1, U2 of every
 * focusable element in this context, separated by the SAME settle used after
 * every key press (twoFrames), with no input in between. Never focuses or
 * scrolls. Shared by the batch runner and the real-Chromium selftests.
 */
export async function sampleFocusBaseline(page: Page): Promise<FocusBaseline> {
  const sample = () => pe<Record<string, Record<string, string>>>(page, { op: 'focus-baseline' });
  const u1At = now();
  const u1 = await sample();
  await twoFrames(page);
  const u2At = now();
  const u2 = await sample();
  return { u1, u2, u1At, u2At, settle: 'two-animation-frames' };
}
