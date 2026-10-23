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

import (
	"context"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// WorkspaceMigrationNotice is shown as a banner on the project Files tab
// while the hub's legacy workspace trees have not yet been migrated into
// the project storage layout (design-v4 M2, ptone/scion#5819).
type WorkspaceMigrationNotice struct {
	Message      string `json:"message"`
	OperationKey string `json:"operationKey"`
}

// workspaceMigrationNotice returns the banner for project's Files tab, or
// nil when there is none. Stub: the M2 maintenance operation
// (ptone/scion#5819) owns this file and supplies the real status.
func (s *Server) workspaceMigrationNotice(_ context.Context, _ *store.Project) *WorkspaceMigrationNotice {
	return nil
}
