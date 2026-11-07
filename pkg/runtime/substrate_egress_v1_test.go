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
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	ateapi "github.com/GoogleCloudPlatform/scion/third_party/substrate/ateapi/v1"
)

// egressRuleView flattens one v1 egress rule for assertions.
type egressRuleView struct {
	protocol string
	hosts    []string
	ports    []int32
	allPorts bool
}

func viewEgressRules(t *testing.T, req *ateapi.CreateActorEgressPolicyRequest) []egressRuleView {
	t.Helper()
	var views []egressRuleView
	for i, r := range req.GetEgressPolicy().GetRules() {
		switch {
		case r.GetHttp() != nil:
			views = append(views, egressRuleView{"http", r.GetHttp().GetHostnames(), r.GetHttp().GetPorts().GetNumbers(), r.GetHttp().GetPorts().GetAll() != nil})
		case r.GetTlsPassthrough() != nil:
			views = append(views, egressRuleView{"tls_passthrough", r.GetTlsPassthrough().GetHostnames(), r.GetTlsPassthrough().GetPorts().GetNumbers(), r.GetTlsPassthrough().GetPorts().GetAll() != nil})
		case r.GetHttps() != nil:
			t.Errorf("rule[%d] is an https (MITM) rule; none is produced yet", i)
		default:
			t.Errorf("rule[%d] sets no protocol: %v", i, r)
		}
	}
	return views
}

// targetsFor returns the targets for host, in order.
func targetsFor(targets []substrateEgressTarget, host string) []substrateEgressTarget {
	var out []substrateEgressTarget
	for _, tg := range targets {
		if tg.host == host {
			out = append(out, tg)
		}
	}
	return out
}

// assertOnlyHTTPS443 fails if any target is not TLS passthrough on 443.
func assertOnlyHTTPS443(t *testing.T, targets []substrateEgressTarget) {
	t.Helper()
	for _, tg := range targets {
		if tg.protocol != substrate.EgressProtocolTLSPassthrough || tg.port != 443 {
			t.Errorf("target %+v: want only tls_passthrough on 443", tg)
		}
	}
}

// A trusted hub endpoint from broker configuration on http and a port other
// than 443 (an in-cluster hub) yields exactly one http rule on that port.
func TestSubstrateEgressTargets_TrustedInClusterHTTPHub(t *testing.T) {
	cfg := RunConfig{TrustedHubEndpoint: "http://hub.scion-system.svc:8080"}
	targets, err := substrateEgressTargets(cfg, map[string]string{}, config.V1SubstrateConfig{})
	if err != nil {
		t.Fatalf("substrateEgressTargets() error = %v", err)
	}
	hub := targetsFor(targets, "hub.scion-system.svc")
	want := []substrateEgressTarget{{protocol: substrate.EgressProtocolHTTP, host: "hub.scion-system.svc", port: 8080}}
	if !slices.Equal(hub, want) {
		t.Fatalf("hub targets = %+v, want exactly %+v", hub, want)
	}

	var httpRules []egressRuleView
	for _, r := range viewEgressRules(t, buildEgressPolicy("scion-p", "a", targets)) {
		if r.protocol == "http" {
			httpRules = append(httpRules, r)
		}
	}
	if len(httpRules) != 1 || !slices.Equal(httpRules[0].hosts, []string{"hub.scion-system.svc"}) || !slices.Equal(httpRules[0].ports, []int32{8080}) || httpRules[0].allPorts {
		t.Errorf("http rules = %+v, want one http rule for hub.scion-system.svc on 8080", httpRules)
	}
}

// The scheme and port defaults of the trusted hub endpoint.
func TestSubstrateEgressTargets_TrustedHubDefaults(t *testing.T) {
	for endpoint, want := range map[string]substrateEgressTarget{
		"http://hub.example.com":       {protocol: substrate.EgressProtocolHTTP, host: "hub.example.com", port: 80},
		"https://hub.example.com":      {protocol: substrate.EgressProtocolTLSPassthrough, host: "hub.example.com", port: 443},
		"hub.example.com":              {protocol: substrate.EgressProtocolTLSPassthrough, host: "hub.example.com", port: 443},
		"https://Hub.Example.com:8443": {protocol: substrate.EgressProtocolTLSPassthrough, host: "hub.example.com", port: 8443},
	} {
		targets, err := substrateEgressTargets(RunConfig{TrustedHubEndpoint: endpoint}, map[string]string{}, config.V1SubstrateConfig{})
		if err != nil {
			t.Errorf("TrustedHubEndpoint %q: error = %v", endpoint, err)
			continue
		}
		if got := targetsFor(targets, "hub.example.com"); !slices.Equal(got, []substrateEgressTarget{want}) {
			t.Errorf("TrustedHubEndpoint %q: targets = %+v, want %+v", endpoint, got, want)
		}
	}

	// Any scheme but a leading http:// or https:// makes the endpoint
	// unparseable: no hub target, a warning, no refusal.
	targets, err := substrateEgressTargets(RunConfig{TrustedHubEndpoint: "ftp://hub.example.com"}, map[string]string{}, config.V1SubstrateConfig{})
	if err != nil {
		t.Errorf("TrustedHubEndpoint with scheme ftp: error = %v, want no hub target and no refusal", err)
	}
	if got := targetsFor(targets, "hub.example.com"); len(got) != 0 {
		t.Errorf("TrustedHubEndpoint with scheme ftp: targets = %+v, want none", got)
	}
}

