package cloudrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/run/apiv2/runpb"
)

// guardedHosts are the real Google endpoints the OS Login path could reach.
// No test may dial them; tests inject local servers instead.
var guardedHosts = map[string]bool{
	"oslogin.googleapis.com":   true,
	"metadata.google.internal": true,
	"169.254.169.254":          true,
}

// errGuardedDial is returned for a dial to a guarded host.
var errGuardedDial = errors.New("test dialed a real Google endpoint")

var (
	guardMu      sync.Mutex
	guardedDials []string
)

// TestMain replaces the default HTTP transport with one that refuses, and
// records, any dial to a guarded host, and ignores proxy settings so the
// refusal sees the real host. It also replaces the production credential
// detection and drops any ambient metadata host, so no test can use ambient
// credentials. A run that dialed a guarded host fails.
func TestMain(m *testing.M) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		if guardedHosts[host] {
			guardMu.Lock()
			guardedDials = append(guardedDials, host)
			guardMu.Unlock()
			return nil, errGuardedDial
		}
		return dialer.DialContext(ctx, network, addr)
	}
	http.DefaultTransport = base
	// Without an override the metadata client dials 169.254.169.254, which
	// the guard refuses; tests that need it set their own local host.
	_ = os.Unsetenv("GCE_METADATA_HOST")
	defaultCredentials = func() (tokenSource, error) {
		return nil, errors.New("tests must inject a token source")
	}

	code := m.Run()
	guardMu.Lock()
	if len(guardedDials) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: tests dialed real Google endpoints: %v\n", guardedDials)
		code = 1
	}
	guardMu.Unlock()
	os.Exit(code)
}

func TestDialGuard_RefusesRealOSLogin(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://oslogin.googleapis.com/v1/users/x:importSshPublicKey", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = http.DefaultClient.Do(req)
	if !errors.Is(err, errGuardedDial) {
		t.Fatalf("dial to the real OS Login host: err = %v, want the guard error", err)
	}
	// This dial was deliberate; clear it so the run does not fail.
	guardMu.Lock()
	guardedDials = guardedDials[:len(guardedDials)-1]
	guardMu.Unlock()
}

func newImportTestConnector(t *testing.T, sa string) (*IAPExecConnector, *osLoginServer, *testClock) {
	t.Helper()
	clock := newTestClock()
	srv := startOSLoginServer(t, clock, 5*time.Minute)
	fake := &fakeInstanceGetter{insts: map[string]*runpb.Instance{testInstanceName("agent-1"): {ServiceAccount: sa, Uid: "u1"}}}
	return newTestConnector(fakeClient(fake), clock), srv, clock
}

func TestSignedKey_CacheMissImportsThenSigns(t *testing.T) {
	c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	ctx := context.Background()

	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatalf("signedKeyForInstance: %v", err)
	}
	if got := srv.callOrder(); len(got) != 2 || got[0] != "import" || got[1] != "sign" {
		t.Fatalf("OS Login calls = %v, want [import sign]", got)
	}
	imp := srv.importReq(0)
	if want := "/v1/users/" + testCallerEmail + ":importSshPublicKey"; imp.path != want {
		t.Errorf("import path = %q, want %q", imp.path, want)
	}
	if imp.projectID != testProject {
		t.Errorf("import projectId = %q, want %q", imp.projectID, testProject)
	}
	if imp.auth != "Bearer tok" {
		t.Errorf("import Authorization = %q, want the caller's token", imp.auth)
	}
	if imp.body["key"] == "" || imp.body["key"] != srv.body(0)["sshPublicKey"] {
		t.Error("imported key is not the key that was signed")
	}

	// Cache hit: neither import nor sign.
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatalf("cached call: %v", err)
	}
	if got := srv.callOrder(); len(got) != 2 {
		t.Errorf("OS Login calls after a cache hit = %v, want no new calls", got)
	}

	// A miss inside the import window signs without importing.
	c.Forget(testProject, testLocation, "agent-1")
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatalf("after Forget: %v", err)
	}
	if got := srv.callOrder(); len(got) != 3 || got[2] != "sign" {
		t.Fatalf("OS Login calls = %v, want [import sign sign]", got)
	}

	// After the window, the next miss imports its own fresh key, then signs.
	clock.advance(importWindow)
	c.Forget(testProject, testLocation, "agent-1")
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	if got := srv.callOrder(); len(got) != 5 || got[3] != "import" || got[4] != "sign" {
		t.Fatalf("OS Login calls = %v, want [import sign sign import sign]", got)
	}
	if srv.importReq(1).body["key"] != srv.body(2)["sshPublicKey"] || srv.importReq(1).body["key"] == imp.body["key"] {
		t.Error("miss after the window did not import its own fresh key")
	}
}

