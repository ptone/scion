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
import { suiteDigest, captureHostCheck } from './suite-digest.mjs';

export const CAPTURE_KIND = 'wave01-capture';
export const RUN_KIND = 'wave01-capture-run';
export const SUBSTEPS = ['M0', 'M1', 'M2', 'M3'];
export const STATUSES = ['complete', 'capture-error', 'blocked'];
export const OUTCOMES = ['pass', 'fail', 'pending', 'inconclusive', 'not-applicable', 'blocked'];
export const QUARANTINE_AFTER = 3;
const SHA_RE = /^[0-9a-f]{64}$/;

// ─── E-ENV (§5a) ─────────────────────────────────────────────────────────

/**
 * Contract FROZEN rev 4 §5a E-ENV-1 (rulings R-7 per-artifact attribution).
 * E-ENV-1a: two runner probes of the serving process, inside the window,
 * with no real credential:
 *   P-API  protected API + `Authorization: Bearer scion_dev_<64 fresh hex>`:
 *          401 "development authentication is not enabled" ⇒ OFF;
 *          "invalid development token" or any 2xx ⇒ ON (pkg/hub/auth.go:467-477,
 *          detectTokenType :686-689, writeError {"error":{"message"}} errors.go:284-304)
 *   P-WEB  cookie-less GET /auth/me: 401 without identity ⇒ OFF; any returned
 *          identity ⇒ ON (web.go:949, :2773-2800; devAuthMiddleware :1896-1945)
 * E-ENV-1b: steward provenance record of the ACTUAL serving process (load
 * path + values); the runner checks binding/completeness/contradictions only
 * and never resolves precedence.
 */
export const DEV_AUTH_OFF_MESSAGE = 'development authentication is not enabled';
export const DEV_AUTH_ON_MESSAGE = 'invalid development token';
export const LOAD_PATHS = Object.freeze(['settings-global', 'settings-local', 'legacy']);
export const PROVENANCE_FLAGS = Object.freeze(['--hosted', '--production', '--dev-auth']);
export const SETTINGS_PATH_KEYS = Object.freeze([
  'server.mode',
  'server.auth.dev_mode',
  'server.auth.mode',
]);
export const LEGACY_FILE_KEYS = Object.freeze(['mode', 'auth.devMode', 'auth.mode']);
export const PROVENANCE_ENV_KEYS = Object.freeze([
  'SCION_SERVER_MODE',
  'SCION_SERVER_AUTH_DEVMODE',
  'SCION_SERVER_AUTH_MODE',
]);

/** Shared environment booleans compared between same-slot PRE and POST (review2 RB3). */
export const SHARED_ENV_BOOLEANS = Object.freeze([
  'e_env_1_dev_auth_effective',
  'e_env_3_test_login_enabled',
  'e_env_4_runtime_broker_effective',
  'e_env_4_no_broker_process_or_dispatch',
]);

const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v);
const okBool = (v) => typeof v === 'boolean' || v === 'absent';
const okVal = (v) =>
  (typeof v === 'string' && v !== '') || typeof v === 'boolean' || v === 'absent';

/**
 * Classify the two probes. Input is what the runner observed (status, error
 * message / identity presence, timestamp); no credential value is recorded.
 * @param {{status: number, message: string | null, at: string} | null} api
 * @param {{status: number, hasIdentity: boolean, at: string} | null} web
 */
export function classifyDevAuthProbes(api, web) {
  const apiV = !api
    ? 'missing'
    : (api.status >= 200 && api.status < 300) || api.message === DEV_AUTH_ON_MESSAGE
      ? 'on'
      : api.status === 401 && api.message === DEV_AUTH_OFF_MESSAGE
        ? 'off'
        : 'unexplained';
  const webV = !web
    ? 'missing'
    : web.hasIdentity
      ? 'on'
      : web.status === 401
        ? 'off'
        : 'unexplained';
  return { api: apiV, web: webV };
}

/**
 * E-ENV-1b provenance record checks (no precedence resolution).
 * @param {unknown} prov
 * @param {unknown} declaredDevAuth e_env_1_dev_auth_effective
 */
export const HOSTED_LOG_LINE = 'Server mode: hosted';
export const WORKSTATION_LOG_PREFIX = 'Server mode: workstation';
export const AUTH_MODE_SOURCES = Object.freeze([
  'settings:server.auth.mode',
  'legacy-file',
  'env:SCION_SERVER_AUTH_MODE',
  'unset-default',
]);

/**
 * Ruling R-8: support required for each declared_effective_* field.
 *   hosted    — the CURRENT process start's log line "Server mode: hosted"
 *               (cmd/server_foreground.go:240-244) with its timestamp, at or
 *               after the recorded process start of THIS slot generation;
 *               "Server mode: workstation (…)" ⇒ hosted false ⇒ FAIL.
 *   dev-auth  — the steward's determination from the recorded inputs; the
 *               startup "WARNING: Development authentication enabled" line
 *               (:313-320, hosted ∧ dev-auth) PRESENT ⇒ FAIL; its absence is
 *               supporting only, never OFF proof (probes decide OFF).
 *   auth.mode — the input that sets it (settings server.auth.mode via
 *               settings_v1.go:3280, a legacy file, or SCION_SERVER_AUTH_MODE),
 *               or "unset" citing the default (hub_config.go:583-584).
 * Missing support ⇒ record incomplete ⇒ INCONCLUSIVE (R-7).
 */
