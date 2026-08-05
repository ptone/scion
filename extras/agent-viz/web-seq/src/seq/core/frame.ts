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
 * Per-frame view model: everything the renderer needs, with no further
 * computation left to do at draw time.
 *
 * ## Vertical axis
 *
 * Time flows *upward*: the past is at the top, the playhead sits at the bottom
 * of the readable frame, and the future extends below it. A fixed
 * {@link FrameGeometry.msPerPx} governs the whole canvas, in every zone, which
 * is what makes the geometry honest - an interval that lasted twice as long is
 * drawn exactly twice as tall, wherever it currently is on screen.
 *
 * ## Three zones
 *
 *  - **WAKE** (`y < frameTop`): already departed, fading out. Kept on screen so
 *    the eye can follow something it was tracking a moment ago.
 *  - **FRAME** (`frameTop..frameBottom`): the readable viewport.
 *  - **STAGING** (`y > frameBottom`): the lookahead band. Nothing here is meant
 *    to be *read*; as the warp velocity rises, {@link FrameModel.streakFactor}
 *    tells the renderer to cross-fade this band from discrete geometry into
 *    motion streaks.
 *
 * ## Honest cropping
 *
 * A sliding window necessarily cuts edges in half. Simply not drawing them
 * would make the picture lie about connectivity, so every edge with exactly one
 * usable endpoint becomes an {@link FrameEdgeStub}: anchored on the visible
 * lifeline, aimed at the screen boundary the peer lies beyond, and labelled
 * with who that peer is. Three things can make an endpoint unusable: it is off
 * the top/bottom of the canvas (`offscreen`), its lifeline is not rendered at
 * all (`hidden`, e.g. solo mode), or its column is so far away horizontally
 * that a full line would sweep across everything between (`distant`).
 */

import type { Column, ColumnLayout } from './columns.js';
import type {
  Confidence,
  Digest,
  Edge,
  EdgeKind,
  Interval,
  IntervalKind,
  Lifeline,
} from './types.js';

/** Which of the three vertical bands a thing occupies. */
export type Zone = 'wake' | 'frame' | 'staging';

/** Below this velocity the staging zone is drawn as ordinary geometry. */
export const STREAK_MIN_VELOCITY = 2;
/** At or above this velocity the staging zone is pure motion streaks. */
export const STREAK_FULL_VELOCITY = 64;

/** Horizontal inset applied per nesting level, in px. */
const DEFAULT_INTERVAL_INSET = 3;
/** Never draw an interval narrower than this. */
const DEFAULT_MIN_INTERVAL_WIDTH = 2;
/** Never draw an interval shorter than this, so instantaneous work stays visible. */
const DEFAULT_MIN_INTERVAL_HEIGHT = 1;
/** Beyond this many columns apart, an edge becomes a pair of stubs. */
const DEFAULT_MAX_COLUMN_SPAN = 8;

export interface FrameGeometry {
  width: number;
  height: number;
  /** Vertical fraction where the viewport (readable zone) starts/ends. */
  frameTop: number;
  frameBottom: number;
  /** Wall-ms visible per pixel. Constant => honest geometry. */
  msPerPx: number;

  /** Horizontal inset per nesting level, px. Default 3. */
  intervalInsetPx?: number;
  /** Minimum drawn interval width, px. Default 2. */
  minIntervalWidthPx?: number;
  /** Minimum drawn interval height, px. Default 1. */
  minIntervalHeightPx?: number;
  /** Column-distance threshold past which edges become stubs. Default 8. */
  maxColumnSpan?: number;
}

/** {@link FrameGeometry} with every optional knob resolved. */
export interface ResolvedGeometry {
  width: number;
  height: number;
  frameTop: number;
  frameBottom: number;
  msPerPx: number;
  intervalInsetPx: number;
  minIntervalWidthPx: number;
  minIntervalHeightPx: number;
  maxColumnSpan: number;
  /** `frameTop * height`, precomputed. */
  frameTopPx: number;
  /** `frameBottom * height`, precomputed. */
  frameBottomPx: number;
}

