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
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"

	conduitgrpc "github.com/GoogleCloudPlatform/scion/pkg/conduit/transport/grpc"
)

// pingCountingListener parses the HTTP/2 frames each accepted connection
// receives (client to server) and counts PING frames that are not ACKs:
// the pings the dialer itself sends.
type pingCountingListener struct {
	net.Listener
	pings  atomic.Int64
	frames atomic.Int64
	wg     sync.WaitGroup
}

func (l *pingCountingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		preface := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(pr, preface); err != nil {
			return
		}
		fr := http2.NewFramer(io.Discard, pr)
		fr.SetMaxReadFrameSize(1 << 24)
		for {
			f, err := fr.ReadFrame()
			if err != nil {
				_, _ = io.Copy(io.Discard, pr)
				return
			}
			l.frames.Add(1)
			if p, ok := f.(*http2.PingFrame); ok && !p.IsAck() {
				l.pings.Add(1)
			}
		}
	}()
	return &teeConn{Conn: c, w: pw}, nil
}

type teeConn struct {
	net.Conn
	w *io.PipeWriter
}

func (c *teeConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		_, _ = c.w.Write(b[:n])
	}
	if err != nil {
		_ = c.w.CloseWithError(err)
	}
	return n, err
}

func (c *teeConn) Close() error {
	_ = c.w.Close()
	return c.Conn.Close()
}

// TestDialerSendsNoHTTP2Pings checks D6: with client keepalive off and BDP
// estimation disabled, the dialer sends no HTTP/2 PING during a session,
// neither while idle nor while receiving bulk data (which is what
// triggers BDP pings when the windows are left dynamic).
func TestDialerSendsNoHTTP2Pings(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := &pingCountingListener{Listener: inner}
	srv := conduitgrpc.NewServer(echo)
	go func() { _ = srv.Serve(ln) }()
	defer srv.Stop()

	c, err := dial(t, "http://"+inner.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{1}, 512<<10)
	for range 16 {
		if err := c.WriteFrame(payload); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ReadFrame(); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(500 * time.Millisecond) // idle
	_ = c.Close()
	srv.Stop()
	ln.wg.Wait()
	if ln.frames.Load() == 0 {
		t.Fatal("frame parser saw no frames; the check is not observing the connection")
	}
	if n := ln.pings.Load(); n != 0 {
		t.Fatalf("dialer sent %d HTTP/2 PING frames, want 0", n)
	}
}
