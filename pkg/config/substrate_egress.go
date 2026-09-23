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
	"net/netip"
	"regexp"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// egressAllowCatchAllValues are exact egress_allow entries (compared after
// trim/lowercase/one-trailing-dot-strip) that would authorize traffic to
// any destination. Not load-bearing for correctness on its own — a
// catch-all also fails the public-hostname grammar below — but gives a
// clearer error than "not a valid hostname" for the obvious spellings.
var egressAllowCatchAllValues = map[string]bool{
	"all":       true,
	"*":         true,
	"0.0.0.0/0": true,
	"::/0":      true,
}

// egressAllowBlockedHostSuffixes are hostname suffixes that always resolve
// inside the cluster, via mDNS, or to loopback, regardless of what CIDR
// range backs them at any given moment. Kept as a second layer on top of
// the public-suffix check (review round 4, substrate-lead direction):
// every one of these is already rejected by requiring an ICANN-managed
// public suffix (none of "svc"/"cluster.local"/"internal"/"local"/
// "localhost" is one), but an explicit list doesn't depend on the public
// suffix list never changing to include one.
var egressAllowBlockedHostSuffixes = []string{
	".svc",
	".cluster.local",
	".internal",
	".local",
	".localhost",
}

// egressAllowBlockedHostNames are exact hostnames (after normalization)
// that resolve to loopback/local via convention rather than a suffix rule.
var egressAllowBlockedHostNames = map[string]bool{
	// The traditional /etc/hosts loopback alias on many Linux
	// distributions; resolved locally regardless of DNS (review round 3,
	// R-A).
	"localhost.localdomain": true,
}

