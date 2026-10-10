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
 * The test-hub banner in a real browser (ptone/scion#4240, phase W): the
 * member and super-admin variants on the dashboard, a project page, an admin
 * page and the login page, for a viewer, a member and an admin user; no
 * banner when every gate is off; no close control; and the shell bringing
 * the banner back after it is removed in devtools.
 */

import { test, expect, type Page } from '@playwright/test';

import {
  MEMBER_TIER,
  OFF,
  PROJECT_ID,
  SUPER_ADMIN_TIER,
  setupHub,
  type Role,
  type TestInfraStatus,
} from './mock-api.js';

const MEMBER_TEXT = 'TEST HUB: test identities are enabled';
const SUPER_ADMIN_TEXT =
  'TEST HUB: test admin identities are enabled; this hub can mint hub-admins and super-admins';

const banner = (page: Page) => page.locator('sl-alert.test-hub-banner');

async function expectBanner(page: Page, text: string, icon: string): Promise<void> {
  const b = banner(page);
  await expect(b).toHaveCount(1);
  await expect(b).toBeVisible();
  await expect(b).toHaveText(text);
  await expect(b).toHaveAttribute('variant', 'warning');
  await expect(b).toHaveAttribute('open', '');
  await expect(b).not.toHaveAttribute('closable', /.*/);
  await expect(b.locator('sl-icon[slot="icon"]')).toHaveAttribute('name', icon);
}

const pages: Array<{ name: string; path: string; ready: string; roles: Role[] }> = [
  { name: 'dashboard', path: '/', ready: 'scion-app', roles: ['viewer', 'member', 'admin'] },
  {
    name: 'project page',
    path: `/projects/${PROJECT_ID}`,
    ready: 'scion-app',
    roles: ['viewer', 'member', 'admin'],
  },
  {
    name: 'admin page',
    path: '/admin/users',
    ready: 'scion-page-admin-users',
    roles: ['admin'],
  },
];

const variants: Array<[string, TestInfraStatus, string, string]> = [
  ['member tier', MEMBER_TIER, MEMBER_TEXT, 'exclamation-triangle'],
  ['super-admin tier', SUPER_ADMIN_TIER, SUPER_ADMIN_TEXT, 'exclamation-octagon'],
];

for (const [variant, status, text, icon] of variants) {
  test.describe(variant, () => {
    for (const p of pages) {
      for (const role of p.roles) {
        test(`${p.name} as ${role}`, async ({ page }) => {
          await setupHub(page, { status, role });
          await page.goto(p.path, { waitUntil: 'domcontentloaded' });
          await expect(page.locator(p.ready)).toHaveCount(1);
          await expectBanner(page, text, icon);
        });
      }
    }

    test('login page, signed out', async ({ page }) => {
      const hub = await setupHub(page, { status, role: null });
      await page.goto('/login', { waitUntil: 'domcontentloaded' });
      await expect(page.locator('scion-login-page')).toHaveCount(1);
      await expectBanner(page, text, icon);
      expect(hub.statusRequests()).toBeGreaterThan(0);
    });
  });
}

test.describe('every gate off', () => {
  for (const p of pages) {
    test(`no banner element on the ${p.name}`, async ({ page }) => {
      const hub = await setupHub(page, { status: OFF, role: 'admin' });
      await page.goto(p.path, { waitUntil: 'domcontentloaded' });
      await expect(page.locator(p.ready)).toHaveCount(1);
      await expect.poll(() => hub.statusRequests()).toBeGreaterThan(0);
      await expect(page.locator('sl-alert')).toHaveCount(0);
    });
  }

  test('no banner element on the login page', async ({ page }) => {
    const hub = await setupHub(page, { status: OFF, role: null });
    await page.goto('/login', { waitUntil: 'domcontentloaded' });
    await expect(page.locator('scion-login-page')).toHaveCount(1);
    await expect.poll(() => hub.statusRequests()).toBeGreaterThan(0);
    await expect(page.locator('sl-alert')).toHaveCount(0);
  });
});

test('removing the banner in devtools, then navigating, brings it back', async ({ page }) => {
  await setupHub(page, { status: MEMBER_TIER, role: 'member' });
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expectBanner(page, MEMBER_TEXT, 'exclamation-triangle');

  // What deleting the node in the elements panel does.
  await page.evaluate(() => {
    const shell = document.querySelector('scion-app');
    shell?.shadowRoot?.querySelector('sl-alert.test-hub-banner')?.remove();
  });
  await expect(banner(page)).toHaveCount(0);

  // An in-app navigation (the router intercepts the link click).
  await page.evaluate((href) => {
    const a = document.createElement('a');
    a.href = href;
    a.textContent = 'go';
    a.id = 'test-nav-link';
    document.body.appendChild(a);
  }, `/projects/${PROJECT_ID}`);
  await page.locator('#test-nav-link').click();
  await expect(page).toHaveURL(new RegExp(`/projects/${PROJECT_ID}$`));
  await expectBanner(page, MEMBER_TEXT, 'exclamation-triangle');
});

test('browser flags, experiments and storage do not change it', async ({ page }) => {
  await page.addInitScript(() => {
    window.__SCION_FEATURES__ = { 'web.test_hub_banner': false, 'hub.test_identities': false };
    for (const store of [window.localStorage, window.sessionStorage]) {
      store.setItem('scion:feature:web.test_hub_banner', 'false');
      store.setItem('scion:feature:test_hub_banner', 'false');
      store.setItem('scion-test-hub-banner-dismissed', 'true');
    }
  });
  await setupHub(page, { status: MEMBER_TIER, role: 'member' });
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expectBanner(page, MEMBER_TEXT, 'exclamation-triangle');
});
