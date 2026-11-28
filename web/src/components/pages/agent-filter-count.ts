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

import type { AgentListWindow } from '../../client/agent-list-window.js';
import { formatFilterResultCount } from '../shared/filter-box/filter-count.js';

export interface AgentWindowFilterCountInput {
  window: Pick<
    AgentListWindow,
    | 'state'
    | 'total'
    | 'totalApproximate'
    | 'display'
    | 'incompleteReason'
    | 'stats'
    | 'memberIndex'
  >;
  /** Length of the page's local `agents` array (used when `agentsIsUnfiltered` is true). */
  agentsLength: number;
  /** True only when the local `agents` array was fetched with no server `?q=` filter (including no `phase:`). */
  agentsIsUnfiltered: boolean;
  /** True when the committed query has no non-phase server predicates (so `stats.total` is the phase-independent total). */
  phaseOnlyServerFilter: boolean;
  /** Cached unfiltered total from an earlier unfiltered load, or `null` when unknown. */
  unfilteredTotal: number | null;
  /** True when `unfilteredTotal` is a lower bound (`+`). */
  unfilteredTotalApproximate: boolean;
  /** Optional fallback total (e.g. `project.agentCount`) when no unfiltered load total is cached. */
  fallbackTotal?: number | null;
}

/**
 * Computes and formats the active filter match count for an `AgentListWindow`-backed
 * page (`<scion-page-agents>` and `<scion-page-project-detail>`).
 *
 * Never trusts `stateManager.isAgentSetComplete()` as a denominator because
 * phase-filtered complete loads can mark the store complete with a subset.
 */
export function formatAgentWindowFilterCount(input: AgentWindowFilterCountInput): string {
  const { window: win } = input;
  let matched: number;
  let matchedApproximate: boolean;
  let total: number | null = null;
  let totalApproximate = false;

  if (win.state === 'paged') {
    matched = typeof win.total === 'number' ? win.total : 0;
    matchedApproximate = win.totalApproximate;
    if (input.phaseOnlyServerFilter && (win.memberIndex.countOnly || win.memberIndex.size > 0)) {
      total = win.stats.total;
      totalApproximate = win.memberIndex.approximate;
    } else if (input.unfilteredTotal !== null) {
      total = input.unfilteredTotal;
      totalApproximate = input.unfilteredTotalApproximate;
    } else if (typeof input.fallbackTotal === 'number') {
      total = input.fallbackTotal;
    }
  } else {
    matched = win.display.length;
    matchedApproximate = win.incompleteReason !== null;
    if (input.agentsIsUnfiltered) {
      if (win.incompleteReason === null) {
        total = input.agentsLength;
      } else if (win.incompleteReason === 'capped') {
        total = input.agentsLength;
        totalApproximate = true;
      } else if (typeof input.fallbackTotal === 'number') {
        total = input.fallbackTotal;
      }
    } else if (input.unfilteredTotal !== null) {
      total = input.unfilteredTotal;
      totalApproximate = input.unfilteredTotalApproximate;
    } else if (typeof input.fallbackTotal === 'number') {
      total = input.fallbackTotal;
    }
  }

  return formatFilterResultCount({
    active: true,
    matched,
    matchedApproximate,
    total,
    totalApproximate,
    singular: 'agent',
    plural: 'agents',
  });
}
