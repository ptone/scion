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
 * Sequence Lifeline Tree
 *
 * The ancestry sidebar. It answers "who spawned whom" — which the main diagram
 * deliberately cannot, since columns there are packed by slot recycling rather
 * than by kinship — and it controls which columns render at all.
 *
 * The forest is rebuilt locally from each lifeline's `parentId` / `ancestry`,
 * children ordered by the digest's depth-first `order` so the sidebar and the
 * diagram agree on sequence. Collapsing a row hides its whole subtree and
 * annotates the row with the descendant count, so nothing silently disappears.
 *
 * Purely presentational: `collapsed`, `solo`, `activeIds` and `selectedId` all
 * come in as properties, and every interaction leaves as a `CustomEvent`.
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import type { Lifeline } from '../core/types.js';

/** One flattened, renderable row of the ancestry forest. */
interface Row {
  lifeline: Lifeline;
  /** Nesting level within the rendered forest (not `lifeline.depth`). */
  indent: number;
  hasChildren: boolean;
  /** Total descendants, used for the badge shown while collapsed. */
  descendantCount: number;
}

/** Indentation applied per ancestry level, in rem. */
const INDENT_REM = 0.75;

@customElement('scion-seq-lifeline-tree')
export class ScionSeqLifelineTree extends LitElement {
  /** Every lifeline in the run, in any order. */
  @property({ attribute: false })
  lifelines: Lifeline[] = [];

  /** Ids whose subtrees are hidden. */
  @property({ attribute: false })
  collapsed: ReadonlySet<string> = new Set();

  /** Id of the soloed lifeline, or null when nothing is soloed. */
  @property({ type: String })
  solo: string | null = null;

  /** Ids currently alive/visible in the viewport; others render dimmed. */
  @property({ attribute: false })
  activeIds: ReadonlySet<string> = new Set();

  /** Id of the currently selected lifeline. */
  @property({ attribute: false })
  selectedId: string | null = null;

  static override styles = css`
    :host {
      display: flex;
      flex-direction: column;
      min-height: 0;
      height: 100%;
      font-family: var(--scion-font-sans, sans-serif);
      font-size: var(--scion-font-size-sm, 0.875rem);
      color: var(--scion-text, #0f172a);
      background: var(--scion-surface, #ffffff);
      border-right: 1px solid var(--scion-border, #e2e8f0);
    }

    .header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--scion-space-2, 0.5rem);
      padding: var(--scion-space-2, 0.5rem) var(--scion-space-2, 0.5rem);
      border-bottom: 1px solid var(--scion-border, #e2e8f0);
      font-size: var(--scion-font-size-xs, 0.75rem);
      color: var(--scion-text-muted, #64748b);
      text-transform: uppercase;
      letter-spacing: 0.04em;
      flex: 0 0 auto;
    }

    .clear-solo {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      padding: 1px 6px;
      border: 1px solid var(--scion-primary, #3b82f6);
      border-radius: var(--scion-radius-full, 9999px);
      background: transparent;
      color: var(--scion-primary, #3b82f6);
      font: inherit;
      text-transform: none;
      letter-spacing: normal;
      cursor: pointer;
    }

    .rows {
      flex: 1 1 auto;
      overflow-y: auto;
      overflow-x: hidden;
      min-height: 0;
      padding: 2px 0;
    }

    .row {
      display: flex;
      align-items: center;
      gap: 4px;
      width: 100%;
      box-sizing: border-box;
      /* Compact: this list is expected to run to ~100 rows. */
      height: 1.5rem;
      padding: 0 var(--scion-space-1, 0.25rem) 0 0;
      border: 0;
      border-left: 2px solid transparent;
      background: transparent;
      color: inherit;
      font: inherit;
      text-align: left;
      cursor: pointer;
      transition: background var(--scion-transition-fast, 150ms ease);
    }

    .row:hover {
      background: var(--scion-bg-subtle, #f1f5f9);
    }

    .row.selected {
      background: var(--scion-primary-50, #eff6ff);
      border-left-color: var(--scion-primary, #3b82f6);
    }

    /* Not currently active: still listed, but visibly receded. */
    .row.dimmed .name,
    .row.dimmed .swatch {
      opacity: 0.4;
    }

    .row.soloed .name {
      font-weight: 600;
      color: var(--scion-primary, #3b82f6);
    }

    .chevron,
    .solo-btn {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      flex: 0 0 auto;
      width: 1rem;
      height: 1rem;
      padding: 0;
      border: 0;
      border-radius: var(--scion-radius-sm, 0.25rem);
      background: transparent;
      color: var(--scion-text-muted, #64748b);
      cursor: pointer;
      font-size: var(--scion-font-size-xs, 0.75rem);
      line-height: 1;
    }

    .chevron:hover,
    .solo-btn:hover {
      color: var(--scion-text, #0f172a);
      background: var(--scion-bg-subtle, #f1f5f9);
    }

    .chevron.spacer {
      visibility: hidden;
      cursor: default;
    }

    .solo-btn.active {
      color: var(--scion-primary, #3b82f6);
    }

    .swatch {
      flex: 0 0 auto;
      width: 0.5rem;
      height: 0.5rem;
      border-radius: var(--scion-radius-sm, 0.25rem);
      /* Colour comes from the digest, so it is set inline. */
      background: var(--scion-text-muted, #64748b);
    }

    .name {
      flex: 1 1 auto;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .count {
      flex: 0 0 auto;
      padding: 0 5px;
      border-radius: var(--scion-radius-full, 9999px);
      background: var(--scion-neutral-100, #f1f5f9);
      color: var(--scion-text-muted, #64748b);
      font-family: var(--scion-font-mono, monospace);
      font-size: var(--scion-font-size-xs, 0.75rem);
      line-height: 1.1;
    }

    .empty {
      padding: var(--scion-space-3, 0.75rem);
      color: var(--scion-text-muted, #64748b);
      font-size: var(--scion-font-size-xs, 0.75rem);
    }
  `;

