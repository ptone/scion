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

// Wave01 capture/run records: E-ENV gate evaluation, per-substep capture
// record validation, run validation against the Release pair, and the E7
// quarantine ledger (contract §3: 3 consecutive capture errors ⇒ quarantine).
//
//   node wave01/records.mjs validate-run RUN_DIR --base FILE --companion FILE --env-post FILE
//   node wave01/records.mjs quarantine-status --ledger FILE
//   node wave01/records.mjs quarantine-clear --ledger FILE --state W01-Sxx --by ID --reason TEXT

import * as fs from 'node:fs';
import * as path from 'node:path';
import { sha256, validatePair, CONTRACT_SHA256 } from './release.mjs';

export const CAPTURE_KIND = 'wave01-capture';
export const RUN_KIND = 'wave01-capture-run';
export const SUBSTEPS = ['M0', 'M1', 'M2', 'M3'];
export const STATUSES = ['complete', 'capture-error', 'blocked'];
export const OUTCOMES = ['pass', 'fail', 'pending', 'inconclusive', 'not-applicable', 'blocked'];
export const QUARANTINE_AFTER = 3;
const SHA_RE = /^[0-9a-f]{64}$/;

// ─── E-ENV (§5a) ─────────────────────────────────────────────────────────

/** Default maximum age (minutes) of the PRE declaration's window_start at batch start. */
export const DEFAULT_ENV_MAX_AGE_MIN = 60;

const ms = (iso) => {
  if (typeof iso !== 'string') return NaN;
  const t = Date.parse(iso);
  return /Z$/.test(iso) ? t : NaN;
};

/**
 * Grade E-ENV-1..5 for the PRE window (contract §5a; review1 B2/B3).
 *
 * Inputs: the steward's PRE declaration for THIS capture window, the
 * runner's own anonymous probe, the Release slotGeneration and the batch
 * start. Missing or stale evidence ⇒ inconclusive; contradiction ⇒ fail.
 * The window is bound by window_start (alias window_ts) ≤ batchStart and
 * ≥ batchStart − maxAgeMin; e_env_5.ts must lie inside
 * [window_start, batchStart]; the self anonymous probe must be at or after
 * window_start. E-ENV-4 needs evidence DURING the batch: the PRE part is
 * graded here (effective false + no process/dispatch true) and the gate
 * is completed only by a POST declaration (evaluateEnvPost), so here it is
 * `awaitingPost: true` when the PRE part passes.
 *
 * @param {any} decl
 * @param {{status: number, at: string} | null} selfAnon401
 * @param {{slotGeneration: string}} release
 * @param {{batchStart: string, maxAgeMin?: number}} window
 */
