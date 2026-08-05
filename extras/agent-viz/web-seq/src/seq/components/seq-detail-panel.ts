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
 * Sequence Detail Panel
 *
 * Inspector for the currently selected interval or edge.
 *
 * The panel's job is not merely to print numbers but to say how much each
 * number can be trusted. Scion's telemetry frequently observes only one end of
 * a span, and the digest records that as a {@link Confidence} rather than
 * fabricating a plausible duration. Surfacing it here — as a coloured badge
 * *and* a plain-English sentence about what it means for the figure above it —
 * is the difference between a viewer that informs and one that misleads.
 *
 * Where the digest captured a Cloud Logging `insertId`, a deep link to the
 * originating record is offered; where it did not, the button is disabled with
 * an explanation rather than hidden, so an absent source record is itself
 * visible information.
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import type { Confidence, Edge, Interval, Lifeline } from '../core/types.js';

/** What the panel is currently inspecting. */
export type SeqSelection =
  | { kind: 'interval'; interval: Interval; lifeline: Lifeline }
  | { kind: 'edge'; edge: Edge; from: Lifeline; to: Lifeline };

/** Base of the Cloud Logging query deep link. */
const LOGS_QUERY_BASE = 'https://console.cloud.google.com/logs/query';

/** Half-width of the time window centred on the record, in ms. */
const LOGS_WINDOW_MS = 5 * 60 * 1000;

/**
 * Builds a Cloud Logging deep link for a single record.
 *
 * Three parts matter, and omitting any of them makes the link useless in
 * practice:
 *  - `query` pins the exact record by `insertId`.
 *  - `timeRange` centres a window on the record. Without it the console
 *    defaults to the last hour, which for a run being diagnosed after the fact
 *    means the query returns nothing at all.
 *  - `project` selects the right project, otherwise the console opens against
 *    whichever one the user last visited.
 */
export function buildLogsUrl(
  logId: string,
  projectId: string,
  baseMs: number | null,
  offsetMs: number
): string {
  let url = `${LOGS_QUERY_BASE};query=${encodeURIComponent(`insertId="${logId}"`)}`;
  if (baseMs !== null && Number.isFinite(offsetMs)) {
    const at = baseMs + offsetMs;
    const from = new Date(at - LOGS_WINDOW_MS).toISOString();
    const to = new Date(at + LOGS_WINDOW_MS).toISOString();
    url += `;timeRange=${encodeURIComponent(`${from}/${to}`)}`;
  }
  if (projectId) {
    url += `?project=${encodeURIComponent(projectId)}`;
  }
  return url;
}

/**
 * Human-readable duration.
 *
 * Follows the house `formatDurationHMS` style (`1h 2m 3s`) but extends it
 * downwards: spans in this visualization are routinely sub-second, and
 * rendering a 420 ms tool call as `0s` would erase exactly the detail the
 * viewer opened the panel to see.
 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0ms';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 10000) {
    // Keep one decimal in the 1-10s band, where 1.2s vs 1.9s matters.
    const seconds = Math.round(ms / 100) / 10;
    return `${seconds}s`;
  }
  const totalSeconds = Math.floor(ms / 1000);
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = Math.floor(totalSeconds % 60);
  const parts: string[] = [];
  if (h > 0) parts.push(`${h}h`);
  if (m > 0) parts.push(`${m}m`);
  if (s > 0 || parts.length === 0) parts.push(`${s}s`);
  return parts.join(' ');
}

function pad(n: number, width = 2): string {
  return String(Math.floor(n)).padStart(width, '0');
}

/** Parses RFC3339 / RFC3339Nano, truncating beyond millisecond precision. */
export function parseInstantMs(value: string): number | null {
  if (!value) return null;
  const normalized = value.replace(/(\.\d{3})\d+/, '$1');
  const t = Date.parse(normalized);
  return Number.isNaN(t) ? null : t;
}

