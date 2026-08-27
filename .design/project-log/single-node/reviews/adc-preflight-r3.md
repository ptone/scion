# `fix/adc-preflight` (ptone/scion, `c49a4c2d`): round-3 DELTA review

Reviewer: sn-adcpreflight-rev2 (code reviewer). Date: 2026-08-28. Task #85, review round 3.
Brief: `briefs/sn-adcpreflight-rev3.md`. Prior rounds: `reviews/adc-preflight-r1.md`,
`reviews/adc-preflight-r2.md`. Reviewed in a private clone at `/tmp/adcrev2`; `/workspace`
untouched.

## Executive Summary

Every round-2 finding is closed, verified by mutation rather than by reading, and **the live
deploy passed end to end** — real Instance, `iapEnabled: true`, 302 to `accounts.google.com`,
with round 1's `#79` diagnostic and round 2's identity-comparison fix both behaving correctly
against reality. The `?`/`#` bypass I proved in round 2 is genuinely dead.

But the question the brief actually asked — *"do the validator and curl agree on what the host
is, for every input"* — has the answer **no, for eleven measured inputs**, and there is a second
consequence of the hoist that the brief's stated reasoning does not cover: a **permitted** host
can be used to retarget the step 3b PATCH at another project's Instance. Neither is exploitable
today. Both mean the check still does not enforce the property three documents say it enforces.
**Risk: MEDIUM. REQUEST CHANGES**, on a measured five-line fix that I have already run the full
suite and shellcheck against.

---

## Critical

**None.** No input I found causes a token to leave the machine on the current tree. Round 2's
Critical 1 is closed — proven, not read (see §1).

---

## Required

### Required 1 — `di_validate_override_url` returns 0 for eleven strings that are not hosts. curl is the only thing stopping them, not the check.

The brief asked me to stop re-running the table and instead ask whether the validator and curl
agree about the host. They do not. I extracted the function from the branch tree and paired each
verdict with a live curl oracle (`curl -s -S -w 'code=%{http_code} ip=%{remote_ip}'`, curl 7.88.1):

| input (`_DI_API_BASE=…`) | validator | curl |
|---|---|---|
| `https://evil.example .googleapis.com` (space) | **ALLOW** | `curl: (3) URL using bad/illegal format` |
| `https://evil.example\t.googleapis.com` | **ALLOW** | `curl: (3)` |
| `https://evil.example\n.googleapis.com` | **ALLOW** | `curl: (3)` |
| `https://evil.example\r.googleapis.com` | **ALLOW** | `curl: (3)` |
| `https://evil.example%2f.googleapis.com` | **ALLOW** | `curl: (3)` |
| `https://evil.example%23.googleapis.com` | **ALLOW** | `curl: (3)` |
| `https://evil.example%3f.googleapis.com` | **ALLOW** | `curl: (3)` |
| `https://evil.example;.googleapis.com` | **ALLOW** | `curl: (3)` |
| `https://evil.example,.googleapis.com` | **ALLOW** | `curl: (3)` |
| `https://evil.example:8x.googleapis.com` (non-numeric port) | **ALLOW** | `curl: (3)` |
| `https://a@b@oauth2.googleapis.com` | ALLOW | `curl: (3)` |

**This is fail-closed today and I want that stated plainly: no token leaks.** `code=000`, `ip=`
empty, connection never opened. I could not build an exfiltration out of any of these rows.

The defect is structural, and it is the same structure as round 2's. In each of the first ten
rows the function extracts a "host" such as `evil.example .googleapis.com` — which is not a
hostname — observes that it ends in `.googleapis.com`, and **returns 0, asserting the host is
permitted.** The security property is not being supplied by the rule; it is being supplied by
curl 7.88's parser strictness. The seam is documented as validated in three places, and for
these inputs it is not validated, it is merely unreachable.

Three of these rows become live token exfiltration under any transport whose parser is less
strict than curl's — `%2f` if the host is percent-decoded before splitting, the space row if the
host is truncated at whitespace, `:8x` if the authority is split at the first `:`. All three are
behaviours real URL parsers exhibit. I am *not* asking you to defend against a future curl; I am
saying the check's own postcondition is false and the fix costs four lines.

**Suggested fix** — assert a positive host shape after the port strip, before the `case`:

