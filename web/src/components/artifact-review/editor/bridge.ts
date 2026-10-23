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
 * Markdown with CriticMarkup to the editor's document and back.
 *
 * Load: utils/critic-sentinels.ts replaces each mark token with a sentinel
 * (parseCritic reads the marks, so code is treated exactly as the hub
 * treats it), the stock Milkdown parser builds the document, then
 * applySentinels turns sentinel runs into flat marks and comment nodes and
 * deletes the sentinels. Save: the reverse.
 *
 * Where Markdown sees code but the critic parser does not, tokens inside
 * the code stay literal text, as written.
 */

import type { Mark, Node, Schema } from '@milkdown/prose/model';
import { Transform } from '@milkdown/prose/transform';

import {
  criticToSentinelText,
  sentinelSet,
  sentinelTextToCritic,
  tokenOf,
} from '../../../utils/critic-sentinels.js';
import type { SentinelAlphabet } from '../../../utils/critic-sentinels.js';
import { COMMENT, CRITIC_MARK_NAMES, MARKS, markKeyOf, runKeyOf } from './schema.js';
import type { MarkKey } from './schema.js';

interface Edit {
  pos: number;
  note?: string;
  literal?: string;
}

/**
 * Turns the sentinels of tr.doc into marks and comment nodes. A run may
 * cross block boundaries; the mark goes on every textblock in between that
 * allows it. A run that covers no markable text keeps its tokens as text.
 */
export function applySentinels(
  tr: Transform,
  schema: Schema,
  notes: string[],
  a: SentinelAlphabet
): void {
  const tokenMap = tokenOf(a);
  const tok = (c: string): string => tokenMap.get(c) ?? c;
  const sentinels = sentinelSet(a);
  const open = new Map<string, MarkKey>([
    [a.insOpen, 'ins'],
    [a.delOpen, 'del'],
    [a.subOpen, 'subOld'],
    [a.hlOpen, 'hl'],
  ]);
  const closeOf: Record<MarkKey, string> = {
    ins: a.insClose,
    del: a.delClose,
    subOld: a.subClose,
    subNew: a.subClose,
    hl: a.hlClose,
  };
  const openOf: Record<MarkKey, string> = {
    ins: a.insOpen,
    del: a.delOpen,
    subOld: a.subOpen,
    subNew: a.subOpen,
    hl: a.hlOpen,
  };
  const insType = schema.marks[MARKS.ins];
  let state: MarkKey | null = null;
  let runStart = 0;
  let openEdit: Edit | null = null;
  let sepEdit: Edit | null = null;
  let pending: { from: number; to: number; key: MarkKey }[] = [];
  const ranges: { from: number; to: number; key: MarkKey }[] = [];
  const edits: Edit[] = [];
  let noteIdx = 0;
  const markable = (from: number, to: number): boolean => {
    let ok = false;
    tr.doc.nodesBetween(from, to, (n, pos, parent) => {
      if (n.isText && parent?.type.allowsMarkType(insType)) {
        const t = (n.text ?? '').slice(Math.max(0, from - pos), to - pos);
        for (const c of t) {
          if (!sentinels.has(c)) {
            ok = true;
            break;
          }
        }
      }
      return !ok;
    });
    return ok;
  };
  tr.doc.descendants((node, pos, parent) => {
    if (!node.isText) return true;
    const t = node.text ?? '';
    const code = !parent?.type.allowsMarkType(insType);
    for (let i = 0; i < t.length; i++) {
      const c = t[i];
      if (!sentinels.has(c)) continue;
      const p = pos + i;
      if (code) {
        if (c === a.note) edits.push({ pos: p, literal: `{>>${notes[noteIdx++] ?? ''}<<}` });
        else edits.push({ pos: p, literal: tok(c) });
        continue;
      }
      const key = open.get(c);
      if (key && state === null) {
        state = key;
        runStart = p + 1;
        openEdit = { pos: p };
        sepEdit = null;
        pending = [];
        edits.push(openEdit);
      } else if (c === a.subSep && state === 'subOld') {
        pending.push({ from: runStart, to: p, key: 'subOld' });
        state = 'subNew';
        runStart = p + 1;
        sepEdit = { pos: p };
        edits.push(sepEdit);
      } else if (state !== null && c === closeOf[state]) {
        pending.push({ from: runStart, to: p, key: state });
        const closeEdit: Edit = { pos: p };
        edits.push(closeEdit);
        if (pending.some((r) => markable(r.from, r.to))) {
          ranges.push(...pending);
        } else {
          openEdit!.literal = tok(openOf[state]);
          if (sepEdit) sepEdit.literal = tok(a.subSep);
          closeEdit.literal = tok(c);
        }
        state = null;
      } else if (c === a.note) {
        edits.push({ pos: p, note: notes[noteIdx++] ?? '' });
      } else {
        // Its partner was inside code: keep the token as written.
        edits.push({ pos: p, literal: tok(c) });
      }
    }
    return false;
  });
  const endState = state as MarkKey | null;
  if (endState !== null && openEdit) {
    (openEdit as Edit).literal = tok(openOf[endState]);
    if (sepEdit) (sepEdit as Edit).literal = tok(a.subSep);
  }
  for (const r of ranges) {
    if (r.to > r.from) tr.addMark(r.from, r.to, schema.marks[MARKS[r.key]].create());
  }
  for (const e of edits.reverse()) {
    if (e.literal !== undefined) {
      const marks = tr.doc.resolve(e.pos + 1).marks();
      tr.replaceWith(e.pos, e.pos + 1, schema.text(e.literal, marks));
    } else if (e.note !== undefined) {
      tr.replaceWith(e.pos, e.pos + 1, schema.nodes[COMMENT].create({ note: e.note }));
    } else {
      tr.delete(e.pos, e.pos + 1);
    }
  }
}

