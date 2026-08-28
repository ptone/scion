# `fix/adc-preflight` round 4 — the harness, not the rules — Review

## Executive Summary
The primary fix is sound: `shellQuote` is correct, every validator row arrives at bash
byte-identical by observation, all fourteen mutations go red for the right reason, and the
`?`/`#`+shape+scheme guards close the retargeting class I opened in r3. Risk level: **LOW**.
One real finding — there is a *second* Go→bash channel in the same file (`_DI_*=%q` in the
setup strings) with the identical `%q`-is-not-shell-quoting defect that this whole round
exists to kill — but it carries no adversarial value today, so it is not a current false
pin. **Optional, not blocking. This is shippable.**

Reviewed head **`93ae24526`** (I re-pointed — see below). New head is a pure fast-forward
over the reviewed `df3b204dd`; the one added commit is comment-only, confirmed by diff.

---

## The head that moved (your mid-round message)

I took **option 1**: re-pointed to `93ae24526` and ran every gate there. I also did
option 2's check — the one-commit diff `df3b204dd..93ae24526`:

- `df3b204dd` **is an ancestor** of `93ae24526` — pure fast-forward, nothing rewritten.
- 1 commit, 1 file (`cmd/deploy_script_test.go`), +13/−4.
- **Every changed line is a comment** (the DO-NOT-DELETE-AS-REDUNDANT block on the two
  allow rows, plus a reflow of the existing double-userinfo comment) **plus one unchanged
  context row**. No code, no table row, no assertion changed. Your "comment only" reading
  is correct, verified by observation, not by taking your word.

Force-push / rebase integrity: `93ae24526` is ahead 12 / behind 0 of **both** `origin/main`
(ptone) and `upstream/main` (GCP) — they are at the same point. The three in-scope files are
exactly what I reviewed in r3 plus the r4 delta I read here in full.

---

## §1 — The harness (answered by measurement)

I built a throwaway probe (`go/ast` to extract the *real* table literals, then push each
through the real `shellQuote → runBashFuncWithSetup` path and hexdump what bash received).
Deleted before the final gates; tree is clean.

**Does every validator row arrive at bash byte-identical? YES.**
- **38/38 rows** decoded from bash equal the Go literal, byte for byte. (Note the count:
  **38, not 39** — see "What is wrong in the brief.")
- **25/25 adversarial bytes** the table doesn't contain today but a future row could:
  single quote, two single quotes, backslash, literal `\t`, `$HOME`, `${HOME}`, `$(id)`,
  `` `id` ``, double quote, `!`, `!!`, `;id;`, newline+`;`, glob, tilde, non-ASCII UTF-8,
  invalid UTF-8, high bytes, all-quotes-mixed, empty, only-quote, trailing newline, tab,
  CR, VT/FF — **all arrive intact.**
- **NUL**: the one byte that cannot survive fails **loudly** — `fork/exec: invalid
  argument` — no silent truncation. Acceptable.

`shellQuote` is correct: canonical POSIX single-quote escaping (`'` → `'\''`), and inside
single quotes bash expands nothing, so `$`, `` ` ``, `\`, `!`, `*`, whitespace all pass
through literally. It is right for every byte a Go string literal can hold except NUL, which
Go's `exec` rejects before bash sees it.

**Is there another Go→bash boundary with the same lossiness? YES — this is the finding.**
See **Optional 1** below. Short version: the `_DI_API_BASE=%q` / `_DI_TOKENINFO_URL=%q`
assignments inside the setup strings (`preflightSetup`, `TestScriptRejectsNonGoogle*`, and
the pin-test setups) are `%q` interpolated into a bash **double-quoted** context, which
`shellQuote` does not cover. Measured lossy for the R2 class and worse:
- `https://evil.example\t.googleapis.com` → arrives as literal backslash-t.
- `https://evil.example\n.googleapis.com` → arrives as literal backslash-n.
- `https://$HOME.googleapis.com` → **`https:///home/scion.googleapis.com`** (expanded).
- `https://x$(id -u).googleapis.com` → **`https://x1002.googleapis.com`** (command
  substitution **executed**).

The argv log and counter file use `printf '%s\n' "$*" >> %q` / `%[2]q` — `%q` for a
*filename Go controls* (a `t.TempDir()` path), never for adversarial input, so they are not
lossy in practice. `cmd.Env` is untouched by this change.

**Third `exec.Command` site (`EnableIAPPatchBodyViaStubServer`), your addendum — answered
by measurement, not by taking your or the developer's word:** it builds `-d '%s'` with the
body single-quoted and sends to `server.URL` (loopback). I measured it:
- The **real** body `{"iapEnabled":true,"invokerIamDisabled":true}` arrives at the stub
  **byte-identical**, request lands on loopback. Safe.
