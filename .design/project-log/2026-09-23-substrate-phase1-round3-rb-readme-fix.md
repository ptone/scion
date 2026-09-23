# Substrate Phase 1 — round 3 R-B: fix the NetworkPolicy enforcement check and restart guidance

sb-rev-3's round 3 review (`reviews/round-3-sb-rev-3.md`) found R-B in the
README I added in the previous round (`f6612c57f`): the enforcement check
gives a false negative on the actual reference cluster, and the restart
guidance omits the one pod whose restart matters most for the §5 fallback.
Both problems traced back to writing the "Operational prerequisites"
section from general GKE knowledge instead of checking `infra/cluster.md`
closely enough — it already had the answer to both.

## Problem 1: the enforcement check gives a false negative on Calico

The check I'd written (`datapathProvider` only, expect `ADVANCED_DATAPATH`)
only detects Dataplane V2. `substrate-scion-test` — the cluster this README
is written against — enforces via the **Calico** NetworkPolicy add-on
(`infra/cluster.md`, "NetworkPolicy Enforcement (Calico)"), not Dataplane
V2. On that cluster the check reports `datapathProvider=LEGACY_DATAPATH`,
which reads as "enforcement is off" when it's actually on. The README
named Calico explicitly in the prerequisite text while the check it
pointed to couldn't detect Calico at all — a direct contradiction within
the same document.

**Fix:** check both signals in one command:
```sh
gcloud container clusters describe <cluster> --location=<location> --project=<project> \
  --format='value(networkConfig.datapathProvider,networkPolicy.enabled,networkPolicy.provider)'
```
Expect `ADVANCED_DATAPATH` **or** `True CALICO`
(`networkPolicy.enabled=True`, `networkPolicy.provider=CALICO`). Added a
note to also check `addonsConfig.networkPolicyConfig.disabled` is `false`
where available, since the add-on being enabled per the fields above
doesn't rule out a later config change disabling it. Both the "Operational
prerequisites" callout (README lines ~24-51) and the "Known Phase 1
limitations" bullet (~384-406) now reference this cluster's actual
enforcement mechanism, not a generic "GKE Dataplane V2" assumption.

## Problem 2: "restart every worker pod" omits the router itself

`infra/cluster.md` step 4 (the procedure actually executed on
`substrate-scion-test`) restarted **all** workload pods: `ate-system`
(explicitly including `atenet-router`), the workers, and the `atelet`
DaemonSet. My round-2 text said "rolling-restarting every worker pod" —
which reads as "the worker pods" and omits the router pod entirely. Pods
created before Calico keep GKE's PTP CNI and get no enforcement regardless
of the NetworkPolicy object's existence. An operator who restarts only the
workers (the pods the phrase most naturally refers to) leaves the
**router** pod unenforced — and the router NetworkPolicy is the one
control the §5 first-bootstrap-wins fallback actually depends on. This
isn't a peripheral gap: `kubectl get networkpolicy` still shows the object
as applied either way, so nothing signals the miss short of the
traffic-level verification steps already in the README (which would catch
it, but only after the fact, and only if run).

**Fix:** rewrote the prerequisite as a numbered procedure (matching
`infra/cluster.md`'s own steps) naming, explicitly, every pod set that must
be restarted — `atenet-router` in `${ATE_SYSTEM_NAMESPACE}` first, then the
worker pods, then the `atelet` DaemonSet — with a direct statement that an
unrestarted router pod leaves `atenet-router-restrict-ingress` completely
unenforced, silently. Added the `projectcalico.org/ds-ready=true`
node-labeling step (`infra/cluster.md` step 3) that the original text
omitted entirely — without it, `calico-node` never schedules onto nodes
that existed before the Calico rollout, so restarting pods on those nodes
still doesn't get them enforcement.

## Validation

- `kubeconform -summary` on `broker.yaml`: 11/11 resources still valid
  (README-only change, no manifest edit — confirmed via `git diff --stat`
  touching only `README.md`).
- No Go changes this round.

## Not in scope for me this round

R-A (egress validator bypass rows from round 3) and O-1/O-2/O-3/N-1 are all
`pkg/runtime`/`pkg/config`/`pkg/sciontool/substrate` — sb-dev's files this
round, or general code items not assigned to me.
