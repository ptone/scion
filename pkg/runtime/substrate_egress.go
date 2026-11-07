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
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	ateapi "github.com/GoogleCloudPlatform/scion/third_party/substrate/ateapi/v1"
)

// hardcodedModelEgressHosts are the harness model API hosts this runtime
// hardcodes (substrate-runtime.md §7): direct Anthropic API access, and the
// Google OAuth2 token endpoint every GCP-authenticated call (including
// Vertex) needs regardless of region. This deliberately does NOT include a
// "*.googleapis.com" wildcard: that covers every Google API — GCS, Compute,
// BigQuery, and hundreds more — not just the ones this runtime actually
// calls, and would let an actor exfiltrate to any GCS bucket or any
// project's Google API. The Vertex AI endpoint itself is added separately,
// by substrateEgressTargets, derived from the agent's own configured
// region (see addVertexAIHost) rather than hardcoded here, since the
// correct host depends on CLOUD_ML_REGION. The configured telemetry
// endpoint host is likewise added as its own rule from the actual env var
// in play, not assumed from this list — see the telemetry section below.
var hardcodedModelEgressHosts = []string{
	"api.anthropic.com",
	"oauth2.googleapis.com",
}

// vertexRegionLabelPattern matches a single valid DNS label (lowercase
// letters, digits, and interior hyphens only — RFC 1123, not starting or
// ending with a hyphen). CLOUD_ML_REGION must match this exactly, or
// addVertexAIHost refuses it: this is the security-load-bearing check for
// the whole function, not a formatting nicety. A region of "*" fed
// unchecked into "<region>-aiplatform.googleapis.com" would construct
// "*-aiplatform.googleapis.com" — a wildcard host, exactly the class of
// grant hardcodedModelEgressHosts's doc comment explains removing
// "*.googleapis.com" to avoid. A region containing "." could construct an
// extra label Google's own regional endpoints never have. Uppercase is
// refused rather than silently lowercased, since CLOUD_ML_REGION reaching
// here with the wrong case indicates a misconfiguration worth surfacing,
// not papering over.
var vertexRegionLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// addVertexAIHost adds the Vertex AI regional endpoint host
// ("<region>-aiplatform.googleapis.com", or the bare "aiplatform.googleapis.com"
// for the global/unset region) when the resolved env indicates Vertex AI
// auth is in play (CLAUDE_CODE_USE_VERTEX, set by
// pkg/harness/container_script_harness.go's vertex-ai translation, or
// ANTHROPIC_VERTEX_PROJECT_ID/CLOUD_ML_REGION directly, for a harness that
// sets them without going through that translation). Added via add (not
// addTenantHost): the region is agent/template-configurable, but once
// validated against vertexRegionLabelPattern the result always terminates
// in the fixed ".googleapis.com" suffix — a tenant can only choose which
// Google-owned regional endpoint is reached, never redirect to
// infrastructure outside Google's own, so this needs no egress_allow
// coverage gate, the same trust basis as the other entries in
// hardcodedModelEgressHosts. An invalid region is a config error, not a
// silent skip: returning one here must fail Run closed (see its caller),
// since silently dropping the Vertex host would leave a Vertex-configured
// agent unable to reach its model API at all, an availability failure
// that's easier to diagnose as a refused Run than as an opaque egress
// timeout.
//
// This covers ONLY the Claude harness's Vertex AI integration — the sole
// one substrate currently wires through container_script_harness.go's
// vertex-ai env translation (substrate-runtime.md §7). A harness with its
// own model-API host (Vertex-backed or otherwise) needs the same two-part
// treatment — a validated, fixed-Google-suffix host via add(), or a tenant
// host via addTenantHost's coverage gate — added here when that harness is
// wired for substrate.
func addVertexAIHost(add func(string), env map[string]string) error {
	usesVertex := env["CLAUDE_CODE_USE_VERTEX"] != "" ||
		env["ANTHROPIC_VERTEX_PROJECT_ID"] != "" ||
		env["CLOUD_ML_REGION"] != ""
	if !usesVertex {
		return nil
	}
	region := env["CLOUD_ML_REGION"]
	var host string
	if region == "" || region == "global" {
		host = "aiplatform.googleapis.com"
	} else {
		if !vertexRegionLabelPattern.MatchString(region) {
			return fmt.Errorf("CLOUD_ML_REGION %q is not a single valid DNS label; refusing to construct a Vertex AI egress host from it", region)
		}
		host = region + "-aiplatform.googleapis.com"
	}
	normalized, err := substrate.NormalizeEgressAllowEntry(host)
	if err != nil {
		return fmt.Errorf("constructed Vertex AI host %q failed validation: %w", host, err)
	}
	add(normalized)
	return nil
}

