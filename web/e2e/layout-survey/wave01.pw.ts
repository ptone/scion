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
 * ATTACH-ONLY Wave01 measurement batch (contract FROZEN rev 4).
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
import { randomBytes, randomUUID } from 'node:crypto';
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
import { STATES, substepsFor, type StateDef, type Substep } from './wave01/manifest.js';
import {
  applyBatch,
  applyAuthCheck,
  envDecision,
  DEFAULT_ENV_MAX_AGE_MIN,
  classifyDevAuthProbes,
  provenanceKey,
  sharedEnvValues,
  evaluateEnv,
  isQuarantined,
  loadLedger,
  saveLedger,
  validateCapture,
  CAPTURE_KIND,
  RUN_KIND,
  type EnvGate,
} from './wave01/records.mjs';
import { assertValueFree, sha256, validatePair } from './wave01/release.mjs';
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

/**
 * Rev 4 §5a E-ENV-1a probes. P-API sends a FRESH random fake dev token (never a
 * real credential; the value is not recorded); P-WEB is a cookie-less
 * GET /auth/me. Only status, the error message and identity presence are kept.
 */
async function devAuthProbes(baseURL: string) {
  const fake = `scion_dev_${randomBytes(32).toString('hex')}`;
  const apiAt = now();
  let api: {
    status: number;
    message: string | null;
    at: string;
    endpoint: string;
    token: string;
  } | null = null;
  try {
    const res = await fetch(`${baseURL}/api/v1/groups`, {
      headers: { Authorization: `Bearer ${fake}` },
      redirect: 'manual',
    });
    let message: string | null = null;
    try {
      const body = (await res.json()) as { error?: { message?: string } | string };
      message = typeof body.error === 'object' ? (body.error?.message ?? null) : null;
    } catch {
      message = null;
    }
    api = {
      status: res.status,
      message,
      at: apiAt,
      endpoint: 'GET /api/v1/groups',
      token: 'scion_dev_<64 fresh random hex; not recorded>',
    };
  } catch {
    api = null;
  }
  const webAt = now();
  let web: { status: number; hasIdentity: boolean; at: string; endpoint: string } | null = null;
  try {
    const res = await fetch(`${baseURL}/auth/me`, { redirect: 'manual' });
    let hasIdentity = false;
    try {
      const body = (await res.json()) as Record<string, unknown>;
      hasIdentity = ['userId', 'UserID', 'email', 'Email', 'id'].some(
        (k) => typeof body[k] === 'string' && body[k] !== ''
      );
    } catch {
      hasIdentity = false;
    }
    web = { status: res.status, hasIdentity, at: webAt, endpoint: 'GET /auth/me (no cookie)' };
  } catch {
    web = null;
  }
  return { api, web };
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
  // The run directory exists before any pre-capture step, so a stop caused by
  // an exception before capture still leaves a run.json (review2 N-d).
  const runId = `${evidence ? 'wave01-run' : 'debug-run'}-${new Date().toISOString().replace(/[:.]/g, '').slice(0, 15)}Z-${randomUUID().slice(0, 8)}`;
  const runDir = path.join(cfg.evidenceDir, runId);
  fs.mkdirSync(cfg.evidenceDir, { recursive: true });
  fs.mkdirSync(runDir);
  try {
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
    const base = baseBytes
      ? (JSON.parse(baseBytes.toString('utf-8')) as Record<string, any>)
      : null;
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
              suiteFileCount: suite.fileCount,
              browserVersion,
              baseURL: cfg.baseURL,
            },
          })
        : ['missing: Release pair not provided'];
    if (evidence && pairErrors.length) stops.push(`Release pair invalid: ${pairErrors.join('; ')}`);
    const baseSha = baseBytes ? sha256(baseBytes) : null;
    const compSha = compBytes ? sha256(compBytes) : null;

    // ── E-ENV (§5a), PRE window ─────────────────────────────────────────────
    const batchStart = now();
    const maxAgeMin = Number(process.env.LAYOUT_SURVEY_ENV_MAX_AGE_MIN || DEFAULT_ENV_MAX_AGE_MIN);
    const envDeclBytes = cfg.envDeclarationFile ? fs.readFileSync(cfg.envDeclarationFile) : null;
    const envDecl = envDeclBytes ? JSON.parse(envDeclBytes.toString('utf-8')) : null;
    if (envDecl) assertValueFree(envDecl, 'env PRE declaration'); // never embed credential material
    const envDeclSha = envDeclBytes ? sha256(envDeclBytes) : null;
    const self401 = await anon401(cfg.baseURL);
    const preProbes = await devAuthProbes(cfg.baseURL);
    let env: EnvGate[] = evaluateEnv(
      envDecl,
      self401,
      { slotGeneration: String(base?.slotGeneration ?? ''), baseURL: cfg.baseURL },
      { batchStart, maxAgeMin },
      preProbes
    );
    let envD = envDecision(env);
    // review2 RB3: a bound PRE declaring test-login disabled is checked against
    // the runner's own test-login attempt (success ⇒ FAIL, refusal ⇒ INCONCLUSIVE).
    let authCheckInventory: unknown = null;
    if (envD.authCheckPending) {
      const at = now();
      try {
        const o = await openAdminSession({ ...cfg, purpose: `wave01-authcheck-${runId}` });
        authCheckInventory = o.inventory;
        env = applyAuthCheck(env, { succeeded: true, at });
      } catch (e) {
        env = applyAuthCheck(env, {
          succeeded: false,
          at,
          detail: String(e instanceof Error ? e.message : e).slice(0, 300),
        });
      }
      envD = envDecision(env);
      stops.push(
        `E-ENV-3 auth check: PRE declares test-login disabled (${envD.fails.includes('E-ENV-3') ? 'runner test-login succeeded: contradiction, FAIL' : 'runner test-login refused: capture impossible, INCONCLUSIVE'})`
      );
    }
    if (envD.fails.length)
      stops.push(
        `E-ENV contradiction (${envD.fails.join(', ')}): batch FAIL and stopped${envD.securityReport ? '; repo-default secret accepted: steward raises the private security report' : ''}`
      );
    if (evidence && envD.missing.length)
      stops.push(
        `E-ENV evidence missing/stale for this capture window (${envD.missing.join(', ')}): batch INCONCLUSIVE, stopped`
      );

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

    // Every (state, profile, substep) the frozen manifest defines; each gets a
    // record (complete, capture-error or BLOCKED with reason) — review1 N5.
    const expectedRecords = STATES.flatMap((st) =>
      PROFILES.flatMap((p) => substepsFor(st).map((sub) => `${st.id}.${p.id}.${sub}.capture.json`))
    );

    let runWritten = false;
    const writeRun = (extra: Record<string, unknown>) => {
      if (runWritten) return;
      runWritten = true;
      fs.writeFileSync(
        path.join(runDir, 'run.json'),
        JSON.stringify(
          {
            ...common,
            kind: RUN_KIND,
            id: `${RUN_KIND}-${randomUUID()}`,
            createdAt: now(),
            startedAt: batchStart,
            endedAt: now(),
            pins: {
              contract: CONTRACT,
              pilotFinding: PILOT_FINDING,
              helperExcerpt: HELPER_EXCERPT,
            },
            host,
            suiteManifest: suite.manifest,
            pairErrors,
            env,
            envDecision: envD,
            envMaxAgeMin: maxAgeMin,
            envDeclarationSha256: envDeclSha,
            // Value-free steward PRE declaration, embedded so the assessor grades
            // from the record itself (assertValueFree enforced at load).
            envPreDeclaration: envDecl,
            envPreBaseURL: typeof envDecl?.baseURL === 'string' ? envDecl.baseURL : null,
            envPreValues: envDecl ? sharedEnvValues(envDecl) : null,
            envPreProvenanceKey: envDecl ? provenanceKey(envDecl.e_env_1_provenance) : null,
            envPreDevAuthProbes: preProbes,
            authCheckInventory,
            expectedRecords,
            statesSelected: cfg.states,
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
    };

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

    const records: Rec[] = [];
    let ledger = ledgerBefore;
    let aborted: string | null = null;
    let mainPre: Awaited<ReturnType<typeof fetchMainJs>> | null = null;
    let inventory: Awaited<ReturnType<typeof openAdminSession>>['inventory'] | null = null;

    /** Durable provisional copy of each finished substep record (immutable). */
    const pushRecord = (r: Rec) => {
      records.push(r);
      const { fileName, ...body } = r;
      fs.writeFileSync(
        path.join(runDir, fileName.replace(/\.capture\.json$/, '.provisional.json')),
        JSON.stringify(
          {
            ...body,
            provisional: true,
            note: 'superseded by the .capture.json written after the batch post-check',
          },
          null,
          2
        ) + '\n',
        { flag: 'wx' }
      );
      if (evidence) ledger = applyBatch(ledger, runId, [r]);
    };

    try {
      mainPre = await fetchMainJs(cfg.baseURL);
      const opened = await openAdminSession({ ...cfg, purpose: `wave01-${runId}` });
      const session = opened.session;
      inventory = opened.inventory;
      const principalId = inventory.principal.id;

      const baseRecord = (state: StateDef, profileId: string, substep: Substep) => ({
        ...common,
        kind: CAPTURE_KIND,
        id: `${CAPTURE_KIND}-${randomUUID()}`,
        createdAt: now(),
        stateId: state.id,
        profile: PROFILES.find((p) => p.id === profileId),
        substep,
        principalId,
      });
      const blockedOne = (state: StateDef, profileId: string, sub: Substep, reason: string) =>
        pushRecord({
          ...baseRecord(state, profileId, sub),
          status: 'blocked',
          errorReason: reason,
          readiness: [],
          actions: [],
          outcomes: [],
          files: [],
          startedAt: now(),
          endedAt: now(),
          fileName: `${state.id}.${profileId}.${sub}.capture.json`,
        });
      const blocked = (state: StateDef, reason: string) => {
        for (const p of PROFILES)
          for (const sub of substepsFor(state)) blockedOne(state, p.id, sub, reason);
      };
      const quarantineReason = `quarantined after ${QUARANTINE_AFTER} consecutive capture errors; owner must clear before redispatch`;

      for (const state of STATES) {
        if (!cfg.states.includes(state.id)) {
          blocked(state, 'not selected for this batch (LAYOUT_SURVEY_STATES); omitted explicitly');
          continue;
        }
        if (state.adapter !== 'implemented') {
          blocked(state, state.blockedReason ?? 'adapter not implemented');
          continue;
        }
        if (isQuarantined(ledger, state.id)) {
          blocked(state, quarantineReason);
          continue;
        }
        const rb = await groups.readback(
          cfg.baseURL,
          session.accessToken,
          principalId,
          state,
          fixture
        );
        const route = groups.routeFor(state, fixture);
        for (const profile of PROFILES) {
          for (const sub of substepsFor(state)) {
            // review1 N8: quarantine applies immediately, before the next substep.
            if (isQuarantined(ledger, state.id)) {
              blockedOne(state, profile.id, sub, `${quarantineReason} (reached during this batch)`);
              continue;
            }
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
                  throw new CaptureError(
                    'E-MAIN: loaded main entry sha256 != Release mainJsSha256'
                  );
              } else if (sub === 'M1') {
                const f = await groups.runM1Forward(await open('-fwd'), state, rb, route);
                const unreached = Object.keys(f.targets).filter(
                  (k) =>
                    !f.startReached.includes(k) && !f.presses.some((p) => p.reached.includes(k))
                );
                const b = unreached.length
                  ? await groups.runM1Backward(await open('-bwd'), state, rb, route, f.targets)
                  : null;
                outcomes = [groups.gradeAF1(state, f, b)];
              } else if (sub === 'M3') {
                outcomes = [await groups.runM3(await open(), state, profile, rb, route)];
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
            } finally {
              // review1 N1: SAFETY is checked regardless of other errors and takes priority.
              const mutating = ctxs.flatMap((c) => c.mutatingRequests);
              if (mutating.length) {
                status = 'capture-error';
                outcomes = [];
                errorReason =
                  `SAFETY: mutating request(s) observed during measurement: ${JSON.stringify(mutating)}${errorReason ? ` | other error: ${errorReason}` : ''}`.slice(
                    0,
                    1600
                  );
                rec.safetyReport = {
                  mutatingRequests: mutating,
                  reportTo: 'web-layout-arch (private)',
                };
              }
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
            pushRecord({ ...(rec as Rec), fileName: `${prefix}.capture.json` });
          }
        }
      }
    } catch (e) {
      aborted = String(e instanceof Error ? e.message : e).slice(0, 800);
    } finally {
      // ── Batch end (always): E-MAIN + E-ENV-2 post-checks, then final records
      // and run.json — an aborted batch is recorded as aborted (review1 B4).
      const mainPost = await fetchMainJs(cfg.baseURL).catch((e) => ({
        status: 0,
        sha256: null as string | null,
        at: now(),
        error: String(e),
      }));
      const postAnon = await anon401(cfg.baseURL).catch((e) => ({
        status: 0,
        at: now(),
        endpoint: '/api/v1/groups',
        redirectFollowed: false,
        error: String(e),
      }));
      const postProbes = await devAuthProbes(cfg.baseURL);
      const postProbeCls = classifyDevAuthProbes(postProbes.api, postProbes.web);
      const postProbesOk = postProbeCls.api === 'off' && postProbeCls.web === 'off';
      const expected = base?.mainJsSha256 ?? null;
      const mainOk = !!expected && mainPre?.sha256 === expected && mainPost.sha256 === expected;
      const envOk = !envD.stop;
      const postAnonOk = postAnon.status === 401;
      const batchValid =
        !aborted &&
        mainOk &&
        envOk &&
        postAnonOk &&
        postProbesOk &&
        (evidence ? pairErrors.length === 0 : true);
      const reasons = [
        aborted ? `batch aborted: ${aborted}` : null,
        !expected
          ? 'no Release mainJsSha256 (debug run)'
          : !mainOk
            ? 'assets/main.js sha256 mismatch before/after batch'
            : null,
        !envOk
          ? `E-ENV pre-window gates not all passing (${[...envD.fails, ...envD.missing].join(', ')})`
          : null,
        !postAnonOk ? `post-batch anonymous probe status ${postAnon.status} (E-ENV-2)` : null,
        !postProbesOk
          ? `post-batch dev-auth probes P-API ${postProbeCls.api}, P-WEB ${postProbeCls.web} (E-ENV-1${postProbeCls.api === 'on' || postProbeCls.web === 'on' ? ' FAIL' : ''})`
          : null,
      ].filter(Boolean);
      const batchInvalidReason = batchValid ? null : reasons.join('; ');
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
        if (evidence)
          validationErrors.push(...validateCapture(final).map((e2) => `${fileName}: ${e2}`));
        fs.writeFileSync(path.join(runDir, fileName), JSON.stringify(final, null, 2) + '\n', {
          flag: 'wx',
        });
        captureIds.push(r.id);
      }
      if (evidence) saveLedger(ledgerFile, ledger);
      writeRun({
        batchValid,
        batchInvalidReason,
        aborted: !!aborted,
        abortReason: aborted,
        mainJs: { expected, pre: mainPre, post: mainPost },
        envPostSelfAnon401: postAnon,
        envPostDevAuthProbes: postProbes,
        envPostDeclarationRequired:
          'validate-run --env-post <steward POST declaration covering startedAt..endedAt>',
        captureIds,
        issuanceInventory: inventory,
        quarantine: { before: ledgerBefore.states, after: ledger.states },
        validationErrors,
        counts: {
          expected: expectedRecords.length,
          written: records.length,
          complete: records.filter((r) => r.status === 'complete').length,
          captureError: records.filter((r) => r.status === 'capture-error').length,
          blocked: records.filter((r) => r.status === 'blocked').length,
        },
      });
      if (aborted) throw new Error(`batch aborted (run.json written): ${aborted}`);
      expect(validationErrors, 'every record validates').toEqual([]);
      // Pilot convention: exit non-zero when any substep is a capture error, so
      // the steward never mistakes a partial batch for a clean one. Records and
      // run.json are already written above.
      expect(
        records
          .filter((r) => r.status === 'capture-error')
          .map((r) => `${r.fileName}: ${r.errorReason}`),
        'no capture-error records'
      ).toEqual([]);
      expect(batchValid || !evidence, `batch valid (${batchInvalidReason ?? ''})`).toBe(true);
    }
  } catch (e) {
    // Pre-capture exception (unparseable input, network error, …): record an
    // honest aborted run if nothing has been written yet, then fail the test.
    const runFile = path.join(runDir, 'run.json');
    if (!fs.existsSync(runFile)) {
      fs.writeFileSync(
        runFile,
        JSON.stringify(
          {
            schemaVersion: 1,
            kind: RUN_KIND,
            id: `${RUN_KIND}-${randomUUID()}`,
            createdAt: now(),
            producer: cfg.operatorIdentity,
            runId,
            evidenceMode: cfg.evidenceMode,
            contractSha256: CONTRACT.sha256,
            aborted: true,
            abortPhase: 'pre-capture',
            abortReason: String(e instanceof Error ? e.message : e).slice(0, 800),
            batchValid: false,
            captureIds: [],
          },
          null,
          2
        ) + '\n',
        { flag: 'wx' }
      );
    }
    throw e;
  }
});
