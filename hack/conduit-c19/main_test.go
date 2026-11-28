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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	// Env fallback + base64 secret + hub-rss-pids.
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
		"--rpc-concurrency=96",
		"--clock-skew=25ms",
		"--memory-budget-mb=512",
	}, func(k string) string { return envMap[k] })
	require.NoError(t, err)
	assert.Equal(t, c19driver.ModeDeliver, cfg.Mode)
	assert.Equal(t, "http://hub.local", cfg.HubURL)
	assert.Equal(t, "tok-1", cfg.UserToken)
	assert.Equal(t, "brk-1", cfg.BrokerID)
	assert.Equal(t, []byte("secret-bytes"), cfg.BrokerSecret)
	assert.Equal(t, 96, cfg.BrokerRPCConcurrency)
	assert.Equal(t, 25*time.Millisecond, cfg.ClockSkew)
	assert.Equal(t, []int{12, 34}, cfg.HubRSSPIDs)
	assert.Equal(t, int64(512*1024*1024), cfg.MemoryBudgetBytes)
}

// fakeLiveHub is a lightweight in-process HTTP + Conduit WS hub that exercises
// the exact live CLI wire paths:
//   - /api/v1/conduit WS admission for fake agents and fake broker
//   - POST /api/v1/agents/{id}/message (creates real message row, dispatches to broker)
//   - POST /api/v1/runtime-brokers/{brokerId}/agents/{agentId}/deliver (binds row, stamps not_after, calls agent /v1/control/deliver-message)
//   - GET /api/v1/agents/{id}/logs (routes envelope RPC over broker's conduit session)
//   - GET /api/v1/agents/{id}/pty (preflight JSON or WS PTY echo + optional 4503 drain close)
type fakeLiveHub struct {
	server *httptest.Server

	mu              sync.Mutex
	agentSessions   map[string]conduit.LocalSession
	brokerSession   conduit.LocalSession
	messages        map[string]string // msgID -> state ("dispatched" | "delivered")
	brokerRPCDelay  time.Duration
	drainPTYAfter   int // close PTY with 4503 after N echoes if > 0
	deliverCalls    atomic.Int64
	largeDeliveries atomic.Int64
	messageRows     atomic.Int64
	brokerRPCCalls  atomic.Int64
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

func newFakeLiveHub(t *testing.T, brokerRPCDelay time.Duration, drainPTYAfter int) *fakeLiveHub {
	t.Helper()
	h := &fakeLiveHub{
		agentSessions:  make(map[string]conduit.LocalSession),
		messages:       make(map[string]string),
		brokerRPCDelay: brokerRPCDelay,
		drainPTYAfter:  drainPTYAfter,
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
			msgID := uuid.NewString()
			h.mu.Lock()
			h.messages[msgID] = "dispatched"
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
			if h.brokerRPCDelay > 0 {
				select {
				case <-r.Context().Done():
					http.Error(w, "cancelled", http.StatusGatewayTimeout)
					return
				case <-time.After(h.brokerRPCDelay):
				}
			}
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
						websocket.FormatCloseMessage(wsprotocol.ClosePTYUpstreamUnavailable, "relay_restart"),
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
		for _, mid := range req.MessageIDs {
			if _, ok := h.messages[mid]; !ok {
				h.mu.Unlock()
				http.Error(w, "unknown message_id", http.StatusBadRequest)
				return
			}
		}
		as := h.agentSessions[agentID]
		h.mu.Unlock()
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
		}
		h.mu.Lock()
		for _, mid := range req.MessageIDs {
			h.messages[mid] = "delivered"
		}
		h.mu.Unlock()
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

func TestCLI_EndToEnd_DeliverMode(t *testing.T) {
	hub := newFakeLiveHub(t, 5*time.Millisecond, 0)
	agentsFile := writeAgentsFixture(t, 4)
	outFile := filepath.Join(t.TempDir(), "c19-deliver.json")

	cfg, err := parseFlags([]string{
		"--mode=deliver",
		"--hub-url=" + hub.server.URL,
		"--agents-file=" + agentsFile,
		"--user-token=user-tok",
		"--broker-id=broker-1",
		"--broker-secret=super-secret-key",
		"--duration=1200ms",
		"--deliver-interval=600ms",
		"--large-body-count=2",
		"--rpc-concurrency=16",
		"--baseline-samples=4",
		"--stalled-tunnel=false",
		"--extra-submits=2",
		"--extra-submit-delay=50ms",
		"--output=" + outFile,
	}, func(string) string { return "" })
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	rep, err := run(context.Background(), cfg, &stdout, &stderr)
	require.NoError(t, err)
	assert.True(t, rep.Pass, "deliver mode report must pass; table:\n%s", stdout.String())
	assert.GreaterOrEqual(t, rep.Counts.DeliverSucceeded, int64(4))
	assert.Equal(t, int64(2), rep.Counts.LargeBodySucceeded)
	assert.Equal(t, int64(0), rep.Counts.DeliverErrors)
	assert.Equal(t, int64(0), rep.Counts.Total429s)
	assert.GreaterOrEqual(t, hub.messageRows.Load(), int64(6), "real message rows must be created via POST /message")
	assert.GreaterOrEqual(t, hub.deliverCalls.Load(), int64(6), "real /deliver RPCs must reach agents")
	assert.Equal(t, int64(2), hub.largeDeliveries.Load(), "512 KiB deliveries must reach agents")
	assert.Greater(t, rep.Latencies.DeliverPaste512K.Count, 0, "512 KiB paste timing must be recorded")
	assert.Greater(t, rep.Latencies.SendToReceipt.Count, 0, "send-to-receipt timing must be recorded")
	assert.Greater(t, rep.Latencies.CombinedDeliver.Count, 0, "combined timing must be recorded")
	assert.Greater(t, rep.Latencies.ControlBaseline.Count, 0, "baseline control latency must be recorded")
	assert.Greater(t, rep.Latencies.ControlLoad.Count, 0, "load control latency must be recorded")
	assert.GreaterOrEqual(t, rep.Counts.MaxConcurrentBrokerRPCs, int64(4), "concurrent broker RPCs must run in parallel")

	// Confirm every created message row ended in delivered state.
	hub.mu.Lock()
	for id, st := range hub.messages {
		assert.Equal(t, "delivered", st, "message row %s", id)
	}
	hub.mu.Unlock()

	// Confirm output JSON file is valid and matches report.
	raw, err := os.ReadFile(outFile)
	require.NoError(t, err)
	var diskRep c19driver.Report
	require.NoError(t, json.Unmarshal(raw, &diskRep))
	assert.True(t, diskRep.Pass)
	assert.Equal(t, rep.Counts.DeliverSucceeded, diskRep.Counts.DeliverSucceeded)
}

func TestCLI_EndToEnd_BrokerRPCConcurrency(t *testing.T) {
	// Use a 40ms handler delay with --rpc-concurrency=32 so concurrent in-flight
	// broker RPCs demonstrably reach high concurrency (>=16, well above CI smoke's ~3).
	hub := newFakeLiveHub(t, 40*time.Millisecond, 0)
	agentsFile := writeAgentsFixture(t, 4)

	cfg, err := parseFlags([]string{
		"--mode=broker-rpc",
		"--hub-url=" + hub.server.URL,
		"--agents-file=" + agentsFile,
		"--user-token=user-tok",
		"--broker-id=broker-1",
		"--broker-secret=super-secret-key",
		"--duration=500ms",
		"--rpc-concurrency=32",
		"--baseline-samples=2",
		"--stalled-tunnel=false",
	}, func(string) string { return "" })
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	rep, err := run(context.Background(), cfg, &stdout, &stderr)
	require.NoError(t, err)
	assert.True(t, rep.Pass, "broker-rpc mode must pass; table:\n%s", stdout.String())
	assert.GreaterOrEqual(t, rep.Counts.MaxConcurrentBrokerRPCs, int64(16),
		"broker-rpc concurrency must actually reach configured high concurrency (got %d)", rep.Counts.MaxConcurrentBrokerRPCs)
	assert.Greater(t, rep.Counts.BrokerRPCSucceeded, int64(32))
	assert.Equal(t, int64(0), rep.Counts.BrokerRPCErrors)
	assert.Equal(t, int64(0), rep.Counts.Total429s)
}

func TestCLI_EndToEnd_Phase2AndPlannedDrain4503(t *testing.T) {
	hub := newFakeLiveHub(t, 0, 2) // close PTY with 4503 relay_restart after 2 echoes
	agentsFile := writeAgentsFixture(t, 2)

	cfg, err := parseFlags([]string{
		"--mode=phase2",
		"--hub-url=" + hub.server.URL,
		"--agents-file=" + agentsFile,
		"--user-token=user-tok",
		"--duration=1500ms",
		"--pty-count=2",
		"--baseline-samples=3",
		"--stalled-tunnel=false",
		"--expect-drain",
	}, func(string) string { return "" })
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	rep, err := run(context.Background(), cfg, &stdout, &stderr)
	require.NoError(t, err)
	assert.True(t, rep.Pass, "phase2 mode with expected 4503 drain must pass; table:\n%s", stdout.String())
	assert.GreaterOrEqual(t, rep.Counts.PTYEchoSucceeded, int64(4))
	assert.GreaterOrEqual(t, rep.Drain.Count4503, int64(2))
	assert.Contains(t, rep.Drain.Reasons, "relay_restart")
}
