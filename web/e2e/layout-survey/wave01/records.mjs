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
//   node wave01/records.mjs validate-run RUN_DIR --base FILE --companion FILE --env-post FILE [--out FILE]
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

/**
 * §5a E-ENV-1 "across service unit/args, settings file and environment".
 * Ruling R-6 (assessor 16:11–16:12Z): each class records RAW layer values;
 * the effective value is DERIVED through the served backend's precedence
 * (source-checked at 1694e511, = backend 4a253489):
 *   config value  = SCION_SERVER_* env > local settings > global settings >
 *                   embedded default (auth.devMode=false, auth.mode/mode unset)
 *                   (pkg/config/hub_config.go:966-970, 1036-1043, 1536-1546, 1817-1822)
 *   hosted        = --hosted/--production if either flag is set, else config
 *                   mode ∈ {hosted, production} (cmd/server.go:235-236,
 *                   cmd/server_foreground.go:1013-1018)
 *   devMode       = explicit --dev-auth, else ON when non-hosted (workstation
 *                   defaults), else config devMode
 *                   (server_foreground.go:1020-1024, 1064-1066; server_config.go:35-36)
 *   auth.mode     = config auth.mode ("dev" = exclusive dev human auth)
 * "absent" must be explicit; a missing input is UNKNOWN, never defaulted.
 */
export const E_ENV_1_SOURCE_CLASSES = Object.freeze(['unit/args', 'settings', 'environment']);
export const E_ENV_1_FLAGS = Object.freeze(['--dev-auth', '--hosted', '--production']);
export const E_ENV_1_SETTINGS_KEYS = Object.freeze(['auth.devMode', 'auth.mode', 'mode']);
export const E_ENV_1_ENV_KEYS = Object.freeze({
  'auth.devMode': 'SCION_SERVER_AUTH_DEVMODE',
  'auth.mode': 'SCION_SERVER_AUTH_MODE',
  mode: 'SCION_SERVER_MODE',
});
const CONFIG_DEFAULTS = Object.freeze({ 'auth.devMode': false, 'auth.mode': null, mode: null });
const UNKNOWN = Symbol('unknown');

/** Shared environment booleans compared between same-slot PRE and POST (review2 RB3). */
export const SHARED_ENV_BOOLEANS = Object.freeze([
  'e_env_1_dev_auth_effective',
  'e_env_3_test_login_enabled',
  'e_env_4_runtime_broker_effective',
  'e_env_4_no_broker_process_or_dispatch',
]);

const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v);
const okBool = (v) => typeof v === 'boolean' || v === 'absent';
const okStr = (v) => (typeof v === 'string' && v !== '') || v === 'absent';

/**
 * Derive effective dev-auth from raw layers (R-6). Returns the derivation
 * with `unknown` markers instead of guessing; `problems` lists missing or
 * malformed inputs.
 * @param {unknown} sources
 */
