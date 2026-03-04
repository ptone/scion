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
 * Agent detail page component
 *
 * Displays detailed information about a single agent
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

import type { PageData, Agent, Grove, Notification } from '../../shared/types.js';
import { can, isTerminalAvailable, getAgentDisplayStatus, isAgentRunning } from '../../shared/types.js';

interface AgentNotificationsResponse {
  userNotifications: Notification[];
  agentNotifications: Notification[];
}
import type { StatusType } from '../shared/status-badge.js';
import { apiFetch } from '../../client/api.js';
import { stateManager } from '../../client/state.js';
import '../shared/status-badge.js';

@customElement('scion-page-agent-detail')
export class ScionPageAgentDetail extends LitElement {
  /**
   * Page data from SSR
   */
  @property({ type: Object })
  pageData: PageData | null = null;

  /**
   * Agent ID from URL
   */
  @property({ type: String })
  agentId = '';

  /**
   * Loading state
   */
  @state()
  private loading = true;

  /**
   * Agent data
   */
  @state()
  private agent: Agent | null = null;

  /**
   * Parent grove data
   */
  @state()
  private grove: Grove | null = null;

  /**
   * Error message if loading failed
   */
  @state()
  private error: string | null = null;

  /**
   * Action in progress
   */
  @state()
  private actionLoading = false;

  /**
   * Notifications about this agent for the current user
   */
  @state()
  private userNotifications: Notification[] = [];

  /**
   * Notifications sent TO this agent
   */
  @state()
  private agentNotifications: Notification[] = [];

