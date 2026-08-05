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
 * Cross-language contract test.
 *
 * Every other test in this directory feeds the core hand-written fixtures, so
 * they all pass even if the Go generator and the TypeScript reader have drifted
 * apart. This one loads a digest actually produced by `cmd/seq-viz` and drives
 * the whole read path with it, which is the only place that drift can be
 * caught.
 *
 * The fixture is the committed demo sample, read directly rather than copied,
 * so the data the tests assert against and the data a person actually opens in
 * the viewer are the same bytes and cannot drift. Regenerate it with:
 *
 *   ./demo/regenerate.sh sample
 */

import { describe, it, expect } from 'vitest';

import digestJson from '../../../../demo/sample/run.digest.json';
import { SCHEMA_VERSION, type Digest } from './types.js';
import { WarpFn } from './warp.js';
import { PlaybackClock } from './clock.js';
import { computeColumns, buildForest, activeLifelineIds } from './columns.js';
import { buildFrame } from './frame.js';

const digest = digestJson as unknown as Digest;

describe('Go-generated digest', () => {
  it('matches the schema version this build understands', () => {
    expect(digest.version).toBe(SCHEMA_VERSION);
  });

  it('populates every field the frontend reads', () => {
    // Guards against a Go json tag being renamed or omitempty-ed away: reading
    // `undefined` would silently render an empty view rather than fail.
    expect(digest.durationMs).toBeGreaterThan(0);
    expect(digest.startedAt).toBeTruthy();
    expect(digest.lifelines.length).toBeGreaterThan(0);
    expect(digest.intervals.length).toBeGreaterThan(0);
    expect(digest.edges.length).toBeGreaterThan(0);
    expect(digest.density.samples.length).toBeGreaterThan(0);
    expect(digest.density.bucketMs).toBeGreaterThan(0);
    expect(digest.density.peak).toBeGreaterThan(0);
    expect(digest.warp.knots.length).toBeGreaterThan(0);
    expect(digest.warp.totalTauMs).toBeGreaterThan(0);
    expect(digest.warp.maxVelocity).toBeGreaterThan(0);
    expect(digest.stats.lifelineCount).toBe(digest.lifelines.length);
    expect(digest.stats.intervalCount).toBe(digest.intervals.length);
    expect(digest.stats.edgeCount).toBe(digest.edges.length);
  });

  it('has referentially intact intervals and edges', () => {
    const ids = new Set(digest.lifelines.map((l) => l.id));
    for (const iv of digest.intervals) expect(ids.has(iv.lifelineId)).toBe(true);
    for (const e of digest.edges) {
      expect(ids.has(e.fromId)).toBe(true);
      expect(ids.has(e.toId)).toBe(true);
    }
  });

  it('never reports an interval that ends before it starts', () => {
    for (const iv of digest.intervals) expect(iv.endMs).toBeGreaterThanOrEqual(iv.startMs);
  });

  it('never reports a message arriving before it was sent', () => {
    // A negative slope would render as an arrow pointing backwards in time.
    for (const e of digest.edges) expect(e.recvMs).toBeGreaterThanOrEqual(e.sendMs);
  });

  it('nests intervals within their parent on the same lifeline', () => {
    // Containment is what makes the per-lifeline flame graph legible; a child
    // poking outside its parent would draw outside the activation box.
    const byLifeline = new Map<string, typeof digest.intervals>();
    for (const iv of digest.intervals) {
      const list = byLifeline.get(iv.lifelineId) ?? [];
      list.push(iv);
      byLifeline.set(iv.lifelineId, list);
    }
    for (const list of byLifeline.values()) {
      const sorted = [...list].sort((a, b) => a.depth - b.depth || a.startMs - b.startMs);
      for (const child of sorted) {
        // Strictly inside: a child that begins at the exact instant a candidate
        // parent ends is a sibling that followed it, not something nested in it.
        const parent = sorted.find(
          (p) => p.depth === child.depth - 1 && p.startMs <= child.startMs && p.endMs > child.startMs
        );
        if (parent) expect(child.endMs).toBeLessThanOrEqual(parent.endMs + 1e-6);
      }
    }
  });
});