export function deriveDevAuth(sources) {
  const problems = [];
  const src = isObj(sources) ? sources : null;
  if (!src) problems.push('e_env_1_sources missing or not an object');
  for (const k of Object.keys(src ?? {}))
    if (![...E_ENV_1_SOURCE_CLASSES, 'keys_checked'].includes(k))
      problems.push(`unknown class ${k}`);
  // unit/args
  const ua = src && isObj(src['unit/args']) ? src['unit/args'] : null;
  if (src && !ua) problems.push('class unit/args missing/malformed');
  const flag = (name) => {
    if (!ua || !(name in ua)) {
      if (ua) problems.push(`unit/args ${name} missing`);
      return UNKNOWN;
    }
    if (!okBool(ua[name])) {
      problems.push(`unit/args ${name} malformed`);
      return UNKNOWN;
    }
    return ua[name];
  };
  const devFlag = flag('--dev-auth');
  const hostedFlag = flag('--hosted');
  const prodFlag = flag('--production');
  // settings + environment layers → config values
  const st = src && isObj(src.settings) ? src.settings : null;
  if (src && !st) problems.push('class settings missing/malformed');
  const env = src && isObj(src.environment) ? src.environment : null;
  if (src && !env) problems.push('class environment missing/malformed');
  const layerValue = (layer, key, valid) => {
    // returns value | 'absent' | UNKNOWN
    if (layer === 'absent') return 'absent';
    if (!isObj(layer) || !(key in layer) || !valid(layer[key])) return UNKNOWN;
    return layer[key];
  };
  const fileLayer = (scope) => {
    if (!st || !(scope in st)) {
      if (st)
        problems.push(`settings.${scope} missing (write "absent" if the file does not exist)`);
      return UNKNOWN;
    }
    const l = st[scope];
    if (l !== 'absent' && !isObj(l)) {
      problems.push(`settings.${scope} malformed`);
      return UNKNOWN;
    }
    return l;
  };
  const globalL = fileLayer('global');
  const localL = fileLayer('local');
  const resolve = (key, valid) => {
    // env > local > global > default; an UNKNOWN layer blocks resolution
    // unless a higher layer already decided.
    const envKey = E_ENV_1_ENV_KEYS[key];
    const chain = [
      [
        'environment',
        env ? (envKey in env && valid(env[envKey]) ? env[envKey] : UNKNOWN) : UNKNOWN,
      ],
      ['settings.local', localL === UNKNOWN ? UNKNOWN : layerValue(localL, key, valid)],
      ['settings.global', globalL === UNKNOWN ? UNKNOWN : layerValue(globalL, key, valid)],
    ];
    for (const [name, v] of chain) {
      if (v === UNKNOWN) {
        problems.push(`${name}: ${key} missing/malformed`);
        return { value: UNKNOWN, from: name };
      }
      if (v !== 'absent') return { value: v, from: name };
    }
    return { value: CONFIG_DEFAULTS[key], from: 'embedded-default' };
  };
  const devCfg = resolve('auth.devMode', okBool);
  const authMode = resolve('auth.mode', okStr);
  const modeCfg = resolve('mode', okStr);
  // hosted
  let hosted;
  if (hostedFlag === UNKNOWN || prodFlag === UNKNOWN) hosted = UNKNOWN;
  else if (hostedFlag !== 'absent' || prodFlag !== 'absent')
    hosted = hostedFlag === true || prodFlag === true;
  else
    hosted =
      modeCfg.value === UNKNOWN
        ? UNKNOWN
        : modeCfg.value === 'hosted' || modeCfg.value === 'production';
  // effective devMode
  let devMode;
  if (devFlag === true) devMode = true;
  else if (devFlag === false) devMode = false;
  else if (devFlag === UNKNOWN) devMode = UNKNOWN;
  else if (hosted === false) devMode = true;
  else if (hosted === true) devMode = devCfg.value;
  else devMode = devCfg.value === true ? true : UNKNOWN; // ON in both branches
  const show = (v) => (v === UNKNOWN ? 'unknown' : v);
  return {
    effectiveDevMode: show(devMode),
    effectiveAuthMode: show(authMode.value),
    hosted: show(hosted),
    flags: {
      '--dev-auth': show(devFlag),
      '--hosted': show(hostedFlag),
      '--production': show(prodFlag),
    },
    config: {
      'auth.devMode': { value: show(devCfg.value), from: devCfg.from },
      'auth.mode': { value: show(authMode.value), from: authMode.from },
      mode: { value: show(modeCfg.value), from: modeCfg.from },
    },
    complete: problems.length === 0,
    problems: Array.from(new Set(problems)),
  };
}

/**
 * Grade E-ENV-1 from an ATTRIBUTABLE declaration per the R-6 order:
 * FAIL if a forbidden state is established (derived devMode ON, effective
 * auth.mode "dev", or declared effective ON) — even with other inputs
 * missing; INCONCLUSIVE if OFF is not proven; PASS only with complete
 * explicit evidence, derived OFF, hosted evidenced and declared OFF.
 */
