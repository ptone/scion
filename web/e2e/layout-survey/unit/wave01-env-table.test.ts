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
 * Table-driven E-ENV negative controls (review2 RB1–RB3; assessor R-3/R-4;
 * owner 16:03Z). Every row states the expected grade so a regression in any
 * single rule shows up as one named failing case.
 */

import { describe, expect, it } from 'vitest';
import {
  applyAuthCheck,
  deriveDevAuth,
  envDecision,
  evaluateEnv,
  evaluateEnvPost,
  SHARED_ENV_BOOLEANS,
  sharedEnvValues,
  sourcesKey,
} from '../wave01/records.mjs';

const HOST = 'https://baseline.example';
const OTHER = 'https://candidate.example';
const batchStart = '2026-10-07T16:00:00Z';
const W = { batchStart, maxAgeMin: 30 };
const rel = { slotGeneration: 'gen-1', baseURL: HOST };
const self401 = { status: 401, at: '2026-10-07T15:59:00Z' };
const SOURCES = {
  'unit/args': { '--dev-auth': false, '--hosted': true, '--production': 'absent' },
  settings: {
    global: 'absent',
    local: { 'auth.devMode': false, 'auth.mode': 'absent', mode: 'hosted' },
  },
  environment: {
    SCION_SERVER_AUTH_DEVMODE: 'absent',
    SCION_SERVER_AUTH_MODE: 'absent',
    SCION_SERVER_MODE: 'absent',
  },
  keys_checked: ['argv', 'settings.local:server.auth', 'env:SCION_SERVER_*'],
} as Record<string, any>;
/** Deep-patch helper for the sources fixture. */
const src = (patch: (s: Record<string, any>) => void) => {
  const c = JSON.parse(JSON.stringify(SOURCES));
  patch(c);
  return c;
};

const PRE = {
  baseURL: HOST,
  window_start: '2026-10-07T15:45:00Z',
  slotGeneration: 'gen-1',
  e_env_1_dev_auth_effective: false,
  e_env_1_sources: SOURCES,
  e_env_2_anon_401: { status: 401, ts: '2026-10-07T15:46:00Z' },
  e_env_3_test_login_enabled: true,
  e_env_4_runtime_broker_effective: false,
  e_env_4_no_broker_process_or_dispatch: true,
  e_env_5: {
    probe: 'repo-default-secret-challenge',
    result_status: 401,
    ts: '2026-10-07T15:47:00Z',
  },
} as Record<string, unknown>;

const POST = {
  baseURL: HOST,
  window_start: '2026-10-07T15:45:00Z',
  window_end: '2026-10-07T16:30:00Z',
  slotGeneration: 'gen-1',
  e_env_1_dev_auth_effective: false,
  e_env_1_sources: SOURCES,
  e_env_2_anon_401: { status: 401, ts: '2026-10-07T16:21:00Z' },
  e_env_3_test_login_enabled: true,
  e_env_4_runtime_broker_effective: false,
  e_env_4_no_broker_process_or_dispatch: true,
} as Record<string, unknown>;

const RUN = {
  startedAt: '2026-10-07T16:00:00Z',
  endedAt: '2026-10-07T16:20:00Z',
  slotGeneration: 'gen-1',
  baseURL: HOST,
  preBaseURL: HOST,
  preValues: sharedEnvValues(PRE),
  preSourcesKey: sourcesKey(SOURCES),
  testLoginUsed: true,
};

const gate = (decl: unknown, id: string, self = self401) =>
  evaluateEnv(decl, self, rel, W).find((g) => g.gate === id)!;
const without = (o: Record<string, unknown>, k: string) => {
  const c = { ...o };
  delete c[k];
  return c;
};

describe('baseline fixtures are valid (guards the tables below)', () => {
  it('PRE passes with E-ENV-4 awaiting POST, POST passes', () => {
    const r = evaluateEnv(PRE, self401, rel, W);
    expect(r.map((g) => g.outcome)).toEqual(['pass', 'pass', 'pass', 'inconclusive', 'pass']);
    expect(envDecision(r).stop).toBe(false);
    expect(evaluateEnvPost(POST, RUN).outcome).toBe('pass');
  });
});

