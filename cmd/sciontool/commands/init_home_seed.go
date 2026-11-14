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

package commands

import (
	"fmt"
	"os"

	"github.com/GoogleCloudPlatform/scion/pkg/agenthome"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

// Seams for tests: the one accepted seed path, the effective UID and the
// no-follow chown.
var (
	homeSeedMountPath = api.HomeSeedMountPath
	homeSeedGeteuid   = os.Geteuid
	homeSeedLchown    = os.Lchown
)

// seedEphemeralHome copies the read-only home seed of an ephemeral instance
// (ptone/scion#7780) into agentHome. The runtime sets api.HomeSeedEnvVar
// only for such an instance; without it this does nothing. It runs before
// the staged secrets are written, the workspace is cloned and the pre-start
// hooks run, so the seed never overwrites a secret target. The variable is
// unset afterwards, so a re-exec of init does not seed again.
//
// The workspace and home roots are instance-local memory volumes, which
// Cloud Run mounts root-owned with mode 1777. When init runs as root they
// are chowned to uid:gid first (without following a link), so $HOME is
// owned by the scion user as some tools expect; the 1777 mode stays. The copy is
// agenthome.SeedTree: no links followed or copied, every entry owned by
// uid:gid, within agenthome.ProvisionedHomeLimits.
func seedEphemeralHome(agentHome, workspace string, uid, gid int) error {
	seed, ok := os.LookupEnv(api.HomeSeedEnvVar)
	if !ok {
		return nil
	}
	_ = os.Unsetenv(api.HomeSeedEnvVar)
	if seed != homeSeedMountPath {
		return fmt.Errorf("refusing the home seed: %s must be %s", api.HomeSeedEnvVar, homeSeedMountPath)
	}
	if homeSeedGeteuid() == 0 && uid != 0 {
		for _, dir := range []string{workspace, agentHome} {
			if err := homeSeedLchown(dir, uid, gid); err != nil {
				return fmt.Errorf("home seed: chown %s: %w", dir, err)
			}
		}
	}
	src, err := os.OpenRoot(seed)
	if err != nil {
		return fmt.Errorf("home seed: open %s: %w", seed, err)
	}
	defer func() { _ = src.Close() }()
	home, err := agenthome.Open(agentHome)
	if err != nil {
		return fmt.Errorf("home seed: %w", err)
	}
	defer func() { _ = home.Close() }()
	res, err := home.SeedTree(src, agenthome.SeedOptions{UID: uid, GID: gid, Limits: agenthome.ProvisionedHomeLimits})
	if err != nil {
		return fmt.Errorf("home seed: copy into %s: %w", agentHome, err)
	}
	log.Info("Seeded the home from %s: %d dirs, %d files, %d bytes, %d skipped", seed, res.Dirs, res.Files, res.Bytes, len(res.Skipped))
	return nil
}

// workspacePathOrDefault is SCION_WORKSPACE_PATH, else /workspace, as
// gitCloneWorkspace resolves it.
func workspacePathOrDefault() string {
	if p := os.Getenv("SCION_WORKSPACE_PATH"); p != "" {
		return p
	}
	return "/workspace"
}