export function gradeEEnv1(decl) {
  const d = deriveDevAuth(decl?.e_env_1_sources);
  const declared = decl?.e_env_1_dev_auth_effective;
  const reasons = [];
  if (d.effectiveDevMode === true) reasons.push('derived effective devMode ON');
  if (d.effectiveAuthMode === 'dev') reasons.push('effective auth.mode "dev"');
  if (declared === true) reasons.push('declared e_env_1_dev_auth_effective ON');
  if (reasons.length) {
    return {
      outcome: 'fail',
      reasons,
      derivation: d,
      declared,
      declaredVsDerived: declared === d.effectiveDevMode ? 'agree' : 'disagree',
    };
  }
  const open = [];
  if (!d.complete) open.push(...d.problems);
  if (d.effectiveDevMode !== false) open.push(`effective devMode ${d.effectiveDevMode}`);
  if (d.effectiveAuthMode === 'unknown') open.push('effective auth.mode unknown');
  if (d.hosted !== true) open.push(`hosted mode not evidenced (${d.hosted})`);
  if (declared !== false)
    open.push(
      `declared e_env_1_dev_auth_effective ${declared === undefined ? 'missing' : 'malformed'}`
    );
  return { outcome: open.length ? 'inconclusive' : 'pass', reasons: open, derivation: d, declared };
}

/** Canonical comparison form of a sources object (for PRE/POST equality). */
export function sourcesKey(sources) {
  if (!isObj(sources)) return null;
  const canon = (v) =>
    isObj(v)
      ? Object.fromEntries(
          Object.keys(v)
            .sort()
            .map((k) => [k, canon(v[k])])
        )
      : Array.isArray(v)
        ? v.map(canon)
        : v;
  const { keys_checked: _k, ...rest } = sources;
  return JSON.stringify(canon(rest));
}

/** Value snapshot of the shared booleans of a declaration (recorded in run.json). */
export function sharedEnvValues(decl) {
  return Object.fromEntries(
    SHARED_ENV_BOOLEANS.map((k) => [k, typeof decl?.[k] === 'boolean' ? decl[k] : null])
  );
}

/** Default maximum age (minutes) of the PRE declaration's window_start at batch start. */
export const DEFAULT_ENV_MAX_AGE_MIN = 30; // ii2-confirmed 15:49Z

const ms = (iso) => {
  if (typeof iso !== 'string') return NaN;
  const t = Date.parse(iso);
  return /Z$/.test(iso) ? t : NaN;
};

const IP_RE = /^(\d{1,3}(\.\d{1,3}){3}|\[[0-9a-f:]+\])$/i;

/**
 * Canonical origin of a served-target URL, as Release validation uses it:
 * http(s), no credentials/path/query/fragment, configured hostname (never a
 * raw IP). Returns null when it is not such a URL.
 * @param {unknown} v
 */
export function canonicalOrigin(v) {
  if (typeof v !== 'string') return null;
  let u;
  try {
    u = new URL(v);
  } catch {
    return null;
  }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') return null;
  if (u.username || u.password || (u.pathname && u.pathname !== '/') || u.search || u.hash)
    return null;
  if (IP_RE.test(u.hostname)) return null;
  return u.origin;
}

/**
 * Host binding of an env declaration (ii2 + review2 amendment 15:54Z; owner
 * 15:55Z: parsed canonical origin, compared exactly as the Release pair is,
 * no aliases or substrings). `baseURL` is required. Any status other than
 * 'ok' (missing, unparseable, raw IP, or a different origin) means the
 * declaration is not evidence for THIS run ⇒ INCONCLUSIVE (assessor ruling
 * relayed 15:55Z); 'mismatch' is still reported distinctly.
 * @param {any} decl
 * @param {string} runBaseURL
 * @returns {{status: 'ok' | 'missing' | 'mismatch', declared: string | null, runOrigin: string | null}}
 */