  static override styles = css`
    :host {
      display: block;
    }

    .back-link {
      display: inline-flex;
      align-items: center;
      gap: 0.5rem;
      color: var(--scion-text-muted, #64748b);
      text-decoration: none;
      font-size: 0.875rem;
      margin-bottom: 1rem;
    }

    .back-link:hover {
      color: var(--scion-primary, #3b82f6);
    }

    .header {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      margin-bottom: 1.5rem;
      gap: 1rem;
    }

    .header-info {
      flex: 1;
    }

    .header-title {
      display: flex;
      align-items: center;
      gap: 0.75rem;
      margin-bottom: 0.5rem;
    }

    .header-title sl-icon {
      color: var(--scion-primary, #3b82f6);
      font-size: 1.5rem;
    }

    .header h1 {
      font-size: 1.5rem;
      font-weight: 700;
      color: var(--scion-text, #1e293b);
      margin: 0;
    }

    .header-meta {
      display: flex;
      align-items: center;
      gap: 1rem;
      margin-top: 0.5rem;
    }

    .template-badge {
      display: inline-flex;
      align-items: center;
      gap: 0.25rem;
      padding: 0.25rem 0.75rem;
      background: var(--scion-bg-subtle, #f1f5f9);
      border-radius: var(--scion-radius, 0.5rem);
      font-size: 0.875rem;
      color: var(--scion-text-muted, #64748b);
    }

    .grove-link {
      display: inline-flex;
      align-items: center;
      gap: 0.25rem;
      color: var(--scion-text-muted, #64748b);
      text-decoration: none;
      font-size: 0.875rem;
    }

    .grove-link:hover {
      color: var(--scion-primary, #3b82f6);
    }

    .broker-link {
      display: inline-flex;
      align-items: center;
      gap: 0.25rem;
      color: var(--scion-text-muted, #64748b);
      text-decoration: none;
      font-size: 0.875rem;
    }

    .broker-link:hover {
      color: var(--scion-primary, #3b82f6);
    }

    .header-actions {
      display: flex;
      gap: 0.5rem;
      flex-shrink: 0;
    }

    .card {
      background: var(--scion-surface, #ffffff);
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-lg, 0.75rem);
      padding: 1.5rem;
      margin-bottom: 1.5rem;
    }

    .card-title {
      font-size: 1rem;
      font-weight: 600;
      color: var(--scion-text, #1e293b);
      margin: 0 0 1rem 0;
      padding-bottom: 0.75rem;
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
    }

    .info-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
      gap: 1.5rem;
    }

    .info-item {
      display: flex;
      flex-direction: column;
    }

    .info-label {
      font-size: 0.75rem;
      color: var(--scion-text-muted, #64748b);
      text-transform: uppercase;
      letter-spacing: 0.05em;
      margin-bottom: 0.25rem;
    }

    .info-value {
      font-size: 1rem;
      color: var(--scion-text, #1e293b);
    }

    .info-value.mono {
      font-family: var(--scion-font-mono, monospace);
      font-size: 0.875rem;
    }

    .task-summary {
      font-size: 1rem;
      color: var(--scion-text, #1e293b);
      padding: 1rem;
      background: var(--scion-bg-subtle, #f1f5f9);
      border-radius: var(--scion-radius, 0.5rem);
      margin-top: 1rem;
      white-space: pre-wrap;
      line-height: 1.5;
    }

    .status-timeline {
      display: flex;
      flex-direction: column;
      gap: 0.75rem;
    }

    .timeline-item {
      display: flex;
      align-items: flex-start;
      gap: 0.75rem;
    }

    .timeline-dot {
      width: 10px;
      height: 10px;
      border-radius: 50%;
      background: var(--scion-border, #e2e8f0);
      margin-top: 0.35rem;
      flex-shrink: 0;
    }

    .timeline-dot.active {
      background: var(--sl-color-success-500, #22c55e);
    }

    .timeline-content {
      flex: 1;
    }

    .timeline-title {
      font-weight: 500;
      color: var(--scion-text, #1e293b);
    }

    .timeline-time {
      font-size: 0.75rem;
      color: var(--scion-text-muted, #64748b);
    }

    .quick-actions {
      display: flex;
      gap: 1rem;
    }

    .quick-action {
      display: flex;
      flex-direction: column;
      align-items: center;
      padding: 1.5rem;
      background: var(--scion-surface, #ffffff);
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-lg, 0.75rem);
      cursor: pointer;
      transition: all var(--scion-transition-fast, 150ms ease);
      text-decoration: none;
      color: inherit;
      flex: 1;
    }

    .quick-action:hover:not([disabled]) {
      border-color: var(--scion-primary, #3b82f6);
      box-shadow: var(--scion-shadow-md, 0 4px 6px -1px rgba(0, 0, 0, 0.1));
    }

    .quick-action[disabled] {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .quick-action sl-icon {
      font-size: 2rem;
      color: var(--scion-primary, #3b82f6);
      margin-bottom: 0.5rem;
    }

    .quick-action span {
      font-weight: 500;
      color: var(--scion-text, #1e293b);
    }

    /* Notification items (agent detail card) */
    .notif-section-title {
      font-size: 0.8125rem;
      font-weight: 600;
      color: var(--scion-text-muted, #64748b);
      text-transform: uppercase;
      letter-spacing: 0.05em;
      margin: 1rem 0 0.5rem 0;
    }

    .notif-section-title:first-of-type {
      margin-top: 0;
    }

    .notif-list-item {
      display: flex;
      gap: 0.625rem;
      padding: 0.625rem 0;
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
    }

    .notif-list-item:last-child {
      border-bottom: none;
    }

    .notif-icon {
      flex-shrink: 0;
      display: flex;
      align-items: flex-start;
      padding-top: 2px;
    }

    .notif-icon sl-icon {
      font-size: 1rem;
    }

    .notif-icon.status-success sl-icon { color: var(--scion-success, #22c55e); }
    .notif-icon.status-warning sl-icon { color: var(--scion-warning, #f59e0b); }
    .notif-icon.status-danger sl-icon { color: var(--scion-danger, #ef4444); }
    .notif-icon.status-info sl-icon { color: var(--scion-text-muted, #64748b); }

    .notif-body {
      flex: 1;
      min-width: 0;
    }

    .notif-message {
      font-size: 0.8125rem;
      line-height: 1.4;
      color: var(--scion-text, #1e293b);
      word-break: break-word;
      display: -webkit-box;
      -webkit-line-clamp: 2;
      -webkit-box-orient: vertical;
      overflow: hidden;
    }

    .notif-truncation-badge {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      padding: 0 0.375rem;
      margin-top: 0.125rem;
      font-size: 0.6875rem;
      font-weight: 700;
      line-height: 1.25rem;
      border-radius: 0.5rem;
      background: var(--scion-bg-subtle, #f1f5f9);
      color: var(--scion-text-muted, #64748b);
      cursor: pointer;
      letter-spacing: 0.05em;
    }

    .notif-truncation-badge:hover {
      background: var(--scion-border, #e2e8f0);
    }

    .notif-meta {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      margin-top: 0.25rem;
      font-size: 0.6875rem;
      color: var(--scion-text-muted, #64748b);
    }

    .notif-mark-read {
      border: none;
      background: transparent;
      color: var(--scion-text-muted, #64748b);
      font-size: 0.6875rem;
      cursor: pointer;
      padding: 0;
      transition: color 0.15s ease;
    }

    .notif-mark-read:hover {
      color: var(--scion-primary, #3b82f6);
    }

    .notif-empty {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      padding: 1.5rem 0;
      color: var(--scion-text-muted, #64748b);
      font-size: 0.8125rem;
    }

    .notif-empty sl-icon {
      font-size: 1.25rem;
      opacity: 0.4;
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

    .agent-error-banner {
      background: var(--sl-color-danger-50, #fef2f2);
      border: 1px solid var(--sl-color-danger-200, #fecaca);
      border-radius: var(--scion-radius-lg, 0.75rem);
      padding: 1rem 1.25rem;
      margin-bottom: 1.5rem;
      display: flex;
      align-items: flex-start;
      gap: 0.75rem;
    }

    .agent-error-banner sl-icon {
      color: var(--sl-color-danger-500, #ef4444);
      font-size: 1.25rem;
      flex-shrink: 0;
      margin-top: 0.125rem;
    }

    .agent-error-banner .error-content {
      flex: 1;
      min-width: 0;
    }

    .agent-error-banner .error-title {
      font-weight: 600;
      color: var(--sl-color-danger-700, #b91c1c);
      margin-bottom: 0.25rem;
    }

    .agent-error-banner .error-message {
      font-size: 0.875rem;
      color: var(--sl-color-danger-600, #dc2626);
      font-family: var(--scion-font-mono, monospace);
      white-space: pre-wrap;
      word-break: break-word;
    }
  `;