export function checkSupport(support, prov, slotGeneration, batchStart) {
  const missing = [];
  const forbidden = [];
  if (!isObj(support)) return { missing: ['support'], forbidden };
  const h = support.hosted;
  if (!isObj(h)) missing.push('support.hosted');
  else {
    if (typeof h.log_line !== 'string' || !h.log_line) missing.push('support.hosted.log_line');
    else if (h.log_line.startsWith(WORKSTATION_LOG_PREFIX))
      forbidden.push(`startup log "${h.log_line}" (non-hosted)`);
    else if (h.log_line !== HOSTED_LOG_LINE)
      missing.push('support.hosted.log_line (not the "Server mode: hosted" line)');
    const lt = Date.parse(h.log_ts ?? '');
    const pt = Date.parse(h.process_start_ts ?? '');
    if (!/Z$/.test(h.log_ts ?? '') || Number.isNaN(lt)) missing.push('support.hosted.log_ts');
    if (!/Z$/.test(h.process_start_ts ?? '') || Number.isNaN(pt))
      missing.push('support.hosted.process_start_ts');
    // Source attribution (owner via ii2, 16:55Z): timestamps must say where
    // they came from (OS process table / service manager; serve-path log).
    if (typeof h.process_start_source !== 'string' || !h.process_start_source.trim())
      missing.push('support.hosted.process_start_source');
    if (typeof h.log_ts_source !== 'string' || !h.log_ts_source.trim())
      missing.push('support.hosted.log_ts_source');
    if (!Number.isNaN(lt) && !Number.isNaN(pt) && lt < pt)
      missing.push('support.hosted log line predates the current process start (not attributable)');
    // Ruling R-10: process_start_ts <= log_ts <= batch start — the supporting
    // line must come from the process that served this batch. (The gap
    // between process start and the log line is recorded, not graded.)
    const bs = Date.parse(batchStart ?? '');
    if (!Number.isNaN(bs)) {
      if (!Number.isNaN(pt) && pt > bs)
        missing.push('support.hosted.process_start_ts after batch start (not the serving process)');
      if (!Number.isNaN(lt) && lt > bs)
        missing.push(
          'support.hosted.log_ts after batch start (not from the serving process start)'
        );
    }
    // Owner 16:55Z: the bound is the runner-recorded batch start; a declared
    // batch_start (optional, recorded) must be consistent and cannot move it.
    if (h.batch_start !== undefined) {
      const db = Date.parse(h.batch_start ?? '');
      if (!/Z$/.test(h.batch_start ?? '') || Number.isNaN(db))
        missing.push('support.hosted.batch_start malformed');
      else if ((!Number.isNaN(lt) && db < lt) || (!Number.isNaN(bs) && db > bs))
        missing.push(
          'support.hosted.batch_start inconsistent with log_ts / runner-recorded batch start'
        );
    }
    if (slotGeneration !== undefined && h.slot_generation !== slotGeneration)
      missing.push('support.hosted.slot_generation (log not attributable to this slot generation)');
  }
  const d = support.dev_auth;
  if (!isObj(d)) missing.push('support.dev_auth');
  else {
    if (d.basis !== 'recorded-inputs') missing.push('support.dev_auth.basis');
    if (typeof d.dev_auth_warning_present !== 'boolean')
      missing.push('support.dev_auth.dev_auth_warning_present');
    else if (d.dev_auth_warning_present)
      forbidden.push('startup "WARNING: Development authentication enabled" present');
  }
  // Ruling R-9: auth_mode support must be backed by the SAME record, and a
  // forbidden effective auth.mode established by the record itself is FAIL
  // (no general resolver: env overlay is applied last; the settings path has
  // a single selected file; legacy merge order is never resolved here).
  const a = support.auth_mode;
  const declared = prov?.declared_effective_auth_mode;
  const envAM = isObj(prov?.env) ? prov.env.SCION_SERVER_AUTH_MODE : undefined;
  const envSet = typeof envAM === 'string' && envAM !== 'absent';
  const settingsPath =
    prov?.load_path === 'settings-global' || prov?.load_path === 'settings-local';
  const legacyPath = prov?.load_path === 'legacy';
  const settingsAM =
    settingsPath && isObj(prov?.path_values) ? prov.path_values['server.auth.mode'] : undefined;
  const legacyFiles =
    legacyPath && isObj(prov?.path_values) && Array.isArray(prov.path_values.files)
      ? prov.path_values.files
      : [];
  // (c) FAIL cases established by the record.
  if (envSet && envAM === 'dev')
    forbidden.push('SCION_SERVER_AUTH_MODE=dev recorded (env overlay applied last)');
  if (settingsPath && envAM === 'absent' && settingsAM === 'dev')
    forbidden.push('selected settings file server.auth.mode=dev with env absent');
  // Legacy merge with a dev-valued file and no env overlay: establishing the
  // effective value would need the merge order resolved ⇒ not proven
  // (INCONCLUSIVE) unless declared "dev" (which is FAIL via R-8).
  if (
    legacyPath &&
    !envSet &&
    declared !== 'dev' &&
    legacyFiles.some((f) => isObj(f) && f['auth.mode'] === 'dev')
  )
    missing.push(
      'legacy merged file sets auth.mode=dev; effective value not established without resolving merge order'
    );
  if (!isObj(a) || !AUTH_MODE_SOURCES.includes(a.source)) missing.push('support.auth_mode.source');
  else {
    const fail = (why) => missing.push(`support.auth_mode invalid: ${why}`);
    if (a.source === 'unset-default') {
      // (b) every recorded auth.mode input must be absent.
      if (declared !== 'unset') fail('unset-default requires declared "unset"');
      if (envAM !== 'absent') fail('SCION_SERVER_AUTH_MODE not recorded absent');
      if (settingsPath && settingsAM !== 'absent')
        fail('selected settings file server.auth.mode not absent');
      if (
        legacyPath &&
        (legacyFiles.length === 0 ||
          legacyFiles.some((f) => !isObj(f) || f['auth.mode'] !== 'absent'))
      )
        fail('a merged legacy file sets auth.mode');
    } else {
      if (declared === 'unset') fail('declared "unset" requires source unset-default');
      if (a.source === 'env:SCION_SERVER_AUTH_MODE') {
        if (!envSet) fail('cited SCION_SERVER_AUTH_MODE is not recorded present');
        else if (envAM !== declared) fail('cited SCION_SERVER_AUTH_MODE value != declared');
      } else if (a.source === 'settings:server.auth.mode') {
        if (!settingsPath) fail('cited settings input but load_path is not a settings path');
        else if (typeof settingsAM !== 'string' || settingsAM === 'absent')
          fail('cited server.auth.mode is not recorded present');
        else if (settingsAM !== declared) fail('cited server.auth.mode value != declared');
        else if (envSet) fail('SCION_SERVER_AUTH_MODE is set and applied last; cite the env input');
      } else if (a.source === 'legacy-file') {
        const f = legacyFiles.find((x) => isObj(x) && x.file === a.file);
        if (!legacyPath) fail('cited legacy file but load_path is not legacy');
        else if (typeof a.file !== 'string' || !a.file) fail('legacy-file source needs file');
        else if (!f) fail('cited file is not among the recorded merged files');
        else if (typeof f['auth.mode'] !== 'string' || f['auth.mode'] === 'absent')
          fail('cited file does not record auth.mode');
        else if (f['auth.mode'] !== declared) fail('cited file auth.mode != declared');
        else if (envSet) fail('SCION_SERVER_AUTH_MODE is set and applied last; cite the env input');
      }
    }
  }
  return { missing, forbidden };
}