func TestSignedKey_OneImportPerWindowAcrossInstances(t *testing.T) {
	clock := newTestClock()
	srv := startOSLoginServer(t, clock, 30*time.Minute)
	insts := map[string]*runpb.Instance{}
	for i := 1; i <= 4; i++ {
		insts[testInstanceName(fmt.Sprintf("agent-%d", i))] = &runpb.Instance{ServiceAccount: "sa@x", Uid: fmt.Sprintf("u%d", i)}
	}
	c := newTestConnector(fakeClient(&fakeInstanceGetter{insts: insts}), clock)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, fmt.Sprintf("agent-%d", i), "tok"); err != nil {
			t.Fatal(err)
		}
		clock.advance(time.Minute)
	}
	if srv.importCount() != 1 || srv.count() != 3 {
		t.Fatalf("inside one window: imports = %d, signs = %d; want 1 and 3", srv.importCount(), srv.count())
	}
	clock.advance(importWindow)
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-4", "tok"); err != nil {
		t.Fatal(err)
	}
	if srv.importCount() != 2 {
		t.Errorf("after the window: imports = %d, want 2", srv.importCount())
	}
	if got := srv.callOrder(); got[len(got)-2] != "import" || got[len(got)-1] != "sign" {
		t.Errorf("OS Login calls = %v, want the last miss to import then sign", got)
	}
}

// signMissingAccount is the sign response for a caller without a POSIX
// account.
const signMissingAccount = `{"error":{"code":404,"status":"NOT_FOUND","message":"No POSIX account provisioned yet for the current user in this project"}}`

func TestSignedKey_MissingAccountReimportsAndRetriesOnce(t *testing.T) {
	c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	ctx := context.Background()
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatal(err)
	}

	// Past the gap but inside the window, the miss skips the import; the
	// sign 404 forces one import of the same fresh key and one retry.
	clock.advance(importMinGap)
	c.Forget(testProject, testLocation, "agent-1")
	srv.failSignN(1, http.StatusNotFound, signMissingAccount)
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatalf("retry after re-import: %v", err)
	}
	want := []string{"import", "sign", "sign", "import", "sign"}
	if got := srv.callOrder(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("OS Login calls = %v, want %v", got, want)
	}
	if srv.importReq(1).body["key"] != srv.body(1)["sshPublicKey"] || srv.body(1)["sshPublicKey"] != srv.body(2)["sshPublicKey"] {
		t.Error("re-import and retry did not use the miss's own key")
	}
}

func TestSignedKey_SecondMissingAccountFailsClosed(t *testing.T) {
	c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	ctx := context.Background()
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatal(err)
	}
	clock.advance(importMinGap)
	c.Forget(testProject, testLocation, "agent-1")
	srv.failSign(http.StatusNotFound, signMissingAccount)

	_, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok")
	if err == nil {
		t.Fatal("signedKeyForInstance succeeded after two missing-account responses, want error")
	}
	want := []string{"import", "sign", "sign", "import", "sign"}
	if got := srv.callOrder(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("OS Login calls = %v, want %v", got, want)
	}
	if !c.lastImportOK.IsZero() || len(c.signedKeys) != 0 {
		t.Errorf("after failing closed: import window cleared = %v, cached keys = %d; want true and 0", c.lastImportOK.IsZero(), len(c.signedKeys))
	}

	// The next exec after the gap imports again.
	clock.advance(importMinGap)
	srv.failSign(0, "")
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatal(err)
	}
	if got := srv.callOrder(); len(got) != 7 || got[5] != "import" {
		t.Errorf("OS Login calls = %v, want the next exec to import", got)
	}
}

func TestSignedKey_MissingAccountInsideGapFailsClosed(t *testing.T) {
	c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	ctx := context.Background()
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatal(err)
	}
	clock.advance(10 * time.Second)
	c.Forget(testProject, testLocation, "agent-1")
	srv.failSign(http.StatusNotFound, signMissingAccount)

	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err == nil {
		t.Fatal("signedKeyForInstance succeeded after a 404 inside the gap, want error")
	}
	if got := srv.callOrder(); fmt.Sprint(got) != "[import sign sign]" {
		t.Fatalf("OS Login calls = %v, want [import sign sign] (no import inside the gap)", got)
	}

	// After the gap the next import goes through.
	clock.advance(importMinGap)
	srv.failSign(0, "")
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatal(err)
	}
	if srv.importCount() != 2 {
		t.Errorf("imports after the gap = %d, want 2", srv.importCount())
	}
}

func TestSignedKey_404WithOtherStatusDoesNotReimport(t *testing.T) {
	c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	ctx := context.Background()
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatal(err)
	}
	clock.advance(importMinGap)
	c.Forget(testProject, testLocation, "agent-1")
	srv.failSign(http.StatusNotFound, `{"error":{"code":404,"status":"FAILED_PRECONDITION","message":"instance not found"}}`)
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err == nil {
		t.Fatal("signedKeyForInstance succeeded after a sign 404, want error")
	}
	if got := srv.callOrder(); fmt.Sprint(got) != "[import sign sign]" {
		t.Errorf("OS Login calls = %v, want [import sign sign]", got)
	}
}

