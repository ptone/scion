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
 * Markdown rendering for the artifact preview.
 *
 * The preview is a sandboxed iframe (srcdoc) whose document carries its own
 * Content-Security-Policy, so the artifact surface allows images only from
 * the hub origin whatever the rest of the app allows:
 *
 *   - sandbox="allow-same-origin" (plus popups for links) and never
 *     allow-scripts: no script can run in the frame, and the same origin lets
 *     the session cookie authenticate the hub image requests;
 *   - CSP default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'.
 *
 * Images are rewritten before the document is built: a relative path loads
 * the version's own file, an absolute http(s) URL loads the copy the hub
 * fetched at publish time (files/_remote/...), found through the version's
 * manifest, and anything else becomes a placeholder. Every image request
 * uses ?stream=1, so it is answered by the hub itself.
 */

import type { ArtifactFile } from '../client/artifacts.js';

/** CSP of the artifact preview document. */
export const ARTIFACT_PREVIEW_CSP =
  "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'";

/**
 * Sandbox flags of the artifact markdown preview frame. allow-scripts is
 * never set: together with allow-same-origin it would let a script lift
 * the sandbox.
 */
export const ARTIFACT_PREVIEW_SANDBOX =
  'allow-same-origin allow-popups allow-popups-to-escape-sandbox';

/** Where a version's files live and what the version's manifest holds. */
export interface ArtifactImageContext {
  /** Path prefix of the version's files, ending in "/files/". */
  filesBase: string;
  files: ArtifactFile[];
}

export type ResolvedImage = { kind: 'file' | 'remote'; src: string } | { kind: 'placeholder' };

const SCHEME = /^[a-z][a-z0-9+.-]*:/i;
const IMG_TAG = /^\s*<img\b[^<>]*>\s*$/i;
const FORBID_TAGS = [
  'style',
  'picture',
  'source',
  'video',
  'audio',
  'track',
  'iframe',
  'frame',
  'object',
  'embed',
  'form',
  'input',
  'button',
  'svg',
  'math',
  'link',
  'meta',
  'base',
];
const FORBID_ATTR = [
  'srcset',
  'sizes',
  'style',
  'poster',
  'background',
  'formaction',
  'xlink:href',
  'ping',
];
const RELATIVE_BASE = 'https://artifact.invalid/files/';

/** Absolute http(s) URL in a normalized form, or null. */
export function normalizeRemoteUrl(u: string): string | null {
  try {
    const parsed = new URL(u.trim());
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.href : null;
  } catch {
    return null;
  }
}

function encodePath(p: string): string {
  return p
    .split('/')
    .map((seg) => encodeURIComponent(seg))
    .join('/');
}

/**
 * Decides what an image source becomes in the preview. Both sides of the
 * remote match are normalized by the URL parser, so a URL written with or
 * without percent-encoding finds its manifest entry.
 */
export function resolveImageSrc(rawSrc: string, ctx: ArtifactImageContext): ResolvedImage {
  const src = rawSrc.trim();
  if (!src || src.startsWith('//') || src.startsWith('\\')) {
    return { kind: 'placeholder' };
  }
  if (SCHEME.test(src)) {
    const normalized = normalizeRemoteUrl(src);
    if (!normalized) return { kind: 'placeholder' };
    const file = ctx.files.find(
      (f) => f.origin === 'remote' && f.sourceUrl && normalizeRemoteUrl(f.sourceUrl) === normalized
    );
    if (!file || file.fetchStatus !== 'ok') return { kind: 'placeholder' };
    return { kind: 'remote', src: `${ctx.filesBase}${encodePath(file.path)}?stream=1` };
  }
  let rel: string;
  try {
    const resolved = new URL(src, RELATIVE_BASE);
    if (
      resolved.origin !== new URL(RELATIVE_BASE).origin ||
      !resolved.pathname.startsWith('/files/')
    ) {
      return { kind: 'placeholder' };
    }
    rel = decodeURIComponent(resolved.pathname.slice('/files/'.length));
  } catch {
    return { kind: 'placeholder' };
  }
  if (!rel || rel === '_remote' || rel.startsWith('_remote/')) {
    return { kind: 'placeholder' };
  }
  return { kind: 'file', src: `${ctx.filesBase}${encodePath(rel)}?stream=1` };
}

/** The element shown instead of an image that cannot be loaded. */
export function imagePlaceholder(doc: Document, alt: string): HTMLElement {
  const span = doc.createElement('span');
  span.className = 'artifact-image-placeholder';
  span.setAttribute('role', 'img');
  span.setAttribute('aria-label', alt || 'Image unavailable');
  span.setAttribute('title', 'Image unavailable');
  span.textContent = alt ? `[image: ${alt}]` : '[image unavailable]';
  return span;
}

