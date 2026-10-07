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
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  assertDisjoint,
  readSecretFile,
  requireEnv,
  validateBaseURL,
  validateFixtureTag,
} from '../lib/config.js';
import { sha256, treeDigest } from '../lib/digest.mjs';
import {
  assertBadgesVisible,
  assertContainersFit,
  assertNamesVisible,
  assertNoDocumentOverflow,
  assertTableFits,
  type DocumentMetrics,
  type RowMetrics,
} from '../lib/geometry.js';
import { jwtTimes } from '../lib/session.js';
import { collectSecretValues, scanDir } from '../scripts/bundle.mjs';
import { parseServerLaunch, validateRecord } from '../scripts/records.mjs';

let tmp: string;
beforeEach(() => {
  tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'ls-unit-'));
});
afterEach(() => fs.rmSync(tmp, { recursive: true, force: true }));

describe('config validation', () => {
  it('rejects empty required env values', () => {
    process.env.LS_UNIT_EMPTY = '  ';
    expect(() => requireEnv('LS_UNIT_EMPTY')).toThrow(/missing or empty/);
    delete process.env.LS_UNIT_EMPTY;
    expect(() => requireEnv('LS_UNIT_EMPTY')).toThrow(/missing or empty/);
  });

  it('accepts origins only', () => {
    expect(validateBaseURL('https://baseline.example/')).toBe('https://baseline.example');
    expect(() => validateBaseURL('https://u:p@x.example')).toThrow(/credentials/);
    expect(() => validateBaseURL('https://x.example/scion')).toThrow(/origin/);
    expect(() => validateBaseURL('ftp://x.example')).toThrow(/http/);
  });

  it('requires 0600 secret files with content', () => {
    const f = path.join(tmp, 's');
    fs.writeFileSync(f, 'x'.repeat(32), { mode: 0o644 });
    fs.chmodSync(f, 0o644);
    expect(() => readSecretFile(f)).toThrow(/0600/);
    fs.chmodSync(f, 0o600);
    expect(readSecretFile(f)).toBe('x'.repeat(32));
    fs.writeFileSync(f, '\n');
    expect(() => readSecretFile(f)).toThrow(/empty/);
  });

  it('validates fixture tags and disjoint dirs', () => {
    expect(validateFixtureTag('gen1')).toBe('gen1');
    expect(() => validateFixtureTag('')).toThrow();
    expect(() => validateFixtureTag('Bad Tag')).toThrow();
    expect(() => assertDisjoint('/a/b', '/a/b/c')).toThrow(/disjoint/);
    expect(() => assertDisjoint('/a/b', '/a/bc')).not.toThrow();
  });
});

describe('digest', () => {
  it('tree digest matches the documented sha256sum pipeline', () => {
    fs.mkdirSync(path.join(tmp, 'd/sub'), { recursive: true });
    fs.writeFileSync(path.join(tmp, 'd/b.txt'), 'b');
    fs.writeFileSync(path.join(tmp, 'd/sub/a.txt'), 'a');
    const shell = execFileSync(
      'sh',
      [
        '-c',
        'cd "$1" && find . -type f | sed \'s|^\\./||\' | LC_ALL=C sort | xargs sha256sum | sha256sum | cut -c1-64',
        'sh',
        path.join(tmp, 'd'),
      ],
      { encoding: 'utf-8' }
    ).trim();
    expect(treeDigest(path.join(tmp, 'd'))).toBe(shell);
  });

  it('rejects symlinks in digested trees', () => {
    fs.mkdirSync(path.join(tmp, 'd'));
    fs.symlinkSync('/etc/hostname', path.join(tmp, 'd/link'));
    expect(() => treeDigest(path.join(tmp, 'd'))).toThrow(/symlink/);
  });
});

