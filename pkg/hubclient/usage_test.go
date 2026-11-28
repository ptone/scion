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

package hubclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageService_ScopedEndpointsAndRawPassthrough(t *testing.T) {
	var gotPath string
	var gotQuery string
	rawBody := `{"scope":{"kind":"project","id":"proj-1"},"window":{"since":"2026-11-20T00:00:00Z","until":"2026-11-27T23:59:59Z","granularity":"day"},"group_by":["model"],"calls":10,"error_calls":1,"events":10,"null_token_events":0,"tokens":{"input":100,"output":50,"cache_read":null,"cache_write":0,"cache_write_1h":null,"reasoning":null,"summable_tokens":150,"unknown_events":{"output":2}},"groups":[{"model":"claude-sonnet-4-6","calls":10,"error_calls":1,"events":10,"null_token_events":0,"tokens":{"input":100,"output":50,"cache_read":null,"cache_write":0,"cache_write_1h":null,"reasoning":null,"summable_tokens":150}}],"truncated":false,"measurement_status":"partial","unmeasured_agents":1,"stale_agents":2,"as_of":"2026-11-27T12:00:00Z","last_receipt_at":null,"future_field":"preserved"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rawBody))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)

	opts := &UsageQueryOptions{
		Since:   "7d",
		Until:   "2026-11-27",
		GroupBy: []string{"model", "day"},
		Limit:   25,
	}

	resp, err := c.Usage().GetProject(context.Background(), "proj-1", opts)
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/projects/proj-1/usage", gotPath)
	assert.Contains(t, gotQuery, "since=7d")
	assert.Contains(t, gotQuery, "until=2026-11-27")
	assert.Contains(t, gotQuery, "groupBy=model%2Cday")
	assert.Contains(t, gotQuery, "limit=25")
	assert.Equal(t, "partial", resp.MeasurementStatus)
	assert.Equal(t, 1, resp.UnmeasuredAgents)
	assert.Equal(t, 2, resp.StaleAgents)
	require.NotNil(t, resp.Tokens.Input)
	assert.Equal(t, int64(100), *resp.Tokens.Input)
	assert.Nil(t, resp.Tokens.CacheRead)
	require.NotNil(t, resp.Tokens.CacheWrite)
	assert.Equal(t, int64(0), *resp.Tokens.CacheWrite)
	require.NotNil(t, resp.Tokens.UnknownEvents)
	assert.Equal(t, int64(2), resp.Tokens.UnknownEvents.Output)
	assert.JSONEq(t, rawBody, string(resp.Raw))

	_, err = c.Usage().GetMe(context.Background(), &UsageQueryOptions{Since: "7d"})
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/me/usage", gotPath)

	_, err = c.Usage().GetUser(context.Background(), "user-42", nil)
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/users/user-42/usage", gotPath)

	_, err = c.Usage().GetGlobal(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/usage", gotPath)
}

func TestUsageService_GetAgent(t *testing.T) {
	var gotPath string
	var gotQuery string
	rawBody := `{"agent_id":"ag-1","project_id":"proj-1","window":{"since":"2026-11-20T00:00:00Z","until":"2026-11-27T23:59:59Z"},"calls":5,"error_calls":0,"events":5,"null_token_events":0,"tokens":{"input":50,"output":25,"cache_read":null,"cache_write":null,"cache_write_1h":null,"reasoning":null,"summable_tokens":75},"by_model":[{"model":"claude-sonnet-4-6","harness":"claude","calls":5,"error_calls":0,"events":5,"null_token_events":0,"tokens":{"input":50,"output":25,"cache_read":null,"cache_write":null,"cache_write_1h":null,"reasoning":null,"summable_tokens":75}}],"measurement_status":"measured","as_of":"2026-11-27T12:00:00Z","last_receipt_at":null}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rawBody))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)

	resp, err := c.Usage().GetAgent(context.Background(), "ag-1", &UsageQueryOptions{Since: "7d"})
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/agents/ag-1/usage", gotPath)
	assert.Equal(t, "since=7d", gotQuery)
	assert.Equal(t, "ag-1", resp.AgentID)
	assert.Equal(t, "measured", resp.MeasurementStatus)
	require.Len(t, resp.ByModel, 1)
	assert.Equal(t, "claude-sonnet-4-6", resp.ByModel[0].Model)
	assert.JSONEq(t, rawBody, string(resp.Raw))
}

