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

	_, err := parseFlags([]string{"--mode=phase2"}, emptyEnv)
	require.ErrorContains(t, err, "--hub-url")

	_, err = parseFlags([]string{"--mode=bogus", "--hub-url=http://h", "--agents-file=a.json", "--user-token=t"}, emptyEnv)
	require.ErrorContains(t, err, "invalid --mode")

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

	// Env fallback + base64 secret + hub-rss-pids + default rpc-concurrency=128 + no misleading user-id default.
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
	assert.Empty(t, cfg.UserID, "must not set a misleading default user-id (R-2)")
	assert.Equal(t, "brk-1", cfg.BrokerID)
	assert.Equal(t, []byte("secret-bytes"), cfg.BrokerSecret)
	assert.Equal(t, 128, cfg.BrokerRPCConcurrency, "default --rpc-concurrency must be 128")
	assert.Equal(t, 25*time.Millisecond, cfg.ClockSkew)
	assert.Equal(t, []int{12, 34}, cfg.HubRSSPIDs)
	assert.Equal(t, int64(512*1024*1024), cfg.MemoryBudgetBytes)
	assert.NotEmpty(t, cfg.RunTag)
}

// R-2: decodeTunnelStreamID uses shared relay.TunnelResponseStreamIDKey ("streamId")
// and resolveUserID resolves the principal ID from GET /api/v1/auth/me.
func TestR2_DecodeStreamIDAndResolveUserID(t *testing.T) {
	id, err := decodeTunnelStreamID([]byte(`{"streamId":42,"kind":"tcp"}`))
	require.NoError(t, err)
	assert.Equal(t, uint32(42), id)

	_, err = decodeTunnelStreamID([]byte(`{"stream_id":42}`))
	require.ErrorContains(t, err, `missing "streamId"`)

	_, err = decodeTunnelStreamID([]byte(`{"streamId":0}`))
	require.ErrorContains(t, err, "stream id 0")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/me" && r.Header.Get("Authorization") == "Bearer good-tok" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"user-real-123"}`))
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	uid, err := resolveUserID(context.Background(), srv.Client(), srv.URL, "good-tok")
	require.NoError(t, err)
	assert.Equal(t, "user-real-123", uid)

	_, err = resolveUserID(context.Background(), srv.Client(), srv.URL, "bad-tok")
	require.ErrorContains(t, err, "401")
}

// R6: No self/MemStats fallback; unreadable configured PID fails the sample.
func TestMemorySampler_NoSelfFallbackAndUnreadablePIDFails(t *testing.T) {
	assert.Nil(t, makeMemorySampler(nil, ""), "must not fall back to driver self RSS")

	sampler := makeMemorySampler([]int{999999999}, "")
	require.NotNil(t, sampler)
	_, err := sampler(context.Background())
	require.Error(t, err, "unreadable PID must fail sample rather than returning partial sum")

	validSampler := makeMemorySampler([]int{os.Getpid()}, "")
	require.NotNil(t, validSampler)
	s, err := validSampler(context.Background())
	require.NoError(t, err)
	assert.Greater(t, s.Bytes, int64(0))
	assert.Equal(t, "proc_hub_pids", s.Source)
	assert.Equal(t, 1, s.PIDCount)
}

