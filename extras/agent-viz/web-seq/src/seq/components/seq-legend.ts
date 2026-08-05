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
 * Sequence Legend
 *
 * A collapsible key to the diagram's encodings.
 *
 * This visualization overloads a sequence diagram with a metric time axis, and
 * several of the resulting encodings are not guessable: that a bar's *length*
 * is its true duration, that hatching means the duration was inferred rather
 * than observed, that an edge's *slope* is its delivery latency and a
 * horizontal edge means arrival was never recorded. Each entry therefore ships
 * with a small inline SVG sample drawn the same way the diagram draws it.
 *
 * When `stats` is supplied the legend also reports how many intervals fall in
 * each confidence class, so the viewer learns up front what proportion of what
 * they are looking at was actually measured.
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import type { Stats } from '../core/types.js';

@customElement('scion-seq-legend')
export class ScionSeqLegend extends LitElement {
  /** Whether the legend body is expanded. Reflected for host-level styling. */
  @property({ type: Boolean, reflect: true })
  open = false;

  /** Digest composition; when present, confidence counts are shown. */
  @property({ attribute: false })
  stats: Stats | null = null;

  static override styles = css`
    :host {
      display: block;
      font-family: var(--scion-font-sans, sans-serif);
      font-size: var(--scion-font-size-xs, 0.75rem);
      color: var(--scion-text, #0f172a);
      background: var(--scion-surface, #ffffff);
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius, 0.5rem);
      box-shadow: var(--scion-shadow-sm, 0 1px 2px 0 rgb(0 0 0 / 0.05));
      max-width: 22rem;
      overflow: hidden;
    }

    .toggle {
      display: flex;
      align-items: center;
      gap: var(--scion-space-1, 0.25rem);
      width: 100%;
      box-sizing: border-box;
      padding: var(--scion-space-1, 0.25rem) var(--scion-space-2, 0.5rem);
      border: 0;
      background: transparent;
      color: var(--scion-text-secondary, #334155);
      font: inherit;
      font-weight: 600;
      text-align: left;
      cursor: pointer;
    }

    .toggle:hover {
      background: var(--scion-bg-subtle, #f1f5f9);
    }

    .spacer {
      flex: 1 1 auto;
    }

    .body {
      padding: var(--scion-space-2, 0.5rem);
      border-top: 1px solid var(--scion-border, #e2e8f0);
      display: flex;
      flex-direction: column;
      gap: var(--scion-space-3, 0.75rem);
    }

    .section-title {
      color: var(--scion-text-muted, #64748b);
      text-transform: uppercase;
      letter-spacing: 0.04em;
      margin-bottom: var(--scion-space-1, 0.25rem);
    }

    .entry {
      display: grid;
      grid-template-columns: 3rem 1fr;
      gap: var(--scion-space-2, 0.5rem);
      align-items: center;
      margin-bottom: var(--scion-space-1, 0.25rem);
      line-height: 1.35;
    }

    .entry .sample {
      display: flex;
      align-items: center;
      justify-content: center;
    }

    .entry .text {
      color: var(--scion-text-secondary, #334155);
    }

    .entry .term {
      color: var(--scion-text, #0f172a);
      font-weight: 600;
    }

    .counts {
      display: flex;
      flex-wrap: wrap;
      gap: var(--scion-space-1, 0.25rem);
    }

    .count {
      display: inline-flex;
      align-items: baseline;
      gap: 4px;
      padding: 1px 6px;
      border-radius: var(--scion-radius-full, 9999px);
      font-family: var(--scion-font-mono, monospace);
      color: #ffffff;
    }

    .count.measured {
      background: var(--scion-success, #10b981);
    }

    .count.inferred {
      background: var(--scion-warning, #f59e0b);
    }

    .count.open {
      background: var(--scion-text-muted, #64748b);
    }

    .count.edges {
      background: transparent;
      color: var(--scion-text-muted, #64748b);
      border: 1px solid var(--scion-border, #e2e8f0);
    }

    .ratio {
      color: var(--scion-text-muted, #64748b);
      margin-top: var(--scion-space-1, 0.25rem);
    }

    svg {
      display: block;
      overflow: visible;
    }
  `;

