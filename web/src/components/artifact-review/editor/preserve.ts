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
 * Source preservation. A Markdown serializer normalizes what it writes
 * (list bullets, table padding, backslashes), so re-serializing a whole
 * document after a one-word edit would change text outside the marks.
 * Instead each top-level block keeps the exact source it was loaded from:
 *
 * - a block that is still the same node object as at load (ProseMirror
 *   shares untouched nodes between states) is written from that source;
 * - an edited block is written by the three-way patch (diff.ts): the
 *   serializer's text of the block before and after the edit are diffed,
 *   and that diff is applied to the block's source, so the serializer's
 *   style cancels out and only the edit lands;
 * - the items of a top-level list and the rows of a top-level table are
 *   units of their own: unchanged ones are written from their source, and
 *   only an edited one is patched.
 *
 * Blocks inside a mark that spans blocks are never written on their own;
 * they form one unit with their neighbours (and a list or table holding a
 * mark that spans two of its items or rows is not split into them).
 */

import type { Node, Schema } from '@milkdown/prose/model';

import {
  chooseAlphabet,
  criticToSentinelText,
  readTag,
  sentinelSet,
} from '../../../utils/critic-sentinels.js';
import type { SentinelAlphabet } from '../../../utils/critic-sentinels.js';
import { saveDoc } from './bridge.js';
import { breakOf } from './schema.js';
import { diff, rebase } from './diff.js';
import type { Edit } from './diff.js';
import type { Consumed } from './bridge.js';
import type { MdNode as MdTreeNode } from './create.js';

interface Block {
  src: string;
  /** The source between this block and the next (or the end). */
  after: string;
  /** Blocks joined by a mark that spans the break between them share a unit. */
  unit: number;
  /**
   * For a top-level list or table: where each item or row lies in src,
   * one per child of the block's node.
   */
  subs?: { from: number; to: number }[];
}

export interface Baseline {
  lead: string;
  blocks: Block[];
  /** The top-level nodes as loaded, by position. */
  nodes: Node[];
  /**
   * Per load-time unit or item or row node: the serializer's text of it
   * and the diff from its source to that text, which do not change while
   * it is edited (computed on its first edit).
   */
  written: Map<Node, Map<number, { text: string; toSource: Edit[] | null }>>;
  /**
   * writeUnit's result per current first node of a unit, with the unit's
   * position and nodes: nodes do not change, so the same nodes at the same
   * place give the same text (the whole-document check of a hazardous
   * document reads every edited unit on each edit). The serializer, schema
   * and parser a session passes are the same for every call on a baseline.
   */
  units: WeakMap<Node, { i: number; j: number; nodes: Node[]; md: string | null }>;
}

interface MdNode {
  type?: string;
  position?: { start: { offset?: number }; end: { offset?: number } };
  children?: MdNode[];
}

/** The Markdown parser's node types whose children are units of their own. */
const SUB_UNIT_PARENTS = new Set(['list', 'table']);

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
  const { text, starts, ends } = criticToSentinelText(md, a, true);
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
    const block: Block = {
      src: md.slice(mdStart(s), mdEnd(e)),
      after: md.slice(mdEnd(e), next),
      unit,
    };
    const subs = subUnits(kids[i], doc.child(i), inside);
    if (subs) {
      const at = mdStart(s);
      block.subs = subs.map(([cs, ce]) => ({ from: mdStart(cs) - at, to: mdEnd(ce) - at }));
    }
    return block;
  });
  const lead = md.slice(0, mdStart(spans[0][0]));
  const nodes: Node[] = [];
  doc.forEach((n) => nodes.push(n));
  return { lead, blocks, nodes, written: new Map(), units: new WeakMap() };
}

/**
 * The sentinel-text spans of the items of a list or the rows of a table,
 * one per child of node, or null: another kind of block, children that do
 * not line up with the node's, or a mark that spans two of them.
 */
