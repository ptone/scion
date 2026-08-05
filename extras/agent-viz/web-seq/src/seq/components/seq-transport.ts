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
 * Sequence Transport Bar
 *
 * The playback controls for the sequence visualizer. Purely presentational:
 * every property comes in from the owner and every interaction leaves as a
 * `CustomEvent`; the component holds no playback state of its own beyond the
 * in-flight scrub position.
 *
 * Two clocks are on display at once, and keeping them legible is the point of
 * the design:
 *  - **viewer time** (tau) drives the scrubber, because that is the axis the
 *    user is actually dragging along;
 *  - **wall time** drives the absolute timestamp readout, because that is what
 *    really happened.
 *
 * The warp between them means the wall clock does not advance evenly. Rather
 * than hide that, the readout is given a blur/glow whose intensity tracks
 * `velocity`, and an explicit "N× express" badge appears whenever the run is
 * being traversed faster than real time. A spinning, slightly smeared clock is
 * an honest depiction of what the express lane is doing.
 */

import { LitElement, html, css } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

/** Rate multipliers offered in the rate selector. */
const RATES = [0.25, 0.5, 1, 2, 4, 8] as const;

/** Velocity below which playback is presented as ordinary real time. */
const EXPRESS_THRESHOLD = 1.5;

function pad(n: number, width = 2): string {
  return String(Math.floor(n)).padStart(width, '0');
}

/**
 * Parses an RFC3339 / RFC3339Nano instant into epoch milliseconds.
 *
 * `Date.parse` is only specified for three fractional digits, so sub-millisecond
 * precision is truncated first rather than relying on engine leniency.
 */
function parseInstantMs(value: string): number | null {
  if (!value) return null;
  const normalized = value.replace(/(\.\d{3})\d+/, '$1');
  const t = Date.parse(normalized);
  return Number.isNaN(t) ? null : t;
}

/** `h:mm:ss` (or `m:ss` under an hour) for an elapsed-duration readout. */
function formatClock(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
}

