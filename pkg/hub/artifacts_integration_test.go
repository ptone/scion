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
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/artifacts"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enableArtifactsForTest turns the artifact service on for srv: the
// hub.artifacts experiment (through a registry where it defaults on, so no
// operational settings are needed), an artifact store on its own SQLite
// file, and local blob storage. It returns the store and the storage.
func enableArtifactsForTest(t *testing.T, srv *Server) (artifacts.Store, *storage.LocalStorage) {
	t.Helper()
	var active []experiments.Experiment
	for _, e := range experiments.Default().All() {
		if e.Name == experiments.Artifacts {
			e.Default = true
		}
		active = append(active, e)
	}
	reg, err := experiments.NewRegistry(active, nil)
	require.NoError(t, err)
	srv.experiments = reg
	require.True(t, srv.experimentEnabled(experiments.Artifacts))

	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "artifacts.db")+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	st := artifacts.NewStore(db, "sqlite")
	require.NoError(t, st.Init(context.Background()))
	srv.SetArtifactStore(st)

	blobs, err := storage.NewLocal(storage.Config{Provider: storage.ProviderLocal, Bucket: "hub", LocalPath: t.TempDir()})
	require.NoError(t, err)
	srv.SetStorage(blobs)
	return st, blobs
}

// artifactAgent creates an agent row in project and mints it a token with
// the scopes of role, as the hub does at dispatch.
func artifactAgent(t *testing.T, srv *Server, s store.Store, projectID, slug string, role AgentRole) (*store.Agent, string) {
	t.Helper()
	a := &store.Agent{ID: tid("art-" + slug), Slug: slug, Name: slug, ProjectID: projectID, Phase: "running"}
	require.NoError(t, s.CreateAgent(context.Background(), a))
	tok, err := srv.GetAgentTokenService().GenerateAgentToken(a.ID, projectID, ScopesForRole(role), nil)
	require.NoError(t, err)
	return a, tok
}

func artifactProject(t *testing.T, s store.Store, slug string) *store.Project {
	t.Helper()
	p := &store.Project{ID: tid("art-" + slug), Name: slug, Slug: slug, Created: time.Now(), Updated: time.Now()}
	require.NoError(t, s.CreateProject(context.Background(), p))
	return p
}

func agentClient(t *testing.T, baseURL, token string) hubclient.Client {
	t.Helper()
	c, err := hubclient.New(baseURL, hubclient.WithAgentToken(token))
	require.NoError(t, err)
	return c
}

func sha256OfReader(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	_, err := io.Copy(h, rc)
	require.NoError(t, err)
	return hex.EncodeToString(h.Sum(nil))
}

func requireHubStatus(t *testing.T, err error, status int) {
	t.Helper()
	var apiErr *apiclient.APIError
	require.True(t, errors.As(err, &apiErr), "want an API error with status %d, got %v", status, err)
	require.Equal(t, status, apiErr.StatusCode, apiErr.Error())
}

// TestArtifactsTwoAgentsSameProject is the cross-broker publish/get
// acceptance criterion of P1 (ptone/scion#3208), in process: one hub,
// two distinct agent identities in the same project (as if on two
// different runtime brokers, sharing nothing but the hub). Agent A
// publishes through the hub client; agent B fetches by reference and gets
// byte-identical content. An agent in another project gets 404 on every
// read route, the same answer as for an artifact that does not exist.
func TestArtifactsTwoAgentsSameProject(t *testing.T) {
	srv, s := testServer(t)
	enableArtifactsForTest(t, srv)
	hub := httptest.NewServer(srv.Handler())
	t.Cleanup(hub.Close)

	p1 := artifactProject(t, s, "p1")
	p2 := artifactProject(t, s, "p2")
	agentA, tokA := artifactAgent(t, srv, s, p1.ID, "agent-a", AgentRoleBaseline)
	_, tokB := artifactAgent(t, srv, s, p1.ID, "agent-b", AgentRoleBaseline)
	_, tokX := artifactAgent(t, srv, s, p2.ID, "agent-x", AgentRoleBaseline)
	a, b, x := agentClient(t, hub.URL, tokA), agentClient(t, hub.URL, tokB), agentClient(t, hub.URL, tokX)
	ctx := context.Background()

	content := []byte("# Artifact system design\n\nPublished by agent A, read by agent B.\n")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])

	// Agent A publishes; the artifact is owned by A's stable id and homed in p1.
	pub, err := a.Artifacts().Publish(ctx, &hubclient.PublishArtifactRequest{
		Name: "design.md", Title: "Artifact system design",
		Content: bytes.NewReader(content), Size: int64(len(content)), SHA256: digest,
	})
	require.NoError(t, err)
	assert.Equal(t, "scion://artifact/"+pub.Artifact.ID, pub.Artifact.Ref)
	assert.Equal(t, artifacts.PrincipalKindAgent, pub.Artifact.OwnerKind)
	assert.Equal(t, agentA.ID, pub.Artifact.OwnerRef)
	assert.Equal(t, p1.ID, pub.Artifact.ScopeRef)
	require.NotNil(t, pub.Version)
	assert.Equal(t, 1, pub.Version.Seq)

	// Agent B resolves the reference and fetches byte-identical content.
	id, seq, err := artifacts.ParseRef(pub.Artifact.Ref)
	require.NoError(t, err)
	meta, err := b.Artifacts().Get(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, meta.Version)
	assert.Equal(t, digest, meta.Version.Files[0].SHA256)
	for _, s := range []int{seq, 1} {
		rc, err := b.Artifacts().OpenFile(ctx, id, s, meta.Version.EntryPath)
		require.NoError(t, err)
		assert.Equal(t, digest, sha256OfReader(t, rc), "seq %d: agent B's bytes differ from what agent A published", s)
	}

	// An agent in another project: 404 everywhere, like a missing artifact.
	_, err = x.Artifacts().Get(ctx, id)
	requireHubStatus(t, err, http.StatusNotFound)
	_, err = x.Artifacts().OpenFile(ctx, id, 0, "design.md")
	requireHubStatus(t, err, http.StatusNotFound)
	_, err = x.Artifacts().OpenFile(ctx, id, 1, "design.md")
	requireHubStatus(t, err, http.StatusNotFound)
	_, err = b.Artifacts().Get(ctx, "00000000-0000-4000-8000-000000000000")
	requireHubStatus(t, err, http.StatusNotFound)
}

