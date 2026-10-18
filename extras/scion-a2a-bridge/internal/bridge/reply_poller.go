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
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/scion/extras/scion-a2a-bridge/internal/state"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	hubstore "github.com/GoogleCloudPlatform/scion/pkg/store"
)

// The reply poller returns an agent's reply to an A2A call by reading the
// caller's own direct conversation with the agent through the hub API, with
// the caller's own credential. The hub's read checks decide what it can see.
//
// It finds the conversation in the caller's list of direct conversations
// (the one whose dmPeer is the agent), reads the anchor (the message the
// bridge sent, by the ID the send returned) for its creation time, and then
// reads the conversation's newest messages after that time. The reply is
// the oldest agent message of a reply or question type that is newer than
// the anchor on the pages read. It is appended to the task's event log with
// the dedup key "poll:<hub message ID>", which only the poller produces, and
// the executor's wait accepts only such events (and a cancel).

const (
	// replyPollDedupPrefix prefixes the dedup key of every event the reply
	// poller appends. The executor's wait keys on it.
	replyPollDedupPrefix = "poll:"

	// replyPollPageSize is the number of messages one conversation read asks for.
	replyPollPageSize = 50

	// replyPollFirstDelay is the delay before the first read after the send.
	replyPollFirstDelay = time.Second
	// replyPollStep is added to the delay after each read that found no reply.
	replyPollStep = 500 * time.Millisecond
	// replyPollMaxDelay caps the delay between reads.
	replyPollMaxDelay = 3 * time.Second
)

// replyReader is the part of the hub conversation API the reply poller uses.
// hubclient.ConversationService satisfies it.
type replyReader interface {
	List(ctx context.Context, opts *hubclient.ListConversationsOptions) (*hubclient.ConversationListResult, error)
	GetMessage(ctx context.Context, conversationID, messageID string) (*hubstore.Message, error)
	ListMessages(ctx context.Context, conversationID string, opts *hubclient.ConversationMessagesOptions) (*hubstore.ListResult[hubstore.Message], error)
}

// pollOutcome is the result of one read of the caller's conversation. Its
// value is the reason code that log lines carry.
type pollOutcome string

const (
	pollFound  pollOutcome = "found"
	pollNotYet pollOutcome = "not_yet"
	pollError  pollOutcome = "error"
)

// replyRowKind classifies a conversation message for reply selection.
type replyRowKind int

const (
	// replyRowSkip is a message that is neither a reply nor a question;
	// scanning continues past it.
	replyRowSkip replyRowKind = iota
	// replyRowAnswer is a reply that completes the call.
	replyRowAnswer
	// replyRowQuestion is the agent asking the caller a question; the call
	// ends input-required.
	replyRowQuestion
)

// classifyReplyRow returns how a message from the agent counts for reply
// selection. The message types are the closed list of pkg/messages.
//
// An input-needed message is a question only when it carries a thread ID:
// the agent's own outbound messages filed in a direct conversation always
// do, while the hub's subscriber notifications (sent on WAITING_FOR_INPUT
// to every subscriber of the agent, whatever conversation it came from) do
// not, and are skipped.
func classifyReplyRow(m *hubstore.Message) replyRowKind {
	switch m.Type {
	case "", messages.TypeInstruction, messages.TypeReply, messages.TypeChat, messages.TypeGroupSet:
		return replyRowAnswer
	case messages.TypeInputNeeded:
		if m.ThreadID != "" {
			return replyRowQuestion
		}
		return replyRowSkip
	default:
		// state-change, system, mention and the retired assistant-reply.
		return replyRowSkip
	}
}

// selectReply scans one newest-first page of conversation messages. It
// reports whether the anchor is on the page and, if it is, returns the
// oldest message newer than the anchor that the agent sent with a reply or
// question type. A message is never taken as newer than the anchor unless
// the anchor itself is on the page: the page order (created, id) descending
// is the hub's keyset order, so the messages before the anchor on the page
// are exactly the newer ones.
func selectReply(items []hubstore.Message, anchorID, agentID string) (reply *hubstore.Message, anchorSeen bool) {
	anchorIdx := -1
	for i := range items {
		if items[i].ID == anchorID {
			anchorIdx = i
			break
		}
	}
	if anchorIdx < 0 {
		return nil, false
	}
	// Walk from just above the anchor towards the newest message, so the
	// first match is the oldest candidate.
	for i := anchorIdx - 1; i >= 0; i-- {
		m := &items[i]
		if m.SenderID != agentID {
			continue
		}
		if classifyReplyRow(m) != replyRowSkip {
			return m, true
		}
	}
	return nil, true
}

