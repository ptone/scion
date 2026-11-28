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

import { describe, it, expect, afterEach, vi } from 'vitest';
import { html } from 'lit';
import './step-list-editor.js';
import type {
  ScionStepListEditor,
  StepDialectAdmission,
  StepDialectRenderers,
  StepListStep,
} from './step-list-editor.js';

interface TestExprStep extends StepListStep {
  expr?: string;
}

const ADMITTED_TWO: StepDialectAdmission[] = [
  { dialect: 'cel', admitted: true, label: 'CEL' },
  { dialect: 'glob', admitted: true, label: 'Glob' },
  { dialect: 'system_one', admitted: false, reason: 'Managed by policy engine' },
];

const TEST_RENDERERS: StepDialectRenderers<TestExprStep> = {
  cel: ({ step, index, readonly, onChange }) => html`
    <input
      data-test-body=${index}
      ?readonly=${readonly}
      .value=${step.expr ?? ''}
      @input=${(e: Event) => {
        if (readonly) return;
        const input = e.target as HTMLInputElement;
        onChange({ ...step, expr: input.value });
      }}
    />
  `,
  glob: ({ step, index, readonly, onChange }) => html`
    <input
      data-test-body=${index}
      ?readonly=${readonly}
      .value=${step.expr ?? ''}
      @input=${(e: Event) => {
        if (readonly) return;
        const input = e.target as HTMLInputElement;
        onChange({ ...step, expr: input.value });
      }}
    />
  `,
};

