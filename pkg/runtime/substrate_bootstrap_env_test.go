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

package runtime

import (
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// TestBuildBootstrapEnv_GCPTelemetryCredentials_Present proves that when the
// well-known scion-telemetry-gcp-credentials file secret is delivered,
// buildBootstrapEnv points SCION_OTEL_GCP_CREDENTIALS at the same resolved
// path findGCPTelemetryCredentialPath itself returns for that secret set.
func TestBuildBootstrapEnv_GCPTelemetryCredentials_Present(t *testing.T) {
	secrets := []api.ResolvedSecret{
		{Name: "scion-telemetry-gcp-credentials", Type: "file", Target: "~/.config/gcp/sa.json", Value: "key-data"},
	}
	cfg := RunConfig{
		UnixUsername:    "scion",
		ResolvedSecrets: secrets,
	}

	want := findGCPTelemetryCredentialPath(secrets, util.GetHomeDir(cfg.UnixUsername))
	if want == "" {
		t.Fatal("test setup: findGCPTelemetryCredentialPath returned empty, fixture is broken")
	}

	env := buildBootstrapEnv(cfg)

	got, ok := env[telemetryGCPCredentialsEnvVar]
	if !ok {
		t.Fatalf("env[%q] absent, want present with value %q", telemetryGCPCredentialsEnvVar, want)
	}
	if got != want {
		t.Errorf("env[%q] = %q, want %q (the resolved secret path)", telemetryGCPCredentialsEnvVar, got, want)
	}
}

// TestBuildBootstrapEnv_GCPTelemetryCredentials_ContentNeverLeaks proves the
// secret's own content (as opposed to its resolved path) never appears
// anywhere in the built env map — not under the telemetry key, and not
// smuggled into any other key's value.
func TestBuildBootstrapEnv_GCPTelemetryCredentials_ContentNeverLeaks(t *testing.T) {
	const sentinelContent = "FAKE-SENTINEL-gcp-sa-key-content-not-a-real-credential"
	secrets := []api.ResolvedSecret{
		{Name: "scion-telemetry-gcp-credentials", Type: "file", Target: "~/.config/gcp/sa.json", Value: sentinelContent},
	}
	cfg := RunConfig{
		UnixUsername:    "scion",
		ResolvedSecrets: secrets,
	}

	env := buildBootstrapEnv(cfg)

	for k, v := range env {
		if strings.Contains(v, sentinelContent) {
			t.Errorf("secret content leaked into env[%q] = %q", k, v)
		}
		if strings.Contains(k, sentinelContent) {
			t.Errorf("secret content leaked into an env key: %q", k)
		}
	}
}

// TestBuildBootstrapEnv_GCPTelemetryCredentials_AbsentWhenNoSecret proves
// that without the well-known file secret, SCION_OTEL_GCP_CREDENTIALS is not
// merely empty but entirely absent from the env map.
func TestBuildBootstrapEnv_GCPTelemetryCredentials_AbsentWhenNoSecret(t *testing.T) {
	cfg := RunConfig{
		UnixUsername: "scion",
	}

	env := buildBootstrapEnv(cfg)

	if v, ok := env[telemetryGCPCredentialsEnvVar]; ok {
		t.Errorf("env[%q] = %q, want the key absent entirely", telemetryGCPCredentialsEnvVar, v)
	}
}
