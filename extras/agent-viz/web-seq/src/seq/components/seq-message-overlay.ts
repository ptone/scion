// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * Reader for a single message, opened from the bubble on an arrow.
 *
 * Separate from the detail panel on purpose. The panel is a persistent
 * inspector docked beside a moving diagram, sized for timings and confidence;
 * a message body is prose that runs to a couple of thousand characters and
 * wants width, wrapping and the reader's full attention. Squeezing it into the
 * panel would either truncate it to uselessness or turn the panel into a wall
 * of text that hides the timing data it exists to show.
 *
 * Everything shown here is already in the digest. In particular the body is
 * whatever the builder captured under `Options.MaxBodyLen`, so an absent body
 * means "not exported", never "the agent sent nothing" — and the two are
 * distinguished in the UI, because guessing between them is exactly the kind of
 * quiet lie this tool is built to avoid.
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

import type { Confidence, Edge, Lifeline } from '../core/types.js';
import {
  buildLogsUrl,
  formatAbsolute,
  formatDuration,
  parseInstantMs,
} from './seq-detail-panel.js';

/** The message under the reader, resolved against the digest by the host. */
export interface SeqMessageView {
  edge: Edge;
  from: Lifeline;
  to: Lifeline;
}

/** One-line explanation of how much the arrival time can be trusted. */
const ARRIVAL_COPY: Record<Confidence, string> = {
  measured: 'Arrival was logged; this latency is real.',
  inferred: 'Arrival was not logged; taken as the recipient’s next activity — an upper bound.',
  open: 'Arrival was never observed; the latency is not knowable.',
};

@customElement('scion-seq-message-overlay')
export class ScionSeqMessageOverlay extends LitElement {
  /** Message to display; null renders nothing at all. */
  @property({ attribute: false }) message: SeqMessageView | null = null;

  /** Project the run belongs to, for the Cloud Logging deep link. */
  @property({ type: String }) projectId = '';

  /** RFC3339 run start; the origin for every ms offset in the digest. */
  @property({ type: String }) startedAt = '';

  /** Transient "Copied" acknowledgement on the copy button. */
  @state() private copied = false;

  static override styles = css`
    :host {
      /* Above the legend and detail dock, which are themselves absolutely
         positioned inside the canvas area. */
      position: absolute;
      inset: 0;
      z-index: 20;
      display: flex;
      align-items: center;
      justify-content: center;
      font-family: var(--scion-font-sans, sans-serif);
      font-size: var(--scion-font-size-sm, 0.875rem);
    }

    .scrim {
      position: absolute;
      inset: 0;
      background: rgba(2, 6, 23, 0.55);
    }

    .card {
      position: relative;
      display: flex;
      flex-direction: column;
      width: min(680px, calc(100% - 2rem));
      max-height: calc(100% - 2rem);
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-lg, 0.75rem);
      background: var(--scion-surface, #ffffff);
      color: var(--scion-text, #0f172a);
      box-shadow: 0 18px 48px rgba(2, 6, 23, 0.45);
      overflow: hidden;
    }

    header {
      display: flex;
      align-items: center;
      gap: var(--scion-space-2, 0.5rem);
      padding: var(--scion-space-2, 0.5rem) var(--scion-space-3, 0.75rem);
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
    }

    .route {
      flex: 1 1 auto;
      min-width: 0;
      display: flex;
      align-items: center;
      gap: 6px;
      font-weight: 600;
      overflow: hidden;
      white-space: nowrap;
      text-overflow: ellipsis;
    }

    .arrow {
      color: var(--scion-text-muted, #64748b);
      font-weight: 400;
    }

    .swatch {
      display: inline-block;
      width: 0.5rem;
      height: 0.5rem;
      margin-right: 5px;
      border-radius: var(--scion-radius-sm, 0.25rem);
    }

    .close-btn {
      flex: 0 0 auto;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 1.5rem;
      height: 1.5rem;
      padding: 0;
      border: 0;
      border-radius: var(--scion-radius-sm, 0.25rem);
      background: transparent;
      color: var(--scion-text-muted, #64748b);
      cursor: pointer;
    }

    .close-btn:hover {
      background: var(--scion-bg-subtle, #f1f5f9);
      color: var(--scion-text, #0f172a);
    }

    .meta {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--scion-space-1, 0.25rem) var(--scion-space-3, 0.75rem);
      padding: var(--scion-space-2, 0.5rem) var(--scion-space-3, 0.75rem);
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
      background: var(--scion-bg-subtle, #f8fafc);
      font-size: var(--scion-font-size-xs, 0.75rem);
      color: var(--scion-text-secondary, #334155);
    }

    .meta .mono {
      font-family: var(--scion-font-mono, monospace);
      font-variant-numeric: tabular-nums;
    }

    .k {
      color: var(--scion-text-muted, #64748b);
      margin-right: 4px;
    }

    .tag {
      padding: 1px 8px;
      border-radius: var(--scion-radius-full, 9999px);
      font-family: var(--scion-font-mono, monospace);
      font-weight: 600;
      color: #ffffff;
      background: var(--scion-text-muted, #64748b);
    }

    .tag.measured {
      background: var(--scion-success, #10b981);
    }

    .tag.inferred {
      background: var(--scion-warning, #f59e0b);
    }

    .tag.type {
      background: var(--scion-bg, #e2e8f0);
      color: var(--scion-text-secondary, #334155);
    }

    .tag.urgent {
      background: var(--scion-danger, #ef4444);
    }

    .tag.broadcast {
      background: var(--scion-info, #3b82f6);
    }

    .note {
      flex-basis: 100%;
      color: var(--scion-text-muted, #64748b);
    }

    .body {
      flex: 1 1 auto;
      min-height: 0;
      overflow: auto;
      margin: 0;
      padding: var(--scion-space-3, 0.75rem);
      font-family: var(--scion-font-mono, monospace);
      font-size: var(--scion-font-size-sm, 0.875rem);
      line-height: 1.5;
      /* pre-wrap, not pre: message bodies carry meaningful newlines and
         indentation, but no meaningful line length. */
      white-space: pre-wrap;
      overflow-wrap: anywhere;
    }

    .absent {
      padding: var(--scion-space-4, 1rem);
      color: var(--scion-text-muted, #64748b);
      line-height: 1.5;
    }

    .truncated {
      padding: var(--scion-space-2, 0.5rem) var(--scion-space-3, 0.75rem);
      border-top: 1px dashed var(--scion-border, #e2e8f0);
      color: var(--scion-warning, #b45309);
      font-size: var(--scion-font-size-xs, 0.75rem);
    }

    footer {
      display: flex;
      align-items: center;
      gap: var(--scion-space-2, 0.5rem);
      padding: var(--scion-space-2, 0.5rem) var(--scion-space-3, 0.75rem);
      border-top: 1px solid var(--scion-border, #e2e8f0);
    }

    footer .spacer {
      flex: 1 1 auto;
    }

    a.log-link {
      text-decoration: none;
    }
  `;

