# Layout survey — Wave01 measurement runner

Attach-only measurement runner for the Wave01 campaign, graded under
**`wave01-contract-FROZEN-rev8.md`** (54230 B, sha256
`f2d4f52308582daf4dd7b44d5143aa724f630e1091e99acd608463261d9e4f5e`). It
supersedes rev 7 (`3e3662f5…4008`), rev 6 (`67218ac2…5f42`, never
implemented), rev 5 (`9df504e0…288d`), rev 4 (`0fcc579e…c855`), rev 3
(`69ec8b81…6680`), rev 2 (`1877b1d4…5447`) and rev 1 (`f7143415…e8ae`). No
Wave01 evidence exists under any of them. Rev 5–8 change only the A-F1 focus
indicator rule (see below). Ruling R-6 is withdrawn; R-11 is superseded.

- Groups-table clauses: pilot finding rev 2 (`8a287ba4…56ec572fc`).
- Helper-seeded rows: helper excerpt (`d03ca22a…1ea40`).
- Interpretation rulings, folded into rev 4/5:
  - R-1: APP shell = `scion-app`.
  - R-2/R-3: slot binding.
  - R-5: whitespace set.
- Rulings R-7 to R-10 and R-12 to R-19 (binding on rev 8, §5a/§0 unchanged):
  - R-7: per-artifact E-ENV-1 attribution.
  - R-8: support for each declared value.
  - R-9: auth_mode support consistent with the record.
  - R-10: process/log/probe timing and POST continuity.
  - R-12: an attributable POST compares environment values before
    completeness.
  - R-13: missing post-batch evidence ⇒ INCONCLUSIVE; `batchValid` is derived.
  - R-14: shared declared booleans are slot-level (a PRE/POST difference is
    FAIL even across a restart); the provenance record is process-level (not
    compared after a restart).
  - R-15: a top-level environment field missing from the PRE or POST record
    is not recorded on that side ⇒ not compared ⇒ INCONCLUSIVE (the comparison
    key omits it; a legacy `'<missing>'` sentinel is never a value).
  - R-16: first-match precedence inside the rev 8 A-F1 indicator rule.
  - R-17: the A-F1 record requirements (full U1/U2, per-node names, element
    indicator values).
  - R-18: outline removed on focus ⇒ INCONCLUSIVE; animations on the
    element's own `::before`/`::after` are in the running scope.
  - R-19: the per-direction sampling (O-3) and class-name keys (O-4) conform;
    their residuals are documented, never covered.

This is measurement tooling only. It changes no product code. The pilot
runner (`capture.pw.ts`, `playwright.config.ts`) is unchanged.

## Files

| Path                                                                   | Role                                                                                                                                                       |
| ---------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `playwright.wave01.config.ts` → `wave01.pw.ts`                         | Measured, attach-only batch. It has no globalSetup/globalTeardown/webServer and no `page.route`. It never builds, starts, seeds, resets or stops anything. |
| `wave01/contract.ts`                                                   | Pinned digests, profiles, tolerance, policy table (§1b), named scrollers, actionable definition, APP shell tag.                                            |
| `wave01/manifest.ts`                                                   | The 15 frozen states (§4a). Each has `adapter: implemented \| blocked`.                                                                                    |
| `wave01/probe.ts`                                                      | Self-contained in-page RAW probe. It only reads layout. The single exception is the labelled `programmatic-scroll` fallback (§2b).                         |
| `wave01/evaluate.ts`                                                   | Pure clause evaluators over raw JSON, so the assessor can recompute any outcome.                                                                           |
| `wave01/groups.ts`                                                     | W01-S01/S02 adapter: guarded in-batch readback, M0-primary + M0-pos-k, M1 (A-F1), M3 (B-I1).                                                               |
| `wave01/runner-lib.ts`                                                 | Fresh context per substep, R0, native wheel/pointer/keyboard input, screenshots, raw files, accessible names.                                              |
| `wave01/suite-digest.mjs`                                              | §5b E-RUN suite digest and capture-host checks.                                                                                                            |
| `wave01/release.mjs`                                                   | Steward tooling: base Release (`releaseKind: verification`), `wave01-release-ext` companion, `validate-pair`, `validate-distinct`.                         |
| `wave01/records.mjs`                                                   | E-ENV PRE/POST gates, capture-record validation, `validate-run`, E7 quarantine ledger.                                                                     |
| `wave01/config.ts`                                                     | Environment loader (evidence vs debug-non-evidence).                                                                                                       |
| `playwright.wave01-selftest.config.ts` → `wave01-probe.selftest.pw.ts` | **Local non-evidence** real-Chromium self-test on a synthetic shadow/slot DOM.                                                                             |
| `unit/wave01-*.test.ts`                                                | Hub-free unit tests.                                                                                                                                       |

## Suite digest (§5b E-RUN; canonical form confirmed by the assessor)

The digest covers tracked blobs at the runner commit under
`web/e2e/layout-survey/` and `web/e2e/harness/`. There are no extension paths
outside those roots.

- Each file contributes one line: `"<sha256>␠␠<repo-relative path>\n"`.
- Whole lines are sorted in `LC_ALL=C` byte order.
- The digest is the sha256 of the concatenated lines.

```bash
node web/e2e/layout-survey/wave01/suite-digest.mjs --commit <runner commit> [--manifest]
# equivalent: git ls-files web/e2e/layout-survey/ web/e2e/harness/ | xargs sha256sum | LC_ALL=C sort | sha256sum
```

