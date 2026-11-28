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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/hack/conduit-c19/c19driver"
	"github.com/GoogleCloudPlatform/scion/internal/conduittest"
	"github.com/GoogleCloudPlatform/scion/pkg/agentcontrol"
)

// brokerFlushSeparator matches pkg/agent/msgbuffer.go's "\n\n" join separator.
const brokerFlushSeparator = "\n\n"

// brokerFlushMaxAttempts and brokerFlushRetryBackoff mirror
// pkg/agent/msgbuffer.go's default pre-paste retry bounds (v2.22 §2.4).
const (
	brokerFlushMaxAttempts  = 3
	brokerFlushRetryBackoff = 500 * time.Millisecond
)

type queuedMsg struct {
	id   string
	text string
}

type flushOutcome struct {
	status  int
	outcome string
	err     error
}

type agentFlushQueue struct {
	mu      sync.Mutex
	flushMu sync.Mutex // serializes /deliver calls per agent (mirrors msgbuffer flushGate)
	pending []queuedMsg
	timer   *time.Timer
}

// c19BrokerFlusher is the fake broker's control-channel HTTP handler +
// debounced message flusher. It mirrors pkg/agent/msgbuffer.go and v2.22
// §1.6/§2.4:
//   - receives Hub POST /api/v1/agents/{slug}/message over the control channel,
//   - coalesces messages per agent over a configurable debounce window joined
//     with "\n\n",
//   - splits flushes along message boundaries to stay within MaxDeliverText
//     (512 KiB), MaxDeliverRequestBytes (760 KiB), and MaxDeliverMessages (64),
//   - serializes flushes per agent with a per-agent lock,
//   - retries proven-not-reached responses up to 3 times (500ms apart) reusing
//     the same delivery_id.
//
// Deviation from production: production uses a fixed 2s debounce window; C19
// allows a shorter --broker-debounce (default 150ms) so high-rate C19 runs
// complete flushes within the test window. This deviation is disclosed in the
// JSON report (broker_flush_mode, broker_debounce_ms) and README.
type c19BrokerFlusher struct {
	broker           *conduittest.Broker
	debounce         time.Duration
	extraSubmits     int
	extraSubmitDelay time.Duration

	mu       sync.Mutex
	slugToID map[string]string
	queues   map[string]*agentFlushQueue
	outcomes map[string]flushOutcome
	waiters  map[string][]chan flushOutcome
	wg       sync.WaitGroup
}

func newC19BrokerFlusher(slugToID map[string]string, debounce time.Duration, extraSubmits int, extraSubmitDelay time.Duration) *c19BrokerFlusher {
	if debounce <= 0 {
		debounce = 150 * time.Millisecond
	}
	cp := make(map[string]string, len(slugToID))
	for k, v := range slugToID {
		cp[k] = v
	}
	return &c19BrokerFlusher{
		debounce:         debounce,
		extraSubmits:     extraSubmits,
		extraSubmitDelay: extraSubmitDelay,
		slugToID:         cp,
		queues:           make(map[string]*agentFlushQueue),
		outcomes:         make(map[string]flushOutcome),
		waiters:          make(map[string][]chan flushOutcome),
	}
}

func (f *c19BrokerFlusher) SetBroker(b *conduittest.Broker) {
	f.mu.Lock()
	f.broker = b
	f.mu.Unlock()
}

func (f *c19BrokerFlusher) resolveAgentID(slugOrID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.slugToID[slugOrID]; ok {
		return id
	}
	return slugOrID
}

func (f *c19BrokerFlusher) queueFor(agentID string) *agentFlushQueue {
	f.mu.Lock()
	defer f.mu.Unlock()
	q, ok := f.queues[agentID]
	if !ok {
		q = &agentFlushQueue{}
		f.queues[agentID] = q
	}
	return q
}

// ServeHTTP handles Hub->broker control-channel dispatches
// (POST /api/v1/agents/{slug}/message).
func (f *c19BrokerFlusher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/api/v1/agents/") || !strings.HasSuffix(r.URL.Path, "/message") {
		http.NotFound(w, r)
		return
	}
	slugOrID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/agents/"), "/message")
	var req struct {
		MessageID         string `json:"message_id"`
		Message           string `json:"message"`
		DeliveryText      string `json:"delivery_text"`
		Interrupt         bool   `json:"interrupt"`
		StructuredMessage *struct {
			Msg string `json:"msg"`
		} `json:"structured_message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	text := req.DeliveryText
	if text == "" && req.StructuredMessage != nil {
		text = req.StructuredMessage.Msg
	}
	if text == "" {
		text = req.Message
	}
	agentID := f.resolveAgentID(slugOrID)
	f.enqueue(agentID, queuedMsg{id: req.MessageID, text: text}, req.Interrupt)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"accepted"}`))
}

