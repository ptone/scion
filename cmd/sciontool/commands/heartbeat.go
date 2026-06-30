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

package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

var heartbeatDaemon bool

var heartbeatCmd = &cobra.Command{
	Use:   "heartbeat",
	Short: "Run the Hub heartbeat loop",
	Long: `Run a lightweight heartbeat daemon that periodically reports liveness
to the Hub and watches the local status file for changes.

This is designed for environments where sciontool does not run as PID 1
(e.g. managed agent sandboxes). It provides:

  - Hub heartbeat loop (periodic liveness reports)
  - Status file watcher (reads agent-info.json changes, reports to Hub)

Use --daemon to run in the background (typically with & in a shell script).

Required environment variables:
  SCION_HUB_ENDPOINT  Hub API URL
  SCION_AUTH_TOKEN     Agent authentication token (or token file at ~/.scion/scion-token)
  SCION_AGENT_ID       Agent identifier`,
	Run: func(cmd *cobra.Command, args []string) {
		runHeartbeatDaemon()
	},
}

func init() {
	heartbeatCmd.Flags().BoolVar(&heartbeatDaemon, "daemon", false,
		"Run as a background daemon (no-op flag for documentation; use shell & to background)")
	rootCmd.AddCommand(heartbeatCmd)
}

func runHeartbeatDaemon() {
	hubClient := hub.NewClient()
	if hubClient == nil || !hubClient.IsConfigured() {
		log.Error("Hub client not configured — set SCION_HUB_ENDPOINT, SCION_AUTH_TOKEN, and SCION_AGENT_ID")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	heartbeatDone := hubClient.StartHeartbeat(ctx, &hub.HeartbeatConfig{
		Interval: hub.DefaultHeartbeatInterval,
		Timeout:  hub.DefaultHeartbeatTimeout,
		OnError: func(err error) {
			log.Error("Heartbeat failed: %v", err)
		},
		OnSuccess: func() {
			log.Debug("Heartbeat sent")
		},
	})
	log.Info("Heartbeat daemon started (interval: %s)", hub.DefaultHeartbeatInterval)

	// Start status file watcher
	statusPath := resolveStatusPath()
	statusDone := watchStatusFile(ctx, hubClient, statusPath)

	// Block until signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigCh
	log.Info("Received %s, shutting down", sig)
	cancel()

	<-heartbeatDone
	<-statusDone
}

func resolveStatusPath() string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/scion"
	}
	return filepath.Join(home, "agent-info.json")
}

// watchStatusFile polls the agent-info.json file for changes and reports
// status updates to the Hub. Returns a channel closed when the watcher exits.
func watchStatusFile(ctx context.Context, client *hub.Client, path string) <-chan struct{} {
	done := make(chan struct{})

	go func() {
		defer close(done)

		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		var lastMod time.Time
		var lastActivity string

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				if !info.ModTime().After(lastMod) {
					continue
				}
				lastMod = info.ModTime()

				data, err := os.ReadFile(path)
				if err != nil {
					log.Debug("Failed to read status file: %v", err)
					continue
				}

				var agentInfo struct {
					Activity string `json:"activity"`
					Phase    string `json:"phase"`
					Message  string `json:"message"`
				}
				if err := json.Unmarshal(data, &agentInfo); err != nil {
					log.Debug("Failed to parse status file: %v", err)
					continue
				}

				if agentInfo.Activity == lastActivity {
					continue
				}
				lastActivity = agentInfo.Activity

				update := hub.StatusUpdate{
					Activity: state.Activity(agentInfo.Activity),
					Message:  agentInfo.Message,
				}
				if agentInfo.Phase != "" {
					update.Phase = state.Phase(agentInfo.Phase)
				}
				if err := client.UpdateStatus(ctx, update); err != nil {
					log.Debug("Failed to report status to Hub: %v", err)
				} else {
					log.Debug("Reported status: activity=%s", agentInfo.Activity)
				}
			}
		}
	}()

	return done
}