export function checkProvenance(prov, declaredDevAuth, slotGeneration, batchStart) {
  const missing = [];
  const contradictions = [];
  const forbidden = [];
  if (!isObj(prov))
    return { complete: false, missing: ['e_env_1_provenance'], contradictions, forbidden };
  const flags = isObj(prov.flags) ? prov.flags : null;
  if (!flags) missing.push('flags');
  for (const f of PROVENANCE_FLAGS) if (!flags || !okBool(flags[f])) missing.push(`flags.${f}`);
  if (!LOAD_PATHS.includes(prov.load_path)) missing.push('load_path');
  if (
    !Array.isArray(prov.files_examined) ||
    prov.files_examined.length === 0 ||
    !prov.files_examined.every((x) => typeof x === 'string' && x)
  )
    missing.push('files_examined');
  const pv = prov.path_values;
  if (prov.load_path === 'legacy') {
    const files = isObj(pv) && Array.isArray(pv.files) ? pv.files : null;
    if (!files || files.length === 0) missing.push('path_values.files');
    else
      files.forEach((f, i) => {
        if (!isObj(f) || typeof f.file !== 'string' || !f.file)
          missing.push(`path_values.files[${i}].file`);
        for (const k of LEGACY_FILE_KEYS)
          if (!isObj(f) || !okVal(f[k])) missing.push(`path_values.files[${i}].${k}`);
      });
  } else if (prov.load_path) {
    for (const k of SETTINGS_PATH_KEYS)
      if (!isObj(pv) || !okVal(pv[k])) missing.push(`path_values.${k}`);
  }
  const env = isObj(prov.env) ? prov.env : null;
  if (!env) missing.push('env');
  for (const k of PROVENANCE_ENV_KEYS) if (!env || !okVal(env[k])) missing.push(`env.${k}`);
  if (!env || !['present', 'absent'].includes(env.SCION_SERVER_AUTH_DEV_MODE))
    missing.push('env.SCION_SERVER_AUTH_DEV_MODE');
  if (typeof prov.declared_effective_hosted !== 'boolean')
    missing.push('declared_effective_hosted');
  if (
    typeof prov.declared_effective_auth_mode !== 'string' ||
    prov.declared_effective_auth_mode === ''
  )
    missing.push('declared_effective_auth_mode');
  if (typeof declaredDevAuth !== 'boolean') missing.push('e_env_1_dev_auth_effective');
  const sup = checkSupport(prov.support, prov, slotGeneration, batchStart);
  missing.push(...sup.missing);
  forbidden.push(...sup.forbidden);
  // Forbidden states declared or directly recorded.
  if (declaredDevAuth === true) forbidden.push('declared effective dev-auth ON');
  if (prov.declared_effective_hosted === false)
    forbidden.push('declared effective hosted mode false (non-hosted)');
  if (flags && flags['--dev-auth'] === true) forbidden.push('explicit --dev-auth=true');
  if (prov.declared_effective_auth_mode === 'dev')
    forbidden.push('declared effective auth.mode "dev"');
  // Contradictions with inputs that directly override everything.
  if (
    flags &&
    typeof flags['--dev-auth'] === 'boolean' &&
    typeof declaredDevAuth === 'boolean' &&
    flags['--dev-auth'] !== declaredDevAuth
  )
    contradictions.push(
      `explicit --dev-auth=${flags['--dev-auth']} but declared effective dev-auth ${declaredDevAuth}`
    );
  if (flags && typeof prov.declared_effective_hosted === 'boolean') {
    const h = flags['--hosted'];
    const pr = flags['--production'];
    const anyTrue = h === true || pr === true;
    const explicitFalse = (h === false || pr === false) && !anyTrue;
    if (anyTrue && prov.declared_effective_hosted === false)
      contradictions.push('explicit --hosted/--production true but declared hosted false');
    if (explicitFalse && prov.declared_effective_hosted === true)
      contradictions.push('explicit --hosted/--production false but declared hosted true');
  }
  if (
    env &&
    typeof env.SCION_SERVER_AUTH_MODE === 'string' &&
    env.SCION_SERVER_AUTH_MODE !== 'absent' &&
    typeof prov.declared_effective_auth_mode === 'string' &&
    env.SCION_SERVER_AUTH_MODE !== prov.declared_effective_auth_mode
  )
    contradictions.push(
      `SCION_SERVER_AUTH_MODE=${env.SCION_SERVER_AUTH_MODE} but declared auth.mode ${prov.declared_effective_auth_mode}`
    );
  return {
    complete: missing.length === 0,
    missing: Array.from(new Set(missing)),
    contradictions,
    forbidden,
  };
}

