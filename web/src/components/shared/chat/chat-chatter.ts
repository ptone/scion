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
 * Agent Chatter — a read-only feed of a space's agent-to-agent messages.
 *
 * This is the native-chat equivalent of the external chat integrations'
 * observe mode: every message one agent in the project sends another, in the
 * order it happened. The user is not a participant, so there is no composer,
 * no read state and no typing indicator — only history and a live tail.
 *
 * Data sources:
 *   - GET /api/v1/chat/spaces/{projectId}/interagent — newest-first history,
 *     paged backwards with the `before` timestamp it hands back.
 *   - `chat-interagent-received` from the StateManager — the live tail,
 *     published on project.{id}.chat.interagent, which the chat SSE scope
 *     already subscribes to.
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { apiFetch, extractApiError } from '../../../client/api.js';
import { stateManager } from '../../../client/main.js';
import { getMarkdownRenderer } from '../../../utils/markdown.js';

/**
 * One agent-to-agent message. Both the history endpoint (store.Message) and
 * the SSE event carry these fields under the same names.
 */
export interface ChatterMessage {
  id: string;
  sender: string;
  recipient: string;
  msg: string;
  createdAt: string;
}

/** How many messages one history page asks for. */
const PAGE_SIZE = 100;

/** Distance from the bottom, in px, still counted as "following the feed". */
const FOLLOW_THRESHOLD_PX = 80;

@customElement('scion-chat-chatter')
export class ScionChatChatter extends LitElement {
  /** The space whose agent traffic is shown. */
  @property()
  projectId = '';

  @state() private messages: ChatterMessage[] = [];
  @state() private loading = false;
  @state() private loadingOlder = false;
  @state() private error = '';
  /** Whether the server has older messages beyond what is loaded. */
  @state() private hasOlder = false;
  /** Rendered markdown bodies, keyed by message ID. */
  @state() private bodies = new Map<string, string>();

  /** The `before` value for the next older page. */
  private nextBefore = '';
  /** Guards against a slow response for a space the user has since left. */
  private fetchId = 0;
  /** IDs already shown — the live tail can echo something history returned. */
  private seen = new Set<string>();
  /** Whether the viewport was at the bottom before the last render. */
  private following = true;
  private readonly onInteragent = this.handleInteragent.bind(this);

