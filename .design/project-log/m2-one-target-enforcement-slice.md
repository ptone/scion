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
