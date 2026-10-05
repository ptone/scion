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

package hub

import "github.com/GoogleCloudPlatform/scion/pkg/artifacts"

// SetArtifactStore installs the artifact service's store. The store owns the
// artifact_* tables, created by its Init outside the Ent migration graph
// (design D3). Until a store is set the artifact routes cannot serve data.
func (s *Server) SetArtifactStore(st artifacts.Store) {
	s.mu.Lock()
	s.artifactStore = st
	s.mu.Unlock()
}

// ArtifactStore returns the artifact store, or nil when none is set.
func (s *Server) ArtifactStore() artifacts.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.artifactStore
}