```bash
# Assert a POSITIVE host shape before consulting the allowlist. Without this
# the glob accepts strings that are not hosts at all (whitespace, %2f, a
# non-numeric port) and the only thing stopping them is curl refusing to
# parse the URL — the safety property must live in the check, not the client.
if [[ ! "$host" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*|\[[0-9a-f:]+\])$ ]]; then
  echo "Error: $var_name does not name a host: '$host'." >&2
  return 1
fi
```

Measured: **all 21 existing table rows still pass**, all ten new rows flip to reject, and the
`a@b@` row correctly stays ALLOW (the validator is right there and curl is merely stricter —
see Corrections). The `?`/`#` rows, `evil-googleapis.com`, `googleapis.com.evil.tld`, userinfo,
ports, IPv6 loopback and the trailing-dot fail-closed all keep their current verdicts.

### Required 2 — after the hoist, a *permitted* host can retarget the step 3b PATCH at another project's Instance.

The brief (§1, last paragraph) says: *"the rule is a host allowlist, not a URL allowlist —
`https://oauth2.googleapis.com/../evil` is honoured. Harmless here because the only permitted
non-Google hosts are loopback. Confirm that reasoning still holds after the hoist."*

**It does not hold.** The reasoning is about *hosts*, and this attack uses a legitimate Google
host. Before the hoist, `_DI_API_BASE` reached one read-only GET. It now also determines the
URL of the security-critical mutation. Measured against the branch's own
`di_build_iap_patch_url` and `di_validate_override_url`:

```
_DI_API_BASE='https://us-east4-run.googleapis.com/v2/projects/victim/locations/us-east4/instances/victim?updateMask=iapEnabled&z='

validator: ALLOW      (host is genuinely us-east4-run.googleapis.com)
PATCH ->   https://us-east4-run.googleapis.com/v2/projects/victim/locations/us-east4/
           instances/victim?updateMask=iapEnabled&z=/v2/projects/my-proj/...
```

The operator's live ADC token is sent as a PATCH against a **different Instance in a different
project**, with a valid `updateMask`, and the operator's own Instance is silently left with IAP
off. The trailing `&z=` swallows the appended path into the query string; `?` is the
load-bearing character.

Exploiting this requires setting `_DI_API_BASE`, so the *risk* is unchanged — an actor with that
much control has already won. But the *stated reason it is harmless* is now wrong, and that
reason is what you will carry into the next decision. The correct statement is: "harmless
because an actor who can set `_DI_API_BASE` can already run arbitrary code," **not** "harmless
because only loopback is permitted."

**Suggested fix** — one guard at the top of the function, which subsumes the whole class:

```bash
# An endpoint override is a base URL, never a query or a fragment. Rejecting
# them outright stops a PERMITTED host being used to retarget the step 3b
# PATCH at another project's Instance via the path.
if [[ "$url" == *[?#]* ]]; then
  echo "Error: $var_name must not contain '?' or '#'; it is an endpoint, not a query." >&2
  return 1
fi
```

No legitimate value contains `?` or `#`: the real defaults do not, `http://127.0.0.1:PORT` does
not, and `.../tokeninfo` does not.

### Required 3 — the new docs block claims a mismatch warning that this same diff removed for every service-account ADC.

`hub-setup-cloudrun.md` gains (this diff):

> *"The deploy script detects this mismatch and warns…"*

The same diff makes the script **skip** the comparison whenever `tokeninfo` omits the `email`
claim — which is every service-account ADC: metadata server, GCE, Cloud Shell, CI. That is not a
corner: it is exactly what my live deploy hit, and the script correctly printed

```
ADC identity: client ID 110532853671892060667 (no email claim — comparison with the gcloud account skipped)
```

So the delta simultaneously introduces a doc claim and the behaviour that falsifies it, for the
majority credential type. This is the round-2 shape at lower stakes (a diagnostic, not a
control), and it is one sentence:

> The deploy script compares the two identities and warns when they differ, **but only when the
> ADC token carries an `email` claim. Service-account ADC (metadata server, Cloud Shell, CI)
> does not, and the script says so and skips the comparison** — so on those, verify by hand.

---

## Verification the brief asked for

### §1 — the bypass is fixed; here is the next one

