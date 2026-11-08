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

// SubstrateRuntime implements runtime.Runtime for Agent Substrate
// (github.com/agent-substrate/substrate), a Kubernetes-hosted actor runtime.
// See deploy/substrate/README.md for the design this implements.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/k8s"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	ateapi "github.com/GoogleCloudPlatform/scion/third_party/substrate/ateapi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Bounds not exposed as settings (only TemplateReadyTimeout is
// configurable — substrate-runtime.md §2).
const (
	defaultActorRunningTimeout = 5 * time.Minute
	actorRunningPollInterval   = 2 * time.Second
	defaultHealthzTimeout      = 5 * time.Minute
	defaultExecTimeout         = 60 * time.Second
)

// substrateAgentRecord holds the fields List needs that ListActors cannot
// return (Substrate actors carry no labels — substrate-runtime.md §4), synthesised
// from the broker's own record of what it passed to Run. A broker restart
// loses this for actors it did not create in this process lifetime
// (substrate-runtime.md §4); a ConfigMap-backed store is future work.
type substrateAgentRecord struct {
	Labels        map[string]string
	Template      string
	HarnessConfig string
	Project       string
	ProjectID     string
	ProjectPath   string
	Image         string
}

// SubstrateRuntime implements runtime.Runtime for Agent Substrate.
type SubstrateRuntime struct {
	cfg    config.V1SubstrateConfig
	client ateapi.ControlClient
	conn   *grpc.ClientConn // non-nil only when this runtime opened it (nil for injected test clients)
	router *substrate.RouterClient
	// k8sClient is not read by any runtime method — the dialer receives its
	// own clientset directly as a Dial parameter (below) and never reads
	// this field back. Retained anyway so tests can inject a fake clientset
	// and assert, with a real client-go fake's Actions(), that GetLogs makes
	// no Kubernetes call — a meaningful regression guard only as long as
	// this field stays unread by anything else.
	k8sClient kubernetes.Interface

	now   func() time.Time
	sleep func(time.Duration)

	// sleepCtx is the cancellable wait waitForHealthz uses between polls, so a
	// cancelled Run context returns promptly instead of blocking out a full
	// backoff. Separate from sleep (which the uncancellable template/poll
	// waits still use) so this change stays scoped to the healthz loop.
	sleepCtx func(context.Context, time.Duration) error

	// healthzTimeout bounds how long Run waits for the control server to
	// report awaiting-bootstrap. A struct field (rather than always using
	// defaultHealthzTimeout directly) so tests can shrink it and exercise
	// the timeout path in well under defaultHealthzTimeout's 5 minutes.
	healthzTimeout time.Duration

	// projectSet is the source of an unfiltered List in operator mode
	// (substrate v1 design §3.3): the projects whose atespaces List reads.
	// The runtime broker installs it (InstallProjectSet); nil means an unfiltered
	// List fails as cold rather than returning an empty result.
	projectSetMu sync.RWMutex
	projectSet   ProjectSet

	// lastSkipKey is the set of atespaces the previous unfiltered List
	// skipped, so the skip note is logged at Warn only when it changes.
	skipLogMu   sync.Mutex
	lastSkipKey string
}

var _ ProjectSetReceiver = (*SubstrateRuntime)(nil)

// ErrSubstrateRuntimeShared is returned by InstallProjectSet when the
// runtime already serves another broker instance's ProjectSet.
// NewSubstrateRuntime memoizes runtimes per config process-wide, so two
// broker instances in one process that resolve the same substrate config
// share one runtime; operator mode refuses that rather than let one
// instance list with the other's projects.
var ErrSubstrateRuntimeShared = errors.New("substrate runtime is shared by another broker instance in this process; operator mode supports one broker instance per process")

// InstallProjectSet implements ProjectSetReceiver. It installs ps when the
// runtime has none, or when ps is the set already installed (the same
// broker instance installing again). A runtime that carries a different
// set is refused with ErrSubstrateRuntimeShared and keeps its set.
func (r *SubstrateRuntime) InstallProjectSet(ps ProjectSet) error {
	r.projectSetMu.Lock()
	defer r.projectSetMu.Unlock()
	if r.projectSet != nil && !sameProjectSet(r.projectSet, ps) {
		return ErrSubstrateRuntimeShared
	}
	r.projectSet = ps
	return nil
}

// ReleaseProjectSet implements ProjectSetReceiver: it clears the installed
// set only when it is ps (compare-and-clear), so a stopping broker instance
// never clears another instance's set.
func (r *SubstrateRuntime) ReleaseProjectSet(ps ProjectSet) {
	r.projectSetMu.Lock()
	defer r.projectSetMu.Unlock()
	if r.projectSet != nil && sameProjectSet(r.projectSet, ps) {
		r.projectSet = nil
	}
}

// sameProjectSet reports whether a and b are the same ProjectSet value.
// Two sets count as the same only when they have the same dynamic type, both
// values are comparable at run time, and they are equal; anything else counts
// as different and the install is refused, so the comparison never panics.
func sameProjectSet(a, b ProjectSet) bool {
	ta := reflect.TypeOf(a)
	if ta == nil || ta != reflect.TypeOf(b) {
		return false
	}
	if !reflect.ValueOf(a).Comparable() || !reflect.ValueOf(b).Comparable() {
		return false
	}
	return a == b
}

// SetProjectSetForTest replaces the runtime's ProjectSet unconditionally,
// for tests that drive a runtime without a broker.
func (r *SubstrateRuntime) SetProjectSetForTest(ps ProjectSet) {
	r.projectSetMu.Lock()
	defer r.projectSetMu.Unlock()
	r.projectSet = ps
}

func (r *SubstrateRuntime) currentProjectSet() ProjectSet {
	r.projectSetMu.RLock()
	defer r.projectSetMu.RUnlock()
	return r.projectSet
}

// ErrSubstrateAtespaceNotProvisioned matches (errors.Is) every
// *SubstrateAtespaceProvisioningError.
var ErrSubstrateAtespaceNotProvisioned = errors.New("substrate: atespace not provisioned for the broker")

// SubstrateAtespaceProvisioningError reports that a project's atespace does
// not exist or the broker principal cannot read it (GetAtespace or
// ListActors returned NotFound or PermissionDenied). In operator mode the
// broker never creates atespaces (substrate v1 design §3.7): an operator
// provisions each one and grants the broker editor on it.
type SubstrateAtespaceProvisioningError struct {
	Atespace string
	Code     codes.Code
}

func (e *SubstrateAtespaceProvisioningError) Error() string {
	return fmt.Sprintf("substrate: atespace %q is not provisioned or the broker cannot read it (%s): ask your operator to provision atespace %q and grant the broker editor on it",
		e.Atespace, e.Code, e.Atespace)
}

// Is reports a match for ErrSubstrateAtespaceNotProvisioned.
func (e *SubstrateAtespaceProvisioningError) Is(target error) bool {
	return target == ErrSubstrateAtespaceNotProvisioned
}

// Unwrap returns a gRPC status carrying only the code: the server's message
// is not kept, so neither the error text nor status.FromError on the
// returned error can expose it.
func (e *SubstrateAtespaceProvisioningError) Unwrap() error { return status.Error(e.Code, "") }

// atespaceProvisioningError returns the typed provisioning error for a
// NotFound or PermissionDenied err on atespace, else nil.
func atespaceProvisioningError(atespace string, err error) error {
	switch c := status.Code(err); c {
	case codes.NotFound, codes.PermissionDenied:
		return &SubstrateAtespaceProvisioningError{Atespace: atespace, Code: c}
	}
	return nil
}

// substrateAgentStateMu guards substrateControlTokens and
// substrateAgentRecords, which are shared process-wide across every
// SubstrateRuntime instance regardless of which config produced it.
//
// Per-agent state must live at this scope, not on the SubstrateRuntime
// instance: the broker resolves substrate as an auxiliary runtime keyed
// only by Name() ("substrate") and holds exactly one such runtime at a
// time, while NewSubstrateRuntime's memoization below is keyed per
// V1SubstrateConfig — a config change (editing egress_allow,
// snapshot_storage, ...), or a second substrate profile with different
// settings, produces a genuinely different *SubstrateRuntime instance. If
// control tokens and agent records lived on that instance instead of here,
// an agent started under one config would become unreachable for
// exec/list-with-project-labels the moment the broker resolved a different
// config — the same class of problem the per-instance gRPC connection
// memoization below addresses (there, triggered by every `start`; here, by
// a config change): moving this state to the one thing every instance
// shares, the process, is what keeps it reachable regardless of which
// config resolved the runtime.
var (
	substrateAgentStateMu sync.Mutex
	// substrateControlTokens maps "<atespace>/<actor>" to the control_token
	// minted at bootstrap (substrate-runtime.md §4 step 8: "keep it in
	// memory, keyed by `<atespace>/<actor>`").
	substrateControlTokens = make(map[string]string)
	// substrateAgentRecords maps actor UID to the label/metadata record
	// synthesised at Run (substrate-runtime.md §4: keyed by actor UID).
	substrateAgentRecords = make(map[string]*substrateAgentRecord)
	// substrateExecSecrets maps "<atespace>/<actor>" (the same id
	// substrateControlTokens is keyed by, set and deleted alongside it) to
	// that actor's substrateSecretCandidates, captured once at Run. Exec and
	// ExecWithStdin have no RunConfig of their own to redact against — only
	// id — so this is what lets them run a doExec failure (which can embed
	// the command's own stderr, or an HTTP error body) through the same
	// redactEnvValues scrubbing Run's own errors get via SubstrateRuntime.redact,
	// instead of returning it to the caller unredacted.
	substrateExecSecrets = make(map[string]map[string]string)
)

// substrateRuntimesMu and substrateRuntimes memoize SubstrateRuntime
// instances process-wide, keyed on a canonical encoding of the effective
// V1SubstrateConfig (substrateRuntimeCacheKey). This is about connections,
// not per-agent state (see substrateAgentStateMu above): each distinct
// config gets its own gRPC ClientConn, router client, and CA, since those
// legitimately differ per config, while per-agent state is shared by every
// instance regardless of config.
//
// This exists because the broker treats substrate as an auxiliary runtime,
// not its default one (substrate-runtime.md §2 never makes it the
// default): pkg/runtimebroker resolves a fresh Runtime from settings via
// GetRuntime on every `start` whose profile isn't the default, and would
// otherwise call NewSubstrateRuntime again each time. Without memoization,
// every call would dial a brand new gRPC ClientConn that is never closed,
// leaking one connection per agent start.
var (
	substrateRuntimesMu sync.Mutex
	substrateRuntimes   = make(map[string]*SubstrateRuntime)
)

