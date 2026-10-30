/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * Suggested paragraph breaks and joins.
 *
 * A boundary between two consecutive textblocks can carry a suggestion
 * when it lies between two paragraphs or headings, two list items or two
 * block quotes (the carrier is the block after the boundary, which gets
 * the critic_block attribute; schema.ts). In the text, the suggestion is
 * an insertion or deletion mark around the source between the two blocks.
 *
 * The builders here make the tracked form of a structural edit on a
 * Transform:
 *
 * - a split (Enter) keeps both halves, and the new block carries a
 *   suggested break; in a list item's first paragraph it makes a new list
 *   item, and after a heading the new block is a paragraph;
 * - a join (Backspace at a block start, Delete at a block end) keeps the
 *   blocks apart and marks the boundary as a suggested join, or really
 *   joins them when the boundary is the reviewer's own pending break;
 * - deleting across blocks marks the text deleted and every boundary in
 *   the range as a suggested join (the reviewer's own pending text and
 *   breaks really go), and typing over such a range inserts after it;
 * - pasting several blocks inserts the first inline and each later one as
 *   a new block with a suggested break, its text an insertion.
 *
 * A boundary suggestion of an earlier review is never changed. policy.ts
 * checks every result before it applies.
 */

import { Slice } from '@milkdown/prose/model';
import type { Fragment, Mark, Node, NodeType, ResolvedPos } from '@milkdown/prose/model';
import { ReplaceStep } from '@milkdown/prose/transform';
import type { Transform } from '@milkdown/prose/transform';

import type { HintKind } from './policy.js';
import { BLOCK_ATTR, COMMENT, MARKS, blockBreak, breakOf, isMine, markKeyOf } from './schema.js';
import { stripCritic, trackStep } from './track.js';

/** A boundary between two consecutive textblocks that can carry a suggestion. */
export interface Boundary {
  /** The position before the carrier (the block after the boundary). */
  carrierPos: number;
  carrier: Node;
  /** The end of the content of the textblock before the boundary. */
  aEnd: number;
  /** The start of the content of the textblock after it. */
  bStart: number;
}

const TABLE_NODES = new Set([
  'table',
  'table_row',
  'table_header_row',
  'table_cell',
  'table_header',
]);
const LIST_NODES = new Set(['bullet_list', 'ordered_list', 'list_item']);
const TEXT_TYPES = new Set(['paragraph', 'heading']);

/** Whether a node is a paragraph or heading (not code). */
function isTextType(n: Node): boolean {
  return TEXT_TYPES.has(n.type.name);
}

/** The hint for a boundary that cannot carry a suggestion, from the nodes around it. */
function boundaryHint(nodes: Node[]): HintKind {
  if (nodes.some((n) => n.type.spec.code)) return 'code';
  if (nodes.some((n) => TABLE_NODES.has(n.type.name) || LIST_NODES.has(n.type.name))) {
    return 'list-table';
  }
  return 'break';
}

/**
 * The boundary before the textblock at tbPos (the position before it):
 * null at the start of the document, a refusal hint where the boundary
 * cannot carry a suggestion, or the boundary.
 */
export function boundaryBefore(doc: Node, tbPos: number): Boundary | { refuse: HintKind } | null {
  const $in = doc.resolve(tbPos + 1);
  const t2 = $in.parent;
  let d = $in.depth;
  while (d > 0 && $in.index(d - 1) === 0) d--;
  if (d === 0) return null;
  const parent = $in.node(d - 1);
  const c2 = $in.node(d);
  const c1 = parent.child($in.index(d - 1) - 1);
  const carrierPos = $in.before(d);
  // The last textblock of c1, the block before the boundary.
  const path: Node[] = [c1];
  let t1 = c1;
  let t1Pos = carrierPos - c1.nodeSize;
  while (!t1.isTextblock) {
    const last = t1.lastChild;
    if (!last || t1.isLeaf) return { refuse: boundaryHint([c1, c2]) };
    t1Pos = t1Pos + t1.nodeSize - 1 - last.nodeSize;
    t1 = last;
    path.push(t1);
  }
  const around: Node[] = [...path, c2, t2];
  for (let k = 1; k <= $in.depth; k++) around.push($in.node(k));
  const pair = (a: string, b: string): boolean => c1.type.name === a && c2.type.name === b;
  const shapes =
    (isTextType(c1) && isTextType(c2)) ||
    pair('list_item', 'list_item') ||
    pair('blockquote', 'blockquote');
  if (
    !shapes ||
    !isTextType(t1) ||
    !isTextType(t2) ||
    around.some((n) => TABLE_NODES.has(n.type.name) || n.type.spec.code)
  ) {
    return { refuse: boundaryHint(around) };
  }
  return { carrierPos, carrier: c2, aEnd: t1Pos + 1 + t1.content.size, bStart: tbPos + 1 };
}

