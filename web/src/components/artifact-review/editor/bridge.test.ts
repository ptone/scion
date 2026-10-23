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

import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import { chooseAlphabet } from '../../../utils/critic-sentinels.js';
import type { Node } from '@milkdown/prose/model';

import { loadDoc, loadDocument, saveDoc, sentinelLeft } from './bridge.js';
import type { Consumed } from './bridge.js';
import { consumedAsInSource } from './preserve.js';
import { MARKS } from './schema.js';
import type { Core } from './create.js';
import { docMarkKinds, earlierKept, markKey, marksReadBack, textMarkKinds } from './guard.js';
import { parseCritic, projectCritic } from '../../../utils/critic.js';
import { ReviewSession } from './session.js';
import { corpus, destroyCores, makeCore } from './__fixtures__/core.js';

let core: Core;
beforeAll(async () => {
  core = await makeCore();
});
afterAll(destroyCores);

describe('corpus round trip', () => {
  it('saves every corpus document unedited as the same bytes', () => {
    const s = new ReviewSession(core);
    for (const c of corpus) {
      expect(s.load(c.in), c.name).toBe(true);
      expect(s.save(), c.name).toBe(c.in);
    }
  });

  it('keeps every block byte for byte when a forced save edits nothing', () => {
    const bad: string[] = [];
    for (const c of corpus) {
      const s = new ReviewSession(core);
      s.load(c.in);
      s.markDirty();
      if (s.save() !== c.in)
        bad.push(`${c.name}: ${JSON.stringify(c.in)} -> ${JSON.stringify(s.save())}`);
    }
    expect(bad).toEqual([]);
  });

  // Marks the editor keeps as literal text: empty marks (no text to carry
  // a mark), marks around a whole fenced block, and the cases where
  // CommonMark sees code that the critic parser does not.
  const LITERAL = [
    'empty insertion',
    'empty deletion',
    'substitution both empty',
    'four plus empty insertion',
    'fence inside a mark',
    'tilde fence in a block quote stays marks (container blocks are not recognised)',
    'tilde fence nested in a list item four spaces deep stays marks',
    'indented code block stays marks',
  ];

  it('reads the same marks as parseCritic, except the pinned literal cases', () => {
    const bad: string[] = [];
    for (const c of corpus) {
      const a = chooseAlphabet(c.in)!;
      const doc = loadDoc(c.in, core.parse, core.schema, a)!;
      const d = docMarkKinds(doc).join(',');
      const t = textMarkKinds(c.in).join(',');
      if (d !== t) bad.push(c.name);
    }
    expect(bad).toEqual(LITERAL);
  });

  it('keeps formatting inside a substitution new side (room for formatting suggestions)', () => {
    const md = 'make {~~a~>**a**~~} bold\n';
    const s = new ReviewSession(core);
    expect(s.load(md)).toBe(true);
    expect(s.save()).toBe(md);
    s.markDirty();
    expect(s.save()).toBe(md);
    const dom = core.view.dom;
    expect(dom.querySelector('del.critic-sub-old strong')).toBeNull();
    expect(
      dom.querySelector('ins.critic-sub-new strong, strong ins.critic-sub-new')
    ).not.toBeNull();
  });

  it('reads the same mark texts back after saving, except the pinned literal cases', () => {
    const bad: string[] = [];
    for (const c of corpus) {
      const a = chooseAlphabet(c.in)!;
      const doc = loadDoc(c.in, core.parse, core.schema, a)!;
      if (marksReadBack(doc, c.in, core.plain) !== null) bad.push(c.name);
    }
    expect(bad).toEqual(LITERAL);
  });

  // A forced full re-serialization (no source passthrough) keeps the clean
  // and accept projections of every other corpus document, compared after
  // both sides go through the same serializer normalization. Where it does
  // not: an empty substitution (its tokens stay literal and get
  // backslashes), and the cases where CommonMark sees code that the
  // critic parser does not (the serializer rewrites the code block).
  // Phase 1 refuses edits in such blocks; phase 2 patches the source.
  const RESERIALIZE_DIFFERS = [
    'substitution both empty',
    'tilde fence in a block quote stays marks (container blocks are not recognised)',
    'tilde fence nested in a list item four spaces deep stays marks',
    'indented code block stays marks',
    'a heading does not end a run of lines',
  ];

  it('keeps the projections after a forced full re-serialization, except pinned cases', () => {
    const norm = (t: string): string => core.serialize(core.parse(t)).replace(/\s+$/, '');
    const bad: string[] = [];
    for (const c of corpus) {
      const a = chooseAlphabet(c.in)!;
      const out = saveDoc(
        loadDoc(c.in, core.parse, core.schema, a)!,
        core.serialize,
        core.schema,
        a
      );
      const same = (['clean', 'accept'] as const).every(
        (m) => norm(projectCritic(out, m)) === norm(projectCritic(c.in, m))
      );
      if (!same) bad.push(c.name);
    }
    expect(bad).toEqual(RESERIALIZE_DIFFERS);
  });
});

