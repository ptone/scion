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
  classifyDevAuthProbes,
  envDecision,
  evaluateEnv,
  evaluateEnvPost,
  SHARED_ENV_BOOLEANS,
  sharedEnvValues,
  provenanceKey,
} from '../wave01/records.mjs';

const HOST = 'https://baseline.example';
const OTHER = 'https://candidate.example';
const batchStart = '2026-10-07T16:00:00Z';
const W = { batchStart, maxAgeMin: 30 };
const rel = { slotGeneration: 'gen-1', baseURL: HOST };
const self401 = { status: 401, at: '2026-10-07T15:59:00Z' };
const PROV = {
  flags: { '--hosted': true, '--production': 'absent', '--dev-auth': false },
  load_path: 'settings-global',
  files_examined: ['~/.scion/settings.yaml'],
  path_values: {
    'server.mode': 'hosted',
    'server.auth.dev_mode': false,
    'server.auth.mode': 'absent',
  },
  env: {
    SCION_SERVER_MODE: 'absent',
    SCION_SERVER_AUTH_DEVMODE: 'absent',
    SCION_SERVER_AUTH_MODE: 'absent',
    SCION_SERVER_AUTH_DEV_MODE: 'absent',
  },
  declared_effective_hosted: true,
  declared_effective_auth_mode: 'oauth',
} as Record<string, any>;
/** Deep-patch helper for the provenance fixture. */
const prov = (patch: (p: Record<string, any>) => void) => {
  const c = JSON.parse(JSON.stringify(PROV));
  patch(c);
  return c;
};
const P_API_OFF = {
  status: 401,
  message: 'development authentication is not enabled',
  at: '2026-10-07T15:59:30Z',
};
const P_WEB_OFF = { status: 401, hasIdentity: false, at: '2026-10-07T15:59:31Z' };
const PROBES_OFF = { api: P_API_OFF, web: P_WEB_OFF };

const PRE = {
  baseURL: HOST,
  window_start: '2026-10-07T15:45:00Z',
  slotGeneration: 'gen-1',
  e_env_1_dev_auth_effective: false,
  e_env_1_provenance: PROV,
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
  e_env_1_provenance: PROV,
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
  preProvenanceKey: provenanceKey(PROV),
  testLoginUsed: true,
};

const gate = (decl: unknown, id: string, self = self401, probes: unknown = PROBES_OFF) =>
  evaluateEnv(decl, self, rel, W, probes as never).find((g) => g.gate === id)!;
const without = (o: Record<string, unknown>, k: string) => {
  const c = { ...o };
  delete c[k];
  return c;
};

describe('baseline fixtures are valid (guards the tables below)', () => {
  it('PRE passes with E-ENV-4 awaiting POST, POST passes', () => {
    const r = evaluateEnv(PRE, self401, rel, W, PROBES_OFF);
    expect(r.map((g) => g.outcome)).toEqual(['pass', 'pass', 'pass', 'inconclusive', 'pass']);
    expect(envDecision(r).stop).toBe(false);
    expect(evaluateEnvPost(POST, RUN).outcome).toBe('pass');
  });
});

describe('rev 4 E-ENV-1a probe classification', () => {
  const api: Array<[string, unknown, string]> = [
    ['401 "development authentication is not enabled" ⇒ off', P_API_OFF, 'off'],
    [
      '401 "invalid development token" ⇒ on',
      { ...P_API_OFF, message: 'invalid development token' },
      'on',
    ],
    ['200 ⇒ on', { ...P_API_OFF, status: 200, message: null }, 'on'],
    [
      '401 "missing authorization header" ⇒ unexplained',
      { ...P_API_OFF, message: 'missing authorization header' },
      'unexplained',
    ],
    ['302 ⇒ unexplained', { ...P_API_OFF, status: 302, message: null }, 'unexplained'],
    ['missing ⇒ missing', null, 'missing'],
  ];
  for (const [label, probe, expected] of api) {
    it(`P-API ${label}`, () =>
      expect(classifyDevAuthProbes(probe as never, P_WEB_OFF).api).toBe(expected));
  }
  const web: Array<[string, unknown, string]> = [
    ['401 without identity ⇒ off', P_WEB_OFF, 'off'],
    [
      '200 with identity (dev auto-login) ⇒ on',
      { ...P_WEB_OFF, status: 200, hasIdentity: true },
      'on',
    ],
    ['401 with identity ⇒ on', { ...P_WEB_OFF, hasIdentity: true }, 'on'],
    ['200 without identity ⇒ unexplained', { ...P_WEB_OFF, status: 200 }, 'unexplained'],
    ['missing ⇒ missing', null, 'missing'],
  ];
  for (const [label, probe, expected] of web) {
    it(`P-WEB ${label}`, () =>
      expect(classifyDevAuthProbes(P_API_OFF, probe as never).web).toBe(expected));
  }
});

