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
 * ATTACH-ONLY /admin/groups capture (Phase-1 layout survey).
 *
 * Does not build, start, seed, reset, stop or tear down anything. It reads a
 * steward Release record and fixture map, opens the bounded synthetic-admin
 * test-login session, and for each profile (390x844, 820x1180, 1440x900)
 * captures the default and fixture-filtered states with readiness checks,
 * geometry assertions, one interaction and screenshot hashes. One Capture
 * record per profile plus a run manifest are written to a fresh run dir.
 */

import { test, expect, type Browser, type Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { assertDisjoint, loadCaptureConfig } from './lib/config.js';
import {
  fixtureRecipeSha,
  scenarioSuiteSha,
  sha256,
  sha256File,
  SHA256_RE,
} from './lib/digest.mjs';
import {
  assertBadgesVisible,
  assertContainersFit,
  assertNamesVisible,
  assertTableFits,
  measureTableFit,
  assertNoDocumentOverflow,
  measureDocument,
  measureRow,
  type AssertionOutcome,
  type RowMetrics,
} from './lib/geometry.js';
import { envelope, readbackGroups, writeJSONExclusive, type FixtureMap } from './lib/records.js';
import { validateRecord } from './scripts/records.mjs';
import { openAdminSession } from './lib/session.js';

const PROFILES = [
  { key: 'phone-390x844', width: 390, height: 844 },
  { key: 'tablet-820x1180', width: 820, height: 1180 },
  { key: 'desktop-1440x900', width: 1440, height: 900 },
] as const;

const SCENARIO = {
  key: 'admin-groups',
  routeTemplate: '/admin/groups',
  roleKey: 'synthetic-admin (bounded admin-page exception)',
  fixtureProfiles: ['normal', 'long-content'],
  theme: 'light',
  locale: 'en-US',
  timezoneId: 'UTC',
  deviceScaleFactor: 1,
};

interface ReleaseRecord {
  kind: string;
  id: string;
  slotGeneration: string;
  mainJsSha256: string;
  baseURL: string;
  releaseKind: string;
  sourceSha: string;
  scenarioSuiteSha: string;
  serverLaunch: Record<string, unknown>;
}

/**
 * Non-secret evidence that the slot is the real hosted Hub integration:
 * unauthenticated API refusal (no dev-auth auto-session), healthz component
 * summary, the steward-declared launch mode from the Release, and the
 * runner's own no-mock guarantee.
 */
async function probeIntegration(baseURL: string, release: ReleaseRecord) {
  const anon = await fetch(`${baseURL}/api/v1/groups`, { redirect: 'manual' });
  let health: Record<string, unknown> = {};
  try {
    const res = await fetch(`${baseURL}/healthz`, { redirect: 'manual' });
    health = res.ok ? ((await res.json()) as Record<string, unknown>) : {};
  } catch {
    health = {};
  }
  const web = (health.web ?? {}) as Record<string, unknown>;
  const hub = (health.hub ?? {}) as Record<string, unknown>;
  return {
    anonymousGroupsApiStatus: anon.status,
    anonymousRefused: anon.status === 401,
    healthz: {
      status: health.status ?? null,
      scionVersion: health.scionVersion ?? null,
      components: Object.keys(health).sort(),
      webAssetsEmbedded: web.assetsEmbedded ?? null,
      hubChecks: Object.keys((hub.checks ?? {}) as object).sort(),
    },
    declaredServerLaunch: release.serverLaunch,
    auth: 'test-login (real /api/v1/auth/test-login; no dev-auth)',
    mocks: 'none: the runner registers no page.route/context.route interception',
  };
}

interface ReadyCheck {
  id: string;
  ok: boolean;
  details?: unknown;
}

interface Action {
  kind: 'setup' | 'measured';
  action: string;
  target?: string;
  at: string;
  outcome?: string;
}

interface FileEntry {
  kind: 'screenshot-viewport' | 'screenshot-fullpage' | 'geometry' | 'diagnostic-screenshot';
  path: string;
  sha256: string;
}

async function fetchMainJs(
  baseURL: string
): Promise<{ status: number; sha256: string | null; at: string }> {
  const at = new Date().toISOString();
  const res = await fetch(`${baseURL}/assets/main.js`, { redirect: 'manual' });
  if (!res.ok) return { status: res.status, sha256: null, at };
  return { status: res.status, sha256: sha256(Buffer.from(await res.arrayBuffer())), at };
}

async function healthz(baseURL: string): Promise<ReadyCheck> {
  try {
    const res = await fetch(`${baseURL}/healthz`, { redirect: 'manual' });
    const body = res.ok ? ((await res.json()) as { status?: string }) : {};
    return {
      id: 'healthz',
      ok: res.ok && body.status === 'healthy',
      details: { status: res.status, health: body.status ?? null },
    };
  } catch (e) {
    return { id: 'healthz', ok: false, details: { error: String(e) } };
  }
}

/** Wait until the element's bounding box is unchanged across consecutive double-rAF samples (max 50 samples). */
async function waitLayoutStable(page: Page, selector: string): Promise<boolean> {
  const loc = page.locator(selector).first();
  let prev = '';
  for (let i = 0; i < 50; i++) {
    const b = await loc.boundingBox().catch(() => null);
    const cur = JSON.stringify(b);
    if (b && cur === prev) return true;
    prev = cur;
    await page.evaluate(
      () => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
    );
  }
  return false;
}

function fontInventoryDigest(): string | null {
  try {
    const out = execFileSync('fc-list', ['--format', '%{family}|%{style}|%{file}\n'], {
      encoding: 'utf-8',
    });
    return sha256(out.split('\n').sort().join('\n'));
  } catch {
    return null;
  }
}

test('attach-only capture: /admin/groups at phone/tablet/desktop', async ({ browser }) => {
  test.setTimeout(240_000);
  const cfg = loadCaptureConfig();
  assertDisjoint(cfg.evidenceDir, cfg.privateDir);

  const releaseBytes = fs.readFileSync(cfg.releaseFile);
  const releaseRecordSha256 = sha256(releaseBytes);
  const release = JSON.parse(releaseBytes.toString('utf-8')) as ReleaseRecord;
  const releaseErrors = validateRecord(release) as string[];
  if (release.kind !== 'release' || releaseErrors.length) {
    throw new Error(`release record invalid: ${releaseErrors.join('; ') || 'kind != release'}`);
  }
  if (!SHA256_RE.test(release.mainJsSha256)) throw new Error('release mainJsSha256 is not 64-hex');
  if (new URL(release.baseURL).origin !== cfg.baseURL) {
    throw new Error(`release baseURL ${release.baseURL} != LAYOUT_SURVEY_BASE_URL ${cfg.baseURL}`);
  }
  const fixture = JSON.parse(fs.readFileSync(cfg.fixtureMapFile, 'utf-8')) as FixtureMap;
  // The map may come from the closed seed snapshot restored into this slot,
  // so its seed-time baseURL can differ; the per-profile real-API readback
  // against THIS slot is what proves the rows exist here.
  if (!fixture.readback?.ok) throw new Error('fixture map has no successful steward readback');

  const runId = `capture-run-${new Date().toISOString().replace(/[:.]/g, '').slice(0, 15)}Z-${randomUUID().slice(0, 8)}`;
  const runDir = path.join(cfg.evidenceDir, runId);
  fs.mkdirSync(cfg.evidenceDir, { recursive: true });
  fs.mkdirSync(runDir); // throws if it exists: never overwrite evidence
  const rel = (f: string) => path.relative(runDir, f).split(path.sep).join('/');

  // ── Batch start: release identity ────────────────────────────────────
  const mainPre = await fetchMainJs(cfg.baseURL);
  const releaseOkPre = mainPre.sha256 === release.mainJsSha256;

  // Capture-time setup (not a measured user action): bounded admin session.
  const { session, inventory } = await openAdminSession({ ...cfg, purpose: `capture-${runId}` });
  const setupActions: Action[] = [
    {
      kind: 'setup',
      action: 'steward-seed (separate command; referenced, not executed here)',
      target: fixture.id,
      at: fixture.createdAt,
    },
    {
      kind: 'setup',
      action: 'test-login bounded admin session',
      target: inventory.principal.id,
      at: new Date().toISOString(),
    },
  ];

  const integration = await probeIntegration(cfg.baseURL, release);

  const browserVersion = (browser as Browser).version();
  const fontDigest = fontInventoryDigest();
  const longRes = fixture.resources.find((r) => r.key === 'long');
  if (!longRes) throw new Error('fixture map has no "long" resource');

  const captureIds: string[] = [];
  const errors: string[] = [];
  const records: Array<Record<string, unknown> & { id: string; profileKey: string }> = [];

  for (const profile of PROFILES) {
    const env = envelope('capture', cfg.operatorIdentity);
    const startedAt = new Date().toISOString();
    const readyChecks: ReadyCheck[] = [];
    const actions: Action[] = [...setupActions];
    const files: FileEntry[] = [];
    const assertions: Record<string, AssertionOutcome[]> = {};
    const observations: Record<string, unknown> = {};
    const consoleErrors: string[] = [];
    const failedRequests: Array<{ path: string; status: number }> = [];
    let status: 'complete' | 'capture-error' = 'complete';
    let errorReason: string | null = null;

    readyChecks.push({
      id: 'release-main-js-pre',
      ok: releaseOkPre,
      details: {
        expected: release.mainJsSha256,
        observed: mainPre.sha256,
        httpStatus: mainPre.status,
      },
    });
    readyChecks.push(await healthz(cfg.baseURL));
    const rb = await readbackGroups(
      cfg.baseURL,
      session.accessToken,
      fixture.searchTerm,
      fixture.resources
    );
    readyChecks.push({ id: 'groups-list-api-readback', ok: rb.ok, details: rb });

    const context = await browser.newContext({
      baseURL: cfg.baseURL,
      storageState: session.storageStatePath,
      viewport: { width: profile.width, height: profile.height },
      deviceScaleFactor: SCENARIO.deviceScaleFactor,
      colorScheme: 'light',
      locale: SCENARIO.locale,
      timezoneId: SCENARIO.timezoneId,
    });
    const page = await context.newPage();
    page.on('console', (m) => {
      if (m.type() === 'error' && consoleErrors.length < 20)
        consoleErrors.push(m.text().slice(0, 300));
    });
    page.on('response', (r) => {
      if (r.status() >= 400 && failedRequests.length < 20)
        failedRequests.push({ path: new URL(r.url()).pathname, status: r.status() });
    });

    const shot = async (state: string, fullPage: boolean, diagnostic = false) => {
      const name = `${profile.key}-${state}${fullPage ? '-fullpage' : ''}.png`;
      const file = path.join(runDir, name);
      await page.screenshot({ path: file, fullPage, animations: 'disabled' });
      files.push({
        kind: diagnostic
          ? 'diagnostic-screenshot'
          : fullPage
            ? 'screenshot-fullpage'
            : 'screenshot-viewport',
        path: rel(file),
        sha256: sha256File(file),
      });
    };
    const writeGeometry = (state: string, data: unknown) => {
      const file = path.join(runDir, `${profile.key}-${state}-geometry.json`);
      writeJSONExclusive(file, data);
      files.push({ kind: 'geometry', path: rel(file), sha256: sha256File(file) });
    };

    try {
      if (!readyChecks.every((c) => c.ok)) throw new Error('pre-navigation readiness failed');

      // ── State 1: default /admin/groups ───────────────────────────────
      actions.push({
        kind: 'measured',
        action: 'goto',
        target: '/admin/groups',
        at: new Date().toISOString(),
      });
      await page.goto('/admin/groups', { waitUntil: 'domcontentloaded' });
      await expect(
        page.locator('scion-page-admin-groups').getByRole('heading', { level: 1, name: 'Groups' })
      ).toBeVisible({ timeout: 20_000 });
      await expect(page.getByRole('table', { name: 'Groups' })).toBeVisible({ timeout: 20_000 });
      await expect(page.locator('.skeleton-table')).toHaveCount(0, { timeout: 20_000 });
      await page.evaluate(() => document.fonts.ready.then(() => undefined));
      const stable1 = await waitLayoutStable(page, 'table');
      readyChecks.push(
        { id: 'default:heading+table+no-skeleton+fonts', ok: true },
        { id: 'default:layout-stable', ok: stable1 }
      );
      if (!stable1) throw new Error('default state layout did not stabilise');
      await shot('default', false);
      await shot('default', true);
      const docDefault = await measureDocument(page);
      const fitDefault = await measureTableFit(page);
      assertions['default'] = [
        assertTableFits(fitDefault),
        assertNoDocumentOverflow(docDefault),
        assertContainersFit(docDefault),
      ];
      writeGeometry('default', { document: docDefault, tableFit: fitDefault });

      // ── State 2: filtered to the seeded fixture rows ────────────────
      const filtered = `/admin/groups?q=${encodeURIComponent(fixture.searchTerm)}`;
      actions.push({
        kind: 'measured',
        action: 'goto',
        target: filtered,
        at: new Date().toISOString(),
      });
      await page.goto(filtered, { waitUntil: 'domcontentloaded' });
      for (const r of fixture.resources) {
        await expect(
          page.locator(`a.group-name-link[href="/admin/groups/${encodeURIComponent(r.id)}"]`)
        ).toBeVisible({ timeout: 20_000 });
      }
      await expect(page.locator('.skeleton-table')).toHaveCount(0, { timeout: 20_000 });
      const stable2 = await waitLayoutStable(page, 'table');
      readyChecks.push(
        {
          id: 'fixture-filter:all-seeded-rows-visible',
          ok: true,
          details: fixture.resources.map((r) => r.id),
        },
        { id: 'fixture-filter:layout-stable', ok: stable2 }
      );
      if (!stable2) throw new Error('fixture-filter state layout did not stabilise');
      await shot('fixture-filter', false);
      await shot('fixture-filter', true);
      const docFiltered = await measureDocument(page);
      const fitFiltered = await measureTableFit(page);
      const rows: RowMetrics[] = [];
      const accessibleNames: Record<string, string | null> = {};
      for (const r of fixture.resources) {
        const link = page.locator(
          `a.group-name-link[href="/admin/groups/${encodeURIComponent(r.id)}"]`
        );
        await link.scrollIntoViewIfNeeded();
        actions.push({
          kind: 'measured',
          action: 'scroll-into-view',
          target: r.key,
          at: new Date().toISOString(),
        });
        rows.push(await measureRow(page, r.key, r.id));
        const snap = await link.ariaSnapshot().catch(() => '');
        const m = /link "((?:[^"\\]|\\.)*)"/.exec(snap);
        accessibleNames[r.key] = m ? JSON.parse(`"${m[1]}"`) : null;
      }
      const expectedNames = Object.fromEntries(fixture.resources.map((r) => [r.key, r.name]));
      assertions['fixture-filter'] = [
        assertTableFits(fitFiltered),
        assertNoDocumentOverflow(docFiltered),
        assertBadgesVisible(rows, docFiltered.innerWidth),
        assertNamesVisible(rows, docFiltered.innerWidth, expectedNames, accessibleNames),
        assertContainersFit(docFiltered),
      ];
      observations['fixture-filter'] = {
        longNameTruncated: rows.find((r) => r.key === 'long')?.linkTruncated ?? null,
        nameLinkTargetSizes: rows.map((r) => ({
          key: r.key,
          width: r.link?.width ?? null,
          height: r.link?.height ?? null,
          meets44x44ProductTarget: !!r.link && r.link.width >= 44 && r.link.height >= 44,
        })),
      };
      writeGeometry('fixture-filter', {
        document: docFiltered,
        tableFit: fitFiltered,
        rows,
        accessibleNames,
      });

      // ── Interaction LS-I1: open the long-name group from the list ───
      const longLink = page.locator(
        `a.group-name-link[href="/admin/groups/${encodeURIComponent(longRes.id)}"]`
      );
      const t0 = new Date().toISOString();
      let navOk = false;
      try {
        await longLink.click({ timeout: 10_000 });
        await page.waitForURL((u) => u.pathname === `/admin/groups/${longRes.id}`, {
          timeout: 15_000,
        });
        navOk = true;
      } catch {
        navOk = false;
      }
      actions.push({
        kind: 'measured',
        action: 'click',
        target: 'long-name link',
        at: t0,
        outcome: navOk ? 'navigated' : 'no-navigation',
      });
      assertions['fixture-filter']!.push({
        id: 'LS-I1-long-name-link-opens-detail',
        oracleClause: 'B-I1',
        description: 'clicking the long-name row link navigates to /admin/groups/<id>',
        outcome: navOk ? 'pass' : 'fail',
        details: { finalPath: new URL(page.url()).pathname },
      });
    } catch (e) {
      status = 'capture-error';
      errorReason = String(e instanceof Error ? e.message : e).slice(0, 500);
      errors.push(`${profile.key}: ${errorReason}`);
      try {
        await shot('capture-error', false, true);
      } catch {
        /* page may be unusable */
      }
    }

    const theme = await page
      .evaluate(() => ({
        dataTheme: document.documentElement.getAttribute('data-theme'),
        className: document.documentElement.className,
        prefersDark: window.matchMedia('(prefers-color-scheme: dark)').matches,
      }))
      .catch(() => null);
    await context.close();

    const record = {
      ...env,
      kind: 'capture' as const,
      runId,
      status,
      errorReason,
      scenarioKey: SCENARIO.key,
      profileKey: profile.key,
      purpose: 'survey',
      capturerIdentity: cfg.operatorIdentity,
      releaseId: release.id,
      releaseRecordSha256,
      slotGeneration: release.slotGeneration,
      releaseKind: release.releaseKind,
      servedSourceSha: release.sourceSha,
      baseURL: cfg.baseURL,
      integration,
      route: {
        template: SCENARIO.routeTemplate,
        states: ['default', `fixture-filter (?q=${fixture.searchTerm})`],
      },
      resourceKeys: Object.fromEntries(fixture.resources.map((r) => [r.key, r.id])),
      fixture: {
        fixtureMapId: fixture.id,
        seededBaseURL: fixture.baseURL,
        recipeId: fixture.recipeId,
        fixtureRecipeSha: fixture.fixtureRecipeSha,
        runnerRecipeSha: fixtureRecipeSha(),
        profiles: SCENARIO.fixtureProfiles,
      },
      // Runner identity (distinct from the served source above).
      scenarioSuiteSha: scenarioSuiteSha(),
      runnerReleaseScenarioSuiteSha: release.scenarioSuiteSha,
      role: SCENARIO.roleKey,
      principalId: inventory.principal.id,
      environment: {
        viewport: { width: profile.width, height: profile.height },
        deviceScaleFactor: SCENARIO.deviceScaleFactor,
        isMobile: false,
        hasTouch: false,
        browser: { engine: 'chromium', version: browserVersion },
        os: { platform: os.platform(), release: os.release(), arch: os.arch() },
        fontInventorySha256: fontDigest,
        theme: SCENARIO.theme,
        themeObserved: theme,
        locale: SCENARIO.locale,
        timezoneId: SCENARIO.timezoneId,
      },
      mainJs: { expected: release.mainJsSha256, pre: mainPre },
      readyChecks,
      actions,
      assertions,
      observations,
      masks: [],
      consoleErrors,
      failedRequests,
      files,
      startedAt,
      endedAt: new Date().toISOString(),
    };
    records.push(record);
    captureIds.push(record.id);
  }

  // ── Batch end: release identity must not have changed ──────────────
  const mainPost = await fetchMainJs(cfg.baseURL);
  const releaseOkPost = mainPost.sha256 === release.mainJsSha256;
  const batchValid = releaseOkPre && releaseOkPost && mainPre.sha256 === mainPost.sha256;
  const batchInvalidReason = batchValid
    ? null
    : 'assets/main.js digest mismatch before/after batch (design §86): all captures invalid';

  // Capture records are written only after the post-batch check so each one
  // carries both digests and the batch verdict (no implicit link to run.json).
  for (const record of records) {
    writeJSONExclusive(path.join(runDir, `${record.profileKey}.capture.json`), {
      ...record,
      mainJs: { expected: release.mainJsSha256, pre: mainPre, post: mainPost },
      batchValid,
      batchInvalidReason,
    });
  }

  writeJSONExclusive(path.join(runDir, 'run.json'), {
    ...envelope('capture', cfg.operatorIdentity),
    kind: 'capture-run',
    runId,
    releaseId: release.id,
    releaseRecordSha256,
    slotGeneration: release.slotGeneration,
    servedSourceSha: release.sourceSha,
    scenarioSuiteSha: scenarioSuiteSha(),
    baseURL: cfg.baseURL,
    integration,
    captureIds,
    mainJs: { expected: release.mainJsSha256, pre: mainPre, post: mainPost },
    batchValid,
    batchInvalidReason,
    attachOnly: {
      builtHub: false,
      startedHub: false,
      seeded: false,
      reset: false,
      stoppedHub: false,
      teardown: false,
    },
    setupActions,
    issuanceInventory: inventory,
    errors,
  });

  expect(batchValid, 'release main.js digest matched before and after the batch').toBe(true);
  expect(errors, 'no capture-error records').toEqual([]);
});
