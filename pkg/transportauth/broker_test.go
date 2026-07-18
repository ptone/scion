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

package transportauth

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockADCSource implements TokenSource for testing.
type mockADCSource struct {
	audience string
	token    string
	expiry   time.Time
}

func (m *mockADCSource) Token() (string, error) {
	if m.token == "" {
		return "", fmt.Errorf("no token")
	}
	return m.token, nil
}

func (m *mockADCSource) SetToken(token string, expiry time.Time) {
	m.token = token
	m.expiry = expiry
}

func (m *mockADCSource) Expiry() time.Time {
	return m.expiry
}

func mockADCNew(audience string) (TokenSource, error) {
	return &mockADCSource{audience: audience, token: makeTestJWT(time.Now().Add(1 * time.Hour))}, nil
}

func mockADCNewFailing(audience string) (TokenSource, error) {
	return nil, fmt.Errorf("ADC not available")
}

func TestResolveBrokerTransport_MetadataPrecedence(t *testing.T) {
	cleanup := overrideGCPDetection(true)
	defer cleanup()

	_ = os.Unsetenv(EnvMetadataMode)
	t.Setenv(EnvTransportAudience, "https://audience.example.com")

	src, err := ResolveBrokerTransport(mockADCNew)
	require.NoError(t, err)
	require.NotNil(t, src)
	_, ok := src.(*MetadataSource)
	assert.True(t, ok, "should return MetadataSource when on GCE")
}

func TestResolveBrokerTransport_ADCFallback(t *testing.T) {
	cleanup := overrideGCPDetection(false)
	defer cleanup()

	t.Setenv(EnvTransportAudience, "https://audience.example.com")

	src, err := ResolveBrokerTransport(mockADCNew)
	require.NoError(t, err)
	require.NotNil(t, src)
	_, ok := src.(*mockADCSource)
	assert.True(t, ok, "should fall back to ADC when not on GCE")
}

func TestResolveBrokerTransport_ADCFallbackWhenMetadataBlocked(t *testing.T) {
	cleanup := overrideGCPDetection(true)
	defer cleanup()

	t.Setenv(EnvMetadataMode, "assign")
	t.Setenv(EnvTransportAudience, "https://audience.example.com")

	src, err := ResolveBrokerTransport(mockADCNew)
	require.NoError(t, err)
	require.NotNil(t, src)
	_, ok := src.(*mockADCSource)
	assert.True(t, ok, "should fall back to ADC when SCION_METADATA_MODE is set")
}

func TestResolveBrokerTransport_NoAudience(t *testing.T) {
	_ = os.Unsetenv(EnvTransportAudience)
	_ = os.Unsetenv(EnvHubOIDCAudience)

	src, err := ResolveBrokerTransport(mockADCNew)
	require.NoError(t, err)
	assert.Nil(t, src, "should return nil when no audience configured")
}

func TestResolveBrokerTransport_LegacyAudience(t *testing.T) {
	cleanup := overrideGCPDetection(false)
	defer cleanup()

	_ = os.Unsetenv(EnvTransportAudience)
	t.Setenv(EnvHubOIDCAudience, "https://legacy-audience.example.com")

	src, err := ResolveBrokerTransport(mockADCNew)
	require.NoError(t, err)
	require.NotNil(t, src, "should use SCION_HUB_OIDC_AUDIENCE as fallback")
}

func TestResolveBrokerTransport_NilADCConstructor(t *testing.T) {
	cleanup := overrideGCPDetection(false)
	defer cleanup()

	t.Setenv(EnvTransportAudience, "https://audience.example.com")

	src, err := ResolveBrokerTransport(nil)
	require.NoError(t, err)
	assert.Nil(t, src, "should return nil when ADC constructor is nil and not on GCE")
}

func TestResolveBrokerTransport_ADCError(t *testing.T) {
	cleanup := overrideGCPDetection(false)
	defer cleanup()

	t.Setenv(EnvTransportAudience, "https://audience.example.com")

	_, err := ResolveBrokerTransport(mockADCNewFailing)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ADC not available")
}

func TestResolveBrokerTransportWithAudience_EmptyAudience(t *testing.T) {
	src, err := ResolveBrokerTransportWithAudience("", mockADCNew)
	require.NoError(t, err)
	assert.Nil(t, src)
}