**Round 2's Critical 1 is genuinely closed.** Verified against the real script, not the table:
`_DI_API_BASE='https://evil.example?.googleapis.com'` and the `#` form are both rejected by name
with the variable named, before any gcloud call.

The three-line extraction is correct on every axis I could attack **except host *shape***:

- **Scheme.** Stripped by `${url#*://}`. It is otherwise **unconstrained** —
  `dict://x.googleapis.com` is ALLOWed and curl really opens a TCP connection (`curl: (28)
  Connection timed out`, i.e. it tried). Only Google/loopback hosts can pass, and curl carries
  no `Authorization` header on `dict:`/`gopher:`, so there is no token exposure. **FYI, not a
  finding.** Scheme-relative `//evil.example`, bare `evil.example` and `file:///etc/passwd` all
  reject correctly.
- **Multiple `@`.** Your belief that validator and curl agree is **wrong, in the safe
  direction** — see Corrections. `https://oauth2.googleapis.com@evil.example` is correctly
  rejected by the validator and independently by curl (`(6) Could not resolve host:
  evil.example`), which is the case that matters.
- **Empty host.** `https://`, `''`, unset-vs-set-empty all reject. `${_DI_API_BASE:-…}` treats
  set-empty as unset, so the default applies — correct.
- **Whitespace, control chars, percent-encoding, `;`, `,`, non-numeric port.** Required 1.
- **Host-not-URL.** Required 2.

### §2 — live deploy: PASS

Real end-to-end run on the branch's script. `sn-r3-rev`, `ptone-experiments`, `us-east4`, image
`…/scion-omni:f99a818`, impersonating `scion-instance-gym@…`, gcloud 582.0.0.

- **Exit 0.** URL printed: `https://sn-r3-rev-721899303052.us-east4.run.app`.
- **`iapEnabled: true`** and `invokerIamDisabled: true`, read back from the REST v2 GET.
- **Unauthenticated fetch → HTTP/2 302** to `accounts.google.com/o/oauth2/v2/auth`, with
  `x-goog-iap-generated-response: true`.
- Steps 4–7 all completed; `di_assert_perimeter` verified enforcement itself.

**Does anything now run before the gcloud capability probe that should not? No.** The new block
is silent when no override is set — the live output goes straight from flag parsing to
`==> Step 1`. Two things worth recording:

- **`#79`'s old-SDK diagnostic still fires first.** Verified *before* I upgraded this container,
  on a genuine gcloud **575.0.0** with no overrides set: the script aborts naming the missing
  `beta run instances` noun and the `apt-get` remedy, with no side effect. The hoisted seam block
  produced no output at all. This is the check the brief most wanted and it is intact.
- **The bad-override abort is actionable and runs earlier still.** With
  `_DI_API_BASE=https://evil.example`, di_main aborts before the `#79` probe, naming the host,
  the variable and the remedy ("Unset it and retry"). The gcloud stub records nothing — that is
  the `assert.NoFileExists` pin, and under mutation it genuinely flips (see §5/m8).

Two live-run observations that are not findings but you should know: the deploy **modified the
region-level IAP binding** for `cloud_run-us-east4` (step 5 added
`scion-my-grove@deploy-demo-test`), which is shared regional state, not per-Instance; and step 4
polled through `404 → 503 → 500` for roughly two minutes before IAP took, which is within the
"30–75 seconds" the script advertises but not by much.

**Every Instance I created is deleted.** `sn-r3-rev` deleted; the post-run list is exactly the
nine baseline names (`e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminfix-t`,
`sn-adminseed-t`, `sn-ready`, `sn-step6`, `sn-walk`), none touched, none restarted. No failed
runs, so nothing else to clean up.

### §3 — the read-count pin: it is a grep, and I can show you both edges

You asked for a durability judgement. I probed it by inserting a second reader in step 3b, one
form at a time, and reading whether the pin went red.

