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
 * Tests for <scion-seq-minimap>'s interaction contract.
 *
 * The strip runs the full height of the window along its right edge, so the
 * pointer crosses it constantly on the way somewhere else. Both behaviours
 * asserted here exist to stop it reacting to traffic that was never aimed at
 * it: seeking requires taking hold of the viewport window, and the hover
 * readout requires the pointer to stop.
 */

// @vitest-environment happy-dom

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import './seq-minimap.js';
import type { ScionSeqMinimap } from './seq-minimap.js';

/** Strip height used throughout; happy-dom has no layout, so it is imposed. */
const HEIGHT = 400;
/** Matches PAD_Y in the component. */
const PAD_Y = 6;
const USABLE = HEIGHT - PAD_Y * 2;
const DURATION = 100_000;

/** Wall time of a y offset, mirroring the component's own mapping. */
function wallAt(y: number): number {
  return ((y - PAD_Y) / USABLE) * DURATION;
}

/** y offset of a wall time. */
function yAt(wallMs: number): number {
  return PAD_Y + (wallMs / DURATION) * USABLE;
}

function pointer(type: string, clientY: number): Event {
  const e = new MouseEvent(type, { clientY, bubbles: true, composed: true });
  Object.defineProperty(e, 'pointerId', { value: 1 });
  return e;
}

describe('scion-seq-minimap', () => {
  let el: ScionSeqMinimap;
  let canvas: HTMLCanvasElement;
  let seeks: number[];

  beforeEach(async () => {
    el = document.createElement('scion-seq-minimap') as ScionSeqMinimap;
    el.durationMs = DURATION;
    el.wallMs = 48_000;
    el.viewportStartMs = 40_000;
    el.viewportEndMs = 50_000;
    document.body.appendChild(el);
    // happy-dom lays nothing out, so the strip would be zero-height and every
    // handler would short-circuit.
    el.getBoundingClientRect = (): DOMRect =>
      ({ top: 0, left: 0, width: 40, height: HEIGHT }) as DOMRect;
    await el.updateComplete;

    seeks = [];
    el.addEventListener('seq-seek-wall', (e) => {
      seeks.push((e as CustomEvent<{ wallMs: number }>).detail.wallMs);
    });
    const found = el.shadowRoot?.querySelector('canvas');
    if (!found) throw new Error('minimap has no canvas');
    canvas = found;
  });

  afterEach(() => {
    vi.useRealTimers();
    el.remove();
    document.body.innerHTML = '';
  });

  it('ignores a press away from the viewport window', () => {
    // The old behaviour seeked from anywhere in the strip, which turned a
    // misplaced click into an unrecoverable jump across the run.
    canvas.dispatchEvent(pointer('pointerdown', 40));
    canvas.dispatchEvent(pointer('pointermove', 60));
    expect(seeks).toEqual([]);
  });

  it('seeks while dragging the viewport window', () => {
    const grab = yAt(45_000);
    canvas.dispatchEvent(pointer('pointerdown', grab));
    // Taking hold of the window must not move it; only the drag does.
    expect(seeks).toEqual([]);

    canvas.dispatchEvent(pointer('pointermove', grab + 40));
    expect(seeks).toHaveLength(1);
    expect(seeks[0]).toBeCloseTo(48_000 + (wallAt(grab + 40) - wallAt(grab)), 6);
  });

  it('keeps the grip: the window follows the pointer, it does not centre on it', () => {
    // Grab near the window's top edge and drag; the delta is what matters, so
    // the playhead moves by exactly the distance dragged.
    const grab = yAt(40_500);
    canvas.dispatchEvent(pointer('pointerdown', grab));
    canvas.dispatchEvent(pointer('pointermove', grab + 10));
    const moved = seeks[0] - 48_000;
    expect(moved).toBeCloseTo((10 / USABLE) * DURATION, 6);
  });

  it('releases the drag on pointerup', () => {
    const grab = yAt(45_000);
    canvas.dispatchEvent(pointer('pointerdown', grab));
    canvas.dispatchEvent(pointer('pointerup', grab));
    canvas.dispatchEvent(pointer('pointermove', grab + 50));
    expect(seeks).toEqual([]);
  });

  it('grabs a sliver of a window that is thinner than a pixel', () => {
    // A 49-minute run in a 6-second viewport draws a window under 1px tall.
    // Without a grab margin it would be unusable.
    el.viewportStartMs = 50_000;
    el.viewportEndMs = 50_050;
    canvas.dispatchEvent(pointer('pointerdown', yAt(50_000) + 3));
    canvas.dispatchEvent(pointer('pointermove', yAt(50_000) + 13));
    expect(seeks).toHaveLength(1);
  });

  it('keeps its grip on a window that hangs off the start of the run', () => {
    // At the very beginning the viewport is wider than the run has time for, so
    // the window straddles t=0 and the grab lands on a negative wall time.
    // Clamping that to zero used to swallow part of every drag here.
    el.viewportStartMs = -40_000;
    el.viewportEndMs = 10_000;
    el.wallMs = 0;
    // The drawn window pins its top to the strip, so the grab lands in the
    // padding above the time axis -- a y with no wall time of its own.
    const grab = 2;
    canvas.dispatchEvent(pointer('pointerdown', grab));
    canvas.dispatchEvent(pointer('pointermove', grab + 30));
    expect(seeks[0]).toBeCloseTo((30 / USABLE) * DURATION, 6);
  });

  it('keeps the readout off the viewport window', async () => {
    // The readout is drawn at the pointer, so on the handle it would cover the
    // affordance the reader is about to grab.
    vi.useFakeTimers();
    canvas.dispatchEvent(pointer('pointermove', yAt(45_000)));
    vi.advanceTimersByTime(300);
    await el.updateComplete;
    expect(el.shadowRoot?.querySelector('.readout')).toBeNull();
  });

  it('shows the hover readout only after the pointer settles', async () => {
    vi.useFakeTimers();
    canvas.dispatchEvent(pointer('pointermove', 120));
    await el.updateComplete;
    expect(el.shadowRoot?.querySelector('.readout')).toBeNull();

    vi.advanceTimersByTime(300);
    await el.updateComplete;
    const readout = el.shadowRoot?.querySelector('.readout');
    expect(readout).not.toBeNull();
    expect(readout?.textContent?.trim()).toBe('0:29');
  });

  it('drops the readout when the pointer leaves', async () => {
    vi.useFakeTimers();
    canvas.dispatchEvent(pointer('pointermove', 120));
    vi.advanceTimersByTime(300);
    await el.updateComplete;
    expect(el.shadowRoot?.querySelector('.readout')).not.toBeNull();

    canvas.dispatchEvent(pointer('pointerleave', 120));
    await el.updateComplete;
    expect(el.shadowRoot?.querySelector('.readout')).toBeNull();
  });

  it('does not leave a pending readout behind after leaving', async () => {
    vi.useFakeTimers();
    canvas.dispatchEvent(pointer('pointermove', 120));
    canvas.dispatchEvent(pointer('pointerleave', 120));
    vi.advanceTimersByTime(300);
    await el.updateComplete;
    expect(el.shadowRoot?.querySelector('.readout')).toBeNull();
  });
});
