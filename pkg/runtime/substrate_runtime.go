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
// See /scion-volumes/scratchpad/projects/substrate-integration/{findings.md,
// phase1-spec.md} for the design this Phase 1 slice implements.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/k8s"
	"github.com/GoogleCloudPlatform/scion/pkg/projectcompat"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Phase 1 bounds not exposed as settings (only TemplateReadyTimeout is
// configurable — phase1-spec.md §2.3).
const (
	defaultActorRunningTimeout = 5 * time.Minute
	actorRunningPollInterval   = 2 * time.Second
	defaultHealthzTimeout      = 5 * time.Minute
	defaultExecTimeout         = 60 * time.Second
	substrateLogTailLines      = 2000
)

// substrateAgentRecord holds the fields List needs that ListActors cannot
// return (Substrate actors carry no labels — findings.md §3), synthesised
// from the broker's own record of what it passed to Run. Phase 1 accepts
// that a broker restart loses this for actors it did not create in this
// process lifetime (phase1-spec.md §2.2 List row); Phase 2 adds the
// ConfigMap-backed store.
type substrateAgentRecord struct {
	Labels        map[string]string
	Template      string
	HarnessConfig string
	Project       string
	ProjectID     string
	Image         string
}

// SubstrateRuntime implements runtime.Runtime for Agent Substrate.
type SubstrateRuntime struct {
	cfg       config.V1SubstrateConfig
	client    ateapipb.ControlClient
	conn      *grpc.ClientConn // non-nil only when this runtime opened it (nil for injected test clients)
	router    *substrate.RouterClient
	k8sClient kubernetes.Interface // for PodLogs (GetLogs) and the dialer's own TokenRequest calls

	now   func() time.Time
	sleep func(time.Duration)

	// healthzTimeout bounds how long Run waits for the control server to
	// report awaiting-bootstrap. A struct field (rather than always using
	// defaultHealthzTimeout directly) so tests can shrink it and exercise
	// the timeout path in well under defaultHealthzTimeout's 5 minutes.
	healthzTimeout time.Duration
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
// config — the same failure class as the per-instance gRPC connection
// memoization bug below (there, triggered by every `start`; here, by a
// config change), fixed the same way: moving this state to the one thing
// every instance shares, the process.
var (
	substrateAgentStateMu sync.Mutex
	// substrateControlTokens maps "<atespace>/<actor>" to the control_token
	// minted at bootstrap (phase1-spec.md §2.2 step 8: "keep it in memory
	// keyed by <atespace>/<actor>").
	substrateControlTokens = make(map[string]string)
	// substrateAgentRecords maps actor UID to the label/metadata record
	// synthesised at Run (phase1-spec.md §2.2 List row: "keyed by actor
	// uid").
	substrateAgentRecords = make(map[string]*substrateAgentRecord)
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
// not its default one (findings.md/phase1-spec.md never made it the
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
// substrate (phase1-spec.md §2.3): callers only reach this constructor when
// a profile explicitly selects it.
func NewSubstrateRuntime(sc *config.V1SubstrateConfig) (*SubstrateRuntime, error) {
	if sc == nil {
		sc = &config.V1SubstrateConfig{}
	}
	if sc.APIEndpoint == "" {
		return nil, fmt.Errorf("substrate: runtimes.<name>.substrate.api_endpoint is required")
	}
	if sc.RouterEndpoint == "" {
		return nil, fmt.Errorf("substrate: runtimes.<name>.substrate.router_endpoint is required")
	}
	if err := sc.Validate(); err != nil {
		return nil, err
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
// NewSubstrateRuntime, exactly as pkg/agent.ResolveRuntime/GetRuntime do in
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
		healthzTimeout: defaultHealthzTimeout,
	}, nil
}

// NewSubstrateRuntimeForTest builds a SubstrateRuntime with injected
// dependencies and no real network/cluster access, for unit tests. Exported
// so tests in other packages (e.g. pkg/agent, pkg/runtimebroker) can drive a
// real SubstrateRuntime — List/Delete/Run's actual logic, not a
// reimplementation of it — through a fake ateapipb.ControlClient and
// substrate.RouterClient, the same way this package's own tests do.
func NewSubstrateRuntimeForTest(client ateapipb.ControlClient, router *substrate.RouterClient, k8sClient kubernetes.Interface, cfg config.V1SubstrateConfig) *SubstrateRuntime {
	return &SubstrateRuntime{
		cfg:            cfg,
		client:         client,
		router:         router,
		k8sClient:      k8sClient,
		now:            time.Now,
		sleep:          func(time.Duration) {},
		healthzTimeout: defaultHealthzTimeout,
	}
}

func (r *SubstrateRuntime) Name() string { return "substrate" }

// ExecUser returns "scion" — the tmux session runs under the scion user
// after sciontool init sets up the environment, same as every other
// runtime.
func (r *SubstrateRuntime) ExecUser() string { return "scion" }

// Run implements the 9 steps of phase1-spec.md §2.2. Any failure after
// CreateActor triggers best-effort cleanup (delete the actor and its
// egress policy) before returning.
func (r *SubstrateRuntime) Run(ctx context.Context, cfg RunConfig) (string, error) {
	// Fail fast on a misconfigured egress_allow before touching the control
	// plane at all. NewSubstrateRuntime already validates this at
	// construction time; this is a defensive re-check in case a
	// SubstrateRuntime was ever built by another path (e.g. tests) that
	// skipped it.
	if err := r.cfg.Validate(); err != nil {
		return "", err
	}

	atespace := substrateAtespaceName(cfg.ProjectID)
	actorName := cfg.Name
	id := atespace + "/" + actorName

	// Step 1: atespace.
	if err := r.ensureAtespace(ctx, atespace); err != nil {
		return "", r.redact(cfg, err)
	}

	// Step 2: image must be digest-pinned. Tag resolution is Phase 2.
	if !isDigestPinned(cfg.Image) {
		return "", fmt.Errorf("substrate: image %q is not pinned by digest (@sha256:...); pin the harness image in the substrate profile (tag resolution is a Phase 2 feature)", cfg.Image)
	}

	// Step 3: content-addressed template, created + waited-ready if new.
	templateName := substrateTemplateName(cfg.Image, r.cfg, cfg.Resources)
	tmpl := buildActorTemplate(atespace, templateName, cfg.Image, r.cfg, cfg.Resources)
	if err := ensureActorTemplate(ctx, r.client, atespace, templateName, tmpl, templateReadyTimeout(r.cfg), r.sleep); err != nil {
		return "", r.redact(cfg, err)
	}

	// Step 4: create the actor.
	actor, err := r.client.CreateActor(ctx, &ateapipb.CreateActorRequest{
		Actor: &ateapipb.Actor{
			Metadata:      &ateapipb.ResourceMetadata{Atespace: atespace, Name: actorName},
			ActorTemplate: &ateapipb.ObjectRef{Atespace: atespace, Name: templateName},
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
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = r.client.DeleteActorEgressPolicy(cleanupCtx, &ateapipb.DeleteActorEgressPolicyRequest{
			Actor: &ateapipb.ObjectRef{Atespace: atespace, Name: actorName},
		})
		_, _ = r.client.DeleteActor(cleanupCtx, &ateapipb.DeleteActorRequest{
			Actor:    &ateapipb.ObjectRef{Atespace: atespace, Name: actorName},
			AnyState: true,
		})
	}

	// Step 5: egress policy. (r.cfg was already validated at the top of
	// Run; r.cfg is immutable for the lifetime of this call, so revalidating
	// it here would only ever re-check the same result.)
	env := buildBootstrapEnv(cfg)
	hostnames := substrateEgressHostnames(cfg, env, r.cfg)
	if _, err := r.client.CreateActorEgressPolicy(ctx, buildEgressPolicy(atespace, actorName, hostnames)); err != nil {
		cleanup()
		return "", r.redact(cfg, fmt.Errorf("substrate: create egress policy for %s: %w", id, err))
	}

	// Step 6: resume, then wait RUNNING.
	if _, err := r.client.ResumeActor(ctx, &ateapipb.ResumeActorRequest{
		Actor: &ateapipb.ObjectRef{Atespace: atespace, Name: actorName},
	}); err != nil {
		cleanup()
		return "", r.redact(cfg, fmt.Errorf("substrate: resume actor %s: %w", id, err))
	}
	if err := r.waitRunning(ctx, atespace, actorName); err != nil {
		cleanup()
		return "", r.redact(cfg, err)
	}

	// Step 7: wait for the control server to be awaiting bootstrap.
	if err := waitForHealthz(ctx, r.router, atespace, actorName, healthzAwaitingBootstrap, r.healthzTimeout, r.sleep); err != nil {
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
	}); err != nil {
		if errors.Is(err, errBootstrapHijacked) {
			// Treat as a compromise indicator, not a retry: delete the
			// actor and its egress policy so nothing keeps running under
			// config it never should have received, and fail loudly. See
			// errBootstrapHijacked's doc comment for the threat model.
			runtimeLog.Error("substrate: bootstrap hijack detected, deleting actor as a precaution",
				"atespace", atespace, "actor", actorName)
			cleanup()
			return "", fmt.Errorf("substrate: actor %s was bootstrapped by another caller before this broker's request reached it; deleted as a precaution", id)
		}
		cleanup()
		return "", r.redact(cfg, err)
	}

	substrateAgentStateMu.Lock()
	substrateControlTokens[id] = controlToken
	substrateAgentRecords[actorUID] = &substrateAgentRecord{
		Labels: cfg.Labels,
		// scion.harness_config is the same label key
		// CloudRunSandboxRuntime.Run populates HarnessConfig from — see
		// labelValue and its use in cloudrun_sandbox_runtime.go.
		HarnessConfig: labelValue(cfg.Labels, "scion.harness_config"),
		Template:      cfg.Template,
		Project:       cfg.Project,
		ProjectID:     cfg.ProjectID,
		Image:         cfg.Image,
	}
	substrateAgentStateMu.Unlock()

	// Step 9.
	return id, nil
}

// bootstrapNonce is the single call site for the bootstrap request's bearer
// value (brief instruction: "structure the code so the nonce source is one
// function"). phase1-spec.md §5 leaves open a choice between MintActorJWT
// (verifiable actor identity via a systemInfo volume) and a fallback
// (first-bootstrap-wins, secured by a NetworkPolicy restricting router
// ingress to the broker namespace), pending further investigation into
// whether MintActorJWT is actually usable here.
//
// This is the fallback. It could not confirm MintActorJWT's alternative is
// even possible from ateapi.proto alone: a systemInfo TrustBundleDataSource
// projects a named, "allowlisted in atelet" trust bundle into the actor,
// and whether one of those allowlisted names carries what's needed to
// verify a substrate-issued actor JWT is opaque outside atelet's
// implementation. Do not switch this to MintActorJWT without confirming
// that decision — see the project log for the open question this leaves.
func (r *SubstrateRuntime) bootstrapNonce(ctx context.Context, atespace, actorName, actorUID string) (string, error) {
	return generateControlToken()
}

// Delete implements phase1-spec.md §2.2 Delete row: DeleteActorEgressPolicy
// (ignoring NotFound), then DeleteActor(any_state=true), then drop the
// in-memory control token (and label record, best effort).
func (r *SubstrateRuntime) Delete(ctx context.Context, id string) error {
	atespace, actorName, err := splitSubstrateID(id)
	if err != nil {
		return err
	}

	var uid string
	if actor, gerr := r.client.GetActor(ctx, &ateapipb.GetActorRequest{Actor: &ateapipb.ObjectRef{Atespace: atespace, Name: actorName}}); gerr == nil {
		uid = actor.GetMetadata().GetUid()
	}

	if _, err := r.client.DeleteActorEgressPolicy(ctx, &ateapipb.DeleteActorEgressPolicyRequest{
		Actor: &ateapipb.ObjectRef{Atespace: atespace, Name: actorName},
	}); err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("substrate: delete egress policy for %s: %w", id, err)
	}

	if _, err := r.client.DeleteActor(ctx, &ateapipb.DeleteActorRequest{
		Actor:    &ateapipb.ObjectRef{Atespace: atespace, Name: actorName},
		AnyState: true,
	}); err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("substrate: delete actor %s: %w", id, err)
	}

	substrateAgentStateMu.Lock()
	delete(substrateControlTokens, id)
	if uid != "" {
		delete(substrateAgentRecords, uid)
	}
	substrateAgentStateMu.Unlock()
	return nil
}

// Stop is the same as Delete in Phase 1: nothing here suspends to a DATA
// snapshot yet, and faking a "stopped" state that just left the actor
// running (or claiming a durable stop that instead discarded the actor's
// state) would both be dishonest about what happened. See findings.md §4.6
// and phase1-spec.md §2.2 Stop row.
//
// TODO(Phase 2): SuspendActor with DATA scope instead, once resume-from-
// suspend and the $HOME durableDir layout (findings.md Q5) land, so Stop
// keeps the workspace and frees the worker rather than deleting the actor.
func (r *SubstrateRuntime) Stop(ctx context.Context, id string) error {
	return r.Delete(ctx, id)
}

// substrateAtespacePrefix is the naming convention substrateAtespaceName
// produces. List uses it to skip actors outside any scion-managed
// atespace: ListActors with an empty atespace (List has no project context
// to scope it to — the spec's literal "ListActors(atespace)" can't be
// applied here) lists across the whole cluster, which may host other
// tenants sharing the same Substrate install.
const substrateAtespacePrefix = "scion-"

// List implements phase1-spec.md §2.2 List row.
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
// This is deliberate, not an oversight: an earlier version of this
// function tried to recover the agent slug from the actor name (inverting
// containerName) so a record-less actor could still be found by slug.
// That construction could not be made to fail closed against every
// project-scoped caller (a lookup scoped to project B could still resolve
// to project A's sole record-less actor for the same slug, since nothing
// here could verify which project a record-less actor actually belonged
// to strongly enough for every caller). Rather than accept that risk, this
// reports record-less actors exactly as before any such recovery existed:
// never resolvable by slug, so a wrong-actor action is structurally
// impossible, at the cost of requiring an operator to re-identify (or
// simply restart) a record-less actor by hand. ptone/scion#1819 tracks
// hardening the generic slug-matching call path in pkg/agent; the durable
// fix here is persisting agent records so they survive a broker restart in
// the first place (Phase 2), not reconstructing them from the actor name.
func (r *SubstrateRuntime) List(ctx context.Context, labelFilter map[string]string) ([]api.AgentInfo, error) {
	var actors []*ateapipb.Actor
	pageToken := ""
	for {
		resp, err := r.client.ListActors(ctx, &ateapipb.ListActorsRequest{PageToken: pageToken})
		if err != nil {
			return nil, fmt.Errorf("substrate: list actors: %w", err)
		}
		actors = append(actors, resp.GetActors()...)
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}

	substrateAgentStateMu.Lock()
	defer substrateAgentStateMu.Unlock()

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
		var template, harnessConfig, project, projectID, image string
		if rec != nil {
			for k, v := range rec.Labels {
				labels[k] = v
			}
			template = rec.Template
			harnessConfig = rec.HarnessConfig
			project = rec.Project
			projectID = rec.ProjectID
			image = rec.Image
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
			// (ptone/scion#1819 tracks hardening that call path in
			// general; this is the substrate-specific root cause). This
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
			Image:         image,
		})
	}
	return agents, nil
}

