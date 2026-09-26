# Project Log: Explicit Attach Error for Substrate Agents

**Date:** 2026-09-26
**Component:** `cmd/attach.go`, `cmd/common.go` (client-side attach gates only); `.design/kubernetes/substrate-runtime.md` §4 (doc correction)

See `.design/kubernetes/substrate-runtime.md` §4 for the `SubstrateRuntime.Attach` contract as it now actually behaves. This entry records why attaching to a substrate-backed agent used to fail silently and where the fix landed.

## The defect

`scion attach <agent>` against a substrate-backed agent printed `Attaching to agent '<slug>' via Hub...` and then exited 0 with no visible error, leaving the user staring at a dead terminal. The same silent failure exists on `scion start -a` and `scion resume -a` for a substrate agent: both attach immediately after the agent reaches the running phase, using the same underlying WebSocket call.

The runtime broker's PTY code (`pkg/runtimebroker/pty_handlers.go`, both `LocalPTYSession.Run` and `StreamPTYHandler.Run`) already rejects a substrate agent's PTY session with a fixed internal error — but by the time either of those runs, the hub's own PTY handler (`pkg/hub/pty_handlers.go`, `handleAgentPTY`) has already upgraded the CLI's WebSocket connection. An error surfacing after a 101 Switching Protocols response has no clean HTTP status left to ride back on; the hub's `PTYSession.Run` only logs it and closes the socket. The CLI's own WebSocket client (`pkg/wsclient/pty.go`) sees an ordinary connection close, not a distinguishable error, so nothing non-zero-exits and nothing prints.

## The fix: reject before any WebSocket dial, using data already in hand

`cmd/attach.go`'s `attachViaHub` already calls the hub's agent-get endpoint (`ProjectAgents(projectID).Get`) before ever touching a WebSocket, to check the agent is running. That same response already carries `agent.Runtime` — populated from the runtime broker's own runtime-type string at agent-create time (`SubstrateRuntime.Name()` returns `"substrate"`; the broker propagates it through `AgentInfo.Runtime` → `AgentResponse.RuntimeType` → the hub's stored `Agent.Runtime` → the hub's agent-get JSON body). `attachViaHub` already special-cased one runtime family this way: a `managed:`-prefixed runtime returns a fixed, non-zero-exit error before any dial is attempted, for exactly the same reason (no PTY primitive to attach to).

Both checks — managed and substrate — now live in one shared helper, `attachUnsupportedErr(agentRuntime string) error` (`cmd/attach.go`), that returns the fixed error for a given runtime string or `nil` when attach is supported. `attachViaHub` calls it right after the agent-get response comes back, before the phase check, before token resolution, and before `wsclient.AttachToAgent` ever dials anything.

`cmd/common.go`'s `startAgentViaHub` has the same shape of bug at both of its own `wsclient.AttachToAgent` call sites — the workspace-upload branch's post-poll running check, and the main polling loop's `ready:` label — because both attach immediately after the agent reaches the running phase using the agent-get response's `Runtime` field. Both sites now call `attachUnsupportedErr` the same way: the workspace-upload branch checks it right after the phase check and before building the attach options; the `ready:`-label path captures `agent.Runtime` into a variable (`agentRuntime`) alongside `agent.ID` inside the polling loop, before `goto ready`, and checks it at the top of the `ready:` block, before transport resolution.

Cobra's own error path (`cmd/root.go Execute()`) already prints `Error: <message>` to stderr and calls `os.Exit(1)` for any `RunE` error — the same relay the managed-agent check, the "agent not found" check, and the "not running" check all already use. No new error code, wire field, or stream frame was introduced; nothing needed one, since the rejection never leaves the CLI process's own memory.

