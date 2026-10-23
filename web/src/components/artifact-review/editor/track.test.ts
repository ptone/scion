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

import { afterEach, describe, expect, it } from 'vitest';
import { undo } from '@milkdown/prose/history';
import { splitBlock, toggleMark } from '@milkdown/prose/commands';
import { TextSelection } from '@milkdown/prose/state';
import type { Transaction } from '@milkdown/prose/state';

import { onlyMarksChanged, projectCritic } from '../../../utils/critic.js';
import { ReviewController, scrollToHeading } from './controller.js';
import type { NoteRequest } from './controller.js';
import type { HintKind } from './policy.js';
import { renderContext } from './__fixtures__/core.js';

interface Harness {
  c: ReviewController;
  hints: HintKind[];
  asked: NoteRequest[];
  answer: { note: string | null };
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
  const asked: NoteRequest[] = [];
  const answer = { note: 'a note' as string | null };
  const c = await ReviewController.create({
    root,
    render: () => renderContext,
    askNote: (req) => {
      asked.push(req);
      return Promise.resolve(answer.note);
    },
    onHint: (h) => hints.push(h),
    onChange: () => {},
  });
  made.push(c);
  expect(c.load(md)).toBe(true);
  return { c, hints, asked, answer };
}

/** Document position of the n-th character of the text of the first textblock containing it. */
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

function select(c: ReviewController, from: number, to = from): void {
  const v = c.view;
  v.dispatch(v.state.tr.setSelection(TextSelection.create(v.state.doc, from, to)));
}

function type(c: ReviewController, text: string, from: number, to = from): void {
  select(c, from, to);
  const v = c.view;
  v.dispatch(v.state.tr.insertText(text, from, to));
}

function backspace(c: ReviewController, at: number): void {
  select(c, at);
  const v = c.view;
  v.dispatch(v.state.tr.delete(at - 1, at));
}

function deleteRange(c: ReviewController, from: number, to: number): void {
  select(c, from, to);
  const v = c.view;
  v.dispatch(v.state.tr.delete(from, to));
}

const text = (c: ReviewController): string => c.flush()!;

