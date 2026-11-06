package cloudrun

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/compute/metadata"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	resourcemanagerpb "cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

type IAPExecConnector struct {
	IapTunnelUrlOverride  string
	IapTunnelInsecureTLS  bool
	SSHUser               string
	SSHHost               string
	StrictHostKeyChecking string
	UserKnownHostsFile    string
	SSHDebug              bool

	// newInstancesClient overrides how the Instances API client used to look
	// up the instance's service account is constructed. Nil in production
	// (NewInstancesClient); tests inject a fake.
	newInstancesClient func(ctx context.Context) (InstancesAPI, error)
	// detectCredentials overrides how the token source is built. Nil in
	// production (Application Default Credentials); tests inject a fake.
	detectCredentials func() (tokenSource, error)
	// lookupProjectNumber overrides how a project ID is resolved to its
	// number. Nil in production (fetchProjectNumber); tests inject a fake.
	lookupProjectNumber func(ctx context.Context, projectID string) (string, error)
	// callerEmail overrides how the email of the identity the connector
	// authenticates as is resolved. Nil in production (resolveCallerEmail);
	// tests inject a fake.
	callerEmail func(ctx context.Context) (string, error)
	// now overrides the clock used for signed key expiry. Nil in production
	// (time.Now).
	now func() time.Time
	// sshRunner overrides one ssh run. Nil in production (runSSH); tests
	// inject a fake to observe the authentication retry.
	sshRunner func(ctx context.Context, project, location, instanceName string, cmdArgs []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error

	credsMu sync.Mutex
	creds   tokenSource

	// instancesClient is created on first use and kept for the connector's
	// lifetime; see Close.
	clientMu        sync.Mutex
	instancesClient InstancesAPI

	mu             sync.Mutex
	projectNumbers map[string]string
	signedKeys     map[string]*signedKey
	// lastImportOK is when a key was last imported into the caller's OS
	// Login profile successfully; zero when none counts. Guarded by mu.
	lastImportOK time.Time
	// lastImportAttempt is when an import was last attempted, whatever its
	// outcome. Guarded by mu.
	lastImportAttempt time.Time

	// importSlot holds one token while an import decision or import is in
	// progress, so concurrent misses share one import; waiters give up when
	// their context ends. Created on first use.
	importSlotOnce sync.Once
	importSlot     chan struct{}
}

// tokenSource yields OAuth2 access tokens. *auth.Credentials implements it
// and caches and refreshes its token internally.
type tokenSource interface {
	Token(ctx context.Context) (*auth.Token, error)
}

// signedKey is an SSH key pair and its OS Login certificate for one instance.
type signedKey struct {
	instanceUID    string
	serviceAccount string
	privKeyPEM     []byte
	cert           string
	reuseUntil     time.Time
}

const (
	// signedKeyMinRemaining is how much validity a cached certificate must
	// have left to be reused.
	signedKeyMinRemaining = 60 * time.Second
	// signedKeyDefaultValidity is assumed when the certificate's expiry
	// cannot be read.
	signedKeyDefaultValidity = 4 * time.Minute
	// importMinGap is the least time between two import attempts by one
	// connector, whatever triggers them. With importedKeyValidity it bounds
	// the connector's live imported keys to about five.
	importMinGap = 60 * time.Second
	// importedKeyValidity is how long a public key imported into the
	// caller's OS Login profile stays valid.
	importedKeyValidity = 5 * time.Minute
)

// errSSHAuthFailed marks an ssh run rejected during public key
// authentication.
var errSSHAuthFailed = errors.New("SSH authentication failed")

// importTimeout bounds one import request. Tests shorten it.
var importTimeout = 30 * time.Second

// osLoginBaseURL is the OS Login API endpoint. Tests point it at a local
// server.
var osLoginBaseURL = "https://oslogin.googleapis.com"

// defaultCredentials builds the production token source. Tests replace it so
// they never pick up ambient credentials.
var defaultCredentials = detectDefaultCredentials

// lookupMetadataEmail returns the metadata server's default service account
// email. Tests replace it or point GCE_METADATA_HOST at a local server.
var lookupMetadataEmail = metadataServiceAccountEmail

// defaultServiceAccount matches, lower-cased, a project's default Compute
// Engine and App Engine service accounts, which are never accepted as an
// instance's runtime identity. The hub recognises the same two forms in
// isValidServiceAccountEmail (pkg/hub/passthrough_gate.go) and
// projectIDFromServiceAccount (pkg/hub/google_sa.go).
var defaultServiceAccount = regexp.MustCompile(`^([0-9]+-compute@developer|[^@]+@appspot)\.gserviceaccount\.com$`)

// isDefaultServiceAccount reports whether sa is a project default service
// account.
func isDefaultServiceAccount(sa string) bool {
	return defaultServiceAccount.MatchString(strings.ToLower(strings.TrimSpace(sa)))
}

func NewIAPExecConnector(iapTunnelUrlOverride string) *IAPExecConnector {
	return &IAPExecConnector{
		IapTunnelUrlOverride: iapTunnelUrlOverride,
	}
}

func (c *IAPExecConnector) Connect(ctx context.Context, project, location, instanceName string) error {
	return c.runWithAuthRetry(project, location, instanceName, true, func() error {
		return c.runOnce(ctx, project, location, instanceName, nil, os.Stdin, os.Stdout, os.Stderr)
	})
}

func (c *IAPExecConnector) Exec(ctx context.Context, project, location, instanceName string, cmd []string) ([]byte, error) {
	var outBuf bytes.Buffer
	err := c.runWithAuthRetry(project, location, instanceName, true, func() error {
		outBuf.Reset()
		return c.runOnce(ctx, project, location, instanceName, cmd, nil, &outBuf, &outBuf)
	})
	return outBuf.Bytes(), err
}

// ExecWithStdin runs cmd over the same IAP SSH tunnel as Exec, but attaches
// stdin so callers can deliver secrets without putting them in cmd (which
// becomes part of the remote command line). See #1355.
func (c *IAPExecConnector) ExecWithStdin(ctx context.Context, project, location, instanceName string, cmd []string, stdin io.Reader) ([]byte, error) {
	// The stdin reader cannot be replayed, so an authentication failure drops
	// the cached key without retrying.
	var outBuf bytes.Buffer
	err := c.runWithAuthRetry(project, location, instanceName, false, func() error {
		return c.runOnce(ctx, project, location, instanceName, cmd, stdin, &outBuf, &outBuf)
	})
	return outBuf.Bytes(), err
}

// runOnce runs one ssh session through sshRunner, or runSSH by default.
func (c *IAPExecConnector) runOnce(ctx context.Context, project, location, instanceName string, cmdArgs []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	if c.sshRunner != nil {
		return c.sshRunner(ctx, project, location, instanceName, cmdArgs, stdin, stdout, stderr)
	}
	return c.runSSH(ctx, project, location, instanceName, cmdArgs, stdin, stdout, stderr)
}

// runWithAuthRetry runs attempt. When it fails SSH authentication, the
// instance's cached signed key is dropped and, if retry is set, attempt runs
// once more with a freshly signed key.
//
// Authentication failure is recognised only from the ssh client's own
// diagnostics (see runSSHClient), never from the remote command's output or
// exit status, so a remote command cannot make Connect or Exec re-run it or
// reset the connector's state. ExecWithStdin never re-runs.
//
// An authentication failure also clears the import window, so the next
// signed key is imported first. Caching a signed key, and importing only once
// per window, assume that a signed cert authenticates without its key
// remaining in the login profile; this reset covers the case where it does
// not.
func (c *IAPExecConnector) runWithAuthRetry(project, location, instanceName string, retry bool, attempt func() error) error {
	err := attempt()
	if !errors.Is(err, errSSHAuthFailed) {
		return err
	}
	c.Forget(project, location, instanceName)
	c.clearImportWindow()
	if !retry {
		return err
	}
	return attempt()
}

// Forget drops the cached signed key for the named instance.
func (c *IAPExecConnector) Forget(project, location, instanceName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.signedKeys, instanceResourceName(project, location, instanceShortID(instanceName)))
}

