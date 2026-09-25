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
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetFlagChanged clears the Changed bit on the named flags so a shared,
// package-level cobra command can be reused across test cases without
// carrying state from a previous ParseFlags call.
func resetFlagChanged(fs *pflag.FlagSet, names ...string) {
	for _, name := range names {
		if f := fs.Lookup(name); f != nil {
			f.Changed = false
		}
	}
}

// assertNoLocalProjectFlag asserts that cmd does not register its own
// --project flag distinct from the root's persistent --project/-g. It does
// not assert cmd.Flags().Lookup("project") == nil, because that lookup is
// order-dependent on a shared, package-level command: cobra's
// mergePersistentFlags copies the root's persistent flags directly into
// cmd's own FlagSet the first time cmd parses flags or is executed (see
// Command.ParseFlags / Command.Execute in spf13/cobra), and that merge is
// permanent for the process, not scoped to one test. Once any other test in
// this package has parsed flags on cmd, Lookup("project") always finds the
// merged-in root flag, regardless of run order. pflag's FlagSet.AddFlagSet
// only fills in names that are not already present, so a real local
// override (the regression this guards against) always wins over the
// merge and is never replaced by the inherited flag — checking pointer
// identity against the root's flag catches that regression in either
// ordering, while a plain nil check only catches it if this test happens
// to run before every test that triggers the merge.
func assertNoLocalProjectFlag(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	f := cmd.Flags().Lookup("project")
	if f == nil {
		return
	}
	assert.Same(t, rootCmd.PersistentFlags().Lookup("project"), f,
		"%s must not register a local --project flag distinct from the root's --project/-g", cmd.Name())
}

// TestSAAddCmd_GCPProjectFlagRenamed locks in the rename: the local --project
// flag (GCP project ID) became --gcp-project so it no longer shadows the root
// --project/-g scion-project selector. No alias for the old name is
// registered: an alias called "project" would recreate the shadowing.
func TestSAAddCmd_GCPProjectFlagRenamed(t *testing.T) {
	assert.NotNil(t, saAddCmd.Flags().Lookup("gcp-project"), "add command should register --gcp-project")
	assertNoLocalProjectFlag(t, saAddCmd)
}

// TestSAAddCmd_RootProjectAndGCPProjectFlagsParseIndependently is the
// regression test for the flag shadowing: with the local flag renamed,
// `-g <scion-project> --gcp-project <gcp>` must parse both values into
// their own variables instead of one shadowing the other.
func TestSAAddCmd_RootProjectAndGCPProjectFlagsParseIndependently(t *testing.T) {
	origProjectPath, origSAProjectID := projectPath, saProjectID
	defer func() { projectPath, saProjectID = origProjectPath, origSAProjectID }()
	defer resetFlagChanged(saAddCmd.Flags(), "project", "gcp-project")

	err := saAddCmd.ParseFlags([]string{"-g", "my-scion-project", "--gcp-project", "my-gcp-project"})
	require.NoError(t, err)

	assert.Equal(t, "my-scion-project", projectPath)
	assert.Equal(t, "my-gcp-project", saProjectID)
}

// TestSAAddCmd_OldProjectFlagHint verifies that `add ... --project my-gcp`
// (the pre-rename invocation) produces the targeted hint rather than a
// generic "required flag(s) ... not set" error.
func TestSAAddCmd_OldProjectFlagHint(t *testing.T) {
	origProjectPath, origSAProjectID := projectPath, saProjectID
	defer func() { projectPath, saProjectID = origProjectPath, origSAProjectID }()
	defer resetFlagChanged(saAddCmd.Flags(), "project", "gcp-project")

	err := saAddCmd.ParseFlags([]string{"--project", "my-gcp-project"})
	require.NoError(t, err)

	require.NotNil(t, saAddCmd.PreRunE, "add command must wire the hint check as PreRunE for it to actually run")
	err = saAddCmd.PreRunE(saAddCmd, nil)
	require.Error(t, err)
	assert.Equal(t, gcpProjectFlagHint, err.Error())
}

// TestSAAddCmd_NoHintWhenGCPProjectSet ensures the hint only fires for the
// old-flag case, not whenever --project happens to be set alongside a
// correctly-specified --gcp-project.
func TestSAAddCmd_NoHintWhenGCPProjectSet(t *testing.T) {
	origProjectPath, origSAProjectID := projectPath, saProjectID
	defer func() { projectPath, saProjectID = origProjectPath, origSAProjectID }()
	defer resetFlagChanged(saAddCmd.Flags(), "project", "gcp-project")

	err := saAddCmd.ParseFlags([]string{"-g", "my-scion-project", "--gcp-project", "my-gcp-project"})
	require.NoError(t, err)

	require.NotNil(t, saAddCmd.PreRunE)
	require.NoError(t, saAddCmd.PreRunE(saAddCmd, nil))
}

// TestSAAddCmd_NoHintWhenNoProjectFlags ensures a bare invocation, with
// neither --project nor --gcp-project set, does not trigger the hint: that
// case must fall through to cobra's normal required-flag error instead.
func TestSAAddCmd_NoHintWhenNoProjectFlags(t *testing.T) {
	origProjectPath, origSAProjectID := projectPath, saProjectID
	defer func() { projectPath, saProjectID = origProjectPath, origSAProjectID }()
	defer resetFlagChanged(saAddCmd.Flags(), "project", "gcp-project")

	err := saAddCmd.ParseFlags([]string{})
	require.NoError(t, err)

	require.NotNil(t, saAddCmd.PreRunE)
	require.NoError(t, saAddCmd.PreRunE(saAddCmd, nil))
}