/** A lifeline column rule, resolved to screen coordinates. */
export interface FrameLifelineRule {
  /** Id of the column (the subtree root when collapsed). */
  lifelineId: string;
  label: string;
  color: string;
  x: number;
  width: number;
  collapsed: boolean;
  folded: boolean;
  /** Number of lifelines this column renders; > 1 when composite. */
  memberCount: number;
  depth: number;
  /** y of the earliest birth among members. */
  yBirth: number;
  /** y of the latest death among members. */
  yDeath: number;
  /** Birth marker falls inside the canvas. */
  birthOnScreen: boolean;
  /** Death marker falls inside the canvas *and* a real termination was seen. */
  deathOnScreen: boolean;
  /** A real termination was observed for every member. */
  died: boolean;
  /** Alive at the playhead's wall time. */
  alive: boolean;
  /** The lifetime intersects the visible wall-time range. */
  visible: boolean;
}

/** An interval resolved to a screen rectangle. */
export interface FrameInterval {
  id: string;
  lifelineId: string;
  /** Column that renders it; differs from `lifelineId` when absorbed. */
  columnId: string;
  kind: IntervalKind;
  /** Nesting depth within the lifeline, from the digest. */
  depth: number;
  /** Depth actually used for the inset; adds absorbed-subtree depth. */
  insetLevel: number;

  x: number;
  y: number;
  width: number;
  height: number;

  /** Lifeline colour to fill with; the renderer applies theme alpha on top. */
  colorKey: string;
  confidence: Confidence;
  error: boolean;
  label?: string;
  logId?: string;

  startMs: number;
  endMs: number;
  zone: Zone;
  /** 1 inside the frame, fading to 0 at the top of the wake. */
  opacity: number;
  /** Top edge is above the canvas. */
  clippedTop: boolean;
  /** Bottom edge is below the canvas. */
  clippedBottom: boolean;
}

/** An edge resolved to a sloped screen segment. */
export interface FrameEdge {
  id: string;
  kind: EdgeKind;
  fromId: string;
  toId: string;
  /** Sender-side point: sender's column x at `sendMs`. */
  x1: number;
  y1: number;
  /** Recipient-side point: recipient's column x at `recvMs`. */
  x2: number;
  y2: number;
  sendMs: number;
  recvMs: number;
  /** Delivery latency in wall-ms; the slope of the segment expresses it. */
  latencyMs: number;
  recvConfidence: Confidence;
  broadcast: boolean;
  label?: string;
  msgType?: string;
  logId?: string;
  /** Colour of the sending lifeline. */
  colorKey: string;
  zone: Zone;
  opacity: number;
}

/** Screen boundary a stub points at. */
export type StubSide = 'top' | 'bottom' | 'left' | 'right';

/** Why the peer endpoint could not be drawn. */
export type StubReason = 'offscreen' | 'hidden' | 'distant';

/**
 * Half of an edge whose other end cannot be drawn.
 *
 * The renderer draws a short segment from `(x, y)` toward `(tipX, tipY)` - the
 * point where the true edge line leaves the canvas - with an arrowhead and the
 * peer's name, so connectivity survives cropping.
 */
export interface FrameEdgeStub {
  edgeId: string;
  kind: EdgeKind;
  /** `outgoing`: this end is the sender. `incoming`: this end is the recipient. */
  direction: 'outgoing' | 'incoming';
  reason: StubReason;
  side: StubSide;

  /** Anchor on the visible lifeline. */
  x: number;
  y: number;
  /** Where the true edge line meets the canvas boundary. */
  tipX: number;
  tipY: number;

  /** The lifeline that is on screen. */
  anchorId: string;
  /** The lifeline that is not. */
  otherId: string;
  /** Display name of the off-screen peer. */
  label: string;
  /** Edge label / message type, when the digest carries one. */
  detail?: string;

  latencyMs: number;
  recvConfidence: Confidence;
  colorKey: string;
  zone: Zone;
  opacity: number;
}

/** Everything one frame needs, fully resolved. */
export interface FrameModel {
  geom: ResolvedGeometry;
  /** Wall time at the playhead. */
  wallMs: number;
  /** Effective wall-ms per viewer-ms, as reported by the clock. */
  velocity: number;
  /** 0 = read the staging zone, 1 = it is pure motion. */
  streakFactor: number;

