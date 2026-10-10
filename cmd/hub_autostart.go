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
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/daemon"
	"github.com/GoogleCloudPlatform/scion/pkg/hubsync"
)

// Automatic start of the local workstation server.
//
// When no Hub endpoint is configured, a command that needs a hub may start
// the workstation server (hub, broker and web on loopback, with dev auth) by
// running `<this binary> server start`, the same daemon path a user runs by
// hand, so an automatically started server is identical to a user-started
// one. It is controlled by the hub.auto_start setting and the
// SCION_HUB_AUTO_START env var, and never happens inside an agent container.

// autoStartEnvVar overrides the hub.auto_start setting for one invocation.
const autoStartEnvVar = "SCION_HUB_AUTO_START"

// localServerReadyTimeout bounds the wait for an automatically started
// server to answer /healthz. A first start includes database migrations.
const localServerReadyTimeout = 60 * time.Second

// isLocalWorkstationEndpoint reports whether endpoint is the local
// workstation hub: a loopback host that the CLI reaches with the dev token.
// See hubsync.IsLocalWorkstationEndpoint.
func isLocalWorkstationEndpoint(endpoint string) bool {
	return hubsync.IsLocalWorkstationEndpoint(endpoint)
}

// insideAgentContainer reports whether the CLI runs inside an agent
// container: a container the CLI or a broker started (SCION_HOST_UID), or a
// hub-managed agent (SCION_AGENT_ID).
func insideAgentContainer(getenv func(string) string) bool {
	return getenv("SCION_HOST_UID") != "" || getenv("SCION_AGENT_ID") != ""
}

// parseAutoStartEnv parses a SCION_HUB_AUTO_START value. ok is false when
// the value is not a recognised boolean.
func parseAutoStartEnv(value string) (enabled, ok bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yes", "on":
		return true, true
	case "no", "off":
		return false, true
	}
	b, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, false
	}
	return b, true
}

// autoStartAllowed reports whether the CLI may start the local workstation
// server, and, when it may not, why. Precedence: never inside an agent
// container; then SCION_HUB_AUTO_START; then the hub.auto_start setting
// (default true). An unrecognised SCION_HUB_AUTO_START value disables
// auto-start, so a typo never spawns a daemon.
func autoStartAllowed(settings *config.Settings) (bool, string) {
	return autoStartAllowedFor(settings, os.Getenv)
}

func autoStartAllowedFor(settings *config.Settings, getenv func(string) string) (bool, string) {
	if insideAgentContainer(getenv) {
		return false, "the CLI is running inside an agent container"
	}
	if raw := getenv(autoStartEnvVar); raw != "" {
		enabled, ok := parseAutoStartEnv(raw)
		if !ok {
			return false, fmt.Sprintf("%s=%q is not a boolean", autoStartEnvVar, raw)
		}
		if !enabled {
			return false, fmt.Sprintf("%s=%s turns it off", autoStartEnvVar, raw)
		}
		return true, ""
	}
	if !settings.IsHubAutoStartEnabled() {
		return false, "the hub.auto_start setting is false"
	}
	return true, ""
}

// ensureLocalServerFn is a seam so tests never spawn a server.
var ensureLocalServerFn = ensureLocalServer

// ensureLocalServer starts the local workstation server as a daemon by
// running `<this binary> server start`, waits until it answers, and returns
// the hub endpoint that the server start wrote to the global settings.
// It does not retry or kill anything: errors from the server start (a port
// conflict, a stale PID file) are shown as the server reports them.
func ensureLocalServer() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to find the scion executable: %w", err)
	}
	globalDir, err := config.GetGlobalDir()
	if err != nil {
		return "", fmt.Errorf("failed to get global directory: %w", err)
	}

	fmt.Fprintln(os.Stderr, "No hub configured; starting the local scion server...")
	start := exec.Command(executable, "server", "start")
	// Its output goes to stderr so that stdout stays clean for --format json.
	start.Stdout = os.Stderr
	start.Stderr = os.Stderr
	// Starting a server for the user must not open a browser.
	start.Env = append(os.Environ(), "SCION_NO_BROWSER=1")
	if err := start.Run(); err != nil {
		return "", fmt.Errorf("failed to start the local scion server (see the output above): %w", err)
	}

	// The server start writes hub.endpoint to the global settings when it
	// is not set yet (printWorkstationQuickstart).
	endpoint := ""
	if vs, err := config.LoadSingleFileVersioned(globalDir); err == nil {
		endpoint = vs.GetHubEndpoint()
	}
	if endpoint == "" {
		return "", fmt.Errorf("the local scion server started but no hub endpoint was configured; check 'scion server status' and set one with 'scion config set --global hub.endpoint <url>'")
	}

	host, port, err := endpointHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid hub endpoint %q: %w", endpoint, err)
	}
	ready, health := waitForServerReady(host, port, localServerReadyTimeout)
	msg, _ := quickstartReadyMessage(ready, health)
	if !ready {
		return "", fmt.Errorf("the local scion server at %s did not become ready:%s\nCheck the log at %s",
			endpoint, msg, daemon.GetLogPathComponent(serverDaemonComponent, globalDir))
	}
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}

	fmt.Fprintf(os.Stderr, "Local scion server is running at %s. Stop it with 'scion server stop'; "+
		"turn off automatic start with 'scion config set --global hub.auto_start false'. "+
		"To start it at login, run 'scion server install'.\n", endpoint)
	return endpoint, nil
}

// endpointHostPort splits an http(s) endpoint into host and port, using the
// scheme's default port when none is given.
func endpointHostPort(endpoint string) (string, int, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", 0, err
	}
	host := u.Hostname()
	if host == "" {
		return "", 0, fmt.Errorf("no host")
	}
	portStr := u.Port()
	if portStr == "" {
		switch u.Scheme {
		case "https":
			portStr = "443"
		default:
			portStr = "80"
		}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q", portStr)
	}
	if strings.Contains(host, ":") {
		// waitForServerReady formats host:port without brackets.
		host = "[" + host + "]"
	}
	return host, port, nil
}