func TestUsageService_BudgetsEndpoints(t *testing.T) {
	var gotPath string
	var gotQuery string
	rawBudgets := `{"scope":{"kind":"project","id":"proj-1"},"as_of":"2026-11-28T12:00:00Z","budgets":[{"id":"b-1","limit_definition_id":"ld-1","limit_name":"llm_tokens_per_project_day","scope_kind":"project","scope_id":"proj-1","window_start_utc":"2026-11-28T00:00:00Z","window_end_utc":"2026-11-29T00:00:00Z","cap":500000,"token_action":"pause_new","consumed":125000,"state":"ok","thresholds_notified":0,"override_until":null,"state_version":3,"as_of":"2026-11-28T12:00:00Z"}]}`
	rawGroup := `{"scope":{"kind":"group","id":"grp-1"},"group_name":"platform","non_additive":true,"as_of":"2026-11-28T12:00:00Z","budgets":[{"id":"gb-1","limit_definition_id":"ld-g","limit_name":"llm_tokens_per_group_month","scope_kind":"group","scope_id":"grp-1","window_start_utc":"2026-11-01T00:00:00Z","window_end_utc":"2026-12-01T00:00:00Z","cap":null,"token_action":"notify_only","consumed":9000,"state":"ok","thresholds_notified":0,"override_until":null,"state_version":1,"as_of":"2026-11-28T12:00:00Z","non_additive":true,"members":[{"user_id":"u-1","email":"alice@example.com","display_name":"Alice","tokens_summable":9000,"calls":4,"events":4}]}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/groups/platform/usage/budgets" {
			_, _ = w.Write([]byte(rawGroup))
			return
		}
		_, _ = w.Write([]byte(rawBudgets))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)

	projResp, err := c.Usage().GetProjectBudgets(context.Background(), "proj-1")
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/projects/proj-1/usage/budgets", gotPath)
	require.Len(t, projResp.Budgets, 1)
	assert.Equal(t, "b-1", projResp.Budgets[0].ID)
	require.NotNil(t, projResp.Budgets[0].Cap)
	assert.Equal(t, int64(500000), *projResp.Budgets[0].Cap)
	assert.Equal(t, int64(3), projResp.Budgets[0].StateVersion)
	assert.JSONEq(t, rawBudgets, string(projResp.Raw))

	_, err = c.Usage().GetMeBudgets(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/me/usage/budgets", gotPath)

	_, err = c.Usage().GetUserBudgets(context.Background(), "user-42")
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/users/user-42/usage/budgets", gotPath)

	grpResp, err := c.Usage().GetGroupBudgets(context.Background(), "platform", &GroupBudgetsQueryOptions{ProjectID: "proj-1"})
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/groups/platform/usage/budgets", gotPath)
	assert.Equal(t, "project_id=proj-1", gotQuery)
	assert.True(t, grpResp.NonAdditive)
	assert.Equal(t, "platform", grpResp.GroupName)
	require.Len(t, grpResp.Budgets, 1)
	assert.Nil(t, grpResp.Budgets[0].Cap)
	require.Len(t, grpResp.Budgets[0].Members, 1)
	assert.Equal(t, "alice@example.com", grpResp.Budgets[0].Members[0].Email)
	assert.JSONEq(t, rawGroup, string(grpResp.Raw))
}

func TestUsageService_OverrideBudgets(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	statusToReturn := http.StatusOK
	respBody := `{"id":"b-1","limit_definition_id":"ld-1","limit_name":"llm_tokens_per_project_day","scope_kind":"project","scope_id":"proj-1","window_start_utc":"2026-11-28T00:00:00Z","window_end_utc":"2026-11-29T00:00:00Z","cap":500000,"token_action":"pause_new","consumed":510000,"state":"ok","thresholds_notified":3,"override_until":"2026-11-28T23:00:00Z","state_version":4,"as_of":"2026-11-28T12:00:00Z"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusToReturn)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)

	ver := int64(3)
	until := time.Date(2026, 11, 28, 23, 0, 0, 0, time.UTC)
	entry, err := c.Usage().OverrideProjectBudget(context.Background(), "proj-1", "b-1", &OverrideUsageBudgetRequest{
		OverrideUntil: &until,
		StateVersion:  &ver,
		Reason:        "release crunch",
	})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/v1/projects/proj-1/usage/budgets/b-1:override", gotPath)
	assert.Equal(t, int64(4), entry.StateVersion)
	assert.JSONEq(t, respBody, string(entry.Raw))

	var sent map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &sent))
	assert.Equal(t, "2026-11-28T23:00:00Z", sent["override_until"])
	assert.Equal(t, float64(3), sent["state_version"])
	assert.Equal(t, "release crunch", sent["reason"])

	// Clear override must explicitly send "override_until": null.
	_, err = c.Usage().OverrideUserBudget(context.Background(), "user-42", "b-2", &OverrideUsageBudgetRequest{
		OverrideUntil: nil,
		StateVersion:  &ver,
	})
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/users/user-42/usage/budgets/b-2:override", gotPath)
	var clearSent map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &clearSent))
	val, hasKey := clearSent["override_until"]
	assert.True(t, hasKey, "override_until key must be present when clearing")
	assert.Nil(t, val, "override_until must be JSON null when clearing")

	// Propagates 412 as apiclient.APIError.
	statusToReturn = http.StatusPreconditionFailed
	respBody = `{"error":{"code":"revision_conflict","message":"budget state_version mismatch"}}`
	_, err = c.Usage().OverrideProjectBudget(context.Background(), "proj-1", "b-1", &OverrideUsageBudgetRequest{
		OverrideUntil: &until,
		StateVersion:  &ver,
	})
	require.Error(t, err)
	var apiErr *apiclient.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusPreconditionFailed, apiErr.StatusCode)
}
