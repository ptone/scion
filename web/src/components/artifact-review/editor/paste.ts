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
 * Paste and drop in the review editor.
 *
 * The editor takes over every paste: it reads the clipboard's Markdown or
 * plain text (never its HTML), reads it with the editor's own Markdown
 * parser, and inserts it as one edit, which the tracker records as a
 * suggestion. Several paragraphs, headings or list items go in as new
 * blocks with suggested breaks (structural.ts); code blocks and tables go
 * in as plain paragraphs; raw HTML is not pasted. Copies from the editor itself paste the same way, from the
 * text the editor put on the clipboard. Pasted HTML that does reach
 * ProseMirror's own paste path is emptied first. Dropping files or content
 * is refused; image files get the hint that images are added by
 * publishing a new version.
 */

import { Fragment, Slice } from '@milkdown/prose/model';
import type { Node } from '@milkdown/prose/model';
import { Plugin, PluginKey, TextSelection } from '@milkdown/prose/state';
import type { EditorView } from '@milkdown/prose/view';

import { parseCritic } from '../../../utils/critic.js';
import type { HintKind } from './policy.js';
import { CRITIC_MARK_NAMES } from './schema.js';
import { buildPaste, isRefusal } from './structural.js';
import type { Piece } from './structural.js';
import { STRUCT_META } from './track.js';

export const pasteKey = new PluginKey('criticPaste');

function hasImageFile(data: DataTransfer | null): boolean {
  if (!data) return false;
  for (const item of Array.from(data.items ?? [])) {
    if (item.kind === 'file' && item.type.startsWith('image/')) return true;
  }
  for (const f of Array.from(data.files ?? [])) if (f.type.startsWith('image/')) return true;
  return false;
}

/** The clipboard text the editor reads: Markdown if offered, else plain text. */
export function clipboardText(data: DataTransfer | null): string {
  if (!data) return '';
  const md = data.getData('text/markdown');
  return (md || data.getData('text/plain') || '').replace(/\r\n?/g, '\n');
}

/**
 * The blocks of a parsed paste as paragraphs and headings with their
 * inline content: list items and quotes give their paragraphs, a code
 * block or a table gives plain-text paragraphs (one per line or row), and
 * a thematic break gives nothing. Raw HTML anywhere, or a critic mark,
 * refuses the paste (null): it is never pasted as HTML.
 */
export function pastePieces(doc: Node): Piece[] | null {
  const out: Piece[] = [];
  let ok = true;
  const schema = doc.type.schema;
  // A line of code or a table row becomes paragraph text, where a mark
  // token it holds would be read as a mark: such a paste is refused.
  const plainLines = (text: string): void => {
    for (const line of text.split('\n')) {
      if (!line.trim()) continue;
      if (parseCritic(line).some((x) => x.kind !== 'text')) ok = false;
      out.push({ type: 'paragraph', content: Fragment.from(schema.text(line)) });
    }
  };
  const walk = (n: Node): void => {
    if (!ok) return;
    if (n.type.name === 'html') {
      ok = false;
      return;
    }
    if (n.type.spec.code && n.isTextblock) {
      plainLines(n.textContent);
      return;
    }
    if (n.type.name === 'table') {
      n.forEach((row) => {
        const cells: string[] = [];
        row.forEach((cell) => cells.push(cell.textContent.trim()));
        plainLines(cells.filter((c) => c).join(' '));
      });
      return;
    }
    if (n.isTextblock) {
      const nodes: Node[] = [];
      n.forEach((x) => {
        if (x.type.name === 'html' || x.marks.some((m) => CRITIC_MARK_NAMES.has(m.type.name))) {
          ok = false;
        }
        nodes.push(x);
      });
      const heading = n.type.name === 'heading';
      out.push({
        type: heading ? 'heading' : 'paragraph',
        level: heading ? Number(n.attrs.level) : undefined,
        content: Fragment.from(nodes),
      });
      return;
    }
    n.forEach(walk);
  };
  doc.forEach(walk);
  return ok ? out : null;
}

