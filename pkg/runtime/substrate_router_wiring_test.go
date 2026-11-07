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

package runtime

import (
	"errors"
	"strings"
	"testing"

	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/k8s"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
)

// stubSubstrateK8sClient replaces the production builder's Kubernetes
// client constructor for one test and returns its call counter.
func stubSubstrateK8sClient(t *testing.T, client *k8s.Client, err error) *int {
	t.Helper()
	calls := 0
	orig := newSubstrateK8sClient
	newSubstrateK8sClient = func() (*k8s.Client, error) {
		calls++
		return client, err
	}
	t.Cleanup(func() { newSubstrateK8sClient = orig })
	return &calls
}

// TestNewSubstrateRuntimeFromConfig_RouterEndpointRefusedFirst: the
// production builder refuses a router_endpoint that is not
// https://<host>:443 as an invalid profile, before any Kubernetes client is
// built (and so before any CA lookup or dial).
func TestNewSubstrateRuntimeFromConfig_RouterEndpointRefusedFirst(t *testing.T) {
	for _, endpoint := range []string{
		"http://atenet-router.ate-system.svc:80",
		"https://atenet-router.ate-system.svc:8443",
		"https://atenet-router.ate-system.svc",
		"atenet-router.ate-system.svc:443",
		"not a url",
	} {
		t.Run(endpoint, func(t *testing.T) {
			calls := stubSubstrateK8sClient(t, nil, errors.New("the Kubernetes client must not be built"))
			rt, err := newSubstrateRuntimeFromConfig(config.V1SubstrateConfig{
				APIEndpoint:    "api.ate-system.svc:443",
				RouterEndpoint: endpoint,
				CAFile:         "/nonexistent/ca.pem",
			})
			if rt != nil || err == nil {
				t.Fatalf("newSubstrateRuntimeFromConfig() = (%v, %v), want a refusal", rt, err)
			}
			if !errors.Is(err, ErrSubstrateProfileInvalid) {
				t.Errorf("error %v does not match ErrSubstrateProfileInvalid", err)
			}
			if !errors.Is(err, substrate.ErrInvalidRouterEndpoint) {
				t.Errorf("error %v does not match substrate.ErrInvalidRouterEndpoint", err)
			}
			if *calls != 0 {
				t.Errorf("Kubernetes client built %d time(s) before the router_endpoint refusal, want 0", *calls)
			}
		})
	}
}

// TestNewSubstrateRuntime_ProductionBuilderRefusesRouterEndpoint drives the
// refusal through NewSubstrateRuntime with the production builder, so the
// tagged error reaches the caller unwrapped (a default profile refuses boot).
func TestNewSubstrateRuntime_ProductionBuilderRefusesRouterEndpoint(t *testing.T) {
	resetSubstrateRuntimeRegistryForTest(t)
	origBuilder := substrateRuntimeBuilder
	substrateRuntimeBuilder = newSubstrateRuntimeFromConfig
	t.Cleanup(func() { substrateRuntimeBuilder = origBuilder })
	calls := stubSubstrateK8sClient(t, nil, errors.New("the Kubernetes client must not be built"))

	_, err := NewSubstrateRuntime(&config.V1SubstrateConfig{
		APIEndpoint:    "api.ate-system.svc:443",
		RouterEndpoint: "http://atenet-router.ate-system.svc:80",
	})
	if !errors.Is(err, ErrSubstrateProfileInvalid) || !errors.Is(err, substrate.ErrInvalidRouterEndpoint) {
		t.Fatalf("NewSubstrateRuntime() error = %v, want an invalid-profile router_endpoint refusal", err)
	}
	if *calls != 0 {
		t.Errorf("Kubernetes client built %d time(s), want 0", *calls)
	}
}

// TestNewSubstrateRuntimeFromConfig_RouterCertNotConfiguredRefuses: with a
// valid endpoint, the builder reaches the router client, which refuses
// because Phase 1c configures no client certificate paths. The refusal is
// an invalid profile and happens before any dial.
func TestNewSubstrateRuntimeFromConfig_RouterCertNotConfiguredRefuses(t *testing.T) {
	calls := stubSubstrateK8sClient(t, &k8s.Client{Clientset: kubefake.NewClientset()}, nil)
	rt, err := newSubstrateRuntimeFromConfig(config.V1SubstrateConfig{
		APIEndpoint:    "api.ate-system.svc:443",
		RouterEndpoint: "https://atenet-router.ate-system.svc:443",
		CAFile:         "/nonexistent/ca.pem",
	})
	if rt != nil || err == nil {
		t.Fatalf("newSubstrateRuntimeFromConfig() = (%v, %v), want a refusal", rt, err)
	}
	if !errors.Is(err, substrate.ErrRouterClientCertNotConfigured) {
		t.Errorf("error %v does not match substrate.ErrRouterClientCertNotConfigured", err)
	}
	if !errors.Is(err, ErrSubstrateProfileInvalid) {
		t.Errorf("error %v does not match ErrSubstrateProfileInvalid", err)
	}
	if !strings.Contains(err.Error(), "router client certificate not configured") {
		t.Errorf("error %q is not the actionable certificate message", err)
	}
	if *calls != 1 {
		t.Errorf("Kubernetes client built %d time(s), want 1", *calls)
	}
}

// TestNewSubstrateRuntimeFromConfig_K8sClientErrorUntagged: with a valid
// endpoint, a Kubernetes client failure stays a plain (possibly transient)
// error, not an invalid profile.
func TestNewSubstrateRuntimeFromConfig_K8sClientErrorUntagged(t *testing.T) {
	calls := stubSubstrateK8sClient(t, nil, errors.New("simulated API server unavailable"))
	_, err := newSubstrateRuntimeFromConfig(config.V1SubstrateConfig{
		APIEndpoint:    "api.ate-system.svc:443",
		RouterEndpoint: "https://atenet-router.ate-system.svc:443",
	})
	if err == nil || errors.Is(err, ErrSubstrateProfileInvalid) {
		t.Fatalf("error = %v, want an untagged Kubernetes client error", err)
	}
	if *calls != 1 {
		t.Errorf("Kubernetes client built %d time(s), want 1", *calls)
	}
}
