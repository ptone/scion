# Layout survey — Wave01 measurement runner

Attach-only measurement runner for the Wave01 campaign, graded under
**`wave01-contract-FROZEN-rev3.md`** (44319 B, sha256
`69ec8b81af27c0bbb5be53a4cdf0722c98022368d6ebc947213431aeb03d6680`). It
supersedes rev 2 (`1877b1d4…5447`) and rev 1 (`f7143415…e8ae`). No Wave01
evidence exists under either.

- Groups-table clauses: pilot finding rev 2 (`8a287ba4…56ec572fc`).
- Helper-seeded rows: helper excerpt (`d03ca22a…1ea40`).
- Interpretation rulings: R-1 (APP shell = `scion-app`, folded into rev 3
  §1a) and R-2 (Release distinctness, below).

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
  "e_env_1_sources": {
    "unit/args": { "--dev-auth": false, "--hosted": true, "--production": "absent" },
    "settings": {
      "global": "absent",
      "local": { "auth.devMode": false, "auth.mode": "absent", "mode": "hosted" }
    },
    "environment": {
      "SCION_SERVER_AUTH_DEVMODE": "absent",
      "SCION_SERVER_AUTH_MODE": "absent",
      "SCION_SERVER_MODE": "absent"
    },
    "keys_checked": ["<concrete key names inspected>"]
  },
  "e_env_3_test_login_enabled": true,
  "e_env_4_runtime_broker_effective": false,
  "e_env_4_no_broker_process_or_dispatch": true,
  "e_env_2_anon_401": { "status": 401, "ts": "…Z" },
  "e_env_5": { "probe": "repo-default-secret-challenge", "result_status": 401, "ts": "…Z" }
}
```

`window_ts` is accepted as an alias for `window_start`. Every "absent" must be
written explicitly. A missing key is never defaulted. The settings key-path
spelling is pending ii2's source read.

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

### E-ENV-1 derivation (rulings R-4 and R-6)

The per-layer raw values are applied through the served backend's
precedence. It was source-checked at 1694e511, which matches backend
4a253489:

| Step         | Rule                                                                                                                            | Source                                                                    |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| config value | `SCION_SERVER_*` env > local settings > global settings > embedded default (`auth.devMode=false`; `auth.mode` and `mode` unset) | `pkg/config/hub_config.go:966-970, 1036-1043, 1536-1546, 1817-1822`       |
| hosted       | `--hosted`/`--production` if either is set, else config `mode` ∈ {hosted, production}                                           | `cmd/server.go:235-236`, `cmd/server_foreground.go:1013-1018`             |
| devMode      | explicit `--dev-auth`; else ON if non-hosted (workstation default); else config devMode                                         | `server_foreground.go:1020-1024, 1064-1066`, `cmd/server_config.go:35-36` |
| auth.mode    | config `auth.mode` (`"dev"` = exclusive dev human auth)                                                                         | `hub_config.go:582-588`                                                   |

A missing input is unknown, never defaulted. E-ENV-1 is graded in the
assessor's order, and the first match wins:

1. **Unbound** declaration ⇒ INCONCLUSIVE.
2. **FAIL** if any of:
   - the chain derives devMode ON;
   - effective `auth.mode = "dev"`;
   - the declared `e_env_1_dev_auth_effective` is `true`.

   This holds even when other classes are missing, as long as they cannot
   change the result. A declared-vs-derived disagreement is recorded with
   the FAIL.

3. **INCONCLUSIVE** if any of:
   - a class or key that could change the result is missing or malformed;
   - the chain cannot be derived;
   - hosted mode is not evidenced;
   - the declared value is missing.
4. **PASS** only if all three classes are explicit, the chain derives OFF,
   hosted mode is evidenced, and declared = `false`.

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
- carry `e_env_3_test_login_enabled`, `e_env_4_runtime_broker_effective` and
  `e_env_4_no_broker_process_or_dispatch`.

E-ENV-1 is optional on POST (R-4). If present, it must derive cleanly and its
layer values must equal PRE's.

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
   `--out` writes an immutable `wave01-validation` record. It binds the
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

### validate-run checks

- both SHAs on every record, and every file's sha256;
- E-MAIN pre/post and the loaded main entry;
- the E-ENV PRE gates, the post-batch anonymous probe and the POST
  declaration;
- every expected record exists;
- every complete M0 has exactly one M0-primary full-page screenshot.

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
  shadow/slot DOM. It covers the B1 negative control, the rev 3 (c) positive
  control, R-5 NBSP/U+3000 preservation, accessible names and wheel
  positioning.
- `wave01-loopback.selftest.pw.ts`: runs the **real** `wave01.pw.ts` as a
  child process in debug mode against a 127.0.0.1 node server, which is not a
  hub. It checks the clean path, the SAFETY priority, readback failure as
  per-substep capture errors, and an honest aborted run on test-login failure.

Run all of the above with
`CHROMIUM_EXECUTABLE=… npx playwright test -c e2e/layout-survey/playwright.wave01-selftest.config.ts`.

## Contract → code coverage (rev 3)

- **impl** = implemented and unit/self-tested.
- **BLOCKED** = not implemented at this commit. The affected states emit
  BLOCKED records, never placeholders.

| Clause                                                                                                                                                                             | Code                                                                                                                            | Status                                                                                                                                          |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| §1 profiles, DPR 1, light, en-US, UTC; fresh context per (state, profile, substep)                                                                                                 | `contract.ts`; `runner-lib.openSubstep`; M1 Shift+Tab retry in its own context                                                  | impl                                                                                                                                            |
| §1 tolerance 1px                                                                                                                                                                   | `contract.TOL`                                                                                                                  | impl                                                                                                                                            |
| §1 shadow/slot walk; deep hit test                                                                                                                                                 | `probe` deepAll/flatParent/deepPoint/composedContains                                                                           | impl (self-test)                                                                                                                                |
| §1 K(e) + axis-aware CLIP; scrollers, offscreen ≠ clipped, reachability                                                                                                            | `probe.chainEntry`, `evaluate.clip` (pending + inner-scroller deferral), `runner-lib.positionStep`, `resolveObservations`       | impl                                                                                                                                            |
| §1 programmatic positioning (labelled; never reachability)                                                                                                                         | `probe` `programmatic-scroll`; resolution ⇒ inconclusive                                                                        | impl                                                                                                                                            |
| §1 actionable target                                                                                                                                                               | `contract.ACTIONABLE`, `probe.isActionable`                                                                                     | impl                                                                                                                                            |
| §1 rendered-text comparison (rev 3)                                                                                                                                                | `evaluate.renderedTextEquals` on `innerText` + computed `white-space`; raw readback recorded                                    | impl for B-A3 (real-Chromium control)                                                                                                           |
| §1 FULL(e, v) generic rule                                                                                                                                                         | — (S01/S02 use the pilot B-A3 shapes)                                                                                           | BLOCKED (A-L3/A-T1, S03+)                                                                                                                       |
| §1a APP shell `scion-app` (R-1 erratum)                                                                                                                                            | `contract.APP_SHELL_TAG`                                                                                                        | impl                                                                                                                                            |
| §1b policies, POL-SR                                                                                                                                                               | `contract.POLICIES`, `probe.policiesFor`, `probe.isSrOnly`                                                                      | impl                                                                                                                                            |
| §1b hide-mobile hidden at P1                                                                                                                                                       | raw `hideMobile`/display; B-OVR lists hidden columns                                                                            | grading BLOCKED with S07/S10/S11/S13/S14                                                                                                        |
| A-D1, A-C1, A-S1/A-S2 (page layer), A-N2                                                                                                                                           | `evalAD1/AC1/AS1/AS2/AN2` (+ live accessible names)                                                                             | impl                                                                                                                                            |
| §2c active layer / background inert / M0-underlay                                                                                                                                  | —                                                                                                                               | BLOCKED (S05, A-N1 P1)                                                                                                                          |
| A-N1 (M2)                                                                                                                                                                          | —                                                                                                                               | BLOCKED (S04, S05)                                                                                                                              |
| A-F1 (rev 3: body start asserted, Tab/Shift+Tab, stop rule, focus-outside-document, composed-descendant target; indicator (a) outline, (b) box-shadow, (c) non-outline style diff) | `groups.traverse`, `evaluate.pressChecks`/`evalAF1`                                                                             | impl (real-Chromium negative + positive controls). BLOCKED with S05/S07/S08/S15: start points after preparation actions, and drawer focus scope |
| A-L1…A-L5, A-T1…A-T3                                                                                                                                                               | —                                                                                                                               | BLOCKED (S03–S15)                                                                                                                               |
| B-C1, B-A1, B-A2, B-A3, B-OVR, B-I1 (pilot finding rev 2 + rev 3 rendered-text rule)                                                                                               | `evalBC1/BA1/BA2/BA3/BOVR`, `groups.runM3`                                                                                      | impl (S01/S02)                                                                                                                                  |
| §2a measured interactions / forbidden activation                                                                                                                                   | `pointerClick` (refuses on a failed hit test); SAFETY non-GET check with priority                                               | impl                                                                                                                                            |
| §2a R0 (no networkidle), readback-bound expectations                                                                                                                               | `waitR0`, guarded `groups.readback`                                                                                             | impl                                                                                                                                            |
| §2b M0-primary + M0-pos-k (same context), M1, M3                                                                                                                                   | `groups.runM0/runM1Forward/runM1Backward/runM3`                                                                                 | impl; M2 BLOCKED                                                                                                                                |
| §3 capture errors never graded; quarantine after 3 (immediate, in-batch)                                                                                                           | `wave01.pw.ts`; `records.applyBatch/isQuarantined/clearQuarantine`                                                              | impl                                                                                                                                            |
| §4 counting                                                                                                                                                                        | `expectedRecords` = 15 × 3 × substeps; one M0 per state × profile                                                               | impl (S01/S02 captured; others BLOCKED records)                                                                                                 |
| §5 fixtures                                                                                                                                                                        | groups via API + readback; companion `fixtureHelper`                                                                            | impl for groups; helper states BLOCKED                                                                                                          |
| §5a E-ENV-1…5                                                                                                                                                                      | `records.evaluateEnv` (PRE, window-bound), `envDecision`, post anonymous probe, `evaluateEnvPost` via `validate-run --env-post` | impl                                                                                                                                            |
| §5b E-REL (pair; R-2 distinctness)                                                                                                                                                 | `release.mjs` buildBase/buildCompanion/validatePair/validateDistinctPairs                                                       | impl                                                                                                                                            |
| §5b E-MAIN, E-RUN, attach-only                                                                                                                                                     | `wave01.pw.ts`, `suite-digest.mjs`, `playwright.wave01.config.ts`                                                               | impl                                                                                                                                            |
| §5b E-XFER, E-ID, E-CRED retirement, E-PUB                                                                                                                                         | steward / assessor processes; the runner records a value-free issuance inventory                                                | outside runner                                                                                                                                  |
