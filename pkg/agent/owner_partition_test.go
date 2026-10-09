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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// P2.3 S5 (ptone/scion#3274): a start in flight on one flat instance never
// removes another instance's (or an unlabeled) object on the shared
// daemon, even one with the same name in the same project; a legacy
// manager's pre-start cleanup is unchanged.
func TestOwnerPartition_StartPreCleanNeverRemovesForeignObject(t *testing.T) {
	mockRuntimeForTest(t)
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	t.Setenv("HOME", tmpDir)
	require.NoError(t, config.InitMachine(getTestHarnesses()))
	root := filepath.Join(tmpDir, "project")
	require.NoError(t, config.InitProject(filepath.Join(root, ".scion"), getTestHarnesses()))
	require.NoError(t, os.Chdir(root))

	foreign := map[string]map[string]string{
		"another instance": {"scion.name": "worker", "scion.project_id": "proj-1", api.LabelRuntimeBrokerID: "broker-b"},
		"unlabeled":        {"scion.name": "worker", "scion.project_id": "proj-1"},
	}
	for name, labels := range foreign {
		for _, owned := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s owned=%v", name, owned), func(t *testing.T) {
				var deleted []string
				rt := &runtime.MockRuntime{
					ListFunc: func(context.Context, map[string]string) ([]api.AgentInfo, error) {
						return []api.AgentInfo{{ID: "cid-foreign", ContainerID: "cid-foreign", Name: "worker", Phase: "stopped",
							ContainerStatus: "Exited (0)", Labels: labels}}, nil
					},
					RunFunc: func(context.Context, runtime.RunConfig) (string, error) { return "cid-new", nil },
					DeleteFunc: func(_ context.Context, ref runtime.RunRef) error {
						deleted = append(deleted, ref.ID)
						return nil
					},
				}
				m := NewManager(rt).(*AgentManager)
				if owned {
					m.SetOwner(OwnerScope{RuntimeBrokerID: "broker-a"})
				}
				_, _ = m.Start(context.Background(), api.StartOptions{Name: "worker", ProjectPath: root, HarnessConfig: "claude", NoAuth: true,
					Env: map[string]string{"SCION_PROJECT_ID": "proj-1", "SCION_AGENT_ID": "agent-a1"}})
				if owned {
					assert.NotContains(t, deleted, "cid-foreign", "an owned start removed a foreign object")
				} else {
					assert.Contains(t, deleted, "cid-foreign", "legacy pre-start cleanup is unchanged")
				}
			})
		}
	}
}
