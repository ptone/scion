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

package telemetrycontract

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUsageBudgetEvent_MarshalAndValidate(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	winStart := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	winEnd := winStart.AddDate(0, 0, 1)

	env := NewEnvelope(EventTypeUsageBudget, "crossing-1", "hub-1", now, now)
	env.HubID = "hub-1"
	env.ProjectID = "proj-1"

	ev := &UsageBudgetEvent{
		Envelope:       env,
		CrossingID:     "state-1:2",
		StateID:        "state-1",
		LimitName:      "llm_tokens_per_project_day",
		LimitDefID:     "def-1",
		ScopeKind:      "project",
		ScopeID:        "proj-1",
		WindowStartUTC: FormatTS(winStart),
		WindowEndUTC:   FormatTS(winEnd),
		PrevState:      "warn",
		NewState:       "exhausted",
		ThresholdPct:   100,
		Consumed:       100000,
		Cap:            100000,
		TokenAction:    "suspend_and_pause",
		StateVersion:   2,
	}

	raw, err := Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded UsageBudgetEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if decoded.EventType != EventTypeUsageBudget {
		t.Fatalf("event_type = %q, want %q", decoded.EventType, EventTypeUsageBudget)
	}
	if decoded.ThresholdPct != 100 || decoded.TokenAction != "suspend_and_pause" {
		t.Fatalf("unexpected decoded payload: %+v", decoded)
	}
}

func TestUsageBudgetEvent_ValidateRejectsInvalid(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	ev := &UsageBudgetEvent{
		Envelope:       NewEnvelope(EventTypeUsageBudget, "c1", "hub-1", now, now),
		CrossingID:     "c1",
		LimitName:      "llm_tokens_per_project_day",
		ScopeKind:      "project",
		ScopeID:        "proj-1",
		WindowStartUTC: FormatTS(now),
		WindowEndUTC:   FormatTS(now.Add(time.Hour)),
		NewState:       "warn",
		ThresholdPct:   75, // invalid
		Consumed:       10,
		Cap:            100,
		StateVersion:   1,
	}
	if err := ev.Validate(); err == nil {
		t.Fatal("expected error for invalid threshold_pct=75")
	}
}
