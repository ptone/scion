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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// templateRefFixture is a HOME with a global template, a project (.scion)
// with a project template, and a template directory outside both roots
// holding a marker file.
type templateRefFixture struct {
	home, globalTemplates, projectDir, outsideDir string
}

func newTemplateRefFixture(t *testing.T) templateRefFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalTemplates := filepath.Join(home, ".scion", "templates")
	writeTemplateDir(t, filepath.Join(globalTemplates, "g"))
	projectDir := filepath.Join(t.TempDir(), ".scion")
	writeTemplateDir(t, filepath.Join(projectDir, "templates", "p"))
	outside := filepath.Join(t.TempDir(), "outside")
	writeTemplateDir(t, outside)
	require.NoError(t, os.WriteFile(filepath.Join(outside, "marker.txt"), []byte("outside content"), 0o644))
	return templateRefFixture{home: home, globalTemplates: globalTemplates, projectDir: projectDir, outsideDir: outside}
}

func writeTemplateDir(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644))
}

// relTo returns a relative reference from base that reaches target.
func relTo(t *testing.T, base, target string) string {
	t.Helper()
	rel, err := filepath.Rel(base, target)
	require.NoError(t, err)
	return rel
}

func TestValidateRemoteTemplateRef(t *testing.T) {
	for _, ok := range []string{"", "default", "web-dev", "team/web-dev", "0f1e2d3c-uuid-like", ":gcs:my-bucket/templates/x", "https://github.com/acme/repo/tree/main/templates/x"} {
		assert.NoError(t, ValidateRemoteTemplateRef(ok), ok)
	}
	for _, bad := range []string{"/etc", "/home/user/.scion", "../x", "a/../../x", "..", ".", "/a:b", "/x.tgz"} {
		err := ValidateRemoteTemplateRef(bad)
		assert.ErrorIs(t, err, ErrTemplateRefNotAllowed, bad)
	}
	for _, bad := range []string{":local:/etc", "http://169.254.169.254/latest/x.tgz", "https://127.0.0.1/x.tgz"} {
		err := ValidateRemoteTemplateRef(bad)
		assert.ErrorIs(t, err, ErrTemplateRefNotAllowed, bad)
		assert.ErrorIs(t, err, ErrRemoteSourceNotAccepted, bad)
	}
}

func TestFindTemplateInProjectPath_RemoteRefusesAbsoluteAndEscapingRefs(t *testing.T) {
	fx := newTemplateRefFixture(t)

	for _, projectPath := range []string{fx.projectDir, ""} {
		_, err := FindTemplateInProjectPath(fx.outsideDir, projectPath, TemplateRefRemote)
		assert.ErrorIs(t, err, ErrTemplateRefNotAllowed, "absolute ref, projectPath %q", projectPath)
		_, err = GetTemplateChainInProject(fx.outsideDir, projectPath, TemplateRefRemote)
		assert.ErrorIs(t, err, ErrTemplateRefNotAllowed, "absolute ref chain, projectPath %q", projectPath)
	}

	// A relative ref that leaves the project tier and one that leaves the
	// global tier.
	fromProject := relTo(t, filepath.Join(fx.projectDir, "templates"), fx.outsideDir)
	fromGlobal := relTo(t, fx.globalTemplates, fx.outsideDir)
	for _, ref := range []string{fromProject, fromGlobal} {
		for _, projectPath := range []string{fx.projectDir, ""} {
			_, err := FindTemplateInProjectPath(ref, projectPath, TemplateRefRemote)
			assert.ErrorIs(t, err, ErrTemplateRefNotAllowed, "ref %q, projectPath %q", ref, projectPath)
			_, err = GetTemplateChainInProject(ref, projectPath, TemplateRefRemote)
			assert.ErrorIs(t, err, ErrTemplateRefNotAllowed, "chain ref %q, projectPath %q", ref, projectPath)
		}
	}

	// Names inside the roots still resolve.
	tpl, err := FindTemplateInProjectPath("p", fx.projectDir, TemplateRefRemote)
	require.NoError(t, err)
	assert.Equal(t, "project", tpl.Scope)
	tpl, err = FindTemplateInProjectPath("g", fx.projectDir, TemplateRefRemote)
	require.NoError(t, err)
	assert.Equal(t, "global", tpl.Scope)
}

func TestFindTemplateInProjectPath_RemoteRefusesSymlinkedTemplateDir(t *testing.T) {
	fx := newTemplateRefFixture(t)
	require.NoError(t, os.Symlink(fx.outsideDir, filepath.Join(fx.projectDir, "templates", "linked")))
	require.NoError(t, os.Symlink(fx.outsideDir, filepath.Join(fx.globalTemplates, "glinked")))

	for _, name := range []string{"linked", "glinked"} {
		_, err := FindTemplateInProjectPath(name, fx.projectDir, TemplateRefRemote)
		assert.ErrorIs(t, err, ErrTemplateNotFound, name)
	}
	assert.Nil(t, FindTemplateInScope("glinked", "global"))
}

