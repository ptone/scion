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
 * Grove detail page component
 *
 * Displays a single grove with its agents and settings
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

import type { PageData, Grove, Agent, Capabilities } from '../../shared/types.js';
import { can, canAny, getAgentDisplayStatus, isAgentRunning, isTerminalAvailable } from '../../shared/types.js';
import type { StatusType } from '../shared/status-badge.js';
import { apiFetch } from '../../client/api.js';
import { stateManager } from '../../client/state.js';
import '../shared/status-badge.js';

@customElement('scion-page-grove-detail')
export class ScionPageGroveDetail extends LitElement {
  /**
   * Page data from SSR
   */
  @property({ type: Object })
  pageData: PageData | null = null;

  /**
   * Grove ID from URL
   */
  @property({ type: String })
  groveId = '';

  /**
   * Loading state
   */
  @state()
  private loading = true;

  /**
   * Grove data
   */
  @state()
  private grove: Grove | null = null;

  /**
   * Agents in this grove
   */
  @state()
  private agents: Agent[] = [];

  /**
   * Error message if loading failed
   */
  @state()
  private error: string | null = null;

  /**
   * Loading state for actions
   */
  @state()
  private actionLoading: Record<string, boolean> = {};

  /**
   * Scope-level capabilities from the agents list response
   */
  @state()
  private agentScopeCapabilities: Capabilities | undefined;

  /**
   * Workspace files for hub-native groves
   */
  @state()
  private workspaceFiles: Array<{
    path: string;
    size: number;
    modTime: string;
    mode: string;
  }> = [];

  /**
   * Workspace loading state
   */
  @state()
  private workspaceLoading = false;

  /**
   * Total size of workspace files
   */
  @state()
  private workspaceTotalSize = 0;

  /**
   * Upload in progress
   */
  @state()
  private uploadProgress = false;

  /**
   * Loading state for stop-all action
   */
  @state()
  private stopAllLoading = false;

  /**
   * Workspace error
   */
  @state()
  private workspaceError: string | null = null;

