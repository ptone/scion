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
 * The editor's own shortcuts for what the Markdown presets' keymaps did.
 * Formatting, heading, list and code block shortcuts would change the
 * document in a way suggest mode does not record, so each one shows the
 * hint the policy gives for its class of edit and changes nothing. Enter,
 * Backspace and Delete at a block boundary, and Shift-Enter, make
 * suggested breaks, joins and line breaks (structural.ts). Tab in a table
 * moves between cells, and every other key keeps ProseMirror's base
 * behaviour.
 */

import type { Command, EditorState, Transaction } from '@milkdown/prose/state';
import { Selection, TextSelection } from '@milkdown/prose/state';

import { POLICY, containerClass } from './policy.js';
import type { EditClass, HintKind } from './policy.js';
import { buildDelete, buildJoin, buildSplit, isRefusal } from './structural.js';
import type { Built } from './structural.js';
import { STRUCT_META } from './track.js';
import type { StructuralMeta } from './track.js';

/** The formatting and block shortcuts of the presets, by the class of edit they make. */
export const REFUSED_SHORTCUTS: Record<string, EditClass> = {
  'Mod-b': 'format-inline',
  'Mod-i': 'format-inline',
  'Mod-e': 'format-inline',
  'Mod-Alt-x': 'format-inline',
  'Mod-Alt-0': 'format-block',
  'Mod-Alt-1': 'format-block',
  'Mod-Alt-2': 'format-block',
  'Mod-Alt-3': 'format-block',
  'Mod-Alt-4': 'format-block',
  'Mod-Alt-5': 'format-block',
  'Mod-Alt-6': 'format-block',
  'Mod-Alt-7': 'format-block',
  'Mod-Alt-8': 'format-block',
  'Mod-Alt-c': 'format-block',
  'Mod-Shift-b': 'format-block',
};

function inTable(state: EditorState): boolean {
  return containerClass(state.selection.$from) === 'table';
}

function inList(state: EditorState): boolean {
  return containerClass(state.selection.$from) === 'list';
}

/** The text position at the start of the next (dir 1) or previous (dir -1) table cell, or null. */
function cellStart(state: EditorState, dir: 1 | -1): number | null {
  const $from = state.selection.$from;
  let d = $from.depth;
  while (d > 0 && !['table_cell', 'table_header'].includes($from.node(d).type.name)) d--;
  if (d === 0) return null;
  const edge = dir > 0 ? $from.after(d) : $from.before(d);
  const found = Selection.findFrom(state.doc.resolve(edge), dir, true);
  if (!found || containerClass(found.$from) !== 'table') return null;
  if (dir > 0) return found.from;
  // Moving back lands at the end of the previous cell: go to its start.
  return found.$from.start();
}

/** The review editor's keymap; refuse is told the hint of a refused shortcut. */
export function reviewKeymap(refuse: (hint: HintKind) => void): Record<string, Command> {
  const refuseAs =
    (cls: EditClass): Command =>
    () => {
      const rule = POLICY[cls];
      if (rule.action === 'refuse') refuse(rule.hint);
      return true;
    };
  const keys: Record<string, Command> = {};
  for (const [key, cls] of Object.entries(REFUSED_SHORTCUTS)) keys[key] = refuseAs(cls);
  const tab =
    (dir: 1 | -1): Command =>
    (state, dispatch) => {
      if (inList(state)) return refuseAs('list')(state);
      if (!inTable(state)) return false;
      const at = cellStart(state, dir);
      if (at !== null) dispatch?.(state.tr.setSelection(TextSelection.create(state.doc, at)));
      return true;
    };
  keys.Tab = tab(1);
  keys['Mod-]'] = tab(1);
  keys['Shift-Tab'] = tab(-1);
  keys['Mod-['] = tab(-1);
  // Dispatches a structural edit built in its tracked form (the policy
  // still checks it), or shows the builder's refusal.
  const finish = (
    tr: Transaction,
    r: Built,
    meta: StructuralMeta,
    dispatch: ((tr: Transaction) => void) | undefined
  ): boolean => {
    if (isRefusal(r)) {
      refuse(r.refuse);
      return true;
    }
    if (!('head' in r)) return true;
    tr.setSelection(TextSelection.create(tr.doc, r.head));
    tr.setMeta(STRUCT_META, { ...meta, from: r.from ?? meta.from, to: r.to ?? meta.to });
    dispatch?.(tr.scrollIntoView());
    return true;
  };
  // Enter suggests a paragraph break (a new list item in a list).
  keys.Enter = (state, dispatch): boolean => {
    if (inTable(state)) return refuseAs('table')(state);
    const sel = state.selection;
    if (!(sel instanceof TextSelection) || !sel.$from.sameParent(sel.$to)) {
      refuse('break');
      return true;
    }
    const tr = state.tr;
    return finish(
      tr,
      buildSplit(tr, sel.from, sel.to),
      { cls: 'split', from: sel.from, to: sel.to },
      dispatch
    );
  };
  keys['Mod-Enter'] = (state): boolean => (inTable(state) ? refuseAs('table')(state) : false);
  // Backspace at the start of a block, or Delete at the end of one,
  // suggests joining it with its neighbour; with a selection across
  // blocks, both suggest deleting it. Elsewhere the base keymap deletes a
  // character, which the tracker records.
  const join =
    (dir: -1 | 1): Command =>
    (state, dispatch) => {
      const sel = state.selection;
      if (!(sel instanceof TextSelection)) return false;
      const tr = state.tr;
      if (!sel.empty) {
        if (sel.$from.sameParent(sel.$to)) return false;
        const meta: StructuralMeta = { cls: 'cross-block', from: sel.from, to: sel.to };
        return finish(tr, buildDelete(tr, sel.from, sel.to, null), meta, dispatch);
      }
      const $p = sel.$from;
      if (!$p.parent.isTextblock) return false;
      if ($p.parentOffset !== (dir < 0 ? 0 : $p.parent.content.size)) return false;
      const r = buildJoin(tr, $p, dir);
      if (r === null) return false;
      return finish(tr, r, { cls: 'join', from: $p.pos, to: $p.pos }, dispatch);
    };
  keys.Backspace = join(-1);
  keys.Delete = join(1);
  // A line break in a paragraph is an inserted hard break (the tracker
  // records it); a heading or a table cell cannot hold one.
  keys['Shift-Enter'] = (state, dispatch): boolean => {
    if (inTable(state)) return refuseAs('table')(state);
    const { $from, $to } = state.selection;
    if (!$from.sameParent($to) || !$from.parent.isTextblock) {
      refuse('break');
      return true;
    }
    if ($from.parent.type.spec.code) return refuseAs('code')(state);
    const hb = state.schema.nodes.hardbreak;
    if ($from.parent.type.name !== 'paragraph' || !hb) {
      refuse('break');
      return true;
    }
    dispatch?.(state.tr.replaceSelectionWith(hb.create(), false).scrollIntoView());
    return true;
  };
  return keys;
}