// substrateRuntimeBuilder constructs a fresh *SubstrateRuntime for cfg
// (dialing ateapi and building a Kubernetes client). It is a package
// variable so tests can replace it with a fake and exercise the
// memoization/registry logic in NewSubstrateRuntime without real network or
// cluster access.
var substrateRuntimeBuilder = newSubstrateRuntimeFromConfig

// substrateRuntimeCacheKey returns a canonical, deterministic string key for
// cfg. encoding/json sorts map keys and has no other source of
// nondeterminism for this struct (all fields are strings, a string map, or
// a string slice), so two configs with the same field values always produce
// the same key regardless of construction order.
func substrateRuntimeCacheKey(cfg config.V1SubstrateConfig) (string, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("substrate: encode config for runtime memoization: %w", err)
	}
	return string(b), nil
}

// NewSubstrateRuntime returns the process-wide SubstrateRuntime for sc,
// building one on first use and reusing it on every subsequent call with an
// equal config (see substrateRuntimesMu). There is no auto-detect path for
// substrate (substrate-runtime.md §2): callers only reach this constructor when
// a profile explicitly selects it.
//
// Errors fall into two classes, and only the first matches
// ErrSubstrateProfileInvalid:
//   - deterministic config validation failures (a missing required endpoint,
//     or a block substrate.Validate rejects): the same settings fail the same
//     way on every attempt, so they are tagged with the sentinel;
//   - construct-time dependency failures from substrateRuntimeBuilder
//     (building the Kubernetes client, substrate.Dial's trust-bundle/CA load
//     and API dial): these can be transient, so they are returned untagged
//     and a broker whose default runtime hits one starts degraded rather
//     than refusing to start.
func NewSubstrateRuntime(sc *config.V1SubstrateConfig) (*SubstrateRuntime, error) {
	if sc == nil {
		sc = &config.V1SubstrateConfig{}
	}
	if sc.APIEndpoint == "" {
		return nil, substrateProfileInvalid(fmt.Errorf("substrate: runtimes.<name>.substrate.api_endpoint is required"))
	}
	if sc.RouterEndpoint == "" {
		return nil, substrateProfileInvalid(fmt.Errorf("substrate: runtimes.<name>.substrate.router_endpoint is required"))
	}
	if err := substrate.Validate(sc); err != nil {
		return nil, substrateProfileInvalid(err)
	}

	key, err := substrateRuntimeCacheKey(*sc)
	if err != nil {
		return nil, err
	}

	substrateRuntimesMu.Lock()
	defer substrateRuntimesMu.Unlock()

	if rt, ok := substrateRuntimes[key]; ok {
		return rt, nil
	}

	rt, err := substrateRuntimeBuilder(*sc)
	if err != nil {
		return nil, err
	}
	substrateRuntimes[key] = rt
	return rt, nil
}

// SetSubstrateRuntimeBuilderForTest overrides the process-wide constructor
// NewSubstrateRuntime uses to build a fresh *SubstrateRuntime for a config
// it hasn't seen before, for tests in other packages (e.g.
// pkg/runtimebroker) that need to exercise the real memoized-by-config
// resolution path — pkg/runtime/factory.go's "substrate" case calling
// NewSubstrateRuntime, exactly as pkg/agent.ResolveRuntimeWithRecord/GetRuntime do in
// production — without dialing real ateapi/Kubernetes.
//
// Also swaps substrateRuntimes (the memo NewSubstrateRuntime caches built
// instances in) for a fresh, empty map, and restores both the builder and
// the memo together in the returned func — mirroring
// resetSubstrateRuntimeRegistryForTest, this package's own in-package
// equivalent, which other packages cannot call since it is unexported.
// Without this, a fake instance built under a test's builder (e.g. one
// wired to an httptest server the test then closes) stays cached in the
// process-wide memo under its config's key after the test ends, and a
// later call with an equal config — in the same test's next iteration, a
// different test, or a shuffled run — gets that stale fake back instead of
// building a fresh one, hanging or failing against a server that no longer
// exists.
//
// Call the returned func (e.g. via t.Cleanup) to restore the previous
// builder and memo together.
func SetSubstrateRuntimeBuilderForTest(builder func(config.V1SubstrateConfig) (*SubstrateRuntime, error)) (restore func()) {
	substrateRuntimesMu.Lock()
	prevBuilder := substrateRuntimeBuilder
	prevRuntimes := substrateRuntimes
	substrateRuntimeBuilder = builder
	substrateRuntimes = make(map[string]*SubstrateRuntime)
	substrateRuntimesMu.Unlock()
	return func() {
		substrateRuntimesMu.Lock()
		substrateRuntimeBuilder = prevBuilder
		substrateRuntimes = prevRuntimes
		substrateRuntimesMu.Unlock()
	}
}

// WipeSubstrateAgentStateForTest clears the process-wide
// substrateControlTokens and substrateAgentRecords maps, for tests in other
// packages (e.g. pkg/runtimebroker) that need to simulate a runtime process
// restart against a *SubstrateRuntime built over a fake ateapi client. This
// is test-only support, exported (rather than kept package-private like
// this package's own equivalent used by substrate_restart_test.go) solely
// because pkg/runtimebroker cannot reach an unexported symbol here; nothing
// in production ever calls it — the real analog of "wipe" is simply a new
// broker process starting with empty maps. Call the returned func (e.g. via
// t.Cleanup) to restore the previous contents.
func WipeSubstrateAgentStateForTest() (restore func()) {
	substrateAgentStateMu.Lock()
	prevTokens, prevRecords, prevSecrets := substrateControlTokens, substrateAgentRecords, substrateExecSecrets
	substrateControlTokens = make(map[string]string)
	substrateAgentRecords = make(map[string]*substrateAgentRecord)
	substrateExecSecrets = make(map[string]map[string]string)
	substrateAgentStateMu.Unlock()
	return func() {
		substrateAgentStateMu.Lock()
		substrateControlTokens, substrateAgentRecords, substrateExecSecrets = prevTokens, prevRecords, prevSecrets
		substrateAgentStateMu.Unlock()
	}
}

// newSubstrateRuntimeFromConfig builds a fresh SubstrateRuntime by dialing
// ateapi and building an in-cluster Kubernetes client. This is
// substrateRuntimeBuilder's production implementation.
func newSubstrateRuntimeFromConfig(sc config.V1SubstrateConfig) (*SubstrateRuntime, error) {
	k8sClient, err := k8s.NewClientWithContext("", "")
	if err != nil {
		return nil, fmt.Errorf("substrate: build Kubernetes client: %w", err)
	}

	ctx := context.Background()
	conn, err := substrate.Dial(ctx, k8sClient.Clientset, substrate.DialerConfig{
		APIEndpoint:        sc.APIEndpoint,
		TokenAudience:      sc.TokenAudience,
		CAFile:             sc.CAFile,
		ClusterTrustBundle: sc.ClusterTrustBundle,
	})
	if err != nil {
		return nil, err
	}

	return &SubstrateRuntime{
		cfg:            sc,
		client:         substrate.NewControlClient(conn),
		conn:           conn,
		router:         substrate.NewRouterClient(sc.RouterEndpoint),
		k8sClient:      k8sClient.Clientset,
		now:            time.Now,
		sleep:          time.Sleep,
		sleepCtx:       sleepWithContext,
		healthzTimeout: defaultHealthzTimeout,
	}, nil
}

// NewSubstrateRuntimeForTest builds a SubstrateRuntime with injected
// dependencies and no real network/cluster access, for unit tests. Exported
// so tests in other packages (e.g. pkg/agent, pkg/runtimebroker) can drive a
// real SubstrateRuntime — List/Delete/Run's actual logic, not a
// reimplementation of it — through a fake ateapi.ControlClient and
// substrate.RouterClient, the same way this package's own tests do.
func NewSubstrateRuntimeForTest(client ateapi.ControlClient, router *substrate.RouterClient, k8sClient kubernetes.Interface, cfg config.V1SubstrateConfig) *SubstrateRuntime {
	return &SubstrateRuntime{
		cfg:            cfg,
		client:         client,
		router:         router,
		k8sClient:      k8sClient,
		now:            time.Now,
		sleep:          func(time.Duration) {},
		sleepCtx:       func(context.Context, time.Duration) error { return nil },
		healthzTimeout: defaultHealthzTimeout,
	}
}

func (r *SubstrateRuntime) Name() string { return "substrate" }

// PerProfileInstances implements PerProfileInstancesRuntime. Every substrate
// profile shares the runtime type "substrate", but each instance is bound to
// its own profile's V1SubstrateConfig (for example its own egress_allow), so
// a request naming a different substrate profile needs its own manager
// rather than the default runtime's.
func (r *SubstrateRuntime) PerProfileInstances() bool { return true }

var _ PerProfileInstancesRuntime = (*SubstrateRuntime)(nil)

// SupportsAttach implements AttachCapableRuntime. Substrate has no
// exec/attach/TTY primitive to dial: its broker-side PTY path
// would only reject the stream after a caller's WebSocket upgrade already
// succeeded, so callers report this up front instead of after the fact —
// the broker's direct-connect and control-channel PTY handlers ask
// HasAttachSupport on the live instance they already resolved
// (pkg/runtimebroker), and the Hub reads the same answer secondhand from
// the broker's stored metadata to choose the PTY path (and answers the
// CLI's attach preflight with it), not from a compiled runtime-type table.
func (r *SubstrateRuntime) SupportsAttach() bool { return false }

var _ AttachCapableRuntime = (*SubstrateRuntime)(nil)

// AsyncLaunchUnsupported implements AsyncLaunchUnsupportedRuntime. Run does
// not implement RunConfig.Checkpoint or RunConfig.OnResourceCreated: it
// calls neither hook, so the broker can neither cancel a substrate launch at
// a checkpoint nor learn which resources to clean up after it. The broker
// serves an async create request for this runtime synchronously instead.
func (r *SubstrateRuntime) AsyncLaunchUnsupported() bool { return true }

