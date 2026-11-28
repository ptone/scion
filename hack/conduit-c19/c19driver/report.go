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

package c19driver

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/conduit"
)

// v2.17 / v2.22 §6.2 pass-bar thresholds.
const (
	BarControlP99AddedMaxMS = 100.0   // < 100 ms (v2.17)
	BarDeliverPasteP99MaxMS = 8000.0  // <= 8 s (v2.22 §6.2)
	BarSendToReceiptMaxMS   = 10000.0 // <= 10 s (v2.22 §6.2)
	BarCombinedDeliverMaxMS = 18000.0 // < 18 s (v2.22 §6.2)
	Bar429MaxCount          = int64(0)
)

// Canonical bar names referenced by --allow-unevaluated and reports.
const (
	BarControlP99Added          = "control_p99_added"
	BarSessionStall             = "session_stall"
	BarHTTP429Count             = "http_429_count"
	BarDeliverPasteP99          = "deliver_paste_p99"
	BarSendToReceiptMax         = "send_to_receipt_max"
	BarCombinedDeliver          = "combined_deliver_max"
	BarDeliverErrors            = "deliver_errors"
	BarPGRowsDelivered          = "pg_rows_delivered"
	BarOfferedLoad              = "offered_load"
	BarBrokerRPCErrors          = "broker_rpc_errors"
	BarBrokerSessionConcurrency = "broker_session_concurrency"
	BarHubRSSPeak               = "hub_rss_peak"
	BarPlannedDrain4503         = "planned_drain_4503"
	BarPlannedDrainRefusesNew   = "planned_drain_refuses_new_streams"
	BarRotateTokenMix           = "rotate_token_mix"
)

// Report verdicts.
const (
	VerdictPass       = "PASS"
	VerdictFail       = "FAIL"
	VerdictIncomplete = "INCOMPLETE"
)

// LatencySummaryMS is the JSON-friendly millisecond summary of a latency distribution.
type LatencySummaryMS struct {
	Count int     `json:"count"`
	P50MS float64 `json:"p50_ms"`
	P99MS float64 `json:"p99_ms"`
	MaxMS float64 `json:"max_ms"`
}

func summarizeMS(samples []time.Duration) LatencySummaryMS {
	s := SummarizeLatencies(samples)
	return LatencySummaryMS{
		Count: s.Count,
		P50MS: durMS(s.P50),
		P99MS: durMS(s.P99),
		MaxMS: durMS(s.Max),
	}
}

