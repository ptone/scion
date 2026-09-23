# Substrate Phase 1 — review round 4 fixes (sb-rev-4)

sb-rev-4's report (`/scion-volumes/scratchpad/projects/substrate-integration/reviews/round-4-sb-rev-4.md`)
found two more bypass/structural gaps in round 3's allowlist validator.
sb-em's task split covers all of them plus a mid-round architecture
refinement, approved by substrate-lead, that this entry describes in detail
below.

## Item 1 + R4-1: rebuild `egress_allow` on the public suffix list, not a hand-rolled "last label is alphabetic" heuristic

Round 3's allowlist accepted anything whose last label was alphabetic (or
IDNA punycode) as "TLD-shaped". That is a proxy for "looks like a domain",
not for "is delegated on the public Internet" — it let through Kubernetes'
own DNS zones and other reserved/made-up zones that are alphabetic but not
real TLDs at all:

- `169-254-169-254.default.pod` / `127-0-0-1.default.pod` /
  `10-0-0-1.kube-system.pod` — hyphen-encoded IPs under Kubernetes' `pod`
  DNS zone, which resolve to the pod's own IP (including, on GKE, the
  metadata server's `169.254.169.254`) with zero cluster-specific
  knowledge required to construct.
- `*.default.pod` — a wildcard over the same zone.
- `foo.home.arpa` (RFC 8375 private-network zone), `foo.lan`, `foo.corp`,
  `foo.local`, `foo.internal` — all alphabetic-last-label, none a real TLD.
- `example.kom` — a typo'd TLD, alphabetic, and not real.

**Fix:** replaced the last-label-alphabetic check with
`golang.org/x/net/publicsuffix.PublicSuffix`, already available via the
existing `golang.org/x/net` module (no `go.mod`/`go.sum` change). Hostname
normalization also moved onto `golang.org/x/net/idna`'s strict `idna.Lookup`
profile (`.ToASCII`), replacing round 3's ad hoc Unicode handling, for the
same reason: rely on a maintained standard-conformant implementation rather
than a hand-rolled one for exactly the part of this validator most likely to
have an edge case nobody thought of.

### The rule, and the refinement it went through mid-round

The first implementation required the *whole domain's* matched public
suffix to be ICANN-managed (`publicsuffix.PublicSuffix(ascii)` with
`icann==true`). That correctly rejected every R4-1 case above, but I found
before this went out for review that it also rejected legitimate hostnames
on multi-tenant platforms whose PSL entry is in the PSL's **PRIVATE**
section rather than ICANN's own: `storage.googleapis.com` (Google Cloud) and
`foo.github.io` (GitHub Pages) both failed, because `googleapis.com` and
`github.io` are PRIVATE PSL entries, not ICANN ones. The same class would
have hit `herokuapp.com` and any other private-suffix platform. I flagged
this to sb-em before pushing; substrate-lead approved a refinement, treated
here as **a deliberate refinement of option A** (the allowlist-first
direction), not a reversion to blocklisting:

- **(0)** Reject the whole `.arpa` TLD unconditionally, before either rule
  below runs. `arpa` is itself an ICANN-listed infrastructure TLD, so rule
  1 alone would not catch it, but everything delegated under it
  (`home.arpa`, `in-addr.arpa`, `ip6.arpa`) is special-use by convention,
  not a public host.
- **(1)** The top-level domain — the last label alone — must be
  ICANN-listed: `publicsuffix.PublicSuffix(lastLabel)` reports
  `icann==true`. This is what rejects `pod`/`svc`/`lan`/`corp`/`kom`: none
  of these is a real top-level domain at all, ICANN or otherwise.
- **(2)** The domain must have at least one label beneath its *own* matched
  public suffix, whether that suffix is ICANN- or PRIVATE-listed. This is
  what distinguishes `storage.googleapis.com` (accepted: one label,
  `storage`, beneath the PRIVATE suffix `googleapis.com`) from
  `googleapis.com` or `*.googleapis.com` (rejected: the entry *is* the
  suffix, with nothing beneath it).

A leading `*.` wildcard's remainder must independently pass both rules —
`*.com`/`*.co.uk` are rejected (the remainder is itself a public suffix with
nothing beneath it), and so is `*.googleapis.com` (the remainder,
`googleapis.com`, is itself a PRIVATE-section public suffix with nothing
beneath it). `*.example.com` passes, since `example.com` has `example`
beneath the `com` suffix.

