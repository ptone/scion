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
 * Root component of the sequence visualizer.
 *
 * Owns everything stateful - the digest, the playback clock, the column
 * collapse/solo state and the current selection - and drives a single
 * requestAnimationFrame loop. Every child is presentational: properties down,
 * CustomEvents up.
 *
 * The visualization is a sequence diagram whose vertical axis is metric wall
 * time. Two consequences shape this file:
 *
 *  1. Geometry is honest. `msPerPx` is constant across the whole canvas, so a
 *     bar's height is always proportional to its true duration. Boring
 *     stretches are compressed by traversing them *faster* (the warp), never by
 *     drawing them shorter.
 *  2. Column identity must be stable. See {@link ScionSeqViz.refreshLayout} -
 *     recomputing which columns are visible on every frame would make the axis
 *     shimmer and destroy object constancy, so it is deliberately throttled and
 *     hysteretic.
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

import type { Digest, Interval, Edge, Lifeline } from '../core/types.js';
import { SCHEMA_VERSION } from '../core/types.js';
import { WarpFn } from '../core/warp.js';
import { PlaybackClock } from '../core/clock.js';
import { computeColumns, activeLifelineIds, type ColumnLayout } from '../core/columns.js';
import type { HitResult } from '../core/render.js';

import './seq-canvas.js';
import './seq-transport.js';
import './seq-minimap.js';
import './seq-lifeline-tree.js';
import './seq-detail-panel.js';
import './seq-legend.js';
import type { SeqSelection } from './seq-detail-panel.js';
import type { SeqMinimapMarker } from './seq-minimap.js';

/** Column geometry, px. */
const MIN_COLUMN_WIDTH = 104;
const MAX_COLUMN_WIDTH = 240;
const COLUMN_GAP = 10;
const FOLDED_WIDTH = 8;

/** Zoom bounds, wall-ms per pixel. */
const MIN_MS_PER_PX = 2;
const MAX_MS_PER_PX = 4000;
const DEFAULT_MS_PER_PX = 60;

/**
 * Minimum interval between column-layout recomputations, ms.
 *
 * Auto-fold depends on which lifelines are active in the visible window, which
 * changes continuously during playback. Recomputing it every frame would make
 * columns appear, disappear and slide constantly - the single fastest way to
 * destroy object constancy in a view like this. Throttling plus the widened
 * window below means the axis changes rarely and visibly, rather than
 * shimmering.
 */
const LAYOUT_REFRESH_MS = 400;

/**
 * The activity window used for auto-fold is the visible window widened by this
 * factor. Hysteresis: a lifeline stays unfolded for a while after it goes quiet
 * and unfolds slightly before it becomes active, so columns do not flicker at
 * the boundary.
 */
const ACTIVITY_WINDOW_FACTOR = 3;

@customElement('scion-seq-viz')
export class ScionSeqViz extends LitElement {
  @state() private digest: Digest | null = null;
  @state() private loadError: string | null = null;
  @state() private loading = true;

  @state() private playing = false;
  @state() private tauMs = 0;
  @state() private wallMs = 0;
  @state() private velocity = 1;
  @state() private rate = 1;

  @state() private msPerPx = DEFAULT_MS_PER_PX;

  @state() private collapsed: ReadonlySet<string> = new Set<string>();
  @state() private solo: string | null = null;
  @state() private autoFold = true;

  @state() private layout: ColumnLayout | null = null;
  @state() private activeIds: ReadonlySet<string> = new Set<string>();

  @state() private selection: SeqSelection | null = null;
  @state() private selectedLifelineId: string | null = null;

  @state() private legendOpen = false;

  /**
   * Whether the run-overview rail is expanded.
   *
   * Reflected to an attribute because the rail's width lives in this host's
   * `grid-template-columns`, and `:host()` selectors are the only way a shadow
   * root can restyle its own host.
   */
  @property({ type: Boolean, reflect: true, attribute: 'rail-collapsed' })
  railCollapsed = false;

  @state() private viewportStartMs = 0;
  @state() private viewportEndMs = 0;
  @state() private canvasWidthPx = 0;

  private warp: WarpFn | null = null;
  private clock: PlaybackClock | null = null;

  private rafHandle = 0;
  private lastFrameTs = 0;
  private lastLayoutAt = 0;
  private layoutDirty = true;

