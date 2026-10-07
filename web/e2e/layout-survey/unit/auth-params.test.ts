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
 * Regression coverage for the optional sessionSecret/storageDir parameters
 * added to web/e2e/harness/auth.ts: defaults must be byte-for-byte the old
 * behaviour (well-known secret, shared temp dir), explicit values must be used.
 */

import { createHash, createHmac } from 'node:crypto';
import * as fs from 'node:fs';
import * as http from 'node:http';
import type { AddressInfo } from 'node:net';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import {
  createSession,
  DEFAULT_AUTH_STORAGE_DIR,
  generateTestLoginToken,
} from '../../harness/auth.js';
import { E2E_SESSION_SECRET } from '../../harness/hub.js';

const derive = (secret: string) =>
  createHash('sha256').update(`scion-hub-signing-key:user_signing_key:${secret}`).digest();

function verifies(token: string, secret: string): boolean {
  const [h, p, s] = token.split('.');
  const expected = createHmac('sha256', derive(secret)).update(`${h}.${p}`).digest('base64url');
  return expected === s;
}

describe('generateTestLoginToken', () => {
  it('defaults to the well-known E2E secret', () => {
    const t = generateTestLoginToken();
    expect(verifies(t, E2E_SESSION_SECRET)).toBe(true);
    const payload = JSON.parse(Buffer.from(t.split('.')[1]!, 'base64url').toString());
    expect(payload.aud).toBe('scion-test-login');
    expect(payload.sub).toBe('e2e-harness');
    expect(payload.exp - payload.iat).toBe(300);
  });

  it('signs with an explicit secret and not the default', () => {
    const t = generateTestLoginToken('e2e-harness', 'slot-secret-0123456789abcdef');
    expect(verifies(t, 'slot-secret-0123456789abcdef')).toBe(true);
    expect(verifies(t, E2E_SESSION_SECRET)).toBe(false);
  });

  it('rejects an empty explicit secret', () => {
    expect(() => generateTestLoginToken('e2e-harness', '')).toThrow(/non-empty/);
  });
});

describe('createSession', () => {
  let server: http.Server;
  let baseURL: string;
  const seenAuth: string[] = [];

  beforeAll(async () => {
    server = http.createServer((req, res) => {
      seenAuth.push(String(req.headers.authorization ?? ''));
      let body = '';
      req.on('data', (c) => (body += c));
      req.on('end', () => {
        const { email, role } = JSON.parse(body);
        res.setHeader('Set-Cookie', [
          'scion_sess=cookieval123; Path=/; HttpOnly; SameSite=Lax; Max-Age=600',
        ]);
        res.setHeader('Content-Type', 'application/json');
        res.end(
          JSON.stringify({
            user: { id: 'u1', email, displayName: email, role },
            accessToken: 'a.b.c',
            refreshToken: 'r.s.t',
          })
        );
      });
    });
    await new Promise<void>((r) => server.listen(0, '127.0.0.1', () => r()));
    baseURL = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  });

  afterAll(() => new Promise<void>((r) => server.close(() => r())));

  it('default: well-known secret and the shared harness storage dir', async () => {
    const s = await createSession(baseURL, { email: 'default@e2e.test', role: 'viewer' });
    expect(path.dirname(s.storageStatePath)).toBe(DEFAULT_AUTH_STORAGE_DIR);
    expect(DEFAULT_AUTH_STORAGE_DIR).toBe(path.join(os.tmpdir(), 'scion-e2e-auth'));
    const bearer = seenAuth.at(-1)!.replace(/^Bearer /, '');
    expect(verifies(bearer, E2E_SESSION_SECRET)).toBe(true);
    fs.rmSync(s.storageStatePath, { force: true });
  });

  it('explicit: slot secret and private storage dir (0700 dir, 0600 file)', async () => {
    const dir = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'ls-auth-')), 'private');
    const s = await createSession(
      baseURL,
      { email: 'explicit@survey.test', role: 'admin' },
      { sessionSecret: 'slot-secret-0123456789abcdef', storageDir: dir }
    );
    expect(path.dirname(s.storageStatePath)).toBe(dir);
    expect(fs.statSync(dir).mode & 0o777).toBe(0o700);
    expect(fs.statSync(s.storageStatePath).mode & 0o777).toBe(0o600);
    const bearer = seenAuth.at(-1)!.replace(/^Bearer /, '');
    expect(verifies(bearer, 'slot-secret-0123456789abcdef')).toBe(true);
    expect(verifies(bearer, E2E_SESSION_SECRET)).toBe(false);
    const state = JSON.parse(fs.readFileSync(s.storageStatePath, 'utf-8'));
    expect(state.cookies[0].name).toBe('scion_sess');
    expect(s.accessToken).toBe('a.b.c');
    fs.rmSync(path.dirname(dir), { recursive: true, force: true });
  });

  it('re-tightens a pre-existing storage file to 0600 (mode only applies on create)', async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ls-auth-'));
    const pre = path.join(dir, 'reuse_survey_test.json');
    fs.writeFileSync(pre, '{}', { mode: 0o644 });
    fs.chmodSync(pre, 0o644);
    const s = await createSession(
      baseURL,
      { email: 'reuse@survey.test', role: 'viewer' },
      { storageDir: dir }
    );
    expect(s.storageStatePath).toBe(pre);
    expect(fs.statSync(pre).mode & 0o777).toBe(0o600);
    fs.rmSync(dir, { recursive: true, force: true });
  });
});
