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
	"sync"
	"time"
)

const (
	// DefaultNormalizeTimeout is the default wall-clock timeout per normalizer
	// invocation (contract D4).
	DefaultNormalizeTimeout = 60 * time.Second

	// DefaultNormalizeCPUSeconds is the default RLIMIT_CPU ceiling (contract D4).
	DefaultNormalizeCPUSeconds = 30

	// DefaultNormalizeMaxAddressSpaceMiB is the default RLIMIT_AS ceiling in
	// MiB (contract D4).
	DefaultNormalizeMaxAddressSpaceMiB = 1024

	// DefaultNormalizeMaxRSSMiB is the soft RSS budget in MiB (contract D4).
	DefaultNormalizeMaxRSSMiB = 256

	// MaxNormalizeStdoutLineBytes caps any single JSONL line emitted by a
	// normalizer (contract §2: 1 MiB).
	MaxNormalizeStdoutLineBytes = 1 << 20

	// MaxNormalizeStdoutTotalBytes caps total stdout bytes emitted by a
	// normalizer in one round (64 MiB).
	MaxNormalizeStdoutTotalBytes = 64 << 20

	// MaxNormalizeStderrBytes caps captured stderr bytes for diagnostics
	// (contract §2: 64 KiB).
	MaxNormalizeStderrBytes = 64 << 10

	// NormalizeExecPath is the fixed PATH in the clean normalizer environment
	// (contract D5).
	NormalizeExecPath = "/opt/scion/venv/bin:/usr/bin:/bin"
)

// NormalizeLimits holds the resource limits for one normalizer invocation.
type NormalizeLimits struct {
	CPUSeconds         int
	MaxAddressSpaceMiB int
	MaxRSSMiB          int
}

// NormalizeExecRequest describes one sandboxed normalizer execution.
type NormalizeExecRequest struct {
	// Command is the resolved argv to execute (Command[0] must be absolute).
	Command []string
	// Input is the single JSON request object written to stdin before closing it.
	Input []byte
	// Limits holds the resource limits; zero fields fall back to contract D4 defaults.
	Limits NormalizeLimits
	// Timeout is the wall-clock timeout; <= 0 falls back to DefaultNormalizeTimeout.
	Timeout time.Duration
}

// NormalizeExecResult captures the outcome of a sandboxed normalizer execution.
type NormalizeExecResult struct {
	// StdoutLines holds each JSONL line emitted to stdout (without trailing \n or \r\n).
	StdoutLines [][]byte
	// Stderr holds captured stderr, truncated to MaxNormalizeStderrBytes.
	Stderr []byte
	// StderrTruncated is true if stderr exceeded MaxNormalizeStderrBytes.
	StderrTruncated bool
	// ExitCode is the process exit code (0 on success, -1 if signaled or not exited).
	ExitCode int
	// Signal is the terminating signal description when the process was killed by a signal.
	Signal string
	// PeakRSSBytes is the child process peak RSS in bytes from rusage.Maxrss.
	PeakRSSBytes int64
	// TimedOut is true if the invocation hit its wall-clock timeout and was killed.
	TimedOut bool
	// StdoutExceeded is true if stdout exceeded the per-line or total byte cap.
	StdoutExceeded bool
}

// NormalizeExecutor runs a trajectory normalizer command inside the sandbox.
type NormalizeExecutor interface {
	Execute(ctx context.Context, req NormalizeExecRequest) (*NormalizeExecResult, error)
}

