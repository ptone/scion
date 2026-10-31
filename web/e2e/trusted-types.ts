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
 * the same rule as an enforced page policy on each document the route
 * fulfills, so a markup call made with a plain string throws and is
 * recorded, the way it would be once the rule is enforced (or, in 'report'
 * mode, as a second report-only policy, TEST_REPORT_POLICY). The probe
 * records each violation of that rule (enforced or report-only) and each
 * page error or console error that names a Trusted Type, from before any
 * page script runs.
 */

import { expect, type Page } from '@playwright/test';

/** The Trusted Types rule the helpers add to each document. */
export const TRUSTED_TYPES_RULE = "require-trusted-types-for 'script'";

/** Where the browser sends the reports of the test's report-only rule. */
export const TEST_REPORT_PATH = '/__e2e-trusted-types-report';

/**
 * The test's report-only policy: the rule plus a report endpoint only the
 * test uses. The hub sends the same rule report-only; giving the test's
 * policy its own text keeps its violations apart from the hub's, so the
 * report-mode check can require a violation of the test's own policy.
 */
export const TEST_REPORT_POLICY = `${TRUSTED_TYPES_RULE}; report-uri ${TEST_REPORT_PATH}`;

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

/** One main-frame document request the page's route handled, in order. */
interface RoutedDocument {
  url: string;
  /** True when fulfilled with the rule added; false when passed to the
   * browser without it (a sign-in request or a redirect). */
  withRule: boolean;
}

const routedByPage = new WeakMap<Page, RoutedDocument[]>();

/**
 * Adds the rule to each document response the page's route fulfills
 * (keeping the response's own policies; sign-in requests and redirects are
 * passed to the browser without it) and installs the probe. Call before the first
 * navigation. Returns the page-side errors list, filled as they come.
 */
export async function enforceTrustedTypes(
  page: Page,
  options: TrustedTypesOptions = {}
): Promise<string[]> {
  const header =
    options.mode === 'report' ? 'content-security-policy-report-only' : 'content-security-policy';
  const routed: RoutedDocument[] = [];
  routedByPage.set(page, routed);
  await page.route(
    (url) => !url.pathname.startsWith('/api/'),
    async (route) => {
      if (new URL(route.request().url()).pathname === TEST_REPORT_PATH) {
        await route.fulfill({ status: 204 });
        return;
      }
      if (route.request().resourceType() !== 'document') {
        await route.fallback();
        return;
      }
      const url = route.request().url();
      // The guard follows the page's own documents, not those of frames.
      const record = (withRule: boolean): void => {
        if (route.request().frame() === page.mainFrame()) routed.push({ url, withRule });
      };
      // A route cannot hand a fetched redirect back to WebKit (Playwright
      // refuses to fulfill a 3xx there), and when the browser follows a
      // redirect itself, this handler sees only the first URL of the chain:
      // the document the chain ends at is not routed and gets no rule. So a
      // test signs in first and then opens its page directly (see
      // expectRuleOnLoadedDocument). The sign-in endpoints are passed to the
      // browser unfetched, so their one-time codes are used once.
      if (new URL(url).pathname.startsWith('/auth/')) {
        record(false);
        await route.continue();
        return;
      }
      const response = await route.fetch({ maxRedirects: 0 });
      if (response.status() >= 300 && response.status() < 400) {
        // An unexpected redirect: the browser requests it again and follows
        // it; expectRuleOnLoadedDocument then reports the unrouted page.
        record(false);
        await route.continue();
        return;
      }
      const headers = response.headers();
      const existing = headers[header];
      const policy = options.mode === 'report' ? TEST_REPORT_POLICY : TRUSTED_TYPES_RULE;
      headers[header] = existing ? `${existing}, ${policy}` : policy;
      record(true);
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
  // A report-only violation is also logged to the console by some browsers
  // (WebKit: "[Report Only] This requires a TrustedHTML value ..."). Such a
  // line is kept apart: trustedTypesFindings pairs it with a report-only
  // violation the probe recorded and the test allows, and reports it as an
  // error only when it has no such pair.
  const notices: string[] = [];
  noticesByPage.set(page, notices);
  page.on('console', (msg) => {
    if (msg.type() !== 'error' || !namesTrustedType(msg.text())) return;
    if (isReportOnlyNotice(msg.text())) notices.push(msg.text());
    else errors.push(msg.text());
  });
  return errors;
}

const noticesByPage = new WeakMap<Page, string[]>();

/** A console line some browsers log for a report-only violation. */
function isReportOnlyNotice(text: string): boolean {
  return /^\[Report Only\]|\(Report-Only policy\)/i.test(text);
}

/** The Trusted Type a console notice says is required, or null. */
function noticeType(text: string): string | null {
  return /Trusted ?(ScriptURL|Script|HTML)/i.exec(text)?.[1]?.toLowerCase() ?? null;
}

/** The Trusted Type a violation's sink requires, from its sample. */
function sinkType(sample: string): string {
  const sink = sample.split('|')[0] ?? '';
  if (/\bsrc\b|ScriptURL|importScripts|Worker/i.test(sink)) return 'scripturl';
  if (/eval|Function|setTimeout|setInterval|HTMLScriptElement|text$|textContent/i.test(sink)) {
    return 'script';
  }
  return 'html';
}

/**
 * The findings so far: page-side violations not matched by `allowed`, and
 * the collected errors, plus each report-only console notice that does not
 * pair with an allowed report-only violation of the Trusted Type it names.
 * Also returns how many violations each allowance matched, so a test can
 * check an allowance is still needed, and how many notices were seen and
 * paired.
 */
export async function trustedTypesFindings(
  page: Page,
  errors: string[],
  allowed: AllowedViolation[] = []
): Promise<
  TrustedTypesFindings & {
    allowedCounts: Record<string, number>;
    /** Report-only console notices: seen, and paired with an allowed violation. */
    notices: { seen: number; matched: number };
  }
> {
  // Copied before the first await, so the result matches the lists as they
  // were when the caller asked: an error or notice logged while the page is
  // read does not appear in this result.
  const collected = [...errors];
  const notices = [...(noticesByPage.get(page) ?? [])];
  const all = await page.evaluate(() => [...(window.__trustedTypesViolations ?? [])]);
  const allowedCounts: Record<string, number> = Object.fromEntries(
    allowed.map((a) => [a.reason, 0])
  );
  const violations: string[] = [];
  // Allowed report-only violations, by the Trusted Type their sink requires,
  // each available to pair with one console notice.
  const allowedReports: string[] = [];
  for (const v of all) {
    const match = allowed.find(
      (a) => a.sample.test(v.sample) && (!a.sourceFile || a.sourceFile.test(v.sourceFile))
    );
    if (match) {
      allowedCounts[match.reason] = (allowedCounts[match.reason] ?? 0) + 1;
      if (v.disposition === 'report') allowedReports.push(sinkType(v.sample));
    } else violations.push(`${v.disposition} ${v.sample} ${v.sourceFile}:${v.lineNumber}`);
  }
  const unmatched: string[] = [];
  for (const text of notices) {
    const type = noticeType(text);
    // A notice that names no Trusted Type cannot be paired, so it is an error.
    const at = type === null ? -1 : allowedReports.indexOf(type);
    if (text.includes('require-trusted-types-for') && at >= 0) allowedReports.splice(at, 1);
    else unmatched.push(text);
  }
  return {
    violations,
    errors: [...collected, ...unmatched],
    allowedCounts,
    notices: { seen: notices.length, matched: notices.length - unmatched.length },
  };
}

/**
 * Asserts there are no findings: no violation outside `allowed` and no
 * error, a report-only console notice counting as an error unless it pairs
 * with an allowed report-only violation. Returns how many violations each
 * allowance matched.
 */
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
 * throws when enforced), reported by the test's own policy. A page served
 * by the hub also carries the hub's own report-only rule, so the check
 * matches the test's policy only: originalPolicy exactly TRUSTED_TYPES_RULE
 * when enforced (the hub sends no enforced rule), and in report mode a
 * policy matchesTestReportPolicy accepts (the rule plus a report-uri to the
 * test's endpoint, which the hub's policy has not). Run it after the page's
 * own assertions, so their empty lists are not vacuous.
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
  const ownPolicy = (policy: string): boolean =>
    options.mode === 'report' ? matchesTestReportPolicy(policy) : policy === TRUSTED_TYPES_RULE;
  const fresh = (): Promise<
    Pick<TrustedTypesViolation, 'disposition' | 'originalPolicy' | 'sample'>[]
  > =>
    page.evaluate(
      (from) =>
        (window.__trustedTypesViolations ?? [])
          .slice(from)
          .map(({ disposition, originalPolicy, sample }) => ({
            disposition,
            originalPolicy,
            sample,
          })),
      before
    );
  const mine = async (): Promise<number> =>
    (await fresh()).filter(
      (v) =>
        v.disposition === disposition &&
        ownPolicy(v.originalPolicy) &&
        v.sample.startsWith('Element innerHTML')
    ).length;
  await expect
    .poll(mine)
    .toBeGreaterThanOrEqual(1)
    .catch(async (e) => {
      const policies = [
        ...new Set((await fresh()).map((v) => `${v.disposition}: ${v.originalPolicy}`)),
      ];
      throw new Error(`${String(e)}\npolicies of the new violations: ${policies.join(' | ')}`);
    });
}

