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

package plugin

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManager(t *testing.T) {
	mgr := NewManager(nil)
	require.NotNil(t, mgr)
	assert.Empty(t, mgr.ListPlugins())
}

func TestManagerWithLogger(t *testing.T) {
	logger := slog.Default()
	mgr := NewManager(logger)
	require.NotNil(t, mgr)
}

func TestManagerHasPlugin_NotLoaded(t *testing.T) {
	mgr := NewManager(nil)
	assert.False(t, mgr.HasPlugin(PluginTypeBroker, "nats"))
	assert.False(t, mgr.HasPlugin(PluginTypeHarness, "cursor"))
}

func TestManagerGet_NotLoaded(t *testing.T) {
	mgr := NewManager(nil)

	_, err := mgr.Get(PluginTypeBroker, "nats")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "plugin not loaded")
}

func TestManagerGetBroker_NotLoaded(t *testing.T) {
	mgr := NewManager(nil)

	_, err := mgr.GetBroker("nats")
	assert.Error(t, err)
}

func TestManagerGetHarness_NotLoaded(t *testing.T) {
	mgr := NewManager(nil)

	_, err := mgr.GetHarness("cursor")
	assert.Error(t, err)
}

func TestManagerShutdown_Empty(t *testing.T) {
	mgr := NewManager(nil)
	mgr.Shutdown() // Should not panic
	assert.Empty(t, mgr.ListPlugins())
}

func TestManagerLoadAll_EmptyConfig(t *testing.T) {
	mgr := NewManager(nil)
	dir := t.TempDir()

	err := mgr.LoadAll(PluginsConfig{}, dir)
	assert.NoError(t, err)
	assert.Empty(t, mgr.ListPlugins())
}

func TestManagerLoadOne_MissingBinary(t *testing.T) {
	mgr := NewManager(nil)
	dir := t.TempDir()

	err := mgr.LoadOne(PluginTypeBroker, "nats", PluginEntry{}, dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestManagerGet_UnknownType(t *testing.T) {
	mgr := NewManager(nil)

	_, err := mgr.Get("unknown", "foo")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not loaded")
}
