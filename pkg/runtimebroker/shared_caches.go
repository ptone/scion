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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/templatecache"
)

// SharedCaches are a host's broker caches, opened once and shared by every
// Runtime Broker instance of the host, whatever its runtime scope
// (ptone/scion#3274, P2.3 S4). Two cache objects on one directory would
// keep separate indexes and evict each other's files; one object per
// directory serializes every index and file change under that object's
// own lock. The choice per cache:
//
//   - templates and harness-configs (templatecache): one shared object per
//     directory. A resolved path is read after the resolver returns (a
//     start copies the template later), so the shared objects keep every
//     entry used within SharedCacheMinEvictAge from eviction (the cache
//     exceeds its size rather than remove an entry in use), and Acquire
//     pins an entry for a caller that releases it.
//   - skills (templatecache): one shared object, same eviction rule; the
//     skill install additionally verifies the copied content's hash and
//     re-downloads on a mismatch.
//   - GitHub resolution (agent.GitHubResolutionCache, metadata only): one
//     shared object; its own locks serialize reads, refreshes and the
//     delayed write of its file. The host closes it once, after every
//     instance has shut down (it is a brokerhost.Service).
type SharedCaches struct {
	Templates      *templatecache.Cache
	HarnessConfigs *templatecache.Cache
	Skills         *templatecache.Cache
	GitHub         *agent.GitHubResolutionCache // nil when it cannot be opened (resolution runs uncached)
}

// SharedCacheMinEvictAge is how long a shared cache entry stays in use
// after its last Get or Put (see templatecache.Cache.SetMinEvictAge).
const SharedCacheMinEvictAge = 30 * time.Minute

// skillCacheMaxSize is the skill cache's size bound (as a single Runtime
// Broker uses).
const skillCacheMaxSize = int64(500 * 1024 * 1024)

// NewSharedCaches opens the host's caches in the directories a single
// Runtime Broker uses: templateDir (default ~/.scion/cache/templates) and its
// siblings harness-configs and skills, and the GitHub resolution cache.
func NewSharedCaches(templateDir string, maxSize int64) (*SharedCaches, error) {
	if templateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		templateDir = filepath.Join(home, ".scion", "cache", "templates")
	}
	if maxSize <= 0 {
		maxSize = templatecache.DefaultMaxSize
	}
	open := func(dir string, size int64) (*templatecache.Cache, error) {
		c, err := templatecache.New(dir, size)
		if err != nil {
			return nil, err
		}
		c.SetMinEvictAge(SharedCacheMinEvictAge)
		return c, nil
	}
	var sc SharedCaches
	var err error
	if sc.Templates, err = open(templateDir, maxSize); err != nil {
		return nil, fmt.Errorf("failed to initialize template cache: %w", err)
	}
	if sc.HarnessConfigs, err = open(filepath.Join(filepath.Dir(templateDir), "harness-configs"), maxSize); err != nil {
		return nil, fmt.Errorf("failed to initialize harness-config cache: %w", err)
	}
	if sc.Skills, err = open(filepath.Join(filepath.Dir(templateDir), "skills"), skillCacheMaxSize); err != nil {
		return nil, fmt.Errorf("failed to initialize skill cache: %w", err)
	}
	if dir, err := agent.GitHubResolutionCacheDir(); err != nil {
		slog.Warn("github resolution cache: cannot determine cache dir", "error", err)
	} else if gh, err := agent.NewGitHubResolutionCache(dir, agent.DefaultResolutionCacheTTL); err != nil {
		slog.Warn("github resolution cache: init failed (running uncached)", "error", err)
	} else {
		sc.GitHub = gh
	}
	return &sc, nil
}

// Start implements brokerhost.Service (the caches are already open).
func (c *SharedCaches) Start(context.Context) error { return nil }

// Stop closes the GitHub resolution cache once, after every instance has
// shut down (brokerhost.Service): it waits, within a bound, for background
// refreshes and writes entries still waiting for their delayed write.
func (c *SharedCaches) Stop(ctx context.Context) {
	if c.GitHub == nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ghResolutionCacheCloseTimeout)
	defer cancel()
	if err := c.GitHub.Close(closeCtx); err != nil {
		slog.Warn("GitHub resolution cache closed before background refreshes finished", "error", err)
	}
}