// A hub-endpoint-shaped value supplied through agent, template or harness
// env (SCION_HUB_ENDPOINT / SCION_HUB_URL) never yields an http rule or a
// port other than 443, whatever the trusted endpoint is.
func TestSubstrateEgressTargets_EnvHubEndpointNeverYieldsHTTPOrNon443(t *testing.T) {
	for _, trusted := range []string{"", "https://hub.example.com"} {
		for _, key := range []string{"SCION_HUB_ENDPOINT", "SCION_HUB_URL"} {
			for _, v := range []string{"http://hub.example.com:8080", "http://evil.example.com", "https://evil.example.com:8443", "evil.example.com:9000"} {
				env := map[string]string{key: v}
				sc := config.V1SubstrateConfig{EgressAllow: []string{"evil.example.com"}}
				targets, err := substrateEgressTargets(RunConfig{TrustedHubEndpoint: trusted}, env, sc)
				if err != nil {
					t.Errorf("trusted %q, %s=%q: error = %v", trusted, key, v, err)
					continue
				}
				assertOnlyHTTPS443(t, targets)
			}
		}
	}
}

// The same guarantee end to end through Run: a hub endpoint in the agent's
// env (where template env lands), in the resolved auth env, or from the
// harness produces no http and no non-443 rule in the
// CreateActorEgressPolicy request.
func TestSubstrateRun_EnvHubEndpointYieldsNoHTTPOrNon443Rule(t *testing.T) {
	const hub = "http://hub.scion-system.svc:8080"
	sources := map[string]func(cfg *RunConfig){
		// RunConfig.Env is where the resolved env (api.StartOptions
		// ResolvedEnv) and template/agent env land.
		"resolved or template env": func(cfg *RunConfig) {
			cfg.Env = append(slices.Clone(cfg.Env), "SCION_HUB_ENDPOINT="+hub, "SCION_HUB_URL="+hub)
		},
		"harness env": func(cfg *RunConfig) {
			cfg.Harness = &mockHarness{command: []string{"claude"}, env: map[string]string{"SCION_HUB_ENDPOINT": hub, "SCION_HUB_URL": hub}}
			cfg.Env = slices.DeleteFunc(slices.Clone(cfg.Env), func(e string) bool { return strings.HasPrefix(e, "SCION_HUB_ENDPOINT=") })
		},
		"resolved auth env": func(cfg *RunConfig) {
			cfg.ResolvedAuth = &api.ResolvedAuth{EnvVars: map[string]string{"SCION_HUB_ENDPOINT": hub}}
		},
	}
	for name, apply := range sources {
		t.Run(name, func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()

			var mu sync.Mutex
			var captured *ateapi.CreateActorEgressPolicyRequest
			fc.createActorEgressPolicy = func(req *ateapi.CreateActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
				mu.Lock()
				defer mu.Unlock()
				captured = proto.Clone(req).(*ateapi.CreateActorEgressPolicyRequest)
				return &ateapi.EgressPolicy{}, nil
			}

			cfg := testSubstrateRunConfig()
			cfg.TrustedHubEndpoint = ""
			apply(&cfg)
			if _, err := rt.Run(context.Background(), cfg); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if captured == nil {
				t.Fatal("Run() sent no CreateActorEgressPolicy request")
			}
			for _, r := range viewEgressRules(t, captured) {
				if r.protocol == "http" {
					t.Errorf("rule %+v: env hub endpoint produced an http rule", r)
				}
				if r.allPorts || !slices.Equal(r.ports, []int32{443}) {
					t.Errorf("rule %+v: want port 443 only", r)
				}
				if slices.Contains(r.hosts, "hub.scion-system.svc") {
					t.Errorf("rule %+v: env hub host reached the policy", r)
				}
			}
		})
	}
}

// A tenant-derived endpoint not on https port 443 that egress_allow does not
// list on its port and protocol is dropped with a warning naming the
// egress_allow entry to add (design r2 N2-2); the translation does not
// fail. The warning never carries the URL's userinfo or query.
func TestSubstrateEgressTargets_TenantNon443DroppedWithWarning(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		host     string
		wantHint string
	}{
		{"host:4317", map[string]string{"SCION_OTEL_ENDPOINT": "otel.example.com:4317"}, "otel.example.com", `otel.example.com:4317`},
		{"https on 4317", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otel.example.com:4317"}, "otel.example.com", `otel.example.com:4317`},
		{"http on 4318", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://otel.example.com:4318/v1/traces"}, "otel.example.com", `http://otel.example.com:4318`},
		{"http default port", map[string]string{"SCION_OTEL_ENDPOINT": "http://otel.example.com"}, "otel.example.com", `http://otel.example.com:80`},
		{"git clone on 8443", map[string]string{"SCION_GIT_CLONE_URL": "https://git.example.com:8443/org/repo.git"}, "git.example.com", `git.example.com:8443`},
		{"userinfo and query stripped", map[string]string{"SCION_OTEL_ENDPOINT": "https://tok-user:tok-secret@otel.example.com:4317/v1?key=tok-query"}, "otel.example.com", `otel.example.com:4317`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := &syncBuffer{}
			setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
			// A bare entry covers the host on 443 only.
			sc := config.V1SubstrateConfig{EgressAllow: []string{tc.host}}
			targets, err := substrateEgressTargets(RunConfig{}, tc.env, sc)
			if err != nil {
				t.Fatalf("substrateEgressTargets() error = %v, want the host dropped, not a failure", err)
			}
			for _, tg := range targetsFor(targets, tc.host) {
				if tg.port != 443 || tg.protocol != substrate.EgressProtocolTLSPassthrough {
					t.Errorf("target %+v: the uncovered non-443 endpoint produced a rule", tg)
				}
			}
			out := logs.String()
			if !strings.Contains(out, "egress_allow") || !strings.Contains(out, tc.wantHint) || !strings.Contains(out, tc.host) {
				t.Errorf("log = %q, want a warning naming %s and the egress_allow entry %s", out, tc.host, tc.wantHint)
			}
			if tc.name == "userinfo and query stripped" && !strings.Contains(out, "scheme=https") {
				t.Errorf("log = %q, want scheme=https in the reduction warning", out)
			}
			for _, leak := range []string{"tok-user", "tok-secret", "tok-query", "/v1"} {
				if strings.Contains(out, leak) {
					t.Errorf("log = %q, leaks %q from the endpoint URL", out, leak)
				}
			}
		})
	}

	t.Run("non-http scheme is unparseable", func(t *testing.T) {
		logs := &syncBuffer{}
		setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
		env := map[string]string{"SCION_OTEL_ENDPOINT": "grpc://otel.example.com:4317"}
		sc := config.V1SubstrateConfig{EgressAllow: []string{"otel.example.com:4317"}}
		targets, err := substrateEgressTargets(RunConfig{}, env, sc)
		if err != nil {
			t.Fatalf("error = %v, want a drop", err)
		}
		// Only the operator entry itself remains.
		want := []substrateEgressTarget{{protocol: substrate.EgressProtocolTLSPassthrough, host: "otel.example.com", port: 4317}}
		if got := targetsFor(targets, "otel.example.com"); !slices.Equal(got, want) {
			t.Errorf("targets = %+v, want only the operator entry %+v", got, want)
		}
		out := logs.String()
		if !strings.Contains(out, "could not be parsed") || !strings.Contains(out, "source=SCION_OTEL_ENDPOINT") {
			t.Errorf("log = %q, want the unparseable warning naming the source", out)
		}
		for _, leak := range []string{"grpc", "otel.example.com", "4317"} {
			if strings.Contains(out, leak) {
				t.Errorf("log = %q, leaks %q", out, leak)
			}
		}
	})
}