export function hostBinding(decl, runBaseURL) {
  const runOrigin = canonicalOrigin(runBaseURL);
  const declared = typeof decl?.baseURL === 'string' ? decl.baseURL : null;
  const d = canonicalOrigin(declared);
  if (!runOrigin || !d) return { status: 'missing', declared, runOrigin };
  return { status: d === runOrigin ? 'ok' : 'mismatch', declared, runOrigin };
}

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
 * @param {{slotGeneration: string, baseURL: string}} release
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
    host: hostBinding(decl, release.baseURL),
  };
  const hostStatus = binding.host.status;
  // Ruling R-3: a declaration not bound to THIS slot (host + generation) is
  // unattributable ⇒ every gate it feeds is INCONCLUSIVE, whatever its
  // values say; contradictions are FAIL only on an attributable declaration.
  const attributable = genOk && hostStatus === 'ok';
  if (!genOk)
    binding.bindingMismatch = {
      declared: decl.slotGeneration ?? null,
      release: release.slotGeneration,
    };
  const bound = attributable && windowOk;
  const bool3 = (v, good) =>
    typeof v !== 'boolean' ? 'inconclusive' : v === good ? 'pass' : 'fail';
  // A contradiction is a FAIL even on a stale/mismatched declaration; missing/stale otherwise ⇒ inconclusive.
  const grade = (v, good, extraOk = true) => {
    if (!attributable) return 'inconclusive';
    const b = bool3(v, good);
    if (b === 'fail') return 'fail';
    if (!bound || !extraOk) return 'inconclusive';
    return b;
  };
  const e1 = gradeEEnv1(decl);
  const out = [
    g(
      'E-ENV-1',
      !attributable
        ? 'inconclusive'
        : e1.outcome === 'fail'
          ? 'fail'
          : !bound
            ? 'inconclusive'
            : e1.outcome,
      {
        rule: 'R-6 derivation (raw layers through backend precedence)',
        ...e1,
        binding,
      }
    ),
    (() => {
      // Own probe is graded independently of any declaration. A BOUND
      // declaration whose steward anonymous probe is ≠ 401 is a declared
      // forbidden state ⇒ FAIL (review2 RB1, R-3). The steward probe's
      // timestamp-in-window is RECORDED only, pending an assessor ruling.
      const own = selfOutcome(ws);
      const sw = decl.e_env_2_anon_401;
      const swStatus = sw && typeof sw.status === 'number' ? sw.status : null;
      const swTs = ms(sw?.ts);
      const swTsInWindow =
        !Number.isNaN(swTs) &&
        !Number.isNaN(ws) &&
        !Number.isNaN(batchStart) &&
        swTs >= ws &&
        swTs <= batchStart;
      const stewardContradicts = attributable && swStatus !== null && swStatus !== 401;
      return g('E-ENV-2', own === 'fail' || stewardContradicts ? 'fail' : own, {
        self: selfAnon401,
        selfOutcome: own,
        steward: sw ?? null,
        stewardStatusGraded: attributable ? swStatus : null,
        stewardContradicts,
        stewardTsInWindow: swTsInWindow,
        stewardTsRule:
          'recorded only (pending assessor ruling on review2 RB1 timestamp restriction)',
        windowStart: wsIso ?? null,
      });
    })(),
    g(
      'E-ENV-3',
      bound && decl.e_env_3_test_login_enabled === true ? 'pass' : 'inconclusive',
      {
        testLoginEnabled: decl.e_env_3_test_login_enabled ?? null,
        note:
          decl.e_env_3_test_login_enabled === false
            ? 'declared test-login DISABLED: the capture principal authenticates via test-login, so capture cannot proceed (stop); if the runner nevertheless authenticates in the window, validate-run grades the contradiction FAIL'
            : 'recorded separately; capture principal is synthetic',
        binding,
      },
      { awaitingAuthCheck: bound && decl.e_env_3_test_login_enabled === false }
    ),
  ];
  const eff4 = bool3(decl.e_env_4_runtime_broker_effective, false);
  const nob4 = bool3(decl.e_env_4_no_broker_process_or_dispatch, true);
  const pre4 = !attributable
    ? 'inconclusive'
    : eff4 === 'fail' || nob4 === 'fail'
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
      !attributable
        ? 'inconclusive'
        : st !== null && st !== 401 && st !== 403
          ? 'fail'
          : !bound || st === null || !t5Ok
            ? 'inconclusive'
            : 'pass',
      { probe: e5?.probe ?? null, resultStatus: st, at: e5?.ts ?? null, tsInWindow: t5Ok, binding }
    )
  );
  if (hostStatus === 'mismatch') {
    // Assessor grading ruling (relayed by ii2 15:55Z): a declaration not
    // bound to this run's host is MISSING evidence for this run ⇒
    // INCONCLUSIVE (bound=false above), never PASS; FAIL stays reserved for
    // contradictions. Recorded explicitly.
    for (const gte of out) {
      if (gte.gate === 'E-ENV-2') continue;
      gte.details = { ...gte.details, crossHost: binding.host };
    }
  }
  return out;
}

