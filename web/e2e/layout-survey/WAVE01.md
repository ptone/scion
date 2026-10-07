# Layout survey — Wave01 measurement runner

Attach-only measurement runner for the Wave01 campaign, graded under
**`wave01-contract-FROZEN-rev2.md`** (42455 B, sha256
`1877b1d40a5e4bf04d87e47d419a4451cac5ccb2d3f27f9a63d8ce32c88a5447`). The
groups-table clauses come from pilot finding rev 2 (`8a287ba4…56ec572fc`).
Helper-seeded rows follow the helper excerpt (`d03ca22a…1ea40`).

This is measurement tooling only. It changes no product code. The pilot
runner (`capture.pw.ts`, `playwright.config.ts`) is unchanged.

## Files

| Path                                                                   | Role                                                                                                                                                       |
| ---------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `playwright.wave01.config.ts` → `wave01.pw.ts`                         | Measured, attach-only batch. It has no globalSetup/globalTeardown/webServer and no `page.route`. It never builds, starts, seeds, resets or stops anything. |
| `wave01/contract.ts`                                                   | Pinned digests, profiles, tolerance, policy table (§1b), named scrollers, actionable definition.                                                           |
| `wave01/manifest.ts`                                                   | The 15 frozen states (§4a). Each state's `adapter` is `implemented` or `blocked`.                                                                          |
| `wave01/probe.ts`                                                      | Self-contained in-page RAW probe. It only reads layout. The one exception is the labelled `programmatic-scroll` fallback (§2b).                            |
| `wave01/evaluate.ts`                                                   | Pure clause evaluators over raw JSON. The assessor can recompute any outcome from stored records.                                                          |
| `wave01/groups.ts`                                                     | W01-S01/S02 adapter: in-batch readback, M0-primary + M0-pos-k, M1 (A-F1), M3 (B-I1).                                                                       |
| `wave01/runner-lib.ts`                                                 | Fresh context per substep, R0 readiness, native wheel/pointer/keyboard input, screenshots, raw files.                                                      |
| `wave01/suite-digest.mjs`                                              | §5b E-RUN suite digest and capture-host checks.                                                                                                            |
| `wave01/release.mjs`                                                   | Steward tooling for the base Release (`releaseKind: verification`), the `wave01-release-ext` companion, and `validate-pair`.                               |
| `wave01/records.mjs`                                                   | E-ENV gates, capture-record validation, `validate-run`, and the E7 quarantine ledger.                                                                      |
| `wave01/config.ts`                                                     | Environment loader (evidence vs debug-non-evidence).                                                                                                       |
| `playwright.wave01-selftest.config.ts` → `wave01-probe.selftest.pw.ts` | **Local non-evidence** probe self-test on a synthetic shadow/slot DOM.                                                                                     |
| `unit/wave01-*.test.ts`                                                | Hub-free unit tests.                                                                                                                                       |

## Suite digest (§5b E-RUN; canonical form confirmed by the assessor)

Files are the tracked blobs at the runner commit under `web/e2e/layout-survey/`
and `web/e2e/harness/`. There are no extension paths outside those roots. Each
line is `"<sha256>␠␠<repo-relative path>\n"`. Whole lines are sorted in
`LC_ALL=C` byte order, and the digest is the sha256 of the concatenation.

```bash
node web/e2e/layout-survey/wave01/suite-digest.mjs --commit <runner commit> [--manifest]
# equivalent: git ls-files web/e2e/layout-survey/ web/e2e/harness/ | xargs sha256sum | LC_ALL=C sort | sha256sum
```

A symlink, gitlink or other non-regular entry under the covered paths stops the
run. Rev 2 does not define these cases.

## Operator recipe (steward/capturer = ii2)

Prerequisites: the same slot launch as the pilot README (hosted, test-login,
runtime broker OFF, **no** `--dev-auth`, slot-specific session secret). The
capture host also needs a private checkout of `web/` at the reviewed runner
commit, `npm ci`, Chromium and fonts.

1. **Seed groups (steward, separate, unmeasured):** pilot
   `playwright.seed.config.ts` produces the groups-v1 fixture map. Projects,
   users and helper rows come in phase 2.
