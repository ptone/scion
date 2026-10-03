// Command conduit-sshd is a THROWAWAY spike (Conduit Phase 0b, design Q5 /
// ptone/scion#2776). It is an embedded SSH server (gliderlabs/ssh over
// x/crypto/ssh) that stands in for the sciontool SSH stream handler:
//
//   - transport: stdin/stdout (-stdio, for ProxyCommand use) or a TCP listener
//     (-listen; one goroutine per connection, or strictly serial with -serial).
//   - auth: "the stream is already authenticated" — no client auth at all, the
//     requested user name is ignored and everything runs as the process user.
//   - host key: ed25519 generated at start (or loaded/persisted via -hostkey).
//   - sessions: shell with PTY on request, exec without PTY, sftp subsystem.
//   - forwarding profile (design §3.8, A3):
//     direct-tcpip only to loopback (127.0.0.0/8, ::1, "localhost"),
//     direct-streamlocal@openssh.com only to existing Unix sockets in the
//     container filesystem, ports 9810/18380 denied, <= 32 channels per
//     connection, tcpip-forward (-R), streamlocal-forward, agent forwarding
//     (-A) and X11 refused.
//
// This is not production code and is not intended to merge into main.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

var (
	flagStdio       = flag.Bool("stdio", false, "serve a single SSH connection on stdin/stdout (ProxyCommand mode)")
	flagListen      = flag.String("listen", "", "TCP address to listen on; connections are served one at a time")
	flagHostKey     = flag.String("hostkey", "", "path to an OpenSSH ed25519 private key; generated (and saved) if missing; ephemeral if empty")
	flagHostKeyPub  = flag.String("hostkey-pub-out", "", "write the host public key (authorized_keys format) to this path")
	flagMaxChannels = flag.Int("max-channels", 32, "maximum concurrently open channels per SSH connection")
	flagDenyPorts   = flag.String("deny-ports", "9810,18380", "comma-separated destination ports always refused")
	flagShell       = flag.String("shell", "", "shell to run (default: $SHELL or /bin/bash)")
	flagLog         = flag.String("log", "", "log file (default: stderr in -listen mode, /tmp/conduit-sshd.log in -stdio mode)")
	flagWorkdir     = flag.String("workdir", "", "initial working directory for sessions (default: $HOME)")
	flagInitHostKey = flag.Bool("init-hostkey", false, "create -hostkey (and -hostkey-pub-out) if missing, print the public key, and exit")
	flagSerial      = flag.Bool("serial", false, "with -listen: serve accepted connections strictly one at a time")
)

var (
	denyPorts = map[uint32]bool{}
	connSeq   atomic.Int64
)

type ctxKey string

const (
	ctxKeyChanCount ctxKey = "conduit-chan-count"
	ctxKeyConnID    ctxKey = "conduit-conn-id"
)

func main() {
	flag.Parse()
	if *flagInitHostKey {
		if *flagHostKey == "" {
			fmt.Fprintln(os.Stderr, "-init-hostkey requires -hostkey")
			os.Exit(2)
		}
		signer, err := loadOrCreateHostKey(*flagHostKey)
		if err != nil {
			fmt.Fprintln(os.Stderr, "host key:", err)
			os.Exit(1)
		}
		pub := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(signer.PublicKey())))
		if *flagHostKeyPub != "" {
			if err := os.WriteFile(*flagHostKeyPub, []byte(pub+"\n"), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, "write host pub:", err)
				os.Exit(1)
			}
		}
		fmt.Println(pub)
		return
	}
	if *flagStdio == (*flagListen != "") {
		fmt.Fprintln(os.Stderr, "exactly one of -stdio or -listen is required")
		os.Exit(2)
	}
	setupLog()
	for _, p := range strings.Split(*flagDenyPorts, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			log.Fatalf("bad -deny-ports entry %q: %v", p, err)
		}
		denyPorts[uint32(n)] = true
	}

	signer, err := loadOrCreateHostKey(*flagHostKey)
	if err != nil {
		log.Fatalf("host key: %v", err)
	}
	pubLine := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(signer.PublicKey())))
	log.Printf("host key %s %s", gossh.FingerprintSHA256(signer.PublicKey()), pubLine)
	if *flagHostKeyPub != "" {
		if err := os.WriteFile(*flagHostKeyPub, []byte(pubLine+"\n"), 0o644); err != nil {
			log.Fatalf("write host pub: %v", err)
		}
	}

	srv := newServer(signer)

	if *flagStdio {
		log.Printf("serving one connection on stdio (pid %d)", os.Getpid())
		srv.HandleConn(&stdioConn{})
		log.Printf("stdio connection finished")
		return
	}

	ln, err := net.Listen("tcp", *flagListen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("listening on %s (serial=%v)", ln.Addr(), *flagSerial)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatalf("accept: %v", err)
		}
		log.Printf("accepted %s", c.RemoteAddr())
		serve := func() {
			srv.HandleConn(c)
			log.Printf("connection from %s finished", c.RemoteAddr())
		}
		if *flagSerial {
			serve()
		} else {
			go serve()
		}
	}
}

