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

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { describe, expect, it } from 'vitest';
import { CONTRACT, POLICIES } from '../wave01/contract.js';
import { STATES, substepsFor } from '../wave01/manifest.js';
import {
  applyBatch,
  clearQuarantine,
  emptyLedger,
  DEFAULT_ENV_MAX_AGE_MIN,
  canonicalOrigin,
  envDecision,
  envStop,
  evaluateEnvPost,
  hostBinding,
  provenanceKey,
  preComparisonInputs,
  provenanceEnvKey,
  provenanceSupportKey,
  evaluateEnv,
  isQuarantined,
  validateCapture,
  validateRun,
  validationRecord,
} from '../wave01/records.mjs';
import {
  CONTRACT_SHA256,
  assertValueFree,
  validateDistinctPairs,
  validatePair,
} from '../wave01/release.mjs';
import {
  digestEntries,
  repoRoot,
  suiteDigest,
  SUITE_DIGEST_METHOD,
} from '../wave01/suite-digest.mjs';

const PROV = {
  flags: { '--hosted': true, '--production': 'absent', '--dev-auth': false },
  load_path: 'settings-global',
  files_examined: ['~/.scion/settings.yaml'],
  path_values: {
    'server.mode': 'hosted',
    'server.auth.dev_mode': false,
    'server.auth.mode': 'absent',
  },
  env: {
    SCION_SERVER_MODE: 'absent',
    SCION_SERVER_AUTH_DEVMODE: 'absent',
    SCION_SERVER_AUTH_MODE: 'absent',
    SCION_SERVER_AUTH_DEV_MODE: 'absent',
  },
  declared_effective_hosted: true,
  declared_effective_auth_mode: 'unset',
  support: {
    hosted: {
      log_line: 'Server mode: hosted',
      log_ts: '2026-10-07T15:30:05Z',
      process_start_ts: '2026-10-07T15:30:00Z',
      process_start_source: 'proc:/proc/<pid>/stat starttime',
      log_ts_source: 'journal:scion-hub.service',
      slot_generation: 'gen-1',
    },
    dev_auth: { basis: 'recorded-inputs', dev_auth_warning_present: false },
    auth_mode: { source: 'unset-default' },
  },
};
const PROBES_OFF = {
  api: {
    status: 401,
    message: 'development authentication is not enabled',
    at: '2026-10-07T15:59:30Z',
  },
  web: { status: 401, hasIdentity: false, at: '2026-10-07T15:59:31Z' },
};
const ev = (...a: Parameters<typeof evaluateEnv>) =>
  evaluateEnv(a[0], a[1], a[2], a[3], a[4] === undefined ? PROBES_OFF : a[4]);

const sha = (b: Buffer | string) => createHash('sha256').update(b).digest('hex');

describe('suite digest (§5b E-RUN, assessor-confirmed canonical form)', () => {
  it('sorts whole lines bytewise (by hash first), not by path', () => {
    const entries = [
      { path: 'web/e2e/a.ts', bytes: Buffer.from('zzz') },
      { path: 'web/e2e/b.ts', bytes: Buffer.from('aaa') },
    ];
    const r = digestEntries(entries);
    const lines = r.manifest.trimEnd().split('\n');
    const hashes = lines.map((l) => l.slice(0, 64));
    expect([...hashes].sort()).toEqual(hashes);
    expect(r.digest).toBe(sha(r.manifest));
    expect(r.manifest.endsWith('\n')).toBe(true);
    expect(lines[0]).toMatch(/^[0-9a-f]{64} {2}web\/e2e\/[ab]\.ts$/);
  });

  it('reproduces the assessor pre-extension references at 1694e511 (if that commit is present)', () => {
    const root = repoRoot();
    let has = true;
    try {
      execFileSync('git', ['cat-file', '-e', '1694e51145a0a26bedf754751a130d7b05544232^{commit}'], {
        cwd: root,
      });
    } catch {
      has = false;
    }
    if (!has) return;
    const at = (paths: string[]) =>
      suiteDigest({ commit: '1694e51145a0a26bedf754751a130d7b05544232', paths });
    expect(at(['web/e2e/layout-survey/'])).toMatchObject({
      fileCount: 23,
      digest: '1a5c52de890b5fb1a5e3ebc0b03e0a02d6dc535a71efa6b881b8e8d9823b2853',
    });
    expect(at(['web/e2e/harness/'])).toMatchObject({
      fileCount: 7,
      digest: '54f9893c42e123a2a7b5fd9ebf3add9a045ab5672ee5f1cde04cc27cda3725b4',
    });
    expect(
      at(['web/e2e/harness/auth.ts', 'web/e2e/harness/hub.ts', 'web/e2e/harness/seed.ts']).digest
    ).toBe('4167934f013b69167a2c6af8e24e19183350d7f456502c675aca63f1e1c12441');
    expect(at(['web/e2e/layout-survey/', 'web/e2e/harness/'])).toMatchObject({
      fileCount: 30,
      digest: '54a591ff1a9406e049fd116336fe48c0581f274c0874ce9aa7f5ddd462218d6f',
    });
  });
});

describe('manifest and pins', () => {
  it('pins contract rev 7', () => {
    expect(CONTRACT.sha256).toBe(
      '3e3662f5080c084dac2de8445923642592e0db048b7b321c77c5274a64d34008'
    );
    expect(CONTRACT_SHA256).toBe(CONTRACT.sha256);
  });
  it('has exactly the 15 frozen states, 3 profiles ⇒ 45 primary captures; A-N1 only on S04/S05', () => {
    expect(STATES.map((s) => s.id)).toEqual(
      Array.from({ length: 15 }, (_, i) => `W01-S${String(i + 1).padStart(2, '0')}`)
    );
    expect(STATES.filter((s) => s.m2).map((s) => s.id)).toEqual(['W01-S04', 'W01-S05']);
    expect(substepsFor(STATES[1]!)).toEqual(['M0', 'M1', 'M3']);
  });
  it('unimplemented adapters are explicitly blocked, never implemented placeholders', () => {
    for (const s of STATES) {
      if (s.id === 'W01-S01' || s.id === 'W01-S02') expect(s.adapter).toBe('implemented');
      else expect(s).toMatchObject({ adapter: 'blocked' });
    }
  });
  it('policy table carries only contract policy IDs', () => {
    const ids = new Set(POLICIES.map((p) => p.id));
    expect([...ids].every((id) => /^POL-(V|H|E|CLAMP|COLLAPSED)-/.test(id))).toBe(true);
  });
});