  /**
   * Flattens the ancestry forest into renderable rows, skipping the subtrees of
   * collapsed nodes.
   *
   * A lifeline whose `parentId` is missing from `lifelines` is promoted to a
   * root rather than dropped: a partial digest must still render every actor it
   * does contain.
   */
  private buildRows(): Row[] {
    const byId = new Map<string, Lifeline>();
    for (const l of this.lifelines) byId.set(l.id, l);

    const children = new Map<string, Lifeline[]>();
    const roots: Lifeline[] = [];
    for (const l of this.lifelines) {
      const parentId = l.parentId ?? l.ancestry[l.ancestry.length - 1];
      if (parentId !== undefined && parentId !== l.id && byId.has(parentId)) {
        const bucket = children.get(parentId);
        if (bucket) bucket.push(l);
        else children.set(parentId, [l]);
      } else {
        roots.push(l);
      }
    }

    const byOrder = (a: Lifeline, b: Lifeline): number =>
      a.order - b.order || a.name.localeCompare(b.name);
    roots.sort(byOrder);
    for (const bucket of children.values()) bucket.sort(byOrder);

    const countDescendants = (id: string, seen: Set<string>): number => {
      if (seen.has(id)) return 0;
      seen.add(id);
      let total = 0;
      for (const child of children.get(id) ?? []) {
        total += 1 + countDescendants(child.id, seen);
      }
      return total;
    };

    const rows: Row[] = [];
    const visit = (lifeline: Lifeline, indent: number, path: Set<string>): void => {
      // Guard against a cyclic ancestry chain in a malformed digest.
      if (path.has(lifeline.id)) return;
      const kids = children.get(lifeline.id) ?? [];
      rows.push({
        lifeline,
        indent,
        hasChildren: kids.length > 0,
        descendantCount: countDescendants(lifeline.id, new Set()),
      });
      if (this.collapsed.has(lifeline.id)) return;
      const nextPath = new Set(path);
      nextPath.add(lifeline.id);
      for (const kid of kids) visit(kid, indent + 1, nextPath);
    };
    for (const root of roots) visit(root, 0, new Set());
    return rows;
  }