export function evaluateEnv(decl, selfAnon401, release, window) {
  const g = (gate, outcome, details, extra = {}) => ({ gate, outcome, details, ...extra });
  const batchStart = ms(window?.batchStart);
  const maxAgeMin = window?.maxAgeMin ?? DEFAULT_ENV_MAX_AGE_MIN;
  const selfOutcome = (startMs) => {
    if (!selfAnon401) return 'inconclusive';
    if (selfAnon401.status !== 401) return 'fail';
    const at = ms(selfAnon401.at);
    if (Number.isNaN(at) || (!Number.isNaN(startMs) && at < startMs)) return 'inconclusive';
    return 'pass';
  };
  if (!decl) {
    return [
      g('E-ENV-1', 'inconclusive', 'no steward PRE env declaration'),
      g('E-ENV-2', selfOutcome(NaN), { self: selfAnon401 }),
      g('E-ENV-3', 'inconclusive', 'no steward PRE env declaration'),
      g('E-ENV-4', 'inconclusive', 'no steward PRE env declaration'),
      g('E-ENV-5', 'inconclusive', 'no steward PRE env declaration'),
    ];
  }
  const wsIso = decl.window_start ?? decl.window_ts;
  const ws = ms(wsIso);
  const genOk = decl.slotGeneration === release.slotGeneration;
  const windowOk =
    !Number.isNaN(ws) &&
    !Number.isNaN(batchStart) &&
    ws <= batchStart &&
    ws >= batchStart - maxAgeMin * 60_000;
  const binding = {
    declaredSlotGeneration: decl.slotGeneration ?? null,
    releaseSlotGeneration: release.slotGeneration,
    windowStart: wsIso ?? null,
    batchStart: window?.batchStart ?? null,
    maxAgeMin,
    genOk,
    windowOk,
  };
  const bound = genOk && windowOk;
  const bool3 = (v, good) =>
    typeof v !== 'boolean' ? 'inconclusive' : v === good ? 'pass' : 'fail';
  // A contradiction is a FAIL even on a stale/mismatched declaration; missing/stale otherwise ⇒ inconclusive.
  const grade = (v, good, extraOk = true) => {
    const b = bool3(v, good);
    if (b === 'fail') return 'fail';
    if (!bound || !extraOk) return 'inconclusive';
    return b;
  };
  const sources = decl.e_env_1_sources;
  const sourcesOk =
    Array.isArray(sources) &&
    sources.length > 0 &&
    sources.every((x) => typeof x === 'string' && x);
  const out = [
    g('E-ENV-1', grade(decl.e_env_1_dev_auth_effective, false, sourcesOk), {
      effective: decl.e_env_1_dev_auth_effective ?? null,
      sources: sources ?? null,
      sourcesOk,
      binding,
    }),
    g('E-ENV-2', selfOutcome(ws), {
      self: selfAnon401,
      steward: decl.e_env_2_anon_401 ?? null,
      windowStart: wsIso ?? null,
    }),
    g(
      'E-ENV-3',
      bound && typeof decl.e_env_3_test_login_enabled === 'boolean' ? 'pass' : 'inconclusive',
      {
        testLoginEnabled: decl.e_env_3_test_login_enabled ?? null,
        note: 'recorded separately; capture principal is synthetic',
        binding,
      }
    ),
  ];
  const eff4 = bool3(decl.e_env_4_runtime_broker_effective, false);
  const nob4 = bool3(decl.e_env_4_no_broker_process_or_dispatch, true);
  const pre4 =
    eff4 === 'fail' || nob4 === 'fail'
      ? 'fail'
      : !bound || eff4 !== 'pass' || nob4 !== 'pass'
        ? 'inconclusive'
        : 'pass';
  out.push(
    g(
      'E-ENV-4',
      pre4 === 'pass' ? 'inconclusive' : pre4,
      {
        phase: 'pre',
        effective: decl.e_env_4_runtime_broker_effective ?? null,
        noBrokerProcessOrDispatch: decl.e_env_4_no_broker_process_or_dispatch ?? null,
        binding,
        note: 'contract §5a requires no broker process or dispatch DURING the batch: completed only by the POST declaration (validate-run --env-post)',
      },
      { awaitingPost: pre4 === 'pass' }
    )
  );
  const e5 = decl.e_env_5;
  const st = e5 && typeof e5.result_status === 'number' ? e5.result_status : null;
  const t5 = ms(e5?.ts);
  const t5Ok =
    !Number.isNaN(t5) &&
    !Number.isNaN(ws) &&
    !Number.isNaN(batchStart) &&
    t5 >= ws &&
    t5 <= batchStart;
  out.push(
    g(
      'E-ENV-5',
      st !== null && st !== 401 && st !== 403
        ? 'fail'
        : !bound || st === null || !t5Ok
          ? 'inconclusive'
          : 'pass',
      { probe: e5?.probe ?? null, resultStatus: st, at: e5?.ts ?? null, tsInWindow: t5Ok, binding }
    )
  );
  return out;
}

/**
 * Pre-capture decision: any FAIL ⇒ stop (batch FAIL); any inconclusive
 * other than E-ENV-4 awaiting its POST evidence ⇒ stop (batch INCONCLUSIVE).
 */
export function envDecision(results) {
  const fails = results.filter((r) => r.outcome === 'fail').map((r) => r.gate);
  const missing = results
    .filter((r) => r.outcome === 'inconclusive' && !r.awaitingPost)
    .map((r) => r.gate);
  return {
    stop: fails.length > 0 || missing.length > 0,
    fails,
    missing,
    securityReport: fails.includes('E-ENV-5'),
  };
}

/** Back-compat helper: true when the batch must stop on E-ENV. */
export function envStop(results) {
  return envDecision(results).stop;
}

