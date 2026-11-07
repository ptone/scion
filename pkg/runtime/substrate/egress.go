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

package substrate

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
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
// the public-suffix check below: every one of these is already rejected by
// requiring an ICANN-managed public suffix (none of
// "svc"/"cluster.local"/"internal"/"local"/"localhost" is one), but an
// explicit list doesn't depend on the public suffix list never changing to
// include one.
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
	// distributions; resolved locally regardless of DNS.
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

// egressAllowMaxLength is the maximum length, in characters, of a fully
// qualified domain name in presentation form (RFC 1035 §3.1 / RFC 1123
// §2.1's 255-octet wire-format limit, minus the root label and length-octet
// accounting, works out to 253 characters of dotted text without a trailing
// dot). Counted against the exact string NormalizeEgressAllowEntry returns
// — including a leading "*." wildcard prefix, since that's what's actually
// sent — not just the hostname portion: idna.Lookup doesn't set
// VerifyDNSLength, so nothing else in this file enforces it, and an
// over-length entry that passed validation here would only fail later,
// inside Substrate's own CreateActorEgressPolicy call, which would break
// the invariant that whatever validates here is guaranteed sendable.
const egressAllowMaxLength = 253

// egressAllowSpecialUseTLDs are top-level domains that ARE, or overlap
// with, an ICANN-listed public-suffix entry but are carved out by their own
// RFC for a special-use purpose rather than ordinary public hosts — so the
// generic ICANN-suffix check in egressAllowSuffixOK would not, on its own,
// reject them. Checked, and rejected, before either of that function's two
// numbered rules run.
var egressAllowSpecialUseTLDs = map[string]string{
	// "arpa" is itself a real, ICANN-managed IANA infrastructure TLD, so
	// rule 1 alone would accept it — but everything actually delegated
	// under it is special-use by convention, not a public host:
	// "home.arpa" (RFC 8375, a private local-network zone),
	// "in-addr.arpa"/"ip6.arpa" (reverse DNS).
	"arpa": "the \"arpa\" top-level domain is reserved for special-use and reverse-DNS zones, not public hosts",
	// "onion" appears in the PSL's ICANN section (so rule 1 alone would
	// accept it too), but RFC 7686 reserves it for Tor hidden-service
	// addresses: compliant resolvers return NXDOMAIN for it, and only
	// Tor-aware software resolves it at all — never the public DNS.
	"onion": "the \"onion\" top-level domain is a special-use name for Tor hidden services (RFC 7686), not resolvable via the public DNS",
}

// Validate rejects an egress_allow list that would let a Substrate actor
// reach the router, other in-cluster services, or "everything" through its
// EgressPolicy. See ValidateEgressAllow for the exact rules.
//
// Threat model: the bootstrap nonce fallback (substrate-runtime.md §5.2)
// trusts whichever caller reaches the control server's /bootstrap endpoint
// first. The NetworkPolicy restricting router ingress to the broker
// namespace is the primary defense against an unauthorized bootstrap; this
// validation is the second layer, keeping a legitimately-bootstrapped actor
// from being able to reach the router (and thereby bootstrap or exec
// against *other* actors) or other in-cluster services via its own egress,
// regardless of what an operator puts in egress_allow.
//
// s is the substrate-specific config block (config.V1SubstrateConfig); the
// type itself stays in pkg/config (config parsing stays in config), but the
// substrate-specific validation rules live here with the runtime they gate.
func Validate(s *config.V1SubstrateConfig) error {
	if s == nil {
		return nil
	}
	if err := ValidateEgressAllow(s.EgressAllow); err != nil {
		return fmt.Errorf("runtimes.<name>.substrate.egress_allow: %w", err)
	}
	if err := ValidateEgressTrustBundle(s.EgressTrustBundle); err != nil {
		return fmt.Errorf("runtimes.<name>.substrate.egress_trust_bundle: %w", err)
	}
	return nil
}

