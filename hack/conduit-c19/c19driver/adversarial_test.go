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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func healthyDeliverResult() (*Result, EvalOptions) {
	now := time.Now()
	res := &Result{
		Mode:                           ModeDeliver,
		Duration:                       30 * time.Second,
		DeliverInterval:                2 * time.Second,
		AgentCount:                     50,
		ExpectedLargeBodyCount:         10,
		ExpectedBrokerRPCConcurrency:   128,
		EffectiveMaxConcurrentRPCs:     128,
		BrokerRPCEnabled:               true,
		DeliverAttempts:                760,
		DeliverPeriodicAttempts:        750,
		DeliverSucceeded:               760,
		LargeBodySucceeded:             10,
		LargeBodyMinBytesObserved:      509000,
		LargeBodyMaxBytesObserved:      511000,
		BrokerRPCSucceeded:             500,
		ControlProbeSucceeded:          120,
		PTYEchoSucceeded:               40,
		MaxConcurrentBrokerRPCs:        128,
		MaxConcurrentBrokerSessionRPCs: 128,
		BaselineControlLatencies:       []time.Duration{5 * time.Millisecond, 6 * time.Millisecond},
		LoadControlLatencies:           []time.Duration{8 * time.Millisecond, 9 * time.Millisecond},
		DeliverPasteLatencies:          []time.Duration{700 * time.Millisecond},
		LargePasteLatencies: []time.Duration{
			time.Second, time.Second, time.Second, time.Second, time.Second,
			time.Second, time.Second, time.Second, time.Second, time.Second,
		},
		SendToReceiptDelays:          []time.Duration{500 * time.Millisecond},
		CombinedDeliverLatencies:     []time.Duration{1500 * time.Millisecond},
		PeakMemoryBytes:              256 * 1024 * 1024,
		MemorySource:                 "proc_hub_pids",
		MemoryPIDCount:               2,
		DrainTriggeredAt:             now.Add(-35 * time.Second),
		Drain4503Count:               2,
		Drain4503RelayRestartCount:   2,
		Drain4503Delays:              []time.Duration{29 * time.Second},
		DrainAcceptedStreamAttempted: true,
		DrainAcceptedStreamFinished:  true,
		PostDrainWorkSucceeded:       10,
		PostDrainReplacementWork:     4,
		SessionReconnections:         2,
	}
	opts := EvalOptions{
		MemoryBudgetBytes:      512 * 1024 * 1024,
		ExpectDrain:            true,
		DrainDeadline:          30 * time.Second,
		DrainDeadlineTolerance: 5 * time.Second,
		MessageRowsCreated:     1080,
		PGRowsChecked:          true,
		PGRowsTotal:            1080,
		PGRowsDelivered:        1080,
		AllowUnevaluated:       []string{BarRotateTokenMix, BarPlannedDrainRefusesNew},
	}
	return res, opts
}

