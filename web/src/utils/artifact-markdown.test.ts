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
import type { ArtifactFile } from '../client/artifacts.js';
import {
  ARTIFACT_PREVIEW_CSP,
  ARTIFACT_PREVIEW_SANDBOX,
  buildPreviewDocument,
  renderArtifactMarkdown,
  resolveImageSrc,
} from './artifact-markdown.js';

const BASE = '/api/v1/artifacts/a1/versions/2/files/';
const GOOD = 'https://img.example/good.png';
const GOOD_PATH = '_remote/' + 'a'.repeat(64);
const FAILED = 'https://img.example/failed.png';
const FAILED_PATH = '_remote/' + 'b'.repeat(64);
const UNICODE = 'https://img.example/ü.png';
const UNICODE_PATH = '_remote/' + 'c'.repeat(64);

const files: ArtifactFile[] = [
  { path: 'doc.md', size: 10, sha256: 'x', mediaType: 'text/markdown' },
  {
    path: GOOD_PATH,
    size: 5,
    sha256: 'y',
    mediaType: 'image/png',
    origin: 'remote',
    sourceUrl: GOOD,
    fetchStatus: 'ok',
  },
  {
    path: FAILED_PATH,
    size: 0,
    sha256: '',
    mediaType: '',
    origin: 'remote',
    sourceUrl: FAILED,
    fetchStatus: 'failed',
  },
  {
    path: UNICODE_PATH,
    size: 5,
    sha256: 'z',
    mediaType: 'image/png',
    origin: 'remote',
    sourceUrl: UNICODE,
    fetchStatus: 'ok',
  },
];
const ctx = { filesBase: BASE, files };

describe('resolveImageSrc', () => {
  it('maps a fetched remote image to its _remote file, streamed', () => {
    expect(resolveImageSrc(GOOD, ctx)).toEqual({
      kind: 'remote',
      src: `${BASE}${GOOD_PATH}?stream=1`,
    });
  });
  it('matches a remote URL whatever its percent-encoding', () => {
    expect(resolveImageSrc('https://img.example/%C3%BC.png', ctx)).toEqual({
      kind: 'remote',
      src: `${BASE}${UNICODE_PATH}?stream=1`,
    });
  });
  it('shows a placeholder for a failed or unknown remote image', () => {
    expect(resolveImageSrc(FAILED, ctx)).toEqual({ kind: 'placeholder' });
    expect(resolveImageSrc('https://img.example/never-fetched.png', ctx)).toEqual({
      kind: 'placeholder',
    });
  });
  it('resolves relative paths against the version files', () => {
    expect(resolveImageSrc('img/a b.png', ctx)).toEqual({
      kind: 'file',
      src: `${BASE}img/a%20b.png?stream=1`,
    });
    expect(resolveImageSrc('./img/../shot.png', ctx)).toEqual({
      kind: 'file',
      src: `${BASE}shot.png?stream=1`,
    });
  });
  it('drops everything else', () => {
    for (const src of [
      '',
      'data:image/png;base64,AAAA',
      'javascript:alert(1)',
      'file:///etc/passwd',
      '//img.example/x.png',
      '../../../../etc/passwd',
      '/api/v1/agents',
      '_remote/' + 'a'.repeat(64),
    ]) {
      expect(resolveImageSrc(src, ctx), src).toEqual({ kind: 'placeholder' });
    }
  });
});

describe('renderArtifactMarkdown', () => {
  it('rewrites images and leaves no remote image source', async () => {
    const md = [
      `![good](${GOOD})`,
      `![failed](${FAILED})`,
      '![local](img/local.png)',
      '![data](data:image/png;base64,AAAA)',
      `<img src="${GOOD}" srcset="https://evil.example/x.png 2x" alt="inline">`,
      '<script>alert(1)</script>',
      '<video src="https://img.example/v.mp4"></video>',
    ].join('\n\n');
    const out = await renderArtifactMarkdown(md, ctx);
    const doc = new DOMParser().parseFromString(out, 'text/html');
    const srcs = Array.from(doc.querySelectorAll('img')).map((i) => i.getAttribute('src'));
    expect(srcs).toEqual([
      `${BASE}${GOOD_PATH}?stream=1`,
      `${BASE}img/local.png?stream=1`,
      `${BASE}${GOOD_PATH}?stream=1`,
    ]);
    for (const src of srcs) {
      expect(src!.startsWith(BASE) && src!.endsWith('?stream=1')).toBe(true);
    }
    expect(doc.querySelectorAll('.artifact-image-placeholder')).toHaveLength(2);
    expect(doc.querySelector('script')).toBeNull();
    expect(doc.querySelector('video')).toBeNull();
    expect(doc.querySelector('[srcset]')).toBeNull();
    // Escaped raw HTML may mention a host as visible text; no element or
    // attribute may reference one.
    for (const el of Array.from(doc.body.querySelectorAll('*'))) {
      for (const attr of Array.from(el.attributes)) {
        expect(attr.value, `${el.tagName} ${attr.name}`).not.toMatch(/img\.example|evil\.example/);
      }
    }
  });

  it('shows other raw HTML as text', async () => {
    const out = await renderArtifactMarkdown('<b>bold</b> and <iframe src="x"></iframe>', ctx);
    expect(out).toContain('&lt;b&gt;');
    expect(out).not.toContain('<iframe');
  });
});

describe('preview document', () => {
  it('carries the CSP and no script', () => {
    const doc = buildPreviewDocument('<p>hi</p>');
    expect(doc).toContain(
      `<meta http-equiv="Content-Security-Policy" content="${ARTIFACT_PREVIEW_CSP}">`
    );
    expect(ARTIFACT_PREVIEW_CSP).toBe(
      "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'"
    );
    expect(ARTIFACT_PREVIEW_CSP).not.toContain('script-src');
    expect(doc.toLowerCase()).not.toContain('<script');
  });
  it('never combines allow-scripts with allow-same-origin', () => {
    expect(ARTIFACT_PREVIEW_SANDBOX.split(/\s+/)).toContain('allow-same-origin');
    expect(ARTIFACT_PREVIEW_SANDBOX).not.toContain('allow-scripts');
  });
});
