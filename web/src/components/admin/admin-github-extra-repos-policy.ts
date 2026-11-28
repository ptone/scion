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
import { customElement, property, state } from 'lit/decorators.js';

import { apiFetch, extractApiError } from '../../client/api.js';
import { hasAnyPermission, type AdminStatus } from '../../lib/admin-permissions.js';
import { FEATURE_FLAGS_CHANGED_EVENT, isFeatureEnabled } from '../../utils/feature-flags.js';

export type GitHubExtraReposPolicy = 'project_admin' | 'hub_admin' | 'off';

interface GitHubAppPolicyResponse {
  extra_repos_policy?: string;
}

function normalizePolicy(value?: string): GitHubExtraReposPolicy {
  if (value === 'hub_admin' || value === 'off') {
    return value;
  }
  return 'project_admin';
}

@customElement('scion-admin-github-extra-repos-policy')
export class ScionAdminGitHubExtraReposPolicy extends LitElement {
  @property({ type: Boolean }) appConfigured = false;
  @property({ type: Number }) configSaveCounter = 0;
  @property({ type: Object }) adminStatus: AdminStatus | null = null;

  @state() private enabled = false;
  @state() private loaded = false;
  @state() private loading = false;
  @state() private saving = false;
  @state() private policy: GitHubExtraReposPolicy = 'project_admin';
  @state() private savedPolicy: GitHubExtraReposPolicy = 'project_admin';
  @state() private error: string | null = null;
  @state() private success: string | null = null;

  private loadedOnce = false;
  private flagsChangedHandler = (): void => {
    this.syncEnabledState();
  };

  static override styles = css`
    :host {
      display: block;
    }

    :host([hidden]) {
      display: none;
    }

    .section {
      background: var(--scion-surface);
      border: 1px solid var(--scion-border);
      border-radius: var(--scion-radius-lg);
      padding: 1.5rem;
    }

    .section-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.75rem;
      flex-wrap: wrap;
      margin: 0 0 1rem 0;
      padding-bottom: 0.75rem;
      border-bottom: 1px solid var(--scion-border);
    }

    .section-title {
      font-size: 1rem;
      font-weight: 600;
      color: var(--scion-text);
      margin: 0;
    }

    .description {
      font-size: 0.875rem;
      color: var(--scion-text-muted);
      margin: 0 0 1rem 0;
    }

    sl-radio-group::part(form-control-label) {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      margin: -1px;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
      border: 0;
    }

    .radio-options {
      display: flex;
      flex-direction: column;
      gap: 0.75rem;
    }

    .option-hint {
      display: block;
      font-size: 0.8125rem;
      color: var(--scion-text-muted);
      margin-top: 0.125rem;
    }

    .actions {
      display: flex;
      align-items: center;
      gap: 0.75rem;
      margin-top: 1.25rem;
    }

    .loading-row {
      display: flex;
      justify-content: center;
      padding: 1.5rem;
    }

    .status-message {
      font-size: 0.875rem;
      padding: 0.75rem 1rem;
      border-radius: var(--scion-radius);
      margin-bottom: 1rem;
    }

    .status-message.error {
      background: var(--scion-error-bg);
      color: var(--scion-error-text);
      border: 1px solid var(--scion-error-border);
    }

    .status-message.success {
      background: var(--scion-success-bg);
      color: var(--scion-success-text);
      border: 1px solid var(--scion-success-border);
    }
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    window.addEventListener(FEATURE_FLAGS_CHANGED_EVENT, this.flagsChangedHandler);
    this.syncEnabledState();
  }

  override disconnectedCallback(): void {
    window.removeEventListener(FEATURE_FLAGS_CHANGED_EVENT, this.flagsChangedHandler);
    super.disconnectedCallback();
  }

  override updated(changed: Map<string, unknown>): void {
    super.updated(changed);
    if (changed.has('configSaveCounter') && this.enabled && this.configSaveCounter > 0) {
      void this.loadPolicy();
    }
  }

  private syncEnabledState(): void {
    const next = isFeatureEnabled('hub.github_extra_repos');
    this.enabled = next;
    this.hidden = !next;
    if (next && !this.loadedOnce) {
      this.loadedOnce = true;
      void this.loadPolicy();
    }
  }

  private get canEdit(): boolean {
    if (!this.adminStatus) {
      return true;
    }
    return hasAnyPermission(this.adminStatus, ['hub.github_app.update']);
  }

  async loadPolicy(): Promise<void> {
    this.loading = true;
    this.error = null;
    try {
      const res = await apiFetch('/api/v1/github-app');
      if (!res.ok) {
        this.error = await extractApiError(res, 'Failed to load extra repositories policy');
        return;
      }
      const data = (await res.json()) as GitHubAppPolicyResponse;
      const normalized = normalizePolicy(data.extra_repos_policy);
      this.policy = normalized;
      this.savedPolicy = normalized;
      this.loaded = true;
    } catch {
      this.error = 'Failed to load extra repositories policy';
    } finally {
      this.loading = false;
    }
  }

  private handlePolicyChange(e: Event): void {
    const target = e.target as HTMLInputElement & { value?: string };
    this.policy = normalizePolicy(target.value);
    this.success = null;
    this.error = null;
  }

  async savePolicy(): Promise<void> {
    if (!this.loaded || !this.canEdit) {
      return;
    }
    this.saving = true;
    this.error = null;
    this.success = null;
    try {
      const res = await apiFetch('/api/v1/github-app', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ extra_repos_policy: this.policy }),
      });
      if (!res.ok) {
        this.error = await extractApiError(res, 'Failed to save extra repositories policy');
        return;
      }
      const data = (await res.json()) as GitHubAppPolicyResponse;
      const normalized = normalizePolicy(data.extra_repos_policy ?? this.policy);
      this.policy = normalized;
      this.savedPolicy = normalized;
      this.success = 'Extra repositories policy saved.';
    } catch {
      this.error = 'Failed to save extra repositories policy';
    } finally {
      this.saving = false;
    }
  }

  override render() {
    if (!this.enabled) {
      return nothing;
    }

    const isDirty = this.policy !== this.savedPolicy;

    return html`
      <div class="section">
        <div class="section-header">
          <h3 class="section-title">Extra Read-Only Repositories Policy</h3>
        </div>

