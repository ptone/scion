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
 * Suggested paragraph breaks and joins (structural.ts): Enter, Backspace
 * and Delete at block boundaries, deleting and typing across blocks,
 * pasting several blocks, and Shift-Enter, in paragraphs, headings, both
 * list kinds and block quotes; loading boundary marks; ownership; and the
 * code-run and paste rules.
 */

import { afterEach, describe, expect, it } from 'vitest';
import { undo } from '@milkdown/prose/history';
import { Fragment, Slice } from '@milkdown/prose/model';
import type { Node } from '@milkdown/prose/model';
import { splitBlock } from '@milkdown/prose/commands';
import { TextSelection } from '@milkdown/prose/state';
import type { Transaction } from '@milkdown/prose/state';
import { ReplaceStep, Transform } from '@milkdown/prose/transform';

import { onlyMarksChanged, projectCritic } from '../../../utils/critic.js';
import { ReviewController } from './controller.js';
import { docMarkRuns, marksReadBack } from './guard.js';
import { classify, decide } from './policy.js';
import { STRUCT_META } from './track.js';
import { buildDelete, buildPaste, buildSplit } from './structural.js';
import type { Piece } from './structural.js';
import type { HintKind } from './policy.js';
import { ALLOWED_ATTRIBUTES, ALLOWED_TAGS } from './safe-render.js';
import { BLOCK_ATTR, breakOf } from './schema.js';
import { saveDoc } from './bridge.js';
import { docStrings } from './preserve.js';
import { chooseAlphabet } from '../../../utils/critic-sentinels.js';
import { renderContext } from './__fixtures__/core.js';
import { reviewEditorStyles } from '../review-editor-styles.js';

interface Harness {
  c: ReviewController;
  hints: HintKind[];
  root: HTMLElement;
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
  const c = await ReviewController.create({
    root,
    render: () => renderContext,
    askNote: () => Promise.resolve(null),
    onHint: (h) => hints.push(h),
    onChange: () => {},
  });
  made.push(c);
  expect(c.load(md)).toBe(true);
  return { c, hints, root };
}

/** Document position of the offset-th character of the first text containing needle. */
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

/** The end of the content of the textblock holding needle. */
function endOf(c: ReviewController, needle: string): number {
  return c.view.state.doc.resolve(posOf(c, needle)).end();
}

/** The start of the content of the textblock holding needle. */
function startOf(c: ReviewController, needle: string): number {
  return c.view.state.doc.resolve(posOf(c, needle)).start();
}

function select(c: ReviewController, from: number, to = from): void {
  const v = c.view;
  v.dispatch(v.state.tr.setSelection(TextSelection.create(v.state.doc, from, to)));
}

function press(c: ReviewController, key: string, mods: Partial<KeyboardEventInit> = {}): boolean {
  const e = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...mods });
  c.view.dom.dispatchEvent(e);
  return e.defaultPrevented;
}

function typeText(c: ReviewController, text: string): void {
  const v = c.view;
  const { from, to } = v.state.selection;
  v.dispatch(v.state.tr.insertText(text, from, to));
}

/** Pastes text (as text/plain) at the selection, through the editor's paste guard. */
function paste(h: Harness, text: string): void {
  const data = new DataTransfer();
  data.setData('text/plain', text);
  const e = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
  Object.defineProperty(e, 'clipboardData', { value: data });
  h.c.view.dom.dispatchEvent(e);
}

/** The saved text, which must be a review the hub accepts against md. */
function saved(c: ReviewController, md: string): string {
  const out = c.flush();
  expect(out).not.toBeNull();
  expect(onlyMarksChanged(out!, projectCritic(md, 'clean'))).toBe(true);
  return out!;
}

/** The boundary suggestions of a document, in order, as change/own. */
function breaks(doc: Node): string[] {
  const out: string[] = [];
  doc.descendants((n) => {
    const b = breakOf(n);
    if (b) out.push(`${b.change}/${b.own ? 'own' : 'earlier'}`);
    return true;
  });
  return out;
}

/** Reopens text in a fresh editor and returns its pilcrow elements' classes. */
async function pilcrows(text: string): Promise<string[]> {
  const h = await open(text);
  return [...h.root.querySelectorAll('.critic-break-ins, .critic-break-del')].map(
    (el) =>
      `${el.tagName.toLowerCase()}.${el.classList.contains('critic-break-ins') ? 'ins' : 'del'}`
  );
}

/** Asserts a saved review against its source: clean projection, accept projection, reopen. */
async function check(md: string, out: string, want: string, accept: string): Promise<void> {
  expect(out).toBe(want);
  expect(projectCritic(out, 'clean')).toBe(projectCritic(md, 'clean'));
  expect(projectCritic(out, 'accept')).toBe(accept);
  const again = await open(out);
  expect(again.c.flush()).toBe(out);
}