A non-regular entry (symlink, gitlink) under the covered paths stops the
digest; the contract leaves that case undefined.

## E-ENV evidence (§5a): PRE and POST declarations

The steward (ii2) writes two value-free JSON declarations per capture window.
They contain key names, booleans and UTC timestamps only.

```json
{
  "baseURL": "https://<slot host>",
  "window_start": "…Z",
  "window_end": "…Z (POST only)",
  "slotGeneration": "…",
  "e_env_1_dev_auth_effective": false,
  "e_env_1_provenance": {
    "flags": { "--hosted": true, "--production": "absent", "--dev-auth": false },
    "load_path": "settings-global | settings-local | legacy",
    "files_examined": ["<paths inspected>"],
    "path_values": {
      "server.mode": "hosted",
      "server.auth.dev_mode": false,
      "server.auth.mode": "absent"
    },
    "env": {
      "SCION_SERVER_MODE": "absent",
      "SCION_SERVER_AUTH_DEVMODE": "absent",
      "SCION_SERVER_AUTH_MODE": "absent",
      "SCION_SERVER_AUTH_DEV_MODE": "absent"
    },
    "declared_effective_hosted": true,
    "declared_effective_auth_mode": "unset",
    "support": {
      "hosted": {
        "log_line": "Server mode: hosted",
        "log_ts": "…Z",
        "log_ts_source": "journal:<unit> | file:<log path>",
        "process_start_ts": "…Z",
        "process_start_source": "proc:/proc/<pid>/stat starttime | systemd:<unit> ActiveEnterTimestamp",
        "slot_generation": "…"
      },
      "dev_auth": { "basis": "recorded-inputs", "dev_auth_warning_present": false },
      "auth_mode": { "source": "unset-default" }
    }
  },
  "e_env_3_test_login_enabled": true,
  "e_env_4_runtime_broker_effective": false,
  "e_env_4_no_broker_process_or_dispatch": true,
  "e_env_2_anon_401": { "status": 401, "ts": "…Z" },
  "e_env_5": { "probe": "repo-default-secret-challenge", "result_status": 401, "ts": "…Z" }
}
```

`window_ts` is accepted as an alias for `window_start`. Every "absent" must be
written explicitly. A missing key is never defaulted.

On the legacy load path, `path_values` is
`{ "files": [ { "file", "mode", "auth.devMode", "auth.mode" }, … ] }`, listed in
merge order. ii2 source-verified this record shape against backend 4a253489
(16:38Z). **Support for each declared value (ruling R-8; `support`, required):**

- **hosted:** the current process start's log line `Server mode: hosted`
  (`cmd/server_foreground.go:240-244`).
  - **Required fields:** `log_ts`, `log_ts_source`, `process_start_ts`,
    `process_start_source` and `slot_generation`. The two source tags are
    checked for presence only; their content is the steward's attestation.
  - **Time binding (R-10):** `process_start_ts ≤ log_ts ≤ batch start`, where
    batch start is the runner's own recorded start (`run.json` `startedAt`).
    Both P-API/P-WEB probes must be timestamped after the process start.
    `slot_generation` must equal this slot's generation.
  - **Advisory `batch_start`:** optional. If present it must fall in
    [log_ts, runner start], and it never moves the bound.
  - **Gap:** the delay between process start and the log line is recorded,
    not graded.
  - **Outcomes:** `Server mode: workstation (…)` ⇒ FAIL. A missing,
    unattributed or unbound line ⇒ INCONCLUSIVE.
- **dev_auth:** `basis: "recorded-inputs"` (the steward's determination from
  the flags, load path values and env), plus `dev_auth_warning_present`.
  The startup `WARNING: Development authentication enabled` line (:313-320)
  present ⇒ FAIL. Its absence is supporting only and never proves OFF; the
  probes decide OFF.
- **auth_mode:** `source` is one of `settings:server.auth.mode`
  (`settings_v1.go:3280`), `legacy-file` (with `file`),
  `env:SCION_SERVER_AUTH_MODE`, or `unset-default`.
  - **Consistency with the same record (R-9):**
    - A cited input must be recorded PRESENT on the load path actually taken,
      or in env, and its value must equal the declared mode. A settings citation
      is invalid while `SCION_SERVER_AUTH_MODE` is set, because env is applied
      last.
    - A legacy citation must name one of the merged files.
    - `unset` with `unset-default` (the default noted at
      `hub_config.go:583-584`) requires every recorded auth.mode input to be
      absent.
    - Otherwise ⇒ INCONCLUSIVE.
  - **FAIL established by the record itself:**
    - `SCION_SERVER_AUTH_MODE=dev`;
    - on the settings path, with env absent, the selected file's
      `server.auth.mode=dev`;
    - declared `"dev"`.
  - **Legacy path:** a merged file set to `dev` with env absent ⇒ INCONCLUSIVE
    (resolving merge order is not allowed), unless `dev` is declared.

Any required declaration without its support ⇒ record incomplete ⇒
INCONCLUSIVE.

`env` is an overlay applied on every load path, so it is always filled in;
`load_path` names the file source. `run.json` embeds the value-free PRE
declaration, which is checked by `assertValueFree` at load.

### Binding (rulings R-2/R-3)

Each declaration must carry `baseURL`, the served target, as a canonical
http(s) origin:

- configured hostname, never a raw IP;
- no path and no credentials;
- compared by exact parsed `origin` equality, the same way the Release pair
  is validated;
