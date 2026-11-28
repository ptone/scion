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

package commands

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	normalizeExecNoFileLimit uint64 = 256
	normalizeExecFSizeLimit  uint64 = 0
)

func setRlimitClamped(resource int, soft, hard uint64) error {
	var cur unix.Rlimit
	if err := unix.Getrlimit(resource, &cur); err != nil {
		return err
	}
	if cur.Max != unix.RLIM_INFINITY && hard > cur.Max {
		hard = cur.Max
	}
	if soft > hard {
		soft = hard
	}
	return unix.Setrlimit(resource, &unix.Rlimit{Cur: soft, Max: hard})
}

func execWithNormalizeLimits(args []string, asMiB, cpuSeconds int) error {
	if len(args) == 0 || !strings.HasPrefix(args[0], "/") {
		return fmt.Errorf("normalize-exec: command must be an absolute path")
	}

	// Capture environment before lowering RLIMIT_AS so small test limits do
	// not fail Go allocations prior to execve.
	env := os.Environ()

	if err := setRlimitClamped(unix.RLIMIT_NOFILE, normalizeExecNoFileLimit, normalizeExecNoFileLimit); err != nil {
		return fmt.Errorf("normalize-exec: set RLIMIT_NOFILE: %w", err)
	}
	if err := setRlimitClamped(unix.RLIMIT_FSIZE, normalizeExecFSizeLimit, normalizeExecFSizeLimit); err != nil {
		return fmt.Errorf("normalize-exec: set RLIMIT_FSIZE: %w", err)
	}
	// Set soft = cpuSeconds and hard = cpuSeconds+1 so the kernel delivers
	// SIGXCPU at the soft limit (terminating default-disposition processes
	// cleanly with SIGXCPU) and SIGKILL 1s later if SIGXCPU is caught/ignored.
	cpuSoft := uint64(cpuSeconds)
	cpuHard := cpuSoft + 1
	if err := setRlimitClamped(unix.RLIMIT_CPU, cpuSoft, cpuHard); err != nil {
		return fmt.Errorf("normalize-exec: set RLIMIT_CPU: %w", err)
	}

	asBytes := uint64(asMiB) * 1024 * 1024
	if err := setRlimitClamped(unix.RLIMIT_AS, asBytes, asBytes); err != nil {
		return fmt.Errorf("normalize-exec: set RLIMIT_AS: %w", err)
	}

	return syscall.Exec(args[0], args, env)
}
