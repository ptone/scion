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
 * One editing session of the review editor: the text it was opened with,
 * the document built from it, and the save that writes unchanged blocks
 * from their source. Saving without an edit returns the opening text as
 * is; it never goes through the serializer.
 */

import type { Node } from '@milkdown/prose/model';
import { Transform } from '@milkdown/prose/transform';
import { TextSelection } from '@milkdown/prose/state';

import { normalizeCritic, parseCritic, projectCritic } from '../../../utils/critic.js';

import { chooseAlphabet, chooseOpeningAlphabet } from '../../../utils/critic-sentinels.js';
import { loadDocument, saveDoc } from './bridge.js';
import type { LoadedDoc } from './bridge.js';
import type { Core } from './create.js';
import {
  docMarkRanges,
  docMarkRuns,
  earlierKept,
  markKey,
  marksReadBack,
  ownRuns,
  sameRuns,
  textMarkRuns,
} from './guard.js';
import type { MarkRun } from './guard.js';
import { consumedAsInSource, docStrings, makeBaseline, savePreserving } from './preserve.js';
import type { Baseline } from './preserve.js';
import { COMMENT, CRITIC_MARK_NAMES } from './schema.js';
import { LOAD_META } from './track.js';

export class ReviewSession {
  private source = '';
  private baseline: Baseline | null = null;
  private dirty = false;
  /**
   * Read-only top-level blocks, always saved from their source: blocks
   * holding mark tokens inside attributes, and the units whose source the
   * editor does not read as the critic parser does (design 4.2, item 3).
   */
  private readOnly = new Set<number>();
  /** The source's marks not added by this reviewer, with their source offsets. */
  private earlier: { start: number; key: string }[] = [];
  /** The source offset of each top-level block. */
  private starts: number[] = [];

  constructor(private readonly core: Core) {}

  /**
   * Opens md in the editor. Returns false (and leaves the editor empty)
   * when the document cannot be represented faithfully, so the caller can
   * fall back to the source view: no free alphabet, a mark or note that
   * would show in another place than the source has it, or with other
   * text (the load invariant: consumedAsInSource, then readOnlyUnits).
   */
  load(md: string, baseline?: string): boolean {
    const { view, schema, parse, parseMd } = this.core;
    // The alphabet must be free in the source and in what the Markdown
    // parser makes of the sentinel form (character references decode to
    // candidate characters, which would otherwise read as sentinels).
    const a = chooseOpeningAlphabet(
      md,
      (text) => `${docStrings(parse(md))}\n${docStrings(parse(text))}`
    );
    if (!a) return false;
    const loaded = loadDocument(md, parse, schema, a);
    if (!loaded) return false;
    if (!consumedAsInSource(md, a, parseMd, loaded.doc.childCount, loaded.consumed)) return false;
    const base = makeBaseline(md, loaded.doc, parseMd, a);
    const readOnly = this.readOnlyUnits(md, loaded, base);
    if (!readOnly) return false;
    const own =
      baseline === undefined || !base
        ? { doc: loaded.doc, mine: new Set<number>() }
        : this.ownMarks(loaded.doc, md, baseline, base, readOnly);
    const doc = own.doc;
    const tr = view.state.tr.replaceWith(0, view.state.doc.content.size, doc.content);
    tr.setSelection(TextSelection.atStart(tr.doc));
    tr.setMeta(LOAD_META, true).setMeta('addToHistory', false);
    view.dispatch(tr);
    this.baseline = makeBaseline(md, view.state.doc, parseMd, a);
    this.source = md;
    this.readOnly = readOnly;
    this.earlier = parseCritic(md)
      .filter((x) => x.kind !== 'text' && !own.mine.has(x.start))
      .map((x) => ({ start: x.start, key: markKey(x) }));
    this.starts = this.baseline ? blockStarts(this.baseline) : [];
    this.dirty = false;
    return true;
  }

  /**
   * The text part of the load invariant, per unit (blocks joined by a
   * spanning mark): the document's marks (kind, text, note, in order) must
   * be the critic parser's marks of the unit's source range, read in the
   * whole text. Where they differ because tokens stay text (inside code or
   * attributes), the unit is read-only and saved from its source; where
   * they differ for no such reason, null (the editor does not open).
   */
  private readOnlyUnits(md: string, loaded: LoadedDoc, base: Baseline | null): Set<number> | null {
    const out = new Set(loaded.attributeBlocks);
    if (!base) return out;
    const { schema } = this.core;
    const starts = blockStarts(base);
    for (let k = 0; k < base.blocks.length; k++) {
      const [i, j] = unitRangeOf(base, k, k);
      k = j;
      const nodes: Node[] = [];
      for (let q = i; q <= j; q++) nodes.push(base.nodes[q]);
      const unit = schema.topNodeType.create(null, nodes);
      const end = starts[j] + base.blocks[j].src.length;
      const want = textMarkRuns(md, this.core.plain, starts[i], end);
      if (sameRuns(docMarkRuns(unit), want, true)) continue;
      let reason = false;
      for (let q = i; q <= j; q++) {
        if (loaded.attributeBlocks.has(q) || loaded.literalBlocks.has(q)) reason = true;
      }
      if (!reason) return null;
      for (let q = i; q <= j; q++) out.add(q);
    }
    return out;
  }