- no `host` alias and no substring matching.

PRE.baseURL must equal POST.baseURL, which must equal the run's served
baseURL. `slotGeneration` must equal the base Release's.

- **Not bound** (another origin, PRE and POST on different hosts, or another
  generation): the declaration is unattributable. Every gate it feeds is
  **INCONCLUSIVE** whatever its values, and it is recorded as `crossHost` or
  `bindingMismatch`.
- **FAIL** only for contradictions of the environment:
  - the runner's own probes;
  - a correctly bound declaration reporting a forbidden state;
  - same-slot PRE and POST disagreeing on an environment value.

Either outcome stops capture, and validate-run rejects the run.

### E-ENV-1 (contract rev 8 §5a, unchanged from rev 4; rulings R-7 to R-10, R-12 to R-15)

All citations are at 1694e511, which matches served backend 4a253489.

**E-ENV-1a: runner probes**, sent to the run's own served origin before
capture (as a gate) and again after the batch. No real credential is used.

- **P-API:** `GET /api/v1/groups` with `Authorization: Bearer scion_dev_<64 fresh random hex>`.
  The token is generated per probe and never recorded.
  - 401 `"development authentication is not enabled"` ⇒ OFF.
  - `"invalid development token"` or any 2xx ⇒ ON.
  - Anything else ⇒ unexplained.
  - Sources: `pkg/hub/auth.go:467-477`, `detectTokenType` :686-689,
    `writeError` JSON in `errors.go:284-304`.
- **P-WEB:** a cookie-less `GET /auth/me`.
  - 401 without a user identity ⇒ OFF.
  - Any returned identity ⇒ ON.
  - Anything else ⇒ unexplained.
  - Sources: `web.go:949`, :2773-2800; `devAuthMiddleware` :1896-1945,
    wired at :2969.

**E-ENV-1b: steward provenance record** of the actual serving process. It
holds the process flags, the load path taken (`hub_config.go:1059-1080`
settings path, where global and local are alternatives; :1180+ legacy merge),
that path's values, the camelCase serve-path env keys (:1536-1546,
:1817-1822), `SCION_SERVER_AUTH_DEV_MODE` as present/absent, and the declared
effective hosted mode, dev-auth and auth.mode.

The runner checks the record for binding, completeness and contradictions,
and **never resolves precedence**. Contradiction checks are limited to inputs
at the top of precedence:

- an explicit `--dev-auth` vs the declared dev-auth (applied last,
  `server_foreground.go:1064-1066`);
- an explicit `--hosted`/`--production` vs the declared hosted mode
  (:1013-1018);
- a set `SCION_SERVER_AUTH_MODE` vs the declared auth.mode. The env value is
  last on both load paths, and no CLI flag or later code writes `Auth.Mode`
  (the only writer is `settings_v1.go:3280`).

Under R-9 the runner also checks `auth.mode` file values: a selected
settings file with `server.auth.mode=dev` (env absent) ⇒ FAIL; a legacy
merged file with `auth.mode=dev` ⇒ INCONCLUSIVE (the merge order is not
resolved); the cited `support.auth_mode` source must be a recorded, present
input on the taken load path. Other lower-layer values (`server.mode`,
`server.auth.dev_mode`, legacy `mode`/`auth.devMode`) are recorded but not
checked; the probes are the decisive OFF proof.

**Grading (R-7, per artifact).** Probe results are attributable by
construction. A record bound to another host or generation is excluded and
counts as missing.

1. Either probe ON ⇒ **FAIL**, whatever any record says.
2. The attributable record shows dev-auth ON, hosted false, explicit
   `--dev-auth=true`, or declared auth.mode `"dev"` ⇒ **FAIL**.
3. **INCONCLUSIVE** if any of:
   - a probe is missing or unexplained;
   - the record is missing, unbound, incomplete or contradictory.
4. **PASS** only if both probes are OFF and an attributable, complete record
   declares hosted true and dev-auth OFF.

After the batch, a probe ON ⇒ E-ENV-1 FAIL and the batch is invalid.
Missing or unexplained post-batch probes ⇒ INCONCLUSIVE in validate-run.

### Other gates

- **E-ENV-2:** decided by the runner's own anonymous probe, which must be
  taken at or after window_start with `redirect: manual`; a redirect is not a 401. On a bound declaration, a steward `e_env_2_anon_401.status ≠ 401` is a
  FAIL regardless of its timestamp. Whether that timestamp falls in the window
  is recorded only; it is an implementation choice, not a frozen rule. A
  missing steward probe is recorded, and the runner's own probe still decides.
- **E-ENV-3:** a bound PRE with `e_env_3_test_login_enabled: true` passes. A
  bound PRE declaring `false` makes the runner attempt its own test-login.
  Success is a contradiction (FAIL); refusal means capture is impossible
  (INCONCLUSIVE). The batch stops either way.
- **E-ENV-4:** the PRE part needs effective `false` and no-dispatch `true`.
  It then becomes `awaitingPost`.
- **E-ENV-5:** `e_env_5.ts` must lie in [window_start, batch start]. A status
  other than 401 or 403 on a bound declaration is a FAIL, and the steward
  sends a private security report.

### PRE declaration

`LAYOUT_SURVEY_ENV_DECLARATION_FILE` is graded before capture.

- `LAYOUT_SURVEY_ENV_MAX_AGE_MIN` (default 30) is runner hygiene only, not a
  contract parameter. It never replaces the in-window probes or the POST
  proof, and it is recorded in run.json.
