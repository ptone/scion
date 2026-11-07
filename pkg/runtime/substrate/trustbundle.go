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
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// clusterTrustBundleV1Path is the collection path of the
// certificates.k8s.io/v1 ClusterTrustBundle resource.
const clusterTrustBundleV1Path = "/apis/certificates.k8s.io/v1/clustertrustbundles"

// ClusterTrustBundleError reports a ClusterTrustBundle that could not be
// used as a trust anchor. It names the bundle only, never its contents.
type ClusterTrustBundleError struct {
	// Name is the configured ClusterTrustBundle name.
	Name string
	// Reason says which step failed, in words an operator can act on.
	Reason string
	// Err is the underlying error, when there is one.
	Err error
}

func (e *ClusterTrustBundleError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("substrate: ClusterTrustBundle %q: %s: %v", e.Name, e.Reason, e.Err)
	}
	return fmt.Sprintf("substrate: ClusterTrustBundle %q: %s", e.Name, e.Reason)
}

func (e *ClusterTrustBundleError) Unwrap() error { return e.Err }

// ctbGetFunc reads the PEM trust bundle of the named ClusterTrustBundle
// from one API version.
type ctbGetFunc func(ctx context.Context, name string) (string, error)

// ctbLookup reads a ClusterTrustBundle from certificates.k8s.io/v1, falling
// back to v1beta1 only when v1 answers 404. It is the one lookup shared by
// the ateapi dialer and the router client. The two legs are fields so tests
// can drive each one on its own.
type ctbLookup struct {
	v1      ctbGetFunc
	v1beta1 ctbGetFunc
}

// newCTBLookup builds the production lookup over k8sClient.
func newCTBLookup(k8sClient kubernetes.Interface) ctbLookup {
	return ctbLookup{
		v1: func(ctx context.Context, name string) (string, error) {
			return getClusterTrustBundleV1(ctx, k8sClient, name)
		},
		v1beta1: func(ctx context.Context, name string) (string, error) {
			return getClusterTrustBundleV1beta1(ctx, k8sClient, name)
		},
	}
}

// get returns the named bundle's PEM. The fallback rule is narrow on
// purpose, because this decides which roots the broker trusts:
//
//   - v1 answers 200: its bundle is the answer. It is never replaced by
//     v1beta1, even when it turns out empty or invalid (the caller fails
//     closed on that).
//   - v1 answers 404: the group-version is not served or the object is
//     missing. v1beta1 is asked for the SAME name, so trust never widens
//     beyond that one named object.
//   - anything else from v1 (401, 403, other statuses, transport or decode
//     errors): fail closed, no fallback.
//   - any error from v1beta1, a 404 included: fail closed.
func (l ctbLookup) get(ctx context.Context, name string) (string, error) {
	if err := checkClusterTrustBundleName(name); err != nil {
		return "", err
	}
	bundle, err := l.v1(ctx, name)
	if err == nil {
		return bundle, nil
	}
	if !apierrors.IsNotFound(err) {
		return "", &ClusterTrustBundleError{Name: name, Reason: "certificates.k8s.io/v1 read failed (no fallback to v1beta1)", Err: err}
	}
	bundle, err = l.v1beta1(ctx, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", &ClusterTrustBundleError{Name: name, Reason: "not found in certificates.k8s.io/v1 or v1beta1", Err: err}
		}
		return "", &ClusterTrustBundleError{Name: name, Reason: "certificates.k8s.io/v1beta1 read failed (v1 answered 404)", Err: err}
	}
	return bundle, nil
}

// checkClusterTrustBundleName refuses a name that cannot be one path segment.
// ClusterTrustBundle names may contain ':' (for example
// "servicedns.podcert.ate.dev:identity:primary-bundle"), so this checks only
// what would change the request path.
func checkClusterTrustBundleName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\?#% \t\r\n") {
		return errors.New("substrate: cluster_trust_bundle is not a valid ClusterTrustBundle name")
	}
	return nil
}

