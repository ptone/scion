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
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	ateapi "github.com/GoogleCloudPlatform/scion/third_party/substrate/ateapi/v1"
)

// mustResolveSubstrateResources resolves r or fails the test.
func mustResolveSubstrateResources(t *testing.T, r *api.ResourceSpec) substrateResources {
	t.Helper()
	res, err := resolveSubstrateResources(r)
	if err != nil {
		t.Fatalf("resolveSubstrateResources(%+v) error = %v", r, err)
	}
	return res
}

// mustBuildActorTemplate resolves resources and builds the template, or
// fails the test.
func mustBuildActorTemplate(t *testing.T, atespace, templateName, imageDigest string, sc config.V1SubstrateConfig, resources *api.ResourceSpec) *ateapi.ActorTemplate {
	t.Helper()
	tmpl, err := buildActorTemplate(atespace, templateName, imageDigest, sc, mustResolveSubstrateResources(t, resources))
	if err != nil {
		t.Fatalf("buildActorTemplate() error = %v", err)
	}
	return tmpl
}

const gib = int64(1) << 30

// cpu and memory resolve independently: the agent's limit when set, else
// the built-in substrate default (cpu 2, memory 4Gi).
func TestResolveSubstrateResources_PerField(t *testing.T) {
	cases := []struct {
		name    string
		in      *api.ResourceSpec
		wantCPU int64
		wantMem int64
	}{
		{"neither set (nil)", nil, 2000, 4 * gib},
		{"neither set (empty)", &api.ResourceSpec{}, 2000, 4 * gib},
		{"cpu only", &api.ResourceSpec{Limits: api.ResourceList{CPU: "4"}}, 4000, 4 * gib},
		{"memory only", &api.ResourceSpec{Limits: api.ResourceList{Memory: "8Gi"}}, 2000, 8 * gib},
		{"both", &api.ResourceSpec{Limits: api.ResourceList{CPU: "500m", Memory: "1536Mi"}}, 500, 1536 << 20},
		{"requests do not set limits", &api.ResourceSpec{Requests: api.ResourceList{CPU: "8", Memory: "16Gi"}}, 2000, 4 * gib},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustResolveSubstrateResources(t, tc.in)
			if got.cpuMillicores != tc.wantCPU || got.memoryBytes != tc.wantMem {
				t.Errorf("resolveSubstrateResources() = %+v, want cpu %dm, memory %d", got, tc.wantCPU, tc.wantMem)
			}
		})
	}
}

// The bounds: memory up to MaxInt64 bytes is accepted, MaxInt64+1 and 8Ei
// are refused; cpu up to v1's 999999 millicores is accepted, 1000 cores
// (and anything larger) refused. The error names the field.
func TestResolveSubstrateResources_Bounds(t *testing.T) {
	got, err := resolveSubstrateResources(&api.ResourceSpec{Limits: api.ResourceList{Memory: "9223372036854775807", CPU: "999999m"}})
	if err != nil || got.memoryBytes != math.MaxInt64 || got.cpuMillicores != 999999 {
		t.Errorf("maxima: resolveSubstrateResources() = %+v, %v; want memory MaxInt64 and cpu 999999m", got, err)
	}
	// A fractional suffix just under the int64 edge is accepted.
	if got, err := resolveSubstrateResources(&api.ResourceSpec{Limits: api.ResourceList{Memory: "7.99999Ei"}}); err != nil || got.memoryBytes <= 0 {
		t.Errorf("7.99999Ei: resolveSubstrateResources() = %+v, %v; want accepted", got, err)
	}
	for _, tc := range []struct {
		limits api.ResourceList
		field  string
	}{
		{api.ResourceList{Memory: "9223372036854775808"}, "memory"},
		{api.ResourceList{Memory: "8Ei"}, "memory"},
		{api.ResourceList{Memory: "8.00001Ei"}, "memory"},
		{api.ResourceList{Memory: "9.3e18"}, "memory"},
		{api.ResourceList{CPU: "1000"}, "cpu"},
		{api.ResourceList{CPU: "1000000m"}, "cpu"},
		{api.ResourceList{CPU: "9223372036854775807m"}, "cpu"},
	} {
		got, err := resolveSubstrateResources(&api.ResourceSpec{Limits: tc.limits})
		if err == nil {
			t.Errorf("resolveSubstrateResources(%+v) = %+v, nil; want an error", tc.limits, got)
			continue
		}
		if !strings.Contains(err.Error(), tc.field) {
			t.Errorf("error = %q, want it to name the %s field", err, tc.field)
		}
	}
}

