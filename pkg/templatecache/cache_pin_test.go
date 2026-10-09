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

package templatecache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Eviction never removes an entry in use (ptone/scion#3274, P2.3 S4).

func entryFiles(n int) map[string][]byte {
	return map[string][]byte{"scion-agent.yaml": []byte(strings.Repeat("x", n))}
}

// TestCache_PinnedEntryNeverEvicted: an entry acquired (being read) is kept
// under eviction pressure, and evictable again once released.
func TestCache_PinnedEntryNeverEvicted(t *testing.T) {
	c, err := New(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put("hash-a", entryFiles(60)); err != nil {
		t.Fatal(err)
	}
	path, release, ok := c.Acquire("hash-a")
	if !ok {
		t.Fatal("Acquire missed")
	}
	if _, err := c.Put("hash-b", entryFiles(60)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "scion-agent.yaml")); err != nil {
		t.Fatalf("a pinned entry was evicted: %v", err)
	}
	release()
	release() // idempotent
	if _, err := c.Put("hash-c", entryFiles(60)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a released entry must be evictable again: %v", err)
	}
}

// TestCache_RecentlyUsedEntryKeptWithMinEvictAge: with a minimum eviction
// age, a path just returned by Get (read after the call, as hydration
// does) is not evicted by a concurrent Put; past the age it is.
func TestCache_RecentlyUsedEntryKeptWithMinEvictAge(t *testing.T) {
	c, err := New(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c.now = func() time.Time { return now }
	c.SetMinEvictAge(time.Minute)
	if _, err := c.Put("hash-a", entryFiles(60)); err != nil {
		t.Fatal(err)
	}
	path, ok := c.Get("hash-a")
	if !ok {
		t.Fatal("Get missed")
	}
	if _, err := c.Put("hash-b", entryFiles(60)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("an entry used within the minimum age was evicted: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := c.Put("hash-c", entryFiles(60)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an entry past the minimum age must be evictable: %v", err)
	}
}

// TestCache_SharedObjectReadsNeverRaceEviction: a reader (one instance's
// hydration) pins an entry and keeps reading its files while writers (other
// instances, through the same shared object) add entries that each force an
// eviction; the pinned entry's files are never removed under the reader.
func TestCache_SharedObjectReadsNeverRaceEviction(t *testing.T) {
	c, err := New(t.TempDir(), 150) // every new entry must evict something
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put("hot", entryFiles(60)); err != nil {
		t.Fatal(err)
	}
	path, release, ok := c.Acquire("hot")
	if !ok {
		t.Fatal("Acquire missed")
	}
	var missing atomic.Int32
	done := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			if _, err := os.ReadFile(filepath.Join(path, "scion-agent.yaml")); err != nil {
				missing.Add(1)
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	var writers sync.WaitGroup
	for w := 0; w < 2; w++ {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			for i := 0; i < 50; i++ {
				_, _ = c.Put(fmt.Sprintf("cold-%d-%d", w, i), entryFiles(80))
			}
		}(w)
	}
	writers.Wait()
	close(done)
	<-readerDone
	release()
	if n := missing.Load(); n != 0 {
		t.Fatalf("%d read(s) found the pinned entry's files evicted", n)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the pinned entry is gone: %v", err)
	}
}
