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

package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProjectGitHubProjects struct {
	hubclient.ProjectService
	info          *hubclient.ProjectGitHub
	getErr        error
	bindCalls     []int64
	bindResp      *hubclient.BindGitHubInstallationResponse
	bindErr       error
	unbindHit     bool
	unbindErr     error
	setPermsCalls []hubclient.GitHubTokenPermissions
	setPermsErr   error
	resetPermsHit bool
	resetPermsErr error
}

func (f *fakeProjectGitHubProjects) GetGitHub(_ context.Context, _ string) (*hubclient.ProjectGitHub, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.info, nil
}

func (f *fakeProjectGitHubProjects) BindGitHubInstallation(_ context.Context, projectID string, installationID int64) (*hubclient.BindGitHubInstallationResponse, error) {
	f.bindCalls = append(f.bindCalls, installationID)
	if f.bindErr != nil {
		return nil, f.bindErr
	}
	if f.bindResp != nil {
		return f.bindResp, nil
	}
	return &hubclient.BindGitHubInstallationResponse{
		ProjectID:      projectID,
		InstallationID: installationID,
		Status:         "associated",
	}, nil
}

func (f *fakeProjectGitHubProjects) UnbindGitHubInstallation(_ context.Context, _ string) error {
	f.unbindHit = true
	return f.unbindErr
}

func (f *fakeProjectGitHubProjects) SetGitHubPermissions(_ context.Context, _ string, perms hubclient.GitHubTokenPermissions) (*hubclient.GitHubTokenPermissions, error) {
	f.setPermsCalls = append(f.setPermsCalls, perms)
	if f.setPermsErr != nil {
		return nil, f.setPermsErr
	}
	return &perms, nil
}

func (f *fakeProjectGitHubProjects) ResetGitHubPermissions(_ context.Context, _ string) error {
	f.resetPermsHit = true
	return f.resetPermsErr
}

type fakeProjectGitHubApp struct {
	hubclient.GitHubAppService
	installations []hubclient.GitHubInstallation
	listErr       error
}

func (f *fakeProjectGitHubApp) ListInstallations(_ context.Context, _ *hubclient.ListGitHubInstallationsOptions) ([]hubclient.GitHubInstallation, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.installations, nil
}

func TestProjectGitHubStatus_Output(t *testing.T) {
	instID := int64(4412)
	basis := "creator_pull"
	projects := &fakeProjectGitHubProjects{
		info: &hubclient.ProjectGitHub{
			ProjectID: "proj-1",
			App: hubclient.ProjectGitHubApp{
				Configured: true,
				Slug:       "scion-bot",
				WebHost:    "github.com",
			},
			Remote: hubclient.ProjectGitHubRemote{
				Owner:     "acme",
				Repo:      "api",
				OnAppHost: true,
			},
			Association: hubclient.ProjectGitHubAssociation{
				InstallationID: &instID,
				AccountLogin:   "acme",
				Basis:          &basis,
				WriteAllowed:   false,
				Authorized:     true,
				Verified:       true,
			},
			Status: &hubclient.GitHubAppProjectStatus{
				State: "ok",
			},
			Permissions: hubclient.ProjectGitHubPermissions{
				Mode:          "custom",
				AppGrantKnown: true,
				Requested: map[string]string{
					"contents": "write",
					"issues":   "write",
				},
				AppGranted: map[string]string{
					"contents": "write",
					"issues":   "read",
				},
				Effective: map[string]string{
					"contents": "read",
					"issues":   "read",
				},
				Capped: []string{"contents", "issues"},
				CapReasons: map[string]string{
					"contents": "association_read_only",
					"issues":   "app_grant",
				},
			},
		},
	}

	prevFmt := outputFormat
	outputFormat = ""
	t.Cleanup(func() { outputFormat = prevFmt })

	var buf bytes.Buffer
	err := runProjectGitHubStatus(context.Background(), projects, "proj-1", &buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "acme/api (github.com)")
	assert.Contains(t, out, "@scion-bot (configured)")
	assert.Contains(t, out, "acme (#4412)")
	assert.Contains(t, out, "creator_pull (read-only)")
	assert.Contains(t, out, "Token Permissions (mode: custom)")
	assert.Contains(t, out, "association is read-only")
	assert.Contains(t, out, "app not granted write")

	// Also verify bound_by_name, unverified state, and unloaded app permissions.
	adminBasis := "admin_binding"
	projects.info.Association = hubclient.ProjectGitHubAssociation{
		InstallationID: &instID,
		AccountLogin:   "acme",
		Basis:          &adminBasis,
		WriteAllowed:   true,
		BoundBy:        "user-uuid",
		BoundByName:    "Dana Admin",
		BoundAt:        "2026-11-20T12:00:00Z",
		Authorized:     true,
		Verified:       true,
	}
	projects.info.Permissions.AppGrantKnown = false
	projects.info.Permissions.AppGranted = nil
	buf.Reset()
	require.NoError(t, runProjectGitHubStatus(context.Background(), projects, "proj-1", &buf))
	assert.Contains(t, buf.String(), "admin_binding (Dana Admin, 2026-11-20T12:00:00Z)")
	assert.Contains(t, buf.String(), "App permissions not yet loaded.")

	projects.info.Association = hubclient.ProjectGitHubAssociation{
		InstallationID: &instID,
		AccountLogin:   "acme",
		Authorized:     false,
		Verified:       false,
		ErrorCode:      "association_unverified",
		Reason:         "check unavailable",
	}
	buf.Reset()
	require.NoError(t, runProjectGitHubStatus(context.Background(), projects, "proj-1", &buf))
	assert.Contains(t, buf.String(), "unverified (check unavailable)")
}

