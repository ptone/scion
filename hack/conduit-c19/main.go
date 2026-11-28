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

// Command conduit-c19 is the Criterion 19 live load driver for Scion Conduit
// HA verification (v2.17 / v2.22 §6.2). It is non-shipped validation tooling
// and is never compiled into production binaries.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/GoogleCloudPlatform/scion/hack/conduit-c19/c19driver"
	"github.com/GoogleCloudPlatform/scion/internal/conduittest"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/clock"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/envelope"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/relay"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/transport/ws"
	"github.com/GoogleCloudPlatform/scion/pkg/wsprotocol"
	conduitv1 "github.com/GoogleCloudPlatform/scion/proto/conduit/v1"
)

// maxMessageChunkBytes stays below messages.MaxMessageLength (16,000 chars)
// with ~1,000 bytes of headroom per row for the Hub's structured DeliveryText
// framing so that 33 coalesced rows total ~510-520 KiB and fit inside a single
// 512 KiB (524,288-byte) v2.22 /deliver flush without splitting.
const maxMessageChunkBytes = 15000

const largeBodyRowsPerDelivery = 33

type cliConfig struct {
	Mode                    c19driver.Mode
	RunTag                  string
	HubURL                  string
	HubURLs                 []string
	AgentsFile              string
	AgentLimit              int
	UserToken               string
	UserID                  string
	BrokerID                string
	BrokerSecret            []byte
	Duration                time.Duration
	DeliverInterval         time.Duration
	LargeBodyCount          int
	LargeBodyPacing         time.Duration
	RotateTokenCount        int
	RotateURLTemplate       string
	BrokerRPCCount          int
	BrokerRPCConcurrency    int
	BrokerRPCPacing         time.Duration
	BrokerRPCLatency        time.Duration
	BrokerMaxConcurrentRPCs int
	BrokerDebounce          time.Duration
	ControlConcurrency      int
	BaselineControlSamples  int
	ControlProbeRate        float64
	PTYCount                int
	StalledTunnel           bool
	StalledTunnelPort       int
	ExtraSubmits            int
	ExtraSubmitDelay        time.Duration
	ClockSkew               time.Duration
	HubRSSPIDs              []int
	HubRSSCmd               string
	MemoryBudgetBytes       int64
	DrainAfter              time.Duration
	DrainCmd                string
	DrainDeadline           time.Duration
	DrainDeadlineTolerance  time.Duration
	ExpectDrain             bool
	AllowUnevaluated        []string
	MessageIDsFile          string
	OutputPath              string
	JSONStdout              bool
	InsecureSkipVerify      bool
}

