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

// Wave01 Release pair: pilot-schema base Release + `wave01-release-ext`
// companion (owner decision 14:58Z/14:59Z; assessor-confirmed against
// contract §5b E-REL). STEWARD-run tooling; the measured runner only reads
// and validates these records.
//
//   node wave01/release.mjs base --out FILE <pilot `records.mjs release` flags>
//      Pilot buildRelease with releaseKind forced to `verification` and
//      scenarioSuiteSha = the contract E-RUN suite digest at HEAD (covered
//      paths must be clean). Written 0444, refuses to overwrite.
//
//   node wave01/release.mjs companion --out FILE --base FILE --phase baseline|candidate
//        --browser-version TEXT --font-image-digest HEX | --font-manifest-from-fc-list
//        --native-chat true|false --terminal-workspace true|false
//        --groups-fixture-map FILE --credential-inventory FILE
//        --fixture-helper FILE|none --steward ID
//      Writes the companion (0444, no overwrite) bound to the base by id +
//      file sha256 + slotGeneration; prints its sha256.
//
//   node wave01/release.mjs validate-pair --base FILE --companion FILE
//      Exit 1 on any missing normative field or duplicated-value mismatch.

import { execFileSync } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import * as fs from 'node:fs';
import { buildRelease, validateRecord } from '../scripts/records.mjs';
import {
  captureHostCheck,
  suiteDigest,
  COVERED_PATHS,
  SUITE_DIGEST_METHOD,
} from './suite-digest.mjs';

export const COMPANION_KIND = 'wave01-release-ext';
export const CONTRACT_SHA256 = '1877b1d40a5e4bf04d87e47d419a4451cac5ccb2d3f27f9a63d8ce32c88a5447';
export const FRONTEND_BASELINE = '1694e51145a0a26bedf754751a130d7b05544232';
export const BACKEND = '4a253489ebe3298fcfe4d7271b3642a5578b2b31';
const SHA_RE = /^[0-9a-f]{64}$/;
const GIT_RE = /^[0-9a-f]{40}$/;

/** @param {Buffer|string} b */
export const sha256 = (b) => createHash('sha256').update(b).digest('hex');

const isEmpty = (v) =>
  v === undefined ||
  v === null ||
  (typeof v === 'string' && v.trim() === '') ||
  (Array.isArray(v) && v.length === 0);

/** Reject anything that looks like credential material in a value-free record. */
export function assertValueFree(obj, where) {
  const walk = (v, p) => {
    if (typeof v === 'string') {
      if (/^[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}$/.test(v))
        throw new Error(`${where}: ${p} looks like a JWT; records must be value-free`);
      if (/^(scion_|Bearer\s)/.test(v)) throw new Error(`${where}: ${p} looks like a token`);
      if (v.length > 512)
        throw new Error(`${where}: ${p} is suspiciously long for a value-free record`);
    } else if (v && typeof v === 'object') {
      for (const [k, x] of Object.entries(v)) {
        if (/token|secret|password|cookie_value|jti/i.test(k) && typeof x === 'string')
          throw new Error(`${where}: key ${p}.${k} must not carry a value`);
        walk(x, `${p}.${k}`);
      }
    }
  };
  walk(obj, '$');
}

/** sha256 of the LC_ALL=C-sorted sha256 manifest of fonts the browser can load (contract §5b). */
export function fontManifestDigest() {
  const out = execFileSync('fc-list', ['--format', '%{file}\n'], { encoding: 'utf-8' });
  const files = Array.from(new Set(out.split('\n').filter(Boolean)));
  const lines = files.map((f) => Buffer.from(`${sha256(fs.readFileSync(f))}  ${f}\n`));
  lines.sort(Buffer.compare);
  return {
    method: 'font-file-manifest (LC_ALL=C-sorted "<sha256>  <path>" of fc-list files)',
    sha256: sha256(Buffer.concat(lines)),
    fileCount: lines.length,
  };
}

const COMPANION_REQUIRED = [
  'schemaVersion',
  'kind',
  'id',
  'createdAt',
  'producer',
  'baseReleaseId',
  'baseReleaseSha256',
  'slotGeneration',
  'baseURL',
  'releaseKind',
  'phase',
  'contract',
  'servedSourceSha',
  'backendSourceSha',
  'runner',
  'browser',
  'fontImageDigest',
  'settingsProfile',
  'fixtures',
  'fixtureHelper',
  'credentialInventory',
];

