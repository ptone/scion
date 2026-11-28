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
 * Shared Token Budgets status panel component.
 *
 * Renders active raw-token budgets (project, user, user_project, or pooled
 * group), including state badges (ok / warn / exhausted), consumed vs cap,
 * UTC window end, breach action, active override status, optional override
 * controls (with state_version / If-Match CAS and 412/428 handling),
 * non-additive pool notices, and per-member breakdowns for group admins.
 */

import { LitElement, html, css, nothing, type TemplateResult } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

import { apiFetch, extractApiError } from '../../client/api.js';
import {
  budgetStateDisplay,
  budgetUsagePercent,
  formatLimitDisplayName,
  formatTokenAmount,
  formatTokenExact,
  formatUtcTimestamp,
  isOverrideActive,
  tokenActionBadgeVariant,
  tokenActionLabel,
  type GroupBudgetItem,
  type GroupBudgetMemberView,
  type UsageBudgetEntry,
} from '../../shared/usage-budgets.js';
import { formatNumber } from '../../utils/format-number.js';

export type BudgetListItem = UsageBudgetEntry | GroupBudgetItem;

function hasGroupFields(item: BudgetListItem): item is GroupBudgetItem {
  return 'non_additive' in item;
}

@customElement('scion-token-budgets-panel')
export class ScionTokenBudgetsPanel extends LitElement {
  @property({ type: Array })
  budgets: BudgetListItem[] = [];

  @property({ type: Boolean })
  loading = false;

  @property({ type: String })
  error: string | null = null;

  @property({ type: String })
  heading = 'Token Budgets';

  @property({ type: String })
  emptyMessage = 'No token budgets configured for this scope.';

  @property({ type: Boolean })
  nonAdditive = false;

  @property({ type: Boolean })
  canOverride = false;

  /**
   * Base path for overrides, e.g. `/api/v1/projects/${projectId}/usage/budgets`.
   * Calls `POST ${overrideEndpointBase}/${budget.id}:override`.
   */
  @property({ type: String })
  overrideEndpointBase = '';

  @property({ type: Boolean })
  showProjectScope = false;

  // --- Override dialog state ---
  @state()
  private overrideTarget: BudgetListItem | null = null;

  @state()
  private overrideUntilInput = '';

  @state()
  private overrideReason = '';

  @state()
  private overrideSaving = false;

  @state()
  private overrideError: string | null = null;

  @state()
  private overrideConflict = false;

  // --- Clear override state ---
  @state()
  private clearingBudgetId: string | null = null;

  @state()
  private inlineError: string | null = null;

