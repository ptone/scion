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

package substrate

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

const (
	// maxBootstrapBodyBytes bounds the bootstrap request body (env, files,
	// start_cmd, control_token). It is generous because files can carry
	// harness credential bundles, but still bounded so a malformed or
	// hostile caller can't exhaust memory.
	maxBootstrapBodyBytes = 64 * 1024 * 1024

	// maxExecBodyBytes bounds the exec request body (argv + metadata; no
	// large payloads are expected here).
	maxExecBodyBytes = 1 * 1024 * 1024

	// defaultFileMode is used when a bootstrap file entry omits mode (0).
	defaultFileMode = 0o644
)

// InitRunner runs the equivalent of `sciontool init -- <argv...>` in
// process. forwardTermSignal must be false when called from Bootstrap: see
// commands.InitRunOptions.ForwardTermSignal for why substrate-serve must own
// SIGTERM handling itself in Phase 1.
//
// The concrete implementation (cmd/sciontool/commands.RunInit) lives in the
// cmd layer; Server takes it as a function value so this package never
// imports cmd/sciontool/commands. This is the "reuse, don't fork" seam for
// the init logic required by phase1-spec.md §2.1.
type InitRunner func(argv []string, forwardTermSignal bool) int

// Server implements the `sciontool substrate-serve` control server
// (phase1-spec.md §2.1): healthz, one-shot bootstrap, and authenticated
// exec. /pty, /rehydrate and /tunnel/open are out of scope for Phase 1.
type Server struct {
	nonceVerifier NonceVerifier
	runInit       InitRunner

	// chownUID/chownGID own bootstrap-written files and directories,
	// matching the "scion" user's ownership. -1 means "don't chown"
	// (e.g. no scion user found, or running as a non-root test).
	chownUID int
	chownGID int

	mu           sync.Mutex
	bootstrapped bool
	controlToken string
}

// Option configures a Server at construction time.
type Option func(*Server)

// WithNonceVerifier overrides the bootstrap nonce verifier. Default is
// FirstBootstrapWinsVerifier (phase1-spec.md §5 fallback).
func WithNonceVerifier(v NonceVerifier) Option {
	return func(s *Server) { s.nonceVerifier = v }
}

// WithInitRunner sets the function used to run the init path after a
// successful bootstrap. Required for a Server that will actually serve
// bootstrap requests; tests that only exercise auth/state may omit it.
func WithInitRunner(r InitRunner) Option {
	return func(s *Server) { s.runInit = r }
}

// WithChownOwner overrides the uid/gid used to chown bootstrap-written
// files. Defaults to the "scion" system user, when present.
func WithChownOwner(uid, gid int) Option {
	return func(s *Server) { s.chownUID, s.chownGID = uid, gid }
}

// NewServer creates a Server ready to be mounted with Handler().
func NewServer(opts ...Option) *Server {
	s := &Server{
		nonceVerifier: FirstBootstrapWinsVerifier{},
	}
	s.chownUID, s.chownGID = lookupScionOwner()
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// lookupScionOwner resolves the "scion" system user's uid/gid. It returns
// (-1, -1) if the user can't be found (e.g. in a unit test environment),
// which callers treat as "skip chown".
func lookupScionOwner() (int, int) {
	u, err := user.Lookup("scion")
	if err != nil {
		return -1, -1
	}
	uid, errU := strconv.Atoi(u.Uid)
	gid, errG := strconv.Atoi(u.Gid)
	if errU != nil || errG != nil {
		return -1, -1
	}
	return uid, gid
}

// Handler returns the http.Handler for the control server's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/scion/v1/healthz", s.handleHealthz)
	mux.HandleFunc("/scion/v1/bootstrap", s.handleBootstrap)
	mux.HandleFunc("/scion/v1/exec", s.handleExec)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state := StateAwaitingBootstrap
	if s.isBootstrapped() {
		state = StateRunning
	}
	writeJSON(w, http.StatusOK, HealthzResponse{State: state})
}

