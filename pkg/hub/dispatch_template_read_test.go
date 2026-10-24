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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/templatecache"
	"github.com/GoogleCloudPlatform/scion/pkg/transfer"
)

// ============================================================================
// ptone/scion#5770: a Runtime Broker may read a user-scoped template only for
// an agent the hub records as running on it, bound to the template by the
// hub-written AppliedConfig.TemplateID, at the content hash recorded for the
// agent, and only while the agent's creator can still read the template.
// Everything else keeps the ptone/scion#1916 answer: 404.
// ============================================================================

type dispatchReadFixture struct {
	*brokerScopeFixture
	creator  *store.User
	userTmpl *store.Template // user-scoped, owned by creator
	twin     *store.Template // global template with the same slug
	agent    *store.Agent    // on f.broker, bound to userTmpl
	other    *store.RuntimeBroker
}

const dispatchReadHash = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func setupDispatchReadTest(t *testing.T) *dispatchReadFixture {
	t.Helper()
	ctx := context.Background()
	f := &dispatchReadFixture{brokerScopeFixture: setupBrokerScopeTest(t)}

	f.creator = &store.User{
		ID: tid("dbr-creator"), Email: "dbr-creator@test.com", DisplayName: "Creator",
		Role: store.UserRoleMember, Status: store.UserStatusActive, Created: time.Now(),
	}
	require.NoError(t, f.store.CreateUser(ctx, f.creator))
	ensureHubMembership(ctx, f.store, f.creator.ID)

	f.userTmpl = &store.Template{
		ID: tid("dbr-user-tmpl"), Slug: "dbr-shared", Name: "dbr-shared",
		Scope: store.TemplateScopeUser, ScopeID: f.creator.ID, OwnerID: f.creator.ID,
		Status: store.TemplateStatusActive, Harness: "claude", ContentHash: dispatchReadHash,
		Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, f.store.CreateTemplate(ctx, f.userTmpl))
	f.twin = &store.Template{
		ID: tid("dbr-global-twin"), Slug: "dbr-shared", Name: "dbr-shared",
		Scope: store.TemplateScopeGlobal, Status: store.TemplateStatusActive, Harness: "claude",
		ContentHash: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		Created:     time.Now(), Updated: time.Now(),
	}
	require.NoError(t, f.store.CreateTemplate(ctx, f.twin))

	f.agent = f.boundAgent(t, "dbr-agent", f.broker.ID, f.userTmpl.ID, dispatchReadHash, f.creator.ID)

	f.other = &store.RuntimeBroker{
		ID: tid("dbr-other-broker"), Name: "dbr-other-broker", Slug: "dbr-other-broker",
		Status: store.BrokerStatusOnline, Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, f.store.CreateRuntimeBroker(ctx, f.other))
	require.NoError(t, f.store.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID: f.other.ID, SecretKey: []byte("dbr-other-secret-32-bytes!!!!!!"), Algorithm: store.BrokerSecretAlgorithmHMACSHA256, Status: store.BrokerSecretStatusActive,
	}))
	require.NoError(t, f.store.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID: f.projP.ID, BrokerID: f.other.ID, BrokerName: f.other.Name, Status: store.BrokerStatusOnline,
	}))
	return f
}

// boundAgent creates an agent in project P on brokerID whose hub-written
// applied config names templateID at hash, created by createdBy.
func (f *dispatchReadFixture) boundAgent(t *testing.T, slug, brokerID, templateID, hash, createdBy string) *store.Agent {
	t.Helper()
	a := &store.Agent{
		ID: tid(slug), Slug: slug, Name: slug, ProjectID: f.projP.ID,
		RuntimeBrokerID: brokerID, Phase: string(state.PhaseRunning),
		Template: "dbr-shared", CreatedBy: createdBy, OwnerID: createdBy,
		AppliedConfig: &store.AgentAppliedConfig{TemplateID: templateID, TemplateHash: hash},
	}
	require.NoError(t, f.store.CreateAgent(context.Background(), a))
	return a
}

// signedBrokerRequest signs a request for signedTarget as broker (secret)
// and returns it with its URL replaced by sentTarget, so a test can tamper
// with the query after signing.
func signedBrokerRequest(t *testing.T, srv *Server, brokerID string, secret []byte, method, signedTarget, sentTarget string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, signedTarget, bytes.NewReader(nil))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "dbr-nonce-" + uuid.New().String()
	req.Header.Set(HeaderBrokerID, brokerID)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	mac := hmac.New(sha256.New, secret)
	mac.Write(srv.brokerAuthService.buildCanonicalString(req, timestamp, nonce))
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	if sentTarget != signedTarget {
		u, err := url.Parse(sentTarget)
		require.NoError(t, err)
		req.URL.Path, req.URL.RawQuery = u.Path, u.RawQuery
		req.RequestURI = sentTarget
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *dispatchReadFixture) asOther(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	return signedBrokerRequest(t, f.srv, f.other.ID, []byte("dbr-other-secret-32-bytes!!!!!!"), http.MethodGet, target, target)
}

// commitTemplateContent applies mutate to template id and writes its content
// and storage columns the way a template commit does (UpdateTemplate writes
// metadata only).
func commitTemplateContent(t *testing.T, st store.Store, id string, mutate func(*store.Template)) *store.Template {
	t.Helper()
	ctx := context.Background()
	tmpl, err := st.GetTemplate(ctx, id)
	require.NoError(t, err)
	expected := store.TemplateContentPrecondition{ContentHash: tmpl.ContentHash, Layout: tmpl.Layout}
	mutate(tmpl)
	require.NoError(t, st.UpdateTemplateContent(ctx, tmpl, expected))
	got, err := st.GetTemplate(ctx, id)
	require.NoError(t, err)
	return got
}

func templateGetFor(tmplID, agentID string) string {
	target := "/api/v1/templates/" + tmplID
	if agentID != "" {
		target += "?agentId=" + url.QueryEscape(agentID)
	}
	return target
}

