/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * The review editor's Milkdown dependency is pinned to one exact version,
 * in package.json and in the lockfile, and the editor imports only the
 * packages it is meant to use. An upgrade is a deliberate change that
 * edits MILKDOWN_VERSION here and re-runs the editor's rendering tests.
 */

import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const MILKDOWN_VERSION = '7.22.3';

/** The Milkdown packages the editor depends on directly. */
const DIRECT = [
  '@milkdown/core',
  '@milkdown/ctx',
  '@milkdown/plugin-history',
  '@milkdown/preset-commonmark',
  '@milkdown/preset-gfm',
  '@milkdown/prose',
  '@milkdown/utils',
];

/** Import specifiers the editor may use. */
const ALLOWED_IMPORT =
  /^@milkdown\/(core|ctx|utils|preset-commonmark|preset-gfm|plugin-history|prose\/[a-z-]+)$/;

const web = join(__dirname, '../../..');

describe('Milkdown dependency', () => {
  it('pins every direct Milkdown package exactly in package.json', () => {
    const pkg = JSON.parse(readFileSync(join(web, 'package.json'), 'utf-8')) as {
      dependencies: Record<string, string>;
      devDependencies: Record<string, string>;
    };
    const all = { ...pkg.dependencies, ...pkg.devDependencies };
    const milkdown = Object.keys(all).filter((k) => k.startsWith('@milkdown/'));
    expect(milkdown.sort()).toEqual(DIRECT);
    for (const name of DIRECT) expect(all[name], name).toBe(MILKDOWN_VERSION);
  });

  it('has every @milkdown entry of the lockfile at that version', () => {
    const lock = JSON.parse(readFileSync(join(web, 'package-lock.json'), 'utf-8')) as {
      packages: Record<string, { version?: string }>;
    };
    const entries = Object.entries(lock.packages).filter(([path]) =>
      /(^|\/)node_modules\/@milkdown\/[^/]+$/.test(path)
    );
    expect(entries.length).toBeGreaterThanOrEqual(DIRECT.length);
    for (const [path, entry] of entries) expect(entry.version, path).toBe(MILKDOWN_VERSION);
    const names = entries.map(([path]) => path.replace(/^.*node_modules\//, ''));
    expect(names).not.toContain('@milkdown/kit');
    expect(names).not.toContain('@milkdown/crepe');
  });

  it('imports only the allowed Milkdown packages', () => {
    const files: string[] = [];
    const walk = (dir: string): void => {
      for (const name of readdirSync(dir)) {
        const p = join(dir, name);
        if (statSync(p).isDirectory()) walk(p);
        else if (p.endsWith('.ts')) files.push(p);
      }
    };
    walk(join(web, 'src'));
    const seen = new Set<string>();
    for (const f of files) {
      const text = readFileSync(f, 'utf-8');
      for (const m of text.matchAll(/from '(@milkdown\/[^']+)'|import\('(@milkdown\/[^']+)'\)/g)) {
        const spec = m[1] ?? m[2];
        seen.add(spec);
        expect(ALLOWED_IMPORT.test(spec), `${relative(web, f)}: ${spec}`).toBe(true);
        expect(
          relative(web, f).startsWith('src/components/artifact-review/'),
          relative(web, f)
        ).toBe(true);
      }
    }
    expect(seen.size).toBeGreaterThan(0);
  });
});
