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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/hack/conduit-c19/c19driver"
	"github.com/GoogleCloudPlatform/scion/pkg/agentcontrol"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/clock"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/envelope"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/transport/ws"
	"github.com/GoogleCloudPlatform/scion/pkg/wsprotocol"
	conduitv1 "github.com/GoogleCloudPlatform/scion/proto/conduit/v1"
)

func TestParseFlags_ValidationAndEnvFallback(t *testing.T) {
	emptyEnv := func(string) string { return "" }

	// Missing required hub-url.
	_, err := parseFlags([]string{"--mode=phase2"}, emptyEnv)
	require.ErrorContains(t, err, "--hub-url")

	// Invalid mode.
	_, err = parseFlags([]string{"--mode=bogus", "--hub-url=http://h", "--agents-file=a.json", "--user-token=t"}, emptyEnv)
	require.ErrorContains(t, err, "invalid --mode")

	// Deliver mode requires broker-id and broker-secret.
	_, err = parseFlags([]string{"--mode=deliver", "--hub-url=http://h", "--agents-file=a.json", "--user-token=t"}, emptyEnv)
	require.ErrorContains(t, err, "--broker-id")

	// R1: L6 modes refuse to start without hub RSS budget/source or drain config
	// unless waived via --allow-unevaluated.
	_, err = parseFlags([]string{
		"--mode=phase2", "--hub-url=http://h", "--agents-file=a.json", "--user-token=t",
	}, emptyEnv)
	require.ErrorContains(t, err, "hub RSS")

	_, err = parseFlags([]string{
		"--mode=phase2", "--hub-url=http://h", "--agents-file=a.json", "--user-token=t",
		"--allow-unevaluated=hub_rss_peak",
	}, emptyEnv)
	require.ErrorContains(t, err, "planned drain")

	// Env fallback + base64 secret + hub-rss-pids + default rpc-concurrency=128.
	b64 := base64.StdEncoding.EncodeToString([]byte("secret-bytes"))
	envMap := map[string]string{
		"C19_HUB_URL":           "http://hub.local/",
		"C19_AGENTS_FILE":       "/tmp/agents.json",
		"C19_USER_TOKEN":        "tok-1",
		"C19_BROKER_ID":         "brk-1",
		"C19_BROKER_SECRET_B64": b64,
		"C19_HUB_RSS_PIDS":      "12, 34",
	}
	cfg, err := parseFlags([]string{
		"--mode=deliver",
		"--duration=10s",
		"--clock-skew=25ms",
		"--memory-budget-mb=512",
		"--expect-drain",
	}, func(k string) string { return envMap[k] })
	require.NoError(t, err)
	assert.Equal(t, c19driver.ModeDeliver, cfg.Mode)
	assert.Equal(t, "http://hub.local", cfg.HubURL)
	assert.Equal(t, "tok-1", cfg.UserToken)
	assert.Equal(t, "brk-1", cfg.BrokerID)
	assert.Equal(t, []byte("secret-bytes"), cfg.BrokerSecret)
	assert.Equal(t, 128, cfg.BrokerRPCConcurrency, "default --rpc-concurrency must be 128")
	assert.Equal(t, 25*time.Millisecond, cfg.ClockSkew)
	assert.Equal(t, []int{12, 34}, cfg.HubRSSPIDs)
	assert.Equal(t, int64(512*1024*1024), cfg.MemoryBudgetBytes)
	assert.NotEmpty(t, cfg.RunTag)
}

// R6: No self/MemStats fallback; unreadable configured PID fails the sample.
func TestMemorySampler_NoSelfFallbackAndUnreadablePIDFails(t *testing.T) {
	assert.Nil(t, makeMemorySampler(nil, ""), "must not fall back to driver self RSS")

	sampler := makeMemorySampler([]int{999999999}, "")
	require.NotNil(t, sampler)
	_, err := sampler(context.Background())
	require.Error(t, err, "unreadable PID must fail sample rather than returning partial sum")

	// Valid PID (current process) succeeds with source=proc_hub_pids and pid_count=1.
	validSampler := makeMemorySampler([]int{os.Getpid()}, "")
	require.NotNil(t, validSampler)
	s, err := validSampler(context.Background())
	require.NoError(t, err)
	assert.Greater(t, s.Bytes, int64(0))
	assert.Equal(t, "proc_hub_pids", s.Source)
	assert.Equal(t, 1, s.PIDCount)
}