// TestSignedKey_StaleMissingAccountKeepsNewerImport holds one miss's sign
// while another miss gets a 404, re-imports and succeeds; the held sign then
// answers 404 too. Its compare-and-clear must keep the newer import, so it
// retries without a second import and succeeds.
func TestSignedKey_StaleMissingAccountKeepsNewerImport(t *testing.T) {
	clock := newTestClock()
	srv := startOSLoginServer(t, clock, 30*time.Minute)
	// Registered after the server, so it runs before the server closes and
	// the held sign never blocks the close, even when the test fails early.
	arrived, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseSign := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseSign)
	c := newTestConnector(fakeClient(&fakeInstanceGetter{insts: map[string]*runpb.Instance{
		testInstanceName("agent-1"): {ServiceAccount: "sa@x", Uid: "u1"},
		testInstanceName("agent-2"): {ServiceAccount: "sa@x", Uid: "u2"},
	}}), clock)
	if _, err := c.signedKeyForInstance(context.Background(), testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatal(err)
	}
	// Bounds both misses: the import slot wait and every request.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clock.advance(importMinGap)
	c.Forget(testProject, testLocation, "agent-1")

	// Signs 2 (A) and 3 (B) answer 404; sign 2's answer is held.
	srv.failSignN(2, http.StatusNotFound, signMissingAccount)
	srv.onRequest(func(ctx context.Context, kind string, n int) {
		if kind == "sign" && n == 2 {
			close(arrived)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	})
	errA := make(chan error, 1)
	go func() {
		_, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok")
		errA <- err
	}()
	select {
	case <-arrived:
	case err := <-errA:
		t.Fatalf("miss A finished before its sign was held: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("miss A's sign never reached the server")
	}
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-2", "tok"); err != nil {
		t.Fatalf("miss B: %v", err)
	}
	releaseSign()
	select {
	case err := <-errA:
		if err != nil {
			t.Errorf("miss A after the newer import: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("miss A never finished after release")
	}
	if srv.importCount() != 2 {
		t.Errorf("imports = %d, want 2 (the first and exactly one re-import)", srv.importCount())
	}
}

func TestSignedKey_OtherSignFailureDoesNotReimport(t *testing.T) {
	c, srv, _ := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	srv.failSign(http.StatusForbidden, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied"}}`)
	if _, err := c.signedKeyForInstance(context.Background(), testProject, testLocation, "agent-1", "tok"); err == nil {
		t.Fatal("signedKeyForInstance succeeded after a sign 403, want error")
	}
	if got := srv.callOrder(); fmt.Sprint(got) != "[import sign]" {
		t.Errorf("OS Login calls = %v, want [import sign]", got)
	}
}

func TestAuthFailureClearsEntryAndImportWindow(t *testing.T) {
	c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	ctx := context.Background()
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatal(err)
	}
	authErr := fmt.Errorf("SSH command failed: %w: exit status 255", errSSHAuthFailed)
	_ = c.runWithAuthRetry(testProject, testLocation, "agent-1", false, func() error { return authErr })
	if len(c.signedKeys) != 0 || !c.lastImportOK.IsZero() {
		t.Fatalf("after an auth failure: cached keys = %d, import window cleared = %v; want 0 and true", len(c.signedKeys), c.lastImportOK.IsZero())
	}
	clock.advance(importMinGap)
	if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatal(err)
	}
	if got := srv.callOrder(); fmt.Sprint(got) != "[import sign import sign]" {
		t.Errorf("OS Login calls = %v, want the next miss to import again", got)
	}
}

// TestSignedKey_ConcurrentMissesShareOneImport runs misses for the same and
// for different instances at once. Run it with -race.
func TestSignedKey_ConcurrentMissesShareOneImport(t *testing.T) {
	clock := newTestClock()
	srv := startOSLoginServer(t, clock, 30*time.Minute)
	c := newTestConnector(fakeClient(&fakeInstanceGetter{insts: map[string]*runpb.Instance{
		testInstanceName("agent-1"): {ServiceAccount: "sa@x", Uid: "u1"},
		testInstanceName("agent-2"): {ServiceAccount: "sa@x", Uid: "u2"},
	}}), clock)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for _, id := range []string{"agent-1", "agent-1", "agent-2", "agent-2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := c.signedKeyForInstance(ctx, testProject, testLocation, id, "tok"); err != nil {
				errs <- err
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if srv.importCount() != 1 {
		t.Errorf("imports = %d, want 1", srv.importCount())
	}
	if got := srv.callOrder(); len(got) == 0 || got[0] != "import" {
		t.Errorf("OS Login calls = %v, want the import first", got)
	}
}

func TestSignedKey_ImportFailureFailsClosed(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
			srv.failImport(status, `{"error":{"code":`+strconv.Itoa(status)+`,"status":"DENIED","message":"no"}}`)

			_, err := c.signedKeyForInstance(context.Background(), testProject, testLocation, "agent-1", "tok")
			if err == nil {
				t.Fatal("signedKeyForInstance succeeded after a failed import, want error")
			}
			if !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Errorf("error = %q, want it to carry status %d", err, status)
			}
			if srv.count() != 0 {
				t.Errorf("sign requests = %d, want 0", srv.count())
			}
			if srv.importCount() != 1 {
				t.Errorf("import requests = %d, want 1", srv.importCount())
			}
			if len(c.signedKeys) != 0 {
				t.Errorf("cached signed keys = %d, want 0", len(c.signedKeys))
			}
			if !c.lastImportOK.IsZero() {
				t.Error("import window not cleared after a failed import")
			}
			// The next exec after the gap imports again.
			clock.advance(importMinGap)
			srv.failImport(0, "")
			if _, err := c.signedKeyForInstance(context.Background(), testProject, testLocation, "agent-1", "tok"); err != nil {
				t.Fatal(err)
			}
			if srv.importCount() != 2 {
				t.Errorf("import requests = %d, want 2", srv.importCount())
			}
		})
	}
}