// The broker running the agent reads the agent's user-scoped template on
// every read surface (get, download, files).
func TestDispatchBoundRead_UserTemplate_BoundAgentOnThisBroker_Allowed(t *testing.T) {
	f := setupDispatchReadTest(t)

	rec := f.asBroker(t, http.MethodGet, templateGetFor(f.userTmpl.ID, f.agent.ID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got store.Template
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, f.userTmpl.ID, got.ID)

	// Download and files pass the gate (the template has no files and no
	// storage here, so they answer past it, but never with the gate's 404).
	rec = f.asBroker(t, http.MethodGet, "/api/v1/templates/"+f.userTmpl.ID+"/download?agentId="+f.agent.ID)
	assert.NotEqual(t, http.StatusNotFound, rec.Code, "download must pass the gate: %s", rec.Body.String())
	rec = f.asBroker(t, http.MethodGet, "/api/v1/templates/"+f.userTmpl.ID+"/files?agentId="+f.agent.ID)
	assert.NotEqual(t, http.StatusNotFound, rec.Code, "files must pass the gate: %s", rec.Body.String())
}

// Without an agent the ptone/scion#1916 answer stands, on every surface.
func TestDispatchBoundRead_UserTemplate_NoAgentID_Denied(t *testing.T) {
	f := setupDispatchReadTest(t)
	for _, target := range []string{
		"/api/v1/templates/" + f.userTmpl.ID,
		"/api/v1/templates/" + f.userTmpl.ID + "/download",
		"/api/v1/templates/" + f.userTmpl.ID + "/files",
	} {
		rec := f.asBroker(t, http.MethodGet, target)
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s: %s", target, rec.Body.String())
	}
}

// Every negative case reads as 404, on get, download and files.
func TestDispatchBoundRead_Denials(t *testing.T) {
	f := setupDispatchReadTest(t)
	ctx := context.Background()

	// An agent on this broker bound to a different template.
	otherTmpl := mustCreateScopeTemplate(t, f.store, "dbr-other-user-tmpl", store.TemplateScopeUser, f.creator.ID, f.creator.ID)
	// Same recorded hash as the user template, so only the TemplateID check
	// tells them apart.
	otherAgent := f.boundAgent(t, "dbr-other-agent", f.broker.ID, otherTmpl.ID, dispatchReadHash, f.creator.ID)

	// An agent whose slug names the template but whose TemplateID does not.
	slugOnly := f.boundAgent(t, "dbr-slug-only", f.broker.ID, f.twin.ID, dispatchReadHash, f.creator.ID)

	// A soft-deleted agent.
	deleted := f.boundAgent(t, "dbr-deleted", f.broker.ID, f.userTmpl.ID, dispatchReadHash, f.creator.ID)
	deleted.DeletedAt = time.Now()
	require.NoError(t, f.store.UpdateAgent(ctx, deleted))

	// An agent created by another agent (its identity cannot read a user
	// template).
	agentCreated := f.boundAgent(t, "dbr-agent-created", f.broker.ID, f.userTmpl.ID, dispatchReadHash, f.agent.ID)

	// An agent bound to the template at an older hash.
	stale := f.boundAgent(t, "dbr-stale-hash", f.broker.ID, f.userTmpl.ID,
		"sha256:3333333333333333333333333333333333333333333333333333333333333333", f.creator.ID)

	cases := []struct {
		name    string
		tmplID  string
		agentID string
	}{
		{"other agent on this broker, different TemplateID", f.userTmpl.ID, otherAgent.ID},
		{"slug matches but TemplateID does not", f.userTmpl.ID, slugOnly.ID},
		{"soft-deleted agent", f.userTmpl.ID, deleted.ID},
		{"unknown agent", f.userTmpl.ID, tid("dbr-no-such-agent")},
		{"creator is an agent", f.userTmpl.ID, agentCreated.ID},
		{"template changed since dispatch", f.userTmpl.ID, stale.ID},
	}
	for _, tc := range cases {
		for _, suffix := range []string{"", "/download", "/files"} {
			target := "/api/v1/templates/" + tc.tmplID + suffix + "?agentId=" + url.QueryEscape(tc.agentID)
			rec := f.asBroker(t, http.MethodGet, target)
			assert.Equal(t, http.StatusNotFound, rec.Code, "%s %s: %s", tc.name, suffix, rec.Body.String())
		}
	}
}

// Another broker (for example a second flat instance on the same host,
// sharing its cache) cannot read the template by naming the agent.
func TestDispatchBoundRead_AgentOnOtherBroker_Denied(t *testing.T) {
	f := setupDispatchReadTest(t)
	for _, suffix := range []string{"", "/download", "/files"} {
		rec := f.asOther(t, "/api/v1/templates/"+f.userTmpl.ID+suffix+"?agentId="+f.agent.ID)
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s: %s", suffix, rec.Body.String())
	}
}

// The read follows the hub's record of the broker: after the agent moves,
// the old broker loses access and the new one gains it.
func TestDispatchBoundRead_FollowsRuntimeBrokerID(t *testing.T) {
	f := setupDispatchReadTest(t)
	ctx := context.Background()
	moved, err := f.store.GetAgent(ctx, f.agent.ID)
	require.NoError(t, err)
	moved.RuntimeBrokerID = f.other.ID
	require.NoError(t, f.store.UpdateAgent(ctx, moved))

	assert.Equal(t, http.StatusNotFound, f.asBroker(t, http.MethodGet, templateGetFor(f.userTmpl.ID, f.agent.ID)).Code, "source after a move")
	assert.Equal(t, http.StatusOK, f.asOther(t, templateGetFor(f.userTmpl.ID, f.agent.ID)).Code, "target after a move")
}

// The creator check runs at read time: a deactivated creator stops reads.
func TestDispatchBoundRead_CreatorLostAccess_Denied(t *testing.T) {
	f := setupDispatchReadTest(t)
	ctx := context.Background()
	f.creator.Status = "suspended"
	require.NoError(t, f.store.UpdateUser(ctx, f.creator))

	rec := f.asBroker(t, http.MethodGet, templateGetFor(f.userTmpl.ID, f.agent.ID))
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// Global and project reads are unchanged: global with or without an agent;
// a project template of a project the broker does not serve stays denied
// even when an agent names it.
func TestDispatchBoundRead_GlobalAndProjectUnchanged(t *testing.T) {
	f := setupDispatchReadTest(t)
	assert.Equal(t, http.StatusOK, f.asBroker(t, http.MethodGet, templateGetFor(f.twin.ID, "")).Code)
	assert.Equal(t, http.StatusOK, f.asBroker(t, http.MethodGet, templateGetFor(f.twin.ID, f.agent.ID)).Code)

	projQTmpl := mustCreateScopeTemplate(t, f.store, "dbr-q-tmpl", store.TemplateScopeProject, f.projQ.ID, "")
	bound := f.boundAgent(t, "dbr-q-bound", f.broker.ID, projQTmpl.ID, "", f.creator.ID)
	assert.Equal(t, http.StatusNotFound, f.asBroker(t, http.MethodGet, templateGetFor(projQTmpl.ID, bound.ID)).Code,
		"a project template of a project the broker does not serve stays denied")
}

// The agentId is covered by the broker's HMAC signature: a request signed
// for one agent and sent naming another fails authentication.
func TestDispatchBoundRead_AgentIDCoveredBySignature(t *testing.T) {
	f := setupDispatchReadTest(t)
	signed := templateGetFor(f.userTmpl.ID, tid("dbr-unrelated"))
	sent := templateGetFor(f.userTmpl.ID, f.agent.ID)

	rec := signedBrokerRequest(t, f.srv, f.broker.ID, f.brokerSecret, http.MethodGet, signed, sent)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "a tampered agentId must fail verification: %s", rec.Body.String())

	rec = signedBrokerRequest(t, f.srv, f.broker.ID, f.brokerSecret, http.MethodGet, sent, sent)
	assert.Equal(t, http.StatusOK, rec.Code, "the untampered request passes: %s", rec.Body.String())
}

// With local storage the download answers hub proxy URLs; each carries the
// agent (escaped) so the raw read passes the same gate, and the same URL
// without it is refused.
func TestDispatchBoundRead_LocalStorageProxyURLsCarryAgent(t *testing.T) {
	f := setupDispatchReadTest(t)
	ctx := context.Background()
	stor, err := storage.NewLocal(storage.Config{Provider: storage.ProviderLocal, Bucket: "b", LocalPath: t.TempDir()})
	require.NoError(t, err)
	f.srv.SetStorage(stor)

	content := []byte("harness: claude\n")
	storagePath := "templates/user/" + f.creator.ID + "/dbr-shared"
	_, err = stor.Upload(ctx, storagePath+"/scion-agent.yaml", bytes.NewReader(content), storage.UploadOptions{})
	require.NoError(t, err)
	// Same content hash as the agent records: only files and storage change.
	tmpl := commitTemplateContent(t, f.store, f.userTmpl.ID, func(tm *store.Template) {
		tm.StoragePath, tm.StorageBucket = storagePath, "b"
		tm.Files = []store.TemplateFile{{Path: "scion-agent.yaml", Size: int64(len(content)), Hash: "sha256:placeholder"}}
	})

	rec := f.asBroker(t, http.MethodGet, "/api/v1/templates/"+tmpl.ID+"/download?agentId="+url.QueryEscape(f.agent.ID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var dl DownloadResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &dl))
	require.Len(t, dl.Files, 1)
	u, err := url.Parse(dl.Files[0].URL)
	require.NoError(t, err)
	assert.Equal(t, f.agent.ID, u.Query().Get("agentId"), "the proxy URL must carry the agent: %s", dl.Files[0].URL)

	rec = f.asBroker(t, http.MethodGet, u.RequestURI())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, content, rec.Body.Bytes())

	q := u.Query()
	q.Del("agentId")
	u.RawQuery = q.Encode()
	rec = f.asBroker(t, http.MethodGet, u.RequestURI())
	assert.Equal(t, http.StatusNotFound, rec.Code, "without the agent the raw read is refused: %s", rec.Body.String())
}

// ptone/scion#5770 issue test: with a user-scoped template and a global
// template of the same name, the broker's hydrator, talking to a real hub
// over its HMAC client, hydrates the USER template's content for the agent
// it runs, on a flat Runtime Broker row and on a legacy one. Without the
// agent the hub still refuses the read.
func TestDispatchBoundRead_HydratesUserTemplateOverHMAC(t *testing.T) {
	for _, flat := range []bool{false, true} {
		name := map[bool]string{false: "legacy broker", true: "flat broker"}[flat]
		t.Run(name, func(t *testing.T) {
			f := setupDispatchReadTest(t)
			ctx := context.Background()
			stor, err := storage.NewLocal(storage.Config{Provider: storage.ProviderLocal, Bucket: "b", LocalPath: t.TempDir()})
			require.NoError(t, err)
			f.srv.SetStorage(stor)

			// Give both templates real files with different content.
			withFiles := func(id, storagePath string, content []byte) string {
				_, err := stor.Upload(ctx, storagePath+"/scion-agent.yaml", bytes.NewReader(content), storage.UploadOptions{})
				require.NoError(t, err)
				tmpl := commitTemplateContent(t, f.store, id, func(tm *store.Template) {
					tm.StoragePath, tm.StorageBucket = storagePath, "b"
					tm.Files = []store.TemplateFile{{Path: "scion-agent.yaml", Size: int64(len(content)), Hash: transfer.HashBytes(content)}}
					tm.ContentHash = f.srv.computeContentHash(tm.Files)
				})
				return tmpl.ContentHash
			}
			userContent := []byte("harness: claude\n# user template\n")
			userHash := withFiles(f.userTmpl.ID, "templates/user/"+f.creator.ID+"/dbr-shared", userContent)
			withFiles(f.twin.ID, "templates/global/dbr-shared", []byte("harness: claude\n# global template\n"))

			brokerID, secret := f.broker.ID, f.brokerSecret
			if flat {
				fb := &store.RuntimeBroker{
					ID: tid("dbr-flat-broker"), Name: "dbr-flat", Slug: "dbr-flat", Status: store.BrokerStatusOnline,
					RuntimeTarget: &api.RuntimeTargetDescriptor{ID: tid("dbr-flat-target"), Type: "docker", DisplayName: "Docker"},
					Created:       time.Now(), Updated: time.Now(),
				}
				require.NoError(t, f.store.CreateRuntimeBroker(ctx, fb))
				secret = seedBrokerSecret(t, f.store, fb.ID)
				brokerID = fb.ID
			}
			a := f.boundAgent(t, "dbr-hydrate-agent", brokerID, f.userTmpl.ID, userHash, f.creator.ID)

			hubTS := httptest.NewServer(f.srv.Handler())
			defer hubTS.Close()
			hc, err := hubclient.New(hubTS.URL, hubclient.WithHMACAuth(brokerID, secret))
			require.NoError(t, err)
			cache, err := templatecache.New(t.TempDir(), 1<<20)
			require.NoError(t, err)
			hydrator := templatecache.NewHydrator(cache, hc)

			dir, err := hydrator.HydrateForAgent(ctx, f.userTmpl.ID, a.ID)
			require.NoError(t, err)
			got, err := os.ReadFile(filepath.Join(dir, "scion-agent.yaml"))
			require.NoError(t, err)
			assert.Equal(t, string(userContent), string(got), "the user template's content must be hydrated")

			_, err = templatecache.NewHydrator(cache, hc).Hydrate(ctx, f.userTmpl.ID)
			require.Error(t, err, "without the agent the hub refuses the read")
			assert.True(t, apiclient.IsNotFoundError(err), "a refusal reads as not found: %v", err)
		})
	}
}

func TestRewriteLocalTemplateDownloadURLs_AgentID(t *testing.T) {
	in := func() []DownloadURLInfo {
		return []DownloadURLInfo{
			{Path: "a.yaml", URL: "file:///x/a.yaml", Hash: "sha256:abc"},
			{Path: "b.yaml", URL: "https://signed.example/b"},
		}
	}
	got := rewriteLocalTemplateDownloadURLs(in(), "http://hub", "t1", true, "agent/1 x")
	assert.Equal(t, "http://hub/api/v1/templates/t1/files/a.yaml?raw=1&hash=sha256%3Aabc&agentId=agent%2F1+x", got[0].URL)
	assert.Equal(t, "https://signed.example/b", got[1].URL, "a signed storage URL is never rewritten")

	got = rewriteLocalTemplateDownloadURLs(in(), "http://hub", "t1", false, "a1")
	assert.Equal(t, "http://hub/api/v1/templates/t1/files/a.yaml?raw=1&agentId=a1", got[0].URL)

	got = rewriteLocalTemplateDownloadURLs(in(), "http://hub", "t1", false, "")
	assert.Equal(t, "http://hub/api/v1/templates/t1/files/a.yaml?raw=1", got[0].URL, "no agent: as before")
}