// supportedEgressTrustBundle is the only trust bundle name the vendored
// Substrate version (v1.0.0, third_party/substrate/ateapi/v1) resolves —
// see docs/egress-trust-bundle.md and
// demos/egress/egress-mitm-template.yaml.tmpl in agent-substrate/substrate. Kept as a single named constant rather than
// a slice: ValidateEgressTrustBundle's doc comment explains why a future
// second name is meant to grow this into an allowlist, not change the
// field's type.
const supportedEgressTrustBundle = "egress-mitm.ate.dev"

// ValidateEgressTrustBundle rejects an egress_trust_bundle value that is not
// empty (off) and not exactly supportedEgressTrustBundle — the only name
// the vendored Substrate version's atelet actually resolves. An unsupported name would
// not fail here; it would fail much later and much less clearly, at actor
// start, with atelet's own "ClusterTrustBundle ... not found" (a name not on
// its allowlist gets the same wording as one simply missing from the
// cluster) or "is not supported by this deployment" error — see
// docs/egress-trust-bundle.md's "Operational notes" table. Failing fast here
// with an error that names the one supported value is strictly better than
// forwarding an actor-start failure the operator would have to go dig a log
// line out for.
func ValidateEgressTrustBundle(name string) error {
	if name == "" || name == supportedEgressTrustBundle {
		return nil
	}
	return fmt.Errorf("egress_trust_bundle %q is not supported; the only trust bundle name Substrate supports is %q", name, supportedEgressTrustBundle)
}

// ValidateEgressAllow is allowlist-first: it accepts only public FQDNs —
// no IP addresses or CIDRs at all (Substrate's v1 egress rule hostnames,
// which is what these entries feed, explicitly reject IP addresses). Each
// entry may name a port or an http:// scheme (see NormalizeEgressAllowEntry);
// the host part is checked as described below. The full public-suffix
// acceptance rule is isolated in egressAllowSuffixOK; see its doc comment
// for the exact checks (a special-use-TLD block ahead of everything else,
// an ICANN-listed-TLD check, at least one label beneath the domain's own
// matched suffix — ICANN or PRIVATE — and, for a wildcard entry only, a
// third check against wildcard PSL rules).
//
// A "last label is alphabetic" check is not enough on its own: it looks
// like a TLD but doesn't confirm the TLD is actually delegated on the
// public Internet — it would let through Kubernetes' own `pod`/`svc` DNS
// zones, `home.arpa` (RFC 8375), and made-up zones like `lan`/`corp`, all
// of which resolve inside a cluster or a local network rather than the
// public Internet. This matters concretely: `169-254-169-254.default.pod`
// resolves to the GKE metadata server on any cluster, no cluster-specific
// knowledge required. Checking the actual public suffix list catches all
// of these, since none of "pod"/"svc"/"lan"/"corp"/"kom" is a real
// top-level domain, ICANN-managed or otherwise.
//
// Requiring the domain's own matched suffix to be ICANN-managed rejects
// "pod"/"svc"/"lan"/"corp" as needed, but would also reject legitimate
// hostnames on multi-tenant platforms whose PSL entry is in the PRIVATE
// section rather than ICANN's — "storage.googleapis.com" (Google Cloud)
// and "foo.github.io" (GitHub Pages) both fail an "ICANN suffix only"
// check, since "googleapis.com" and "github.io" are PRIVATE PSL entries.
// Splitting the check in two avoids that: the top-level domain alone must
// be ICANN-listed (a real TLD), but the "at least one label beneath the
// suffix" check is evaluated against whichever suffix — ICANN or PRIVATE —
// actually matches the full domain. That accepts real hostnames on
// private-suffix platforms while still rejecting the bare platform domain
// itself ("googleapis.com", "*.googleapis.com", "*.github.io") and every
// made-up or in-cluster zone ("pod", "svc", "lan", "corp", the typo "kom").
//
// A wildcard entry over a remainder that isn't itself a public suffix, but
// where every single-label child of it IS one (a PSL *wildcard* rule, like
// "*.run.app" or "*.compute.amazonaws.com"), is also rejected — that grants
// every tenant on the platform, the same class of problem a bare wildcard
// over "googleapis.com" is. TLD-ness itself is decided from
// publicsuffix.PublicSuffix("x."+tld)'s icann flag, not
// publicsuffix.PublicSuffix(tld)'s, because several real ccTLDs ("ck",
// "er", "fk", "jm", "kh", "mm", "np", "pg") have only a wildcard rule in
// the PSL and no bare-TLD rule, so checking the bare label alone gives a
// false rejection for every host under them. ".onion" (RFC 7686, Tor
// hidden services) is rejected the same way ".arpa" is, via
// egressAllowSpecialUseTLDs: like "arpa", "onion" is itself ICANN-listed,
// so the generic ICANN check alone would not catch it. A single-label
// wildcard remainder ("*.com", "*.co", "*.bd") reaches the public-suffix
// check and gets that check's message, rather than being intercepted
// earlier by a "not fully qualified" single-label message, which would be
// misleading for a wildcard over a bare TLD.
//
// Residual risk, not closed by this or any DNS-name-shape check: an
// ICANN-valid public hostname can still be configured (by its owner, or by
// an attacker exploiting DNS rebinding) to resolve to a private or
// in-cluster IP address — services like nip.io/sslip.io do this
// deliberately and by design, and a wildcard entry over one of them
// authorizes every address it might ever hand out. Only a check performed
// by the egress proxy itself, after DNS resolution, against the address it
// actually connects to, can close that; no client-side allowlist over the
// name alone can.
//
// A leading "*." wildcard label is accepted, but the remainder after it
// must independently satisfy the same rule — "*.com", "*.co", "*.bd", and
// "*.co.uk" are all rejected (the remainder is itself a public suffix with
// no label beneath it, or the remainder's own children are all public
// suffixes via a PSL wildcard rule, as with
// "*.run.app"/"*.compute.amazonaws.com"/"*.compute-1.amazonaws.com"/
// "*.kawasaki.jp"), while "*.example.com" passes ("example.com" has
// "example" beneath the "com" suffix, and "com" has no PSL wildcard rule).
//
// Entries are also capped at egressAllowMaxLength characters, counting a
// wildcard prefix if present, matching the standard DNS presentation-form
// limit.
//
// The existing in-cluster/loopback/local hostname suffix and exact-name
// blocklists (egressAllowBlockedHostSuffixes, egressAllowBlockedHostNames)
// remain as a second layer on top of the public-suffix check — redundant
// against everything the public suffix list already excludes, but not
// dependent on it staying that way.
//
// Empty entries are ignored. The returned error names the offending entry
// as originally written, before normalization.
func ValidateEgressAllow(entries []string) error {
	_, err := ParseEgressAllow(entries)
	return err
}