/**
 * Whether a reported policy is the test's report-only policy. Some browsers
 * (Firefox) report the policy text normalized, with the report endpoint as a
 * full URL ("...; report-uri http://host:port/__e2e-trusted-types-report"),
 * so it is matched by the rule (as its own directive) and a report-uri whose
 * path is the test's endpoint, which only the test's policy carries; the
 * hub's own report-only rule does not match.
 */
export function matchesTestReportPolicy(policy: string): boolean {
  // Directive names and keywords are case-insensitive; the endpoint path is not.
  const directives = policy.split(';').map((d) => d.trim().split(/\s+/));
  const hasRule = directives.some((d) => d.join(' ').toLowerCase() === TRUSTED_TYPES_RULE);
  const reportsToTest = directives.some(
    ([name, ...values]) =>
      name?.toLowerCase() === 'report-uri' &&
      values.some((value) => new URL(value, 'http://x').pathname === TEST_REPORT_PATH)
  );
  return hasRule && reportsToTest;
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

/**
 * Asserts that the document the page has loaded was routed and given the
 * rule. It fails when the page was reached through a redirect the browser
 * followed (the end of a redirect chain is not routed), so a passing check
 * cannot come from the hub's own report-only rule alone.
 */
export async function expectRuleOnLoadedDocument(page: Page): Promise<void> {
  const routed = routedByPage.get(page);
  if (!routed) throw new Error('expectRuleOnLoadedDocument: call enforceTrustedTypes first');
  const loaded = await page.evaluate(
    () => (performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming).name
  );
  // The page's latest document request decides. After a redirect the
  // browser followed, the latest request is the redirect (its end is not
  // routed), so an earlier routed load of the same URL cannot match.
  const latest = routed.at(-1);
  expect(
    latest,
    `the loaded document ${loaded} is not the page's latest routed request; requests: ${routed
      .map((d) => `${d.url} (${d.withRule ? 'with rule' : 'passed through'})`)
      .join(', ')}`
  ).toEqual({ url: loaded, withRule: true });
}
