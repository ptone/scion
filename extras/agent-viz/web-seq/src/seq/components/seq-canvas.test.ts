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
 * Tests for <scion-seq-canvas>'s wheel contract.
 *
 * The canvas reports intent and never moves the clock itself, so what matters
 * here is that the numbers it emits are in the right units: a wheel gesture of
 * N pixels must be worth exactly N pixels of wall time at the current scale,
 * whatever unit the hardware reported it in.
 */

// @vitest-environment happy-dom

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import './seq-canvas.js';
import { wheelPixels, type ScionSeqCanvas } from './seq-canvas.js';

function wheel(init: { deltaY: number; deltaMode?: number; ctrlKey?: boolean }): Event {
  const e = new MouseEvent('wheel', { bubbles: true, composed: true, ctrlKey: init.ctrlKey });
  Object.defineProperty(e, 'deltaY', { value: init.deltaY });
  Object.defineProperty(e, 'deltaMode', { value: init.deltaMode ?? 0 });
  return e;
}

describe('scion-seq-canvas wheel', () => {
  let el: ScionSeqCanvas;
  let canvas: HTMLCanvasElement;

  beforeEach(async () => {
    el = document.createElement('scion-seq-canvas') as ScionSeqCanvas;
    el.msPerPx = 50;
    document.body.appendChild(el);
    await el.updateComplete;
    const found = el.shadowRoot?.querySelector('canvas');
    if (!found) throw new Error('no canvas');
    canvas = found;
  });

  afterEach(() => {
    el.remove();
    document.body.innerHTML = '';
  });

  it('scrolls time one-to-one with the geometry', () => {
    const seen: number[] = [];
    el.addEventListener('seq-scroll-time', (e) => {
      seen.push((e as CustomEvent<{ deltaWallMs: number }>).detail.deltaWallMs);
    });
    canvas.dispatchEvent(wheel({ deltaY: 100 }));
    // 100px at 50ms/px is 5s of wall time, which is exactly what 100px of this
    // canvas represents. Down is forward, because time runs down the canvas.
    expect(seen).toEqual([5000]);
  });

  it('scrolls backwards on an upward wheel', () => {
    const seen: number[] = [];
    el.addEventListener('seq-scroll-time', (e) => {
      seen.push((e as CustomEvent<{ deltaWallMs: number }>).detail.deltaWallMs);
    });
    canvas.dispatchEvent(wheel({ deltaY: -40 }));
    expect(seen).toEqual([-2000]);
  });

  it('zooms rather than scrolls under ctrl', () => {
    const scrolls: number[] = [];
    const zooms: number[] = [];
    el.addEventListener('seq-scroll-time', () => scrolls.push(1));
    el.addEventListener('seq-zoom', (e) => {
      zooms.push((e as CustomEvent<{ factor: number }>).detail.factor);
    });
    canvas.dispatchEvent(wheel({ deltaY: -100, ctrlKey: true }));
    expect(scrolls).toEqual([]);
    expect(zooms).toHaveLength(1);
    // Up zooms in, which means fewer ms per pixel.
    expect(zooms[0]).toBeLessThan(1);
  });

  it('ignores a wheel event with no movement', () => {
    let count = 0;
    el.addEventListener('seq-scroll-time', () => count++);
    el.addEventListener('seq-zoom', () => count++);
    canvas.dispatchEvent(wheel({ deltaY: 0 }));
    expect(count).toBe(0);
  });
});

describe('wheelPixels', () => {
  it('passes pixel deltas through', () => {
    expect(wheelPixels(wheel({ deltaY: 120 }) as WheelEvent)).toBe(120);
  });

  it('scales line deltas, which is what a mouse wheel reports', () => {
    // A notch is 3 lines on most platforms; untranslated it would move the
    // view by three milliseconds instead of a screenful.
    expect(wheelPixels(wheel({ deltaY: 3, deltaMode: 1 }) as WheelEvent)).toBe(48);
  });

  it('scales page deltas', () => {
    expect(wheelPixels(wheel({ deltaY: 1, deltaMode: 2 }) as WheelEvent)).toBe(400);
  });
});
