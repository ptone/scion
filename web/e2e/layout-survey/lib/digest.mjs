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

// Dependency-free digest helpers shared by the Playwright runner (TypeScript)
// and the steward/capturer node scripts. Plain ESM so `node` can run the
// scripts without a TypeScript loader.

import { createHash } from 'node:crypto';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

export const SHA256_RE = /^[0-9a-f]{64}$/;

/** @param {Buffer|string} data */
export function sha256(data) {
  return createHash('sha256').update(data).digest('hex');
}

/** @param {string} file */
export function sha256File(file) {
  return sha256(fs.readFileSync(file));
}

/**
 * List regular files under root (relative POSIX paths, sorted bytewise).
 * Symlinks are rejected so a tree digest cannot silently follow them.
 * @param {string} root
 * @param {(rel: string) => boolean} [include]
 * @returns {string[]}
 */
export function listTree(root, include = () => true) {
  /** @type {string[]} */
  const out = [];
  /** @param {string} dir */
  const walk = (dir) => {
    for (const ent of fs.readdirSync(dir, { withFileTypes: true })) {
      const abs = path.join(dir, ent.name);
      const rel = path.relative(root, abs).split(path.sep).join('/');
      if (ent.isSymbolicLink()) throw new Error(`symlink not allowed in digested tree: ${rel}`);
      if (ent.isDirectory()) walk(abs);
      else if (ent.isFile() && include(rel)) out.push(rel);
    }
  };
  walk(root);
  return out.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
}

/**
 * Manifest text in `sha256sum` format ("<hex>  <relpath>\n" per file).
 * @param {string} root
 * @param {(rel: string) => boolean} [include]
 */
export function treeManifest(root, include) {
  return listTree(root, include)
    .map((rel) => `${sha256File(path.join(root, rel))}  ${rel}\n`)
    .join('');
}

/**
 * Tree digest = sha256 of the sha256sum-format manifest. Reproducible with
 * `(cd root && find . -type f | sed 's|^\./||' | LC_ALL=C sort | xargs sha256sum) | sha256sum`.
 * @param {string} root
 * @param {(rel: string) => boolean} [include]
 */
export function treeDigest(root, include) {
  return sha256(treeManifest(root, include));
}

/** Directory holding the layout-survey suite (parent of lib/). */
export const SUITE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

/**
 * Scenario-suite digest: every tracked suite source file (not docs, not
 * fault-injection patches, not records templates). Both the runner and the
 * release helper compute it with this one function.
 */
export function scenarioSuiteSha() {
  return treeDigest(
    SUITE_DIR,
    (rel) =>
      /\.(ts|mjs|json)$/.test(rel) &&
      !rel.startsWith('records/') &&
      !rel.startsWith('fault-injection/')
  );
}

/** Fixture recipe digest (the JSON recipe file bytes). */
export function fixtureRecipeSha() {
  return sha256File(path.join(SUITE_DIR, 'fixtures', 'groups-v1.json'));
}
