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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agentcontrol"
)

func findBar(t *testing.T, bars []BarResult, name string) BarResult {
	t.Helper()
	for _, b := range bars {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("bar %q not found", name)
	return BarResult{}
}

func TestSummarizeLatencies(t *testing.T) {
	assert.Equal(t, LatencyStats{}, SummarizeLatencies(nil))

	// Nearest-rank: idx = ceil(p*n) - 1.
	// For n=5: p50 -> ceil(2.5)-1 = 2 (30ms); p99 -> ceil(4.95)-1 = 4 (100ms).
	samples := []time.Duration{
		50 * time.Millisecond,
		10 * time.Millisecond,
		100 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
	}
	s := SummarizeLatencies(samples)
	assert.Equal(t, 5, s.Count)
	assert.Equal(t, 30*time.Millisecond, s.P50)
	assert.Equal(t, 100*time.Millisecond, s.P99)
	assert.Equal(t, 100*time.Millisecond, s.Max)
}

func TestSkewCorrectedDelay(t *testing.T) {
	hubSend := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	notAfterMS := hubSend.Add(agentcontrol.DeliverNotAfter).UnixMilli()

	// Zero skew: receipt 250ms after hubSend -> raw=250ms, corrected=250ms, clamped=false.
	raw, corr, clamped, ok := SkewCorrectedDelay(notAfterMS, hubSend.Add(250*time.Millisecond), 0)
	require.True(t, ok)
	assert.False(t, clamped)
	assert.Equal(t, 250*time.Millisecond, raw)
	assert.Equal(t, 250*time.Millisecond, corr)

	// Agent clock 500ms ahead of hub: receipt looks 750ms after hubSend -> corrected=250ms.
	raw, corr, clamped, ok = SkewCorrectedDelay(notAfterMS, hubSend.Add(750*time.Millisecond), 500*time.Millisecond)
	require.True(t, ok)
	assert.False(t, clamped)
	assert.Equal(t, 750*time.Millisecond, raw)
	assert.Equal(t, 250*time.Millisecond, corr)

	// Negative corrected delay clamps to 0 and reports clamped=true.
	raw, corr, clamped, ok = SkewCorrectedDelay(notAfterMS, hubSend.Add(100*time.Millisecond), 500*time.Millisecond)
	require.True(t, ok)
	assert.True(t, clamped)
	assert.Equal(t, 100*time.Millisecond, raw)
	assert.Equal(t, time.Duration(0), corr)

	// Zero/negative notAfterUnixMS returns ok=false.
	_, _, _, ok = SkewCorrectedDelay(0, hubSend, 0)
	assert.False(t, ok)
}

func TestBuildReport_AllBarsPass(t *testing.T) {
	res := &Result{
		Mode:                           ModeDeliver,
		RunTag:                         "c19-test-1",
		Duration:                       10 * time.Second,
		AgentCount:                     50,
		ExpectedLargeBodyCount:         2,
		ExpectedRotateTokenCount:       5,
		BrokerRPCEnabled:               true,
		DeliverAttempts:                250,
		DeliverSucceeded:               250,
		LargeBodySucceeded:             2,
		RotateTokenSucceeded:           5,
		BrokerRPCSucceeded:             500,
		MaxConcurrentBrokerRPCs:        128,
		MaxConcurrentBrokerSessionRPCs: 120,
		BaselineControlLatencies:       []time.Duration{5 * time.Millisecond, 10 * time.Millisecond},
		LoadControlLatencies:           []time.Duration{15 * time.Millisecond, 25 * time.Millisecond},
		DeliverPasteLatencies:          []time.Duration{700 * time.Millisecond, 900 * time.Millisecond},
		LargePasteLatencies:            []time.Duration{1200 * time.Millisecond, 1500 * time.Millisecond},
		SendToReceiptDelays:            []time.Duration{20 * time.Millisecond, 50 * time.Millisecond},
		RawSendToReceiptDelays:         []time.Duration{20 * time.Millisecond, 50 * time.Millisecond},
		CombinedDeliverLatencies:       []time.Duration{1220 * time.Millisecond, 1550 * time.Millisecond},
		PeakMemoryBytes:                200 * 1024 * 1024,
		MemorySource:                   "proc_hub_pids",
		MemoryPIDCount:                 2,
		Drain4503Count:                 2,
		Drain4503RelayRestartCount:     2,
		Drain4503Reasons:               []string{"relay_restart", "relay_restart"},
	}

	rep := BuildReport(res, EvalOptions{
		MemoryBudgetBytes:        512 * 1024 * 1024,
		ExpectDrain:              true,
		HubURL:                   "https://lb.test",
		MessageRowsCreated:       314,
		LargeBodyRowsPerDelivery: 33,
	})
	assert.Equal(t, VerdictPass, rep.Verdict)
	assert.True(t, rep.Pass, "all bars should pass: %+v", rep.Bars)
	// Nearest-rank p99 of [5ms, 10ms] = 10ms; [15ms, 25ms] = 25ms -> added = 15ms.
	assert.InDelta(t, 15.0, rep.Latencies.ControlP99AddedMS, 0.01)
	assert.Contains(t, findBar(t, rep.Bars, BarHubRSSPeak).Observed, "source=proc_hub_pids, pids=2")
	assert.Equal(t, int64(314), rep.Counts.MessageRowsCreated)
	assert.Equal(t, 33, rep.Counts.LargeBodyRowsPerDelivery)
	assert.Equal(t, int64(120), rep.Counts.MaxConcurrentBrokerSessionRPCs)

	raw, err := json.Marshal(rep)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, true, decoded["pass"])
	assert.Equal(t, "PASS", decoded["verdict"])
	assert.Equal(t, "deliver", decoded["mode"])
	assert.Equal(t, "c19-test-1", decoded["run_tag"])
	assert.Contains(t, decoded, "latencies")
	assert.Contains(t, decoded, "counts")
	assert.Contains(t, decoded, "memory")
	assert.Contains(t, decoded, "drain")
	assert.Contains(t, decoded, "bars")

	tbl := FormatTable(rep)
	assert.Contains(t, tbl, "[PASS]")
	assert.Contains(t, tbl, BarDeliverPasteP99)
	assert.Contains(t, tbl, BarSendToReceiptMax)
	assert.Contains(t, tbl, BarCombinedDeliver)
	assert.Contains(t, tbl, BarPlannedDrain4503)
	assert.Contains(t, tbl, BarBrokerRPCErrors)
}

