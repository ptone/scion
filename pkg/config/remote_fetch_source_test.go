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

package config

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusedRemoteSources are template sources that every fetch path (CLI, hub,
// broker) must refuse before any rclone or network code runs.
var refusedRemoteSources = []struct {
	name string
	uri  string
}{
	{"local backend", ":local:/etc"},
	{"local backend relative", ":local:etc"},
	{"alias backend", ":alias:/etc"},
	{"alias with remote parameter", ":alias,remote=/etc:"},
	{"crypt wrapping local", ":crypt,remote=':local:/',password=x:"},
	{"union", ":union,upstreams='/etc /tmp':"},
	{"combine", ":combine:x"},
	{"chunker", ":chunker,remote=/etc:"},
	{"hasher", ":hasher,remote=/etc:"},
	{"compress", ":compress,remote=/etc:"},
	{"cache", ":cache,remote=/etc:"},
	{"memory", ":memory:x"},
	{"sftp", ":sftp,host=169.254.169.254:x"},
	{"http backend", ":http,url='http://169.254.169.254/':"},
	{"s3", ":s3:bucket/x"},
	{"long gcs name", ":googlecloudstorage:bucket/x"},
	{"uppercase gcs", ":GCS:bucket/x"},
	{"gcs metadata endpoint", ":gcs,endpoint=http://169.254.169.254:bucket/x"},
	{"gcs loopback endpoint", ":gcs,endpoint=127.0.0.1:8080:bucket/x"},
	{"gcs service account file", ":gcs,service_account_file=/etc/key.json:bucket/x"},
	{"gcs env auth", ":gcs,env_auth:bucket/x"},
	{"gcs anonymous parameter", ":gcs,anonymous=true:bucket/x"},
	{"gcs host path", ":gcs:/etc"},
	{"gcs dotdot", ":gcs:bucket/../x"},
	{"gcs empty segment", ":gcs:bucket//x"},
	{"gcs colon in path", ":gcs:bucket/a:b"},
	{"gcs backslash", `:gcs:bucket\x`},
	{"gcs bad bucket", ":gcs:B"},
	{"gcs no path", ":gcs:"},
	{"empty backend", "::x"},
	{"no path separator", ":gcs"},
	{"metadata archive http", "http://169.254.169.254/latest/meta-data/x.tar.gz"},
	{"metadata archive https", "https://169.254.169.254/x.tgz"},
	{"loopback archive", "https://127.0.0.1/x.tgz"},
	{"localhost archive", "https://localhost/x.zip"},
	{"private archive", "https://10.0.0.5/x.zip"},
	{"ipv6 loopback archive", "https://[::1]/x.zip"},
	{"mapped metadata archive", "https://[::ffff:169.254.169.254]/x.zip"},
	{"http archive", "http://example.com/x.tgz"},
	{"archive with userinfo", "https://user:pw@example.com/x.tgz"},
	{"github http", "http://github.com/o/r/tree/main/t"},
	{"github userinfo", "https://x:tok@github.com/o/r/tree/main/t"},
	{"unknown http host", "https://example.com/templates"},
}

// acceptedRemoteSources parse and validate; nothing here touches the network.
var acceptedRemoteSources = []string{
	":gcs:my-bucket/templates/x",
	":gcs:my-bucket/templates/x/",
	":gcs:my-bucket",
	":gcs:my.bucket_1/a/b-c",
	"https://github.com/o/r/tree/main/.scion/templates/x",
	"https://example.com/t.tgz",
	"https://example.com/t.zip",
}

func TestParseRcloneRemote(t *testing.T) {
	r, err := ParseRcloneRemote(":gcs:my-bucket/templates/x")
	require.NoError(t, err)
	assert.Equal(t, RcloneRemote{Backend: "gcs", Path: "my-bucket/templates/x"}, r)

	for _, tc := range refusedRemoteSources {
		if !strings.HasPrefix(tc.uri, ":") {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRcloneRemote(tc.uri)
			require.Error(t, err)
		})
	}
}

func TestParseRcloneRemote_ErrorsDoNotRepeatSource(t *testing.T) {
	for _, uri := range []string{
		":gcs,access_token=SECRETVALUE:bucket/x",
		":s3,secret_access_key=SECRETVALUE:bucket/x",
		":SECRETVALUE!:x",
		":gcs:SECRETVALUE/../x",
	} {
		_, err := ParseRcloneRemote(uri)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "SECRETVALUE")
	}
}

func TestValidateRemoteFetchSource_ErrorsDoNotRepeatSource(t *testing.T) {
	for _, uri := range []string{
		"https://github.com/SECRETVALUE",
		"https://github.com/",
		"https://x:SECRETVALUE@github.com/o/r/tree/main/t",
		"http://github.com/SECRETVALUE/r/tree/main/t",
		"https://SECRETVALUE.example/t.tgz?x=SECRETVALUE",
		"https://127.0.0.1/SECRETVALUE.tgz",
		"https://example.com/SECRETVALUE",
		":gcs,endpoint=SECRETVALUE:b/x",
	} {
		err := ValidateRemoteFetchSource(uri)
		require.ErrorIs(t, err, ErrRemoteSourceNotAccepted, uri)
		assert.NotContains(t, err.Error(), "SECRETVALUE", uri)
	}
}

func TestParseRcloneRemote_NamesRefusedBackend(t *testing.T) {
	_, err := ParseRcloneRemote(":local:/etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"local"`)
	assert.Contains(t, err.Error(), "accepted: gcs")

	_, err = ParseRcloneRemote(":alias,remote=/etc:")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection-string parameters")
}

func TestValidateRemoteFetchSource(t *testing.T) {
	for _, tc := range refusedRemoteSources {
		t.Run("refused/"+tc.name, func(t *testing.T) {
			assert.ErrorIs(t, ValidateRemoteFetchSource(tc.uri), ErrRemoteSourceNotAccepted)
			assert.ErrorIs(t, ValidateRemoteURI(tc.uri), ErrRemoteSourceNotAccepted)
		})
	}
	for _, uri := range acceptedRemoteSources {
		t.Run("accepted/"+uri, func(t *testing.T) {
			assert.NoError(t, ValidateRemoteFetchSource(uri))
			assert.NoError(t, ValidateRemoteURI(uri))
		})
	}
}

// recordRcloneFetch replaces the rclone copy step and records each call.
func recordRcloneFetch(t *testing.T) *[]RemoteFetchCredentials {
	t.Helper()
	var calls []RemoteFetchCredentials
	restore := SetRcloneFetchForTest(func(_ context.Context, remote RcloneRemote, creds RemoteFetchCredentials, destPath string) error {
		calls = append(calls, creds)
		require.NoError(t, os.MkdirAll(filepath.Join(destPath, "tpl"), 0o755))
		return os.WriteFile(filepath.Join(destPath, "tpl", "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644)
	})
	t.Cleanup(restore)
	return &calls
}

func TestFetchRemoteTemplate_RefusesBeforeFetching(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	calls := recordRcloneFetch(t)
	cacheDir, err := remoteCacheDir()
	require.NoError(t, err)

	for _, tc := range refusedRemoteSources {
		t.Run(tc.name, func(t *testing.T) {
			for _, opts := range []RemoteFetchOptions{{}, {Credentials: RemoteFetchCallerCredentials}} {
				_, err := FetchRemoteTemplate(context.Background(), tc.uri, opts)
				require.ErrorIs(t, err, ErrRemoteSourceNotAccepted)
			}
		})
	}
	assert.Empty(t, *calls, "a refused source must never reach rclone")
	_, statErr := os.Stat(cacheDir)
	assert.True(t, os.IsNotExist(statErr), "a refused source must not create the cache dir")
}

func TestFetchRemoteTemplate_RcloneCredentialMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := recordRcloneFetch(t)

	_, err := FetchRemoteTemplate(context.Background(), ":gcs:my-bucket/t", RemoteFetchOptions{})
	require.NoError(t, err)
	_, err = FetchRemoteTemplate(context.Background(), ":gcs:my-bucket/t", RemoteFetchOptions{Credentials: RemoteFetchAnonymous})
	require.NoError(t, err)
	_, err = FetchRemoteTemplate(context.Background(), ":gcs:my-bucket/t", RemoteFetchOptions{Credentials: RemoteFetchCallerCredentials})
	require.NoError(t, err)

	assert.Equal(t, []RemoteFetchCredentials{RemoteFetchAnonymous, RemoteFetchAnonymous, RemoteFetchCallerCredentials}, *calls,
		"the zero value must be anonymous")
}

// The broker resolves templates through FindTemplateInProjectPath and
// GetTemplateChainInProject: both must refuse the sources and fetch
// anonymously. FindTemplateWithContext (shared) defaults to anonymous; only
// FindTemplateWithCredentials with an explicit caller mode uses caller
// credentials.
func TestTemplateResolution_RemoteSourceRulesAndCredentialMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := recordRcloneFetch(t)
	projectDir := filepath.Join(t.TempDir(), ".scion")
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "templates"), 0o755))

	for _, tc := range refusedRemoteSources {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FindTemplateInProjectPath(tc.uri, projectDir)
			assert.Error(t, err)
			_, err = GetTemplateChainInProject(tc.uri, projectDir)
			assert.Error(t, err)
			_, err = FindTemplateInProjectPath(tc.uri, "")
			assert.Error(t, err)
			_, err = FindTemplateWithCredentials(context.Background(), tc.uri, RemoteFetchCallerCredentials)
			assert.Error(t, err)
		})
	}
	require.Empty(t, *calls, "a refused source must never reach rclone")

	const src = ":gcs:my-bucket/t"
	_, err := FindTemplateInProjectPath(src, projectDir)
	require.NoError(t, err)
	_, err = GetTemplateChainInProject(src, projectDir)
	require.NoError(t, err)
	_, err = FindTemplateInProjectPath(src, "")
	require.NoError(t, err)
	_, err = FindTemplateWithContext(context.Background(), src)
	require.NoError(t, err)
	_, err = FindTemplate(src)
	require.NoError(t, err)
	assert.Equal(t, []RemoteFetchCredentials{
		RemoteFetchAnonymous, RemoteFetchAnonymous, RemoteFetchAnonymous, RemoteFetchAnonymous, RemoteFetchAnonymous,
	}, *calls)

	*calls = nil
	_, err = FindTemplateWithCredentials(context.Background(), src, RemoteFetchCallerCredentials)
	require.NoError(t, err)
	assert.Equal(t, []RemoteFetchCredentials{RemoteFetchCallerCredentials}, *calls)
}