function subUnits(md: MdNode, node: Node, inside: readonly boolean[]): [number, number][] | null {
  if (!md.type || !SUB_UNIT_PARENTS.has(md.type)) return null;
  const kids = md.children ?? [];
  if (kids.length === 0 || kids.length !== node.childCount) return null;
  const spans: [number, number][] = [];
  for (const k of kids) {
    const from = k.position?.start.offset;
    const to = k.position?.end.offset;
    if (from === undefined || to === undefined) return null;
    if (
      spans.length &&
      (from < spans[spans.length - 1][1] || (inside[spans[spans.length - 1][1]] && inside[from]))
    ) {
      return null;
    }
    spans.push([from, to]);
  }
  return spans;
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
 * The text of top-level blocks as the serializer writes them (Markdown
 * with CriticMarkup, without the trailing line break), with a sentinel
 * alphabet free in every node of free; null when there is none.
 */
function serializeBlocks(
  nodes: readonly Node[],
  serialize: (d: Node) => string,
  schema: Schema,
  free: readonly Node[] = nodes
): string | null {
  const a = chooseAlphabet(free.map(docStrings).join('\n'));
  if (!a) return null;
  const doc = schema.topNodeType.create(null, [...nodes]);
  return saveDoc(doc, serialize, schema, a).replace(/\n+$/, '');
}

/**
 * The serializer's text of child c of a list or table block, written on
 * its own: an item in a list with the block's attributes, a row in a table
 * with the original block's header row (or, for the header row, its
 * first row), keeping only that row's line. wrap is the block as loaded,
 * so the text before and after an edit differ only by the edit.
 */
function serializeChild(
  wrap: Node,
  child: Node,
  c: number,
  serialize: (d: Node) => string,
  schema: Schema,
  free: readonly Node[]
): string | null {
  if (wrap.type.name === 'table') {
    if (wrap.childCount < 2) return null;
    const rows = c === 0 ? [child, wrap.child(1)] : [wrap.child(0), child];
    const text = serializeBlocks([wrap.type.create(wrap.attrs, rows)], serialize, schema, free);
    const lines = text?.split('\n');
    if (!lines || lines.length !== 3) return null;
    return lines[c === 0 ? 0 : 2];
  }
  return serializeBlocks([wrap.type.create(wrap.attrs, [child])], serialize, schema, free);
}

/** Whether two documents have the same textblocks with the same inline content. */
function sameInline(a: Node, b: Node): boolean {
  const blocks = (d: Node): Node[] => {
    const out: Node[] = [];
    d.descendants((n) => {
      if (n.isTextblock) out.push(n);
      return !n.isTextblock;
    });
    return out;
  };
  const x = blocks(a);
  const y = blocks(b);
  return (
    x.length === y.length && x.every((n, k) => n.type === y[k].type && n.content.eq(y[k].content))
  );
}

/**
 * The three-way patch of one unit: src (the unit's source) with the edit
 * from before to after applied, where both are the serializer's text
 * (before with its diff from src, from the baseline's cache). The
 * patch is kept only when it reads (in context: the text around it, as
 * Markdown) with the same inline content as after; otherwise, and when it
 * does not apply cleanly, the result is after, which the guard then judges
 * like any other text. A patch can apply and still land in the wrong
 * place: an insertion after a character the source wrote as a character
 * reference would land inside the reference.
 */
function patch(
  src: string,
  before: { text: string; toSource: Edit[] | null },
  after: string,
  parse: (md: string) => Node,
  context: (unit: string) => string = (unit) => unit
): string {
  const r = rebase(src, before.text, after, before.toSource);
  if (r === null || r === after) return after;
  return sameInline(parse(context(r)), parse(context(after))) ? r : after;
}

/**
 * The serializer's text of the load-time node key (write() computes it)
 * and its diff from src, from base's cache or computed and cached now;
 * null when write() gives null.
 */
function cached(
  base: Baseline,
  key: Node,
  src: string,
  write: () => string | null,
  last = -1
): { text: string; toSource: Edit[] | null } | null {
  const hit = base.written.get(key)?.get(last);
  if (hit) return hit;
  const text = write();
  if (text === null) return null;
  const entry = { text, toSource: diff(src, text) };
  let byLast = base.written.get(key);
  if (!byLast) {
    byLast = new Map();
    base.written.set(key, byLast);
  }
  byLast.set(last, entry);
  return entry;
}

/**
 * Writes an edited list or table block item by item or row by row: an
 * unchanged item or row (and the source between them) from its source,
 * an edited one by the three-way patch of its own source. In a list, the
 * items a suggested break added are written with the item before them,
 * and two items with the reviewer's own suggested join between them
 * together (each such group patches its own source, so the items around
 * it are never rewritten). was is the block as loaded, now as it is, src
 * its source and subs where each item or row lies in src. Null when an
 * item or row cannot be written alone, or the items no longer line up.
 */
function writeChildren(
  base: Baseline,
  was: Node,
  now: Node,
  src: string,
  subs: readonly { from: number; to: number }[],
  serialize: (d: Node) => string,
  schema: Schema,
  parse: (md: string) => Node
): string | null {
  const free = [was, now];
  const table = was.type.name === 'table';
  if (table && (now.childCount !== subs.length || hasBreak(now))) return null;
  // Groups of source items c0..c1 and the current items written for them.
  const groups: { c0: number; c1: number; nodes: Node[] }[] = [];
  let c = -1;
  for (let k = 0; k < now.childCount; k++) {
    const n = now.child(k);
    const br = breakOf(n);
    const last = groups[groups.length - 1];
    if (br?.own && br.change === 'ins') {
      if (!last) return null;
      last.nodes.push(n);
      continue;
    }
    c++;
    if (c >= subs.length) return null;
    if (br?.own && br.change === 'del' && last) {
      last.c1 = c;
      last.nodes.push(n);
    } else groups.push({ c0: c, c1: c, nodes: [n] });
  }
  if (c + 1 !== subs.length) return null;
  const style = listStyle(src);
  // A group of an ordered list is numbered from its first item's own number
  // in the source, so an item a break adds after it takes the next number.
  const items = (nodes: Node[], first: number): string | null => {
    const num = was.type.name === 'ordered_list' ? itemNumber(src.slice(subs[first].from)) : null;
    const attrs = num === null ? was.attrs : { ...was.attrs, order: num };
    const t = serializeBlocks([was.type.create(attrs, nodes)], serialize, schema, free);
    return t === null ? null : restyle(t, style);
  };
  let out = '';
  let at = 0;
  for (const g of groups) {
    const wasKids: Node[] = [];
    for (let k = g.c0; k <= g.c1; k++) wasKids.push(was.child(k));
    if (wasKids.length === g.nodes.length && wasKids.every((n, k) => n === g.nodes[k])) continue;
    const slice = src.slice(subs[g.c0].from, subs[g.c1].to);
    const before = cached(
      base,
      wasKids[0],
      slice,
      () =>
        table
          ? serializeChild(was, wasKids[0], g.c0, serialize, schema, free)
          : items(wasKids, g.c0),
      g.c1
    );
    const after = table
      ? serializeChild(was, g.nodes[0], g.c0, serialize, schema, free)
      : items(g.nodes, g.c0);
    if (before === null || after === null) return null;
    // A row reads as a row only under its table's header and delimiter
    // lines (the header row: above the delimiter and first row).
    const context = !table
      ? undefined
      : g.c0 === 0
        ? (row: string): string => row + src.slice(subs[0].to, subs[1].to)
        : (row: string): string => src.slice(0, subs[1].from) + row;
    out += src.slice(at, subs[g.c0].from) + patch(slice, before, after, parse, context);
    at = subs[g.c1].to;
  }
  return out + src.slice(at);
}

/**
 * A unit as it is now: baseline blocks bi..bj, which are the current
 * document's top-level blocks di..dj (more of them when a suggested break
 * split a block in two).
 */
export interface UnitSpan {
  bi: number;
  bj: number;
  di: number;
  dj: number;
}

/** How the current document's top-level blocks line up with the baseline's. */
export interface Layout {
  /** Per current block: the baseline block it is (an inserted one: the block before it). */
  of: number[];
  /** Per baseline block: its current block. */
  at: number[];
  /** The units in order, covering every block. */
  units: UnitSpan[];
}

/**
 * The layout of doc on base, or null when its blocks do not line up. A
 * top-level block carrying the reviewer's own suggested break is inserted
 * (it is part of the unit of the block before it); every other block is
 * the next baseline block. Blocks joined by a mark that spans them (from
 * the text) or by the reviewer's own suggested join form one unit.
 */
export function layout(doc: Node, base: Baseline): Layout | null {
  const of: number[] = [];
  const at: number[] = [];
  let k = -1;
  let ok = true;
  doc.forEach((n, _off, idx) => {
    const br = breakOf(n);
    if (br?.own && br.change === 'ins') {
      if (k < 0) ok = false;
      of.push(k);
    } else {
      k++;
      of.push(k);
      at.push(idx);
    }
  });
  if (!ok || at.length !== base.blocks.length) return null;
  // join[k]: baseline block k is in one unit with block k - 1.
  const join = base.blocks.map((b, i) => i > 0 && base.blocks[i - 1].unit === b.unit);
  for (let i = 1; i < at.length; i++) {
    const br = breakOf(doc.child(at[i]));
    if (br?.own && br.change === 'del') join[i] = true;
  }
  const units: UnitSpan[] = [];
  for (let i = 0; i < at.length; i++) {
    if (!join[i] || units.length === 0) {
      units.push({ bi: i, bj: i, di: at[i], dj: at[i] });
    } else units[units.length - 1].bj = i;
  }
  for (const u of units) u.dj = u.bj + 1 < at.length ? at[u.bj + 1] - 1 : doc.childCount - 1;
  return { of, at, units };
}

/** The units of lay covering current blocks first..last, as one span; null when out of range. */
export function spanOf(lay: Layout, first: number, last: number): UnitSpan | null {
  const hit = lay.units.filter((u) => u.dj >= first && u.di <= last);
  if (hit.length === 0) return null;
  return { bi: hit[0].bi, bj: hit[hit.length - 1].bj, di: hit[0].di, dj: hit[hit.length - 1].dj };
}

/** Whether the unit is as loaded: the same block nodes, none inserted. */
function unchanged(doc: Node, base: Baseline, u: UnitSpan): boolean {
  if (u.dj - u.di !== u.bj - u.bi) return false;
  for (let k = 0; k <= u.bj - u.bi; k++)
    if (doc.child(u.di + k) !== base.nodes[u.bi + k]) return false;
  return true;
}

/**
 * The list marker style of a unit's source: the first bullet character
 * and the first ordered-list delimiter that start a line, if any.
 */
export function listStyle(src: string): { bullet?: string; delim?: string } {
  const out: { bullet?: string; delim?: string } = {};
  for (const m of src.matchAll(/^[ \t]*(?:>[ \t]?)*[ \t]*(?:([-*+])|\d{1,9}([.)]))(?=[ \t]|$)/gm)) {
    if (m[1] && !out.bullet) out.bullet = m[1];
    if (m[2] && !out.delim) out.delim = m[2];
  }
  return out;
}