describe('track changes (inline)', () => {
  it('typing inserts', async () => {
    const { c } = await open('a big dog\n');
    type(c, 'very ', posOf(c, 'big'));
    expect(text(c)).toBe('a {++very ++}big dog\n');
  });

  it('typing over a selection substitutes', async () => {
    const { c } = await open('a big dog\n');
    const p = posOf(c, 'big');
    type(c, 'small', p, p + 3);
    expect(text(c)).toBe('a {~~big~>small~~} dog\n');
  });

  it('backspace deletes, and a second one merges', async () => {
    const { c } = await open('a big dog\n');
    const p = posOf(c, 'big');
    backspace(c, p + 3);
    backspace(c, p + 2);
    expect(text(c)).toBe('a b{--ig--} dog\n');
  });

  it('typing inside an own insertion extends it, without nesting', async () => {
    const { c } = await open('a dog\n');
    type(c, 'big ', posOf(c, 'dog'));
    type(c, 'X', posOf(c, 'big', 2));
    expect(text(c)).toBe('a {++biXg ++}dog\n');
  });

  it('backspace in an own insertion removes the character', async () => {
    const { c } = await open('a dog\n');
    type(c, 'big ', posOf(c, 'dog'));
    backspace(c, posOf(c, 'big', 2));
    expect(text(c)).toBe('a {++bg ++}dog\n');
  });

  it('typing in an own replacement new side extends it', async () => {
    const { c } = await open('a cat!\n');
    const p = posOf(c, 'cat');
    type(c, 'dog', p, p + 3);
    type(c, 'o', posOf(c, 'dog', 1));
    expect(text(c)).toBe('a {~~cat~>doog~~}!\n');
  });

  it('retyping over an own replacement keeps one substitution', async () => {
    const { c } = await open('a big dog\n');
    const p = posOf(c, 'big');
    type(c, 'small', p, p + 3);
    const q = posOf(c, 'small');
    type(c, 'tiny', q, q + 5);
    expect(text(c)).toBe('a {~~big~>tiny~~} dog\n');
  });

  it('an earlier insertion or comment is not removed by typing (refused)', async () => {
    const { c, hints } = await open('a {++big ++}dog{>>note<<}\n');
    backspace(c, posOf(c, 'big', 2));
    const p = posOf(c, 'dog', 3);
    deleteRange(c, p, p + 1);
    expect(hints).toEqual(['generic', 'generic']);
    expect(text(c)).toBe('a {++big ++}dog{>>note<<}\n');
  });

  it('typing next to an earlier insertion, deletion or highlight makes a separate insertion', async () => {
    const { c } = await open('a {++big++} {--old--} {==hl==} z\n');
    type(c, 'X', posOf(c, 'big', 3));
    type(c, 'Y', posOf(c, 'old', 3));
    type(c, 'W', posOf(c, 'hl', 2));
    expect(text(c)).toBe('a {++big++}{++X++} {--old--}{++Y++} {==hl==}{++W++} z\n');
  });

  it('one undo reverts a deletion and a replacement', async () => {
    const { c } = await open('a big dog\n');
    const p = posOf(c, 'big');
    deleteRange(c, p, p + 3);
    expect(text(c)).toBe('a {--big--} dog\n');
    expect(undo(c.view.state, c.view.dispatch)).toBe(true);
    expect(text(c)).toBe('a big dog\n');
    type(c, 'red', posOf(c, 'dog'), posOf(c, 'dog') + 3);
    expect(text(c)).toBe('a big {~~dog~>red~~}\n');
    expect(undo(c.view.state, c.view.dispatch)).toBe(true);
    expect(text(c)).toBe('a big dog\n');
  });

  it('deleting bold text keeps the formatting inside the deletion', async () => {
    const { c } = await open('a **big** dog\n');
    const p = posOf(c, 'big');
    deleteRange(c, p, p + 3);
    expect(text(c)).toBe('a {--**big**--} dog\n');
  });

  it('an edit inside inline code replaces the whole span', async () => {
    const { c } = await open('run `make test` now\n');
    type(c, 'X', posOf(c, 'make', 2));
    expect(text(c)).toBe('run {~~`make test`~>`maXke test`~~} now\n');
  });

  it('one undo reverts one tracked edit', async () => {
    const { c } = await open('a big dog\n');
    type(c, 'very ', posOf(c, 'big'));
    expect(undo(c.view.state, c.view.dispatch)).toBe(true);
    expect(text(c)).toBe('a big dog\n');
  });

  it('keeps every other block byte for byte', async () => {
    const md = '# Title\n\n* one\n* two\n\n| a | b |\n|-|-|\n| 1 | 2 |\n\nedit me\n';
    const { c } = await open(md);
    type(c, 'X', posOf(c, 'edit'));
    const out = text(c);
    expect(out).toBe('# Title\n\n* one\n* two\n\n| a | b |\n|-|-|\n| 1 | 2 |\n\n{++X++}edit me\n');
    expect(onlyMarksChanged(out, projectCritic(md, 'clean'))).toBe(true);
  });
});

