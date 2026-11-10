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

//go:build linux

package control

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	conduitv1 "github.com/GoogleCloudPlatform/scion/proto/conduit/v1"
)

// listeningSockets returns the inodes of this process's listening
// sockets: TCP and TCP6 in state LISTEN, and listening unix sockets.
func listeningSockets(t *testing.T) map[string]bool {
	t.Helper()
	listening := map[string]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		scanLines(t, f, func(fields []string) {
			// sl local rem st ... inode is field 9.
			if len(fields) > 9 && fields[3] == "0A" {
				listening[fields[9]] = true
			}
		})
	}
	scanLines(t, "/proc/net/unix", func(fields []string) {
		// Num RefCount Protocol Flags Type St Inode; flag 00010000 is
		// __SO_ACCEPTCON (listening).
		if len(fields) > 6 && fields[3] == "00010000" {
			listening[fields[6]] = true
		}
	})
	own := map[string]bool{}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		link, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if err != nil {
			continue
		}
		if inode, ok := strings.CutPrefix(link, "socket:["); ok {
			inode = strings.TrimSuffix(inode, "]")
			if listening[inode] {
				own[inode] = true
			}
		}
	}
	return own
}

func scanLines(t *testing.T, path string, fn func([]string)) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fn(strings.Fields(s.Text()))
	}
}

// TestDeliverRoute_NoListener: enabling and serving the deliver-message
// route opens no listening socket; the route is reached only as an
// RpcRequest through RPCHandler (design r3 §5.2 no-listener item). A planted
// listener shows the check sees this process's sockets.
func TestDeliverRoute_NoListener(t *testing.T) {
	before := listeningSockets(t)

	h := New(Options{KickTokenRefresh: func() {}, Deliver: &DeliverOptions{Runner: &fakeRunner{}}})
	resp := RPCHandler(h).HandleRPC(context.Background(), &conduitv1.RpcRequest{
		Method: "POST", Path: Prefix + RouteDeliverMessage, Body: []byte(body(nil)),
		Headers: map[string]string{"Content-Type": "application/json"},
	})
	if resp.GetStatus() != 200 {
		t.Fatalf("RPC status %d body %q, want 200", resp.GetStatus(), resp.GetBody())
	}
	after := listeningSockets(t)
	for inode := range after {
		if !before[inode] {
			t.Fatalf("a listening socket appeared (inode %s)", inode)
		}
	}

	// Control: a planted listener is detected.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	planted := listeningSockets(t)
	if len(planted) <= len(after) {
		t.Fatalf("planted listener not detected: %d before, %d after", len(after), len(planted))
	}
}
