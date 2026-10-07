#!/usr/bin/env node
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

// Phase-1 manual records helper (four records: Release, Capture, Finding,
// Verification).
//
//   node records.mjs release --out FILE --assets-dir DIR --binary FILE \
//        --lockfile FILE --source-sha SHA --backend-source-sha SHA \
//        --included-commits SHA[,SHA] --toolchain TEXT --base-url URL \
//        --slot-generation ID --release-kind preview|verification \
//        --steward ID --fixture-snapshot-sha256 HEX --schema-version-id ID \
//        --settings-profile FILE --publication-checkpoint TEXT
//      Computes binary/asset-tree/main.js/lockfile/settings/suite/recipe
//      digests and writes an immutable Release record (refuses to overwrite).
//
//   (release also needs --server-launch-flags="<non-secret server start flags>",
//    recorded as serverLaunch {hosted, runtimeBroker, testLogin, devAuth})
//
//   node records.mjs validate-run RUN_DIR --release FILE
//      Cross-checks every capture in a run against the Release (id, record
//      digest, slotGeneration, main.js pre/post) and run.json batch validity.
//
//   node records.mjs validate FILE...
//      Validates required fields / digest formats for any of the 4 records
//      (and capture records written by capture.pw.ts). Exit 1 on any error.

import { randomUUID } from 'node:crypto';
import * as fs from 'node:fs';
import * as path from 'node:path';
import {
  fixtureRecipeSha,
  scenarioSuiteSha,
  sha256,
  sha256File,
  SHA256_RE,
  treeDigest,
} from '../lib/digest.mjs';

const GIT_SHA_RE = /^[0-9a-f]{40}$/;

export const REQUIRED = {
  release: [
    'sourceSha',
    'includedCommits',
    'backendSourceSha',
    'binarySha256',
    'assetTreeSha256',
    'mainJsSha256',
    'lockfileSha256',
    'toolchain',
    'fixtureRecipeSha',
    'fixtureSnapshotSha256',
    'schemaVersionId',
    'scenarioSuiteSha',
    'settingsProfileSha',
    'baseURL',
    'slotGeneration',
    'stewardIdentity',
    'releaseKind',
    'publicationCheckpoint',
    'serverLaunch',
  ],
  capture: [
    'status',
    'scenarioKey',
    'profileKey',
    'capturerIdentity',
    'releaseId',
    'slotGeneration',
    'resourceKeys',
    'environment',
    'readyChecks',
    'actions',
    'assertions',
    'files',
    'startedAt',
    'endedAt',
    'mainJs',
    'runId',
    'releaseRecordSha256',
    'integration',
  ],
  finding: [
    'captureIds',
    'scenarioKey',
    'affectedProfiles',
    'category',
    'severity',
    'confidence',
    'symptom',
    'expectedBehavior',
    'reproduction',
    'location',
    'suspectedSharedCause',
    'dedupeKey',
    'acceptance',
  ],
  verification: [
    'findingId',
    'acceptanceSha256',
    'baselineCaptureIds',
    'candidateCaptureIds',
    'verificationReleaseId',
    'verificationSourceSha',
    'identities',
    'assertionsRun',
    'evidence',
    'verdict',
    'rationale',
    'remainingIssues',
    'verifierIdentity',
  ],
};

const SHA_FIELDS = {
  release: [
    'binarySha256',
    'assetTreeSha256',
    'mainJsSha256',
    'lockfileSha256',
    'fixtureRecipeSha',
    'fixtureSnapshotSha256',
    'scenarioSuiteSha',
    'settingsProfileSha',
  ],
  verification: ['acceptanceSha256'],
};

const isEmpty = (v) =>
  v === undefined ||
  v === null ||
  (typeof v === 'string' && v.trim() === '') ||
  (Array.isArray(v) && v.length === 0);

