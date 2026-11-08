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
	"sync"
)

// ListCompleteness records whether a List read its runtime's whole scope.
// A caller that must not treat a partial listing as a full inventory (the
// broker heartbeat, which reconciles absent agents as missing) attaches one
// with WithListCompleteness. A runtime that skips part of its scope in a
// way that may hide actors marks it incomplete and still returns what it
// did read. A runtime that never marks it reports complete, so the List
// contract is unchanged for every other runtime and caller.
type ListCompleteness struct {
	mu         sync.Mutex
	incomplete bool
	reason     string
}

// MarkIncomplete records that the List did not read its whole scope. The
// first reason is kept.
func (c *ListCompleteness) MarkIncomplete(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.incomplete {
		c.reason = reason
	}
	c.incomplete = true
}

// Incomplete reports whether the List was marked incomplete, and why.
func (c *ListCompleteness) Incomplete() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.incomplete, c.reason
}

type listCompletenessKey struct{}

// WithListCompleteness attaches c to ctx for a List call.
func WithListCompleteness(ctx context.Context, c *ListCompleteness) context.Context {
	return context.WithValue(ctx, listCompletenessKey{}, c)
}

// markListIncomplete marks the ListCompleteness attached to ctx, if any.
func markListIncomplete(ctx context.Context, reason string) {
	if c, _ := ctx.Value(listCompletenessKey{}).(*ListCompleteness); c != nil {
		c.MarkIncomplete(reason)
	}
}
