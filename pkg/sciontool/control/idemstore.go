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
	"container/list"
	"time"
)

// Idempotency store bounds (design r3 §5.5): at most idemMaxEntries
// finished deliveries, least recently used first out, each kept for
// idemTTL.
const (
	idemMaxEntries = 1024
	idemTTL        = 15 * time.Minute
)

// idemKey scopes a delivery: the hub-set broker id and the delivery id.
// The agent is implicit (one store per sciontool).
type idemKey struct {
	brokerID, deliveryID string
}

// idemOutcome is what a replay answers. It holds the outcome only, never
// the message text.
type idemOutcome struct {
	status  int
	outcome string
	stage   string
}

type idemEntry struct {
	key     idemKey
	out     idemOutcome
	expires time.Time
}

// idemStore is a bounded LRU of finished delivery outcomes with a TTL.
// It is not safe for concurrent use; the deliverer guards it.
type idemStore struct {
	max   int
	ttl   time.Duration
	order *list.List // front is the most recently used
	byKey map[idemKey]*list.Element
}

func newIdemStore(max int, ttl time.Duration) *idemStore {
	return &idemStore{max: max, ttl: ttl, order: list.New(), byKey: map[idemKey]*list.Element{}}
}

// get returns the stored outcome for k if it has not expired at now. A
// hit counts as a use.
func (s *idemStore) get(k idemKey, now time.Time) (idemOutcome, bool) {
	el, ok := s.byKey[k]
	if !ok {
		return idemOutcome{}, false
	}
	e := el.Value.(*idemEntry)
	if !now.Before(e.expires) {
		s.remove(el)
		return idemOutcome{}, false
	}
	s.order.MoveToFront(el)
	return e.out, true
}

// put records the outcome for k, replacing any earlier one, then drops
// expired entries and evicts the least recently used beyond max.
func (s *idemStore) put(k idemKey, out idemOutcome, now time.Time) {
	if el, ok := s.byKey[k]; ok {
		s.remove(el)
	}
	s.byKey[k] = s.order.PushFront(&idemEntry{key: k, out: out, expires: now.Add(s.ttl)})
	for el := s.order.Back(); el != nil; {
		prev := el.Prev()
		if s.order.Len() > s.max || !now.Before(el.Value.(*idemEntry).expires) {
			s.remove(el)
		}
		el = prev
	}
}

func (s *idemStore) remove(el *list.Element) {
	delete(s.byKey, el.Value.(*idemEntry).key)
	s.order.Remove(el)
}

func (s *idemStore) len() int { return s.order.Len() }
