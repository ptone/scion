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
 * Tests for admin-quotas.ts's usage-detail rendering (ptone/scion#2061 P2.2,
 * design.md Amendment A1).
 *
 * The backend does not produce brokerAgentLimitSource: "not_enforced" yet
 * (P1b, ptone/scion#2270, is a separate in-progress PR) — this test stubs
 * the usage-by-limit response with that source to pin the rendering
 * contract ahead of the backend wiring: a broker-scoped reservation whose
 * cap is not currently enforced must show a visible "not enforced" marker
 * next to the cap in the Active Usage detail panel.
 */

import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import {
  resetServerFlagStateForTests,
  setFeatureFlag,
  TOKENOMICS_TOKEN_BUDGETS_FLAG,
  TOKENOMICS_USAGE_LEDGER_FLAG,
} from '../../utils/feature-flags.js';

const LIMIT_ID = 'limit-max-agents-per-broker';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const LIMIT_DEFINITION = {
  id: LIMIT_ID,
  name: 'max_agents_per_broker',
  resourceType: 'agent',
  unit: 'count',
  description: 'Max concurrent agents per broker',
  defaultValue: 30,
  system: true,
  createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(),
};

function makeReservation(source: string) {
  return {
    id: 'res-1',
    limitDefinitionId: LIMIT_ID,
    subjectId: 'broker-1',
    scopeType: 'broker',
    scopeId: 'broker-1',
    resourceId: 'agent-1',
    reserved: 1,
    createdAt: new Date().toISOString(),
    brokerAgentLimit: 30,
    brokerAgentLimitSource: source,
  };
}

function createFetchHandler(reservation: ReturnType<typeof makeReservation>) {
  return (url: string | URL | Request): Promise<Response> => {
    const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
    if (path.endsWith('/api/v1/admin/limits')) {
      return Promise.resolve(jsonResponse({ items: [LIMIT_DEFINITION] }));
    }
    if (path.endsWith('/api/v1/admin/usage')) {
      return Promise.resolve(
        jsonResponse({ items: [{ limitDefinition: LIMIT_DEFINITION, activeCount: 1 }] })
      );
    }
    if (path.endsWith(`/api/v1/admin/limits/${LIMIT_ID}/entitlements`)) {
      return Promise.resolve(jsonResponse({ items: [] }));
    }
    if (path.endsWith(`/api/v1/admin/usage/${LIMIT_ID}`)) {
      return Promise.resolve(
        jsonResponse({
          limitDefinition: LIMIT_DEFINITION,
          reservations: [reservation],
          totalActive: 1,
        })
      );
    }
    return Promise.resolve(jsonResponse({}, 404));
  };
}

async function mountAdminQuotasPage(
  reservation: ReturnType<typeof makeReservation>
): Promise<HTMLElement & { updateComplete: Promise<boolean> }> {
  vi.stubGlobal('fetch', vi.fn(createFetchHandler(reservation)));
  const el = document.createElement('scion-page-admin-quotas') as HTMLElement & {
    updateComplete: Promise<boolean>;
  };
  document.body.appendChild(el);
  await el.updateComplete;
  await new Promise((resolve) => setTimeout(resolve, 20));
  await el.updateComplete;
  return el;
}

async function expandFirstRow(
  element: HTMLElement & { updateComplete: Promise<boolean> }
): Promise<void> {
  const row = element.shadowRoot?.querySelector('tr.clickable');
  expect(row).not.toBeNull();
  (row as HTMLElement).click();
  await element.updateComplete;
  await new Promise((resolve) => setTimeout(resolve, 20));
  await element.updateComplete;
}

describe('scion-page-admin-quotas — not_enforced marker (design.md Amendment A1)', () => {
  let element: (HTMLElement & { updateComplete: Promise<boolean> }) | null = null;

  beforeAll(async () => {
    await import('./admin-quotas.js');
  }, 60_000);

  afterEach(() => {
    element?.remove();
    element = null;
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('shows a visible "not enforced" marker and keeps the cap value when the source is not_enforced', async () => {
    // Stubbed: the backend doesn't produce this source yet (P1b in progress).
    element = await mountAdminQuotasPage(makeReservation('not_enforced'));
    await expandFirstRow(element);

    const text = element.shadowRoot?.textContent?.replace(/\s+/g, ' ').trim() ?? '';
    expect(text).toContain('Broker cap:');
    // The value must be kept (A1: "keep the resolved value"), not omitted.
    expect(text).toContain('Broker cap: 30');
    expect(text.toLowerCase()).toContain('not enforced');

    const marker = element.shadowRoot?.querySelector('.not-enforced-marker');
    expect(marker).not.toBeNull();
    expect(marker?.textContent?.toLowerCase()).toContain('not enforced');
  });

  it('does not show the marker for an enforced source (hub_default)', async () => {
    element = await mountAdminQuotasPage(makeReservation('hub_default'));
    await expandFirstRow(element);

    const text = element.shadowRoot?.textContent?.replace(/\s+/g, ' ').trim() ?? '';
    expect(text).toContain('Broker cap: 30');
    expect(text).toContain('(hub_default)');
    expect(text.toLowerCase()).not.toContain('not enforced');
    expect(element.shadowRoot?.querySelector('.not-enforced-marker')).toBeNull();
  });
});

// ptone/scion#6033: max_agents_per_project's summary count is a sum across
// every project, while its cap applies per project, so the row shows the
// count only, labelled, never an "n / cap" fraction or a progress bar.
describe('scion-page-admin-quotas — per-project limit row', () => {
  const PROJECT_LIMIT = {
    ...LIMIT_DEFINITION,
    id: 'limit-max-agents-per-project',
    name: 'max_agents_per_project',
    description: 'Maximum agents per project',
    defaultValue: 5,
  };
  let element: (HTMLElement & { updateComplete: Promise<boolean> }) | null = null;

  beforeAll(async () => {
    await import('./admin-quotas.js');
  }, 60_000);

  afterEach(() => {
    element?.remove();
    element = null;
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('shows the hub-wide count with the per-project label and no fraction', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string | URL | Request): Promise<Response> => {
        const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
        if (path.endsWith('/api/v1/admin/limits')) {
          return Promise.resolve(jsonResponse({ items: [PROJECT_LIMIT] }));
        }
        if (path.endsWith('/api/v1/admin/usage')) {
          return Promise.resolve(
            jsonResponse({ items: [{ limitDefinition: PROJECT_LIMIT, activeCount: 31 }] })
          );
        }
        return Promise.resolve(jsonResponse({}, 404));
      })
    );
    const el = document.createElement('scion-page-admin-quotas') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    element = el;
    document.body.appendChild(el);
    await el.updateComplete;
    await new Promise((resolve) => setTimeout(resolve, 20));
    await el.updateComplete;

    const row = el.shadowRoot?.querySelector('tr.clickable');
    expect(row).not.toBeNull();
    const text = row?.textContent?.replace(/\s+/g, ' ').trim() ?? '';
    expect(text).toContain('31');
    expect(text).toContain("across all projects; cap applies per project (creator's resolved cap)");
    expect(text).not.toContain('31 / 5');
    expect(row?.querySelector('sl-progress-bar')).toBeNull();
  });
});

describe('scion-page-admin-quotas — llm_tokens limits & entitlement edit (ptone/scion#9243)', () => {
  const TOKEN_LIMIT = {
    id: 'limit-tokens-proj-day',
    name: 'llm_tokens_per_project_day',
    resourceType: 'llm_tokens',
    unit: 'tokens',
    description: 'Daily raw token budget per project',
    defaultValue: 0,
    system: true,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  };

  const GROUP_POOL_LIMIT = {
    id: 'limit-tokens-group-month',
    name: 'llm_tokens_per_group_month',
    resourceType: 'llm_tokens',
    unit: 'tokens',
    description: 'Monthly pooled raw token budget per group',
    defaultValue: 0,
    system: true,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  };

  const BINDING_NO_ACTION = {
    id: 'bind-1',
    limitDefinitionId: TOKEN_LIMIT.id,
    subjectType: 'system_default',
    subjectId: '',
    scopeType: 'project',
    scopeId: 'proj-1',
    value: 5_000_000,
    tokenAction: null,
    createdBy: 'admin@example.com',
    createdAt: new Date().toISOString(),
    updatedAt: '2026-11-28T10:00:00Z',
  };

  let element: (HTMLElement & { updateComplete: Promise<boolean> }) | null = null;

  beforeAll(async () => {
    await import('./admin-quotas.js');
  }, 60_000);

  afterEach(() => {
    element?.remove();
    element = null;
    delete window.__SCION_FEATURES__;
    resetServerFlagStateForTests();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('hides llm_tokens limits when tokenomics.token_budgets is off', async () => {
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, false);

    vi.stubGlobal(
      'fetch',
      vi.fn((url: string | URL | Request): Promise<Response> => {
        const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
        if (path.endsWith('/api/v1/admin/limits')) {
          return Promise.resolve(jsonResponse({ items: [LIMIT_DEFINITION, TOKEN_LIMIT] }));
        }
        if (path.endsWith('/api/v1/admin/usage')) {
          return Promise.resolve(jsonResponse({ items: [] }));
        }
        return Promise.resolve(jsonResponse({}, 404));
      })
    );

    element = document.createElement('scion-page-admin-quotas') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);
    await vi.waitFor(() => {
      expect(element?.shadowRoot?.querySelectorAll('tr.clickable').length).toBe(1);
    });
    expect(element.shadowRoot?.textContent).not.toContain('llm_tokens_per_project_day');
  });

  it('edits an llm_tokens binding sending explicit pause_new tokenAction and If-Match from list updatedAt, and handles 412', async () => {
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, true);

    const putCalls: Array<{
      url: string;
      headers: Record<string, string>;
      body: Record<string, unknown>;
    }> = [];
    let putCount = 0;

    vi.stubGlobal(
      'fetch',
      vi.fn((url: string | URL | Request, init?: RequestInit): Promise<Response> => {
        const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
        const method = (init?.method || 'GET').toUpperCase();
        if (path.endsWith('/api/v1/admin/limits')) {
          return Promise.resolve(jsonResponse({ items: [TOKEN_LIMIT, GROUP_POOL_LIMIT] }));
        }
        if (path.endsWith('/api/v1/admin/usage')) {
          return Promise.resolve(jsonResponse({ items: [] }));
        }
        if (path.endsWith(`/api/v1/admin/limits/${TOKEN_LIMIT.id}/entitlements`)) {
          return Promise.resolve(jsonResponse({ items: [BINDING_NO_ACTION] }));
        }
        if (path.endsWith(`/api/v1/admin/usage/${TOKEN_LIMIT.id}`)) {
          return Promise.resolve(
            jsonResponse({ limitDefinition: TOKEN_LIMIT, reservations: [], totalActive: 0 })
          );
        }
        if (
          path.endsWith(`/api/v1/admin/entitlements/${BINDING_NO_ACTION.id}`) &&
          method === 'PUT'
        ) {
          putCount++;
          putCalls.push({
            url: path,
            headers: (init?.headers as Record<string, string>) || {},
            body: JSON.parse(typeof init?.body === 'string' ? init.body : '{}') as Record<
              string,
              unknown
            >,
          });
          if (putCount === 1) {
            return Promise.resolve(jsonResponse({ error: 'revision mismatch' }, 412));
          }
          return Promise.resolve(jsonResponse({ ...BINDING_NO_ACTION, tokenAction: 'pause_new' }));
        }
        return Promise.resolve(jsonResponse({}, 404));
      })
    );

    element = document.createElement('scion-page-admin-quotas') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);

    await vi.waitFor(() => {
      expect(element?.shadowRoot?.querySelectorAll('tr.clickable').length).toBe(2);
    });

    // Expand first row (TOKEN_LIMIT)
    const rows = element.shadowRoot?.querySelectorAll('tr.clickable');
    (rows?.[0] as HTMLElement).click();
    await vi.waitFor(() => {
      expect(
        element?.shadowRoot?.querySelector('[data-testid="edit-entitlement-btn"]')
      ).not.toBeNull();
    });

    // Action badge defaults to Pause new launches when stored tokenAction is null
    expect(
      element.shadowRoot
        ?.querySelector('[data-testid="entitlement-token-action"]')
        ?.textContent?.trim()
    ).toBe('Pause new launches');

    // Click edit
    (
      element.shadowRoot?.querySelector('[data-testid="edit-entitlement-btn"]') as HTMLElement
    ).click();
    await vi.waitFor(() => {
      const sel = element?.shadowRoot?.querySelector(
        '[data-testid="entitlement-token-action-select"]'
      ) as (HTMLElement & { value?: string }) | null;
      expect(sel?.getAttribute('value')).toBe('pause_new');
    });

    // Submit -> hits 412, verifies If-Match derived from list updatedAt and explicit pause_new tokenAction
    (
      element.shadowRoot?.querySelector('[data-testid="entitlement-submit-btn"]') as HTMLElement
    ).click();
    await vi.waitFor(() => {
      expect(putCalls.length).toBe(1);
      expect(
        element?.shadowRoot?.querySelector('[data-testid="entitlement-dialog-error"]')?.textContent
      ).toContain('412');
    });

    expect(putCalls[0].headers['If-Match']).toBe('"2026-11-28T10:00:00Z"');
    expect(putCalls[0].body.tokenAction).toBe('pause_new');
    expect(putCalls[0].body.subjectType).toBe('system_default');
    expect(putCalls[0].body.subjectId).toBe('');
  });

  it('locks subject type to group and shows non-additive notice for pooled group limits', async () => {
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, true);

    vi.stubGlobal(
      'fetch',
      vi.fn((url: string | URL | Request): Promise<Response> => {
        const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
        if (path.endsWith('/api/v1/admin/limits')) {
          return Promise.resolve(jsonResponse({ items: [GROUP_POOL_LIMIT] }));
        }
        if (path.endsWith('/api/v1/admin/usage')) {
          return Promise.resolve(jsonResponse({ items: [] }));
        }
        if (path.endsWith(`/api/v1/admin/limits/${GROUP_POOL_LIMIT.id}/entitlements`)) {
          return Promise.resolve(jsonResponse({ items: [] }));
        }
        if (path.endsWith(`/api/v1/admin/usage/${GROUP_POOL_LIMIT.id}`)) {
          return Promise.resolve(
            jsonResponse({ limitDefinition: GROUP_POOL_LIMIT, reservations: [], totalActive: 0 })
          );
        }
        return Promise.resolve(jsonResponse({}, 404));
      })
    );

    element = document.createElement('scion-page-admin-quotas') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);

    await vi.waitFor(() => {
      expect(element?.shadowRoot?.querySelector('tr.clickable')).not.toBeNull();
    });
    (element.shadowRoot?.querySelector('tr.clickable') as HTMLElement).click();
    await vi.waitFor(() => {
      expect(
        element?.shadowRoot?.querySelector('[data-testid="add-entitlement-btn"]')
      ).not.toBeNull();
    });
    (
      element.shadowRoot?.querySelector('[data-testid="add-entitlement-btn"]') as HTMLElement
    ).click();
    await element.updateComplete;

    expect(
      element.shadowRoot?.querySelector('[data-testid="pooled-group-binding-notice"]')?.textContent
    ).toContain('non-additive');
    const subjType = element.shadowRoot?.querySelector('[data-testid="entitlement-subject-type"]');
    expect(subjType?.getAttribute('value')).toBe('group');
    expect(subjType?.hasAttribute('disabled')).toBe(true);
  });

  it('sends list updatedAt as If-Match (preserving microseconds) so a concurrent admin update causes 412 instead of silent revert, while keeping typed edits', async () => {
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, true);

    const INITIAL_BINDING = {
      ...BINDING_NO_ACTION,
      value: 5_000_000,
      updatedAt: '2026-11-28T10:00:00.123456Z',
    };
    const CONCURRENT_BINDING = {
      ...BINDING_NO_ACTION,
      value: 7_000_000,
      tokenAction: 'notify_only',
      updatedAt: '2026-11-28T11:00:00.654321Z',
    };

    // Simulate another admin modifying the binding on the Hub after the list loaded.
    let serverBinding = INITIAL_BINDING;
    let getByIdCalls = 0;
    const putCalls: Array<{ headers: Record<string, string>; body: Record<string, unknown> }> = [];

    vi.stubGlobal(
      'fetch',
      vi.fn((url: string | URL | Request, init?: RequestInit): Promise<Response> => {
        const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
        const method = (init?.method || 'GET').toUpperCase();
        if (path.endsWith('/api/v1/admin/limits')) {
          return Promise.resolve(jsonResponse({ items: [TOKEN_LIMIT] }));
        }
        if (path.endsWith('/api/v1/admin/usage')) {
          return Promise.resolve(jsonResponse({ items: [] }));
        }
        if (path.endsWith(`/api/v1/admin/limits/${TOKEN_LIMIT.id}/entitlements`)) {
          return Promise.resolve(jsonResponse({ items: [serverBinding] }));
        }
        if (path.endsWith(`/api/v1/admin/usage/${TOKEN_LIMIT.id}`)) {
          return Promise.resolve(
            jsonResponse({ limitDefinition: TOKEN_LIMIT, reservations: [], totalActive: 0 })
          );
        }
        if (path.endsWith(`/api/v1/admin/entitlements/${INITIAL_BINDING.id}`) && method === 'GET') {
          getByIdCalls++;
          return Promise.resolve(
            new Response(JSON.stringify(serverBinding), {
              status: 200,
              headers: {
                'Content-Type': 'application/json',
                ETag: `"${serverBinding.updatedAt}"`,
              },
            })
          );
        }
        if (path.endsWith(`/api/v1/admin/entitlements/${INITIAL_BINDING.id}`) && method === 'PUT') {
          const headers = (init?.headers as Record<string, string>) || {};
          const body = JSON.parse(typeof init?.body === 'string' ? init.body : '{}') as Record<
            string,
            unknown
          >;
          putCalls.push({ headers, body });
          if (headers['If-Match'] !== `"${serverBinding.updatedAt}"`) {
            return Promise.resolve(jsonResponse({ error: 'revision mismatch' }, 412));
          }
          return Promise.resolve(jsonResponse({ ...serverBinding, ...body }));
        }
        return Promise.resolve(jsonResponse({}, 404));
      })
    );

    element = document.createElement('scion-page-admin-quotas') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);

    await vi.waitFor(() => {
      expect(element?.shadowRoot?.querySelector('tr.clickable')).not.toBeNull();
    });
    (element.shadowRoot?.querySelector('tr.clickable') as HTMLElement).click();
    await vi.waitFor(() => {
      expect(
        element?.shadowRoot?.querySelector('[data-testid="edit-entitlement-btn"]')
      ).not.toBeNull();
    });

    // Concurrent admin updates the binding on the Hub before this admin clicks Edit
    serverBinding = CONCURRENT_BINDING;

    (
      element.shadowRoot?.querySelector('[data-testid="edit-entitlement-btn"]') as HTMLElement
    ).click();
    await element.updateComplete;

    // Admin types a new value (not overwritten by any background GET)
    const valueInput = element.shadowRoot?.querySelector(
      '[data-testid="entitlement-value"]'
    ) as HTMLInputElement;
    valueInput.value = '9000000';
    valueInput.dispatchEvent(new CustomEvent('sl-input', { bubbles: true }));
    await element.updateComplete;

    // Submit: must carry the list row's revision (10:00:00.123456Z), NOT the
    // concurrent server revision, so the Hub returns 412 and never reverts.
    (
      element.shadowRoot?.querySelector('[data-testid="entitlement-submit-btn"]') as HTMLElement
    ).click();
    await vi.waitFor(() => {
      expect(putCalls.length).toBe(1);
      expect(
        element?.shadowRoot?.querySelector('[data-testid="entitlement-dialog-error"]')?.textContent
      ).toContain('412');
    });

    expect(getByIdCalls).toBe(0);
    expect(putCalls[0].headers['If-Match']).toBe('"2026-11-28T10:00:00.123456Z"');
    expect(putCalls[0].body.value).toBe(9_000_000);
    // The 412 handler refreshes the entitlements table with the concurrent 7M / notify_only binding.
    await vi.waitFor(() => {
      expect(
        element?.shadowRoot
          ?.querySelector('[data-testid="entitlement-token-action"]')
          ?.textContent?.trim()
      ).toBe('Notify only');
    });
  });
});
