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

package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// WithProjectStateLock runs fn while holding the provisioning file lock of
// the project state directory stateDir (ProjectStateDir of the project's
// workspace): the lock ProvisionShared takes in its sentinel directory
// before it populates a shared workspace (the provisioning init container
// takes it after its legacy workspace lock). It lets one-shot work that
// places a project's workspace content (the project storage migration,
// ptone/scion#5819) exclude a concurrent first provisioning for its whole
// duration. fn runs under the lock's context, which is cancelled if the lock
// is lost.
//
// stateDir must already exist: the caller creates it with the broker's leaf
// contract (shareddirs.EnsureLeaf), because a directory created here would
// lack the setgid bit and default ACL the provisioning init container needs.
func WithProjectStateLock(ctx context.Context, stateDir string, fn func(ctx context.Context) error) error {
	if stateDir == "" {
		return errors.New("provision: project state directory is required")
	}
	fi, err := os.Lstat(stateDir)
	if err != nil {
		return fmt.Errorf("provision: project state directory: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("provision: project state directory %s is not a directory", stateDir)
	}
	held, err := acquireFileLock(ctx, stateDir)
	if err != nil {
		return fmt.Errorf("provision: acquire project state lock: %w", err)
	}
	runErr := fn(held.ctx)
	if relErr := held.release(); relErr != nil && runErr == nil {
		return fmt.Errorf("provision: release project state lock: %w", relErr)
	}
	return runErr
}