/**
 * E-ENV-1 per ruling R-7: per-artifact attribution. Own probes are
 * attributable by construction (probe ON ⇒ FAIL regardless). An unbound
 * record is excluded and counts as missing. Then FAIL on a forbidden state
 * in the attributable record; INCONCLUSIVE if anything required is missing,
 * contradictory or unexplained; PASS only with both probes OFF and an
 * attributable complete record declaring hosted true and dev-auth OFF.
 * @param {any} decl
 * @param {{api: any, web: any} | null} probes
 * @param {{attributable: boolean, bound: boolean}} b
 */
export function gradeEEnv1(decl, probes, b) {
  const cls = classifyDevAuthProbes(probes?.api ?? null, probes?.web ?? null);
  const prov = b.attributable
    ? checkProvenance(
        decl?.e_env_1_provenance,
        decl?.e_env_1_dev_auth_effective,
        decl?.slotGeneration,
        b.batchStart
      )
    : null;
  const details = {
    probes: { observed: probes ?? null, classified: cls },
    provenance: prov,
    recordAttributable: b.attributable,
    recordBoundToWindow: b.bound,
  };
  // Ruling R-10: the probes must come after the serving process start.
  const pst = Date.parse(decl?.e_env_1_provenance?.support?.hosted?.process_start_ts ?? '');
  const probesBeforeStart =
    b.attributable && !Number.isNaN(pst)
      ? ['api', 'web'].filter((k) => {
          const at = Date.parse(probes?.[k]?.at ?? '');
          return !Number.isNaN(at) && at < pst;
        })
      : [];
  if (cls.api === 'on' || cls.web === 'on')
    return {
      outcome: 'fail',
      reasons: [`probe shows dev-auth ON (P-API ${cls.api}, P-WEB ${cls.web})`],
      ...details,
    };
  if (prov && prov.forbidden.length)
    return { outcome: 'fail', reasons: prov.forbidden, ...details };
  const open = [];
  if (cls.api !== 'off') open.push(`P-API ${cls.api}`);
  if (cls.web !== 'off') open.push(`P-WEB ${cls.web}`);
  if (!b.attributable)
    open.push('provenance record not attributable to this slot (counts as missing)');
  else if (!b.bound) open.push('provenance record not bound to this capture window');
  if (prov && !prov.complete) open.push(`provenance incomplete: ${prov.missing.join(', ')}`);
  if (prov && prov.contradictions.length) open.push(...prov.contradictions);
  if (probesBeforeStart.length)
    open.push(
      `probe(s) ${probesBeforeStart.join(', ')} taken before the serving process start (R-10)`
    );
  return { outcome: open.length ? 'inconclusive' : 'pass', reasons: open, ...details };
}

