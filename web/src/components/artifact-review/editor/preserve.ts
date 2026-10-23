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
 * Block-level source preservation. A Markdown serializer normalizes what it
 * writes (list bullets, table padding, backslashes), so re-serializing a whole
 * document after a one-word edit would change text outside the marks.
 * Instead each top-level block keeps the exact source it was loaded from.
 * On save, a block that is still the same node object as at load
 * (ProseMirror shares untouched nodes between states) is written from that
 * source, and only edited blocks go through the serializer.
 *
 * Blocks inside a mark that spans blocks are never kept verbatim on their
 * own; they are re-serialized with their neighbours.
 */

import type { Node, Schema } from '@milkdown/prose/model';

import { chooseAlphabet, criticToSentinelText } from '../../../utils/critic-sentinels.js';
import type { SentinelAlphabet } from '../../../utils/critic-sentinels.js';
import { saveDoc } from './bridge.js';

interface Block {
  src: string;
  /** The source between this block and the next (or the end). */
  after: string;
  /** Blocks joined by a mark that spans the break between them share a unit. */
  unit: number;
}

export interface Baseline {
  lead: string;
  blocks: Block[];
  /** The top-level nodes as loaded, by position. */
  nodes: Node[];
}

interface MdNode {
  position?: { start: { offset?: number }; end: { offset?: number } };
}

/**
 * The baseline of doc, loaded from md under alphabet a, or null when the
 * document's top-level blocks do not line up one to one with the
 * Markdown's. parseMd is the Markdown parser the editor uses.
 */
export function makeBaseline(
  md: string,
  doc: Node,
  parseMd: (s: string) => { children: MdNode[] },
  a: SentinelAlphabet
): Baseline | null {
  const { text, starts, ends } = criticToSentinelText(md, a);
  const kids = parseMd(text).children;
  if (
    kids.length === 0 ||
    kids.length !== doc.childCount ||
    kids.some((k) => k.position?.start.offset === undefined || k.position?.end.offset === undefined)
  ) {
    return null;
  }
  const opens = new Set([a.insOpen, a.delOpen, a.subOpen, a.hlOpen]);
  const closes = new Set([a.insClose, a.delClose, a.subClose, a.hlClose]);
  // Whether text offset i is inside a mark (marks may span blocks).
  const inside: boolean[] = [];
  let open = false;
  for (let i = 0; i <= text.length; i++) {
    inside.push(open);
    if (opens.has(text[i])) open = true;
    else if (closes.has(text[i])) open = false;
  }
  const mdStart = (t: number): number => (t < text.length ? starts[t] : md.length);
  const mdEnd = (t: number): number => (t > 0 ? ends[t - 1] : 0);
  const spans = kids.map((k) => [k.position!.start.offset!, k.position!.end.offset!] as const);
  let unit = 0;
  const blocks: Block[] = spans.map(([s, e], i) => {
    // A mark open at the end of the previous block joins this one to it.
    if (i > 0 && !(inside[spans[i - 1][1]] && inside[s])) unit++;
    const next = i + 1 < spans.length ? mdStart(spans[i + 1][0]) : md.length;
    return { src: md.slice(mdStart(s), mdEnd(e)), after: md.slice(mdEnd(e), next), unit };
  });
  const lead = md.slice(0, mdStart(spans[0][0]));
  const nodes: Node[] = [];
  doc.forEach((n) => nodes.push(n));
  return { lead, blocks, nodes };
}

/** Every string the serializer could write for doc: text, notes and attributes. */
export function docStrings(doc: Node): string {
  const parts: string[] = [];
  const attrs = (o: Record<string, unknown>): void => {
    for (const v of Object.values(o)) if (typeof v === 'string') parts.push(v);
  };
  doc.descendants((n) => {
    if (n.isText) parts.push(n.text ?? '');
    attrs(n.attrs);
    for (const m of n.marks) attrs(m.attrs);
    return true;
  });
  return parts.join('\n');
}

/**
 * Saves doc, writing every unit of blocks unchanged since load from its
 * source and serializing the others (a unit with a mark that spans blocks
 * is serialized as a whole, so the mark stays one run). Units holding a
 * block of verbatim are always written from their source. Returns null when
 * no sentinel alphabet is free for the edited text, or when blocks were
 * added or removed while verbatim is not empty.
 */
export function savePreserving(
  doc: Node,
  base: Baseline | null,
  serialize: (d: Node) => string,
  schema: Schema,
  verbatim: ReadonlySet<number> = new Set()
): { md: string; rewritten: number } | null {
  const a = chooseAlphabet(docStrings(doc));
  if (!a) return null;
  if (!base || doc.childCount !== base.blocks.length) {
    if (verbatim.size > 0) return null;
    return { md: saveDoc(doc, serialize, schema, a), rewritten: doc.childCount };
  }
  let md = base.lead;
  let rewritten = 0;
  let i = 0;
  while (i < base.blocks.length) {
    let j = i;
    while (j + 1 < base.blocks.length && base.blocks[j + 1].unit === base.blocks[i].unit) j++;
    let same = true;
    for (let k = i; k <= j; k++) if (doc.child(k) !== base.nodes[k]) same = false;
    for (let k = i; k <= j; k++) if (verbatim.has(k)) same = true;
    if (same) {
      for (let k = i; k <= j; k++) {
        md += base.blocks[k].src;
        if (k < j) md += base.blocks[k].after;
      }
    } else {
      const kids: Node[] = [];
      for (let k = i; k <= j; k++) kids.push(doc.child(k));
      rewritten += kids.length;
      md += saveDoc(schema.topNodeType.create(null, kids), serialize, schema, a).replace(
        /\n+$/,
        ''
      );
    }
    md += base.blocks[j].after;
    i = j + 1;
  }
  return { md, rewritten };
}
