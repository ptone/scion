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

// Package grpc is the conduit "grpc" transport (design §3.6, §3.6.1): one
// encoded Frame per message of the bidirectional stream
//
//	service Conduit { rpc Session(stream Frame) returns (stream Frame); }
//
// (package scion.conduit.v1, documented in proto/conduit/v1/conduit.proto).
// The service is registered from a hand-written grpc.ServiceDesc with a
// pass-through codec, so frames are not decoded and re-encoded here; the
// wire format is that of a generated stub.
//
// Credentials travel as request metadata using the ws header names
// (lower-cased). Liveness is the conduit Ping/Pong frames: the dialer
// turns gRPC keepalive off and disables BDP pings, because HTTP/2 PING is
// hop-by-hop and does not reach the hub through a proxy (§3.6.1 item 6).
package grpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	gogrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/GoogleCloudPlatform/scion/pkg/conduit/transport"
	"github.com/GoogleCloudPlatform/scion/pkg/wsprotocol"
)

const (
	// ServiceName is the fully qualified gRPC service name.
	ServiceName = "scion.conduit.v1.Conduit"
	// SessionMethod is the full method name of Conduit.Session, which is
	// also the HTTP/2 :path of every conduit gRPC request.
	SessionMethod = "/" + ServiceName + "/Session"
)

// MaxMessageSize bounds one message in each direction: the ws limit (room
// for a 768 KiB RPC body plus headers, far above the 64 KiB data frame).
const MaxMessageSize = wsprotocol.DefaultMaxMessageSize

// Conn adapts one Conduit.Session stream to transport.Conn. ReadFrame and
// WriteFrame follow the transport.Conn rules: one reader goroutine, one
// writer goroutine, Close from anywhere.
type Conn struct {
	stream gogrpc.Stream
	// closeFn ends the stream (server: return from the handler; client:
	// half-close, then cancel).
	closeFn func(writing bool)

	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
}

var _ transport.Conn = (*Conn)(nil)

// ReadFrame implements transport.Conn.
func (c *Conn) ReadFrame() ([]byte, error) {
	var f rawFrame
	if err := c.stream.RecvMsg(&f); err != nil {
		return nil, c.mapErr(err)
	}
	return f.b, nil
}

// WriteFrame implements transport.Conn. The frame is copied before
// WriteFrame returns, so b may be reused.
func (c *Conn) WriteFrame(b []byte) error {
	if len(b) > MaxMessageSize {
		return transport.ErrFrameTooLarge
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return transport.ErrClosed
	default:
	}
	if err := c.stream.SendMsg(&rawFrame{b: b}); err != nil {
		return c.mapErr(err)
	}
	return nil
}

// Close implements transport.Conn. It is idempotent and unblocks a
// pending ReadFrame or WriteFrame. Frames written before Close (the final
// GoAway of a session close, §3.3.1) are delivered before the stream ends
// unless a write is stuck at the time.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		// A write in flight (a stalled peer) means the stream cannot be
		// half-closed cleanly; closeFn then ends it at once.
		writing := !c.writeMu.TryLock()
		close(c.done)
		c.closeFn(writing)
		if !writing {
			c.writeMu.Unlock()
		}
	})
	return nil
}

// Done is closed once Close has been called.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Transport implements transport.Conn.
func (c *Conn) Transport() string { return transport.GRPC }

// Context returns the stream's context: on the server it carries the
// request metadata and the peer.
func (c *Conn) Context() context.Context { return c.stream.Context() }

// mapErr maps a stream error to the transport errors the session knows.
func (c *Conn) mapErr(err error) error {
	select {
	case <-c.done:
		return transport.ErrClosed
	default:
	}
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	if status.Code(err) == codes.ResourceExhausted {
		return transport.ErrFrameTooLarge
	}
	return err
}

// --------------------------------------------------------------- server

// Handler serves one Conduit.Session stream. It authenticates the caller
// from the stream context (metadata, peer) and returns a status error to
// refuse it, or calls Accept and then runs the session on c. The RPC ends
// when Handler returns or c is closed.
type Handler func(ctx context.Context, c *ServerConn) error

// ServerConn is the server end of a Session stream.
type ServerConn struct {
	*Conn
	ss gogrpc.ServerStream
}

// Accept sends the response headers: the dialer's Dial returns only once
// they arrive, so a refusal (a status before Accept) is a dial error and
// an accepted stream is a transport connection.
func (c *ServerConn) Accept() error {
	return c.ss.SendHeader(metadata.MD{})
}

// ServerOptions are the options every conduit grpc.Server uses: the
// pass-through codec and the message size limits. Server keepalive
// enforcement stays at the grpc-go defaults, and no MaxConnectionAge is
// set, so the conduit GoAway is the only planned session end (§3.6.1).
func ServerOptions() []gogrpc.ServerOption {
	return []gogrpc.ServerOption{
		gogrpc.ForceServerCodecV2(frameCodec{}),
		gogrpc.MaxRecvMsgSize(MaxMessageSize),
		gogrpc.MaxSendMsgSize(MaxMessageSize),
	}
}

// NewServer returns a grpc.Server serving only Conduit.Session with h.
// extra options are appended to ServerOptions.
func NewServer(h Handler, extra ...gogrpc.ServerOption) *gogrpc.Server {
	srv := gogrpc.NewServer(append(ServerOptions(), extra...)...)
	Register(srv, h)
	return srv
}

// Register registers Conduit.Session with h on srv. srv must have been
// built with ServerOptions.
func Register(srv *gogrpc.Server, h Handler) {
	srv.RegisterService(&serviceDesc, &service{h: h})
}

type service struct{ h Handler }

var serviceDesc = gogrpc.ServiceDesc{
	ServiceName: ServiceName,
	HandlerType: (*any)(nil),
	Streams: []gogrpc.StreamDesc{{
		StreamName:    "Session",
		Handler:       sessionHandler,
		ServerStreams: true,
		ClientStreams: true,
	}},
	Metadata: "conduit/v1/conduit.proto",
}

func sessionHandler(srv any, ss gogrpc.ServerStream) error {
	return srv.(*service).serve(ss)
}

// serve runs the handler. Returning ends the RPC and cancels the stream
// context, which unblocks a RecvMsg or SendMsg still in progress; so a
// Close returns from here at once even while the handler is still
// cleaning up.
func (s *service) serve(ss gogrpc.ServerStream) error {
	stopped := make(chan struct{})
	c := &ServerConn{ss: ss}
	c.Conn = &Conn{
		stream:  ss,
		done:    make(chan struct{}),
		closeFn: func(bool) { close(stopped) },
	}
	errc := make(chan error, 1)
	go func() { errc <- s.h(ss.Context(), c) }()
	select {
	case err := <-errc:
		_ = c.Close()
		return handlerStatus(err)
	case <-stopped:
		return nil
	}
}

// handlerStatus is the RPC status of a handler return: status errors pass
// through, any other error is a generic Internal (the cause belongs in the
// server log, not on the wire).
func handlerStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Internal, "conduit session failed")
}

// closeLinger bounds how long a client Close waits for the server to end
// the stream after the half-close before cancelling it.
const closeLinger = time.Second
