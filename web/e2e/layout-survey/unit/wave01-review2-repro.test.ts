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
 * Preserved reproduction cases from wl-wave1-runner-review2 round 1
 * (report a1bad26d…6666). Source artifacts, byte-verified on receipt:
 *   envrepro.mjs   2027 B acc81e452e32c481814c58278eb70e0a09bb7fd56653ccf5d337b8eb740f0fff (R1–R8)
 *   envrepro2.mjs  2680 B afd02d3519d2cb41313c18ecdb1d0370d1801716ca34c105907ab3e1c8b23aee (C0–C9, P1–P5)
 *   fakehub.mjs    2157 B f115422c069b2bea4a46251380cde5e076d0c3d5e86a05e738386f862200dc8f
 *   fixture.json    376 B 784820b2af278f9f3f61daa5ad15fad114b923a67480ed4f3197429e58466d5b
 *   run-loopback.sh 1232 B e45a36d2209e5acb99bd74b69eb6e99d3505aff3d0ecea1843e8550ab1002a47
 * The loopback modes (SAFETY, readback failure, test-login abort) are
 * captured in wave01-loopback.selftest.pw.ts. Case IDs are kept so a future
 * reviewer can map each one. Assertions are the CORRECTED outcomes (several
 * review2 printouts showed the a472d6a0/ba58ceac defects: C3, C5, P4, P5, R2,
 * R4). E-ENV-1 cases are TRANSLATED to contract
 * FROZEN rev 4 (probes + provenance record, R-7); the R-6/R-4 source-list
 * semantics they originally exercised are withdrawn.
 */

import { describe, expect, it } from 'vitest';
import {
  applyAuthCheck,
  envDecision,
  evaluateEnv,
  evaluateEnvPost,
  sharedEnvValues,
  provenanceKey,
} from '../wave01/records.mjs';

const A = 'https://slot-a.example';
const B = 'https://slot-b.example';
const batchStart = '2026-10-07T16:00:00Z';
const iso = (minutes: number) => new Date(Date.parse(batchStart) + minutes * 60_000).toISOString();
const rel = { slotGeneration: 'g7', baseURL: A };
const W = { batchStart, maxAgeMin: 30 };
const self401 = { status: 401, at: iso(0) };
const PROBES_OFF = {
  api: { status: 401, message: 'development authentication is not enabled', at: iso(0) },
  web: { status: 401, hasIdentity: false, at: iso(0) },
};
const PROBE_ON = {
  ...PROBES_OFF,
  api: { ...PROBES_OFF.api, message: 'invalid development token' },
};
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
  declared_effective_auth_mode: 'unset',
  support: {
    hosted: {
      log_line: 'Server mode: hosted',
      log_ts: '2026-10-07T15:30:05Z',
      process_start_ts: '2026-10-07T15:30:00Z',
      process_start_source: 'proc:/proc/<pid>/stat starttime',
      log_ts_source: 'journal:scion-hub.service',
      slot_generation: 'g7',
    },
    dev_auth: { basis: 'recorded-inputs', dev_auth_warning_present: false },
    auth_mode: { source: 'unset-default' },
  },
} as Record<string, unknown>;

const decl = (o: Record<string, unknown> = {}) => ({
  baseURL: A,
  window_start: iso(-10),
  slotGeneration: 'g7',
  e_env_1_dev_auth_effective: false,
  e_env_1_provenance: PROV,
  e_env_3_test_login_enabled: true,
  e_env_4_runtime_broker_effective: false,
  e_env_4_no_broker_process_or_dispatch: true,
  e_env_2_anon_401: { status: 401, ts: iso(-9) },
  e_env_5: { probe: 'repo-default-secret-challenge', result_status: 401, ts: iso(-8) },
  ...o,
});
const grade = (d: unknown, self = self401, probes: unknown = PROBES_OFF) => {
  const g = evaluateEnv(d, self, rel, W, probes as never);
  return { g, by: Object.fromEntries(g.map((x) => [x.gate, x.outcome])), D: envDecision(g) };
};
const run = {
  startedAt: iso(0),
  endedAt: iso(20),
  slotGeneration: 'g7',
  baseURL: A,
  preBaseURL: A,
  preValues: sharedEnvValues(decl()),
  preProvenanceKey: provenanceKey(PROV),
  preProcessStartTs: '2026-10-07T15:30:00Z',
  testLoginUsed: true,
};
const post = (o: Record<string, unknown> = {}) => ({
  baseURL: A,
  window_start: iso(-1),
  window_end: iso(21),
  serving_process_start_ts: '2026-10-07T15:30:00Z',
  slotGeneration: 'g7',
  e_env_1_dev_auth_effective: false,
  e_env_3_test_login_enabled: true,
  e_env_4_runtime_broker_effective: false,
  e_env_4_no_broker_process_or_dispatch: true,
  ...o,
});