func durMS(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// BarResult records the evaluation of one v2.17 / v2.22 §6.2 pass bar.
// Unevaluated bars always carry Pass == false so they are never mistaken for
// passing measurements.
type BarResult struct {
	Name      string  `json:"name"`
	Threshold string  `json:"threshold"`
	Observed  string  `json:"observed"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
	Required  bool    `json:"required"`
	Evaluated bool    `json:"evaluated"`
	Pass      bool    `json:"pass"`
	Waived    bool    `json:"waived,omitempty"`
	Note      string  `json:"note,omitempty"`
}

// EvalOptions configures pass-bar thresholds and metadata for BuildReport.
type EvalOptions struct {
	// MemoryBudgetBytes, when > 0, enforces PeakMemoryBytes <= MemoryBudgetBytes.
	MemoryBudgetBytes int64
	// ExpectDrain, when true, evaluates planned_drain_4503 (relay_restart,
	// deadline, and post-drain replacement clauses).
	ExpectDrain bool
	// DrainDeadline is the configured hub drain deadline (e.g. 30s) used to
	// evaluate whether 4503 closes occurred within DrainDeadline + Tolerance.
	DrainDeadline time.Duration
	// DrainDeadlineTolerance is added to DrainDeadline when evaluating 4503
	// close timing (default 5s when DrainDeadline > 0).
	DrainDeadlineTolerance time.Duration
	// AllowUnevaluated lists bar names explicitly permitted to remain
	// unevaluated without turning the overall verdict into INCOMPLETE.
	AllowUnevaluated []string
	// ClockSkew is recorded in the report metadata for auditability.
	ClockSkew time.Duration
	// HubURL is recorded in the report metadata.
	HubURL string
	// RunTag overrides res.RunTag if set.
	RunTag string
	// MessageRowsCreated is the total number of Hub message rows created.
	MessageRowsCreated int64
	// LargeBodyRowsPerDelivery records how many Hub message rows are bound per
	// 512 KiB delivery (e.g. 33 for ceil(512KiB/15886) with \n\n join).
	LargeBodyRowsPerDelivery int
	// ExpectedLargeBodyCount overrides res.ExpectedLargeBodyCount when > 0.
	ExpectedLargeBodyCount int
	// BrokerFlushMode documents the broker flush implementation and any
	// deviation from production defaults (e.g. mirrored_v2_22_short_debounce).
	BrokerFlushMode string
	// BrokerDebounceMS records the fake broker's debounce delay in milliseconds.
	BrokerDebounceMS int64
	// PGRowsChecked is true when --pg-dsn was provided and the driver queried
	// Postgres messages.dispatch_state for all recorded message IDs.
	PGRowsChecked bool
	// PGRowsDelivered is the count of recorded message IDs in Postgres with
	// dispatch_state = 'delivered'.
	PGRowsDelivered int64
	// PGRowsTotal is the total count of recorded message IDs found in Postgres.
	PGRowsTotal int64
	// PGRowsQueryErr records any error encountered while querying Postgres.
	PGRowsQueryErr string
}

// Report is the machine-readable JSON output emitted by hack/conduit-c19.
type Report struct {
	TimestampUTC               string   `json:"timestamp_utc"`
	Mode                       Mode     `json:"mode"`
	RunTag                     string   `json:"run_tag,omitempty"`
	HubURL                     string   `json:"hub_url,omitempty"`
	DurationMS                 int64    `json:"duration_ms"`
	AgentCount                 int      `json:"agent_count"`
	RPCConcurrency             int      `json:"rpc_concurrency,omitempty"`
	EffectiveMaxConcurrentRPCs int      `json:"effective_max_concurrent_rpcs,omitempty"`
	ClockSkewMS                float64  `json:"clock_skew_ms"`
	BrokerFlushMode            string   `json:"broker_flush_mode,omitempty"`
	BrokerDebounceMS           int64    `json:"broker_debounce_ms,omitempty"`
	AllowUnevaluated           []string `json:"allow_unevaluated,omitempty"`

	Counts struct {
		DeliverAttempts                int64 `json:"deliver_attempts"`
		DeliverPeriodicAttempts        int64 `json:"deliver_periodic_attempts"`
		DeliverSucceeded               int64 `json:"deliver_succeeded"`
		DeliverNonDelivered            int64 `json:"deliver_non_delivered"`
		DeliverErrors                  int64 `json:"deliver_errors"`
		LargeBodySucceeded             int64 `json:"large_body_succeeded"`
		LargeBodyErrors                int64 `json:"large_body_errors"`
		LargeBodyRowsPerDelivery       int   `json:"large_body_rows_per_delivery,omitempty"`
		MessageRowsCreated             int64 `json:"message_rows_created,omitempty"`
		RotateTokenSucceeded           int64 `json:"rotate_token_succeeded"`
		RotateTokenErrors              int64 `json:"rotate_token_errors"`
		BrokerRPCSucceeded             int64 `json:"broker_rpc_succeeded"`
		BrokerRPCErrors                int64 `json:"broker_rpc_errors"`
		DrainExcusedBrokerRPCErrors    int64 `json:"drain_excused_broker_rpc_errors,omitempty"`
		BaselineControlErrors          int64 `json:"baseline_control_errors"`
		ControlProbeSucceeded          int64 `json:"control_probe_succeeded"`
		ControlProbeErrors             int64 `json:"control_probe_errors"`
		DrainExcusedControlErrors      int64 `json:"drain_excused_control_errors,omitempty"`
		PTYEchoSucceeded               int64 `json:"pty_echo_succeeded"`
		PTYEchoErrors                  int64 `json:"pty_echo_errors"`
		DrainExcusedPTYErrors          int64 `json:"drain_excused_pty_errors,omitempty"`
		Deliver429s                    int64 `json:"deliver_429s"`
		FlusherDeliver429s             int64 `json:"flusher_deliver_429s"`
		FlusherDeliver503s             int64 `json:"flusher_deliver_503s,omitempty"`
		BrokerRPC429s                  int64 `json:"broker_rpc_429s"`
		Control429s                    int64 `json:"control_429s"`
		Total429s                      int64 `json:"total_429s"`
		MaxConcurrentBrokerRPCs        int64 `json:"max_concurrent_broker_rpcs"`
		MaxConcurrentBrokerSessionRPCs int64 `json:"max_concurrent_broker_session_rpcs"`
		MaxConcurrentControls          int64 `json:"max_concurrent_controls"`
		StalledBulkBytesAtStart        int64 `json:"stalled_bulk_bytes_at_start"`
		StalledBulkBytesWritten        int64 `json:"stalled_bulk_bytes_written"`
		SendToReceiptClamped           int64 `json:"send_to_receipt_clamped"`
		SessionReconnections           int64 `json:"session_reconnections"`
	} `json:"counts"`

	Latencies struct {
		ControlBaseline          LatencySummaryMS `json:"control_baseline"`
		ControlLoad              LatencySummaryMS `json:"control_load"`
		ControlP99AddedMS        float64          `json:"control_p99_added_ms"`
		DeliverEndToEnd          LatencySummaryMS `json:"deliver_end_to_end"`
		DeliverPasteAll          LatencySummaryMS `json:"deliver_paste_all"`
		DeliverPaste512K         LatencySummaryMS `json:"deliver_paste_512k"`
		DeliverPaste512KMinBytes int              `json:"deliver_paste_512k_min_bytes,omitempty"`
		DeliverPaste512KMaxBytes int              `json:"deliver_paste_512k_max_bytes,omitempty"`
		SendToReceipt            LatencySummaryMS `json:"send_to_receipt_skew_corrected"`
		SendToReceiptRaw         LatencySummaryMS `json:"send_to_receipt_raw"`
		CombinedDeliver          LatencySummaryMS `json:"combined_deliver"`
		RotateToken              LatencySummaryMS `json:"rotate_token"`
		BrokerRPC                LatencySummaryMS `json:"broker_rpc"`
		PTYEcho                  LatencySummaryMS `json:"pty_echo"`
	} `json:"latencies"`

	Memory struct {
		PeakBytes            int64          `json:"peak_bytes"`
		BudgetBytes          int64          `json:"budget_bytes,omitempty"`
		Source               string         `json:"source,omitempty"`
		PIDCount             int            `json:"pid_count,omitempty"`
		ExitedAfterDrainPIDs int            `json:"exited_after_drain_pids,omitempty"`
		ExitedAfterDrainPID  int            `json:"exited_after_drain_pid,omitempty"`
		SampleErrors         int64          `json:"sample_errors,omitempty"`
		Samples              []MemorySample `json:"samples,omitempty"`
	} `json:"memory"`

	Drain struct {
		Expected                bool     `json:"expected"`
		Count4503               int64    `json:"count_4503"`
		RelayRestartCount       int64    `json:"relay_restart_4503"`
		OtherCount              int64    `json:"other_4503"`
		Reasons                 []string `json:"reasons,omitempty"`
		OtherReasons            []string `json:"other_reasons,omitempty"`
		TriggerTo4503MinMS      float64  `json:"trigger_to_4503_min_ms,omitempty"`
		TriggerTo4503MaxMS      float64  `json:"trigger_to_4503_max_ms,omitempty"`
		DeadlineMS              int64    `json:"deadline_ms,omitempty"`
		DeadlineEvaluated       bool     `json:"deadline_evaluated"`
		AcceptedStreamAttempted bool     `json:"accepted_stream_attempted"`
		AcceptedStreamFinished  bool     `json:"accepted_stream_finished"`
		PostDrainWork           int64    `json:"post_drain_work_succeeded"`
		ReplacementWork         int64    `json:"post_drain_replacement_work"`
		ReplacementEvaluated    bool     `json:"replacement_evaluated"`
		Reconnects              int64    `json:"session_reconnections"`
	} `json:"drain"`

	PGCheck struct {
		Checked   bool   `json:"checked"`
		Expected  int64  `json:"expected_rows,omitempty"`
		Total     int64  `json:"found_rows,omitempty"`
		Delivered int64  `json:"delivered_rows,omitempty"`
		Error     string `json:"error,omitempty"`
	} `json:"pg_check"`

	FirstError string      `json:"first_error,omitempty"`
	Bars       []BarResult `json:"bars"`
	Verdict    string      `json:"verdict"`
	Pass       bool        `json:"pass"`
}

// BuildReport converts a raw c19driver.Result into a structured Report and
// evaluates all applicable v2.17 / v2.22 §6.2 pass bars.
func BuildReport(res *Result, opts EvalOptions) Report {
	var r Report
	r.TimestampUTC = time.Now().UTC().Format(time.RFC3339)
	r.Mode = res.Mode
	r.RunTag = res.RunTag
	if opts.RunTag != "" {
		r.RunTag = opts.RunTag
	}
	r.HubURL = opts.HubURL
	r.DurationMS = res.Duration.Milliseconds()
	r.AgentCount = res.AgentCount
	r.RPCConcurrency = res.ExpectedBrokerRPCConcurrency
	if r.RPCConcurrency <= 0 {
		r.RPCConcurrency = int(res.MaxConcurrentBrokerRPCs)
	}
	r.EffectiveMaxConcurrentRPCs = res.EffectiveMaxConcurrentRPCs
	if r.EffectiveMaxConcurrentRPCs <= 0 {
		r.EffectiveMaxConcurrentRPCs = conduit.DefaultMaxConcurrentRPCs
	}
	r.ClockSkewMS = durMS(opts.ClockSkew)
	r.BrokerFlushMode = opts.BrokerFlushMode
	r.BrokerDebounceMS = opts.BrokerDebounceMS
	if len(opts.AllowUnevaluated) > 0 {
		r.AllowUnevaluated = append([]string(nil), opts.AllowUnevaluated...)
	}

	r.Counts.DeliverAttempts = res.DeliverAttempts
	r.Counts.DeliverPeriodicAttempts = res.DeliverPeriodicAttempts
	r.Counts.DeliverSucceeded = res.DeliverSucceeded
	r.Counts.DeliverNonDelivered = res.DeliverNonDelivered
	r.Counts.DeliverErrors = res.DeliverErrors
	r.Counts.LargeBodySucceeded = res.LargeBodySucceeded
	r.Counts.LargeBodyErrors = res.LargeBodyErrors
	r.Counts.LargeBodyRowsPerDelivery = opts.LargeBodyRowsPerDelivery
	r.Counts.MessageRowsCreated = opts.MessageRowsCreated
	r.Counts.RotateTokenSucceeded = res.RotateTokenSucceeded
	r.Counts.RotateTokenErrors = res.RotateTokenErrors
	r.Counts.BrokerRPCSucceeded = res.BrokerRPCSucceeded
	r.Counts.BrokerRPCErrors = res.BrokerRPCErrors
	r.Counts.DrainExcusedBrokerRPCErrors = res.DrainExcusedBrokerRPCErrors
	r.Counts.BaselineControlErrors = res.BaselineControlErrors
	r.Counts.ControlProbeSucceeded = res.ControlProbeSucceeded
	r.Counts.ControlProbeErrors = res.ControlProbeErrors
	r.Counts.DrainExcusedControlErrors = res.DrainExcusedControlErrors
	r.Counts.PTYEchoSucceeded = res.PTYEchoSucceeded
	r.Counts.PTYEchoErrors = res.PTYEchoErrors
	r.Counts.DrainExcusedPTYErrors = res.DrainExcusedPTYErrors
	r.Counts.Deliver429s = res.Deliver429s
	r.Counts.FlusherDeliver429s = res.FlusherDeliver429s
	r.Counts.FlusherDeliver503s = res.FlusherDeliver503s
	r.Counts.BrokerRPC429s = res.BrokerRPC429s
	r.Counts.Control429s = res.Control429s
	sum429 := res.Deliver429s + res.FlusherDeliver429s + res.BrokerRPC429s + res.Control429s
	r.Counts.Total429s = max(res.Total429s, sum429)
	r.Counts.MaxConcurrentBrokerRPCs = res.MaxConcurrentBrokerRPCs
	r.Counts.MaxConcurrentBrokerSessionRPCs = res.MaxConcurrentBrokerSessionRPCs
	r.Counts.MaxConcurrentControls = res.MaxConcurrentControls
	r.Counts.StalledBulkBytesAtStart = res.StalledBulkBytesAtStart
	r.Counts.StalledBulkBytesWritten = res.StalledBulkBytesWritten
	r.Counts.SendToReceiptClamped = res.SendToReceiptClamped
	r.Counts.SessionReconnections = res.SessionReconnections

	r.Latencies.ControlBaseline = summarizeMS(res.BaselineControlLatencies)
	r.Latencies.ControlLoad = summarizeMS(res.LoadControlLatencies)
	added := r.Latencies.ControlLoad.P99MS - r.Latencies.ControlBaseline.P99MS
	if added < 0 {
		added = 0
	}
	r.Latencies.ControlP99AddedMS = added
	r.Latencies.DeliverEndToEnd = summarizeMS(res.DeliverLatencies)
	r.Latencies.DeliverPasteAll = summarizeMS(res.DeliverPasteLatencies)
	r.Latencies.DeliverPaste512K = summarizeMS(res.LargePasteLatencies)
	r.Latencies.DeliverPaste512KMinBytes = res.LargeBodyMinBytesObserved
	r.Latencies.DeliverPaste512KMaxBytes = res.LargeBodyMaxBytesObserved
	r.Latencies.SendToReceipt = summarizeMS(res.SendToReceiptDelays)
	r.Latencies.SendToReceiptRaw = summarizeMS(res.RawSendToReceiptDelays)
	r.Latencies.CombinedDeliver = summarizeMS(res.CombinedDeliverLatencies)
	r.Latencies.RotateToken = summarizeMS(res.RotateLatencies)
	r.Latencies.BrokerRPC = summarizeMS(res.BrokerRPCLatencies)
	r.Latencies.PTYEcho = summarizeMS(res.PTYLatencies)

	r.Memory.PeakBytes = res.PeakMemoryBytes
	r.Memory.BudgetBytes = opts.MemoryBudgetBytes
	r.Memory.Source = res.MemorySource
	r.Memory.PIDCount = res.MemoryPIDCount
	r.Memory.ExitedAfterDrainPIDs = res.MemoryExitedAfterDrainPIDs
	r.Memory.ExitedAfterDrainPID = res.MemoryExitedAfterDrainPID
	r.Memory.SampleErrors = res.MemorySampleErrors
	r.Memory.Samples = res.MemorySamples

	r.Drain.Expected = opts.ExpectDrain
	r.Drain.Count4503 = res.Drain4503Count
	r.Drain.RelayRestartCount = res.Drain4503RelayRestartCount
	r.Drain.OtherCount = res.Drain4503OtherCount
	r.Drain.Reasons = res.Drain4503Reasons
	r.Drain.OtherReasons = res.Drain4503OtherReasons
	r.Drain.AcceptedStreamAttempted = res.DrainAcceptedStreamAttempted
	r.Drain.AcceptedStreamFinished = res.DrainAcceptedStreamFinished
	r.Drain.PostDrainWork = res.PostDrainWorkSucceeded
	r.Drain.ReplacementWork = res.PostDrainReplacementWork
	r.Drain.Reconnects = res.SessionReconnections
	r.Drain.DeadlineMS = opts.DrainDeadline.Milliseconds()
	if len(res.Drain4503Delays) > 0 {
		cp := append([]time.Duration(nil), res.Drain4503Delays...)
		slices.Sort(cp)
		r.Drain.TriggerTo4503MinMS = durMS(cp[0])
		r.Drain.TriggerTo4503MaxMS = durMS(cp[len(cp)-1])
	}

	r.PGCheck.Checked = opts.PGRowsChecked
	r.PGCheck.Expected = opts.MessageRowsCreated
	r.PGCheck.Total = opts.PGRowsTotal
	r.PGCheck.Delivered = opts.PGRowsDelivered
	r.PGCheck.Error = opts.PGRowsQueryErr

	if res.FirstError != nil {
		r.FirstError = res.FirstError.Error()
	}

	r.Bars = evaluateBars(&r, res, opts)

	hasFail := false
	hasIncomplete := false
	for i := range r.Bars {
		b := &r.Bars[i]
		if !b.Evaluated {
			// Unevaluated bars must never claim pass=true.
			b.Pass = false
			if b.Required {
				if slices.Contains(opts.AllowUnevaluated, b.Name) {
					b.Waived = true
				} else {
					hasIncomplete = true
				}
			}
			continue
		}
		if !b.Pass {
			hasFail = true
		}
	}

	switch {
	case hasFail:
		r.Verdict = VerdictFail
		r.Pass = false
	case hasIncomplete:
		r.Verdict = VerdictIncomplete
		r.Pass = false
	default:
		r.Verdict = VerdictPass
		r.Pass = true
	}
	return r
}

func evaluateBars(r *Report, res *Result, opts EvalOptions) []BarResult {
	var bars []BarResult

	// 1. Control RPC p99 added vs baseline (< 100 ms, v2.17). Required in all modes.
	//    Fails if baseline or non-drain load probes errored even when successful samples were fast.
	probeNonDrainErrs := r.Counts.BaselineControlErrors + r.Counts.ControlProbeErrors
	switch {
	case probeNonDrainErrs > 0:
		val := r.Latencies.ControlP99AddedMS
		bars = append(bars, BarResult{
			Name:      BarControlP99Added,
			Threshold: "< 100 ms (0 probe errors)",
			Observed: fmt.Sprintf("%.2f ms (base %.2f -> load %.2f, n=%d/%d, base_err=%d, load_err=%d)",
				val, r.Latencies.ControlBaseline.P99MS, r.Latencies.ControlLoad.P99MS,
				r.Latencies.ControlBaseline.Count, r.Latencies.ControlLoad.Count,
				r.Counts.BaselineControlErrors, r.Counts.ControlProbeErrors),
			Value:     val,
			Unit:      "ms",
			Required:  true,
			Evaluated: true,
			Pass:      false,
			Note:      "agent-0 conduit session control probes failed outside drain window (possible session-window stall)",
		})
	case r.Latencies.ControlBaseline.Count > 0 && r.Latencies.ControlLoad.Count > 0:
		val := r.Latencies.ControlP99AddedMS
		obs := fmt.Sprintf("%.2f ms (base %.2f -> load %.2f, n=%d/%d",
			val, r.Latencies.ControlBaseline.P99MS, r.Latencies.ControlLoad.P99MS,
			r.Latencies.ControlBaseline.Count, r.Latencies.ControlLoad.Count)
		if r.Counts.DrainExcusedControlErrors > 0 {
			obs += fmt.Sprintf(", drain_excused=%d", r.Counts.DrainExcusedControlErrors)
		}
		obs += ")"
		bars = append(bars, BarResult{
			Name:      BarControlP99Added,
			Threshold: "< 100 ms",
			Observed:  obs,
			Value:     val,
			Unit:      "ms",
			Required:  true,
			Evaluated: true,
			Pass:      val < BarControlP99AddedMaxMS,
			Note:      "agent-0 conduit session PTY open+echo probe (off broker-RPC churn path)",
		})
	default:
		bars = append(bars, BarResult{
			Name:      BarControlP99Added,
			Threshold: "< 100 ms",
			Observed:  "unevaluated",
			Unit:      "ms",
			Required:  true,
			Evaluated: false,
			Pass:      false,
			Note:      "missing baseline or under-load agent-session control probe samples",
		})
	}

	// 1b. Session stall gate (L6a/L6c: "no session-window stall"). Required in all modes.
	//     Gates non-drain control-probe (baseline + load) and PTY echo errors.
	stallAttempts := int64(r.Latencies.ControlBaseline.Count) + r.Counts.BaselineControlErrors +
		r.Counts.ControlProbeSucceeded + r.Counts.ControlProbeErrors + r.Counts.DrainExcusedControlErrors +
		r.Counts.PTYEchoSucceeded + r.Counts.PTYEchoErrors + r.Counts.DrainExcusedPTYErrors
	stallNonDrainErrs := r.Counts.BaselineControlErrors + r.Counts.ControlProbeErrors + r.Counts.PTYEchoErrors
	if stallAttempts == 0 {
		bars = append(bars, BarResult{
			Name:      BarSessionStall,
			Threshold: "0 non-drain probe/PTY errors",
			Observed:  "unevaluated (0 probes/echoes)",
			Unit:      "count",
			Required:  true,
			Evaluated: false,
			Pass:      false,
			Note:      "no agent-session control probes or PTY echoes attempted",
		})
	} else {
		obs := fmt.Sprintf("base_err=%d probe_err=%d pty_err=%d",
			r.Counts.BaselineControlErrors, r.Counts.ControlProbeErrors, r.Counts.PTYEchoErrors)
		if r.Counts.DrainExcusedControlErrors > 0 || r.Counts.DrainExcusedPTYErrors > 0 {
			obs += fmt.Sprintf(" (drain_excused: probe=%d pty=%d)", r.Counts.DrainExcusedControlErrors, r.Counts.DrainExcusedPTYErrors)
		}
		bars = append(bars, BarResult{
			Name:      BarSessionStall,
			Threshold: "0 non-drain probe/PTY errors",
			Observed:  obs,
			Value:     float64(stallNonDrainErrs),
			Unit:      "count",
			Required:  true,
			Evaluated: true,
			Pass:      stallNonDrainErrs == 0,
			Note:      "fails on any baseline/load control-probe or PTY echo error outside the planned-drain window",
		})
	}

	// 2. 429 count == 0 at default MaxConcurrentRPCs=128 (v2.17 / v2.22 §6.2). Required in all modes.
	//    If a non-default --broker-max-concurrent-rpcs override was used and 0 429s occurred,
	//    mark unevaluated (INCOMPLETE unless waived) so weakening the limit never passes silently.
	obs429 := fmt.Sprintf("%d (deliver=%d flusher_429=%d broker=%d control=%d",
		r.Counts.Total429s, r.Counts.Deliver429s, r.Counts.FlusherDeliver429s, r.Counts.BrokerRPC429s, r.Counts.Control429s)
	if r.Counts.FlusherDeliver503s > 0 {
		obs429 += fmt.Sprintf(", flusher_503=%d", r.Counts.FlusherDeliver503s)
	}
	obs429 += ")"
	nonDefaultMax := r.EffectiveMaxConcurrentRPCs != conduit.DefaultMaxConcurrentRPCs
	if r.Counts.Total429s > Bar429MaxCount {
		bars = append(bars, BarResult{
			Name:      BarHTTP429Count,
			Threshold: fmt.Sprintf("== 0 (at default MaxConcurrentRPCs=%d)", conduit.DefaultMaxConcurrentRPCs),
			Observed:  obs429,
			Value:     float64(r.Counts.Total429s),
			Unit:      "count",
			Required:  true,
			Evaluated: true,
			Pass:      false,
			Note:      "counts direct HTTP 429s from /message, every /deliver attempt (incl. retried attempts in broker flusher), control probes, and broker-session 429s surfaced as Hub 502",
		})
	} else if nonDefaultMax {
		bars = append(bars, BarResult{
			Name:      BarHTTP429Count,
			Threshold: fmt.Sprintf("== 0 (at default MaxConcurrentRPCs=%d)", conduit.DefaultMaxConcurrentRPCs),
			Observed:  fmt.Sprintf("%s [non_default_max_concurrent_rpcs=%d]", obs429, r.EffectiveMaxConcurrentRPCs),
			Value:     0,
			Unit:      "count",
			Required:  true,
			Evaluated: false,
			Pass:      false,
			Note:      fmt.Sprintf("non_default_max_concurrent_rpcs=%d overrides default %d; waive via --allow-unevaluated=http_429_count only for diagnostic runs", r.EffectiveMaxConcurrentRPCs, conduit.DefaultMaxConcurrentRPCs),
		})
	} else {
		bars = append(bars, BarResult{
			Name:      BarHTTP429Count,
			Threshold: fmt.Sprintf("== 0 (at default MaxConcurrentRPCs=%d)", conduit.DefaultMaxConcurrentRPCs),
			Observed:  obs429,
			Value:     0,
			Unit:      "count",
			Required:  true,
			Evaluated: true,
			Pass:      true,
			Note:      "counts direct HTTP 429s from /message, every /deliver attempt (incl. retried attempts in broker flusher), control probes, and broker-session 429s surfaced as Hub 502",
		})
	}

	// 3. Deliver-mode bars (v2.22 §6.2).
	if r.Mode == ModeDeliver {
		wantLarge := res.ExpectedLargeBodyCount
		if opts.ExpectedLargeBodyCount > 0 {
			wantLarge = opts.ExpectedLargeBodyCount
		}
		paste512 := r.Latencies.DeliverPaste512K
		minB := r.Latencies.DeliverPaste512KMinBytes
		maxB := r.Latencies.DeliverPaste512KMaxBytes
		switch {
		case paste512.Count == 0:
			bars = append(bars, BarResult{
				Name:      BarDeliverPasteP99,
				Threshold: "<= 8000 ms (512 KiB)",
				Observed:  fmt.Sprintf("0 samples (expected >= %d)", max(1, wantLarge)),
				Unit:      "ms",
				Required:  true,
				Evaluated: true,
				Pass:      false,
				Note:      "no 512 KiB agent-side paste timings recorded",
			})
		case wantLarge > 0 && paste512.Count < wantLarge:
			bars = append(bars, BarResult{
				Name:      BarDeliverPasteP99,
				Threshold: fmt.Sprintf("<= 8000 ms (n >= %d)", wantLarge),
				Observed:  fmt.Sprintf("%.2f ms (n=%d < %d, bytes=%d..%d)", paste512.P99MS, paste512.Count, wantLarge, minB, maxB),
				Value:     paste512.P99MS,
				Unit:      "ms",
				Required:  true,
				Evaluated: true,
				Pass:      false,
				Note:      "fewer 512 KiB paste samples than configured large-body-count",
			})
		case minB < LargeBodyMinBytes:
			bars = append(bars, BarResult{
				Name:      BarDeliverPasteP99,
				Threshold: fmt.Sprintf("<= 8000 ms (bytes >= %d)", LargeBodyMinBytes),
				Observed:  fmt.Sprintf("%.2f ms (n=%d, bytes=%d..%d < %d)", paste512.P99MS, paste512.Count, minB, maxB, LargeBodyMinBytes),
				Value:     paste512.P99MS,
				Unit:      "ms",
				Required:  true,
				Evaluated: true,
				Pass:      false,
				Note:      "large-body payload size unrecorded or below 496 KiB threshold",
			})
		case r.Counts.LargeBodyErrors > 0:
			bars = append(bars, BarResult{
				Name:      BarDeliverPasteP99,
				Threshold: "<= 8000 ms (0 large errors)",
				Observed:  fmt.Sprintf("%.2f ms (n=%d, large_err=%d, bytes=%d..%d)", paste512.P99MS, paste512.Count, r.Counts.LargeBodyErrors, minB, maxB),
				Value:     paste512.P99MS,
				Unit:      "ms",
				Required:  true,
				Evaluated: true,
				Pass:      false,
				Note:      "one or more 512 KiB deliveries errored",
			})
		default:
			bars = append(bars, BarResult{
				Name:      BarDeliverPasteP99,
				Threshold: "<= 8000 ms (512 KiB)",
				Observed:  fmt.Sprintf("%.2f ms (n=%d, bytes=%d..%d)", paste512.P99MS, paste512.Count, minB, maxB),
				Value:     paste512.P99MS,
				Unit:      "ms",
				Required:  true,
				Evaluated: true,
				Pass:      paste512.P99MS <= BarDeliverPasteP99MaxMS,
				Note:      "33 x 15,000-byte rows + Hub envelopes (~507-520 KiB); measures fake-agent RecordingTmuxRunner handler time (includes extra submits, not real tmux)",
			})
		}

		// Skew-corrected send-to-receipt delay max <= 10 s (p99 also recorded).
		// Clamped negative samples (> 0) indicate mis-signed --clock-skew or clock drift
		// and yield INCOMPLETE (never PASS) unless max already exceeded 10 s (FAIL).
		if r.Latencies.SendToReceipt.Count > 0 {
			obs := fmt.Sprintf("%.2f ms (p99 %.2f ms, n=%d", r.Latencies.SendToReceipt.MaxMS, r.Latencies.SendToReceipt.P99MS, r.Latencies.SendToReceipt.Count)
			if r.Counts.SendToReceiptClamped > 0 {
				obs += fmt.Sprintf(", clamped=%d", r.Counts.SendToReceiptClamped)
			}
			obs += ")"
			switch {
			case r.Latencies.SendToReceipt.MaxMS > BarSendToReceiptMaxMS:
				bars = append(bars, BarResult{
					Name:      BarSendToReceiptMax,
					Threshold: "<= 10000 ms (clamped == 0)",
					Observed:  obs,
					Value:     r.Latencies.SendToReceipt.MaxMS,
					Unit:      "ms",
					Required:  true,
					Evaluated: true,
					Pass:      false,
				})
			case r.Counts.SendToReceiptClamped > 0:
				bars = append(bars, BarResult{
					Name:      BarSendToReceiptMax,
					Threshold: "<= 10000 ms (clamped == 0)",
					Observed:  obs,
					Value:     r.Latencies.SendToReceipt.MaxMS,
					Unit:      "ms",
					Required:  true,
					Evaluated: false,
					Pass:      false,
					Note:      "clamped negative send-to-receipt samples detected; verify --clock-skew sign/value",
				})
			default:
				bars = append(bars, BarResult{
					Name:      BarSendToReceiptMax,
					Threshold: "<= 10000 ms (clamped == 0)",
					Observed:  obs,
					Value:     r.Latencies.SendToReceipt.MaxMS,
					Unit:      "ms",
					Required:  true,
					Evaluated: true,
					Pass:      true,
				})
			}
		} else {
			bars = append(bars, BarResult{
				Name:      BarSendToReceiptMax,
				Threshold: "<= 10000 ms",
				Observed:  "0 samples",
				Unit:      "ms",
				Required:  true,
				Evaluated: true,
				Pass:      false,
				Note:      "no send-to-receipt timings recorded",
			})
		}

		// Combined per-delivery max < 18 s.
		if r.Latencies.CombinedDeliver.Count > 0 {
			bars = append(bars, BarResult{
				Name:      BarCombinedDeliver,
				Threshold: "< 18000 ms",
				Observed:  fmt.Sprintf("%.2f ms (p99 %.2f ms, n=%d)", r.Latencies.CombinedDeliver.MaxMS, r.Latencies.CombinedDeliver.P99MS, r.Latencies.CombinedDeliver.Count),
				Value:     r.Latencies.CombinedDeliver.MaxMS,
				Unit:      "ms",
				Required:  true,
				Evaluated: true,
				Pass:      r.Latencies.CombinedDeliver.MaxMS < BarCombinedDeliverMaxMS,
			})
		} else {
			bars = append(bars, BarResult{
				Name:      BarCombinedDeliver,
				Threshold: "< 18000 ms",
				Observed:  "0 samples",
				Unit:      "ms",
				Required:  true,
				Evaluated: true,
				Pass:      false,
				Note:      "no combined delivery timings recorded",
			})
		}

		// Zero broker-observed /deliver HTTP errors / non-delivered outcomes.
		badDeliveries := r.Counts.DeliverErrors + r.Counts.DeliverNonDelivered + r.Counts.LargeBodyErrors
		bars = append(bars, BarResult{
			Name:      BarDeliverErrors,
			Threshold: "== 0 (succeeded > 0)",
			Observed:  fmt.Sprintf("errors=%d non_delivered=%d succeeded=%d", r.Counts.DeliverErrors, r.Counts.DeliverNonDelivered, r.Counts.DeliverSucceeded),
			Value:     float64(badDeliveries),
			Unit:      "count",
			Required:  true,
			Evaluated: true,
			Pass:      badDeliveries == 0 && r.Counts.DeliverSucceeded > 0,
			Note:      "broker-observed /deliver HTTP outcomes only (not PG row state; Postgres terminal state is checked by pg_rows_delivered)",
		})

		// Postgres terminal state check for all recorded message IDs (L6c: "100% PG message rows terminal delivered").
		switch {
		case !opts.PGRowsChecked:
			bars = append(bars, BarResult{
				Name:      BarPGRowsDelivered,
				Threshold: "100% recorded rows delivered in PG",
				Observed:  fmt.Sprintf("unevaluated (no --pg-dsn; recorded_ids=%d)", r.Counts.MessageRowsCreated),
				Unit:      "rows",
				Required:  true,
				Evaluated: false,
				Pass:      false,
				Note:      "pass --pg-dsn (env C19_PG_DSN) to verify directly, or run psql against --message-ids-file: SELECT dispatch_state, COUNT(*) FROM messages WHERE id = ANY(string_to_array(pg_read_file('<ids-file>'), E'\\n')) GROUP BY dispatch_state;",
			})
		case opts.PGRowsQueryErr != "":
			bars = append(bars, BarResult{
				Name:      BarPGRowsDelivered,
				Threshold: "100% recorded rows delivered in PG",
				Observed:  fmt.Sprintf("query_error=%s", opts.PGRowsQueryErr),
				Unit:      "rows",
				Required:  true,
				Evaluated: true,
				Pass:      false,
				Note:      "Postgres verification query failed",
			})
		default:
			expected := r.Counts.MessageRowsCreated
			pass := expected > 0 && opts.PGRowsTotal == expected && opts.PGRowsDelivered == expected
			bars = append(bars, BarResult{
				Name:      BarPGRowsDelivered,
				Threshold: fmt.Sprintf("delivered == %d (100%% of recorded rows)", expected),
				Observed:  fmt.Sprintf("delivered=%d found=%d recorded=%d", opts.PGRowsDelivered, opts.PGRowsTotal, expected),
				Value:     float64(opts.PGRowsDelivered),
				Unit:      "rows",
				Required:  true,
				Evaluated: true,
				Pass:      pass,
				Note:      "direct Postgres query over all message IDs recorded in --message-ids-file",
			})
		}

		// Offered-load gate (L6c: 1 delivery/agent/2s; periodic deliveries only,
		// excluding large-body injections so large bodies do not inflate periodic rate).
		interval := res.DeliverInterval
		if interval <= 0 {
			interval = 2 * time.Second
		}
		if res.AgentCount <= 0 || res.Duration <= 0 {
			bars = append(bars, BarResult{
				Name:      BarOfferedLoad,
				Threshold: ">= 90% of configured delivery rate",
				Observed:  "unevaluated",
				Unit:      "attempts",
				Required:  true,
				Evaluated: false,
				Pass:      false,
				Note:      "missing agent count or duration for offered-load calculation",
			})
		} else {
			periodicAttempts := r.Counts.DeliverPeriodicAttempts
			if periodicAttempts == 0 && r.Counts.DeliverAttempts > 0 && r.Counts.LargeBodySucceeded == 0 && r.Counts.LargeBodyErrors == 0 {
				periodicAttempts = r.Counts.DeliverAttempts
			}
			expected := float64(res.AgentCount) * (float64(res.Duration) / float64(interval))
			minAttempts := int64(math.Ceil(0.9 * expected))
			if expected > 0 && minAttempts < 1 {
				minAttempts = 1
			}
			pct := 0.0
			if expected > 0 {
				pct = 100.0 * float64(periodicAttempts) / expected
			}
			ratePerSec := float64(periodicAttempts) / res.Duration.Seconds()
			bars = append(bars, BarResult{
				Name:      BarOfferedLoad,
				Threshold: fmt.Sprintf(">= %d periodic attempts (90%% of %.0f)", minAttempts, expected),
				Observed:  fmt.Sprintf("periodic_attempts=%d (%.0f%% of %.0f, %.1f/s; total=%d)", periodicAttempts, pct, expected, ratePerSec, r.Counts.DeliverAttempts),
				Value:     float64(periodicAttempts),
				Unit:      "attempts",
				Required:  true,
				Evaluated: true,
				Pass:      periodicAttempts >= minAttempts,
				Note:      "measures periodic delivery attempts only (excludes large-body injections); detects closed-loop slowdown / dropped ticks",
			})
		}

		// Rotate-token mix disclosure.
		if res.ExpectedRotateTokenCount > 0 {
			bars = append(bars, BarResult{
				Name:      BarRotateTokenMix,
				Threshold: fmt.Sprintf("errors == 0 (succeeded >= %d)", res.ExpectedRotateTokenCount),
				Observed:  fmt.Sprintf("errors=%d succeeded=%d", r.Counts.RotateTokenErrors, r.Counts.RotateTokenSucceeded),
				Value:     float64(r.Counts.RotateTokenErrors),
				Unit:      "count",
				Required:  true,
				Evaluated: true,
				Pass:      r.Counts.RotateTokenErrors == 0 && r.Counts.RotateTokenSucceeded >= int64(res.ExpectedRotateTokenCount),
			})
		} else {
			bars = append(bars, BarResult{
				Name:      BarRotateTokenMix,
				Threshold: "mixed with deliveries (v2.22 §6.2)",
				Observed:  "not exercised: no Hub rotation route",
				Unit:      "count",
				Required:  true,
				Evaluated: false,
				Pass:      false,
				Note:      "Hub has no HTTP route invoking Server.RequestAgentTokenRotation; waive via --allow-unevaluated=rotate_token_mix",
			})
		}
	}

	// 4. Broker RPC error & broker-session concurrency bars in every mode where broker-RPC churn ran.
	if r.Mode == ModeBrokerRPC || res.BrokerRPCEnabled {
		brokerObs := fmt.Sprintf("errors=%d succeeded=%d client_conc=%d broker_session_conc=%d", r.Counts.BrokerRPCErrors, r.Counts.BrokerRPCSucceeded, r.Counts.MaxConcurrentBrokerRPCs, r.Counts.MaxConcurrentBrokerSessionRPCs)
		if r.Counts.DrainExcusedBrokerRPCErrors > 0 {
			brokerObs += fmt.Sprintf(" (drain_excused=%d)", r.Counts.DrainExcusedBrokerRPCErrors)
		}
		bars = append(bars, BarResult{
			Name:      BarBrokerRPCErrors,
			Threshold: "== 0 (succeeded > 0)",
			Observed:  brokerObs,
			Value:     float64(r.Counts.BrokerRPCErrors),
			Unit:      "count",
			Required:  true,
			Evaluated: true,
			Pass:      r.Counts.BrokerRPCErrors == 0 && r.Counts.BrokerRPCSucceeded > 0,
			Note:      "client_conc is driver goroutine concurrency (--rpc-concurrency); broker_session_conc is inbound in-flight high-water on the broker conduit session; drain-window /logs errors are excused and reported separately",
		})

		// Always evaluate broker_session_concurrency against conduit.DefaultMaxConcurrentRPCs (128)
		// so lowering --rpc-concurrency cannot vacuously pass "exercising MaxConcurrentRPCs (128)".
		defaultTarget := conduit.DefaultMaxConcurrentRPCs
		minHW := int64(math.Ceil(0.9 * float64(defaultTarget)))
		bars = append(bars, BarResult{
			Name:      BarBrokerSessionConcurrency,
			Threshold: fmt.Sprintf(">= %d (90%% of default %d)", minHW, defaultTarget),
			Observed:  fmt.Sprintf("broker_session_conc=%d (client_conc=%d, rpc_concurrency=%d, default_max=%d)", r.Counts.MaxConcurrentBrokerSessionRPCs, r.Counts.MaxConcurrentBrokerRPCs, r.RPCConcurrency, defaultTarget),
			Value:     float64(r.Counts.MaxConcurrentBrokerSessionRPCs),
			Unit:      "count",
			Required:  true,
			Evaluated: true,
			Pass:      r.Counts.MaxConcurrentBrokerSessionRPCs >= minHW,
			Note:      "evaluated against conduit.DefaultMaxConcurrentRPCs (128) so low --rpc-concurrency or --broker-rpc-latency=0 cannot vacuously pass",
		})
	}

	// 5. Aggregate session memory / hub RSS. Required in all L6 modes.
	switch {
	case r.Memory.SampleErrors > 0:
		bars = append(bars, BarResult{
			Name:      BarHubRSSPeak,
			Threshold: formatBudget(opts.MemoryBudgetBytes),
			Observed:  fmt.Sprintf("sample_errors=%d peak=%.1f MiB (source=%s, pids=%d)", r.Memory.SampleErrors, bytesMiB(r.Memory.PeakBytes), nonEmpty(r.Memory.Source, "unknown"), r.Memory.PIDCount),
			Value:     float64(r.Memory.PeakBytes),
			Unit:      "bytes",
			Required:  true,
			Evaluated: true,
			Pass:      false,
			Note:      "one or more hub RSS samples failed (e.g. unreadable non-drained PID)",
		})
	case opts.MemoryBudgetBytes > 0 && r.Memory.PeakBytes > 0:
		obs := fmt.Sprintf("%.1f MiB (source=%s", bytesMiB(r.Memory.PeakBytes), nonEmpty(r.Memory.Source, "unknown"))
		if r.Memory.PIDCount > 0 {
			obs += fmt.Sprintf(", pids=%d", r.Memory.PIDCount)
		}
		if r.Memory.ExitedAfterDrainPID > 0 {
			obs += fmt.Sprintf(", exited_after_drain_pid=%d", r.Memory.ExitedAfterDrainPID)
		} else if r.Memory.ExitedAfterDrainPIDs > 0 {
			obs += fmt.Sprintf(", exited_after_drain=%d", r.Memory.ExitedAfterDrainPIDs)
		}
		obs += ")"
		bars = append(bars, BarResult{
			Name:      BarHubRSSPeak,
			Threshold: formatBudget(opts.MemoryBudgetBytes),
			Observed:  obs,
			Value:     float64(r.Memory.PeakBytes),
			Unit:      "bytes",
			Required:  true,
			Evaluated: true,
			Pass:      r.Memory.PeakBytes <= opts.MemoryBudgetBytes,
		})
	default:
		note := "no hub RSS sampler configured"
		if r.Memory.PeakBytes > 0 && opts.MemoryBudgetBytes <= 0 {
			note = "no --memory-budget-mb configured"
		}
		bars = append(bars, BarResult{
			Name:      BarHubRSSPeak,
			Threshold: formatBudget(opts.MemoryBudgetBytes),
			Observed:  "unevaluated",
			Value:     float64(r.Memory.PeakBytes),
			Unit:      "bytes",
			Required:  true,
			Evaluated: false,
			Pass:      false,
			Note:      note,
		})
	}

	// 6. Planned drain 4503 observation: evaluates relay_restart, lower+upper
	//    deadline window [deadline-tol, deadline+tol], bounded pre-trigger
	//    accepted stream completion, session reconnection, and replacement work.
	drainObs := fmt.Sprintf("relay_restart=%d other_4503=%d delay=%.0f..%.0fms accepted_ok=%t replacement_work=%d reconnects=%d",
		r.Drain.RelayRestartCount, r.Drain.OtherCount, r.Drain.TriggerTo4503MinMS, r.Drain.TriggerTo4503MaxMS,
		r.Drain.AcceptedStreamFinished, r.Drain.ReplacementWork, r.Drain.Reconnects)
	if !opts.ExpectDrain {
		bars = append(bars, BarResult{
			Name:      BarPlannedDrain4503,
			Threshold: ">= 1 relay_restart at deadline + accepted stream + reconnect + replacement",
			Observed:  drainObs,
			Value:     float64(r.Drain.RelayRestartCount),
			Unit:      "count",
			Required:  true,
			Evaluated: false,
			Pass:      false,
			Note:      "planned drain not enabled (--expect-drain not set)",
		})
	} else {
		hasTrigger := !res.DrainTriggeredAt.IsZero()
		hasDeadline := opts.DrainDeadline > 0
		tol := opts.DrainDeadlineTolerance
		if tol <= 0 {
			tol = 5 * time.Second
		}
		minBound := opts.DrainDeadline - tol
		if minBound < 0 {
			minBound = 0
		}
		maxBound := opts.DrainDeadline + tol
		r.Drain.DeadlineEvaluated = hasTrigger && hasDeadline && len(res.Drain4503Delays) > 0
		r.Drain.ReplacementEvaluated = hasTrigger

		switch {
		case r.Drain.RelayRestartCount < 1:
			bars = append(bars, BarResult{
				Name:      BarPlannedDrain4503,
				Threshold: fmt.Sprintf(">= 1 relay_restart in [%s, %s] + accepted stream + reconnect + replacement", minBound, maxBound),
				Observed:  drainObs,
				Value:     0,
				Unit:      "count",
				Required:  true,
				Evaluated: true,
				Pass:      false,
				Note:      "no 4503 relay_restart close observed",
			})
		case !hasTrigger || !hasDeadline || !res.DrainAcceptedStreamAttempted:
			note := "deadline/replacement clauses unmeasurable without --drain-cmd/--drain-after and --drain-deadline"
			if hasTrigger && hasDeadline && !res.DrainAcceptedStreamAttempted {
				note = "bounded pre-trigger accepted-stream check was not attempted"
			}
			bars = append(bars, BarResult{
				Name:      BarPlannedDrain4503,
				Threshold: fmt.Sprintf(">= 1 relay_restart in [%s, %s] + accepted stream + reconnect + replacement", minBound, maxBound),
				Observed:  drainObs,
				Value:     float64(r.Drain.RelayRestartCount),
				Unit:      "count",
				Required:  true,
				Evaluated: false,
				Pass:      false,
				Note:      note,
			})
		default:
			cp := append([]time.Duration(nil), res.Drain4503Delays...)
			slices.Sort(cp)
			minDelay := cp[0]
			maxDelay := cp[len(cp)-1]
			withinDeadline := len(cp) > 0 && minDelay >= minBound && maxDelay <= maxBound
			acceptedOK := res.DrainAcceptedStreamFinished
			reconnected := r.Drain.Reconnects >= 1
			replacementOK := r.Drain.ReplacementWork >= 1
			pass := withinDeadline && acceptedOK && reconnected && replacementOK
			note := fmt.Sprintf("window=[%s, %s], accepted_ok=%t, reconnects=%d, replacement_work=%d", minBound, maxBound, acceptedOK, r.Drain.Reconnects, r.Drain.ReplacementWork)
			switch {
			case minDelay < minBound:
				note = fmt.Sprintf("4503 close delay %.0fms arrived before deadline lower bound %s (immediate kill, not graceful drain)", durMS(minDelay), minBound)
			case maxDelay > maxBound:
				note = fmt.Sprintf("4503 close delay %.0fms exceeded deadline upper bound %s", durMS(maxDelay), maxBound)
			case !acceptedOK:
				note = "bounded pre-trigger accepted stream failed to complete during drain window"
			case !reconnected:
				note = "no agent session re-admitted after drain trigger (reconnects == 0)"
			case !replacementOK:
				note = "no work succeeded on a re-admitted session or re-dialed PTY stream after drain trigger"
			}
			bars = append(bars, BarResult{
				Name:      BarPlannedDrain4503,
				Threshold: fmt.Sprintf(">= 1 relay_restart in [%s, %s] + accepted stream + reconnect + replacement", minBound, maxBound),
				Observed:  drainObs,
				Value:     float64(r.Drain.RelayRestartCount),
				Unit:      "count",
				Required:  true,
				Evaluated: true,
				Pass:      pass,
				Note:      note,
			})
		}
	}

	// 6b. Split sub-clause: "planned drain refuses new streams on draining session".
	//     Not directly observable behind a round-robin LB without node pinning;
	//     always emitted as unevaluated (INCOMPLETE unless explicitly waived).
	bars = append(bars, BarResult{
		Name:      BarPlannedDrainRefusesNew,
		Threshold: "new stream open on draining hub refused",
		Observed:  "not exercised: round-robin LB cannot pin new stream to draining hub",
		Unit:      "count",
		Required:  true,
		Evaluated: false,
		Pass:      false,
		Note:      "verify via direct-node L2/L3 drain check and waive via --allow-unevaluated=planned_drain_refuses_new_streams",
	})

	return bars
}

func bytesMiB(b int64) float64 {
	return float64(b) / (1024 * 1024)
}

func formatBudget(b int64) string {
	if b <= 0 {
		return "configured budget"
	}
	return fmt.Sprintf("<= %.1f MiB", bytesMiB(b))
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// FormatTable renders a concise human-readable summary table of r.
func FormatTable(r Report) string {
	var sb strings.Builder
	verdict := r.Verdict
	if verdict == "" {
		if r.Pass {
			verdict = VerdictPass
		} else {
			verdict = VerdictFail
		}
	}
	fmt.Fprintf(&sb, "C19 Load Report [%s] mode=%s run_tag=%s agents=%d duration=%dms client_rpc_conc=%d broker_session_rpc_conc=%d\n",
		verdict, r.Mode, r.RunTag, r.AgentCount, r.DurationMS, r.Counts.MaxConcurrentBrokerRPCs, r.Counts.MaxConcurrentBrokerSessionRPCs)
	tw := tabwriter.NewWriter(&sb, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "BAR\tTHRESHOLD\tOBSERVED\tSTATUS")
	for _, b := range r.Bars {
		var status string
		switch {
		case !b.Evaluated && b.Waived:
			status = "WAIVED"
		case !b.Evaluated && b.Required:
			status = "INCOMPLETE"
		case !b.Evaluated:
			status = "UNEVALUATED"
		case b.Pass:
			status = "PASS"
		default:
			status = "FAIL"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.Name, b.Threshold, b.Observed, status)
	}
	_ = tw.Flush()
	if r.FirstError != "" {
		fmt.Fprintf(&sb, "First error: %s\n", r.FirstError)
	}
	return sb.String()
}