func (c *IAPExecConnector) runSSH(ctx context.Context, project, location, instanceName string, cmdArgs []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	authCreds, err := c.tokenSource()
	if err != nil {
		return fmt.Errorf("failed to detect credentials: %v", err)
	}
	tok, err := authCreds.Token(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve auth token: %v", err)
	}

	projectNumber, err := c.projectNumber(ctx, project)
	if err != nil {
		return fmt.Errorf("failed to resolve project number: %v", err)
	}

	shortID := instanceShortID(instanceName)

	key, err := c.signedKeyForInstance(ctx, project, location, shortID, tok.Value)
	if err != nil {
		return err
	}

	tempDir, err := os.MkdirTemp("", "run-ssh-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	privKeyPath := filepath.Join(tempDir, "id_rsa")
	err = os.WriteFile(privKeyPath, key.privKeyPEM, 0600)
	if err != nil {
		return fmt.Errorf("failed to write private key: %v", err)
	}

	certPath := filepath.Join(tempDir, "id_rsa-cert.pub")
	err = os.WriteFile(certPath, []byte(key.cert), 0644)
	if err != nil {
		return fmt.Errorf("failed to write SSH certificate: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to start local TCP proxy: %v", err)
	}
	defer func() { _ = listener.Close() }()
	localPort := strings.Split(listener.Addr().String(), ":")[1]

	baseURL := "wss://tunnel.cloudproxy.app/v4"
	if c.IapTunnelUrlOverride != "" {
		baseURL = c.IapTunnelUrlOverride
	}
	wsURL := fmt.Sprintf("%s/connect?project=%s&port=22&newWebsocket=True&project_number=%s&cr_workload_type=instance&cr_deployment_name=%s&region=%s",
		baseURL, project, projectNumber, shortID, location)

	headers := http.Header{}
	headers.Add("Authorization", "Bearer "+tok.Value)
	headers.Add("Origin", "bot:iap-tunneler")

	var tlsConfig *tls.Config
	if c.IapTunnelUrlOverride != "" {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: c.IapTunnelInsecureTLS,
		}
		if u, err := url.Parse(c.IapTunnelUrlOverride); err == nil {
			tlsConfig.ServerName = u.Hostname()
		}
	}

	dialer := websocket.Dialer{
		Subprotocols:     []string{"relay.tunnel.cloudproxy.app"},
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig:  tlsConfig,
	}

	wsConn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("failed to connect to IAP WebSocket tunnel: %v (Status: %s, Body: %q)", err, resp.Status, string(body))
		}
		return fmt.Errorf("failed to connect to IAP WebSocket tunnel: %v", err)
	}
	defer func() { _ = wsConn.Close() }()
	slog.Debug("negotiated IAP tunnel websocket subprotocol", "subprotocol", resp.Header.Get("Sec-Websocket-Protocol"))

	iapConn := NewIapConn(wsConn)

	if err := iapConn.Handshake(); err != nil {
		return fmt.Errorf("IAP handshake failed: %w", err)
	}

	go func() {
		localConn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = localConn.Close() }()

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			written, err := io.Copy(iapConn, localConn)
			slog.Debug("copied local SSH bytes to IAP tunnel", "bytes", written, "error", err)
		}()

		go func() {
			defer wg.Done()
			_, _ = io.Copy(localConn, iapConn)
		}()

		wg.Wait()
	}()

	sshUser := c.SSHUser
	if sshUser == "" {
		sshUser = "root"
	}
	sshHost := c.SSHHost
	if sshHost == "" {
		sshHost = "127.0.0.1"
	}
	strictHostKeyChecking := c.StrictHostKeyChecking
	if strictHostKeyChecking == "" {
		strictHostKeyChecking = "accept-new"
	}

	sshArgs := []string{
		"-p", localPort,
		"-i", privKeyPath,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=" + strictHostKeyChecking,
	}
	if c.UserKnownHostsFile != "" {
		sshArgs = append(sshArgs, "-o", "UserKnownHostsFile="+c.UserKnownHostsFile)
	}
	if c.SSHDebug {
		sshArgs = append(sshArgs, "-v")
	}
	sshArgs = append(sshArgs, sshUser+"@"+sshHost)
	sshArgs = appendRemoteCommand(sshArgs, cmdArgs)

	slog.Debug("running SSH command", "user", sshUser, "host", sshHost, "port", localPort, "command_args", len(cmdArgs))
	err = runSSHClient(ctx, tempDir, sshArgs, stdin, stdout, stderr)
	slog.Debug("SSH command finished", "error", err)
	return err
}

