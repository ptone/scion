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

import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import {
  resetServerFlagStateForTests,
  setFeatureFlag,
  TOKENOMICS_TOKEN_BUDGETS_FLAG,
  TOKENOMICS_USAGE_LEDGER_FLAG,
} from '../../utils/feature-flags.js';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('scion-page-profile-budgets', () => {
  let element: (HTMLElement & { updateComplete: Promise<boolean> }) | null = null;

  beforeAll(async () => {
    await import('./profile-budgets.js');
  }, 60_000);

  afterEach(() => {
    element?.remove();
    element = null;
    delete window.__SCION_FEATURES__;
    resetServerFlagStateForTests();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('stays hidden and does not fetch when tokenomics.token_budgets is off', async () => {
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, false);
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    element = document.createElement('scion-page-profile-budgets') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);
    await element.updateComplete;

    expect(fetchMock).not.toHaveBeenCalled();
    expect(element.shadowRoot?.querySelector('[data-testid="profile-budgets-page"]')).toBeNull();
  });

  it('fetches /api/v1/me/usage/budgets and renders personal and per-project sections when enabled', async () => {
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, true);
    const fetchMock = vi.fn(() =>
      Promise.resolve(
        jsonResponse({
          scope: { kind: 'user', id: 'u-1' },
          as_of: '2026-11-28T12:00:00Z',
          budgets: [
            {
              id: 'b-user',
              limit_definition_id: 'def-1',
              limit_name: 'llm_tokens_per_user_day',
              scope_kind: 'user',
              scope_id: 'u-1',
              window_start_utc: '2026-11-28T00:00:00Z',
              window_end_utc: '2026-11-29T00:00:00Z',
              cap: 500_000,
              token_action: 'pause_new',
              consumed: 100_000,
              state: 'ok',
              thresholds_notified: 0,
              override_until: null,
              state_version: 1,
              as_of: '2026-11-28T12:00:00Z',
            },
            {
              id: 'b-up',
              limit_definition_id: 'def-2',
              limit_name: 'llm_tokens_per_user_project_day',
              scope_kind: 'user_project',
              scope_id: 'u-1',
              project_scope_id: 'proj-alpha',
              window_start_utc: '2026-11-28T00:00:00Z',
              window_end_utc: '2026-11-29T00:00:00Z',
              cap: 200_000,
              token_action: 'notify_only',
              consumed: 180_000,
              state: 'warn',
              thresholds_notified: 1,
              override_until: null,
              state_version: 2,
              as_of: '2026-11-28T12:00:00Z',
            },
          ],
        })
      )
    );
    vi.stubGlobal('fetch', fetchMock);

    element = document.createElement('scion-page-profile-budgets') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);

    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith('/api/v1/me/usage/budgets', expect.anything());
      const panels = element?.shadowRoot?.querySelectorAll('scion-token-budgets-panel');
      expect(panels?.length).toBe(2);
    });
  });
});