// ============================================================================
// Resume fill (ptone/scion#5770): a resume or restart that assigns a
// Runtime Broker to an agent with none persists it before dispatch, so the
// start's hydration passes the dispatch-bound read.
// ============================================================================

// fillProbeDispatcher records, at dispatch time, the broker stored on the
// agent row and whether the dispatch-bound gate admits the stored broker
// and another broker for the agent's user template.
type fillProbeDispatcher struct {
	createAgentDispatcher
	srv           *Server
	tmpl          *store.Template
	otherBrokerID string
	starts        int
	storedBroker  string
	storedAllowed bool
	otherAllowed  bool
	startErr      error
	// storedHash and dispatchedHash are the template hash on the agent row
	// and on the agent handed to the dispatch, at dispatch time.
	storedHash     string
	dispatchedHash string
	// storedVersion is the agent row's state version at dispatch time;
	// storedModel and dispatchedModel are the inline model on the row and on
	// the dispatched agent (a held edit folded at start, ptone/scion#3976).
	storedVersion   int64
	storedModel     string
	dispatchedModel string
	// stops counts stop dispatches (a restart's stop leg).
	stops int
}

func (d *fillProbeDispatcher) DispatchAgentStop(context.Context, *store.Agent) error {
	d.stops++
	return nil
}

func (d *fillProbeDispatcher) DispatchAgentStart(ctx context.Context, agent *store.Agent, task string, resume bool) error {
	d.starts++
	row, err := d.srv.store.GetAgent(ctx, agent.ID)
	if err != nil {
		return err
	}
	d.storedBroker = row.RuntimeBrokerID
	d.storedVersion = row.StateVersion
	if row.AppliedConfig != nil {
		d.storedHash = row.AppliedConfig.TemplateHash
		if row.AppliedConfig.InlineConfig != nil {
			d.storedModel = row.AppliedConfig.InlineConfig.Model
		}
	}
	if agent.AppliedConfig != nil {
		d.dispatchedHash = agent.AppliedConfig.TemplateHash
		if agent.AppliedConfig.InlineConfig != nil {
			d.dispatchedModel = agent.AppliedConfig.InlineConfig.Model
		}
	}
	// The gate reads the template's current row, as the hub does.
	tmpl, err := d.srv.store.GetTemplate(ctx, d.tmpl.ID)
	if err != nil {
		return err
	}
	res := dispatchCatalogResource{Kind: catalogKindTemplate, ID: tmpl.ID, Scope: tmpl.Scope, ScopeID: tmpl.ScopeID,
		ContentHash: tmpl.ContentHash, Authz: templateResource(tmpl)}
	if row.RuntimeBrokerID != "" {
		d.storedAllowed, _ = d.srv.brokerMayReadForDispatch(ctx, NewBrokerIdentity(row.RuntimeBrokerID), res, agent.ID)
	}
	d.otherAllowed, _ = d.srv.brokerMayReadForDispatch(ctx, NewBrokerIdentity(d.otherBrokerID), res, agent.ID)
	return d.startErr
}

// racingFillStore lands a competing broker fill just before the handler's
// own SetAgentRuntimeBrokerIfEmpty, once its fault switch is armed.
type racingFillStore struct {
	store.Store
	fault     *storeFaultSwitch
	competing string
}

func (r *racingFillStore) SetAgentRuntimeBrokerIfEmpty(ctx context.Context, agentID, brokerID string) (*store.Agent, error) {
	if !r.fault.Active() {
		return r.Store.SetAgentRuntimeBrokerIfEmpty(ctx, agentID, brokerID)
	}
	if _, err := r.Store.SetAgentRuntimeBrokerIfEmpty(ctx, agentID, r.competing); err != nil {
		return nil, err
	}
	return r.Store.SetAgentRuntimeBrokerIfEmpty(ctx, agentID, brokerID)
}

type resumeFillFixture struct {
	*handleExistingAgentAuthzFixture
	broker *store.RuntimeBroker // the project's default broker
	other  *store.RuntimeBroker
	tmpl   *store.Template
	disp   *fillProbeDispatcher
}

func setupResumeFill(t *testing.T) *resumeFillFixture {
	t.Helper()
	ctx := context.Background()
	f := &resumeFillFixture{handleExistingAgentAuthzFixture: handleExistingAgentAuthzSetup(t)}
	var err error
	f.broker, err = f.store.GetRuntimeBroker(ctx, f.project.DefaultRuntimeBrokerID)
	require.NoError(t, err)
	f.other = &store.RuntimeBroker{
		ID: tid("fill-other-broker"), Name: "fill-other", Slug: "fill-other",
		Status: store.BrokerStatusOnline, Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, f.store.CreateRuntimeBroker(ctx, f.other))
	f.tmpl = &store.Template{
		ID: tid("fill-user-tmpl"), Slug: "fill-user", Name: "fill-user",
		Scope: store.TemplateScopeUser, ScopeID: f.owner.ID, OwnerID: f.owner.ID,
		Status: store.TemplateStatusActive, Harness: "claude", ContentHash: dispatchReadHash,
		Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, f.store.CreateTemplate(ctx, f.tmpl))
	f.disp = &fillProbeDispatcher{srv: f.srv, tmpl: f.tmpl, otherBrokerID: f.other.ID}
	f.srv.SetDispatcher(f.disp)
	return f
}

// userTemplateAgent is the owner's agent at phase, bound to the user
// template, with the given stored broker.
func (f *resumeFillFixture) userTemplateAgent(t *testing.T, name, phase, brokerID string) *store.Agent {
	t.Helper()
	a := f.agent(t, name, phase)
	a.RuntimeBrokerID = brokerID
	a.AppliedConfig.TemplateID = f.tmpl.ID
	a.AppliedConfig.TemplateHash = f.tmpl.ContentHash
	require.NoError(t, f.store.UpdateAgent(context.Background(), a))
	return a
}

func (f *resumeFillFixture) post(t *testing.T, a *store.Agent, resume bool) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]interface{}{"name": a.Slug, "projectId": f.project.ID}
	if resume {
		body["resume"] = true
	}
	return doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents", body)
}

// resumeFillBranches are the three branches of handleExistingAgent that
// fill an empty broker: resume of a suspended agent, resume in place of a
// stopped agent, and start of a created agent.
var resumeFillBranches = []struct {
	name   string
	phase  string
	resume bool
}{
	{"suspended resume", string(state.PhaseSuspended), false},
	{"resume in place", string(state.PhaseStopped), true},
	{"created start", string(state.PhaseCreated), false},
}

// The row holds the broker before dispatch, so that broker passes the
// dispatch-bound read during the start and any other broker does not; the
// post-start write neither conflicts with nor reverts the fill.
func TestResumeFill_PersistsBrokerBeforeDispatch(t *testing.T) {
	for _, br := range resumeFillBranches {
		t.Run(br.name, func(t *testing.T) {
			f := setupResumeFill(t)
			a := f.userTemplateAgent(t, "fill-ok-"+tidSlugSafe(br.name), br.phase, "")

			rec := f.post(t, a, br.resume)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, 1, f.disp.starts)
			assert.Equal(t, f.broker.ID, f.disp.storedBroker, "the broker must be persisted before dispatch")
			assert.True(t, f.disp.storedAllowed, "the resume's hydration must pass the dispatch-bound read")
			assert.False(t, f.disp.otherAllowed, "any other broker must stay denied")

			got, err := f.store.GetAgent(context.Background(), a.ID)
			require.NoError(t, err)
			assert.Equal(t, f.broker.ID, got.RuntimeBrokerID, "the post-start write must not revert the fill")
			assert.Equal(t, string(state.PhaseRunning), got.Phase, "the post-start write must not conflict")
		})
	}
}

// A fill lost to a concurrent placement on another broker is a 409 with
// retry and nothing is dispatched; the other broker stays stored.
func TestResumeFill_LostCompareAndSet_409NoDispatch(t *testing.T) {
	for _, br := range resumeFillBranches {
		t.Run(br.name, func(t *testing.T) {
			f := setupResumeFill(t)
			a := f.userTemplateAgent(t, "fill-race-"+tidSlugSafe(br.name), br.phase, "")
			_, fault := installStoreFault(t, f.srv, func(inner store.Store, fault *storeFaultSwitch) *racingFillStore {
				return &racingFillStore{Store: inner, fault: fault, competing: f.other.ID}
			})
			fault.Arm()

			rec := f.post(t, a, br.resume)
			assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "agent placement changed concurrently; retry")
			assert.Zero(t, f.disp.starts, "a lost fill must not dispatch")
			got, err := f.store.GetAgent(context.Background(), a.ID)
			require.NoError(t, err)
			assert.Equal(t, f.other.ID, got.RuntimeBrokerID)
		})
	}
}

// The same broker already filled by a concurrent request counts as success.
func TestResumeFill_SameBrokerAlreadyFilled_Succeeds(t *testing.T) {
	for _, br := range resumeFillBranches {
		t.Run(br.name, func(t *testing.T) {
			f := setupResumeFill(t)
			a := f.userTemplateAgent(t, "fill-same-"+tidSlugSafe(br.name), br.phase, "")
			_, fault := installStoreFault(t, f.srv, func(inner store.Store, fault *storeFaultSwitch) *racingFillStore {
				return &racingFillStore{Store: inner, fault: fault, competing: f.broker.ID}
			})
			fault.Arm()

			rec := f.post(t, a, br.resume)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, 1, f.disp.starts)
			assert.Equal(t, f.broker.ID, f.disp.storedBroker)
		})
	}
}

// A request refused with "no runtime broker available" persists nothing.
func TestResumeFill_GuardRefusalPersistsNothing(t *testing.T) {
	for _, br := range resumeFillBranches {
		t.Run(br.name, func(t *testing.T) {
			f := setupResumeFill(t)
			a := f.userTemplateAgent(t, "fill-guard-"+tidSlugSafe(br.name), br.phase, "")
			f.srv.SetDispatcher(nil)

			rec := f.post(t, a, br.resume)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			got, err := f.store.GetAgent(context.Background(), a.ID)
			require.NoError(t, err)
			assert.Empty(t, got.RuntimeBrokerID, "a refused request must persist nothing")
		})
	}
}

// A stored broker is never overwritten by the fill.
func TestResumeFill_StoredBrokerNeverOverwritten(t *testing.T) {
	for _, br := range resumeFillBranches {
		t.Run(br.name, func(t *testing.T) {
			f := setupResumeFill(t)
			a := f.userTemplateAgent(t, "fill-kept-"+tidSlugSafe(br.name), br.phase, f.other.ID)

			rec := f.post(t, a, br.resume)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, f.other.ID, f.disp.storedBroker)
			got, err := f.store.GetAgent(context.Background(), a.ID)
			require.NoError(t, err)
			assert.Equal(t, f.other.ID, got.RuntimeBrokerID)
		})
	}
}