- The only byte that breaks a single-quoted word is `'` itself: a hypothetical body
  `{"a":"it's"}` arrives **empty** (curl exits 2). The real body is a fixed constant with
  no `'`, so this is latent, not live. The developer's judgement ("no seam to be lossy
  about") holds for the real body; the one caveat is the single quote, which never occurs.
  **Not a finding.**

## §2 — The scheme guard (checked as code)

Rejects `dict://`, `file://`, scheme-relative `//evil.example`, and no-scheme
`evil.example`. Accepts all four legitimate shapes: `https://…-run.googleapis.com`,
`http://127.0.0.1:PORT`, `http://localhost:PORT`, `http://[::1]:PORT`. It rejects nothing
legitimate. Pinned by m14 (red on the `dict` row only). The error names the variable
(`_DI_API_BASE must be an http:// or https:// URL.`) but **does not name the offending
scheme** — a cosmetic gap, and contrary to what the brief asked me to confirm (see below).
I concur with taking Optional 2: applying "safety must not be supplied by a downstream
component" to `%2f` but not to `dict://` in the same function would be indefensible.

## §3 — Does anything exercise the real default through the guards? — **NO live deploy.**

**Branch taken: no live deploy, and no gap finding.** Reasoning, precisely:

No test drives `di_main` with *no* overrides (all four `di_main` tests set both seams). By
the brief's literal binary that says "run a live deploy." But the brief's *actual* fear —
"if the shape regex or scheme guard is wrong about `https://REGION-run.googleapis.com`, no
operator can deploy" — is already covered by committed tests, so a live deploy would only
re-establish a property two tests already pin:

1. `TestScriptValidateOverrideURL` allow row **"regional Cloud Run endpoint (the real
   default)"** = `https://us-east4-run.googleapis.com`, and **"tokeninfo endpoint (the real
   default)"** = `https://oauth2.googleapis.com/tokeninfo`, feed those *exact* default
   strings to the *exact* `di_validate_override_url` the guards live in, and assert ALLOW.
2. `TestScriptDefaultAPIBaseIsTheRegionalEndpoint` (+ the `di_resolve_*` tests) pin that the
   resolver *emits* exactly that string when no override is set.

Together they cover "the resolver produces X" ∧ "the guard accepts X." The only unpinned
link is the three-line `resolve → validate` wiring in `di_main`, which is itself exercised
end-to-end by `TestScriptPreflightRunsBeforeInstanceCreation`. I additionally confirmed the
whole path hermetically: I ran `di_main` with **no overrides**, a stubbed gcloud, and
observed the real default pass **both** guards and execution proceed to the ADC mint
(gcloud argv log recorded `beta run instances --help`, `config get account`, `projects
describe`, `auth application-default print-access-token` — i.e. it got *past* the guards).

A live deploy's marginal value is in the parts §3 did **not** make load-bearing (real SDK
behaviour, a real 201, the IAP 302) — none of which this delta touches. The guard-bricks-
deploys risk is a deterministic property of a regex over a fixed string, and it is more
reliably closed by the committed allow row than by one live run. **No Instance created.**

## §4 — Second retargeting payload — **could not build one; class is closed.**

You built the first; I tried the second. I could not, and I can now state *why* precisely,
confirmed with a curl oracle over payloads that pass the guard:

- The suffix `di_build_iap_patch_url` appends —
  `/v2/projects/$P/locations/$R/instances/$N?updateMask=iapEnabled,invokerIamDisabled` — is
  **trusted text containing the only `?` in the final URL**. With `?`/`#` banned from the
  override, the attacker can never introduce an earlier `?`, so the query is *always* the
  safe `updateMask`. The r3 no-op-the-mutation trick is dead (measured: r3 payload now
  REJECTED).
- The suffix cannot be **removed**: RFC 3986 dot-segment normalisation deletes only segments
  *preceding* a `..`, and the appended suffix contains no `..`. Measured — a `..`-laden
  override lands on `…/v2/v2/projects/**myproj**/…/instances/**myinst**`, i.e. the operator's
  *own* instance with junk prefix, never a victim resource.
- A permitted host with a victim path in the override just prepends junk before the
  operator's own appended path — a non-resource 404, safe mask, own host. No retarget.
- `;` (matrix param) is not a query delimiter to curl; the appended `?updateMask` still wins.

The `?`/`#` guard closes the class, not just the payload. The developer's claim holds.

## §5 — The fourteen mutations (re-read for WHY)

All 14 red; shellcheck clean under each. WHY each went red:

