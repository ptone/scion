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

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/brokercontrol"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// inspectingRuntime is a mock runtime that reports scripted binds.
type inspectingRuntime struct {
	*runtime.MockRuntime
	results []inspectResult
	calls   int
	stopped []runtime.RunRef
}

type inspectResult struct {
	binds []runtime.BindMount
	err   error
}

func (r *inspectingRuntime) InspectBindMounts(context.Context, string) ([]runtime.BindMount, error) {
	res := r.results[min(r.calls, len(r.results)-1)]
	r.calls++
	return res.binds, res.err
}

func newInspectingRuntime(results ...inspectResult) *inspectingRuntime {
	r := &inspectingRuntime{results: results}
	r.MockRuntime = &runtime.MockRuntime{StopFunc: func(_ context.Context, ref runtime.RunRef) error {
		r.stopped = append(r.stopped, ref)
		return nil
	}}
	return r
}

func controlPathHome(t *testing.T) (global, outside string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	global = filepath.Join(home, ".scion")
	outside = filepath.Join(root, "data")
	for _, d := range []string{global, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return global, outside
}

func TestVerifyCreatedMounts(t *testing.T) {
	global, outside := controlPathHome(t)
	ref := runtime.RunRef{ID: "c1", RunID: "r1"}
	clean := inspectResult{binds: []runtime.BindMount{{Source: outside, Destination: "/x"}}}
	bad := inspectResult{binds: []runtime.BindMount{{Source: global, Destination: "/g"}}}
	failed := inspectResult{err: runtime.ErrMountsUnverifiable}

	t.Run("clean binds", func(t *testing.T) {
		rt := newInspectingRuntime(clean)
		if err := (&AgentManager{Runtime: rt}).verifyCreatedMounts(context.Background(), ref, "a"); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(rt.stopped) != 0 {
			t.Fatalf("stopped a clean agent")
		}
	})
	t.Run("refused bind stops at once", func(t *testing.T) {
		rt := newInspectingRuntime(bad)
		err := (&AgentManager{Runtime: rt}).verifyCreatedMounts(context.Background(), ref, "a")
		if !errors.Is(err, brokercontrol.ErrControlPathMount) {
			t.Fatalf("err = %v", err)
		}
		if rt.calls != 1 || len(rt.stopped) != 1 || rt.stopped[0] != ref {
			t.Fatalf("calls=%d stopped=%v", rt.calls, rt.stopped)
		}
	})
	t.Run("one inspect failure is retried", func(t *testing.T) {
		rt := newInspectingRuntime(failed, clean)
		if err := (&AgentManager{Runtime: rt}).verifyCreatedMounts(context.Background(), ref, "a"); err != nil {
			t.Fatalf("err = %v", err)
		}
		if rt.calls != 2 || len(rt.stopped) != 0 {
			t.Fatalf("calls=%d stopped=%v", rt.calls, rt.stopped)
		}
	})
	t.Run("two inspect failures stop", func(t *testing.T) {
		rt := newInspectingRuntime(failed, failed)
		err := (&AgentManager{Runtime: rt}).verifyCreatedMounts(context.Background(), ref, "a")
		if !errors.Is(err, brokercontrol.ErrControlPathMount) {
			t.Fatalf("err = %v", err)
		}
		if len(rt.stopped) != 1 {
			t.Fatalf("stopped=%v", rt.stopped)
		}
	})
	t.Run("runtime without inspection", func(t *testing.T) {
		if err := (&AgentManager{Runtime: &runtime.MockRuntime{}}).verifyCreatedMounts(context.Background(), ref, "a"); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}
