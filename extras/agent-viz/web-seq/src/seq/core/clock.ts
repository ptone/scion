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
 * Playback clock over viewer time.
 *
 * The clock owns exactly one number - `tauMs` - and derives everything else
 * from the {@link WarpFn}. It is deliberately frame-loop agnostic: the caller
 * feeds it real elapsed milliseconds via {@link PlaybackClock.tick}, which
 * makes playback fully deterministic and unit-testable without timers.
 */

import type { WarpFn } from './warp.js';

/**
 * Largest real elapsed step honoured by a single {@link PlaybackClock.tick}.
 *
 * A backgrounded tab resumes with a multi-second delta; without this cap the
 * run would jump forward by minutes of wall time on the first visible frame.
 */
export const MAX_TICK_MS = 250;

/** Slowest user playback multiplier. */
export const MIN_RATE = 0.25;
/** Fastest user playback multiplier. */
export const MAX_RATE = 8;

/** Tolerance for "has reached the end", in viewer ms. */
const END_EPSILON = 1e-6;

function clamp(v: number, lo: number, hi: number): number {
  if (!Number.isFinite(v)) return lo;
  if (v < lo) return lo;
  if (v > hi) return hi;
  return v;
}

export class PlaybackClock {
  private readonly warp: WarpFn;
  private _tauMs = 0;
  private _playing = false;
  private _rate = 1;

  constructor(warp: WarpFn, opts?: { rate?: number }) {
    this.warp = warp;
    if (opts && opts.rate !== undefined) {
      this._rate = clamp(opts.rate, MIN_RATE, MAX_RATE);
    }
  }

  /** Current viewer time, always within `[0, totalTauMs]`. */
  get tauMs(): number {
    return this._tauMs;
  }
  set tauMs(v: number) {
    this._tauMs = clamp(v, 0, this.warp.totalTauMs);
  }

  get playing(): boolean {
    return this._playing;
  }

  /** User playback multiplier applied on top of the warp; clamped to 0.25..8. */
  get rate(): number {
    return this._rate;
  }
  set rate(v: number) {
    this._rate = clamp(v, MIN_RATE, MAX_RATE);
  }

  /**
   * Start playback. Pressing play once the run has finished restarts it from
   * the beginning, matching every other media transport the user has met.
   */
  play(): void {
    if (this.atEnd && this.warp.totalTauMs > 0) this._tauMs = 0;
    this._playing = true;
  }

  pause(): void {
    this._playing = false;
  }

  toggle(): void {
    if (this._playing) this.pause();
    else this.play();
  }

  seekTau(tauMs: number): void {
    this.tauMs = tauMs;
  }

  /** Seek by wall time; the warp turns it into the equivalent viewer time. */
  seekWall(wallMs: number): void {
    this.tauMs = this.warp.tauAt(wallMs);
  }

  /** Wall time currently at the playhead. */
  get wallMs(): number {
    return this.warp.wallAt(this._tauMs);
  }

  /** Effective wall-ms per viewer-ms right now, including the user rate. */
  get velocity(): number {
    return this.warp.velocityAt(this._tauMs) * this._rate;
  }

  /** Fraction of the run played, 0..1. Zero-length runs report 1. */
  get progress(): number {
    const total = this.warp.totalTauMs;
    if (total <= 0) return 1;
    return this._tauMs / total;
  }

  /** True once playback has reached the end of the run. */
  get atEnd(): boolean {
    return this._tauMs >= this.warp.totalTauMs - END_EPSILON;
  }

  /**
   * Advance by real elapsed milliseconds.
   *
   * The step is capped at {@link MAX_TICK_MS} before the rate multiplier is
   * applied, clamped to the mapped tau range, and auto-pauses on arrival at the
   * end. Returns true if `tauMs` changed.
   */
  tick(realDeltaMs: number): boolean {
    if (!this._playing) return false;
    if (!Number.isFinite(realDeltaMs) || realDeltaMs <= 0) return false;

    const step = Math.min(realDeltaMs, MAX_TICK_MS) * this._rate;
    const before = this._tauMs;
    this._tauMs = clamp(before + step, 0, this.warp.totalTauMs);
    if (this.atEnd) this._playing = false;
    return this._tauMs !== before;
  }
}