// End to end: a telemetry host on 4317 with no operator entry yields no rule
// for it and a warning, and the Run proceeds.
func TestSubstrateRun_TelemetryOn4317WithoutEntryDroppedRunProceeds(t *testing.T) {
	logs := &syncBuffer{}
	setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	var mu sync.Mutex
	var captured *ateapi.CreateActorEgressPolicyRequest
	fc.createActorEgressPolicy = func(req *ateapi.CreateActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
		mu.Lock()
		defer mu.Unlock()
		captured = proto.Clone(req).(*ateapi.CreateActorEgressPolicyRequest)
		return &ateapi.EgressPolicy{}, nil
	}

	cfg := testSubstrateRunConfig()
	cfg.Env = append(slices.Clone(cfg.Env), "SCION_OTEL_ENDPOINT=otel.example.com:4317")
	if _, err := rt.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run() error = %v, want the Run to proceed", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if captured == nil {
		t.Fatal("Run() sent no CreateActorEgressPolicy request")
	}
	for _, r := range viewEgressRules(t, captured) {
		if slices.Contains(r.hosts, "otel.example.com") {
			t.Errorf("rule %+v: the uncovered telemetry host reached the policy", r)
		}
	}
	if out := logs.String(); !strings.Contains(out, "otel.example.com:4317") || !strings.Contains(out, "egress_allow") {
		t.Errorf("log = %q, want the warning naming otel.example.com:4317 and egress_allow", out)
	}
}

