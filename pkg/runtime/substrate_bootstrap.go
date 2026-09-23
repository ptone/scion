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

package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// Substrate control server routes (phase1-spec.md §2.1), reachable through
// the router with the ate-target-actor header.
const (
	substrateHealthzPath   = "/scion/v1/healthz"
	substrateBootstrapPath = "/scion/v1/bootstrap"
	substrateExecPath      = "/scion/v1/exec"
)

// Per-request deadlines for router calls. RouterClient itself carries no
// blanket timeout (see its doc comment), so every call here sets its own —
// a single fixed client timeout can't fit both a quick healthz probe and an
// exec whose timeout_s is caller-chosen and can legitimately run longer
// than that (review round 1, Consider #8).
const (
	// healthzRequestTimeout bounds one GET /healthz attempt. waitForHealthz
	// retries across its own outer timeout/backoff, so this only bounds a
	// single attempt.
	healthzRequestTimeout = 10 * time.Second
	// bootstrapRequestTimeout bounds the one-shot POST /bootstrap call.
	bootstrapRequestTimeout = 30 * time.Second
	// execTimeoutSlack is added on top of the caller-requested exec
	// timeout_s to get doExec's own context deadline, so the HTTP round
	// trip has room for the server's own timeout_s enforcement plus
	// network/processing overhead, instead of racing it.
	execTimeoutSlack = 10 * time.Second
)

// healthzAwaitingBootstrap and healthzRunning are the two states substrate-serve's
// GET /scion/v1/healthz reports (phase1-spec.md §2.1).
const (
	healthzAwaitingBootstrap = "awaiting-bootstrap"
	healthzRunning           = "running"
)

// bootstrapFile is one entry of the bootstrap payload's "files" array.
// mode is a plain decimal file mode (e.g. 384 == 0600), matching the spec
// example — not octal text — so it round-trips through JSON as a number.
type bootstrapFile struct {
	Path       string `json:"path"`
	Mode       int    `json:"mode"`
	ContentB64 string `json:"content_b64"`
}

// bootstrapRequest is the POST /scion/v1/bootstrap body (phase1-spec.md §2.1).
type bootstrapRequest struct {
	Env          map[string]string `json:"env"`
	Files        []bootstrapFile   `json:"files"`
	StartCmd     string            `json:"start_cmd"`
	ControlToken string            `json:"control_token"`
}

// healthzResponse is the GET /scion/v1/healthz body.
type healthzResponse struct {
	State string `json:"state"`
}

// execRequest is the POST /scion/v1/exec body.
type execRequest struct {
	Argv     []string `json:"argv"`
	User     string   `json:"user"`
	TimeoutS int      `json:"timeout_s"`
}

// execResponse is the POST /scion/v1/exec body. Truncated flags that stdout
// or stderr hit the 4 MiB per-stream cap (phase1-spec.md §2.1); the field
// name is provisional pending sb-em/sb-dev-2 agreement (brief §Scope 5).
type execResponse struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
}

// defaultFileMode is applied to every bootstrap file. Neither
// api.FileMapping nor api.ResolvedSecret carries a mode, so a single
// conservative, owner-only mode is used for all of them — matching the
// phase1-spec.md §2.1 example (384 decimal == 0600 octal).
const defaultFileMode = 0o600

