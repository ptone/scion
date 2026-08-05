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

import type { ColumnLayout } from './columns.js';
import { computeColumns } from './columns.js';
import type { FrameGeometry, FrameModel } from './frame.js';
import { buildFrame, resolveGeometry, streakFactorFor } from './frame.js';
import { hitTest } from './render.js';
import type {
  Confidence,
  Digest,
  Edge,
  EdgeKind,
  Interval,
  IntervalKind,
  Lifeline,
} from './types.js';

/* -------------------------------------------------------------------------- */
/* Fixtures                                                                   */
/* -------------------------------------------------------------------------- */

interface LlSpec {
  id: string;
  parent?: string;
  birthMs?: number;
  deathMs?: number;
  died?: boolean;
}

interface IvSpec {
  id: string;
  lifelineId: string;
  startMs: number;
  endMs: number;
  depth?: number;
  kind?: IntervalKind;
  confidence?: Confidence;
  label?: string;
}

interface EdgeSpec {
  id: string;
  fromId: string;
  toId: string;
  sendMs: number;
  recvMs: number;
  kind?: EdgeKind;
  recvConfidence?: Confidence;
}

function makeLifelines(specs: LlSpec[]): Lifeline[] {
  const parentOf = new Map<string, string | undefined>(
    specs.map((s) => [s.id, s.parent]),
  );
  return specs.map((s, order) => {
    const ancestry: string[] = [];
    const seen = new Set<string>([s.id]);
    let cursor = parentOf.get(s.id);
    while (cursor !== undefined && !seen.has(cursor)) {
      seen.add(cursor);
      ancestry.unshift(cursor);
      cursor = parentOf.get(cursor);
    }
    const base: Lifeline = {
      id: s.id,
      name: s.id.toUpperCase(),
      color: '#4488cc',
      ancestry,
      depth: ancestry.length,
      order,
      slot: order,
      birthMs: s.birthMs ?? 0,
      deathMs: s.deathMs ?? 10_000,
      died: s.died ?? true,
    };
    return s.parent === undefined ? base : { ...base, parentId: s.parent };
  });
}

function makeDigest(
  lifelines: LlSpec[],
  intervals: IvSpec[],
  edges: EdgeSpec[],
  durationMs = 10_000,
): Digest {
  const ivs: Interval[] = intervals.map((s) => {
    const base: Interval = {
      id: s.id,
      lifelineId: s.lifelineId,
      kind: s.kind ?? 'turn',
      depth: s.depth ?? 0,
      startMs: s.startMs,
      endMs: s.endMs,
      confidence: s.confidence ?? 'measured',
    };
    return s.label === undefined ? base : { ...base, label: s.label };
  });
  const es: Edge[] = edges.map((s) => ({
    id: s.id,
    kind: s.kind ?? 'message',
    fromId: s.fromId,
    toId: s.toId,
    sendMs: s.sendMs,
    recvMs: s.recvMs,
    recvConfidence: s.recvConfidence ?? 'inferred',
  }));
  return {
    version: 1,
    projectId: 'p',
    projectName: 'P',
    startedAt: '2026-01-01T00:00:00Z',
    endedAt: '2026-01-01T00:00:10Z',
    durationMs,
    lifelines: makeLifelines(lifelines),
    intervals: ivs,
    edges: es,
    density: { bucketMs: 1000, samples: [], peak: 0 },
    warp: { knots: [], totalTauMs: durationMs, minVelocity: 1, maxVelocity: 1 },
    stats: {
      lifelineCount: lifelines.length,
      intervalCount: ivs.length,
      edgeCount: es.length,
      maxConcurrent: lifelines.length,
      measuredIntervals: 0,
      inferredIntervals: 0,
      openIntervals: 0,
      inferredEdges: 0,
      compressionRatio: 1,
    },
  };
}

/**
 * 300x400 canvas, frame band from y=100 to y=300, 10 wall-ms per pixel.
 * With the playhead at 5000ms the canvas spans wall 2000..6000.
 */
const GEOM: FrameGeometry = {
  width: 300,
  height: 400,
  frameTop: 0.25,
  frameBottom: 0.75,
  msPerPx: 10,
};

const PLAYHEAD = 5000;

