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
 * A fixed 200 kB Markdown document (about 3,600 top-level blocks: headings,
 * paragraphs with marks, lists, tables and code), the same every time.
 * With hazard, its first line holds a literal opener (design E7), so the
 * per-edit whole-text check runs.
 */
export function largeDoc(hazard: boolean): string {
  const parts: string[] = [];
  parts.push(
    hazard
      ? 'Notes: {== an unclosed opener stays literal here.\n'
      : 'Notes: an opening paragraph.\n'
  );
  // The length of parts joined by line breaks.
  let size = parts[0].length;
  for (let i = 1; size < 200_000; i++) {
    parts.push(
      `## Section ${i}\n`,
      `Paragraph ${i} with some words, a [link](https://example.com/${i}) and \`code ${i}\` and {++an insertion++} here.\n`,
      `* item ${i} one\n* item ${i} two with *emphasis*\n* item ${i} three\n`,
      `| col | val |\n|:----|----:|\n| a${i} | ${i} |\n| b${i} | {--old--} |\n`,
      '```js\nconst x = ' + i + ';\n```\n'
    );
    size = parts.reduce((n, p) => n + p.length, 0) + parts.length - 1;
  }
  return parts.join('\n');
}
