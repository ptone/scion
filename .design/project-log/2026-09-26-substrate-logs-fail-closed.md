# Project Log: Substrate Agent Logs Fail Closed

**Date:** 2026-09-26
**Component:** `pkg/runtime/substrate_runtime.go`, `pkg/runtimebroker`, `pkg/hub`, `deploy/substrate/broker.yaml`

See `.design/kubernetes/substrate-runtime.md` §4 for the runtime lifecycle table this entry updates, and `deploy/substrate/README.md`'s "Known Phase 1 limitations" → "Logs" for the operator-facing contract this results in.

## The exposure

`SubstrateRuntime.GetLogs` resolved an actor's assigned worker pod (`GetActor` → `status.worker_assignment`) and then read that pod's last 2000 lines via client-go's `PodLogs`, with no actor-, container- or time-scoped filter. Worker pods in this runtime are shared across atespaces and users — a pod can host actors from more than one project over its lifetime — so an unfiltered pod-level read returns every tenant's actor output that pod ever wrote, not just the requesting caller's, and any worker-level log lines that happen to name a different atespace. This is a cross-tenant information exposure, not a correctness bug, so the fix is to fail closed rather than to try to filter the read correctly in this phase.

## Inventory before touching anything

Every caller that could reach substrate `GetLogs` or any worker-pod log read was enumerated by grepping the whole call graph — the broker's `/logs` HTTP handler, the hub's log relay (`agent log relay failed`), `scion logs` in both hub and local mode, the web log viewer, the Cloud Logging–backed diagnostics/message-log endpoints, and the CLI's own post-start diagnostic read (`pkg/agent/run.go`, on a container that exited immediately after `Start`). The Cloud Logging–backed endpoints (cloud-logs, message-logs, admin diagnostics) never touch runtime `GetLogs` at all — a distinct code path querying Cloud Logging directly — so they were out of scope. `--follow` is already rejected outright in hub mode and reads a local file in local mode, so it never reaches the broker. No support-bundle or debug-dump mechanism exists in the codebase to check.

A second pass, specifically for the RBAC removal, confirmed nothing else in the substrate runtime or broker uses the Kubernetes `pods`/`pods/log` grant: `k8sClient` (the only Kubernetes clientset field on `SubstrateRuntime`) was referenced at exactly two sites before this change — the constructor and `GetLogs` — and nowhere in `Run`, `waitRunning`, `bootstrapNonce`, `Exec`, `Delete`, `Stop`, or `List`. The dialer's own Kubernetes call is a `ServiceAccounts(...).CreateToken` (TokenRequest), never a `Pods` call. The broker's PTY/exec switch explicitly short-circuits to an error for substrate before it would ever reach a Kubernetes call. No "describe worker" or debug helper exists in either package.

## The fix: an explicit sentinel, not a narrower filter

`GetLogs` now returns an exported sentinel (`ErrLogsNotSupported`) immediately, with no ateapi or Kubernetes call at all — not "try to scope the read and fail if that's not possible," but "this is not implemented, full stop," which is easier to prove closed (a client-interface fake with zero recorded actions) than to prove a filtered read is airtight. The broker maps the sentinel to an explicit 501 with a stable code (`runtime_logs_unsupported`) rather than letting it fall into the existing generic 500 "runtime_error" path, so a caller can distinguish "this feature does not exist here" from "the request failed." The fixed error text names no atespace, worker, pod, namespace, actor or agent — it is the same string regardless of which agent asked.

The hub's log relay handler mapped every broker error the same way before this change — a generic 502 wrapping the broker's status and JSON body as a string inside the message — which would have buried the clean 501/`runtime_logs_unsupported` response inside a re-serialized JSON blob under the wrong status and a wrong generic code. It now recognizes that specific status-and-code pair on the broker's error type and passes the broker's own status, code and message straight through, unchanged for every other error (matching on both status and code, not status alone, so no other 501 anywhere else picks up this treatment by accident).

## Why RBAC removal is defense in depth, not the primary fix

The broker's worker-namespace `Role`/`RoleBinding` granting `get` on `pods` and `pods/log` existed for exactly one caller. With that caller gone, the grant is both dead weight and a standing risk if a future change reintroduces a pod-log read without revisiting RBAC — removing it now means a regression would fail at the Kubernetes API layer, not just at the application layer. This is why the runtime-level fix (a sentinel with zero calls, provable in a unit test with a fake clientset) is the change that actually closes the exposure; the RBAC removal is a second, independent layer that only matters if the first one is ever weakened.