func main() {
	cfg, err := parseFlags(os.Args[1:], os.Getenv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "conduit-c19: %v\n", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	report, err := run(ctx, cfg, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conduit-c19: %v\n", err)
		os.Exit(1)
	}
	if report.Verdict != c19driver.VerdictPass || !report.Pass {
		os.Exit(1)
	}
}

func parseFlags(args []string, getenv func(string) string) (*cliConfig, error) {
	fs := flag.NewFlagSet("conduit-c19", flag.ContinueOnError)
	cfg := &cliConfig{}

	var (
		modeStr             string
		hubURLsStr          string
		brokerSecretStr     string
		brokerSecretB64     string
		hubRSSPIDsStr       string
		memoryBudgetMB      int64
		allowUnevaluatedStr string
	)

	fs.StringVar(&modeStr, "mode", string(c19driver.ModeDeliver), "Traffic mode: phase2, broker-rpc, or deliver")
	fs.StringVar(&cfg.RunTag, "run-tag", "", "Unique tag included in message payloads and report (default: auto-generated)")
	fs.StringVar(&cfg.HubURL, "hub-url", getenv("C19_HUB_URL"), "Hub or load-balancer base URL (env: C19_HUB_URL)")
	fs.StringVar(&hubURLsStr, "hub-urls", getenv("C19_HUB_URLS"), "Optional comma-separated hub URLs to split agents across")
	fs.StringVar(&cfg.AgentsFile, "agents-file", getenv("C19_AGENTS_FILE"), "Path to JSON file listing AgentTarget entries (env: C19_AGENTS_FILE)")
	fs.IntVar(&cfg.AgentLimit, "agents", 0, "Maximum number of agents to use from --agents-file (0 = all)")
	fs.StringVar(&cfg.UserToken, "user-token", getenv("C19_USER_TOKEN"), "User/admin bearer token for Hub API calls (prefer env C19_USER_TOKEN)")
	fs.StringVar(&cfg.UserID, "user-id", getenv("C19_USER_ID"), "User principal ID for Conduit tunnel Hello (optional; resolved from GET /api/v1/auth/me when unset)")
	fs.StringVar(&cfg.BrokerID, "broker-id", getenv("C19_BROKER_ID"), "Runtime broker ID for Conduit broker session, control channel, and /deliver (env: C19_BROKER_ID)")
	fs.StringVar(&brokerSecretStr, "broker-secret", getenv("C19_BROKER_SECRET"), "Raw broker HMAC secret (prefer env C19_BROKER_SECRET over CLI flag)")
	fs.StringVar(&brokerSecretB64, "broker-secret-b64", getenv("C19_BROKER_SECRET_B64"), "Base64-encoded broker HMAC secret (prefer env C19_BROKER_SECRET_B64)")
	fs.DurationVar(&cfg.Duration, "duration", 30*time.Second, "Active load duration")
	fs.DurationVar(&cfg.DeliverInterval, "deliver-interval", 2*time.Second, "Per-agent delivery cadence in deliver mode")
	fs.IntVar(&cfg.LargeBodyCount, "large-body-count", 10, "Number of 512 KiB deliveries to inject in deliver mode")
	fs.DurationVar(&cfg.LargeBodyPacing, "large-body-pacing", 0, "Delay between 512 KiB deliveries (0 = auto Duration/(count+1), negative = unpaced)")
	fs.IntVar(&cfg.RotateTokenCount, "rotate-count", 0, "Number of rotate-token calls (requires --rotate-url-template)")
	fs.StringVar(&cfg.RotateURLTemplate, "rotate-url-template", getenv("C19_ROTATE_URL_TEMPLATE"), "Optional URL template with {agent_id} for POST rotate-token control RPCs")
	fs.IntVar(&cfg.BrokerRPCCount, "broker-rpc-count", 0, "Total broker RPCs (0 = continuous for --duration)")
	fs.IntVar(&cfg.BrokerRPCConcurrency, "rpc-concurrency", 128, "Client-side concurrent broker-RPC workers (default 128; session high-water is reported separately)")
	fs.DurationVar(&cfg.BrokerRPCPacing, "rpc-pacing", 0, "Optional per-worker delay between broker RPCs for smooth pacing")
	fs.DurationVar(&cfg.BrokerRPCLatency, "broker-rpc-latency", 0, "Simulated latency inside the fake broker RPC handler so in-flight RPCs overlap on the broker session")
	fs.IntVar(&cfg.BrokerMaxConcurrentRPCs, "broker-max-concurrent-rpcs", 0, "Override MaxConcurrentRPCs on the fake broker conduit session (0 = library default 128)")
	fs.DurationVar(&cfg.BrokerDebounce, "broker-debounce", 500*time.Millisecond, "Debounce window for the fake broker's mirrored v2.22 message flusher (production default is 2s; disclosed in report)")
	fs.IntVar(&cfg.ControlConcurrency, "control-concurrency", 8, "Concurrent control RPC workers")
	fs.IntVar(&cfg.BaselineControlSamples, "baseline-samples", 30, "Baseline agent-session control probes measured before load starts (after 2 warmup probes)")
	fs.Float64Var(&cfg.ControlProbeRate, "control-probe-rate", c19driver.DefaultControlProbeRate, "Under-load control probe frequency in probes/sec (default 4.0)")
	fs.IntVar(&cfg.PTYCount, "pty-count", 4, "Number of concurrent PTY echo/keepalive sessions in phase2 mode (or drain observers)")
	fs.BoolVar(&cfg.StalledTunnel, "stalled-tunnel", true, "Open a stalled bulk TCP tunnel on agent 0")
	fs.IntVar(&cfg.StalledTunnelPort, "stalled-tunnel-port", 8080, "Exposed port on agent 0 for the stalled bulk TCP tunnel")
	fs.IntVar(&cfg.ExtraSubmits, "extra-submits", 2, "Extra submit count for deliver-message (shipped default: 2)")
	fs.DurationVar(&cfg.ExtraSubmitDelay, "extra-submit-delay", 300*time.Millisecond, "Delay between extra submits (shipped default: 300ms)")
	fs.DurationVar(&cfg.ClockSkew, "clock-skew", 0, "Known agent-minus-hub clock skew subtracted from send-to-receipt delay")
	fs.StringVar(&hubRSSPIDsStr, "hub-rss-pids", getenv("C19_HUB_RSS_PIDS"), "Comma-separated hub PIDs to sum VmRSS from /proc/<pid>/status")
	fs.StringVar(&cfg.HubRSSCmd, "hub-rss-cmd", getenv("C19_HUB_RSS_CMD"), "Optional shell command printing aggregate hub RSS in bytes")
	fs.Int64Var(&memoryBudgetMB, "memory-budget-mb", 0, "Peak hub RSS budget in MiB (required unless hub_rss_peak is in --allow-unevaluated)")
	fs.DurationVar(&cfg.DrainAfter, "drain-after", 0, "Delay into the load window before running --drain-cmd")
	fs.StringVar(&cfg.DrainCmd, "drain-cmd", "", "Shell command to trigger a planned hub drain")
	fs.DurationVar(&cfg.DrainDeadline, "drain-deadline", 30*time.Second, "Expected hub drain deadline for 4503 relay_restart closes")
	fs.DurationVar(&cfg.DrainDeadlineTolerance, "drain-deadline-tolerance", 5*time.Second, "Tolerance added to --drain-deadline when evaluating 4503 close timing")
	fs.BoolVar(&cfg.ExpectDrain, "expect-drain", false, "Evaluate planned_drain_4503 (relay_restart, deadline, and post-drain replacement work)")
	fs.StringVar(&allowUnevaluatedStr, "allow-unevaluated", "", "Comma-separated bar names explicitly permitted to remain unevaluated (echoed in report)")
	fs.StringVar(&cfg.MessageIDsFile, "message-ids-file", "", "Optional path to write created Hub message IDs (one per line) for PG audit")
	fs.StringVar(&cfg.OutputPath, "output", "", "Write machine-readable JSON report to this file path")
	fs.BoolVar(&cfg.JSONStdout, "json", false, "Print JSON report to stdout instead of the human table")
	fs.BoolVar(&cfg.InsecureSkipVerify, "insecure-skip-verify", false, "Skip TLS certificate verification (for self-signed testbed LB)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cfg.Mode = c19driver.Mode(modeStr)
	if !cfg.Mode.Valid() {
		return nil, fmt.Errorf("invalid --mode %q (want phase2, broker-rpc, or deliver)", modeStr)
	}
	if cfg.RunTag == "" {
		cfg.RunTag = "c19-" + uuid.NewString()[:8]
	}
	if hubURLsStr != "" {
		for _, u := range strings.Split(hubURLsStr, ",") {
			u = strings.TrimSpace(strings.TrimRight(u, "/"))
			if u != "" {
				cfg.HubURLs = append(cfg.HubURLs, u)
			}
		}
	}
	cfg.HubURL = strings.TrimRight(strings.TrimSpace(cfg.HubURL), "/")
	if cfg.HubURL == "" && len(cfg.HubURLs) > 0 {
		cfg.HubURL = cfg.HubURLs[0]
	}
	if cfg.HubURL == "" {
		return nil, errors.New("--hub-url (or C19_HUB_URL) is required")
	}
	if cfg.AgentsFile == "" {
		return nil, errors.New("--agents-file (or C19_AGENTS_FILE) is required")
	}
	if cfg.UserToken == "" {
		return nil, errors.New("--user-token (or C19_USER_TOKEN) is required")
	}
	if cfg.Duration <= 0 {
		return nil, errors.New("--duration must be positive")
	}
	if cfg.BrokerRPCConcurrency <= 0 {
		return nil, errors.New("--rpc-concurrency must be positive")
	}
	if cfg.ControlProbeRate <= 0 {
		return nil, errors.New("--control-probe-rate must be positive")
	}
	if brokerSecretB64 != "" {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(brokerSecretB64))
		if err != nil {
			return nil, fmt.Errorf("decode --broker-secret-b64: %w", err)
		}
		cfg.BrokerSecret = b
	} else if brokerSecretStr != "" {
		cfg.BrokerSecret = []byte(brokerSecretStr)
	}
	if cfg.Mode == c19driver.ModeDeliver || cfg.Mode == c19driver.ModeBrokerRPC {
		if cfg.BrokerID == "" {
			return nil, fmt.Errorf("--broker-id is required in %s mode", cfg.Mode)
		}
		if len(cfg.BrokerSecret) == 0 {
			return nil, fmt.Errorf("--broker-secret or --broker-secret-b64 is required in %s mode", cfg.Mode)
		}
	}
	if cfg.RotateTokenCount > 0 && cfg.RotateURLTemplate == "" {
		return nil, errors.New("--rotate-url-template is required when --rotate-count > 0")
	}
	if hubRSSPIDsStr != "" {
		for _, p := range strings.Split(hubRSSPIDsStr, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			pid, err := strconv.Atoi(p)
			if err != nil || pid <= 0 {
				return nil, fmt.Errorf("invalid PID %q in --hub-rss-pids", p)
			}
			cfg.HubRSSPIDs = append(cfg.HubRSSPIDs, pid)
		}
	}
	if memoryBudgetMB > 0 {
		cfg.MemoryBudgetBytes = memoryBudgetMB * 1024 * 1024
	}
	if allowUnevaluatedStr != "" {
		for _, name := range strings.Split(allowUnevaluatedStr, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				cfg.AllowUnevaluated = append(cfg.AllowUnevaluated, name)
			}
		}
	}

	if !slices.Contains(cfg.AllowUnevaluated, c19driver.BarHubRSSPeak) {
		if cfg.MemoryBudgetBytes <= 0 || (len(cfg.HubRSSPIDs) == 0 && cfg.HubRSSCmd == "") {
			return nil, errors.New("hub RSS measurement requires --memory-budget-mb and (--hub-rss-pids or --hub-rss-cmd), or pass --allow-unevaluated=hub_rss_peak")
		}
	}
	if !slices.Contains(cfg.AllowUnevaluated, c19driver.BarPlannedDrain4503) {
		if !cfg.ExpectDrain && cfg.DrainCmd == "" {
			return nil, errors.New("planned drain verification requires --expect-drain (and --drain-cmd/--drain-after), or pass --allow-unevaluated=planned_drain_4503")
		}
	}
	if cfg.DrainCmd != "" && !cfg.ExpectDrain {
		cfg.ExpectDrain = true
	}

	return cfg, nil
}