/** Screen y for a wall time under {@link GEOM} and {@link PLAYHEAD}. */
function yOf(wallMs: number): number {
  return 300 + (wallMs - PLAYHEAD) / 10;
}

function layoutOf(
  digest: Digest,
  overrides: Partial<Parameters<typeof computeColumns>[1]> = {},
): ColumnLayout {
  return computeColumns(digest.lifelines, {
    collapsed: new Set<string>(),
    solo: null,
    columnWidth: 40,
    gap: 10,
    foldedWidth: 8,
    ...overrides,
  });
}

function frameOf(
  digest: Digest,
  layout: ColumnLayout = layoutOf(digest),
  velocity = 1,
  geom: FrameGeometry = GEOM,
): FrameModel {
  return buildFrame(digest, layout, geom, PLAYHEAD, velocity);
}

const TWO_AGENTS: LlSpec[] = [{ id: 'a' }, { id: 'b', parent: 'a' }];

/* -------------------------------------------------------------------------- */
/* Geometry                                                                   */
/* -------------------------------------------------------------------------- */

describe('resolveGeometry', () => {
  it('fills in the optional knobs and precomputes the zone boundaries', () => {
    const g = resolveGeometry(GEOM);
    expect(g.frameTopPx).toBe(100);
    expect(g.frameBottomPx).toBe(300);
    expect(g.intervalInsetPx).toBe(3);
    expect(g.maxColumnSpan).toBe(8);
  });

  it('repairs nonsense input', () => {
    const g = resolveGeometry({
      width: 0,
      height: 0,
      frameTop: -1,
      frameBottom: 5,
      msPerPx: 0,
    });
    expect(g.width).toBe(1);
    expect(g.height).toBe(1);
    expect(g.frameTop).toBe(0);
    expect(g.frameBottom).toBe(1);
    expect(g.msPerPx).toBe(1);
  });
});

describe('buildFrame honest geometry', () => {
  it('draws an interval twice as long exactly twice as tall', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [
        { id: 'short', lifelineId: 'a', startMs: 3000, endMs: 4000 },
        { id: 'long', lifelineId: 'b', startMs: 3000, endMs: 5000 },
      ],
      [],
    );
    const model = frameOf(digest);
    const short = model.intervals.find((i) => i.id === 'short');
    const long = model.intervals.find((i) => i.id === 'long');
    expect(short?.height).toBeCloseTo(100, 9);
    expect(long?.height).toBeCloseTo(200, 9);
    expect((long?.height ?? 0) / (short?.height ?? 1)).toBeCloseTo(2, 9);
  });

  it('uses the same scale in every zone', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [
        { id: 'wake', lifelineId: 'a', startMs: 2200, endMs: 2700 },
        { id: 'frame', lifelineId: 'a', startMs: 4000, endMs: 4500 },
        { id: 'staging', lifelineId: 'b', startMs: 5300, endMs: 5800 },
      ],
      [],
    );
    const model = frameOf(digest);
    for (const iv of model.intervals) expect(iv.height).toBeCloseTo(50, 9);
  });

  it('places time flowing upward with the playhead at the frame bottom', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [{ id: 'now', lifelineId: 'a', startMs: 4000, endMs: 5000 }],
      [],
    );
    const model = frameOf(digest);
    expect(model.playheadY).toBe(300);
    const iv = model.intervals[0];
    expect(iv.y).toBeCloseTo(yOf(4000), 9);
    expect(iv.y + iv.height).toBeCloseTo(yOf(5000), 9);
    expect(model.visibleStartMs).toBeCloseTo(2000, 9);
    expect(model.visibleEndMs).toBeCloseTo(6000, 9);
  });

  it('never lets a zero-duration interval vanish entirely', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [{ id: 'blip', lifelineId: 'a', startMs: 4000, endMs: 4000 }],
      [],
    );
    const model = frameOf(digest);
    expect(model.intervals[0].height).toBeGreaterThan(0);
  });
});