  static override styles = css`
    :host {
      display: block;
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

    .header-path {
      font-family: var(--scion-font-mono, monospace);
      font-size: 0.875rem;
      color: var(--scion-text-muted, #64748b);
      margin-top: 0.25rem;
      word-break: break-all;
    }

    .header-actions {
      display: flex;
      gap: 0.5rem;
      flex-shrink: 0;
    }

    .stats-row {
      display: flex;
      gap: 2rem;
      margin-bottom: 2rem;
      padding: 1.25rem;
      background: var(--scion-surface, #ffffff);
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-lg, 0.75rem);
    }

    .stat {
      display: flex;
      flex-direction: column;
    }

    .stat-label {
      font-size: 0.75rem;
      color: var(--scion-text-muted, #64748b);
      text-transform: uppercase;
      letter-spacing: 0.05em;
      margin-bottom: 0.25rem;
    }

    .stat-value {
      font-size: 1.5rem;
      font-weight: 700;
      color: var(--scion-text, #1e293b);
    }

    .section-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 1rem;
    }

    .section-header h2 {
      font-size: 1.125rem;
      font-weight: 600;
      color: var(--scion-text, #1e293b);
      margin: 0;
    }

    .agent-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
      gap: 1.5rem;
    }

    .agent-card {
      background: var(--scion-surface, #ffffff);
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-lg, 0.75rem);
      padding: 1.5rem;
      transition: all var(--scion-transition-fast, 150ms ease);
      text-decoration: none;
      color: inherit;
      display: block;
    }

    .agent-card:hover {
      border-color: var(--scion-primary, #3b82f6);
      box-shadow: var(--scion-shadow-md, 0 4px 6px -1px rgba(0, 0, 0, 0.1));
    }

    .agent-header {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      margin-bottom: 0.75rem;
    }

    .agent-name {
      font-size: 1.125rem;
      font-weight: 600;
      color: var(--scion-text, #1e293b);
      margin: 0;
      display: flex;
      align-items: center;
      gap: 0.5rem;
    }

    .agent-name sl-icon {
      color: var(--scion-primary, #3b82f6);
    }

    .agent-meta {
      font-size: 0.813rem;
      color: var(--scion-text-muted, #64748b);
      margin-top: 0.25rem;
    }

    .agent-meta sl-icon {
      font-size: 0.875rem;
      vertical-align: -0.125em;
      opacity: 0.7;
    }

    .agent-meta .broker-link {
      display: inline-flex;
      align-items: center;
      gap: 0.25rem;
      color: var(--scion-text-muted, #64748b);
      text-decoration: none;
    }

    .agent-meta .broker-link:hover {
      color: var(--scion-primary, #3b82f6);
    }

    .agent-task {
      font-size: 0.875rem;
      color: var(--scion-text, #1e293b);
      margin-top: 0.75rem;
      padding: 0.75rem;
      background: var(--scion-bg-subtle, #f1f5f9);
      border-radius: var(--scion-radius, 0.5rem);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .agent-actions {
      display: flex;
      gap: 0.5rem;
      margin-top: 1rem;
      padding-top: 1rem;
      border-top: 1px solid var(--scion-border, #e2e8f0);
    }

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

    .workspace-section {
      margin-bottom: 2rem;
    }

    .workspace-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 1rem;
    }

    .workspace-header-left {
      display: flex;
      align-items: center;
      gap: 0.75rem;
    }

    .workspace-header h2 {
      font-size: 1.125rem;
      font-weight: 600;
      color: var(--scion-text, #1e293b);
      margin: 0;
    }

    .workspace-meta {
      font-size: 0.75rem;
      color: var(--scion-text-muted, #64748b);
    }

    .file-table {
      width: 100%;
      border-collapse: collapse;
      background: var(--scion-surface, #ffffff);
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-lg, 0.75rem);
      overflow: hidden;
    }

    .file-table th,
    .file-table td {
      padding: 0.625rem 1rem;
      text-align: left;
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
    }

    .file-table th {
      font-size: 0.75rem;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: var(--scion-text-muted, #64748b);
      background: var(--scion-bg-subtle, #f8fafc);
      font-weight: 600;
    }

    .file-table tr:last-child td {
      border-bottom: none;
    }

    .file-name {
      font-family: var(--scion-font-mono, monospace);
      font-size: 0.875rem;
      display: flex;
      align-items: center;
      gap: 0.5rem;
    }

    .file-name sl-icon {
      color: var(--scion-text-muted, #64748b);
      flex-shrink: 0;
    }

    .file-size,
    .file-date {
      font-size: 0.8125rem;
      color: var(--scion-text-muted, #64748b);
    }

    .file-actions {
      text-align: right;
    }

    .workspace-empty {
      text-align: center;
      padding: 2.5rem 2rem;
      background: var(--scion-surface, #ffffff);
      border: 1px dashed var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-lg, 0.75rem);
    }

    .workspace-empty > sl-icon {
      font-size: 2.5rem;
      color: var(--scion-text-muted, #64748b);
      opacity: 0.5;
      margin-bottom: 0.75rem;
    }

    .workspace-empty p {
      color: var(--scion-text-muted, #64748b);
      margin: 0 0 1rem 0;
      font-size: 0.875rem;
    }

    .workspace-error {
      color: var(--sl-color-danger-600, #dc2626);
      font-size: 0.875rem;
      padding: 0.75rem 1rem;
      background: var(--sl-color-danger-50, #fef2f2);
      border-radius: var(--scion-radius, 0.5rem);
      margin-bottom: 1rem;
    }
  `;

  private boundOnAgentsUpdated = this.onAgentsUpdated.bind(this);
  private boundOnGrovesUpdated = this.onGrovesUpdated.bind(this);

  override connectedCallback(): void {
    super.connectedCallback();
    // SSR property bindings (.groveId=) aren't restored during client-side
    // hydration for top-level page components. Fall back to URL parsing.
    if (!this.groveId && typeof window !== 'undefined') {
      const match = window.location.pathname.match(/\/groves\/([^/]+)/);
      if (match) {
        this.groveId = match[1];
      }
    }
    void this.loadData();

    // Set SSE scope to this grove (receives all agent events within grove)
    if (this.groveId) {
      stateManager.setScope({ type: 'grove', groveId: this.groveId });
    }

    // Listen for real-time updates
    stateManager.addEventListener('agents-updated', this.boundOnAgentsUpdated as EventListener);
    stateManager.addEventListener('groves-updated', this.boundOnGrovesUpdated as EventListener);
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    stateManager.removeEventListener('agents-updated', this.boundOnAgentsUpdated as EventListener);
    stateManager.removeEventListener('groves-updated', this.boundOnGrovesUpdated as EventListener);
  }