describe('geometry assertions', () => {
  const doc = (scrollWidth: number): DocumentMetrics => ({
    innerWidth: 390,
    innerHeight: 844,
    devicePixelRatio: 1,
    docScrollWidth: scrollWidth,
    docClientWidth: 390,
    bodyScrollWidth: scrollWidth,
    overflowingScrollContainers: [],
    clippedOverflow: [],
    offViewportElements: [],
  });
  const box = (x: number, right: number) => ({
    x,
    y: 0,
    width: right - x,
    height: 20,
    right,
    bottom: 20,
  });
  const row = (key: string, badgeX: number, badgeRight: number, hit = true): RowMetrics => ({
    key,
    found: true,
    link: box(70, 300),
    linkTruncated: false,
    linkText: key,
    linkTitle: null,
    linkAriaLabel: null,
    linkHitTestOk: true,
    badge: box(badgeX, badgeRight),
    badgeHitTestOk: hit,
    clipContainer: box(16, 374),
    row: box(16, 374),
  });

  it('LS-A1 uses a 1px tolerance', () => {
    expect(assertNoDocumentOverflow(doc(391)).outcome).toBe('pass');
    expect(assertNoDocumentOverflow(doc(392)).outcome).toBe('fail');
  });

  it('LS-A2 fails a badge clipped by its container even when the document does not overflow', () => {
    expect(assertBadgesVisible([row('short', 300, 350)], 390).outcome).toBe('pass');
    const clipped = assertBadgesVisible(
      [row('short', 300, 350), row('long', 752, 809, false)],
      390
    );
    expect(clipped.outcome).toBe('fail');
    expect(clipped.details).toEqual({ failingRows: ['long'] });
    expect(assertBadgesVisible([row('occluded', 300, 350, false)], 390).outcome).toBe('fail');
    expect(assertBadgesVisible([], 390).outcome).toBe('fail');
  });
});

describe('container-fit and full-name assertions (B-C1 / LS-A4 / B-A3)', () => {
  const box = (x: number, right: number) => ({
    x,
    y: 0,
    width: right - x,
    height: 20,
    right,
    bottom: 20,
  });
  const base: RowMetrics = {
    key: 'long',
    found: true,
    link: box(70, 300),
    linkTruncated: false,
    linkText: 'Long Name',
    linkTitle: null,
    linkAriaLabel: null,
    linkHitTestOk: true,
    badge: box(310, 360),
    badgeHitTestOk: true,
    clipContainer: box(16, 374),
    row: box(16, 374),
  };
  it('LS-C1 compares table.scrollWidth with the clip container clientWidth (±1px)', () => {
    const fit = (t: number, c: number) => ({
      found: true,
      tableScrollWidth: t,
      containerClientWidth: c,
      containerPath: 'div.table-container',
    });
    expect(assertTableFits(fit(357, 356)).outcome).toBe('pass');
    expect(assertTableFits(fit(868, 356)).outcome).toBe('fail');
    expect(
      assertTableFits({
        found: false,
        tableScrollWidth: null,
        containerClientWidth: null,
        containerPath: null,
      }).outcome
    ).toBe('fail');
  });
  it('LS-A4 fails app-shell scroll overflow (FI-1 shape) and exempts only named sr-only clips', () => {
    const doc = (extra: Partial<DocumentMetrics>): DocumentMetrics => ({
      innerWidth: 390,
      innerHeight: 844,
      devicePixelRatio: 1,
      docScrollWidth: 390,
      docClientWidth: 390,
      bodyScrollWidth: 390,
      overflowingScrollContainers: [],
      clippedOverflow: [],
      offViewportElements: [],
      ...extra,
    });
    const srOnly = { path: 'table > caption.sr-only', scrollWidth: 585, clientWidth: 1 };
    expect(assertContainersFit(doc({ clippedOverflow: [srOnly] })).outcome).toBe('pass');
    const fi1 = doc({
      overflowingScrollContainers: [
        { path: 'scion-app > div.content', scrollWidth: 1626, clientWidth: 390, overflowX: 'auto' },
      ],
    });
    expect(assertNoDocumentOverflow(fi1).outcome).toBe('pass'); // the D1 blind spot
    expect(assertContainersFit(fi1).outcome).toBe('fail');
    const clip = doc({
      clippedOverflow: [
        { path: 'sl-tab-panel > div.table-container', scrollWidth: 868, clientWidth: 356 },
      ],
    });
    expect(assertContainersFit(clip).outcome).toBe('fail');
  });
  it('LS-A3 accepts shape (i) full text and shape (ii) ellipsis + exact title/accessible name only', () => {
    const names = { long: 'Long Name' };
    expect(assertNamesVisible([base], 390, names, {}).outcome).toBe('pass');
    const cut = { ...base, linkTruncated: true };
    expect(assertNamesVisible([cut], 390, names, {}).outcome).toBe('fail');
    expect(assertNamesVisible([{ ...cut, linkTitle: 'Long Name' }], 390, names, {}).outcome).toBe(
      'pass'
    );
    expect(assertNamesVisible([cut], 390, names, { long: 'Long Name' }).outcome).toBe('pass');
    expect(assertNamesVisible([{ ...cut, linkTitle: 'Long' }], 390, names, {}).outcome).toBe(
      'fail'
    );
    expect(assertNamesVisible([{ ...base, link: box(70, 703) }], 390, names, {}).outcome).toBe(
      'fail'
    );
    expect(assertNamesVisible([{ ...base, linkHitTestOk: false }], 390, names, {}).outcome).toBe(
      'fail'
    );
    expect(assertNamesVisible([], 390, names, {}).outcome).toBe('fail');
  });
});