  static override styles = css`
    :host {
      display: flex;
      flex-direction: column;
      flex: 1;
      min-height: 0;
      overflow: hidden;
    }

    .feed {
      flex: 1;
      min-height: 0;
      overflow-y: auto;
      padding: 0.5rem 0.75rem 1rem;
      font-size: 0.8125rem;
    }

    .notice {
      padding: 1.5rem 1rem;
      text-align: center;
      color: var(--scion-text-muted, #64748b);
      font-size: 0.8125rem;
    }

    .notice.error {
      color: var(--scion-danger-600, #dc2626);
    }

    .older {
      display: flex;
      justify-content: center;
      padding: 0.25rem 0 0.5rem;
    }

    .entry {
      padding: 0.375rem 0.5rem;
      border-left: 2px solid var(--scion-border, #e2e8f0);
      margin-bottom: 0.25rem;
    }

    .entry:hover {
      background: var(--scion-bg-subtle, #f8fafc);
    }

    .entry-header {
      display: flex;
      align-items: baseline;
      flex-wrap: wrap;
      gap: 0.375rem;
      font-size: 0.75rem;
      color: var(--scion-text-muted, #64748b);
      margin-bottom: 0.125rem;
    }

    .participant {
      font-weight: 600;
      color: var(--scion-text, #1e293b);
      font-family: var(--sl-font-mono, ui-monospace, monospace);
    }

    .arrow {
      color: var(--scion-text-muted, #94a3b8);
    }

    .timestamp {
      margin-left: auto;
      font-variant-numeric: tabular-nums;
      white-space: nowrap;
    }

    .body {
      color: var(--scion-text, #334155);
      line-height: 1.5;
      overflow-wrap: anywhere;
    }

    .body.plain {
      white-space: pre-wrap;
    }

    /* Markdown output is kept compact — this is a monitoring feed, not prose. */
    .body :first-child {
      margin-top: 0;
    }

    .body :last-child {
      margin-bottom: 0;
    }

    .body p {
      margin: 0 0 0.375rem;
    }

    .body pre {
      background: var(--scion-bg-subtle, #f1f5f9);
      padding: 0.5rem;
      border-radius: 0.25rem;
      overflow-x: auto;
      margin: 0.25rem 0;
    }

    .body code {
      font-family: var(--sl-font-mono, ui-monospace, monospace);
      font-size: 0.75rem;
    }
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    stateManager.addEventListener('chat-interagent-received', this.onInteragent);
  }

  override disconnectedCallback(): void {
    stateManager.removeEventListener('chat-interagent-received', this.onInteragent);
    super.disconnectedCallback();
  }

  override willUpdate(changed: Map<string, unknown>): void {
    if (changed.has('projectId')) {
      this.reset();
      if (this.projectId) void this.loadHistory();
    }
  }

  override updated(): void {
    if (this.following) this.scrollToBottom();
  }

  private reset(): void {
    this.fetchId++;
    this.messages = [];
    this.bodies = new Map();
    this.seen = new Set();
    this.nextBefore = '';
    this.hasOlder = false;
    this.error = '';
    this.following = true;
  }

  /** Load the most recent page of agent-to-agent messages. */
  private async loadHistory(): Promise<void> {
    const currentId = this.fetchId;
    this.loading = true;
    this.error = '';
    try {
      const page = await this.fetchPage();
      if (currentId !== this.fetchId) return;
      this.messages = page.messages;
      this.registerAll(page.messages);
      this.hasOlder = page.hasMore;
      this.nextBefore = page.nextBefore;
      void this.renderBodies(page.messages);
    } catch (err) {
      if (currentId !== this.fetchId) return;
      this.error = err instanceof Error ? err.message : 'Failed to load agent chatter';
    } finally {
      if (currentId === this.fetchId) this.loading = false;
    }
  }

  /** Prepend the next older page, keeping the reader's place in the feed. */
  private async loadOlder(): Promise<void> {
    if (this.loadingOlder || !this.nextBefore) return;
    const currentId = this.fetchId;
    this.loadingOlder = true;
    // Older messages go on top, so staying put means staying off the bottom.
    this.following = false;
    const feed = this.feedEl();
    const previousHeight = feed?.scrollHeight ?? 0;
    try {
      const page = await this.fetchPage(this.nextBefore);
      if (currentId !== this.fetchId) return;
      const fresh = page.messages.filter((m) => !this.seen.has(m.id));
      this.registerAll(fresh);
      this.messages = [...fresh, ...this.messages];
      this.hasOlder = page.hasMore;
      this.nextBefore = page.nextBefore;
      void this.renderBodies(fresh);
      await this.updateComplete;
      const grownBy = (this.feedEl()?.scrollHeight ?? 0) - previousHeight;
      if (feed && grownBy > 0) feed.scrollTop += grownBy;
    } catch (err) {
      if (currentId !== this.fetchId) return;
      this.error = err instanceof Error ? err.message : 'Failed to load older messages';
    } finally {
      if (currentId === this.fetchId) this.loadingOlder = false;
    }
  }

  /**
   * Fetch one page of history, oldest-last. The endpoint answers newest-first
   * because that is the store's order; the feed reads the other way.
   */
  private async fetchPage(
    before?: string
  ): Promise<{ messages: ChatterMessage[]; nextBefore: string; hasMore: boolean }> {
    const params = new URLSearchParams({ limit: String(PAGE_SIZE) });
    if (before) params.set('before', before);

    const res = await apiFetch(
      `/api/v1/chat/spaces/${encodeURIComponent(this.projectId)}/interagent?${params.toString()}`
    );
    if (!res.ok) {
      throw new Error(await extractApiError(res, 'Failed to load agent chatter'));
    }
    const data = (await res.json()) as {
      messages?: ChatterMessage[];
      nextBefore?: string;
      hasMore?: boolean;
    };
    return {
      messages: [...(data.messages ?? [])].reverse(),
      nextBefore: data.nextBefore ?? '',
      hasMore: Boolean(data.hasMore),
    };
  }

  private registerAll(messages: ChatterMessage[]): void {
    for (const m of messages) this.seen.add(m.id);
  }

  /** Append a live message published on project.{id}.chat.interagent. */
  private handleInteragent(e: Event): void {
    const detail = (e as CustomEvent).detail as { data?: Record<string, unknown> } | undefined;
    const data = detail?.data;
    if (!data) return;
    if (data.projectId && data.projectId !== this.projectId) return;

    const msg: ChatterMessage = {
      id: String(data.id ?? ''),
      sender: String(data.sender ?? ''),
      recipient: String(data.recipient ?? ''),
      msg: String(data.msg ?? ''),
      createdAt: String(data.createdAt ?? new Date().toISOString()),
    };
    if (!msg.sender || !msg.recipient) return;
    // A message with no ID cannot be deduplicated, but dropping it would lose
    // traffic — key it on its content instead.
    const key = msg.id || `${msg.createdAt}|${msg.sender}|${msg.recipient}`;
    if (this.seen.has(key)) return;
    this.seen.add(key);

    this.following = this.isNearBottom();
    this.messages = [...this.messages, msg];
    void this.renderBodies([msg]);
  }

  /** Render markdown for the given messages and fold it into the body map. */
  private async renderBodies(messages: ChatterMessage[]): Promise<void> {
    if (messages.length === 0) return;
    const currentId = this.fetchId;
    let renderer;
    try {
      renderer = await getMarkdownRenderer();
    } catch {
      return; // Bodies fall back to plain text.
    }
    if (currentId !== this.fetchId) return;

    const next = new Map(this.bodies);
    for (const m of messages) {
      if (!m.msg) continue;
      try {
        next.set(m.id || m.createdAt, renderer.render(m.msg));
      } catch {
        // Leave this one as plain text.
      }
    }
    this.bodies = next;
  }

  private feedEl(): HTMLElement | null {
    return this.renderRoot?.querySelector('.feed') ?? null;
  }

  private isNearBottom(): boolean {
    const feed = this.feedEl();
    if (!feed) return true;
    return feed.scrollHeight - feed.scrollTop - feed.clientHeight < FOLLOW_THRESHOLD_PX;
  }

  private scrollToBottom(): void {
    const feed = this.feedEl();
    if (feed) feed.scrollTop = feed.scrollHeight;
  }

  private handleScroll(): void {
    this.following = this.isNearBottom();
  }

  /** "agent:planner" reads as "planner" — the prefix is noise in this view. */
  private formatParticipant(value: string): string {
    if (value.startsWith('agent:')) return value.slice('agent:'.length);
    if (value.startsWith('user:')) return value.slice('user:'.length);
    return value;
  }

  private formatTimestamp(iso: string): string {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return '';
    const sameDay = new Date().toDateString() === d.toDateString();
    const time = d.toLocaleTimeString('en', { hour: '2-digit', minute: '2-digit' });
    if (sameDay) return time;
    return `${d.toLocaleDateString('en', { month: 'short', day: 'numeric' })} ${time}`;
  }

  override render() {
    return html`
      <div class="feed" @scroll=${() => this.handleScroll()}>
        ${this.hasOlder
          ? html`
              <div class="older">
                <sl-button
                  size="small"
                  variant="text"
                  ?loading=${this.loadingOlder}
                  @click=${() => void this.loadOlder()}
                >
                  Load older
                </sl-button>
              </div>
            `
          : nothing}
        ${this.error ? html`<div class="notice error">${this.error}</div>` : nothing}
        ${this.renderFeed()}
      </div>
    `;
  }

  private renderFeed() {
    if (this.messages.length > 0) {
      return this.messages.map((m) => this.renderEntry(m));
    }
    if (this.loading) return html`<div class="notice">Loading agent chatter…</div>`;
    if (this.error) return nothing;
    return html`<div class="notice">No agent-to-agent messages in this space yet.</div>`;
  }

  private renderEntry(m: ChatterMessage) {
    const body = this.bodies.get(m.id || m.createdAt);
    return html`
      <div class="entry">
        <div class="entry-header">
          <span class="participant">${this.formatParticipant(m.sender)}</span>
          <span class="arrow">&rarr;</span>
          <span class="participant">${this.formatParticipant(m.recipient)}</span>
          <span class="timestamp">${this.formatTimestamp(m.createdAt)}</span>
        </div>
        ${body
          ? html`<div class="body" .innerHTML=${body}></div>`
          : html`<div class="body plain">${m.msg}</div>`}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-chat-chatter': ScionChatChatter;
  }
}
