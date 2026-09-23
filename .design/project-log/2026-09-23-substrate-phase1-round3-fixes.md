# Substrate Phase 1 — review round 3 fixes (sb-rev-3)

sb-rev-3 (`07520be0b..1ef0c13a9`, extended to include `f6612c57f`/`1ef0c13a9`):
**REQUEST CHANGES**, 0 Critical, Required 2 (R-A, R-B), Consider 3 (O-1, O-2,
O-3), Nit 1 (N-1). R-B (the README NetworkPolicy-check/restart-guidance
fix) is sb-dev-2's file (`deploy/substrate/README.md`) and out of scope for
this entry. This covers R-A, the related "validate what you send" item,
O-1, O-2, O-3, and N-1.

## R-A: egress_allow validator rebuilt as an allowlist

sb-rev-3 found four more bypass classes in round 2's validator, all a
consequence of the same structural gap: round 2 (and round 1 before it)
built the validator as a **blocklist** — reject known-bad shapes — so
anything that didn't match a known-bad shape fell through and was accepted.
Each round found a new shape nobody had blocklisted yet:

- **Double trailing dot** (`foo.svc..`): `normalizeEgressAllowEntry` strips
  exactly one trailing dot (deliberately — round 2's fix), so `foo.svc..`
  normalizes to `foo.svc.`, which still ends in `.` and is not a suffix
  match against `.svc`.
- **Mixed-radix IP octets** (`0x7f.0.0.1`, `10.0x0.0.1`,
  `0x0.0x0.0x0.0x0`): round 2's `numericIPAliasPattern` recognized
  all-hex OR all-decimal forms, never a per-octet mix, which is exactly
  what `inet_aton`-style parsers accept as loopback/private addresses.
- **Non-ASCII IDN look-alikes** (`kubernetes.default。svc` with U+3002
  IDEOGRAPHIC FULL STOP, `foo.ＳＶＣ` fullwidth letters): the validator only
  ever split on ASCII `.` and matched ASCII suffixes; anything a browser's
  or curl's IDNA/UTS-46 mapping would fold into an in-cluster name passed
  straight through.
- **`.localhost` / `localhost.localdomain`**: RFC 6761 and common
  `/etc/hosts` convention resolve these to loopback regardless of DNS; no
  existing suffix or namespace rule covered them.

**Fix (substrate-lead direction, revised from the initial per-bypass patch
sb-em first requested): rebuild as an allowlist.** An entry is accepted at
all only if it is:

- **(a) a public-FQDN-shaped hostname**: every label matches
  `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$` (ASCII letters/digits/hyphens,
  RFC 1035 label shape), a leading `*` is allowed only as the first label,
  and the **last label must be alphabetic** (`^[a-z]{2,63}$`) or IDNA
  punycode (`xn--...`) — no public TLD is numeric, hex, or hyphenated; or
- **(b) an IP address or CIDR that parses as CANONICAL via `net/netip`**,
  which (unlike `net.ParseIP`/`net.ParseCIDR`) rejects leading zeros,
  mixed-radix octets, and other non-canonical spellings. Anything that
  *looks* like an IP attempt (built only from hex/decimal digits, `x`,
  `.`, `:`) but fails canonical parsing is rejected outright as a
  malformed IP — never given a second chance as a hostname, which is
  exactly the fallback round 2's fix left open for `0x7f.0.0.1` et al.

One structural rule replaces the growing special-case list: an empty label
(from the leftover dot after a double-trailing-dot strip), any non-ASCII
character, a space, and a numeric/hex/hyphenated last label are all
rejected the same way. The existing private/in-cluster CIDR list and
suffix blocklist (`.svc`, `.cluster.local`, `.internal`, `.local`, now also
`.localhost`) remain as a **second layer** on top of already-well-formed
entries, per substrate-lead's explicit instruction — not the primary gate.
A small `egressAllowBlockedHostNames` exact-match set covers
`localhost.localdomain`, which doesn't fit a suffix rule. The
known-namespace map shrinks to just `"default"` (the only un-hyphenated
Kubernetes namespace name in scion's own deployment surface — every
hyphenated one, `kube-system`/`ate-system`/etc., is now caught by the
last-label-alphabetic rule on its own). `numericIPAliasPattern` is removed
entirely: single-label numeric/hex aliases were already caught by the
single-label-hostname rule, and multi-label ones (`127.0.0.0x1`,
`foo.123`) are now caught by the last-label rule.

### Every bypass row, rounds 1–3 (all now rejected — one consolidated test)

`TestValidateEgressAllow_AllBypassesRounds1Through3` in
`pkg/config/substrate_egress_test.go` locks in every one of these in a
single table, so the full history stays tested against whatever the
validator becomes next, rather than being scattered across three rounds'
worth of separate test functions:

**Round 1** (the original blocklist): `all`, `*`, `0.0.0.0/0`, `::/0`,
`10.0.0.0/8`, `192.168.1.1` (bare IP inside a blocked range),
`atenet-router.ate-system.svc`, `api.ate-system.svc.cluster.local`,
`metadata.internal`.

**Round 2** (trailing dot, k8s short names, non-canonical IPs):
`atenet-router.ate-system.svc.cluster.local.`, `foo.SVC.`,
`atenet-router.ate-system`, `api.ate-system`, `kubernetes.default`,
`metadata`, `cluster.local`, `0.0.0.0/8`, `::`, `0x7f000001`, `2130706433`,
`127.1`, `[::1]`, `fe80::1%eth0`, `10.0.0.1:443`, `240.0.0.0/4`,
`2002::/16`, `64:ff9b::/96`.

**Round 3** (double trailing dot, mixed-radix IPs, IDN look-alikes,
localhost forms, and assorted garbage): `foo.svc..`,
`atenet-router.ate-system.svc.cluster.local..`, `kubernetes.default..`,
`0x7f.0.0.1`, `0x7f.1`, `127.0.0.0x1`, `10.0x0.0.1`, `0x0.0x0.0x0.0x0`,
`kubernetes.default。svc` (U+3002), `foo.ＳＶＣ` (fullwidth), `foo.localhost`,
`localhost.localdomain`, `1.2.3.4.5`, `foo.123`, `foo.0x7f`, `git hub.com`
(embedded space).

### No false positives (unchanged, per sb-rev-3's explicit list)

`TestValidateEgressAllow_NoFalsePositives`: `api.anthropic.com`,
`github.com`, `registry.npmjs.org`, `storage.googleapis.com`,
`my-host.example.com`, `*.github.com`, `GitHub.COM.` (uppercase + trailing
dot), `xn--80ak6aa92e.com`, `foo.xn--p1ai` (IDNA punycode TLD), `8.8.8.8`,
`2001:4860:4860::8888`, `1.1.1.0/24`.

### O-1: three more IPv4-embedding IPv6 ranges

Added to `egressAllowBlockedCIDRs`: `::/96` (IPv4-compatible IPv6,
deprecated `::a.b.c.d` form, still parses), `64:ff9b:1::/48` (RFC 8215
local-use NAT64), and `2001::/32` (Teredo, RFC 4380) — all three embed
IPv4 address space, matching round 2's original intent for the transition-
range additions. `TestValidateEgressAllow_O1CIDRs` covers each, plus a
Teredo-adjacent public address (`2001:4860:4860::8888`, Google's public
DNS) to confirm the narrow `2001::/32` prefix doesn't over-match every
`2001:`-prefixed address.

### O-3: localhost/IDN/uppercase test coverage

`TestValidateEgressAllow_LocalhostForms` dedicates coverage to every
localhost-shaped rejection (`localhost`, `LOCALHOST`, `localhost.`,
`foo.localhost`, `FOO.LOCALHOST`, `localhost.localdomain`,
`LOCALHOST.LOCALDOMAIN`) beyond what the combined bypass table already
exercises. IDN and uppercase forms are covered by the bypass table (IDN)
and the no-false-positives list (`GitHub.COM.`, `foo.xn--p1ai`).

### Related: "validate what you send"

`substrateEgressHostnames` (`pkg/runtime/substrate_egress.go`) was sending
`strings.TrimSpace(raw)` for each `egress_allow` entry into the actor's
`EgressPolicy`, not the normalized form `ValidateEgressAllow` actually
checked. An entry like `GitHub.COM.` validates fine (it normalizes to
`github.com`), but Substrate's own `HostnameRule` requires a lowercase name
with no trailing dot — so the unnormalized string would be rejected by the
Substrate API even though scion's own validation had accepted it.
`normalizeEgressAllowEntry` is now exported as
`config.NormalizeEgressAllowEntry` so `pkg/runtime` can send exactly the
string that was validated.

## N-1: stale `EgressAllow` doc comment

`pkg/config/settings_v1.go`'s field doc said `Validate` runs "at
settings-load time ... and defensively again in Run before
CreateActorEgressPolicy" — neither half was still true after round 2's own
N-2 fix removed the second `Run` call and confirmed `Validate` runs in
`NewSubstrateRuntime`, not any settings-load hook (there isn't one for this
yet). The comment's inlined rule list had also drifted from
`ValidateEgressAllow`'s actual rules across two rounds of fixes. Corrected
to state where `Validate` actually runs, and to point at
`ValidateEgressAllow`'s doc comment instead of re-describing rules that
change more often than a field doc comment would otherwise be kept in sync
with.

## O-2: as-root assumption for the fatal chown

Round 2's N3 fix (`writeFileAtomicMode`, atomic temp-file-then-rename
bootstrap file writes) made a failing `tmp.Chown` abort the whole
bootstrap, where the previous `os.Chown` was best-effort. That's correct
*only* under the assumption `substrate-serve` runs as root in the actor
(`chown(2)` to an arbitrary uid/gid is root-only on Linux — there's no
analogue of "owner can chgrp to their own groups" for arbitrary uid/gid
changes). Documented explicitly in `writeFileAtomicMode`'s doc comment, so
a future change running it as non-root doesn't have to rediscover via every
bootstrap failing with `EPERM` why this call stopped being best-effort.

