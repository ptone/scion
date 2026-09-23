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
	"net/netip"
	"regexp"
	"strings"
)

// egressAllowCatchAllValues are exact egress_allow entries (compared after
// NormalizeEgressAllowEntry) that would authorize traffic to any
// destination. Not load-bearing for correctness (a catch-all CIDR/hostname
// is also caught by the grammar/overlap checks below), but gives a clearer
// error than "overlaps 10.0.0.0/8" for the obvious spellings.
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
//
// This list is a second layer, not the primary gate: the primary gate is
// the allowlist grammar in validateEgressAllowEntry (round 3 direction,
// substrate-lead) — only a public-shaped FQDN or a canonically-parsed IP/
// CIDR is ever considered at all. This list then narrows that already-
// well-formed set further, rejecting the ranges below even when spelled
// canonically.
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
	"::/128",         // the IPv6 unspecified address ("::").
	"::/96",          // IPv4-compatible IPv6 ("::a.b.c.d", deprecated but still parses), embeds all of IPv4 space (review round 3, O-1).
	"2002::/16",      // 6to4, embeds IPv4 space (review round 2, O2).
	"64:ff9b::/96",   // NAT64 (RFC 6052, well-known prefix), embeds IPv4 space (review round 2, O2).
	"64:ff9b:1::/48", // NAT64 (RFC 8215, local-use prefix), embeds IPv4 space (review round 3, O-1).
	"2001::/32",      // Teredo (RFC 4380), embeds IPv4 space (review round 3, O-1).
}

// egressAllowBlockedHostSuffixes are hostname suffixes that always resolve
// inside the cluster, via mDNS, or to loopback, regardless of what CIDR
// range backs them at any given moment.
var egressAllowBlockedHostSuffixes = []string{
	".svc",
	".cluster.local",
	".internal",
	".local",
	".localhost", // RFC 6761: MUST resolve to loopback (review round 3, R-A).
}

// egressAllowBlockedHostNames are exact hostnames (after normalization)
// that resolve to loopback/local via convention rather than a suffix rule.
var egressAllowBlockedHostNames = map[string]bool{
	// The traditional /etc/hosts loopback alias on many Linux
	// distributions; resolved locally regardless of DNS (review round 3,
	// R-A).
	"localhost.localdomain": true,
}

// egressAllowKnownNamespaces are Kubernetes namespace names common enough
// that a hostname ending in one of them is far more likely to be a cluster
// short name (resolved through the pod's DNS search path, e.g.
// "kubernetes.default") than a genuine public hostname. Only needed for
// namespace names that are themselves valid alphabetic "TLD" shapes (no
// hyphen) — a hyphenated one, like "ate-system", is already rejected by
// egressAllowLastLabelPattern below, since no public TLD is hyphenated.
// This can't enumerate every deployment's actual broker/worker namespace
// names — V1SubstrateConfig doesn't carry them.
var egressAllowKnownNamespaces = map[string]bool{
	"default": true,
}

// egressAllowLabelPattern matches one ASCII LDH-only DNS label: a letter or
// digit, optionally followed by up to 61 letters/digits/hyphens and ending
// in a letter or digit (RFC 1035 label syntax, lowercase since entries are
// normalized before this runs). It rejects empty labels (from "..", e.g. a
// double trailing dot surviving normalization's single-dot strip), any
// non-ASCII character (blocking IDN look-alikes such as U+3002 IDEOGRAPHIC
// FULL STOP or fullwidth letters, which this validator never Unicode-
// normalizes), space, underscore, and leading/trailing hyphens.
var egressAllowLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// egressAllowLastLabelPattern matches a valid public-TLD-shaped last label:
// purely alphabetic, 2-63 characters. No public TLD is numeric, hex, or
// hyphenated, which is what excludes every IP-address-shaped string that
// reaches this point (e.g. "127.0.0.1", "0x7f.0.0.1", "1.2.3.4.5",
// "foo.123") and every hyphenated cluster short name (e.g.
// "atenet-router.ate-system") without a radix-aware parser or an
// exhaustive namespace list. IDNA punycode labels ("xn--...") are checked
// separately, since they are alphanumeric-with-hyphens, not purely
// alphabetic.
var egressAllowLastLabelPattern = regexp.MustCompile(`^[a-z]{2,63}$`)