| # | Mutation | Red set | Why |
|---|---|---|---|
| m1 | preflight moved below step 3a | PreflightRunsBeforeInstanceCreation | ADC mint no longer precedes deploy in argv log |
| m2 | ADC mint → plain `gcloud auth print-access-token` | Preflight* | wrong credential store; stub answers only the ADC noun |
| m3 | identity compare no longer skipped on absent email | SkipsComparisonWhenTokeninfoOmitsEmail | forces a compare the test says must be skipped |
| m4 | tokeninfo URL no longer echoed | Preflight | assertion on the echoed URL fails |
| m5 | `_DI_TOKENINFO_URL` guard deleted | RejectsNonGoogleTokeninfoHost | `evil.example` reaches the network; no rejection message |
| m6 | updateMask drops `iapEnabled` | EnableIAPUpdateMask | mask no longer carries the required field |
| m7 | step 3b re-mints its own token | Step3bReusesPreflightToken | second mint recorded; token not reused |
| m8 | `_DI_API_BASE` guard deleted | RejectsNonGoogleAPIBase | evil base accepted; no rejection |
| m9 | host extraction → path-strip-only | **ValidateOverrideURL: exactly the 2 allow rows** (uppercase, double-userinfo) | uppercase no longer folded → allowlist misses `FOO.GOOGLEAPIS.COM`; userinfo no longer stripped → `a@b@…` fails the shape check. Reject rows now caught first by the `?`/`#` guard + shape assertion, so they stay green. **Exactly the moved set the brief predicts.** |
| m10 | default base drops `-run` infix | DefaultAPIBaseIsTheRegionalEndpoint | wrong default host |
| m11 | step 3b re-reads `_DI_API_BASE` | SeamsAreReadInExactlyOnePlace | seam read in two places, not one |
| m12 | `?`/`#` guard deleted | **ValidateOverrideURL: exactly the 2 permitted-host rows** (retarget-via-query, permitted-host-with-fragment) | those rows have a *permitted* host before the `?`/`#`; host extraction strips there and the allowlist passes them — only the explicit guard catches them. `evil.example?...` rows stay red because their host is non-permitted. |
| m13 | shape assertion deleted | **ValidateOverrideURL: all 10 not-a-host rows** (space, tab, LF, CR, `%2f`, `%23`, `%3f`, `;`, `,`, non-numeric port) | each ends in a permitted suffix so the `case` glob matches; only the positive shape assertion rejects them |
| m14 | scheme guard deleted | **ValidateOverrideURL: the 1 dict row** | `dict://x.googleapis.com` → host `x.googleapis.com` passes shape+allowlist; only the scheme guard rejects it. `file://`/no-scheme stay red (empty host fails the shape check). |

**m9's moved red set is real and load-bearing:** the two allow rows (uppercase,
double-userinfo) are now the *only* pin on the host EXTRACTION (fold-case + last-`@` strip).
Delete them and a regression to path-strip-only extraction is undetectable. The r4 head's
DO-NOT-DELETE-AS-REDUNDANT comment records exactly this — I verified the comment's claim by
running m9 with the extraction reverted: red on those two rows and nothing else. **Keep the
comment; it is correct.**

## §6 — R5 assertion pair — confirmed, you read the developer right.

`TestScriptRejectsNonGoogle{TokeninfoHost,APIBase}`:
- exit code demoted to `require.NotEqual(0, …)`, labelled **PREMISE, not signal** — correct:
  a run that reaches the network also exits non-zero, so it cannot discriminate; making it
  `require` says so in code and guards the real assertions.
- host now asserted via the **rejection message** (`refusing to send an access token to host
  'evil.example'`) rather than a bare `Contains(stderr, "evil.example")` that a network echo
  could satisfy. Plus `_DI_TOKENINFO_URL`/`_DI_API_BASE` named, plus `NoFileExists(argvLog)`
  pinning "before any side effect."

This is exactly the pair I meant in r3. Confirmed.

## §7 — Gates (my runner)

| Gate | Result |
|---|---|
| `gofmt -l` (both test files) | clean |
| `go vet ./cmd/` | clean (exit 0) |
| `go build ./...` | clean |
| `go test ./cmd/` (full package) | **ok** |
| `TestScript*` | **40 PASS / 0 FAIL / 1 SKIP** |
| validator rows | **38** (8 allow + 30 reject) — see below |
| shellcheck (CI `while read` loop, v0.9.0) | **62/62** |
| hermeticity | verified structurally (see below) — could not blackhole egress |