  private intervalById = new Map<string, Interval>();
  private edgeById = new Map<string, Edge>();
  private lifelineById = new Map<string, Lifeline>();

  private readonly boundKeydown = (e: KeyboardEvent): void => this.onKeydown(e);

  static override styles = css`
    :host {
      display: grid;
      /*
        The overview rail is 200px because it has to carry a lane per lifeline
        plus a time gutter. At scrollbar width it can only be a progress
        indicator, which is not worth the screen edge it occupies.
      */
      grid-template-columns: 250px 1fr 200px;
      grid-template-rows: auto 1fr auto;
      height: 100%;
      background: var(--scion-bg, #0f172a);
      color: var(--scion-text, #f1f5f9);
      font-family: var(--scion-font-sans, system-ui, sans-serif);
      overflow: hidden;
    }

    header {
      grid-column: 1 / -1;
      display: flex;
      align-items: center;
      gap: var(--scion-space-4, 1rem);
      padding: var(--scion-space-2, 0.5rem) var(--scion-space-4, 1rem);
      border-bottom: 1px solid var(--scion-border, #334155);
      background: var(--scion-surface, #1e293b);
      min-height: 44px;
    }
    .title {
      font-size: var(--scion-font-size-sm, 0.875rem);
      font-weight: 600;
    }
    .sub {
      font-size: var(--scion-font-size-xs, 0.75rem);
      color: var(--scion-text-muted, #94a3b8);
      font-family: var(--scion-font-mono, monospace);
    }
    .spacer {
      flex: 1;
    }
    .ctl {
      display: flex;
      align-items: center;
      gap: var(--scion-space-2, 0.5rem);
      font-size: var(--scion-font-size-xs, 0.75rem);
      color: var(--scion-text-secondary, #cbd5e1);
    }
    .zoom button {
      background: var(--scion-surface-raised, #263449);
      color: var(--scion-text-secondary, #cbd5e1);
      border: 1px solid var(--scion-border, #334155);
      border-radius: var(--scion-radius-sm, 0.25rem);
      width: 22px;
      height: 22px;
      cursor: pointer;
      font-size: 12px;
      line-height: 1;
    }
    .zoom button:hover {
      border-color: var(--scion-border-hover, #475569);
      color: var(--scion-text, #f1f5f9);
    }

    aside {
      grid-row: 2;
      border-right: 1px solid var(--scion-border, #334155);
      background: var(--scion-surface, #1e293b);
      overflow: hidden;
      display: flex;
      flex-direction: column;
    }
    scion-seq-lifeline-tree {
      flex: 1;
      min-height: 0;
    }

    main {
      grid-row: 2;
      position: relative;
      min-width: 0;
      overflow: hidden;
    }
    scion-seq-canvas {
      position: absolute;
      inset: 0;
    }

    :host([rail-collapsed]) {
      grid-template-columns: 250px 1fr 22px;
    }

    .rail {
      grid-row: 2;
      border-left: 1px solid var(--scion-border, #334155);
      background: var(--scion-surface, #1e293b);
      display: flex;
      flex-direction: column;
      overflow: hidden;
    }
    .rail-head {
      display: flex;
      align-items: center;
      gap: var(--scion-space-1, 0.25rem);
      padding: 2px 2px 2px 6px;
      border-bottom: 1px solid var(--scion-border, #334155);
      font-size: 10px;
      letter-spacing: 0.06em;
      text-transform: uppercase;
      color: var(--scion-text-muted, #94a3b8);
      white-space: nowrap;
    }
    .rail-head .spacer {
      flex: 1;
    }
    .rail-head button {
      background: none;
      border: none;
      color: var(--scion-text-muted, #94a3b8);
      cursor: pointer;
      font-size: 11px;
      line-height: 1;
      padding: 3px 4px;
      border-radius: var(--scion-radius-sm, 0.25rem);
    }
    .rail-head button:hover {
      background: var(--scion-surface-raised, #263449);
      color: var(--scion-text, #f1f5f9);
    }
    scion-seq-minimap {
      flex: 1;
      min-height: 0;
    }

    footer {
      grid-column: 1 / -1;
      border-top: 1px solid var(--scion-border, #334155);
      background: var(--scion-surface, #1e293b);
    }

    .overlay {
      grid-column: 1 / -1;
      grid-row: 2;
      display: flex;
      align-items: center;
      justify-content: center;
      gap: var(--scion-space-3, 0.75rem);
      color: var(--scion-text-muted, #94a3b8);
      font-size: var(--scion-font-size-sm, 0.875rem);
    }

    .detail-dock {
      position: absolute;
      right: var(--scion-space-3, 0.75rem);
      top: var(--scion-space-3, 0.75rem);
      width: 330px;
      max-height: calc(100% - 1.5rem);
      overflow: auto;
      z-index: 5;
    }
    .legend-dock {
      position: absolute;
      left: var(--scion-space-3, 0.75rem);
      bottom: var(--scion-space-3, 0.75rem);
      z-index: 5;
      max-width: 340px;
    }
  `;