// Invalid, non-positive and int64-overflowing values are refused.
func TestResolveSubstrateResources_Invalid(t *testing.T) {
	for _, tc := range []api.ResourceList{
		{CPU: "abc"},
		{CPU: "0"},
		{CPU: "-1"},
		{CPU: "10P"}, // 1e19 millicores overflows int64
		{Memory: "lots"},
		{Memory: "0Mi"},
		{Memory: "-4Gi"},
		{Memory: "8Ei"}, // 2^63 bytes overflows int64
	} {
		if got, err := resolveSubstrateResources(&api.ResourceSpec{Limits: tc}); err == nil {
			t.Errorf("resolveSubstrateResources(%+v) = %+v, nil; want an error", tc, got)
		}
	}
}

// Limits are set at template level only; the container carries none.
func TestBuildActorTemplate_ResourcesAtTemplateLevelOnly(t *testing.T) {
	image := "repo/image@sha256:" + strings.Repeat("b", 64)
	for _, in := range []*api.ResourceSpec{nil, {Limits: api.ResourceList{CPU: "3", Memory: "6Gi"}}} {
		tmpl := mustBuildActorTemplate(t, "scion-test", "scion-abc123", image, config.V1SubstrateConfig{}, in)
		want := mustResolveSubstrateResources(t, in)
		lim := tmpl.GetResources().GetLimits()
		if lim.GetCpuMillicores() != want.cpuMillicores || lim.GetMemoryBytes() != want.memoryBytes {
			t.Errorf("template limits = %v, want cpu %dm, memory %d", lim, want.cpuMillicores, want.memoryBytes)
		}
		for _, c := range tmpl.GetContainers() {
			if c.GetResources() != nil {
				t.Errorf("container %q Resources = %v, want none (limits are template-level only)", c.GetName(), c.GetResources())
			}
		}
	}
}

// A template cannot be built without both a cpu and a memory limit.
func TestBuildActorTemplate_RefusesMissingResource(t *testing.T) {
	image := "repo/image@sha256:" + strings.Repeat("b", 64)
	for _, res := range []substrateResources{{}, {cpuMillicores: 1000}, {memoryBytes: gib}, {cpuMillicores: -1, memoryBytes: gib}} {
		if tmpl, err := buildActorTemplate("scion-test", "scion-abc123", image, config.V1SubstrateConfig{}, res); err == nil {
			t.Errorf("buildActorTemplate(%+v) = %v, nil; want an error", res, tmpl)
		}
	}
}

// snapshot_config is omitted (v1 defaults; golden snapshots on by default).
func TestBuildActorTemplate_OmitsSnapshotConfig(t *testing.T) {
	image := "repo/image@sha256:" + strings.Repeat("b", 64)
	tmpl := mustBuildActorTemplate(t, "scion-test", "scion-abc123", image, config.V1SubstrateConfig{EgressTrustBundle: "egress-mitm.ate.dev"}, nil)
	if tmpl.GetSnapshotConfig() != nil {
		t.Errorf("SnapshotConfig = %v, want omitted", tmpl.GetSnapshotConfig())
	}
}

