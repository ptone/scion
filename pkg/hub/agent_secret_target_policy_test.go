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

// Tests for the agent-route target policy (pkg/secret/agent_policy.go): the
// write-time env-name and file-target checks in agentSetSecret, the delivery
// drops in resolveSecrets, resolveAsNeededForKeys and selectDeliverySecrets,
// the runtime reads (checks 7 and 8) and the agent secret list filters.

package hub

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// targetPolicyFixture holds a project with one agent (the writer).
type targetPolicyFixture struct {
	projectID string
	writerID  string
}

func newTargetPolicyFixture(t *testing.T, st store.Store) targetPolicyFixture {
	t.Helper()
	ctx := context.Background()
	f := targetPolicyFixture{projectID: tid("tp-project"), writerID: tid("tp-writer")}
	if err := st.CreateProject(ctx, &store.Project{
		ID: f.projectID, Name: "Target Policy Project", Slug: "target-policy-project",
		Created: time.Now(), Updated: time.Now(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := st.CreateAgent(ctx, &store.Agent{
		ID: f.writerID, Slug: "tp-writer", Name: "tp-writer",
		ProjectID: f.projectID, Phase: string(state.PhaseRunning), StateVersion: 1,
		Created: time.Now(), Updated: time.Now(),
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return f
}

func seedTargetPolicySecret(t *testing.T, st store.Store, projectID, key, typ, target, createdBy, updatedBy, mode string) {
	t.Helper()
	if err := st.CreateSecret(context.Background(), &store.Secret{
		ID: tid("tp-sec-" + key), Key: key, EncryptedValue: "value-" + key,
		SecretType: typ, Target: target, Scope: store.ScopeProject, ScopeID: projectID,
		InjectionMode: mode, CreatedBy: createdBy, UpdatedBy: updatedBy,
	}); err != nil {
		t.Fatalf("create secret %s: %v", key, err)
	}
}

func newTargetPolicyDispatcher(st store.Store) *HTTPAgentDispatcher {
	d := NewHTTPAgentDispatcherWithClient(st, &mockRuntimeBrokerClient{}, false, slog.Default())
	d.SetSecretBackend(secret.NewLocalBackend(st, "test-hub-id", "test-secret"))
	return d
}

func targetPolicyResolvedNames(t *testing.T, d *HTTPAgentDispatcher, agent *store.Agent) map[string]bool {
	t.Helper()
	resolved, _, err := d.resolveSecrets(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
	out := make(map[string]bool, len(resolved))
	for _, r := range resolved {
		out[r.Name] = true
	}
	return out
}

// TestResolveSecrets_AgentWrittenUnsafeTargetsDropped: rows written through
// the agent route whose env name or file target the agent policy refuses are
// never delivered (for example rows that predate the write-time check); the
// same rows written by a user are delivered unchanged.
//
// MUTANT M3d (env delivery drop removed) fails the LD_PRELOAD and PATH rows.
// MUTANT M4d (file delivery drop removed) fails the ~/.bashrc, /etc and
// empty-target rows.
func TestResolveSecrets_AgentWrittenUnsafeTargetsDropped(t *testing.T) {
	st := createTestStore(t)
	f := newTargetPolicyFixture(t, st)
	d := newTargetPolicyDispatcher(st)
	writer := "agent:" + f.writerID
	recipient := &store.Agent{ID: tid("tp-recipient"), ProjectID: f.projectID}

	seedTargetPolicySecret(t, st, f.projectID, "PRELOAD_TOKEN", store.SecretTypeEnvironment, "LD_PRELOAD", writer, writer, store.InjectionModeAlways)
	seedTargetPolicySecret(t, st, f.projectID, "RC_FILE", store.SecretTypeFile, "~/.bashrc", writer, writer, store.InjectionModeAlways)
	seedTargetPolicySecret(t, st, f.projectID, "ROOT_FILE", store.SecretTypeFile, "/etc/ld.so.preload", writer, writer, store.InjectionModeAlways)
	seedTargetPolicySecret(t, st, f.projectID, "NOTARGET_FILE", store.SecretTypeFile, "", writer, writer, store.InjectionModeAlways)
	seedTargetPolicySecret(t, st, f.projectID, "OK_FILE", store.SecretTypeFile, "~/.scion/agent-secrets/cert.pem", writer, writer, store.InjectionModeAlways)
	seedTargetPolicySecret(t, st, f.projectID, "OK_TOKEN", store.SecretTypeEnvironment, "OK_TOKEN", writer, writer, store.InjectionModeAlways)
	// The same unsafe targets written by a user are untouched by this policy.
	seedTargetPolicySecret(t, st, f.projectID, "USER_PRELOAD", store.SecretTypeEnvironment, "LD_PRELOAD_USER_TOKEN", "user-admin", "user-admin", store.InjectionModeAlways)
	seedTargetPolicySecret(t, st, f.projectID, "USER_RC_FILE", store.SecretTypeFile, "/etc/ssl/cert.pem", "user-admin", "user-admin", store.InjectionModeAlways)

	got := targetPolicyResolvedNames(t, d, recipient)
	for _, dropped := range []string{"PRELOAD_TOKEN", "RC_FILE", "ROOT_FILE", "NOTARGET_FILE"} {
		if got[dropped] {
			t.Errorf("%s: agent-written secret with a refused target was delivered", dropped)
		}
	}
	for _, kept := range []string{"OK_FILE", "OK_TOKEN", "USER_PRELOAD", "USER_RC_FILE"} {
		if !got[kept] {
			t.Errorf("%s: expected to be delivered", kept)
		}
	}

	seedTargetPolicySecret(t, st, f.projectID, "PATHY_TOKEN", store.SecretTypeEnvironment, "PATH", writer, writer, store.InjectionModeAsNeeded)
	asNeeded, err := d.resolveAsNeededForKeys(context.Background(), recipient, []string{"PATH"}, nil)
	if err != nil {
		t.Fatalf("resolveAsNeededForKeys: %v", err)
	}
	if _, ok := asNeeded["PATH"]; ok {
		t.Error("as-needed path served an agent-written PATH")
	}
}

// TestAgentGetSecret_AgentWrittenUnsafeTargetNotFound: the runtime read
// (check 7) hides an agent-written project secret whose env name the agent
// policy refuses, as start-time delivery does; a safe agent-written secret
// stays readable.
//
// MUTANT M3e (check-7 target drop removed) fails it.
func TestAgentGetSecret_AgentWrittenUnsafeTargetNotFound(t *testing.T) {
	srv, s, agentID, projectID, agentToken := setupAgentSecretGetTest(t)
	writer := "agent:" + agentID
	seedTargetPolicySecret(t, s, projectID, "PRELOAD_GET_TOKEN", store.SecretTypeEnvironment, "LD_PRELOAD", writer, writer, store.InjectionModeAlways)
	seedTargetPolicySecret(t, s, projectID, "SAFE_GET_TOKEN", store.SecretTypeEnvironment, "SAFE_GET_TOKEN", writer, writer, store.InjectionModeAlways)

	rec := doRequestWithAgentToken(t, srv, http.MethodGet, "/api/v1/agents/"+agentID+"/secrets/PRELOAD_GET_TOKEN", nil, agentToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("agent-written LD_PRELOAD target: status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	rec = doRequestWithAgentToken(t, srv, http.MethodGet, "/api/v1/agents/"+agentID+"/secrets/SAFE_GET_TOKEN", nil, agentToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent-written safe target: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func putTargetPolicySecret(t *testing.T, srv *Server, agentID, token, key string, body AgentSetSecretRequest) int {
	t.Helper()
	if body.Value == "" {
		body.Value = base64.StdEncoding.EncodeToString([]byte("v"))
	}
	rec := doRequestWithAgentToken(t, srv, http.MethodPut, "/api/v1/agents/"+agentID+"/secrets/"+key, body, token)
	return rec.Code
}

// TestAgentSetSecret_DangerousEnvTarget_Rejected: write-time env policy.
//
// MUTANT M3c (write-time env check removed) fails it.
func TestAgentSetSecret_DangerousEnvTarget_Rejected(t *testing.T) {
	srv, _, agentID, _, agentToken := setupAgentSecretTest(t)
	for _, target := range []string{"BASH_ENV", "LD_PRELOAD", "PATH", "GIT_SSH_COMMAND", "GITHUB_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "NODE_OPTIONS"} {
		if code := putTargetPolicySecret(t, srv, agentID, agentToken, "SOME_TOKEN_"+target, AgentSetSecretRequest{Type: "environment", Target: target}); code != http.StatusBadRequest {
			t.Errorf("target %s: status = %d, want 400", target, code)
		}
	}
	// A key that is itself a refused name (target defaults to the key).
	if code := putTargetPolicySecret(t, srv, agentID, agentToken, "PROMPT_COMMAND", AgentSetSecretRequest{}); code != http.StatusBadRequest {
		t.Errorf("key PROMPT_COMMAND: status = %d, want 400", code)
	}
	if code := putTargetPolicySecret(t, srv, agentID, agentToken, "GOOD_TOKEN", AgentSetSecretRequest{}); code != http.StatusCreated {
		t.Errorf("GOOD_TOKEN: status = %d, want 201", code)
	}
}

// TestAgentSetSecret_FileTargetOutsideDir_Rejected: write-time file policy.
//
// MUTANT M4c (write-time file check removed) fails it.
func TestAgentSetSecret_FileTargetOutsideDir_Rejected(t *testing.T) {
	srv, _, agentID, _, agentToken := setupAgentSecretTest(t)
	for _, target := range []string{"/etc/ld.so.preload", "~/.bashrc", "~/.profile", "~/.copilot/config.json", "~/.scion/agent-secrets/../.bashrc"} {
		if code := putTargetPolicySecret(t, srv, agentID, agentToken, "F_KEY", AgentSetSecretRequest{Type: "file", Target: target}); code != http.StatusBadRequest {
			t.Errorf("target %s: status = %d, want 400", target, code)
		}
	}
	if code := putTargetPolicySecret(t, srv, agentID, agentToken, "F_NOTARGET", AgentSetSecretRequest{Type: "file"}); code != http.StatusBadRequest {
		t.Errorf("file without target: status = %d, want 400", code)
	}
	if code := putTargetPolicySecret(t, srv, agentID, agentToken, "F_OK", AgentSetSecretRequest{Type: "file", Target: "~/.scion/agent-secrets/cert.pem"}); code != http.StatusCreated {
		t.Errorf("agent-secrets dir target: status = %d, want 201", code)
	}
}

// setTargetPolicyRow stores an always-mode secret through the backend.
// createdBy stays the scope owner (so source and lineage checks pass as for
// any shared row); updatedBy carries the agent-route writer prefix, which is
// what marks the row agent-written (secret.IsAgentWritten).
func setTargetPolicyRow(t *testing.T, b secret.SecretBackend, key, typ, target, scope, scopeID, createdBy, updatedBy string) {
	t.Helper()
	if _, _, err := b.Set(context.Background(), &secret.SetSecretInput{
		Name: key, Value: "v-" + key, SecretType: typ, Target: target,
		Scope: scope, ScopeID: scopeID, InjectionMode: store.InjectionModeAlways,
		AllowProgeny: scope == store.ScopeUser, CreatedBy: createdBy, UpdatedBy: updatedBy,
	}); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

// TestAgentListSecrets_AgentWrittenUnsafeTargetsHidden: the agent secret list
// hides agent-written rows whose env name or file target the agent policy
// refuses, in each list path, and keeps a safe agent-written row next to it
// (the control that shows the row would otherwise be listed):
//   - project part (agentListSecrets) and the user part on a chain without
//     recorded provenance (agentListUserSecretsProgeny): newMaterialFixture;
//   - user part on a recorded chain (agentListUserSecrets): a recorded
//     principal-ceiling edge.
//
// MUTANT M3g (any one of the three list filters removed) fails the
// matching subtest.
func TestAgentListSecrets_AgentWrittenUnsafeTargetsHidden(t *testing.T) {
	t.Run("project and unrecorded user chain", func(t *testing.T) {
		f := &userScopeCeilingFixture{materialFixture: newMaterialFixture(t, "tp-list-unrecorded")}
		writer := "agent:" + f.AgentID
		b := f.Server.secretBackend
		setTargetPolicyRow(t, b, "TP_PROJ_PRELOAD", store.SecretTypeEnvironment, "LD_PRELOAD", store.ScopeProject, f.ProjectID, f.UserID, writer)
		setTargetPolicyRow(t, b, "TP_PROJ_RCFILE", store.SecretTypeFile, "~/.bashrc", store.ScopeProject, f.ProjectID, f.UserID, writer)
		setTargetPolicyRow(t, b, "TP_PROJ_SAFE_TOKEN", store.SecretTypeEnvironment, "TP_PROJ_SAFE_TOKEN", store.ScopeProject, f.ProjectID, f.UserID, writer)
		setTargetPolicyRow(t, b, "TP_USER_PRELOAD", store.SecretTypeEnvironment, "LD_PRELOAD", store.ScopeUser, f.UserID, f.UserID, writer)
		setTargetPolicyRow(t, b, "TP_USER_SAFE_TOKEN", store.SecretTypeEnvironment, "TP_USER_SAFE_TOKEN", store.ScopeUser, f.UserID, f.UserID, writer)

		code, keys := f.listKeys(t, f.AgentID, f.Token, "")
		if code != http.StatusOK {
			t.Fatalf("list: status = %d, want 200", code)
		}
		for _, hidden := range []string{"TP_PROJ_PRELOAD", "TP_PROJ_RCFILE", "TP_USER_PRELOAD"} {
			if containsKey(keys, hidden) {
				t.Errorf("%s: agent-written secret with a refused target was listed", hidden)
			}
		}
		for _, listed := range []string{"TP_PROJ_SAFE_TOKEN", "TP_USER_SAFE_TOKEN"} {
			if !containsKey(keys, listed) {
				t.Errorf("%s: expected to be listed (control): %v", listed, keys)
			}
		}
	})

	t.Run("recorded user chain", func(t *testing.T) {
		f := newUserScopeCeilingFixture(t, "tp-list-recorded", store.EffectCeiling{Kind: store.EffectCeilingPrincipal})
		writer := "agent:" + f.AgentID
		b := f.Server.secretBackend
		setTargetPolicyRow(t, b, "TP_REC_USER_PRELOAD", store.SecretTypeEnvironment, "LD_PRELOAD", store.ScopeUser, f.UserID, f.UserID, writer)
		setTargetPolicyRow(t, b, "TP_REC_USER_SAFE_TOKEN", store.SecretTypeEnvironment, "TP_REC_USER_SAFE_TOKEN", store.ScopeUser, f.UserID, f.UserID, writer)

		code, keys := f.listKeys(t, f.AgentID, f.Token, store.ScopeUser)
		if code != http.StatusOK {
			t.Fatalf("list: status = %d, want 200", code)
		}
		if containsKey(keys, "TP_REC_USER_PRELOAD") {
			t.Error("TP_REC_USER_PRELOAD: agent-written secret with a refused target was listed")
		}
		if !containsKey(keys, "TP_REC_USER_SAFE_TOKEN") {
			t.Errorf("TP_REC_USER_SAFE_TOKEN: expected to be listed (control): %v", keys)
		}
	})
}

// TestAgentGetSecret_UserScopeAgentWrittenUnsafeTargetNotFound: the runtime
// read of a user-scope secret (check 8) hides an agent-written row whose env
// name or file target the agent policy refuses; a safe agent-written row in
// the same scope stays readable (the control).
//
// MUTANT M4e (check-8 target drop removed) fails it.
func TestAgentGetSecret_UserScopeAgentWrittenUnsafeTargetNotFound(t *testing.T) {
	f := newUserScopeCeilingFixture(t, "tp-get-user", store.EffectCeiling{Kind: store.EffectCeilingPrincipal})
	writer := "agent:" + f.AgentID
	b := f.Server.secretBackend
	setTargetPolicyRow(t, b, "TP_GET_USER_PRELOAD", store.SecretTypeEnvironment, "LD_PRELOAD", store.ScopeUser, f.UserID, f.UserID, writer)
	setTargetPolicyRow(t, b, "TP_GET_USER_RCFILE", store.SecretTypeFile, "~/.bashrc", store.ScopeUser, f.UserID, f.UserID, writer)
	setTargetPolicyRow(t, b, "TP_GET_USER_SAFE_TOKEN", store.SecretTypeEnvironment, "TP_GET_USER_SAFE_TOKEN", store.ScopeUser, f.UserID, f.UserID, writer)

	for _, key := range []string{"TP_GET_USER_PRELOAD", "TP_GET_USER_RCFILE"} {
		if code, body := f.readAs(t, f.AgentID, f.Token, key); code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404: %s", key, code, body)
		}
	}
	if code, body := f.readAs(t, f.AgentID, f.Token, "TP_GET_USER_SAFE_TOKEN"); code != http.StatusOK {
		t.Errorf("TP_GET_USER_SAFE_TOKEN: status = %d, want 200 (control): %s", code, body)
	}
}

// TestSelectDeliverySecrets_AgentWrittenUnsafeTargetsDropped: authorized
// start-time delivery (selectDeliverySecrets) drops agent-written rows whose
// env name or file target the agent policy refuses, and delivers a safe
// agent-written env and file row next to them (the controls).
//
// MUTANT M3h (selectDeliverySecrets target drop removed) fails it.
func TestSelectDeliverySecrets_AgentWrittenUnsafeTargetsDropped(t *testing.T) {
	m := newLDFixture(t, "tp-select", ldOptions{})
	ctx := context.Background()
	pid := m.agent.ProjectID
	writer := "agent:" + m.agent.ID
	setTargetPolicyRow(t, m.backend, "TP_SEL_PRELOAD", store.SecretTypeEnvironment, "LD_PRELOAD", store.ScopeProject, pid, m.projectOwnerID, writer)
	setTargetPolicyRow(t, m.backend, "TP_SEL_RCFILE", store.SecretTypeFile, "~/.bashrc", store.ScopeProject, pid, m.projectOwnerID, writer)
	setTargetPolicyRow(t, m.backend, "TP_SEL_SAFE_TOKEN", store.SecretTypeEnvironment, "TP_SEL_SAFE_TOKEN", store.ScopeProject, pid, m.projectOwnerID, writer)
	setTargetPolicyRow(t, m.backend, "TP_SEL_SAFE_FILE", store.SecretTypeFile, "~/.scion/agent-secrets/tp.pem", store.ScopeProject, pid, m.projectOwnerID, writer)

	got, _, err := m.d.selectDeliverySecrets(ctx, newDeliverySelector(m.authz, m.agent, PurposeDispatch), m.agent)
	if err != nil {
		t.Fatalf("selectDeliverySecrets: %v", err)
	}
	byName := make(map[string]bool, len(got))
	for _, s := range got {
		byName[s.Name] = true
	}
	for _, dropped := range []string{"TP_SEL_PRELOAD", "TP_SEL_RCFILE"} {
		if byName[dropped] {
			t.Errorf("%s: agent-written secret with a refused target was delivered", dropped)
		}
	}
	for _, kept := range []string{"TP_SEL_SAFE_TOKEN", "TP_SEL_SAFE_FILE"} {
		if !byName[kept] {
			t.Errorf("%s: expected to be delivered (control)", kept)
		}
	}
}