describe('review2 envrepro2 C0–C9 (PRE)', () => {
  it('C0 good bound ⇒ E-ENV-1/2/3/5 pass, E-ENV-4 awaits POST, no stop', () => {
    const r = grade(decl());
    expect(r.by).toEqual({
      'E-ENV-1': 'pass',
      'E-ENV-2': 'pass',
      'E-ENV-3': 'pass',
      'E-ENV-4': 'inconclusive',
      'E-ENV-5': 'pass',
    });
    expect(r.D.stop).toBe(false);
  });
  it('C1 cross-host PRE (B), same gen, own anon probe 200 ⇒ E-ENV-2 FAIL, others INCONCLUSIVE, stop', () => {
    const r = grade(decl({ baseURL: B }), { status: 200, at: iso(0) });
    expect(r.by['E-ENV-2']).toBe('fail');
    expect(r.by['E-ENV-1']).toBe('inconclusive');
    expect(r.D.stop).toBe(true);
  });
  it('C1 (rev 4) cross-host PRE (B), own dev-auth probe ON ⇒ E-ENV-1 FAIL (R-7)', () => {
    expect(grade(decl({ baseURL: B }), self401, PROBE_ON).by['E-ENV-1']).toBe('fail');
  });
  it('C2 cross-host PRE (B) declaring dev-auth ON ⇒ INCONCLUSIVE (unattributable), stop', () => {
    const r = grade(decl({ baseURL: B, e_env_1_dev_auth_effective: true }));
    expect(r.by['E-ENV-1']).toBe('inconclusive');
    expect(r.D).toMatchObject({ stop: true, fails: [] });
  });
  it('C3 bound, steward anon 200 ⇒ E-ENV-2 FAIL (RB1; a472d6a0 wrongly passed)', () => {
    expect(grade(decl({ e_env_2_anon_401: { status: 200, ts: iso(-9) } })).by['E-ENV-2']).toBe(
      'fail'
    );
  });
  it('C4 (translated) bound, incomplete E-ENV-1b record ⇒ E-ENV-1 INCONCLUSIVE', () => {
    const { flags: _f, ...noFlags } = PROV;
    expect(grade(decl({ e_env_1_provenance: noFlags })).by['E-ENV-1']).toBe('inconclusive');
  });
  it('C5 bound PRE test-login=false, runner test-login then succeeds ⇒ E-ENV-3 FAIL (RB3; a472d6a0 wrongly passed)', () => {
    const r = grade(decl({ e_env_3_test_login_enabled: false }));
    expect(r.D).toMatchObject({ stop: true, authCheckPending: true });
    const after = applyAuthCheck(r.g, { succeeded: true, at: iso(0) });
    expect(after.find((x) => x.gate === 'E-ENV-3')!.outcome).toBe('fail');
    expect(envDecision(after).fails).toContain('E-ENV-3');
  });
  it('C6 baseURL missing ⇒ declaration gates INCONCLUSIVE', () => {
    const r = grade(decl({ baseURL: undefined }));
    expect(r.by['E-ENV-1']).toBe('inconclusive');
    expect(r.by['E-ENV-5']).toBe('inconclusive');
  });
  for (const [id, url] of [
    ['C7 trailing slash', `${A}/`],
    ['C8 uppercase host', 'https://SLOT-A.example'],
    ['C9 explicit default port', 'https://slot-a.example:443'],
  ] as const) {
    it(`${id} normalises to the same canonical origin ⇒ PASS`, () => {
      expect(grade(decl({ baseURL: url })).by['E-ENV-1']).toBe('pass');
    });
  }
});