/**
 * text (the serializer's) with its list markers in the style of the
 * unit's source, so a list item a suggested break adds is written with the
 * list's own bullet or delimiter (the serializer writes "-" and "."). The
 * same change is made to the text before and after an edit, so it cancels
 * out of the three-way patch everywhere else.
 */
export function restyle(text: string, style: { bullet?: string; delim?: string }): string {
  let out = text;
  if (style.bullet && style.bullet !== '-') {
    out = out.replace(/^([ \t]*(?:>[ \t]?)*[ \t]*)-(?=[ \t]|$)/gm, `$1${style.bullet}`);
  }
  if (style.delim === ')')
    out = out.replace(/^([ \t]*(?:>[ \t]?)*[ \t]*\d{1,9})\.(?=[ \t]|$)/gm, '$1)');
  return out;
}

/**
 * Writes the unit u of doc: from its source when it is unchanged since
 * load, item by item or row by row for one edited list or table whose
 * items or rows still line up with its source, and by the three-way patch
 * of the unit's source otherwise. Returns the text without the separator
 * after block bj, or null when no sentinel alphabet is free.
 */
export function writeUnit(
  doc: Node,
  base: Baseline,
  u: UnitSpan,
  serialize: (d: Node) => string,
  schema: Schema,
  parse: (md: string) => Node
): string | null {
  if (unchanged(doc, base, u)) return unitSource(base, u.bi, u.bj);
  const nodes: Node[] = [];
  for (let k = u.di; k <= u.dj; k++) nodes.push(doc.child(k));
  const hit = base.units.get(nodes[0]);
  if (
    hit &&
    hit.i === u.bi &&
    hit.j === u.bj &&
    hit.nodes.length === nodes.length &&
    hit.nodes.every((n, k) => n === nodes[k])
  ) {
    return hit.md;
  }
  const md = writeUnitAnew(doc, base, u, serialize, schema, parse);
  base.units.set(nodes[0], { i: u.bi, j: u.bj, nodes, md });
  return md;
}

