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
 * The horizontal axis: lifelines as a *tree* of columns rather than a flat row
 * of actors.
 *
 * A run with 50-100 agents cannot be shown as 100 side-by-side columns and stay
 * readable. Because lifelines carry their spawn ancestry, the axis can be a
 * forest instead: collapsing a parent absorbs its entire subtree into one
 * composite column, and every edge that pointed into the subtree still resolves
 * - to the composite - so nothing silently disappears.
 *
 * Two independent mechanisms narrow the axis:
 *  - **collapse** (user intent): a subtree becomes one full-width column;
 *  - **fold** (automatic): a subtree with no activity in the current window
 *    shrinks to a narrow stripe. It is *not* removed, because a column that
 *    vanishes takes the viewer's mental model of "who exists" with it.
 */

import type { Lifeline } from './types.js';

/** A node of the lifeline ancestry forest. */
export interface ColumnNode {
  lifelineId: string;
  lifeline: Lifeline;
  children: ColumnNode[];
  /** Total nodes strictly below this one. */
  descendantCount: number;
}

/** One rendered column of the axis. */
export interface Column {
  /** Lifeline whose column this is (the subtree root when collapsed). */
  lifelineId: string;
  lifeline: Lifeline;
  /** All lifeline ids this column renders, = [lifelineId] when expanded. */
  memberIds: string[];
  collapsed: boolean;
  /**
   * Auto-folded: idle in the active window, drawn as a narrow stripe. Distinct
   * from `collapsed`, which is a user decision and survives window changes.
   */
  folded: boolean;
  /** Centre x in px. */
  x: number;
  width: number;
  /** Nesting depth *within the rendered forest*; 0 for rendered roots. */
  depth: number;
}

export interface ColumnLayout {
  columns: Column[];
  /** lifelineId -> the column that currently renders it (absorbed or not). */
  columnFor: Map<string, Column>;
  totalWidth: number;
}

export interface ColumnOptions {
  collapsed: ReadonlySet<string>;
  /** When set, render only this subtree. */
  solo: string | null;
  /** Hide lifelines with no activity in this wall-time window. */
  activeWindow?: { startMs: number; endMs: number };
  /** Lifelines active in the window (caller-computed), required if activeWindow set. */
  activeIds?: ReadonlySet<string>;
  columnWidth: number;
  gap: number;
  /** Idle subtrees collapse to this narrow stripe instead of disappearing. */
  foldedWidth: number;
}

/**
 * Build the ancestry forest.
 *
 * Roots are lifelines with no `parentId`, plus any whose parent is missing from
 * the input (a partial digest must still render). Siblings are ordered by
 * `lifeline.order`, which the digest already emits in depth-first order, so a
 * depth-first walk of this forest reproduces the digest's intended layout and
 * keeps parents adjacent to their children.
 */
export function buildForest(lifelines: Lifeline[]): ColumnNode[] {
  const nodes = new Map<string, ColumnNode>();
  for (const l of lifelines) {
    nodes.set(l.id, {
      lifelineId: l.id,
      lifeline: l,
      children: [],
      descendantCount: 0,
    });
  }

  const roots: ColumnNode[] = [];
  for (const l of lifelines) {
    const node = nodes.get(l.id);
    if (!node) continue;
    const parent =
      l.parentId !== undefined && l.parentId !== l.id
        ? nodes.get(l.parentId)
        : undefined;
    if (parent && !isAncestorOf(node, parent, nodes)) {
      parent.children.push(node);
    } else {
      roots.push(node);
    }
  }

  const byOrder = (a: ColumnNode, b: ColumnNode): number =>
    a.lifeline.order - b.lifeline.order ||
    a.lifelineId.localeCompare(b.lifelineId);
  roots.sort(byOrder);
  for (const node of nodes.values()) node.children.sort(byOrder);

  // Post-order accumulation of subtree sizes, iteratively (deep spawn chains
  // are plausible and recursion here is avoidable).
  const order: ColumnNode[] = [];
  const stack = [...roots];
  while (stack.length > 0) {
    const n = stack.pop();
    if (!n) break;
    order.push(n);
    for (const c of n.children) stack.push(c);
  }
  for (let i = order.length - 1; i >= 0; i--) {
    const n = order[i];
    let count = 0;
    for (const c of n.children) count += 1 + c.descendantCount;
    n.descendantCount = count;
  }

  return roots;
}

/**
 * Guard against a cyclic `parentId` chain in a malformed digest: attaching
 * `candidateParent` under `node` would create a cycle if `node` is already on
 * `candidateParent`'s ancestry path.
 */
function isAncestorOf(
  node: ColumnNode,
  candidateParent: ColumnNode,
  nodes: Map<string, ColumnNode>,
): boolean {
  let cursor: ColumnNode | undefined = candidateParent;
  const seen = new Set<string>();
  while (cursor) {
    if (cursor.lifelineId === node.lifelineId) return true;
    if (seen.has(cursor.lifelineId)) return true;
    seen.add(cursor.lifelineId);
    const pid: string | undefined = cursor.lifeline.parentId;
    cursor = pid !== undefined ? nodes.get(pid) : undefined;
  }
  return false;
}

