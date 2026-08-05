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
 * Wire types for the precomputed run digest.
 *
 * This file mirrors `internal/digest/types.go` exactly and must be kept in sync
 * with it. The digest is the only input the renderer consumes: it never infers
 * anything, which is what makes it a pure, testable function of its input.
 *
 * Two time domains are in play throughout:
 *  - **wall time** (`t`): real elapsed time inside the run. All *geometry* is in
 *    this domain, so a bar's drawn length is always its true duration.
 *  - **viewer time** (`tau`): time as experienced while watching playback.
 *
 * The elasticity of the visualization lives entirely in the mapping between
 * them ({@link Warp}), never in the geometry. Idle stretches are traversed
 * *faster* rather than drawn *shorter*.
 */

/** Schema version understood by this frontend. */
export const SCHEMA_VERSION = 1;

/**
 * How much an interval's duration can be trusted.
 *
 * Scion's hook-per-process telemetry often emits an end event with no matching
 * start, which would silently become a zero-duration span. Rather than
 * fabricate a plausible duration, the digest labels what it actually knows and
 * the renderer draws each case differently.
 */
export type Confidence =
  /** Both endpoints observed; the duration is real. */
  | 'measured'
  /** One endpoint derived from the neighbouring event; an upper bound. */
  | 'inferred'
  /** No end known; runs to lifeline death or end of run, drawn unterminated. */
  | 'open';

/**
 * Nesting layer of an interval. Lower `depth` encloses higher `depth` on the
 * same lifeline, producing a flame graph *within* each actor's column.
 */
export type IntervalKind = 'lifecycle' | 'session' | 'turn' | 'tool';

/** Classification of an edge between two lifelines. */
export type EdgeKind = 'message' | 'spawn' | 'destroy';

/**
 * One persistent actor column.
 *
 * Unlike a trace viewer - where an actor appearing N times yields N unrelated
 * bars - a lifeline exists once for the whole run, and every interval and edge
 * references it by id. That is the property this visualization borrows from
 * sequence diagrams.
 */
export interface Lifeline {
  id: string;
  name: string;
  harness?: string;
  color: string;

  /** Spawning lifeline, absent for roots. */
  parentId?: string;
  /** Ordered ancestor chain, `[root, ..., parent]`. */
  ancestry: string[];
  /** `ancestry.length`; 0 for roots. */
  depth: number;

  /**
   * Position in a depth-first traversal of the ancestry forest. Rendering in
   * this order keeps parents adjacent to children, which keeps edges short.
   */
  order: number;

  /**
   * Recycled column index. Lifelines whose lifetimes do not overlap may share a
   * slot, so a run with 100 agents but only 8-12 concurrent needs ~12 columns.
   */
  slot: number;

  birthMs: number;
  deathMs: number;
  /** Whether a real termination was observed, vs. running to end of log. */
  died: boolean;

  /** Cloud Logging `insertId` of the creating record, for deep linking. */
  logId?: string;
}

/**
 * A span of activity on one lifeline.
 *
 * Because the vertical axis is metric, this is simultaneously the
 * sequence-diagram *activation box* and the trace *span*. That identity is the
 * central idea of the visualization.
 */
export interface Interval {
  id: string;
  lifelineId: string;
  kind: IntervalKind;
  /** Nesting level within the lifeline; children are drawn inset. */
  depth: number;

  startMs: number;
  endMs: number;

  label?: string;
  confidence: Confidence;
  /** The source event reported a failure. */
  error?: boolean;
  /** Cloud Logging `insertId` of the originating start record. */
  logId?: string;
}

/**
 * A message between two lifelines.
 *
 * On a metric time axis a message is not horizontal: it leaves at `sendMs` and
 * arrives at `recvMs`, so the drawn line is **sloped**, and the slope *is* the
 * delivery latency. Steep edges reveal queueing or a busy recipient without the
 * viewer reading a number.
 */
export interface Edge {
  id: string;
  kind: EdgeKind;

  fromId: string;
  toId: string;

  sendMs: number;
  recvMs: number;

  /**
   * Provenance of `recvMs`.
   *
   * The Cloud Logging message stream records one timestamp per message, so a
   * true receive time is unavailable from that source. Where possible arrival
   * is inferred as the recipient's next observed activity - arguably the more
   * useful quantity, since it shows when the recipient actually acted - and
   * marked `inferred`. Otherwise `recvMs === sendMs` and this is `open`, drawn
   * horizontal. A digest sourced from the messages table (which has both
   * `created` and `dispatched_at`) could report `measured`.
   */
  recvConfidence: Confidence;

  msgType?: string;
  label?: string;
  broadcast?: boolean;
  logId?: string;
}

/**
 * Uniform sampling of activity intensity across the run; the default driver of
 * the velocity profile. The express lane accelerates where this is low.
 */
export interface Density {
  bucketMs: number;
  /**
   * Weighted event count per bucket, pre-smoothed by a frame-sized kernel so
   * deceleration begins *before* a burst enters the viewport, not after.
   */
  samples: number[];
  peak: number;
}

/** A single sample of the viewer-time to wall-time mapping. */
export interface WarpKnot {
  /** Viewer time: the clock that advances steadily while watching. */
  tauMs: number;
  /** Corresponding wall time within the run. */
  wallMs: number;
  /**
   * `dWall/dTau` here, in wall-ms per viewer-ms. 1 is real time; 60 means a
   * minute of run per second of watching. The renderer uses this directly to
   * cross-fade the staging zone into motion streaks as it rises.
   */
  velocity: number;
}

/**
 * The precomputed monotonic mapping between viewer time and wall time.
 *
 * Stored as piecewise-linear knots. Because both coordinates strictly increase,
 * the mapping inverts cheaply - which is what lets the scrubber, minimap,
 * timestamp readout and shareable links all stay mutually consistent: they are
 * projections of one function. It is computed offline so the same run always
 * plays identically and a link to a moment always lands on that moment.
 */
export interface Warp {
  knots: WarpKnot[];
  /** Total viewer-time length of playback at 1x, shorter than the run itself. */
  totalTauMs: number;
  minVelocity: number;
  maxVelocity: number;
}

/** Composition of the digest, including how much of it is inferred. */
export interface Stats {
  lifelineCount: number;
  intervalCount: number;
  edgeCount: number;
  /** Largest number of simultaneously alive lifelines = column slots needed. */
  maxConcurrent: number;

  measuredIntervals: number;
  inferredIntervals: number;
  openIntervals: number;
  inferredEdges: number;

  /** `durationMs / warp.totalTauMs`: speedup over real time at 1x. */
  compressionRatio: number;
}

/** The complete precomputed model of one run. */
export interface Digest {
  version: number;

  projectId: string;
  projectName: string;

  /** RFC3339Nano absolute bounds; all ms offsets are relative to `startedAt`. */
  startedAt: string;
  endedAt: string;
  durationMs: number;

  lifelines: Lifeline[];
  intervals: Interval[];
  edges: Edge[];

  density: Density;
  warp: Warp;
  stats: Stats;
}