var _ AsyncLaunchUnsupportedRuntime = (*SubstrateRuntime)(nil)

// SupportsEmptyPerAgentWorkspace reports false: Run never mounts
// RunConfig.Workspace (the actor's filesystem comes from its template), so
// it cannot give an agent the private agents/<slug>/workspace directory an
// empty-per-agent project (design #2703) requires. Opting out makes a
// broker whose default runtime is substrate stop advertising the mode, so
// the hub fails such creates closed with 412 instead of silently running
// the agent without its workspace.
func (r *SubstrateRuntime) SupportsEmptyPerAgentWorkspace() bool { return false }

var _ EmptyPerAgentCapableRuntime = (*SubstrateRuntime)(nil)

// ExecUser returns "scion" — the tmux session runs under the scion user
// after sciontool init sets up the environment, same as every other
// runtime.
func (r *SubstrateRuntime) ExecUser() string { return "scion" }

// Run implements the 9 steps of substrate-runtime.md §4. Any failure after
// CreateActor triggers best-effort cleanup (delete the actor and its
// egress policy) before returning.
// errEmptyPerAgentSubstrate is returned by SubstrateRuntime.Run for an
// empty-per-agent agent (design #2703).
var errEmptyPerAgentSubstrate = errors.New("substrate: \"Empty directory per agent\" (empty-per-agent) workspaces are not supported on the substrate runtime, " +
	"which does not mount the agent's workspace directory; use a Docker, Podman, Apple or Kubernetes broker for this project")

func (r *SubstrateRuntime) Run(ctx context.Context, cfg RunConfig) (string, error) {
	// Checked before anything touches the control plane: substrate never
	// mounts RunConfig.Workspace, so it cannot give the agent its private
	// directory. SupportsEmptyPerAgentWorkspace=false keeps the hub from
	// dispatching here once the broker's heartbeat reports it; this covers
	// the window before that, as Cloud Run's rejectEmptyPerAgentOnCloudRun does.
	if isEmptyPerAgentRun(cfg) {
		return "", errEmptyPerAgentSubstrate
	}
	// Fail fast on a misconfigured egress_allow before touching the control
	// plane at all. NewSubstrateRuntime already validates this at
	// construction time; this is a defensive re-check in case a
	// SubstrateRuntime was ever built by another path (e.g. tests) that
	// skipped it.
	if err := substrate.Validate(&r.cfg); err != nil {
		return "", err
	}
	// Settings keys the v1 API has no target for refuse the Run rather
	// than being silently ignored.
	if err := checkSubstrateRemovedKeys(r.cfg); err != nil {
		return "", err
	}
	if err := checkSubstrateEgressTrustBundle(r.cfg); err != nil {
		return "", err
	}
	// Resolved before any control-plane call: v1 needs both a cpu and a
	// memory limit on the template, and an invalid value fails here.
	resources, err := resolveSubstrateResources(cfg.Resources)
	if err != nil {
		return "", err
	}
	if cfg.ProjectID == "" {
		// An empty ProjectID would hash to the same atespace for every
		// such agent (substrateAtespaceName has no other input), colliding
		// every caller that ever reaches Run without a real project
		// identity onto one shared atespace. Reject outright rather than
		// let that collision happen silently.
		return "", fmt.Errorf("substrate: RunConfig.ProjectID must not be empty")
	}

	atespace := substrateAtespaceName(cfg.ProjectID)
	actorName := cfg.Name
	id := atespace + "/" + actorName

	// Egress targets, before any control-plane call: they need only cfg
	// and r.cfg, and a refusal (an invalid CLOUD_ML_REGION, an egress
	// config error) then costs no create or cleanup.
	env := buildBootstrapEnv(cfg)
	targets, err := substrateEgressTargets(cfg, env, r.cfg)
	if err != nil {
		return "", r.redact(cfg, fmt.Errorf("substrate: resolve egress hosts for %s: %w", id, err))
	}

	// Step 1: atespace (verify-or-fail; operator mode never creates one).
	if err := r.ensureAtespace(ctx, atespace); err != nil {
		// The provisioning error's text holds only the atespace name and
		// the gRPC code (never the server message or any env value), so
		// it is returned as is: redact would flatten it to a string and
		// callers could no longer match ErrSubstrateAtespaceNotProvisioned.
		var perr *SubstrateAtespaceProvisioningError
		if errors.As(err, &perr) {
			return "", perr
		}
		return "", r.redact(cfg, err)
	}

	// Step 2: image must be digest-pinned. Tag resolution is not currently supported.
	if !isDigestPinned(cfg.Image) {
		return "", fmt.Errorf("substrate: image %q is not pinned by digest (@sha256:...); set a digest image in the agent's template or pass --image (tag resolution is not currently supported)", cfg.Image)
	}

	// Step 3: content-addressed template, created + waited-ready if new.
	templateName := substrateTemplateName(cfg.Image, r.cfg, resources)
	tmpl, err := buildActorTemplate(atespace, templateName, cfg.Image, r.cfg, resources)
	if err != nil {
		return "", err
	}
	if err := ensureActorTemplate(ctx, r.client, atespace, templateName, tmpl, templateReadyTimeout(r.cfg), r.sleep); err != nil {
		return "", r.redact(cfg, err)
	}

	// Step 4: create the actor.
	actor, err := r.client.CreateActor(ctx, &ateapi.CreateActorRequest{
		Actor: &ateapi.Actor{
			Metadata:      &ateapi.ResourceMetadata{Atespace: atespace, Name: actorName},
			ActorTemplate: &ateapi.ObjectRef{Atespace: atespace, Name: templateName},
		},
	})
	if err != nil {
		if status.Code(err) == codes.AlreadyExists {
			// Matches agent.isContainerNameInUseError's pattern-match on
			// the error text (pkg/agent/run.go), so the existing broker
			// handling for agent.ErrContainerNameInUse applies without
			// pkg/runtime depending on pkg/agent (which would cycle back).
			return "", fmt.Errorf("substrate: container name %q already in use in atespace %q", actorName, atespace)
		}
		return "", r.redact(cfg, fmt.Errorf("substrate: create actor %s: %w", id, err))
	}
	actorUID := actor.GetMetadata().GetUid()

	// Every step below cleans up the actor + policy on failure (best
	// effort — a fresh context, since ctx may already be near its
	// deadline/cancelled).
	// policyUID is the uid CreateActorEgressPolicy returned, once Step 5
	// has run.
	var policyUID string
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// The actor goes first and the policy only once the actor is gone
		// (design v0.5.5 §3.5 lifecycle invariant: a runnable actor never
		// loses its egress policy). Each delete carries the uid this Run created
		// (when known), so cleanup removes only this Run's actor and
		// policy, never those of a later incarnation created under the
		// same name. With an unknown policy uid (Step 5 failed) there is
		// no policy delete: a partly created policy is at worst an orphan
		// with no runnable actor.
		_, aerr := r.client.DeleteActor(cleanupCtx, substrateDeleteActorRequest(atespace, actorName, actorUID))
		if aerr != nil && status.Code(aerr) != codes.NotFound {
			return
		}
		if policyUID == "" {
			return
		}
		_, _ = r.client.DeleteActorEgressPolicy(cleanupCtx, substrateDeleteEgressPolicyRequest(atespace, actorName, policyUID))
	}

	// Step 5: egress policy, from the targets resolved before Step 1. It
	// is created before Step 6's ResumeActor (the only ResumeActor in
	// this runtime), and a failed create stops the Run before the resume.
	policy, err := r.client.CreateActorEgressPolicy(ctx, buildEgressPolicy(atespace, actorName, targets))
	if err != nil {
		cleanup()
		return "", r.redact(cfg, fmt.Errorf("substrate: create egress policy for %s: %w", id, err))
	}
	policyUID = policy.GetMetadata().GetUid()

	// Step 6: resume, then wait RUNNING.
	if _, err := r.client.ResumeActor(ctx, &ateapi.ResumeActorRequest{
		Actor: &ateapi.ObjectRef{Atespace: atespace, Name: actorName},
	}); err != nil {
		cleanup()
		return "", r.redact(cfg, fmt.Errorf("substrate: resume actor %s: %w", id, err))
	}
	if err := r.waitRunning(ctx, atespace, actorName); err != nil {
		cleanup()
		return "", r.redact(cfg, err)
	}

	// Step 7: wait for the control server to be awaiting bootstrap.
	if err := waitForHealthz(ctx, r.router, atespace, actorName, healthzAwaitingBootstrap, r.healthzTimeout, r.sleepCtx); err != nil {
		cleanup()
		return "", r.redact(cfg, err)
	}

	// Step 8: bootstrap.
	files, err := buildBootstrapFiles(cfg)
	if err != nil {
		cleanup()
		return "", r.redact(cfg, err)
	}
	startCmd, err := buildSubstrateStartCmd(cfg)
	if err != nil {
		cleanup()
		return "", r.redact(cfg, err)
	}
	controlToken, err := generateControlToken()
	if err != nil {
		cleanup()
		return "", err
	}
	nonce, err := r.bootstrapNonce(ctx, atespace, actorName, actorUID)
	if err != nil {
		cleanup()
		return "", r.redact(cfg, err)
	}
	if err := postBootstrap(ctx, r.router, atespace, actorName, nonce, bootstrapRequest{
		Env:          env,
		Files:        files,
		StartCmd:     startCmd,
		ControlToken: controlToken,
	}, func(s string) string { return redactEnvValues(s, substrateSecretCandidates(cfg)) }); err != nil {
		if errors.Is(err, errBootstrapHijacked) {
			// Treat as a compromise indicator, not a retry: delete the
			// actor and its egress policy so nothing keeps running under
			// config it never should have received, and fail loudly. See
			// errBootstrapHijacked's doc comment for the threat model.
			runtimeLog().Error("substrate: bootstrap hijack detected, deleting actor as a precaution",
				"atespace", atespace, "actor", actorName)
			cleanup()
			return "", fmt.Errorf("substrate: actor %s was bootstrapped by another caller before this broker's request reached it; deleted as a precaution", id)
		}
		var pathErr *bootstrapPathRejectedError
		if errors.As(err, &pathErr) {
			// A bootstrap file's Path failed serve-side validation
			// (symlink traversal, or an invalid path) — log the stable code
			// and the path explicitly here, rather than relying on a caller
			// to notice them inside the generic returned-error text below.
			// Both are configuration, never file content, so they're safe
			// in a log line (see deploy/substrate/README.md's "No symlink
			// traversal in a target's path" note). This is a structured
			// slog call (key/value attrs, not a format string), so a
			// newline or control byte in pathErr.path is already quoted by
			// slog's own attribute encoding — no separate %q is needed here
			// the way it is for bootstrapPathRejectedError.Error()'s plain
			// fmt.Sprintf (substrate_bootstrap.go), which has no such
			// built-in quoting.
			runtimeLog().Error("substrate: bootstrap rejected a file path",
				"atespace", atespace, "actor", actorName, "code", pathErr.code, "path", pathErr.path)
		}
		cleanup()
		return "", r.redact(cfg, err)
	}

	// The project path isn't a RunConfig field of its own (unlike Project/
	// ProjectID) — pkg/agent/run.go carries it as an annotation
	// ("scion.project_path", set unconditionally by Start from the
	// resolved project directory, hub-dispatched or not), the same
	// convention DockerRuntime.List (docker.go) and K8sRuntime.List
	// (k8s_runtime.go) read it by, annotations first and falling back to
	// labels for a caller that only set the label.
	projectPath := projectkeys.ProjectPathFromLabels(cfg.Annotations)
	if projectPath == "" {
		projectPath = projectkeys.ProjectPathFromLabels(cfg.Labels)
	}

	substrateAgentStateMu.Lock()
	substrateControlTokens[id] = controlToken
	substrateExecSecrets[id] = substrateSecretCandidates(cfg)
	substrateAgentRecords[actorUID] = &substrateAgentRecord{
		Labels: cfg.Labels,
		// scion.harness_config is the same label key
		// CloudRunSandboxRuntime.Run populates HarnessConfig from — see
		// labelValue and its use in cloudrun_sandbox_runtime.go.
		HarnessConfig: labelValue(cfg.Labels, "scion.harness_config"),
		Template:      cfg.Template,
		Project:       cfg.Project,
		ProjectID:     cfg.ProjectID,
		ProjectPath:   projectPath,
		Image:         cfg.Image,
	}
	substrateAgentStateMu.Unlock()

	// Step 9.
	return id, nil
}

