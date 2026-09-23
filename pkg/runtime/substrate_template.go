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
	"sort"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// substrateServeEntrypointVersion is folded into the template's
// content-address (phase1-spec.md §2.2 step 3) so a change to the
// `sciontool substrate-serve` protocol forces a new template rather than
// silently reusing a golden snapshot built against the old one.
const substrateServeEntrypointVersion = "substrate-serve/v1"

// defaultTemplateReadyTimeout bounds how long Run waits for a newly created
// ActorTemplate's golden snapshot to become ready, when
// V1SubstrateConfig.TemplateReadyTimeout is unset.
const defaultTemplateReadyTimeout = 10 * time.Minute

// templateReadyPollInterval is how often Run re-polls GetActorTemplate
// while waiting for the golden snapshot.
const templateReadyPollInterval = 5 * time.Second

// substrateTemplateName computes the content-addressed ActorTemplate name
// (phase1-spec.md §2.2 step 3). Same inputs always produce the same name,
// so concurrent Runs for the same effective template converge on one
// CreateActorTemplate instead of racing to create distinct ones.
//
// The hash covers every input that changes what buildActorTemplate
// produces: image digest, sandbox class, sandbox config name, worker
// selector, snapshot storage location, the *effective* resources (resolved
// against config.BuiltinDefaultResources when resources is nil — hashing
// the nil pointer as "" while buildActorTemplate substitutes a real default
// would let a change to that default silently reuse the old golden
// template), the hardcoded snapshot scope, and the entrypoint version. This
// is a deliberate deviation from the spec's literal hash-input list (image
// digest + sandbox class + resources + scope + entrypoint version only) —
// see review round 1, Consider #7: those other fields are template content
// too, and changing them in settings must not silently reuse a stale
// golden template.
func substrateTemplateName(imageDigest string, sc config.V1SubstrateConfig, resources *api.ResourceSpec) string {
	effectiveResources := resources
	if effectiveResources == nil {
		effectiveResources = config.BuiltinDefaultResources()
	}

	h := sha256.New()
	// hash.Hash.Write never returns an error (see the hash.Hash doc
	// comment), so the error from Fprintf is deliberately discarded rather
	// than checked.
	_, _ = fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%s|%s",
		imageDigest,
		sc.SandboxClass,
		sc.SandboxConfigName,
		workerSelectorCacheKey(sc.WorkerSelector),
		sc.SnapshotStorage,
		resourcesCacheKey(effectiveResources),
		"DATA", // on_pause/on_commit scope, hardcoded for Phase 1
		substrateServeEntrypointVersion,
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

// resourcesCacheKey renders a *api.ResourceSpec into a stable string for
// substrateTemplateName's hash input.
func resourcesCacheKey(r *api.ResourceSpec) string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("req(cpu=%s,mem=%s)limit(cpu=%s,mem=%s)disk=%s",
		r.Requests.CPU, r.Requests.Memory, r.Limits.CPU, r.Limits.Memory, r.Disk)
}

// substrateSandboxClass maps the config string ("gvisor"/"microvm", default
// gvisor) to the proto enum.
func substrateSandboxClass(name string) ateapipb.SandboxClass {
	switch name {
	case "microvm":
		return ateapipb.SandboxClass_SANDBOX_CLASS_MICROVM
	default:
		return ateapipb.SandboxClass_SANDBOX_CLASS_GVISOR
	}
}

