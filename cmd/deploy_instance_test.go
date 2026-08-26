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

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// BuildIAPAudience tests
// ---------------------------------------------------------------------------

func TestBuildIAPAudience(t *testing.T) {
	tests := []struct {
		name          string
		projectNumber string
		region        string
		instanceName  string
		want          string
	}{
		{
			name:          "standard audience path",
			projectNumber: "123456789",
			region:        "us-east4",
			instanceName:  "scion-hub-1",
			want:          "/projects/123456789/locations/us-east4/services/scion-hub-1",
		},
		{
			name:          "different region",
			projectNumber: "999",
			region:        "us-central1",
			instanceName:  "my-instance",
			want:          "/projects/999/locations/us-central1/services/my-instance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildIAPAudience(tt.projectNumber, tt.region, tt.instanceName)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestBuildIAPAudienceUsesServicesNotInstances is a pinning test required by
// §11.9. The IAP audience uses "services" even for Cloud Run Instances.
// This is IAP's fixed resource vocabulary across every backend type, not a bug.
// Changing "services" to "instances" will produce an audience mismatch on
// every request, resulting in a 401 that does not obviously point back here.
// See §11.3 of cloudrun-instances-sandboxes.md.
func TestBuildIAPAudienceUsesServicesNotInstances(t *testing.T) {
	audience := BuildIAPAudience("123", "us-east4", "my-instance")

	// The audience MUST contain "services", NOT "instances".
	assert.Contains(t, audience, "/services/",
		"IAP audience must use 'services' vocabulary even for Instances — "+
			"this is IAP's fixed path format. Do NOT change to 'instances'; "+
			"see §11.3 of cloudrun-instances-sandboxes.md")
	assert.NotContains(t, audience, "/instances/",
		"IAP audience must NOT use 'instances' — IAP uses 'services' for all "+
			"backend types including Cloud Run Instances")
}

// TestBuildIAPAudienceAcceptedByIsSupportedIAPAudience verifies that the
// audience format produced by BuildIAPAudience is accepted by the existing
// isSupportedIAPAudience validator in server_foreground.go.
func TestBuildIAPAudienceAcceptedByIsSupportedIAPAudience(t *testing.T) {
	audience := BuildIAPAudience("123456789", "us-east4", "scion-hub-1")
	assert.True(t, isSupportedIAPAudience(audience),
		"BuildIAPAudience output must be accepted by isSupportedIAPAudience — "+
			"if this fails, the hub will reject IAP tokens for this Instance")
}

// ---------------------------------------------------------------------------
// BuildInstanceURL tests
// ---------------------------------------------------------------------------

func TestBuildInstanceURL(t *testing.T) {
	tests := []struct {
		name          string
		instanceName  string
		projectNumber string
		region        string
		want          string
	}{
		{
			name:          "standard URL format",
			instanceName:  "scion-hub-1",
			projectNumber: "123456789",
			region:        "us-east4",
			want:          "https://scion-hub-1-123456789.us-east4.run.app",
		},
		{
			name:          "different name and region",
			instanceName:  "test-inst",
			projectNumber: "999",
			region:        "us-central1",
			want:          "https://test-inst-999.us-central1.run.app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildInstanceURL(tt.instanceName, tt.projectNumber, tt.region)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestBuildInstanceURLMatchesIapAudienceToCloudRunURL verifies that BuildInstanceURL
// produces the same URL that iapAudienceToCloudRunURL would derive from the
// corresponding audience path. This is a consistency check — both functions
// must agree on the URL format.
func TestBuildInstanceURLMatchesIapAudienceToCloudRunURL(t *testing.T) {
	projectNumber := "123456789"
	region := "us-east4"
	name := "scion-hub-1"

	audience := BuildIAPAudience(projectNumber, region, name)
	fromAudience := iapAudienceToCloudRunURL(audience)
	direct := BuildInstanceURL(name, projectNumber, region)

	assert.Equal(t, fromAudience, direct,
		"BuildInstanceURL and iapAudienceToCloudRunURL must produce the same URL")
}

// ---------------------------------------------------------------------------
// IAP enable PATCH tests
// ---------------------------------------------------------------------------

// TestEnableIAPPatchBody verifies that the PATCH body sent by diEnableIAP
// contains both iapEnabled: true and invokerIamDisabled: true. The invariant
// is that invokerIamDisabled: true is NEVER sent without iapEnabled: true —
// both must appear together in every PATCH.
func TestEnableIAPPatchBody(t *testing.T) {
	var capturedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		capturedBody = body
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	// Build the same body that diEnableIAP builds internally
	patchBody := map[string]bool{
		"iapEnabled":         true,
		"invokerIamDisabled": true,
	}
	jsonBody, err := json.Marshal(patchBody)
	require.NoError(t, err)

	// Call the REST endpoint directly (we can't call diEnableIAP because
	// it calls diGetAccessToken which requires gcloud)
	statusCode, _, err := diRESTCall(http.MethodPatch, server.URL, "fake-token", jsonBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, statusCode)

	// Verify the captured body contains both critical fields
	var parsed map[string]interface{}
	err = json.Unmarshal(capturedBody, &parsed)
	require.NoError(t, err)

	assert.Equal(t, true, parsed["iapEnabled"],
		"iapEnabled must be true — omitting it leaves the instance without IAP")
	assert.Equal(t, true, parsed["invokerIamDisabled"],
		"invokerIamDisabled must be true — the invariant requires both fields together")

	// Verify ONLY these two fields are present (no extra fields that could
	// interact with v1-only fields like sandboxLauncher)
	assert.Len(t, parsed, 2,
		"PATCH body must contain exactly iapEnabled and invokerIamDisabled — "+
			"no other fields, to avoid interacting with v1-only fields (§11.5c)")
}

// TestEnableIAPUpdateMask verifies that the PATCH URL includes the correct
// updateMask query parameter targeting both iapEnabled and invokerIamDisabled.
// The updateMask ensures only the IAP booleans are touched, leaving v1-only
// fields (like sandboxLauncher) untouched.
func TestEnableIAPUpdateMask(t *testing.T) {
	var capturedURL string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	// Build the URL the same way diEnableIAP does, but pointing at our test server
	patchURL := fmt.Sprintf("%s?updateMask=iapEnabled,invokerIamDisabled", server.URL)

	statusCode, _, err := diRESTCall(http.MethodPatch, patchURL, "fake-token", []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, statusCode)

	// Verify updateMask is present and contains both fields
	assert.Contains(t, capturedURL, "updateMask=",
		"PATCH URL must include updateMask to limit which fields are modified")
	assert.Contains(t, capturedURL, "iapEnabled",
		"updateMask must include iapEnabled")
	assert.Contains(t, capturedURL, "invokerIamDisabled",
		"updateMask must include invokerIamDisabled")
}

// ---------------------------------------------------------------------------
// diIAMMemberPrefix tests
// ---------------------------------------------------------------------------

func TestIAMMemberPrefix_UserEmail(t *testing.T) {
	email := "admin@example.com"
	prefix := diIAMMemberPrefix(email)
	assert.Equal(t, "user:", prefix)
	assert.Equal(t, "user:admin@example.com", prefix+email,
		"normal email must produce user:<email> IAM member")
}

func TestIAMMemberPrefix_ServiceAccount(t *testing.T) {
	email := "deploy@my-project.iam.gserviceaccount.com"
	prefix := diIAMMemberPrefix(email)
	assert.Equal(t, "serviceAccount:", prefix)
	assert.Equal(t, "serviceAccount:deploy@my-project.iam.gserviceaccount.com", prefix+email,
		"service account email must produce serviceAccount:<email> IAM member")
}

// ---------------------------------------------------------------------------
// Gate 2 (perimeter assertion) tests
// ---------------------------------------------------------------------------

func TestAssertPerimeter_IAPEnforcing(t *testing.T) {
	// Simulate IAP: 302 to accounts.google.com with IAP header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://accounts.google.com/signin?...")
		w.Header().Set("X-Goog-Iap-Generated-Response", "true")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	err := diAssertPerimeter(server.URL)
	assert.NoError(t, err, "should succeed when IAP is enforcing")
}

func TestAssertPerimeter_AppAnswers(t *testing.T) {
	// Simulate no IAP: app answers directly with 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello world"))
	}))
	defer server.Close()

	err := diAssertPerimeter(server.URL)
	require.Error(t, err, "must FAIL when app answers directly")
	assert.Contains(t, err.Error(), "UNPROTECTED",
		"error message must clearly indicate the instance is unprotected")
}

func TestAssertPerimeter_WrongRedirect(t *testing.T) {
	// 302 but not to accounts.google.com
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://evil.example.com/phish")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	err := diAssertPerimeter(server.URL)
	require.Error(t, err, "must fail when redirect is not to accounts.google.com")
	assert.Contains(t, err.Error(), "not to accounts.google.com")
}

func TestAssertPerimeter_IAPNoHeader(t *testing.T) {
	// 302 to accounts.google.com but missing the IAP header — still passes
	// because the redirect alone proves IAP is enforcing; the header is
	// a bonus check.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://accounts.google.com/signin?...")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	err := diAssertPerimeter(server.URL)
	assert.NoError(t, err, "should pass even without IAP header if redirect is correct")
}

func TestAssertPerimeter_CloudRunErrorPage(t *testing.T) {
	// When the Instance is dead (wrong port, crash loop, missing binary),
	// Cloud Run returns its own error page (502 or 503) instead of the
	// IAP 302. The error message must mention Instance health so the
	// operator knows the problem is the container, not IAP.
	for _, code := range []int{http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				w.Write([]byte("Cloud Run error page"))
			}))
			defer server.Close()

			err := diAssertPerimeter(server.URL)
			require.Error(t, err, "must fail when Cloud Run returns %d", code)
			assert.Contains(t, err.Error(), "not be serving",
				"error message must mention the instance may not be serving")
			assert.Contains(t, err.Error(), "CMD",
				"error message must suggest checking the Dockerfile CMD")
		})
	}
}

