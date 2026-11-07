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

package tmuxdeliver

import (
	"fmt"
	"testing"
)

// TestMemoStore_EvictionAndBound ports pkg/agent's
// TestReadyMemoStore_EvictionAndBound (removed in S1b, when pkg/agent's own
// memo store was replaced by MemoStore), assertion for assertion. It is an
// internal test because get, markReady and markTimedOut are unexported.
func TestMemoStore_EvictionAndBound(t *testing.T) {
	var s MemoStore
	// 1. A recorded ready outcome is returned for its key and nonce.
	s.markReady("c1", "n1")
	if !s.get("c1", "n1").ready {
		t.Fatal("expected ready memo")
	}
	// 2. A nonce recorded for another key does not match.
	if s.get("c2", "n1").ready {
		t.Fatal("a nonce recorded for another container must not match")
	}
	// 3, 4. The first timeout for a nonce reports first, a second does not.
	if !s.markTimedOut("c1", "n2") {
		t.Fatal("first timeout for n2 should report first")
	}
	if s.markTimedOut("c1", "n2") {
		t.Fatal("second timeout for n2 should not report first")
	}
	// 5. A new nonce for a key drops the key's older entries.
	if s.get("c1", "n1").ready || s.Len() != 1 {
		t.Fatalf("a new nonce for c1 must evict n1; size=%d", s.Len())
	}
	// 6. The store never holds more than MaxMemos entries.
	for i := 0; i < MaxMemos+10; i++ {
		s.markReady(fmt.Sprintf("c-%d", i), fmt.Sprintf("n-%d", i))
	}
	if n := s.Len(); n > MaxMemos {
		t.Fatalf("size = %d, want at most %d", n, MaxMemos)
	}
	// 7. Clear drops every entry.
	s.Clear()
	if s.Len() != 0 {
		t.Fatal("clear left entries")
	}
}
