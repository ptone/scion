# M2 one-target enforcement slice

## 2026-10-08 UTC — authorization schema checkpoint

The schema component has an accepted focused package GREEN. The overall
one-target slice remains incomplete. Work is based on immutable pin
`1628c385a9fff747a46494cff0715cc2760519fd` on task branch
`scion/audit-update-m2-one-target-slice`. A read-only live check preserved original
M2 at `6d6e4de4598c82f0579fb0c566ea422c7e997b29`; its ref was not changed.
The intended later composition is confined to the engineering-test domain.
Production NEW remains default-off and structurally unadmitted.

The pre-edit map found no truthful authorization decision action in the pinned
catalog: its access-boundary action accepted only commit/succeeded. Amendment 1
explicitly rescoped the dependency repair to `pkg/hub/auditevent/types.go`,
`catalog.go` and `auditevent_test.go`. It added the sealed exported
`AuthorizationDecisionPayload` with string fields PermissionID, Permission,
Reason, DeniedBy and Sampled, plus its private five-leaf projection. The single
`authorization/decide` catalog entry accepts decision/allow and decision/deny,
structured_log only, with required nonempty-string byte caps 128/128/256/64/5
and Sampled enum true/false. Existing envelope requirements and the original
access-boundary entry are preserved. No production mapper or builder was added.

Initial code and security reviews approved the schema packet. Conservation
review 1 requested changes: M1 identified nil versus allocated-empty optional
payload schema in the new catalog expectation; L1 identified an unrelated
canary in the sampled-enum non-retention assertion. Amendment 2 changed only
the test file: it expects an allocated empty OptionalPayloadLeaves slice and
checks actual submitted rejected strings, including TRUE, in both Validate and
Render validation errors. Missing/empty cases remain rejection-only. Both
production files stayed byte-identical to Amendment 1. Conservation re-review 2
approved the correction and closed M1/L1 without adding top-level tests.

Amendment 3 released exactly one wrapped `go test` invocation for
`./pkg/hub/auditevent`, with `-gcflags=all=-c=1 -timeout 14m -count=1 -p 1 -v`,
15-minute outer timeout, GOMEMLIMIT=6GiB, GOGC=40, GOMAXPROCS=1, shared GOCACHE
and `ulimit -v 12000000`. The wrapper acquired slot 4 after zero seconds; the
inside-slot memory check reported 34 GiB available and passed the 30 GiB gate.
UTC interval: 2026-10-08T02:52:13.216300+00:00 through
2026-10-08T02:56:13.044866+00:00. Command and wrapper exit were 0; recorded
command wall time was 240 seconds (wrapper measurement 239.826754 seconds).
Package result: PASS; `github.com/GoogleCloudPlatform/scion/pkg/hub/auditevent`
reported 0.043s. Exactly 29 top-level tests plus 156 subtests produced 185 RUN
and 185 matching unique PASS outcomes, with zero FAIL, SKIP, missing, unexpected
or duplicate outcome. No repair, retry or second Go command ran. The evidence
was independently accepted by engineering manager audit-update-em.

This validates only the schema package. It supplies no admission mapper or
positive one-target admission fixture, production provenance/trust, T approval,
activation, deployment, emitter cutover or legacy retirement. The historical
scanner remains deferred; no full constructor/target census was restored.
There is no race or full CI claim. Existing schema tests named for race safety
passed under the normal command; the race detector was not run. The accepted
three-path source diff remains 301 lines / 12,714 bytes, SHA-256
`4c15701abbf439dfe8e4548f25d424df9c43581ad769090bf9c46f01b28185e8`.

Protected artifacts remain broker-local under
`/scion-volumes/scratchpad/projects/audit-update/`. Identities below bind this
checkpoint; the log does not reproduce protected report or raw evidence bodies.

| Artifact under reviews/ | Lines | Bytes | SHA-256 |
| --- | ---: | ---: | --- |
| m2-one-target-slice-preedit-map.md | 667 | 54808 | ff65d669195fff9ff7c686af23de05fd7c4318ac608ea42ad1574cc49d452f09 |
| m2-one-target-slice-schema-test-fix-review-amendment-2.md | 471 | 25077 | 172363cc2a0a5786fa0c8eaa70a8eeb8993d17bfb04f97be468d89a9aadda168 |
| m2-one-target-schema-code-review-1.md | 115 | 14001 | 474c3ce3ed909f6c3d871d378035df576c076dfdfce46e6b1c862f763d36710b |
| m2-one-target-schema-conservation-review-1.md | 123 | 15337 | 07b826edc66fe59af184a042e8d97c5858577d17912d1c2c94d257cc2f9df53e |
| m2-one-target-schema-conservation-review-2.md | 130 | 16427 | a8e189021f818ca27e42369f435554023a0ea7237ad1d4d27c11b4671e9f4efc |
| m2-one-target-schema-security-review-1.md | 162 | 20703 | 4ec1b4fcf6bcbd75c29bde748238ae8f06c9b500fca4c008f699e92e15d268cc |
| m2-one-target-schema-green-amendment-3.txt | 783 | 55611 | 61d61939f27ccbe3bdc453c36bbd690487a15d18a80112dd9ca4518d0067c479 |