/** Every lifeline id in the subtree rooted at `node`, including the root. */
function subtreeIds(node: ColumnNode): string[] {
  const out: string[] = [];
  const stack: ColumnNode[] = [node];
  while (stack.length > 0) {
    const n = stack.pop();
    if (!n) break;
    out.push(n.lifelineId);
    for (let i = n.children.length - 1; i >= 0; i--) stack.push(n.children[i]);
  }
  return out;
}

function findNode(roots: ColumnNode[], id: string): ColumnNode | null {
  const stack = [...roots];
  while (stack.length > 0) {
    const n = stack.pop();
    if (!n) break;
    if (n.lifelineId === id) return n;
    for (const c of n.children) stack.push(c);
  }
  return null;
}

/**
 * Lay out the visible columns.
 *
 * Traversal is depth-first in `lifeline.order`, so a column always sits
 * immediately to the right of its parent and message edges stay short. A
 * collapsed node emits one composite column covering its whole subtree and the
 * walk does not descend; an idle subtree emits one narrow folded column and the
 * walk does not descend either.
 *
 * When `solo` names a lifeline that is not present, nothing is rendered - an
 * empty axis is a truthful answer to "show me only this subtree" for a subtree
 * that does not exist, whereas silently showing everything is not.
 */
export function computeColumns(
  lifelines: Lifeline[],
  opts: ColumnOptions,
): ColumnLayout {
  const forest = buildForest(lifelines);

  let roots: ColumnNode[] = forest;
  if (opts.solo !== null && opts.solo !== undefined) {
    const soloNode = findNode(forest, opts.solo);
    roots = soloNode ? [soloNode] : [];
  }

  const useActivity = opts.activeWindow !== undefined;
  const activeIds: ReadonlySet<string> = opts.activeIds ?? new Set<string>();
  const isActive = (id: string): boolean => activeIds.has(id);

  const columnWidth = Math.max(1, opts.columnWidth);
  const foldedWidth = Math.max(1, Math.min(opts.foldedWidth, columnWidth));
  const gap = Math.max(0, opts.gap);

  const columns: Column[] = [];
  const columnFor = new Map<string, Column>();
  let cursor = 0;

  const push = (
    node: ColumnNode,
    memberIds: string[],
    collapsed: boolean,
    folded: boolean,
    depth: number,
  ): void => {
    const width = folded ? foldedWidth : columnWidth;
    const col: Column = {
      lifelineId: node.lifelineId,
      lifeline: node.lifeline,
      memberIds,
      collapsed,
      folded,
      x: cursor + width / 2,
      width,
      depth,
    };
    cursor += width + gap;
    columns.push(col);
    for (const id of memberIds) columnFor.set(id, col);
  };

  // Explicit stack instead of recursion; the frame loop calls this on every
  // layout change and deep spawn chains should not risk a stack overflow.
  interface Pending {
    node: ColumnNode;
    depth: number;
  }
  const stack: Pending[] = [];
  for (let i = roots.length - 1; i >= 0; i--) {
    stack.push({ node: roots[i], depth: 0 });
  }

  while (stack.length > 0) {
    const item = stack.pop();
    if (!item) break;
    const { node, depth } = item;

    const members = subtreeIds(node);

    if (useActivity && !members.some(isActive)) {
      // Whole subtree idle: one narrow stripe, still present on the axis.
      push(node, members, opts.collapsed.has(node.lifelineId), true, depth);
      continue;
    }

    if (opts.collapsed.has(node.lifelineId)) {
      push(node, members, true, false, depth);
      continue;
    }

    // The node itself may be idle while a descendant is not; narrow just this
    // column and keep walking.
    const selfFolded = useActivity && !isActive(node.lifelineId);
    push(node, [node.lifelineId], false, selfFolded, depth);

    for (let i = node.children.length - 1; i >= 0; i--) {
      stack.push({ node: node.children[i], depth: depth + 1 });
    }
  }

  const totalWidth = columns.length > 0 ? Math.max(0, cursor - gap) : 0;
  return { columns, columnFor, totalWidth };
}

/**
 * Lifelines with any interval or edge endpoint inside `[startMs, endMs]`.
 *
 * Provided so callers do not each reinvent the predicate that
 * {@link ColumnOptions.activeIds} expects; `computeColumns` deliberately takes
 * the set rather than computing it, so the caller can cache it across frames.
 */
export function activeLifelineIds(
  intervals: ReadonlyArray<{ lifelineId: string; startMs: number; endMs: number }>,
  edges: ReadonlyArray<{ fromId: string; toId: string; sendMs: number; recvMs: number }>,
  startMs: number,
  endMs: number,
): Set<string> {
  const out = new Set<string>();
  for (const iv of intervals) {
    if (iv.endMs >= startMs && iv.startMs <= endMs) out.add(iv.lifelineId);
  }
  for (const e of edges) {
    const lo = Math.min(e.sendMs, e.recvMs);
    const hi = Math.max(e.sendMs, e.recvMs);
    if (hi >= startMs && lo <= endMs) {
      out.add(e.fromId);
      out.add(e.toId);
    }
  }
  return out;
}
