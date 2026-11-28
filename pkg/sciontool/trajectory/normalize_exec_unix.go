//go:build unix

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

package trajectory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/procreap"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/rootexec"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/suppgroups"
)

// SandboxExecutor executes a trajectory normalizer by re-execing sciontool
// (`internal normalize-exec`) with dropped workload credentials (or under the
// current non-root identity when AllowCurrentNonRoot is set in a non-root job
// container), a clean from-scratch environment, rlimits, process-group timeout
// termination, and bounded stdout/stderr capture.
type SandboxExecutor struct {
	cfg     SandboxExecutorConfig
	selfExe string

	// test-only knobs; never exported.
	geteuidFn           func() int
	maxStdoutLineBytes  int
	maxStdoutTotalBytes int
	maxStderrBytes      int
}

func (e *SandboxExecutor) currentEUID() int {
	if e.geteuidFn != nil {
		return e.geteuidFn()
	}
	return os.Geteuid()
}

// shouldDropCredentials reports whether the child must have
// SysProcAttr.Credential set, or returns an error if the configuration must
// fail closed (for example, running as root with no valid WorkloadUID/GID).
func validateCredentialMode(cfg SandboxExecutorConfig, euid int) (dropCred bool, err error) {
	if cfg.AllowCurrentNonRoot && euid > 0 {
		// Already non-root (e.g. non-root, read-only-rootfs converter job
		// container without CAP_SETUID/CAP_SETGID): omit SysProcAttr.Credential
		// so the kernel does not reject setgroups/setuid with EPERM.
		return false, nil
	}
	if cfg.WorkloadUID <= 0 || cfg.WorkloadGID <= 0 {
		if cfg.AllowCurrentNonRoot && euid == 0 {
			return false, fmt.Errorf("trajectory normalize executor: refusing to skip credential drop while running as root (uid=%d gid=%d)", cfg.WorkloadUID, cfg.WorkloadGID)
		}
		return false, fmt.Errorf("trajectory normalize executor: valid non-root workload uid/gid required (uid=%d gid=%d)", cfg.WorkloadUID, cfg.WorkloadGID)
	}
	return true, nil
}

// NewSandboxExecutor constructs a SandboxExecutor, failing closed if
// WorkloadUID or WorkloadGID is <= 0 (unless AllowCurrentNonRoot is true and
// the current process is already non-root) or if SelfExe (when set) is not
// absolute.
func NewSandboxExecutor(cfg SandboxExecutorConfig) (*SandboxExecutor, error) {
	return newSandboxExecutorWithEUID(cfg, os.Geteuid)
}

func newSandboxExecutorWithEUID(cfg SandboxExecutorConfig, geteuidFn func() int) (*SandboxExecutor, error) {
	if _, err := validateCredentialMode(cfg, geteuidFn()); err != nil {
		return nil, err
	}
	if cfg.HomeDir == "" {
		return nil, errors.New("trajectory normalize executor: HomeDir is required")
	}
	selfExe := cfg.SelfExe
	if selfExe == "" {
		selfExe = rootexec.SelfExe()
	} else if !filepath.IsAbs(selfExe) || !strings.HasPrefix(selfExe, "/") {
		return nil, fmt.Errorf("trajectory normalize executor: SelfExe must be an absolute path, got %q", selfExe)
	}
	return &SandboxExecutor{
		cfg:                 cfg,
		selfExe:             selfExe,
		geteuidFn:           geteuidFn,
		maxStdoutLineBytes:  MaxNormalizeStdoutLineBytes,
		maxStdoutTotalBytes: MaxNormalizeStdoutTotalBytes,
		maxStderrBytes:      MaxNormalizeStderrBytes,
	}, nil
}