// buildActorTemplate constructs the ActorTemplate for CreateActorTemplate
// (phase1-spec.md §2.2 step 3). Env is always empty — per-agent config is
// never baked into the template (findings.md §4.2); it is pushed after the
// actor starts, via POST /scion/v1/bootstrap.
func buildActorTemplate(atespace, templateName, imageDigest string, sc config.V1SubstrateConfig, resources *api.ResourceSpec) *ateapipb.ActorTemplate {
	if resources == nil {
		resources = config.BuiltinDefaultResources()
	}

	var limits []*ateapipb.Limits
	if resources.Limits.CPU != "" {
		limits = append(limits, &ateapipb.Limits{Name: "cpu", Quantity: resources.Limits.CPU})
	}
	if resources.Limits.Memory != "" {
		limits = append(limits, &ateapipb.Limits{Name: "memory", Quantity: resources.Limits.Memory})
	}

	tmpl := &ateapipb.ActorTemplate{
		Metadata: &ateapipb.ResourceMetadata{Atespace: atespace, Name: templateName},
		Containers: []*ateapipb.Container{
			{
				Name:    "scion-agent",
				Image:   imageDigest,
				Command: []string{"sciontool", "substrate-serve"},
				Env:     nil, // no secrets, ever (findings.md §4.2)
				VolumeMounts: []*ateapipb.VolumeMount{
					{Name: "workspace", MountPath: "/workspace"},
				},
				Resources: &ateapipb.Resources{Limits: limits},
			},
		},
		Volumes: []*ateapipb.Volume{
			{Name: "workspace", DurableDir: &ateapipb.DurableDirVolumeSource{}},
		},
		SnapshotsConfig: &ateapipb.SnapshotsConfig{
			OnPause:         ateapipb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_DATA,
			OnCommit:        ateapipb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_DATA,
			StorageLocation: sc.SnapshotStorage,
		},
		SandboxConfig: &ateapipb.SandboxConfig{
			SandboxClass: substrateSandboxClass(sc.SandboxClass),
			ConfigName:   sc.SandboxConfigName,
		},
	}

	if len(sc.WorkerSelector) > 0 {
		tmpl.WorkerSelector = &ateapipb.Selector{MatchLabels: sc.WorkerSelector}
	}

	return tmpl
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
// ready (phase1-spec.md §2.2 step 3: "creating a template boots a golden
// actor, so wait for the template to be ready"). clock lets tests replace
// time.Sleep with an instant no-op.
func ensureActorTemplate(ctx context.Context, client ateapipb.ControlClient, atespace, templateName string, tmpl *ateapipb.ActorTemplate, timeout time.Duration, sleep func(time.Duration)) error {
	existing, err := client.GetActorTemplate(ctx, &ateapipb.GetActorTemplateRequest{
		ActorTemplate: &ateapipb.ObjectRef{Atespace: atespace, Name: templateName},
	})
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return fmt.Errorf("substrate: get actor template %s/%s: %w", atespace, templateName, err)
		}
		created, err := client.CreateActorTemplate(ctx, &ateapipb.CreateActorTemplateRequest{ActorTemplate: tmpl})
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

		existing, err = client.GetActorTemplate(ctx, &ateapipb.GetActorTemplateRequest{
			ActorTemplate: &ateapipb.ObjectRef{Atespace: atespace, Name: templateName},
		})
		if err != nil {
			return fmt.Errorf("substrate: poll actor template %s/%s: %w", atespace, templateName, err)
		}
		if templateIsReady(existing) {
			return nil
		}
		if msg := templateErrorMessage(existing); msg != "" {
			return fmt.Errorf("substrate: actor template %s/%s golden snapshot failed: %s", atespace, templateName, msg)
		}
	}
}

// templateIsReady reports whether tmpl's golden snapshot has been taken
// (GoldenSnapshotStatus.golden_tag is set).
func templateIsReady(tmpl *ateapipb.ActorTemplate) bool {
	return tmpl != nil &&
		tmpl.GetStatus() != nil &&
		tmpl.GetStatus().GetGoldenSnapshotStatus() != nil &&
		tmpl.GetStatus().GetGoldenSnapshotStatus().GetGoldenTag() != nil &&
		tmpl.GetStatus().GetGoldenSnapshotStatus().GetGoldenTag().GetName() != ""
}

// templateErrorMessage returns the golden-snapshot error message, if any.
func templateErrorMessage(tmpl *ateapipb.ActorTemplate) string {
	if tmpl == nil || tmpl.GetStatus() == nil || tmpl.GetStatus().GetGoldenSnapshotStatus() == nil {
		return ""
	}
	return tmpl.GetStatus().GetGoldenSnapshotStatus().GetErrorMessage()
}
