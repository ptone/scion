// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"slices"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

// The broker reports preserveWorkspaceCreate from its registration on, and
// the workstation broker's stored capabilities carry it, so a hub routes a
// start through create-with-gather only to a broker that keeps the
// workspace (ptone/scion#6096).
func TestBrokerRegistrationCapabilities_IncludesPreserveWorkspaceCreate(t *testing.T) {
	if !slices.Contains(brokerRegistrationCapabilities(), "preserveWorkspaceCreate") {
		t.Errorf("brokerRegistrationCapabilities() = %v, want it to include preserveWorkspaceCreate", brokerRegistrationCapabilities())
	}
}

func TestStoreBrokerCapabilities_CarriesPreserveWorkspaceCreate(t *testing.T) {
	for _, want := range []bool{true, false} {
		got := storeBrokerCapabilities(&hubclient.BrokerCapabilities{PreserveWorkspaceCreate: want})
		if got == nil || got.PreserveWorkspaceCreate != want {
			t.Errorf("storeBrokerCapabilities(PreserveWorkspaceCreate=%v) = %+v", want, got)
		}
	}
}
