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
 * The review editor without its UI: the Milkdown core, the suggest-mode
 * plugins (policy, tracking, paste, shortcuts) and the session. The Lit
 * element (review-editor.ts) supplies the note dialog and shows hints.
 */

import { keymap } from '@milkdown/prose/keymap';
import { Fragment } from '@milkdown/prose/model';
import type { Node, Slice } from '@milkdown/prose/model';
import { Transform } from '@milkdown/prose/transform';
import { NodeSelection, Plugin, PluginKey, TextSelection } from '@milkdown/prose/state';
import type { Transaction } from '@milkdown/prose/state';
import type { EditorView } from '@milkdown/prose/view';

import { artifactLinkTarget } from '../../../client/artifact-preview.js';
import { createCore } from './create.js';
import type { Core } from './create.js';
import { marksReadBack } from './guard.js';
import { guardTransfers, pastePlugin } from './paste.js';
import { containerClass, policyPlugin } from './policy.js';
import type { HintKind } from './policy.js';
import { addComment, commentNumber, editComment, planComment, selectedComment } from './review.js';
import type { RenderContext } from './safe-render.js';
import { COMMENT, CRITIC_MARK_NAMES } from './schema.js';
import { ReviewSession } from './session.js';
import { reconcileComposition, snapshotComposition, trackPlugin } from './track.js';
import type { CompositionSnapshot } from './track.js';

/** What the note dialog is asked to show. */
export interface NoteRequest {
  /** Existing note when editing, '' for a new comment. */
  note: string;
  /** The comment's number when editing, 0 for a new one. */
  number: number;
  /** Selected text for a new comment that highlights it, or '' . */
  quote: string;
  /** Text describing where a comment without highlight goes. */
  context: string;
}

/** The note dialog's answer: a note, or null when cancelled. */
export type AskNote = (req: NoteRequest) => Promise<string | null>;

export interface ControllerOptions {
  root: HTMLElement;
  render: () => RenderContext;
  askNote: AskNote;
  onHint: (hint: HintKind) => void;
  /** Called after each change of the document by the reviewer. */
  onChange: () => void;
}

/** A heading's fragment id, as GitHub writes it: lower case, punctuation dropped, spaces as hyphens. */
export function headingSlug(text: string): string {
  return text
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, '')
    .replace(/\s/g, '-');
}

/** Moves the cursor to the heading whose fragment id is id; false if none. */
export function scrollToHeading(view: EditorView, id: string): boolean {
  let want: string;
  try {
    want = decodeURIComponent(id).toLowerCase();
  } catch {
    return false;
  }
  let at = -1;
  view.state.doc.descendants((n, pos) => {
    if (at >= 0) return false;
    if (n.type.name === 'heading' && headingSlug(n.textContent) === want) at = pos + 1;
    return n.isBlock && !n.isTextblock;
  });
  if (at < 0) return false;
  view.dispatch(
    view.state.tr.setSelection(TextSelection.create(view.state.doc, at)).scrollIntoView()
  );
  view.focus();
  return true;
}

export class ReviewController {
  private core!: Core;
  private session!: ReviewSession;
  private loading = false;
  private readonly uiKey = new PluginKey<unknown>('criticUi');
  private composing: CompositionSnapshot | null = null;
  private unguard: () => void = () => {};

  private constructor(private readonly opts: ControllerOptions) {}

  static async create(opts: ControllerOptions): Promise<ReviewController> {
    const c = new ReviewController(opts);
    await c.init();
    return c;
  }

  get view(): EditorView {
    return this.core.view;
  }

  private async init(): Promise<void> {
    const opts = this.opts;
    const onRefuse = (hint: HintKind): void => opts.onHint(hint);
    const unitOk = (doc: Node, first: number, last: number): boolean =>
      this.session.unitOk(doc, first, last);
    let parse: (md: string) => Node = () => {
      throw new Error('editor not ready');
    };
    this.core = await createCore({
      root: opts.root,
      render: opts.render,
      plugins: () => [
        pastePlugin((md) => parse(md), onRefuse),
        policyPlugin(onRefuse, unitOk),
        trackPlugin(),
        this.uiPlugin(),
        keymap({
          'Mod-Alt-m': () => {
            void this.comment();
            return true;
          },
          'Ctrl-Alt-m': () => {
            void this.comment();
            return true;
          },
          Enter: (state) => {
            const c = selectedComment(state);
            if (!c) return false;
            void this.editNote(c.pos);
            return true;
          },
        }),
      ],
    });
    parse = this.core.parse;
    this.session = new ReviewSession(this.core);
    this.unguard = guardTransfers(opts.root, () => this.core.view, this.core.parse, onRefuse);
  }

