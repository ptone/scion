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
 * Copies Shoelace's icon assets into public/shoelace/assets/icons so that
 * <sl-icon> resolves under both the Vite dev server and the embedded Go server.
 *
 * The main web UI maintains an explicit allowlist of icon names; for this
 * prototype we copy the whole set, which trades a little bundle size for not
 * having to touch a manifest every time a new icon is used.
 */

import { cp, mkdir, access } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '..');

const src = resolve(root, 'node_modules/@shoelace-style/shoelace/dist/assets/icons');
const dest = resolve(root, 'public/shoelace/assets/icons');

try {
  await access(src);
} catch {
  console.warn('[copy-shoelace-icons] Shoelace not installed yet; skipping.');
  process.exit(0);
}

await mkdir(dest, { recursive: true });
await cp(src, dest, { recursive: true });
console.info(`[copy-shoelace-icons] copied icons -> ${dest}`);