describe('buildFrame zones', () => {
  it('assigns wake, frame and staging by position', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [
        { id: 'wake', lifelineId: 'a', startMs: 2300, endMs: 2400 },
        { id: 'frame', lifelineId: 'a', startMs: 4000, endMs: 4100 },
        { id: 'staging', lifelineId: 'b', startMs: 5400, endMs: 5500 },
      ],
      [],
    );
    const model = frameOf(digest);
    const zoneOf = (id: string): string | undefined =>
      model.intervals.find((i) => i.id === id)?.zone;
    expect(zoneOf('wake')).toBe('wake');
    expect(zoneOf('frame')).toBe('frame');
    expect(zoneOf('staging')).toBe('staging');
  });

  it('fades the wake out toward the top of the canvas', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [
        { id: 'deep', lifelineId: 'a', startMs: 2050, endMs: 2100 },
        { id: 'shallow', lifelineId: 'a', startMs: 2800, endMs: 2850 },
        { id: 'inframe', lifelineId: 'a', startMs: 4000, endMs: 4100 },
      ],
      [],
    );
    const model = frameOf(digest);
    const op = (id: string): number =>
      model.intervals.find((i) => i.id === id)?.opacity ?? -1;
    expect(op('deep')).toBeLessThan(op('shallow'));
    expect(op('shallow')).toBeLessThan(1);
    expect(op('inframe')).toBe(1);
  });
});

describe('buildFrame culling', () => {
  it('excludes intervals outside the visible wall-time range', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [
        { id: 'past', lifelineId: 'a', startMs: 0, endMs: 500 },
        { id: 'future', lifelineId: 'a', startMs: 8000, endMs: 9000 },
        { id: 'visible', lifelineId: 'a', startMs: 4000, endMs: 4500 },
        { id: 'spanning', lifelineId: 'b', startMs: 0, endMs: 9000 },
      ],
      [],
    );
    const model = frameOf(digest);
    const drawn = model.intervals.map((i) => i.id).sort();
    expect(drawn).toEqual(['spanning', 'visible']);
    expect(model.counts.intervalsDrawn).toBe(2);
  });

  it('marks a spanning interval as clipped at both ends', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [{ id: 'spanning', lifelineId: 'a', startMs: 0, endMs: 9000 }],
      [],
    );
    const iv = frameOf(digest).intervals[0];
    expect(iv.clippedTop).toBe(true);
    expect(iv.clippedBottom).toBe(true);
  });

  it('skips intervals whose lifeline is not rendered', () => {
    const digest = makeDigest(
      [{ id: 'a' }, { id: 'b', parent: 'a' }, { id: 'c', parent: 'a' }],
      [
        { id: 'iv-b', lifelineId: 'b', startMs: 4000, endMs: 4500 },
        { id: 'iv-c', lifelineId: 'c', startMs: 4000, endMs: 4500 },
      ],
      [],
    );
    const model = frameOf(digest, layoutOf(digest, { solo: 'b' }));
    expect(model.intervals.map((i) => i.id)).toEqual(['iv-b']);
  });

  it('does not even consider far-away intervals', () => {
    const many: IvSpec[] = [];
    for (let i = 0; i < 500; i++) {
      many.push({
        id: `iv-${i}`,
        lifelineId: 'a',
        startMs: i * 100,
        endMs: i * 100 + 50,
      });
    }
    const digest = makeDigest(TWO_AGENTS, many, [], 50_000);
    const model = frameOf(digest);
    expect(model.counts.intervalsConsidered).toBeLessThan(100);
    expect(model.counts.intervalsDrawn).toBeGreaterThan(0);
  });

  it('excludes edges entirely outside the range', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [],
      [
        { id: 'past', fromId: 'a', toId: 'b', sendMs: 100, recvMs: 200 },
        { id: 'now', fromId: 'a', toId: 'b', sendMs: 4000, recvMs: 4200 },
        { id: 'future', fromId: 'a', toId: 'b', sendMs: 9000, recvMs: 9100 },
      ],
    );
    const model = frameOf(digest);
    expect(model.edges.map((e) => e.id)).toEqual(['now']);
    expect(model.stubs).toHaveLength(0);
  });
});

