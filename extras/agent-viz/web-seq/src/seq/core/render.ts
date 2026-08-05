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
 * Canvas painter for a {@link FrameModel}.
 *
 * Canvas rather than SVG or DOM because 50-100 columns times thousands of
 * intervals must hold 60fps; a retained-mode tree of that size cannot. The
 * painter is deliberately dumb: every coordinate it draws was resolved by
 * {@link buildFrame}, so this file contains no layout arithmetic and no policy
 * beyond styling.
 *
 * Confidence is drawn differently rather than annotated, because a viewer reads
 * shape long before they read a legend:
 *  - `measured` - solid fill; the duration is real.
 *  - `inferred` - hatched and translucent; an upper bound, not a measurement.
 *  - `open` - fades out at the unterminated end with no end cap; we do not know
 *    when it stopped and refuse to draw a boundary that would claim we do.
 *
 * The only DOM type referenced anywhere in `core/` is
 * `CanvasRenderingContext2D`, and only here.
 */

import type {
  FrameEdge,
  FrameEdgeStub,
  FrameInterval,
  FrameLifelineRule,
  FrameModel,
} from './frame.js';

/** Every colour, font and alpha the painter uses. Nothing is hardcoded below. */
export interface RenderTheme {
  /** Canvas clear colour. */
  background: string;
  /** Wash laid over the wake band (top). */
  wakeWash: string;
  /** Wash laid over the staging band (bottom) as streaks take over. */
  stagingWash: string;
  /** Hairlines at the frame boundaries. */
  frameEdge: string;
  /** The "now" line at the bottom of the frame. */
  playhead: string;

  /** Column rule before birth / after death. */
  columnRule: string;
  /** Column rule while the lifeline is alive. */
  columnRuleAlive: string;
  /** Column rule for auto-folded (idle) columns. */
  columnRuleFolded: string;
  /** Birth and death caps on a column rule. */
  columnCap: string;
  /** Column name text. */
  columnLabel: string;
  /** Column name text for folded/idle columns. */
  columnLabelMuted: string;

  /** Border around an interval bar. */
  intervalStroke: string;
  /** Border around an interval whose source event reported a failure. */
  intervalErrorStroke: string;
  /** Diagonal hatching drawn over `inferred` intervals. */
  intervalHatch: string;
  /** Text drawn inside a saturated interval bar. */
  intervalLabel: string;
  /**
   * Text drawn inside a faded (outer) interval bar, where the dark ink used on
   * a saturated fill would vanish into the background.
   */
  intervalLabelFaint: string;

  edgeMessage: string;
  edgeSpawn: string;
  edgeDestroy: string;
  /** Edge whose arrival time is unknown; drawn dashed and horizontal. */
  edgeOpen: string;

  /** Highlight laid over the edge under the pointer. */
  edgeHover: string;
  /** Fill of the message bubble affordance. */
  bubbleFill: string;
  /** Outline of the message bubble affordance. */
  bubbleStroke: string;
  /** The speech-lines glyph inside the bubble. */
  bubbleGlyph: string;

  /** Stub segment and arrowhead. */
  stub: string;
  /** Stub peer-name text. */
  stubLabel: string;

  /** Motion streaks in the staging band. */
  streak: string;

  labelFont: string;
  smallFont: string;

  /** Fill alpha for a `measured` interval. */
  alphaMeasured: number;
  /** Fill alpha for an `inferred` interval. */
  alphaInferred: number;
  /** Fill alpha at the terminated end of an `open` interval. */
  alphaOpen: number;

  /**
   * Per-nesting-level fill alpha multiplier, indexed by inset level; the last
   * entry applies to anything deeper.
   *
   * A lifeline's outermost bar spans its whole life, so at full saturation it
   * paints the column solid and swallows every tool call nested inside it --
   * exactly the information the viewer came for. Fading the containers turns
   * them into an envelope and lets the innermost, most specific work read as
   * the foreground. This is the reverse of the usual flame-graph convention,
   * and deliberately so: here the enclosing frame is context, not content.
   */
  alphaByInset: readonly number[];
}