func TestProjectGitHubStatus_StripsControlChars(t *testing.T) {
	prevFmt := outputFormat
	outputFormat = ""
	t.Cleanup(func() { outputFormat = prevFmt })

	instID := int64(4412)
	adminBasis := "admin_binding"
	projects := &fakeProjectGitHubProjects{
		info: &hubclient.ProjectGitHub{
			ProjectID: "proj-1",
			App:       hubclient.ProjectGitHubApp{Configured: true, Slug: "scion\x1b[31m-bot\x1b[0m", WebHost: "github.com"},
			Remote:    hubclient.ProjectGitHubRemote{Owner: "acme\x1b]0;own\x07", Repo: "api\x1b[2J", OnAppHost: true},
			Association: hubclient.ProjectGitHubAssociation{
				InstallationID: &instID,
				AccountLogin:   "acme\x1b[31m-evil\x1b[0m",
				Basis:          &adminBasis,
				WriteAllowed:   true,
				BoundBy:        "user-uuid",
				BoundByName:    "Dana\x1b]0;pwn\x07 Admin",
				BoundAt:        "2026-11-20T12:00:00Z",
				Authorized:     true,
				Verified:       true,
			},
			Status: &hubclient.GitHubAppProjectStatus{
				State:        "error",
				ErrorMessage: "bad\x1b[2Jtoken\x07",
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, runProjectGitHubStatus(context.Background(), projects, "proj-1", &buf))
	out := buf.String()
	assert.NotContains(t, out, "\x1b", "ANSI/OSC escape sequences must be stripped")
	assert.NotContains(t, out, "\x07", "BEL terminator must be stripped")
	assert.Contains(t, out, "acme[31m-evil[0m (#4412)")
	assert.Contains(t, out, "Dana]0;pwn Admin")
	assert.Contains(t, out, "bad[2Jtoken")

	// Also verify reason field in the unverified/unauthorized branch.
	projects.info.Association = hubclient.ProjectGitHubAssociation{
		InstallationID: &instID,
		AccountLogin:   "acme\x1b]0;title\x07",
		Authorized:     false,
		Verified:       false,
		ErrorCode:      "association_unverified",
		Reason:         "check\x1b[31m failed\x1b[0m",
	}
	buf.Reset()
	require.NoError(t, runProjectGitHubStatus(context.Background(), projects, "proj-1", &buf))
	out = buf.String()
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "\x07")
	assert.Contains(t, out, "unverified (check[31m failed[0m)")
}

func TestProjectGitHubBind_AutoSelectAndErrors(t *testing.T) {
	prevFmt := outputFormat
	outputFormat = ""
	t.Cleanup(func() { outputFormat = prevFmt })

	baseInfo := &hubclient.ProjectGitHub{
		ProjectID: "proj-1",
		App:       hubclient.ProjectGitHubApp{Configured: true, WebHost: "github.com"},
		Remote:    hubclient.ProjectGitHubRemote{Owner: "Acme", Repo: "api", OnAppHost: true},
	}

	t.Run("auto-selects single matching active installation (case-insensitive)", func(t *testing.T) {
		projects := &fakeProjectGitHubProjects{info: baseInfo}
		ghApp := &fakeProjectGitHubApp{
			installations: []hubclient.GitHubInstallation{
				{InstallationID: 1001, AccountLogin: "other-org", AccountType: "Organization", Status: "active"},
				{InstallationID: 2002, AccountLogin: "acme", AccountType: "Organization", Status: "suspended"},
				{InstallationID: 4412, AccountLogin: "acme", AccountType: "Organization", Status: "active"},
			},
		}
		var buf bytes.Buffer
		err := runProjectGitHubBind(context.Background(), projects, ghApp, "proj-1", 0, &buf)
		require.NoError(t, err)
		assert.Equal(t, []int64{4412}, projects.bindCalls)
		assert.Contains(t, buf.String(), "acme (#4412)")
	})

	t.Run("errors when zero matching active installations", func(t *testing.T) {
		projects := &fakeProjectGitHubProjects{info: baseInfo}
		ghApp := &fakeProjectGitHubApp{
			installations: []hubclient.GitHubInstallation{
				{InstallationID: 1001, AccountLogin: "other-org", AccountType: "Organization", Status: "active"},
				{InstallationID: 2002, AccountLogin: "acme", AccountType: "Organization", Status: "suspended"},
			},
		}
		var buf bytes.Buffer
		err := runProjectGitHubBind(context.Background(), projects, ghApp, "proj-1", 0, &buf)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `no active GitHub App installation found matching owner "Acme"`)
		assert.Empty(t, projects.bindCalls)
	})

	t.Run("errors listing candidates when multiple active installations match", func(t *testing.T) {
		projects := &fakeProjectGitHubProjects{info: baseInfo}
		ghApp := &fakeProjectGitHubApp{
			installations: []hubclient.GitHubInstallation{
				{InstallationID: 4412, AccountLogin: "acme", AccountType: "Organization", Status: "active"},
				{InstallationID: 5512, AccountLogin: "ACME", AccountType: "Organization", Status: "active"},
			},
		}
		var buf bytes.Buffer
		err := runProjectGitHubBind(context.Background(), projects, ghApp, "proj-1", 0, &buf)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "multiple active GitHub App installations match")
		assert.Contains(t, err.Error(), "4412")
		assert.Contains(t, err.Error(), "5512")
		assert.Contains(t, err.Error(), "--installation")
		assert.Empty(t, projects.bindCalls)
	})

	t.Run("explicit --installation binds directly without listing", func(t *testing.T) {
		projects := &fakeProjectGitHubProjects{info: baseInfo}
		ghApp := &fakeProjectGitHubApp{}
		var buf bytes.Buffer
		err := runProjectGitHubBind(context.Background(), projects, ghApp, "proj-1", 9999, &buf)
		require.NoError(t, err)
		assert.Equal(t, []int64{9999}, projects.bindCalls)
		assert.Contains(t, buf.String(), "#9999")
	})

	t.Run("strips control characters from candidate account login and type", func(t *testing.T) {
		evilInfo := &hubclient.ProjectGitHub{
			ProjectID: "proj-1",
			App:       hubclient.ProjectGitHubApp{Configured: true, WebHost: "github.com"},
			Remote:    hubclient.ProjectGitHubRemote{Owner: "acme\x1b[31m", Repo: "api", OnAppHost: true},
		}
		projects := &fakeProjectGitHubProjects{info: evilInfo}
		ghApp := &fakeProjectGitHubApp{
			installations: []hubclient.GitHubInstallation{
				{InstallationID: 4412, AccountLogin: "acme\x1b[31m", AccountType: "Org\x1b]0;pwn\x07", Status: "active"},
				{InstallationID: 5512, AccountLogin: "ACME\x1b[31m", AccountType: "User\x07", Status: "active"},
			},
		}
		var buf bytes.Buffer
		err := runProjectGitHubBind(context.Background(), projects, ghApp, "proj-1", 0, &buf)
		require.Error(t, err)
		// Check only the candidate list lines (the %q owner uses Go quoting).
		lines := strings.Split(err.Error(), "\n")
		require.GreaterOrEqual(t, len(lines), 3)
		for _, line := range lines[1:] {
			assert.NotContains(t, line, "\x1b")
			assert.NotContains(t, line, "\x07")
		}
	})
}