// With an empty project path, a remote lookup searches the global template
// directory only, confined; it never falls back to the CWD-based FindTemplate.
func TestFindTemplateInProjectPath_RemoteEmptyProjectPathIsGlobalOnly(t *testing.T) {
	fx := newTemplateRefFixture(t)
	// A project template reachable from the CWD must not be found.
	t.Chdir(filepath.Dir(fx.projectDir))

	_, err := FindTemplateInProjectPath("p", "", TemplateRefRemote)
	assert.ErrorIs(t, err, ErrTemplateNotFound)
	tpl, err := FindTemplateInProjectPath("g", "", TemplateRefRemote)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(fx.globalTemplates, "g"), tpl.Path)
	_, err = FindTemplateInProjectPath(fx.outsideDir, "", TemplateRefRemote)
	assert.ErrorIs(t, err, ErrTemplateRefNotAllowed)
	_, err = FindTemplateInProjectPath("../outside", "", TemplateRefRemote)
	assert.ErrorIs(t, err, ErrTemplateRefNotAllowed)
}

// The local CLI keeps absolute template directories on its own host.
func TestFindTemplate_LocalAcceptsAbsoluteDir(t *testing.T) {
	fx := newTemplateRefFixture(t)

	tpl, err := FindTemplate(fx.outsideDir)
	require.NoError(t, err)
	assert.Equal(t, fx.outsideDir, tpl.Path)
	tpl, err = FindTemplateInProjectPath(fx.outsideDir, "", templateRefLocal)
	require.NoError(t, err)
	assert.Equal(t, fx.outsideDir, tpl.Path)
	tpl, err = FindTemplateInProjectPath(fx.outsideDir, fx.projectDir, templateRefLocal)
	require.NoError(t, err)
	assert.Equal(t, fx.outsideDir, tpl.Path)

	// Relative refs are confined for the CLI too.
	_, err = FindTemplate(relTo(t, fx.globalTemplates, fx.outsideDir))
	assert.ErrorIs(t, err, ErrTemplateRefNotAllowed)
}

func TestGetTemplateChainForDir_TrustedDirectory(t *testing.T) {
	fx := newTemplateRefFixture(t)
	chain, err := GetTemplateChainForDir(fx.outsideDir, fx.projectDir)
	require.NoError(t, err)
	require.NotEmpty(t, chain)
	assert.Equal(t, fx.outsideDir, chain[len(chain)-1].Path)
}

// A remote URI is detected before the local-path rule and fetched through the
// shared validator in anonymous mode; an absolute path is never taken for a
// URI.
func TestFindTemplateInProjectPath_RemoteURIRouting(t *testing.T) {
	fx := newTemplateRefFixture(t)
	calls := recordRcloneFetch(t)

	_, err := FindTemplateInProjectPath(":gcs:my-bucket/t", fx.projectDir, TemplateRefRemote)
	require.NoError(t, err)
	_, err = FindTemplateInProjectPath(":gcs:my-bucket/t", "", TemplateRefRemote)
	require.NoError(t, err)
	assert.Equal(t, []RemoteFetchCredentials{RemoteFetchAnonymous, RemoteFetchAnonymous}, *calls)

	*calls = nil
	for _, ref := range []string{":local:" + fx.outsideDir, ":alias,remote=" + fx.outsideDir + ":"} {
		_, err := FindTemplateInProjectPath(ref, fx.projectDir, TemplateRefRemote)
		assert.ErrorIs(t, err, ErrRemoteSourceNotAccepted, ref)
	}
	assert.Empty(t, *calls)

	for _, ref := range []string{"/a:b", "/x.tgz"} {
		_, err := FindTemplateInProjectPath(ref, fx.projectDir, TemplateRefRemote)
		assert.ErrorIs(t, err, ErrTemplateRefNotAllowed, ref)
	}
}