// bootstrapNonce is the single call site for the bootstrap request's bearer
// value, so the broker has one place to change how it is derived.
// substrate-runtime.md §5.2 documents two options: MintActorJWT (verifiable
// actor identity via a systemInfo volume) and the fallback this function
// implements (first-bootstrap-wins, secured by a NetworkPolicy restricting
// router ingress to the broker namespace). §5.2 records this fallback as
// the one in use, not an open question.
//
// This is the fallback. It could not confirm MintActorJWT's alternative is
// even possible from ateapi.proto alone: a systemInfo TrustBundleDataSource
// projects a named, "allowlisted in atelet" trust bundle into the actor,
// and whether one of those allowlisted names carries what's needed to
// verify a substrate-issued actor JWT is opaque outside atelet's
// implementation. Do not switch this to MintActorJWT without confirming
// that it is actually usable here.
func (r *SubstrateRuntime) bootstrapNonce(ctx context.Context, atespace, actorName, actorUID string) (string, error) {
	return generateControlToken()
}

// ErrSubstrateIncarnationChanged reports that DeleteActor refused Delete's
// uid-conditioned actor delete because the actor now holding the name is not
// the one Delete read (its uid differs): a different incarnation exists, and
// nothing was deleted. A refused uid-conditioned policy delete after the
// actor delete is not this error: Delete returns nil, because the policy
// Delete read is gone and the later incarnation's policy is left in place.
var ErrSubstrateIncarnationChanged = errors.New("substrate: a different incarnation of the actor exists; nothing deleted")

// substrateDeleteEgressPolicyRequest builds a DeleteActorEgressPolicyRequest
// for the actor's "default" policy. A non-empty uid becomes a DeleteOptions
// precondition, so the server deletes only that policy object; an unknown
// uid sends no options.
func substrateDeleteEgressPolicyRequest(atespace, actorName, uid string) *ateapi.DeleteActorEgressPolicyRequest {
	req := &ateapi.DeleteActorEgressPolicyRequest{
		Actor: &ateapi.ObjectRef{Atespace: atespace, Name: actorName},
		Name:  substrateEgressPolicyName,
	}
	if uid != "" {
		req.Options = &ateapi.DeleteOptions{Uid: uid}
	}
	return req
}

// substrateDeleteActorRequest builds a DeleteActorRequest. A non-empty uid
// becomes a DeleteOptions precondition, so the server deletes only that
// incarnation; an unknown uid (the record-less path) sends no options.
func substrateDeleteActorRequest(atespace, actorName, uid string) *ateapi.DeleteActorRequest {
	req := &ateapi.DeleteActorRequest{Actor: &ateapi.ObjectRef{Atespace: atespace, Name: actorName}}
	if uid != "" {
		req.Options = &ateapi.DeleteOptions{Uid: uid}
	}
	return req
}

// isSubstrateUIDPreconditionFailure reports whether err is DeleteActor or
// DeleteActorEgressPolicy refusing a uid precondition. The v1 proto does
// not name the code for a DeleteOptions.uid mismatch; this assumes
// FailedPrecondition, which the
// live Phase 1d observation (design §7 gate item 2b) confirms. Aborted is
// deliberately not mapped: the v1 API uses it for "a concurrent write
// landed first; retry". Only a delete that sent a uid can fail this way.
func isSubstrateUIDPreconditionFailure(err error, uidSent bool) bool {
	return uidSent && status.Code(err) == codes.FailedPrecondition
}

// Delete implements the design v0.5.5 §3.5 lifecycle invariant (a
// runnable actor never loses its egress policy):
// DeleteActor first, then DeleteActorEgressPolicy (ignoring NotFound) only
// once the actor is gone, then drop the in-memory control token (and label
// record, best effort). If the policy delete then fails, no runnable actor
// remains and the orphan policy is reported.
//
// The policy's own uid and the actor's uid are read first, and each delete
// carries its uid as a precondition. A later incarnation created under the
// same name has a new actor uid and a new policy uid, so its actor is
// refused (ErrSubstrateIncarnationChanged, nothing deleted, in-memory state
// kept) and its policy is never removed. If DeleteActor fails for any
// other reason, the policy is left in place and the error returned; a
// retried Delete finishes the job. A policy read that fails with anything
// but NotFound returns an error before any delete; with no policy found
// there is no policy delete (a policy created later is at worst an orphan
// with no runnable actor). When the actor uid is unknown (the read failed,
// the record-less path) the actor delete is unconditional.
// P4: enforce ref.RunID (ptone/scion#2550). Today the actor name is reused
// across runs, so this still targets whatever actor holds the name.
func (r *SubstrateRuntime) Delete(ctx context.Context, ref RunRef) error {
	id := ref.ID
	atespace, actorName, err := splitSubstrateID(id)
	if err != nil {
		return err
	}

	// Only NotFound means "no policy", and then there is no policy delete.
	// Any other read error returns before anything is deleted. A retried
	// Delete finishes.
	var policyUID string
	policy, perr := r.client.GetActorEgressPolicy(ctx, &ateapi.GetActorEgressPolicyRequest{
		Actor: &ateapi.ObjectRef{Atespace: atespace, Name: actorName},
		Name:  substrateEgressPolicyName,
	})
	switch {
	case perr == nil:
		policyUID = policy.GetMetadata().GetUid()
	case status.Code(perr) != codes.NotFound:
		// The status code only: the server's text may carry the uid.
		return fmt.Errorf("substrate: delete actor %s: read egress policy (code %s); nothing was deleted, retry the delete", id, status.Code(perr))
	}
	var uid string
	if actor, gerr := r.client.GetActor(ctx, &ateapi.GetActorRequest{Actor: &ateapi.ObjectRef{Atespace: atespace, Name: actorName}}); gerr == nil {
		uid = actor.GetMetadata().GetUid()
	}

	deletedActor, err := r.client.DeleteActor(ctx, substrateDeleteActorRequest(atespace, actorName, uid))
	if isSubstrateUIDPreconditionFailure(err, uid != "") {
		// The server's error is not appended: it may carry the uid.
		return fmt.Errorf("%w (%s)", ErrSubstrateIncarnationChanged, id)
	}
	if err != nil && status.Code(err) != codes.NotFound {
		// The policy stays: the actor may still be able to run.
		return fmt.Errorf("substrate: delete actor %s: %w", id, err)
	}

	// The actor is gone. With no policy uid there is no policy delete. A
	// refused policy delete means the policy at the name belongs to a
	// later incarnation: it is not this Run's to remove, and this Run's
	// policy is gone, so Delete returns nil (not
	// ErrSubstrateIncarnationChanged).
	if policyUID != "" {
		_, err = r.client.DeleteActorEgressPolicy(ctx, substrateDeleteEgressPolicyRequest(atespace, actorName, policyUID))
		if err != nil && status.Code(err) != codes.NotFound && !isSubstrateUIDPreconditionFailure(err, true) {
			// No runnable actor remains; the orphan policy is reported by
			// its actor id and the status code only (the server's text may
			// carry the uid). A retried Delete removes it.
			return fmt.Errorf("substrate: actor %s was deleted, but its egress policy could not be deleted (code %s); an orphan policy remains, retry the delete", id, status.Code(err))
		}
	}
	if uid == "" {
		// The speculative GetActor above failed (a transient error, or a
		// race where some other caller's own GetActor/List landed first) —
		// fall back to DeleteActor's own response, which names the actor
		// it just removed. Without this, a failed GetActor alone would
		// leave uid empty and skip the substrateAgentRecords cleanup below
		// entirely, leaking that record for the lifetime of the process.
		uid = deletedActor.GetMetadata().GetUid()
	}

	substrateAgentStateMu.Lock()
	delete(substrateControlTokens, id)
	delete(substrateExecSecrets, id)
	if uid != "" {
		delete(substrateAgentRecords, uid)
	}
	substrateAgentStateMu.Unlock()
	return nil
}

