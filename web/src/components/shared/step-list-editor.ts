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
 * Shared consumer-neutral ordered step-list editor (`scion-step-list-editor`).
 *
 * Implements UI-SH rev 2 Component A:
 * - Opaque `Step = { dialect, ...opaque }`: reads only `dialect` (plus optional
 *   `readonly` / `role`) and never renames, maps, or adds fields.
 * - Untouched rows pass through by reference on every edit, add, remove, or move.
 * - Stable DOM identity across reorders via an internal WeakMap (never written back).
 * - Non-admitted rows can be moved or removed, never edited; new rows use only
 *   admitted dialects.
 * - Index-keyed `rowErrors` are dropped on any structural change (add/remove/move)
 *   so an error is never displayed against a shifted row.
 * - Per-row named slots (`row-badge-{i}`, `row-detail-{i}`, `row-help-{i}`).
 * - Data in, events out (`steps-changed`, `step-select`); no fetches; no consumer imports.
 */

import {
  LitElement,
  html,
  css,
  nothing,
  type ReactiveController,
  type ReactiveControllerHost,
  type TemplateResult,
} from 'lit';
import { customElement, property } from 'lit/decorators.js';
import { Directive, directive, type ElementPart } from 'lit/directive.js';
import { repeat } from 'lit/directives/repeat.js';

/** Minimal step shape read by the shared base. All other fields are opaque and preserved. */
export interface StepListStep {
  dialect: string;
  readonly?: boolean;
  role?: string;
}

/** Dialect admission entry supplied by the consumer. */
export interface StepDialectAdmission {
  dialect: string;
  admitted: boolean;
  reason?: string;
  label?: string;
}

/** Per-row (0-based) validation / compile error. */
export interface StepRowError {
  line?: number;
  column?: number;
  category?: string;
  message: string;
}

/** Consumer-supplied vocabulary for rendering a step's optional `role` badge. */
export interface StepRoleBadge {
  label: string;
  variant?: 'primary' | 'success' | 'neutral' | 'warning' | 'danger';
}

/** Context passed to a consumer's per-dialect body renderer. */
export interface StepBodyRenderContext<T extends StepListStep = StepListStep> {
  step: T;
  index: number;
  readonly: boolean;
  nonAdmitted: boolean;
  admission?: StepDialectAdmission | undefined;
  onChange: (replacement: T) => void;
}

/** Per-dialect body renderer returning a Lit template for a step's body. */
export type StepBodyRenderer<T extends StepListStep = StepListStep> = (
  ctx: StepBodyRenderContext<T>
) => TemplateResult | typeof nothing;

/**
 * Map of dialect name -> body renderer.
 * Consumers may also supply `'*'` as a fallback renderer for dialects without a
 * dedicated renderer (e.g. non-admitted dialects rendered read-only).
 */
export type StepDialectRenderers<T extends StepListStep = StepListStep> = Record<
  string,
  StepBodyRenderer<T>
>;

/** Optional data-attribute names so thin wrappers can preserve legacy test selectors. */
export interface StepListAttrNames {
  addButton: string;
  empty: string;
  row: string;
  nonAdmitted: string;
  dialect: string;
  dialectReadonly: string;
  moveUp: string;
  moveDown: string;
  remove: string;
  error: string;
}

export const DEFAULT_STEP_LIST_ATTRS: StepListAttrNames = {
  addButton: 'data-add-step',
  empty: 'data-step-empty',
  row: 'data-step-row',
  nonAdmitted: 'data-step-non-admitted',
  dialect: 'data-step-dialect',
  dialectReadonly: 'data-step-dialect-readonly',
  moveUp: 'data-step-up',
  moveDown: 'data-step-down',
  remove: 'data-step-remove',
  error: 'data-step-error',
};

/** Optional presentation copy for the list shell. */
export interface StepListCopy {
  heading: string;
  hint: string;
  emptyText: string;
  addLabel: string;
  moveUpLabel: string;
  moveDownLabel: string;
  removeLabel: string;
}

