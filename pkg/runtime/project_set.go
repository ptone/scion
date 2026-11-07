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

import "context"

// ProjectSet supplies the projects a runtime serves, for listings that have
// no project context of their own. SubstrateRuntime uses it as the source of
// an unfiltered List in operator mode (substrate v1 design §3.3): it lists the
// atespace of each served project instead of every atespace in the cluster.
//
// The runtime broker implements it from its hub project registrations;
// this package only consumes it, so it does not depend on the hub client.
type ProjectSet interface {
	// ServedProjects returns the IDs of the projects this broker serves.
	// It returns an error, never an empty result, when the set has not
	// been loaded yet or cannot be read.
	ServedProjects(ctx context.Context) ([]string, error)
}

// ProjectSetReceiver is the optional capability of a runtime that takes a
// ProjectSet. The runtime broker sets its ProjectSet on every runtime it
// registers that implements it.
type ProjectSetReceiver interface {
	SetProjectSet(ps ProjectSet)
}
