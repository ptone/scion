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
 * The artifact page with Trusted Types rules on (ptone/scion#6367): reading
 * a markdown entry in the preview frame and opening the review editor make
 * no plain-string markup call, and the page's icons are drawn.
 *
 * The preview still reads its rendered markdown back into nodes once, from
 * a string, to point images at the version's files (IMAGE_REWRITE below).
 * That would throw under the enforced rule and leave the preview empty, so
 * this test adds the rule as report-only and accepts exactly that call.
 * When ptone/scion#6674 gives the preview nodes to rewrite instead, the
 * allowance goes and the test switches to the enforced rule.
 */

import { expect, test } from '@playwright/test';

import { devSignInURL } from '../harness/auth.js';
import {
  artifactPage,
  createProject,
  hub,
  publish,
  setExperiments,
  startGraphicalReview,
} from './fixtures.js';
import {
  enforceTrustedTypes,
  expectIconsDrawn,
  expectNoTrustedTypesFindings,
  expectProbeSeesIconFileParse,
  expectProbeSeesViolation,
  expectRuleOnLoadedDocument,
  trustedTypesFindings,
  type AllowedViolation,
  type TrustedTypesOptions,
} from '../trusted-types.js';

const MODE: TrustedTypesOptions = { mode: 'report' };

const DOC = [
  '# Trusted preview',
  '',
  'A paragraph with a [link](https://example.com/page) and `code`.',
  '',
  '- one',
  '- two',
  '',
  '```go',
  'func main() {}',
  '```',
  '',
].join('\n');

/** The preview's image rewrite of its rendered markdown (ptone/scion#6674). */
const IMAGE_REWRITE: AllowedViolation[] = [
  {
    reason: 'preview image rewrite (ptone/scion#6674)',
    sample: /^Element innerHTML\|<h1>Trusted preview<\/h1>/,
  },
];

let project = '';

test.describe.configure({ mode: 'serial' });

test.beforeAll(async () => {
  await setExperiments(true);
  project = await createProject(`e2e-tt-${Date.now().toString(36)}`);
});

test.afterAll(async () => {
  await setExperiments(null);
});

test('the artifact preview and review editor make no plain-string markup call', async ({
  page,
}) => {
  const errors = await enforceTrustedTypes(page, MODE);
  const a = await publish({
    project,
    title: 'Trusted preview',
    entry: 'index.md',
    files: { 'index.md': Buffer.from(DOC) },
  });
  // Sign in first, then open the artifact page directly: a page reached
  // through the sign-in redirect is not routed, so it would get no rule.
  const h = hub();
  await page.goto(await devSignInURL(h.baseURL, h.devToken), { waitUntil: 'domcontentloaded' });
  await page.goto(`${h.baseURL}${artifactPage(project, a.id)}`, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('scion-page-artifact-detail')).toBeAttached({ timeout: 30_000 });
  await expect(page.locator('scion-page-artifact-detail scion-detail-header').first()).toBeVisible({
    timeout: 30_000,
  });
  await expectRuleOnLoadedDocument(page);

  // The preview frame shows the document, with its own script-free policy.
  const frameHost = page.locator('scion-page-artifact-detail scion-artifact-markdown-frame');
  await expect(frameHost).toBeVisible();
  const frame = frameHost.locator('iframe');
  await expect(frame).toHaveAttribute(
    'sandbox',
    'allow-same-origin allow-popups allow-popups-to-escape-sandbox'
  );
  const preview = page.frameLocator('scion-artifact-markdown-frame iframe');
  await expect(preview.locator('h1')).toHaveText('Trusted preview');
  await expect(preview.locator('meta[http-equiv="Content-Security-Policy"]')).toHaveAttribute(
    'content',
    "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'"
  );
  await expect(preview.locator('a[href="https://example.com/page"]')).toHaveCount(1);

  await expectIconsDrawn(page, 1);
  const before = await expectNoTrustedTypesFindings(page, errors, IMAGE_REWRITE);
  // The allowance is still needed; remove it once this fails.
  expect(before['preview image rewrite (ptone/scion#6674)']).toBeGreaterThan(0);
  // Report-only console notices: only WebKit logs one per report-only
  // violation, so only there must notices be seen. In any engine, every
  // notice must pair with the allowance, or it is a finding.
  const paired = await trustedTypesFindings(page, errors, IMAGE_REWRITE);
  if (test.info().project.name === 'webkit') {
    expect(paired.notices.seen, 'WebKit report-only notices').toBeGreaterThan(0);
  }
  expect(paired.notices.matched, 'notices paired with the allowance').toBe(paired.notices.seen);
  // Negative check: without the allowance, the image rewrite's report-only
  // violations are findings, and so is every console notice of them, since
  // none can pair. The error count is taken first, so an error logged while
  // the findings are read cannot make the comparison fail.
  const errorsBefore = errors.length;
  const strict = await trustedTypesFindings(page, errors);
  expect(strict.violations.length, 'report-only violations without an allowance').toBeGreaterThan(
    0
  );
  expect(strict.notices.matched, 'notices paired without an allowance').toBe(0);
  expect(strict.errors.length, 'unpaired notices count as errors').toBe(
    errorsBefore + strict.notices.seen
  );

  await startGraphicalReview(page);
  await expectIconsDrawn(page, 1);
  await expectNoTrustedTypesFindings(page, errors, IMAGE_REWRITE);

  await expectProbeSeesViolation(page, MODE);
  await expectProbeSeesIconFileParse(page);
});