export const DEFAULT_STEP_LIST_COPY: StepListCopy = {
  heading: 'Steps',
  hint: '',
  emptyText: 'No steps configured.',
  addLabel: 'Add step',
  moveUpLabel: 'Move step up',
  moveDownLabel: 'Move step down',
  removeLabel: 'Remove step',
};

class DataAttrsDirective extends Directive {
  private prev = new Set<string>();

  render(_attrs: Record<string, string | boolean | undefined>) {
    return nothing;
  }

  override update(part: ElementPart, [attrs]: [Record<string, string | boolean | undefined>]) {
    const el = part.element;
    const next = new Set<string>();
    for (const [name, val] of Object.entries(attrs)) {
      if (val === undefined || val === false) continue;
      next.add(name);
      el.setAttribute(name, val === true ? '' : val);
    }
    for (const old of this.prev) {
      if (!next.has(old)) {
        el.removeAttribute(old);
      }
    }
    this.prev = next;
    return nothing;
  }
}

const dataAttrs = directive(DataAttrsDirective);

export const stepListEditorStyles = css`
  :host {
    display: block;
  }

  .step-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 0.5rem;
    gap: 0.5rem;
  }

  .step-label {
    font-size: 0.875rem;
    font-weight: 600;
    color: var(--scion-text);
  }

  .step-hint {
    font-size: 0.75rem;
    color: var(--scion-text-muted);
    margin-bottom: 0.5rem;
  }

  .empty-note {
    font-size: 0.8125rem;
    color: var(--scion-text-muted);
    padding: 0.75rem;
    background: var(--scion-bg-subtle);
    border: 1px dashed var(--scion-border);
    border-radius: var(--scion-radius);
  }

  .step-list {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .step-row {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.75rem;
    background: var(--scion-bg-subtle);
    border: 1px solid var(--scion-border);
    border-radius: var(--scion-radius);
  }

  .step-row.has-error {
    border-color: var(--sl-color-danger-500);
  }

  .step-row-top {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  .order-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 1.5rem;
    height: 1.5rem;
    padding: 0 0.375rem;
    font-size: 0.75rem;
    font-weight: 600;
    font-family: var(--scion-font-mono);
    color: var(--scion-text-muted);
    background: var(--scion-surface);
    border: 1px solid var(--scion-border);
    border-radius: var(--scion-radius-sm);
  }

  .role-badge {
    display: inline-flex;
    align-items: center;
    padding: 0.125rem 0.5rem;
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--scion-text);
    background: var(--scion-surface);
    border: 1px solid var(--scion-border);
    border-radius: var(--scion-radius-sm);
    flex-shrink: 0;
  }

  .dialect-select {
    width: 9rem;
    flex-shrink: 0;
  }

  .dialect-readonly {
    display: inline-flex;
    align-items: center;
    padding: 0.25rem 0.5rem;
    font-size: 0.8125rem;
    font-weight: 600;
    font-family: var(--scion-font-mono);
    color: var(--scion-text);
    background: var(--scion-surface);
    border: 1px solid var(--scion-border);
    border-radius: var(--scion-radius-sm);
    flex-shrink: 0;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  .row-actions {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    margin-left: auto;
  }

  .fallback-readonly {
    font-size: 0.8125rem;
    color: var(--scion-text-muted);
    padding: 0.5rem;
    background: var(--scion-surface);
    border: 1px solid var(--scion-border);
    border-radius: var(--scion-radius-sm);
  }

  .row-error {
    display: flex;
    align-items: flex-start;
    gap: 0.375rem;
    font-size: 0.75rem;
    color: var(--sl-color-danger-700);
    background: var(--sl-color-danger-50);
    border: 1px solid var(--sl-color-danger-200);
    border-radius: var(--scion-radius-sm);
    padding: 0.375rem 0.5rem;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .row-error-pos {
    font-family: var(--scion-font-mono);
    font-weight: 600;
    flex-shrink: 0;
  }
`;

/**
 * Shared state/invariant controller for ordered step-list editors.
 *
 * Maintains stable WeakMap-backed DOM keys without mutating step objects,
 * drops displayed row errors on any structural change (add/remove/move),
 * and enforces reference-identity and admission invariants.
 */
