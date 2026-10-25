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

//go:build !no_sqlite

package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusedHubTemplateRefs are template references a remote request must not
// carry: absolute host directories, relative paths that leave the template
// directories, and remote sources the shared validator does not accept.
var refusedHubTemplateRefs = map[string]string{
	"absolute host directory": "/home/user/.scion",
	"absolute root":           "/",
	"escaping relative path":  "../../../home/user/.scion",
	"template directory":      ".",
	"rclone local backend":    ":local:/home/user/.scion",
	"archive on loopback":     "https://127.0.0.1/x.tgz",
}

// linkedProjectWithLocalBroker creates a project whose default broker is a
// provider with a local path: the case in which an unresolved template name
// used to be passed through to the broker unchanged.
func linkedProjectWithLocalBroker(t *testing.T, s store.Store, suffix string) *store.Project {
	t.Helper()
	ctx := context.Background()
	broker := &store.RuntimeBroker{
		ID:     tid("broker_tplref_" + suffix),
		Slug:   "tplref-broker-" + suffix,
		Name:   "Template Ref Broker " + suffix,
		Status: store.BrokerStatusOnline,
	}
	require.NoError(t, s.CreateRuntimeBroker(ctx, broker))
	project := &store.Project{
		ID:                     tid("project_tplref_" + suffix),
		Slug:                   "tplref-project-" + suffix,
		Name:                   "Template Ref Project " + suffix,
		GitRemote:              "github.com/test/tplref-" + suffix,
		DefaultRuntimeBrokerID: broker.ID,
		Created:                time.Now(),
		Updated:                time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))
	require.NoError(t, s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID:  project.ID,
		BrokerID:   broker.ID,
		BrokerName: broker.Name,
		LocalPath:  "/home/user/project/.scion",
		Status:     store.BrokerStatusOnline,
	}))
	return project
}

func TestAgentCreate_RefusesTemplateRefsOutsideTheTemplateRoots(t *testing.T) {
	srv, s := testServer(t)
	project := linkedProjectWithLocalBroker(t, s, "create")

	for name, ref := range refusedHubTemplateRefs {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", map[string]interface{}{
				"name":      "refused-agent",
				"projectId": project.ID,
				"template":  ref,
			})
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "template reference not allowed")
		})
	}
	agent, err := s.GetAgentBySlug(context.Background(), project.ID, "refused-agent")
	assert.True(t, agent == nil || err != nil, "no agent is created for a refused template reference")

	// A template name, and an accepted remote source, are still forwarded
	// to the broker unchanged.
	for slug, ref := range map[string]string{"named-agent": "my-local-template", "remote-agent": ":gcs:my-bucket/templates/x"} {
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", map[string]interface{}{
			"name":      slug,
			"projectId": project.ID,
			"template":  ref,
		})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var resp CreateAgentResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.NotNil(t, resp.Agent)
		assert.Equal(t, ref, resp.Agent.Template)
	}
}

func TestScheduledEvent_RefusesTemplateRefsOutsideTheTemplateRoots(t *testing.T) {
	srv, _, projectID := setupScheduledEventTest(t)
	path := "/api/v1/projects/" + projectID + "/scheduled-events"

	for name, ref := range refusedHubTemplateRefs {
		t.Run(name, func(t *testing.T) {
			// Convenience fields.
			rec := doRequest(t, srv, http.MethodPost, path, CreateScheduledEventRequest{
				EventType: "dispatch_agent",
				FireIn:    "30m",
				AgentName: "sched-agent",
				Template:  ref,
			})
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "template reference not allowed")

			// Raw payload.
			payload, err := json.Marshal(DispatchAgentEventPayload{AgentName: "sched-agent", Template: ref})
			require.NoError(t, err)
			rec = doRequest(t, srv, http.MethodPost, path, CreateScheduledEventRequest{
				EventType: "dispatch_agent",
				FireIn:    "30m",
				Payload:   string(payload),
			})
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "template reference not allowed")
		})
	}
}