describe('Enter suggests a paragraph break', () => {
  it('in a paragraph', async () => {
    const md = 'ab cd\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, 'cd'));
    expect(press(c, 'Enter')).toBe(true);
    expect(hints).toEqual([]);
    expect(breaks(c.view.state.doc)).toEqual(['ins/own']);
    await check(md, saved(c, md), 'ab {++\n\n++}cd\n', 'ab \n\ncd\n');
    expect(await pilcrows(saved(c, md))).toEqual(['p.ins']);
  });

  it('at the end of a paragraph, then typing: one run across the boundary', async () => {
    const md = 'ab\n';
    const { c } = await open(md);
    select(c, endOf(c, 'ab'));
    press(c, 'Enter');
    typeText(c, 'new');
    await check(md, saved(c, md), 'ab{++\n\nnew++}\n', 'ab\n\nnew\n');
  });

  it('in a heading: the text after the break is a paragraph', async () => {
    const md = '# Title here\n\nNext.\n';
    const { c } = await open(md);
    select(c, posOf(c, 'here'));
    press(c, 'Enter');
    await check(
      md,
      saved(c, md),
      '# Title {++\n\n++}here\n\nNext.\n',
      '# Title \n\nhere\n\nNext.\n'
    );
  });

  it('is refused at the start of a heading (it would turn the heading into a paragraph)', async () => {
    const md = '# Title\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, 'Title'));
    press(c, 'Enter');
    expect(hints).toEqual(['break']);
    expect(c.flush()).toBe(md);
  });

  it('in a bullet list item: a new item with the list bullet', async () => {
    for (const bullet of ['-', '*', '+']) {
      const md = `${bullet} a b\n${bullet} c\n`;
      const { c, hints } = await open(md);
      select(c, posOf(c, 'a b', 2));
      press(c, 'Enter');
      expect(hints).toEqual([]);
      expect(breaks(c.view.state.doc)).toEqual(['ins/own']);
      await check(
        md,
        saved(c, md),
        `${bullet} a {++\n${bullet} ++}b\n${bullet} c\n`,
        `${bullet} a \n${bullet} b\n${bullet} c\n`
      );
    }
  });

  it('in an ordered list item: a new item with the list delimiter, other numbers kept', async () => {
    const cases: [string, string][] = [
      ['1. a b\n2. c\n', '1. a {++\n2. ++}b\n2. c\n'],
      ['1) a b\n2) c\n', '1) a {++\n2) ++}b\n2) c\n'],
      ['3. three x\n7. seven\n', '3. three {++\n4. ++}x\n7. seven\n'],
      ['1. a\n2. b\n3. c d\n', '1. a\n2. b\n3. c {++\n4. ++}d\n'],
      ['1. a\n1. b c\n1. d\n', '1. a\n1. b {++\n2. ++}c\n1. d\n'],
    ];
    for (const [md, want] of cases) {
      const { c, hints } = await open(md);
      const needle = md.includes('b c')
        ? 'b c'
        : md.includes('three')
          ? 'three x'
          : md.includes('c d')
            ? 'c d'
            : 'a b';
      select(c, posOf(c, needle, needle.length - 1));
      press(c, 'Enter');
      expect(hints).toEqual([]);
      const out = saved(c, md);
      expect(out).toBe(want);
      // The new item is an item of the same list.
      const again = await open(out);
      expect(again.c.view.state.doc.childCount).toBe(c.view.state.doc.childCount);
    }
  });

  it('at the end of a list item, then typing', async () => {
    const md = '- a\n- b\n';
    const { c } = await open(md);
    select(c, endOf(c, 'a'));
    press(c, 'Enter');
    typeText(c, 'new');
    await check(md, saved(c, md), '- a{++\n- new++}\n- b\n', '- a\n- new\n- b\n');
  });

  it('in a block quote', async () => {
    const md = '> a b\n';
    const { c } = await open(md);
    select(c, posOf(c, 'a b', 2));
    press(c, 'Enter');
    await check(md, saved(c, md), '> a {++\n>\n> ++}b\n', '> a \n>\n> b\n');
  });

  it('inside a link, emphasis or code span is refused', async () => {
    const md = 'x [link text](https://example.com) *em text* `code text` y\n';
    const { c, hints } = await open(md);
    for (const [needle, hint] of [
      ['link text', 'break'],
      ['em text', 'break'],
      ['code text', 'code'],
    ] as const) {
      select(c, posOf(c, needle, 3));
      press(c, 'Enter');
      expect(hints.pop()).toBe(hint);
    }
    expect(c.flush()).toBe(md);
  });

  it('in a table cell or a code block is refused', async () => {
    const md = '| a | b |\n|---|---|\n| c | d |\n\n```\ncode\n```\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, 'c'));
    press(c, 'Enter');
    select(c, posOf(c, 'code', 2));
    press(c, 'Enter');
    expect(hints).toEqual(['list-table', 'code']);
    expect(c.flush()).toBe(md);
  });

  it('is one history event', async () => {
    const md = 'ab cd\n';
    const { c } = await open(md);
    select(c, posOf(c, 'cd'));
    press(c, 'Enter');
    expect(undo(c.view.state, c.view.dispatch)).toBe(true);
    expect(c.flush()).toBe(md);
    expect(breaks(c.view.state.doc)).toEqual([]);
  });

  it('over a selection marks it deleted, then breaks after it', async () => {
    const md = 'ab xy cd\n';
    const { c } = await open(md);
    select(c, posOf(c, 'xy'), posOf(c, 'xy', 2));
    press(c, 'Enter');
    await check(md, saved(c, md), 'ab {--xy--}{++\n\n++} cd\n', 'ab \n\n cd\n');
  });
});

describe('Backspace and Delete at a boundary suggest a join', () => {
  it('between paragraphs (Backspace at the start, Delete at the end)', async () => {
    const md = 'a\n\nb\n';
    for (const how of ['Backspace', 'Delete']) {
      const { c, hints } = await open(md);
      select(c, how === 'Backspace' ? startOf(c, 'b') : endOf(c, 'a'));
      expect(press(c, how)).toBe(true);
      expect(hints).toEqual([]);
      expect(breaks(c.view.state.doc)).toEqual(['del/own']);
      await check(md, saved(c, md), 'a{--\n\n--}b\n', 'ab\n');
      expect(await pilcrows(saved(c, md))).toEqual(['p.del']);
    }
  });

  it('between a paragraph and a heading, and a heading and a paragraph', async () => {
    const md = 'a\n\n# h\n\nb\n';
    const { c } = await open(md);
    select(c, startOf(c, 'h'));
    press(c, 'Backspace');
    select(c, startOf(c, 'b'));
    press(c, 'Backspace');
    await check(md, saved(c, md), 'a{--\n\n# --}h{--\n\n--}b\n', 'ahb\n');
  });

  it('between list items, of both kinds', async () => {
    for (const [md, want, accept] of [
      ['- a\n- b\n', '- a{--\n- --}b\n', '- ab\n'],
      ['* a\n* b\n', '* a{--\n* --}b\n', '* ab\n'],
      ['1. a\n2. b\n', '1. a{--\n2. --}b\n', '1. ab\n'],
    ]) {
      const { c, hints } = await open(md);
      select(c, startOf(c, 'b'));
      press(c, 'Backspace');
      expect(hints).toEqual([]);
      await check(md, saved(c, md), want, accept);
    }
  });

  it('between paragraphs of a block quote', async () => {
    const md = '> a\n>\n> b\n';
    const { c } = await open(md);
    select(c, startOf(c, 'b'));
    press(c, 'Backspace');
    await check(md, saved(c, md), '> a{--\n>\n> --}b\n', '> ab\n');
  });

  it('a second Backspace at a suggested join changes nothing', async () => {
    const md = 'a\n\nb\n';
    const { c, hints } = await open(md);
    select(c, startOf(c, 'b'));
    press(c, 'Backspace');
    select(c, startOf(c, 'b'));
    press(c, 'Backspace');
    expect(hints).toEqual([]);
    expect(saved(c, md)).toBe('a{--\n\n--}b\n');
  });

  it('into a list, a table or a code block is refused', async () => {
    const md = 'a\n\n- b\n\n```\ncode\n```\n\nc\n\n| x |\n|---|\n| y |\n';
    const { c, hints } = await open(md);
    select(c, startOf(c, 'b'));
    press(c, 'Backspace');
    select(c, startOf(c, 'c'));
    press(c, 'Backspace');
    select(c, startOf(c, 'y'));
    press(c, 'Backspace');
    expect(hints).toEqual(['list-table', 'code']);
    expect(c.flush()).toBe(md);
  });
});