// sshBinary is the ssh client run for each session. Tests replace it.
var sshBinary = "ssh"

// sshClientAuthFailure matches the line the ssh client writes when the server
// rejects public key authentication, optionally prefixed by "user@host: ".
var sshClientAuthFailure = regexp.MustCompile(`(?m)^(?:\S+: )?Permission denied \(publickey`)

// runSSHClient runs the ssh client with sshArgs. The client's own
// diagnostics go, through -E, to a client log created fresh and exclusively
// in a new private directory under dir, apart from the remote command's
// stderr. An authentication failure is reported, wrapping errSSHAuthFailed,
// only when ssh exits 255 and that log carries the client's auth-failure
// line: the remote command can neither write the log nor make its exit
// status count. Reading the client's result directly, rather than adding an
// auth-only pre-flight connection or moving to the Go ssh library, keeps one
// connection and the existing ssh behaviour. The log can name the user and
// host, so it is never logged or returned, and it is removed afterwards.
// Client diagnostics, including -v output, therefore no longer reach stderr.
func runSSHClient(ctx context.Context, dir string, sshArgs []string, stdin io.Reader, stdout, stderr io.Writer) error {
	logDir, err := os.MkdirTemp(dir, "ssh-client-*")
	if err != nil {
		return fmt.Errorf("failed to create SSH client log dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(logDir) }()
	logPath := filepath.Join(logDir, "client.log")
	if err := createClientLog(logPath); err != nil {
		return fmt.Errorf("failed to create SSH client log: %w", err)
	}

	sshCmd := exec.CommandContext(ctx, sshBinary, append([]string{"-E", logPath}, sshArgs...)...)
	sshCmd.Stdin = stdin
	sshCmd.Stdout = stdout
	sshCmd.Stderr = stderr
	err = sshCmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 255 && clientLogShowsAuthFailure(logPath) {
		slog.Debug("SSH client authentication failed")
		return fmt.Errorf("SSH command failed: %w: %w", errSSHAuthFailed, err)
	}
	return fmt.Errorf("SSH command failed: %w", err)
}

// createClientLog creates an empty, private ssh client log at path, refusing
// one that already exists so a stale log can never be read. It also returns
// the error from closing the new file, so a log that cannot be closed fails
// the session closed.
func createClientLog(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// clientLogShowsAuthFailure reports whether the ssh client log at path holds
// the client's auth-failure line.
func clientLogShowsAuthFailure(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return false
	}
	return sshClientAuthFailure.Match(data)
}

// tokenSource returns the connector's token source, building it on first
// use. A failed build is not cached.
func (c *IAPExecConnector) tokenSource() (tokenSource, error) {
	c.credsMu.Lock()
	defer c.credsMu.Unlock()
	if c.creds != nil {
		return c.creds, nil
	}
	detect := c.detectCredentials
	if detect == nil {
		detect = defaultCredentials
	}
	creds, err := detect()
	if err != nil {
		return nil, err
	}
	c.creds = creds
	return creds, nil
}

func detectDefaultCredentials() (tokenSource, error) {
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{})
	if err != nil {
		return nil, err
	}
	return creds, nil
}

// projectNumber resolves projectID to its project number, caching successful
// lookups per project ID.
func (c *IAPExecConnector) projectNumber(ctx context.Context, projectID string) (string, error) {
	c.mu.Lock()
	number, ok := c.projectNumbers[projectID]
	c.mu.Unlock()
	if ok {
		return number, nil
	}

	lookup := c.lookupProjectNumber
	if lookup == nil {
		lookup = fetchProjectNumber
	}
	number, err := lookup(ctx, projectID)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	if c.projectNumbers == nil {
		c.projectNumbers = make(map[string]string)
	}
	c.projectNumbers[projectID] = number
	c.mu.Unlock()
	return number, nil
}

func instanceShortID(instanceName string) string {
	if idx := strings.LastIndex(instanceName, "/"); idx != -1 {
		return instanceName[idx+1:]
	}
	return instanceName
}

func instanceResourceName(project, location, shortID string) string {
	return fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, shortID)
}