func setupLog() {
	path := *flagLog
	if path == "" && *flagStdio {
		path = "/tmp/conduit-sshd.log"
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix(fmt.Sprintf("[conduit-sshd %d] ", os.Getpid()))
	if path == "" || path == "-" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conduit-sshd: cannot open log %s: %v\n", path, err)
		os.Exit(1)
	}
	log.SetOutput(f)
}

func newServer(signer gossh.Signer) *ssh.Server {
	srv := &ssh.Server{
		Version: "conduit-spike",
		Handler: sessionHandler,
		ConnCallback: func(ctx ssh.Context, c net.Conn) net.Conn {
			ctx.SetValue(ctxKeyChanCount, new(atomic.Int32))
			ctx.SetValue(ctxKeyConnID, connSeq.Add(1))
			return c
		},
		ConnectionFailedCallback: func(c net.Conn, err error) {
			log.Printf("handshake failed: %v", err)
		},
		// No auth handlers => NoClientAuth: the stream is already authenticated.
		PtyCallback: func(ctx ssh.Context, p ssh.Pty) bool { return true },
		SubsystemHandlers: map[string]ssh.SubsystemHandler{
			"sftp": sftpHandler,
		},
		ChannelHandlers: map[string]ssh.ChannelHandler{
			"session":                        capped(filteredSessionHandler),
			"direct-tcpip":                   capped(directTCPIPHandler),
			"direct-streamlocal@openssh.com": capped(directStreamlocalHandler),
			"default":                        rejectChannel,
		},
		RequestHandlers: map[string]ssh.RequestHandler{
			"tcpip-forward":                          refuseGlobal,
			"cancel-tcpip-forward":                   refuseGlobal,
			"streamlocal-forward@openssh.com":        refuseGlobal,
			"cancel-streamlocal-forward@openssh.com": refuseGlobal,
			"default": func(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte) {
				// keepalive@openssh.com, no-more-sessions@openssh.com, hostkeys-* ...
				// A "false" reply is what OpenSSH expects for unknown requests.
				return false, nil
			},
		},
	}
	srv.AddHostKey(signer)
	return srv
}

func cid(ctx ssh.Context) int64 {
	if v, ok := ctx.Value(ctxKeyConnID).(int64); ok {
		return v
	}
	return 0
}

// ---------- channel cap ----------

func capped(h ssh.ChannelHandler) ssh.ChannelHandler {
	return func(srv *ssh.Server, conn *gossh.ServerConn, nc gossh.NewChannel, ctx ssh.Context) {
		cnt := ctx.Value(ctxKeyChanCount).(*atomic.Int32)
		n := cnt.Add(1)
		defer cnt.Add(-1)
		if int(n) > *flagMaxChannels {
			log.Printf("conn=%d DENY channel %s: cap %d reached (would be #%d)", cid(ctx), nc.ChannelType(), *flagMaxChannels, n)
			nc.Reject(gossh.ResourceShortage, fmt.Sprintf("channel limit (%d) reached", *flagMaxChannels))
			return
		}
		log.Printf("conn=%d open channel %s (#%d open)", cid(ctx), nc.ChannelType(), n)
		h(srv, conn, nc, ctx)
		log.Printf("conn=%d close channel %s", cid(ctx), nc.ChannelType())
	}
}

func rejectChannel(srv *ssh.Server, conn *gossh.ServerConn, nc gossh.NewChannel, ctx ssh.Context) {
	log.Printf("conn=%d DENY channel type %q", cid(ctx), nc.ChannelType())
	nc.Reject(gossh.Prohibited, "channel type not permitted by conduit profile")
}

func refuseGlobal(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte) {
	log.Printf("conn=%d DENY global request %q (remote forwarding is not permitted)", cid(ctx), req.Type)
	return false, nil
}

// ---------- session request filter (agent / X11 refusal) ----------

// gliderlabs/ssh v0.3.8 unconditionally replies "true" to
// auth-agent-req@openssh.com (see session.go TODO). We wrap the NewChannel so
// the session request stream passes through a filter that refuses agent and
// X11 requests before gliderlabs sees them.
type filteredNewChannel struct {
	gossh.NewChannel
	ctx ssh.Context
}