func TestProjectGitHubUnbind(t *testing.T) {
	prevFmt := outputFormat
	outputFormat = ""
	t.Cleanup(func() { outputFormat = prevFmt })

	projects := &fakeProjectGitHubProjects{}
	var buf bytes.Buffer
	err := runProjectGitHubUnbind(context.Background(), projects, "proj-1", &buf)
	require.NoError(t, err)
	assert.True(t, projects.unbindHit)
	assert.Contains(t, buf.String(), "Removed GitHub App installation from project proj-1.")
}

func TestProjectGitHubPermissions_ShowAndWarnings(t *testing.T) {
	prevFmt := outputFormat
	outputFormat = ""
	t.Cleanup(func() { outputFormat = prevFmt })

	projects := &fakeProjectGitHubProjects{
		info: &hubclient.ProjectGitHub{
			ProjectID: "proj-1",
			Permissions: hubclient.ProjectGitHubPermissions{
				Mode:          "custom",
				AppGrantKnown: true,
				Requested: map[string]string{
					"contents": "write",
					"issues":   "write",
					"checks":   "read",
					"metadata": "read",
				},
				AppGranted: map[string]string{
					"contents": "write",
					"issues":   "read",
					"metadata": "read",
				},
				Effective: map[string]string{
					"contents": "read",
					"issues":   "read",
					"checks":   "read",
					"metadata": "read",
				},
				Capped: []string{"contents", "issues"},
				CapReasons: map[string]string{
					"contents": "association_read_only",
				},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, runProjectGitHubPermissionsShow(context.Background(), projects, "proj-1", &buf))
	out := buf.String()
	assert.Contains(t, out, "Token Permissions (mode: custom)")
	assert.Contains(t, out, "association is read-only")
	// Issues (write > read) and Checks (read > none) exceed App grant in custom mode.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Issues") || strings.HasPrefix(line, "Checks") {
			assert.Contains(t, line, "the App does not grant this; tokens will fail until the App is updated", "line: %s", line)
		}
		if strings.HasPrefix(line, "Contents") || strings.HasPrefix(line, "Metadata") {
			assert.NotContains(t, line, "the App does not grant this", "line: %s", line)
		}
	}

	// When AppGrantKnown is false and AppGranted is nil, do not emit false beyond-grant warnings.
	projects.info.Permissions.AppGrantKnown = false
	projects.info.Permissions.AppGranted = nil
	buf.Reset()
	require.NoError(t, runProjectGitHubPermissionsShow(context.Background(), projects, "proj-1", &buf))
	assert.Contains(t, buf.String(), "App permissions not yet loaded.")
	assert.NotContains(t, buf.String(), "the App does not grant this")
}