// appendRemoteCommand appends the remote command to run as a single trailing
// element of sshArgs, or leaves sshArgs unchanged when cmdArgs is empty (the
// interactive/attach path, where ssh should start a login shell instead of
// running a command).
//
// ssh joins every argument that follows user@host with spaces into one
// string and hands it to the remote login shell, which re-parses it. Passing
// cmdArgs through as separate trailing args would let the remote shell
// re-split and re-interpret any element containing a space or shell
// metacharacter. Quoting each element and joining them into a single string
// here ensures the remote shell instead sees each original argument as one
// literal word.
func appendRemoteCommand(sshArgs []string, cmdArgs []string) []string {
	if len(cmdArgs) == 0 {
		return sshArgs
	}
	return append(sshArgs, quoteRemoteCommand(cmdArgs))
}

// quoteRemoteCommand joins cmdArgs into a single POSIX shell command string
// in which each original argument round-trips as one literal word once the
// remote shell parses it.
func quoteRemoteCommand(cmdArgs []string) string {
	quoted := make([]string, len(cmdArgs))
	for i, arg := range cmdArgs {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// shellQuote wraps s in single quotes for POSIX shell parsing. Each embedded
// single quote is replaced by closing the quoted string, inserting a
// backslash immediately before a literal single quote, and reopening the
// quoted string. The result parses back to exactly s as one word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func fetchProjectNumber(ctx context.Context, projectID string) (string, error) {
	client, err := resourcemanager.NewProjectsClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()

	req := &resourcemanagerpb.GetProjectRequest{
		Name: fmt.Sprintf("projects/%s", projectID),
	}
	proj, err := client.GetProject(ctx, req)
	if err != nil {
		return "", err
	}

	parts := strings.Split(proj.GetName(), "/")
	if len(parts) == 2 {
		return parts[1], nil
	}
	return "", fmt.Errorf("unexpected project name format: %s", proj.GetName())
}

func generateSSHKeyPair() ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	privateKeyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	})

	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	pubKeyOpenSSH := ssh.MarshalAuthorizedKey(publicKey)

	return privKeyPEM, pubKeyOpenSSH, nil
}

