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
 * Reads icons from the production build: the icon sprite sheet that
 * scripts/copy-shoelace-icons.mjs builds and Vite fingerprints into
 * dist/client/assets/shoelace-icons-<hash>.svg.
 */

import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

/** dist/client of the production build. */
export const distClient = join(webRoot, 'dist/client');

/** The built sheet's path under dist/client (e.g. "assets/shoelace-icons-ab12.svg"). */
export function builtSheetPath(): string {
  const assets = join(distClient, 'assets');
  if (!existsSync(assets)) throw new Error(`no production build in ${distClient}`);
  const sheets = readdirSync(assets).filter((n) => /^shoelace-icons-[\w-]+\.svg$/.test(n));
  if (sheets.length !== 1) throw new Error(`expected one icon sheet, found ${sheets.join(', ')}`);
  return `assets/${sheets[0]}`;
}

/** The drawing content of one icon's <symbol> in a sheet, or null. */
export function symbolContent(sheet: string, name: string): string | null {
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const m = new RegExp(`<symbol id="${escaped}"[^>]*>([\\s\\S]*?)</symbol>`).exec(sheet);
  return m ? (m[1] ?? null) : null;
}

/** The drawing content of a packaged Shoelace icon, as the sheet holds it. */
export function shoelaceIconContent(name: string): string {
  const svg = readFileSync(
    join(webRoot, `node_modules/@shoelace-style/shoelace/dist/assets/icons/${name}.svg`),
    'utf-8'
  );
  const body = /<svg\b[^>]*>([\s\S]*)<\/svg>/.exec(svg)?.[1] ?? '';
  return body
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .join('');
}
