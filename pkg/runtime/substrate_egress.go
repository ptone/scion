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
var vertexRegionLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// addVertexAIHost adds the Vertex AI regional endpoint host
// ("<region>-aiplatform.googleapis.com", or the bare "aiplatform.googleapis.com"
// for the global/unset region) when the resolved env indicates Vertex AI
// auth is in play (CLAUDE_CODE_USE_VERTEX, set by
// pkg/harness/container_script_harness.go's vertex-ai translation, or
// ANTHROPIC_VERTEX_PROJECT_ID/CLOUD_ML_REGION directly, for a harness that
// sets them without going through that translation). Added via add (not
// checkTenantHost): the region is agent/template-configurable, but once
// validated against vertexRegionLabelPattern the result always terminates
// in the fixed ".googleapis.com" suffix — a tenant can only choose which
// Google-owned regional endpoint is reached, never redirect to
// infrastructure outside Google's own, so this needs no egress_allow
// coverage gate, the same trust basis as the other entries in
// hardcodedModelEgressHosts. An invalid region adds no host and logs a
// warning; it never refuses the Run (design-addenda A4: binary-fixed hosts
// never refuse a Run).
//
// This covers ONLY the Claude harness's Vertex AI integration — the sole
// one substrate currently wires through container_script_harness.go's
// vertex-ai env translation (substrate-runtime.md §7). A harness with its
// own model-API host (Vertex-backed or otherwise) needs the same two-part
// treatment — a validated, fixed-Google-suffix host via add(), or a tenant
// host via checkTenantHost's coverage check — added here when that harness is
// wired for substrate.
func addVertexAIHost(add func(string), env map[string]string) {
	usesVertex := env["CLAUDE_CODE_USE_VERTEX"] != "" ||
		env["ANTHROPIC_VERTEX_PROJECT_ID"] != "" ||
		env["CLOUD_ML_REGION"] != ""
	if !usesVertex {
		return
	}
	region := env["CLOUD_ML_REGION"]
	var host string
	switch {
	case region == "":
		// Refused, with no rule (sec-review-3 condition on design-addenda
		// A4): the host is only ever built from one validated label.
		slog.Warn("substrate: CLOUD_ML_REGION is empty; no Vertex AI egress host added")
		return
	case region == "global":
		host = "aiplatform.googleapis.com"
	default:
		if !vertexRegionLabelPattern.MatchString(region) {
			// The region value is not logged: it is agent input.
			slog.Warn("substrate: CLOUD_ML_REGION is not a single valid DNS label; no Vertex AI egress host added")
			return
		}
		host = region + "-aiplatform.googleapis.com"
	}
	normalized, err := substrate.NormalizeEgressAllowEntry(host)
	if err != nil {
		slog.Warn("substrate: constructed Vertex AI host failed validation; no Vertex AI egress host added", "error", err)
		return
	}
	add(normalized.Host)
}