// substrateLabelsMatch mirrors the label-filter pattern used by the other
// runtimes (e.g. CloudRunSandboxRuntime.List): an entry with no explicit
// label for a filtered project/project-id key still matches on the
// synthesised project/projectID fields.
func substrateLabelsMatch(labels map[string]string, project, projectID string, filter map[string]string) bool {
	for k, v := range filter {
		actual := labels[k]
		if actual == "" {
			switch k {
			case projectcompat.LabelProject, projectcompat.LabelGrove:
				actual = project
			case projectcompat.LabelProjectID, projectcompat.LabelGroveID:
				actual = projectID
			}
		}
		if actual != v {
			return false
		}
	}
	return true
}

// substratePhase maps ateapipb.ActorState onto scion's AgentInfo.Phase
// vocabulary (findings.md §4.6).
func substratePhase(s ateapipb.ActorState) string {
	switch s {
	case ateapipb.ActorState_ACTOR_STATE_RESUMING:
		return "provisioning"
	case ateapipb.ActorState_ACTOR_STATE_RUNNING:
		return "running"
	case ateapipb.ActorState_ACTOR_STATE_SUSPENDING, ateapipb.ActorState_ACTOR_STATE_PAUSING:
		return "stopping"
	case ateapipb.ActorState_ACTOR_STATE_SUSPENDED, ateapipb.ActorState_ACTOR_STATE_PAUSED:
		return "stopped"
	case ateapipb.ActorState_ACTOR_STATE_CRASHED:
		return "error"
	case ateapipb.ActorState_ACTOR_STATE_DELETING:
		return "stopping"
	case ateapipb.ActorState_ACTOR_STATE_REVERTING:
		return "provisioning"
	default:
		return "unknown"
	}
}