/** Canonical comparison form of a provenance record (PRE/POST equality). */
export function provenanceKey(prov) {
  if (!isObj(prov)) return null;
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
  return JSON.stringify(canon(prov));
}

/**
 * Environment-only comparison form of a provenance record (review4 RB4-1,
 * assessor-concurred 17:17Z). PRE/POST same-process equality is graded on the
 * ENVIRONMENT values only: flags, load_path, files_examined, path_values, env
 * and the declared_effective_* values. `support.*` is supporting metadata
 * (source text, timestamp precision, advisory batch_start); differences there
 * are recorded as notes, never an environment FAIL.
 */
export const PROVENANCE_COMPARED_FIELDS = [
  'flags',
  'load_path',
  'files_examined',
  'path_values',
  'env',
  'declared_effective_hosted',
  'declared_effective_auth_mode',
];
export function provenanceEnvKey(prov) {
  if (!isObj(prov)) return null;
  // Ruling R-15 (review6 RB6-1): a field not recorded on this side is OMITTED,
  // never encoded as a value, so the comparison treats it as one-sided.
  return provenanceKey(
    Object.fromEntries(
      PROVENANCE_COMPARED_FIELDS.filter((k) => k in prov && prov[k] !== undefined).map((k) => [
        k,
        prov[k],
      ])
    )
  );
}
/**
 * Ruling R-12 leaf-wise comparison of the environment fields of two
 * provenance records. A value recorded on both sides (an explicit `absent`
 * included) that differs is listed in `differs`; a key recorded on only one
 * side (undefined/null on the other) is not a value and is listed in
 * `oneSided`. Arrays (files_examined, legacy merged files) compare whole.
 * @param {any} pre @param {any} post
 */
export function provenanceEnvDiff(pre, post) {
  /** @type {string[]} */ const differs = [];
  /** @type {string[]} */ const oneSided = [];
  // R-15: the legacy '<missing>' sentinel (older comparison keys) is never a value.
  const recorded = (v) => v !== undefined && v !== null && v !== '<missing>';
  const rec = (a, b, at) => {
    const ra = recorded(a);
    const rb = recorded(b);
    if (!ra && !rb) return;
    if (ra !== rb) return void oneSided.push(at);
    if (isObj(a) && isObj(b)) {
      for (const k of [...new Set([...Object.keys(a), ...Object.keys(b)])].sort())
        rec(a[k], b[k], `${at}.${k}`);
      return;
    }
    if (provenanceKey({ v: a }) !== provenanceKey({ v: b })) differs.push(at);
  };
  for (const k of PROVENANCE_COMPARED_FIELDS)
    rec(isObj(pre) ? pre[k] : undefined, isObj(post) ? post[k] : undefined, k);
  return { differs, oneSided };
}

/** Comparison form of the supporting metadata only (recorded, not graded). */
export function provenanceSupportKey(prov) {
  if (!isObj(prov)) return null;
  return provenanceKey(isObj(prov.support) ? prov.support : { '<missing>': true });
}

/**
 * PRE comparison inputs, derived ONLY from the PRE declaration (owner O-a):
 * the runner records them in run.json, and validate-run re-derives them from
 * the embedded raw declaration bytes rather than trusting those fields.
 * @param {any} decl
 */
