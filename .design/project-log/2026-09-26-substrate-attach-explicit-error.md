# Project Log: Explicit `scion attach` Error for Substrate Agents

**Date:** 2026-09-26
**Component:** `cmd/attach.go` (client-side attach gate only); `.design/kubernetes/substrate-runtime.md` §4 (doc correction)

See `.design/kubernetes/substrate-runtime.md` §4 for the `SubstrateRuntime.Attach` contract as it now actually behaves. This entry records why `scion attach` on a substrate agent used to fail silently and where the fix landed.

## The defect

`scion attach <agent>` against a substrate-backed agent printed `Attaching to agent '<slug>' via Hub...` and then exited 0 with no visible error, leaving the user staring at a dead terminal. The runtime broker's PTY code (`pkg/runtimebroker/pty_handlers.go`, both `LocalPTYSession.Run` and `StreamPTYHandler.Run`) already rejects a substrate agent's PTY session with a fixed internal error — but by the time either of those runs, the hub's own PTY handler (`pkg/hub/pty_handlers.go`, `handleAgentPTY`) has already upgraded the CLI's WebSocket connection. An error surfacing after a 101 Switching Protocols response has no clean HTTP status left to ride back on; the hub's `PTYSession.Run` only logs it and closes the socket. The CLI's own WebSocket client (`pkg/wsclient/pty.go`) sees an ordinary connection close, not a distinguishable error, so nothing non-zero-exits and nothing prints.

## The fix: reject before any WebSocket dial, using data already in hand

`cmd/attach.go`'s `attachViaHub` already calls the hub's agent-get endpoint (`ProjectAgents(projectID).Get`) before ever touching a WebSocket, to check the agent is running. That same response already carries `agent.Runtime` — populated from the runtime broker's own runtime-type string at agent-create time (`SubstrateRuntime.Name()` returns `"substrate"`; the broker propagates it through `AgentInfo.Runtime` → `AgentResponse.RuntimeType` → the hub's stored `Agent.Runtime` → the hub's agent-get JSON body). `attachViaHub` already special-cases one runtime family this way: a `managed:`-prefixed runtime returns a fixed, non-zero-exit error before any dial is attempted, for exactly the same reason (no PTY primitive to attach to).

The fix adds a second, parallel check immediately after the managed-agent one: `agent.Runtime == "substrate"` returns a fixed error, `attach is not supported for agents on the substrate runtime in this phase`, before the phase check, before token resolution, and before `wsclient.AttachToAgent` ever dials anything. Cobra's own error path (`cmd/root.go Execute()`) already prints `Error: <message>` to stderr and calls `os.Exit(1)` for any `RunE` error — the same relay the managed-agent check, the "agent not found" check, and the "not running" check all already use. No new error code, wire field, or stream frame was introduced; nothing needed one, since the rejection never leaves the CLI process's own memory.

**Alternatives considered and rejected:**
- Rejecting inside the hub's `handleAgentPTY`, before its own WebSocket upgrade, mirroring the existing `runtime_logs_unsupported` pattern in `pkg/hub/handlers_logs.go`. This is a real, valid API-boundary fix — it would also protect a non-`scion attach` caller of the same endpoint — but the CLI's `pty.go` `Connect()` currently only reports the raw HTTP status code on a failed handshake (`connection failed with status %d: %w`), not the response body's message; wiring that through would touch a file shared by every runtime's attach path (docker/k8s/apple/cloudrun included), for a client that already has everything it needs from the agent-get response it fetches on every attach anyway. Left as a possible follow-up, not taken here to keep the change scoped to the one code path with the defect.
- Rejecting inside the runtime broker's direct-attach HTTP handler (`handleAgentAttach`, used only for direct-connect callers that bypass the hub) before its own upgrade. Doesn't fix the hub-mediated path `scion attach` actually uses, and touches broker-shared code for no benefit here.
- Having the broker's PTY stream send a new error frame back over the already-upgraded WebSocket. Explicitly out of scope: inventing a new frame protocol for a defect this narrow is disproportionate, and the generic "any PTY `Run()` error is broker-log-only" gap this would really be fixing is tracked separately.

The runtime broker's existing substrate guards in `pty_handlers.go` (`LocalPTYSession.Run`, `StreamPTYHandler.Run`) are unchanged — they remain defense in depth for any caller that reaches the broker directly.

## Scope and parity

Only `cmd/attach.go` changed (plus its test file). No hub, broker, or `wsclient` code was touched, so every other runtime's attach path — docker, k8s, apple, cloudrun — is provably unaffected: the new check is a single `==` comparison against a literal that only a substrate agent's `Runtime` field ever equals, gated after the managed-agent check and before the phase check, and every existing attach test in the package passes unchanged.

## Tests

Two new table-shaped unit tests in `cmd/attach_test.go`, following the existing mock-hub-server pattern already used for the managed-agent and IAP-gate regression tests:
- `TestAttachViaHub_SubstrateAgent_ReturnsExplicitError` — a mock hub agent-get response with `Runtime: "substrate"` produces exactly the fixed message and nothing else (checked with `assert.Equal`, plus an explicit scan for infra-shaped leakage: atespace/namespace/URL/project-ID/agent-ID substrings).
- `TestAttachViaHub_DockerAgent_UnaffectedBySubstrateCheck` — a `Runtime: "docker"` agent passes the new check untouched and reaches the WebSocket dial step exactly as it did before this change (the mock server doesn't implement a WS upgrade, so the dial itself fails — that failure is the proof the gate was cleared).

## Gates

`gofmt -l` on the touched files is clean. `go build -buildvcs=false ./...` succeeds. `go vet ./cmd/...` is clean. `GOGC=40 golangci-lint run --new-from-rev=8a9e02439 --concurrency=1 ./cmd/...` reports zero issues. `go test ./cmd/...` passes both new tests and every existing attach/PTY-adjacent test in the package unchanged; three failures pre-exist in unrelated files in the same package (`TestHubAllOrOneActions` — real-network 401 against the community hub endpoint; `TestRequireImageRegistryForBroker_Settings` and `TestInitPluginManager_MigratesConfigFileOnlyPlugin` — environment/global-config-state dependent) and are unaffected by this change (confirmed unrelated by file and by the substance of their failures). No `-race`/`-cover`/full-suite run was taken, per the standing one-at-a-time cap on this branch.
