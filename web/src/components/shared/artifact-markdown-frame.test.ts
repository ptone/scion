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

import { afterEach, beforeAll, describe, expect, it } from 'vitest';
import type { ArtifactFile } from '../../client/artifacts.js';
import type { ScionArtifactMarkdownFrame } from './artifact-markdown-frame.js';

const BASE = '/api/v1/artifacts/a1/versions/1/files/';
const files: ArtifactFile[] = [
  { path: 'doc.md', size: 1, sha256: 'x', mediaType: 'text/markdown' },
  {
    path: '_remote/' + 'a'.repeat(64),
    size: 5,
    sha256: 'y',
    mediaType: 'image/png',
    origin: 'remote',
    sourceUrl: 'https://img.example/good.png',
    fetchStatus: 'ok',
  },
];

async function mount(content: string): Promise<ScionArtifactMarkdownFrame> {
  const el = document.createElement('scion-artifact-markdown-frame') as ScionArtifactMarkdownFrame;
  el.content = content;
  el.filesBase = BASE;
  el.files = files;
  document.body.appendChild(el);
  for (let i = 0; i < 20; i++) {
    await el.updateComplete;
    const frame = el.shadowRoot!.querySelector('iframe');
    if (frame && frame.srcdoc) return el;
    await new Promise((r) => setTimeout(r, 10));
  }
  return el;
}

describe('scion-artifact-markdown-frame', () => {
  beforeAll(async () => {
    await import('./artifact-markdown-frame.js');
  }, 30_000);

  afterEach(() => {
    document.body.innerHTML = '';
  });

  it('renders into a sandboxed frame without scripts', async () => {
    const el = await mount('# Hi\n\n![g](https://img.example/good.png)\n\n![r](img/a.png)');
    const frame = el.shadowRoot!.querySelector('iframe')!;
    const sandbox = frame.getAttribute('sandbox') ?? '';
    expect(sandbox.split(/\s+/)).toContain('allow-same-origin');
    expect(sandbox).not.toContain('allow-scripts');
    const srcdoc = frame.srcdoc;
    expect(srcdoc).toContain(
      `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'">`
    );
    expect(srcdoc.toLowerCase()).not.toContain('<script');
    expect(srcdoc).not.toContain('img.example');
    const doc = new DOMParser().parseFromString(srcdoc, 'text/html');
    const srcs = Array.from(doc.querySelectorAll('img')).map((i) => i.getAttribute('src'));
    expect(srcs).toEqual([
      `${BASE}_remote/${'a'.repeat(64)}?stream=1`,
      `${BASE}img/a.png?stream=1`,
    ]);
  });
});