  static override styles = css`
    :host {
      display: block;
    }

    .panel {
      background: var(--scion-surface);
      border: 1px solid var(--scion-border);
      border-radius: var(--scion-radius-lg);
      padding: 1.25rem;
    }

    .panel-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.75rem;
      margin-bottom: 1rem;
      flex-wrap: wrap;
    }

    .panel-title-row {
      display: flex;
      align-items: center;
      gap: 0.5rem;
    }

    .panel-title-row h2,
    .panel-title-row h3 {
      margin: 0;
      font-size: 1rem;
      font-weight: 600;
      color: var(--scion-text);
    }

    .panel-title-row sl-icon {
      color: var(--scion-text-muted);
      font-size: 1.125rem;
    }

    .non-additive-notice {
      margin-bottom: 1rem;
    }

    .inline-error {
      margin-bottom: 1rem;
    }

    .loading-state {
      display: flex;
      align-items: center;
      gap: 0.75rem;
      padding: 1.5rem 0;
      color: var(--scion-text-muted);
      font-size: 0.875rem;
    }

    .empty-state {
      padding: 1.5rem;
      text-align: center;
      color: var(--scion-text-muted);
      font-size: 0.875rem;
      background: var(--scion-bg-subtle);
      border-radius: var(--scion-radius);
    }

    .budget-list {
      display: flex;
      flex-direction: column;
      gap: 0.875rem;
    }

    .budget-card {
      background: var(--scion-bg-subtle);
      border: 1px solid var(--scion-border);
      border-radius: var(--scion-radius);
      padding: 1rem;
    }

    .budget-top {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      gap: 0.75rem;
      flex-wrap: wrap;
      margin-bottom: 0.625rem;
    }

    .budget-heading {
      display: flex;
      flex-direction: column;
      gap: 0.25rem;
    }

    .budget-name-row {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      flex-wrap: wrap;
    }

    .budget-name {
      font-weight: 600;
      font-size: 0.9375rem;
      color: var(--scion-text);
    }

    .budget-sub {
      font-size: 0.75rem;
      color: var(--scion-text-muted);
      font-family: var(--scion-font-mono);
    }

    .budget-actions {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      flex-wrap: wrap;
    }

    .usage-row {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: 0.75rem;
      flex-wrap: wrap;
      margin-bottom: 0.375rem;
      font-size: 0.875rem;
    }

    .usage-figures {
      font-variant-numeric: tabular-nums;
      color: var(--scion-text);
      font-weight: 500;
    }

    .usage-cap-muted {
      color: var(--scion-text-muted);
      font-weight: 400;
    }

    .usage-pct {
      font-size: 0.8125rem;
      color: var(--scion-text-muted);
      font-variant-numeric: tabular-nums;
    }

    sl-progress-bar {
      --height: 0.5rem;
      margin-bottom: 0.625rem;
    }

    sl-progress-bar.state-warn {
      --indicator-color: var(--sl-color-warning-500);
    }

    sl-progress-bar.state-exhausted {
      --indicator-color: var(--sl-color-danger-500);
    }

    sl-progress-bar.state-ok {
      --indicator-color: var(--sl-color-success-500);
    }

    .budget-meta {
      display: flex;
      flex-wrap: wrap;
      gap: 0.5rem 1.25rem;
      font-size: 0.8125rem;
      color: var(--scion-text-muted);
      align-items: center;
    }

    .meta-item {
      display: inline-flex;
      align-items: center;
      gap: 0.375rem;
    }

    .override-banner {
      margin-top: 0.625rem;
      padding: 0.5rem 0.75rem;
      border-radius: var(--scion-radius);
      background: var(--sl-color-primary-50);
      border: 1px solid var(--sl-color-primary-200);
      color: var(--scion-text);
      font-size: 0.8125rem;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.75rem;
      flex-wrap: wrap;
    }

    .members-section {
      margin-top: 0.875rem;
      padding-top: 0.75rem;
      border-top: 1px solid var(--scion-border);
    }

    .members-header {
      font-size: 0.75rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.04em;
      color: var(--scion-text-muted);
      margin-bottom: 0.5rem;
    }

    .members-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.8125rem;
    }

    .members-table th {
      text-align: left;
      padding: 0.375rem 0.5rem;
      color: var(--scion-text-muted);
      font-weight: 600;
      border-bottom: 1px solid var(--scion-border);
    }

    .members-table td {
      padding: 0.375rem 0.5rem;
      color: var(--scion-text);
      border-bottom: 1px solid var(--scion-border);
      font-variant-numeric: tabular-nums;
    }

    .members-table tr:last-child td {
      border-bottom: none;
    }

    .members-table .num {
      text-align: right;
    }

    .member-sub {
      display: block;
      font-size: 0.75rem;
      color: var(--scion-text-muted);
    }

    .members-empty {
      font-size: 0.8125rem;
      color: var(--scion-text-muted);
      padding: 0.25rem 0;
    }

    .preset-row {
      display: flex;
      flex-wrap: wrap;
      gap: 0.5rem;
      margin-bottom: 0.75rem;
    }

    .form-row {
      margin-bottom: 1rem;
    }

    .form-row:last-child {
      margin-bottom: 0;
    }

    .form-row sl-input {
      width: 100%;
    }

    .dialog-alert {
      margin-bottom: 1rem;
    }
  `;

  private requestRefresh(): void {
    this.dispatchEvent(
      new CustomEvent('budgets-refresh', {
        bubbles: true,
        composed: true,
      })
    );
  }

  private openOverrideDialog(budget: BudgetListItem): void {
    this.overrideTarget = budget;
    this.overrideError = null;
    this.overrideConflict = false;
    this.overrideReason = '';
    // Default to +1 hour from now, clamped to window_end_utc.
    const defaultUntil = this.computePresetIso(budget, 60);
    this.overrideUntilInput = defaultUntil;
  }

  private closeOverrideDialog(): void {
    this.overrideTarget = null;
    this.overrideError = null;
    this.overrideConflict = false;
    this.overrideSaving = false;
  }

  private computePresetIso(budget: BudgetListItem, minutesFromNow: number): string {
    const asOfMs = budget.as_of ? Date.parse(budget.as_of) : NaN;
    const nowMs = Number.isNaN(asOfMs) ? Date.now() : Math.max(Date.now(), asOfMs);
    const endMs = Date.parse(budget.window_end_utc);
    let targetMs = nowMs + minutesFromNow * 60_000;
    if (!Number.isNaN(endMs) && targetMs > endMs) {
      targetMs = endMs;
    }
    return new Date(targetMs).toISOString();
  }