func TestParseGitHubTokenPermissionsArgs(t *testing.T) {
	t.Run("parses valid permissions including none", func(t *testing.T) {
		got, err := parseGitHubTokenPermissionsArgs([]string{
			"contents=write",
			"pull_requests=read",
			"issues=none",
			"metadata=read",
			"checks=write",
			"actions=none",
		})
		require.NoError(t, err)
		assert.Equal(t, hubclient.GitHubTokenPermissions{
			Contents:     "write",
			PullRequests: "read",
			Issues:       "",
			Metadata:     "read",
			Checks:       "write",
			Actions:      "",
		}, got)
	})

	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"empty args", nil, "at least one permission"},
		{"missing equals", []string{"contents"}, "expected key=level"},
		{"unknown key", []string{"deployments=write"}, "unknown permission"},
		{"invalid level", []string{"contents=admin"}, "invalid level"},
		{"duplicate key", []string{"contents=read", "contents=write"}, "duplicate permission"},
		{"all none", []string{"contents=none", "issues=none"}, "at least one permission must be set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGitHubTokenPermissionsArgs(tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestProjectGitHubPermissions_SetAndReset(t *testing.T) {
	prevFmt := outputFormat
	outputFormat = ""
	t.Cleanup(func() { outputFormat = prevFmt })

	projects := &fakeProjectGitHubProjects{
		info: &hubclient.ProjectGitHub{
			ProjectID: "proj-1",
			Permissions: hubclient.ProjectGitHubPermissions{
				Mode:          "custom",
				AppGrantKnown: true,
				Requested:     map[string]string{"contents": "read", "metadata": "read"},
				AppGranted:    map[string]string{"contents": "write", "metadata": "read"},
				Effective:     map[string]string{"contents": "read", "metadata": "read"},
			},
		},
	}

	var buf bytes.Buffer
	perms := hubclient.GitHubTokenPermissions{Contents: "read", Metadata: "read"}
	require.NoError(t, runProjectGitHubPermissionsSet(context.Background(), projects, "proj-1", perms, &buf))
	assert.Equal(t, []hubclient.GitHubTokenPermissions{perms}, projects.setPermsCalls)
	assert.Contains(t, buf.String(), "Updated custom GitHub token permissions for project proj-1.")
	assert.Contains(t, buf.String(), "Token Permissions (mode: custom)")

	projects.info.Permissions.Mode = "default"
	buf.Reset()
	require.NoError(t, runProjectGitHubPermissionsReset(context.Background(), projects, "proj-1", &buf))
	assert.True(t, projects.resetPermsHit)
	assert.Contains(t, buf.String(), "Reset GitHub token permissions to defaults for project proj-1.")
	assert.Contains(t, buf.String(), "Token Permissions (mode: default)")
}

