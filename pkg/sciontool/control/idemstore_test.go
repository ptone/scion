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

package control

import (
	"fmt"
	"testing"
	"time"
)

func key(i int) idemKey { return idemKey{brokerID: "b", deliveryID: fmt.Sprintf("d-%d", i)} }

// TestIdemStore_Bounds: the store holds 1024 entries for 15 minutes,
// outcome only (design r3 §5.5 layer 2).
func TestIdemStore_Bounds(t *testing.T) {
	if idemMaxEntries != 1024 || idemTTL != 15*time.Minute {
		t.Fatalf("bounds %d / %s, want 1024 / 15m", idemMaxEntries, idemTTL)
	}
}

// TestIdemStore_TTL: an entry answers until its TTL passes, then is gone.
func TestIdemStore_TTL(t *testing.T) {
	now := newFakeClock().Now()
	s := newIdemStore(idemMaxEntries, idemTTL)
	out := idemOutcome{status: 200, outcome: outcomeDelivered}
	s.put(key(1), out, now)
	if got, ok := s.get(key(1), now.Add(idemTTL-time.Second)); !ok || got != out {
		t.Fatalf("before TTL: %v %v", got, ok)
	}
	if _, ok := s.get(key(1), now.Add(idemTTL)); ok {
		t.Fatal("entry answered at its TTL")
	}
	if s.len() != 0 {
		t.Fatalf("expired entry kept: len %d", s.len())
	}
}

// TestIdemStore_LRU: past 1024 entries the least recently used goes
// first; a get refreshes an entry's place.
func TestIdemStore_LRU(t *testing.T) {
	now := newFakeClock().Now()
	s := newIdemStore(idemMaxEntries, idemTTL)
	out := idemOutcome{status: 200, outcome: outcomeDelivered}
	for i := 0; i < idemMaxEntries; i++ {
		s.put(key(i), out, now)
	}
	if _, ok := s.get(key(0), now); !ok { // key 0 is now the most recent
		t.Fatal("key 0 missing")
	}
	s.put(key(idemMaxEntries), out, now)
	if s.len() != idemMaxEntries {
		t.Fatalf("len %d, want %d", s.len(), idemMaxEntries)
	}
	if _, ok := s.get(key(1), now); ok {
		t.Fatal("least recently used entry (key 1) not evicted")
	}
	for _, i := range []int{0, 2, idemMaxEntries} {
		if _, ok := s.get(key(i), now); !ok {
			t.Fatalf("key %d evicted", i)
		}
	}
}

// TestIdemStore_PutDropsExpired: a put drops entries whose TTL passed.
func TestIdemStore_PutDropsExpired(t *testing.T) {
	now := newFakeClock().Now()
	s := newIdemStore(idemMaxEntries, idemTTL)
	s.put(key(1), idemOutcome{status: 200}, now)
	s.put(key(2), idemOutcome{status: 200}, now.Add(idemTTL))
	if s.len() != 1 {
		t.Fatalf("len %d, want 1", s.len())
	}
}