/** Absolute local timestamp for a run-relative offset. */
export function formatAbsolute(baseMs: number | null, offsetMs: number): string {
  if (baseMs === null) return '—';
  const d = new Date(baseMs + offsetMs);
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(
      d.getMilliseconds(),
      3
    )}`
  );
}

/** What each confidence level means for trusting the number beside it. */
const CONFIDENCE_COPY: Record<Confidence, { label: string; interval: string; edge: string }> = {
  measured: {
    label: 'measured',
    interval: 'Both endpoints were observed in the log — this duration is real.',
    edge: 'Both send and arrival were observed — this latency is real.',
  },
  inferred: {
    label: 'inferred',
    interval:
      'End not observed; bounded by the next event on this lifeline — treat as an upper bound.',
    edge:
      'Arrival not recorded; taken as the recipient’s next observed activity — an upper bound on latency.',
  },
  open: {
    label: 'open',
    interval:
      'No end was ever observed; the bar runs to the lifeline’s death or the end of the run — the true duration may be shorter.',
    edge: 'Arrival time is unknown; the edge is drawn horizontal and the latency is not knowable.',
  },
};

@customElement('scion-seq-detail-panel')
export class ScionSeqDetailPanel extends LitElement {
  /** The interval or edge under inspection; null hides the panel body. */
  @property({ attribute: false })
  selection: SeqSelection | null = null;

  /** Project the run belongs to, shown for context. */
  @property({ type: String })
  projectId = '';

  /** RFC3339 run start; the origin for every ms offset in the digest. */
  @property({ type: String })
  startedAt = '';

  static override styles = css`
    :host {
      display: block;
      font-family: var(--scion-font-sans, sans-serif);
      font-size: var(--scion-font-size-sm, 0.875rem);
      color: var(--scion-text, #0f172a);
      background: var(--scion-surface, #ffffff);
      border-left: 1px solid var(--scion-border, #e2e8f0);
      overflow-y: auto;
    }

    .header {
      display: flex;
      align-items: center;
      gap: var(--scion-space-2, 0.5rem);
      padding: var(--scion-space-2, 0.5rem) var(--scion-space-3, 0.75rem);
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
      position: sticky;
      top: 0;
      background: var(--scion-surface, #ffffff);
      z-index: 1;
    }

    .kind {
      flex: 0 0 auto;
      padding: 1px 6px;
      border-radius: var(--scion-radius-sm, 0.25rem);
      background: var(--scion-bg-subtle, #f1f5f9);
      color: var(--scion-text-secondary, #334155);
      font-size: var(--scion-font-size-xs, 0.75rem);
      text-transform: uppercase;
      letter-spacing: 0.04em;
    }

    .title {
      flex: 1 1 auto;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-weight: 600;
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

    .body {
      padding: var(--scion-space-3, 0.75rem);
      display: flex;
      flex-direction: column;
      gap: var(--scion-space-3, 0.75rem);
    }

    .grid {
      display: grid;
      grid-template-columns: auto 1fr;
      gap: var(--scion-space-1, 0.25rem) var(--scion-space-3, 0.75rem);
      align-items: baseline;
    }

    .k {
      color: var(--scion-text-muted, #64748b);
      font-size: var(--scion-font-size-xs, 0.75rem);
      white-space: nowrap;
    }

    .v {
      min-width: 0;
      overflow-wrap: anywhere;
    }

    .v.mono {
      font-family: var(--scion-font-mono, monospace);
      font-variant-numeric: tabular-nums;
    }

    .v.big {
      font-family: var(--scion-font-mono, monospace);
      font-size: var(--scion-font-size-lg, 1.125rem);
      font-weight: 600;
    }

    .swatch {
      display: inline-block;
      width: 0.5rem;
      height: 0.5rem;
      margin-right: 5px;
      border-radius: var(--scion-radius-sm, 0.25rem);
      vertical-align: baseline;
    }

    /* --- Confidence ------------------------------------------------------ */

    .confidence {
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius, 0.5rem);
      padding: var(--scion-space-2, 0.5rem);
      background: var(--scion-bg-subtle, #f8fafc);
    }

    .badge {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      padding: 1px 8px;
      border-radius: var(--scion-radius-full, 9999px);
      font-family: var(--scion-font-mono, monospace);
      font-size: var(--scion-font-size-xs, 0.75rem);
      font-weight: 600;
      letter-spacing: 0.02em;
      color: #ffffff;
      background: var(--scion-text-muted, #64748b);
    }

    .badge.measured {
      background: var(--scion-success, #10b981);
    }

    .badge.inferred {
      background: var(--scion-warning, #f59e0b);
    }

    .badge.open {
      background: var(--scion-text-muted, #64748b);
    }

    .confidence-note {
      margin-top: var(--scion-space-1, 0.25rem);
      color: var(--scion-text-secondary, #334155);
      font-size: var(--scion-font-size-xs, 0.75rem);
      line-height: 1.4;
    }

    .error-flag {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      color: var(--scion-danger, #ef4444);
      font-size: var(--scion-font-size-xs, 0.75rem);
      font-weight: 600;
    }

    .footer {
      border-top: 1px solid var(--scion-border, #e2e8f0);
      padding-top: var(--scion-space-2, 0.5rem);
      display: flex;
      flex-direction: column;
      gap: var(--scion-space-1, 0.25rem);
    }

    .project {
      color: var(--scion-text-muted, #64748b);
      font-size: var(--scion-font-size-xs, 0.75rem);
      font-family: var(--scion-font-mono, monospace);
    }

    .empty {
      padding: var(--scion-space-4, 1rem);
      color: var(--scion-text-muted, #64748b);
      font-size: var(--scion-font-size-xs, 0.75rem);
      line-height: 1.5;
    }

    a.log-link {
      text-decoration: none;
    }
  `;

  private onClose(): void {
    this.dispatchEvent(new CustomEvent('seq-close-detail', { bubbles: true, composed: true }));
  }

  private renderConfidence(confidence: Confidence, context: 'interval' | 'edge') {
    const copy = CONFIDENCE_COPY[confidence];
    return html`
      <div class="confidence">
        <span class="badge ${confidence}">${copy.label}</span>
        <div class="confidence-note">
          ${context === 'edge' ? copy.edge : copy.interval}
        </div>
      </div>
    `;
  }

  private renderLifeline(lifeline: Lifeline) {
    return html`
      <span class="v">
        <span class="swatch" style="background: ${lifeline.color}"></span>${lifeline.name}
      </span>
    `;
  }

  /**
   * Deep link to the originating Cloud Logging record. When the digest captured
   * no `insertId` the control stays visible but disabled — an absent source
   * record is worth stating explicitly rather than silently omitting.
   */
  private renderLogLink(logId: string | undefined, baseMs: number | null, offsetMs: number) {
    if (!logId) {
      return html`
        <sl-tooltip
          content="No source log record was captured for this item, so it cannot be deep-linked."
        >
          <span>
            <sl-button size="small" disabled>
              <sl-icon slot="prefix" name="journal-text"></sl-icon>
              View in Cloud Logging
            </sl-button>
          </span>
        </sl-tooltip>
      `;
    }
    const href = buildLogsUrl(logId, this.projectId, baseMs, offsetMs);
    return html`
      <a
        class="log-link"
        href=${href}
        target="_blank"
        rel="noopener noreferrer"
        title=${`insertId=${logId}`}
      >
        <sl-button size="small" variant="default">
          <sl-icon slot="prefix" name="journal-text"></sl-icon>
          View in Cloud Logging
        </sl-button>
      </a>
    `;
  }

  private renderInterval(interval: Interval, lifeline: Lifeline, baseMs: number | null) {
    const duration = Math.max(0, interval.endMs - interval.startMs);
    return html`
      <div class="body">
        <div class="grid">
          <span class="k">Lifeline</span>${this.renderLifeline(lifeline)}
          <span class="k">Nesting</span
          ><span class="v">${interval.kind} · depth ${interval.depth}</span>
          <span class="k">Start</span
          ><span class="v mono">${formatAbsolute(baseMs, interval.startMs)}</span>
          <span class="k">End</span
          ><span class="v mono"
            >${interval.confidence === 'open'
              ? html`${formatAbsolute(baseMs, interval.endMs)} <em>(unterminated)</em>`
              : formatAbsolute(baseMs, interval.endMs)}</span
          >
          <span class="k">Duration</span><span class="v big">${formatDuration(duration)}</span>
        </div>

        ${interval.error
          ? html`<span class="error-flag"
              ><sl-icon name="exclamation-triangle-fill"></sl-icon>The source event reported a
              failure.</span
            >`
          : nothing}
        ${this.renderConfidence(interval.confidence, 'interval')}

        <div class="footer">
          ${this.renderLogLink(interval.logId, baseMs, interval.startMs)}
          ${this.projectId ? html`<span class="project">project ${this.projectId}</span>` : nothing}
        </div>
      </div>
    `;
  }

  private renderEdge(edge: Edge, from: Lifeline, to: Lifeline, baseMs: number | null) {
    const latency = Math.max(0, edge.recvMs - edge.sendMs);
    return html`
      <div class="body">
        <div class="grid">
          <span class="k">From</span>${this.renderLifeline(from)}
          <span class="k">To</span>${this.renderLifeline(to)}
          <span class="k">Kind</span
          ><span class="v"
            >${edge.kind}${edge.msgType ? html` · ${edge.msgType}` : nothing}${edge.broadcast
              ? html` · broadcast`
              : nothing}</span
          >
          <span class="k">Sent</span
          ><span class="v mono">${formatAbsolute(baseMs, edge.sendMs)}</span>
          <span class="k">Arrived</span
          ><span class="v mono"
            >${edge.recvConfidence === 'open'
              ? html`${formatAbsolute(baseMs, edge.recvMs)} <em>(not observed)</em>`
              : formatAbsolute(baseMs, edge.recvMs)}</span
          >
          <span class="k">Latency</span
          ><span class="v big"
            >${edge.recvConfidence === 'open' ? 'unknown' : formatDuration(latency)}</span
          >
        </div>

        ${this.renderConfidence(edge.recvConfidence, 'edge')}

        <div class="footer">
          ${this.renderLogLink(edge.logId, baseMs, edge.sendMs)}
          ${this.projectId ? html`<span class="project">project ${this.projectId}</span>` : nothing}
        </div>
      </div>
    `;
  }

  override render() {
    const selection = this.selection;
    if (!selection) {
      return html`
        <div class="empty">
          Nothing selected. Click an activation bar or a message edge in the diagram to inspect its
          timing and how confidently it was measured.
        </div>
      `;
    }

    const baseMs = parseInstantMs(this.startedAt);
    const title =
      selection.kind === 'interval'
        ? (selection.interval.label ?? selection.interval.kind)
        : (selection.edge.label ?? selection.edge.msgType ?? selection.edge.kind);

    return html`
      <div class="header">
        <span class="kind">${selection.kind}</span>
        <span class="title" title=${title}>${title}</span>
        <button
          class="close-btn"
          type="button"
          aria-label="Close details"
          @click=${(): void => this.onClose()}
        >
          <sl-icon name="x-lg"></sl-icon>
        </button>
      </div>
      ${selection.kind === 'interval'
        ? this.renderInterval(selection.interval, selection.lifeline, baseMs)
        : this.renderEdge(selection.edge, selection.from, selection.to, baseMs)}
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-seq-detail-panel': ScionSeqDetailPanel;
  }
}