- Any FAIL, or any missing or stale evidence in evidence mode, stops the
  batch before capture.
- After the batch, the runner probes anonymous 401 again. A non-401 result
  invalidates the batch.

### POST declaration

The steward writes the POST declaration after the batch. It must:

- cover `run.json` `startedAt..endedAt`;
- carry the same `baseURL` and `slotGeneration`;
- carry `serving_process_start_ts`, or a POST record whose
  `support.hosted.process_start_ts` is used instead, equal to PRE's process
  start (R-10 continuity). Missing ⇒ INCONCLUSIVE. Different ⇒ the serving
  process restarted during the batch ⇒ INCONCLUSIVE, not FAIL;
- carry `e_env_3_test_login_enabled`, `e_env_4_runtime_broker_effective` and
  `e_env_4_no_broker_process_or_dispatch`.

The E-ENV-1b record is optional on POST. If present, it must be
non-forbidden and complete, and its **environment values** must equal PRE's:
`flags`, `load_path`, `files_examined`, `path_values`, `env`,
`declared_effective_hosted`, `declared_effective_auth_mode` (review4 RB4-1).
Order (ruling R-12):

1. **Attribution first.** The record is compared only if it is bound to the
   same host and `slotGeneration` and shows the same serving process
   (`serving_process_start_ts` equals PRE's process start, and the record's
   own `support.hosted.process_start_ts`, if present, agrees). Otherwise ⇒
   not compared ⇒ INCONCLUSIVE (R-3/R-10). Incompleteness in unrelated
   fields does not make it unattributable.
2. **Compare before completeness.** Leaf by leaf, every value recorded on
   both sides (an explicit `absent` is a value) that differs ⇒ **FAIL**,
   listed by path (e.g. `env.SCION_SERVER_MODE`). A key recorded on only one
   side, at any level including a whole top-level field missing from PRE
   (R-15), is not a value: it is not compared ⇒ INCONCLUSIVE unless another
   FAIL applies. Arrays (`files_examined`, legacy merged files) compare whole.
3. **Then completeness.** Missing or invalid fields are recorded alongside
   (`missing:`) and never downgrade a FAIL.

`support.*` is supporting metadata (source text, timestamp precision,
advisory `batch_start`): a PRE/POST difference there is recorded as a note,
never a FAIL (an invalid support field still makes the record incomplete ⇒
INCONCLUSIVE). `run.json` `envPreProvenanceKey` is the environment-only
canonical form; `envPreSupportKey` is recorded only.

These are FAIL:

- any shared boolean (`e_env_1_dev_auth_effective`,
  `e_env_3_test_login_enabled`, `e_env_4_*`) differing between same-slot PRE
  and POST;
- a POST declaring test-login disabled while the runner authenticated via
  test-login during the batch.

`validate-run --env-post` makes the POST declaration mandatory: missing ⇒
INCONCLUSIVE, contradicted ⇒ FAIL.

## Operator recipe (steward/capturer = ii2)

Prerequisites:

- The slot is launched as described in the pilot README: hosted, test-login,
  runtime broker OFF, **no** `--dev-auth`, slot-specific session secret.
- The capture host has a private, clean checkout of the reviewed runner
  commit, plus `npm ci`, Chromium and fonts.

Steps:

1. **Seed groups (steward, separate, unmeasured):** the pilot
   `playwright.seed.config.ts` writes the groups-v1 fixture map. Projects,
   users and helper rows come in phase 2.
2. **Base Release (per slot generation and phase):**
   `node e2e/layout-survey/wave01/release.mjs base --out base.json <pilot records.mjs release flags>`.
3. **Companion (frozen before capture):**
   `node e2e/layout-survey/wave01/release.mjs companion --out companion.json --base base.json --phase baseline|candidate --browser-version <chromium full version> (--font-image-digest <hex> | --font-manifest-from-fc-list) --native-chat true|false --terminal-workspace true|false --groups-fixture-map <map> --credential-inventory <value-free json> --fixture-helper <H1 json>|none --steward <id>`,
   then `release.mjs validate-pair --base base.json --companion companion.json`.
   Baseline and each candidate need **distinct** pairs (ruling R-2):
   - distinct ids and file sha256s;
   - host-distinct baseURLs (a different port alone does not count);
   - the slotGeneration value may repeat across hosts, because slot identity
     is (host, slotGeneration).

   Check with
   `release.mjs validate-distinct --base-a … --companion-a … --base-b … --companion-b …`.

4. **PRE env declaration** for this window (see above).
5. **Capture:**
   ```bash
   cd web
   LAYOUT_SURVEY_BASE_URL=… LAYOUT_SURVEY_SESSION_SECRET_FILE=… LAYOUT_SURVEY_PRIVATE_DIR=… \
   LAYOUT_SURVEY_OPERATOR=ii2 LAYOUT_SURVEY_ADMIN_EMAIL=… LAYOUT_SURVEY_FIXTURE_MAP=… \
   LAYOUT_SURVEY_EVIDENCE_DIR=… LAYOUT_SURVEY_STATE_DIR=… \
   LAYOUT_SURVEY_BASE_RELEASE_FILE=base.json LAYOUT_SURVEY_COMPANION_FILE=companion.json \
   LAYOUT_SURVEY_ENV_DECLARATION_FILE=env-pre.json [LAYOUT_SURVEY_ENV_MAX_AGE_MIN=30] \
   LAYOUT_SURVEY_REVIEWED_RUNNER_COMMIT=<reviewed sha> LAYOUT_SURVEY_REVIEWED_SUITE_DIGEST=<digest> \
   [LAYOUT_SURVEY_STATES=W01-S01,W01-S02] [CHROMIUM_EXECUTABLE=…] \
   npx playwright test -c e2e/layout-survey/playwright.wave01.config.ts
   ```
   The batch **stops before capture**, recording the reasons in run.json, if
   any of these checks fail:
   - capture-host check: HEAD = reviewed commit, `web/e2e` clean with no
     untracked files, and no diff against the reviewed commit;
   - suite digest and file count equal the reviewed values;
   - the Release pair is valid;
   - the E-ENV PRE gates pass;
   - the fixture map sha256 equals the companion's.