/** writeUnit without its cache. */
function writeUnitAnew(
  doc: Node,
  base: Baseline,
  u: UnitSpan,
  serialize: (d: Node) => string,
  schema: Schema,
  parse: (md: string) => Node
): string | null {
  const was = base.nodes[u.bi];
  const now = doc.child(u.di);
  const subs = base.blocks[u.bi].subs;
  if (u.bi === u.bj && u.di === u.dj && subs && now.type === was.type) {
    const out = writeChildren(
      base,
      was,
      now,
      base.blocks[u.bi].src,
      subs,
      serialize,
      schema,
      parse
    );
    if (out !== null) return out;
  }
  const wasNodes = base.nodes.slice(u.bi, u.bj + 1);
  const nowNodes: Node[] = [];
  for (let k = u.di; k <= u.dj; k++) nowNodes.push(doc.child(k));
  const free = [...wasNodes, ...nowNodes];
  const src = unitSource(base, u.bi, u.bj);
  const style = listStyle(src);
  const write = (nodes: Node[]): string | null => {
    const t = serializeBlocks(nodes, serialize, schema, free);
    return t === null ? null : restyle(t, style);
  };
  const before = cached(base, was, src, () => write(wasNodes), u.bj);
  const after = write(nowNodes);
  if (before === null || after === null) return null;
  return patch(src, before, after, parse);
}

