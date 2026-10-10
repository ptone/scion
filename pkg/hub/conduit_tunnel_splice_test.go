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

package hub

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/conduit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spliceFakeStream is a conduit.Stream for splice tests. Read blocks until
// the stream is closed locally (ErrStreamClosed), half-closed by the peer
// (io.EOF) or closed by the peer (io.EOF for code 0, else *CloseError).
// It records the first local close and any CloseWrite.
type spliceFakeStream struct {
	mu          sync.Mutex
	cond        *sync.Cond
	localClosed bool
	localCode   *conduit.CloseError
	peerFin     bool
	peerClose   *conduit.CloseError
	closeWrite  bool
	// closeDelay delays CloseWithCode, to order the two legs' closes.
	closeDelay time.Duration
}

func newSpliceFakeStream() *spliceFakeStream {
	s := &spliceFakeStream{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *spliceFakeStream) Read([]byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for !s.localClosed && !s.peerFin && s.peerClose == nil {
		s.cond.Wait()
	}
	switch {
	case s.localClosed:
		return 0, conduit.ErrStreamClosed
	case s.peerClose != nil && s.peerClose.Code != conduit.CloseNormal:
		return 0, s.peerClose
	default:
		return 0, io.EOF
	}
}

func (s *spliceFakeStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.localClosed {
		return 0, conduit.ErrStreamClosed
	}
	return len(p), nil
}

func (s *spliceFakeStream) CloseWithCode(code uint32, reason string) error {
	if s.closeDelay > 0 {
		time.Sleep(s.closeDelay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.localClosed {
		return nil
	}
	s.localClosed = true
	s.localCode = &conduit.CloseError{Code: code, Reason: reason}
	s.cond.Broadcast()
	return nil
}

func (s *spliceFakeStream) Close() error                   { return s.CloseWithCode(conduit.CloseNormal, "") }
func (s *spliceFakeStream) Resize(cols, rows uint16) error { return nil }
func (s *spliceFakeStream) ID() uint32                     { return 2 }

func (s *spliceFakeStream) CloseWrite() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeWrite = true
	return nil
}

// State reports closed once the peer closed the stream (not on a fin).
func (s *spliceFakeStream) State() conduit.StreamState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peerClose != nil || s.localClosed {
		return conduit.StateClosed
	}
	return conduit.StateActive
}

func (s *spliceFakeStream) peer(fin bool, closeErr *conduit.CloseError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peerFin = s.peerFin || fin
	if closeErr != nil {
		s.peerClose = closeErr
	}
	s.cond.Broadcast()
}

func (s *spliceFakeStream) firstClose() *conduit.CloseError {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.localCode
}

// runSplice runs t's two pumps and returns a channel closed when both end.
func runSplice(t *conduitTunnel) <-chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); t.pump(t.target, t.user) }()
	go func() { defer wg.Done(); t.pump(t.user, t.target) }()
	go func() { wg.Wait(); close(done) }()
	return done
}

func waitSpliceDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the splice did not end")
	}
}

// TestConduitTunnel_CloseBothPassesCodeToBothLegs: the authorization close
// reaches both legs with its code, whichever leg closes first, so the user
// leg never ends with a normal close instead.
func TestConduitTunnel_CloseBothPassesCodeToBothLegs(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		userDelay, targetDelay time.Duration
	}{
		{"target leg closes first", 50 * time.Millisecond, 0},
		{"user leg closes first", 0, 50 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, target := newSpliceFakeStream(), newSpliceFakeStream()
			user.closeDelay, target.closeDelay = tc.userDelay, tc.targetDelay
			tun := &conduitTunnel{user: user, target: target}
			done := runSplice(tun)

			tun.closeBoth(conduit.CloseUnauthenticated, conduitReasonAuthzExpired)
			waitSpliceDone(t, done)
			require.Eventually(t, func() bool { return user.firstClose() != nil && target.firstClose() != nil }, 10*time.Second, 5*time.Millisecond)

			want := &conduit.CloseError{Code: conduit.CloseUnauthenticated, Reason: conduitReasonAuthzExpired}
			assert.Equal(t, want, user.firstClose(), "user leg")
			assert.Equal(t, want, target.firstClose(), "target leg")
		})
	}
}

// TestConduitTunnel_SpliceWithoutRecordedClose: with no recorded close the
// splice passes a leg's own end on, as before: a peer close code goes to
// the other leg, a normal close is a normal close, and a fin half-closes.
func TestConduitTunnel_SpliceWithoutRecordedClose(t *testing.T) {
	t.Run("peer close code", func(t *testing.T) {
		user, target := newSpliceFakeStream(), newSpliceFakeStream()
		tun := &conduitTunnel{user: user, target: target}
		done := runSplice(tun)
		target.peer(false, &conduit.CloseError{Code: conduit.CloseRelayRestart, Reason: "relay_restart"})
		require.Eventually(t, func() bool { return user.firstClose() != nil }, 10*time.Second, 5*time.Millisecond)
		assert.Equal(t, uint32(conduit.CloseRelayRestart), user.firstClose().Code)
		user.peer(true, nil)
		waitSpliceDone(t, done)
	})
	t.Run("normal close", func(t *testing.T) {
		user, target := newSpliceFakeStream(), newSpliceFakeStream()
		tun := &conduitTunnel{user: user, target: target}
		done := runSplice(tun)
		target.peer(false, &conduit.CloseError{Code: conduit.CloseNormal})
		require.Eventually(t, func() bool { return user.firstClose() != nil }, 10*time.Second, 5*time.Millisecond)
		assert.Equal(t, uint32(conduit.CloseNormal), user.firstClose().Code)
		user.peer(true, nil)
		waitSpliceDone(t, done)
	})
	t.Run("half-close", func(t *testing.T) {
		user, target := newSpliceFakeStream(), newSpliceFakeStream()
		tun := &conduitTunnel{user: user, target: target}
		done := runSplice(tun)
		target.peer(true, nil)
		require.Eventually(t, func() bool {
			user.mu.Lock()
			defer user.mu.Unlock()
			return user.closeWrite
		}, 10*time.Second, 5*time.Millisecond)
		assert.Nil(t, user.firstClose(), "a fin only half-closes the other leg")
		user.peer(true, nil)
		waitSpliceDone(t, done)
	})
}
