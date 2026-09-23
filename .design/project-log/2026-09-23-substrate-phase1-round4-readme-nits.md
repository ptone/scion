# Substrate Phase 1 — round 4 N4-2/N4-3: README wording nits

sb-rev-4's round 4 review (`reviews/round-4-sb-rev-4.md`) confirmed R-B
(round 3's NetworkPolicy check fix) and the `e21525ab8` broker fixes are
all correct — including independently re-deriving the `--host=0.0.0.0`
side-effect analysis and the HMAC fail-closed behavior sb-em had asked me
to confirm, both matching what I'd found. Two Nits remained, both wording
issues in `deploy/substrate/README.md`, not substance.

## N4-2: an unverified specific value asserted as fact

I'd written that `substrate-scion-test` "reports
`datapathProvider=LEGACY_DATAPATH`" and pointed to `infra/cluster.md` "for
what these fields actually read." Checked: `cluster.md` documents that the
cluster enforces via Calico and how it was enabled, but never records the
literal output of the `gcloud ... describe` command — that specific value
was my own inference from "it's Calico, not Dataplane V2," not something
verified against a real command run. Presenting an inferred value as if it
were a recorded fact, with a citation to a document that doesn't contain
it, is exactly the kind of claim that erodes trust in the rest of the doc.

**Fix:** dropped the specific value. The reasoning that motivated the check
in the first place doesn't need it — it's enough to say Calico clusters
don't report `ADVANCED_DATAPATH` at all (that field only ever reflects
Dataplane V2), so reading `datapathProvider` alone would misread *any*
Calico cluster as unenforced, this one included. Added an explicit
instruction to capture the real output from the cluster directly rather
than trust a guessed value in this file.

Also fixed the adjacent wording bug: "expect ... `True CALICO`" read as if
`gcloud`'s `--format=value(...)` output were a single joined string when
it's actually tab-separated fields in field order. Corrected to "expect
`networkPolicy.enabled=True` and `networkPolicy.provider=CALICO`" and
added a sentence stating the command prints tab-separated fields in the
order requested.

## N4-3: citing the branch that doesn't run in our config

I'd cited `pkg/runtimebroker/server.go:816-819` (the general
non-loopback-without-strict-auth check) as *the* fail-closed mechanism.
Checked: our ConfigMap sets `server.broker.hub_endpoint`, which makes
`HubEnabled` true, so `validateBrokerAuthStartup`'s **hub-mode branch**
(`:805-809`, "...in hub mode requires HMAC auth keys") is the one that
actually evaluates and returns the error in our configuration — the
general branch at `:816-819` only reaches its own check when `HubEnabled`
is false, which isn't our case. Both branches genuinely refuse to start
rather than fall open, so the *behavior* claim was correct; only the
citation pointed at code that isn't the one executing for us.

**Fix:** cited both branches, explicit about which one actually fires
given `hub_endpoint` is set in this deployment's ConfigMap, and that the
other is the one that *would* apply if `HubEnabled` were false.

## Validation

- `git diff --stat`: `deploy/substrate/README.md` only, no manifest
  change.
- `kubeconform -summary` on `broker.yaml`: 11/11 resources still valid
  (unaffected, as expected for a README-only change).
- No Go changes this round.

## Not in scope for me this round

R4-1 (the `<a-b-c-d>.<ns>.pod` egress-allow bypass, Required · High) is
`pkg/config/substrate_egress.go` — sb-dev's file this round.
