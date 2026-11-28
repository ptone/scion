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
 * Admin Quotas page component
 *
 * Manages limit definitions, entitlement bindings, and usage display.
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';

import { apiFetch, extractApiError } from '../../client/api.js';
import {
  DEFAULT_TOKEN_ACTION,
  isLLMTokensResourceType,
  isPooledGroupLimit,
  tokenActionBadgeVariant,
  tokenActionLabel,
  type TokenAction,
} from '../../shared/usage-budgets.js';
import { FEATURE_FLAGS_CHANGED_EVENT, tokenBudgetsEnabled } from '../../utils/feature-flags.js';
import { formatNumber } from '../../utils/format-number.js';
import { formatRelative } from '../../utils/time.js';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface LimitDefinition {
  id: string;
  name: string;
  resourceType: string;
  unit: string;
  description: string;
  defaultValue: number;
  system: boolean;
  createdAt: string;
  updatedAt: string;
}

interface EntitlementBinding {
  id: string;
  limitDefinitionId: string;
  subjectType: string;
  subjectId: string;
  scopeType: string;
  scopeId: string;
  value: number;
  tokenAction?: string | null;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

interface UsageSummaryEntry {
  limitDefinition: LimitDefinition;
  activeCount: number;
}

interface UsageByLimitResponse {
  limitDefinition: LimitDefinition;
  reservations: UsageReservation[];
  totalActive: number;
}

interface UsageReservation {
  id: string;
  limitDefinitionId: string;
  subjectId: string;
  scopeType: string;
  scopeId: string;
  resourceId: string;
  reserved: number;
  createdAt: string;
  releasedAt?: string;
  /**
   * The broker's current effective max_agents_per_broker limit, present only
   * for broker-scoped reservations under that limit (ptone/scion#2061 P2.2,
   * design.md §5.9). Mirrors Go usageReservationView.BrokerAgentLimit
   * (pkg/hub/handlers_quota.go) exactly — hand-written since there is no
   * Go->TS generator (design.md §6). Absent for every other reservation.
   */
  brokerAgentLimit?: number;
  /**
   * The precedence step that produced brokerAgentLimit: "broker" |
   * "entitlement" | "hub_default" | "unlimited" | "not_enforced". This names
   * the step, not whether the result is a cap: when the broker is
   * unlimited, brokerAgentLimit is absent but brokerAgentLimitSource is
   * still whichever step produced it ("broker" for a settings.maxAgents=0
   * override, "entitlement"/"hub_default" for a 0 binding or default).
   * "unlimited" itself means no limit definition or no quota service is
   * configured hub-wide, a state in which this reservation (which requires
   * quota enforcement to have run) would not exist to display in the first
   * place.
   *
   * "not_enforced" (design.md Amendment A1) means the P1b enforcement
   * switch is off: brokerAgentLimit keeps whatever the precedence steps
   * resolved (a cap, or absent when that resolves to unlimited, exactly as
   * above), but the value is informational only — it is not currently
   * applied. The usage detail must show this visibly, not only in a
   * tooltip.
   */
  brokerAgentLimitSource?: string;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

@customElement('scion-page-admin-quotas')
export class ScionPageAdminQuotas extends LitElement {
  // --- List state ---
  @state() private loading = true;
  @state() private limits: LimitDefinition[] = [];
  @state() private usageSummary: Map<string, number> = new Map();
  @state() private error: string | null = null;

  // --- Expanded limit (entitlements + usage detail) ---
  @state() private expandedLimitId: string | null = null;
  @state() private entitlements: EntitlementBinding[] = [];
  @state() private entitlementsLoading = false;
  @state() private entitlementsError: string | null = null;
  @state() private usageDetail: UsageByLimitResponse | null = null;

  // --- Create/Edit limit dialog ---
  @state() private showLimitDialog = false;
  @state() private editingLimit: LimitDefinition | null = null;
  @state() private limitForm: {
    name: string;
    resourceType: string;
    unit: string;
    description: string;
    // null means the field is empty or not a valid non-negative integer, so
    // saveLimitDefinition can tell "cleared by the user" apart from an
    // explicit 0 (= unlimited) and reject it instead of silently sending 0
    // (ptone/scion#2061 P1a review F1: clearing this on a system row like
    // max_agents_per_broker would otherwise turn off the crash ceiling).
    defaultValue: number | null;
  } = {
    name: '',
    resourceType: '',
    unit: '',
    description: '',
    defaultValue: 0,
  };
  @state() private limitDialogError: string | null = null;
  @state() private limitDialogSaving = false;

  // --- Feature flag ---
  @state() private tokenBudgetsFlagEnabled = tokenBudgetsEnabled();

  // --- Create/Edit entitlement dialog ---
  @state() private showEntitlementDialog = false;
  @state() private editingEntitlement: EntitlementBinding | null = null;
  @state() private editingEntitlementEtag: string | null = null;
  @state() private entitlementDialogConflict = false;
  @state() private entitlementForm: {
    subjectType: string;
    subjectId: string;
    scopeType: string;
    scopeId: string;
    value: number;
    tokenAction: TokenAction;
  } = {
    subjectType: 'user',
    subjectId: '',
    scopeType: 'system',
    scopeId: '',
    value: 0,
    tokenAction: DEFAULT_TOKEN_ACTION,
  };
  @state() private entitlementDialogError: string | null = null;
  @state() private entitlementDialogSaving = false;

  // --- Delete confirmation ---
  @state() private showDeleteDialog = false;
  @state() private deleteTarget: {
    type: 'limit' | 'entitlement';
    id: string;
    name: string;
  } | null = null;
  @state() private deleteLoading = false;
  @state() private deleteDialogError: string | null = null;