  override connectedCallback(): void {
    super.connectedCallback();
    window.addEventListener('keydown', this.boundKeydown);
    void this.load();
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    window.removeEventListener('keydown', this.boundKeydown);
    if (this.rafHandle) cancelAnimationFrame(this.rafHandle);
    this.rafHandle = 0;
  }

  private async load(): Promise<void> {
    this.loading = true;
    this.loadError = null;
    try {
      const res = await fetch('/api/digest');
      if (!res.ok) throw new Error(`HTTP ${res.status} loading digest`);
      const digest = (await res.json()) as Digest;
      if (digest.version !== SCHEMA_VERSION) {
        throw new Error(
          `Digest schema version ${digest.version} is not supported by this build (expected ${SCHEMA_VERSION}).`
        );
      }
      this.adopt(digest);
    } catch (err) {
      this.loadError = err instanceof Error ? err.message : String(err);
    } finally {
      this.loading = false;
    }
  }

  private adopt(digest: Digest): void {
    this.digest = digest;
    this.intervalById = new Map(digest.intervals.map((i) => [i.id, i]));
    this.edgeById = new Map(digest.edges.map((e) => [e.id, e]));
    this.lifelineById = new Map(digest.lifelines.map((l) => [l.id, l]));

    this.warp = new WarpFn(digest.warp);
    this.clock = new PlaybackClock(this.warp, { rate: this.rate });
    this.tauMs = 0;
    this.wallMs = this.warp.wallAt(0);
    this.velocity = this.warp.velocityAt(0);

    // Start with the ancestry tree collapsed below depth 1: a hundred columns
    // at once is unreadable, and the tree is the affordance for drilling in.
    const collapsed = new Set<string>();
    for (const l of digest.lifelines) {
      if (l.depth >= 1 && digest.lifelines.some((c) => c.parentId === l.id)) {
        collapsed.add(l.id);
      }
    }
    this.collapsed = collapsed;

    this.layoutDirty = true;
    this.refreshLayout(true);
    this.startLoop();
  }

  private startLoop(): void {
    if (this.rafHandle) cancelAnimationFrame(this.rafHandle);
    this.lastFrameTs = 0;
    const step = (ts: number): void => {
      this.rafHandle = requestAnimationFrame(step);
      const clock = this.clock;
      if (!clock) return;
      const delta = this.lastFrameTs === 0 ? 0 : ts - this.lastFrameTs;
      this.lastFrameTs = ts;

      if (clock.playing && delta > 0) {
        clock.tick(delta);
        this.tauMs = clock.tauMs;
        this.wallMs = clock.wallMs;
        this.velocity = clock.velocity;
        if (!clock.playing) this.playing = false; // auto-paused at the end
      }
      this.maybeRefreshLayout(ts);
    };
    this.rafHandle = requestAnimationFrame(step);
  }

  /**
   * Recompute the column layout at most every {@link LAYOUT_REFRESH_MS}, and
   * only when something that affects it has actually changed.
   */
  private maybeRefreshLayout(ts: number): void {
    if (!this.autoFold && !this.layoutDirty) return;
    if (!this.layoutDirty && ts - this.lastLayoutAt < LAYOUT_REFRESH_MS) return;
    this.lastLayoutAt = ts;
    this.refreshLayout(false);
  }