2. **Base Release (per slot generation and phase):**
   `node e2e/layout-survey/wave01/release.mjs base --out base.json <pilot records.mjs release flags>`.
   This is the pilot builder unchanged, with `releaseKind` forced to
   `verification` and `scenarioSuiteSha` set to the E-RUN digest. The covered
   paths must be clean.
3. **Companion (frozen before capture):**
   `node e2e/layout-survey/wave01/release.mjs companion --out companion.json --base base.json --phase baseline|candidate --browser-version <chromium full version> (--font-image-digest <hex> | --font-manifest-from-fc-list) --native-chat true|false --terminal-workspace true|false --groups-fixture-map <map> --credential-inventory <value-free json> --fixture-helper <H1 json>|none --steward <id>`
   Then run `release.mjs validate-pair --base base.json --companion companion.json`.
4. **Env declaration** for this capture window: a JSON file with key names
   and booleans only (`window_ts, slotGeneration, e_env_1_dev_auth_effective,
e_env_1_sources, e_env_3_test_login_enabled, e_env_4_runtime_broker_effective,
e_env_4_no_broker_process_or_dispatch, e_env_2_anon_401{status,ts},
e_env_5{probe,result_status,ts}`). E-ENV-5 is a steward probe. The runner
   itself probes E-ENV-2 (anonymous 401, no redirect-follow).
5. **Capture:**
   ```bash
   cd web
   LAYOUT_SURVEY_BASE_URL=… LAYOUT_SURVEY_SESSION_SECRET_FILE=… LAYOUT_SURVEY_PRIVATE_DIR=… \
   LAYOUT_SURVEY_OPERATOR=ii2 LAYOUT_SURVEY_ADMIN_EMAIL=… LAYOUT_SURVEY_FIXTURE_MAP=… \
   LAYOUT_SURVEY_EVIDENCE_DIR=… LAYOUT_SURVEY_STATE_DIR=… \
   LAYOUT_SURVEY_BASE_RELEASE_FILE=base.json LAYOUT_SURVEY_COMPANION_FILE=companion.json \
   LAYOUT_SURVEY_ENV_DECLARATION_FILE=env.json \
   LAYOUT_SURVEY_REVIEWED_RUNNER_COMMIT=<reviewed sha> LAYOUT_SURVEY_REVIEWED_SUITE_DIGEST=<digest> \
   [LAYOUT_SURVEY_STATES=W01-S01,W01-S02] [CHROMIUM_EXECUTABLE=…] \
   npx playwright test -c e2e/layout-survey/playwright.wave01.config.ts
   ```
   Before any capture, the batch **stops** (writing `run.json` with the
   reasons) on any of the following:
   - the capture-host check fails (HEAD ≠ reviewed, dirty or untracked
     `web/e2e`, or a diff against the reviewed commit);
   - the suite digest ≠ the reviewed digest;
   - the Release pair is invalid;
   - E-ENV evidence is missing or contradicted;
   - the fixture map ≠ the companion.
6. **Validate:** run `node e2e/layout-survey/wave01/records.mjs validate-run <run dir> --base base.json --companion companion.json`,
   then seal and publish the run with the pilot `scripts/bundle.mjs publish`.
7. **Quarantine:** the ledger is `$LAYOUT_SURVEY_STATE_DIR/wave01-quarantine-ledger.json`
   (steward state, not evidence). Three consecutive capture-error substeps for a
   state quarantine it, and later batches emit BLOCKED records for it. Only the
   owner clears it:
   `records.mjs quarantine-clear --ledger … --state W01-Sxx --by <owner> --reason <text>`.

**Debug (non-evidence):** `LAYOUT_SURVEY_DEBUG_NON_EVIDENCE=1` allows missing
Release, env and review inputs. Records and the run are stamped
`debug-non-evidence`, the run directory is `debug-run-…`, the ledger is not
updated, and `validate-run` rejects the run.

## Records

One `wave01-capture` record is written per (state, profile, substep):
`<state>.<P>.<M>.capture.json`. Each record carries:

- the contract sha, both Release SHAs and IDs, slotGeneration, releaseKind
  and phase;
- the runner HEAD and suite digest;
- main.js expected/pre/post and `batchValid` (written after the post-check);
- readiness and actions, including every native input, labelled
  positioning steps and their offsets;