// clusterTrustBundleV1 is the part of a certificates.k8s.io/v1
// ClusterTrustBundle the lookup reads. Unknown fields are deliberately
// ignored: a real object carries more metadata and spec fields (for
// example spec.signerName) than the lookup needs, and new fields must not
// break it. What the lookup relies on is checked strictly instead:
// apiVersion, kind, metadata.name and the presence of spec.trustBundle.
type clusterTrustBundleV1 struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		TrustBundle *string `json:"trustBundle"`
	} `json:"spec"`
}

// getClusterTrustBundleV1 is the certificates.k8s.io/v1 leg. It is a raw GET
// through the discovery REST client, so it does not depend on client-go
// shipping a typed v1 ClusterTrustBundle client. Keep it in this one
// function: moving to a typed client later changes nothing else.
//
// A non-2xx answer comes back as an apierrors status error (a 404 matches
// apierrors.IsNotFound, whether the group-version is not served or the
// object is missing). A 200 must decode as a v1 ClusterTrustBundle with
// the requested name and a spec.trustBundle string, or it is an error.
func getClusterTrustBundleV1(ctx context.Context, k8sClient kubernetes.Interface, name string) (string, error) {
	rc := k8sClient.Discovery().RESTClient()
	if rc == nil {
		return "", errors.New("no REST client for certificates.k8s.io/v1")
	}
	raw, err := rc.Get().
		AbsPath(clusterTrustBundleV1Path, name).
		SetHeader("Accept", "application/json").
		Do(ctx).
		Raw()
	if err != nil {
		return "", err
	}
	var obj clusterTrustBundleV1
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", errors.New("certificates.k8s.io/v1 answer is not a ClusterTrustBundle object")
	}
	if obj.APIVersion != "certificates.k8s.io/v1" || obj.Kind != "ClusterTrustBundle" {
		return "", errors.New("certificates.k8s.io/v1 answer is not a ClusterTrustBundle object")
	}
	if obj.Metadata.Name != name {
		return "", errors.New("certificates.k8s.io/v1 answer names a different ClusterTrustBundle")
	}
	if obj.Spec.TrustBundle == nil {
		return "", errors.New("certificates.k8s.io/v1 answer has no spec.trustBundle")
	}
	return *obj.Spec.TrustBundle, nil
}

// getClusterTrustBundleV1beta1 is the certificates.k8s.io/v1beta1 leg.
func getClusterTrustBundleV1beta1(ctx context.Context, k8sClient kubernetes.Interface, name string) (string, error) {
	ctb, err := k8sClient.CertificatesV1beta1().ClusterTrustBundles().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return ctb.Spec.TrustBundle, nil
}

// rootCAPool builds the pool that verifies a Substrate serving certificate,
// from exactly one configured source: the named ClusterTrustBundle when
// set, else the CA file. It never returns the system roots, and it refuses
// when neither source is configured or the source holds no certificate.
func rootCAPool(ctx context.Context, lookup ctbLookup, clusterTrustBundle, caFile string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	switch {
	case clusterTrustBundle != "":
		bundle, err := lookup.get(ctx, clusterTrustBundle)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(bundle) == "" {
			return nil, &ClusterTrustBundleError{Name: clusterTrustBundle, Reason: "trust bundle is empty"}
		}
		if !pool.AppendCertsFromPEM([]byte(bundle)) {
			return nil, &ClusterTrustBundleError{Name: clusterTrustBundle, Reason: "contains no valid certificates"}
		}
	case caFile != "":
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("substrate: read ca_file %q: %w", caFile, err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("substrate: ca_file %q contains no valid certificates", caFile)
		}
	default:
		return nil, errors.New("substrate: one of cluster_trust_bundle or ca_file must be configured; InsecureSkipVerify and the system roots are not supported")
	}
	return pool, nil
}