// buildCommand constructs the *exec.Cmd for req without starting it. Splitting
// this out lets unit tests verify SysProcAttr.Credential, Setpgid, Env, Dir,
// and fail-closed UID/GID checks without requiring root privileges.
func (e *SandboxExecutor) buildCommand(ctx context.Context, req NormalizeExecRequest) (*exec.Cmd, error) {
	if len(req.Command) == 0 || req.Command[0] == "" {
		return nil, errors.New("trajectory normalize executor: command is required")
	}
	if !filepath.IsAbs(req.Command[0]) || !strings.HasPrefix(req.Command[0], "/") {
		return nil, fmt.Errorf("trajectory normalize executor: command[0] must be an absolute path, got %q", req.Command[0])
	}

	cpuSeconds := req.Limits.CPUSeconds
	if cpuSeconds < 0 {
		return nil, fmt.Errorf("trajectory normalize executor: cpu_seconds must be non-negative, got %d", cpuSeconds)
	}
	if cpuSeconds == 0 {
		cpuSeconds = DefaultNormalizeCPUSeconds
	}

	asMiB := req.Limits.MaxAddressSpaceMiB
	if asMiB < 0 {
		return nil, fmt.Errorf("trajectory normalize executor: max_address_space_mib must be non-negative, got %d", asMiB)
	}
	if asMiB == 0 {
		asMiB = DefaultNormalizeMaxAddressSpaceMiB
	}

	dropCred, err := validateCredentialMode(e.cfg, e.currentEUID())
	if err != nil {
		return nil, err
	}

	selfExe := e.selfExe
	if selfExe == "" {
		selfExe = rootexec.SelfExe()
	}

	shimArgs := make([]string, 0, 7+len(req.Command))
	shimArgs = append(shimArgs,
		"internal", "normalize-exec",
		"--as-mib", strconv.Itoa(asMiB),
		"--cpu-seconds", strconv.Itoa(cpuSeconds),
		"--",
	)
	shimArgs = append(shimArgs, req.Command...)

	cmd := exec.CommandContext(ctx, selfExe, shimArgs...)
	cmd.Dir = "/"
	cmd.Env = BuildNormalizeEnv(e.cfg.HomeDir)

	sysAttr := &syscall.SysProcAttr{
		Setpgid: true,
	}
	if dropCred {
		sysAttr.Credential = suppgroups.Credential(uint32(e.cfg.WorkloadUID), uint32(e.cfg.WorkloadGID))
	}
	cmd.SysProcAttr = sysAttr

	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second

	return cmd, nil
}

// Execute runs req inside the normalizer sandbox using procreap.RunManaged so
// PID 1's zombie reaper never races cmd.Wait().
func (e *SandboxExecutor) Execute(ctx context.Context, req NormalizeExecRequest) (*NormalizeExecResult, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultNormalizeTimeout
	}

	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, timeout)
	defer timeoutCancel()

	runCtx, runCancel := context.WithCancel(timeoutCtx)
	defer runCancel()

	cmd, err := e.buildCommand(runCtx, req)
	if err != nil {
		return nil, err
	}

	maxLine := e.maxStdoutLineBytes
	if maxLine <= 0 {
		maxLine = MaxNormalizeStdoutLineBytes
	}
	maxTotal := e.maxStdoutTotalBytes
	if maxTotal <= 0 {
		maxTotal = MaxNormalizeStdoutTotalBytes
	}
	maxStderr := e.maxStderrBytes
	if maxStderr <= 0 {
		maxStderr = MaxNormalizeStderrBytes
	}

	stdoutW := newCappedLineWriter(maxLine, maxTotal, runCancel)
	stderrW := newCappedBuffer(maxStderr)

	cmd.Stdin = bytes.NewReader(req.Input)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	runErr := procreap.RunManaged(cmd)

	lines, stdoutExceeded := stdoutW.finalize()
	stderrBytes, stderrTruncated := stderrW.result()

	res := &NormalizeExecResult{
		StdoutLines:     lines,
		Stderr:          stderrBytes,
		StderrTruncated: stderrTruncated,
		ExitCode:        -1,
		TimedOut:        errors.Is(timeoutCtx.Err(), context.DeadlineExceeded),
		StdoutExceeded:  stdoutExceeded,
	}

	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = ws.Signal().String()
		}
		if rusage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok && rusage != nil && rusage.Maxrss > 0 {
			if runtime.GOOS == "darwin" {
				res.PeakRSSBytes = rusage.Maxrss
			} else {
				res.PeakRSSBytes = rusage.Maxrss * 1024
			}
		}
	}

	// If the process never started (no ProcessState) and was not timed out or
	// cap-killed, surface the start error to the caller.
	if runErr != nil && cmd.ProcessState == nil && !res.TimedOut && !res.StdoutExceeded {
		return nil, fmt.Errorf("trajectory normalize executor: %w", runErr)
	}
	// If the caller's parent context was canceled (distinct from our own
	// per-round timeout), propagate the cancellation alongside partial results.
	if ctx.Err() != nil {
		return res, ctx.Err()
	}

	return res, nil
}
