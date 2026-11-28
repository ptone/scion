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
 * Provider-neutral readiness renderer for trigger integrations.
 *
 * Accepts a generic `{ ready, missing_events, missing_permissions, error }`
 * payload and renders either:
 * - Full block mode (`compact = false`, default): badge, status message, and
 *   missing events / permissions lists.
 * - One-line summary mode (`compact = true`): inline indicator, concise
 *   summary, and optional link to the full details view.
 */

import { LitElement, html, css, nothing, type TemplateResult } from 'lit';
import { customElement, property } from 'lit/decorators.js';

export interface IntegrationReadiness {
  ready: boolean;
  missing_events?: string[] | null;
  missing_permissions?: string[] | null;
  error?: string | null;
}

export type IntegrationReadinessState = 'ready' | 'missing' | 'error';

export function resolveIntegrationReadinessState(
  readiness: IntegrationReadiness
): IntegrationReadinessState {
  if (readiness.error && readiness.error.trim().length > 0) {
    return 'error';
  }
  return readiness.ready ? 'ready' : 'missing';
}

function pluralize(count: number, singular: string, plural: string): string {
  return `${count} ${count === 1 ? singular : plural}`;
}

@customElement('scion-integration-readiness')
export class ScionIntegrationReadiness extends LitElement {
  @property({ type: Object })
  readiness: IntegrationReadiness | null = null;

  @property({ type: Boolean, reflect: true })
  compact = false;

  @property({ type: String })
  label = 'Triggers';

  @property({ type: String })
  linkHref = '';

  @property({ type: String })
  linkLabel = 'Details';

  static override styles = css`
    :host {
      display: block;
    }

    :host([compact]) {
      display: inline-flex;
      align-items: center;
    }

    .summary {
      display: inline-flex;
      align-items: center;
      gap: 0.5rem;
      font-size: 0.875rem;
      color: var(--scion-text);
      min-width: 0;
    }

    .summary-label {
      font-size: 0.75rem;
      color: var(--scion-text-muted);
      white-space: nowrap;
    }

    .status-dot {
      display: inline-block;
      width: 8px;
      height: 8px;
      border-radius: var(--scion-radius-full, 9999px);
      flex-shrink: 0;
    }

    .status-dot.ready {
      background: var(--sl-color-success-600);
    }

    .status-dot.missing {
      background: var(--sl-color-warning-600);
    }

    .status-dot.error {
      background: var(--sl-color-danger-600);
    }

    .summary-text {
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    .summary-text.error {
      color: var(--sl-color-danger-700);
    }

    .readiness-link {
      color: var(--scion-primary);
      text-decoration: none;
      font-size: 0.8125rem;
      white-space: nowrap;
    }

    .readiness-link:hover {
      text-decoration: underline;
    }

    .block {
      display: flex;
      flex-direction: column;
      gap: 0.75rem;
      padding: 1rem 1.25rem;
      background: var(--scion-surface);
      border: 1px solid var(--scion-border);
      border-radius: var(--scion-radius-lg);
      font-size: 0.875rem;
      color: var(--scion-text);
    }

    .block-header {
      display: flex;
      align-items: center;
      gap: 0.625rem;
      flex-wrap: wrap;
    }

    .block-title {
      font-weight: 600;
      color: var(--scion-text);
    }

    .block-message {
      margin: 0;
      color: var(--scion-text-muted);
    }

    .block-error {
      margin: 0;
      padding: 0.625rem 0.75rem;
      background: var(--scion-bg-subtle);
      border: 1px solid var(--sl-color-danger-200);
      border-radius: var(--scion-radius);
      color: var(--sl-color-danger-700);
      font-size: 0.8125rem;
    }

    .missing-group {
      display: flex;
      flex-direction: column;
      gap: 0.375rem;
    }

    .missing-heading {
      font-size: 0.75rem;
      font-weight: 600;
      color: var(--scion-text-muted);
      text-transform: uppercase;
      letter-spacing: 0.03em;
    }

    .missing-list {
      display: flex;
      flex-wrap: wrap;
      gap: 0.375rem;
      list-style: none;
      margin: 0;
      padding: 0;
    }

    .missing-item code {
      display: inline-block;
      font-family: var(--scion-font-mono);
      font-size: 0.75rem;
      padding: 0.125rem 0.5rem;
      background: var(--scion-bg-subtle);
      border: 1px solid var(--scion-border);
      border-radius: var(--scion-radius);
      color: var(--scion-text);
    }
  `;