| second reader added | pin |
|---|---|
| `_v=_DI_API_BASE; api_base="${!_v:-x}"` (indirection) | **GREEN — walks past** |
| `api_base="$(printenv _DI_API_BASE …)"` | **GREEN — walks past** |
| `api_base="$(env \| grep ^_DI_API_BASE= …)"` | **GREEN — walks past** |
| `_v="_DI_API""_BASE"; "${!_v:-x}"` (computed name) | **GREEN — walks past** |
| `if [[ -v _DI_API_BASE ]]; then api_base="$_DI_API_BASE"; fi` | **GREEN — walks past** |
| `api_base="${_DI_API_BASE:-x}"` | RED (correct) |
| `api_base="${_DI_API_BASE-x}"` | RED (correct) |
| *innocent comment containing the literal `${_DI_API_BASE:-...}`* | **RED — false positive** |
| *existing call reformatted to `api_base=$(di_resolve_api_base …)`* | **RED — false positive** |
| *extra call written as `_x=$(di_resolve_api_base …)`* | **GREEN — walks past** |

Two mitigations in the pin's favour, which I checked rather than assumed: `api_base="${_DI_API_BASE}"`
and `api_base="$_DI_API_BASE"` both **die immediately under `set -u`** when the variable is
unset, so those two forms cannot ship unnoticed. `set -u` therefore pushes a careless refactorer
toward the `:-` form the pin does catch. That is real and I credit it.

But `[[ -v VAR ]]`-guarded and indirection reads are `set -u`-safe, fully functional, and
invisible. And the false positive is not hypothetical: **§3 line 79 of your own brief contains
the exact string that breaks the pin.** The next person who pastes that sentence into `deploy.sh`
as a comment turns CI red for no reason, and pins that fail for unrelated edits get deleted.

**Is "exactly one read" the right invariant?** It is the right *shape* — "every read is
validated" is not expressible as a grep — but this implementation states it too narrowly and
matches too literally. A strictly better one-line version: strip comment lines, then count the
bare variable name.

