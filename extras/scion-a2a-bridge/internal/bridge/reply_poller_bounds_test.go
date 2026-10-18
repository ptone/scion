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

package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	hubstore "github.com/GoogleCloudPlatform/scion/pkg/store"
)

// noWaitSleep is a sleep that returns at once while ctx is live.
func noWaitSleep(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }

// endCapture records the cause a poller ends its call with.
type endCapture struct {
	mu    sync.Mutex
	cause error
}

func (c *endCapture) end(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cause = err
}

func (c *endCapture) get() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cause
}

// TestReplyPoller_BackoffSchedule: the first read is 1 s after the send,
// then each empty read adds 0.5 s, up to 3 s (D8).
func TestReplyPoller_BackoffSchedule(t *testing.T) {
	b, _ := newLifecycleTestBridge(t)
	reader := &fakeReplyReader{
		byID: map[string]hubstore.Message{pollTestAnchorID: anchorRow()},
		page: []hubstore.Message{anchorRow()},
	}
	p := newReplyPoller(b, reader, "backoff-task", "helper", pollTestAgentID, pollTestAnchorID, "conv-1", nil)
	var delays []time.Duration
	p.sleep = func(_ context.Context, d time.Duration) bool {
		delays = append(delays, d)
		return len(delays) <= 7
	}
	p.run(context.Background())

	want := []time.Duration{
		time.Second, 1500 * time.Millisecond, 2 * time.Second, 2500 * time.Millisecond,
		3 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second,
	}
	if fmt.Sprint(delays) != fmt.Sprint(want) {
		t.Fatalf("delays %v, want %v", delays, want)
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if len(reader.messageOpts) != 7 {
		t.Fatalf("page reads = %d, want 7 (one per elapsed delay)", len(reader.messageOpts))
	}
}

// blockingReader blocks every page read until its context ends, counting
// the reads in flight.
type blockingReader struct {
	mu       sync.Mutex
	inFlight int
	max      int
	returned int
	entered  chan struct{}
}

func (r *blockingReader) List(context.Context, *hubclient.ListConversationsOptions) (*hubclient.ConversationListResult, error) {
	return &hubclient.ConversationListResult{}, nil
}

func (r *blockingReader) GetMessage(context.Context, string, string) (*hubstore.Message, error) {
	m := anchorRow()
	return &m, nil
}

func (r *blockingReader) ListMessages(ctx context.Context, _ string, _ *hubclient.ConversationMessagesOptions) (*hubstore.ListResult[hubstore.Message], error) {
	r.mu.Lock()
	r.inFlight++
	if r.inFlight > r.max {
		r.max = r.inFlight
	}
	r.mu.Unlock()
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	r.mu.Lock()
	r.inFlight--
	r.returned++
	r.mu.Unlock()
	return nil, ctx.Err()
}

func (r *blockingReader) stats() (inFlight, max, returned int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inFlight, r.max, r.returned
}

// TestReplyPoller_Stops covers the poller's ends: on the reply, on a
// terminal outcome, on cancel, and on stop, which joins the poller's
// goroutine (D8).
func TestReplyPoller_Stops(t *testing.T) {
	t.Run("on the reply", func(t *testing.T) {
		b, store := newLifecycleTestBridge(t)
		reply := dmRow("reply-msg", pollTestAgentID, messages.TypeInstruction, "dm:x", "the reply", time.Second)
		reader := &fakeReplyReader{
			byID: map[string]hubstore.Message{pollTestAnchorID: anchorRow()},
			page: []hubstore.Message{reply, anchorRow()},
		}
		ends := &endCapture{}
		p := newReplyPoller(b, reader, "stop-reply", "helper", pollTestAgentID, pollTestAnchorID, "conv-1", ends.end)
		p.sleep = noWaitSleep
		p.run(context.Background())
		if events, _ := store.ReadTaskEvents(context.Background(), "stop-reply", 0, 10); len(events) == 0 {
			t.Fatal("no reply appended")
		}
		if ends.get() != nil {
			t.Fatalf("call ended with %v, want the reply appended instead", ends.get())
		}
	})

	t.Run("on a terminal outcome", func(t *testing.T) {
		b, store := newLifecycleTestBridge(t)
		reader := &fakeReplyReader{listErr: &apiclient.APIError{StatusCode: http.StatusForbidden}}
		ends := &endCapture{}
		p := newReplyPoller(b, reader, "stop-terminal", "helper", pollTestAgentID, pollTestAnchorID, "", ends.end)
		p.sleep = noWaitSleep
		p.run(context.Background())
		var ended *replyPollEnded
		if !errors.As(ends.get(), &ended) || ended.outcome != pollForbidden || ended.Error() != replyMsgForbidden {
			t.Fatalf("call ended with %v, want the refusal %q", ends.get(), replyMsgForbidden)
		}
		reader.mu.Lock()
		lists := len(reader.listOpts)
		reader.mu.Unlock()
		if lists != 1 {
			t.Fatalf("lists = %d, want 1 (no retry after a refusal)", lists)
		}
		if events, _ := store.ReadTaskEvents(context.Background(), "stop-terminal", 0, 10); len(events) != 0 {
			t.Fatalf("%d events appended after a refusal, want 0", len(events))
		}
	})

	t.Run("on cancel", func(t *testing.T) {
		b, _ := newLifecycleTestBridge(t)
		reader := &fakeReplyReader{}
		p := newReplyPoller(b, reader, "stop-cancel", "helper", pollTestAgentID, pollTestAnchorID, "", nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p.run(ctx)
		reader.mu.Lock()
		defer reader.mu.Unlock()
		if len(reader.listOpts)+len(reader.messageOpts) != 0 {
			t.Fatal("a cancelled poller read the conversation")
		}
	})

	t.Run("stop joins the poller's goroutine", func(t *testing.T) {
		b, _ := newLifecycleTestBridge(t)
		reader := &blockingReader{entered: make(chan struct{}, 1)}
		p := newReplyPoller(b, reader, "stop-join", "helper", pollTestAgentID, pollTestAnchorID, "conv-1", nil)
		p.sleep = noWaitSleep
		p.start(context.Background())
		select {
		case <-reader.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the poller never read")
		}
		p.stop()
		if inFlight, _, returned := reader.stats(); inFlight != 0 || returned != 1 {
			t.Fatalf("after stop: %d reads in flight, %d returned; want 0 and 1", inFlight, returned)
		}
		select {
		case <-p.exited:
		default:
			t.Fatal("the poller's goroutine had not exited when stop returned")
		}
		if n := len(b.replyPollSem); n != 0 {
			t.Fatalf("%d read slots held after stop, want 0", n)
		}
		p.stop() // a second stop is harmless
	})
}

// TestReplyPoller_InFlightCap: 100 concurrent calls hold at most 64 reads
// in flight (reply_poll.concurrency default) (D8).
func TestReplyPoller_InFlightCap(t *testing.T) {
	b, _ := newLifecycleTestBridge(t)
	if c := cap(b.replyPollSem); c != defaultReplyPollConcurrency {
		t.Fatalf("read cap = %d, want %d", c, defaultReplyPollConcurrency)
	}
	reader := &blockingReader{entered: make(chan struct{}, 1)}
	pollers := make([]*replyPoller, 0, 100)
	for i := 0; i < 100; i++ {
		p := newReplyPoller(b, reader, "cap-task-"+strconv.Itoa(i), "helper", pollTestAgentID, pollTestAnchorID, "conv-1", nil)
		p.sleep = noWaitSleep
		p.start(context.Background())
		pollers = append(pollers, p)
	}
	defer func() {
		for _, p := range pollers {
			p.stop()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		inFlight, _, _ := reader.stats()
		if inFlight == defaultReplyPollConcurrency {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d reads in flight, want %d", inFlight, defaultReplyPollConcurrency)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if _, max, _ := reader.stats(); max != defaultReplyPollConcurrency {
		t.Fatalf("at most %d reads were in flight, want %d", max, defaultReplyPollConcurrency)
	}
}

// TestReplyPoller_AppendFence: nothing is appended (and so nothing is
// dispatched) once the call has ended or the fence refuses (D10).
func TestReplyPoller_AppendFence(t *testing.T) {
	reply := dmRow("fenced-reply", pollTestAgentID, messages.TypeInstruction, "dm:x", "a late reply", time.Second)
	tests := []struct {
		name    string
		prepare func(p *replyPoller) context.Context
		want    int
	}{
		{
			name: "standalone, lease lost: no append",
			prepare: func(p *replyPoller) context.Context {
				p.fence = func(context.Context) bool { return false }
				return context.Background()
			},
		},
		{
			name: "cancelled, then the reply arrives: no append",
			prepare: func(p *replyPoller) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		{
			name: "plugin, done set, then the reply arrives: no append",
			prepare: func(p *replyPoller) context.Context {
				p.done.Store(true)
				return context.Background()
			},
		},
		{
			name:    "live call: appended",
			prepare: func(p *replyPoller) context.Context { return context.Background() },
			want:    2, // the artifact and the message event
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, store := newLifecycleTestBridge(t)
			p := newReplyPoller(b, &fakeReplyReader{}, "fence-task", "helper", pollTestAgentID, pollTestAnchorID, "conv-1", nil)
			ctx := tc.prepare(p)
			m := reply
			p.appendReply(ctx, &m)
			events, err := store.ReadTaskEvents(context.Background(), "fence-task", 0, 10)
			if err != nil {
				t.Fatalf("read events: %v", err)
			}
			if len(events) != tc.want {
				t.Fatalf("%d events appended, want %d", len(events), tc.want)
			}
		})
	}

	t.Run("the standalone fence refuses without an SDK store", func(t *testing.T) {
		b, _ := newLifecycleTestBridge(t)
		if b.standaloneAppendFence("fence-task")(context.Background()) {
			t.Fatal("standalone fence accepted an append without an SDK store")
		}
	})
}

// pagedReader serves newest-first pages of 50 by offset cursor.
type pagedReader struct {
	mu     sync.Mutex
	rows   []hubstore.Message // newest first
	anchor hubstore.Message
	pages  int
}

func (r *pagedReader) List(context.Context, *hubclient.ListConversationsOptions) (*hubclient.ConversationListResult, error) {
	return &hubclient.ConversationListResult{}, nil
}

func (r *pagedReader) GetMessage(_ context.Context, _, messageID string) (*hubstore.Message, error) {
	if messageID != r.anchor.ID {
		return nil, &apiclient.APIError{StatusCode: http.StatusNotFound}
	}
	m := r.anchor
	return &m, nil
}

func (r *pagedReader) ListMessages(_ context.Context, _ string, opts *hubclient.ConversationMessagesOptions) (*hubstore.ListResult[hubstore.Message], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pages++
	offset, _ := strconv.Atoi(opts.Cursor)
	if offset > len(r.rows) {
		offset = len(r.rows)
	}
	end := offset + opts.Limit
	next := ""
	if end < len(r.rows) {
		next = strconv.Itoa(end)
	} else {
		end = len(r.rows)
	}
	return &hubstore.ListResult[hubstore.Message]{Items: append([]hubstore.Message(nil), r.rows[offset:end]...), NextCursor: next}, nil
}

func (r *pagedReader) pageCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pages
}

// newerRows returns n rows newer than the anchor, newest first: caller
// messages (another channel) and skipped agent rows, alternately.
func newerRows(n int) []hubstore.Message {
	rows := make([]hubstore.Message, 0, n)
	for i := n; i >= 1; i-- {
		offset := time.Duration(i) * time.Second
		if i%2 == 0 {
			m := dmRow(fmt.Sprintf("caller-%03d", i), "caller-uuid", messages.TypeInstruction, "", "a web message", offset)
			rows = append(rows, m)
		} else {
			rows = append(rows, dmRow(fmt.Sprintf("status-%03d", i), pollTestAgentID, messages.TypeStateChange, "", "a status notification", offset))
		}
	}
	return rows
}

// TestReplyPoller_Paging covers the page cursor and its bounds (D17).
func TestReplyPoller_Paging(t *testing.T) {
	t.Run("120 rows after the anchor: the reply is found through the cursor in one poll", func(t *testing.T) {
		b, _ := newLifecycleTestBridge(t)
		rows := newerRows(120)
		// The reply is among the oldest rows after the anchor.
		rows[118] = dmRow("the-reply", pollTestAgentID, messages.TypeInstruction, "dm:x", "the reply", 2*time.Second)
		rows = append(rows, anchorRow(), dmRow("older", pollTestAgentID, messages.TypeInstruction, "dm:x", "an earlier reply", -time.Second))
		reader := &pagedReader{rows: rows, anchor: anchorRow()}
		p := newReplyPoller(b, reader, "paging-a", "helper", pollTestAgentID, pollTestAnchorID, "conv-1", nil)
		outcome, reply := p.pollOnce(context.Background())
		if outcome != pollFound || reply == nil || reply.ID != "the-reply" {
			t.Fatalf("poll = %s %v, want found the-reply", outcome, reply)
		}
		if n := reader.pageCount(); n != 3 {
			t.Fatalf("pages read = %d, want 3", n)
		}
	})

	t.Run("more than 200 rows after the anchor: the call ends at once", func(t *testing.T) {
		b, store := newLifecycleTestBridge(t)
		rows := append(newerRows(250), anchorRow())
		reader := &pagedReader{rows: rows, anchor: anchorRow()}
		ends := &endCapture{}
		p := newReplyPoller(b, reader, "paging-b", "helper", pollTestAgentID, pollTestAnchorID, "conv-1", ends.end)
		p.sleep = noWaitSleep
		p.run(context.Background())
		var ended *replyPollEnded
		if !errors.As(ends.get(), &ended) || ended.Error() != replyMsgTooMany {
			t.Fatalf("call ended with %v, want %q", ends.get(), replyMsgTooMany)
		}
		if n := reader.pageCount(); n != replyPollMaxPages {
			t.Fatalf("pages read = %d, want %d", n, replyPollMaxPages)
		}
		if events, _ := store.ReadTaskEvents(context.Background(), "paging-b", 0, 10); len(events) != 0 {
			t.Fatalf("%d events appended, want 0", len(events))
		}
	})

	t.Run("anchor not on the pages and no further page: not yet, an earlier reply is never taken", func(t *testing.T) {
		b, store := newLifecycleTestBridge(t)
		rows := []hubstore.Message{
			dmRow("newer-status", pollTestAgentID, messages.TypeStateChange, "", "a status notification", time.Second),
			dmRow("earlier-reply", pollTestAgentID, messages.TypeInstruction, "dm:x", "an earlier reply", -2*time.Second),
		}
		reader := &pagedReader{rows: rows, anchor: anchorRow()}
		ends := &endCapture{}
		p := newReplyPoller(b, reader, "paging-c", "helper", pollTestAgentID, pollTestAnchorID, "conv-1", ends.end)
		if outcome, reply := p.pollOnce(context.Background()); outcome != pollNotYet || reply != nil {
			t.Fatalf("poll = %s %v, want not_yet with nothing taken", outcome, reply)
		}
		polls := 0
		p.sleep = func(ctx context.Context, _ time.Duration) bool {
			polls++
			return polls <= 3
		}
		p.run(context.Background())
		if ends.get() != nil {
			t.Fatalf("call ended with %v, want the poller to keep waiting", ends.get())
		}
		if events, _ := store.ReadTaskEvents(context.Background(), "paging-c", 0, 10); len(events) != 0 {
			t.Fatalf("%d events appended, want 0 (the earlier reply must not be returned)", len(events))
		}
	})
}