// TestArtifactsAgentScopes pins the least-privilege agent scope wiring: a
// read-only agent can read its project's artifacts but cannot publish
// (project:artifact:write is minted for baseline and full only), and an
// agent cannot publish into another project.
func TestArtifactsAgentScopes(t *testing.T) {
	assert.Contains(t, ScopesForRole(AgentRoleBaseline), ScopeProjectArtifactWrite)
	assert.Contains(t, ScopesForRole(AgentRoleFull), ScopeProjectArtifactWrite)
	assert.NotContains(t, ScopesForRole(AgentRoleReadOnly), ScopeProjectArtifactWrite)

	srv, s := testServer(t)
	enableArtifactsForTest(t, srv)
	hub := httptest.NewServer(srv.Handler())
	t.Cleanup(hub.Close)
	p1 := artifactProject(t, s, "scopes-p1")
	p2 := artifactProject(t, s, "scopes-p2")
	_, tokW := artifactAgent(t, srv, s, p1.ID, "writer", AgentRoleBaseline)
	_, tokR := artifactAgent(t, srv, s, p1.ID, "reader", AgentRoleReadOnly)
	w, r := agentClient(t, hub.URL, tokW), agentClient(t, hub.URL, tokR)
	ctx := context.Background()

	publish := func(c hubclient.Client, scope string) (*hubclient.ArtifactResponse, error) {
		return c.Artifacts().Publish(ctx, &hubclient.PublishArtifactRequest{
			Name: "notes.txt", Scope: scope, Content: bytes.NewReader([]byte("notes")), Size: 5,
		})
	}
	pub, err := publish(w, "")
	require.NoError(t, err)

	_, err = publish(r, "")
	requireHubStatus(t, err, http.StatusForbidden)
	_, err = publish(w, p2.ID)
	requireHubStatus(t, err, http.StatusForbidden)

	// The read-only agent still reads through project:artifact:read.
	rc, err := r.Artifacts().OpenFile(ctx, pub.Artifact.ID, 0, "notes.txt")
	require.NoError(t, err)
	_ = rc.Close()
}

// TestArtifactsExperimentOffAnswers404 checks that every artifact route is
// 404 while hub.artifacts is off (P1 acceptance), with the service fully
// configured: turning the experiment off hides every route, including for a
// caller who could otherwise read.
func TestArtifactsExperimentOffAnswers404(t *testing.T) {
	srv, s := testServer(t)
	enableArtifactsForTest(t, srv)
	p1 := artifactProject(t, s, "off-p1")
	_, tok := artifactAgent(t, srv, s, p1.ID, "off-agent", AgentRoleBaseline)

	rec := doRequestWithAgentToken(t, srv, http.MethodGet, "/api/v1/artifacts/00000000-0000-4000-8000-000000000000", nil, tok)
	require.Equal(t, http.StatusNotFound, rec.Code)

	hub := httptest.NewServer(srv.Handler())
	t.Cleanup(hub.Close)
	pub, err := agentClient(t, hub.URL, tok).Artifacts().Publish(context.Background(), &hubclient.PublishArtifactRequest{
		Name: "a.txt", Content: bytes.NewReader([]byte("a")), Size: 1,
	})
	require.NoError(t, err)

	srv.experiments = experiments.Default() // hub.artifacts defaults off
	for _, rq := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/artifacts/" + pub.Artifact.ID},
		{http.MethodGet, "/api/v1/artifacts/" + pub.Artifact.ID + "/files/a.txt"},
		{http.MethodGet, "/api/v1/artifacts/" + pub.Artifact.ID + "/versions/1/files/a.txt"},
		{http.MethodPost, "/api/v1/artifacts?name=b.txt"},
		{http.MethodGet, "/api/v1/artifacts/shared/token"},
	} {
		rec := doRequestWithAgentToken(t, srv, rq.method, rq.path, nil, tok)
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s %s", rq.method, rq.path)
	}
}