/**
 * A neutral dark theme.
 *
 * Real callers should build a theme from CSS custom properties so the
 * visualization tracks the app's palette; this exists so the module is usable
 * (and testable) standalone.
 */
export const DEFAULT_THEME: RenderTheme = {
  background: '#0d1117',
  wakeWash: 'rgba(13, 17, 23, 0.55)',
  stagingWash: 'rgba(13, 17, 23, 0.45)',
  frameEdge: 'rgba(255, 255, 255, 0.08)',
  playhead: 'rgba(255, 255, 255, 0.35)',

  columnRule: 'rgba(255, 255, 255, 0.08)',
  columnRuleAlive: 'rgba(255, 255, 255, 0.20)',
  columnRuleFolded: 'rgba(255, 255, 255, 0.10)',
  columnCap: 'rgba(255, 255, 255, 0.45)',
  columnLabel: 'rgba(230, 237, 243, 0.92)',
  columnLabelMuted: 'rgba(230, 237, 243, 0.45)',

  intervalStroke: 'rgba(0, 0, 0, 0.45)',
  intervalErrorStroke: '#f85149',
  intervalHatch: 'rgba(255, 255, 255, 0.35)',
  intervalLabel: 'rgba(13, 17, 23, 0.92)',
  intervalLabelFaint: 'rgba(230, 237, 243, 0.85)',

  edgeMessage: 'rgba(230, 237, 243, 0.75)',
  edgeSpawn: 'rgba(126, 231, 135, 0.75)',
  edgeDestroy: 'rgba(248, 81, 73, 0.75)',
  edgeOpen: 'rgba(230, 237, 243, 0.40)',

  edgeHover: 'rgba(255, 212, 121, 0.95)',
  bubbleFill: '#f0b429',
  bubbleStroke: 'rgba(13, 17, 23, 0.85)',
  bubbleGlyph: 'rgba(13, 17, 23, 0.9)',

  stub: 'rgba(230, 237, 243, 0.55)',
  stubLabel: 'rgba(230, 237, 243, 0.70)',

  streak: 'rgba(230, 237, 243, 0.22)',

  labelFont: '11px system-ui, sans-serif',
  smallFont: '9px system-ui, sans-serif',

  alphaMeasured: 0.95,
  alphaInferred: 0.4,
  alphaOpen: 0.85,
  // Deliberately low at the top: lifecycle, session and turn bars all span
  // most of a lifeline and are painted one over another, so their alphas
  // compound. Values that look reasonable in isolation add up to an opaque
  // column that hides every tool call inside it.
  alphaByInset: [0.1, 0.14, 0.26, 1],
};

/** What sits under a point, for click-to-inspect. */
export type HitResult =
  | { type: 'interval'; interval: FrameInterval }
  | { type: 'edge'; edge: FrameEdge }
  /** The message bubble affordance at an edge's midpoint. */
  | { type: 'edge-bubble'; edge: FrameEdge }
  | { type: 'stub'; stub: FrameEdgeStub };

/**
 * Transient pointer state.
 *
 * Deliberately not part of {@link FrameModel}: the frame is a pure projection
 * of the digest at an instant, and mixing in where the mouse happens to be
 * would mean rebuilding it on every mouse move.
 */
export interface FrameInteraction {
  /** Edge currently under the pointer, which grows a bubble at its midpoint. */
  hoverEdgeId?: string | null;
}

/** Click tolerance around a one-dimensional edge, in px. */
const EDGE_HIT_TOLERANCE = 5;
/** Radius of the message bubble affordance, px. */
export const EDGE_BUBBLE_RADIUS = 9;
/**
 * Shortest edge that gets a bubble, px.
 *
 * A bubble on a segment barely longer than itself covers the thing it is
 * annotating, and its hit area swallows the neighbouring edges.
 */