export class StepListEditorController<
  T extends StepListStep = StepListStep,
> implements ReactiveController {
  private host: ReactiveControllerHost;
  private keyMap = new WeakMap<object, number>();
  private nextKeyId = 1;
  private errorsInvalidatedFor: Record<number, StepRowError> | null = null;

  constructor(host: ReactiveControllerHost) {
    this.host = host;
    host.addController(this);
  }

  hostConnected(): void {
    // No external subscriptions or fetches.
  }

  /** Returns a stable internal key for `step` without modifying `step`. */
  getStepKey(step: T): number {
    let key = this.keyMap.get(step);
    if (key === undefined) {
      key = this.nextKeyId++;
      this.keyMap.set(step, key);
    }
    return key;
  }

  /** Transfers an internal key from `prev` to `next` on in-place edit without mutating either object. */
  private transferKey(prev: T, next: T): void {
    if (prev === next) return;
    const prevKey = this.keyMap.get(prev);
    if (prevKey !== undefined && !this.keyMap.has(next)) {
      this.keyMap.set(next, prevKey);
    }
  }

  /** Returns the admission entry for `dialect`, if present. */
  getAdmission(
    dialect: string,
    admittedDialects: readonly StepDialectAdmission[]
  ): StepDialectAdmission | undefined {
    return admittedDialects.find((d) => d.dialect === dialect);
  }

  /** Returns all entries with `admitted === true`. */
  getAdmittedOptions(admittedDialects: readonly StepDialectAdmission[]): StepDialectAdmission[] {
    return admittedDialects.filter((d) => d.admitted);
  }

  /** Returns true iff `dialect` is admitted. */
  isAdmittedDialect(dialect: string, admittedDialects: readonly StepDialectAdmission[]): boolean {
    return admittedDialects.some((d) => d.dialect === dialect && d.admitted);
  }

  /**
   * Returns true iff `step` has a non-empty dialect that is not admitted.
   */
  isNonAdmittedStep(
    step: T | undefined,
    admittedDialects: readonly StepDialectAdmission[]
  ): boolean {
    if (!step?.dialect) return false;
    return !this.isAdmittedDialect(step.dialect, admittedDialects);
  }

  /**
   * Returns the errors to display. If a structural change (add/remove/move)
   * occurred since the consumer last supplied `rowErrors`, returns `{}` so
   * stale index-keyed errors are never displayed against shifted rows.
   */
  getEffectiveErrors(rowErrors: Record<number, StepRowError>): Record<number, StepRowError> {
    if (this.errorsInvalidatedFor !== null && this.errorsInvalidatedFor === rowErrors) {
      return {};
    }
    return rowErrors;
  }

  /** Marks the current `rowErrors` reference invalid after a structural change. */
  invalidateErrors(rowErrors: Record<number, StepRowError>): void {
    this.errorsInvalidatedFor = rowErrors;
  }

  addStep(
    steps: readonly T[],
    admittedDialects: readonly StepDialectAdmission[],
    rowErrors: Record<number, StepRowError>,
    opts: {
      readonly: boolean;
      maxSteps?: number | undefined;
      createStep?: ((dialect: string) => T) | undefined;
    }
  ): T[] | null {
    if (opts.readonly) return null;
    if (opts.maxSteps !== undefined && steps.length >= opts.maxSteps) return null;
    const admitted = this.getAdmittedOptions(admittedDialects);
    const defaultDialect = admitted[0]?.dialect;
    if (!defaultDialect) return null;
    const created = opts.createStep
      ? opts.createStep(defaultDialect)
      : ({ dialect: defaultDialect } as T);
    this.invalidateErrors(rowErrors);
    this.host.requestUpdate();
    return [...steps, created];
  }

  removeStep(
    steps: readonly T[],
    index: number,
    rowErrors: Record<number, StepRowError>,
    readonly: boolean
  ): T[] | null {
    if (readonly) return null;
    if (index < 0 || index >= steps.length) return null;
    this.invalidateErrors(rowErrors);
    this.host.requestUpdate();
    return steps.filter((_, i) => i !== index);
  }

  moveStep(
    steps: readonly T[],
    index: number,
    delta: -1 | 1,
    rowErrors: Record<number, StepRowError>,
    readonly: boolean
  ): T[] | null {
    if (readonly) return null;
    const target = index + delta;
    if (index < 0 || index >= steps.length || target < 0 || target >= steps.length) return null;
    const next = [...steps];
    const [item] = next.splice(index, 1);
    next.splice(target, 0, item);
    this.invalidateErrors(rowErrors);
    this.host.requestUpdate();
    return next;
  }

  updateDialect(
    steps: readonly T[],
    index: number,
    dialect: string,
    admittedDialects: readonly StepDialectAdmission[],
    readonly: boolean
  ): T[] | null {
    if (readonly) return null;
    const current = steps[index];
    if (!current || current.readonly || this.isNonAdmittedStep(current, admittedDialects)) {
      return null;
    }
    if (!this.isAdmittedDialect(dialect, admittedDialects)) {
      return null;
    }
    const replacement = { ...current, dialect } as T;
    this.transferKey(current, replacement);
    return steps.map((s, i) => (i === index ? replacement : s));
  }

  replaceStep(
    steps: readonly T[],
    index: number,
    replacement: T,
    admittedDialects: readonly StepDialectAdmission[],
    readonly: boolean
  ): T[] | null {
    if (readonly) return null;
    const current = steps[index];
    if (!current || current.readonly || this.isNonAdmittedStep(current, admittedDialects)) {
      return null;
    }
    this.transferKey(current, replacement);
    return steps.map((s, i) => (i === index ? replacement : s));
  }
}