describe('review2 envrepro2 P1–P5 (POST)', () => {
  it('P1 POST-only broker ON, bound ⇒ FAIL', () => {
    expect(evaluateEnvPost(post({ e_env_4_runtime_broker_effective: true }), run).outcome).toBe(
      'fail'
    );
  });
  it('P2 POST host B, broker ON ⇒ INCONCLUSIVE (unattributable, not graded)', () => {
    expect(
      evaluateEnvPost(post({ baseURL: B, e_env_4_runtime_broker_effective: true }), run)
    ).toMatchObject({
      outcome: 'inconclusive',
      attributable: false,
    });
  });
  it('P3 PRE A / POST B (run A) ⇒ INCONCLUSIVE', () => {
    expect(evaluateEnvPost(post({ baseURL: B }), run).outcome).toBe('inconclusive');
  });
  it('P4 PRE test-login true, POST false (same slot) ⇒ FAIL (RB3; a472d6a0 wrongly passed)', () => {
    expect(evaluateEnvPost(post({ e_env_3_test_login_enabled: false }), run).outcome).toBe('fail');
  });
  it('P5 POST without test-login field ⇒ INCONCLUSIVE (RB3; a472d6a0 wrongly passed)', () => {
    expect(evaluateEnvPost(post({ e_env_3_test_login_enabled: undefined }), run).outcome).toBe(
      'inconclusive'
    );
  });
});

describe('review2 envrepro R1–R8 (round-1 repros, current semantics)', () => {
  it('R1 PRE for host-B, same gen ⇒ never PASS; stop', () => {
    const r = grade(decl({ baseURL: 'https://host-b.example' }));
    expect(r.by['E-ENV-1']).toBe('inconclusive');
    expect(r.D.stop).toBe(true);
  });
  it('R2 steward anon probe 200 ⇒ E-ENV-2 FAIL, stop', () => {
    const r = grade(decl({ e_env_2_anon_401: { status: 200, ts: iso(-9) } }));
    expect(r.by['E-ENV-2']).toBe('fail');
    expect(r.D.stop).toBe(true);
  });
  it('R3 (translated) E-ENV-1 evidence incomplete ⇒ INCONCLUSIVE, stop', () => {
    const r = grade(decl({ e_env_1_provenance: { ...PROV, files_examined: [] } }));
    expect(r.by['E-ENV-1']).toBe('inconclusive');
    expect(r.D.stop).toBe(true);
  });
  it('R4 E-ENV-3 test-login false + runner test-login succeeds ⇒ FAIL (same defect as C5)', () => {
    const r = grade(decl({ e_env_3_test_login_enabled: false }));
    expect(r.D.stop).toBe(true);
    const after = applyAuthCheck(r.g, { succeeded: true, at: iso(0) });
    expect(after.find((x) => x.gate === 'E-ENV-3')!.outcome).toBe('fail');
  });
  it('R5 POST for host-B ⇒ INCONCLUSIVE', () => {
    expect(evaluateEnvPost(post({ baseURL: 'https://host-b.example' }), run).outcome).toBe(
      'inconclusive'
    );
  });
  it('R6 POST window covering the run with all required fields ⇒ PASS', () => {
    expect(evaluateEnvPost(post(), run).outcome).toBe('pass');
  });
  it('R7 POST slotGeneration differs ⇒ INCONCLUSIVE (bindingMismatch)', () => {
    const r = evaluateEnvPost(post({ slotGeneration: 'g8' }), run);
    expect(r.outcome).toBe('inconclusive');
    expect(r.problems.join(' ')).toContain('bindingMismatch');
  });
  it('R8 POST without E-ENV-1b record or e_env_5 ⇒ allowed (PRE-only requirements)', () => {
    expect(evaluateEnvPost(post(), run).outcome).toBe('pass');
  });
});

/**
 * Preserved reproduction cases from wl-wave1-runner-review3 round 1
 * (report 7ea732985aaf…5202 + addendum1 c172061b…b54d), byte-verified:
 *   env3.mjs  5421 B c7f8bc5e9c02c9a262f6a36ae93047c31aa3a4557fc60a3e6817ef2fa6058667 (Q1–Q25)
 *   env3b.mjs 2883 B 9ada4fc8e61ad1a0990f5ca1b4d1e3ce07d7ca3c083d4838f3677fc138ee6575 (C1–C4)
 *   env3c.mjs 2154 B f615db77721b9de50961434dc2d4be53b9b64967fdf79b81ee206e8a337dd2ce (Q8, Q8b)
 * Assertions are the outcomes required by assessor rulings R-9 and R-10
 * (e7d074aa wrongly passed Q1–Q8 and C3, and graded C1/C2 INCONCLUSIVE).
 * The fixture here is review3's with the bound host/generation of this file
 * and the owner-required source attribution fields.
 */