/**
 * POST declaration (written by the steward after the batch) completing
 * E-ENV-4 "during the batch" and re-asserting E-ENV-1: window must cover
 * [run.startedAt, run.endedAt]; same slotGeneration; dev-auth and runtime
 * broker effective false; no broker process/dispatch true.
 * @param {any} post
 * @param {{startedAt: string, endedAt: string, slotGeneration: string}} run
 */
export function evaluateEnvPost(post, run) {
  if (!post) return { outcome: 'inconclusive', problems: ['missing: POST env declaration'] };
  const problems = [];
  const fails = [];
  const ws = ms(post.window_start ?? post.window_ts);
  const we = ms(post.window_end);
  const rs = ms(run.startedAt);
  const re = ms(run.endedAt);
  if (post.slotGeneration !== run.slotGeneration) problems.push('POST slotGeneration != run');
  if (Number.isNaN(ws) || Number.isNaN(we) || Number.isNaN(rs) || Number.isNaN(re))
    problems.push('POST/run window timestamps missing or not UTC ISO');
  else if (!(ws <= rs && we >= re))
    problems.push('POST window does not cover [run.startedAt, run.endedAt]');
  if (post.e_env_1_dev_auth_effective === true)
    fails.push('E-ENV-1: dev-auth effective ON in POST window');
  else if (post.e_env_1_dev_auth_effective !== false)
    problems.push('POST e_env_1_dev_auth_effective missing');
  if (post.e_env_4_runtime_broker_effective === true)
    fails.push('E-ENV-4: runtime broker effective ON in POST window');
  else if (post.e_env_4_runtime_broker_effective !== false)
    problems.push('POST e_env_4_runtime_broker_effective missing');
  if (post.e_env_4_no_broker_process_or_dispatch === false)
    fails.push('E-ENV-4: broker process/dispatch during the batch');
  else if (post.e_env_4_no_broker_process_or_dispatch !== true)
    problems.push('POST e_env_4_no_broker_process_or_dispatch missing');
  return {
    outcome: fails.length ? 'fail' : problems.length ? 'inconclusive' : 'pass',
    problems: [...fails, ...problems],
  };
}

// ─── Capture record validation ───────────────────────────────────────────

const isEmpty = (v) =>
  v === undefined ||
  v === null ||
  (typeof v === 'string' && v.trim() === '') ||
  (Array.isArray(v) && v.length === 0);

const CAPTURE_REQUIRED = [
  'schemaVersion',
  'kind',
  'id',
  'createdAt',
  'producer',
  'runId',
  'contractSha256',
  'stateId',
  'profile',
  'substep',
  'status',
  'baseReleaseId',
  'baseReleaseSha256',
  'companionId',
  'companionSha256',
  'slotGeneration',
  'releaseKind',
  'phase',
  'runner',
  'mainJs',
  'batchValid',
  'readiness',
  'actions',
  'outcomes',
  'files',
  'startedAt',
  'endedAt',
  'evidenceMode',
];

