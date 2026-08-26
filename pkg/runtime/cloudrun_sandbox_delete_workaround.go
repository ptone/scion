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

// This file is a workaround for an upstream Cloud Run Sandbox defect:
// `sandbox delete --force` never returns. The deletion IS effective --
// the sandbox really is gone -- but the CLI process hangs indefinitely.
//
// See defect-sandbox-delete-hang.md for the full investigation.
//
// KNOWN-BAD BUILD: runsc google-958767651 (spec 1.2.1, 2026-08-04).
//
// EXIT CRITERIA -- remove this file when ALL of the following hold
// on a runsc build NEWER than google-958767651:
//   1. `sandbox delete --force` returns within DefaultDeleteTimeout on a
//      sandbox with a live process (not just idle sandboxes).
//   2. No orphaned `runsc delete` process remains after the command returns.
//   3. The above holds across concurrent deletes (our actual access pattern).
//   4. The self-detecting WARN log ("upstream defect may be fixed") fires
//      on normal delete returns -- this is the primary removal trigger
//      since there is no public bug to watch.
//
// To remove: delete this file, revert Stop()/Delete() in
// cloudrun_sandbox_runtime.go to a plain exec of `sandbox delete --force`,
// and drop the SCION_CLOUDRUN_DELETE_WORKAROUND env var check.

package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// deleteDefectRef identifies the platform defect this file works around.
// Tracked internally by the Cloud Run team -- there is no public issue to cite.
// Observed on runsc google-958767651 (spec 1.2.1, 2026-08-04).
// Evidence and control matrix: .design/project-log/defect-sandbox-delete-hang.md
const deleteDefectRef = "cloudrun sandbox: 'sandbox delete --force' never returns; " +
	"see .design/project-log/defect-sandbox-delete-hang.md"

// DefaultDeleteTimeout is the timeout for sandbox delete --force.
// Chosen to be long enough that a working delete would always complete,
// but short enough that a hung delete does not block teardown.
const DefaultDeleteTimeout = 10 * time.Second

// deleteWorkaroundEnabled controls whether the timeout/reaper workaround
// is active. Set SCION_CLOUDRUN_DELETE_WORKAROUND=off to bypass.
var deleteWorkaroundEnabled = true

// deleteWorkaroundFixDetected fires a one-time WARN when delete returns
// normally, signaling that the upstream defect may have been fixed and
// this workaround is a candidate for removal.
var deleteWorkaroundFixDetected sync.Once

func init() {
	if os.Getenv("SCION_CLOUDRUN_DELETE_WORKAROUND") == "off" {
		deleteWorkaroundEnabled = false
		slog.Warn("Cloud Run delete workaround DISABLED via SCION_CLOUDRUN_DELETE_WORKAROUND=off",
			"defect", deleteDefectRef)
	}
}

// deleteWithTimeout runs `sandbox delete --force` with a timeout. When the
// command times out (the expected case with the platform bug), the process
// group is killed, orphaned runsc processes are reaped, and the delete is
// treated as successful (the sandbox really is gone despite the hang).
func (r *CloudRunSandboxRuntime) deleteWithTimeout(ctx context.Context, id string) error {
	timeout := r.deleteTimeout
	if timeout == 0 {
		timeout = DefaultDeleteTimeout
	}

	cmd := exec.Command(r.bin, "delete", "--force", id)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cloudrun-sandbox: failed to start delete --force: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			slog.Warn("sandbox delete --force returned with error",
				"sandbox", id, "error", err)
		} else {
			deleteWorkaroundFixDetected.Do(func() {
				slog.Warn("sandbox delete --force returned normally -- "+
					"upstream defect may be fixed; this workaround is a candidate for removal",
					"sandbox", id, "defect", deleteDefectRef)
			})
			slog.Info("sandbox delete --force completed normally",
				"sandbox", id)
		}
	case <-time.After(timeout):
		slog.Warn("sandbox delete --force timed out, treating as success",
			"sandbox", id, "timeout", timeout,
			"defect", deleteDefectRef)
		killProcessGroup(cmd)
		<-done // reap the zombie
		reapOrphanedRunsc(id)
	case <-ctx.Done():
		killProcessGroup(cmd)
		<-done
		return ctx.Err()
	}

	r.state.remove(id)
	return nil
}

// killProcessGroup sends SIGKILL to the entire process group of cmd.
// This ensures that child processes (e.g., runsc) are also killed.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Kill the entire process group (negative PID).
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// reapOrphanedRunsc scans /proc for orphaned `runsc delete` processes
// targeting the given sandbox and kills them. These orphans are left
// behind when `sandbox delete --force` hangs and we kill its process
// group — the runsc subprocess may have been reparented to init.
func reapOrphanedRunsc(sandboxName string) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		slog.Debug("reapOrphanedRunsc: cannot read /proc", "error", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if isOrphanedRunscProcess(cmdline, sandboxName) {
			slog.Warn("reapOrphanedRunsc: killing orphaned runsc delete process",
				"pid", pid, "sandbox", sandboxName)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// isOrphanedRunscProcess checks whether raw /proc/<pid>/cmdline bytes
// represent an orphaned `runsc delete` process for the given sandbox.
//
// It splits the cmdline on NUL bytes (the kernel's argv separator) and
// checks three conditions:
//   - argv[0] basename contains "runsc"
//   - argv contains "delete"
//   - the last element is exactly sandboxName
//
// Captured orphan argv (from the defect investigation):
//
//	/usr/local/gcp/bin/runsc --platform=xemu --platform_device_path=/dev/xemu \
//	  --root=/tmp/runsc-root --ignore-cgroups --TESTONLY-unsafe-nonroot \
//	  --overlay2=root:memory --network=none delete --force <sandbox-id>
func isOrphanedRunscProcess(cmdline []byte, sandboxName string) bool {
	// Split on NUL, filtering empty trailing entries from the
	// kernel's NUL-terminated format.
	parts := strings.Split(string(cmdline), "\x00")
	var args []string
	for _, p := range parts {
		if p != "" {
			args = append(args, p)
		}
	}
	if len(args) == 0 {
		return false
	}
	// argv[0] basename must contain "runsc".
	if !strings.Contains(filepath.Base(args[0]), "runsc") {
		return false
	}
	// Must contain the "delete" subcommand.
	if !slices.Contains(args, "delete") {
		return false
	}
	// Last element must be the sandbox name (exact equality, not substring).
	return args[len(args)-1] == sandboxName
}