/**
 * Validate a base Release + companion pair. Returns errors (empty = valid).
 * Missing/unparseable inputs are reported as `missing:` errors so callers
 * can grade INCONCLUSIVE; disagreements as `mismatch:` (FAIL).
 * @param {{baseBytes: Buffer, companion: any, expect?: {runnerCommit?: string, suiteDigest?: string, browserVersion?: string, baseURL?: string}}} a
 */
export function validatePair({ baseBytes, companion, expect = {} }) {
  const errs = [];
  let base;
  try {
    base = JSON.parse(baseBytes.toString('utf-8'));
  } catch {
    return ['missing: base Release is not JSON'];
  }
  errs.push(...validateRecord(base).map((e) => `missing: base ${e}`));
  if (!companion || typeof companion !== 'object') return [...errs, 'missing: companion'];
  for (const f of COMPANION_REQUIRED)
    if (isEmpty(companion[f])) errs.push(`missing: companion.${f}`);
  if (companion.schemaVersion !== 1) errs.push('mismatch: companion.schemaVersion must be 1');
  if (companion.kind !== COMPANION_KIND)
    errs.push(`mismatch: companion.kind must be ${COMPANION_KIND}`);
  const eq = (label, a, b) => {
    if (a === undefined || b === undefined) errs.push(`missing: ${label}`);
    else if (JSON.stringify(a) !== JSON.stringify(b))
      errs.push(`mismatch: ${label} (${JSON.stringify(a)} != ${JSON.stringify(b)})`);
  };
  eq(
    'companion.baseReleaseSha256 vs base file sha256',
    companion.baseReleaseSha256,
    sha256(baseBytes)
  );
  eq('companion.baseReleaseId vs base.id', companion.baseReleaseId, base.id);
  eq(
    'companion.slotGeneration vs base.slotGeneration',
    companion.slotGeneration,
    base.slotGeneration
  );
  eq('companion.baseURL vs base.baseURL', companion.baseURL, base.baseURL);
  eq('base.releaseKind', base.releaseKind, 'verification');
  eq('companion.releaseKind vs base.releaseKind', companion.releaseKind, base.releaseKind);
  eq('companion.servedSourceSha vs base.sourceSha', companion.servedSourceSha, base.sourceSha);
  eq(
    'companion.backendSourceSha vs base.backendSourceSha',
    companion.backendSourceSha,
    base.backendSourceSha
  );
  eq('companion.contract.sha256', companion.contract?.sha256, CONTRACT_SHA256);
  eq(
    'companion.runner.suiteDigest vs base.scenarioSuiteSha',
    companion.runner?.suiteDigest,
    base.scenarioSuiteSha
  );
  eq(
    'companion.settingsProfile.settingsProfileSha vs base.settingsProfileSha',
    companion.settingsProfile?.settingsProfileSha,
    base.settingsProfileSha
  );
  eq(
    'companion.fixtures.fixtureRecipeSha vs base.fixtureRecipeSha',
    companion.fixtures?.fixtureRecipeSha,
    base.fixtureRecipeSha
  );
  eq(
    'companion.fixtures.fixtureSnapshotSha256 vs base.fixtureSnapshotSha256',
    companion.fixtures?.fixtureSnapshotSha256,
    base.fixtureSnapshotSha256
  );
  if (!['baseline', 'candidate'].includes(companion.phase))
    errs.push('mismatch: companion.phase must be baseline|candidate');
  if (companion.phase === 'baseline' && base.sourceSha !== FRONTEND_BASELINE)
    errs.push(`mismatch: baseline phase requires served sourceSha ${FRONTEND_BASELINE}`);
  if (companion.phase === 'candidate' && base.sourceSha === FRONTEND_BASELINE)
    errs.push('mismatch: candidate phase must serve a reviewed fix head, not the baseline source');
  if (base.backendSourceSha && base.backendSourceSha !== BACKEND)
    errs.push(`mismatch: backendSourceSha must be ${BACKEND} (contract Release identity)`);
  const r = companion.runner ?? {};
  if (!GIT_RE.test(r.commit ?? '')) errs.push('missing: companion.runner.commit (40-hex)');
  if (!SHA_RE.test(r.suiteDigest ?? ''))
    errs.push('missing: companion.runner.suiteDigest (64-hex)');
  if (typeof r.suiteFileCount !== 'number') errs.push('missing: companion.runner.suiteFileCount');
  if (r.suiteDigestMethod !== SUITE_DIGEST_METHOD)
    errs.push('mismatch: companion.runner.suiteDigestMethod');
  if (companion.browser?.engine !== 'chromium' || isEmpty(companion.browser?.version))
    errs.push('missing: companion.browser {engine: chromium, version}');
  if (!SHA_RE.test(companion.fontImageDigest?.sha256 ?? ''))
    errs.push('missing: companion.fontImageDigest.sha256');
  for (const k of ['web.native_chat', 'web.terminal_workspace'])
    if (typeof companion.settingsProfile?.[k] !== 'boolean')
      errs.push(`missing: companion.settingsProfile["${k}"] boolean`);
  const fh = companion.fixtureHelper ?? {};
  if (fh.status === 'accepted') {
    for (const k of [
      'sourceCommit',
      'sourceFileSha256',
      'buildDigest',
      'goToolchain',
      'recipeSha256',
      'schemaIdentity',
      'snapshotBeforeSha256',
      'snapshotAfterSha256',
      'snapshotFinalSha256',
      'reviewVerdict',
    ])
      if (isEmpty(fh[k])) errs.push(`missing: companion.fixtureHelper.${k}`);
  } else if (fh.status !== 'not-used' || isEmpty(fh.reason)) {
    errs.push(
      'missing: companion.fixtureHelper must be {status: accepted, …H1} or {status: not-used, reason}'
    );
  }
  const ci = companion.credentialInventory ?? {};
  if (typeof ci.counts !== 'object' || isEmpty(ci.roles) || !Array.isArray(ci.validityWindows))
    errs.push('missing: companion.credentialInventory {counts, roles, validityWindows}');
  if (!SHA_RE.test(companion.fixtures?.groupsFixtureMapSha256 ?? ''))
    errs.push('missing: companion.fixtures.groupsFixtureMapSha256');
  try {
    assertValueFree(companion, 'companion');
  } catch (e) {
    errs.push(`mismatch: ${e instanceof Error ? e.message : e}`);
  }
  if (expect.runnerCommit !== undefined)
    eq('companion.runner.commit vs runner HEAD', r.commit, expect.runnerCommit);
  if (expect.suiteDigest !== undefined)
    eq('companion.runner.suiteDigest vs computed suite digest', r.suiteDigest, expect.suiteDigest);
  if (expect.browserVersion !== undefined)
    eq(
      'companion.browser.version vs runner browser',
      companion.browser?.version,
      expect.browserVersion
    );
  if (expect.baseURL !== undefined)
    eq(
      'base.baseURL origin vs LAYOUT_SURVEY_BASE_URL',
      new URL(base.baseURL).origin,
      expect.baseURL
    );
  return errs;
}