// EgressProtocol is the v1 egress rule kind an egress_allow entry becomes.
type EgressProtocol string

const (
	// EgressProtocolHTTP is a cleartext HTTP rule (v1 HttpRule).
	EgressProtocolHTTP EgressProtocol = "http"
	// EgressProtocolTLSPassthrough is TLS forwarded without decryption
	// (v1 TlsPassthroughRule).
	EgressProtocolTLSPassthrough EgressProtocol = "tls_passthrough"
)

// Default ports for an egress_allow entry that names none.
const (
	defaultHTTPPort  int32 = 80
	defaultHTTPSPort int32 = 443
)

// EgressAllowTarget is one parsed egress_allow entry: a host pattern (the
// canonical form NormalizeEgressAllowEntry returns: an exact hostname or a
// single-label "*." wildcard) on exactly one port, never a range or all
// ports.
type EgressAllowTarget struct {
	Protocol EgressProtocol
	Host     string
	Port     int32
}

// ParseEgressAllow validates every entry exactly as NormalizeEgressAllowEntry
// does (the same implementation, normalizeEgressAllowEntry) and returns the
// typed targets, skipping empty entries. It rejects a list that names the
// same host and port with both http and TLS: v1 would rank them as a tie.
func ParseEgressAllow(entries []string) ([]EgressAllowTarget, error) {
	var targets []EgressAllowTarget
	protoByHostPort := make(map[string]EgressProtocol)
	for _, raw := range entries {
		t, ok, err := normalizeEgressAllowEntry(raw)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		key := t.Host + ":" + strconv.Itoa(int(t.Port))
		if prev, seen := protoByHostPort[key]; seen && prev != t.Protocol {
			return nil, fmt.Errorf("egress_allow names %s as both %s and %s; one host and port can have only one protocol", key, prev, t.Protocol)
		}
		protoByHostPort[key] = t.Protocol
		targets = append(targets, t)
	}
	return targets, nil
}

