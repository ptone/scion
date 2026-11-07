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

package control

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/procreap"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/suppgroups"
)

// runnerPassEnv are the variables of this process passed to tmux as they
// are: what it needs to find the server socket. Everything else (notably
// credentials, TMUX and this process's PATH) stays out of its
// environment, so the child holds nothing the route's caller could not
// already reach (design r3 §5.2 in-container process item). The child's
// PATH is the fixed ToolPath, so the probe script's commands resolve
// there too.
var runnerPassEnv = []string{"TMUX_TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "TZ"}

// LocalRunner runs the tmuxdeliver steps as local processes (design r3
// §3.4 Runner item), as the agent user that owns the tmux server: the same
// container user an in-container process already is, so the route adds
// no privilege (§5.2 in-container process item). It runs only tmux and the readiness probe
// (allowedArgv): no shell other than the probe's constant script, and
// nothing from a request reaches argv except through the tmuxdeliver
// step builder (§5.3 schema and exec items).
type LocalRunner struct {
	// UID and GID are the agent user; zero means this process's identity.
	UID, GID int
	// Username sets HOME, USER and LOGNAME for the child.
	Username string
	// RequirePrivilegeDrop refuses to run as this process's identity.
	RequirePrivilegeDrop bool
	// TmuxPath and ShPath are the absolute paths run for tmux and for the
	// readiness probe's shell, resolved once by ResolveTool. Run refuses
	// to start anything until both are set.
	TmuxPath, ShPath string
}

// Run implements tmuxdeliver.Runner. The child is started with
// exec.CommandContext, so ctx ending kills it; WaitDelay bounds how long
// Run then waits for its output pipes.
func (r LocalRunner) Run(ctx context.Context, argv []string, stdin io.Reader) (string, error) {
	if err := allowedArgv(argv); err != nil {
		return "", err
	}
	// Run as the agent user, never as sciontool's own (possibly root)
	// identity when a drop is required (§5.2 in-container process item).
	drop := r.UID > 0 && r.GID > 0
	if r.RequirePrivilegeDrop && !drop {
		return "", errors.New("control: privilege drop required but no agent user is set")
	}
	path, err := r.execPath(argv[0])
	if err != nil {
		return "", err
	}
	// Exec the resolved absolute path; argv[0] stays the logical name.
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Args[0] = argv[0]
	cmd.Env = runnerEnv(r.Username)
	cmd.Stdin = stdin
	out := &cappedBuffer{max: runnerMaxOutput}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = runnerWaitDelay
	if drop {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: suppgroups.Credential(uint32(r.UID), uint32(r.GID))}
	}
	if err := procreap.RunManaged(cmd); err != nil {
		return out.String(), runnerError(argv, err)
	}
	return out.String(), nil
}

func runnerEnv(username string) []string {
	env := []string{"PATH=" + ToolPath}
	for _, k := range runnerPassEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	if username != "" {
		env = append(env, "HOME=/home/"+username, "USER="+username, "LOGNAME="+username)
	} else if home, ok := os.LookupEnv("HOME"); ok {
		env = append(env, "HOME="+home)
	}
	return env
}