// rcloneEnvOverrides would change gcs behaviour if the environment fed the
// fetch configuration.
var rcloneEnvOverrides = map[string]string{
	"RCLONE_GCS_ANONYMOUS":                   "false",
	"RCLONE_GCS_ENV_AUTH":                    "true",
	"RCLONE_GCS_SERVICE_ACCOUNT_FILE":        "/nonexistent/key.json",
	"RCLONE_GCS_SERVICE_ACCOUNT_CREDENTIALS": "{}",
	"RCLONE_GCS_ACCESS_TOKEN":                "tok",
	"RCLONE_GCS_ENDPOINT":                    "http://169.254.169.254",
	"RCLONE_GCS_TOKEN":                       `{"access_token":"tok"}`,
}

func TestRcloneFetchConfig_FixedInCode(t *testing.T) {
	for k, v := range rcloneEnvOverrides {
		t.Setenv(k, v)
	}
	info, err := fs.Find("gcs")
	require.NoError(t, err)
	rule := allowedRcloneBackends["gcs"]

	anon := rcloneFetchConfig(info.Options, rule, RemoteFetchAnonymous)
	assert.Equal(t, "true", anon["anonymous"])
	caller := rcloneFetchConfig(info.Options, rule, RemoteFetchCallerCredentials)
	assert.Equal(t, "false", caller["anonymous"])
	for _, m := range []map[string]string{anon, caller} {
		assert.Equal(t, "false", m["env_auth"])
		assert.Empty(t, m["service_account_file"])
		assert.Empty(t, m["service_account_credentials"])
		assert.Empty(t, m["access_token"])
		assert.Empty(t, m["endpoint"])
		assert.Empty(t, m["token"])
	}
}