  /** y of the playhead; equals `geom.frameBottomPx`. */
  playheadY: number;
  /** Wall time at `y = 0` (top of canvas). */
  visibleStartMs: number;
  /** Wall time at `y = height` (bottom of canvas). */
  visibleEndMs: number;

  lifelines: FrameLifelineRule[];
  /** Sorted by nesting depth ascending, so the deepest are drawn last. */
  intervals: FrameInterval[];
  edges: FrameEdge[];
  stubs: FrameEdgeStub[];

  /** Diagnostics; cheap to keep and invaluable when tuning culling. */
  counts: {
    intervalsConsidered: number;
    intervalsDrawn: number;
    edgesConsidered: number;
    edgesDrawn: number;
    stubs: number;
    /** Edges whose two endpoints landed in the same composite column. */
    edgesInternal: number;
  };
}

/**
 * Cross-fade weight for the staging zone.
 *
 * Ramps on a log scale between {@link STREAK_MIN_VELOCITY} and
 * {@link STREAK_FULL_VELOCITY}, smoothstepped so the transition has no visible
 * corner. Monotonically non-decreasing in `velocity`.
 */
export function streakFactorFor(velocity: number): number {
  if (!Number.isFinite(velocity) || velocity <= STREAK_MIN_VELOCITY) return 0;
  if (velocity >= STREAK_FULL_VELOCITY) return 1;
  const lo = Math.log2(STREAK_MIN_VELOCITY);
  const hi = Math.log2(STREAK_FULL_VELOCITY);
  const t = (Math.log2(velocity) - lo) / (hi - lo);
  return t * t * (3 - 2 * t);
}

/** Resolve the optional geometry knobs and precompute the zone boundaries. */
export function resolveGeometry(geom: FrameGeometry): ResolvedGeometry {
  const height = Math.max(1, geom.height);
  const frameTop = clamp01(geom.frameTop);
  const frameBottom = Math.max(frameTop, clamp01(geom.frameBottom));
  const msPerPx =
    Number.isFinite(geom.msPerPx) && geom.msPerPx > 0 ? geom.msPerPx : 1;
  return {
    width: Math.max(1, geom.width),
    height,
    frameTop,
    frameBottom,
    msPerPx,
    intervalInsetPx: geom.intervalInsetPx ?? DEFAULT_INTERVAL_INSET,
    minIntervalWidthPx: geom.minIntervalWidthPx ?? DEFAULT_MIN_INTERVAL_WIDTH,
    minIntervalHeightPx: geom.minIntervalHeightPx ?? DEFAULT_MIN_INTERVAL_HEIGHT,
    maxColumnSpan: geom.maxColumnSpan ?? DEFAULT_MAX_COLUMN_SPAN,
    frameTopPx: frameTop * height,
    frameBottomPx: frameBottom * height,
  };
}

function clamp01(v: number): number {
  if (!Number.isFinite(v)) return 0;
  if (v < 0) return 0;
  if (v > 1) return 1;
  return v;
}

/* -------------------------------------------------------------------------- */
/* Digest index                                                               */
/* -------------------------------------------------------------------------- */

interface DigestIndex {
  lifelineById: Map<string, Lifeline>;
  /** Intervals sorted by `startMs`. */
  intervals: Interval[];
  /** `prefixMaxEnd[i]` = max `endMs` over `intervals[0..i]`; non-decreasing. */
  prefixMaxEnd: number[];
  /** Edges sorted by `min(sendMs, recvMs)`. */
  edges: Edge[];
  /** `prefixMaxT[i]` = max `max(sendMs, recvMs)` over `edges[0..i]`. */
  prefixMaxT: number[];
}

// A digest is immutable once loaded, so the index can outlive any single frame.
const indexCache = new WeakMap<Digest, DigestIndex>();