func TestSplitMessageChunks(t *testing.T) {
	body := strings.Repeat("A", c19driver.LargeBodyBytes)
	chunks := splitMessageChunks(body, maxMessageChunkBytes)
	assert.Len(t, chunks, largeBodyRowsPerDelivery)
	assert.Equal(t, 33, len(chunks))
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c), maxMessageChunkBytes)
	}
	assert.Equal(t, body, strings.Join(chunks, ""))
}

type fakeAdmitter struct {
	mu    sync.Mutex
	hello *conduitv1.Hello
}

func (a *fakeAdmitter) Admit(_ context.Context, hello *conduitv1.Hello) (*conduitv1.Welcome, error) {
	a.mu.Lock()
	a.hello = hello
	a.mu.Unlock()
	return &conduitv1.Welcome{
		SessionId:       "sess-" + uuid.NewString()[:8],
		RelayInstanceId: "fake-hub-a",
	}, nil
}

func (a *fakeAdmitter) Refresh(_ context.Context, _ *conduitv1.AuthRefresh) error {
	return nil
}

type fakeMsgRow struct {
	state string
	text  string
}

// fakeLiveHub is an in-process HTTP + Conduit WS hub that mirrors real Hub
// semantics:
//   - /api/v1/conduit WS admission for fake agents and fake broker
//   - POST /api/v1/agents/{id}/message (enforces 16,000-char limit, stores row text, dispatches to broker)
//   - POST /api/v1/runtime-brokers/{brokerId}/agents/{agentId}/deliver (verifies concatenated stored row texts == delivered text, stamps not_after, calls agent /v1/control/deliver-message)
//   - GET /api/v1/agents/{id}/logs (routes envelope RPC over broker's conduit session and maps broker 429 -> HTTP 502 matching handleAgentLogs + brokerStatusError)
//   - GET /api/v1/agents/{id}/pty (preflight JSON or WS PTY echo + configurable 4503 close reason)
type fakeLiveHub struct {
	server *httptest.Server

	mu              sync.Mutex
	agentSessions   map[string]conduit.LocalSession
	brokerSession   conduit.LocalSession
	messages        map[string]fakeMsgRow
	drainPTYAfter   int
	drainPTYReason  string
	deliverCalls    atomic.Int64
	largeDeliveries atomic.Int64
	largeRowCounts  []int
	textMismatches  atomic.Int64
	messageRows     atomic.Int64
	brokerRPCCalls  atomic.Int64
}

