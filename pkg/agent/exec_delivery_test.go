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
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/tmuxdeliver"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// Tests for the exec delivery path on tmuxdeliver (S1b). The existing
// delivery tests in this package pin step order, locking, extra submits and
// readiness; these cover what S1b adds.

// TestDeliveryOp pins the mapping from a deliverImmediate call to its op.
func TestDeliveryOp(t *testing.T) {
	cases := []struct {
		interrupt bool
		message   string
		want      tmuxdeliver.Op
	}{
		{true, "hello", tmuxdeliver.OpInterruptPaste},
		{true, "", tmuxdeliver.OpInterruptSubmit},
		{false, "", tmuxdeliver.OpSubmitOnly},
		{false, "hello", tmuxdeliver.OpPaste},
	}
	for _, tc := range cases {
		if got := deliveryOp(tc.interrupt, tc.message); got != tc.want {
			t.Errorf("deliveryOp(%v, %q) = %q, want %q", tc.interrupt, tc.message, got, tc.want)
		}
	}
}

// TestDeliverImmediate_SanitisesPastedText: the exec path loads the
// sanitised text on load-buffer's stdin.
func TestDeliverImmediate_SanitisesPastedText(t *testing.T) {
	var calls []string
	mgr := newDeliveryTestManager(api.DeliverySpec{}, &calls)
	var loaded string
	mgr.Runtime.(*runtime.MockRuntime).ExecWithStdinFunc = func(ctx context.Context, id string, cmd []string, stdin io.Reader) (string, error) {
		b, err := io.ReadAll(stdin)
		loaded = string(b)
		return "", err
	}
	msg := "a\x1b[201~b\x07c\x7fd\u009be\tf\ng"
	if err := mgr.deliverImmediate(context.Background(), "test-agent", "", msg, false); err != nil {
		t.Fatalf("deliverImmediate: %v", err)
	}
	if want := tmuxdeliver.Sanitize(msg); loaded != want {
		t.Fatalf("loaded %q, want the sanitised %q", loaded, want)
	}
	if loaded != "a[201~bcde\tf\ng" {
		t.Fatalf("loaded %q, want %q", loaded, "a[201~bcde\tf\ng")
	}
}

// TestDeliverImmediate_AllControlTextIsNotPasted: a text with only
// characters the sanitiser removes runs no command and returns
// ErrEmptyAfterSanitize, not a partial delivery.
func TestDeliverImmediate_AllControlTextIsNotPasted(t *testing.T) {
	var calls []string
	mgr := newDeliveryTestManager(api.DeliverySpec{}, &calls)
	err := mgr.deliverImmediate(context.Background(), "test-agent", "", "\x1b\x07\x00\x7f", false)
	if !errors.Is(err, tmuxdeliver.ErrEmptyAfterSanitize) {
		t.Fatalf("err = %v, want ErrEmptyAfterSanitize", err)
	}
	var partial *PartialDeliveryError
	if errors.As(err, &partial) {
		t.Fatalf("err = %v is a PartialDeliveryError", err)
	}
	if !strings.Contains(err.Error(), "failed to send message to agent 'test-agent'") {
		t.Fatalf("err = %q does not name the agent", err)
	}
	if len(calls) != 0 {
		t.Fatalf("commands run: %q, want none", calls)
	}
}