function indexOf(digest: Digest): DigestIndex {
  const cached = indexCache.get(digest);
  if (cached) return cached;

  const lifelineById = new Map<string, Lifeline>();
  for (const l of digest.lifelines ?? []) lifelineById.set(l.id, l);

  const intervals = (digest.intervals ?? []).slice().sort((a, b) => a.startMs - b.startMs);
  const prefixMaxEnd: number[] = new Array(intervals.length);
  let running = -Infinity;
  for (let i = 0; i < intervals.length; i++) {
    running = Math.max(running, intervals[i].endMs);
    prefixMaxEnd[i] = running;
  }

  const edges = (digest.edges ?? [])
    .slice()
    .sort((a, b) => Math.min(a.sendMs, a.recvMs) - Math.min(b.sendMs, b.recvMs));
  const prefixMaxT: number[] = new Array(edges.length);
  running = -Infinity;
  for (let i = 0; i < edges.length; i++) {
    running = Math.max(running, Math.max(edges[i].sendMs, edges[i].recvMs));
    prefixMaxT[i] = running;
  }

  const idx: DigestIndex = {
    lifelineById,
    intervals,
    prefixMaxEnd,
    edges,
    prefixMaxT,
  };
  indexCache.set(digest, idx);
  return idx;
}

/**
 * First index whose prefix-max exceeds `value`; everything before it ended
 * before the window opened and can be skipped without inspection.
 */
