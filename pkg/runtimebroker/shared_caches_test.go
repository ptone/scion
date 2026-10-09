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

package runtimebroker

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/brokercredentials"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// P2.3 S4 (ptone/scion#3274): one cache object per directory for every
// instance of a host, whatever its runtime scope.

func newCacheTestServer(t *testing.T, brokerID, rtName string, sc *SharedCaches) *Server {
	t.Helper()
	cfg := DefaultServerConfig()
	cfg.BrokerID = brokerID
	cfg.StateDir = t.TempDir()
	cfg.HubEnabled = true
	cfg.HubEndpoint = "http://127.0.0.1:1"
	cfg.InMemoryCredentials = &brokercredentials.BrokerCredentials{BrokerID: brokerID, SecretKey: "c2VjcmV0", HubEndpoint: cfg.HubEndpoint}
	cfg.TemplateCacheDir = filepath.Join(t.TempDir(), "own", "templates")
	cfg.SharedCaches = sc
	return New(cfg, &mockManager{}, &runtime.MockRuntime{NameFunc: func() string { return rtName }})
}

// TestSharedCaches_OneObjectPerDirectoryAcrossScopes: two instances with
// different runtime scopes (Docker and Kubernetes) use the host's single
// cache objects, never their own; a server without shared caches still
// opens its own (single Runtime Broker unchanged).
func TestSharedCaches_OneObjectPerDirectoryAcrossScopes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sc, err := NewSharedCaches(filepath.Join(t.TempDir(), "cache", "templates"), 0)
	require.NoError(t, err)
	docker := newCacheTestServer(t, "rb-docker", "docker", sc)
	k8s := newCacheTestServer(t, "rb-k8s", "kubernetes", sc)
	for _, s := range []*Server{docker, k8s} {
		assert.Same(t, sc.Templates, s.cache)
		assert.Same(t, sc.HarnessConfigs, s.hcCache)
		assert.Same(t, sc.Skills, s.skCache)
		if sc.GitHub != nil {
			assert.Same(t, sc.GitHub, s.ghResolutionCache)
		}
	}
	solo := newCacheTestServer(t, "rb-solo", "docker", nil)
	require.NotNil(t, solo.cache)
	assert.NotSame(t, sc.Templates, solo.cache, "a server without shared caches opens its own")
}

// TestSharedCaches_DirectoriesAndEvictionRule: the shared caches use the
// single-broker directory layout and keep recently used entries from
// eviction.
func TestSharedCaches_DirectoriesAndEvictionRule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := filepath.Join(t.TempDir(), "cache")
	sc, err := NewSharedCaches(filepath.Join(base, "templates"), 100)
	require.NoError(t, err)
	p, err := sc.HarnessConfigs.Put("h1", map[string][]byte{"config.yaml": []byte("x")})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "harness-configs", "h1"), p)
	p, err = sc.Skills.Put("s1", map[string][]byte{"SKILL.md": []byte("x")})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "skills", "s1"), p)

	// A template just resolved (Put) is not evicted by another instance's
	// Put under pressure.
	big := map[string][]byte{"scion-agent.yaml": make([]byte, 60)}
	first, err := sc.Templates.Put("t1", big)
	require.NoError(t, err)
	_, err = sc.Templates.Put("t2", big)
	require.NoError(t, err)
	assert.DirExists(t, first, "an entry in use is kept (the cache exceeds its size instead)")
}

// TestSharedCaches_ClosedOnceByTheHostNotByInstances: an instance's
// shutdown never closes the shared GitHub resolution cache; the host's
// Stop does, once.
func TestSharedCaches_ClosedOnceByTheHostNotByInstances(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sc, err := NewSharedCaches(filepath.Join(t.TempDir(), "cache", "templates"), 0)
	require.NoError(t, err)
	require.NotNil(t, sc.GitHub, "the GitHub resolution cache opens under a writable HOME")
	a := newCacheTestServer(t, "rb-a", "docker", sc)
	require.NoError(t, a.Shutdown(context.Background()))
	assert.False(t, sc.GitHub.Closed(), "an instance's shutdown leaves the shared cache open")
	sc.Stop(context.Background())
	assert.True(t, sc.GitHub.Closed())
}
