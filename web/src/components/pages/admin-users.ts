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
 * Admin Users page component
 *
 * View of all users with admin actions: promote/demote, suspend/reactivate, delete
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';

import type { AdminUser, UserRole } from '../../shared/types.js';
import '../shared/status-badge.js';
import { extractApiError } from '../../client/api.js';

type SortField = 'name' | 'created' | 'lastSeen';
type SortDir = 'asc' | 'desc';
type AdminTab = 'users' | 'allow-list';

interface ConfirmAction {
  title: string;
  message: string;
  variant: 'primary' | 'danger' | 'warning';
  confirmLabel: string;
  user: AdminUser;
  action: () => Promise<void>;
}

interface AllowListEntry {
  id: string;
  email: string;
  note: string;
  addedBy: string;
  inviteId?: string;
  created: string;
}

const PAGE_SIZE = 50;

@customElement('scion-page-admin-users')
export class ScionPageAdminUsers extends LitElement {
  @state()
  private loading = true;

  @state()
  private users: AdminUser[] = [];

  @state()
  private error: string | null = null;

  @state()
  private sortField: SortField = 'name';

  @state()
  private sortDir: SortDir = 'asc';

  @state()
  private totalCount = 0;

  @state()
  private currentPage = 1;

  @state()
  private nextCursor: string | null = null;

  @state()
  private cursorHistory: string[] = [];

  @state()
  private currentUserId: string | null = null;

  @state()
  private confirmAction: ConfirmAction | null = null;

  @state()
  private actionInProgress = false;

  @state()
  private actionFeedback: { message: string; variant: 'success' | 'danger' } | null = null;

  @state()
  private activeTab: AdminTab = 'users';

  @state()
  private allowListEntries: AllowListEntry[] = [];

  @state()
  private allowListLoading = false;

  @state()
  private allowListTotalCount = 0;

  @state()
  private showAddEmailDialog = false;

  @state()
  private addEmailValue = '';

  @state()
  private addEmailNote = '';

  @state()
  private addEmailInProgress = false;

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

