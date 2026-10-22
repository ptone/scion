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

package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// maxPrivateGitConfigSize caps how much of a base .git/config is copied into the
// per-agent private copy. A real repository config is a few KiB; the cap bounds
// a pathological base (and the agent, which can grow its own copy once mounted —
// the broker never reads it back, P3) rather than defining a security boundary.
const maxPrivateGitConfigSize = 1 << 20 // 1 MiB

// seedPrivateGitConfig writes a per-agent private copy of the repository's
// .git/config into the agent's (broker-owned, agent-scoped) state dir and
// returns its path, for the runtime to bind read-write as a single file over the
// container's .git/config (E-X2 PR 2, P1/P2/P8).
//
// It reads the base config (absent → empty), strips credential-bearing keys
// (util.RedactGitConfigSecrets, P8), and writes the result atomically with an
// O_EXCL temp file + rename (P2) at <agentDir>/git/config, mode 0600, owned by
// this broker process. The broker never reads this file back as configuration
// (P3); the runtime re-checks it (regular file, not a symlink, link count 1,
// broker-owned) immediately before binding.
//
// The returned path is always a fresh, broker-written regular file: any stale
// file (including one an earlier agent run left, or a symlink) is replaced by the
// rename.
func seedPrivateGitConfig(baseConfigPath, agentDir string) (string, error) {
	if agentDir == "" {
		return "", fmt.Errorf("seed private git config: empty agent dir")
	}
	gitDir := filepath.Join(agentDir, "git")
	if err := os.MkdirAll(gitDir, 0700); err != nil {
		return "", fmt.Errorf("seed private git config: %w", err)
	}

	raw, err := readBaseGitConfig(baseConfigPath)
	if err != nil {
		return "", fmt.Errorf("seed private git config: %w", err)
	}
	redacted, stripped, err := util.RedactGitConfigSecrets(raw)
	if err != nil {
		return "", fmt.Errorf("seed private git config: redact base config: %w", err)
	}
	if len(stripped) > 0 {
		util.Debugf("seedPrivateGitConfig: stripped credential-bearing keys from the private copy: %v", stripped)
	}

	tmp, err := os.CreateTemp(gitDir, "config.*.tmp") // O_EXCL
	if err != nil {
		return "", fmt.Errorf("seed private git config: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("seed private git config: chmod: %w", err)
	}
	if _, err := tmp.Write(redacted); err != nil {
		tmp.Close()
		return "", fmt.Errorf("seed private git config: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("seed private git config: close: %w", err)
	}
	dest := filepath.Join(gitDir, "config")
	if err := os.Rename(tmpName, dest); err != nil {
		return "", fmt.Errorf("seed private git config: rename: %w", err)
	}
	return dest, nil
}

// readBaseGitConfig reads up to maxPrivateGitConfigSize bytes of the base
// .git/config. A missing file is not an error (returns empty); a file larger
// than the cap is.
func readBaseGitConfig(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxPrivateGitConfigSize+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPrivateGitConfigSize {
		return nil, fmt.Errorf("base git config %s exceeds %d bytes", path, maxPrivateGitConfigSize)
	}
	return raw, nil
}
