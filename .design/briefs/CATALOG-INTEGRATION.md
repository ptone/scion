# Brief: declare the messaging admin route in the authorization catalog

## Base — read this first, do not guess

Branch: `scion/tranche-g-mainmerge` at **`8249312ae`** on **`https://github.com/ptone/scion.git`**.

This is tranche-g merged with upstream main `58e68918`, which brought in the
authorization audit refactor. Do **not** branch from `main`, from `tranche-g`,
or from anything you already have checked out. Fetch this exact SHA.

```
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git -C /workspace fetch -q "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
    scion/tranche-g-mainmerge:refs/rt/tgm --force
git -C /workspace worktree add --detach /tmp/wt-catalog refs/rt/tgm -q
```

Confirm `git rev-parse --short HEAD` prints `8249312ae` before you edit anything.
`origin` in your container does **not** point at ptone/scion. Use the explicit
token URL above for every fetch and push.

## The problem

Main's new `make check-authorization-catalog` gate requires every registered
route and every registered permission to be declared in the operation catalog
at `pkg/hub/authzop/catalog.go`. Our messaging admin route predates that gate,
so it is undeclared and the gate fails — correctly, fail-closed.

Two tests are red **because of our work**:

1. `TestRegisteredPermissionsConsumed` — `hub.messaging.update` is registered in
   `pkg/hub/permissions/registry.go` but no catalog operation consumes it.
   Merged tree reports 83/121; main's baseline is 83/120.
2. `TestEntryPointsCoverRouteMetadata` — `/api/v1/admin/messaging` is in
   `pkg/hub/route_metadata.go` but no catalog operation or exemption covers it.
   Merged tree reports 178 routes / 177 covered; main's baseline is 177/177.

Your job is to take exactly those two from red to green.

## What you must NOT do

**Do not make the gate pass by weakening the gate.** In this task that trap has
a specific, tempting shape: the gate is also satisfiable by adding the route to
an **exemption** list. An exemption declares "this route does not need
authorization". That is false for our route and would be a security regression
dressed as a passing build. **Add an operation. Never an exemption.**

Do not edit the gate, its tests, its thresholds, or `hack/check-authorization-catalog.sh`.

Do not change any runtime behaviour. The catalog is a *description* of
enforcement that already exists. If you find yourself editing a handler, a
route registration, or the permission registry to make the catalog fit, stop
and message me — that means the description and the enforcement disagree, and
which one is wrong is my call, not yours.

## What to do

Add one operation to `pkg/hub/authzop/catalog.go` in the `hub` domain, adjacent
to the existing `hub.config.update` entry (~line 1152), which is a close
structural analogue.

**Derive every field from the enforcement that actually exists — do not copy the
neighbour and assume.** Read these and let them tell you the values:

- `pkg/hub/route_metadata.go`, entry `admin.messaging` — classification,
  permission, resource, action.
- `pkg/hub/admin_messaging.go`, `handleAdminMessaging` — the HTTP methods it
  actually accepts, and the principal/credential it actually requires.
- `pkg/hub/route_classification_test.go` — the `case` statement listing
  `/api/v1/admin/messaging` alongside the other PUT-method admin routes.
- `pkg/hub/permissions/registry.go`, `hub.messaging.update`.

Note there is already a catalog entry covering `/api/v1/admin/messaging/divergence`
(~line 1253). That is a *different* route (GET, diagnostics). Do not fold ours
into it and do not modify it.

If the method set in the handler disagrees with what route_classification_test.go
implies, report the disagreement to me rather than picking one.

## How to verify — measure, don't assume

Run the gate on your tree and capture the whole output to a file. Do not pipe a
measurement through `head` or `tail` before it lands in the log.

```
export GOCACHE=/tmp/gocache-catalog     # the shared gocache is contended
cd /tmp/wt-catalog && make check-authorization-catalog > /tmp/catalog-after.log 2>&1; echo "exit=$?"
```

Required end state, all three:

- `TestRegisteredPermissionsConsumed` — **PASS**, and the consumed count moves
  84/121. If the denominator changes, you added a permission; you were not asked to.
- `TestEntryPointsCoverRouteMetadata` — **PASS**, at 178 routes / 178 covered.
- `TestMutationClassificationBidirectional` — **still fails at exactly 200
  discovered / 198 classified**, with the two unclassified being
  `pkg/hub/handlers_agents_core.go:ensureHostSARecord:CreateGCPServiceAccount`
  and `pkg/hub/brokerauth.go:CompleteBrokerJoin:DeleteJoinToken`.

That third one is **inherited from upstream main and is not yours to fix.** It
fails identically on a clean main checkout. Leave it alone. But watch those
numbers: if 200/198 moves in either direction, you have changed something
outside your scope and I need to know.

Then confirm no collateral damage:

```
go build ./... ; echo "build=$?"
go vet ./...   ; echo "vet=$?"
make compat-literals check-authz-guards check-conversation-upsert-guard check-security-marker-gates
go test ./pkg/hub/authzop/ ./pkg/hub/permissions/ 2>&1 | tee /tmp/pkg-after.log
```

`gofmt -l` your changed files. Note that `pkg/hub/handlers_agents_core.go` and
`pkg/hub/web_test.go` are gofmt-dirty on this tree — that is inherited from
upstream main, it is not yours, and you must **not** reformat them.

`pkg/hub` as a whole takes ~7 minutes to compile and test. If you run it, run it
in the foreground. A backgrounded job's exit code belongs to the launcher, not
the job — check the log actually has bytes in it before believing a green.

## Reporting

Report to me (`ca-msg-arch`) before pushing, with:

- `git diff --numstat` per file, run by you, pasted raw.
- The before/after numbers for all three tests named above.
- Anything you changed that this brief did not ask for, called out explicitly.
  If it is out of scope, route it to me — do not grade it as harmless and keep it.

If your numbers disagree with the ones in this brief, **your numbers are the
finding.** Report them; do not adjust your work to match my prose.

Push only after I approve:

```
git push "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
    HEAD:refs/heads/scion/tranche-g-mainmerge
git ls-remote "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
    'refs/heads/scion/tranche-g-mainmerge'
```

Never push to `main` or to `tranche-g`.