describe('review3 R-9 / R-10 cases (corrected outcomes)', () => {
  const P = (patch: (p: Record<string, any>) => void) => {
    const c = JSON.parse(JSON.stringify(PROV)) as Record<string, any>;
    patch(c);
    return decl({ e_env_1_provenance: c });
  };
  const e1 = (d: unknown) => grade(d).by['E-ENV-1'];
  const legacy = (files: Array<Record<string, unknown>>) => (p: Record<string, any>) => {
    p.load_path = 'legacy';
    p.files_examined = files.map((f) => f.file);
    p.path_values = { files };
  };
  const rows: Array<[string, (p: Record<string, any>) => void, string]> = [
    [
      'Q1 settings server.auth.mode=dev, env absent, declared unset ⇒ FAIL (R-9c)',
      (p) => (p.path_values['server.auth.mode'] = 'dev'),
      'fail',
    ],
    [
      'Q2 settings server.auth.mode=proxy, declared unset ⇒ INCONCLUSIVE (R-9b)',
      (p) => (p.path_values['server.auth.mode'] = 'proxy'),
      'inconclusive',
    ],
    [
      'Q3 cites env:SCION_SERVER_AUTH_MODE while env absent ⇒ INCONCLUSIVE (R-9a)',
      (p) => {
        p.declared_effective_auth_mode = 'oauth';
        p.support.auth_mode = { source: 'env:SCION_SERVER_AUTH_MODE' };
      },
      'inconclusive',
    ],
    [
      'Q4 cites settings input on the legacy path ⇒ INCONCLUSIVE (R-9a)',
      (p) => {
        legacy([{ file: 'a.yaml', mode: 'hosted', 'auth.devMode': false, 'auth.mode': 'absent' }])(
          p
        );
        p.declared_effective_auth_mode = 'oauth';
        p.support.auth_mode = { source: 'settings:server.auth.mode' };
      },
      'inconclusive',
    ],
    [
      'Q5 cites settings input recorded absent ⇒ INCONCLUSIVE (R-9a)',
      (p) => {
        p.declared_effective_auth_mode = 'oauth';
        p.support.auth_mode = { source: 'settings:server.auth.mode' };
      },
      'inconclusive',
    ],
    [
      'Q6 cites a legacy file not among the merged files ⇒ INCONCLUSIVE (R-9a)',
      (p) => {
        legacy([{ file: 'a.yaml', mode: 'hosted', 'auth.devMode': false, 'auth.mode': 'absent' }])(
          p
        );
        p.declared_effective_auth_mode = 'oauth';
        p.support.auth_mode = { source: 'legacy-file', file: 'zzz.yaml' };
      },
      'inconclusive',
    ],
    [
      'C1 env SCION_SERVER_AUTH_MODE=dev, declared oauth citing env ⇒ FAIL (R-9c)',
      (p) => {
        p.env.SCION_SERVER_AUTH_MODE = 'dev';
        p.declared_effective_auth_mode = 'oauth';
        p.support.auth_mode = { source: 'env:SCION_SERVER_AUTH_MODE' };
      },
      'fail',
    ],
    [
      'C2 env SCION_SERVER_AUTH_MODE=dev, declared unset ⇒ FAIL (R-9c)',
      (p) => (p.env.SCION_SERVER_AUTH_MODE = 'dev'),
      'fail',
    ],
    [
      'C3 legacy merged file auth.mode=dev, env absent, declared oauth citing b.yaml ⇒ INCONCLUSIVE (R-9c)',
      (p) => {
        legacy([
          { file: 'a.yaml', mode: 'hosted', 'auth.devMode': false, 'auth.mode': 'dev' },
          { file: 'b.yaml', mode: 'hosted', 'auth.devMode': false, 'auth.mode': 'oauth' },
        ])(p);
        p.declared_effective_auth_mode = 'oauth';
        p.support.auth_mode = { source: 'legacy-file', file: 'b.yaml' };
      },
      'inconclusive',
    ],
    [
      'C4 settings server.auth.mode=oauth, declared oauth citing settings ⇒ PASS (positive control)',
      (p) => {
        p.path_values['server.auth.mode'] = 'oauth';
        p.declared_effective_auth_mode = 'oauth';
        p.support.auth_mode = { source: 'settings:server.auth.mode' };
      },
      'pass',
    ],
    [
      'Q7 process start + log after batch start ⇒ INCONCLUSIVE (R-10)',
      (p) => {
        p.support.hosted.process_start_ts = '2026-10-07T17:00:00Z';
        p.support.hosted.log_ts = '2026-10-07T17:00:01Z';
      },
      'inconclusive',
    ],
    [
      'Q8 (actual) log_ts after batch start ⇒ INCONCLUSIVE (R-10; addendum1)',
      (p) => (p.support.hosted.log_ts = '2026-10-09T00:00:00Z'),
      'inconclusive',
    ],
    [
      'Q8b long gap, both before batch start ⇒ PASS (gap ungraded)',
      (p) => {
        p.support.hosted.process_start_ts = '2026-10-01T00:00:00Z';
        p.support.hosted.log_ts = '2026-10-07T15:00:00Z';
      },
      'pass',
    ],
    [
      'source attribution missing (process_start_source) ⇒ INCONCLUSIVE',
      (p) => delete p.support.hosted.process_start_source,
      'inconclusive',
    ],
    [
      'source attribution missing (log_ts_source) ⇒ INCONCLUSIVE',
      (p) => delete p.support.hosted.log_ts_source,
      'inconclusive',
    ],
    [
      'declared batch_start after the runner batch start ⇒ INCONCLUSIVE (cannot move the bound)',
      (p) => (p.support.hosted.batch_start = '2026-10-07T17:00:00Z'),
      'inconclusive',
    ],
    [
      'declared batch_start within [log_ts, runner batch start] ⇒ PASS (advisory)',
      (p) => (p.support.hosted.batch_start = '2026-10-07T15:50:00Z'),
      'pass',
    ],
  ];
  for (const [label, patch, expected] of rows) {
    it(label, () => expect(e1(P(patch))).toBe(expected));
  }
  it('R-10: probes taken before the serving process start ⇒ INCONCLUSIVE', () => {
    const early = {
      api: { ...PROBES_OFF.api, at: '2026-10-07T15:00:00Z' },
      web: { ...PROBES_OFF.web, at: '2026-10-07T15:00:01Z' },
    };
    expect(grade(decl(), self401, early).by['E-ENV-1']).toBe('inconclusive');
  });
});

