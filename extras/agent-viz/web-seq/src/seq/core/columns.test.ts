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

import type { Column, ColumnNode } from './columns.js';
import { activeLifelineIds, buildForest, computeColumns } from './columns.js';
import type { Lifeline } from './types.js';

/**
 * The fixture forest, in depth-first order:
 *
 *   root
 *   +- a
 *   |  +- a1
 *   |  +- a2
 *   +- b
 *      +- b1
 */
const TREE: Array<[id: string, parent: string | null]> = [
  ['root', null],
  ['a', 'root'],
  ['a1', 'a'],
  ['a2', 'a'],
  ['b', 'root'],
  ['b1', 'b'],
];

function makeLifelines(
  tree: Array<[string, string | null]> = TREE,
): Lifeline[] {
  const byId = new Map<string, string | null>(tree);
  const ancestryOf = (id: string): string[] => {
    const chain: string[] = [];
    const seen = new Set<string>([id]);
    let cursor = byId.get(id) ?? null;
    while (cursor && !seen.has(cursor)) {
      seen.add(cursor);
      chain.unshift(cursor);
      cursor = byId.get(cursor) ?? null;
    }
    return chain;
  };
  return tree.map(([id, parent], order) => {
    const ancestry = ancestryOf(id);
    const base: Lifeline = {
      id,
      name: id.toUpperCase(),
      color: '#888888',
      ancestry,
      depth: ancestry.length,
      order,
      slot: order,
      birthMs: order * 100,
      deathMs: 10_000,
      died: true,
    };
    return parent === null ? base : { ...base, parentId: parent };
  });
}

const OPTS = {
  collapsed: new Set<string>(),
  solo: null,
  columnWidth: 40,
  gap: 10,
  foldedWidth: 8,
};

function ids(columns: Column[]): string[] {
  return columns.map((c) => c.lifelineId);
}

function find(roots: ColumnNode[], id: string): ColumnNode | undefined {
  const stack = [...roots];
  while (stack.length > 0) {
    const n = stack.pop();
    if (!n) break;
    if (n.lifelineId === id) return n;
    stack.push(...n.children);
  }
  return undefined;
}

describe('buildForest', () => {
  it('roots the ancestry forest and counts descendants', () => {
    const roots = buildForest(makeLifelines());
    expect(roots).toHaveLength(1);
    expect(roots[0].lifelineId).toBe('root');
    expect(roots[0].descendantCount).toBe(5);
    expect(find(roots, 'a')?.descendantCount).toBe(2);
    expect(find(roots, 'a1')?.descendantCount).toBe(0);
  });

  it('orders siblings by lifeline.order', () => {
    const roots = buildForest(makeLifelines());
    expect(roots[0].children.map((c) => c.lifelineId)).toEqual(['a', 'b']);
    expect(find(roots, 'a')?.children.map((c) => c.lifelineId)).toEqual([
      'a1',
      'a2',
    ]);
  });

  it('treats a lifeline with a missing parent as a root', () => {
    const lifelines = makeLifelines([
      ['x', 'nobody'],
      ['y', null],
    ]);
    const roots = buildForest(lifelines);
    expect(roots.map((r) => r.lifelineId)).toEqual(['x', 'y']);
  });

  it('does not loop on a cyclic parent chain', () => {
    const lifelines = makeLifelines([
      ['p', 'q'],
      ['q', 'p'],
    ]);
    const roots = buildForest(lifelines);
    // One of the two becomes a root; neither is lost and the walk terminates.
    const seen = new Set<string>();
    const stack = [...roots];
    while (stack.length > 0) {
      const n = stack.pop();
      if (!n) break;
      expect(seen.has(n.lifelineId)).toBe(false);
      seen.add(n.lifelineId);
      stack.push(...n.children);
    }
    expect(seen).toEqual(new Set(['p', 'q']));
  });

  it('handles an empty input', () => {
    expect(buildForest([])).toEqual([]);
  });
});