func TestFindHarnessConfigDir_RefusesNonLocalNames(t *testing.T) {
	fx := newTemplateRefFixture(t)
	hcOutside := filepath.Join(filepath.Dir(fx.outsideDir), "hc")
	require.NoError(t, os.MkdirAll(hcOutside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hcOutside, "config.yaml"), []byte("harness: generic\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(fx.home, ".scion", "harness-configs"), 0o755))

	escaping := relTo(t, filepath.Join(fx.home, ".scion", "harness-configs"), hcOutside)
	for _, name := range []string{escaping, hcOutside} {
		_, err := FindHarnessConfigDir(name, fx.projectDir, fx.outsideDir)
		var nf *HarnessConfigNotFoundError
		assert.ErrorAs(t, err, &nf, name)
	}
}

func TestResolveContentInChain_ConfinedNoFollow(t *testing.T) {
	fx := newTemplateRefFixture(t)
	tplDir := filepath.Join(fx.projectDir, "templates", "p")
	chain := []*Template{{Name: "p", Path: tplDir}}
	secret := filepath.Join(fx.outsideDir, "marker.txt")

	// A non-local field is inline content: no file is read.
	for _, field := range []string{secret, relTo(t, tplDir, secret)} {
		got, err := ResolveContentInChain(chain, field)
		require.NoError(t, err)
		assert.Equal(t, field, string(got))
	}

	// A symbolic link is refused, never followed and never inline text.
	require.NoError(t, os.Symlink(secret, filepath.Join(tplDir, "system-prompt.md")))
	got, err := ResolveContentInChain(chain, "system-prompt.md")
	assert.ErrorIs(t, err, ErrTemplateContentRefused)
	assert.Nil(t, got)
	present, err := TemplateFileExists(tplDir, "system-prompt.md")
	assert.ErrorIs(t, err, ErrTemplateContentRefused)
	assert.False(t, present)

	// A regular file is read; an absent one stays inline content.
	require.NoError(t, os.WriteFile(filepath.Join(tplDir, "agents.md"), []byte("instructions"), 0o644))
	got, err = ResolveContentInChain(chain, "agents.md")
	require.NoError(t, err)
	assert.Equal(t, "instructions", string(got))
	got, err = ResolveContentInChain(chain, "missing.md")
	require.NoError(t, err)
	assert.Equal(t, "missing.md", string(got))
	present, err = TemplateFileExists(tplDir, "agents.md")
	require.NoError(t, err)
	assert.True(t, present)
}

func TestTemplateLoadConfig_RefusesSymlinkedConfig(t *testing.T) {
	fx := newTemplateRefFixture(t)
	tplDir := filepath.Join(t.TempDir(), "tpl")
	require.NoError(t, os.MkdirAll(tplDir, 0o755))
	require.NoError(t, os.Symlink(filepath.Join(fx.outsideDir, "scion-agent.yaml"), filepath.Join(tplDir, "scion-agent.yaml")))

	_, err := (&Template{Name: "tpl", Path: tplDir}).LoadConfig()
	assert.ErrorIs(t, err, ErrTemplateContentRefused)
}

func TestLoadHarnessConfigDir_RefusesSymlinkedConfig(t *testing.T) {
	fx := newTemplateRefFixture(t)
	target := filepath.Join(fx.outsideDir, "config.yaml")
	require.NoError(t, os.WriteFile(target, []byte("harness: generic\n"), 0o644))
	hcDir := filepath.Join(t.TempDir(), "hc")
	require.NoError(t, os.MkdirAll(hcDir, 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(hcDir, "config.yaml")))

	_, err := LoadHarnessConfigDir(hcDir)
	assert.ErrorIs(t, err, ErrTemplateContentRefused)
}

// FindTemplateWithContext (shared) fetches anonymously; only the CLI's
// FindTemplateWithCallerCredentials uses caller credentials.
func TestFindTemplateWithCallerCredentials_Mode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := recordRcloneFetch(t)
	_, err := FindTemplateWithContext(context.Background(), ":gcs:my-bucket/t")
	require.NoError(t, err)
	_, err = FindTemplateWithCallerCredentials(context.Background(), ":gcs:my-bucket/t")
	require.NoError(t, err)
	assert.Equal(t, []RemoteFetchCredentials{RemoteFetchAnonymous, RemoteFetchCallerCredentials}, *calls)
}

// Inline content that cannot be a file name on this system (a path segment
// over the name length limit) or that goes through a regular file stays
// inline content: the lookup reports it absent, not refused.
func TestResolveContentInChain_LongOrNonDirectoryInlineTextStaysInline(t *testing.T) {
	tplDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tplDir, "agents.md"), []byte("file"), 0o644))
	chain := []*Template{{Name: "t", Path: tplDir}}

	longSegment := strings.Repeat("a", 300)
	for _, field := range []string{
		longSegment,
		"You are a reviewer/" + longSegment,
		"agents.md/not-a-directory",
		strings.Repeat("word/", 900) + "end",
	} {
		got, err := ResolveContentInChain(chain, field)
		require.NoError(t, err, "field of %d bytes", len(field))
		assert.Equal(t, field, string(got))
	}

	present, err := TemplateFileExists(tplDir, longSegment)
	require.NoError(t, err)
	assert.False(t, present)

	// A refused entry is still refused.
	require.NoError(t, os.Symlink(filepath.Join(tplDir, "agents.md"), filepath.Join(tplDir, "linked.md")))
	_, err = ResolveContentInChain(chain, "linked.md")
	assert.ErrorIs(t, err, ErrTemplateContentRefused)
}

// Inline text whose first path segment cannot be a file name stops the
// lookup at the first step: a segment over the name length limit
// (ENAMETOOLONG) or a NUL byte (EINVAL). It is inline content, not refused.
func TestResolveContentInChain_UnnameableFirstSegmentStaysInline(t *testing.T) {
	tplDir := t.TempDir()
	chain := []*Template{{Name: "t", Path: tplDir}}
	for _, field := range []string{
		strings.Repeat("a", 300) + "/rest",
		"inline\x00text",
	} {
		got, err := ResolveContentInChain(chain, field)
		require.NoError(t, err, "field %q", field)
		assert.Equal(t, field, string(got))
	}
}