// buildBootstrapEnv assembles the full agent env for the bootstrap payload.
//
// phase1-spec.md §2.2 step 8 states the formula as cfg.Env +
// ResolvedAuth.EnvVars + env-type ResolvedSecrets. This also folds in
// cfg.Harness.GetEnv()/GetTelemetryEnv(), which every other runtime
// includes (see buildCommonRunArgs, KubernetesRuntime.buildPod) and which
// the harness needs to run at all (model, task and telemetry env are not
// otherwise present in cfg.Env). Flagged to sb-em as a spec/behavior
// question (see project log) rather than silently narrowed to the literal
// formula, since narrowing it would ship a harness that cannot start.
func buildBootstrapEnv(cfg RunConfig) map[string]string {
	env := make(map[string]string)

	if cfg.Harness != nil {
		for k, v := range cfg.Harness.GetEnv(cfg.Name, util.GetHomeDir(cfg.UnixUsername), cfg.UnixUsername) {
			if v != "" {
				env[k] = v
			}
		}
		if cfg.TelemetryEnabled {
			for k, v := range cfg.Harness.GetTelemetryEnv() {
				env[k] = v
			}
		}
	}

	for _, e := range cfg.Env {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}

	if cfg.ResolvedAuth != nil {
		for k, v := range cfg.ResolvedAuth.EnvVars {
			env[k] = v
		}
	}

	for _, s := range cfg.ResolvedSecrets {
		if s.Type == "environment" || s.Type == "" {
			env[s.Target] = s.Value
		}
	}

	// SCION_RUNTIME=substrate lets sciontool disable autoexpose/port-forward
	// (blocked by Substrate's default-deny, no-WebSocket-egress posture —
	// findings.md §1, §10) so it does not spin retrying a tunnel that can
	// never connect.
	env["SCION_RUNTIME"] = "substrate"

	return env
}

// substrateSecretCandidates returns every value from cfg that must never
// appear in an error string, keyed by the name/target that identifies it in
// a redaction marker: harness env/telemetry env, cfg.Env, ResolvedAuth's
// env vars, and both env-type and file-type ResolvedSecrets.
//
// This is deliberately its own function, not argv_redact.go's
// externalEnvValues (written for cloudrun-sandbox's argv construction) and
// not buildBootstrapEnv's output either:
//
//   - externalEnvValues only covers cfg.Env and Harness.GetEnv(), and
//     cross-checks against a final env map — it silently has no coverage
//     for ResolvedAuth.EnvVars or ResolvedSecrets at all, which is exactly
//     what let real secret values reach an unredacted error (review round
//     1, Required #3).
//   - buildBootstrapEnv's output isn't reusable as-is either: it adds
//     SCION_RUNTIME=substrate, a runtime-synthesised constant that is also
//     a substring of every one of this runtime's own error-message
//     prefixes ("substrate: ..."). Feeding that into redactEnvValues (a
//     blunt substring replace over the whole error text) would rewrite
//     "substrate" everywhere it appears, corrupting unrelated error
//     messages. If buildBootstrapEnv or this function ever gains another
//     synthesised constant, keep mirroring the *external* sources here
//     rather than importing the other function's whole output, so this bug
//     class can't recur silently.
func substrateSecretCandidates(cfg RunConfig) map[string]string {
	secrets := make(map[string]string)
	// add keys by "<source>:<name>" rather than bare name. Two different
	// sources can legitimately use the same name (e.g. a file-type
	// ResolvedSecret named "GITHUB_TOKEN" alongside a cfg.Env
	// "GITHUB_TOKEN=..." entry) — with a bare-name map, the second one
	// added would silently overwrite the first map entry, and the
	// overwritten source's value would stay in the request but drop out of
	// the redaction set (review round 2, Consider O1(b)). The source
	// prefix also makes the redaction marker in an error message more
	// informative ("[value of secret_file:GITHUB_TOKEN redacted]" instead
	// of just the name).
	add := func(source, key, value string) {
		if value == "" {
			return
		}
		secrets[source+":"+key] = value
	}

	if cfg.Harness != nil {
		for k, v := range cfg.Harness.GetEnv(cfg.Name, util.GetHomeDir(cfg.UnixUsername), cfg.UnixUsername) {
			add("harness_env", k, v)
		}
		if cfg.TelemetryEnabled {
			for k, v := range cfg.Harness.GetTelemetryEnv() {
				add("telemetry_env", k, v)
			}
		}
	}

	for _, e := range cfg.Env {
		if k, v, ok := strings.Cut(e, "="); ok {
			add("cfg_env", k, v)
		}
	}

	if cfg.ResolvedAuth != nil {
		for k, v := range cfg.ResolvedAuth.EnvVars {
			add("resolved_auth_env", k, v)
		}
		// ResolvedAuth.Files' contents (credential JSON, tokens, etc. read
		// from SourcePath) go into the bootstrap payload the same as any
		// other secret and are just as much a candidate for leaking into
		// an error message (review round 1's original call-out, "bootstrap-
		// file contents", was never actually covered — review round 2,
		// Consider O1(a)). A read failure here is swallowed: it can't leak
		// content it never read, and buildBootstrapFiles independently
		// surfaces the read error (naming only the path, never content).
		for _, f := range cfg.ResolvedAuth.Files {
			if f.SourcePath == "" {
				continue
			}
			if data, err := os.ReadFile(f.SourcePath); err == nil {
				add("resolved_auth_file", f.ContainerPath, string(data))
			}
		}
	}

	for _, s := range cfg.ResolvedSecrets {
		key := s.Name
		if key == "" {
			key = s.Target
		}
		switch s.Type {
		case "environment", "":
			add("secret_env", key, s.Value)
		case "file":
			add("secret_file", key, s.Value)
		}
	}

	return secrets
}

