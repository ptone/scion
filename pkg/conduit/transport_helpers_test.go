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

package conduit

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/conduit/transport"
	conduitgrpc "github.com/GoogleCloudPlatform/scion/pkg/conduit/transport/grpc"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit/transport/ws"
)

// The session-level suite runs over one transport per test process,
// selected by envTestTransport: memory (the default), ws or grpc.
// TestTransportParity re-runs the whole suite over ws and grpc in child
// processes, so the same tests, unchanged, cover every transport.
const envTestTransport = "CONDUIT_TEST_TRANSPORT"

// testTransport returns the transport this test process runs the suite
// over.
func testTransport() string {
	if v := os.Getenv(envTestTransport); v != "" {
		return v
	}
	return transport.Memory
}

// testPipe returns two connected Conns over the transport under test: a
// is the dialing end, b the accepting end. On a real transport, a test
// that needs the in-memory pipe's synchronous buffer (Buffer 0: a write
// blocks until the peer reads, to play a peer that stopped reading) or
// its frame limit is skipped: kernel and HTTP/2 buffers make that peer
// impossible to script.
func testPipe(t testing.TB, opts transport.MemoryOptions) (a, b transport.Conn) {
	t.Helper()
	switch tr := testTransport(); tr {
	case transport.Memory:
		return transport.Pipe(opts)
	case transport.WS, transport.GRPC:
		if opts.Buffer == 0 || opts.MaxFrame > 0 {
			t.Skipf("needs the in-memory transport's synchronous buffer or frame limit (suite running over %s)", tr)
		}
		if tr == transport.WS {
			return wsPipe(t)
		}
		return grpcPipe(t)
	default:
		t.Fatalf("%s=%q: want memory, ws or grpc", envTestTransport, tr)
		return nil, nil
	}
}

// pipeFailed reports a pipe that could not be built. testPipe may run on
// a goroutine other than the test's (inside a DialerFunc), where
// t.Fatal is not allowed, so it records the error and returns a closed
// in-memory pair, which the session sees as a dead transport.
func pipeFailed(t testing.TB, what string, err error) (transport.Conn, transport.Conn) {
	t.Errorf("%s pipe: %v", what, err)
	a, b := transport.Pipe(transport.MemoryOptions{Buffer: 1})
	_ = a.Close()
	return a, b
}

// wsPipe connects a ws dialer to a ws server on loopback.
func wsPipe(t testing.TB) (transport.Conn, transport.Conn) {
	accepted := make(chan transport.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := ws.Upgrade(w, r, nil, ws.Options{})
		if err != nil {
			return
		}
		accepted <- c
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	d := &ws.Dialer{URL: "ws" + strings.TrimPrefix(srv.URL, "http")}
	a, err := d.Dial(ctx)
	if err != nil {
		return pipeFailed(t, "ws", err)
	}
	select {
	case b := <-accepted:
		t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
		return a, b
	case <-time.After(waitTimeout):
		_ = a.Close()
		return pipeFailed(t, "ws", context.DeadlineExceeded)
	}
}

// grpcPipe connects a grpc dialer to a conduit gRPC server on loopback
// (h2c).
func grpcPipe(t testing.TB) (transport.Conn, transport.Conn) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return pipeFailed(t, "grpc", err)
	}
	accepted := make(chan transport.Conn, 1)
	srv := conduitgrpc.NewServer(func(_ context.Context, c *conduitgrpc.ServerConn) error {
		if err := c.Accept(); err != nil {
			return err
		}
		accepted <- c
		<-c.Done()
		return nil
	})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	d := &conduitgrpc.Dialer{URL: "http://" + ln.Addr().String()}
	a, err := d.Dial(ctx)
	if err != nil {
		return pipeFailed(t, "grpc", err)
	}
	select {
	case b := <-accepted:
		t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
		return a, b
	case <-time.After(waitTimeout):
		_ = a.Close()
		return pipeFailed(t, "grpc", context.DeadlineExceeded)
	}
}