interface Item {
  from: number;
  to: number;
  key: MarkKey | null;
  /** runKeyOf: neighbours with the same key but different runs are separate marks. */
  run: string | null;
  note?: string;
}

/** A run of the serializer's walk: the mark key and its run identity. */
interface RunState {
  key: MarkKey;
  run: string;
}

/** Formatting marks two neighbours share (not code, not critic). */
function sharedMarks(a: readonly Mark[], b: readonly Mark[]): Mark[] {
  const keep = (ms: readonly Mark[]): Mark[] =>
    ms.filter((m) => !m.type.spec.code && !CRITIC_MARK_NAMES.has(m.type.name));
  const bm = keep(b);
  return keep(a).filter((m) => m.isInSet(bm));
}

/** The document with critic marks and comments replaced by sentinels. */
export function docToSentinels(
  doc: Node,
  schema: Schema,
  a: SentinelAlphabet
): { doc: Node; notes: string[] } {
  const openS: Record<MarkKey, string> = {
    ins: a.insOpen,
    del: a.delOpen,
    subOld: a.subOpen,
    subNew: a.subOpen + a.subSep,
    hl: a.hlOpen,
  };
  const closeS: Record<MarkKey, string> = {
    ins: a.insClose,
    del: a.delClose,
    subOld: a.subSep + a.subClose,
    subNew: a.subClose,
    hl: a.hlClose,
  };
  const transition = (from: RunState | null, to: RunState | null): string => {
    if (from?.run === to?.run) return '';
    if (from?.key === 'subOld' && to?.key === 'subNew') return a.subSep;
    return (from ? closeS[from.key] : '') + (to ? openS[to.key] : '');
  };
  const blocks: { end: number; items: Item[] }[] = [];
  doc.descendants((node, pos) => {
    if (!node.isTextblock) return true;
    const items: Item[] = [];
    node.forEach((child, off) => {
      const from = pos + 1 + off;
      if (child.type.name === COMMENT) {
        items.push({ from, to: from + 1, key: null, run: null, note: String(child.attrs.note) });
      } else {
        items.push({
          from,
          to: from + child.nodeSize,
          key: markKeyOf(child.marks),
          run: runKeyOf(child.marks),
        });
      }
    });
    blocks.push({ end: pos + 1 + node.content.size, items });
    return false;
  });
  const ins = new Map<number, string>();
  const add = (pos: number, s: string): void => {
    if (s) ins.set(pos, (ins.get(pos) ?? '') + s);
  };
  const notes: string[] = [];
  const comments: number[] = [];
  let state: RunState | null = null;
  const runOf = (it: Item | undefined): RunState | null =>
    it?.key && it.run ? { key: it.key, run: it.run } : null;
  blocks.forEach((b, bi) => {
    for (const it of b.items) {
      const next = runOf(it);
      add(it.from, transition(state, next));
      state = next;
      if (it.note !== undefined) {
        comments.push(it.from);
        notes.push(it.note);
      }
    }
    // A run carries into the next block only if that block starts with it.
    const next = runOf(blocks[bi + 1]?.items[0]);
    if (state !== null && next?.run !== state.run) {
      add(b.end, transition(state, null));
      state = null;
    }
  });
  const tr = new Transform(doc);
  const ops = [
    ...[...ins].map(([pos, s]) => ({ pos, s, order: 1 })),
    ...comments.map((pos) => ({ pos, s: a.note, order: 0 })),
  ].sort((x, y) => y.pos - x.pos || x.order - y.order);
  for (const op of ops) {
    const $p = doc.resolve(op.pos);
    if (op.order === 0) {
      const before = $p.nodeBefore;
      const after = doc.resolve(op.pos + 1).nodeAfter;
      const ms = before && after ? sharedMarks(before.marks, after.marks) : [];
      tr.replaceWith(op.pos, op.pos + 1, schema.text(op.s, ms));
    } else {
      const before = $p.nodeBefore;
      const after = $p.nodeAfter;
      const ms = before && after ? sharedMarks(before.marks, after.marks) : [];
      tr.insert(op.pos, schema.text(op.s, ms));
    }
  }
  for (const name of Object.values(MARKS)) {
    tr.removeMark(0, tr.doc.content.size, schema.marks[name]);
  }
  return { doc: tr.doc, notes };
}

