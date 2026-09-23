# Substrate Phase 1 — review round 5 fixes (sb-rev-5)

sb-rev-5 **APPROVED** the round 4 change outright (0 Critical, 0 Required),
with 5 Nit/Optional findings and 4 FYIs. Two of the Optionals (N5-1, N5-2)
were approach-level — consequences of the decided public-suffix rule as
written, not implementation bugs — and went to sb-em/substrate-lead for
disposition. Both were approved as refinements. This entry covers all five
Nits plus the special-use `.onion` addition sb-em bundled in. Everything
stays inside `pkg/config/substrate_egress.go`'s `egressAllowSuffixOK` /
`validatePublicHostname` / `NormalizeEgressAllowEntry` seam and its test
file, per sb-em's explicit scope instruction — no other file changed.

## N5-1 (approved refinement): wildcard over a PSL *wildcard* rule

`*.run.app`, `*.compute.amazonaws.com`, `*.compute-1.amazonaws.com`, and
`*.kawasaki.jp` were all **accepted** under round 4's rule. Each remainder
(`run.app`, etc.) is not itself a public suffix — so rule 2 (at least one
label beneath the matched suffix) doesn't reject it, since a bare
`foo.run.app` is a perfectly good, specific, single-tenant host. But the
PSL lists these as **wildcard** rules (`*.run.app`), meaning every
single-label child of the remainder is *itself* a public suffix — the exact
multi-tenant-platform shape rule 2 exists to reject for `googleapis.com`/
`github.io`, just expressed as a wildcard PSL rule instead of a bare one. A
wildcard `egress_allow` entry over the bare remainder grants every tenant on
the platform, not one host.

**Fix:** a third check in `egressAllowSuffixOK`, applied only when the
caller is validating a wildcard's remainder: prepend a throwaway label and
ask whether `publicsuffix.PublicSuffix("x."+remainder)` still comes back as
the whole probe string. If so, the remainder is itself a wildcard PSL rule,
and the wildcard entry is rejected. `*.github.io` and `*.googleapis.com`
stay rejected too, via the existing bare-suffix check (rule 2) — unaffected
by this addition, since they're bare PSL entries, not wildcard ones.

## N5-2 (approved refinement): 8 real ccTLDs wrongly rejected

`ck`, `er`, `fk`, `jm`, `kh`, `mm`, `np`, and `pg` have **only** a wildcard
rule in the PSL (`*.ck`), no bare-TLD rule. Round 4's TLD check,
`publicsuffix.PublicSuffix(lastLabel)`, falls through to the PSL's
unmanaged default for a bare `"ck"` and wrongly reports `icann==false` —
rejecting `www.ck` (a legitimate PSL *exception* host, directly
registrable) and every host under `com.np`/`com.jm`/etc. with "not a
recognized ICANN-managed public suffix", even though these ccTLDs are real
and ICANN-delegated. This fails closed, so sb-rev-5 flagged it as a false
positive, not a security issue.

**Fix:** rule 1 now checks `publicsuffix.PublicSuffix("x."+lastLabel)`'s
icann flag instead of `publicsuffix.PublicSuffix(lastLabel)`'s — prepending
a throwaway label makes the wildcard rule match, the same trick N5-1 uses
in the other direction. `www.ck`, `foo.com.np`, and `example.com.jm` are now
accepted; `pod`, `lan`, `corp`, `internal`, and `kom` (none of which match
any PSL rule, wildcard or bare) stay rejected — the fallback default always
reports `icann==false` regardless of whether the label is checked bare or
with a prepended probe label.

## special-use: reject `.onion` alongside `.arpa`

`onion` is listed in the PSL's **ICANN** section (unusual for a special-use
name), so rule 1 alone would accept it — the same gap that made the
explicit `.arpa` pre-check necessary in round 4. RFC 7686 reserves
`.onion` for Tor hidden-service addresses: compliant resolvers return
NXDOMAIN for it, and only Tor-aware client software resolves it at all,
never the public DNS.