// TestNewRcloneSourceFs_AnonymousNeverUsesAmbientCredentials shows that an
// anonymous gcs source never consults the process's credentials: with every
// credential source in the environment pointing at a missing file, the
// anonymous source opens (no credential lookup), while caller mode tries
// Application Default Credentials and fails on the missing file. Only the
// bucket is named, so opening the Fs makes no network call.
func TestNewRcloneSourceFs_AnonymousNeverUsesAmbientCredentials(t *testing.T) {
	for k, v := range rcloneEnvOverrides {
		t.Setenv(k, v)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", missing)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RCLONE_CONFIG", filepath.Join(t.TempDir(), "rclone.conf"))
	remote := RcloneRemote{Backend: "gcs", Path: "my-bucket"}

	f, err := newRcloneSourceFs(context.Background(), remote, RemoteFetchAnonymous)
	require.NoError(t, err)
	assert.Equal(t, "my-bucket", f.Root())

	_, err = newRcloneSourceFs(context.Background(), remote, RemoteFetchCallerCredentials)
	require.Error(t, err, "caller mode must use the process's default credentials")
	assert.Contains(t, err.Error(), "missing.json")

	_, err = newRcloneSourceFs(context.Background(), RcloneRemote{Backend: "local", Path: "/"}, RemoteFetchAnonymous)
	require.Error(t, err)
}

func TestIsPublicAddr(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"169.254.1.1", "100.64.0.1", "0.0.0.0", "0.1.2.3", "255.255.255.255",
		"224.0.0.1", "192.0.2.1", "198.18.0.1", "240.0.0.1",
		"::1", "::", "fe80::1", "fc00::1", "fd00:ec2::254", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe",
		"2002:a9fe:a9fe::1", "2001:db8::1", "fec0::1",
		"::a9fe:a9fe", "::7f00:1", "::808:808", // IPv4-compatible (::/96)
		"::ffff:0:a9fe:a9fe", "::ffff:0:808:808", // IPv4-translated (::ffff:0:0:0/96)
		"64:ff9b::808:808", // NAT64 (64:ff9b::/96)
	} {
		assert.False(t, isPublicAddr(netip.MustParseAddr(s)), s)
	}
	for _, s := range []string{"8.8.8.8", "142.250.1.1", "2607:f8b0:4004::1", "::ffff:8.8.8.8"} {
		assert.True(t, isPublicAddr(netip.MustParseAddr(s)), s)
	}
}

func TestCheckPublicDialAddress(t *testing.T) {
	for _, a := range []string{"169.254.169.254:80", "127.0.0.1:443", "[::1]:443", "10.0.0.1:443", "garbage", "example.com:443"} {
		assert.ErrorIs(t, checkPublicDialAddress(a), errNonPublicAddress, a)
	}
	assert.NoError(t, checkPublicDialAddress("8.8.8.8:443"))
	assert.NoError(t, checkPublicDialAddress("[2607:f8b0:4004::1]:443"))
}

// stubArchiveNet replaces name resolution and the final dial of archive
// downloads. dial records each dialed address and fails, so nothing leaves
// the test process.
func stubArchiveNet(t *testing.T, answers map[string][]string) *[]string {
	t.Helper()
	var dialed []string
	prevLookup, prevDial := archiveLookup, archiveDial
	t.Cleanup(func() { archiveLookup, archiveDial = prevLookup, prevDial })
	archiveLookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, s := range answers[host] {
			out = append(out, netip.MustParseAddr(s))
		}
		if len(out) == 0 {
			return nil, errors.New("no such host")
		}
		return out, nil
	}
	archiveDial = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, errors.New("stub dial")
	}
	return &dialed
}