// signedKeyForInstance returns an SSH key pair with an OS Login certificate
// for the named instance, signed for the service account the instance runs
// as. Expired signed keys are swept and the instance is looked up on every
// call; the call fails, and drops any signed key cached for the instance,
// when its service account cannot be determined. A signed key is reused for the same
// instance (name and uid) and service account while its certificate has at
// least signedKeyMinRemaining validity left.
func (c *IAPExecConnector) signedKeyForInstance(ctx context.Context, project, location, shortID, token string) (*signedKey, error) {
	name := instanceResourceName(project, location, shortID)
	now := c.clock()
	c.mu.Lock()
	c.sweepExpiredLocked(now)
	c.mu.Unlock()

	serviceAccount, uid, err := c.instanceServiceAccount(ctx, project, location, shortID)
	if err != nil {
		c.mu.Lock()
		delete(c.signedKeys, name)
		c.mu.Unlock()
		return nil, fmt.Errorf("failed to determine service account of instance %q: %w", shortID, err)
	}

	// The lookup takes time, so expiry is checked again against a fresh
	// clock reading.
	c.mu.Lock()
	if k, ok := c.signedKeys[name]; ok && k.instanceUID == uid && k.serviceAccount == serviceAccount && c.clock().Before(k.reuseUntil) {
		c.mu.Unlock()
		return k, nil
	}
	c.mu.Unlock()

	privKeyPEM, pubKeyOpenSSH, err := generateSSHKeyPair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate SSH keys: %v", err)
	}
	// Signing needs the caller to have an OS Login POSIX account, which
	// importing a key creates. The import runs at most once per validity
	// window, and on a missing-account response, and never sooner than
	// importMinGap after the previous attempt. It imports this call's fresh
	// key, whose private half lives only in the in-memory cache entry until
	// reuseUntil. A failed import fails the call. A reused signed key needs
	// neither step.
	used, err := c.ensureCallerAccount(ctx, project, string(pubKeyOpenSSH), token)
	if err != nil {
		return nil, err
	}
	signedCert, err := requestSignedCertificate(ctx, project, location, shortID, serviceAccount, string(pubKeyOpenSSH), token)
	if isMissingAccount(err) {
		c.invalidateImport(used)
		again, ierr := c.ensureCallerAccount(ctx, project, string(pubKeyOpenSSH), token)
		if ierr != nil {
			return nil, ierr
		}
		// Retry once, and only when an import newer than the one this sign
		// relied on now counts; inside importMinGap there is none and the
		// call fails.
		if again.After(used) {
			signedCert, err = requestSignedCertificate(ctx, project, location, shortID, serviceAccount, string(pubKeyOpenSSH), token)
		}
		if isMissingAccount(err) {
			c.clearImportWindow()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to sign SSH key: %w", err)
	}

	now = c.clock()
	k := &signedKey{
		instanceUID:    uid,
		serviceAccount: serviceAccount,
		privKeyPEM:     privKeyPEM,
		cert:           signedCert,
		reuseUntil:     certExpiry(signedCert, now).Add(-signedKeyMinRemaining),
	}
	if k.reuseUntil.After(now) {
		c.mu.Lock()
		if c.signedKeys == nil {
			c.signedKeys = make(map[string]*signedKey)
		}
		c.signedKeys[name] = k
		c.mu.Unlock()
	}
	return k, nil
}

// instanceServiceAccount returns the service account and uid of the named
// instance, read from the Instances API. It fails when the instance reports
// no service account.
func (c *IAPExecConnector) instanceServiceAccount(ctx context.Context, project, location, shortID string) (string, string, error) {
	inst, err := c.getInstance(ctx, project, location, shortID)
	if err != nil {
		return "", "", err
	}
	if inst.GetServiceAccount() == "" {
		return "", "", errors.New("instance reports no service account")
	}
	if isDefaultServiceAccount(inst.GetServiceAccount()) {
		return "", "", errors.New("instance runs as a project default service account")
	}
	return inst.GetServiceAccount(), inst.GetUid(), nil
}

// getInstance reads the named instance from the Instances API.
func (c *IAPExecConnector) getInstance(ctx context.Context, project, location, shortID string) (*runpb.Instance, error) {
	client, err := c.instancesAPI(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create Instances API client: %w", err)
	}

	inst, err := client.GetInstance(ctx, &runpb.GetInstanceRequest{Name: instanceResourceName(project, location, shortID)})
	if err != nil {
		return nil, fmt.Errorf("failed to get instance: %w", err)
	}
	return inst, nil
}