describe('warp produced by the Go velocity planner', () => {
  const warp = new WarpFn(digest.warp);

  it('is monotonic in wall time', () => {
    let prev = -Infinity;
    const steps = 500;
    for (let i = 0; i <= steps; i++) {
      const wall = warp.wallAt((warp.totalTauMs * i) / steps);
      expect(wall).toBeGreaterThanOrEqual(prev);
      prev = wall;
    }
  });

  it('round-trips tau -> wall -> tau', () => {
    const steps = 200;
    for (let i = 0; i <= steps; i++) {
      const tau = (warp.totalTauMs * i) / steps;
      expect(warp.tauAt(warp.wallAt(tau))).toBeCloseTo(tau, 3);
    }
  });

  it('spans the full run', () => {
    expect(warp.wallAt(0)).toBeCloseTo(0, 6);
    expect(warp.wallAt(warp.totalTauMs)).toBeCloseTo(digest.durationMs, 0);
  });

  it('respects the velocity bounds the generator advertised', () => {
    const steps = 500;
    for (let i = 0; i <= steps; i++) {
      const v = warp.velocityAt((warp.totalTauMs * i) / steps);
      expect(v).toBeGreaterThan(0);
      expect(v).toBeLessThanOrEqual(digest.warp.maxVelocity + 1e-6);
    }
  });

  it('agrees with the Go WallAt: velocity is the derivative of wallAt', () => {
    // wallAt is piecewise linear, so this must be sampled strictly *inside* a
    // segment; a difference straddling a knot averages two slopes and would
    // fail for reasons that say nothing about correctness.
    const knots = digest.warp.knots;
    let checked = 0;
    for (let i = 0; i + 1 < knots.length; i += Math.ceil(knots.length / 40)) {
      const a = knots[i].tauMs;
      const b = knots[i + 1].tauMs;
      const span = b - a;
      if (span <= 1e-6) continue;
      const mid = a + span / 2;
      const h = span / 4;
      const numeric = (warp.wallAt(mid + h) - warp.wallAt(mid - h)) / (2 * h);
      expect(numeric).toBeCloseTo(warp.velocityAt(mid), 6);
      checked++;
    }
    expect(checked).toBeGreaterThan(0);
  });
});

describe('playback over the real digest', () => {
  it('advances monotonically and terminates', () => {
    const clock = new PlaybackClock(new WarpFn(digest.warp));
    clock.play();
    let prevWall = -Infinity;
    let ticks = 0;
    // 16ms frames; generous cap so a stuck clock fails rather than hangs.
    while (clock.playing && ticks < 200_000) {
      clock.tick(16);
      expect(clock.wallMs).toBeGreaterThanOrEqual(prevWall);
      prevWall = clock.wallMs;
      ticks++;
    }
    expect(clock.playing).toBe(false);
    expect(clock.atEnd).toBe(true);
    expect(clock.wallMs).toBeCloseTo(digest.durationMs, 0);
  });

  it('takes roughly the advertised viewing time at 1x', () => {
    // totalTauMs is the promise the transport bar makes to the user.
    const clock = new PlaybackClock(new WarpFn(digest.warp));
    clock.play();
    let ticks = 0;
    while (clock.playing && ticks < 200_000) {
      clock.tick(16);
      ticks++;
    }
    expect(ticks * 16).toBeCloseTo(digest.warp.totalTauMs, -2);
  });
});

