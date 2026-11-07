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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/substratecaps"
	ateapi "github.com/GoogleCloudPlatform/scion/third_party/substrate/ateapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/resource"
)

// substrateServeEntrypointVersion is folded into the template's
// content-address (substrate-runtime.md §3) so a change to the
// `sciontool substrate-serve` protocol forces a new template rather than
// silently reusing a golden snapshot built against the old one. Bumped to
// v2 for the absolute-Command hardening below: Command itself is not part
// of the hash input list (it's a Go literal, not a value derived from
// config that could otherwise vary), so this version bump is the only
// thing that forces existing golden snapshots — built with the old, bare
// "sciontool" entrypoint baked in — to be rebuilt rather than reused as-is.
const substrateServeEntrypointVersion = "substrate-serve/v2"

// substrateServePort is the TCP port substrate-serve listens on inside the
// actor: the --addr flag's default (":80") in
// cmd/sciontool/commands/substrate_serve.go. buildActorTemplate's Command
// passes no --addr, so the control server always binds this port, which is
// also the port the inbound router targets by default.
const substrateServePort = 80

// substrateWakeupProbeTimeoutSeconds is how long Substrate keeps polling the
// wakeup probe before it fails the wakeup and moves the actor to
// ACTOR_STATE_CRASHED. Substrate's own default is 30s. 120s is deliberately
// conservative:
//
//   - On a cold boot (the golden actor, or any actor started without a
//     snapshot), substrate-serve runs the rootfs fixup and the self-binary
//     integrity check synchronously before it starts listening (see
//     runSubstrateServe). On a slow or busy worker that, plus process start,
//     can approach 30s, and a false failure there fails the whole template.
//   - On a restore, the listener is part of the checkpointed memory, so the
//     first poll normally answers and this bound is never approached.
//   - A broken image (for example a sciontool without substrate-serve, which
//     exits at once) still fails within two minutes: inside
//     defaultTemplateReadyTimeout (10m) for the golden actor, and inside
//     defaultHealthzTimeout (5m), the client-side wait that runs after
//     ResumeActor returns.
const substrateWakeupProbeTimeoutSeconds = 120

// substrateWakeupProbe returns the wakeup probe for the agent container: GET
// substrateHealthzPath on substrateServePort. healthz needs no auth and
// always answers 200 once the control server is up, whatever lifecycle state
// it reports (pkg/sciontool/substrate's HealthzResponse), so the probe
// passes on both a fresh golden boot ("awaiting-bootstrap") and a resumed,
// bootstrapped actor ("running" or "init-failed"). It gates each wakeup on
// substrate-serve actually serving: the probe is polled on every wakeup,
// including a cold boot, and a miss within the timeout crashes the actor
// (ateapi.proto, WakeupProbe). The golden actor's cold boot is
// gated the same way, so an image whose entrypoint exits fails template
// creation rather than producing a golden that cannot be restored.
// Substrate's API guide further states that when every container declares
// a probe, the golden snapshot is taken once the probe has answered,
// instead of after a fixed warm-up delay.
//
// A fresh value on every call, so no caller can mutate a shared probe and
// change what substrateTemplateName hashes without changing its input.
func substrateWakeupProbe() *ateapi.WakeupProbe {
	return &ateapi.WakeupProbe{
		HttpGet: &ateapi.HttpGetAction{
			Path: substrateHealthzPath,
			Port: substrateServePort,
		},
		TimeoutSeconds: substrateWakeupProbeTimeoutSeconds,
	}
}

// substrateWakeupProbeCacheKey renders the wakeup probe into a stable string
// for substrateTemplateName's hash input, from the same value
// buildActorTemplate sets, so a change to the probe's path, port or timeout
// always produces a new template name.
func substrateWakeupProbeCacheKey() string {
	p := substrateWakeupProbe()
	return fmt.Sprintf("wakeup(path=%s,port=%d,timeout=%d)",
		p.GetHttpGet().GetPath(), p.GetHttpGet().GetPort(), p.GetTimeoutSeconds())
}