/** @returns {string[]} */
export function validateCapture(rec) {
  const errs = [];
  for (const f of CAPTURE_REQUIRED) {
    if (f === 'outcomes' || f === 'files' || f === 'actions' || f === 'readiness') {
      if (!Array.isArray(rec[f])) errs.push(`${f} must be an array`);
    } else if (f === 'batchValid') {
      if (typeof rec.batchValid !== 'boolean') errs.push('batchValid must be boolean');
    } else if (isEmpty(rec[f])) errs.push(`missing ${f}`);
  }
  if (rec.schemaVersion !== 1) errs.push('schemaVersion must be 1');
  if (rec.kind !== CAPTURE_KIND) errs.push(`kind must be ${CAPTURE_KIND}`);
  if (rec.contractSha256 !== CONTRACT_SHA256)
    errs.push('contractSha256 is not the pinned FROZEN rev 3 digest');
  if (!SUBSTEPS.includes(rec.substep)) errs.push(`substep ${rec.substep} invalid`);
  if (!STATUSES.includes(rec.status)) errs.push(`status ${rec.status} invalid`);
  if (rec.status !== 'complete' && isEmpty(rec.errorReason))
    errs.push('non-complete record needs errorReason');
  if (!['evidence', 'debug-non-evidence'].includes(rec.evidenceMode))
    errs.push('evidenceMode invalid');
  for (const k of ['baseReleaseSha256', 'companionSha256'])
    if (rec[k] && !SHA_RE.test(rec[k]) && rec.evidenceMode === 'evidence')
      errs.push(`${k} not 64-hex`);
  for (const o of rec.outcomes ?? []) {
    if (isEmpty(o.clause)) errs.push('outcome without clause');
    if (!OUTCOMES.includes(o.outcome)) errs.push(`outcome ${o.clause}: invalid ${o.outcome}`);
    if (!Array.isArray(o.policyIds)) errs.push(`outcome ${o.clause}: policyIds must be an array`);
    if (o.outcome === 'pending')
      errs.push(`outcome ${o.clause}: unresolved pending in a final record`);
  }
  if (rec.status === 'complete' && rec.substep === 'M0' && rec.evidenceMode === 'evidence') {
    if (
      !rec.loadedScripts ||
      !rec.loadedMainEntry ||
      !SHA_RE.test(rec.loadedMainEntry.sha256 ?? '')
    )
      errs.push('M0 must record loaded script URLs and the loaded main entry sha256 (E-MAIN)');
  }
  for (const f of rec.files ?? []) {
    if (!f.path || path.isAbsolute(f.path) || f.path.split('/').includes('..'))
      errs.push(`file path not relative-safe: ${f.path}`);
    if (!SHA_RE.test(f.sha256 ?? '')) errs.push(`file ${f.path} has no sha256`);
  }
  return errs;
}

/**
 * Validate a whole run against its Release pair: every capture pins both
 * SHAs, slotGeneration, the companion's runner commit + suite digest, the
 * Release main.js pre AND post, its own M0 loaded-entry digest, and every
 * referenced file's bytes. Missing ⇒ `missing:`; disagreement ⇒ `mismatch:`.
 */
