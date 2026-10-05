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

package harness

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// Resolve refuses a harness-config whose provisioner cannot run, with a
// typed error naming the harness-config, its directory and the fix
// (ptone/scion#611).
func TestResolve_UnusableProvisioner(t *testing.T) {
	for _, tc := range []struct {
		name, provisioner, wantReason string
	}{
		{"builtin", "  type: builtin\n  interface_version: 1\n", `provisioner.type "builtin"`},
		{"builtin-with-command", "  type: builtin\n  command: [\"python3\", \"x.py\"]\n", `provisioner.type "builtin"`},
		{"empty-command", "  type: container-script\n  interface_version: 1\n", "provisioner.command is empty"},
		{"untyped-empty-command", "  interface_version: 1\n", "provisioner.command is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			hcDir := filepath.Join(home, ".scion", "harness-configs", "hc")
			writeFile(t, filepath.Join(hcDir, "config.yaml"), "harness: claude\nimage: img:test\nprovisioner:\n"+tc.provisioner)
			writeFile(t, filepath.Join(hcDir, "provision.py"), "#!/usr/bin/env python3\n")

			_, err := Resolve(context.Background(), ResolveOptions{Name: "hc"})
			var ue *UnusableProvisionerError
			if !errors.As(err, &ue) || !errors.Is(err, ErrUnusableProvisioner) {
				t.Fatalf("expected UnusableProvisionerError, got %v", err)
			}
			if ue.Name != "hc" || ue.Path != hcDir || ue.Source != config.HarnessConfigSourceBrokerLocal {
				t.Errorf("error = %+v", ue)
			}
			msg := err.Error()
			for _, want := range []string{`"hc"`, hcDir, tc.wantReason, "scion harness-config upgrade hc --activate-script", "scion harness-config install harnesses/"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not contain %q", msg, want)
				}
			}
			if strings.Contains(msg, "harness-config sync") {
				t.Errorf("a broker-local config should not suggest a hub sync: %q", msg)
			}
		})
	}
}

// A hub-hydrated copy with an unusable provisioner is refused too, and the
// fix also names updating the hub copy.
func TestResolve_UnusableProvisioner_HubHydrated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hydrated := filepath.Join(t.TempDir(), "hc")
	writeFile(t, filepath.Join(hydrated, "config.yaml"), "harness: claude\nimage: img:test\nprovisioner:\n  type: builtin\n")

	_, err := Resolve(context.Background(), ResolveOptions{Name: "hc", ConfigDirPath: hydrated})
	var ue *UnusableProvisionerError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnusableProvisionerError, got %v", err)
	}
	if ue.Source != config.HarnessConfigSourceHubHydrated {
		t.Errorf("source = %q, want hub-hydrated", ue.Source)
	}
	if !strings.Contains(err.Error(), "scion harness-config sync hc") {
		t.Errorf("error does not name the hub update: %v", err)
	}
}

// A container-script provisioner with a command, and a harness-config with
// no provisioner block, still resolve.
func TestResolve_UsableProvisionerUnaffected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".scion", "harness-configs")
	writeFile(t, filepath.Join(dir, "cs", "config.yaml"), "harness: claude\nimage: img:test\nprovisioner:\n  type: container-script\n  command: [\"python3\", \"/home/scion/.scion/harness/provision.py\"]\n")
	writeFile(t, filepath.Join(dir, "cs", "provision.py"), "#!/usr/bin/env python3\n")
	writeFile(t, filepath.Join(dir, "decl", "config.yaml"), "harness: generic\nimage: img:test\n")

	if r, err := Resolve(context.Background(), ResolveOptions{Name: "cs"}); err != nil || r.Implementation != "container-script" {
		t.Errorf("container-script: r=%v err=%v", r, err)
	}
	if r, err := Resolve(context.Background(), ResolveOptions{Name: "decl"}); err != nil || r.Implementation != "generic" {
		t.Errorf("declarative: r=%v err=%v", r, err)
	}
}