// A failed start keeps the fill, as a flat first placement does.
func TestResumeFill_FailedStartKeepsFill(t *testing.T) {
	f := setupResumeFill(t)
	a := f.userTemplateAgent(t, "fill-fail", string(state.PhaseSuspended), "")
	f.disp.startErr = errors.New("broker refused the start")

	rec := f.post(t, a, false)
	assert.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err := f.store.GetAgent(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, f.broker.ID, got.RuntimeBrokerID, "a failed start keeps the fill")
}

// ============================================================================
// ptone/scion#5770: reincarnate and move keep a user-scoped
// TemplateID that still exists, authorized for the agent's creator, and
// refuse (409 template_unavailable) rather than silently swap it for a
// same-named project or global template. Global and project TemplateIDs,
// and IDs that no longer resolve, keep the slug path.
// ============================================================================

const (
	reincUserHash   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reincGlobalHash = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// reincUserTemplates creates a user-scoped template owned by the
// reincarnate fixture's creator and a global template with the same slug.
func reincUserTemplates(t *testing.T, s store.Store) (user, twin *store.Template) {
	t.Helper()
	ctx := context.Background()
	creator := tid("user-creator")
	slug := "reinc-shared-" + tidSlugSafe(t.Name())
	user = &store.Template{
		ID: tid("reinc-user-tmpl-" + t.Name()), Name: slug, Slug: slug, Harness: "claude",
		Scope: store.TemplateScopeUser, ScopeID: creator, OwnerID: creator,
		Status: store.TemplateStatusActive, ContentHash: reincUserHash,
	}
	require.NoError(t, s.CreateTemplate(ctx, user))
	twin = &store.Template{
		ID: tid("reinc-global-twin-" + t.Name()), Name: slug, Slug: slug, Harness: "claude",
		Scope: store.TemplateScopeGlobal, Status: store.TemplateStatusActive, ContentHash: reincGlobalHash,
	}
	require.NoError(t, s.CreateTemplate(ctx, twin))
	return user, twin
}

// transferTemplate makes user-scoped template id belong to owner, so its
// previous owner can no longer read it.
func transferTemplate(t *testing.T, st store.Store, id, owner string) {
	t.Helper()
	tmpl, err := st.GetTemplate(context.Background(), id)
	require.NoError(t, err)
	tmpl.OwnerID, tmpl.ScopeID = owner, owner
	require.NoError(t, st.UpdateTemplate(context.Background(), tmpl))
}

func bindTemplate(tmpl *store.Template) func(a *store.Agent) {
	return func(a *store.Agent) {
		a.Template = tmpl.Slug
		a.AppliedConfig.TemplateID = tmpl.ID
		a.AppliedConfig.TemplateHash = tmpl.ContentHash
	}
}

func reincDryRun(t *testing.T, srv *Server, agent *store.Agent, identity Identity) *httptest.ResponseRecorder {
	t.Helper()
	req := reincarnateRequest(t, agent.ID, identity, ReincarnateAgentRequest{DryRun: true})
	rec := httptest.NewRecorder()
	srv.handleReincarnateAgent(rec, req, agent.ID)
	return rec
}

func decodePlan(t *testing.T, rec *httptest.ResponseRecorder) ReincarnateAgentResponse {
	t.Helper()
	var resp ReincarnateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return resp
}

// Self-reincarnation of a user-template agent keeps the user template, not
// its same-named global twin: it is authorized as the creator, not as the
// calling agent (whose identity could not read a user template).
func TestReincarnate_UserTemplate_SelfReincarnateKeepsIt(t *testing.T) {
	srv, s, project, broker := setupReincarnateTestServer(t, newReincarnateTestDispatcher())
	user, _ := reincUserTemplates(t, s)
	agent := newReincarnateTestAgent(t, s, project, broker, bindTemplate(user))

	rec := reincDryRun(t, srv, agent, agentIdentityFor(agent.ID, project.ID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	plan := decodePlan(t, rec).Plan
	assert.Equal(t, reincUserHash, plan.Template.New, "the user template must be kept, not swapped for the global twin")

	fresh, _, err := srv.buildFreshAppliedConfig(context.Background(), agent, project, "")
	require.NoError(t, err)
	assert.Equal(t, user.ID, fresh.TemplateID)
	assert.Equal(t, reincUserHash, fresh.TemplateHash)
}

// A real reincarnate persists the kept TemplateID.
func TestReincarnate_UserTemplate_RealRunPersistsTemplateID(t *testing.T) {
	disp := newReincarnateTestDispatcher()
	srv, s, project, broker := setupReincarnateTestServer(t, disp)
	user, _ := reincUserTemplates(t, s)
	agent := newReincarnateTestAgent(t, s, project, broker, bindTemplate(user))

	req := reincarnateRequest(t, agent.ID, agentIdentityFor(agent.ID, project.ID), ReincarnateAgentRequest{})
	rec := httptest.NewRecorder()
	srv.handleReincarnateAgent(rec, req, agent.ID)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	waitForReincarnationSettled(t, s, agent.ID)

	got, err := s.GetAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	require.NotNil(t, got.AppliedConfig)
	assert.Equal(t, user.ID, got.AppliedConfig.TemplateID, "reincarnate keeps the user template")
	assert.Equal(t, reincUserHash, got.AppliedConfig.TemplateHash)
}

// A move keeps the TemplateID too (same builder).
func TestReincarnate_UserTemplate_MoveKeepsTemplateID(t *testing.T) {
	f := setupMoveFixture(t, true, nil)
	user, _ := reincUserTemplates(t, f.s)
	ctx := context.Background()
	a, err := f.s.GetAgent(ctx, f.agent.ID)
	require.NoError(t, err)
	bindTemplate(user)(a)
	require.NoError(t, f.s.UpdateAgent(ctx, a))
	f.agent, err = f.s.GetAgent(ctx, a.ID)
	require.NoError(t, err)

	rec := f.reincarnate(t, ReincarnateAgentRequest{DryRun: true, TargetBroker: f.dst.ID})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, reincUserHash, decodePlan(t, rec).Plan.Template.New, "a move keeps the user template")
}

// The creator can no longer read the template (it moved to another owner): 409
// template_unavailable on a dry run and a real run, the agent unchanged.
func TestReincarnate_UserTemplate_CreatorGone_409(t *testing.T) {
	disp := newReincarnateTestDispatcher()
	srv, s, project, broker := setupReincarnateTestServer(t, disp)
	user, _ := reincUserTemplates(t, s)
	agent := newReincarnateTestAgent(t, s, project, broker, bindTemplate(user))
	ctx := context.Background()
	// The creator can no longer read the template: it now belongs to
	// another user. (Deactivating the creator would instead put the agent
	// on a standing hold, refused earlier.)
	transferTemplate(t, s, user.ID, tid("reinc-other-owner"))
	before, err := s.GetAgent(ctx, agent.ID)
	require.NoError(t, err)

	for _, dry := range []bool{true, false} {
		req := reincarnateRequest(t, agent.ID, agentIdentityFor(agent.ID, project.ID), ReincarnateAgentRequest{DryRun: dry})
		rec := httptest.NewRecorder()
		srv.handleReincarnateAgent(rec, req, agent.ID)
		requireAPIError(t, rec, http.StatusConflict, ErrCodeTemplateUnavailable)
		assert.Contains(t, rec.Body.String(), "its creator can no longer read")
	}
	after, err := s.GetAgent(ctx, agent.ID)
	require.NoError(t, err)
	assert.Equal(t, before.StateVersion, after.StateVersion, "a refused reincarnate writes nothing")
	assert.Equal(t, user.ID, after.AppliedConfig.TemplateID)
	assert.Zero(t, disp.stopCalls)
	assert.Zero(t, disp.startCalls)
}

// A move whose creator lost access answers the same 409.
func TestReincarnate_UserTemplate_MoveCreatorGone_409(t *testing.T) {
	f := setupMoveFixture(t, true, nil)
	user, _ := reincUserTemplates(t, f.s)
	ctx := context.Background()
	a, err := f.s.GetAgent(ctx, f.agent.ID)
	require.NoError(t, err)
	bindTemplate(user)(a)
	require.NoError(t, f.s.UpdateAgent(ctx, a))
	f.agent, err = f.s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	transferTemplate(t, f.s, user.ID, tid("reinc-other-owner"))

	rec := f.reincarnate(t, ReincarnateAgentRequest{DryRun: true, TargetBroker: f.dst.ID})
	requireAPIError(t, rec, http.StatusConflict, ErrCodeTemplateUnavailable)
	assert.Contains(t, rec.Body.String(), "its creator can no longer read")
}

// The user template changed since the agent was dispatched: a reincarnate
// never re-pins, it refuses with 409.
func TestReincarnate_UserTemplate_ChangedSinceDispatch_409(t *testing.T) {
	srv, s, project, broker := setupReincarnateTestServer(t, newReincarnateTestDispatcher())
	user, _ := reincUserTemplates(t, s)
	agent := newReincarnateTestAgent(t, s, project, broker, func(a *store.Agent) {
		bindTemplate(user)(a)
		a.AppliedConfig.TemplateHash = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	})

	rec := reincDryRun(t, srv, agent, agentIdentityFor(agent.ID, project.ID))
	requireAPIError(t, rec, http.StatusConflict, ErrCodeTemplateUnavailable)
	assert.Contains(t, rec.Body.String(), "changed since the agent was dispatched; reincarnate cannot keep it: re-create the agent, or restore the template to the version the agent was dispatched with and reincarnate again")
}

// Regression guard: a global-template agent whose creator agent is
// deleted still reincarnates exactly as on main (the creator gate applies to
// user scope only).
func TestReincarnate_GlobalTemplate_CreatorAgentDeleted_Unchanged(t *testing.T) {
	srv, s, project, broker := setupReincarnateTestServer(t, newReincarnateTestDispatcher())
	_, twin := reincUserTemplates(t, s)
	ctx := context.Background()
	parent := newReincarnateTestAgent(t, s, project, broker, func(a *store.Agent) {
		a.ID = tid("reinc-parent-" + t.Name())
		a.Slug = "reinc-parent-" + tidSlugSafe(t.Name())
	})
	agent := newReincarnateTestAgent(t, s, project, broker, func(a *store.Agent) {
		bindTemplate(twin)(a)
		a.CreatedBy = parent.ID
	})
	parent.DeletedAt = time.Now()
	require.NoError(t, s.UpdateAgent(ctx, parent))
	require.Nil(t, srv.creatorIdentityForAgent(ctx, agent), "fixture: the creator is gone")

	fresh, _, err := srv.buildFreshAppliedConfig(ctx, agent, project, "")
	require.NoError(t, err)
	assert.Equal(t, twin.ID, fresh.TemplateID, "a global template with a gone creator reincarnates as on main")
}

// Regression guard: a project-template agent whose creator user is
// deactivated still reincarnates as on main.
func TestReincarnate_ProjectTemplate_CreatorDeactivated_Unchanged(t *testing.T) {
	srv, s, project, broker := setupReincarnateTestServer(t, newReincarnateTestDispatcher())
	ctx := context.Background()
	projTmpl := &store.Template{
		ID: tid("reinc-proj-tmpl-" + t.Name()), Name: "reinc-proj", Slug: "reinc-proj-" + tidSlugSafe(t.Name()),
		Harness: "claude", Scope: store.TemplateScopeProject, ScopeID: project.ID,
		Status: store.TemplateStatusActive, ContentHash: reincGlobalHash,
	}
	require.NoError(t, s.CreateTemplate(ctx, projTmpl))
	agent := newReincarnateTestAgent(t, s, project, broker, bindTemplate(projTmpl))
	creator, err := s.GetUser(ctx, tid("user-creator"))
	require.NoError(t, err)
	creator.Status = "suspended"
	require.NoError(t, s.UpdateUser(ctx, creator))

	fresh, _, err := srv.buildFreshAppliedConfig(ctx, agent, project, "")
	require.NoError(t, err)
	assert.Equal(t, projTmpl.ID, fresh.TemplateID)
}

// Once the owner deletes their user template, the recorded ID no
// longer resolves and main's slug path runs: here it picks the same-named
// global template. Pinned on purpose (ptone/scion#5776 tracks the slug
// path).
func TestReincarnate_DeletedUserTemplate_FollowsSlugPath(t *testing.T) {
	srv, s, project, broker := setupReincarnateTestServer(t, newReincarnateTestDispatcher())
	user, twin := reincUserTemplates(t, s)
	agent := newReincarnateTestAgent(t, s, project, broker, bindTemplate(user))
	require.NoError(t, s.DeleteTemplate(context.Background(), user.ID))

	fresh, _, err := srv.buildFreshAppliedConfig(context.Background(), agent, project, "")
	require.NoError(t, err)
	assert.Equal(t, twin.ID, fresh.TemplateID, "a deleted user template follows the slug path")
}

// An agent with no TemplateID (legacy row) keeps the slug path.
func TestReincarnate_NoTemplateID_SlugPathUnchanged(t *testing.T) {
	srv, s, project, broker := setupReincarnateTestServer(t, newReincarnateTestDispatcher())
	_, twin := reincUserTemplates(t, s)
	agent := newReincarnateTestAgent(t, s, project, broker, func(a *store.Agent) { a.Template = twin.Slug })

	fresh, _, err := srv.buildFreshAppliedConfig(context.Background(), agent, project, "")
	require.NoError(t, err)
	assert.Equal(t, twin.ID, fresh.TemplateID)
}

// templateLookupErrStore fails GetTemplate for one ID with a non-NotFound
// error.
type templateLookupErrStore struct {
	store.Store
	fault  *storeFaultSwitch
	failID string
}

var errInjectedTemplateLookup = errors.New("injected template lookup failure")

func (s *templateLookupErrStore) GetTemplate(ctx context.Context, id string) (*store.Template, error) {
	if s.fault.Active() && id == s.failID {
		return nil, errInjectedTemplateLookup
	}
	return s.Store.GetTemplate(ctx, id)
}

// A store error other than NotFound fails the rebuild: no swap onto
// the global twin, nothing written, nothing dispatched.
func TestReincarnate_TemplateStoreError_FailsWithoutSwap(t *testing.T) {
	disp := newReincarnateTestDispatcher()
	srv, s, project, broker := setupReincarnateTestServer(t, disp)
	user, _ := reincUserTemplates(t, s)
	agent := newReincarnateTestAgent(t, s, project, broker, bindTemplate(user))
	before, err := s.GetAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	_, fault := installStoreFault(t, srv, func(inner store.Store, fault *storeFaultSwitch) *templateLookupErrStore {
		return &templateLookupErrStore{Store: inner, fault: fault, failID: user.ID}
	})
	fault.Arm()

	req := reincarnateRequest(t, agent.ID, agentIdentityFor(agent.ID, project.ID), ReincarnateAgentRequest{})
	rec := httptest.NewRecorder()
	srv.handleReincarnateAgent(rec, req, agent.ID)
	requireAPIError(t, rec, http.StatusInternalServerError, ErrCodeInternalError)

	after, err := s.GetAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	assert.Equal(t, before.StateVersion, after.StateVersion, "no AppliedConfig is written")
	assert.Equal(t, user.ID, after.AppliedConfig.TemplateID)
	assert.Zero(t, disp.stopCalls)
	assert.Zero(t, disp.startCalls)
	assert.Zero(t, disp.reprovisionCalls)
}

// ptone/scion#5770: the dispatchBoundCatalogRead capability, and the
// 412 for a user-scoped template on a broker known to lack it.

func TestCapabilitiesFromStrings_DispatchBoundCatalogRead(t *testing.T) {
	for _, name := range []string{"dispatchBoundCatalogRead", "dispatch_bound_catalog_read"} {
		assert.True(t, capabilitiesFromStrings([]string{name}).DispatchBoundCatalogRead, name)
	}
	assert.False(t, capabilitiesFromStrings([]string{"sync"}).DispatchBoundCatalogRead)
}

func TestCheckDispatchBoundReadCapability(t *testing.T) {
	user := &store.Template{Slug: "mine", Scope: store.TemplateScopeUser}
	lacking := &store.RuntimeBroker{ID: "b1", Name: "old-broker", Capabilities: &store.BrokerCapabilities{Sync: true}}
	having := &store.RuntimeBroker{ID: "b2", Capabilities: &store.BrokerCapabilities{DispatchBoundCatalogRead: true}}
	unknown := &store.RuntimeBroker{ID: "b3"}

	err := checkDispatchBoundReadCapability(user, lacking)
	require.ErrorIs(t, err, errBrokerLacksDispatchBoundRead, "known to lack: refused")
	assert.Contains(t, err.Error(), "user-scoped template mine cannot be used on Runtime Broker old-broker")
	assert.NoError(t, checkDispatchBoundReadCapability(user, having))
	assert.NoError(t, checkDispatchBoundReadCapability(user, unknown), "nil capabilities: tried")
	for _, scope := range []string{store.TemplateScopeGlobal, store.TemplateScopeProject} {
		assert.NoError(t, checkDispatchBoundReadCapability(&store.Template{Slug: "x", Scope: scope}, lacking), "%s templates are never refused", scope)
	}
	assert.NoError(t, checkDispatchBoundReadCapability(nil, lacking))
}

type capabilityGateFixture struct {
	*handleExistingAgentAuthzFixture
	broker   *store.RuntimeBroker
	userTmpl *store.Template
	global   *store.Template
}

func setupCapabilityGate(t *testing.T, caps *store.BrokerCapabilities) *capabilityGateFixture {
	t.Helper()
	ctx := context.Background()
	f := &capabilityGateFixture{handleExistingAgentAuthzFixture: handleExistingAgentAuthzSetup(t)}
	f.srv.SetDispatcher(&createAgentDispatcher{})
	var err error
	f.broker, err = f.store.GetRuntimeBroker(ctx, f.project.DefaultRuntimeBrokerID)
	require.NoError(t, err)
	f.broker.Capabilities = caps
	require.NoError(t, f.store.UpdateRuntimeBroker(ctx, f.broker))
	f.userTmpl = &store.Template{
		ID: tid("capgate-user-tmpl"), Slug: "capgate-user", Name: "capgate-user", Harness: "claude",
		Scope: store.TemplateScopeUser, ScopeID: f.owner.ID, OwnerID: f.owner.ID,
		Status: store.TemplateStatusActive, ContentHash: dispatchReadHash, Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, f.store.CreateTemplate(ctx, f.userTmpl))
	f.global = &store.Template{
		ID: tid("capgate-global-tmpl"), Slug: "capgate-global", Name: "capgate-global", Harness: "claude",
		Scope: store.TemplateScopeGlobal, Status: store.TemplateStatusActive, ContentHash: dispatchReadHash,
		Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, f.store.CreateTemplate(ctx, f.global))
	return f
}

func (f *capabilityGateFixture) create(t *testing.T, name, templateID string) int {
	t.Helper()
	rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents", map[string]interface{}{
		"name": name, "projectId": f.project.ID, "template": templateID,
	})
	if rec.Code == http.StatusPreconditionFailed {
		requireAPIError(t, rec, http.StatusPreconditionFailed, ErrCodeUnsupportedCapability)
		assert.Contains(t, rec.Body.String(), "does not support user-scoped templates")
	}
	return rec.Code
}