## Out of scope (per sb-em's task split)

- **R-B** (README NetworkPolicy-enforcement-check false negative on Calico
  clusters, and the restart guidance omitting the router pod) —
  `deploy/substrate/README.md`, sb-dev-2's file, edited in parallel this
  round.
- **N-3** was already fixed in round 2 (the atomic-write fix this round's
  O-2 documents further).

## Gate results

- `go build ./...` — pass.
- `go vet ./...` — pass, no output.
- `go test -count=1` with every `SCION_*` env var unset:
  `./pkg/config/...`, `./pkg/runtime/...`, `./pkg/sciontool/...` — all pass
  except the same pre-existing `TestNativeTelemetryPolicyEffectiveChildEnv`
  in `pkg/sciontool/supervisor` sb-rev-3's own gate notes already flagged
  (not touched by this branch).
- `go test -race -count=1` with `SCION_*` unset:
  - `./pkg/config/...` — pass.
  - `./pkg/runtime/` and `./pkg/runtime/substrate/...` — pass, no races
    (including the R1 fix's `substrateAgentStateMu`-guarded maps).
  - `./pkg/runtime/cloudrun/...` (a sibling package under `./pkg/runtime/...`,
    not touched by this branch) has a pre-existing, unrelated data race in
    `TestStreamLogsPropagatesListingErrors` (`logs.go`'s `logBackoff`).
    Confirmed identical on the unmodified `1ef0c13a9` base via a detached
    worktree — not introduced here.
- `GOGC=40 golangci-lint run --new-from-rev=main --concurrency=1
  ./pkg/config/... ./pkg/runtime/... ./pkg/sciontool/...` — 0 issues (not
  run by sb-rev-3 this round; run anyway per this project's own gates).
- `gofmt -l` on every changed file — clean.
