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

// postBootstrap sends the bootstrap payload through the router, authorized
// with nonce (phase1-spec.md §2.1). A 409 means the actor was already
// bootstrapped, which Run treats as success (idempotent retry after a
// broker crash between bootstrap and returning); any other non-2xx status
// is an error.
func postBootstrap(ctx context.Context, router *substrate.RouterClient, atespace, actorName, nonce string, req bootstrapRequest) error {
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

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusConflict {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("substrate: bootstrap %s/%s failed: status %d: %s", atespace, actorName, resp.StatusCode, string(msg))
}

// doExec sends argv through the router to the actor's control server,
// authorized with the actor's control_token (phase1-spec.md §2.2 Exec row).
func doExec(ctx context.Context, router *substrate.RouterClient, atespace, actorName, controlToken string, argv []string, user string, timeout time.Duration) (execResponse, error) {
	var out execResponse

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
