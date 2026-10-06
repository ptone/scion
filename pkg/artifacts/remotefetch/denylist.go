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

package remotefetch

import "net/netip"

// cloudMetadata is the link-local address cloud providers serve instance
// metadata and credentials on. The link-local rule already covers it; it is
// named on its own so the intent survives any change to the general rules.
var cloudMetadata = netip.MustParseAddr("169.254.169.254")

// deniedPrefixes are address ranges the fetcher never connects to, beyond
// the classes netip reports directly (loopback, private, link-local,
// multicast, unspecified). They cover special-purpose IPv4 ranges and the
// IPv6 ranges that embed or translate an IPv4 address, so an IPv6 spelling
// cannot reach a denied IPv4 destination.
var deniedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		// IPv4 special purpose (RFC 6890 and successors).
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // shared address space (carrier-grade NAT)
		"169.254.0.0/16",  // link-local, including cloud metadata
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"192.88.99.0/24",  // 6to4 relay anycast
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved, including broadcast
		// IPv6 special purpose.
		"::/96",          // IPv4-compatible (deprecated) and unspecified
		"64:ff9b::/96",   // NAT64 well-known prefix
		"64:ff9b:1::/48", // NAT64 local use
		"100::/64",       // discard
		"2001::/32",      // Teredo
		"2001:db8::/32",  // documentation
		"2002::/16",      // 6to4
		"fec0::/10",      // site-local (deprecated)
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// isDenied reports whether the fetcher must refuse to connect to addr. An
// IPv4-mapped IPv6 address is judged as the IPv4 address it maps.
func isDenied(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return true
	}
	addr = addr.Unmap()
	if addr == cloudMetadata {
		return true
	}
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() {
		return true
	}
	for _, p := range deniedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