// instancesAPI returns the connector's Instances API client, creating it on
// first use. The client outlives the call that creates it, so it is created
// with ctx's values but without its cancellation. A failed creation is not
// cached.
func (c *IAPExecConnector) instancesAPI(ctx context.Context) (InstancesAPI, error) {
	c.clientMu.Lock()
	defer c.clientMu.Unlock()
	if c.instancesClient != nil {
		return c.instancesClient, nil
	}
	newClient := c.newInstancesClient
	if newClient == nil {
		newClient = NewInstancesClient
	}
	client, err := newClient(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	c.instancesClient = client
	return client, nil
}

// Close releases the connector's Instances API client, if one was created.
// The cached client lives as long as the connector, which for the Cloud Run
// runtime is the lifetime of the process. Close exists for callers that own a
// shorter-lived connector.
func (c *IAPExecConnector) Close() error {
	c.clientMu.Lock()
	defer c.clientMu.Unlock()
	if c.instancesClient == nil {
		return nil
	}
	err := c.instancesClient.Close()
	c.instancesClient = nil
	return err
}

// sweepExpiredLocked drops cached signed keys that may no longer be reused.
// c.mu must be held.
func (c *IAPExecConnector) sweepExpiredLocked(now time.Time) {
	for name, k := range c.signedKeys {
		if !now.Before(k.reuseUntil) {
			delete(c.signedKeys, name)
		}
	}
}

func (c *IAPExecConnector) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// certExpiry returns when the OpenSSH certificate stops being valid, or
// signedKeyDefaultValidity after now when that cannot be read.
func certExpiry(cert string, now time.Time) time.Time {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(cert))
	if err == nil {
		if sshCert, ok := pub.(*ssh.Certificate); ok && sshCert.ValidBefore != ssh.CertTimeInfinity && sshCert.ValidBefore <= math.MaxInt64 {
			return time.Unix(int64(sshCert.ValidBefore), 0)
		}
	}
	return now.Add(signedKeyDefaultValidity)
}

// importWindow is how long after a successful import no further import is
// needed.
const importWindow = importedKeyValidity - signedKeyMinRemaining

// ensureCallerAccount imports publicKey into the caller's OS Login profile
// unless a successful import happened within importWindow or an import was
// attempted within importMinGap. It returns the time of the import that
// counts, zero when none does. A failed import clears the window. The import
// is bounded by importTimeout, and waiting for another caller's import ends
// with ctx.
func (c *IAPExecConnector) ensureCallerAccount(ctx context.Context, project, publicKey, token string) (time.Time, error) {
	slot := c.importSlotChan()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return time.Time{}, fmt.Errorf("failed to import SSH key into OS Login profile: %w", ctx.Err())
	}
	defer func() { <-slot }()

	c.mu.Lock()
	last, lastAttempt := c.lastImportOK, c.lastImportAttempt
	c.mu.Unlock()
	now := c.clock()
	if !last.IsZero() && now.Before(last.Add(importWindow)) {
		return last, nil
	}
	if !lastAttempt.IsZero() && now.Before(lastAttempt.Add(importMinGap)) {
		slog.Debug("OS Login key import skipped", "reason", "rate limited")
		return last, nil
	}

	c.mu.Lock()
	c.lastImportAttempt = now
	c.mu.Unlock()
	importCtx, cancel := context.WithTimeout(ctx, importTimeout)
	err := c.importCallerKey(importCtx, project, publicKey, token, now)
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.lastImportOK = time.Time{}
		return time.Time{}, err
	}
	c.lastImportOK = now
	return now, nil
}

// importSlotChan returns the connector's one-token import slot.
func (c *IAPExecConnector) importSlotChan() chan struct{} {
	c.importSlotOnce.Do(func() { c.importSlot = make(chan struct{}, 1) })
	return c.importSlot
}

// invalidateImport clears the import window if the import that counts is
// still used, so a concurrent re-import is not repeated.
func (c *IAPExecConnector) invalidateImport(used time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastImportOK.Equal(used) {
		c.lastImportOK = time.Time{}
	}
}

// clearImportWindow makes the next signed key be imported first.
func (c *IAPExecConnector) clearImportWindow() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastImportOK = time.Time{}
}

// isMissingAccount reports whether err is the OS Login response for a caller
// without a POSIX account: 404 with the API status NOT_FOUND.
func isMissingAccount(err error) bool {
	var apiErr *osLoginAPIError
	return errors.As(err, &apiErr) && apiErr.statusCode == http.StatusNotFound && apiErr.apiStatus == "NOT_FOUND"
}

// importCallerKey imports publicKey into the OS Login profile of the
// identity the connector authenticates as, which creates its POSIX account
// when it has none and otherwise adds the key to it. The key expires
// importedKeyValidity after now. Only the outcome and the HTTP status are
// logged; the caller's email never leaves this function except in the
// request URL.
func (c *IAPExecConnector) importCallerKey(ctx context.Context, project, publicKey, token string, now time.Time) error {
	resolve := c.callerEmail
	if resolve == nil {
		resolve = c.resolveCallerEmail
	}
	email, err := resolve(ctx)
	if err != nil || email == "" {
		slog.Warn("OS Login key import failed", "reason", "caller identity unresolved")
		return errors.New("failed to import SSH key into OS Login profile: caller identity could not be determined")
	}
	expiry := now.Add(importedKeyValidity)
	if err := importSSHPublicKey(ctx, email, project, publicKey, token, expiry); err != nil {
		var apiErr *osLoginAPIError
		if errors.As(err, &apiErr) {
			slog.Warn("OS Login key import failed", "status", apiErr.statusCode)
		} else {
			slog.Warn("OS Login key import failed", "reason", "request error")
		}
		return fmt.Errorf("failed to import SSH key into OS Login profile: %w", err)
	}
	slog.Debug("OS Login key import succeeded")
	return nil
}