describe('refused edits in suggest mode', () => {
  it('Enter (a paragraph break) changes nothing and gives the break hint', async () => {
    const { c, hints } = await open('one two\n');
    select(c, posOf(c, 'two'));
    splitBlock(c.view.state, c.view.dispatch);
    expect(text(c)).toBe('one two\n');
    expect(hints).toEqual(['break']);
  });

  it('formatting changes nothing and gives the formatting hint', async () => {
    const { c, hints } = await open('one two\n');
    const p = posOf(c, 'two');
    select(c, p, p + 3);
    toggleMark(c.view.state.schema.marks.strong)(c.view.state, c.view.dispatch);
    expect(text(c)).toBe('one two\n');
    expect(hints).toEqual(['format']);
  });

  it('typing in a list item or a table is refused in this version', async () => {
    const { c, hints } = await open('- item\n\n| a |\n|---|\n| cell |\n');
    type(c, 'X', posOf(c, 'item'));
    type(c, 'X', posOf(c, 'cell'));
    expect(text(c)).toBe('- item\n\n| a |\n|---|\n| cell |\n');
    expect(hints).toEqual(['list-table', 'list-table']);
  });

  it('typing in a code block is refused', async () => {
    const { c, hints } = await open('```\ncode here\n```\n');
    type(c, 'X', posOf(c, 'code'));
    expect(text(c)).toBe('```\ncode here\n```\n');
    expect(hints).toEqual(['code']);
  });

  it('an edit whose save would change text outside marks is refused', async () => {
    // The serializer writes this paragraph without its backslash, which
    // would change the text outside marks, so the edit is refused.
    const md = 'a \\# b c\n';
    const { c, hints } = await open(md);
    type(c, 'X', posOf(c, 'b c'));
    expect(hints).toEqual(['generic']);
    expect(text(c)).toBe(md);
    // Positive control: the same edit in a paragraph the serializer keeps.
    const ok = await open('a b c\n');
    type(ok.c, 'X', posOf(ok.c, 'b c'));
    expect(ok.hints).toEqual([]);
    expect(text(ok.c)).toBe('a {++X++}b c\n');
  });
});

describe('comments', () => {
  it('wraps an inline selection as a highlight with a note', async () => {
    const { c, asked } = await open('one two three\n');
    const p = posOf(c, 'two');
    select(c, p, p + 3);
    await c.comment();
    expect(asked[0].quote).toBe('two');
    expect(text(c)).toBe('one {==two==}{>>a note<<} three\n');
  });

  it('puts a note at the cursor without a selection', async () => {
    const { c } = await open('one two\n');
    select(c, posOf(c, 'two'));
    await c.comment();
    expect(text(c)).toBe('one {>>a note<<}two\n');
  });

  it('a cancelled dialog adds nothing', async () => {
    const { c, answer } = await open('one two\n');
    answer.note = null;
    select(c, posOf(c, 'two'));
    await c.comment();
    expect(text(c)).toBe('one two\n');
  });

  it('edits an own note, and an empty note deletes the comment and its highlight', async () => {
    const { c, answer, asked } = await open('one two three\n');
    const p = posOf(c, 'two');
    select(c, p, p + 3);
    answer.note = 'old';
    await c.comment();
    expect(text(c)).toBe('one {==two==}{>>old<<} three\n');
    let pos = -1;
    c.view.state.doc.descendants((n, q) => {
      if (n.type.name === 'critic_comment') pos = q;
      return true;
    });
    answer.note = 'new';
    await c.editNote(pos);
    expect(asked[1]).toMatchObject({ note: 'old', number: 1 });
    expect(text(c)).toBe('one {==two==}{>>new<<} three\n');
    answer.note = '';
    await c.editNote(pos);
    expect(text(c)).toBe('one two three\n');
  });

  it('does not edit a comment already in the text', async () => {
    const { c, hints, asked } = await open('one {==two==}{>>old<<} three\n');
    let pos = -1;
    c.view.state.doc.descendants((n, q) => {
      if (n.type.name === 'critic_comment') pos = q;
      return true;
    });
    await c.editNote(pos);
    expect(asked).toEqual([]);
    expect(hints).toEqual(['generic']);
    expect(text(c)).toBe('one {==two==}{>>old<<} three\n');
  });

  it('refuses a comment whose save would change text outside marks', async () => {
    const { c, hints } = await open('a \\# b c\n');
    select(c, posOf(c, 'b c'));
    await c.comment();
    expect(hints).toEqual(['generic']);
    expect(text(c)).toBe('a \\# b c\n');
    // Positive control: the same comment in a paragraph the serializer keeps.
    const ok = await open('a b c\n');
    select(ok.c, posOf(ok.c, 'b c'));
    await ok.c.comment();
    expect(ok.hints).toEqual([]);
    expect(text(ok.c)).toBe('a {>>a note<<}b c\n');
  });

  it('refuses a note that would end the comment early', async () => {
    const { c, hints, answer } = await open('one two\n');
    answer.note = 'x <<} y';
    select(c, posOf(c, 'two'));
    await c.comment();
    expect(hints).toEqual(['generic']);
    expect(text(c)).toBe('one two\n');
  });

  it('refuses a comment inside a list in this version', async () => {
    const { c, hints } = await open('- item\n');
    select(c, posOf(c, 'item'));
    await c.comment();
    expect(hints).toEqual(['list-table']);
    expect(text(c)).toBe('- item\n');
  });
});

