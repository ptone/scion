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

package substrate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	certsv1beta1 "k8s.io/api/certificates/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

const (
	testCTBName    = "servicedns.podcert.ate.dev:identity:primary-bundle"
	ctbV1ObjPath   = "/apis/certificates.k8s.io/v1/clustertrustbundles/" + testCTBName
	ctbBetaObjPath = "/apis/certificates.k8s.io/v1beta1/clustertrustbundles/" + testCTBName
)

var ctbGroupResource = schema.GroupResource{Group: "certificates.k8s.io", Resource: "clustertrustbundles"}

// v1NotServed is a v1 leg that answers 404, as a cluster serving no
// certificates.k8s.io/v1 ClusterTrustBundle does.
func v1NotServed(_ context.Context, name string) (string, error) {
	return "", apierrors.NewNotFound(ctbGroupResource, name)
}

// lookupV1NotServed is the shared lookup with v1 answering 404 and v1beta1
// read from client (normally a fake clientset).
func lookupV1NotServed(client kubernetes.Interface) ctbLookup {
	return ctbLookup{
		v1: v1NotServed,
		v1beta1: func(ctx context.Context, name string) (string, error) {
			return getClusterTrustBundleV1beta1(ctx, client, name)
		},
	}
}

// apiReply is one canned API server answer.
type apiReply struct {
	status int
	body   string
	// contentType defaults to application/json.
	contentType string
}

// apiStub is an httptest Kubernetes API server answering fixed paths, so the
// v1 leg runs through a real clientset and real HTTP status codes. It counts
// requests per path.
type apiStub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	replies map[string]apiReply
	hits    map[string]int
}