  static override styles = css`
    :host {
      display: block;
    }

    .header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 1.5rem;
    }

    .header h1 {
      font-size: 1.5rem;
      font-weight: 700;
      color: var(--scion-text, #1e293b);
      margin: 0;
    }

    .header-right {
      display: flex;
      align-items: center;
      gap: 1rem;
    }

    .item-count {
      font-size: 0.875rem;
      color: var(--scion-text-muted, #64748b);
    }

    .table-container {
      background: var(--scion-surface, #ffffff);
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-lg, 0.75rem);
      overflow: hidden;
    }

    table {
      width: 100%;
      border-collapse: collapse;
    }

    th {
      text-align: left;
      padding: 0.75rem 1rem;
      font-size: 0.75rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: var(--scion-text-muted, #64748b);
      background: var(--scion-bg-subtle, #f1f5f9);
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
    }

    td {
      padding: 0.75rem 1rem;
      font-size: 0.875rem;
      color: var(--scion-text, #1e293b);
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
      vertical-align: middle;
    }

    tr:last-child td {
      border-bottom: none;
    }

    tr.clickable {
      cursor: pointer;
    }

    tr.clickable:hover td {
      background: var(--scion-bg-subtle, #f1f5f9);
    }

    tr.expanded td {
      background: var(--sl-color-primary-50, #eff6ff);
    }

    .type-badge {
      display: inline-flex;
      align-items: center;
      padding: 0.125rem 0.5rem;
      border-radius: 9999px;
      font-size: 0.75rem;
      font-weight: 500;
    }

    .type-badge.agent {
      background: var(--sl-color-primary-100, #dbeafe);
      color: var(--sl-color-primary-700, #1d4ed8);
    }

    .type-badge.project {
      background: var(--sl-color-success-100, #dcfce7);
      color: var(--sl-color-success-700, #15803d);
    }

    .type-badge.group_member {
      background: var(--sl-color-warning-100, #fef3c7);
      color: var(--sl-color-warning-700, #a16207);
    }

    .type-badge.llm_tokens {
      background: var(--sl-color-purple-100);
      color: var(--sl-color-purple-700);
    }

    .type-badge.default {
      background: var(--scion-bg-subtle, #f1f5f9);
      color: var(--scion-text-muted, #64748b);
    }

    .system-badge {
      display: inline-flex;
      align-items: center;
      padding: 0.125rem 0.5rem;
      border-radius: 9999px;
      font-size: 0.6875rem;
      font-weight: 600;
      background: var(--sl-color-neutral-100, #f1f5f9);
      color: var(--sl-color-neutral-600, #475569);
    }

    /* Visible marker for a broker cap whose source is "not_enforced"
     * (design.md Amendment A1): the value shown is a real, resolved cap,
     * but it is not currently enforced. Must not be tooltip-only. */
    .not-enforced-marker {
      display: inline-flex;
      align-items: center;
      margin-left: 0.375rem;
      padding: 0.0625rem 0.375rem;
      border-radius: 9999px;
      font-size: 0.6875rem;
      font-weight: 600;
      background: var(--sl-color-warning-100, #fef3c7);
      color: var(--sl-color-warning-700, #a16207);
    }

    .meta-text {
      font-size: 0.8125rem;
      color: var(--scion-text-muted, #64748b);
    }

    .mono {
      font-family: var(--scion-font-mono, monospace);
      font-size: 0.8125rem;
    }

    .usage-bar-cell {
      min-width: 140px;
    }

    .usage-info {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      font-size: 0.8125rem;
      color: var(--scion-text-muted, #64748b);
    }

    .usage-info sl-progress-bar {
      flex: 1;
      min-width: 80px;
      --height: 8px;
    }

    .actions-cell {
      white-space: nowrap;
      text-align: right;
    }

    .actions-cell sl-icon-button::part(base) {
      font-size: 1rem;
      padding: 0.25rem;
    }

    /* Expanded detail panel */
    .detail-panel {
      background: var(--scion-surface, #ffffff);
      border: 1px solid var(--scion-border, #e2e8f0);
      border-top: none;
      border-radius: 0 0 var(--scion-radius-lg, 0.75rem) var(--scion-radius-lg, 0.75rem);
      margin-top: -1px;
      margin-bottom: 1rem;
    }

    .detail-section {
      padding: 1rem 1.5rem;
    }

    .detail-section + .detail-section {
      border-top: 1px solid var(--scion-border, #e2e8f0);
    }

    .detail-section-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 0.75rem;
    }

    .detail-section-header h3 {
      font-size: 0.875rem;
      font-weight: 600;
      color: var(--scion-text, #1e293b);
      margin: 0;
    }

    .entitlement-table {
      width: 100%;
      border-collapse: collapse;
    }

    .entitlement-table th {
      background: transparent;
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
      padding: 0.5rem 0.75rem;
    }

    .entitlement-table td {
      padding: 0.5rem 0.75rem;
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
    }

    .entitlement-table tr:last-child td {
      border-bottom: none;
    }

    .usage-detail-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
      gap: 0.75rem;
    }

    .usage-card {
      background: var(--scion-bg-subtle, #f1f5f9);
      border-radius: var(--scion-radius, 0.5rem);
      padding: 0.75rem 1rem;
    }

    .usage-card-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 0.5rem;
    }

    .usage-card-subject {
      font-size: 0.8125rem;
      font-weight: 500;
      color: var(--scion-text, #1e293b);
      font-family: var(--scion-font-mono, monospace);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      max-width: 180px;
    }

    .usage-card-count {
      font-size: 0.8125rem;
      font-weight: 600;
      color: var(--scion-text-muted, #64748b);
    }

    .usage-card sl-progress-bar {
      --height: 6px;
    }

    /* Dialog form layout */
    .form-row {
      margin-bottom: 1rem;
    }

    .form-row:last-child {
      margin-bottom: 0;
    }

    .form-row sl-input,
    .form-row sl-select,
    .form-row sl-textarea {
      width: 100%;
    }

    .dialog-error {
      margin-bottom: 1rem;
    }

    /* Empty and loading states */
    .empty-state {
      text-align: center;
      padding: 4rem 2rem;
      background: var(--scion-surface, #ffffff);
      border: 1px dashed var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-lg, 0.75rem);
    }

    .empty-state > sl-icon {
      font-size: 4rem;
      color: var(--scion-text-muted, #64748b);
      opacity: 0.5;
      margin-bottom: 1rem;
    }

    .empty-state h2 {
      font-size: 1.25rem;
      font-weight: 600;
      color: var(--scion-text, #1e293b);
      margin: 0 0 0.5rem 0;
    }

    .empty-state p {
      color: var(--scion-text-muted, #64748b);
      margin: 0;
    }

    .loading-state {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      padding: 4rem 2rem;
      color: var(--scion-text-muted, #64748b);
    }

    .loading-state sl-spinner {
      font-size: 2rem;
      margin-bottom: 1rem;
    }

    .error-state {
      text-align: center;
      padding: 3rem 2rem;
      background: var(--scion-surface, #ffffff);
      border: 1px solid var(--sl-color-danger-200, #fecaca);
      border-radius: var(--scion-radius-lg, 0.75rem);
    }

    .error-state sl-icon {
      font-size: 3rem;
      color: var(--sl-color-danger-500, #ef4444);
      margin-bottom: 1rem;
    }

    .error-state h2 {
      font-size: 1.25rem;
      font-weight: 600;
      color: var(--scion-text, #1e293b);
      margin: 0 0 0.5rem 0;
    }

    .error-state p {
      color: var(--scion-text-muted, #64748b);
      margin: 0 0 1rem 0;
    }

    .error-details {
      font-family: var(--scion-font-mono, monospace);
      font-size: 0.875rem;
      background: var(--scion-bg-subtle, #f1f5f9);
      padding: 0.75rem 1rem;
      border-radius: var(--scion-radius, 0.5rem);
      color: var(--sl-color-danger-700, #b91c1c);
      margin-bottom: 1rem;
    }

    .inline-empty {
      text-align: center;
      padding: 1.5rem;
      color: var(--scion-text-muted, #64748b);
      font-size: 0.875rem;
    }

    @media (max-width: 768px) {
      .hide-mobile {
        display: none;
      }
    }
  `;

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  private boundOnFeatureFlagsChanged = (): void => {
    const next = tokenBudgetsEnabled();
    if (next !== this.tokenBudgetsFlagEnabled) {
      this.tokenBudgetsFlagEnabled = next;
    }
  };

