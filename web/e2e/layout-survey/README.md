# Layout survey — Phase-1 runner (`/admin/groups`)

> Wave01 (15 states × 3 profiles, contract FROZEN rev 4) uses a separate
> runner: see [`WAVE01.md`](WAVE01.md). Everything below is the unchanged
> Phase-1 pilot runner.

Attach-only capture of the real Hub `/admin/groups` page at 390×844,
820×1180 and 1440×900, plus the steward seed, the four manual records
(Release, Capture, Finding, Verification), a credential scan, and immutable
bundle publication. Design: web-layout campaign `design.md`
(sha256 `33b72101…306603a`), Phase 1.

| File | Who runs it | What it does |
|---|---|---|
| `playwright.seed.config.ts` → `seed.steward.ts` | steward only | test-login as the synthetic admin; `createGroup` ×3 from `fixtures/groups-v1.json`; real list-API readback; writes the fixture map. Refuses to re-seed. |
| `playwright.config.ts` → `capture.pw.ts` | capturer | attach-only: no globalSetup/teardown/webServer, never builds/starts/seeds/resets/stops. Readiness, screenshots, geometry assertions, one interaction, Capture records, `run.json`. |
| `scripts/records.mjs` | steward / anyone | `release` builds a Release record from real artifacts; `validate` checks any of the four records. |
| `scripts/bundle.mjs` | capturer | `scan` (paths/counts only) and `publish` (scan → SHA256SUMS → immutable copy → re-verify → tar.gz + sha256). |
| `records/*.template.json` | assessor / grader | Finding and Verification skeletons. The developer never authors these. |
| `fault-injection/FI-1-table-min-width.patch` | steward (fallback only) | Labeled TEST-ONLY perturbation for a distinct candidate build. Never shipped. |

Unit tests (no Hub): `npx vitest run -c e2e/layout-survey/vitest.config.ts`.
Typecheck: `node_modules/.bin/tsc -p e2e/layout-survey/tsconfig.json` (local TypeScript; not the unrelated `tsc` npm package).

## Environment (all required; empty values are errors)

| Variable | Used by | Meaning |
|---|---|---|
| `LAYOUT_SURVEY_BASE_URL` | both | host-scoped origin of the slot (no path) |
| `LAYOUT_SURVEY_SESSION_SECRET_FILE` | both | path to a **0600** file with the slot's session secret (read in-process; never argv/log) |
| `LAYOUT_SURVEY_PRIVATE_DIR` | both | **0700** directory for storage state + value-bearing credential inventory. Never evidence. |
| `LAYOUT_SURVEY_OPERATOR` | both | identity of whoever runs the command |
| `LAYOUT_SURVEY_ADMIN_EMAIL` | both | synthetic admin principal (not a real person) |
| `LAYOUT_SURVEY_FIXTURE_MAP` | both | fixture map path (seed writes, capture reads) |
| `LAYOUT_SURVEY_FIXTURE_TAG` | seed | slug namespace, `^[a-z0-9][a-z0-9-]{0,23}$` |
| `LAYOUT_SURVEY_EVIDENCE_DIR` | capture | evidence root; each run creates a new `capture-run-…` subdir |
| `LAYOUT_SURVEY_RELEASE_FILE` | capture | steward Release record for this slot |
| `CHROMIUM_EXECUTABLE` | capture | optional system Chromium |

## Runbook (steward = ii2)

Prerequisites per slot: launched with `server start --foreground --host … --web-port …
--enable-hub --enable-web --enable-runtime-broker=false --hosted --enable-test-login
--db <slot-local> --web-assets-dir <immutable dir>`, **no `--dev-auth`**, secret via
`SCION_SERVER_SESSION_SECRET` from a 0600 file. Capture host: Node 24, `npm ci` in a
private checkout of `web/` at the runner commit, Chromium, CJK/emoji fonts, `fc-list`.

0. **Admin bootstrap (steward, before anything is measured).** `test-login` with
   `role=admin` alone gets 403 on `POST /api/v1/groups`; the account also needs the
   system super-admin binding. Either set `server.hub.admin_emails` to the synthetic
   admin for the slot, or test-login once and restart the slot (startup logs
   `backfilled super-admin role bindings`). Record route + principal id. The
   capture never restarts or backfills.
1. **Seed (steward):**
   `cd web && npx playwright test -c e2e/layout-survey/playwright.seed.config.ts`
   → fixture map + `*.seed-issuance.json` (value-free). Readback must be `ok`.
   Then stop the seed slot, checkpoint SQLite, snapshot, sha256 → `fixtureSnapshotSha256`.