// Every field of the explicit hash list changes the name; equal inputs give
// equal names.
func TestSubstrateTemplateHashInput_EachFieldChangesName(t *testing.T) {
	base := substrateTemplateHashInput{
		image:             "repo/image@sha256:" + strings.Repeat("a", 64),
		workerSelector:    "pool=x",
		cpuMillicores:     2000,
		memoryBytes:       4 * gib,
		caps:              "CHOWN,SETUID",
		entrypointVersion: "substrate-serve/v2",
		probe:             "wakeup(path=/scion/v1/healthz,port=80,timeout=120)",
		trustBundleNames:  "egress-mitm.ate.dev",
		volumes:           "system-info:system_info,workspace:durable_dir",
	}
	copyOf := base
	if copyOf.name() != base.name() {
		t.Fatal("a copy of the inputs gave a different name")
	}
	mutations := map[string]func(*substrateTemplateHashInput){
		"image":             func(in *substrateTemplateHashInput) { in.image += "x" },
		"workerSelector":    func(in *substrateTemplateHashInput) { in.workerSelector = "pool=y" },
		"cpuMillicores":     func(in *substrateTemplateHashInput) { in.cpuMillicores++ },
		"memoryBytes":       func(in *substrateTemplateHashInput) { in.memoryBytes++ },
		"caps":              func(in *substrateTemplateHashInput) { in.caps = "CHOWN" },
		"entrypointVersion": func(in *substrateTemplateHashInput) { in.entrypointVersion = "substrate-serve/v3" },
		"probe":             func(in *substrateTemplateHashInput) { in.probe = "wakeup(path=/x,port=80,timeout=120)" },
		"trustBundleNames":  func(in *substrateTemplateHashInput) { in.trustBundleNames = "" },
		"volumes":           func(in *substrateTemplateHashInput) { in.volumes = "workspace:durable_dir" },
	}
	for field, mutate := range mutations {
		in := base
		mutate(&in)
		if in.name() == base.name() {
			t.Errorf("changing %s did not change the template name", field)
		}
	}
}

// The real inputs reach the hash: worker selector (order-insensitive),
// resolved resources and image. No trust bundle is projected in Phase 1, so
// egress_trust_bundle does not change the name (Run refuses it).
func TestSubstrateTemplateName_InputsReachHash(t *testing.T) {
	image := "repo/image@sha256:" + strings.Repeat("a", 64)
	res := mustResolveSubstrateResources(t, nil)
	sc := config.V1SubstrateConfig{WorkerSelector: map[string]string{"a": "1", "b": "2"}}
	base := substrateTemplateName(image, sc, res)

	if again := substrateTemplateName(image, config.V1SubstrateConfig{WorkerSelector: map[string]string{"b": "2", "a": "1"}}, res); again != base {
		t.Errorf("equal inputs gave %q and %q", base, again)
	}
	if tb := substrateTemplateName(image, config.V1SubstrateConfig{WorkerSelector: sc.WorkerSelector, EgressTrustBundle: "egress-mitm.ate.dev"}, res); tb != base {
		t.Errorf("egress_trust_bundle changed the template name (%q vs %q); nothing is projected in Phase 1", tb, base)
	}
	for name, got := range map[string]string{
		"image":           substrateTemplateName(image+"0", sc, res),
		"worker selector": substrateTemplateName(image, config.V1SubstrateConfig{WorkerSelector: map[string]string{"a": "1"}}, res),
		"cpu":             substrateTemplateName(image, sc, substrateResources{cpuMillicores: res.cpuMillicores + 1, memoryBytes: res.memoryBytes}),
		"memory":          substrateTemplateName(image, sc, substrateResources{cpuMillicores: res.cpuMillicores, memoryBytes: res.memoryBytes + 1}),
	} {
		if got == base {
			t.Errorf("changing the %s did not change the template name", name)
		}
	}
}

// Each settings key v1 has no target for refuses the Run, naming the key and
// its replacement, before any control-plane call.
func TestSubstrateRun_RemovedSettingsKeysRefused(t *testing.T) {
	cases := []struct {
		key             string
		sc              config.V1SubstrateConfig
		wantReplacement string
	}{
		{"sandbox_class", config.V1SubstrateConfig{SandboxClass: "gvisor"}, "spec.sandboxClasses"},
		{"sandbox_config_name", config.V1SubstrateConfig{SandboxConfigName: "gvisor-default"}, "spec.sandboxClasses"},
		{"snapshot_storage", config.V1SubstrateConfig{SnapshotStorage: "gs://bucket/prefix/"}, "--snapshot-location"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			resetSubstrateAgentStateForTest(t)
			rec := &callRecorder{}
			rt := NewSubstrateRuntimeForTest(newFakeControlClient(rec), substrate.NewRouterClient("http://example.invalid"), nil, tc.sc)
			_, err := rt.Run(context.Background(), testSubstrateRunConfig())
			if err == nil {
				t.Fatalf("Run() with %s set = nil error, want a refusal", tc.key)
			}
			msg := err.Error()
			if !strings.Contains(msg, `"`+tc.key+`"`) || !strings.Contains(msg, tc.wantReplacement) {
				t.Errorf("Run() error = %q, want it to name %q and %q", msg, tc.key, tc.wantReplacement)
			}
			if tc.key != "snapshot_storage" && !strings.Contains(msg, "worker_selector") {
				t.Errorf("Run() error = %q, want it to name worker_selector", msg)
			}
			if calls := rec.list(); len(calls) != 0 {
				t.Errorf("Run() made control-plane calls %v before refusing", calls)
			}
		})
	}
}