const MIN_BUBBLE_EDGE_LENGTH = 34;
/** Below this bar height a label cannot be legible, so it is skipped. */
const MIN_LABEL_HEIGHT = 11;
/** Below this bar width a label cannot be legible either. */
const MIN_LABEL_WIDTH = 26;
/** Streak spacing in the staging band, px. */
const STREAK_SPACING = 22;

export function renderFrame(
  ctx: CanvasRenderingContext2D,
  model: FrameModel,
  theme: RenderTheme,
  interaction?: FrameInteraction,
): void {
  const g = model.geom;

  ctx.save();
  ctx.globalAlpha = 1;
  ctx.fillStyle = theme.background;
  ctx.fillRect(0, 0, g.width, g.height);

  drawZones(ctx, model, theme);
  for (const rule of model.lifelines) drawColumnRule(ctx, rule, g.height, theme);
  for (const iv of model.intervals) drawInterval(ctx, iv, theme);
  for (const e of model.edges) drawEdge(ctx, e, theme);
  for (const s of model.stubs) drawStub(ctx, s, theme);
  drawStaging(ctx, model, theme);
  drawLabels(ctx, model, theme);
  drawHoverBubble(ctx, model, theme, interaction);

  ctx.restore();
}

/* -------------------------------------------------------------------------- */
/* Zones                                                                      */
/* -------------------------------------------------------------------------- */

function drawZones(
  ctx: CanvasRenderingContext2D,
  model: FrameModel,
  theme: RenderTheme,
): void {
  const g = model.geom;
  ctx.save();
  ctx.globalAlpha = 1;

  ctx.strokeStyle = theme.frameEdge;
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(0, Math.round(g.frameTopPx) + 0.5);
  ctx.lineTo(g.width, Math.round(g.frameTopPx) + 0.5);
  ctx.stroke();

  ctx.strokeStyle = theme.playhead;
  ctx.beginPath();
  ctx.moveTo(0, Math.round(model.playheadY) + 0.5);
  ctx.lineTo(g.width, Math.round(model.playheadY) + 0.5);
  ctx.stroke();

  ctx.restore();
}

/**
 * The staging cross-fade.
 *
 * As `streakFactor` rises the band is progressively washed out and replaced by
 * upward motion streaks: the picture stops inviting the viewer to read it and
 * starts telling them things are moving fast.
 */
function drawStaging(
  ctx: CanvasRenderingContext2D,
  model: FrameModel,
  theme: RenderTheme,
): void {
  const f = model.streakFactor;
  if (f <= 0) return;
  const g = model.geom;
  const top = g.frameBottomPx;
  const h = g.height - top;
  if (h <= 0) return;

  ctx.save();
  ctx.beginPath();
  ctx.rect(0, top, g.width, h);
  ctx.clip();

  ctx.globalAlpha = f * 0.85;
  ctx.fillStyle = theme.stagingWash;
  ctx.fillRect(0, top, g.width, h);

  ctx.globalAlpha = f;
  ctx.strokeStyle = theme.streak;
  ctx.lineWidth = 1;
  ctx.beginPath();
  for (const rule of model.lifelines) {
    if (!rule.visible) continue;
    const x = Math.round(rule.x) + 0.5;
    for (let y = top; y < g.height; y += STREAK_SPACING) {
      const len = Math.min(STREAK_SPACING * 0.7 * (0.4 + f), g.height - y);
      ctx.moveTo(x, y);
      ctx.lineTo(x, y + len);
    }
  }
  ctx.stroke();

  ctx.restore();

  // The wake gets the mirror treatment: a soft wash so departing geometry does
  // not compete with the readable frame.
  ctx.save();
  ctx.globalAlpha = 1;
  ctx.fillStyle = theme.wakeWash;
  ctx.fillRect(0, 0, g.width, Math.max(0, g.frameTopPx));
  ctx.restore();
}

/* -------------------------------------------------------------------------- */
/* Columns                                                                    */
/* -------------------------------------------------------------------------- */

