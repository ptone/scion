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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

var (
	setupTestKeyOnce sync.Once
	setupTestKeyPEM  string
)

func setupTestPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	setupTestKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		block := &pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		}
		setupTestKeyPEM = string(pem.EncodeToMemory(block))
	})
	return setupTestKeyPEM
}

func configureSetupTestGitHubApp(t *testing.T, srv *Server, ghAPIURL string) {
	t.Helper()
	srv.mu.Lock()
	srv.config.GitHubAppConfig.AppID = 42
	srv.config.GitHubAppConfig.PrivateKey = setupTestPrivateKeyPEM(t)
	srv.config.GitHubAppConfig.APIBaseURL = ghAPIURL
	srv.mu.Unlock()
}

func setupTestServer(t *testing.T, ghAPIURL string) (*Server, store.Store) {
	t.Helper()
	srv, s := webhookTestServer(t)
	configureSetupTestGitHubApp(t, srv, ghAPIURL)
	return srv, s
}

type setupFaultStore struct {
	store.Store
	fault     *storeFaultSwitch
	getErr    error
	createErr error
	updateErr error
}

func (f *setupFaultStore) GetGitHubInstallation(ctx context.Context, installationID int64) (*store.GitHubInstallation, error) {
	if f.fault.Active() && f.getErr != nil {
		return nil, f.getErr
	}
	return f.Store.GetGitHubInstallation(ctx, installationID)
}

func (f *setupFaultStore) CreateGitHubInstallation(ctx context.Context, installation *store.GitHubInstallation) error {
	if f.fault.Active() && f.createErr != nil {
		return f.createErr
	}
	return f.Store.CreateGitHubInstallation(ctx, installation)
}

func (f *setupFaultStore) UpdateGitHubInstallation(ctx context.Context, installation *store.GitHubInstallation) error {
	if f.fault.Active() && f.updateErr != nil {
		return f.updateErr
	}
	return f.Store.UpdateGitHubInstallation(ctx, installation)
}