// addBinaryFixedHosts adds the BINARY-FIXED source class (design-addenda
// A4): hardcodedModelEgressHosts, and the Vertex AI regional host (one
// validated label under the fixed googleapis.com suffix). Moving the Vertex
// host to the tenant-derived class is a one-line edit here.
func addBinaryFixedHosts(addFixed func(string), env map[string]string) {
	for _, h := range hardcodedModelEgressHosts {
		addFixed(h)
	}
	addVertexAIHost(addFixed, env)
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
// (substrate-runtime.md §7; design §3.5). The result is deduplicated and
// sorted, so buildEgressPolicy never emits two rules that tie.
//
// Three source classes (design-addenda A4):
//
//   - OPERATOR-TRUSTED: (1) and (2) below;
//   - BINARY-FIXED: (3) below (addBinaryFixedHosts);
//   - TENANT-DERIVED: the hosts checkTenantHost checks; they never emit a
//     rule.
//
// Rules come only from:
//
//  1. cfg.TrustedHubEndpoint (operator-trusted), resolved (unchanged by
//     this code) from the operator-derived tiers only, never from agent,
//     template, harness or project settings or env (RunConfig's own doc
//     comment; pkg/agent/run.go). An http:// endpoint becomes an http
//     target on its port (default 80); https:// or a bare host becomes TLS
//     passthrough on its port (default 443). It is not validated as a
//     public hostname: an in-cluster hub (e.g.
//     "hub.scion-system.svc.cluster.local") is a legitimate, common
//     deployment shape that substrate.NormalizeEgressAllowEntry's
//     public-hostname grammar would reject outright.
//  2. Operator egress_allow entries (operator-trusted), validated by
//     substrate.NormalizeEgressAllowEntry (via substrate.ParseEgressAllow):
//     "host", "host:port", "https://host[:port]" or "http://host[:port]".
//     A bare host narrows from any port (the pre-1.0 API) to 443, by
//     design. egress_allow is operator-only (substrate_operator_only.go).
//  3. BINARY-FIXED, the hosts fixed in this binary:
//     hardcodedModelEgressHosts and the Vertex AI host (a validated region
//     label under the fixed googleapis.com suffix), TLS passthrough on 443.
//     They never refuse a Run: when an operator-trusted source already has
//     the same host and port under another protocol, the fixed host is
//     skipped with a warning, so the operator's choice stands; an invalid
//     region adds no Vertex host.
//
// The same host and port from two operator-trusted sources under different
// protocols is a config refusal (v1 would rank them as a tie).
//
// Tenant-derived hosts (cfg.GitClone.URL/SCION_GIT_CLONE_URL, every
// OTEL_*/SCION_OTEL_* endpoint, ANTHROPIC_VERTEX_BASE_URL) NEVER emit a
// rule and never refuse a Run. Each is checked against the
// operator-trusted rules, host-, port- and protocol-aware
// (substrate.EgressAllowCovers for the host: an exact match or a wildcard
// one label above it): covered means the operator rule already admits it
// and nothing is added; not covered means it is dropped with a warning
// naming the egress_allow entry that would allow it, never with the URL's
// userinfo or query. The host must also pass
// substrate.NormalizeEgressAllowEntry's host checks, which reject every
// IP-literal/CIDR form; an invalid host is dropped with a warning. The
// final env's own SCION_HUB_ENDPOINT/SCION_HUB_URL is read only to log a
// mismatch with the trusted hub; it is never a rule.
//
// MITM ("https" rules) is not produced yet; every TLS target is passthrough.
func substrateEgressTargets(cfg RunConfig, env map[string]string, sc config.V1SubstrateConfig) ([]substrateEgressTarget, error) {
	seen := make(map[substrateEgressTarget]struct{})
	protoByHostPort := make(map[string]substrate.EgressProtocol)
	var targets []substrateEgressTarget
	// conflict reports the protocol already chosen for t's host and port,
	// when it differs from t's.
	conflict := func(t substrateEgressTarget) (substrate.EgressProtocol, bool) {
		prev, ok := protoByHostPort[fmt.Sprintf("%s:%d", t.host, t.port)]
		return prev, ok && prev != t.protocol
	}
	record := func(t substrateEgressTarget) {
		if _, ok := seen[t]; ok {
			return
		}
		protoByHostPort[fmt.Sprintf("%s:%d", t.host, t.port)] = t.protocol
		seen[t] = struct{}{}
		targets = append(targets, t)
	}
	// addOperator adds an operator-trusted target; a protocol conflict is a
	// config refusal.
	addOperator := func(t substrateEgressTarget) error {
		if prev, bad := conflict(t); bad {
			return fmt.Errorf("substrate: egress destination %s:%d is configured as both %s and %s; one host and port can have only one protocol", t.host, t.port, prev, t.protocol)
		}
		record(t)
		return nil
	}
	// addFixed adds a host fixed in this binary on TLS 443, skipped with a
	// warning when an operator-trusted source chose another protocol.
	addFixed := func(h string) {
		t := substrateEgressTarget{protocol: substrate.EgressProtocolTLSPassthrough, host: strings.ToLower(strings.TrimSpace(h)), port: substrateHTTPSPort}
		if t.host == "" {
			return
		}
		if prev, bad := conflict(t); bad {
			slog.Warn("substrate: not adding a built-in egress host; an operator-trusted rule uses another protocol on the same port",
				"host", t.host, "port", t.port, "operator_protocol", prev)
			return
		}
		record(t)
	}

	// Operator-trusted source (2), parsed up front. A parse error fails the
	// Run (substrate.Validate at the top of Run already refuses such a
	// config, so this is a second check, never a skip).
	operatorEgressAllow, err := substrate.ParseEgressAllow(sc.EgressAllow)
	if err != nil {
		return nil, fmt.Errorf("runtimes.<name>.substrate.egress_allow: %w", err)
	}

	// Operator-trusted source (1): the trusted hub endpoint, the only place
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
			if err := addOperator(t); err != nil {
				return nil, err
			}
		}
	}
	for _, e := range operatorEgressAllow {
		if err := addOperator(substrateEgressTarget{protocol: e.Protocol(), host: e.Host, port: e.Port}); err != nil {
			return nil, err
		}
	}
	// operatorTargets are the operator-trusted rules tenant hosts are
	// checked against.
	operatorTargets := slices.Clone(targets)

	trustedHubHost := hostFromURL(cfg.TrustedHubEndpoint)
	if finalHubHost := hostFromURLEnv(env, "SCION_HUB_ENDPOINT", "SCION_HUB_URL"); finalHubHost != "" && finalHubHost != trustedHubHost {
		slog.Warn("substrate: ignoring agent env hub endpoint that does not match the operator-configured hub",
			"trusted_host", trustedHubHost, "env_host", finalHubHost)
	}

	// Source class BINARY-FIXED.
	addBinaryFixedHosts(addFixed, env)

	// covered reports whether an operator-trusted rule with the same
	// protocol and port covers host.
	covered := func(protocol substrate.EgressProtocol, host string, port int32) bool {
		for _, t := range operatorTargets {
			if t.protocol == protocol && t.port == port && substrate.EgressAllowCovers([]string{t.host}, host) {
				return true
			}
		}
		return false
	}

	// checkTenantHost checks a destination derived from tenant-controllable
	// input (raw is a URL or host[:port]; source names the setting). It
	// never adds a rule: covered by an operator-trusted rule on the same
	// protocol and port means it is already admitted; anything else is
	// dropped with a warning naming the egress_allow entry to add. The
	// need maps like the translation: http:// is an http need on its port
	// (default 80); https:// or bare is a passthrough need (default 443).
	// Tenant values are reduced to scheme, host and port
	// (parseDerivedEndpoint) before parsing or logging; parser errors never
	// echo userinfo or query.
	// Well-formedness alone would not be enough to admit it: an
	// attacker-chosen but ordinary-looking public hostname (a service like
	// nip.io/sslip.io that resolves an embedded IP octet on request, or any
	// domain the tenant registers) would pass the grammar check. Only the
	// normalized host, port and scheme are logged: never the userinfo,
	// query, or full URL it was derived from, either of which could carry
	// a credential.
	checkTenantHost := func(source, raw string) {
		scheme, h, port, ok := parseDerivedEndpoint(raw)
		if !ok {
			slog.Warn("substrate: could not parse a tenant-derived endpoint; no egress host added for it", "source", source)
			return
		}
		entry, err := substrate.NormalizeEgressAllowEntry(h)
		if err != nil {
			slog.Warn("substrate: dropping invalid tenant-derived egress host", "source", source, "host", h, "error", err)
			return
		}
		if entry.IsZero() {
			return
		}
		host := entry.Host
		var protocol substrate.EgressProtocol
		var hint string
		switch {
		case scheme == "https" && port == substrateHTTPSPort:
			protocol, hint = substrate.EgressProtocolTLSPassthrough, host
		case scheme == "https":
			protocol, hint = substrate.EgressProtocolTLSPassthrough, fmt.Sprintf("%s:%d", host, port)
		case scheme == "http":
			protocol, hint = substrate.EgressProtocolHTTP, fmt.Sprintf("http://%s:%d", host, port)
		default:
			slog.Warn("substrate: dropping tenant-derived egress host with an unsupported scheme",
				"source", source, "host", host, "scheme", scheme,
				"hint", "only an operator egress_allow entry (host[:port] or http://host[:port]) on the same port can allow it")
			return
		}
		if covered(protocol, host, port) {
			return
		}
		slog.Warn("substrate: dropping tenant-derived egress host not covered by egress_allow",
			"source", source, "host", host, "port", port, "scheme", scheme,
			"hint", fmt.Sprintf("add %q (or a wildcard covering it on that port) to runtimes.<name>.substrate.egress_allow to permit it", hint))
	}

	if cfg.GitClone != nil && cfg.GitClone.URL != "" {
		if hostFromURL(cfg.GitClone.URL) != "" {
			checkTenantHost("git clone URL", cfg.GitClone.URL)
		} else {
			// hostFromURL returns "" for a URL it cannot extract a hostname
			// from — notably an scp-style git remote ("git@host:org/r.git",
			// no "://"), whose "org" is not a port.
			// Nothing is admitted for it, logged so the failure mode is
			// distinguishable.
			slog.Warn("substrate: could not extract a hostname from the configured git clone URL; no egress host added for it", "hint", "scp-style remotes (git@host:org/repo.git) are not supported here — use an https:// URL")
		}
	} else if v := firstEnv(env, "SCION_GIT_CLONE_URL"); v != "" {
		checkTenantHost("SCION_GIT_CLONE_URL", v)
	}

	// The configured telemetry endpoint hosts (substrate-runtime.md §7). No
	// wildcard covers a default here (hardcodedModelEgressHosts carries no
	// "*.googleapis.com" entry). Checked independently, because a
	// deployment could point different signals at different collectors.
	for _, key := range []string{
		"SCION_OTEL_ENDPOINT",                // pkg/sciontool/telemetry, pkg/util/logging: scion's own var.
		"OTEL_EXPORTER_OTLP_ENDPOINT",        // OTel SDK standard var, if a harness sets it directly.
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", // OTel SDK per-signal override.
	} {
		if v := firstEnv(env, key); v != "" {
			checkTenantHost(key, v)
		}
	}

	// ANTHROPIC_VERTEX_BASE_URL (or any other model base-URL override a
	// harness forwards) is NOT a fixed Google host the way
	// addVertexAIHost's constructed endpoint is: it is a URL an
	// agent/template can set outright, so it is a tenant-derived host.
	if v := firstEnv(env, "ANTHROPIC_VERTEX_BASE_URL"); v != "" {
		checkTenantHost("ANTHROPIC_VERTEX_BASE_URL", v)
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

// parseDerivedEndpoint reduces a URL or bare host[:port] to its lowercase
// scheme, lowercase host and port, and nothing else: userinfo, path, query
// and fragment are cut off before anything is parsed or logged. The scheme
// is detected only by a literal "http://" or "https://" prefix (any other
// "<name>://" yields that name, which callers refuse); a value with no
// scheme is https. A missing port is the scheme's default (443 for https,
// 80 for http, 0 for anything else). An IPv6 literal must be bracketed. ok
// is false when no host can be extracted or the port is not a number from
// 1 to 65535. No URL parser is used.
func parseDerivedEndpoint(raw string) (scheme, host string, port int32, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", 0, false
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "https://"):
		scheme, s = "https", s[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		scheme, s = "http", s[len("http://"):]
	case strings.Contains(s, "://"):
		i := strings.Index(s, "://")
		scheme, s = lower[:i], s[i+len("://"):]
	default:
		scheme = "https"
	}
	// The authority ends at the first "/", "?" or "#"; userinfo ends at
	// the last "@" before it.
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	var p string
	hasPort := false
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", 0, false
		}
		host = s[1:end]
		switch after := s[end+1:]; {
		case after == "":
		case strings.HasPrefix(after, ":"):
			p, hasPort = after[1:], true
		default:
			return "", "", 0, false
		}
	} else if h, pp, found := strings.Cut(s, ":"); found {
		if strings.Contains(pp, ":") {
			return "", "", 0, false
		}
		host, p, hasPort = h, pp, true
	} else {
		host = s
	}
	if host == "" {
		return "", "", 0, false
	}
	switch {
	case hasPort:
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
	return scheme, strings.ToLower(host), port, true
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

// hostFromURL extracts the lowercase hostname from a URL or bare
// host[:port] value (see parseDerivedEndpoint), or "".
func hostFromURL(raw string) string {
	_, host, _, ok := parseDerivedEndpoint(raw)
	if !ok {
		return ""
	}
	return host
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