describe('review3 RB-2 continuity (R-10) on POST', () => {
  it('POST serving_process_start_ts missing ⇒ INCONCLUSIVE', () => {
    expect(evaluateEnvPost(post({ serving_process_start_ts: undefined }), run).outcome).toBe(
      'inconclusive'
    );
  });
  it('POST serving_process_start_ts different (restart) ⇒ INCONCLUSIVE, not FAIL', () => {
    const r = evaluateEnvPost(post({ serving_process_start_ts: '2026-10-07T16:10:00Z' }), run);
    expect(r.outcome).toBe('inconclusive');
    expect(r.problems.join(' ')).toContain('restarted');
  });
  it('POST record from a different process start ⇒ INCONCLUSIVE (restart), not an env-value FAIL', () => {
    const restarted = JSON.parse(JSON.stringify(PROV));
    restarted.support.hosted.process_start_ts = '2026-10-07T16:10:00Z';
    restarted.support.hosted.log_ts = '2026-10-07T16:10:05Z';
    const r = evaluateEnvPost(
      post({ serving_process_start_ts: undefined, e_env_1_provenance: restarted }),
      run
    );
    expect(r.outcome).toBe('inconclusive');
  });
  it('same-process POST record with a different environment value ⇒ FAIL (R-3)', () => {
    const changed = JSON.parse(JSON.stringify(PROV));
    changed.files_examined = ['./other.yaml'];
    expect(evaluateEnvPost(post({ e_env_1_provenance: changed }), run).outcome).toBe('fail');
  });
});
