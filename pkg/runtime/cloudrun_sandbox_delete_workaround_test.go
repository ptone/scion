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
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// newTestRuntime creates a CloudRunSandboxRuntime with a shell-script
// binary that executes the given script body. The deleteTimeout is set
// to a short value so tests complete quickly.
func newTestRuntime(t *testing.T, script string) *CloudRunSandboxRuntime {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "sandbox")
	err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	rt := NewCloudRunSandboxRuntime(bin)
	rt.deleteTimeout = 100 * time.Millisecond
	return rt
}

// --- deleteWithTimeout tests ---

func TestCloudRunSandboxDeleteWithTimeout_NormalCompletion(t *testing.T) {
	rt := newTestRuntime(t, "exit 0")

	err := rt.deleteWithTimeout(context.Background(), "test-sandbox")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestCloudRunSandboxDeleteWithTimeout_Timeout(t *testing.T) {
	rt := newTestRuntime(t, "sleep 100")
	rt.deleteTimeout = 100 * time.Millisecond

	start := time.Now()
	err := rt.deleteWithTimeout(context.Background(), "test-sandbox")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error (timeout treated as success), got %v", err)
	}
	// Should complete near the timeout, not after 100s.
	if elapsed > 5*time.Second {
		t.Errorf("took %v, expected near %v", elapsed, rt.deleteTimeout)
	}
}

func TestCloudRunSandboxDeleteWithTimeout_ContextCancellation(t *testing.T) {
	rt := newTestRuntime(t, "sleep 100")
	rt.deleteTimeout = 5 * time.Second // long enough that timeout won't fire first

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := rt.deleteWithTimeout(ctx, "test-sandbox")
	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestCloudRunSandboxDeleteWithTimeout_ProcessErrorNonFatal(t *testing.T) {
	rt := newTestRuntime(t, "exit 1")

	err := rt.deleteWithTimeout(context.Background(), "test-sandbox")
	if err != nil {
		t.Fatalf("expected nil error (process error is non-fatal in workaround), got %v", err)
	}
}

func TestCloudRunSandboxDeleteWithTimeout_CancelsWatcher(t *testing.T) {
	rt := newTestRuntime(t, "exit 0")

	// Set up a watcher cancel for the sandbox.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	rt.watchMu.Lock()
	rt.watchCancels["test-sandbox"] = watchCancel
	rt.watchMu.Unlock()

	// deleteOrWorkaround should cancel the watcher before dispatching.
	err := rt.deleteOrWorkaround(context.Background(), "test-sandbox")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the watcher context was cancelled.
	select {
	case <-watchCtx.Done():
		// expected
	default:
		t.Error("watcher context was not cancelled")
	}

	// Verify the cancel was removed from the map.
	rt.watchMu.Lock()
	_, exists := rt.watchCancels["test-sandbox"]
	rt.watchMu.Unlock()
	if exists {
		t.Error("watcher cancel should have been removed from map")
	}
}

func TestCloudRunSandboxDeleteWithTimeout_DefaultTimeout(t *testing.T) {
	rt := newTestRuntime(t, "exit 0")
	rt.deleteTimeout = 0 // should use DefaultDeleteTimeout

	err := rt.deleteWithTimeout(context.Background(), "test-sandbox")
	if err != nil {
		t.Fatalf("expected nil error with default timeout, got %v", err)
	}
}

// --- killProcessGroup tests ---

func TestKillProcessGroup_NilProcess(t *testing.T) {
	cmd := &exec.Cmd{}
	// Should not panic when Process is nil.
	killProcessGroup(cmd)
}

func TestKillProcessGroup_RunningProcess(t *testing.T) {
	cmd := exec.Command("sleep", "100")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	killProcessGroup(cmd)

	err := cmd.Wait()
	if err == nil {
		t.Error("expected error from killed process, got nil")
	}
}

// --- reapOrphanedRunsc tests ---

func TestReapOrphanedRunsc_NoProc(t *testing.T) {
	// Should not panic when no matching processes exist.
	// On Linux /proc exists but won't have matching runsc processes.
	// On other platforms /proc may not exist (graceful no-op).
	reapOrphanedRunsc("nonexistent-sandbox-id")
}

// --- isOrphanedRunscProcess tests ---

func TestIsOrphanedRunscProcess(t *testing.T) {
	const sandbox = "test-sandbox-1"

	tests := []struct {
		name    string
		cmdline []byte
		want    bool
	}{
		{
			name: "genuine captured orphan",
			cmdline: []byte(
				"/usr/local/gcp/bin/runsc\x00" +
					"--platform=xemu\x00" +
					"--platform_device_path=/dev/xemu\x00" +
					"--root=/tmp/runsc-root\x00" +
					"--ignore-cgroups\x00" +
					"--TESTONLY-unsafe-nonroot\x00" +
					"--overlay2=root:memory\x00" +
					"--network=none\x00" +
					"delete\x00" +
					"--force\x00" +
					"test-sandbox-1\x00"),
			want: true,
		},
		{
			name: "near-miss substring",
			cmdline: []byte(
				"/usr/local/gcp/bin/runsc\x00" +
					"--platform=xemu\x00" +
					"delete\x00" +
					"--force\x00" +
					"test-sandbox-1-extra\x00"),
			want: false, // last arg doesn't exactly match
		},
		{
			name: "near-miss flag value",
			cmdline: []byte(
				"/usr/local/gcp/bin/runsc\x00" +
					"--sandbox=test-sandbox-1\x00" +
					"delete\x00" +
					"--force\x00" +
					"other-sandbox\x00"),
			want: false, // sandbox name appears as a flag value, not last arg
		},
		{
			name: "unrelated runsc process",
			cmdline: []byte(
				"/usr/local/gcp/bin/runsc\x00" +
					"--platform=xemu\x00" +
					"run\x00" +
					"test-sandbox-1\x00"),
			want: false, // "run" not "delete"
		},
		{
			name: "short cmdline",
			cmdline: []byte("/usr/local/gcp/bin/runsc\x00"),
			want:    false,
		},
		{
			name:    "empty cmdline",
			cmdline: []byte{},
			want:    false,
		},
		{
			name: "non-runsc binary",
			cmdline: []byte(
				"/usr/bin/python3\x00" +
					"delete\x00" +
					"--force\x00" +
					"test-sandbox-1\x00"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOrphanedRunscProcess(tt.cmdline, sandbox)
			if got != tt.want {
				t.Errorf("isOrphanedRunscProcess() = %v, want %v", got, tt.want)
			}
		})
	}
}
