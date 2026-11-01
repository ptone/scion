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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/harness"
)

// stubUninterceptedMetadataCloudHost replaces the cloud-host detector for the
// rest of the test and counts its calls.
func stubUninterceptedMetadataCloudHost(t *testing.T, cloud CloudHost) *int {
	t.Helper()
	calls := 0
	orig := uninterceptedMetadataCloudHost
	uninterceptedMetadataCloudHost = func() CloudHost { calls++; return cloud }
	t.Cleanup(func() { uninterceptedMetadataCloudHost = orig })
	return &calls
}

// recordingPodman writes a mock podman that echoes its arguments and records
// that it ran, and returns its path and the record file's path.
func recordingPodman(t *testing.T) (command, ranFile string) {
	t.Helper()
	dir := t.TempDir()
	command = filepath.Join(dir, "mock-podman")
	ranFile = filepath.Join(dir, "ran")
	script := "#!/bin/sh\necho ran > '" + ranFile + "'\necho \"$@\"\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write mock podman: %v", err)
	}
	return command, ranFile
}

func uninterceptedMetadataRunConfig(interception, enabled, ack bool) RunConfig {
	return RunConfig{
		Harness:                       &harness.Generic{},
		Name:                          "test-agent",
		UnixUsername:                  "scion",
		Image:                         "scion-agent:latest",
		Task:                          "hello",
		Env:                           []string{"SCION_METADATA_MODE=block"},
		MetadataInterception:          interception,
		AllowUninterceptedMetadata:    enabled,
		UninterceptedMetadataCloudAck: ack,
	}
}

var (
	cloudDetected   = CloudHost{Detected: true, Source: "dmi", Provider: "gce"}
	cloudUndetected = CloudHost{}
)

// TestPodmanRuntime_Run_RootlessUninterceptedMetadataRefused checks that
// rootless Podman refuses a start requesting the metadata interception
// unless the operator set both the opt-out and the cloud acknowledgement,
// on a detected cloud host and on an undetected one alike, and that the
// container is never started.
func TestPodmanRuntime_Run_RootlessUninterceptedMetadataRefused(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cloud       CloudHost
		enabled     bool
		ack         bool
		wantMissing string
		wantHost    string
	}{
		{name: "no opt-out, detected cloud", cloud: cloudDetected,
			wantMissing: UninterceptedMetadataSetting + ".enabled and " + UninterceptedMetadataSetting + ".acknowledge_cloud_host",
			wantHost:    "detected cloud host (gce)"},
		{name: "no opt-out, undetected host", cloud: cloudUndetected,
			wantMissing: UninterceptedMetadataSetting + ".enabled and " + UninterceptedMetadataSetting + ".acknowledge_cloud_host",
			wantHost:    "treated as a cloud host"},
		{name: "bare opt-out, detected cloud", cloud: cloudDetected, enabled: true,
			wantMissing: UninterceptedMetadataSetting + ".acknowledge_cloud_host",
			wantHost:    "detected cloud host (gce)"},
		{name: "bare opt-out, undetected host", cloud: cloudUndetected, enabled: true,
			wantMissing: UninterceptedMetadataSetting + ".acknowledge_cloud_host",
			wantHost:    "treated as a cloud host"},
		{name: "acknowledgement without opt-out", cloud: cloudUndetected, ack: true,
			wantMissing: UninterceptedMetadataSetting + ".enabled and " + UninterceptedMetadataSetting + ".acknowledge_cloud_host",
			wantHost:    "treated as a cloud host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubUninterceptedMetadataCloudHost(t, tc.cloud)
			command, ranFile := recordingPodman(t)
			rt := &PodmanRuntime{Command: command, Rootless: true}

			_, err := rt.Run(context.Background(), uninterceptedMetadataRunConfig(true, tc.enabled, tc.ack))
			if !errors.Is(err, ErrUninterceptedMetadataRefused) {
				t.Fatalf("Run error = %v, want ErrUninterceptedMetadataRefused", err)
			}
			for _, want := range []string{`"test-agent"`, "podman (rootless)", tc.wantHost, tc.wantMissing, "rootful Podman or Docker", "global settings"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if _, statErr := os.Stat(ranFile); !errors.Is(statErr, os.ErrNotExist) {
				t.Error("podman was invoked; a refused start must not start a container")
			}
		})
	}
}

// TestPodmanRuntime_Run_RootlessUninterceptedMetadataAcknowledged checks that
// the opt-out with the cloud acknowledgement allows the start on either kind
// of host, without NET_ADMIN or a required marker, and logs the
// acknowledgement at Warn.
func TestPodmanRuntime_Run_RootlessUninterceptedMetadataAcknowledged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cloud CloudHost
	}{
		{name: "detected cloud", cloud: cloudDetected},
		{name: "undetected host", cloud: cloudUndetected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubUninterceptedMetadataCloudHost(t, tc.cloud)
			var buf bytes.Buffer
			orig := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(orig) })

			command, _ := recordingPodman(t)
			rt := &PodmanRuntime{Command: command, Rootless: true}
			out, err := rt.Run(context.Background(), uninterceptedMetadataRunConfig(true, true, true))
			if err != nil {
				t.Fatalf("Run failed: %v", err)
			}
			if !strings.Contains(out, MetadataInterceptionEnvVar+"="+MetadataInterceptionOff) {
				t.Errorf("args %q lack the interception-off marker", out)
			}
			if strings.Contains(out, "--cap-add NET_ADMIN") {
				t.Errorf("args %q grant NET_ADMIN to a rootless container", out)
			}
			var ack string
			for _, line := range strings.Split(buf.String(), "\n") {
				if strings.Contains(line, "level=WARN") && strings.Contains(line, "allowed by the operator setting") {
					ack = line
				}
			}
			if ack == "" {
				t.Fatalf("no Warn acknowledgement logged; log was:\n%s", buf.String())
			}
			if !strings.Contains(ack, "setting="+UninterceptedMetadataSetting) {
				t.Errorf("acknowledgement %q does not name the setting", ack)
			}
		})
	}
}

// TestPodmanRuntime_Run_UninterceptedMetadataUnchanged checks that rootful
// Podman, and rootless Podman without a requested interception, start as
// before without consulting the opt-out or the cloud detector.
func TestPodmanRuntime_Run_UninterceptedMetadataUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rootless     bool
		interception bool
		wantMarker   string
		wantNetAdmin bool
	}{
		{name: "rootful with interception", rootless: false, interception: true, wantMarker: MetadataInterceptionRequired, wantNetAdmin: true},
		{name: "rootful without interception", rootless: false, interception: false, wantMarker: MetadataInterceptionOff},
		{name: "rootless without interception", rootless: true, interception: false, wantMarker: MetadataInterceptionOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubUninterceptedMetadataCloudHost(t, cloudDetected)
			command, _ := recordingPodman(t)
			rt := &PodmanRuntime{Command: command, Rootless: tc.rootless}
			out, err := rt.Run(context.Background(), uninterceptedMetadataRunConfig(tc.interception, false, false))
			if err != nil {
				t.Fatalf("Run failed: %v", err)
			}
			if !strings.Contains(out, MetadataInterceptionEnvVar+"="+tc.wantMarker) {
				t.Errorf("args %q lack marker %q", out, tc.wantMarker)
			}
			if got := strings.Contains(out, "--cap-add NET_ADMIN"); got != tc.wantNetAdmin {
				t.Errorf("NET_ADMIN granted = %v, want %v (args %q)", got, tc.wantNetAdmin, out)
			}
			if *calls != 0 {
				t.Errorf("cloud detector called %d times, want 0", *calls)
			}
		})
	}
}