/** The number an ordered list item's source starts with, or null. */
export function itemNumber(src: string): number | null {
  const m = /^[ \t]*(?:>[ \t]?)*[ \t]*(\d{1,9})[.)]/.exec(src);
  return m ? Number(m[1]) : null;
}

/** Whether a node or any block inside it carries a boundary suggestion. */
export function hasBreak(node: Node): boolean {
  let found = breakOf(node) !== null;
  node.descendants((n) => {
    if (found || !n.isBlock) return false;
    if (breakOf(n)) found = true;
    return !found;
  });
  return found;
}

/** The source text of blocks i..j. */
export function unitSource(base: Baseline, i: number, j: number): string {
  let src = '';
  for (let k = i; k <= j; k++) src += base.blocks[k].src + (k < j ? base.blocks[k].after : '');
  return src;
}

/** The unit (blocks joined by a spanning mark) around baseline blocks first..last. */
export function unitRange(base: Baseline, first: number, last: number): [number, number] {
  let i = first;
  let j = last;
  while (i > 0 && base.blocks[i - 1].unit === base.blocks[i].unit) i--;
  while (j + 1 < base.blocks.length && base.blocks[j + 1].unit === base.blocks[j].unit) j++;
  return [i, j];
}

/**
 * Saves doc: every unit of blocks unchanged since load from its source,
 * and the others through writeUnit (a unit with a mark that spans blocks
 * as a whole, so the mark stays one run). Units holding a baseline block
 * of verbatim are always written from their source. Returns null when no
 * sentinel alphabet is free for the edited text, or when the blocks no
 * longer line up with the baseline while verbatim is not empty.
 */
export function savePreserving(
  doc: Node,
  base: Baseline | null,
  serialize: (d: Node) => string,
  schema: Schema,
  parse: (md: string) => Node,
  verbatim: ReadonlySet<number> = new Set()
): SaveResult | null {
  const lay = base ? layout(doc, base) : null;
  if (!base || !lay) {
    if (verbatim.size > 0) return null;
    const a = chooseAlphabet(docStrings(doc));
    if (!a) return null;
    const whole = saveDoc(doc, serialize, schema, a);
    return {
      md: whole,
      rewritten: doc.childCount,
      units: [{ i: -1, j: -1, di: 0, dj: doc.childCount - 1, md: whole, at: 0, written: true }],
    };
  }
  const parts: string[] = [base.lead];
  let at = base.lead.length;
  let rewritten = 0;
  const units: SaveResult['units'] = [];
  for (const u of lay.units) {
    let same = unchanged(doc, base, u);
    for (let k = u.bi; k <= u.bj; k++) if (verbatim.has(k)) same = true;
    const start = at;
    const unit = same
      ? unitSource(base, u.bi, u.bj)
      : writeUnit(doc, base, u, serialize, schema, parse);
    if (unit === null) return null;
    if (!same) rewritten += u.dj - u.di + 1;
    units.push({ i: u.bi, j: u.bj, di: u.di, dj: u.dj, md: unit, at: start, written: !same });
    parts.push(unit, base.blocks[u.bj].after);
    at += unit.length + base.blocks[u.bj].after.length;
  }
  return { md: joinFlat(parts), rewritten, units };
}

