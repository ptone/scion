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

package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// GitHubExtraRepo represents a sibling repository configured on a project for
// read-only GitHub App token minting (ptone/scion#9334).
type GitHubExtraRepo struct {
	Repo    string    `json:"repo"`     // "owner/name", stored as entered, compared case-insensitively
	AddedBy string    `json:"added_by"` // user id
	AddedAt time.Time `json:"added_at"`
	// RemovedFromInstallationAt is set by the installation_repositories.removed
	// webhook hook and cleared when the repo is re-added to the installation or
	// the entry is re-saved.
	RemovedFromInstallationAt *time.Time `json:"removed_from_installation_at,omitempty"`
}

// HashGitHubExtraRepos computes the deterministic compare-and-set version hash
// of a project's extra-repos list. A nil slice and an empty slice produce the
// same hash.
func HashGitHubExtraRepos(repos []GitHubExtraRepo) string {
	if len(repos) == 0 {
		sum := sha256.Sum256([]byte("[]"))
		return hex.EncodeToString(sum[:])
	}
	b, err := json.Marshal(repos)
	if err != nil {
		sum := sha256.Sum256([]byte("[]"))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
