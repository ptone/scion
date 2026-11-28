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
	"errors"
	"fmt"
)

// UsageBudgetEvent is one usage.budget analytics event emitted by the Hub when
// a raw-token budget crosses a soft (50%, 80%) or hard (100%) threshold.
type UsageBudgetEvent struct {
	Envelope

	CrossingID     string `json:"crossing_id"`
	StateID        string `json:"state_id,omitempty"`
	LimitName      string `json:"limit_name"`
	LimitDefID     string `json:"limit_def_id,omitempty"`
	ScopeKind      string `json:"scope_kind"`
	ScopeID        string `json:"scope_id"`
	ProjectScopeID string `json:"project_scope_id,omitempty"`
	WindowStartUTC string `json:"window_start_utc"`
	WindowEndUTC   string `json:"window_end_utc"`
	PrevState      string `json:"prev_state,omitempty"`
	NewState       string `json:"new_state"`
	ThresholdPct   int    `json:"threshold_pct"`
	Consumed       int64  `json:"consumed"`
	Cap            int64  `json:"cap"`
	TokenAction    string `json:"token_action"`
	StateVersion   int64  `json:"state_version"`
}

// Validate checks UsageBudgetEvent invariants in addition to Envelope.Validate.
func (e *UsageBudgetEvent) Validate() error {
	if e == nil {
		return errors.New("telemetrycontract: nil usage budget event")
	}
	var errs []error
	if err := e.Envelope.Validate(); err != nil {
		errs = append(errs, err)
	}
	if e.EventType != EventTypeUsageBudget {
		errs = append(errs, fmt.Errorf("event_type = %q, want %q", e.EventType, EventTypeUsageBudget))
	}
	if e.CrossingID == "" {
		errs = append(errs, errors.New("crossing_id is empty"))
	}
	if e.LimitName == "" {
		errs = append(errs, errors.New("limit_name is empty"))
	}
	if e.ScopeKind == "" {
		errs = append(errs, errors.New("scope_kind is empty"))
	}
	if e.ScopeID == "" {
		errs = append(errs, errors.New("scope_id is empty"))
	}
	if err := ValidateTS(e.WindowStartUTC); err != nil {
		errs = append(errs, fmt.Errorf("window_start_utc: %w", err))
	}
	if err := ValidateTS(e.WindowEndUTC); err != nil {
		errs = append(errs, fmt.Errorf("window_end_utc: %w", err))
	}
	if e.NewState == "" {
		errs = append(errs, errors.New("new_state is empty"))
	}
	switch e.ThresholdPct {
	case 50, 80, 100:
	default:
		errs = append(errs, fmt.Errorf("threshold_pct = %d, want 50, 80, or 100", e.ThresholdPct))
	}
	if e.Consumed < 0 {
		errs = append(errs, fmt.Errorf("consumed = %d, must be >= 0", e.Consumed))
	}
	if e.Cap <= 0 {
		errs = append(errs, fmt.Errorf("cap = %d, must be > 0", e.Cap))
	}
	if e.StateVersion < 1 {
		errs = append(errs, fmt.Errorf("state_version = %d, must be >= 1", e.StateVersion))
	}
	if len(errs) > 0 {
		return fmt.Errorf("telemetrycontract: invalid usage budget event: %w", errors.Join(errs...))
	}
	return nil
}
