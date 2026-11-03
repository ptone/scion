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

//go:build unix

package commands

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
	"github.com/GoogleCloudPlatform/scion/pkg/stagedsecrets"
)

// captureSciontoolLog points the sciontool log file at a fresh path for the
// test and returns a reader for it.
func captureSciontoolLog(t *testing.T) func() string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.log")
	log.SetLogPath(p)
	t.Cleanup(func() { log.SetLogPath(filepath.Join(sandboxHomeDir, "agent.log")) })
	return func() string {
		b, _ := os.ReadFile(p)
		return string(b)
	}
}

// stagedBlob is a SCION_STAGED_SECRETS value carrying only a hub credential.
func stagedBlob(t *testing.T, token string) string {
	t.Helper()
	data, err := json.Marshal(stagedsecrets.Staged{HubToken: token})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data)
}

// runInitStagedFixture sets up an embedded RunInit (no re-exec) whose token
// home is agentHome, with the exit-code and limits paths isolated, and
// records whether the token file existed when the stopping report ran.
type runInitStagedFixture struct {
	agentHome, tokenPath string
	sawAtStopping        *bool
	readLog              func() string
}

func newRunInitStagedFixture(t *testing.T) runInitStagedFixture {
	t.Helper()
	agentHome := t.TempDir()
	setupRunInitAsRootlessScion(t, agentHome)
	t.Cleanup(hub.SetTokenHome(agentHome))
	t.Setenv(envHubTokenStaged, "")
	_ = os.Unsetenv(envHubTokenStaged)
	f := runInitStagedFixture{
		agentHome: agentHome,
		tokenPath: filepath.Join(agentHome, ".scion", hub.TokenFile),
		readLog:   captureSciontoolLog(t),
	}

	origExitCodePath := harnessExitCodePath
	harnessExitCodePath = filepath.Join(t.TempDir(), "no-harness-exit-code")
	t.Cleanup(func() { harnessExitCodePath = origExitCodePath })
	origTrigger := limitsTriggerPath
	limitsTriggerPath = filepath.Join(t.TempDir(), "limits-exceeded")
	t.Cleanup(func() { limitsTriggerPath = origTrigger })

	saw := false
	f.sawAtStopping = &saw
	origStopping := runReportStoppingToHub
	runReportStoppingToHub = func() {
		_, err := os.Lstat(f.tokenPath)
		saw = err == nil
	}
	t.Cleanup(func() { runReportStoppingToHub = origStopping })
	return f
}

func (f runInitStagedFixture) run(t *testing.T, script string) int {
	t.Helper()
	return RunInit([]string{"sh", "-c", script, "sh", f.tokenPath},
		InitRunOptions{DisableTermSignalForwarding: true, DisableReExec: true})
}

// A staged credential is written before the harness runs, is still there
// at the final reports, and is removed after them on a clean exit. The
// credential never reaches the log.
func TestRunInit_StagedHubToken_RemovedAfterFinalReportsOnCleanExit(t *testing.T) {
	f := newRunInitStagedFixture(t)
	t.Setenv(stagedsecrets.EnvVar, stagedBlob(t, initHubTokenCanary))
	seen := filepath.Join(t.TempDir(), "seen")

	if got := f.run(t, `[ -f "$1" ] && touch '`+seen+`'`); got != 0 {
		t.Fatalf("RunInit() = %d, want 0", got)
	}
	if _, err := os.Stat(seen); err != nil {
		t.Error("the harness did not see the staged token file")
	}
	if !*f.sawAtStopping {
		t.Error("the token file was gone before the stopping report")
	}
	if _, err := os.Lstat(f.tokenPath); !os.IsNotExist(err) {
		t.Errorf("staged token file not removed after a clean exit (err %v)", err)
	}
	out := f.readLog()
	if !strings.Contains(out, "Wrote the staged hub credential") || !strings.Contains(out, "Removed the staged hub credential file") {
		t.Errorf("log lacks the staging and removal lines (log length %d)", len(out))
	}
	if strings.Contains(out, initHubTokenCanary) {
		t.Error("log carries the credential")
	}
	if os.Getenv(envHubTokenStaged) != "" {
		t.Error("the staged-token marker leaked past init")
	}
}

