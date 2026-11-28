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
 * Ordered `where` match-rules list editor for Triggers (Stage 2 §9.1,
 * mixed-rule-arrays §2–§4).
 *
 * Thin Triggers consumer of the shared `scion-step-list-editor` base
 * (`web/src/components/shared/step-list-editor.ts`): supplies the CEL
 * `sl-textarea` body renderer and non-admitted dialect readonly notice while
 * keeping the existing Triggers public API unchanged.
 *
 * Exported so source form-authoring panes can render compiled rules read-only.
 */

import { LitElement, html, css, nothing } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import {
  StepListEditorController,
  renderStepListEditor,
  stepListEditorStyles,
  type StepBodyRenderContext,
  type StepDialectAdmission,
  type StepDialectRenderers,
  type StepListAttrNames,
} from '../shared/step-list-editor.js';
import {
  ADMITTED_TRIGGER_DIALECTS,
  type TriggerDialectOption,
  type TriggerMatchRuleSpec,
  type TriggerRuleRowError,
} from './types.js';

const WHERE_LIST_ATTRS: StepListAttrNames = {
  addButton: 'data-add-where-rule',
  empty: 'data-where-empty',
  row: 'data-where-row',
  nonAdmitted: 'data-where-non-admitted',
  dialect: 'data-where-dialect',
  dialectReadonly: 'data-where-dialect-readonly',
  moveUp: 'data-where-up',
  moveDown: 'data-where-down',
  remove: 'data-where-remove',
  error: 'data-where-error',
};

const WHERE_LIST_COPY = {
  heading: 'Match rules (where)',
  hint: 'Evaluated in order (AND with short-circuit). Leave empty for a Types-only rule.',
  emptyText: 'No match rules — matches all selected event types (Types-only rule).',
  addLabel: 'Add rule',
  moveUpLabel: 'Move rule up',
  moveDownLabel: 'Move rule down',
  removeLabel: 'Remove rule',
};

function renderWhereRuleBody(ctx: StepBodyRenderContext<TriggerMatchRuleSpec>) {
  const { step, index, readonly, nonAdmitted, onChange } = ctx;
  return html`
    <sl-textarea
      class="expr-input"
      size="small"
      rows="2"
      resize="auto"
      placeholder="event.attributes.env == 'prod'"
      help-text=${nonAdmitted
        ? `This rule uses ${step.dialect}; edit it with the CLI or API.`
        : nothing}
      ?readonly=${readonly}
      .value=${step.expr}
      data-where-expr=${index}
      @sl-input=${(e: Event) => {
        if (readonly) return;
        const ta = e.target as HTMLTextAreaElement;
        onChange({ ...step, expr: ta.value });
      }}
    ></sl-textarea>
  `;
}

const WHERE_RENDERERS: StepDialectRenderers<TriggerMatchRuleSpec> = {
  cel: renderWhereRuleBody,
  '*': renderWhereRuleBody,
};

@customElement('scion-trigger-where-editor')
export class ScionTriggerWhereEditor extends LitElement {
  @property({ type: Array })
  rules: TriggerMatchRuleSpec[] = [];

  @property({ type: Array })
  admittedDialects: readonly TriggerDialectOption[] = ADMITTED_TRIGGER_DIALECTS;

  /** Per-row (0-based) compile errors parsed from 400 invalid_rule responses. */
  @property({ type: Object })
  rowErrors: Record<number, TriggerRuleRowError> = {};

  @property({ type: Boolean })
  readonly = false;

  private readonly ctrl = new StepListEditorController<TriggerMatchRuleSpec>(this);

  static override styles = [
    stepListEditorStyles,
    css`
      .expr-input {
        width: 100%;
      }

      .expr-input::part(textarea) {
        font-family: var(--scion-font-mono);
        font-size: 0.8125rem;
      }
    `,
  ];

  private emitRules(next: TriggerMatchRuleSpec[]): void {
    this.rules = next;
    this.dispatchEvent(
      new CustomEvent<{ rules: TriggerMatchRuleSpec[] }>('rules-changed', {
        detail: { rules: next },
        bubbles: true,
        composed: true,
      })
    );
  }

  private toStepAdmissions(): StepDialectAdmission[] {
    return this.admittedDialects.map((d) => ({
      dialect: d.value,
      admitted: true,
      label: d.label,
    }));
  }

  override render() {
    return renderStepListEditor<TriggerMatchRuleSpec>({
      controller: this.ctrl,
      steps: this.rules,
      admittedDialects: this.toStepAdmissions(),
      rowErrors: this.rowErrors,
      readonly: this.readonly,
      renderers: WHERE_RENDERERS,
      createStep: (dialect) => ({ dialect, expr: '' }),
      copy: WHERE_LIST_COPY,
      attrs: WHERE_LIST_ATTRS,
      onStepsChanged: (next) => this.emitRules(next),
      onStepSelect: (index) => {
        this.dispatchEvent(
          new CustomEvent<{ index: number }>('step-select', {
            detail: { index },
            bubbles: true,
            composed: true,
          })
        );
      },
    });
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'scion-trigger-where-editor': ScionTriggerWhereEditor;
  }
}