// egressAllowIPAttemptPattern matches strings built only from the
// characters used to spell an IPv4/IPv6 address or CIDR in any radix a
// human or a non-conforming parser might use: hex/decimal digits, 'x' (for
// "0x..." prefixes), '.', ':', and '/'. An entry matching this is required
// to parse as a canonical address/prefix (see validateEgressAllowEntry);
// one that doesn't is rejected as a malformed IP/CIDR rather than being
// reinterpreted as a hostname — round 2's fix let exactly this fallback
// through for mixed-radix octets like "0x7f.0.0.1" (round 3, Required R-A).
var egressAllowIPAttemptPattern = regexp.MustCompile(`^[0-9a-fx.:]+$`)

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

// ValidateEgressAllow is allowlist-first (substrate-lead direction, review
// round 3): an entry is considered AT ALL only if it is (a) a public-FQDN-
// shaped hostname — ASCII LDH labels, an optional leading "*" label, and an
// alphabetic-or-punycode last label — or (b) an IP address or CIDR that
// parses as CANONICAL via net/netip (which, unlike net.ParseIP/ParseCIDR,
// rejects leading zeros, mixed-radix octets, and other non-canonical
// spellings). Rounds 1 and 2 instead grew a blocklist of rejected shapes,
// which is exactly what let each new bypass class through: something that
// merely *resembles* neither shape has nowhere left to fall through to
// here.
//
// Entries that do pass the grammar are then checked, as a second layer,
// against egressAllowBlockedCIDRs/egressAllowBlockedHostSuffixes/
// egressAllowBlockedHostNames — well-formed but still private, in-cluster,
// or loopback/local destinations. Empty entries are ignored. The returned
// error names the offending entry as originally written, before
// normalization.
func ValidateEgressAllow(entries []string) error {
	for _, raw := range entries {
		normalized := NormalizeEgressAllowEntry(raw)
		if normalized == "" {
			continue
		}
		if err := validateEgressAllowEntry(raw, normalized); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeEgressAllowEntry trims whitespace, lowercases, and strips one
// trailing '.' (a syntactically valid FQDN terminator that would otherwise
// defeat suffix matching, e.g.
// "atenet-router.ate-system.svc.cluster.local." — review round 2, fix step
// 1). Deliberately strips only one: an entry with two or more trailing dots
// (e.g. "foo.svc..") is not a validly-terminated FQDN, and is left with a
// dangling '.' that produces an empty final label — rejected by
// egressAllowLabelPattern in the hostname grammar, not silently "fixed" by
// stripping further (review round 3, R-A).
//
// Exported so pkg/runtime.substrateEgressHostnames can send exactly the
// string that was validated, rather than a merely-trimmed one — Substrate's
// own HostnameRule requires a lowercase name with no trailing dot, so
// sending the raw (or only-trimmed) form of an entry that validated fine
// after normalization, e.g. "GitHub.COM.", would fail at the Substrate API
// even though ValidateEgressAllow accepted it (review round 3, "validate
// what you send").
func NormalizeEgressAllowEntry(raw string) string {
	e := strings.TrimSpace(raw)
	e = strings.ToLower(e)
	e = strings.TrimSuffix(e, ".")
	return e
}

// validateEgressAllowEntry validates the already-normalized entry,
// reporting errors against raw (the original, unnormalized text) so the
// operator sees what they actually wrote.
func validateEgressAllowEntry(raw, entry string) error {
	if egressAllowCatchAllValues[entry] || isBareWildcard(entry) {
		return fmt.Errorf("egress_allow entry %q is a catch-all and is not permitted; name specific hosts or CIDRs instead", raw)
	}

	// Brackets and '%' zone IDs are not valid in any form this validator
	// accepts (a bare IPv6 address, a CIDR, or a hostname) — reject
	// unconditionally rather than let them slip through whichever branch
	// below happens not to choke on them (review round 2, fix step 2).
	if strings.ContainsAny(entry, "[]%") {
		return fmt.Errorf("egress_allow entry %q contains an unsupported character (brackets or a zone id) and is not permitted", raw)
	}

	if strings.Contains(entry, "/") {
		return validateCIDREntry(raw, entry)
	}

	if _, err := netip.ParseAddr(entry); err == nil {
		ip := net.ParseIP(entry) // always succeeds: netip's grammar is a subset of net's.
		return checkNetworkOverlap(raw, hostNetwork(ip))
	}

	if egressAllowIPAttemptPattern.MatchString(entry) {
		// Looks like nothing but an attempted IP address (only hex/decimal
		// digits, 'x', '.', ':'), and net/netip just rejected it as
		// non-canonical. Reject it as a malformed IP rather than falling
		// through to the hostname grammar below, which some malformed
		// forms (e.g. "0x7f.0.0.1", whose last label "1" fails the
		// alphabetic-TLD rule anyway) would also happen to reject, but with
		// a confusing "not a valid TLD" message for something the operator
		// clearly meant as an address.
		return fmt.Errorf("egress_allow entry %q looks like an IP address but is not a canonical one (non-canonical radix, leading zeros, or otherwise malformed); use a canonical IPv4/IPv6 address or CIDR", raw)
	}

	return validateHostnameEntry(raw, entry)
}

// validateCIDREntry handles an entry containing '/'.
func validateCIDREntry(raw, entry string) error {
	if _, err := netip.ParsePrefix(entry); err != nil {
		return fmt.Errorf("egress_allow entry %q is not a canonical CIDR: %w", raw, err)
	}
	_, network, err := net.ParseCIDR(entry)
	if err != nil {
		// Unreachable in practice: netip.ParsePrefix already accepted a
		// canonical form, which net.ParseCIDR always also accepts.
		return fmt.Errorf("egress_allow entry %q is not a valid CIDR: %w", raw, err)
	}
	return checkNetworkOverlap(raw, network)
}

// validateHostnameEntry is the allowlist grammar for the "public FQDN"
// shape (review round 3, R-A): every label must be ASCII LDH (a leading "*"
// is the sole exception), and the last label must look like a public TLD
// (alphabetic, or IDNA punycode). This single structural rule is what
// replaces the round 1/2 blocklist patches: an empty label (from a
// leftover trailing dot), a non-ASCII character (IDN look-alikes), a space,
// or a numeric/hex/hyphenated last label are all rejected the same way,
// rather than each needing its own special case.
func validateHostnameEntry(raw, entry string) error {
	labels := strings.Split(entry, ".")
	if len(labels) < 2 {
		return fmt.Errorf("egress_allow entry %q is a single-label hostname, which can resolve through a cluster or local DNS search domain; use a fully-qualified hostname", raw)
	}

	for i, label := range labels {
		if i == 0 && label == "*" {
			continue // leading wildcard label; not itself LDH-shaped.
		}
		if !egressAllowLabelPattern.MatchString(label) {
			return fmt.Errorf("egress_allow entry %q has an invalid DNS label %q (labels must be ASCII letters, digits, and hyphens, and must not start or end with a hyphen, or be empty)", raw, label)
		}
	}

	lastLabel := labels[len(labels)-1]
	if !egressAllowLastLabelPattern.MatchString(lastLabel) && !strings.HasPrefix(lastLabel, "xn--") {
		return fmt.Errorf("egress_allow entry %q ends in %q, which is not a valid public top-level domain (must be alphabetic, or IDNA punycode starting \"xn--\") — this looks like a cluster-internal short name or a malformed address", raw, lastLabel)
	}
	if egressAllowKnownNamespaces[lastLabel] {
		return fmt.Errorf("egress_allow entry %q ends in %q, a well-known Kubernetes namespace name — this looks like a cluster-internal short name, not a public hostname", raw, lastLabel)
	}

	for _, suffix := range egressAllowBlockedHostSuffixes {
		if strings.HasSuffix(entry, suffix) {
			return fmt.Errorf("egress_allow entry %q targets an in-cluster or local DNS suffix (%q) and is not permitted", raw, suffix)
		}
	}
	if egressAllowBlockedHostNames[entry] {
		return fmt.Errorf("egress_allow entry %q is a well-known loopback/local alias and is not permitted", raw)
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