```go
code := regexp.MustCompile(`(?m)^\s*#.*$`).ReplaceAllString(script, "")
mentions := regexp.MustCompile(`\b`+seam.variable+`\b`).FindAllString(code, -1)
assert.Len(t, mentions, 2, "…one read in %s, one name literal in the di_validate_override_url call", seam.resolver)
```

Measured against every probe above: clean = 2; it goes **red** on indirection, `printenv`,
`env|grep`, `${VAR}`, `$VAR`, `[[ -v ]]`, and the `:-` form; **green** on the comment and on the
call reformatting; and as a bonus it goes **red when the validation call is deleted** (count
drops to 1), which the current pin does not detect at all. Only the deliberately
string-concatenated name escapes, and a refactor guard does not need to stop an adversary.

I would also **drop the resolver-call-count assertion**. It is formatting-coupled in both
directions (red on an unquoted rewrite, blind to an added unquoted call) and the property it
guards is low-value: a second call to the resolver means a second *validation*, which is
harmless. Label: this is **Optional**, not blocking — the current pin does catch the most likely
accident. But it should not be left believed to be stronger than it is.

### §4 — both seams hoisted: confirmed, and the preflight is genuinely environment-free

Checking the consequence rather than the argument:

- **Nothing reads either variable outside its resolver.** Comment-stripped, each seam appears
  exactly twice in executable text: once as `${…:-default}` inside its resolver, once as a
  string literal naming itself in the `di_validate_override_url` call. Nowhere else.
- **`di_preflight_rest_credential` reads no environment.** Every one of the thirteen variables
  it references (`adc_azp adc_email adc_stderr_file api_base gcloud_account http_code list_url
  project region resp_file tok tokeninfo_resp tokeninfo_url`) is a `local` it declares itself.
  It names no `DI_*` global and no `_DI_*` seam. Behaviour is fully determined by the five
  arguments, as claimed.
- The one remaining at-a-distance value, `_di_adc_token`, is declared `local _di_adc_token=""` in
  `di_main` **before** the preflight runs, so an exported ambient value cannot reach the step 3b
  read, and the empty-guard at the read site backstops it. Sound.

Hoisting both rather than one was the right call and I would have argued for it.

### §5 — the eleven mutations, and *why* each went red

I reproduced all eleven independently against `/tmp/adcrev2` at `c49a4c2d` and read the failure
text, not the exit code. **All eleven are red for the property they name.**

| # | mutation | pin | why it went red |
|---|---|---|---|
| m1 | preflight block moved below step 3a | `PreflightRunsBeforeInstanceCreation` | argv log shows `beta run instances deploy` with **no `print-access-token` before it**; `adcAt == -1`. Correct signal. |
| m2 | `auth application-default print-access-token` → `auth print-access-token` | same + 4 preflight tests | argv log records `auth print-access-token`; assertion names the ADC form and the `ACCESS_TOKEN_TYPE_UNSUPPORTED` reason. Correct. |
| m3 | `if [[ -n "$adc_email" ]]` → `if true` | `SkipsComparisonWhenTokeninfoOmitsEmail` | stderr contains the WARNING with an **empty** `ADC identity:` — the exact guaranteed false positive round 1 measured; also loses the `azp` value and the word "skipped". Correct, on all three assertions. |
| m4 | tokeninfo-URL echo deleted | `PreflightSucceedsWithMatchingIdentity` | stdout no longer contains the stub URL; message names the query-string exposure. Correct. |
| m5 | `_DI_TOKENINFO_URL` validation deleted | `RejectsNonGoogleTokeninfoHost` | **argv log now exists** — di_main reached the ADC mint. Both naming assertions fail. Correct: it fails on the "before any side effect" property, which is what it claims. |
| m6 | `updateMask` drops `iapEnabled` | `EnableIAPUpdateMask` | URL shows `?updateMask=invokerIamDisabled`. Correct. |
| m7 | step 3b re-mints its own token | `Step3bReusesPreflightToken` | PATCH carries `Bearer ya29.fake-token-mint-**2**`, mint count 2. **Discriminating** — the constant-token trap I reproduced in round 2 is genuinely repaired. Correct. |
| m8 | `_DI_API_BASE` validation deleted | `RejectsNonGoogleAPIBase` | **argv log exists**, `_DI_API_BASE` absent from stderr. Correct. |
| m9 | host extraction → path-strip only | `ValidateOverrideURL` | red on **six named rows**: `reject/query_suffix_(r2_bypass)`, `reject/fragment_suffix_(r2_bypass)`, `reject/query_suffix_after_a_port`, `reject/query_parameter_suffix`, `reject/backslash_suffix`, and `allow/uppercase_host`. Exactly as the brief claims, plus three. Correct. |
| m10 | default base drops `-run` | `DefaultAPIBaseIsTheRegionalEndpoint` | `us-east4.googleapis.com` vs expected `us-east4-run.googleapis.com`, on both regions. Correct — my round-2 m9 escape is closed. |
| m11 | second `${_DI_API_BASE:-…}` read in step 3b | `SeamsAreReadInExactlyOnePlace` | `"[${_DI_API_BASE:- ${_DI_API_BASE:-]" should have 1 item(s), but has 2`. Correct — for the `:-` form only; see §3. |

**The weak-pin repair works.** Under m5/m8 on the *old* stub the tests distinguished clean from
mutated by accident, because di_main died at the SDK probe. With `fullGcloudStub` the mutated run
now reaches the mint and **writes the argv log**, so `assert.NoFileExists` carries real signal
and the claim "the check runs before any side effect" is verified rather than assumed. Good
catch by the developer, and the generalisation it drew is right.

**Did I find a third weak assertion? One, minor.** In `TestScriptRejectsNonGoogleAPIBase`,
`assert.Contains(t, stderr, "evil.example")` **passes under m8** — the connection-failure message
echoes the URL, so the host name appears in stderr whether the check ran or not. It is
non-discriminating on its own. The test as a whole is sound because the `_DI_API_BASE` naming
assertion and `NoFileExists` both fail, so I am filing this as FYI, not a finding. Worth knowing
that only two of that test's four assertions carry signal.

### §6 — gates, re-run

| gate | claimed | measured |
|---|---|---|
| `gofmt -l` | clean | **clean** |
| `go vet ./cmd/` | clean | **clean** (exit 0) |
| `go build ./...` | clean | **clean** (exit 0) |
| `go test ./cmd/` (full) | ok | **ok**, 7.7s |
| `TestScript` top-level | 41 / 0 / 0 | **40 pass / 0 fail / 1 skip** — the skip is `CheckGcloudInstances_FailureMessage` on gcloud 582.0.0, exactly as §6 predicts. 41 total. |
| `ValidateOverrideURL` subtests | 21 | **21** |
| shellcheck, CI-exact loop | 62/62 | **62/62** with 0.9.0, running the workflow's `while`-loop and `find` verbatim |
| hermeticity | 14 green, egress blackholed | **14/14 pass** with `http(s)_proxy` at a dead port and `no_proxy=127.0.0.1,localhost`. Blackhole proven live: `curl https://oauth2.googleapis.com/tokeninfo` → exit 7. |
| `cmd.Env` scrub | covers the relevant tests | **verified, and load-bearing** |