// A create with a user-scoped template on a broker known to lack the
// capability is refused with 412 before anything is persisted.
func TestDispatchBoundCapability_CreateKnownToLack_412(t *testing.T) {
	f := setupCapabilityGate(t, &store.BrokerCapabilities{Sync: true})
	assert.Equal(t, http.StatusPreconditionFailed, f.create(t, "capgate-refused", f.userTmpl.ID))
	_, err := f.store.GetAgentBySlug(context.Background(), f.project.ID, "capgate-refused")
	assert.ErrorIs(t, err, store.ErrNotFound, "a refused create persists nothing")
}

// Global templates are never refused; a broker with no recorded
// capabilities, or one that has the flag, is not refused.
func TestDispatchBoundCapability_CreateNotRefused(t *testing.T) {
	t.Run("global template on a lacking broker", func(t *testing.T) {
		f := setupCapabilityGate(t, &store.BrokerCapabilities{Sync: true})
		assert.NotEqual(t, http.StatusPreconditionFailed, f.create(t, "capgate-global", f.global.ID))
	})
	t.Run("nil capabilities", func(t *testing.T) {
		f := setupCapabilityGate(t, nil)
		assert.NotEqual(t, http.StatusPreconditionFailed, f.create(t, "capgate-nil", f.userTmpl.ID))
	})
	t.Run("broker has the capability", func(t *testing.T) {
		f := setupCapabilityGate(t, &store.BrokerCapabilities{Sync: true, DispatchBoundCatalogRead: true})
		assert.NotEqual(t, http.StatusPreconditionFailed, f.create(t, "capgate-has", f.userTmpl.ID))
	})
}

