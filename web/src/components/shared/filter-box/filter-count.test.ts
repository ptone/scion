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

import { describe, expect, it } from 'vitest';
import { formatFilterResultCount } from './filter-count.js';

describe('formatFilterResultCount', () => {
  it('returns empty string when no filter is active', () => {
    expect(
      formatFilterResultCount({
        active: false,
        matched: 84,
        total: 84,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('');
  });

  it('formats exact matched and total counts with plural noun', () => {
    expect(
      formatFilterResultCount({
        active: true,
        matched: 12,
        total: 84,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('12 of 84 agents');
  });

  it('formats zero matches with known total', () => {
    expect(
      formatFilterResultCount({
        active: true,
        matched: 0,
        total: 84,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('0 of 84 agents');
  });

  it('uses singular noun when total is exactly 1 and not approximate', () => {
    expect(
      formatFilterResultCount({
        active: true,
        matched: 1,
        total: 1,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('1 of 1 agent');
    expect(
      formatFilterResultCount({
        active: true,
        matched: 0,
        total: 1,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('0 of 1 agent');
  });

  it('uses plural noun when total is 1+ (approximate)', () => {
    expect(
      formatFilterResultCount({
        active: true,
        matched: 1,
        total: 1,
        totalApproximate: true,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('1 of 1+ agents');
  });

  it('falls back to "N matching" when total is null or undefined', () => {
    expect(
      formatFilterResultCount({
        active: true,
        matched: 12,
        total: null,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('12 matching');
    expect(
      formatFilterResultCount({
        active: true,
        matched: 0,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('0 matching');
  });

  it('falls back to "N matching" when an exact total is stale below matched', () => {
    expect(
      formatFilterResultCount({
        active: true,
        matched: 15,
        total: 12,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('15 matching');
  });

  it('formats approximate lower-bound counts with + and locale grouping', () => {
    expect(
      formatFilterResultCount({
        active: true,
        matched: 2000,
        matchedApproximate: true,
        total: null,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('2,000+ matching');
    expect(
      formatFilterResultCount({
        active: true,
        matched: 1200,
        matchedApproximate: true,
        total: 2000,
        totalApproximate: true,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('1,200+ of 2,000+ agents');
  });

  it('omits noun cleanly when neither singular nor plural is supplied', () => {
    expect(formatFilterResultCount({ active: true, matched: 3, total: 10 })).toBe('3 of 10');
  });

  it('clamps negative matched counts to 0 and rejects negative totals', () => {
    expect(
      formatFilterResultCount({
        active: true,
        matched: -5,
        total: 10,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('0 of 10 agents');
    expect(
      formatFilterResultCount({
        active: true,
        matched: 3,
        total: -1,
        singular: 'agent',
        plural: 'agents',
      })
    ).toBe('3 matching');
  });
});
