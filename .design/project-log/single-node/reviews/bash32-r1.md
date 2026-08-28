# #88 `scion/bash32-portability` — Review

Round 1 reviewed head **`edfe61f41`**. Branch has since advanced to **`ae6ccefe9`** (two further
commits); **Round 2 addendum is at the top**, the original round-1 body follows unchanged below.

---

# ROUND 2 — head `ae6ccefe9` (added after branch advanced under review)

Three commits on `1befe923`: `edfe61f4` (fix — **unchanged**), `4827ed05` (differential harness),
`ae6ccefe` (bash-3.2 regex fixture + `--check`). The harness commit was amended once
(`12c799e5`→`4827ed05`) after a shellcheck failure.

### Correction to the dispatch (verified, not read)
The dispatch said `edfe61f41` "is no longer in the branch history." **It is** — it is the base
commit of the stack, and `git rev-parse edfe61f4` is bit-for-bit
`edfe61f41770912d7aeb033bcaabdb574cc1edc8`, the exact object I reviewed in round 1. What was
amended is the harness commit *above* it. Consequence: **round 1's P1/P2/P3/P4, R1 and the nits
carry forward by content-addressing** — the fix / test / `ci.yml` trees are provably identical, no
re-read needed. Only the two new commits are new surface.

### New surface — VERDICT: clean (would APPROVE this delta as-is)
`scripts/dev/shell-differential.sh` (233), `scripts/dev/corpus/url-override-adversarial.tsv` (53),
`scripts/dev/bash32-regex-probe.sh` (84). None is run by CI's `bash32` job; all three `.sh` are
caught by the shellcheck job's `find . -name '*.sh'`. This is developer tooling, not shipped
runtime.

### Stale-verification hunt (architect's "verify-then-edit-then-claim-clean" shape) — CLEAN
Re-ran shellcheck myself at `ae6ccefe9` (CI form `-x --source-path=SCRIPTDIR`): both new files
**CLEAN**, full sweep **64/64**. The SC1010 that caused the amend (the bare `done` string on
line 146, now quoted `'done'` with a comment) is genuinely gone — measured, not read. No other
commit in the stack carries a claim that postdates a later edit that I can find.

### Dead-exit-code-column claim — VERDICT: CONFIRMED, by measurement AND structure
dev2 disclosed its harness's first version reported `IDENTICAL` for a candidate in which every
verdict had changed — `$?` was a constant 0 (sentinel `printf x` was the subshell's last command),
so the exit column was dead — and that the 2×2 / 48-input numbers it gave earlier came from that
version, surviving only because the stderr column was live. I tested it three ways:
- **The harness now guards itself.** Its `--self-test` (run on *every* comparison, not behind a
  flag) has a **case 2 "verdict-only change"** — same stderr, different exit — that is red iff the
  exit column is really compared. I ran it: 4/4 pass under bash 5.2. The exact bug now fails the
  instrument before it can emit a verdict.
- **Reproduced the numbers with the exit column live.** base(`1befe923`, `${v,,}`) vs shipped
  sentinel → **IDENTICAL, 22 rows**. base vs plain@scheme → **2**; base vs plain@host → **4** (3
  verdict flips `1→0` + 1 message-only on the denied host, exit `1→1`, stderr changed); base vs
  plain@both → **6**. Exactly dev2's 2+4=6, and consistent with round-1 P4 (Go table: 5 verdict
  flips) and P1 (my own live exit+value harness).
- **Structural proof it *cannot* be otherwise for this function.** Every `return 1` in
  `di_validate_override_url` is preceded by an `echo …>&2`; the only silent path is the allowlist
  `return 0`; return codes are only {0,1}. So reject ⟺ nonzero-exit ⟺ nonempty-stderr are perfectly
  correlated — **the stderr column alone fully determines the verdict**, and no exit-only /
  identical-stderr divergence is constructible here. The dead column hid nothing for *this*
  validator. The numbers survive.

  Caveat kept honest: a dead exit column *would* matter for a function that rejects silently or
  uses distinct non-zero codes with identical stderr. The salvage is specific to
  `di_validate_override_url`'s reject⟺stderr correlation; the self-test is what protects the *next*
  use.

