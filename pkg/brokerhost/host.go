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

// Package brokerhost hosts a collection of explicitly configured flat
// Runtime Broker instances in one process (ptone/scion#3272): each instance
// has its own immutable key, persisted identity, runtime, manager,
// credentials and logger, and is activated or refused independently.
//
// Activation is two-pass. Pass 1 builds every configured instance's runtime,
// probes its execution scope and loads or creates its identity, with no Hub
// registration, heartbeat or control-channel side effect. It then refuses
// every instance whose execution scope conflicts with another's (until
// per-instance ownership of runtime inventory and the shared host resources
// exists, two instances on one scope must not both serve). Pass 2 activates
// the eligible instances: the Activator validates the Hub binding (the
// embedded registration when the Hub runs in the process, otherwise the
// remote activation validation), and only a bound result builds and starts
// the instance's Runtime Broker server.
//
// The host owns the process-wide HTTP listener. Its routing follows the
// CONFIGURED cardinality: with exactly one instance configured, the listener
// serves that instance (when active); with more than one configured, it
// serves host health only and never an instance route, whatever activates.
package brokerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/brokercredentials"
	"github.com/GoogleCloudPlatform/scion/pkg/brokeridentity"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtimebroker"
	"github.com/GoogleCloudPlatform/scion/pkg/util/logging"
)

// Mode is how instances are activated, fixed for the process.
type Mode string

const (
	// ModeColocated: the Hub runs in this process; instances activate
	// through the embedded registration.
	ModeColocated Mode = "colocated"
	// ModeRemote: the Hub is elsewhere (or --simulate-remote-broker);
	// instances activate with their instance-scoped credentials after the
	// remote activation validation.
	ModeRemote Mode = "remote"
)

// State is an instance's lifecycle state.
type State string

const (
	StatePending State = "pending"
	StateActive  State = "active"
	StateRefused State = "refused"
	StateStopped State = "stopped"
)

// RuntimeFactory builds an instance's runtime from its own explicit
// configuration, never from Runtime Broker Profile resolution or process-wide
// environment switches.
type RuntimeFactory func(ctx context.Context, inst config.V1RuntimeBrokerInstanceConfig) (runtime.Runtime, error)

// ScopeProber probes the execution scope of an instance's runtime (Docker:
// the daemon ID through the runtime's own CLI).
type ScopeProber func(ctx context.Context, inst config.V1RuntimeBrokerInstanceConfig, rt runtime.Runtime) (brokeridentity.ExecutionScope, error)

// Activation is a bound activation result: the credentials the instance's
// server connects with.
type Activation struct {
	// InMemoryCredentials are the embedded registration's credentials
	// (ModeColocated).
	InMemoryCredentials *brokercredentials.BrokerCredentials
	// RemoteCredentials are the instance-scoped credentials that passed the
	// remote activation validation (ModeRemote).
	RemoteCredentials []brokercredentials.BrokerCredentials
}

// Candidate is an instance that passed pass 1 (runtime built, scope probed,
// identity loaded, no scope conflict).
type Candidate struct {
	Instance config.V1RuntimeBrokerInstanceConfig
	Identity *brokeridentity.Identity
	Runtime  runtime.Runtime
}

// Activator validates an instance's Hub binding. Activate returns a non-nil
// Activation only for a bound result; any error leaves the instance
// unactivated. Refused records a refusal (from any step) for reporting.
type Activator interface {
	Activate(ctx context.Context, c Candidate) (*Activation, error)
	Refused(inst config.V1RuntimeBrokerInstanceConfig, err error)
}

// InstanceContext is the immutable context of one activated instance,
// passed explicitly to everything it runs.
type InstanceContext struct {
	Instance   config.V1RuntimeBrokerInstanceConfig
	Identity   *brokeridentity.Identity
	Runtime    runtime.Runtime
	Manager    agent.Manager
	Activation *Activation
	// MultiInstance is true when more than one instance is configured.
	MultiInstance bool
	// Logger carries the instance's broker_id and key.
	Logger *slog.Logger
}

// ServerBuilder builds an activated instance's Runtime Broker server; the
// caller supplies the process-wide parts of its configuration.
type ServerBuilder func(ic InstanceContext) (*runtimebroker.Server, error)

