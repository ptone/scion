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
	"errors"
	"io"
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateTestTree copies the real command tree's names and aliases into a
// fresh tree whose every command records that it ran, so the platform gate
// can be applied and executed without touching the shared rootCmd. The
// root gets cobra's default help and completion commands, which the real
// root only adds when it executes.
func gateTestTree(t *testing.T) (root *cobra.Command, ran *[]string) {
	t.Helper()
	ran = &[]string{}
	var clone func(src *cobra.Command, path string) *cobra.Command
	clone = func(src *cobra.Command, path string) *cobra.Command {
		c := &cobra.Command{
			Use:     src.Name(),
			Aliases: append([]string(nil), src.Aliases...),
			Run:     func(*cobra.Command, []string) { *ran = append(*ran, path) },
		}
		for _, child := range src.Commands() {
			childPath := child.Name()
			if path != "" {
				childPath = path + "." + childPath
			}
			c.AddCommand(clone(child, childPath))
		}
		return c
	}
	root = clone(rootCmd, "")
	root.SilenceErrors, root.SilenceUsage = true, true
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	return root, ran
}

// gateState is what the gate changes on one command.
type gateState struct {
	hidden, hasRun, hasRunE bool
}

func gateStates(root *cobra.Command) map[string]gateState {
	states := map[string]gateState{}
	var walk func(cmd *cobra.Command, prefix string)
	walk = func(cmd *cobra.Command, prefix string) {
		for _, child := range cmd.Commands() {
			path := child.Name()
			if prefix != "" {
				path = prefix + "." + path
			}
			states[path] = gateState{hidden: child.Hidden, hasRun: child.Run != nil, hasRunE: child.RunE != nil}
			walk(child, path)
		}
	}
	walk(root, "")
	return states
}

func TestWindowsGate_RefusesServerAndBroker(t *testing.T) {
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"server", "start"}, "server start"},
		{[]string{"broker", "join"}, "runtime-broker join"}, // via the alias
		{[]string{"runtime-broker", "start"}, "runtime-broker start"},
		{[]string{"whoami"}, "whoami"},
		{[]string{"expose"}, "expose"},
		{[]string{"server"}, "server"},
		// Arguments and flags the real command would reject still get the
		// refusal, not a usage error.
		{[]string{"server", "start", "extra-arg", "--no-such-flag"}, "server start"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			root, ran := gateTestTree(t)
			applyPlatformRestrictions(root, "windows")
			root.SetArgs(tc.args)

			err := root.Execute()

			require.Error(t, err)
			assert.True(t, errors.Is(err, errUnsupportedOnWindows), "got %v", err)
			assert.Equal(t, "scion "+tc.path+" is not supported on Windows; the Windows build supports hub client commands only (see docs: Windows)", err.Error())
			assert.Empty(t, *ran, "a gated command must not run")
		})
	}

	// Positive controls: allowlisted commands still run on Windows.
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"version"}, "version"},
		{[]string{"hub", "status"}, "hub.status"},
		{[]string{"tunnel", "status"}, "tunnel.status"},
	} {
		t.Run("runs "+tc.path, func(t *testing.T) {
			root, ran := gateTestTree(t)
			applyPlatformRestrictions(root, "windows")
			root.SetArgs(tc.args)

			require.NoError(t, root.Execute())
			assert.Equal(t, []string{tc.path}, *ran)
		})
	}
}

func TestWindowsGate_HidesOnlyGatedCommands(t *testing.T) {
	root, _ := gateTestTree(t)
	applyPlatformRestrictions(root, "windows")

	for path, st := range gateStates(root) {
		if windowsAllowed[path] {
			assert.False(t, st.hidden, "%s is allowed and must stay visible", path)
			assert.True(t, st.hasRun, "%s is allowed and must keep its run function", path)
			continue
		}
		assert.True(t, st.hidden, "%s is gated and must be hidden", path)
		assert.False(t, st.hasRun, "%s is gated and must have no Run", path)
		assert.True(t, st.hasRunE, "%s is gated and must refuse through RunE", path)
	}
}

// TestWindowsGate_AllowlistEntriesExist keeps the allowlist free of stale
// entries: every key must name a real command.
func TestWindowsGate_AllowlistEntriesExist(t *testing.T) {
	root, _ := gateTestTree(t)
	var missing []string
	for path := range windowsAllowed {
		if resolveCommandPath(root, path) == nil {
			missing = append(missing, path)
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "windowsAllowed keys that are not real commands")
}

func TestWindowsGate_NoopOnUnix(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			root, ran := gateTestTree(t)
			before := gateStates(root)

			applyPlatformRestrictions(root, goos)

			assert.Equal(t, before, gateStates(root), "the tree must be unchanged on %s", goos)
			root.SetArgs([]string{"server", "start"})
			require.NoError(t, root.Execute())
			assert.Equal(t, []string{"server.start"}, *ran)
		})
	}
}
