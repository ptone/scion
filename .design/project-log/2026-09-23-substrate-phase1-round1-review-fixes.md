# Substrate Phase 1 — round 1 review fixes (sb-dev-2's files)

sb-rev's round 1 review (`reviews/round-1-sb-rev.md`, verdict REQUEST
CHANGES) found one Critical and four Required items, all in sb-dev's
`pkg/runtime/...` (addressed separately by sb-dev), plus items in my files:
finding #9 (Consider/Low) and #12 (Nit), and an FYI relayed by sb-em as an
actionable follow-up. This entry covers my three fixes; sb-dev's fixes for
findings 1–5 are their own log entry.

## #9 — `writeBootstrapFile` didn't enforce mode on pre-existing files (Consider · Low)

`pkg/sciontool/substrate/server.go`: `os.WriteFile(path, content, mode)`'s
`mode` argument only applies to a **newly created** file's `open(2)` call,
and even then is subject to the process umask. It has no effect at all on a
file that already exists — `WriteFile` on an existing path just truncates
and rewrites the contents, mode untouched. A credential file baked into the
image at 0644 that a bootstrap payload asks to tighten to 0600 would
silently stay at 0644.

**Fix:** added an explicit `os.Chmod(f.Path, mode)` after `os.WriteFile`
succeeds, so the requested mode always wins regardless of umask or a
pre-existing file. Test (Prove-It Pattern):
`TestWriteBootstrapFile_EnforcesModeOnPreExistingFile` pre-creates a file at
0644, bootstraps content requesting 0600, and asserts the final mode is
exactly 0600. Verified it fails without the fix (`git stash` the one-line
change, rerun: `mode = -rw-r--r--, want 0600`) and passes with it.

## #12 — `ClusterRole` for `clustertrustbundles` wasn't scoped to one bundle (Nit)

`deploy/substrate/broker.yaml`: the `scion-substrate-broker-ctb-reader`
`ClusterRole` granted `get` on `certificates.k8s.io/clustertrustbundles`
with no `resourceNames`, i.e. read access to *every* ClusterTrustBundle in
the cluster (all signers, all tenants), when the broker only ever needs the
one named by `substrate.cluster_trust_bundle`.

**Fix:** added `resourceNames: ["${CLUSTER_TRUST_BUNDLE_NAME}"]` — same
placeholder already used in the ConfigMap, so it can't drift from the value
the runtime actually reads.

## FYI, escalated to a fix by sb-em: the router NetworkPolicy alone doesn't close the §5 fallback's threat model

sb-rev's FYI (not a numbered finding, since FYIs don't block merge under the
`code-review` skill) pointed out something the round 1 manifest missed
entirely: **the §5 fallback nonce's security rests on the router being the
*only* path to an actor's control server on :80.** The `atenet-router`
NetworkPolicy from the first pass only restricts the router's ingress — it
says nothing about whether a worker/actor pod's :80 is *also* reachable
directly by pod IP from any namespace, bypassing the router (and the
single-shot bootstrap check with it) entirely. sb-em treated this as
required, not optional, given what it protects.

**Fix:** added a second `NetworkPolicy`
(`scion-worker-restrict-ingress`) in `SUBSTRATE_WORKER_NAMESPACE`:
`podSelector: {}` (no confirmed, stable actor-pod label to narrow it
further — documented as a Phase 1 limitation), ingress allowed only from
`ATE_SYSTEM_NAMESPACE` (where both `atenet-router`, the intended path, and
`atelet`, the node agent managing the sandbox, live), restricted to TCP
`:80` — the actor control server's port, i.e. exactly what this policy
exists to protect. Added a corresponding verification step to the README:
resolve a real worker pod's IP, confirm a probe pod outside `ate-system` is
refused and one inside connects — mirroring the router verification already
there, and updated the "Bootstrap auth is the §5 fallback" limitation to
name both policies as jointly required, not just the router one.

## README/placeholder table: filled with `infra/cluster.md`'s `substrate-scion-test` values

`infra/cluster.md` landed with concrete values for this specific test
cluster (relayed by sb-em):
`CLUSTER_TRUST_BUNDLE_NAME=servicedns.podcert.ate.dev:identity:primary-bundle`,
`SANDBOX_CONFIG_NAME=gvisor-default`, `SUBSTRATE_WORKER_NAMESPACE=scion-agents`
(not `ate-system`, which round 1's placeholder table had guessed as the
unconfirmed default), `worker_selector: {ate.dev/worker-pool: scion-agents}`,
`SNAPSHOT_STORAGE_URI=gs://snapshot-substrate-scion-test`. Filled the
README's placeholder table and the "Apply order" worked example with these
as the `substrate-scion-test` case; `worker_selector` stays a hand-edit
(not an envsubst placeholder, since it's a map — see the README for why)
but is now called out with its concrete value front and center rather than
left as `{}` with a generic comment.

## Validation

- `pkg/sciontool/substrate/server.go` / `server_test.go`: `go build
  ./pkg/sciontool/substrate/...` pass; `go test ./pkg/sciontool/...
  ./cmd/sciontool/...` pass except the same pre-existing, unrelated
  `TestNativeTelemetryPolicyEffectiveChildEnv/disabled` failure noted in the
  earlier substrate-serve log entry (re-confirmed still present on this
  branch tip, still untouched by any of my changes).
  `golangci-lint run --new-from-rev=origin/scion/substrate-integration
  ./pkg/sciontool/substrate/... ./cmd/sciontool/...`: 0 issues. `gofmt -l`:
  clean.
- `deploy/substrate/broker.yaml`: re-validated with `kubeconform -strict`
  against both the raw placeholder file and a dummy-filled render (now using
  the actual `substrate-scion-test` values, not generic dummies) — 12
  resources (was 11; the new worker NetworkPolicy), all valid, 0 errors.

## Not in scope for me this round

Findings 1–5 (Critical/Required) are all in `pkg/runtime/...`
(`substrate_runtime.go`, `substrate_bootstrap.go`, `substrate_egress.go`) —
sb-dev's files, addressed in their own round 1 fixes. Findings 6, 7, 8, 10,
11, 13 (Consider/Nit, not assigned to me) are also `pkg/runtime`/
`pkg/config` items.