  private boundOnAgentsUpdated = this.onAgentsUpdated.bind(this);
  private boundOnGrovesUpdated = this.onGrovesUpdated.bind(this);
  private relativeTimeInterval: ReturnType<typeof setInterval> | null = null;

  override connectedCallback(): void {
    super.connectedCallback();
    // SSR property bindings (.agentId=) aren't restored during client-side
    // hydration for top-level page components. Fall back to URL parsing.
    if (!this.agentId && typeof window !== 'undefined') {
      const match = window.location.pathname.match(/\/agents\/([^/]+)/);
      if (match) {
        this.agentId = match[1];
      }
    }
    void this.loadData();

    // Listen for real-time updates (scope is set after loadData resolves with groveId)
    stateManager.addEventListener('agents-updated', this.boundOnAgentsUpdated as EventListener);
    stateManager.addEventListener('groves-updated', this.boundOnGrovesUpdated as EventListener);

    // Periodically re-render to keep relative timestamps fresh
    this.relativeTimeInterval = setInterval(() => this.requestUpdate(), 15000);
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    stateManager.removeEventListener('agents-updated', this.boundOnAgentsUpdated as EventListener);
    stateManager.removeEventListener('groves-updated', this.boundOnGrovesUpdated as EventListener);
    if (this.relativeTimeInterval) {
      clearInterval(this.relativeTimeInterval);
      this.relativeTimeInterval = null;
    }
  }

