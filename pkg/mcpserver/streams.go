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

package mcpserver

import (
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

const (
	// CodeResourceExhausted is the JSON-RPC error code returned when a
	// principal exceeds max_streams_per_principal concurrent wait_for_messages
	// or events/stream calls (design §4.5, §9 B9).
	CodeResourceExhausted = -32025

	// DefaultMaxStreamsPerPrincipal is the default per-principal concurrent
	// stream/wait cap per replica (design §4.1, §4.5).
	DefaultMaxStreamsPerPrincipal = 8

	// DefaultStreamWait is the default Hub long-poll wait duration per
	// events/stream iteration and wait_for_messages call (design §4.1, §4.4).
	DefaultStreamWait = 30 * time.Second
)

// StreamLimiter bounds the number of concurrent long-lived streams
// (wait_for_messages and events/stream) per principal within a replica.
// It is safe for concurrent use and designed to be shared across tools and
// MCP Events handlers.
type StreamLimiter struct {
	mu     sync.Mutex
	max    int
	counts map[string]int
}

// NewStreamLimiter creates a StreamLimiter with the given per-principal cap.
// If max <= 0, DefaultMaxStreamsPerPrincipal is used.
func NewStreamLimiter(max int) *StreamLimiter {
	if max <= 0 {
		max = DefaultMaxStreamsPerPrincipal
	}
	return &StreamLimiter{
		max:    max,
		counts: make(map[string]int),
	}
}

// Max returns the configured per-principal concurrent stream cap.
func (l *StreamLimiter) Max() int {
	return l.max
}

// Active returns the number of currently active streams for principalKey.
func (l *StreamLimiter) Active(principalKey string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[principalKey]
}

// Acquire attempts to reserve one stream slot for principalKey. If the
// principal is already at the cap, it returns (nil, false). Otherwise it
// returns an idempotent release function that MUST be called when the stream
// ends.
func (l *StreamLimiter) Acquire(principalKey string) (release func(), ok bool) {
	l.mu.Lock()
	if l.counts[principalKey] >= l.max {
		l.mu.Unlock()
		return nil, false
	}
	l.counts[principalKey]++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if n := l.counts[principalKey]; n <= 1 {
				delete(l.counts, principalKey)
			} else {
				l.counts[principalKey] = n - 1
			}
			l.mu.Unlock()
		})
	}, true
}

// AcquireStream reserves a per-principal concurrent stream slot on s.
// If the principal is at max_streams_per_principal, it returns a *jsonrpc.Error
// with code CodeResourceExhausted (-32025). Reusable by events/stream (P3).
func (s *Server) AcquireStream(principalKey string) (func(), error) {
	release, ok := s.streams.Acquire(principalKey)
	if !ok {
		return nil, &jsonrpc.Error{
			Code:    CodeResourceExhausted,
			Message: fmt.Sprintf("maximum concurrent streams (%d) exceeded for principal", s.streams.Max()),
		}
	}
	return release, nil
}
