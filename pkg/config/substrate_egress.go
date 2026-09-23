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

package config

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// egressAllowCatchAllValues are exact egress_allow entries (compared after
// normalizeEgressAllowEntry) that would authorize traffic to any
// destination.
var egressAllowCatchAllValues = map[string]bool{
	"all":       true,
	"*":         true,
	"0.0.0.0/0": true,
	"::/0":      true,
}

// egressAllowBlockedCIDRs are private, carrier-grade-NAT, link-local,
// loopback, unspecified, reserved, and IPv4-in-IPv6 transition ranges that
// an egress_allow entry must never overlap. These are the ranges most
// likely to reach the Substrate control plane itself (the atenet-router,
// ateapi, or other in-cluster/in-VPC services) rather than a genuine
// external dependency.
var egressAllowBlockedCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"169.254.0.0/16",
	"127.0.0.0/8",
	"0.0.0.0/8",   // "this network"/unspecified-ish; 0.0.0.0 reaches localhost on Linux.
	"240.0.0.0/4", // Class E / reserved — GKE can use this range for pod CIDRs (review round 2, O2).
	"fc00::/7",
	"fe80::/10",
	"::1/128",
	"::/128",       // the IPv6 unspecified address ("::").
	"2002::/16",    // 6to4, embeds IPv4 space (review round 2, O2).
	"64:ff9b::/96", // NAT64, embeds IPv4 space (review round 2, O2).
}

// egressAllowBlockedHostSuffixes are hostname suffixes that always resolve
// inside the cluster (or, for .local, via mDNS/a local search domain),
// regardless of what CIDR range backs them at any given moment.
var egressAllowBlockedHostSuffixes = []string{
	".svc",
	".cluster.local",
	".internal",
	".local",
}

// egressAllowKnownNamespaces are Kubernetes namespace names common enough
// that a hostname ending in one of them is far more likely to be a cluster
// short name (resolved through the pod's DNS search path, e.g.
// "atenet-router.ate-system" or "kubernetes.default") than a genuine public
// hostname. This can't enumerate every deployment's actual broker/worker
// namespace names — V1SubstrateConfig doesn't carry them — so it is backed
// up by the more general "no public TLD is hyphenated" heuristic below.
var egressAllowKnownNamespaces = map[string]bool{
	"default":         true,
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
	"ate-system":      true,
}

// numericIPAliasPattern matches strings that look like an attempt to write
// an IP address in a non-canonical form net.ParseIP doesn't accept: hex
// ("0x7f000001"), bare decimal ("2130706433"), or a partial dotted-quad
// ("127.1"). Such a string would otherwise fall through to the hostname
// path — where it can't match any blocked suffix — and be accepted, even
// though some HTTP/network stacks do interpret these as IP addresses. Since
// whether Substrate's egress gateway does is unknown, they're rejected
// defensively rather than let through as an ordinary hostname (review round
// 2, Required R2, fix step 3).
var numericIPAliasPattern = regexp.MustCompile(`^(0x[0-9a-f]+|[0-9]+(\.[0-9]+){0,3})$`)

// Validate rejects an egress_allow list that would let a Substrate actor
// reach the router, other in-cluster services, or "everything" through its
// EgressPolicy. See ValidateEgressAllow for the exact rules.
//
// Threat model: the Phase 1 bootstrap nonce fallback (phase1-spec.md §5)
// trusts whichever caller reaches the control server's /bootstrap endpoint
// first. The NetworkPolicy restricting router ingress to the broker
// namespace is the primary defense against an unauthorized bootstrap; this
// validation is the second layer, keeping a legitimately-bootstrapped actor
// from being able to reach the router (and thereby bootstrap or exec
// against *other* actors) or other in-cluster services via its own egress,
// regardless of what an operator puts in egress_allow.
func (s *V1SubstrateConfig) Validate() error {
	if s == nil {
		return nil
	}
	if err := ValidateEgressAllow(s.EgressAllow); err != nil {
		return fmt.Errorf("runtimes.<name>.substrate.egress_allow: %w", err)
	}
	return nil
}

// ValidateEgressAllow rejects egress_allow entries that are a catch-all,
// overlap a private/in-cluster/link-local/loopback/reserved IP range,
// target a Kubernetes-internal or local DNS suffix, are a bare
// single-label or cluster-short-name hostname, or use a non-canonical IP
// spelling (host:port, brackets, a zone ID, or a numeric alias). Empty
// entries are ignored. The returned error names the offending entry (as
// originally written, before normalization).
func ValidateEgressAllow(entries []string) error {
	for _, raw := range entries {
		normalized := normalizeEgressAllowEntry(raw)
		if normalized == "" {
			continue
		}
		if err := validateEgressAllowEntry(raw, normalized); err != nil {
			return err
		}
	}
	return nil
}

// normalizeEgressAllowEntry trims whitespace, lowercases, and strips one
// trailing '.' (a syntactically valid FQDN terminator that would otherwise
// defeat suffix matching, e.g.
// "atenet-router.ate-system.svc.cluster.local." — review round 2, Required
// R2, fix step 1).
func normalizeEgressAllowEntry(raw string) string {
	e := strings.TrimSpace(raw)
	e = strings.ToLower(e)
	e = strings.TrimSuffix(e, ".")
	return e
}

