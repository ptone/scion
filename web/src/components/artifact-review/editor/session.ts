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
import type { Core } from './create.js';
import { docMarkRanges, docMarkRuns, marksReadBack, sameRuns } from './guard.js';
import type { MarkRun } from './guard.js';
import { docStrings, makeBaseline, savePreserving } from './preserve.js';
import type { Baseline } from './preserve.js';
import { COMMENT, CRITIC_MARK_NAMES } from './schema.js';
import { LOAD_META } from './track.js';

export class ReviewSession {
  private source = '';
  private baseline: Baseline | null = null;
  private dirty = false;
  /** Top-level blocks holding mark tokens inside attributes: read-only, saved from source. */
  private attributeBlocks = new Set<number>();

  constructor(private readonly core: Core) {}

  /**
   * Opens md in the editor. Returns false (and leaves the editor empty)
   * when the document cannot be represented, so the caller can fall back
   * to the source view.
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
    const doc = baseline === undefined ? loaded.doc : this.ownMarks(loaded.doc, md, baseline);
    const tr = view.state.tr.replaceWith(0, view.state.doc.content.size, doc.content);
    tr.setSelection(TextSelection.atStart(tr.doc));
    tr.setMeta(LOAD_META, true).setMeta('addToHistory', false);
    view.dispatch(tr);
    this.baseline = makeBaseline(md, view.state.doc, parseMd, a);
    this.source = md;
    this.attributeBlocks = loaded.attributeBlocks;
    this.dirty = false;
    return true;
  }

  /**
   * Marks the marks and comments of md that the version's own text
   * (baseline) does not have as this session's (mine), so a reviewer's own
   * marks stay theirs across a switch to the Source view and back. The
   * marks are matched in order (a review only adds marks); when the
   * document's marks do not line up with the critic parser's, nothing is
   * marked.
   */
  private ownMarks(doc: Node, md: string, baseline: string): Node {
    const key = (x: { kind: string; text: string; next?: string }): string =>
      `${x.kind}\u0000${x.text}\u0000${x.next ?? ''}`;
    const segs = parseCritic(md).filter((x) => x.kind !== 'text');
    const base = parseCritic(baseline).filter((x) => x.kind !== 'text');
    const ranges = docMarkRanges(doc);
    if (ranges.length !== segs.length) return doc;
    const tr = new Transform(doc);
    let j = 0;
    segs.forEach((seg, i) => {
      if (j < base.length && key(base[j]) === key(seg)) {
        j++;
        return;
      }
      for (const r of ranges[i]) {
        const node = tr.doc.nodeAt(r.from);
        if (node?.type.name === COMMENT) {
          tr.setNodeMarkup(r.from, undefined, { ...node.attrs, mine: true });
          continue;
        }
        tr.doc.nodesBetween(r.from, r.to, (n) => {
          for (const m of n.marks) {
            if (!CRITIC_MARK_NAMES.has(m.type.name)) continue;
            tr.removeMark(r.from, r.to, m);
            tr.addMark(r.from, r.to, m.type.create({ ...m.attrs, mine: true }));
          }
          return false;
        });
      }
    });
    return tr.doc;
  }

  /** The unit (blocks joined by a spanning mark) around blocks first..last. */
  private unitRange(first: number, last: number): [number, number] {
    const base = this.baseline!;
    let i = first;
    let j = last;
    while (i > 0 && base.blocks[i - 1].unit === base.blocks[i].unit) i--;
    while (j + 1 < base.blocks.length && base.blocks[j + 1].unit === base.blocks[j].unit) j++;
    return [i, j];
  }

  /** The source text of blocks i..j. */
  private unitSource(i: number, j: number): string {
    const base = this.baseline!;
    let src = '';
    for (let k = i; k <= j; k++) src += base.blocks[k].src + (k < j ? base.blocks[k].after : '');
    return src;
  }