// substrateContainerCapabilitiesAdd lists the Linux capabilities
// buildActorTemplate grants on top of Substrate's default set. It is
// substratecaps.Names() — the single source of truth shared with
// checkPrivilegeDropFeasible (cmd/sciontool/commands), so the template and
// the /bootstrap precondition that verifies it can never drift apart — see
// package substratecaps's doc comment for why a leaf package, and
// substratecaps.Required for what each capability is for and the evidence
// behind it. This is also the hash input for substrateTemplateName
// (below), so any future change here also changes the template's
// content-address, forcing a new golden template instead of silently
// reusing one built without the capabilities a code change just added or
// removed.
var substrateContainerCapabilitiesAdd = substratecaps.Names()

// defaultTemplateReadyTimeout bounds how long Run waits for a newly created
// ActorTemplate's golden snapshot to become ready, when
// V1SubstrateConfig.TemplateReadyTimeout is unset.
const defaultTemplateReadyTimeout = 10 * time.Minute

// templateReadyPollInterval is how often Run re-polls GetActorTemplate
// while waiting for the golden snapshot.
const templateReadyPollInterval = 5 * time.Second

// substrateTemplateName computes the content-addressed ActorTemplate name
// (substrate-runtime.md §3). Same inputs always produce the same name,
// so concurrent Runs for the same effective template converge on one
// CreateActorTemplate instead of racing to create distinct ones.
//
// The hash covers an explicit, ordered list of every input that changes
// what buildActorTemplate produces, behind a "v1|" prefix (never a proto
// serialisation, whose bytes are not a stable contract):
//
//  1. the image digest;
//  2. the worker selector (sorted key=value);
//  3. the resolved cpu_millicores and 4. memory_bytes (after the per-field
//     defaulting in resolveSubstrateResources, so a change to a default
//     never silently reuses a template built at the old size);
//  5. the added capabilities (sorted);
//  6. the substrate-serve entrypoint version;
//  7. the wakeup probe's path, port and timeout;
//  8. the trust bundle names (sorted; always empty in Phase 1, which
//     projects none);
//  9. the volume names and kinds.
//
// All of these are template content, so changing any of them in settings,
// or in this runtime's own code, must not silently reuse a stale golden
// template.
func substrateTemplateName(imageDigest string, sc config.V1SubstrateConfig, res substrateResources) string {
	caps := slices.Clone(substrateContainerCapabilitiesAdd)
	sort.Strings(caps)
	return substrateTemplateHashInput{
		image:             imageDigest,
		workerSelector:    workerSelectorCacheKey(sc.WorkerSelector),
		cpuMillicores:     res.cpuMillicores,
		memoryBytes:       res.memoryBytes,
		caps:              strings.Join(caps, ","),
		entrypointVersion: substrateServeEntrypointVersion,
		probe:             substrateWakeupProbeCacheKey(),
		trustBundleNames:  "", // no trust bundle is projected in Phase 1 (see substrateTemplateVolumes)
		volumes:           substrateVolumesCacheKey(substrateTemplateVolumes(sc)),
	}.name()
}

// substrateTemplateHashInput is substrateTemplateName's explicit, ordered
// hash field list, each field already rendered to a stable string.
type substrateTemplateHashInput struct {
	image             string
	workerSelector    string
	cpuMillicores     int64
	memoryBytes       int64
	caps              string
	entrypointVersion string
	probe             string
	trustBundleNames  string
	volumes           string
}

// name hashes the fields in order behind the "v1|" prefix.
func (in substrateTemplateHashInput) name() string {
	h := sha256.New()
	// hash.Hash.Write never returns an error (see the hash.Hash doc
	// comment), so the error from Fprintf is deliberately discarded rather
	// than checked.
	_, _ = fmt.Fprintf(h, "v1|%s|%s|%d|%d|%s|%s|%s|%s|%s",
		in.image,
		in.workerSelector,
		in.cpuMillicores,
		in.memoryBytes,
		in.caps,
		in.entrypointVersion,
		in.probe,
		in.trustBundleNames,
		in.volumes,
	)
	sum := hex.EncodeToString(h.Sum(nil))
	return "scion-" + sum[:12]
}