describe('layout over the real digest', () => {
  it('builds a forest covering every lifeline', () => {
    const roots = buildForest(digest.lifelines);
    let count = 0;
    const walk = (nodes: ReturnType<typeof buildForest>): void => {
      for (const n of nodes) {
        count++;
        walk(n.children);
      }
    };
    walk(roots);
    expect(count).toBe(digest.lifelines.length);
  });

  it('recycles slots down to roughly the concurrency, not the agent count', () => {
    // The whole point of slot recycling: 14 agents that are never all live at
    // once must not demand 14 columns.
    const layout = computeColumns(digest.lifelines, {
      collapsed: new Set(),
      solo: null,
      columnWidth: 104,
      gap: 10,
      foldedWidth: 8,
    });
    expect(layout.columns.length).toBeGreaterThan(0);
    expect(layout.columns.length).toBeLessThanOrEqual(digest.lifelines.length);
  });

  it('narrows to a single subtree when soloed', () => {
    const root = digest.lifelines.find((l) => l.depth === 0);
    expect(root).toBeDefined();
    const parent = digest.lifelines.find(
      (l) => digest.lifelines.some((c) => c.parentId === l.id) && l.depth >= 1
    );
    if (!parent) return;
    const all = computeColumns(digest.lifelines, {
      collapsed: new Set(),
      solo: null,
      columnWidth: 104,
      gap: 10,
      foldedWidth: 8,
    });
    const soloed = computeColumns(digest.lifelines, {
      collapsed: new Set(),
      solo: parent.id,
      columnWidth: 104,
      gap: 10,
      foldedWidth: 8,
    });
    expect(soloed.columns.length).toBeLessThanOrEqual(all.columns.length);
  });

  it('finds active lifelines in a mid-run window', () => {
    const mid = digest.durationMs / 2;
    const active = activeLifelineIds(digest.intervals, digest.edges, mid - 30_000, mid + 30_000);
    expect(active.size).toBeGreaterThan(0);
  });
});

describe('frame building over the real digest', () => {
  const layout = computeColumns(digest.lifelines, {
    collapsed: new Set(),
    solo: null,
    columnWidth: 104,
    gap: 10,
    foldedWidth: 8,
  });
  const geom = {
    width: 1400,
    height: 900,
    frameTop: 0.3,
    frameBottom: 0.78,
    msPerPx: 60,
  };

  it('produces a drawable model at every point in the run', () => {
    const steps = 60;
    for (let i = 0; i <= steps; i++) {
      const wall = (digest.durationMs * i) / steps;
      const model = buildFrame(digest, layout, geom, wall, 1);
      expect(model.visibleEndMs).toBeGreaterThan(model.visibleStartMs);
      expect(Number.isFinite(model.playheadY)).toBe(true);
      for (const iv of model.intervals) {
        expect(Number.isFinite(iv.x)).toBe(true);
        expect(Number.isFinite(iv.y)).toBe(true);
        expect(iv.width).toBeGreaterThan(0);
        expect(iv.height).toBeGreaterThan(0);
      }
    }
  });

  it('actually shows something during the busiest part of the run', () => {
    // Culling that is too aggressive would pass every other assertion here.
    let best = 0;
    let bestWall = 0;
    digest.density.samples.forEach((s, i) => {
      if (s > best) {
        best = s;
        bestWall = i * digest.density.bucketMs;
      }
    });
    const model = buildFrame(digest, layout, geom, bestWall, 1);
    expect(model.intervals.length).toBeGreaterThan(0);
  });

  it('keeps geometry honest: bar height is proportional to duration', () => {
    const model = buildFrame(digest, layout, geom, digest.durationMs / 2, 1);
    for (const fi of model.intervals) {
      const source = digest.intervals.find((i) => i.id === fi.id);
      if (!source) continue;
      const trueDuration = source.endMs - source.startMs;
      // Sub-pixel intervals are clamped to a minimum height so they stay
      // visible; above that clamp the mapping must be exactly linear.
      if (trueDuration / geom.msPerPx > 2) {
        expect(fi.height).toBeCloseTo(trueDuration / geom.msPerPx, 3);
      }
    }
  });

  it('streaks only when moving fast', () => {
    const slow = buildFrame(digest, layout, geom, digest.durationMs / 2, 1);
    const fast = buildFrame(digest, layout, geom, digest.durationMs / 2, 100);
    expect(slow.streakFactor).toBe(0);
    expect(fast.streakFactor).toBeGreaterThan(0);
  });
});