function drawColumnRule(
  ctx: CanvasRenderingContext2D,
  rule: FrameLifelineRule,
  height: number,
  theme: RenderTheme,
): void {
  const x = Math.round(rule.x) + 0.5;
  const yTop = Math.max(0, Math.min(height, rule.yBirth));
  const yBottom = Math.max(0, Math.min(height, rule.yDeath));

  ctx.save();
  ctx.lineWidth = 1;

  // Faint full-height rule: the column exists even where the lifeline does not.
  ctx.strokeStyle = theme.columnRule;
  ctx.globalAlpha = rule.folded ? 0.5 : 1;
  ctx.beginPath();
  ctx.moveTo(x, 0);
  ctx.lineTo(x, height);
  ctx.stroke();

  if (yBottom > yTop) {
    ctx.strokeStyle = rule.folded ? theme.columnRuleFolded : theme.columnRuleAlive;
    ctx.globalAlpha = 1;
    ctx.lineWidth = rule.folded ? 1 : 2;
    ctx.beginPath();
    ctx.moveTo(x, yTop);
    ctx.lineTo(x, yBottom);
    ctx.stroke();
  }

  ctx.lineWidth = 1;
  ctx.strokeStyle = theme.columnCap;
  const cap = Math.max(2, Math.min(6, rule.width / 4));
  if (rule.birthOnScreen) {
    ctx.beginPath();
    ctx.moveTo(x - cap, Math.round(rule.yBirth) + 0.5);
    ctx.lineTo(x + cap, Math.round(rule.yBirth) + 0.5);
    ctx.stroke();
  }
  if (rule.deathOnScreen) {
    // A cross, matching the UML lifeline termination glyph.
    const y = Math.round(rule.yDeath) + 0.5;
    ctx.beginPath();
    ctx.moveTo(x - cap, y - cap);
    ctx.lineTo(x + cap, y + cap);
    ctx.moveTo(x + cap, y - cap);
    ctx.lineTo(x - cap, y + cap);
    ctx.stroke();
  }

  ctx.restore();
}

/* -------------------------------------------------------------------------- */
/* Intervals                                                                  */
/* -------------------------------------------------------------------------- */

/** Fill-alpha multiplier for a nesting level; deeper than the table saturates. */
export function insetAlpha(theme: RenderTheme, insetLevel: number): number {
  const table = theme.alphaByInset;
  if (!table || table.length === 0) return 1;
  const i = Math.max(0, Math.min(table.length - 1, Math.floor(insetLevel)));
  return table[i] ?? 1;
}

function drawInterval(
  ctx: CanvasRenderingContext2D,
  iv: FrameInterval,
  theme: RenderTheme,
): void {
  if (iv.opacity <= 0.01) return;
  const w = Math.max(1, iv.width);
  const h = Math.max(1, iv.height);

  const nest = insetAlpha(theme, iv.insetLevel);

  ctx.save();
  ctx.globalAlpha = iv.opacity;

  if (iv.confidence === 'open') {
    // Fade out at the unterminated (later, lower) end and draw no end cap.
    const grad = ctx.createLinearGradient(0, iv.y, 0, iv.y + h);
    grad.addColorStop(0, iv.colorKey);
    grad.addColorStop(1, 'transparent');
    ctx.globalAlpha = iv.opacity * theme.alphaOpen * nest;
    ctx.fillStyle = grad;
    ctx.fillRect(iv.x, iv.y, w, h);

    ctx.globalAlpha = iv.opacity;
    ctx.strokeStyle = iv.error ? theme.intervalErrorStroke : theme.intervalStroke;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(iv.x + 0.5, iv.y + h);
    ctx.lineTo(iv.x + 0.5, iv.y + 0.5);
    ctx.lineTo(iv.x + w - 0.5, iv.y + 0.5);
    ctx.lineTo(iv.x + w - 0.5, iv.y + h);
    ctx.stroke();
    ctx.restore();
    return;
  }

  ctx.globalAlpha =
    iv.opacity *
    nest *
    (iv.confidence === 'inferred' ? theme.alphaInferred : theme.alphaMeasured);
  ctx.fillStyle = iv.colorKey;
  ctx.fillRect(iv.x, iv.y, w, h);

  if (iv.confidence === 'inferred') {
    // Hatching, stroked by hand rather than via createPattern: a pattern needs
    // an image source, and core/ must not create DOM nodes.
    ctx.save();
    ctx.beginPath();
    ctx.rect(iv.x, iv.y, w, h);
    ctx.clip();
    ctx.globalAlpha = iv.opacity * nest * 0.7;
    ctx.strokeStyle = theme.intervalHatch;
    ctx.lineWidth = 1;
    ctx.beginPath();
    const step = 6;
    const y0 = Math.floor(iv.y);
    const y1 = Math.ceil(iv.y + h);
    for (let y = y0 - w; y < y1 + w; y += step) {
      ctx.moveTo(iv.x, y);
      ctx.lineTo(iv.x + w, y - w);
    }
    ctx.stroke();
    ctx.restore();
  }

  ctx.globalAlpha = iv.opacity;
  ctx.strokeStyle = iv.error ? theme.intervalErrorStroke : theme.intervalStroke;
  ctx.lineWidth = iv.error ? 1.5 : 1;
  ctx.strokeRect(iv.x + 0.5, iv.y + 0.5, Math.max(1, w - 1), Math.max(1, h - 1));

  ctx.restore();
}