// Stop is the same as Delete: nothing here suspends to a DATA
// snapshot yet, and faking a "stopped" state that just left the actor
// running (or claiming a durable stop that instead discarded the actor's
// state) would both be dishonest about what happened. See
// substrate-runtime.md §4's Stop row.
//
// TODO: SuspendActor with DATA scope instead, once resume-from-
// suspend and the $HOME durableDir layout (substrate-runtime.md §11) land, so Stop
// keeps the workspace and frees the worker rather than deleting the actor.
//
// ref.RunID is passed through so Stop picks up Delete's run enforcement.
// TODO(ptone/scion#2550 P4): Delete does not enforce ref.RunID yet.
func (r *SubstrateRuntime) Stop(ctx context.Context, ref RunRef) error {
	return r.Delete(ctx, ref)
}

// substrateAtespacePrefix is the naming convention substrateAtespaceName
// produces. List keeps it as a defensive check that an actor belongs to a
// scion-managed atespace; listAtespaceActors already drops any actor whose
// atespace is not the one it asked for.
const substrateAtespacePrefix = "scion-"

// List implements substrate-runtime.md §4.
//
// Source of the listing, operator mode (substrate v1 design §3.3): when
// labelFilter carries a project ID, List reads that project's atespace
// only, and a NotFound or PermissionDenied there returns the typed
// *SubstrateAtespaceProvisioningError. Otherwise it reads the atespace of
// each project in the injected ProjectSet; a ProjectSet that is unset, cold
// or unreadable fails the List (never an empty result), and an atespace
// that is NotFound or PermissionDenied is skipped with a logged, counted
// note. Any other error fails the List. List never issues an
// empty-atespace ListActors (that needs the global viewer role, which
// operator mode does not grant). maxActorListPages applies per atespace.
//
// AgentInfo.ProjectPath is populated from the record's ProjectPath (set at
// Run time from cfg.Annotations, falling back to cfg.Labels — see Run),
// mirroring DockerRuntime.List and K8sRuntime.List, so a project-scoped
// caller resolves against the correct project even when two record-having
// actors elsewhere share the same agent slug.
//
// Any List call filtered by "scion.name" without a project-scoping key (no
// project-name or project-ID label, canonical or deprecated-alias) reaches
// this guard: for example AgentManager.Delete/Stop's own re-list
// (pkg/agent/manager.go — the local CLI path via cmd/delete.go) and the
// unlabelled backward-compatibility fallbacks in LookupContainerID and
// LookupAgent (pkg/runtimebroker/server.go). LookupContainerID/LookupAgent's
// primary query (scopedNameFilter) does add a project key and is
// unaffected. The broker delete path resolves through
// resolveDeleteTarget/AgentManager.DeleteTarget, which filters by
// "scion.agent" and project ID rather than "scion.name" and hands the
// already-resolved entry to DeleteTarget, so it never reaches this unscoped
// shape. For that specific shape (a "scion.name" filter present, no
// project-scoping key in labelFilter, and more than one record-having
// actor sharing the requested slug), every such actor is excluded from the
// result: the caller sees no match rather than an arbitrary (and
// potentially wrong-project) one. This means an unscoped same-slug Delete,
// Stop, or lookup fallback becomes a no-op — never a wrong-actor action —
// in that scenario; a query that does carry a project key is unaffected.
//
// Known limitation, by design: a record-less actor (this runtime instance
// has no in-memory agent record for it — e.g. right after a broker
// restart) is reported under its actor name, containerName(project, agent)
// = "<project>--<agent>" (pkg/agent/run.go), not its agent slug, and with
// no project labels. It therefore never matches a caller-supplied slug or
// project filter, and Delete/Stop/Exec/Logs for it become a no-op rather
// than acting on it — but it still appears in an unfiltered
// "scion.agent=true" listing, so it isn't lost entirely.
//
// This is deliberate, not an oversight: recovering the agent slug from the
// actor name (inverting containerName) so a record-less actor could still
// be found by slug cannot be made to fail closed against every
// project-scoped caller (a lookup scoped to project B could still resolve
// to project A's sole record-less actor for the same slug, since nothing
// here could verify which project a record-less actor actually belonged
// to strongly enough for every caller). Rather than accept that risk, a
// record-less actor is never resolvable by slug, so a wrong-actor action is
// structurally impossible, at the cost of requiring an operator to
// re-identify (or simply restart) a record-less actor by hand. Hardening
// the generic slug-matching call path in pkg/agent is future work; the
// durable fix here is persisting agent records so they survive a broker
// restart in the first place, not reconstructing them from the actor name.
func (r *SubstrateRuntime) List(ctx context.Context, labelFilter map[string]string) ([]api.AgentInfo, error) {
	actors, skipped, err := r.listOperatorModeActors(ctx, labelFilter)
	if err != nil {
		return nil, err
	}
	markSkippedIncomplete(ctx, skipped)
	// The skip note describes the served-project scope, so only an
	// unfiltered List updates it: a project-scoped List never skips and
	// must not reset the note.
	if labelFilter[projectkeys.LabelProjectID] == "" {
		r.logSkipped(skipped)
	}

	substrateAgentStateMu.Lock()
	defer substrateAgentStateMu.Unlock()

	// Ambiguity guard: labelFilter identifies a caller looking for one
	// specific agent slug ("scion.name") without narrowing to a project (no
	// project-name or project-ID label key — see isProjectNameLabelKey/
	// isProjectIDLabelKey below), which is the shape any List by
	// "scion.name" without a project-scope key reaches: for example
	// AgentManager.Delete/Stop's own re-list
	// (pkg/agent/manager.go — the local CLI path via cmd/delete.go) and the
	// unlabelled fallbacks in LookupContainerID and LookupAgent
	// (pkg/runtimebroker/server.go). LookupContainerID/LookupAgent's
	// primary, project-scoped query (scopedNameFilter) is unaffected. The
	// broker delete path resolves through
	// resolveDeleteTarget/AgentManager.DeleteTarget, which filters by
	// project ID rather than by slug and never re-lists by "scion.name", so
	// it does not reach this guard. If two or more
	// record-having actors share that slug (only possible across different
	// projects — see the per-project uniqueness this runtime otherwise
	// relies on), an unscoped query has no way to pick the right one, and
	// picking one arbitrarily (e.g. ListActors order) risks acting on the
	// wrong project's agent. Rather than guess, every actor sharing that
	// slug is excluded from an unscoped-by-slug result: the caller sees no
	// match (a no-op) instead of a wrong-actor match. A project-scoped
	// query for the same slug is unaffected. Record-less actors do reach
	// this loop (the tally below ranges over every actor ListActors
	// returned) but are skipped by the rec == nil check, so they never
	// contribute a count; they also can't collide with a record-having
	// actor's tally in the first place, since a record-less actor reports
	// its own project-prefixed actor name as "scion.name" (see this
	// function's doc comment), never a bare slug.
	requestedName, hasNameFilter := labelFilter["scion.name"]
	hasProjectScope := false
	for k, v := range labelFilter {
		if v != "" && (isProjectNameLabelKey(k) || isProjectIDLabelKey(k)) {
			hasProjectScope = true
			break
		}
	}

	slugCounts := make(map[string]int)
	if hasNameFilter && !hasProjectScope {
		for _, actor := range actors {
			rec := substrateAgentRecords[actor.GetMetadata().GetUid()]
			if rec == nil {
				continue
			}
			if name := rec.Labels["scion.name"]; name != "" {
				slugCounts[name]++
			}
		}
	}

	var agents []api.AgentInfo
	for _, actor := range actors {
		if !strings.HasPrefix(actor.GetMetadata().GetAtespace(), substrateAtespacePrefix) {
			continue
		}

		actorName := actor.GetMetadata().GetName()
		atespace := actor.GetMetadata().GetAtespace()
		rec := substrateAgentRecords[actor.GetMetadata().GetUid()]

		// "scion.agent" is always set (pkg/runtimebroker lists all agents
		// by it), so a record-less actor still appears in an unfiltered
		// listing even though — see this function's doc comment — it
		// never carries a slug or project identity a caller can filter or
		// match by.
		labels := map[string]string{
			"scion.name":  actorName,
			"scion.agent": "true",
		}
		var template, harnessConfig, project, projectID, projectPath, image string
		if rec != nil {
			for k, v := range rec.Labels {
				labels[k] = v
			}
			template = rec.Template
			harnessConfig = rec.HarnessConfig
			project = rec.Project
			projectID = rec.ProjectID
			projectPath = rec.ProjectPath
			image = rec.Image
		}

		if rec != nil && hasNameFilter && !hasProjectScope &&
			labels["scion.name"] == requestedName && slugCounts[requestedName] > 1 {
			continue
		}

		if !substrateLabelsMatch(labels, project, projectID, labelFilter) {
			continue
		}

		agents = append(agents, api.AgentInfo{
			ContainerID: atespace + "/" + actorName,
			// Name is the agent slug (labels["scion.name"]), matching every
			// other runtime's convention (e.g. DockerRuntime.List) — not
			// the actor name, which is containerName(project, agent) and
			// therefore project-prefixed. AgentManager.Delete/Stop
			// (pkg/agent/manager.go) match a caller-supplied agent ID
			// against Name, so a project-prefixed Name never matches and
			// both silently no-op instead of deleting anything
			// (hardening that call path in general is future work; this
			// is the substrate-specific root cause). This
			// only ever differs from the actor name for a record-having
			// actor (labels["scion.name"] above is overwritten by
			// rec.Labels, which always has the real one); see this
			// function's doc comment for why a record-less actor is
			// deliberately not given the same treatment.
			Name:          labels["scion.name"],
			Runtime:       r.Name(),
			Phase:         substratePhase(actor.GetStatus().GetState()),
			Labels:        labels,
			Template:      template,
			HarnessConfig: harnessConfig,
			Project:       project,
			ProjectID:     projectID,
			ProjectPath:   projectPath,
			Image:         image,
			RunID:         labels[api.LabelRunID],
		})
	}
	return agents, nil
}

// skippedAtespace is a served project's atespace an unfiltered List
// skipped: NotFound (not provisioned) or PermissionDenied (not readable).
type skippedAtespace struct {
	Atespace string
	Code     codes.Code
}

