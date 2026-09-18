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

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/plugin/grpcbroker"
	"google.golang.org/grpc"
)

func TestLoadAndValidateBridgeStartupConfig(t *testing.T) {
	tests := []struct {
		name       string
		hubURL     string
		credential string
		cacheTTL   time.Duration
		wantErr    string
	}{
		{name: "valid defaults", hubURL: "https://hub.example.invalid", credential: "id_token"},
		{name: "missing Hub URL", credential: "id_token", wantErr: "hub.endpoint is required"},
		{name: "invalid Hub URL", hubURL: "://invalid", credential: "id_token", wantErr: "hub.endpoint must be an absolute HTTP(S) URL for geGoogle auth"},
		{name: "implicit credential type", hubURL: "https://hub.example.invalid", wantErr: "auth.ge_exchange.credential_type must be id_token or access_token"},
		{name: "unknown credential type", hubURL: "https://hub.example.invalid", credential: "opaque", wantErr: "auth.ge_exchange.credential_type must be id_token or access_token"},
		{name: "negative cache TTL", hubURL: "https://hub.example.invalid", credential: "id_token", cacheTTL: -time.Nanosecond, wantErr: "auth.ge_exchange.cache_ttl must be between 0 and 5m0s"},
		{name: "cache TTL above maximum", hubURL: "https://hub.example.invalid", credential: "id_token", cacheTTL: 5*time.Minute + time.Nanosecond, wantErr: "auth.ge_exchange.cache_ttl must be between 0 and 5m0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeBridgeConfig(t, tt.hubURL, tt.credential, tt.cacheTTL)
			cfg, err := loadConfig(path)
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			err = validateStartupConfig(cfg)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("validateStartupConfig error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateStartupConfig: %v", err)
			}
			if cfg.Timeouts.SendMessage != 120*time.Second || cfg.Timeouts.SSEKeepalive != 30*time.Second || cfg.Timeouts.PushRetryMax != 3 {
				t.Fatalf("loadConfig defaults = (%v, %v, %d), want (2m0s, 30s, 3)", cfg.Timeouts.SendMessage, cfg.Timeouts.SSEKeepalive, cfg.Timeouts.PushRetryMax)
			}
		})
	}
}

func TestResolveGRPCServerAuthProductionConfigurations(t *testing.T) {
	setGRPCAuthEnvironment(t)
	t.Setenv("GRPC_AUTH_MODE", string(grpcbroker.AuthModeGoogleIDToken))
	t.Setenv("GRPC_AUTH_AUDIENCE", "https://bridge.example.invalid")
	t.Setenv("GRPC_AUTH_SUBJECTS", " hub@example.invalid ")
	t.Setenv("GRPC_TLS_CERT", filepath.Join(t.TempDir(), "ignored-cert.pem"))

	cloudRun, err := resolveGRPCServerAuth(true, slog.Default())
	if err != nil {
		t.Fatalf("resolve Cloud Run gRPC auth: %v", err)
	}
	if cloudRun.ListenAddress != ":8080" || cloudRun.TLSCertFile != "" {
		t.Fatalf("Cloud Run config = address %q, cert %q; want :8080 with platform TLS", cloudRun.ListenAddress, cloudRun.TLSCertFile)
	}
	assertGRPCServerOptionsBuild(t, cloudRun)

	certFile, keyFile, caFile := writeTLSFixture(t)
	t.Setenv("GRPC_TLS_CERT", certFile)
	t.Setenv("GRPC_TLS_KEY", keyFile)
	t.Setenv("GRPC_TLS_CLIENT_CA", caFile)
	t.Setenv("GRPC_PORT", "50051")

	kubernetes, err := resolveGRPCServerAuth(false, slog.Default())
	if err != nil {
		t.Fatalf("resolve Kubernetes gRPC auth: %v", err)
	}
	if kubernetes.ListenAddress != ":50051" || kubernetes.TLSCertFile != certFile || kubernetes.TLSKeyFile != keyFile || kubernetes.TLSClientCAFile != caFile {
		t.Fatalf("Kubernetes config did not preserve listener and mTLS files: %+v", kubernetes)
	}
	assertGRPCServerOptionsBuild(t, kubernetes)
}

func TestResolveGRPCServerAuthRejectsInvalidProductionConfiguration(t *testing.T) {
	setGRPCAuthEnvironment(t)
	t.Setenv("GRPC_AUTH_MODE", string(grpcbroker.AuthModeGoogleIDToken))
	t.Setenv("GRPC_AUTH_SUBJECTS", "hub@example.invalid")

	_, err := resolveGRPCServerAuth(true, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "audience is required for google_id_token auth mode") {
		t.Fatalf("resolveGRPCServerAuth error = %v, want missing-audience validation error", err)
	}
}

func writeBridgeConfig(t *testing.T, hubURL, credential string, cacheTTL time.Duration) string {
	t.Helper()
	contents := fmt.Sprintf(`bridge:
  external_url: https://bridge.example.invalid
hub:
  endpoint: %q
  user: integration@example.invalid
auth:
  scheme: geGoogle
  ge_exchange:
    credential_type: %q
    cache_ttl: %q
`, hubURL, credential, cacheTTL.String())
	path := filepath.Join(t.TempDir(), "bridge.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func setGRPCAuthEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GRPC_AUTH_MODE", "GRPC_AUTH_AUDIENCE", "GRPC_AUTH_SUBJECTS", "GRPC_PORT", "PORT",
		"GRPC_TLS_CERT", "GRPC_TLS_KEY", "GRPC_TLS_CLIENT_CA",
	} {
		t.Setenv(name, "")
	}
}

func assertGRPCServerOptionsBuild(t *testing.T, cfg grpcbroker.StandaloneServerConfig) {
	t.Helper()
	options, err := grpcbroker.BuildStandaloneServerOptions(cfg)
	if err != nil {
		t.Fatalf("BuildStandaloneServerOptions: %v", err)
	}
	grpc.NewServer(options...).Stop()
}

func writeTLSFixture(t *testing.T) (string, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "bridge.example.invalid"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	directory := t.TempDir()
	certFile := filepath.Join(directory, "server.pem")
	keyFile := filepath.Join(directory, "server-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile, certFile
}
