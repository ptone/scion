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

package control

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/tmuxdeliver"
)

const (
	// runnerMaxOutput caps the stdout kept from one step; the readiness
	// probe prints a few short lines and the delivery steps print nothing.
	runnerMaxOutput = 64 << 10
	// runnerWaitDelay bounds the wait for a killed step's output pipes.
	runnerWaitDelay = 2 * time.Second
)

var errArgvNotAllowed = errors.New("control: command not allowed")

// allowedTmuxVerbs are the tmux commands a flush runs.
var allowedTmuxVerbs = map[string]bool{
	"has-session":   true,
	"load-buffer":   true,
	"paste-buffer":  true,
	"delete-buffer": true,
	"send-keys":     true,
}

// allowedArgv accepts only a tmux command or the exact readiness probe:
// exec.Command("tmux", fixed-verb, ...) with no shell and no
// caller-supplied argv (design r3 §5.3 exec item). The verbs come from
// tmuxdeliver's step builder and targetCheckArgv; any other verb (for
// example run-shell or if-shell) is refused (§5.3, "no tmux command
// passthrough").
func allowedArgv(argv []string) error {
	if len(argv) >= 2 && argv[0] == "tmux" && allowedTmuxVerbs[argv[1]] {
		return nil
	}
	if slices.Equal(argv, tmuxdeliver.ProbeArgv()) {
		return nil
	}
	return errArgvNotAllowed
}

// ToolPath is the fixed system PATH tmux and the probe's shell are
// resolved from, never this process's own PATH: a directory on that PATH
// the agent user can write would otherwise let a planted binary run with
// sciontool's identity (S2 review S-1).
const ToolPath = "/usr/bin:/bin"

// ResolveTool returns the absolute path of name in ToolPath: the first
// entry that is a regular file with an execute bit.
func ResolveTool(name string) (string, error) {
	return resolveToolIn(strings.Split(ToolPath, ":"), name)
}

func resolveToolIn(dirs []string, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '/') {
		return "", fmt.Errorf("control: invalid tool name %q", name)
	}
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		if err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("control: %s not found in %s", name, strings.Join(dirs, ":"))
}

// execPath maps a step's logical command name to the absolute path that
// is run.
func (r LocalRunner) execPath(name string) (string, error) {
	var p string
	switch name {
	case "tmux":
		p = r.TmuxPath
	case "sh":
		p = r.ShPath
	}
	if p == "" || !filepath.IsAbs(p) {
		return "", fmt.Errorf("control: no resolved path for %s", name)
	}
	return p, nil
}

// runnerError is the error for a failed step: the verb and the cause,
// never the argv tail (which can carry a buffer name) or the output.
func runnerError(argv []string, err error) error {
	verb := argv[0]
	if verb == "tmux" {
		verb = "tmux " + argv[1]
	}
	return fmt.Errorf("control: %s: %w", verb, err)
}

// cappedBuffer keeps at most max bytes and drops the rest.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }
