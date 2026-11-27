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

// Package teamshosts defines the shared Bot Framework service URL host
// allowlist and validation helpers used by both the Hub Teams channel and
// the Teams broker plugin.
package teamshosts

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// defaultAllowedHosts is the closed set of Bot Framework service URL
// hostnames for Teams: the public cloud (all regions share this host and
// differ only in the path), GCC, GCC High and DoD. Source: Microsoft Teams
// platform documentation, "Send proactive messages" (global service URLs).
// The refusal message lists them in this order.
var defaultAllowedHosts = []string{
	"smba.trafficmanager.net",
	"smba.infra.gcc.teams.microsoft.com",
	"smba.infra.gov.teams.microsoft.us",
	"smba.infra.dod.teams.microsoft.us",
}

// DefaultAllowedHosts returns a copy of the built-in Bot Framework service URL
// host allowlist.
func DefaultAllowedHosts() []string {
	out := make([]string, len(defaultAllowedHosts))
	copy(out, defaultAllowedHosts)
	return out
}

// ServiceURLDeniedError is returned when a Bot Framework service URL fails
// scheme, structure, or host allowlist validation.
type ServiceURLDeniedError struct {
	Reason string
	Host   string
}

func (e *ServiceURLDeniedError) Error() string {
	if e.Host != "" {
		return fmt.Sprintf("teams service URL rejected (%s): host %q", e.Reason, e.Host)
	}
	return fmt.Sprintf("teams service URL rejected (%s)", e.Reason)
}

// ParseExtraHosts parses a comma-separated list of additional allowed hostnames
// into normalized lowercase entries, ignoring empty items.
func ParseExtraHosts(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		h := strings.ToLower(strings.TrimSpace(part))
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

// ValidateServiceURL validates rawURL against the Bot Framework service URL
// policy:
//   - must parse as a valid URL
//   - scheme must be "https"
//   - userinfo must be absent
//   - port must be empty or "443"
//   - lowercase hostname must exactly match DefaultAllowedHosts or extraHosts
//
// extraHosts can only extend the built-in allowlist, never replace or empty it.
func ValidateServiceURL(rawURL string, extraHosts ...string) error {
	if strings.TrimSpace(rawURL) == "" {
		return &ServiceURLDeniedError{Reason: "empty_url"}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return &ServiceURLDeniedError{Reason: "invalid_url"}
	}
	if u.Scheme != "https" {
		return &ServiceURLDeniedError{Reason: "invalid_scheme"}
	}
	if u.User != nil {
		return &ServiceURLDeniedError{Reason: "userinfo_not_allowed"}
	}
	if port := u.Port(); port != "" && port != "443" {
		return &ServiceURLDeniedError{Reason: "invalid_port", Host: strings.ToLower(u.Hostname())}
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return &ServiceURLDeniedError{Reason: "empty_host"}
	}
	if slices.Contains(defaultAllowedHosts, host) {
		return nil
	}
	for _, extra := range extraHosts {
		if h := strings.ToLower(strings.TrimSpace(extra)); h != "" && h == host {
			return nil
		}
	}
	return &ServiceURLDeniedError{Reason: "host_not_allowed", Host: host}
}
