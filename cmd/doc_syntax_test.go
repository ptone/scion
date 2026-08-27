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

// Parse-check validates that fenced `scion ...` code examples in docs resolve
// to real commands with valid flags. It does NOT verify that a command does what
// the prose says it does — only that the syntax is recognised by the cobra tree.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// extractScionLines returns every `scion ...` command from fenced code blocks.
func extractScionLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)
	var lines []string
	inFence := false
	for _, raw := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(raw), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		s := strings.TrimSpace(raw)
		s = strings.TrimPrefix(s, "$ ")
		s = strings.TrimPrefix(s, "# ") // shell prompt, not comment
		if strings.HasPrefix(s, "#") {   // comment line
			continue
		}
		if !strings.HasPrefix(s, "scion ") {
			continue
		}
		// Skip placeholders, usage patterns, shell interpolations, and
		// cobra built-ins (help/completion) that aren't discoverable via Find.
		if strings.ContainsAny(s, "<>$") || strings.Contains(s, "[flags]") || strings.Contains(s, "...") {
			continue
		}
		if strings.HasPrefix(s, "scion help ") || s == "scion help" {
			continue
		}
		lines = append(lines, s)
	}
	return lines
}

func TestDocSyntax(t *testing.T) {
	docFiles := []string{
		"../resources/platform_skills/scion-messaging/SKILL.md",
		"../docs-site/src/content/docs/hosted/user/messaging.md",
		"../docs-site/src/content/docs/reference/cli.md",
		"../docs-site/src/content/docs/glossary.md",
	}

	denyPatterns := []string{
		"scion message conv:", "scion message \"conv:",
		"scion message #", "scion message \"#",
		"scion msg conv:", "scion msg \"conv:",
		"scion msg #", "scion msg \"#",
	}

	for _, rel := range docFiles {
		abs, err := filepath.Abs(rel)
		require.NoError(t, err)
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			t.Logf("skipping missing file: %s", rel)
			continue
		}
		lines := extractScionLines(t, abs)
		for _, line := range lines {
			args := strings.Fields(line)[1:] // strip "scion"
			cmd, rest, err := rootCmd.Find(args)
			require.NoError(t, err, "command not found: %s (from %s)", line, rel)
			require.NoError(t, cmd.ParseFlags(rest), "flag parse failed: %s (from %s)", line, rel)
		}
		// Deny-list check.
		for _, line := range lines {
			for _, pat := range denyPatterns {
				assert.False(t, strings.Contains(line, pat),
					"deny-listed pattern %q in code block: %s (from %s)", pat, line, rel)
			}
		}
	}

	// Rule 10: prove parse-check catches bad syntax.
	t.Run("catches_bad_command", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "bad.md")
		require.NoError(t, os.WriteFile(tmp, []byte("```bash\nscion nonexistent-command --fake-flag\n```\n"), 0644))
		lines := extractScionLines(t, tmp)
		require.Len(t, lines, 1)
		args := strings.Fields(lines[0])[1:]
		_, _, err := rootCmd.Find(args)
		assert.Error(t, err, "expected parse-check to reject unknown command")
	})

	// Rule 10: prove deny-list catches gated forms.
	t.Run("catches_deny_listed_pattern", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "deny.md")
		require.NoError(t, os.WriteFile(tmp, []byte("```bash\nscion message conv:abc123 \"hello\"\n```\n"), 0644))
		lines := extractScionLines(t, tmp)
		require.Len(t, lines, 1)
		found := false
		for _, pat := range denyPatterns {
			if strings.Contains(lines[0], pat) {
				found = true
				break
			}
		}
		assert.True(t, found, "expected deny-list to catch gated conv: pattern")
	})
}