6. **POST env declaration** covering the run's `startedAt..endedAt`.
7. **Validate:**
   `node e2e/layout-survey/wave01/records.mjs validate-run <run dir> --base base.json --companion companion.json --env-post env-post.json --out validation.json`.
   validate-run **recomputes** the PRE gates from inputs embedded in `run.json`:
   the raw declaration bytes (whose digest must equal `envDeclarationSha256`),
   the self anonymous probe, the P-API/P-WEB results and the auth-check
   attempt. Any disagreement with the recorded `run.env` ⇒ mismatch. Every PRE
   comparison input used against POST (`envPreBaseURL`, `envPreValues`,
   `envPreProvenanceKey`, `envPreSupportKey`, `envPreProcessStartTs`) is
   **derived from the embedded raw declaration** (owner O-a); the run.json
   copies are only cross-checked, and a differing copy ⇒ mismatch. POST
   problems keep per-problem labels even when the aggregate is FAIL: graded
   contradictions are `mismatch:`, missing/unattributable ones `missing:`
   (owner O-d).
   `--out` writes an immutable `wave01-validation` record. It also names the
   validator's own runner commit and suite digest, plus `clean` and
   `cleanProblems` (review4 RB4-2). The digest names HEAD's tracked blobs,
   so it identifies the executing validator only when the covered tree is
   clean (no modified, staged or untracked files under `web/e2e`). A dirty
   or unverifiable validator adds `missing: validator identity unverified`,
   so the record can never be VALID; re-validate on a clean checkout of the
   reviewed commit. It binds the
   run.json sha256, every run file's sha256, both Release digests and the POST
   digest, and carries the verdict and classification: VALID, INCONCLUSIVE, or
   REJECTED (FAIL or invalid). **Only a passing validation record is final
   environment-validity evidence.** run.json `batchValid` is provisional.
   Coverage counts per state are printed and recorded separately from
   validity.
   Then seal and publish with the pilot `scripts/bundle.mjs publish`.
8. **Quarantine:** the ledger is
   `$LAYOUT_SURVEY_STATE_DIR/wave01-quarantine-ledger.json`. It is steward
   state, not evidence.
   - Three consecutive capture-error substeps quarantine a state
     **immediately**. Its remaining substeps in the same batch, and every
     later batch, emit BLOCKED records.
   - Only the owner clears it:
     `records.mjs quarantine-clear --ledger … --state W01-Sxx --by <owner> --reason <text>`.

**Debug (non-evidence):** `LAYOUT_SURVEY_DEBUG_NON_EVIDENCE=1` allows missing
Release, env and review inputs. In that mode:

- records and run are stamped `debug-non-evidence`;
- the run directory is named `debug-run-…`;
- the ledger is not updated;
- `validate-run` rejects the run.

## Records

### Capture records

There is one `wave01-capture` record for **every** (state, profile, substep)
in the frozen manifest, named `<state>.<P>.<M>.capture.json` and listed in
`run.json.expectedRecords`. Its status is `complete`, `capture-error` or
`blocked`. A blocked record always gives its reason: no adapter, not selected
for this batch, or quarantined.

Each record carries:

- the contract sha, both Release SHAs and ids, slotGeneration, releaseKind
  and phase;
- the runner HEAD and suite digest;
- main.js expected/pre/post, and `batchValid` (written after the
  post-checks);
- readiness checks and actions: every native input, labelled positioning
  steps with their offsets, and the M1 native body start point;
- `outcomes` (clause, source, outcome, policyIds, raw details), with no
  unresolved `pending`;
- files with sha256: raw JSON per step, M0-primary full-page and viewport
  screenshots, supporting screenshots;
- the readback (endpoint, status, body sha256, principal, time);
- for M0, the loaded script URLs and the loaded `/assets/main.js` sha256.

### Provisional and aborted records

Each finished substep is also written immediately as an immutable
`.provisional.json`.

If the batch aborts on an exception, a `finally` block still writes the final
records gathered so far and `run.json` with `aborted: true` and
`batchValid: false`. A pre-capture exception writes a minimal aborted
run.json, because the run directory is created first.

A Playwright test timeout or a killed process is not guaranteed to reach the
`finally` block (review2 N-f). In that case only the `.provisional.json` copies
survive, and the run has no valid run.json, so it can never validate.
`validate-run` rejects aborted runs.

The runner exits non-zero whenever any substep is a capture error, matching
the pilot convention. All records are written before it exits.

### Capture errors

- A capture error carries `errorReason` and a diagnostic screenshot, and
  never graded outcomes.
- A failed or exceptional readback is a capture error for each affected
  substep.
- A SAFETY violation (any non-GET request during measurement) is checked in
  a `finally`. It takes priority over any other error reason and adds a
  `safetyReport`.