**Fix:** generalized the single hardcoded `.arpa` check into
`egressAllowSpecialUseTLDs`, a `map[string]string` of TLD → rejection
reason, checked unconditionally before either numbered rule runs. `arpa`
and `onion` are both in it now; adding a future special-use TLD (e.g. if
one is ever needed) is a one-line addition to the map rather than another
special-cased `if`.

## N5-3: single-label wildcard remainder got the wrong rejection reason

`*.com`, `*.co`, and `*.bd` were rejected — correctly — but for the wrong
stated reason: the pre-existing single-label check (`len(labels) < 2`,
"not fully qualified, could resolve via a cluster/local search domain") ran
*before* the wildcard's remainder ever reached `egressAllowSuffixOK`, so the
error talked about DNS search-domain ambiguity, which doesn't actually
apply to a wildcard (there's nothing to search-domain-expand under one).
The doc comment's claim ("`*.com` … rejected [because] the remainder …
is itself a public suffix") didn't match what the code actually did, and
the test only asserted rejection, not the reason, so the drift went
unnoticed.

**Fix:** `validatePublicHostname` now takes a `wildcard bool` parameter and
skips the single-label check when `wildcard` is true, letting a
single-label wildcard remainder reach `egressAllowSuffixOK`, which rejects
it for the correct, more specific reason ("the entry is itself a public
suffix"). `TestValidateEgressAllow_RejectsWildcardOverPublicSuffix` now
asserts that reason, plus the `*.co`/`*.bd` rows sb-rev-5 asked for.

## N5-4: no total-length cap

`idna.Lookup` doesn't set `VerifyDNSLength`, so nothing rejected an
over-length name — a 256-character hostname (four 63-character labels plus
`.com`) passed validation, and would only have failed later, inside
Substrate's own `CreateActorEgressPolicy` call, breaking the "validated ==
sendable" invariant rounds 3-4 were about.

**Fix:** `egressAllowMaxLength = 253` (the standard DNS presentation-form
limit), checked in `NormalizeEgressAllowEntry` against the *exact* string
about to be returned — including a `"*."` wildcard prefix, if present, since
that's what's actually sent, not just the bare hostname portion.
`TestValidateEgressAllow_LengthLimit` uses a small test helper,
`buildHostnameOfLength`, to construct a hostname of an exact target length
(253, 256) out of valid LDH labels, rather than approximating, and also
checks that a 252-character bare remainder plus a `"*."` prefix (254 total)
is rejected — confirming the prefix counts.

## Gate results

- `go build ./...` — pass.
- `go vet ./pkg/config/... ./pkg/runtime/...` — pass, no output.
- `gofmt -l` on both changed files — clean.
- `go test -count=1` with every `SCION_*` env var unset: `./pkg/config/...`,
  `./pkg/runtime/...`, `./pkg/runtime/substrate/...` — all pass, including
  the full `TestValidateEgressAllow_AllBypassesRounds1Through5` table (every
  bypass row from all five rounds, run in the same table, each still
  rejected) and every reason-assertion test (`_R4_1RejectionReason`,
  `_ArpaRejectionReason`, `_PrivateSuffixPlatformRejectionReason`,
  `_RejectsWildcardOverPublicSuffix`,
  `_RejectsWildcardOverWildcardSuffixReason`, `_RejectsOnion`).
- `go test -race -count=1` with `SCION_*` unset: `./pkg/config/...` and
  `./pkg/runtime/...` (excluding `cloudrun`, which has the same
  pre-existing, unrelated data race in `TestStreamLogsPropagatesListingErrors`
  flagged in rounds 3 and 4's own gate notes — not touched by this task) —
  pass, no races.
- `GOGC=40 golangci-lint run --new-from-rev=main --concurrency=1
  ./pkg/config/... ./pkg/runtime/...` — 0 issues.

No `go.mod`/`go.sum` changes — `idna` and `publicsuffix` were already
available via the existing `golang.org/x/net` dependency.
