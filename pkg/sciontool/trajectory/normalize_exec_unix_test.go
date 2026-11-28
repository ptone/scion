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
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	testSciontoolOnce sync.Once
	testSciontoolPath string
	testSciontoolErr  error
)

func buildTestSciontool(t *testing.T) string {
	t.Helper()
	testSciontoolOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sciontool-norm-test-*")
		if err != nil {
			testSciontoolErr = err
			return
		}
		bin := filepath.Join(dir, "sciontool")
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "../../../cmd/sciontool")
		out, err := cmd.CombinedOutput()
		if err != nil {
			testSciontoolErr = &buildErr{err: err, out: string(out)}
			return
		}
		testSciontoolPath = bin
	})
	if testSciontoolErr != nil {
		t.Fatalf("build sciontool for sandbox tests: %v", testSciontoolErr)
	}
	return testSciontoolPath
}

type buildErr struct {
	err error
	out string
}

func (e *buildErr) Error() string { return e.err.Error() + ": " + e.out }

func findPython3(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found on PATH")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("abs python3 path: %v", err)
	}
	return abs
}

func newTestExecutor(t *testing.T) *SandboxExecutor {
	t.Helper()
	bin := buildTestSciontool(t)
	cfg := SandboxExecutorConfig{
		HomeDir: t.TempDir(),
		SelfExe: bin,
	}
	if os.Geteuid() == 0 {
		cfg.WorkloadUID = 65534
		cfg.WorkloadGID = 65534
	} else {
		// Non-root test processes exercise the converter job container mode
		// (AllowCurrentNonRoot: true, no CAP_SETUID/CAP_SETGID).
		// Root credential-drop construction is verified directly in
		// TestSandboxExecutor_BuildCommand_SysProcAttr.
		cfg.AllowCurrentNonRoot = true
	}
	e, err := NewSandboxExecutor(cfg)
	if err != nil {
		t.Fatalf("NewSandboxExecutor: %v", err)
	}
	return e
}