// A start, and a resume through create, of an agent recorded with a
// user-scoped template on a lacking broker are refused with 412, and
// nothing is dispatched.
func TestDispatchBoundCapability_StartAndResumeKnownToLack_412(t *testing.T) {
	ctx := context.Background()
	t.Run("lifecycle start", func(t *testing.T) {
		f := setupCapabilityGate(t, &store.BrokerCapabilities{Sync: true})
		a := f.agent(t, "capgate-start", string(state.PhaseStopped))
		a.RuntimeBrokerID = f.broker.ID
		a.AppliedConfig.TemplateID, a.AppliedConfig.TemplateHash = f.userTmpl.ID, f.userTmpl.ContentHash
		require.NoError(t, f.store.UpdateAgent(ctx, a))
		disp := &createAgentDispatcher{}
		f.srv.SetDispatcher(disp)

		rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
		requireAPIError(t, rec, http.StatusPreconditionFailed, ErrCodeUnsupportedCapability)
		assert.False(t, disp.startCalled)
	})
	t.Run("resume through create", func(t *testing.T) {
		f := setupCapabilityGate(t, &store.BrokerCapabilities{Sync: true})
		a := f.agent(t, "capgate-resume", string(state.PhaseSuspended))
		a.AppliedConfig.TemplateID, a.AppliedConfig.TemplateHash = f.userTmpl.ID, f.userTmpl.ContentHash
		require.NoError(t, f.store.UpdateAgent(ctx, a))
		disp := &createAgentDispatcher{}
		f.srv.SetDispatcher(disp)

		rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents", map[string]interface{}{
			"name": a.Slug, "projectId": f.project.ID,
		})
		requireAPIError(t, rec, http.StatusPreconditionFailed, ErrCodeUnsupportedCapability)
		assert.False(t, disp.startCalled)
		got, err := f.store.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Empty(t, got.RuntimeBrokerID, "a 412 refusal persists no broker fill")
	})
}

// ============================================================================
// Start, restart and resume follow the current version of the SAME user
// template, re-recorded before the dispatch while the creator can still
// read it; each launch then stays pinned to that hash (ptone/scion#5770).
// ============================================================================

const repinNewHash = "sha256:4444444444444444444444444444444444444444444444444444444444444444"

// editTemplate commits a new content hash to the fixture's user template.
func (f *resumeFillFixture) editTemplate(t *testing.T, hash string) {
	t.Helper()
	commitTemplateContent(t, f.store, f.tmpl.ID, func(tm *store.Template) { tm.ContentHash = hash })
}

// repinPaths are the three ways an existing agent is started.
var repinPaths = []struct {
	name  string
	phase string
	run   func(t *testing.T, f *resumeFillFixture, a *store.Agent) *httptest.ResponseRecorder
}{
	{"lifecycle start", string(state.PhaseStopped), func(t *testing.T, f *resumeFillFixture, a *store.Agent) *httptest.ResponseRecorder {
		return doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
	}},
	{"lifecycle restart", string(state.PhaseRunning), func(t *testing.T, f *resumeFillFixture, a *store.Agent) *httptest.ResponseRecorder {
		return doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/restart", nil)
	}},
	{"resume through create", string(state.PhaseSuspended), func(t *testing.T, f *resumeFillFixture, a *store.Agent) *httptest.ResponseRecorder {
		return f.post(t, a, false)
	}},
}

// After the user template is edited, a start, restart or resume re-records
// its current hash before the dispatch: the dispatched agent and the row
// carry it, so the broker running the agent reads the edited template.
func TestRepin_StartRestartResumeFollowCurrentVersion(t *testing.T) {
	for _, p := range repinPaths {
		t.Run(p.name, func(t *testing.T) {
			f := setupResumeFill(t)
			a := f.userTemplateAgent(t, "repin-"+tidSlugSafe(p.name), p.phase, f.broker.ID)
			f.editTemplate(t, repinNewHash)

			rec := p.run(t, f, a)
			require.Less(t, rec.Code, 300, rec.Body.String())
			require.Equal(t, 1, f.disp.starts)
			assert.Equal(t, repinNewHash, f.disp.storedHash, "the current hash is recorded before the dispatch")
			assert.Equal(t, repinNewHash, f.disp.dispatchedHash, "the dispatch carries the current hash")
			assert.True(t, f.disp.storedAllowed, "the broker running the agent reads the current version")
			got, err := f.store.GetAgent(context.Background(), a.ID)
			require.NoError(t, err)
			assert.Equal(t, repinNewHash, got.AppliedConfig.TemplateHash, "later writes must not revert the re-recorded hash")
		})
	}
}

// Within a launch the read stays pinned: a commit after the re-record makes
// the gate refuse that launch's reads.
func TestRepin_LaunchStaysPinned(t *testing.T) {
	f := setupResumeFill(t)
	a := f.userTemplateAgent(t, "repin-pinned", string(state.PhaseStopped), f.broker.ID)
	f.editTemplate(t, repinNewHash)
	rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
	require.Less(t, rec.Code, 300, rec.Body.String())
	require.True(t, f.disp.storedAllowed)

	f.editTemplate(t, "sha256:5555555555555555555555555555555555555555555555555555555555555555")
	tmpl, err := f.store.GetTemplate(context.Background(), f.tmpl.ID)
	require.NoError(t, err)
	allowed, reason := f.srv.brokerMayReadForDispatch(context.Background(), NewBrokerIdentity(f.broker.ID), dispatchCatalogResource{
		Kind: catalogKindTemplate, ID: tmpl.ID, Scope: tmpl.Scope, ScopeID: tmpl.ScopeID, ContentHash: tmpl.ContentHash, Authz: templateResource(tmpl),
	}, a.ID)
	assert.False(t, allowed, "a commit during the launch is refused")
	assert.Equal(t, dispatchDenyVersionMismatch, reason)
}

// A creator who can no longer read the template blocks the re-record: the
// old hash stays, so the broker's read is refused.
func TestRepin_CreatorLostAccess_NotRepinned(t *testing.T) {
	f := setupResumeFill(t)
	a := f.userTemplateAgent(t, "repin-creator-gone", string(state.PhaseStopped), f.broker.ID)
	ctx := context.Background()
	// The template now belongs to another user: the agent's creator can no
	// longer read it.
	f.editTemplate(t, repinNewHash)
	tmpl, err := f.store.GetTemplate(ctx, f.tmpl.ID)
	require.NoError(t, err)
	tmpl.OwnerID, tmpl.ScopeID = f.member.ID, f.member.ID
	require.NoError(t, f.store.UpdateTemplate(ctx, tmpl))

	rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
	require.Less(t, rec.Code, 300, rec.Body.String())
	require.Equal(t, 1, f.disp.starts)
	assert.Equal(t, dispatchReadHash, f.disp.storedHash, "no re-record without the creator's access")
	assert.False(t, f.disp.storedAllowed, "the broker's read is refused")
}

// Global templates are never re-recorded (their reads follow the current
// version through the catalog rule); an unchanged user template writes
// nothing.
func TestRepin_NothingToRepin_NoWrite(t *testing.T) {
	ctx := context.Background()
	t.Run("unchanged user template", func(t *testing.T) {
		f := setupResumeFill(t)
		a := f.userTemplateAgent(t, "repin-unchanged", string(state.PhaseStopped), f.broker.ID)
		before, err := f.store.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		require.NoError(t, f.srv.repinUserTemplateForStart(ctx, before))
		after, err := f.store.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, before.StateVersion, after.StateVersion)
	})
	t.Run("global template", func(t *testing.T) {
		f := setupResumeFill(t)
		global := &store.Template{ID: tid("repin-global"), Slug: "repin-global", Name: "repin-global", Harness: "claude",
			Scope: store.TemplateScopeGlobal, Status: store.TemplateStatusActive, ContentHash: repinNewHash}
		require.NoError(t, f.store.CreateTemplate(ctx, global))
		a := f.agent(t, "repin-global-agent", string(state.PhaseStopped))
		a.RuntimeBrokerID = f.broker.ID
		a.AppliedConfig.TemplateID, a.AppliedConfig.TemplateHash = global.ID, dispatchReadHash
		require.NoError(t, f.store.UpdateAgent(ctx, a))
		before, err := f.store.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		require.NoError(t, f.srv.repinUserTemplateForStart(ctx, before))
		after, err := f.store.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, before.StateVersion, after.StateVersion, "a global template is never re-recorded")
		assert.Equal(t, dispatchReadHash, after.AppliedConfig.TemplateHash)
	})
}

// A concurrent write to the agent between the handler's read and the
// re-pin makes the re-pin's compare-and-set fail: the start, restart or
// resume answers 409 template_pin_conflict, nothing is dispatched (a restart
// does not even stop the agent), and the concurrent write survives.
func TestRepin_ConcurrentWrite_409NoDispatch(t *testing.T) {
	for _, p := range repinPaths {
		t.Run(p.name, func(t *testing.T) {
			f := setupResumeFill(t)
			a := f.userTemplateAgent(t, "repin-race-"+tidSlugSafe(p.name), p.phase, f.broker.ID)
			f.editTemplate(t, repinNewHash)
			racing, fault := installStoreFault(t, f.srv, func(inner store.Store, fault *storeFaultSwitch) *racingPinWriteStore {
				return &racingPinWriteStore{Store: inner, fault: fault}
			})
			fault.Arm()

			rec := p.run(t, f, a)
			requireAPIError(t, rec, http.StatusConflict, ErrCodeTemplatePinConflict)
			assert.Zero(t, f.disp.starts, "nothing is dispatched")
			assert.Zero(t, f.disp.stops, "a restart does not stop the agent")
			assert.True(t, racing.raced, "the concurrent write ran")

			got, err := f.store.GetAgent(context.Background(), a.ID)
			require.NoError(t, err)
			assert.Equal(t, "concurrent write", got.Message, "the concurrent write is not reverted")
			assert.Equal(t, dispatchReadHash, got.AppliedConfig.TemplateHash, "nothing is re-recorded")
			assert.Equal(t, p.phase, got.Phase, "the agent is left as it was")
			assert.Empty(t, got.StartClaimID, "the refused start releases its start claim")

			// The 409 says retry: an immediate retry succeeds (the wrapper
			// races only once).
			rec = p.run(t, f, got)
			require.Less(t, rec.Code, 300, "the retry starts: %s", rec.Body.String())
			assert.Equal(t, 1, f.disp.starts)
			assert.Equal(t, repinNewHash, f.disp.storedHash, "the retry re-records the current hash")
		})
	}
}