/**
 * parts joined into one string, built once from the whole list. Keep it
 * this way rather than adding the parts to a string one by one: the review
 * editor reads the saved text with the critic parser on every edit of some
 * documents (design E7), and in Chromium that parser runs about three times
 * slower on a string built up piece by piece than on one built at once.
 */
export function joinFlat(parts: readonly string[]): string {
  return parts.join('');
}

/** What savePreserving wrote. */
export interface SaveResult {
  md: string;
  rewritten: number;
  /**
   * Every unit (baseline blocks i..j, current blocks di..dj), its text,
   * where it starts in md, and whether it was written by writeUnit
   * (otherwise it is its source); the whole document as i = j = -1 when
   * the blocks do not line up.
   */
  units: {
    i: number;
    j: number;
    di: number;
    dj: number;
    md: string;
    at: number;
    written: boolean;
  }[];
}

/**
 * The load invariant (I1), positional, on what the sentinel pass consumed
 * (md opened in the tagged sentinel form, where each note sentinel carries
 * its comment's ordinal):
 * - every sentinel lies inside a top-level block of the Markdown parser's
 *   reading (none in the gaps between blocks, where link reference
 *   definitions are);
 * - for every top-level block, the sentinels consumed while visiting it
 *   are exactly that block's sentinels, in order, and each note was taken
 *   by its own ordinal: the note the critic parser reads at that
 *   sentinel's source offset, not the next one in sequence.
 * Returns the source offsets of the tokens kept as text (in code or an
 * attribute, or a mark around nothing), or null when the check fails (the
 * editor then does not open).
 */
export function consumedAsInSource(
  md: string,
  a: SentinelAlphabet,
  parseMd: (s: string) => { children: MdNode[] },
  blockCount: number,
  consumed: Consumed[]
): Set<number> | null {
  const { text, starts } = criticToSentinelText(md, a, true);
  const sentinels = sentinelSet(a);
  const kids = parseMd(text).children;
  const any = [...text].some((c) => sentinels.has(c));
  if (
    kids.length !== blockCount ||
    kids.some((k) => k.position?.start.offset === undefined || k.position?.end.offset === undefined)
  ) {
    return !any && consumed.length === 0 ? new Set() : null;
  }
  const want: { ch: string; ordinal?: number; at: number }[][] = kids.map(() => []);
  let ordinal = 0;
  let k = 0;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (!sentinels.has(c)) continue;
    while (k < kids.length && kids[k].position!.end.offset! <= i) k++;
    if (k >= kids.length || kids[k].position!.start.offset! > i) return null;
    if (c === a.note) {
      want[k].push({ ch: c, ordinal: ordinal++, at: starts[i] });
      i += readTag(text, i, a)?.digits ?? 0;
    } else {
      want[k].push({ ch: c, at: starts[i] });
    }
  }
  const got: Consumed[][] = kids.map(() => []);
  for (const x of consumed) {
    if (x.block < 0 || x.block >= got.length) return null;
    got[x.block].push(x);
  }
  const held = new Set<number>();
  for (let b = 0; b < want.length; b++) {
    if (want[b].length !== got[b].length) return null;
    for (let i = 0; i < want[b].length; i++) {
      const w = want[b][i];
      const g = got[b][i];
      if (w.ch !== g.ch || w.ordinal !== g.ordinal) return null;
      if (g.held) held.add(w.at);
    }
  }
  return held;
}

/**
 * Whether a sentinel of md's tagged sentinel form lies inside a link
 * reference definition at any depth (inside a block quote or a list item
 * too). definitions are the sentinel form's definitions as the Markdown
 * parser reads them before its transforms (which copy definitions onto
 * references and drop them): definitionNodes, or MdRead.definitions.
 */
export function sentinelInDefinition(
  md: string,
  a: SentinelAlphabet,
  definitions: readonly MdTreeNode[]
): boolean {
  const { text } = criticToSentinelText(md, a, true);
  const sentinels = sentinelSet(a);
  return definitions.some((n) => {
    const from = n.position?.start.offset;
    const to = n.position?.end.offset;
    if (from === undefined || to === undefined) return true;
    for (let i = from; i < to; i++) if (sentinels.has(text[i])) return true;
    return false;
  });
}