describe('scion-step-list-editor', () => {
  afterEach(() => {
    document.body.innerHTML = '';
    vi.restoreAllMocks();
  });

  it('preserves reference identity and exact JSON of untouched steps across add, edit, move, and remove without injecting keys', async () => {
    const fetchSpy = vi.fn();
    vi.stubGlobal('fetch', fetchSpy);

    const stepA: TestExprStep = Object.freeze({ dialect: 'cel', expr: 'a == 1' });
    const stepB: TestExprStep = Object.freeze({ dialect: 'glob', expr: '*.go' });
    const jsonA = JSON.stringify(stepA);
    const jsonB = JSON.stringify(stepB);

    const el = document.createElement('scion-step-list-editor') as ScionStepListEditor;
    el.steps = [stepA, stepB];
    el.admittedDialects = ADMITTED_TWO;
    el.renderers = TEST_RENDERERS;
    document.body.appendChild(el);
    await el.updateComplete;

    let latest: StepListStep[] = el.steps;
    el.addEventListener('steps-changed', (e: Event) => {
      latest = (e as CustomEvent<{ steps: StepListStep[] }>).detail.steps;
    });

    // 1. Add: untouched rows A and B pass through by reference; new step has no injected id.
    (el.shadowRoot?.querySelector('[data-add-step]') as HTMLElement).click();
    await el.updateComplete;
    expect(latest.length).toBe(3);
    expect(latest[0]).toBe(stepA);
    expect(latest[1]).toBe(stepB);
    expect(JSON.stringify(latest[0])).toBe(jsonA);
    expect(JSON.stringify(latest[1])).toBe(jsonB);
    expect(Object.keys(latest[2])).toEqual(['dialect']);
    expect(latest[2]).toEqual({ dialect: 'cel' });
    expect('id' in latest[2]).toBe(false);

    const stepC = latest[2];
    const jsonC = JSON.stringify(stepC);

    // 2. Edit row 0: rows 1 and 2 pass through by reference with identical JSON; edited row has no injected id.
    const body0 = el.shadowRoot?.querySelector('[data-test-body="0"]') as HTMLInputElement;
    body0.value = 'a == 2';
    body0.dispatchEvent(new Event('input', { bubbles: true }));
    await el.updateComplete;
    expect(latest[0]).not.toBe(stepA);
    expect(latest[0]).toEqual({ dialect: 'cel', expr: 'a == 2' });
    expect('id' in latest[0]).toBe(false);
    expect(latest[1]).toBe(stepB);
    expect(latest[2]).toBe(stepC);
    expect(JSON.stringify(latest[1])).toBe(jsonB);
    expect(JSON.stringify(latest[2])).toBe(jsonC);

    const editedA = latest[0];

    // 3. Move row 2 up to index 1: every step keeps exact reference identity and JSON.
    (el.shadowRoot?.querySelector('[data-step-up="2"]') as HTMLElement).click();
    await el.updateComplete;
    expect(latest[0]).toBe(editedA);
    expect(latest[1]).toBe(stepC);
    expect(latest[2]).toBe(stepB);
    expect(JSON.stringify(latest[1])).toBe(jsonC);
    expect(JSON.stringify(latest[2])).toBe(jsonB);

    // 4. Remove row 0: remaining rows keep exact reference identity and JSON.
    (el.shadowRoot?.querySelector('[data-step-remove="0"]') as HTMLElement).click();
    await el.updateComplete;
    expect(latest.length).toBe(2);
    expect(latest[0]).toBe(stepC);
    expect(latest[1]).toBe(stepB);
    expect(JSON.stringify(latest[0])).toBe(jsonC);
    expect(JSON.stringify(latest[1])).toBe(jsonB);

    // No fetches anywhere inside the component.
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('preserves an opaque System-One-shaped step with deep and reference equality across moves and blocks edits when non-admitted', async () => {
    const systemOneStep = {
      id: 'pol-step-42',
      role: 'gate',
      dialect: 'system_one',
      spec: { threshold: 3, tags: ['prod', 'critical'] },
      reason: 'Requires policy approval',
    };
    const originalJson = JSON.stringify(systemOneStep);

    const celStep: TestExprStep = { dialect: 'cel', expr: 'true' };

    const el = document.createElement('scion-step-list-editor') as ScionStepListEditor;
    el.steps = [celStep, systemOneStep];
    el.admittedDialects = ADMITTED_TWO;
    el.roles = { gate: { label: 'Gate', variant: 'warning' } };
    el.renderers = TEST_RENDERERS;
    document.body.appendChild(el);
    await el.updateComplete;

    let latest: StepListStep[] = el.steps;
    let emitted = 0;
    el.addEventListener('steps-changed', (e: Event) => {
      emitted++;
      latest = (e as CustomEvent<{ steps: StepListStep[] }>).detail.steps;
    });

    // Role badge renders from consumer vocabulary; dialect is read-only text (no sl-select).
    const row1 = el.shadowRoot?.querySelector('[data-step-row="1"]');
    expect(row1?.hasAttribute('data-step-non-admitted')).toBe(true);
    expect(row1?.querySelector('sl-select')).toBeNull();
    expect(row1?.querySelector('[data-step-role="gate"]')?.textContent?.trim()).toBe('Gate');
    expect(row1?.querySelector('[data-step-fallback="1"]')?.textContent).toContain(
      'Managed by policy engine'
    );

    // Admitted select on row 0 lists only admitted dialects (never system_one).
    const options = Array.from(
      el.shadowRoot?.querySelectorAll('[data-step-dialect="0"] sl-option') ?? []
    ).map((o) => o.getAttribute('value'));
    expect(options).toEqual(['cel', 'glob']);

    // Move System One step from index 1 to index 0 -> deep AND reference equality preserved.
    (el.shadowRoot?.querySelector('[data-step-up="1"]') as HTMLElement).click();
    await el.updateComplete;
    expect(emitted).toBe(1);
    expect(latest[0]).toBe(systemOneStep);
    expect(latest[0]).toEqual(systemOneStep);
    expect(JSON.stringify(latest[0])).toBe(originalJson);
    expect(latest[1]).toBe(celStep);
  });

  it('drops displayed rowErrors on structural changes (add, remove, move) and restores when consumer re-supplies them', async () => {
    const el = document.createElement('scion-step-list-editor') as ScionStepListEditor;
    el.steps = [
      { dialect: 'cel', expr: 'ok' } as TestExprStep,
      { dialect: 'cel', expr: 'bad ===' } as TestExprStep,
    ];
    el.admittedDialects = ADMITTED_TWO;
    el.renderers = TEST_RENDERERS;
    el.rowErrors = {
      1: { line: 1, column: 5, category: 'syntax', message: 'unexpected token' },
    };
    document.body.appendChild(el);
    await el.updateComplete;

    expect(el.shadowRoot?.querySelector('[data-step-error="0"]')).toBeNull();
    expect(el.shadowRoot?.querySelector('[data-step-error="1"]')?.textContent).toContain(
      'unexpected token'
    );

    // Move row 1 up -> row 1 is now the former row 0; error must be dropped, not shown on shifted row.
    (el.shadowRoot?.querySelector('[data-step-up="1"]') as HTMLElement).click();
    await el.updateComplete;
    expect(el.shadowRoot?.querySelector('[data-step-error="0"]')).toBeNull();
    expect(el.shadowRoot?.querySelector('[data-step-error="1"]')).toBeNull();

    // Consumer re-supplies errors after next validation -> displayed again.
    el.rowErrors = {
      0: { line: 1, column: 5, category: 'syntax', message: 'revalidated error' },
    };
    await el.updateComplete;
    expect(el.shadowRoot?.querySelector('[data-step-error="0"]')?.textContent).toContain(
      'revalidated error'
    );

    // Remove row 0 -> error is dropped (does not shift onto remaining row 0).
    (el.shadowRoot?.querySelector('[data-step-remove="0"]') as HTMLElement).click();
    await el.updateComplete;
    expect(el.shadowRoot?.querySelector('[data-step-error="0"]')).toBeNull();

    // Re-supply error on row 0, then add a row -> error dropped on structural add as well.
    el.rowErrors = { 0: { message: 'err before add' } };
    await el.updateComplete;
    expect(el.shadowRoot?.querySelector('[data-step-error="0"]')).toBeTruthy();

    (el.shadowRoot?.querySelector('[data-add-step]') as HTMLElement).click();
    await el.updateComplete;
    expect(el.shadowRoot?.querySelector('[data-step-error="0"]')).toBeNull();
  });

  it('renders per-row badge, detail, and help/validation slots for consumers', async () => {
    const el = document.createElement('scion-step-list-editor') as ScionStepListEditor;
    el.steps = [{ dialect: 'cel', expr: 'true' } as TestExprStep];
    el.admittedDialects = ADMITTED_TWO;
    el.renderers = TEST_RENDERERS;

    const badge = document.createElement('span');
    badge.slot = 'row-badge-0';
    badge.textContent = 'Custom Badge';
    el.appendChild(badge);

    const detail = document.createElement('div');
    detail.slot = 'row-detail-0';
    detail.textContent = 'Remediation detail editor';
    el.appendChild(detail);

    const help = document.createElement('div');
    help.slot = 'row-help-0';
    help.textContent = 'Validation hint';
    el.appendChild(help);

    document.body.appendChild(el);
    await el.updateComplete;

    const badgeSlot = el.shadowRoot?.querySelector('slot[name="row-badge-0"]') as HTMLSlotElement;
    const detailSlot = el.shadowRoot?.querySelector('slot[name="row-detail-0"]') as HTMLSlotElement;
    const helpSlot = el.shadowRoot?.querySelector('slot[name="row-help-0"]') as HTMLSlotElement;

    expect(badgeSlot).toBeTruthy();
    expect(detailSlot).toBeTruthy();
    expect(helpSlot).toBeTruthy();

    expect(el.querySelector('[slot="row-badge-0"]')?.textContent).toBe('Custom Badge');
    expect(el.querySelector('[slot="row-detail-0"]')?.textContent).toBe(
      'Remediation detail editor'
    );
    expect(el.querySelector('[slot="row-help-0"]')?.textContent).toBe('Validation hint');
  });

  it('fires step-select with row index on row click and ignores action button clicks', async () => {
    const el = document.createElement('scion-step-list-editor') as ScionStepListEditor;
    el.steps = [
      { dialect: 'cel', expr: 'a' } as TestExprStep,
      { dialect: 'cel', expr: 'b' } as TestExprStep,
    ];
    el.admittedDialects = ADMITTED_TWO;
    el.renderers = TEST_RENDERERS;
    document.body.appendChild(el);
    await el.updateComplete;

    const selected: number[] = [];
    el.addEventListener('step-select', (e: Event) => {
      selected.push((e as CustomEvent<{ index: number }>).detail.index);
    });

    (el.shadowRoot?.querySelector('[data-step-row="1"]') as HTMLElement).click();
    expect(selected).toEqual([1]);

    // Clicking move-up button in .row-actions must not emit step-select.
    (el.shadowRoot?.querySelector('[data-step-up="1"]') as HTMLElement).click();
    await el.updateComplete;
    expect(selected).toEqual([1]);
  });

  it('enforces maxSteps limit and re-enables Add after removal', async () => {
    const el = document.createElement('scion-step-list-editor') as ScionStepListEditor;
    el.steps = [{ dialect: 'cel', expr: 'one' } as TestExprStep];
    el.admittedDialects = ADMITTED_TWO;
    el.renderers = TEST_RENDERERS;
    el.maxSteps = 2;
    document.body.appendChild(el);
    await el.updateComplete;

    const addBtn = el.shadowRoot?.querySelector('[data-add-step]') as HTMLElement;
    expect(addBtn.hasAttribute('disabled')).toBe(false);

    addBtn.click();
    await el.updateComplete;
    expect(el.steps.length).toBe(2);
    expect(addBtn.hasAttribute('disabled')).toBe(true);

    // Extra click while at maxSteps must not add a 3rd step.
    addBtn.click();
    await el.updateComplete;
    expect(el.steps.length).toBe(2);

    // Removing a step re-enables Add.
    (el.shadowRoot?.querySelector('[data-step-remove="1"]') as HTMLElement).click();
    await el.updateComplete;
    expect(el.steps.length).toBe(1);
    expect(addBtn.hasAttribute('disabled')).toBe(false);
  });
});
