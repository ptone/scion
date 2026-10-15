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

package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/hub"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 4 of ptone/scion#4240: --enable-test-login starts only when every
// listener of the server process is bound to loopback.

func TestValidateTestLoginListeners(t *testing.T) {
	cfgWith := func(hubHost string, broker bool, brokerHost, conduit string) *config.GlobalConfig {
		cfg := &config.GlobalConfig{}
		cfg.Hub.Host = hubHost
		cfg.RuntimeBroker.Enabled = broker
		cfg.RuntimeBroker.Host = brokerHost
		cfg.Hub.Conduit.InternalListen = conduit
		return cfg
	}
	for _, tc := range []struct {
		name     string
		cfg      *config.GlobalConfig
		start    bool
		listener string // named in the refusal
	}{
		// MUST START.
		{"hub 127.0.0.1", cfgWith("127.0.0.1", false, "", ""), true, ""},
		{"hub ::1", cfgWith("::1", false, "", ""), true, ""},
		{"hub localhost", cfgWith("localhost", false, "", ""), true, ""},
		{"broker off on 0.0.0.0 is not a listener", cfgWith("127.0.0.1", false, "0.0.0.0", ""), true, ""},
		{"broker on loopback", cfgWith("127.0.0.1", true, "127.0.0.1", ""), true, ""},
		{"conduit 127.0.0.1:port", cfgWith("127.0.0.1", true, "::1", "127.0.0.1:9811"), true, ""},
		{"conduit [::1]:port", cfgWith("127.0.0.1", false, "", "[::1]:9811"), true, ""},
		{"conduit localhost:port", cfgWith("127.0.0.1", false, "", "localhost:9811"), true, ""},
		// MUST REFUSE: the main bind.
		{"hub empty (every interface)", cfgWith("", false, "", ""), false, "hub API and web listener"},
		{"hub 0.0.0.0", cfgWith("0.0.0.0", false, "", ""), false, "hub API and web listener"},
		{"hub ::", cfgWith("::", false, "", ""), false, "hub API and web listener"},
		{"hub LAN IP", cfgWith("192.168.1.10", false, "", ""), false, "hub API and web listener"},
		{"hub hostname", cfgWith("hub.example.com", false, "", ""), false, "hub API and web listener"},
		// MUST REFUSE: any other listener off loopback.
		{"broker 0.0.0.0", cfgWith("127.0.0.1", true, "0.0.0.0", ""), false, "runtime broker listener"},
		{"broker empty", cfgWith("127.0.0.1", true, "", ""), false, "runtime broker listener"},
		{"broker LAN IP", cfgWith("127.0.0.1", true, "10.0.0.5", ""), false, "runtime broker listener"},
		{"conduit 0.0.0.0:port", cfgWith("127.0.0.1", false, "", "0.0.0.0:9811"), false, "conduit internal listener"},
		{"conduit :port (every interface)", cfgWith("127.0.0.1", false, "", ":9811"), false, "conduit internal listener"},
		{"conduit LAN IP", cfgWith("127.0.0.1", false, "", "10.0.0.5:9811"), false, "conduit internal listener"},
		{"conduit not host:port", cfgWith("127.0.0.1", false, "", "127.0.0.1"), false, "conduit internal listener"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTestLoginListeners(true, tc.cfg)
			if tc.start {
				assert.NoError(t, err)
			} else {
				require.ErrorIs(t, err, hub.ErrTestLoginNonLoopback)
				assert.Contains(t, err.Error(), tc.listener)
			}
			assert.NoError(t, validateTestLoginListeners(false, tc.cfg), "test-login off never refuses")
		})
	}
	assert.NoError(t, validateTestLoginListeners(true, nil))
}

// tlStartCmd builds a server start command bound to the real server
// globals, registering only the flags these tests set, and parses args.
func tlStartCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	testModeValue, removedTestFlagValue = "", ""
	c := &cobra.Command{Use: "start", RunE: func(*cobra.Command, []string) error { return nil }}
	f := c.Flags()
	f.BoolVar(&hostedMode, "hosted", false, "")
	f.BoolVar(&hostedMode, "production", false, "")
	f.StringVar(&hubHost, "host", "0.0.0.0", "")
	f.BoolVar(&enableHub, "enable-hub", false, "")
	f.BoolVar(&enableRuntimeBroker, "enable-runtime-broker", false, "")
	f.BoolVar(&enableTestLogin, "enable-test-login", false, "")
	registerTestModeFlags(c)
	c.SetArgs(args)
	require.NoError(t, c.Execute())
	return c
}

func tlResetServerState(t *testing.T) {
	t.Helper()
	savedEndpoints := enableDebugEndpoints
	reset := func() {
		resetServerFlags()
		resetTestModeFlags()
		enableTestLogin = false
		enableDebugEndpoints = savedEndpoints
	}
	t.Cleanup(reset)
	reset()
	enableDebugEndpoints = false
	for _, k := range []string{"SCION_SERVER_MODE", "SCION_SERVER_HUB_HOST", "SCION_SERVER_BROKER_HOST", "SCION_SERVER_RUNTIMEBROKER_HOST"} {
		t.Setenv(k, "")
	}
}

type testLoginBindStartCase struct {
	name     string
	settings string // global settings.yaml
	hubEnv   string // SCION_SERVER_HUB_HOST
	args     []string
	start    bool
}

const (
	tlHosted      = "schema_version: \"1\"\nserver:\n  mode: hosted\n"
	tlWorkstation = "schema_version: \"1\"\nserver:\n  mode: workstation\n"
	tlHostedLAN   = "schema_version: \"1\"\nserver:\n  mode: hosted\n  broker:\n    host: 192.168.1.10\n"
)