  private onAgentsUpdated(): void {
    const updatedAgent = stateManager.getAgent(this.agentId);
    if (updatedAgent && this.agent) {
      this.agent = { ...this.agent, ...updatedAgent };
    }
  }

  private onGrovesUpdated(): void {
    if (this.agent?.groveId) {
      const updatedGrove = stateManager.getGrove(this.agent.groveId);
      if (updatedGrove && this.grove) {
        this.grove = { ...this.grove, ...updatedGrove };
      }
    }
  }

  private async loadData(): Promise<void> {
    this.loading = true;
    this.error = null;

    try {
      const agentResponse = await apiFetch(`/api/v1/agents/${this.agentId}`);

      if (!agentResponse.ok) {
        const errorData = (await agentResponse.json().catch(() => ({}))) as { message?: string };
        throw new Error(
          errorData.message || `HTTP ${agentResponse.status}: ${agentResponse.statusText}`
        );
      }

      this.agent = (await agentResponse.json()) as Agent;

      // Set SSE scope now that we have the groveId
      if (this.agent.groveId) {
        stateManager.setScope({
          type: 'agent-detail',
          groveId: this.agent.groveId,
          agentId: this.agentId,
        });
      }

      // Try to load grove info
      if (this.agent.groveId) {
        try {
          const groveResponse = await apiFetch(`/api/v1/groves/${this.agent.groveId}`);
          if (groveResponse.ok) {
            this.grove = (await groveResponse.json()) as Grove;
          }
        } catch {
          // Grove loading is optional, don't fail if it doesn't work
        }
      }

      // Load notifications for this agent
      if (this.pageData?.user) {
        try {
          const notifRes = await apiFetch(`/api/v1/notifications?agentId=${this.agentId}`);
          if (notifRes.ok) {
            const data = (await notifRes.json()) as AgentNotificationsResponse;
            this.userNotifications = data.userNotifications ?? [];
            this.agentNotifications = data.agentNotifications ?? [];
          }
        } catch {
          // Notification loading is optional
        }
      }

      // Seed stateManager so SSE delta merging has full baseline data
      stateManager.seedAgents([this.agent]);
      if (this.grove) {
        stateManager.seedGroves([this.grove]);
      }
    } catch (err) {
      console.error('Failed to load agent:', err);
      this.error = err instanceof Error ? err.message : 'Failed to load agent';
    } finally {
      this.loading = false;
    }
  }

  private formatDate(dateString: string): string {
    try {
      const date = new Date(dateString);
      return new Intl.DateTimeFormat('en', {
        month: 'short',
        day: 'numeric',
        year: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
      }).format(date);
    } catch {
      return dateString;
    }
  }

  private formatRelativeTime(dateString: string): string {
    try {
      const date = new Date(dateString);
      const diffMs = Date.now() - date.getTime();
      const diffSeconds = Math.round(diffMs / 1000);
      const diffMinutes = Math.round(diffMs / (1000 * 60));
      const diffHours = Math.round(diffMs / (1000 * 60 * 60));
      const diffDays = Math.round(diffMs / (1000 * 60 * 60 * 24));

      const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

      if (Math.abs(diffSeconds) < 60) {
        return rtf.format(-diffSeconds, 'second');
      } else if (Math.abs(diffMinutes) < 60) {
        return rtf.format(-diffMinutes, 'minute');
      } else if (Math.abs(diffHours) < 24) {
        return rtf.format(-diffHours, 'hour');
      } else {
        return rtf.format(-diffDays, 'day');
      }
    } catch {
      return dateString;
    }
  }

