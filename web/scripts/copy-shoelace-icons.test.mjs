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

// Tests for scripts/copy-shoelace-icons.mjs, the icon sprite sheet builder.
// The "real build" tests read dist/client and are skipped when no build is
// present (they fail instead with REQUIRE_BUILD=1), so run them after a build.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  SPRITE_PATH,
  SYSTEM_ICONS,
  SYSTEM_PREFIX,
  USED_ICONS,
  iconSymbol,
  sheetIcons,
  spriteSheet,
} from './copy-shoelace-icons.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const webRoot = resolve(here, '..');
const distClient = join(webRoot, 'dist', 'client');

/** The symbol ids of a sheet, in order. */
function symbolIds(sheet) {
  return [...sheet.matchAll(/<symbol id="([^"]+)"/g)].map((m) => m[1]);
}

/** The ids the sheet must hold: every app icon and every system icon. */
const expectedIds = [...USED_ICONS, ...SYSTEM_ICONS.map((n) => `${SYSTEM_PREFIX}${n}`)];

const BOOTSTRAP_LIKE =
  '<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" fill="currentColor" class="bi bi-x" viewBox="0 0 16 16">\n  <path d="M0 0h16v16H0z"/>\n</svg>\n';

test('iconSymbol keeps the drawing attributes and the content, and drops sizing and naming', () => {
  assert.equal(
    iconSymbol('x', BOOTSTRAP_LIKE),
    '<symbol id="x" fill="currentColor" viewBox="0 0 16 16"><path d="M0 0h16v16H0z"/></symbol>'
  );
  const stroked =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" part="p"><polyline points="6 9 12 15 18 9"></polyline></svg>';
  assert.equal(
    iconSymbol('caret', stroked),
    '<symbol id="caret" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"></polyline></symbol>'
  );
});

test('iconSymbol refuses content the sheet does not take', () => {
  const wrap = (body, root = 'viewBox="0 0 16 16"') => `<svg ${root}>${body}</svg>`;
  const refused = [
    ['script element', wrap('<script>1</script>')],
    ['foreignObject element', wrap('<foreignObject><div/></foreignObject>')],
    ['nested use element', wrap('<use href="#a"/>')],
    ['image element', wrap('<image href="a.png"/>')],
    ['link element', wrap('<a href="https://example.com"><path d="M0 0"/></a>')],
    ['style element', wrap('<style>path{}</style>')],
    ['event attribute', wrap('<path d="M0 0" onclick="x()"/>')],
    ['event attribute on the root', wrap('<path d="M0 0"/>', 'viewBox="0 0 16 16" onload="x()"')],
    ['link attribute', wrap('<path xlink:href="#a"/>')],
    ['id attribute', wrap('<clipPath id="a"><path d="M0 0"/></clipPath>')],
    ['url() reference', wrap('<path fill="url(#a)" d="M0 0"/>')],
    ['unknown root attribute', wrap('<path d="M0 0"/>', 'viewBox="0 0 16 16" style="color:red"')],
    ['missing viewBox', wrap('<path d="M0 0"/>', 'fill="currentColor"')],
    ['not an svg element', '<div><path d="M0 0"/></div>'],
    ['two root elements', `${wrap('<path d="M0 0"/>')}<svg viewBox="0 0 1 1"></svg>x`],
  ];
  for (const [what, svg] of refused) {
    assert.throws(() => iconSymbol('bad', svg), /icon "bad"/, what);
  }
});

test('spriteSheet refuses an icon listed twice', () => {
  assert.throws(
    () =>
      spriteSheet([
        ['x', BOOTSTRAP_LIKE],
        ['x', BOOTSTRAP_LIKE],
      ]),
    /icon "x" is listed twice/
  );
});

test('USED_ICONS and SYSTEM_ICONS have no duplicates', () => {
  assert.equal(new Set(USED_ICONS).size, USED_ICONS.length);
  assert.equal(new Set(SYSTEM_ICONS).size, SYSTEM_ICONS.length);
  assert.ok(USED_ICONS.length > 0 && SYSTEM_ICONS.length > 0);
});

test('SYSTEM_ICONS lists exactly the icons of Shoelace system library', () => {
  const lib = join(
    webRoot,
    'node_modules/@shoelace-style/shoelace/dist/components/icon/library.system.js'
  );
  const chunk = /from "([^"]+)"/.exec(readFileSync(lib, 'utf8'))?.[1];
  assert.ok(chunk, 'library.system.js re-exports a chunk');
  const source = readFileSync(resolve(dirname(lib), chunk), 'utf8');
  const keys = [...source.matchAll(/^ {2}"?([a-z-]+)"?: `/gm)].map((m) => m[1]);
  assert.ok(keys.length > 0, 'no system icon keys found');
  assert.deepEqual([...SYSTEM_ICONS].sort(), keys.sort());
});

test('the sheet built from the installed icons holds exactly USED_ICONS and the system icons', async () => {
  const sheet = spriteSheet(await sheetIcons());
  assert.deepEqual(symbolIds(sheet), expectedIds);
  for (const symbol of sheet.match(/<symbol [^>]*>/g)) {
    assert.match(symbol, / viewBox="[^"]+"/, symbol);
  }
  assert.doesNotMatch(sheet, /<script|\son[a-z]+=|href=/i);
});

test('the generated sheet in src/generated is current', async () => {
  assert.ok(existsSync(SPRITE_PATH), `${SPRITE_PATH} missing; run npm run copy:shoelace-icons`);
  assert.equal(readFileSync(SPRITE_PATH, 'utf8'), spriteSheet(await sheetIcons()));
});

// ---- Real build output (after `npm run build`) ----

const haveBuild = existsSync(join(distClient, 'assets', 'main.js'));
const requireBuild = process.env.REQUIRE_BUILD === '1';
const skipReason =
  haveBuild || requireBuild ? false : 'no build in dist/client; run npm run build first';

test('real build: the sheet is a fingerprinted asset with every icon', { skip: skipReason }, () => {
  assert.ok(haveBuild, `no build in ${distClient} (REQUIRE_BUILD=1)`);
  const sheets = readdirSync(join(distClient, 'assets')).filter((n) =>
    /^shoelace-icons-[A-Za-z0-9_-]+\.svg$/.test(n)
  );
  assert.equal(sheets.length, 1, `expected one shoelace-icons-*.svg, found ${sheets.join(', ')}`);
  const sheet = readFileSync(join(distClient, 'assets', sheets[0]), 'utf8');
  assert.deepEqual(symbolIds(sheet), expectedIds);
  // The app's scripts reference the fingerprinted name.
  const scripts = readdirSync(join(distClient, 'assets')).filter((n) => n.endsWith('.js'));
  assert.ok(
    scripts.some((n) => readFileSync(join(distClient, 'assets', n), 'utf8').includes(sheets[0])),
    `no script references ${sheets[0]}`
  );
  // The per-icon files are gone.
  assert.equal(existsSync(join(distClient, 'shoelace', 'assets', 'icons')), false);
});