  /**
   * Marks the marks and comments of md that the version's own text
   * (baseline) does not have as this session's (mine), so a reviewer's own
   * marks stay theirs across a switch to the Source view and back. A mark
   * is earlier when the version's text has the same mark at the same
   * offset of the clean reading (a review never moves that offset: its
   * insertions are not in it, its deletions and highlights keep their
   * text). Units are aligned one by one; read-only units are skipped.
   * Returns the document and the source offsets of the marks it made mine.
   */
  private ownMarks(
    doc: Node,
    md: string,
    baseline: string,
    base: Baseline,
    readOnly: Set<number>
  ): { doc: Node; mine: Set<number> } {
    const mine = new Set<number>();
    const earlier = new Map<string, number>();
    for (const x of cleanKeyed(baseline)) earlier.set(x.key, (earlier.get(x.key) ?? 0) + 1);
    const segs = cleanKeyed(md);
    // Each run of the document, by the top-level block it starts in.
    const ranges = docMarkRanges(doc);
    const blockOf = (r: { from: number }[]): number => doc.resolve(r[0].from).index(0);
    const starts = blockStarts(base);
    const tr = new Transform(doc);
    for (let k = 0; k < base.blocks.length; k++) {
      const [i, j] = unitRangeOf(base, k, k);
      k = j;
      let skip = false;
      for (let q = i; q <= j; q++) if (readOnly.has(q)) skip = true;
      if (skip) continue;
      const from = starts[i];
      const to = starts[j] + base.blocks[j].src.length;
      const unitSegs = segs.filter((x) => x.start >= from && x.start < to);
      const unitRanges = ranges.filter((r) => {
        const b = blockOf(r);
        return b >= i && b <= j;
      });
      if (unitSegs.length !== unitRanges.length) continue;
      unitSegs.forEach((seg, n) => {
        const left = earlier.get(seg.key) ?? 0;
        if (left > 0) {
          earlier.set(seg.key, left - 1);
          return;
        }
        mine.add(seg.start);
        for (const r of unitRanges[n]) markMine(tr, r.from, r.to);
      });
    }
    return { doc: tr.doc, mine };
  }

  private unitRange(first: number, last: number): [number, number] {
    return unitRangeOf(this.baseline!, first, last);
  }

  private unitSource(i: number, j: number): string {
    return unitSourceOf(this.baseline!, i, j);
  }

  /** The earlier marks of blocks i..j (all of them when i is undefined). */
  private earlierOf(i?: number, j?: number): string[] {
    if (i === undefined || j === undefined) return this.earlier.map((x) => x.key);
    const from = this.starts[i];
    const to = this.starts[j] + this.baseline!.blocks[j].src.length;
    return this.earlier.filter((x) => x.start >= from && x.start < to).map((x) => x.key);
  }

  /**
   * Whether the top-level blocks first..last of doc (the units they belong
   * to) would save as text the hub accepts and the reviewer meant: the
   * same clean text as their source, marks that read back as the
   * document's, and every earlier mark and note of the source unchanged
   * and in order (earlierKept: with the read-back, the document's own
   * earlier marks are then unchanged too). A read-only unit refuses every
   * edit.
   */
  unitOk(doc: Node, first: number, last: number): boolean {
    const { schema, serialize } = this.core;
    const clean = (t: string): string => normalizeCritic(projectCritic(t, 'clean'));
    const base = this.baseline;
    if (!base || doc.childCount !== base.blocks.length) {
      if (this.readOnly.size > 0) return false;
      const r = savePreserving(doc, base, serialize, schema);
      return (
        !!r &&
        clean(r.md) === clean(this.source) &&
        earlierKept(this.earlierOf(), r.md, ownRuns(doc))
      );
    }
    const [i, j] = this.unitRange(first, last);
    for (let k = i; k <= j; k++) if (this.readOnly.has(k)) return false;
    const src = this.unitSource(i, j);
    const kids: Node[] = [];
    for (let k = i; k <= j; k++) kids.push(doc.child(k));
    const unit = schema.topNodeType.create(null, kids);
    const a = chooseAlphabet(docStrings(unit));
    if (!a) return false;
    const md = saveDoc(unit, serialize, schema, a).replace(/\n+$/, '');
    if (clean(md) !== clean(src)) return false;
    if (marksReadBack(unit, md, this.core.plain) !== null) return false;
    return earlierKept(this.earlierOf(i, j), md, ownRuns(unit));
  }