// racingPinWriteStore commits another writer's whole-row update just before
// the handler's re-pin, so the row is newer than the handler's read.
type racingPinWriteStore struct {
	store.Store
	fault *storeFaultSwitch
	raced bool
}

func (r *racingPinWriteStore) SetAgentTemplatePin(ctx context.Context, agentID string, expectedVersion int64, templateID, expectedHash, newHash string) (int64, error) {
	if r.fault.Active() && !r.raced {
		r.raced = true
		cur, err := r.Store.GetAgent(ctx, agentID)
		if err != nil {
			return 0, err
		}
		cur.Message = "concurrent write"
		if err := r.Store.UpdateAgent(ctx, cur); err != nil {
			return 0, err
		}
	}
	return r.Store.SetAgentTemplatePin(ctx, agentID, expectedVersion, templateID, expectedHash, newHash)
}

// A start refused by another start's claim never re-pins: the re-pin runs
// only under the start's own claim, so an in-flight launch keeps the hash
// recorded for it.
func TestRepin_RefusedByStartClaim_PinUnchanged(t *testing.T) {
	for _, p := range repinPaths {
		t.Run(p.name, func(t *testing.T) {
			f := setupResumeFill(t)
			require.True(t, f.srv.startClaimsEnabled(), "fixture: start claims are on")
			a := f.userTemplateAgent(t, "repin-claimed-"+tidSlugSafe(p.name), p.phase, f.broker.ID)
			ctx := context.Background()
			// Another hub instance's start is in flight.
			_, err := f.store.ClaimAgentStart(ctx, a.ID, "other-hub-instance", store.StartClaimUser, "", time.Minute)
			require.NoError(t, err)
			f.editTemplate(t, repinNewHash)

			rec := p.run(t, f, a)
			assert.GreaterOrEqual(t, rec.Code, 300, "the start is refused or deferred: %s", rec.Body.String())
			assert.Zero(t, f.disp.starts, "nothing is dispatched")
			assert.Zero(t, f.disp.stops, "a refused restart does not stop the agent")
			got, err := f.store.GetAgent(ctx, a.ID)
			require.NoError(t, err)
			assert.Equal(t, dispatchReadHash, got.AppliedConfig.TemplateHash, "a refused start does not change the pin")
		})
	}
}

// requireWakeReadinessTimeout asserts the wake's answer after a dispatch that
// the fake dispatcher never reports ready: the readiness-timeout error, and
// nothing else.
func requireWakeReadinessTimeout(t *testing.T, dmErr *AgentDMError) {
	t.Helper()
	require.NotNil(t, dmErr, "the fake never reports ready, so the wake ends in the readiness timeout")
	assert.Equal(t, ErrCodeRuntimeError, dmErr.Code)
	assert.Equal(t, http.StatusBadGateway, dmErr.HTTPStatus)
	assert.Contains(t, dmErr.Message, "did not become ready")
}

// A direct-message wake re-pins like a start, and a lost compare-and-set
// answers the sender 409 template_pin_conflict with nothing dispatched.
func TestRepin_Wake(t *testing.T) {
	ctx := context.Background()
	// The fake dispatcher never reports the woken agent ready, so the
	// readiness wait after the dispatch is cut short; these tests look only
	// at what was dispatched.
	prevReady := wakeReadyTimeout
	wakeReadyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { wakeReadyTimeout = prevReady })
	t.Run("follows the current version", func(t *testing.T) {
		f := setupResumeFill(t)
		a := f.userTemplateAgent(t, "repin-wake", string(state.PhaseSuspended), f.broker.ID)
		f.editTemplate(t, repinNewHash)
		cur, err := f.store.GetAgent(ctx, a.ID)
		require.NoError(t, err)

		_, dmErr := f.srv.wakeAgentForDM(ctx, cur)
		requireWakeReadinessTimeout(t, dmErr)
		require.Equal(t, 1, f.disp.starts)
		assert.Equal(t, repinNewHash, f.disp.storedHash)
		assert.Equal(t, repinNewHash, f.disp.dispatchedHash)
	})
	t.Run("lost compare-and-set", func(t *testing.T) {
		f := setupResumeFill(t)
		a := f.userTemplateAgent(t, "repin-wake-race", string(state.PhaseSuspended), f.broker.ID)
		f.editTemplate(t, repinNewHash)
		cur, err := f.store.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		_, fault := installStoreFault(t, f.srv, func(inner store.Store, fault *storeFaultSwitch) *racingPinWriteStore {
			return &racingPinWriteStore{Store: inner, fault: fault}
		})
		fault.Arm()

		_, dmErr := f.srv.wakeAgentForDM(ctx, cur)
		require.NotNil(t, dmErr)
		assert.Equal(t, http.StatusConflict, dmErr.HTTPStatus)
		assert.Equal(t, ErrCodeTemplatePinConflict, dmErr.Code)
		assert.Zero(t, f.disp.starts, "nothing is dispatched")
		got, err := f.store.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, "concurrent write", got.Message)
		assert.Equal(t, dispatchReadHash, got.AppliedConfig.TemplateHash)
		assert.Empty(t, got.StartClaimID, "the refused wake releases its start claim")

		// An immediate retry wakes the agent (not deferred behind a held
		// claim; the wrapper races only once).
		_, dmErr = f.srv.wakeAgentForDM(ctx, got)
		// Dispatched, then only the (fake's) readiness wait fails: a
		// deferred wake would answer no error and dispatch nothing.
		requireWakeReadinessTimeout(t, dmErr)
		assert.Equal(t, 1, f.disp.starts, "the retry is dispatched")
		assert.Equal(t, repinNewHash, f.disp.storedHash)
	})
}

// "Active" means not soft-deleted, in any phase: a stopped or errored agent's
// broker reads, a soft-deleted agent's does not.
func TestDispatchBoundRead_AnyPhaseReads_SoftDeletedRefused(t *testing.T) {
	f := setupDispatchReadTest(t)
	ctx := context.Background()
	for _, phase := range []string{string(state.PhaseStopped), string(state.PhaseError), string(state.PhaseCreated), string(state.PhaseSuspended)} {
		a := f.boundAgent(t, "dbr-phase-"+phase, f.broker.ID, f.userTmpl.ID, dispatchReadHash, f.creator.ID)
		a.Phase = phase
		require.NoError(t, f.store.UpdateAgent(ctx, a))
		assert.Equal(t, http.StatusOK, f.asBroker(t, http.MethodGet, templateGetFor(f.userTmpl.ID, a.ID)).Code, "phase %s reads", phase)
	}
	gone := f.boundAgent(t, "dbr-phase-deleted", f.broker.ID, f.userTmpl.ID, dispatchReadHash, f.creator.ID)
	gone.Phase = string(state.PhaseStopped)
	gone.DeletedAt = time.Now()
	require.NoError(t, f.store.UpdateAgent(ctx, gone))
	assert.Equal(t, http.StatusNotFound, f.asBroker(t, http.MethodGet, templateGetFor(f.userTmpl.ID, gone.ID)).Code)
}

// ============================================================================
// The re-pin and the held-edit fold at start (ptone/scion#5770, after
// upstream ptone/scion#3976): under the start claim the fold runs
// first, then the re-pin compares and sets on the state version the fold
// refreshed. Every test here turns held edits on.
// ============================================================================

const heldModel = "held-model"

// holdModelEdit stores a pending (held) edit of config.model on the agent
// and returns the agent row as stored.
func (f *resumeFillFixture) holdModelEdit(t *testing.T, a *store.Agent) *store.Agent {
	t.Helper()
	ctx := context.Background()
	cur, err := f.store.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	pending := &store.AgentPendingConfig{
		Config:           map[string]json.RawMessage{"model": json.RawMessage(strconv.Quote(heldModel))},
		SavedBy:          f.owner.ID,
		SavedAt:          time.Now(),
		BaseStateVersion: cur.StateVersion,
	}
	_, err = f.store.UpdateAgentConfig(ctx, cur.ID, cur.StateVersion, cur.AppliedConfig, pending)
	require.NoError(t, err)
	got, err := f.store.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.PendingConfig.IsEmpty(), "fixture: the edit is held")
	return got
}

// requireFolded asserts the held model edit was applied to the row and
// removed from its pending config.
func requireFolded(t *testing.T, got *store.Agent) {
	t.Helper()
	require.NotNil(t, got.AppliedConfig)
	require.NotNil(t, got.AppliedConfig.InlineConfig)
	assert.Equal(t, heldModel, got.AppliedConfig.InlineConfig.Model, "the held edit is applied")
	if got.PendingConfig != nil {
		assert.NotContains(t, got.PendingConfig.Config, "model", "the applied key leaves the pending config")
	}
}

// A held edit and a template edit, then a start: the fold applies the held
// edit and the re-pin records the current hash on the version the fold
// refreshed; the dispatch carries both.
func TestRepin_AfterHeldFold_PinsAndDispatchesFolded(t *testing.T) {
	f := setupResumeFill(t)
	setHeldEditsExperiment(t, f.srv, true)
	a := f.userTemplateAgent(t, "repin-fold", string(state.PhaseStopped), f.broker.ID)
	held := f.holdModelEdit(t, a)
	f.editTemplate(t, repinNewHash)

	rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
	require.Less(t, rec.Code, 300, rec.Body.String())
	require.Equal(t, 1, f.disp.starts)
	assert.Equal(t, held.StateVersion+2, f.disp.storedVersion, "the fold, then the re-pin, each move the row once before the dispatch")
	assert.Equal(t, repinNewHash, f.disp.storedHash)
	assert.Equal(t, repinNewHash, f.disp.dispatchedHash)
	assert.Equal(t, heldModel, f.disp.storedModel)
	assert.Equal(t, heldModel, f.disp.dispatchedModel, "the dispatch carries the folded edit")
	got, err := f.store.GetAgent(context.Background(), a.ID)
	require.NoError(t, err)
	requireFolded(t, got)
	assert.Equal(t, repinNewHash, got.AppliedConfig.TemplateHash)
}