describe('buildFrame edges', () => {
  it('resolves a sloped segment whose slope is the delivery latency', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [],
      [{ id: 'm', fromId: 'a', toId: 'b', sendMs: 4000, recvMs: 4500 }],
    );
    const layout = layoutOf(digest);
    const model = frameOf(digest, layout);
    const e = model.edges[0];
    expect(e.x1).toBe(layout.columnFor.get('a')?.x);
    expect(e.x2).toBe(layout.columnFor.get('b')?.x);
    expect(e.y1).toBeCloseTo(yOf(4000), 9);
    expect(e.y2).toBeCloseTo(yOf(4500), 9);
    expect(e.y2 - e.y1).toBeCloseTo(50, 9);
    expect(e.latencyMs).toBe(500);
  });

  it('draws an open-arrival edge horizontally', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [],
      [
        {
          id: 'm',
          fromId: 'a',
          toId: 'b',
          sendMs: 4000,
          recvMs: 4000,
          recvConfidence: 'open',
        },
      ],
    );
    const e = frameOf(digest).edges[0];
    expect(e.recvConfidence).toBe('open');
    expect(e.y1).toBeCloseTo(e.y2, 9);
  });

  it('drops an edge whose two ends are absorbed into one column', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [],
      [{ id: 'm', fromId: 'a', toId: 'b', sendMs: 4000, recvMs: 4200 }],
    );
    const model = frameOf(digest, layoutOf(digest, { collapsed: new Set(['a']) }));
    expect(model.edges).toHaveLength(0);
    expect(model.stubs).toHaveLength(0);
    expect(model.counts.edgesInternal).toBe(1);
  });
});

describe('buildFrame edge stubs', () => {
  it('stubs an arrival that has not scrolled into view yet', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [],
      [{ id: 'm', fromId: 'a', toId: 'b', sendMs: 4000, recvMs: 9000 }],
    );
    const model = frameOf(digest);
    expect(model.edges).toHaveLength(0);
    expect(model.stubs).toHaveLength(1);
    const s = model.stubs[0];
    expect(s.reason).toBe('offscreen');
    expect(s.direction).toBe('outgoing');
    expect(s.side).toBe('bottom');
    expect(s.label).toBe('B');
    expect(s.y).toBeCloseTo(yOf(4000), 9);
    expect(s.tipY).toBeCloseTo(400, 9);
  });

  it('stubs a send that has already scrolled off the top', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [],
      [{ id: 'm', fromId: 'a', toId: 'b', sendMs: 1000, recvMs: 4000 }],
    );
    const model = frameOf(digest);
    expect(model.edges).toHaveLength(0);
    expect(model.stubs).toHaveLength(1);
    const s = model.stubs[0];
    expect(s.reason).toBe('offscreen');
    expect(s.direction).toBe('incoming');
    expect(s.side).toBe('top');
    expect(s.label).toBe('A');
    expect(s.tipY).toBeCloseTo(0, 9);
  });

  it('stubs a peer whose lifeline is not rendered at all', () => {
    const digest = makeDigest(
      [{ id: 'root' }, { id: 'a', parent: 'root' }, { id: 'b', parent: 'root' }],
      [],
      [{ id: 'm', fromId: 'a', toId: 'b', sendMs: 4000, recvMs: 4200 }],
    );
    const model = frameOf(digest, layoutOf(digest, { solo: 'a' }));
    expect(model.edges).toHaveLength(0);
    expect(model.stubs).toHaveLength(1);
    expect(model.stubs[0].reason).toBe('hidden');
    expect(model.stubs[0].otherId).toBe('b');
    expect(model.stubs[0].label).toBe('B');
  });

  it('stubs both ends of a horizontally distant edge', () => {
    const specs: LlSpec[] = [{ id: 'root' }];
    for (let i = 0; i < 11; i++) specs.push({ id: `c${i}`, parent: 'root' });
    const digest = makeDigest(
      specs,
      [],
      [{ id: 'm', fromId: 'root', toId: 'c10', sendMs: 4000, recvMs: 4200 }],
    );
    const model = frameOf(digest);
    expect(model.edges).toHaveLength(0);
    expect(model.stubs).toHaveLength(2);
    expect(model.stubs.every((s) => s.reason === 'distant')).toBe(true);
    expect(model.stubs.map((s) => s.side).sort()).toEqual(['left', 'right']);
    expect(model.stubs.map((s) => s.direction).sort()).toEqual([
      'incoming',
      'outgoing',
    ]);
  });

  it('draws a full edge when the columns are within the span threshold', () => {
    const specs: LlSpec[] = [{ id: 'root' }];
    for (let i = 0; i < 11; i++) specs.push({ id: `c${i}`, parent: 'root' });
    const digest = makeDigest(
      specs,
      [],
      [{ id: 'm', fromId: 'root', toId: 'c7', sendMs: 4000, recvMs: 4200 }],
    );
    const model = frameOf(digest);
    expect(model.edges).toHaveLength(1);
    expect(model.stubs).toHaveLength(0);
  });

  it('keeps the true slope in the stub tip, so latency survives cropping', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [],
      [{ id: 'm', fromId: 'a', toId: 'b', sendMs: 4000, recvMs: 9000 }],
    );
    const layout = layoutOf(digest);
    const s = frameOf(digest, layout).stubs[0];
    const ax = layout.columnFor.get('a')?.x ?? 0;
    const bx = layout.columnFor.get('b')?.x ?? 0;
    // The tip lies on the true line from send to (recipient column, recv).
    const t = (400 - yOf(4000)) / (yOf(9000) - yOf(4000));
    expect(s.tipX).toBeCloseTo(ax + (bx - ax) * t, 6);
  });

  it('keeps every stub tip inside the canvas', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [],
      [
        { id: 'm1', fromId: 'a', toId: 'b', sendMs: 4000, recvMs: 9000 },
        { id: 'm2', fromId: 'a', toId: 'b', sendMs: 1000, recvMs: 4000 },
      ],
    );
    const model = frameOf(digest);
    for (const s of model.stubs) {
      expect(s.tipX).toBeGreaterThanOrEqual(0);
      expect(s.tipX).toBeLessThanOrEqual(300);
      expect(s.tipY).toBeGreaterThanOrEqual(0);
      expect(s.tipY).toBeLessThanOrEqual(400);
    }
  });
});