// Invalid resources refuse the Run before any control-plane call.
func TestSubstrateRun_InvalidResourcesRefusedBeforeControlPlane(t *testing.T) {
	rec := &callRecorder{}
	rt, _, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	cfg := testSubstrateRunConfig()
	cfg.Resources = &api.ResourceSpec{Limits: api.ResourceList{Memory: "-1Gi"}}
	if _, err := rt.Run(context.Background(), cfg); err == nil {
		t.Fatal("Run() with a negative memory limit = nil error, want a refusal")
	}
	if calls := rec.list(); len(calls) != 0 {
		t.Errorf("Run() made control-plane calls %v before refusing", calls)
	}
}

// A FailedPrecondition from CreateActorTemplate (no server snapshot
// location) becomes an error that names the ate-api-server flag.
func TestEnsureActorTemplate_FailedPreconditionNamesSnapshotLocation(t *testing.T) {
	rec := &callRecorder{}
	fc := newFakeControlClient(rec)
	fc.createActorTemplate = func(*ateapi.CreateActorTemplateRequest) (*ateapi.ActorTemplate, error) {
		return nil, status.Error(codes.FailedPrecondition, "no snapshot location configured")
	}
	image := "repo/image@sha256:" + strings.Repeat("b", 64)
	tmpl := mustBuildActorTemplate(t, "scion-test", "scion-abc123", image, config.V1SubstrateConfig{}, nil)

	err := ensureActorTemplate(context.Background(), fc, "scion-test", "scion-abc123", tmpl, time.Minute, func(time.Duration) {})
	if err == nil {
		t.Fatal("ensureActorTemplate() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "--snapshot-location") {
		t.Errorf("error = %q, want it to name --snapshot-location", err)
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("status.Code(error) = %v, want FailedPrecondition preserved", status.Code(err))
	}
}

// egress_trust_bundle refuses the Run before any control-plane call: Phase 1
// projects no trust bundle and emits no https (MITM) rules.
func TestSubstrateRun_EgressTrustBundleRefused(t *testing.T) {
	resetSubstrateAgentStateForTest(t)
	rec := &callRecorder{}
	rt := NewSubstrateRuntimeForTest(newFakeControlClient(rec), substrate.NewRouterClient("http://example.invalid"), nil, config.V1SubstrateConfig{EgressTrustBundle: "egress-mitm.ate.dev"})
	_, err := rt.Run(context.Background(), testSubstrateRunConfig())
	if err == nil {
		t.Fatal("Run() with egress_trust_bundle set = nil error, want a refusal")
	}
	for _, want := range []string{"egress_trust_bundle", "Phase 3", "unset it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Run() error = %q, want it to contain %q", err, want)
		}
	}
	if calls := rec.list(); len(calls) != 0 {
		t.Errorf("Run() made control-plane calls %v before refusing", calls)
	}
}

func TestQuantityRat(t *testing.T) {
	for in, want := range map[string]*big.Rat{
		"2":    big.NewRat(2, 1),
		"500m": big.NewRat(1, 2),
		"4Gi":  big.NewRat(1<<32, 1),
		"1e3":  big.NewRat(1000, 1),
		"1.5k": big.NewRat(1500, 1),
		"1E":   big.NewRat(1_000_000_000_000_000_000, 1),
		"8Ei":  new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), 63)),
	} {
		got, ok := quantityRat(in)
		if !ok || got.Cmp(want) != 0 {
			t.Errorf("quantityRat(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
}