func loadAgentsFile(path string, limit int, hubURLs []string, defaultHubURL string) ([]c19driver.AgentTarget, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agents file %s: %w", path, err)
	}
	var targets []c19driver.AgentTarget
	if err := json.Unmarshal(raw, &targets); err != nil {
		return nil, fmt.Errorf("parse agents file %s: %w", path, err)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("agents file %s contains no agents", path)
	}
	if limit > 0 && limit < len(targets) {
		targets = targets[:limit]
	}
	for i := range targets {
		if targets[i].AgentID == "" || targets[i].Token == "" {
			return nil, fmt.Errorf("agent entry %d missing agent_id or token", i)
		}
		if targets[i].HubURL == "" {
			if len(hubURLs) > 0 {
				targets[i].HubURL = hubURLs[i%len(hubURLs)]
			} else {
				targets[i].HubURL = defaultHubURL
			}
		}
	}
	return targets, nil
}

func resolveUserID(ctx context.Context, hc *http.Client, hubURL, userToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL+"/api/v1/auth/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET /api/v1/auth/me: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET /api/v1/auth/me answered %d: %s (pass --user-id or C19_USER_ID if /auth/me is unavailable)", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var me struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &me); err != nil {
		return "", fmt.Errorf("decode /api/v1/auth/me: %w", err)
	}
	if me.ID == "" {
		return "", errors.New("GET /api/v1/auth/me returned empty id; pass --user-id or C19_USER_ID")
	}
	return me.ID, nil
}

