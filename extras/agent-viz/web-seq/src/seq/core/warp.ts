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
 * Evaluation of the precomputed viewer-time to wall-time {@link Warp}.
 *
 * The warp is the only place elasticity is allowed to live: geometry stays
 * honest (a bar's length is always its true duration) and boring stretches are
 * traversed *faster* instead of being drawn *shorter*. Everything downstream -
 * the clock, the scrubber, the minimap, the timestamp readout, shareable links
 * - is a projection of this one function, which is why it must invert exactly.
 *
 * Lookups are binary searches: the knot array can hold thousands of samples and
 * every one of these methods is called at least once per animation frame.
 */

import type { Warp, WarpKnot } from './types.js';

/** Velocity assumed when the warp carries no usable slope information. */
const FALLBACK_VELOCITY = 1;

/**
 * A piecewise-linear, strictly monotonic map between viewer time (`tau`) and
 * wall time within the run.
 *
 * Construction normalises the knot array defensively (sorts by `tauMs`, drops
 * non-finite samples, forces `wallMs` to be non-decreasing) so that the binary
 * searches below are always valid even against a hand-written or partially
 * corrupt digest.
 */
export class WarpFn {
  private readonly knots: readonly WarpKnot[];
  private readonly _totalTauMs: number;
  private readonly _maxVelocity: number;
  private readonly _fallbackVelocity: number;

  constructor(warp: Warp) {
    const raw: readonly WarpKnot[] =
      warp && Array.isArray(warp.knots) ? warp.knots : [];

    const sorted = raw
      .filter(
        (k): k is WarpKnot =>
          !!k && Number.isFinite(k.tauMs) && Number.isFinite(k.wallMs),
      )
      .slice()
      .sort((a, b) => a.tauMs - b.tauMs);

    // Force both coordinates non-decreasing. A digest that violates this would
    // otherwise make `tauAt` non-invertible.
    const clean: WarpKnot[] = [];
    let prevTau = -Infinity;
    let prevWall = -Infinity;
    for (const k of sorted) {
      const tauMs = Math.max(k.tauMs, prevTau === -Infinity ? k.tauMs : prevTau);
      const wallMs = Math.max(
        k.wallMs,
        prevWall === -Infinity ? k.wallMs : prevWall,
      );
      const velocity =
        Number.isFinite(k.velocity) && k.velocity > 0
          ? k.velocity
          : FALLBACK_VELOCITY;
      clean.push({ tauMs, wallMs, velocity });
      prevTau = tauMs;
      prevWall = wallMs;
    }

    this.knots = clean;

    // The mapped range is authoritative: playback must never advance into tau
    // values the warp says nothing about. Only when there are no knots at all
    // do we fall back to the declared total.
    if (clean.length > 0) {
      this._totalTauMs = Math.max(0, clean[clean.length - 1].tauMs);
    } else {
      this._totalTauMs = Math.max(0, Number.isFinite(warp?.totalTauMs) ? warp.totalTauMs : 0);
    }

    const declaredMax =
      warp && Number.isFinite(warp.maxVelocity) && warp.maxVelocity > 0
        ? warp.maxVelocity
        : 0;

    // Prefer the slope actually implied by the knots so `maxVelocity` and
    // `velocityAt` can never disagree; fall back to the declared value.
    let maxSlope = 0;
    for (let i = 0; i + 1 < clean.length; i++) {
      const s = this.slopeOfSegment(i);
      if (s > maxSlope) maxSlope = s;
    }
    if (maxSlope > 0) {
      this._maxVelocity = maxSlope;
    } else if (clean.length === 1 && clean[0].velocity > 0) {
      this._maxVelocity = clean[0].velocity;
    } else {
      this._maxVelocity = declaredMax > 0 ? declaredMax : FALLBACK_VELOCITY;
    }

    this._fallbackVelocity =
      clean.length > 0 && clean[0].velocity > 0
        ? clean[0].velocity
        : declaredMax > 0
          ? declaredMax
          : FALLBACK_VELOCITY;
  }

  /** Total viewer-time length of playback at 1x. */
  get totalTauMs(): number {
    return this._totalTauMs;
  }

  /** Fastest `dWall/dTau` anywhere in the run; drives streak intensity scaling. */
  get maxVelocity(): number {
    return this._maxVelocity;
  }

  /** Number of knots after normalisation. Exposed for diagnostics and tests. */
  get knotCount(): number {
    return this.knots.length;
  }

  /** Viewer time -> wall time. Out-of-range input clamps to the endpoints. */
  wallAt(tauMs: number): number {
    const n = this.knots.length;
    if (n === 0) return 0;
    const tau = this.clampTau(tauMs);
    if (n === 1) return this.knots[0].wallMs;

    const i = this.segmentForTau(tau);
    const a = this.knots[i];
    const b = this.knots[i + 1];
    const span = b.tauMs - a.tauMs;
    if (span <= 0) return a.wallMs;
    const f = (tau - a.tauMs) / span;
    return a.wallMs + f * (b.wallMs - a.wallMs);
  }

  /** Wall time -> viewer time. Out-of-range input clamps to the endpoints. */
  tauAt(wallMs: number): number {
    const n = this.knots.length;
    if (n === 0) return 0;
    if (n === 1) return this.knots[0].tauMs;

    const first = this.knots[0];
    const last = this.knots[n - 1];
    const wall = Number.isFinite(wallMs) ? wallMs : first.wallMs;
    if (wall <= first.wallMs) return first.tauMs;
    if (wall >= last.wallMs) return last.tauMs;

    const i = this.lowerBoundWall(wall);
    const b = this.knots[i];
    // Landing exactly on a knot - including the first knot of a plateau, where
    // several taus share one wall time - resolves to the earliest such tau, so
    // the inverse stays single-valued.
    if (b.wallMs <= wall) return b.tauMs;
    const a = this.knots[i - 1];
    const span = b.wallMs - a.wallMs;
    if (span <= 0) return a.tauMs;
    const f = (wall - a.wallMs) / span;
    return a.tauMs + f * (b.tauMs - a.tauMs);
  }

  /**
   * `dWall/dTau` at `tauMs`, in wall-ms per viewer-ms.
   *
   * This is the slope of the enclosing segment rather than an interpolation of
   * the knots' own `velocity` fields, so it is exactly the derivative of
   * {@link wallAt}. Where a segment is degenerate the stored knot velocity is
   * used instead.
   */
  velocityAt(tauMs: number): number {
    const n = this.knots.length;
    if (n === 0) return this._fallbackVelocity;
    if (n === 1) return this.knots[0].velocity || this._fallbackVelocity;

    const tau = this.clampTau(tauMs);
    const i = this.segmentForTau(tau);
    const slope = this.slopeOfSegment(i);
    if (slope > 0) return slope;
    return this.knots[i].velocity || this._fallbackVelocity;
  }

  /** Wall time at `tau = 0`; the first mapped instant of the run. */
  get startWallMs(): number {
    return this.knots.length > 0 ? this.knots[0].wallMs : 0;
  }

  /** Wall time at `tau = totalTauMs`; the last mapped instant of the run. */
  get endWallMs(): number {
    return this.knots.length > 0 ? this.knots[this.knots.length - 1].wallMs : 0;
  }

  private clampTau(tauMs: number): number {
    if (!Number.isFinite(tauMs)) return 0;
    const lo = this.knots.length > 0 ? this.knots[0].tauMs : 0;
    const hi = this.knots.length > 0 ? this.knots[this.knots.length - 1].tauMs : 0;
    if (tauMs < lo) return lo;
    if (tauMs > hi) return hi;
    return tauMs;
  }

  private slopeOfSegment(i: number): number {
    const a = this.knots[i];
    const b = this.knots[i + 1];
    if (!a || !b) return 0;
    const dt = b.tauMs - a.tauMs;
    if (dt <= 0) return 0;
    const s = (b.wallMs - a.wallMs) / dt;
    return Number.isFinite(s) && s > 0 ? s : 0;
  }

  /** Last index `i` with `knots[i].tauMs <= tau`, clamped to a valid segment. */
  private segmentForTau(tau: number): number {
    let lo = 0;
    let hi = this.knots.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (this.knots[mid].tauMs <= tau) lo = mid;
      else hi = mid - 1;
    }
    return Math.min(lo, this.knots.length - 2);
  }

  /** First index `i` with `knots[i].wallMs >= wall`. Callers guarantee one exists. */
  private lowerBoundWall(wall: number): number {
    let lo = 0;
    let hi = this.knots.length - 1;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (this.knots[mid].wallMs >= wall) hi = mid;
      else lo = mid + 1;
    }
    return Math.max(1, lo);
  }
}

/**
 * Build a trivial 1:1 warp over `durationMs`.
 *
 * Useful for tests and for digests that arrive without a usable warp: playback
 * then runs at real time with no compression at all.
 */
export function identityWarp(durationMs: number): Warp {
  const d = Number.isFinite(durationMs) && durationMs > 0 ? durationMs : 0;
  return {
    knots: [
      { tauMs: 0, wallMs: 0, velocity: 1 },
      { tauMs: d, wallMs: d, velocity: 1 },
    ],
    totalTauMs: d,
    minVelocity: 1,
    maxVelocity: 1,
  };
}
