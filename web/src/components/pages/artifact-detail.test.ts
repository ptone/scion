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
 * Artifact page (ptone/scion#3213): gated on hub.artifacts, renders
 * markdown / text / images, and shows 404 for missing or unreadable
 * artifacts.
 */

import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import type { ArtifactResponse } from '../../client/artifacts.js';
import type { ScionPageArtifactDetail } from './artifact-detail.js';

const ID = '5f1c2d3e-0000-4000-8000-000000000001';

function artifact(path: string, mediaType: string): ArtifactResponse {
  return {
    artifact: {
      id: ID,
      ref: `scion://artifact/${ID}`,
      scopeKind: 'project',
      scopeRef: 'p-1',
      ownerKind: 'user',
      ownerRef: 'u-1',
      title: 'Design',
      currentSeq: 1,
      createdAt: '2026-10-05T12:00:00Z',
      updatedAt: '2026-10-05T12:00:00Z',
    },
    version: {
      seq: 1,
      ref: `scion://artifact/${ID}@1`,
      kind: 'publish',
      entryPath: path,
      totalBytes: 5,
      fileCount: 1,
      createdAt: '2026-10-05T12:00:00Z',
      state: 'ready',
      files: [{ path, size: 5, sha256: 'ab', mediaType }],
    },
  };
}

/** Mocks fetch: metadata answers meta (or 404 when null), file reads answer body. */
function mockFetch(meta: ArtifactResponse | null, body = '# Hello'): string[] {
  const urls: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      urls.push(url);
      if (url.includes('/files/')) {
        return Promise.resolve(new Response(body, { status: 200 }));
      }
      if (meta === null) {
        return Promise.resolve(
          new Response('{"error":{"code":"not_found","message":"not found"}}', { status: 404 })
        );
      }
      return Promise.resolve(
        new Response(JSON.stringify(meta), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
    })
  );
  return urls;
}

async function mount(flag: boolean): Promise<ScionPageArtifactDetail> {
  window.__SCION_FEATURES__ = { 'hub.artifacts': flag };
  const el = document.createElement('scion-page-artifact-detail') as ScionPageArtifactDetail;
  el.pageData = { path: `/projects/p-1/artifacts/${ID}` } as ScionPageArtifactDetail['pageData'];
  document.body.appendChild(el);
  for (let i = 0; i < 20; i++) {
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
  }
  return el;
}

describe('artifact page', () => {
  beforeAll(async () => {
    await import('./artifact-detail.js');
  }, 30_000);

  afterEach(() => {
    document.body.innerHTML = '';
    vi.unstubAllGlobals();
    delete window.__SCION_FEATURES__;
  });

  it('shows nothing but a 404 when the experiment is off', async () => {
    const urls = mockFetch(artifact('a.md', 'text/markdown'));
    const el = await mount(false);
    expect(el.shadowRoot!.querySelector('scion-page-404')).not.toBeNull();
    expect(urls).toHaveLength(0);
  });

  it('renders markdown, fetching the bytes through the hub', async () => {
    const urls = mockFetch(artifact('design.md', 'text/markdown'), '# Hello');
    const el = await mount(true);
    const preview = el.shadowRoot!.querySelector('scion-markdown-preview') as
      | (HTMLElement & { content: string })
      | null;
    expect(preview).not.toBeNull();
    expect(preview!.content).toBe('# Hello');
    expect(urls).toContain(`/api/v1/artifacts/${ID}/versions/1/files/design.md?stream=1`);
    expect(el.shadowRoot!.querySelector('h1')!.textContent).toBe('Design');
  });

  it('renders text read-only in the code editor', async () => {
    mockFetch(artifact('notes.json', 'application/json'), '{"a":1}');
    const el = await mount(true);
    const editor = el.shadowRoot!.querySelector('scion-code-editor') as
      | (HTMLElement & { content: string; readonly: boolean; language: string })
      | null;
    expect(editor).not.toBeNull();
    expect(editor!.content).toBe('{"a":1}');
    expect(editor!.readonly).toBe(true);
  });

  it('renders images with <img> pointing at the file route', async () => {
    const urls = mockFetch(artifact('shot.png', 'image/png'));
    const el = await mount(true);
    const img = el.shadowRoot!.querySelector('img');
    expect(img?.getAttribute('src')).toBe(`/api/v1/artifacts/${ID}/versions/1/files/shot.png`);
    expect(urls.some((u) => u.includes('/files/'))).toBe(false);
  });

  it('offers HTML only as a download', async () => {
    const urls = mockFetch(artifact('page.html', 'text/html'));
    const el = await mount(true);
    expect(el.shadowRoot!.querySelector('iframe')).toBeNull();
    expect(el.shadowRoot!.querySelector('.download-state')).not.toBeNull();
    expect(urls.some((u) => u.includes('/files/'))).toBe(false);
  });

  it('shows 404 when the hub answers 404', async () => {
    mockFetch(null);
    const el = await mount(true);
    expect(el.shadowRoot!.querySelector('scion-page-404')).not.toBeNull();
  });
});