/**
 * What pasted text, read as Markdown, inserts: inline content (one
 * paragraph's, or one block's text), several blocks (pastePieces), or a
 * refusal hint. Text carrying CriticMarkup, which would be read as marks,
 * and raw HTML are not pasted.
 */
export function pasteContent(
  text: string,
  parse: (md: string) => Node
): { content: Fragment } | { pieces: Piece[] } | { refuse: HintKind } {
  const trimmed = text.replace(/^\n+|\n+$/g, '');
  if (trimmed === '') return { content: Fragment.empty };
  if (parseCritic(trimmed).some((s) => s.kind !== 'text')) return { refuse: 'generic' };
  const pieces = pastePieces(parse(trimmed));
  if (!pieces) return { refuse: 'generic' };
  if (pieces.length === 0) return { content: Fragment.empty };
  if (pieces.length === 1) return { content: pieces[0].content };
  return { pieces };
}

/** Inserts what a paste carries (its text only), or refuses it with a hint. */
export function pasteInto(
  view: EditorView,
  data: DataTransfer | null,
  parse: (md: string) => Node,
  onRefuse: (hint: HintKind) => void
): void {
  if (hasImageFile(data)) {
    onRefuse('image');
    return;
  }
  const read = pasteContent(clipboardText(data), parse);
  if ('refuse' in read) {
    onRefuse(read.refuse);
    return;
  }
  const { from, to } = view.state.selection;
  if ('pieces' in read) {
    // Several blocks: built in their tracked form; the policy checks them.
    const tr = view.state.tr;
    const r = buildPaste(tr, from, to, read.pieces);
    if (isRefusal(r)) {
      onRefuse(r.refuse);
      return;
    }
    if (!('head' in r)) return;
    tr.setSelection(TextSelection.create(tr.doc, r.head));
    tr.setMeta(STRUCT_META, { cls: 'cross-block', from, to });
    view.dispatch(tr.scrollIntoView());
    return;
  }
  if (read.content.size === 0) return;
  const tr = view.state.tr.replace(from, to, new Slice(read.content, 0, 0));
  view.dispatch(tr.scrollIntoView());
}

/**
 * Takes over paste, drop and drag on the element around the editor, in the
 * capture phase, so neither ProseMirror's own paths (some of which skip
 * the plugin props, for example while composing, or for a drop with no
 * position) nor the browser's default insertion ever see them. Returns a
 * function that removes the listeners.
 */
export function guardTransfers(
  root: HTMLElement,
  view: () => EditorView,
  parse: (md: string) => Node,
  onRefuse: (hint: HintKind) => void
): () => void {
  const onPaste = (e: Event): void => {
    e.preventDefault();
    e.stopPropagation();
    pasteInto(view(), (e as ClipboardEvent).clipboardData, parse, onRefuse);
  };
  const onDrop = (e: Event): void => {
    e.preventDefault();
    e.stopPropagation();
    if (hasImageFile((e as DragEvent).dataTransfer)) onRefuse('image');
  };
  const onDrag = (e: Event): void => {
    e.preventDefault();
    e.stopPropagation();
    const dt = (e as DragEvent).dataTransfer;
    if (dt) dt.dropEffect = 'none';
  };
  const events: [string, (e: Event) => void][] = [
    ['paste', onPaste],
    ['drop', onDrop],
    ['dragover', onDrag],
    ['dragenter', onDrag],
    ['dragstart', onDrag],
  ];
  for (const [name, fn] of events) root.addEventListener(name, fn, true);
  return (): void => {
    for (const [name, fn] of events) root.removeEventListener(name, fn, true);
  };
}

/** The paste and drop plugin (behind guardTransfers, which normally handles both). */
export function pastePlugin(
  parse: (md: string) => Node,
  onRefuse: (hint: HintKind) => void
): Plugin {
  return new Plugin({
    key: pasteKey,
    props: {
      handlePaste(view: EditorView, event: ClipboardEvent): boolean {
        pasteInto(view, event.clipboardData, parse, onRefuse);
        return true;
      },
      transformPastedHTML(): string {
        return '';
      },
      handleDrop(_view: EditorView, event: DragEvent): boolean {
        if (hasImageFile(event.dataTransfer)) onRefuse('image');
        return true;
      },
    },
  });
}
