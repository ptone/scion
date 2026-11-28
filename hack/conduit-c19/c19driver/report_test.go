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

func TestSummarizeLatencies(t *testing.T) {
	assert.Equal(t, LatencyStats{}, SummarizeLatencies(nil))

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
	assert.Equal(t, 50*time.Millisecond, s.P99)
	assert.Equal(t, 100*time.Millisecond, s.Max)
}

func TestSkewCorrectedDelay(t *testing.T) {
	hubSend := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	notAfterMS := hubSend.Add(agentcontrol.DeliverNotAfter).UnixMilli()

	// Zero skew: receipt 250ms after hubSend -> raw=250ms, corrected=250ms.
	raw, corr, ok := SkewCorrectedDelay(notAfterMS, hubSend.Add(250*time.Millisecond), 0)
	require.True(t, ok)
	assert.Equal(t, 250*time.Millisecond, raw)
	assert.Equal(t, 250*time.Millisecond, corr)

	// Agent clock 500ms ahead of hub: receipt looks 750ms after hubSend -> corrected=250ms.
	raw, corr, ok = SkewCorrectedDelay(notAfterMS, hubSend.Add(750*time.Millisecond), 500*time.Millisecond)
	require.True(t, ok)
	assert.Equal(t, 750*time.Millisecond, raw)
	assert.Equal(t, 250*time.Millisecond, corr)

	// Negative corrected delay clamps to 0.
	raw, corr, ok = SkewCorrectedDelay(notAfterMS, hubSend.Add(100*time.Millisecond), 500*time.Millisecond)
	require.True(t, ok)
	assert.Equal(t, 100*time.Millisecond, raw)
	assert.Equal(t, time.Duration(0), corr)

	// Zero/negative notAfterUnixMS returns ok=false.
	_, _, ok = SkewCorrectedDelay(0, hubSend, 0)
	assert.False(t, ok)
}

func TestBuildReport_AllBarsPass(t *testing.T) {
	res := &Result{
		Mode:                     ModeDeliver,
		Duration:                 10 * time.Second,
		AgentCount:               50,
		DeliverAttempts:          250,
		DeliverSucceeded:         250,
		LargeBodySucceeded:       10,
		BrokerRPCSucceeded:       500,
		MaxConcurrentBrokerRPCs:  64,
		BaselineControlLatencies: []time.Duration{5 * time.Millisecond, 10 * time.Millisecond},
		LoadControlLatencies:     []time.Duration{15 * time.Millisecond, 25 * time.Millisecond},
		DeliverPasteLatencies:    []time.Duration{700 * time.Millisecond, 900 * time.Millisecond},
		LargePasteLatencies:      []time.Duration{1200 * time.Millisecond, 1500 * time.Millisecond},
		SendToReceiptDelays:      []time.Duration{20 * time.Millisecond, 50 * time.Millisecond},
		RawSendToReceiptDelays:   []time.Duration{20 * time.Millisecond, 50 * time.Millisecond},
		CombinedDeliverLatencies: []time.Duration{1220 * time.Millisecond, 1550 * time.Millisecond},
		PeakMemoryBytes:          200 * 1024 * 1024,
		Drain4503Count:           2,
		Drain4503Reasons:         []string{"relay_restart", "relay_restart"},
	}

	rep := BuildReport(res, EvalOptions{
		MemoryBudgetBytes: 512 * 1024 * 1024,
		ExpectDrain:       true,
		HubURL:            "https://lb.test",
	})
	assert.True(t, rep.Pass, "all bars should pass: %+v", rep.Bars)
	assert.InDelta(t, 10.0, rep.Latencies.ControlP99AddedMS, 0.01)

	// Verify JSON round-trip and required fields.
	raw, err := json.Marshal(rep)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, true, decoded["pass"])
	assert.Equal(t, "deliver", decoded["mode"])
	assert.Contains(t, decoded, "latencies")
	assert.Contains(t, decoded, "counts")
	assert.Contains(t, decoded, "memory")
	assert.Contains(t, decoded, "drain")
	assert.Contains(t, decoded, "bars")

	tbl := FormatTable(rep)
	assert.Contains(t, tbl, "[PASS]")
	assert.Contains(t, tbl, "deliver_paste_p99")
	assert.Contains(t, tbl, "send_to_receipt_max")
	assert.Contains(t, tbl, "combined_deliver_max")
	assert.Contains(t, tbl, "planned_drain_4503")
}

func TestBuildReport_BarFailures(t *testing.T) {
	findBar := func(bars []BarResult, name string) BarResult {
		for _, b := range bars {
			if b.Name == name {
				return b
			}
		}
		t.Fatalf("bar %q not found", name)
		return BarResult{}
	}

	res := &Result{
		Mode:                     ModeDeliver,
		Duration:                 5 * time.Second,
		AgentCount:               10,
		DeliverSucceeded:         10,
		Total429s:                3,
		BaselineControlLatencies: []time.Duration{10 * time.Millisecond},
		LoadControlLatencies:     []time.Duration{150 * time.Millisecond}, // +140ms >= 100ms
		LargePasteLatencies:      []time.Duration{9 * time.Second},        // > 8s
		SendToReceiptDelays:      []time.Duration{11 * time.Second},       // > 10s
		CombinedDeliverLatencies: []time.Duration{20 * time.Second},       // >= 18s
		PeakMemoryBytes:          600 * 1024 * 1024,
	}

	rep := BuildReport(res, EvalOptions{
		MemoryBudgetBytes: 512 * 1024 * 1024,
		ExpectDrain:       true,
	})
	assert.False(t, rep.Pass)
	assert.False(t, findBar(rep.Bars, "control_p99_added").Pass)
	assert.False(t, findBar(rep.Bars, "http_429_count").Pass)
	assert.False(t, findBar(rep.Bars, "deliver_paste_p99").Pass)
	assert.False(t, findBar(rep.Bars, "send_to_receipt_max").Pass)
	assert.False(t, findBar(rep.Bars, "combined_deliver_max").Pass)
	assert.False(t, findBar(rep.Bars, "hub_rss_peak").Pass)
	assert.False(t, findBar(rep.Bars, "planned_drain_4503").Pass)
	assert.Contains(t, FormatTable(rep), "[FAIL]")
}