On the scrub specifically, since you asked me not to trust it: there are exactly three
`exec.Command("bash", …)` sites in `cmd/*_test.go`; two are `runBashFunc` /
`runBashFuncWithSetup` and both set `cmd.Env = scrubbedEnv()`. The third
(`EnableIAPPatchBodyViaStubServer`) never sources `deploy.sh` — it runs a literal `curl` — so it
cannot read a seam. Coverage is complete. I then ran the whole suite with hostile
`_DI_API_BASE=https://evil.example _DI_TOKENINFO_URL=https://evil.example/ti` exported: **ok**.
Replacing `scrubbedEnv()` with `os.Environ()` under the same ambient value turns
`TestScriptDefaultAPIBaseIsTheRegionalEndpoint` red. The scrub is present, effective and
necessary — my round-2 FYI is closed properly.

**The proposed fix passes every gate.** I applied Required 1 + Required 2 to the real script and
re-ran: full `cmd` **ok**, `TestScript` **40/0/1**, all **21** table rows still pass, shellcheck
**clean**. Then reverted — I did not commit anything.

### Force-push integrity

Confirmed three independent ways, per the brief's warning. Six pre-rebase subjects match the six
post-rebase subjects in order; the tree diff between old and new heads is limited to `#1329`'s
`pkg/hub` files plus `.design/project-log/pf-p2a-usermgmt.md`; and `git patch-id --stable` yields
six identical IDs. **Nothing was lost.** `ahead 8 / behind 0`, 4 files, +1019/−13 — matches your
independent count exactly.

---

## Nit / Optional

1. **Optional — replace the read-count regex with a comment-stripped bare-name count, and drop
   the resolver-call assertion.** §3, with the measured replacement and its probe results.
2. **Optional — the scheme is unconstrained.** `dict://x.googleapis.com` is ALLOWed and curl
   opens a connection. No token travels (no header mechanism on those protocols) and only
   Google/loopback hosts pass, so this is defence-in-depth only. If you take Required 1 anyway,
   `^https?://` is one more clause in the same `if`.
3. **Nit — the `a@b@` row deserves a table row.** It is currently untested, and it is the one
   input where the validator is *more* permissive than curl. Pinning it documents the intended
   behaviour rather than leaving it to be rediscovered.

## FYI

- **All five round-2 findings are closed**, each confirmed by mutation, not by reading: Critical
  1 (m9), Required 1 (the 403 message now hedges to both causes, names `gcloud services enable`
  and `run.instances.list`, and the existing assertion still passes), Optional 1 (the hoist, §4),
  Optional 2 (m10), Optional 3 (`[[ ! "$http_code" =~ ^[0-9]+$ ]]`), Nit 4 (`${host,,}`), Nit 5
  (trailing dot, documented as deliberate).
- **Round 1's and round 2's substance survived the live run**, which is worth recording because
  no one had run the script since round 1: the tokeninfo URL is echoed, the preflight precedes
  step 3a on the real path, and the identity comparison printed the *correct* message against a
  real service-account ADC instead of round 1's guaranteed false positive.
- `TestScriptRejectsNonGoogleAPIBase`'s host-naming assertion is non-discriminating (§5).
- The live deploy mutates **region-level** IAP bindings (§2). Not this change's doing; noted so
  it is not mistaken for per-Instance state.

## Positive Feedback

- **The `fullGcloudStub` repair is the best work in this delta.** The developer found a
  false-signal pin unprompted, understood that the red was arriving for the wrong reason, fixed
  it, committed it separately with a comment that explains the reasoning, and generalised it
  correctly. That is the m4 lesson internalised rather than complied with.
- The comment block above the hoisted seam resolution explains *why* the shape is what it is,
  including the failure it prevents. That is the comment that stops the next refactor.