func newFakeLiveHub(t *testing.T, drainPTYAfter int, drainPTYReason string) *fakeLiveHub {
	t.Helper()
	if drainPTYReason == "" {
		drainPTYReason = "relay_restart"
	}
	h := &fakeLiveHub{
		agentSessions:  make(map[string]conduit.LocalSession),
		messages:       make(map[string]fakeMsgRow),
		drainPTYAfter:  drainPTYAfter,
		drainPTYReason: drainPTYReason,
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/conduit", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		tr := ws.New(conn, ws.Options{})
		adm := &fakeAdmitter{}
		sess, err := conduit.Accept(r.Context(), tr, conduit.Config{Clock: clock.Real()}, adm)
		if err != nil {
			_ = conn.Close()
			return
		}
		ls := sess.(conduit.LocalSession)
		adm.mu.Lock()
		hello := adm.hello
		adm.mu.Unlock()
		h.mu.Lock()
		if hello.GetPrincipalKind() == conduitv1.PrincipalKind_PRINCIPAL_KIND_BROKER {
			h.brokerSession = ls
		} else if hello.GetPrincipalKind() == conduitv1.PrincipalKind_PRINCIPAL_KIND_AGENT {
			h.agentSessions[hello.GetPrincipalId()] = ls
		}
		h.mu.Unlock()
		<-ls.Done()
	})

	mux.HandleFunc("/api/v1/agents/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		agentID, sub := parts[0], parts[1]
		switch sub {
		case "message":
			var body struct {
				Message string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if len(body.Message) > maxMessageChunkBytes {
				http.Error(w, "message exceeds 16000 character limit", http.StatusBadRequest)
				return
			}
			msgID := uuid.NewString()
			h.mu.Lock()
			h.messages[msgID] = fakeMsgRow{state: "dispatched", text: body.Message}
			bs := h.brokerSession
			h.mu.Unlock()
			h.messageRows.Add(1)
			if bs != nil {
				rpcReq := envelope.RequestToRPC(wsprotocol.NewRequestEnvelope(
					"msg-"+msgID, http.MethodPost, "/api/v1/agents/"+agentID+"/message", "", nil, []byte(`{"message":"ok"}`),
				))
				_, _ = bs.Call(r.Context(), rpcReq)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message_id": msgID,
				"status":     "dispatched",
			})
		case "logs":
			h.brokerRPCCalls.Add(1)
			h.mu.Lock()
			bs := h.brokerSession
			h.mu.Unlock()
			if bs == nil {
				http.Error(w, "no broker session", http.StatusServiceUnavailable)
				return
			}
			rpcReq := envelope.RequestToRPC(wsprotocol.NewRequestEnvelope(
				"log-"+uuid.NewString()[:8], http.MethodGet, "/api/v1/agents/"+agentID+"/logs", "", nil, nil,
			))
			resp, err := bs.Call(r.Context(), rpcReq)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			// Mirror real Hub handleAgentLogs + brokerStatusError: any broker
			// status >= 400 (including conduit session 429) is returned as HTTP
			// 502 with "Failed to retrieve logs from broker: runtime broker returned error <code>".
			if resp.GetStatus() >= 400 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    "internal_error",
						"message": fmt.Sprintf("Failed to retrieve logs from broker: runtime broker returned error %d", resp.GetStatus()),
					},
				})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(int(resp.GetStatus()))
			_, _ = w.Write(resp.GetBody())
		case "pty":
			if !websocket.IsWebSocketUpgrade(r) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{"path": "agent"})
				return
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			echoes := 0
			for {
				var msg wsprotocol.PTYDataMessage
				if err := conn.ReadJSON(&msg); err != nil {
					return
				}
				if err := conn.WriteJSON(wsprotocol.NewPTYDataMessage(msg.Data)); err != nil {
					return
				}
				echoes++
				if h.drainPTYAfter > 0 && echoes >= h.drainPTYAfter {
					_ = conn.WriteControl(websocket.CloseMessage,
						websocket.FormatCloseMessage(wsprotocol.ClosePTYUpstreamUnavailable, h.drainPTYReason),
						time.Now().Add(time.Second))
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/api/v1/runtime-brokers/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/deliver") {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/runtime-brokers/"), "/")
		if len(parts) != 4 {
			http.NotFound(w, r)
			return
		}
		brokerID, agentID := parts[0], parts[2]
		var req agentcontrol.DeliverRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.mu.Lock()
		var storedText strings.Builder
		for _, mid := range req.MessageIDs {
			row, ok := h.messages[mid]
			if !ok {
				h.mu.Unlock()
				http.Error(w, "unknown message_id", http.StatusBadRequest)
				return
			}
			storedText.WriteString(row.text)
		}
		as := h.agentSessions[agentID]
		h.mu.Unlock()

		// R8 verification: concatenated stored row texts must equal delivered text.
		if req.Text != nil && storedText.String() != *req.Text {
			h.textMismatches.Add(1)
			http.Error(w, "stored row text does not match delivered text", http.StatusBadRequest)
			return
		}
		if as == nil {
			http.Error(w, "no agent session", http.StatusServiceUnavailable)
			return
		}
		req.BrokerID = brokerID
		req.NotAfterUnixMS = time.Now().Add(agentcontrol.DeliverNotAfter).UnixMilli()
		payload, err := json.Marshal(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rpcResp, err := as.Call(r.Context(), &conduitv1.RpcRequest{
			Method:  http.MethodPost,
			Path:    "/v1/control/deliver-message",
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    payload,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		h.deliverCalls.Add(1)
		if req.Text != nil && len(*req.Text) >= c19driver.LargeBodyBytes {
			h.largeDeliveries.Add(1)
			h.mu.Lock()
			h.largeRowCounts = append(h.largeRowCounts, len(req.MessageIDs))
			h.mu.Unlock()
		}
		if rpcResp.GetStatus() == http.StatusOK {
			h.mu.Lock()
			for _, mid := range req.MessageIDs {
				row := h.messages[mid]
				row.state = "delivered"
				h.messages[mid] = row
			}
			h.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(rpcResp.GetStatus()))
		_, _ = w.Write(rpcResp.GetBody())
	})

	h.server = httptest.NewServer(mux)
	t.Cleanup(h.server.Close)
	return h
}

