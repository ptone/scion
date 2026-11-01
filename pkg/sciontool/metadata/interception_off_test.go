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

package metadata

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

func TestConfigFromEnv_InterceptionOff(t *testing.T) {
	tests := []struct {
		value string
		set   bool
		want  bool
	}{
		{value: "off", set: true, want: true},
		{value: "required", set: true, want: false},
		{value: "", set: true, want: false},
		{value: "OFF", set: true, want: false},
		{set: false, want: false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q set=%v", tt.value, tt.set), func(t *testing.T) {
			t.Setenv("SCION_METADATA_MODE", "block")
			t.Setenv(InterceptionEnvVar, tt.value)
			if !tt.set {
				if err := os.Unsetenv(InterceptionEnvVar); err != nil {
					t.Fatal(err)
				}
			}
			cfg := ConfigFromEnv()
			if cfg == nil {
				t.Fatal("ConfigFromEnv() = nil, want a block-mode config")
			}
			if cfg.InterceptionOff != tt.want {
				t.Errorf("InterceptionOff = %v, want %v", cfg.InterceptionOff, tt.want)
			}
		})
	}
}

// captureMetadataLog sends the sciontool log to a temp file for the test and
// returns a function that reads it.
func captureMetadataLog(t *testing.T) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.log")
	log.SetLogPath(path)
	t.Cleanup(func() { log.SetLogPath("") })
	return func() string {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(b)
	}
}

// TestConfigureMetadataInterception_NonRootSkipVisibility checks that a
// non-root start the runtime marked as interception-off (rootless Podman
// allowed by the operator) logs the skipped interception at Warn, while a
// start with no marker (for example on Kubernetes) does not.
func TestConfigureMetadataInterception_NonRootSkipVisibility(t *testing.T) {
	for _, tt := range []struct {
		name     string
		mode     string
		off      bool
		wantWarn bool
	}{
		{name: "interception off, block", mode: modeBlock, off: true, wantWarn: true},
		{name: "interception off, assign", mode: modeAssign, off: true, wantWarn: true},
		{name: "no marker, block", mode: modeBlock, off: false, wantWarn: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			readLog := captureMetadataLog(t)
			redirectCalled, blockCalled := stubInterception(t, nil, nil)
			s := New(Config{Mode: tt.mode, InterceptionOff: tt.off, Port: 18380})
			if err := s.configureMetadataInterception(1000); err != nil {
				t.Fatalf("configureMetadataInterception() error = %v, want nil", err)
			}
			if *redirectCalled || *blockCalled {
				t.Errorf("interception attempted as non-root (redirect %v, filter %v)", *redirectCalled, *blockCalled)
			}
			var warn string
			for _, line := range strings.Split(readLog(), "\n") {
				if strings.Contains(line, "[WARN]") && strings.Contains(line, "Metadata IP interception is not installed") {
					warn = line
				}
			}
			if (warn != "") != tt.wantWarn {
				t.Fatalf("Warn logged = %v, want %v (line %q)", warn != "", tt.wantWarn, warn)
			}
			if tt.wantWarn {
				for _, want := range []string{"not running as root", fmt.Sprintf("mode %q", tt.mode), "GCE_METADATA_HOST"} {
					if !strings.Contains(warn, want) {
						t.Errorf("warning %q does not contain %q", warn, want)
					}
				}
			}
		})
	}
}
