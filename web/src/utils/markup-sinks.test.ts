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
 * Lists every place in the app's source (web/src, tests and fixtures left
 * out) that hands a string to an API that parses markup, and fails when one
 * is added or removed without updating KNOWN below (ptone/scion#6367).
 *
 * The browser reports each of these calls that passes a plain string.
 * Keeping the list here means a new call shows up in review, not only in
 * browser reports.
 */

import { describe, expect, it } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';

const SRC = resolve(__dirname, '..');

/** The markup-parsing calls looked for, by name. */
const PATTERNS: Record<string, RegExp> = {
  'innerHTML/outerHTML assignment': /\.(?:inner|outer)HTML\s*=(?!=|\$\{)/g,
  'innerHTML property binding': /\.innerHTML=\$\{/g,
  insertAdjacentHTML: /\binsertAdjacentHTML\s*\(/g,
  'DOMParser.parseFromString': /\bparseFromString\s*\(/g,
  'document.write': /\b(?:document|doc)\.write(?:ln)?\s*\(/g,
  createContextualFragment: /\bcreateContextualFragment\s*\(/g,
  'setHTMLUnsafe/parseHTMLUnsafe': /\b(?:setHTMLUnsafe|parseHTMLUnsafe)\s*\(/g,
  // `this.srcdoc = ...` on a component is its own state, not the frame's.
  'srcdoc binding or assignment':
    /(?:\.srcdoc=\$\{|(?<!this)\.srcdoc\s*=(?!=|\$\{)|setAttribute\(\s*['"]srcdoc['"])/g,
  'Lit unsafeHTML/unsafeSVG': /\bunsafe(?:HTML|SVG)\s*\(/g,
  // An icon's src attribute makes Shoelace fetch and parse the file; app
  // icons come from the sprite sheet by name instead.
  'sl-icon src attribute': /<sl-icon(?:-button)?\b[^>]*\ssrc=/g,
  'sl-icon src property': /\b(?:icon|iconEl)\.src\s*=/g,
};

/**
 * The calls that are known and accepted, as "file: pattern" -> count.
 * Each entry says why it is here.
 */
const KNOWN: Record<string, number> = {
  // The artifact preview's image rewrite; moves to nodes with ptone/scion#6674.
  'client/artifact-preview.ts: innerHTML/outerHTML assignment': 1,
  // The artifact preview frame's document: set from previewFrameDocument,
  // which builds it from sanitized markup and marks it as accepted markup.
  'components/shared/artifact-markdown-frame.ts: srcdoc binding or assignment': 1,
  // Search result snippets: plain text with only <mark> highlights, rendered by Lit.
  'components/shared/chat/chat-search.ts: Lit unsafeHTML/unsafeSVG': 1,
};

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      return name === '__fixtures__' || name === 'generated' ? [] : sourceFiles(path);
    }
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) && !name.endsWith('.d.ts')
      ? [path]
      : [];
  });
}

function findings(): Record<string, number> {
  const found: Record<string, number> = {};
  for (const path of sourceFiles(SRC)) {
    const text = readFileSync(path, 'utf8');
    const file = relative(SRC, path).split('\\').join('/');
    for (const [name, pattern] of Object.entries(PATTERNS)) {
      const count = [...text.matchAll(pattern)].length;
      if (count > 0) found[`${file}: ${name}`] = count;
    }
  }
  return found;
}

describe('markup-parsing calls in the app source', () => {
  it('scans the source tree', () => {
    expect(sourceFiles(SRC).length).toBeGreaterThan(100);
  });

  it('match the known list exactly', () => {
    expect(findings()).toEqual(KNOWN);
  });

  it('each pattern finds what it is meant to find', () => {
    const samples: Record<string, string> = {
      'innerHTML/outerHTML assignment': 'el.innerHTML = s; el.outerHTML=s;',
      'innerHTML property binding': 'html`<div .innerHTML=${s}></div>`',
      insertAdjacentHTML: "el.insertAdjacentHTML('beforeend', s)",
      'DOMParser.parseFromString': "new DOMParser().parseFromString(s, 'text/html')",
      'document.write': 'document.write(s); doc.writeln(s);',
      createContextualFragment: 'range.createContextualFragment(s)',
      'setHTMLUnsafe/parseHTMLUnsafe': 'el.setHTMLUnsafe(s); Document.parseHTMLUnsafe(s)',
      'srcdoc binding or assignment':
        "html`<iframe .srcdoc=${s}>`; f.srcdoc = s; f.setAttribute('srcdoc', s)",
      'Lit unsafeHTML/unsafeSVG': 'unsafeHTML(s); unsafeSVG(s)',
      'sl-icon src attribute': '<sl-icon src="/a.svg"></sl-icon><sl-icon-button src=${u}>',
      'sl-icon src property': 'icon.src = u;',
    };
    for (const [name, pattern] of Object.entries(PATTERNS)) {
      expect([...samples[name]!.matchAll(pattern)].length, name).toBeGreaterThan(0);
    }
    // Comparisons and reads are not calls.
    expect([
      ...'a.innerHTML === b; x = el.innerHTML;'.matchAll(
        PATTERNS['innerHTML/outerHTML assignment']!
      ),
    ]).toEqual([]);
    expect([
      ...'html`<p .innerHTML=${s}>`'.matchAll(PATTERNS['innerHTML/outerHTML assignment']!),
    ]).toEqual([]);
    expect([...'this.srcdoc = doc;'.matchAll(PATTERNS['srcdoc binding or assignment']!)]).toEqual(
      []
    );
    expect([
      ...'<sl-icon name="x"></sl-icon>'.matchAll(PATTERNS['sl-icon src attribute']!),
    ]).toEqual([]);
  });
});
