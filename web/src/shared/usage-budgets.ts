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
 * Shared types and presentation helpers for Tokenomics raw-token budgets
 * (ptone/scion#9243). Mirrors pkg/hub/handlers_usage_budgets.go and
 * pkg/hub/handlers_group_usage_budgets.go.
 */

import { formatNumber } from '../utils/format-number.js';

export type BudgetState = 'ok' | 'warn' | 'exhausted';

export type TokenAction = 'pause_new' | 'suspend_and_pause' | 'notify_only';

export type UnmeasuredHarnessPolicy = 'allow' | 'deny';

export const DEFAULT_TOKEN_ACTION: TokenAction = 'pause_new';

export const POOLED_GROUP_LIMIT_NAMES = new Set([
  'llm_tokens_per_group_day',
  'llm_tokens_per_group_month',
]);

export function isPooledGroupLimit(limitName: string): boolean {
  return POOLED_GROUP_LIMIT_NAMES.has(limitName);
}

export function isLLMTokensResourceType(resourceType: string): boolean {
  return resourceType === 'llm_tokens';
}

export interface UsageScopeRef {
  kind: string;
  id: string;
}

export interface UsageBudgetEntry {
  id?: string;
  limit_definition_id: string;
  limit_name: string;
  scope_kind: string;
  scope_id: string;
  project_scope_id?: string;
  window_start_utc: string;
  window_end_utc: string;
  cap: number | null;
  token_action: string;
  consumed: number;
  state: string;
  thresholds_notified: number;
  override_until: string | null;
  state_version: number;
  as_of: string;
}

export interface UsageBudgetsResponse {
  scope: UsageScopeRef;
  as_of: string;
  budgets: UsageBudgetEntry[];
}

export interface GroupBudgetMemberView {
  user_id: string;
  email?: string;
  display_name?: string;
  tokens_summable: number;
  calls: number;
  events: number;
}

export interface GroupBudgetItem extends UsageBudgetEntry {
  non_additive: boolean;
  members?: GroupBudgetMemberView[];
}

export interface GroupUsageBudgetsResponse {
  scope: UsageScopeRef;
  group_name?: string;
  non_additive: boolean;
  as_of: string;
  budgets: GroupBudgetItem[];
}

export interface OverrideBudgetPayload {
  override_until: string | null;
  state_version: number;
  reason?: string;
}

export type BudgetBadgeVariant = 'success' | 'warning' | 'danger' | 'neutral';

export function budgetStateDisplay(state: string): {
  variant: BudgetBadgeVariant;
  label: string;
} {
  switch (state) {
    case 'ok':
      return { variant: 'success', label: 'OK' };
    case 'warn':
      return { variant: 'warning', label: 'Warning' };
    case 'exhausted':
      return { variant: 'danger', label: 'Exhausted' };
    default:
      return { variant: 'neutral', label: state || 'Unknown' };
  }
}

export function tokenActionLabel(action: string | null | undefined): string {
  switch (action || DEFAULT_TOKEN_ACTION) {
    case 'pause_new':
      return 'Pause new launches';
    case 'suspend_and_pause':
      return 'Suspend & pause';
    case 'notify_only':
      return 'Notify only';
    default:
      return action || DEFAULT_TOKEN_ACTION;
  }
}

export function tokenActionBadgeVariant(
  action: string | null | undefined
): 'primary' | 'danger' | 'neutral' {
  switch (action || DEFAULT_TOKEN_ACTION) {
    case 'suspend_and_pause':
      return 'danger';
    case 'notify_only':
      return 'neutral';
    case 'pause_new':
    default:
      return 'primary';
  }
}

export function formatLimitDisplayName(limitName: string): string {
  switch (limitName) {
    case 'llm_tokens_per_project_day':
      return 'Project Daily Budget';
    case 'llm_tokens_per_project_month':
      return 'Project Monthly Budget';
    case 'llm_tokens_per_user_day':
      return 'User Daily Budget';
    case 'llm_tokens_per_user_month':
      return 'User Monthly Budget';
    case 'llm_tokens_per_user_project_day':
      return 'Per-Project User Daily Budget';
    case 'llm_tokens_per_user_project_month':
      return 'Per-Project User Monthly Budget';
    case 'llm_tokens_per_group_day':
      return 'Group Pool Daily Budget';
    case 'llm_tokens_per_group_month':
      return 'Group Pool Monthly Budget';
    default:
      return limitName;
  }
}

export function formatTokenAmount(tokens: number): string {
  if (!Number.isFinite(tokens)) return '0';
  const abs = Math.abs(tokens);
  if (abs >= 1_000_000_000) {
    return `${(tokens / 1_000_000_000).toFixed(2).replace(/\.?0+$/, '')}B`;
  }
  if (abs >= 1_000_000) {
    return `${(tokens / 1_000_000).toFixed(2).replace(/\.?0+$/, '')}M`;
  }
  if (abs >= 1_000) {
    return `${(tokens / 1_000).toFixed(1).replace(/\.0$/, '')}K`;
  }
  return formatNumber(tokens);
}

export function formatTokenExact(tokens: number): string {
  if (!Number.isFinite(tokens)) return '0';
  return formatNumber(tokens);
}

export function budgetUsagePercent(consumed: number, cap: number | null): number | null {
  if (cap === null || cap <= 0) return null;
  return Math.min(100, Math.max(0, Math.round((consumed / cap) * 100)));
}

export function isOverrideActive(
  budget: Pick<UsageBudgetEntry, 'override_until' | 'as_of'>
): boolean {
  if (!budget.override_until) return false;
  const untilMs = Date.parse(budget.override_until);
  if (Number.isNaN(untilMs)) return false;
  const refMs = budget.as_of ? Date.parse(budget.as_of) : Date.now();
  return untilMs > (Number.isNaN(refMs) ? Date.now() : refMs);
}

export function formatUtcTimestamp(iso: string | null | undefined): string {
  if (!iso) return '—';
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return iso;
  const d = new Date(ms);
  const yyyy = d.getUTCFullYear();
  const mm = String(d.getUTCMonth() + 1).padStart(2, '0');
  const dd = String(d.getUTCDate()).padStart(2, '0');
  const hh = String(d.getUTCHours()).padStart(2, '0');
  const min = String(d.getUTCMinutes()).padStart(2, '0');
  return `${yyyy}-${mm}-${dd} ${hh}:${min} UTC`;
}