  private close(): void {
    this.dispatchEvent(new CustomEvent('seq-close-message', { bubbles: true, composed: true }));
  }

  private async copyBody(): Promise<void> {
    const body = this.message?.edge.body;
    if (!body) return;
    try {
      await navigator.clipboard.writeText(body);
      this.copied = true;
      // Long enough to read, short enough that the button is honest again
      // before the next click.
      setTimeout(() => {
        this.copied = false;
      }, 1500);
    } catch {
      // Clipboard access is permission-gated and absent over plain HTTP on some
      // browsers. Failing silently is right: the text is on screen and
      // selectable, so nothing is actually lost.
    }
  }

  private renderMeta(edge: Edge, baseMs: number | null) {
    const latency = Math.max(0, edge.recvMs - edge.sendMs);
    const open = edge.recvConfidence === 'open';
    return html`
      <div class="meta">
        ${edge.msgType ? html`<span class="tag type">${edge.msgType}</span>` : nothing}
        <span class="tag ${edge.recvConfidence}">${edge.recvConfidence} arrival</span>
        ${edge.broadcast ? html`<span class="tag broadcast">broadcast</span>` : nothing}
        ${edge.urgent ? html`<span class="tag urgent">urgent</span>` : nothing}
        <span><span class="k">Sent</span
          ><span class="mono">${formatAbsolute(baseMs, edge.sendMs)}</span></span
        >
        <span><span class="k">Arrived</span
          ><span class="mono">${open ? '—' : formatAbsolute(baseMs, edge.recvMs)}</span></span
        >
        <span><span class="k">Latency</span
          ><span class="mono">${open ? 'unknown' : formatDuration(latency)}</span></span
        >
        <span class="note">${ARRIVAL_COPY[edge.recvConfidence]}</span>
      </div>
    `;
  }

  private renderBody(edge: Edge) {
    if (edge.body) return html`<pre class="body">${edge.body}</pre>`;
    // An empty body is ambiguous at this layer, and the ambiguity is worth
    // stating: bodies can be capped off with --max-body, and older digests
    // predate the field entirely.
    return html`
      <div class="absent">
        No message text was captured in this digest. Bodies can be disabled when it is built
        (<code>--max-body -1</code>), and digests produced before bodies were recorded do not carry
        them. The Cloud Logging record below still has the original.
      </div>
    `;
  }

  override render(): unknown {
    const m = this.message;
    if (!m) return nothing;
    const { edge, from, to } = m;
    const baseMs = parseInstantMs(this.startedAt);

    return html`
      <div class="scrim" @click=${(): void => this.close()}></div>
      <div class="card" role="dialog" aria-modal="true" aria-label="Message contents">
        <header>
          <span class="route">
            <span><span class="swatch" style="background: ${from.color}"></span>${from.name}</span>
            <span class="arrow">→</span>
            <span><span class="swatch" style="background: ${to.color}"></span>${to.name}</span>
          </span>
          <button
            class="close-btn"
            type="button"
            aria-label="Close message"
            @click=${(): void => this.close()}
          >
            <sl-icon name="x-lg"></sl-icon>
          </button>
        </header>

        ${this.renderMeta(edge, baseMs)} ${this.renderBody(edge)}
        ${edge.bodyTruncated
          ? html`<div class="truncated">
              Truncated — only the beginning of this message was stored in the digest. Open the log
              record for the whole thing.
            </div>`
          : nothing}

        <footer>
          <sl-button
            size="small"
            ?disabled=${!edge.body}
            @click=${(): void => {
              void this.copyBody();
            }}
          >
            <sl-icon slot="prefix" name=${this.copied ? 'check-lg' : 'clipboard'}></sl-icon>
            ${this.copied ? 'Copied' : 'Copy text'}
          </sl-button>
          <span class="spacer"></span>
          ${edge.logId
            ? html`<a
                class="log-link"
                href=${buildLogsUrl(edge.logId, this.projectId, baseMs, edge.sendMs)}
                target="_blank"
                rel="noopener noreferrer"
                title=${`insertId=${edge.logId}`}
              >
                <sl-button size="small" variant="default">
                  <sl-icon slot="prefix" name="journal-text"></sl-icon>
                  View in Cloud Logging
                </sl-button>
              </a>`
            : nothing}
        </footer>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-seq-message-overlay': ScionSeqMessageOverlay;
  }
}