// buildBootstrapFiles assembles the "files" array: ResolvedAuth.Files (read
// from SourcePath) plus file-type ResolvedSecrets (content already resolved
// in Value — no disk read needed).
func buildBootstrapFiles(cfg RunConfig) ([]bootstrapFile, error) {
	containerHome := util.GetHomeDir(cfg.UnixUsername)
	var files []bootstrapFile

	if cfg.ResolvedAuth != nil {
		for _, f := range cfg.ResolvedAuth.Files {
			if f.SourcePath == "" {
				continue
			}
			data, err := os.ReadFile(f.SourcePath)
			if err != nil {
				return nil, fmt.Errorf("substrate: read auth file %s: %w", f.SourcePath, err)
			}
			files = append(files, bootstrapFile{
				Path:       expandTildeTarget(f.ContainerPath, containerHome),
				Mode:       defaultFileMode,
				ContentB64: base64.StdEncoding.EncodeToString(data),
			})
		}
	}

	for _, s := range cfg.ResolvedSecrets {
		if s.Type != "file" {
			continue
		}
		files = append(files, bootstrapFile{
			Path:       expandTildeTarget(s.Target, containerHome),
			Mode:       defaultFileMode,
			ContentB64: base64.StdEncoding.EncodeToString([]byte(s.Value)),
		})
	}

	return files, nil
}