func TestSignedKey_UnresolvedCallerFailsClosed(t *testing.T) {
	c, srv, _ := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	c.callerEmail = func(context.Context) (string, error) { return "", errors.New("no email") }

	if _, err := c.signedKeyForInstance(context.Background(), testProject, testLocation, "agent-1", "tok"); err == nil {
		t.Fatal("signedKeyForInstance succeeded without a caller identity, want error")
	}
	if got := srv.callOrder(); len(got) != 0 {
		t.Errorf("OS Login calls = %v, want none", got)
	}
}

// TestExec_ImportFailureFailsExec drives the real exec path, with no ssh
// runner injected: the failed import must end the exec before any tunnel or
// ssh is started.
func TestExec_ImportFailureFailsExec(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			c, srv, _ := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
			c.detectCredentials = func() (tokenSource, error) { return fakeTokenSource{}, nil }
			srv.failImport(status, "")

			_, err := c.Exec(context.Background(), testProject, testLocation, "agent-1", []string{"true"})
			if err == nil {
				t.Fatal("Exec succeeded after a failed import, want error")
			}
			if !strings.Contains(err.Error(), "import SSH key") {
				t.Errorf("error = %q, want it to report the failed import", err)
			}
			if srv.count() != 0 {
				t.Errorf("sign requests = %d, want 0", srv.count())
			}
		})
	}
}

func TestImport_ExpiryBoundedToMinutes(t *testing.T) {
	c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	now := clock.now()

	if _, err := c.signedKeyForInstance(context.Background(), testProject, testLocation, "agent-1", "tok"); err != nil {
		t.Fatal(err)
	}
	raw, ok := srv.importReq(0).body["expirationTimeUsec"]
	if !ok {
		t.Fatal("import request has no expirationTimeUsec")
	}
	usec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("expirationTimeUsec %q: %v", raw, err)
	}
	expiry := time.UnixMicro(usec)
	if !expiry.After(now) || expiry.After(now.Add(10*time.Minute)) {
		t.Errorf("import expiry = %s after now, want within (0, 10m]", expiry.Sub(now))
	}
	if got := expiry.Sub(now); got != importedKeyValidity {
		t.Errorf("import expiry = %s after now, want %s", got, importedKeyValidity)
	}
}

func TestInstanceServiceAccount_RefusesDefaultServiceAccounts(t *testing.T) {
	for _, sa := range []string{
		"123456-compute@developer.gserviceaccount.com",
		"987654321098-compute@developer.gserviceaccount.com",
		"123456-Compute@Developer.GServiceAccount.com",
		"my-project@appspot.gserviceaccount.com",
		"My-Project@AppSpot.gserviceaccount.COM",
	} {
		t.Run(sa, func(t *testing.T) {
			c, srv, _ := newImportTestConnector(t, sa)
			_, err := c.signedKeyForInstance(context.Background(), testProject, testLocation, "agent-1", "tok")
			if err == nil {
				t.Fatal("signedKeyForInstance succeeded for a default service account, want error")
			}
			if !strings.Contains(err.Error(), "project default service account") {
				t.Errorf("error = %q, want it to name the refusal", err)
			}
			if got := srv.callOrder(); len(got) != 0 {
				t.Errorf("OS Login calls = %v, want none", got)
			}
		})
	}
	// Look-alikes that are not default accounts are accepted.
	for _, sa := range []string{"team-compute@my-project.iam.gserviceaccount.com", "appspot@my-project.iam.gserviceaccount.com"} {
		c, _, _ := newImportTestConnector(t, sa)
		if _, err := c.signedKeyForInstance(context.Background(), testProject, testLocation, "agent-1", "tok"); err != nil {
			t.Errorf("%s refused: %v", sa, err)
		}
	}
}