**Count reconciliation:** my SDK is **gcloud 582.0.0**, so
`CheckGcloudInstances_FailureMessage` **skips** → **40/0/1**, exactly as the brief predicts
for a 582 runner (vs the developer's no-gcloud container at 41/0/0). Same suite, different
runner.

**Hermeticity — disclosed gap:** `unshare` is denied in this sandbox (no `CAP_NET_ADMIN`),
so I could **not** reproduce the "egress blackholed" run to reproduce the developer's 13/13.
I verified hermeticity structurally instead: every live network target in the deploy tests
is `server.URL`/`srv.URL` (httptest loopback) or a stubbed `gcloud`; the third `exec.Command`
site targets `server.URL`; the adversarial `evil.example` rows abort before any side effect
(pinned by `NoFileExists`). No test dials a real host. The full suite passing corroborates
this.

---

## Optional / Consider

**Optional 1 — the sibling Go→bash channel has the same `%q` defect (the §1 finding).**
`shellQuote` fixed the argv channel; the `_DI_API_BASE=%q` / `_DI_TOKENINFO_URL=%q`
assignments in `preflightSetup` and the `RejectsNonGoogle*`/pin-test setups still use `%q`
into a bash double-quoted context, which is lossy for `\t`/`\n`/`\` and **executes** `$()`
and backticks (measured above). **It is not a current false pin** — every value routed
through it today is metacharacter-free (`https://evil.example`, loopback, `server.URL`), so
every current test asserts what it claims. But it is the *identical* defect this round was
convened to eliminate, in the same file, on the channel that carries the round's headline
security pins (`RejectsNonGoogle*`). If anyone later parameterises those di_main tests over
the R2 host-shape evasions (tab, backslash, `$`) — the natural next test to write — they
would silently test the wrong bytes. This is the same "correctness supplied by a coincidence
of the input" the guard comments and your §2 self-correction reject.
*Suggested fix (one line):* route the seam values through `shellQuote` too, or set them via
`cmd.Env` instead of string interpolation. Non-blocking; forward to a cleanup pass.

**Optional 2 — scheme-guard error could name the offending scheme** (`… must be an http://
or https:// URL (got 'dict').`). Cosmetic; improves the operator message. Non-blocking.

## Nit
- The DO-NOT-DELETE comment (r4 head) is good and I want it kept.

## FYI
- The third `exec.Command` site is safe for the real (constant) body; the only latent hazard
  is an embedded `'`, which never occurs. No action.

## Positive Feedback
- `shellQuote` and the probe-by-observation approach are exactly right, and the developer's
  generalisation — "the reviewer already ran it" ≠ "the pin will detect it" — is the correct
  lesson and correctly applied.
- The positive host-shape assertion + `?`/`#` guard + scheme guard together close the
  retargeting and differential-parsing class cleanly; I could not defeat them.
- The m9 comment turns a subtle, easily-tidied-away invariant into a documented one.

## Test Coverage
Strong. Every guard is pinned by a dedicated mutation; the real defaults are committed allow
rows; the host-extraction invariant is pinned by the two allow rows m9 now depends on. Gap:
no committed test drives `di_main` with no overrides end-to-end, but the property decomposes
into two covered tests (§3), so this is not blocking.

## Backward Compatibility
None affected — deploy-time validation of test-only seams and test infrastructure only. No
wire format, no public surface.

## Final Verdict
**APPROVE.**

No Critical, no Required. The one substantive finding (Optional 1) is test-only, is not a
current false pin, and is one line from closed — forward it to a cleanup pass; it does not
block this merge. **This is shippable.**

Gates run: gofmt, go vet, go build, full `go test ./cmd/`, `TestScript` (40/0/1 on gcloud
582.0.0), validator row count (38), shellcheck 62/62 via the CI loop, all 14 mutations red,
byte-identity of all 38 rows + 25 adversarial bytes + NUL by observation. Gate **not** run:
egress-blackholed hermeticity — `unshare` denied in this sandbox; verified structurally
instead. Review harness deleted; clone tree clean; **no Instance created.**

---

## What is wrong in the brief

1. **Row count: it is 38, not 39.** §1 and §7 both say "39 validator rows"; the developer's
   report reportedly says 39 too. Counted three independent ways: 8 allow + 30 reject = 38
   source literals; 38 `go/ast`-extracted literals; 38 `--- PASS` subtests. The table is
   *correct* at 38 — this is a wrong gate number, not a missing row. No coverage is lost.
2. **§2 asks me to confirm the guard error "names the variable and the offending scheme."**
   It names the variable but **not** the scheme (Optional 2). Minor.
3. **Addendum ahead-count: `93ae24526` is ahead 12, not 13.** It measures ahead 12 / behind
   0 vs both `origin/main` and `upstream/main` (which are at the same commit). `df3b204dd` is
   ahead 11 (as the brief's §7 says); +1 comment commit → 12, not 13. Off by one; harmless.
4. **§3's binary is too strict.** "No di_main-no-override test ⇒ live deploy" skips the
   cheaper truth: the real-default *string* is a committed allow row against the real guard,
   so the risk §3 fears is already pinned without a deploy. I took the no-deploy branch on
   that basis, not on "not exploitable today."

Everything else in the brief checks out, including the central §1 thesis about the `%q`
channel, the m9-moved-set prediction, and the 41/0/0-vs-40/0/1 runner reconciliation.