  private refreshLayout(force: boolean): void {
    const digest = this.digest;
    if (!digest) return;

    const halfSpan = ((this.viewportEndMs - this.viewportStartMs) || 60_000) / 2;
    const centre = this.wallMs;
    const widened = halfSpan * ACTIVITY_WINDOW_FACTOR;
    const startMs = centre - widened;
    const endMs = centre + widened;

    let activeIds: ReadonlySet<string> = this.activeIds;
    if (this.autoFold) {
      activeIds = activeLifelineIds(digest.intervals, digest.edges, startMs, endMs);
    }

    // Skip the rebuild when the visible-actor set is unchanged, so the layout
    // object identity stays stable and Lit does not re-render the canvas props.
    if (!force && this.autoFold && sameSet(activeIds, this.activeIds) && this.layout) {
      this.layoutDirty = false;
      return;
    }
    this.activeIds = activeIds;

    const opts = {
      collapsed: this.collapsed,
      solo: this.solo,
      ...(this.autoFold ? { activeWindow: { startMs, endMs }, activeIds } : {}),
      gap: COLUMN_GAP,
      foldedWidth: FOLDED_WIDTH,
    };

    // Lay out once at the minimum width to learn how many columns survive
    // folding, then widen them to use the canvas. A run with seven agents
    // should not leave half the window empty; a run with forty should not try.
    const probe = computeColumns(digest.lifelines, { ...opts, columnWidth: MIN_COLUMN_WIDTH });
    this.layout = computeColumns(digest.lifelines, {
      ...opts,
      columnWidth: this.fittedColumnWidth(probe),
    });
    this.layoutDirty = false;
  }

  /**
   * Column width that fills the canvas without exceeding a readable maximum.
   *
   * Widening is purely cosmetic and therefore safe: unlike the vertical axis,
   * horizontal position carries no metric meaning, so nothing about the
   * geometry becomes less honest when a column gets wider.
   */
  private fittedColumnWidth(probe: ColumnLayout): number {
    const avail = this.canvasWidthPx;
    if (avail <= 0) return MIN_COLUMN_WIDTH;
    const full = probe.columns.filter((c) => !c.folded).length;
    if (full === 0) return MIN_COLUMN_WIDTH;
    const foldedPx = probe.columns.filter((c) => c.folded).length * (FOLDED_WIDTH + COLUMN_GAP);
    const usable = avail - foldedPx - COLUMN_GAP * (full + 1);
    const each = usable / full;
    return clamp(Math.floor(each), MIN_COLUMN_WIDTH, MAX_COLUMN_WIDTH);
  }

  private onKeydown(e: KeyboardEvent): void {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    const target = e.composedPath()[0];
    if (target instanceof HTMLElement) {
      const tag = target.tagName.toLowerCase();
      if (tag === 'input' || tag === 'textarea' || tag === 'select' || target.isContentEditable) {
        return;
      }
    }
    switch (e.key) {
      case ' ':
        e.preventDefault();
        this.togglePlay();
        break;
      case 'ArrowDown':
        e.preventDefault();
        this.seekTau(this.tauMs + 1000);
        break;
      case 'ArrowUp':
        e.preventDefault();
        this.seekTau(this.tauMs - 1000);
        break;
      case '+':
      case '=':
        e.preventDefault();
        this.zoom(1 / 1.4);
        break;
      case '-':
        e.preventDefault();
        this.zoom(1.4);
        break;
      case 'Escape':
        this.selection = null;
        break;
      default:
        break;
    }
  }

  private zoom(factor: number): void {
    this.msPerPx = clamp(this.msPerPx * factor, MIN_MS_PER_PX, MAX_MS_PER_PX);
  }

  private togglePlay(): void {
    const clock = this.clock;
    if (!clock) return;
    clock.toggle();
    this.playing = clock.playing;
    this.tauMs = clock.tauMs;
    this.wallMs = clock.wallMs;
  }

  private seekTau(tauMs: number): void {
    const clock = this.clock;
    if (!clock) return;
    clock.seekTau(tauMs);
    this.tauMs = clock.tauMs;
    this.wallMs = clock.wallMs;
    this.velocity = clock.velocity;
    this.layoutDirty = true;
  }

  private seekWall(wallMs: number): void {
    const clock = this.clock;
    if (!clock) return;
    clock.seekWall(wallMs);
    this.tauMs = clock.tauMs;
    this.wallMs = clock.wallMs;
    this.velocity = clock.velocity;
    this.layoutDirty = true;
  }

  private onRateChange(rate: number): void {
    this.rate = rate;
    if (this.clock) this.clock.rate = rate;
  }

  private onToggleCollapse(lifelineId: string): void {
    const next = new Set(this.collapsed);
    if (next.has(lifelineId)) next.delete(lifelineId);
    else next.add(lifelineId);
    this.collapsed = next;
    this.layoutDirty = true;
    this.refreshLayout(true);
  }