/** Whether a value is a Boundary (not a refusal or null). */
export function isBoundary(b: Boundary | { refuse: HintKind } | null): b is Boundary {
  return b !== null && !('refuse' in b);
}

/**
 * The carrier of the boundary before the textblock at tbPos when that
 * boundary can carry a suggestion, else null.
 */
export function carrierBefore(doc: Node, tbPos: number): Boundary | null {
  const b = boundaryBefore(doc, tbPos);
  return isBoundary(b) ? b : null;
}

/** What a builder made: the tracked edit on tr, or a refusal, or nothing to do. */
export type Built =
  | { head: number; from?: number; to?: number }
  | { refuse: HintKind }
  | { noop: true };

/** Whether a Built is a refusal. */
export function isRefusal(b: Built): b is { refuse: HintKind } {
  return 'refuse' in b;
}

const mineMark = (doc: Node, key: 'ins' | 'del'): Mark =>
  doc.type.schema.marks[MARKS[key]].create({ mine: true });

/**
 * Whether pos (inside a textblock) is inside an unmarked inline code span
 * or a formatting mark (link, emphasis, strong, strikethrough) that
 * continues on both sides: a break there would cut it in two.
 */
function cutsInline($p: ResolvedPos): HintKind | null {
  const before = $p.nodeBefore;
  const after = $p.nodeAfter;
  if (!before || !after) return null;
  for (const m of before.marks) {
    if (!m.isInSet(after.marks)) continue;
    if (m.type.spec.code) return 'code';
    if (!m.type.name.startsWith('critic_')) return 'break';
  }
  return null;
}

/** Whether doc holds a raw HTML node between from and to (never editable in suggest mode). */
export function holdsHtml(doc: Node, from: number, to: number): boolean {
  let found = false;
  if (to > from) {
    doc.nodesBetween(from, to, (n) => {
      if (n.type.name === 'html') found = true;
      return !found;
    });
  }
  return found;
}

/**
 * Marks [from, to] (inside one textblock of tr.doc) as deleted, as typing
 * would (trackStep), and returns where to now is; null when it cannot. Raw
 * HTML in the range is refused (the caller gives the generic hint), as for
 * an inline edit.
 */
function deleteInline(tr: Transform, from: number, to: number): number | null {
  if (from === to) return to;
  const doc = tr.doc;
  if (holdsHtml(doc, from, to)) return null;
  const step = new ReplaceStep(from, to, Slice.empty);
  const applied = step.apply(doc);
  if (!applied.doc) return null;
  const t = trackStep(step, doc, applied.doc, { empty: false, head: to });
  if (!t) return null;
  const start = tr.steps.length;
  tr.step(step);
  for (const s of t.transform.steps) tr.step(s);
  return tr.mapping.slice(start).map(to, 1);
}

/** The type of block a split makes after the split point. */
export interface SplitType {
  type: NodeType;
  attrs?: Record<string, unknown>;
}

/**
 * Splits the textblock of tr.doc at at, as a suggested break: the new
 * block carries an own inserted break. In the first paragraph of a list
 * item the split makes a new list item; otherwise a new block of type
 * after (a paragraph by default). Returns the start of the new block's
 * content, or a refusal.
 */
