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
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/tmuxdeliver"
)

// The readiness probe and its parser moved to pkg/agent/tmuxdeliver. These
// test-only names keep this package's readiness tests unchanged: each one
// delegates to tmuxdeliver, so the tests exercise the code that runs.

const (
	defaultReadySettle        = tmuxdeliver.DefaultReadySettle
	defaultReadySettleSession = tmuxdeliver.DefaultReadySettleSession
	readyModeNoInitRun        = tmuxdeliver.ModeNoInitRun
)

func readinessProbeArgv() []string { return tmuxdeliver.ProbeArgv() }

func parseReadinessProbe(out string, settleFallback, settle time.Duration) tmuxdeliver.Probe {
	return tmuxdeliver.ParseProbe(out, settleFallback, settle)
}

// regularFileGuard is the expected guard text in the probe script: the path
// is a regular file and not a symlink. It is an expected value, so it is
// spelled out here instead of read from tmuxdeliver.
func regularFileGuard(path string) string {
	return `[ -f ` + path + ` ] && [ ! -L ` + path + ` ]`
}

func (s *readyMemoStore) size() int { return s.Len() }
