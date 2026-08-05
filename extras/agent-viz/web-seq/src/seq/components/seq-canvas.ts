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
 * Canvas host for the sequence strip.
 *
 * This component is deliberately thin: it owns a `<canvas>`, a device-pixel
 * scaling policy and pointer hit-testing, and delegates every decision about
 * *what* to draw to the DOM-free core (`buildFrame` + `renderFrame`). Keeping
 * the drawing logic out of the component is what makes it unit-testable
 * without a browser and portable into the main web UI later.
 *
 * Canvas rather than SVG/DOM because 50-100 columns times thousands of
 * intervals must stay at 60fps; a node per interval would not.
 */

import { LitElement, html, css } from 'lit';
import { customElement, property, query } from 'lit/decorators.js';

import type { Digest } from '../core/types.js';
import type { ColumnLayout } from '../core/columns.js';
import { buildFrame, type FrameModel } from '../core/frame.js';
import { renderFrame, hitTest, DEFAULT_THEME, type HitResult } from '../core/render.js';

/** Fraction of the canvas height where the readable frame begins. */
const DEFAULT_FRAME_TOP = 0.3;
/** Fraction of the canvas height where the readable frame ends (the playhead). */
const DEFAULT_FRAME_BOTTOM = 0.78;

@customElement('scion-seq-canvas')
export class ScionSeqCanvas extends LitElement {
  @property({ attribute: false }) digest: Digest | null = null;
  @property({ attribute: false }) layout: ColumnLayout | null = null;

  /** Current wall time at the playhead. */
  @property({ type: Number }) wallMs = 0;
  /** Current warp velocity; drives the staging-zone streak treatment. */
  @property({ type: Number }) velocity = 1;
  /** Wall-ms per pixel. Constant across all zones, so geometry never lies. */
  @property({ type: Number }) msPerPx = 40;

  @property({ type: Number }) frameTop = DEFAULT_FRAME_TOP;
  @property({ type: Number }) frameBottom = DEFAULT_FRAME_BOTTOM;

  @query('canvas') private canvasEl?: HTMLCanvasElement;

  /** Most recently built frame, exposed for hit-testing and viewport reporting. */
  private lastFrame: FrameModel | null = null;

  private resizeObserver?: ResizeObserver;
  private cssWidth = 0;
  private cssHeight = 0;

  /** Viewport bounds last reported, to avoid redundant events. */
  private reportedStart = NaN;
  private reportedEnd = NaN;

  static override styles = css`
    :host {
      display: block;
      position: relative;
      width: 100%;
      height: 100%;
      background: var(--scion-bg, #0f172a);
      overflow: hidden;
    }
    canvas {
      display: block;
      width: 100%;
      height: 100%;
      cursor: crosshair;
    }
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    if (typeof ResizeObserver !== 'undefined') {
      this.resizeObserver = new ResizeObserver(() => this.measure());
      this.resizeObserver.observe(this);
    }
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.resizeObserver?.disconnect();
    this.resizeObserver = undefined as unknown as ResizeObserver;
  }

  override firstUpdated(): void {
    this.measure();
  }

  override updated(): void {
    this.draw();
  }

  /** The frame currently on screen, or null before the first draw. */
  get frame(): FrameModel | null {
    return this.lastFrame;
  }

  private measure(): void {
    const rect = this.getBoundingClientRect();
    const w = Math.max(1, Math.floor(rect.width));
    const h = Math.max(1, Math.floor(rect.height));
    if (w === this.cssWidth && h === this.cssHeight) return;
    this.cssWidth = w;
    this.cssHeight = h;
    this.draw();
  }

  private draw(): void {
    const canvas = this.canvasEl;
    if (!canvas || !this.digest || !this.layout) return;
    if (this.cssWidth <= 0 || this.cssHeight <= 0) return;

    // happy-dom and other non-browser environments have no 2D context; degrade
    // rather than throw so the component can still be mounted in tests.
    let ctx: CanvasRenderingContext2D | null = null;
    try {
      ctx = canvas.getContext('2d');
    } catch {
      ctx = null;
    }
    if (!ctx) return;

    const dpr = typeof window !== 'undefined' ? Math.min(window.devicePixelRatio || 1, 2) : 1;
    const pixelW = Math.floor(this.cssWidth * dpr);
    const pixelH = Math.floor(this.cssHeight * dpr);
    if (canvas.width !== pixelW || canvas.height !== pixelH) {
      canvas.width = pixelW;
      canvas.height = pixelH;
    }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    const model = buildFrame(
      this.digest,
      this.layout,
      {
        width: this.cssWidth,
        height: this.cssHeight,
        frameTop: this.frameTop,
        frameBottom: this.frameBottom,
        msPerPx: this.msPerPx,
      },
      this.wallMs,
      this.velocity
    );
    this.lastFrame = model;

    renderFrame(ctx, model, DEFAULT_THEME);

    // Tell the parent which slice of wall time is on screen, so the minimap can
    // draw the viewport rectangle against the true-linear overview.
    if (model.visibleStartMs !== this.reportedStart || model.visibleEndMs !== this.reportedEnd) {
      this.reportedStart = model.visibleStartMs;
      this.reportedEnd = model.visibleEndMs;
      this.dispatchEvent(
        new CustomEvent('seq-viewport', {
          detail: { startMs: model.visibleStartMs, endMs: model.visibleEndMs },
          bubbles: true,
          composed: true,
        })
      );
    }
  }

  private onClick(e: MouseEvent): void {
    const model = this.lastFrame;
    const canvas = this.canvasEl;
    if (!model || !canvas) return;
    const rect = canvas.getBoundingClientRect();
    const hit: HitResult | null = hitTest(model, e.clientX - rect.left, e.clientY - rect.top);
    this.dispatchEvent(
      new CustomEvent('seq-select', {
        detail: { hit },
        bubbles: true,
        composed: true,
      })
    );
  }

  override render(): unknown {
    return html`<canvas @click=${(e: MouseEvent): void => this.onClick(e)}></canvas>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-seq-canvas': ScionSeqCanvas;
  }
}