/* -------------------------------------------------------------------------- */
/* Edges                                                                      */
/* -------------------------------------------------------------------------- */

function edgeColor(kind: FrameEdge['kind'], theme: RenderTheme): string {
  switch (kind) {
    case 'spawn':
      return theme.edgeSpawn;
    case 'destroy':
      return theme.edgeDestroy;
    default:
      return theme.edgeMessage;
  }
}

function setDash(ctx: CanvasRenderingContext2D, dash: number[]): void {
  if (typeof ctx.setLineDash === 'function') ctx.setLineDash(dash);
}

function drawEdge(
  ctx: CanvasRenderingContext2D,
  e: FrameEdge,
  theme: RenderTheme,
): void {
  if (e.opacity <= 0.01) return;
  ctx.save();
  ctx.globalAlpha = e.opacity;
  ctx.lineWidth = e.broadcast ? 2 : 1.25;

  if (e.recvConfidence === 'open') {
    // Arrival time unknown: dashed, and horizontal because `recvMs === sendMs`.
    ctx.strokeStyle = theme.edgeOpen;
    setDash(ctx, [4, 3]);
  } else {
    ctx.strokeStyle = edgeColor(e.kind, theme);
    setDash(ctx, e.recvConfidence === 'inferred' ? [7, 3] : []);
  }

  ctx.beginPath();
  ctx.moveTo(e.x1, e.y1);
  ctx.lineTo(e.x2, e.y2);
  ctx.stroke();

  setDash(ctx, []);
  drawArrowhead(ctx, e.x1, e.y1, e.x2, e.y2, ctx.strokeStyle);
  ctx.restore();
}

function drawArrowhead(
  ctx: CanvasRenderingContext2D,
  fromX: number,
  fromY: number,
  toX: number,
  toY: number,
  color: string | CanvasGradient | CanvasPattern,
): void {
  const dx = toX - fromX;
  const dy = toY - fromY;
  const len = Math.hypot(dx, dy);
  if (len < 0.001) return;
  const ux = dx / len;
  const uy = dy / len;
  const size = 6;
  const bx = toX - ux * size;
  const by = toY - uy * size;
  const px = -uy * size * 0.45;
  const py = ux * size * 0.45;

  ctx.beginPath();
  ctx.moveTo(toX, toY);
  ctx.lineTo(bx + px, by + py);
  ctx.lineTo(bx - px, by - py);
  ctx.closePath();
  ctx.fillStyle = color;
  ctx.fill();
}

/**
 * Highlights the hovered edge and draws its bubble affordance.
 *
 * Drawn last, over labels: the bubble is the click target, so nothing may
 * overlap it, and the whole point of the highlight is to pick one arrow out of
 * a thicket of them.
 */