  private uiPlugin(): Plugin {
    return new Plugin({
      key: this.uiKey,
      props: {
        // Shortcuts act on the selection the reviewer sees, even if the
        // browser has not reported its latest move yet.
        handleKeyDown: (view, event): boolean => {
          if (!view.composing && event.keyCode !== 229) this.syncSelection();
          return false;
        },
        // A copy puts the editor's own Markdown of the selection on the
        // clipboard (marks of the review left out), so a paste, which reads
        // text as Markdown, gets back what was copied.
        clipboardTextSerializer: (slice): string => this.sliceMarkdown(slice),
        handleDoubleClickOn: (_view, pos, node): boolean => {
          if (node.type.name !== COMMENT) return false;
          void this.editNote(pos);
          return true;
        },
        handleClick: (view, pos, event): boolean => {
          if (!(event.metaKey || event.ctrlKey)) return false;
          const target = event.target as Element | null;
          const a = target?.closest?.('a');
          if (!a) return false;
          const href = a.getAttribute('href');
          if (href) {
            window.open(href, '_blank', 'noopener,noreferrer');
            return true;
          }
          if (a.classList.contains('fragment-link')) {
            const link = view.state.doc
              .resolve(pos)
              .marks()
              .find((m) => m.type.name === 'link');
            const t = link ? artifactLinkTarget(String(link.attrs.href), this.opts.render()) : null;
            if (t?.kind === 'fragment') return scrollToHeading(view, t.id);
          }
          return false;
        },
        handleDOMEvents: {
          compositionstart: (): boolean => {
            this.beginComposition();
            return false;
          },
          compositionend: (): boolean => {
            // After ProseMirror has applied the composition's last change.
            setTimeout(() => this.endComposition(), 0);
            return false;
          },
        },
      },
      state: {
        init: (): unknown => undefined,
        // The id of the latest composition transaction, for endComposition.
        apply: (tr, value): unknown => tr.getMeta('composition') ?? value,
      },
      view: () => ({
        update: (view, prev): void => {
          if (this.loading || view.state.doc.eq(prev.doc)) return;
          this.session.markDirty();
          this.opts.onChange();
        },
      }),
    });
  }

  /** The Markdown of a slice, without review marks or comments. */
  sliceMarkdown(slice: Slice): string {
    const schema = this.core.view.state.schema;
    let content = slice.content;
    let inline = true;
    content.forEach((n) => {
      if (!n.isInline) inline = false;
    });
    if (inline) content = Fragment.from(schema.nodes.paragraph.create(null, content));
    const doc = schema.topNodeType.createAndFill(null, content);
    if (!doc) return slice.content.textBetween(0, slice.content.size, '\n\n');
    const tr = new Transform(doc);
    for (const name of CRITIC_MARK_NAMES) tr.removeMark(0, tr.doc.content.size, schema.marks[name]);
    const comments: number[] = [];
    tr.doc.descendants((n, pos) => {
      if (n.type.name === COMMENT) comments.push(pos);
      return true;
    });
    for (const pos of comments.reverse()) tr.delete(pos, pos + 1);
    return this.core.serialize(tr.doc).replace(/\n+$/, '');
  }

  /** Snapshots the document as a composition starts. */
  beginComposition(): void {
    this.composing = snapshotComposition(this.core.view.state);
  }

  /**
   * Records the finished composition as an insertion, or restores the
   * document from before it (with a hint) when it cannot be recorded.
   */
  endComposition(): void {
    const snap = this.composing;
    this.composing = null;
    if (!snap) return;
    const view = this.core.view;
    const id: unknown = this.uiKey.getState(view.state);
    const refused = reconcileComposition(view, snap, id, (doc, pos) => {
      const container = containerClass(view.state.doc.resolve(pos + 1));
      if (container === 'code') return 'code';
      if (container) return 'list-table';
      const i = doc.resolve(pos).index(0);
      return this.session.unitOk(doc, i, i) ? null : 'generic';
    });
    if (refused) this.opts.onHint(refused as HintKind);
  }

  /** Opens md; false when it cannot be shown in this editor. */
  load(md: string): boolean {
    this.loading = true;
    try {
      return this.session.load(md);
    } finally {
      this.loading = false;
    }
  }

