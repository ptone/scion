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

import { describe, expect, it } from 'vitest';

import {
  SENTINEL_CANDIDATES,
  chooseAlphabet,
  criticToSentinelText,
  sentinelSet,
  sentinelTextToCritic,
} from './critic-sentinels.js';

describe('sentinel alphabet', () => {
  it('draws only from Unicode Po punctuation, without repeats', () => {
    for (const c of SENTINEL_CANDIDATES) expect(/^\p{Po}$/u.test(c), c).toBe(true);
    expect(new Set(SENTINEL_CANDIDATES).size).toBe(SENTINEL_CANDIDATES.length);
  });

  it('skips every candidate the document contains', () => {
    const used = SENTINEL_CANDIDATES.slice(0, 12).join('');
    const a = chooseAlphabet(`text ${used} more`)!;
    for (const c of sentinelSet(a)) expect(used.includes(c)).toBe(false);
    expect(sentinelSet(a).size).toBe(10);
  });

  it('gives no alphabet when fewer than ten candidates are free', () => {
    expect(chooseAlphabet(SENTINEL_CANDIDATES.slice(4).join(''))).toBeNull();
    // Positive control: one more free candidate is enough.
    expect(chooseAlphabet(SENTINEL_CANDIDATES.slice(10).join(''))).not.toBeNull();
  });
});

describe('sentinel text', () => {
  const md = 'a {++in++} {--out--} {~~old~>new~~} {==hl==}{>>a *note*<<} `{++code++}`';

  it('round-trips, keeping notes aside and code literal', () => {
    const a = chooseAlphabet(md)!;
    const s = criticToSentinelText(md, a);
    expect(s.notes).toEqual(['a *note*']);
    expect(s.text).not.toContain('{++in');
    expect(s.text).toContain('`{++code++}`');
    expect(s.text.includes('*note*')).toBe(false);
    expect(sentinelTextToCritic(s.text, s.notes, a)).toBe(md);
  });

  it('maps every sentinel-text character to the source span it stands for', () => {
    const a = chooseAlphabet(md)!;
    const s = criticToSentinelText(md, a);
    expect(s.starts.length).toBe(s.text.length);
    for (let i = 0; i < s.text.length; i++) {
      const c = s.text[i];
      const span = md.slice(s.starts[i], s.ends[i]);
      if (!sentinelSet(a).has(c)) expect(span).toBe(c);
    }
    const note = s.text.indexOf(a.note);
    expect(md.slice(s.starts[note], s.ends[note])).toBe('{>>a *note*<<}');
  });
});