func newAPIStub(t *testing.T, replies map[string]apiReply) *apiStub {
	t.Helper()
	s := &apiStub{replies: replies, hits: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		reply, ok := s.replies[r.URL.Path]
		s.mu.Unlock()
		if !ok {
			// What an API server answers for a path it does not serve.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 page not found\n"))
			return
		}
		ct := reply.contentType
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *apiStub) clientset(t *testing.T) kubernetes.Interface {
	t.Helper()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: s.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func (s *apiStub) hitCount(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func ctbObjectJSON(t *testing.T, apiVersion, name string, bundle *string) string {
	t.Helper()
	spec := map[string]interface{}{}
	if bundle != nil {
		spec["trustBundle"] = *bundle
	}
	b, err := json.Marshal(map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       "ClusterTrustBundle",
		"metadata":   map[string]interface{}{"name": name},
		"spec":       spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func statusJSON(t *testing.T, code int, reason metav1.StatusReason) string {
	t.Helper()
	b, err := json.Marshal(metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure,
		Message:  string(reason),
		Reason:   reason,
		Code:     int32(code),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func strPtr(s string) *string { return &s }

// v1WithExtraFields is a v1 ClusterTrustBundle carrying fields the lookup
// does not read, as a real API server returns it.
func v1WithExtraFields(t *testing.T, bundle string) string {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{
		"apiVersion": "certificates.k8s.io/v1",
		"kind":       "ClusterTrustBundle",
		"metadata": map[string]interface{}{
			"name":            testCTBName,
			"uid":             "00000000-0000-0000-0000-000000000001",
			"resourceVersion": "42",
			"labels":          map[string]string{"podcert.ate.dev/canarying": "live"},
		},
		"spec": map[string]interface{}{
			"signerName":  "servicedns.podcert.ate.dev/identity",
			"trustBundle": bundle,
			"futureField": true,
		},
		"status": map[string]interface{}{"unknown": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestCTBLookup_FallbackTable drives the shared lookup through a real
// clientset against an API stub, one row per fallback rule.
func TestCTBLookup_FallbackTable(t *testing.T) {
	caPEM := string(generateTestCAPEM(t))
	otherPEM := string(generateTestCAPEM(t))

	v1OK := func(bundle *string) apiReply {
		return apiReply{status: http.StatusOK, body: ctbObjectJSON(t, "certificates.k8s.io/v1", testCTBName, bundle)}
	}
	betaOK := func(bundle string) apiReply {
		return apiReply{status: http.StatusOK, body: ctbObjectJSON(t, "certificates.k8s.io/v1beta1", testCTBName, &bundle)}
	}
	status := func(code int, reason metav1.StatusReason) apiReply {
		return apiReply{status: code, body: statusJSON(t, code, reason)}
	}

	tests := []struct {
		name     string
		replies  map[string]apiReply
		wantPEM  string // "" means an error is wanted
		wantBeta int    // v1beta1 requests expected
	}{
		{
			name:     "v1 200 valid bundle is used, no fallback",
			replies:  map[string]apiReply{ctbV1ObjPath: v1OK(&caPEM), ctbBetaObjPath: betaOK(otherPEM)},
			wantPEM:  caPEM,
			wantBeta: 0,
		},
		{
			name: "v1 200 with unknown fields is used (unknown fields are ignored deliberately)",
			replies: map[string]apiReply{
				ctbV1ObjPath:   {status: http.StatusOK, body: v1WithExtraFields(t, caPEM)},
				ctbBetaObjPath: betaOK(otherPEM),
			},
			wantPEM:  caPEM,
			wantBeta: 0,
		},
		{
			name:     "v1 200 empty bundle fails closed, never reaches v1beta1",
			replies:  map[string]apiReply{ctbV1ObjPath: v1OK(strPtr("")), ctbBetaObjPath: betaOK(caPEM)},
			wantBeta: 0,
		},
		{
			name:     "v1 200 unparseable bundle fails closed, never reaches v1beta1",
			replies:  map[string]apiReply{ctbV1ObjPath: v1OK(strPtr("not a certificate")), ctbBetaObjPath: betaOK(caPEM)},
			wantBeta: 0,
		},
		{
			name:     "v1 200 with no spec.trustBundle fails closed",
			replies:  map[string]apiReply{ctbV1ObjPath: v1OK(nil), ctbBetaObjPath: betaOK(caPEM)},
			wantBeta: 0,
		},
		{
			name:     "v1 200 that is not JSON fails closed",
			replies:  map[string]apiReply{ctbV1ObjPath: {status: http.StatusOK, body: "{not json"}, ctbBetaObjPath: betaOK(caPEM)},
			wantBeta: 0,
		},
		{
			name: "v1 200 naming another object fails closed",
			replies: map[string]apiReply{
				ctbV1ObjPath:   {status: http.StatusOK, body: ctbObjectJSON(t, "certificates.k8s.io/v1", "other", &caPEM)},
				ctbBetaObjPath: betaOK(caPEM),
			},
			wantBeta: 0,
		},
		{
			name: "v1 200 of another kind fails closed",
			replies: map[string]apiReply{
				ctbV1ObjPath:   {status: http.StatusOK, body: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"` + testCTBName + `"},"spec":{"trustBundle":"x"}}`},
				ctbBetaObjPath: betaOK(caPEM),
			},
			wantBeta: 0,
		},
		{
			name:     "v1 404 object missing falls back to v1beta1",
			replies:  map[string]apiReply{ctbV1ObjPath: status(http.StatusNotFound, metav1.StatusReasonNotFound), ctbBetaObjPath: betaOK(caPEM)},
			wantPEM:  caPEM,
			wantBeta: 1,
		},
		{
			name:     "v1 not served (plain 404) falls back to v1beta1",
			replies:  map[string]apiReply{ctbBetaObjPath: betaOK(caPEM)},
			wantPEM:  caPEM,
			wantBeta: 1,
		},
		{
			name:     "v1 404 then v1beta1 404 fails closed",
			replies:  map[string]apiReply{ctbV1ObjPath: status(http.StatusNotFound, metav1.StatusReasonNotFound), ctbBetaObjPath: status(http.StatusNotFound, metav1.StatusReasonNotFound)},
			wantBeta: 1,
		},
		{
			name:     "v1 404 then v1beta1 403 fails closed",
			replies:  map[string]apiReply{ctbBetaObjPath: status(http.StatusForbidden, metav1.StatusReasonForbidden)},
			wantBeta: 1,
		},
		{
			name:     "v1 404 then v1beta1 200 empty bundle fails closed",
			replies:  map[string]apiReply{ctbBetaObjPath: betaOK("")},
			wantBeta: 1,
		},
		{
			name:     "v1 403 fails closed, no fallback",
			replies:  map[string]apiReply{ctbV1ObjPath: status(http.StatusForbidden, metav1.StatusReasonForbidden), ctbBetaObjPath: betaOK(caPEM)},
			wantBeta: 0,
		},
		{
			name:     "v1 401 fails closed, no fallback",
			replies:  map[string]apiReply{ctbV1ObjPath: status(http.StatusUnauthorized, metav1.StatusReasonUnauthorized), ctbBetaObjPath: betaOK(caPEM)},
			wantBeta: 0,
		},
		{
			name:     "v1 500 fails closed, no fallback",
			replies:  map[string]apiReply{ctbV1ObjPath: status(http.StatusInternalServerError, metav1.StatusReasonInternalError), ctbBetaObjPath: betaOK(caPEM)},
			wantBeta: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newAPIStub(t, tt.replies)
			lookup := newCTBLookup(stub.clientset(t))

			pool, err := rootCAPool(context.Background(), lookup, testCTBName, "")
			if tt.wantPEM != "" {
				if err != nil {
					t.Fatalf("rootCAPool() error = %v, want success", err)
				}
				want, werr := rootCAPool(context.Background(), ctbLookup{v1: func(context.Context, string) (string, error) { return tt.wantPEM, nil }}, testCTBName, "")
				if werr != nil {
					t.Fatal(werr)
				}
				if !pool.Equal(want) {
					t.Error("rootCAPool() pool is not exactly the expected bundle")
				}
			} else {
				if err == nil {
					t.Fatal("rootCAPool() succeeded, want it to fail closed")
				}
				var ctbErr *ClusterTrustBundleError
				if !errors.As(err, &ctbErr) || ctbErr.Name != testCTBName {
					t.Errorf("rootCAPool() error = %v, want a *ClusterTrustBundleError naming the bundle", err)
				}
				if strings.Contains(err.Error(), "BEGIN CERTIFICATE") {
					t.Error("error carries bundle contents")
				}
			}
			if got := stub.hitCount(ctbBetaObjPath); got != tt.wantBeta {
				t.Errorf("v1beta1 requests = %d, want %d", got, tt.wantBeta)
			}
			if got := stub.hitCount(ctbV1ObjPath); got != 1 {
				t.Errorf("v1 requests = %d, want 1", got)
			}
		})
	}
}

// TestCTBLookup_V1TransportErrorFailsClosed: a v1 leg that cannot reach the
// API server fails closed and never tries v1beta1.
func TestCTBLookup_V1TransportErrorFailsClosed(t *testing.T) {
	stub := newAPIStub(t, nil)
	cs := stub.clientset(t)
	stub.srv.Close()

	betaCalls := 0
	lookup := newCTBLookup(cs)
	lookup.v1beta1 = func(context.Context, string) (string, error) {
		betaCalls++
		return string(generateTestCAPEM(t)), nil
	}
	if _, err := rootCAPool(context.Background(), lookup, testCTBName, ""); err == nil {
		t.Fatal("rootCAPool() succeeded, want a transport error to fail closed")
	}
	if betaCalls != 0 {
		t.Errorf("v1beta1 called %d times after a v1 transport error, want 0", betaCalls)
	}
}

// TestCTBLookup_V1beta1FakeClientset covers the v1beta1 leg through the
// client-go fake clientset, behind a v1 leg that answers 404.
func TestCTBLookup_V1beta1FakeClientset(t *testing.T) {
	caPEM := generateTestCAPEM(t)

	t.Run("present", func(t *testing.T) {
		client := kubefake.NewClientset(&certsv1beta1.ClusterTrustBundle{
			ObjectMeta: metav1.ObjectMeta{Name: testCTBName},
			Spec:       certsv1beta1.ClusterTrustBundleSpec{TrustBundle: string(caPEM)},
		})
		if _, err := rootCAPool(context.Background(), lookupV1NotServed(client), testCTBName, ""); err != nil {
			t.Fatalf("rootCAPool() error = %v", err)
		}
	})
	t.Run("missing fails closed", func(t *testing.T) {
		client := kubefake.NewClientset()
		_, err := rootCAPool(context.Background(), lookupV1NotServed(client), testCTBName, "")
		var ctbErr *ClusterTrustBundleError
		if !errors.As(err, &ctbErr) || !apierrors.IsNotFound(err) {
			t.Fatalf("rootCAPool() error = %v, want a not-found *ClusterTrustBundleError", err)
		}
	})
	t.Run("forbidden fails closed", func(t *testing.T) {
		client := kubefake.NewClientset()
		client.PrependReactor("get", "clustertrustbundles", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
			return true, nil, apierrors.NewForbidden(ctbGroupResource, testCTBName, errors.New("denied"))
		})
		_, err := rootCAPool(context.Background(), lookupV1NotServed(client), testCTBName, "")
		if err == nil || !apierrors.IsForbidden(err) {
			t.Fatalf("rootCAPool() error = %v, want a forbidden error", err)
		}
	})
	t.Run("empty bundle fails closed", func(t *testing.T) {
		client := kubefake.NewClientset(&certsv1beta1.ClusterTrustBundle{
			ObjectMeta: metav1.ObjectMeta{Name: testCTBName},
		})
		if _, err := rootCAPool(context.Background(), lookupV1NotServed(client), testCTBName, ""); err == nil {
			t.Fatal("rootCAPool() succeeded on an empty v1beta1 bundle, want failure")
		}
	})
}

// TestCTBLookup_V1OutcomesNeverReachV1beta1Fake: a v1 200 whose bundle does
// not parse, or a v1 403/401, leaves the v1beta1 fake clientset untouched.
func TestCTBLookup_V1OutcomesNeverReachV1beta1Fake(t *testing.T) {
	caPEM := generateTestCAPEM(t)
	v1Results := map[string]func(context.Context, string) (string, error){
		"200 unparseable": func(context.Context, string) (string, error) { return "not a certificate", nil },
		"200 empty":       func(context.Context, string) (string, error) { return "", nil },
		"403": func(_ context.Context, name string) (string, error) {
			return "", apierrors.NewForbidden(ctbGroupResource, name, errors.New("denied"))
		},
		"401": func(context.Context, string) (string, error) {
			return "", apierrors.NewUnauthorized("no credentials")
		},
	}
	for name, v1 := range v1Results {
		t.Run(name, func(t *testing.T) {
			client := kubefake.NewClientset(&certsv1beta1.ClusterTrustBundle{
				ObjectMeta: metav1.ObjectMeta{Name: testCTBName},
				Spec:       certsv1beta1.ClusterTrustBundleSpec{TrustBundle: string(caPEM)},
			})
			lookup := lookupV1NotServed(client)
			lookup.v1 = v1
			if _, err := rootCAPool(context.Background(), lookup, testCTBName, ""); err == nil {
				t.Fatal("rootCAPool() succeeded, want it to fail closed")
			}
			if n := len(client.Actions()); n != 0 {
				t.Errorf("v1beta1 fake saw %d action(s), want 0", n)
			}
		})
	}
}

// TestCTBLookup_FakeClientsetV1LegFailsClosed: the fake clientset has no
// discovery REST client, so the production v1 leg cannot answer; that is a
// failure, not a reason to fall back.
func TestCTBLookup_FakeClientsetV1LegFailsClosed(t *testing.T) {
	client := kubefake.NewClientset(&certsv1beta1.ClusterTrustBundle{
		ObjectMeta: metav1.ObjectMeta{Name: testCTBName},
		Spec:       certsv1beta1.ClusterTrustBundleSpec{TrustBundle: string(generateTestCAPEM(t))},
	})
	if _, err := rootCAPool(context.Background(), newCTBLookup(client), testCTBName, ""); err == nil {
		t.Fatal("rootCAPool() succeeded without a v1 answer, want failure")
	}
	if n := len(client.Actions()); n != 0 {
		t.Errorf("v1beta1 fake saw %d action(s), want 0", n)
	}
}

func TestCTBLookup_RefusesPathChangingName(t *testing.T) {
	for _, name := range []string{"a/b", "..", ".", "a?b", "a#b", "a%2fb", "a b"} {
		called := false
		lookup := ctbLookup{
			v1:      func(context.Context, string) (string, error) { called = true; return "", nil },
			v1beta1: func(context.Context, string) (string, error) { called = true; return "", nil },
		}
		if _, err := lookup.get(context.Background(), name); err == nil {
			t.Errorf("get(%q) succeeded, want a refusal", name)
		}
		if called {
			t.Errorf("get(%q) reached a leg", name)
		}
	}
}

func TestRootCAPool_NoSourceRefusesWithoutSystemRoots(t *testing.T) {
	pool, err := rootCAPool(context.Background(), ctbLookup{}, "", "")
	if err == nil || pool != nil {
		t.Fatalf("rootCAPool() = (%v, %v), want a refusal and no pool", pool, err)
	}
}
