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
	"errors"
	"strings"
	"testing"
)

func TestValidateRouterEndpoint(t *testing.T) {
	// zz-uniq-host and the other markers must never appear in an error.
	refused := []struct {
		name     string
		endpoint string
	}{
		{"empty", ""},
		{"plaintext http on 80", "http://zz-uniq-host.ate-system.svc:80"},
		{"http on 443", "http://zz-uniq-host.ate-system.svc:443"},
		{"https on a non-443 port", "https://zz-uniq-host.ate-system.svc:8443"},
		{"bare https host (implied 443)", "https://zz-uniq-host.ate-system.svc"},
		{"bare https host with slash", "https://zz-uniq-host.ate-system.svc/"},
		{"empty port", "https://zz-uniq-host.ate-system.svc:"},
		{"no scheme", "zz-uniq-host.ate-system.svc:443"},
		{"scheme-relative", "//zz-uniq-host.ate-system.svc:443"},
		{"other scheme", "ftp://zz-uniq-host.ate-system.svc:443"},
		{"garbage", "%%zz-uniq-garbage%%"},
		{"unclosed IPv6 literal", "https://[zz:uniq:443"},
		{"userinfo", "https://zz-user:zz-secret@zz-uniq-host.ate-system.svc:443"},
		{"path", "https://zz-uniq-host.ate-system.svc:443/zz-path"},
		{"query", "https://zz-uniq-host.ate-system.svc:443?zz-query=1"},
		{"empty query", "https://zz-uniq-host.ate-system.svc:443?"},
		{"fragment", "https://zz-uniq-host.ate-system.svc:443#zz-frag"},
		{"empty host", "https://:443"},
		{"trailing space", "https://zz-uniq-host.ate-system.svc:443 "},
		{"interior newline", "https://zz-uniq-host.ate-system.svc:443\n"},
	}
	for _, tt := range refused {
		t.Run("refuses "+tt.name, func(t *testing.T) {
			err := ValidateRouterEndpoint(tt.endpoint)
			if err == nil {
				t.Fatalf("ValidateRouterEndpoint(%q) = nil, want a refusal", tt.endpoint)
			}
			if !errors.Is(err, ErrInvalidRouterEndpoint) {
				t.Errorf("error %v does not match ErrInvalidRouterEndpoint", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "https://<host>:443") {
				t.Errorf("error %q does not name the accepted form", msg)
			}
			for _, marker := range []string{"zz-", "8443", ":80"} {
				if strings.Contains(msg, marker) {
					t.Errorf("error %q echoes the configured value (%q)", msg, marker)
				}
			}
		})
	}

	accepted := []string{
		"https://atenet-router.ate-system.svc:443",
		"https://atenet-router.ate-system.svc:443/",
		"https://router.example.com:443",
	}
	for _, endpoint := range accepted {
		t.Run("accepts "+endpoint, func(t *testing.T) {
			if err := ValidateRouterEndpoint(endpoint); err != nil {
				t.Errorf("ValidateRouterEndpoint(%q) = %v, want nil", endpoint, err)
			}
		})
	}
}
