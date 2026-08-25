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
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCloudRunSandboxRuntime_Name(t *testing.T) {
	rt := NewCloudRunSandboxRuntime()
	if rt.Name() != "cloudrun-sandbox" {
		t.Errorf("Name() = %q, want %q", rt.Name(), "cloudrun-sandbox")
	}
}

func TestCloudRunSandboxRuntime_ExecUser(t *testing.T) {
	rt := NewCloudRunSandboxRuntime()
	if rt.ExecUser() != "scion" {
		t.Errorf("ExecUser() = %q, want %q", rt.ExecUser(), "scion")
	}
}

func TestCloudRunSandboxRuntime_LifecycleMethodsReturnNotImplemented(t *testing.T) {
	rt := NewCloudRunSandboxRuntime()
	ctx := context.Background()

	methods := []struct {
		name string
		fn   func() error
	}{
		{"Run", func() error { _, err := rt.Run(ctx, RunConfig{}); return err }},
		{"Stop", func() error { return rt.Stop(ctx, "x") }},
		{"Delete", func() error { return rt.Delete(ctx, "x") }},
		{"Attach", func() error { return rt.Attach(ctx, "x") }},
		{"Sync", func() error { return rt.Sync(ctx, "x", SyncTo) }},
	}

	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			err := m.fn()
			if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
				t.Errorf("%s() error = %v, want 'not yet implemented'", m.name, err)
			}
		})
	}

	t.Run("List", func(t *testing.T) {
		_, err := rt.List(ctx, nil)
		if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
			t.Errorf("List() error = %v, want 'not yet implemented'", err)
		}
	})

	t.Run("GetLogs", func(t *testing.T) {
		_, err := rt.GetLogs(ctx, "x")
		if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
			t.Errorf("GetLogs() error = %v, want 'not yet implemented'", err)
		}
	})

	t.Run("Exec", func(t *testing.T) {
		_, err := rt.Exec(ctx, "x", []string{"ls"})
		if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
			t.Errorf("Exec() error = %v, want 'not yet implemented'", err)
		}
	})

	t.Run("GetWorkspacePath", func(t *testing.T) {
		_, err := rt.GetWorkspacePath(ctx, "x")
		if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
			t.Errorf("GetWorkspacePath() error = %v, want 'not yet implemented'", err)
		}
	})
}

func TestCloudRunSandboxRuntime_ImageMethodsNoOp(t *testing.T) {
	rt := NewCloudRunSandboxRuntime()
	ctx := context.Background()

	t.Run("ImageExists", func(t *testing.T) {
		exists, err := rt.ImageExists(ctx, "any-image")
		if err != nil {
			t.Errorf("ImageExists() error = %v, want nil", err)
		}
		if !exists {
			t.Errorf("ImageExists() = %v, want true (omni-image)", exists)
		}
	})

	t.Run("ImageID", func(t *testing.T) {
		id, err := rt.ImageID(ctx, "any-image")
		if err != nil {
			t.Errorf("ImageID() error = %v, want nil", err)
		}
		if id != "omni-image" {
			t.Errorf("ImageID() = %q, want %q", id, "omni-image")
		}
	})

	t.Run("RemoveImage", func(t *testing.T) {
		err := rt.RemoveImage(ctx, "any-image")
		if err != nil {
			t.Errorf("RemoveImage() error = %v, want nil (no-op)", err)
		}
	})

	t.Run("PullImage", func(t *testing.T) {
		err := rt.PullImage(ctx, "any-image")
		if err != nil {
			t.Errorf("PullImage() error = %v, want nil (no-op)", err)
		}
	})
}

func TestSandboxLauncherAvailable(t *testing.T) {
	// The default sandbox binary path should not exist in the test environment
	if SandboxLauncherAvailable() {
		t.Skip("sandbox binary found at default path; cannot test absence")
	}

	t.Run("absent", func(t *testing.T) {
		if SandboxLauncherAvailable() {
			t.Error("SandboxLauncherAvailable() = true, want false (no binary at default path)")
		}
	})
}

func TestGetRuntime_CloudRunSandbox_DirectProfileName(t *testing.T) {
	t.Setenv("PATH", "")

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("SCION_GROVE", "")

	globalDir := filepath.Join(tmpHome, ".scion")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	tmpWd := t.TempDir()
	if err := os.Chdir(tmpWd); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	r := GetRuntime("", "cloudrun-sandbox")
	if _, ok := r.(*CloudRunSandboxRuntime); !ok {
		t.Fatalf("expected *CloudRunSandboxRuntime from direct profile name, got %T", r)
	}
}

func TestGetRuntime_CloudRunSandbox_SettingsBased(t *testing.T) {
	t.Setenv("PATH", "")

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	globalDir := filepath.Join(tmpHome, ".scion")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}

	settings := `{
		"schema_version": "1",
		"active_profile": "sandbox",
		"runtimes": {
			"crs": {
				"type": "cloudrun-sandbox"
			}
		},
		"profiles": {
			"sandbox": {
				"runtime": "crs"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(globalDir, "settings.json"), []byte(settings), 0644); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	tmpWd := t.TempDir()
	if err := os.Chdir(tmpWd); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	r := GetRuntime("", "")
	if _, ok := r.(*CloudRunSandboxRuntime); !ok {
		t.Fatalf("expected *CloudRunSandboxRuntime from settings, got %T", r)
	}
}

func TestGetRuntime_CloudRunInstance_Precedence_Over_Docker(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Cloud Run Instance detection only applies on Linux")
	}

	t.Setenv("PATH", "")

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("SCION_GROVE", "")
	t.Setenv("CLOUD_RUN_INSTANCE", "instance-1")
	t.Setenv("K_SERVICE", "")

	oldWd, _ := os.Getwd()
	tmpWd := t.TempDir()
	if err := os.Chdir(tmpWd); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	// CLOUD_RUN_INSTANCE is set but the sandbox binary is not at the default
	// path, so the factory should pick cloudrun-instances (CloudRunRuntime).
	r := GetRuntime("", "")
	if _, ok := r.(*CloudRunRuntime); !ok {
		t.Errorf("expected *CloudRunRuntime (cloudrun-instances) when CLOUD_RUN_INSTANCE is set without sandbox binary, got %T", r)
	}
}
