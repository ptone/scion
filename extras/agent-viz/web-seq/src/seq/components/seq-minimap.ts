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
 * Sequence Minimap
 *
 * A vertical strip showing the *whole* run at a strictly uniform scale — the
 * honest counterweight to the elastic main view.
 *
 * Because the main canvas is traversed through a warp, a viewer can lose all
 * sense of where they are in the run as a whole. Here one pixel is always the
 * same number of wall-milliseconds, so the viewport rectangle visibly collapses
 * to a sliver during a long idle stretch and swells during a burst. That
 * contrast *is* the feature: it tells the truth about proportion that the
 * express lane deliberately distorts.
 *
 * Everything is drawn into a `<canvas>`; a run with thousands of density
 * buckets would be prohibitively expensive as DOM. Canvas access is guarded
 * throughout so the component degrades to an empty strip rather than throwing
 * in environments without a 2D context (happy-dom, for instance).
 */

import { LitElement, html, css } from 'lit';
import { customElement, property, query, state } from 'lit/decorators.js';
import type { Density } from '../core/types.js';

/** A point of interest pinned to a wall time. */
export interface SeqMinimapMarker {
  wallMs: number;
  kind: 'error' | 'spawn' | 'note';
  label?: string;
}

/** Vertical padding, in CSS pixels, above and below the time strip. */
const PAD_Y = 4;

/** Fallback colours, used when the theme tokens cannot be resolved. */
const FALLBACK = {
  bg: '#f8fafc',
  border: '#e2e8f0',
  heat: '#3b82f6',
  playhead: '#ef4444',
  viewport: '#3b82f6',
  error: '#ef4444',
  spawn: '#10b981',
  note: '#64748b',
} as const;

@customElement('scion-seq-minimap')
export class ScionSeqMinimap extends LitElement {
  /** Uniform activity sampling for the whole run. */
  @property({ attribute: false })
  density: Density | null = null;

  /** Total wall duration of the run; the strip spans exactly this. */
  @property({ type: Number })
  durationMs = 0;

  /** Playhead position in wall time. */
  @property({ type: Number })
  wallMs = 0;

  /** Wall time at the top of the main view's visible window. */
  @property({ type: Number })
  viewportStartMs = 0;

  /** Wall time at the bottom of the main view's visible window. */
  @property({ type: Number })
  viewportEndMs = 0;

  /** Points of interest to tick along the strip. */
  @property({ attribute: false })
  markers: SeqMinimapMarker[] = [];

  @query('canvas')
  private canvas!: HTMLCanvasElement;

  @state()
  private dragging = false;

  private resizeObserver: ResizeObserver | null = null;