// captureLogs routes slog's default logger, at debug level, into a buffer
// for the rest of the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// errBodyWithIdentifiers is an API error that echoes the caller and numbers.
const errBodyWithIdentifiers = `{"error":{"code":404,"status":"NOT_FOUND","message":"No POSIX account provisioned yet for the current user in this project (broker-runner@my-project.iam.gserviceaccount.com, broker-runner%40my-project.iam.gserviceaccount.com, user broker_runner_my_project_iam_gserv uid 3141592653, project 123456789012)"}}`

func TestImportAndSign_LogsAndErrorsCarryNoSecrets(t *testing.T) {
	logs := captureLogs(t)
	c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	ctx := context.Background()

	key, err := c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok")
	if err != nil {
		t.Fatal(err)
	}
	pubKeyBlob := strings.Fields(srv.importReq(0).body["key"])[1]

	clock.advance(importWindow)
	c.Forget(testProject, testLocation, "agent-1")
	srv.failImport(http.StatusForbidden, strings.Replace(errBodyWithIdentifiers, "404", "403", 1))
	importErr := fmt.Sprint(c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"))

	srv.failImport(0, "")
	srv.failSign(http.StatusNotFound, errBodyWithIdentifiers)
	signErr := fmt.Sprint(c.signedKeyForInstance(ctx, testProject, testLocation, "agent-1", "tok"))

	secrets := map[string]string{
		"public key":     pubKeyBlob,
		"private key":    "PRIVATE KEY",
		"private key b":  strings.Split(string(key.privKeyPEM), "\n")[1],
		"caller email":   testCallerEmail,
		"posix username": testPosixUsername,
		"posix uid":      testPosixUID,
		"project number": "123456789012",
		"email domain":   "gserviceaccount.com",
	}
	got := logs.String()
	if !strings.Contains(got, "OS Login key import succeeded") || !strings.Contains(got, "OS Login key import failed") {
		t.Errorf("logs = %q, want the import outcomes", got)
	}
	for what, secret := range secrets {
		if strings.Contains(got, secret) {
			t.Errorf("logs contain the %s", what)
		}
		if strings.Contains(importErr, secret) {
			t.Errorf("import error contains the %s: %q", what, importErr)
		}
		if strings.Contains(signErr, secret) {
			t.Errorf("sign error contains the %s: %q", what, signErr)
		}
	}
	// The redacted message still explains the failure.
	for _, want := range []string{"404", "NOT_FOUND", "No POSIX account provisioned"} {
		if !strings.Contains(signErr, want) {
			t.Errorf("sign error = %q, want it to contain %q", signErr, want)
		}
	}
	if !strings.Contains(importErr, "403") || !strings.Contains(importErr, "No POSIX account provisioned") {
		t.Errorf("import error = %q, want the status and redacted message", importErr)
	}
}

func TestCredentialsEmail(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"service account key", `{"type":"service_account","client_email":"sa@p.iam.gserviceaccount.com"}`, "sa@p.iam.gserviceaccount.com"},
		{"impersonated", `{"type":"impersonated_service_account","service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/target@p.iam.gserviceaccount.com:generateAccessToken","source_credentials":{"type":"service_account","client_email":"src@p.iam.gserviceaccount.com"}}`, "target@p.iam.gserviceaccount.com"},
		{"external account impersonating", `{"type":"external_account","service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/wif@p.iam.gserviceaccount.com:generateAccessToken"}`, "wif@p.iam.gserviceaccount.com"},
		{"authorized user", `{"type":"authorized_user","client_id":"x"}`, ""},
		{"malformed impersonation url", `{"type":"impersonated_service_account","service_account_impersonation_url":"https://example.com/nothing"}`, ""},
		{"not json", `nope`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := credentialsEmail([]byte(tc.json)); got != tc.want {
				t.Errorf("credentialsEmail = %q, want %q", got, tc.want)
			}
		})
	}
}

// startMetadataServer serves the default service account email at
// GCE_METADATA_HOST and counts requests, rejecting any without the
// Metadata-Flavor header.
func startMetadataServer(t *testing.T, email string) *int {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if r.Header.Get("Metadata-Flavor") != "Google" || r.URL.Path != "/computeMetadata/v1/instance/service-accounts/default/email" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Metadata-Flavor", "Google")
		_, _ = fmt.Fprint(w, email)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))
	return &calls
}