describe('R-6: E-ENV-1 derived through backend precedence (assessor order)', () => {
  const rows: Array<[string, (s: Record<string, any>) => void, Record<string, unknown>, string]> = [
    // PASS
    ['baseline: --hosted, --dev-auth=false, settings OFF ⇒ PASS', () => {}, {}, 'pass'],
    [
      'all optional layers explicitly "absent", hosted via flag, explicit --dev-auth=false ⇒ PASS',
      (c) => {
        c.settings = { global: 'absent', local: 'absent' };
      },
      {},
      'pass',
    ],
    [
      'env devMode=true overridden by explicit --dev-auth=false in hosted mode ⇒ PASS (precedence)',
      (c) => {
        c.environment.SCION_SERVER_AUTH_DEVMODE = true;
      },
      {},
      'pass',
    ],
    [
      'hosted via config mode (no flags), config devMode false ⇒ PASS',
      (c) => {
        c['unit/args'] = { '--dev-auth': 'absent', '--hosted': 'absent', '--production': 'absent' };
      },
      {},
      'pass',
    ],
    // FAIL (forbidden established, FAIL wins over completeness)
    [
      'explicit --dev-auth=true ⇒ FAIL',
      (c) => {
        c['unit/args']['--dev-auth'] = true;
      },
      {},
      'fail',
    ],
    [
      'explicit --dev-auth=true with settings/env classes MISSING ⇒ FAIL (missing cannot change it)',
      (c) => {
        c['unit/args']['--dev-auth'] = true;
        delete c.settings;
        delete c.environment;
      },
      {},
      'fail',
    ],
    [
      'non-hosted (--hosted=false) without explicit --dev-auth ⇒ FAIL (workstation default ON)',
      (c) => {
        c['unit/args'] = { '--dev-auth': 'absent', '--hosted': false, '--production': 'absent' };
      },
      {},
      'fail',
    ],
    [
      'hosted, no flag, env devMode=true over local false ⇒ FAIL',
      (c) => {
        c['unit/args']['--dev-auth'] = 'absent';
        c.environment.SCION_SERVER_AUTH_DEVMODE = true;
      },
      {},
      'fail',
    ],
    [
      'effective auth.mode "dev" from env ⇒ FAIL',
      (c) => {
        c.environment.SCION_SERVER_AUTH_MODE = 'dev';
      },
      {},
      'fail',
    ],
    [
      'auth.mode "dev" in local settings, env absent ⇒ FAIL',
      (c) => {
        c.settings.local['auth.mode'] = 'dev';
      },
      {},
      'fail',
    ],
    [
      'declared effective ON, layers derive OFF ⇒ FAIL (declared ON)',
      () => {},
      { e_env_1_dev_auth_effective: true },
      'fail',
    ],
    // INCONCLUSIVE (OFF not proven)
    [
      'class unit/args missing ⇒ INCONCLUSIVE',
      (c) => {
        delete c['unit/args'];
      },
      {},
      'inconclusive',
    ],
    [
      'class settings missing ⇒ INCONCLUSIVE',
      (c) => {
        delete c.settings;
      },
      {},
      'inconclusive',
    ],
    [
      'class environment missing ⇒ INCONCLUSIVE',
      (c) => {
        delete c.environment;
      },
      {},
      'inconclusive',
    ],
    [
      'settings.global key not written (never defaulted) ⇒ INCONCLUSIVE',
      (c) => {
        delete c.settings.global;
      },
      {},
      'inconclusive',
    ],
    [
      'env key missing ⇒ INCONCLUSIVE',
      (c) => {
        delete c.environment.SCION_SERVER_AUTH_MODE;
      },
      {},
      'inconclusive',
    ],
    [
      'flag value malformed ⇒ INCONCLUSIVE',
      (c) => {
        c['unit/args']['--dev-auth'] = 'off';
      },
      {},
      'inconclusive',
    ],
    [
      'unknown class ⇒ INCONCLUSIVE',
      (c) => {
        c.registry = {};
      },
      {},
      'inconclusive',
    ],
    [
      'flat pre-R-6 form ⇒ INCONCLUSIVE',
      (c) => {
        for (const k of Object.keys(c)) delete c[k];
        Object.assign(c, { 'unit/args': false, settings: 'absent', environment: false });
      },
      {},
      'inconclusive',
    ],
    [
      'non-hosted with explicit --dev-auth=false ⇒ INCONCLUSIVE (hosted mode not evidenced)',
      (c) => {
        c['unit/args']['--hosted'] = false;
      },
      {},
      'inconclusive',
    ],
    [
      'declared effective missing ⇒ INCONCLUSIVE',
      () => {},
      { e_env_1_dev_auth_effective: undefined },
      'inconclusive',
    ],
  ];
  for (const [label, patch, declPatch, expected] of rows) {
    it(label, () => {
      expect(gate({ ...PRE, e_env_1_sources: src(patch), ...declPatch }, 'E-ENV-1').outcome).toBe(
        expected
      );
    });
  }
  it('declared-vs-derived disagreement is recorded alongside the FAIL', () => {
    const g = gate(
      { ...PRE, e_env_1_sources: src((c) => (c['unit/args']['--dev-auth'] = true)) },
      'E-ENV-1'
    );
    expect(g.outcome).toBe('fail');
    expect((g.details as { declaredVsDerived: string }).declaredVsDerived).toBe('disagree');
  });
  it('derivation records where each config value came from', () => {
    const d = deriveDevAuth(src((c) => (c.environment.SCION_SERVER_AUTH_DEVMODE = true)));
    expect(d.config['auth.devMode']).toEqual({ value: true, from: 'environment' });
    expect(d.config.mode).toEqual({ value: 'hosted', from: 'settings.local' });
    expect(d.effectiveDevMode).toBe(false);
  });
  it('unbound declaration with forbidden layers ⇒ INCONCLUSIVE (R-3 first)', () => {
    const decl = {
      ...PRE,
      baseURL: OTHER,
      e_env_1_sources: src((c) => (c['unit/args']['--dev-auth'] = true)),
    };
    expect(gate(decl, 'E-ENV-1').outcome).toBe('inconclusive');
  });
});

