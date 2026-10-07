// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * Frozen Wave01 scenario manifest (contract FROZEN rev 5 §4a), transcribed
 * row by row. `adapter` says whether this runner commit implements the
 * state; unimplemented states emit explicit BLOCKED records and are never
 * reported as passing.
 */

export const SHELL_M0_CLAUSES = ['A-D1', 'A-C1', 'A-S1', 'A-S2', 'A-N2'] as const;

export type Substep = 'M0' | 'M1' | 'M2' | 'M3';

export interface TargetDef {
  /** Stable key used in records. */
  key: string;
  label: string;
  /**
   * How the runner locates the target. `css` is a deep (shadow-piercing)
   * selector scoped to the page element; `firstFixtureRowLink` resolves to
   * the first rendered row name link whose id is in the readback-bound
   * fixture set (DOM order).
   */
  locate: { css: string } | { firstFixtureRowLink: true };
  cite: string;
}

export interface StateDef {
  id: string;
  route: string;
  fixture: string;
  interactions: string;
  m0Clauses: readonly string[];
  af1Targets: readonly TargetDef[];
  /** M3 clause or null. */
  m3: 'B-I1' | 'A-L4' | null;
  /** A-N1 (M2) runs on S04 and S05 only. */
  m2: boolean;
  readiness: 'now' | 'PENDING helper';
  adapter: 'implemented' | 'blocked';
  blockedReason?: string;
}

const CREATE_GROUP: TargetDef = {
  key: 'create-group-btn',
  label: 'Create group',
  locate: { css: '#create-group-btn' },
  cite: 'admin-groups.ts:901-909',
};
const FIRST_FIXTURE_ROW: TargetDef = {
  key: 'first-fixture-row-link',
  label: 'first groups-v1 row name link',
  locate: { firstFixtureRowLink: true },
  cite: 'admin-groups.ts:1217-1227',
};

const NOT_IN_SLICE = 'adapter not implemented at this runner commit (phase 2 of the runner brief)';

export const STATES: readonly StateDef[] = Object.freeze([
  {
    id: 'W01-S01',
    route: '/admin/groups',
    fixture: 'groups-v1 (existing steward seed via POST /api/v1/groups)',
    interactions: '—',
    m0Clauses: ['B-C1', 'B-A1'],
    af1Targets: [CREATE_GROUP, FIRST_FIXTURE_ROW],
    m3: null,
    m2: false,
    readiness: 'now',
    adapter: 'implemented',
  },
  {
    id: 'W01-S02',
    route: '/admin/groups?q=<fixture tag>',
    fixture: 'groups-v1 short/long/unicode',
    interactions: '—',
    m0Clauses: ['B-C1', 'B-A1', 'B-A2', 'B-A3', 'B-OVR'],
    af1Targets: [CREATE_GROUP, { ...FIRST_FIXTURE_ROW, label: 'first fixture row name link' }],
    m3: 'B-I1',
    m2: false,
    readiness: 'now',
    adapter: 'implemented',
  },
  {
    id: 'W01-S03',
    route: '/admin/groups/<long id>',
    fixture: 'groups-v1 long (+ optional 2 members via harness addGroupMember)',
    interactions: '—',
    m0Clauses: ['A-T1', 'A-T2', 'label chips CLIP within their card'],
    af1Targets: [],
    m3: null,
    m2: false,
    readiness: 'now',
    adapter: 'blocked',
    blockedReason: NOT_IN_SLICE,
  },
  {
    id: 'W01-S04',
    route: '/',
    fixture: 'F-PROJ (API) + helper agents + groups-v1',
    interactions: '—',
    m0Clauses: [
      'stat cards: A-C1 each, pairwise overlap ≤1px',
      'activity rows: A-L2 within .activity-list',
    ],
    af1Targets: [],
    m3: null,
    m2: true,
    readiness: 'PENDING helper',
    adapter: 'blocked',
    blockedReason: NOT_IN_SLICE,
  },
  {
    id: 'W01-S05',
    route: '/',
    fixture: 'F-PROJ',
    interactions: 'P1 drawer open; P2/P3 sidebar collapse',
    m0Clauses: ['drawer/collapsed nav clauses (§4a row S05)'],
    af1Targets: [],
    m3: null,
    m2: true,
    readiness: 'now',
    adapter: 'blocked',
    blockedReason: NOT_IN_SLICE,
  },
  ...(
    [
      ['W01-S06', '/projects', 'F-PROJ ×6 (API)', '—', ['A-L1', 'A-L2', 'A-L3'], 'A-L4', 'now'],
      [
        'W01-S07',
        '/projects',
        'F-PROJ ×6',
        'view toggle "table"',
        ['A-L1', 'A-L2', 'A-L3', 'hide-mobile'],
        'A-L4',
        'now',
      ],
      [
        'W01-S08',
        '/projects',
        'F-PROJ; shared-scope readback = 0',
        'scope toggle "Shared"',
        ['A-L5', '.scope-toggle'],
        null,
        'now',
      ],
      [
        'W01-S09',
        '/projects/<empty id>',
        'F-PROJ empty',
        '—',
        ['A-T1', 'A-T2', 'A-L5'],
        null,
        'now',
      ],
      [
        'W01-S10',
        '/projects/<dense id>',
        'F-PROJ dense + 12 helper agents',
        '—',
        ['A-L1', 'A-L2', 'A-L3', 'A-T1', 'A-T2', 'hide-mobile'],
        'A-L4',
        'PENDING helper',
      ],
      [
        'W01-S11',
        '/agents',
        '~18 helper agents across 3 projects',
        '—',
        ['A-L1', 'A-L2', 'A-L3', 'controls', 'hide-mobile'],
        'A-L4',
        'PENDING helper',
      ],
      [
        'W01-S12',
        '/agents/<long id>',
        'helper agent "long"',
        '—',
        ['A-T1', 'A-T2', 'A-T3', 'status badge'],
        null,
        'PENDING helper',
      ],
      [
        'W01-S13',
        '/brokers',
        '3 B1 offline brokers',
        '—',
        ['A-L1', 'A-L2', 'A-L3', 'offline', 'hide-mobile'],
        null,
        'PENDING helper',
      ],
      [
        'W01-S14',
        '/admin/users',
        'F-USER ×6 via test-login',
        '—',
        ['A-L1', 'A-L2', 'A-L3', 'hide-mobile'],
        null,
        'now',
      ],
      [
        'W01-S15',
        '/agents',
        'helper agents; shared-scope readback = 0',
        'scope toggle "Shared"',
        ['A-L5', '.scope-toggle'],
        null,
        'PENDING helper',
      ],
    ] as const
  ).map(
    ([id, route, fixture, interactions, m0Clauses, m3, readiness]): StateDef => ({
      id,
      route,
      fixture,
      interactions,
      m0Clauses,
      af1Targets: [],
      m3,
      m2: false,
      readiness,
      adapter: 'blocked',
      blockedReason: NOT_IN_SLICE,
    })
  ),
]);

export function stateById(id: string): StateDef {
  const s = STATES.find((x) => x.id === id);
  if (!s) throw new Error(`unknown Wave01 state ${id}`);
  return s;
}

/** Substeps that a state runs (§2b): M0 always, M1 always, M2 only S04/S05, M3 where declared. */
export function substepsFor(s: StateDef): Substep[] {
  return ['M0', 'M1', ...(s.m2 ? (['M2'] as const) : []), ...(s.m3 ? (['M3'] as const) : [])];
}
