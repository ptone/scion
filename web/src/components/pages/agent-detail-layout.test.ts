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

import { describe, it, expect, beforeAll } from 'vitest';
import { readFileSync } from 'fs';
import { join } from 'path';

const pageSource = readFileSync(join(__dirname, 'agent-detail.ts'), 'utf-8');

interface CssRule {
  selector: string;
  body: string;
  /** True when the rule sits inside an at-rule block such as `@media`. */
  nested: boolean;
}

/** Every leaf style rule from Lit cssText, including rules inside at-rules. */
function cssRules(cssText: string): CssRule[] {
  const out: CssRule[] = [];
  const stack: string[] = [];
  let buf = '';
  for (const ch of cssText.replace(/\/\*[\s\S]*?\*\//g, '')) {
    if (ch === '{') {
      stack.push(buf.trim());
      buf = '';
    } else if (ch === '}') {
      const selector = stack.pop() ?? '';
      if (!selector.startsWith('@')) {
        const nested = stack.some((s) => s.startsWith('@'));
        for (const part of selector.split(',')) {
          out.push({ selector: part.trim(), body: buf, nested });
        }
      }
      buf = '';
    } else {
      buf += ch;
    }
  }
  return out;
}

/**
 * Top-level style rules keyed by selector. Rules inside `@media` (or other
 * at-rule) blocks are skipped so a responsive override cannot overwrite the
 * base rule of the same selector.
 */
function styleRules(all: CssRule[]): Map<string, string> {
  const rules = new Map<string, string>();
  for (const r of all) if (!r.nested) rules.set(r.selector, r.body);
  return rules;
}

describe('agent detail layout', () => {
  let all: CssRule[];
  let rules: Map<string, string>;

  beforeAll(async () => {
    await import('./agent-detail.js');
    const ctor = customElements.get('scion-page-agent-detail') as unknown as {
      styles: { cssText: string };
    };
    all = cssRules(ctor.styles.cssText);
    rules = styleRules(all);
  });

  it('wraps a long agent name with its badges instead of floating them beside it', () => {
    const text = rules.get('.header-title-text') ?? '';
    expect(text).toMatch(/display:\s*flex/);
    expect(text).toMatch(/flex-wrap:\s*wrap/);
    expect(text).toMatch(/min-width:\s*0/);
    expect(rules.get('.header h1') ?? '').toMatch(/overflow-wrap:\s*anywhere/);
    expect(pageSource).toMatch(/<div class="header-title-text">\s*<h1>/);
  });

  it('keeps the header icon from shrinking beside a long name', () => {
    expect(rules.get('.header-title > sl-icon') ?? '').toMatch(/flex-shrink:\s*0/);
  });

  it('keeps the message-mode select inside its column', () => {
    expect(rules.get('.messaging-grid') ?? '').toMatch(/flex-wrap:\s*wrap/);
    expect(rules.get('.messaging-grid .messaging-mode') ?? '').toMatch(/max-width:\s*360px/);
    expect(rules.get('.messaging-mode sl-select') ?? '').toMatch(/width:\s*100%/);
    // The select used to force itself wider than its column with an inline
    // min-width; no sl-select on this page may set one. (The tag spans lines
    // and contains `=>`, so match up to the closing tag, not the first `>`.)
    expect(pageSource).not.toMatch(
      /<sl-select\b(?:(?!<\/sl-select>)[\s\S])*?style="[^"]*min-width/
    );
    // Nor may any CSS rule (top-level or inside @media) give it one.
    const selectRules = all.filter((r) => /\.messaging-mode\b.*\bsl-select\b/.test(r.selector));
    expect(selectRules.length).toBeGreaterThan(0);
    for (const r of selectRules) expect(r.body).not.toMatch(/(^|[;\s])min-width\s*:/);
  });
});
