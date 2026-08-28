# `fix/adc-preflight` round 5 — O1 pin + O2 + rebase spot-check

Head **`600a0f127`**, scope: the O1 pin, the O2 scheme-error, and the rebase onto
`ce9a7993`. Nothing else reviewed; the branch was already approved at r4.

## Verdict: APPROVE. The pin is real — observed positive on both channels.

## Rebase integrity
- Merge-base with `upstream/main` is **`ce9a7993bca…`**; ahead 13 / behind 0. Clean.
- The branch's own delta against the new base is the same four files (deploy.sh,
  deploy_script_test.go, deploy_script_pin_test.go, and the pre-existing
  `docs-site/…/hub-setup-cloudrun.md`). The large `pkg/hub` churn seen when diffing r4-head
  against r5-head is **upstream drift between the two bases**, not branch changes — it
  vanishes when each head is diffed against its own base.
- The docs file is present in both the old and new upstream bases and is byte-identical
  between the r4-approved head and r5; it only *appears* as an addition against `ce9a7993`
  because of how I first resolved the ref. Not scope creep, not a resurrected deletion.
- The only branch-owned change to `deploy.sh` since the approved r4 head is the **O2** line
  (`… must be an http:// or https:// URL (got '$scheme').`). In scope.

## O1 — the pin (the item that matters)
`seamSetup` is now the **single writer** of either seam, using `shellQuote`, not `%q`
(confirmed: no raw `_DI_*=%q`/`=%s` outside `seamSetup`; `preflightSetup` and both
`RejectsNonGoogle*` tests route through it, and the two pin-test `di_main` tests reach it via
`preflightSetup`). The developer's correction to the brief is right: this is **not** a table
row (a row runs the already-fixed argv channel and never touches the seam channel), but a
dedicated two-subtest test on both channels.

**Observed positive — the pin detects the defect, independently, on each channel:**

| Reversion | argv subtest | seam subtest |
|---|---|---|
| none (as shipped) | PASS | PASS |
| `seamSetup` → `%q` only | **PASS** | **FAIL** |
| `runBashFunc` argv → `%q` only | **FAIL** | **PASS** |

The failures are the **sentinel**, not the exit-code premise:
`file "…/seam-channel-executed" exists` — i.e. `https://$(touch SENTINEL).googleapis.com`
executed during prelude setup. The rejection (`require.NotEqual(0, …)`) stays true either
way, which is exactly why the sentinel is the only honest signal. This satisfies the rule the
developer proposed and the architect adopted: *a negative assertion is not a pin until it has
been observed positive.*

Sentinel hygiene is correct: each subtest's sentinel lives in its own `t.TempDir()`
(`…/001/seam-channel-executed`), so no other test can create, remove, or tidy it between run
and assertion — the m5/m8 weak-pin shape is avoided. The seam subtest additionally keeps the
`NoFileExists(argvLog)` "before any side effect" pin.

## O2 — scheme error names the scheme
`dict://x.googleapis.com` → `… (got 'dict').` Confirmed. The schemeless case
(`evil.example` → `(got 'evil.example')`) is the disclosed imprecision — deferred to #87.

## Gates (my runner, gcloud 582.0.0)
gofmt clean · `go vet` clean · `go build ./...` clean · full `go test ./cmd/` **ok** ·
`TestScript` **41 PASS / 1 SKIP / 0 FAIL** (the skip is `CheckGcloudInstances` on 582; the
+1 vs r4 is the new pin) · validator rows **38** (unchanged — the pin is a test, not a row) ·
shellcheck **62/62** via the CI loop. Review harness deleted earlier; tree clean; **no
Instance created.**

## The two deferred items — both defensible, agree with #87
1. **`fullGcloudStub` third `%q`-into-double-quotes instance.** Defer is sound: the `%q` is on
   a Go-controlled `t.TempDir()` argv-log path, it can never receive a table row, and it fails
   loud rather than green. The discriminator that made O1 blocking — "the next hostile row
   executes and looks like it passed" — cannot occur here. Not the same class in the way that
   matters.
2. **O2 schemeless mislabel.** `${url%%://*}` returns the whole string when there is no `://`,
   so `evil.example` is reported as if it were a scheme. Imprecise, not misleading about cause
   (it is rejected for lacking a valid scheme). Cosmetic; #87 is the right home.

Neither is in scope and neither is wrong to defer.

## Final Verdict
**APPROVE.** No Critical, no Required. The O1 pin is genuine (red on the exact defect, on
each channel, for the sentinel reason), the seam channel is consolidated behind one
shellQuote'd writer, O2 is correct, and the rebase onto `ce9a7993` is clean. Gate not
runnable here: egress-blackholed hermeticity (`unshare` denied) — verified structurally, as
in r4.