  private async handleAction(action: 'start' | 'stop' | 'delete'): Promise<void> {
    if (!this.agent) return;

    this.actionLoading = true;

    try {
      let response: Response;

      switch (action) {
        case 'start':
          response = await apiFetch(`/api/v1/agents/${this.agentId}/start`, {
            method: 'POST',
          });
          break;
        case 'stop':
          response = await apiFetch(`/api/v1/agents/${this.agentId}/stop`, {
            method: 'POST',
          });
          break;
        case 'delete':
          if (!confirm('Are you sure you want to delete this agent?')) {
            this.actionLoading = false;
            return;
          }
          response = await apiFetch(`/api/v1/agents/${this.agentId}`, {
            method: 'DELETE',
          });
          break;
      }

      if (!response.ok) {
        const errorData = (await response.json().catch(() => ({}))) as {
          message?: string;
          error?: { message?: string };
        };
        throw new Error(
          errorData.error?.message || errorData.message || `Failed to ${action} agent`,
        );
      }

      if (action === 'delete') {
        // Navigate back to agents list
        window.location.href = '/agents';
      } else {
        // Reload data to reflect changes
        await this.loadData();
      }
    } catch (err) {
      console.error(`Failed to ${action} agent:`, err);
      alert(err instanceof Error ? err.message : `Failed to ${action} agent`);
    } finally {
      this.actionLoading = false;
    }
  }