Per sb-em's explicit instruction, the whole two-part-plus-arpa rule lives in
one small function, `egressAllowSuffixOK(ascii string, labels []string) (ok
bool, reason string)` in `pkg/config/substrate_egress.go`, so it can be
swapped again on its own — it already changed once this round.

### Residual risk (not closed by this or any name-shape check)

An ICANN-valid public hostname — one that passes every check above — can
still be made to resolve to a private or in-cluster IP address: DNS
rebinding, or services like nip.io/sslip.io that map an IP into a subdomain
of a public suffix by design. No client-side check over the *name* alone
can close this; only a check performed by the egress proxy itself, after
DNS resolution, against the address it actually connects to, can. This is
now stated in `ValidateEgressAllow`'s doc comment, the `EgressAllow` field
doc in `pkg/config/settings_v1.go`, the JSON schema description, and
`deploy/substrate/README.md`.

## Item 2 + R4-2: reject ALL IP/CIDR entries, not just ones overlapping a blocked range

Round 3 accepted any IP or CIDR that parsed canonically via `net/netip` and
didn't overlap a blocked private/in-cluster range — so a *public* IP or
CIDR (`8.8.8.8`, `2001:4860:4860::8888`, `1.1.1.0/24`) was accepted. But
Substrate's own `HostnameRule.patterns` (the only field Phase 1 populates in
`EgressRule`, per `buildEgressPolicy`) explicitly rejects IP addresses per
the proto's own documentation, and `CIDRRule` support is deferred to a later
phase — so an accepted public IP/CIDR entry could never actually work, and
silently accepting one that will be rejected downstream is worse than
telling the operator up front.

**Fix:** removed all CIDR-overlap logic (`egressAllowBlockedCIDRs`,
`checkNetworkOverlap`, `hostNetwork`, `networksOverlap`, `validateCIDREntry`
— `net` is no longer imported at all) and replaced it with an unconditional
rejection: any input that parses as an IP/CIDR, canonical or not, is
rejected with the exact message `"IP/CIDR egress rules not supported in
Phase 1"`. `8.8.8.8`, `2001:4860:4860::8888`, and `1.1.1.0/24` moved from
`TestValidateEgressAllow_AcceptsPublicHostnames` to a dedicated
`TestValidateEgressAllow_IPRejectionNamesTheExactMessage`.