  private onToggle(): void {
    this.open = !this.open;
    this.dispatchEvent(
      new CustomEvent<{ open: boolean }>('seq-legend-toggle', {
        detail: { open: this.open },
        bubbles: true,
        composed: true,
      })
    );
  }

  /** A short activation bar, in one of the three confidence treatments. */
  private barSample(variant: 'solid' | 'hatched' | 'faded') {
    const fill =
      variant === 'solid'
        ? 'var(--scion-primary, #3b82f6)'
        : variant === 'hatched'
          ? 'url(#seq-legend-hatch)'
          : 'var(--scion-primary, #3b82f6)';
    const opacity = variant === 'faded' ? '0.28' : '1';
    return html`
      <svg width="40" height="18" viewBox="0 0 40 18" aria-hidden="true">
        <defs>
          <pattern
            id="seq-legend-hatch"
            width="4"
            height="4"
            patternUnits="userSpaceOnUse"
            patternTransform="rotate(45)"
          >
            <rect width="4" height="4" fill="var(--scion-warning, #f59e0b)" opacity="0.18"></rect>
            <line
              x1="0"
              y1="0"
              x2="0"
              y2="4"
              stroke="var(--scion-warning, #f59e0b)"
              stroke-width="1.5"
            ></line>
          </pattern>
        </defs>
        <rect
          x="8"
          y="3"
          width="24"
          height="12"
          rx="2"
          fill=${fill}
          opacity=${opacity}
          stroke=${variant === 'hatched'
            ? 'var(--scion-warning, #f59e0b)'
            : 'var(--scion-primary, #3b82f6)'}
          stroke-width="1"
          stroke-dasharray=${variant === 'faded' ? '3 2' : nothing}
        ></rect>
      </svg>
    `;
  }

  override render() {
    const stats = this.stats;
    return html`
      <button
        class="toggle"
        type="button"
        aria-expanded=${this.open ? 'true' : 'false'}
        @click=${(): void => this.onToggle()}
      >
        <sl-icon name="info-circle"></sl-icon>
        <span>Legend</span>
        <span class="spacer"></span>
        <sl-icon name=${this.open ? 'chevron-down' : 'chevron-up'}></sl-icon>
      </button>

      ${this.open ? this.renderBody(stats) : nothing}
    `;
  }