func resolveAgentSlugs(ctx context.Context, hc *http.Client, hubURL, userToken string, targets []c19driver.AgentTarget) map[string]string {
	slugToID := make(map[string]string, len(targets)*2)
	for _, t := range targets {
		slugToID[t.AgentID] = t.AgentID
		if t.Slug != "" {
			slugToID[t.Slug] = t.AgentID
			continue
		}
		// Best-effort resolve slug from Hub GET /api/v1/agents/{id}.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL+"/api/v1/agents/"+t.AgentID, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+userToken)
		resp, err := hc.Do(req)
		if err != nil {
			continue
		}
		var info struct {
			Slug string `json:"slug"`
		}
		if resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&info)
		}
		_ = resp.Body.Close()
		if info.Slug != "" {
			slugToID[info.Slug] = t.AgentID
		}
	}
	return slugToID
}

func updateAtomicHighWater(high *atomic.Int64, cur int64) {
	for {
		prev := high.Load()
		if cur <= prev {
			return
		}
		if high.CompareAndSwap(prev, cur) {
			return
		}
	}
}

func run(ctx context.Context, cfg *cliConfig, stdout, stderr io.Writer) (c19driver.Report, error) {
	targets, err := loadAgentsFile(cfg.AgentsFile, cfg.AgentLimit, cfg.HubURLs, cfg.HubURL)
	if err != nil {
		return c19driver.Report{}, err
	}

	var tlsCfg *tls.Config
	if cfg.InsecureSkipVerify {
		tlsCfg = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in flag for self-signed testbed LB
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 256,
			IdleConnTimeout:     90 * time.Second,
			TLSClientConfig:     tlsCfg,
		},
		Timeout: 120 * time.Second,
	}

	// R-2: resolve authenticated user principal ID from Hub /api/v1/auth/me
	// when --user-id is omitted and the stalled tunnel is enabled.
	userID := cfg.UserID
	if cfg.StalledTunnel && userID == "" {
		resolveCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		userID, err = resolveUserID(resolveCtx, httpClient, cfg.HubURL, cfg.UserToken)
		cancel()
		if err != nil {
			return c19driver.Report{}, fmt.Errorf("resolve user id for stalled tunnel: %w", err)
		}
	}

	var (
		broker                 *conduittest.Broker
		flusher                *c19BrokerFlusher
		brokerSessionInFlight  atomic.Int64
		brokerSessionHighWater atomic.Int64
	)
	if cfg.BrokerID != "" && len(cfg.BrokerSecret) > 0 {
		rpcHandler := conduit.RPCHandlerFunc(func(rctx context.Context, req *conduitv1.RpcRequest) *conduitv1.RpcResponse {
			cur := brokerSessionInFlight.Add(1)
			updateAtomicHighWater(&brokerSessionHighWater, cur)
			defer brokerSessionInFlight.Add(-1)

			envReq := envelope.RPCToRequest(req)
			if cfg.BrokerRPCLatency > 0 && strings.HasSuffix(envReq.Path, "/logs") {
				select {
				case <-rctx.Done():
					return envelope.ResponseToRPC(wsprotocol.NewResponseEnvelope(
						envReq.RequestID, http.StatusGatewayTimeout,
						map[string]string{"Content-Type": "application/json"},
						[]byte(`{"error":"cancelled"}`),
					))
				case <-time.After(cfg.BrokerRPCLatency):
				}
			}
			switch {
			case strings.HasSuffix(envReq.Path, "/logs"):
				return envelope.ResponseToRPC(wsprotocol.NewResponseEnvelope(
					envReq.RequestID, http.StatusOK,
					map[string]string{"Content-Type": "application/json"},
					[]byte(`{"logs":"c19-broker-rpc-ok"}`),
				))
			case req.GetPath() == "/v1/broker/list", req.GetPath() == "/v1/broker/status":
				return &conduitv1.RpcResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}
			default:
				return envelope.ResponseToRPC(wsprotocol.NewResponseEnvelope(
					envReq.RequestID, http.StatusOK,
					map[string]string{"Content-Type": "application/json"},
					[]byte(`{"ok":true}`),
				))
			}
		})
		var sessCfg conduit.Config
		if cfg.BrokerMaxConcurrentRPCs > 0 {
			sessCfg.MaxConcurrentRPCs = cfg.BrokerMaxConcurrentRPCs
		}

		// R-1 (product path): in deliver mode, also connect the broker's real
		// control channel so Hub HybridBrokerClient.MessageAgent dispatches
		// reach c19BrokerFlusher, which mirrors v2.22 flush/batching and calls
		// /deliver itself (never racing /deliver from the CLI).
		var ccHandler http.Handler
		if cfg.Mode == c19driver.ModeDeliver {
			slugCtx, slugCancel := context.WithTimeout(ctx, 15*time.Second)
			slugToID := resolveAgentSlugs(slugCtx, httpClient, cfg.HubURL, cfg.UserToken, targets)
			slugCancel()
			flusher = newC19BrokerFlusher(slugToID, cfg.BrokerDebounce, cfg.ExtraSubmits, cfg.ExtraSubmitDelay)
			ccHandler = flusher
		}

		broker, err = conduittest.StartBroker(conduittest.BrokerOptions{
			HubURL:                cfg.HubURL,
			BrokerID:              cfg.BrokerID,
			BrokerSecret:          cfg.BrokerSecret,
			RPCHandler:            rpcHandler,
			Session:               sessCfg,
			ControlChannelHandler: ccHandler,
			HTTPClient:            httpClient,
			TLSClientConfig:       tlsCfg,
		})
		if err != nil {
			return c19driver.Report{}, fmt.Errorf("start fake broker: %w", err)
		}
		if flusher != nil {
			flusher.SetBroker(broker)
		}
		defer func() { _ = broker.Close() }()

		waitCtx, waitCancel := context.WithTimeout(ctx, 30*time.Second)
		err = broker.WaitHealthy(waitCtx)
		waitCancel()
		if err != nil {
			return c19driver.Report{}, fmt.Errorf("wait for broker conduit/control channel: %w", err)
		}
	}

	var (
		msgRowsCreated atomic.Int64
		msgIDsMu       sync.Mutex
		createdMsgIDs  []string
	)
	recordMsgIDs := func(ids []string) {
		msgRowsCreated.Add(int64(len(ids)))
		if cfg.MessageIDsFile != "" {
			msgIDsMu.Lock()
			createdMsgIDs = append(createdMsgIDs, ids...)
			msgIDsMu.Unlock()
		}
	}

	driverCfg := c19driver.Config{
		Mode:                     cfg.Mode,
		RunTag:                   cfg.RunTag,
		Agents:                   targets,
		DefaultHubURL:            cfg.HubURL,
		HTTPClient:               httpClient,
		TLSClientConfig:          tlsCfg,
		Duration:                 cfg.Duration,
		DeliverInterval:          cfg.DeliverInterval,
		LargeBodyCount:           cfg.LargeBodyCount,
		LargeBodyPacing:          cfg.LargeBodyPacing,
		RotateTokenCount:         cfg.RotateTokenCount,
		BrokerRPCCount:           cfg.BrokerRPCCount,
		BrokerRPCContinuous:      cfg.Mode == c19driver.ModeBrokerRPC || (cfg.Mode == c19driver.ModeDeliver && cfg.BrokerRPCCount == 0 && broker != nil),
		BrokerRPCConcurrency:     cfg.BrokerRPCConcurrency,
		BrokerRPCPacing:          cfg.BrokerRPCPacing,
		BrokerSessionHighWater:   func() int64 { return brokerSessionHighWater.Load() },
		ControlConcurrency:       cfg.ControlConcurrency,
		BaselineControlSamples:   cfg.BaselineControlSamples,
		ControlProbeRate:         cfg.ControlProbeRate,
		EnableBulkTunnelOnAgent0: cfg.StalledTunnel,
		EnablePTY:                true, // agent 0 PTY is used by the agent-session control probe in all modes (R-4)
		ClockSkew:                cfg.ClockSkew,
		DrainAfter:               cfg.DrainAfter,
		MemorySampler:            makeMemorySampler(cfg.HubRSSPIDs, cfg.HubRSSCmd),
		// R-4: control probe crosses agent 0's conduit session (sharing it with
		// the stalled bulk tunnel) and is completely off the broker-RPC churn path.
		ControlProbe: makeLiveAgentSessionProbe(cfg, tlsCfg),
	}

	if cfg.Mode == c19driver.ModeDeliver {
		driverCfg.Deliver = makeLiveDeliver(httpClient, cfg, flusher, recordMsgIDs)
		driverCfg.OnLoadComplete = flusher.FlushAll
	}
	if broker != nil {
		driverCfg.BrokerRPC = makeLiveBrokerRPC(httpClient, cfg, targets)
	}
	if cfg.RotateURLTemplate != "" {
		driverCfg.RotateToken = makeLiveRotateToken(httpClient, cfg)
	}
	if cfg.StalledTunnel {
		driverCfg.StalledTunnel = makeLiveStalledTunnel(cfg, userID, tlsCfg)
	}
	if cfg.Mode == c19driver.ModePhase2 || cfg.ExpectDrain {
		ptyCount := cfg.PTYCount
		if ptyCount <= 0 {
			ptyCount = 1
		}
		driverCfg.PTYCount = ptyCount
		driverCfg.PTYWorker = makeLivePTYWorker(cfg, tlsCfg)
	}
	if cfg.DrainCmd != "" {
		driverCfg.OnDrainTrigger = func(dctx context.Context) error {
			cmd := exec.CommandContext(dctx, "sh", "-c", cfg.DrainCmd)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("drain-cmd failed: %w: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		}
	}

	res, err := c19driver.Run(ctx, driverCfg)
	if err != nil {
		return c19driver.Report{}, err
	}

	if cfg.MessageIDsFile != "" {
		msgIDsMu.Lock()
		content := strings.Join(createdMsgIDs, "\n")
		if len(createdMsgIDs) > 0 {
			content += "\n"
		}
		msgIDsMu.Unlock()
		if err := os.WriteFile(cfg.MessageIDsFile, []byte(content), 0o644); err != nil {
			return c19driver.Report{}, fmt.Errorf("write message IDs file %s: %w", cfg.MessageIDsFile, err)
		}
	}

	evalOpts := c19driver.EvalOptions{
		MemoryBudgetBytes:        cfg.MemoryBudgetBytes,
		ExpectDrain:              cfg.ExpectDrain,
		DrainDeadline:            cfg.DrainDeadline,
		DrainDeadlineTolerance:   cfg.DrainDeadlineTolerance,
		AllowUnevaluated:         cfg.AllowUnevaluated,
		ClockSkew:                cfg.ClockSkew,
		HubURL:                   cfg.HubURL,
		RunTag:                   cfg.RunTag,
		MessageRowsCreated:       msgRowsCreated.Load(),
		LargeBodyRowsPerDelivery: largeBodyRowsPerDelivery,
		ExpectedLargeBodyCount:   cfg.LargeBodyCount,
	}
	if cfg.Mode == c19driver.ModeDeliver {
		evalOpts.BrokerFlushMode = "mirrored_v2_22_short_debounce"
		evalOpts.BrokerDebounceMS = cfg.BrokerDebounce.Milliseconds()
	}
	report := c19driver.BuildReport(res, evalOpts)

	jsonBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, fmt.Errorf("marshal report: %w", err)
	}
	jsonBytes = append(jsonBytes, '\n')

	if cfg.OutputPath != "" {
		if err := os.WriteFile(cfg.OutputPath, jsonBytes, 0o644); err != nil {
			return report, fmt.Errorf("write report %s: %w", cfg.OutputPath, err)
		}
		_, _ = fmt.Fprintf(stderr, "Wrote JSON report to %s\n", cfg.OutputPath)
	}

	if cfg.JSONStdout {
		_, _ = stdout.Write(jsonBytes)
	} else {
		_, _ = io.WriteString(stdout, c19driver.FormatTable(report))
	}
	return report, nil
}

