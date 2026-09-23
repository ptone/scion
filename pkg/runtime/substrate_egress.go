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
	"net/url"
	"sort"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
)

// hardcodedModelEgressHosts are the harness model API hosts Phase 1
// hardcodes (phase1-spec.md §2.2 step 5): Anthropic, plus Google auth and
// Vertex. "*.googleapis.com" also happens to cover the telemetry default
// (cloudtrace.googleapis.com, findings.md §2), but substrateEgressHostnames
// adds the actual configured telemetry endpoint as its own rule too — see
// the telemetry section below.
var hardcodedModelEgressHosts = []string{
	"api.anthropic.com",
	"oauth2.googleapis.com",
	"*.googleapis.com",
}

// substrateEgressHostnames collects the hostname patterns for the actor's
// EgressPolicy (phase1-spec.md §2.2 step 5): the hub endpoint host, the git
// clone host, the hardcoded harness model hosts, and egress_allow from
// settings. Duplicates are removed; empty/unresolvable hosts are skipped
// rather than failing Run — a missing hub or git host here means the
// operator will see egress-denied failures on the cluster, which is
// diagnosable, rather than Run refusing to create the actor at all.
func substrateEgressHostnames(cfg RunConfig, env map[string]string, sc config.V1SubstrateConfig) []string {
	seen := make(map[string]struct{})
	var hosts []string
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" {
			return
		}
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		hosts = append(hosts, h)
	}

	if h := hostFromURLEnv(env, "SCION_HUB_ENDPOINT", "SCION_HUB_URL"); h != "" {
		add(h)
	}

	if cfg.GitClone != nil && cfg.GitClone.URL != "" {
		if h := hostFromURL(cfg.GitClone.URL); h != "" {
			add(h)
		}
	} else if h := hostFromURLEnv(env, "SCION_GIT_CLONE_URL"); h != "" {
		add(h)
	}

	// Telemetry endpoint host (phase1-spec.md §2.2 step 5: "the telemetry
	// endpoint"). *.googleapis.com below happens to cover the Cloud Trace
	// default (findings.md §2), but that's a coincidence of the default,
	// not a rule: a self-hosted OTLP collector or the hub's own endpoint
	// needs its own rule, so check every env var scion's telemetry stack
	// actually uses rather than relying on the wildcard. Checked
	// independently, not via a single hostFromURLEnv call, because a
	// deployment could point different signals at different collectors.
	for _, key := range []string{
		"SCION_OTEL_ENDPOINT",                // pkg/sciontool/telemetry, pkg/util/logging: scion's own var.
		"OTEL_EXPORTER_OTLP_ENDPOINT",        // OTel SDK standard var, if a harness sets it directly.
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", // OTel SDK per-signal override.
	} {
		if h := hostFromURLEnv(env, key); h != "" {
			add(h)
		}
	}

	for _, h := range hardcodedModelEgressHosts {
		add(h)
	}

	for _, h := range sc.EgressAllow {
		// Send exactly the string ValidateEgressAllow validated, not just a
		// trimmed one: NormalizeEgressAllowEntry validates AND returns the
		// canonical form in one call, precisely so the two can never drift
		// apart (rounds 3 and 4 of review each found a "validate what you
		// send" gap of that shape). An entry like "GitHub.COM." validates
		// fine but would be rejected by Substrate's API if sent
		// unnormalized, since HostnameRule requires a lowercase name with
		// no trailing dot.
		//
		// The error is ignored here, not silently: r.cfg.Validate() (called
		// at the top of Run, before this is ever reached) already ran every
		// sc.EgressAllow entry through this exact function, so an error
		// here would mean Run's own guard was bypassed. Skip rather than
		// panic or fail Run a second time for something that should be
		// unreachable.
		if normalized, err := config.NormalizeEgressAllowEntry(h); err == nil {
			add(normalized)
		}
	}

	sort.Strings(hosts)
	return hosts
}

// hostFromURLEnv returns the hostname portion of the first non-empty env
// value found among keys, or "" if none are set or parseable.
func hostFromURLEnv(env map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := env[k]; v != "" {
			if h := hostFromURL(v); h != "" {
				return h
			}
		}
	}
	return ""
}

// hostFromURL extracts the hostname from a URL string, tolerating bare
// host[:port] values (no scheme) as well as full URLs.
func hostFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	candidate := raw
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	u, err := url.Parse(candidate)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return u.Hostname()
}

// buildEgressPolicy builds the CreateActorEgressPolicyRequest for actor
// atespace/actorName, with a single EgressRule matching any of hostnames
// (phase1-spec.md §2.2 step 5). "default" is the only permitted egress
// policy resource name per the EgressPolicy proto comment.
func buildEgressPolicy(atespace, actorName string, hostnames []string) *ateapipb.CreateActorEgressPolicyRequest {
	return &ateapipb.CreateActorEgressPolicyRequest{
		Actor: &ateapipb.ObjectRef{Atespace: atespace, Name: actorName},
		EgressPolicy: &ateapipb.EgressPolicy{
			Metadata: &ateapipb.ResourceMetadata{Atespace: atespace, Name: "default"},
			Rules: []*ateapipb.EgressRule{
				{
					Hostnames: &ateapipb.HostnameRule{Patterns: hostnames},
				},
			},
		},
	}
}