describe('computeColumns layout', () => {
  it('emits one column per lifeline in depth-first order', () => {
    const layout = computeColumns(makeLifelines(), OPTS);
    expect(ids(layout.columns)).toEqual(['root', 'a', 'a1', 'a2', 'b', 'b1']);
  });

  it('keeps children adjacent to their parent', () => {
    const layout = computeColumns(makeLifelines(), OPTS);
    const order = ids(layout.columns);
    expect(order.indexOf('a1') - order.indexOf('a')).toBe(1);
    expect(order.indexOf('b1') - order.indexOf('b')).toBe(1);
  });

  it('places columns left to right with the requested width and gap', () => {
    const layout = computeColumns(makeLifelines(), OPTS);
    layout.columns.forEach((c, i) => {
      expect(c.width).toBe(40);
      expect(c.x).toBeCloseTo(i * 50 + 20, 9);
    });
    expect(layout.totalWidth).toBe(6 * 40 + 5 * 10);
  });

  it('records rendered depth relative to the rendered roots', () => {
    const layout = computeColumns(makeLifelines(), OPTS);
    const depthOf = (id: string): number | undefined =>
      layout.columns.find((c) => c.lifelineId === id)?.depth;
    expect(depthOf('root')).toBe(0);
    expect(depthOf('a')).toBe(1);
    expect(depthOf('a1')).toBe(2);
  });

  it('maps every lifeline to its own column when expanded', () => {
    const layout = computeColumns(makeLifelines(), OPTS);
    for (const id of ['root', 'a', 'a1', 'a2', 'b', 'b1']) {
      expect(layout.columnFor.get(id)?.lifelineId).toBe(id);
      expect(layout.columnFor.get(id)?.memberIds).toEqual([id]);
    }
  });

  it('produces an empty layout for no lifelines', () => {
    const layout = computeColumns([], OPTS);
    expect(layout.columns).toEqual([]);
    expect(layout.totalWidth).toBe(0);
  });
});

describe('computeColumns collapsing', () => {
  it('absorbs a whole subtree into one composite column', () => {
    const layout = computeColumns(makeLifelines(), {
      ...OPTS,
      collapsed: new Set(['a']),
    });
    expect(ids(layout.columns)).toEqual(['root', 'a', 'b', 'b1']);

    const composite = layout.columns[1];
    expect(composite.collapsed).toBe(true);
    expect(composite.memberIds.sort()).toEqual(['a', 'a1', 'a2']);
    expect(composite.width).toBe(40);
  });

  it('maps every descendant to the composite column', () => {
    const layout = computeColumns(makeLifelines(), {
      ...OPTS,
      collapsed: new Set(['a']),
    });
    const composite = layout.columns[1];
    for (const id of ['a', 'a1', 'a2']) {
      expect(layout.columnFor.get(id)).toBe(composite);
    }
    expect(layout.columnFor.get('b1')).not.toBe(composite);
  });

  it('collapsing the root leaves exactly one column', () => {
    const layout = computeColumns(makeLifelines(), {
      ...OPTS,
      collapsed: new Set(['root']),
    });
    expect(layout.columns).toHaveLength(1);
    expect(layout.columns[0].memberIds).toHaveLength(6);
    for (const id of ['root', 'a', 'a1', 'a2', 'b', 'b1']) {
      expect(layout.columnFor.get(id)).toBe(layout.columns[0]);
    }
  });

  it('collapsing a leaf changes nothing structurally', () => {
    const layout = computeColumns(makeLifelines(), {
      ...OPTS,
      collapsed: new Set(['b1']),
    });
    expect(ids(layout.columns)).toEqual(['root', 'a', 'a1', 'a2', 'b', 'b1']);
    expect(layout.columnFor.get('b1')?.memberIds).toEqual(['b1']);
  });
});

