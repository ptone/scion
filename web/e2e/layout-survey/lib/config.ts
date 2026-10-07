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
 * Layout-survey run configuration.
 *
 * Everything comes from environment variables naming *values* or *paths*;
 * secret material is only ever read from a mode-0600 file whose path is given
 * in LAYOUT_SURVEY_SESSION_SECRET_FILE. Nothing here starts, builds, seeds or
 * stops a Hub: the survey attaches to a steward-provided endpoint.
 */

import * as fs from 'node:fs';
import * as path from 'node:path';

export interface SurveyConfig {
  /** Host-scoped origin of the Hub slot under test, e.g. https://baseline.example. */
  baseURL: string;
  /** Path of a mode-0600 file containing the slot's session secret. */
  sessionSecretFile: string;
  /** Private (mode 0700) directory for credentials/storage state; never evidence. */
  privateDir: string;
  /** Evidence output directory for this run (created; must not already contain a run). */
  evidenceDir: string;
  /** Fixture map written by the steward seed command. */
  fixtureMapFile: string;
  /** Steward-published Release record (JSON) the capture is attributed to. */
  releaseFile: string;
  /** Identity of whoever executes this command (capturer or steward). */
  operatorIdentity: string;
  /** Synthetic admin principal used for the bounded /admin/groups exception. */
  adminEmail: string;
  /** Short tag namespacing fixture slugs for this slot generation. */
  fixtureTag: string;
}

const TAG_RE = /^[a-z0-9][a-z0-9-]{0,23}$/;
const EMAIL_RE = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;

/** Read a required, non-empty env var. Empty strings are rejected. */
export function requireEnv(name: string): string {
  const v = process.env[name];
  if (v === undefined || v.trim() === '') {
    throw new Error(`layout-survey: required environment variable ${name} is missing or empty`);
  }
  return v.trim();
}

/** Validate a base URL: http(s), no path/query/credentials. */
export function validateBaseURL(raw: string): string {
  const u = new URL(raw);
  if (u.protocol !== 'https:' && u.protocol !== 'http:') {
    throw new Error(`layout-survey: base URL must be http(s), got ${u.protocol}`);
  }
  if (u.username || u.password) {
    throw new Error('layout-survey: base URL must not embed credentials');
  }
  if ((u.pathname && u.pathname !== '/') || u.search || u.hash) {
    throw new Error('layout-survey: base URL must be an origin (no path/query/fragment)');
  }
  return u.origin;
}

/**
 * Ensure a file holds secret material safely: regular file, owned by us,
 * not readable/writable by group or others. Returns its trimmed contents.
 */
export function readSecretFile(file: string): string {
  const st = fs.statSync(file);
  if (!st.isFile()) throw new Error(`layout-survey: secret path is not a regular file`);
  if ((st.mode & 0o077) !== 0) {
    throw new Error('layout-survey: secret file must be mode 0600 (no group/other access)');
  }
  const value = fs.readFileSync(file, 'utf-8').trim();
  if (value.length < 16) throw new Error('layout-survey: secret file is empty or too short');
  return value;
}

/** Create (or verify) a private 0700 directory. */
export function ensurePrivateDir(dir: string): string {
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  const st = fs.statSync(dir);
  if ((st.mode & 0o077) !== 0) {
    throw new Error(`layout-survey: private dir ${dir} must be mode 0700`);
  }
  return dir;
}

export function validateFixtureTag(tag: string): string {
  if (!TAG_RE.test(tag)) {
    throw new Error(
      'layout-survey: LAYOUT_SURVEY_FIXTURE_TAG must match ^[a-z0-9][a-z0-9-]{0,23}$'
    );
  }
  return tag;
}

function loadCommon() {
  const adminEmail = requireEnv('LAYOUT_SURVEY_ADMIN_EMAIL');
  if (!EMAIL_RE.test(adminEmail))
    throw new Error('layout-survey: LAYOUT_SURVEY_ADMIN_EMAIL is not an email');
  return {
    baseURL: validateBaseURL(requireEnv('LAYOUT_SURVEY_BASE_URL')),
    sessionSecretFile: requireEnv('LAYOUT_SURVEY_SESSION_SECRET_FILE'),
    privateDir: path.resolve(requireEnv('LAYOUT_SURVEY_PRIVATE_DIR')),
    operatorIdentity: requireEnv('LAYOUT_SURVEY_OPERATOR'),
    adminEmail,
    fixtureMapFile: path.resolve(requireEnv('LAYOUT_SURVEY_FIXTURE_MAP')),
  };
}

/** Config for the steward seed command. */
export function loadSeedConfig(): Omit<SurveyConfig, 'evidenceDir' | 'releaseFile'> {
  return {
    ...loadCommon(),
    fixtureTag: validateFixtureTag(requireEnv('LAYOUT_SURVEY_FIXTURE_TAG')),
  };
}

/** Config for the attach-only capture run. */
export function loadCaptureConfig(): Omit<SurveyConfig, 'fixtureTag'> {
  return {
    ...loadCommon(),
    evidenceDir: path.resolve(requireEnv('LAYOUT_SURVEY_EVIDENCE_DIR')),
    releaseFile: path.resolve(requireEnv('LAYOUT_SURVEY_RELEASE_FILE')),
  };
}

/** Guard: evidence must never live inside the private credential directory or vice versa. */
export function assertDisjoint(a: string, b: string): void {
  const ra = path.resolve(a) + path.sep;
  const rb = path.resolve(b) + path.sep;
  if (ra.startsWith(rb) || rb.startsWith(ra)) {
    throw new Error('layout-survey: evidence and private directories must be disjoint');
  }
}
