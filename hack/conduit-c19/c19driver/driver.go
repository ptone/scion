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

// Package c19driver implements the Criterion 19 Conduit load profile used by
// both the CI functional smoke test (pkg/hub/conduit_ha_c19_test.go) and the
// live load-driver CLI binary (hack/conduit-c19/main.go). This package is
// validation tooling and is not shipped in production binaries.
package c19driver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/scion/internal/conduittest"
	"github.com/GoogleCloudPlatform/scion/pkg/agentcontrol"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit"
	conduitv1 "github.com/GoogleCloudPlatform/scion/proto/conduit/v1"
)

// LargeBodyBytes is the 512 KiB payload size exercised by the C19 profile.
// LargeBodyMinBytes is the minimum agent-received text size classified as a
// 512 KiB delivery (accounting for Hub DeliveryText framing overhead when 33
// rows are coalesced within the 512 KiB MaxDeliverText cap).
const (
	LargeBodyBytes    = 512 * 1024
	LargeBodyMinBytes = LargeBodyBytes - 16*1024
)

// DefaultControlProbeRate is the default under-load control probe frequency (Hz).
const DefaultControlProbeRate = 4.0

// Mode selects which Criterion 19 traffic mix to execute.
type Mode string

const (
	// ModePhase2 runs Phase 2 traffic: PTY echo/keepalive + stalled bulk TCP
	// tunnel + control RPCs + planned drain observation.
	ModePhase2 Mode = "phase2"
	// ModeBrokerRPC runs Phase 3 broker-RPC traffic: high-concurrency hub->broker
	// RPC churn + stalled bulk TCP tunnel + control RPCs + planned drain.
	ModeBrokerRPC Mode = "broker-rpc"
	// ModeDeliver runs Phase 4 / v2.22 §6.2 deliver-message traffic: per-agent
	// deliveries + 512 KiB bodies + rotate-token + concurrent broker RPC churn +
	// stalled bulk TCP tunnel + planned drain.
	ModeDeliver Mode = "deliver"
)

// Valid reports whether m is a recognized C19 mode.
func (m Mode) Valid() bool {
	switch m {
	case ModePhase2, ModeBrokerRPC, ModeDeliver:
		return true
	default:
		return false
	}
}

// HTTPStatusError wraps an error with an HTTP status code so c19driver can
// distinguish 429 responses (including broker-session 429s surfaced by the hub
// as HTTP 502) from other failures across BrokerRPC/ControlProbe/Deliver.
type HTTPStatusError struct {
	Status     int
	Session429 bool
	Err        error
}

func (e *HTTPStatusError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("http %d: %v", e.Status, e.Err)
	}
	return fmt.Sprintf("http %d", e.Status)
}

func (e *HTTPStatusError) Unwrap() error { return e.Err }

func is429(err error) bool {
	var se *HTTPStatusError
	return errors.As(err, &se) && (se.Status == http.StatusTooManyRequests || se.Session429)
}

// AgentTarget identifies one fake agent to launch in the C19 load profile.
type AgentTarget struct {
	AgentID   string `json:"agent_id"`
	Slug      string `json:"slug,omitempty"`
	ProjectID string `json:"project_id"`
	LaunchID  string `json:"launch_id"`
	Token     string `json:"token"`
	HubURL    string `json:"hub_url,omitempty"`
}

// PTYResult summarizes one PTY worker's echo/keepalive loop and any observed
// planned-drain 4503 close frame.
type PTYResult struct {
	EchoSucceeded          int64
	EchoErrors             int64
	DrainWindowErrors      int64
	PostDrainEchoSucceeded int64
	Latencies              []time.Duration
	Drain4503              bool
	CloseCode              int
	CloseReason            string
	ClosedAt               time.Time
}

// MemorySample records one point-in-time RSS / memory observation.
type MemorySample struct {
	Timestamp            time.Time `json:"timestamp"`
	Bytes                int64     `json:"bytes"`
	Source               string    `json:"source"`
	PIDCount             int       `json:"pid_count,omitempty"`
	ExitedAfterDrainPIDs int       `json:"exited_after_drain_pids,omitempty"`
	ExitedAfterDrainPID  int       `json:"exited_after_drain_pid,omitempty"`
}