/** @returns {string[]} errors */
export function validateRecord(rec) {
  const errs = [];
  if (rec.schemaVersion !== 1) errs.push('schemaVersion must be 1');
  for (const f of ['id', 'createdAt', 'producer', 'kind'])
    if (isEmpty(rec[f])) errs.push(`missing envelope field ${f}`);
  if (rec.createdAt && !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$/.test(rec.createdAt))
    errs.push('createdAt must be UTC ISO (…Z)');
  const kind = rec.kind;
  if (!REQUIRED[kind]) {
    errs.push(`unknown kind ${kind}`);
    return errs;
  }
  for (const f of REQUIRED[kind]) if (isEmpty(rec[f])) errs.push(`missing/empty ${kind}.${f}`);
  for (const f of SHA_FIELDS[kind] ?? [])
    if (rec[f] && !SHA256_RE.test(rec[f])) errs.push(`${kind}.${f} is not 64-hex sha256`);
  if (kind === 'release') {
    if (rec.sourceSha && !GIT_SHA_RE.test(rec.sourceSha))
      errs.push('release.sourceSha must be a full 40-hex git SHA');
    if (rec.backendSourceSha && !GIT_SHA_RE.test(rec.backendSourceSha))
      errs.push('release.backendSourceSha must be a full 40-hex git SHA');
    for (const c of rec.includedCommits ?? [])
      if (!GIT_SHA_RE.test(c))
        errs.push(`release.includedCommits entry ${c} is not a full git SHA`);
    if (rec.releaseKind && !['preview', 'verification'].includes(rec.releaseKind))
      errs.push('release.releaseKind must be preview|verification');
    const sl = rec.serverLaunch ?? {};
    for (const k of ['hosted', 'runtimeBroker', 'testLogin', 'devAuth'])
      if (typeof sl[k] !== 'boolean') errs.push(`release.serverLaunch.${k} must be boolean`);
    if (sl.devAuth === true) errs.push('release.serverLaunch.devAuth must be false for the survey');
    try {
      const u = new URL(rec.baseURL);
      if (u.pathname !== '/' || u.search) errs.push('release.baseURL must be an origin');
    } catch {
      errs.push('release.baseURL is not a URL');
    }
  }
  if (kind === 'capture') {
    if (rec.releaseRecordSha256 && !SHA256_RE.test(rec.releaseRecordSha256))
      errs.push('capture.releaseRecordSha256 is not 64-hex');
    if (rec.mainJs && !('post' in rec.mainJs))
      errs.push('capture.mainJs.post missing (record must carry the post-batch digest)');
    if (typeof rec.batchValid !== 'boolean') errs.push('capture.batchValid must be boolean');
    for (const f of rec.files ?? []) {
      if (!f.path || path.isAbsolute(f.path) || f.path.split('/').includes('..'))
        errs.push(`capture file path not relative-safe: ${f.path}`);
      if (!SHA256_RE.test(f.sha256 ?? '')) errs.push(`capture file ${f.path} has no sha256`);
    }
  }
  if (kind === 'finding') {
    const a = rec.acceptance ?? {};
    for (const f of [
      'revision',
      'sha256',
      'frozenAt',
      'author',
      'requiredProfiles',
      'clauses',
      'verdictRules',
    ]) {
      if (isEmpty(a[f])) errs.push(`missing/empty finding.acceptance.${f}`);
    }
    if (a.sha256 && !SHA256_RE.test(a.sha256)) errs.push('finding.acceptance.sha256 is not 64-hex');
  }
  if (kind === 'verification') {
    if (rec.verdict && !['pass', 'fail', 'blocked', 'inconclusive'].includes(rec.verdict))
      errs.push('verification.verdict invalid');
    const ids = rec.identities ?? {};
    for (const f of ['author', 'capturer', 'reviewer', 'verifier'])
      if (isEmpty(ids[f])) errs.push(`missing verification.identities.${f}`);
    if (
      ids.author &&
      (ids.author === ids.capturer || ids.author === ids.verifier || ids.author === ids.reviewer)
    ) {
      errs.push('verification.identities: author must differ from capturer/reviewer/verifier');
    }
    if (ids.verifier && ids.verifier === ids.capturer)
      errs.push('verification.identities: verifier must differ from capturer');
  }
  return errs;
}

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (!a.startsWith('--')) throw new Error(`unexpected argument ${a}`);
    const eq = a.indexOf('=');
    if (eq > 0) {
      // --flag=value form (required when the value itself starts with "--")
      out[a.slice(2, eq)] = a.slice(eq + 1);
      continue;
    }
    const v = argv[i + 1];
    if (v === undefined || v.startsWith('--')) throw new Error(`flag ${a} needs a value`);
    out[a.slice(2)] = v;
    i++;
  }
  return out;
}

function need(args, k) {
  const v = args[k];
  if (v === undefined || String(v).trim() === '')
    throw new Error(`--${k} is required and must be non-empty`);
  return String(v).trim();
}

/**
 * Parse the steward's NON-SECRET server launch flags into the Release
 * `serverLaunch` summary. Rejects anything that looks like secret material.
 */
export function parseServerLaunch(flags) {
  if (/secret|token|password|key=/i.test(flags))
    throw new Error('--server-launch-flags must not contain secret material');
  const has = (f) => new RegExp(`(^|\\s)${f}(=true)?(\\s|$)`).test(flags);
  const off = (f) => new RegExp(`(^|\\s)${f}=false(\\s|$)`).test(flags);
  return {
    flags: flags.trim(),
    hosted: has('--hosted'),
    runtimeBroker: !off('--enable-runtime-broker') && has('--enable-runtime-broker'),
    testLogin: has('--enable-test-login'),
    devAuth: has('--dev-auth'),
  };
}

/**
 * Cross-record check for one published/unsealed capture run: every capture
 * must reference this Release (id, record digest, slotGeneration, main.js),
 * carry pre+post digests equal to the Release, and the batch must be valid.
 */