describe('rev 4 E-ENV-1b provenance record (binding/completeness/contradictions, no resolver)', () => {
  const missingRows: Array<[string, (p: Record<string, any>) => void]> = [
    ['flags.--hosted', (p) => delete p.flags['--hosted']],
    ['flags.--production', (p) => delete p.flags['--production']],
    ['flags.--dev-auth', (p) => delete p.flags['--dev-auth']],
    ['load_path', (p) => delete p.load_path],
    ['unknown load_path', (p) => (p.load_path = 'auto')],
    ['files_examined', (p) => (p.files_examined = [])],
    ['path_values.server.mode', (p) => delete p.path_values['server.mode']],
    ['path_values.server.auth.dev_mode', (p) => delete p.path_values['server.auth.dev_mode']],
    ['path_values.server.auth.mode', (p) => delete p.path_values['server.auth.mode']],
    ['env.SCION_SERVER_MODE', (p) => delete p.env.SCION_SERVER_MODE],
    ['env.SCION_SERVER_AUTH_DEVMODE', (p) => delete p.env.SCION_SERVER_AUTH_DEVMODE],
    ['env.SCION_SERVER_AUTH_MODE', (p) => delete p.env.SCION_SERVER_AUTH_MODE],
    ['env.SCION_SERVER_AUTH_DEV_MODE presence', (p) => (p.env.SCION_SERVER_AUTH_DEV_MODE = false)],
    ['declared_effective_hosted', (p) => delete p.declared_effective_hosted],
    ['declared_effective_auth_mode', (p) => delete p.declared_effective_auth_mode],
    ['legacy path without per-file values', (p) => (p.load_path = 'legacy')],
  ];
  for (const [label, patch] of missingRows) {
    it(`missing/malformed ${label} ⇒ INCONCLUSIVE`, () => {
      expect(gate({ ...PRE, e_env_1_provenance: prov(patch) }, 'E-ENV-1').outcome).toBe(
        'inconclusive'
      );
    });
  }
  it('complete legacy-path record (per merged file) ⇒ PASS', () => {
    const p = prov((c) => {
      c.load_path = 'legacy';
      c.files_examined = ['~/.scion/server.yaml', './server.yaml'];
      c.path_values = {
        files: [
          {
            file: '~/.scion/server.yaml',
            mode: 'hosted',
            'auth.devMode': false,
            'auth.mode': 'absent',
          },
          {
            file: './server.yaml',
            mode: 'absent',
            'auth.devMode': 'absent',
            'auth.mode': 'absent',
          },
        ],
      };
    });
    expect(gate({ ...PRE, e_env_1_provenance: p }, 'E-ENV-1').outcome).toBe('pass');
  });
  const forbidden: Array<[string, Record<string, unknown>]> = [
    ['declared effective dev-auth ON', { e_env_1_dev_auth_effective: true }],
    [
      'declared hosted false',
      { e_env_1_provenance: prov((c) => (c.declared_effective_hosted = false)) },
    ],
    [
      'explicit --dev-auth=true',
      { e_env_1_provenance: prov((c) => (c.flags['--dev-auth'] = true)) },
    ],
    [
      'declared auth.mode "dev"',
      { e_env_1_provenance: prov((c) => (c.declared_effective_auth_mode = 'dev')) },
    ],
  ];
  for (const [label, patch] of forbidden) {
    it(`bound record: ${label} ⇒ FAIL`, () =>
      expect(gate({ ...PRE, ...patch }, 'E-ENV-1').outcome).toBe('fail'));
    it(`UNBOUND record: ${label} ⇒ INCONCLUSIVE (R-7: excluded, counts as missing)`, () =>
      expect(gate({ ...PRE, ...patch, baseURL: OTHER }, 'E-ENV-1').outcome).toBe('inconclusive'));
  }
  const contradictions: Array<[string, (p: Record<string, any>) => void, Record<string, unknown>]> =
    [
      [
        'explicit --dev-auth=false vs declared ON is FAIL (forbidden wins)',
        () => {},
        { e_env_1_dev_auth_effective: true },
      ],
      [
        'explicit --hosted=false vs declared hosted true ⇒ INCONCLUSIVE',
        (p) => (p.flags['--hosted'] = false),
        {},
      ],
      [
        'SCION_SERVER_AUTH_MODE=proxy vs declared oauth ⇒ INCONCLUSIVE',
        (p) => (p.env.SCION_SERVER_AUTH_MODE = 'proxy'),
        {},
      ],
    ];
  for (const [label, patch, extra] of contradictions) {
    const expected = label.includes('FAIL') ? 'fail' : 'inconclusive';
    it(label, () =>
      expect(gate({ ...PRE, e_env_1_provenance: prov(patch), ...extra }, 'E-ENV-1').outcome).toBe(
        expected
      )
    );
  }
  it('lower-layer file values are NOT checked against declarations (no resolver)', () => {
    const p = prov((c) => (c.path_values['server.auth.dev_mode'] = true)); // e.g. overridden by explicit --dev-auth=false
    expect(gate({ ...PRE, e_env_1_provenance: p }, 'E-ENV-1').outcome).toBe('pass');
  });
});