function mkPair(over: { base?: Record<string, unknown>; comp?: Record<string, unknown> } = {}) {
  const base = {
    schemaVersion: 1,
    kind: 'release',
    id: 'release-1',
    createdAt: '2026-10-07T15:00:00Z',
    producer: 'ii2',
    sourceSha: '1694e51145a0a26bedf754751a130d7b05544232',
    includedCommits: ['1694e51145a0a26bedf754751a130d7b05544232'],
    backendSourceSha: '4a253489ebe3298fcfe4d7271b3642a5578b2b31',
    binarySha256: 'a'.repeat(64),
    assetTreeSha256: 'b'.repeat(64),
    mainJsSha256: 'c'.repeat(64),
    lockfileSha256: 'd'.repeat(64),
    toolchain: 'node 24',
    fixtureRecipeSha: 'e'.repeat(64),
    fixtureSnapshotSha256: 'f'.repeat(64),
    schemaVersionId: 'x',
    scenarioSuiteSha: '1'.repeat(64),
    settingsProfileSha: '2'.repeat(64),
    baseURL: 'https://baseline.example',
    slotGeneration: 'gen-1',
    stewardIdentity: 'ii2',
    releaseKind: 'verification',
    publicationCheckpoint: 'p',
    serverLaunch: { hosted: true, runtimeBroker: false, testLogin: true, devAuth: false },
    ...over.base,
  };
  const baseBytes = Buffer.from(JSON.stringify(base));
  const comp = {
    schemaVersion: 1,
    kind: 'wave01-release-ext',
    id: 'wave01-release-ext-1',
    createdAt: '2026-10-07T15:01:00Z',
    producer: 'ii2',
    baseReleaseId: 'release-1',
    baseReleaseSha256: sha(baseBytes),
    slotGeneration: 'gen-1',
    baseURL: 'https://baseline.example',
    releaseKind: 'verification',
    phase: 'baseline',
    contract: { name: 'wave01-contract-FROZEN-rev7.md', sha256: CONTRACT_SHA256 },
    servedSourceSha: base.sourceSha,
    backendSourceSha: base.backendSourceSha,
    runner: {
      commit: '9'.repeat(40),
      suiteDigest: '1'.repeat(64),
      suiteFileCount: 40,
      suiteDigestMethod: SUITE_DIGEST_METHOD,
    },
    browser: { engine: 'chromium', version: '140.0.0.0' },
    fontImageDigest: { method: 'container-image-digest', sha256: '3'.repeat(64) },
    settingsProfile: {
      'web.native_chat': false,
      'web.terminal_workspace': false,
      settingsProfileSha: '2'.repeat(64),
    },
    fixtures: {
      fixtureRecipeSha: 'e'.repeat(64),
      fixtureSnapshotSha256: 'f'.repeat(64),
      groupsFixtureMapSha256: '4'.repeat(64),
    },
    fixtureHelper: { status: 'not-used', reason: 'groups only' },
    credentialInventory: { counts: { 'access-token': 1 }, roles: ['admin'], validityWindows: [] },
    ...over.comp,
  };
  return { base, baseBytes, comp };
}

