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
	"errors"
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
	if written, err := st.RecordAgentRunStarted(rctx, agentID, runID, now); err != nil {
		if log != nil {
			log.Warn("Failed to record run_started_at", "agent_id", agentID, "run_id", runID, "error", err)
		}
	} else if written && row != nil && row.RunID == runID {
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

// parseMaxDurationRung interprets one rung of the max_duration precedence
// ladder. Empty or unparseable values return ok=false so resolution falls
// through to the next rung (the safe direction, toward hub default / 24h);
// "0" or a non-positive duration returns (0, true, true) to opt out.
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

type tmplMaxDurEntry struct {
	tmpl *store.Template
	cfg  *api.ScionConfig
	err  error
}

func (s *Server) lookupTemplateForMaxDuration(ctx context.Context, templateID string) tmplMaxDurEntry {
	tmpl, err := s.store.GetTemplate(ctx, templateID)
	if err != nil {
		return tmplMaxDurEntry{err: err}
	}
	if tmpl == nil {
		return tmplMaxDurEntry{err: store.ErrNotFound}
	}
	if tmpl.AgentConfig == nil && templateHasAgentConfigFile(tmpl) && s.ensureTemplateAgentConfig(ctx, tmpl) == nil {
		return tmplMaxDurEntry{err: errors.New("template agent config unavailable")}
	}
	cfg, _ := config.StripTemplateEnvironmentKeys(tmpl.AgentConfig)
	return tmplMaxDurEntry{tmpl: tmpl, cfg: cfg}
}

// resolveAgentMaxDuration resolves inline/project > hub template > hub default > 24h.
// On a template read error, missing template, or content-hash mismatch it logs
// and returns (0, false) so the sweep fails open for this tick rather than
// falling through to a shorter default.
func (s *Server) resolveAgentMaxDuration(ctx context.Context, a *store.Agent, hubDefaults opsettings.AgentDefaultsSettings, tmplCache map[string]tmplMaxDurEntry) (time.Duration, bool) {
	if a.AppliedConfig != nil && a.AppliedConfig.InlineConfig != nil {
		if d, unlimited, ok := parseMaxDurationRung(a.AppliedConfig.InlineConfig.MaxDuration); ok {
			return d, !unlimited
		}
	}
	if a.AppliedConfig != nil && a.AppliedConfig.TemplateID != "" {
		tid := a.AppliedConfig.TemplateID
		entry, cached := tmplCache[tid]
		if !cached {
			entry = s.lookupTemplateForMaxDuration(ctx, tid)
			tmplCache[tid] = entry
		}
		if entry.err != nil {
			s.agentLifecycleLog.Warn("max_duration enforcement: skipping agent; could not read template",
				"agent_id", a.ID, "template_id", tid, "error", entry.err)
			return 0, false
		}
		if a.AppliedConfig.TemplateHash != "" && entry.tmpl.ContentHash != a.AppliedConfig.TemplateHash {
			s.agentLifecycleLog.Info("max_duration enforcement: skipping agent; template hash changed since provision",
				"agent_id", a.ID, "template_id", tid, "applied_hash", a.AppliedConfig.TemplateHash, "current_hash", entry.tmpl.ContentHash)
			return 0, false
		}
		if entry.cfg != nil {
			if d, unlimited, ok := parseMaxDurationRung(entry.cfg.MaxDuration); ok {
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
	agents, err := s.store.ListActiveAgentsForMaxDuration(ctx, now.Add(-maxDurationEnforcementGrace))
	if err != nil {
		s.agentLifecycleLog.Error("max_duration enforcement: failed to list active agents", "error", err)
		return
	}
	if len(agents) == 0 {
		return
	}
	hubDefaults, tmplCache, failedBrokers, attempts := s.hubAgentDefaults(), make(map[string]tmplMaxDurEntry), make(map[string]bool), 0
	for i := range agents {
		if attempts >= maxDurationEnforcementPerTickCap {
			break
		}
		a := &agents[i]
		if a.RunID == "" || a.RunStartedAt == nil || a.RunStartedAtRunID != a.RunID ||
			a.RuntimeBrokerID == "" || failedBrokers[a.RuntimeBrokerID] || systemStopBlocked(a) {
			continue
		}
		maxDur, ok := s.resolveAgentMaxDuration(ctx, a, hubDefaults, tmplCache)
		if !ok || !now.After(a.RunStartedAt.Add(maxDur+maxDurationEnforcementGrace)) {
			continue
		}
		s.agentLifecycleLog.Info("Agent exceeded max_duration plus hub grace; stopping run",
			"agent_id", a.ID, "slug", a.Slug, "run_id", a.RunID, "run_started_at", a.RunStartedAt.UTC(), "max_duration", maxDur, "grace", maxDurationEnforcementGrace)
		switch s.stopCurrentRunWithOutcome(ctx, a.ID, a.RunID, "max_duration enforcement stop", state.ActivityLimitsExceeded, state.ExitReasonLimitsExceeded) {
		case systemStopDispatched:
			attempts++
		case systemStopDispatchFailed:
			attempts++
			failedBrokers[a.RuntimeBrokerID] = true
		}
	}
}
