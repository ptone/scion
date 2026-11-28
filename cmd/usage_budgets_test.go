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
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/clitime"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageBudgetsCLI_ScopeDefaultingAndFlags(t *testing.T) {
	t.Setenv("SCION_CLI_MODE", "human")
	asOf := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	baseResp := &hubclient.UsageBudgetsResponse{
		Scope: hubclient.UsageScopeRef{Kind: "project", ID: "proj-1"},
		AsOf:  asOf,
	}

	t.Run("human mode inside linked project defaults to project", func(t *testing.T) {
		svc := &stubUsageService{budgetsResp: baseResp}
		withUsageTestTarget(t, svc, "proj-1")

		cmd := newUsageCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"budgets"})
		require.NoError(t, cmd.Execute())

		assert.Equal(t, "project_budgets", svc.lastMethod)
		assert.Equal(t, "proj-1", svc.lastID)
		assert.Contains(t, out.String(), "Scope:   project (proj-1)")
	})

	t.Run("human mode outside project defaults to /me", func(t *testing.T) {
		svc := &stubUsageService{
			budgetsResp: &hubclient.UsageBudgetsResponse{
				Scope: hubclient.UsageScopeRef{Kind: "user", ID: "u-self"},
				AsOf:  asOf,
			},
		}
		withUsageTestTarget(t, svc, "")

		cmd := newUsageCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"budgets"})
		require.NoError(t, cmd.Execute())

		assert.Equal(t, "me_budgets", svc.lastMethod)
		assert.Contains(t, out.String(), "Scope:   user (u-self)")
	})

	t.Run("--user me and --user <id>", func(t *testing.T) {
		svc := &stubUsageService{budgetsResp: baseResp}
		withUsageTestTarget(t, svc, "proj-1")

		cmdMe := newUsageCmd()
		cmdMe.SetOut(&bytes.Buffer{})
		cmdMe.SetArgs([]string{"budgets", "--user", "me"})
		require.NoError(t, cmdMe.Execute())
		assert.Equal(t, "me_budgets", svc.lastMethod)

		cmdUser := newUsageCmd()
		cmdUser.SetOut(&bytes.Buffer{})
		cmdUser.SetArgs([]string{"budgets", "--user", "user-42"})
		require.NoError(t, cmdUser.Execute())
		assert.Equal(t, "user_budgets", svc.lastMethod)
		assert.Equal(t, "user-42", svc.lastID)
	})

	t.Run("--group routes to GetGroupBudgets", func(t *testing.T) {
		svc := &stubUsageService{
			groupBudgetsResp: &hubclient.GroupUsageBudgetsResponse{
				Scope:       hubclient.UsageScopeRef{Kind: "group", ID: "grp-1"},
				GroupName:   "platform",
				NonAdditive: true,
				AsOf:        asOf,
			},
		}
		withUsageTestTarget(t, svc, "proj-1")

		cmd := newUsageCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"budgets", "--group", "platform"})
		require.NoError(t, cmd.Execute())
		assert.Equal(t, "group_budgets", svc.lastMethod)
		assert.Equal(t, "platform", svc.lastID)
	})

	t.Run("mutually exclusive explicit scope flags fail, while context project allows --user and --group", func(t *testing.T) {
		svc := &stubUsageService{
			budgetsResp: baseResp,
			groupBudgetsResp: &hubclient.GroupUsageBudgetsResponse{
				Scope: hubclient.UsageScopeRef{Kind: "group", ID: "grp-1"},
				AsOf:  asOf,
			},
		}
		withUsageTestTarget(t, svc, "")

		cmd := newUsageCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetArgs([]string{"budgets", "--user", "me", "--group", "platform"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at most one of")

		// User-typed --project conflicts with --user.
		projectPath = "proj-x"
		projectSource = ""
		cmd2 := newUsageCmd()
		cmd2.SetOut(&bytes.Buffer{})
		cmd2.SetArgs([]string{"budgets", "--user", "me"})
		err = cmd2.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at most one of")

		// Context-injected project (scion use) does NOT conflict with --user or --group.
		projectSource = targetSourceContext
		cmd3 := newUsageCmd()
		cmd3.SetOut(&bytes.Buffer{})
		cmd3.SetArgs([]string{"budgets", "--group", "platform"})
		require.NoError(t, cmd3.Execute())
		assert.Equal(t, "group_budgets", svc.lastMethod)
	})
}

func TestUsageBudgetsCLI_AgentMode(t *testing.T) {
	t.Setenv("SCION_CLI_MODE", "agent")
	asOf := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

	t.Run("defaults to agent's own project and refuses --user/--group", func(t *testing.T) {
		svc := &stubUsageService{
			budgetsResp: &hubclient.UsageBudgetsResponse{
				Scope: hubclient.UsageScopeRef{Kind: "project", ID: "agent-proj"},
				AsOf:  asOf,
			},
		}
		withUsageTestTarget(t, svc, "agent-proj")

		cmd := newUsageCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"budgets"})
		require.NoError(t, cmd.Execute())
		assert.Equal(t, "project_budgets", svc.lastMethod)
		assert.Equal(t, "agent-proj", svc.lastID)

		cmdUser := newUsageCmd()
		cmdUser.SetOut(&bytes.Buffer{})
		cmdUser.SetArgs([]string{"budgets", "--user", "me"})
		err := cmdUser.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--user is available only in human mode")

		cmdGroup := newUsageCmd()
		cmdGroup.SetOut(&bytes.Buffer{})
		cmdGroup.SetArgs([]string{"budgets", "--group", "platform"})
		err = cmdGroup.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--group is available only in human mode")
	})

	t.Run("usage.budgets is agentAllowed while usage.budgets.override is removed", func(t *testing.T) {
		assert.True(t, agentAllowed["usage.budgets"], "agentAllowed must include usage.budgets")
		assert.False(t, agentAllowed["usage.budgets.override"], "usage.budgets.override must be human-only")

		root, ran := gateTestTree(t)
		applyAgentMode(root)

		root.SetArgs([]string{"usage", "budgets"})
		require.NoError(t, root.Execute())
		assert.Equal(t, []string{"usage.budgets"}, *ran)

		root2 := cloneCommandForModeExec(rootCmd)
		root2.SilenceErrors = true
		root2.SilenceUsage = true
		root2.SetOut(&bytes.Buffer{})
		root2.SetErr(&bytes.Buffer{})
		applyAgentMode(root2)
		root2.SetArgs([]string{"usage", "budgets", "override", "b-1"})
		err := root2.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `command "scion usage budgets override" is not available in agent mode`)
	})
}

