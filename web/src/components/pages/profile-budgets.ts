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
 * Profile Token Budgets page ("My Budgets").
 *
 * Fetches GET /api/v1/me/usage/budgets and displays the signed-in user's
 * personal (user) and per-project (user_project) raw-token budgets.
 * Gated behind tokenomics.token_budgets (and tokenomics.usage_ledger).
 */

import { LitElement, html, css, nothing, type TemplateResult } from 'lit';
import { customElement, state } from 'lit/decorators.js';

import { apiFetch, extractApiError } from '../../client/api.js';
import type { UsageBudgetEntry, UsageBudgetsResponse } from '../../shared/usage-budgets.js';
import { FEATURE_FLAGS_CHANGED_EVENT, tokenBudgetsEnabled } from '../../utils/feature-flags.js';
import '../shared/token-budgets-panel.js';

@customElement('scion-page-profile-budgets')
export class ScionPageProfileBudgets extends LitElement {
  @state()
  private enabled = tokenBudgetsEnabled();

  @state()
  private loading = false;

  @state()
  private error: string | null = null;

  @state()
  private budgets: UsageBudgetEntry[] = [];

  private boundOnFeatureFlagsChanged = (): void => {
    const next = tokenBudgetsEnabled();
    if (next !== this.enabled) {
      this.enabled = next;
      if (next) {
        void this.loadBudgets();
      } else {
        this.budgets = [];
        this.error = null;
      }
    }
  };

  static override styles = css`
    :host {
      display: block;
    }

    .page-header {
      margin-bottom: 1.5rem;
    }

    .page-header h1 {
      font-size: 1.5rem;
      font-weight: 700;
      color: var(--scion-text);
      margin: 0 0 0.25rem 0;
    }

    .page-header p {
      margin: 0;
      font-size: 0.875rem;
      color: var(--scion-text-muted);
    }

    .sections {
      display: flex;
      flex-direction: column;
      gap: 1.25rem;
    }
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    this.enabled = tokenBudgetsEnabled();
    window.addEventListener(FEATURE_FLAGS_CHANGED_EVENT, this.boundOnFeatureFlagsChanged);
    if (this.enabled) {
      void this.loadBudgets();
    }
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    window.removeEventListener(FEATURE_FLAGS_CHANGED_EVENT, this.boundOnFeatureFlagsChanged);
  }

  private async loadBudgets(): Promise<void> {
    if (!this.enabled) return;
    this.loading = true;
    this.error = null;

    try {
      const res = await apiFetch('/api/v1/me/usage/budgets');
      if (!this.isConnected || !this.enabled) return;
      if (!res.ok) {
        throw new Error(await extractApiError(res, `HTTP ${res.status}`));
      }
      const data = (await res.json()) as UsageBudgetsResponse;
      this.budgets = data.budgets ?? [];
    } catch (err) {
      if (!this.isConnected) return;
      this.error = err instanceof Error ? err.message : 'Failed to load token budgets';
    } finally {
      if (this.isConnected) {
        this.loading = false;
      }
    }
  }

  override render(): TemplateResult | typeof nothing {
    if (!this.enabled) {
      return nothing;
    }

    const userBudgets = this.budgets.filter((b) => b.scope_kind === 'user');
    const userProjectBudgets = this.budgets.filter((b) => b.scope_kind === 'user_project');

    return html`
      <div class="page-header" data-testid="profile-budgets-page">
        <h1>My Token Budgets</h1>
        <p>Current UTC day and month raw-token usage against your personal and per-project caps.</p>
      </div>

      <div class="sections">
        <scion-token-budgets-panel
          heading="Personal Budgets (All Projects)"
          emptyMessage="No personal token budgets configured."
          .budgets=${userBudgets}
          ?loading=${this.loading}
          .error=${this.error}
          @budgets-refresh=${() => this.loadBudgets()}
        ></scion-token-budgets-panel>

        ${!this.loading && !this.error && userProjectBudgets.length > 0
          ? html`
              <scion-token-budgets-panel
                heading="Per-Project Personal Budgets"
                emptyMessage="No per-project token budgets apply."
                .budgets=${userProjectBudgets}
                showProjectScope
                @budgets-refresh=${() => this.loadBudgets()}
              ></scion-token-budgets-panel>
            `
          : nothing}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-page-profile-budgets': ScionPageProfileBudgets;
  }
}