// generateControlToken returns a random 32-byte hex string, used both as
// the bearer for POST /scion/v1/exec (phase1-spec.md §2.1) and, in the
// Phase 1 fallback nonce (see substrateBootstrapNonce), as the bootstrap
// bearer itself.
func generateControlToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("substrate: generate control token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// waitForHealthz polls GET /scion/v1/healthz through the router until it
// reports wantState, or timeout elapses. clock is time.Sleep in
// production, replaced in tests.
func waitForHealthz(ctx context.Context, router *substrate.RouterClient, atespace, actorName, wantState string, timeout time.Duration, sleep func(time.Duration)) error {
	deadline := time.Now().Add(timeout)
	backoff := 500 * time.Millisecond
	const maxBackoff = 5 * time.Second

	for {
		state, err := getHealthz(ctx, router, atespace, actorName)
		if err == nil && state == wantState {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("substrate: %s/%s did not reach healthz state %q within %s: %w", atespace, actorName, wantState, timeout, err)
			}
			return fmt.Errorf("substrate: %s/%s did not reach healthz state %q within %s (last state: %q)", atespace, actorName, wantState, timeout, state)
		}
		sleep(backoff)
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func getHealthz(ctx context.Context, router *substrate.RouterClient, atespace, actorName string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, healthzRequestTimeout)
	defer cancel()

	resp, err := router.Do(ctx, atespace, actorName, http.MethodGet, substrateHealthzPath, nil, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("healthz returned status %d", resp.StatusCode)
	}
	var hz healthzResponse
	if err := json.NewDecoder(resp.Body).Decode(&hz); err != nil {
		return "", fmt.Errorf("decode healthz response: %w", err)
	}
	return hz.State, nil
}

// errBootstrapHijacked is returned by postBootstrap when the control server
// answers 409: someone bootstrapped this actor before the broker's own
// request landed.
//
// Under the Phase 1 fallback nonce (phase1-spec.md §5: any bearer accepted,
// correctness resting on "first bootstrap wins" plus a NetworkPolicy
// restricting router ingress to the broker namespace), the broker is
// supposed to be the only caller that can ever reach this endpoint before
// it is claimed — it sends its POST immediately after the actor reaches
// RUNNING. A 409 on the broker's own first attempt therefore means another
// caller reached the control server first: either the NetworkPolicy has a
// gap, or something else in the broker's namespace raced it. That is not a
// benign retry to swallow; it is the only signal Phase 1 has that the
// fallback's trust assumption was violated, so Run treats it as a
// compromise indicator (see Run's handling of this sentinel).
var errBootstrapHijacked = errors.New("bootstrap rejected: actor was already bootstrapped by another caller")

// postBootstrap sends the bootstrap payload through the router, authorized
// with nonce (phase1-spec.md §2.1). Any non-2xx status is an error; 409
// specifically becomes errBootstrapHijacked (see its doc comment) rather
// than being treated as an idempotent no-op.
func postBootstrap(ctx context.Context, router *substrate.RouterClient, atespace, actorName, nonce string, req bootstrapRequest) error {
	ctx, cancel := context.WithTimeout(ctx, bootstrapRequestTimeout)
	defer cancel()

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("substrate: marshal bootstrap request: %w", err)
	}

	resp, err := router.Do(ctx, atespace, actorName, http.MethodPost, substrateBootstrapPath, bytes.NewReader(body), map[string]string{
		"Authorization": "Bearer " + nonce,
		"Content-Type":  "application/json",
	})
	if err != nil {
		return fmt.Errorf("substrate: POST bootstrap to %s/%s: %w", atespace, actorName, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusConflict {
		return errBootstrapHijacked
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("substrate: bootstrap %s/%s failed: status %d: %s", atespace, actorName, resp.StatusCode, string(msg))
}

// doExec sends argv through the router to the actor's control server,
// authorized with the actor's control_token (phase1-spec.md §2.2 Exec row).
func doExec(ctx context.Context, router *substrate.RouterClient, atespace, actorName, controlToken string, argv []string, user string, timeout time.Duration) (execResponse, error) {
	var out execResponse

	// The HTTP round trip needs longer than timeout_s itself: the control
	// server enforces timeout_s server-side and then still has to write the
	// response, and the request has to reach it and come back. Racing the
	// client's deadline against the server's own enforcement is exactly
	// what cut exec calls short before (review round 1, Consider #8).
	ctx, cancel := context.WithTimeout(ctx, timeout+execTimeoutSlack)
	defer cancel()

	body, err := json.Marshal(execRequest{
		Argv:     argv,
		User:     user,
		TimeoutS: int(timeout.Seconds()),
	})
	if err != nil {
		return out, fmt.Errorf("substrate: marshal exec request: %w", err)
	}

	resp, err := router.Do(ctx, atespace, actorName, http.MethodPost, substrateExecPath, bytes.NewReader(body), map[string]string{
		"Authorization": "Bearer " + controlToken,
		"Content-Type":  "application/json",
	})
	if err != nil {
		return out, fmt.Errorf("substrate: POST exec to %s/%s: %w", atespace, actorName, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, fmt.Errorf("substrate: exec on %s/%s failed: status %d: %s", atespace, actorName, resp.StatusCode, string(msg))
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("substrate: decode exec response from %s/%s: %w", atespace, actorName, err)
	}
	if out.ExitCode != 0 {
		return out, fmt.Errorf("substrate: exec on %s/%s exited %d: %s", atespace, actorName, out.ExitCode, out.Stderr)
	}
	return out, nil
}