describe('buildFrame nesting', () => {
  it('insets nested intervals so the column reads as a flame graph', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [
        { id: 'session', lifelineId: 'a', startMs: 3000, endMs: 5000, depth: 0 },
        { id: 'turn', lifelineId: 'a', startMs: 3500, endMs: 4500, depth: 1 },
        { id: 'tool', lifelineId: 'a', startMs: 3700, endMs: 4000, depth: 2 },
      ],
      [],
    );
    const model = frameOf(digest);
    const w = (id: string): number =>
      model.intervals.find((i) => i.id === id)?.width ?? 0;
    expect(w('session')).toBe(40);
    expect(w('turn')).toBe(34);
    expect(w('tool')).toBe(28);
    // Shallow first, so the renderer paints the deepest last.
    expect(model.intervals.map((i) => i.id)).toEqual(['session', 'turn', 'tool']);
  });

  it('insets an absorbed lifeline further inside a collapsed column', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [
        { id: 'own', lifelineId: 'a', startMs: 4000, endMs: 4500, depth: 0 },
        { id: 'child', lifelineId: 'b', startMs: 4000, endMs: 4500, depth: 0 },
      ],
      [],
    );
    const model = frameOf(digest, layoutOf(digest, { collapsed: new Set(['a']) }));
    const own = model.intervals.find((i) => i.id === 'own');
    const child = model.intervals.find((i) => i.id === 'child');
    expect(own?.columnId).toBe('a');
    expect(child?.columnId).toBe('a');
    expect(child?.insetLevel).toBe(1);
    expect(child?.width).toBeLessThan(own?.width ?? 0);
  });

  it('never insets a folded stripe out of existence', () => {
    const digest = makeDigest(
      TWO_AGENTS,
      [{ id: 'deep', lifelineId: 'a', startMs: 4000, endMs: 4500, depth: 6 }],
      [],
    );
    const model = frameOf(
      digest,
      layoutOf(digest, {
        activeWindow: { startMs: 0, endMs: 10_000 },
        activeIds: new Set(['a']),
      }),
    );
    expect(model.intervals[0].width).toBeGreaterThan(0);
  });
});