func TestUsageBudgetsCLI_WindowsAllowed(t *testing.T) {
	assert.True(t, windowsAllowed["usage.budgets"], "windowsAllowed must include usage.budgets")
	assert.True(t, windowsAllowed["usage.budgets.override"], "windowsAllowed must include usage.budgets.override")

	root, ran := gateTestTree(t)
	applyPlatformRestrictions(root, "windows")
	root.SetArgs([]string{"usage", "budgets"})
	require.NoError(t, root.Execute())
	assert.Equal(t, []string{"usage.budgets"}, *ran)

	root2, ran2 := gateTestTree(t)
	applyPlatformRestrictions(root2, "windows")
	root2.SetArgs([]string{"usage", "budgets", "override", "b-1"})
	require.NoError(t, root2.Execute())
	assert.Equal(t, []string{"usage.budgets.override"}, *ran2)
}

func TestUsageBudgetsCLI_TableRenderingAndTimeZones(t *testing.T) {
	t.Setenv("SCION_CLI_MODE", "human")
	asOf := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	dayStart := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	dayEnd := time.Date(2026, 11, 29, 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	monthEnd := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	overrideUntil := time.Date(2026, 11, 28, 22, 30, 0, 0, time.UTC)

	svc := &stubUsageService{
		budgetsResp: &hubclient.UsageBudgetsResponse{
			Scope: hubclient.UsageScopeRef{Kind: "project", ID: "proj-1"},
			AsOf:  asOf,
			Budgets: []hubclient.UsageBudgetEntry{
				{
					ID:             "b-day",
					LimitName:      "llm_tokens_per_project_day",
					ScopeKind:      "project",
					ScopeID:        "proj-1",
					WindowStartUTC: dayStart,
					WindowEndUTC:   dayEnd,
					Cap:            int64Ptr(500000),
					TokenAction:    "pause_new",
					Consumed:       400000,
					State:          "warn",
					OverrideUntil:  &overrideUntil,
					StateVersion:   2,
				},
				{
					ID:             "",
					LimitName:      "llm_tokens_per_project_month",
					ScopeKind:      "project",
					ScopeID:        "proj-1",
					WindowStartUTC: monthStart,
					WindowEndUTC:   monthEnd,
					Cap:            nil, // unlimited
					TokenAction:    "notify_only",
					Consumed:       1250000,
					State:          "ok",
				},
			},
		},
	}
	withUsageTestTarget(t, svc, "proj-1")

	cmd := newUsageCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"budgets"})
	require.NoError(t, cmd.Execute())

	got := out.String()
	assert.Contains(t, got, "Scope:   project (proj-1)")
	assert.Contains(t, got, "As of:   2026-11-28 12:00 UTC")
	assert.Contains(t, got, "ID")
	assert.Contains(t, got, "CONSUMED / CAP")
	assert.Contains(t, got, "OVERRIDE UNTIL")
	assert.Contains(t, got, "b-day")
	assert.Contains(t, got, "400,000 / 500,000")
	assert.Contains(t, got, "80.0%")
	assert.Contains(t, got, "day (ends 2026-11-29 00:00 UTC)")
	assert.Contains(t, got, "2026-11-28 22:30 UTC")
	assert.Contains(t, got, "1,250,000 / unlimited")
	assert.Contains(t, got, "month (ends 2026-12-01 00:00 UTC)")

	// Verify non-UTC display zone conversion through pkg/clitime.
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	clitime.SetZone(ny)
	var outNY bytes.Buffer
	writeUsageBudgetsTable(&outNY, svc.budgetsResp)
	assert.Contains(t, outNY.String(), "As of:   2026-11-28 07:00 EST")
	assert.Contains(t, outNY.String(), "day (ends 2026-11-28 19:00 EST)")
	assert.Contains(t, outNY.String(), "2026-11-28 17:30 EST")
}