    .user-count {
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

    th.sortable {
      cursor: pointer;
      user-select: none;
    }

    th.sortable:hover {
      color: var(--scion-text, #1e293b);
    }

    .sort-indicator {
      display: inline-block;
      margin-left: 0.25rem;
      font-size: 0.625rem;
      vertical-align: middle;
      opacity: 0.4;
    }

    th.sorted .sort-indicator {
      opacity: 1;
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

    tr:hover td {
      background: var(--scion-bg-subtle, #f1f5f9);
    }

    .user-identity {
      display: flex;
      align-items: center;
      gap: 0.75rem;
    }

    .user-avatar {
      width: 2rem;
      height: 2rem;
      border-radius: 50%;
      background: var(--scion-primary, #3b82f6);
      color: white;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 0.75rem;
      font-weight: 600;
      flex-shrink: 0;
      overflow: hidden;
    }

    .user-avatar img {
      width: 100%;
      height: 100%;
      object-fit: cover;
    }

    .user-info {
      display: flex;
      flex-direction: column;
      min-width: 0;
    }

    .user-name {
      font-weight: 500;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    .user-email {
      font-size: 0.75rem;
      color: var(--scion-text-muted, #64748b);
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    .role-badge {
      display: inline-flex;
      align-items: center;
      padding: 0.125rem 0.5rem;
      border-radius: 9999px;
      font-size: 0.75rem;
      font-weight: 500;
    }

    .role-badge.admin {
      background: var(--sl-color-warning-100, #fef3c7);
      color: var(--sl-color-warning-700, #a16207);
    }

    .role-badge.member {
      background: var(--sl-color-primary-100, #dbeafe);
      color: var(--sl-color-primary-700, #1d4ed8);
    }

    .role-badge.viewer {
      background: var(--scion-bg-subtle, #f1f5f9);
      color: var(--scion-text-muted, #64748b);
    }

    .status-cell {
      display: flex;
      flex-direction: column;
      gap: 0.125rem;
    }

    .status-dot {
      display: inline-flex;
      align-items: center;
      gap: 0.375rem;
      font-size: 0.8125rem;
    }

    .status-dot::before {
      content: '';
      width: 0.5rem;
      height: 0.5rem;
      border-radius: 50%;
      flex-shrink: 0;
    }

    .status-dot.active::before {
      background: var(--sl-color-success-500, #22c55e);
    }

    .status-dot.suspended::before {
      background: var(--sl-color-danger-500, #ef4444);
    }

    .last-seen-text {
      font-size: 0.6875rem;
      color: var(--scion-text-muted, #64748b);
      padding-left: 0.875rem;
    }

    .meta-text {
      font-size: 0.8125rem;
      color: var(--scion-text-muted, #64748b);
    }

    .id-text {
      font-family: var(--scion-font-mono, monospace);
      font-size: 0.75rem;
      color: var(--scion-text-muted, #64748b);
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

    .pagination {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 0.75rem 1rem;
      border-top: 1px solid var(--scion-border, #e2e8f0);
      background: var(--scion-bg-subtle, #f1f5f9);
    }

    .pagination-info {
      font-size: 0.8125rem;
      color: var(--scion-text-muted, #64748b);
    }

    .pagination-controls {
      display: flex;
      align-items: center;
      gap: 0.5rem;
    }

    .pagination-controls sl-button::part(base) {
      font-size: 0.8125rem;
    }

    .page-indicator {
      font-size: 0.8125rem;
      color: var(--scion-text-muted, #64748b);
      padding: 0 0.5rem;
    }

    .actions-cell {
      text-align: right;
      width: 3rem;
    }

    sl-dropdown sl-button::part(base) {
      padding: 0.25rem;
      min-height: unset;
    }

    sl-menu-item::part(base) {
      font-size: 0.8125rem;
    }

    sl-menu-item sl-icon {
      font-size: 1rem;
    }

    .menu-item-danger::part(base) {
      color: var(--sl-color-danger-600, #dc2626);
    }

    .menu-item-danger::part(base):hover {
      background: var(--sl-color-danger-50, #fef2f2);
    }

    .confirm-body {
      font-size: 0.875rem;
      line-height: 1.5;
      color: var(--scion-text, #1e293b);
    }

    .confirm-user {
      display: flex;
      align-items: center;
      gap: 0.75rem;
      padding: 0.75rem;
      margin: 0.75rem 0;
      background: var(--scion-bg-subtle, #f1f5f9);
      border-radius: var(--scion-radius, 0.5rem);
    }

    .feedback-alert {
      margin-bottom: 1rem;
    }

    .tabs {
      display: flex;
      gap: 0;
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
      margin-bottom: 1.5rem;
    }

    .tab-btn {
      padding: 0.625rem 1.25rem;
      font-size: 0.875rem;
      font-weight: 500;
      color: var(--scion-text-muted, #64748b);
      background: none;
      border: none;
      border-bottom: 2px solid transparent;
      cursor: pointer;
      transition: color 0.15s, border-color 0.15s;
    }

    .tab-btn:hover {
      color: var(--scion-text, #1e293b);
    }

    .tab-btn.active {
      color: var(--scion-primary, #3b82f6);
      border-bottom-color: var(--scion-primary, #3b82f6);
    }

    .allow-list-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 1rem;
    }

    .allow-list-header span {
      font-size: 0.875rem;
      color: var(--scion-text-muted, #64748b);
    }

    .add-email-form {
      display: flex;
      flex-direction: column;
      gap: 1rem;
    }

    @media (max-width: 768px) {
      .hide-mobile {
        display: none;
      }
    }
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    void this.loadCurrentUser();
    void this.loadUsers();
  }

  private async loadCurrentUser(): Promise<void> {
    try {
      const res = await fetch('/auth/me', { credentials: 'include' });
      if (res.ok) {
        const data = (await res.json()) as { id?: string };
        this.currentUserId = data.id || null;
      }
    } catch {
      // Non-critical — actions will still work, just can't prevent self-actions
    }
  }

  private async loadUsers(cursor?: string): Promise<void> {
    this.loading = true;
    this.error = null;

    try {
      const params = new URLSearchParams({
        limit: String(PAGE_SIZE),
        sort: this.sortField,
        dir: this.sortDir,
      });
      if (cursor) {
        params.set('cursor', cursor);
      }

      const response = await fetch(`/api/v1/users?${params.toString()}`, {
        credentials: 'include',
      });

      if (!response.ok) {
        throw new Error(await extractApiError(response, `HTTP ${response.status}: ${response.statusText}`));
      }

      const data = (await response.json()) as {
        users?: AdminUser[];
        nextCursor?: string;
        totalCount?: number;
      };
      this.users = Array.isArray(data) ? data : data.users || [];
      this.nextCursor = (data as { nextCursor?: string }).nextCursor || null;
      this.totalCount = (data as { totalCount?: number }).totalCount ?? this.users.length;
    } catch (err) {
      console.error('Failed to load users:', err);
      this.error = err instanceof Error ? err.message : 'Failed to load users';
    } finally {
      this.loading = false;
    }
  }

  private goToNextPage(): void {
    if (!this.nextCursor) return;
    this.cursorHistory = [...this.cursorHistory, this.nextCursor];
    this.currentPage++;
    void this.loadUsers(this.nextCursor);
  }

  private goToPrevPage(): void {
    if (this.currentPage <= 1) return;
    this.currentPage--;
    // Remove the last cursor from history; the one before it is what we navigate to
    const history = [...this.cursorHistory];
    history.pop();
    this.cursorHistory = history;
    const cursor = this.currentPage === 1 ? undefined : history[history.length - 1];
    void this.loadUsers(cursor);
  }

  private async updateUser(userId: string, updates: { role?: string; status?: string }): Promise<void> {
    const response = await fetch(`/api/v1/users/${userId}`, {
      method: 'PATCH',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(updates),
    });
    if (!response.ok) {
      throw new Error(await extractApiError(response, `HTTP ${response.status}`));
    }
  }

  private async deleteUser(userId: string): Promise<void> {
    const response = await fetch(`/api/v1/users/${userId}`, {
      method: 'DELETE',
      credentials: 'include',
    });
    if (!response.ok) {
      throw new Error(await extractApiError(response, `HTTP ${response.status}`));
    }
  }

  private promptChangeRole(user: AdminUser, newRole: UserRole): void {
    const action = newRole === 'admin' ? 'Promote' : 'Change role';
    const roleLabel = newRole === 'admin' ? 'an admin' : `a ${newRole}`;
    this.confirmAction = {
      title: `${action} to ${newRole}`,
      message: `Are you sure you want to make this user ${roleLabel}?`,
      variant: newRole === 'admin' ? 'warning' : 'primary',
      confirmLabel: action,
      user,
      action: async () => {
        await this.updateUser(user.id, { role: newRole });
        this.showFeedback('success', `${user.displayName || user.email} is now ${roleLabel}.`);
        void this.loadUsers(this.currentPage > 1 ? this.cursorHistory[this.cursorHistory.length - 1] : undefined);
      },
    };
  }

  private promptToggleSuspend(user: AdminUser): void {
    const suspending = user.status === 'active';
    this.confirmAction = {
      title: suspending ? 'Suspend user' : 'Reactivate user',
      message: suspending
        ? 'This user will be unable to sign in or use the system while suspended.'
        : 'This will restore the user\'s access to the system.',
      variant: suspending ? 'warning' : 'primary',
      confirmLabel: suspending ? 'Suspend' : 'Reactivate',
      user,
      action: async () => {
        const newStatus = suspending ? 'suspended' : 'active';
        await this.updateUser(user.id, { status: newStatus });
        this.showFeedback('success', `${user.displayName || user.email} has been ${suspending ? 'suspended' : 'reactivated'}.`);
        void this.loadUsers(this.currentPage > 1 ? this.cursorHistory[this.cursorHistory.length - 1] : undefined);
      },
    };
  }

  private promptDelete(user: AdminUser): void {
    this.confirmAction = {
      title: 'Delete user',
      message: 'This action is permanent and cannot be undone. All data associated with this user will be removed.',
      variant: 'danger',
      confirmLabel: 'Delete',
      user,
      action: async () => {
        await this.deleteUser(user.id);
        this.showFeedback('success', `${user.displayName || user.email} has been deleted.`);
        void this.loadUsers(this.currentPage > 1 ? this.cursorHistory[this.cursorHistory.length - 1] : undefined);
      },
    };
  }

  private async executeConfirmedAction(): Promise<void> {
    if (!this.confirmAction) return;
    this.actionInProgress = true;
    try {
      await this.confirmAction.action();
    } catch (err) {
      this.showFeedback('danger', err instanceof Error ? err.message : 'Action failed');
    } finally {
      this.actionInProgress = false;
      this.confirmAction = null;
    }
  }

  private showFeedback(variant: 'success' | 'danger', message: string): void {
    this.actionFeedback = { variant, message };
    setTimeout(() => { this.actionFeedback = null; }, 5000);
  }

  private isSelf(user: AdminUser): boolean {
    return !!this.currentUserId && user.id === this.currentUserId;
  }

  private formatRelativeTime(dateString: string | undefined): string {
    if (!dateString) return 'Never';
    try {
      const date = new Date(dateString);
      if (isNaN(date.getTime())) return 'Never';
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

  private getInitials(name: string): string {
    return name
      .split(/\s+/)
      .map((w) => w[0])
      .join('')
      .toUpperCase()
      .slice(0, 2);
  }

  private toggleSort(field: SortField): void {
    if (this.sortField === field) {
      this.sortDir = this.sortDir === 'asc' ? 'desc' : 'asc';
    } else {
      this.sortField = field;
      this.sortDir = field === 'name' ? 'asc' : 'desc';
    }
    // Reset pagination and re-fetch with new sort applied server-side
    this.currentPage = 1;
    this.cursorHistory = [];
    this.nextCursor = null;
    void this.loadUsers();
  }

  private sortIndicator(field: SortField): string {
    return this.sortField === field ? (this.sortDir === 'asc' ? '▲' : '▼') : '▲';
  }

  private get totalPages(): number {
    return Math.max(1, Math.ceil(this.totalCount / PAGE_SIZE));
  }

  private get rangeStart(): number {
    return (this.currentPage - 1) * PAGE_SIZE + 1;
  }

  private get rangeEnd(): number {
    return Math.min(this.currentPage * PAGE_SIZE, this.totalCount);
  }

  override render() {
    return html`
      <div class="header">
        <h1>Users</h1>
      </div>

      ${this.actionFeedback
        ? html`
            <sl-alert
              class="feedback-alert"
              variant=${this.actionFeedback.variant}
              open
              closable
              duration="5000"
              @sl-after-hide=${() => { this.actionFeedback = null; }}
            >
              <sl-icon slot="icon" name=${this.actionFeedback.variant === 'success' ? 'check-circle' : 'exclamation-triangle'}></sl-icon>
              ${this.actionFeedback.message}
            </sl-alert>
          `
        : nothing}

      <div class="tabs">
        <button
          class="tab-btn ${this.activeTab === 'users' ? 'active' : ''}"
          @click=${() => { this.activeTab = 'users'; }}
        >Users ${!this.loading ? `(${this.totalCount})` : ''}</button>
        <button
          class="tab-btn ${this.activeTab === 'allow-list' ? 'active' : ''}"
          @click=${() => { this.activeTab = 'allow-list'; this.loadAllowList(); }}
        >Allow List ${this.allowListTotalCount > 0 ? `(${this.allowListTotalCount})` : ''}</button>
      </div>

      ${this.activeTab === 'users'
        ? this.loading ? this.renderLoading() : this.error ? this.renderError() : this.renderUsers()
        : this.renderAllowListTab()}

      ${this.renderConfirmDialog()}
      ${this.renderAddEmailDialog()}
    `;
  }

  private renderLoading() {
    return html`
      <div class="loading-state">
        <sl-spinner></sl-spinner>
        <p>Loading users...</p>
      </div>
    `;
  }

  private renderError() {
    return html`
      <div class="error-state">
        <sl-icon name="exclamation-triangle"></sl-icon>
        <h2>Failed to Load Users</h2>
        <p>There was a problem connecting to the API.</p>
        <div class="error-details">${this.error}</div>
        <sl-button variant="primary" @click=${() => this.loadUsers()}>
          <sl-icon slot="prefix" name="arrow-clockwise"></sl-icon>
          Retry
        </sl-button>
      </div>
    `;
  }

  private renderUsers() {
    if (this.users.length === 0) {
      return html`
        <div class="empty-state">
          <sl-icon name="people"></sl-icon>
          <h2>No Users Found</h2>
          <p>There are no users registered in the system.</p>
        </div>
      `;
    }

    const hasPagination = this.totalCount > PAGE_SIZE;

    return html`
      <div class="table-container">
        <table>
          <thead>
            <tr>
              <th
                class="sortable ${this.sortField === 'name' ? 'sorted' : ''}"
                @click=${() => this.toggleSort('name')}
              >
                User
                <span class="sort-indicator">${this.sortIndicator('name')}</span>
              </th>
              <th>Role</th>
              <th
                class="sortable ${this.sortField === 'lastSeen' ? 'sorted' : ''}"
                @click=${() => this.toggleSort('lastSeen')}
              >
                Status
                <span class="sort-indicator">${this.sortIndicator('lastSeen')}</span>
              </th>
              <th class="hide-mobile">Last Login</th>
              <th
                class="hide-mobile sortable ${this.sortField === 'created' ? 'sorted' : ''}"
                @click=${() => this.toggleSort('created')}
              >
                Created
                <span class="sort-indicator">${this.sortIndicator('created')}</span>
              </th>
              <th class="actions-cell"></th>
            </tr>
          </thead>
          <tbody>
            ${this.users.map((user) => this.renderUserRow(user))}
          </tbody>
        </table>
        ${hasPagination ? this.renderPagination() : ''}
      </div>
    `;
  }

  private renderPagination() {
    return html`
      <div class="pagination">
        <span class="pagination-info">
          Showing ${this.rangeStart}-${this.rangeEnd} of ${this.totalCount}
        </span>
        <div class="pagination-controls">
          <sl-button
            size="small"
            variant="default"
            ?disabled=${this.currentPage <= 1}
            @click=${() => this.goToPrevPage()}
          >
            <sl-icon slot="prefix" name="chevron-left"></sl-icon>
            Previous
          </sl-button>
          <span class="page-indicator">Page ${this.currentPage} of ${this.totalPages}</span>
          <sl-button
            size="small"
            variant="default"
            ?disabled=${!this.nextCursor}
            @click=${() => this.goToNextPage()}
          >
            Next
            <sl-icon slot="suffix" name="chevron-right"></sl-icon>
          </sl-button>
        </div>
      </div>
    `;
  }

  private renderUserRow(user: AdminUser) {
    const self = this.isSelf(user);
    return html`
      <tr>
        <td>
          <div class="user-identity">
            <div class="user-avatar">
              ${user.avatarUrl
                ? html`<img src="${user.avatarUrl}" alt="${user.displayName}" />`
                : this.getInitials(user.displayName || user.email)}
            </div>
            <div class="user-info">
              <span class="user-name">${user.displayName || user.email}</span>
              <span class="user-email">${user.email}</span>
            </div>
          </div>
        </td>
        <td>
          <span class="role-badge ${user.role}">${user.role}</span>
        </td>
        <td>
          <div class="status-cell">
            <span class="status-dot ${user.status}">${user.status}</span>
            ${user.lastSeen
              ? html`<span class="last-seen-text">${this.formatRelativeTime(user.lastSeen)}</span>`
              : ''}
          </div>
        </td>
        <td class="hide-mobile">
          <span class="meta-text">${this.formatRelativeTime(user.lastLogin)}</span>
        </td>
        <td class="hide-mobile">
          <span class="meta-text">${this.formatRelativeTime(user.created)}</span>
        </td>
        <td class="actions-cell">
          ${self
            ? nothing
            : html`
                <sl-dropdown placement="bottom-end" hoist>
                  <sl-button slot="trigger" size="small" variant="text" caret>
                    <sl-icon name="three-dots-vertical"></sl-icon>
                  </sl-button>
                  <sl-menu>
                    ${user.role !== 'admin'
                      ? html`<sl-menu-item @click=${() => this.promptChangeRole(user, 'admin')}>
                          <sl-icon slot="prefix" name="shield-check"></sl-icon>
                          Promote to Admin
                        </sl-menu-item>`
                      : nothing}
                    ${user.role === 'admin'
                      ? html`<sl-menu-item @click=${() => this.promptChangeRole(user, 'member')}>
                          <sl-icon slot="prefix" name="person"></sl-icon>
                          Demote to Member
                        </sl-menu-item>`
                      : nothing}
                    ${user.role !== 'viewer'
                      ? html`<sl-menu-item @click=${() => this.promptChangeRole(user, 'viewer')}>
                          <sl-icon slot="prefix" name="eye"></sl-icon>
                          Set as Viewer
                        </sl-menu-item>`
                      : nothing}
                    <sl-divider></sl-divider>
                    ${user.status === 'active'
                      ? html`<sl-menu-item @click=${() => this.promptToggleSuspend(user)}>
                          <sl-icon slot="prefix" name="slash-circle"></sl-icon>
                          Suspend
                        </sl-menu-item>`
                      : html`<sl-menu-item @click=${() => this.promptToggleSuspend(user)}>
                          <sl-icon slot="prefix" name="check-circle"></sl-icon>
                          Reactivate
                        </sl-menu-item>`}
                    <sl-divider></sl-divider>
                    <sl-menu-item class="menu-item-danger" @click=${() => this.promptDelete(user)}>
                      <sl-icon slot="prefix" name="trash"></sl-icon>
                      Delete
                    </sl-menu-item>
                  </sl-menu>
                </sl-dropdown>
              `}
        </td>
      </tr>
    `;
  }

  private async loadAllowList(): Promise<void> {
    this.allowListLoading = true;
    try {
      const response = await fetch('/api/v1/admin/allow-list', {
        credentials: 'include',
      });
      if (!response.ok) {
        throw new Error(await extractApiError(response, `HTTP ${response.status}`));
      }
      const data = (await response.json()) as {
        items: AllowListEntry[];
        totalCount: number;
      };
      this.allowListEntries = data.items || [];
      this.allowListTotalCount = data.totalCount ?? 0;
    } catch (err) {
      this.showFeedback('danger', err instanceof Error ? err.message : 'Failed to load allow list');
    } finally {
      this.allowListLoading = false;
    }
  }

  private async addToAllowList(): Promise<void> {
    const email = this.addEmailValue.trim().toLowerCase();
    if (!email || !email.includes('@')) return;

    this.addEmailInProgress = true;
    try {
      const response = await fetch('/api/v1/admin/allow-list', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, note: this.addEmailNote }),
      });
      if (!response.ok) {
        throw new Error(await extractApiError(response, `HTTP ${response.status}`));
      }
      this.showFeedback('success', `Added ${email} to the allow list.`);
      this.showAddEmailDialog = false;
      this.addEmailValue = '';
      this.addEmailNote = '';
      void this.loadAllowList();
    } catch (err) {
      this.showFeedback('danger', err instanceof Error ? err.message : 'Failed to add email');
    } finally {
      this.addEmailInProgress = false;
    }
  }

  private async removeFromAllowList(email: string): Promise<void> {
    try {
      const response = await fetch(`/api/v1/admin/allow-list/${encodeURIComponent(email)}`, {
        method: 'DELETE',
        credentials: 'include',
      });
      if (!response.ok) {
        throw new Error(await extractApiError(response, `HTTP ${response.status}`));
      }
      this.showFeedback('success', `Removed ${email} from the allow list.`);
      void this.loadAllowList();
    } catch (err) {
      this.showFeedback('danger', err instanceof Error ? err.message : 'Failed to remove email');
    }
  }

  private renderAllowListTab() {
    if (this.allowListLoading) {
      return this.renderLoading();
    }

    return html`
      <div class="allow-list-header">
        <span>${this.allowListTotalCount} email${this.allowListTotalCount !== 1 ? 's' : ''} on the allow list</span>
        <sl-button size="small" variant="primary" @click=${() => { this.showAddEmailDialog = true; }}>
          <sl-icon slot="prefix" name="plus-lg"></sl-icon>
          Add Email
        </sl-button>
      </div>

      ${this.allowListEntries.length === 0
        ? html`
            <div class="empty-state">
              <sl-icon name="shield-lock"></sl-icon>
              <h2>No Allow List Entries</h2>
              <p>When invite-only mode is enabled, only emails on this list (and admin emails) can log in.</p>
            </div>
          `
        : html`
            <div class="table-container">
              <table>
                <thead>
                  <tr>
                    <th>Email</th>
                    <th class="hide-mobile">Note</th>
                    <th class="hide-mobile">Added</th>
                    <th class="actions-cell"></th>
                  </tr>
                </thead>
                <tbody>
                  ${this.allowListEntries.map(
                    (entry) => html`
                      <tr>
                        <td>${entry.email}</td>
                        <td class="hide-mobile"><span class="meta-text">${entry.note || '-'}</span></td>
                        <td class="hide-mobile"><span class="meta-text">${this.formatRelativeTime(entry.created)}</span></td>
                        <td class="actions-cell">
                          <sl-button
                            size="small"
                            variant="text"
                            @click=${() => this.removeFromAllowList(entry.email)}
                          >
                            <sl-icon name="trash" style="color: var(--sl-color-danger-600, #dc2626)"></sl-icon>
                          </sl-button>
                        </td>
                      </tr>
                    `,
                  )}
                </tbody>
              </table>
            </div>
          `}
    `;
  }

  private renderAddEmailDialog() {
    if (!this.showAddEmailDialog) return nothing;
    return html`
      <sl-dialog
        label="Add Email to Allow List"
        open
        @sl-request-close=${() => { if (!this.addEmailInProgress) this.showAddEmailDialog = false; }}
      >
        <div class="add-email-form">
          <sl-input
            label="Email address"
            type="email"
            placeholder="user@example.com"
            .value=${this.addEmailValue}
            @sl-input=${(e: Event) => { this.addEmailValue = (e.target as HTMLInputElement).value; }}
            required
          ></sl-input>
          <sl-input
            label="Note (optional)"
            placeholder="e.g., New hire, Q3 contractor"
            .value=${this.addEmailNote}
            @sl-input=${(e: Event) => { this.addEmailNote = (e.target as HTMLInputElement).value; }}
          ></sl-input>
        </div>
        <sl-button
          slot="footer"
          variant="default"
          ?disabled=${this.addEmailInProgress}
          @click=${() => { this.showAddEmailDialog = false; }}
        >Cancel</sl-button>
        <sl-button
          slot="footer"
          variant="primary"
          ?loading=${this.addEmailInProgress}
          ?disabled=${!this.addEmailValue.trim().includes('@')}
          @click=${() => this.addToAllowList()}
        >Add</sl-button>
      </sl-dialog>
    `;
  }

  private renderConfirmDialog() {
    const action = this.confirmAction;
    if (!action) return nothing;
    return html`
      <sl-dialog
        label=${action.title}
        open
        @sl-request-close=${() => { if (!this.actionInProgress) this.confirmAction = null; }}
      >
        <div class="confirm-body">
          <div class="confirm-user">
            <div class="user-avatar">
              ${action.user.avatarUrl
                ? html`<img src="${action.user.avatarUrl}" alt="${action.user.displayName}" />`
                : this.getInitials(action.user.displayName || action.user.email)}
            </div>
            <div class="user-info">
              <span class="user-name">${action.user.displayName || action.user.email}</span>
              <span class="user-email">${action.user.email}</span>
            </div>
          </div>
          <p>${action.message}</p>
        </div>
        <sl-button
          slot="footer"
          variant="default"
          ?disabled=${this.actionInProgress}
          @click=${() => { this.confirmAction = null; }}
        >Cancel</sl-button>
        <sl-button
          slot="footer"
          variant=${action.variant}
          ?loading=${this.actionInProgress}
          @click=${() => this.executeConfirmedAction()}
        >${action.confirmLabel}</sl-button>
      </sl-dialog>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-page-admin-users': ScionPageAdminUsers;
  }
}
