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
import { EditorState, TextSelection } from '@milkdown/prose/state';

import { normalizeCritic, parseCritic, projectCritic } from '../../../utils/critic.js';

import { chooseAlphabet, chooseOpeningAlphabet } from '../../../utils/critic-sentinels.js';
import { loadDocument, saveDoc } from './bridge.js';
import type { LoadedDoc } from './bridge.js';
import type { Core } from './create.js';
import {
  docMarkRanges,
  docMarkRuns,
  earlierKept,
  keyedMarks,
  markKey,
  marksReadBack,
  sameRuns,
  textMarkRuns,
} from './guard.js';
import {
  consumedAsInSource,
  docStrings,
  makeBaseline,
  savePreserving,
  sentinelInDefinition,
} from './preserve.js';
import type { Baseline } from './preserve.js';
import { COMMENT, CRITIC_MARK_NAMES } from './schema.js';
import type { HintKind } from './policy.js';

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
  /** Source offsets of the marks of the opening text that are this session's own. */
  private mineStarts = new Set<number>();
  /** The source offset of each top-level block. */
  private starts: number[] = [];
  /**
   * Blocks of units where an own mark could not be shown as own (a
   * read-only or unaligned unit): a refusal there shows the generic hint,
   * never the earlier-review one.
   */
  private ownerUnknown = new Set<number>();
  /** The review's ownership, kept across loads (view switches). */
  private ownership: Ownership = newOwnership();

  constructor(private readonly core: Core) {}

  /**
   * Opens md in the editor. Returns false (and leaves the editor empty)
   * when the document cannot be represented faithfully, so the caller can
   * fall back to the source view: no free alphabet, a mark or note that
   * would show in another place than the source has it, or with other
   * text (the load invariant: consumedAsInSource, then readOnlyUnits).
   */
  load(md: string, ownership?: object): boolean {
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
    if (sentinelInDefinition(md, a, this.core.parseMdRaw)) return false;
    const held = consumedAsInSource(md, a, parseMd, loaded.doc.childCount, loaded.consumed);
    if (!held) return false;
    const base = makeBaseline(md, loaded.doc, parseMd, a);
    const readOnly = this.readOnlyUnits(md, loaded, base, held);
    if (!readOnly) return false;
    const owner = asOwnership(ownership);
    const own = this.ownMarks(loaded.doc, md, owner, base, readOnly);
    // A fresh state: the opened document, with no undo history.
    view.updateState(
      EditorState.create({
        doc: own.doc,
        plugins: view.state.plugins,
        selection: TextSelection.atStart(own.doc),
      })
    );
    this.baseline = makeBaseline(md, view.state.doc, parseMd, a);
    this.source = md;
    this.readOnly = readOnly;
    this.mineStarts = own.mine;
    this.starts = this.baseline ? blockStarts(this.baseline) : [];
    this.ownerUnknown = own.unknown;
    this.ownership = owner;
    recordOwnership(owner, md, own.mine);
    this.dirty = false;
    return true;
  }

  /**
   * The text part of the load invariant, per unit (blocks joined by a
   * spanning mark): the document's marks (kind, text, note, in order) must
   * be the critic parser's marks of the unit's source range, read in the
   * whole text. A unit holding tokens kept as text (held: in code or an
   * attribute, or a mark around nothing) is read-only and saved from its
   * source, and its other marks must still match exactly (the parser's
   * marks minus the held ones). Any other difference: null (the editor
   * does not open).
   */
  private readOnlyUnits(
    md: string,
    loaded: LoadedDoc,
    base: Baseline | null,
    held: ReadonlySet<number>
  ): Set<number> | null {
    const out = new Set(loaded.attributeBlocks);
    if (!base) return out;
    const { schema } = this.core;
    const starts = blockStarts(base);
    for (let k = 0; k < base.blocks.length; k++) {
      const [i, j] = unitRangeOf(base, k, k);
      k = j;
      const nodes: Node[] = [];
      for (let q = i; q <= j; q++) nodes.push(base.nodes[q]);
      const runs = docMarkRuns(schema.topNodeType.create(null, nodes));
      const end = starts[j] + base.blocks[j].src.length;
      const plain = this.core.plain;
      if (sameRuns(runs, textMarkRuns(md, plain, starts[i], end), true)) continue;
      // The only differences allowed are the held tokens.
      if (!sameRuns(runs, textMarkRuns(md, plain, starts[i], end, held), true)) return null;
      for (let q = i; q <= j; q++) out.add(q);
    }
    return out;
  }

  /**
   * Marks this session's own marks of md as mine (design E5, S-a). A mark
   * is own when the last text this review handed out had it as own at the
   * same place, or when it is new (no mark with its kind, text and new
   * side was in that text: added in the Source view). Every other mark is
   * earlier. Nothing is matched against the version's text. Units are
   * aligned one by one; an own mark in a read-only or unaligned unit stays
   * earlier there (immutable), and its blocks are noted as unknown.
   */
  private ownMarks(
    doc: Node,
    md: string,
    owner: Ownership,
    base: Baseline | null,
    readOnly: Set<number>
  ): { doc: Node; mine: Set<number>; unknown: Set<number> } {
    const mine = new Set<number>();
    const unknown = new Set<number>();
    const wanted = ownStarts(owner, md);
    if (!wanted.size || !base) return { doc, mine, unknown };
    const segs = parseCritic(md).filter((x) => x.kind !== 'text');
    const ranges = docMarkRanges(doc);
    const blockOf = (r: { from: number }[]): number => doc.resolve(r[0].from).index(0);
    const starts = blockStarts(base);
    const tr = new Transform(doc);
    for (let k = 0; k < base.blocks.length; k++) {
      const [i, j] = unitRangeOf(base, k, k);
      k = j;
      const from = starts[i];
      const to = starts[j] + base.blocks[j].src.length;
      const unitSegs = segs.filter((x) => x.start >= from && x.start < to);
      if (!unitSegs.some((x) => wanted.has(x.start))) continue;
      let skip = false;
      for (let q = i; q <= j; q++) if (readOnly.has(q)) skip = true;
      const unitRanges = ranges.filter((r) => {
        const b = blockOf(r);
        return b >= i && b <= j;
      });
      if (skip || unitSegs.length !== unitRanges.length) {
        for (let q = i; q <= j; q++) unknown.add(q);
        continue;
      }
      unitSegs.forEach((seg, n) => {
        if (!wanted.has(seg.start)) return;
        mine.add(seg.start);
        for (const r of unitRanges[n]) markMine(tr, r.from, r.to);
      });
    }
    return { doc: tr.doc, mine, unknown };
  }

  private unitRange(first: number, last: number): [number, number] {
    return unitRangeOf(this.baseline!, first, last);
  }

  private unitSource(i: number, j: number): string {
    return unitSourceOf(this.baseline!, i, j);
  }

  /**
   * The earlier marks of blocks i..j (the whole text when i is undefined),
   * keyed as the save invariant compares them: each with its place in
   * that unit's source with the marks removed (withPlace).
   */
  private earlierOf(i?: number, j?: number, withPlace = true): string[] {
    const whole = i === undefined || j === undefined;
    const src = whole ? this.source : this.unitSource(i, j);
    const at = whole ? 0 : this.starts[i];
    return keyedMarks(src, (st) => this.mineStarts.has(at + st), withPlace)
      .filter((m) => !m.mine)
      .map((m) => m.key);
  }

  /**
   * Whether the top-level blocks first..last of doc (the units they belong
   * to) would save as text the hub accepts and the reviewer meant: the
   * same clean text as their source, marks that read back as the
   * document's, and every earlier mark and note of the source unchanged,
   * in order and in its place (earlierKept). Returns null when they would,
   * 'moved' when only an earlier mark's place would change, and 'generic'
   * otherwise (also for a read-only unit, or blocks that no longer line up
   * with the opening text).
   */
  unitCheck(doc: Node, first: number, last: number): HintKind | null {
    const { schema, serialize } = this.core;
    const clean = (t: string): string => normalizeCritic(projectCritic(t, 'clean'));
    const base = this.baseline;
    if (!base || doc.childCount !== base.blocks.length) return 'generic';
    const [i, j] = this.unitRange(first, last);
    for (let k = i; k <= j; k++) if (this.readOnly.has(k)) return 'generic';
    const src = this.unitSource(i, j);
    const kids: Node[] = [];
    for (let k = i; k <= j; k++) kids.push(doc.child(k));
    const unit = schema.topNodeType.create(null, kids);
    const a = chooseAlphabet(docStrings(unit));
    if (!a) return 'generic';
    const md = saveDoc(unit, serialize, schema, a).replace(/\n+$/, '');
    if (clean(md) !== clean(src)) return 'generic';
    if (marksReadBack(unit, md, this.core.plain) !== null) return 'generic';
    // The written unit must open again as it is shown (for example, an
    // escaped autolink-looking text must not become a link that takes in
    // a mark's closing token).
    if (!this.reopens(md)) return 'generic';
    const runs = docMarkRuns(unit);
    if (earlierKept(this.earlierOf(i, j), md, runs)) return null;
    return earlierKept(this.earlierOf(i, j, false), md, runs, false) ? 'moved' : 'generic';
  }

  /** Whether md opens in the editor (the load checks, without showing it). */
  private reopens(md: string): boolean {
    const { schema, parse, parseMd } = this.core;
    const a = chooseOpeningAlphabet(
      md,
      (text) => `${docStrings(parse(md))}\n${docStrings(parse(text))}`
    );
    if (!a) return false;
    const loaded = loadDocument(md, parse, schema, a);
    if (!loaded || sentinelInDefinition(md, a, this.core.parseMdRaw)) return false;
    const held = consumedAsInSource(md, a, parseMd, loaded.doc.childCount, loaded.consumed);
    if (!held) return false;
    return this.readOnlyUnits(md, loaded, makeBaseline(md, loaded.doc, parseMd, a), held) !== null;
  }

  /** unitCheck passes. */
  unitOk(doc: Node, first: number, last: number): boolean {
    return this.unitCheck(doc, first, last) === null;
  }

  /**
   * The hint for an edit refused in block because it touches a mark the
   * reviewer did not add: the earlier-review hint, or the generic one in a
   * read-only unit and where own and earlier marks could not be told apart
   * (so the wording is never wrong for the reviewer).
   */
  earlierHint(block: number): HintKind {
    return this.ownerUnknown.has(block) || this.readOnly.has(block) ? 'generic' : 'earlier';
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

  /** The review's ownership object (pass it to load to keep own marks own). */
  get ownershipState(): object {
    return this.ownership;
  }

  /**
   * The current text. Without an edit, the opening text. Returns null if
   * the edited document cannot be written (no free sentinel alphabet), or
   * if any unit the serializer wrote would not read back as the document
   * or would change an earlier mark or note (key, order or place). Records
   * which marks of the text are own, for the next load.
   */
  save(): string | null {
    if (!this.dirty) return this.source;
    const { view, schema, serialize } = this.core;
    const doc = view.state.doc;
    const r = savePreserving(doc, this.baseline, serialize, schema, this.readOnly);
    if (!r) return null;
    const ownOut = new Set<number>();
    for (const u of r.units) {
      if (!u.written) {
        // Written from its source: its own marks are the opening text's.
        const from = this.starts[u.i];
        const to = this.starts[u.j] + this.baseline!.blocks[u.j].src.length;
        for (const st of this.mineStarts) if (st >= from && st < to) ownOut.add(st - from + u.at);
        continue;
      }
      const kids: Node[] = [];
      if (u.i < 0) doc.forEach((n) => kids.push(n));
      else for (let k = u.i; k <= u.j; k++) kids.push(doc.child(k));
      const unit = schema.topNodeType.create(null, kids);
      if (marksReadBack(unit, u.md, this.core.plain) !== null) return null;
      const runs = docMarkRuns(unit);
      const before = u.i < 0 ? this.earlierOf() : this.earlierOf(u.i, u.j);
      if (!earlierKept(before, u.md, runs)) return null;
      parseCritic(u.md)
        .filter((x) => x.kind !== 'text')
        .forEach((x, n) => {
          if (runs[n].mine) ownOut.add(x.start + u.at);
        });
    }
    recordOwnership(this.ownership, r.md, ownOut);
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
 * Which marks of a review are the reviewer's own, kept across loads (view
 * switches) by the element that holds the review (design E5, S-a). The
 * page passes the same object for the whole review.
 */
export interface Ownership {
  /** The own marks of the last text handed out, by kind, text, new side and place. */
  own: Map<string, number>;
  /** Every mark of that text, by kind, text and new side; null before the first load. */
  handed: Map<string, number> | null;
}

/** An ownership with no own marks (a new review). */
export function newOwnership(): Ownership {
  return { own: new Map(), handed: null };
}

/** The ownership in an object the page holds (filled on first use). */
function asOwnership(o: object | undefined): Ownership {
  const x = (o ?? {}) as Partial<Ownership>;
  if (!(x.own instanceof Map)) {
    x.own = new Map();
    x.handed = null;
  }
  return x as Ownership;
}

/** Each mark segment of md: source offset, content key, and content key with its place. */
function segmentKeys(md: string): { start: number; content: string; placed: string }[] {
  const out: { start: number; content: string; placed: string }[] = [];
  let clean = 0;
  for (const x of parseCritic(md)) {
    if (x.kind !== 'text') {
      const content = markKey(x);
      out.push({ start: x.start, content, placed: `${content}\u0000@${clean}` });
    }
    if (
      x.kind === 'text' ||
      x.kind === 'deletion' ||
      x.kind === 'highlight' ||
      x.kind === 'substitution'
    ) {
      clean += x.text.replace(/\r\n?/g, '\n').length;
    }
  }
  return out;
}

/** Takes one of key from a multiset; whether there was one. */
function take(m: Map<string, number>, key: string): boolean {
  const n = m.get(key) ?? 0;
  if (n <= 0) return false;
  m.set(key, n - 1);
  return true;
}

/** The source offsets of md's marks that are own under owner. */
function ownStarts(owner: Ownership, md: string): Set<number> {
  const segs = segmentKeys(md);
  const own = new Map(owner.own);
  const handed = owner.handed ? new Map(owner.handed) : null;
  const out = new Set<number>();
  for (const x of segs) {
    if (take(own, x.placed)) {
      out.add(x.start);
      if (handed) take(handed, x.content);
    }
  }
  if (handed) {
    for (const x of segs) if (!out.has(x.start) && !take(handed, x.content)) out.add(x.start);
  }
  return out;
}

/** Records md as the text handed out, with the marks at mine (source offsets) as own. */
function recordOwnership(owner: Ownership, md: string, mine: ReadonlySet<number>): void {
  owner.own = new Map();
  owner.handed = new Map();
  for (const x of segmentKeys(md)) {
    owner.handed.set(x.content, (owner.handed.get(x.content) ?? 0) + 1);
    if (mine.has(x.start)) owner.own.set(x.placed, (owner.own.get(x.placed) ?? 0) + 1);
  }
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