func TestUsageBudgetsCLI_GroupBudgetsNonAdditiveAndMembers(t *testing.T) {
	t.Setenv("SCION_CLI_MODE", "human")
	asOf := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	monthStart := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	monthEnd := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	svc := &stubUsageService{
		groupBudgetsResp: &hubclient.GroupUsageBudgetsResponse{
			Scope:       hubclient.UsageScopeRef{Kind: "group", ID: "grp-1"},
			GroupName:   "platform",
			NonAdditive: true,
			AsOf:        asOf,
			Budgets: []hubclient.GroupBudgetItem{
				{
					ID:             "gb-1",
					LimitName:      "llm_tokens_per_group_month",
					ScopeKind:      "group",
					ScopeID:        "grp-1",
					ProjectScopeID: "proj-1",
					WindowStartUTC: monthStart,
					WindowEndUTC:   monthEnd,
					Cap:            int64Ptr(1000000),
					TokenAction:    "suspend_and_pause",
					Consumed:       600000,
					State:          "warn",
					NonAdditive:    true,
					Members: []hubclient.GroupBudgetMemberView{
						{UserID: "u-1", Email: "alice@example.com", DisplayName: "Alice", TokensSummable: 400000, Calls: 12, Events: 12},
						{UserID: "u-2", TokensSummable: 200000, Calls: 5, Events: 5},
					},
				},
			},
		},
	}
	withUsageTestTarget(t, svc, "")

	cmd := newUsageCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"budgets", "--group", "platform"})
	require.NoError(t, cmd.Execute())

	got := out.String()
	assert.Contains(t, got, "Group:   platform")
	assert.Contains(t, got, "group:grp-1 (proj-1)")
	assert.Contains(t, got, "600,000 / 1,000,000")
	assert.Contains(t, got, "60.0%")
	assert.Contains(t, got, "Alice <alice@example.com>")
	assert.Contains(t, got, "400,000")
	assert.Contains(t, got, "u-2")
	assert.Contains(t, got, "non-additive across overlapping groups")
}

func TestUsageBudgetsCLI_JSONPassthrough(t *testing.T) {
	t.Setenv("SCION_CLI_MODE", "human")
	raw := json.RawMessage(`{"scope":{"kind":"user","id":"u1"},"as_of":"2026-11-28T12:00:00Z","budgets":[],"future_flag":true}`)
	svc := &stubUsageService{
		budgetsResp: &hubclient.UsageBudgetsResponse{Raw: raw},
	}
	withUsageTestTarget(t, svc, "")

	cmd := newUsageCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"budgets", "--json"})
	require.NoError(t, cmd.Execute())
	assert.JSONEq(t, string(raw), out.String())
}

