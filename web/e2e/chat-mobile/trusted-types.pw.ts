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
 * Chat runs with Trusted Types enforced (ptone/scion#6367): the space rail,
 * a thread with markdown, links, a table and a code block that names a
 * language, and a direct message conversation make no plain-string markup
 * call, and every icon they show (app icons and Shoelace's own) is drawn.
 *
 * Tagged @static so it also runs on the WebKit iPhone project.
 *
 * Until ptone/scion#6127 lands, chat message rendering still assigns markup
 * strings, which would throw under the enforced rule and blank the thread,
 * so these tests add the rule as report-only and accept exactly those calls
 * (CHAT_RENDERING below). When ptone/scion#6127 is merged in, the allowance
 * goes and the tests switch to the enforced rule.
 */

import { test, expect } from '@playwright/test';
import { HUMAN_MEMBER_ID } from './mock-api.js';
import { openChatRail, openGeneralThread } from './fixture.js';
import {
  enforceTrustedTypes,
  expectIconsDrawn,
  expectNoTrustedTypesFindings,
  expectProbeSeesIconFileParse,
  expectProbeSeesViolation,
  type AllowedViolation,
  type TrustedTypesOptions,
} from '../trusted-types.js';

const MODE: TrustedTypesOptions = { mode: 'report' };

/** Chat message rendering's markup assignments, replaced by ptone/scion#6127. */
const CHAT_RENDERING: AllowedViolation[] = [
  {
    reason: 'chat message rendering (ptone/scion#6127)',
    sample: /^Element innerHTML\|/,
    sourceFile:
      /\/src\/(utils\/markdown|components\/shared\/(chat\/chat-message|markdown-preview|chat\/chat-interagent-marker))\.ts|\/node_modules\/\.vite\/deps\/(lit[\w-]*|chunk-[\w-]+)\.js/,
  },
];

test('@static chat thread and rail make no plain-string markup call and draw their icons', async ({
  page,
}) => {
  const errors = await enforceTrustedTypes(page, MODE);
  await openChatRail(page);
  await openGeneralThread(page);
  await expect(page.locator('scion-chat-message').first()).toBeVisible({ timeout: 15_000 });

  await expectIconsDrawn(page, 3);
  await expectNoTrustedTypesFindings(page, errors, CHAT_RENDERING);
  await expectProbeSeesViolation(page, MODE);
  await expectProbeSeesIconFileParse(page);
});

test('@static a direct message conversation makes no plain-string markup call', async ({
  page,
}) => {
  const errors = await enforceTrustedTypes(page, MODE);
  await openChatRail(page);
  await page.goto(`/chat/dm/${HUMAN_MEMBER_ID}`, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('scion-chat-thread')).toBeAttached({ timeout: 15_000 });

  await expectIconsDrawn(page, 1);
  await expectNoTrustedTypesFindings(page, errors, CHAT_RENDERING);
  await expectProbeSeesViolation(page, MODE);
});
