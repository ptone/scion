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

// TrustedProxiesKey is the settings key of the hub's trusted proxy list
// (HubServerConfig.TrustedProxies). It is read once at startup and is
// file/env only: the server-config API refuses it in every mode.
const TrustedProxiesKey = "server.hub.trusted_proxies"

// trustedProxyEntryMaxEcho bounds how much of a refused entry an error
// message repeats.
const trustedProxyEntryMaxEcho = 64

// ValidateTrustedProxies checks server.hub.trusted_proxies. Each entry must
// be an IPv4 or IPv6 address or a CIDR range (for example "10.0.0.0/8" or
// "2001:db8::/32"). An IPv6 zone suffix ("fe80::1%eth0") is refused, as is
// an empty entry. An empty list is valid and leaves the hub using each
// request's peer address. The error names the index and the entry.
func ValidateTrustedProxies(entries []string) error {
	for i, raw := range entries {
		entry := strings.TrimSpace(raw)
		shown := entry
		if len(shown) > trustedProxyEntryMaxEcho {
			shown = shown[:trustedProxyEntryMaxEcho] + "..."
		}
		switch {
		case entry == "":
			return fmt.Errorf("%s[%d]: empty entry; each entry must be an IP address or a CIDR range", TrustedProxiesKey, i)
		case strings.Contains(entry, "%"):
			return fmt.Errorf("%s[%d]: %q has an IPv6 zone suffix, which is not supported", TrustedProxiesKey, i, shown)
		}
		if _, _, err := net.ParseCIDR(entry); err == nil {
			continue
		}
		if net.ParseIP(entry) != nil {
			continue
		}
		return fmt.Errorf("%s[%d]: %q is not an IP address or a CIDR range", TrustedProxiesKey, i, shown)
	}
	return nil
}
