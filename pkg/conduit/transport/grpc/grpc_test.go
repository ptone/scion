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

package grpc_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	_ "google.golang.org/grpc/encoding/proto" // the standard codec, as every gRPC user has it
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/GoogleCloudPlatform/scion/pkg/conduit/transport"
	conduitgrpc "github.com/GoogleCloudPlatform/scion/pkg/conduit/transport/grpc"
	conduitv1 "github.com/GoogleCloudPlatform/scion/proto/conduit/v1"
)

// serve starts a conduit gRPC server with h on a loopback port (h2c) and
// returns its http:// URL.
func serve(t *testing.T, h conduitgrpc.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := conduitgrpc.NewServer(h)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return "http://" + ln.Addr().String()
}

// echo accepts every stream and echoes frames until the stream ends.
func echo(_ context.Context, c *conduitgrpc.ServerConn) error {
	if err := c.Accept(); err != nil {
		return err
	}
	for {
		b, err := c.ReadFrame()
		if err != nil {
			return nil
		}
		if err := c.WriteFrame(b); err != nil {
			return nil
		}
	}
}

func dial(t *testing.T, url string, h http.Header) (transport.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := &conduitgrpc.Dialer{URL: url, Header: func(context.Context) (http.Header, error) { return h, nil }}
	return d.Dial(ctx)
}

func TestRoundTrip(t *testing.T) {
	url := serve(t, echo)
	c, err := dial(t, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if got := c.Transport(); got != transport.GRPC {
		t.Fatalf("Transport() = %q", got)
	}
	buf := []byte("frame-1")
	if err := c.WriteFrame(buf); err != nil {
		t.Fatal(err)
	}
	copy(buf, "XXXXXXX") // the slice may be reused once WriteFrame returns
	big := bytes.Repeat([]byte{7}, 700<<10)
	if err := c.WriteFrame(big); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{[]byte("frame-1"), big} {
		got, err := c.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("echo mismatch: %d bytes, want %d", len(got), len(want))
		}
	}
}

func TestFrameTooLarge(t *testing.T) {
	url := serve(t, echo)
	c, err := dial(t, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.WriteFrame(make([]byte, conduitgrpc.MaxMessageSize+1)); !errors.Is(err, transport.ErrFrameTooLarge) {
		t.Fatalf("WriteFrame = %v, want ErrFrameTooLarge", err)
	}
}

// TestMetadataCarriesHeaders checks that credentials travel as metadata
// under the lower-cased ws header names, and that Dial returns only after
// the server accepted.
func TestMetadataCarriesHeaders(t *testing.T) {
	got := make(chan metadata.MD, 1)
	url := serve(t, func(ctx context.Context, c *conduitgrpc.ServerConn) error {
		md, _ := metadata.FromIncomingContext(ctx)
		got <- md
		return echo(ctx, c)
	})
	h := http.Header{}
	h.Set("X-Scion-Agent-Token", "tok")
	h.Set("X-Scion-Run-Id", "run-1")
	h.Set("Proxy-Authorization", "Bearer platform")
	c, err := dial(t, url, h)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	md := <-got
	for k, want := range map[string]string{"x-scion-agent-token": "tok", "x-scion-run-id": "run-1", "proxy-authorization": "Bearer platform"} {
		if v := md.Get(k); len(v) != 1 || v[0] != want {
			t.Errorf("metadata %s = %v, want %q", k, v, want)
		}
	}
	if v := md.Get("authorization"); len(v) != 0 {
		t.Errorf("authorization metadata = %v, want none", v)
	}
}

// TestRefusalIsDialError checks that a status returned before Accept is a
// *DialError carrying the code, and that only Unauthenticated and
// PermissionDenied count as denials.
func TestRefusalIsDialError(t *testing.T) {
	for _, tc := range []struct {
		code   codes.Code
		denial bool
	}{
		{codes.Unauthenticated, true},
		{codes.PermissionDenied, true},
		{codes.Unavailable, false},
		{codes.ResourceExhausted, false},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			url := serve(t, func(context.Context, *conduitgrpc.ServerConn) error {
				return status.Error(tc.code, "refused")
			})
			_, err := dial(t, url, nil)
			var de *conduitgrpc.DialError
			if !errors.As(err, &de) {
				t.Fatalf("Dial = %v, want *DialError", err)
			}
			if de.Code != tc.code || de.IsAuthDenial() != tc.denial {
				t.Fatalf("DialError code %s denial %v, want %s %v", de.Code, de.IsAuthDenial(), tc.code, tc.denial)
			}
		})
	}
}