describe('deleting and typing across blocks', () => {
  it('deleting across two paragraphs', async () => {
    const md = 'one\n\ntwo\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, 'one'), endOf(c, 'two'));
    press(c, 'Backspace');
    expect(hints).toEqual([]);
    await check(md, saved(c, md), '{--one\n\ntwo--}\n', '\n');
  });

  it('deleting part of each of three blocks', async () => {
    const md = 'one\n\ntwo\n\nthree\n';
    const { c } = await open(md);
    select(c, posOf(c, 'one', 1), posOf(c, 'three', 2));
    press(c, 'Delete');
    await check(md, saved(c, md), 'o{--ne\n\ntwo\n\nth--}ree\n', 'oree\n');
  });

  it('typing over a selection across blocks', async () => {
    const md = 'one\n\ntwo\n';
    const { c } = await open(md);
    select(c, posOf(c, 'one'), endOf(c, 'two'));
    typeText(c, 'x');
    await check(md, saved(c, md), '{--one\n\ntwo--}{++x++}\n', 'x\n');
  });

  it('deleting across two list items', async () => {
    const md = '- one\n- two\n';
    const { c } = await open(md);
    select(c, posOf(c, 'one', 1), posOf(c, 'two', 1));
    press(c, 'Backspace');
    await check(md, saved(c, md), '- o{--ne\n- t--}wo\n', '- owo\n');
  });

  it('deleting across two table cells is refused', async () => {
    const md = '| a | b |\n|---|---|\n| c | d |\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, 'c'), posOf(c, 'd', 1));
    press(c, 'Backspace');
    expect(hints).toEqual(['list-table']);
    expect(c.flush()).toBe(md);
  });
});

describe('Shift-Enter suggests a line break', () => {
  it('in a paragraph', async () => {
    const md = 'ab cd\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, 'cd'));
    expect(press(c, 'Enter', { shiftKey: true })).toBe(true);
    expect(hints).toEqual([]);
    await check(md, saved(c, md), 'ab {++\\\n++}cd\n', 'ab \\\ncd\n');
    const again = await open(saved(c, md));
    expect(docMarkRuns(again.c.view.state.doc).map((r) => r.kind)).toEqual(['insertion']);
    expect(again.c.readOnlyBlocks()).toEqual([]);
  });

  it('in a heading is refused', async () => {
    const md = '# ab cd\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, 'cd'));
    press(c, 'Enter', { shiftKey: true });
    expect(hints).toEqual(['break']);
    expect(c.flush()).toBe(md);
  });
});

describe('pasting several blocks', () => {
  it('several paragraphs: one insertion run across the boundaries', async () => {
    const md = 'ab\n';
    const h = await open(md);
    select(h.c, posOf(h.c, 'ab', 1));
    paste(h, 'P1\n\nP2');
    expect(h.hints).toEqual([]);
    await check(md, saved(h.c, md), 'a{++P1\n\nP2++}b\n', 'aP1\n\nP2b\n');
  });

  it('a heading among them stays a heading', async () => {
    const md = 'ab\n';
    const h = await open(md);
    select(h.c, posOf(h.c, 'ab', 1));
    paste(h, 'P1\n\n## H');
    await check(md, saved(h.c, md), 'a{++P1\n\n## H++}b\n', 'aP1\n\n## Hb\n');
  });

  it('into a list item: new list items', async () => {
    const md = '- ab\n';
    const h = await open(md);
    select(h.c, posOf(h.c, 'ab', 1));
    paste(h, 'P1\n\nP2');
    await check(md, saved(h.c, md), '- a{++P1\n- P2++}b\n', '- aP1\n- P2b\n');
  });

  it('list items and code are pasted as plain paragraphs', async () => {
    const md = 'ab\n';
    const h = await open(md);
    select(h.c, posOf(h.c, 'ab', 1));
    paste(h, '- i1\n- i2\n\n```\nline one\nline two\n```');
    expect(h.hints).toEqual([]);
    await check(
      md,
      saved(h.c, md),
      'a{++i1\n\ni2\n\nline one\n\nline two++}b\n',
      'ai1\n\ni2\n\nline one\n\nline twob\n'
    );
  });

  it('HTML in a paste refuses it: it is never pasted as an HTML node', async () => {
    const md = 'ab\n';
    const h = await open(md);
    for (const text of ['<div>x</div>\n\nP2', 'P1\n\n<div>\nx\n</div>', 'P1 <b>x</b>\n\nP2']) {
      select(h.c, posOf(h.c, 'ab', 1));
      paste(h, text);
      expect(h.hints.pop()).toBe('generic');
      let html = false;
      h.c.view.state.doc.descendants((n) => {
        if (n.type.name === 'html') html = true;
        return true;
      });
      expect(html).toBe(false);
    }
    expect(h.c.flush()).toBe(md);
  });

  it('is one history event', async () => {
    const md = 'ab\n';
    const h = await open(md);
    select(h.c, posOf(h.c, 'ab', 1));
    paste(h, 'P1\n\nP2\n\nP3');
    expect(undo(h.c.view.state, h.c.view.dispatch)).toBe(true);
    expect(h.c.flush()).toBe(md);
  });
});

