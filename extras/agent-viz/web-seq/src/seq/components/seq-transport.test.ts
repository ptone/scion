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
 * Tests for <scion-seq-transport>, which is the event contract the playback
 * owner wires against. Asserted here:
 *   - the play/pause button dispatches `seq-play-toggle` (no detail);
 *   - the scrubber dispatches `seq-seek` carrying the dragged viewer time;
 *   - the rate selector dispatches `seq-rate-change` with a numeric rate;
 *   - all three events cross the shadow boundary (bubbles + composed);
 *   - the velocity indicator switches to "express" only above real time, and
 *     the absolute clock readout is derived from `startedAt + wallMs`.
 */

// @vitest-environment happy-dom

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import './seq-transport.js';
import type { ScionSeqTransport } from './seq-transport.js';

function shadow(el: ScionSeqTransport): ShadowRoot {
  const root = el.shadowRoot;
  if (!root) throw new Error('transport has no shadow root');
  return root;
}

describe('scion-seq-transport', () => {
  let el: ScionSeqTransport;

  beforeEach(async () => {
    el = document.createElement('scion-seq-transport') as ScionSeqTransport;
    el.totalTauMs = 60_000;
    el.tauMs = 12_000;
    el.durationMs = 3_600_000;
    el.wallMs = 600_000;
    el.startedAt = '2026-08-05T10:00:00.000Z';
    el.rate = 1;
    el.velocity = 1;
    document.body.appendChild(el);
    await el.updateComplete;
  });

  afterEach(() => {
    el.remove();
    document.body.innerHTML = '';
  });

  it('dispatches seq-play-toggle with no detail when the play button is clicked', () => {
    let count = 0;
    let detail: unknown = 'unset';
    el.addEventListener('seq-play-toggle', (e) => {
      count++;
      detail = (e as CustomEvent).detail;
    });

    const button = shadow(el).querySelector<HTMLButtonElement>('.play-btn');
    expect(button).toBeTruthy();
    button?.click();

    expect(count).toBe(1);
    expect(detail).toBeNull();
  });

  it('reflects the playing state on the play button label', async () => {
    expect(shadow(el).querySelector('.play-btn')?.getAttribute('aria-label')).toBe('Play');
    el.playing = true;
    await el.updateComplete;
    expect(shadow(el).querySelector('.play-btn')?.getAttribute('aria-label')).toBe('Pause');
  });

  it('dispatches seq-seek with the scrubbed viewer time', () => {
    const seen: number[] = [];
    el.addEventListener('seq-seek', (e) => {
      seen.push((e as CustomEvent<{ tauMs: number }>).detail.tauMs);
    });

    const range = shadow(el).querySelector<HTMLInputElement>('input.scrub');
    expect(range).toBeTruthy();
    expect(range?.getAttribute('max')).toBe('60000');

    if (range) {
      range.value = '42000';
      range.dispatchEvent(new Event('input', { bubbles: true, composed: true }));
    }

    expect(seen).toEqual([42000]);
  });

  it('never emits a non-finite tauMs, even from a garbage range value', () => {
    const seen: number[] = [];
    el.addEventListener('seq-seek', (e) => {
      seen.push((e as CustomEvent<{ tauMs: number }>).detail.tauMs);
    });
    const range = shadow(el).querySelector<HTMLInputElement>('input.scrub');
    if (range) {
      // A range input sanitizes its own value, but the handler guards anyway.
      range.value = 'not-a-number';
      range.dispatchEvent(new Event('input'));
    }
    expect(seen.every((v) => Number.isFinite(v))).toBe(true);
  });

  it('dispatches seq-rate-change with a numeric rate from the selector', () => {
    let rate: number | null = null;
    el.addEventListener('seq-rate-change', (e) => {
      rate = (e as CustomEvent<{ rate: number }>).detail.rate;
    });

    const select = shadow(el).querySelector('sl-select');
    expect(select).toBeTruthy();
    (select as unknown as { value: string }).value = '4';
    select?.dispatchEvent(new Event('sl-change'));

    expect(rate).toBe(4);
  });

  it('offers the documented rate options', () => {
    const values = Array.from(shadow(el).querySelectorAll('sl-option')).map((o) =>
      o.getAttribute('value')
    );
    expect(values).toEqual(['0.25', '0.5', '1', '2', '4', '8']);
  });

  it('bubbles its events across the shadow boundary', () => {
    let seen = false;
    document.body.addEventListener('seq-play-toggle', () => {
      seen = true;
    });
    shadow(el).querySelector<HTMLButtonElement>('.play-btn')?.click();
    expect(seen).toBe(true);
  });

  it('labels ordinary playback as real time and fast traversal as express', async () => {
    expect(shadow(el).querySelector('.velocity')?.textContent).toContain('real time');
    expect(shadow(el).querySelector('.velocity')?.classList.contains('express')).toBe(false);

    el.velocity = 42;
    el.maxVelocity = 120;
    await el.updateComplete;

    const badge = shadow(el).querySelector('.velocity');
    expect(badge?.textContent).toContain('42× express');
    expect(badge?.classList.contains('express')).toBe(true);
  });

  it('derives the absolute clock readout from startedAt + wallMs', () => {
    const expected = new Date(Date.parse('2026-08-05T10:00:00.000Z') + 600_000);
    const hh = String(expected.getHours()).padStart(2, '0');
    const mm = String(expected.getMinutes()).padStart(2, '0');
    expect(shadow(el).querySelector('.clock-time')?.textContent?.trim()).toBe(
      `${hh}:${mm}:00.000`
    );
  });

  it('shows a placeholder clock when the run start is unparseable', async () => {
    el.startedAt = 'not-a-timestamp';
    await el.updateComplete;
    expect(shadow(el).querySelector('.clock-time')?.textContent?.trim()).toBe('--:--:--.---');
  });

  it('shows elapsed and total in both time domains', () => {
    const domains = shadow(el).querySelector('.domains')?.textContent ?? '';
    // wall: 10m of a 1h run; tau: 12s of 60s.
    expect(domains).toContain('10:00');
    expect(domains).toContain('1:00:00');
    expect(domains).toContain('0:12');
  });
});
