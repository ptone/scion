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
 * Builds the Shoelace icon sprite sheet: one SVG file holding a <symbol>
 * for each icon the app uses (USED_ICONS) and for each of Shoelace's own
 * system icons (SYSTEM_ICONS). main.ts imports the file with ?url, so Vite
 * fingerprints it, and registers both icon libraries to draw from it. The
 * browser loads the sheet once and every icon is drawn from it.
 *
 * To add a new icon: add its Bootstrap Icons name to the USED_ICONS array
 * below, then run `npm run copy:shoelace-icons`. The build fails when an
 * icon in either list cannot be found.
 */

import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath, pathToFileURL } from 'url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = resolve(__dirname, '..');

const SRC = resolve(ROOT, 'node_modules/@shoelace-style/shoelace/dist/assets/icons');
const SRC_FALLBACK = resolve(ROOT, 'node_modules/bootstrap-icons/icons');
const SYSTEM_LIBRARY = resolve(
  ROOT,
  'node_modules/@shoelace-style/shoelace/dist/components/icon/library.system.js'
);
/** Where the sheet is written; main.ts imports it. Generated, not committed. */
export const SPRITE_PATH = resolve(ROOT, 'src/generated/shoelace-icons.svg');

/** Symbol id prefix of Shoelace's system icons (see main.ts). */
export const SYSTEM_PREFIX = 'system-';

/** Icons referenced via <sl-icon name="..."> across the app. */
export const USED_ICONS = [
  'arrow-clockwise',
  'arrow-counterclockwise',
  'bell-slash',
  'arrow-down',
  'arrow-down-circle',
  'arrow-left',
  'arrow-left-right',
  'arrow-right',
  'arrow-left-circle',
  'arrow-repeat',
  'arrow-return-right',
  'arrow-right-circle',
  'arrow-up',
  'arrow-up-circle',
  'arrows-angle-contract',
  'arrows-angle-expand',
  'arrows-fullscreen',
  'bar-chart',
  'bell',
  'box-arrow-in-down-left',
  'box-arrow-in-right',
  'box-arrow-right',
  'box-arrow-up-right',
  'broadcast-pin',
  'building',
  'calendar-event',
  'calendar-x',
  'check2',
  'check2-all',
  'chevron-down',
  'chevron-left',
  'chevron-right',
  'chevron-up',
  'chat-dots',
  'chat-square',
  'chat-text',
  'check',
  'check-circle',
  'check-circle-fill',
  'check-lg',
  'circle',
  'circle-fill',
  'clipboard',
  'clipboard-check',
  'clock',
  'cloud',
  'cloud-arrow-down',
  'collection',
  'cloud-download',
  'cloud-upload',
  'clock-history',
  'compass',
  'code',
  'code-slash',
  'code-square',
  'copy',
  'cpu',
  'dash-circle',
  'database',
  'diagram-3',
  'diagram-3-fill',
  'door-open',
  'download',
  'emoji-frown',
  'emoji-smile',
  'envelope',
  'envelope-open',
  'exclamation-circle',
  'exclamation-circle-fill',
  'exclamation-octagon',
  'exclamation-triangle',
  'eye',
  'eye-slash',
  'file-earmark',
  'file-earmark-arrow-down',
  'file-earmark-code',
  'file-earmark-plus',
  'file-earmark-richtext',
  'file-earmark-text',
  'file-earmark-zip',
  'file-text',
  'files',
  'filetype-md',
  'floppy',
  'folder',
  'folder-fill',
  'folder-minus',
  'folder-plus',
  'folder-symlink',
  'folder-x',
  'folder2-open',
  'funnel',
  'gear',
  'git',
  'github',
  'globe',
  'globe2',
  'graph-up',
  'grid',
  'grid-3x3-gap',
  'hammer',
  'google',
  'hash',
  'hdd-rack',
  'heart-pulse',
  'house',
  'hourglass-bottom',
  'hourglass-split',
  'image',
  'info-circle',
  'info-circle-fill',
  'journal-text',
  'bell-fill',
  'key',
  'keyboard',
  'lightbulb',
  'lightning-charge',
  'link-45deg',
  'list',
  'lock',
  'list-ul',
  'moon',
  'paperclip',
  'pencil',
  'pencil-square',
  'people',
  'person',
  'person-check',
  'person-circle',
  'person-lines-fill',
  'person-plus',
  'person-x',
  'play-circle',
  'plus-circle',
  'plug',
  'plus-lg',
  'printer',
  'puzzle',
  'question-circle',
  'reply',
  'robot',
  'pause-circle',
  'search',
  'send',
  'shield',
  'shield-check',
  'shield-exclamation',
  'shield-lock',
  'shield-x',
  'slash-circle',
  'sliders',
  'sort-down',
  'sort-alpha-down',
  'sort-alpha-down-alt',
  'speedometer2',
  'star',
  'star-fill',
  'stop-circle',
  'sun',
  'tag',
  'terminal',
  'three-dots-vertical',
  'trash',
  'upload',
  'volume-mute',
  'volume-up',
  'wifi-off',
  'wrench-adjustable',
  'x-circle',
  'x-circle-fill',
  'x-lg',
];

