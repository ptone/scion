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
import { TextSelection } from '@milkdown/prose/state';

import { normalizeCritic, projectCritic } from '../../../utils/critic.js';

import { chooseAlphabet } from '../../../utils/critic-sentinels.js';
import { loadDoc, saveDoc } from './bridge.js';
import type { Core } from './create.js';
import { marksReadBack } from './guard.js';
import { docStrings, makeBaseline, savePreserving } from './preserve.js';
import type { Baseline } from './preserve.js';
import { LOAD_META } from './track.js';

export class ReviewSession {
  private source = '';
  private baseline: Baseline | null = null;
  private dirty = false;

  constructor(private readonly core: Core) {}

  /**
   * Opens md in the editor. Returns false (and leaves the editor empty)
   * when the document cannot be represented, so the caller can fall back
   * to the source view.
   */
  load(md: string): boolean {
    const { view, schema, parse, parseMd } = this.core;
    // The alphabet must be free in the source and in what the Markdown
    // parser makes of it: character references (&#x2E00;) decode to
    // candidate characters, which would otherwise read as sentinels.
    const a = chooseAlphabet(`${md}\n${docStrings(parse(md))}`);
    if (!a) return false;
    const doc = loadDoc(md, parse, schema, a);
    const tr = view.state.tr.replaceWith(0, view.state.doc.content.size, doc.content);
    tr.setSelection(TextSelection.atStart(tr.doc));
    tr.setMeta(LOAD_META, true).setMeta('addToHistory', false);
    view.dispatch(tr);
    this.baseline = makeBaseline(md, view.state.doc, parseMd, a);
    this.source = md;
    this.dirty = false;
    return true;
  }

  /**
   * Whether the top-level blocks first..last of doc (the units they belong
   * to) would save as text the hub accepts: the same clean text as their
   * source, and marks that read back as the document's. A unit whose
   * source already reads differently (the pinned literal cases) is only
   * held to the clean text.
   */
  unitOk(doc: Node, first: number, last: number): boolean {
    const { schema, serialize } = this.core;
    const clean = (t: string): string => normalizeCritic(projectCritic(t, 'clean'));
    const base = this.baseline;
    if (!base || doc.childCount !== base.blocks.length) {
      const r = savePreserving(doc, base, serialize, schema);
      return !!r && clean(r.md) === clean(this.source);
    }
    let i = first;
    let j = last;
    while (i > 0 && base.blocks[i - 1].unit === base.blocks[i].unit) i--;
    while (j + 1 < base.blocks.length && base.blocks[j + 1].unit === base.blocks[j].unit) j++;
    let src = '';
    const kids: Node[] = [];
    const orig: Node[] = [];
    for (let k = i; k <= j; k++) {
      src += base.blocks[k].src + (k < j ? base.blocks[k].after : '');
      kids.push(doc.child(k));
      orig.push(base.nodes[k]);
    }
    const unit = schema.topNodeType.create(null, kids);
    const a = chooseAlphabet(docStrings(unit));
    if (!a) return false;
    const md = saveDoc(unit, serialize, schema, a).replace(/\n+$/, '');
    if (clean(md) !== clean(src)) return false;
    const plain = this.core.plain;
    if (marksReadBack(unit, md, plain) === null) return true;
    return marksReadBack(schema.topNodeType.create(null, orig), src, plain) !== null;
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
    const r = savePreserving(view.state.doc, this.baseline, serialize, schema);
    return r ? r.md : null;
  }
}