func TestResolveCallerEmail(t *testing.T) {
	const metaEmail = "meta-sa@p.iam.gserviceaccount.com"
	creds := func(json string) func() (tokenSource, error) {
		return func() (tokenSource, error) {
			return auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: fakeTokenSource{}, JSON: []byte(json)}), nil
		}
	}
	tests := []struct {
		name         string
		detect       func() (tokenSource, error)
		want         string
		wantErr      bool
		wantMetadata int
	}{
		{"service account key", creds(`{"type":"service_account","client_email":"key-sa@p.iam.gserviceaccount.com"}`), "key-sa@p.iam.gserviceaccount.com", false, 0},
		{"metadata credentials", creds(""), metaEmail, false, 1},
		{"non-JSON token source", func() (tokenSource, error) { return fakeTokenSource{}, nil }, metaEmail, false, 1},
		{"authorized user fails without asking metadata", creds(`{"type":"authorized_user"}`), "", true, 0},
		{"no credentials", func() (tokenSource, error) { return nil, errors.New("none") }, "", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := startMetadataServer(t, metaEmail)
			c := &IAPExecConnector{detectCredentials: tc.detect}
			got, err := c.resolveCallerEmail(context.Background())
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("resolveCallerEmail = %q, %v; want %q, error %v", got, err, tc.want, tc.wantErr)
			}
			if *calls != tc.wantMetadata {
				t.Errorf("metadata requests = %d, want %d", *calls, tc.wantMetadata)
			}
		})
	}
}

func TestRedactIdentifiers(t *testing.T) {
	in := "denied for a.b@x.iam.gserviceaccount.com, a.b%40x.iam.gserviceaccount.com, A.B%40X.COM, name_domain_com uid 1234567 sa_118 PERMISSION_DENIED"
	got := redactIdentifiers(in)
	for _, leak := range []string{"a.b", "%40", "name_domain_com", "1234567", "sa_118", "gserviceaccount"} {
		if strings.Contains(got, leak) {
			t.Errorf("redactIdentifiers(%q) = %q, still contains %q", in, got, leak)
		}
	}
	if !strings.Contains(got, "PERMISSION_DENIED") || !strings.Contains(got, "denied for") {
		t.Errorf("redactIdentifiers dropped the readable parts: %q", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	s := strings.Repeat("é", 200) // 2 bytes each
	got := truncateRunes(s, 301)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateRunes cut inside a rune: %q", got)
	}
	if want := strings.Repeat("é", 150) + "..."; got != want {
		t.Errorf("truncateRunes = %d bytes, want %d", len(got), len(want))
	}
	if got := truncateRunes("short", 300); got != "short" {
		t.Errorf("truncateRunes(short) = %q", got)
	}
}

// TestImport_UnreachableEndpointLeaksNothing points the OS Login endpoint at
// a closed listener: the transport error quotes the URL, which names the
// caller, so neither the error nor the logs may carry it.
func TestImport_UnreachableEndpointLeaksNothing(t *testing.T) {
	logs := captureLogs(t)
	c, srv, _ := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	prev := osLoginBaseURL
	osLoginBaseURL = "http://" + addr
	t.Cleanup(func() { osLoginBaseURL = prev })

	_, err = c.signedKeyForInstance(context.Background(), testProject, testLocation, "agent-1", "tok")
	if err == nil {
		t.Fatal("signedKeyForInstance succeeded against a closed endpoint, want error")
	}
	for _, leak := range []string{testCallerEmail, "%40", "gserviceaccount"} {
		if strings.Contains(err.Error(), leak) || strings.Contains(logs.String(), leak) {
			t.Errorf("error or logs contain %q: %q / %q", leak, err, logs.String())
		}
	}
	if srv.count() != 0 {
		t.Errorf("sign requests = %d, want 0", srv.count())
	}
}

func TestImport_CancellationStaysVisible(t *testing.T) {
	c, _, _ := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.importCallerKey(ctx, testProject, "ssh-rsa AAAA", "tok", time.Unix(1_800_000_000, 0))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("import error = %v, want it to wrap context.Canceled", err)
	}
	if err != nil && strings.Contains(err.Error(), testCallerEmail) {
		t.Errorf("import error names the caller: %q", err)
	}
}

// authFailingRunner returns an ssh runner that, like runSSH, gets a signed
// key for the instance and then reports a client-side authentication
// failure. It counts its runs.
func authFailingRunner(c *IAPExecConnector, runs *int) func(context.Context, string, string, string, []string, io.Reader, io.Writer, io.Writer) error {
	return func(ctx context.Context, project, location, instanceName string, _ []string, _ io.Reader, _, _ io.Writer) error {
		*runs++
		if _, err := c.signedKeyForInstance(ctx, project, location, instanceShortID(instanceName), "tok"); err != nil {
			return err
		}
		return fmt.Errorf("SSH command failed: %w: exit status 255", errSSHAuthFailed)
	}
}