func TestProjectGitHub_AgentModeAndWindowsGates(t *testing.T) {
	// Agent mode: status and permissions (read) allowed; bind, unbind, set, reset NOT allowed.
	assert.True(t, agentAllowed["project.github"], "project.github must be allowed in agent mode")
	assert.True(t, agentAllowed["project.github.status"], "project.github.status must be allowed in agent mode")
	assert.True(t, agentAllowed["project.github.permissions"], "project.github.permissions must be allowed in agent mode")
	assert.False(t, agentAllowed["project.github.bind"], "project.github.bind must NOT be allowed in agent mode")
	assert.False(t, agentAllowed["project.github.unbind"], "project.github.unbind must NOT be allowed in agent mode")
	assert.False(t, agentAllowed["project.github.permissions.set"], "project.github.permissions.set must NOT be allowed in agent mode")
	assert.False(t, agentAllowed["project.github.permissions.reset"], "project.github.permissions.reset must NOT be allowed in agent mode")

	// Windows gate: all subcommands + parents allowed.
	root, ran := gateTestTree(t)
	applyPlatformRestrictions(root, "windows")
	for _, args := range [][]string{
		{"project", "github", "status"},
		{"project", "github", "bind"},
		{"project", "github", "unbind"},
		{"project", "github", "permissions"},
		{"project", "github", "permissions", "set"},
		{"project", "github", "permissions", "reset"},
	} {
		*ran = nil
		root.SetArgs(args)
		err := root.Execute()
		require.NoError(t, err, "expected %v to be allowed on Windows", args)
		assert.NotEmpty(t, *ran, "expected %v to execute on Windows", args)
	}
}