// makeLiveDeliver posts real user messages via POST /api/v1/agents/{id}/message
// (splitting 512 KiB bodies into 33 chunks of 15,000 bytes posted with bounded
// concurrency so all 33 rows coalesce inside one debounce window into a single
// ~510-520 KiB v2.22 /deliver flush) and waits for the fake broker (which
// receives the Hub's control-channel dispatch) to flush and call /deliver itself.
// The CLI never calls /deliver directly.
func makeLiveDeliver(hc *http.Client, cfg *cliConfig, flusher *c19BrokerFlusher, onIDs func([]string)) func(context.Context, string, string) (int, string, error) {
	return func(ctx context.Context, agentID, body string) (int, string, error) {
		var chunks []string
		if len(body) >= c19driver.LargeBodyMinBytes {
			// Produce 33 chunks of 15,000 bytes (495,000 raw bytes); with the
			// Hub's ~450-byte DeliveryText wrapper per row, the coalesced flush
			// lands at ~510 KiB (>= LargeBodyMinBytes and <= 512 KiB cap).
			chunks = make([]string, largeBodyRowsPerDelivery)
			chunkText := strings.Repeat("X", maxMessageChunkBytes)
			for i := range chunks {
				chunks[i] = chunkText
			}
		} else {
			chunks = splitChunksForJoin(body, maxMessageChunkBytes, brokerFlushSeparator)
		}

		// Post chunks sequentially so chunk order is preserved and small
		// single-host testbeds are not CPU-stampeded; the broker flusher's
		// sliding debounce timer coalesces all chunks into one ~512 KiB flush.
		msgIDs := make([]string, len(chunks))
		for i, ch := range chunks {
			id, st, err := createHubMessageRow(ctx, hc, cfg.HubURL, cfg.UserToken, agentID, ch)
			if err != nil {
				return st, "", err
			}
			msgIDs[i] = id
		}
		if onIDs != nil {
			onIDs(msgIDs)
		}
		return flusher.WaitForMessages(ctx, msgIDs)
	}
}