describe('load-time safety net', () => {
  it('finds an alphabet character left in text, a node attribute or a mark attribute', () => {
    const a = chooseAlphabet('x')!;
    const { schema } = core.view.state;
    const doc = (
      inline: ReturnType<typeof schema.text>[]
    ): ReturnType<typeof schema.topNodeType.create> =>
      schema.topNodeType.create(null, [schema.nodes.paragraph.create(null, inline)]);
    expect(sentinelLeft(doc([schema.text('plain')]), a)).toBe(false);
    expect(sentinelLeft(doc([schema.text(`a${a.note}b`)]), a)).toBe(true);
    const link = schema.marks.link.create({ href: `https://x.example/${a.insOpen}` });
    expect(sentinelLeft(doc([schema.text('l', [link])]), a)).toBe(true);
    const img = schema.nodes.image.create({ src: 'a.png', alt: `x${a.hlClose}` });
    expect(
      sentinelLeft(schema.topNodeType.create(null, [schema.nodes.paragraph.create(null, [img])]), a)
    ).toBe(true);
  });
});

// The two invariants are tested against deliberately broken parts (a
// parser that moves text, a serializer that rewrites an earlier mark), not
// only against the constructs known today.
describe('load invariant (I1)', () => {
  const md = 'one {==a==}{>>A<<}\n\ntwo {==b==}{>>B<<}\n';

  it('opens the document with the real parser', () => {
    expect(new ReviewSession(core).load(md)).toBe(true);
  });

  it('refuses when a broken parser moves blocks (marks shown on other text)', () => {
    const swap = (t: string): Node => {
      const parts = t.split('\n\n');
      [parts[0], parts[1]] = [parts[1], parts[0]];
      return core.parse(parts.join('\n\n'));
    };
    expect(new ReviewSession({ ...core, parse: swap }).load(md)).toBe(false);
  });

  it('refuses when a broken parser moves a mark token into another block', () => {
    // Moves the last character before the first break (a comment sentinel)
    // to the end of the text.
    const move = (t: string): Node => {
      const i = t.indexOf('\n\n');
      if (i < 1) return core.parse(t);
      return core.parse(t.slice(0, i - 1) + t.slice(i) + t[i - 1]);
    };
    expect(new ReviewSession({ ...core, parse: move }).load(md)).toBe(false);
  });

  it('refuses when what the pass consumed differs from the source in any way', () => {
    const a = chooseAlphabet(md)!;
    const loaded = loadDocument(md, core.parse, core.schema, a)!;
    const ok = (c: Consumed[]): boolean =>
      consumedAsInSource(md, a, core.parseMd, loaded.doc.childCount, c);
    expect(ok(loaded.consumed)).toBe(true);
    const notes = loaded.consumed.map((x) => ({ ...x }));
    const withNotes = notes.filter((x) => x.note !== undefined);
    const first = withNotes[0].note!;
    withNotes[0].note = withNotes[1].note!;
    withNotes[1].note = first;
    expect(ok(notes)).toBe(false);
    expect(ok(loaded.consumed.map((x, i) => (i === 0 ? { ...x, block: 1 } : x)))).toBe(false);
    expect(ok(loaded.consumed.slice(1))).toBe(false);
    expect(ok([...loaded.consumed, loaded.consumed[0]])).toBe(false);
  });
});

describe('save invariant (I2)', () => {
  it('earlierKept: earlier marks unchanged and in order, plus exactly the own ones', () => {
    const before = parseCritic('a {++x++} b {>>n<<}')
      .filter((x) => x.kind !== 'text')
      .map(markKey);
    expect(earlierKept(before, 'a {++x++} b {>>n<<}', 0)).toBe(true);
    expect(earlierKept(before, 'a {++x++} {--b--} {>>n<<}{>>mine<<}', 2)).toBe(true);
    expect(earlierKept(before, 'a {++y++} b {>>n<<}', 0)).toBe(false);
    expect(earlierKept(before, 'a {++x++} b {>>m<<}', 0)).toBe(false);
    expect(earlierKept(before, 'a b {>>n<<}{++x++}', 0)).toBe(false);
    expect(earlierKept(before, 'a {++x++} b {>>n<<}{++z++}', 0)).toBe(false);
    expect(earlierKept(before, 'a {++x++} b', 1)).toBe(false);
  });

  // An edit beside an earlier insertion whose text a broken serializer
  // rewrites in a way the plain reading does not see.
  const md = 'a {++*x*++} b\n';
  const edit = (s: ReviewSession, c: Core): void => {
    const v = c.view;
    const end = v.state.doc.child(0).nodeSize - 1;
    const mark = c.schema.marks[MARKS.ins].create({ mine: true });
    v.dispatch(v.state.tr.insert(end, c.schema.text(' Z', [mark])));
    s.markDirty();
  };

  it('saves the edit with the real serializer', () => {
    const s = new ReviewSession(core);
    expect(s.load(md)).toBe(true);
    edit(s, core);
    expect(s.unitOk(core.view.state.doc, 0, 0)).toBe(true);
    expect(s.save()).toBe('a {++*x*++} b{++ Z++}\n');
  });

  it('refuses the edit and the save when the serializer rewrites the earlier mark', () => {
    const broken = {
      ...core,
      serialize: (d: Node): string => core.serialize(d).replace(/\*/g, '_'),
    };
    const s = new ReviewSession(broken);
    expect(s.load(md)).toBe(true);
    edit(s, broken);
    expect(s.unitOk(core.view.state.doc, 0, 0)).toBe(false);
    expect(s.save()).toBeNull();
  });
});
