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
 * A vertical strip showing the *whole* run at a strictly uniform scale - the
 * honest counterweight to the elastic main view.
 *
 * Because the main canvas is traversed through a warp, a viewer can lose all
 * sense of where they are in the run as a whole. Here one pixel is always the
 * same number of wall-milliseconds, so the viewport rectangle visibly collapses
 * to a sliver during a long idle stretch and swells during a burst. That
 * contrast *is* the feature: it tells the truth about proportion that the
 * express lane deliberately distorts.
 *
 * The strip is laid out in three bands, left to right:
 *
 *   | mm:ss ticks | density heat | one lane per lifeline |
 *
 * The lanes are what make this a *map* rather than a scrollbar: each lifeline
 * gets a fixed column for the entire run, so "who was alive when, and who was
 * actually busy" is readable at a glance and a click lands you there. A
 * scrollbar-width strip cannot carry that, which is why this is sized in
 * proportion to what it has to show.
 *
 * Everything is drawn into a `<canvas>`; a run with thousands of intervals
 * would be prohibitively expensive as DOM. The static bands (ticks, heat,
 * lanes) are rasterized once into an offscreen layer and blitted each frame, so
 * per-frame cost is one `drawImage` plus the moving parts. Canvas access is
 * guarded throughout so the component degrades to an empty strip rather than
 * throwing in environments without a 2D context (happy-dom, for instance).
 */

import { LitElement, html, css } from 'lit';
import { customElement, property, query, state } from 'lit/decorators.js';
import type { Density, Interval, Lifeline } from '../core/types.js';

/** A point of interest pinned to a wall time. */
export interface SeqMinimapMarker {
  wallMs: number;
  kind: 'error' | 'spawn' | 'note';
  label?: string;
}

/** Vertical padding, in CSS pixels, above and below the time strip. */
const PAD_Y = 6;

/** Width of the mm:ss gutter, in CSS pixels. */
const TICK_W = 34;

/** Width of the density heat band, in CSS pixels. */
const HEAT_W = 9;

/** Lanes narrower than this are not worth drawing separately. */
const MIN_LANE_W = 2;

/**
 * Extra grab margin around the viewport rectangle, in CSS pixels.
 *
 * On a long run the window is legitimately a two-pixel sliver, which is not a
 * target anyone can hit. The margin makes it one without making the whole strip
 * draggable.
 */
const VIEWPORT_GRAB_PAD = 7;

/**
 * How long the pointer must linger before the hover readout appears, in ms.
 *
 * Without it the readout fires on the way past: the strip sits against the edge
 * of the window, so the pointer crosses it constantly on its way somewhere
 * else, and a dashed line plus a floating label flashing on every transit is
 * noise. A short dwell makes it appear only when it was asked for.
 */
const HOVER_DWELL_MS = 220;

/** Target vertical spacing between time labels, in CSS pixels. */
const TICK_SPACING_PX = 58;

/** Candidate label intervals, in ms; the first that clears the spacing wins. */
const TICK_STEPS_MS = [
  1_000, 5_000, 10_000, 30_000, 60_000, 120_000, 300_000, 600_000, 900_000, 1_800_000, 3_600_000,
  7_200_000, 21_600_000, 43_200_000, 86_400_000,
];

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
  muted: '#64748b',
} as const;

@customElement('scion-seq-minimap')
export class ScionSeqMinimap extends LitElement {
  /** Uniform activity sampling for the whole run. */
  @property({ attribute: false })
  density: Density | null = null;

  /** Every lifeline in the run, in depth-first order; one lane each. */
  @property({ attribute: false })
  lifelines: readonly Lifeline[] = [];

  /** Every interval in the run; painted into the lanes. */
  @property({ attribute: false })
  intervals: readonly Interval[] = [];

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

  /** Lifeline to pick out from the rest, usually the current selection. */
  @property({ type: String })
  highlightLifelineId: string | null = null;

  @query('canvas')
  private canvas!: HTMLCanvasElement;

