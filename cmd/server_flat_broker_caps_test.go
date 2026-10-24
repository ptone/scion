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

package cmd

import (
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

// The co-located broker's startEnvGather capability reaches the stored row,
// so the workstation hub gathers as-needed values at start on its own broker
// instead of refusing those starts as for an old broker (ptone/scion#6101).
func TestStoreBrokerCapabilities_CopiesStartEnvGather(t *testing.T) {
	if got := storeBrokerCapabilities(&hubclient.BrokerCapabilities{StartEnvGather: true}); got == nil || !got.StartEnvGather {
		t.Fatalf("StartEnvGather not copied: %+v", got)
	}
	if got := storeBrokerCapabilities(&hubclient.BrokerCapabilities{}); got.StartEnvGather {
		t.Fatal("StartEnvGather set without the broker reporting it")
	}
}