describe('Release pair (base + wave01-release-ext companion)', () => {
  it('a consistent pair validates', () => {
    const { baseBytes, comp } = mkPair();
    expect(validatePair({ baseBytes, companion: comp })).toEqual([]);
  });
  it('provenance mismatch is rejected, never overridden', () => {
    const { baseBytes, comp } = mkPair({ comp: { slotGeneration: 'gen-2' } });
    expect(
      validatePair({ baseBytes, companion: comp }).some((e) =>
        e.startsWith('mismatch: companion.slotGeneration')
      )
    ).toBe(true);
    const p2 = mkPair();
    expect(
      validatePair({
        baseBytes: Buffer.from(p2.baseBytes.toString() + ' '),
        companion: p2.comp,
      }).some((e) => e.includes('baseReleaseSha256'))
    ).toBe(true);
    const p3 = mkPair({
      comp: { runner: { ...mkPair().comp.runner, suiteDigest: '7'.repeat(64) } },
    });
    expect(
      validatePair({ baseBytes: p3.baseBytes, companion: p3.comp }).some((e) =>
        e.includes('suiteDigest vs base.scenarioSuiteSha')
      )
    ).toBe(true);
  });
  it('releaseKind must be verification on both and agree', () => {
    const p = mkPair({ base: { releaseKind: 'preview' }, comp: { releaseKind: 'preview' } });
    expect(
      validatePair({ baseBytes: p.baseBytes, companion: p.comp }).some((e) =>
        e.startsWith('mismatch: base.releaseKind')
      )
    ).toBe(true);
  });
  it('baseline must serve 1694e511; a candidate must not', () => {
    const p = mkPair({ comp: { phase: 'candidate' } });
    expect(
      validatePair({ baseBytes: p.baseBytes, companion: p.comp }).some((e) =>
        e.includes('candidate phase')
      )
    ).toBe(true);
  });
  it('runtime expectations (runner HEAD, suite digest, browser) must match', () => {
    const { baseBytes, comp } = mkPair();
    const errs = validatePair({
      baseBytes,
      companion: comp,
      expect: { runnerCommit: '8'.repeat(40), browserVersion: '141' },
    });
    expect(errs.filter((e) => e.startsWith('mismatch')).length).toBe(2);
  });
  it('missing normative fields are reported as missing (INCONCLUSIVE), not passed', () => {
    const { baseBytes, comp } = mkPair();
    const { credentialInventory: _drop, ...rest } = comp;
    expect(
      validatePair({ baseBytes, companion: rest }).some((e) =>
        e.startsWith('missing: companion.credentialInventory')
      )
    ).toBe(true);
  });
  it('baseline/candidate pairs must differ by host, not port alone (R-2)', () => {
    const a = mkPair();
    const b = mkPair({
      base: { baseURL: 'https://baseline.example:8443', id: 'release-2', slotGeneration: 'gen-2' },
      comp: { id: 'c2' },
    });
    expect(
      validateDistinctPairs(
        { base: a.base, companion: a.comp },
        { base: b.base, companion: b.comp }
      )
    ).toContain('baseline/candidate baseURL hosts must differ (not ports alone)');
  });
  it('R-2: equal slotGeneration values on different hosts are allowed; ids/file shas must differ', () => {
    const a = mkPair();
    const b = mkPair({
      base: { baseURL: 'https://candidate.example', id: 'release-2' },
      comp: { id: 'c2' },
    });
    expect(
      validateDistinctPairs(
        { base: a.base, companion: a.comp, baseSha256: 'x', companionSha256: 'y' },
        { base: b.base, companion: b.comp, baseSha256: 'z', companionSha256: 'w' }
      )
    ).toEqual([]);
    expect(
      validateDistinctPairs(
        { base: a.base, companion: a.comp, baseSha256: 'x' },
        { base: b.base, companion: b.comp, baseSha256: 'x' }
      )
    ).toContain('same base Release file sha256');
  });
  it('value-free guard rejects token-like strings', () => {
    expect(() =>
      assertValueFree(
        { x: 'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4eHh4eHgifQ.c2lnbmF0dXJlLXZhbHVl' },
        't'
      )
    ).toThrow(/JWT/);
    expect(() => assertValueFree({ refreshToken: 'abc' }, 't')).toThrow(/must not carry a value/);
  });
});

