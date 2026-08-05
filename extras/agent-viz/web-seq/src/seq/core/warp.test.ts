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

import { describe, expect, it } from 'vitest';

import type { Warp, WarpKnot } from './types.js';
import { WarpFn, identityWarp } from './warp.js';

/** Build a warp from `[tau, wall]` pairs, deriving each knot's velocity. */
function warpOf(pairs: Array<[number, number]>): Warp {
  const knots: WarpKnot[] = pairs.map(([tauMs, wallMs], i) => {
    const next = pairs[i + 1] ?? pairs[i];
    const prev = pairs[i - 1] ?? pairs[i];
    const dTau = next[0] - prev[0];
    const dWall = next[1] - prev[1];
    return { tauMs, wallMs, velocity: dTau > 0 ? dWall / dTau : 1 };
  });
  const velocities = knots.map((k) => k.velocity);
  return {
    knots,
    totalTauMs: pairs.length > 0 ? pairs[pairs.length - 1][0] : 0,
    minVelocity: velocities.length > 0 ? Math.min(...velocities) : 1,
    maxVelocity: velocities.length > 0 ? Math.max(...velocities) : 1,
  };
}

/** A warp with a slow burst in the middle and fast idle stretches around it. */
const compound = warpOf([
  [0, 0],
  [1000, 60_000],
  [3000, 63_000],
  [4000, 240_000],
]);

describe('WarpFn.wallAt', () => {
  it('interpolates linearly within a segment', () => {
    const w = new WarpFn(compound);
    expect(w.wallAt(0)).toBe(0);
    expect(w.wallAt(500)).toBeCloseTo(30_000, 6);
    expect(w.wallAt(1000)).toBeCloseTo(60_000, 6);
    expect(w.wallAt(2000)).toBeCloseTo(61_500, 6);
    expect(w.wallAt(4000)).toBeCloseTo(240_000, 6);
  });

  it('clamps out-of-range input to the endpoints', () => {
    const w = new WarpFn(compound);
    expect(w.wallAt(-99_999)).toBe(0);
    expect(w.wallAt(99_999)).toBeCloseTo(240_000, 6);
    expect(w.wallAt(Number.NaN)).toBe(0);
  });

  it('is monotonically non-decreasing', () => {
    const w = new WarpFn(compound);
    let prev = -Infinity;
    for (let tau = -100; tau <= 4100; tau += 7) {
      const wall = w.wallAt(tau);
      expect(wall).toBeGreaterThanOrEqual(prev);
      prev = wall;
    }
  });
});

describe('WarpFn round trip', () => {
  it('tauAt(wallAt(t)) === t across many samples', () => {
    const w = new WarpFn(compound);
    for (let tau = 0; tau <= 4000; tau += 3.5) {
      expect(w.tauAt(w.wallAt(tau))).toBeCloseTo(tau, 6);
    }
  });

  it('wallAt(tauAt(wall)) === wall across many samples', () => {
    const w = new WarpFn(compound);
    for (let wall = 0; wall <= 240_000; wall += 137) {
      expect(w.wallAt(w.tauAt(wall))).toBeCloseTo(wall, 6);
    }
  });

  it('survives a thousand-knot warp', () => {
    const pairs: Array<[number, number]> = [];
    let wall = 0;
    for (let i = 0; i < 1000; i++) {
      pairs.push([i * 10, wall]);
      wall += 1 + (i % 17) * 3;
    }
    const w = new WarpFn(warpOf(pairs));
    expect(w.knotCount).toBe(1000);
    for (let i = 0; i < 1000; i += 7) {
      const tau = i * 10 + 4;
      expect(w.tauAt(w.wallAt(tau))).toBeCloseTo(Math.min(tau, 9990), 6);
    }
  });

  it('clamps wall input outside the mapped range', () => {
    const w = new WarpFn(compound);
    expect(w.tauAt(-1)).toBe(0);
    expect(w.tauAt(1e9)).toBe(4000);
  });
});