  override render() {
    if (this.loading) {
      return this.renderLoading();
    }

    if (this.error || !this.agent) {
      return this.renderError();
    }

    return html`
      <a href="${this.grove ? `/groves/${this.grove.id}` : '/agents'}" class="back-link">
        <sl-icon name="arrow-left"></sl-icon>
        ${this.grove ? `To ${this.grove.name}` : 'Back to Agents'}
      </a>

      <div class="header">
        <div class="header-info">
          <div class="header-title">
            <sl-icon name="cpu"></sl-icon>
            <h1>${this.agent.name}</h1>
            <scion-status-badge
              status=${getAgentDisplayStatus(this.agent) as StatusType}
              label=${getAgentDisplayStatus(this.agent)}
            ></scion-status-badge>
          </div>
          <div class="header-meta">
            <span class="template-badge">
              <sl-icon name="code-square"></sl-icon>
              ${this.agent.template}
            </span>
            ${this.grove
              ? html`
                  <a href="/groves/${this.grove.id}" class="grove-link">
                    <sl-icon name="folder"></sl-icon>
                    ${this.grove.name}
                  </a>
                `
              : ''}
            ${this.agent.runtimeBrokerId
              ? html`
                  <a href="/brokers/${this.agent.runtimeBrokerId}" class="broker-link">
                    <sl-icon name="hdd-rack"></sl-icon>
                    ${this.agent.runtimeBrokerName || this.agent.runtimeBrokerId}
                  </a>
                `
              : ''}
          </div>
        </div>
        <div class="header-actions">
          ${isAgentRunning(this.agent)
            ? can(this.agent._capabilities, 'stop') ? html`
                <sl-button
                  variant="danger"
                  size="small"
                  outline
                  ?loading=${this.actionLoading}
                  ?disabled=${this.actionLoading}
                  @click=${() => this.handleAction('stop')}
                >
                  <sl-icon slot="prefix" name="stop-circle"></sl-icon>
                  Stop
                </sl-button>
              ` : nothing
            : can(this.agent._capabilities, 'start') ? html`
                <sl-button
                  variant="success"
                  size="small"
                  ?loading=${this.actionLoading}
                  ?disabled=${this.actionLoading}
                  @click=${() => this.handleAction('start')}
                >
                  <sl-icon slot="prefix" name="play-circle"></sl-icon>
                  Start
                </sl-button>
              ` : nothing}
          ${can(this.agent._capabilities, 'delete') ? html`
            <sl-button
              variant="danger"
              size="small"
              ?loading=${this.actionLoading}
              ?disabled=${this.actionLoading}
              @click=${() => this.handleAction('delete')}
            >
              <sl-icon slot="prefix" name="trash"></sl-icon>
              Delete
            </sl-button>
          ` : nothing}
        </div>
      </div>

      ${this.agent.phase === 'error' && (this.agent.detail?.message || this.agent.message)
        ? html`
            <div class="agent-error-banner">
              <sl-icon name="exclamation-octagon"></sl-icon>
              <div class="error-content">
                <div class="error-title">Agent Error</div>
                <div class="error-message">${this.agent.detail?.message || this.agent.message}</div>
              </div>
            </div>
          `
        : ''}

      <!-- Quick Actions -->
      <div class="quick-actions" style="margin-bottom: 1.5rem;">
        ${can(this.agent._capabilities, 'attach') ? html`
          <a
            class="quick-action"
            href="/agents/${this.agentId}/terminal"
            ?disabled=${!isTerminalAvailable(this.agent)}
          >
            <sl-icon name="terminal"></sl-icon>
            <span>Open Terminal</span>
          </a>
        ` : nothing}
        <div class="quick-action" disabled>
          <sl-icon name="file-text"></sl-icon>
          <span>View Logs</span>
        </div>
        <div class="quick-action" disabled>
          <sl-icon name="gear"></sl-icon>
          <span>Settings</span>
        </div>
      </div>

      <!-- Agent Info -->
      <div class="card">
        <h3 class="card-title">Agent Information</h3>
        <div class="info-grid">
          <div class="info-item">
            <span class="info-label">Agent ID</span>
            <span class="info-value mono">${this.agent.id}</span>
          </div>
          <div class="info-item">
            <span class="info-label">Template</span>
            <span class="info-value">${this.agent.template}</span>
          </div>
          <div class="info-item">
            <span class="info-label">Grove</span>
            <span class="info-value">${this.grove?.name || this.agent.groveId}</span>
          </div>
          <div class="info-item">
            <span class="info-label">Created</span>
            <span class="info-value">${this.formatDate(this.agent.createdAt)}</span>
          </div>
          <div class="info-item">
            <span class="info-label">Last Seen</span>
            <span class="info-value"
              >${this.agent.lastSeen
                ? this.formatRelativeTime(this.agent.lastSeen)
                : this.formatRelativeTime(this.agent.updatedAt)}</span
            >
          </div>
        </div>

        ${this.agent.detail?.taskSummary || this.agent.taskSummary
          ? html`
              <h4
                style="margin-top: 1.5rem; margin-bottom: 0.5rem; font-size: 0.875rem; font-weight: 600;"
              >
                Current Task
              </h4>
              <div class="task-summary">${this.agent.detail?.taskSummary || this.agent.taskSummary}</div>
            `
          : ''}
      </div>

      <!-- Status -->
      <div class="card">
        <h3 class="card-title">Status</h3>
        <div class="info-grid">
          <div class="info-item">
            <span class="info-label">Phase</span>
            <span class="info-value">
              <scion-status-badge
                status=${this.agent.phase as StatusType}
                label=${this.agent.phase}
                size="small"
              ></scion-status-badge>
            </span>
          </div>
          <div class="info-item">
            <span class="info-label">Activity</span>
            <span class="info-value">
              ${this.agent.activity
                ? html`<scion-status-badge
                    status=${this.agent.activity as StatusType}
                    label=${this.agent.activity}
                    size="small"
                  ></scion-status-badge>`
                : html`<span style="color: var(--scion-text-muted, #64748b);">—</span>`}
            </span>
          </div>
          ${this.agent.detail?.toolName
            ? html`
                <div class="info-item">
                  <span class="info-label">Tool</span>
                  <span class="info-value mono">${this.agent.detail.toolName}</span>
                </div>
              `
            : ''}
          ${this.agent.detail?.message && this.agent.phase !== 'error'
            ? html`
                <div class="info-item">
                  <span class="info-label">Detail</span>
                  <span class="info-value">${this.agent.detail.message}</span>
                </div>
              `
            : ''}
        </div>
      </div>

      ${this.renderNotificationsCard()}
    `;
  }

  // ---------------------------------------------------------------------------
  // Notifications card
  // ---------------------------------------------------------------------------