### A-F1 focus indicator (rev 8 §2, rulings R-16, R-18, R-19)

**Sampled nodes.** For every focus candidate the probe samples the element,
its `::before` and `::after` pseudo-elements, and every flat-tree ancestor up
to the document root, keyed by node (`path`, `path::before`, `path::after`).
Before the first key press of each M1 direction, `runner-lib.sampleFocusBaseline`
takes two unfocused samples U1 and U2 of every sampled node of every
focusable element, separated by the same settle used after every Tab (two
animation frames), with no input in between. At each press the probe takes
F for the focused element's sampled nodes.

**Running animations (R-18 Q-B, R-20 + addendum; review8 RB8-1).** The
scope is exactly the sampled nodes: any animation (CSS animation, CSS
transition or script Web Animation) in play state `running` whose effect
target is the focused element, its own `::before`/`::after`, or a flat-tree
ancestor (own animations only). Descendants of the focused element and
ancestors' pseudo-elements are out of scope. The collector is the union of:
the element's `getAnimations({ subtree: true })` filtered to effects
targeting it; each ancestor's own `getAnimations()`; and the
`getAnimations()` of each in-scope node's OWN root node (Document or
ShadowRoot, never the slot host's), filtered to targets in scope with a
pseudo-element only `::before`/`::after` of the focused element. The union is
deduplicated per Animation. The registry part catches script animations on
an own pseudo-element created before its box existed (or after the box was
removed and recreated), in light DOM, shadow trees and slotted content.
Entries are recorded as `path` or `path::before`/`path::after`. (Counting
other own pseudo-elements such as `::marker` from the subtree collector is
conservative over-inclusion, R-20 Q2.)

**Per press, first match wins (R-16):**

1. **PASS:** U1/U2 present and identical on every sampled node, nothing
   running at F, and on the element itself an outline (style not none and
   width above 0) or a box-shadow that is ABSENT in U2 and PRESENT in F
   (`decidedBy` `outline-appears` / `box-shadow-appears`).
2. **INCONCLUSIVE**, any listed condition:
   - no baseline for some sampled node (even with an outline on F); a press
     without the pseudo/ancestor F samples counts as no baseline (review7
     O-1);
   - any U1 ≠ U2;
   - a running animation/transition at F;
   - an outline or box-shadow present in BOTH U2 and F (persistent, whether
     or not it changes);
   - an outline-\* change while the element's outline is present in U2 or F,
     e.g. an outline removed on focus (R-18 Q-A);
   - any non-outline U2→F difference on the element;
   - any U2→F difference on a pseudo-element or ancestor (e.g.
     `:focus-within`).
3. **FAIL** (`static-no-indicator`): none of the above, i.e. F = U2 on every
   sampled node except outline-\* changes on the element while its outline is
   none or width 0 in BOTH U2 and F (UA outline-offset noise).
4. Anything else ⇒ INCONCLUSIVE (never PASS, never a defect by itself; open
   coverage).

**Records.** The M1 raw record (`M1-*.presses`) keeps the full U1/U2 maps for
every sampled node and, per press, the full element F, `sampledNodes`, the
differing property names per node for U1/U2 (`nodeDiffsU1U2`) and U2/F
(`nodeDiffsU2F`), the element's own outline-\* and box-shadow VALUES in U2
and F (`indicatorValuesU2`/`indicatorValuesF`) and `runningAnimations`.
Full per-press ancestor/pseudo F maps are not persisted (assessor 18:14Z).
The per-press decision (`result`, `decidedBy`, `baselineMissing`,
`undeterminableReasons`) is in the capture record's A-F1 outcome details
(`outcomes[].details.presses`), not in the raw presses file (review7 O-2). A
visibility or hit-test failure FAILs the press whatever the indicator; in
the aggregate FAIL dominates, then INCONCLUSIVE. The runner never repairs
focus or scroll.

**Known residual limitations (ruling R-19; never claimed as covered).**

- O-3: U1/U2 are sampled once per M1 direction, before the first press. An
  indicator PASS means "absent in the per-direction baseline U2 and present
  in F, with a stable baseline and nothing running". It is not proof that
  focus caused the change: a non-focus-caused outline or box-shadow first
  appearing on the element between U2 and its press would also meet it. This
  conforms to rev 8 and is listed by the assessor as an A-F1 residual
  ("focus causality" open coverage).
- O-4: node keys include up to three class names per segment, so a class
  toggled on focus changes the key. The node then has no matching U1/U2
  baseline ⇒ missing baseline ⇒ INCONCLUSIVE, never PASS or FAIL (a key
  mismatch is never read as "no difference").

### Evidence immutability policy (owner O-3)

Every run artifact is write-once: JSON records (raw, provisional, capture,
run.json) are created exclusively (`wx`, never overwritten) with mode 0444;
screenshots are set to 0444 right after capture; and when run.json (always
the last artifact) is written, every regular file in the run directory is
sealed to 0444. Aborted and stopped runs are sealed too. The run directory
stays operator-writable so a separate `validate-run --out` record (also
`wx`, 0444) can be added. File modes are hygiene; integrity is established by
the sha256 bindings that validate-run checks. The loopback selftests assert
0444 on every run file in every mode.

### validate-run checks

- both SHAs on every record, and every file's sha256;
- E-MAIN pre/post and the loaded main entry;
- the E-ENV PRE gates, the post-batch anonymous probe and the POST
  declaration;
