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
 * STEWARD-ONLY seed command for the /admin/groups survey fixture.
 *
 * Run only through playwright.seed.config.ts (a separate invocation); the
 * capture config never matches this file. Creates the recipe's groups via
 * the real POST /api/v1/groups (harness createGroup), reads them back through
 * the real list API, and writes the fixture map. Refuses to run twice into
 * the same fixture map path.
 */

import { test, expect } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { createGroup } from '../harness/seed.js';
import { loadSeedConfig } from './lib/config.js';
import { fixtureRecipeSha, SUITE_DIR } from './lib/digest.mjs';
import {
  envelope,
  readbackGroups,
  writeJSONExclusive,
  type FixtureMap,
  type FixtureResource,
  type Recipe,
} from './lib/records.js';
import { openAdminSession } from './lib/session.js';

test('steward seed: /admin/groups fixture (groups-v1)', async () => {
  const cfg = loadSeedConfig();
  if (fs.existsSync(cfg.fixtureMapFile)) {
    throw new Error(`fixture map already exists at ${cfg.fixtureMapFile}; refusing to re-seed`);
  }
  const recipe = JSON.parse(
    fs.readFileSync(path.join(SUITE_DIR, 'fixtures', 'groups-v1.json'), 'utf-8')
  ) as Recipe;

  const { session, inventory } = await openAdminSession({
    ...cfg,
    purpose: `steward-seed-${cfg.fixtureTag}`,
  });
  const searchTerm = `ls-${cfg.fixtureTag}-`;

  const resources: FixtureResource[] = [];
  for (const g of recipe.groups) {
    const slug = `${searchTerm}${g.slugSuffix}`;
    const created = await createGroup(cfg.baseURL, session.accessToken, {
      name: g.name,
      slug,
      description: g.description,
      labels: g.labels,
    });
    expect(created.id, `createGroup returned an id for ${g.key}`).toBeTruthy();
    resources.push({
      key: g.key,
      profile: g.profile,
      id: created.id,
      slug,
      name: g.name,
      description: g.description,
      labels: g.labels,
    });
  }

  const readback = await readbackGroups(cfg.baseURL, session.accessToken, searchTerm, resources);
  const map: FixtureMap = {
    ...envelope('fixture-map', cfg.operatorIdentity),
    kind: 'fixture-map',
    recipeId: recipe.recipeId,
    fixtureRecipeSha: fixtureRecipeSha(),
    fixtureTag: cfg.fixtureTag,
    baseURL: cfg.baseURL,
    searchTerm,
    resources,
    readback,
    seedIdentity: { principalId: inventory.principal.id, purpose: inventory.purpose },
  };
  fs.mkdirSync(path.dirname(cfg.fixtureMapFile), { recursive: true });
  writeJSONExclusive(cfg.fixtureMapFile, map);
  // Value-free inventory beside the map so the steward can account for it.
  writeJSONExclusive(cfg.fixtureMapFile.replace(/\.json$/, '') + '.seed-issuance.json', inventory);

  expect(
    readback.ok,
    `real-API readback: missing=${readback.missing} mismatched=${readback.mismatched}`
  ).toBe(true);
});