// TestHandlerErrorIsGeneric checks that a non-status handler error is not
// sent to the dialer.
func TestHandlerErrorIsGeneric(t *testing.T) {
	url := serve(t, func(context.Context, *conduitgrpc.ServerConn) error {
		return errors.New("secret detail")
	})
	_, err := dial(t, url, nil)
	var de *conduitgrpc.DialError
	if !errors.As(err, &de) || de.Code != codes.Internal || de.Message != "conduit session failed" {
		t.Fatalf("Dial = %v, want generic Internal", err)
	}
}

func TestDialUnreachableIsUnavailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	_ = ln.Close()
	_, err = dial(t, url, nil)
	var de *conduitgrpc.DialError
	if !errors.As(err, &de) || de.Code != codes.Unavailable || de.IsAuthDenial() {
		t.Fatalf("Dial = %v, want Unavailable", err)
	}
}

// TestClientCloseEndsServerStream checks that a client Close delivers the
// frames written before it and ends the server's read with an error.
func TestClientCloseEndsServerStream(t *testing.T) {
	ended := make(chan []string, 1)
	url := serve(t, func(_ context.Context, c *conduitgrpc.ServerConn) error {
		if err := c.Accept(); err != nil {
			return err
		}
		var got []string
		for {
			b, err := c.ReadFrame()
			if err != nil {
				ended <- got
				return nil
			}
			got = append(got, string(b))
		}
	})
	c, err := dial(t, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.WriteFrame([]byte("last"))
	_ = c.Close()
	select {
	case got := <-ended:
		if len(got) != 1 || got[0] != "last" {
			t.Fatalf("server read %v, want [last]", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server stream did not end")
	}
	if _, err := c.ReadFrame(); err == nil {
		t.Fatal("ReadFrame after Close succeeded")
	}
}

// TestServerCloseUnblocksBothEnds checks that a server Close returns from
// the RPC while its read is blocked, the frame written before it reaches
// the client, and the client then reads an end of stream.
func TestServerCloseUnblocksBothEnds(t *testing.T) {
	readErr := make(chan error, 1)
	url := serve(t, func(_ context.Context, c *conduitgrpc.ServerConn) error {
		if err := c.Accept(); err != nil {
			return err
		}
		go func() {
			_, err := c.ReadFrame()
			readErr <- err
		}()
		_ = c.WriteFrame([]byte("bye"))
		_ = c.Close()
		<-c.Done()
		return nil
	})
	c, err := dial(t, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	b, err := c.ReadFrame()
	if err != nil || string(b) != "bye" {
		t.Fatalf("ReadFrame = %q, %v", b, err)
	}
	if _, err := c.ReadFrame(); err == nil {
		t.Fatal("client read after server close succeeded")
	}
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("server read returned no error after Close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server read not unblocked by Close")
	}
}

func TestTarget(t *testing.T) {
	for _, tc := range []struct {
		in     string
		target string
		tls    bool
		bad    bool
	}{
		{in: "https://hub.example.com", target: "hub.example.com:443", tls: true},
		{in: "https://hub.example.com/", target: "hub.example.com:443", tls: true},
		{in: "http://localhost:9811", target: "localhost:9811"},
		{in: "https://[::1]:8443", target: "[::1]:8443", tls: true},
		{in: "wss://hub.example.com", bad: true},
		{in: "https://hub.example.com/api", bad: true},
		{in: "https://hub.example.com?x=1", bad: true},
		{in: "https://", bad: true},
	} {
		target, useTLS, err := conduitgrpc.Target(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("Target(%q) succeeded, want error", tc.in)
			}
			continue
		}
		if err != nil || target != tc.target || useTLS != tc.tls {
			t.Errorf("Target(%q) = %q, %v, %v; want %q, %v", tc.in, target, useTLS, err, tc.target, tc.tls)
		}
	}
}

// TestCodecNotRegisteredGlobally checks that the conduit codec does not
// replace the process-wide "proto" codec other gRPC users rely on.
func TestCodecNotRegisteredGlobally(t *testing.T) {
	_ = conduitgrpc.NewServer(echo) // building a server must not register anything
	c := encoding.GetCodecV2("proto")
	if c == nil {
		t.Fatal("no proto codec registered")
	}
	f := &conduitv1.Frame{Body: &conduitv1.Frame_Ping{Ping: &conduitv1.Ping{Nonce: 9}}}
	bs, err := c.Marshal(f)
	if err != nil {
		t.Fatalf("global proto codec cannot marshal a proto message: %v", err)
	}
	want, _ := proto.Marshal(f)
	if !bytes.Equal(bs.Materialize(), want) {
		t.Fatal("global proto codec output differs from proto.Marshal")
	}
}