// SandboxExecutorConfig configures a SandboxExecutor.
type SandboxExecutorConfig struct {
	// HomeDir is passed as HOME in the clean environment (contract D5).
	HomeDir string
	// WorkloadUID and WorkloadGID are the non-root UID/GID to drop to via
	// SysProcAttr.Credential. Both must be > 0 unless AllowCurrentNonRoot is
	// true and the current process is already non-root.
	WorkloadUID int
	WorkloadGID int
	// AllowCurrentNonRoot permits running the child without setting
	// SysProcAttr.Credential when the parent process is already non-root
	// (geteuid() > 0, e.g. inside a non-root, read-only-rootfs converter job
	// container without CAP_SETUID/CAP_SETGID).
	//
	// Fails closed when geteuid() == 0: a root process can never skip the
	// credential drop, even if AllowCurrentNonRoot is true.
	AllowCurrentNonRoot bool
	// SelfExe overrides rootexec.SelfExe() ("/proc/self/exe") when set. Must be
	// an absolute path; intended for tests pointing at a built sciontool binary.
	SelfExe string
}

// BuildNormalizeEnv builds the from-scratch environment for a normalizer
// subprocess per contract D5. Nothing from os.Environ() and no SCION_*
// variables pass through.
func BuildNormalizeEnv(homeDir string) []string {
	return []string{
		"HOME=" + homeDir,
		"LANG=C.UTF-8",
		"PATH=" + NormalizeExecPath,
		"PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONSAFEPATH=1",
		"TZ=UTC",
	}
}

// cappedLineWriter splits stdout into lines while enforcing both a per-line
// byte cap and a total byte cap. When either cap is exceeded, onExceed is
// invoked once (typically to cancel the process context) and further bytes are
// drained without error so the child does not stall before SIGKILL lands.
type cappedLineWriter struct {
	mu       sync.Mutex
	maxLine  int
	maxTotal int
	total    int
	buf      []byte
	lines    [][]byte
	exceeded bool
	onExceed func()
}

func newCappedLineWriter(maxLine, maxTotal int, onExceed func()) *cappedLineWriter {
	return &cappedLineWriter{
		maxLine:  maxLine,
		maxTotal: maxTotal,
		onExceed: onExceed,
	}
}

func (w *cappedLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.exceeded {
		return len(p), nil
	}

	if w.total+len(p) > w.maxTotal {
		w.triggerExceedLocked()
		return len(p), nil
	}
	w.total += len(p)

	rem := p
	for len(rem) > 0 {
		idx := bytes.IndexByte(rem, '\n')
		if idx < 0 {
			if len(w.buf)+len(rem) > w.maxLine {
				w.triggerExceedLocked()
				return len(p), nil
			}
			w.buf = append(w.buf, rem...)
			break
		}
		chunk := rem[:idx]
		if len(w.buf)+len(chunk) > w.maxLine {
			w.triggerExceedLocked()
			return len(p), nil
		}
		line := make([]byte, len(w.buf)+len(chunk))
		copy(line, w.buf)
		copy(line[len(w.buf):], chunk)
		w.buf = w.buf[:0]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		w.lines = append(w.lines, line)
		rem = rem[idx+1:]
	}
	return len(p), nil
}

func (w *cappedLineWriter) triggerExceedLocked() {
	if w.exceeded {
		return
	}
	w.exceeded = true
	w.buf = nil
	if w.onExceed != nil {
		w.onExceed()
	}
}

func (w *cappedLineWriter) finalize() ([][]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.exceeded && len(w.buf) > 0 {
		line := append([]byte(nil), w.buf...)
		if line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		w.lines = append(w.lines, line)
		w.buf = nil
	}
	return w.lines, w.exceeded
}

// cappedBuffer captures up to maxBytes and discards any excess without error.
type cappedBuffer struct {
	mu        sync.Mutex
	maxBytes  int
	buf       bytes.Buffer
	truncated bool
}

func newCappedBuffer(maxBytes int) *cappedBuffer {
	return &cappedBuffer{maxBytes: maxBytes}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.buf.Len() < b.maxBytes {
		avail := b.maxBytes - b.buf.Len()
		if len(p) <= avail {
			_, _ = b.buf.Write(p)
		} else {
			_, _ = b.buf.Write(p[:avail])
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) result() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() == 0 {
		return nil, b.truncated
	}
	return append([]byte(nil), b.buf.Bytes()...), b.truncated
}