describe('composition (IME) input', () => {
  /**
   * What ProseMirror does while composing (untracked transactions tagged
   * with the composition id), then the controller's end-of-composition
   * step. time sets the composition transaction's time.
   */
  function compose(
    c: ReviewController,
    at: number,
    edit: (tr: Transaction) => Transaction,
    time?: number
  ): void {
    select(c, at);
    c.beginComposition();
    const v = c.view;
    const tr = edit(v.state.tr);
    tr.setMeta('composition', 7);
    if (time !== undefined) tr.setTime(time);
    v.dispatch(tr);
    c.endComposition();
  }

  it('records a composed word as an insertion', async () => {
    const { c, hints } = await open('a dog\n');
    const p = posOf(c, 'dog');
    compose(c, p, (tr) => tr.insertText('大きい', p));
    expect(hints).toEqual([]);
    expect(text(c)).toBe('a {++大きい++}dog\n');
  });

  it('one undo after a long pause returns the document from before the composition', async () => {
    const { c } = await open('a dog\n');
    const before = c.view.state.doc;
    const p = posOf(c, 'dog');
    compose(c, p, (tr) => tr.insertText('犬', p), Date.now() - 2000);
    expect(text(c)).toBe('a {++犬++}dog\n');
    expect(undo(c.view.state, c.view.dispatch)).toBe(true);
    expect(c.view.state.doc.eq(before)).toBe(true);
  });

  it('undoes a composition that removed text, with the generic hint', async () => {
    const { c, hints } = await open('a dog\n');
    const before = c.view.state.doc;
    const p = posOf(c, 'dog');
    compose(c, p, (tr) => tr.insertText('犬', p, p + 3), Date.now() - 2000);
    expect(hints).toEqual(['generic']);
    expect(text(c)).toBe('a dog\n');
    undo(c.view.state, c.view.dispatch);
    expect(c.view.state.doc.eq(before)).toBe(true);
  });

  it('undoes a composition in a list item or a code block, with their hints', async () => {
    const { c, hints } = await open('- item\n\n```\ncode\n```\n');
    const before = c.view.state.doc;
    const li = posOf(c, 'item');
    compose(c, li, (tr) => tr.insertText('X', li), Date.now() - 2000);
    const code = posOf(c, 'code');
    compose(c, code, (tr) => tr.insertText('Y', code));
    expect(hints).toEqual(['list-table', 'code']);
    expect(text(c)).toBe('- item\n\n```\ncode\n```\n');
    while (undo(c.view.state, c.view.dispatch)) {
      // back to the start
    }
    expect(c.view.state.doc.eq(before)).toBe(true);
  });

  it('undoes a composition that landed outside the block it started in', async () => {
    const { c, hints } = await open('one\n\ntwo\n');
    select(c, posOf(c, 'one'));
    c.beginComposition();
    const q = posOf(c, 'two');
    const tr = c.view.state.tr.insertText('Z', q);
    tr.setMeta('composition', 9);
    c.view.dispatch(tr);
    c.endComposition();
    expect(hints).toEqual(['generic']);
    expect(text(c)).toBe('one\n\ntwo\n');
  });
});

describe('fragment links', () => {
  it('Mod-click on a #fragment link moves to the heading', async () => {
    const { c } = await open('[go](#second-part)\n\n# First\n\n## Second part\n');
    expect(scrollToHeading(c.view, 'second-part')).toBe(true);
    expect(c.view.state.selection.$from.parent.textContent).toBe('Second part');
    expect(scrollToHeading(c.view, 'nowhere')).toBe(false);
  });
});
