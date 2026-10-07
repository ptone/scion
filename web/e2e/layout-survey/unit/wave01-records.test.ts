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
  envStop,
  evaluateEnv,
  isQuarantined,
  validateCapture,
  validateRun,
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
  it('pins contract rev 2', () => {
    expect(CONTRACT.sha256).toBe(
      '1877b1d40a5e4bf04d87e47d419a4451cac5ccb2d3f27f9a63d8ce32c88a5447'
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
    contract: { name: 'wave01-contract-FROZEN-rev2.md', sha256: CONTRACT_SHA256 },
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
  it('baseline/candidate pairs must differ by host, not port alone', () => {
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

describe('E-ENV gates', () => {
  const decl = {
    window_ts: '2026-10-07T15:00:00Z',
    slotGeneration: 'gen-1',
    e_env_1_dev_auth_effective: false,
    e_env_3_test_login_enabled: true,
    e_env_4_runtime_broker_effective: false,
    e_env_2_anon_401: { status: 401, ts: 'x' },
    e_env_5: { probe: 'repo-default-secret-challenge', result_status: 401, ts: 'x' },
  };
  it('all present and consistent ⇒ pass', () => {
    expect(
      evaluateEnv(decl, { status: 401, at: 'x' }, { slotGeneration: 'gen-1' }).every(
        (g) => g.outcome === 'pass'
      )
    ).toBe(true);
  });
  it('accepted repo-default secret ⇒ E-ENV-5 FAIL and batch stop', () => {
    const r = evaluateEnv(
      { ...decl, e_env_5: { ...decl.e_env_5, result_status: 200 } },
      { status: 401, at: 'x' },
      { slotGeneration: 'gen-1' }
    );
    expect(r.find((g) => g.gate === 'E-ENV-5')?.outcome).toBe('fail');
    expect(envStop(r)).toBe(true);
  });
  it('a redirect is not a 401', () => {
    expect(
      evaluateEnv(decl, { status: 302, at: 'x' }, { slotGeneration: 'gen-1' }).find(
        (g) => g.gate === 'E-ENV-2'
      )?.outcome
    ).toBe('fail');
  });
  it('missing declaration or other slot generation ⇒ inconclusive', () => {
    expect(
      evaluateEnv(null, { status: 401, at: 'x' }, { slotGeneration: 'gen-1' }).filter(
        (g) => g.outcome === 'inconclusive'
      )
    ).toHaveLength(4);
    expect(
      evaluateEnv(decl, { status: 401, at: 'x' }, { slotGeneration: 'gen-9' }).find(
        (g) => g.gate === 'E-ENV-1'
      )?.outcome
    ).toBe('inconclusive');
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
  function writeRun(mutate?: (c: Record<string, any>) => void) {
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
      files: [{ kind: 'shot', path: 'shot.png', sha256: sha('png-bytes') }],
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
    fs.writeFileSync(
      path.join(runDir, 'run.json'),
      JSON.stringify({
        kind: 'wave01-capture-run',
        runId: 'run-1',
        evidenceMode: 'evidence',
        batchValid: true,
        baseReleaseSha256: sha(baseBytes),
        companionSha256: compSha,
        contractSha256: CONTRACT_SHA256,
        runner: { head: comp.runner.commit, suiteDigest: comp.runner.suiteDigest },
        captureIds: ['cap-1'],
      })
    );
    return validateRun(runDir, path.join(dir, 'base.json'), path.join(dir, 'comp.json'));
  }
  it('a consistent run validates', () => {
    expect(writeRun()).toEqual([]);
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