func TestAuthFailureStorm_ImportsBoundedByGap(t *testing.T) {
	t.Run("20 execs inside 60s", func(t *testing.T) {
		c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
		runs := 0
		c.sshRunner = authFailingRunner(c, &runs)
		for i := 0; i < 20; i++ {
			if _, err := c.Exec(context.Background(), testProject, testLocation, "agent-1", []string{"true"}); !errors.Is(err, errSSHAuthFailed) {
				t.Fatalf("exec %d: err = %v, want the auth failure", i, err)
			}
			clock.advance(2 * time.Second)
		}
		if runs != 40 {
			t.Errorf("ssh runs = %d, want 40 (each exec retried once)", runs)
		}
		if srv.importCount() != 1 {
			t.Errorf("imports = %d, want exactly 1", srv.importCount())
		}
	})
	t.Run("20 execs over 190s", func(t *testing.T) {
		c, srv, clock := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
		runs := 0
		c.sshRunner = authFailingRunner(c, &runs)
		start := clock.now()
		for i := 0; i < 20; i++ {
			if i > 0 {
				clock.advance(10 * time.Second)
			}
			_, _ = c.Exec(context.Background(), testProject, testLocation, "agent-1", []string{"true"})
		}
		elapsed := clock.now().Sub(start)
		bound := int(elapsed/importMinGap) + 1
		if n := srv.importCount(); n > bound || n < 2 {
			t.Errorf("imports = %d over %s, want between 2 and %d", n, elapsed, bound)
		}
	})
}

// hangImports makes every import request wait until its client gives up or
// the test ends, and reports each arrival on the returned channel.
func hangImports(t *testing.T, srv *osLoginServer) <-chan struct{} {
	t.Helper()
	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // runs before the server closes
	srv.onRequest(func(ctx context.Context, kind string, _ int) {
		if kind != "import" {
			return
		}
		arrived <- struct{}{}
		select {
		case <-ctx.Done():
		case <-release:
		}
	})
	return arrived
}

func setImportTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := importTimeout
	importTimeout = d
	t.Cleanup(func() { importTimeout = prev })
}

func TestEnsureCallerAccount_HungImportTimesOut(t *testing.T) {
	c, srv, _ := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	hangImports(t, srv)
	setImportTimeout(t, 200*time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := c.ensureCallerAccount(context.Background(), testProject, "ssh-rsa AAAA", "tok")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want the import timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hung import did not time out")
	}
	if !c.lastImportOK.IsZero() {
		t.Error("a timed-out import counted as a success")
	}
}

func TestEnsureCallerAccount_WaiterHonoursItsContext(t *testing.T) {
	c, srv, _ := newImportTestConnector(t, "agent-runner@my-project.iam.gserviceaccount.com")
	arrived := hangImports(t, srv)
	setImportTimeout(t, 10*time.Second)

	go func() { _, _ = c.ensureCallerAccount(context.Background(), testProject, "ssh-rsa AAAA", "tok") }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("first import never reached the server")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.ensureCallerAccount(ctx, testProject, "ssh-rsa BBBB", "tok")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiter err = %v, want its own context's error", err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("waiter returned after %s, want promptly after its context ended", waited)
	}
	if srv.importCount() != 1 {
		t.Errorf("imports = %d, want only the one holding the slot", srv.importCount())
	}
}

// fakeSSH installs a shell script as the ssh client. The script records the
// -E client log path, whether that log was a fresh empty file, and its mode
// as ls shows it, in a record file, then runs body with $log set to the
// client log.
func fakeSSH(t *testing.T, body string) (record string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("fake ssh client needs a POSIX sh: %v", err)
	}
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	script := filepath.Join(dir, "ssh")
	content := "#!/bin/sh\nlog=\"\"\nwhile [ $# -gt 0 ]; do if [ \"$1\" = \"-E\" ]; then log=\"$2\"; shift; fi; shift; done\n" +
		"if [ -f \"$log\" ] && [ ! -s \"$log\" ]; then fresh=fresh; else fresh=stale; fi\n" +
		"mode=$(ls -l \"$log\" | cut -c1-10)\n" +
		"echo \"$log $fresh $mode\" >> '" + record + "'\n" + body + "\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := sshBinary
	sshBinary = script
	t.Cleanup(func() { sshBinary = prev })
	return record
}

// clientRunner returns an ssh runner that runs the real client wrapper, with
// the ssh binary faked, and counts its runs.
func clientRunner(t *testing.T, runs *int) func(context.Context, string, string, string, []string, io.Reader, io.Writer, io.Writer) error {
	return func(ctx context.Context, _, _, _ string, cmdArgs []string, stdin io.Reader, stdout, stderr io.Writer) error {
		*runs++
		return runSSHClient(ctx, t.TempDir(), append([]string{"root@127.0.0.1"}, cmdArgs...), stdin, stdout, stderr)
	}
}

// cachedConnector returns a connector holding a signed key for agent-1 and a
// recorded successful import.
func cachedConnector() *IAPExecConnector {
	return &IAPExecConnector{
		signedKeys:   map[string]*signedKey{testInstanceName("agent-1"): {instanceUID: "u1", reuseUntil: time.Now().Add(time.Hour)}},
		lastImportOK: time.Unix(1_800_000_000, 0),
	}
}

const authFailureLine = "root@127.0.0.1: Permission denied (publickey)."

