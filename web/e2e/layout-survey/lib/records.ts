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

/** Small shared helpers: record envelope, fixture map types, real-API readback. */

import { randomUUID } from 'node:crypto';
import * as fs from 'node:fs';

export interface Envelope {
  schemaVersion: 1;
  kind: 'release' | 'capture' | 'finding' | 'verification' | 'fixture-map';
  id: string;
  createdAt: string;
  producer: string;
}

export function envelope(kind: Envelope['kind'], producer: string): Envelope {
  if (!producer) throw new Error('record producer identity must be non-empty');
  return {
    schemaVersion: 1,
    kind,
    id: `${kind}-${randomUUID()}`,
    createdAt: new Date().toISOString(),
    producer,
  };
}

export interface RecipeGroup {
  key: string;
  profile: 'normal' | 'long-content';
  name: string;
  slugSuffix: string;
  description: string;
  labels: Record<string, string>;
}

export interface Recipe {
  recipeId: string;
  schemaVersion: 1;
  groups: RecipeGroup[];
}

export interface FixtureResource {
  key: string;
  profile: string;
  id: string;
  slug: string;
  name: string;
  description: string;
  labels: Record<string, string>;
}

export interface FixtureMap extends Envelope {
  kind: 'fixture-map';
  recipeId: string;
  fixtureRecipeSha: string;
  fixtureTag: string;
  baseURL: string;
  /** Search term that selects exactly the seeded rows (slug prefix). */
  searchTerm: string;
  resources: FixtureResource[];
  readback: ReadbackResult;
  seedIdentity: { principalId: string; purpose: string };
}

export interface ReadbackResult {
  endpoint: string;
  status: number;
  ok: boolean;
  matched: string[];
  missing: string[];
  mismatched: string[];
  checkedAt: string;
}

interface ApiGroup {
  id: string;
  name: string;
  slug: string;
  description?: string;
  labels?: Record<string, string>;
}

/**
 * Read the seeded rows back through the real list API and compare exact
 * id/name/slug/description/labels. Bearer token is used only in the header.
 */
export async function readbackGroups(
  baseURL: string,
  accessToken: string,
  searchTerm: string,
  expected: FixtureResource[]
): Promise<ReadbackResult> {
  const endpoint = `/api/v1/groups?search=${encodeURIComponent(searchTerm)}&limit=50`;
  const res = await fetch(`${baseURL}${endpoint}`, {
    headers: { Authorization: `Bearer ${accessToken}` },
    redirect: 'manual',
  });
  const result: ReadbackResult = {
    endpoint,
    status: res.status,
    ok: false,
    matched: [],
    missing: [],
    mismatched: [],
    checkedAt: new Date().toISOString(),
  };
  if (!res.ok) return result;
  const body = (await res.json()) as { groups?: ApiGroup[] };
  const byId = new Map((body.groups ?? []).map((g) => [g.id, g]));
  for (const e of expected) {
    const g = byId.get(e.id);
    if (!g) {
      result.missing.push(e.key);
      continue;
    }
    const same =
      g.name === e.name &&
      g.slug === e.slug &&
      (g.description ?? '') === e.description &&
      JSON.stringify(sortKeys(g.labels ?? {})) === JSON.stringify(sortKeys(e.labels));
    (same ? result.matched : result.mismatched).push(e.key);
  }
  result.ok = result.missing.length === 0 && result.mismatched.length === 0 && expected.length > 0;
  return result;
}

function sortKeys(o: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(o).sort(([a], [b]) => (a < b ? -1 : 1)));
}

/** Write JSON exclusively (fails if the file already exists). */
export function writeJSONExclusive(file: string, value: unknown, mode = 0o644): void {
  fs.writeFileSync(file, JSON.stringify(value, null, 2) + '\n', { flag: 'wx', mode });
}
