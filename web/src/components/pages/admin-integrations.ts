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

import { LitElement, html, css, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';

import { apiFetch, extractApiError } from '../../client/api.js';

interface IntegrationStatus {
  type: string;
  enabled: boolean;
  running: boolean;
  status: string;
  message?: string;
  details?: Record<string, string>;
  deployment_mode: string;
}

const INTEGRATION_META: Record<string, { label: string; icon: string; description: string }> = {
  telegram: {
    label: 'Telegram',
    icon: 'send',
    description: 'Bidirectional messaging between Telegram groups and Scion agents.',
  },
};

@customElement('scion-page-admin-integrations')
export class ScionPageAdminIntegrations extends LitElement {
  @state() private loading = true;
  @state() private error: string | null = null;
  @state() private integrations: IntegrationStatus[] = [];
  @state() private actionLoading: string | null = null;

  static override styles = css`
    :host {
      display: block;
      padding: 1.5rem 2rem;
      max-width: 56rem;
    }

    .page-header {
      margin-bottom: 1.5rem;
    }

    .page-header h1 {
      font-size: 1.5rem;
      font-weight: 700;
      color: var(--scion-text, #1e293b);
      margin: 0 0 0.25rem 0;
    }

    .page-header p {
      color: var(--scion-text-muted, #64748b);
      margin: 0;
      font-size: 0.875rem;
    }

    .error-banner {
      background: var(--sl-color-danger-50, #fef2f2);
      color: var(--sl-color-danger-700, #b91c1c);
      border: 1px solid var(--sl-color-danger-200, #fecaca);
      border-radius: var(--scion-radius, 0.5rem);
      padding: 0.75rem 1rem;
      margin-bottom: 1rem;
      font-size: 0.875rem;
    }

    .loading-state {
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 1rem;
      padding: 3rem 0;
    }

    .loading-state sl-spinner {
      font-size: 2rem;
    }

    .empty-state {
      text-align: center;
      padding: 3rem 1rem;
      color: var(--scion-text-muted, #64748b);
    }

    .empty-state sl-icon {
      font-size: 2.5rem;
      margin-bottom: 1rem;
    }

    .integration-list {
      display: flex;
      flex-direction: column;
      gap: 1rem;
    }

    .integration-card {
      background: var(--scion-surface, #ffffff);
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-lg, 0.75rem);
      padding: 1.25rem 1.5rem;
    }

    .card-header {
      display: flex;
      align-items: center;
      gap: 0.75rem;
      margin-bottom: 1rem;
    }

    .card-icon {
      display: flex;
      align-items: center;
      justify-content: center;
      width: 2.5rem;
      height: 2.5rem;
      border-radius: var(--scion-radius, 0.5rem);
      background: var(--sl-color-primary-50, #eff6ff);
      color: var(--sl-color-primary-600, #2563eb);
      font-size: 1.25rem;
      flex-shrink: 0;
    }

    .card-title {
      flex: 1;
    }

    .card-title h3 {
      font-size: 1rem;
      font-weight: 600;
      color: var(--scion-text, #1e293b);
      margin: 0;
    }

    .card-title .card-desc {
      font-size: 0.8125rem;
      color: var(--scion-text-muted, #64748b);
      margin: 0.125rem 0 0 0;
    }

    .card-actions {
      display: flex;
      align-items: center;
      gap: 0.5rem;
    }

    .status-badge {
      display: inline-flex;
      align-items: center;
      gap: 0.25rem;
      font-size: 0.75rem;
      font-weight: 600;
      padding: 0.125rem 0.5rem;
      border-radius: 9999px;
      text-transform: uppercase;
      letter-spacing: 0.025em;
    }

    .status-badge.healthy {
      background: var(--sl-color-success-100, #dcfce7);
      color: var(--sl-color-success-700, #15803d);
    }

    .status-badge.stopped,
    .status-badge.disabled {
      background: var(--sl-color-neutral-100, #f1f5f9);
      color: var(--sl-color-neutral-600, #475569);
    }

    .status-badge.degraded,
    .status-badge.unhealthy {
      background: var(--sl-color-warning-100, #fef9c3);
      color: var(--sl-color-warning-700, #a16207);
    }

    .card-details {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(12rem, 1fr));
      gap: 0.75rem;
      padding-top: 1rem;
      border-top: 1px solid var(--scion-border, #e2e8f0);
    }

    .detail-item {
      font-size: 0.8125rem;
    }

    .detail-item .detail-label {
      color: var(--scion-text-muted, #64748b);
      margin-bottom: 0.125rem;
    }

    .detail-item .detail-value {
      color: var(--scion-text, #1e293b);
      font-weight: 500;
    }

    .detail-item .detail-value code {
      background: var(--sl-color-neutral-100, #f1f5f9);
      padding: 0.0625rem 0.25rem;
      border-radius: 0.25rem;
      font-size: 0.8125rem;
    }
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    void this.loadIntegrations();
  }

  private async loadIntegrations(): Promise<void> {
    this.loading = true;
    this.error = null;
    try {
      const res = await apiFetch('/api/v1/admin/integrations');
      if (!res.ok) {
        this.error = await extractApiError(res, 'Failed to load integrations');
        return;
      }
      const data = (await res.json()) as { integrations: IntegrationStatus[] };
      this.integrations = data.integrations ?? [];
    } catch {
      this.error = 'Failed to connect to the server.';
    } finally {
      this.loading = false;
    }
  }

  override render() {
    return html`
      <div class="page-header">
        <h1>Integrations</h1>
        <p>Manage messaging and notification integrations.</p>
      </div>

      ${this.error ? html`<div class="error-banner">${this.error}</div>` : nothing}

      ${this.loading ? html`
        <div class="loading-state">
          <sl-spinner></sl-spinner>
        </div>
      ` : this.integrations.length === 0 ? html`
        <div class="empty-state">
          <sl-icon name="plug"></sl-icon>
          <p>No integrations configured. Complete onboarding to set up your first integration.</p>
        </div>
      ` : html`
        <div class="integration-list">
          ${this.integrations.map(i => this.renderIntegrationCard(i))}
        </div>
      `}
    `;
  }

  private renderIntegrationCard(integration: IntegrationStatus) {
    const meta = INTEGRATION_META[integration.type] ?? {
      label: integration.type,
      icon: 'puzzle',
      description: '',
    };

    const isToggling = this.actionLoading === integration.type;

    return html`
      <div class="integration-card">
        <div class="card-header">
          <div class="card-icon">
            <sl-icon name=${meta.icon}></sl-icon>
          </div>
          <div class="card-title">
            <h3>${meta.label}</h3>
            <div class="card-desc">${meta.description}</div>
          </div>
          <div class="card-actions">
            <span class="status-badge ${integration.status}">${integration.status}</span>
            <sl-switch
              ?checked=${integration.enabled && integration.running}
              ?disabled=${isToggling}
              @sl-change=${(e: Event) => {
                const checked = (e.target as HTMLInputElement).checked;
                void this.toggleIntegration(integration.type, checked);
              }}
            ></sl-switch>
          </div>
        </div>

        ${integration.details ? html`
          <div class="card-details">
            ${integration.details['bot_username'] ? html`
              <div class="detail-item">
                <div class="detail-label">Bot</div>
                <div class="detail-value"><code>${integration.details['bot_username']}</code></div>
              </div>
            ` : nothing}
            ${integration.details['inbound_mode'] ? html`
              <div class="detail-item">
                <div class="detail-label">Inbound Mode</div>
                <div class="detail-value">${integration.details['inbound_mode'] === 'poll' ? 'Polling' : integration.details['inbound_mode']}</div>
              </div>
            ` : nothing}
            <div class="detail-item">
              <div class="detail-label">Deployment</div>
              <div class="detail-value">${integration.deployment_mode === 'subprocess' ? 'Co-located subprocess' : integration.deployment_mode}</div>
            </div>
            ${integration.details['version'] ? html`
              <div class="detail-item">
                <div class="detail-label">Version</div>
                <div class="detail-value">${integration.details['version']}</div>
              </div>
            ` : nothing}
            ${integration.message && integration.running ? html`
              <div class="detail-item">
                <div class="detail-label">Status</div>
                <div class="detail-value">${integration.message}</div>
              </div>
            ` : nothing}
          </div>
        ` : nothing}
      </div>
    `;
  }

  private async toggleIntegration(type: string, enable: boolean): Promise<void> {
    this.actionLoading = type;
    this.error = null;
    try {
      const action = enable ? 'enable' : 'disable';
      const res = await apiFetch(`/api/v1/admin/integrations/${type}/${action}`, {
        method: 'POST',
      });
      if (!res.ok) {
        this.error = await extractApiError(res, `Failed to ${action} ${type}`);
        return;
      }
      await this.loadIntegrations();
    } catch {
      this.error = 'Failed to connect to the server.';
    } finally {
      this.actionLoading = null;
    }
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-page-admin-integrations': ScionPageAdminIntegrations;
  }
}