func (f *c19BrokerFlusher) enqueue(agentID string, msg queuedMsg, immediate bool) {
	q := f.queueFor(agentID)
	q.mu.Lock()
	q.pending = append(q.pending, msg)
	totalLen := joinedBytes(q.pending)
	shouldFlushNow := immediate || len(q.pending) >= agentcontrol.MaxDeliverMessageIDs || totalLen >= agentcontrol.MaxDeliverText
	if shouldFlushNow {
		if q.timer != nil {
			q.timer.Stop()
			q.timer = nil
		}
		batch := q.pending
		q.pending = nil
		q.mu.Unlock()
		f.spawnFlush(agentID, q, batch)
		return
	}
	if q.timer != nil {
		q.timer.Stop()
	}
	q.timer = time.AfterFunc(f.debounce, func() {
		q.mu.Lock()
		batch := q.pending
		q.pending = nil
		q.timer = nil
		q.mu.Unlock()
		if len(batch) > 0 {
			f.spawnFlush(agentID, q, batch)
		}
	})
	q.mu.Unlock()
}

func (f *c19BrokerFlusher) spawnFlush(agentID string, q *agentFlushQueue, batch []queuedMsg) {
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		f.flushBatch(context.Background(), agentID, q, batch)
	}()
}

// FlushAll immediately flushes all pending per-agent queues and waits for all
// in-flight /deliver calls to complete before ctx expires.
func (f *c19BrokerFlusher) FlushAll(ctx context.Context) error {
	f.mu.Lock()
	agents := make([]string, 0, len(f.queues))
	queues := make([]*agentFlushQueue, 0, len(f.queues))
	for id, q := range f.queues {
		agents = append(agents, id)
		queues = append(queues, q)
	}
	f.mu.Unlock()

	for i, q := range queues {
		q.mu.Lock()
		if q.timer != nil {
			q.timer.Stop()
			q.timer = nil
		}
		batch := q.pending
		q.pending = nil
		q.mu.Unlock()
		if len(batch) > 0 {
			f.spawnFlush(agents[i], q, batch)
		}
	}

	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *c19BrokerFlusher) flushBatch(ctx context.Context, agentID string, q *agentFlushQueue, batch []queuedMsg) {
	q.flushMu.Lock()
	defer q.flushMu.Unlock()

	f.mu.Lock()
	broker := f.broker
	f.mu.Unlock()
	if broker == nil {
		f.recordOutcomes(batch, flushOutcome{err: errors.New("broker not initialized")})
		return
	}

	subFlushes := splitSubFlushes(batch)
	for _, sf := range subFlushes {
		ids := make([]string, 0, len(sf))
		parts := make([]string, 0, len(sf))
		for _, m := range sf {
			if m.id != "" {
				ids = append(ids, m.id)
			}
			parts = append(parts, m.text)
		}
		joined := strings.Join(parts, brokerFlushSeparator)
		text := joined
		req := &agentcontrol.DeliverRequest{
			V:          1,
			DeliveryID: "c19-" + uuid.NewString(),
			MessageIDs: ids,
			Op:         agentcontrol.OpPaste,
			Text:       &text,
			DeadlineMS: 92000,
			SubmitKeys: []string{"Enter"},
		}
		if f.extraSubmits > 0 {
			req.ExtraSubmits = &agentcontrol.ExtraSubmits{
				Count:   f.extraSubmits,
				DelayMS: int(f.extraSubmitDelay.Milliseconds()),
			}
		}

		var out flushOutcome
		for attempt := 1; attempt <= brokerFlushMaxAttempts; attempt++ {
			callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			status, resp, err := broker.DeliverMessage(callCtx, agentID, req)
			cancel()
			out = flushOutcome{status: status, outcome: string(resp.Outcome), err: err}
			if err == nil && status == http.StatusOK {
				break
			}
			if !isRetryablePrePaste(status, string(resp.Outcome)) || attempt == brokerFlushMaxAttempts {
				break
			}
			time.Sleep(brokerFlushRetryBackoff)
		}
		f.recordOutcomes(sf, out)
	}
}

