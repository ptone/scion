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

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"
)

const (
	helperModeEnv      = "SCION_INTEGRATION_HELPER_MODE"
	helperAddressEnv   = "SCION_INTEGRATION_HELPER_ADDRESS"
	helperReplicaIDEnv = "SCION_INTEGRATION_HELPER_REPLICA_ID"
)

type processSpec struct {
	Name      string
	Mode      string
	ReplicaID string
	Port      int
	Env       map[string]string
}

type testProcess struct {
	Name  string
	PID   int
	Port  int
	Ready bool
	cmd   *exec.Cmd
}

func (p *testProcess) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", p.Port)
}

type processTopology struct {
	ctx          context.Context
	cancel       context.CancelFunc
	allocator    *loopbackPortAllocator
	logs         *sanitizedWriter
	observations *observationRecorder
	processes    []*testProcess
	stopOnce     sync.Once
}

func newProcessTopology(t *testing.T, firstPort int, redactor *credentialRedactor) *processTopology {
	t.Helper()
	if redactor == nil {
		redactor = newCredentialRedactor()
	}
	ctx, cancel := context.WithCancel(context.Background())
	topology := &processTopology{
		ctx:          ctx,
		cancel:       cancel,
		allocator:    newLoopbackPortAllocator(firstPort, firstPort+100),
		logs:         newSanitizedWriter(redactor),
		observations: newObservationRecorder(redactor),
	}
	t.Cleanup(func() { topology.stop(t) })
	return topology
}

func (t *processTopology) start(tb testing.TB, spec processSpec) *testProcess {
	tb.Helper()
	port := spec.Port
	if port == 0 {
		var err error
		port, err = t.allocator.reserve()
		if err != nil {
			tb.Fatalf("reserve port for %s: %v", spec.Name, err)
		}
		// The helper process cannot inherit arbitrary production listeners, so keep the
		// deterministic reservation until immediately before starting the child.
		if err := t.allocator.release(port); err != nil {
			tb.Fatalf("release port for %s: %v", spec.Name, err)
		}
	}

	cmd := exec.CommandContext(t.ctx, os.Args[0], "-test.run=^TestHarnessHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(),
		helperModeEnv+"="+spec.Mode,
		helperAddressEnv+"=127.0.0.1:"+strconv.Itoa(port),
		helperReplicaIDEnv+"="+spec.ReplicaID,
	)
	for key, value := range spec.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdout = t.logs
	cmd.Stderr = t.logs
	if err := cmd.Start(); err != nil {
		tb.Fatalf("start %s: %v", spec.Name, err)
	}
	process := &testProcess{Name: spec.Name, PID: cmd.Process.Pid, Port: port, cmd: cmd}
	t.processes = append(t.processes, process)
	if err := waitForTCP(t.ctx, fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second); err != nil {
		tb.Fatalf("wait for %s readiness (PID %d): %v\nlogs:\n%s", spec.Name, process.PID, err, t.logs.String())
	}
	process.Ready = true
	t.observations.record(observation{ReplicaID: spec.ReplicaID, Outcome: "ready"})
	return process
}

func (t *processTopology) stopProcess(tb testing.TB, process *testProcess) {
	tb.Helper()
	if process.cmd.ProcessState != nil {
		return
	}
	if err := process.cmd.Process.Kill(); err != nil {
		tb.Fatalf("kill %s (PID %d): %v", process.Name, process.PID, err)
	}
	if err := process.cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			tb.Fatalf("wait for %s (PID %d): %v", process.Name, process.PID, err)
		}
	}
}

func (t *processTopology) stop(tb testing.TB) {
	tb.Helper()
	t.stopOnce.Do(func() {
		t.cancel()
		for _, process := range t.processes {
			if err := process.cmd.Wait(); err != nil && process.cmd.ProcessState == nil {
				tb.Errorf("wait for %s (PID %d): %v", process.Name, process.PID, err)
			}
			t.observations.record(observation{ReplicaID: process.Name, Outcome: "stopped"})
		}
		if err := t.allocator.close(); err != nil {
			tb.Errorf("close port allocator: %v", err)
		}
	})
}

func waitForTCP(ctx context.Context, address string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	dialer := net.Dialer{Timeout: 50 * time.Millisecond}
	for time.Now().Before(deadline) {
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			return connection.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for %s", address)
}

type loopbackPortAllocator struct {
	mu           sync.Mutex
	next, last   int
	reservations map[int]net.Listener
}

func newLoopbackPortAllocator(first, last int) *loopbackPortAllocator {
	return &loopbackPortAllocator{next: first, last: last, reservations: make(map[int]net.Listener)}
}

func (a *loopbackPortAllocator) reserve() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for port := a.next; port <= a.last; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		a.next = port + 1
		a.reservations[port] = listener
		return port, nil
	}
	return 0, fmt.Errorf("no loopback ports available in deterministic range")
}

func (a *loopbackPortAllocator) release(port int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	listener, ok := a.reservations[port]
	if !ok {
		return fmt.Errorf("port %d is not reserved", port)
	}
	delete(a.reservations, port)
	return listener.Close()
}

func (a *loopbackPortAllocator) close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var firstErr error
	for port, listener := range a.reservations {
		if err := listener.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close reserved port %d: %w", port, err)
		}
		delete(a.reservations, port)
	}
	return firstErr
}