export function splitInto(tr: Transform, at: number, after?: SplitType): Built {
  const doc = tr.doc;
  const schema = doc.type.schema;
  const $at = doc.resolve(at);
  const tb = $at.parent;
  if (!tb.isTextblock) return { refuse: 'generic' };
  if (tb.type.spec.code) return { refuse: 'code' };
  for (let d = $at.depth; d > 0; d--) {
    if (TABLE_NODES.has($at.node(d).type.name)) return { refuse: 'list-table' };
  }
  if (!isTextType(tb)) return { refuse: 'break' };
  // A break before a heading's text would turn the heading into a paragraph.
  if (tb.type.name === 'heading' && $at.parentOffset === 0 && tb.content.size > 0) {
    return { refuse: 'break' };
  }
  const cut = cutsInline($at);
  if (cut) return { refuse: cut };
  const ins = blockBreak('ins', true);
  const d = $at.depth;
  const item = d >= 2 ? $at.node(d - 1) : null;
  try {
    if (item && item.type.name === 'list_item' && $at.index(d - 1) === 0) {
      const attrs: Record<string, unknown> = { ...item.attrs, [BLOCK_ATTR]: ins };
      if (attrs.checked !== undefined && attrs.checked !== null) attrs.checked = false;
      tr.split(at, 2, [
        { type: item.type, attrs },
        { type: schema.nodes.paragraph, attrs: { [BLOCK_ATTR]: null } },
      ]);
      return { head: at + 4 };
    }
    const type = after?.type ?? schema.nodes.paragraph;
    tr.split(at, 1, [{ type, attrs: { ...(after?.attrs ?? {}), [BLOCK_ATTR]: ins } }]);
    return { head: at + 2 };
  } catch {
    return { refuse: 'break' };
  }
}

/**
 * Enter: the selection from..to (inside one textblock) is marked deleted
 * and a suggested break goes after it.
 */
export function buildSplit(tr: Transform, from: number, to: number): Built {
  const doc = tr.doc;
  const $from = doc.resolve(from);
  const $to = doc.resolve(to);
  if (!$from.sameParent($to) || !$from.parent.isTextblock) return { refuse: 'break' };
  if ($from.parent.type.spec.code) return { refuse: 'code' };
  const at = deleteInline(tr, from, to);
  if (at === null) return { refuse: 'generic' };
  return splitInto(tr, at);
}

/** One range of a textblock's inline content. */
interface Span {
  from: number;
  to: number;
}

/**
 * Deleting from..to across textblocks of tr.doc, then inserting insert
 * (inline content) after the range: the text is marked deleted (own
 * pending text really goes), every boundary in the range becomes a
 * suggested join (own pending breaks really join), and the inserted text
 * is an own insertion. Refused when the range holds anything but text in
 * paragraphs, headings, list items and quotes, or when it would change an
 * earlier review's mark or boundary suggestion.
 */