- every expected record exists;
- every complete M0 has exactly one M0-primary full-page screenshot.

- **Classification (ruling R-13):** missing or unexplained evidence is
  `missing:` (INCONCLUSIVE), and only contradictions are `mismatch:`
  (REJECTED).
  - Missing/unexplained: post-batch P-API/P-WEB not obtained or outside the
    classified set (e.g. HTTP 500), a post-batch anonymous probe with no
    response (absent, status 0, fetch error), a post-batch main.js not
    obtained.
  - Contradictions: a probe showing ON, an observed non-401 anonymous status,
    a different main.js digest, bound forbidden declarations, same-slot
    environment differences (R-12), hash/binding mismatches.
  - `batchValid` is a derived flag. Its causes are re-checked independently
    and classified there. The flag itself is a mismatch only if it is not a
    boolean, if a capture copy (or `batchInvalidReason`) differs from
    run.json, or if it is false with no independently established cause.

## Known limits (documented, review1 non-blocking)

- **N2:** mutation detection uses page `request` events. It does not cover
  service-worker requests or WebSocket frames. The groups pages at 1694e511
  use neither to mutate.
- **N9:** the capture-host check relies on `git status`, which does not show
  gitignored files under `web/e2e`. Pinned code cannot import them.
- **N11:** `run.json` embeds the synthetic admin principal in the issuance
  inventory. That is fine as private evidence; keep it out of public
  summaries (E-PUB).
- SAFETY would turn any background non-GET request into a capture error. No
  such call exists in the shell or admin-groups source at 1694e511. The monitor
  is context-wide, so popups and new pages are included (review2 N-e).
- **N-b:** the PRE max age (30 min) is runner hygiene. The owner and assessor
  say it is not a grading threshold, and it never replaces the in-window
  probe or the POST proof.

## Local non-evidence tests

- `unit/wave01-*.test.ts` (vitest): the evaluators, the records/E-ENV logic,
  and a table-driven E-ENV matrix (`wave01-env-table.test.ts`).
- `wave01-probe.selftest.pw.ts`: real-Chromium probe controls on a synthetic
  shadow/slot DOM. It covers the rev 8 / R-16 A-F1 controls: a focus ring
  or box-shadow appearing on focus ⇒ PASS (incl. the shadow-hosted button);
  permanent outline/box-shadow ⇒ INCONCLUSIVE; static `outline:none`/
  `outline:0` ⇒ FAIL; `:focus-within` wrapper outline and `::after` focus
  underline ⇒ INCONCLUSIVE with the change recorded on that node; no
  baseline + ring ⇒ INCONCLUSIVE; stable background/`font-weight`/
  `border-left-width` changes ⇒ INCONCLUSIVE; review4 O-b alternate,
  review5 O-1 `steps(1)` and 60 s linear animations, and animation plus a
  font-weight focus style ⇒ INCONCLUSIVE. It also covers
  R-5 NBSP/U+3000 preservation, accessible names and wheel
  positioning.
- `wave01-loopback.selftest.pw.ts`: runs the **real** `wave01.pw.ts` as a
  child process in debug mode against a 127.0.0.1 node server, which is not a
  hub. It checks the clean path, the SAFETY priority, readback failure as
  per-substep capture errors, an honest aborted run on test-login failure, and
  a dev-auth-ON server (the E-ENV-1a probes stop the batch with E-ENV-1 FAIL and
  never record the probe token).

Run all of the above with
`CHROMIUM_EXECUTABLE=… npx playwright test -c e2e/layout-survey/playwright.wave01-selftest.config.ts`.

## Contract → code coverage (rev 8)

- **impl** = implemented and unit/self-tested.
- **BLOCKED** = not implemented at this commit. The affected states emit
  BLOCKED records, never placeholders.