  override connectedCallback(): void {
    super.connectedCallback();
    this.tokenBudgetsFlagEnabled = tokenBudgetsEnabled();
    window.addEventListener(FEATURE_FLAGS_CHANGED_EVENT, this.boundOnFeatureFlagsChanged);
    void this.loadData();
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    window.removeEventListener(FEATURE_FLAGS_CHANGED_EVENT, this.boundOnFeatureFlagsChanged);
  }

  // ---------------------------------------------------------------------------
  // Data loading
  // ---------------------------------------------------------------------------

  private async loadData(): Promise<void> {
    this.loading = true;
    this.error = null;

    try {
      const [limitsRes, usageRes] = await Promise.all([
        apiFetch('/api/v1/admin/limits'),
        apiFetch('/api/v1/admin/usage'),
      ]);

      if (!limitsRes.ok) {
        throw new Error(await extractApiError(limitsRes, `HTTP ${limitsRes.status}`));
      }
      if (!usageRes.ok) {
        throw new Error(await extractApiError(usageRes, `HTTP ${usageRes.status}`));
      }

      const limitsData = (await limitsRes.json()) as { items: LimitDefinition[] };
      const usageData = (await usageRes.json()) as { items: UsageSummaryEntry[] };

      this.limits = limitsData.items || [];

      const summaryMap = new Map<string, number>();
      for (const entry of usageData.items || []) {
        summaryMap.set(entry.limitDefinition.id, entry.activeCount);
      }
      this.usageSummary = summaryMap;
    } catch (err) {
      console.error('Failed to load quotas:', err);
      this.error = err instanceof Error ? err.message : 'Failed to load quotas';
    } finally {
      this.loading = false;
    }
  }