// workerSelectorCacheKey renders a worker-selector match-labels map into a
// stable string for substrateTemplateName's hash input: sorted by key, so
// map iteration order never changes the hash.
func workerSelectorCacheKey(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
	}
	return b.String()
}

// substrateVolumesCacheKey renders volume names and kinds into a stable
// string for substrateTemplateName's hash input, sorted by name.
func substrateVolumesCacheKey(vols []*ateapi.Volume) string {
	keys := make([]string, 0, len(vols))
	for _, v := range vols {
		kind := "unknown"
		switch {
		case v.GetDurableDir() != nil:
			kind = "durable_dir"
		case v.GetSystemInfo() != nil:
			kind = "system_info"
		case v.GetExternal() != nil:
			kind = "external"
		case v.GetImage() != nil:
			kind = "image"
		}
		keys = append(keys, v.GetName()+":"+kind)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// Built-in substrate resource defaults, used for a field the agent's
// resolved resources leave unset. v1 requires both a cpu and a memory limit
// on every ActorTemplate.
//
// The memory default deliberately reverses scion's choice to leave memory
// unset (config.BuiltinDefaultResources, pkg/config/resource_defaults.go:
// a hard memory limit OOM-kills the largest process, often the harness).
// v1 requires a memory limit and boots a micro-VM of that size, so substrate
// agents always get one; 4Gi is sized for a harness plus its tools.
const (
	substrateBuiltinDefaultCPU    = "2"
	substrateBuiltinDefaultMemory = "4Gi"
)

// substrateResources is the template-level compute size: both fields are
// always positive once resolveSubstrateResources returns without error.
type substrateResources struct {
	cpuMillicores int64
	memoryBytes   int64
}

// resolveSubstrateResources resolves cpu and memory independently: the
// agent's resolved limit when set, else the built-in substrate default.
// Phase 2 adds the operator's default_resources as a middle step in
// resolveSubstrateQuantity; nothing else changes.
func resolveSubstrateResources(agent *api.ResourceSpec) (substrateResources, error) {
	var agentCPU, agentMemory string
	if agent != nil {
		agentCPU, agentMemory = agent.Limits.CPU, agent.Limits.Memory
	}

	cpuQ, err := parseSubstrateQuantity("cpu", resolveSubstrateQuantity(agentCPU, substrateBuiltinDefaultCPU), resource.NewMilliQuantity(math.MaxInt64, resource.DecimalSI))
	if err != nil {
		return substrateResources{}, err
	}
	memQ, err := parseSubstrateQuantity("memory", resolveSubstrateQuantity(agentMemory, substrateBuiltinDefaultMemory), resource.NewQuantity(math.MaxInt64, resource.BinarySI))
	if err != nil {
		return substrateResources{}, err
	}
	res := substrateResources{cpuMillicores: cpuQ.MilliValue(), memoryBytes: memQ.Value()}
	if res.cpuMillicores <= 0 || res.memoryBytes <= 0 {
		return substrateResources{}, fmt.Errorf("substrate: resources must be positive (cpu %dm, memory %d bytes)", res.cpuMillicores, res.memoryBytes)
	}
	return res, nil
}

// resolveSubstrateQuantity picks one field's quantity string: the agent's
// value if set, else the built-in default. The operator default_resources
// step (Phase 2) goes between the two.
func resolveSubstrateQuantity(agentValue, builtinDefault string) string {
	if agentValue != "" {
		return agentValue
	}
	return builtinDefault
}

// parseSubstrateQuantity parses a Kubernetes quantity string and rejects
// non-positive values and values above limit (which would overflow the
// int64 the v1 API carries).
func parseSubstrateQuantity(field, value string, limit *resource.Quantity) (resource.Quantity, error) {
	q, err := resource.ParseQuantity(value)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("substrate: invalid %s limit %q: %w", field, value, err)
	}
	if q.Sign() <= 0 {
		return resource.Quantity{}, fmt.Errorf("substrate: %s limit %q must be positive", field, value)
	}
	// Compared as decimals: a value above the int64 range (such as 8Ei)
	// does not compare above an int64-sized Quantity otherwise.
	if q.AsDec().Cmp(limit.AsDec()) > 0 {
		return resource.Quantity{}, fmt.Errorf("substrate: %s limit %q is too large", field, value)
	}
	return q, nil
}

// substrateRemovedKeys lists the substrate settings keys the v1 API has no
// target for, with what replaces each. checkSubstrateRemovedKeys refuses a
// Run that sets any of them, so they are never silently ignored.
var substrateRemovedKeys = []struct {
	key         string
	value       func(config.V1SubstrateConfig) string
	replacement string
}{
	{"sandbox_class", func(sc config.V1SubstrateConfig) string { return sc.SandboxClass },
		"select the sandbox class with worker_selector, matching a WorkerPool whose spec.sandboxClasses lists it"},
	{"sandbox_config_name", func(sc config.V1SubstrateConfig) string { return sc.SandboxConfigName },
		"select the sandbox config with worker_selector, matching a WorkerPool whose spec.sandboxClasses references it"},
	{"snapshot_storage", func(sc config.V1SubstrateConfig) string { return sc.SnapshotStorage },
		"set the snapshot location server-wide with the ate-api-server --snapshot-location flag"},
}

// checkSubstrateEgressTrustBundle refuses a config that sets
// egress_trust_bundle: Phase 1 projects no trust bundle and emits no https
// (MITM) egress rules, so the setting would otherwise be silently ignored.
func checkSubstrateEgressTrustBundle(sc config.V1SubstrateConfig) error {
	if sc.EgressTrustBundle != "" {
		return fmt.Errorf("substrate: setting \"egress_trust_bundle\" is not supported yet: MITM egress (egress_trust_bundle) arrives in Phase 3; unset it in the substrate runtime settings for now")
	}
	return nil
}

// checkSubstrateRemovedKeys returns an error naming the first removed
// settings key sc sets, and its v1 replacement.
func checkSubstrateRemovedKeys(sc config.V1SubstrateConfig) error {
	for _, k := range substrateRemovedKeys {
		if k.value(sc) != "" {
			return fmt.Errorf("substrate: setting %q is not supported by the Substrate v1 API; remove it from the substrate runtime settings and %s", k.key, k.replacement)
		}
	}
	return nil
}

// substrateTemplateVolumes returns the template's volumes: the workspace
// durableDir. substrateTemplateName hashes the same value. Phase 1 does not
// project the egress trust bundle (a system-info volume); Phase 3 adds it
// back together with https (MITM) egress rules, and Run refuses
// egress_trust_bundle until then (checkSubstrateEgressTrustBundle).
func substrateTemplateVolumes(config.V1SubstrateConfig) []*ateapi.Volume {
	return []*ateapi.Volume{
		{Name: "workspace", DurableDir: &ateapi.DurableDirVolumeSource{}},
	}
}

// buildActorTemplate constructs the ActorTemplate for CreateActorTemplate
// (substrate-runtime.md §3). Env carries no secrets and no per-agent
// config, ever (substrate-runtime.md §3; that is pushed after the actor starts, via
// POST /scion/v1/bootstrap); this function sets no Env at all.
//
// Resources are set at template level only, where v1 requires both a cpu
// and a memory limit; the container carries none, so nothing is booked
// twice. A res missing either value is refused, so no template is ever
// built without both.
//
// snapshot_config is omitted: every field then takes its v1 default, and
// golden snapshots are enabled by default. The snapshot location is the
// ate-api-server's --snapshot-location.
func buildActorTemplate(atespace, templateName, imageDigest string, sc config.V1SubstrateConfig, res substrateResources) (*ateapi.ActorTemplate, error) {
	if res.cpuMillicores <= 0 || res.memoryBytes <= 0 {
		return nil, fmt.Errorf("substrate: actor template %s/%s needs both a cpu and a memory limit (cpu %dm, memory %d bytes)", atespace, templateName, res.cpuMillicores, res.memoryBytes)
	}

	tmpl := &ateapi.ActorTemplate{
		Metadata: &ateapi.ResourceMetadata{Atespace: atespace, Name: templateName},
		Containers: []*ateapi.Container{
			{
				Name:  "scion-agent",
				Image: imageDigest,
				// Absolute, never a bare name: this is PID 1's own launch
				// argv, resolved by whatever mechanism starts the
				// container's entrypoint — not something this codebase's
				// own rootexec.Resolve (fixed system directories, verified
				// by fd-walk) has any chance to guard, since it runs before
				// this binary's own code executes at all. A bare
				// "sciontool" would be looked up against the container's
				// PATH, which includes the workload-owned npm-global
				// directory ahead of any trusted system directory — see
				// the rootexec package doc comment for the general shape
				// of that hazard. /usr/local/bin/sciontool is where the
				// image installs it (image-build/scion-base/Dockerfile).
				Command: []string{"/usr/local/bin/sciontool", "substrate-serve"},
				// No Env: no secrets and no per-agent config, ever
				// (substrate-runtime.md §3).
				VolumeMounts: []*ateapi.VolumeMount{
					{Name: "workspace", MountPath: "/workspace"},
				},
				// Substrate always starts the actor process as UID 0 / GID 0
				// (ContainerSpec has no user field — agent-substrate/substrate
				// internal/ocispec/ocispec.go) with a minimal default
				// capability set (AUDIT_WRITE, KILL, NET_BIND_SERVICE —
				// cmd/atelet/oci.go) that does not include any of
				// substratecaps.Required. scion never runs the harness or
				// exec as root, and dropping from root to the scion user —
				// via the supervisor's own syscall.Credential drop
				// (pkg/sciontool/supervisor/supervisor.go's Run, ~lines
				// 113-150) and the exec control-plane endpoint's own
				// syscall.Credential drop (pkg/sciontool/substrate's
				// runExec, used for `sciontool substrate-serve exec`) — as
				// well as RunInit's own chowns of the log file and
				// workspace immediately after that drop, all need
				// capabilities this default set doesn't grant. See
				// substratecaps.Required for exactly which ones and the
				// evidence behind each — the same capabilities Docker's
				// default set already grants, which is why this only
				// surfaces on Substrate. These are added here, not assumed
				// from a container default, so they apply inside the gVisor
				// sentry the actor runs in; a direct credential drop grants
				// the dropped-to process none of them, so nothing
				// scion-owned ever runs privileged.
				SecurityContext: &ateapi.SecurityContext{
					Capabilities: &ateapi.Capabilities{
						// Clone, not the package-level slice itself: any
						// caller that mutated tmpl...Capabilities.Add in
						// place would otherwise corrupt
						// substrateContainerCapabilitiesAdd for every future
						// template built in this process, silently changing
						// what substrateTemplateName hashes without changing
						// the hash input's own value.
						Add: slices.Clone(substrateContainerCapabilitiesAdd),
					},
				},
				// Gates every wakeup, including the golden actor's first
				// boot, on substrate-serve answering healthz. See
				// substrateWakeupProbe for what it checks and
				// substrateWakeupProbeTimeoutSeconds for the timing.
				WakeupProbe: substrateWakeupProbe(),
			},
		},
		Volumes: substrateTemplateVolumes(sc),
		Resources: &ateapi.ResourceRequirements{
			Limits: &ateapi.Resources{CpuMillicores: res.cpuMillicores, MemoryBytes: res.memoryBytes},
		},
	}

	if len(sc.WorkerSelector) > 0 {
		tmpl.WorkerSelector = &ateapi.Selector{MatchLabels: sc.WorkerSelector}
	}

	return tmpl, nil
}

// templateReadyTimeout resolves V1SubstrateConfig.TemplateReadyTimeout,
// falling back to defaultTemplateReadyTimeout when unset or unparsable.
func templateReadyTimeout(sc config.V1SubstrateConfig) time.Duration {
	if sc.TemplateReadyTimeout == "" {
		return defaultTemplateReadyTimeout
	}
	d, err := time.ParseDuration(sc.TemplateReadyTimeout)
	if err != nil || d <= 0 {
		return defaultTemplateReadyTimeout
	}
	return d
}

// ensureActorTemplate gets the ActorTemplate named templateName in atespace,
// creating it if missing, then waits for its golden snapshot to become
// ready — creating a template boots a golden actor and snapshots it
// (substrate-runtime.md §3), so Run must not proceed to CreateActor until
// that snapshot is ready. clock lets tests replace time.Sleep with an
// instant no-op.
func ensureActorTemplate(ctx context.Context, client ateapi.ControlClient, atespace, templateName string, tmpl *ateapi.ActorTemplate, timeout time.Duration, sleep func(time.Duration)) error {
	existing, err := client.GetActorTemplate(ctx, &ateapi.GetActorTemplateRequest{
		ActorTemplate: &ateapi.ObjectRef{Atespace: atespace, Name: templateName},
	})
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return fmt.Errorf("substrate: get actor template %s/%s: %w", atespace, templateName, err)
		}
		created, err := client.CreateActorTemplate(ctx, &ateapi.CreateActorTemplateRequest{ActorTemplate: tmpl})
		if status.Code(err) == codes.FailedPrecondition {
			// The v1 server refuses a template with FailedPrecondition when
			// it has no snapshot location (scion sets no per-template
			// location).
			return fmt.Errorf("substrate: create actor template %s/%s: Substrate has no snapshot location; set the ate-api-server --snapshot-location flag: %w", atespace, templateName, err)
		}
		if err != nil && status.Code(err) != codes.AlreadyExists {
			return fmt.Errorf("substrate: create actor template %s/%s: %w", atespace, templateName, err)
		}
		existing = created
	}

	if templateIsReady(existing) {
		return nil
	}

	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("substrate: actor template %s/%s did not become ready within %s", atespace, templateName, timeout)
		}
		sleep(templateReadyPollInterval)

		existing, err = client.GetActorTemplate(ctx, &ateapi.GetActorTemplateRequest{
			ActorTemplate: &ateapi.ObjectRef{Atespace: atespace, Name: templateName},
		})
		if err != nil {
			return fmt.Errorf("substrate: poll actor template %s/%s: %w", atespace, templateName, err)
		}
		if templateIsReady(existing) {
			return nil
		}
		if msg := templateErrorMessage(existing); msg != "" {
			// templateName is deterministic (substrateTemplateName hashes
			// the image/config/resources shape), so leaving the failed
			// template in place would make every future Run for this same
			// shape find the SAME broken template via GetActorTemplate
			// above and never attempt CreateActorTemplate again — sticky
			// until an operator manually intervenes. Best-effort cleanup:
			// a failed delete here doesn't change the outcome (Run already
			// fails either way), and the next Run's own GetActorTemplate
			// will simply find the same broken template again and retry
			// this same cleanup.
			if _, delErr := client.DeleteActorTemplate(ctx, &ateapi.DeleteActorTemplateRequest{
				ActorTemplate: &ateapi.ObjectRef{Atespace: atespace, Name: templateName},
			}); delErr != nil && status.Code(delErr) != codes.NotFound {
				runtimeLog().Warn("substrate: failed to clean up a failed golden-snapshot template",
					"atespace", atespace, "template", templateName, "error", delErr)
			}
			return fmt.Errorf("substrate: actor template %s/%s golden snapshot failed: %s", atespace, templateName, msg)
		}
	}
}

// templateIsReady reports whether tmpl's golden snapshot has been taken
// (GoldenSnapshotStatus.golden_tag is set).
func templateIsReady(tmpl *ateapi.ActorTemplate) bool {
	return tmpl != nil &&
		tmpl.GetStatus() != nil &&
		tmpl.GetStatus().GetGoldenSnapshotStatus() != nil &&
		tmpl.GetStatus().GetGoldenSnapshotStatus().GetGoldenTag() != nil &&
		tmpl.GetStatus().GetGoldenSnapshotStatus().GetGoldenTag().GetName() != ""
}

// templateErrorMessage returns the golden-snapshot error message, if any.
func templateErrorMessage(tmpl *ateapi.ActorTemplate) string {
	if tmpl == nil || tmpl.GetStatus() == nil || tmpl.GetStatus().GetGoldenSnapshotStatus() == nil {
		return ""
	}
	return tmpl.GetStatus().GetGoldenSnapshotStatus().GetErrorMessage()
}