// GetLogs implements phase1-spec.md §2.2 GetLogs row: GetActor →
// status.worker_assignment → client-go PodLogs (tail 2000 lines).
//
// API-shape note (validation question 8): the spec described this as
// "GetActor → status.worker → GetWorker → pod name/namespace", but
// ActorStatus.worker_assignment already carries worker_pod and
// worker_namespace directly (a deliberate denormalization — see the proto
// comment on WorkerAssignment: "readers on a hot path do not have to fetch
// the Worker at all"). No separate GetWorker call is needed or made.
func (r *SubstrateRuntime) GetLogs(ctx context.Context, id string) (string, error) {
	atespace, actorName, err := splitSubstrateID(id)
	if err != nil {
		return "", err
	}

	actor, err := r.client.GetActor(ctx, &ateapipb.GetActorRequest{Actor: &ateapipb.ObjectRef{Atespace: atespace, Name: actorName}})
	if err != nil {
		return "", fmt.Errorf("substrate: get actor %s: %w", id, err)
	}

	wa := actor.GetStatus().GetWorkerAssignment()
	if wa == nil || wa.GetWorkerPod() == "" {
		return "", fmt.Errorf("substrate: actor %s has no assigned worker (state: %s)", id, actor.GetStatus().GetState())
	}
	if r.k8sClient == nil {
		return "", fmt.Errorf("substrate: no Kubernetes client configured for pod logs")
	}

	tailLines := int64(substrateLogTailLines)
	req := r.k8sClient.CoreV1().Pods(wa.GetWorkerNamespace()).GetLogs(wa.GetWorkerPod(), &corev1.PodLogOptions{TailLines: &tailLines})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("substrate: stream logs for %s (pod %s/%s): %w", id, wa.GetWorkerNamespace(), wa.GetWorkerPod(), err)
	}
	defer func() { _ = stream.Close() }()

	data, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("substrate: read logs for %s: %w", id, err)
	}
	return string(data), nil
}

