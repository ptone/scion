// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !no_sqlite

package hub

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The server-side log of a fail-closed resolution names the secret's scope,
// key and failure kind, and carries no value, Secret Manager path or backend
// message: on the as-needed pass, a provision and a start.
func TestFailClosedResolution_LogNamesKeyNotDetail(t *testing.T) {
	const want = "project/" + failClosedKey + " (permission_denied)"
	capture := func(f *runDispatchFixture) *bytes.Buffer {
		var buf bytes.Buffer
		f.d.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
		return &buf
	}
	check := func(t *testing.T, logged, line string) {
		t.Helper()
		assert.Contains(t, logged, line)
		assert.Contains(t, logged, want, "the log names scope, key and kind")
		assertNoFailClosedDetailExceptKey(t, logged, "the server log")
	}

	t.Run("as-needed pass", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.d.SetSecretBackend(newDeniedGCPBackend(t, f.store, f.agent.ProjectID))
		buf := capture(f)
		_, err := f.d.resolveAsNeededForKeys(context.Background(), f.agent, []string{failClosedKey}, nil)
		require.Error(t, err)
		check(t, buf.String(), "failing the as-needed pass")
	})
	t.Run("provision", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.d.SetSecretBackend(newDeniedGCPBackend(t, f.store, f.agent.ProjectID))
		gatherNeedsClient(f)
		buf := capture(f)
		require.Error(t, f.d.DispatchAgentProvision(context.Background(), f.agent))
		check(t, buf.String(), "failing the as-needed pass")
	})
	t.Run("start", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.d.SetSecretBackend(newDeniedGCPBackend(t, f.store, f.agent.ProjectID))
		buf := capture(f)
		require.Error(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		check(t, buf.String(), "failed to resolve secrets")
	})
}

// assertNoFailClosedDetailExceptKey is assertNoFailClosedDetail for the
// server log, which may name the key but nothing else.
func assertNoFailClosedDetailExceptKey(t *testing.T, text, what string) {
	t.Helper()
	for _, f := range []string{failClosedValue, failClosedSMPath, failClosedSMError, "projects/", fakeResolveBackendError} {
		assert.NotContains(t, text, f, "%s must not contain %q", what, f)
	}
}
