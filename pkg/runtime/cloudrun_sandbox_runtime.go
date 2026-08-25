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

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/projectcompat"
)

const defaultSandboxBin = "/usr/local/gcp/bin/sandbox"

// defaultScionRoot is the parent directory for all writable sandbox paths.
// A single bind mount of this directory satisfies the write-visibility
// requirement for agent home, workspace, tmux sockets, and shared dirs.
// With --rootfs /, writes to unmounted paths go to a private rootfs overlay
// that the launcher never sees (§3.2a). This mount makes them visible.
const defaultScionRoot = "/scion"

// defaultStatePath is the default location for the sandbox state store.
const defaultStatePath = "/tmp/scion-sandbox-state.json"

// SandboxLauncherAvailable reports whether the Cloud Run Sandbox launcher
// binary is present on the filesystem.
func SandboxLauncherAvailable() bool {
	_, err := os.Stat(defaultSandboxBin)
	return err == nil
}

// -----------------------------------------------------------------------
// State Store
//
// The sandbox CLI has no `list` command. The runtime maintains its own
// state in a JSON file (design doc §4.5).
// -----------------------------------------------------------------------

// sandboxStateEntry tracks a single sandbox's lifecycle.
type sandboxStateEntry struct {
	SandboxName   string            `json:"sandbox_name"`
	AgentID       string            `json:"agent_id"`
	ProjectID     string            `json:"project_id"`
	Project       string            `json:"project"`
	Labels        map[string]string `json:"labels"`
	Template      string            `json:"template"`
	HarnessConfig string            `json:"harness_config"`
	CreatedAt     time.Time         `json:"created_at"`
	TmuxSocket    string            `json:"tmux_socket"`
	AgentHome     string            `json:"agent_home"`
	Workspace     string            `json:"workspace"`
	Image         string            `json:"image"`
	Stopped       bool              `json:"stopped"`
	ExitCode      *int              `json:"exit_code,omitempty"`
	StoppedAt     *time.Time        `json:"stopped_at,omitempty"`
}

// sandboxStateStore is a thread-safe, JSON-backed store of sandbox entries.
type sandboxStateStore struct {
	mu      sync.Mutex
	path    string
	entries map[string]*sandboxStateEntry
}

func newSandboxStateStore(path string) *sandboxStateStore {
	ss := &sandboxStateStore{
		path:    path,
		entries: make(map[string]*sandboxStateEntry),
	}
	ss.load()
	return ss
}

// add inserts or replaces an entry and persists the store.
func (s *sandboxStateStore) add(entry *sandboxStateEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[entry.SandboxName] = entry
	s.save()
}

// remove deletes an entry by sandbox name and persists the store.
func (s *sandboxStateStore) remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, name)
	s.save()
}

// get returns an entry by sandbox name, or nil if not found.
func (s *sandboxStateStore) get(name string) *sandboxStateEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[name]
}

// list returns a snapshot of all entries.
func (s *sandboxStateStore) list() []*sandboxStateEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*sandboxStateEntry, 0, len(s.entries))
	for _, e := range s.entries {
		cp := *e
		result = append(result, &cp)
	}
	return result
}

// markStopped records that a sandbox has exited.
func (s *sandboxStateStore) markStopped(name string, exitCode *int, stoppedAt *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[name]; ok {
		e.Stopped = true
		e.ExitCode = exitCode
		e.StoppedAt = stoppedAt
		s.save()
	}
}

// save persists the store to disk. Caller must hold mu.
func (s *sandboxStateStore) save() {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		runtimeLog.Warn("sandbox state store: failed to marshal", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		runtimeLog.Warn("sandbox state store: failed to create dir", "error", err)
		return
	}
	if err := os.WriteFile(s.path, data, 0644); err != nil {
		runtimeLog.Warn("sandbox state store: failed to write", "path", s.path, "error", err)
	}
}

// load reads the store from disk. Caller must hold mu (or be in init).
func (s *sandboxStateStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			runtimeLog.Warn("sandbox state store: failed to read", "path", s.path, "error", err)
		}
		return
	}
	var entries map[string]*sandboxStateEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		runtimeLog.Warn("sandbox state store: failed to unmarshal", "path", s.path, "error", err)
		return
	}
	s.entries = entries
}