describe('R-3: bound vs unbound declarations, per forbidden value', () => {
  const forbidden: Array<[string, Record<string, unknown>, string]> = [
    ['dev-auth ON', { e_env_1_dev_auth_effective: true }, 'E-ENV-1'],
    [
      'steward anon 200',
      { e_env_2_anon_401: { status: 200, ts: '2026-10-07T15:46:00Z' } },
      'E-ENV-2',
    ],
    ['broker ON', { e_env_4_runtime_broker_effective: true }, 'E-ENV-4'],
    ['dispatch during window', { e_env_4_no_broker_process_or_dispatch: false }, 'E-ENV-4'],
    [
      'repo-default secret accepted',
      { e_env_5: { probe: 'x', result_status: 200, ts: '2026-10-07T15:47:00Z' } },
      'E-ENV-5',
    ],
  ];
  const bindings: Array<[string, Record<string, unknown>, string]> = [
    ['bound', {}, 'fail'],
    ['cross-host (same generation)', { baseURL: OTHER }, 'inconclusive'],
    ['other generation', { slotGeneration: 'gen-2' }, 'inconclusive'],
  ];
  for (const [what, patch, id] of forbidden) {
    for (const [b, bpatch, expected0] of bindings) {
      // E-ENV-2 is decided by the runner's own in-window probe; an unbound
      // steward probe is ignored, so the gate stays PASS (own probe 401).
      const expected = id === 'E-ENV-2' && expected0 === 'inconclusive' ? 'pass' : expected0;
      it(`${what}, ${b} ⇒ ${id} ${expected.toUpperCase()}`, () => {
        expect(gate({ ...PRE, ...patch, ...bpatch }, id).outcome).toBe(expected);
      });
    }
  }
  it('steward anon 200 is FAIL regardless of its timestamp (assessor 16:03Z)', () => {
    expect(
      gate({ ...PRE, e_env_2_anon_401: { status: 200, ts: '2020-01-01T00:00:00Z' } }, 'E-ENV-2')
        .outcome
    ).toBe('fail');
  });
  it('missing steward anon probe: recorded, own in-window probe decides', () => {
    const g = gate(without(PRE, 'e_env_2_anon_401'), 'E-ENV-2');
    expect(g.outcome).toBe('pass');
    expect((g.details as { steward: unknown }).steward).toBeNull();
  });
});

describe('independent run-probe contradictions are FAIL whatever the declaration', () => {
  const decls: Array<[string, unknown]> = [
    ['no declaration', null],
    ['bound declaration', PRE],
    ['cross-host declaration', { ...PRE, baseURL: OTHER }],
    ['other-generation declaration', { ...PRE, slotGeneration: 'gen-2' }],
  ];
  for (const [label, decl] of decls) {
    it(`own anonymous probe 200, ${label} ⇒ E-ENV-2 FAIL, stop`, () => {
      const r = evaluateEnv(decl, { status: 200, at: '2026-10-07T15:59:00Z' }, rel, W);
      expect(r.find((g) => g.gate === 'E-ENV-2')!.outcome).toBe('fail');
      expect(envDecision(r).stop).toBe(true);
    });
  }
});