// markSkippedIncomplete marks the caller's ListCompleteness incomplete when
// a List skipped a PermissionDenied atespace, on any path. Such a skip may
// hide live actors (the atespace may exist and hold them): the broker
// heartbeat then reports that target incomplete instead of letting the hub
// reconcile the hidden agents as missing. A NotFound skip means no
// atespace exists, so it hides nothing and the List stays complete.
func markSkippedIncomplete(ctx context.Context, skipped []skippedAtespace) {
	var hidden int
	for _, sk := range skipped {
		if sk.Code == codes.PermissionDenied {
			hidden++
		}
	}
	if hidden > 0 {
		markListIncomplete(ctx, fmt.Sprintf("substrate: %d served atespace(s) not readable (PermissionDenied); their actors are unknown", hidden))
	}
}

// logSkipped logs an unfiltered List's skipped atespaces: at Warn only when
// the skipped set changes (with its count), at Debug while it stays the
// same, and at Info once when it clears, because the heartbeat lists every
// interval.
func (r *SubstrateRuntime) logSkipped(skipped []skippedAtespace) {
	var hidden int
	parts := make([]string, 0, len(skipped))
	for _, sk := range skipped {
		if sk.Code == codes.PermissionDenied {
			hidden++
		}
		parts = append(parts, sk.Atespace+":"+sk.Code.String())
	}
	sort.Strings(parts)
	key := strings.Join(parts, ",")

	r.skipLogMu.Lock()
	changed := key != r.lastSkipKey
	r.lastSkipKey = key
	r.skipLogMu.Unlock()

	switch {
	case len(skipped) > 0 && changed:
		slog.Warn("substrate: list skipped served projects with no provisioned or readable atespace",
			"skipped", len(skipped), "permission_denied", hidden, "atespaces", parts)
	case len(skipped) > 0:
		slog.Debug("substrate: list still skipping served projects with no provisioned or readable atespace",
			"skipped", len(skipped), "permission_denied", hidden)
	case changed:
		slog.Info("substrate: list skips no served project")
	}
}

// errSubstrateProjectSetUnset fails an unfiltered List on a runtime the
// broker never gave a ProjectSet: listing nothing would hide every actor.
var errSubstrateProjectSetUnset = errors.New("substrate: list actors: no served-project set (the runtime broker has not provided one)")

// listOperatorModeActors returns the actors List reports, read per atespace
// (see List's doc comment), and the atespaces an unfiltered listing skipped
// because they were NotFound or PermissionDenied.
func (r *SubstrateRuntime) listOperatorModeActors(ctx context.Context, labelFilter map[string]string) ([]*ateapi.Actor, []skippedAtespace, error) {
	if projectID := labelFilter[projectkeys.LabelProjectID]; projectID != "" {
		// A single project: its atespace only. A NotFound or
		// PermissionDenied comes back as the typed provisioning error.
		actors, err := r.listAtespaceActors(ctx, substrateAtespaceName(projectID))
		if err != nil {
			return nil, nil, err
		}
		return actors, nil, nil
	}

	ps := r.currentProjectSet()
	if ps == nil {
		return nil, nil, errSubstrateProjectSetUnset
	}
	projectIDs, err := ps.ServedProjects(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("substrate: list actors: served projects unavailable: %w", err)
	}
	var actors []*ateapi.Actor
	var skipped []skippedAtespace
	for _, projectID := range projectIDs {
		if projectID == "" {
			continue
		}
		atespace := substrateAtespaceName(projectID)
		got, err := r.listAtespaceActors(ctx, atespace)
		if err != nil {
			var perr *SubstrateAtespaceProvisioningError
			if errors.As(err, &perr) {
				// A served project with no provisioned (or no
				// readable) substrate atespace, common on a broker
				// that also serves other runtimes.
				skipped = append(skipped, skippedAtespace{Atespace: atespace, Code: perr.Code})
				continue
			}
			return nil, nil, err
		}
		actors = append(actors, got...)
	}
	return actors, skipped, nil
}

// listAtespaceActors pages ListActors over one atespace, up to
// maxActorListPages pages, and keeps only actors that report that
// atespace. A NotFound or PermissionDenied returns the typed
// *SubstrateAtespaceProvisioningError; any other failure a plain error.
func (r *SubstrateRuntime) listAtespaceActors(ctx context.Context, atespace string) ([]*ateapi.Actor, error) {
	var actors []*ateapi.Actor
	pageToken := ""
	for page := 0; ; page++ {
		if page >= maxActorListPages {
			return nil, fmt.Errorf("substrate: list actors in atespace %s: exceeded %d pages", atespace, maxActorListPages)
		}
		resp, err := r.client.ListActors(ctx, &ateapi.ListActorsRequest{
			Atespace: atespace,
			Options:  &ateapi.ListOptions{PageSize: substrateListPageSize, PageToken: pageToken},
		})
		if err != nil {
			if perr := atespaceProvisioningError(atespace, err); perr != nil {
				return nil, perr
			}
			return nil, fmt.Errorf("substrate: list actors in atespace %s: %w", atespace, err)
		}
		for _, actor := range resp.GetActors() {
			if actor.GetMetadata().GetAtespace() == atespace {
				actors = append(actors, actor)
			}
		}
		next := resp.GetNextPageToken()
		if next != "" && next == pageToken {
			return nil, fmt.Errorf("substrate: list actors in atespace %s: server returned a repeated page token", atespace)
		}
		pageToken = next
		if pageToken == "" {
			break
		}
	}
	return actors, nil
}

// RecordlessActors implements the broker's optional record-less-actor
// capability (see pkg/runtimebroker's RecordlessActorProber): given
// projectID, it returns the atespace that project maps to and every
// actor in it that this runtime process has no in-memory record for
// (substrateAgentRecords, keyed by actor UID — lost across a
// process restart for any actor a previous process created) AND that is not
// already in ACTOR_STATE_DELETING (see the loop below for why: a record-less
// actor already being deleted needs no further protection, and excluding it
// is what keeps ordinary same-project stop-then-delete sequences — no
// restart involved — from being misreported as "broker restarted").
//
// Race window: there is a narrow gap between CreateActor succeeding (Run,
// above) and this function's own caller observing the record it writes
// afterward — spanning waitRunning, healthz and bootstrap, so on the order
// of tens of seconds. An actor created in that window looks record-less to
// a concurrent call here, exactly like a genuinely pre-restart actor does.
// This is not hardened further: the caller (resolveDeleteTarget/stopAgent)
// only ever turns a would-be success into an explicit error on a hit, never
// selects a delete/stop target from this list, so the failure mode is a
// spurious 409 on an unrelated absent-slug request in the same project (or
// on the in-flight agent itself), never a wrong action. See
// deploy/substrate/README.md, consequence (c).
//
// Positive rule for what counts as a project atespace's actor: Run (above)
// is the only call site in this runtime that creates an Actor in a
// project's own atespace, and it creates exactly one per real agent, named
// cfg.Name. ActorTemplate and Tag are distinct resource kinds that
// ListActors never returns. Scoping the list to that one atespace (both via
// the request's Atespace field and the defensive equality check below) is
// the only exclusion this method applies.
//
// Known gap: in the v1 API an ActorTemplate's golden actor lives in the
// template's own atespace, which for scion is the project atespace
// (third_party/substrate/ateapi/v1/ateapi.proto:218-219: "Delete an
// ActorTemplate together with its golden actor and golden tag in the
// template's atespace"). It is temporary: the template controller deletes
// it after tagging the golden snapshot (:1244-1245). While a template
// builds, that golden actor may therefore appear in this list with no
// record and count as record-less (and List reports it as an agent). The
// proto gives no naming contract to filter it on, so it is not filtered
// here; the live Phase 1d observation and a design follow-up decide the
// fix.
func (r *SubstrateRuntime) RecordlessActors(ctx context.Context, projectID string) (atespace string, actors []RecordlessActor, err error) {
	atespace = substrateAtespaceName(projectID)

	var rawActors []*ateapi.Actor
	pageToken := ""
	for page := 0; ; page++ {
		if page >= maxRecordlessActorListPages {
			return atespace, nil, fmt.Errorf("substrate: list actors in atespace %s: exceeded %d pages", atespace, maxRecordlessActorListPages)
		}
		resp, err := r.client.ListActors(ctx, &ateapi.ListActorsRequest{
			Atespace: atespace,
			Options:  &ateapi.ListOptions{PageSize: substrateListPageSize, PageToken: pageToken},
		})
		if err != nil {
			return atespace, nil, fmt.Errorf("substrate: list actors in atespace %s: %w", atespace, err)
		}
		rawActors = append(rawActors, resp.GetActors()...)
		next := resp.GetNextPageToken()
		if next != "" && next == pageToken {
			return atespace, nil, fmt.Errorf("substrate: list actors in atespace %s: server returned a repeated page token", atespace)
		}
		pageToken = next
		if pageToken == "" {
			break
		}
	}

	substrateAgentStateMu.Lock()
	defer substrateAgentStateMu.Unlock()

	for _, actor := range rawActors {
		if actor.GetMetadata().GetAtespace() != atespace {
			continue
		}
		if substrateAgentRecords[actor.GetMetadata().GetUid()] != nil {
			continue
		}
		if actor.GetStatus().GetState() == ateapi.ActorState_ACTOR_STATE_DELETING {
			// A record-less actor already in ACTOR_STATE_DELETING needs no
			// protection here: a DeleteActor for it has already been
			// accepted, so it is on its way out, restart or not. (Delete,
			// above, removes the egress policy only after DeleteActor
			// succeeded, so a DELETING actor may still have its policy
			// until Delete's next step or a retried Delete removes it; a
			// broker restart in between can leave a policy object without
			// a runnable actor, never a runnable actor without its
			// policy.) Excluding
			// it here is also what keeps ordinary, no-restart operation
			// working: Stop is Delete (this runtime's Stop doc
			// comment), which drops the in-memory record immediately but
			// leaves the actor listed in DELETING for some time afterward
			// (the fire-and-forget note above) — without this exclusion, an
			// absent-slug delete/stop in the same project during that window
			// would falsely report "broker restarted" even though nothing
			// restarted.
			//
			// This exclusion is safe to apply unconditionally, not just
			// "usually", with respect to the egress invariant: an actor in
			// DELETING keeps whatever egress policy it has (Delete's own
			// ordering, above, never removes the policy first), so
			// excluding it here cannot leave a runnable actor without its
			// policy, whether or not the DELETING state itself ever
			// resolves. The
			// proto documents no transition out of DELETING back to a live
			// state either: RevertActor, the one RPC that returns an actor
			// to a pre-delete state, only accepts crashed or running
			// actors, or suspended ones with a node-local snapshot
			// (third_party/substrate/ateapi/v1/ateapi.proto:52-54), and a
			// deleted actor stops being listed (there is no
			// ACTOR_STATE_DELETED for it to sit in; the live Phase 1d
			// observation re-checks this against v1's delete_time).
			// ActorStatus.state is also `+k8s:required` (proto:894), so a
			// listed actor's state is never zero-valued/unknown here.
			// DELETING actors are observed either to finish deleting or to
			// stay stuck; none returns to a live state.
			//
			// It is NOT unconditionally safe with respect to the broader
			// invariant this whole mechanism protects: an actor already
			// DELETING when a broker restart happens is excluded here, so a
			// delete of its slug afterward returns the ordinary idempotent
			// 404 instead of 409, and the hub drops its record while the
			// actor may still be sitting in the cluster, stuck. That is a
			// deliberate, documented exception (it trades a rare, stuck
			// actor, which keeps its egress policy, for not reintroducing a
			// false 409 on ordinary stop-then-delete). See
			// deploy/substrate/README.md, "After a broker restart",
			// consequence (d) (to be re-worded for the v1 delete order in
			// the docs phase).
			continue
		}
		actors = append(actors, RecordlessActor{Name: actor.GetMetadata().GetName(), UID: actor.GetMetadata().GetUid()})
	}
	return atespace, actors, nil
}