func TestUsageBudgetsCLI_OverrideFlows(t *testing.T) {
	t.Setenv("SCION_CLI_MODE", "human")
	fixedNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	t.Cleanup(clitime.SetNow(func() time.Time { return fixedNow }))

	makeList := func(ver int64) *hubclient.UsageBudgetsResponse {
		return &hubclient.UsageBudgetsResponse{
			Scope: hubclient.UsageScopeRef{Kind: "project", ID: "proj-1"},
			AsOf:  fixedNow,
			Budgets: []hubclient.UsageBudgetEntry{
				{
					ID:           "b-1",
					LimitName:    "llm_tokens_per_project_day",
					ScopeKind:    "project",
					ScopeID:      "proj-1",
					Cap:          int64Ptr(500000),
					Consumed:     520000,
					State:        "exhausted",
					StateVersion: ver,
				},
			},
		}
	}

	t.Run("duration 1d and 2h, plus RFC3339", func(t *testing.T) {
		expectedUntil := fixedNow.Add(24 * time.Hour)
		svc := &stubUsageService{
			budgetsResp: makeList(3),
			overrideResp: &hubclient.UsageBudgetEntry{
				ID:            "b-1",
				LimitName:     "llm_tokens_per_project_day",
				Cap:           int64Ptr(500000),
				Consumed:      520000,
				State:         "ok",
				OverrideUntil: &expectedUntil,
				StateVersion:  4,
			},
		}
		withUsageTestTarget(t, svc, "proj-1")

		cmd := newUsageCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"budgets", "override", "b-1", "--until", "1d", "--reason", "release"})
		require.NoError(t, cmd.Execute())

		require.Len(t, svc.overrideCalls, 1)
		call := svc.overrideCalls[0]
		assert.Equal(t, "project", call.scope)
		assert.Equal(t, "proj-1", call.scopeID)
		assert.Equal(t, "b-1", call.budgetID)
		require.NotNil(t, call.req.OverrideUntil)
		assert.Equal(t, expectedUntil, *call.req.OverrideUntil)
		require.NotNil(t, call.req.StateVersion)
		assert.Equal(t, int64(3), *call.req.StateVersion)
		assert.Equal(t, "release", call.req.Reason)
		assert.Contains(t, out.String(), "Set override on budget b-1")
		assert.Contains(t, out.String(), "2026-11-29 12:00 UTC")
	})

	t.Run("--clear sends nil override_until on --user scope", func(t *testing.T) {
		svc := &stubUsageService{
			budgetsResp: &hubclient.UsageBudgetsResponse{
				Scope: hubclient.UsageScopeRef{Kind: "user", ID: "user-9"},
				AsOf:  fixedNow,
				Budgets: []hubclient.UsageBudgetEntry{
					{ID: "b-u1", LimitName: "llm_tokens_per_user_day", State: "ok", StateVersion: 5},
				},
			},
			overrideResp: &hubclient.UsageBudgetEntry{
				ID:           "b-u1",
				LimitName:    "llm_tokens_per_user_day",
				State:        "exhausted",
				StateVersion: 6,
			},
		}
		withUsageTestTarget(t, svc, "proj-1")

		cmd := newUsageCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"budgets", "override", "b-u1", "--clear", "--user", "user-9"})
		require.NoError(t, cmd.Execute())

		require.Len(t, svc.overrideCalls, 1)
		assert.Equal(t, "user", svc.overrideCalls[0].scope)
		assert.Equal(t, "user-9", svc.overrideCalls[0].scopeID)
		assert.Nil(t, svc.overrideCalls[0].req.OverrideUntil)
		assert.Equal(t, int64(5), *svc.overrideCalls[0].req.StateVersion)
		assert.Contains(t, out.String(), "Cleared override on budget b-u1")
	})

	t.Run("412 retry succeeds on second attempt with refreshed state_version", func(t *testing.T) {
		until := fixedNow.Add(2 * time.Hour)
		svc := &stubUsageService{
			budgetsQueue: []*hubclient.UsageBudgetsResponse{
				makeList(3),
				makeList(4),
			},
			overrideErrQueue: []error{
				&apiclient.APIError{StatusCode: http.StatusPreconditionFailed, Code: "revision_conflict", Message: "stale"},
				nil,
			},
			overrideResp: &hubclient.UsageBudgetEntry{
				ID:            "b-1",
				LimitName:     "llm_tokens_per_project_day",
				State:         "ok",
				OverrideUntil: &until,
				StateVersion:  5,
			},
		}
		withUsageTestTarget(t, svc, "proj-1")

		cmd := newUsageCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"budgets", "override", "b-1", "--until", "2h"})
		require.NoError(t, cmd.Execute())

		assert.Equal(t, 2, svc.budgetGetCalls)
		require.Len(t, svc.overrideCalls, 2)
		assert.Equal(t, int64(3), *svc.overrideCalls[0].req.StateVersion)
		assert.Equal(t, int64(4), *svc.overrideCalls[1].req.StateVersion)
	})

	t.Run("412 retry fails cleanly when second attempt also conflicts", func(t *testing.T) {
		conflict := &apiclient.APIError{StatusCode: http.StatusPreconditionFailed, Code: "revision_conflict", Message: "stale"}
		svc := &stubUsageService{
			budgetsQueue:     []*hubclient.UsageBudgetsResponse{makeList(3), makeList(4)},
			overrideErrQueue: []error{conflict, conflict},
		}
		withUsageTestTarget(t, svc, "proj-1")

		cmd := newUsageCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetArgs([]string{"budgets", "override", "b-1", "--until", "2h"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "412 Precondition Failed after retry")
		assert.Equal(t, 2, svc.budgetGetCalls)
		assert.Len(t, svc.overrideCalls, 2)
	})

	t.Run("flag validation", func(t *testing.T) {
		svc := &stubUsageService{budgetsResp: makeList(1)}
		withUsageTestTarget(t, svc, "proj-1")

		for _, tc := range []struct {
			name string
			args []string
			want string
		}{
			{"neither until nor clear", []string{"budgets", "override", "b-1"}, "one of --until or --clear"},
			{"both until and clear", []string{"budgets", "override", "b-1", "--until", "1h", "--clear"}, "mutually exclusive"},
			{"negative duration", []string{"budgets", "override", "b-1", "--until", "-1h"}, "must be positive"},
			{"bad duration", []string{"budgets", "override", "b-1", "--until", "tomorrow"}, "invalid --until"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cmd := newUsageCmd()
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetArgs(tc.args)
				err := cmd.Execute()
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
			})
		}
	})
}

