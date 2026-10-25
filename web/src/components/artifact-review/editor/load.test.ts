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
 * Opening a document reads each thing once and shares it between the load
 * checks. These tests pin that every shared reading gives each check the
 * same result as reading it on its own.
 */

import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import { chooseOpeningAlphabet, probeOpeningAlphabet } from '../../../utils/critic-sentinels.js';
import { parseCritic } from '../../../utils/critic.js';
import { loadDocument } from './bridge.js';
import type { Core } from './create.js';
import { TextMarks } from './guard.js';
import type { MarkRun } from './guard.js';
import { docStrings, makeBaseline } from './preserve.js';
import type { Baseline } from './preserve.js';
import { ReviewSession, codeNeighbours } from './session.js';
import { corpus, destroyCores, makeCore, sweepDocs } from './__fixtures__/core.js';

let core: Core;
beforeAll(async () => {
  core = await makeCore();
});
afterAll(destroyCores);

const docs = [...corpus.map((c) => c.in), ...sweepDocs.map((d) => d.in)];

/** The baseline md opens with, or null. */
function baselineOf(md: string): Baseline | null {
  const a = chooseOpeningAlphabet(
    md,
    (text) => `${docStrings(core.parse(md))}\n${docStrings(core.parse(text))}`
  );
  if (!a) return null;
  const loaded = loadDocument(md, core.parse, core.schema, a);
  return loaded ? makeBaseline(md, loaded.doc, core.parseMd, a) : null;
}

describe('marks of a range, from one reading of the whole text', () => {
  /** The marks of [from, to): every segment of a fresh whole-text parse that starts there. */
  const fresh = (md: string, from: number, to: number, skip: Set<number>): MarkRun[] =>
    parseCritic(md)
      .filter((s) => s.kind !== 'text' && s.start >= from && s.start < to && !skip.has(s.start))
      .map(
        (s): MarkRun =>
          s.kind === 'comment'
            ? { kind: 'comment', text: s.text }
            : s.kind === 'substitution'
              ? { kind: 'substitution', text: core.plain(s.text), next: core.plain(s.next ?? '') }
              : { kind: s.kind as MarkRun['kind'], text: core.plain(s.text) }
      );

  it('gives every range the marks a fresh read of the whole text gives it', () => {
    let ranges = 0;
    for (const md of docs) {
      const marks = new TextMarks(md);
      const cuts = [0, ...parseCritic(md).flatMap((s) => [s.start, s.end]), md.length];
      const skip = new Set(
        parseCritic(md)
          .filter((_, i) => i % 3 === 0)
          .map((s) => s.start)
      );
      for (let i = 0; i < cuts.length; i++) {
        for (let j = i; j < Math.min(cuts.length, i + 4); j++) {
          for (const sk of [new Set<number>(), skip]) {
            expect(marks.runs(core.plain, cuts[i], cuts[j], sk), md).toEqual(
              fresh(md, cuts[i], cuts[j], sk)
            );
            ranges++;
          }
        }
      }
      expect(marks.runs(core.plain), md).toEqual(fresh(md, 0, Infinity, new Set()));
    }
    expect(ranges).toBeGreaterThan(1000);
  });
});

describe('code neighbours', () => {
  /** Per block: another block of its line run, or the text between blocks of the run, holds a backtick. */
  const reference = (base: Baseline): boolean[] => {
    const n = base.blocks.length;
    const run: number[] = [];
    let r = 0;
    for (let k = 0; k < n; k++) {
      run.push(r);
      if (/\n[ \t]*\r?\n/.test(base.blocks[k].after.replace(/\r\n/g, '\n'))) r++;
    }
    const gap = new Set<number>();
    if (n > 0 && base.lead.includes('`')) gap.add(run[0]);
    for (let k = 0; k < n; k++) {
      if (!base.blocks[k].after.includes('`')) continue;
      gap.add(run[k]);
      if (k + 1 < n) gap.add(run[k + 1]);
    }
    return base.blocks.map(
      (_, k) =>
        gap.has(run[k]) ||
        base.blocks.some((b, m) => m !== k && run[m] === run[k] && b.src.includes('`'))
    );
  };

  it('agrees with a block-by-block comparison', () => {
    const extra = [
      'a `x`\nb\n\nc `y`\n',
      '`a`\nb `c`\nd\n',
      '- a `x`\n- b\n\n`c`\n',
      'a\n[r]: /u "`"\nb\n\nc\n',
    ];
    let n = 0;
    for (const md of [...docs, ...extra]) {
      const base = baselineOf(md);
      if (!base) continue;
      expect(codeNeighbours(base), md).toEqual(reference(base));
      n++;
    }
    expect(n).toBeGreaterThan(50);
  });
});

describe('the text of marks', () => {
  it('reads each distinct mark text once per load, and again at the next load', () => {
    const md = 'a {++x++} b {++x++} c {--y--} d {++x++}\n\ne {~~x~>y~~}\n';
    const seen: string[] = [];
    const counting: Core = {
      ...core,
      plain: (t: string): string => {
        seen.push(t);
        return core.plain(t);
      },
    };
    const s = new ReviewSession(counting);
    expect(s.load(md)).toBe(true);
    expect([...seen].sort()).toEqual(['x', 'y']);
    seen.length = 0;
    expect(s.load(md)).toBe(true);
    expect([...seen].sort()).toEqual(['x', 'y']);
  });
});

describe('opening alphabet without the probe', () => {
  const shown =
    (md: string) =>
    (text: string): string =>
      `${docStrings(core.parse(md))}\n${docStrings(core.parse(text))}`;

  it('is the alphabet the probe chooses with the real parser', () => {
    const named = [
      'R&amp;D {++and++} &copy; &nbsp; &lt;b&gt; {>>&hellip;<<}\n',
      '[a](u "&quot;{++x++}&quot;") &emsp;&ensp; `&amp;`\n',
      '<span title="&mdash;">{==x==}</span>{>>&rarr;<<}\n',
    ];
    let n = 0;
    for (const md of [...docs, ...named]) {
      if (md.includes('&#')) continue;
      expect(
        chooseOpeningAlphabet(md, () => ''),
        md
      ).toEqual(probeOpeningAlphabet(md, shown(md)));
      n++;
    }
    expect(n).toBeGreaterThan(100);
  });
});