export interface RenderStepListOptions<T extends StepListStep = StepListStep> {
  controller: StepListEditorController<T>;
  steps: readonly T[];
  admittedDialects: readonly StepDialectAdmission[];
  rowErrors: Record<number, StepRowError>;
  readonly: boolean;
  maxSteps?: number | undefined;
  roles?: Record<string, StepRoleBadge> | undefined;
  renderers: StepDialectRenderers<T>;
  createStep?: ((dialect: string) => T) | undefined;
  copy?: Partial<StepListCopy> | undefined;
  attrs?: StepListAttrNames | undefined;
  onStepsChanged: (next: T[]) => void;
  onStepSelect: (index: number) => void;
}

/**
 * Renders the shared ordered step-list editor template.
 * Used directly by `<scion-step-list-editor>` and by thin consumer wrappers.
 */
export function renderStepListEditor<T extends StepListStep>(
  options: RenderStepListOptions<T>
): TemplateResult {
  const {
    controller,
    steps,
    admittedDialects,
    rowErrors,
    readonly,
    maxSteps,
    roles,
    renderers,
    createStep,
    onStepsChanged,
    onStepSelect,
  } = options;
  const copy: StepListCopy = { ...DEFAULT_STEP_LIST_COPY, ...options.copy };
  const attrs: StepListAttrNames = options.attrs ?? DEFAULT_STEP_LIST_ATTRS;

  const admittedOptions = controller.getAdmittedOptions(admittedDialects);
  const effectiveErrors = controller.getEffectiveErrors(rowErrors);
  const atMaxSteps = maxSteps !== undefined && steps.length >= maxSteps;
  const canAdd = !readonly && !atMaxSteps && admittedOptions.length > 0;

  const seenKeys = new Set<number>();
  const keyFn = (step: T, index: number): string => {
    const base = controller.getStepKey(step);
    if (seenKeys.has(base)) {
      return `${base}-${index}`;
    }
    seenKeys.add(base);
    return String(base);
  };

  return html`
    <div class="step-header">
      ${copy.heading ? html`<span class="step-label">${copy.heading}</span>` : nothing}
      ${readonly
        ? nothing
        : html`
            <sl-button
              size="small"
              variant="default"
              ?disabled=${!canAdd}
              ${dataAttrs({ [attrs.addButton]: true })}
              @click=${() => {
                const next = controller.addStep(steps, admittedDialects, rowErrors, {
                  readonly,
                  maxSteps,
                  createStep,
                });
                if (next) onStepsChanged(next);
              }}
            >
              <sl-icon slot="prefix" name="plus-lg"></sl-icon>
              ${copy.addLabel}
            </sl-button>
          `}
    </div>
    ${copy.hint ? html`<div class="step-hint">${copy.hint}</div>` : nothing}
    ${steps.length === 0
      ? html`
          <div class="empty-note" ${dataAttrs({ [attrs.empty]: true })}>${copy.emptyText}</div>
        `
      : html`
          <div class="step-list">
            ${repeat(steps, keyFn, (step, index) => {
              const err = effectiveErrors[index];
              const nonAdmitted = controller.isNonAdmittedStep(step, admittedDialects);
              const admission = step.dialect
                ? controller.getAdmission(step.dialect, admittedDialects)
                : undefined;
              const rowReadonly = readonly || nonAdmitted || Boolean(step.readonly);
              const roleBadge = step.role ? roles?.[step.role] : undefined;
              const renderer = renderers[step.dialect] ?? renderers['*'];

              return html`
                <div
                  class="step-row ${err ? 'has-error' : ''}"
                  ${dataAttrs({
                    [attrs.row]: String(index),
                    [attrs.nonAdmitted]: nonAdmitted,
                  })}
                  @click=${(e: Event) => {
                    const target = e.target as HTMLElement | null;
                    if (target?.closest('.row-actions')) return;
                    onStepSelect(index);
                  }}
                >
                  <div class="step-row-top">
                    <span class="order-badge" title="Evaluation order">${index + 1}</span>
                    ${step.role
                      ? html`
                          <span
                            class="role-badge"
                            data-step-role=${step.role}
                            data-role-variant=${roleBadge?.variant ?? 'neutral'}
                            >${roleBadge?.label ?? step.role}</span
                          >
                        `
                      : nothing}
                    <slot name="row-badge-${index}"></slot>
                    ${nonAdmitted || step.readonly
                      ? html`
                          <span
                            class="dialect-readonly"
                            ${dataAttrs({
                              [attrs.dialect]: String(index),
                              [attrs.dialectReadonly]: true,
                            })}
                            ><span class="sr-only">Dialect: </span>${step.dialect}</span
                          >
                        `
                      : html`
                          <sl-select
                            class="dialect-select"
                            size="small"
                            label="Dialect"
                            hoist
                            ?disabled=${readonly}
                            .value=${step.dialect || admittedOptions[0]?.dialect || ''}
                            ${dataAttrs({ [attrs.dialect]: String(index) })}
                            @sl-change=${(e: Event) => {
                              const sel = e.target as HTMLSelectElement;
                              const next = controller.updateDialect(
                                steps,
                                index,
                                sel.value,
                                admittedDialects,
                                readonly
                              );
                              if (next) onStepsChanged(next);
                            }}
                          >
                            ${admittedOptions.map(
                              (d) =>
                                html`<sl-option value=${d.dialect}
                                  >${d.label ?? d.dialect}</sl-option
                                >`
                            )}
                          </sl-select>
                        `}
                    ${readonly
                      ? nothing
                      : html`
                          <div class="row-actions">
                            <sl-icon-button
                              name="arrow-up"
                              label=${copy.moveUpLabel}
                              ?disabled=${index === 0}
                              ${dataAttrs({ [attrs.moveUp]: String(index) })}
                              @click=${() => {
                                const next = controller.moveStep(
                                  steps,
                                  index,
                                  -1,
                                  rowErrors,
                                  readonly
                                );
                                if (next) onStepsChanged(next);
                              }}
                            ></sl-icon-button>
                            <sl-icon-button
                              name="arrow-down"
                              label=${copy.moveDownLabel}
                              ?disabled=${index === steps.length - 1}
                              ${dataAttrs({ [attrs.moveDown]: String(index) })}
                              @click=${() => {
                                const next = controller.moveStep(
                                  steps,
                                  index,
                                  1,
                                  rowErrors,
                                  readonly
                                );
                                if (next) onStepsChanged(next);
                              }}
                            ></sl-icon-button>
                            <sl-icon-button
                              name="trash"
                              label=${copy.removeLabel}
                              ${dataAttrs({ [attrs.remove]: String(index) })}
                              @click=${() => {
                                const next = controller.removeStep(
                                  steps,
                                  index,
                                  rowErrors,
                                  readonly
                                );
                                if (next) onStepsChanged(next);
                              }}
                            ></sl-icon-button>
                          </div>
                        `}
                  </div>
                  ${renderer
                    ? renderer({
                        step,
                        index,
                        readonly: rowReadonly,
                        nonAdmitted,
                        admission,
                        onChange: (replacement) => {
                          const next = controller.replaceStep(
                            steps,
                            index,
                            replacement,
                            admittedDialects,
                            readonly
                          );
                          if (next) onStepsChanged(next);
                        },
                      })
                    : html`
                        <div class="fallback-readonly" data-step-fallback=${index}>
                          Read-only step
                          (${step.dialect || 'unknown'})${admission?.reason
                            ? `: ${admission.reason}`
                            : ''}
                        </div>
                      `}
                  <slot name="row-detail-${index}"></slot>
                  ${err
                    ? html`
                        <div
                          class="row-error"
                          role="alert"
                          ${dataAttrs({ [attrs.error]: String(index) })}
                        >
                          ${err.line !== undefined
                            ? html`<span class="row-error-pos"
                                >${err.line}${err.column !== undefined
                                  ? `:${err.column}`
                                  : ''}</span
                              >`
                            : nothing}
                          <span>${err.category ? `[${err.category}] ` : ''}${err.message}</span>
                        </div>
                      `
                    : nothing}
                  <slot name="row-help-${index}"></slot>
                </div>
              `;
            })}
          </div>
        `}
  `;
}