// R1: Missing required bars must produce INCOMPLETE (never PASS) and never set
// pass=true on an unevaluated bar.
func TestBuildReport_UnevaluatedRequiredBarsIncomplete(t *testing.T) {
	res := &Result{
		Mode:                     ModeDeliver,
		Duration:                 5 * time.Second,
		AgentCount:               10,
		ExpectedLargeBodyCount:   2,
		DeliverSucceeded:         10,
		LargePasteLatencies:      []time.Duration{500 * time.Millisecond, 600 * time.Millisecond},
		SendToReceiptDelays:      []time.Duration{10 * time.Millisecond},
		CombinedDeliverLatencies: []time.Duration{610 * time.Millisecond},
		// No BaselineControlLatencies, no MemorySamples, no Drain4503, no RotateToken.
	}

	rep := BuildReport(res, EvalOptions{})
	assert.Equal(t, VerdictIncomplete, rep.Verdict)
	assert.False(t, rep.Pass)
	for _, b := range rep.Bars {
		if !b.Evaluated {
			assert.False(t, b.Pass, "unevaluated bar %q must never have pass=true", b.Name)
		}
	}
	assert.False(t, findBar(t, rep.Bars, BarControlP99Added).Evaluated)
	assert.False(t, findBar(t, rep.Bars, BarHubRSSPeak).Evaluated)
	assert.False(t, findBar(t, rep.Bars, BarPlannedDrain4503).Evaluated)
	rotBar := findBar(t, rep.Bars, BarRotateTokenMix)
	assert.False(t, rotBar.Evaluated)
	assert.Equal(t, "not exercised: no Hub rotation route", rotBar.Observed)
	assert.Contains(t, FormatTable(rep), "[INCOMPLETE]")

	// Explicitly waiving those unevaluated bars is echoed in report and allows PASS.
	repWaived := BuildReport(res, EvalOptions{
		AllowUnevaluated: []string{BarControlP99Added, BarHubRSSPeak, BarPlannedDrain4503, BarRotateTokenMix},
	})
	assert.Equal(t, VerdictPass, repWaived.Verdict)
	assert.True(t, repWaived.Pass)
	assert.Equal(t, []string{BarControlP99Added, BarHubRSSPeak, BarPlannedDrain4503, BarRotateTokenMix}, repWaived.AllowUnevaluated)
	for _, b := range repWaived.Bars {
		if !b.Evaluated {
			assert.False(t, b.Pass, "waived unevaluated bar %q must still have pass=false", b.Name)
			assert.True(t, b.Waived)
		}
	}
}

// R2: Nearest-rank p99 must not drop the worst sample at small n.
// Nine 1s pastes + one 9s paste of 512 KiB must FAIL the <= 8s bar.
func TestBuildReport_NearestRankP99CatchesSmallNTail(t *testing.T) {
	pastes := make([]time.Duration, 10)
	for i := 0; i < 9; i++ {
		pastes[i] = time.Second
	}
	pastes[9] = 9 * time.Second

	res := &Result{
		Mode:                     ModeDeliver,
		Duration:                 10 * time.Second,
		AgentCount:               10,
		ExpectedLargeBodyCount:   10,
		DeliverSucceeded:         10,
		LargePasteLatencies:      pastes,
		SendToReceiptDelays:      []time.Duration{10 * time.Millisecond},
		CombinedDeliverLatencies: []time.Duration{9010 * time.Millisecond},
	}
	rep := BuildReport(res, EvalOptions{
		AllowUnevaluated: []string{BarControlP99Added, BarHubRSSPeak, BarPlannedDrain4503, BarRotateTokenMix},
	})
	assert.Equal(t, VerdictFail, rep.Verdict)
	assert.False(t, rep.Pass)
	pasteBar := findBar(t, rep.Bars, BarDeliverPasteP99)
	assert.True(t, pasteBar.Evaluated)
	assert.False(t, pasteBar.Pass)
	assert.InDelta(t, 9000.0, pasteBar.Value, 0.01)
}