// R-1 / R8: 512 KiB chunking with "\n\n" join fits inside MaxDeliverText
// (512 KiB) in a single sub-flush; 33 chunks of 15,000 raw bytes plus Hub
// DeliveryText framing (~450 B/row) land at ~510 KiB (>= LargeBodyMinBytes).
func TestSplitChunksForJoinAndSubFlushes(t *testing.T) {
	body := strings.Repeat("A", c19driver.LargeBodyBytes)
	chunks := splitChunksForJoin(body, maxMessageChunkBytes, brokerFlushSeparator)
	assert.GreaterOrEqual(t, len(chunks), largeBodyRowsPerDelivery)
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c), maxMessageChunkBytes)
	}
	joined := strings.Join(chunks, brokerFlushSeparator)
	assert.Equal(t, c19driver.LargeBodyBytes, len(joined))

	msgs := make([]queuedMsg, len(chunks))
	for i, c := range chunks {
		msgs[i] = queuedMsg{id: fmt.Sprintf("m-%d", i), text: c}
	}
	sub := splitSubFlushes(msgs)
	assert.Len(t, sub, 1, "chunks joined with \\n\\n must fit in one 512 KiB sub-flush")

	// Adding one more message overflows 512 KiB and splits into 2 sub-flushes.
	msgs = append(msgs, queuedMsg{id: "m-extra", text: "overflow"})
	sub2 := splitSubFlushes(msgs)
	assert.Len(t, sub2, 2)
	assert.Len(t, sub2[0], len(chunks))
	assert.Len(t, sub2[1], 1)
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

// fakePlumbingHub is a minimal in-process HTTP + Conduit WS stub used strictly
// for unit/plumbing tests of broker-session concurrency (R5), broker-session
// 429 -> 502 detection (R4), agent-session control-probe delay sensitivity (R-4),
// and planned-drain 4503 reason handling (R-5). Real Hub wire compatibility for
// all three modes (including control channel, stalled tunnel, and PG rows) is
// covered by TestConduitHA_C19_LiveCLI in pkg/hub/conduit_ha_c19_test.go.
type fakePlumbingHub struct {
	server *httptest.Server

	mu             sync.Mutex
	brokerSession  conduit.LocalSession
	drainPTYAfter  int
	drainPTYReason string
	ptyEchoDelayNS atomic.Int64
	brokerRPCCalls atomic.Int64
}

