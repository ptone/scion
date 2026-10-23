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
 * Earlier marks in this version of the editor (design E5): a mark is the
 * reviewer's own only if this review made it (kept across a switch to the
 * Source view and back); every other mark is earlier and immutable, and
 * no edit may move one (its place in the source with the marks removed).
 */

import { afterEach, describe, expect, it } from 'vitest';
import { undo } from '@milkdown/prose/history';
import { TextSelection } from '@milkdown/prose/state';

import { ReviewController } from './controller.js';
import type { HintKind } from './policy.js';
import { LOAD_META } from './track.js';
import { corpus, renderContext } from './__fixtures__/core.js';

interface Harness {
  c: ReviewController;
  hints: HintKind[];
  changes: { n: number };
  answer: { note: string };
}

const made: ReviewController[] = [];
afterEach(async () => {
  for (const c of made.splice(0)) await c.destroy();
  document.body.replaceChildren();
});

async function open(md: string): Promise<Harness> {
  const root = document.createElement('div');
  document.body.append(root);
  const hints: HintKind[] = [];
  const changes = { n: 0 };
  const answer = { note: 'ok' };
  const c = await ReviewController.create({
    root,
    render: () => renderContext,
    askNote: () => Promise.resolve(answer.note),
    onHint: (h) => hints.push(h),
    onChange: () => changes.n++,
  });
  made.push(c);
  expect(c.load(md)).toBe(true);
  return { c, hints, changes, answer };
}

function posOf(c: ReviewController, needle: string, offset = 0): number {
  let found = -1;
  c.view.state.doc.descendants((n, pos) => {
    if (found >= 0) return false;
    if (n.isText && n.text!.includes(needle)) found = pos + n.text!.indexOf(needle) + offset;
    return true;
  });
  if (found < 0) throw new Error(`no ${needle}`);
  return found;
}

function type(c: ReviewController, text: string, at: number): void {
  const v = c.view;
  v.dispatch(v.state.tr.setSelection(TextSelection.create(v.state.doc, at)));
  v.dispatch(v.state.tr.insertText(text, at));
}

function comments(c: ReviewController): { note: string; mine: boolean; pos: number }[] {
  const out: { note: string; mine: boolean; pos: number }[] = [];
  c.view.state.doc.descendants((n, pos) => {
    if (n.type.name === 'critic_comment') {
      out.push({ note: String(n.attrs.note), mine: n.attrs.mine === true, pos });
    }
    return true;
  });
  return out;
}

/** Whether any mark or comment of the document is the reviewer's. */
function ownCount(c: ReviewController): number {
  let n = 0;
  let prevMine = false;
  c.view.state.doc.descendants((node) => {
    if (node.type.name === 'critic_comment') {
      if (node.attrs.mine) n++;
      prevMine = false;
      return true;
    }
    if (!node.isText) {
      prevMine = false;
      return true;
    }
    const m = node.marks.find((x) => x.type.name.startsWith('critic_'));
    const mine = m?.attrs.mine === true;
    if (mine && !prevMine) n++;
    prevMine = mine;
    return true;
  });
  return n;
}

// Earlier comments at the start and the end edge of the four inline runs.
const EDGES: [string, string, string][] = [
  ['link end', 'See [the docs{>>check this<<}](https://e.example) for more.\n', 'more.'],
  ['link start', 'See [{>>check this<<}the docs](https://e.example) for more.\n', 'more.'],
  ['emphasis end', 'Make it *clear{>>why?<<}* now.\n', 'now.'],
  ['emphasis start', 'Make it *{>>why?<<}clear* now.\n', 'now.'],
  ['strong end', 'Make it **clear{>>why?<<}** now.\n', 'now.'],
  ['strong start', 'Make it **{>>why?<<}clear** now.\n', 'now.'],
  ['strikethrough end', 'Make it ~~clear{>>why?<<}~~ now.\n', 'now.'],
  ['strikethrough start', 'Make it ~~{>>why?<<}clear~~ now.\n', 'now.'],
];