Human-directed question inventory: none. No product-owner or target-owner
approval was requested or inferred. This log packet stops for engineering
manager log acceptance and durability disposition; it grants no next-phase work.

## 2026-10-08 UTC — agent schema compatibility checkpoint

Amendment 11's bounded project-resource fixture search found no suitable truthful
real-Decide project allow/deny fixture within the authorized four-path/runtime
boundary. That finding is bounded; it is not a repository-wide absence claim.
The separate Amendment 12 compatibility checkpoint retains primary `project`
and adds exactly `agent` only for `authorization/decide`. Membership is exact;
both `Catalog()` and private `catalogEntry()` defensively copy the additional
resource kinds. The access-boundary contract and shared validation after
membership remain unchanged. Base: `69a52c6ee688860ed4fbb359faae884de76c03ee`.

The accepted three-path postimage identities are:

| Path under pkg/hub/auditevent/ | Lines | Bytes | SHA-256 |
| --- | ---: | ---: | --- |
| auditevent_test.go | 1174 | 41984 | af666ada63ee864bc9cb11ac435b41c3d14fb576f5a826421e0a7b076e8eb073 |
| catalog.go | 163 | 6479 | c18d7029bdd8a77b1ae567736752a9bb6487ef379b57cddb2fb5898b79969bf1 |
| validate.go | 320 | 10209 | 76dd8cd1adc94d9dbfd609087cc9ded0d5f63aa93fa0bc6ac89c1016ac287047 |

The fresh independent code, conservation and security reports each APPROVE.
Audit-update-em explicitly deferred conservation Optional C1/C2 as nonblocking:
C1's literal resource-kind error pin is redundant with the unchanged literal
validator guard and existing ValidationError contract; C2's additional agent
malformed-field matrix would broaden coverage after identical shared
post-membership validation. Neither optional note was implemented; both remain
recorded for possible future work.

Amendment 13 ran the sole authorized literal wrapped auditevent command once.
Command identity: 13 lines / 580 bytes / SHA-256
`ffa1b0e23eb2d6b5a8e9bb6fa4a16ba3938ebe51c414b62024e5bae3288da867`.
Slot 2 was acquired after queue 0 seconds; acquire-time available memory was
46 GiB, passing the 30 GiB gate. UTC start:
2026-10-08T06:09:43.966008+00:00; UTC end:
2026-10-08T06:09:52.192290+00:00. Wrapper wall: 8.226086681999732 seconds;
command wall: 8 seconds. Command and wrapper exit: 0. Package PASS reported
0.052s. Exactly 31 top-level tests + 203 subtests = 234 unique RUN and 234
matching PASS, with zero fail, skip, duplicate, missing, unexpected or mismatch.
The accepted staged full-index patch and all three postimages were unchanged
at postflight; working diff, unmerged and untracked inventories were empty.
No repair, retry, second Go command, race detector or broader CI run occurred.

Protected identities under reviews/ bind this separate compatibility checkpoint:

| Artifact | Lines | Bytes | SHA-256 |
| --- | ---: | ---: | --- |
| m2-one-target-slice-project-fixture-map-amendment-11.md | 133 | 21819 | 1236466ca46f69c6677ebc8cf3d25f2023f43ce6ce535014758bf61154ff71c3 |
| m2-one-target-agent-schema-amendment-12.patch | 343 | 16034 | 6d990e1c7e59888a034414937f62e2cb11b133f071882033ffe278e74da6b30c |
| m2-one-target-agent-schema-review-amendment-12.md | 517 | 36212 | 09752b28b780901ef7738ff18f37145028c8349d60d78f5a4aa4968b1164767b |
| m2-one-target-agent-schema-code-review-1.md | 126 | 14150 | 8164b83fecd8d6db15d3b007a279d138fdabeb3aaa2d14a984d5733f9c0c5388 |
| m2-one-target-agent-schema-conservation-review-1.md | 107 | 16824 | 7b4ad9a01fae180d1d8d60328f1a282045d6cc4b63190fcbcfba6ab2b3f62092 |
| m2-one-target-agent-schema-security-review-1.md | 139 | 18091 | 140a2e89328801e4dda23f52a75d97a4fb282aea4b8caf3881ba06d6df50ea78 |
| m2-one-target-agent-schema-green-amendment-13.txt | 1072 | 72869 | f91699dca7b4bc5c50e59689888a0f3d00b5815b9c4b3c194813edb11716a5cc |

This proves catalog/validation compatibility only: no real Decide provenance,
admission, handler trust, ingestion, durability, T approval, production NEW
acceptance, activation or deployment is established. Production NEW remains
empty/default-off/structurally unadmitted. The separately mapped four-path
real-Decide fixture remains frozen until this checkpoint is durable. Overall
one-target work remains incomplete. This append leaves the three schema paths
staged and this log unstaged, pending exact log acceptance and separate
durability disposition. Human questions and owner-approval requests: none.