func createHubMessageRow(ctx context.Context, hc *http.Client, hubURL, userToken, agentID, chunk string) (string, int, error) {
	payload, err := json.Marshal(map[string]any{"message": chunk})
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hubURL+"/api/v1/agents/"+agentID+"/message", bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := hc.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, &c19driver.HTTPStatusError{
			Status: resp.StatusCode,
			Err:    fmt.Errorf("POST /api/v1/agents/%s/message: %s", agentID, strings.TrimSpace(string(raw))),
		}
	}
	var out struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", resp.StatusCode, fmt.Errorf("decode message response: %w", err)
	}
	if out.MessageID == "" {
		return "", resp.StatusCode, errors.New("POST /message response missing message_id")
	}
	return out.MessageID, resp.StatusCode, nil
}

// isBrokerSession429Body detects a broker conduit session 429 refusal that the
// Hub's handleAgentLogs maps to HTTP 502 with brokerStatusError's text
// ("runtime broker returned error 429").
func isBrokerSession429Body(status int, body string) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	return status == http.StatusBadGateway && strings.Contains(body, "runtime broker returned error 429")
}

func makeLiveBrokerRPC(hc *http.Client, cfg *cliConfig, targets []c19driver.AgentTarget) func(context.Context, int) error {
	return func(ctx context.Context, seq int) error {
		agentID := targets[seq%len(targets)].AgentID
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.HubURL+"/api/v1/agents/"+agentID+"/logs?tail=1", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.UserToken)
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
			return nil
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		bodyStr := strings.TrimSpace(string(raw))
		return &c19driver.HTTPStatusError{
			Status:     resp.StatusCode,
			Session429: isBrokerSession429Body(resp.StatusCode, bodyStr),
			Err:        fmt.Errorf("GET /api/v1/agents/%s/logs answered %d: %s", agentID, resp.StatusCode, bodyStr),
		}
	}
}