@customElement('scion-step-list-editor')
export class ScionStepListEditor extends LitElement {
  @property({ type: Array })
  steps: StepListStep[] = [];

  @property({ type: Array })
  admittedDialects: readonly StepDialectAdmission[] = [];

  @property({ type: Object })
  rowErrors: Record<number, StepRowError> = {};

  @property({ type: Boolean })
  readonly = false;

  @property({ type: Number })
  maxSteps?: number;

  @property({ type: Object })
  roles?: Record<string, StepRoleBadge>;

  @property({ attribute: false })
  renderers: StepDialectRenderers = {};

  @property({ attribute: false })
  createStep?: (dialect: string) => StepListStep;

  @property({ type: String })
  heading = DEFAULT_STEP_LIST_COPY.heading;

  @property({ type: String })
  hint = DEFAULT_STEP_LIST_COPY.hint;

  @property({ type: String })
  emptyText = DEFAULT_STEP_LIST_COPY.emptyText;

  @property({ type: String })
  addLabel = DEFAULT_STEP_LIST_COPY.addLabel;

  private readonly ctrl = new StepListEditorController(this);

  static override styles = stepListEditorStyles;

  private emitSteps(next: StepListStep[]): void {
    this.steps = next;
    this.dispatchEvent(
      new CustomEvent<{ steps: StepListStep[] }>('steps-changed', {
        detail: { steps: next },
        bubbles: true,
        composed: true,
      })
    );
  }

  private emitSelect(index: number): void {
    this.dispatchEvent(
      new CustomEvent<{ index: number }>('step-select', {
        detail: { index },
        bubbles: true,
        composed: true,
      })
    );
  }

  override render() {
    return renderStepListEditor({
      controller: this.ctrl,
      steps: this.steps,
      admittedDialects: this.admittedDialects,
      rowErrors: this.rowErrors,
      readonly: this.readonly,
      maxSteps: this.maxSteps,
      roles: this.roles,
      renderers: this.renderers,
      createStep: this.createStep,
      copy: {
        heading: this.heading,
        hint: this.hint,
        emptyText: this.emptyText,
        addLabel: this.addLabel,
      },
      onStepsChanged: (next) => this.emitSteps(next),
      onStepSelect: (index) => this.emitSelect(index),
    });
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-step-list-editor': ScionStepListEditor;
  }
}