export function validateRun(runDir, releaseFile) {
  const errs = [];
  const bytes = fs.readFileSync(releaseFile);
  const release = JSON.parse(bytes.toString('utf-8'));
  const relSha = sha256(bytes);
  errs.push(...validateRecord(release).map((e) => `release: ${e}`));
  const run = JSON.parse(fs.readFileSync(path.join(runDir, 'run.json'), 'utf-8'));
  if (run.batchValid !== true) errs.push('run.json batchValid is not true');
  if (run.releaseRecordSha256 !== relSha)
    errs.push('run.json releaseRecordSha256 != release file digest');
  const captures = fs.readdirSync(runDir).filter((f) => f.endsWith('.capture.json'));
  if (captures.length === 0) errs.push('no capture records in run');
  const ids = new Set();
  for (const f of captures) {
    const c = JSON.parse(fs.readFileSync(path.join(runDir, f), 'utf-8'));
    ids.add(c.id);
    errs.push(...validateRecord(c).map((e) => `${f}: ${e}`));
    if (c.runId !== run.runId) errs.push(`${f}: runId != run.json runId`);
    if (c.releaseId !== release.id) errs.push(`${f}: releaseId != release.id`);
    if (c.releaseRecordSha256 !== relSha)
      errs.push(`${f}: releaseRecordSha256 != release file digest`);
    if (c.slotGeneration !== release.slotGeneration) errs.push(`${f}: slotGeneration mismatch`);
    if (c.mainJs?.expected !== release.mainJsSha256)
      errs.push(`${f}: mainJs.expected != release.mainJsSha256`);
    if (c.mainJs?.pre?.sha256 !== release.mainJsSha256) errs.push(`${f}: mainJs.pre != release`);
    if (c.mainJs?.post?.sha256 !== release.mainJsSha256) errs.push(`${f}: mainJs.post != release`);
    if (c.batchValid !== true) errs.push(`${f}: batchValid is not true`);
    if (c.status !== 'complete') errs.push(`${f}: status ${c.status}`);
  }
  for (const id of run.captureIds ?? [])
    if (!ids.has(id)) errs.push(`run.json captureId ${id} has no record`);
  return errs;
}

export function buildRelease(args) {
  const assetsDir = need(args, 'assets-dir');
  const mainJs = path.join(assetsDir, 'assets', 'main.js');
  if (!fs.existsSync(mainJs))
    throw new Error(`${mainJs} not found (expected built web/dist/client)`);
  const rec = {
    schemaVersion: 1,
    kind: 'release',
    id: `release-${randomUUID()}`,
    createdAt: new Date().toISOString(),
    producer: need(args, 'steward'),
    sourceSha: need(args, 'source-sha'),
    includedCommits: need(args, 'included-commits')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean),
    backendSourceSha: need(args, 'backend-source-sha'),
    binarySha256: sha256File(need(args, 'binary')),
    assetTreeSha256: treeDigest(assetsDir),
    mainJsSha256: sha256File(mainJs),
    lockfileSha256: sha256File(need(args, 'lockfile')),
    toolchain: need(args, 'toolchain'),
    fixtureRecipeSha: fixtureRecipeSha(),
    fixtureSnapshotSha256: need(args, 'fixture-snapshot-sha256'),
    schemaVersionId: need(args, 'schema-version-id'),
    scenarioSuiteSha: scenarioSuiteSha(),
    settingsProfileSha: sha256File(need(args, 'settings-profile')),
    baseURL: new URL(need(args, 'base-url')).origin,
    slotGeneration: need(args, 'slot-generation'),
    stewardIdentity: need(args, 'steward'),
    releaseKind: need(args, 'release-kind'),
    publicationCheckpoint: need(args, 'publication-checkpoint'),
    serverLaunch: parseServerLaunch(need(args, 'server-launch-flags')),
  };
  const errs = validateRecord(rec);
  if (errs.length) throw new Error(`release record invalid:\n  ${errs.join('\n  ')}`);
  return rec;
}

function main() {
  const [cmd, ...rest] = process.argv.slice(2);
  if (cmd === 'release') {
    const args = parseArgs(rest);
    const out = need(args, 'out');
    delete args.out;
    const rec = buildRelease(args);
    fs.writeFileSync(out, JSON.stringify(rec, null, 2) + '\n', { flag: 'wx', mode: 0o444 });
    console.log(`${sha256File(out)}  ${out}`);
    return;
  }
  if (cmd === 'validate') {
    if (rest.length === 0) throw new Error('validate needs at least one file');
    let bad = 0;
    for (const f of rest) {
      const errs = validateRecord(JSON.parse(fs.readFileSync(f, 'utf-8')));
      console.log(
        `${errs.length ? 'INVALID' : 'ok     '} ${f}${errs.length ? '\n  ' + errs.join('\n  ') : ''}`
      );
      if (errs.length) bad++;
    }
    process.exit(bad ? 1 : 0);
  }
  if (cmd === 'validate-run') {
    const args = parseArgs(rest.slice(1));
    if (!rest[0] || rest[0].startsWith('--'))
      throw new Error('validate-run needs RUN_DIR --release FILE');
    const errs = validateRun(rest[0], need(args, 'release'));
    console.log(errs.length ? `INVALID ${rest[0]}\n  ${errs.join('\n  ')}` : `ok      ${rest[0]}`);
    process.exit(errs.length ? 1 : 0);
  }
  console.error(
    'usage: records.mjs release --out FILE ... | validate FILE... | validate-run RUN_DIR --release FILE'
  );
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