// polledReplyMessage converts a conversation message to the structured
// message the event pipeline appends.
func polledReplyMessage(m *hubstore.Message) *messages.StructuredMessage {
	return &messages.StructuredMessage{
		Version:   messages.Version,
		Type:      m.Type,
		Sender:    m.Sender,
		SenderID:  m.SenderID,
		Msg:       m.Msg,
		Timestamp: m.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// pollOnlyEvent is the wait predicate for a call with a live reply poller:
// it accepts only the poller's events and a canceled final event. Push
// events (agent-wide status, pushed content) are skipped.
func pollOnlyEvent(ev state.TaskEvent) bool {
	return strings.HasPrefix(ev.DedupKey, replyPollDedupPrefix) || isCanceledEvent(ev)
}

// replyPoller reads one call's reply from the caller's conversation with
// the agent. It runs in its own goroutine from start until stop.
type replyPoller struct {
	bridge    *Bridge
	reader    replyReader
	log       *slog.Logger
	taskID    string
	agentSlug string
	agentID   string
	anchorID  string

	// sleep waits for d or until ctx ends; it reports whether the full
	// delay elapsed. Tests replace it.
	sleep func(ctx context.Context, d time.Duration) bool

	// Resolved on the first reads, then reused.
	conversationID string
	afterBound     string

	lastOutcome pollOutcome

	done   atomic.Bool
	cancel context.CancelFunc
	exited chan struct{}
}

// newReplyPoller returns a poller for one call. anchorID is the hub message
// ID the send returned; it must not be empty.
func newReplyPoller(b *Bridge, reader replyReader, taskID, agentSlug, agentID, anchorID string) *replyPoller {
	return &replyPoller{
		bridge:    b,
		reader:    reader,
		log:       b.log,
		taskID:    taskID,
		agentSlug: agentSlug,
		agentID:   agentID,
		anchorID:  anchorID,
		sleep:     sleepContext,
		exited:    make(chan struct{}),
	}
}

// start runs the poller in a goroutine bound to ctx.
func (p *replyPoller) start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	go func() {
		defer close(p.exited)
		p.run(ctx)
	}()
}

// stop ends the poller and waits for its goroutine to exit, so that no
// poller event can be appended after stop returns.
func (p *replyPoller) stop() {
	p.done.Store(true)
	if p.cancel != nil {
		p.cancel()
	}
	<-p.exited
}

// run reads the conversation on the backoff schedule until it finds the
// reply or ctx ends.
func (p *replyPoller) run(ctx context.Context) {
	delay := replyPollFirstDelay
	for {
		if !p.sleep(ctx, delay) {
			return
		}
		outcome, reply := p.pollOnce(ctx)
		p.logOutcome(outcome)
		if outcome == pollFound {
			p.appendReply(ctx, reply)
			return
		}
		if delay += replyPollStep; delay > replyPollMaxDelay {
			delay = replyPollMaxDelay
		}
	}
}

// pollOnce makes one read of the caller's conversation.
func (p *replyPoller) pollOnce(ctx context.Context) (pollOutcome, *hubstore.Message) {
	if p.conversationID == "" {
		id, err := p.findConversation(ctx)
		if err != nil {
			return pollError, nil
		}
		if id == "" {
			return pollNotYet, nil
		}
		p.conversationID = id
	}
	if p.afterBound == "" {
		anchor, err := p.reader.GetMessage(ctx, p.conversationID, p.anchorID)
		if err != nil {
			return pollError, nil
		}
		// The handler parses whole-second RFC3339; one second before the
		// anchor, truncated, only widens the window.
		p.afterBound = anchor.CreatedAt.Add(-time.Second).UTC().Format(time.RFC3339)
	}
	page, err := p.reader.ListMessages(ctx, p.conversationID, &hubclient.ConversationMessagesOptions{
		After: p.afterBound,
		Limit: replyPollPageSize,
	})
	if err != nil {
		return pollError, nil
	}
	reply, _ := selectReply(page.Items, p.anchorID, p.agentID)
	if reply == nil {
		return pollNotYet, nil
	}
	return pollFound, reply
}

// findConversation returns the ID of the caller's direct conversation with
// the agent, or "" when it is not listed. The list carries no limit: the
// hub truncates after sorting, so with a limit the conversation could fall
// off the list.
func (p *replyPoller) findConversation(ctx context.Context) (string, error) {
	list, err := p.reader.List(ctx, &hubclient.ListConversationsOptions{Kind: "direct"})
	if err != nil {
		return "", err
	}
	for _, c := range list.Conversations {
		if c.DMPeer != nil && c.DMPeer.Kind == "agent" && c.DMPeer.ID == p.agentID {
			return c.ID, nil
		}
	}
	return "", nil
}

// appendReply appends the reply to the task's event log, unless the call
// has ended.
func (p *replyPoller) appendReply(ctx context.Context, reply *hubstore.Message) {
	if p.done.Load() || ctx.Err() != nil {
		return
	}
	p.bridge.processAndAppendEvent(ctx, p.taskID, p.agentSlug, polledReplyMessage(reply), replyPollDedupPrefix+reply.ID)
}

// logOutcome logs a read outcome when it differs from the previous one. The
// line carries the reason code only: no message text and no identifiers.
func (p *replyPoller) logOutcome(outcome pollOutcome) {
	if outcome == p.lastOutcome {
		return
	}
	p.lastOutcome = outcome
	p.log.Debug("reply poll", "reason", string(outcome))
}

// sleepContext waits for d or until ctx ends, and reports whether the full
// delay elapsed.
func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