describe('earlier comments at the edge of an inline run', () => {
  for (const [name, version, after] of EDGES) {
    it(`${name}: an edit elsewhere keeps the comment in its place`, async () => {
      const { c, hints } = await open(version);
      type(c, '!', posOf(c, after, after.length));
      expect(hints).toEqual([]);
      expect(c.flush()).toBe(version.replace(`${after}\n`, `${after}{++!++}\n`));
    });

    it(`${name}: after a switch to Source and back, the comment is still earlier and read-only`, async () => {
      const { c, hints, answer } = await open(version);
      type(c, '!', posOf(c, after, after.length));
      expect(c.load(c.flush()!, c.ownership)).toBe(true);
      const [comment] = comments(c);
      expect(comment.mine).toBe(false);
      answer.note = 'rewritten';
      await c.editNote(comment.pos);
      // Deleting it is refused too.
      const v = c.view;
      v.dispatch(v.state.tr.setSelection(TextSelection.create(v.state.doc, comment.pos + 1)));
      v.dispatch(v.state.tr.delete(comment.pos, comment.pos + 1));
      expect(hints).toEqual(['earlier', 'earlier']);
      expect(c.flush()).toBe(version.replace(`${after}\n`, `${after}{++!++}\n`));
    });

    it(`${name}: an edit that would move the comment is refused, with its own hint`, async () => {
      const { c, hints } = await open(version);
      // The comment loses the formatting it was written under (as if the
      // writer placed it by its neighbours): it would be written outside
      // the run, in another place.
      const [comment] = comments(c);
      const v = c.view;
      const node = v.state.doc.nodeAt(comment.pos)!;
      v.dispatch(
        v.state.tr.setNodeMarkup(comment.pos, undefined, node.attrs, []).setMeta(LOAD_META, true)
      );
      type(c, '!', posOf(c, after, after.length));
      expect(hints).toEqual(['moved']);
      expect(c.view.state.doc.textContent.includes('!')).toBe(false);
      // Save refuses to write the comment in another place, too.
      expect(c.flush()).toBeNull();
    });
  }

  it('positive control: a comment in the middle of link text', async () => {
    const version = 'See [a{>>n<<}b](https://e.example) for more.\n';
    const { c, hints } = await open(version);
    type(c, '!', posOf(c, 'more.', 5));
    expect(hints).toEqual([]);
    expect(c.flush()).toBe('See [a{>>n<<}b](https://e.example) for more.{++!++}\n');
  });

  it('an earlier comment inside emphasis (rev-5a): edit accepted, comment kept, still earlier', async () => {
    const version = 'Plain *word{>>earlier note<<}* end.\n';
    const { c, hints } = await open(version);
    type(c, 'X', posOf(c, 'end', 3));
    expect(c.flush()).toBe('Plain *word{>>earlier note<<}* end{++X++}.\n');
    expect(c.load(c.flush()!, c.ownership)).toBe(true);
    const [comment] = comments(c);
    expect(comment.mine).toBe(false);
    const v = c.view;
    v.dispatch(v.state.tr.setSelection(TextSelection.create(v.state.doc, comment.pos + 1)));
    v.dispatch(v.state.tr.delete(comment.pos, comment.pos + 1));
    expect(hints).toEqual(['earlier']);
  });
});

describe('ownership across reloads', () => {
  // The reload property: after any accepted edit, opening the saved text
  // again with the review's ownership leaves every earlier mark earlier,
  // and exactly the reviewer's mark own.
  const docs = [
    ...EDGES.map(([, md]) => md),
    'a {++x++} b {==h==}{>>n<<} c {--d--} e {~~f~>g~~} h\n',
    ...corpus.map((x) => x.in),
  ];

  it('opening the saved text again keeps every earlier mark earlier', async () => {
    let edited = 0;
    for (const md of docs) {
      const root = document.createElement('div');
      document.body.append(root);
      const c = await ReviewController.create({
        root,
        render: () => renderContext,
        askNote: () => Promise.resolve(null),
        onHint: () => {},
        onChange: () => {},
      });
      made.push(c);
      if (!c.load(md)) continue;
      // Type at the end of the last textblock.
      let end = -1;
      c.view.state.doc.descendants((n, q) => {
        if (n.isTextblock) end = q + n.nodeSize - 1;
        return true;
      });
      if (end < 0) continue;
      type(c, 'Q', end);
      const out = c.flush();
      if (out === null || out === md) continue;
      edited++;
      expect(c.load(out, c.ownership), md).toBe(true);
      expect(ownCount(c), md).toBe(1);
      // A new review of the same text: nothing is the reviewer's.
      expect(c.load(out), md).toBe(true);
      expect(ownCount(c), md).toBe(0);
    }
    expect(edited).toBeGreaterThan(20);
  });
});

describe('composition and history', () => {
  it('a composition reports no change until it settles', async () => {
    const { c, changes } = await open('a dog\n');
    const p = posOf(c, 'dog');
    const v = c.view;
    v.dispatch(v.state.tr.setSelection(TextSelection.create(v.state.doc, p)));
    c.beginComposition();
    const tr = v.state.tr.insertText('犬', p);
    tr.setMeta('composition', 1);
    v.dispatch(tr);
    expect(changes.n).toBe(0);
    c.endComposition();
    c.settleComposition();
    expect(changes.n).toBe(1);
    expect(c.flush()).toBe('a {++犬++}dog\n');
  });

  it('opening another text drops the undo history', async () => {
    const { c } = await open('one two\n');
    type(c, 'X', posOf(c, 'two'));
    expect(c.load('other text\n')).toBe(true);
    const before = c.view.state.doc;
    expect(undo(c.view.state, c.view.dispatch)).toBe(false);
    expect(c.view.state.doc.eq(before)).toBe(true);
  });
});