// importSSHPublicKey calls users.importSshPublicKey for email. The response
// carries the login profile and is discarded unread on success. A transport
// error is returned without detail, since it can quote the request URL, which
// names the user.
func importSSHPublicKey(ctx context.Context, email, projectID, publicKey, token string, expiry time.Time) error {
	endpoint := fmt.Sprintf("%s/v1/users/%s:importSshPublicKey?projectId=%s", osLoginBaseURL, url.PathEscape(email), url.QueryEscape(projectID))
	jsonBody, err := json.Marshal(map[string]string{
		"key":                publicKey,
		"expirationTimeUsec": strconv.FormatInt(expiry.UnixMicro(), 10),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return errors.New("invalid import request")
	}
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Add("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("import request failed: %w", ctxErr)
		}
		return errors.New("import request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newOSLoginAPIError(resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// resolveCallerEmail returns the email of the identity the connector's
// credentials authenticate as: the client email of a service account key, or
// the target of an impersonated credential. Credentials without a JSON
// definition come from the metadata server, which is asked for its default
// service account. Any other credential fails, rather than importing the key
// for an identity other than the one that signs.
func (c *IAPExecConnector) resolveCallerEmail(ctx context.Context) (string, error) {
	ts, err := c.tokenSource()
	if err != nil {
		return "", err
	}
	var credsJSON []byte
	if creds, ok := ts.(*auth.Credentials); ok {
		credsJSON = creds.JSON()
	}
	if len(credsJSON) > 0 {
		if email := credentialsEmail(credsJSON); email != "" {
			return email, nil
		}
		return "", errors.New("credentials do not name a service account")
	}
	return lookupMetadataEmail(ctx)
}

// credentialsEmail returns the service account a credentials JSON file
// authenticates as, or "" when it names none.
func credentialsEmail(credsJSON []byte) string {
	var f struct {
		Type                           string `json:"type"`
		ClientEmail                    string `json:"client_email"`
		ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
	}
	if err := json.Unmarshal(credsJSON, &f); err != nil {
		return ""
	}
	// An impersonated credential authenticates as its target, not as the
	// source it was built from.
	if u := f.ServiceAccountImpersonationURL; u != "" {
		const marker = "/serviceAccounts/"
		i := strings.LastIndex(u, marker)
		if i < 0 {
			return ""
		}
		email, _, _ := strings.Cut(u[i+len(marker):], ":")
		if !strings.Contains(email, "@") {
			return ""
		}
		return email
	}
	if f.Type == "service_account" {
		return f.ClientEmail
	}
	return ""
}

// metadataServiceAccountEmail asks the metadata server, at GCE_METADATA_HOST
// when set, for the email of the default service account.
func metadataServiceAccountEmail(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// A client on http.DefaultTransport, rather than the package's own, so
	// tests' dial guard covers it.
	email, err := metadata.NewClient(&http.Client{}).EmailWithContext(ctx, "default")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("metadata server email lookup failed: %w", ctxErr)
		}
		return "", errors.New("metadata server email lookup failed")
	}
	email = strings.TrimSpace(email)
	if !strings.Contains(email, "@") {
		return "", errors.New("metadata server returned no service account email")
	}
	return email, nil
}

// osLoginAPIError is a non-2xx OS Login API response, reduced to the HTTP
// status and the API's error status and message, with identifiers redacted.
type osLoginAPIError struct {
	statusCode int
	apiStatus  string
	message    string
}

func (e *osLoginAPIError) Error() string {
	msg := fmt.Sprintf("OS Login API returned status %d", e.statusCode)
	if e.apiStatus != "" {
		msg += " " + e.apiStatus
	}
	if e.message != "" {
		msg += ": " + e.message
	}
	return msg
}

// maxOSLoginErrorMessage caps the API error message kept in an error.
const maxOSLoginErrorMessage = 300

var (
	// emailLike matches anything shaped like an email address, also with
	// the @ percent-encoded.
	emailLike = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+(@|%40)[a-z0-9.\-]+`)
	// posixNameLike matches lower-case words joined by underscores, the
	// shape of OS Login POSIX usernames (name_domain_com, sa_<id>); API
	// enum values such as NOT_FOUND are upper-case and kept.
	posixNameLike = regexp.MustCompile(`\b[a-z0-9][a-z0-9.\-]*(_[a-z0-9.\-]+)+\b`)
	// longNumber matches digit runs as long as project numbers and uids.
	longNumber = regexp.MustCompile(`[0-9]{6,}`)
)

// newOSLoginAPIError reads resp's error body.
func newOSLoginAPIError(resp *http.Response) *osLoginAPIError {
	e := &osLoginAPIError{statusCode: resp.StatusCode}
	var body struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return e
	}
	e.apiStatus = redactIdentifiers(body.Error.Status)
	e.message = redactIdentifiers(body.Error.Message)
	e.message = truncateRunes(e.message, maxOSLoginErrorMessage)
	return e
}

// redactIdentifiers replaces email-like substrings, POSIX-username-like
// words and long digit runs.
func redactIdentifiers(s string) string {
	s = emailLike.ReplaceAllString(s, "[redacted]")
	s = posixNameLike.ReplaceAllString(s, "[redacted]")
	return longNumber.ReplaceAllString(s, "[redacted]")
}

// truncateRunes shortens s to at most limit bytes, cutting on a rune
// boundary, and marks the cut.
func truncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func requestSignedCertificate(ctx context.Context, projectID, region, instanceID, serviceAccount, publicKey, token string) (string, error) {
	url := fmt.Sprintf("%s/v1beta/projects/%s/locations/%s:signSshPublicKey", osLoginBaseURL, projectID, region)

	bodyData := map[string]string{
		"sshPublicKey":     publicKey,
		"serviceAccount":   serviceAccount,
		"cloudRunResource": fmt.Sprintf("projects/%s/locations/%s/instances/%s", projectID, region, instanceID),
	}
	jsonBody, err := json.Marshal(bodyData)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Add("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", newOSLoginAPIError(resp)
	}

	var resData struct {
		SignedSshPublicKey string `json:"signedSshPublicKey"`
	}
	err = json.NewDecoder(resp.Body).Decode(&resData)
	if err != nil {
		return "", err
	}

	return resData.SignedSshPublicKey, nil
}

type IapConn struct {
	ws            *websocket.Conn
	bytesReceived uint64
	readBuffer    bytes.Buffer
	readMutex     sync.Mutex
	writeMutex    sync.Mutex
}

func NewIapConn(ws *websocket.Conn) *IapConn {
	return &IapConn{
		ws: ws,
	}
}

func (c *IapConn) Write(b []byte) (int, error) {
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()

	buf := new(bytes.Buffer)
	_ = binary.Write(buf, binary.BigEndian, uint16(0x0004))
	_ = binary.Write(buf, binary.BigEndian, uint32(len(b)))
	buf.Write(b)

	err := c.ws.WriteMessage(websocket.BinaryMessage, buf.Bytes())
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *IapConn) Read(b []byte) (int, error) {
	c.readMutex.Lock()
	defer c.readMutex.Unlock()

	for c.readBuffer.Len() == 0 {
		mt, msg, err := c.ws.ReadMessage()
		if err != nil {
			return 0, err
		}
		slog.Debug("received IAP tunnel websocket message", "message_type", mt, "length", len(msg))
		if mt != websocket.BinaryMessage {
			continue
		}

		if len(msg) < 2 {
			continue
		}

		tag := binary.BigEndian.Uint16(msg[0:2])
		payload := msg[2:]
		slog.Debug("received IAP tunnel frame", "tag", tag, "payload_length", len(payload))

		switch tag {
		case 0x0001:
		case 0x0002:
		case 0x0004:
			if len(payload) < 4 {
				continue
			}
			dataLen := binary.BigEndian.Uint32(payload[0:4])
			data := payload[4:]
			if uint32(len(data)) < dataLen {
				continue
			}

			c.readBuffer.Write(data[:dataLen])
			c.bytesReceived += uint64(dataLen)

			ackBuf := new(bytes.Buffer)
			_ = binary.Write(ackBuf, binary.BigEndian, uint16(0x0007))
			_ = binary.Write(ackBuf, binary.BigEndian, uint64(c.bytesReceived))
			c.writeMutex.Lock()
			_ = c.ws.WriteMessage(websocket.BinaryMessage, ackBuf.Bytes())
			c.writeMutex.Unlock()

		case 0x0007:
		}
	}

	return c.readBuffer.Read(b)
}

func (c *IapConn) Close() error {
	return c.ws.Close()
}

func (c *IapConn) LocalAddr() net.Addr {
	return c.ws.LocalAddr()
}

func (c *IapConn) RemoteAddr() net.Addr {
	return c.ws.RemoteAddr()
}

func (c *IapConn) SetDeadline(t time.Time) error {
	return nil
}

func (c *IapConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *IapConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func (c *IapConn) Handshake() error {
	c.readMutex.Lock()
	defer c.readMutex.Unlock()

	for {
		mt, msg, err := c.ws.ReadMessage()
		if err != nil {
			return fmt.Errorf("read message error: %w", err)
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		if len(msg) < 2 {
			continue
		}
		tag := binary.BigEndian.Uint16(msg[0:2])
		if tag == 0x0001 || tag == 0x0002 {
			return nil
		}
	}
}