// substrateEgressTarget is one allowed egress destination: a host pattern
// on exactly one port, with the v1 rule kind it becomes. Never a port range
// and never all ports.
type substrateEgressTarget struct {
	protocol substrate.EgressProtocol
	host     string
	port     int32
}

// substrateHTTPSPort is the only port a host derived from anything other
// than the two operator-trusted sources may get.
const substrateHTTPSPort int32 = 443

// substrateEgressTargets collects the actor's egress destinations
// (substrate-runtime.md §7; design §3.5): the trusted hub endpoint, the git
// clone host, the telemetry hosts, the hardcoded harness model hosts and
// egress_allow from settings. The result is deduplicated and sorted, so
// buildEgressPolicy never emits two rules that tie.
//
// Ports and the cleartext http protocol come ONLY from two
// operator-trusted sources:
//
//  1. cfg.TrustedHubEndpoint, the hub endpoint resolved from broker
//     configuration (RunConfig's own doc comment; pkg/agent/run.go). An
//     http:// endpoint becomes an http target on its port (default 80);
//     https:// or a bare host becomes TLS passthrough on its port (default
//     443). It is not validated as a public hostname: an in-cluster hub
//     (e.g. "hub.scion-system.svc.cluster.local") is a legitimate,
//     common deployment shape that substrate.NormalizeEgressAllowEntry's
//     public-hostname grammar would reject outright. The FINAL env's own
//     SCION_HUB_ENDPOINT/SCION_HUB_URL, which an agent/template override
//     can change, is read only to detect and log a mismatch; it never
//     adds a target, on any port.
//  2. Operator egress_allow entries (substrate.NormalizeEgressAllowEntry, via substrate.ParseEgressAllow):
//     "host", "host:port", "https://host[:port]" or "http://host[:port]".
//     egress_allow is operator-only (substrate_operator_only.go).
//
// Every other host is TLS passthrough on 443 only:
//
//   - hardcodedModelEgressHosts and the Vertex AI host (fixed literals or a
//     validated, fixed-Google-suffix construction);
//   - tenant-controllable hosts: cfg.GitClone.URL/SCION_GIT_CLONE_URL,
//     every OTEL_*/SCION_OTEL_* endpoint and ANTHROPIC_VERTEX_BASE_URL.
//     Each goes through addTenantHost, which admits a host ONLY if an
//     operator egress_allow entry covers it on the same protocol and port
//     (substrate.EgressAllowCovers for the host: an exact match, or a
//     wildcard entry one label above it). An endpoint that is not covered
//     is dropped with a warning naming the egress_allow entry that would
//     allow it, never with the URL's userinfo or query.
//     Before the coverage check, the host must pass the same
//     substrate.NormalizeEgressAllowEntry validator operator entries use,
//     which also rejects every IP-literal/CIDR form.
//
// MITM ("https" rules) is not produced yet; every TLS target is passthrough.
func substrateEgressTargets(cfg RunConfig, env map[string]string, sc config.V1SubstrateConfig) ([]substrateEgressTarget, error) {
	seen := make(map[substrateEgressTarget]struct{})
	protoByHostPort := make(map[string]substrate.EgressProtocol)
	var targets []substrateEgressTarget
	add := func(t substrateEgressTarget) error {
		t.host = strings.ToLower(strings.TrimSpace(t.host))
		if t.host == "" {
			return nil
		}
		if _, ok := seen[t]; ok {
			return nil
		}
		key := fmt.Sprintf("%s:%d", t.host, t.port)
		if prev, ok := protoByHostPort[key]; ok && prev != t.protocol {
			return fmt.Errorf("egress destination %s is configured as both %s and %s; one host and port can have only one protocol", key, prev, t.protocol)
		}
		protoByHostPort[key] = t.protocol
		seen[t] = struct{}{}
		targets = append(targets, t)
		return nil
	}
	addHTTPS := func(h string) error {
		return add(substrateEgressTarget{protocol: substrate.EgressProtocolTLSPassthrough, host: h, port: substrateHTTPSPort})
	}

	// operatorEgressAllow is every sc.EgressAllow entry, parsed, computed
	// up front so addTenantHost can check coverage against it. A parse
	// error fails the Run (substrate.Validate at the top of Run already
	// refuses such a config, so this is a second check, never a skip).
	operatorEgressAllow, err := substrate.ParseEgressAllow(sc.EgressAllow)
	if err != nil {
		return nil, fmt.Errorf("runtimes.<name>.substrate.egress_allow: %w", err)
	}

	// covered reports whether an egress_allow entry on the same protocol
	// and port covers host.
	covered := func(protocol substrate.EgressProtocol, host string, port int32) bool {
		for _, t := range operatorEgressAllow {
			if t.Protocol == protocol && t.Port == port && substrate.EgressAllowCovers([]string{t.Host}, host) {
				return true
			}
		}
		return false
	}

	// addTenantHost admits a destination derived from tenant-controllable
	// input (raw is a URL or host[:port]; source names the setting for the
	// error) only if it is a well-formed public hostname AND covered by an
	// operator egress_allow entry on the same protocol and port.
	// Well-formedness alone is not enough: an attacker-chosen but
	// otherwise ordinary-looking public hostname (a service like
	// nip.io/sslip.io that resolves an embedded IP octet on request, or any
	// domain the tenant registers) would pass the grammar check while
	// letting the tenant point their own git-clone remote or telemetry
	// endpoint at an address of their choosing. Only the bare host is
	// logged or named in an error: never the userinfo, query, or full URL
	// it was derived from, either of which could carry a credential.
	//
	// This can therefore never WIDEN the policy beyond what sc.EgressAllow
	// already grants: every operator entry is added unconditionally below,
	// a covered destination only adds one already subsumed by an operator
	// entry on the same protocol and port, and an uncovered one adds
	// nothing.
	addTenantHost := func(source, raw string) error {
		scheme, h, port, ok := parseDerivedEndpoint(raw)
		if !ok {
			slog.Warn("substrate: could not parse a tenant-derived endpoint; no egress host added for it", "source", source)
			return nil
		}
		normalized, err := substrate.NormalizeEgressAllowEntry(h)
		if err != nil {
			slog.Warn("substrate: dropping invalid tenant-derived egress host", "host", h, "error", err)
			return nil
		}
		if normalized == "" {
			return nil
		}
		if scheme == "https" && port == substrateHTTPSPort {
			if !covered(substrate.EgressProtocolTLSPassthrough, normalized, port) {
				slog.Warn("substrate: dropping tenant-derived egress host not covered by egress_allow",
					"host", normalized, "hint", "add this host (or a wildcard covering it) to runtimes.<name>.substrate.egress_allow to permit it")
				return nil
			}
			return addHTTPS(normalized)
		}
		// Not https on 443: only an operator entry on the same protocol and
		// port may allow it; otherwise it is dropped with a warning naming
		// the egress_allow entry to add. The warning carries only the
		// normalized host, port and scheme, never the URL it came from.
		protocol := substrate.EgressProtocolTLSPassthrough
		hint := fmt.Sprintf("%s:%d", normalized, port)
		switch scheme {
		case "https":
		case "http":
			protocol = substrate.EgressProtocolHTTP
			hint = fmt.Sprintf("http://%s:%d", normalized, port)
		default:
			slog.Warn("substrate: dropping tenant-derived egress host with an unsupported scheme",
				"source", source, "host", normalized, "scheme", scheme,
				"hint", "only https on port 443, or an operator egress_allow entry (host:port or http://host:port) on the same port, can allow it")
			return nil
		}
		if !covered(protocol, normalized, port) {
			slog.Warn("substrate: dropping tenant-derived egress host not on https port 443 and not covered by egress_allow on its port",
				"source", source, "host", normalized, "port", port, "scheme", scheme,
				"hint", fmt.Sprintf("add %q to runtimes.<name>.substrate.egress_allow to permit it", hint))
			return nil
		}
		return add(substrateEgressTarget{protocol: protocol, host: normalized, port: port})
	}

	// The trusted hub endpoint: operator-trusted source (1), the only place
	// outside egress_allow that may yield an http target or a port other
	// than 443.
	if cfg.TrustedHubEndpoint != "" {
		scheme, trustedHubHost, port, ok := parseDerivedEndpoint(cfg.TrustedHubEndpoint)
		if !ok {
			slog.Warn("substrate: could not parse the trusted hub endpoint; no egress host added for it")
		} else {
			if substrate.IsIPLiteralHost(trustedHubHost) {
				// An IP-literal host, added unvalidated (see above on why
				// the trusted hub host skips NormalizeEgressAllowEntry),
				// including the short and mixed-radix inet_aton spellings
				// ("127.1", "0x7f000001", "2130706433") netip alone misses,
				// would otherwise reach CreateActorEgressPolicy and fail
				// only there: v1 egress rule hostnames do not accept IP
				// addresses. Fail here instead, as a clear config error.
				return nil, fmt.Errorf("substrate: trusted hub endpoint %q resolves to IP address %q; configure the hub endpoint with a DNS hostname — substrate's egress policy supports hostname patterns only, not IP addresses", cfg.TrustedHubEndpoint, trustedHubHost)
			}
			var t substrateEgressTarget
			switch scheme {
			case "http":
				t = substrateEgressTarget{protocol: substrate.EgressProtocolHTTP, host: trustedHubHost, port: port}
			case "https":
				t = substrateEgressTarget{protocol: substrate.EgressProtocolTLSPassthrough, host: trustedHubHost, port: port}
			default:
				return nil, fmt.Errorf("substrate: trusted hub endpoint host %q uses scheme %q; use an http:// or https:// hub endpoint", trustedHubHost, scheme)
			}
			if err := add(t); err != nil {
				return nil, fmt.Errorf("substrate: %w", err)
			}
		}
	}
	trustedHubHost := strings.ToLower(hostFromURL(cfg.TrustedHubEndpoint))
	if finalHubHost := strings.ToLower(hostFromURLEnv(env, "SCION_HUB_ENDPOINT", "SCION_HUB_URL")); finalHubHost != "" && finalHubHost != trustedHubHost {
		slog.Warn("substrate: ignoring agent env hub endpoint that does not match the operator-configured hub",
			"trusted_host", trustedHubHost, "env_host", finalHubHost)
	}

	if cfg.GitClone != nil && cfg.GitClone.URL != "" {
		if hostFromURL(cfg.GitClone.URL) != "" {
			if err := addTenantHost("git clone URL", cfg.GitClone.URL); err != nil {
				return nil, err
			}
		} else {
			// hostFromURL returns "" for a URL it cannot extract a hostname
			// from — notably an scp-style git remote ("git@host:org/r.git",
			// no "://"), which url.Parse does not accept as a URL at all.
			// This fails closed (no host is added, so the agent's own git
			// clone may fail with egress-denied rather than being silently
			// widened), logged so the failure mode is distinguishable.
			slog.Warn("substrate: could not extract a hostname from the configured git clone URL; no egress host added for it", "hint", "scp-style remotes (git@host:org/repo.git) are not supported here — use an https:// URL")
		}
	} else if v := firstEnv(env, "SCION_GIT_CLONE_URL"); v != "" {
		if err := addTenantHost("SCION_GIT_CLONE_URL", v); err != nil {
			return nil, err
		}
	}

	// The configured telemetry endpoint hosts (substrate-runtime.md §7). No
	// wildcard covers a default here (hardcodedModelEgressHosts carries no
	// "*.googleapis.com" entry): a self-hosted OTLP collector, the hub's
	// own endpoint, or the GCP default Cloud Trace exporter all need their
	// own explicit coverage. Checked independently, because a deployment
	// could point different signals at different collectors.
	for _, key := range []string{
		"SCION_OTEL_ENDPOINT",                // pkg/sciontool/telemetry, pkg/util/logging: scion's own var.
		"OTEL_EXPORTER_OTLP_ENDPOINT",        // OTel SDK standard var, if a harness sets it directly.
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", // OTel SDK per-signal override.
	} {
		if v := firstEnv(env, key); v != "" {
			if err := addTenantHost(key, v); err != nil {
				return nil, err
			}
		}
	}

	// ANTHROPIC_VERTEX_BASE_URL (or any other model base-URL override a
	// harness forwards) is NOT a fixed Google host the way
	// addVertexAIHost's constructed endpoint is: it is a URL an
	// agent/template can set outright, so it goes through addTenantHost's
	// coverage gate like the git-clone and telemetry hosts above.
	if v := firstEnv(env, "ANTHROPIC_VERTEX_BASE_URL"); v != "" {
		if err := addTenantHost("ANTHROPIC_VERTEX_BASE_URL", v); err != nil {
			return nil, err
		}
	}

	for _, h := range hardcodedModelEgressHosts {
		if err := addHTTPS(h); err != nil {
			return nil, err
		}
	}
	var vertexErr error
	if err := addVertexAIHost(func(h string) {
		if err := addHTTPS(h); err != nil && vertexErr == nil {
			vertexErr = err
		}
	}, env); err != nil {
		return nil, err
	}
	if vertexErr != nil {
		return nil, vertexErr
	}

	// Operator-trusted source (2): every egress_allow entry, exactly as
	// parsed and validated.
	for _, t := range operatorEgressAllow {
		if err := add(substrateEgressTarget{protocol: t.Protocol, host: t.Host, port: t.Port}); err != nil {
			return nil, fmt.Errorf("substrate: %w", err)
		}
	}

	sort.Slice(targets, func(i, j int) bool { return substrateEgressTargetLess(targets[i], targets[j]) })
	return targets, nil
}

