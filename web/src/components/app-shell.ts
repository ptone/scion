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
 * Main Application Shell Component
 *
 * Provides the overall layout structure with sidebar navigation and content area.
 * Uses Shoelace components for UI and integrates with shared Scion components.
 */

import { LitElement, html, css } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

// Import shared components
import './shared/nav.js';
import './shared/header.js';
import './shared/breadcrumb.js';
import './shared/debug-panel.js';

import type { User } from '../shared/types.js';
import type { AccessDeniedDetail } from '../client/api.js';

/**
 * Page title configuration
 */
const PAGE_TITLES: Record<string, string> = {
  '/': 'Dashboard',
  '/groves': 'Groves',
  '/agents': 'Agents',
  '/brokers': 'Brokers',
  '/settings': 'Settings',
  '/admin/users': 'Users',
  '/admin/groups': 'Groups',
};

@customElement('scion-app')
export class ScionApp extends LitElement {
  /**
   * Current authenticated user
   */
  @property({ type: Object })
  user: User | null = null;

  /**
   * Current URL path for navigation highlighting
   */
  @property({ type: String })
  currentPath = '/';

  /**
   * Whether the mobile drawer is open
   */
  @state()
  _drawerOpen = false;

  /** Bound listener reference for cleanup */
  private _accessDeniedHandler = this.handleAccessDenied.bind(this);

  static override styles = css`
    :host {
      display: flex;
      height: 100vh;
      height: 100dvh;
      background: var(--scion-bg, #f8fafc);
    }

    /* Desktop sidebar */
    .sidebar {
      display: flex;
      flex-shrink: 0;
      position: sticky;
      top: 0;
      height: 100vh;
    }

    @media (max-width: 768px) {
      .sidebar {
        display: none;
      }
    }

    /* Hide mobile drawer until Shoelace is loaded */
    /* This prevents SSR from rendering a visible duplicate nav */
    sl-drawer:not(:defined) {
      display: none;
    }

    /* Mobile drawer */
    .mobile-drawer {
      --size: 280px;
    }

    .mobile-drawer::part(panel) {
      background: var(--scion-surface, #ffffff);
    }

    .mobile-drawer::part(close-button) {
      color: var(--scion-text, #1e293b);
    }

    .mobile-drawer::part(close-button):hover {
      color: var(--scion-primary, #3b82f6);
    }

    /* Main content area */
    .main {
      flex: 1;
      display: flex;
      flex-direction: column;
      min-width: 0; /* Prevent flex overflow */
    }

    /* Content wrapper */
    .content {
      flex: 1;
      padding: 1.5rem;
      overflow: auto;
      display: flex;
      flex-direction: column;
    }

    @media (max-width: 640px) {
      .content {
        padding: 1rem;
      }
    }

    /* Max width container */
    .content-inner {
      max-width: var(--scion-content-max-width, 1400px);
      margin: 0 auto;
      width: 100%;
      flex: 1;
      display: flex;
      flex-direction: column;
    }

    /* Loading overlay */
    .loading-overlay {
      position: fixed;
      inset: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      background: rgba(255, 255, 255, 0.8);
      z-index: 9999;
      opacity: 0;
      visibility: hidden;
      transition:
        opacity 0.2s ease,
        visibility 0.2s ease;
    }

    .loading-overlay.visible {
      opacity: 1;
      visibility: visible;
    }

    @media (prefers-color-scheme: dark) {
      .loading-overlay {
        background: rgba(15, 23, 42, 0.8);
      }
    }
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    window.addEventListener('scion:access-denied', this._accessDeniedHandler as EventListener);
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    window.removeEventListener('scion:access-denied', this._accessDeniedHandler as EventListener);
  }

  private handleAccessDenied(event: CustomEvent<AccessDeniedDetail>): void {
    const detail = event.detail || {};
    const action = detail.action || 'perform this action on';
    const message = `You don't have permission to ${action} this resource.`;

    const alert = Object.assign(document.createElement('sl-alert'), {
      variant: 'warning',
      closable: true,
      duration: 5000,
    });
    alert.innerHTML = `
      <sl-icon name="exclamation-triangle" slot="icon"></sl-icon>
      ${message}
    `;
    document.body.appendChild(alert);
    void (alert as HTMLElement & { toast(): Promise<void> }).toast();
  }

  override render() {
    const pageTitle = this.getPageTitle();

    return html`
      <!-- Desktop Sidebar -->
      <aside class="sidebar">
        <scion-nav .user=${this.user} .currentPath=${this.currentPath}></scion-nav>
      </aside>

      <!-- Mobile Drawer -->
      <sl-drawer
        class="mobile-drawer"
        ?open=${this._drawerOpen}
        placement="start"
        @sl-hide=${(): void => this.handleDrawerClose()}
      >
        <scion-nav
          .user=${this.user}
          .currentPath=${this.currentPath}
          @nav-click=${(): void => this.handleNavClick()}
        ></scion-nav>
      </sl-drawer>

      <!-- Main Content -->
      <main class="main">
        <scion-header
          .user=${this.user}
          .currentPath=${this.currentPath}
          .pageTitle=${pageTitle}
          ?showMobileMenu=${true}
          @mobile-menu-toggle=${(): void => this.handleMobileMenuToggle()}
          @logout=${(): void => this.handleLogout()}
        ></scion-header>

        <div class="content">
          <div class="content-inner">
            <slot></slot>
          </div>
        </div>
      </main>

      <!-- Debug Panel (only shows in debug mode) -->
      <scion-debug-panel></scion-debug-panel>
    `;
  }

  /**
   * Get the page title based on current path
   */
  private getPageTitle(): string {
    // Check for exact match
    if (PAGE_TITLES[this.currentPath]) {
      return PAGE_TITLES[this.currentPath];
    }

    // Check for pattern matches
    if (this.currentPath === '/groves/new') {
      return 'Create Grove';
    }
    if (this.currentPath.startsWith('/groves/')) {
      return 'Grove Details';
    }
    if (this.currentPath === '/agents/new') {
      return 'Create Agent';
    }
    if (this.currentPath.startsWith('/agents/')) {
      // Check if it's the terminal page
      if (this.currentPath.includes('/terminal')) {
        return 'Terminal';
      }
      return 'Agent Details';
    }

    return 'Page Not Found';
  }

  /**
   * Handle mobile menu toggle
   */
  private handleMobileMenuToggle(): void {
    this._drawerOpen = !this._drawerOpen;
  }

  /**
   * Handle drawer close event
   */
  private handleDrawerClose(): void {
    this._drawerOpen = false;
  }

  /**
   * Handle navigation click from nav component
   */
  private handleNavClick(): void {
    // Close drawer on navigation in mobile
    this._drawerOpen = false;
  }

  /**
   * Handle logout action
   */
  private handleLogout(): void {
    // POST to logout endpoint
    fetch('/auth/logout', {
      method: 'POST',
      credentials: 'include',
    })
      .then(() => {
        // Redirect to login page
        window.location.href = '/auth/login';
      })
      .catch((error) => {
        console.error('Logout failed:', error);
      });
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-app': ScionApp;
  }
}
