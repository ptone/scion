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
import SlIcon from '@shoelace-style/shoelace/dist/components/icon/icon.component.js';
// Defines sl-icon, as main.ts does before it registers the icon libraries.
import '@shoelace-style/shoelace/dist/components/icon/icon.js';

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

  it('keep the icon drawing out of hit testing, so a click lands on the icon element', async () => {
    // WebKit hit-tests a sprite <use> as its own target, outside the
    // clickable that holds the icon; with the drawing out of hit testing a
    // click lands on the sl-icon element, inside that clickable.
    const noHit = /svg\s*\{[^}]*pointer-events:\s*none/;
    // sl-icon is defined, so its styles are its own, collected at definition.
    expect(Object.hasOwn(SlIcon, 'elementStyles')).toBe(true);
    registerShoelaceIcons('/sheet.svg');
    registerShoelaceIcons('/sheet.svg');
    const rules = SlIcon.elementStyles
      .map((s) => ('cssText' in s ? s.cssText : ''))
      .filter((text) => noHit.test(text));
    expect(rules).toHaveLength(1);

    // An icon created now carries the style in its own shadow root, once.
    const icon = document.createElement('sl-icon') as SlIcon;
    icon.name = 'gear';
    document.body.append(icon);
    try {
      await icon.updateComplete;
      const root = icon.shadowRoot!;
      const texts = [
        ...root.adoptedStyleSheets.flatMap((sheet) => [...sheet.cssRules].map((r) => r.cssText)),
        ...[...root.querySelectorAll('style')].map((el) => el.textContent ?? ''),
      ];
      expect(texts.filter((text) => noHit.test(text))).toHaveLength(1);
    } finally {
      icon.remove();
    }
  });

  it('refuses to register before sl-icon is defined, when its styles would be dropped', () => {
    const own = Object.getOwnPropertyDescriptor(SlIcon, 'elementStyles')!;
    delete (SlIcon as { elementStyles?: unknown }).elementStyles;
    try {
      expect(() => registerShoelaceIcons('/sheet.svg')).toThrow(/define sl-icon/);
    } finally {
      Object.defineProperty(SlIcon, 'elementStyles', own);
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