// reconcile checks each entry for liveness and removes confirmed-dead
// entries. Called once at startup. If the file is stale from a previous
// Instance, all entries are dead and will be pruned.
func (s *sandboxStateStore) reconcile(bin string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.entries) == 0 {
		return
	}

	var toRemove []string
	for name, entry := range s.entries {
		if entry.Stopped {
			toRemove = append(toRemove, name)
			continue
		}
		// Probe liveness: try to exec 'true' in the sandbox.
		// If it fails, the sandbox is dead.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := runSimpleCommand(ctx, bin, "exec", name, "--", "true")
		cancel()
		if err != nil {
			runtimeLog.Info("sandbox state reconcile: sandbox not alive, removing",
				"name", name, "agentID", entry.AgentID)
			toRemove = append(toRemove, name)
		}
	}

	for _, name := range toRemove {
		delete(s.entries, name)
	}

	if len(toRemove) > 0 {
		s.save()
	}
}

// -----------------------------------------------------------------------
// Sandbox name helpers
// -----------------------------------------------------------------------

// sandboxNameRe matches characters NOT allowed in sandbox names.
var sandboxNameRe = regexp.MustCompile(`[^a-z0-9-]`)

// sanitizeSandboxName produces a safe sandbox name from an agent slug.
func sanitizeSandboxName(name string) string {
	s := strings.ToLower(name)
	s = sandboxNameRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "sandbox"
	}
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

// -----------------------------------------------------------------------
// /scion directory layout
// -----------------------------------------------------------------------

// scionPaths holds the resolved /scion paths for a single sandbox agent.
type scionPaths struct {
	root      string // e.g. /scion
	agentHome string // /scion/agents/<slug>/home
	workspace string // /scion/agents/<slug>/workspace
	tmuxDir   string // /scion/agents/<slug>/tmux
}

// prepareScionLayout creates the /scion directory structure for a sandbox
// agent and relocates broker-provisioned content into it.
//
// Motivation (§3.2a): --rootfs / is READ-ONLY. Writes go to a private
// overlay the launcher never sees. --write only makes MOUNTED filesystems
// writable. A single bind mount of /scion makes all writable paths
// visible to the host.
func prepareScionLayout(rootDir, slug string, cfg RunConfig) (scionPaths, error) {
	p := scionPaths{
		root:      rootDir,
		agentHome: filepath.Join(rootDir, "agents", slug, "home"),
		workspace: filepath.Join(rootDir, "agents", slug, "workspace"),
		tmuxDir:   filepath.Join(rootDir, "agents", slug, "tmux"),
	}

	// Create all directories.
	for _, d := range []string{p.agentHome, p.workspace, p.tmuxDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return p, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	// Relocate agent home to /scion and symlink back for broker readback.
	// The broker provisions agent-info.json etc. at config.HomeDir. The
	// symlink ensures the broker reads from the /scion copy where sandbox
	// writes are visible via the bind mount.
	if cfg.HomeDir != "" && !strings.HasPrefix(cfg.HomeDir, rootDir+"/") && cfg.HomeDir != rootDir {
		if err := relocateToScion(cfg.HomeDir, p.agentHome); err != nil {
			runtimeLog.Warn("could not relocate HomeDir to /scion; agent home writes may go to overlay",
				"homeDir", cfg.HomeDir, "scionHome", p.agentHome, "error", err)
		}
	}

	// Workspace: copy content from broker-provisioned workspace to /scion.
	// Do NOT remove the original — it may be shared among agents.
	if cfg.Workspace != "" && !strings.HasPrefix(cfg.Workspace, rootDir+"/") && cfg.Workspace != rootDir {
		if err := copyDirContents(cfg.Workspace, p.workspace); err != nil {
			// Non-fatal: agent can still read via rootfs; writes go to /scion.
			runtimeLog.Debug("workspace content not copied to /scion",
				"workspace", cfg.Workspace, "scionWorkspace", p.workspace, "error", err)
		}
	}

	// Create shared dirs under /scion/shared/<name>.
	for _, sd := range cfg.SharedDirs {
		sdPath := filepath.Join(rootDir, "shared", sd.Name)
		if err := os.MkdirAll(sdPath, 0755); err != nil {
			runtimeLog.Warn("failed to create shared dir in /scion layout",
				"name", sd.Name, "error", err)
		}
	}

	return p, nil
}

// relocateToScion moves the contents of src to dst and replaces src with
// a symlink to dst. If src does not exist or is already a symlink, this
// is a no-op.
func relocateToScion(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		// src doesn't exist — nothing to relocate.
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Already a symlink (perhaps from a previous run).
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("relocateToScion: %s is not a directory", src)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("readdir %s: %w", src, err)
	}

	// Move each entry from src to dst (atomic on same filesystem).
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if err := os.Rename(srcPath, dstPath); err != nil {
			// Cross-filesystem: fall back to not relocating this entry.
			runtimeLog.Debug("could not rename to /scion, skipping",
				"src", srcPath, "dst", dstPath, "error", err)
		}
	}

	// Replace original directory with symlink so broker readback works.
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("remove original dir %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(src), 0755); err != nil {
		return fmt.Errorf("ensure parent of symlink %s: %w", src, err)
	}
	if err := os.Symlink(dst, src); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", src, dst, err)
	}
	return nil
}