func writeAgentsFixture(t *testing.T, count int) string {
	t.Helper()
	targets := make([]c19driver.AgentTarget, count)
	for i := 0; i < count; i++ {
		targets[i] = c19driver.AgentTarget{
			AgentID:   "agent-" + uuid.NewString()[:8],
			ProjectID: "proj-1",
			LaunchID:  "run-" + uuid.NewString()[:8],
			Token:     "agent-tok",
		}
	}
	raw, err := json.Marshal(targets)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "agents.json")
	require.NoError(t, os.WriteFile(p, raw, 0o600))
	return p
}

// Covers R1, R3, R4, R6, R7, R8 (33 rows per 512 KiB delivery whose texts
// concatenate to the delivered body), and message-ids-file auditability.
func TestCLI_EndToEnd_DeliverMode(t *testing.T) {
	hub := newFakeLiveHub(t, 0, "")
	agentsFile := writeAgentsFixture(t, 4)
	outFile := filepath.Join(t.TempDir(), "c19-deliver.json")
	idsFile := filepath.Join(t.TempDir(), "msg-ids.txt")

	cfg, err := parseFlags([]string{
		"--mode=deliver",
		"--run-tag=c19-e2e-deliver",
		"--hub-url=" + hub.server.URL,
		"--agents-file=" + agentsFile,
		"--user-token=user-tok",
		"--broker-id=broker-1",
		"--broker-secret=super-secret-key",
		"--duration=1200ms",
		"--deliver-interval=600ms",
		"--large-body-count=2",
		"--rpc-concurrency=16",
		"--broker-rpc-latency=5ms",
		"--baseline-samples=4",
		"--control-probe-rate=10",
		"--stalled-tunnel=false",
		"--extra-submits=2",
		"--extra-submit-delay=50ms",
		"--hub-rss-pids=" + strconv.Itoa(os.Getpid()),
		"--memory-budget-mb=4096",
		"--allow-unevaluated=planned_drain_4503,rotate_token_mix",
		"--message-ids-file=" + idsFile,
		"--output=" + outFile,
	}, func(string) string { return "" })
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	rep, err := run(context.Background(), cfg, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, c19driver.VerdictPass, rep.Verdict, "table:\n%s", stdout.String())
	assert.True(t, rep.Pass)
	assert.Equal(t, "c19-e2e-deliver", rep.RunTag)
	assert.GreaterOrEqual(t, rep.Counts.DeliverSucceeded, int64(4))
	assert.Equal(t, int64(2), rep.Counts.LargeBodySucceeded)
	assert.Equal(t, int64(0), rep.Counts.DeliverErrors)
	assert.Equal(t, int64(0), rep.Counts.BrokerRPCErrors)
	assert.Equal(t, int64(0), rep.Counts.Total429s)
	assert.Equal(t, int64(0), hub.textMismatches.Load(), "concatenated stored row texts must equal delivered text")
	assert.Equal(t, int64(2), hub.largeDeliveries.Load())
	assert.Equal(t, []int{33, 33}, hub.largeRowCounts, "each 512 KiB delivery must bind 33 message rows")
	assert.Equal(t, 33, rep.Counts.LargeBodyRowsPerDelivery)
	assert.Equal(t, hub.messageRows.Load(), rep.Counts.MessageRowsCreated)
	assert.Equal(t, 2, rep.Latencies.DeliverPaste512K.Count)
	assert.Greater(t, rep.Latencies.SendToReceipt.Count, 0)
	assert.Greater(t, rep.Latencies.CombinedDeliver.Count, 0)
	assert.Greater(t, rep.Latencies.ControlBaseline.Count, 0)
	assert.Greater(t, rep.Latencies.ControlLoad.Count, 0)
	assert.Greater(t, rep.Counts.MaxConcurrentBrokerSessionRPCs, int64(0))

	// Confirm every created message row ended in delivered state.
	hub.mu.Lock()
	for id, row := range hub.messages {
		assert.Equal(t, "delivered", row.state, "message row %s", id)
	}
	hub.mu.Unlock()

	// Confirm message IDs file contains exact created count.
	idLines := strings.Split(strings.TrimSpace(mustReadFile(t, idsFile)), "\n")
	assert.Len(t, idLines, int(rep.Counts.MessageRowsCreated))

	// Confirm output JSON file is valid and matches report.
	var diskRep c19driver.Report
	require.NoError(t, json.Unmarshal([]byte(mustReadFile(t, outFile)), &diskRep))
	assert.Equal(t, c19driver.VerdictPass, diskRep.Verdict)
	assert.Equal(t, rep.Counts.DeliverSucceeded, diskRep.Counts.DeliverSucceeded)
}

func mustReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

// R5: Broker-side handler latency proves real in-flight concurrency on the
// broker conduit session reaches >= 0.9 * configured concurrency.
func TestCLI_EndToEnd_BrokerRPCConcurrency(t *testing.T) {
	hub := newFakeLiveHub(t, 0, "")
	agentsFile := writeAgentsFixture(t, 4)

	const wantConc = 32
	cfg, err := parseFlags([]string{
		"--mode=broker-rpc",
		"--hub-url=" + hub.server.URL,
		"--agents-file=" + agentsFile,
		"--user-token=user-tok",
		"--broker-id=broker-1",
		"--broker-secret=super-secret-key",
		"--duration=600ms",
		"--rpc-concurrency=" + strconv.Itoa(wantConc),
		"--broker-rpc-latency=40ms",
		"--baseline-samples=2",
		"--control-probe-rate=10",
		"--stalled-tunnel=false",
		"--allow-unevaluated=hub_rss_peak,planned_drain_4503",
	}, func(string) string { return "" })
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	rep, err := run(context.Background(), cfg, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, c19driver.VerdictPass, rep.Verdict, "table:\n%s", stdout.String())
	const minExpected int64 = 29 // ceil(0.9 * 32)
	assert.GreaterOrEqual(t, rep.Counts.MaxConcurrentBrokerSessionRPCs, minExpected,
		"broker-side session high-water must reach >= 0.9x configured (%d), got %d", minExpected, rep.Counts.MaxConcurrentBrokerSessionRPCs)
	assert.Greater(t, rep.Counts.BrokerRPCSucceeded, int64(wantConc))
	assert.Equal(t, int64(0), rep.Counts.BrokerRPCErrors)
	assert.Equal(t, int64(0), rep.Counts.Total429s)
}