  private renderBody(stats: Stats | null) {
    return html`
      <div class="body">
        <div>
          <div class="section-title">Geometry</div>

          <div class="entry">
            <span class="sample">
              <svg width="40" height="26" viewBox="0 0 40 26" aria-hidden="true">
                <rect
                  x="14"
                  y="2"
                  width="12"
                  height="22"
                  rx="2"
                  fill="var(--scion-primary, #3b82f6)"
                  opacity="0.85"
                ></rect>
                <line
                  x1="6"
                  y1="2"
                  x2="6"
                  y2="24"
                  stroke="var(--scion-text-muted, #64748b)"
                  stroke-width="1"
                ></line>
                <line
                  x1="3"
                  y1="2"
                  x2="9"
                  y2="2"
                  stroke="var(--scion-text-muted, #64748b)"
                  stroke-width="1"
                ></line>
                <line
                  x1="3"
                  y1="24"
                  x2="9"
                  y2="24"
                  stroke="var(--scion-text-muted, #64748b)"
                  stroke-width="1"
                ></line>
              </svg>
            </span>
            <span class="text"
              ><span class="term">Bar length = true duration.</span> The vertical axis is metric, so
              a bar twice as tall really did take twice as long. Playback speeds up over idle
              stretches; it never shortens them.</span
            >
          </div>

          <div class="entry">
            <span class="sample">
              <svg width="40" height="26" viewBox="0 0 40 26" aria-hidden="true">
                <rect
                  x="6"
                  y="2"
                  width="28"
                  height="22"
                  rx="2"
                  fill="var(--scion-primary, #3b82f6)"
                  opacity="0.25"
                ></rect>
                <rect
                  x="11"
                  y="6"
                  width="18"
                  height="14"
                  rx="2"
                  fill="var(--scion-primary, #3b82f6)"
                  opacity="0.5"
                ></rect>
                <rect
                  x="16"
                  y="10"
                  width="8"
                  height="7"
                  rx="1.5"
                  fill="var(--scion-primary, #3b82f6)"
                  opacity="0.9"
                ></rect>
              </svg>
            </span>
            <span class="text"
              ><span class="term">Nesting = session &gt; turn &gt; tool.</span> Inset bars are
              children of the bar enclosing them, producing a flame graph inside each actor's own
              column.</span
            >
          </div>
        </div>

        <div>
          <div class="section-title">Confidence</div>

          <div class="entry">
            <span class="sample">${this.barSample('solid')}</span>
            <span class="text"
              ><span class="term">Solid = measured.</span> Both endpoints were observed; the
              duration is real.</span
            >
          </div>

          <div class="entry">
            <span class="sample">${this.barSample('hatched')}</span>
            <span class="text"
              ><span class="term">Hatched = inferred.</span> One endpoint came from the neighbouring
              event — read the length as an upper bound.</span
            >
          </div>

          <div class="entry">
            <span class="sample">${this.barSample('faded')}</span>
            <span class="text"
              ><span class="term">Faded, unterminated = open.</span> No end was ever seen; the bar
              runs to the lifeline's death or the end of the run.</span
            >
          </div>
        </div>

        <div>
          <div class="section-title">Messages</div>

          <div class="entry">
            <span class="sample">
              <svg width="40" height="26" viewBox="0 0 40 26" aria-hidden="true">
                <line
                  x1="4"
                  y1="4"
                  x2="34"
                  y2="20"
                  stroke="var(--scion-text-secondary, #334155)"
                  stroke-width="1.5"
                ></line>
                <polygon points="34,20 28,18 29,14" fill="var(--scion-text-secondary, #334155)"></polygon>
              </svg>
            </span>
            <span class="text"
              ><span class="term">Sloped edge: the slope is the latency.</span> A message leaves at
              its send time and lands at its arrival time, so a steeper line means a slower
              delivery or a busy recipient.</span
            >
          </div>

          <div class="entry">
            <span class="sample">
              <svg width="40" height="26" viewBox="0 0 40 26" aria-hidden="true">
                <line
                  x1="4"
                  y1="13"
                  x2="34"
                  y2="13"
                  stroke="var(--scion-text-muted, #64748b)"
                  stroke-width="1.5"
                  stroke-dasharray="4 3"
                ></line>
                <polygon points="34,13 28,10 28,16" fill="var(--scion-text-muted, #64748b)"></polygon>
              </svg>
            </span>
            <span class="text"
              ><span class="term">Dashed and horizontal: arrival unknown.</span> No receive time was
              recorded, so the edge is drawn flat rather than inventing a latency.</span
            >
          </div>
        </div>

        ${stats ? this.renderStats(stats) : nothing}
      </div>
    `;
  }

  private renderStats(stats: Stats) {
    const total = Math.max(1, stats.intervalCount);
    const measuredPct = Math.round((stats.measuredIntervals / total) * 100);
    return html`
      <div>
        <div class="section-title">This run</div>
        <div class="counts">
          <span class="count measured" title="Intervals with both endpoints observed"
            >${stats.measuredIntervals} measured</span
          >
          <span class="count inferred" title="Intervals with one endpoint derived"
            >${stats.inferredIntervals} inferred</span
          >
          <span class="count open" title="Intervals with no observed end"
            >${stats.openIntervals} open</span
          >
          <span class="count edges" title="Edges whose arrival time was inferred"
            >${stats.inferredEdges} inferred edges</span
          >
        </div>
        <div class="ratio">
          ${measuredPct}% of ${stats.intervalCount} intervals are fully measured across
          ${stats.lifelineCount} lifelines.
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-seq-legend': ScionSeqLegend;
  }
}
