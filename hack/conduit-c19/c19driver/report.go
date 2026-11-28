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
	"strings"
	"text/tabwriter"
	"time"
)

// v2.17 / v2.22 §6.2 pass-bar thresholds.
const (
	BarControlP99AddedMaxMS = 100.0   // < 100 ms (v2.17)
	BarDeliverPasteP99MaxMS = 8000.0  // <= 8 s (v2.22 §6.2)
	BarSendToReceiptMaxMS   = 10000.0 // <= 10 s (v2.22 §6.2)
	BarCombinedDeliverMaxMS = 18000.0 // < 18 s (v2.22 §6.2)
	Bar429MaxCount          = int64(0)
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
type BarResult struct {
	Name      string  `json:"name"`
	Threshold string  `json:"threshold"`
	Observed  string  `json:"observed"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
	Evaluated bool    `json:"evaluated"`
	Pass      bool    `json:"pass"`
	Note      string  `json:"note,omitempty"`
}

// EvalOptions configures optional pass-bar thresholds for BuildReport.
type EvalOptions struct {
	// MemoryBudgetBytes, when > 0, enforces PeakMemoryBytes <= MemoryBudgetBytes.
	MemoryBudgetBytes int64
	// ExpectDrain, when true, requires at least one planned-drain 4503 observation.
	ExpectDrain bool
	// ClockSkew is recorded in the report metadata for auditability.
	ClockSkew time.Duration
	// HubURL is recorded in the report metadata.
	HubURL string
}

// Report is the machine-readable JSON output emitted by hack/conduit-c19.
type Report struct {
	TimestampUTC string  `json:"timestamp_utc"`
	Mode         Mode    `json:"mode"`
	HubURL       string  `json:"hub_url,omitempty"`
	DurationMS   int64   `json:"duration_ms"`
	AgentCount   int     `json:"agent_count"`
	ClockSkewMS  float64 `json:"clock_skew_ms"`

	Counts struct {
		DeliverAttempts         int64 `json:"deliver_attempts"`
		DeliverSucceeded        int64 `json:"deliver_succeeded"`
		DeliverNonDelivered     int64 `json:"deliver_non_delivered"`
		DeliverErrors           int64 `json:"deliver_errors"`
		LargeBodySucceeded      int64 `json:"large_body_succeeded"`
		LargeBodyErrors         int64 `json:"large_body_errors"`
		RotateTokenSucceeded    int64 `json:"rotate_token_succeeded"`
		RotateTokenErrors       int64 `json:"rotate_token_errors"`
		BrokerRPCSucceeded      int64 `json:"broker_rpc_succeeded"`
		BrokerRPCErrors         int64 `json:"broker_rpc_errors"`
		ControlProbeSucceeded   int64 `json:"control_probe_succeeded"`
		ControlProbeErrors      int64 `json:"control_probe_errors"`
		PTYEchoSucceeded        int64 `json:"pty_echo_succeeded"`
		PTYEchoErrors           int64 `json:"pty_echo_errors"`
		Deliver429s             int64 `json:"deliver_429s"`
		BrokerRPC429s           int64 `json:"broker_rpc_429s"`
		Control429s             int64 `json:"control_429s"`
		Total429s               int64 `json:"total_429s"`
		MaxConcurrentBrokerRPCs int64 `json:"max_concurrent_broker_rpcs"`
		MaxConcurrentControls   int64 `json:"max_concurrent_controls"`
		StalledBulkBytesAtStart int64 `json:"stalled_bulk_bytes_at_start"`
		StalledBulkBytesWritten int64 `json:"stalled_bulk_bytes_written"`
		SessionReconnections    int64 `json:"session_reconnections"`
	} `json:"counts"`

	Latencies struct {
		ControlBaseline   LatencySummaryMS `json:"control_baseline"`
		ControlLoad       LatencySummaryMS `json:"control_load"`
		ControlP99AddedMS float64          `json:"control_p99_added_ms"`
		DeliverEndToEnd   LatencySummaryMS `json:"deliver_end_to_end"`
		DeliverPasteAll   LatencySummaryMS `json:"deliver_paste_all"`
		DeliverPaste512K  LatencySummaryMS `json:"deliver_paste_512k"`
		SendToReceipt     LatencySummaryMS `json:"send_to_receipt_skew_corrected"`
		SendToReceiptRaw  LatencySummaryMS `json:"send_to_receipt_raw"`
		CombinedDeliver   LatencySummaryMS `json:"combined_deliver"`
		RotateToken       LatencySummaryMS `json:"rotate_token"`
		BrokerRPC         LatencySummaryMS `json:"broker_rpc"`
		PTYEcho           LatencySummaryMS `json:"pty_echo"`
	} `json:"latencies"`

	Memory struct {
		PeakBytes   int64          `json:"peak_bytes"`
		BudgetBytes int64          `json:"budget_bytes,omitempty"`
		Samples     []MemorySample `json:"samples,omitempty"`
	} `json:"memory"`

	Drain struct {
		Expected   bool     `json:"expected"`
		Count4503  int64    `json:"count_4503"`
		Reasons    []string `json:"reasons,omitempty"`
		Reconnects int64    `json:"session_reconnections"`
	} `json:"drain"`

	FirstError string      `json:"first_error,omitempty"`
	Bars       []BarResult `json:"bars"`
	Pass       bool        `json:"pass"`
}

// BuildReport converts a raw c19driver.Result into a structured Report and
// evaluates all applicable v2.17 / v2.22 §6.2 pass bars.
func BuildReport(res *Result, opts EvalOptions) Report {
	var r Report
	r.TimestampUTC = time.Now().UTC().Format(time.RFC3339)
	r.Mode = res.Mode
	r.HubURL = opts.HubURL
	r.DurationMS = res.Duration.Milliseconds()
	r.AgentCount = res.AgentCount
	r.ClockSkewMS = durMS(opts.ClockSkew)

	r.Counts.DeliverAttempts = res.DeliverAttempts
	r.Counts.DeliverSucceeded = res.DeliverSucceeded
	r.Counts.DeliverNonDelivered = res.DeliverNonDelivered
	r.Counts.DeliverErrors = res.DeliverErrors
	r.Counts.LargeBodySucceeded = res.LargeBodySucceeded
	r.Counts.LargeBodyErrors = res.LargeBodyErrors
	r.Counts.RotateTokenSucceeded = res.RotateTokenSucceeded
	r.Counts.RotateTokenErrors = res.RotateTokenErrors
	r.Counts.BrokerRPCSucceeded = res.BrokerRPCSucceeded
	r.Counts.BrokerRPCErrors = res.BrokerRPCErrors
	r.Counts.ControlProbeSucceeded = res.ControlProbeSucceeded
	r.Counts.ControlProbeErrors = res.ControlProbeErrors
	r.Counts.PTYEchoSucceeded = res.PTYEchoSucceeded
	r.Counts.PTYEchoErrors = res.PTYEchoErrors
	r.Counts.Deliver429s = res.Deliver429s
	r.Counts.BrokerRPC429s = res.BrokerRPC429s
	r.Counts.Control429s = res.Control429s
	r.Counts.Total429s = res.Total429s
	r.Counts.MaxConcurrentBrokerRPCs = res.MaxConcurrentBrokerRPCs
	r.Counts.MaxConcurrentControls = res.MaxConcurrentControls
	r.Counts.StalledBulkBytesAtStart = res.StalledBulkBytesAtStart
	r.Counts.StalledBulkBytesWritten = res.StalledBulkBytesWritten
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
	r.Latencies.SendToReceipt = summarizeMS(res.SendToReceiptDelays)
	r.Latencies.SendToReceiptRaw = summarizeMS(res.RawSendToReceiptDelays)
	r.Latencies.CombinedDeliver = summarizeMS(res.CombinedDeliverLatencies)
	r.Latencies.RotateToken = summarizeMS(res.RotateLatencies)
	r.Latencies.BrokerRPC = summarizeMS(res.BrokerRPCLatencies)
	r.Latencies.PTYEcho = summarizeMS(res.PTYLatencies)

	r.Memory.PeakBytes = res.PeakMemoryBytes
	r.Memory.BudgetBytes = opts.MemoryBudgetBytes
	r.Memory.Samples = res.MemorySamples

	r.Drain.Expected = opts.ExpectDrain
	r.Drain.Count4503 = res.Drain4503Count
	r.Drain.Reasons = res.Drain4503Reasons
	r.Drain.Reconnects = res.SessionReconnections

	if res.FirstError != nil {
		r.FirstError = res.FirstError.Error()
	}

	r.Bars = evaluateBars(&r, opts)
	r.Pass = true
	for _, b := range r.Bars {
		if b.Evaluated && !b.Pass {
			r.Pass = false
			break
		}
	}
	return r
}

func evaluateBars(r *Report, opts EvalOptions) []BarResult {
	var bars []BarResult

	// 1. Control RPC p99 added vs baseline (< 100 ms, v2.17).
	if r.Latencies.ControlBaseline.Count > 0 && r.Latencies.ControlLoad.Count > 0 {
		val := r.Latencies.ControlP99AddedMS
		bars = append(bars, BarResult{
			Name:      "control_p99_added",
			Threshold: "< 100 ms",
			Observed:  fmt.Sprintf("%.2f ms (base %.2f -> load %.2f)", val, r.Latencies.ControlBaseline.P99MS, r.Latencies.ControlLoad.P99MS),
			Value:     val,
			Unit:      "ms",
			Evaluated: true,
			Pass:      val < BarControlP99AddedMaxMS,
		})
	} else {
		bars = append(bars, BarResult{
			Name:      "control_p99_added",
			Threshold: "< 100 ms",
			Observed:  "n/a",
			Unit:      "ms",
			Evaluated: false,
			Pass:      true,
			Note:      "no baseline/load control samples",
		})
	}

	// 2. 429 count == 0 at default MaxConcurrentRPCs=128 (v2.17 / v2.22 §6.2).
	bars = append(bars, BarResult{
		Name:      "http_429_count",
		Threshold: "== 0",
		Observed:  fmt.Sprintf("%d", r.Counts.Total429s),
		Value:     float64(r.Counts.Total429s),
		Unit:      "count",
		Evaluated: true,
		Pass:      r.Counts.Total429s <= Bar429MaxCount,
	})

	// 3. Deliver-mode bars (v2.22 §6.2).
	if r.Mode == ModeDeliver {
		// Paste time p99 <= 8 s (prefer 512 KiB samples; fall back to all paste samples if none).
		pasteSummary := r.Latencies.DeliverPaste512K
		pasteNote := "512 KiB deliveries"
		if pasteSummary.Count == 0 {
			pasteSummary = r.Latencies.DeliverPasteAll
			pasteNote = "all deliveries (no 512 KiB samples)"
		}
		if pasteSummary.Count > 0 {
			bars = append(bars, BarResult{
				Name:      "deliver_paste_p99",
				Threshold: "<= 8000 ms",
				Observed:  fmt.Sprintf("%.2f ms (n=%d)", pasteSummary.P99MS, pasteSummary.Count),
				Value:     pasteSummary.P99MS,
				Unit:      "ms",
				Evaluated: true,
				Pass:      pasteSummary.P99MS <= BarDeliverPasteP99MaxMS,
				Note:      pasteNote,
			})
		} else {
			bars = append(bars, BarResult{
				Name:      "deliver_paste_p99",
				Threshold: "<= 8000 ms",
				Observed:  "0 samples",
				Unit:      "ms",
				Evaluated: true,
				Pass:      false,
				Note:      "no agent-side paste timings recorded",
			})
		}

		// Skew-corrected send-to-receipt delay max <= 10 s (p99 also recorded).
		if r.Latencies.SendToReceipt.Count > 0 {
			bars = append(bars, BarResult{
				Name:      "send_to_receipt_max",
				Threshold: "<= 10000 ms",
				Observed:  fmt.Sprintf("%.2f ms (p99 %.2f ms, n=%d)", r.Latencies.SendToReceipt.MaxMS, r.Latencies.SendToReceipt.P99MS, r.Latencies.SendToReceipt.Count),
				Value:     r.Latencies.SendToReceipt.MaxMS,
				Unit:      "ms",
				Evaluated: true,
				Pass:      r.Latencies.SendToReceipt.MaxMS <= BarSendToReceiptMaxMS,
			})
		} else {
			bars = append(bars, BarResult{
				Name:      "send_to_receipt_max",
				Threshold: "<= 10000 ms",
				Observed:  "0 samples",
				Unit:      "ms",
				Evaluated: true,
				Pass:      false,
				Note:      "no send-to-receipt timings recorded",
			})
		}

		// Combined per-delivery max < 18 s.
		if r.Latencies.CombinedDeliver.Count > 0 {
			bars = append(bars, BarResult{
				Name:      "combined_deliver_max",
				Threshold: "< 18000 ms",
				Observed:  fmt.Sprintf("%.2f ms (p99 %.2f ms, n=%d)", r.Latencies.CombinedDeliver.MaxMS, r.Latencies.CombinedDeliver.P99MS, r.Latencies.CombinedDeliver.Count),
				Value:     r.Latencies.CombinedDeliver.MaxMS,
				Unit:      "ms",
				Evaluated: true,
				Pass:      r.Latencies.CombinedDeliver.MaxMS < BarCombinedDeliverMaxMS,
			})
		} else {
			bars = append(bars, BarResult{
				Name:      "combined_deliver_max",
				Threshold: "< 18000 ms",
				Observed:  "0 samples",
				Unit:      "ms",
				Evaluated: true,
				Pass:      false,
				Note:      "no combined delivery timings recorded",
			})
		}

		// Zero delivery errors / non-delivered outcomes.
		badDeliveries := r.Counts.DeliverErrors + r.Counts.DeliverNonDelivered
		bars = append(bars, BarResult{
			Name:      "deliver_errors",
			Threshold: "== 0 (succeeded > 0)",
			Observed:  fmt.Sprintf("errors=%d non_delivered=%d succeeded=%d", r.Counts.DeliverErrors, r.Counts.DeliverNonDelivered, r.Counts.DeliverSucceeded),
			Value:     float64(badDeliveries),
			Unit:      "count",
			Evaluated: true,
			Pass:      badDeliveries == 0 && r.Counts.DeliverSucceeded > 0,
		})
	}

	// 4. Broker RPC error bar (when broker RPCs ran).
	if r.Mode == ModeBrokerRPC {
		bars = append(bars, BarResult{
			Name:      "broker_rpc_errors",
			Threshold: "== 0 (succeeded > 0)",
			Observed:  fmt.Sprintf("errors=%d succeeded=%d max_concurrency=%d", r.Counts.BrokerRPCErrors, r.Counts.BrokerRPCSucceeded, r.Counts.MaxConcurrentBrokerRPCs),
			Value:     float64(r.Counts.BrokerRPCErrors),
			Unit:      "count",
			Evaluated: true,
			Pass:      r.Counts.BrokerRPCErrors == 0 && r.Counts.BrokerRPCSucceeded > 0,
		})
	}

	// 5. Aggregate session memory / hub RSS.
	if opts.MemoryBudgetBytes > 0 && r.Memory.PeakBytes > 0 {
		bars = append(bars, BarResult{
			Name:      "hub_rss_peak",
			Threshold: fmt.Sprintf("<= %.1f MiB", float64(opts.MemoryBudgetBytes)/(1024*1024)),
			Observed:  fmt.Sprintf("%.1f MiB", float64(r.Memory.PeakBytes)/(1024*1024)),
			Value:     float64(r.Memory.PeakBytes),
			Unit:      "bytes",
			Evaluated: true,
			Pass:      r.Memory.PeakBytes <= opts.MemoryBudgetBytes,
		})
	} else {
		obs := "not sampled"
		if r.Memory.PeakBytes > 0 {
			obs = fmt.Sprintf("%.1f MiB (no budget)", float64(r.Memory.PeakBytes)/(1024*1024))
		}
		bars = append(bars, BarResult{
			Name:      "hub_rss_peak",
			Threshold: "configured budget",
			Observed:  obs,
			Value:     float64(r.Memory.PeakBytes),
			Unit:      "bytes",
			Evaluated: false,
			Pass:      true,
		})
	}

	// 6. Planned drain 4503 observation.
	if opts.ExpectDrain {
		bars = append(bars, BarResult{
			Name:      "planned_drain_4503",
			Threshold: ">= 1",
			Observed:  fmt.Sprintf("%d (reconnects=%d)", r.Drain.Count4503, r.Drain.Reconnects),
			Value:     float64(r.Drain.Count4503),
			Unit:      "count",
			Evaluated: true,
			Pass:      r.Drain.Count4503 >= 1,
		})
	} else {
		bars = append(bars, BarResult{
			Name:      "planned_drain_4503",
			Threshold: "observed",
			Observed:  fmt.Sprintf("%d (reconnects=%d)", r.Drain.Count4503, r.Drain.Reconnects),
			Value:     float64(r.Drain.Count4503),
			Unit:      "count",
			Evaluated: false,
			Pass:      true,
		})
	}

	return bars
}

// FormatTable renders a concise human-readable summary table of r.
func FormatTable(r Report) string {
	var sb strings.Builder
	verdict := "PASS"
	if !r.Pass {
		verdict = "FAIL"
	}
	fmt.Fprintf(&sb, "C19 Load Report [%s] mode=%s agents=%d duration=%dms max_broker_rpc_concurrency=%d\n",
		verdict, r.Mode, r.AgentCount, r.DurationMS, r.Counts.MaxConcurrentBrokerRPCs)
	tw := tabwriter.NewWriter(&sb, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "BAR\tTHRESHOLD\tOBSERVED\tSTATUS")
	for _, b := range r.Bars {
		status := "INFO"
		if b.Evaluated {
			if b.Pass {
				status = "PASS"
			} else {
				status = "FAIL"
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.Name, b.Threshold, b.Observed, status)
	}
	_ = tw.Flush()
	if r.FirstError != "" {
		fmt.Fprintf(&sb, "First error: %s\n", r.FirstError)
	}
	return sb.String()
}