  private formatCompactText(
    state: IntegrationReadinessState,
    missingEvents: string[],
    missingPermissions: string[],
    errorText: string
  ): string {
    if (state === 'ready') {
      return 'Ready';
    }
    if (state === 'error') {
      return `Error: ${errorText}`;
    }
    const parts: string[] = [];
    if (missingEvents.length > 0) {
      parts.push(pluralize(missingEvents.length, 'event', 'events'));
    }
    if (missingPermissions.length > 0) {
      parts.push(pluralize(missingPermissions.length, 'permission', 'permissions'));
    }
    if (parts.length > 0) {
      return `Missing ${parts.join(', ')}`;
    }
    return 'Not ready';
  }

  private renderCompact(
    state: IntegrationReadinessState,
    missingEvents: string[],
    missingPermissions: string[],
    errorText: string
  ): TemplateResult {
    const text = this.formatCompactText(state, missingEvents, missingPermissions, errorText);
    return html`
      <div class="summary" data-testid="integration-readiness-summary" data-state=${state}>
        ${this.label ? html`<span class="summary-label">${this.label}</span>` : nothing}
        <span class="status-dot ${state}" aria-hidden="true"></span>
        <span
          class="summary-text ${state}"
          title=${state === 'error' ? errorText : text}
          data-testid="integration-readiness-summary-text"
          >${text}</span
        >
        ${this.linkHref
          ? html`
              <a
                class="readiness-link"
                href=${this.linkHref}
                data-testid="integration-readiness-link"
                >${this.linkLabel}</a
              >
            `
          : nothing}
      </div>
    `;
  }

  private renderBlock(
    state: IntegrationReadinessState,
    missingEvents: string[],
    missingPermissions: string[],
    errorText: string
  ): TemplateResult {
    const badgeVariant = state === 'ready' ? 'success' : state === 'error' ? 'danger' : 'warning';
    const badgeLabel = state === 'ready' ? 'Ready' : state === 'error' ? 'Error' : 'Not ready';

    return html`
      <div class="block" data-testid="integration-readiness-block" data-state=${state}>
        <div class="block-header">
          ${this.label ? html`<span class="block-title">${this.label}</span>` : nothing}
          <sl-badge variant=${badgeVariant} data-testid="integration-readiness-badge"
            >${badgeLabel}</sl-badge
          >
        </div>

        ${state === 'ready'
          ? html`<p class="block-message">All required events and permissions are configured.</p>`
          : nothing}
        ${state === 'error'
          ? html`<p class="block-error" data-testid="integration-readiness-error">${errorText}</p>`
          : nothing}
        ${state === 'missing'
          ? html`
              ${missingEvents.length > 0
                ? html`
                    <div class="missing-group" data-testid="integration-readiness-missing-events">
                      <span class="missing-heading">Missing events</span>
                      <ul class="missing-list">
                        ${missingEvents.map(
                          (ev) => html`<li class="missing-item"><code>${ev}</code></li>`
                        )}
                      </ul>
                    </div>
                  `
                : nothing}
              ${missingPermissions.length > 0
                ? html`
                    <div
                      class="missing-group"
                      data-testid="integration-readiness-missing-permissions"
                    >
                      <span class="missing-heading">Missing permissions</span>
                      <ul class="missing-list">
                        ${missingPermissions.map(
                          (perm) => html`<li class="missing-item"><code>${perm}</code></li>`
                        )}
                      </ul>
                    </div>
                  `
                : nothing}
              ${missingEvents.length === 0 && missingPermissions.length === 0
                ? html`<p class="block-message" data-testid="integration-readiness-not-ready">
                    Integration is not ready yet. Check hub webhook configuration.
                  </p>`
                : nothing}
            `
          : nothing}
      </div>
    `;
  }

  override render() {
    if (!this.readiness) {
      return nothing;
    }

    const state = resolveIntegrationReadinessState(this.readiness);
    const missingEvents = Array.isArray(this.readiness.missing_events)
      ? this.readiness.missing_events
      : [];
    const missingPermissions = Array.isArray(this.readiness.missing_permissions)
      ? this.readiness.missing_permissions
      : [];
    const errorText = (this.readiness.error ?? '').trim();

    return this.compact
      ? this.renderCompact(state, missingEvents, missingPermissions, errorText)
      : this.renderBlock(state, missingEvents, missingPermissions, errorText);
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-integration-readiness': ScionIntegrationReadiness;
  }
}