/** Rewrites every image under root; see resolveImageSrc. */
export function rewriteImages(root: ParentNode, ctx: ArtifactImageContext): void {
  for (const el of Array.from(root.querySelectorAll('[src]'))) {
    if (el.tagName !== 'IMG') el.removeAttribute('src');
  }
  for (const img of Array.from(root.querySelectorAll('img'))) {
    img.removeAttribute('srcset');
    img.removeAttribute('sizes');
    const resolved = resolveImageSrc(img.getAttribute('src') ?? '', ctx);
    if (resolved.kind === 'placeholder') {
      img.replaceWith(imagePlaceholder(img.ownerDocument, img.getAttribute('alt') ?? ''));
      continue;
    }
    img.setAttribute('src', resolved.src);
    img.setAttribute('loading', 'lazy');
    img.setAttribute('referrerpolicy', 'no-referrer');
  }
}

function escapeHtml(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

/**
 * Renders artifact markdown to sanitized HTML with its images rewritten.
 * Raw HTML is shown as text, except a standalone <img> tag, which is
 * sanitized and rewritten like a markdown image.
 */
export async function renderArtifactMarkdown(
  markdown: string,
  ctx: ArtifactImageContext
): Promise<string> {
  const [{ Marked }, purifyModule] = await Promise.all([import('marked'), import('dompurify')]);
  const createPurify = (purifyModule.default ?? purifyModule) as unknown as (w: Window) => {
    addHook(name: string, fn: (node: Element) => void): void;
    sanitize(dirty: string, cfg: Record<string, unknown>): DocumentFragment;
  };
  const purify = createPurify(window);
  purify.addHook('afterSanitizeAttributes', (node: Element) => {
    if (node.tagName === 'A') {
      node.setAttribute('target', '_blank');
      node.setAttribute('rel', 'noopener noreferrer');
    }
  });
  const marked = new Marked({
    renderer: {
      html({ text }: { text: string }): string {
        return IMG_TAG.test(text) ? text : escapeHtml(text);
      },
    },
  });
  const raw = marked.parse(markdown, { async: false });
  const fragment = purify.sanitize(raw, {
    RETURN_DOM_FRAGMENT: true,
    FORBID_TAGS,
    FORBID_ATTR,
  });
  rewriteImages(fragment, ctx);
  const holder = document.createElement('div');
  holder.appendChild(fragment);
  return holder.innerHTML;
}

const PREVIEW_STYLES = `
  body { margin: 0; padding: 1.5rem 2rem; font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
    font-size: 0.9375rem; line-height: 1.7; color: #1e293b; background: #ffffff; overflow-wrap: anywhere; }
  h1, h2, h3, h4, h5, h6 { margin: 1.5em 0 0.5em; font-weight: 600; line-height: 1.3; }
  h1 { font-size: 1.75rem; border-bottom: 1px solid #e2e8f0; padding-bottom: 0.3em; }
  h2 { font-size: 1.375rem; border-bottom: 1px solid #e2e8f0; padding-bottom: 0.3em; }
  body > :first-child { margin-top: 0; }
  p, ul, ol, blockquote, pre, table { margin: 0 0 1em; }
  a { color: #2563eb; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.85em;
    background: #f1f5f9; padding: 0.15em 0.35em; border-radius: 4px; }
  pre { background: #f1f5f9; padding: 0.75rem 1rem; border-radius: 6px; overflow-x: auto; }
  pre code { background: none; padding: 0; }
  blockquote { border-left: 4px solid #e2e8f0; padding-left: 1rem; color: #64748b; }
  table { border-collapse: collapse; }
  th, td { border: 1px solid #e2e8f0; padding: 0.4rem 0.75rem; }
  img { max-width: 100%; height: auto; }
  .artifact-image-placeholder { display: inline-block; padding: 0.25rem 0.5rem; border: 1px dashed #cbd5e1;
    border-radius: 4px; color: #64748b; font-size: 0.8125rem; background: #f8fafc; }
`;

/** The complete srcdoc document of the preview frame. It has no script. */
export function buildPreviewDocument(bodyHtml: string): string {
  return [
    '<!doctype html><html><head><meta charset="utf-8">',
    `<meta http-equiv="Content-Security-Policy" content="${ARTIFACT_PREVIEW_CSP}">`,
    '<meta name="referrer" content="no-referrer">',
    `<style>${PREVIEW_STYLES}</style>`,
    `</head><body>${bodyHtml}</body></html>`,
  ].join('');
}