  private emit<T>(type: string, detail: T): void {
    this.dispatchEvent(
      new CustomEvent<T>(type, { detail, bubbles: true, composed: true })
    );
  }

  private onToggleCollapse(e: Event, lifelineId: string): void {
    e.stopPropagation();
    this.emit<{ lifelineId: string }>('seq-toggle-collapse', { lifelineId });
  }

  private onSolo(e: Event, lifelineId: string): void {
    e.stopPropagation();
    const next = this.solo === lifelineId ? null : lifelineId;
    this.emit<{ lifelineId: string | null }>('seq-solo', { lifelineId: next });
  }

  private onClearSolo(): void {
    this.emit<{ lifelineId: string | null }>('seq-solo', { lifelineId: null });
  }

  private onSelect(lifelineId: string): void {
    this.emit<{ lifelineId: string }>('seq-select-lifeline', { lifelineId });
  }

  private renderRow(row: Row) {
    const { lifeline } = row;
    const collapsed = this.collapsed.has(lifeline.id);
    const soloed = this.solo === lifeline.id;
    const dimmed = !this.activeIds.has(lifeline.id);
    const classes = [
      'row',
      dimmed ? 'dimmed' : '',
      soloed ? 'soloed' : '',
      this.selectedId === lifeline.id ? 'selected' : '',
    ]
      .filter(Boolean)
      .join(' ');

    return html`
      <div
        class=${classes}
        role="treeitem"
        tabindex="0"
        aria-level=${row.indent + 1}
        aria-selected=${this.selectedId === lifeline.id ? 'true' : 'false'}
        aria-expanded=${row.hasChildren ? String(!collapsed) : nothing}
        data-lifeline-id=${lifeline.id}
        title=${lifeline.harness ? `${lifeline.name} (${lifeline.harness})` : lifeline.name}
        style="padding-left: ${(row.indent * INDENT_REM).toFixed(2)}rem"
        @click=${(): void => this.onSelect(lifeline.id)}
        @keydown=${(e: KeyboardEvent): void => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            this.onSelect(lifeline.id);
          }
        }}
      >
        ${row.hasChildren
          ? html`<button
              class="chevron"
              type="button"
              aria-label=${collapsed ? `Expand ${lifeline.name}` : `Collapse ${lifeline.name}`}
              @click=${(e: Event): void => this.onToggleCollapse(e, lifeline.id)}
            >
              <sl-icon name=${collapsed ? 'chevron-right' : 'chevron-down'}></sl-icon>
            </button>`
          : html`<span class="chevron spacer"></span>`}

        <span class="swatch" style="background: ${lifeline.color}"></span>
        <span class="name">${lifeline.name}</span>

        ${collapsed && row.descendantCount > 0
          ? html`<span class="count" title="${row.descendantCount} hidden descendants"
              >+${row.descendantCount}</span
            >`
          : nothing}

        <button
          class="solo-btn ${soloed ? 'active' : ''}"
          type="button"
          aria-pressed=${soloed ? 'true' : 'false'}
          aria-label=${soloed ? `Clear solo on ${lifeline.name}` : `Solo ${lifeline.name}`}
          title=${soloed ? 'Clear solo' : 'Show only this lifeline and its messages'}
          @click=${(e: Event): void => this.onSolo(e, lifeline.id)}
        >
          <sl-icon name=${soloed ? 'eye-fill' : 'eye'}></sl-icon>
        </button>
      </div>
    `;
  }

  override render() {
    const rows = this.buildRows();
    return html`
      <div class="header">
        <span>Lifelines (${this.lifelines.length})</span>
        ${this.solo !== null
          ? html`<button
              class="clear-solo"
              type="button"
              title="Show all lifelines again"
              @click=${(): void => this.onClearSolo()}
            >
              <sl-icon name="x"></sl-icon>clear solo
            </button>`
          : nothing}
      </div>
      <div class="rows" role="tree" aria-label="Lifeline ancestry">
        ${rows.length === 0
          ? html`<div class="empty">No lifelines in this run.</div>`
          : rows.map((row) => this.renderRow(row))}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-seq-lifeline-tree': ScionSeqLifelineTree;
  }
}