func TestHandleGitHubAppSetup_FirstSetupCreatesInstallation(t *testing.T) {
	fakeGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations/12345":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":12345,"app_id":42,"account":{"login":"acme","type":"Organization"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/12345/access_tokens":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"token":"ghs_test_token","expires_at":%q}`,
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"total_count":2,"repositories":[{"full_name":"acme/widgets"},{"full_name":"acme/api"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeGH.Close()

	srv, s := setupTestServer(t, fakeGH.URL)

	req := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=12345&setup_action=install", nil)
	rec := httptest.NewRecorder()
	srv.handleGitHubAppSetup(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected status %d, got %d: %s", http.StatusFound, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/github-app/installed?installation_id=12345" {
		t.Fatalf("expected redirect to /github-app/installed?installation_id=12345, got %q", got)
	}

	got, err := s.GetGitHubInstallation(context.Background(), 12345)
	if err != nil {
		t.Fatalf("GetGitHubInstallation: %v", err)
	}
	if got.AccountLogin != "acme" || got.AccountType != "Organization" {
		t.Errorf("account = (%q, %q), want (acme, Organization)", got.AccountLogin, got.AccountType)
	}
	if got.AppID != 42 {
		t.Errorf("AppID = %d, want 42", got.AppID)
	}
	if got.Status != store.GitHubInstallationStatusActive {
		t.Errorf("Status = %q, want %q", got.Status, store.GitHubInstallationStatusActive)
	}
	wantRepos := []string{"acme/widgets", "acme/api"}
	if !slices.Equal(got.Repositories, wantRepos) {
		t.Errorf("Repositories = %v, want %v", got.Repositories, wantRepos)
	}
}

func TestHandleGitHubAppSetup_RepeatedSetupUpdatesExistingAndPreservesUnrelatedFields(t *testing.T) {
	fakeGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations/12345":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":12345,"app_id":999,"account":{"login":"acme-renamed","type":"Organization"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/12345/access_tokens":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"token":"ghs_test_token","expires_at":%q}`,
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"total_count":2,"repositories":[{"full_name":"acme-renamed/api"},{"full_name":"acme-renamed/web"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeGH.Close()

	srv, s := setupTestServer(t, fakeGH.URL)
	ctx := context.Background()

	originalCreatedAt := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	if err := s.CreateGitHubInstallation(ctx, &store.GitHubInstallation{
		InstallationID: 12345,
		AccountLogin:   "acme-old",
		AccountType:    "User",
		AppID:          42,
		Repositories:   []string{"acme-old/legacy"},
		Status:         store.GitHubInstallationStatusSuspended,
		CreatedAt:      originalCreatedAt,
		UpdatedAt:      originalCreatedAt,
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}

	routeSetAt := time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC)
	wantRoute := store.GitHubTriggerRoute{
		PrimaryProjectID: "proj-routed",
		SetBy:            "admin-1",
		SetAt:            routeSetAt,
	}
	if err := s.SetGitHubInstallationTriggerRoute(ctx, 12345, "acme-old/legacy", wantRoute); err != nil {
		t.Fatalf("SetGitHubInstallationTriggerRoute: %v", err)
	}

	const boundProjectID = "00000000-0000-4000-8000-000000000001"
	instID := int64(12345)
	const wantBindingAnnotation = `{"installation_id":12345,"remote":"https://github.com/acme-old/legacy.git","bound_by":"admin-1"}`
	if err := s.CreateProject(ctx, &store.Project{
		ID:                   boundProjectID,
		Name:                 "Bound Project",
		Slug:                 "bound-project",
		GitRemote:            "https://github.com/acme-renamed/api.git",
		GitHubInstallationID: &instID,
		Annotations: map[string]string{
			"scion.dev/github-installation-binding": wantBindingAnnotation,
		},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=12345&setup_action=update", nil)
	rec := httptest.NewRecorder()
	srv.handleGitHubAppSetup(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected status %d, got %d: %s", http.StatusFound, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/github-app/installed?installation_id=12345" {
		t.Fatalf("expected redirect to /github-app/installed?installation_id=12345, got %q", got)
	}

	got, err := s.GetGitHubInstallation(ctx, 12345)
	if err != nil {
		t.Fatalf("GetGitHubInstallation: %v", err)
	}
	if got.AccountLogin != "acme-renamed" || got.AccountType != "Organization" {
		t.Errorf("account = (%q, %q), want (acme-renamed, Organization)", got.AccountLogin, got.AccountType)
	}
	if got.Status != store.GitHubInstallationStatusActive {
		t.Errorf("Status = %q, want %q", got.Status, store.GitHubInstallationStatusActive)
	}
	wantRepos := []string{"acme-renamed/api", "acme-renamed/web"}
	if !slices.Equal(got.Repositories, wantRepos) {
		t.Errorf("Repositories = %v, want %v", got.Repositories, wantRepos)
	}

	// Unrelated fields must survive repeated setup.
	if got.AppID != 42 {
		t.Errorf("AppID = %d, want preserved 42", got.AppID)
	}
	if !got.CreatedAt.Equal(originalCreatedAt) {
		t.Errorf("CreatedAt = %v, want preserved %v", got.CreatedAt, originalCreatedAt)
	}
	gotRoute, ok := got.TriggerRouting["acme-old/legacy"]
	if !ok || gotRoute.PrimaryProjectID != wantRoute.PrimaryProjectID || gotRoute.SetBy != wantRoute.SetBy {
		t.Errorf("TriggerRouting = %+v, want preserved route %+v", got.TriggerRouting, wantRoute)
	}

	// Existing project binding must not be touched.
	proj, err := s.GetProject(ctx, boundProjectID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if proj.GitHubInstallationID == nil || *proj.GitHubInstallationID != 12345 {
		t.Errorf("project GitHubInstallationID = %v, want 12345", proj.GitHubInstallationID)
	}
	if proj.Annotations["scion.dev/github-installation-binding"] != wantBindingAnnotation {
		t.Errorf("project binding annotation = %q, want %q",
			proj.Annotations["scion.dev/github-installation-binding"], wantBindingAnnotation)
	}
}

func TestHandleGitHubAppSetup_RepeatedSetupPreservesReposOnRepoListFailure(t *testing.T) {
	fakeGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations/12345":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":12345,"app_id":42,"account":{"login":"acme","type":"Organization"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/12345/access_tokens":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"token":"ghs_test_token","expires_at":%q}`,
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"upstream failure"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeGH.Close()

	srv, s := setupTestServer(t, fakeGH.URL)
	ctx := context.Background()

	wantRepos := []string{"acme/widgets", "acme/api"}
	if err := s.CreateGitHubInstallation(ctx, &store.GitHubInstallation{
		InstallationID: 12345,
		AccountLogin:   "acme",
		AccountType:    "Organization",
		AppID:          42,
		Repositories:   slices.Clone(wantRepos),
		Status:         store.GitHubInstallationStatusSuspended,
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=12345&setup_action=update", nil)
	rec := httptest.NewRecorder()
	srv.handleGitHubAppSetup(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected status %d, got %d: %s", http.StatusFound, rec.Code, rec.Body.String())
	}

	got, err := s.GetGitHubInstallation(ctx, 12345)
	if err != nil {
		t.Fatalf("GetGitHubInstallation: %v", err)
	}
	if got.Status != store.GitHubInstallationStatusActive {
		t.Errorf("Status = %q, want %q", got.Status, store.GitHubInstallationStatusActive)
	}
	if !slices.Equal(got.Repositories, wantRepos) {
		t.Errorf("Repositories = %v, want preserved %v", got.Repositories, wantRepos)
	}
}

func TestHandleGitHubAppSetup_StoreErrorsReportedNotSwallowed(t *testing.T) {
	const secretDBDetail = "sqlite_internal_secret_detail_8626"

	fakeGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations/12345":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":12345,"app_id":42,"account":{"login":"acme","type":"Organization"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/12345/access_tokens":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"token":"ghs_test_token","expires_at":%q}`,
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"total_count":1,"repositories":[{"full_name":"acme/widgets"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeGH.Close()

	cases := []struct {
		name      string
		seedRow   bool
		getErr    error
		createErr error
		updateErr error
	}{
		{
			name:   "get_error",
			getErr: errors.New(secretDBDetail),
		},
		{
			name:      "create_error",
			createErr: errors.New(secretDBDetail),
		},
		{
			name:      "update_error",
			seedRow:   true,
			updateErr: errors.New(secretDBDetail),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, s, _, fault := testServerWithStoreFault(t, func(inner store.Store, f *storeFaultSwitch) *setupFaultStore {
				return &setupFaultStore{
					Store:     inner,
					fault:     f,
					getErr:    tc.getErr,
					createErr: tc.createErr,
					updateErr: tc.updateErr,
				}
			})
			configureSetupTestGitHubApp(t, srv, fakeGH.URL)

			if tc.seedRow {
				if err := s.CreateGitHubInstallation(context.Background(), &store.GitHubInstallation{
					InstallationID: 12345,
					AccountLogin:   "acme-old",
					AccountType:    "Organization",
					AppID:          42,
					Repositories:   []string{"acme-old/legacy"},
					Status:         store.GitHubInstallationStatusActive,
				}); err != nil {
					t.Fatalf("seed CreateGitHubInstallation: %v", err)
				}
			}

			fault.Arm()

			req := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=12345&setup_action=install", nil)
			rec := httptest.NewRecorder()
			srv.handleGitHubAppSetup(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("expected status %d, got %d (Location=%q, body=%s)",
					http.StatusInternalServerError, rec.Code, rec.Header().Get("Location"), rec.Body.String())
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("expected no redirect Location on store error, got %q", loc)
			}
			if strings.Contains(rec.Body.String(), secretDBDetail) {
				t.Errorf("response body leaked internal store error: %s", rec.Body.String())
			}
		})
	}
}

func TestHandleGitHubAppSetup_PublicRouteAuthorizationUnchanged(t *testing.T) {
	fakeGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations/54321":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":54321,"app_id":42,"account":{"login":"acme","type":"Organization"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/54321/access_tokens":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"token":"ghs_test_token","expires_at":%q}`,
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"total_count":1,"repositories":[{"full_name":"acme/public-test"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeGH.Close()

	srv, s := setupTestServer(t, fakeGH.URL)

	// Send an unauthenticated request through the full HTTP handler stack
	// (including auth middleware and route guards).
	req := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=54321&setup_action=install", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected unauthenticated GET /github-app/setup to return %d, got %d: %s",
			http.StatusFound, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/github-app/installed?installation_id=54321" {
		t.Fatalf("expected redirect to /github-app/installed?installation_id=54321, got %q", got)
	}

	got, err := s.GetGitHubInstallation(context.Background(), 54321)
	if err != nil {
		t.Fatalf("GetGitHubInstallation: %v", err)
	}
	if got.AccountLogin != "acme" {
		t.Errorf("AccountLogin = %q, want acme", got.AccountLogin)
	}
}

func TestCombinedWebServer_GitHubAppSetupForwardsToHub(t *testing.T) {
	t.Run("unconfigured app returns 503 from hub handler", func(t *testing.T) {
		srv, _ := webhookTestServer(t)
		ws := newTestWebServer(t, WebServerConfig{
			AssetsDir: t.TempDir(),
		})
		ws.MountHubAPI(srv.Handler(), func(context.Context) error { return nil })

		req := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=12345&setup_action=install", nil)
		rec := httptest.NewRecorder()
		ws.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected status %d from handleGitHubAppSetup, got %d: %s",
				http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "GitHub App not configured") {
			t.Fatalf("expected body to contain %q, got %q", "GitHub App not configured", rec.Body.String())
		}
	})

	t.Run("configured app redirects to installed page", func(t *testing.T) {
		fakeGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/app/installations/67890":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":67890,"app_id":42,"account":{"login":"acme","type":"Organization"}}`))
			case r.Method == http.MethodPost && r.URL.Path == "/app/installations/67890/access_tokens":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprintf(w, `{"token":"ghs_test_token","expires_at":%q}`,
					time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
			case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"total_count":1,"repositories":[{"full_name":"acme/combined"}]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer fakeGH.Close()

		srv, s := setupTestServer(t, fakeGH.URL)
		ws := newTestWebServer(t, WebServerConfig{
			AssetsDir: t.TempDir(),
		})
		ws.MountHubAPI(srv.Handler(), func(context.Context) error { return nil })

		req := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=67890&setup_action=install", nil)
		rec := httptest.NewRecorder()
		ws.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusFound {
			t.Fatalf("expected status %d, got %d: %s", http.StatusFound, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); got != "/github-app/installed?installation_id=67890" {
			t.Fatalf("Location = %q, want %q", got, "/github-app/installed?installation_id=67890")
		}

		got, err := s.GetGitHubInstallation(context.Background(), 67890)
		if err != nil {
			t.Fatalf("GetGitHubInstallation: %v", err)
		}
		if got.AccountLogin != "acme" {
			t.Errorf("AccountLogin = %q, want acme", got.AccountLogin)
		}
	})

	t.Run("SPA still serves installed and non-setup paths", func(t *testing.T) {
		srv, _ := webhookTestServer(t)
		ws := newDevAuthWebServer(t, func(cfg *WebServerConfig) {
			cfg.AssetsDir = t.TempDir()
		})
		ws.MountHubAPI(srv.Handler(), func(context.Context) error { return nil })
		handler := devSignedIn(t, ws)

		for _, path := range []string{
			"/github-app/installed?installation_id=67890",
			"/github-app/setup/extra",
			"/projects",
		} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s: expected SPA status %d, got %d: %s", path, http.StatusOK, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "scion-app") {
				t.Fatalf("GET %s: expected SPA shell containing scion-app, got %q", path, rec.Body.String())
			}
		}
	})
}

func TestHandleGitHubAppSetup_RateLimitBlocksGitHubCallsAndIsPerIP(t *testing.T) {
	var ghCalls atomic.Int32
	fakeGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ghCalls.Add(1)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations/77777":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":77777,"app_id":42,"account":{"login":"acme","type":"Organization"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77777/access_tokens":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"token":"ghs_test_token","expires_at":%q}`,
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"total_count":1,"repositories":[{"full_name":"acme/rate-limit"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeGH.Close()

	srv, _ := setupTestServer(t, fakeGH.URL)
	if srv.githubAppSetupRateLimiter == nil {
		t.Fatal("expected srv.githubAppSetupRateLimiter to be initialized")
	}
	now := time.Now()
	srv.githubAppSetupRateLimiter.buckets.nowFunc = func() time.Time { return now }

	ws := newTestWebServer(t, WebServerConfig{AssetsDir: t.TempDir()})
	ws.MountHubAPI(srv.Handler(), func(context.Context) error { return nil })
	handler := ws.Handler()

	// Requests within burst from IP A succeed and call GitHub.
	for i := range githubAppSetupBurst {
		req := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=77777&setup_action=install", nil)
		req.RemoteAddr = "198.51.100.10:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("IP A request %d: expected %d, got %d: %s", i+1, http.StatusFound, rec.Code, rec.Body.String())
		}
	}

	callsBeforeLimit := ghCalls.Load()
	if callsBeforeLimit == 0 {
		t.Fatal("expected GitHub API calls for requests within limit")
	}

	// Next request from IP A is rejected with 429 and makes zero GitHub API calls.
	reqOver := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=77777&setup_action=install", nil)
	reqOver.RemoteAddr = "198.51.100.10:5678"
	recOver := httptest.NewRecorder()
	handler.ServeHTTP(recOver, reqOver)
	if recOver.Code != http.StatusTooManyRequests {
		t.Fatalf("over-limit request: expected %d, got %d: %s", http.StatusTooManyRequests, recOver.Code, recOver.Body.String())
	}
	if recOver.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 429 response")
	}
	if !strings.Contains(recOver.Body.String(), "too many setup callbacks") {
		t.Errorf("expected generic 429 message, got %q", recOver.Body.String())
	}
	if got := ghCalls.Load(); got != callsBeforeLimit {
		t.Fatalf("rate-limited request must not call GitHub: calls went from %d to %d", callsBeforeLimit, got)
	}

	// Spoofing X-Forwarded-For from the same untrusted peer must still be 429 with no GitHub calls.
	reqSpoof := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=77777&setup_action=install", nil)
	reqSpoof.RemoteAddr = "198.51.100.10:9999"
	reqSpoof.Header.Set("X-Forwarded-For", "203.0.113.50")
	recSpoof := httptest.NewRecorder()
	handler.ServeHTTP(recSpoof, reqSpoof)
	if recSpoof.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed XFF request: expected %d, got %d: %s", http.StatusTooManyRequests, recSpoof.Code, recSpoof.Body.String())
	}
	if got := ghCalls.Load(); got != callsBeforeLimit {
		t.Fatalf("spoofed XFF request must not call GitHub: calls went from %d to %d", callsBeforeLimit, got)
	}

	// A different IP B has its own budget and succeeds.
	reqOtherIP := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=77777&setup_action=install", nil)
	reqOtherIP.RemoteAddr = "198.51.100.20:4321"
	recOtherIP := httptest.NewRecorder()
	handler.ServeHTTP(recOtherIP, reqOtherIP)
	if recOtherIP.Code != http.StatusFound {
		t.Fatalf("IP B request: expected %d, got %d: %s", http.StatusFound, recOtherIP.Code, recOtherIP.Body.String())
	}
	if got := ghCalls.Load(); got <= callsBeforeLimit {
		t.Fatalf("expected IP B request to reach GitHub API, calls stayed at %d", got)
	}
}

func TestServer_GitHubAppSetupRateLimiter_CleanupRunsInBackground(t *testing.T) {
	srv, _ := webhookTestServer(t)
	if srv.githubAppSetupRateLimiter == nil {
		t.Fatal("expected srv.githubAppSetupRateLimiter to be set")
	}
	srv.githubAppSetupRateLimiter.buckets.cleanupInterval = 5 * time.Millisecond
	srv.githubAppSetupRateLimiter.buckets.maxEntries = 1
	srv.githubAppSetupRateLimiter.buckets.maxAge = 10 * time.Millisecond

	req1 := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=1", nil)
	req1.RemoteAddr = "198.51.100.9:1"
	if allowed, _ := srv.githubAppSetupRateLimiter.Allow(req1); !allowed {
		t.Fatal("first IP should be allowed")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=1", nil)
	req2.RemoteAddr = "198.51.100.10:1"
	if allowed, _ := srv.githubAppSetupRateLimiter.Allow(req2); allowed {
		t.Fatal("second IP should be refused when limiter is at capacity (maxEntries=1)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartBackgroundServices(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if allowed, _ := srv.githubAppSetupRateLimiter.Allow(req2); allowed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("second IP was still refused after 2s; StartBackgroundServices did not run githubAppSetupRateLimiter.StartCleanup")
}
