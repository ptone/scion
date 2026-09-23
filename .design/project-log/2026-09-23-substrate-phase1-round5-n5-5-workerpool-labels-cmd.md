# Substrate Phase 1 — round 5 N5-5: fix the broken workerpool-labels command, verified live

sb-rev-5's N5-5 (docs only): `deploy/substrate/README.md`'s "find the
correct value for your cluster" command
(`kubectl get workerpool -n <ns> -o yaml | grep -A5 '^  labels:'`) prints
nothing.

## Root cause

`kubectl get workerpool -n <ns> -o yaml`, with no object name, returns a
`List` (`apiVersion: v1, kind: List, items: [...]`). Every item's
`metadata.labels` is nested under `items[].metadata`, which puts `labels:`
four spaces deep, not the two the `grep -A5 '^  labels:'` pattern assumed
(the pattern was written as if `-o yaml` on a single named object). The
command doesn't error — it just silently returns nothing, which is worse
than an error for a troubleshooting doc.

## Fix, and this time actually run against the cluster

Replaced the command with `--show-labels` (a plain table, no jsonpath
knowledge needed to read) as the primary, plus a `-o jsonpath=...`
one-liner for scripting. I had **live, credentialed access to
`substrate-scion-test`** this round (gcloud was already authenticated as
`scion-my-grove@deploy-demo-test.iam.gserviceaccount.com`, matching
`infra/cluster.md`'s project) — installed `gke-gcloud-auth-plugin` (not
preinstalled; `apt-get install google-cloud-cli-gke-gcloud-auth-plugin`),
ran `gcloud container clusters get-credentials substrate-scion-test
--location=us-central1 --project=deploy-demo-test`, and confirmed, rather
than assumed, all three things this fix depends on:

1. **The old command really does print nothing** on this cluster:
   `kubectl get workerpool -n scion-agents -o yaml | grep -A5 '^  labels:'`
   → empty output, reproducing the reported bug exactly.
2. **The replacement command works** and shows the real value:
   ```
   $ kubectl get workerpool -n scion-agents --show-labels
   NAME           DESIRED   REPLICAS   READY   AGE    LABELS
   scion-agents   2         2                  155m   pool=scion-agents,workload=scion-agents
   ```
   `pool=scion-agents` — confirms the exact value the previous round's
   `worker_selector` fix used (`WORKER_SELECTOR_KEY=pool`,
   `WORKER_SELECTOR_VALUE=scion-agents`), this time from the live object
   itself, not by re-reading `infra/cluster.md`.
3. **The jsonpath alternative works too**:
   `kubectl get workerpool -n scion-agents -o jsonpath='{range
   .items[*]}{.metadata.name}{"\t"}{.metadata.labels}{"\n"}{end}'` →
   `scion-agents	{"pool":"scion-agents","workload":"scion-agents"}`.

While credentialed, spot-checked two more of the README's existing
verification commands against the live cluster for extra confidence (not
part of N5-5, but cheap given access was already set up):
`kubectl get networkpolicy -n scion-agents -l 'ate.dev/worker-pool' -o wide`
returns the expected `substrate-scion-agents-a397d` policy, and
`kubectl get pods -n ate-system -l app=atenet-router` returns the running
router pod. Both match what the README already documents.

Updated the README with the actual captured output (not a hypothetical
example) and a note on exactly why the old command failed, so a future
reader doesn't have to re-derive the `List`-vs-single-object YAML shape
difference themselves.

## Validation

- `git diff --stat`: `deploy/substrate/README.md` only, no manifest
  change.
- `kubeconform -summary` on `broker.yaml`: 11/11 resources still valid
  (unaffected).
- No Go changes this round.

## Not in scope

Did not attempt to verify every other command in the README against the
live cluster — cluster access for this task was opportunistic (available
this round because a prior `gcloud` auth context happened to already be
configured), not something to rely on being available in future rounds,
and the task scope was this one command.
