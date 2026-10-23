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
import { loadDoc } from './bridge.js';
import type { Core } from './create.js';
import { docMarkKinds, textMarkKinds } from './guard.js';
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
      const doc = loadDoc(c.in, core.parse, core.schema, a);
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
});