// Restart sequence: after a crash exit the file (which the refresh loop
// made newer) is kept, and the next start with the same creation-time
// payload keeps it rather than writing the older staged credential.
func TestRunInit_StagedHubToken_CrashKeepsRefreshedTokenForRestart(t *testing.T) {
	f := newRunInitStagedFixture(t)
	createTime := iatToken(1000)
	refreshed := iatToken(2000)
	t.Setenv(stagedsecrets.EnvVar, stagedBlob(t, createTime))

	// The harness stands in for the refresh loop, writing a newer
	// credential, then crashes.
	script := `printf '%s' '` + refreshed + `' > "$1.tmp" && mv "$1.tmp" "$1"; exit 3`
	if got := f.run(t, script); got == 0 {
		t.Fatalf("RunInit() = 0, want a crash exit")
	}
	if got := readFileString(t, f.tokenPath); got != refreshed {
		t.Fatal("the refreshed token file was not kept after a crash exit")
	}
	if !strings.Contains(f.readLog(), "Kept the staged hub credential file for a restart") {
		t.Error("log lacks the kept-for-restart line")
	}

	// The container restarts with the same payload.
	t.Setenv(stagedsecrets.EnvVar, stagedBlob(t, createTime))
	if got := f.run(t, `test "$(cat "$1")" = '`+refreshed+`'`); got != 0 {
		t.Fatalf("restart RunInit() = %d, want 0 (the harness must see the refreshed credential)", got)
	}
	if _, err := os.Lstat(f.tokenPath); !os.IsNotExist(err) {
		t.Errorf("token file not removed after the restart's clean exit (err %v)", err)
	}
}

// Without a staged payload (docker, Kubernetes), a token file the broker
// provided is neither replaced nor removed.
func TestRunInit_NoStagedPayload_LeavesBrokerTokenFile(t *testing.T) {
	f := newRunInitStagedFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.tokenPath, []byte(initHubTokenCanary), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f.run(t, "true"); got != 0 {
		t.Fatalf("RunInit() = %d, want 0", got)
	}
	if got := readFileString(t, f.tokenPath); got != initHubTokenCanary {
		t.Error("a broker-provided token file was changed or removed")
	}
}

// RunInit with a hub endpoint but no credential logs the ERROR naming the
// missing credential and never claims hub-connected.
func TestRunInit_EndpointWithoutCredential_LogsErrorNotConnected(t *testing.T) {
	f := newRunInitStagedFixture(t)
	t.Setenv(hub.EnvHubEndpoint, "http://127.0.0.1:9")
	t.Setenv(hub.EnvAgentID, "agent-1")
	t.Cleanup(hub.SetHubTestSandboxed())
	if got := f.run(t, "true"); got != 0 {
		t.Fatalf("RunInit() = %d, want 0", got)
	}
	out := f.readLog()
	if strings.Contains(out, "hub-connected") {
		t.Error("log claims hub-connected with no credential")
	}
	if !strings.Contains(out, "[ERROR] Hub endpoint is set") || !strings.Contains(out, "token file absent") {
		t.Errorf("log lacks the ERROR naming the missing credential (log length %d)", len(out))
	}
}

// logHubOperatingMode itself: an ERROR (no hub-connected, no credential)
// for an unconfigured client, and hub-connected for a configured one.
func TestLogHubOperatingMode(t *testing.T) {
	_, tokenPath := isolatedTokenHome(t)
	readLog := captureSciontoolLog(t)
	t.Cleanup(hub.SetHubTestSandboxed())
	t.Setenv(hub.EnvHubToken, "")
	t.Setenv(hub.EnvHubURL, "")
	t.Setenv(hub.EnvAgentMode, "")
	t.Setenv(hub.EnvHubEndpoint, "http://127.0.0.1:9")
	t.Setenv(hub.EnvAgentID, "agent-1")

	logHubOperatingMode(hub.NewClient())
	out := readLog()
	if !strings.Contains(out, "[ERROR] Hub endpoint is set") || strings.Contains(out, "hub-connected") {
		t.Errorf("unconfigured client: want the ERROR and no hub-connected (log length %d)", len(out))
	}

	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(initHubTokenCanary), 0o600); err != nil {
		t.Fatal(err)
	}
	client := hub.NewClient()
	if client == nil || !client.IsConfigured() {
		t.Fatal("no configured client with a token file, endpoint and agent ID")
	}
	logHubOperatingMode(client)
	out = readLog()
	if !strings.Contains(out, "[INFO] Operating mode: hub-connected (endpoint: http://127.0.0.1:9)") {
		t.Errorf("configured client: no hub-connected line (log length %d)", len(out))
	}
	if strings.Contains(out, initHubTokenCanary) {
		t.Error("log carries the credential")
	}
}
