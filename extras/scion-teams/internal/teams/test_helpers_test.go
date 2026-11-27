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

package teams

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testBotServiceURL = "https://smba.trafficmanager.net/amer"

// newTestBotConnectorServer starts a local TLS httptest.Server and returns an
// allowlisted Bot Framework service URL ("https://smba.trafficmanager.net/amer")
// alongside an *http.Client that dials the local test server. This exercises
// Sender's real service URL host/scheme validation without any bypass.
func newTestBotConnectorServer(t *testing.T, handler http.Handler) (string, *http.Client) {
	t.Helper()

	ts := httptest.NewTLSServer(handler)
	t.Cleanup(ts.Close)

	return testBotServiceURL, botConnectorClientForAddr(ts.Client(), ts.Listener.Addr().String())
}

func botConnectorClientForAddr(base *http.Client, addr string) *http.Client {
	tr := base.Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	return &http.Client{
		Transport:     tr,
		CheckRedirect: refuseRedirect,
	}
}