describe('E-ENV gates (capture-window bound; review1 B2/B3)', () => {
  const batchStart = '2026-10-07T16:00:00Z';
  const W = { batchStart, maxAgeMin: 60 };
  const decl = {
    window_start: '2026-10-07T15:50:00Z',
    slotGeneration: 'gen-1',
    baseURL: 'https://baseline.example',
    e_env_1_dev_auth_effective: false,
    e_env_1_provenance: PROV,
    e_env_3_test_login_enabled: true,
    e_env_4_runtime_broker_effective: false,
    e_env_4_no_broker_process_or_dispatch: true,
    e_env_2_anon_401: { status: 401, ts: '2026-10-07T15:51:00Z' },
    e_env_5: {
      probe: 'repo-default-secret-challenge',
      result_status: 401,
      ts: '2026-10-07T15:52:00Z',
    },
  };
  const self = { status: 401, at: '2026-10-07T15:59:00Z' };
  const rel = { slotGeneration: 'gen-1', baseURL: 'https://baseline.example' };
  const gate = (r: ReturnType<typeof evaluateEnv>, id: string) => r.find((x) => x.gate === id)!;

  it('complete, bound evidence ⇒ E-ENV-1/2/3/5 pass, E-ENV-4 awaits POST evidence, no stop', () => {
    const r = ev(decl, self, rel, W);
    expect(['E-ENV-1', 'E-ENV-2', 'E-ENV-3', 'E-ENV-5'].map((g) => gate(r, g).outcome)).toEqual([
      'pass',
      'pass',
      'pass',
      'pass',
    ]);
    expect(gate(r, 'E-ENV-4')).toMatchObject({ outcome: 'inconclusive', awaitingPost: true });
    expect(envDecision(r)).toMatchObject({ stop: false, fails: [], missing: [] });
  });
  it('review1 B2 repro: broker effective ON + anonymous 200 ⇒ FAIL and stop', () => {
    const r = ev(
      { ...decl, e_env_4_runtime_broker_effective: true },
      { ...self, status: 200 },
      rel,
      W
    );
    expect(gate(r, 'E-ENV-2').outcome).toBe('fail');
    expect(gate(r, 'E-ENV-4').outcome).toBe('fail');
    expect(envDecision(r)).toMatchObject({ stop: true, fails: ['E-ENV-2', 'E-ENV-4'] });
  });
  it('review1 B3 repro: stale window, stale E-ENV-5 ts, empty sources, no-dispatch false ⇒ never all-pass; stop', () => {
    const r = ev(
      {
        ...decl,
        window_start: '2020-01-01T00:00:00Z',
        e_env_5: { ...decl.e_env_5, ts: '2020-01-01T00:00:00Z' },
        e_env_1_sources: [],
        e_env_4_no_broker_process_or_dispatch: false,
      },
      self,
      rel,
      W
    );
    expect(gate(r, 'E-ENV-1').outcome).toBe('inconclusive');
    expect(gate(r, 'E-ENV-4').outcome).toBe('fail');
    expect(gate(r, 'E-ENV-5').outcome).toBe('inconclusive');
    expect(envDecision(r).stop).toBe(true);
  });
  it('missing E-ENV-1b provenance record ⇒ inconclusive (stop) even with probes OFF', () => {
    const { e_env_1_provenance: _p, ...noProv } = decl;
    const r = ev(noProv, self, rel, W);
    expect(gate(r, 'E-ENV-1').outcome).toBe('inconclusive');
    expect(envDecision(r).missing).toContain('E-ENV-1');
  });
  it('missing no-dispatch attestation ⇒ E-ENV-4 inconclusive without awaitingPost (stop)', () => {
    const { e_env_4_no_broker_process_or_dispatch: _x, ...d } = decl;
    const r = ev(d, self, rel, W);
    expect(gate(r, 'E-ENV-4')).toMatchObject({ outcome: 'inconclusive', awaitingPost: false });
    expect(envDecision(r).stop).toBe(true);
  });
  it('window in the future or older than maxAge ⇒ inconclusive; window_ts alias accepted', () => {
    expect(
      gate(ev({ ...decl, window_start: '2026-10-07T16:05:00Z' }, self, rel, W), 'E-ENV-1').outcome
    ).toBe('inconclusive');
    expect(
      gate(ev({ ...decl, window_start: '2026-10-07T14:30:00Z' }, self, rel, W), 'E-ENV-1').outcome
    ).toBe('inconclusive');
    const { window_start: ws, ...d } = decl;
    expect(gate(ev({ ...d, window_ts: ws }, self, rel, W), 'E-ENV-1').outcome).toBe('pass');
  });
  it('default max age is 30 min (ii2-confirmed): a 40-min-old window is stale without an explicit override', () => {
    expect(DEFAULT_ENV_MAX_AGE_MIN).toBe(30);
    const d = { ...decl, window_start: '2026-10-07T15:20:00Z' };
    expect(gate(ev(d, self, rel, { batchStart }), 'E-ENV-1').outcome).toBe('inconclusive');
    expect(gate(ev(d, self, rel, { batchStart, maxAgeMin: 60 }), 'E-ENV-1').outcome).toBe('pass');
  });
  it('a self probe before the window start is not evidence for this window', () => {
    expect(
      gate(ev(decl, { status: 401, at: '2026-10-07T15:00:00Z' }, rel, W), 'E-ENV-2').outcome
    ).toBe('inconclusive');
  });
  it('accepted repo-default secret ⇒ E-ENV-5 FAIL, stop, security report', () => {
    const r = ev({ ...decl, e_env_5: { ...decl.e_env_5, result_status: 200 } }, self, rel, W);
    expect(gate(r, 'E-ENV-5').outcome).toBe('fail');
    expect(envDecision(r)).toMatchObject({ stop: true, securityReport: true });
    expect(envStop(r)).toBe(true);
  });
  it('a redirect is not a 401', () => {
    expect(gate(ev(decl, { ...self, status: 302 }, rel, W), 'E-ENV-2').outcome).toBe('fail');
  });
  it('missing declaration or other slot generation ⇒ inconclusive', () => {
    expect(ev(null, self, rel, W).filter((g) => g.outcome === 'inconclusive')).toHaveLength(4);
    expect(
      gate(
        ev(decl, self, { slotGeneration: 'gen-9', baseURL: 'https://baseline.example' }, W),
        'E-ENV-1'
      ).outcome
    ).toBe('inconclusive');
  });
  it('POST declaration must cover the batch and attest no broker process/dispatch', () => {
    const run = {
      startedAt: '2026-10-07T16:00:00Z',
      endedAt: '2026-10-07T16:20:00Z',
      slotGeneration: 'gen-1',
      baseURL: 'https://baseline.example',
      preBaseURL: 'https://baseline.example',
      preValues: {
        e_env_1_dev_auth_effective: false,
        e_env_3_test_login_enabled: true,
        e_env_4_runtime_broker_effective: false,
        e_env_4_no_broker_process_or_dispatch: true,
      },
      preProvenanceKey: provenanceEnvKey(PROV),
      preSupportKey: provenanceSupportKey(PROV),
      preProcessStartTs: '2026-10-07T15:30:00Z',
    };
    const post = {
      baseURL: 'https://baseline.example',
      window_start: '2026-10-07T15:50:00Z',
      window_end: '2026-10-07T16:25:00Z',
      serving_process_start_ts: '2026-10-07T15:30:00Z',
      slotGeneration: 'gen-1',
      e_env_1_dev_auth_effective: false,
      e_env_1_provenance: PROV,
      e_env_3_test_login_enabled: true,
      e_env_4_runtime_broker_effective: false,
      e_env_4_no_broker_process_or_dispatch: true,
    };
    expect(evaluateEnvPost(post, run).outcome).toBe('pass');
    expect(evaluateEnvPost({ ...post, window_end: '2026-10-07T16:10:00Z' }, run).outcome).toBe(
      'inconclusive'
    );
    expect(
      evaluateEnvPost({ ...post, e_env_4_no_broker_process_or_dispatch: false }, run).outcome
    ).toBe('fail');
    expect(evaluateEnvPost(null, run).outcome).toBe('inconclusive');
    // host binding (ii2/review2 15:54Z; owner 15:55Z canonical origin)
    expect(evaluateEnvPost({ ...post, baseURL: 'https://candidate.example' }, run).outcome).toBe(
      'inconclusive'
    );
    expect(
      evaluateEnvPost(post, { ...run, preBaseURL: 'https://candidate.example' }).problems.join(' ')
    ).toContain('!= PRE baseURL');
    expect(evaluateEnvPost({ ...post, baseURL: undefined }, run).outcome).toBe('inconclusive');
    // R-3: PRE/POST on different hosts with a forbidden POST value ⇒ INCONCLUSIVE (unattributable)
    const cross = evaluateEnvPost(
      { ...post, baseURL: 'https://candidate.example', e_env_4_runtime_broker_effective: true },
      run
    );
    expect(cross).toMatchObject({ outcome: 'inconclusive', attributable: false });
    // R-3: same host+generation, PRE broker off / POST broker on ⇒ FAIL
    expect(evaluateEnvPost({ ...post, e_env_4_runtime_broker_effective: true }, run)).toMatchObject(
      {
        outcome: 'fail',
        attributable: true,
      }
    );
  });

  it('negative control: SAME slotGeneration on a DIFFERENT host is never evidence for this run (R-2)', () => {
    const r = ev({ ...decl, baseURL: 'https://candidate.example' }, self, rel, W);
    for (const g of ['E-ENV-1', 'E-ENV-3', 'E-ENV-4', 'E-ENV-5'])
      expect(gate(r, g).outcome, g).toBe('inconclusive');
    expect(gate(r, 'E-ENV-4').awaitingPost).toBe(false);
    expect(JSON.stringify(gate(r, 'E-ENV-1').details)).toContain('crossHost');
    expect(envDecision(r).stop).toBe(true);
  });
  it('R-3: a cross-host declaration is unattributable ⇒ INCONCLUSIVE even with forbidden values', () => {
    const r = ev(
      {
        ...decl,
        baseURL: 'https://candidate.example',
        e_env_1_dev_auth_effective: true,
        e_env_5: { ...decl.e_env_5, result_status: 200 },
      },
      self,
      rel,
      W
    );
    expect(gate(r, 'E-ENV-1').outcome).toBe('inconclusive');
    expect(gate(r, 'E-ENV-5').outcome).toBe('inconclusive');
    expect(envDecision(r)).toMatchObject({ stop: true, fails: [] });
  });
  it('R-3: generation mismatch is recorded as bindingMismatch and is INCONCLUSIVE', () => {
    const r = ev(
      { ...decl, slotGeneration: 'gen-2', e_env_4_runtime_broker_effective: true },
      self,
      rel,
      W
    );
    expect(gate(r, 'E-ENV-4').outcome).toBe('inconclusive');
    expect(JSON.stringify(gate(r, 'E-ENV-4').details)).toContain('bindingMismatch');
  });
  it('R-3: a BOUND declaration reporting a forbidden state is FAIL', () => {
    expect(
      gate(ev({ ...decl, e_env_1_dev_auth_effective: true }, self, rel, W), 'E-ENV-1').outcome
    ).toBe('fail');
  });
  it('baseURL is compared as a parsed canonical origin: no host alias, no IP, no path, port matters', () => {
    expect(canonicalOrigin('https://baseline.example/')).toBe('https://baseline.example');
    expect(canonicalOrigin('https://10.0.0.5')).toBeNull();
    expect(canonicalOrigin('https://baseline.example/app')).toBeNull();
    expect(canonicalOrigin('baseline.example')).toBeNull();
    expect(hostBinding({ host: 'baseline.example' }, 'https://baseline.example').status).toBe(
      'missing'
    );
    expect(
      hostBinding({ baseURL: 'https://baseline.example:8443' }, 'https://baseline.example').status
    ).toBe('mismatch');
    expect(
      hostBinding({ baseURL: 'https://BASELINE.example' }, 'https://baseline.example').status
    ).toBe('ok');
    const { baseURL: _b, ...noUrl } = decl;
    expect(gate(ev(noUrl, self, rel, W), 'E-ENV-1').outcome).toBe('inconclusive');
  });
});