describe('buildFrame lifeline rules', () => {
  it('resolves birth and death to screen positions', () => {
    const digest = makeDigest(
      [{ id: 'a', birthMs: 3000, deathMs: 4500, died: true }],
      [],
      [],
    );
    const rule = frameOf(digest).lifelines[0];
    expect(rule.yBirth).toBeCloseTo(yOf(3000), 9);
    expect(rule.yDeath).toBeCloseTo(yOf(4500), 9);
    expect(rule.birthOnScreen).toBe(true);
    expect(rule.deathOnScreen).toBe(true);
    expect(rule.alive).toBe(false);
    expect(rule.visible).toBe(true);
  });

  it('reports a lifeline still running at the playhead as alive', () => {
    const digest = makeDigest(
      [{ id: 'a', birthMs: 3000, deathMs: 9000, died: false }],
      [],
      [],
    );
    const rule = frameOf(digest).lifelines[0];
    expect(rule.alive).toBe(true);
    expect(rule.died).toBe(false);
    expect(rule.deathOnScreen).toBe(false);
  });

  it('summarises a composite column', () => {
    const digest = makeDigest(TWO_AGENTS, [], []);
    const model = frameOf(digest, layoutOf(digest, { collapsed: new Set(['a']) }));
    expect(model.lifelines).toHaveLength(1);
    expect(model.lifelines[0].memberCount).toBe(2);
    expect(model.lifelines[0].label).toBe('A +1');
  });
});

describe('streakFactorFor', () => {
  it('is zero at or below real-ish time', () => {
    expect(streakFactorFor(0)).toBe(0);
    expect(streakFactorFor(1)).toBe(0);
    expect(streakFactorFor(2)).toBe(0);
  });

  it('saturates at high velocity', () => {
    expect(streakFactorFor(64)).toBe(1);
    expect(streakFactorFor(10_000)).toBe(1);
  });

  it('rises monotonically with velocity', () => {
    let prev = -1;
    for (let v = 0; v <= 128; v += 0.5) {
      const f = streakFactorFor(v);
      expect(f).toBeGreaterThanOrEqual(prev);
      expect(f).toBeGreaterThanOrEqual(0);
      expect(f).toBeLessThanOrEqual(1);
      prev = f;
    }
  });

  it('is strictly between the bounds in the middle', () => {
    const mid = streakFactorFor(12);
    expect(mid).toBeGreaterThan(0);
    expect(mid).toBeLessThan(1);
  });

  it('reaches the model as streakFactor', () => {
    const digest = makeDigest(TWO_AGENTS, [], []);
    expect(frameOf(digest, layoutOf(digest), 1).streakFactor).toBe(0);
    expect(frameOf(digest, layoutOf(digest), 1000).streakFactor).toBe(1);
    expect(frameOf(digest, layoutOf(digest), 8).velocity).toBe(8);
  });

  it('ignores nonsense velocity', () => {
    expect(streakFactorFor(Number.NaN)).toBe(0);
    expect(streakFactorFor(-5)).toBe(0);
  });
});

describe('hitTest', () => {
  const digest = makeDigest(
    TWO_AGENTS,
    [
      { id: 'session', lifelineId: 'a', startMs: 3000, endMs: 5000, depth: 0 },
      { id: 'tool', lifelineId: 'a', startMs: 3500, endMs: 4500, depth: 1 },
    ],
    [{ id: 'm', fromId: 'a', toId: 'b', sendMs: 4800, recvMs: 4900 }],
  );

  it('returns the deepest interval under the point', () => {
    const model = frameOf(digest);
    const hit = hitTest(model, 20, yOf(4000));
    expect(hit?.type).toBe('interval');
    expect(hit?.type === 'interval' && hit.interval.id).toBe('tool');
  });

  it('returns the enclosing interval outside the nested one', () => {
    const model = frameOf(digest);
    const hit = hitTest(model, 20, yOf(3100));
    expect(hit?.type === 'interval' && hit.interval.id).toBe('session');
  });

  it('returns an edge when no interval is under the point', () => {
    const model = frameOf(digest);
    const e = model.edges[0];
    const hit = hitTest(model, (e.x1 + e.x2) / 2, (e.y1 + e.y2) / 2);
    expect(hit?.type).toBe('edge');
    expect(hit?.type === 'edge' && hit.edge.id).toBe('m');
  });

  it('returns null over empty space', () => {
    const model = frameOf(digest);
    expect(hitTest(model, 299, 399)).toBeNull();
  });
});
