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

package k8s

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const explicitTestKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://cluster.example:6443
    certificate-authority: certs/ca.crt
users:
- name: u
  user:
    token: t
contexts:
- name: ctx-a
  context:
    cluster: c
    user: u
- name: ctx-b
  context:
    cluster: c
    user: u
current-context: ctx-a
`

// simulatePod makes the in-cluster environment variables present, so a
// loader that could fall back to in-cluster credentials would try to.
func simulatePod(t *testing.T) {
	t.Helper()
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
}

func TestNewClientFromKubeconfigFile(t *testing.T) {
	simulatePod(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.kubeconfig")
	if err := os.WriteFile(path, []byte(explicitTestKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestCA(t, dir)

	c, err := NewClientFromKubeconfigFile(path, "")
	if err != nil {
		t.Fatalf("valid file: %v", err)
	}
	if c.CurrentContext != "ctx-a" {
		t.Errorf("empty context: got %q, want the file's current-context ctx-a", c.CurrentContext)
	}
	if want := filepath.Join(dir, "certs", "ca.crt"); c.Config.CAFile != want {
		t.Errorf("relative CA path: got %q, want %q (resolved against the file's directory)", c.Config.CAFile, want)
	}
	if c.Config.Host != "https://cluster.example:6443" {
		t.Errorf("host = %q", c.Config.Host)
	}

	c, err = NewClientFromKubeconfigFile(path, "ctx-b")
	if err != nil || c.CurrentContext != "ctx-b" {
		t.Fatalf("explicit context: %v %+v", err, c)
	}
	if _, err := NewClientFromKubeconfigFile(path, "no-such-context"); err == nil {
		t.Error("an unknown context must fail")
	}
}

// TestNewClientFromKubeconfigFile_NoFallback: an empty, missing or
// contextless explicit file is an error even where in-cluster credentials
// look available; nothing is loaded from another source.
func TestNewClientFromKubeconfigFile_NoFallback(t *testing.T) {
	simulatePod(t)
	dir := t.TempDir()
	// A valid default source is available too.
	writeTestCA(t, dir)
	defaultPath := filepath.Join(dir, "default.kubeconfig")
	if err := os.WriteFile(defaultPath, []byte(explicitTestKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", defaultPath)

	empty := filepath.Join(dir, "empty.kubeconfig")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	contextless := filepath.Join(dir, "contextless.kubeconfig")
	noCurrent := explicitTestKubeconfig[:len(explicitTestKubeconfig)-len("current-context: ctx-a\n")]
	if err := os.WriteFile(contextless, []byte(noCurrent), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"zero-byte file":     empty,
		"no current-context": contextless,
		"missing file":       filepath.Join(dir, "missing.kubeconfig"),
	} {
		t.Run(name, func(t *testing.T) {
			c, err := NewClientFromKubeconfigFile(path, "")
			if err == nil {
				t.Fatalf("got a client (context %q, host %q); want an error with no fallback", c.CurrentContext, c.Config.Host)
			}
		})
	}
}

// writeTestCA writes the CA file the test kubeconfig names relatively.
func writeTestCA(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "certs"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "certs", "ca.crt"), ca, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestNewClientFromKubeconfigFile_NoADCSubstitution: when the explicit
// file's exec credential plugin fails on GCE, Verify refuses instead of
// switching to Application Default Credentials.
func TestNewClientFromKubeconfigFile_NoADCSubstitution(t *testing.T) {
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:1") // metadata.OnGCE reports true
	dir := t.TempDir()
	path := filepath.Join(dir, "exec.kubeconfig")
	content := `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:1
    insecure-skip-tls-verify: true
users:
- name: u
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: /bin/false
      interactiveMode: Never
contexts:
- name: ctx
  context:
    cluster: c
    user: u
current-context: ctx
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewClientFromKubeconfigFile(path, "")
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	err = c.Verify()
	if err == nil {
		t.Fatal("Verify must refuse when the explicit file's credential plugin fails")
	}
	if !strings.Contains(err.Error(), "no other credential source is used") {
		t.Errorf("want the explicit-file refusal, got: %v", err)
	}
	if strings.Contains(err.Error(), "Application Default Credentials (ADC) auth fallback") {
		t.Errorf("the ADC fallback was attempted: %v", err)
	}
}
