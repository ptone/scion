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

package runtimebroker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	scionrt "github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtimebroker/harvest"
)

// SetHarvestEngine installs the broker's trajectory+usage harvest engine
// (Tokenomics P6.1). Passing nil disables harvesting.
func (s *Server) SetHarvestEngine(eng *harvest.Engine) {
	s.harvestEngineMu.Lock()
	s.harvestEngine = eng
	s.harvestEngineMu.Unlock()
}

func (s *Server) getHarvestEngine() *harvest.Engine {
	s.harvestEngineMu.RLock()
	eng := s.harvestEngine
	s.harvestEngineMu.RUnlock()
	return eng
}

func runtimeForManager(mgr agent.Manager, fallback scionrt.Runtime) scionrt.Runtime {
	switch m := mgr.(type) {
	case *agent.AgentManager:
		if m != nil && m.Runtime != nil {
			return m.Runtime
		}
	case managerRuntimeProvider:
		if m != nil {
			if rt := m.managerRuntime(); rt != nil {
				return rt
			}
		}
	}
	return fallback
}

// resolveHarvestTarget builds a harvest.Target from existing broker state
// (runtime capability, agent metadata, scion-agent.json, and the shared
// ResolveMoveBundleDirsForRuntime home resolver) before files are removed.
func (s *Server) resolveHarvestTarget(
	ctx context.Context,
	mgr agent.Manager,
	runtimeNameHint string,
	slug, agentID, projectID, runID, projectPath, harnessHint string,
) harvest.Target {
	rt := runtimeForManager(mgr, s.currentRuntime())
	runtimeName := runtimeNameHint
	if runtimeName == "" && rt != nil {
		runtimeName = rt.Name()
	}
	readable := scionrt.HomeReadableByBroker(rt)

	target := harvest.Target{
		AgentID:      agentID,
		Slug:         slug,
		ProjectID:    projectID,
		RunID:        runID,
		Harness:      harnessHint,
		RuntimeName:  runtimeName,
		HomeReadable: readable,
	}
	if !readable {
		return target
	}

	dirs, err := agent.ResolveMoveBundleDirsForRuntime(ctx, agent.MoveBundleRuntimeInput{
		RuntimeName: runtimeName,
		AgentSlug:   slug,
		AgentID:     agentID,
		ProjectPath: projectPath,
		ProjectID:   projectID,
		BrokerMode:  true,
	})
	if err == nil && dirs.HomeDir != "" {
		target.HomeDir = dirs.HomeDir
		if dirs.AgentDir != "" {
			if h := readHarnessFromAgentDir(dirs.AgentDir); h != "" {
				target.Harness = h
			}
		}
	}

	return target
}

