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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/brokercredentials"
	"github.com/GoogleCloudPlatform/scion/pkg/brokerhost"
	"github.com/GoogleCloudPlatform/scion/pkg/brokeridentity"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// TestFlatInstanceServerConfig covers the production per-instance server
// configuration: the NFS verify-only rule by CONFIGURED count, and the
// per-mode Hub wiring (co-located: HubInProcess + in-memory credentials;
// remote: validated instance credentials enable the Hub integration,
// control channel and heartbeat).
func TestFlatInstanceServerConfig(t *testing.T) {
	id := &brokeridentity.Identity{InstanceKey: "docker-a", RuntimeBrokerID: "rb-a",
		RuntimeTarget: api.RuntimeTargetDescriptor{ID: "t-a", Type: "docker"}}
	inst := config.V1RuntimeBrokerInstanceConfig{Key: "docker-a", Name: "a", RuntimeTarget: &config.V1RuntimeTargetConfig{Type: "docker"}}
	inMemory := &brokercredentials.BrokerCredentials{Name: "local", BrokerID: "rb-a", SecretKey: "c2VjcmV0"}
	remote := []brokercredentials.BrokerCredentials{{Name: "hub", BrokerID: "rb-a", SecretKey: "c2VjcmV0", HubEndpoint: "https://hub.example"}}

	for _, tc := range []struct {
		mode        brokerhost.Mode
		multi       bool
		hubEndpoint string
		act         brokerhost.Activation
	}{
		{brokerhost.ModeColocated, false, "http://localhost:8080", brokerhost.Activation{InMemoryCredentials: inMemory}},
		{brokerhost.ModeColocated, true, "http://localhost:8080", brokerhost.Activation{InMemoryCredentials: inMemory}},
		{brokerhost.ModeRemote, false, "", brokerhost.Activation{RemoteCredentials: remote}},
		{brokerhost.ModeRemote, true, "", brokerhost.Activation{RemoteCredentials: remote}},
	} {
		t.Run(fmt.Sprintf("%s multi=%v", tc.mode, tc.multi), func(t *testing.T) {
			act := tc.act
			sh := flatServerShared{cfg: &config.GlobalConfig{RuntimeBroker: config.RuntimeBrokerConfig{Host: "127.0.0.1", Port: 9800}},
				mode: tc.mode, multiInstance: tc.multi, hubEndpoint: tc.hubEndpoint}
			c := flatInstanceServerConfig(sh, brokerhost.InstanceContext{Instance: inst, Identity: id, Activation: &act})

			assert.Equal(t, "rb-a", c.BrokerID)
			assert.Equal(t, "a", c.BrokerName)
			if tc.multi {
				assert.NotEmpty(t, c.NFSVerifyOnlyReason, "several configured: no instance mounts")
			} else {
				assert.Empty(t, c.NFSVerifyOnlyReason, "one configured: unchanged")
			}
			if assert.NotNil(t, c.FlatInstance) {
				assert.Same(t, id, c.FlatInstance.Identity)
				assert.Equal(t, tc.mode == brokerhost.ModeColocated, c.FlatInstance.HubInProcess)
				assert.Equal(t, act.RemoteCredentials, c.FlatInstance.RemoteCredentials)
			}
			assert.Equal(t, act.InMemoryCredentials, c.InMemoryCredentials)
			assert.True(t, c.HubEnabled, "Hub integration on (co-located endpoint or remote credentials)")
			assert.True(t, c.ControlChannelEnabled)
			assert.True(t, c.HeartbeatEnabled)
			assert.True(t, c.BrokerAuthEnabled)
			assert.True(t, c.BrokerAuthStrictMode)
			assert.Nil(t, c.DefaultProfile, "a flat instance reports no Runtime Broker Profile")
		})
	}
}