func newFakePlumbingHub(t *testing.T, drainPTYAfter int, drainPTYReason string) *fakePlumbingHub {
	t.Helper()
	if drainPTYReason == "" {
		drainPTYReason = "relay_restart"
	}
	h := &fakePlumbingHub{
		drainPTYAfter:  drainPTYAfter,
		drainPTYReason: drainPTYReason,
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fake-user-1"}`))
	})
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
				http.Error(w, "upgrade required", http.StatusUpgradeRequired)
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
				if d := time.Duration(h.ptyEchoDelayNS.Load()); d > 0 {
					time.Sleep(d)
				}
				if err := conn.WriteJSON(wsprotocol.NewPTYDataMessage(msg.Data)); err != nil {
					return
				}
				echoes++
				// Only close long-lived PTY workers (not single-echo control probes).
				if h.drainPTYAfter > 0 && echoes >= h.drainPTYAfter && !strings.Contains(string(msg.Data), "c19-ctrl-probe") {
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
			Slug:      fmt.Sprintf("agent-slug-%d", i),
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

// R5: Broker-side handler latency proves real in-flight concurrency on the
// broker conduit session reaches >= 0.9 * configured concurrency.
func TestCLI_Plumbing_BrokerRPCConcurrency(t *testing.T) {
	hub := newFakePlumbingHub(t, 0, "")
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
		"--baseline-samples=4",
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

// R4: Broker-session 429s (triggered by low MaxConcurrentRPCs on the broker
// conduit session and mapped by Hub /logs to HTTP 502) must be counted in
// http_429_count and FAIL the run.
func TestCLI_Plumbing_BrokerSession429Detected(t *testing.T) {
	hub := newFakePlumbingHub(t, 0, "")
	agentsFile := writeAgentsFixture(t, 2)

	cfg, err := parseFlags([]string{
		"--mode=broker-rpc",
		"--hub-url=" + hub.server.URL,
		"--agents-file=" + agentsFile,
		"--user-token=user-tok",
		"--broker-id=broker-1",
		"--broker-secret=super-secret-key",
		"--duration=600ms",
		"--rpc-concurrency=16",
		"--broker-rpc-latency=30ms",
		"--broker-max-concurrent-rpcs=2",
		"--baseline-samples=2",
		"--stalled-tunnel=false",
		"--allow-unevaluated=hub_rss_peak,planned_drain_4503",
	}, func(string) string { return "" })
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	rep, err := run(context.Background(), cfg, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, c19driver.VerdictFail, rep.Verdict, "table:\n%s", stdout.String())
	assert.False(t, rep.Pass)
	assert.Greater(t, rep.Counts.BrokerRPC429s, int64(0), "broker-session 429 mapped via hub 502 must be counted")
	assert.Greater(t, rep.Counts.Total429s, int64(0), "total 429 count must reflect broker-session 429s")
	assert.Greater(t, rep.Counts.BrokerRPCErrors, int64(0))
}

// R-4: Injecting delay into agent 0's PTY session during load must move
// control_p99_added past 100ms and FAIL the bar.
func TestCLI_Plumbing_ControlProbeDetectsSessionDelay(t *testing.T) {
	hub := newFakePlumbingHub(t, 0, "")
	agentsFile := writeAgentsFixture(t, 2)

	// Start with 0ms delay during baseline; inject 140ms delay on agent-0 PTY
	// once the load window begins (via DrainAfter hook).
	cfg, err := parseFlags([]string{
		"--mode=phase2",
		"--hub-url=" + hub.server.URL,
		"--agents-file=" + agentsFile,
		"--user-token=user-tok",
		"--duration=1200ms",
		"--pty-count=1",
		"--baseline-samples=4",
		"--control-probe-rate=8",
		"--stalled-tunnel=false",
		"--allow-unevaluated=hub_rss_peak,planned_drain_4503",
	}, func(string) string { return "" })
	require.NoError(t, err)

	// Inject 140ms delay into agent-0 PTY right after baseline finishes (~100ms).
	go func() {
		time.Sleep(150 * time.Millisecond)
		hub.ptyEchoDelayNS.Store(int64(140 * time.Millisecond))
	}()

	var stdout, stderr bytes.Buffer
	rep, err := run(context.Background(), cfg, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, c19driver.VerdictFail, rep.Verdict, "table:\n%s", stdout.String())
	assert.GreaterOrEqual(t, rep.Latencies.ControlP99AddedMS, 100.0,
		"injected agent-session delay must move control_p99_added >= 100ms (got %.2fms)", rep.Latencies.ControlP99AddedMS)
}

// R-5: Planned drain with trigger + deadline + post-drain work passes when
// reason is relay_restart, and fails when reason is not relay_restart.
func TestCLI_Plumbing_PlannedDrain4503(t *testing.T) {
	t.Run("relay_restart_with_trigger_passes", func(t *testing.T) {
		hub := newFakePlumbingHub(t, 2, "relay_restart")
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
			"--drain-after=100ms",
			"--drain-cmd=true",
			"--drain-deadline=5s",
			"--expect-drain",
			"--allow-unevaluated=hub_rss_peak",
		}, func(string) string { return "" })
		require.NoError(t, err)

		var stdout, stderr bytes.Buffer
		rep, err := run(context.Background(), cfg, &stdout, &stderr)
		require.NoError(t, err)
		assert.Equal(t, c19driver.VerdictPass, rep.Verdict, "table:\n%s", stdout.String())
		assert.GreaterOrEqual(t, rep.Drain.RelayRestartCount, int64(2))
		assert.True(t, rep.Drain.DeadlineEvaluated)
		assert.True(t, rep.Drain.ReplacementEvaluated)
		assert.Greater(t, rep.Drain.PostDrainWork, int64(0))
	})

	t.Run("non_relay_restart_4503_fails", func(t *testing.T) {
		hub := newFakePlumbingHub(t, 2, "broker_disconnected")
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
			"--drain-after=100ms",
			"--drain-cmd=true",
			"--drain-deadline=5s",
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

var _ = agentcontrol.OutcomeDelivered