describe('server launch summary (D3)', () => {
  it('derives hosted/broker/test-login/dev-auth from non-secret flags', () => {
    expect(
      parseServerLaunch(
        '--hosted --enable-hub --enable-web --enable-runtime-broker=false --enable-test-login'
      )
    ).toMatchObject({ hosted: true, runtimeBroker: false, testLogin: true, devAuth: false });
    expect(parseServerLaunch('--enable-runtime-broker --dev-auth')).toMatchObject({
      hosted: false,
      runtimeBroker: true,
      devAuth: true,
    });
    expect(() => parseServerLaunch('--hosted --session-secret abc')).toThrow(/secret/);
  });
});

describe('session inventory', () => {
  it('extracts iat/exp from a JWT without its signature', () => {
    const p = Buffer.from(JSON.stringify({ iat: 1_800_000_000, exp: 1_800_000_900 })).toString(
      'base64url'
    );
    expect(jwtTimes(`h.${p}.s`)).toEqual({
      issuedAt: '2027-01-15T08:00:00.000Z',
      expiresAt: '2027-01-15T08:15:00.000Z',
    });
    expect(jwtTimes('opaque')).toEqual({ issuedAt: null, expiresAt: null });
  });
});

describe('credential scan', () => {
  it('finds exact issued values and JWT patterns, reporting no values', () => {
    const priv = path.join(tmp, 'private');
    const ev = path.join(tmp, 'evidence');
    fs.mkdirSync(priv);
    fs.mkdirSync(ev);
    const jwt = 'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1MSJ9.c2lnbmF0dXJlLXNpZw';
    fs.writeFileSync(
      path.join(priv, 'credentials-x.json'),
      JSON.stringify({ accessToken: jwt, refreshToken: 'refresh-opaque-value-123' })
    );
    fs.writeFileSync(path.join(ev, 'clean.json'), '{"ok":true}');
    const secrets = collectSecretValues(priv, undefined);
    expect(scanDir(ev, secrets).matches).toEqual([]);
    fs.writeFileSync(path.join(ev, 'leak.txt'), `token refresh-opaque-value-123 and ${jwt}`);
    const r = scanDir(ev, secrets);
    expect(r.matches.map((m: { kind: string }) => m.kind).sort()).toEqual([
      'exact-issued-value',
      'pattern:jwt',
    ]);
    expect(JSON.stringify(r)).not.toContain('refresh-opaque-value-123');
  });
});

describe('record validation', () => {
  const base = { schemaVersion: 1, id: 'x', createdAt: '2026-10-07T10:00:00.000Z', producer: 'p' };
  it('rejects empty and malformed release fields', () => {
    const errs: string[] = validateRecord({
      ...base,
      kind: 'release',
      sourceSha: '4a25348',
      mainJsSha256: 'abc',
      includedCommits: [],
    });
    expect(errs.join('\n')).toMatch(/sourceSha must be a full 40-hex/);
    expect(errs.join('\n')).toMatch(/mainJsSha256 is not 64-hex/);
    expect(errs.join('\n')).toMatch(/missing\/empty release.includedCommits/);
  });
  it('enforces identity separation on verification records', () => {
    const errs: string[] = validateRecord({
      ...base,
      kind: 'verification',
      findingId: 'f',
      acceptanceSha256: sha256('a'),
      baselineCaptureIds: ['b'],
      candidateCaptureIds: ['c'],
      verificationReleaseId: 'r',
      verificationSourceSha: 's',
      identities: { author: 'dev', capturer: 'dev', reviewer: 'rev', verifier: 'uat' },
      assertionsRun: ['LS-A2'],
      evidence: ['e'],
      verdict: 'pass',
      rationale: 'r',
      remainingIssues: 'none',
      verifierIdentity: 'uat',
    });
    expect(errs.join('\n')).toMatch(/author must differ/);
  });
});
