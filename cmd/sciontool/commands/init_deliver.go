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
	"slices"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/conduit"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/control"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

// deliverResolveTool resolves tmux and the readiness probe's shell from
// the fixed control.ToolPath (a variable so tests need no tmux).
var deliverResolveTool = control.ResolveTool

// envStartCmd carries the tmux start command when a runtime passes it
// through the environment instead of argv.
const envStartCmd = "SCION_START_CMD"

// startCmdFUSESuffix ends the shell script Docker and Podman run when the
// agent has FUSE mounts: the mounts, then this, with the tmux start
// command in envStartCmd (pkg/runtime/common.go:1037-1052). docker/podman
// run args are not shell-expanded, so sciontool sees it literally.
const startCmdFUSESuffix = `exec sh -c "$SCION_START_CMD"`

// deliverRouteOptions returns the options that enable the deliver-message
// control route, or nil when the route must be absent (design r3 §1.5,
// registration conditions): the hub must list experiments.ConduitDeliver
// in the agent env set at create time, tmux must be found, and the
// harness must run in the "scion" tmux session. tmux and the probe's
// shell are resolved once, here, to absolute paths in control.ToolPath
// (never this process's PATH); if either is missing the route is absent.
// The tmux steps run as ptyUser, the agent user that owns the tmux
// server.
func deliverRouteOptions(ptyUser conduit.PTYUser, childArgs []string, getenv func(string) string, resolve func(string) (string, error)) *control.DeliverOptions {
	if !hubExperimentListed(getenv, experiments.ConduitDeliver) {
		return nil
	}
	tmuxPath, err := resolve("tmux")
	if err != nil {
		log.Debug("Conduit: deliver-message not offered: tmux not found in %s", control.ToolPath)
		return nil
	}
	shPath, err := resolve("sh")
	if err != nil {
		log.Debug("Conduit: deliver-message not offered: sh not found in %s", control.ToolPath)
		return nil
	}
	if !harnessInScionSession(childArgs, getenv) {
		return nil
	}
	return &control.DeliverOptions{Runner: control.LocalRunner{
		UID:                  ptyUser.UID,
		GID:                  ptyUser.GID,
		Username:             ptyUser.Username,
		RequirePrivilegeDrop: ptyUser.RequirePrivilegeDrop,
		TmuxPath:             tmuxPath,
		ShPath:               shPath,
	}}
}

// hubExperimentListed reports whether name is in the hub's experiment
// list in the agent env (conduit.EnvHubExperiments).
func hubExperimentListed(getenv func(string) string, name string) bool {
	for _, n := range strings.Split(getenv(conduit.EnvHubExperiments), ",") {
		if strings.TrimSpace(n) == name {
			return true
		}
	}
	return false
}

// harnessInScionSession reports whether init's child command starts the
// harness in the tmux session conduit.DefaultPTYSession. It accepts only
// these shapes, and fails closed on anything else:
//
//   - argv is exactly [sh or /bin/sh, -c, S];
//   - S is the tmux start command, or, when argv[2] ends with
//     startCmdFUSESuffix, the value of envStartCmd;
//   - the fields of S begin exactly "tmux new-session -d -s scion -n",
//     the prefix buildTmuxStartCmd emits (pkg/runtime/common.go:280-293).
//
// The runtimes that produce these shapes: docker/podman
// (common.go:1054-1055, and FUSE common.go:1037-1052), Kubernetes (the
// gate shell expands $SCION_START_CMD before it execs init,
// k8s_runtime.go:2185-2186), Cloud Run (cloudrun_runtime.go:900-901),
// Cloud Run sandbox (cloudrun_sandbox_runtime.go:1609) and substrate
// (substrate_runtime.go:1423).
//
// Couples to the runtime's start-command format (common.go:280-293 and
// the per-runtime wrappers); a format change makes the route absent,
// never present. Replace with an explicit broker marker (S5/S6
// follow-up, ptone/scion#7353).
//
// It logs only which shape matched or why none did, at debug; never the
// command, which carries the harness command line.
func harnessInScionSession(childArgs []string, getenv func(string) string) bool {
	if len(childArgs) != 3 || (childArgs[0] != "sh" && childArgs[0] != "/bin/sh") || childArgs[1] != "-c" {
		log.Debug("Conduit: deliver-message not offered: child command is not a sh -c wrapper")
		return false
	}
	script, shape := childArgs[2], "sh -c"
	if strings.HasSuffix(script, startCmdFUSESuffix) {
		script, shape = getenv(envStartCmd), "sh -c with the start command in the environment"
		if script == "" {
			log.Debug("Conduit: deliver-message not offered: start command variable empty")
			return false
		}
	}
	want := []string{"tmux", "new-session", "-d", "-s", conduit.DefaultPTYSession, "-n"}
	fields := strings.Fields(script)
	if len(fields) < len(want) || !slices.Equal(fields[:len(want)], want) {
		log.Debug("Conduit: deliver-message not offered: start command (%s) does not create the %s tmux session", shape, conduit.DefaultPTYSession)
		return false
	}
	log.Debug("Conduit: deliver-message offered: harness in the %s tmux session (%s)", conduit.DefaultPTYSession, shape)
	return true
}
