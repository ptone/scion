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
 * Wave01 runner configuration (attach-only). Values and paths only; the
 * session secret is read in-process from a 0600 file by lib/session.ts.
 *
 * Evidence mode (default) requires every provenance input. Setting
 * LAYOUT_SURVEY_DEBUG_NON_EVIDENCE=1 allows missing Release/env/review
 * inputs for local debugging; every record is then stamped
 * `debug-non-evidence` and validate-run rejects the run.
 */

import * as path from 'node:path';
import { requireEnv, validateBaseURL } from '../lib/config.js';
import { STATES } from './manifest.js';

export interface Wave01Config {
  evidenceMode: 'evidence' | 'debug-non-evidence';
  baseURL: string;
  sessionSecretFile: string;
  privateDir: string;
  operatorIdentity: string;
  adminEmail: string;
  fixtureMapFile: string;
  evidenceDir: string;
  stateDir: string;
  baseReleaseFile: string | null;
  companionFile: string | null;
  envDeclarationFile: string | null;
  reviewedRunnerCommit: string | null;
  reviewedSuiteDigest: string | null;
  states: string[];
}

const opt = (name: string): string | null => {
  const v = process.env[name];
  return v === undefined || v.trim() === '' ? null : v.trim();
};

export function parseStates(raw: string | null): string[] {
  if (!raw) return STATES.map((s) => s.id);
  const ids = raw
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
  for (const id of ids) {
    if (!STATES.some((s) => s.id === id))
      throw new Error(`LAYOUT_SURVEY_STATES: unknown state ${id}`);
  }
  return ids;
}

export function loadWave01Config(): Wave01Config {
  const debug = process.env.LAYOUT_SURVEY_DEBUG_NON_EVIDENCE === '1';
  const evidenceMode = debug ? 'debug-non-evidence' : 'evidence';
  const need = (name: string) => (debug ? opt(name) : requireEnv(name));
  const cfg: Wave01Config = {
    evidenceMode,
    baseURL: validateBaseURL(requireEnv('LAYOUT_SURVEY_BASE_URL')),
    sessionSecretFile: requireEnv('LAYOUT_SURVEY_SESSION_SECRET_FILE'),
    privateDir: path.resolve(requireEnv('LAYOUT_SURVEY_PRIVATE_DIR')),
    operatorIdentity: requireEnv('LAYOUT_SURVEY_OPERATOR'),
    adminEmail: requireEnv('LAYOUT_SURVEY_ADMIN_EMAIL'),
    fixtureMapFile: path.resolve(requireEnv('LAYOUT_SURVEY_FIXTURE_MAP')),
    evidenceDir: path.resolve(requireEnv('LAYOUT_SURVEY_EVIDENCE_DIR')),
    stateDir: path.resolve(requireEnv('LAYOUT_SURVEY_STATE_DIR')),
    baseReleaseFile: need('LAYOUT_SURVEY_BASE_RELEASE_FILE'),
    companionFile: need('LAYOUT_SURVEY_COMPANION_FILE'),
    envDeclarationFile: need('LAYOUT_SURVEY_ENV_DECLARATION_FILE'),
    reviewedRunnerCommit: need('LAYOUT_SURVEY_REVIEWED_RUNNER_COMMIT'),
    reviewedSuiteDigest: need('LAYOUT_SURVEY_REVIEWED_SUITE_DIGEST'),
    states: parseStates(opt('LAYOUT_SURVEY_STATES')),
  };
  for (const k of ['baseReleaseFile', 'companionFile', 'envDeclarationFile'] as const) {
    const v = cfg[k];
    if (v) cfg[k] = path.resolve(v);
  }
  return cfg;
}