  private async submitOverride(): Promise<void> {
    const budget = this.overrideTarget;
    if (!budget || !budget.id || !this.overrideEndpointBase) return;

    const rawUntil = this.overrideUntilInput.trim();
    if (!rawUntil) {
      this.overrideError = 'Override expiration timestamp (UTC ISO-8601) is required.';
      return;
    }
    const parsedMs = Date.parse(rawUntil);
    if (Number.isNaN(parsedMs)) {
      this.overrideError =
        'Invalid timestamp. Use ISO-8601 UTC format (e.g. 2026-11-28T23:59:59Z).';
      return;
    }

    this.overrideSaving = true;
    this.overrideError = null;
    this.overrideConflict = false;

    try {
      const payload: {
        override_until: string;
        state_version: number;
        reason?: string;
      } = {
        override_until: new Date(parsedMs).toISOString(),
        state_version: budget.state_version,
      };
      const trimmedReason = this.overrideReason.trim();
      if (trimmedReason) {
        payload.reason = trimmedReason;
      }

      const res = await apiFetch(
        `${this.overrideEndpointBase}/${encodeURIComponent(budget.id)}:override`,
        {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'If-Match': `"${budget.state_version}"`,
          },
          body: JSON.stringify(payload),
        }
      );

      if (res.status === 412) {
        this.overrideConflict = true;
        this.overrideError =
          'This budget changed concurrently (version conflict). Budgets have been refreshed — close and re-open Override to retry.';
        this.requestRefresh();
        return;
      }
      if (res.status === 428) {
        this.overrideError =
          'Budget state version is required to apply an override. Refreshing budgets.';
        this.requestRefresh();
        return;
      }
      if (!res.ok) {
        throw new Error(await extractApiError(res, `HTTP ${res.status}`));
      }

      this.closeOverrideDialog();
      this.requestRefresh();
    } catch (err) {
      this.overrideError = err instanceof Error ? err.message : 'Failed to apply override';
    } finally {
      this.overrideSaving = false;
    }
  }

  private async clearOverride(budget: BudgetListItem): Promise<void> {
    if (!budget.id || !this.overrideEndpointBase) return;
    this.clearingBudgetId = budget.id;
    this.inlineError = null;

    try {
      const res = await apiFetch(
        `${this.overrideEndpointBase}/${encodeURIComponent(budget.id)}:override`,
        {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'If-Match': `"${budget.state_version}"`,
          },
          body: JSON.stringify({
            override_until: null,
            state_version: budget.state_version,
          }),
        }
      );

      if (res.status === 412) {
        this.inlineError =
          'Budget version changed while clearing override (412 conflict). Budgets have been refreshed — please retry.';
        this.requestRefresh();
        return;
      }
      if (res.status === 428) {
        this.inlineError =
          'Budget version precondition required (428). Budgets have been refreshed — please retry.';
        this.requestRefresh();
        return;
      }
      if (!res.ok) {
        throw new Error(await extractApiError(res, `HTTP ${res.status}`));
      }

      this.requestRefresh();
    } catch (err) {
      this.inlineError = err instanceof Error ? err.message : 'Failed to clear override';
    } finally {
      this.clearingBudgetId = null;
    }
  }

  private renderMemberBreakdown(members: GroupBudgetMemberView[]): TemplateResult {
    return html`
      <div class="members-section" data-testid="budget-member-breakdown">
        <div class="members-header">Per-Member Spend Breakdown</div>
        ${members.length === 0
          ? html`<div class="members-empty">No member usage recorded in this window.</div>`
          : html`
              <table class="members-table">
                <thead>
                  <tr>
                    <th>Member</th>
                    <th class="num">Tokens</th>
                    <th class="num">API Calls</th>
                    <th class="num">Events</th>
                  </tr>
                </thead>
                <tbody>
                  ${members.map(
                    (m) => html`
                      <tr>
                        <td>
                          <span>${m.display_name || m.email || m.user_id}</span>
                          ${m.display_name && m.email
                            ? html`<span class="member-sub">${m.email}</span>`
                            : nothing}
                        </td>
                        <td class="num" title=${formatTokenExact(m.tokens_summable)}>
                          ${formatTokenAmount(m.tokens_summable)}
                        </td>
                        <td class="num">${formatNumber(m.calls)}</td>
                        <td class="num">${formatNumber(m.events)}</td>
                      </tr>
                    `
                  )}
                </tbody>
              </table>
            `}
      </div>
    `;
  }

  private renderBudgetCard(budget: BudgetListItem): TemplateResult {
    const stateInfo = budgetStateDisplay(budget.state);
    const pct = budgetUsagePercent(budget.consumed, budget.cap);
    const activeOverride = isOverrideActive(budget);
    const isUnlimited = budget.cap === null || budget.cap <= 0;
    const actionVariant = tokenActionBadgeVariant(budget.token_action);
    const actionText = tokenActionLabel(budget.token_action);
    const canModifyOverride =
      this.canOverride && Boolean(this.overrideEndpointBase) && Boolean(budget.id);
    const isClearing = this.clearingBudgetId === budget.id;
    const members = hasGroupFields(budget) ? budget.members : undefined;

    return html`
      <div
        class="budget-card"
        data-testid="budget-card"
        data-limit-name=${budget.limit_name}
        data-budget-state=${budget.state}
      >
        <div class="budget-top">
          <div class="budget-heading">
            <div class="budget-name-row">
              <span class="budget-name">${formatLimitDisplayName(budget.limit_name)}</span>
              <sl-badge
                variant=${stateInfo.variant}
                data-testid="budget-state-badge"
                data-state=${budget.state}
              >
                ${stateInfo.label}
              </sl-badge>
              ${activeOverride
                ? html`<sl-badge variant="primary" data-testid="budget-override-badge">
                    Override active
                  </sl-badge>`
                : nothing}
            </div>
            <div class="budget-sub">
              ${budget.limit_name}${this.showProjectScope && budget.project_scope_id
                ? ` · project ${budget.project_scope_id}`
                : budget.scope_kind === 'group' && budget.project_scope_id
                  ? ` · project ${budget.project_scope_id}`
                  : budget.scope_kind === 'group'
                    ? ' · hub-wide pool'
                    : ''}
            </div>
          </div>

          <div class="budget-actions">
            <sl-badge variant=${actionVariant} data-testid="budget-action-badge">
              ${actionText}
            </sl-badge>
            ${canModifyOverride
              ? html`
                  <sl-button
                    size="small"
                    variant="default"
                    data-testid="budget-override-btn"
                    @click=${() => this.openOverrideDialog(budget)}
                  >
                    ${activeOverride ? 'Update Override' : 'Override'}
                  </sl-button>
                `
              : nothing}
          </div>
        </div>

        <div class="usage-row">
          <span class="usage-figures" data-testid="budget-consumption">
            <span title=${formatTokenExact(budget.consumed)}
              >${formatTokenAmount(budget.consumed)}</span
            >
            <span class="usage-cap-muted">
              /
              ${isUnlimited
                ? html`<span data-testid="budget-cap-unlimited">Unlimited</span>`
                : html`<span title=${formatTokenExact(budget.cap!)}
                    >${formatTokenAmount(budget.cap!)} tokens</span
                  >`}
            </span>
          </span>
          ${pct !== null
            ? html`<span class="usage-pct" data-testid="budget-pct">${pct}%</span>`
            : nothing}
        </div>

        ${pct !== null
          ? html`
              <sl-progress-bar
                value=${pct}
                class="state-${budget.state === 'exhausted'
                  ? 'exhausted'
                  : budget.state === 'warn'
                    ? 'warn'
                    : 'ok'}"
              ></sl-progress-bar>
            `
          : nothing}

        <div class="budget-meta">
          <span class="meta-item" data-testid="budget-window-end">
            <sl-icon name="clock"></sl-icon>
            Resets ${formatUtcTimestamp(budget.window_end_utc)}
          </span>
        </div>

        ${activeOverride
          ? html`
              <div class="override-banner" data-testid="budget-override-banner">
                <span>
                  Override active until
                  <strong>${formatUtcTimestamp(budget.override_until)}</strong>
                </span>
                ${canModifyOverride
                  ? html`
                      <sl-button
                        size="small"
                        variant="text"
                        ?loading=${isClearing}
                        data-testid="budget-clear-override-btn"
                        @click=${() => this.clearOverride(budget)}
                      >
                        Clear override
                      </sl-button>
                    `
                  : nothing}
              </div>
            `
          : nothing}
        ${members !== undefined ? this.renderMemberBreakdown(members) : nothing}
      </div>
    `;
  }

  private renderOverrideDialog(): TemplateResult | typeof nothing {
    const budget = this.overrideTarget;
    if (!budget) return nothing;

    return html`
      <sl-dialog
        label="Override Token Budget"
        open
        data-testid="budget-override-dialog"
        @sl-hide=${(e: Event) => {
          if (e.target !== e.currentTarget) return;
          this.closeOverrideDialog();
        }}
      >
        ${this.overrideError
          ? html`
              <sl-alert
                variant="danger"
                open
                class="dialog-alert"
                data-testid="budget-override-error"
              >
                ${this.overrideError}
              </sl-alert>
            `
          : nothing}

        <p style="margin-top: 0; font-size: 0.875rem; color: var(--scion-text-muted);">
          Temporarily allow launches under
          <strong>${formatLimitDisplayName(budget.limit_name)}</strong> until a UTC timestamp within
          the current window (ends ${formatUtcTimestamp(budget.window_end_utc)}).
        </p>

        <div class="preset-row">
          <sl-button
            size="small"
            @click=${() => {
              this.overrideUntilInput = this.computePresetIso(budget, 60);
            }}
          >
            +1 hour
          </sl-button>
          <sl-button
            size="small"
            @click=${() => {
              this.overrideUntilInput = this.computePresetIso(budget, 240);
            }}
          >
            +4 hours
          </sl-button>
          <sl-button
            size="small"
            @click=${() => {
              this.overrideUntilInput = budget.window_end_utc;
            }}
          >
            End of window
          </sl-button>
        </div>

        <div class="form-row">
          <sl-input
            label="Override Until (UTC ISO-8601)"
            placeholder="2026-11-28T23:59:59Z"
            value=${this.overrideUntilInput}
            data-testid="budget-override-until-input"
            @sl-input=${(e: Event) => {
              this.overrideUntilInput = (e.target as HTMLInputElement).value;
            }}
            required
          ></sl-input>
        </div>

        <div class="form-row">
          <sl-input
            label="Reason (optional)"
            placeholder="Recorded in audit log"
            value=${this.overrideReason}
            data-testid="budget-override-reason-input"
            @sl-input=${(e: Event) => {
              this.overrideReason = (e.target as HTMLInputElement).value;
            }}
          ></sl-input>
        </div>

        <sl-button
          slot="footer"
          variant="primary"
          ?loading=${this.overrideSaving}
          ?disabled=${this.overrideConflict}
          data-testid="budget-override-submit-btn"
          @click=${() => this.submitOverride()}
        >
          Apply Override
        </sl-button>
        <sl-button slot="footer" @click=${() => this.closeOverrideDialog()}>Cancel</sl-button>
      </sl-dialog>
    `;
  }

  override render(): TemplateResult {
    const showNonAdditive =
      this.nonAdditive || this.budgets.some((b) => hasGroupFields(b) && b.non_additive);

    return html`
      <div class="panel" data-testid="token-budgets-panel">
        <div class="panel-header">
          <div class="panel-title-row">
            <sl-icon name="speedometer2"></sl-icon>
            <h3>${this.heading}</h3>
          </div>
          <sl-button
            size="small"
            variant="text"
            ?disabled=${this.loading}
            data-testid="token-budgets-refresh-btn"
            @click=${() => this.requestRefresh()}
          >
            <sl-icon slot="prefix" name="arrow-clockwise"></sl-icon>
            Refresh
          </sl-button>
        </div>

        ${showNonAdditive
          ? html`
              <sl-alert
                variant="primary"
                open
                class="non-additive-notice"
                data-testid="budget-non-additive-notice"
              >
                <sl-icon slot="icon" name="info-circle"></sl-icon>
                Pooled group budgets are shared across all effective members and are
                <strong>non-additive</strong> across overlapping groups (a member in multiple
                budgeted groups charges each pool independently).
              </sl-alert>
            `
          : nothing}
        ${this.inlineError
          ? html`
              <sl-alert
                variant="danger"
                open
                class="inline-error"
                data-testid="budget-inline-error"
              >
                ${this.inlineError}
              </sl-alert>
            `
          : nothing}
        ${this.loading
          ? html`
              <div class="loading-state">
                <sl-spinner></sl-spinner>
                <span>Loading token budgets...</span>
              </div>
            `
          : this.error
            ? html`
                <sl-alert variant="danger" open data-testid="token-budgets-error">
                  ${this.error}
                </sl-alert>
              `
            : this.budgets.length === 0
              ? html`<div class="empty-state" data-testid="token-budgets-empty">
                  ${this.emptyMessage}
                </div>`
              : html`
                  <div class="budget-list">
                    ${this.budgets.map((b) => this.renderBudgetCard(b))}
                  </div>
                `}
      </div>
      ${this.renderOverrideDialog()}
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-token-budgets-panel': ScionTokenBudgetsPanel;
  }
}