var refusedSessionReqs = map[string]bool{
	"auth-agent-req@openssh.com": true,
	"auth-agent-req":             true,
	"x11-req":                    true,
}

func (f filteredNewChannel) Accept() (gossh.Channel, <-chan *gossh.Request, error) {
	ch, reqs, err := f.NewChannel.Accept()
	if err != nil {
		return ch, reqs, err
	}
	out := make(chan *gossh.Request)
	go func() {
		defer close(out)
		for r := range reqs {
			if refusedSessionReqs[r.Type] {
				log.Printf("conn=%d DENY session request %q (want_reply=%v)", cid(f.ctx), r.Type, r.WantReply)
				r.Reply(false, nil)
				continue
			}
			if r.Type != "window-change" {
				log.Printf("conn=%d session request %q", cid(f.ctx), r.Type)
			}
			out <- r
		}
	}()
	return ch, out, nil
}

func filteredSessionHandler(srv *ssh.Server, conn *gossh.ServerConn, nc gossh.NewChannel, ctx ssh.Context) {
	ssh.DefaultSessionHandler(srv, conn, filteredNewChannel{NewChannel: nc, ctx: ctx}, ctx)
}

// ---------- forwarding ----------

type directTCPIPPayload struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

// loopbackDest returns the literal loopback address to dial, or an error.
// Hostnames other than "localhost" are never resolved (no DNS-rebinding games).
func loopbackDest(host string) (string, error) {
	h := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(h, "localhost") || strings.EqualFold(h, "localhost.") {
		return "127.0.0.1", nil
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return "", fmt.Errorf("destination %q is not a loopback IP literal", host)
	}
	if !ip.IsLoopback() {
		return "", fmt.Errorf("destination %s is not loopback", ip)
	}
	return ip.String(), nil
}

func directTCPIPHandler(srv *ssh.Server, conn *gossh.ServerConn, nc gossh.NewChannel, ctx ssh.Context) {
	var d directTCPIPPayload
	if err := gossh.Unmarshal(nc.ExtraData(), &d); err != nil {
		nc.Reject(gossh.ConnectionFailed, "bad direct-tcpip payload")
		return
	}
	host, err := loopbackDest(d.DestAddr)
	if err != nil {
		log.Printf("conn=%d DENY direct-tcpip %s:%d: %v", cid(ctx), d.DestAddr, d.DestPort, err)
		nc.Reject(gossh.Prohibited, err.Error())
		return
	}
	if denyPorts[d.DestPort] {
		log.Printf("conn=%d DENY direct-tcpip %s:%d: port on deny-list", cid(ctx), d.DestAddr, d.DestPort)
		nc.Reject(gossh.Prohibited, fmt.Sprintf("port %d is reserved", d.DestPort))
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(d.DestPort)))
	dconn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		log.Printf("conn=%d direct-tcpip %s dial failed: %v", cid(ctx), target, err)
		nc.Reject(gossh.ConnectionFailed, err.Error())
		return
	}
	log.Printf("conn=%d ALLOW direct-tcpip -> %s (origin %s:%d)", cid(ctx), target, d.OriginAddr, d.OriginPort)
	splice(ctx, nc, dconn)
}

type directStreamlocalPayload struct {
	SocketPath string
	Reserved0  string
	Reserved1  uint32
}

func directStreamlocalHandler(srv *ssh.Server, conn *gossh.ServerConn, nc gossh.NewChannel, ctx ssh.Context) {
	var d directStreamlocalPayload
	if err := gossh.Unmarshal(nc.ExtraData(), &d); err != nil {
		nc.Reject(gossh.ConnectionFailed, "bad direct-streamlocal payload")
		return
	}
	p := d.SocketPath
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		log.Printf("conn=%d DENY direct-streamlocal %q: not a clean absolute path", cid(ctx), p)
		nc.Reject(gossh.Prohibited, "socket path must be a clean absolute path")
		return
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		log.Printf("conn=%d DENY direct-streamlocal %q: not an existing unix socket", cid(ctx), p)
		nc.Reject(gossh.Prohibited, "not an existing unix socket in the container")
		return
	}
	dconn, err := net.DialTimeout("unix", p, 10*time.Second)
	if err != nil {
		nc.Reject(gossh.ConnectionFailed, err.Error())
		return
	}
	log.Printf("conn=%d ALLOW direct-streamlocal -> %s", cid(ctx), p)
	splice(ctx, nc, dconn)
}