// copyDirContents copies files from src to dst (non-recursive, best-effort).
func copyDirContents(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			// Shallow: skip subdirectories for now.
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			continue
		}
		info, _ := entry.Info()
		perm := fs.FileMode(0644)
		if info != nil {
			perm = info.Mode().Perm()
		}
		_ = os.WriteFile(dstPath, data, perm)
	}
	return nil
}

// -----------------------------------------------------------------------
// Mount and environment construction
// -----------------------------------------------------------------------

// mountArg returns the single --mount argument for the /scion bind mount.
func mountArg(rootDir string) string {
	return fmt.Sprintf("type=bind,source=%s,destination=%s", rootDir, rootDir)
}

// envFor builds the environment variable map for a sandbox agent.
// These are passed via /usr/bin/env (not --env flags) because the sandbox
// CLI declares --env as 'string' not 'stringArray', so repeating the flag
// may overwrite rather than accumulate.
func envFor(cfg RunConfig, paths scionPaths) map[string]string {
	env := make(map[string]string)

	// Parse broker-provided env vars.
	for _, e := range cfg.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}

	// Harness-specific env vars.
	if cfg.Harness != nil {
		for k, v := range cfg.Harness.GetEnv(cfg.Name, paths.agentHome, cfg.UnixUsername) {
			env[k] = v
		}
	}

	// TMUX socket directory — must be under the /scion mount so the tmux
	// socket file is visible to the host (P4 attach).
	env["TMUX_TMPDIR"] = paths.tmuxDir

	// Project identity.
	if cfg.Project != "" {
		env["SCION_PROJECT"] = cfg.Project
		env["SCION_GROVE"] = cfg.Project
	}
	if cfg.ProjectID != "" {
		env["SCION_PROJECT_ID"] = cfg.ProjectID
		env["SCION_GROVE_ID"] = cfg.ProjectID
	}

	// Workspace backend.
	if cfg.WorkspaceBackendName != "" {
		env["SCION_WORKSPACE_BACKEND"] = cfg.WorkspaceBackendName
	}

	// Host UID/GID for container user synchronisation.
	uid, gid := os.Getuid(), os.Getgid()
	env["SCION_HOST_UID"] = strconv.Itoa(uid)
	env["SCION_HOST_GID"] = strconv.Itoa(gid)

	return env
}

// envArgs converts an env map to a sorted list of KEY=VALUE strings
// suitable for /usr/bin/env.
func envArgs(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	args := make([]string, 0, len(keys))
	for _, k := range keys {
		args = append(args, fmt.Sprintf("%s=%s", k, env[k]))
	}
	return args
}

// -----------------------------------------------------------------------
// Entrypoint construction
// -----------------------------------------------------------------------

