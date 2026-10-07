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
 * Bounded admin-exception session for the /admin/groups scenario.
 *
 * Reuses the real harness test-login contract (web/e2e/harness/auth.ts) with
 * a slot-specific secret read from a 0600 file and a private storage dir.
 * Credential values are written ONLY to the private dir (for the steward's
 * teardown probes); evidence receives a value-free issuance inventory.
 */

import * as fs from 'node:fs';
import * as path from 'node:path';
import { createSession, type AuthSession } from '../../harness/auth.js';
import { ensurePrivateDir, readSecretFile } from './config.js';

export interface IssuedCredential {
  type: 'test-login-challenge' | 'access-token' | 'refresh-token' | 'cookie-session';
  /** Cookie name for cookie sessions; never a value. */
  name?: string;
  issuedAt: string | null;
  expiresAt: string | null;
}

export interface IssuanceInventory {
  principal: { id: string; email: string; role: string };
  purpose: string;
  issuedBy: string;
  /** Whether any token refresh was performed by this tool (it never refreshes). */
  refreshPerformed: false;
  note: string;
  credentials: IssuedCredential[];
}

export interface SurveySession {
  session: AuthSession;
  inventory: IssuanceInventory;
}

/** Decode a JWT's iat/exp claims without verifying it. Returns nulls if not a JWT. */
export function jwtTimes(token: string): { issuedAt: string | null; expiresAt: string | null } {
  const parts = token.split('.');
  if (parts.length !== 3) return { issuedAt: null, expiresAt: null };
  try {
    const payload = JSON.parse(Buffer.from(parts[1]!, 'base64url').toString('utf-8')) as {
      iat?: number;
      exp?: number;
    };
    const iso = (n?: number) => (typeof n === 'number' ? new Date(n * 1000).toISOString() : null);
    return { issuedAt: iso(payload.iat), expiresAt: iso(payload.exp) };
  } catch {
    return { issuedAt: null, expiresAt: null };
  }
}

/**
 * Open the bounded admin session. `purpose` is recorded in the inventory
 * (e.g. "steward-seed", "capture").
 */
export async function openAdminSession(opts: {
  baseURL: string;
  sessionSecretFile: string;
  privateDir: string;
  adminEmail: string;
  operatorIdentity: string;
  purpose: string;
}): Promise<SurveySession> {
  const secret = readSecretFile(opts.sessionSecretFile);
  const privateDir = ensurePrivateDir(opts.privateDir);
  const storageDir = ensurePrivateDir(path.join(privateDir, `storage-${opts.purpose}`));
  const challengeIssued = new Date();
  const session = await createSession(
    opts.baseURL,
    { email: opts.adminEmail, role: 'admin', displayName: 'Layout Survey Admin (synthetic)' },
    { sessionSecret: secret, storageDir }
  );

  const state = JSON.parse(fs.readFileSync(session.storageStatePath, 'utf-8')) as {
    cookies: Array<{ name: string; expires: number }>;
  };
  const inventory: IssuanceInventory = {
    principal: { id: session.user.id, email: session.user.email, role: session.user.role },
    purpose: opts.purpose,
    issuedBy: opts.operatorIdentity,
    refreshPerformed: false,
    note: 'No refresh is performed by the layout-survey tools. Cookie issuedAt is approximated by the test-login request time; challenge expiry is the harness 300 s lifetime.',
    credentials: [
      {
        type: 'test-login-challenge',
        issuedAt: challengeIssued.toISOString(),
        // harness generateTestLoginToken uses a 300 s lifetime
        expiresAt: new Date(challengeIssued.getTime() + 300_000).toISOString(),
      },
      { type: 'access-token', ...jwtTimes(session.accessToken) },
      { type: 'refresh-token', ...jwtTimes(session.refreshToken) },
      ...state.cookies.map((c) => ({
        type: 'cookie-session' as const,
        name: c.name,
        issuedAt: challengeIssued.toISOString(),
        expiresAt: c.expires > 0 ? new Date(c.expires * 1000).toISOString() : null,
      })),
    ],
  };

  // Private, value-bearing record for the steward's teardown probes / scan.
  const credFile = path.join(privateDir, `credentials-${opts.purpose}.json`);
  fs.writeFileSync(
    credFile,
    JSON.stringify(
      {
        principalId: session.user.id,
        accessToken: session.accessToken,
        refreshToken: session.refreshToken,
        storageStatePath: session.storageStatePath,
        inventory,
      },
      null,
      2
    ),
    { mode: 0o600, flag: 'wx' } // never overwrite an earlier issuance record
  );
  // File-mode contract: every value-bearing file in the private dir is 0600.
  for (const f of [credFile, session.storageStatePath]) {
    fs.chmodSync(f, 0o600);
    if ((fs.statSync(f).mode & 0o077) !== 0) throw new Error(`credential file ${f} is not 0600`);
  }
  return { session, inventory };
}