        <p class="description">
          Controls who may configure sibling read-only repositories on projects associated with the
          GitHub App installation. Setting this to Disabled immediately blocks extra-read token
          minting hub-wide.
        </p>

        ${this.error ? html`<div class="status-message error">${this.error}</div>` : nothing}
        ${this.success ? html`<div class="status-message success">${this.success}</div>` : nothing}
        ${this.loading
          ? html`<div class="loading-row"><sl-spinner></sl-spinner></div>`
          : html`
              <sl-radio-group
                label="Extra read-only repositories policy"
                .value=${this.policy}
                @sl-change=${this.handlePolicyChange}
              >
                <div class="radio-options">
                  <sl-radio value="project_admin" ?disabled=${!this.canEdit || this.saving}>
                    Project admins (default)
                    <span class="option-hint">
                      Project admins and hub admins can configure extra read-only repositories in
                      the project's installation.
                    </span>
                  </sl-radio>
                  <sl-radio value="hub_admin" ?disabled=${!this.canEdit || this.saving}>
                    Hub admins only
                    <span class="option-hint">
                      Only hub admins with GitHub App update permission can modify extra read-only
                      repositories.
                    </span>
                  </sl-radio>
                  <sl-radio value="off" ?disabled=${!this.canEdit || this.saving}>
                    Disabled
                    <span class="option-hint">
                      Blocks all edits and immediately refuses extra-read token minting across the
                      hub.
                    </span>
                  </sl-radio>
                </div>
              </sl-radio-group>

              <div class="actions">
                <sl-button
                  variant="primary"
                  size="small"
                  data-action="save-extra-repos-policy"
                  ?loading=${this.saving}
                  ?disabled=${!this.loaded || !this.canEdit || !isDirty || this.saving}
                  @click=${() => this.savePolicy()}
                >
                  Save policy
                </sl-button>
              </div>
            `}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-admin-github-extra-repos-policy': ScionAdminGitHubExtraReposPolicy;
  }
}