function drawHoverBubble(
  ctx: CanvasRenderingContext2D,
  model: FrameModel,
  theme: RenderTheme,
  interaction?: FrameInteraction,
): void {
  const id = interaction?.hoverEdgeId;
  if (!id) return;
  const e = model.edges.find((c) => c.id === id);
  if (!e) return;
  const at = edgeBubbleAt(e);
  if (!at) return;

  ctx.save();
  // Full opacity even for a faded edge: the pointer is on it, so it has been
  // singled out, and a ghostly highlight would read as a rendering glitch.
  ctx.globalAlpha = 1;
  ctx.strokeStyle = theme.edgeHover;
  ctx.lineWidth = e.broadcast ? 2.5 : 1.75;
  setDash(ctx, []);
  ctx.beginPath();
  ctx.moveTo(e.x1, e.y1);
  ctx.lineTo(e.x2, e.y2);
  ctx.stroke();
  drawArrowhead(ctx, e.x1, e.y1, e.x2, e.y2, theme.edgeHover);

  drawBubbleGlyph(ctx, at.x, at.y, EDGE_BUBBLE_RADIUS, theme);
  ctx.restore();
}

/** Paints a rounded speech bubble with a tail and three text lines. */
function drawBubbleGlyph(
  ctx: CanvasRenderingContext2D,
  cx: number,
  cy: number,
  r: number,
  theme: RenderTheme,
): void {
  const w = r * 2;
  const h = r * 1.55;
  const x = cx - w / 2;
  const y = cy - h / 2 - r * 0.2;
  const rad = Math.min(3.5, h / 2);

  ctx.beginPath();
  // roundRect is not in happy-dom's canvas stub, and older Safari lacks it.
  if (typeof ctx.roundRect === 'function') {
    ctx.roundRect(x, y, w, h, rad);
  } else {
    ctx.rect(x, y, w, h);
  }
  // The tail, pointing down-left, is what makes a rounded rectangle read as
  // speech at 18px rather than as a button.
  ctx.moveTo(x + w * 0.28, y + h);
  ctx.lineTo(x + w * 0.24, y + h + r * 0.62);
  ctx.lineTo(x + w * 0.55, y + h);
  ctx.closePath();

  ctx.fillStyle = theme.bubbleFill;
  ctx.fill();
  ctx.lineWidth = 1;
  ctx.strokeStyle = theme.bubbleStroke;
  ctx.stroke();

  ctx.strokeStyle = theme.bubbleGlyph;
  ctx.lineWidth = 1;
  for (let i = 0; i < 2; i++) {
    const ly = Math.round(y + h * (0.36 + i * 0.32)) + 0.5;
    ctx.beginPath();
    ctx.moveTo(x + w * 0.22, ly);
    ctx.lineTo(x + w * (i === 0 ? 0.78 : 0.62), ly);
    ctx.stroke();
  }
}

/* -------------------------------------------------------------------------- */
/* Stubs                                                                      */
/* -------------------------------------------------------------------------- */

function drawStub(
  ctx: CanvasRenderingContext2D,
  s: FrameEdgeStub,
  theme: RenderTheme,
): void {
  if (s.opacity <= 0.01) return;
  ctx.save();
  ctx.globalAlpha = s.opacity;
  ctx.strokeStyle = theme.stub;
  ctx.lineWidth = 1.25;
  setDash(ctx, s.reason === 'offscreen' ? [] : [3, 3]);

  ctx.beginPath();
  ctx.moveTo(s.x, s.y);
  ctx.lineTo(s.tipX, s.tipY);
  ctx.stroke();
  setDash(ctx, []);

  if (s.direction === 'outgoing') {
    drawArrowhead(ctx, s.x, s.y, s.tipX, s.tipY, theme.stub);
  } else {
    drawArrowhead(ctx, s.tipX, s.tipY, s.x, s.y, theme.stub);
  }

  // A dot on the lifeline marks where the message actually attaches.
  ctx.fillStyle = s.colorKey;
  ctx.beginPath();
  ctx.arc(s.x, s.y, 2, 0, Math.PI * 2);
  ctx.fill();

  ctx.restore();
}