/** Shoelace's own system icons, used inside its components. */
export const SYSTEM_ICONS = [
  'caret',
  'check',
  'chevron-down',
  'chevron-left',
  'chevron-right',
  'copy',
  'eye',
  'eye-slash',
  'eyedropper',
  'grip-vertical',
  'indeterminate',
  'person-fill',
  'play-fill',
  'pause-fill',
  'radio',
  'star-fill',
  'x-lg',
  'x-circle-fill',
];

/** Root <svg> attributes that carry over to the symbol (drawing attributes). */
const KEPT_ATTRIBUTES = new Set([
  'viewBox',
  'fill',
  'fill-rule',
  'clip-rule',
  'stroke',
  'stroke-width',
  'stroke-linecap',
  'stroke-linejoin',
]);

/** Root <svg> attributes that are dropped (sizing, naming, namespace). */
const DROPPED_ATTRIBUTES = new Set(['xmlns', 'width', 'height', 'class', 'part']);

/** Elements and attributes an icon may not contain; the build fails on them. */
const FORBIDDEN = [
  [/<\s*(script|foreignObject|iframe|object|embed|use|image|a|style)\b/i, 'element'],
  [/\son[a-z]+\s*=/i, 'event attribute'],
  [/\b(xlink:)?href\s*=/i, 'link attribute'],
  [/\bid\s*=/i, 'id attribute'],
  [/url\(/i, 'url() reference'],
];

/**
 * Turns one icon's SVG source into a <symbol> with the given id. Throws
 * when the source is not a single <svg> element, has a root attribute the
 * sheet does not know, or contains anything in FORBIDDEN.
 */
export function iconSymbol(id, svg) {
  const match = /^\s*<svg\b([^>]*)>([\s\S]*)<\/svg>\s*$/.exec(svg);
  if (!match) throw new Error(`icon "${id}" is not a single <svg> element`);
  const [, rootAttributes, body] = match;
  for (const [pattern, what] of FORBIDDEN) {
    if (pattern.test(body) || (what !== 'element' && pattern.test(rootAttributes))) {
      throw new Error(`icon "${id}" contains a disallowed ${what}`);
    }
  }
  const kept = [];
  const rest = rootAttributes.replace(/\s([a-zA-Z:-]+)="([^"]*)"/g, (_m, name, value) => {
    if (KEPT_ATTRIBUTES.has(name)) kept.push(`${name}="${value}"`);
    else if (!DROPPED_ATTRIBUTES.has(name)) {
      throw new Error(`icon "${id}" has an unexpected root attribute ${name}`);
    }
    return '';
  });
  if (rest.trim() !== '') throw new Error(`icon "${id}" has a malformed root element`);
  if (!kept.some((a) => a.startsWith('viewBox='))) {
    throw new Error(`icon "${id}" has no viewBox`);
  }
  const content = body
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .join('');
  return `<symbol id="${id}" ${kept.join(' ')}>${content}</symbol>`;
}

/** The whole sheet from [id, svg source] pairs. */
export function spriteSheet(icons) {
  const ids = new Set();
  const symbols = icons.map(([id, svg]) => {
    if (ids.has(id)) throw new Error(`icon "${id}" is listed twice`);
    ids.add(id);
    return iconSymbol(id, svg);
  });
  return `<svg xmlns="http://www.w3.org/2000/svg">${symbols.join('')}</svg>\n`;
}

/** The SVG source of one app icon, from Shoelace or Bootstrap Icons. */
function readAppIcon(name) {
  for (const dir of [SRC, SRC_FALLBACK]) {
    const path = resolve(dir, `${name}.svg`);
    if (existsSync(path)) return readFileSync(path, 'utf8');
  }
  throw new Error(`icon "${name}.svg" not found in Shoelace or Bootstrap Icons`);
}

/** The SVG source of each system icon, from Shoelace's system library. */
async function readSystemIcons() {
  const { default: library } = await import(pathToFileURL(SYSTEM_LIBRARY).href);
  return SYSTEM_ICONS.map((name) => {
    const url = library.resolver(name);
    const prefix = 'data:image/svg+xml,';
    if (typeof url !== 'string' || !url.startsWith(prefix)) {
      throw new Error(`Shoelace has no system icon "${name}"`);
    }
    return [`${SYSTEM_PREFIX}${name}`, decodeURIComponent(url.slice(prefix.length))];
  });
}

/** Every icon of the sheet as [id, svg source] pairs. */
export async function sheetIcons() {
  return [...USED_ICONS.map((name) => [name, readAppIcon(name)]), ...(await readSystemIcons())];
}

async function main() {
  const sheet = spriteSheet(await sheetIcons());
  mkdirSync(dirname(SPRITE_PATH), { recursive: true });
  writeFileSync(SPRITE_PATH, sheet);
  console.log(
    `Shoelace icons: ${USED_ICONS.length} app and ${SYSTEM_ICONS.length} system icons, ` +
      `${sheet.length} bytes`
  );
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((err) => {
    console.error(`copy-shoelace-icons: ${err.message}`);
    process.exit(1);
  });
}