- `outcomes` (clause, source, outcome, policyIds, raw details), with no
  unresolved `pending`;
- files with sha256: raw JSON per step, M0-primary full-page and viewport
  screenshots, M0-pos-k and M1/M3 supporting screenshots;
- readback (endpoint, status, body sha256, principal, time);
- for M0, the loaded script URLs and the loaded `/assets/main.js` sha256.

Capture errors carry `errorReason` and a diagnostic screenshot, and never
carry graded outcomes. Any non-GET request observed during measurement is a
SAFETY capture error. BLOCKED records name the reason: no adapter, or
quarantined.

## Contract → code coverage (rev 2)

Status values:

- **impl** = implemented and unit/self-tested.
- **impl (S01/S02)** = implemented for this slice.
- **BLOCKED** = not implemented at this commit. The affected states emit
  BLOCKED records and are never placeholders.

| Clause                                                                                                                      | Code                                                                                                                                                                                 | Status                                                                                                      |
| --------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------- |
| §1 profiles, DPR 1, light, en-US, UTC; fresh context per (state, profile, substep)                                          | `contract.ts` PROFILES/ENVIRONMENT; `runner-lib.openSubstep`; `wave01.pw.ts` `open()` (M1 Shift+Tab retry in its own context)                                                        | impl                                                                                                        |
| §1 tolerance 1px                                                                                                            | `contract.TOL`, used by every evaluator                                                                                                                                              | impl                                                                                                        |
| §1 shadow/slot walk; deep hit test                                                                                          | `probe` deepAll/flatParent/deepPoint/composedContains                                                                                                                                | impl (self-test)                                                                                            |
| §1 K(e) + axis-aware CLIP                                                                                                   | `probe.chainEntry` + `evaluate.clip`                                                                                                                                                 | impl                                                                                                        |
| §1 scrollers / offscreen ≠ clipped / reachability                                                                           | `evaluate.clip` pending + inner-scroller deferral; `runner-lib.positionStep`; `evaluate.resolveObservations`                                                                         | impl                                                                                                        |
| §1 programmatic positioning (labelled; never reachability)                                                                  | `probe` `programmatic-scroll`; resolution ⇒ inconclusive                                                                                                                             | impl                                                                                                        |
| §1 actionable target                                                                                                        | `contract.ACTIONABLE`, `probe.isActionable`                                                                                                                                          | impl                                                                                                        |
| §1 FULL(e, v) generic rule                                                                                                  | — (S01/S02 use pilot B-A3 shapes)                                                                                                                                                    | BLOCKED (needed by A-L3/A-T1, S03+)                                                                         |
| §1a APP shell                                                                                                               | `contract.APP_SHELL_TAG = 'scion-app'` (assessor ruling R-1: the contract text says `scion-app-shell`, but the cited app-shell.ts:67 defines `scion-app`, and the file:line governs) | impl                                                                                                        |
| §1b policies (V/H/E/CLAMP/COLLAPSED)                                                                                        | `contract.POLICIES`, matched by host + selector/part in `probe.policiesFor`                                                                                                          | impl                                                                                                        |
| §1b POL-SR (rev 2)                                                                                                          | `probe.isSrOnly`; exempt in `clip`, `hitTest`, `evalAC1`; raw `srOnly: true` kept                                                                                                    | impl                                                                                                        |
| §1b hide-mobile hidden at P1 (must be display:none)                                                                         | raw `hideMobile`, display recorded; B-OVR lists hidden columns                                                                                                                       | impl for S01/S02; grading clause BLOCKED with S07/S10/S11/S13/S14                                           |
| A-D1                                                                                                                        | `evalAD1`                                                                                                                                                                            | impl                                                                                                        |
| A-C1                                                                                                                        | `probe` overflow scan + `evalAC1`                                                                                                                                                    | impl                                                                                                        |
| A-S1 (page layer)                                                                                                           | `evalAS1`                                                                                                                                                                            | impl                                                                                                        |
| A-S2 (page layer)                                                                                                           | `evalAS2` (nesting = DOM or box containment)                                                                                                                                         | impl                                                                                                        |
| A-N2 (rev 2)                                                                                                                | `probe` `nav` op + Playwright accessible name + `evalAN2`                                                                                                                            | impl                                                                                                        |
| Active layer / background inert / M0-underlay (§2c)                                                                         | —                                                                                                                                                                                    | BLOCKED (S05, A-N1 P1)                                                                                      |
| A-N1 (M2)                                                                                                                   | —                                                                                                                                                                                    | BLOCKED (S04, S05)                                                                                          |
| A-F1 (rev 2: start, Tab/Shift+Tab, stop rule, focus-outside-document, target reached, indicator incl. unfocused-style diff) | `groups.traverse`, `evaluate.pressChecks`/`evalAF1`                                                                                                                                  | impl (body start). Preparation-action start points and drawer focus scope are BLOCKED with S05/S07/S08/S15. |
| A-L1…A-L5, A-T1…A-T3                                                                                                        | —                                                                                                                                                                                    | BLOCKED (S03–S15)                                                                                           |
| B-C1, B-A1, B-A2, B-A3, B-OVR, B-I1 (pilot finding rev 2; IDs from in-batch readback)                                       | `evalBC1/BA1/BA2/BA3/BOVR`, `groups.runM3`                                                                                                                                           | impl (S01/S02)                                                                                              |
| §2a measured interactions (hit test → pointer click at centre)                                                              | `runner-lib.pointerClick` (refuses on a failed hit test)                                                                                                                             | impl. S01/S02 have no preparation interactions.                                                             |
| §2a forbidden activation                                                                                                    | Only the hit-tested long-name link is clicked (M3); a non-GET request ⇒ SAFETY capture error                                                                                         | impl                                                                                                        |
| §2a R0 (no networkidle)                                                                                                     | `runner-lib.waitR0` (+ supporting `document.fonts.ready`), readback-bound count                                                                                                      | impl                                                                                                        |
| §2a expected values from in-batch readback                                                                                  | `groups.readback` (steward map ↔ readback drift ⇒ capture error)                                                                                                                     | impl                                                                                                        |
| §2b M0-primary (+ one stability window) and M0-pos-k in the same context                                                    | `groups.runM0`                                                                                                                                                                       | impl                                                                                                        |
| §2b M1, M3                                                                                                                  | `groups.runM1Forward/Backward`, `groups.runM3`                                                                                                                                       | impl                                                                                                        |
| §2b M2                                                                                                                      | —                                                                                                                                                                                    | BLOCKED (S04/S05)                                                                                           |
| §3 capture error never graded; quarantine after 3 consecutive                                                               | `wave01.pw.ts`; `records.applyBatch/isQuarantined/clearQuarantine`                                                                                                                   | impl                                                                                                        |
| §4 counting: 45 primary = M0-primary screenshots                                                                            | one M0 record per state × profile                                                                                                                                                    | impl (S01/S02 = 6). The others are BLOCKED records.                                                         |
| §5 fixtures C-FX1…5, helper provenance                                                                                      | groups via API + readback; companion `fixtureHelper` (H1 fields or not-used)                                                                                                         | impl for groups. Helper-dependent states are BLOCKED.                                                       |
| §5a E-ENV-1…5                                                                                                               | `records.evaluateEnv` (steward declaration + runner anonymous 401), `envStop`                                                                                                        | impl                                                                                                        |
| §5b E-REL (base + companion, both SHAs pinned)                                                                              | `release.mjs` buildBase/buildCompanion/validatePair; `records.validateRun`                                                                                                           | impl                                                                                                        |
| §5b E-MAIN (pre/post + loaded main entry)                                                                                   | `wave01.pw.ts` fetch pre/post; M0 `loadedMainEntry` from the page's own response                                                                                                     | impl                                                                                                        |
| §5b E-RUN (suite digest, capture-host checks, attach-only)                                                                  | `suite-digest.mjs`; `playwright.wave01.config.ts`                                                                                                                                    | impl                                                                                                        |
| §5b E-XFER, E-ID, E-CRED retirement, E-PUB                                                                                  | steward / assessor processes. The runner records a value-free issuance inventory in `run.json`; values stay in the 0600 private dir.                                                 | outside runner                                                                                              |

Known risk: if the app ever issues a background non-GET request on load, the
SAFETY check turns every substep into a capture error. No such call exists in
the shell or admin-groups source at 1694e511.