// Config configures a C19 load run.
type Config struct {
	// Mode selects the traffic mix (default ModeDeliver when empty).
	Mode Mode
	// RunTag is an optional unique identifier included in message payloads and
	// echoed in the report so operators can verify Postgres rows for this run.
	RunTag string
	// Agents lists the N fake agents to start concurrently (e.g. N=50).
	Agents []AgentTarget
	// DefaultHubURL is used for any AgentTarget whose HubURL is empty.
	DefaultHubURL string
	// HTTPClient and TLSClientConfig are forwarded to fake agents when set.
	HTTPClient      *http.Client
	TLSClientConfig *tls.Config
	// Duration is the active load phase length (default 5s).
	Duration time.Duration
	// DeliverInterval is the per-agent delivery cadence (default 2s).
	DeliverInterval time.Duration
	// LargeBodyCount is the number of 512 KiB deliver-message calls to inject
	// across the run (default 0 when unset).
	LargeBodyCount int
	// LargeBodyPacing explicitly sets the delay between 512 KiB deliveries.
	// When 0, large bodies are spaced evenly across Duration/(LargeBodyCount+1).
	// Set to a negative duration (e.g. -1) to send without inter-delivery sleep.
	LargeBodyPacing time.Duration
	// RotateTokenCount is the number of rotate-token control RPCs to inject
	// when RotateToken is non-nil.
	RotateTokenCount int
	// BrokerRPCCount is the total number of broker RPCs to execute when > 0.
	// When 0 and BrokerRPCContinuous is true (or Mode == ModeBrokerRPC),
	// workers run continuously for Duration.
	BrokerRPCCount int
	// BrokerRPCContinuous runs broker RPC workers for the full Duration even
	// when BrokerRPCCount is 0.
	BrokerRPCContinuous bool
	// BrokerRPCConcurrency is the number of concurrent goroutines issuing
	// BrokerRPC calls (default 1). Set to 128 to exercise MaxConcurrentRPCs.
	BrokerRPCConcurrency int
	// BrokerRPCPacing optionally sleeps for this duration between successive
	// BrokerRPC calls within each worker (0 = no sleep).
	BrokerRPCPacing time.Duration
	// BrokerMaxConcurrentRPCs records the effective MaxConcurrentRPCs on the
	// broker conduit session (0 = default 128) for the broker_session_concurrency bar.
	BrokerMaxConcurrentRPCs int
	// ControlConcurrency is the number of concurrent workers for RotateToken
	// during load (default 1).
	ControlConcurrency int
	// BaselineControlSamples is the number of control probes measured before
	// load starts to establish the baseline p99 for control_p99_added_ms.
	BaselineControlSamples int
	// BaselineWarmupSamples is the number of warmup control probes discarded
	// before recording baseline samples (default 2 when BaselineControlSamples > 0).
	BaselineWarmupSamples int
	// ControlProbeRate is the fixed probe rate (probes/sec) for under-load
	// control latency sampling (default DefaultControlProbeRate = 4.0/s).
	ControlProbeRate float64
	// EnableBulkTunnelOnAgent0 installs an in-memory bulk TCP producer on
	// Agents[0]'s DialLocal so StalledTunnel fills the stream receive window.
	EnableBulkTunnelOnAgent0 bool
	// EnablePTY installs a FakePTYSpawner on every agent (enabled automatically
	// in ModePhase2, when PTYWorker != nil, or when ControlProbe != nil).
	EnablePTY bool
	// PTYCount is the number of concurrent PTY workers to spawn when PTYWorker
	// is non-nil (default 1).
	PTYCount int
	// ClockSkew is subtracted from raw (receipt - hub_send) send-to-receipt
	// delays to compute skew-corrected send-to-receipt delay.
	ClockSkew time.Duration
	// DrainAfter triggers OnDrainTrigger after this offset into Duration.
	DrainAfter time.Duration
	// DrainDeadline defines the drain window [DrainTriggeredAt, DrainTriggeredAt+DrainDeadline]
	// during which control-probe and PTY errors on sessions owned by the drained
	// hub (DrainedRelayInstanceID) are excused and reported separately (default 30s).
	DrainDeadline time.Duration
	// DrainedRelayInstanceID identifies the hub/relay instance being drained
	// (from Welcome.relay_instance_id). Drain-window excusal for control-probe
	// and PTY errors applies ONLY to agents owned by this relay; sessions on
	// surviving hubs receive no excusal.
	DrainedRelayInstanceID string
	// MemorySampleInterval controls how often MemorySampler is called during
	// the load window (default 1s when MemorySampler != nil).
	MemorySampleInterval time.Duration

	// Deliver sends one deliver-message flush to agentID with body and returns
	// the HTTP status code, outcome string, and any error. Required in ModeDeliver.
	Deliver func(ctx context.Context, agentID, body string) (status int, outcome string, err error)
	// FlusherAttemptStats optionally returns total HTTP 429 and 503 counts
	// observed across all /deliver attempts (including retried attempts) inside
	// the broker flusher so retried 429s are never hidden from http_429_count.
	FlusherAttemptStats func() (deliver429s, deliver503s int64)
	// OnLoadComplete, when non-nil, is called after the load loops finish and
	// before fake agents are closed (e.g. to drain buffered broker flushes).
	OnLoadComplete func(ctx context.Context) error
	// RotateToken invokes POST /v1/control/rotate-token on agentID. Optional.
	RotateToken func(ctx context.Context, agentID string) error
	// BrokerRPC invokes one hub->broker list/status/logs RPC. Optional.
	BrokerRPC func(ctx context.Context, seq int) error
	// BrokerSessionHighWater optionally returns the broker-side inbound
	// conduit RPC in-flight high-water mark observed inside the broker handler.
	BrokerSessionHighWater func() int64
	// ControlProbe invokes one lightweight control-plane probe crossing the
	// agent's conduit session (off the broker-RPC churn path) for baseline vs
	// under-load p99 measurement. Never falls back to RotateToken or BrokerRPC.
	ControlProbe func(ctx context.Context, agentID string) error
	// StalledTunnel opens a bulk TCP tunnel stream to Agents[0] and leaves its
	// reader completely stalled until the returned close function is called.
	// Optional.
	StalledTunnel func(ctx context.Context, agentID string) (closeFn func(), err error)
	// PTYWorker runs an interactive PTY echo/keepalive session against agentID
	// until ctx is cancelled or the server closes the stream (e.g. 4503 drain).
	PTYWorker func(ctx context.Context, agentID string) (*PTYResult, error)
	// MemorySampler samples hub/aggregate RSS memory. Optional.
	MemorySampler func(ctx context.Context) (MemorySample, error)
	// OnDrainTrigger is invoked once at DrainAfter into the load window to
	// initiate a planned hub drain (e.g. SIGINT to hub-b). Optional.
	OnDrainTrigger func(ctx context.Context) error
	// DrainAcceptedStream, when set alongside OnDrainTrigger, is started just
	// before the drain trigger to execute a bounded exchange that must complete
	// cleanly within the drain window ("accepted streams finish within drain
	// deadline"). Optional.
	DrainAcceptedStream func(ctx context.Context, agentID string) error
}

// LatencyStats summarizes a slice of recorded durations.
type LatencyStats struct {
	Count int           `json:"count"`
	P50   time.Duration `json:"p50"`
	P99   time.Duration `json:"p99"`
	Max   time.Duration `json:"max"`
}

