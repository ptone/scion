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
import { COMMENT, isMine, markKeyOf, runKeyOf } from './schema.js';
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
export interface MarkRun {
  kind: 'insertion' | 'deletion' | 'substitution' | 'highlight' | 'comment';
  /** The mark's text (a replacement's old side); a comment's note. */
  text: string;
  /** A replacement's new side. */
  next?: string;
  /** Added in this editing session (document runs only). */
  mine?: boolean;
}

const KIND_OF: Record<MarkKey, MarkRun['kind']> = {
  ins: 'insertion',
  del: 'deletion',
  subOld: 'substitution',
  subNew: 'substitution',
  hl: 'highlight',
};

/**
 * The marks of a document in order, with their text (a replacement once,
 * with both sides). Runs follow runKeyOf, as the serializer writes them.
 */
export function docMarkRuns(doc: Node): MarkRun[] {
  const out: MarkRun[] = [];
  let prev: string | null = null;
  let cur: MarkRun | null = null;
  let side: 'text' | 'next' = 'text';
  doc.descendants((n) => {
    if (n.type.name === COMMENT) {
      out.push({ kind: 'comment', text: String(n.attrs.note), mine: n.attrs.mine === true });
      prev = null;
      cur = null;
      return false;
    }
    if (n.isBlock) return true; // A run that continues into the next block stays one mark.
    const k = markKeyOf(n.marks);
    const run = runKeyOf(n.marks);
    if (k === null || run === null) {
      prev = null;
      cur = null;
      return false;
    }
    if (run !== prev) {
      if (prev === 'subOld' && k === 'subNew' && cur) {
        side = 'next';
        cur.next = '';
      } else {
        cur = { kind: KIND_OF[k], text: '', mine: isMine(n.marks) };
        side = 'text';
        if (k === 'subOld' || k === 'subNew') cur.next = '';
        if (k === 'subNew') side = 'next';
        out.push(cur);
      }
    }
    const t = n.isText ? (n.text ?? '') : '';
    if (cur) {
      if (side === 'next') cur.next = (cur.next ?? '') + t;
      else cur.text += t;
    }
    prev = run;
    return false;
  });
  return out;
}

/**
 * The document ranges of each mark run, in the order of docMarkRuns (a
 * replacement's two sides are one run; a run may span blocks).
 */
export function docMarkRanges(doc: Node): { from: number; to: number }[][] {
  const out: { from: number; to: number }[][] = [];
  let prev: string | null = null;
  doc.descendants((n, pos) => {
    if (n.type.name === COMMENT) {
      out.push([{ from: pos, to: pos + 1 }]);
      prev = null;
      return false;
    }
    if (n.isBlock) return true;
    const run = runKeyOf(n.marks);
    if (run === null) {
      prev = null;
      return false;
    }
    if (run !== prev && !(prev === 'subOld' && run === 'subNew')) out.push([]);
    out[out.length - 1].push({ from: pos, to: pos + n.nodeSize });
    prev = run;
    return false;
  });
  return out;
}

/** The sequence of mark kinds in a document, in order (a replacement once). */
export function docMarkKinds(doc: Node): string[] {
  return docMarkRuns(doc).map((r) => r.kind);
}

/** The sequence of mark kinds parseCritic reads in text. */
export function textMarkKinds(text: string): string[] {
  return parseCritic(text)
    .filter((s) => s.kind !== 'text')
    .map((s) => s.kind);
}

/** The marks parseCritic reads in text, their texts read by plain() (comments as written). */
export function textMarkRuns(text: string, plain: (md: string) => string): MarkRun[] {
  const out: MarkRun[] = [];
  for (const s of parseCritic(text)) {
    if (s.kind === 'text') continue;
    if (s.kind === 'comment') out.push({ kind: 'comment', text: s.text });
    else if (s.kind === 'substitution') {
      out.push({ kind: 'substitution', text: plain(s.text), next: plain(s.next ?? '') });
    } else out.push({ kind: s.kind, text: plain(s.text) });
  }
  return out;
}

// Whitespace is not compared: line breaks inside a mark read differently
// as Markdown text and as the editor's text.
const squash = (t: string): string => t.replace(/\s+/g, '');

/** Whether two mark sequences agree (kinds; texts when plain; notes exactly). */
export function sameRuns(d: MarkRun[], t: MarkRun[], texts: boolean): boolean {
  return (
    d.length === t.length &&
    d.every((r, i) => {
      const o = t[i];
      if (r.kind !== o.kind) return false;
      if (!texts) return true;
      if (r.kind === 'comment') return r.text === o.text;
      return squash(r.text) === squash(o.text) && squash(r.next ?? '') === squash(o.next ?? '');
    })
  );
}

/**
 * Whether the saved text reads back as the document's marks: the same
 * kinds in the same order and, when plain is given (it reads a mark's
 * Markdown as the editor shows it), the same texts. Returns both readings
 * when they differ, for a diagnostic.
 */
export function marksReadBack(
  doc: Node,
  text: string,
  plain?: (md: string) => string
): { doc: MarkRun[]; text: MarkRun[] } | null {
  const d = docMarkRuns(doc);
  const t = plain
    ? textMarkRuns(text, plain)
    : textMarkKinds(text).map((kind) => ({ kind, text: '' }) as MarkRun);
  return sameRuns(d, t, !!plain) ? null : { doc: d, text: t };
}