describe('E7 quarantine', () => {
  const rec = (status: string, i: number) => ({ id: `c${i}`, stateId: 'W01-S01', status });
  it('3 consecutive capture errors for one state ⇒ quarantined; a complete record resets', () => {
    let l = applyBatch(emptyLedger(), 'r1', [rec('capture-error', 1), rec('capture-error', 2)]);
    expect(isQuarantined(l, 'W01-S01')).toBe(false);
    l = applyBatch(l, 'r2', [rec('complete', 3)]);
    expect(l.states['W01-S01']!.consecutiveErrors).toBe(0);
    l = applyBatch(l, 'r3', [
      rec('capture-error', 4),
      rec('capture-error', 5),
      rec('blocked', 6),
      rec('capture-error', 7),
    ]);
    expect(isQuarantined(l, 'W01-S01')).toBe(true);
  });
  it('clearing needs an owner identity and reason', () => {
    const l = applyBatch(emptyLedger(), 'r', [
      rec('capture-error', 1),
      rec('capture-error', 2),
      rec('capture-error', 3),
    ]);
    expect(() => clearQuarantine(l, 'W01-S01', '', '')).toThrow();
    expect(
      isQuarantined(clearQuarantine(l, 'W01-S01', 'web-layout-arch', 'slot restarted'), 'W01-S01')
    ).toBe(false);
  });
});