func (s *Server) isBootstrapped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bootstrapped
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token, ok := bearerToken(r)
	if !ok || !s.nonceVerifier.VerifyNonce(token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if s.isBootstrapped() {
		http.Error(w, "already bootstrapped", http.StatusConflict)
		return
	}

	var req BootstrapRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBootstrapBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Claim the single bootstrap slot now, before performing any side
	// effects. A second request that arrives concurrently (or after a
	// failure below) gets 409, matching "accepted once per process
	// lifetime" literally — Phase 1 does not support retrying a bootstrap
	// that failed partway through.
	s.mu.Lock()
	if s.bootstrapped {
		s.mu.Unlock()
		http.Error(w, "already bootstrapped", http.StatusConflict)
		return
	}
	s.bootstrapped = true
	s.controlToken = req.ControlToken
	s.mu.Unlock()

	for _, f := range req.Files {
		if err := s.writeBootstrapFile(f); err != nil {
			// Deliberately do not include the file's content or the
			// underlying error's arguments in the response/log beyond the
			// path — content_b64 may carry secrets.
			log.Error("bootstrap: failed to write file %s: %v", f.Path, redactErr(err))
			http.Error(w, "failed to write bootstrap files", http.StatusInternalServerError)
			return
		}
	}

	for k, v := range req.Env {
		if err := os.Setenv(k, v); err != nil {
			log.Error("bootstrap: failed to set env var %s: %v", k, err)
		}
	}

	if s.runInit != nil {
		childArgs := []string{"sh", "-c", req.StartCmd}
		go func() {
			// forwardTermSignal=false: substrate-serve is PID 1 and owns
			// SIGTERM itself (Phase 1: log, don't forward — see
			// cmd/substrate_serve.go and InitRunOptions.ForwardTermSignal).
			exitCode := s.runInit(childArgs, false)
			log.Info("substrate-serve: in-process init exited with code %d", exitCode)
		}()
	} else {
		log.Error("bootstrap: no InitRunner configured; child process was not started")
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeBootstrapFile decodes and writes one bootstrap file, creating any
// missing parent directories and chowning both the file and any directories
// this call created to the scion user.
func (s *Server) writeBootstrapFile(f BootstrapFile) error {
	if f.Path == "" || !filepath.IsAbs(f.Path) {
		return errInvalidBootstrapPath
	}
	content, err := base64.StdEncoding.DecodeString(f.ContentB64)
	if err != nil {
		return errInvalidBootstrapContent
	}

	dir := filepath.Dir(f.Path)
	created, err := mkdirAllTracked(dir, 0o755)
	if err != nil {
		return err
	}

	mode := os.FileMode(f.Mode)
	if mode == 0 {
		mode = defaultFileMode
	}
	if err := os.WriteFile(f.Path, content, mode); err != nil {
		return err
	}

	if s.chownUID >= 0 && s.chownGID >= 0 {
		for _, d := range created {
			_ = os.Chown(d, s.chownUID, s.chownGID)
		}
		_ = os.Chown(f.Path, s.chownUID, s.chownGID)
	}
	return nil
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token, ok := bearerToken(r)
	s.mu.Lock()
	expected := s.controlToken
	bootstrapped := s.bootstrapped
	s.mu.Unlock()

	if !bootstrapped || expected == "" || !ok ||
		subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req ExecRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxExecBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Argv) == 0 {
		http.Error(w, "argv must not be empty", http.StatusBadRequest)
		return
	}

	execUser := req.User
	if execUser == "" {
		execUser = "scion"
	}
	if execUser != "scion" && execUser != "root" {
		http.Error(w, `user must be "scion" or "root"`, http.StatusBadRequest)
		return
	}

	timeout := defaultExecTimeout
	if req.TimeoutS > 0 {
		if d := time.Duration(req.TimeoutS) * time.Second; d < maxExecTimeout {
			timeout = d
		} else {
			timeout = maxExecTimeout
		}
	}

	resp := runExec(r.Context(), execUser, req.Argv, timeout)
	writeJSON(w, http.StatusOK, resp)
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header. ok is false if the header is missing or malformed.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(h, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