// TestAdversarial_DegradedInputsNeverPass verifies that every pass bar in
// hack/conduit-c19/BARS.md catches degraded inputs and yields FAIL or
// INCOMPLETE (never PASS).
func TestAdversarial_DegradedInputsNeverPass(t *testing.T) {
	// Baseline sanity check: healthy inputs must PASS.
	baseRes, baseOpts := healthyDeliverResult()
	baseRep := BuildReport(baseRes, baseOpts)
	require.Equal(t, VerdictPass, baseRep.Verdict, "healthy baseline must PASS:\n%s", FormatTable(baseRep))

	cases := []struct {
		name        string
		wantVerdict string // VerdictFail or VerdictIncomplete
		degrade     func(res *Result, opts *EvalOptions)
	}{
		{
			name:        "baseline_control_probe_errors",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.BaselineControlErrors = 1
			},
		},
		{
			name:        "control_probe_errors_dropped_behind_fast_successes",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.ControlProbeSucceeded = 2
				res.ControlProbeErrors = 118
				res.LoadControlLatencies = []time.Duration{7 * time.Millisecond, 8 * time.Millisecond}
			},
		},
		{
			name:        "pty_echo_errors_during_load",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.PTYEchoSucceeded = 10
				res.PTYEchoErrors = 40
			},
		},
		{
			name:        "session_stall_zero_attempts_is_incomplete",
			wantVerdict: VerdictIncomplete,
			degrade: func(res *Result, _ *EvalOptions) {
				res.BaselineControlLatencies = nil
				res.LoadControlLatencies = nil
				res.ControlProbeSucceeded = 0
				res.PTYEchoSucceeded = 0
			},
		},
		{
			name:        "control_probe_missing_load_samples",
			wantVerdict: VerdictIncomplete,
			degrade: func(res *Result, _ *EvalOptions) {
				res.LoadControlLatencies = nil
			},
		},
		{
			name:        "control_probe_missing_baseline_samples",
			wantVerdict: VerdictIncomplete,
			degrade: func(res *Result, _ *EvalOptions) {
				res.BaselineControlLatencies = nil
			},
		},
		{
			name:        "control_p99_added_at_or_above_100ms",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.LoadControlLatencies = []time.Duration{106 * time.Millisecond}
			},
		},
		{
			name:        "http_429_direct_deliver",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.Deliver429s = 1
				res.Total429s = 1
			},
		},
		{
			name:        "http_429_broker_session_surfaced_as_502",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.BrokerRPC429s = 1
				res.Total429s = 1
			},
		},
		{
			name:        "http_429_retried_in_flusher_hidden_behind_200",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.FlusherDeliver429s = 1
			},
		},
		{
			name:        "deliver_paste_512k_unrecorded_payload_size",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.LargeBodyMinBytesObserved = 0
				res.LargeBodyMaxBytesObserved = 0
			},
		},
		{
			name:        "deliver_paste_512k_undersized_payload",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.LargeBodyMinBytesObserved = 100 * 1024
				res.LargeBodyMaxBytesObserved = 100 * 1024
			},
		},
		{
			name:        "deliver_paste_512k_zero_samples",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.LargePasteLatencies = nil
			},
		},
		{
			name:        "deliver_paste_512k_fewer_than_expected",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.LargePasteLatencies = []time.Duration{time.Second, time.Second}
			},
		},
		{
			name:        "deliver_paste_512k_nearest_rank_p99_exceeds_8s",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.LargePasteLatencies[9] = 9 * time.Second
			},
		},
		{
			name:        "deliver_paste_512k_large_body_error",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.LargeBodyErrors = 1
				res.DeliverErrors = 1
			},
		},
		{
			name:        "send_to_receipt_zero_samples",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.SendToReceiptDelays = nil
			},
		},
		{
			name:        "send_to_receipt_exceeds_10s",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.SendToReceiptDelays = []time.Duration{10500 * time.Millisecond}
			},
		},
		{
			name:        "combined_deliver_zero_samples",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.CombinedDeliverLatencies = nil
			},
		},
		{
			name:        "combined_deliver_at_or_above_18s",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.CombinedDeliverLatencies = []time.Duration{18 * time.Second}
			},
		},
		{
			name:        "deliver_non_delivered_outcome",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.DeliverNonDelivered = 1
			},
		},
		{
			name:        "deliver_zero_succeeded",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.DeliverSucceeded = 0
			},
		},
		{
			name:        "offered_load_closed_loop_slowdown",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.DeliverAttempts = 20
				res.DeliverPeriodicAttempts = 10
				res.DeliverSucceeded = 20
			},
		},
		{
			name:        "offered_load_large_bodies_cannot_inflate_periodic_rate",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.DeliverAttempts = 750
				res.DeliverPeriodicAttempts = 600 // < 90% of 750 = 675
			},
		},
		{
			name:        "pg_rows_delivered_unwaived_without_dsn_is_incomplete",
			wantVerdict: VerdictIncomplete,
			degrade: func(_ *Result, opts *EvalOptions) {
				opts.PGRowsChecked = false
			},
		},
		{
			name:        "pg_rows_delivered_row_not_delivered_fails_despite_200_http",
			wantVerdict: VerdictFail,
			degrade: func(_ *Result, opts *EvalOptions) {
				opts.PGRowsDelivered = opts.MessageRowsCreated - 1
			},
		},
		{
			name:        "pg_rows_delivered_query_error_fails",
			wantVerdict: VerdictFail,
			degrade: func(_ *Result, opts *EvalOptions) {
				opts.PGRowsQueryErr = "connection refused"
			},
		},
		{
			name:        "broker_rpc_error_nonzero",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.BrokerRPCErrors = 1
			},
		},
		{
			name:        "broker_rpc_zero_succeeded",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.BrokerRPCSucceeded = 0
			},
		},
		{
			name:        "broker_session_concurrency_vacuous_latency_zero",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.MaxConcurrentBrokerSessionRPCs = 1
			},
		},
		{
			name:        "rotate_token_mix_unwaived",
			wantVerdict: VerdictIncomplete,
			degrade: func(_ *Result, opts *EvalOptions) {
				opts.AllowUnevaluated = nil
			},
		},
		{
			name:        "rotate_token_mix_exercised_with_error",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, opts *EvalOptions) {
				opts.AllowUnevaluated = nil
				res.ExpectedRotateTokenCount = 5
				res.RotateTokenSucceeded = 4
				res.RotateTokenErrors = 1
			},
		},
		{
			name:        "hub_rss_over_budget",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.PeakMemoryBytes = 600 * 1024 * 1024
			},
		},
		{
			name:        "hub_rss_unreadable_non_drained_pid",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.MemorySampleErrors = 1
			},
		},
		{
			name:        "hub_rss_missing_sampler_or_budget",
			wantVerdict: VerdictIncomplete,
			degrade: func(res *Result, opts *EvalOptions) {
				res.PeakMemoryBytes = 0
				opts.MemoryBudgetBytes = 0
			},
		},
		{
			name:        "planned_drain_zero_readmitted_sessions",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.SessionReconnections = 0
				res.PostDrainWorkSucceeded = 50
				res.PostDrainReplacementWork = 0
			},
		},
		{
			name:        "planned_drain_reconnected_but_zero_replacement_work",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.SessionReconnections = 2
				res.PostDrainWorkSucceeded = 50
				res.PostDrainReplacementWork = 0
			},
		},
		{
			name:        "planned_drain_4503_exceeds_deadline",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.Drain4503Delays = []time.Duration{40 * time.Second}
			},
		},
		{
			name:        "planned_drain_only_non_relay_restart_4503",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.Drain4503RelayRestartCount = 0
				res.Drain4503OtherCount = 2
			},
		},
		{
			name:        "planned_drain_immediate_kill_before_deadline",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.Drain4503Delays = []time.Duration{100 * time.Millisecond}
			},
		},
		{
			name:        "planned_drain_accepted_stream_failed_during_drain",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.DrainAcceptedStreamFinished = false
			},
		},
		{
			name:        "planned_drain_accepted_stream_not_attempted_is_incomplete",
			wantVerdict: VerdictIncomplete,
			degrade: func(res *Result, _ *EvalOptions) {
				res.DrainAcceptedStreamAttempted = false
				res.DrainAcceptedStreamFinished = false
			},
		},
		{
			name:        "planned_drain_refuses_new_streams_unwaived_is_incomplete",
			wantVerdict: VerdictIncomplete,
			degrade: func(_ *Result, opts *EvalOptions) {
				opts.AllowUnevaluated = []string{BarRotateTokenMix}
			},
		},
		{
			name:        "broker_session_concurrency_low_rpc_concurrency_vs_default_128",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				res.ExpectedBrokerRPCConcurrency = 4
				res.MaxConcurrentBrokerRPCs = 4
				res.MaxConcurrentBrokerSessionRPCs = 4
			},
		},
		{
			name:        "http_429_non_default_max_concurrent_rpcs_is_incomplete",
			wantVerdict: VerdictIncomplete,
			degrade: func(res *Result, _ *EvalOptions) {
				res.EffectiveMaxConcurrentRPCs = 1000
			},
		},
		{
			name:        "send_to_receipt_clamped_negative_is_incomplete",
			wantVerdict: VerdictIncomplete,
			degrade: func(res *Result, _ *EvalOptions) {
				res.SendToReceiptClamped = 1
			},
		},
		{
			name:        "drain_window_surviving_hub_probe_error_not_excused",
			wantVerdict: VerdictFail,
			degrade: func(res *Result, _ *EvalOptions) {
				// r3-R1: errors on agents owned by the surviving hub ("hub-a")
				// during the drain of "hub-b" must NEVER be excused.
				if ShouldExcuseDrainError(true, "hub-a", "hub-b") {
					res.DrainExcusedControlErrors = 1
				} else {
					res.ControlProbeErrors = 1
				}
			},
		},
		{
			name:        "planned_drain_missing_trigger_is_incomplete",
			wantVerdict: VerdictIncomplete,
			degrade: func(res *Result, _ *EvalOptions) {
				res.DrainTriggeredAt = time.Time{}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, opts := healthyDeliverResult()
			tc.degrade(res, &opts)
			rep := BuildReport(res, opts)
			assert.False(t, rep.Pass, "degraded case %s must NEVER pass:\n%s", tc.name, FormatTable(rep))
			assert.Equal(t, tc.wantVerdict, rep.Verdict, "degraded case %s verdict mismatch:\n%s", tc.name, FormatTable(rep))
		})
	}
}