  @state()
  private dragging = false;

  /** True while the pointer is over the draggable viewport window. */
  @state()
  private overViewport = false;

  /** Wall time under the cursor, or null when the pointer is away. */
  @state()
  private hoverWallMs: number | null = null;

  /** Pending dwell timer; the readout only appears when it fires. */
  private hoverTimer: ReturnType<typeof setTimeout> | null = null;
  /** Last pointer offset within the strip, in CSS pixels. */
  private hoverY = 0;

  /**
   * Wall time under the pointer when the drag began, and the playhead's wall
   * time at that instant.
   *
   * The drag is relative to these rather than absolute, so the window keeps the
   * exact grip the pointer took on it. Seeking to the pointer's own position
   * would snap the window's centre under the cursor the moment it moved.
   */
  private dragAnchorWallMs = 0;
  private dragStartWallMs = 0;
  /** Element holding the pointer capture for the current drag. */
  private captured: Element | null = null;

  private resizeObserver: ResizeObserver | null = null;

  /**
   * Cached raster of the bands that only change when the data or the size
   * changes. Rebuilt lazily; see {@link staticKey}.
   */
  private layer: HTMLCanvasElement | null = null;
  private layerKey = '';

  static override styles = css`
    :host {
      display: block;
      position: relative;
      width: 100%;
      height: 100%;
      background: var(--scion-bg-subtle, #f8fafc);
      /* Default cursor, because most of the strip does nothing on a press.
         Only the window is a control, and it is the part that says grab. */
      cursor: default;
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

    canvas.grab {
      cursor: grab;
    }

    canvas.grabbing {
      cursor: grabbing;
    }

    .readout {
      position: absolute;
      left: 2px;
      right: 2px;
      transform: translateY(-50%);
      pointer-events: none;
      font-family: var(--scion-font-mono, monospace);
      font-size: 10px;
      line-height: 1.4;
      text-align: center;
      color: var(--scion-text, #f1f5f9);
      background: var(--scion-surface-raised, #263449);
      border: 1px solid var(--scion-border-hover, #475569);
      border-radius: var(--scion-radius-sm, 0.25rem);
      padding: 1px 0;
    }
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    if (typeof ResizeObserver !== 'undefined') {
      this.resizeObserver = new ResizeObserver(() => {
        this.layerKey = ''; // size changed: the raster is stale
        this.draw();
      });
      this.resizeObserver.observe(this);
    }
  }

  override disconnectedCallback(): void {
    this.resizeObserver?.disconnect();
    this.resizeObserver = null;
    this.layer = null;
    this.clearHover();
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

  /**
   * Inverse of {@link yFor}: a pointer position back to a wall time.
   *
   * `clamp` is off during a drag. A viewport window at either end of the run
   * hangs off the strip -- the window is wider than the run's remaining time --
   * so a grab near the origin lands on a y whose wall time is negative. Pinning
   * that to zero makes the drag delta short by however far off the end the grab
   * was, and the window slides out from under the pointer exactly where the
   * geometry is tightest. Off the ends the mapping stays linear; the clock
   * clamps the resulting seek anyway.
   */
  private wallFor(y: number, height: number, clamp = true): number {
    const span = Math.max(1, this.durationMs);
    const usable = Math.max(1, height - PAD_Y * 2);
    const fraction = (y - PAD_Y) / usable;
    if (!clamp) return fraction * span;
    return Math.min(Math.max(fraction, 0), 1) * span;
  }

  /** Left edge and width of the lane band, in CSS pixels. */
  private laneBand(width: number): { x: number; w: number } {
    const x = Math.min(TICK_W + HEAT_W, Math.max(0, width - MIN_LANE_W));
    return { x, w: Math.max(0, width - x) };
  }

  private emitSeek(wallMs: number): void {
    this.dispatchEvent(
      new CustomEvent<{ wallMs: number }>('seq-seek-wall', {
        detail: { wallMs },
        bubbles: true,
        composed: true,
      })
    );
  }

  /**
   * Vertical extent of the grabbable window, in CSS pixels, or null when there
   * is no window to grab.
   */
  private grabBand(height: number): { top: number; bottom: number } | null {
    if (this.viewportEndMs <= this.viewportStartMs) return null;
    const top = this.yFor(this.viewportStartMs, height);
    const bottom = Math.max(top + 2, this.yFor(this.viewportEndMs, height));
    return { top: top - VIEWPORT_GRAB_PAD, bottom: bottom + VIEWPORT_GRAB_PAD };
  }

  /** True when a pointer at this offset would take hold of the window. */
  private isOnViewport(y: number, height: number): boolean {
    const band = this.grabBand(height);
    return band !== null && y >= band.top && y <= band.bottom;
  }

  /**
   * Begins a drag, but only on the window itself.
   *
   * Seeking used to happen on any press anywhere in the strip, which made the
   * whole right-hand edge of the app a trapdoor: a stray click while reaching
   * for the scrollbar threw the playhead across the run with nothing to undo
   * it. Now the window is a handle, and everywhere else is inert.
   */
  private onPointerDown(e: PointerEvent): void {
    const rect = this.getBoundingClientRect();
    if (rect.height <= 0) return;
    const y = e.clientY - rect.top;
    if (!this.isOnViewport(y, rect.height)) return;

    this.dragging = true;
    this.clearHover();
    this.dragAnchorWallMs = this.wallFor(y, rect.height, false);
    this.dragStartWallMs = this.wallMs;
    // Capture on the canvas, not the host. The listeners are on the canvas,
    // and capturing on the host retargets every subsequent pointermove to the
    // host -- which is an ancestor, so the events never reach the listener and
    // the drag silently does nothing after the initial press.
    const target = e.currentTarget;
    if (target instanceof Element) {
      try {
        target.setPointerCapture(e.pointerId);
        this.captured = target;
      } catch {
        // Pointer capture is unavailable in some test environments; the drag
        // still works as long as the pointer stays over the strip.
      }
    }
  }

  private onPointerMove(e: PointerEvent): void {
    const rect = this.getBoundingClientRect();
    if (rect.height <= 0) return;
    const y = e.clientY - rect.top;

    if (this.dragging) {
      const moved = this.wallFor(y, rect.height, false) - this.dragAnchorWallMs;
      this.emitSeek(this.dragStartWallMs + moved);
      return;
    }

    this.overViewport = this.isOnViewport(y, rect.height);
    if (this.overViewport) {
      // No readout on the handle. It would sit directly over the window --
      // hiding the grab affordance at the moment the reader is reaching for it
      // -- to report a time the transport clock is already showing, since the
      // window is where the playhead is.
      this.clearHover();
      return;
    }
    this.trackHover(y, rect.height);
  }

  /**
   * Updates the hover readout, subject to the dwell delay.
   *
   * Once the readout is up it follows the pointer immediately - the delay is
   * about not appearing unbidden, not about lagging behind once it has.
   * Movement under a pixel is ignored so a resting hand does not repaint.
   */
  private trackHover(y: number, height: number): void {
    if (Math.round(y) === Math.round(this.hoverY) && this.hoverTimer !== null) return;
    this.hoverY = y;
    if (this.hoverWallMs !== null) {
      this.hoverWallMs = this.wallFor(y, height);
      return;
    }
    if (this.hoverTimer !== null) return;
    this.hoverTimer = setTimeout(() => {
      this.hoverTimer = null;
      const rect = this.getBoundingClientRect();
      if (rect.height > 0) this.hoverWallMs = this.wallFor(this.hoverY, rect.height);
    }, HOVER_DWELL_MS);
  }

  private clearHover(): void {
    if (this.hoverTimer !== null) {
      clearTimeout(this.hoverTimer);
      this.hoverTimer = null;
    }
    this.hoverWallMs = null;
  }

  private onPointerUp(e: PointerEvent): void {
    if (!this.dragging) return;
    this.dragging = false;
    try {
      this.captured?.releasePointerCapture(e.pointerId);
    } catch {
      // See onPointerDown.
    }
    this.captured = null;
  }

  private onPointerLeave(): void {
    this.overViewport = false;
    this.clearHover();
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

    this.blitStatic(ctx, width, height, dpr);
    this.drawViewport(ctx, width, height);
    this.drawMarkers(ctx, width, height);
    this.drawPlayhead(ctx, width, height);
    this.drawHover(ctx, width, height);
  }

  /**
   * Identity of the cached raster. Any change here invalidates it. The playhead
   * and viewport are deliberately absent: they move every frame and are drawn
   * live on top.
   */
  private staticKey(width: number, height: number): string {
    return [
      width,
      height,
      this.durationMs,
      this.lifelines.length,
      this.intervals.length,
      this.density?.samples.length ?? 0,
      this.highlightLifelineId ?? '',
    ].join(':');
  }

  private blitStatic(
    ctx: CanvasRenderingContext2D,
    width: number,
    height: number,
    dpr: number
  ): void {
    const key = this.staticKey(width, height);
    if (!this.layer || this.layerKey !== key) {
      const layer = this.renderStatic(width, height, dpr);
      if (!layer) {
        // No offscreen canvas available: draw straight to the target. Slower
        // per frame, but correctness does not depend on the cache.
        this.drawHeat(ctx, width, height);
        this.drawLanes(ctx, width, height);
        this.drawTimeTicks(ctx, width, height);
        return;
      }
      this.layer = layer;
      this.layerKey = key;
    }
    try {
      ctx.drawImage(this.layer, 0, 0, width, height);
    } catch {
      // A stub canvas may not support drawImage; the moving parts still paint.
    }
  }

  /** Rasterizes the bands that do not move, or null if that is not possible. */
  private renderStatic(width: number, height: number, dpr: number): HTMLCanvasElement | null {
    let layer: HTMLCanvasElement;
    try {
      layer = document.createElement('canvas');
      layer.width = Math.max(1, Math.round(width * dpr));
      layer.height = Math.max(1, Math.round(height * dpr));
    } catch {
      return null;
    }
    if (typeof layer.getContext !== 'function') return null;
    let lctx: CanvasRenderingContext2D | null = null;
    try {
      lctx = layer.getContext('2d');
    } catch {
      return null;
    }
    if (!lctx) return null;
    lctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.drawHeat(lctx, width, height);
    this.drawLanes(lctx, width, height);
    this.drawTimeTicks(lctx, width, height);
    return layer;
  }

  /** Heat band: one row per pixel, sampled from the density buckets. */
  private drawHeat(ctx: CanvasRenderingContext2D, width: number, height: number): void {
    const density = this.density;
    if (!density || density.samples.length === 0) return;
    const peak = density.peak > 0 ? density.peak : Math.max(...density.samples, 1);
    const heat = this.token('--scion-primary', FALLBACK.heat);
    const usable = Math.max(1, height - PAD_Y * 2);
    const count = density.samples.length;
    const w = Math.min(HEAT_W, Math.max(1, width - TICK_W));
    const x = Math.max(0, Math.min(TICK_W, width - w));

    for (let y = 0; y < usable; y++) {
      const index = Math.min(count - 1, Math.floor((y / usable) * count));
      const sample = density.samples[index] ?? 0;
      // Square-root compression: a run's peak is often orders of magnitude
      // above its median, and a linear ramp would render everything but the
      // burst as blank.
      const intensity = Math.min(1, Math.sqrt(Math.max(0, sample) / peak));
      if (intensity <= 0.01) continue;
      ctx.globalAlpha = 0.15 + intensity * 0.75;
      ctx.fillStyle = heat;
      ctx.fillRect(x, PAD_Y + y, w, 1);
    }
    ctx.globalAlpha = 1;
  }

  /**
   * One lane per lifeline: a faint band for its lifetime, solid marks for the
   * intervals in which it was actually doing something.
   *
   * This is the part that turns a progress gutter into a map. Lifetime alone
   * would say only "existed"; the busy marks are what let you see a subgraph
   * wake up, work, and go quiet, and pick the moment worth seeking to.
   */
  private drawLanes(ctx: CanvasRenderingContext2D, width: number, height: number): void {
    const lifelines = this.lifelines;
    if (lifelines.length === 0) return;
    const band = this.laneBand(width);
    if (band.w < MIN_LANE_W) return;

    const ordered = [...lifelines].sort((a, b) => a.order - b.order);
    const laneW = band.w / ordered.length;
    if (laneW < 0.5) return; // beyond this the lanes are noise, not signal
    const drawW = Math.max(1, laneW - (laneW > 3 ? 1 : 0));

    const laneX = new Map<string, number>();
    ordered.forEach((l, i) => laneX.set(l.id, band.x + i * laneW));

    // Lifetime bands first, so busy marks sit on top of them.
    for (const l of ordered) {
      const x = laneX.get(l.id) ?? band.x;
      const top = this.yFor(l.birthMs, height);
      const bottom = this.yFor(l.deathMs, height);
      ctx.globalAlpha = 0.22;
      ctx.fillStyle = l.color;
      ctx.fillRect(x, top, drawW, Math.max(1, bottom - top));
    }

    const danger = this.token('--scion-danger', FALLBACK.error);
    const colorOf = new Map(ordered.map((l) => [l.id, l.color]));
    for (const iv of this.intervals) {
      // Lifecycle spans duplicate the lifetime band drawn above.
      if (iv.kind === 'lifecycle') continue;
      const x = laneX.get(iv.lifelineId);
      if (x === undefined) continue;
      const top = this.yFor(iv.startMs, height);
      const bottom = this.yFor(iv.endMs, height);
      ctx.globalAlpha = iv.error ? 1 : 0.75;
      ctx.fillStyle = iv.error ? danger : (colorOf.get(iv.lifelineId) ?? FALLBACK.note);
      ctx.fillRect(x, top, drawW, Math.max(1, bottom - top));
    }
    ctx.globalAlpha = 1;

    // The selected lifeline gets an outline so it can be followed through the
    // whole run without hunting for its colour.
    const hl = this.highlightLifelineId;
    if (hl && laneX.has(hl)) {
      const x = laneX.get(hl) as number;
      ctx.strokeStyle = this.token('--scion-text', '#f1f5f9');
      ctx.globalAlpha = 0.85;
      ctx.lineWidth = 1;
      ctx.strokeRect(Math.round(x) + 0.5, PAD_Y + 0.5, Math.max(1, drawW - 1), height - PAD_Y * 2);
      ctx.globalAlpha = 1;
    }
  }

  /** mm:ss gutter down the left edge, at a round interval. */
  private drawTimeTicks(ctx: CanvasRenderingContext2D, width: number, height: number): void {
    if (width < TICK_W + 4 || this.durationMs <= 0) return;
    const usable = Math.max(1, height - PAD_Y * 2);
    const pxPerMs = usable / Math.max(1, this.durationMs);
    const step =
      TICK_STEPS_MS.find((s) => s * pxPerMs >= TICK_SPACING_PX) ??
      TICK_STEPS_MS[TICK_STEPS_MS.length - 1];

    const muted = this.token('--scion-text-muted', FALLBACK.muted);
    ctx.fillStyle = muted;
    ctx.strokeStyle = muted;
    ctx.globalAlpha = 0.7;
    ctx.font = '9px var(--scion-font-mono, monospace)';
    ctx.textAlign = 'right';
    ctx.textBaseline = 'middle';

    for (let t = 0; t <= this.durationMs; t += step) {
      const y = Math.round(this.yFor(t, height)) + 0.5;
      ctx.globalAlpha = 0.25;
      ctx.beginPath();
      ctx.moveTo(TICK_W - 3, y);
      ctx.lineTo(width, y);
      ctx.stroke();
      ctx.globalAlpha = 0.8;
      ctx.fillText(formatOffset(t), TICK_W - 5, y);
    }
    ctx.globalAlpha = 1;
    ctx.textAlign = 'left';
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
    // The window is the only handle on the strip, so it has to look like one
    // the moment the pointer is near it.
    const active = this.overViewport || this.dragging;

    ctx.save();
    ctx.globalAlpha = active ? 0.28 : 0.16;
    ctx.fillStyle = accent;
    ctx.fillRect(1, top, width - 2, h);
    ctx.globalAlpha = 1;
    ctx.strokeStyle = accent;
    ctx.lineWidth = active ? 2 : 1;
    ctx.strokeRect(1.5, top + 0.5, width - 3, Math.max(1, h - 1));

    if (active) {
      // Grip bars on both edges: the conventional "this moves" marking, and
      // they stay visible when the window itself is only a few pixels tall.
      ctx.globalAlpha = 0.9;
      ctx.lineWidth = 2;
      const cx = width / 2;
      for (const y of [top, top + h]) {
        ctx.beginPath();
        ctx.moveTo(cx - 7, y);
        ctx.lineTo(cx + 7, y);
        ctx.stroke();
      }
    }
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
    const x0 = Math.max(0, TICK_W - 4);
    for (const marker of this.markers) {
      const y = Math.round(this.yFor(marker.wallMs, height)) + 0.5;
      ctx.strokeStyle = colors[marker.kind] ?? FALLBACK.note;
      ctx.lineWidth = marker.kind === 'error' ? 2 : 1;
      ctx.beginPath();
      ctx.moveTo(x0, y);
      ctx.lineTo(Math.min(width, x0 + (marker.kind === 'note' ? 4 : 7)), y);
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

  private drawHover(ctx: CanvasRenderingContext2D, width: number, height: number): void {
    if (this.hoverWallMs === null) return;
    const y = Math.round(this.yFor(this.hoverWallMs, height)) + 0.5;
    ctx.strokeStyle = this.token('--scion-text', '#f1f5f9');
    ctx.globalAlpha = 0.45;
    ctx.lineWidth = 1;
    ctx.setLineDash([2, 2]);
    ctx.beginPath();
    ctx.moveTo(0, y);
    ctx.lineTo(width, y);
    ctx.stroke();
    ctx.setLineDash([]);
    ctx.globalAlpha = 1;
  }

  override render() {
    const label =
      `Run overview, ${Math.round(this.durationMs / 1000)} seconds at uniform scale. ` +
      `Drag the highlighted window to seek.`;
    // The readout is DOM rather than canvas text so it stays crisp and
    // selectable-looking at any device pixel ratio.
    const readout =
      this.hoverWallMs === null
        ? null
        : html`<div
            class="readout"
            style=${`top:${this.yFor(this.hoverWallMs, this.clientHeight || 1)}px`}
          >
            ${formatOffset(this.hoverWallMs)}
          </div>`;
    return html`
      <canvas
        class=${this.dragging ? 'grabbing' : this.overViewport ? 'grab' : ''}
        role="slider"
        aria-label=${label}
        aria-valuemin="0"
        aria-valuemax=${this.durationMs}
        aria-valuenow=${Math.round(this.wallMs)}
        @pointerdown=${(e: PointerEvent): void => this.onPointerDown(e)}
        @pointermove=${(e: PointerEvent): void => this.onPointerMove(e)}
        @pointerup=${(e: PointerEvent): void => this.onPointerUp(e)}
        @pointercancel=${(e: PointerEvent): void => this.onPointerUp(e)}
        @pointerleave=${(): void => this.onPointerLeave()}
      ></canvas>
      ${readout}
    `;
  }
}

/** Formats a run-relative offset as `h:mm:ss` or `mm:ss`. */
export function formatOffset(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const mm = h > 0 ? String(m).padStart(2, '0') : String(m);
  return `${h > 0 ? `${h}:` : ''}${mm}:${String(s).padStart(2, '0')}`;
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-seq-minimap': ScionSeqMinimap;
  }
}