// SummarizeLatencies computes count, nearest-rank p50, nearest-rank p99, and
// max over samples. Nearest-rank uses idx = ceil(p*n) - 1, so p99 equals the
// maximum for n <= 100 and never drops the worst sample at small n.
func SummarizeLatencies(samples []time.Duration) LatencyStats {
	if len(samples) == 0 {
		return LatencyStats{}
	}
	cp := append([]time.Duration(nil), samples...)
	slices.Sort(cp)
	n := len(cp)
	pct := func(p float64) time.Duration {
		idx := int(math.Ceil(p*float64(n))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= n {
			idx = n - 1
		}
		return cp[idx]
	}
	return LatencyStats{
		Count: n,
		P50:   pct(0.50),
		P99:   pct(0.99),
		Max:   cp[n-1],
	}
}

// SkewCorrectedDelay computes the skew-corrected send-to-receipt delay from the
// hub-stamped NotAfterUnixMS, the agent receipt timestamp, and known clockSkew
// (agentClock - hubClock). Returns raw delay, non-negative skew-corrected
// delay, whether a negative result was clamped to zero, and ok (false when
// notAfterUnixMS <= 0).
func SkewCorrectedDelay(notAfterUnixMS int64, receiptAt time.Time, clockSkew time.Duration) (raw, corrected time.Duration, clamped, ok bool) {
	if notAfterUnixMS <= 0 {
		return 0, 0, false, false
	}
	hubSendAt := time.UnixMilli(notAfterUnixMS).Add(-agentcontrol.DeliverNotAfter)
	raw = receiptAt.Sub(hubSendAt)
	corrected = raw - clockSkew
	if corrected < 0 {
		corrected = 0
		clamped = true
	}
	return raw, corrected, clamped, true
}

// Result holds counters, errors, and latencies from a C19 load run.
type Result struct {
	Mode                           Mode
	RunTag                         string
	Duration                       time.Duration
	DeliverInterval                time.Duration
	AgentCount                     int
	ExpectedLargeBodyCount         int
	ExpectedRotateTokenCount       int
	ExpectedBrokerRPCConcurrency   int
	EffectiveMaxConcurrentRPCs     int
	BrokerRPCEnabled               bool
	DeliverAttempts                int64
	DeliverPeriodicAttempts        int64
	DeliverSucceeded               int64
	DeliverNonDelivered            int64
	Deliver429s                    int64
	FlusherDeliver429s             int64
	FlusherDeliver503s             int64
	DeliverErrors                  int64
	LargeBodySucceeded             int64
	LargeBodyErrors                int64
	LargeBodyMinBytesObserved      int
	LargeBodyMaxBytesObserved      int
	RotateTokenSucceeded           int64
	RotateTokenErrors              int64
	BrokerRPCSucceeded             int64
	BrokerRPCErrors                int64
	DrainExcusedBrokerRPCErrors    int64
	BrokerRPC429s                  int64
	BaselineControlErrors          int64
	ControlProbeSucceeded          int64
	ControlProbeErrors             int64
	DrainExcusedControlErrors      int64
	Control429s                    int64
	Total429s                      int64
	MaxConcurrentBrokerRPCs        int64
	MaxConcurrentBrokerSessionRPCs int64
	MaxConcurrentControls          int64
	PTYEchoSucceeded               int64
	PTYEchoErrors                  int64
	DrainExcusedPTYErrors          int64
	DrainTriggeredAt               time.Time
	Drain4503Count                 int64
	Drain4503RelayRestartCount     int64
	Drain4503OtherCount            int64
	Drain4503Reasons               []string
	Drain4503OtherReasons          []string
	Drain4503Delays                []time.Duration
	DrainAcceptedStreamAttempted   bool
	DrainAcceptedStreamFinished    bool
	PostDrainWorkSucceeded         int64
	PostDrainReplacementWork       int64
	SessionReconnections           int64
	StalledBulkBytesAtStart        int64
	StalledBulkBytesWritten        int64
	SendToReceiptClamped           int64
	PeakMemoryBytes                int64
	MemorySource                   string
	MemoryPIDCount                 int
	MemoryExitedAfterDrainPIDs     int
	MemoryExitedAfterDrainPID      int
	MemorySampleErrors             int64
	MemorySamples                  []MemorySample

	DeliverLatencies         []time.Duration
	DeliverPasteLatencies    []time.Duration
	LargePasteLatencies      []time.Duration
	SendToReceiptDelays      []time.Duration
	RawSendToReceiptDelays   []time.Duration
	CombinedDeliverLatencies []time.Duration
	RotateLatencies          []time.Duration
	BrokerRPCLatencies       []time.Duration
	BaselineControlLatencies []time.Duration
	LoadControlLatencies     []time.Duration
	PTYLatencies             []time.Duration
	FirstError               error
}

// ShouldExcuseDrainError reports whether a control-probe or PTY error during a
// planned drain may be excused: ONLY when inside the drain window AND the
// agent's session is owned by the explicitly designated drained relay. Agents
// on surviving hubs receive zero excusal.
func ShouldExcuseDrainError(inDrainWindow bool, agentRelayID, drainedRelayID string) bool {
	return inDrainWindow && drainedRelayID != "" && agentRelayID == drainedRelayID
}

func updateHighWater(high *atomic.Int64, cur int64) {
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

// Run starts all configured fake agents, optionally establishes a baseline
// control latency, opens a stalled bulk TCP tunnel on agent 0, executes the
// configured C19 workload for cfg.Duration, tears everything down cleanly, and
// returns the aggregated Result.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if len(cfg.Agents) == 0 {
		return nil, errors.New("c19driver: at least one agent target is required")
	}
	mode := cfg.Mode
	if mode == "" {
		mode = ModeDeliver
	}
	if !mode.Valid() {
		return nil, fmt.Errorf("c19driver: invalid mode %q", mode)
	}
	if mode == ModeDeliver && cfg.Deliver == nil {
		return nil, errors.New("c19driver: Deliver callback is required in deliver mode")
	}
	if mode == ModeBrokerRPC && cfg.BrokerRPC == nil {
		return nil, errors.New("c19driver: BrokerRPC callback is required in broker-rpc mode")
	}
	if cfg.Duration <= 0 {
		cfg.Duration = 5 * time.Second
	}
	if cfg.DeliverInterval <= 0 {
		cfg.DeliverInterval = 2 * time.Second
	}
	if cfg.LargeBodyCount < 0 {
		cfg.LargeBodyCount = 0
	}
	if cfg.RotateTokenCount < 0 {
		cfg.RotateTokenCount = 0
	}
	if cfg.BrokerRPCCount < 0 {
		cfg.BrokerRPCCount = 0
	}
	if cfg.BrokerRPCConcurrency <= 0 {
		cfg.BrokerRPCConcurrency = 1
	}
	if cfg.ControlConcurrency <= 0 {
		cfg.ControlConcurrency = 1
	}
	if cfg.ControlProbeRate <= 0 {
		cfg.ControlProbeRate = DefaultControlProbeRate
	}
	if cfg.PTYCount <= 0 && cfg.PTYWorker != nil {
		cfg.PTYCount = 1
	}
	if cfg.MemorySampleInterval <= 0 {
		cfg.MemorySampleInterval = time.Second
	}
	warmupSamples := cfg.BaselineWarmupSamples
	if warmupSamples == 0 && cfg.BaselineControlSamples > 0 {
		warmupSamples = 2
	}

	enablePTY := cfg.EnablePTY || mode == ModePhase2 || cfg.PTYWorker != nil || cfg.ControlProbe != nil
	runBrokerRPC := cfg.BrokerRPC != nil && (cfg.BrokerRPCCount > 0 || cfg.BrokerRPCContinuous || mode == ModeBrokerRPC)
	drainWindow := cfg.DrainDeadline
	if drainWindow <= 0 {
		drainWindow = 30 * time.Second
	}
	effMaxConc := cfg.BrokerMaxConcurrentRPCs
	if effMaxConc <= 0 {
		effMaxConc = conduit.DefaultMaxConcurrentRPCs
	}

	var (
		mu                       sync.Mutex
		bulkWritten              atomic.Int64
		reconnections            atomic.Int64
		clampedDelays            atomic.Int64
		memSampleErrs            atomic.Int64
		drainTriggered           atomic.Int64 // unix nanos when OnDrainTrigger fired (0 if not triggered)
		postDrainWork            atomic.Int64
		postDrainReplacementWork atomic.Int64
		baselineControlErr       atomic.Int64
		drainExcusedControlErr   atomic.Int64
		drainExcusedPTYErr       atomic.Int64
		admittedCounts           = make([]atomic.Int64, len(cfg.Agents))
		reAdmittedAfterDrain     = make([]atomic.Bool, len(cfg.Agents))
		agentRelayIDs            = make([]string, len(cfg.Agents))
		res                      = &Result{
			Mode:                         mode,
			RunTag:                       cfg.RunTag,
			Duration:                     cfg.Duration,
			DeliverInterval:              cfg.DeliverInterval,
			ExpectedLargeBodyCount:       cfg.LargeBodyCount,
			ExpectedRotateTokenCount:     cfg.RotateTokenCount,
			ExpectedBrokerRPCConcurrency: cfg.BrokerRPCConcurrency,
			EffectiveMaxConcurrentRPCs:   effMaxConc,
			BrokerRPCEnabled:             runBrokerRPC,
		}
	)

	inDrainWindow := func() bool {
		trig := drainTriggered.Load()
		if trig <= 0 {
			return false
		}
		return time.Since(time.Unix(0, trig)) <= drainWindow
	}

	isDrainedAgent := func(agentIdx int) bool {
		if cfg.DrainedRelayInstanceID == "" || agentIdx < 0 || agentIdx >= len(agentRelayIDs) {
			return false
		}
		mu.Lock()
		rid := agentRelayIDs[agentIdx]
		mu.Unlock()
		return ShouldExcuseDrainError(inDrainWindow(), rid, cfg.DrainedRelayInstanceID)
	}

	recordPostDrainForAgent := func(agentIdx int) {
		if drainTriggered.Load() <= 0 {
			return
		}
		postDrainWork.Add(1)
		if agentIdx >= 0 && agentIdx < len(reAdmittedAfterDrain) && reAdmittedAfterDrain[agentIdx].Load() {
			postDrainReplacementWork.Add(1)
		}
	}

	agentOpts := make([]conduittest.AgentOptions, len(cfg.Agents))
	for i, target := range cfg.Agents {
		hubURL := target.HubURL
		if hubURL == "" {
			hubURL = cfg.DefaultHubURL
		}
		tok := target.Token
		agentIdx := i
		opt := conduittest.AgentOptions{
			HubURL:          hubURL,
			AgentID:         target.AgentID,
			ProjectID:       target.ProjectID,
			LaunchID:        target.LaunchID,
			Token:           func() string { return tok },
			HTTPClient:      cfg.HTTPClient,
			TLSClientConfig: cfg.TLSClientConfig,
			EnablePTY:       enablePTY,
			OnSession: func(w *conduitv1.Welcome) {
				if w != nil && w.GetRelayInstanceId() != "" {
					mu.Lock()
					agentRelayIDs[agentIdx] = w.GetRelayInstanceId()
					mu.Unlock()
				}
				if admittedCounts[agentIdx].Add(1) > 1 {
					reconnections.Add(1)
					if drainTriggered.Load() > 0 {
						reAdmittedAfterDrain[agentIdx].Store(true)
					}
				}
			},
			OnDeliverTiming: func(dt conduittest.DeliverTiming) {
				if dt.Status != http.StatusOK {
					return
				}
				raw, corrected, clamped, hasDelay := SkewCorrectedDelay(dt.NotAfterUnixMS, dt.ReceiptAt, cfg.ClockSkew)
				if clamped {
					clampedDelays.Add(1)
				}
				mu.Lock()
				res.DeliverPasteLatencies = append(res.DeliverPasteLatencies, dt.PasteDuration)
				if dt.TextBytes >= LargeBodyMinBytes {
					res.LargePasteLatencies = append(res.LargePasteLatencies, dt.PasteDuration)
					if res.LargeBodyMinBytesObserved == 0 || dt.TextBytes < res.LargeBodyMinBytesObserved {
						res.LargeBodyMinBytesObserved = dt.TextBytes
					}
					if dt.TextBytes > res.LargeBodyMaxBytesObserved {
						res.LargeBodyMaxBytesObserved = dt.TextBytes
					}
				}
				if hasDelay {
					res.RawSendToReceiptDelays = append(res.RawSendToReceiptDelays, raw)
					res.SendToReceiptDelays = append(res.SendToReceiptDelays, corrected)
					res.CombinedDeliverLatencies = append(res.CombinedDeliverLatencies, corrected+dt.PasteDuration)
				}
				mu.Unlock()
			},
		}
		if i == 0 && cfg.EnableBulkTunnelOnAgent0 {
			opt.DialLocal = func(_ context.Context, _, _ string) (net.Conn, error) {
				serverConn, clientConn := net.Pipe()
				go func() {
					defer func() { _ = serverConn.Close() }()
					chunk := make([]byte, 16*1024)
					for j := range chunk {
						chunk[j] = byte(j)
					}
					for {
						n, err := serverConn.Write(chunk)
						if n > 0 {
							bulkWritten.Add(int64(n))
						}
						if err != nil {
							return
						}
					}
				}()
				return clientConn, nil
			}
		}
		agentOpts[i] = opt
	}

	admitCtx, admitCancel := context.WithTimeout(ctx, 45*time.Second)
	agents, err := conduittest.StartAgents(admitCtx, agentOpts)
	admitCancel()
	if err != nil {
		return nil, fmt.Errorf("c19driver: start agents: %w", err)
	}
	defer func() {
		var closeWg sync.WaitGroup
		for _, a := range agents {
			closeWg.Add(1)
			go func(ag *conduittest.Agent) {
				defer closeWg.Done()
				_ = ag.Close()
			}(a)
		}
		closeWg.Wait()
	}()
	res.AgentCount = len(agents)

	// Baseline control-plane latency phase (before stalled tunnel and load).
	// Uses only cfg.ControlProbe (never RotateToken or BrokerRPC) so baseline
	// measures the agent's conduit session without side effects or churn overlap.
	// Warmup probes discard initial TCP/TLS setup latency so baseline p99 is not
	// biased high.
	if cfg.ControlProbe != nil && cfg.BaselineControlSamples > 0 {
		probeAgentID := cfg.Agents[0].AgentID
		for i := 0; i < warmupSamples; i++ {
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = cfg.ControlProbe(callCtx, probeAgentID)
			cancel()
		}
		for i := 0; i < cfg.BaselineControlSamples; i++ {
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			start := time.Now()
			err := cfg.ControlProbe(callCtx, probeAgentID)
			elapsed := time.Since(start)
			cancel()
			if err != nil {
				baselineControlErr.Add(1)
				mu.Lock()
				if res.FirstError == nil {
					res.FirstError = fmt.Errorf("baseline control probe agent %s: %w", probeAgentID, err)
				}
				mu.Unlock()
				continue
			}
			res.BaselineControlLatencies = append(res.BaselineControlLatencies, elapsed)
		}
	}

	recordErr := func(e error) {
		if e == nil {
			return
		}
		mu.Lock()
		if res.FirstError == nil {
			res.FirstError = e
		}
		mu.Unlock()
	}

	// Take initial memory sample if configured.
	sampleMemory := func() {
		if cfg.MemorySampler == nil {
			return
		}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		sample, err := cfg.MemorySampler(sctx)
		cancel()
		if err != nil {
			memSampleErrs.Add(1)
			recordErr(fmt.Errorf("memory sample: %w", err))
			return
		}
		if sample.Bytes <= 0 {
			memSampleErrs.Add(1)
			recordErr(errors.New("memory sample: returned non-positive bytes"))
			return
		}
		if sample.Timestamp.IsZero() {
			sample.Timestamp = time.Now().UTC()
		}
		mu.Lock()
		res.MemorySamples = append(res.MemorySamples, sample)
		if sample.Source != "" {
			res.MemorySource = sample.Source
		}
		if sample.PIDCount > 0 {
			res.MemoryPIDCount = sample.PIDCount
		}
		if sample.ExitedAfterDrainPIDs > res.MemoryExitedAfterDrainPIDs {
			res.MemoryExitedAfterDrainPIDs = sample.ExitedAfterDrainPIDs
		}
		if sample.ExitedAfterDrainPID > 0 {
			res.MemoryExitedAfterDrainPID = sample.ExitedAfterDrainPID
		}
		if sample.Bytes > res.PeakMemoryBytes {
			res.PeakMemoryBytes = sample.Bytes
		}
		mu.Unlock()
	}
	sampleMemory()

	// Open the stalled bulk tunnel stream before the load window starts so its
	// receive window fills and backpressures before any workload begins.
	var stalledAtStart int64
	if cfg.StalledTunnel != nil {
		closeTunnel, err := cfg.StalledTunnel(ctx, cfg.Agents[0].AgentID)
		if err != nil {
			return nil, fmt.Errorf("c19driver: open stalled tunnel: %w", err)
		}
		defer closeTunnel()

		if cfg.EnableBulkTunnelOnAgent0 {
			deadline := time.Now().Add(10 * time.Second)
			for bulkWritten.Load() < conduit.DefaultStreamWindow {
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("c19driver: stalled tunnel only wrote %d bytes (< %d)", bulkWritten.Load(), conduit.DefaultStreamWindow)
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(15 * time.Millisecond):
				}
			}
			// Wait until the bulk producer is actually backpressured (5
			// consecutive 100ms quiet windows = 500ms with zero byte progress)
			// so staged window fills under CPU load do not trigger mid-fill.
			stallDeadline := time.Now().Add(20 * time.Second)
			const requiredQuietWindows = 5
			quietCount := 0
			lastBytes := bulkWritten.Load()
			for {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
				nowBytes := bulkWritten.Load()
				if nowBytes == lastBytes {
					quietCount++
					if quietCount >= requiredQuietWindows {
						stalledAtStart = nowBytes
						break
					}
				} else {
					quietCount = 0
					lastBytes = nowBytes
				}
				if time.Now().After(stallDeadline) {
					stalledAtStart = nowBytes
					break
				}
			}
		}
	}
	res.StalledBulkBytesAtStart = stalledAtStart

	var (
		deliverAttempt           atomic.Int64
		deliverPeriodicAttempt   atomic.Int64
		deliverOK                atomic.Int64
		deliverNonDelivered      atomic.Int64
		deliver429               atomic.Int64
		deliverErr               atomic.Int64
		largeOK                  atomic.Int64
		largeErr                 atomic.Int64
		rotateOK                 atomic.Int64
		rotateErr                atomic.Int64
		brokerOK                 atomic.Int64
		brokerErr                atomic.Int64
		drainExcusedBrokerRPCErr atomic.Int64
		broker429                atomic.Int64
		brokerInFlight           atomic.Int64
		brokerHighWater          atomic.Int64
		controlOK                atomic.Int64
		controlErr               atomic.Int64
		control429               atomic.Int64
		controlInFlight          atomic.Int64
		controlHighWater         atomic.Int64
		ptyOK                    atomic.Int64
		ptyErr                   atomic.Int64
		drain4503                atomic.Int64
		drainRelayRestart        atomic.Int64
		drainOther               atomic.Int64
		drainAcceptedAttempted   atomic.Bool
		drainAcceptedFinished    atomic.Bool
	)

	loadStart := time.Now()
	loadDeadline := loadStart.Add(cfg.Duration)
	loadCtx, loadCancel := context.WithDeadline(ctx, loadDeadline)
	defer loadCancel()
	atLoadEnd := func() bool {
		return loadCtx.Err() != nil || !time.Now().Before(loadDeadline.Add(-5*time.Millisecond))
	}

	var wg sync.WaitGroup

	// 0a. Periodic memory sampling during load.
	if cfg.MemorySampler != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(cfg.MemorySampleInterval)
			defer tick.Stop()
			for {
				select {
				case <-loadCtx.Done():
					return
				case <-tick.C:
					sampleMemory()
				}
			}
		}()
	}

	// 0b. Optional planned drain trigger at DrainAfter.
	if cfg.OnDrainTrigger != nil && cfg.DrainAfter > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-loadCtx.Done():
				return
			case <-time.After(cfg.DrainAfter):
			}
			// If a bounded accepted-stream check is configured, start it just
			// before firing the drain trigger so it is already accepted on the
			// draining session and must complete cleanly during the drain window.
			if cfg.DrainAcceptedStream != nil {
				streamAgent := cfg.Agents[0].AgentID
				if len(cfg.Agents) > 1 {
					streamAgent = cfg.Agents[1].AgentID
				}
				drainAcceptedAttempted.Store(true)
				wg.Add(1)
				go func() {
					defer wg.Done()
					sctx, scancel := context.WithTimeout(ctx, drainWindow+5*time.Second)
					defer scancel()
					if err := cfg.DrainAcceptedStream(sctx, streamAgent); err == nil {
						drainAcceptedFinished.Store(true)
					} else {
						recordErr(fmt.Errorf("drain accepted stream: %w", err))
					}
				}()
				// Brief pause so the stream open lands before the drain trigger.
				select {
				case <-loadCtx.Done():
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
			now := time.Now()
			drainTriggered.Store(now.UnixNano())
			mu.Lock()
			res.DrainTriggeredAt = now
			mu.Unlock()
			if err := cfg.OnDrainTrigger(loadCtx); err != nil {
				recordErr(fmt.Errorf("drain trigger: %w", err))
			}
		}()
	}

	// 1. Per-agent periodic delivery loops (ModeDeliver).
	// Serialize per-agent deliver calls with a per-agent mutex so periodic and
	// 512 KiB deliveries to the same agent do not overlap.
	agentDeliverMu := make([]sync.Mutex, len(cfg.Agents))
	if mode == ModeDeliver && cfg.Deliver != nil {
		msgPrefix := "c19"
		if cfg.RunTag != "" {
			msgPrefix = cfg.RunTag
		}
		for i, target := range cfg.Agents {
			wg.Add(1)
			go func(idx int, agentID string) {
				defer wg.Done()
				stagger := time.Duration(idx) * cfg.DeliverInterval / time.Duration(len(cfg.Agents))
				if stagger > 0 {
					select {
					case <-loadCtx.Done():
						return
					case <-time.After(stagger):
					}
				}
				seq := 0
				sendOne := func() {
					agentDeliverMu[idx].Lock()
					defer agentDeliverMu[idx].Unlock()
					callCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
					defer cancel()
					start := time.Now()
					deliverAttempt.Add(1)
					deliverPeriodicAttempt.Add(1)
					status, outcome, err := cfg.Deliver(callCtx, agentID, fmt.Sprintf("%s-agent-%d-msg-%d", msgPrefix, idx, seq))
					elapsed := time.Since(start)
					seq++
					// When FlusherAttemptStats is wired, /deliver 429s are counted
					// once in FlusherDeliver429s; only count /message 429s (HTTPStatusError)
					// here to avoid double-counting.
					if is429(err) || (cfg.FlusherAttemptStats == nil && status == http.StatusTooManyRequests) {
						deliver429.Add(1)
					}
					if err != nil || status != http.StatusOK {
						deliverErr.Add(1)
						if err != nil {
							recordErr(fmt.Errorf("deliver agent %s: %w", agentID, err))
						} else {
							recordErr(fmt.Errorf("deliver agent %s: status %d", agentID, status))
						}
						return
					}
					if outcome != "delivered" {
						deliverNonDelivered.Add(1)
						recordErr(fmt.Errorf("deliver agent %s: outcome %q, want delivered", agentID, outcome))
						return
					}
					deliverOK.Add(1)
					recordPostDrainForAgent(idx)
					mu.Lock()
					res.DeliverLatencies = append(res.DeliverLatencies, elapsed)
					mu.Unlock()
				}
				sendOne()
				tick := time.NewTicker(cfg.DeliverInterval)
				defer tick.Stop()
				for {
					select {
					case <-loadCtx.Done():
						return
					case <-tick.C:
						sendOne()
					}
				}
			}(i, target.AgentID)
		}

		// 2. Large (512 KiB) deliver-message bodies scheduled at absolute
		//    offsets from loadStart so delivery latency does not drift the
		//    schedule past Duration.
		if cfg.LargeBodyCount > 0 {
			largePayload := strings.Repeat("X", LargeBodyBytes)
			pacing := cfg.LargeBodyPacing
			if pacing == 0 && cfg.LargeBodyCount > 1 && cfg.Duration > 0 {
				pacing = cfg.Duration / time.Duration(cfg.LargeBodyCount+1)
			}
			if pacing < 0 {
				pacing = 0
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < cfg.LargeBodyCount; i++ {
					if i > 0 && pacing > 0 {
						targetTime := loadStart.Add(time.Duration(i) * pacing)
						if wait := time.Until(targetTime); wait > 0 {
							select {
							case <-loadCtx.Done():
								return
							case <-time.After(wait):
							}
						}
					}
					if loadCtx.Err() != nil {
						return
					}
					targetIdx := i % len(cfg.Agents)
					target := cfg.Agents[targetIdx]
					agentDeliverMu[targetIdx].Lock()
					callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					start := time.Now()
					deliverAttempt.Add(1)
					status, outcome, err := cfg.Deliver(callCtx, target.AgentID, largePayload)
					elapsed := time.Since(start)
					cancel()
					agentDeliverMu[targetIdx].Unlock()
					if is429(err) || (cfg.FlusherAttemptStats == nil && status == http.StatusTooManyRequests) {
						deliver429.Add(1)
					}
					if err != nil || status != http.StatusOK {
						largeErr.Add(1)
						deliverErr.Add(1)
						if err != nil {
							recordErr(fmt.Errorf("large deliver agent %s: %w", target.AgentID, err))
						} else {
							recordErr(fmt.Errorf("large deliver agent %s: status %d", target.AgentID, status))
						}
						continue
					}
					if outcome != "delivered" {
						largeErr.Add(1)
						deliverNonDelivered.Add(1)
						recordErr(fmt.Errorf("large deliver agent %s: outcome %q, want delivered", target.AgentID, outcome))
						continue
					}
					largeOK.Add(1)
					deliverOK.Add(1)
					recordPostDrainForAgent(targetIdx)
					mu.Lock()
					res.DeliverLatencies = append(res.DeliverLatencies, elapsed)
					mu.Unlock()
				}
			}()
		}
	}

	// 3. Control RPCs (rotate-token) mixed across agents, starting with agent 0
	//    (which shares its session with the stalled bulk tunnel).
	if cfg.RotateToken != nil && cfg.RotateTokenCount > 0 {
		var rotateSeq atomic.Int64
		workers := min(cfg.ControlConcurrency, cfg.RotateTokenCount)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					i := int(rotateSeq.Add(1) - 1)
					if i >= cfg.RotateTokenCount {
						return
					}
					target := cfg.Agents[i%len(cfg.Agents)]
					cur := controlInFlight.Add(1)
					updateHighWater(&controlHighWater, cur)
					callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					start := time.Now()
					err := cfg.RotateToken(callCtx, target.AgentID)
					elapsed := time.Since(start)
					cancel()
					controlInFlight.Add(-1)
					if is429(err) {
						control429.Add(1)
					}
					if err != nil {
						rotateErr.Add(1)
						recordErr(fmt.Errorf("rotate-token agent %s: %w", target.AgentID, err))
						continue
					}
					rotateOK.Add(1)
					mu.Lock()
					res.RotateLatencies = append(res.RotateLatencies, elapsed)
					mu.Unlock()
				}
			}()
		}
	}

	// 3b. Fixed-rate under-load control probe loop on agent 0 (shares its
	//     conduit session with the stalled tunnel, off the broker-RPC churn path).
	if cfg.ControlProbe != nil && cfg.BaselineControlSamples > 0 {
		probeAgentID := cfg.Agents[0].AgentID
		probeInterval := time.Duration(float64(time.Second) / cfg.ControlProbeRate)
		if probeInterval < 10*time.Millisecond {
			probeInterval = 10 * time.Millisecond
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(probeInterval)
			defer tick.Stop()
			for {
				select {
				case <-loadCtx.Done():
					return
				case <-tick.C:
					cur := controlInFlight.Add(1)
					updateHighWater(&controlHighWater, cur)
					callCtx, cancel := context.WithTimeout(loadCtx, 10*time.Second)
					start := time.Now()
					err := cfg.ControlProbe(callCtx, probeAgentID)
					elapsed := time.Since(start)
					cancel()
					controlInFlight.Add(-1)
					if is429(err) {
						control429.Add(1)
					}
					if err != nil {
						if atLoadEnd() {
							return
						}
						if isDrainedAgent(0) {
							drainExcusedControlErr.Add(1)
						} else {
							controlErr.Add(1)
							recordErr(fmt.Errorf("control probe agent %s: %w", probeAgentID, err))
						}
						continue
					}
					controlOK.Add(1)
					recordPostDrainForAgent(0)
					mu.Lock()
					res.LoadControlLatencies = append(res.LoadControlLatencies, elapsed)
					mu.Unlock()
				}
			}
		}()
	}

	// 4. Concurrent broker RPC churn (list/status/logs) over the broker's
	//    conduit session with configurable concurrency and smooth pacing.
	if runBrokerRPC {
		workers := cfg.BrokerRPCConcurrency
		if cfg.BrokerRPCCount > 0 && workers > cfg.BrokerRPCCount {
			workers = cfg.BrokerRPCCount
		}
		var brokerSeq atomic.Int64
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				if cfg.BrokerRPCPacing > 0 && workers > 1 {
					stagger := time.Duration(workerID) * cfg.BrokerRPCPacing / time.Duration(workers)
					if stagger > 0 {
						select {
						case <-loadCtx.Done():
							return
						case <-time.After(stagger):
						}
					}
				}
				for {
					var seq int
					if cfg.BrokerRPCCount > 0 {
						seq = int(brokerSeq.Add(1) - 1)
						if seq >= cfg.BrokerRPCCount {
							return
						}
					} else {
						if loadCtx.Err() != nil {
							return
						}
						seq = int(brokerSeq.Add(1) - 1)
					}
					cur := brokerInFlight.Add(1)
					updateHighWater(&brokerHighWater, cur)
					parentCtx := ctx
					if cfg.BrokerRPCCount == 0 {
						parentCtx = loadCtx
					}
					callCtx, cancel := context.WithTimeout(parentCtx, 15*time.Second)
					start := time.Now()
					err := cfg.BrokerRPC(callCtx, seq)
					elapsed := time.Since(start)
					cancel()
					brokerInFlight.Add(-1)
					if is429(err) {
						broker429.Add(1)
					}
					if err != nil {
						if cfg.BrokerRPCCount == 0 && atLoadEnd() {
							return
						}
						if inDrainWindow() {
							drainExcusedBrokerRPCErr.Add(1)
						} else {
							brokerErr.Add(1)
							recordErr(fmt.Errorf("broker RPC %d: %w", seq, err))
						}
					} else {
						brokerOK.Add(1)
						recordPostDrainForAgent(seq % len(cfg.Agents))
						mu.Lock()
						res.BrokerRPCLatencies = append(res.BrokerRPCLatencies, elapsed)
						mu.Unlock()
					}
					if cfg.BrokerRPCPacing > 0 {
						select {
						case <-loadCtx.Done():
							if cfg.BrokerRPCCount == 0 {
								return
							}
						case <-time.After(cfg.BrokerRPCPacing):
						}
					}
				}
			}(w)
		}
	}

	// 5. Interactive PTY echo/keepalive workers (ModePhase2 or drain observer).
	if cfg.PTYWorker != nil && cfg.PTYCount > 0 {
		for i := 0; i < cfg.PTYCount; i++ {
			agentIdx := i % len(cfg.Agents)
			target := cfg.Agents[agentIdx]
			wg.Add(1)
			go func(idx int, agentID string) {
				defer wg.Done()
				pr, err := cfg.PTYWorker(loadCtx, agentID)
				if err != nil {
					if isDrainedAgent(idx) {
						drainExcusedPTYErr.Add(1)
					} else {
						ptyErr.Add(1)
						recordErr(fmt.Errorf("pty agent %s: %w", agentID, err))
					}
				}
				if pr != nil {
					ptyOK.Add(pr.EchoSucceeded)
					ptyErr.Add(pr.EchoErrors)
					if isDrainedAgent(idx) {
						drainExcusedPTYErr.Add(pr.DrainWindowErrors)
					} else {
						ptyErr.Add(pr.DrainWindowErrors)
					}
					if pr.PostDrainEchoSucceeded > 0 {
						postDrainWork.Add(pr.PostDrainEchoSucceeded)
						postDrainReplacementWork.Add(pr.PostDrainEchoSucceeded)
					}
					mu.Lock()
					res.PTYLatencies = append(res.PTYLatencies, pr.Latencies...)
					if pr.Drain4503 {
						drain4503.Add(1)
						res.Drain4503Reasons = append(res.Drain4503Reasons, pr.CloseReason)
						if pr.CloseReason == "relay_restart" {
							drainRelayRestart.Add(1)
							if trigNS := drainTriggered.Load(); trigNS > 0 && !pr.ClosedAt.IsZero() {
								d := pr.ClosedAt.Sub(time.Unix(0, trigNS))
								if d < 0 {
									d = 0
								}
								res.Drain4503Delays = append(res.Drain4503Delays, d)
							}
						} else {
							drainOther.Add(1)
							res.Drain4503OtherReasons = append(res.Drain4503OtherReasons, pr.CloseReason)
						}
					}
					mu.Unlock()
				}
			}(agentIdx, target.AgentID)
		}
	}

	wg.Wait()
	if cfg.OnLoadComplete != nil {
		drainCtx, drainCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := cfg.OnLoadComplete(drainCtx); err != nil {
			recordErr(fmt.Errorf("on-load-complete: %w", err))
		}
		drainCancel()
	}
	sampleMemory()

	res.DeliverAttempts = deliverAttempt.Load()
	res.DeliverPeriodicAttempts = deliverPeriodicAttempt.Load()
	res.DeliverSucceeded = deliverOK.Load()
	res.DeliverNonDelivered = deliverNonDelivered.Load()
	res.Deliver429s = deliver429.Load()
	if cfg.FlusherAttemptStats != nil {
		f429, f503 := cfg.FlusherAttemptStats()
		res.FlusherDeliver429s = f429
		res.FlusherDeliver503s = f503
	}
	res.DeliverErrors = deliverErr.Load()
	res.LargeBodySucceeded = largeOK.Load()
	res.LargeBodyErrors = largeErr.Load()
	res.RotateTokenSucceeded = rotateOK.Load()
	res.RotateTokenErrors = rotateErr.Load()
	res.BrokerRPCSucceeded = brokerOK.Load()
	res.BrokerRPCErrors = brokerErr.Load()
	res.DrainExcusedBrokerRPCErrors = drainExcusedBrokerRPCErr.Load()
	res.BrokerRPC429s = broker429.Load()
	res.BaselineControlErrors = baselineControlErr.Load()
	res.ControlProbeSucceeded = controlOK.Load()
	res.ControlProbeErrors = controlErr.Load()
	res.DrainExcusedControlErrors = drainExcusedControlErr.Load()
	res.Control429s = control429.Load()
	res.Total429s = res.Deliver429s + res.FlusherDeliver429s + res.BrokerRPC429s + res.Control429s
	res.MaxConcurrentBrokerRPCs = brokerHighWater.Load()
	if cfg.BrokerSessionHighWater != nil {
		res.MaxConcurrentBrokerSessionRPCs = cfg.BrokerSessionHighWater()
	}
	res.MaxConcurrentControls = controlHighWater.Load()
	res.PTYEchoSucceeded = ptyOK.Load()
	res.PTYEchoErrors = ptyErr.Load()
	res.DrainExcusedPTYErrors = drainExcusedPTYErr.Load()
	res.Drain4503Count = drain4503.Load()
	res.Drain4503RelayRestartCount = drainRelayRestart.Load()
	res.Drain4503OtherCount = drainOther.Load()
	res.DrainAcceptedStreamAttempted = drainAcceptedAttempted.Load()
	res.DrainAcceptedStreamFinished = drainAcceptedFinished.Load()
	res.PostDrainWorkSucceeded = postDrainWork.Load()
	res.PostDrainReplacementWork = postDrainReplacementWork.Load()
	res.SessionReconnections = reconnections.Load()
	res.StalledBulkBytesWritten = bulkWritten.Load()
	res.SendToReceiptClamped = clampedDelays.Load()
	res.MemorySampleErrors = memSampleErrs.Load()
	return res, nil
}