export function validateRun(runDir, baseFile, companionFile, envPostFile) {
  const errs = [];
  const baseBytes = fs.readFileSync(baseFile);
  const compBytes = fs.readFileSync(companionFile);
  const base = JSON.parse(baseBytes.toString('utf-8'));
  const comp = JSON.parse(compBytes.toString('utf-8'));
  const baseSha = sha256(baseBytes);
  const compSha = sha256(compBytes);
  errs.push(...validatePair({ baseBytes, companion: comp }).map((e) => `pair ${e}`));
  const runFile = path.join(runDir, 'run.json');
  if (!fs.existsSync(runFile)) return [...errs, 'missing: run.json'];
  const run = JSON.parse(fs.readFileSync(runFile, 'utf-8'));
  if (run.kind !== RUN_KIND) errs.push(`mismatch: run.json kind must be ${RUN_KIND}`);
  if (run.evidenceMode !== 'evidence') errs.push('mismatch: run is debug-non-evidence');
  if (run.batchValid !== true) errs.push('mismatch: run.json batchValid is not true');
  if (run.baseReleaseSha256 !== baseSha) errs.push('mismatch: run.baseReleaseSha256 != base file');
  if (run.companionSha256 !== compSha) errs.push('mismatch: run.companionSha256 != companion file');
  if (run.runner?.suiteDigest !== comp.runner?.suiteDigest)
    errs.push('mismatch: run suite digest != companion');
  if (run.runner?.head !== comp.runner?.commit)
    errs.push('mismatch: run runner HEAD != companion runner.commit');
  if (run.contractSha256 !== CONTRACT_SHA256) errs.push('mismatch: run contractSha256');
  if (run.aborted) errs.push(`mismatch: run aborted: ${run.abortReason ?? 'unknown'}`);
  // E-ENV (§5a), independently of batchValid: every PRE gate must PASS except
  // E-ENV-4, which is completed by the POST declaration; the runner's own
  // post-batch anonymous probe must be 401.
  for (const gte of run.env ?? []) {
    if (gte.outcome === 'fail') errs.push(`mismatch: ${gte.gate} FAIL (pre window)`);
    else if (gte.outcome !== 'pass' && !(gte.gate === 'E-ENV-4' && gte.awaitingPost))
      errs.push(`missing: ${gte.gate} ${gte.outcome} (pre window)`);
  }
  if (!Array.isArray(run.env) || run.env.length !== 5)
    errs.push('missing: run.env must carry E-ENV-1..5');
  if (run.envPostSelfAnon401?.status !== 401)
    errs.push(
      `mismatch: post-batch anonymous probe status ${run.envPostSelfAnon401?.status ?? 'missing'} (E-ENV-2)`
    );
  if (envPostFile === undefined) {
    errs.push('missing: POST env declaration not supplied (--env-post); E-ENV-4 INCONCLUSIVE');
  } else {
    const post = fs.existsSync(envPostFile)
      ? JSON.parse(fs.readFileSync(envPostFile, 'utf-8'))
      : null;
    const r = evaluateEnvPost(post, {
      startedAt: run.startedAt,
      endedAt: run.endedAt,
      slotGeneration: base.slotGeneration,
    });
    if (r.outcome === 'fail') errs.push(...r.problems.map((p) => `mismatch: POST env ${p}`));
    else if (r.outcome !== 'pass') errs.push(...r.problems.map((p) => `missing: POST env ${p}`));
  }
  // Completeness (review1 N5): every expected (state, profile, substep)
  // record exists, and every complete M0 carries a primary screenshot.
  const present = new Set(fs.readdirSync(runDir));
  if (!Array.isArray(run.expectedRecords) || run.expectedRecords.length === 0)
    errs.push('missing: run.json expectedRecords');
  for (const name of run.expectedRecords ?? [])
    if (!present.has(name)) errs.push(`missing: expected record ${name}`);
  const files = fs.readdirSync(runDir).filter((f) => f.endsWith('.capture.json'));
  if (files.length === 0) errs.push('missing: no capture records');
  const ids = new Set();
  for (const f of files) {
    const c = JSON.parse(fs.readFileSync(path.join(runDir, f), 'utf-8'));
    ids.add(c.id);
    errs.push(...validateCapture(c).map((e) => `${f}: ${e}`));
    const m = (cond, msg) => cond || errs.push(`${f}: mismatch: ${msg}`);
    m(c.runId === run.runId, 'runId');
    m(c.baseReleaseSha256 === baseSha, 'baseReleaseSha256');
    m(c.companionSha256 === compSha, 'companionSha256');
    m(c.baseReleaseId === base.id, 'baseReleaseId');
    m(c.companionId === comp.id, 'companionId');
    m(c.slotGeneration === base.slotGeneration, 'slotGeneration');
    m(c.releaseKind === base.releaseKind, 'releaseKind');
    m(c.phase === comp.phase, 'phase');
    m(c.runner?.suiteDigest === comp.runner?.suiteDigest, 'runner suite digest');
    m(c.runner?.head === comp.runner?.commit, 'runner HEAD');
    m(c.mainJs?.expected === base.mainJsSha256, 'mainJs.expected');
    m(c.mainJs?.pre?.sha256 === base.mainJsSha256, 'mainJs.pre');
    m(c.mainJs?.post?.sha256 === base.mainJsSha256, 'mainJs.post');
    m(c.batchValid === true, 'batchValid');
    if (c.status === 'complete' && c.substep === 'M0')
      m(
        c.loadedMainEntry?.sha256 === base.mainJsSha256,
        'loaded main entry sha256 != Release mainJsSha256'
      );
    if (c.status === 'complete' && c.substep === 'M0') {
      const prim = (c.files ?? []).filter((x) => x.kind === 'M0-primary-screenshot-fullpage');
      if (prim.length !== 1) errs.push(`${f}: missing: M0-primary full-page screenshot`);
    }
    for (const fe of c.files ?? []) {
      const abs = path.join(runDir, fe.path);
      if (!fs.existsSync(abs)) errs.push(`${f}: missing: file ${fe.path}`);
      else if (sha256(fs.readFileSync(abs)) !== fe.sha256)
        errs.push(`${f}: mismatch: file ${fe.path} sha256`);
    }
  }
  for (const id of run.captureIds ?? [])
    if (!ids.has(id)) errs.push(`missing: capture ${id} listed in run.json`);
  if ((run.captureIds ?? []).length !== ids.size)
    errs.push('mismatch: run.json captureIds count != capture files');
  return errs;
}

// ─── E7 quarantine ledger ────────────────────────────────────────────────

