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
 * The production build, served by the real hub, runs with Trusted Types
 * enforced (ptone/scion#6367): the dashboard, the terminal workspace and
 * chat load with no plain-string markup call, and their icons, drawn from
 * the built icon sheet, show.
 */

import { expect, test } from '@playwright/test';

import { devSignInURL } from '../harness/auth.js';
import { readHubState, type HubState } from '../harness/hub.js';
import { stateFile } from './state.js';
import {
  enforceTrustedTypes,
  expectIconsDrawn,
  expectNoTrustedTypesFindings,
  expectProbeSeesIconFileParse,
  expectProbeSeesViolation,
  expectRuleOnLoadedDocument,
} from '../trusted-types.js';

function hubState(): HubState {
  const state = readHubState(stateFile());
  if (!state) throw new Error('no hub state; did global-setup run?');
  return state;
}

for (const path of ['/', '/terminals', '/chat']) {
  test(`${path} makes no plain-string markup call with the rule enforced`, async ({ browser }) => {
    const state = hubState();
    const context = await browser.newContext({ baseURL: state.baseURL });
    const page = await context.newPage();
    const errors = await enforceTrustedTypes(page);
    const sheets: string[] = [];
    page.on('request', (r) => {
      const url = new URL(r.url());
      if (/shoelace-icons|^\/shoelace\//.test(url.pathname)) sheets.push(url.pathname);
    });

    await page.goto(await devSignInURL(state.baseURL, state.devToken), {
      waitUntil: 'domcontentloaded',
    });
    await page.goto(path, { waitUntil: 'domcontentloaded' });
    // A routed page, or on /terminals the terminal workspace host, which is
    // attached hidden until the workspace is shown, so wait until visible.
    const ready = path === '/terminals' ? '#terminal-workspace' : '[data-scion-page]';
    await expect(page.locator(ready).first()).toBeVisible({ timeout: 30_000 });
    // The page was opened directly after signing in, so it was routed.
    await expectRuleOnLoadedDocument(page);

    await expectIconsDrawn(page, 1);
    await expectNoTrustedTypesFindings(page, errors);
    // Icons come from the one fingerprinted sheet, not from per-icon files.
    expect([...new Set(sheets)], 'icon sheet requests').toEqual([
      expect.stringMatching(/^\/assets\/shoelace-icons-[\w-]+\.svg$/),
    ]);

    await expectProbeSeesViolation(page);
    await expectProbeSeesIconFileParse(page);
    await context.close();
  });
}