// buildEntrypoint constructs the process tree that runs inside the sandbox:
//
//	sciontool init -- sh -c '<tmux-command>'
//
// The tmux command follows the pattern from common.go:469-482:
//
//	tmux new-session -d -s scion -n agent <agent-window-cmd> \;
//	    set-option -g window-size latest \;
//	    new-window -t scion -n shell \;
//	    select-window -t scion:agent \;
//	    attach-session -t scion
func buildEntrypoint(cfg RunConfig) ([]string, error) {
	// Build the harness command line.
	var cmdLine string
	if cfg.NoAuth {
		cmdLine = buildNoAuthCmdLine(cfg.NoAuthMessage, cfg.NoAuthCommand)
	} else if cfg.Harness != nil {
		harnessArgs := cfg.Harness.GetCommand(cfg.Task, cfg.Resume, cfg.CommandArgs)
		var quotedArgs []string
		for _, a := range harnessArgs {
			quotedArgs = append(quotedArgs, shellQuote(a))
		}
		cmdLine = strings.Join(quotedArgs, " ")
	} else {
		return nil, fmt.Errorf("cloudrun-sandbox: no harness provided")
	}

	// Wrap the harness in a shell that records its real exit code (see
	// common.go:469-475 for the pattern).
	agentWindowCmd := "sh -c " + shellQuote(cmdLine+"; echo $? > "+state.HarnessExitCodeFile)

	// Build tmux command (common.go:479-482 pattern).
	tmuxCmd := fmt.Sprintf(
		"tmux new-session -d -s scion -n agent %s \\; set-option -g window-size latest \\; new-window -t scion -n shell \\; select-window -t scion:agent \\; attach-session -t scion",
		agentWindowCmd,
	)

	return []string{"sciontool", "init", "--", "sh", "-c", tmuxCmd}, nil
}

// -----------------------------------------------------------------------
// CloudRunSandboxRuntime
// -----------------------------------------------------------------------

// CloudRunSandboxRuntime implements the Runtime interface for Cloud Run
// Sandboxes. Sandboxes are nested isolated workloads launched from inside
// a Cloud Run Instance via the `sandbox` CLI binary.
//
// Architecture:
//   - All writable paths live under /scion (single bind mount).
//   - Environment is injected via /usr/bin/env (not --env flags).
//   - State is tracked locally in a JSON file (no `sandbox list` command).
//   - A watcher goroutine runs `sandbox wait` to detect exits.
type CloudRunSandboxRuntime struct {
	bin     string             // path to sandbox binary
	state   *sandboxStateStore // local state (sandbox CLI has no list command)
	rootDir string             // /scion by default; overridable for testing
}

// NewCloudRunSandboxRuntime returns a new CloudRunSandboxRuntime.
func NewCloudRunSandboxRuntime(cfg *config.V1CloudRunSandboxConfig) *CloudRunSandboxRuntime {
	bin := defaultSandboxBin
	if cfg != nil && cfg.SandboxBin != "" {
		bin = cfg.SandboxBin
	}

	ss := newSandboxStateStore(defaultStatePath)
	ss.reconcile(bin)

	return &CloudRunSandboxRuntime{
		bin:     bin,
		state:   ss,
		rootDir: defaultScionRoot,
	}
}

func (r *CloudRunSandboxRuntime) Name() string { return "cloudrun-sandbox" }

func (r *CloudRunSandboxRuntime) ExecUser() string { return "scion" }

