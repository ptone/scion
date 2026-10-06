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
 * Artifact Markdown Frame
 *
 * Shows a markdown artifact inside a sandboxed iframe. See
 * utils/artifact-markdown.ts for the frame's security properties. No script
 * ever runs inside the frame: the parent sizes it to its content and swaps a
 * failed image for a placeholder, which the same origin allows.
 */

import { LitElement, html, css } from 'lit';
import type { TemplateResult } from 'lit';
import { customElement, property, query, state } from 'lit/decorators.js';
import type { ArtifactFile } from '../../client/artifacts.js';
import {
  ARTIFACT_PREVIEW_SANDBOX,
  buildPreviewDocument,
  imagePlaceholder,
  renderArtifactMarkdown,
} from '../../utils/artifact-markdown.js';

@customElement('scion-artifact-markdown-frame')
export class ScionArtifactMarkdownFrame extends LitElement {
  /** Raw markdown text. */
  @property({ type: String }) content = '';
  /** Path prefix of the version's files, ending in "/files/". */
  @property({ type: String }) filesBase = '';
  /** The version's manifest. */
  @property({ attribute: false }) files: ArtifactFile[] = [];

  @state() private srcdoc = '';
  @state() private error: string | null = null;
  @query('iframe') private frame?: HTMLIFrameElement;

  static override styles = css`
    :host {
      display: block;
    }
    iframe {
      display: block;
      width: 100%;
      min-height: 200px;
      border: 1px solid var(--scion-border, #e2e8f0);
      border-radius: var(--scion-radius, 0.5rem);
      background: var(--scion-surface, #ffffff);
    }
    .error-state {
      padding: 1rem;
      color: var(--sl-color-danger-600, #dc2626);
      font-size: 0.875rem;
    }
  `;

  override updated(changed: Map<string, unknown>): void {
    if (changed.has('content') || changed.has('filesBase') || changed.has('files')) {
      void this.renderDocument();
    }
  }

  private async renderDocument(): Promise<void> {
    try {
      const body = await renderArtifactMarkdown(this.content, {
        filesBase: this.filesBase,
        files: this.files,
      });
      this.srcdoc = buildPreviewDocument(body);
      this.error = null;
    } catch (err) {
      console.error('Failed to render artifact markdown:', err);
      this.error = 'Failed to render markdown preview';
    }
  }

  private onLoad = (): void => {
    const frame = this.frame;
    const doc = frame?.contentDocument;
    if (!frame || !doc) return;
    const resize = (): void => {
      const h = doc.documentElement?.scrollHeight ?? 0;
      if (h > 0) frame.style.height = `${h + 2}px`;
    };
    for (const img of Array.from(doc.querySelectorAll('img'))) {
      img.addEventListener('load', resize);
      img.addEventListener('error', () => {
        img.replaceWith(imagePlaceholder(doc, img.getAttribute('alt') ?? ''));
        resize();
      });
    }
    resize();
  };

  override render(): TemplateResult {
    if (this.error) {
      return html`<div class="error-state">${this.error}</div>`;
    }
    return html`<iframe
      title="Artifact preview"
      sandbox=${ARTIFACT_PREVIEW_SANDBOX}
      referrerpolicy="no-referrer"
      .srcdoc=${this.srcdoc}
      @load=${this.onLoad}
    ></iframe>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-artifact-markdown-frame': ScionArtifactMarkdownFrame;
  }
}