// parseEgressPort parses an exact TCP port, 1 to 65535.
func parseEgressPort(raw, p string) (int32, error) {
	n, err := strconv.ParseUint(p, 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("egress_allow entry %q has invalid port %q; a port is a number from 1 to 65535", raw, p)
	}
	return int32(n), nil
}

// NormalizeEgressAllowEntry validates raw as an egress_allow entry and
// returns the canonical string to send in the actor's EgressPolicy
// (returns "", nil for an empty/whitespace-only entry, which callers
// should skip). This function is deliberately both the validator and the
// normalizer: the string it returns is exactly the string that passed
// every check, so what's validated and what's sent cannot drift apart.
// ValidateEgressAllow, ParseEgressAllow and pkg/runtime.substrateEgressTargets
// all go through it (or normalizeEgressAllowEntry, its implementation);
// none should reimplement or bypass it.
//
// Accepted forms, and the canonical string each returns:
//
//   - "host" or "*.host": TLS passthrough on 443 (canonical "host"). A bare
//     host narrows from any port (the pre-1.0 API) to 443 (v1), by design;
//   - "host:port" or "*.host:port": TLS passthrough on that port
//     (canonical "host:port", or "host" for 443);
//   - "https://host[:port]": the same as the bare forms;
//   - "http://host[:port]": cleartext HTTP on that port, default 80
//     (canonical "http://host:port").
//
// The port is an integer from 1 to 65535. A URL form carries no userinfo,
// path (other than "/"), query or fragment. The host part passes every
// host check below (validatePublicHostname and the catch-all, IP/CIDR and
// blocked-suffix refusals), so no syntax admits an in-cluster or IP host.
//
// A "*.host" wildcard keeps today's meaning without widening: the v1 rule
// pattern syntax matches exactly one leftmost label, as EgressAllowCovers
// does ("*.example.com" matches "api.example.com", not "example.com" or
// "nested.api.example.com": third_party/substrate/ateapi/v1/ateapi.proto
// :683-685, the syntax tls_passthrough and https rules share, :726, :755),
// so the entry is sent as the same pattern, never a broader one.
func NormalizeEgressAllowEntry(raw string) (string, error) {
	t, ok, err := normalizeEgressAllowEntry(raw)
	if err != nil || !ok {
		return "", err
	}
	switch {
	case t.Protocol == EgressProtocolHTTP:
		return "http://" + t.Host + ":" + strconv.Itoa(int(t.Port)), nil
	case t.Port != defaultHTTPSPort:
		return t.Host + ":" + strconv.Itoa(int(t.Port)), nil
	default:
		return t.Host, nil
	}
}

