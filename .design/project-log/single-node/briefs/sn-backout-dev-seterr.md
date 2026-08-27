# Brief: fix the `set -e` failure paths in `deploy.sh` (5 review findings)

Author: sn-impl-arch (architect). Date: 2026-08-27, 19:05. Task #84.
Branch: **`scion/sn-backout`** — the one you own. Do not start a new branch.

**This branch backs OPEN PR `GoogleCloudPlatform/scion#1325`.** ptone is holding the merge on this
work. Your push updates a live PR and re-runs its CI.

---

## 1. Where this came from

`gemini-code-assist` left 5 inline findings on the PR — 2 high, 3 medium. **I checked all five
against the source and they are real.** I very nearly dismissed them as bot noise; on
`GoogleCloudPlatform/scion#1310` we took 4 of 6 and declined 2, so the prior was reasonable and
wrong.

**The premise, which I verified:** `di_main` runs `set -euo pipefail` as its first statement,
`deploy.sh:341`. `set -e` is a **global shell option, not function-scoped**, so every function
`di_main` calls afterwards inherits it.

## 2. THE FINDING NOBODY FILED — read this first

**The test suite cannot catch this bug class, and that is a design problem, not an oversight.**

`cmd/deploy_script_test.go:52` invokes functions as:

```go
bashCmd := fmt.Sprintf("source %q && %s", scriptPath, funcName)
```

Sourcing runs no `set -e` (it is inside `di_main`, which tests never call). So **every test executes
these functions in a different shell mode than production does.** The sourceable-functions seam is
what kept 22 of 28 tests alive when the Go command was deleted — it is genuinely good — but it also
means the tests are structurally blind to everything below.

I want your view on this as a separate question from the fixes. Options I can see:

- **A.** Add one test that runs a function under `set -euo pipefail` explicitly, per failure path.
- **B.** Have the test helper opt into `set -euo pipefail` in the sourced shell by default, so all
  tests match production. Riskier — it may light up unrelated tests, which is arguably the point.
- **C.** Something better.

**Do not silently pick one and bury it in the same commit as the fixes.** Tell me which, and why.
If B lights up other tests, that is information I want, not a problem to hide.

## 3. The five findings, and where gemini's reasoning is imprecise

Take all five. But **two of its explanations are wrong in a way that matters for the fix.**

### High — `deploy.sh:306`, inside the IAP polling loop. **The worst of the five.**

```bash
location="$(grep -i '^location:' "$headers_file" | head -1 | sed ... | tr -d '\r')"
```

No `Location` header → `grep` exits 1 → `pipefail` fails the pipeline → `set -e` kills the deploy.
**Inside a retry loop this converts a transient into a total deploy failure.** That is the
"rejects a good install" category — strictly worse than the thing it is polling for.

### High — `deploy.sh:274`, the perimeter assertion.

Same mechanism. The script dies **before** reaching its own
`SECURITY FAILURE: got 302 but not to accounts.google.com` message. It still **fails closed**, which
is right — but it cannot say why, which is defect #79's class all over again.

### Medium — `deploy.sh:405`, `:429`, `:505`.

```bash
local operator_email
operator_email="$(gcloud config get account 2>/dev/null | tr -d '[:space:]')"
if [[ -z "$operator_email" ]]; then ...
```

**Gemini calls the `[[ -z ]]` check "dead code". That is imprecise and I do not want it fixed on
that reasoning.** The check still catches the real case where **gcloud exits 0 and prints nothing**
— an unset account, a project with no number returned. It is dead *only* for the non-zero-exit case.

The actual defect is nastier than "dead code": `2>/dev/null` discards gcloud's own error, and `set -e`
exits without bash printing anything. So a gcloud failure produces a **completely silent exit, no
message, no cause.** Keep the `-z` checks. Add handling for the non-zero case.

Note the separate assignment matters. `local x="$(cmd)"` on one line would mask the failure entirely,
because the assignment takes `local`'s exit status. These are two statements, so `set -e` does fire.
Do not "fix" anything by collapsing them onto one line — that hides the error instead of handling it.

## 4. Constraints

- **`scripts/single-node/deploy.sh` and the test file only.**
- **Preserve fail-closed behaviour on the perimeter assertion.** If you are ever unsure whether a
  change makes that gate more permissive, stop and ask me. It is the tier's only network perimeter.
- Every failure path must **print a cause**. That is the whole point of this branch.
- `shellcheck scripts/single-node/deploy.sh` must stay clean. `bash -n` too.
- **Do not force-push.** #1325 is open; force-pushing under it is destructive. Normal push onto the tip.
- **Do not touch the gcloud preflight** at `:195` or the section-2 docs edit. Both are cleared.
- Fully qualify issue numbers: `ptone/scion#NNNN` / `GoogleCloudPlatform/scion#NNNN`. `#79`, `#84`
  here are internal task numbers.

## 5. Report back

1. New head SHA — **the moment you push**, a live PR moves under ptone.
2. Each of the 5, and what you did.
3. Your answer on the test-mode question in §2, A/B/C and why.
4. Whether any other `grep`/command-substitution site in the file has the same shape. **Give me the
   class, not just these five instances** — the last review closed by finding the general rule.
5. Anything in this brief that is wrong. Six people corrected me today and all six were right.
