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
 * Grove Schedules page component
 *
 * Displays recurring schedules for a grove with full CRUD management.
 */

import { LitElement, html, css } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

import type { PageData } from '../../shared/types.js';
import { apiFetch, extractApiError } from '../../client/api.js';
import '../shared/schedule-list.js';

interface Grove {
  id: string;
  name: string;
  slug?: string;
}

@customElement('scion-page-grove-schedules')
export class ScionPageGroveSchedules extends LitElement {
  @property({ type: Object })
  pageData: PageData | null = null;

  @state() private groveId = '';
  @state() private grove: Grove | null = null;
  @state() private loading = true;
  @state() private error: string | null = null;

  static override styles = css`
    :host {
      display: block;
    }

    .header {
      display: flex;
      align-items: center;
      gap: 0.75rem;
      margin-bottom: 0.5rem;
    }

    .header h1 {
      font-size: 1.5rem;
      font-weight: 700;
      color: var(--scion-text, #1e293b);
      margin: 0;
    }

    .back-link {
      display: inline-flex;
      align-items: center;
      gap: 0.375rem;
      font-size: 0.875rem;
      color: var(--scion-primary, #3b82f6);
      text-decoration: none;
      margin-bottom: 1rem;
    }

    .back-link:hover {
      text-decoration: underline;
    }

    .subtitle {
      font-size: 0.875rem;
      color: var(--scion-text-muted, #64748b);
      margin: 0 0 1.5rem 0;
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
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    this.extractGroveId();
    void this.loadGrove();
  }

  private extractGroveId(): void {
    const path = this.pageData?.path || window.location.pathname;
    const match = path.match(/\/groves\/([^/]+)\/schedules/);
    if (match) {
      this.groveId = decodeURIComponent(match[1]);
    }
  }

  private async loadGrove(): Promise<void> {
    if (!this.groveId) {
      this.error = 'No grove ID found in URL';
      this.loading = false;
      return;
    }

    try {
      const response = await apiFetch(`/api/v1/groves/${encodeURIComponent(this.groveId)}`);
      if (!response.ok) {
        throw new Error(await extractApiError(response, `HTTP ${response.status}`));
      }
      this.grove = (await response.json()) as Grove;
    } catch (err) {
      this.error = err instanceof Error ? err.message : 'Failed to load grove';
    } finally {
      this.loading = false;
    }
  }

  override render() {
    if (this.loading) {
      return html`
        <div class="loading-state">
          <sl-spinner></sl-spinner>
          <p>Loading...</p>
        </div>
      `;
    }

    if (this.error) {
      return html`
        <div class="error-state">
          <sl-icon name="exclamation-triangle"></sl-icon>
          <h2>Failed to Load</h2>
          <div class="error-details">${this.error}</div>
          <sl-button variant="primary" @click=${() => this.loadGrove()}>
            <sl-icon slot="prefix" name="arrow-clockwise"></sl-icon>
            Retry
          </sl-button>
        </div>
      `;
    }

    const groveName = this.grove?.name || this.grove?.slug || this.groveId;

    return html`
      <a class="back-link" href="/groves/${encodeURIComponent(this.groveId)}">
        <sl-icon name="arrow-left"></sl-icon>
        Back to ${groveName}
      </a>

      <div class="header">
        <h1>Recurring Schedules</h1>
      </div>
      <p class="subtitle">Automated recurring tasks for ${groveName}.</p>

      <scion-schedule-list .groveId=${this.groveId}></scion-schedule-list>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-page-grove-schedules': ScionPageGroveSchedules;
  }
}