  private renderNotificationsCard() {
    const hasUser = this.userNotifications.length > 0;
    const hasAgent = this.agentNotifications.length > 0;

    return html`
      <div class="card">
        <h3 class="card-title">Notifications</h3>
        ${!hasUser && !hasAgent
          ? html`<div class="notif-empty">
              <sl-icon name="bell-slash"></sl-icon>
              <span>No notifications for this agent</span>
            </div>`
          : html`
              ${hasUser
                ? html`
                    ${hasAgent ? html`<div class="notif-section-title">Your Notifications</div>` : nothing}
                    ${this.userNotifications.map((n) => this.renderNotifItem(n, true))}
                  `
                : nothing}
              ${hasAgent
                ? html`
                    ${hasUser ? html`<div class="notif-section-title">Agent Notifications</div>` : nothing}
                    ${this.agentNotifications.map((n) => this.renderNotifItem(n, false))}
                  `
                : nothing}
            `}
      </div>
    `;
  }

  private renderNotifItem(n: Notification, canAck: boolean) {
    return html`
      <div class="notif-list-item">
        <div class="notif-icon ${this.notifStatusClass(n.status)}">
          <sl-icon name=${this.notifStatusIcon(n.status)}></sl-icon>
        </div>
        <div class="notif-body">
          <div class="notif-message">${n.message}</div>
          <sl-tooltip content=${n.message} hoist>
            <span class="notif-truncation-badge" style="display:none">...</span>
          </sl-tooltip>
          <div class="notif-meta">
            <span>${this.formatRelativeTime(n.createdAt)}</span>
            ${canAck && !n.acknowledged
              ? html`<button class="notif-mark-read" @click=${() => this.ackNotification(n.id)}>Mark read</button>`
              : nothing}
          </div>
        </div>
      </div>
    `;
  }

  private notifStatusIcon(status: string): string {
    switch (status) {
      case 'COMPLETED': return 'check-circle-fill';
      case 'WAITING_FOR_INPUT': return 'exclamation-circle-fill';
      case 'LIMITS_EXCEEDED': return 'x-circle-fill';
      default: return 'info-circle-fill';
    }
  }

  private notifStatusClass(status: string): string {
    switch (status) {
      case 'COMPLETED': return 'status-success';
      case 'WAITING_FOR_INPUT': return 'status-warning';
      case 'LIMITS_EXCEEDED': return 'status-danger';
      default: return 'status-info';
    }
  }

  private async ackNotification(id: string): Promise<void> {
    try {
      await apiFetch(`/api/v1/notifications/${id}/ack`, { method: 'POST' });
      this.userNotifications = this.userNotifications.filter((n) => n.id !== id);
    } catch {
      // Ignore
    }
  }

  override updated(changed: Map<string, unknown>): void {
    super.updated(changed);
    this.detectNotifTruncation();
  }

  private detectNotifTruncation(): void {
    const messages = this.shadowRoot?.querySelectorAll('.notif-message');
    if (!messages) return;
    messages.forEach((el) => {
      const badge = el.parentElement?.querySelector('.notif-truncation-badge') as HTMLElement | null;
      if (!badge) return;
      badge.style.display = el.scrollHeight > el.clientHeight ? 'inline-flex' : 'none';
    });
  }

  private renderLoading() {
    return html`
      <div class="loading-state">
        <sl-spinner></sl-spinner>
        <p>Loading agent...</p>
      </div>
    `;
  }

  private renderError() {
    return html`
      <a href="${this.grove ? `/groves/${this.grove.id}` : '/agents'}" class="back-link">
        <sl-icon name="arrow-left"></sl-icon>
        ${this.grove ? `To ${this.grove.name}` : 'Back to Agents'}
      </a>

      <div class="error-state">
        <sl-icon name="exclamation-triangle"></sl-icon>
        <h2>Failed to Load Agent</h2>
        <p>There was a problem loading this agent.</p>
        <div class="error-details">${this.error || 'Agent not found'}</div>
        <sl-button variant="primary" @click=${() => this.loadData()}>
          <sl-icon slot="prefix" name="arrow-clockwise"></sl-icon>
          Retry
        </sl-button>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-page-agent-detail': ScionPageAgentDetail;
  }
}