// The internal-range check runs at dial time on the resolved address, so a
// public-looking name that resolves to an internal address is refused even
// though ValidateRemoteFetchSource accepted the URL.
func TestFetchArchive_DialTimeAddressCheck(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(k, "")
	}
	dialed := stubArchiveNet(t, map[string][]string{
		"metadata.example": {"169.254.169.254"},
		"loopback.example": {"127.0.0.1"},
		"mixed.example":    {"8.8.8.8", "10.0.0.1"},
		"v6.example":       {"fd00:ec2::254"},
		"public.example":   {"8.8.8.8"},
	})

	for _, host := range []string{"metadata.example", "loopback.example", "mixed.example", "v6.example"} {
		uri := "https://" + host + "/t.tgz"
		require.NoError(t, ValidateRemoteFetchSource(uri), "the name alone looks public")
		err := fetchArchive(context.Background(), uri, t.TempDir(), RemoteFetchAnonymous)
		require.Error(t, err, host)
		assert.ErrorIs(t, err, errNonPublicAddress, host)
	}
	assert.Empty(t, *dialed, "no connection may be attempted to a non-public address")

	err := fetchArchive(context.Background(), "https://public.example/t.tgz", t.TempDir(), RemoteFetchAnonymous)
	require.Error(t, err)
	assert.Equal(t, []string{"8.8.8.8:443"}, *dialed, "the checked address is the one dialed")
}

func TestArchiveCheckRedirect(t *testing.T) {
	mk := func(s string) *http.Request {
		u, err := url.Parse(s)
		require.NoError(t, err)
		return &http.Request{URL: u}
	}
	via := []*http.Request{mk("https://example.com/t.tgz")}
	assert.NoError(t, archiveCheckRedirect(mk("https://cdn.example.com/t.tgz"), via))
	assert.Error(t, archiveCheckRedirect(mk("http://cdn.example.com/t.tgz"), via))
	assert.Error(t, archiveCheckRedirect(mk("http://169.254.169.254/latest"), via))
	assert.Error(t, archiveCheckRedirect(mk("https://u:p@cdn.example.com/t.tgz"), via))
	assert.Error(t, archiveCheckRedirect(mk("https://cdn.example.com/t.tgz"), make([]*http.Request, maxFetchRedirects)))

	ghVia := []*http.Request{mk("https://github.com/o/r/archive/refs/heads/main.tar.gz")}
	assert.NoError(t, githubCheckRedirect(mk("https://codeload.github.com/o/r/tar.gz/refs/heads/main"), ghVia))
	assert.Error(t, githubCheckRedirect(mk("https://example.com/x"), ghVia))
	assert.Error(t, githubCheckRedirect(mk("http://codeload.github.com/x"), ghVia))
	assert.Error(t, githubCheckRedirect(mk("https://codeload.github.com:8443/x"), ghVia))
	assert.Error(t, githubCheckRedirect(mk("https://169.254.169.254/x"), ghVia))

	c := newArchiveHTTPClient(RemoteFetchAnonymous)
	require.NotNil(t, c.CheckRedirect)
}

// A redirect is followed through the same dial-time check: the redirect
// target resolves to an internal address and is never dialed.
func TestArchiveClient_RedirectTargetCheckedAtDial(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(k, "")
	}
	dialed := stubArchiveNet(t, map[string][]string{"internal.example": {"10.0.0.7"}})
	tr := newArchiveHTTPClient(RemoteFetchAnonymous).Transport.(*http.Transport)
	_, err := tr.DialContext(context.Background(), "tcp", "internal.example:443")
	assert.ErrorIs(t, err, errNonPublicAddress)
	assert.Empty(t, *dialed)
}

// A real redirect from a source server to an internal address is followed
// through the dial-time check and refused; a redirect to http is refused by
// the redirect policy. Nothing is dialed.
func TestFetchArchive_RealRedirectToInternalRefused(t *testing.T) {
	dialed := stubArchiveNet(t, map[string][]string{
		"internal.example": {"10.0.0.7"},
		"metadata.example": {"169.254.169.254"},
	})
	for _, tc := range []struct{ name, target string }{
		{"https to private", "https://internal.example/t.tgz"},
		{"https to metadata", "https://metadata.example/t.tgz"},
		{"https to metadata literal", "https://169.254.169.254/t.tgz"},
		{"http downgrade", "http://internal.example/t.tgz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				http.Redirect(w, r, tc.target, http.StatusFound)
			}))
			t.Cleanup(srv.Close)
			t.Cleanup(AllowArchiveServerForTest(srv.URL))

			err := fetchArchive(context.Background(), srv.URL+"/t.tgz", t.TempDir(), RemoteFetchAnonymous)
			require.Error(t, err)
			assert.Equal(t, 1, hits, "the source server answered with the redirect")
			if strings.HasPrefix(tc.target, "http://") {
				assert.Contains(t, err.Error(), "non-https")
			} else {
				assert.ErrorIs(t, err, errNonPublicAddress)
			}
		})
	}
	assert.Empty(t, *dialed, "no connection to the redirect target")
}