  /**
   * Whether the top-level blocks first..last of doc (the units they belong
   * to) would save as text the hub accepts: the same clean text as their
   * source, and marks that read back as the document's. A unit whose
   * source already reads differently (the pinned literal cases) is only
   * held to the clean text. With earlier (typed edits, compositions), the
   * marks not added in this session must also read exactly as in the
   * source unit: an edit never changes an earlier reviewer's mark or note.
   * Review commands (comments) pass earlier = false. A unit holding mark
   * tokens inside attributes is read-only.
   */
  unitOk(doc: Node, first: number, last: number, earlier = true): boolean {
    const { schema, serialize } = this.core;
    const clean = (t: string): string => normalizeCritic(projectCritic(t, 'clean'));
    const base = this.baseline;
    if (!base || doc.childCount !== base.blocks.length) {
      if (this.attributeBlocks.size > 0) return false;
      const r = savePreserving(doc, base, serialize, schema);
      return !!r && clean(r.md) === clean(this.source);
    }
    const [i, j] = this.unitRange(first, last);
    for (let k = i; k <= j; k++) if (this.attributeBlocks.has(k)) return false;
    const src = this.unitSource(i, j);
    const kids: Node[] = [];
    const orig: Node[] = [];
    for (let k = i; k <= j; k++) {
      kids.push(doc.child(k));
      orig.push(base.nodes[k]);
    }
    const unit = schema.topNodeType.create(null, kids);
    const a = chooseAlphabet(docStrings(unit));
    if (!a) return false;
    const md = saveDoc(unit, serialize, schema, a).replace(/\n+$/, '');
    if (clean(md) !== clean(src)) return false;
    const plain = this.core.plain;
    const sourceReads = marksReadBack(schema.topNodeType.create(null, orig), src, plain) === null;
    if (!sourceReads) return true;
    if (marksReadBack(unit, md, plain) !== null) return false;
    if (earlier) {
      // The source unit reads back as its loaded nodes (sourceReads), so
      // their earlier marks are the source's earlier marks.
      const kept = (n: Node): MarkRun[] => docMarkRuns(n).filter((r) => !r.mine);
      if (!sameRuns(kept(unit), kept(schema.topNodeType.create(null, orig)), true)) return false;
    }
    return true;
  }

  /**
   * The save-time mark check (design 4.6, item 3), per unit: every unit
   * that is written by the serializer must read back as the document's
   * marks. Units written from their source (unchanged since load, or
   * holding tokens in attributes) and units whose source already reads
   * differently are not compared. Returns the first difference, or null.
   */
  marksCheck(): { doc: MarkRun[]; text: MarkRun[] } | null {
    const base = this.baseline;
    const { view, schema, serialize } = this.core;
    const doc = view.state.doc;
    if (!base || doc.childCount !== base.blocks.length) return null;
    const plain = this.core.plain;
    for (let k = 0; k < base.blocks.length; k++) {
      const [i, j] = this.unitRange(k, k);
      k = j;
      let unchanged = true;
      let attributes = false;
      for (let q = i; q <= j; q++) {
        if (doc.child(q) !== base.nodes[q]) unchanged = false;
        if (this.attributeBlocks.has(q)) attributes = true;
      }
      if (unchanged || attributes) continue;
      const kids: Node[] = [];
      const orig: Node[] = [];
      for (let q = i; q <= j; q++) {
        kids.push(doc.child(q));
        orig.push(base.nodes[q]);
      }
      const src = this.unitSource(i, j);
      if (marksReadBack(schema.topNodeType.create(null, orig), src, plain) !== null) continue;
      const unit = schema.topNodeType.create(null, kids);
      const a = chooseAlphabet(docStrings(unit));
      if (!a) continue;
      const md = saveDoc(unit, serialize, schema, a).replace(/\n+$/, '');
      const diff = marksReadBack(unit, md, plain);
      if (diff) return diff;
    }
    return null;
  }

  /** The read-only top-level blocks (mark tokens inside attributes), in order. */
  readOnlyBlocks(): number[] {
    return [...this.attributeBlocks].sort((x, y) => x - y);
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
    const r = savePreserving(
      view.state.doc,
      this.baseline,
      serialize,
      schema,
      this.attributeBlocks
    );
    return r ? r.md : null;
  }
}
