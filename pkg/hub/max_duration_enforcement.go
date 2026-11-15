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

package hub

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

const (
	// maxDurationEnforcementGrace gives sciontool's in-container timer and
	// shutdown room to finish before the hub stops the run directly.
	maxDurationEnforcementGrace      = 10 * time.Minute
	maxDurationEnforcementPerTickCap = 50
	runStartedRecordTimeout          = 5 * time.Second
)

func recordAgentRunStartedBestEffort(ctx context.Context, st store.Store, log *slog.Logger, agentID, runID string, row *store.Agent) {
	if st == nil || agentID == "" || runID == "" {
		return
	}
	now := time.Now().UTC()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runStartedRecordTimeout)
	defer cancel()
	written, err := st.RecordAgentRunStarted(rctx, agentID, runID, now)
	if err != nil {
		if log != nil {
			log.Warn("Failed to record run_started_at", "agent_id", agentID, "run_id", runID, "error", err)
		}
		return
	}
	if written && row != nil && row.RunID == runID {
		row.RunStartedAt, row.RunStartedAtRunID = &now, runID
	}
}

func (d *HTTPAgentDispatcher) recordRunStarted(ctx context.Context, agent *store.Agent, runID string) {
	if d != nil && agent != nil {
		recordAgentRunStartedBestEffort(ctx, d.store, d.log, agent.ID, runID, agent)
	}
}

func (s *Server) recordLaunchedRunStarted(ctx context.Context, agentID, runID string, row *store.Agent) {
	if s != nil {
		recordAgentRunStartedBestEffort(ctx, s.store, s.agentLifecycleLog, agentID, runID, row)
	}
}

func parseMaxDurationRung(raw string) (dur time.Duration, unlimited, ok bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return 0, false, false
	}
	if v == "0" {
		return 0, true, true
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, false, false
	}
	return max(d, 0), d <= 0, true
}

// resolveAgentMaxDuration resolves inline/project > hub template > hub default > 24h.
func (s *Server) resolveAgentMaxDuration(ctx context.Context, a *store.Agent, hubDefaults opsettings.AgentDefaultsSettings, tmplCache map[string]*api.ScionConfig) (time.Duration, bool) {
	if a.AppliedConfig != nil && a.AppliedConfig.InlineConfig != nil {
		if d, unlimited, ok := parseMaxDurationRung(a.AppliedConfig.InlineConfig.MaxDuration); ok {
			return d, !unlimited
		}
	}
	if a.AppliedConfig != nil && a.AppliedConfig.TemplateID != "" {
		tid := a.AppliedConfig.TemplateID
		jc, cached := tmplCache[tid]
		if !cached {
			jc = s.templateJobConfigByID(ctx, tid)
			tmplCache[tid] = jc
		}
		if jc != nil {
			if d, unlimited, ok := parseMaxDurationRung(jc.MaxDuration); ok {
				return d, !unlimited
			}
		}
	}
	if d, unlimited, ok := parseMaxDurationRung(hubDefaults.DefaultMaxDuration); ok {
		return d, !unlimited
	}
	return config.DefaultHubMaxDurationValue, true
}

func (s *Server) agentMaxDurationEnforcementHandler() func(context.Context) {
	return func(ctx context.Context) { s.enforceMaxDurationAt(ctx, time.Now()) }
}

func (s *Server) enforceMaxDurationAt(ctx context.Context, now time.Time) {
	if !s.experimentEnabled(experiments.MaxDurationEnforcement) {
		return
	}
	agents, err := s.store.ListActiveAgentsForMaxDuration(ctx)
	if err != nil {
		s.agentLifecycleLog.Error("max_duration enforcement: failed to list active agents", "error", err)
		return
	}
	if len(agents) == 0 {
		return
	}
	hubDefaults, tmplCache, stopped := s.hubAgentDefaults(), make(map[string]*api.ScionConfig), 0
	for i := range agents {
		if stopped >= maxDurationEnforcementPerTickCap {
			break
		}
		a := &agents[i]
		if a.RunID == "" || a.RunStartedAt == nil || a.RunStartedAtRunID != a.RunID || a.RuntimeBrokerID == "" || systemStopBlocked(a) {
			continue
		}
		maxDur, ok := s.resolveAgentMaxDuration(ctx, a, hubDefaults, tmplCache)
		if !ok || !now.After(a.RunStartedAt.Add(maxDur+maxDurationEnforcementGrace)) {
			continue
		}
		s.agentLifecycleLog.Info("Agent exceeded max_duration plus hub grace; stopping run",
			"agent_id", a.ID, "slug", a.Slug, "run_id", a.RunID, "run_started_at", a.RunStartedAt.UTC(), "max_duration", maxDur, "grace", maxDurationEnforcementGrace)
		if s.stopCurrentRunWithOutcome(ctx, a.ID, a.RunID, "max_duration enforcement stop", state.ActivityLimitsExceeded, state.ExitReasonLimitsExceeded) {
			stopped++
		}
	}
}