func makeLiveRotateToken(hc *http.Client, cfg *cliConfig) func(context.Context, string) error {
	return func(ctx context.Context, agentID string) error {
		u := strings.ReplaceAll(cfg.RotateURLTemplate, "{agent_id}", agentID)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.UserToken)
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16*1024))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return &c19driver.HTTPStatusError{
				Status: resp.StatusCode,
				Err:    fmt.Errorf("rotate-token %s answered %d", u, resp.StatusCode),
			}
		}
		return nil
	}
}

// makeLiveAgentSessionProbe opens a PTY WebSocket to agentID (agent 0, sharing
// its conduit session with the stalled bulk TCP tunnel and off the broker-RPC
// churn path), sends one echo frame, waits for the echo from the agent's fake
// PTY, and closes cleanly. This measures real agent-session responsiveness and
// detects session-window / head-of-line stalls (R-4).
func makeLiveAgentSessionProbe(cfg *cliConfig, tlsCfg *tls.Config) func(context.Context, string) error {
	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig:  tlsCfg,
	}
	return func(ctx context.Context, agentID string) error {
		u := httpToWSURL(cfg.HubURL) + "/api/v1/agents/" + agentID + "/pty?cols=80&rows=24"
		conn, resp, err := dialer.DialContext(ctx, u, http.Header{"Authorization": {"Bearer " + cfg.UserToken}})
		if err != nil {
			if resp != nil {
				defer func() { _ = resp.Body.Close() }()
				return &c19driver.HTTPStatusError{Status: resp.StatusCode, Err: fmt.Errorf("pty probe dial: %w", err)}
			}
			return err
		}
		defer func() {
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "probe-done"),
				time.Now().Add(time.Second))
			_ = conn.Close()
		}()

		const probePayload = "c19-ctrl-probe"
		deadline := time.Now().Add(5 * time.Second)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		_ = conn.SetWriteDeadline(deadline)
		if err := conn.WriteJSON(wsprotocol.NewPTYDataMessage([]byte(probePayload))); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(deadline)
		var got strings.Builder
		for !strings.Contains(got.String(), probePayload) {
			var msg wsprotocol.PTYDataMessage
			if err := conn.ReadJSON(&msg); err != nil {
				return err
			}
			if msg.Type == wsprotocol.TypeData {
				got.Write(msg.Data)
			}
		}
		return nil
	}
}

func httpToWSURL(httpURL string) string {
	if strings.HasPrefix(httpURL, "https://") {
		return "wss://" + strings.TrimPrefix(httpURL, "https://")
	}
	return "ws://" + strings.TrimPrefix(httpURL, "http://")
}

// decodeTunnelStreamID decodes the user-side stream ID from a POST /v1/tunnels
// JSON response using the shared relay.TunnelResponseStreamIDKey ("streamId").
func decodeTunnelStreamID(body []byte) (uint32, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return 0, fmt.Errorf("decode tunnel response: %w", err)
	}
	raw, ok := fields[relay.TunnelResponseStreamIDKey]
	if !ok {
		return 0, fmt.Errorf("tunnel response missing %q field: %s", relay.TunnelResponseStreamIDKey, strings.TrimSpace(string(body)))
	}
	var id uint32
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, fmt.Errorf("decode %q: %w", relay.TunnelResponseStreamIDKey, err)
	}
	if id == 0 {
		return 0, fmt.Errorf("tunnel response returned stream id 0")
	}
	return id, nil
}