// normalizeEgressAllowEntry is NormalizeEgressAllowEntry's implementation,
// returning the typed target. ok is false (with a nil error) for an empty
// entry.
func normalizeEgressAllowEntry(raw string) (target EgressAllowTarget, ok bool, err error) {
	e := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if e == "" {
		return EgressAllowTarget{}, false, nil
	}
	// Catch-alls, IP and CIDR spellings (including IPv6, whose colons are
	// not a port separator) and bracketed or zoned forms are refused by
	// the host checks on the whole entry, with the same errors as before
	// ports existed.
	if egressAllowCatchAllValues[e] || isBareWildcard(e) || strings.ContainsAny(e, "[]%") || looksLikeIPAttempt(e) {
		if _, herr := normalizeEgressAllowHost(raw, e); herr != nil {
			return EgressAllowTarget{}, false, herr
		}
		return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q is not permitted", raw)
	}

	protocol := EgressProtocolTLSPassthrough
	port := defaultHTTPSPort
	hostPart := e
	switch {
	case strings.Contains(e, "://"):
		u, perr := url.Parse(e)
		if perr != nil {
			if strings.Contains(perr.Error(), "invalid port") {
				return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q has invalid port; a port is a number from 1 to 65535", raw)
			}
			return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q is not a valid URL", raw)
		}
		if u.User != nil {
			// The entry is not echoed: userinfo may carry a credential.
			return EgressAllowTarget{}, false, fmt.Errorf("an egress_allow URL entry carries userinfo, which is not permitted; write it as http://host[:port] or host:port")
		}
		if u.Path != "" && u.Path != "/" {
			return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q may not carry a path; write it as http://host[:port] or host:port", raw)
		}
		if u.RawQuery != "" || u.ForceQuery {
			return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q may not carry a query; write it as http://host[:port] or host:port", raw)
		}
		if u.Fragment != "" || strings.Contains(e, "#") {
			return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q may not carry a fragment; write it as http://host[:port] or host:port", raw)
		}
		if u.Opaque != "" {
			return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q is not a valid URL", raw)
		}
		switch u.Scheme {
		case "http":
			protocol, port = EgressProtocolHTTP, defaultHTTPPort
		case "https":
		default:
			return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q has unsupported scheme %q; use http:// or https://, or host:port", raw, u.Scheme)
		}
		hostPart = u.Hostname()
		if p := u.Port(); p != "" {
			if port, err = parseEgressPort(raw, p); err != nil {
				return EgressAllowTarget{}, false, err
			}
		} else if strings.HasSuffix(u.Host, ":") {
			return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q has invalid port %q; a port is a number from 1 to 65535", raw, "")
		}
	case strings.Contains(e, ":"):
		h, p, _ := strings.Cut(e, ":")
		if strings.Contains(p, ":") {
			return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q is not host:port", raw)
		}
		hostPart = h
		if port, err = parseEgressPort(raw, p); err != nil {
			return EgressAllowTarget{}, false, err
		}
	}

	host, err := normalizeEgressAllowHost(raw, hostPart)
	if err != nil {
		return EgressAllowTarget{}, false, err
	}
	if host == "" {
		return EgressAllowTarget{}, false, fmt.Errorf("egress_allow entry %q names no host", raw)
	}
	return EgressAllowTarget{Protocol: protocol, Host: host, Port: port}, true, nil
}

// normalizeEgressAllowHost applies every host check to host (the host part
// of raw) and returns its canonical form; errors name raw as written.
func normalizeEgressAllowHost(raw, host string) (string, error) {
	e := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".") // at most one trailing dot; see the doc on egressAllowLabelPattern for why a second one isn't stripped too.
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
		return "", fmt.Errorf("egress_allow entry %q: IP/CIDR egress rules not currently supported", raw)
	}

	rest, wildcard := strings.CutPrefix(e, "*.")
	ascii, err := validatePublicHostname(raw, rest, wildcard)
	if err != nil {
		return "", err
	}
	final := ascii
	if wildcard {
		final = "*." + ascii
	}
	// Enforce the total length against the exact host about to be sent,
	// including a wildcard prefix — not just the hostname portion — so
	// what's validated matches what's sendable.
	if len(final) > egressAllowMaxLength {
		return "", fmt.Errorf("egress_allow entry %q is %d characters long, over the %d-character limit for a DNS name", raw, len(final), egressAllowMaxLength)
	}
	return final, nil
}

// EgressAllowCovers reports whether host (a bare, already-lowercased
// hostname — never a URL, never carrying a leading wildcard) is covered by
// any entry in normalizedEntries, each of which must already be the
// canonical form NormalizeEgressAllowEntry returns (this function does not
// re-normalize them). An entry covers host either by an exact match, or —
// when the entry has a leading "*." — when host has exactly one more label
// than the wildcard's own remainder and ends with it: "*.example.com"
// covers "api.example.com" but not "example.com" itself (no label to
// consume) or "a.b.example.com" (more than one extra label) — the same
// single-level shape a TLS wildcard certificate matches, chosen because it
// is the narrowest, least surprising reading of what an operator who wrote
// "*.example.com" meant to authorize.
func EgressAllowCovers(normalizedEntries []string, host string) bool {
	for _, entry := range normalizedEntries {
		if egressAllowEntryCovers(entry, host) {
			return true
		}
	}
	return false
}