func TestSSHClient_RemoteForgedAuthFailureKeepsState(t *testing.T) {
	cases := map[string]string{
		"remote prints the marker and exits 255": "echo '" + authFailureLine + "' >&2\nexit 255",
		"exit 255 with an empty client log":      "exit 255",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			fakeSSH(t, body)
			c := cachedConnector()
			runs := 0
			c.sshRunner = clientRunner(t, &runs)

			_, err := c.Exec(context.Background(), testProject, testLocation, "agent-1", []string{"true"})
			if err == nil || errors.Is(err, errSSHAuthFailed) {
				t.Fatalf("err = %v, want a non-auth failure", err)
			}
			if runs != 1 {
				t.Errorf("ssh runs = %d, want 1 (no auth retry)", runs)
			}
			if _, ok := c.signedKeys[testInstanceName("agent-1")]; !ok {
				t.Error("cached signed key dropped by a remote exit 255")
			}
			if c.lastImportOK.IsZero() {
				t.Error("import window cleared by a remote exit 255")
			}
		})
	}
}

func TestSSHClient_ClientAuthFailureClearsState(t *testing.T) {
	fakeSSH(t, "echo '"+authFailureLine+"' >> \"$log\"\nexit 255")
	c := cachedConnector()
	runs := 0
	c.sshRunner = clientRunner(t, &runs)

	_, err := c.Exec(context.Background(), testProject, testLocation, "agent-1", []string{"true"})
	if !errors.Is(err, errSSHAuthFailed) {
		t.Fatalf("err = %v, want the auth failure", err)
	}
	if runs != 2 {
		t.Errorf("ssh runs = %d, want 2 (Exec retries once)", runs)
	}
	if len(c.signedKeys) != 0 || !c.lastImportOK.IsZero() {
		t.Errorf("cached keys = %d, import window cleared = %v; want 0 and true", len(c.signedKeys), c.lastImportOK.IsZero())
	}
	if strings.Contains(err.Error(), "root@") {
		t.Errorf("error carries the client log: %q", err)
	}
}

func TestSSHClient_FreshClientLogPerRun(t *testing.T) {
	record := fakeSSH(t, "echo '"+authFailureLine+"' >> \"$log\"\nexit 255")
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		if err := runSSHClient(context.Background(), dir, []string{"root@127.0.0.1"}, nil, io.Discard, io.Discard); !errors.Is(err, errSSHAuthFailed) {
			t.Fatalf("run %d: err = %v, want the auth failure", i, err)
		}
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("records = %q, want 2", lines)
	}
	paths := map[string]bool{}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) != 3 || f[1] != "fresh" {
			t.Errorf("record %q: want a fresh empty client log", l)
			continue
		}
		if runtime.GOOS != "windows" && f[2] != "-rw-------" {
			t.Errorf("client log mode = %s, want -rw------- (0600)", f[2])
		}
		paths[f[0]] = true
		if _, err := os.Stat(f[0]); !os.IsNotExist(err) {
			t.Errorf("client log %s left behind (stat err %v)", f[0], err)
		}
	}
	if len(paths) != 2 {
		t.Errorf("client log paths = %v, want a distinct path per run", paths)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("leftovers in the session dir: %d entries", len(entries))
	}
}

func TestCreateClientLog_PrivateAndExclusive(t *testing.T) {
	// A permissive umask, so a create that relied on it would show.
	restoreUmask := setPermissiveUmask()
	defer restoreUmask()
	dir := t.TempDir()
	path := filepath.Join(dir, "client.log")
	if err := createClientLog(path); err != nil {
		t.Fatalf("createClientLog: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("client log mode = %o, want 600", mode)
		}
	}

	// An existing file, stale content included, is refused and kept as is.
	if err := os.WriteFile(path, []byte(authFailureLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := createClientLog(path); !errors.Is(err, os.ErrExist) {
		t.Errorf("createClientLog over an existing file: err = %v, want os.ErrExist", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != authFailureLine+"\n" {
		t.Errorf("existing file after the refused create = %q, %v; want it unchanged", got, err)
	}
}

func TestClientLogShowsAuthFailure(t *testing.T) {
	tests := []struct {
		name, log string
		want      bool
	}{
		{"client line", authFailureLine + "\r\n", true},
		{"bare client line", "Permission denied (publickey,keyboard-interactive).\n", true},
		{"after other lines", "Warning: Permanently added host\r\n" + authFailureLine + "\r\n", true},
		{"mid-line", "note: saw Permission denied (publickey earlier\n", false},
		{"other failure", "ssh: connect to host 127.0.0.1 port 1: Connection refused\r\n", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "client.log")
			if err := os.WriteFile(path, []byte(tc.log), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := clientLogShowsAuthFailure(path); got != tc.want {
				t.Errorf("clientLogShowsAuthFailure = %v, want %v", got, tc.want)
			}
		})
	}
}
