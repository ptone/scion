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
	"strings"
)

// egressAllowCatchAllValues are exact (case-insensitive) egress_allow
// entries that would authorize traffic to any destination.
var egressAllowCatchAllValues = map[string]bool{
	"all":       true,
	"*":         true,
	"0.0.0.0/0": true,
	"::/0":      true,
}

// egressAllowBlockedCIDRs are private, carrier-grade-NAT, link-local, and
// loopback ranges (RFC 1918, RFC 6598, RFC 3927, RFC 5735, and their IPv6
// equivalents) that an egress_allow entry must never overlap. These are the
// ranges most likely to reach the Substrate control plane itself (the
// atenet-router, ateapi, or other in-cluster/in-VPC services) rather than a
// genuine external dependency.
var egressAllowBlockedCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"169.254.0.0/16",
	"127.0.0.0/8",
	"fc00::/7",
	"fe80::/10",
	"::1/128",
}

// egressAllowBlockedHostSuffixes are hostname suffixes that always resolve
// inside the cluster, regardless of what CIDR range backs them at any given
// moment.
var egressAllowBlockedHostSuffixes = []string{
	".svc",
	".cluster.local",
	".internal",
}

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
// overlap a private/in-cluster/link-local/loopback IP range, or target a
// Kubernetes-internal DNS suffix. Empty entries are ignored. The returned
// error names the offending entry.
func ValidateEgressAllow(entries []string) error {
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if err := validateEgressAllowEntry(raw, entry); err != nil {
			return err
		}
	}
	return nil
}

func validateEgressAllowEntry(raw, entry string) error {
	lower := strings.ToLower(entry)

	if egressAllowCatchAllValues[lower] {
		return fmt.Errorf("egress_allow entry %q is a catch-all and is not permitted; name specific hosts or CIDRs instead", raw)
	}
	if isBareWildcard(entry) {
		return fmt.Errorf("egress_allow entry %q is a catch-all and is not permitted; name specific hosts or CIDRs instead", raw)
	}

	if network := parseEntryAsNetwork(entry); network != nil {
		for _, blockedStr := range egressAllowBlockedCIDRs {
			// blockedStr is a package constant; the error is unreachable.
			_, blocked, _ := net.ParseCIDR(blockedStr)
			if networksOverlap(network, blocked) {
				return fmt.Errorf("egress_allow entry %q overlaps the private/in-cluster range %s and is not permitted", raw, blockedStr)
			}
		}
		return nil
	}

	for _, suffix := range egressAllowBlockedHostSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return fmt.Errorf("egress_allow entry %q targets an in-cluster DNS suffix (%q) and is not permitted", raw, suffix)
		}
	}
	return nil
}

// isBareWildcard reports whether entry consists only of '*' and '.'
// characters (e.g. "*", "**", "*.*", ".*"), which match everything or close
// to it. A legitimate single-label wildcard such as "*.example.com" is not
// bare — it has a non-wildcard, non-dot suffix — and is left to the router's
// own HostnameRule semantics (one leading wildcard label, matching exactly
// one label).
func isBareWildcard(entry string) bool {
	if !strings.Contains(entry, "*") {
		return false
	}
	return strings.Trim(entry, "*.") == ""
}

// parseEntryAsNetwork parses entry as a CIDR ("10.0.0.0/8") or a bare IP
// literal ("10.0.0.5", treated as a host route: /32 for IPv4, /128 for
// IPv6). Returns nil if entry is neither — i.e. it's a hostname pattern,
// which validateEgressAllowEntry checks against the blocked suffixes
// instead.
func parseEntryAsNetwork(entry string) *net.IPNet {
	if strings.Contains(entry, "/") {
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil
		}
		return network
	}
	ip := net.ParseIP(entry)
	if ip == nil {
		return nil
	}
	bits := 128
	if ip.To4() != nil {
		bits = 32
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
}

// networksOverlap reports whether a and b share any address: either
// contains the other's base address. Sufficient for two well-formed CIDRs
// regardless of which is larger.
func networksOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}