func TestParseBudgetOverrideUntil(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

	got, err := parseBudgetOverrideUntil("2026-11-28T23:59:59Z", now)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 11, 28, 23, 59, 59, 0, time.UTC), got)

	got, err = parseBudgetOverrideUntil("2h30m", now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(2*time.Hour+30*time.Minute), got)

	got, err = parseBudgetOverrideUntil("1d", now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(24*time.Hour), got)

	got, err = parseBudgetOverrideUntil("2d12h", now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(60*time.Hour), got)

	_, err = parseBudgetOverrideUntil("0d", now)
	require.Error(t, err)

	_, err = parseBudgetOverrideUntil("d", now)
	require.Error(t, err)
}

func TestUsageBudgetsCLI_ErrorFormatting(t *testing.T) {
	routeNotFound := &apiclient.APIError{StatusCode: http.StatusNotFound, Code: "not_found", Message: "route not found"}
	assert.ErrorIs(t, formatUsageBudgetAPIError(routeNotFound, true, false), errUsageBudgetsExperimentDisabled)
	assert.Contains(t, formatUsageBudgetAPIError(routeNotFound, true, false).Error(), "token budgets are not enabled on this Hub")

	otherNotFound := &apiclient.APIError{StatusCode: http.StatusNotFound, Code: "not_found", Message: "Project not found"}
	assert.ErrorIs(t, formatUsageBudgetAPIError(otherNotFound, false, false), errUsageBudgetsExperimentDisabled)
	entityErr := formatUsageBudgetAPIError(otherNotFound, true, false)
	assert.ErrorIs(t, entityErr, otherNotFound)
	assert.Contains(t, entityErr.Error(), "tokenomics.token_budgets")

	forb := &apiclient.APIError{StatusCode: http.StatusForbidden, Code: "forbidden", Message: "denied"}
	assert.Contains(t, formatUsageBudgetAPIError(forb, true, true).Error(), "permission denied overriding budget")
	assert.Contains(t, formatUsageBudgetAPIError(forb, true, false).Error(), "permission denied reading token budgets")

	precondReq := &apiclient.APIError{StatusCode: http.StatusPreconditionRequired, Code: "revision_conflict", Message: "missing"}
	assert.Contains(t, formatUsageBudgetAPIError(precondReq, true, true).Error(), "428 Precondition Required")

	precondFail := &apiclient.APIError{StatusCode: http.StatusPreconditionFailed, Code: "revision_conflict", Message: "mismatch"}
	assert.Contains(t, formatUsageBudgetAPIError(precondFail, true, true).Error(), "412 Precondition Failed")
}