describe('RB3: POST shared booleans (required / disagreement)', () => {
  const required = SHARED_ENV_BOOLEANS.filter((k) => k !== 'e_env_1_dev_auth_effective');
  for (const k of required) {
    it(`POST missing ${k} ⇒ INCONCLUSIVE`, () => {
      expect(evaluateEnvPost(without(POST, k), RUN).outcome).toBe('inconclusive');
    });
  }
  it('POST without E-ENV-1 at all is allowed (R-4: not mandated on POST)', () => {
    expect(
      evaluateEnvPost(without(without(POST, 'e_env_1_dev_auth_effective'), 'e_env_1_sources'), RUN)
        .outcome
    ).toBe('pass');
  });
  it('POST with E-ENV-1 but incomplete sources ⇒ INCONCLUSIVE', () => {
    expect(
      evaluateEnvPost({ ...POST, e_env_1_sources: src((c) => delete c.settings) }, RUN).outcome
    ).toBe('inconclusive');
  });
  it('POST layer values differing from PRE (same slot, both derive OFF) ⇒ FAIL', () => {
    const r = evaluateEnvPost(
      { ...POST, e_env_1_sources: src((c) => (c.environment.SCION_SERVER_AUTH_DEVMODE = true)) },
      RUN
    );
    expect(r.outcome).toBe('fail');
    expect(r.problems.join(' ')).toContain('e_env_1_sources layer values');
  });
  const flips: Array<[string, unknown]> = [
    ['e_env_1_dev_auth_effective', true],
    ['e_env_3_test_login_enabled', false],
    ['e_env_4_runtime_broker_effective', true],
    ['e_env_4_no_broker_process_or_dispatch', false],
  ];
  for (const [k, v] of flips) {
    it(`same-slot PRE/POST disagreement on ${k} ⇒ FAIL`, () => {
      const r = evaluateEnvPost(
        {
          ...POST,
          [k]: v,
        },
        RUN
      );
      expect(r.outcome).toBe('fail');
      expect(r.problems.join(' ')).toContain(k);
    });
    it(`different-slot POST disagreeing on ${k} ⇒ INCONCLUSIVE (R-3)`, () => {
      expect(evaluateEnvPost({ ...POST, [k]: v, baseURL: OTHER }, RUN).outcome).toBe(
        'inconclusive'
      );
    });
  }
  it('POST steward anon ≠ 401 (bound) ⇒ FAIL', () => {
    expect(
      evaluateEnvPost(
        { ...POST, e_env_2_anon_401: { status: 302, ts: '2026-10-07T16:21:00Z' } },
        RUN
      ).outcome
    ).toBe('fail');
  });
  it('POST declares test-login disabled while the runner authenticated in the batch ⇒ FAIL', () => {
    const r = evaluateEnvPost(
      { ...POST, e_env_3_test_login_enabled: false },
      { ...RUN, preValues: { ...RUN.preValues, e_env_3_test_login_enabled: false } }
    );
    expect(r.outcome).toBe('fail');
    expect(r.problems.join(' ')).toContain('authenticated via test-login');
  });
});

describe('RB3: PRE test-login disabled ⇒ runner auth check', () => {
  const decl = { ...PRE, e_env_3_test_login_enabled: false };
  it('pending auth check stops the batch until resolved', () => {
    const r = evaluateEnv(decl, self401, rel, W);
    expect(r.find((g) => g.gate === 'E-ENV-3')!.awaitingAuthCheck).toBe(true);
    expect(envDecision(r)).toMatchObject({ stop: true, authCheckPending: true, missing: [] });
  });
  it('runner test-login succeeded ⇒ E-ENV-3 FAIL (contradiction)', () => {
    const r = applyAuthCheck(evaluateEnv(decl, self401, rel, W), {
      succeeded: true,
      at: batchStart,
    });
    expect(r.find((g) => g.gate === 'E-ENV-3')!.outcome).toBe('fail');
    expect(envDecision(r)).toMatchObject({ stop: true, fails: ['E-ENV-3'] });
  });
  it('runner test-login refused ⇒ E-ENV-3 INCONCLUSIVE, stop', () => {
    const r = applyAuthCheck(evaluateEnv(decl, self401, rel, W), {
      succeeded: false,
      at: batchStart,
    });
    expect(r.find((g) => g.gate === 'E-ENV-3')!.outcome).toBe('inconclusive');
    expect(envDecision(r)).toMatchObject({ stop: true, missing: ['E-ENV-3'] });
  });
  it('an UNBOUND declaration with test-login disabled triggers no auth check (R-3)', () => {
    const r = evaluateEnv({ ...decl, baseURL: OTHER }, self401, rel, W);
    expect(r.find((g) => g.gate === 'E-ENV-3')!.awaitingAuthCheck).toBe(false);
  });
});
