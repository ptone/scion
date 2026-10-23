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
 * Track changes: every edit inside one textblock is rewritten into
 * suggestion marks.
 *
 * appendTransaction sees each user transaction after it applied. For a
 * single replace step inside one textblock it undoes the step and redoes
 * it as a suggestion in the same history event, so one undo reverts both:
 *
 * - deleted plain or highlighted text stays, marked as a deletion (or as a
 *   replacement's old side when the step also inserts text);
 * - deleted text of the reviewer's own pending insertion (or a
 *   replacement's new side, or a comment) really goes;
 * - already deleted text is left alone;
 * - inserted text goes after the deleted range, marked as an insertion, or
 *   as a replacement's new side when it replaces text or continues one.
 *   Equal adjacent marks merge, so typing inside one's own suggestion
 *   extends it instead of nesting.
 * - an edit inside an unmarked inline code span becomes a replacement of
 *   the whole span (old span, then new span), since a mark inside code
 *   would be read as literal text.
 *
 * policy.ts decides which transactions reach this plugin at all.
 */

import { Fragment } from '@milkdown/prose/model';
import type { Node, ResolvedPos, Schema } from '@milkdown/prose/model';
import { Plugin, PluginKey, TextSelection } from '@milkdown/prose/state';
import type { EditorState, Transaction } from '@milkdown/prose/state';
import { ReplaceStep, Transform } from '@milkdown/prose/transform';
import type { EditorView } from '@milkdown/prose/view';

import { COMMENT, CRITIC_MARK_NAMES, MARKS, markKeyOf } from './schema.js';

/** Meta of transactions the tracker must not rewrite (its own, and commands'). */
export const SKIP_TRACK = 'criticSkipTrack';
/** Meta of the transaction that loads a document. */
export const LOAD_META = 'criticLoad';

export const trackKey = new PluginKey('criticTrack');

/** The inline content of a slice that stays inside one textblock, or null. */
export function inlineContent(slice: {
  content: Fragment;
  openStart: number;
  openEnd: number;
}): Fragment | null {
  if (slice.openStart !== slice.openEnd) return null;
  let frag = slice.content;
  for (let d = 0; d < slice.openStart; d++) {
    if (frag.childCount !== 1) return null;
    frag = frag.firstChild!.content;
  }
  let ok = true;
  frag.forEach((n) => {
    if (!n.isInline) ok = false;
  });
  return ok ? frag : null;
}

function stripCritic(frag: Fragment, schema: Schema): Node[] {
  const out: Node[] = [];
  frag.forEach((n) => {
    let marks = n.marks;
    for (const name of CRITIC_MARK_NAMES) marks = schema.marks[name].removeFromSet(marks);
    out.push(n.mark(marks));
  });
  return out;
}

/** The result of tracking one step: the steps on top of the edited doc and the new cursor. */
export interface Tracked {
  transform: Transform;
  head: number;
}

/**
 * The unmarked inline code run around [from, to] of $from's textblock, or
 * null when the edit is not inside one.
 */
function codeRun(
  $from: ResolvedPos,
  from: number,
  to: number,
  schema: Schema
): { from: number; to: number } | null {
  const code = schema.marks.inlineCode;
  if (!code) return null;
  const start = $from.start();
  const runs: { from: number; to: number }[] = [];
  $from.parent.forEach((child, off) => {
    const a = start + off;
    const b = a + child.nodeSize;
    if (!(child.isText && code.isInSet(child.marks) && markKeyOf(child.marks) === null)) return;
    const last = runs[runs.length - 1];
    if (last && last.to === a) last.to = b;
    else runs.push({ from: a, to: b });
  });
  for (const r of runs) {
    if (r.from > from || to > r.to) continue;
    // Typing at the very edge of a span is outside it (inline code is not inclusive).
    if (from === to && (from === r.from || from === r.to)) continue;
    return r;
  }
  return null;
}

/**
 * Rewrites the replace step (from oldDoc to newDoc) as a suggestion.
 * Returns null when the step is not an edit inside one textblock.
 */
export function trackStep(
  step: ReplaceStep,
  oldDoc: Node,
  newDoc: Node,
  cursor: { empty: boolean; head: number }
): Tracked | null {
  const s = step as ReplaceStep & {
    from: number;
    to: number;
    structure: boolean;
  };
  if (s.structure) return null;
  const { from, to, slice } = s;
  const schema = oldDoc.type.schema;
  const insFrag = slice.size ? inlineContent(slice) : null;
  if (slice.size && !insFrag) return null;
  const $from = oldDoc.resolve(from);
  const $to = oldDoc.resolve(to);
  if (!$from.sameParent($to) || !$from.parent.isTextblock) return null;
  if (!insFrag && from === to) return null;

  const out = new Transform(newDoc);
  out.step(step.invert(oldDoc));
  const fwd = (): ReturnType<Transform['mapping']['slice']> => out.mapping.slice(1);

  const run = codeRun($from, from, to, schema);
  if (run) {
    // Old span becomes the replacement's old side; the edited span its new side.
    const oldSpan = oldDoc.slice(run.from, run.to).content;
    const edited = oldDoc
      .slice(run.from, from)
      .content.append(insFrag ?? Fragment.empty)
      .append(oldDoc.slice(to, run.to).content);
    out.addMark(run.from, run.to, schema.marks[MARKS.subOld].create());
    const subNew = schema.marks[MARKS.subNew].create();
    const nodes: Node[] = [];
    edited.forEach((n) => {
      if (n.isText) nodes.push(n.mark(subNew.addToSet(n.marks)));
    });
    if (nodes.length === 0 && oldSpan.size === 0) return null;
    out.insert(run.to, nodes);
    const head = run.to + nodes.reduce((sum, n) => sum + n.nodeSize, 0);
    return { transform: out, head };
  }

  // Classify the deleted range.
  const realDeletes: { from: number; to: number }[] = [];
  const toMark: { from: number; to: number }[] = [];
  oldDoc.nodesBetween(from, to, (node, pos) => {
    if (!node.isInline) return true;
    const a = Math.max(pos, from);
    const b = Math.min(pos + node.nodeSize, to);
    if (b <= a) return false;
    if (node.type.name === COMMENT) {
      realDeletes.push({ from: a, to: b });
      return false;
    }
    const k = markKeyOf(node.marks);
    if (k === 'ins' || k === 'subNew') realDeletes.push({ from: a, to: b });
    else if (k === null || k === 'hl') toMark.push({ from: a, to: b });
    return false;
  });

  const replacing = !!insFrag && toMark.length > 0;
  const delType = schema.marks[replacing ? MARKS.subOld : MARKS.del];
  for (const r of toMark) {
    out.removeMark(r.from, r.to, schema.marks[MARKS.hl]);
    out.addMark(r.from, r.to, delType.create());
  }

  let insEnd = -1;
  let afterIns = 0;
  if (insFrag) {
    let key: 'ins' | 'subNew' = 'ins';
    if (replacing) {
      key = 'subNew';
    } else {
      const before = $from.nodeBefore ? markKeyOf($from.nodeBefore.marks) : null;
      const after = $to.nodeAfter ? markKeyOf($to.nodeAfter.marks) : null;
      if (before === 'subNew' || after === 'subNew' || before === 'subOld') key = 'subNew';
    }
    const mark = schema.marks[MARKS[key]].create();
    const nodes = stripCritic(insFrag, schema).map((n) => n.mark(mark.addToSet(n.marks)));
    const at = fwd().map(to);
    out.insert(at, nodes);
    insEnd = at + nodes.reduce((sum, n) => sum + n.nodeSize, 0);
    afterIns = out.steps.length;
  }
  for (const r of realDeletes.sort((x, y) => y.from - x.from)) {
    const m = fwd();
    out.delete(m.map(r.from, -1), m.map(r.to, -1));
  }

  let head: number;
  if (insEnd >= 0) {
    head = out.mapping.slice(afterIns).map(insEnd);
  } else {
    const forward = cursor.empty && cursor.head === from;
    head = fwd().map(forward ? to : from, forward ? 1 : -1);
  }
  return { transform: out, head: Math.min(head, out.doc.content.size) };
}

/** Copies a Tracked result onto a transaction of the state it was computed on. */
function applyTracked(tr: Transaction, t: Tracked): Transaction {
  for (const step of t.transform.steps) tr.step(step);
  tr.setSelection(TextSelection.create(tr.doc, t.head));
  tr.setMeta(SKIP_TRACK, true);
  return tr;
}

/** The single replace step of a user transaction, or null. */
export function singleReplace(tr: Transaction): ReplaceStep | null {
  if (tr.steps.length !== 1 || !(tr.steps[0] instanceof ReplaceStep)) return null;
  return tr.steps[0];
}

/** Whether a transaction is one the tracker leaves alone. */
export function isExempt(tr: Transaction): boolean {
  return (
    !tr.docChanged ||
    !!tr.getMeta(SKIP_TRACK) ||
    !!tr.getMeta(LOAD_META) ||
    tr.getMeta('history$') !== undefined ||
    tr.getMeta('composition') !== undefined
  );
}

/** Tracks a user transaction against the state it applies to, or null. */
export function trackTransaction(tr: Transaction, before: EditorState): Tracked | null {
  const step = singleReplace(tr);
  if (!step) return null;
  const sel = before.selection;
  return trackStep(step, before.doc, tr.doc, { empty: sel.empty, head: sel.head });
}

/** The tracking plugin. */
export function trackPlugin(): Plugin {
  return new Plugin({
    key: trackKey,
    appendTransaction(trs, oldState, newState): Transaction | null {
      if (trs.length !== 1) return null;
      const tr = trs[0];
      if (isExempt(tr)) return null;
      const t = trackTransaction(tr, oldState);
      return t ? applyTracked(newState.tr, t) : null;
    },
  });
}

/** The textblock around a position and where it starts. */
interface BlockSnapshot {
  pos: number;
  node: Node;
}

/** The textblock at the selection, to compare with after a composition. */
export function snapshotBlock(state: EditorState): BlockSnapshot | null {
  const $from = state.selection.$from;
  if (!$from.parent.isTextblock) return null;
  return { pos: $from.before(), node: $from.parent };
}

/**
 * After a composition (IME, dictation, autocorrect on some keyboards),
 * which neither the policy nor the tracker touches while it runs, records
 * the composed text as an insertion. check vets the result (the block's
 * container, and that it would save); it returns a refusal reason or null.
 * Returns null when the composition was recorded, or the reason it was
 * not ('generic' when the composition removed text); the block is then
 * restored to its state before the composition.
 */
export function reconcileComposition(
  view: EditorView,
  snap: BlockSnapshot,
  check: (doc: Node, blockPos: number) => string | null = () => null
): string | null {
  const state = view.state;
  const now = state.doc.nodeAt(snap.pos);
  if (!now || now.type !== snap.node.type) return 'generic';
  const restore = (why: string): string => {
    const tr = state.tr.replaceWith(
      snap.pos + 1,
      snap.pos + 1 + now.content.size,
      snap.node.content
    );
    tr.setMeta(SKIP_TRACK, true);
    view.dispatch(tr);
    return why;
  };
  const start = now.content.findDiffStart(snap.node.content);
  if (start === null) return null;
  const end = now.content.findDiffEnd(snap.node.content);
  if (!end) return null;
  let { a: endNow, b: endOld } = end;
  // findDiffEnd may overlap findDiffStart when text repeats.
  const overlap = start - Math.min(endNow, endOld);
  if (overlap > 0) {
    endNow += overlap;
    endOld += overlap;
  }
  // Only insertions are recorded in this version: a composition that
  // removed text (or replaced a selection) is undone.
  if (endOld > start) return restore('generic');
  const base = snap.pos + 1;
  // Restore the block, then apply the composed text as one insertion.
  const revert = state.tr.replaceWith(base, base + now.content.size, snap.node.content);
  const oldDoc = revert.doc;
  const step = new ReplaceStep(base + start, base + start, now.slice(start, endNow));
  const applied = step.apply(oldDoc);
  if (!applied.doc) return restore('generic');
  const t = trackStep(step, oldDoc, applied.doc, { empty: true, head: base + start });
  if (!t) return restore('generic');
  const refused = check(t.transform.doc, snap.pos);
  if (refused) return restore(refused);
  revert.step(step);
  for (const s of t.transform.steps) revert.step(s);
  revert.setSelection(TextSelection.create(revert.doc, t.head));
  revert.setMeta(SKIP_TRACK, true);
  view.dispatch(revert);
  return null;
}
