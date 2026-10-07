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
 * ATTACH-ONLY Wave01 measurement batch (contract FROZEN rev 2).
 *
 * Never builds, starts, seeds, resets or stops anything; registers no route
 * interception. Per batch: capture-host + suite-digest checks, Release pair
 * validation, E-ENV gates, main.js pre-check, then for every selected state
 * × profile × substep a fresh browser context producing raw measurements,
 * screenshots and a per-substep record. Records and run.json are written
 * only after the main.js post-check so each carries the batch verdict.
 * States without an adapter, or quarantined, get explicit BLOCKED records.
 */

import { test, expect, type Browser } from '@playwright/test';
import { randomUUID } from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { assertDisjoint, ensurePrivateDir } from './lib/config.js';
import { openAdminSession } from './lib/session.js';
import {
  CONTRACT,
  HELPER_EXCERPT,
  PILOT_FINDING,
  PROFILES,
  QUARANTINE_AFTER,
} from './wave01/contract.js';
import { loadWave01Config } from './wave01/config.js';
import type { ClauseResult } from './wave01/evaluate.js';
import * as groups from './wave01/groups.js';
import { STATES, stateById, substepsFor, type StateDef, type Substep } from './wave01/manifest.js';
import {
  applyBatch,
  envStop,
  evaluateEnv,
  isQuarantined,
  loadLedger,
  saveLedger,
  validateCapture,
  CAPTURE_KIND,
  RUN_KIND,
  type EnvGate,
} from './wave01/records.mjs';
import { sha256, validatePair } from './wave01/release.mjs';
import {
  CaptureError,
  closeSubstep,
  now,
  openSubstep,
  screenshot,
  type SubstepCtx,
} from './wave01/runner-lib.js';
import { captureHostCheck, suiteDigest } from './wave01/suite-digest.mjs';

interface Rec extends Record<string, unknown> {
  id: string;
  stateId: string;
  status: 'complete' | 'capture-error' | 'blocked';
  fileName: string;
}

async function fetchMainJs(baseURL: string) {
  const at = now();
  const res = await fetch(`${baseURL}/assets/main.js`, { redirect: 'manual' });
  if (!res.ok) return { status: res.status, sha256: null as string | null, at };
  return { status: res.status, sha256: sha256(Buffer.from(await res.arrayBuffer())), at };
}

async function anon401(baseURL: string) {
  const at = now();
  const res = await fetch(`${baseURL}/api/v1/groups`, { redirect: 'manual' });
  return { status: res.status, at, endpoint: '/api/v1/groups', redirectFollowed: false };
}

