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
 * The guard: a review may change nothing outside its marks, and the hub
 * refuses a review that does. The editor checks this twice:
 *
 * - per edit, cheaply: the clean reading (insertions and replacements' new
 *   sides dropped, deletions and old sides kept, comments dropped) of each
 *   changed top-level block must be the same before and after;
 * - per save: the marks parseCritic reads from the saved text must be the
 *   marks of the document (kinds and texts), which catches a mark written
 *   in a form that would be read as literal text.
 *
 * The page also runs the full onlyMarksChanged check on the saved text, as
 * it does for the source view.
 */

import type { Node } from '@milkdown/prose/model';

import { parseCritic } from '../../../utils/critic.js';
import { COMMENT, markKeyOf } from './schema.js';
import type { MarkKey } from './schema.js';

/** The clean reading of a node: what the text is with every mark rejected. */
export function cleanReading(node: Node): string {
  const parts: string[] = [];
  const walk = (n: Node): void => {
    if (n.isText) {
      const k = markKeyOf(n.marks);
      if (k !== 'ins' && k !== 'subNew') parts.push(n.text ?? '');
      return;
    }
    if (n.type.name === COMMENT) return;
    if (n.isLeaf) {
      const k = markKeyOf(n.marks);
      if (k !== 'ins' && k !== 'subNew') parts.push(`￼${n.type.name}${JSON.stringify(n.attrs)}`);
      return;
    }
    parts.push(`<${n.type.name}${JSON.stringify(n.attrs)}>`);
    n.forEach(walk);
    parts.push(`</${n.type.name}>`);
  };
  walk(node);
  return parts.join('');
}

/**
 * Whether the clean reading of the top-level blocks between from and to
 * (positions in before) is the same in after, whose blocks are found by
 * mapping. Both documents must have the same number of top-level blocks.
 */
export function cleanUnchanged(before: Node, after: Node, from: number, to: number): boolean {
  if (before.childCount !== after.childCount) return false;
  const first = before.resolve(from).index(0);
  const last = before.resolve(to).index(0);
  for (let i = first; i <= last && i < before.childCount; i++) {
    if (cleanReading(before.child(i)) !== cleanReading(after.child(i))) return false;
  }
  return true;
}

/** One mark as the document holds it and as parseCritic reads it. */
export interface MarkReading {
  kind: 'insertion' | 'deletion' | 'substitution' | 'highlight' | 'comment';
}

const KIND_OF: Record<MarkKey, MarkReading['kind']> = {
  ins: 'insertion',
  del: 'deletion',
  subOld: 'substitution',
  subNew: 'substitution',
  hl: 'highlight',
};

/** The sequence of mark kinds in a document, in order (a replacement once). */
export function docMarkKinds(doc: Node): string[] {
  const out: string[] = [];
  let prev: MarkKey | null = null;
  doc.descendants((n) => {
    if (n.type.name === COMMENT) {
      out.push('comment');
      prev = null;
      return false;
    }
    if (n.isBlock) {
      // A run that continues into the next block stays one mark.
      return true;
    }
    const k = markKeyOf(n.marks);
    if (k !== prev && k !== null && !(prev === 'subOld' && k === 'subNew')) out.push(KIND_OF[k]);
    prev = k;
    return false;
  });
  return out;
}

/** The sequence of mark kinds parseCritic reads in text. */
export function textMarkKinds(text: string): string[] {
  return parseCritic(text)
    .filter((s) => s.kind !== 'text')
    .map((s) => s.kind);
}

/**
 * Whether the saved text reads back as the document's marks. Returns the
 * two sequences when they differ, for a diagnostic.
 */
export function marksReadBack(doc: Node, text: string): { doc: string[]; text: string[] } | null {
  const d = docMarkKinds(doc);
  const t = textMarkKinds(text);
  return d.length === t.length && d.every((k, i) => k === t[i]) ? null : { doc: d, text: t };
}