// R3: 512 KiB paste bar must never fall back to small bodies and must fail if
// fewer than ExpectedLargeBodyCount 512 KiB samples were recorded.
func TestBuildReport_512KPasteDoesNotFallbackToSmallBodies(t *testing.T) {
	baseRes := Result{
		Mode:                     ModeDeliver,
		Duration:                 5 * time.Second,
		AgentCount:               10,
		ExpectedLargeBodyCount:   4,
		DeliverSucceeded:         20,
		DeliverPasteLatencies:    []time.Duration{50 * time.Millisecond, 60 * time.Millisecond},
		SendToReceiptDelays:      []time.Duration{10 * time.Millisecond},
		CombinedDeliverLatencies: []time.Duration{70 * time.Millisecond},
	}
	opts := EvalOptions{
		AllowUnevaluated: []string{BarControlP99Added, BarHubRSSPeak, BarPlannedDrain4503, BarRotateTokenMix},
	}

	// Case 1: zero 512 KiB samples -> FAIL even though small pastes are 50ms.
	repEmpty := BuildReport(&baseRes, opts)
	assert.Equal(t, VerdictFail, repEmpty.Verdict)
	assert.False(t, findBar(t, repEmpty.Bars, BarDeliverPasteP99).Pass)

	// Case 2: 2 of 4 configured 512 KiB samples -> FAIL.
	partial := baseRes
	partial.LargePasteLatencies = []time.Duration{500 * time.Millisecond, 600 * time.Millisecond}
	repPartial := BuildReport(&partial, opts)
	assert.Equal(t, VerdictFail, repPartial.Verdict)
	assert.False(t, findBar(t, repPartial.Bars, BarDeliverPasteP99).Pass)
}

// R4 + R6 + Consider (drain reason): broker-RPC errors fail deliver mode,
// memory sample errors fail hub_rss_peak, and non-relay_restart 4503s do not
// satisfy planned_drain_4503.
func TestBuildReport_BarFailures(t *testing.T) {
	res := &Result{
		Mode:                     ModeDeliver,
		Duration:                 5 * time.Second,
		AgentCount:               10,
		ExpectedLargeBodyCount:   1,
		BrokerRPCEnabled:         true,
		DeliverSucceeded:         10,
		BrokerRPCSucceeded:       5,
		BrokerRPCErrors:          2,
		Total429s:                3,
		BrokerRPC429s:            3,
		BaselineControlLatencies: []time.Duration{10 * time.Millisecond},
		LoadControlLatencies:     []time.Duration{150 * time.Millisecond}, // +140ms >= 100ms
		LargePasteLatencies:      []time.Duration{9 * time.Second},        // > 8s
		SendToReceiptDelays:      []time.Duration{11 * time.Second},       // > 10s
		CombinedDeliverLatencies: []time.Duration{20 * time.Second},       // >= 18s
		PeakMemoryBytes:          600 * 1024 * 1024,
		MemorySource:             "proc_hub_pids",
		MemoryPIDCount:           2,
		MemorySampleErrors:       1,
		Drain4503Count:           1,
		Drain4503OtherCount:      1,
		Drain4503OtherReasons:    []string{"broker_disconnected"},
	}

	rep := BuildReport(res, EvalOptions{
		MemoryBudgetBytes: 512 * 1024 * 1024,
		ExpectDrain:       true,
	})
	assert.Equal(t, VerdictFail, rep.Verdict)
	assert.False(t, rep.Pass)
	assert.False(t, findBar(t, rep.Bars, BarControlP99Added).Pass)
	assert.False(t, findBar(t, rep.Bars, BarHTTP429Count).Pass)
	assert.False(t, findBar(t, rep.Bars, BarDeliverPasteP99).Pass)
	assert.False(t, findBar(t, rep.Bars, BarSendToReceiptMax).Pass)
	assert.False(t, findBar(t, rep.Bars, BarCombinedDeliver).Pass)
	assert.False(t, findBar(t, rep.Bars, BarBrokerRPCErrors).Pass)
	assert.False(t, findBar(t, rep.Bars, BarHubRSSPeak).Pass)
	assert.False(t, findBar(t, rep.Bars, BarPlannedDrain4503).Pass)
	assert.Equal(t, int64(1), rep.Drain.OtherCount)
	assert.Equal(t, int64(0), rep.Drain.RelayRestartCount)
	assert.Contains(t, FormatTable(rep), "[FAIL]")
}
