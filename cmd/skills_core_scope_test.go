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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSkillsCommands_CoreScopeRejected(t *testing.T) {
	t.Run("list --scope core rejected with global hint", func(t *testing.T) {
		isolateNoHubForTest(t, false)
		setCmdFlagForTest(t, skillsListCmd, "scope", "core")

		err := skillsListCmd.RunE(skillsListCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no longer supported")
		assert.Contains(t, err.Error(), `"global"`)
	})

	t.Run("publish --scope core rejected with global hint", func(t *testing.T) {
		isolateNoHubForTest(t, false)
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o644))
		setCmdFlagForTest(t, skillsPublishCmd, "version", "1.0.0")
		setCmdFlagForTest(t, skillsPublishCmd, "scope", "core")

		err := skillsPublishCmd.RunE(skillsPublishCmd, []string{dir})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no longer supported")
		assert.Contains(t, err.Error(), `"global"`)
	})

	t.Run("list --scope unknown rejected", func(t *testing.T) {
		isolateNoHubForTest(t, false)
		setCmdFlagForTest(t, skillsListCmd, "scope", "bogus")

		err := skillsListCmd.RunE(skillsListCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be one of global, project, user")
	})

	t.Run("flag help omits core", func(t *testing.T) {
		listScopeFlag := skillsListCmd.Flags().Lookup("scope")
		require.NotNil(t, listScopeFlag)
		assert.NotContains(t, listScopeFlag.Usage, "core")
		assert.Contains(t, listScopeFlag.Usage, "global, project, user")

		publishScopeFlag := skillsPublishCmd.Flags().Lookup("scope")
		require.NotNil(t, publishScopeFlag)
		assert.NotContains(t, publishScopeFlag.Usage, "core")
		assert.Contains(t, publishScopeFlag.Usage, "global, project, user")
	})
}
