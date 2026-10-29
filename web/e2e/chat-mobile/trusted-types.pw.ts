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
} from '../trusted-types.js';

test('@static chat thread and rail make no plain-string markup call and draw their icons', async ({
  page,
}) => {
  const errors = await enforceTrustedTypes(page);
  await openChatRail(page);
  await openGeneralThread(page);
  await expect(page.locator('scion-chat-message').first()).toBeVisible({ timeout: 15_000 });

  await expectIconsDrawn(page, 3);
  await expectNoTrustedTypesFindings(page, errors);
  await expectProbeSeesViolation(page);
  await expectProbeSeesIconFileParse(page);
});

test('@static a direct message conversation makes no plain-string markup call', async ({
  page,
}) => {
  const errors = await enforceTrustedTypes(page);
  await openChatRail(page);
  await page.goto(`/chat/dm/${HUMAN_MEMBER_ID}`, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('scion-chat-thread')).toBeAttached({ timeout: 15_000 });

  await expectIconsDrawn(page, 1);
  await expectNoTrustedTypesFindings(page, errors);
  await expectProbeSeesViolation(page);
});