// An egress_allow entry otel.example.com:4317 yields exactly one
// tls_passthrough rule on 4317 for that host, and a telemetry endpoint on
// that port is then allowed without adding another.
func TestSubstrateEgressTargets_EgressAllowHostPort(t *testing.T) {
	sc := config.V1SubstrateConfig{EgressAllow: []string{"otel.example.com:4317"}}
	for _, env := range []map[string]string{
		{},
		{"SCION_OTEL_ENDPOINT": "otel.example.com:4317"},
		{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otel.example.com:4317"},
	} {
		targets, err := substrateEgressTargets(RunConfig{}, env, sc)
		if err != nil {
			t.Fatalf("env %v: error = %v", env, err)
		}
		want := []substrateEgressTarget{{protocol: substrate.EgressProtocolTLSPassthrough, host: "otel.example.com", port: 4317}}
		if got := targetsFor(targets, "otel.example.com"); !slices.Equal(got, want) {
			t.Errorf("env %v: targets = %+v, want exactly %+v", env, got, want)
		}
		var rules []egressRuleView
		for _, r := range viewEgressRules(t, buildEgressPolicy("scion-p", "a", targets)) {
			if slices.Contains(r.hosts, "otel.example.com") {
				rules = append(rules, r)
			}
		}
		if len(rules) != 1 || rules[0].protocol != "tls_passthrough" || !slices.Equal(rules[0].ports, []int32{4317}) || rules[0].allPorts {
			t.Errorf("env %v: rules for otel.example.com = %+v, want one tls_passthrough rule on 4317", env, rules)
		}
	}
}

// An http:// egress_allow entry becomes an http rule on its port (default
// 80), and also allows a matching http telemetry endpoint.
func TestSubstrateEgressTargets_EgressAllowHTTP(t *testing.T) {
	sc := config.V1SubstrateConfig{EgressAllow: []string{"http://plain.example.com", "http://collector.example.com:4318"}}
	env := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector.example.com:4318"}
	targets, err := substrateEgressTargets(RunConfig{}, env, sc)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if got, want := targetsFor(targets, "plain.example.com"), []substrateEgressTarget{{protocol: substrate.EgressProtocolHTTP, host: "plain.example.com", port: 80}}; !slices.Equal(got, want) {
		t.Errorf("plain.example.com targets = %+v, want %+v", got, want)
	}
	if got, want := targetsFor(targets, "collector.example.com"), []substrateEgressTarget{{protocol: substrate.EgressProtocolHTTP, host: "collector.example.com", port: 4318}}; !slices.Equal(got, want) {
		t.Errorf("collector.example.com targets = %+v, want %+v", got, want)
	}
}

// Catch-alls, IPs and CIDRs in egress_allow are refused, in every syntax,
// and never become an allow-all rule.
func TestSubstrateEgressTargets_CatchAllsIPsAndCIDRsRefused(t *testing.T) {
	for _, bad := range []string{
		"*", "all", "0.0.0.0/0", "::/0", "*.*",
		"*:443", "http://*", "https://*:8443",
		"10.0.0.1", "10.0.0.1:443", "http://10.0.0.1:8080", "https://[2001:db8::1]:443",
		"10.0.0.0/8", "2001:db8::/32",
	} {
		sc := config.V1SubstrateConfig{EgressAllow: []string{bad}}
		if err := substrate.Validate(&sc); err == nil {
			t.Errorf("substrate.Validate(egress_allow %q) = nil, want a refusal", bad)
		}
		if targets, err := substrateEgressTargets(RunConfig{}, map[string]string{}, sc); err == nil {
			t.Errorf("substrateEgressTargets(egress_allow %q) = %+v, want a refusal", bad, targets)
		}
	}
}

// host, host:443 and https://host dedupe to one rule entry.
func TestSubstrateEgressTargets_EquivalentFormsDedupe(t *testing.T) {
	sc := config.V1SubstrateConfig{EgressAllow: []string{"api.example.com", "api.example.com:443", "https://api.example.com", "https://API.example.com:443/"}}
	targets, err := substrateEgressTargets(RunConfig{}, map[string]string{}, sc)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if got := targetsFor(targets, "api.example.com"); len(got) != 1 {
		t.Errorf("targets for api.example.com = %+v, want exactly one", got)
	}
	count := 0
	for _, r := range viewEgressRules(t, buildEgressPolicy("scion-p", "a", targets)) {
		for _, h := range r.hosts {
			if h == "api.example.com" {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("api.example.com appears %d times across rules, want once", count)
	}
}

// One host and port on two protocols would tie in v1; it is refused.
func TestSubstrateEgressTargets_ProtocolConflictRefused(t *testing.T) {
	sc := config.V1SubstrateConfig{EgressAllow: []string{"svc.example.com:8080", "http://svc.example.com:8080"}}
	if _, err := substrateEgressTargets(RunConfig{}, map[string]string{}, sc); err == nil {
		t.Error("egress_allow with host:8080 as both http and TLS: error = nil, want a refusal")
	}
	cfg := RunConfig{TrustedHubEndpoint: "http://hub.example.com:8080"}
	sc = config.V1SubstrateConfig{EgressAllow: []string{"hub.example.com:8080"}}
	if _, err := substrateEgressTargets(cfg, map[string]string{}, sc); err == nil {
		t.Error("http trusted hub and TLS egress_allow on the same host:port: error = nil, want a refusal")
	}
}

// The policy is the same whatever the order of the inputs.
func TestSubstrateEgressTargets_Deterministic(t *testing.T) {
	allow := []string{"b.example.com", "a.example.com:8443", "http://c.example.com:8080", "*.d.example.com", "a.example.com"}
	cfg := RunConfig{TrustedHubEndpoint: "http://hub.scion-system.svc:8080"}
	first, err := substrateEgressTargets(cfg, map[string]string{}, config.V1SubstrateConfig{EgressAllow: allow})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	reversed := slices.Clone(allow)
	slices.Reverse(reversed)
	second, err := substrateEgressTargets(cfg, map[string]string{}, config.V1SubstrateConfig{EgressAllow: reversed})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !slices.Equal(first, second) {
		t.Errorf("targets depend on egress_allow order:\n%+v\n%+v", first, second)
	}
	if a, b := buildEgressPolicy("scion-p", "a", first), buildEgressPolicy("scion-p", "a", second); !proto.Equal(a, b) {
		t.Errorf("policy depends on egress_allow order:\n%v\n%v", a, b)
	}
	if !slices.IsSortedFunc(first, func(x, y substrateEgressTarget) int {
		if substrateEgressTargetLess(x, y) {
			return -1
		}
		if substrateEgressTargetLess(y, x) {
			return 1
		}
		return 0
	}) {
		t.Errorf("targets not sorted: %+v", first)
	}
}

// A wildcard egress_allow entry is never widened: it is sent as the same
// single-label pattern (v1 ateapi.proto:683-685 matches exactly one label).
// A tenant host one label under it is covered and adds no rule of its own;
// a tenant host two labels under it is not covered: no rule, and a warning.
func TestSubstrateEgressTargets_WildcardNotWidened(t *testing.T) {
	logs := &syncBuffer{}
	setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
	sc := config.V1SubstrateConfig{EgressAllow: []string{"*.example.com"}}
	env := map[string]string{
		"SCION_GIT_CLONE_URL": "https://nested.api.example.com/org/repo.git",
		"SCION_OTEL_ENDPOINT": "https://api.example.com",
	}
	targets, err := substrateEgressTargets(RunConfig{}, env, sc)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	for _, h := range []string{"nested.api.example.com", "api.example.com"} {
		if got := targetsFor(targets, h); len(got) != 0 {
			t.Errorf("tenant host %s produced targets %+v; tenant hosts never emit a rule", h, got)
		}
	}
	var patterns []string
	for _, r := range viewEgressRules(t, buildEgressPolicy("scion-p", "a", targets)) {
		patterns = append(patterns, r.hosts...)
	}
	if !slices.Contains(patterns, "*.example.com") {
		t.Errorf("rule hostnames = %v, want the wildcard sent as written", patterns)
	}
	for _, p := range patterns {
		if p == "*" || p == "*.com" || (strings.HasPrefix(p, "*.") && p != "*.example.com") {
			t.Errorf("rule hostnames = %v, contain a broader wildcard %q", patterns, p)
		}
	}
	out := logs.String()
	if !strings.Contains(out, "nested.api.example.com") {
		t.Errorf("log = %q, want a warning for the two-label host nested.api.example.com", out)
	}
	if strings.Contains(out, "host=api.example.com") {
		t.Errorf("log = %q, the covered host api.example.com should not be warned about", out)
	}
	if !substrate.EgressAllowCovers([]string{"*.example.com"}, "api.example.com") || substrate.EgressAllowCovers([]string{"*.example.com"}, "nested.api.example.com") {
		t.Error("EgressAllowCovers: *.example.com must cover exactly one label")
	}
}

// A tenant host covered by an operator wildcard on the same port and
// protocol adds no extra rule: the policy equals the one without the
// tenant value.
func TestSubstrateEgressTargets_TenantCoveredByWildcardAddsNoRule(t *testing.T) {
	sc := config.V1SubstrateConfig{EgressAllow: []string{"*.example.com", "*.example.com:4317"}}
	without, err := substrateEgressTargets(RunConfig{}, map[string]string{}, sc)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	env := map[string]string{
		"SCION_OTEL_ENDPOINT": "otel.example.com:4317",
		"SCION_GIT_CLONE_URL": "https://git.example.com/org/repo.git",
	}
	with, err := substrateEgressTargets(RunConfig{}, env, sc)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !slices.Equal(with, without) {
		t.Errorf("tenant values changed the targets:\nwith:    %+v\nwithout: %+v", with, without)
	}
}

// A tenant host equal to an operator http://h:443 entry is not a tie and
// not a refusal (tenant hosts never emit a rule): the translation succeeds
// with only the operator's http rule for that host, and the Run proceeds.
func TestSubstrateEgressTargets_TenantEqualToOperatorHTTP443NoRefusal(t *testing.T) {
	sc := config.V1SubstrateConfig{EgressAllow: []string{"http://h.example.com:443"}}
	env := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://h.example.com"}
	targets, err := substrateEgressTargets(RunConfig{}, env, sc)
	if err != nil {
		t.Fatalf("error = %v, want no refusal", err)
	}
	want := []substrateEgressTarget{{protocol: substrate.EgressProtocolHTTP, host: "h.example.com", port: 443}}
	if got := targetsFor(targets, "h.example.com"); !slices.Equal(got, want) {
		t.Errorf("targets = %+v, want only the operator's %+v", got, want)
	}

	rec := &callRecorder{}
	rt, _, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	rt.cfg = sc
	cfg := testSubstrateRunConfig()
	cfg.Env = append(slices.Clone(cfg.Env), "OTEL_EXPORTER_OTLP_ENDPOINT=https://h.example.com")
	if _, err := rt.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run() error = %v, want the Run to proceed", err)
	}
}

// "*.example.com" yields exactly one wildcard rule entry on 443, and a
// wildcard plus an exact host under it on the same port is not a tie (v1
// ranks the exact name first), so both are sent and nothing is refused.
func TestSubstrateEgressTargets_WildcardOneRuleAndNoTieWithExact(t *testing.T) {
	sc := config.V1SubstrateConfig{EgressAllow: []string{"*.example.com"}}
	targets, err := substrateEgressTargets(RunConfig{}, map[string]string{}, sc)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	want := []substrateEgressTarget{{protocol: substrate.EgressProtocolTLSPassthrough, host: "*.example.com", port: 443}}
	if got := targetsFor(targets, "*.example.com"); !slices.Equal(got, want) {
		t.Errorf("targets = %+v, want %+v", got, want)
	}

	sc = config.V1SubstrateConfig{EgressAllow: []string{"*.example.com", "api.example.com"}}
	targets, err = substrateEgressTargets(RunConfig{}, map[string]string{}, sc)
	if err != nil {
		t.Fatalf("wildcard plus exact host: error = %v, want no tie refusal", err)
	}
	if len(targetsFor(targets, "*.example.com")) != 1 || len(targetsFor(targets, "api.example.com")) != 1 {
		t.Errorf("targets = %+v, want both the wildcard and the exact host", targets)
	}
}

// One host and port on two protocols is a config refusal with a
// deterministic error naming the host, port and both protocols.
func TestSubstrateEgressTargets_HTTPOn443PlusBareHostRefused(t *testing.T) {
	sc := config.V1SubstrateConfig{EgressAllow: []string{"http://h.example.com:443", "h.example.com"}}
	var first string
	for i := 0; i < 3; i++ {
		_, err := substrateEgressTargets(RunConfig{}, map[string]string{}, sc)
		if err == nil {
			t.Fatal("error = nil, want a refusal")
		}
		msg := err.Error()
		for _, want := range []string{"h.example.com:443", "http", "tls_passthrough"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error = %q, want it to name %q", msg, want)
			}
		}
		if i == 0 {
			first = msg
		} else if msg != first {
			t.Errorf("error not deterministic: %q vs %q", msg, first)
		}
	}
}

// Design-addenda A4: the binary-fixed model hosts are emitted as
// tls_passthrough on 443 with no operator entry.
func TestSubstrateEgressTargets_A4BinaryFixedHostsOn443(t *testing.T) {
	targets, err := substrateEgressTargets(RunConfig{}, map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", "CLOUD_ML_REGION": "us-east5"}, config.V1SubstrateConfig{})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	for _, h := range append(slices.Clone(hardcodedModelEgressHosts), "us-east5-aiplatform.googleapis.com") {
		want := []substrateEgressTarget{{protocol: substrate.EgressProtocolTLSPassthrough, host: h, port: 443}}
		if got := targetsFor(targets, h); !slices.Equal(got, want) {
			t.Errorf("targets for %s = %+v, want %+v", h, got, want)
		}
	}
}

// Design-addenda A4: an operator http://<model-host>:443 entry wins; the
// binary-fixed host is skipped with a warning and nothing is refused.
func TestSubstrateEgressTargets_A4OperatorWinsOverBinaryFixed(t *testing.T) {
	logs := &syncBuffer{}
	setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
	host := hardcodedModelEgressHosts[0]
	sc := config.V1SubstrateConfig{EgressAllow: []string{"http://" + host + ":443"}}
	targets, err := substrateEgressTargets(RunConfig{}, map[string]string{}, sc)
	if err != nil {
		t.Fatalf("error = %v, want no refusal", err)
	}
	want := []substrateEgressTarget{{protocol: substrate.EgressProtocolHTTP, host: host, port: 443}}
	if got := targetsFor(targets, host); !slices.Equal(got, want) {
		t.Errorf("targets for %s = %+v, want only the operator's %+v", host, got, want)
	}
	if out := logs.String(); !strings.Contains(out, "built-in egress host") || !strings.Contains(out, host) {
		t.Errorf("log = %q, want the skipped built-in host warning naming %s", out, host)
	}
}

// Design-addenda A4: an uncovered git clone host emits no rule and is
// dropped with a warning naming the egress_allow entry to add.
func TestSubstrateEgressTargets_A4UncoveredCloneHostDropped(t *testing.T) {
	logs := &syncBuffer{}
	setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
	cfg := RunConfig{GitClone: &api.GitCloneConfig{URL: "https://git.example.com/org/repo.git"}}
	targets, err := substrateEgressTargets(cfg, map[string]string{}, config.V1SubstrateConfig{})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if got := targetsFor(targets, "git.example.com"); len(got) != 0 {
		t.Errorf("uncovered clone host produced targets %+v", got)
	}
	if out := logs.String(); !strings.Contains(out, "git.example.com") || !strings.Contains(out, "egress_allow") {
		t.Errorf("log = %q, want the warning naming git.example.com and egress_allow", out)
	}
}

// Design §9 AC9 accepted forms, at the translation: http://host -> http on
// 80; https://host:8443 -> passthrough 8443; http://*.example.com:8080 ->
// one wildcard http rule on 8080; *.example.com:8443 -> one wildcard
// passthrough rule on 8443.
func TestSubstrateEgressTargets_AC9AcceptedForms(t *testing.T) {
	sc := config.V1SubstrateConfig{EgressAllow: []string{
		"http://plain.example.net", "https://api.example.net:8443", "http://*.example.com:8080", "*.example.com:8443",
	}}
	targets, err := substrateEgressTargets(RunConfig{}, map[string]string{}, sc)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	for host, want := range map[string][]substrateEgressTarget{
		"plain.example.net": {{protocol: substrate.EgressProtocolHTTP, host: "plain.example.net", port: 80}},
		"api.example.net":   {{protocol: substrate.EgressProtocolTLSPassthrough, host: "api.example.net", port: 8443}},
		"*.example.com": {
			{protocol: substrate.EgressProtocolHTTP, host: "*.example.com", port: 8080},
			{protocol: substrate.EgressProtocolTLSPassthrough, host: "*.example.com", port: 8443},
		},
	} {
		if got := targetsFor(targets, host); !slices.Equal(got, want) {
			t.Errorf("targets for %s = %+v, want %+v", host, got, want)
		}
	}
}

// assertTenantOnlyUnchanged checks design §9 AC9 GENERAL [r4:R4-1]: adding
// tenant-derived input (env) leaves the rule set unchanged.
func assertTenantOnlyUnchanged(t *testing.T, cfg RunConfig, sc config.V1SubstrateConfig, env map[string]string) []substrateEgressTarget {
	t.Helper()
	without, err := substrateEgressTargets(cfg, map[string]string{}, sc)
	if err != nil {
		t.Fatalf("without tenant input: error = %v", err)
	}
	with, err := substrateEgressTargets(cfg, env, sc)
	if err != nil {
		t.Fatalf("with tenant input: error = %v, want no refusal", err)
	}
	if !slices.Equal(with, without) {
		t.Errorf("tenant input changed the rules:\nwith:    %+v\nwithout: %+v", with, without)
	}
	return with
}

// Design §9 AC9 GENERAL [r4:R4-1], the four concrete cases.
func TestSubstrateEgressTargets_R4TenantOnlyInputLeavesRulesUnchanged(t *testing.T) {
	t.Run("hub-shaped env value", func(t *testing.T) {
		assertTenantOnlyUnchanged(t, RunConfig{TrustedHubEndpoint: "https://hub.example.com"}, config.V1SubstrateConfig{},
			map[string]string{"SCION_HUB_ENDPOINT": "http://hub.scion-system.svc:8080", "SCION_HUB_URL": "http://evil.example.com:8080"})
	})
	t.Run("uncovered tenant host on 443", func(t *testing.T) {
		logs := &syncBuffer{}
		setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
		assertTenantOnlyUnchanged(t, RunConfig{}, config.V1SubstrateConfig{},
			map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otel.example.com"})
		if out := logs.String(); !strings.Contains(out, "otel.example.com") || !strings.Contains(out, "egress_allow") {
			t.Errorf("log = %q, want the warning naming otel.example.com", out)
		}
	})
	t.Run("passthrough need covered only by an operator http wildcard", func(t *testing.T) {
		logs := &syncBuffer{}
		setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
		sc := config.V1SubstrateConfig{EgressAllow: []string{"http://*.example.com:443"}}
		got := assertTenantOnlyUnchanged(t, RunConfig{}, sc, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://api.example.com"})
		want := []substrateEgressTarget{{protocol: substrate.EgressProtocolHTTP, host: "*.example.com", port: 443}}
		if w := targetsFor(got, "*.example.com"); !slices.Equal(w, want) {
			t.Errorf("operator rule = %+v, want it untouched: %+v", w, want)
		}
		out := logs.String()
		if !strings.Contains(out, `add \"api.example.com\"`) {
			t.Errorf("log = %q, want the warning to name the passthrough entry api.example.com", out)
		}
		if strings.Contains(out, "http://api.example.com") {
			t.Errorf("log = %q, the warning names an http entry for a passthrough need", out)
		}
	})
	t.Run("tenant http://h:443 with operator https://h:443", func(t *testing.T) {
		sc := config.V1SubstrateConfig{EgressAllow: []string{"https://h.example.com:443"}}
		env := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://h.example.com:443"}
		assertTenantOnlyUnchanged(t, RunConfig{}, sc, env)

		rec := &callRecorder{}
		rt, _, _, closeServer := newTestSubstrateHarness(t, rec)
		defer closeServer()
		rt.cfg = sc
		cfg := testSubstrateRunConfig()
		cfg.Env = append(slices.Clone(cfg.Env), "OTEL_EXPORTER_OTLP_ENDPOINT=http://h.example.com:443")
		if _, err := rt.Run(context.Background(), cfg); err != nil {
			t.Fatalf("Run() error = %v, want the Run to proceed", err)
		}
	})
}

// Tenant values are reduced to scheme, host and port before parsing or
// logging; a parser error never echoes the URL's userinfo, path or query.
func TestSubstrateEgressTargets_TenantValueReducedBeforeParseOrLog(t *testing.T) {
	logs := &syncBuffer{}
	setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
	env := map[string]string{
		"SCION_OTEL_ENDPOINT":         "https://tok-user:tok-secret@bad_host!.example.com:4317/tok-path?key=tok-query#tok-frag",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://tok-user2:tok-secret2@10.0.0.1:4318/v1?key=tok-query2",
		"ANTHROPIC_VERTEX_BASE_URL":   "https://tok-user3:tok-secret3@[::1",
	}
	if _, err := substrateEgressTargets(RunConfig{}, env, config.V1SubstrateConfig{}); err != nil {
		t.Fatalf("error = %v, want drops only", err)
	}
	out := logs.String()
	for _, leak := range []string{"tok-user", "tok-secret", "tok-path", "tok-query", "tok-frag"} {
		if strings.Contains(out, leak) {
			t.Errorf("log = %q, leaks %q", out, leak)
		}
	}
	if !strings.Contains(out, "bad_host!.example.com") {
		t.Errorf("log = %q, want the invalid host named", out)
	}
}

func TestParseDerivedEndpoint(t *testing.T) {
	type res struct {
		scheme, host string
		port         int32
		ok           bool
	}
	for raw, want := range map[string]res{
		"https://User:pw@Host.Example.com:4317/p?q#f": {"https", "host.example.com", 4317, true},
		"HTTP://host.example.com":                     {"", "", 0, false},
		"host.example.com":                            {"https", "host.example.com", 443, true},
		"host.example.com:8080/x":                     {"https", "host.example.com", 8080, true},
		"grpc://host.example.com:4317":                {"", "", 0, false},
		"grpc://host.example.com":                     {"", "", 0, false},
		"https://host.example.com/p?next=https://x":   {"", "", 0, false},
		"tok:pw@host.example.com/?a=grpc://x":         {"", "", 0, false},
		"http://a?b@h.example":                        {"", "", 0, false},
		"http://host.example.com:8080/p":              {"http", "host.example.com", 8080, true},
		"https://[fd00::1]:8443":                      {"https", "fd00::1", 8443, true},
		"git@github.com:acme/repo.git":                {"", "", 0, false},
		"https://host.example.com:0":                  {"", "", 0, false},
		"https://host.example.com:99999":              {"", "", 0, false},
		"https://[::1":                                {"", "", 0, false},
		"2001:db8::1":                                 {"", "", 0, false},
		"":                                            {"", "", 0, false},
		// An "@" after the first "/", "?" or "#" (a malformed userinfo
		// holding that delimiter) makes the value unparseable (N5-2).
		"https://tok/user@host.example.com":  {"", "", 0, false},
		"https://tok?user@host.example.com":  {"", "", 0, false},
		"https://tok#user@host.example.com":  {"", "", 0, false},
		"https://host.example.com/path@x":    {"", "", 0, false},
		"https://u@host.example.com/p?x=1#y": {"https", "host.example.com", 443, true},
	} {
		s, h, p, ok := parseDerivedEndpoint(raw)
		if got := (res{s, h, p, ok}); got != want {
			t.Errorf("parseDerivedEndpoint(%q) = %+v, want %+v", raw, got, want)
		}
	}
}

// Design §9 AC9 (N5-2): a tenant value with an "@" after the first "/", "?"
// or "#" is unparseable: no rule, and a warning that names the source
// setting only (no host, userinfo, path or query).
func TestSubstrateEgressTargets_AtAfterDelimiterUnparseable(t *testing.T) {
	for _, delim := range []string{"/", "?", "#"} {
		t.Run(delim, func(t *testing.T) {
			logs := &syncBuffer{}
			setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
			sc := config.V1SubstrateConfig{EgressAllow: []string{"tokhost.example.com", "real.example.com"}}
			env := map[string]string{"SCION_OTEL_ENDPOINT": "https://tokhost.example.com" + delim + "tok-secret@real.example.com:4317"}
			assertTenantOnlyUnchanged(t, RunConfig{}, sc, env)
			out := logs.String()
			if !strings.Contains(out, "source=SCION_OTEL_ENDPOINT") {
				t.Errorf("log = %q, want a warning naming the source setting", out)
			}
			for _, leak := range []string{"tokhost", "tok-secret", "real.example.com"} {
				if strings.Contains(out, leak) {
					t.Errorf("log = %q, leaks %q", out, leak)
				}
			}
		})
	}
}

// Design §9 AC9 N5-2, the two literal values verbatim: no rule, and a
// warning naming the source setting only.
func TestSubstrateEgressTargets_N5_2LiteralValues(t *testing.T) {
	for _, v := range []string{"https://us/er@h.example/p", "http://a?b@h.example"} {
		t.Run(v, func(t *testing.T) {
			logs := &syncBuffer{}
			setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
			sc := config.V1SubstrateConfig{EgressAllow: []string{"h.example.com"}}
			assertTenantOnlyUnchanged(t, RunConfig{}, sc, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": v})
			out := logs.String()
			if !strings.Contains(out, "source=OTEL_EXPORTER_OTLP_ENDPOINT") {
				t.Errorf("log = %q, want a warning naming the source setting", out)
			}
			for _, leak := range []string{"h.example", "us/er", "a?b", "/p"} {
				if strings.Contains(out, leak) {
					t.Errorf("log = %q, leaks %q", out, leak)
				}
			}
		})
	}
}

// SEC-1: a "://" anywhere but a leading http:// or https:// makes a tenant
// value unparseable; neither the log nor any error carries value bytes.
func TestSubstrateEgressTargets_EmbeddedSchemeUnparseable(t *testing.T) {
	for name, v := range map[string]string{
		"scheme inside the query":  "https://tokhost.example.com/p?next=grpc://toksecret",
		"scheme after userinfo":    "tokuser:tokpass@tokhost.example.com/?a=https://toksecret",
		"non-http scheme":          "grpc://tokhost.example.com:4317",
		"uppercase scheme literal": "HTTPS://tokhost.example.com",
	} {
		t.Run(name, func(t *testing.T) {
			logs := &syncBuffer{}
			setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
			before, err := substrateEgressTargets(RunConfig{}, map[string]string{}, config.V1SubstrateConfig{})
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			after, err := substrateEgressTargets(RunConfig{}, map[string]string{"SCION_OTEL_ENDPOINT": v}, config.V1SubstrateConfig{})
			if err != nil {
				t.Fatalf("error = %v, want a drop", err)
			}
			if !slices.Equal(before, after) {
				t.Errorf("tenant value changed the rules: %+v vs %+v", after, before)
			}
			out := logs.String()
			if !strings.Contains(out, "source=SCION_OTEL_ENDPOINT") {
				t.Errorf("log = %q, want the warning naming the source", out)
			}
			for _, leak := range []string{"tok", "grpc", "://"} {
				if strings.Contains(out, leak) {
					t.Errorf("log = %q, leaks %q", out, leak)
				}
			}
		})
	}
}

// SEC-4: a tenant value covered by a built-in rule adds nothing and is not
// warned about as dropped.
func TestSubstrateEgressTargets_TenantCoveredByBuiltinNoWarning(t *testing.T) {
	logs := &syncBuffer{}
	setRuntimeLogHandler(t, slog.NewTextHandler(logs, nil))
	host := hardcodedModelEgressHosts[0]
	assertTenantOnlyUnchanged(t, RunConfig{}, config.V1SubstrateConfig{}, map[string]string{"ANTHROPIC_VERTEX_BASE_URL": "https://" + host + "/v1"})
	if out := logs.String(); strings.Contains(out, "dropping") {
		t.Errorf("log = %q, want no dropping warning for a host covered by a built-in rule", out)
	}
}

// N1: a cross-protocol tie names both sides' sources.
func TestSubstrateEgressTargets_TieNamesBothSources(t *testing.T) {
	_, err := substrateEgressTargets(RunConfig{TrustedHubEndpoint: "http://hub.example.com:8080"}, map[string]string{},
		config.V1SubstrateConfig{EgressAllow: []string{"api.example.com", "hub.example.com:8080"}})
	if err == nil {
		t.Fatal("error = nil, want the tie refusal")
	}
	for _, want := range []string{"trusted hub endpoint", "egress_allow entry 2", "http", "tls_passthrough", "hub.example.com:8080"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

// Design §9 AC9 N5-1 at Run level (review L3c, L5): an invalid
// CLOUD_ML_REGION refuses the Run before any control-plane call.
func TestSubstrateRun_InvalidVertexRegionRefusedBeforeCreate(t *testing.T) {
	rec := &callRecorder{}
	rt, _, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	cfg := testSubstrateRunConfig()
	cfg.Env = append(slices.Clone(cfg.Env), "CLAUDE_CODE_USE_VERTEX=1", "CLOUD_ML_REGION=Bad.Region")
	_, err := rt.Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run() error = nil, want the CLOUD_ML_REGION refusal")
	}
	if !strings.Contains(err.Error(), "CLOUD_ML_REGION") || strings.Contains(err.Error(), "Bad.Region") {
		t.Errorf("Run() error = %q, want it to name CLOUD_ML_REGION and not the value", err)
	}
	if calls := rec.list(); len(calls) != 0 {
		t.Errorf("Run() made control-plane calls %v before refusing", calls)
	}
}