func TestSandboxExecutor_BuildCommand_SysProcAttr(t *testing.T) {
	asRoot := func() int { return 0 }
	asNonRoot := func() int { return 1000 }

	// Fail closed on invalid UID/GID (including AllowCurrentNonRoot when root),
	// empty HomeDir, or relative SelfExe.
	for _, tc := range []struct {
		name string
		cfg  SandboxExecutorConfig
		euid func() int
	}{
		{"zero uid", SandboxExecutorConfig{HomeDir: "/home/scion", WorkloadUID: 0, WorkloadGID: 1000}, asRoot},
		{"negative uid", SandboxExecutorConfig{HomeDir: "/home/scion", WorkloadUID: -1, WorkloadGID: 1000}, asRoot},
		{"zero gid", SandboxExecutorConfig{HomeDir: "/home/scion", WorkloadUID: 1000, WorkloadGID: 0}, asRoot},
		{"negative gid", SandboxExecutorConfig{HomeDir: "/home/scion", WorkloadUID: 1000, WorkloadGID: -1}, asRoot},
		{"allow non-root but running as root with zero uid", SandboxExecutorConfig{HomeDir: "/home/scion", AllowCurrentNonRoot: true}, asRoot},
		{"non-root without AllowCurrentNonRoot and zero uid", SandboxExecutorConfig{HomeDir: "/home/scion"}, asNonRoot},
		{"empty homedir", SandboxExecutorConfig{HomeDir: "", WorkloadUID: 1000, WorkloadGID: 1000}, asRoot},
		{"relative selfExe", SandboxExecutorConfig{HomeDir: "/home/scion", WorkloadUID: 1000, WorkloadGID: 1000, SelfExe: "sciontool"}, asRoot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newSandboxExecutorWithEUID(tc.cfg, tc.euid); err == nil {
				t.Errorf("newSandboxExecutorWithEUID(%+v) succeeded, want fail-closed error", tc.cfg)
			}
		})
	}

	// Root parent with AllowCurrentNonRoot=true and valid WorkloadUID/GID MUST still drop credentials.
	t.Run("root with AllowCurrentNonRoot and valid uid still drops credentials", func(t *testing.T) {
		e, err := newSandboxExecutorWithEUID(SandboxExecutorConfig{
			HomeDir:             "/home/scion",
			WorkloadUID:         1001,
			WorkloadGID:         1002,
			AllowCurrentNonRoot: true,
		}, asRoot)
		if err != nil {
			t.Fatalf("newSandboxExecutorWithEUID: %v", err)
		}
		cmd, err := e.buildCommand(context.Background(), NormalizeExecRequest{
			Command: []string{"/proc/self/exe", "internal", "trajectory-convert", "--harness", "claude"},
		})
		if err != nil {
			t.Fatalf("buildCommand: %v", err)
		}
		if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
			t.Fatal("expected SysProcAttr.Credential to be set when running as root")
		}
		if cmd.SysProcAttr.Credential.Uid != 1001 || cmd.SysProcAttr.Credential.Gid != 1002 {
			t.Errorf("Credential = (%d, %d), want (1001, 1002)", cmd.SysProcAttr.Credential.Uid, cmd.SysProcAttr.Credential.Gid)
		}
	})

	// Non-root parent (e.g. converter job container) with AllowCurrentNonRoot=true omits Credential.
	t.Run("non-root with AllowCurrentNonRoot omits Credential", func(t *testing.T) {
		e, err := newSandboxExecutorWithEUID(SandboxExecutorConfig{
			HomeDir:             "/tmp",
			AllowCurrentNonRoot: true,
		}, asNonRoot)
		if err != nil {
			t.Fatalf("newSandboxExecutorWithEUID: %v", err)
		}
		cmd, err := e.buildCommand(context.Background(), NormalizeExecRequest{
			Command: []string{"/proc/self/exe", "internal", "trajectory-convert", "--harness", "claude"},
		})
		if err != nil {
			t.Fatalf("buildCommand: %v", err)
		}
		if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
			t.Fatal("expected SysProcAttr.Setpgid = true")
		}
		if cmd.SysProcAttr.Credential != nil {
			t.Errorf("expected Credential == nil in non-root job container mode, got %+v", cmd.SysProcAttr.Credential)
		}
	})

	execer, err := newSandboxExecutorWithEUID(SandboxExecutorConfig{
		HomeDir:     "/home/scion",
		WorkloadUID: 1001,
		WorkloadGID: 1002,
	}, asRoot)
	if err != nil {
		t.Fatalf("NewSandboxExecutor: %v", err)
	}

	// Refuse relative or empty command.
	if _, err := execer.buildCommand(context.Background(), NormalizeExecRequest{Command: nil}); err == nil {
		t.Error("expected error for empty command")
	}
	if _, err := execer.buildCommand(context.Background(), NormalizeExecRequest{Command: []string{"python3", "normalize.py"}}); err == nil {
		t.Error("expected error for relative command")
	}

	cmd, err := execer.buildCommand(context.Background(), NormalizeExecRequest{
		Command: []string{"/opt/scion/venv/bin/python3", "-I", "/home/scion/.scion/harness/normalize.py"},
	})
	if err != nil {
		t.Fatalf("buildCommand: %v", err)
	}

	if cmd.Path != "/proc/self/exe" {
		t.Errorf("cmd.Path = %q, want /proc/self/exe", cmd.Path)
	}
	wantArgs := []string{
		"/proc/self/exe",
		"internal", "normalize-exec",
		"--as-mib", strconv.Itoa(DefaultNormalizeMaxAddressSpaceMiB),
		"--cpu-seconds", strconv.Itoa(DefaultNormalizeCPUSeconds),
		"--",
		"/opt/scion/venv/bin/python3", "-I", "/home/scion/.scion/harness/normalize.py",
	}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Errorf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}
	if cmd.Dir != "/" {
		t.Errorf("cmd.Dir = %q, want /", cmd.Dir)
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("cmd.SysProcAttr = nil")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Error("cmd.SysProcAttr.Setpgid = false, want true")
	}
	if cmd.SysProcAttr.Credential == nil {
		t.Fatal("cmd.SysProcAttr.Credential = nil")
	}
	if cmd.SysProcAttr.Credential.Uid != 1001 || cmd.SysProcAttr.Credential.Gid != 1002 {
		t.Errorf("Credential = (%d, %d), want (1001, 1002)", cmd.SysProcAttr.Credential.Uid, cmd.SysProcAttr.Credential.Gid)
	}
	if cmd.SysProcAttr.Credential.Groups == nil {
		t.Error("Credential.Groups = nil, want non-nil slice (suppgroups pattern)")
	}
	wantEnv := BuildNormalizeEnv("/home/scion")
	if !reflect.DeepEqual(cmd.Env, wantEnv) {
		t.Errorf("cmd.Env = %v, want %v", cmd.Env, wantEnv)
	}
}