// Run launches an agent inside a sandbox. See brief §Deliverable 2.
func (r *CloudRunSandboxRuntime) Run(ctx context.Context, cfg RunConfig) (string, error) {
	slug := sanitizeSandboxName(cfg.Name)

	// C7 (§4.3c): Vertex AI and gcloud-adc auth modes are structurally
	// unavailable on this runtime. --allow-egress grants network access
	// but NOT GCP service access. Log a warning but do not reject — let
	// the operator decide.
	if cfg.ResolvedAuth != nil {
		method := cfg.ResolvedAuth.Method
		if method == "vertex-ai" || strings.Contains(strings.ToLower(method), "gcloud") {
			runtimeLog.Warn("cloudrun-sandbox: auth mode may not work in sandbox — "+
				"--allow-egress does not grant GCP service access (§4.3c)",
				"method", method, "agent", cfg.Name)
		}
	}

	// Prepare the /scion directory layout (§3.2a: rootfs is read-only;
	// only mounted paths are writable).
	paths, err := prepareScionLayout(r.rootDir, slug, cfg)
	if err != nil {
		return "", fmt.Errorf("cloudrun-sandbox: prepare layout: %w", err)
	}

	// Build environment.
	env := envFor(cfg, paths)

	// Build entrypoint command.
	entrypoint, err := buildEntrypoint(cfg)
	if err != nil {
		return "", err
	}

	// Build the sandbox run command.
	// C1: No `create` verb. Use `sandbox run <name> --detach --rootfs / --write --allow-egress`.
	// C3: --allow-egress is MANDATORY.
	args := []string{
		"run", slug,
		"--detach",
		"--rootfs", "/",
		"--write",
		"--allow-egress",
		"--mount", mountArg(r.rootDir),
		"--",
		"/usr/bin/env",
	}
	args = append(args, envArgs(env)...)
	args = append(args, entrypoint...)

	WriteRuntimeDebugFile(cfg, r.bin, args)

	out, err := runSimpleCommand(ctx, r.bin, args...)
	if err != nil {
		return "", fmt.Errorf("cloudrun-sandbox: run failed: %w (output: %s)", err, out)
	}

	// Record in state store.
	entry := &sandboxStateEntry{
		SandboxName:   slug,
		AgentID:       cfg.Name,
		ProjectID:     cfg.ProjectID,
		Project:       cfg.Project,
		Labels:        cfg.Labels,
		Template:      cfg.Template,
		HarnessConfig: labelValue(cfg.Labels, "scion.harness_config"),
		CreatedAt:     time.Now(),
		TmuxSocket:    paths.tmuxDir,
		AgentHome:     paths.agentHome,
		Workspace:     paths.workspace,
		Image:         cfg.Image,
	}
	r.state.add(entry)

	// Start a watcher goroutine that blocks on `sandbox wait` and records
	// the exit in the state store when the sandbox terminates.
	go r.watchSandbox(slug)

	return slug, nil
}

// Stop performs a graceful delete (no --force). The sandbox CLI has no
// stop/pause verb; Stop==Delete when the platform has no pause verb
// (K8s precedent). Kept as a separate method so Delete can escalate
// with --force if the graceful attempt fails.
func (r *CloudRunSandboxRuntime) Stop(ctx context.Context, id string) error {
	_, err := runSimpleCommand(ctx, r.bin, "delete", id)
	if err != nil {
		return fmt.Errorf("cloudrun-sandbox: stop (graceful delete) failed: %w", err)
	}
	r.state.remove(id)
	return nil
}

// Delete removes a sandbox, escalating with --force if needed.
func (r *CloudRunSandboxRuntime) Delete(ctx context.Context, id string) error {
	// Attempt graceful delete first.
	_, err := runSimpleCommand(ctx, r.bin, "delete", id)
	if err != nil {
		// Escalate with --force (§4.3 grace period).
		runtimeLog.Info("cloudrun-sandbox: graceful delete failed, escalating with --force",
			"sandbox", id, "error", err)
		_, err = runSimpleCommand(ctx, r.bin, "delete", "--force", id)
		if err != nil {
			return fmt.Errorf("cloudrun-sandbox: forced delete failed: %w", err)
		}
	}
	r.state.remove(id)
	return nil
}

// List returns agent info for all tracked sandboxes, applying an optional
// label filter.
func (r *CloudRunSandboxRuntime) List(ctx context.Context, labelFilter map[string]string) ([]api.AgentInfo, error) {
	entries := r.state.list()

	var agents []api.AgentInfo
	for _, entry := range entries {
		// Label filtering (same pattern as docker.go:182-201).
		match := true
		for k, v := range labelFilter {
			actual := entry.Labels[k]
			if actual == "" {
				switch k {
				case projectcompat.LabelProject:
					actual = projectcompat.ProjectNameFromLabels(entry.Labels)
				case projectcompat.LabelProjectID:
					actual = projectcompat.ProjectIDFromLabels(entry.Labels)
				}
			}
			if actual != v {
				match = false
				break
			}
		}
		if !match {
			continue
		}

		statusStr := "running"
		phase := "running"
		if entry.Stopped {
			statusStr = "stopped"
			phase = "stopped"
		}

		// STOPGAP (§9.1b): Report ExitCode=nil for stopped sandboxes.
		// Non-zero ExitCode is hardcoded to PhaseError at handlers_runtime_brokers.go:719,
		// and Instance teardown (our normal shutdown) SIGKILLs every sandbox simultaneously.
		// Reporting the real code would put the entire fleet into PhaseError on every
		// normal teardown. Once ExitReason can modulate the phase decision (Phase 2 of
		// #1257), we should start reporting the real exit code here.
		//
		// The state store tracks the real exit code internally (in sandboxStateEntry)
		// so it's available when #1260 merges and ExitReason is wired.
		agents = append(agents, api.AgentInfo{
			ContainerID:     entry.SandboxName,
			Name:            entry.AgentID,
			ContainerStatus: statusStr,
			Phase:           phase,
			Image:           entry.Image,
			Labels:          entry.Labels,
			Template:        entry.Template,
			HarnessConfig:   entry.HarnessConfig,
			Project:         entry.Project,
			ProjectID:       entry.ProjectID,
			Runtime:         r.Name(),
		})
	}

	return agents, nil
}