2. **Release record (steward), per slot generation:**
   `node e2e/layout-survey/scripts/records.mjs release --out <release.json> --assets-dir <asset dir> --binary <scion binary> --lockfile web/package-lock.json --source-sha <40-hex> --backend-source-sha <40-hex> --included-commits <40-hex,…> --toolchain "<node/go/image>" --base-url <origin> --slot-generation <id> --release-kind preview|verification --steward <id> --fixture-snapshot-sha256 <hex> --schema-version-id <id> --settings-profile <settings file> --publication-checkpoint <text> --server-launch-flags="<the slot's non-secret server start flags>"`
   (written 0444, refuses to overwrite; prints its sha256). `serverLaunch` records
   hosted / runtimeBroker / testLogin / devAuth derived from those flags; flags that
   look like secret material are rejected, and `devAuth: true` fails validation.
   `sourceSha` is the source of the SERVED assets (baseline 4a253489…, candidate =
   reviewed fix SHA); `scenarioSuiteSha` is the runner suite digest — keep them distinct.
3. **Capture (capturer ≠ developer):**
   `npx playwright test -c e2e/layout-survey/playwright.config.ts`
   Exit 0 = batch valid and no `capture-error`. `assets/main.js` is hashed before and
   after; any mismatch marks the batch invalid in `run.json`. Assertion failures are
   **recorded outcomes**, not runner failures.
4. **Scan + publish:**
   `node e2e/layout-survey/scripts/bundle.mjs publish <evidence>/<capture-run-…> --dest <durable root> --private $LAYOUT_SURVEY_PRIVATE_DIR --secret-file $LAYOUT_SURVEY_SESSION_SECRET_FILE`
   Send the printed bundle digest + tar sha256 to recipients; they re-hash the bytes
   they fetched.
5. **Validate records:** `node e2e/layout-survey/scripts/records.mjs validate-run <run dir> --release <release.json>`
   (cross-checks every capture against the Release: id, release-record sha256,
   slotGeneration, main.js pre AND post, batchValid) and
   `node e2e/layout-survey/scripts/records.mjs validate <finding.json> <verification.json>`.
   Bundle integrity: `SHA256SUMS` covers every evidence file; `bundle.json` (which
   names the SHA256SUMS digest) is covered by the tarball digest, which recipients
   re-hash after fetching — no circular self-hash.
6. Teardown/revocation probes are steward-owned (design §98–107); the private dir's
   `credentials-*.json` lists every issued credential for those probes, then is retired last.

### Fault/recovery fallback (only if the genuine defect cannot be used)

FI-1 adds `min-width: 1600px` to the groups `table` (applies cleanly to both the
baseline source and the fix head). Use it on top of a source where the assertions
pass (the reviewed fix head): steward checkout at that SHA, `git apply
web/e2e/layout-survey/fault-injection/FI-1-table-min-width.patch`, `npm ci && npm
run build`, publish as a distinct asset dir + Release (`releaseKind: preview`,
`publicationCheckpoint` saying "FAULT INJECTION FI-1, test-only"). Capture →
expect **LS-C1 and LS-A4 fail at all three profiles** (LS-A2/A3 also fail at
390/820). LS-A1 does NOT detect it: the app shell's `.content` scrolls
internally, so document scrollWidth never exceeds the viewport. Recovery C-R1:
fresh checkout without the patch, rebuild, new Release, capture → LS-C1/LS-A4
pass again. Never merge or ship the patch.

## Assertions (1 CSS px tolerance; walks open shadow roots)

Each outcome carries `oracleClause`, the matching clause of uat-lead's frozen
Rehearsal-B supplement (rev 2, sha256 `8a287ba4…56ec572fc`). That oracle governs
pass/fail; these are its machine implementation.

| id | clause | states | definition |
|---|---|---|---|
| `LS-C1-table-fits-clip-container` | B-C1 | default, fixture-filter | groups `table.scrollWidth <= C(table).clientWidth + 1`, C = nearest ancestor (crossing shadow roots) with overflow-x hidden/clip/auto/scroll |
| `LS-A1-no-document-horizontal-overflow` | B-A1 | default, fixture-filter | `documentElement.scrollWidth <= innerWidth + 1` (supporting only — blind to in-shell overflow) |
| `LS-A2-type-badge-unclipped` | B-A2 | fixture-filter | every fixture row's `.type-badge` is inside the viewport and C(name link), and its centre hit-tests to itself |
| `LS-A3-name-visible-and-reachable` | B-A3 | fixture-filter | every fixture row's name link box is inside the viewport, its centre hit-tests to the link, and the full name is (i) rendered untruncated or (ii) ellipsized with the exact name as `title`/`aria-label`/accessible name |
| `LS-A4-no-unexpected-container-overflow` | B-OVR (supporting) | default, fixture-filter | no scroll container (e.g. app-shell `.content`) or block clip container (e.g. `.table-container`) anywhere in the page overflows by > 1px, except named policies (`sr-only-visually-hidden`) |
| `LS-I1-long-name-link-opens-detail` | B-I1 | fixture-filter | clicking the long-name link reaches `/admin/groups/<id>` within 15 s |

Capture records also carry `runId`, `releaseRecordSha256`, main.js `pre`+`post`,
`batchValid`, `servedSourceSha`, `scenarioSuiteSha` and an `integration` block
(anonymous API refusal = no dev-auth session, healthz components, declared
`serverLaunch`, auth mode, `mocks: none`). The issuance inventory states
`refreshPerformed: false`.

Readiness failures produce `status: capture-error` records with a diagnostic
screenshot. They are never findings.