// maxRecordlessActorListPages bounds RecordlessActors' ListActors paging
// loop: a misbehaving server that never returns an empty next_page_token
// would otherwise spin until ctx expires. A page holds an unspecified but
// presumably large number of actors server-side; the atespace this scopes
// to is one project's own agents, not a cluster-wide listing, so this is
// generous headroom for that without leaving memory use effectively
// unbounded. A package-level var, not a const, so a test can lower it to
// exercise the cap without paging through this many fake responses.
var maxRecordlessActorListPages = 100

// maxActorListPages bounds List's ListActors paging loop PER ATESPACE
// (listAtespaceActors), the same way maxRecordlessActorListPages bounds
// RecordlessActors' — a misbehaving server that never returns an empty
// next_page_token would otherwise spin until ctx expires. List reads one
// atespace per served project, so its overall call count is bounded by
// the served-project count times this cap (substrate v1 design §3.3). The
// value is unchanged from when List made a single cluster-wide query. A
// package-level var, not a const, for the same test-lowering reason as
// maxRecordlessActorListPages.
var maxActorListPages = 1000

// substrateListPageSize is the ListOptions.page_size List and
// RecordlessActors request. The server may return fewer and coerces
// values above 1000 to 1000.
const substrateListPageSize int32 = 500

// isProjectNameLabelKey reports whether key is the project-name label key.
func isProjectNameLabelKey(key string) bool {
	return key == projectkeys.LabelProject
}

// isProjectIDLabelKey reports whether key is the project-ID label key. See
// isProjectNameLabelKey.
func isProjectIDLabelKey(key string) bool {
	return key == projectkeys.LabelProjectID
}

// substrateLabelsMatch mirrors the label-filter pattern used by the other
// runtimes (e.g. CloudRunSandboxRuntime.List): an entry with no explicit
// label for a filtered project/project-id key still matches on the
// synthesised project/projectID fields.
func substrateLabelsMatch(labels map[string]string, project, projectID string, filter map[string]string) bool {
	for k, v := range filter {
		actual := labels[k]
		if actual == "" {
			switch {
			case isProjectNameLabelKey(k):
				actual = project
			case isProjectIDLabelKey(k):
				actual = projectID
			}
		}
		if actual != v {
			return false
		}
	}
	return true
}

// substratePhase maps ateapi.ActorState onto scion's AgentInfo.Phase
// vocabulary.
func substratePhase(s ateapi.ActorState) string {
	switch s {
	case ateapi.ActorState_ACTOR_STATE_RESUMING:
		return "provisioning"
	case ateapi.ActorState_ACTOR_STATE_RUNNING:
		return "running"
	case ateapi.ActorState_ACTOR_STATE_SUSPENDING:
		return "stopping"
	case ateapi.ActorState_ACTOR_STATE_SUSPENDED:
		return "stopped"
	case ateapi.ActorState_ACTOR_STATE_CRASHED:
		return "error"
	case ateapi.ActorState_ACTOR_STATE_DELETING:
		return "stopping"
	case ateapi.ActorState_ACTOR_STATE_REVERTING:
		return "provisioning"
	default:
		return "unknown"
	}
}

// GetLogs does not read worker pod logs (substrate-runtime.md §4). Worker
// pods are shared across atespaces and users, so a pod-level log read would
// return other tenants' actor output — and any worker-level lines naming
// other atespaces — alongside the caller's own. It returns
// ErrLogsNotSupported and makes no ateapi or Kubernetes call.
func (r *SubstrateRuntime) GetLogs(ctx context.Context, id string) (string, error) {
	return "", ErrLogsNotSupported
}

// Exec implements substrate-runtime.md §4: POST /scion/v1/exec via the
// router, using the control_token minted at bootstrap.
func (r *SubstrateRuntime) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	atespace, actorName, err := splitSubstrateID(id)
	if err != nil {
		return "", err
	}

	substrateAgentStateMu.Lock()
	token, ok := substrateControlTokens[id]
	substrateAgentStateMu.Unlock()
	if !ok {
		return "", fmt.Errorf("substrate: no control token cached for %s (only the broker process that bootstrapped it holds this in memory; lost on that process's restart, or if a different broker process bootstrapped this actor)", id)
	}

	res, err := doExec(ctx, r.router, atespace, actorName, token, cmd, nil, r.ExecUser(), defaultExecTimeout, r.execRedactor(id))
	if err != nil {
		return "", r.redactExecErr(id, err)
	}
	return res.Stdout, nil
}

// execStdinProbeArgv returns a fresh no-op argv slice for the capability
// probe below to run: it exists purely to elicit a stdin_supported response,
// never to do real work, so it is harmless on a control server old enough
// not to understand Stdin at all (it just runs "true" and ignores whatever
// it was sent). A function returning a new slice each call, rather than a
// shared package variable, so nothing else in the package could mutate what
// the probe runs.
func execStdinProbeArgv() []string {
	return []string{"true"}
}

// ExecWithStdin implements the Runtime interface's stdin-piped exec (see
// interface.go's doc comment): stdin is delivered via execRequest.Stdin over
// the same control-server exec path Exec uses, instead of being embedded in
// cmd's argv, so a caller delivering a secret (e.g. resetAuth's token) never
// puts it where it would be readable from the control server's own process
// argv via /proc/<pid>/cmdline. stdin is capped at maxExecStdinBytes and
// never appears in any returned error.
//
// Before running the real command, this sends an uncached capability probe
// — a no-op exec with a 1-byte stdin payload — and requires the response to
// confirm StdinSupported. Without the probe, an old control server that
// doesn't understand ExecRequest.Stdin at all would silently ignore it and
// run the real command anyway with nothing attached to its stdin: for
// resetAuth's write-then-rename script, that means `cat` reads immediate
// EOF and the rename replaces a working token file with an empty one before
// doExec's own post-exec StdinSupported check (kept below as defence in
// depth) ever gets a chance to fail the call. The probe never runs the real
// command or touches real state, so a version-skew failure here is always
// side-effect-free. Deliberately uncached: resetAuth is rare enough that the
// extra round trip doesn't matter, and caching would risk trusting a stale
// answer across an actor restart or an image upgrade.
func (r *SubstrateRuntime) ExecWithStdin(ctx context.Context, id string, cmd []string, stdin io.Reader) (string, error) {
	atespace, actorName, err := splitSubstrateID(id)
	if err != nil {
		return "", err
	}

	substrateAgentStateMu.Lock()
	token, ok := substrateControlTokens[id]
	substrateAgentStateMu.Unlock()
	if !ok {
		return "", fmt.Errorf("substrate: no control token cached for %s (only the broker process that bootstrapped it holds this in memory; lost on that process's restart, or if a different broker process bootstrapped this actor)", id)
	}

	data, err := readExecStdin(stdin)
	if err != nil {
		return "", err
	}

	if _, err := doExec(ctx, r.router, atespace, actorName, token, execStdinProbeArgv(), []byte("x"), r.ExecUser(), defaultExecTimeout, r.execRedactor(id)); err != nil {
		if errors.Is(err, errStdinUnsupported) {
			// Only this specific failure means what it says: the probe
			// reached the control server, ran, and the server never
			// confirmed it understood Stdin. Any other probe failure
			// (a transport error, an auth rejection, "true" missing from
			// the image) has nothing to do with version skew and must not
			// be reported as if it did.
			return "", r.redactExecErr(id, fmt.Errorf("substrate: stdin capability probe failed for %s: control server may be running an image older than the reset-auth stdin change; upgrade the actor's sciontool image: %w", id, err))
		}
		return "", r.redactExecErr(id, fmt.Errorf("substrate: stdin capability probe failed for %s: %w", id, err))
	}

	res, err := doExec(ctx, r.router, atespace, actorName, token, cmd, data, r.ExecUser(), defaultExecTimeout, r.execRedactor(id))
	if err != nil {
		return "", r.redactExecErr(id, err)
	}
	return res.Stdout, nil
}

// Attach is not currently supported (substrate-runtime.md §4).
// The broker's PTY switch (pty_handlers.go) returns a clean error before
// reaching this method; it exists to satisfy the Runtime interface and as
// a defensive fallback.
func (r *SubstrateRuntime) Attach(ctx context.Context, id string) error {
	return fmt.Errorf("substrate: attach not currently supported on substrate")
}

// Sync is not supported: there is no host filesystem backing an actor's
// workspace to sync to/from. Use the hub workspace API.
func (r *SubstrateRuntime) Sync(ctx context.Context, id string, direction SyncDirection) error {
	return fmt.Errorf("substrate: workspace sync is not supported on substrate; use the hub workspace API")
}

// GetWorkspacePath is not supported for the same reason as Sync.
func (r *SubstrateRuntime) GetWorkspacePath(ctx context.Context, id string) (string, error) {
	return "", fmt.Errorf("substrate: no host workspace path on substrate; use the hub workspace API")
}

