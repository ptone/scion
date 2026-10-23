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
 * The ESLint block for the review editor's folder (eslint.config.mjs): it
 * applies to the editor's sources, and it catches each way of turning a
 * string into HTML, beside forms that stay allowed.
 */

import { resolve } from 'node:path';
import { ESLint, Linter } from 'eslint';
import { describe, expect, it } from 'vitest';

const webRoot = resolve(__dirname, '../../..');
const eslint = new ESLint({ cwd: webRoot });

async function syntaxRule(file: string): Promise<Linter.RuleEntry | undefined> {
  const config = (await eslint.calculateConfigForFile(resolve(webRoot, file))) as {
    rules?: Record<string, Linter.RuleEntry>;
  };
  return config.rules?.['no-restricted-syntax'];
}

async function htmlErrors(code: string): Promise<number> {
  const rule = await syntaxRule('src/components/artifact-review/editor/controller.ts');
  const messages = new Linter().verify(code, {
    languageOptions: { ecmaVersion: 'latest', sourceType: 'module' },
    rules: { 'no-restricted-syntax': rule! },
  });
  return messages.filter((m) => m.message.includes('must not turn strings into HTML')).length;
}

describe('review editor HTML lint rule', () => {
  it('applies to the editor sources', async () => {
    const rule = await syntaxRule('src/components/artifact-review/review-editor.ts');
    const level = Array.isArray(rule) ? rule[0] : rule;
    expect(level === 'error' || level === 2).toBe(true);
  });

  it.each([
    ['innerHTML assignment', 'el.innerHTML = s;'],
    ['computed outerHTML', "el['outerHTML'] = s;"],
    ['insertAdjacentHTML', "el.insertAdjacentHTML('beforeend', s);"],
    ['DOMParser', 'new DOMParser();'],
    ['document.write', 'document.write(s);'],
    ['computed document.write', "document['write'](s);"],
    ['computed document.writeln', "document['writeln'](s);"],
    ['Lit .innerHTML binding', 'html`<div .innerHTML=${s}></div>`;'],
    ['Lit .srcdoc binding', 'html`<iframe .srcdoc = ${s}></iframe>`;'],
    ['object key innerHTML', 'Object.assign(el, { innerHTML: s });'],
    ['shorthand key innerHTML', 'const innerHTML = s; Object.assign(el, { innerHTML });'],
    ['quoted key outerHTML', "Object.assign(el, { 'outerHTML': s });"],
    ['Lit srcdoc attribute binding', 'html`<iframe srcdoc=${s}></iframe>`;'],
    ['quoted Lit srcdoc attribute binding', 'html`<iframe srcdoc="${s}"></iframe>`;'],
    ['window.document.write', 'window.document.write(s);'],
    ['upper-case Lit SRCDOC binding', 'html`<iframe SRCDOC=${s}></iframe>`;'],
    ["setAttribute('srcdoc')", "el.setAttribute('srcdoc', s);"],
    ["setAttribute('onclick')", "el.setAttribute('onclick', s);"],
    ["setAttribute('ONLOAD')", "el.setAttribute('ONLOAD', s);"],
    ['globalThis.document.writeln', 'globalThis.document.writeln(s);'],
  ])('catches %s', async (_name, code) => {
    expect(await htmlErrors(code)).toBeGreaterThan(0);
  });

  it.each([
    ['textContent', 'el.textContent = s;'],
    ['Lit text binding', 'html`<div title=${s}>${s}</div>`;'],
    ['object key textContent', 'Object.assign(el, { textContent: s });'],
    ['a write method on another object', 'stream.write(s);'],
    ['write on another member', 'window.stream.write(s);'],
    ["setAttribute('title')", "el.setAttribute('title', s);"],
    ['a srcdoc word in template text', 'html`<p>srcdoc is an attribute</p>`;'],
  ])('allows %s', async (_name, code) => {
    expect(await htmlErrors(code)).toBe(0);
  });
});