func isRetryablePrePaste(status int, outcome string) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusNotImplemented, http.StatusUnprocessableEntity:
		return true
	case http.StatusConflict:
		return outcome == string(agentcontrol.OutcomeBusy)
	default:
		return false
	}
}

func (f *c19BrokerFlusher) recordOutcomes(batch []queuedMsg, out flushOutcome) {
	f.mu.Lock()
	for _, m := range batch {
		if m.id == "" {
			continue
		}
		f.outcomes[m.id] = out
		if ws, ok := f.waiters[m.id]; ok {
			for _, ch := range ws {
				ch <- out
			}
			delete(f.waiters, m.id)
		}
	}
	f.mu.Unlock()
}

// WaitForMessages blocks until every message ID in msgIDs has been flushed by
// the broker and returns the combined outcome (first non-OK/error if any).
func (f *c19BrokerFlusher) WaitForMessages(ctx context.Context, msgIDs []string) (int, string, error) {
	var lastStatus = http.StatusOK
	var lastOutcome = string(agentcontrol.OutcomeDelivered)
	for _, id := range msgIDs {
		out, err := f.waitOne(ctx, id)
		if err != nil {
			return 0, "", err
		}
		if out.err != nil || out.status != http.StatusOK || out.outcome != string(agentcontrol.OutcomeDelivered) {
			return out.status, out.outcome, out.err
		}
		lastStatus = out.status
		lastOutcome = out.outcome
	}
	return lastStatus, lastOutcome, nil
}

func (f *c19BrokerFlusher) waitOne(ctx context.Context, msgID string) (flushOutcome, error) {
	f.mu.Lock()
	if out, ok := f.outcomes[msgID]; ok {
		f.mu.Unlock()
		return out, nil
	}
	ch := make(chan flushOutcome, 1)
	f.waiters[msgID] = append(f.waiters[msgID], ch)
	f.mu.Unlock()

	select {
	case out := <-ch:
		return out, nil
	case <-ctx.Done():
		return flushOutcome{}, fmt.Errorf("waiting for broker /deliver of message %s: %w", msgID, ctx.Err())
	}
}

func joinedBytes(msgs []queuedMsg) int {
	if len(msgs) == 0 {
		return 0
	}
	n := 0
	for i, m := range msgs {
		if i > 0 {
			n += len(brokerFlushSeparator)
		}
		n += len(m.text)
	}
	return n
}

// splitSubFlushes splits msgs along message boundaries so each sub-flush fits
// within v2.22 §1.6 limits (<= 64 messages, joined text <= 512 KiB).
func splitSubFlushes(msgs []queuedMsg) [][]queuedMsg {
	if len(msgs) == 0 {
		return nil
	}
	var out [][]queuedMsg
	var cur []queuedMsg
	curBytes := 0
	for _, m := range msgs {
		add := len(m.text)
		if len(cur) > 0 {
			add += len(brokerFlushSeparator)
		}
		if len(cur) > 0 && (len(cur) >= agentcontrol.MaxDeliverMessageIDs || curBytes+add > agentcontrol.MaxDeliverText) {
			out = append(out, cur)
			cur = nil
			curBytes = 0
			add = len(m.text)
		}
		cur = append(cur, m)
		curBytes += add
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// splitChunksForJoin partitions body into chunks <= maxChunk bytes such that
// joining the chunks with sep ("\n\n") has total byte length == len(body) for
// 512 KiB payloads (so 33 chunks joined with "\n\n" equal exactly 524,288 bytes
// and fit in a single 33-row v2.22 /deliver flush).
func splitChunksForJoin(body string, maxChunk int, sep string) []string {
	if maxChunk <= 0 || len(body) <= maxChunk {
		return []string{body}
	}
	// Choose chunk count k so k chunks + (k-1)*len(sep) == len(body) and each
	// chunk <= maxChunk.
	k := (len(body) + maxChunk - 1) / maxChunk
	for {
		avail := len(body) - (k-1)*len(sep)
		if avail <= k*maxChunk {
			break
		}
		k++
	}
	payloadLen := len(body) - (k-1)*len(sep)
	base := payloadLen / k
	rem := payloadLen % k
	chunks := make([]string, 0, k)
	off := 0
	for i := 0; i < k; i++ {
		sz := base
		if i < rem {
			sz++
		}
		chunks = append(chunks, body[off:off+sz])
		off += sz
	}
	return chunks
}

var _ = c19driver.LargeBodyBytes // keep package import aligned with LargeBodyBytes
