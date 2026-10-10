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

package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	gogrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/GoogleCloudPlatform/scion/pkg/conduit/transport"
)

// flowWindow is the HTTP/2 stream and connection window the dialer
// advertises. Setting it explicitly also turns off grpc-go's BDP
// estimation, whose PINGs would otherwise be the dialer's only HTTP/2
// pings; the conduit session's own credit (4 MiB per session) stays the
// binding flow control.
const flowWindow = 8 << 20

// DialError reports a Session stream the server refused, or could not be
// opened at all. Code is the gRPC status code: a refusal by the hub, a
// status synthesised by grpc-go from an HTTP answer (401 is
// Unauthenticated, 403 PermissionDenied), or Unavailable for connection,
// TLS and HTTP/2 failures. The transport negotiation rules (§3.6) key off
// it: Unauthenticated and PermissionDenied are denials and never a reason
// to change transports.
type DialError struct {
	Code    codes.Code
	Message string
	Err     error
}

func (e *DialError) Error() string {
	return fmt.Sprintf("conduit/grpc: dial: %s: %s", e.Code, e.Message)
}

func (e *DialError) Unwrap() error { return e.Err }

// IsAuthDenial reports whether the stream was refused for authentication
// or policy reasons.
func (e *DialError) IsAuthDenial() bool {
	return e.Code == codes.Unauthenticated || e.Code == codes.PermissionDenied
}

func dialError(err error) *DialError {
	st := status.Convert(err)
	return &DialError{Code: st.Code(), Message: st.Message(), Err: err}
}

// Dialer opens Conduit.Session streams. It implements transport.Dialer.
// Every Dial uses its own client connection, so one session is one HTTP/2
// connection, as one session is one WebSocket on ws.
type Dialer struct {
	// URL is the gRPC endpoint: an https:// (TLS) or http:// (h2c)
	// origin. A path is refused: the request path is always
	// SessionMethod.
	URL string
	// Header returns the credentials for one attempt (the ws handshake
	// header names; they are sent as lower-cased metadata). It is called
	// on every Dial so rotated credentials are picked up.
	Header func(ctx context.Context) (http.Header, error)
	// TLS overrides the TLS client configuration of an https URL.
	TLS *tls.Config
	// DialOptions are appended to the dialer's options (tests: an
	// in-memory listener).
	DialOptions []gogrpc.DialOption
}

var _ transport.Dialer = (*Dialer)(nil)

// Target parses a conduit gRPC URL into a gRPC target (host:port) and
// whether it uses TLS.
func Target(raw string) (target string, useTLS bool, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false, fmt.Errorf("conduit/grpc: invalid endpoint %q: %w", raw, err)
	}
	port := "80"
	switch u.Scheme {
	case "https":
		useTLS, port = true, "443"
	case "http":
	default:
		return "", false, fmt.Errorf("conduit/grpc: endpoint %q: scheme must be https or http", raw)
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", false, fmt.Errorf("conduit/grpc: endpoint %q has no host", raw)
	}
	if p := strings.TrimSuffix(u.Path, "/"); p != "" || u.RawQuery != "" || u.User != nil {
		return "", false, fmt.Errorf("conduit/grpc: endpoint %q must be an origin (no path, query or user info)", raw)
	}
	if u.Port() != "" {
		port = u.Port()
	}
	return net.JoinHostPort(u.Hostname(), port), useTLS, nil
}

// ClientOptions are the dial options every conduit client connection
// uses: the pass-through codec, the message size limits and explicit
// flow-control windows. gRPC keepalive is not configured (off), so the
// dialer sends no HTTP/2 PINGs; conduit Ping/Pong frames are the liveness
// signal (§3.6.1 item 6).
func ClientOptions() []gogrpc.DialOption {
	return []gogrpc.DialOption{
		gogrpc.WithDefaultCallOptions(
			gogrpc.ForceCodecV2(frameCodec{}),
			gogrpc.MaxCallRecvMsgSize(MaxMessageSize),
			gogrpc.MaxCallSendMsgSize(MaxMessageSize),
		),
		gogrpc.WithInitialWindowSize(flowWindow),
		gogrpc.WithInitialConnWindowSize(flowWindow),
	}
}

var sessionStreamDesc = gogrpc.StreamDesc{
	StreamName:    "Session",
	ServerStreams: true,
	ClientStreams: true,
}

// Dial implements transport.Dialer. It returns once the server accepted
// the stream (its response headers arrived); a refusal is a *DialError.
// ctx bounds the dial only, not the stream.
func (d *Dialer) Dial(ctx context.Context) (transport.Conn, error) {
	target, useTLS, err := Target(d.URL)
	if err != nil {
		return nil, err
	}
	md := metadata.MD{}
	if d.Header != nil {
		h, err := d.Header(ctx)
		if err != nil {
			return nil, fmt.Errorf("conduit/grpc: stream headers: %w", err)
		}
		for k, vs := range h {
			md.Append(strings.ToLower(k), vs...)
		}
	}
	var creds credentials.TransportCredentials
	if useTLS {
		cfg := d.TLS
		if cfg == nil {
			cfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		creds = credentials.NewTLS(cfg)
	} else {
		creds = insecure.NewCredentials()
	}
	opts := append(ClientOptions(), gogrpc.WithTransportCredentials(creds))
	opts = append(opts, d.DialOptions...)
	cc, err := gogrpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("conduit/grpc: %w", err)
	}
	sctx, cancel := context.WithCancel(context.Background())
	end := func() {
		cancel()
		_ = cc.Close()
	}
	// The dial context bounds the handshake: until the headers arrive,
	// its end cancels the stream.
	accepted := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-accepted:
		}
	}()
	stream, err := cc.NewStream(metadata.NewOutgoingContext(sctx, md), &sessionStreamDesc, SessionMethod)
	if err != nil {
		close(accepted)
		end()
		return nil, dialFailure(ctx, err)
	}
	hdr, err := stream.Header()
	if err == nil && hdr == nil {
		// The stream ended without headers (a refusal): its status is
		// what RecvMsg returns.
		err = stream.RecvMsg(&rawFrame{})
		if err == nil {
			err = status.Error(codes.Internal, "conduit stream ended without headers")
		}
	}
	close(accepted)
	if err != nil {
		end()
		return nil, dialFailure(ctx, err)
	}
	c := &Conn{stream: stream, done: make(chan struct{})}
	c.closeFn = func(writing bool) {
		if writing {
			end()
			return
		}
		// Half-close after the frames already written; the server ends
		// the stream when it reads EOF. Cancel if it does not in time.
		_ = stream.CloseSend()
		time.AfterFunc(closeLinger, end)
	}
	return c, nil
}

// dialFailure prefers the dial context's error when it ended the dial.
func dialFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &DialError{Code: status.FromContextError(ctx.Err()).Code(), Message: ctx.Err().Error(), Err: ctx.Err()}
	}
	return dialError(err)
}