// Exec implements phase1-spec.md §2.2 Exec row: POST /scion/v1/exec via the
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

	res, err := doExec(ctx, r.router, atespace, actorName, token, cmd, r.ExecUser(), defaultExecTimeout)
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}

// Attach is not supported in Phase 1 (findings.md §3, phase1-spec.md §2.2).
// The broker's PTY switch (pty_handlers.go) returns a clean error before
// reaching this method; it exists to satisfy the Runtime interface and as
// a defensive fallback.
func (r *SubstrateRuntime) Attach(ctx context.Context, id string) error {
	return fmt.Errorf("substrate: attach not yet supported on substrate (Phase 2)")
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

// ensureAtespace creates atespace, treating AlreadyExists as success
// (phase1-spec.md §2.2 step 1).
func (r *SubstrateRuntime) ensureAtespace(ctx context.Context, atespace string) error {
	_, err := r.client.CreateAtespace(ctx, &ateapipb.CreateAtespaceRequest{
		Atespace: &ateapipb.Atespace{Metadata: &ateapipb.ResourceMetadata{Name: atespace}},
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("substrate: create atespace %q: %w", atespace, err)
	}
	return nil
}

// waitRunning polls GetActor until state is RUNNING, or defaultActorRunningTimeout
// elapses. A CRASHED state fails fast rather than waiting out the timeout.
func (r *SubstrateRuntime) waitRunning(ctx context.Context, atespace, actorName string) error {
	deadline := r.now().Add(defaultActorRunningTimeout)
	for {
		actor, err := r.client.GetActor(ctx, &ateapipb.GetActorRequest{Actor: &ateapipb.ObjectRef{Atespace: atespace, Name: actorName}})
		if err != nil {
			return fmt.Errorf("substrate: get actor %s/%s: %w", atespace, actorName, err)
		}
		state := actor.GetStatus().GetState()
		if state == ateapipb.ActorState_ACTOR_STATE_RUNNING {
			return nil
		}
		if state == ateapipb.ActorState_ACTOR_STATE_CRASHED {
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
// logs or an HTTP response — see #1342 / TestCloudRunSandboxRun_
// ErrorDoesNotLeakEnvValues, which this mirrors).
func (r *SubstrateRuntime) redact(cfg RunConfig, err error) error {
	if err == nil {
		return nil
	}
	return errors.New(redactEnvValues(err.Error(), substrateSecretCandidates(cfg)))
}

// isDigestPinned reports whether image is pinned by digest
// ([registry/]repository[:tag]@sha256:...), per phase1-spec.md §2.2 step 2.
func isDigestPinned(image string) bool {
	return strings.Contains(image, "@sha256:")
}

// substrateAtespaceName computes "scion-<first 12 chars of projectID>"
// (phase1-spec.md §2.2 step 1), sanitised to a valid Kubernetes short name
// (ResourceMetadata.atespace's k8s-short-name format: lowercase alphanumeric
// and '-', starting and ending with an alphanumeric character). A UUID
// project ID is already valid as-is; this defends against any other project
// ID shape (uppercase letters, underscores, a trailing '-' from truncating
// mid-segment) producing an invalid atespace name.
func substrateAtespaceName(projectID string) string {
	s := projectID
	if len(s) > 12 {
		s = s[:12]
	}
	return "scion-" + sanitizeK8sShortNameFragment(s)
}

// sanitizeK8sShortNameFragment lowercases s and replaces every character
// that isn't a lowercase letter, digit, or '-' with '-', then trims leading
// and trailing '-' (a k8s-short-name segment must start and end with an
// alphanumeric character). An all-invalid or empty input becomes "x" so the
// result is never empty.
func sanitizeK8sShortNameFragment(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	trimmed := strings.Trim(b.String(), "-")
	if trimmed == "" {
		return "x"
	}
	return trimmed
}

// splitSubstrateID splits a runtime ID of the form "<atespace>/<actor>",
// the format Run returns (phase1-spec.md §2.2 step 9).
func splitSubstrateID(id string) (atespace, actorName string, err error) {
	atespace, actorName, ok := strings.Cut(id, "/")
	if !ok || atespace == "" || actorName == "" {
		return "", "", fmt.Errorf("substrate: invalid actor id %q, want \"<atespace>/<actor>\"", id)
	}
	return atespace, actorName, nil
}

// buildSubstrateStartCmd builds the tmux start command exactly as the
// shared helper (common.go) builds it for every other runtime — see the
// brief's shared start-cmd helper extraction. Substrate's control server
// execs this the same way Cloud Run/-sandbox's PID 1 does: no TTY, so it
// polls the tmux session's liveness rather than attaching.
func buildSubstrateStartCmd(cfg RunConfig) (string, error) {
	cmdLine, ok := harnessCmdLine(cfg)
	if !ok {
		return "", fmt.Errorf("substrate: no harness provided")
	}
	agentWindowCmd := tmuxAgentWindowCmd("/bin/sh", cmdLine)
	return buildTmuxStartCmd(agentWindowCmd, tmuxPollSession), nil
}