func (r *CloudRunSandboxRuntime) GetLogs(ctx context.Context, id string) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: GetLogs not yet implemented")
}

func (r *CloudRunSandboxRuntime) Attach(ctx context.Context, id string) error {
	// C4: Attach remains a stub (P4 scope). The sandbox CLI has no --user
	// flag, so exec/attach would give a root shell. P4 will implement
	// attach via the tmux socket.
	return fmt.Errorf("cloudrun-sandbox: Attach not yet implemented")
}

// ImageExists returns true — the omni-image is always present.
func (r *CloudRunSandboxRuntime) ImageExists(ctx context.Context, image string) (bool, error) {
	return true, nil
}

// ImageID returns a fixed identifier — the omni-image is always present.
func (r *CloudRunSandboxRuntime) ImageID(ctx context.Context, image string) (string, error) {
	return "omni-image", nil
}

// RemoveImage is a no-op — the omni-image cannot be removed.
func (r *CloudRunSandboxRuntime) RemoveImage(ctx context.Context, image string) error {
	return nil
}

// PullImage is a no-op — the omni-image is already present.
func (r *CloudRunSandboxRuntime) PullImage(ctx context.Context, image string) error {
	return nil
}

// Sync is a no-op — the filesystem is shared via bind mounts. The
// sandbox uses --rootfs / (the launcher's filesystem) and the /scion
// bind mount makes writes visible to both sides. There is nothing
// additional to synchronise.
func (r *CloudRunSandboxRuntime) Sync(ctx context.Context, id string, direction SyncDirection) error {
	return nil
}

// Exec remains a stub (P4 scope).
// C4: The sandbox CLI has no --user flag, so exec would give a root
// shell. P4 will implement exec with proper privilege handling.
func (r *CloudRunSandboxRuntime) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: Exec not yet implemented")
}

// GetWorkspacePath returns the launcher-side workspace path from the
// state store. Since bind mounts use the same paths inside and outside
// the sandbox (both are under /scion), this is the /scion workspace path.
func (r *CloudRunSandboxRuntime) GetWorkspacePath(ctx context.Context, id string) (string, error) {
	entry := r.state.get(id)
	if entry == nil {
		return "", fmt.Errorf("cloudrun-sandbox: sandbox %q not found in state store", id)
	}
	return entry.Workspace, nil
}

// -----------------------------------------------------------------------
// Watcher goroutine
// -----------------------------------------------------------------------

// watchSandbox blocks on `sandbox wait <name>` and updates the state
// store when the sandbox exits.
func (r *CloudRunSandboxRuntime) watchSandbox(name string) {
	// sandbox wait blocks until the sandbox exits.
	cmd := exec.Command(r.bin, "wait", name)
	out, err := cmd.CombinedOutput()

	now := time.Now()
	var exitCode *int

	if err != nil {
		// C5 (§4.3d): sandbox wait exit-code semantics are undocumented.
		// Treat ExitCode=nil as normal ("nil means unknown"), not an error.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := exitErr.ExitCode()
			exitCode = &code
		}
		// Non-ExitError (e.g. binary not found) is logged but not fatal.
	} else {
		// Try to parse exit code from stdout if provided.
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			if code, parseErr := strconv.Atoi(trimmed); parseErr == nil {
				exitCode = &code
			}
		}
	}

	// Log at info level — sandbox exit is normal (C5).
	runtimeLog.Info("sandbox exited", "name", name, "exitCode", exitCode)
	r.state.markStopped(name, exitCode, &now)
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// labelValue safely retrieves a label value, returning "" if the map is
// nil or the key is absent.
func labelValue(labels map[string]string, key string) string {
	if labels == nil {
		return ""
	}
	return labels[key]
}