describe('boundary marks in the text', () => {
  const cases: [string, string[]][] = [
    ['a{++\n\n++}b\n', ['p.ins']],
    ['a{--\n\n--}b\n', ['p.del']],
    ['a{++\n\nnew++}\n', ['p.ins']],
    ['{--a\n\nb--}\n', ['p.del']],
    ['- a{++\n- ++}b\n', ['li.ins']],
    ['* a{--\n* --}b\n', ['li.del']],
    ['1. a{++\n2. ++}b\n', ['li.ins']],
    ['> a{++\n>\n> ++}b\n', ['p.ins']],
    ['# h{--\n\n--}b\n', ['p.del']],
  ];

  it('open with a pilcrow at each boundary and save byte for byte', async () => {
    for (const [md, want] of cases) {
      expect(await pilcrows(md), md).toEqual(want);
      const { c } = await open(md);
      expect(c.flush()).toBe(md);
      // An edit elsewhere in the block rewrites it with the mark as it was.
      const doc = c.view.state.doc;
      let at = -1;
      doc.descendants((n, pos) => {
        if (at < 0 && n.isText && !n.marks.length) at = pos;
        return at < 0;
      });
      if (at < 0) continue;
      select(c, at);
      typeText(c, 'Z');
      const out = saved(c, md);
      // The edit is taken (not refused), and only it changes the text.
      expect(out, md).toContain('{++Z++}');
      expect(out.replace('{++Z++}', ''), md).toBe(md);
    }
  });

  it('are never the reviewer’s own when loaded', async () => {
    for (const [md] of cases) {
      const { c } = await open(md);
      const doc = c.view.state.doc;
      expect(breaks(doc).length, md).toBeGreaterThan(0);
      expect(
        breaks(doc).every((b) => b.endsWith('/earlier')),
        md
      ).toBe(true);
      expect(docMarkRuns(doc).every((r) => r.mine === false)).toBe(true);
      expect(c.hasOwnMarks()).toBe(false);
    }
  });

  it('a boundary mark around a code fence stays literal text', async () => {
    const md = 'a{++\n\n```\ncode\n```\n\n++}b\n';
    const { c } = await open(md);
    expect(breaks(c.view.state.doc)).toEqual([]);
    expect(c.flush()).toBe(md);
  });
});

describe('ownership', () => {
  it('withdrawing an own break with Backspace really joins the blocks', async () => {
    const md = 'ab cd\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, 'cd'));
    press(c, 'Enter');
    select(c, startOf(c, 'cd'));
    press(c, 'Backspace');
    expect(hints).toEqual([]);
    expect(breaks(c.view.state.doc)).toEqual([]);
    expect(c.view.state.doc.childCount).toBe(1);
    expect(c.flush()).toBe(md);
  });

  it('withdrawing an own new list item with typed text', async () => {
    const md = '- a\n';
    const { c } = await open(md);
    select(c, endOf(c, 'a'));
    press(c, 'Enter');
    typeText(c, 'new');
    select(c, posOf(c, 'a'), endOf(c, 'new'));
    // Deleting the own text and the own break leaves the item as it was.
    select(c, endOf(c, 'a'), endOf(c, 'new'));
    press(c, 'Backspace');
    expect(c.flush()).toBe(md);
  });

  it('joining across an earlier boundary mark is refused', async () => {
    const md = 'a{++\n\n++}b\n\nc{--\n\n--}d\n';
    const { c, hints } = await open(md);
    select(c, startOf(c, 'b'));
    press(c, 'Backspace');
    select(c, startOf(c, 'd'));
    press(c, 'Backspace');
    select(c, posOf(c, 'a'), endOf(c, 'b'));
    press(c, 'Backspace');
    expect(hints).toEqual(['earlier', 'earlier', 'earlier']);
    expect(c.flush()).toBe(md);
  });

  it('after a round trip through the source text, own breaks are earlier', async () => {
    const md = 'ab cd\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, 'cd'));
    press(c, 'Enter');
    const out = saved(c, md);
    expect(c.load(out)).toBe(true);
    expect(breaks(c.view.state.doc)).toEqual(['ins/earlier']);
    select(c, startOf(c, 'cd'));
    press(c, 'Backspace');
    expect(hints).toEqual(['earlier']);
    expect(c.flush()).toBe(out);
  });
});