// egressAllowLabelPattern matches one ASCII LDH-only DNS label: a letter or
// digit, optionally followed by up to 61 letters/digits/hyphens and ending
// in a letter or digit (RFC 1035 label syntax, lowercase since entries are
// normalized before this runs). Applied to the IDNA-converted ASCII form,
// so an internationalized label appears here as its "xn--..." punycode
// encoding. Rejects empty labels (from "..", e.g. a double trailing dot
// surviving normalization's single-dot strip) and leading/trailing
// hyphens.
var egressAllowLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

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
// rounds 3-4): Phase 1 accepts only public FQDNs — no IP addresses or
// CIDRs at all (Substrate's own HostnameRule.patterns, which is what these
// entries feed, explicitly rejects IP addresses; CIDRRule support is
// deferred to a later phase, review round 4, R4-2). The full public-suffix
// acceptance rule is isolated in egressAllowSuffixOK; see its doc comment
// for the exact two-part check (ICANN-listed TLD, plus at least one label
// beneath the domain's own matched suffix, ICANN or PRIVATE) and the
// unconditional ".arpa" block ahead of it.
//
// The public-suffix requirement replaced round 3's "last label is
// alphabetic" rule, which is a proxy for "looks like a TLD" but not for "is
// a real, publicly-delegated one" — it let through Kubernetes' own `pod`/
// `svc` DNS zones, `home.arpa` (RFC 8375), and made-up zones like `lan`/
// `corp`, all of which resolve inside a cluster or a local network rather
// than on the public Internet (review round 4, R4-1: `169-254-169-254.
// default.pod` resolves to the GKE metadata server on any cluster, no
// cluster-specific knowledge required).
//
// The rule went through one refinement in round 4 itself: the first
// version required the domain's own matched suffix to be ICANN-managed,
// which correctly rejected "pod"/"svc"/"lan"/"corp", but also rejected
// legitimate hostnames on multi-tenant platforms whose PSL entry is in the
// PRIVATE section rather than ICANN's — "storage.googleapis.com" (Google
// Cloud) and "foo.github.io" (GitHub Pages) both fail an "ICANN suffix
// only" check, since "googleapis.com" and "github.io" are PRIVATE PSL
// entries. Substrate-lead's approved refinement splits the check in two:
// the top-level domain alone must be ICANN-listed (a real TLD), but the
// "at least one label beneath the suffix" check is evaluated against
// whichever suffix — ICANN or PRIVATE — actually matches the full domain.
// That accepts real hostnames on private-suffix platforms while still
// rejecting the bare platform domain itself ("googleapis.com",
// "*.googleapis.com", "*.github.io") and everything round 4 originally
// targeted ("pod", "svc", "lan", "corp", the numeric typo "kom").
//
// Residual risk, not closed by this or any DNS-name-shape check: an
// ICANN-valid public hostname can still be configured (by its owner, or by
// an attacker exploiting DNS rebinding) to resolve to a private or
// in-cluster IP address — services like nip.io/sslip.io do this
// deliberately and by design. Only a check performed by the egress proxy
// itself, after DNS resolution, against the address it actually connects
// to, can close that; no client-side allowlist over the name alone can.
//
// A leading "*." wildcard label is accepted, but the remainder after it
// must independently satisfy the same rule — "*.com" and "*.co.uk" are
// rejected (the remainder, "com" or "co.uk", is itself a public suffix
// with no label beneath it), while "*.example.com" passes ("example.com"
// has "example" beneath the "com" suffix).
//
// The existing in-cluster/loopback/local hostname suffix and exact-name
// blocklists (egressAllowBlockedHostSuffixes, egressAllowBlockedHostNames)
// remain as a second layer on top of the public-suffix check, per
// substrate-lead's direction — redundant against everything the public
// suffix list already excludes, but not dependent on it staying that way.
//
// Empty entries are ignored. The returned error names the offending entry
// as originally written, before normalization.
func ValidateEgressAllow(entries []string) error {
	for _, raw := range entries {
		if _, err := NormalizeEgressAllowEntry(raw); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeEgressAllowEntry validates raw as an egress_allow entry and
// returns the canonical string to send in the actor's EgressPolicy
// (returns "", nil for an empty/whitespace-only entry, which callers
// should skip). This function is deliberately both the validator and the
// normalizer: the string it returns is exactly the string that passed
// every check, so what's validated and what's sent cannot drift apart —
// rounds 3 and 4 of review both found gaps of exactly that shape.
// ValidateEgressAllow and pkg/runtime.substrateEgressHostnames both call
// this; neither should reimplement or bypass it.
func NormalizeEgressAllowEntry(raw string) (string, error) {
	e := strings.TrimSpace(raw)
	e = strings.ToLower(e)
	e = strings.TrimSuffix(e, ".") // at most one trailing dot; see the doc on egressAllowLabelPattern for why a second one isn't stripped too.
	if e == "" {
		return "", nil
	}

	if egressAllowCatchAllValues[e] || isBareWildcard(e) {
		return "", fmt.Errorf("egress_allow entry %q is a catch-all and is not permitted; name a specific public hostname instead", raw)
	}
	if strings.ContainsAny(e, "[]%") {
		return "", fmt.Errorf("egress_allow entry %q contains an unsupported character (brackets or a zone id) and is not permitted", raw)
	}
	if looksLikeIPAttempt(e) {
		return "", fmt.Errorf("egress_allow entry %q: IP/CIDR egress rules not supported in Phase 1", raw)
	}

	rest, wildcard := strings.CutPrefix(e, "*.")
	ascii, err := validatePublicHostname(raw, rest)
	if err != nil {
		return "", err
	}
	if wildcard {
		return "*." + ascii, nil
	}
	return ascii, nil
}

// egressAllowSuffixOK is THE public-suffix acceptance rule (substrate-lead's
// approved refinement of the "option A" allowlist direction, review round
// 4). Deliberately isolated in its own small function — nothing else in
// this file depends on its internals — so this specific rule can be
// swapped out on its own if it needs to change again, which it already has
// once this round.
//
// ascii is the candidate hostname (already IDNA-converted to ASCII, with
// any leading "*." wildcard already stripped by the caller); labels is
// strings.Split(ascii, ".").
//
// Two checks, both required:
//
//  1. The top-level domain (the last label) must itself be an
//     ICANN-managed public suffix: publicsuffix.PublicSuffix(lastLabel)
//     must report icann==true. This is what rejects Kubernetes' own DNS
//     zones ("pod", "svc"), special-use and made-up zones ("lan", "corp"),
//     and typos ("kom") — none of these is a real top-level domain at all,
//     ICANN-managed or otherwise, so checking the label in isolation
//     (rather than the whole domain's matched suffix) is what actually
//     answers "is this a real TLD".
//  2. The domain must have at least one label beneath its OWN matched
//     public suffix — which may be a PRIVATE-section entry, not only an
//     ICANN one. The public suffix list's PRIVATE section exists
//     precisely for multi-tenant platforms where each subdomain is
//     operated by a different, mutually untrusting party: "googleapis.com"
//     (Google Cloud) and "github.io" (GitHub Pages) are both PRIVATE
//     entries. Treating rule 1 alone as sufficient would accept
//     "googleapis.com" or "*.googleapis.com" as if the whole platform were
//     a single hostname; requiring a label beneath the actual (possibly
//     private) suffix accepts "storage.googleapis.com" and
//     "foo.github.io" — real, specific hostnames on those platforms — while
//     still rejecting the bare platform domain and any wildcard directly
//     over it.
//
// The whole ".arpa" top-level domain is rejected unconditionally before
// either check runs: "arpa" is itself ICANN-listed (it is a real IANA
// infrastructure TLD), so rule 1 alone would not catch it, but zones
// delegated under it are special-use by convention rather than public
// hosts — "home.arpa" (RFC 8375, a private local-network zone),
// "in-addr.arpa"/"ip6.arpa" (reverse DNS) — none of which are hosts on the
// public Internet in the sense this validator means.
func egressAllowSuffixOK(ascii string, labels []string) (ok bool, reason string) {
	lastLabel := labels[len(labels)-1]
	if lastLabel == "arpa" {
		return false, "the \"arpa\" top-level domain is reserved for special-use and reverse-DNS zones, not public hosts"
	}

	if _, icann := publicsuffix.PublicSuffix(lastLabel); !icann {
		return false, fmt.Sprintf("top-level domain %q is not a recognized ICANN-managed public suffix — likely a private, unmanaged, or made-up zone (e.g. a Kubernetes DNS zone like \"pod\"/\"svc\", or a typo)", lastLabel)
	}

	suffix, _ := publicsuffix.PublicSuffix(ascii)
	if ascii == suffix {
		return false, fmt.Sprintf("the entry is itself a public suffix (%q) — whether ICANN-managed or a private multi-tenant platform suffix such as \"googleapis.com\"/\"github.io\" — not a specific hostname beneath one", suffix)
	}

	return true, ""
}

// validatePublicHostname validates hostname (already lowercased/trimmed,
// and with any leading "*." wildcard label already removed by the caller)
// against the public-FQDN grammar, returning its canonical ASCII form.
func validatePublicHostname(raw, hostname string) (string, error) {
	ascii, err := idna.Lookup.ToASCII(hostname)
	if err != nil {
		return "", fmt.Errorf("egress_allow entry %q is not a valid hostname (or valid internationalized domain name): %w", raw, err)
	}

	labels := strings.Split(ascii, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("egress_allow entry %q is a single-label hostname, which can resolve through a cluster or local DNS search domain; use a fully-qualified hostname", raw)
	}
	for _, label := range labels {
		if !egressAllowLabelPattern.MatchString(label) {
			return "", fmt.Errorf("egress_allow entry %q has an invalid DNS label %q (labels must be ASCII letters, digits, and hyphens, and must not start or end with a hyphen, or be empty)", raw, label)
		}
	}

	if ok, reason := egressAllowSuffixOK(ascii, labels); !ok {
		return "", fmt.Errorf("egress_allow entry %q: %s", raw, reason)
	}

	for _, blockedSuffix := range egressAllowBlockedHostSuffixes {
		if strings.HasSuffix(ascii, blockedSuffix) {
			return "", fmt.Errorf("egress_allow entry %q targets an in-cluster or local DNS suffix (%q) and is not permitted", raw, blockedSuffix)
		}
	}
	if egressAllowBlockedHostNames[ascii] {
		return "", fmt.Errorf("egress_allow entry %q is a well-known loopback/local alias and is not permitted", raw)
	}

	return ascii, nil
}

// isBareWildcard reports whether entry consists only of '*' and '.'
// characters (e.g. "*", "**", "*.*", ".*"), which match everything or close
// to it. A legitimate single-label wildcard such as "*.example.com" is not
// bare — it has a non-wildcard, non-dot suffix.
func isBareWildcard(entry string) bool {
	if !strings.Contains(entry, "*") {
		return false
	}
	return strings.Trim(entry, "*.") == ""
}

// looksLikeIPAttempt reports whether entry is an IP address or CIDR,
// canonical or not — anything from a clean net/netip-parseable address to
// an inet_aton-style mixed-radix spelling ("0x7f.0.0.1", "10.0x0.0.1").
// Phase 1 rejects all of these outright (see ValidateEgressAllow's doc);
// this is what routes an entry to that rejection instead of the hostname
// grammar.
//
// Deliberately narrow: it does NOT treat a bare string of hex-alphabet
// characters as an IP attempt just because every character happens to be
// in [0-9a-f]. Only an explicit "0x" prefix counts as hex; an unprefixed
// token must be pure decimal digits. Real, unrelated public hostnames
// whose labels spell hex-legal words — "cafe.de", "dead.beef.com",
// "abc.de", "fab.be", "adcb.ae" — are never mistaken for an IP address
// this way (review round 4, N4-1: a broader character-class-only pattern
// rejected all of these as false positives).
func looksLikeIPAttempt(entry string) bool {
	if strings.Contains(entry, "/") {
		if _, err := netip.ParsePrefix(entry); err == nil {
			return true
		}
	} else if _, err := netip.ParseAddr(entry); err == nil {
		return true
	}
	return allPartsNumericOrHex(strings.Split(entry, "."))
}

// allPartsNumericOrHex reports whether every dot-separated part is a plain
// decimal number or a "0x"-prefixed hex number — the per-part shapes
// inet_aton-style parsers accept (including the 1-4 part shorthand forms,
// and octal-looking decimal digit runs, which this treats as decimal since
// Go has no octal-vs-decimal ambiguity to resolve here — either shape is
// rejected as non-canonical regardless).
func allPartsNumericOrHex(parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if p == "" || !isNumericOrHexToken(p) {
			return false
		}
	}
	return true
}

func isNumericOrHexToken(s string) bool {
	if rest, ok := strings.CutPrefix(s, "0x"); ok {
		return rest != "" && isAllHexDigits(rest)
	}
	return isAllDigits(s)
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isAllHexDigits(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