  private async loadEntitlements(limitId: string): Promise<void> {
    this.entitlementsLoading = true;
    this.entitlements = [];
    this.usageDetail = null;
    this.entitlementsError = null;

    try {
      const [entRes, usageRes] = await Promise.all([
        apiFetch(`/api/v1/admin/limits/${limitId}/entitlements`),
        apiFetch(`/api/v1/admin/usage/${limitId}`),
      ]);

      if (entRes.ok) {
        const data = (await entRes.json()) as { items: EntitlementBinding[] };
        this.entitlements = data.items || [];
      } else {
        this.entitlementsError = await extractApiError(entRes, `HTTP ${entRes.status}`);
      }

      if (usageRes.ok) {
        this.usageDetail = (await usageRes.json()) as UsageByLimitResponse;
      } else if (!this.entitlementsError) {
        // Only set if we don't already have an error
        this.entitlementsError = await extractApiError(usageRes, `HTTP ${usageRes.status}`);
      }
    } catch (err) {
      console.error('Failed to load entitlements:', err);
      this.entitlementsError = err instanceof Error ? err.message : 'Failed to load entitlements';
    } finally {
      this.entitlementsLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Limit CRUD
  // ---------------------------------------------------------------------------

  private openCreateLimit(): void {
    this.editingLimit = null;
    this.limitForm = { name: '', resourceType: '', unit: '', description: '', defaultValue: 0 };
    this.limitDialogError = null;
    this.showLimitDialog = true;
  }

  private openEditLimit(limit: LimitDefinition, e: Event): void {
    e.stopPropagation();
    this.editingLimit = limit;
    this.limitForm = {
      name: limit.name,
      resourceType: limit.resourceType,
      unit: limit.unit,
      description: limit.description,
      defaultValue: limit.defaultValue,
    };
    this.limitDialogError = null;
    this.showLimitDialog = true;
  }

  private async saveLimitDefinition(): Promise<void> {
    if (!this.limitForm.name.trim()) {
      this.limitDialogError = 'Name is required';
      return;
    }
    if (!this.limitForm.resourceType) {
      this.limitDialogError = 'Resource type is required';
      return;
    }
    if (this.limitForm.defaultValue === null) {
      this.limitDialogError =
        'Default Value is required and must be a non-negative integer (0 = unlimited)';
      return;
    }

    this.limitDialogSaving = true;
    this.limitDialogError = null;

    try {
      const body = JSON.stringify({
        name: this.limitForm.name.trim(),
        resourceType: this.limitForm.resourceType,
        unit: this.limitForm.unit.trim(),
        description: this.limitForm.description.trim(),
        defaultValue: this.limitForm.defaultValue,
      });

      let res: Response;
      if (this.editingLimit) {
        res = await apiFetch(`/api/v1/admin/limits/${this.editingLimit.id}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body,
        });
      } else {
        res = await apiFetch('/api/v1/admin/limits', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body,
        });
      }

      if (!res.ok) {
        throw new Error(await extractApiError(res, `HTTP ${res.status}`));
      }

      this.showLimitDialog = false;
      await this.loadData();
    } catch (err) {
      this.limitDialogError = err instanceof Error ? err.message : 'Failed to save limit';
    } finally {
      this.limitDialogSaving = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Entitlement CRUD
  // ---------------------------------------------------------------------------

  private getExpandedLimit(): LimitDefinition | undefined {
    return this.limits.find((l) => l.id === this.expandedLimitId);
  }

  private normalizeTokenAction(action: string | null | undefined): TokenAction {
    if (action === 'suspend_and_pause' || action === 'notify_only' || action === 'pause_new') {
      return action;
    }
    return DEFAULT_TOKEN_ACTION;
  }

  private openCreateEntitlement(limit: LimitDefinition): void {
    this.editingEntitlement = null;
    this.editingEntitlementEtag = null;
    this.entitlementDialogConflict = false;
    const isPooled = isPooledGroupLimit(limit.name);
    const isProjectScopedDefault =
      limit.name === 'llm_tokens_per_project_day' || limit.name === 'llm_tokens_per_project_month';
    this.entitlementForm = {
      subjectType: isPooled ? 'group' : isProjectScopedDefault ? 'system_default' : 'user',
      subjectId: '',
      scopeType: isProjectScopedDefault ? 'project' : 'system',
      scopeId: '',
      value: 0,
      tokenAction: DEFAULT_TOKEN_ACTION,
    };
    this.entitlementDialogError = null;
    this.showEntitlementDialog = true;
  }

  private formatEntitlementIfMatch(updatedAt: string | undefined): string | null {
    const trimmed = (updatedAt || '').trim();
    if (!trimmed || Number.isNaN(Date.parse(trimmed))) {
      return null;
    }
    // Preserve the raw RFC3339Nano string from the list payload (avoiding JS
    // Date millisecond truncation); the Hub's parseEntitlementIfMatch parses
    // quoted RFC3339Nano and compares at microsecond precision.
    return `"${trimmed}"`;
  }

  private openEditEntitlement(binding: EntitlementBinding): void {
    this.editingEntitlement = binding;
    this.editingEntitlementEtag = this.formatEntitlementIfMatch(binding.updatedAt);
    this.entitlementDialogConflict = false;
    this.entitlementForm = {
      subjectType: binding.subjectType,
      subjectId: binding.subjectId,
      scopeType: binding.scopeType,
      scopeId: binding.scopeId,
      value: binding.value,
      tokenAction: this.normalizeTokenAction(binding.tokenAction),
    };
    this.entitlementDialogError = null;
    this.showEntitlementDialog = true;
  }

  private async saveEntitlement(): Promise<void> {
    const limit = this.getExpandedLimit();
    const isTokenLimit = Boolean(limit && isLLMTokensResourceType(limit.resourceType));
    const isPooled = Boolean(limit && isPooledGroupLimit(limit.name));

    const subjectType = isPooled ? 'group' : this.entitlementForm.subjectType;
    const subjectId = subjectType === 'system_default' ? '' : this.entitlementForm.subjectId.trim();
    const scopeType = this.entitlementForm.scopeType;
    const scopeId = scopeType === 'project' ? this.entitlementForm.scopeId.trim() : '';

    if (isPooled) {
      if (!subjectId) {
        this.entitlementDialogError = 'Group ID is required for pooled group token limits';
        return;
      }
      if (scopeType !== 'system' && scopeType !== 'project') {
        this.entitlementDialogError = 'Pooled group bindings must use system or project scope';
        return;
      }
      if (scopeType === 'project' && !scopeId) {
        this.entitlementDialogError = 'Project ID is required when scope is project';
        return;
      }
    } else if (subjectType !== 'system_default' && !subjectId) {
      this.entitlementDialogError = 'Subject ID is required';
      return;
    }

    if (isTokenLimit && scopeType === 'project' && !scopeId) {
      this.entitlementDialogError = 'Project ID is required when scope is project';
      return;
    }

    const numericValue = Number(this.entitlementForm.value);
    if (!Number.isInteger(numericValue) || numericValue < 0) {
      this.entitlementDialogError = 'Value must be a non-negative integer (0 = unlimited)';
      return;
    }

    this.entitlementDialogSaving = true;
    this.entitlementDialogError = null;
    this.entitlementDialogConflict = false;

    try {
      const payload: Record<string, unknown> = {
        subjectType,
        subjectId,
        scopeType,
        scopeId,
        value: numericValue,
      };
      // PUT replaces tokenAction when omitted, so always send an explicit
      // tokenAction for llm_tokens limits (defaulting to pause_new).
      if (isTokenLimit) {
        payload.tokenAction = this.normalizeTokenAction(this.entitlementForm.tokenAction);
      }

      const headers: Record<string, string> = { 'Content-Type': 'application/json' };
      let res: Response;
      if (this.editingEntitlement) {
        if (this.editingEntitlementEtag) {
          headers['If-Match'] = this.editingEntitlementEtag;
        }
        res = await apiFetch(
          `/api/v1/admin/entitlements/${encodeURIComponent(this.editingEntitlement.id)}`,
          {
            method: 'PUT',
            headers,
            body: JSON.stringify(payload),
          }
        );
        if (res.status === 412) {
          this.entitlementDialogConflict = true;
          this.entitlementDialogError =
            'This entitlement binding was modified concurrently (412 precondition failed). Bindings have been refreshed — close and re-open Edit to retry.';
          if (this.expandedLimitId) {
            await this.loadEntitlements(this.expandedLimitId);
          }
          return;
        }
      } else {
        res = await apiFetch(`/api/v1/admin/limits/${this.expandedLimitId}/entitlements`, {
          method: 'POST',
          headers,
          body: JSON.stringify(payload),
        });
      }

      if (!res.ok) {
        throw new Error(await extractApiError(res, `HTTP ${res.status}`));
      }

      this.showEntitlementDialog = false;
      this.editingEntitlement = null;
      this.editingEntitlementEtag = null;
      if (this.expandedLimitId) {
        await this.loadEntitlements(this.expandedLimitId);
      }
    } catch (err) {
      this.entitlementDialogError =
        err instanceof Error
          ? err.message
          : this.editingEntitlement
            ? 'Failed to update entitlement'
            : 'Failed to create entitlement';
    } finally {
      this.entitlementDialogSaving = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Delete
  // ---------------------------------------------------------------------------

  private confirmDeleteLimit(limit: LimitDefinition, e: Event): void {
    e.stopPropagation();
    this.deleteTarget = { type: 'limit', id: limit.id, name: limit.name };
    this.deleteDialogError = null;
    this.showDeleteDialog = true;
  }

  private confirmDeleteEntitlement(binding: EntitlementBinding): void {
    this.deleteTarget = {
      type: 'entitlement',
      id: binding.id,
      name: `${binding.subjectType}:${binding.subjectId}`,
    };
    this.deleteDialogError = null;
    this.showDeleteDialog = true;
  }

  private async executeDelete(): Promise<void> {
    if (!this.deleteTarget) return;

    this.deleteLoading = true;
    this.deleteDialogError = null;

    try {
      let url: string;
      if (this.deleteTarget.type === 'limit') {
        url = `/api/v1/admin/limits/${this.deleteTarget.id}`;
      } else {
        url = `/api/v1/admin/entitlements/${this.deleteTarget.id}`;
      }

      const res = await apiFetch(url, { method: 'DELETE' });
      if (!res.ok) {
        throw new Error(await extractApiError(res, `HTTP ${res.status}`));
      }

      const wasLimitDelete = this.deleteTarget.type === 'limit';

      // Clear expanded state before reload to avoid referencing deleted limit
      if (wasLimitDelete) {
        this.expandedLimitId = null;
      }

      this.showDeleteDialog = false;
      this.deleteTarget = null;
      this.deleteDialogError = null;

      // Reload appropriate data
      await this.loadData();
      if (!wasLimitDelete && this.expandedLimitId) {
        await this.loadEntitlements(this.expandedLimitId);
      }
    } catch (err) {
      this.deleteDialogError = err instanceof Error ? err.message : 'Failed to delete';
    } finally {
      this.deleteLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Row expand/collapse
  // ---------------------------------------------------------------------------

  private async toggleExpand(limitId: string): Promise<void> {
    if (this.expandedLimitId === limitId) {
      this.expandedLimitId = null;
      this.entitlements = [];
      this.usageDetail = null;
      return;
    }

    this.expandedLimitId = limitId;
    await this.loadEntitlements(limitId);
  }

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  private resourceTypeBadgeClass(rt: string): string {
    if (rt === 'agent') return 'agent';
    if (rt === 'project') return 'project';
    if (rt === 'group_member') return 'group_member';
    if (rt === 'llm_tokens') return 'llm_tokens';
    return 'default';
  }

  private formatValue(value: number, resourceType?: string): string {
    if (value === 0) return 'unlimited';
    if (resourceType && isLLMTokensResourceType(resourceType)) {
      return formatNumber(value);
    }
    return String(value);
  }

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  override render() {
    return html`
      <div class="header">
        <h1>Quotas</h1>
        <div class="header-right">
          ${!this.loading && !this.error
            ? (() => {
                const count = this.getVisibleLimits().length;
                return html`<span class="item-count"
                  >${count} limit${count !== 1 ? 's' : ''}</span
                >`;
              })()
            : ''}
          <sl-button variant="primary" size="small" @click=${this.openCreateLimit}>
            <sl-icon slot="prefix" name="plus-lg"></sl-icon>
            Create Limit
          </sl-button>
        </div>
      </div>

      ${this.loading ? this.renderLoading() : this.error ? this.renderError() : this.renderLimits()}
      ${this.renderLimitDialog()} ${this.renderEntitlementDialog()} ${this.renderDeleteDialog()}
    `;
  }

  private renderLoading() {
    return html`
      <div class="loading-state">
        <sl-spinner></sl-spinner>
        <p>Loading quotas...</p>
      </div>
    `;
  }

  private renderError() {
    return html`
      <div class="error-state">
        <sl-icon name="exclamation-triangle"></sl-icon>
        <h2>Failed to Load Quotas</h2>
        <p>There was a problem connecting to the API.</p>
        <div class="error-details">${this.error}</div>
        <sl-button variant="primary" @click=${() => this.loadData()}>
          <sl-icon slot="prefix" name="arrow-clockwise"></sl-icon>
          Retry
        </sl-button>
      </div>
    `;
  }

  private getVisibleLimits(): LimitDefinition[] {
    if (this.tokenBudgetsFlagEnabled) return this.limits;
    return this.limits.filter((l) => !isLLMTokensResourceType(l.resourceType));
  }

  private renderLimits() {
    const visibleLimits = this.getVisibleLimits();
    if (visibleLimits.length === 0) {
      return html`
        <div class="empty-state">
          <sl-icon name="speedometer2"></sl-icon>
          <h2>No Limit Definitions</h2>
          <p>Create a limit definition to start managing quotas.</p>
        </div>
      `;
    }

    return html`
      <div class="table-container">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Resource Type</th>
              <th class="hide-mobile">Unit</th>
              <th class="hide-mobile">Default</th>
              <th class="hide-mobile">Usage</th>
              <th class="hide-mobile">Updated</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            ${visibleLimits.map((limit) => this.renderLimitRow(limit))}
          </tbody>
        </table>
      </div>
      ${this.expandedLimitId
        ? (() => {
            const limit = visibleLimits.find((l) => l.id === this.expandedLimitId);
            return limit ? this.renderDetailPanel(limit) : nothing;
          })()
        : nothing}
    `;
  }

  private renderLimitRow(limit: LimitDefinition) {
    const activeCount = this.usageSummary.get(limit.id) ?? 0;
    const isExpanded = this.expandedLimitId === limit.id;
    // max_agents_per_broker's activeCount is a sum across every broker
    // (ptone/scion#2061 P2.2), while defaultValue is the *per-broker* cap —
    // dividing one by the other (e.g. "36 / 30" for three brokers at 12
    // each) reads as a breached quota when no single broker is near its
    // cap, and ignores per-broker overrides/entitlements entirely. Render
    // just the count for this one limit; the per-broker expansion below
    // still shows each broker's real effective cap and source.
    const isPerBrokerLimit = limit.name === 'max_agents_per_broker';
    // max_agents_per_project's activeCount is likewise a sum across every
    // project, while the cap applies per project (and is the agent creator's
    // resolved cap), so it gets the same count-only rendering
    // (ptone/scion#6033).
    const isPerProjectLimit = limit.name === 'max_agents_per_project';
    const isTokenLimit = isLLMTokensResourceType(limit.resourceType);
    const isPooledGroup = isPooledGroupLimit(limit.name);
    const usageMeta = isPerBrokerLimit
      ? 'across all brokers; cap is per broker'
      : isPerProjectLimit
        ? "across all projects; cap applies per project (creator's resolved cap)"
        : '';
    const pct =
      !usageMeta && !isTokenLimit && limit.defaultValue > 0
        ? Math.min(100, Math.round((activeCount / limit.defaultValue) * 100))
        : 0;

    return html`
      <tr
        class="clickable ${isExpanded ? 'expanded' : ''}"
        @click=${() => this.toggleExpand(limit.id)}
      >
        <td>
          <span style="font-weight: 500">${limit.name}</span>
          ${limit.system
            ? html`<span class="system-badge" style="margin-left: 0.5rem">system</span>`
            : nothing}
          ${isPooledGroup
            ? html`<span class="system-badge" style="margin-left: 0.375rem">pooled</span>`
            : nothing}
          ${limit.description
            ? html`<br /><span class="meta-text">${limit.description}</span>`
            : nothing}
        </td>
        <td>
          <span class="type-badge ${this.resourceTypeBadgeClass(limit.resourceType)}">
            ${limit.resourceType || '—'}
          </span>
        </td>
        <td class="hide-mobile">
          <span class="mono">${limit.unit || '—'}</span>
        </td>
        <td class="hide-mobile">
          <span class="mono">${this.formatValue(limit.defaultValue, limit.resourceType)}</span>
        </td>
        <td class="hide-mobile usage-bar-cell">
          <div class="usage-info">
            ${isTokenLimit
              ? html`<span class="meta-text" style="font-size: 0.75rem"
                  >${isPooledGroup
                    ? 'Pooled across group (non-additive)'
                    : 'Tracked per UTC window'}</span
                >`
              : usageMeta
                ? html`
                    <span>${activeCount}</span>
                    <span class="meta-text" style="font-size: 0.75rem">${usageMeta}</span>
                  `
                : html`
                    <span
                      >${activeCount}${limit.defaultValue > 0
                        ? ` / ${limit.defaultValue}`
                        : ''}</span
                    >
                    ${limit.defaultValue > 0
                      ? html`<sl-progress-bar value=${pct}></sl-progress-bar>`
                      : nothing}
                  `}
          </div>
        </td>
        <td class="hide-mobile">
          <span class="meta-text">${formatRelative(limit.updatedAt)}</span>
        </td>
        <td class="actions-cell">
          <sl-icon-button
            name="pencil"
            label="Edit"
            @click=${(e: Event) => this.openEditLimit(limit, e)}
          ></sl-icon-button>
          ${!limit.system
            ? html`
                <sl-icon-button
                  name="trash"
                  label="Delete"
                  @click=${(e: Event) => this.confirmDeleteLimit(limit, e)}
                ></sl-icon-button>
              `
            : nothing}
          <sl-icon-button
            name=${isExpanded ? 'chevron-up' : 'chevron-down'}
            label=${isExpanded ? 'Collapse' : 'Expand'}
          ></sl-icon-button>
        </td>
      </tr>
    `;
  }

  private renderDetailPanel(limit: LimitDefinition) {
    const isTokenLimit = isLLMTokensResourceType(limit.resourceType);
    const isPooledGroup = isPooledGroupLimit(limit.name);

    return html`
      <div class="detail-panel">
        <!-- Entitlements section -->
        <div class="detail-section">
          <div class="detail-section-header">
            <h3>Entitlement Bindings</h3>
            <sl-button
              size="small"
              data-testid="add-entitlement-btn"
              @click=${() => this.openCreateEntitlement(limit)}
            >
              <sl-icon slot="prefix" name="plus-lg"></sl-icon>
              Add Binding
            </sl-button>
          </div>
          ${this.entitlementsLoading
            ? html`<div class="loading-state" style="padding: 1.5rem">
                <sl-spinner></sl-spinner>
              </div>`
            : this.entitlementsError
              ? html`<sl-alert variant="danger" open class="dialog-error"
                  >${this.entitlementsError}</sl-alert
                >`
              : this.entitlements.length === 0
                ? html`<div class="inline-empty">
                    No entitlement bindings for this limit. The default value
                    (${this.formatValue(limit.defaultValue, limit.resourceType)}) applies to all
                    subjects.
                  </div>`
                : html`
                    <table class="entitlement-table">
                      <thead>
                        <tr>
                          <th>Subject Type</th>
                          <th>Subject ID</th>
                          <th>Scope</th>
                          <th>Value</th>
                          ${isTokenLimit ? html`<th>Token Action</th>` : nothing}
                          <th class="hide-mobile">Created By</th>
                          <th class="hide-mobile">Created</th>
                          <th></th>
                        </tr>
                      </thead>
                      <tbody>
                        ${this.entitlements.map(
                          (b) => html`
                            <tr data-testid="entitlement-row" data-binding-id=${b.id}>
                              <td><span class="type-badge default">${b.subjectType}</span></td>
                              <td><span class="mono">${b.subjectId || '—'}</span></td>
                              <td>
                                <span class="mono"
                                  >${b.scopeType}${b.scopeId ? `:${b.scopeId}` : ''}</span
                                >
                              </td>
                              <td>
                                <span class="mono"
                                  >${this.formatValue(b.value, limit.resourceType)}</span
                                >
                              </td>
                              ${isTokenLimit
                                ? html`
                                    <td>
                                      <sl-badge
                                        variant=${tokenActionBadgeVariant(b.tokenAction)}
                                        data-testid="entitlement-token-action"
                                      >
                                        ${tokenActionLabel(b.tokenAction)}
                                      </sl-badge>
                                    </td>
                                  `
                                : nothing}
                              <td class="hide-mobile">
                                <span class="meta-text">${b.createdBy || '—'}</span>
                              </td>
                              <td class="hide-mobile">
                                <span class="meta-text">${formatRelative(b.createdAt)}</span>
                              </td>
                              <td class="actions-cell">
                                <sl-icon-button
                                  name="pencil"
                                  label="Edit"
                                  data-testid="edit-entitlement-btn"
                                  @click=${() => this.openEditEntitlement(b)}
                                ></sl-icon-button>
                                <sl-icon-button
                                  name="trash"
                                  label="Delete"
                                  @click=${() => this.confirmDeleteEntitlement(b)}
                                ></sl-icon-button>
                              </td>
                            </tr>
                          `
                        )}
                      </tbody>
                    </table>
                  `}
        </div>

        <!-- Usage detail section -->
        <div class="detail-section">
          <div class="detail-section-header">
            <h3>${isTokenLimit ? 'Budget Window Tracking' : 'Active Usage'}</h3>
          </div>
          ${isTokenLimit
            ? html`<div class="inline-empty">
                ${isPooledGroup
                  ? 'Pooled group budgets are tracked per UTC window on each group detail page and are non-additive across overlapping groups.'
                  : 'Raw-token budgets are evaluated per UTC day/month window and displayed on Project Settings, Profile Budgets, and Group Detail pages.'}
              </div>`
            : this.entitlementsLoading
              ? html`<div class="loading-state" style="padding: 1.5rem">
                  <sl-spinner></sl-spinner>
                </div>`
              : this.entitlementsError
                ? nothing
                : !this.usageDetail || this.usageDetail.reservations.length === 0
                  ? html`<div class="inline-empty">No active usage reservations.</div>`
                  : html`
                      <div style="margin-bottom: 0.75rem">
                        <span class="meta-text"
                          >Total active: <strong>${this.usageDetail.totalActive}</strong></span
                        >
                      </div>
                      <div class="usage-detail-grid">
                        ${this.usageDetail.reservations.map(
                          (r) => html`
                            <div class="usage-card">
                              <div class="usage-card-header">
                                <span class="usage-card-subject" title=${r.subjectId}
                                  >${r.subjectId}</span
                                >
                                <span class="usage-card-count">${r.reserved} reserved</span>
                              </div>
                              <div class="meta-text" style="font-size: 0.75rem">
                                Resource: <span class="mono">${r.resourceId}</span>
                              </div>
                              ${r.brokerAgentLimitSource
                                ? html`
                                    <div class="meta-text" style="font-size: 0.75rem">
                                      Broker cap:
                                      <span class="mono"
                                        >${r.brokerAgentLimit != null
                                          ? this.formatValue(r.brokerAgentLimit)
                                          : 'unlimited'}</span
                                      >
                                      <!-- Defensive: brokerAgentLimitSource is
                                      "unlimited" only when no limit definition
                                      or quota service is configured hub-wide,
                                      a state this reservation (which requires
                                      quota enforcement to have run) can't
                                      actually reach — kept to avoid ever
                                      rendering the redundant "(unlimited)".
                                      "not_enforced" is suppressed here too: the
                                      ".not-enforced-marker" pill below already
                                      says "not enforced", so showing the raw
                                      "(not_enforced)" token alongside it would
                                      render the same fact twice. -->
                                      ${r.brokerAgentLimitSource === 'unlimited' ||
                                      r.brokerAgentLimitSource === 'not_enforced'
                                        ? nothing
                                        : html`<span title="Precedence source"
                                            >(${r.brokerAgentLimitSource})</span
                                          >`}
                                      ${r.brokerAgentLimitSource === 'not_enforced'
                                        ? html`<span class="not-enforced-marker"
                                            >not enforced</span
                                          >`
                                        : nothing}
                                    </div>
                                  `
                                : nothing}
                            </div>
                          `
                        )}
                      </div>
                    `}
        </div>
      </div>
    `;
  }

  // ---------------------------------------------------------------------------
  // Dialogs
  // ---------------------------------------------------------------------------

  private renderLimitDialog() {
    const title = this.editingLimit ? 'Edit Limit Definition' : 'Create Limit Definition';
    // System-seeded limit definitions: only default_value and description
    // can be changed (enforced server-side too, pkg/hub/handlers_quota.go
    // updateLimitDefinition). Keep name/resourceType/unit read-only here so
    // the form can't produce a request the server will 403.
    const isSystemEdit = !!this.editingLimit?.system;

    return html`
      <sl-dialog
        label=${title}
        ?open=${this.showLimitDialog}
        @sl-hide=${(e: Event) => {
          if (e.target !== e.currentTarget) return;
          this.showLimitDialog = false;
        }}
      >
        ${this.limitDialogError
          ? html`<sl-alert variant="danger" open class="dialog-error"
              >${this.limitDialogError}</sl-alert
            >`
          : nothing}
        ${isSystemEdit
          ? html`<sl-alert variant="neutral" open class="dialog-error"
              >This is a system limit definition. Only default value and description can be
              changed.</sl-alert
            >`
          : nothing}

        <div class="form-row">
          <sl-input
            label="Name"
            placeholder="e.g. max-agents-per-project"
            value=${this.limitForm.name}
            @sl-input=${(e: Event) => {
              this.limitForm = { ...this.limitForm, name: (e.target as HTMLInputElement).value };
            }}
            ?readonly=${isSystemEdit}
            required
          ></sl-input>
        </div>

        <div class="form-row">
          <sl-select
            label="Resource Type"
            placeholder="Select resource type"
            value=${this.limitForm.resourceType}
            @sl-change=${(e: Event) => {
              this.limitForm = {
                ...this.limitForm,
                resourceType: (e.target as HTMLSelectElement).value,
              };
            }}
            ?disabled=${isSystemEdit}
            required
          >
            <sl-option value="agent">agent</sl-option>
            <sl-option value="project">project</sl-option>
            <sl-option value="group">group</sl-option>
            <sl-option value="group_member">group_member</sl-option>
            ${this.tokenBudgetsFlagEnabled || this.limitForm.resourceType === 'llm_tokens'
              ? html`<sl-option value="llm_tokens">llm_tokens</sl-option>`
              : nothing}
          </sl-select>
        </div>

        <div class="form-row">
          <sl-input
            label="Unit"
            placeholder="e.g. agents, projects, members"
            value=${this.limitForm.unit}
            @sl-input=${(e: Event) => {
              this.limitForm = { ...this.limitForm, unit: (e.target as HTMLInputElement).value };
            }}
            ?readonly=${isSystemEdit}
          ></sl-input>
        </div>

        <div class="form-row">
          <sl-input
            label="Default Value"
            type="number"
            min="0"
            placeholder="0 = unlimited"
            help-text="Required. Use 0 for unlimited."
            value=${this.limitForm.defaultValue === null ? '' : String(this.limitForm.defaultValue)}
            @sl-input=${(e: Event) => {
              const raw = (e.target as HTMLInputElement).value.trim();
              const parsed = raw === '' ? NaN : Number(raw);
              this.limitForm = {
                ...this.limitForm,
                defaultValue: Number.isInteger(parsed) && parsed >= 0 ? parsed : null,
              };
            }}
          ></sl-input>
        </div>

        <div class="form-row">
          <sl-input
            label="Description"
            placeholder="Optional description"
            value=${this.limitForm.description}
            @sl-input=${(e: Event) => {
              this.limitForm = {
                ...this.limitForm,
                description: (e.target as HTMLInputElement).value,
              };
            }}
          ></sl-input>
        </div>

        <sl-button
          slot="footer"
          variant="primary"
          ?loading=${this.limitDialogSaving}
          @click=${this.saveLimitDefinition}
        >
          ${this.editingLimit ? 'Update' : 'Create'}
        </sl-button>
        <sl-button
          slot="footer"
          @click=${() => {
            this.showLimitDialog = false;
          }}
        >
          Cancel
        </sl-button>
      </sl-dialog>
    `;
  }

  private renderEntitlementDialog() {
    const limit = this.getExpandedLimit();
    const isTokenLimit = Boolean(limit && isLLMTokensResourceType(limit.resourceType));
    const isPooled = Boolean(limit && isPooledGroupLimit(limit.name));
    const isEdit = Boolean(this.editingEntitlement);
    const isSystemDefault = !isPooled && this.entitlementForm.subjectType === 'system_default';

    return html`
      <sl-dialog
        label=${isEdit ? 'Edit Entitlement Binding' : 'Create Entitlement Binding'}
        ?open=${this.showEntitlementDialog}
        data-testid="entitlement-dialog"
        @sl-hide=${(e: Event) => {
          if (e.target !== e.currentTarget) return;
          this.showEntitlementDialog = false;
          this.editingEntitlement = null;
          this.editingEntitlementEtag = null;
          this.entitlementDialogConflict = false;
        }}
      >
        ${this.entitlementDialogError
          ? html`<sl-alert
              variant="danger"
              open
              class="dialog-error"
              data-testid="entitlement-dialog-error"
              >${this.entitlementDialogError}</sl-alert
            >`
          : nothing}
        ${isPooled
          ? html`<sl-alert
              variant="primary"
              open
              class="dialog-error"
              data-testid="pooled-group-binding-notice"
              >Pooled group token bindings apply to an explicit user group (system-wide or scoped to
              a single project). Overlapping group pools are non-additive.</sl-alert
            >`
          : nothing}

        <div class="form-row">
          <sl-select
            label="Subject Type"
            value=${isPooled ? 'group' : this.entitlementForm.subjectType}
            ?disabled=${isPooled}
            data-testid="entitlement-subject-type"
            @sl-change=${(e: Event) => {
              const nextSubjectType = (e.target as HTMLSelectElement).value;
              this.entitlementForm = {
                ...this.entitlementForm,
                subjectType: nextSubjectType,
                subjectId:
                  nextSubjectType === 'system_default' ? '' : this.entitlementForm.subjectId,
              };
            }}
          >
            ${isPooled
              ? html`<sl-option value="group">group</sl-option>`
              : html`
                  <sl-option value="user">user</sl-option>
                  <sl-option value="group">group</sl-option>
                  <sl-option value="system_default">system_default</sl-option>
                `}
          </sl-select>
        </div>

        <div class="form-row">
          <sl-input
            label=${isPooled ? 'Group ID' : 'Subject ID'}
            placeholder=${isSystemDefault
              ? 'Not used for system_default'
              : isPooled
                ? 'Explicit group ID'
                : 'User or group ID'}
            value=${isSystemDefault ? '' : this.entitlementForm.subjectId}
            ?disabled=${isSystemDefault}
            ?required=${!isSystemDefault}
            data-testid="entitlement-subject-id"
            @sl-input=${(e: Event) => {
              this.entitlementForm = {
                ...this.entitlementForm,
                subjectId: (e.target as HTMLInputElement).value,
              };
            }}
          ></sl-input>
        </div>

        <div class="form-row">
          <sl-select
            label="Scope Type"
            value=${this.entitlementForm.scopeType}
            data-testid="entitlement-scope-type"
            @sl-change=${(e: Event) => {
              const nextScopeType = (e.target as HTMLSelectElement).value;
              this.entitlementForm = {
                ...this.entitlementForm,
                scopeType: nextScopeType,
                scopeId: nextScopeType === 'project' ? this.entitlementForm.scopeId : '',
              };
            }}
          >
            <sl-option value="system">system</sl-option>
            <sl-option value="project">project</sl-option>
          </sl-select>
        </div>

        ${this.entitlementForm.scopeType === 'project'
          ? html`
              <div class="form-row">
                <sl-input
                  label="Scope ID (Project ID)"
                  placeholder="Project ID"
                  value=${this.entitlementForm.scopeId}
                  ?required=${isTokenLimit}
                  data-testid="entitlement-scope-id"
                  @sl-input=${(e: Event) => {
                    this.entitlementForm = {
                      ...this.entitlementForm,
                      scopeId: (e.target as HTMLInputElement).value,
                    };
                  }}
                ></sl-input>
              </div>
            `
          : nothing}

        <div class="form-row">
          <sl-input
            label="Value"
            type="number"
            min="0"
            placeholder="0 = unlimited"
            help-text="Use 0 for unlimited."
            value=${String(this.entitlementForm.value)}
            data-testid="entitlement-value"
            @sl-input=${(e: Event) => {
              this.entitlementForm = {
                ...this.entitlementForm,
                value: Number((e.target as HTMLInputElement).value) || 0,
              };
            }}
          ></sl-input>
        </div>

        ${isTokenLimit && this.tokenBudgetsFlagEnabled
          ? html`
              <div class="form-row">
                <sl-select
                  label="Token Breach Action"
                  value=${this.normalizeTokenAction(this.entitlementForm.tokenAction)}
                  help-text="Action taken when token usage reaches 100% of cap."
                  data-testid="entitlement-token-action-select"
                  @sl-change=${(e: Event) => {
                    this.entitlementForm = {
                      ...this.entitlementForm,
                      tokenAction: this.normalizeTokenAction((e.target as HTMLSelectElement).value),
                    };
                  }}
                >
                  <sl-option value="pause_new">Pause new launches (default)</sl-option>
                  <sl-option value="suspend_and_pause">Suspend running &amp; pause new</sl-option>
                  <sl-option value="notify_only">Notify only (shadow)</sl-option>
                </sl-select>
              </div>
            `
          : nothing}

        <sl-button
          slot="footer"
          variant="primary"
          ?loading=${this.entitlementDialogSaving}
          ?disabled=${this.entitlementDialogConflict}
          data-testid="entitlement-submit-btn"
          @click=${this.saveEntitlement}
        >
          ${isEdit ? 'Update' : 'Create'}
        </sl-button>
        <sl-button
          slot="footer"
          @click=${() => {
            this.showEntitlementDialog = false;
            this.editingEntitlement = null;
            this.editingEntitlementEtag = null;
            this.entitlementDialogConflict = false;
          }}
        >
          Cancel
        </sl-button>
      </sl-dialog>
    `;
  }

  private renderDeleteDialog() {
    return html`
      <sl-dialog
        label="Confirm Delete"
        ?open=${this.showDeleteDialog}
        @sl-hide=${(e: Event) => {
          if (e.target !== e.currentTarget) return;
          this.showDeleteDialog = false;
          this.deleteTarget = null;
          this.deleteDialogError = null;
        }}
      >
        ${this.deleteDialogError
          ? html`<sl-alert variant="danger" open class="dialog-error"
              >${this.deleteDialogError}</sl-alert
            >`
          : nothing}
        <p>
          Are you sure you want to delete
          ${this.deleteTarget?.type === 'limit' ? 'limit definition' : 'entitlement binding'}
          <strong>${this.deleteTarget?.name}</strong>? This action cannot be undone.
        </p>
        <sl-button
          slot="footer"
          variant="danger"
          ?loading=${this.deleteLoading}
          @click=${this.executeDelete}
        >
          Delete
        </sl-button>
        <sl-button
          slot="footer"
          @click=${() => {
            this.showDeleteDialog = false;
            this.deleteTarget = null;
          }}
        >
          Cancel
        </sl-button>
      </sl-dialog>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-page-admin-quotas': ScionPageAdminQuotas;
  }
}