var testLoginBindStartCases = []testLoginBindStartCase{
	// MUST START.
	{name: "hosted --host 127.0.0.1", settings: tlHosted, args: []string{"--enable-test-login", "--host", "127.0.0.1"}, start: true},
	{name: "hosted --host ::1", settings: tlHosted, args: []string{"--enable-test-login", "--host", "::1"}, start: true},
	{name: "hosted --host localhost", settings: tlHosted, args: []string{"--enable-test-login", "--host", "localhost"}, start: true},
	{name: "workstation default host (127.0.0.1, broker on loopback)", settings: tlWorkstation, args: []string{"--enable-test-login"}, start: true},
	{name: "test-login off on 0.0.0.0", settings: tlHosted, args: []string{"--host", "0.0.0.0"}, start: true},
	// MUST REFUSE.
	{name: "hosted, host unset (every interface)", settings: tlHosted, args: []string{"--enable-test-login"}},
	{name: "hosted --host 0.0.0.0", settings: tlHosted, args: []string{"--enable-test-login", "--host", "0.0.0.0"}},
	{name: "hosted --host ::", settings: tlHosted, args: []string{"--enable-test-login", "--host", "::"}},
	{name: "hosted, LAN IP from server.hub.host", settings: tlHosted, hubEnv: "10.1.2.3", args: []string{"--enable-test-login"}},
	{name: "hosted --host hostname", settings: tlHosted, args: []string{"--enable-test-login", "--host", "hub.example.com"}},
	{name: "workstation --host 0.0.0.0", settings: tlWorkstation, args: []string{"--enable-test-login", "--host", "0.0.0.0"}},
	{name: "loopback hub, broker listener on a LAN IP", settings: tlHostedLAN, args: []string{"--enable-test-login", "--host", "127.0.0.1", "--enable-hub", "--enable-runtime-broker"}},
	// TEST MODE: independent of test-login. A level does not relax the
	// bind rule, and test-login on loopback needs no level.
	{name: "test-mode member, host unset", settings: tlHosted, args: []string{"--enable-test-login", "--test-mode=member:" + tiaCmdHubID}},
	{name: "test-mode admin, --host 0.0.0.0", settings: tlHosted, args: []string{"--enable-test-login", "--test-mode=admin:" + tiaCmdHubID, "--host", "0.0.0.0"}},
	{name: "test-mode member, --host 127.0.0.1", settings: tlHosted, args: []string{"--enable-test-login", "--test-mode=member:" + tiaCmdHubID, "--host", "127.0.0.1"}, start: true},
}

// Foreground: loadAndReconcileConfig refuses before anything is started.
func TestLoadAndReconcileConfig_TestLoginBind(t *testing.T) {
	for _, tc := range testLoginBindStartCases {
		t.Run(tc.name, func(t *testing.T) {
			tlResetServerState(t)
			writeServerSettings(t, tc.settings)
			if tc.hubEnv != "" {
				t.Setenv("SCION_SERVER_HUB_HOST", tc.hubEnv)
			}
			_, err := loadAndReconcileConfig(tlStartCmd(t, tc.args...))
			if tc.start {
				assert.NotErrorIs(t, err, hub.ErrTestLoginNonLoopback)
			} else {
				assert.ErrorIs(t, err, hub.ErrTestLoginNonLoopback)
			}
		})
	}
}

// Daemon: the parent refuses before it starts the --foreground child, so
// the reason is reported directly.
func TestResolveDaemonServerMode_TestLoginBind(t *testing.T) {
	for _, tc := range testLoginBindStartCases {
		t.Run(tc.name, func(t *testing.T) {
			tlResetServerState(t)
			writeServerSettings(t, tc.settings)
			if tc.hubEnv != "" {
				t.Setenv("SCION_SERVER_HUB_HOST", tc.hubEnv)
			}
			err := resolveDaemonServerMode(tlStartCmd(t, tc.args...))
			if tc.start {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, hub.ErrTestLoginNonLoopback)
			}
		})
	}
}

// The checked-in callers that start a hub with --enable-test-login bind it
// to loopback, so the confinement leaves them working: the Playwright e2e
// harness (web/e2e/harness/hub.ts, which also pins the broker listener to
// loopback when it enables one) and the perf wall-clock benchmark.
func TestTestLoginCallersBindLoopback(t *testing.T) {
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", rel))
		require.NoError(t, err)
		return string(b)
	}

	harness := read("web/e2e/harness/hub.ts")
	require.Contains(t, harness, "'--enable-test-login'")
	m := regexp.MustCompile(`'--host',\s*'([^']*)'`).FindAllStringSubmatch(harness, -1)
	require.Len(t, m, 1, "one --host argument in the harness")
	assert.NoError(t, hub.CheckTestLoginBind(true, m[0][1]), "harness --host")
	b := regexp.MustCompile(`SCION_SERVER_RUNTIMEBROKER_HOST = '([^']*)'`).FindAllStringSubmatch(harness, -1)
	require.Len(t, b, 1, "the harness pins the broker listener host")
	assert.NoError(t, hub.CheckTestLoginBind(true, b[0][1]), "harness broker host")

	bench := read("perf/bench/wallclock.sh")
	require.Contains(t, bench, "--enable-test-login")
	h := regexp.MustCompile(`--host (\S+)`).FindAllStringSubmatch(bench, -1)
	require.Len(t, h, 1, "one --host argument in the benchmark")
	assert.NoError(t, hub.CheckTestLoginBind(true, h[0][1]), "benchmark --host")
	assert.False(t, strings.Contains(bench, "--enable-runtime-broker"), "the benchmark runs no broker listener")
}