function firstRelevant(prefixMax: number[], value: number): number {
  let lo = 0;
  let hi = prefixMax.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (prefixMax[mid] < value) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

/** Last index (exclusive) whose sort key is `<= value`. */
function lastRelevant<T>(items: T[], key: (t: T) => number, value: number): number {
  let lo = 0;
  let hi = items.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (key(items[mid]) <= value) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

/* -------------------------------------------------------------------------- */
/* Frame construction                                                         */
/* -------------------------------------------------------------------------- */

/**
 * Build the view model for one frame.
 *
 * `wallMs` is the playhead (bottom of the readable frame) and `velocity` is the
 * clock's effective wall-ms per viewer-ms, used only for the streak cross-fade.
 */
export function buildFrame(
  digest: Digest,
  layout: ColumnLayout,
  geom: FrameGeometry,
  wallMs: number,
  velocity: number,
): FrameModel {
  const g = resolveGeometry(geom);
  const idx = indexOf(digest);
  const playhead = Number.isFinite(wallMs) ? wallMs : 0;

  /** Wall time -> y. Later wall times are lower on screen. */
  const yFor = (t: number): number => g.frameBottomPx + (t - playhead) / g.msPerPx;

  const visibleStartMs = playhead - g.frameBottomPx * g.msPerPx;
  const visibleEndMs = playhead + (g.height - g.frameBottomPx) * g.msPerPx;

  const zoneOf = (y: number): Zone => {
    if (y < g.frameTopPx) return 'wake';
    if (y <= g.frameBottomPx) return 'frame';
    return 'staging';
  };

  /** Full opacity inside the frame and staging; linear fade out through the wake. */
  const opacityAt = (y: number): number => {
    if (y >= g.frameTopPx) return 1;
    if (g.frameTopPx <= 0) return 1;
    const f = y / g.frameTopPx;
    return f < 0 ? 0 : f;
  };

  const columnIndex = new Map<string, number>();
  layout.columns.forEach((c, i) => columnIndex.set(c.lifelineId, i));

  /* ---- lifeline rules -------------------------------------------------- */

  const lifelines: FrameLifelineRule[] = layout.columns.map((col) => {
    let birth = Infinity;
    let death = -Infinity;
    let allDied = true;
    for (const id of col.memberIds) {
      const l = idx.lifelineById.get(id);
      if (!l) continue;
      birth = Math.min(birth, l.birthMs);
      death = Math.max(death, l.deathMs);
      if (!l.died) allDied = false;
    }
    if (!Number.isFinite(birth)) birth = digest.durationMs > 0 ? 0 : 0;
    if (!Number.isFinite(death)) death = digest.durationMs;

    const yBirth = yFor(birth);
    const yDeath = yFor(death);
    return {
      lifelineId: col.lifelineId,
      label: labelForColumn(col),
      color: col.lifeline.color,
      x: col.x,
      width: col.width,
      collapsed: col.collapsed,
      folded: col.folded,
      memberCount: col.memberIds.length,
      depth: col.depth,
      yBirth,
      yDeath,
      birthOnScreen: yBirth >= 0 && yBirth <= g.height,
      deathOnScreen: allDied && yDeath >= 0 && yDeath <= g.height,
      died: allDied,
      alive: playhead >= birth && playhead <= death,
      visible: death >= visibleStartMs && birth <= visibleEndMs,
    };
  });

  /* ---- intervals ------------------------------------------------------- */

  const intervals: FrameInterval[] = [];
  let intervalsConsidered = 0;
  const ivStart = firstRelevant(idx.prefixMaxEnd, visibleStartMs);
  const ivEnd = lastRelevant(idx.intervals, (iv) => iv.startMs, visibleEndMs);

  for (let i = ivStart; i < ivEnd; i++) {
    const iv = idx.intervals[i];
    intervalsConsidered++;
    if (iv.endMs < visibleStartMs || iv.startMs > visibleEndMs) continue;

    const col = layout.columnFor.get(iv.lifelineId);
    if (!col) continue;

    const insetLevel = iv.depth + absorbedDepth(col, iv.lifelineId, idx.lifelineById);
    const rect = insetRect(col, insetLevel, g);

    const yTop = yFor(iv.startMs);
    const rawHeight = (iv.endMs - iv.startMs) / g.msPerPx;
    const height = Math.max(g.minIntervalHeightPx, rawHeight);
    const mid = yTop + height / 2;

    const lifeline = idx.lifelineById.get(iv.lifelineId);
    const fi: FrameInterval = {
      id: iv.id,
      lifelineId: iv.lifelineId,
      columnId: col.lifelineId,
      kind: iv.kind,
      depth: iv.depth,
      insetLevel,
      x: rect.x,
      y: yTop,
      width: rect.width,
      height,
      colorKey: lifeline ? lifeline.color : col.lifeline.color,
      confidence: iv.confidence,
      error: iv.error === true,
      ...(iv.label !== undefined ? { label: iv.label } : {}),
      ...(iv.logId !== undefined ? { logId: iv.logId } : {}),
      startMs: iv.startMs,
      endMs: iv.endMs,
      zone: zoneOf(mid),
      opacity: opacityAt(mid),
      clippedTop: yTop < 0,
      clippedBottom: yTop + height > g.height,
    };
    intervals.push(fi);
  }

  // Shallow first so the flame graph's deepest frames land on top.
  intervals.sort((a, b) => a.insetLevel - b.insetLevel || a.startMs - b.startMs);

  /* ---- edges and stubs -------------------------------------------------- */

  const edges: FrameEdge[] = [];
  const stubs: FrameEdgeStub[] = [];
  let edgesConsidered = 0;
  let edgesInternal = 0;

  const eStart = firstRelevant(idx.prefixMaxT, visibleStartMs);
  const eEnd = lastRelevant(
    idx.edges,
    (e) => Math.min(e.sendMs, e.recvMs),
    visibleEndMs,
  );

  for (let i = eStart; i < eEnd; i++) {
    const e = idx.edges[i];
    edgesConsidered++;

    const lo = Math.min(e.sendMs, e.recvMs);
    const hi = Math.max(e.sendMs, e.recvMs);
    if (hi < visibleStartMs || lo > visibleEndMs) continue;

    const fromCol = layout.columnFor.get(e.fromId);
    const toCol = layout.columnFor.get(e.toId);
    if (!fromCol && !toCol) continue;
    if (fromCol && toCol && fromCol === toCol) {
      // Both ends absorbed into one composite column: the message is internal
      // to that column and has nowhere to be drawn.
      edgesInternal++;
      continue;
    }

    const y1 = yFor(e.sendMs);
    const y2 = yFor(e.recvMs);
    const sendOnScreen = y1 >= 0 && y1 <= g.height;
    const recvOnScreen = y2 >= 0 && y2 <= g.height;

    const fromLl = idx.lifelineById.get(e.fromId);
    const toLl = idx.lifelineById.get(e.toId);
    const colorKey = fromLl ? fromLl.color : (fromCol ?? toCol)!.lifeline.color;
    const latencyMs = e.recvMs - e.sendMs;

    let tooFar = false;
    if (fromCol && toCol) {
      const a = columnIndex.get(fromCol.lifelineId);
      const b = columnIndex.get(toCol.lifelineId);
      if (a !== undefined && b !== undefined) {
        tooFar = Math.abs(a - b) > g.maxColumnSpan;
      }
    }

    const mkStub = (
      anchorCol: Column,
      anchorY: number,
      anchorIsSender: boolean,
      reason: StubReason,
      peerX: number | null,
      peerY: number,
      peerName: string,
      peerId: string,
    ): void => {
      const side = stubSide(anchorCol.x, anchorY, peerX, peerY, g);
      const tip = stubTip(anchorCol.x, anchorY, peerX, peerY, side, g);
      const stub: FrameEdgeStub = {
        edgeId: e.id,
        kind: e.kind,
        direction: anchorIsSender ? 'outgoing' : 'incoming',
        reason,
        side,
        x: anchorCol.x,
        y: anchorY,
        tipX: tip.x,
        tipY: tip.y,
        anchorId: anchorIsSender ? e.fromId : e.toId,
        otherId: peerId,
        label: peerName,
        ...(e.label !== undefined
          ? { detail: e.label }
          : e.msgType !== undefined
            ? { detail: e.msgType }
            : {}),
        latencyMs,
        recvConfidence: e.recvConfidence,
        colorKey,
        zone: zoneOf(anchorY),
        opacity: opacityAt(anchorY),
      };
      stubs.push(stub);
    };

    if (tooFar && fromCol && toCol) {
      // Both ends exist but are too far apart to connect without the line
      // sweeping across every column between them.
      if (sendOnScreen) {
        mkStub(
          fromCol,
          y1,
          true,
          'distant',
          toCol.x,
          y2,
          nameOf(toLl, toCol, e.toId),
          e.toId,
        );
      }
      if (recvOnScreen) {
        mkStub(
          toCol,
          y2,
          false,
          'distant',
          fromCol.x,
          y1,
          nameOf(fromLl, fromCol, e.fromId),
          e.fromId,
        );
      }
      continue;
    }

    const fromUsable = fromCol !== undefined && sendOnScreen;
    const toUsable = toCol !== undefined && recvOnScreen;

    if (fromUsable && toUsable && fromCol && toCol) {
      const midY = (y1 + y2) / 2;
      const fe: FrameEdge = {
        id: e.id,
        kind: e.kind,
        fromId: e.fromId,
        toId: e.toId,
        x1: fromCol.x,
        y1,
        x2: toCol.x,
        y2,
        sendMs: e.sendMs,
        recvMs: e.recvMs,
        latencyMs,
        recvConfidence: e.recvConfidence,
        broadcast: e.broadcast === true,
        ...(e.label !== undefined ? { label: e.label } : {}),
        ...(e.msgType !== undefined ? { msgType: e.msgType } : {}),
        ...(e.logId !== undefined ? { logId: e.logId } : {}),
        colorKey,
        zone: zoneOf(midY),
        opacity: opacityAt(midY),
      };
      edges.push(fe);
      continue;
    }

    if (fromUsable && fromCol) {
      mkStub(
        fromCol,
        y1,
        true,
        toCol === undefined ? 'hidden' : 'offscreen',
        toCol ? toCol.x : null,
        y2,
        nameOf(toLl, toCol, e.toId),
        e.toId,
      );
      continue;
    }
    if (toUsable && toCol) {
      mkStub(
        toCol,
        y2,
        false,
        fromCol === undefined ? 'hidden' : 'offscreen',
        fromCol ? fromCol.x : null,
        y1,
        nameOf(fromLl, fromCol, e.fromId),
        e.fromId,
      );
      continue;
    }
    // Neither end is on screen: nothing honest to draw.
  }

  edges.sort((a, b) => a.sendMs - b.sendMs);

  return {
    geom: g,
    wallMs: playhead,
    velocity,
    streakFactor: streakFactorFor(velocity),
    playheadY: g.frameBottomPx,
    visibleStartMs,
    visibleEndMs,
    lifelines,
    intervals,
    edges,
    stubs,
    counts: {
      intervalsConsidered,
      intervalsDrawn: intervals.length,
      edgesConsidered,
      edgesDrawn: edges.length,
      stubs: stubs.length,
      edgesInternal,
    },
  };
}

function labelForColumn(col: Column): string {
  if (col.memberIds.length > 1) {
    return `${col.lifeline.name} +${col.memberIds.length - 1}`;
  }
  return col.lifeline.name;
}

function nameOf(
  lifeline: Lifeline | undefined,
  col: Column | undefined,
  id: string,
): string {
  if (lifeline) return lifeline.name;
  if (col) return col.lifeline.name;
  return id;
}

/**
 * Extra inset for a lifeline absorbed into a collapsed ancestor's column, so
 * the composite still reads as a flame graph of the whole subtree.
 */
function absorbedDepth(
  col: Column,
  lifelineId: string,
  lifelineById: Map<string, Lifeline>,
): number {
  if (col.lifelineId === lifelineId) return 0;
  const own = lifelineById.get(lifelineId);
  if (!own) return 0;
  const root = lifelineById.get(col.lifelineId);
  const rootDepth = root ? root.depth : col.lifeline.depth;
  const d = own.depth - rootDepth;
  return d > 0 ? d : 0;
}

function insetRect(
  col: Column,
  level: number,
  g: ResolvedGeometry,
): { x: number; width: number } {
  const full = col.width;
  const wanted = full - 2 * g.intervalInsetPx * Math.max(0, level);
  const width = Math.max(Math.min(full, g.minIntervalWidthPx), wanted);
  return { x: col.x - width / 2, width };
}

/** Which canvas boundary the missing endpoint lies beyond. */
function stubSide(
  ax: number,
  ay: number,
  peerX: number | null,
  peerY: number,
  g: ResolvedGeometry,
): StubSide {
  const vertical = peerY < 0 ? 'top' : peerY > g.height ? 'bottom' : null;
  if (peerX === null) {
    // Peer column is not rendered at all: fall back to the vertical side if we
    // have one, otherwise point off the nearer horizontal edge.
    if (vertical) return vertical;
    return ax < g.width / 2 ? 'left' : 'right';
  }
  const dx = peerX - ax;
  const dy = peerY - ay;
  // Prefer the axis the peer is actually further out on.
  if (vertical && Math.abs(dy) >= Math.abs(dx)) return vertical;
  if (Math.abs(dx) > 0) return dx < 0 ? 'left' : 'right';
  return vertical ?? 'bottom';
}

/**
 * Point where the segment from the anchor toward the peer leaves the canvas.
 *
 * Keeping the real slope here is what lets a stub still communicate latency:
 * a steep stub means the peer is far away in time, not just off screen.
 */
function stubTip(
  ax: number,
  ay: number,
  peerX: number | null,
  peerY: number,
  side: StubSide,
  g: ResolvedGeometry,
): { x: number; y: number } {
  const px = peerX ?? (side === 'left' ? -g.width : 2 * g.width);
  const py = peerY;
  const dx = px - ax;
  const dy = py - ay;

  let t = 1;
  if (side === 'top' && dy !== 0) t = (0 - ay) / dy;
  else if (side === 'bottom' && dy !== 0) t = (g.height - ay) / dy;
  else if (side === 'left' && dx !== 0) t = (0 - ax) / dx;
  else if (side === 'right' && dx !== 0) t = (g.width - ax) / dx;
  if (!Number.isFinite(t) || t < 0) t = 0;
  if (t > 1) t = 1;

  return {
    x: clampTo(ax + dx * t, 0, g.width),
    y: clampTo(ay + dy * t, 0, g.height),
  };
}

function clampTo(v: number, lo: number, hi: number): number {
  if (!Number.isFinite(v)) return lo;
  return v < lo ? lo : v > hi ? hi : v;
}