/** Baseline and candidate must be distinct pairs on distinct hosts (not ports alone). */
export function validateDistinctPairs(a, b) {
  const errs = [];
  if (a.companion.id === b.companion.id) errs.push('same companion id');
  if (a.base.id === b.base.id) errs.push('same base Release id');
  if (a.base.slotGeneration === b.base.slotGeneration) errs.push('same slotGeneration');
  if (new URL(a.base.baseURL).hostname === new URL(b.base.baseURL).hostname)
    errs.push('baseline/candidate baseURL hosts must differ (not ports alone)');
  return errs;
}

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (!a.startsWith('--')) throw new Error(`unexpected argument ${a}`);
    const eqi = a.indexOf('=');
    if (eqi > 0) {
      out[a.slice(2, eqi)] = a.slice(eqi + 1);
      continue;
    }
    const v = argv[i + 1];
    if (v === undefined || v.startsWith('--')) {
      out[a.slice(2)] = true;
      continue;
    }
    out[a.slice(2)] = v;
    i++;
  }
  return out;
}
const need = (args, k) => {
  const v = args[k];
  if (v === undefined || v === true || String(v).trim() === '')
    throw new Error(`--${k} is required`);
  return String(v).trim();
};
const bool = (args, k) => {
  const v = need(args, k);
  if (v !== 'true' && v !== 'false') throw new Error(`--${k} must be true|false`);
  return v === 'true';
};
const writeImmutable = (out, rec) => {
  fs.writeFileSync(out, JSON.stringify(rec, null, 2) + '\n', { flag: 'wx', mode: 0o444 });
  console.log(`${sha256(fs.readFileSync(out))}  ${out}`);
};