export function preComparisonInputs(decl) {
  const prov = decl?.e_env_1_provenance;
  return {
    envPreBaseURL: typeof decl?.baseURL === 'string' ? decl.baseURL : null,
    envPreValues: decl ? sharedEnvValues(decl) : null,
    envPreProvenanceKey: decl ? provenanceEnvKey(prov) : null,
    envPreSupportKey: decl ? provenanceSupportKey(prov) : null,
    envPreProcessStartTs:
      typeof prov?.support?.hosted?.process_start_ts === 'string'
        ? prov.support.hosted.process_start_ts
        : null,
  };
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
export function evaluateEnv(decl, selfAnon401, release, window, probes = null) {
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
      (() => {
        // R-7: own probes are attributable even without any declaration.
        const e1 = gradeEEnv1(null, probes, { attributable: false, bound: false });
        return g('E-ENV-1', e1.outcome, {
          rule: 'rev 4 §5a (R-7); no steward PRE declaration',
          ...e1,
        });
      })(),
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
  const e1 = gradeEEnv1(decl, probes, { attributable, bound, batchStart: window?.batchStart });
  const out = [
    g('E-ENV-1', e1.outcome, {
      rule: 'rev 4 §5a E-ENV-1a probes + 1b provenance (R-7)',
      ...e1,
      binding,
    }),
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
 * @param {{startedAt: string, endedAt: string, slotGeneration: string, baseURL: string, preBaseURL?: string, preValues?: Record<string, boolean | null>, preProvenanceKey?: string | null, preSupportKey?: string | null, preProcessStartTs?: string | null, testLoginUsed?: boolean}} run
 */
export function evaluateEnvPost(post, run) {
  if (!post)
    return {
      outcome: 'inconclusive',
      problems: ['missing: POST env declaration'],
      fails: [],
      open: ['missing: POST env declaration'],
      notes: [],
      attributable: false,
    };
  const problems = [];
  const fails = [];
  const notes = [];
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
  if (post.e_env_1_provenance !== undefined) {
    // E-ENV-1b is required on PRE; on POST it is optional. Ruling R-12
    // (assessor 17:38Z): on an ATTRIBUTABLE POST record (same host and
    // generation, and the same serving process: serving_process_start_ts ==
    // PRE process_start_ts, R-10) environment values are compared FIRST;
    // any value recorded on both sides (an explicit `absent` is a value) that
    // differs ⇒ FAIL, whatever else is incomplete. Incompleteness is recorded
    // alongside. A key missing on one side is not compared ⇒ INCONCLUSIVE.
    const postProv = post.e_env_1_provenance;
    const pc = checkProvenance(
      postProv,
      post.e_env_1_dev_auth_effective,
      post.slotGeneration,
      run.startedAt
    );
    if (pc.forbidden.length) fails.push(`E-ENV-1 (POST record): ${pc.forbidden.join('; ')}`);
    const bound =
      post.slotGeneration === run.slotGeneration &&
      hb.status === 'ok' &&
      (run.preBaseURL === undefined || hostBinding(post, run.preBaseURL).status === 'ok');
    const recordStart = isObj(postProv) ? postProv.support?.hosted?.process_start_ts : undefined;
    const sameProcess =
      typeof run.preProcessStartTs === 'string' &&
      post.serving_process_start_ts === run.preProcessStartTs &&
      (recordStart === undefined || recordStart === run.preProcessStartTs);
    if (!bound)
      problems.push('POST e_env_1_provenance not bound to this slot (R-3) — not compared');
    else if (!sameProcess)
      // R-10/R-12: serving_process_start_ts missing, or a different process
      // start (restart) ⇒ not attributable ⇒ no comparison (INCONCLUSIVE).
      problems.push(
        'POST record not attributable to the PRE serving process (serving_process_start_ts missing or different; R-10) — not compared'
      );
    else if (run.preProvenanceKey) {
      // The PRE key may be the environment-only form (runner, RB4-1) or a
      // whole-record canonical form; both are compared on the environment
      // fields only, so support metadata never decides equality.
      let preProv = null;
      try {
        preProv = JSON.parse(run.preProvenanceKey);
      } catch {
        /* reported below */
      }
      if (!isObj(preProv)) problems.push('PRE provenance key unreadable — not compared');
      else {
        const d = provenanceEnvDiff(preProv, postProv);
        if (d.differs.length)
          fails.push(
            `same-slot PRE/POST disagreement on e_env_1_provenance (environment values): ${d.differs.join(', ')}`
          );
        if (d.oneSided.length)
          problems.push(
            `POST/PRE environment value(s) not recorded on both sides — not compared: ${d.oneSided.join(', ')}`
          );
        const preSupport =
          run.preSupportKey ?? (isObj(preProv.support) ? provenanceSupportKey(preProv) : null);
        if (preSupport && provenanceSupportKey(postProv) !== preSupport)
          notes.push(
            'PRE/POST support metadata differs (recorded only; not an environment value — RB4-1)'
          );
      }
    } else problems.push('missing: PRE provenance key — not compared');
    // Completeness/contradictions are graded AFTER the comparison (R-12).
    if (!pc.forbidden.length && (!pc.complete || pc.contradictions.length))
      problems.push(
        `POST e_env_1_provenance incomplete/contradictory: ${[...pc.missing, ...pc.contradictions].join('; ')}`
      );
  } else if (post.e_env_1_dev_auth_effective === true)
    fails.push('E-ENV-1 (POST): declared effective dev-auth ON');
  // Ruling R-10 continuity: POST must show the SAME serving-process start as
  // PRE (a restart during the batch means no single attributable process
  // covered it ⇒ INCONCLUSIVE).
  {
    const postStart =
      post.serving_process_start_ts ?? post.e_env_1_provenance?.support?.hosted?.process_start_ts;
    if (run.preProcessStartTs === undefined || run.preProcessStartTs === null)
      problems.push('missing: PRE serving process start (run.envPreProcessStartTs)');
    else if (typeof postStart !== 'string' || !postStart)
      problems.push('POST serving_process_start_ts missing (R-10 continuity)');
    else if (postStart !== run.preProcessStartTs)
      problems.push(
        `serving process restarted during the batch (PRE ${run.preProcessStartTs}, POST ${postStart}) — R-10`
      );
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
    // O-d: graded contradictions and open (missing/unattributable) problems
    // kept apart so validate-run can label them mismatch:/missing:.
    fails: [...fails],
    open: [...problems],
    notes,
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
    errs.push('contractSha256 is not the pinned FROZEN rev 8 digest');
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
  // Ruling R-13: batchValid is a DERIVED flag. Its causes (abort, E-MAIN,
  // E-ENV gates, post probes, pair) are re-checked independently below and
  // classified there (missing ⇒ `missing:`, contradiction ⇒ `mismatch:`). The
  // flag itself is a mismatch only if malformed, inconsistent between copies,
  // or false with no independently established cause (checked at the end).
  if (typeof run.batchValid !== 'boolean') errs.push('mismatch: run.json batchValid not boolean');
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
  // O-a: every PRE comparison input is derived from the embedded RAW
  // declaration below; the run.json copies are only cross-checked.
  /** @type {ReturnType<typeof preComparisonInputs> | null} */
  let pre = null;
  // review3 O1: recompute the PRE gates from the embedded RAW declaration and
  // the recorded probe / auth-check results instead of trusting run.env.
  if (typeof run.envPreDeclarationRaw !== 'string') errs.push('missing: run.envPreDeclarationRaw');
  else {
    if (sha256(Buffer.from(run.envPreDeclarationRaw, 'utf-8')) !== run.envDeclarationSha256)
      errs.push('mismatch: embedded PRE declaration bytes do not match envDeclarationSha256');
    let decl = null;
    try {
      decl = JSON.parse(run.envPreDeclarationRaw);
    } catch {
      errs.push('mismatch: embedded PRE declaration is not JSON');
    }
    if (decl) {
      pre = preComparisonInputs(decl);
      for (const [k, v] of Object.entries(pre))
        if (k in run && provenanceKey({ v: run[k] ?? null }) !== provenanceKey({ v }))
          errs.push(`mismatch: run.${k} differs from the value derived from envPreDeclarationRaw`);
      let recomputed = evaluateEnv(
        decl,
        run.envPreSelfAnon401 ?? null,
        { slotGeneration: base.slotGeneration, baseURL: run.baseURL ?? base.baseURL },
        { batchStart: run.startedAt, maxAgeMin: run.envMaxAgeMin },
        run.envPreDevAuthProbes ?? null
      );
      if (run.envAuthCheck) recomputed = applyAuthCheck(recomputed, run.envAuthCheck);
      for (const g of recomputed) {
        const recorded = (run.env ?? []).find((x) => x.gate === g.gate);
        if (!recorded) errs.push(`missing: run.env ${g.gate}`);
        else if (recorded.outcome !== g.outcome || !!recorded.awaitingPost !== !!g.awaitingPost)
          errs.push(`mismatch: recomputed ${g.gate} ${g.outcome} != recorded ${recorded.outcome}`);
      }
    }
  }
  if (!pre?.envPreValues) errs.push('missing: PRE shared env values (from envPreDeclarationRaw)');
  if (!pre?.envPreBaseURL)
    errs.push('missing: PRE declaration baseURL (from envPreDeclarationRaw)');
  else if (hostBinding({ baseURL: pre.envPreBaseURL }, base.baseURL).status !== 'ok')
    errs.push(
      `missing: PRE env baseURL ${pre.envPreBaseURL} is not bound to Release baseURL ${base.baseURL} (INCONCLUSIVE)`
    );
  if (run.baseURL && new URL(run.baseURL).origin !== new URL(base.baseURL).origin)
    errs.push('mismatch: run.baseURL != Release baseURL');
  if (!Array.isArray(run.env) || run.env.length !== 5)
    errs.push('missing: run.env must carry E-ENV-1..5');
  // rev 4 §5a: P-API/P-WEB repeated after the batch; ON ⇒ E-ENV-1 FAIL (batch
  // invalid); missing/unexplained ⇒ INCONCLUSIVE.
  {
    const pc = classifyDevAuthProbes(
      run.envPostDevAuthProbes?.api ?? null,
      run.envPostDevAuthProbes?.web ?? null
    );
    if (pc.api === 'on' || pc.web === 'on')
      errs.push(
        `mismatch: post-batch dev-auth probe shows ON (P-API ${pc.api}, P-WEB ${pc.web}) — E-ENV-1 FAIL`
      );
    else if (pc.api !== 'off' || pc.web !== 'off')
      errs.push(
        `missing: post-batch dev-auth probes not both OFF (P-API ${pc.api}, P-WEB ${pc.web})`
      );
  }
  {
    // R-13: E-ENV-2 needs an OBSERVED 401. No response (missing, status 0,
    // fetch error) is missing evidence; an observed non-401 is a contradiction.
    const st = run.envPostSelfAnon401?.status;
    if (typeof st !== 'number' || st <= 0 || run.envPostSelfAnon401?.error)
      errs.push(
        `missing: post-batch anonymous probe returned no response (${st ?? 'absent'}; E-ENV-2 INCONCLUSIVE)`
      );
    else if (st !== 401) errs.push(`mismatch: post-batch anonymous probe status ${st} (E-ENV-2)`);
  }
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
      preBaseURL: pre?.envPreBaseURL ?? undefined,
      preValues: pre?.envPreValues ?? undefined,
      preProvenanceKey: pre?.envPreProvenanceKey ?? undefined,
      preSupportKey: pre?.envPreSupportKey ?? undefined,
      preProcessStartTs: pre?.envPreProcessStartTs ?? null,
      testLoginUsed:
        Array.isArray(run.issuanceInventory?.credentials) &&
        run.issuanceInventory.credentials.length > 0,
    });
    // O-d: per-problem labels survive an aggregate FAIL — graded
    // contradictions are `mismatch:`, open/unattributable ones `missing:`.
    errs.push(...r.fails.map((p) => `mismatch: POST env ${p}`));
    errs.push(...r.open.map((p) => `missing: POST env ${p}`));
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
    // R-13: a post-batch main.js that was not obtained is missing evidence;
    // an obtained, different digest is a contradiction (E-MAIN).
    if (typeof c.mainJs?.post?.sha256 !== 'string')
      errs.push(`${f}: missing: mainJs.post not obtained (E-MAIN INCONCLUSIVE)`);
    else m(c.mainJs.post.sha256 === base.mainJsSha256, 'mainJs.post');
    // Copies of the derived flag must agree with run.json (R-13).
    m(c.batchValid === run.batchValid, 'batchValid differs from run.json');
    m(
      (c.batchInvalidReason ?? null) === (run.batchInvalidReason ?? null),
      'batchInvalidReason differs from run.json'
    );
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
  // R-13: a false derived flag must be explained by an independently
  // established cause; otherwise the copies contradict the evidence.
  if (run.batchValid === false && errs.length === 0)
    errs.push('mismatch: batchValid false without an independently established cause');
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
 * review3 O5 + review4 RB4-2: which code produced the verdict, and whether
 * that code IS the committed tree it names. The digest is computed from HEAD's
 * tracked blobs, so it identifies the executing validator only when the
 * covered tree (web/e2e/layout-survey, web/e2e/harness) is clean: no
 * modified, staged or untracked files. `clean: false` ⇒ the validation record
 * can never be VALID (assessor 17:17Z: unverified ⇒ INCONCLUSIVE until
 * re-validated cleanly). The root is derived from THIS module's location, i.e.
 * the tree the executing validator was loaded from.
 * @param {{root?: string}} [opts]
 */
export function validatorIdentity(opts = {}) {
  try {
    const d = suiteDigest({ root: opts.root });
    const h = captureHostCheck({ root: opts.root, reviewedCommit: 'HEAD' });
    return {
      head: d.commit,
      suiteDigest: d.digest,
      suiteFileCount: d.fileCount,
      method: d.method,
      clean: h.ok,
      cleanProblems: h.problems,
    };
  } catch (e) {
    return {
      clean: false,
      cleanProblems: ['validator identity could not be established'],
      error: String(e instanceof Error ? e.message : e).slice(0, 200),
    };
  }
}

/**
 * Immutable validation result bound to the exact inputs (owner 16:04Z: the
 * passing validate-run output, not run.json batchValid, is the final
 * environment-validity evidence). `classification` follows §0/§5a: any
 * `mismatch:` ⇒ REJECTED-FAIL-OR-INVALID; only `missing:` ⇒ INCONCLUSIVE.
 */
export function validationRecord(
  runDir,
  baseFile,
  companionFile,
  envPostFile,
  runErrs,
  identity = validatorIdentity()
) {
  // RB4-2: an unverified validator identity is never VALID. The validator is
  // not run evidence, so this is `missing:` (verdict unverified ⇒
  // INCONCLUSIVE), never a FAIL of the run itself.
  const errs = [
    ...runErrs,
    ...(identity.clean === true
      ? []
      : (identity.cleanProblems ?? ['validator cleanliness not established']).map(
          (p) => `missing: validator identity unverified (re-validate on a clean tree): ${p}`
        )),
  ];
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
    validator: identity,
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
    const runErrs = validateRun(
      dir,
      arg(rest, 'base'),
      arg(rest, 'companion'),
      i >= 0 ? arg(rest, 'env-post') : undefined
    );
    const o = rest.indexOf('--out');
    const envPost = i >= 0 ? arg(rest, 'env-post') : undefined;
    const rec = validationRecord(dir, arg(rest, 'base'), arg(rest, 'companion'), envPost, runErrs);
    const errs = rec.errors;
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
