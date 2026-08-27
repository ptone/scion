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

package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
)

func TestHandleAdminMessagingDivergence_IncludesFallbacks(t *testing.T) {
	// Reset global metrics for this test.
	messaging.DivergenceMetrics = &messaging.DivergenceCounter{}

	// Increment some counters.
	messaging.DivergenceMetrics.Inc(true)  // 1 match
	messaging.DivergenceMetrics.Inc(true)  // 2 matches
	messaging.DivergenceMetrics.Inc(false) // 1 mismatch
	messaging.DivergenceMetrics.IncFallback()
	messaging.DivergenceMetrics.IncFallback()

	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/messaging/divergence", nil)
	rr := httptest.NewRecorder()
	srv.handleAdminMessagingDivergence(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify all expected fields are present.
	if _, ok := body["fallbacks"]; !ok {
		t.Fatal("expected 'fallbacks' field in response, got:", body)
	}

	if got := body["fallbacks"].(float64); got != 2 {
		t.Errorf("expected fallbacks=2, got %v", got)
	}
	if got := body["matches"].(float64); got != 2 {
		t.Errorf("expected matches=2, got %v", got)
	}
	if got := body["mismatches"].(float64); got != 1 {
		t.Errorf("expected mismatches=1, got %v", got)
	}
	if got := body["total"].(float64); got != 3 {
		t.Errorf("expected total=3, got %v", got)
	}
}

func TestHandleAdminMessagingDivergence_FallbacksZero(t *testing.T) {
	messaging.DivergenceMetrics = &messaging.DivergenceCounter{}

	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/messaging/divergence", nil)
	rr := httptest.NewRecorder()
	srv.handleAdminMessagingDivergence(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if got := body["fallbacks"].(float64); got != 0 {
		t.Errorf("expected fallbacks=0, got %v", got)
	}
}

func TestHandleAdminMessagingDivergence_MethodNotAllowed(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/messaging/divergence", nil)
	rr := httptest.NewRecorder()
	srv.handleAdminMessagingDivergence(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}