export function buildBase(args) {
  const host = captureHostCheck({ reviewedCommit: 'HEAD' });
  if (!host.ok) throw new Error(`covered suite paths not clean: ${host.problems.join('; ')}`);
  if (args['release-kind'] && args['release-kind'] !== 'verification')
    throw new Error(
      'Wave01 base Release uses releaseKind verification for both phases (owner decision)'
    );
  const suite = suiteDigest();
  const rec = buildRelease({ ...args, 'release-kind': 'verification' });
  rec.scenarioSuiteSha = suite.digest;
  const errs = validateRecord(rec);
  if (errs.length) throw new Error(`base Release invalid: ${errs.join('; ')}`);
  return rec;
}

export function buildCompanion(args) {
  const baseFile = need(args, 'base');
  const baseBytes = fs.readFileSync(baseFile);
  const base = JSON.parse(baseBytes.toString('utf-8'));
  const host = captureHostCheck({ reviewedCommit: 'HEAD' });
  if (!host.ok) throw new Error(`covered suite paths not clean: ${host.problems.join('; ')}`);
  const suite = suiteDigest();
  const font = args['font-manifest-from-fc-list']
    ? fontManifestDigest()
    : {
        method: 'container-image-digest',
        sha256: need(args, 'font-image-digest').replace(/^sha256:/, ''),
      };
  const helperArg = need(args, 'fixture-helper');
  const fixtureHelper =
    helperArg === 'none'
      ? {
          status: 'not-used',
          reason:
            'batch states use only API-seeded fixtures (groups-v1); helper-dependent states are not run',
        }
      : { status: 'accepted', ...JSON.parse(fs.readFileSync(helperArg, 'utf-8')) };
  const credentialInventory = JSON.parse(
    fs.readFileSync(need(args, 'credential-inventory'), 'utf-8')
  );
  assertValueFree(credentialInventory, 'credential inventory');
  const rec = {
    schemaVersion: 1,
    kind: COMPANION_KIND,
    id: `${COMPANION_KIND}-${randomUUID()}`,
    createdAt: new Date().toISOString(),
    producer: need(args, 'steward'),
    baseReleaseId: base.id,
    baseReleaseSha256: sha256(baseBytes),
    slotGeneration: base.slotGeneration,
    baseURL: base.baseURL,
    releaseKind: base.releaseKind,
    phase: need(args, 'phase'),
    contract: { name: 'wave01-contract-FROZEN-rev2.md', sha256: CONTRACT_SHA256 },
    servedSourceSha: base.sourceSha,
    backendSourceSha: base.backendSourceSha,
    runner: {
      commit: suite.commit,
      suiteDigest: suite.digest,
      suiteFileCount: suite.fileCount,
      suiteDigestMethod: SUITE_DIGEST_METHOD,
      coveredPaths: [...COVERED_PATHS],
    },
    browser: { engine: 'chromium', version: need(args, 'browser-version') },
    fontImageDigest: font,
    settingsProfile: {
      'web.native_chat': bool(args, 'native-chat'),
      'web.terminal_workspace': bool(args, 'terminal-workspace'),
      settingsProfileSha: base.settingsProfileSha,
    },
    fixtures: {
      fixtureRecipeSha: base.fixtureRecipeSha,
      fixtureSnapshotSha256: base.fixtureSnapshotSha256,
      groupsFixtureMapSha256: sha256(fs.readFileSync(need(args, 'groups-fixture-map'))),
    },
    fixtureHelper,
    credentialInventory,
  };
  const errs = validatePair({ baseBytes, companion: rec });
  if (errs.length) throw new Error(`companion invalid:\n  ${errs.join('\n  ')}`);
  return rec;
}

function main() {
  const [cmd, ...rest] = process.argv.slice(2);
  const args = parseArgs(rest);
  if (cmd === 'base') {
    const out = need(args, 'out');
    delete args.out;
    writeImmutable(out, buildBase(args));
    return;
  }
  if (cmd === 'companion') {
    const out = need(args, 'out');
    writeImmutable(out, buildCompanion(args));
    return;
  }
  if (cmd === 'validate-pair') {
    const errs = validatePair({
      baseBytes: fs.readFileSync(need(args, 'base')),
      companion: JSON.parse(fs.readFileSync(need(args, 'companion'), 'utf-8')),
    });
    console.log(errs.length ? `INVALID\n  ${errs.join('\n  ')}` : 'ok');
    process.exit(errs.length ? 1 : 0);
  }
  console.error('usage: release.mjs base|companion|validate-pair …');
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