  private onAgentsUpdated(): void {
    const updatedAgents = stateManager.getAgents();
    // Merge SSE agent deltas into local agent list
    const agentMap = new Map(this.agents.map((a) => [a.id, a]));
    for (const agent of updatedAgents) {
      if (agent.groveId === this.groveId || agentMap.has(agent.id)) {
        agentMap.set(agent.id, { ...agentMap.get(agent.id), ...agent } as Agent);
      }
    }
    // Remove agents that were explicitly deleted via SSE
    const deletedIds = stateManager.getDeletedAgentIds();
    for (const id of deletedIds) {
      agentMap.delete(id);
    }
    this.agents = Array.from(agentMap.values());
  }

  private onGrovesUpdated(): void {
    const updatedGrove = stateManager.getGrove(this.groveId);
    if (updatedGrove && this.grove) {
      this.grove = { ...this.grove, ...updatedGrove };
    }
  }

  private async loadData(): Promise<void> {
    this.loading = true;
    this.error = null;

    try {
      // Load grove and agents in parallel
      const [groveResponse, agentsResponse] = await Promise.all([
        apiFetch(`/api/v1/groves/${this.groveId}`),
        apiFetch(`/api/v1/groves/${this.groveId}/agents`),
      ]);

      if (!groveResponse.ok) {
        const errorData = (await groveResponse.json().catch(() => ({}))) as { message?: string };
        throw new Error(
          errorData.message || `HTTP ${groveResponse.status}: ${groveResponse.statusText}`
        );
      }

      this.grove = (await groveResponse.json()) as Grove;

      if (agentsResponse.ok) {
        const agentsData = (await agentsResponse.json()) as
          | { agents?: Agent[]; _capabilities?: Capabilities }
          | Agent[];
        if (Array.isArray(agentsData)) {
          this.agents = agentsData;
          this.agentScopeCapabilities = undefined;
        } else {
          this.agents = agentsData.agents || [];
          this.agentScopeCapabilities = agentsData._capabilities;
        }
      } else {
        // Fallback: if grove-scoped agents endpoint fails, try filtering from all agents
        this.agents = [];
        this.agentScopeCapabilities = undefined;
      }

      // Seed stateManager so SSE delta merging has full baseline data
      stateManager.seedAgents(this.agents);
      if (this.grove) {
        stateManager.seedGroves([this.grove]);
      }

      // Load workspace files for hub-native groves
      if (this.grove && !this.grove.gitRemote) {
        void this.loadWorkspaceFiles();
      }
    } catch (err) {
      console.error('Failed to load grove:', err);
      this.error = err instanceof Error ? err.message : 'Failed to load grove';
    } finally {
      this.loading = false;
    }
  }