  private onSolo(lifelineId: string | null): void {
    this.solo = this.solo === lifelineId ? null : lifelineId;
    this.layoutDirty = true;
    this.refreshLayout(true);
  }

  private onCanvasSelect(hit: HitResult | null): void {
    if (!hit) {
      this.selection = null;
      return;
    }
    if (hit.type === 'interval') {
      const interval = this.intervalById.get(hit.interval.id);
      const lifeline = this.lifelineById.get(hit.interval.lifelineId);
      this.selection = interval && lifeline ? { kind: 'interval', interval, lifeline } : null;
      this.selectedLifelineId = hit.interval.lifelineId;
      return;
    }
    if (hit.type === 'edge') {
      const edge = this.edgeById.get(hit.edge.id);
      const from = this.lifelineById.get(hit.edge.fromId);
      const to = this.lifelineById.get(hit.edge.toId);
      this.selection = edge && from && to ? { kind: 'edge', edge, from, to } : null;
      return;
    }
    // A stub is connectivity information: jump to the peer rather than inspect.
    const edge = this.edgeById.get(hit.stub.edgeId);
    if (edge) this.seekWall(hit.stub.direction === 'outgoing' ? edge.recvMs : edge.sendMs);
  }

  private onViewport(startMs: number, endMs: number, widthPx: number): void {
    this.viewportStartMs = startMs;
    this.viewportEndMs = endMs;
    if (widthPx > 0 && widthPx !== this.canvasWidthPx) {
      this.canvasWidthPx = widthPx;
      this.layoutDirty = true;
    }
  }

  private markers(): SeqMinimapMarker[] {
    const digest = this.digest;
    if (!digest) return [];
    const out: SeqMinimapMarker[] = [];
    for (const iv of digest.intervals) {
      if (iv.error) out.push({ wallMs: iv.startMs, kind: 'error', label: iv.label ?? 'error' });
    }
    return out;
  }

  private renderHeader(): unknown {
    const d = this.digest;
    const ratio = d ? d.stats.compressionRatio : 1;
    return html`
      <header>
        <span class="title">${d ? d.projectName || d.projectId || 'Sequence' : 'Sequence'}</span>
        <span class="sub">
          ${d
            ? `${d.stats.lifelineCount} lifelines · ${d.stats.maxConcurrent} concurrent · ${d.stats.intervalCount} intervals · ${d.stats.edgeCount} edges`
            : ''}
        </span>
        <span class="spacer"></span>
        <label class="ctl">
          <input
            type="checkbox"
            .checked=${this.autoFold}
            @change=${(e: Event): void => {
              this.autoFold = (e.target as HTMLInputElement).checked;
              this.layoutDirty = true;
              this.refreshLayout(true);
            }}
          />
          auto-fold idle
        </label>
        <span class="ctl zoom">
          <button title="Zoom out (-)" @click=${(): void => this.zoom(1.4)}>−</button>
          <span class="sub">${formatScale(this.msPerPx)}</span>
          <button title="Zoom in (+)" @click=${(): void => this.zoom(1 / 1.4)}>+</button>
        </span>
        <span class="ctl sub" title="Playback speedup vs real time at 1x">
          ${ratio > 0 ? `${ratio.toFixed(1)}× compressed` : ''}
        </span>
      </header>
    `;
  }