func egressAllowEntryCovers(normalizedEntry, host string) bool {
	if normalizedEntry == host {
		return true
	}
	remainder, isWildcard := strings.CutPrefix(normalizedEntry, "*.")
	if !isWildcard {
		return false
	}
	label, ok := strings.CutSuffix(host, "."+remainder)
	return ok && label != "" && !strings.Contains(label, ".")
}

// egressAllowSuffixOK is THE public-suffix acceptance rule. Deliberately
// isolated in its own small function — nothing else in this file depends
// on its internals — so this specific rule can be swapped out on its own
// if it needs to change again.
//
// ascii is the candidate hostname (already IDNA-converted to ASCII, with
// any leading "*." wildcard already stripped by the caller); labels is
// strings.Split(ascii, "."); wildcard reports whether the caller had a
// leading "*." (i.e. this is validating the remainder of a wildcard entry,
// not a bare hostname).
//
// egressAllowSpecialUseTLDs is checked first, unconditionally: some
// ICANN-listed labels ("arpa", "onion") are carved out for a special use
// other than ordinary public hosts, which rule 1 below would not catch on
// its own since they genuinely are ICANN-listed.
//
// Two checks after that, both required:
//
//  1. The top-level domain (the last label) must itself be an
//     ICANN-managed public suffix. Checked as
//     publicsuffix.PublicSuffix("x."+lastLabel), not
//     publicsuffix.PublicSuffix(lastLabel): several real ccTLDs — "ck",
//     "er", "fk", "jm", "kh", "mm", "np", "pg" — have only a wildcard rule
//     ("*.ck") in the PSL, no bare-TLD rule, so PublicSuffix("ck") alone
//     falls through to the unmanaged default and wrongly reports
//     icann==false. Prepending a throwaway label makes the wildcard rule
//     match, so the check works the same way for a wildcard-only ccTLD as
//     for an ordinary one. This is what rejects Kubernetes' own DNS zones
//     ("pod", "svc"), special-use and made-up zones ("lan", "corp"), and
//     typos ("kom") — none of these is a real top-level domain at all,
//     ICANN-managed or otherwise, whether checked bare or with a prepended
//     label.
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
// A third check applies only when wildcard is true: some PSL suffixes are
// *wildcard* rules rather than bare ones — "run.app",
// "compute.amazonaws.com", "compute-1.amazonaws.com", "kawasaki.jp" are
// none of them a public suffix by themselves (so rule 2 lets them through:
// "run.app" is a perfectly good specific host, one label under the "app"
// TLD), but every single label prepended to one of them IS its own public
// suffix — the exact multi-tenant-platform shape rule 2 exists to catch —
// so a wildcard entry over the bare remainder ("*.run.app") would grant
// every tenant on the platform, not one specific host, the same problem a
// bare wildcard over "googleapis.com"/"github.io" would be if rule 2 didn't
// already catch those directly. Detected the same way rule 1 detects a
// wildcard-only ccTLD: prepend a throwaway label and ask whether the whole
// thing still comes back as the public suffix.
func egressAllowSuffixOK(ascii string, labels []string, wildcard bool) (ok bool, reason string) {
	lastLabel := labels[len(labels)-1]
	if reason, special := egressAllowSpecialUseTLDs[lastLabel]; special {
		return false, reason
	}

	if _, icann := publicsuffix.PublicSuffix("x." + lastLabel); !icann {
		return false, fmt.Sprintf("top-level domain %q is not a recognized ICANN-managed public suffix — likely a private, unmanaged, or made-up zone (e.g. a Kubernetes DNS zone like \"pod\"/\"svc\", or a typo)", lastLabel)
	}

	suffix, _ := publicsuffix.PublicSuffix(ascii)
	if ascii == suffix {
		return false, fmt.Sprintf("the entry is itself a public suffix (%q) — whether ICANN-managed or a private multi-tenant platform suffix such as \"googleapis.com\"/\"github.io\" — not a specific hostname beneath one", suffix)
	}

	if wildcard {
		probe := "x." + ascii
		if s, _ := publicsuffix.PublicSuffix(probe); s == probe {
			return false, fmt.Sprintf("%q is a wildcard public-suffix rule (every subdomain beneath it is a separate, mutually untrusting tenant, the same shape as \"googleapis.com\"/\"github.io\") — a wildcard egress_allow entry over it would grant every tenant on the platform, not a specific hostname beneath one", ascii)
		}
	}

	return true, ""
}