test('Wave01 attach-only measurement batch', async ({ browser }) => {
  test.setTimeout(3 * 60 * 60 * 1000);
  const cfg = loadWave01Config();
  assertDisjoint(cfg.evidenceDir, cfg.privateDir);
  assertDisjoint(cfg.stateDir, cfg.privateDir);
  ensurePrivateDir(cfg.privateDir);
  const evidence = cfg.evidenceMode === 'evidence';
  const stops: string[] = [];

  // ── Runner identity (§5b E-RUN capture-host checks) ─────────────────────
  const suite = suiteDigest();
  const host = cfg.reviewedRunnerCommit
    ? captureHostCheck({ reviewedCommit: cfg.reviewedRunnerCommit })
    : null;
  if (evidence) {
    if (!host?.ok) stops.push(`capture-host check failed: ${host?.problems.join('; ')}`);
    if (suite.digest !== cfg.reviewedSuiteDigest)
      stops.push(`suite digest ${suite.digest} != reviewed ${cfg.reviewedSuiteDigest}`);
  }

  // ── Release pair (E-REL) ────────────────────────────────────────────────
  const baseBytes = cfg.baseReleaseFile ? fs.readFileSync(cfg.baseReleaseFile) : null;
  const compBytes = cfg.companionFile ? fs.readFileSync(cfg.companionFile) : null;
  const base = baseBytes ? (JSON.parse(baseBytes.toString('utf-8')) as Record<string, any>) : null;
  const companion = compBytes
    ? (JSON.parse(compBytes.toString('utf-8')) as Record<string, any>)
    : null;
  const browserVersion = (browser as Browser).version();
  const pairErrors =
    baseBytes && companion
      ? validatePair({
          baseBytes,
          companion,
          expect: {
            runnerCommit: suite.commit,
            suiteDigest: suite.digest,
            browserVersion,
            baseURL: cfg.baseURL,
          },
        })
      : ['missing: Release pair not provided'];
  if (evidence && pairErrors.length) stops.push(`Release pair invalid: ${pairErrors.join('; ')}`);
  const baseSha = baseBytes ? sha256(baseBytes) : null;
  const compSha = compBytes ? sha256(compBytes) : null;

  // ── E-ENV (§5a) ─────────────────────────────────────────────────────────
  const envDecl = cfg.envDeclarationFile
    ? JSON.parse(fs.readFileSync(cfg.envDeclarationFile, 'utf-8'))
    : null;
  const envDeclSha = cfg.envDeclarationFile
    ? sha256(fs.readFileSync(cfg.envDeclarationFile))
    : null;
  const self401 = await anon401(cfg.baseURL);
  const env: EnvGate[] = evaluateEnv(envDecl, self401, {
    slotGeneration: String(base?.slotGeneration ?? ''),
  });
  if (envStop(env))
    stops.push(
      'E-ENV contradiction (dev-auth effective ON or repo-default secret accepted): batch stopped; steward raises the private report'
    );
  if (evidence && env.some((g) => g.outcome === 'inconclusive'))
    stops.push('E-ENV evidence missing for this capture window (batch would be INCONCLUSIVE)');

  const runId = `${evidence ? 'wave01-run' : 'debug-run'}-${new Date().toISOString().replace(/[:.]/g, '').slice(0, 15)}Z-${randomUUID().slice(0, 8)}`;
  const runDir = path.join(cfg.evidenceDir, runId);
  fs.mkdirSync(cfg.evidenceDir, { recursive: true });
  fs.mkdirSync(runDir);
  fs.mkdirSync(cfg.stateDir, { recursive: true });
  const ledgerFile = path.join(cfg.stateDir, 'wave01-quarantine-ledger.json');
  const ledgerBefore = loadLedger(ledgerFile);

  const common = {
    schemaVersion: 1 as const,
    producer: cfg.operatorIdentity,
    runId,
    evidenceMode: cfg.evidenceMode,
    contractSha256: CONTRACT.sha256,
    baseReleaseId: base?.id ?? 'none (debug)',
    baseReleaseSha256: baseSha ?? 'none (debug)',
    companionId: companion?.id ?? 'none (debug)',
    companionSha256: compSha ?? 'none (debug)',
    slotGeneration: base?.slotGeneration ?? 'none (debug)',
    releaseKind: base?.releaseKind ?? 'none (debug)',
    phase: companion?.phase ?? 'none (debug)',
    runner: {
      head: suite.commit,
      suiteDigest: suite.digest,
      suiteFileCount: suite.fileCount,
      method: suite.method,
    },
    capturerIdentity: cfg.operatorIdentity,
    baseURL: cfg.baseURL,
  };

  const writeRun = (extra: Record<string, unknown>) =>
    fs.writeFileSync(
      path.join(runDir, 'run.json'),
      JSON.stringify(
        {
          ...common,
          kind: RUN_KIND,
          id: `${RUN_KIND}-${randomUUID()}`,
          createdAt: now(),
          pins: { contract: CONTRACT, pilotFinding: PILOT_FINDING, helperExcerpt: HELPER_EXCERPT },
          host,
          suiteManifest: suite.manifest,
          pairErrors,
          env,
          envDeclarationSha256: envDeclSha,
          attachOnly: {
            builtHub: false,
            startedHub: false,
            seeded: false,
            reset: false,
            stoppedHub: false,
            routeMocks: 'none',
          },
          environment: {
            browser: { engine: 'chromium', version: browserVersion },
            os: { platform: os.platform(), release: os.release(), arch: os.arch() },
          },
          ...extra,
        },
        null,
        2
      ) + '\n',
      { flag: 'wx' }
    );

  if (stops.length) {
    writeRun({ batchValid: false, stopped: stops, captureIds: [] });
    throw new Error(`batch stopped before capture:\n  ${stops.join('\n  ')}`);
  }

  const fixture = JSON.parse(
    fs.readFileSync(cfg.fixtureMapFile, 'utf-8')
  ) as groups.GroupsFixtureMap;
  const fixtureMapSha = sha256(fs.readFileSync(cfg.fixtureMapFile));
  if (evidence && companion?.fixtures?.groupsFixtureMapSha256 !== fixtureMapSha) {
    writeRun({
      batchValid: false,
      stopped: ['groups fixture map sha256 != companion.fixtures.groupsFixtureMapSha256'],
      captureIds: [],
    });
    throw new Error('fixture map does not match the companion');
  }

  const mainPre = await fetchMainJs(cfg.baseURL);
  const { session, inventory } = await openAdminSession({ ...cfg, purpose: `wave01-${runId}` });
  const records: Rec[] = [];

  const baseRecord = (state: StateDef, profileId: string, substep: Substep) => ({
    ...common,
    kind: CAPTURE_KIND,
    id: `${CAPTURE_KIND}-${randomUUID()}`,
    createdAt: now(),
    stateId: state.id,
    profile: PROFILES.find((p) => p.id === profileId),
    substep,
    principalId: inventory.principal.id,
  });

  const blocked = (state: StateDef, reason: string) => {
    for (const p of PROFILES)
      for (const sub of substepsFor(state)) {
        const r = {
          ...baseRecord(state, p.id, sub),
          status: 'blocked' as const,
          errorReason: reason,
          readiness: [],
          actions: [],
          outcomes: [],
          files: [],
          startedAt: now(),
          endedAt: now(),
        };
        records.push({ ...r, fileName: `${state.id}.${p.id}.${sub}.capture.json` });
      }
  };

  for (const id of cfg.states) {
    const state = stateById(id);
    if (state.adapter !== 'implemented') {
      blocked(state, state.blockedReason ?? 'adapter not implemented');
      continue;
    }
    if (isQuarantined(ledgerBefore, id)) {
      blocked(
        state,
        `quarantined after ${QUARANTINE_AFTER} consecutive capture errors; owner must clear before redispatch`
      );
      continue;
    }
    const rb = await groups.readback(
      cfg.baseURL,
      session.accessToken,
      inventory.principal.id,
      state,
      fixture
    );
    const route = groups.routeFor(state, fixture);
    for (const profile of PROFILES) {
      for (const sub of substepsFor(state)) {
        const prefix = `${state.id}.${profile.id}.${sub}`;
        const startedAt = now();
        const rec: Record<string, unknown> = {
          ...baseRecord(state, profile.id, sub),
          route,
          readback: rb,
        };
        let outcomes: ClauseResult[] = [];
        let status: Rec['status'] = 'complete';
        let errorReason: string | null = null;
        const ctxs: SubstepCtx[] = [];
        const open = async (suffix = '') => {
          const c = await openSubstep(browser, {
            baseURL: cfg.baseURL,
            storageStatePath: session.storageStatePath,
            profile,
            runDir,
            prefix: prefix + suffix,
            policyContext: [state.id],
          });
          ctxs.push(c);
          return c;
        };
        try {
          if (!rb.ok)
            throw new CaptureError(
              `in-batch readback failed: ${rb.problems.join('; ') || `HTTP ${rb.status}`}`
            );
          if (sub === 'M0') {
            const ctx = await open();
            const r = await groups.runM0(ctx, state, profile, rb, route);
            outcomes = r.outcomes;
            Object.assign(rec, {
              positioning: r.positioning,
              loadedScripts: r.loadedScripts,
              loadedMainEntry: r.loadedMainEntry,
            });
            if (!r.loadedMainEntry?.sha256)
              throw new CaptureError('E-MAIN: loaded main entry /assets/main.js not observed');
            if (base && r.loadedMainEntry.sha256 !== base.mainJsSha256)
              throw new CaptureError('E-MAIN: loaded main entry sha256 != Release mainJsSha256');
          } else if (sub === 'M1') {
            const f = await groups.runM1Forward(await open('-fwd'), state, rb, route);
            const unreached = Object.keys(f.targets).filter(
              (k) => !f.startReached.includes(k) && !f.presses.some((p) => p.reached.includes(k))
            );
            const b = unreached.length
              ? await groups.runM1Backward(await open('-bwd'), state, rb, route, f.targets)
              : null;
            outcomes = [groups.gradeAF1(state, f, b)];
          } else if (sub === 'M3') {
            outcomes = [await groups.runM3(await open(), state, profile, rb, route)];
          }
          for (const c of ctxs) {
            if (c.mutatingRequests.length) {
              throw new CaptureError(
                `SAFETY: mutating request(s) observed during measurement: ${JSON.stringify(c.mutatingRequests)}`
              );
            }
          }
        } catch (e) {
          status = 'capture-error';
          errorReason = String(e instanceof Error ? e.message : e).slice(0, 800);
          outcomes = [];
          const last = ctxs[ctxs.length - 1];
          if (last)
            await screenshot(
              last,
              'capture-error.diagnostic',
              false,
              'diagnostic-screenshot'
            ).catch(() => undefined);
        }
        Object.assign(rec, {
          status,
          errorReason,
          outcomes,
          readiness: ctxs.flatMap((c) => c.readiness.map((r) => ({ context: c.prefix, ...r }))),
          actions: ctxs.flatMap((c) => c.actions.map((a) => ({ context: c.prefix, ...a }))),
          files: ctxs.flatMap((c) => c.files),
          consoleErrors: ctxs.flatMap((c) => c.consoleErrors),
          failedRequests: ctxs.flatMap((c) => c.failedRequests),
          mutatingRequests: ctxs.flatMap((c) => c.mutatingRequests),
          startedAt,
          endedAt: now(),
        });
        for (const c of ctxs) await closeSubstep(c);
        records.push({ ...(rec as Rec), fileName: `${prefix}.capture.json` });
      }
    }
  }

  // ── Batch end: E-MAIN post-check, then write every record ───────────────
  const mainPost = await fetchMainJs(cfg.baseURL);
  const expected = base?.mainJsSha256 ?? null;
  const batchValid =
    !!expected &&
    mainPre.sha256 === expected &&
    mainPost.sha256 === expected &&
    (evidence ? pairErrors.length === 0 : true);
  const batchInvalidReason = batchValid
    ? null
    : !expected
      ? 'no Release mainJsSha256 (debug run)'
      : 'assets/main.js sha256 mismatch before/after batch: all captures invalid';
  const captureIds: string[] = [];
  const validationErrors: string[] = [];
  for (const r of records) {
    const { fileName, ...body } = r;
    const final = {
      ...body,
      mainJs: { expected, pre: mainPre, post: mainPost },
      batchValid,
      batchInvalidReason,
    };
    if (evidence) validationErrors.push(...validateCapture(final).map((e) => `${fileName}: ${e}`));
    fs.writeFileSync(path.join(runDir, fileName), JSON.stringify(final, null, 2) + '\n', {
      flag: 'wx',
    });
    captureIds.push(r.id);
  }
  const ledgerAfter = evidence ? applyBatch(ledgerBefore, runId, records) : ledgerBefore;
  if (evidence) saveLedger(ledgerFile, ledgerAfter);
  writeRun({
    batchValid,
    batchInvalidReason,
    mainJs: { expected, pre: mainPre, post: mainPost },
    captureIds,
    issuanceInventory: inventory,
    states: cfg.states,
    quarantine: { before: ledgerBefore.states, after: ledgerAfter.states },
    validationErrors,
    counts: {
      complete: records.filter((r) => r.status === 'complete').length,
      captureError: records.filter((r) => r.status === 'capture-error').length,
      blocked: records.filter((r) => r.status === 'blocked').length,
    },
  });

  expect(validationErrors, 'every record validates').toEqual([]);
  expect(batchValid || !evidence, 'release main.js digest matched before and after the batch').toBe(
    true
  );
});

// Referenced so the manifest stays the single source of state IDs.
void STATES;