  override render(): unknown {
    if (this.loading) {
      return html`${this.renderHeader()}
        <div class="overlay"><sl-spinner></sl-spinner> Loading digest…</div>`;
    }
    if (this.loadError) {
      return html`${this.renderHeader()}
        <div class="overlay">
          <sl-alert variant="danger" open>
            <sl-icon slot="icon" name="exclamation-triangle"></sl-icon>
            ${this.loadError}
          </sl-alert>
        </div>`;
    }
    const d = this.digest;
    if (!d) return this.renderHeader();

    return html`
      ${this.renderHeader()}

      <aside>
        <scion-seq-lifeline-tree
          .lifelines=${d.lifelines}
          .collapsed=${this.collapsed}
          .solo=${this.solo}
          .activeIds=${this.activeIds}
          .selectedId=${this.selectedLifelineId}
          @seq-toggle-collapse=${(e: CustomEvent<{ lifelineId: string }>): void =>
            this.onToggleCollapse(e.detail.lifelineId)}
          @seq-solo=${(e: CustomEvent<{ lifelineId: string | null }>): void =>
            this.onSolo(e.detail.lifelineId)}
          @seq-select-lifeline=${(e: CustomEvent<{ lifelineId: string }>): void => {
            this.selectedLifelineId = e.detail.lifelineId;
          }}
        ></scion-seq-lifeline-tree>
      </aside>

      <main>
        <scion-seq-canvas
          .digest=${d}
          .layout=${this.layout}
          .wallMs=${this.wallMs}
          .velocity=${this.velocity}
          .msPerPx=${this.msPerPx}
          @seq-select=${(e: CustomEvent<{ hit: HitResult | null }>): void =>
            this.onCanvasSelect(e.detail.hit)}
          @seq-viewport=${(
            e: CustomEvent<{ startMs: number; endMs: number; widthPx: number }>
          ): void => this.onViewport(e.detail.startMs, e.detail.endMs, e.detail.widthPx)}
        ></scion-seq-canvas>

        ${this.selection
          ? html`<div class="detail-dock">
              <scion-seq-detail-panel
                .selection=${this.selection}
                .projectId=${d.projectId}
                .startedAt=${d.startedAt}
                @seq-close-detail=${(): void => {
                  this.selection = null;
                }}
              ></scion-seq-detail-panel>
            </div>`
          : nothing}

        <div class="legend-dock">
          <scion-seq-legend
            ?open=${this.legendOpen}
            .stats=${d.stats}
            @seq-legend-toggle=${(e: CustomEvent<{ open: boolean }>): void => {
              this.legendOpen = e.detail.open;
            }}
          ></scion-seq-legend>
        </div>
      </main>

      <div class="rail">
        <div class="rail-head">
          ${this.railCollapsed ? nothing : html`<span>run overview</span>`}
          <span class="spacer"></span>
          <button
            title=${this.railCollapsed ? 'Show run overview' : 'Hide run overview'}
            aria-label=${this.railCollapsed ? 'Show run overview' : 'Hide run overview'}
            aria-expanded=${String(!this.railCollapsed)}
            @click=${(): void => {
              this.railCollapsed = !this.railCollapsed;
            }}
          >
            ${this.railCollapsed ? '‹' : '›'}
          </button>
        </div>
        <scion-seq-minimap
          .density=${d.density}
          .lifelines=${d.lifelines}
          .intervals=${d.intervals}
          .durationMs=${d.durationMs}
          .wallMs=${this.wallMs}
          .viewportStartMs=${this.viewportStartMs}
          .viewportEndMs=${this.viewportEndMs}
          .markers=${this.markers()}
          .highlightLifelineId=${this.selectedLifelineId}
          @seq-seek-wall=${(e: CustomEvent<{ wallMs: number }>): void =>
            this.seekWall(e.detail.wallMs)}
        ></scion-seq-minimap>
      </div>

      <footer>
        <scion-seq-transport
          .playing=${this.playing}
          .tauMs=${this.tauMs}
          .totalTauMs=${d.warp.totalTauMs}
          .wallMs=${this.wallMs}
          .durationMs=${d.durationMs}
          .startedAt=${d.startedAt}
          .rate=${this.rate}
          .velocity=${this.velocity}
          .maxVelocity=${d.warp.maxVelocity}
          @seq-play-toggle=${(): void => this.togglePlay()}
          @seq-seek=${(e: CustomEvent<{ tauMs: number }>): void => this.seekTau(e.detail.tauMs)}
          @seq-rate-change=${(e: CustomEvent<{ rate: number }>): void =>
            this.onRateChange(e.detail.rate)}
        ></scion-seq-transport>
      </footer>
    `;
  }
}

function clamp(v: number, lo: number, hi: number): number {
  return v < lo ? lo : v > hi ? hi : v;
}

function sameSet(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
  if (a.size !== b.size) return false;
  for (const v of a) if (!b.has(v)) return false;
  return true;
}

/** Human-readable vertical scale, e.g. "1.2s/100px". */
function formatScale(msPerPx: number): string {
  const per100 = msPerPx * 100;
  if (per100 < 1000) return `${Math.round(per100)}ms/100px`;
  if (per100 < 60_000) return `${(per100 / 1000).toFixed(1)}s/100px`;
  return `${(per100 / 60_000).toFixed(1)}m/100px`;
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-seq-viz': ScionSeqViz;
  }
}