**Alternatives considered and rejected:**
- Rejecting inside the hub's `handleAgentPTY`, before its own WebSocket upgrade, mirroring the existing `runtime_logs_unsupported` pattern in `pkg/hub/handlers_logs.go`. This is a real, valid API-boundary fix — it would also protect a non-CLI caller of the same endpoint — but the CLI's `pty.go` `Connect()` currently only reports the raw HTTP status code on a failed handshake (`connection failed with status %d: %w`), not the response body's message; wiring that through would touch a file shared by every runtime's attach path (docker/k8s/apple/cloudrun included), for a client that already has everything it needs from the agent-get response it fetches on every attach anyway. Not taken here, since it changes shared infrastructure for a defect whose fix is already fully available client-side.
- Rejecting inside the runtime broker's direct-attach HTTP handler (`handleAgentAttach`, used only for direct-connect callers that bypass the hub) before its own upgrade. Doesn't fix the hub-mediated path the CLI actually uses, and touches broker-shared code for no benefit here.
- Having the broker's PTY stream send a new error frame back over the already-upgraded WebSocket. Explicitly out of scope: inventing a new frame protocol for a defect this narrow is disproportionate. The broker's PTY `Run()` only logging errors to broker-side logs, without forwarding any of them over the WebSocket to any caller, remains unaddressed by this change.

The runtime broker's existing substrate guards in `pty_handlers.go` (`LocalPTYSession.Run`, `StreamPTYHandler.Run`) are unchanged — they remain defense in depth for any caller that reaches the broker directly.

## Scope and parity

`cmd/attach.go` and `cmd/common.go` changed (plus their test files). No hub, broker, or `wsclient` code was touched. The new check is a single `==` comparison (via the shared `attachUnsupportedErr` helper) against a literal that only a substrate agent's `Runtime` field is expected to equal, gated before the phase check on every path, and every existing attach test in the package passes unchanged. This holds for agents whose stored `Runtime` was reported directly by the broker; for an agent with an empty stored `Runtime`, the hub's agent-get enrichment (`pkg/hub/handlers_agents_core.go`, around line 2089) fills it in from the first available profile on a single-profile broker, so the same gate still applies once enrichment runs. It does not by itself guarantee every other runtime's attach path (docker, k8s, apple, cloudrun) is unaffected on a broker with multiple available profiles and no stored `Runtime` — that scenario is outside what this change was scoped to verify.

The web UI terminal is a separate client from the CLI and is not covered by this change: it still gets only a post-upgrade close for a substrate agent. Fixing that needs a hub-side pre-upgrade rejection in `handleAgentPTY` and is left for that work.

## Tests

`cmd/attach_test.go`:
- `TestAttachViaHub_SubstrateAgent_ReturnsExplicitError` — a mock hub agent-get response with `Runtime: "substrate"` produces exactly the fixed message and nothing else (checked with `assert.Equal`, plus an explicit scan for infra-shaped leakage: atespace/namespace/URL/project-ID/agent-ID substrings).
- `TestAttachViaHub_DockerAgent_UnaffectedBySubstrateCheck` — a `Runtime: "docker"` agent passes the new check untouched and reaches the WebSocket dial step exactly as it did before this change; the assertion pins that the error is the dial failure from `pkg/wsclient/pty.go` (`"connection failed"`), not some other, earlier failure that happens to also be non-nil.
- `TestStartAgentViaHub_Site2_SubstrateAgent_ReturnsExplicitError` — the same fixed-message check as above, but through `startAgentViaHub`'s `ready:`-label path (attach after `start -a` / `resume -a`), using the existing `TestStartAgentViaHub_Site2_*` mock-hub harness with `Runtime: "substrate"`.

## Gates

`gofmt -l` (via `make fmt`) on the touched files is clean. `go build -buildvcs=false ./cmd/... ./pkg/hub/...` succeeds. `go vet ./cmd/` is clean. `go test ./cmd/...` passes, run with all `SCION_*` environment variables unset — those variables are present in this environment because it is itself a substrate-managed agent container, and several unrelated tests in this package read Hub/project state from them; with the env scrub, `go test ./cmd/...` is green with no pre-existing failures. No `-race`/`-cover`/full-suite run was taken.