/* -------------------------------------------------------------------------- */
/* Labels                                                                     */
/* -------------------------------------------------------------------------- */

function drawLabels(
  ctx: CanvasRenderingContext2D,
  model: FrameModel,
  theme: RenderTheme,
): void {
  const g = model.geom;
  const quiet = model.streakFactor > 0.5;

  ctx.save();
  ctx.textBaseline = 'middle';

  // Column headers, pinned to the top of the canvas.
  ctx.font = theme.labelFont;
  for (const rule of model.lifelines) {
    ctx.globalAlpha = rule.visible ? 1 : 0.45;
    ctx.fillStyle = rule.folded ? theme.columnLabelMuted : theme.columnLabel;
    if (rule.folded || rule.width < 40) {
      ctx.save();
      ctx.translate(rule.x + 3, 6);
      ctx.rotate(Math.PI / 2);
      ctx.textAlign = 'left';
      ctx.fillText(ellipsize(ctx, rule.label, g.height * 0.25), 0, 0);
      ctx.restore();
    } else {
      ctx.textAlign = 'center';
      ctx.fillText(ellipsize(ctx, rule.label, rule.width - 4), rule.x, 10);
    }
  }

  // Interval labels, only where they can actually be read.
  ctx.font = theme.smallFont;
  ctx.textAlign = 'left';
  for (const iv of model.intervals) {
    if (iv.label === undefined) continue;
    // The lifecycle bar's label is the agent's name, which the column header
    // already shows permanently. Drawing it again would be harmless if the bar
    // were short, but it spans most of the run, so its label sticks to the top
    // edge and sits directly under the header -- every column captioned twice.
    if (iv.kind === 'lifecycle') continue;
    if (quiet && iv.zone === 'staging') continue;
    if (iv.height < MIN_LABEL_HEIGHT || iv.width < MIN_LABEL_WIDTH) continue;
    const y = Math.max(6, Math.min(g.height - 6, iv.y + 7));
    ctx.globalAlpha = iv.opacity;
    // Dark ink reads on a saturated bar and disappears on a faded one, so the
    // ink follows the fill rather than the other way round.
    const filled =
      insetAlpha(theme, iv.insetLevel) *
      (iv.confidence === 'inferred' ? theme.alphaInferred : theme.alphaMeasured);
    ctx.fillStyle = filled >= 0.55 ? theme.intervalLabel : theme.intervalLabelFaint;
    ctx.fillText(ellipsize(ctx, iv.label, iv.width - 6), iv.x + 3, y);
  }

  // Stub peer names: the whole point of a stub is saying who is on the far end.
  ctx.fillStyle = theme.stubLabel;
  for (const s of model.stubs) {
    if (quiet && s.zone === 'staging') continue;
    ctx.globalAlpha = s.opacity * 0.9;
    ctx.textAlign = s.side === 'left' ? 'left' : s.side === 'right' ? 'right' : 'left';
    const tx = s.side === 'right' ? s.tipX - 3 : s.tipX + 3;
    const ty = Math.max(6, Math.min(g.height - 6, s.tipY - 6));
    ctx.fillText(ellipsize(ctx, s.label, 90), tx, ty);
  }

  ctx.restore();
}

function ellipsize(
  ctx: CanvasRenderingContext2D,
  text: string,
  maxWidth: number,
): string {
  if (maxWidth <= 0) return '';
  if (typeof ctx.measureText !== 'function') return text;
  if (ctx.measureText(text).width <= maxWidth) return text;
  let out = text;
  while (out.length > 1 && ctx.measureText(`${out}…`).width > maxWidth) {
    out = out.slice(0, -1);
  }
  return `${out}…`;
}

/* -------------------------------------------------------------------------- */
/* Hit testing                                                                */
/* -------------------------------------------------------------------------- */

