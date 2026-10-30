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
 * Runs a page with Trusted Types enforced and records every place it hands
 * a plain string to an API that parses markup (ptone/scion#6367).
 *
 * The hub sends the Trusted Types rule as report-only. These helpers add
 * the same rule as an enforced page policy on every document response, so
 * a markup call made with a plain string throws and is recorded, the way
 * it would be once the rule is enforced (or, in 'report' mode, as a second
 * report-only policy). The probe records each violation
 * of that rule (enforced or report-only) and each page error or console
 * error that names a Trusted Type, from before any page script runs.
 */

import { expect, type Page } from '@playwright/test';

/** The Trusted Types rule the helpers add to each document. */
export const TRUSTED_TYPES_RULE = "require-trusted-types-for 'script'";

/** One recorded violation of the rule. */
export interface TrustedTypesViolation {
  disposition: string;
  /** The sink and the start of the value, e.g. "Element innerHTML|<p>hi". */
  sample: string;
  sourceFile: string;
  lineNumber: number;
  /** The policy that reported it, so the test's own rule can be told apart. */
  originalPolicy: string;
}

/** A violation a test accepts for now, with the reason it is accepted. */
export interface AllowedViolation {
  reason: string;
  sample: RegExp;
  sourceFile?: RegExp;
}

/** What the probe records, less the allowed violations. */
export interface TrustedTypesFindings {
  /** Violations: "<disposition> <sample> <source>:<line>". */
  violations: string[];
  /** Page errors and console errors that name a Trusted Type. */
  errors: string[];
}

export interface TrustedTypesOptions {
  /**
   * 'enforce' (the default) adds the rule as an enforced page policy, so a
   * plain-string markup call throws. 'report' adds it as report-only, for a
   * page that still has an allowed call which would otherwise break it.
   */
  mode?: 'enforce' | 'report';
}

declare global {
  interface Window {
    __trustedTypesViolations?: TrustedTypesViolation[];
  }
}

/**
 * Adds the rule to every document response of the page (keeping the
 * response's own policies) and installs the probe. Call before the first
 * navigation. Returns the page-side errors list, filled as they come.
 */
export async function enforceTrustedTypes(
  page: Page,
  options: TrustedTypesOptions = {}
): Promise<string[]> {
  const header =
    options.mode === 'report' ? 'content-security-policy-report-only' : 'content-security-policy';
  await page.route(
    (url) => !url.pathname.startsWith('/api/'),
    async (route) => {
      if (route.request().resourceType() !== 'document') {
        await route.fallback();
        return;
      }
      // Redirects go back to the browser unchanged, so it follows them
      // itself (a sign-in link redirects to the page it opens); the rule is
      // added to the document the redirect chain ends at.
      const response = await route.fetch({ maxRedirects: 0 });
      if (response.status() >= 300 && response.status() < 400) {
        await route.fulfill({ response });
        return;
      }
      const headers = response.headers();
      const existing = headers[header];
      headers[header] = existing ? `${existing}, ${TRUSTED_TYPES_RULE}` : TRUSTED_TYPES_RULE;
      await route.fulfill({ response, headers });
    }
  );
  await page.addInitScript(() => {
    const seen: TrustedTypesViolation[] = [];
    window.__trustedTypesViolations = seen;
    document.addEventListener(
      'securitypolicyviolation',
      (e) => {
        if (e.effectiveDirective !== 'require-trusted-types-for') return;
        seen.push({
          disposition: e.disposition,
          sample: e.sample,
          sourceFile: e.sourceFile,
          lineNumber: e.lineNumber,
          originalPolicy: e.originalPolicy,
        });
      },
      true
    );
  });
  const errors: string[] = [];
  const namesTrustedType = (text: string): boolean => /Trusted ?(HTML|Script|Type)/i.test(text);
  page.on('pageerror', (err) => {
    if (namesTrustedType(String(err))) errors.push(String(err));
  });
  page.on('console', (msg) => {
    if (msg.type() === 'error' && namesTrustedType(msg.text())) errors.push(msg.text());
  });
  return errors;
}

/**
 * The findings so far: page-side violations not matched by `allowed`, and
 * the collected errors. Also returns how many violations each allowance
 * matched, so a test can check an allowance is still needed.
 */
export async function trustedTypesFindings(
  page: Page,
  errors: string[],
  allowed: AllowedViolation[] = []
): Promise<TrustedTypesFindings & { allowedCounts: Record<string, number> }> {
  const all = await page.evaluate(() => [...(window.__trustedTypesViolations ?? [])]);
  const allowedCounts: Record<string, number> = Object.fromEntries(
    allowed.map((a) => [a.reason, 0])
  );
  const violations: string[] = [];
  for (const v of all) {
    const match = allowed.find(
      (a) => a.sample.test(v.sample) && (!a.sourceFile || a.sourceFile.test(v.sourceFile))
    );
    if (match) allowedCounts[match.reason] = (allowedCounts[match.reason] ?? 0) + 1;
    else violations.push(`${v.disposition} ${v.sample} ${v.sourceFile}:${v.lineNumber}`);
  }
  return { violations, errors: [...errors], allowedCounts };
}

/** Expects no findings beyond the allowed ones. */
export async function expectNoTrustedTypesFindings(
  page: Page,
  errors: string[],
  allowed: AllowedViolation[] = []
): Promise<Record<string, number>> {
  const {
    violations,
    errors: errs,
    allowedCounts,
  } = await trustedTypesFindings(page, errors, allowed);
  expect({ violations, errors: errs }).toEqual({ violations: [], errors: [] });
  return allowedCounts;
}

/**
 * Checks that the rule is in force and the probe sees it: a plain string
 * markup assignment adds a violation with the expected disposition (and
 * throws when enforced). A page served by the hub also carries the hub's
 * own report-only rule, so the same assignment is reported once per rule;
 * the check therefore counts new violations of the expected disposition
 * reported by the test's own rule (originalPolicy). Run it after the page's own assertions, so their empty lists
 * are not vacuous.
 */
export async function expectProbeSeesViolation(
  page: Page,
  options: TrustedTypesOptions = {}
): Promise<void> {
  const before = await page.evaluate(() => window.__trustedTypesViolations?.length ?? -1);
  expect(before).toBeGreaterThanOrEqual(0);
  const threw = await page.evaluate(() => {
    try {
      document.createElement('div').innerHTML = '<b>control</b>';
      return false;
    } catch {
      return true;
    }
  });
  expect(threw, 'a plain string markup assignment throws only under the enforced rule').toBe(
    options.mode !== 'report'
  );
  const disposition = options.mode === 'report' ? 'report' : 'enforce';
  await expect
    .poll(() =>
      page.evaluate(
        ({ from, want, rule }) =>
          (window.__trustedTypesViolations ?? [])
            .slice(from)
            .filter(
              (v) =>
                v.disposition === want &&
                v.originalPolicy === rule &&
                v.sample.startsWith('Element innerHTML')
            ).length,
        { from: before, want: disposition, rule: TRUSTED_TYPES_RULE }
      )
    )
    .toBeGreaterThanOrEqual(1);
}

/**
 * Checks that the probe also sees Shoelace's own markup call: an icon given
 * a file through its src attribute (which the app never does) fetches the
 * file and parses it as markup, which adds a violation. Shows that an empty
 * list on the page is not just Shoelace's calls going unseen.
 */
export async function expectProbeSeesIconFileParse(page: Page): Promise<void> {
  await page.route('**/__tt-control-icon.svg', (route) =>
    route.fulfill({
      contentType: 'image/svg+xml',
      body: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><path d="M0 0h16v16H0z"/></svg>',
    })
  );
  const before = await page.evaluate(() => window.__trustedTypesViolations?.length ?? -1);
  expect(before).toBeGreaterThanOrEqual(0);
  await page.evaluate(() => {
    const icon = document.createElement('sl-icon');
    icon.setAttribute('src', '/__tt-control-icon.svg');
    document.body.append(icon);
  });
  await expect
    .poll(() => page.evaluate(() => window.__trustedTypesViolations?.length ?? -1))
    .toBeGreaterThan(before);
}

/**
 * Waits until every sl-icon in the document (including open shadow roots)
 * has drawn something, and returns how many there are. An icon counts as
 * drawn when its own shadow root holds an svg with a non-empty box.
 */
export async function expectIconsDrawn(page: Page, atLeast: number): Promise<number> {
  let count = 0;
  await expect
    .poll(async () => {
      const result = await page.evaluate(() => {
        const icons: Element[] = [];
        const walk = (root: Document | ShadowRoot): void => {
          for (const el of root.querySelectorAll('*')) {
            if (el.localName === 'sl-icon') icons.push(el);
            if (el.shadowRoot) walk(el.shadowRoot);
          }
        };
        walk(document);
        const visible = icons.filter((icon) => {
          const box = icon.getBoundingClientRect();
          return box.width > 0 && box.height > 0;
        });
        const blank = visible.filter((icon) => {
          const svg = icon.shadowRoot?.querySelector('svg');
          if (!svg) return true;
          const graphic = svg.querySelector('use') ?? svg;
          const box = (graphic as SVGGraphicsElement).getBBox();
          return !(box.width > 0 && box.height > 0);
        });
        return { visible: visible.length, blank: blank.map((i) => i.getAttribute('name')) };
      });
      count = result.visible;
      return result.blank.length === 0 && result.visible >= atLeast
        ? 'ok'
        : `visible=${result.visible} blank=${result.blank.join(',')}`;
    })
    .toBe('ok');
  return count;
}
