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
 * The terminal workspace runs with Trusted Types enforced (ptone/scion#6367):
 * opening terminals, the rail with its per-terminal buttons, the bulk
 * toolbar and the place menu make no plain-string markup call, and every
 * icon they show is drawn.
 */

import { test, expect, type Page } from '@playwright/test';
import { installEventSourceStub } from '../event-source-stub.js';
import {
  enforceTrustedTypes,
  expectIconsDrawn,
  expectNoTrustedTypesFindings,
  expectProbeSeesIconFileParse,
  expectProbeSeesViolation,
  expectRuleOnLoadedDocument,
} from '../trusted-types.js';

const agentA = '11111111-1111-4111-8111-111111111111';
const agentB = '22222222-2222-4222-8222-222222222222';
const agents: Record<string, { id: string; name: string; phase: string; projectId: string }> = {
  [agentA]: { id: agentA, name: 'agent-a', phase: 'running', projectId: 'fixture-project' },
  [agentB]: { id: agentB, name: 'agent-b', phase: 'running', projectId: 'fixture-project' },
};

async function setup(page: Page): Promise<{ readonly attaches: number }> {
  let attaches = 0;
  await installEventSourceStub(page);
  await page.route('**/auth/me', (route) =>
    route.fulfill({ json: { id: 'fixture-user', email: 'fixture@example.test' } })
  );
  await page.route('**/api/v1/settings/public', (route) =>
    route.fulfill({ json: { nativeChatEnabled: true } })
  );
  await page.route(/\/api\/v1\/agents(\?|$)/, (route) =>
    route.fulfill({
      json: Object.values(agents).map((a) => ({ ...a, _capabilities: { actions: ['attach'] } })),
    })
  );
  await page.route('**/api/v1/agents/**', (route) => {
    if (route.request().url().endsWith('/pty')) {
      void route.fulfill({ json: {} });
      return;
    }
    const id =
      route
        .request()
        .url()
        .match(/\/api\/v1\/agents\/([^/?]+)/)?.[1] ?? '';
    void route.fulfill({
      status: agents[id] ? 200 : 404,
      json: agents[id] ?? { error: 'not found' },
    });
  });
  await page.route('**/api/v1/system/status', (route) =>
    route.fulfill({ json: { complete: true } })
  );
  await page.routeWebSocket('**/pty?*', () => {
    attaches++;
  });
  return {
    get attaches(): number {
      return attaches;
    },
  };
}

test('terminal workspace makes no plain-string markup call and draws its icons', async ({
  page,
}) => {
  const errors = await enforceTrustedTypes(page);
  const sockets = await setup(page);

  await page.goto(`/terminals/${agentA}`);
  await expect(page.locator('#terminal-workspace')).toHaveCount(1);
  await expectRuleOnLoadedDocument(page);
  await expect.poll(() => sockets.attaches).toBe(1);
  await page.evaluate(
    (path) =>
      document.dispatchEvent(new CustomEvent('nav-click', { detail: { path }, bubbles: true })),
    `/terminals/${agentB}`
  );
  await expect.poll(() => sockets.attaches).toBe(2);
  await expect(page.locator('.terminal-rail-item')).toHaveCount(2);

  // Each rail row has its place, reconnect and close buttons, each with its icon.
  for (const name of ['grid', 'arrow-clockwise', 'x-circle']) {
    await expect(
      page.locator(`.terminal-rail-item .terminal-icon-action sl-icon[name="${name}"]`)
    ).toHaveCount(2);
  }
  await expect(page.locator('.terminal-bulk-reconnect sl-icon[name="arrow-repeat"]')).toHaveCount(
    1
  );
  await expect(page.locator('.terminal-bulk-remove sl-icon[name="trash"]')).toHaveCount(1);

  // The place menu opens from the first row.
  await page.locator('.terminal-rail-item .terminal-place-btn').first().click();

  await expectIconsDrawn(page, 7);
  await expectNoTrustedTypesFindings(page, errors);
  await expectProbeSeesViolation(page);
  await expectProbeSeesIconFileParse(page);
});
