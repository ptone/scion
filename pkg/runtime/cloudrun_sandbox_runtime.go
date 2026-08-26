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
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// CloudRunSandboxRuntime implements the Runtime interface for gVisor
// sandboxes running inside a Cloud Run Instance. Each agent gets its
// own sandbox created and managed via the platform-injected `sandbox`
// CLI binary (/usr/local/gcp/bin/sandbox).
type CloudRunSandboxRuntime struct {
	// bin is the path to the sandbox CLI binary.
	bin string

	// state tracks known sandbox IDs for cleanup coordination.
	state sandboxState

	// watchMu protects watchCancels.
	watchMu sync.Mutex
	// watchCancels maps sandbox IDs to cancel functions for their
	// watcher goroutines.
	watchCancels map[string]context.CancelFunc

	// deleteTimeout overrides DefaultDeleteTimeout when non-zero.
	// Used by tests to avoid waiting the full default duration.
	deleteTimeout time.Duration
}

// sandboxState is a thread-safe set of known sandbox IDs.
type sandboxState struct {
	mu    sync.Mutex
	known map[string]struct{}
}

func (s *sandboxState) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.known == nil {
		s.known = make(map[string]struct{})
	}
	s.known[id] = struct{}{}
}

func (s *sandboxState) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.known, id)
}

// NewCloudRunSandboxRuntime returns a CloudRunSandboxRuntime that uses
// the given binary path for sandbox CLI commands.
func NewCloudRunSandboxRuntime(bin string) *CloudRunSandboxRuntime {
	return &CloudRunSandboxRuntime{
		bin:          bin,
		watchCancels: make(map[string]context.CancelFunc),
	}
}

func (r *CloudRunSandboxRuntime) Name() string { return "cloudrun-sandbox" }

func (r *CloudRunSandboxRuntime) ExecUser() string { return "scion" }

// Stop terminates a sandbox. It delegates to deleteOrWorkaround which
// chooses between the timeout workaround and the plain delete path.
func (r *CloudRunSandboxRuntime) Stop(ctx context.Context, id string) error {
	return r.deleteOrWorkaround(ctx, id)
}

// Delete removes a sandbox. It delegates to deleteOrWorkaround which
// chooses between the timeout workaround and the plain delete path.
func (r *CloudRunSandboxRuntime) Delete(ctx context.Context, id string) error {
	return r.deleteOrWorkaround(ctx, id)
}

// deleteOrWorkaround dispatches to the workaround or the plain path based
// on the kill switch. The watcher cancel is performed here so neither
// downstream path needs it — and removing the workaround file does not
// lose the watcher cancel logic.
func (r *CloudRunSandboxRuntime) deleteOrWorkaround(ctx context.Context, id string) error {
	// Cancel the watcher goroutine for this sandbox.
	r.watchMu.Lock()
	if cancel, ok := r.watchCancels[id]; ok {
		cancel()
		delete(r.watchCancels, id)
	}
	r.watchMu.Unlock()

	if deleteWorkaroundEnabled {
		return r.deleteWithTimeout(ctx, id)
	}
	return r.deletePlain(ctx, id)
}

// deletePlain is the non-workaround path: plain `sandbox delete --force`.
// Used when SCION_CLOUDRUN_DELETE_WORKAROUND=off.
func (r *CloudRunSandboxRuntime) deletePlain(ctx context.Context, id string) error {
	cmd := exec.CommandContext(ctx, r.bin, "delete", "--force", id)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cloudrun-sandbox: delete --force failed: %w", err)
	}
	r.state.remove(id)
	return nil
}

// --- Lifecycle stubs (not yet implemented for sandbox runtime) ---

func (r *CloudRunSandboxRuntime) Run(ctx context.Context, cfg RunConfig) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: Run not yet implemented")
}

func (r *CloudRunSandboxRuntime) List(ctx context.Context, labelFilter map[string]string) ([]api.AgentInfo, error) {
	return nil, nil
}

func (r *CloudRunSandboxRuntime) GetLogs(ctx context.Context, id string) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: GetLogs not yet implemented")
}

func (r *CloudRunSandboxRuntime) Attach(ctx context.Context, id string) error {
	return fmt.Errorf("cloudrun-sandbox: Attach not yet implemented")
}

func (r *CloudRunSandboxRuntime) ImageExists(ctx context.Context, image string) (bool, error) {
	return false, fmt.Errorf("cloudrun-sandbox: ImageExists not yet implemented")
}

func (r *CloudRunSandboxRuntime) ImageID(ctx context.Context, image string) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: ImageID not yet implemented")
}

func (r *CloudRunSandboxRuntime) RemoveImage(ctx context.Context, image string) error {
	return fmt.Errorf("cloudrun-sandbox: RemoveImage not yet implemented")
}

func (r *CloudRunSandboxRuntime) PullImage(ctx context.Context, image string) error {
	return fmt.Errorf("cloudrun-sandbox: PullImage not yet implemented")
}

func (r *CloudRunSandboxRuntime) Sync(ctx context.Context, id string, direction SyncDirection) error {
	return fmt.Errorf("cloudrun-sandbox: Sync not yet implemented")
}

func (r *CloudRunSandboxRuntime) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: Exec not yet implemented")
}

func (r *CloudRunSandboxRuntime) GetWorkspacePath(ctx context.Context, id string) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: GetWorkspacePath not yet implemented")
}
