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

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it, expect, beforeAll, afterEach } from 'vitest';
import type { IntegrationReadiness, ScionIntegrationReadiness } from './integration-readiness.js';

const THIS_DIR = dirname(fileURLToPath(import.meta.url));
const COMPONENT_SOURCE_PATH = resolve(THIS_DIR, 'integration-readiness.ts');

describe('scion-integration-readiness', () => {
  beforeAll(async () => {
    await import('./integration-readiness.js');
  });

  afterEach(() => {
    document.body.innerHTML = '';
  });

  async function mount(props: {
    readiness: IntegrationReadiness | null;
    compact?: boolean;
    label?: string;
    linkHref?: string;
    linkLabel?: string;
  }): Promise<ScionIntegrationReadiness> {
    const el = document.createElement('scion-integration-readiness') as ScionIntegrationReadiness;
    el.readiness = props.readiness;
    if (props.compact !== undefined) el.compact = props.compact;
    if (props.label !== undefined) el.label = props.label;
    if (props.linkHref !== undefined) el.linkHref = props.linkHref;
    if (props.linkLabel !== undefined) el.linkLabel = props.linkLabel;
    document.body.appendChild(el);
    await el.updateComplete;
    return el;
  }

  it('renders nothing when readiness is null', async () => {
    const el = await mount({ readiness: null });
    expect(el.shadowRoot?.querySelector('[data-testid="integration-readiness-block"]')).toBeNull();
    expect(
      el.shadowRoot?.querySelector('[data-testid="integration-readiness-summary"]')
    ).toBeNull();
  });

  it('renders ready state in full block mode', async () => {
    const el = await mount({
      readiness: {
        ready: true,
        missing_events: [],
        missing_permissions: [],
      },
    });
    const block = el.shadowRoot!.querySelector('[data-testid="integration-readiness-block"]')!;
    expect(block.getAttribute('data-state')).toBe('ready');
    const badge = el.shadowRoot!.querySelector('[data-testid="integration-readiness-badge"]')!;
    expect(badge.getAttribute('variant')).toBe('success');
    expect(badge.textContent?.trim()).toBe('Ready');
    expect(
      el.shadowRoot!.querySelector('[data-testid="integration-readiness-missing-events"]')
    ).toBeNull();
    expect(
      el.shadowRoot!.querySelector('[data-testid="integration-readiness-missing-permissions"]')
    ).toBeNull();
    expect(el.shadowRoot!.querySelector('[data-testid="integration-readiness-error"]')).toBeNull();
  });

  it('renders missing events and missing permissions in full block mode', async () => {
    const el = await mount({
      readiness: {
        ready: false,
        missing_events: ['issue_comment', 'pull_request'],
        missing_permissions: ['issues:write'],
      },
    });
    const block = el.shadowRoot!.querySelector('[data-testid="integration-readiness-block"]')!;
    expect(block.getAttribute('data-state')).toBe('missing');
    const badge = el.shadowRoot!.querySelector('[data-testid="integration-readiness-badge"]')!;
    expect(badge.getAttribute('variant')).toBe('warning');
    expect(badge.textContent?.trim()).toBe('Not ready');

    const eventsEl = el.shadowRoot!.querySelector(
      '[data-testid="integration-readiness-missing-events"]'
    )!;
    expect(eventsEl.textContent).toContain('issue_comment');
    expect(eventsEl.textContent).toContain('pull_request');

    const permsEl = el.shadowRoot!.querySelector(
      '[data-testid="integration-readiness-missing-permissions"]'
    )!;
    expect(permsEl.textContent).toContain('issues:write');
  });

  it('renders fallback not-ready message when not ready with no listed missing items', async () => {
    const el = await mount({
      readiness: {
        ready: false,
        missing_events: [],
        missing_permissions: [],
      },
    });
    const block = el.shadowRoot!.querySelector('[data-testid="integration-readiness-block"]')!;
    expect(block.getAttribute('data-state')).toBe('missing');
    expect(
      el.shadowRoot!.querySelector('[data-testid="integration-readiness-not-ready"]')?.textContent
    ).toContain('not ready');
  });

  it('renders error state in full block mode', async () => {
    const el = await mount({
      readiness: {
        ready: false,
        missing_events: [],
        missing_permissions: [],
        error: 'Failed to fetch metadata.',
      },
    });
    const block = el.shadowRoot!.querySelector('[data-testid="integration-readiness-block"]')!;
    expect(block.getAttribute('data-state')).toBe('error');
    const badge = el.shadowRoot!.querySelector('[data-testid="integration-readiness-badge"]')!;
    expect(badge.getAttribute('variant')).toBe('danger');
    expect(badge.textContent?.trim()).toBe('Error');
    expect(
      el
        .shadowRoot!.querySelector('[data-testid="integration-readiness-error"]')
        ?.textContent?.trim()
    ).toBe('Failed to fetch metadata.');
  });

  it('renders compact one-line summary for ready, missing, and error states with link', async () => {
    const readyEl = await mount({
      compact: true,
      label: 'Triggers',
      linkHref: '/projects/p1/github?tab=automation',
      linkLabel: 'Automation →',
      readiness: { ready: true, missing_events: [], missing_permissions: [] },
    });
    const readySummary = readyEl.shadowRoot!.querySelector(
      '[data-testid="integration-readiness-summary"]'
    )!;
    expect(readySummary.getAttribute('data-state')).toBe('ready');
    expect(
      readyEl.shadowRoot!.querySelector('[data-testid="integration-readiness-summary-text"]')
        ?.textContent
    ).toBe('Ready');
    const link = readyEl.shadowRoot!.querySelector(
      '[data-testid="integration-readiness-link"]'
    ) as HTMLAnchorElement;
    expect(link).not.toBeNull();
    expect(link.getAttribute('href')).toBe('/projects/p1/github?tab=automation');
    expect(link.textContent?.trim()).toBe('Automation →');
    readyEl.remove();

    const missingEl = await mount({
      compact: true,
      readiness: {
        ready: false,
        missing_events: ['issue_comment', 'pull_request'],
        missing_permissions: ['issues:write'],
      },
    });
    expect(
      missingEl
        .shadowRoot!.querySelector('[data-testid="integration-readiness-summary"]')
        ?.getAttribute('data-state')
    ).toBe('missing');
    expect(
      missingEl.shadowRoot!.querySelector('[data-testid="integration-readiness-summary-text"]')
        ?.textContent
    ).toBe('Missing 2 events, 1 permission');
    missingEl.remove();

    const errorEl = await mount({
      compact: true,
      readiness: {
        ready: false,
        missing_events: [],
        missing_permissions: [],
        error: 'Credentials invalid.',
      },
    });
    expect(
      errorEl
        .shadowRoot!.querySelector('[data-testid="integration-readiness-summary"]')
        ?.getAttribute('data-state')
    ).toBe('error');
    expect(
      errorEl.shadowRoot!.querySelector('[data-testid="integration-readiness-summary-text"]')
        ?.textContent
    ).toBe('Error: Credentials invalid.');
  });

  it('contains no GitHub identifiers (provider-neutrality guard)', () => {
    const src = readFileSync(COMPONENT_SOURCE_PATH, 'utf-8');
    const forbidden = /\b(github\w*|octokit\w*|ghes\w*)\b|github/gi;
    const matches = src.match(forbidden) ?? [];
    expect(matches, 'integration-readiness.ts must remain provider-neutral').toEqual([]);
  });
});