describe('capture records and validate-run', () => {
  const POST_OK = {
    baseURL: 'https://baseline.example',
    window_start: '2026-10-07T15:00:00Z',
    window_end: '2026-10-07T16:00:00Z',
    serving_process_start_ts: '2026-10-07T15:30:00Z',
    slotGeneration: 'gen-1',
    e_env_1_dev_auth_effective: false,
    e_env_1_provenance: PROV,
    e_env_3_test_login_enabled: true,
    e_env_4_runtime_broker_effective: false,
    e_env_4_no_broker_process_or_dispatch: true,
  };
  function writeRun(
    mutate?: (c: Record<string, any>) => void,
    mutateRun?: (r: Record<string, any>) => void,
    envPost: Record<string, unknown> | null | 'omit' = POST_OK
  ) {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'wave01-run-'));
    const { base, baseBytes, comp } = mkPair();
    fs.writeFileSync(path.join(dir, 'base.json'), baseBytes);
    fs.writeFileSync(path.join(dir, 'comp.json'), JSON.stringify(comp));
    const compSha = sha(fs.readFileSync(path.join(dir, 'comp.json')));
    const runDir = path.join(dir, 'run');
    fs.mkdirSync(runDir);
    fs.writeFileSync(path.join(runDir, 'shot.png'), 'png-bytes');
    const capture: Record<string, any> = {
      schemaVersion: 1,
      kind: 'wave01-capture',
      id: 'cap-1',
      createdAt: '2026-10-07T15:10:00Z',
      producer: 'ii2',
      runId: 'run-1',
      contractSha256: CONTRACT_SHA256,
      stateId: 'W01-S01',
      profile: { id: 'P1', width: 390, height: 844 },
      substep: 'M0',
      status: 'complete',
      errorReason: null,
      evidenceMode: 'evidence',
      baseReleaseId: base.id,
      baseReleaseSha256: sha(baseBytes),
      companionId: comp.id,
      companionSha256: compSha,
      slotGeneration: base.slotGeneration,
      releaseKind: 'verification',
      phase: 'baseline',
      runner: { head: comp.runner.commit, suiteDigest: comp.runner.suiteDigest },
      mainJs: {
        expected: base.mainJsSha256,
        pre: { sha256: base.mainJsSha256 },
        post: { sha256: base.mainJsSha256 },
      },
      batchValid: true,
      readiness: [],
      actions: [],
      outcomes: [{ clause: 'B-C1', outcome: 'pass', policyIds: [] }],
      files: [
        { kind: 'M0-primary-screenshot-fullpage', path: 'shot.png', sha256: sha('png-bytes') },
      ],
      loadedScripts: [{ url: 'https://baseline.example/assets/main.js' }],
      loadedMainEntry: {
        url: 'https://baseline.example/assets/main.js',
        sha256: base.mainJsSha256,
      },
      startedAt: 'a',
      endedAt: 'b',
    };
    mutate?.(capture);
    fs.writeFileSync(path.join(runDir, 'W01-S01.P1.M0.capture.json'), JSON.stringify(capture));
    // O1: a real bound PRE declaration embedded raw; validate-run recomputes the gates.
    const preDecl = {
      baseURL: 'https://baseline.example',
      window_start: '2026-10-07T15:35:00Z',
      slotGeneration: 'gen-1',
      e_env_1_dev_auth_effective: false,
      e_env_1_provenance: PROV,
      e_env_2_anon_401: { status: 401, ts: '2026-10-07T15:36:00Z' },
      e_env_3_test_login_enabled: true,
      e_env_4_runtime_broker_effective: false,
      e_env_4_no_broker_process_or_dispatch: true,
      e_env_5: {
        probe: 'repo-default-secret-challenge',
        result_status: 401,
        ts: '2026-10-07T15:36:30Z',
      },
    };
    const preRaw = JSON.stringify(preDecl);
    const preProbes = {
      api: {
        status: 401,
        message: 'development authentication is not enabled',
        at: '2026-10-07T15:39:30Z',
      },
      web: { status: 401, hasIdentity: false, at: '2026-10-07T15:39:31Z' },
    };
    const runObj: Record<string, any> = {
      envPreDeclarationRaw: preRaw,
      envDeclarationSha256: sha(preRaw),
      envPreSelfAnon401: { status: 401, at: '2026-10-07T15:39:00Z' },
      envPreDevAuthProbes: preProbes,
      envMaxAgeMin: 30,
      kind: 'wave01-capture-run',
      runId: 'run-1',
      evidenceMode: 'evidence',
      batchValid: true,
      baseReleaseSha256: sha(baseBytes),
      companionSha256: compSha,
      contractSha256: CONTRACT_SHA256,
      runner: { head: comp.runner.commit, suiteDigest: comp.runner.suiteDigest },
      captureIds: ['cap-1'],
      startedAt: '2026-10-07T15:40:00Z',
      endedAt: '2026-10-07T15:50:00Z',
      expectedRecords: ['W01-S01.P1.M0.capture.json'],
      env: [
        { gate: 'E-ENV-1', outcome: 'pass' },
        { gate: 'E-ENV-2', outcome: 'pass' },
        { gate: 'E-ENV-3', outcome: 'pass' },
        { gate: 'E-ENV-4', outcome: 'inconclusive', awaitingPost: true },
        { gate: 'E-ENV-5', outcome: 'pass' },
      ],
      envPostSelfAnon401: { status: 401, at: '2026-10-07T15:50:01Z' },
      envPostDevAuthProbes: PROBES_OFF,
      // O-a: the run.json copies are what the runner writes (one derivation).
      ...preComparisonInputs(preDecl),
      baseURL: 'https://baseline.example',
    };
    mutateRun?.(runObj);
    fs.writeFileSync(path.join(runDir, 'run.json'), JSON.stringify(runObj));
    let postFile: string | undefined;
    if (envPost !== 'omit') {
      postFile = path.join(dir, 'env-post.json');
      if (envPost) fs.writeFileSync(postFile, JSON.stringify(envPost));
    }
    return validateRun(runDir, path.join(dir, 'base.json'), path.join(dir, 'comp.json'), postFile);
  }
  it('a consistent run validates', () => {
    expect(writeRun()).toEqual([]);
  });
  it('validate-run independently rejects E-ENV failures and missing POST evidence (review1 B2/B3)', () => {
    expect(
      writeRun(undefined, (r) => (r.env[1] = { gate: 'E-ENV-2', outcome: 'fail' })).some((e) =>
        e.includes('E-ENV-2 FAIL')
      )
    ).toBe(true);
    expect(
      writeRun(undefined, (r) => (r.env[3] = { gate: 'E-ENV-4', outcome: 'inconclusive' })).some(
        (e) => e.startsWith('missing: E-ENV-4')
      )
    ).toBe(true);
    expect(
      writeRun(undefined, (r) => (r.envPostSelfAnon401 = { status: 200 })).some((e) =>
        e.includes('post-batch anonymous probe')
      )
    ).toBe(true);
    expect(
      writeRun(undefined, undefined, 'omit').some((e) =>
        e.includes('POST env declaration not supplied')
      )
    ).toBe(true);
    expect(writeRun(undefined, undefined, null).some((e) => e.includes('missing: POST env'))).toBe(
      true
    );
    expect(
      writeRun(undefined, undefined, {
        baseURL: 'https://baseline.example',
        window_start: '2026-10-07T15:00:00Z',
        window_end: '2026-10-07T16:00:00Z',
        serving_process_start_ts: '2026-10-07T15:30:00Z',
        slotGeneration: 'gen-1',
        e_env_1_dev_auth_effective: false,
        e_env_1_provenance: PROV,
        e_env_3_test_login_enabled: true,
        e_env_4_runtime_broker_effective: false,
        e_env_4_no_broker_process_or_dispatch: false,
      }).some((e) => e.startsWith('mismatch: POST env E-ENV-4'))
    ).toBe(true);
  });
  /** Re-seal the embedded raw PRE declaration after a mutation (sha kept consistent). */
  const reseal = (mut: (d: Record<string, any>) => void) => (r: Record<string, any>) => {
    const d = JSON.parse(r.envPreDeclarationRaw);
    mut(d);
    r.envPreDeclarationRaw = JSON.stringify(d);
    r.envDeclarationSha256 = sha(r.envPreDeclarationRaw);
    Object.assign(r, preComparisonInputs(d));
  };
  it('validate-run: PRE/POST baseURL must bind to the run and to each other', () => {
    expect(
      writeRun(
        undefined,
        reseal((d) => (d.baseURL = 'https://candidate.example'))
      ).some((e) => e.includes('not bound to Release baseURL'))
    ).toBe(true);
    expect(
      writeRun(
        undefined,
        reseal((d) => delete d.baseURL)
      ).some((e) => e.includes('missing: PRE declaration baseURL'))
    ).toBe(true);
  });
  it('O-a: PRE comparison inputs are derived from the embedded raw declaration; tampered run.json copies are mismatches', () => {
    for (const [k, v] of [
      ['envPreBaseURL', 'https://candidate.example'],
      ['envPreValues', { e_env_1_dev_auth_effective: true }],
      ['envPreProvenanceKey', '{}'],
      ['envPreSupportKey', '{}'],
      ['envPreProcessStartTs', '2026-10-07T15:31:00Z'],
    ] as const) {
      const errs = writeRun(undefined, (r) => (r[k] = v));
      expect(errs).toContain(
        `mismatch: run.${k} differs from the value derived from envPreDeclarationRaw`
      );
    }
    // With every run.json copy removed, the derived values still drive the
    // POST comparison: a POST env-value change is a FAIL (mismatch:).
    const strip = (r: Record<string, any>) => {
      for (const k of Object.keys(preComparisonInputs({}))) delete r[k];
    };
    expect(writeRun(undefined, strip)).toEqual([]);
    const post = JSON.parse(JSON.stringify(POST_OK));
    post.e_env_1_provenance.flags['--production'] = false;
    expect(
      writeRun(undefined, strip, post).some((e) =>
        e.startsWith('mismatch: POST env same-slot PRE/POST disagreement on e_env_1_provenance')
      )
    ).toBe(true);
  });
  it('O1: validate-run recomputes PRE gates from the embedded raw declaration (tampered run.env / bytes rejected)', () => {
    expect(
      writeRun(undefined, (r) => (r.env[0] = { gate: 'E-ENV-1', outcome: 'inconclusive' })).some(
        (e) => e.includes('recomputed E-ENV-1 pass != recorded inconclusive')
      )
    ).toBe(true);
    expect(
      writeRun(
        undefined,
        (r) => (r.envPreDeclarationRaw = r.envPreDeclarationRaw.replace('15:35:00Z', '15:34:00Z'))
      ).some((e) => e.includes('do not match envDeclarationSha256'))
    ).toBe(true);
    expect(
      writeRun(undefined, (r) => delete r.envPreDeclarationRaw).some((e) =>
        e.includes('envPreDeclarationRaw')
      )
    ).toBe(true);
  });
  // ── Ruling R-13: missing post-batch evidence ⇒ INCONCLUSIVE; contradictions ⇒ REJECTED.
  const classify = (errs: string[]) =>
    errs.length === 0
      ? 'VALID'
      : errs.some((e) => /mismatch:/.test(e))
        ? 'REJECTED'
        : 'INCONCLUSIVE';
  /** The runner's derived flag on every copy (run.json + captures), with one reason. */
  const derivedInvalid = (reason: string, mutRun?: (r: Record<string, any>) => void) =>
    [
      (c: Record<string, any>) =>
        Object.assign(c, { batchValid: false, batchInvalidReason: reason }),
      (r: Record<string, any>) => {
        Object.assign(r, { batchValid: false, batchInvalidReason: reason });
        mutRun?.(r);
      },
    ] as const;
  it('R-13 (a): post-batch P-API/P-WEB not obtained (network error) ⇒ INCONCLUSIVE, derived batchValid=false not a mismatch', () => {
    const errs = writeRun(
      ...derivedInvalid(
        'post-batch dev-auth probes P-API missing, P-WEB missing (E-ENV-1)',
        (r) => {
          r.envPostDevAuthProbes = { api: null, web: null };
        }
      )
    );
    expect(errs.some((e) => e.startsWith('missing: post-batch dev-auth probes'))).toBe(true);
    expect(classify(errs)).toBe('INCONCLUSIVE');
  });
  it('R-13 (b): post-batch P-API HTTP 500 (unexplained) ⇒ INCONCLUSIVE', () => {
    const errs = writeRun(
      ...derivedInvalid(
        'post-batch dev-auth probes P-API unexplained, P-WEB off (E-ENV-1)',
        (r) => {
          r.envPostDevAuthProbes = {
            ...PROBES_OFF,
            api: { status: 500, message: null, at: '2026-10-07T15:50:02Z' },
          };
        }
      )
    );
    expect(classify(errs)).toBe('INCONCLUSIVE');
  });
  it('R-13 (c): post-batch anonymous probe never returned (status 0 / fetch error) ⇒ INCONCLUSIVE, not "≠ 401"', () => {
    const errs = writeRun(
      ...derivedInvalid('post-batch anonymous probe status 0 (E-ENV-2)', (r) => {
        r.envPostSelfAnon401 = {
          status: 0,
          at: '2026-10-07T15:50:01Z',
          error: 'TypeError: fetch failed',
        };
      })
    );
    expect(errs.some((e) => e.startsWith('missing: post-batch anonymous probe'))).toBe(true);
    expect(classify(errs)).toBe('INCONCLUSIVE');
    // also: probe record absent entirely
    expect(
      classify(
        writeRun(
          ...derivedInvalid('post-batch anonymous probe status undefined (E-ENV-2)', (r) => {
            delete r.envPostSelfAnon401;
          })
        )
      )
    ).toBe('INCONCLUSIVE');
  });
  it('R-13: post-batch main.js not obtained ⇒ INCONCLUSIVE; obtained but different ⇒ REJECTED', () => {
    expect(
      classify(
        writeRun(
          (c) => {
            c.mainJs.post = { status: 0, sha256: null, at: 'x', error: 'fetch failed' };
            Object.assign(c, { batchValid: false, batchInvalidReason: 'r' });
          },
          (r) => Object.assign(r, { batchValid: false, batchInvalidReason: 'r' })
        )
      )
    ).toBe('INCONCLUSIVE');
    expect(
      classify(
        writeRun(
          (c) => {
            c.mainJs.post = { status: 200, sha256: 'f'.repeat(64), at: 'x' };
            Object.assign(c, { batchValid: false, batchInvalidReason: 'r' });
          },
          (r) => Object.assign(r, { batchValid: false, batchInvalidReason: 'r' })
        )
      )
    ).toBe('REJECTED');
  });
  it('R-13 control: post-batch P-API ON ⇒ REJECTED', () => {
    const errs = writeRun(
      ...derivedInvalid('post-batch dev-auth probes P-API on, P-WEB off (E-ENV-1 FAIL)', (r) => {
        r.envPostDevAuthProbes = {
          ...PROBES_OFF,
          api: { ...PROBES_OFF.api, message: 'invalid development token' },
        };
      })
    );
    expect(errs.some((e) => e.startsWith('mismatch: post-batch dev-auth probe shows ON'))).toBe(
      true
    );
    expect(classify(errs)).toBe('REJECTED');
  });
  it('R-13 control: an observed non-401 anonymous status (302 redirect) ⇒ REJECTED', () => {
    expect(
      classify(
        writeRun(
          ...derivedInvalid('post-batch anonymous probe status 302 (E-ENV-2)', (r) => {
            r.envPostSelfAnon401 = { status: 302, at: '2026-10-07T15:50:01Z' };
          })
        )
      )
    ).toBe('REJECTED');
  });
  it('R-13 tamper controls: flag-copy inconsistency or a false flag with no established cause ⇒ REJECTED', () => {
    // capture says false, run.json says true
    expect(
      writeRun((c) => Object.assign(c, { batchValid: false, batchInvalidReason: 'x' })).some((e) =>
        e.includes('mismatch: batchValid differs from run.json')
      )
    ).toBe(true);
    // every copy false but nothing independently explains it
    const errs = writeRun(...derivedInvalid('unexplained'));
    expect(errs).toContain('mismatch: batchValid false without an independently established cause');
    expect(classify(errs)).toBe('REJECTED');
    // malformed flag
    expect(
      writeRun(undefined, (r) => (r.batchValid = 'yes')).some((e) =>
        e.includes('batchValid not boolean')
      )
    ).toBe(true);
  });
  it('an aborted run never validates (review1 B4)', () => {
    expect(
      writeRun(undefined, (r) =>
        Object.assign(r, { aborted: true, abortReason: 'readback exception' })
      ).some((e) => e.includes('run aborted'))
    ).toBe(true);
  });
  it('completeness: a missing expected record or M0-primary screenshot is reported (review1 N5)', () => {
    expect(
      writeRun(undefined, (r) => r.expectedRecords.push('W01-S01.P2.M0.capture.json')).some((e) =>
        e.includes('missing: expected record W01-S01.P2.M0')
      )
    ).toBe(true);
    expect(
      writeRun((c) => (c.files[0].kind = 'other')).some((e) =>
        e.includes('M0-primary full-page screenshot')
      )
    ).toBe(true);
  });
  it('rejects a capture bound to another companion, a changed file or a wrong loaded main entry', () => {
    expect(
      writeRun((c) => (c.companionSha256 = '0'.repeat(64))).some((e) =>
        e.includes('companionSha256')
      )
    ).toBe(true);
    expect(
      writeRun((c) => (c.files[0].sha256 = '0'.repeat(64))).some((e) =>
        e.includes('file shot.png sha256')
      )
    ).toBe(true);
    expect(
      writeRun((c) => (c.loadedMainEntry.sha256 = '0'.repeat(64))).some((e) =>
        e.includes('loaded main entry')
      )
    ).toBe(true);
  });
  it('capture errors never carry graded outcomes silently; pending outcomes are invalid in final records', () => {
    expect(
      validateCapture({ status: 'capture-error' }).includes('non-complete record needs errorReason')
    ).toBe(true);
    expect(
      writeRun((c) => (c.outcomes = [{ clause: 'B-A2', outcome: 'pending', policyIds: [] }])).some(
        (e) => e.includes('unresolved pending')
      )
    ).toBe(true);
  });
});

describe('O5: validation record binds the validator code identity', () => {
  it('carries the validator runner commit and suite digest', () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'wave01-val-'));
    fs.writeFileSync(path.join(dir, 'run.json'), '{}');
    const rec = validationRecord(
      dir,
      '/nonexistent/base.json',
      '/nonexistent/comp.json',
      undefined,
      ['missing: x']
    ) as {
      validator: { head: string; suiteDigest: string };
      classification: string;
    };
    expect(rec.validator.head).toMatch(/^[0-9a-f]{40}$/);
    expect(rec.validator.suiteDigest).toMatch(/^[0-9a-f]{64}$/);
    expect(rec.classification).toBe('INCONCLUSIVE');
  });
});