export function buildDelete(
  tr: Transform,
  from: number,
  to: number,
  insert: Fragment | null
): Built {
  const doc = tr.doc;
  const $from = doc.resolve(from);
  const $to = doc.resolve(to);
  if (!$from.parent.isTextblock || !$to.parent.isTextblock) return { refuse: 'generic' };
  const blocks: { pos: number; node: Node }[] = [];
  let bad: HintKind | null = null;
  doc.nodesBetween(from, to, (n, pos) => {
    if (bad) return false;
    if (n.isTextblock) {
      if (n.type.spec.code) bad = 'code';
      else blocks.push({ pos, node: n });
      return false;
    }
    if (TABLE_NODES.has(n.type.name)) bad = 'list-table';
    else if (n.isBlock && n.isLeaf) bad = 'break';
    return !bad;
  });
  if (bad) return { refuse: bad };
  if (blocks.length < 2) return { refuse: 'generic' };
  // Every boundary in the range must carry a suggestion, none an earlier one.
  const joins: Boundary[] = [];
  const marks: Boundary[] = [];
  for (let k = 1; k < blocks.length; k++) {
    const b = boundaryBefore(doc, blocks[k].pos);
    if (!isBoundary(b)) return { refuse: b ? b.refuse : 'break' };
    if (b.aEnd !== blocks[k - 1].pos + 1 + blocks[k - 1].node.content.size) {
      return { refuse: 'break' };
    }
    const cur = breakOf(b.carrier);
    if (cur && !cur.own) return { refuse: 'earlier' };
    if (cur?.change === 'ins') joins.push(b);
    else if (!cur) marks.push(b);
  }
  // The inline content of the range, as typing would treat it.
  const realDeletes: Span[] = [];
  const toMark: Span[] = [];
  let foreign = false;
  doc.nodesBetween(from, to, (node, pos) => {
    if (foreign) return false;
    if (!node.isInline) return true;
    const a = Math.max(pos, from);
    const b = Math.min(pos + node.nodeSize, to);
    if (b <= a) return false;
    if (node.type.name === 'html') {
      foreign = true;
      return false;
    }
    if (node.type.name === COMMENT) {
      if (node.attrs.mine === true) realDeletes.push({ from: a, to: b });
      else foreign = true;
      return false;
    }
    const k = markKeyOf(node.marks);
    if (k === 'ins' || k === 'subNew') {
      if (isMine(node.marks)) realDeletes.push({ from: a, to: b });
      else foreign = true;
    } else if (k === null || k === 'hl') {
      toMark.push({ from: a, to: b });
    }
    return false;
  });
  if (foreign) return { refuse: 'earlier' };
  const schema = doc.type.schema;
  const start = tr.steps.length;
  for (const b of marks) tr.setNodeAttribute(b.carrierPos, BLOCK_ATTR, blockBreak('del', true));
  const del = mineMark(doc, 'del');
  for (const r of toMark) {
    tr.removeMark(r.from, r.to, schema.marks[MARKS.hl]);
    tr.addMark(r.from, r.to, del);
  }
  let insEnd = -1;
  let afterIns = 0;
  if (insert && insert.size > 0) {
    const ins = mineMark(doc, 'ins');
    const nodes = stripCritic(insert, schema).map((n) => n.mark(ins.addToSet(n.marks)));
    const at = tr.mapping.slice(start).map(to);
    tr.insert(at, nodes);
    insEnd = at + nodes.reduce((sum, n) => sum + n.nodeSize, 0);
    afterIns = tr.steps.length;
  }
  const cuts: Span[] = [...realDeletes, ...joins.map((b) => ({ from: b.aEnd, to: b.bStart }))].sort(
    (x, y) => y.from - x.from
  );
  try {
    for (const r of cuts) {
      const m = tr.mapping.slice(start);
      tr.delete(m.map(r.from, -1), m.map(r.to, -1));
    }
  } catch {
    return { refuse: 'break' };
  }
  if (tr.steps.length === start) return { noop: true };
  const head =
    insEnd >= 0 ? tr.mapping.slice(afterIns).map(insEnd) : tr.mapping.slice(start).map(from, -1);
  return { head: Math.min(head, tr.doc.content.size) };
}

/** The textblock after the one at pos ($p inside it), as its position, or null. */
export function nextTextblock(doc: Node, $p: ResolvedPos): number | null {
  let found: number | null = null;
  doc.nodesBetween($p.after(), doc.content.size, (n, pos) => {
    if (found !== null) return false;
    if (n.isTextblock) {
      found = pos;
      return false;
    }
    return true;
  });
  return found;
}

/**
 * Backspace at the start of the textblock at $p (dir -1), or Delete at the
 * end of it (dir 1): a suggested join of the boundary. null when this is
 * not a boundary that can carry a suggestion (the caller lets the editor's
 * own handling and the policy decide).
 */
