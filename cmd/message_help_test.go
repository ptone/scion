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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
)

// TestMessageHelpCoversAllRefForms asserts that every conversation-reference
// form accepted by messaging.ParseReference is documented in messageCmd.Long.
// The table entries are validated against the live parser, so they cannot drift
// from the implementation.
func TestMessageHelpCoversAllRefForms(t *testing.T) {
	// Go cannot enumerate iota constants. This is a tripwire, not coverage —
	// it fails loudly when a new ReferenceKind is added, which is the best
	// available guarantee that the table stays current.
	require.Equal(t, 4, int(messaging.RefThread),
		"ReferenceKind enum gained a member — add it to the table below AND to messageCmd.Long")

	table := []struct {
		Kind          messaging.ReferenceKind
		CanonicalInput string
		PrefixInHelp  string
	}{
		{messaging.RefAgent, "@test-agent", "@<agent"},
		{messaging.RefEmail, "@user@example.com", "@<email"},
		{messaging.RefConversation, "conv:00000000-0000-0000-0000-000000000000", "conv:<"},
		{messaging.RefThread, "#my-thread", "#<thread"},
	}

	long := messageCmd.Long

	for _, tc := range table {
		t.Run(tc.CanonicalInput, func(t *testing.T) {
			// (a) Validate the canonical input against the live parser.
			ref, err := messaging.ParseReference(tc.CanonicalInput)
			require.NoError(t, err, "ParseReference(%q) should succeed", tc.CanonicalInput)
			assert.Equal(t, tc.Kind, ref.Kind,
				"ParseReference(%q) returned unexpected Kind", tc.CanonicalInput)

			// (b) Assert the help text documents this form.
			assert.True(t, strings.Contains(long, tc.PrefixInHelp),
				"messageCmd.Long must contain %q for reference form %q",
				tc.PrefixInHelp, tc.CanonicalInput)
		})
	}

	// Rule 10: prove the help-text check is load-bearing. A fabricated prefix
	// that does not appear in Long must NOT match.
	t.Run("catches_missing_form", func(t *testing.T) {
		assert.False(t, strings.Contains(long, "xyz:<fake>"),
			"expected Long to NOT contain fabricated prefix xyz:<fake> — assertion mechanism is broken")
	})

	// CHANGE 2: assert that deprecation warnings referencing conversation forms
	// point to forms that are actually documented in Long.
	t.Run("deprecation_warnings_reference_documented_forms", func(t *testing.T) {
		// Patterns that indicate a deprecation message references a
		// conversation-reference form.
		refPatterns := []string{"@<", "conv:", "#<"}
		checked := 0
		for _, d := range deprecationReplacements {
			for _, pat := range refPatterns {
				if strings.Contains(d.Message, pat) {
					checked++
					assert.True(t, strings.Contains(long, pat),
						"deprecation for --%s says %q which references %q, but messageCmd.Long does not contain it",
						d.Flag, d.Message, pat)
				}
			}
		}
		require.Greater(t, checked, 0,
			"expected at least one deprecation replacement to reference a conversation form")
	})
}
