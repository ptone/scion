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

import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { getIconLibrary } from '@shoelace-style/shoelace/dist/components/icon/library.js';

import { registerShoelaceIcons, spriteLibraries } from './shoelace-icons.js';

describe('Shoelace icon libraries', () => {
  it('resolve app icons and system icons to symbols of the sheet', () => {
    const [app, system] = spriteLibraries('/assets/shoelace-icons-abc.svg');
    expect(app).toMatchObject({ name: 'default', spriteSheet: true });
    expect(system).toMatchObject({ name: 'system', spriteSheet: true });
    expect(app!.resolver('trash')).toBe('/assets/shoelace-icons-abc.svg#trash');
    expect(system!.resolver('caret')).toBe('/assets/shoelace-icons-abc.svg#system-caret');
  });

  it('registers both libraries to draw from the sheet', () => {
    registerShoelaceIcons('/sheet.svg');
    for (const [name, icon, url] of [
      ['default', 'gear', '/sheet.svg#gear'],
      ['system', 'x-lg', '/sheet.svg#system-x-lg'],
    ] as const) {
      const library = getIconLibrary(name);
      expect(library?.spriteSheet, name).toBe(true);
      expect(library?.resolver(icon)).toBe(url);
    }
  });

  it('use the symbol id prefix the sheet builder writes', () => {
    const script = readFileSync(
      resolve(__dirname, '../../scripts/copy-shoelace-icons.mjs'),
      'utf8'
    );
    expect(script).toContain("export const SYSTEM_PREFIX = 'system-';");
  });

  it('are registered by main.ts at load, in place of the per-file icon path', () => {
    const main = readFileSync(resolve(__dirname, 'main.ts'), 'utf8');
    expect(main).toMatch(/^registerShoelaceIcons\(\);$/m);
    expect(main).not.toContain('setBasePath');
  });
});