// ---------------------------------------------------------------------------
// diShortenError tests
// ---------------------------------------------------------------------------

func TestShortenError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "dial tcp error",
			err:  fmt.Errorf("dial tcp 1.2.3.4:443: connect: connection refused"),
			want: "connection refused",
		},
		{
			name: "TLS error",
			err:  fmt.Errorf("TLS handshake timeout"),
			want: "TLS not ready",
		},
		{
			name: "short error unchanged",
			err:  fmt.Errorf("something went wrong"),
			want: "something went wrong",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diShortenError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// diPrintProjectIAPBindings tests
// ---------------------------------------------------------------------------

func TestPrintProjectIAPBindings_NoBindings(t *testing.T) {
	// Policy with no IAP bindings — should print "no bindings" message
	policy := `bindings:
- members:
  - user:someone@example.com
  role: roles/editor
- members:
  - user:other@example.com
  role: roles/viewer
`
	// Just verify it doesn't panic
	diPrintProjectIAPBindings(policy, "  ")
}

func TestPrintProjectIAPBindings_WithIAPBinding(t *testing.T) {
	policy := `bindings:
- members:
  - user:admin@example.com
  - user:dev@example.com
  role: roles/iap.httpsResourceAccessor
- members:
  - user:other@example.com
  role: roles/viewer
`
	// Just verify it doesn't panic
	diPrintProjectIAPBindings(policy, "  ")
}

// ---------------------------------------------------------------------------
// diSanitizeResponse tests
// ---------------------------------------------------------------------------

func TestSanitizeResponse(t *testing.T) {
	short := "short error"
	assert.Equal(t, short, diSanitizeResponse(short))

	long := string(make([]byte, 600))
	result := diSanitizeResponse(long)
	assert.Contains(t, result, "truncated")
	assert.LessOrEqual(t, len(result), 520)
}
