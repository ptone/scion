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

package cmd

import (
	"bytes"
	"context"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedBuffer is a bytes.Buffer safe for the log package's writes from
// the signal goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureStdLog sends the standard logger's output to a buffer for the
// rest of the test.
func captureStdLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	savedOut, savedFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(savedOut)
		log.SetFlags(savedFlags)
	})
	return buf
}

// fakeSignalNotify records what installServerShutdownSignals subscribes to
// and hands the test the channel, so signals are delivered without
// signalling the process. Like signal.Notify, delivery never blocks.
type fakeSignalNotify struct {
	calls int
	ch    chan<- os.Signal
	sigs  []os.Signal
}

func (f *fakeSignalNotify) notify(c chan<- os.Signal, sigs ...os.Signal) {
	f.calls++
	f.ch = c
	f.sigs = append([]os.Signal(nil), sigs...)
}

func (f *fakeSignalNotify) deliver(sig os.Signal) {
	select {
	case f.ch <- sig:
	default:
	}
}

// PS-1: SIGTERM and SIGINT each start the graceful shutdown by calling
// cancel exactly once; a second signal of either kind causes no second
// cancel and no panic; the subscribed set is exactly {Interrupt, SIGTERM}.
func TestInstallServerShutdownSignals_SingleCancel(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second os.Signal
		logged        string
	}{
		{"SIGTERM then SIGINT", syscall.SIGTERM, os.Interrupt, "Received signal terminated, shutting down..."},
		{"SIGINT then SIGTERM", os.Interrupt, syscall.SIGTERM, "Received signal interrupt, shutting down..."},
		{"SIGTERM twice", syscall.SIGTERM, syscall.SIGTERM, "Received signal terminated, shutting down..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureStdLog(t)
			var f fakeSignalNotify
			var cancels atomic.Int32
			cancelled := make(chan struct{}, 2)
			installServerShutdownSignals(f.notify, func() {
				cancels.Add(1)
				cancelled <- struct{}{}
			})

			require.Equal(t, 1, f.calls, "notify is called once")
			assert.ElementsMatch(t, []os.Signal{os.Interrupt, syscall.SIGTERM}, f.sigs)
			require.NotNil(t, f.ch)

			f.deliver(tc.first)
			<-cancelled
			assert.Contains(t, logs.String(), tc.logged)

			// The handler reads one signal and exits, so a second signal
			// stays unread in the (still subscribed) channel and can never
			// reach cancel.
			f.deliver(tc.second)
			assert.Equal(t, 1, len(f.ch), "the second signal is not consumed")
			f.deliver(tc.second) // channel full: dropped, as signal.Notify does
			assert.Equal(t, int32(1), cancels.Load(), "cancel is called exactly once")
		})
	}
}

// PS-2: a SIGTERM-initiated shutdown runs the step-16 tail in order:
// cancel, then the server wait returns, then the audit writer closes, then
// runServerStart's deferred log cleanups. The harness mirrors
// runServerStart's structure and calls the real installServerShutdownSignals
// and awaitServerExit; only notify, wait, close and the log cleanup are fakes.
func TestServerShutdown_SIGTERMOrderedPath(t *testing.T) {
	events := make(chan string, 8)
	var f fakeSignalNotify
	installed := make(chan struct{})

	run := func() error {
		defer func() { events <- "log-cleanup" }() // step 1's deferred log cleanups
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		installServerShutdownSignals(f.notify, func() {
			events <- "cancel"
			cancel()
		})
		close(installed)
		return awaitServerExit(ctx, make(chan error), cancel,
			func() { events <- "wait-return" },
			func(context.Context) error {
				events <- "close"
				return nil
			})
	}

	done := make(chan error, 1)
	go func() { done <- run() }()
	<-installed
	f.deliver(syscall.SIGTERM)
	require.NoError(t, <-done)

	close(events)
	var got []string
	for e := range events {
		got = append(got, e)
	}
	assert.Equal(t, []string{"cancel", "wait-return", "close", "log-cleanup"}, got)
}