### `bash32-regex-probe.sh` `--check` — validates for real
Recorded fixture matches the probe output the architect relayed from ptone's real 3.2.57.
`--check` under bash 5.2 → `IDENTICAL to bash 3.2.57 on all 5 cases` (rc 0 — bash 5 agrees with 3.2
on these constructs, which is the portability reassurance for deploy.sh's unquoted `=~`). Corrupted
the fixture in a copy → `--check` correctly reports **DIVERGENT, rc 1**. Not vacuous. Case 5 (the
quoted-RHS-is-literal trap) is a real pin tied to a real fail-open risk.

### R1 disposition — deletion DISCHARGES it, does not move it (ruling on the architect's proposal)
Proposed fix: replace the fetch-compile job with `runs-on: macos-15` + `SCION_TEST_BASH=/bin/bash`
(GitHub's macOS runners ship `Bash 3.2.57(1)-release` natively).
- **Discharges, not relocates.** R1's subject is that the workflow reaches *outside its pinned
  supply chain* to fetch source and compile+execute it at job time. A pre-provisioned runner
  binary is part of the GitHub runner image — the same image already providing the OS, the shell
  that runs every `run:`, git and the toolchains. The job already trusts that image, unavoidably,
  for everything. Using its bash extends **no new trust**. Categorically different from
  fetch+compile. Once the fetch/compile steps are deleted, R1 has no subject (and it takes the
  Optional log-swallowing item with it).
- **One correction to the framing:** the right analogue is "the runner image itself," *not*
  `actions/checkout`. checkout is SHA-pinned; the runner image is pinned only by a moving label
  GitHub patches — a *less*-pinned dependency than checkout. That is acceptable only because it is
  unavoidable and already fully trusted, and because of the two conditions below.
- **Two conditions for the discharge to hold:** (1) pin a **specific** runner version
  (`macos-15`), never `macos-latest` — a floating alias GitHub can repoint onto an image with a
  different bash; (2) **keep the canary** — its job is now load-bearing, not belt-and-suspenders:
  it is the backstop that catches a runner-image bash drift (or `-latest` silently serving bash 5).
- **Status:** I am ruling on a *proposal*; the `macos-15` job is not in the tree at `ae6ccefe9`,
  which still contains the fetch/compile job. R1's subject is therefore still present in the
  current tree. Re-check when the new job lands: `runs-on` pinned (not `-latest`),
  `SCION_TEST_BASH=/bin/bash`, canary retained and now asserting the version, no fetch/compile
  remnant.

### Round-2 verdict
The **new-commit delta (`4827ed05`+`ae6ccefe`) is APPROVE** — clean, and unusually disciplined
(self-testing harness, in-repo fixture for an un-reobtainable measurement, honest disclosure of its
own founding bug). The **branch overall stays REQUEST CHANGES** for one reason only: R1's subject is
still in the tree at `ae6ccefe9`. Its resolution (delete the fetch/compile job → native runner) is
accepted in principle; verdict flips to APPROVE when that lands with the two conditions above. No
new findings this round.

---

# ROUND 1 — head `edfe61f41`

Branch `scion/bash32-portability` @ **`edfe61f41`**, cut from `main = 1befe923`, ahead 1 /
behind 0. Three files: `deploy.sh` (+23), `cmd/deploy_script_test.go` (+145), `ci.yml` (+63).
Scope: this branch only; #85 not re-opened, #90 (the `--help` sed) not touched.

**Note on scope:** the "one harness commit landing now" the dispatch mentioned is **not yet
on the branch** (ahead 1, single commit `edfe61f41`). This review covers `edfe61f41` only. If
the differential harness lands as its own commit (dev "what's wrong" §4), it is new
test-infrastructure surface and wants its own look.

## Executive summary

Risk: **MEDIUM**, driven entirely by CI supply-chain, not by the fix. The **deploy.sh /
test** change is clean and I would approve it as-is: the fix is correct, the security
regression the obvious fix would have caused is really avoided, the pins are observed-positive
per site, and the coverage instrument is genuinely self-validating. But the new `bash32` CI job
**fetches a bash source tarball with no integrity check, then compiles and executes it**, in a
workflow file where every `uses:` is SHA-pinned. That is one **Required** finding.
**REQUEST CHANGES** — one line to fix, and the rest is a clean cleanup pass.

The load-bearing caveat is the dev's own and it is not a defect I can close: **the CI gate has
never executed on real bash 3.2** — its first run is its first execution. The *code* fix is
proven here by measurement; the *job* is not. Watch the first `bash32` run.

## Critical

None.

## Required

**R1 — `ci.yml` `Build bash 3.2.57`: the one artifact this workflow compiles and executes is
the one thing it does not pin.** Every `uses:` in this file is SHA-pinned
(`actions/checkout@d23441a…`, `actions/setup-go@924ae3a…`) — a deliberate, visible
supply-chain bar. This new step then does:

```sh
curl -fsSL -o /tmp/bash32.tar.gz https://ftp.gnu.org/gnu/bash/bash-3.2.57.tar.gz
tar xzf … ; ./configure … ; make … ; /tmp/bash32/bash …   # compiled AND executed
```

HTTPS gives transport integrity against a passive MITM; it does **not** pin the artifact, so a
substituted or tampered upstream tarball (compromised mirror/infra, or a redirect `-L` follows)
would be compiled and run in CI. That is arbitrary code execution in the workflow context, and
it violates the file's own established standard for everything else it runs. This is in the
delta (the step is added by this branch) and the fix is one line:

```sh
echo "<published-sha256>  /tmp/bash32.tar.gz" | sha256sum -c -
```

inserted between the `curl` and the `tar`, aborting on mismatch. Pin the **published**
SHA-256 of `bash-3.2.57.tar.gz`, verified against a trusted record (GNU signature / a source
you trust) — I deliberately do **not** supply a digest here because I cannot reach
`ftp.gnu.org` from this container to verify one, and an unverified hash in a review is the same
defect one layer up. **Blocks merge**: download-compile-execute of unverified code is exactly
the class the rest of this file was written to prevent.

The severity is *consistency-with-the-repo's-own-bar* + trivial fix, not a live exploit
(HTTPS + canonical source + `persist-credentials: false` on this job). But "not exploitable
today" is the argument for Optional only when the codebase has not already decided the question
— and this file has decided it, everywhere else.

## Priority 1 — the sentinel form. Verdict: correct; I tried to break it.

`v="$(printf '%s' "$v" | tr '[:upper:]' '[:lower:]'; printf x)"; v="${v%x}"`

Measured the shipped form against `${v,,}` byte-for-byte (`od -c`, exit + value) on adversarial
inputs, not the dev's set:

| input | result |
|---|---|
| `https`, `HTTPS`, empty | IDENTICAL |
| trailing 1 / 3 newlines | IDENTICAL (newline preserved — the whole point) |
| **ends in `x`, `xxx`, single `x`** | IDENTICAL (`%x` strips one byte, not the real trailing x) |
| trailing CR / tab+spaces / embedded newline | IDENTICAL |
| **invalid UTF-8 (`HT\xff\xfeTPS`)** | IDENTICAL (byte-oriented; junk preserved) |
| very long (5000 chars) | IDENTICAL |
| **non-ASCII uppercase (`MÜNCHEN`)** | **DIFFERS**: `tr`→`mÜnchen`, `${v,,}`→`münchen` |

- **NUL**: not representable in a bash variable or `$()` capture, so it cannot reach either
  form; not a weakness in the sentinel.
- The one divergence (**non-ASCII uppercase letters**) is cosmetic and has **no verdict
  effect**: such a host fails the ASCII-only shape regex `^[a-z0-9…]` either way, and the only
  observable difference is the *case of a non-ASCII char in the error message for an
  already-rejected host*. It is also locale-dependent and pre-exists the change — in the
  C/POSIX locale `${v,,}` does not lowercase `Ü` either, so the two forms are identical there.
  So the dev's "byte-identical on all 48 inputs" is true for the ASCII/test set; the precise
  statement is "byte-identical except for the case of non-ASCII uppercase letters, which never
  changes a verdict." FYI, not a defect.

## Priority 2 — the self-validating negative control. Verdict: it really validates itself.

The instrument is `${v@Z}` (bash 4.4+), asserted to be a *runtime* "bad substitution" that
fires only when its line executes. Measured on bash 5.2: `bash -n` parses `${v@Z}` clean, and
it dies at expansion with `bad substitution` — runtime, as claimed. I cannot run 3.2 (same
egress limit as dev2), so the question is whether the control holds on an *arbitrary*
interpreter. It does, because the negative control requires **both**:

- `require.NotContains(quiet, "bad substitution")` — poison an *unreached* line, expect silence;
- `require.Contains(loud, "bad substitution")` — poison the *same* line, reach it, expect the error.

Walk the three ways an interpreter can treat `@Z`:
1. **Runtime error (bash ≥4.4, and by design 3.2 for `,,`-class):** quiet silent ✓, loud fires
   ✓ → instrument valid, proceeds. Correct.
2. **Parse error (the architect's worry for 3.2):** `source` fails for *any* poisoned copy, so
   both probes emit the parse error. If its text contains "bad substitution" → quiet's
   NotContains fails (red). If it does not → loud's Contains fails (red). **Either way the test
   goes red, not vacuously green.** Caught regardless of the exact message.
3. **Silently accepted:** loud never sees "bad substitution" → Contains fails (red). Caught.

So on any interpreter where `@Z` is not "runtime error firing exactly on execution," at least
one negative-control assertion fails and the whole test is red. The rewrite from a
run-once-by-hand check to an in-test control is the right call and it does what it claims. This
is reasoned + bash-5-measured; **not** run on 3.2 — stated as such.

## Priority 3 — the CI canary. Verdict: sound as composed; one hardening item (non-blocking).

**Answer to "what could make the job green while running bash 5":** essentially nothing today.
- If `/tmp/bash32/bash` were bash 5, `${x,,}` succeeds → canary hits the fail branch → `exit 1`.
  Caught.
- The suite step uses `-count=1` (no cached green from a prior bash-5 run) and
  `SCION_TEST_BASH=/tmp/bash32/bash`; **all four** `exec.Command` sites now route through
  `testBash()` (verified — no hardcoded `"bash"` remains), so no subtest silently falls back to
  system bash. The canary checks the *same* path the suite runs. Good.

**The one gap (Optional / hardening) — this is the architect's "green while running NOTHING",
and I rule it non-blocking.** The canary step runs `set -uo pipefail` **without `-e`**, and
`if "$BASH32" -c …` on a *missing* binary returns non-zero → the `if` is false → it falls
through to `echo "canary ok"`. **Measured: a missing binary yields a vacuous "canary ok".**

But the vacuous path is **unreachable today**, and I verified the reason in the actual YAML:
the build and the canary are **two separate steps**, and the build step ends with
`/tmp/bash32/bash --version | head -1` under `set -euo pipefail`. A missing binary fails the
build step; GitHub Actions then halts the job (default per-step failure) and the canary step
never runs. So the composed job is safe.

The architect's framing is the correct one and I adopt it: **the canary's protection against
"nothing at all" is borrowed from a different step, not established by the canary itself.** That
is the same "incidental protection" shape flagged elsewhere tonight — the check takes credit for
an invariant something upstream happens to enforce. It is not a defect *now*, but it is a latent
one: reorder the steps, merge them, or drop that `--version` line in a later edit, and the
canary silently goes vacuous with no test catching it. Since R1 already requires touching this
job, make the canary self-sufficient in the same pass:
```sh
test -x "$BASH32" || { echo "::error::$BASH32 missing"; exit 1; }
"$BASH32" --version | head -1 | grep -q 'version 3\.2' || { echo "::error::not 3.2"; exit 1; }
```
Also minor: the canary's `BASH32=` literal and the suite's `SCION_TEST_BASH:` literal are two
copies of the same path; drift would decouple what is canaried from what is run. Consider a
single job-level `env:`.

Ruling: **Optional/hardening, not merge-blocking.** The composed job is correct as written; the
weakness is decoupling risk, worth fixing because we are in the file anyway.

## Priority 4 — the pin rows. Verdict: observed positive, per site, exactly as claimed.

Mutated the plain form at each site independently and ran the table:

| mutation | reject rows that flip red | CR row |
|---|---|---|
| plain @ **286 (scheme)** | exactly `trailing_newline_inside_the_scheme`, `…_uppercase_scheme` (**2**) | green |
| plain @ **294 (host)** | exactly `trailing_newline_on_a_permitted_host`, `trailing_newlines_…`, `…_uppercase_permitted_host` (**3**) | green |
| shipped (sentinel) both | none | green |

The `trailing carriage return on a permitted host` row stays green under both mutations — the
blast-radius control: the fix strips newlines only, not CR, so it pins the fix did not
over-reach. The 5 table flips (2+3) reconcile with the dev's "2+4=6" differential: the 6th
divergence is a *message-only* change on an already-rejected denied host (shape error →
allowlist error, both reject), which is not a verdict flip, so no table row fires on it. The
numbers are internally consistent.

## Gates (my runner, gcloud 582 present)

gofmt clean · `go vet` clean · `go build ./...` clean · `go test ./cmd/ -run TestScript`
**42 PASS / 1 SKIP / 0 FAIL** (skip = `CheckGcloudInstances` on 582; dev saw 43/0/0 because
their container has no gcloud — same suite, different runner, as prior rounds) ·
`TestScriptLowercasingIsReachedByTheSuite` PASS incl. negative control + both sites ·
override-URL table **44 rows (8 allow / 36 reject)** · shellcheck 0.9.0 clean on both operator
scripts.

## Optional / Nit

- **Optional — `ci.yml` build step swallows its own logs.** `./configure … >/tmp/…configure.log
  2>&1` and `make … >/tmp/…make.log 2>&1` redirect to files that are **never printed**. Under
  `set -euo pipefail` a build failure is loud (the job goes red) but *blind* — the operator sees
  an exit code and no output, on a step that compiles from source and is the single most likely
  thing to break on a runner-toolchain change. Fix: `trap 'cat /tmp/bash32-*.log' ERR` before the
  build, or drop the redirects. Operability, not correctness — non-blocking. Bundle with R1.
- **Nit:** `deploy.sh:298` says "byte-identical … on all **45** inputs tested" while the test
  comment (`:545`) and the dev report say **48**. Reconcile the number so a later reader is not
  misled about the differential's coverage.

## FYI

- **Shared-workflow coordination is clean (architect-verified).** `GoogleCloudPlatform/scion#1339`
  is another team's open `ci:` PR touching this same file, adding ~3 lines near line 100 inside an
  existing job; our change appends a new top-level `bash32:` job at 182+. No textual overlap, no
  conflict. Consistent with what I see locally (our addition is a self-contained trailing job). I
  did not independently review #1339 — out of scope, and I cannot reach it from here; recording the
  architect's check.
- The non-ASCII-uppercase cosmetic divergence (P1) — no verdict impact.
- Reviewed `edfe61f41` only; the differential-harness commit is not yet on the branch (ahead 1).
- The gate has never run on real bash 3.2 (dev disclosure) — inherent, not a defect. Watch the
  first `bash32` run; if green on the first attempt, read the canary line before trusting it.

## Two disclosed items the architect owns (not defects in this change)

1. **`ci.yml` edited without a specific instruction.** The dispatch specified this job in
   substance, so I read it as assigned; flagging the dev's flag rather than burying it. Your
   call.
2. **The differential harness is not in the repo** — it lives in `/tmp/diff/` on a container
   that will be destroyed, and rev2's #89/#90 fixes will need the same 48-input byte-comparison
   standard. This is time-sensitive: if you want it landed, it has to happen before the box
   dies. Not a blocker for this change; a decision to make now rather than later.

## Final Verdict

**REQUEST CHANGES**, on one Required finding: **R1**, the unpinned fetch-compile-execute of the
bash 3.2 tarball in `ci.yml`. It is a one-line fix (`sha256sum -c` on the published digest) and
it is the only thing standing between this and APPROVE.

The **deploy.sh / test delta is clean and I would approve it unchanged**: the fix is correct and
measured (P1), the coverage instrument is genuinely self-validating (P2), the pins are
observed-positive per site with a working blast-radius control (P4). R1 lives entirely in the CI
job, not in the security-relevant function.

Bundle into the one cleanup pass: **R1** (checksum-pin the tarball), plus Optionals — canary
self-sufficiency (P3), surface the build logs, single `env:` for the interpreter path — and the
45-vs-48 nit.

**Gates run here:** gofmt / `go vet` / `go build` clean; `go test ./cmd/ -run TestScript`
42 PASS / 1 SKIP / 0 FAIL (skip = gcloud-582 runner difference); coverage/negative-control test
PASS; override-URL table 44 rows; shellcheck 0.9.0 clean. **Gates I could not run, and why:** the
`bash32` CI job itself — no macOS and no reachable bash-3.2 source in this container
(`ftp.gnu.org`/`bminor` egress-blocked, same limit dev2 hit); its first execution is in CI. The
`@Z` runtime-vs-parse behaviour on real 3.2 is likewise unmeasurable here — reasoned in P2,
measured only on bash 5.2.

## What in the brief is wrong

One thing that matters, two calibrations.

**Matters:** the four priority items scoped the CI axis to *interpreter authenticity* — P3 asks
"what makes the canary green while running the wrong interpreter." That is a real question and I
answered it (nothing, today). But the only **Required** finding on this branch is in a different
CI axis the brief did not point at: *artifact integrity* — is the thing we compile and run
trustworthy at all. The canary can perfectly prove "this is bash 3.2" while the tarball it was
built from was substituted upstream. Priority 3 asked whether the guard identifies the
interpreter; it did not ask whether the interpreter is the one we meant to build. The Required
lived in the gap between those two questions. (This is also why the architect's *third* input,
not the four priorities, is where the blocker surfaced — good instinct to send it.)

**Calibrations:**
- Priority 3's framing ("green while running bash 5") points at the strongest failure mode, but
  the *reachable* interpreter-side weakness is narrower and different: not "bash 5 masquerading"
  (the canary catches that) but "no interpreter at all" passing vacuously — and even that is
  covered in composition by a *different step*. Worth stating precisely so the fix targets the
  real gap and does not borrow its safety.
- The dev report's "byte-identical on all 48 inputs" is true for the tested set; the exact
  claim needs the non-ASCII-uppercase carve-out (no verdict impact). Small, but it is the kind
  of "closed against the inputs I thought of" narrowing this whole task exists to catch.
