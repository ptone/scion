# Task #88 — `deploy.sh` on macOS bash 3.2 — developer report

Author: sn-adcpreflight-dev2. Date: 2026-08-28. Task #88, round 1.
Branch **`scion/bash32-portability`**, head **`edfe61f41`**, pushed to `ptone/scion`.
Cut from `GoogleCloudPlatform/scion` main at `1befe923` (the #1335 merge). Brief:
`briefs/sn-bash32-dev-r1.md`.

One commit. Three files: `deploy.sh`, `deploy_script_test.go`, `ci.yml`.

---

## Part 1 — the two lines

Verified your sweep rather than taking it: I ran 17 bash-4+ construct patterns over the file
(case-modification, `@Q` transforms, `declare -A`, namerefs, `mapfile`/`readarray`, `globstar`,
`lastpipe`, `wait -n`, `coproc`, `[[ -v ]]`, `;;&`, `|&`, `&>>`, `printf %(…)T`, negative
subscripts, auto-fd `exec {n}<`, `$'\u'`). **Two hits, both yours: 286 and 294.** Comment-stripped
re-sweep after the fix is clean.

`tr`, as you said — no new dependency.

## Part 1b — **the obvious fix is a security regression.** Measured, not argued.

This is the finding of the task and it is not the one either of us went looking for.

The natural one-liner is:

```bash
host="$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')"
```

**Command substitution strips all trailing newlines**, and the lowercasing runs **before** the
positive host-shape assertion — so the plain form deletes a trailing newline *before the check that
exists to reject it*.

I built a differential harness: the real function from `1befe923` versus each candidate, fed all 40
extracted table values plus 8 adversarial trailing-whitespace inputs, comparing **exit code and
exact stderr bytes** (`od -c`).

### The 2×2, per site, as you asked

| line 286 (scheme) | line 294 (host) | result vs original |
|---|---|---|
| sentinel | sentinel | **IDENTICAL** — all 48 inputs (shipped) |
| **plain** | sentinel | diverges on **2** inputs |
| sentinel | **plain** | diverges on **4** inputs |
| plain | plain | diverges on **6** inputs |

**Off-diagonal green, and 2 + 4 = 6 exactly.** The two sites are independent: neither masks the
other, and neither cell's divergence overlaps the other's.

**Which inputs, and why:**

- **Site 294 (host), plain form — 3 verdict flips REJECT → ALLOW:**
  `https://oauth2.googleapis.com\n`, the same with three newlines, and
  `https://OAUTH2.GOOGLEAPIS.COM\n`. Plus one message change: a trailing newline on a *denied* host
  moves from the shape error to the allowlist error (both still reject).
- **Site 286 (scheme), plain form — 2 verdict flips REJECT → ALLOW:**
  `https\n://oauth2.googleapis.com` and `HTTPS\n://oauth2.googleapis.com`. You called this one
  before I measured it: a scheme comparison that silently accepts `https\n`.

**A trailing newline in an endpoint URL is request-smuggling shape, and the entire argument for the
host-shape assertion in R2 was that curl must not be the thing that saves us.** A portability fix
would have reopened that class, in the same file, four days later, under a commit message about
macOS.

**The 44-row table goes fully green on the plain form.** Not one pre-existing row has trailing
whitespace. I only caught it because I wrote the adversarial rows *before* choosing an
implementation — your rule, and it earned its keep on first use.

**Shipped form**, at both sites:

```bash
host="$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]'; printf x)"
host="${host%x}"
```

Byte-identical to the original on all 48 inputs, exit code and stderr.

### The pin rows

Six new reject rows, commented in the m9 style with the measured per-site numbers, because the
sentinel form is uglier than the plain one and someone will try to tidy it.

**Observed positive, per site:**

| mutation | red rows |
|---|---|
| plain form at **286** only | `trailing_newline_inside_the_scheme`, `trailing_newline_inside_an_uppercase_scheme` — **only those two** |
| plain form at **294** only | `trailing_newline_on_a_permitted_host`, `trailing_newlines_on_a_permitted_host`, `trailing_newline_on_an_uppercase_permitted_host` — **only those three** |

The `\r` row stays green under both, correctly: substitution strips newlines only. It pins that the
fix did not over-reach.

**`shopt -s nocasematch`** — considered and rejected before I started, one line as requested: it is
global state that changes `[[ =~ ]]` and `case` for everything in scope, so it would silently alter
the host-shape regex and the allowlist `case` as well as the comparison I wanted, and getting it
wrong fails *open*. The sentinel is uglier and local.

## Part 2 — the gate

### What I could not do, and said early

**Building bash 3.2 here is a rabbit hole, but not for the reason you guessed — the build is easy,
I cannot obtain the source.** This container's egress is allowlisted: `ftp.gnu.org` and
`mirrors.kernel.org` time out (`000`), `github.com/bminor/bash` returns **404** while
`github.com/GoogleCloudPlatform/scion` returns **200**. apt carries 5.2 only.

### Coverage vs semantics — the split

- **Coverage** — does the suite *execute* 286 and 294? Settled here today.
- **Semantics** — does 3.2 behave identically (your `=~`/`BASH_REMATCH` question)? **Not settled. I
  did not guess.** Routed to ptone via the #89 diagnostic.

### The instrument, stated so nobody misreads it

**`${v@Z}` is not a simulation of bash 3.2.** It is an invalid parameter transformation that fails
in the **same class at the same moment** as `${v,,}` on 3.2: `bash -n` parses it clean, and it dies
at expansion time with `bad substitution`. That is all the coverage question needs. **Nothing in
this task was tested on bash 3.2.**

### `TestScriptLowercasingIsReachedByTheSuite`

Poisons each lowercasing line with `@Z` in a temp copy and asserts `bad substitution`. **Both sites
PASS — the suite executes both**, so the 3.2 job has something to detect.

It guards its own vacuity: if the poison does not apply (someone reformats the line),
`require.NotEqual` fails with instructions rather than passing green.

**And I proved the instrument discriminates**, because a new instrument asserting a positive is
worth nothing until it has been seen to stay silent when it should. Negative control: poison the
`'?'/'#'` **rejection** line — unreachable for a clean URL — and probe the *same poisoned script*
twice:

| input | result |
|---|---|
| `https://oauth2.googleapis.com` (never reaches the line) | **silent** |
| `https://oauth2.googleapis.com/x?y` (reaches it) | `bad substitution` |

So `@Z` reports **execution**, not presence.

### `SCION_TEST_BASH`

All three `exec.Command("bash", …)` sites now call `testBash()`, defaulting to `bash`. Seam
verified honored: `SCION_TEST_BASH=/bin/dash` fails, and *for the right reason* —
`/bin/dash: set: Illegal option -o pipefail`, i.e. the script really ran under the named
interpreter.

### The CI job and your canary

Added `bash32`: builds 3.2.57, runs `go test ./cmd/ -run TestScript` under it. YAML validated.

**Your canary is the best part of the brief and I verified the half that matters.** I cannot run
real 3.2, but the canary's job is to catch a *silent fallback to system bash*, and that direction I
can test:

| interpreter | canary |
|---|---|
| `/bin/bash` 5.2 — the silent-green failure mode | **fails loudly, exit 1** |
| a shell without `${x,,}` (`/bin/dash`, 3.2-like) | **passes** |

**Not verified: that the canary passes on real bash 3.2.57.** The build step and the suite under
3.2 are likewise unrun by me. First CI execution is their first execution.

---

## Gates

SDK state named: **no gcloud in this container**, so `TestScriptCheckGcloudInstances_FailureMessage`
passes rather than skips.

| gate | result |
|---|---|
| `gofmt -l ./cmd` | clean |
| `go vet ./cmd/` | clean |
| `go build ./...` | clean |
| `go test ./cmd/` (full) | **ok**, 8.2s |
| `go test -run TestScript` | **43 pass / 0 fail / 0 skip** (+1: the new coverage test) |
| `TestScriptValidateOverrideURL` | **44 rows** (8 allow / 36 reject) |
| shellcheck 0.9.0, CI `while`/`find` loop | **62/62** |
| differential, 48 inputs, exit code + stderr bytes | **byte-identical** |
| per-site 2×2 | off-diagonal green, 2 + 4 = 6 |
| pin-row mutation, per site | red on exactly its own rows |
| `@Z` coverage, both sites | both executed; negative control silent, and now **inside the test** |
| canary vs bash 5 | fails loudly, as designed |

**`ahead 1 / behind 0`** vs `GoogleCloudPlatform/scion` main `1befe9237`, measured last. Published
head `edfe61f41` matches local. Tree clean.

---

## What in here is wrong

1. **My round-5 report claimed the R2 host-shape rule was closed, and it was closed only against
   the inputs I thought of.** Trailing whitespace was never in the table. The rule survived because
   `${v,,}` happens not to strip anything — the protection was **incidental to an implementation
   detail**, not established by the check. I did not know that when I wrote "the class is closed",
   and I would write that sentence more narrowly now: the class is closed *against the inputs
   enumerated*, which is a smaller claim than it sounded.

2. **The gate I am shipping has never run.** The 3.2 build, the canary's pass branch, and the suite
   under 3.2 are all first-executed in CI. I verified everything verifiable here and I am naming
   the rest rather than letting "gate added" imply "gate proven". **Watch the first `bash32` run.**
   If it is green on the first attempt, be suspicious and check the canary line in the log.

3. **`ci.yml` is shared infrastructure and I edited it without a specific instruction to.** Your
   brief specified the job in substance, so I judged it assigned, but the standing rule is that
   build config is off-limits absent explicit assignment. Flagging the judgement, not hiding it.

4. **The differential harness should live in the repo, and I did not put it there.** You asked
   where it belongs; my answer is *in the repo*, because rev2's #89 fixes need the same 48-input
   byte-comparison standard and a scratch tool in `/tmp` will not be there when they need it. I
   left it out of this commit deliberately: it is a new test-infrastructure surface, it is not
   needed to fix the §1 blocker, and bundling it would widen an urgent commit. **It is
   `/tmp/diff/` on this container and it dies with the container** — so if you want it, say so and
   I will land it as its own commit now, before this box goes away. That is a real deadline, not a
   preference.

5. **`${v@Z}` is bash 4.4+, and I nearly shipped that as a flag instead of a fix.** The instrument
   proving 3.2 compatibility is itself a bash-4-ism. It is test-only, so not a portability defect —
   but the 3.2 job runs `-run TestScript`, which **includes** this test, so it executes on 3.2 for
   real. My whole argument rests on `@Z` being a **runtime** error, which I measured on bash 5 and
   **cannot measure on 3.2**. If 3.2 rejected it at **parse** time instead, `source` would fail
   immediately, every subtest would see `bad substitution` without executing anything, and the
   coverage gate inside the portability gate would be a decoration — passing hardest exactly where
   it matters.

   I first wrote this up as a disclosure. That was wrong: it is a defect in a gate I am shipping,
   and "flag it" is the choice that leaves it broken. So the negative control is now **inside the
   test** rather than something I ran once by hand. It poisons a line a clean URL never reaches and
   requires silence, then requires the same line to fire when a URL does reach it. On any
   interpreter where `@Z` is a parse error, the control goes **red** instead of the suite going
   vacuously green. The test now validates its own instrument on whatever bash it runs under,
   which is the only form of this I can honestly ship without a 3.2 to test on.

## Not done

No live deploy. No Instance touched. No upstream PR, no merge. `=~`/`BASH_REMATCH` on 3.2 is
**unanswered by me, deliberately**.
