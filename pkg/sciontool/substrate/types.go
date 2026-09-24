// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package substrate implements the Phase 1 `sciontool substrate-serve`
// control server: the in-actor HTTP server that the Substrate runtime
// broker bootstraps a scion agent through (phase1-spec.md §2.1). It is the
// template entrypoint for the `substrate` runtime; the broker/runtime side
// of the integration lives in pkg/runtime.
//
// The JSON request/response shapes in this file are a contract shared with
// the runtime client (pkg/runtime/substrate, built independently against
// the same spec) — do not change field names or types without updating
// both sides.
package substrate

// State is the control server's lifecycle state, returned by /healthz.
type State string

const (
	// StateAwaitingBootstrap is the initial state: the golden-snapshot
	// process is warm but has not yet received per-agent config.
	StateAwaitingBootstrap State = "awaiting-bootstrap"
	// StateRunning is entered after a successful one-shot bootstrap.
	StateRunning State = "running"
	// StateInitFailed is entered when the in-process init (InitRunner)
	// exits non-zero. The control server stays up and HTTP-reachable —
	// Substrate does not observe a PID 1 exit as a failure signal (the
	// actor stays ACTOR_STATE_RUNNING regardless), so healthz reporting a
	// distinct state here is what lets a caller that already knows to
	// probe it (e.g. a future broker-side liveness check) distinguish this
	// from a genuinely running harness — see the cmd layer's
	// exitCodePrivilegeDropRequired and reportInitFailure (called from
	// inside RunInit itself) for the direct Hub report, which is the
	// primary failure signal today. Reported over HTTP as a normal 200 OK
	// with body {"state":"init-failed"} — healthz's status code always
	// means "the control server itself is up and answering," never
	// "everything behind it succeeded"; the state field is where a caller
	// distinguishes success from this.
	StateInitFailed State = "init-failed"
)

// HealthzResponse is the body of GET /scion/v1/healthz, always returned
// with HTTP 200 — the status code reflects the control server's own
// liveness, not the state it reports. See each State constant's own doc
// comment for what State a caller can expect and what it means.
type HealthzResponse struct {
	State State `json:"state"`
}

// BootstrapFile is one file to write during bootstrap, matching
// ResolvedAuth.Files / file-type ResolvedSecrets on the runtime side.
type BootstrapFile struct {
	Path       string `json:"path"`
	Mode       uint32 `json:"mode"`
	ContentB64 string `json:"content_b64"`
}

// BootstrapRequest is the body of POST /scion/v1/bootstrap.
type BootstrapRequest struct {
	// Env is the full agent environment (SCION_AGENT_ID, SCION_HUB_ENDPOINT,
	// SCION_AUTH_TOKEN, SCION_GIT_CLONE_URL, ...).
	Env map[string]string `json:"env"`
	// Files are written to disk before the child process starts.
	Files []BootstrapFile `json:"files"`
	// StartCmd is the same command string the k8s runtime places in
	// SCION_START_CMD (a tmux invocation that starts the harness).
	StartCmd string `json:"start_cmd"`
	// ControlToken is the bearer token required on all later
	// /scion/v1/exec calls.
	ControlToken string `json:"control_token"`
}

// ExecRequest is the body of POST /scion/v1/exec.
type ExecRequest struct {
	Argv     []string `json:"argv"`
	User     string   `json:"user"`
	TimeoutS int      `json:"timeout_s"`
}

// ExecResponse is the body returned by POST /scion/v1/exec.
type ExecResponse struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
}