// ListenerConfig is the process-wide listener (server.broker.host/port).
type ListenerConfig struct {
	Host         string
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// Config configures a Host.
type Config struct {
	GlobalDir string
	// Instances are the strictly loaded and validated
	// server.broker.instances, in configuration order.
	Instances   []config.V1RuntimeBrokerInstanceConfig
	Mode        Mode
	LegacyIDs   []string
	NewRuntime  RuntimeFactory
	ProbeScope  ScopeProber
	Activator   Activator
	BuildServer ServerBuilder
	Listener    ListenerConfig
	Logger      *slog.Logger
}

// InstanceStatus is the reported state of one configured instance.
type InstanceStatus struct {
	Key             string `json:"key"`
	Name            string `json:"name"`
	RuntimeBrokerID string `json:"runtimeBrokerId,omitempty"`
	RuntimeTargetID string `json:"runtimeTargetId,omitempty"`
	State           State  `json:"state"`
	Error           string `json:"error,omitempty"`
}

type instance struct {
	cfg      config.V1RuntimeBrokerInstanceConfig
	rt       runtime.Runtime
	identity *brokeridentity.Identity
	scope    brokeridentity.ExecutionScope
	server   *runtimebroker.Server
	ctx      InstanceContext
	state    State
	err      error
}

// Host hosts the configured instances.
type Host struct {
	cfg       Config
	log       *slog.Logger
	mu        sync.RWMutex
	instances []*instance
	prepared  bool
	httpSrv   *http.Server
}

// New validates cfg and returns a Host. Invalid instance configuration
// (invalid or duplicate keys, unsupported targets) refuses the whole process:
// no instance starts.
func New(cfg Config) (*Host, error) {
	if len(cfg.Instances) == 0 {
		return nil, errors.New("brokerhost: no Runtime Broker instances configured")
	}
	if errs := config.ValidateRuntimeBrokerInstances(cfg.Instances); len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Path+": "+e.Message)
		}
		return nil, fmt.Errorf("server.broker.instances: %s", strings.Join(msgs, "; "))
	}
	if cfg.Mode != ModeColocated && cfg.Mode != ModeRemote {
		return nil, fmt.Errorf("brokerhost: unknown mode %q", cfg.Mode)
	}
	if cfg.NewRuntime == nil || cfg.ProbeScope == nil || cfg.Activator == nil || cfg.BuildServer == nil {
		return nil, errors.New("brokerhost: NewRuntime, ProbeScope, Activator and BuildServer are required")
	}
	if cfg.GlobalDir == "" {
		return nil, errors.New("brokerhost: GlobalDir is required")
	}
	log := cfg.Logger
	if log == nil {
		log = logging.Subsystem("broker.host")
	}
	h := &Host{cfg: cfg, log: log}
	for _, inst := range cfg.Instances {
		h.instances = append(h.instances, &instance{cfg: inst, state: StatePending})
	}
	return h, nil
}

// MultiInstance reports whether more than one instance is configured. It is
// the configured cardinality, never the active count.
func (h *Host) MultiInstance() bool { return len(h.cfg.Instances) > 1 }

func (h *Host) refuse(in *instance, err error) {
	h.mu.Lock()
	in.state = StateRefused
	in.err = err
	h.mu.Unlock()
	h.log.Error("Runtime Broker instance not activated", "instance", in.cfg.Key, "error", err)
	h.cfg.Activator.Refused(in.cfg, err)
}

