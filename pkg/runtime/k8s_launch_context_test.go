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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// postCreateRecorder records the post-create steps Run takes through the
// test hooks. Every hook fails with its ctx's error when that ctx is already
// done, as a real exec or stream would, so a step run on a cancelled
// context is never recorded as having happened.
type postCreateRecorder struct {
	mu    sync.Mutex
	syncs []string // destination paths
	execs []string // joined commands
}

func (p *postCreateRecorder) gateTouched() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.execs {
		if c == "touch /tmp/.scion-home-ready" {
			return true
		}
	}
	return false
}

func (p *postCreateRecorder) homeSynced(dest string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range p.syncs {
		if d == dest {
			return true
		}
	}
	return false
}

func (p *postCreateRecorder) anySync() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.syncs) > 0
}

// newPostCreateTestRuntime returns a KubernetesRuntime over a fake clientset
// whose post-create exec and sync steps go to rec. onGet is called on every
// Get of the agent pod with the 1-based call count; it returns the pod to
// report, or nil to report the pod as created (pending).
func newPostCreateTestRuntime(t *testing.T, rec *postCreateRecorder, onGet func(n int) *corev1.Pod) *KubernetesRuntime {
	t.Helper()
	clientset := k8sfake.NewClientset()
	var mu sync.Mutex
	gets := 0
	clientset.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		mu.Lock()
		gets++
		n := gets
		mu.Unlock()
		if pod := onGet(n); pod != nil {
			return true, pod, nil
		}
		return false, nil, nil
	})
	client := k8s.NewTestClient(fake.NewSimpleDynamicClient(k8sruntime.NewScheme()), clientset)
	r := NewKubernetesRuntime(client)
	r.podReadyPollInterval = 10 * time.Millisecond
	r.execProbe = func(ctx context.Context, namespace, podName string) error {
		return ctx.Err()
	}
	r.execHook = func(ctx context.Context, namespace, podName string, cmd []string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		rec.mu.Lock()
		rec.execs = append(rec.execs, strings.Join(cmd, " "))
		rec.mu.Unlock()
		return "", nil
	}
	r.syncToPodHook = func(ctx context.Context, namespace, podName, sourcePath, destPath string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		rec.mu.Lock()
		rec.syncs = append(rec.syncs, destPath)
		rec.mu.Unlock()
		return nil
	}
	return r
}

func runningAgentPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  agentContainerName,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}

// postCreateTestConfig returns a RunConfig with a real agent home, so Run
// performs the home sync step as well as the startup-gate touch.
func postCreateTestConfig(t *testing.T) RunConfig {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	homeDir := filepath.Join(tmpHome, ".scion", "agents", "a", "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatal(err)
	}
	return RunConfig{
		Name:         "test-agent",
		Image:        "test-image",
		UnixUsername: "scion",
		HomeDir:      homeDir,
	}
}

const postCreateTestGuard = 10 * time.Second

// A create request that ends (its ctx is cancelled) while the pod is still
// coming up no longer abandons the pod at its startup gate: with a launch
// context attached, Run keeps waiting, and once the pod is ready it still
// syncs the agent home and touches the gate marker. This is the GKE
// Autopilot cold-start case, where the hub's synchronous create request
// ends long before the pod is ready.
func TestRun_LaunchContextOutlivesCancelledRequest(t *testing.T) {
	rec := &postCreateRecorder{}
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()

	r := newPostCreateTestRuntime(t, rec, func(n int) *corev1.Pod {
		if n == 1 {
			cancelReq() // the request ends mid-wait
		}
		if n >= 4 {
			return runningAgentPod("test-agent")
		}
		return nil
	})

	launchCtx, cancelLaunch := context.WithTimeout(context.WithoutCancel(reqCtx), postCreateTestGuard)
	defer cancelLaunch()

	id, err := r.Run(WithLaunchContext(reqCtx, launchCtx), postCreateTestConfig(t))
	if err != nil {
		t.Fatalf("Run() error = %v, want the post-create phase to finish after the request ended", err)
	}
	if id != "test-agent" {
		t.Errorf("Run() id = %q, want %q", id, "test-agent")
	}
	if reqCtx.Err() == nil {
		t.Fatal("test setup: the request context was never cancelled")
	}
	if !rec.homeSynced("/home/scion") {
		t.Errorf("home was not synced after the request ended (syncs %v)", rec.syncs)
	}
	if !rec.gateTouched() {
		t.Errorf("startup gate was not touched after the request ended (execs %v)", rec.execs)
	}
}