| Clause                                                                                                                                                                                                                                                                                                                                                        | Code                                                                                                                            | Status                                                                                                                                          |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| §1 profiles, DPR 1, light, en-US, UTC; fresh context per (state, profile, substep)                                                                                                                                                                                                                                                                            | `contract.ts`; `runner-lib.openSubstep`; M1 Shift+Tab retry in its own context                                                  | impl                                                                                                                                            |
| §1 tolerance 1px                                                                                                                                                                                                                                                                                                                                              | `contract.TOL`                                                                                                                  | impl                                                                                                                                            |
| §1 shadow/slot walk; deep hit test                                                                                                                                                                                                                                                                                                                            | `probe` deepAll/flatParent/deepPoint/composedContains                                                                           | impl (self-test)                                                                                                                                |
| §1 K(e) + axis-aware CLIP; scrollers, offscreen ≠ clipped, reachability                                                                                                                                                                                                                                                                                       | `probe.chainEntry`, `evaluate.clip` (pending + inner-scroller deferral), `runner-lib.positionStep`, `resolveObservations`       | impl                                                                                                                                            |
| §1 programmatic positioning (labelled; never reachability)                                                                                                                                                                                                                                                                                                    | `probe` `programmatic-scroll`; resolution ⇒ inconclusive                                                                        | impl                                                                                                                                            |
| §1 actionable target                                                                                                                                                                                                                                                                                                                                          | `contract.ACTIONABLE`, `probe.isActionable`                                                                                     | impl                                                                                                                                            |
| §1 rendered-text comparison (rev 4, R-5)                                                                                                                                                                                                                                                                                                                      | `evaluate.renderedTextEquals` on `innerText` + computed `white-space`; raw readback recorded                                    | impl for B-A3 (real-Chromium control)                                                                                                           |
| §1 FULL(e, v) generic rule                                                                                                                                                                                                                                                                                                                                    | — (S01/S02 use the pilot B-A3 shapes)                                                                                           | BLOCKED (A-L3/A-T1, S03+)                                                                                                                       |
| §1a APP shell `scion-app` (R-1 erratum)                                                                                                                                                                                                                                                                                                                       | `contract.APP_SHELL_TAG`                                                                                                        | impl                                                                                                                                            |
| §1b policies, POL-SR                                                                                                                                                                                                                                                                                                                                          | `contract.POLICIES`, `probe.policiesFor`, `probe.isSrOnly`                                                                      | impl                                                                                                                                            |
| §1b hide-mobile hidden at P1                                                                                                                                                                                                                                                                                                                                  | raw `hideMobile`/display; B-OVR lists hidden columns                                                                            | grading BLOCKED with S07/S10/S11/S13/S14                                                                                                        |
| A-D1, A-C1, A-S1/A-S2 (page layer), A-N2                                                                                                                                                                                                                                                                                                                      | `evalAD1/AC1/AS1/AS2/AN2` (+ live accessible names)                                                                             | impl                                                                                                                                            |
| §2c active layer / background inert / M0-underlay                                                                                                                                                                                                                                                                                                             | —                                                                                                                               | BLOCKED (S05, A-N1 P1)                                                                                                                          |
| A-N1 (M2)                                                                                                                                                                                                                                                                                                                                                     | —                                                                                                                               | BLOCKED (S04, S05)                                                                                                                              |
| A-F1 (rev 8 + R-16/R-18: body start asserted, Tab/Shift+Tab, stop rule, focus-outside-document, composed-descendant target; sampled nodes = element + ::before/::after + flat-tree ancestors; PASS only for an outline/box-shadow appearing on focus with a stable baseline and nothing running; listed INCONCLUSIVE conditions; FAIL only when fully static) | `groups.traverse`, `evaluate.pressChecks`/`evalAF1`                                                                             | impl (real-Chromium negative + positive controls). BLOCKED with S05/S07/S08/S15: start points after preparation actions, and drawer focus scope |
| A-L1…A-L5, A-T1…A-T3                                                                                                                                                                                                                                                                                                                                          | —                                                                                                                               | BLOCKED (S03–S15)                                                                                                                               |
| B-C1, B-A1, B-A2, B-A3, B-OVR, B-I1 (pilot finding rev 2 + rev 4 rendered-text rule)                                                                                                                                                                                                                                                                          | `evalBC1/BA1/BA2/BA3/BOVR`, `groups.runM3`                                                                                      | impl (S01/S02)                                                                                                                                  |
| §2a measured interactions / forbidden activation                                                                                                                                                                                                                                                                                                              | `pointerClick` (refuses on a failed hit test); SAFETY non-GET check with priority                                               | impl                                                                                                                                            |
| §2a R0 (no networkidle), readback-bound expectations                                                                                                                                                                                                                                                                                                          | `waitR0`, guarded `groups.readback`                                                                                             | impl                                                                                                                                            |
| §2b M0-primary + M0-pos-k (same context), M1, M3                                                                                                                                                                                                                                                                                                              | `groups.runM0/runM1Forward/runM1Backward/runM3`                                                                                 | impl; M2 BLOCKED                                                                                                                                |
| §3 capture errors never graded; quarantine after 3 (immediate, in-batch)                                                                                                                                                                                                                                                                                      | `wave01.pw.ts`; `records.applyBatch/isQuarantined/clearQuarantine`                                                              | impl                                                                                                                                            |
| §4 counting                                                                                                                                                                                                                                                                                                                                                   | `expectedRecords` = 15 × 3 × substeps; one M0 per state × profile                                                               | impl (S01/S02 captured; others BLOCKED records)                                                                                                 |
| §5 fixtures                                                                                                                                                                                                                                                                                                                                                   | groups via API + readback; companion `fixtureHelper`                                                                            | impl for groups; helper states BLOCKED                                                                                                          |
| §5a E-ENV-1 (rev 4: P-API/P-WEB probes pre + post; E-ENV-1b provenance completeness/contradictions; R-7 per-artifact)                                                                                                                                                                                                                                         | `wave01.pw.ts` `devAuthProbes`; `records.classifyDevAuthProbes/checkProvenance/gradeEEnv1`; post probes in `validate-run`       | impl (unit tables + real-runner loopback dev-auth-ON control)                                                                                   |
| §5a E-ENV-2…5, slot binding                                                                                                                                                                                                                                                                                                                                   | `records.evaluateEnv` (PRE, window-bound), `envDecision`, post anonymous probe, `evaluateEnvPost` via `validate-run --env-post` | impl                                                                                                                                            |
| §5b E-REL (pair; R-2 distinctness)                                                                                                                                                                                                                                                                                                                            | `release.mjs` buildBase/buildCompanion/validatePair/validateDistinctPairs                                                       | impl                                                                                                                                            |
| §5b E-MAIN, E-RUN, attach-only                                                                                                                                                                                                                                                                                                                                | `wave01.pw.ts`, `suite-digest.mjs`, `playwright.wave01.config.ts`                                                               | impl                                                                                                                                            |
| §5b E-XFER, E-ID, E-CRED retirement, E-PUB                                                                                                                                                                                                                                                                                                                    | steward / assessor processes; the runner records a value-free issuance inventory                                                | outside runner                                                                                                                                  |