describe('WarpFn.velocityAt', () => {
  it('is the derivative of wallAt', () => {
    const w = new WarpFn(compound);
    expect(w.velocityAt(500)).toBeCloseTo(60, 9);
    expect(w.velocityAt(2000)).toBeCloseTo(1.5, 9);
    expect(w.velocityAt(3500)).toBeCloseTo(177, 9);
  });

  it('agrees with a numeric difference of wallAt', () => {
    const w = new WarpFn(compound);
    for (const tau of [100, 1500, 2500, 3900]) {
      const numeric = (w.wallAt(tau + 0.5) - w.wallAt(tau - 0.5)) / 1;
      expect(w.velocityAt(tau)).toBeCloseTo(numeric, 6);
    }
  });

  it('clamps to the end segments outside the range', () => {
    const w = new WarpFn(compound);
    expect(w.velocityAt(-500)).toBeCloseTo(60, 9);
    expect(w.velocityAt(500_000)).toBeCloseTo(177, 9);
  });

  it('reports the fastest segment as maxVelocity', () => {
    const w = new WarpFn(compound);
    expect(w.maxVelocity).toBeCloseTo(177, 9);
  });
});

describe('WarpFn degenerate inputs', () => {
  it('handles zero knots', () => {
    const w = new WarpFn({ knots: [], totalTauMs: 500, minVelocity: 1, maxVelocity: 1 });
    expect(w.totalTauMs).toBe(500);
    expect(w.wallAt(123)).toBe(0);
    expect(w.tauAt(123)).toBe(0);
    expect(w.velocityAt(123)).toBe(1);
  });

  it('handles a single knot', () => {
    const w = new WarpFn({
      knots: [{ tauMs: 0, wallMs: 4200, velocity: 3 }],
      totalTauMs: 0,
      minVelocity: 3,
      maxVelocity: 3,
    });
    expect(w.totalTauMs).toBe(0);
    expect(w.wallAt(0)).toBe(4200);
    expect(w.wallAt(1000)).toBe(4200);
    expect(w.tauAt(9999)).toBe(0);
    expect(w.velocityAt(0)).toBe(3);
    expect(w.maxVelocity).toBe(3);
  });

  it('handles a zero-length run', () => {
    const w = new WarpFn(identityWarp(0));
    expect(w.totalTauMs).toBe(0);
    expect(w.wallAt(0)).toBe(0);
    expect(w.wallAt(50)).toBe(0);
    expect(w.tauAt(50)).toBe(0);
    expect(Number.isFinite(w.velocityAt(0))).toBe(true);
  });

  it('repairs unsorted and non-monotonic knots', () => {
    const w = new WarpFn({
      knots: [
        { tauMs: 200, wallMs: 500, velocity: 1 },
        { tauMs: 0, wallMs: 0, velocity: 1 },
        { tauMs: 100, wallMs: 900, velocity: 1 },
      ],
      totalTauMs: 200,
      minVelocity: 1,
      maxVelocity: 1,
    });
    expect(w.totalTauMs).toBe(200);
    let prev = -Infinity;
    for (let tau = 0; tau <= 200; tau += 5) {
      const wall = w.wallAt(tau);
      expect(wall).toBeGreaterThanOrEqual(prev);
      prev = wall;
    }
  });

  it('drops non-finite knots', () => {
    const w = new WarpFn({
      knots: [
        { tauMs: 0, wallMs: 0, velocity: 1 },
        { tauMs: Number.NaN, wallMs: 10, velocity: 1 },
        { tauMs: 100, wallMs: 100, velocity: 1 },
      ],
      totalTauMs: 100,
      minVelocity: 1,
      maxVelocity: 1,
    });
    expect(w.knotCount).toBe(2);
    expect(w.wallAt(50)).toBeCloseTo(50, 9);
  });

  it('keeps a wall plateau single-valued', () => {
    // A zero-velocity stretch: several taus map to the same wall time.
    const w = new WarpFn(
      warpOf([
        [0, 0],
        [100, 100],
        [200, 100],
        [300, 200],
      ]),
    );
    expect(w.wallAt(150)).toBeCloseTo(100, 9);
    expect(w.tauAt(100)).toBe(100);
    expect(w.wallAt(w.tauAt(100))).toBeCloseTo(100, 9);
  });

  it('exposes the mapped wall bounds', () => {
    const w = new WarpFn(compound);
    expect(w.startWallMs).toBe(0);
    expect(w.endWallMs).toBe(240_000);
  });
});

describe('identityWarp', () => {
  it('maps tau to wall one to one', () => {
    const w = new WarpFn(identityWarp(5000));
    expect(w.totalTauMs).toBe(5000);
    expect(w.wallAt(1234)).toBeCloseTo(1234, 9);
    expect(w.tauAt(1234)).toBeCloseTo(1234, 9);
    expect(w.velocityAt(1234)).toBeCloseTo(1, 9);
  });
});
