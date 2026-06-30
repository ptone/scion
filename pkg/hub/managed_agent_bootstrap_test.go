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

package hub

import (
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

func TestGenerateBootstrapScript(t *testing.T) {
	script := generateBootstrapScript("my-bootstrap-bucket", "v1.2.3")

	if !strings.Contains(script, "#!/bin/bash") {
		t.Error("script should start with shebang")
	}
	if !strings.Contains(script, "set -e") {
		t.Error("script should use set -e")
	}
	if !strings.Contains(script, "my-bootstrap-bucket") {
		t.Error("script should contain the GCS bucket name")
	}
	if !strings.Contains(script, "v1.2.3") {
		t.Error("script should contain the version")
	}
	if !strings.Contains(script, "storage.googleapis.com/my-bootstrap-bucket/bin/v1.2.3/linux-amd64") {
		t.Error("script should construct correct GCS URL")
	}
	if !strings.Contains(script, "/usr/local/bin/scion") {
		t.Error("script should download scion binary")
	}
	if !strings.Contains(script, "/usr/local/bin/sciontool") {
		t.Error("script should download sciontool binary")
	}
	if !strings.Contains(script, "${SCION_TOKEN}") {
		t.Error("script should reference SCION_TOKEN env var")
	}
	if !strings.Contains(script, "~/.scion/scion-token") {
		t.Error("script should write token file")
	}
	if !strings.Contains(script, "sciontool heartbeat &") {
		t.Error("script should start heartbeat in the background")
	}
}

func TestGenerateBootstrapScript_DifferentValues(t *testing.T) {
	script := generateBootstrapScript("other-bucket", "abc12345")

	if !strings.Contains(script, "other-bucket") {
		t.Error("script should use the provided bucket name")
	}
	if !strings.Contains(script, "abc12345") {
		t.Error("script should use the provided version")
	}
}

func TestBootstrapBucketFromSettings(t *testing.T) {
	t.Run("nil settings", func(t *testing.T) {
		if got := bootstrapBucketFromSettings(nil); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("no managed_agents section", func(t *testing.T) {
		vs := &config.VersionedSettings{}
		if got := bootstrapBucketFromSettings(vs); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("no bootstrap section", func(t *testing.T) {
		vs := &config.VersionedSettings{
			ManagedAgents: &config.V1ManagedAgentsConfig{},
		}
		if got := bootstrapBucketFromSettings(vs); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("bucket configured", func(t *testing.T) {
		vs := &config.VersionedSettings{
			ManagedAgents: &config.V1ManagedAgentsConfig{
				Bootstrap: &config.V1ManagedAgentsBootstrapConfig{
					GCSBucket: "my-bucket",
				},
			},
		}
		if got := bootstrapBucketFromSettings(vs); got != "my-bucket" {
			t.Errorf("expected %q, got %q", "my-bucket", got)
		}
	})
}

func TestBuildManagedEnvironment_NoBucketFallback(t *testing.T) {
	srv, _ := testServer(t)

	agent := &store.Agent{
		ID:        "agent-1",
		Slug:      "test-agent",
		ProjectID: "proj-1",
	}

	env, err := srv.buildManagedEnvironment(agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env == nil {
		t.Fatal("expected non-nil environment")
	}
	if env.Type != "remote" {
		t.Errorf("expected type %q, got %q", "remote", env.Type)
	}
	if len(env.Sources) != 0 {
		t.Errorf("expected no sources for fallback, got %d", len(env.Sources))
	}
}

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		url    string
		domain string
	}{
		{"https://hub.example.com", "hub.example.com"},
		{"https://hub.example.com:8443/api", "hub.example.com"},
		{"http://localhost:9810", "localhost"},
		{"https://192.168.1.1:443", "192.168.1.1"},
		{"not-a-url", ""}, // Go's url.Parse treats this as a relative path, so Hostname() returns ""
		{"", ""},
	}

	for _, tt := range tests {
		got := extractDomain(tt.url)
		if got != tt.domain {
			t.Errorf("extractDomain(%q) = %q, want %q", tt.url, got, tt.domain)
		}
	}
}
