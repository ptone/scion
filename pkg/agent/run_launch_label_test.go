package agent

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// TestStartLaunchIDLabel covers the launch id a started container carries:
// Start labels a new container with the request's launch id and returns it,
// and a reused running container returns its own label instead.
func TestStartLaunchIDLabel(t *testing.T) {
	const launchID = "6f1c3f8e-0000-4000-8000-000000000001"
	const oldID = "6f1c3f8e-0000-4000-8000-000000000002"
	running := func(labels map[string]string) []api.AgentInfo {
		return []api.AgentInfo{{ContainerID: "old", Name: "resume-test", Phase: string(state.PhaseRunning), Labels: labels}}
	}
	tests := []struct {
		name      string
		launchID  string
		existing  []api.AgentInfo // listed before Start runs anything
		listAfter bool            // the started container is listed again
		wantRun   bool
		wantLabel string // label on the new container ("" means absent)
		wantID    string
	}{
		{name: "new container", launchID: launchID, listAfter: true, wantRun: true, wantLabel: launchID, wantID: launchID},
		{name: "new container, not listed again", launchID: launchID, wantRun: true, wantLabel: launchID, wantID: launchID},
		{name: "new container without launch id", listAfter: true, wantRun: true},
		{name: "reused labelled container", launchID: launchID, existing: running(map[string]string{api.LabelLaunchID: oldID}), wantID: oldID},
		{name: "reused container with a rewritten label key", launchID: launchID, existing: running(map[string]string{"scion_launch_id": oldID}), wantID: oldID},
		{name: "reused unlabelled container", launchID: launchID, existing: running(nil), wantID: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran *runtime.RunConfig
			mgr, projectScionDir := newResumePhaseTestFixture(t, nil)
			rt := mgr.(*AgentManager).Runtime.(*runtime.MockRuntime)
			rt.ListFunc = func(context.Context, map[string]string) ([]api.AgentInfo, error) {
				if ran == nil {
					return tt.existing, nil
				}
				if !tt.listAfter {
					return nil, nil
				}
				return []api.AgentInfo{{ContainerID: "mock-id", Name: "resume-test", Phase: string(state.PhaseRunning), Labels: ran.Labels}}, nil
			}
			rt.RunFunc = func(_ context.Context, cfg runtime.RunConfig) (string, error) {
				ran = &cfg
				return "mock-id", nil
			}

			env := map[string]string{}
			if tt.launchID != "" {
				env["SCION_LAUNCH_ID"] = tt.launchID
			}
			got, err := mgr.Start(context.Background(), api.StartOptions{
				Name:        "resume-test",
				ProjectPath: projectScionDir,
				BrokerMode:  true,
				NoAuth:      true,
				Env:         env,
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if (ran != nil) != tt.wantRun {
				t.Fatalf("container run = %v, want %v", ran != nil, tt.wantRun)
			}
			if ran != nil {
				label, ok := ran.Labels[api.LabelLaunchID]
				if tt.wantLabel == "" && ok {
					t.Errorf("label %s = %q, want none", api.LabelLaunchID, label)
				}
				if tt.wantLabel != "" && label != tt.wantLabel {
					t.Errorf("label %s = %q, want %q", api.LabelLaunchID, label, tt.wantLabel)
				}
			}
			if got.LaunchID != tt.wantID {
				t.Errorf("LaunchID = %q, want %q", got.LaunchID, tt.wantID)
			}
		})
	}
}