describe('R-7: per-artifact attribution for E-ENV-1', () => {
  const ON = { api: { ...P_API_OFF, message: 'invalid development token' }, web: P_WEB_OFF };
  const rows: Array<[string, unknown, unknown, string]> = [
    ['probes OFF + bound complete record ⇒ PASS', PRE, PROBES_OFF, 'pass'],
    ['probe ON + bound record ⇒ FAIL', PRE, ON, 'fail'],
    [
      'probe ON + UNBOUND record ⇒ FAIL (own probe attributable by construction)',
      { ...PRE, baseURL: OTHER },
      ON,
      'fail',
    ],
    ['probe ON + other-generation record ⇒ FAIL', { ...PRE, slotGeneration: 'gen-2' }, ON, 'fail'],
    ['probe ON + no declaration ⇒ FAIL', null, ON, 'fail'],
    [
      'probes OFF + UNBOUND record ⇒ INCONCLUSIVE',
      { ...PRE, baseURL: OTHER },
      PROBES_OFF,
      'inconclusive',
    ],
    [
      'probes OFF + no declaration ⇒ INCONCLUSIVE (record required)',
      null,
      PROBES_OFF,
      'inconclusive',
    ],
    [
      'probes OFF + record missing ⇒ INCONCLUSIVE',
      without(PRE, 'e_env_1_provenance'),
      PROBES_OFF,
      'inconclusive',
    ],
    [
      'P-WEB unexplained ⇒ INCONCLUSIVE',
      PRE,
      { api: P_API_OFF, web: { ...P_WEB_OFF, status: 500 } },
      'inconclusive',
    ],
    ['probes missing ⇒ INCONCLUSIVE', PRE, null, 'inconclusive'],
  ];
  for (const [label, decl, probes, expected] of rows) {
    it(label, () => {
      const r = evaluateEnv(decl, self401, rel, W, probes as never);
      expect(r.find((g) => g.gate === 'E-ENV-1')!.outcome).toBe(expected);
    });
  }
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
      const r = evaluateEnv(decl, { status: 200, at: '2026-10-07T15:59:00Z' }, rel, W, PROBES_OFF);
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
  it('POST without an E-ENV-1b record is allowed (required on PRE only)', () => {
    expect(
      evaluateEnvPost(
        without(without(POST, 'e_env_1_dev_auth_effective'), 'e_env_1_provenance'),
        RUN
      ).outcome
    ).toBe('pass');
  });
  it('POST with an incomplete E-ENV-1b record ⇒ INCONCLUSIVE', () => {
    expect(
      evaluateEnvPost({ ...POST, e_env_1_provenance: prov((c) => delete c.load_path) }, RUN).outcome
    ).toBe('inconclusive');
  });
  it('POST provenance differing from PRE (same slot) ⇒ FAIL', () => {
    const r = evaluateEnvPost(
      { ...POST, e_env_1_provenance: prov((c) => (c.files_examined = ['./other.yaml'])) },
      RUN
    );
    expect(r.outcome).toBe('fail');
    expect(r.problems.join(' ')).toContain('e_env_1_provenance');
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
    const r = evaluateEnv(decl, self401, rel, W, PROBES_OFF);
    expect(r.find((g) => g.gate === 'E-ENV-3')!.awaitingAuthCheck).toBe(true);
    expect(envDecision(r)).toMatchObject({ stop: true, authCheckPending: true, missing: [] });
  });
  it('runner test-login succeeded ⇒ E-ENV-3 FAIL (contradiction)', () => {
    const r = applyAuthCheck(evaluateEnv(decl, self401, rel, W, PROBES_OFF), {
      succeeded: true,
      at: batchStart,
    });
    expect(r.find((g) => g.gate === 'E-ENV-3')!.outcome).toBe('fail');
    expect(envDecision(r)).toMatchObject({ stop: true, fails: ['E-ENV-3'] });
  });
  it('runner test-login refused ⇒ E-ENV-3 INCONCLUSIVE, stop', () => {
    const r = applyAuthCheck(evaluateEnv(decl, self401, rel, W, PROBES_OFF), {
      succeeded: false,
      at: batchStart,
    });
    expect(r.find((g) => g.gate === 'E-ENV-3')!.outcome).toBe('inconclusive');
    expect(envDecision(r)).toMatchObject({ stop: true, missing: ['E-ENV-3'] });
  });
  it('an UNBOUND declaration with test-login disabled triggers no auth check (R-3)', () => {
    const r = evaluateEnv({ ...decl, baseURL: OTHER }, self401, rel, W, PROBES_OFF);
    expect(r.find((g) => g.gate === 'E-ENV-3')!.awaitingAuthCheck).toBe(false);
  });
});
