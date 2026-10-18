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
 * Project visitors (ptone/scion#4603): types and helpers shared by the
 * project Visitors tab and the Admin > Federation page.
 *
 * Hub routes (pkg/hub/handlers_project_visitors.go and
 * handlers_project_visitor_rules.go), all 404 while the
 * federation.project_visitors experiment is off and all needing
 * project.manage_visitors on the project:
 *
 *  - GET/POST     /api/v1/projects/{id}/visitors              grants by email
 *  - PATCH/DELETE /api/v1/projects/{id}/visitors/{visitorId}  window / revoke
 *  - GET/POST     /api/v1/projects/{id}/visitors/rules        rules by domain
 *  - PATCH/DELETE /api/v1/projects/{id}/visitors/rules/{id}   window / remove
 *
 * The experiment check and the permission check here only decide what the
 * page shows; the hub enforces both on every request.
 */

import { apiFetch } from '../client/api.js';
import { hasAnyPermission, type AdminStatus } from '../lib/admin-permissions.js';
import { isFeatureEnabled } from '../utils/feature-flags.js';
import { can, type Capabilities } from './types.js';

/** The registered server-layer experiment (pkg/experiments/registry.go). */
export const PROJECT_VISITORS_FLAG = 'federation.project_visitors';

/** The project capability action of project.manage_visitors. */
export const MANAGE_VISITORS_ACTION = 'manage_visitors';

/** One visitor grant, as GET /visitors returns it (ProjectVisitor). */
export interface ProjectVisitor {
  /** The federated account ID; the item routes address it. */
  id: string;
  bindingId: string;
  email: string;
  issuer: string;
  /** pending | active | suspended */
  status: string;
  /** Whether the hub accepts visitors from the issuer now. */
  issuerAccepted: boolean;
  grantedBy?: string;
  notBefore?: string;
  expiresAt?: string;
  createdAt: string;
  /** "email" for a grant by email, "rule" for a binding a visitor rule created. */
  source: 'email' | 'rule';
  sourceRuleId?: string;
}

/** One visitor rule, as GET /visitors/rules returns it (ProjectVisitorRuleView). */
export interface ProjectVisitorRule {
  id: string;
  issuer: string;
  domain: string;
  issuerAccepted: boolean;
  createdBy?: string;
  notBefore?: string;
  expiresAt?: string;
  createdAt: string;
}

/**
 * Whether the page shows the project's Visitors tab: the experiment is on
 * and the caller holds project.manage_visitors on the project.
 */
export function projectVisitorsVisible(capabilities: Capabilities | undefined): boolean {
  return isFeatureEnabled(PROJECT_VISITORS_FLAG) && can(capabilities, MANAGE_VISITORS_ACTION);
}

/** The visitors collection URL of a project. */
export function projectVisitorsUrl(projectId: string): string {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/visitors`;
}

/** The visitor rules collection URL of a project. */
export function projectVisitorRulesUrl(projectId: string): string {
  return `${projectVisitorsUrl(projectId)}/rules`;
}

/** An issuer URL as the hub compares it: trimmed, without trailing slashes. */
export function normalizeIssuer(issuer: string): string {
  return issuer.trim().replace(/\/+$/, '');
}

/**
 * Whether allow_visitor_grants applies to an issuer type ("" means hub),
 * mirroring config.IssuerAllowsVisitorGrantsType.
 */
export function issuerTypeAllowsVisitorGrants(issuerType: string | undefined): boolean {
  return !issuerType || issuerType === 'hub' || issuerType === 'user';
}

/** The trusted issuer fields the visitor issuer choice reads. */
export interface VisitorIssuerSettings {
  issuer_url: string;
  issuer_type?: string;
  allow_visitor_grants?: boolean;
}

/**
 * The issuers a project owner may pick for a new grant or rule: trusted
 * issuers of type user or hub whose allow_visitor_grants toggle is on.
 */
export function visitorGrantIssuers(issuers: readonly VisitorIssuerSettings[]): string[] {
  const out: string[] = [];
  for (const issuer of issuers) {
    if (!issuer.allow_visitor_grants || !issuerTypeAllowsVisitorGrants(issuer.issuer_type)) {
      continue;
    }
    const url = normalizeIssuer(issuer.issuer_url);
    if (url && !out.includes(url)) out.push(url);
  }
  return out;
}

/**
 * Issuers of the project's grants and rules that the hub accepts now, for
 * callers who cannot read the hub's issuer list.
 */
export function acceptedProjectIssuers(
  visitors: readonly ProjectVisitor[],
  rules: readonly ProjectVisitorRule[]
): string[] {
  const out: string[] = [];
  for (const item of [...visitors, ...rules]) {
    if (!item.issuerAccepted) continue;
    const url = normalizeIssuer(item.issuer);
    if (url && !out.includes(url)) out.push(url);
  }
  return out.sort();
}

/**
 * How the Add visitor dialog offers issuers. "picker": a closed list of
 * the issuers that accept visitor grants (hub admins only). "text": a free
 * issuer field with suggestions from the project's own grants and rules;
 * the hub refuses an issuer it does not accept.
 */
export interface VisitorIssuerChoices {
  mode: 'picker' | 'text';
  issuers: string[];
}

/** The hub permission of the Admin > Federation page (hub.config.read). */
export const HUB_CONFIG_READ_PERMISSION = 'hub.config.read';

/**
 * Whether the caller is a hub admin who may read the hub's trusted issuer
 * list (the admin settings route). Only such a caller gets the picker; no
 * other caller sends a request to an admin route.
 */
export function canReadHubIssuers(adminStatus: AdminStatus | null): boolean {
  return (
    adminStatus !== null &&
    (adminStatus.isAdmin || adminStatus.isSuperAdmin) &&
    hasAnyPermission(adminStatus, [HUB_CONFIG_READ_PERMISSION])
  );
}

/**
 * Loads the issuer choices of the Add visitor dialog. A hub admin
 * (canReadHubIssuers) gets a picker of the trusted issuers whose toggle is
 * on, read from the admin settings route. Any other caller gets a text
 * field whose suggestions come only from the project's grants and rules
 * that the caller already reads.
 *
 * Kept in one place so that a later project-scoped issuer route only
 * changes this function.
 */
export async function loadVisitorIssuerChoices(
  adminStatus: AdminStatus | null,
  visitors: readonly ProjectVisitor[],
  rules: readonly ProjectVisitorRule[]
): Promise<VisitorIssuerChoices> {
  const text: VisitorIssuerChoices = {
    mode: 'text',
    issuers: acceptedProjectIssuers(visitors, rules),
  };
  if (!canReadHubIssuers(adminStatus)) return text;
  try {
    const res = await apiFetch('/api/v1/admin/server-config', {
      suppressAccessDeniedToast: true,
    });
    if (res.ok) {
      const data = (await res.json()) as {
        federation?: { trusted_issuers?: VisitorIssuerSettings[] };
      };
      return {
        mode: 'picker',
        issuers: visitorGrantIssuers(data.federation?.trusted_issuers ?? []),
      };
    }
  } catch {
    // Fall through to the text field.
  }
  return text;
}

/**
 * Why an issuer typed into the text field is refused before it is sent, or
 * null when its shape is acceptable: an absolute https URL with a host and
 * no credentials, query or fragment. Whether the hub accepts the issuer is
 * the hub's decision; its refusal is shown as it sends it.
 */
export function issuerShapeError(value: string): string | null {
  const trimmed = value.trim();
  if (!trimmed) return 'Enter the issuer URL';
  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    return 'The issuer must be an https:// URL';
  }
  if (url.protocol !== 'https:' || !url.hostname) return 'The issuer must be an https:// URL';
  if (url.username || url.password || url.search || url.hash || /[?#]/.test(trimmed)) {
    return 'The issuer URL cannot have credentials, a query or a fragment';
  }
  return null;
}

/** Status text shown when the hub admin has turned the issuer's toggle off. */
export const ISSUER_DISABLED_LABEL = 'Issuer disabled by hub admin';

/** The status shown for a grant or rule. */
export function visitorStatusLabel(status: string | undefined, issuerAccepted: boolean): string {
  if (!issuerAccepted) return ISSUER_DISABLED_LABEL;
  switch (status) {
    case 'pending':
      return 'Pending';
    case 'active':
      return 'Active';
    case 'suspended':
      return 'Suspended';
    case undefined:
    case '':
      return 'Active';
    default:
      return status;
  }
}

/** Expiry choices of the add and extend dialogs; '' is no expiry. */
export const VISITOR_EXPIRY_CHOICES: ReadonlyArray<{ value: string; label: string }> = [
  { value: '', label: 'No expiry' },
  { value: '7', label: '7 days' },
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
  { value: '365', label: '1 year' },
];

/** The expiry instant of an expiry choice (days from now), or null for no expiry. */
export function expiryFromChoice(days: string, now: Date = new Date()): string | null {
  const n = Number.parseInt(days, 10);
  if (!days || !Number.isFinite(n) || n <= 0) return null;
  return new Date(now.getTime() + n * 24 * 60 * 60 * 1000).toISOString();
}

/** The domain form the hub expects for a rule: trimmed, lower case, no leading "@". */
export function normalizeVisitorDomain(domain: string): string {
  return domain.trim().replace(/^@/, '').toLowerCase();
}