  /**
   * The current text, serializing only after an edit. Logs a diagnostic
   * (and still returns the text, which the page then checks) when the
   * saved marks do not read back as the document's.
   */
  flush(): string | null {
    const text = this.session.save();
    if (text !== null && this.session.isDirty) {
      const diff = marksReadBack(this.core.view.state.doc, text, this.core.plain);
      if (diff) console.error('review editor: saved marks read back differently', diff);
    }
    return text;
  }

  get dirty(): boolean {
    return this.session.isDirty;
  }

  /**
   * Takes the browser's selection into the editor state when it moved
   * without the editor seeing it yet (a shortcut pressed right after a
   * caret move, before the browser reported the selection change).
   */
  private syncSelection(): void {
    const view = this.core.view;
    const root = view.root as Document | (ShadowRoot & { getSelection?: () => Selection | null });
    const sel = root.getSelection?.() ?? document.getSelection();
    if (!sel?.anchorNode || !sel.focusNode) return;
    if (!view.dom.contains(sel.anchorNode) || !view.dom.contains(sel.focusNode)) return;
    let anchor: number;
    let head: number;
    try {
      anchor = view.posAtDOM(sel.anchorNode, sel.anchorOffset);
      head = view.posAtDOM(sel.focusNode, sel.focusOffset);
    } catch {
      return;
    }
    const cur = view.state.selection;
    if (cur.anchor === anchor && cur.head === head) return;
    if (cur instanceof NodeSelection && sel.isCollapsed) return;
    try {
      view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, anchor, head)));
    } catch {
      // Not a text position; keep the editor's own selection.
    }
  }

  /** Comment on the selection (toolbar button and shortcut). */
  async comment(): Promise<void> {
    const view = this.core.view;
    this.syncSelection();
    const plan = planComment(view.state);
    if ('refuse' in plan) {
      this.opts.onHint(plan.refuse);
      return;
    }
    const context = plan.highlight ? '' : this.commentContext(plan.at);
    const note = await this.opts.askNote({ note: '', number: 0, quote: plan.quote, context });
    view.focus();
    if (note === null || note.trim() === '') return;
    // The document may have changed while the dialog was open.
    const again = planComment(view.state);
    if ('refuse' in again) return;
    this.dispatchChecked(addComment(view.state, again, note), again.at);
  }

  /**
   * Edit the note of this session's comment at pos (Enter, double-click).
   * Comments already in the text are not edited here.
   */
  async editNote(pos: number): Promise<void> {
    const view = this.core.view;
    const node = view.state.doc.nodeAt(pos);
    if (node?.type.name !== COMMENT) return;
    if (node.attrs.mine !== true) {
      this.opts.onHint('generic');
      return;
    }
    const docBefore = view.state.doc;
    const number = commentNumber(view.state, pos);
    const note = await this.opts.askNote({
      note: String(node.attrs.note),
      number,
      quote: '',
      context: this.commentContext(pos),
    });
    view.focus();
    if (note === null) return;
    // The document may have changed while the dialog was open: the comment
    // must still be at pos, unchanged.
    if (view.state.doc !== docBefore && view.state.doc.nodeAt(pos) !== node) return;
    const tr = editComment(view.state, pos, note);
    if (tr) this.dispatchChecked(tr, pos);
  }

  /**
   * Dispatches a comment command unless its save would change text outside
   * marks or misread the marks (the same check as for typed edits); then
   * it shows the generic hint instead.
   */
  private dispatchChecked(tr: Transaction, pos: number): void {
    const view = this.core.view;
    const i = view.state.doc.resolve(Math.min(pos, view.state.doc.content.size)).index(0);
    if (!this.session.unitOk(tr.doc, i, i)) {
      this.opts.onHint('generic');
      return;
    }
    view.dispatch(tr);
  }

  /** "After “…text.”": where a comment without highlight sits. */
  private commentContext(pos: number): string {
    const $p = this.core.view.state.doc.resolve(pos);
    const before = $p.parent.textBetween(0, $p.parentOffset, ' ', ' ').trim();
    if (!before) return '';
    const tail = before.length > 40 ? `…${before.slice(-40)}` : before;
    return `After “${tail}”`;
  }

  focus(): void {
    this.core.view.focus();
  }

  async destroy(): Promise<void> {
    this.unguard();
    await this.core.destroy();
  }
}
