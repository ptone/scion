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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// WorkspaceLocks coordinates operations on shared local paths (project
// directories, the shared worktree base, NFS workspace paths, agent
// directories) across every Runtime Broker server of one process
// (ptone/scion#3274, P2.3 S2). The flat host creates one and injects it into
// every instance (ServerConfig.WorkspaceLocks); a server without one gets
// its own, so a single Runtime Broker behaves as before.
//
// Keys are canonical paths (CanonicalWorkspacePath): absolute, cleaned and
// with symlinks resolved, also for paths that do not exist yet, so two
// spellings of one directory (a symlink alias, a relative path, a trailing
// separator) are one key. A lock on a path also excludes every path inside
// it and every directory containing it, so removing a project directory
// waits for (and blocks) work on any workspace under it.
//
// Lock takes all of an operation's paths at once or none of them: it never
// holds some keys while waiting for others, so two operations taking
// overlapping sets in any order cannot deadlock. A holder keeps the lock
// through its cleanup (the release function is called last).
//
// Locks coordinate operations; they do not establish ownership. Who may
// act on an agent or a path is decided by the ownership records (S1).
type WorkspaceLocks struct {
	mu      sync.Mutex
	held    map[string]int // canonical key -> holders (always 1 while held)
	changed chan struct{}  // closed and replaced on every release

	// users report, per server, whether it has live agents in a project
	// (a flat instance: its ownership records), so a project removal by one
	// server never removes a workspace another server still uses.
	usersMu sync.Mutex
	users   map[*Server]func(projectID string) (bool, error)
}

// NewWorkspaceLocks returns an empty lock service.
func NewWorkspaceLocks() *WorkspaceLocks {
	return &WorkspaceLocks{held: map[string]int{}, changed: make(chan struct{})}
}

// registerProjectUser records how server reports its live agents in a
// project; nil removes it.
func (l *WorkspaceLocks) registerProjectUser(s *Server, inUse func(projectID string) (bool, error)) {
	l.usersMu.Lock()
	defer l.usersMu.Unlock()
	if l.users == nil {
		l.users = map[*Server]func(string) (bool, error){}
	}
	if inUse == nil {
		delete(l.users, s)
		return
	}
	l.users[s] = inUse
}

// projectInUseByOthers reports whether a server other than self sharing this
// service has live agents in the project. A server whose answer cannot be
// read counts as using it.
func (l *WorkspaceLocks) projectInUseByOthers(self *Server, projectID string) bool {
	l.usersMu.Lock()
	defer l.usersMu.Unlock()
	for s, inUse := range l.users {
		if s == self {
			continue
		}
		if used, err := inUse(projectID); err != nil || used {
			return true
		}
	}
	return false
}

// locks returns the server's workspace lock service, creating a private one
// for a server built without New.
func (s *Server) locks() *WorkspaceLocks {
	s.workspaceLocksOnce.Do(func() {
		if s.workspaceLocks == nil {
			s.workspaceLocks = NewWorkspaceLocks()
		}
	})
	return s.workspaceLocks
}

// CanonicalWorkspacePath returns the lock key of path: absolute and
// cleaned, with every symlink in its longest existing prefix resolved and
// the not-yet-existing remainder appended, so the key a path has before it
// is created is the key it has afterwards.
func CanonicalWorkspacePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("workspace lock: empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("workspace lock: %w", err)
	}
	abs = filepath.Clean(abs)
	existing, rest := abs, []string{}
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			parts := append([]string{resolved}, rest...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("workspace lock: resolving %s: %w", existing, err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			// Nothing of the path exists (not even the root): use it as is.
			return abs, nil
		}
		rest = append([]string{filepath.Base(existing)}, rest...)
		existing = parent
	}
}

// overlaps reports whether a and b are the same path or one contains the
// other.
func overlaps(a, b string) bool {
	if a == b {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(a, strings.TrimSuffix(b, sep)+sep) || strings.HasPrefix(b, strings.TrimSuffix(a, sep)+sep)
}

// Lock acquires every path (empty paths are ignored) and returns the
// release function, which is safe to call more than once. It waits until
// no held key overlaps any requested key and then takes them all; it
// returns ctx's error (holding nothing) if ctx ends first. Nested Lock calls
// by one holder on overlapping paths deadlock, as with sync.Mutex: take
// every path an operation needs in one call.
func (l *WorkspaceLocks) Lock(ctx context.Context, paths ...string) (func(), error) {
	keys, err := canonicalKeys(paths)
	if err != nil {
		return nil, err
	}
	for {
		l.mu.Lock()
		if !l.conflictsLocked(keys) {
			for _, k := range keys {
				l.held[k]++
			}
			l.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { l.release(keys) }) }, nil
		}
		wait := l.changed
		l.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, fmt.Errorf("workspace lock on %s: %w", strings.Join(keys, ", "), ctx.Err())
		}
	}
}

func canonicalKeys(paths []string) ([]string, error) {
	seen := map[string]bool{}
	var keys []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		k, err := CanonicalWorkspacePath(p)
		if err != nil {
			return nil, err
		}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (l *WorkspaceLocks) conflictsLocked(keys []string) bool {
	for held := range l.held {
		for _, k := range keys {
			if overlaps(held, k) {
				return true
			}
		}
	}
	return false
}

func (l *WorkspaceLocks) release(keys []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if l.held[k]--; l.held[k] <= 0 {
			delete(l.held, k)
		}
	}
	close(l.changed)
	l.changed = make(chan struct{})
}

// agentFileLockPaths are the shared paths an agent's file operations
// (provisioning cleanup, DeleteAgentFiles, delete marks) touch: the
// project root, which holds the shared base clone, its worktrees and the
// agent directories, and the enclosing repository when it differs; the
// agent's external directory (split storage); and the global agents
// directory entry DeleteAgentFiles also removes. For the global project only
// the agent's own entries are taken, never the global directory itself.
func agentFileLockPaths(projectPath, agentName string) []string {
	projectDir, err := config.GetResolvedProjectDir(projectPath)
	if err != nil || projectDir == "" {
		projectDir = projectPath
	}
	var paths []string
	if config.IsGlobalProjectDir(projectDir) {
		paths = append(paths, filepath.Join(projectDir, "agents", agentName), filepath.Join(projectDir, "workspace", agentName))
	} else {
		root := projectDir
		if filepath.Base(projectDir) == config.DotScion {
			root = filepath.Dir(projectDir)
		}
		paths = append(paths, root)
		if repoRoot, err := util.RepoRootDir(projectDir); err == nil && repoRoot != "" {
			paths = append(paths, repoRoot)
		}
		if ext, err := config.GetGitProjectExternalAgentsDir(projectDir); err == nil && ext != "" {
			paths = append(paths, filepath.Join(ext, agentName))
		}
	}
	if globalAgents, err := config.GetGlobalAgentsDir(); err == nil && globalAgents != "" {
		paths = append(paths, filepath.Join(globalAgents, agentName))
	}
	return paths
}

// lockAgentFiles takes the process-wide workspace lock on every shared path
// an agent's file operations touch (agentFileLockPaths), all at once.
func (s *Server) lockAgentFiles(ctx context.Context, projectPath, agentName string) (func(), error) {
	if projectPath == "" || agentName == "" {
		return func() {}, nil
	}
	return s.locks().Lock(ctx, agentFileLockPaths(projectPath, agentName)...)
}