// TestDeliverImmediate_ErrorsNeverCarryMessageText (sec, binding): whichever
// step fails, the returned error, partial or not, never contains the
// message text. That covers an interrupt key, load-buffer, paste-buffer, the
// closing submit, an extra submit and a context cancelled before the extra
// submits. The fake runtime's errors name the failing argv, which never
// carries the text either. (The readiness probe never sees the text.)
func TestDeliverImmediate_ErrorsNeverCarryMessageText(t *testing.T) {
	const secret = "SECRET-PAYLOAD-7f3a"
	cases := []struct {
		name        string
		interrupt   bool
		failStdin   bool   // load-buffer fails
		failSubcmd  string // an Exec whose argv[1] is this fails
		failNth     int    // ... on this occurrence (1-based)
		cancelAfter string // cancel the context after the Exec whose argv[1] is this
		wantPartial bool
	}{
		{name: "interrupt key", interrupt: true, failSubcmd: "send-keys", failNth: 1},
		{name: "load-buffer", failStdin: true},
		{name: "paste-buffer", failSubcmd: "paste-buffer", failNth: 1},
		{name: "closing submit", failSubcmd: "send-keys", failNth: 1, wantPartial: true},
		{name: "extra submit", failSubcmd: "send-keys", failNth: 2, wantPartial: true},
		{name: "context cancel before extra submit", cancelAfter: "paste-buffer", wantPartial: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			mgr := newDeliveryTestManager(api.DeliverySpec{SubmitKeys: []string{"Enter"}, ExtraSubmits: 1, ExtraSubmitDelay: time.Second}, &calls)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt := mgr.Runtime.(*runtime.MockRuntime)
			rt.ExecWithStdinFunc = func(ctx context.Context, id string, cmd []string, stdin io.Reader) (string, error) {
				if tc.failStdin {
					return "", fmt.Errorf("exec %q failed", cmd)
				}
				return "", nil
			}
			seen := 0
			rt.ExecFunc = func(ctx context.Context, id string, cmd []string) (string, error) {
				if len(cmd) >= 2 && cmd[1] == tc.failSubcmd {
					seen++
					if seen == tc.failNth {
						return "", fmt.Errorf("exec %q failed", cmd)
					}
				}
				if len(cmd) >= 2 && cmd[1] == tc.cancelAfter {
					cancel()
				}
				return "", nil
			}
			err := mgr.deliverImmediate(ctx, "test-agent", "", "hello "+secret, tc.interrupt)
			if err == nil {
				t.Fatal("deliverImmediate = nil, want an error")
			}
			var partial *PartialDeliveryError
			if got := errors.As(err, &partial); got != tc.wantPartial {
				t.Fatalf("partial = %v, want %v (err %v)", got, tc.wantPartial, err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%+v", err), secret) {
				t.Fatalf("error carries the message text: %q", err)
			}
		})
	}
}

// TestDeliverImmediate_EmptyAfterSanitizeNamesOnlyTheAgent: the
// all-control error carries the agent name and the reason, nothing of the
// text.
func TestDeliverImmediate_EmptyAfterSanitizeNamesOnlyTheAgent(t *testing.T) {
	var calls []string
	mgr := newDeliveryTestManager(api.DeliverySpec{}, &calls)
	err := mgr.deliverImmediate(context.Background(), "test-agent", "", "\x1b\x07", false)
	want := "failed to send message to agent 'test-agent': " + tmuxdeliver.ErrEmptyAfterSanitize.Error()
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestMessageBuffer_NoRetryForEmptyAfterSanitize: ErrEmptyAfterSanitize is
// permanent, so flush reports it after one attempt, and the failure handler
// gets an error that names it.
func TestMessageBuffer_NoRetryForEmptyAfterSanitize(t *testing.T) {
	var attempts int32
	handled := make(chan error, 1)
	buf := NewMessageBuffer(10*time.Millisecond, func(agentID, projectID, message string, interrupt bool) error {
		atomic.AddInt32(&attempts, 1)
		return fmt.Errorf("failed to send message to agent '%s': %w", agentID, tmuxdeliver.ErrEmptyAfterSanitize)
	})
	tuneFlushRetry(buf, 3, 5*time.Millisecond)
	defer buf.Close()

	buf.SendWithFailureHandler("a", "p", "\x1b", func(err error) { handled <- err })

	select {
	case err := <-handled:
		if !errors.Is(err, tmuxdeliver.ErrEmptyAfterSanitize) {
			t.Fatalf("handler error = %v, want ErrEmptyAfterSanitize", err)
		}
		if !strings.Contains(err.Error(), "empty after sanitising") {
			t.Fatalf("handler error %q does not give the reason", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failure handler")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("delivery attempts = %d, want 1 (no retry)", got)
	}
}