// validatePublicHostname validates hostname (already lowercased/trimmed,
// and with any leading "*." wildcard label already removed by the caller)
// against the public-FQDN grammar, returning its canonical ASCII form.
// wildcard reports whether the caller had a leading "*." — see
// egressAllowSuffixOK's doc for why that changes the suffix check, and the
// single-label case just below for why it changes that check too: a bare
// single-label entry like "com" is rejected as "not fully qualified", since
// a resolver could fold it into a search-domain suffix — but a *wildcard*
// single-label remainder like "*.com" has no such ambiguity (there's
// nothing to search-domain-expand under a wildcard), so it's let through to
// egressAllowSuffixOK, which rejects it for the more specific and more
// accurate reason that "com" is itself a public suffix.
func validatePublicHostname(raw, hostname string, wildcard bool) (string, error) {
	ascii, err := idna.Lookup.ToASCII(hostname)
	if err != nil {
		return "", fmt.Errorf("egress_allow entry %q is not a valid hostname (or valid internationalized domain name): %w", raw, err)
	}

	labels := strings.Split(ascii, ".")
	if len(labels) < 2 && !wildcard {
		return "", fmt.Errorf("egress_allow entry %q is a single-label hostname, which can resolve through a cluster or local DNS search domain; use a fully-qualified hostname", raw)
	}
	for _, label := range labels {
		if !egressAllowLabelPattern.MatchString(label) {
			return "", fmt.Errorf("egress_allow entry %q has an invalid DNS label %q (labels must be ASCII letters, digits, and hyphens, and must not start or end with a hyphen, or be empty)", raw, label)
		}
	}

	if ok, reason := egressAllowSuffixOK(ascii, labels, wildcard); !ok {
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
// ValidateEgressAllow rejects all of these outright (see its doc); this is
// what routes an entry to that rejection instead of the hostname grammar.
//
// Deliberately narrow: it does NOT treat a bare string of hex-alphabet
// characters as an IP attempt just because every character happens to be
// in [0-9a-f]. Only an explicit "0x" prefix counts as hex; an unprefixed
// token must be pure decimal digits. Real, unrelated public hostnames
// whose labels spell hex-legal words — "cafe.de", "dead.beef.com",
// "abc.de", "fab.be", "adcb.ae" — are never mistaken for an IP address
// this way (a broader character-class-only pattern would reject all of
// these as false positives).
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

// IsIPLiteralHost reports whether host is an IP address, or would be read
// as one by a URL parser or inet_aton-style resolver, rather than as a DNS
// name: either net/netip parses it (IPv4, IPv6, IPv6 with a zone), or its
// last label is a number in the WHATWG URL Standard's "ends in a number"
// sense — all decimal digits, or "0x" followed by zero or more hex digits.
// That second test is what catches the short and mixed-radix forms
// netip.ParseAddr does not accept ("127.1", "0x7f000001", "0177.0.0.1",
// "2130706433"). One trailing dot is ignored and case does not matter.
//
// Only the LAST label is examined, so a hostname with digits in an earlier
// label ("api2.example.com", "1.example.com") is not an IP literal.
// Unprefixed hex-alphabet labels are not numbers either: "cafe.de" and
// "dead.beef" are hostnames.
//
// It is for hosts that skip NormalizeEgressAllowEntry — the operator's
// trusted hub host in pkg/runtime. Tenant and egress_allow entries already
// refuse every one of these shapes: looksLikeIPAttempt catches the
// all-numeric forms, and the public-suffix check refuses a hostname whose
// last label is a number, since no public suffix is numeric.
func IsIPLiteralHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	last := h[strings.LastIndex(h, ".")+1:]
	if last == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(last, "0x"); ok {
		return isAllHexDigits(rest)
	}
	return isAllDigits(last)
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