describe('code runs and breaks', () => {
  it('a break between two lone backticks of different lengths keeps both projections', async () => {
    const md = 'a `` b ` c\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, ' ` c'));
    press(c, 'Enter');
    expect(hints).toEqual([]);
    const out = saved(c, md);
    expect(out).toBe('a `` b{++\n\n++} ` c\n');
    expect(projectCritic(out, 'clean')).toBe(md);
    expect(projectCritic(out, 'accept')).toBe('a `` b\n\n ` c\n');
  });

  it('a break that changes the backtick runs of a line run shared with another block is refused', async () => {
    // The heading and the paragraph are one line run (no blank line
    // between them), and both hold backticks; none pair today, but a blank
    // line inside the paragraph changes the runs the critic parser pairs.
    const md = '# h ```\nb `` c ` d\n';
    const { c, hints } = await open(md);
    expect(c.readOnlyBlocks()).toEqual([]);
    select(c, posOf(c, ' ` d'));
    press(c, 'Enter');
    expect(hints).toEqual(['code-run']);
    expect(c.flush()).toBe(md);
  });

  it('a break in a block whose code the critic parser reads across its edge is refused', async () => {
    const md = '# a `\nb ` c\n';
    const { c, hints } = await open(md);
    expect(c.isHazardous).toBe(true);
    select(c, posOf(c, ' ` c'));
    press(c, 'Enter');
    expect(hints).toEqual(['generic']);
    expect(c.flush()).toBe(md);
  });

  it('a break that would make literal tokens a mark is refused', async () => {
    // The critic parser pairs the backtick in the attribute with the one
    // after the tokens, so the tokens are text; a break between them would
    // end that code run and make them a mark.
    const md = 'x <a title="`">{++y++}` z\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, '` z'));
    press(c, 'Enter');
    expect(hints).toEqual(['generic']);
    expect(c.flush()).toBe(md);
    expect(projectCritic(md, 'clean')).toBe(md);
  });

  it('in a hazardous document, structural edits keep the whole-text check', async () => {
    // A literal opener makes the document hazardous.
    const md = 'one {++ two\n\nthree\n\nfour five\n';
    const { c, hints } = await open(md);
    expect(c.isHazardous).toBe(true);
    // A break writes {++, ++}: the closer would pair with the literal opener.
    select(c, posOf(c, 'three', 2));
    press(c, 'Enter');
    expect(hints).toEqual(['generic']);
    expect(c.flush()).toBe(md);
    // A join writes deletion tokens, which pair with nothing else.
    select(c, startOf(c, 'four'));
    press(c, 'Backspace');
    expect(hints).toEqual(['generic']);
    const out = c.flush();
    expect(out).toBe('one {++ two\n\nthree{--\n\n--}four five\n');
    expect(onlyMarksChanged(out!, projectCritic(md, 'clean'))).toBe(true);
  });
});

describe('own is never saved (boundaries)', () => {
  it('the same document saves to the same bytes with and without own boundary flags', async () => {
    const md = 'ab cd\n\nef\n\n- x\n- y\n';
    const { c } = await open(md);
    select(c, posOf(c, 'cd'));
    press(c, 'Enter');
    select(c, startOf(c, 'ef'));
    press(c, 'Backspace');
    select(c, startOf(c, 'y'));
    press(c, 'Backspace');
    const doc = c.view.state.doc;
    expect(breaks(doc)).toEqual(['ins/own', 'del/own', 'del/own']);
    const tr = new Transform(doc);
    doc.descendants((n, pos) => {
      const b = breakOf(n);
      if (b) tr.setNodeAttribute(pos, BLOCK_ATTR, { ...b, own: false });
      return true;
    });
    expect(breaks(tr.doc).every((b) => b.endsWith('/earlier'))).toBe(true);
    const serialize = (d: Node): string =>
      (c as unknown as { core: { serialize(x: Node): string } }).core.serialize(d);
    const a1 = chooseAlphabet(docStrings(doc))!;
    const a2 = chooseAlphabet(docStrings(tr.doc))!;
    const bytesWith = saveDoc(doc, serialize, doc.type.schema, a1);
    expect(saveDoc(tr.doc, serialize, doc.type.schema, a2)).toBe(bytesWith);
    expect(bytesWith).toBe('ab {++\n\n++}cd{--\n\n--}ef\n\n- x{--\n- --}y\n');
  });
});

describe('a break next to an earlier break of the same kind', () => {
  it('is refused: the two would read back as one suggestion', async () => {
    const md = 'a{++\n\n++}b\n';
    const { c, hints } = await open(md);
    select(c, startOf(c, 'b'));
    press(c, 'Enter');
    expect(hints).toEqual(['generic']);
    expect(c.flush()).toBe(md);
  });
});

describe('rendering of boundary suggestions', () => {
  it('uses only the fixed classes, elements and attributes', async () => {
    const md =
      'a{++\n\n++}bc\n\n# h{--\n\n--}c\n\n- x{++\n- ++}y\n- [ ] t{--\n- [ ] --}u\n\n> q{++\n>\n> ++}r\n\n1) n{++\n2) ++}m\n';
    const h = await open(md);
    select(h.c, posOf(h.c, 'bc', 1));
    press(h.c, 'Enter');
    expect(h.hints).toEqual([]);
    const classes = new Set<string>();
    for (const el of Array.from(h.root.querySelectorAll('*'))) {
      const tag = el.tagName.toLowerCase();
      expect(ALLOWED_TAGS.has(tag), `tag ${tag}`).toBe(true);
      for (const attr of Array.from(el.attributes)) {
        expect(attr.name.startsWith('on'), `${tag}[${attr.name}]`).toBe(false);
        expect(ALLOWED_ATTRIBUTES.has(attr.name), `${tag}[${attr.name}]`).toBe(true);
      }
      for (const cls of Array.from(el.classList)) {
        if (cls.startsWith('critic-break')) classes.add(`${tag}.${cls}`);
      }
    }
    expect([...classes].sort()).toEqual([
      'li.critic-break-del',
      'li.critic-break-ins',
      'p.critic-break-del',
      'p.critic-break-ins',
    ]);
    expect(h.root.querySelectorAll('.critic-break-ins, .critic-break-del').length).toBe(7);
  });
});

/** The editor core of a controller (tests only). */
function coreOf(c: ReviewController): { plain(md: string): string } {
  return (c as unknown as { core: { plain(md: string): string } }).core;
}

/** The structural edit kinds of the whole-text check tests. */
type Act = (h: Harness) => void;

describe('raw HTML is never edited by a structural edit', () => {
  const md = 'ab <b>x</b> cd ef\n';

  it('Enter over a selection holding inline HTML is refused; over plain text, taken', async () => {
    const h = await open(md);
    select(h.c, posOf(h.c, 'ab', 1), posOf(h.c, 'x', 1));
    press(h.c, 'Enter');
    expect(h.hints).toEqual(['generic']);
    expect(h.c.flush()).toBe(md);
    // Control: the same edit over plain text beside it.
    select(h.c, posOf(h.c, 'ef'), posOf(h.c, 'ef', 1));
    press(h.c, 'Enter');
    expect(h.hints).toEqual(['generic']);
    expect(saved(h.c, md)).toBe('ab <b>x</b> cd {--e--}{++\n\n++}f\n');
  });

  it('a two-paragraph paste over a selection holding inline HTML is refused; over plain text, taken', async () => {
    const h = await open(md);
    select(h.c, posOf(h.c, 'ab', 1), posOf(h.c, 'x', 1));
    paste(h, 'P1\n\nP2');
    expect(h.hints).toEqual(['generic']);
    expect(h.c.flush()).toBe(md);
    select(h.c, posOf(h.c, 'ef'), posOf(h.c, 'ef', 1));
    paste(h, 'P1\n\nP2');
    expect(h.hints).toEqual(['generic']);
    expect(saved(h.c, md)).toBe('ab <b>x</b> cd {--e--}{++P1\n\nP2++}f\n');
  });

  it('a split from outside the keymap over inline HTML is refused as an HTML edit', async () => {
    const h = await open(md);
    select(h.c, posOf(h.c, 'ab', 1), posOf(h.c, 'x', 1));
    splitBlock(h.c.view.state, h.c.view.dispatch);
    expect(h.hints).toEqual(['generic']);
    expect(h.c.flush()).toBe(md);
    let marked = false;
    h.c.view.state.doc.descendants((n) => {
      if (n.type.name === 'html' && n.marks.length) marked = true;
      return true;
    });
    expect(marked).toBe(false);
  });
});

describe('whole-text check for structural edits in hazardous documents (design E7)', () => {
  /** Switches off the fast first-line checks, so only the whole-text check is left. */
  function firstLineOff(c: ReviewController): void {
    const session = (c as unknown as { session: Record<string, unknown> }).session;
    session.addsCloser = (): boolean => false;
    session.changesCode = (): boolean => false;
  }
  /** Forces the hazard flag off, so the per-edit whole-text check does not run. */
  function hazardOff(c: ReviewController): void {
    (c as unknown as { session: Record<string, unknown> }).session.hazardous = false;
  }
  // Each case: the hazardous document, its variant that is not hazardous
  // (the literal opener taken out), and a structural edit whose closing
  // token would pair with the literal opener in another unit.
  const cases: [string, string, string, Act][] = [
    [
      'a break after a literal insertion opener',
      'Note: {++ stays literal.\n\n- one\n- two\n',
      'Note: stays literal.\n\n- one\n- two\n',
      (h): void => {
        select(h.c, posOf(h.c, 'two', 1));
        press(h.c, 'Enter');
      },
    ],
    [
      'a join after a literal deletion opener',
      'Note: {-- stays literal.\n\none\n\ntwo\n',
      'Note: stays literal.\n\none\n\ntwo\n',
      (h): void => {
        select(h.c, startOf(h.c, 'two'));
        press(h.c, 'Backspace');
      },
    ],
    [
      'a two-paragraph paste after a literal insertion opener',
      'Note: {++ stays literal.\n\nab\n',
      'Note: stays literal.\n\nab\n',
      (h): void => {
        select(h.c, posOf(h.c, 'ab', 1));
        paste(h, 'P1\n\nP2');
      },
    ],
  ];
  for (const [name, md, plainMd, act] of cases) {
    it(`refuses ${name} with only the whole-text check left`, async () => {
      const h = await open(md);
      expect(h.c.isHazardous).toBe(true);
      firstLineOff(h.c);
      act(h);
      expect(h.hints).toEqual(['generic']);
      expect(h.c.flush()).toBe(md);
    });

    it(`takes ${name} with no check left, and the save then refuses it`, async () => {
      // Control: without the whole-text check the edit is taken, so the
      // refusal above is that check's; save's own whole-text check stops it.
      const h = await open(md);
      firstLineOff(h.c);
      hazardOff(h.c);
      act(h);
      expect(h.hints).toEqual([]);
      expect(h.c.flush()).toBeNull();
    });

    it(`takes ${name} in the same document without the literal opener`, async () => {
      // Positive control: not hazardous, so the edit is taken and saves.
      const h = await open(plainMd);
      expect(h.c.isHazardous).toBe(false);
      act(h);
      expect(h.hints).toEqual([]);
      expect(saved(h.c, plainMd)).not.toBe(plainMd);
    });
  }
});

describe('a hard break alone in a mark loads as that mark', () => {
  for (const [md, kinds] of [
    ['ab {--\\\ncd--} ef\n', ['deletion']],
    ['ab {--\\\n--}cd\n', ['deletion']],
    ['ab {==\\\n==}cd\n', ['highlight']],
    ['ab {==\\\n==}{>>a note<<}cd\n', ['highlight', 'comment']],
    ['ab {~~\\\n~>x~~}cd\n', ['substitution']],
  ] as const) {
    it(`opens ${JSON.stringify(md)} with the mark, not literal tokens`, async () => {
      const { c } = await open(md);
      expect(docMarkRuns(c.view.state.doc).map((r) => r.kind)).toEqual([...kinds]);
      expect(c.readOnlyBlocks()).toEqual([]);
      expect(c.flush()).toBe(md);
    });
  }
});

describe('pasting one block inserts its text', () => {
  const cases: [string, string][] = [
    ['# H', 'a{++H++}b\n'],
    ['- item', 'a{++item++}b\n'],
    ['```\nline\n```', 'a{++line++}b\n'],
    ['| x | y |\n|---|---|\n| 1 | 2 |', 'a{++x y++}{++\n\n1 2++}b\n'],
  ];
  for (const [text, want] of cases) {
    it(`pastes ${JSON.stringify(text)} as text`, async () => {
      const md = 'ab\n';
      const h = await open(md);
      select(h.c, posOf(h.c, 'ab', 1));
      paste(h, text);
      expect(h.hints).toEqual([]);
      const out = saved(h.c, md);
      if (!text.startsWith('|')) {
        expect(out).toBe(want);
        // Text only: one paragraph, no heading, list or code block.
        const doc = h.c.view.state.doc;
        expect(doc.childCount).toBe(1);
        expect(doc.firstChild!.type.name).toBe('paragraph');
      } else {
        // A table is two rows: two paragraphs of plain text.
        expect(projectCritic(out, 'accept')).toBe('ax y\n\n1 2b\n');
      }
    });
  }

  it('a code line holding CriticMarkup is not pasted as text', async () => {
    const md = 'ab\n';
    const h = await open(md);
    select(h.c, posOf(h.c, 'ab', 1));
    paste(h, 'P1\n\n```\nsee {++x++} here\n```');
    expect(h.hints).toEqual(['generic']);
    paste(h, '```\n{--y--}\n```');
    expect(h.hints).toEqual(['generic', 'generic']);
    expect(h.c.flush()).toBe(md);
  });
});

describe('the read-back takes the alternative reading only for boundary runs', () => {
  it('a mark without a boundary whose text gains line-start syntax still fails', async () => {
    const { c } = await open('x {++a b++} y\n');
    const doc = c.view.state.doc;
    // The same mark, written across a line with an ordered-list marker the
    // Markdown reading keeps as text.
    expect(marksReadBack(doc, 'x {++a\n2. b++} y\n', coreOf(c).plain)).not.toBeNull();
    expect(marksReadBack(doc, 'x {++a\n10) b++} y\n', coreOf(c).plain)).not.toBeNull();
    expect(marksReadBack(doc, 'x {++a b++} y\n', coreOf(c).plain)).toBeNull();
  });

  it('a mark around a suggested break reads back with its separator', async () => {
    for (const md of ['1. a{++\n2. ++}b\n', '* a{++\n* ++}b\n', 'ab {++\\\n++}cd\n']) {
      const { c } = await open(md);
      expect(marksReadBack(c.view.state.doc, md, coreOf(c).plain), md).toBeNull();
    }
  });
});

describe('more boundary cases: quotes, line breaks, task items, composition', () => {
  it('a join between two block quotes', async () => {
    const md = '> a\n\n> b\n';
    const { c, hints } = await open(md);
    select(c, startOf(c, 'b'));
    press(c, 'Backspace');
    expect(hints).toEqual([]);
    await check(md, saved(c, md), '> a{--\n\n> --}b\n', '> ab\n');
    expect(await pilcrows(saved(c, md))).toEqual(['blockquote.del']);
  });

  it('Shift-Enter in a list item and in a quote', async () => {
    for (const md of ['- ab cd\n', '> ab cd\n']) {
      const { c, hints } = await open(md);
      select(c, posOf(c, 'cd'));
      press(c, 'Enter', { shiftKey: true });
      expect(hints, md).toEqual([]);
      const out = saved(c, md);
      expect(out, md).toContain('{++\\\n');
      expect(projectCritic(out, 'clean')).toBe(md);
      const again = await open(out);
      expect(again.c.flush()).toBe(out);
      // It opens again as an insertion, not as literal tokens.
      expect(
        docMarkRuns(again.c.view.state.doc).map((r) => r.kind),
        md
      ).toEqual(['insertion']);
    }
  });

  it('Enter at the end of a block, saved and reloaded without typing', async () => {
    const md = 'ab\n\ncd\n';
    const { c } = await open(md);
    select(c, endOf(c, 'ab'));
    press(c, 'Enter');
    await check(md, saved(c, md), 'ab{++\n\n++}\n\ncd\n', 'ab\n\n\n\ncd\n');
    expect(await pilcrows(saved(c, md))).toEqual(['p.ins']);
  });

  it('Enter in a task list item makes an unchecked item', async () => {
    for (const [md, want] of [
      ['- [ ] task one\n', '- [ ] task {++\n- [ ] ++}one\n'],
      ['- [x] done it\n', '- [x] done {++\n- [ ] ++}it\n'],
    ]) {
      const { c, hints } = await open(md);
      select(c, md.includes('task') ? posOf(c, 'one') : posOf(c, ' it', 1));
      press(c, 'Enter');
      expect(hints, md).toEqual([]);
      expect(saved(c, md)).toBe(want);
    }
  });

  it('Enter in the second paragraph of a list item makes a paragraph in the item', async () => {
    const md = '- a\n\n  b c\n';
    const { c, hints } = await open(md);
    select(c, posOf(c, 'b c', 2));
    press(c, 'Enter');
    expect(hints).toEqual([]);
    expect(breaks(c.view.state.doc)).toEqual(['ins/own']);
    const out = saved(c, md);
    expect(out).toBe('- a\n\n  b {++\n\n  ++}c\n');
    const again = await open(out);
    expect(again.c.flush()).toBe(out);
  });

  it('Enter right after a composition records the composed text, then the break', async () => {
    const md = 'ab\n';
    const { c, hints } = await open(md);
    const at = endOf(c, 'ab');
    select(c, at);
    c.beginComposition();
    const tr = c.view.state.tr.insertText('X', at);
    tr.setMeta('composition', 7);
    c.view.dispatch(tr);
    c.endComposition();
    // Enter before the settle step: it settles first.
    press(c, 'Enter');
    expect(hints).toEqual([]);
    expect(saved(c, md)).toBe('ab{++X\n\n++}\n');
  });

  it('with the cursor outside a list item’s text, Backspace and typing change nothing', async () => {
    const md = '- a\n- b\n';
    const { c, hints } = await open(md);
    // The position inside the second item, before its paragraph.
    const pos = startOf(c, 'b') - 1;
    c.view.dispatch(c.view.state.tr.setSelection(new TextSelection(c.view.state.doc.resolve(pos))));
    press(c, 'Backspace');
    expect(c.flush()).toBe(md);
    // Typing there: ProseMirror wraps the text in a new paragraph, which
    // the policy refuses with a hint; the text stays as it was.
    const v = c.view;
    const before = hints.length;
    v.dispatch(v.state.tr.insertText('Z', pos));
    expect(hints.length).toBe(before + 1);
    expect(c.flush()).toBe(md);
  });
});

describe('the policy refuses structural edits over raw HTML on its own', () => {
  // These call the policy directly, without the builders' own refusal, so
  // each of its two checks is tested by itself.
  const md = 'ab <b>x</b> cd\n';

  it('a raw split step over inline HTML is classed as an HTML edit', async () => {
    const { c } = await open(md);
    const state = c.view.state;
    const from = posOf(c, 'ab', 1);
    const to = posOf(c, 'x', 1);
    const p = state.schema.nodes.paragraph;
    const tr = state.tr.step(
      new ReplaceStep(from, to, new Slice(Fragment.from([p.create(), p.create()]), 1, 1))
    );
    expect(classify(tr, state)).toBe('html');
    // The same split over plain text is a split.
    const plain = posOf(c, 'cd', 1);
    const tr2 = state.tr.step(
      new ReplaceStep(plain, plain, new Slice(Fragment.from([p.create(), p.create()]), 1, 1))
    );
    expect(classify(tr2, state)).toBe('split');
  });

  it('a built structural edit whose range holds inline HTML is refused', async () => {
    const { c } = await open(md);
    const state = c.view.state;
    // A change that keeps the text (so the other checks pass), tagged as a
    // built structural edit over a range.
    const built = (from: number, to: number): Transaction => {
      const tr = state.tr.setNodeAttribute(0, BLOCK_ATTR, null);
      tr.setMeta(STRUCT_META, { cls: 'split', from, to });
      return tr;
    };
    const pass = (): null => null;
    expect(decide(built(posOf(c, 'ab', 1), posOf(c, 'x', 1)), state, pass)).toBe('generic');
    // Control: the same edit over plain text is allowed.
    expect(decide(built(posOf(c, 'cd'), posOf(c, 'cd', 1)), state, pass)).toBeNull();
  });
});

describe('pilcrow styles', () => {
  it('draw only on paragraphs, headings, and the first child of a list item or quote', () => {
    // A list item's own ::before is the task-list checkbox: no pilcrow rule,
    // forced-colors included, may select it.
    const css = reviewEditorStyles.cssText.replace(/\/\*[\s\S]*?\*\//g, '');
    const selectors = [...css.matchAll(/([^{}]+)\{/g)]
      // Split on top-level commas only, not those inside :is(...).
      .flatMap((m) => m[1].split(/,(?![^()]*\))/))
      .map((sel) => sel.trim())
      .filter((sel) => /critic-break-(ins|del)/.test(sel) && sel.includes('::before'));
    expect(selectors.length).toBeGreaterThan(0);
    // The parse keeps whole selectors: the forced-colors ones are among them.
    expect(selectors).toContain(':is(p, h1, h2, h3, h4, h5, h6).critic-break-ins::before');
    expect(selectors).toContain(':is(li, blockquote).critic-break-del > :first-child::before');
    for (const sel of selectors) {
      expect(
        /^(p|h[1-6]|:is\(p, h1, h2, h3, h4, h5, h6\))\.critic-break-(ins|del)::before$/.test(sel) ||
          /^:is\(li, blockquote\)\.critic-break-(ins|del) > :first-child::before$/.test(sel),
        sel
      ).toBe(true);
    }
  });
});

describe('the builders refuse raw HTML on their own', () => {
  // The builders called directly, without the policy behind them, so their
  // own check is tested by itself.
  const md = 'ab <b>x</b> cd\n\nef\n';

  it('a split over a selection holding inline HTML is refused; over plain text, built', async () => {
    const { c } = await open(md);
    const tr = new Transform(c.view.state.doc);
    expect(buildSplit(tr, posOf(c, 'ab', 1), posOf(c, 'x', 1))).toEqual({ refuse: 'generic' });
    expect(tr.steps).toHaveLength(0);
    const ok = new Transform(c.view.state.doc);
    expect('head' in buildSplit(ok, posOf(c, 'cd'), posOf(c, 'cd', 1))).toBe(true);
  });

  it('a multi-block paste over a selection holding inline HTML is refused', async () => {
    const { c } = await open(md);
    const p = c.view.state.schema;
    const pieces: Piece[] = [
      { type: 'paragraph', content: Fragment.from(p.text('P1')) },
      { type: 'paragraph', content: Fragment.from(p.text('P2')) },
    ];
    const tr = new Transform(c.view.state.doc);
    expect(buildPaste(tr, posOf(c, 'ab', 1), posOf(c, 'x', 1), pieces)).toEqual({
      refuse: 'generic',
    });
    const ok = new Transform(c.view.state.doc);
    expect('head' in buildPaste(ok, posOf(c, 'cd'), posOf(c, 'cd', 1), pieces)).toBe(true);
  });

  it('a deletion across blocks that holds inline HTML is refused; without it, built', async () => {
    const { c } = await open(md);
    const tr = new Transform(c.view.state.doc);
    expect('refuse' in buildDelete(tr, posOf(c, 'ab', 1), posOf(c, 'ef', 1), null)).toBe(true);
    expect(tr.steps).toHaveLength(0);
    const ok = new Transform(c.view.state.doc);
    expect('head' in buildDelete(ok, posOf(c, 'cd', 1), posOf(c, 'ef', 1), null)).toBe(true);
  });
});

describe('a written unit must open again with the blocks it shows', () => {
  it('a new item in a nested list with its own bullet is refused, so it never reopens as another list', async () => {
    // The outer list uses "-" and the nested one "*": the item a break adds
    // is written with the outer list's bullet, which would start a new
    // nested list on reopening. The reopen check refuses it; the same break
    // in a nested list with the outer bullet is taken.
    const md = '- a\n  * b\n';
    const { c, hints } = await open(md);
    select(c, endOf(c, 'b'));
    press(c, 'Enter');
    expect(hints).toEqual(['generic']);
    expect(c.flush()).toBe(md);
    const same = '- a\n  - b\n';
    const h2 = await open(same);
    select(h2.c, endOf(h2.c, 'b'));
    press(h2.c, 'Enter');
    expect(h2.hints).toEqual([]);
    expect(saved(h2.c, same)).toBe('- a\n  - b{++\n  - ++}\n');
  });
});