/**
 * Centre of the message bubble for an edge, or null where one cannot be drawn.
 *
 * The midpoint is the only stable anchor on a sloped segment: both endpoints
 * are already crowded by the lifeline column and the arrowhead, and a fixed
 * offset from either end drifts as the slope changes.
 */
export function edgeBubbleAt(e: FrameEdge): { x: number; y: number } | null {
  if (e.kind !== 'message') return null;
  if (e.opacity <= 0.05) return null;
  if (Math.hypot(e.x2 - e.x1, e.y2 - e.y1) < MIN_BUBBLE_EDGE_LENGTH) return null;
  return { x: (e.x1 + e.x2) / 2, y: (e.y1 + e.y2) / 2 };
}

/**
 * Topmost thing under `(x, y)`.
 *
 * Resolution follows paint order, because what the viewer is pointing at is
 * whatever they can see: the hover bubble first, then edges and stubs, then
 * intervals. Arrows are drawn over the bars they cross, and nearly every arrow
 * crosses one; searching intervals first made those arrows unreachable, which
 * is most of them. The cost is a ~5px band along each arrow where a bar cannot
 * be clicked, and a bar is a far larger target than a hairline.
 *
 * Intervals are searched last-drawn first, which is deepest first, so clicking
 * a nested tool span selects the tool and not the session that encloses it.
 */
export function hitTest(
  model: FrameModel,
  x: number,
  y: number,
  interaction?: FrameInteraction,
): HitResult | null {
  // The bubble is only present while its edge is hovered, and while present it
  // outranks everything: it is a small deliberate target drawn on top.
  const hoverId = interaction?.hoverEdgeId;
  if (hoverId) {
    const e = model.edges.find((c) => c.id === hoverId);
    const b = e ? edgeBubbleAt(e) : null;
    if (e && b && Math.hypot(x - b.x, y - b.y) <= EDGE_BUBBLE_RADIUS + 2) {
      return { type: 'edge-bubble', edge: e };
    }
  }
  return hitTestStatic(model, x, y);
}

function hitTestStatic(model: FrameModel, x: number, y: number): HitResult | null {
  let best: HitResult | null = null;
  let bestDist = EDGE_HIT_TOLERANCE;
  for (let i = model.edges.length - 1; i >= 0; i--) {
    const e = model.edges[i];
    if (e.opacity <= 0.01) continue;
    const d = distanceToSegment(x, y, e.x1, e.y1, e.x2, e.y2);
    if (d <= bestDist) {
      bestDist = d;
      best = { type: 'edge', edge: e };
    }
  }
  for (let i = model.stubs.length - 1; i >= 0; i--) {
    const s = model.stubs[i];
    if (s.opacity <= 0.01) continue;
    const d = distanceToSegment(x, y, s.x, s.y, s.tipX, s.tipY);
    if (d <= bestDist) {
      bestDist = d;
      best = { type: 'stub', stub: s };
    }
  }
  if (best) return best;

  for (let i = model.intervals.length - 1; i >= 0; i--) {
    const iv = model.intervals[i];
    if (iv.opacity <= 0.01) continue;
    if (
      x >= iv.x &&
      x <= iv.x + Math.max(1, iv.width) &&
      y >= iv.y &&
      y <= iv.y + Math.max(1, iv.height)
    ) {
      return { type: 'interval', interval: iv };
    }
  }
  return null;
}

function distanceToSegment(
  px: number,
  py: number,
  ax: number,
  ay: number,
  bx: number,
  by: number,
): number {
  const dx = bx - ax;
  const dy = by - ay;
  const lenSq = dx * dx + dy * dy;
  if (lenSq === 0) return Math.hypot(px - ax, py - ay);
  let t = ((px - ax) * dx + (py - ay) * dy) / lenSq;
  t = t < 0 ? 0 : t > 1 ? 1 : t;
  return Math.hypot(px - (ax + dx * t), py - (ay + dy * t));
}