  private getGroveStatusVariant(status: string): 'success' | 'warning' | 'danger' | 'neutral' {
    switch (status) {
      case 'active':
        return 'success';
      case 'inactive':
        return 'neutral';
      case 'error':
        return 'danger';
      default:
        return 'neutral';
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

  private async loadWorkspaceFiles(): Promise<void> {
    this.workspaceLoading = true;
    this.workspaceError = null;

    try {
      const response = await apiFetch(`/api/v1/groves/${this.groveId}/workspace/files`);

      if (!response.ok) {
        const errorData = (await response.json().catch(() => ({}))) as { message?: string };
        throw new Error(errorData.message || `HTTP ${response.status}`);
      }

      const data = (await response.json()) as {
        files: Array<{ path: string; size: number; modTime: string; mode: string }>;
        totalSize: number;
        totalCount: number;
      };

      this.workspaceFiles = data.files || [];
      this.workspaceTotalSize = data.totalSize || 0;
    } catch (err) {
      console.error('Failed to load workspace files:', err);
      this.workspaceError = err instanceof Error ? err.message : 'Failed to load files';
    } finally {
      this.workspaceLoading = false;
    }
  }

  private handleUploadClick(): void {
    const input = this.shadowRoot?.querySelector('#workspace-file-input') as HTMLInputElement;
    if (input) {
      input.click();
    }
  }

  private async handleFileUpload(e: Event): Promise<void> {
    const input = e.target as HTMLInputElement;
    const fileList = input.files;
    if (!fileList || fileList.length === 0) return;

    this.uploadProgress = true;
    this.workspaceError = null;

    try {
      const formData = new FormData();
      for (let i = 0; i < fileList.length; i++) {
        const file = fileList[i];
        formData.append(file.name, file);
      }

      const response = await apiFetch(`/api/v1/groves/${this.groveId}/workspace/files`, {
        method: 'POST',
        body: formData,
      });

      if (!response.ok) {
        const errorData = (await response.json().catch(() => ({}))) as { message?: string };
        throw new Error(errorData.message || `Upload failed: HTTP ${response.status}`);
      }

      // Reload file list
      await this.loadWorkspaceFiles();
    } catch (err) {
      console.error('Failed to upload files:', err);
      this.workspaceError = err instanceof Error ? err.message : 'Upload failed';
    } finally {
      this.uploadProgress = false;
      // Reset the input so the same files can be re-selected
      input.value = '';
    }
  }

  private async handleFileDelete(filePath: string): Promise<void> {
    if (!confirm(`Delete ${filePath}?`)) return;

    try {
      // Encode each path segment individually (preserve /)
      const encodedPath = filePath
        .split('/')
        .map((seg) => encodeURIComponent(seg))
        .join('/');

      const response = await apiFetch(
        `/api/v1/groves/${this.groveId}/workspace/files/${encodedPath}`,
        {
          method: 'DELETE',
        }
      );

      if (!response.ok && response.status !== 204) {
        const errorData = (await response.json().catch(() => ({}))) as { message?: string };
        throw new Error(errorData.message || `Delete failed: HTTP ${response.status}`);
      }

      // Reload file list
      await this.loadWorkspaceFiles();
    } catch (err) {
      console.error('Failed to delete file:', err);
      this.workspaceError = err instanceof Error ? err.message : 'Delete failed';
    }
  }

  private handleFileDownload(filePath: string): void {
    const encodedPath = filePath
      .split('/')
      .map((seg) => encodeURIComponent(seg))
      .join('/');
    // Open the download URL in a new context to trigger browser download
    window.open(`/api/v1/groves/${this.groveId}/workspace/files/${encodedPath}`, '_blank');
  }

  private handleDownloadArchive(): void {
    window.open(`/api/v1/groves/${this.groveId}/workspace/archive`, '_blank');
  }

  private formatFileSize(bytes: number): string {
    if (bytes === 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    const size = bytes / Math.pow(1024, i);
    return `${size.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
  }

  private async handleAgentAction(
    agentId: string,
    action: 'start' | 'stop' | 'delete'
  ): Promise<void> {
    this.actionLoading = { ...this.actionLoading, [agentId]: true };

    try {
      let response: Response;

      switch (action) {
        case 'start':
          response = await apiFetch(`/api/v1/agents/${agentId}/start`, {
            method: 'POST',
          });
          break;
        case 'stop':
          response = await apiFetch(`/api/v1/agents/${agentId}/stop`, {
            method: 'POST',
          });
          break;
        case 'delete':
          if (!confirm('Are you sure you want to delete this agent?')) {
            this.actionLoading = { ...this.actionLoading, [agentId]: false };
            return;
          }
          response = await apiFetch(`/api/v1/agents/${agentId}`, {
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
          errorData.error?.message || errorData.message || `Failed to ${action} agent`
        );
      }

      // Reload data to reflect changes
      await this.loadData();
    } catch (err) {
      console.error(`Failed to ${action} agent:`, err);
      alert(err instanceof Error ? err.message : `Failed to ${action} agent`);
    } finally {
      this.actionLoading = { ...this.actionLoading, [agentId]: false };
    }
  }

  private hasRunningAgents(): boolean {
    return this.agents.some((a) => isAgentRunning(a));
  }

  private async handleStopAll(): Promise<void> {
    if (!confirm('Are you sure you want to stop all running agents in this grove?')) {
      return;
    }

    this.stopAllLoading = true;

    try {
      const response = await apiFetch(`/api/v1/groves/${this.groveId}/agents/stop-all`, {
        method: 'POST',
      });

      if (!response.ok) {
        const errorData = (await response.json().catch(() => ({}))) as {
          message?: string;
          error?: { message?: string };
        };
        throw new Error(
          errorData.error?.message || errorData.message || 'Failed to stop all agents'
        );
      }

      const result = (await response.json()) as { stopped: number; failed: number };
      if (result.failed > 0) {
        alert(`Stopped ${result.stopped} agents, ${result.failed} failed.`);
      }

      await this.loadData();
    } catch (err) {
      console.error('Failed to stop all agents:', err);
      alert(err instanceof Error ? err.message : 'Failed to stop all agents');
    } finally {
      this.stopAllLoading = false;
    }
  }

  override render() {
    if (this.loading) {
      return this.renderLoading();
    }

    if (this.error) {
      return this.renderError();
    }

    if (!this.grove) {
      return this.renderError();
    }

    return html`
      <a href="/groves" class="back-link">
        <sl-icon name="arrow-left"></sl-icon>
        Back to Groves
      </a>

      <div class="header">
        <div class="header-info">
          <div class="header-title">
            ${this.grove.gitRemote
              ? html`<sl-icon name="diagram-3"></sl-icon>`
              : html`<sl-icon name="folder-fill"></sl-icon>`}
            <h1>${this.grove.name}</h1>
            <scion-status-badge
              status=${this.getGroveStatusVariant(this.grove.status)}
              label=${this.grove.status}
              size="small"
            ></scion-status-badge>
          </div>
          <div class="header-path">${this.grove.gitRemote || 'Hub Workspace'}</div>
        </div>
        <div class="header-actions">
          ${can(this.agentScopeCapabilities, 'create')
            ? html`
                <a href="/agents/new?groveId=${this.groveId}" style="text-decoration: none;">
                  <sl-button variant="primary" size="small">
                    <sl-icon slot="prefix" name="plus-lg"></sl-icon>
                    New Agent
                  </sl-button>
                </a>
              `
            : nothing}
          ${canAny(this.grove?._capabilities, 'update', 'delete', 'manage')
            ? html`
                <a href="/groves/${this.groveId}/settings" style="text-decoration: none;">
                  <sl-button size="small">
                    <sl-icon slot="prefix" name="gear"></sl-icon>
                    Settings
                  </sl-button>
                </a>
              `
            : nothing}
        </div>
      </div>

      <div class="stats-row">
        <div class="stat">
          <span class="stat-label">Agents</span>
          <span class="stat-value">${this.agents.length}</span>
        </div>
        <div class="stat">
          <span class="stat-label">Running</span>
          <span class="stat-value"
            >${this.agents.filter((a) => isAgentRunning(a)).length}</span
          >
        </div>
        <div class="stat">
          <span class="stat-label">Created</span>
          <span class="stat-value" style="font-size: 1rem; font-weight: 500;">
            ${this.formatDate(this.grove.createdAt)}
          </span>
        </div>
        <div class="stat">
          <span class="stat-label">Updated</span>
          <span class="stat-value" style="font-size: 1rem; font-weight: 500;">
            ${this.formatDate(this.grove.updatedAt)}
          </span>
        </div>
      </div>

      ${!this.grove.gitRemote ? this.renderWorkspaceFiles() : ''}

      <div class="section-header">
        <h2>Agents</h2>
        ${can(this.agentScopeCapabilities, 'stop_all') && this.hasRunningAgents() ? html`
          <sl-button
            variant="danger"
            size="small"
            outline
            ?loading=${this.stopAllLoading}
            ?disabled=${this.stopAllLoading}
            @click=${() => this.handleStopAll()}
          >
            <sl-icon slot="prefix" name="sign-stop"></sl-icon>
            Stop All
          </sl-button>
        ` : nothing}
      </div>

      ${this.agents.length === 0 ? this.renderEmptyAgents() : this.renderAgentGrid()}
    `;
  }

  private renderWorkspaceFiles() {
    return html`
      <div class="workspace-section">
        <div class="workspace-header">
          <div class="workspace-header-left">
            <h2>Workspace Files</h2>
            <span class="workspace-meta">
              ${this.workspaceFiles.length}
              file${this.workspaceFiles.length !== 1 ? 's' : ''}${this.workspaceTotalSize > 0
                ? ` (${this.formatFileSize(this.workspaceTotalSize)})`
                : ''}
            </span>
          </div>
          <div style="display: flex; gap: 0.5rem; align-items: center;">
            ${this.workspaceFiles.length > 0
              ? html`
                  <sl-button
                    size="small"
                    variant="default"
                    @click=${() => this.handleDownloadArchive()}
                  >
                    <sl-icon slot="prefix" name="file-earmark-zip"></sl-icon>
                    Download Zip
                  </sl-button>
                `
              : nothing}
            ${can(this.grove?._capabilities, 'update')
              ? html`
                  <input
                    type="file"
                    id="workspace-file-input"
                    multiple
                    style="display: none"
                    @change=${this.handleFileUpload}
                  />
                  <sl-button
                    size="small"
                    variant="default"
                    ?loading=${this.uploadProgress}
                    ?disabled=${this.uploadProgress}
                    @click=${() => this.handleUploadClick()}
                  >
                    <sl-icon slot="prefix" name="upload"></sl-icon>
                    Upload Files
                  </sl-button>
                `
              : nothing}
          </div>
        </div>

        ${this.workspaceError
          ? html`<div class="workspace-error">${this.workspaceError}</div>`
          : ''}
        ${this.workspaceLoading
          ? html`
              <div class="loading-state" style="padding: 2rem;">
                <sl-spinner></sl-spinner>
                <p>Loading files...</p>
              </div>
            `
          : this.workspaceFiles.length === 0
            ? html`
                <div class="workspace-empty">
                  <sl-icon name="file-earmark"></sl-icon>
                  <p>
                    No files in
                    workspace.${can(this.grove?._capabilities, 'update')
                      ? ' Upload files to seed this grove.'
                      : ''}
                  </p>
                  ${can(this.grove?._capabilities, 'update')
                    ? html`
                        <sl-button
                          size="small"
                          variant="primary"
                          ?loading=${this.uploadProgress}
                          ?disabled=${this.uploadProgress}
                          @click=${() => this.handleUploadClick()}
                        >
                          <sl-icon slot="prefix" name="upload"></sl-icon>
                          Upload Files
                        </sl-button>
                      `
                    : nothing}
                </div>
              `
            : html`
                <table class="file-table">
                  <thead>
                    <tr>
                      <th>Name</th>
                      <th>Size</th>
                      <th>Modified</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    ${this.workspaceFiles.map(
                      (file) => html`
                        <tr>
                          <td>
                            <span class="file-name">
                              <sl-icon name="file-earmark"></sl-icon>
                              ${file.path}
                            </span>
                          </td>
                          <td><span class="file-size">${this.formatFileSize(file.size)}</span></td>
                          <td>
                            <span class="file-date">${this.formatDate(file.modTime)}</span>
                          </td>
                          <td class="file-actions">
                            <sl-icon-button
                              name="download"
                              label="Download ${file.path}"
                              @click=${() => this.handleFileDownload(file.path)}
                            ></sl-icon-button>
                            ${can(this.grove?._capabilities, 'update')
                              ? html`
                                  <sl-icon-button
                                    name="trash"
                                    label="Delete ${file.path}"
                                    @click=${() => this.handleFileDelete(file.path)}
                                  ></sl-icon-button>
                                `
                              : nothing}
                          </td>
                        </tr>
                      `
                    )}
                  </tbody>
                </table>
              `}
      </div>
    `;
  }

  private renderLoading() {
    return html`
      <div class="loading-state">
        <sl-spinner></sl-spinner>
        <p>Loading grove...</p>
      </div>
    `;
  }

  private renderError() {
    return html`
      <a href="/groves" class="back-link">
        <sl-icon name="arrow-left"></sl-icon>
        Back to Groves
      </a>

      <div class="error-state">
        <sl-icon name="exclamation-triangle"></sl-icon>
        <h2>Failed to Load Grove</h2>
        <p>There was a problem loading this grove.</p>
        <div class="error-details">${this.error || 'Grove not found'}</div>
        <sl-button variant="primary" @click=${() => this.loadData()}>
          <sl-icon slot="prefix" name="arrow-clockwise"></sl-icon>
          Retry
        </sl-button>
      </div>
    `;
  }

  private renderEmptyAgents() {
    return html`
      <div class="empty-state">
        <sl-icon name="cpu"></sl-icon>
        <h2>No Agents</h2>
        <p>
          This grove doesn't have any agents
          yet.${can(this.agentScopeCapabilities, 'create')
            ? ' Create your first agent to get started.'
            : ''}
        </p>
        ${can(this.agentScopeCapabilities, 'create')
          ? html`
              <a href="/agents/new?groveId=${this.groveId}" style="text-decoration: none;">
                <sl-button variant="primary">
                  <sl-icon slot="prefix" name="plus-lg"></sl-icon>
                  New Agent
                </sl-button>
              </a>
            `
          : nothing}
      </div>
    `;
  }

  private renderAgentGrid() {
    return html`
      <div class="agent-grid">${this.agents.map((agent) => this.renderAgentCard(agent))}</div>
    `;
  }

  private renderAgentCard(agent: Agent) {
    const isLoading = this.actionLoading[agent.id] || false;

    return html`
      <div class="agent-card">
        <div class="agent-header">
          <div>
            <h3 class="agent-name">
              <sl-icon name="cpu"></sl-icon>
              <a href="/agents/${agent.id}" style="color: inherit; text-decoration: none;">
                ${agent.name}
              </a>
            </h3>
            <div class="agent-meta">
              <div><sl-icon name="code-square"></sl-icon> ${agent.template}</div>
              ${agent.runtimeBrokerId
                ? html`<div>
                    <a href="/brokers/${agent.runtimeBrokerId}" class="broker-link">
                      <sl-icon name="hdd-rack"></sl-icon>
                      ${agent.runtimeBrokerName || agent.runtimeBrokerId}
                    </a>
                  </div>`
                : ''}
            </div>
          </div>
          <scion-status-badge
            status=${getAgentDisplayStatus(agent) as StatusType}
            label=${getAgentDisplayStatus(agent)}
            size="small"
          ></scion-status-badge>
        </div>

        ${agent.taskSummary ? html`<div class="agent-task">${agent.taskSummary}</div>` : ''}

        <div class="agent-actions">
          ${can(agent._capabilities, 'attach')
            ? html`
                <sl-button
                  variant="primary"
                  size="small"
                  href="/agents/${agent.id}/terminal"
                  ?disabled=${!isTerminalAvailable(agent)}
                >
                  <sl-icon slot="prefix" name="terminal"></sl-icon>
                  Terminal
                </sl-button>
              `
            : nothing}
          ${isAgentRunning(agent)
            ? can(agent._capabilities, 'stop')
              ? html`
                  <sl-button
                    variant="danger"
                    size="small"
                    outline
                    ?loading=${isLoading}
                    ?disabled=${isLoading}
                    @click=${() => this.handleAgentAction(agent.id, 'stop')}
                  >
                    <sl-icon slot="prefix" name="stop-circle"></sl-icon>
                    Stop
                  </sl-button>
                `
              : nothing
            : can(agent._capabilities, 'start')
              ? html`
                  <sl-button
                    variant="success"
                    size="small"
                    outline
                    ?loading=${isLoading}
                    ?disabled=${isLoading}
                    @click=${() => this.handleAgentAction(agent.id, 'start')}
                  >
                    <sl-icon slot="prefix" name="play-circle"></sl-icon>
                    Start
                  </sl-button>
                `
              : nothing}
          ${can(agent._capabilities, 'delete')
            ? html`
                <sl-button
                  variant="default"
                  size="small"
                  outline
                  ?loading=${isLoading}
                  ?disabled=${isLoading}
                  @click=${() => this.handleAgentAction(agent.id, 'delete')}
                >
                  <sl-icon slot="prefix" name="trash"></sl-icon>
                </sl-button>
              `
            : nothing}
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-page-grove-detail': ScionPageGroveDetail;
  }
}