/**
 * Whether a sentinel landed in an attribute (a token inside a link target
 * or title, an image source, raw HTML, an HTML comment, a code block's
 * language). The critic parser reads a mark there, but the editor would
 * show the token as part of the attribute, and a comment's note could not
 * be matched to its place: such a document is not opened in the editor.
 */
function sentinelInAttribute(doc: Node, a: SentinelAlphabet): boolean {
  const sentinels = sentinelSet(a);
  const has = (attrs: Record<string, unknown>): boolean =>
    Object.values(attrs).some((v) => typeof v === 'string' && [...v].some((c) => sentinels.has(c)));
  let found = false;
  doc.descendants((node) => {
    if (found) return false;
    if (has(node.attrs) || node.marks.some((m) => has(m.attrs))) found = true;
    return !found;
  });
  return found;
}

/**
 * Parses Markdown with CriticMarkup into a document, or null when it
 * cannot be shown faithfully (a mark token inside an attribute).
 */
export function loadDoc(
  md: string,
  parse: (s: string) => Node,
  schema: Schema,
  a: SentinelAlphabet
): Node | null {
  const { text, notes } = criticToSentinelText(md, a);
  const tr = new Transform(parse(text));
  if (sentinelInAttribute(tr.doc, a)) return null;
  applySentinels(tr, schema, notes, a);
  return tr.doc;
}

/** Serializes a document to Markdown with CriticMarkup. */
export function saveDoc(
  doc: Node,
  serialize: (d: Node) => string,
  schema: Schema,
  a: SentinelAlphabet
): string {
  const s = docToSentinels(doc, schema, a);
  return sentinelTextToCritic(serialize(s.doc), s.notes, a);
}
