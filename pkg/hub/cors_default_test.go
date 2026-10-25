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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// Hub CORS policy: off by default, explicit opt-in per origin, and never
// Access-Control-Allow-Credentials.

const corsForeignOrigin = "https://example.invalid"

// newCORSHubServer returns a Hub server whose CORS settings come from cors.
func newCORSHubServer(t *testing.T, cors func(*ServerConfig)) *Server {
	t.Helper()
	st, err := newTestStore(t, ":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	cfg := DefaultServerConfig()
	cors(&cfg)
	srv, err := newTestHubServer(t, cfg, st)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	return srv
}

// hubFromGlobalDefaults applies the Hub CORS settings of the built-in global
// config, as the server command maps them.
func hubFromGlobalDefaults(cfg *ServerConfig) {
	h := config.DefaultGlobalConfig().Hub
	cfg.CORSEnabled = h.CORSEnabled
	cfg.CORSAllowedOrigins = h.CORSAllowedOrigins
	cfg.CORSAllowedMethods = h.CORSAllowedMethods
	cfg.CORSAllowedHeaders = h.CORSAllowedHeaders
	cfg.CORSMaxAge = h.CORSMaxAge
}

// corsRequests are cross-origin requests covering a read, a mutating call
// and a preflight.
var corsRequests = []struct {
	method, path string
}{
	{http.MethodGet, "/healthz"},
	{http.MethodPost, "/api/v1/agents"},
	{http.MethodOptions, "/api/v1/agents"},
}

// serveCORS sends method path from origin to h and returns the response
// headers.
func serveCORS(h http.Handler, method, path, origin string) http.Header {
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	req.Header.Set("Origin", origin)
	if method == http.MethodOptions {
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Header()
}

func TestHubCORSDefault_NoAllowOrigin(t *testing.T) {
	for _, tc := range []struct {
		name string
		cors func(*ServerConfig)
	}{
		{"global config defaults", hubFromGlobalDefaults},
		{"server config defaults", func(*ServerConfig) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCORSHubServer(t, tc.cors).Handler()
			for _, rq := range corsRequests {
				hdr := serveCORS(h, rq.method, rq.path, corsForeignOrigin)
				if got := hdr.Get("Access-Control-Allow-Origin"); got != "" {
					t.Errorf("%s %s from a foreign origin: Access-Control-Allow-Origin = %q, want none", rq.method, rq.path, got)
				}
				if got := hdr.Values("Access-Control-Allow-Credentials"); len(got) != 0 {
					t.Errorf("%s %s: Access-Control-Allow-Credentials = %q, want none", rq.method, rq.path, got)
				}
			}
		})
	}
}

func TestHubCORSDefault_ConfigOff(t *testing.T) {
	h := config.DefaultGlobalConfig().Hub
	if h.CORSEnabled || len(h.CORSAllowedOrigins) != 0 {
		t.Errorf("global config Hub CORS enabled=%v origins=%q, want off with none", h.CORSEnabled, h.CORSAllowedOrigins)
	}
	sc := DefaultServerConfig()
	if sc.CORSEnabled || len(sc.CORSAllowedOrigins) != 0 {
		t.Errorf("DefaultServerConfig CORS enabled=%v origins=%q, want off with none", sc.CORSEnabled, sc.CORSAllowedOrigins)
	}
	if w := corsWildcardWarning(sc); w != "" {
		t.Errorf("default config warns %q, want no warning", w)
	}
}

func TestHubCORSOptIn_ListedOriginOnly(t *testing.T) {
	const listed = "https://ui.example.test"
	h := newCORSHubServer(t, func(cfg *ServerConfig) {
		cfg.CORSEnabled = true
		cfg.CORSAllowedOrigins = []string{listed}
	}).Handler()
	for _, rq := range corsRequests {
		if got := serveCORS(h, rq.method, rq.path, listed).Get("Access-Control-Allow-Origin"); got != listed {
			t.Errorf("%s %s from the listed origin: Access-Control-Allow-Origin = %q, want %q", rq.method, rq.path, got, listed)
		}
		if got := serveCORS(h, rq.method, rq.path, corsForeignOrigin).Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s %s from an unlisted origin: Access-Control-Allow-Origin = %q, want none", rq.method, rq.path, got)
		}
	}
}

func TestHubCORSOptIn_EnabledWithoutOriginsAllowsNone(t *testing.T) {
	h := newCORSHubServer(t, func(cfg *ServerConfig) {
		hubFromGlobalDefaults(cfg)
		cfg.CORSEnabled = true
	}).Handler()
	for _, rq := range corsRequests {
		if got := serveCORS(h, rq.method, rq.path, corsForeignOrigin).Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s %s with CORS enabled and default origins: Access-Control-Allow-Origin = %q, want none", rq.method, rq.path, got)
		}
	}
}

func TestHubCORSOptIn_ExplicitWildcardEchoesAndWarns(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := newCORSHubServer(t, func(cfg *ServerConfig) {
		cfg.CORSEnabled = true
		cfg.CORSAllowedOrigins = []string{"*"}
	}).Handler()

	if !strings.Contains(logs.String(), "Hub CORS allows every origin") {
		t.Errorf("no startup warning for an explicit wildcard; logs:\n%s", logs.String())
	}
	const origin = "https://dev.example.test"
	if got := serveCORS(h, http.MethodPost, "/api/v1/agents", origin).Get("Access-Control-Allow-Origin"); got != origin {
		t.Errorf("explicit wildcard: Access-Control-Allow-Origin = %q, want %q", got, origin)
	}
}

// The middleware echoes the allowed origin, so it must never add
// Access-Control-Allow-Credentials, whatever the origin list.
func TestHubCORS_NeverAllowsCredentials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		origins []string
		origin  string
	}{
		{"explicit wildcard", []string{"*"}, "https://dev.example.test"},
		{"listed origin", []string{"https://ui.example.test"}, "https://ui.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCORSHubServer(t, func(cfg *ServerConfig) {
				cfg.CORSEnabled = true
				cfg.CORSAllowedOrigins = tc.origins
			}).Handler()
			for _, rq := range corsRequests {
				hdr := serveCORS(h, rq.method, rq.path, tc.origin)
				if hdr.Get("Access-Control-Allow-Origin") != tc.origin {
					t.Fatalf("%s %s: precondition: origin %q not allowed", rq.method, rq.path, tc.origin)
				}
				if got := hdr.Values("Access-Control-Allow-Credentials"); len(got) != 0 {
					t.Errorf("%s %s: Access-Control-Allow-Credentials = %q, want none", rq.method, rq.path, got)
				}
			}
		})
	}
}