export function buildJoin(tr: Transform, $p: ResolvedPos, dir: -1 | 1): Built | null {
  const doc = tr.doc;
  let tbPos: number;
  if (dir < 0) tbPos = $p.before();
  else {
    const next = nextTextblock(doc, $p);
    if (next === null) return { noop: true };
    tbPos = next;
  }
  const b = boundaryBefore(doc, tbPos);
  if (b === null) return { noop: true };
  if (!isBoundary(b)) return null;
  if (dir > 0 && b.aEnd !== $p.pos) return null;
  const cur = breakOf(b.carrier);
  if (cur && !cur.own) return { refuse: 'earlier' };
  if (cur?.change === 'del') return { noop: true };
  const r = buildDelete(tr, b.aEnd, b.bStart, null);
  if (!('head' in r)) return r;
  // Backspace stays before the boundary; Delete moves past it.
  const m = tr.mapping;
  return {
    head: dir < 0 ? m.map(b.aEnd, -1) : m.map(b.bStart, 1),
    from: b.aEnd,
    to: b.bStart,
  };
}

/** One block of pasted text: a paragraph or heading and its inline content. */
export interface Piece {
  type: 'paragraph' | 'heading';
  level?: number | undefined;
  content: Fragment;
}

/**
 * Pasting several blocks over from..to: the selection is deleted as
 * typing would, the first piece goes in inline and each later one as a
 * new block with a suggested break, all of it an own insertion. In a list
 * item the later pieces are new list items (their text only).
 */
export function buildPaste(tr: Transform, from: number, to: number, pieces: Piece[]): Built {
  const doc = tr.doc;
  const schema = doc.type.schema;
  if (pieces.length < 2) return { refuse: 'paste-multi' };
  let at: number;
  const $from = doc.resolve(from);
  const $to = doc.resolve(to);
  if ($from.sameParent($to)) {
    if (!$from.parent.isTextblock) return { refuse: 'generic' };
    if ($from.parent.type.spec.code) return { refuse: 'code' };
    const r = deleteInline(tr, from, to);
    if (r === null) return { refuse: 'generic' };
    at = r;
  } else {
    const start = tr.steps.length;
    const r = buildDelete(tr, from, to, null);
    if (isRefusal(r)) return r;
    at = tr.mapping.slice(start).map(to);
  }
  const ins = mineMark(doc, 'ins');
  const put = (pos: number, content: Fragment): number => {
    const nodes = stripCritic(content, schema).map((n) => n.mark(ins.addToSet(n.marks)));
    if (nodes.length === 0) return pos;
    tr.insert(pos, nodes);
    return pos + nodes.reduce((sum, n) => sum + n.nodeSize, 0);
  };
  const $at = tr.doc.resolve(at);
  if (!$at.parent.isTextblock) return { refuse: 'generic' };
  if ($at.parent.type.spec.code) return { refuse: 'code' };
  const cut = cutsInline($at);
  if (cut) return { refuse: cut };
  at = put(at, pieces[0].content);
  for (const p of pieces.slice(1)) {
    const type =
      p.type === 'heading'
        ? { type: schema.nodes.heading, attrs: { level: p.level ?? 1 } }
        : { type: schema.nodes.paragraph };
    const s = splitInto(tr, at, type);
    if (!('head' in s)) return isRefusal(s) ? s : { refuse: 'paste-multi' };
    at = put(s.head, p.content);
  }
  return { head: at };
}

/**
 * The tracked form of a user's replace step that is not an edit inside one
 * textblock (track.ts), built on oldDoc: a split (a step that inserts
 * only block structure inside one textblock), or a deletion across
 * textblocks with inline text or nothing inserted. Null when it is
 * neither; a refusal when it cannot be recorded.
 */
export function buildFromStep(
  tr: Transform,
  step: ReplaceStep & { from: number; to: number },
  inline: Fragment | null
): Built | null {
  const doc = tr.doc;
  const { from, to, slice } = step;
  const $from = doc.resolve(from);
  const $to = doc.resolve(to);
  if (!$from.parent.isTextblock || !$to.parent.isTextblock) return null;
  if ($from.sameParent($to)) {
    // Only structure inserted: a split.
    if (slice.size === 0 || inline) return null;
    let text = false;
    slice.content.descendants((n) => {
      if (n.isInline) text = true;
      return !text;
    });
    if (text) return null;
    return buildSplit(tr, from, to);
  }
  if (slice.size > 0 && !inline) return null;
  return buildDelete(tr, from, to, inline);
}