// validateEgressAllowEntry validates the already-normalized entry,
// reporting errors against raw (the original, unnormalized text) so the
// operator sees what they actually wrote.
func validateEgressAllowEntry(raw, entry string) error {
	if egressAllowCatchAllValues[entry] {
		return fmt.Errorf("egress_allow entry %q is a catch-all and is not permitted; name specific hosts or CIDRs instead", raw)
	}
	if isBareWildcard(entry) {
		return fmt.Errorf("egress_allow entry %q is a catch-all and is not permitted; name specific hosts or CIDRs instead", raw)
	}

	// Brackets, and '%' zone IDs, are not valid in any form this validator
	// accepts (a bare IPv6 address, a CIDR, or a hostname) — reject
	// unconditionally rather than let them slip through whichever branch
	// below happens not to choke on them (review round 2, fix step 2).
	if strings.ContainsAny(entry, "[]%") {
		return fmt.Errorf("egress_allow entry %q contains an unsupported character (brackets or a zone id) and is not permitted", raw)
	}

	if strings.Contains(entry, "/") {
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return fmt.Errorf("egress_allow entry %q is not a valid CIDR: %w", raw, err)
		}
		return checkNetworkOverlap(raw, network)
	}

	if strings.Contains(entry, ":") {
		// Contains a colon but isn't a CIDR (handled above): must be a bare
		// IPv6 address, or it's malformed — e.g. "10.0.0.1:443"
		// (host:port) or a zone ID already caught above. Letting this fall
		// through to the hostname path would accept it, since colons never
		// occur in valid DNS names either (fix step 2).
		ip := net.ParseIP(entry)
		if ip == nil {
			return fmt.Errorf("egress_allow entry %q contains ':' but is not a valid IPv6 address; host:port and zone-ID forms are not accepted", raw)
		}
		return checkNetworkOverlap(raw, hostNetwork(ip))
	}

	if ip := net.ParseIP(entry); ip != nil {
		return checkNetworkOverlap(raw, hostNetwork(ip))
	}

	if numericIPAliasPattern.MatchString(entry) {
		return fmt.Errorf("egress_allow entry %q looks like a non-canonical IP address (hex, decimal, or a partial dotted-quad); use a canonical IPv4/IPv6 address or CIDR instead", raw)
	}

	// Hostname path.
	labels := strings.Split(entry, ".")
	if len(labels) < 2 {
		return fmt.Errorf("egress_allow entry %q is a single-label hostname, which can resolve through a cluster or local DNS search domain; use a fully-qualified hostname", raw)
	}
	for _, suffix := range egressAllowBlockedHostSuffixes {
		if strings.HasSuffix(entry, suffix) {
			return fmt.Errorf("egress_allow entry %q targets an in-cluster or local DNS suffix (%q) and is not permitted", raw, suffix)
		}
	}
	lastLabel := labels[len(labels)-1]
	if egressAllowKnownNamespaces[lastLabel] {
		return fmt.Errorf("egress_allow entry %q ends in %q, a well-known Kubernetes namespace name — this looks like a cluster-internal short name, not a public hostname", raw, lastLabel)
	}
	if strings.Contains(lastLabel, "-") && !strings.HasPrefix(lastLabel, "xn--") {
		return fmt.Errorf("egress_allow entry %q ends in %q, which is not a valid public top-level domain (hyphenated, and not IDNA punycode) — this looks like a cluster-internal short name", raw, lastLabel)
	}

	return nil
}

// isBareWildcard reports whether entry consists only of '*' and '.'
// characters (e.g. "*", "**", "*.*", ".*"), which match everything or close
// to it. A legitimate single-label wildcard such as "*.example.com" is not
// bare — it has a non-wildcard, non-dot suffix — and is left to the
// router's own HostnameRule semantics (one leading wildcard label, matching
// exactly one label).
func isBareWildcard(entry string) bool {
	if !strings.Contains(entry, "*") {
		return false
	}
	return strings.Trim(entry, "*.") == ""
}

// hostNetwork returns the exact-match network for a single IP: /32 for
// IPv4, /128 for IPv6.
func hostNetwork(ip net.IP) *net.IPNet {
	bits := 128
	if ip.To4() != nil {
		bits = 32
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
}

// checkNetworkOverlap returns an error naming raw and the first blocked
// range network overlaps, or nil if it overlaps none of them.
func checkNetworkOverlap(raw string, network *net.IPNet) error {
	for _, blockedStr := range egressAllowBlockedCIDRs {
		// blockedStr is a package constant; the error is unreachable.
		_, blocked, _ := net.ParseCIDR(blockedStr)
		if networksOverlap(network, blocked) {
			return fmt.Errorf("egress_allow entry %q overlaps the private/in-cluster range %s and is not permitted", raw, blockedStr)
		}
	}
	return nil
}

// networksOverlap reports whether a and b share any address: either
// contains the other's base address. Sufficient for two well-formed CIDRs
// regardless of which is larger — including IPv4-mapped IPv6 addresses,
// which net.IPNet.Contains normalises before comparing.
func networksOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}