// substrateEgressTargetLess orders targets by protocol, port, then host.
func substrateEgressTargetLess(a, b substrateEgressTarget) bool {
	if a.protocol != b.protocol {
		return a.protocol < b.protocol
	}
	if a.port != b.port {
		return a.port < b.port
	}
	return a.host < b.host
}

// parseDerivedEndpoint splits a URL or bare host[:port] into its lowercase
// scheme, lowercase host and port. A value with no scheme is https; a
// missing port is the scheme's default (443 for https, 80 for http, 0 for
// anything else). ok is false when no host can be extracted or the port is
// not a number from 1 to 65535.
func parseDerivedEndpoint(raw string) (scheme, host string, port int32, ok bool) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return "", "", 0, false
	}
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	u, err := url.Parse(candidate)
	if err != nil || u.Hostname() == "" {
		return "", "", 0, false
	}
	scheme = strings.ToLower(u.Scheme)
	switch p := u.Port(); {
	case p != "":
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil || n == 0 {
			return "", "", 0, false
		}
		port = int32(n)
	case scheme == "https":
		port = substrateHTTPSPort
	case scheme == "http":
		port = 80
	}
	return scheme, strings.ToLower(u.Hostname()), port, true
}

// firstEnv returns env[key] trimmed.
func firstEnv(env map[string]string, key string) string {
	return strings.TrimSpace(env[key])
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

// substrateEgressPolicyName is the only permitted name of an actor's v1
// egress policy resource (EgressPolicy.metadata.name, and the name field
// of Get/DeleteActorEgressPolicy).
const substrateEgressPolicyName = "default"

// substrateMaxRuleHostnames is v1's maxItems for one rule's hostnames.
const substrateMaxRuleHostnames = 256

// buildEgressPolicy builds the CreateActorEgressPolicyRequest for actor
// atespace/actorName (substrate-runtime.md §7; design §3.5). Targets are
// grouped into one rule per protocol and exact port, hostnames sorted, and
// rules ordered by protocol then port, so the same targets always give the
// same request. targets must come from substrateEgressTargets, which
// deduplicates them and refuses one host and port on two protocols, so no
// two rules can tie.
func buildEgressPolicy(atespace, actorName string, targets []substrateEgressTarget) *ateapi.CreateActorEgressPolicyRequest {
	type group struct {
		protocol substrate.EgressProtocol
		port     int32
	}
	hostsByGroup := make(map[group][]string)
	var groups []group
	for _, t := range targets {
		g := group{t.protocol, t.port}
		if _, ok := hostsByGroup[g]; !ok {
			groups = append(groups, g)
		}
		hostsByGroup[g] = append(hostsByGroup[g], t.host)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].protocol != groups[j].protocol {
			return groups[i].protocol < groups[j].protocol
		}
		return groups[i].port < groups[j].port
	})

	var rules []*ateapi.EgressRule
	for _, g := range groups {
		hosts := slices.Clone(hostsByGroup[g])
		slices.Sort(hosts)
		hosts = slices.Compact(hosts)
		for len(hosts) > 0 {
			n := min(len(hosts), substrateMaxRuleHostnames)
			chunk, ports := hosts[:n], &ateapi.Ports{Numbers: []int32{g.port}}
			hosts = hosts[n:]
			switch g.protocol {
			case substrate.EgressProtocolHTTP:
				rules = append(rules, &ateapi.EgressRule{Http: &ateapi.HttpRule{Hostnames: chunk, Ports: ports}})
			default:
				rules = append(rules, &ateapi.EgressRule{TlsPassthrough: &ateapi.TlsPassthroughRule{Hostnames: chunk, Ports: ports}})
			}
		}
	}

	return &ateapi.CreateActorEgressPolicyRequest{
		Actor: &ateapi.ObjectRef{Atespace: atespace, Name: actorName},
		EgressPolicy: &ateapi.EgressPolicy{
			Metadata: &ateapi.ResourceMetadata{Atespace: atespace, Name: substrateEgressPolicyName},
			Rules:    rules,
		},
	}
}