/** Absolute local wall-clock time, to the millisecond. */
function formatWallClock(baseMs: number | null, offsetMs: number): string {
  if (baseMs === null) return '--:--:--.---';
  const d = new Date(baseMs + offsetMs);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(
    d.getMilliseconds(),
    3
  )}`;
}

@customElement('scion-seq-transport')
export class ScionSeqTransport extends LitElement {
  /** Whether playback is currently running. */
  @property({ type: Boolean })
  playing = false;

  /** Current viewer time (tau), in viewer-milliseconds. */
  @property({ type: Number })
  tauMs = 0;

  /** Full viewer-time length of playback at 1x. */
  @property({ type: Number })
  totalTauMs = 0;

  /** Current wall time within the run, in milliseconds from `startedAt`. */
  @property({ type: Number })
  wallMs = 0;

  /** Full wall duration of the run. */
  @property({ type: Number })
  durationMs = 0;

  /** RFC3339 instant the run started; the origin for all ms offsets. */
  @property({ type: String })
  startedAt = '';

  /** User-selected playback multiplier applied on top of the warp. */
  @property({ type: Number })
  rate = 1;

  /** Current warp velocity in wall-ms per viewer-ms. 1 is real time. */
  @property({ type: Number })
  velocity = 1;

  /** Highest velocity present in the warp; used to normalise the indicator. */
  @property({ type: Number })
  maxVelocity = 120;

  /**
   * Scrub position while the user is dragging. Held locally so the thumb keeps
   * up with the pointer even if the owner throttles its seek handling.
   */
  @state()
  private scrubTau: number | null = null;

  static override styles = css`
    :host {
      display: block;
      font-family: var(--scion-font-sans, sans-serif);
      color: var(--scion-text, #0f172a);
      background: var(--scion-surface, #ffffff);
      border-top: 1px solid var(--scion-border, #e2e8f0);
    }

    .bar {
      display: flex;
      align-items: center;
      gap: var(--scion-space-3, 0.75rem);
      padding: var(--scion-space-2, 0.5rem) var(--scion-space-3, 0.75rem);
      min-width: 0;
    }

    .play-btn {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 2rem;
      height: 2rem;
      flex: 0 0 auto;
      padding: 0;
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius-full, 9999px);
      background: var(--scion-surface-raised, #ffffff);
      color: var(--scion-text, #0f172a);
      cursor: pointer;
      font-size: var(--scion-font-size-sm, 0.875rem);
      transition: border-color var(--scion-transition-fast, 150ms ease);
    }

    .play-btn:hover {
      border-color: var(--scion-border-hover, #cbd5e1);
    }

    /* --- Scrubber -------------------------------------------------------- */

    .scrub-wrap {
      flex: 1 1 auto;
      display: flex;
      flex-direction: column;
      gap: 2px;
      min-width: 0;
    }

    input[type='range'].scrub {
      -webkit-appearance: none;
      appearance: none;
      width: 100%;
      height: 1rem;
      margin: 0;
      background: transparent;
      cursor: pointer;
    }

    input[type='range'].scrub::-webkit-slider-runnable-track {
      height: 6px;
      border-radius: var(--scion-radius-full, 9999px);
      background: var(--scion-bg-subtle, #f1f5f9);
      border: 1px solid var(--scion-border, #e2e8f0);
    }

    input[type='range'].scrub::-moz-range-track {
      height: 6px;
      border-radius: var(--scion-radius-full, 9999px);
      background: var(--scion-bg-subtle, #f1f5f9);
      border: 1px solid var(--scion-border, #e2e8f0);
    }

    input[type='range'].scrub::-webkit-slider-thumb {
      -webkit-appearance: none;
      appearance: none;
      width: 12px;
      height: 12px;
      margin-top: -4px;
      border-radius: var(--scion-radius-full, 9999px);
      background: var(--scion-primary, #3b82f6);
      border: 2px solid var(--scion-surface, #ffffff);
    }

    input[type='range'].scrub::-moz-range-thumb {
      width: 12px;
      height: 12px;
      border: 2px solid var(--scion-surface, #ffffff);
      border-radius: var(--scion-radius-full, 9999px);
      background: var(--scion-primary, #3b82f6);
    }

    input[type='range'].scrub:focus-visible {
      outline: 2px solid var(--scion-primary, #3b82f6);
      outline-offset: 2px;
      border-radius: var(--scion-radius-sm, 0.25rem);
    }

    /* --- Readouts -------------------------------------------------------- */

    .domains {
      display: flex;
      gap: var(--scion-space-3, 0.75rem);
      font-family: var(--scion-font-mono, monospace);
      font-size: var(--scion-font-size-xs, 0.75rem);
      color: var(--scion-text-muted, #64748b);
      white-space: nowrap;
    }

    .domains .domain-key {
      color: var(--scion-text-disabled, #94a3b8);
      margin-right: 2px;
    }

    .clock {
      flex: 0 0 auto;
      display: flex;
      flex-direction: column;
      align-items: flex-end;
      gap: 1px;
      min-width: 9.5rem;
    }

    .clock-time {
      font-family: var(--scion-font-mono, monospace);
      font-size: var(--scion-font-size-base, 1rem);
      font-variant-numeric: tabular-nums;
      line-height: 1.1;
      color: var(--scion-text, #0f172a);
      /* filter/text-shadow are set inline and scale with the velocity. */
      transition:
        filter var(--scion-transition-fast, 150ms ease),
        color var(--scion-transition-fast, 150ms ease);
    }

    .velocity {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      font-family: var(--scion-font-mono, monospace);
      font-size: var(--scion-font-size-xs, 0.75rem);
      line-height: 1;
      padding: 2px 6px;
      border-radius: var(--scion-radius-full, 9999px);
      border: 1px solid var(--scion-border, #e2e8f0);
      color: var(--scion-text-muted, #64748b);
      background: var(--scion-bg-subtle, #f8fafc);
      white-space: nowrap;
    }

    .velocity.express {
      color: var(--scion-primary, #3b82f6);
      border-color: var(--scion-primary, #3b82f6);
      background: color-mix(in srgb, var(--scion-primary, #3b82f6) 10%, transparent);
    }

    .streaks {
      display: inline-block;
      width: 14px;
      height: 8px;
      border-radius: 1px;
      background: repeating-linear-gradient(
        90deg,
        currentColor 0 2px,
        transparent 2px 4px
      );
      opacity: var(--seq-streak-opacity, 0);
    }

    sl-select {
      flex: 0 0 auto;
      width: 6.25rem;
    }
  `;

  /** Scrub value currently shown: the drag position, else the owner's tau. */
  private get displayTau(): number {
    return this.scrubTau ?? this.tauMs;
  }

  /**
   * Velocity mapped onto 0..1 on a log scale, so the first few multiples of
   * real time are as visually distinct as the last few dozen.
   */
  private get velocityFraction(): number {
    const v = Math.max(1, this.velocity || 1);
    const max = Math.max(2, this.maxVelocity || 2);
    if (v <= 1) return 0;
    return Math.min(1, Math.log(v) / Math.log(max));
  }

  private emit<T>(type: string, detail?: T): void {
    this.dispatchEvent(
      detail === undefined
        ? new CustomEvent(type, { bubbles: true, composed: true })
        : new CustomEvent<T>(type, { detail, bubbles: true, composed: true })
    );
  }

  private onPlayToggle(): void {
    this.emit('seq-play-toggle');
  }

  private onScrubInput(e: Event): void {
    const target = e.target as HTMLInputElement;
    const tauMs = Number(target.value);
    if (!Number.isFinite(tauMs)) return;
    this.scrubTau = tauMs;
    this.emit<{ tauMs: number }>('seq-seek', { tauMs });
  }

  private onScrubCommit(): void {
    this.scrubTau = null;
  }

  private onRateChange(e: Event): void {
    const target = e.target as unknown as { value?: unknown };
    const raw = Array.isArray(target.value) ? target.value[0] : target.value;
    const rate = Number(raw);
    if (!Number.isFinite(rate) || rate <= 0) return;
    this.emit<{ rate: number }>('seq-rate-change', { rate });
  }

  private renderVelocity() {
    const fraction = this.velocityFraction;
    const express = this.velocity >= EXPRESS_THRESHOLD;
    const shown = this.velocity >= 10 ? Math.round(this.velocity) : Math.round(this.velocity * 10) / 10;
    const label = express ? `${shown}× express` : 'real time';
    const tip = express
      ? `Traversing idle time at ${shown}× real speed — the wall clock is spinning to catch up.`
      : 'Playing at true wall-clock speed.';
    return html`
      <sl-tooltip content=${tip}>
        <span
          class="velocity ${express ? 'express' : ''}"
          style="--seq-streak-opacity: ${fraction.toFixed(2)}"
        >
          <span class="streaks"></span>${label}
        </span>
      </sl-tooltip>
    `;
  }

  override render() {
    const baseMs = parseInstantMs(this.startedAt);
    const fraction = this.velocityFraction;
    const clockStyle =
      `filter: blur(${(fraction * 1.1).toFixed(2)}px);` +
      ` text-shadow: 0 0 ${(fraction * 10).toFixed(1)}px` +
      ` rgba(59, 130, 246, ${(fraction * 0.85).toFixed(2)});`;
    const max = Math.max(0, this.totalTauMs);

    return html`
      <div class="bar" part="bar">
        <button
          class="play-btn"
          type="button"
          aria-label=${this.playing ? 'Pause' : 'Play'}
          title=${this.playing ? 'Pause' : 'Play'}
          @click=${(): void => this.onPlayToggle()}
        >
          <sl-icon name=${this.playing ? 'pause-fill' : 'play-fill'}></sl-icon>
        </button>

        <div class="scrub-wrap">
          <input
            class="scrub"
            type="range"
            min="0"
            max=${max}
            step="1"
            .value=${String(Math.round(this.displayTau))}
            aria-label="Seek viewer time"
            aria-valuetext=${`${formatClock(this.displayTau)} of ${formatClock(max)} viewer time`}
            @input=${(e: Event): void => this.onScrubInput(e)}
            @change=${(): void => this.onScrubCommit()}
            @pointerup=${(): void => this.onScrubCommit()}
          />
          <div class="domains">
            <span title="Wall time elapsed in the run">
              <span class="domain-key">wall</span>${formatClock(this.wallMs)} /
              ${formatClock(this.durationMs)}
            </span>
            <span title="Viewer time elapsed while watching">
              <span class="domain-key">τ</span>${formatClock(this.displayTau)} /
              ${formatClock(this.totalTauMs)}
            </span>
          </div>
        </div>

        ${this.renderVelocity()}

        <div class="clock">
          <span
            class="clock-time"
            style=${clockStyle}
            title=${baseMs === null ? 'Run start time unknown' : 'Absolute time in the run'}
            >${formatWallClock(baseMs, this.wallMs)}</span
          >
        </div>

        <sl-select
          size="small"
          hoist
          value=${String(this.rate)}
          aria-label="Playback rate"
          @sl-change=${(e: Event): void => this.onRateChange(e)}
        >
          ${RATES.map(
            (r) => html`<sl-option value=${String(r)}>${r}×</sl-option>`
          )}
        </sl-select>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-seq-transport': ScionSeqTransport;
  }
}