// Prepare runs both activation passes. It returns an error only when it was
// already run; per-instance refusals are reported through Status and the
// Activator, and never stop sibling instances.
func (h *Host) Prepare(ctx context.Context) error {
	h.mu.Lock()
	if h.prepared {
		h.mu.Unlock()
		return errors.New("brokerhost: already prepared")
	}
	h.prepared = true
	h.mu.Unlock()

	// Pass 1: runtime, scope and identity for every candidate. No Hub
	// registration, heartbeat or control channel.
	var candidates []*instance
	for _, in := range h.instances {
		rt, err := h.cfg.NewRuntime(ctx, in.cfg)
		if err != nil {
			h.refuse(in, fmt.Errorf("flat Runtime Broker instance %q: building its runtime: %w", in.cfg.Key, err))
			continue
		}
		scope, err := h.cfg.ProbeScope(ctx, in.cfg, rt)
		if err != nil {
			h.refuse(in, fmt.Errorf("flat Runtime Broker instance %q: %w", in.cfg.Key, err))
			continue
		}
		id, err := brokeridentity.LoadOrCreate(brokeridentity.InstanceDir(h.cfg.GlobalDir, in.cfg.Key), in.cfg.Key, in.cfg.RuntimeTarget.Type, scope, h.cfg.LegacyIDs)
		if err != nil {
			h.refuse(in, fmt.Errorf("flat Runtime Broker instance %q: %w", in.cfg.Key, err))
			continue
		}
		in.rt, in.scope, in.identity = rt, scope, id
		candidates = append(candidates, in)
	}

	// Conflict groups: every member of a group sharing one execution scope
	// is refused; no order-based winner.
	groups := map[string][]*instance{}
	var order []string
	for _, in := range candidates {
		k := ScopeKey(in.scope)
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], in)
	}
	var eligible []*instance
	for _, k := range order {
		members := groups[k]
		if len(members) == 1 {
			eligible = append(eligible, members[0])
			continue
		}
		keys := make([]string, 0, len(members))
		for _, m := range members {
			keys = append(keys, m.cfg.Key)
		}
		sort.Strings(keys)
		for _, m := range members {
			h.refuse(m, &ScopeConflictError{InstanceKey: m.cfg.Key, Scope: k, Instances: keys})
		}
	}

	// Pass 2: activate each eligible instance independently.
	for _, in := range eligible {
		act, err := h.cfg.Activator.Activate(ctx, Candidate{Instance: in.cfg, Identity: in.identity, Runtime: in.rt})
		if err != nil {
			h.refuse(in, err)
			continue
		}
		if act == nil {
			h.refuse(in, fmt.Errorf("flat Runtime Broker instance %q: activation returned no credentials", in.cfg.Key))
			continue
		}
		ic := InstanceContext{
			Instance:      in.cfg,
			Identity:      in.identity,
			Runtime:       in.rt,
			Manager:       agent.NewManager(in.rt),
			Activation:    act,
			MultiInstance: h.MultiInstance(),
			Logger: h.log.With(slog.String(logging.AttrBrokerID, in.identity.RuntimeBrokerID),
				slog.String("instance", in.cfg.Key)),
		}
		srv, err := h.cfg.BuildServer(ic)
		if err != nil {
			h.refuse(in, fmt.Errorf("flat Runtime Broker instance %q: %w", in.cfg.Key, err))
			continue
		}
		h.mu.Lock()
		in.server, in.ctx, in.state = srv, ic, StateActive
		h.mu.Unlock()
		ic.Logger.Info("Runtime Broker instance activated", "runtimeTarget", in.identity.RuntimeTarget.ID)
	}
	return nil
}

// ScopeConflictError refuses an instance whose execution scope is shared by
// another configured instance. It is temporary: it is lifted once runtime
// inventory and the shared host resources are partitioned by instance.
type ScopeConflictError struct {
	InstanceKey string
	Scope       string
	Instances   []string
}

func (e *ScopeConflictError) Error() string {
	return fmt.Sprintf("flat Runtime Broker instance %q not activated: its execution scope %s is shared by instances %s; "+
		"instances on one execution scope cannot be hosted together in this release. Configure each instance on its own scope",
		e.InstanceKey, e.Scope, strings.Join(e.Instances, ", "))
}

// ScopeKey is the identity part of an execution scope (contract section 5):
// the Docker daemon ID, or the Kubernetes cluster UID plus namespace.
func ScopeKey(s brokeridentity.ExecutionScope) string {
	switch {
	case s.Docker != nil:
		return "docker:" + s.Docker.DaemonID
	case s.Kubernetes != nil:
		return "kubernetes:" + s.Kubernetes.ClusterUID + "/" + s.Kubernetes.Namespace
	default:
		return s.Type + ":"
	}
}

// Status returns every configured instance's state, in configuration order.
func (h *Host) Status() []InstanceStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]InstanceStatus, 0, len(h.instances))
	for _, in := range h.instances {
		st := InstanceStatus{Key: in.cfg.Key, Name: in.cfg.Name, State: in.state}
		if in.identity != nil {
			st.RuntimeBrokerID = in.identity.RuntimeBrokerID
			st.RuntimeTargetID = in.identity.RuntimeTarget.ID
		}
		if in.err != nil {
			st.Error = in.err.Error()
		}
		out = append(out, st)
	}
	return out
}