Added `TestBuildEgressPolicy_PatternsMatchInputVerbatim` (confirms
`buildEgressPolicy` sends every input hostname into `HostnameRule.patterns`
verbatim, in order, and never sets `CIDRRule`) and
`TestSubstrateEgressPolicy_EndToEndNoIPPatterns` (wires
`substrateEgressHostnames` straight into `buildEgressPolicy`, confirming end
to end that nothing IP-shaped reaches the actor's `EgressPolicy`) to
`pkg/runtime/substrate_egress_test.go`, alongside
`TestSubstrateEgressHostnames_RejectsIPShapedEgressAllow`.

## Item 3 / N4-1: narrow the IP-attempt heuristic

The entry point that routes a string to the IP-rejection message rather
than the hostname grammar, `looksLikeIPAttempt`, needed to be narrow enough
not to catch real hex-alphabet domains. An early draft (and round 3's old
`numericIPAliasPattern`) used a broad "every character is in
`[0-9a-fx.:]`" class, which would have misclassified `cafe.de`,
`dead.beef.com`, `abc.de`, `fab.be`, and `adcb.ae` — all real, unrelated
public hostnames whose labels happen to spell hex-legal words — as IP
attempts.

**Fix:** `looksLikeIPAttempt` now fires only when the entry is (a)
`net/netip`-parseable as an address or CIDR, or (b) shaped like an
`inet_aton`-style spelling: every dot-separated part is either pure decimal
digits or an explicit `"0x"`-prefixed hex token (`allPartsNumericOrHex` /
`isNumericOrHexToken`). Requiring an explicit `0x` prefix for the hex case,
rather than treating any hex-legal character run as suspect, is what keeps
`cafe.de` et al. out — none of their labels start with `0x`, and `de`/`com`/
`be`/`ae` aren't pure-decimal-or-`0x` tokens either. All five are asserted
as accepted in `TestValidateEgressAllow_NoFalsePositives`.

## Item 4 / R4-1 bypass coverage

Every R4-1 case, plus the refinement's own required rows, is now locked into
`TestValidateEgressAllow_AllBypassesRounds1Through4` (renamed/extended from
Rounds1Through3):

**New this round:** `169-254-169-254.default.pod`, `127-0-0-1.default.pod`,
`10-0-0-1.kube-system.pod`, `*.default.pod`, `foo.home.arpa`, `foo.lan`,
`foo.corp`, `foo.local`, `foo.internal`, `example.kom`, plus the refinement's
required-reject rows: `home.arpa`, `1.0.0.10.in-addr.arpa`,
`googleapis.com`, `*.googleapis.com`, `*.github.io`.

Rejection *reason* is asserted where the reason is stable and meaningful,
split across three dedicated tests rather than one, since the three
rejection paths now say different things:

- `TestValidateEgressAllow_R4_1RejectionReason` — "ICANN-managed public
  suffix" substring, for `169-254-169-254.default.pod`,
  `10-0-0-1.kube-system.pod`, `foo.lan`, `foo.corp`, `example.kom`.
- `TestValidateEgressAllow_ArpaRejectionReason` — "arpa" substring, for
  `home.arpa`, `foo.home.arpa`, `1.0.0.10.in-addr.arpa`.
- `TestValidateEgressAllow_PrivateSuffixPlatformRejectionReason` — "itself a
  public suffix" substring, for `googleapis.com`, `*.googleapis.com`,
  `*.github.io`.

`storage.googleapis.com` and `foo.github.io` are asserted **accepted** in
`TestValidateEgressAllow_NoFalsePositives`, per the refinement.

## Item 5: documentation

- `ValidateEgressAllow`'s doc comment (`pkg/config/substrate_egress.go`):
  rewritten to describe the allowlist-first design, the R4-1 rationale, the
  mid-round refinement history (explicitly framed as a refinement of option
  A, not a reversion), the wildcard rule, the second-layer blocklist, and
  the residual DNS-rebinding/nip.io/sslip.io risk.
- `pkg/config/settings_v1.go`'s `EgressAllow` field doc: added a short
  summary of the Phase 1 rule (public FQDNs only, real ICANN TLD, at least
  one label below the matched suffix) and the residual-risk statement, while
  still deferring to `ValidateEgressAllow`'s doc for the exact rule set
  (per round 3's N-1 fix, which this doesn't reverse) since that changes
  more often than a field doc would otherwise be kept in sync with.
- `pkg/config/schemas/settings-v1.schema.json`'s `egress_allow` description:
  updated to state the public-FQDN-only rule and the residual risk.
- `deploy/substrate/README.md`'s `egress_allow` paragraph (the only part of
  that file this task touched — sb-dev-2 owns the rest of the README this
  round; picked up via rebase, no conflicts): rewrote the rejected-shapes
  list to match the new rule set (IP/CIDR entirely unsupported, ICANN-TLD
  requirement, private-suffix-platform handling) and added the residual-risk
  paragraph, pointing at `ValidateEgressAllow`'s doc comment for the exact
  rules rather than re-describing them a third place.

## Gate results

- `go build ./...` — pass.
- `go vet ./pkg/config/... ./pkg/runtime/...` — pass, no output.
- `gofmt -l` on every changed Go file — clean.
- `go test -count=1` with every `SCION_*` env var unset:
  `./pkg/config/...`, `./pkg/runtime/...`, `./pkg/runtime/substrate/...` —
  all pass.
- `go test -race -count=1` with `SCION_*` unset:
  - `./pkg/config/...` — pass.
  - `./pkg/runtime/` and `./pkg/runtime/substrate/...` — pass, no races.
  - `./pkg/runtime/cloudrun/...` (a sibling package under `./pkg/runtime/...`,
    not touched by this task) has a pre-existing, unrelated data race in
    `TestStreamLogsPropagatesListingErrors` (`logs.go`'s `logBackoff`),
    already flagged in round 3's own gate notes. Confirmed identical on the
    unmodified base commit (`e21525ab8`) via a detached `git worktree
    add --detach` check — not introduced by this task.
- `GOGC=40 golangci-lint run --new-from-rev=main --concurrency=1
  ./pkg/config/... ./pkg/runtime/...` — one finding (`QF1001`: De Morgan's
  law applicable in `isAllHexDigits`), fixed; re-run clean, 0 issues.