// R4: Broker-session 429s (triggered by a low MaxConcurrentRPCs on the broker
// conduit session and mapped by the Hub's /logs handler to HTTP 502) must be
// counted in http_429_count and FAIL both broker-rpc and deliver modes.
func TestCLI_EndToEnd_BrokerSession429Detected(t *testing.T) {
	for _, mode := range []string{"broker-rpc", "deliver"} {
		t.Run(mode, func(t *testing.T) {
			hub := newFakeLiveHub(t, 0, "")
			agentsFile := writeAgentsFixture(t, 2)

			args := []string{
				"--mode=" + mode,
				"--hub-url=" + hub.server.URL,
				"--agents-file=" + agentsFile,
				"--user-token=user-tok",
				"--broker-id=broker-1",
				"--broker-secret=super-secret-key",
				"--duration=600ms",
				"--rpc-concurrency=16",
				"--broker-rpc-latency=30ms",
				"--broker-max-concurrent-rpcs=2",
				"--baseline-samples=1",
				"--stalled-tunnel=false",
				"--allow-unevaluated=hub_rss_peak,planned_drain_4503,rotate_token_mix",
			}
			if mode == "deliver" {
				args = append(args, "--deliver-interval=500ms", "--large-body-count=1", "--extra-submits=0")
			}

			cfg, err := parseFlags(args, func(string) string { return "" })
			require.NoError(t, err)

			var stdout, stderr bytes.Buffer
			rep, err := run(context.Background(), cfg, &stdout, &stderr)
			require.NoError(t, err)
			assert.Equal(t, c19driver.VerdictFail, rep.Verdict, "table:\n%s", stdout.String())
			assert.False(t, rep.Pass)
			assert.Greater(t, rep.Counts.BrokerRPC429s, int64(0), "broker-session 429 mapped via hub 502 must be counted")
			assert.Greater(t, rep.Counts.Total429s, int64(0), "total 429 count must reflect broker-session 429s")
			assert.Greater(t, rep.Counts.BrokerRPCErrors, int64(0))
		})
	}
}

// Phase 2 + planned-drain 4503 reason check (relay_restart passes; other 4503
// reason is tracked separately and fails planned_drain_4503).
func TestCLI_EndToEnd_Phase2AndPlannedDrain4503(t *testing.T) {
	t.Run("relay_restart_passes", func(t *testing.T) {
		hub := newFakeLiveHub(t, 2, "relay_restart")
		agentsFile := writeAgentsFixture(t, 2)

		cfg, err := parseFlags([]string{
			"--mode=phase2",
			"--hub-url=" + hub.server.URL,
			"--agents-file=" + agentsFile,
			"--user-token=user-tok",
			"--duration=1200ms",
			"--pty-count=2",
			"--baseline-samples=3",
			"--control-probe-rate=10",
			"--stalled-tunnel=false",
			"--expect-drain",
			"--allow-unevaluated=hub_rss_peak",
		}, func(string) string { return "" })
		require.NoError(t, err)

		var stdout, stderr bytes.Buffer
		rep, err := run(context.Background(), cfg, &stdout, &stderr)
		require.NoError(t, err)
		assert.Equal(t, c19driver.VerdictPass, rep.Verdict, "table:\n%s", stdout.String())
		assert.GreaterOrEqual(t, rep.Counts.PTYEchoSucceeded, int64(4))
		assert.GreaterOrEqual(t, rep.Drain.RelayRestartCount, int64(2))
		assert.Equal(t, int64(0), rep.Drain.OtherCount)
	})

	t.Run("non_relay_restart_4503_fails", func(t *testing.T) {
		hub := newFakeLiveHub(t, 2, "broker_disconnected")
		agentsFile := writeAgentsFixture(t, 2)

		cfg, err := parseFlags([]string{
			"--mode=phase2",
			"--hub-url=" + hub.server.URL,
			"--agents-file=" + agentsFile,
			"--user-token=user-tok",
			"--duration=1200ms",
			"--pty-count=2",
			"--baseline-samples=3",
			"--control-probe-rate=10",
			"--stalled-tunnel=false",
			"--expect-drain",
			"--allow-unevaluated=hub_rss_peak",
		}, func(string) string { return "" })
		require.NoError(t, err)

		var stdout, stderr bytes.Buffer
		rep, err := run(context.Background(), cfg, &stdout, &stderr)
		require.NoError(t, err)
		assert.Equal(t, c19driver.VerdictFail, rep.Verdict, "table:\n%s", stdout.String())
		assert.Equal(t, int64(0), rep.Drain.RelayRestartCount)
		assert.GreaterOrEqual(t, rep.Drain.OtherCount, int64(2))
		assert.Contains(t, rep.Drain.OtherReasons, "broker_disconnected")
	})
}
