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
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

func TestSubstrateEgressHostnames_TelemetryHost(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "SCION_OTEL_ENDPOINT",
			env:  map[string]string{"SCION_OTEL_ENDPOINT": "otel-collector.example.com:4317"},
			want: "otel-collector.example.com",
		},
		{
			name: "OTEL_EXPORTER_OTLP_ENDPOINT",
			env:  map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otlp.example.com:4318"},
			want: "otlp.example.com",
		},
		{
			name: "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
			env:  map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://traces.example.com:4318/v1/traces"},
			want: "traces.example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hosts := substrateEgressHostnames(RunConfig{}, tc.env, config.V1SubstrateConfig{})
			if !containsHost(hosts, tc.want) {
				t.Errorf("substrateEgressHostnames() = %v, want it to contain %q (from %s)", hosts, tc.want, tc.name)
			}
		})
	}
}

func TestSubstrateEgressHostnames_TelemetryHostsAreIndependent(t *testing.T) {
	// A deployment could point different OTel signals at different
	// collectors; both must survive, not just the first one found.
	env := map[string]string{
		"SCION_OTEL_ENDPOINT":         "cloud-otel.example.com:4317",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "self-hosted-otel.example.com:4318",
	}
	hosts := substrateEgressHostnames(RunConfig{}, env, config.V1SubstrateConfig{})
	if !containsHost(hosts, "cloud-otel.example.com") {
		t.Errorf("substrateEgressHostnames() = %v, missing SCION_OTEL_ENDPOINT host", hosts)
	}
	if !containsHost(hosts, "self-hosted-otel.example.com") {
		t.Errorf("substrateEgressHostnames() = %v, missing OTEL_EXPORTER_OTLP_ENDPOINT host", hosts)
	}
}

func TestSubstrateEgressHostnames_NoTelemetryEnv(t *testing.T) {
	hosts := substrateEgressHostnames(RunConfig{}, map[string]string{}, config.V1SubstrateConfig{})
	// The hardcoded model hosts (including *.googleapis.com, which happens
	// to cover the Cloud Trace default) are always present regardless.
	if !containsHost(hosts, "*.googleapis.com") {
		t.Errorf("substrateEgressHostnames() = %v, want the hardcoded model hosts present even with no telemetry env", hosts)
	}
}

func containsHost(hosts []string, want string) bool {
	for _, h := range hosts {
		if h == want {
			return true
		}
	}
	return false
}