// TestArtifactWriteScopeDelegation: an agent whose token predates the
// artifact scopes can still delegate the full role, and an explicitly
// requested artifact write scope still requires the delegator to hold it.
func TestArtifactWriteScopeDelegation(t *testing.T) {
	authz, _ := authzTestSetup(t)
	var withoutArtifact []AgentTokenScope
	for _, s := range ScopesForRole(AgentRoleFull) {
		if s != ScopeProjectArtifactWrite {
			withoutArtifact = append(withoutArtifact, s)
		}
	}
	actor := &agentIdentityWrapper{&AgentTokenClaims{Scopes: withoutArtifact}}
	grant := GrantDescriptor{Type: GrantTypeAgentDelegation, AgentRole: string(AgentRoleFull), ProjectID: tid("deleg-artifact")}
	decision := authz.CanDelegate(context.Background(), actor, grant)
	assert.True(t, decision.Allowed, decision.Reason)

	grant.AgentScopes = []AgentTokenScope{ScopeProjectArtifactWrite}
	decision = authz.CanDelegate(context.Background(), actor, grant)
	assert.False(t, decision.Allowed, "an explicitly requested scope the actor lacks must be denied")
}

// userArtifactRequest sends an artifact request as user with a raw body.
func userArtifactRequest(t *testing.T, srv *Server, user *store.User, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	token, _, _, err := srv.userTokenService.GenerateTokenPair(user.ID, user.Email, user.DisplayName, user.Role, ClientTypeWeb)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestArtifactsProjectMembers covers the user side (a user opens the
// artifact; P1 acceptance, ptone/scion#3208): project
// roles carry artifact.read and artifact.create, so a project member can
// publish into the project and other members and the project's agents can
// read it; a user without a role in the project cannot publish there and
// gets 404 on every read route.
func TestArtifactsProjectMembers(t *testing.T) {
	srv, s := testServer(t)
	enableArtifactsForTest(t, srv)
	ctx := context.Background()
	p1 := artifactProject(t, s, "members-p1")
	createTestUserWithProjectRole(t, s, tid("art-member"), "art-member@test.com", p1.ID, store.ProjectRoleMember)
	createTestUserWithProjectRole(t, s, tid("art-member2"), "art-member2@test.com", p1.ID, store.ProjectRoleMember)
	require.NoError(t, s.CreateUser(ctx, &store.User{ID: tid("art-outsider"), Email: "art-outsider@test.com", DisplayName: "Outsider", Role: "member", Status: "active"}))
	member, err := s.GetUser(ctx, tid("art-member"))
	require.NoError(t, err)
	member2, err := s.GetUser(ctx, tid("art-member2"))
	require.NoError(t, err)
	outsider, err := s.GetUser(ctx, tid("art-outsider"))
	require.NoError(t, err)
	_, agentTok := artifactAgent(t, srv, s, p1.ID, "members-agent", AgentRoleBaseline)

	// A member publishes into the project (users name the scope).
	rec := userArtifactRequest(t, srv, member, http.MethodPost, "/api/v1/artifacts?name=report.md&scope="+p1.ID, []byte("# Report\n"))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	id := decodeArtifactID(t, rec)

	paths := []string{
		"/api/v1/artifacts/" + id,
		"/api/v1/artifacts/" + id + "/files/report.md",
		"/api/v1/artifacts/" + id + "/versions/1/files/report.md",
	}
	for _, p := range paths {
		assert.Equal(t, http.StatusOK, userArtifactRequest(t, srv, member, http.MethodGet, p, nil).Code, "owner %s", p)
		assert.Equal(t, http.StatusOK, userArtifactRequest(t, srv, member2, http.MethodGet, p, nil).Code, "member %s", p)
		assert.Equal(t, http.StatusOK, doRequestWithAgentToken(t, srv, http.MethodGet, p, nil, agentTok).Code, "project agent %s", p)
		assert.Equal(t, http.StatusNotFound, userArtifactRequest(t, srv, outsider, http.MethodGet, p, nil).Code, "outsider %s", p)
	}

	rec = userArtifactRequest(t, srv, outsider, http.MethodPost, "/api/v1/artifacts?name=x.md&scope="+p1.ID, []byte("x"))
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

func decodeArtifactID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp artifacts.ArtifactResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Artifact.ID)
	return resp.Artifact.ID
}