func makeLiveStalledTunnel(cfg *cliConfig, userID string, tlsCfg *tls.Config) func(context.Context, string) (func(), error) {
	return func(ctx context.Context, agentID string) (func(), error) {
		var (
			mu      sync.Mutex
			streams = map[uint32]conduit.Stream{}
		)
		sessCfg := conduit.Config{
			Clock: clock.Real(),
			StreamHandler: conduit.StreamHandlerFunc(func(_ context.Context, _ *conduitv1.StreamOpen, ps conduit.PendingStream) error {
				st, err := ps.Accept()
				if err != nil {
					return nil
				}
				mu.Lock()
				streams[st.ID()] = st
				mu.Unlock()
				return nil
			}),
		}
		wsDialer := &websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
			TLSClientConfig:  tlsCfg,
		}
		d := &ws.Dialer{
			URL: httpToWSURL(cfg.HubURL) + "/api/v1/conduit",
			WS:  wsDialer,
			Header: func(context.Context) (http.Header, error) {
				return http.Header{"Authorization": {"Bearer " + cfg.UserToken}}, nil
			},
		}
		hello := &conduitv1.Hello{
			PrincipalKind: conduitv1.PrincipalKind_PRINCIPAL_KIND_USER,
			PrincipalId:   userID,
		}
		dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		s, _, err := conduit.Dial(dialCtx, d, sessCfg, hello)
		if err != nil {
			return nil, fmt.Errorf("dial user conduit: %w", err)
		}
		ls := s.(conduit.LocalSession)

		body, err := json.Marshal(map[string]any{
			"agent":  agentID,
			"kind":   "tcp",
			"params": map[string]string{"port": strconv.Itoa(cfg.StalledTunnelPort)},
		})
		if err != nil {
			_ = ls.Close()
			return nil, err
		}
		resp, err := ls.Call(dialCtx, &conduitv1.RpcRequest{
			Method: http.MethodPost,
			Path:   relay.TunnelsRPCPath,
			Body:   body,
		})
		if err != nil {
			_ = ls.Close()
			return nil, fmt.Errorf("open tcp tunnel RPC: %w", err)
		}
		if resp.GetStatus() != http.StatusOK {
			_ = ls.Close()
			return nil, fmt.Errorf("open tcp tunnel answered %d: %s", resp.GetStatus(), string(resp.GetBody()))
		}
		streamID, err := decodeTunnelStreamID(resp.GetBody())
		if err != nil {
			_ = ls.Close()
			return nil, err
		}

		deadline := time.Now().Add(10 * time.Second)
		var stream conduit.Stream
		for time.Now().Before(deadline) {
			mu.Lock()
			stream = streams[streamID]
			mu.Unlock()
			if stream != nil {
				break
			}
			select {
			case <-ctx.Done():
				_ = ls.Close()
				return nil, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
		if stream == nil {
			_ = ls.Close()
			return nil, fmt.Errorf("tunnel stream %d did not arrive", streamID)
		}
		return func() {
			_ = stream.Close()
			_ = ls.Close()
		}, nil
	}
}

func makeLivePTYWorker(cfg *cliConfig, tlsCfg *tls.Config) func(context.Context, string) (*c19driver.PTYResult, error) {
	return func(ctx context.Context, agentID string) (*c19driver.PTYResult, error) {
		dialer := &websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
			TLSClientConfig:  tlsCfg,
		}
		u := httpToWSURL(cfg.HubURL) + "/api/v1/agents/" + agentID + "/pty?cols=80&rows=24"
		conn, _, err := dialer.DialContext(ctx, u, http.Header{"Authorization": {"Bearer " + cfg.UserToken}})
		if err != nil {
			return nil, err
		}
		defer func() { _ = conn.Close() }()

		pr := &c19driver.PTYResult{}
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		seq := 0
		for {
			select {
			case <-ctx.Done():
				return pr, nil
			case <-tick.C:
				payload := fmt.Sprintf("c19-pty-%d", seq)
				seq++
				start := time.Now()
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteJSON(wsprotocol.NewPTYDataMessage([]byte(payload))); err != nil {
					if ce, ok := asCloseError(err); ok {
						recordPTYClose(pr, ce)
						return pr, nil
					}
					if ctx.Err() != nil {
						return pr, nil
					}
					pr.EchoErrors++
					return pr, nil
				}
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				var got strings.Builder
				for !strings.Contains(got.String(), payload) {
					var msg wsprotocol.PTYDataMessage
					if err := conn.ReadJSON(&msg); err != nil {
						if ce, ok := asCloseError(err); ok {
							recordPTYClose(pr, ce)
							return pr, nil
						}
						if ctx.Err() != nil {
							return pr, nil
						}
						pr.EchoErrors++
						return pr, nil
					}
					if msg.Type == wsprotocol.TypeData {
						got.Write(msg.Data)
					}
				}
				pr.EchoSucceeded++
				pr.Latencies = append(pr.Latencies, time.Since(start))
			}
		}
	}
}

func asCloseError(err error) (*websocket.CloseError, bool) {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

func recordPTYClose(pr *c19driver.PTYResult, ce *websocket.CloseError) {
	pr.CloseCode = ce.Code
	pr.CloseReason = ce.Text
	pr.ClosedAt = time.Now()
	if ce.Code == wsprotocol.ClosePTYUpstreamUnavailable {
		pr.Drain4503 = true
	}
}

// makeMemorySampler returns a hub RSS sampler for --hub-rss-cmd or
// --hub-rss-pids. It never falls back to the driver's own RSS: when neither
// hub source is configured it returns nil so hub_rss_peak stays unevaluated.
// When --hub-rss-pids is set, every configured PID must be readable on each
// sample or the sample fails.
func makeMemorySampler(pids []int, cmdStr string) func(context.Context) (c19driver.MemorySample, error) {
	if cmdStr != "" {
		return func(ctx context.Context) (c19driver.MemorySample, error) {
			out, err := exec.CommandContext(ctx, "sh", "-c", cmdStr).Output()
			if err != nil {
				return c19driver.MemorySample{}, fmt.Errorf("hub-rss-cmd: %w", err)
			}
			val, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
			if err != nil || val <= 0 {
				return c19driver.MemorySample{}, fmt.Errorf("hub-rss-cmd invalid byte count %q", strings.TrimSpace(string(out)))
			}
			return c19driver.MemorySample{
				Timestamp: time.Now().UTC(),
				Bytes:     val,
				Source:    "cmd",
			}, nil
		}
	}
	if len(pids) > 0 {
		return func(_ context.Context) (c19driver.MemorySample, error) {
			var total int64
			for _, pid := range pids {
				b, err := readProcVmRSS(fmt.Sprintf("/proc/%d/status", pid))
				if err != nil {
					return c19driver.MemorySample{}, fmt.Errorf("read VmRSS for pid %d: %w", pid, err)
				}
				total += b
			}
			return c19driver.MemorySample{
				Timestamp: time.Now().UTC(),
				Bytes:     total,
				Source:    "proc_hub_pids",
				PIDCount:  len(pids),
			}, nil
		}
	}
	return nil
}

func readProcVmRSS(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	}
	return 0, errors.New("VmRSS not found")
}
