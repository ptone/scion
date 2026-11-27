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

package teamshosts

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultAllowedHosts_ReturnsCopy(t *testing.T) {
	h1 := DefaultAllowedHosts()
	require.Len(t, h1, 4)
	h1[0] = "mutated.example"

	h2 := DefaultAllowedHosts()
	assert.Equal(t, "smba.trafficmanager.net", h2[0])
}

func TestValidateServiceURL(t *testing.T) {
	t.Run("built-in hosts accepted", func(t *testing.T) {
		for _, host := range DefaultAllowedHosts() {
			assert.NoError(t, ValidateServiceURL("https://"+host+"/amer/"))
			assert.NoError(t, ValidateServiceURL("https://"+host+":443/amer/"))
		}
		assert.NoError(t, ValidateServiceURL("https://SMBA.TRAFFICMANAGER.NET/emea/"))
	})

	t.Run("extra hosts extend default list without replacing it", func(t *testing.T) {
		assert.NoError(t, ValidateServiceURL("https://emulator.local/v3/", "emulator.local"))
		assert.NoError(t, ValidateServiceURL("https://smba.trafficmanager.net/amer/", "emulator.local"))
		assert.Error(t, ValidateServiceURL("https://other.example/v3/", "emulator.local"))
	})

	rejected := []struct {
		name   string
		rawURL string
		reason string
	}{
		{"empty", "", "empty_url"},
		{"http scheme", "http://smba.trafficmanager.net/amer/", "invalid_scheme"},
		{"userinfo", "https://user:pass@smba.trafficmanager.net/amer/", "userinfo_not_allowed"},
		{"non-443 port", "https://smba.trafficmanager.net:8443/amer/", "invalid_port"},
		{"unlisted host", "https://unlisted.example/amer/", "host_not_allowed"},
		{"subdomain spoofing", "https://smba.trafficmanager.net.evil.example/amer/", "host_not_allowed"},
		{"missing host", "https:///amer/", "empty_host"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateServiceURL(tc.rawURL)
			require.Error(t, err)
			var denied *ServiceURLDeniedError
			require.True(t, errors.As(err, &denied))
			assert.Equal(t, tc.reason, denied.Reason)
		})
	}
}

func TestParseExtraHosts(t *testing.T) {
	assert.Nil(t, ParseExtraHosts(""))
	assert.Nil(t, ParseExtraHosts("   "))
	assert.Equal(t, []string{"a.example", "b.example"}, ParseExtraHosts(" A.Example , , b.example "))
}