// The same with a synchronous restart: the restart re-pins before its stop
// leg, its start leg folds, and the start leg's own re-pin finds the hash
// current and writes nothing.
func TestRepin_RestartWithHeldFold(t *testing.T) {
	f := setupResumeFill(t)
	setHeldEditsExperiment(t, f.srv, true)
	a := f.userTemplateAgent(t, "repin-fold-restart", string(state.PhaseRunning), f.broker.ID)
	held := f.holdModelEdit(t, a)
	f.editTemplate(t, repinNewHash)

	rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/restart", nil)
	require.Less(t, rec.Code, 300, rec.Body.String())
	require.Equal(t, 1, f.disp.stops)
	require.Equal(t, 1, f.disp.starts)
	assert.Equal(t, held.StateVersion+2, f.disp.storedVersion, "the pre-stop re-pin and the fold each move the row once; the start leg's re-pin writes nothing")
	assert.Equal(t, repinNewHash, f.disp.storedHash)
	assert.Equal(t, repinNewHash, f.disp.dispatchedHash)
	assert.Equal(t, heldModel, f.disp.dispatchedModel)
	got, err := f.store.GetAgent(context.Background(), a.ID)
	require.NoError(t, err)
	requireFolded(t, got)
	assert.Equal(t, repinNewHash, got.AppliedConfig.TemplateHash)
}

// The template has no PATCH surface: a PATCH naming it, at the top level or
// inside config, changes neither the template identity nor the pending
// config, so no held edit can carry a template change.
func TestHeldEdit_TemplateHasNoPatchSurface(t *testing.T) {
	f := setupResumeFill(t)
	setHeldEditsExperiment(t, f.srv, true)
	a := f.userTemplateAgent(t, "repin-tmpl-patch", string(state.PhaseRunning), f.broker.ID)
	body := map[string]interface{}{
		"template":     "other-template",
		"config":       map[string]interface{}{"template": "other-template"},
		"stateVersion": a.StateVersion,
	}
	rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPatch, "/api/v1/agents/"+a.ID, body)
	// The status upstream returns for this body (captured from a run; no
	// refusal is asserted).
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err := f.store.GetAgent(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, f.tmpl.ID, got.AppliedConfig.TemplateID, "the template identity is unchanged")
	assert.Equal(t, dispatchReadHash, got.AppliedConfig.TemplateHash, "the template hash is unchanged")
	if got.PendingConfig != nil {
		assert.NotContains(t, got.PendingConfig.Config, "template", "no template key is held")
	}
}

// A start of a running agent (not a restart) keeps its container, so its
// held edits stay held, but it still hydrates the template for its env
// fallback: the re-pin runs, so the broker's read with the current hash is
// allowed.
func TestRepin_RunningStart_NoFold_StillRepins(t *testing.T) {
	t.Run("precondition: a container-creating start folds", func(t *testing.T) {
		f := setupResumeFill(t)
		setHeldEditsExperiment(t, f.srv, true)
		a := f.userTemplateAgent(t, "repin-nofold-pre", string(state.PhaseStopped), f.broker.ID)
		f.holdModelEdit(t, a)
		rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
		require.Less(t, rec.Code, 300, rec.Body.String())
		got, err := f.store.GetAgent(context.Background(), a.ID)
		require.NoError(t, err)
		requireFolded(t, got)
	})
	t.Run("running start", func(t *testing.T) {
		f := setupResumeFill(t)
		setHeldEditsExperiment(t, f.srv, true)
		a := f.userTemplateAgent(t, "repin-nofold", string(state.PhaseRunning), f.broker.ID)
		f.holdModelEdit(t, a)
		f.editTemplate(t, repinNewHash)

		rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
		require.Less(t, rec.Code, 300, rec.Body.String())
		require.Equal(t, 1, f.disp.starts)
		assert.Equal(t, repinNewHash, f.disp.storedHash, "the re-pin runs on a container-keeping start")
		assert.True(t, f.disp.storedAllowed, "the broker's read with the current hash is allowed")
		got, err := f.store.GetAgent(context.Background(), a.ID)
		require.NoError(t, err)
		require.NotNil(t, got.PendingConfig)
		assert.Contains(t, got.PendingConfig.Config, "model", "a container-keeping start leaves its held edits held")
	})
}

// staleBeforeFoldStore commits another writer's whole-row update at the
// fold's first UpdateAgentConfig and answers that call with a version
// conflict, so the fold retries on the newer row and commits without
// refreshing the caller's copy: the caller is stale for the re-pin.
type staleBeforeFoldStore struct {
	store.Store
	fault *storeFaultSwitch
	raced bool
}

func (r *staleBeforeFoldStore) UpdateAgentConfig(ctx context.Context, agentID string, expectedVersion int64, applied *store.AgentAppliedConfig, pending *store.AgentPendingConfig) (int64, error) {
	if r.fault.Active() && !r.raced {
		r.raced = true
		cur, err := r.Store.GetAgent(ctx, agentID)
		if err != nil {
			return 0, err
		}
		cur.Message = "concurrent write"
		if err := r.Store.UpdateAgent(ctx, cur); err != nil {
			return 0, err
		}
		return 0, store.ErrVersionConflict
	}
	return r.Store.UpdateAgentConfig(ctx, agentID, expectedVersion, applied, pending)
}

// A caller made stale before the fold, with a template edit: the fold
// commits the held edit, then the re-pin loses its compare-and-set on the
// caller's version: 409 template_pin_conflict, nothing dispatched, the
// claim released, and a retry starts.
func TestRepin_StaleCaller_FoldCommitted_409TemplatePinConflict(t *testing.T) {
	f := setupResumeFill(t)
	setHeldEditsExperiment(t, f.srv, true)
	a := f.userTemplateAgent(t, "repin-stale-fold", string(state.PhaseStopped), f.broker.ID)
	f.holdModelEdit(t, a)
	f.editTemplate(t, repinNewHash)
	racing, fault := installStoreFault(t, f.srv, func(inner store.Store, fault *storeFaultSwitch) *staleBeforeFoldStore {
		return &staleBeforeFoldStore{Store: inner, fault: fault}
	})
	fault.Arm()

	rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
	requireAPIError(t, rec, http.StatusConflict, ErrCodeTemplatePinConflict)
	assert.True(t, racing.raced, "the concurrent write ran before the fold")
	assert.Zero(t, f.disp.starts, "nothing is dispatched")
	got, err := f.store.GetAgent(context.Background(), a.ID)
	require.NoError(t, err)
	requireFolded(t, got)
	assert.Equal(t, "concurrent write", got.Message)
	assert.Equal(t, dispatchReadHash, got.AppliedConfig.TemplateHash, "nothing is re-recorded")
	assert.Empty(t, got.StartClaimID, "the refused start releases its start claim")

	rec = doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
	require.Less(t, rec.Code, 300, "the retry starts: %s", rec.Body.String())
	assert.Equal(t, 1, f.disp.starts)
	assert.Equal(t, repinNewHash, f.disp.storedHash)
}

// A write racing the re-pin after the fold refreshed the caller: the fold's
// edit is committed and the re-pin loses: 409 template_pin_conflict with
// nothing dispatched.
func TestRepin_RaceAfterFold_409(t *testing.T) {
	f := setupResumeFill(t)
	setHeldEditsExperiment(t, f.srv, true)
	a := f.userTemplateAgent(t, "repin-race-fold", string(state.PhaseStopped), f.broker.ID)
	f.holdModelEdit(t, a)
	f.editTemplate(t, repinNewHash)
	racing, fault := installStoreFault(t, f.srv, func(inner store.Store, fault *storeFaultSwitch) *racingPinWriteStore {
		return &racingPinWriteStore{Store: inner, fault: fault}
	})
	fault.Arm()

	rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
	requireAPIError(t, rec, http.StatusConflict, ErrCodeTemplatePinConflict)
	assert.True(t, racing.raced)
	assert.Zero(t, f.disp.starts, "nothing is dispatched")
	got, err := f.store.GetAgent(context.Background(), a.ID)
	require.NoError(t, err)
	requireFolded(t, got)
	assert.Equal(t, dispatchReadHash, got.AppliedConfig.TemplateHash)
	assert.Empty(t, got.StartClaimID)
}

// foldConflictStore answers the first pendingFoldAttempts UpdateAgentConfig
// calls with a version conflict once its switch is armed, so one start's
// fold exhausts its attempts; later calls pass through.
type foldConflictStore struct {
	store.Store
	fault *storeFaultSwitch
	calls int
}

func (r *foldConflictStore) UpdateAgentConfig(ctx context.Context, agentID string, expectedVersion int64, applied *store.AgentAppliedConfig, pending *store.AgentPendingConfig) (int64, error) {
	if r.fault.Active() && r.calls < pendingFoldAttempts {
		r.calls++
		return 0, store.ErrVersionConflict
	}
	return r.Store.UpdateAgentConfig(ctx, agentID, expectedVersion, applied, pending)
}

// The fold giving up after its compare-and-set attempts keeps upstream's
// answer, 502 runtime_error; the claim is released and nothing is
// dispatched, so the next start is not refused.
func TestStart_FoldExhausted_502RuntimeError_Released(t *testing.T) {
	f := setupResumeFill(t)
	setHeldEditsExperiment(t, f.srv, true)
	a := f.userTemplateAgent(t, "repin-fold-exhausted", string(state.PhaseStopped), f.broker.ID)
	f.holdModelEdit(t, a)
	conflicts, fault := installStoreFault(t, f.srv, func(inner store.Store, fault *storeFaultSwitch) *foldConflictStore {
		return &foldConflictStore{Store: inner, fault: fault}
	})
	fault.Arm()

	rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
	requireAPIError(t, rec, http.StatusBadGateway, ErrCodeRuntimeError)
	assert.Equal(t, pendingFoldAttempts, conflicts.calls, "the fold tried each of its attempts")
	assert.Zero(t, f.disp.starts, "nothing is dispatched")
	got, err := f.store.GetAgent(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Empty(t, got.StartClaimID, "the refused start releases its start claim")
	require.NotNil(t, got.PendingConfig)
	assert.Contains(t, got.PendingConfig.Config, "model", "nothing was folded")

	// The wrapper conflicts only for those attempts.
	rec = doRequestAsUser(t, f.srv, f.owner, http.MethodPost, "/api/v1/agents/"+a.ID+"/start", nil)
	require.Less(t, rec.Code, 300, "the next start is not refused by a held claim: %s", rec.Body.String())
	assert.Equal(t, 1, f.disp.starts)
}