  static override styles = css`
    :host {
      display: block;
      position: relative;
      width: 3rem;
      height: 100%;
      background: var(--scion-bg-subtle, #f8fafc);
      border-left: 1px solid var(--scion-border, #e2e8f0);
      cursor: pointer;
      touch-action: none;
      user-select: none;
    }

    :host([hidden]) {
      display: none;
    }

    canvas {
      display: block;
      width: 100%;
      height: 100%;
    }

    .grabbing {
      cursor: grabbing;
    }
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    if (typeof ResizeObserver !== 'undefined') {
      this.resizeObserver = new ResizeObserver(() => this.draw());
      this.resizeObserver.observe(this);
    }
  }

  override disconnectedCallback(): void {
    this.resizeObserver?.disconnect();
    this.resizeObserver = null;
    super.disconnectedCallback();
  }

  override firstUpdated(): void {
    this.draw();
  }

  override updated(): void {
    this.draw();
  }

  /** Resolves a theme token against this element, with a literal fallback. */
  private token(name: string, fallback: string): string {
    try {
      const value = getComputedStyle(this).getPropertyValue(name).trim();
      return value || fallback;
    } catch {
      return fallback;
    }
  }

  /** Maps a wall time to a y offset in CSS pixels within the strip. */
  private yFor(wallMs: number, height: number): number {
    const span = Math.max(1, this.durationMs);
    const usable = Math.max(1, height - PAD_Y * 2);
    const clamped = Math.min(Math.max(wallMs, 0), span);
    return PAD_Y + (clamped / span) * usable;
  }

  /** Inverse of {@link yFor}: a pointer position back to a wall time. */
  private wallFor(y: number, height: number): number {
    const span = Math.max(1, this.durationMs);
    const usable = Math.max(1, height - PAD_Y * 2);
    const fraction = (y - PAD_Y) / usable;
    return Math.min(Math.max(fraction, 0), 1) * span;
  }

  private emitSeek(clientY: number): void {
    const rect = this.getBoundingClientRect();
    if (rect.height <= 0) return;
    const wallMs = this.wallFor(clientY - rect.top, rect.height);
    this.dispatchEvent(
      new CustomEvent<{ wallMs: number }>('seq-seek-wall', {
        detail: { wallMs },
        bubbles: true,
        composed: true,
      })
    );
  }

  private onPointerDown(e: PointerEvent): void {
    this.dragging = true;
    try {
      this.setPointerCapture(e.pointerId);
    } catch {
      // Pointer capture is unavailable in some test environments; dragging
      // still works via the pointermove listener on this element.
    }
    this.emitSeek(e.clientY);
  }

  private onPointerMove(e: PointerEvent): void {
    if (!this.dragging) return;
    this.emitSeek(e.clientY);
  }

  private onPointerUp(e: PointerEvent): void {
    if (!this.dragging) return;
    this.dragging = false;
    try {
      this.releasePointerCapture(e.pointerId);
    } catch {
      // See onPointerDown.
    }
  }

  /**
   * Repaints the strip. Any missing capability (no canvas, no 2D context, zero
   * size) short-circuits into a no-op rather than an exception, so the
   * component is safe to instantiate anywhere.
   */
  private draw(): void {
    const canvas = this.canvas;
    if (!canvas || typeof canvas.getContext !== 'function') return;

    let ctx: CanvasRenderingContext2D | null = null;
    try {
      ctx = canvas.getContext('2d');
    } catch {
      return;
    }
    if (!ctx) return;

    const rect = this.getBoundingClientRect();
    const width = Math.max(0, Math.round(rect.width));
    const height = Math.max(0, Math.round(rect.height));
    if (width === 0 || height === 0) return;

    const dpr = typeof devicePixelRatio === 'number' && devicePixelRatio > 0 ? devicePixelRatio : 1;
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);

    this.drawDensity(ctx, width, height);
    this.drawViewport(ctx, width, height);
    this.drawMarkers(ctx, width, height);
    this.drawPlayhead(ctx, width, height);
  }

  /** Heat gradient: one row per pixel, sampled from the density buckets. */
  private drawDensity(ctx: CanvasRenderingContext2D, width: number, height: number): void {
    const density = this.density;
    if (!density || density.samples.length === 0) return;
    const peak = density.peak > 0 ? density.peak : Math.max(...density.samples, 1);
    const heat = this.token('--scion-primary', FALLBACK.heat);
    const usable = Math.max(1, height - PAD_Y * 2);
    const count = density.samples.length;

    for (let y = 0; y < usable; y++) {
      const index = Math.min(count - 1, Math.floor((y / usable) * count));
      const sample = density.samples[index] ?? 0;
      // Square-root compression: a run's peak is often orders of magnitude
      // above its median, and a linear ramp would render everything but the
      // burst as blank.
      const intensity = Math.min(1, Math.sqrt(Math.max(0, sample) / peak));
      if (intensity <= 0.01) continue;
      ctx.globalAlpha = 0.12 + intensity * 0.68;
      ctx.fillStyle = heat;
      ctx.fillRect(0, PAD_Y + y, width, 1);
    }
    ctx.globalAlpha = 1;
  }

  /** The current window, drawn as a bright inset rectangle. */
  private drawViewport(ctx: CanvasRenderingContext2D, width: number, height: number): void {
    if (this.viewportEndMs <= this.viewportStartMs) return;
    const top = this.yFor(this.viewportStartMs, height);
    const bottom = this.yFor(this.viewportEndMs, height);
    // A window can legitimately be thinner than a pixel on a long run; keep it
    // visible rather than letting it vanish.
    const h = Math.max(2, bottom - top);
    const accent = this.token('--scion-primary', FALLBACK.viewport);

    ctx.save();
    ctx.globalAlpha = 0.18;
    ctx.fillStyle = accent;
    ctx.fillRect(1, top, width - 2, h);
    ctx.globalAlpha = 1;
    ctx.strokeStyle = accent;
    ctx.lineWidth = 1;
    ctx.strokeRect(1.5, top + 0.5, width - 3, Math.max(1, h - 1));
    ctx.restore();
  }

  /** Markers as short ticks on the left edge, coloured by kind. */
  private drawMarkers(ctx: CanvasRenderingContext2D, width: number, height: number): void {
    if (!this.markers || this.markers.length === 0) return;
    const colors: Record<SeqMinimapMarker['kind'], string> = {
      error: this.token('--scion-danger', FALLBACK.error),
      spawn: this.token('--scion-success', FALLBACK.spawn),
      note: this.token('--scion-text-muted', FALLBACK.note),
    };
    for (const marker of this.markers) {
      const y = Math.round(this.yFor(marker.wallMs, height)) + 0.5;
      ctx.strokeStyle = colors[marker.kind] ?? FALLBACK.note;
      ctx.lineWidth = marker.kind === 'error' ? 2 : 1;
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(marker.kind === 'note' ? width * 0.3 : width * 0.45, y);
      ctx.stroke();
    }
  }

  private drawPlayhead(ctx: CanvasRenderingContext2D, width: number, height: number): void {
    const y = Math.round(this.yFor(this.wallMs, height)) + 0.5;
    ctx.strokeStyle = this.token('--scion-danger', FALLBACK.playhead);
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    ctx.moveTo(0, y);
    ctx.lineTo(width, y);
    ctx.stroke();
  }

  override render() {
    const label =
      `Run overview, ${Math.round(this.durationMs / 1000)} seconds at uniform scale. ` +
      `Click or drag to seek.`;
    return html`
      <canvas
        class=${this.dragging ? 'grabbing' : ''}
        role="slider"
        aria-label=${label}
        aria-valuemin="0"
        aria-valuemax=${this.durationMs}
        aria-valuenow=${Math.round(this.wallMs)}
        @pointerdown=${(e: PointerEvent): void => this.onPointerDown(e)}
        @pointermove=${(e: PointerEvent): void => this.onPointerMove(e)}
        @pointerup=${(e: PointerEvent): void => this.onPointerUp(e)}
        @pointercancel=${(e: PointerEvent): void => this.onPointerUp(e)}
      ></canvas>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-seq-minimap': ScionSeqMinimap;
  }
}