// Ready reports whether every configured instance is active.
func (h *Host) Ready() bool {
	for _, st := range h.Status() {
		if st.State != StateActive {
			return false
		}
	}
	return true
}

// Active returns the activated instances' contexts and servers, in
// configuration order.
func (h *Host) Active() []ActiveInstance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []ActiveInstance
	for _, in := range h.instances {
		if in.state == StateActive {
			out = append(out, ActiveInstance{Context: in.ctx, Server: in.server})
		}
	}
	return out
}

// ActiveInstance is one activated instance.
type ActiveInstance struct {
	Context InstanceContext
	Server  *runtimebroker.Server
}

// Instance returns the activated instance with exactly this Runtime Broker
// ID. There is no fallback to any other instance.
func (h *Host) Instance(runtimeBrokerID string) (*runtimebroker.Server, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, in := range h.instances {
		if in.state == StateActive && in.identity != nil && in.identity.RuntimeBrokerID == runtimeBrokerID {
			return in.server, true
		}
	}
	return nil, false
}

// Handler is the process-wide listener's handler. With exactly one instance
// configured and active it is that instance's handler (the P1 route). In
// every other case (one configured but inactive, or more than one
// configured, whatever activated) it serves host health only and answers
// 404 for everything else, never routing to a surviving instance.
func (h *Host) Handler() http.Handler {
	if !h.MultiInstance() {
		if active := h.Active(); len(active) == 1 {
			return active[0].Server.Handler()
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handleHealthz)
	mux.HandleFunc("/readyz", h.handleReadyz)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	return mux
}

type healthResponse struct {
	Status    string           `json:"status"`
	Instances []InstanceStatus `json:"instances"`
}

// handleHealthz is liveness: the host is serving. It reports each
// instance's state; it never implies the instances are ready.
func (h *Host) handleHealthz(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	if !h.Ready() {
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: status, Instances: h.Status()})
}

// handleReadyz is readiness: 200 only when every configured instance is
// active; 503 while any is refused or pending.
func (h *Host) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if h.Ready() {
		writeJSON(w, http.StatusOK, healthResponse{Status: "ready", Instances: h.Status()})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not ready", Instances: h.Status()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Run starts every active instance's services and the process-wide
// listener, and blocks until ctx is done (then shuts everything down) or the
// listener fails. Prepare must have run.
func (h *Host) Run(ctx context.Context) error {
	h.mu.RLock()
	prepared := h.prepared
	h.mu.RUnlock()
	if !prepared {
		return errors.New("brokerhost: Run before Prepare")
	}
	for _, a := range h.Active() {
		if err := a.Server.StartServices(ctx); err != nil {
			h.markStopped(a.Context.Identity.RuntimeBrokerID, err)
			a.Context.Logger.Error("Runtime Broker instance services failed to start", "error", err)
		}
	}
	h.mu.Lock()
	h.httpSrv = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", h.cfg.Listener.Host, h.cfg.Listener.Port),
		Handler:      h.Handler(),
		ReadTimeout:  h.cfg.Listener.ReadTimeout,
		WriteTimeout: h.cfg.Listener.WriteTimeout,
	}
	srv := h.httpSrv
	h.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case err := <-errCh:
		h.shutdownInstances(context.Background())
		return err
	case <-ctx.Done():
		return h.Shutdown(context.Background())
	}
}

func (h *Host) markStopped(runtimeBrokerID string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, in := range h.instances {
		if in.identity != nil && in.identity.RuntimeBrokerID == runtimeBrokerID {
			in.state, in.err = StateStopped, err
		}
	}
}

// Shutdown stops every active instance (each drains its own starts in
// flight), then the listener.
func (h *Host) Shutdown(ctx context.Context) error {
	h.shutdownInstances(ctx)
	h.mu.RLock()
	srv := h.httpSrv
	h.mu.RUnlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (h *Host) shutdownInstances(ctx context.Context) {
	var wg sync.WaitGroup
	for _, a := range h.Active() {
		wg.Add(1)
		go func(a ActiveInstance) {
			defer wg.Done()
			if err := a.Server.Shutdown(ctx); err != nil {
				a.Context.Logger.Warn("Runtime Broker instance shutdown", "error", err)
			}
		}(a)
	}
	wg.Wait()
	h.mu.Lock()
	for _, in := range h.instances {
		if in.state == StateActive {
			in.state = StateStopped
		}
	}
	h.mu.Unlock()
}