  /**
   * The save-time mark check (design 4.6, item 3), per unit: every unit
   * the serializer writes must read back as the document's marks. Units
   * written from their source (unchanged since load, or read-only) are not
   * compared. Returns the first difference, or null.
   */
  marksCheck(): { doc: MarkRun[]; text: MarkRun[] } | null {
    const base = this.baseline;
    const { view, schema, serialize } = this.core;
    const doc = view.state.doc;
    if (!base || doc.childCount !== base.blocks.length) return null;
    for (let k = 0; k < base.blocks.length; k++) {
      const [i, j] = this.unitRange(k, k);
      k = j;
      let unchanged = true;
      let readOnly = false;
      for (let q = i; q <= j; q++) {
        if (doc.child(q) !== base.nodes[q]) unchanged = false;
        if (this.readOnly.has(q)) readOnly = true;
      }
      if (unchanged || readOnly) continue;
      const kids: Node[] = [];
      for (let q = i; q <= j; q++) kids.push(doc.child(q));
      const unit = schema.topNodeType.create(null, kids);
      const a = chooseAlphabet(docStrings(unit));
      if (!a) continue;
      const md = saveDoc(unit, serialize, schema, a).replace(/\n+$/, '');
      const diff = marksReadBack(unit, md, this.core.plain);
      if (diff) return diff;
    }
    return null;
  }

  /** The read-only top-level blocks, in order. */
  readOnlyBlocks(): number[] {
    return [...this.readOnly].sort((x, y) => x - y);
  }

  /** Records that the document changed after load. */
  markDirty(): void {
    this.dirty = true;
  }

  get isDirty(): boolean {
    return this.dirty;
  }

  /**
   * The current text. Without an edit, the opening text. Returns null if
   * the edited document cannot be written (no free sentinel alphabet).
   */
  save(): string | null {
    if (!this.dirty) return this.source;
    const { view, schema, serialize } = this.core;
    const doc = view.state.doc;
    const r = savePreserving(doc, this.baseline, serialize, schema, this.readOnly);
    // The save invariant: no earlier mark or note changes.
    if (!r || !earlierKept(this.earlierOf(), r.md, ownRuns(doc))) return null;
    return r.md;
  }
}

/** The source offset of each top-level block. */
function blockStarts(base: Baseline): number[] {
  const out: number[] = [];
  let at = base.lead.length;
  for (const b of base.blocks) {
    out.push(at);
    at += b.src.length + b.after.length;
  }
  return out;
}

/** The unit (blocks joined by a spanning mark) around blocks first..last. */
function unitRangeOf(base: Baseline, first: number, last: number): [number, number] {
  let i = first;
  let j = last;
  while (i > 0 && base.blocks[i - 1].unit === base.blocks[i].unit) i--;
  while (j + 1 < base.blocks.length && base.blocks[j + 1].unit === base.blocks[j].unit) j++;
  return [i, j];
}

/** The source text of blocks i..j. */
function unitSourceOf(base: Baseline, i: number, j: number): string {
  let src = '';
  for (let k = i; k <= j; k++) src += base.blocks[k].src + (k < j ? base.blocks[k].after : '');
  return src;
}

/**
 * The marks of md, each keyed by kind, text, new side and its offset in
 * the clean reading, with its source offset.
 */
function cleanKeyed(md: string): { key: string; start: number }[] {
  const out: { key: string; start: number }[] = [];
  let clean = 0;
  for (const x of parseCritic(md)) {
    if (x.kind !== 'text') {
      out.push({
        key: `${x.kind}\u0000${x.text}\u0000${x.next ?? ''}\u0000${clean}`,
        start: x.start,
      });
    }
    if (
      x.kind === 'text' ||
      x.kind === 'deletion' ||
      x.kind === 'highlight' ||
      x.kind === 'substitution'
    ) {
      clean += x.text.length;
    }
  }
  return out;
}

/** Marks the critic mark or comment at from..to as this session's. */
function markMine(tr: Transform, from: number, to: number): void {
  const node = tr.doc.nodeAt(from);
  if (node?.type.name === COMMENT) {
    tr.setNodeMarkup(from, undefined, { ...node.attrs, mine: true });
    return;
  }
  tr.doc.nodesBetween(from, to, (n) => {
    if (!n.isInline) return true;
    for (const m of n.marks) {
      if (!CRITIC_MARK_NAMES.has(m.type.name)) continue;
      tr.removeMark(from, to, m);
      tr.addMark(from, to, m.type.create({ ...m.attrs, mine: true }));
    }
    return false;
  });
}
