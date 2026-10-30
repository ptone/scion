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
 * Points Shoelace's icon libraries at the app's icon sprite sheet.
 *
 * scripts/copy-shoelace-icons.mjs builds the sheet from USED_ICONS (symbol
 * id = icon name) and Shoelace's system icons (symbol id = "system-" +
 * name). Vite fingerprints the file, so browsers cache it, and every icon
 * on a page is drawn from that one file.
 */

import { css } from 'lit';
import SlIcon from '@shoelace-style/shoelace/dist/components/icon/icon.component.js';
import { registerIconLibrary } from '@shoelace-style/shoelace/dist/utilities/icon-library.js';
// Always a file URL, never an inlined data: URL, which <use> cannot load.
import shoelaceIconsUrl from '../generated/shoelace-icons.svg?no-inline';

/** The options each icon library is registered with. */
export interface SpriteLibrary {
  name: 'default' | 'system';
  resolver: (name: string) => string;
  spriteSheet: true;
}

/** The two icon libraries, drawing from the sheet at `sheetUrl`. */
export function spriteLibraries(sheetUrl: string): SpriteLibrary[] {
  return [
    { name: 'default', resolver: (name) => `${sheetUrl}#${name}`, spriteSheet: true },
    { name: 'system', resolver: (name) => `${sheetUrl}#system-${name}`, spriteSheet: true },
  ];
}

/**
 * Keeps an icon's drawing out of hit testing. WebKit treats a sprite <use>
 * as its own click target, outside the button or badge that holds the
 * icon; without hit testing on the drawing, a click lands on the sl-icon
 * element itself, inside that clickable (as it does for inline icons).
 */
const iconDrawingStyle = css`
  svg {
    pointer-events: none;
  }
`;

/**
 * Registers both icon libraries and the icon drawing style. Call before the
 * first icon renders: each icon takes the component's styles when it is
 * created.
 */
export function registerShoelaceIcons(sheetUrl: string = shoelaceIconsUrl): void {
  for (const { name, ...options } of spriteLibraries(sheetUrl)) {
    registerIconLibrary(name, options);
  }
  if (!SlIcon.elementStyles.includes(iconDrawingStyle)) {
    SlIcon.elementStyles.push(iconDrawingStyle);
  }
}