func TestSchedule_RefusesTemplateRefsOutsideTheTemplateRoots(t *testing.T) {
	srv, _, projectID := setupScheduleTest(t)
	path := "/api/v1/projects/" + projectID + "/schedules"

	for name, ref := range refusedHubTemplateRefs {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(t, srv, http.MethodPost, path, CreateScheduleRequest{
				Name:      "convenience",
				CronExpr:  "0 9 * * *",
				EventType: "dispatch_agent",
				AgentName: "sched-agent",
				Template:  ref,
			})
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "template reference not allowed")

			payload, err := json.Marshal(DispatchAgentEventPayload{AgentName: "sched-agent", Template: ref})
			require.NoError(t, err)
			rec = doRequest(t, srv, http.MethodPost, path, CreateScheduleRequest{
				Name:      "raw",
				CronExpr:  "0 9 * * *",
				EventType: "dispatch_agent",
				Payload:   string(payload),
			})
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "template reference not allowed")
		})
	}

	// An update that changes the payload to carry a refused reference is
	// refused too, and the stored schedule keeps its payload.
	rec := doRequest(t, srv, http.MethodPost, path, CreateScheduleRequest{
		Name:      "valid",
		CronExpr:  "0 9 * * *",
		EventType: "dispatch_agent",
		AgentName: "sched-agent",
		Template:  "web-dev",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created store.Schedule
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&created))
	rec = doRequest(t, srv, http.MethodPatch, path+"/"+created.ID, UpdateScheduleRequest{
		Payload: `{"agentName":"sched-agent","template":"/home/user/.scion"}`,
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "template reference not allowed")
}

// A dispatch_agent row stored before the create-time check (or written
// another way) is refused when it fires: no agent is created.
func TestDispatchAgentEventHandler_RefusesStoredTemplateRefsOutsideTheTemplateRoots(t *testing.T) {
	for name, ref := range refusedHubTemplateRefs {
		t.Run(name, func(t *testing.T) {
			ms := newMockStore()
			ms.projects["project-1"] = &store.Project{ID: "project-1", Name: "test-project"}
			creatorID := seedFullRoleDispatchCreator(ms, "project-1")
			srv := newEventHandlerTestServer(ms)

			payload, err := json.Marshal(DispatchAgentEventPayload{AgentName: "stored-agent", Template: ref})
			require.NoError(t, err)
			err = srv.dispatchAgentEventHandler()(context.Background(), withMockAgentRevision(store.ScheduledEvent{
				ID:        "dispatch-tplref-1",
				ProjectID: "project-1",
				EventType: "dispatch_agent",
				Payload:   string(payload),
				CreatedBy: creatorID,
			}, creatorID))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "template reference not allowed")
			assert.Nil(t, findMockAgent(ms, "stored-agent"), "no agent is created")
		})
	}
}

// A hub operational default_template that is not an accepted template
// reference degrades like an unusable hub default: the create succeeds with
// no template and a warning names the setting. (A request-supplied reference
// is refused, TestAgentCreate_RefusesTemplateRefsOutsideTheTemplateRoots.)
func TestCreateAgent_HubDefaultTemplateRefOutsideTheRoots_Degrades(t *testing.T) {
	disp := &createAgentDispatcher{createPhase: string(state.PhaseRunning)}
	srv, s, project := setupLadderServer(t, disp)
	logs := captureHarnessLogs(srv)
	setHubAgentDefaults(srv, opsettings.AgentDefaultsSettings{DefaultTemplate: "/home/user/.scion"})

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "hub-default-host-path",
		ProjectID: project.ID,
		Task:      "do something",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Agent)
	agent, err := s.GetAgent(context.Background(), resp.Agent.ID)
	require.NoError(t, err)
	assert.Empty(t, agent.Template, "the refused hub default is cleared, never dispatched")
	require.Len(t, logs.hubDefaultTemplateWarnings(), 1)
}

// The scheduler-dispatch mirror of the hub-default degradation.
func TestDispatchAgentEventHandler_HubDefaultTemplateRefOutsideTheRoots_Degrades(t *testing.T) {
	ms := newMockStore()
	ms.projects["project-1"] = &store.Project{ID: "project-1", Name: "test-project"}
	creatorID := seedFullRoleDispatchCreator(ms, "project-1")
	srv := newEventHandlerTestServer(ms)
	logs := captureHarnessLogs(srv)
	setHubAgentDefaults(srv, opsettings.AgentDefaultsSettings{DefaultTemplate: "/home/user/.scion"})

	err := srv.dispatchAgentEventHandler()(context.Background(), withMockAgentRevision(store.ScheduledEvent{
		ID:        "dispatch-hub-host-path-1",
		ProjectID: "project-1",
		EventType: "dispatch_agent",
		Payload:   `{"agentName":"sched-hub-host-path","task":"Do the thing"}`,
		CreatedBy: creatorID,
	}, creatorID))
	require.NoError(t, err)
	created := findMockAgent(ms, "sched-hub-host-path")
	require.NotNil(t, created, "agent was not created")
	assert.Empty(t, created.Template, "the refused hub default is cleared, never dispatched")
	require.Len(t, logs.hubDefaultTemplateWarnings(), 1)
}