func TestSandboxExecutor_ExecHappyPathAndCleanEnv(t *testing.T) {
	t.Setenv("SCION_AGENT_TOKEN", "should-not-leak")
	t.Setenv("SCION_AUTH_TOKEN", "should-not-leak")
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-leak")

	py := findPython3(t)
	e := newTestExecutor(t)

	script := `import json, os, sys
req = json.loads(sys.stdin.read())
print(json.dumps({"type": "echo", "req": req}))
print(json.dumps({"type": "env", "env": dict(os.environ)}))
sys.stderr.write("diag-line\n")
`
	res, err := e.Execute(context.Background(), NormalizeExecRequest{
		Command: []string{py, "-I", "-c", script},
		Input:   []byte(`{"interface_version":1,"harness":"claude"}`),
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, stderr = %q", res.ExitCode, string(res.Stderr))
	}
	if res.TimedOut || res.StdoutExceeded || res.StderrTruncated {
		t.Errorf("unexpected flags: timedOut=%v stdoutExceeded=%v stderrTrunc=%v", res.TimedOut, res.StdoutExceeded, res.StderrTruncated)
	}
	if res.PeakRSSBytes <= 0 {
		t.Errorf("PeakRSSBytes = %d, want > 0", res.PeakRSSBytes)
	}
	if string(res.Stderr) != "diag-line\n" {
		t.Errorf("Stderr = %q, want %q", string(res.Stderr), "diag-line\n")
	}
	if len(res.StdoutLines) != 2 {
		t.Fatalf("len(StdoutLines) = %d, want 2", len(res.StdoutLines))
	}

	var envMsg struct {
		Type string            `json:"type"`
		Env  map[string]string `json:"env"`
	}
	if err := json.Unmarshal(res.StdoutLines[1], &envMsg); err != nil {
		t.Fatalf("unmarshal env line: %v", err)
	}
	for k := range envMsg.Env {
		if strings.HasPrefix(k, "SCION_") || k == "ANTHROPIC_API_KEY" {
			t.Errorf("secret/SCION env var leaked into normalizer: %s", k)
		}
	}
	wantEnv := map[string]string{
		"HOME":                    e.cfg.HomeDir,
		"LANG":                    "C.UTF-8",
		"PATH":                    NormalizeExecPath,
		"PYTHONNOUSERSITE":        "1",
		"PYTHONDONTWRITEBYTECODE": "1",
		"PYTHONSAFEPATH":          "1",
		"TZ":                      "UTC",
	}
	for k, want := range wantEnv {
		if got := envMsg.Env[k]; got != want {
			t.Errorf("env[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestSandboxExecutor_ExecNonPythonBinary(t *testing.T) {
	e := newTestExecutor(t)
	bin := buildTestSciontool(t)
	res, err := e.Execute(context.Background(), NormalizeExecRequest{
		Command: []string{bin, "version"},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, stderr = %q", res.ExitCode, string(res.Stderr))
	}
	if len(res.StdoutLines) == 0 {
		t.Errorf("expected version output on stdout, got empty")
	}
}

func TestSandboxExecutor_RLimitCPU(t *testing.T) {
	py := findPython3(t)
	e := newTestExecutor(t)

	res, err := e.Execute(context.Background(), NormalizeExecRequest{
		Command: []string{py, "-I", "-c", "while True:\n    pass\n"},
		Limits:  NormalizeLimits{CPUSeconds: 1, MaxAddressSpaceMiB: 512},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.TimedOut {
		t.Fatalf("expected SIGXCPU before wall-clock timeout, got TimedOut=true")
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected non-zero exit from CPU loop, got 0")
	}
	if res.Signal != syscall.SIGXCPU.String() {
		t.Errorf("Signal = %q, want %q (exit=%d, stderr=%q)", res.Signal, syscall.SIGXCPU.String(), res.ExitCode, string(res.Stderr))
	}
}

func TestSandboxExecutor_RLimitAS(t *testing.T) {
	py := findPython3(t)
	e := newTestExecutor(t)

	// Allocate 512 MiB under a 256 MiB RLIMIT_AS cap; Python must raise MemoryError.
	script := `import sys
try:
    _ = bytearray(512 * 1024 * 1024)
    print("ALLOC_SUCCEEDED")
    sys.exit(0)
except MemoryError:
    print("MEMORY_ERROR")
    sys.exit(42)
`
	res, err := e.Execute(context.Background(), NormalizeExecRequest{
		Command: []string{py, "-I", "-c", script},
		Limits:  NormalizeLimits{CPUSeconds: 10, MaxAddressSpaceMiB: 256},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("allocation unexpectedly succeeded under RLIMIT_AS=256 MiB (stdout=%q)", res.StdoutLines)
	}
	if res.ExitCode == 42 && (len(res.StdoutLines) == 0 || string(res.StdoutLines[0]) != "MEMORY_ERROR") {
		t.Errorf("unexpected stdout on MemoryError: %q", res.StdoutLines)
	}
}

func TestSandboxExecutor_RLimitFSIZE(t *testing.T) {
	py := findPython3(t)
	e := newTestExecutor(t)

	target := filepath.Join(t.TempDir(), "out.txt")
	script := `import sys
with open(sys.argv[1], "wb") as f:
    f.write(b"hello")
    f.flush()
`
	res, err := e.Execute(context.Background(), NormalizeExecRequest{
		Command: []string{py, "-I", "-c", script, target},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("file write unexpectedly succeeded under RLIMIT_FSIZE=0")
	}
	if info, statErr := os.Stat(target); statErr == nil && info.Size() != 0 {
		t.Errorf("target file size = %d, want 0 under RLIMIT_FSIZE=0", info.Size())
	}
}

func TestSandboxExecutor_TimeoutKillsProcessGroup(t *testing.T) {
	py := findPython3(t)
	e := newTestExecutor(t)

	// Spawn a child sleep process and print its PID so we can verify the
	// entire process group is killed on timeout.
	script := `import subprocess, sys, time
p = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"])
print(p.pid, flush=True)
time.sleep(30)
`
	start := time.Now()
	res, err := e.Execute(context.Background(), NormalizeExecRequest{
		Command: []string{py, "-I", "-c", script},
		Timeout: 400 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.TimedOut {
		t.Fatalf("TimedOut = false, want true (exit=%d, stderr=%q)", res.ExitCode, string(res.Stderr))
	}
	if elapsed > 5*time.Second {
		t.Errorf("timeout took %v, expected prompt process-group kill", elapsed)
	}
	if len(res.StdoutLines) > 0 {
		if childPID, convErr := strconv.Atoi(strings.TrimSpace(string(res.StdoutLines[0]))); convErr == nil && childPID > 0 {
			// Give kernel a brief moment to clean up the killed process group member.
			deadline := time.Now().Add(1 * time.Second)
			for time.Now().Before(deadline) {
				if err := syscall.Kill(childPID, 0); err != nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := syscall.Kill(childPID, 0); err == nil {
				_ = syscall.Kill(childPID, syscall.SIGKILL)
				t.Errorf("grandchild pid %d survived process-group kill", childPID)
			}
		}
	}
}

func TestSandboxExecutor_OutputCaps(t *testing.T) {
	py := findPython3(t)

	t.Run("stdout line cap 1 MiB", func(t *testing.T) {
		e := newTestExecutor(t)
		script := `import sys; sys.stdout.write("a" * (1024 * 1024 + 64) + "\n"); sys.stdout.flush()`
		res, err := e.Execute(context.Background(), NormalizeExecRequest{
			Command: []string{py, "-I", "-c", script},
			Timeout: 10 * time.Second,
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !res.StdoutExceeded {
			t.Errorf("StdoutExceeded = false, want true")
		}
	})

	t.Run("stdout total cap", func(t *testing.T) {
		e := newTestExecutor(t)
		e.maxStdoutTotalBytes = 32 * 1024
		script := `import sys
line = "x" * 1000 + "\n"
for _ in range(100):
    sys.stdout.write(line)
sys.stdout.flush()
`
		res, err := e.Execute(context.Background(), NormalizeExecRequest{
			Command: []string{py, "-I", "-c", script},
			Timeout: 10 * time.Second,
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !res.StdoutExceeded {
			t.Errorf("StdoutExceeded = false, want true")
		}
	})

	t.Run("stderr truncated to 64 KiB", func(t *testing.T) {
		e := newTestExecutor(t)
		script := `import sys; sys.stderr.write("e" * (100 * 1024)); sys.stderr.flush()`
		res, err := e.Execute(context.Background(), NormalizeExecRequest{
			Command: []string{py, "-I", "-c", script},
			Timeout: 10 * time.Second,
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
		}
		if !res.StderrTruncated {
			t.Error("StderrTruncated = false, want true")
		}
		if len(res.Stderr) != MaxNormalizeStderrBytes {
			t.Errorf("len(Stderr) = %d, want %d", len(res.Stderr), MaxNormalizeStderrBytes)
		}
	})
}

func TestSandboxExecutor_UIDDrop(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("skipping live UID drop test: requires root (CAP_SETUID/CAP_SETGID); SysProcAttr construction is verified by TestSandboxExecutor_BuildCommand_SysProcAttr")
	}
	py := findPython3(t)
	bin := buildTestSciontool(t)
	e, err := NewSandboxExecutor(SandboxExecutorConfig{
		HomeDir:     t.TempDir(),
		WorkloadUID: 65534,
		WorkloadGID: 65534,
		SelfExe:     bin,
	})
	if err != nil {
		t.Fatalf("NewSandboxExecutor: %v", err)
	}
	res, err := e.Execute(context.Background(), NormalizeExecRequest{
		Command: []string{py, "-I", "-c", "import os; print(os.getuid(), os.getgid())"},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, stderr = %q", res.ExitCode, string(res.Stderr))
	}
	if len(res.StdoutLines) != 1 || string(res.StdoutLines[0]) != "65534 65534" {
		t.Errorf("uid/gid output = %q, want %q", res.StdoutLines, "65534 65534")
	}
}