// Without a launch context, Run behaves as before: the caller's ctx bounds
// the whole call, so cancelling it during the readiness wait ends Run with
// no sync and no gate touch (a CLI interrupt still stops a local create).
func TestRun_NoLaunchContextFollowsCallerContext(t *testing.T) {
	rec := &postCreateRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := newPostCreateTestRuntime(t, rec, func(n int) *corev1.Pod {
		if n == 1 {
			cancel()
		}
		if n >= 4 {
			return runningAgentPod("test-agent")
		}
		return nil
	})

	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, postCreateTestConfig(t))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run() succeeded, want it to end with the caller's cancelled ctx")
		}
	case <-time.After(postCreateTestGuard):
		t.Fatal("Run() did not end after the caller's ctx was cancelled")
	}
	if rec.anySync() || rec.gateTouched() {
		t.Errorf("post-create steps ran on a cancelled ctx (syncs %v, execs %v)", rec.syncs, rec.execs)
	}
}

// A stop or delete of the agent cancels the launch context. When that
// happens during the readiness wait, Run ends promptly with an error and
// neither syncs nor touches the startup gate, even though the pod later
// reports ready.
func TestRun_LaunchContextCancelEndsPostCreatePhase(t *testing.T) {
	rec := &postCreateRecorder{}
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	launchCtx, cancelLaunch := context.WithCancel(context.WithoutCancel(reqCtx))
	defer cancelLaunch()

	r := newPostCreateTestRuntime(t, rec, func(n int) *corev1.Pod {
		switch {
		case n == 1:
			cancelReq() // the request ends first
		case n == 3:
			cancelLaunch() // then the agent is deleted
		case n >= 4:
			return runningAgentPod("test-agent")
		}
		return nil
	})

	done := make(chan error, 1)
	go func() {
		_, err := r.Run(WithLaunchContext(reqCtx, launchCtx), postCreateTestConfig(t))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run() succeeded, want it to end when the launch context was cancelled")
		}
	case <-time.After(postCreateTestGuard):
		t.Fatal("Run() did not end after the launch context was cancelled")
	}
	if rec.anySync() || rec.gateTouched() {
		t.Errorf("post-create steps ran after the launch was cancelled (syncs %v, execs %v)", rec.syncs, rec.execs)
	}
}

// A launch context whose deadline passes before the pod is ready (the
// readiness budget running out) still fails Run with the readiness error and
// the pod name, so the caller's existing failure handling runs, and nothing
// is synced or signalled.
func TestRun_LaunchContextReadyTimeoutFails(t *testing.T) {
	rec := &postCreateRecorder{}
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	launchCtx, cancelLaunch := context.WithTimeout(context.WithoutCancel(reqCtx), 200*time.Millisecond)
	defer cancelLaunch()

	r := newPostCreateTestRuntime(t, rec, func(n int) *corev1.Pod {
		if n == 1 {
			cancelReq()
		}
		return nil // never ready
	})

	id, err := r.Run(WithLaunchContext(reqCtx, launchCtx), postCreateTestConfig(t))
	if err == nil {
		t.Fatal("Run() succeeded, want a readiness timeout")
	}
	if !strings.Contains(err.Error(), "timed out waiting for pod to be ready") {
		t.Errorf("Run() error = %v, want the readiness timeout", err)
	}
	if id != "test-agent" {
		t.Errorf("Run() id = %q, want the created pod's name for cleanup", id)
	}
	if rec.anySync() || rec.gateTouched() {
		t.Errorf("post-create steps ran after a readiness timeout (syncs %v, execs %v)", rec.syncs, rec.execs)
	}
}

func TestWithLaunchContext_NilIsNoOp(t *testing.T) {
	ctx := context.Background()
	if got := WithLaunchContext(ctx, nil); got != ctx {
		t.Error("WithLaunchContext(ctx, nil) should return ctx unchanged")
	}
	if LaunchContextFrom(ctx) != nil {
		t.Error("LaunchContextFrom on a plain ctx should be nil")
	}
}
