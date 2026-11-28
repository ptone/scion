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
	"github.com/GoogleCloudPlatform/scion/pkg/agentcontrol"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/clock"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/envelope"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/relay"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/transport/ws"
	"github.com/GoogleCloudPlatform/scion/pkg/wsprotocol"
	conduitv1 "github.com/GoogleCloudPlatform/scion/proto/conduit/v1"
)

// maxMessageChunkBytes matches messages.MaxMessageLength (16,000 chars) so
// each 512 KiB large delivery creates ceil(524288 / 16000) = 33 real message
// rows whose texts concatenate to the exact delivered body (v2.22 §1.6).
const maxMessageChunkBytes = 16000

const largeBodyRowsPerDelivery = (c19driver.LargeBodyBytes + maxMessageChunkBytes - 1) / maxMessageChunkBytes

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
	RotateTokenCount        int
	RotateURLTemplate       string
	BrokerRPCCount          int
	BrokerRPCConcurrency    int
	BrokerRPCPacing         time.Duration
	BrokerRPCLatency        time.Duration
	BrokerMaxConcurrentRPCs int
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
	fs.StringVar(&cfg.UserID, "user-id", getenv("C19_USER_ID"), "User principal ID for Conduit tunnel Hello (env: C19_USER_ID)")
	fs.StringVar(&cfg.BrokerID, "broker-id", getenv("C19_BROKER_ID"), "Runtime broker ID for Conduit broker session and /deliver (env: C19_BROKER_ID)")
	fs.StringVar(&brokerSecretStr, "broker-secret", getenv("C19_BROKER_SECRET"), "Raw broker HMAC secret (prefer env C19_BROKER_SECRET over CLI flag)")
	fs.StringVar(&brokerSecretB64, "broker-secret-b64", getenv("C19_BROKER_SECRET_B64"), "Base64-encoded broker HMAC secret (prefer env C19_BROKER_SECRET_B64)")
	fs.DurationVar(&cfg.Duration, "duration", 30*time.Second, "Active load duration")
	fs.DurationVar(&cfg.DeliverInterval, "deliver-interval", 2*time.Second, "Per-agent delivery cadence in deliver mode")
	fs.IntVar(&cfg.LargeBodyCount, "large-body-count", 10, "Number of 512 KiB deliveries to inject in deliver mode")
	fs.IntVar(&cfg.RotateTokenCount, "rotate-count", 0, "Number of rotate-token calls (requires --rotate-url-template)")
	fs.StringVar(&cfg.RotateURLTemplate, "rotate-url-template", getenv("C19_ROTATE_URL_TEMPLATE"), "Optional URL template with {agent_id} for POST rotate-token control RPCs")
	fs.IntVar(&cfg.BrokerRPCCount, "broker-rpc-count", 0, "Total broker RPCs (0 = continuous for --duration)")
	fs.IntVar(&cfg.BrokerRPCConcurrency, "rpc-concurrency", 128, "Concurrent broker-RPC workers (default 128 to exercise MaxConcurrentRPCs)")
	fs.DurationVar(&cfg.BrokerRPCPacing, "rpc-pacing", 0, "Optional per-worker delay between broker RPCs for smooth pacing")
	fs.DurationVar(&cfg.BrokerRPCLatency, "broker-rpc-latency", 0, "Simulated latency inside the fake broker RPC handler so in-flight RPCs overlap on the broker session")
	fs.IntVar(&cfg.BrokerMaxConcurrentRPCs, "broker-max-concurrent-rpcs", 0, "Override MaxConcurrentRPCs on the fake broker conduit session (0 = library default 128)")
	fs.IntVar(&cfg.ControlConcurrency, "control-concurrency", 8, "Concurrent control RPC workers")
	fs.IntVar(&cfg.BaselineControlSamples, "baseline-samples", 20, "Baseline control probes measured before load starts")
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
	fs.DurationVar(&cfg.DrainAfter, "drain-after", 0, "Optional delay into the load window before running --drain-cmd")
	fs.StringVar(&cfg.DrainCmd, "drain-cmd", "", "Optional shell command to trigger a planned hub drain")
	fs.BoolVar(&cfg.ExpectDrain, "expect-drain", false, "Require at least one 4503 relay_restart planned-drain close observation")
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

	// Refuse to start in L6 modes without hub RSS budget/source or planned-drain
	// config unless explicitly waived via --allow-unevaluated (R1).
	if !slices.Contains(cfg.AllowUnevaluated, c19driver.BarHubRSSPeak) {
		if cfg.MemoryBudgetBytes <= 0 || (len(cfg.HubRSSPIDs) == 0 && cfg.HubRSSCmd == "") {
			return nil, errors.New("hub RSS measurement requires --memory-budget-mb and (--hub-rss-pids or --hub-rss-cmd), or pass --allow-unevaluated=hub_rss_peak")
		}
	}
	if !slices.Contains(cfg.AllowUnevaluated, c19driver.BarPlannedDrain4503) {
		if !cfg.ExpectDrain && cfg.DrainCmd == "" {
			return nil, errors.New("planned drain verification requires --expect-drain (and optionally --drain-cmd), or pass --allow-unevaluated=planned_drain_4503")
		}
	}
	if cfg.DrainCmd != "" && !cfg.ExpectDrain {
		cfg.ExpectDrain = true
	}

	if cfg.UserID == "" {
		cfg.UserID = "c19-load-user"
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

	var (
		broker                 *conduittest.Broker
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
			case strings.HasSuffix(envReq.Path, "/message"):
				return envelope.ResponseToRPC(wsprotocol.NewResponseEnvelope(
					envReq.RequestID, http.StatusOK,
					map[string]string{"Content-Type": "application/json"},
					[]byte(`{"status":"ok"}`),
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
		broker, err = conduittest.StartBroker(conduittest.BrokerOptions{
			HubURL:          cfg.HubURL,
			BrokerID:        cfg.BrokerID,
			BrokerSecret:    cfg.BrokerSecret,
			RPCHandler:      rpcHandler,
			Session:         sessCfg,
			HTTPClient:      httpClient,
			TLSClientConfig: tlsCfg,
		})
		if err != nil {
			return c19driver.Report{}, fmt.Errorf("start fake broker: %w", err)
		}
		defer func() { _ = broker.Close() }()

		waitCtx, waitCancel := context.WithTimeout(ctx, 30*time.Second)
		err = broker.WaitHealthy(waitCtx)
		waitCancel()
		if err != nil {
			return c19driver.Report{}, fmt.Errorf("wait for broker conduit session: %w", err)
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
		EnablePTY:                cfg.Mode == c19driver.ModePhase2 || cfg.ExpectDrain,
		ClockSkew:                cfg.ClockSkew,
		DrainAfter:               cfg.DrainAfter,
		MemorySampler:            makeMemorySampler(cfg.HubRSSPIDs, cfg.HubRSSCmd),
	}

	if cfg.Mode == c19driver.ModeDeliver {
		driverCfg.Deliver = makeLiveDeliver(httpClient, cfg, broker, recordMsgIDs)
	}
	if broker != nil {
		driverCfg.BrokerRPC = makeLiveBrokerRPC(httpClient, cfg, targets)
	}
	if cfg.RotateURLTemplate != "" {
		driverCfg.RotateToken = makeLiveRotateToken(httpClient, cfg)
	}
	if cfg.Mode == c19driver.ModePhase2 {
		driverCfg.ControlProbe = makeLivePTYPreflight(httpClient, cfg)
	}
	if cfg.StalledTunnel {
		driverCfg.StalledTunnel = makeLiveStalledTunnel(cfg, tlsCfg)
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

	report := c19driver.BuildReport(res, c19driver.EvalOptions{
		MemoryBudgetBytes:        cfg.MemoryBudgetBytes,
		ExpectDrain:              cfg.ExpectDrain,
		AllowUnevaluated:         cfg.AllowUnevaluated,
		ClockSkew:                cfg.ClockSkew,
		HubURL:                   cfg.HubURL,
		RunTag:                   cfg.RunTag,
		MessageRowsCreated:       msgRowsCreated.Load(),
		LargeBodyRowsPerDelivery: largeBodyRowsPerDelivery,
		ExpectedLargeBodyCount:   cfg.LargeBodyCount,
	})

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

// splitMessageChunks partitions body into slices of at most maxChunk bytes so
// that strings.Join(chunks, "") == body and every chunk fits within
// messages.MaxMessageLength (16,000 chars).
func splitMessageChunks(body string, maxChunk int) []string {
	if maxChunk <= 0 || len(body) <= maxChunk {
		return []string{body}
	}
	chunks := make([]string, 0, (len(body)+maxChunk-1)/maxChunk)
	for off := 0; off < len(body); off += maxChunk {
		end := min(off+maxChunk, len(body))
		chunks = append(chunks, body[off:end])
	}
	return chunks
}

// makeLiveDeliver creates real message rows via POST /api/v1/agents/{id}/message
// (splitting 512 KiB bodies into ceil(512KiB/16000) = 33 rows whose texts
// concatenate to the delivered body, matching v2.22 §1.6), then sends one
// deliver-message flush binding all created message_ids via the broker's
// POST /api/v1/runtime-brokers/{brokerId}/agents/{agentId}/deliver endpoint.
func makeLiveDeliver(hc *http.Client, cfg *cliConfig, broker *conduittest.Broker, onIDs func([]string)) func(context.Context, string, string) (int, string, error) {
	return func(ctx context.Context, agentID, body string) (int, string, error) {
		chunks := splitMessageChunks(body, maxMessageChunkBytes)
		msgIDs := make([]string, 0, len(chunks))
		for _, chunk := range chunks {
			msgID, status, err := createHubMessageRow(ctx, hc, cfg.HubURL, cfg.UserToken, agentID, chunk)
			if err != nil {
				return status, "", err
			}
			msgIDs = append(msgIDs, msgID)
		}
		if onIDs != nil {
			onIDs(msgIDs)
		}

		text := body
		req := &agentcontrol.DeliverRequest{
			V:          1,
			DeliveryID: "c19-" + uuid.NewString(),
			MessageIDs: msgIDs,
			Op:         agentcontrol.OpPaste,
			Text:       &text,
			DeadlineMS: 92000,
			SubmitKeys: []string{"Enter"},
		}
		if cfg.ExtraSubmits > 0 {
			req.ExtraSubmits = &agentcontrol.ExtraSubmits{
				Count:   cfg.ExtraSubmits,
				DelayMS: int(cfg.ExtraSubmitDelay.Milliseconds()),
			}
		}
		dStatus, resp, err := broker.DeliverMessage(ctx, agentID, req)
		return dStatus, string(resp.Outcome), err
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

func makeLivePTYPreflight(hc *http.Client, cfg *cliConfig) func(context.Context, string) error {
	return func(ctx context.Context, agentID string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.HubURL+"/api/v1/agents/"+agentID+"/pty", nil)
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
		if resp.StatusCode != http.StatusOK {
			return &c19driver.HTTPStatusError{
				Status: resp.StatusCode,
				Err:    fmt.Errorf("GET /api/v1/agents/%s/pty answered %d", agentID, resp.StatusCode),
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

func makeLiveStalledTunnel(cfg *cliConfig, tlsCfg *tls.Config) func(context.Context, string) (func(), error) {
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
			PrincipalId:   cfg.UserID,
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
		var ok struct {
			StreamID uint32 `json:"stream_id"`
		}
		if err := json.Unmarshal(resp.GetBody(), &ok); err != nil {
			_ = ls.Close()
			return nil, fmt.Errorf("decode tunnel response: %w", err)
		}

		deadline := time.Now().Add(10 * time.Second)
		var stream conduit.Stream
		for time.Now().Before(deadline) {
			mu.Lock()
			stream = streams[ok.StreamID]
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
			return nil, fmt.Errorf("tunnel stream %d did not arrive", ok.StreamID)
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