describe('computeColumns solo', () => {
  it('restricts the axis to one subtree', () => {
    const layout = computeColumns(makeLifelines(), { ...OPTS, solo: 'a' });
    expect(ids(layout.columns)).toEqual(['a', 'a1', 'a2']);
    expect(layout.columns[0].depth).toBe(0);
    expect(layout.columnFor.has('root')).toBe(false);
    expect(layout.columnFor.has('b')).toBe(false);
    expect(layout.totalWidth).toBe(3 * 40 + 2 * 10);
  });

  it('composes with collapse', () => {
    const layout = computeColumns(makeLifelines(), {
      ...OPTS,
      solo: 'a',
      collapsed: new Set(['a']),
    });
    expect(layout.columns).toHaveLength(1);
    expect(layout.columns[0].memberIds.sort()).toEqual(['a', 'a1', 'a2']);
  });

  it('renders nothing for an unknown solo id', () => {
    const layout = computeColumns(makeLifelines(), { ...OPTS, solo: 'ghost' });
    expect(layout.columns).toEqual([]);
    expect(layout.columnFor.size).toBe(0);
  });
});

describe('computeColumns auto-folding', () => {
  const activeWindow = { startMs: 0, endMs: 1000 };

  it('narrows an idle subtree without removing it', () => {
    const layout = computeColumns(makeLifelines(), {
      ...OPTS,
      activeWindow,
      activeIds: new Set(['root', 'b1']),
    });
    expect(ids(layout.columns)).toEqual(['root', 'a', 'b', 'b1']);

    const a = layout.columns[1];
    expect(a.folded).toBe(true);
    expect(a.width).toBe(8);
    expect(a.memberIds.sort()).toEqual(['a', 'a1', 'a2']);
    // Absorbed but still addressable: an edge into a1 still lands somewhere.
    expect(layout.columnFor.get('a1')).toBe(a);
  });

  it('folds an idle parent but keeps walking to an active child', () => {
    const layout = computeColumns(makeLifelines(), {
      ...OPTS,
      activeWindow,
      activeIds: new Set(['root', 'b1']),
    });
    const b = layout.columns[2];
    const b1 = layout.columns[3];
    expect(b.folded).toBe(true);
    expect(b.memberIds).toEqual(['b']);
    expect(b1.folded).toBe(false);
    expect(b1.width).toBe(40);
  });

  it('leaves everything full width when everything is active', () => {
    const all = new Set(['root', 'a', 'a1', 'a2', 'b', 'b1']);
    const layout = computeColumns(makeLifelines(), {
      ...OPTS,
      activeWindow,
      activeIds: all,
    });
    expect(layout.columns).toHaveLength(6);
    expect(layout.columns.every((c) => !c.folded)).toBe(true);
  });

  it('folds everything when nothing is active, and keeps the axis intact', () => {
    const layout = computeColumns(makeLifelines(), {
      ...OPTS,
      activeWindow,
      activeIds: new Set<string>(),
    });
    expect(layout.columns).toHaveLength(1);
    expect(layout.columns[0].folded).toBe(true);
    expect(layout.columns[0].width).toBe(8);
    expect(layout.columnFor.size).toBe(6);
  });

  it('ignores activity when no window is supplied', () => {
    const layout = computeColumns(makeLifelines(), {
      ...OPTS,
      activeIds: new Set<string>(),
    });
    expect(layout.columns).toHaveLength(6);
    expect(layout.columns.every((c) => !c.folded)).toBe(true);
  });
});

describe('activeLifelineIds', () => {
  it('includes lifelines with an interval overlapping the window', () => {
    const active = activeLifelineIds(
      [
        { lifelineId: 'a', startMs: 0, endMs: 50 },
        { lifelineId: 'b', startMs: 500, endMs: 600 },
      ],
      [],
      100,
      400,
    );
    expect(active).toEqual(new Set<string>());
  });

  it('includes both ends of an overlapping edge', () => {
    const active = activeLifelineIds(
      [],
      [{ fromId: 'a', toId: 'b', sendMs: 90, recvMs: 110 }],
      100,
      400,
    );
    expect(active).toEqual(new Set(['a', 'b']));
  });
});
