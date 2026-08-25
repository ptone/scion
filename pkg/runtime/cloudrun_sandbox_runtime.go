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
	"os"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

const defaultSandboxBin = "/usr/local/gcp/bin/sandbox"

// SandboxLauncherAvailable reports whether the Cloud Run Sandbox launcher
// binary is present on the filesystem.
func SandboxLauncherAvailable() bool {
	_, err := os.Stat(defaultSandboxBin)
	return err == nil
}

// CloudRunSandboxRuntime implements the Runtime interface for Cloud Run
// Sandboxes. Sandboxes are nested isolated workloads launched from inside
// a Cloud Run Instance via the `sandbox` CLI binary.
//
// This is a stub: all lifecycle methods return "not yet implemented" errors.
// The real implementation is planned for P3. Image-related methods return
// no-op success because the omni-image means there is one image and it is
// already present (design doc §4.2).
type CloudRunSandboxRuntime struct{}

// NewCloudRunSandboxRuntime returns a new CloudRunSandboxRuntime.
func NewCloudRunSandboxRuntime() *CloudRunSandboxRuntime {
	return &CloudRunSandboxRuntime{}
}

func (r *CloudRunSandboxRuntime) Name() string { return "cloudrun-sandbox" }

func (r *CloudRunSandboxRuntime) ExecUser() string { return "scion" }

func (r *CloudRunSandboxRuntime) Run(ctx context.Context, config RunConfig) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: Run not yet implemented")
}

func (r *CloudRunSandboxRuntime) Stop(ctx context.Context, id string) error {
	return fmt.Errorf("cloudrun-sandbox: Stop not yet implemented")
}

func (r *CloudRunSandboxRuntime) Delete(ctx context.Context, id string) error {
	return fmt.Errorf("cloudrun-sandbox: Delete not yet implemented")
}

func (r *CloudRunSandboxRuntime) List(ctx context.Context, labelFilter map[string]string) ([]api.AgentInfo, error) {
	return nil, fmt.Errorf("cloudrun-sandbox: List not yet implemented")
}

func (r *CloudRunSandboxRuntime) GetLogs(ctx context.Context, id string) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: GetLogs not yet implemented")
}

func (r *CloudRunSandboxRuntime) Attach(ctx context.Context, id string) error {
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

func (r *CloudRunSandboxRuntime) Sync(ctx context.Context, id string, direction SyncDirection) error {
	return fmt.Errorf("cloudrun-sandbox: Sync not yet implemented")
}

func (r *CloudRunSandboxRuntime) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: Exec not yet implemented")
}

func (r *CloudRunSandboxRuntime) GetWorkspacePath(ctx context.Context, id string) (string, error) {
	return "", fmt.Errorf("cloudrun-sandbox: GetWorkspacePath not yet implemented")
}