/**
 * Pre-capture decision: any FAIL ⇒ stop (batch FAIL); any inconclusive
 * other than E-ENV-4 awaiting its POST evidence ⇒ stop (batch INCONCLUSIVE).
 */
export function envDecision(results) {
  const fails = results.filter((r) => r.outcome === 'fail').map((r) => r.gate);
  const missing = results
    .filter((r) => r.outcome === 'inconclusive' && !r.awaitingPost && !r.awaitingAuthCheck)
    .map((r) => r.gate);
  const authCheckPending = results.some((r) => r.awaitingAuthCheck);
  return {
    stop: fails.length > 0 || missing.length > 0 || authCheckPending,
    fails,
    missing,
    authCheckPending,
    securityReport: fails.includes('E-ENV-5'),
  };
}

/**
 * review2 RB3 / assessor 16:03Z: a BOUND PRE declaring test-login disabled is
 * checked against the runner's own test-login attempt. Success contradicts
 * the declaration ⇒ E-ENV-3 FAIL; refusal is consistent ⇒ INCONCLUSIVE
 * (capture impossible). Either way the batch stops.
 * @param {Array<any>} results
 * @param {{succeeded: boolean, at: string, detail?: string}} attempt
 */
export function applyAuthCheck(results, attempt) {
  return results.map((r) => {
    if (r.gate !== 'E-ENV-3' || !r.awaitingAuthCheck) return r;
    return {
      ...r,
      outcome: attempt.succeeded ? 'fail' : 'inconclusive',
      awaitingAuthCheck: false,
      details: {
        ...r.details,
        authCheck: {
          ...attempt,
          verdict: attempt.succeeded
            ? 'contradiction: runner test-login succeeded although the bound PRE declares it disabled'
            : 'consistent: runner test-login refused; capture impossible',
        },
      },
    };
  });
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
 * @param {{startedAt: string, endedAt: string, slotGeneration: string, baseURL: string, preBaseURL?: string, preValues?: Record<string, boolean | null>, preSourcesKey?: string | null, testLoginUsed?: boolean}} run
 */
export function evaluateEnvPost(post, run) {
  if (!post) return { outcome: 'inconclusive', problems: ['missing: POST env declaration'] };
  const problems = [];
  const fails = [];
  const ws = ms(post.window_start ?? post.window_ts);
  const we = ms(post.window_end);
  const rs = ms(run.startedAt);
  const re = ms(run.endedAt);
  if (post.slotGeneration !== run.slotGeneration)
    problems.push(
      `bindingMismatch: POST slotGeneration ${post.slotGeneration ?? null} != ${run.slotGeneration}`
    );
  const hb = hostBinding(post, run.baseURL);
  if (hb.status === 'mismatch')
    problems.push(
      `cross-host: POST baseURL ${hb.declared} != run ${hb.runOrigin} (not evidence for this run)`
    );
  else if (hb.status !== 'ok')
    problems.push(`POST baseURL ${hb.status} (canonical http(s) origin required)`);
  if (run.preBaseURL !== undefined) {
    const hp = hostBinding(post, run.preBaseURL);
    if (hp.status !== 'ok')
      problems.push(
        `POST baseURL ${hb.declared} != PRE baseURL ${run.preBaseURL} (not evidence for this run)`
      );
  }
  if (Number.isNaN(ws) || Number.isNaN(we) || Number.isNaN(rs) || Number.isNaN(re))
    problems.push('POST/run window timestamps missing or not UTC ISO');
  else if (!(ws <= rs && we >= re))
    problems.push('POST window does not cover [run.startedAt, run.endedAt]');
  if (post.e_env_4_runtime_broker_effective === true)
    fails.push('E-ENV-4: runtime broker effective ON in POST window');
  else if (post.e_env_4_runtime_broker_effective !== false)
    problems.push('POST e_env_4_runtime_broker_effective missing');
  if (post.e_env_4_no_broker_process_or_dispatch === false)
    fails.push('E-ENV-4: broker process/dispatch during the batch');
  else if (post.e_env_4_no_broker_process_or_dispatch !== true)
    problems.push('POST e_env_4_no_broker_process_or_dispatch missing');
  // review2 RB3: test-login state required on POST; E-ENV-1 sources complete;
  // steward anonymous probe (if present) must be 401; same-slot PRE/POST
  // shared booleans must agree; a POST claiming test-login disabled while the
  // runner authenticated via test-login in the window is a contradiction.
  if (typeof post.e_env_3_test_login_enabled !== 'boolean')
    problems.push('POST e_env_3_test_login_enabled missing');
  else if (post.e_env_3_test_login_enabled === false && run.testLoginUsed === true)
    fails.push(
      'E-ENV-3: POST declares test-login disabled, but the runner authenticated via test-login during the batch'
    );
  if (post.e_env_1_dev_auth_effective !== undefined || post.e_env_1_sources !== undefined) {
    // R-4/R-6: optional on POST; if carried it must derive cleanly and equal PRE.
    const pe1 = gradeEEnv1(post);
    if (pe1.outcome === 'fail') fails.push(`E-ENV-1 (POST): ${pe1.reasons.join('; ')}`);
    else if (pe1.outcome !== 'pass')
      problems.push(`POST E-ENV-1 not proven OFF: ${pe1.reasons.join('; ')}`);
    else if (run.preSourcesKey && sourcesKey(post.e_env_1_sources) !== run.preSourcesKey)
      fails.push('same-slot PRE/POST disagreement on e_env_1_sources layer values');
  }
  const sw = post.e_env_2_anon_401;
  if (sw && typeof sw.status === 'number' && sw.status !== 401)
    fails.push(`E-ENV-2: POST steward anonymous probe status ${sw.status}`);
  if (run.preValues) {
    for (const k of SHARED_ENV_BOOLEANS) {
      const a = run.preValues[k];
      const b = typeof post[k] === 'boolean' ? post[k] : null;
      if (a === null || a === undefined || b === null) continue; // missing values are reported above/elsewhere
      if (a !== b) fails.push(`same-slot PRE/POST disagreement on ${k}: PRE ${a}, POST ${b}`);
    }
  } else problems.push('missing: PRE shared env values (run.envPreValues)');
  // Ruling R-3: a POST declaration not bound to this run's host (or not
  // matching PRE's host) or generation is unattributable ⇒ INCONCLUSIVE; its
  // value contradictions are recorded but not graded FAIL.
  const attributable =
    post.slotGeneration === run.slotGeneration &&
    hb.status === 'ok' &&
    (run.preBaseURL === undefined || hostBinding(post, run.preBaseURL).status === 'ok');
  if (!attributable && fails.length) {
    problems.push(
      ...fails.map((f) => `unattributable (crossHost/bindingMismatch), not graded: ${f}`)
    );
    fails.length = 0;
  }
  return {
    outcome: fails.length ? 'fail' : problems.length ? 'inconclusive' : 'pass',
    problems: [...fails, ...problems],
    attributable,
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
  if (!run.envPreValues) errs.push('missing: run.envPreValues (PRE shared env values)');
  if (!run.envPreBaseURL) errs.push('missing: run.envPreBaseURL (PRE declaration baseURL)');
  else if (hostBinding({ baseURL: run.envPreBaseURL }, base.baseURL).status !== 'ok')
    errs.push(
      `missing: PRE env baseURL ${run.envPreBaseURL} is not bound to Release baseURL ${base.baseURL} (INCONCLUSIVE)`
    );
  if (run.baseURL && new URL(run.baseURL).origin !== new URL(base.baseURL).origin)
    errs.push('mismatch: run.baseURL != Release baseURL');
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
      baseURL: run.baseURL ?? base.baseURL,
      preBaseURL: run.envPreBaseURL ?? undefined,
      preValues: run.envPreValues ?? undefined,
      preSourcesKey: run.envPreSourcesKey ?? undefined,
      testLoginUsed:
        Array.isArray(run.issuanceInventory?.credentials) &&
        run.issuanceInventory.credentials.length > 0,
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

/**
 * Coverage counts per state, separate from validity (review2 N-j): a run can
 * validate while every slice record is BLOCKED; this makes that visible.
 * @param {string} runDir
 */
export function coverageSummary(runDir) {
  const perState = {};
  for (const f of fs.readdirSync(runDir).filter((x) => x.endsWith('.capture.json'))) {
    const c = JSON.parse(fs.readFileSync(path.join(runDir, f), 'utf-8'));
    const st = (perState[c.stateId] ??= { complete: 0, captureError: 0, blocked: 0 });
    if (c.status === 'complete') st.complete++;
    else if (c.status === 'capture-error') st.captureError++;
    else st.blocked++;
  }
  const totals = Object.values(perState).reduce(
    (a, b) => ({
      complete: a.complete + b.complete,
      captureError: a.captureError + b.captureError,
      blocked: a.blocked + b.blocked,
    }),
    { complete: 0, captureError: 0, blocked: 0 }
  );
  return { perState, totals };
}

/**
 * Immutable validation result bound to the exact inputs (owner 16:04Z: the
 * passing validate-run output, not run.json batchValid, is the final
 * environment-validity evidence). `classification` follows §0/§5a: any
 * `mismatch:` ⇒ REJECTED-FAIL-OR-INVALID; only `missing:` ⇒ INCONCLUSIVE.
 */
export function validationRecord(runDir, baseFile, companionFile, envPostFile, errs) {
  const files = fs
    .readdirSync(runDir)
    .filter((f) => fs.statSync(path.join(runDir, f)).isFile())
    .sort()
    .map((f) => ({ path: f, sha256: sha256(fs.readFileSync(path.join(runDir, f))) }));
  const fileSha = (f) => (f && fs.existsSync(f) ? sha256(fs.readFileSync(f)) : null);
  const mismatches = errs.filter((e) => /mismatch:/.test(e));
  return {
    schemaVersion: 1,
    kind: 'wave01-validation',
    createdAt: new Date().toISOString(),
    contractSha256: CONTRACT_SHA256,
    runDir: path.basename(runDir),
    verdict: errs.length === 0 ? 'accepted' : 'rejected',
    classification:
      errs.length === 0
        ? 'VALID'
        : mismatches.length
          ? 'REJECTED (FAIL or invalid)'
          : 'INCONCLUSIVE',
    inputs: {
      runJsonSha256: fileSha(path.join(runDir, 'run.json')),
      baseReleaseSha256: fileSha(baseFile),
      companionSha256: fileSha(companionFile),
      envPostSha256: fileSha(envPostFile),
      files,
    },
    errors: errs,
    coverage: coverageSummary(runDir),
  };
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
    const o = rest.indexOf('--out');
    const envPost = i >= 0 ? arg(rest, 'env-post') : undefined;
    const rec = validationRecord(dir, arg(rest, 'base'), arg(rest, 'companion'), envPost, errs);
    if (o >= 0) {
      const out = arg(rest, 'out');
      fs.writeFileSync(out, JSON.stringify(rec, null, 2) + '\n', { flag: 'wx', mode: 0o444 });
      console.log(`${sha256(fs.readFileSync(out))}  ${out}`);
    }
    console.log(
      errs.length
        ? `INVALID ${dir} (${rec.classification})\n  ${errs.join('\n  ')}`
        : `ok      ${dir}`
    );
    // Coverage is reported separately from validity (review2 N-j).
    console.log(
      `coverage: ${JSON.stringify(rec.coverage.totals)} per-state ${JSON.stringify(rec.coverage.perState)}`
    );
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
