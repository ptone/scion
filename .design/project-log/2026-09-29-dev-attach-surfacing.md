# Attach-refusal surfacing fix

**Date:** 2026-09-29
**Branch:** `scion/attach-surfacing-fix` (rooted at `3915bc166c86263e0926c1b0063c1b97e3b95166`)

## What changed

- `cmd/attach.go`: `attachSupportedByBroker` no longer defaults to
  "supported" when the runtime broker's point-GET fails. It falls back to
  matching the same broker by ID in the `RuntimeBrokers().List` response,
  and only refuses before dialing (fixed message, no raw server text) when
  neither read can produce the record. A record that was read, by either
  path, keeps the existing profile-then-broker-wide ruling unchanged.
- `pkg/wsprotocol/pty_close.go`: added `ClosePTYAttachUnsupported` (4501) /
  `attach_unsupported`, a close code distinct from the retriable
  4503/session_not_ready it used to share with an actual readiness failure.
  Mirrored into the close-code parity fixture and the TypeScript client.
- `pkg/runtimebroker/controlchannel.go`: the control-channel pre-check that
  refuses a stream before starting the tmux exec now emits
  4501/attach_unsupported. The direct-connect pre-upgrade path keeps its
  existing 501/runtime_attach_unsupported HTTP response — no WebSocket has
  been upgraded yet at that point, so there is no close frame to change.
- `pkg/wsclient/pty.go`: a 4501 close arriving after the WebSocket is
  already upgraded now maps to an explicit "attach is not supported for
  this agent's runtime" error with a non-zero exit, instead of a raw
  close-code error string.
- `pkg/hub/pty_handlers.go`: the "PTY session started"/"ended" log lines now
  carry `routed_broker_id` (agent.RuntimeBrokerID, the broker the stream is
  actually routed to via OpenStream) as its own field, distinct from the
  process-wide `broker_id` attr a combo-mode server attaches to every log
  line (which names the locally co-located broker instead).
- Investigated the second-attempt 1000 close reported live: the CLI's own
  `readFromStdin` treats a non-TTY stdin already at EOF as a clean detach
  (nil, not an error), so it sends its own normal-closure frame and exits 0
  before any broker rejection can arrive. This is the client genuinely
  closing, not a hub defect, so no hub change follows from it. Added a
  deterministic reproduction in `pkg/wsclient` and left the CLI
  refuse-when-stdin-not-a-TTY idea as a proposal, not an implementation.

## Why

Attach on a runtime whose broker doesn't support it was surfacing three
separate problems: the CLI's pre-dial check fail-opened for a principal
denied the broker's point-GET (but not its LIST), the broker-side refusal
was indistinguishable on the wire from a transient readiness failure, and
the hub's PTY session log couldn't say which broker actually enforced (or
refused) a given attach. Each is now a distinct, correctly-attributed
signal instead of three ways of looking like "it just didn't work".

## Verification

Env-scrubbed `go build ./...`, `go vet ./...`, and `gofmt -l` are clean.
`go test -count=1` is green on `cmd`, `pkg/runtimebroker`, `pkg/hub` (and
its subpackages), `pkg/wsprotocol`, and `pkg/wsclient`. Scoped
`golangci-lint run --new-from-rev` reports 0 issues. Full detail, including
the root-cause writeup for the 1000-close investigation, is in
`attach-surfacing-report.md`.