func splice(ctx ssh.Context, nc gossh.NewChannel, dconn net.Conn) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		dconn.Close()
		return
	}
	go gossh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(ch, dconn)
		ch.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		io.Copy(dconn, ch)
		if cw, ok := dconn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	ch.Close()
	dconn.Close()
}

// ---------- sessions ----------

func shellPath() string {
	if *flagShell != "" {
		return *flagShell
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/bash"
}

func baseEnv() []string {
	env := []string{}
	keep := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "TZ": true}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if keep[k] {
			env = append(env, kv)
		}
	}
	if u, err := user.Current(); err == nil {
		env = append(env, "USER="+u.Username, "LOGNAME="+u.Username, "HOME="+u.HomeDir)
	}
	env = append(env, "SHELL="+shellPath())
	return env
}

func workdir() string {
	if *flagWorkdir != "" {
		return *flagWorkdir
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

func sessionHandler(s ssh.Session) {
	ctx := s.Context()
	ptyReq, winCh, isPty := s.Pty()
	raw := s.RawCommand()
	env := append(baseEnv(), s.Environ()...)
	env = append(env, "SSH_CONNECTION=127.0.0.1 0 127.0.0.1 22")
	log.Printf("conn=%d session user(requested)=%q pty=%v cmd=%q", cid(ctx), s.User(), isPty, truncate(raw, 120))

	var cmd *exec.Cmd
	if raw == "" {
		cmd = exec.Command(shellPath(), "-l")
	} else {
		cmd = exec.Command(shellPath(), "-c", raw)
	}
	cmd.Env = env
	cmd.Dir = workdir()

	if isPty {
		cmd.Env = append(cmd.Env, "TERM="+ptyReq.Term)
		f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(ptyReq.Window.Width), Rows: uint16(ptyReq.Window.Height)})
		if err != nil {
			fmt.Fprintf(s.Stderr(), "pty start: %v\n", err)
			s.Exit(1)
			return
		}
		go func() {
			for w := range winCh {
				setWinsize(f, w.Width, w.Height)
			}
		}()
		go io.Copy(f, s)
		go func() {
			<-ctx.Done()
			if cmd.Process != nil {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
			}
		}()
		io.Copy(s, f) // returns EIO when the child side closes
		err = cmd.Wait()
		f.Close()
		s.Exit(exitCode(err))
		return
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		s.Exit(1)
		return
	}
	cmd.Stdout = s
	cmd.Stderr = s.Stderr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(s.Stderr(), "start: %v\n", err)
		s.Exit(127)
		return
	}
	go func() {
		io.Copy(stdin, s)
		stdin.Close()
	}()
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
		case <-stop:
		}
	}()
	err = cmd.Wait()
	close(stop)
	code := exitCode(err)
	log.Printf("conn=%d exec finished code=%d", cid(ctx), code)
	s.Exit(code)
}

func sftpHandler(s ssh.Session) {
	log.Printf("conn=%d sftp subsystem start", cid(s.Context()))
	opts := []sftp.ServerOption{sftp.WithServerWorkingDirectory(workdir())}
	srv, err := sftp.NewServer(s, opts...)
	if err != nil {
		log.Printf("sftp: %v", err)
		return
	}
	if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
		log.Printf("sftp serve: %v", err)
	}
	srv.Close()
	log.Printf("conn=%d sftp subsystem end", cid(s.Context()))
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 255
}

func setWinsize(f *os.File, w, h int) {
	pty.Setsize(f, &pty.Winsize{Cols: uint16(w), Rows: uint16(h)})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------- host key ----------

func loadOrCreateHostKey(path string) (gossh.Signer, error) {
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return gossh.ParsePrivateKey(b)
		}
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	if path != "" {
		blk, err := gossh.MarshalPrivateKey(priv, "conduit-spike host key")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, pem.EncodeToMemory(blk), 0o600); err != nil {
			return nil, err
		}
	}
	return signer, nil
}

// ---------- stdio net.Conn ----------

type stdioConn struct{ closed atomic.Bool }

func (c *stdioConn) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (c *stdioConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	os.Stdin.Close()
	return os.Stdout.Close()
}
func (c *stdioConn) LocalAddr() net.Addr                { return stdioAddr{} }
func (c *stdioConn) RemoteAddr() net.Addr               { return stdioAddr{} }
func (c *stdioConn) SetDeadline(t time.Time) error      { return nil }
func (c *stdioConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *stdioConn) SetWriteDeadline(t time.Time) error { return nil }

type stdioAddr struct{}

func (stdioAddr) Network() string { return "stdio" }
func (stdioAddr) String() string  { return "stdio" }
