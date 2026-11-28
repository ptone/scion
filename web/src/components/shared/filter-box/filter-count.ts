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

import { formatNumber } from '../../../utils/format-number.js';

export interface FilterResultCountOptions {
  /** Whether a committed filter is currently active. When false, returns `''`. */
  active: boolean;
  /** Number of matching items. */
  matched: number;
  /** True when `matched` is a lower bound (rendered with a trailing `+`). */
  matchedApproximate?: boolean;
  /** Total unfiltered items in scope, or `null`/`undefined` when unknown. */
  total?: number | null;
  /** True when `total` is a lower bound (rendered with a trailing `+`). */
  totalApproximate?: boolean;
  /** Singular noun (e.g. `'agent'`). */
  singular?: string;
  /** Plural noun (e.g. `'agents'`). */
  plural?: string;
}

/**
 * Formats a human- and screen-reader-friendly filter match count, such as:
 * - `"12 of 84 agents"`
 * - `"0 of 84 agents"`
 * - `"1 of 1 agent"`
 * - `"12+ of 2,000+ agents"`
 * - `"12 matching"` (when `total` is unknown or stale below `matched`)
 *
 * Returns `''` when no filter is active (`active: false`).
 */
export function formatFilterResultCount(options: FilterResultCountOptions): string {
  if (!options.active) return '';

  const matched = Math.max(0, options.matched);
  const matchedStr = `${formatNumber(matched)}${options.matchedApproximate ? '+' : ''}`;

  const rawTotal = options.total;
  const hasUsableTotal =
    typeof rawTotal === 'number' &&
    Number.isFinite(rawTotal) &&
    rawTotal >= 0 &&
    (rawTotal >= matched || Boolean(options.totalApproximate));

  if (!hasUsableTotal) {
    return `${matchedStr} matching`;
  }

  const total = Math.max(matched, rawTotal);
  const totalApprox = Boolean(options.totalApproximate);
  const totalStr = `${formatNumber(total)}${totalApprox ? '+' : ''}`;
  const noun = total === 1 && !totalApprox ? (options.singular ?? options.plural) : options.plural;

  return noun ? `${matchedStr} of ${totalStr} ${noun}` : `${matchedStr} of ${totalStr}`;
}