- The `#79` diagnostic and the hoisted abort compose correctly and in the right order, which was
  the single riskiest thing about this delta and was got right first time.

## Test Coverage

Strong and materially stronger than round 2. The rule now has a direct table (21 rows), both
seam rejections are pinned at the `di_main` level with a genuine before-any-side-effect
assertion, the default endpoints are pinned against an ambient override, and the token-reuse pin
discriminates. Gaps: the eleven Required-1 inputs (no row asserts a host *shape*), the `a@b@`
row, and the seam read-count pin's coverage of non-`:-` read forms.

## Backward Compatibility

No wire-format change. `di_build_iap_patch_url` gained a leading `api_base` parameter; both call
sites are updated and it is script-internal. The `_DI_*` seams remain test-only and undocumented
for operators. Required 2's fix would reject `?`/`#` in an override — no value in the repo,
tests or docs uses one.

## Final Verdict

**REQUEST CHANGES.**

To be unambiguous about what this verdict is and is not: **the branch's substance is right, the
live deploy works, my round-2 Critical is dead, and all eleven mutations are honest.** I am not
asking for a redesign. I am asking for **five lines in one function and one sentence in one
doc**, all three of which I have already written and measured against the full suite and
shellcheck. Round 4 should be short.

I am blocking rather than approving-with-recommendations for one reason: the deliverable of this
branch is *a seam that is validated*, three documents now say so, and for eleven measured inputs
the validator says "permitted" about a string that is not a host while curl does the actual
rejecting. That is the same sentence I wrote in round 2. It is not exploitable this time and I
have said so plainly — if you judge that non-exploitable-today is enough to ship and hand ptone
the hardening as a follow-up, that is a defensible call and it is yours to make. But you asked
whether the validator and curl agree, and they do not, and I would rather you decide that with
the measurement in front of you.

### Gates run

`gofmt`, `go vet`, `go build ./...`, `go test ./cmd/` (full), `go test -run TestScript` (40/0/1,
21 subtests), shellcheck 0.9.0 via the CI workflow's exact loop (62/62), hermeticity with egress
blackholed (14/14), the ambient-`_DI_*` scrub check, eleven mutations re-read individually, a
twelve-row validator/curl differential with a live oracle, ten read-count-pin durability probes,
and one live end-to-end deploy to `ptone-experiments`/`us-east4`. Nothing I needed was
unavailable; gcloud was upgraded 575.0.0 → 582.0.0 via `apt-get` as the rules permit, and the
`#79` check was done *before* that upgrade so it ran against a genuine old SDK.

---

## Corrections to the review brief

1. **§1, "Multiple `@` … I believe these agree — check that they actually do."** They do not.
   `https://a@b@oauth2.googleapis.com`: the validator ALLOWs it (host `oauth2.googleapis.com`,
   which is the correct answer), and **curl refuses to parse it entirely** — `curl: (3) URL using
   bad/illegal format`. The disagreement runs the safe way, and the validator is the one that is
   right, but the stated belief is wrong. The case that *does* agree, and matters, is
   `https://oauth2.googleapis.com@evil.example`: both resolve the host to `evil.example` and both
   reject.
2. **§1, "Harmless here because the only permitted non-Google hosts are loopback."** This is the
   most important correction. That reasoning covers the *host* half of the seam and no longer
   covers the *path* half now that `_DI_API_BASE` determines a mutating call — see Required 2,
   where a fully permitted `us-east4-run.googleapis.com` retargets the PATCH at another project's
   Instance. The conclusion (harmless) survives; the reason does not.
3. **§3, "will it false-positive — does an innocent comment … break it?"** Yes — and **line 79 of
   this brief contains the breaking string verbatim**. `${_DI_API_BASE:-...}` written in a
   `deploy.sh` comment turns the pin red.
4. **§6, "`TestScript` 41 pass / 0 fail / 0 skip."** On any runner with `beta run instances`
   present it is 40/0/1. Your very next paragraph predicts this, so the two halves of §6
   contradict each other — flagging it only so the count is not chased as a regression.
5. **§5, "the developer found a weak pin … fixed with a stub that carries di_main to the mint;
   committed separately as `c49a4c2d5`."** Confirmed accurate in every particular, including that
   the repair changes m5/m8 from a wrong-reason red to a right-reason red.
