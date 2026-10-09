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

package runtimebroker

import (
	"net/http"
	"testing"
)

// Ownership-negative coverage for a flat instance (ptone/scion#3274, P2.3
// amendment): each refusal happens before any runtime call or ownership
// record is created, and the frozen target checks still come first.

func noOwnedRecord(t *testing.T, f *flatInstanceFixture, agentID string) {
	t.Helper()
	if _, ok, err := f.srv.ownership.Get(flatTestProjectID, agentID); err != nil || ok {
		t.Fatalf("an ownership record exists for %s (err %v)", agentID, err)
	}
}

func TestFlatOwnership_StartWithoutBindingIDsRefused(t *testing.T) {
	f := newFlatInstanceTestServer(t, flatInstanceOpts{hubInProcess: true})
	f.seedOwnedAgent(t)
	target := `"expectedRuntimeTargetId":"` + f.identity.RuntimeTarget.ID + `"`
	for name, req := range map[string][2]string{
		"no project":  {"/api/v1/agents/test-agent-1/start", `{` + target + `,` + flatAgentEnv + `}`},
		"no agent ID": {"/api/v1/agents/test-agent-1/start" + flatStartQuery, `{` + target + `}`},
	} {
		t.Run(name, func(t *testing.T) {
			w := serveFlat(f.srv, http.MethodPost, req[0], req[1])
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
			}
		})
	}
	if n := mgrStartCalls(f); n != 0 {
		t.Fatalf("runtime started without an ownership binding: %d", n)
	}
}

func TestFlatOwnership_CreateWithoutAgentIDRefused(t *testing.T) {
	f := newFlatInstanceTestServer(t, flatInstanceOpts{hubInProcess: true})
	w := serveFlat(f.srv, http.MethodPost, "/api/v1/agents", flatCreateBody("req-no-id", "flat-agent",
		map[string]interface{}{"id": "", "expectedRuntimeTargetId": f.identity.RuntimeTarget.ID}))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if n := mgrStartCalls(f); n != 0 {
		t.Fatalf("runtime started: %d", n)
	}
}

func TestFlatOwnership_ExistingAgentWithoutRecordRefused(t *testing.T) {
	f := newFlatInstanceTestServer(t, flatInstanceOpts{hubInProcess: true})
	for _, path := range []string{"/api/v1/agents/test-agent-1/start" + flatStartQuery, flatRestartPath + flatStartQuery} {
		w := serveFlat(f.srv, http.MethodPost, path, `{"expectedRuntimeTargetId":"`+f.identity.RuntimeTarget.ID+`",`+flatAgentEnv+`}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s: status = %d, want 409 (not adopted): %s", path, w.Code, w.Body.String())
		}
	}
	if stops, starts := mgrCalls(f); stops != 0 || starts != 0 {
		t.Fatalf("an unrecorded agent was stopped or started: stops=%d starts=%d", stops, starts)
	}
	noOwnedRecord(t, f, flatTestAgentID)
}

func TestFlatOwnership_ForeignSlugHolderRefused(t *testing.T) {
	f := newFlatInstanceTestServer(t, flatInstanceOpts{hubInProcess: true})
	// Another agent ID holds the slug "flat-agent" in this instance.
	if err := f.srv.ownership.BeginRun(flatTestProjectID, "another-agent-id", "flat-agent", "run-x"); err != nil {
		t.Fatal(err)
	}
	w := serveFlat(f.srv, http.MethodPost, "/api/v1/agents", flatCreateBody("req-foreign", "flat-agent",
		map[string]interface{}{"expectedRuntimeTargetId": f.identity.RuntimeTarget.ID}))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if n := mgrStartCalls(f); n != 0 {
		t.Fatalf("runtime started for a slug held by another agent: %d", n)
	}
	noOwnedRecord(t, f, "agent-id-flat-agent")
}

// TestFlatOwnership_TargetChecksPrecedeRecordCreation: a create refused by
// the frozen target checks creates no ownership record.
func TestFlatOwnership_TargetChecksPrecedeRecordCreation(t *testing.T) {
	f := newFlatInstanceTestServer(t, flatInstanceOpts{hubInProcess: true})
	for _, extra := range []map[string]interface{}{
		{},                                   // target required
		{"expectedRuntimeTargetId": "other"}, // mismatch
		{"expectedRuntimeTargetId": f.identity.RuntimeTarget.ID, "config": map[string]interface{}{"profile": "p"}}, // profile
	} {
		w := serveFlat(f.srv, http.MethodPost, "/api/v1/agents", flatCreateBody("req-order", "flat-agent", extra))
		if w.Code < 400 {
			t.Fatalf("status = %d, want a refusal", w.Code)
		}
	}
	noOwnedRecord(t, f, "agent-id-flat-agent")
}

// TestFlatOwnership_CreateEstablishesOwnership: a genuine create (IDs, no
// preseeded record) records ownership itself.
func TestFlatOwnership_CreateEstablishesOwnership(t *testing.T) {
	f := newFlatInstanceTestServer(t, flatInstanceOpts{hubInProcess: true})
	w := serveFlat(f.srv, http.MethodPost, "/api/v1/agents", flatCreateBody("req-own", "flat-agent",
		map[string]interface{}{"expectedRuntimeTargetId": f.identity.RuntimeTarget.ID, "config": map[string]interface{}{"template": "claude"}}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	rec, ok, err := f.srv.ownership.Get(flatTestProjectID, "agent-id-flat-agent")
	if err != nil || !ok {
		t.Fatalf("no ownership record after a create: %v", err)
	}
	if rec.AgentSlug != "flat-agent" || len(rec.Runs) != 1 {
		t.Fatalf("record = %+v", rec)
	}
}