// ImageExists always reports true: Substrate pulls the image itself when
// the actor's worker starts it, and scion never touches a local image
// cache for this runtime.
func (r *SubstrateRuntime) ImageExists(ctx context.Context, image string) (bool, error) {
	return true, nil
}

// ImageID returns the digest portion of image (the part after "@"), or the
// whole string if it carries none.
func (r *SubstrateRuntime) ImageID(ctx context.Context, image string) (string, error) {
	if _, digest, ok := strings.Cut(image, "@"); ok {
		return digest, nil
	}
	return image, nil
}

// RemoveImage is a no-op: Substrate manages its own image cache.
func (r *SubstrateRuntime) RemoveImage(ctx context.Context, image string) error { return nil }

// PullImage is a no-op: Substrate pulls the image itself.
func (r *SubstrateRuntime) PullImage(ctx context.Context, image string) error { return nil }

// ensureAtespace verifies that atespace exists and the broker can read it
// (substrate v1 design §3.7, operator mode). It never creates one: on
// NotFound or PermissionDenied it returns a
// *SubstrateAtespaceProvisioningError asking the operator to provision the
// atespace and grant the broker editor on it. Creating atespaces needs the
// global owner role, which operator mode does not grant.
func (r *SubstrateRuntime) ensureAtespace(ctx context.Context, atespace string) error {
	_, err := r.client.GetAtespace(ctx, &ateapi.GetAtespaceRequest{
		Atespace: &ateapi.ObjectRef{Name: atespace},
	})
	if err == nil {
		return nil
	}
	if perr := atespaceProvisioningError(atespace, err); perr != nil {
		return perr
	}
	return fmt.Errorf("substrate: get atespace %q: %w", atespace, err)
}

// waitRunning polls GetActor until state is RUNNING, or defaultActorRunningTimeout
// elapses. A CRASHED state fails fast rather than waiting out the timeout.
func (r *SubstrateRuntime) waitRunning(ctx context.Context, atespace, actorName string) error {
	deadline := r.now().Add(defaultActorRunningTimeout)
	for {
		actor, err := r.client.GetActor(ctx, &ateapi.GetActorRequest{Actor: &ateapi.ObjectRef{Atespace: atespace, Name: actorName}})
		if err != nil {
			return fmt.Errorf("substrate: get actor %s/%s: %w", atespace, actorName, err)
		}
		state := actor.GetStatus().GetState()
		if state == ateapi.ActorState_ACTOR_STATE_RUNNING {
			return nil
		}
		if state == ateapi.ActorState_ACTOR_STATE_CRASHED {
			return fmt.Errorf("substrate: actor %s/%s crashed while starting", atespace, actorName)
		}
		if r.now().After(deadline) {
			return fmt.Errorf("substrate: actor %s/%s did not reach RUNNING within %s (state: %s)", atespace, actorName, defaultActorRunningTimeout, state)
		}
		r.sleep(actorRunningPollInterval)
	}
}

// redact strips any secret value the runtime placed in the bootstrap env
// from err's message before it is returned to a caller (and, eventually,
// logs or an HTTP response — this mirrors TestCloudRunSandboxRun_
// ErrorDoesNotLeakEnvValues).
func (r *SubstrateRuntime) redact(cfg RunConfig, err error) error {
	if err == nil {
		return nil
	}
	return errors.New(redactEnvValues(err.Error(), substrateSecretCandidates(cfg)))
}

// redactExecErr is redact's counterpart for Exec and ExecWithStdin, which
// have no RunConfig of their own at call time — only id. It looks up the
// secret candidates substrateExecSecrets cached for id at Run (see that
// map's own doc comment) and redacts err the same way redact does; if id has
// no cached entry (its actor was deleted, or never bootstrapped by this
// process — mirroring the "no control token cached" case the caller already
// handles separately), err is returned unredacted rather than dropped,
// since doExec's own error text never includes the one piece of id-less
// secret material this path guards against going further.
//
// doExec already redacts the control-server output it embeds, before
// truncating it (see execRedactor); this pass covers the rest of the
// message.
func (r *SubstrateRuntime) redactExecErr(id string, err error) error {
	if err == nil {
		return nil
	}
	secrets, ok := substrateExecSecretsFor(id)
	if !ok {
		return err
	}
	// Keep doExec's command exit typed, including when a caller wrapped it
	// (ExecWithStdin's stdin probe does), so callers can still tell a
	// command result from a runtime failure. Its Output was already
	// redacted before truncation in doExec; redacting the fields again is
	// idempotent.
	var exitErr *CommandExitError
	if errors.As(err, &exitErr) {
		redacted := &CommandExitError{
			Runtime: exitErr.Runtime,
			Target:  redactEnvValues(exitErr.Target, secrets),
			Code:    exitErr.Code,
			Output:  redactEnvValues(exitErr.Output, secrets),
		}
		if error(exitErr) == err {
			return redacted
		}
		// Wrapped: keep the wrapper's context, redacted as a whole exactly
		// like the untyped path below, and unwrap only to the redacted
		// copy, never to the original chain.
		return &redactedExecError{msg: redactEnvValues(err.Error(), secrets), cause: redacted}
	}
	return errors.New(redactEnvValues(err.Error(), secrets))
}

// redactedExecError is a redacted error message that still unwraps to the
// redacted CommandExitError it wraps, so errors.As keeps working after
// redactExecErr.
type redactedExecError struct {
	msg   string
	cause *CommandExitError
}

func (e *redactedExecError) Error() string { return e.msg }

func (e *redactedExecError) Unwrap() error { return e.cause }

// execRedactor returns the redaction doExec applies to control-server output
// for id before truncating it: the same substrateExecSecrets lookup
// redactExecErr uses, or no redaction when id has no cached entry.
func (r *SubstrateRuntime) execRedactor(id string) func(string) string {
	secrets, ok := substrateExecSecretsFor(id)
	if !ok {
		return nil
	}
	return func(s string) string { return redactEnvValues(s, secrets) }
}

// substrateExecSecretsFor returns the secret candidates cached for id at Run.
func substrateExecSecretsFor(id string) (map[string]string, bool) {
	substrateAgentStateMu.Lock()
	defer substrateAgentStateMu.Unlock()
	secrets, ok := substrateExecSecrets[id]
	return secrets, ok
}

// isDigestPinned reports whether image is pinned by digest
// ([registry/]repository[:tag]@sha256:...), per substrate-runtime.md §3.
func isDigestPinned(image string) bool {
	return strings.Contains(image, "@sha256:")
}

// substrateAtespaceNameHashLen is the number of hex characters of the full
// project ID's SHA-256 digest substrateAtespaceName keeps. 32 hex chars is
// 128 bits — collision-resistant at any realistic project-count scale —
// while leaving "scion-" (6 chars) plus the hash comfortably inside the
// 63-character Kubernetes short-name limit (ResourceMetadata.atespace's
// k8s-short-name format).
const substrateAtespaceNameHashLen = 32

// substrateAtespaceName computes "scion-<32 hex chars of sha256(projectID)>".
// Hashing the FULL project ID (not truncating it directly) means two
// project IDs that merely share a prefix — or differ only in case, or in
// characters sanitizeK8sShortNameFragment would otherwise collapse to the
// same '-' — produce different atespaces instead of colliding onto one.
// Colliding here is more than a naming nit: two projects sharing an
// atespace share whatever isolation the atespace boundary provides, and
// RecordlessActors (and the record-less-actor 409 path it feeds) would
// start reporting one project's actor count and identity-unknown state to
// the other. The result is already a valid k8s-short-name (lowercase hex
// plus the fixed "scion-" prefix), so no further sanitization is needed.
//
// See Run's empty-ProjectID rejection: this function assumes projectID is
// non-empty, since an empty ID would otherwise hash to one fixed atespace
// shared by every such caller.
func substrateAtespaceName(projectID string) string {
	sum := sha256.Sum256([]byte(projectID))
	return "scion-" + hex.EncodeToString(sum[:])[:substrateAtespaceNameHashLen]
}

// SubstrateAtespaceNameForTest exposes substrateAtespaceName to other
// packages' tests (e.g. pkg/runtimebroker's substrate restart/lookup
// fixtures) that need to compute the exact atespace name a given project ID
// hashes to, rather than hardcoding a value that would silently drift from
// substrateAtespaceName's own algorithm. A pure function of its input with
// no side effects, so — unlike WipeSubstrateAgentStateForTest or
// SetExecResolveForTest — this carries no production hazard if ever called
// outside a test; exported directly rather than behind a call-site guard.
func SubstrateAtespaceNameForTest(projectID string) string {
	return substrateAtespaceName(projectID)
}

// ProjectSetForTest returns the ProjectSet the runtime broker installed
// (nil if none), so pkg/runtimebroker's tests can assert that every
// runtime entry path installs one. A read with no side effects, like
// SubstrateAtespaceNameForTest.
func (r *SubstrateRuntime) ProjectSetForTest() ProjectSet {
	return r.currentProjectSet()
}

// splitSubstrateID splits a runtime ID of the form "<atespace>/<actor>",
// the format Run returns (substrate-runtime.md §4).
func splitSubstrateID(id string) (atespace, actorName string, err error) {
	atespace, actorName, ok := strings.Cut(id, "/")
	if !ok || atespace == "" || actorName == "" {
		return "", "", fmt.Errorf("substrate: invalid actor id %q, want \"<atespace>/<actor>\"", id)
	}
	return atespace, actorName, nil
}

// buildSubstrateStartCmd builds the tmux start command exactly as the
// shared helper (common.go) builds it for every other runtime, through
// that same shared helper rather than a duplicated implementation.
// Substrate's control server execs this the same way Cloud Run/-sandbox's
// PID 1 does: no TTY, so it polls the tmux session's liveness rather than
// attaching.
func buildSubstrateStartCmd(cfg RunConfig) (string, error) {
	cmdLine, ok := harnessCmdLine(cfg, harnessCmdLineOptions{})
	if !ok {
		return "", fmt.Errorf("substrate: no harness provided")
	}
	agentWindowCmd := tmuxAgentWindowCmd("/bin/sh", cmdLine)
	return buildTmuxStartCmd(agentWindowCmd, tmuxPollSession), nil
}
