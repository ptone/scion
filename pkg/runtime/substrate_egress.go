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
// Vertex. "*.googleapis.com" also covers the telemetry default
// (cloudtrace.googleapis.com, findings.md §2) so no separate telemetry rule
// is added unless egress_allow names one explicitly.
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

	for _, h := range hardcodedModelEgressHosts {
		add(h)
	}

	for _, h := range sc.EgressAllow {
		add(h)
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