export function emptyLedger() {
  return { schemaVersion: 1, kind: 'wave01-quarantine-ledger', states: {}, history: [] };
}

export function loadLedger(file) {
  if (!fs.existsSync(file)) return emptyLedger();
  const l = JSON.parse(fs.readFileSync(file, 'utf-8'));
  if (l.kind !== 'wave01-quarantine-ledger') throw new Error('not a quarantine ledger');
  return l;
}

/**
 * Apply one batch's records (in capture order). A capture-error substep
 * increments the state's consecutive count; a complete substep resets it;
 * blocked records leave it unchanged. ≥ QUARANTINE_AFTER ⇒ quarantined
 * until an owner clears it.
 */
export function applyBatch(ledger, runId, records) {
  const next = JSON.parse(JSON.stringify(ledger));
  for (const r of records) {
    const s = (next.states[r.stateId] ??= {
      consecutiveErrors: 0,
      quarantined: false,
      lastRunId: null,
      errorIds: [],
    });
    if (r.status === 'capture-error') {
      s.consecutiveErrors++;
      s.errorIds.push(r.id);
    } else if (r.status === 'complete') {
      s.consecutiveErrors = 0;
      s.errorIds = [];
    }
    s.lastRunId = runId;
    if (s.consecutiveErrors >= QUARANTINE_AFTER && !s.quarantined) {
      s.quarantined = true;
      next.history.push({
        at: new Date().toISOString(),
        event: 'quarantined',
        stateId: r.stateId,
        runId,
        errorIds: [...s.errorIds],
      });
    }
  }
  return next;
}

export function isQuarantined(ledger, stateId) {
  return !!ledger.states[stateId]?.quarantined;
}

export function clearQuarantine(ledger, stateId, by, reason) {
  if (!by || !reason)
    throw new Error('clearing quarantine needs --by and --reason (owner resolution)');
  const next = JSON.parse(JSON.stringify(ledger));
  const s = next.states[stateId];
  if (!s?.quarantined) throw new Error(`${stateId} is not quarantined`);
  s.quarantined = false;
  s.consecutiveErrors = 0;
  s.errorIds = [];
  next.history.push({ at: new Date().toISOString(), event: 'cleared', stateId, by, reason });
  return next;
}

/** Write the ledger atomically (temp + rename); the ledger is steward state, not evidence. */
export function saveLedger(file, ledger) {
  const tmp = `${file}.tmp-${process.pid}`;
  fs.writeFileSync(tmp, JSON.stringify(ledger, null, 2) + '\n', { mode: 0o644 });
  fs.renameSync(tmp, file);
}

function arg(argv, k) {
  const i = argv.indexOf(`--${k}`);
  if (i < 0 || !argv[i + 1]) throw new Error(`--${k} is required`);
  return argv[i + 1];
}

function main() {
  const [cmd, ...rest] = process.argv.slice(2);
  if (cmd === 'validate-run') {
    const dir = rest[0];
    if (!dir || dir.startsWith('--'))
      throw new Error('validate-run RUN_DIR --base FILE --companion FILE');
    const i = rest.indexOf('--env-post');
    const errs = validateRun(
      dir,
      arg(rest, 'base'),
      arg(rest, 'companion'),
      i >= 0 ? arg(rest, 'env-post') : undefined
    );
    console.log(errs.length ? `INVALID ${dir}\n  ${errs.join('\n  ')}` : `ok      ${dir}`);
    process.exit(errs.length ? 1 : 0);
  }
  if (cmd === 'quarantine-status') {
    console.log(JSON.stringify(loadLedger(arg(rest, 'ledger')), null, 2));
    return;
  }
  if (cmd === 'quarantine-clear') {
    const file = arg(rest, 'ledger');
    saveLedger(
      file,
      clearQuarantine(loadLedger(file), arg(rest, 'state'), arg(rest, 'by'), arg(rest, 'reason'))
    );
    console.log('cleared');
    return;
  }
  console.error('usage: records.mjs validate-run|quarantine-status|quarantine-clear …');
  process.exit(2);
}

if (import.meta.url === `file://${process.argv[1]}`) {
  try {
    main();
  } catch (e) {
    console.error(String(e instanceof Error ? e.message : e));
    process.exit(2);
  }
}