// readHarnessFromAgentDir reads the canonical harness type (e.g. "claude") from
// <agentDir>/scion-agent.json without creating or modifying any files.
func readHarnessFromAgentDir(agentDir string) string {
	data, err := os.ReadFile(filepath.Join(agentDir, "scion-agent.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		Harness       string `json:"harness"`
		HarnessConfig string `json:"harness_config"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	if cfg.Harness != "" {
		return cfg.Harness
	}
	return cfg.HarnessConfig
}

// runPreDeleteHarvest runs a bounded pre_delete harvest after ensuring the
// container is stopped and before DeleteTarget or RemoveNFSAgentFiles remove
// files. Returns the *harvest.Report as the P6.2 custody-hold hook; in P6.1
// the delete path always proceeds.
func (s *Server) runPreDeleteHarvest(
	ctx context.Context,
	target *deleteTarget,
	hubAgentID, projectID, runID, projectPath string,
) *harvest.Report {
	eng := s.getHarvestEngine()
	if eng == nil || target == nil {
		return nil
	}

	// Ensure the container is stopped before reading the home so no live
	// shipper holds state.lock or writes to native artifacts.
	if target.containerID != "" && target.mgr != nil {
		_ = target.mgr.StopTarget(ctx, deleteRunRef(target, runID))
	}

	effectiveAgentID := hubAgentID
	if effectiveAgentID == "" {
		effectiveAgentID = target.agentID
	}
	effectiveRunID := runID
	if effectiveRunID == "" {
		effectiveRunID = target.runID
	}
	effectiveProjectID := projectID
	if effectiveProjectID == "" {
		effectiveProjectID = target.projectID
	}

	ht := s.resolveHarvestTarget(
		ctx,
		target.mgr,
		"",
		target.name,
		effectiveAgentID,
		effectiveProjectID,
		effectiveRunID,
		projectPath,
		target.harness,
	)

	rep := eng.Harvest(ctx, ht, harvest.TriggerPreDelete)
	if s.agentLifecycleLog != nil {
		s.agentLifecycleLog.Info("Agent pre_delete harvest completed",
			"agent_id", effectiveAgentID,
			"slug", target.name,
			"project_id", effectiveProjectID,
			"run_id", effectiveRunID,
			"outcome", rep.Outcome,
			"trajectory", rep.Trajectory,
			"usage", rep.Usage,
			"segments_committed", rep.SegmentsCommitted,
			"usage_events_drained", rep.UsageEventsDrained,
			"error_code", rep.ErrorCode)
	}
	return &rep
}

// runPreResetHarvest runs a bounded pre_reset harvest before Reprovision with
// ResetHome moves the broker-local home aside (or before the next Run lazily
// clears an NFS home leaf on Cloud Run / K8s NFS).
func (s *Server) runPreResetHarvest(
	ctx context.Context,
	mgr agent.Manager,
	runtimeType string,
	req CreateAgentRequest,
	opts api.StartOptions,
) *harvest.Report {
	eng := s.getHarvestEngine()
	if eng == nil {
		return nil
	}

	slug := req.Slug
	if slug == "" {
		slug = opts.Name
	}
	harnessHint := ""
	if req.Config != nil {
		harnessHint = req.Config.HarnessConfig
	}
	ht := s.resolveHarvestTarget(
		ctx,
		mgr,
		runtimeType,
		slug,
		req.ID,
		req.ProjectID,
		opts.RunID,
		opts.ProjectPath,
		harnessHint,
	)

	rep := eng.Harvest(ctx, ht, harvest.TriggerPreReset)
	if s.agentLifecycleLog != nil {
		s.agentLifecycleLog.Info("Agent pre_reset harvest completed",
			"agent_id", req.ID,
			"slug", slug,
			"project_id", req.ProjectID,
			"run_id", opts.RunID,
			"outcome", rep.Outcome,
			"trajectory", rep.Trajectory,
			"usage", rep.Usage,
			"segments_committed", rep.SegmentsCommitted,
			"usage_events_drained", rep.UsageEventsDrained,
			"error_code", rep.ErrorCode)
	}
	return &rep
}

// exitHarvestTracker watches heartbeat agent listings and schedules at most one
// asynchronous post_exit harvest per (agent, run) when an agent transitions
// from running to exited/stopped.
type exitHarvestTracker struct {
	mu          sync.Mutex
	lastRunning map[string]bool
	triggered   map[string]struct{}
	// schedule overrides goroutine dispatch in unit tests when non-nil.
	schedule func(ag api.AgentInfo)
}

func newExitHarvestTracker(schedule func(ag api.AgentInfo)) *exitHarvestTracker {
	return &exitHarvestTracker{
		lastRunning: make(map[string]bool),
		triggered:   make(map[string]struct{}),
		schedule:    schedule,
	}
}

func exitTrackerAgentKey(ag api.AgentInfo) string {
	if id := heartbeatAgentID(ag); id != "" {
		return id
	}
	proj := ag.ProjectID
	if proj == "" {
		proj = ag.Project
	}
	return proj + "/" + ag.Name
}

func isAgentRunningForHarvest(ag api.AgentInfo) bool {
	cs := strings.ToLower(strings.TrimSpace(ag.ContainerStatus))
	if strings.HasPrefix(cs, "running") || strings.HasPrefix(cs, "up") {
		return true
	}
	if cs == "" && strings.EqualFold(ag.Phase, "running") {
		return true
	}
	return false
}

func isAgentExitedForHarvest(ag api.AgentInfo) bool {
	cs := strings.ToLower(strings.TrimSpace(ag.ContainerStatus))
	for _, prefix := range []string{"exited", "stopped", "dead", "failed", "succeeded"} {
		if strings.HasPrefix(cs, prefix) {
			return true
		}
	}
	switch strings.ToLower(strings.TrimSpace(ag.Phase)) {
	case "stopped", "exited", "error":
		return !isAgentRunningForHarvest(ag)
	}
	return false
}

// observe records the latest heartbeat snapshot and schedules asynchronous
// post_exit harvests for any agent that transitioned running -> exited/stopped.
func (t *exitHarvestTracker) observe(agents []api.AgentInfo) {
	if t == nil {
		return
	}
	var toSchedule []api.AgentInfo

	t.mu.Lock()
	for _, ag := range agents {
		key := exitTrackerAgentKey(ag)
		if key == "" {
			continue
		}
		if isAgentRunningForHarvest(ag) {
			t.lastRunning[key] = true
			continue
		}
		if isAgentExitedForHarvest(ag) && t.lastRunning[key] {
			t.lastRunning[key] = false
			debounceKey := key + "|" + ag.RunID
			if _, already := t.triggered[debounceKey]; !already {
				t.triggered[debounceKey] = struct{}{}
				toSchedule = append(toSchedule, ag)
			}
		}
	}
	schedule := t.schedule
	t.mu.Unlock()

	for _, ag := range toSchedule {
		if schedule != nil {
			schedule(ag)
		}
	}
}

func (s *Server) ensureExitHarvestTracker() *exitHarvestTracker {
	s.harvestEngineMu.Lock()
	defer s.harvestEngineMu.Unlock()
	if s.exitHarvestTracker == nil {
		s.exitHarvestTracker = newExitHarvestTracker(s.schedulePostExitHarvest)
	}
	return s.exitHarvestTracker
}

// observeHeartbeatAgentsForHarvest is the non-blocking HeartbeatService hook.
func (s *Server) observeHeartbeatAgentsForHarvest(agents []api.AgentInfo) {
	if s.getHarvestEngine() == nil {
		return
	}
	s.ensureExitHarvestTracker().observe(agents)
}

func (s *Server) schedulePostExitHarvest(ag api.AgentInfo) {
	go func() {
		eng := s.getHarvestEngine()
		if eng == nil {
			return
		}
		ctx := context.Background()
		agentID := heartbeatAgentID(ag)
		projectID := ag.ProjectID
		if projectID == "" {
			projectID = ag.Project
		}
		harnessHint := ag.Harness
		if harnessHint == "" {
			harnessHint = ag.HarnessConfig
		}
		projectPath := ag.ProjectPath
		if projectPath == "" && projectID != "" {
			if resolved, err := s.findAgentProjectDir(ag.Name, projectID, ""); err == nil {
				projectPath = resolved
			}
		}
		var mgr agent.Manager
		if ag.Runtime != "" {
			if aux, ok := s.findAuxiliaryRuntimeByType(ag.Runtime); ok && aux.Manager != nil {
				mgr = aux.Manager
			}
		}
		if mgr == nil {
			mgr = s.currentManager()
		}
		ht := s.resolveHarvestTarget(
			ctx,
			mgr,
			ag.Runtime,
			ag.Name,
			agentID,
			projectID,
			ag.RunID,
			projectPath,
			harnessHint,
		)
		rep := eng.Harvest(ctx, ht, harvest.TriggerPostExit)
		if s.agentLifecycleLog != nil {
			s.agentLifecycleLog.Info("Agent post_exit harvest completed",
				"agent_id", agentID,
				"slug", ag.Name,
				"project_id", projectID,
				"run_id", ag.RunID,
				"outcome", rep.Outcome,
				"trajectory", rep.Trajectory,
				"usage", rep.Usage,
				"segments_committed", rep.SegmentsCommitted,
				"usage_events_drained", rep.UsageEventsDrained,
				"error_code", rep.ErrorCode)
		}
	}()
}
