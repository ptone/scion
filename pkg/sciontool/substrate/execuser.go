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

package substrate

// execAsUserCmd is a deliberate, verbatim copy of
// pkg/runtime.ExecAsUserCmd's wrapper script (see that file's doc comment
// for the full PAM/su rationale — the reasoning is identical here).
//
// It is duplicated rather than imported: pkg/runtime transitively imports
// pkg/config, and sciontool must never import pkg/config (project path
// resolution has no business inside an agent container — see
// TestInitProjectDataIsolation in cmd/sciontool/commands/init_test.go,
// which fails the build if that boundary is crossed). pkg/runtime and
// pkg/sciontool/substrate are both owned by other/parallel work for this
// change (see the substrate-integration brief), so extracting a shared leaf
// package for this one function is left as a follow-up rather than done
// here.
//
// Keep this in sync with pkg/runtime.ExecAsUserCmd if that wrapper's
// behavior ever changes.
func execAsUserCmd(user, cmd string) []string {
	const script = `if [ "$(/usr/bin/whoami)" = "$1" ]; then exec sh -c "$2"; else exec su - "$1" -c "$2"; fi`
	return []string{"sh", "-c", script, "exec-as-user", user, cmd}
}