// Hub and broker (anonymous mode) never use a proxy, so the dial-time check
// sees the destination. The CLI (caller mode) may use the environment's
// proxy, but the target is still resolved and checked first.
func TestArchiveClient_ProxyUse(t *testing.T) {
	stubArchiveNet(t, map[string][]string{
		"internal.example": {"10.0.0.7"},
		"public.example":   {"8.8.8.8"},
	})
	prev := archiveEnvProxy
	t.Cleanup(func() { archiveEnvProxy = prev })
	proxyURL, _ := url.Parse("http://Proxy.Corp.example:3128")
	archiveEnvProxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }

	anon := newArchiveHTTPClient(RemoteFetchAnonymous).Transport.(*http.Transport)
	assert.Nil(t, anon.Proxy, "no proxy for hub and broker fetches")

	caller := newArchiveHTTPClient(RemoteFetchCallerCredentials).Transport.(*http.Transport)
	require.NotNil(t, caller.Proxy)
	req := func(s string) *http.Request {
		r, err := http.NewRequest(http.MethodGet, s, nil)
		require.NoError(t, err)
		return r
	}
	_, err := caller.Proxy(req("https://internal.example/t.tgz"))
	assert.ErrorIs(t, err, errNonPublicAddress, "the target is checked before the proxy is used")
	got, err := caller.Proxy(req("https://public.example/t.tgz"))
	require.NoError(t, err)
	assert.Equal(t, proxyURL, got)
	assert.Equal(t, "proxy.corp.example:3128", canonicalProxyAddr(proxyURL))
}

// Fetch-stage errors (download failures, refused redirects) do not repeat
// the source URL or a redirect target.
func TestFetchErrors_DoNotRepeatURL(t *testing.T) {
	stubArchiveNet(t, map[string][]string{"public.example": {"8.8.8.8"}})
	err := fetchArchive(context.Background(), "https://public.example/SECRETPATH/t.tgz?k=SECRETVALUE", t.TempDir(), RemoteFetchAnonymous)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://SECRETHOST.example/SECRETPATH.tgz", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(AllowArchiveServerForTest(srv.URL))
	err = fetchArchive(context.Background(), srv.URL+"/SECRETPATH.tgz", t.TempDir(), RemoteFetchAnonymous)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-https")
	assert.NotContains(t, err.Error(), "SECRET")

	old := http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	http.DefaultClient.Transport = remoteTemplatesRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("stub transport failure")
	})
	err = fetchGitHubTarball(context.Background(), &GitHubURLParts{Owner: "SECRETOWNER", Repo: "SECRETREPO", Branch: "main"}, t.TempDir(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stub transport failure")
	assert.NotContains(t, err.Error(), "SECRET")
}

func TestWithoutRequestURL(t *testing.T) {
	inner := errors.New("boom")
	err := withoutRequestURL(&url.Error{Op: "Get", URL: "https://SECRET.example/x", Err: &url.Error{Op: "Get", URL: "https://SECRET2.example/", Err: inner}})
	assert.Equal(t, "Get: Get: boom", err.Error())
	assert.ErrorIs(t, err, inner)
	assert.Equal(t, inner, withoutRequestURL(inner))
}

// A DNS failure does not name the source host.
func TestResolvePublic_DNSErrorWithoutHost(t *testing.T) {
	prev := archiveLookup
	t.Cleanup(func() { archiveLookup = prev })
	archiveLookup = func(context.Context, string) ([]netip.Addr, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "SECRETHOST.example", IsNotFound: true}
	}
	_, err := resolvePublic(context.Background(), "SECRETHOST.example")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such host")
	assert.NotContains(t, err.Error(), "SECRET")
}
