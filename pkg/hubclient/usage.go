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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
)

// UsageService provides read access to Track 1 token usage endpoints.
type UsageService interface {
	// GetProject returns token usage for a project (GET /api/v1/projects/{id}/usage).
	GetProject(ctx context.Context, projectID string, opts *UsageQueryOptions) (*UsageSummaryResponse, error)

	// GetMe returns the calling user's token usage (GET /api/v1/me/usage).
	GetMe(ctx context.Context, opts *UsageQueryOptions) (*UsageSummaryResponse, error)

	// GetUser returns token usage for a specific user (GET /api/v1/users/{id}/usage).
	GetUser(ctx context.Context, userID string, opts *UsageQueryOptions) (*UsageSummaryResponse, error)

	// GetGlobal returns hub-wide token usage (GET /api/v1/usage, hub admin only).
	GetGlobal(ctx context.Context, opts *UsageQueryOptions) (*UsageSummaryResponse, error)

	// GetAgent returns token usage for a single agent (GET /api/v1/agents/{id}/usage).
	GetAgent(ctx context.Context, agentID string, opts *UsageQueryOptions) (*AgentUsageResponse, error)

	// GetProjectBudgets returns active token budgets for a project (GET /api/v1/projects/{id}/usage/budgets).
	GetProjectBudgets(ctx context.Context, projectID string) (*UsageBudgetsResponse, error)

	// GetMeBudgets returns active token budgets for the calling user (GET /api/v1/me/usage/budgets).
	GetMeBudgets(ctx context.Context) (*UsageBudgetsResponse, error)

	// GetUserBudgets returns active token budgets for a user (GET /api/v1/users/{id}/usage/budgets).
	GetUserBudgets(ctx context.Context, userID string) (*UsageBudgetsResponse, error)

	// GetGroupBudgets returns active pooled-group token budgets (GET /api/v1/groups/{id}/usage/budgets).
	GetGroupBudgets(ctx context.Context, groupIDOrSlug string, opts *GroupBudgetsQueryOptions) (*GroupUsageBudgetsResponse, error)

	// OverrideProjectBudget sets or clears override_until on a project or user_project budget
	// (POST /api/v1/projects/{id}/usage/budgets/{budgetId}:override).
	OverrideProjectBudget(ctx context.Context, projectID, budgetID string, req *OverrideUsageBudgetRequest) (*UsageBudgetEntry, error)

	// OverrideUserBudget sets or clears override_until on a user or user_project budget
	// (POST /api/v1/users/{id}/usage/budgets/{budgetId}:override).
	OverrideUserBudget(ctx context.Context, userID, budgetID string, req *OverrideUsageBudgetRequest) (*UsageBudgetEntry, error)
}

type usageService struct {
	c *client
}

// UsageQueryOptions configures query parameters for usage read endpoints.
type UsageQueryOptions struct {
	Since       string
	Until       string
	GroupBy     []string
	Limit       int
	ProjectID   string
	UserID      string
	AgentID     string
	Harness     string
	Model       string
	UsageSource string
}

// UsageBucketUnknownEvents reports per-bucket counts of events in the window
// where that bucket was NULL while other events reported known values.
type UsageBucketUnknownEvents struct {
	Input        int64 `json:"input,omitempty"`
	Output       int64 `json:"output,omitempty"`
	CacheRead    int64 `json:"cache_read,omitempty"`
	CacheWrite   int64 `json:"cache_write,omitempty"`
	CacheWrite1h int64 `json:"cache_write_1h,omitempty"`
	Reasoning    int64 `json:"reasoning,omitempty"`
}

// UsageTokens holds nullable canonical Track 1 token buckets.
type UsageTokens struct {
	Input          *int64                    `json:"input"`
	Output         *int64                    `json:"output"`
	CacheRead      *int64                    `json:"cache_read"`
	CacheWrite     *int64                    `json:"cache_write"`
	CacheWrite1h   *int64                    `json:"cache_write_1h"`
	Reasoning      *int64                    `json:"reasoning"`
	SummableTokens *int64                    `json:"summable_tokens"`
	UnknownEvents  *UsageBucketUnknownEvents `json:"unknown_events,omitempty"`
	Saturated      bool                      `json:"saturated,omitempty"`
}

// UsageScopeRef identifies the scope evaluated by a scoped usage API.
type UsageScopeRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

// UsageWindow reports the effective UTC window and optional rollup granularity.
type UsageWindow struct {
	Since       time.Time `json:"since"`
	Until       time.Time `json:"until"`
	Granularity string    `json:"granularity,omitempty"`
}

// UsageGroupRow is one grouped aggregation row in UsageSummaryResponse.
type UsageGroupRow struct {
	BucketStart     *time.Time  `json:"bucket_start,omitempty"`
	ProjectID       *string     `json:"project_id,omitempty"`
	UserID          *string     `json:"user_id,omitempty"`
	AgentID         *string     `json:"agent_id,omitempty"`
	Harness         *string     `json:"harness,omitempty"`
	Model           *string     `json:"model,omitempty"`
	Calls           int64       `json:"calls"`
	ErrorCalls      int64       `json:"error_calls"`
	Events          int64       `json:"events"`
	NullTokenEvents int64       `json:"null_token_events"`
	Tokens          UsageTokens `json:"tokens"`
}

// UsageSummaryResponse mirrors hub.UsageSummaryResponse for scoped usage APIs.
type UsageSummaryResponse struct {
	Scope             UsageScopeRef   `json:"scope"`
	Window            UsageWindow     `json:"window"`
	GroupBy           []string        `json:"group_by,omitempty"`
	Calls             int64           `json:"calls"`
	ErrorCalls        int64           `json:"error_calls"`
	Events            int64           `json:"events"`
	NullTokenEvents   int64           `json:"null_token_events"`
	Tokens            UsageTokens     `json:"tokens"`
	Groups            []UsageGroupRow `json:"groups,omitempty"`
	Truncated         bool            `json:"truncated"`
	MeasurementStatus string          `json:"measurement_status"`
	UnmeasuredAgents  int             `json:"unmeasured_agents"`
	StaleAgents       int             `json:"stale_agents"`
	AsOf              time.Time       `json:"as_of"`
	LastReceiptAt     *time.Time      `json:"last_receipt_at"`

	// Raw is the exact JSON body returned by the Hub for verbatim --json passthrough.
	Raw json.RawMessage `json:"-"`
}

// AgentUsageModelBreakdown reports per-model usage totals on AgentUsageResponse.
type AgentUsageModelBreakdown struct {
	Model           string      `json:"model"`
	Harness         string      `json:"harness,omitempty"`
	UsageSource     string      `json:"usage_source,omitempty"`
	Calls           int64       `json:"calls"`
	ErrorCalls      int64       `json:"error_calls"`
	Events          int64       `json:"events"`
	NullTokenEvents int64       `json:"null_token_events"`
	Tokens          UsageTokens `json:"tokens"`
}

// AgentUsageRunSummary is a compact attribution summary for recent agent runs.
type AgentUsageRunSummary struct {
	RunID         string    `json:"run_id"`
	Generation    int       `json:"generation"`
	Harness       string    `json:"harness,omitempty"`
	HarnessConfig string    `json:"harness_config,omitempty"`
	Template      string    `json:"template,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// AgentUsageResponse mirrors hub.AgentUsageResponse for GET /api/v1/agents/{id}/usage.
type AgentUsageResponse struct {
	AgentID           string                     `json:"agent_id"`
	ProjectID         string                     `json:"project_id"`
	Window            UsageWindow                `json:"window"`
	Calls             int64                      `json:"calls"`
	ErrorCalls        int64                      `json:"error_calls"`
	Events            int64                      `json:"events"`
	NullTokenEvents   int64                      `json:"null_token_events"`
	Tokens            UsageTokens                `json:"tokens"`
	ByModel           []AgentUsageModelBreakdown `json:"by_model"`
	Runs              []AgentUsageRunSummary     `json:"runs,omitempty"`
	MeasurementStatus string                     `json:"measurement_status"`
	AsOf              time.Time                  `json:"as_of"`
	LastReceiptAt     *time.Time                 `json:"last_receipt_at"`

	// Raw is the exact JSON body returned by the Hub for verbatim --json passthrough.
	Raw json.RawMessage `json:"-"`
}

func (s *usageService) GetProject(ctx context.Context, projectID string, opts *UsageQueryOptions) (*UsageSummaryResponse, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("project ID is required")
	}
	path := fmt.Sprintf("/api/v1/projects/%s/usage", url.PathEscape(projectID))
	return s.getSummary(ctx, path, opts)
}

func (s *usageService) GetMe(ctx context.Context, opts *UsageQueryOptions) (*UsageSummaryResponse, error) {
	return s.getSummary(ctx, "/api/v1/me/usage", opts)
}

func (s *usageService) GetUser(ctx context.Context, userID string, opts *UsageQueryOptions) (*UsageSummaryResponse, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("user ID is required")
	}
	path := fmt.Sprintf("/api/v1/users/%s/usage", url.PathEscape(userID))
	return s.getSummary(ctx, path, opts)
}

func (s *usageService) GetGlobal(ctx context.Context, opts *UsageQueryOptions) (*UsageSummaryResponse, error) {
	return s.getSummary(ctx, "/api/v1/usage", opts)
}

func (s *usageService) GetAgent(ctx context.Context, agentID string, opts *UsageQueryOptions) (*AgentUsageResponse, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent ID is required")
	}
	path := fmt.Sprintf("/api/v1/agents/%s/usage", url.PathEscape(agentID))
	var query url.Values
	if opts != nil {
		query = url.Values{}
		if opts.Since != "" {
			query.Set("since", opts.Since)
		}
		if opts.Until != "" {
			query.Set("until", opts.Until)
		}
		if len(query) == 0 {
			query = nil
		}
	}
	resp, err := s.c.getWithQuery(ctx, path, query, nil)
	if err != nil {
		return nil, err
	}
	raw, err := apiclient.DecodeRequired[json.RawMessage](resp)
	if err != nil {
		return nil, err
	}
	if raw == nil || bytes.Equal(bytes.TrimSpace(*raw), []byte("null")) {
		return nil, errors.New("empty or null agent usage response")
	}
	var out AgentUsageResponse
	if err := json.Unmarshal(*raw, &out); err != nil {
		return nil, fmt.Errorf("failed to decode agent usage response: %w", err)
	}
	out.Raw = *raw
	return &out, nil
}

func (s *usageService) getSummary(ctx context.Context, path string, opts *UsageQueryOptions) (*UsageSummaryResponse, error) {
	query := buildUsageSummaryQuery(opts)
	resp, err := s.c.getWithQuery(ctx, path, query, nil)
	if err != nil {
		return nil, err
	}
	raw, err := apiclient.DecodeRequired[json.RawMessage](resp)
	if err != nil {
		return nil, err
	}
	if raw == nil || bytes.Equal(bytes.TrimSpace(*raw), []byte("null")) {
		return nil, errors.New("empty or null usage summary response")
	}
	var out UsageSummaryResponse
	if err := json.Unmarshal(*raw, &out); err != nil {
		return nil, fmt.Errorf("failed to decode usage summary response: %w", err)
	}
	out.Raw = *raw
	return &out, nil
}

func buildUsageSummaryQuery(opts *UsageQueryOptions) url.Values {
	if opts == nil {
		return nil
	}
	q := url.Values{}
	if opts.Since != "" {
		q.Set("since", opts.Since)
	}
	if opts.Until != "" {
		q.Set("until", opts.Until)
	}
	if len(opts.GroupBy) > 0 {
		q.Set("groupBy", strings.Join(opts.GroupBy, ","))
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.ProjectID != "" {
		q.Set("project_id", opts.ProjectID)
	}
	if opts.UserID != "" {
		q.Set("user_id", opts.UserID)
	}
	if opts.AgentID != "" {
		q.Set("agent_id", opts.AgentID)
	}
	if opts.Harness != "" {
		q.Set("harness", opts.Harness)
	}
	if opts.Model != "" {
		q.Set("model", opts.Model)
	}
	if opts.UsageSource != "" {
		q.Set("usage_source", opts.UsageSource)
	}
	if len(q) == 0 {
		return nil
	}
	return q
}

// UsageBudgetEntry mirrors hub.UsageBudgetEntry for active raw-token budgets.
type UsageBudgetEntry struct {
	ID                 string     `json:"id,omitempty"`
	LimitDefinitionID  string     `json:"limit_definition_id"`
	LimitName          string     `json:"limit_name"`
	ScopeKind          string     `json:"scope_kind"`
	ScopeID            string     `json:"scope_id"`
	ProjectScopeID     string     `json:"project_scope_id,omitempty"`
	WindowStartUTC     time.Time  `json:"window_start_utc"`
	WindowEndUTC       time.Time  `json:"window_end_utc"`
	Cap                *int64     `json:"cap"`
	TokenAction        string     `json:"token_action"`
	Consumed           int64      `json:"consumed"`
	State              string     `json:"state"`
	ThresholdsNotified int64      `json:"thresholds_notified"`
	OverrideUntil      *time.Time `json:"override_until"`
	StateVersion       int64      `json:"state_version"`
	AsOf               time.Time  `json:"as_of"`

	// Raw is the exact JSON body returned by the Hub on single-entry responses
	// (such as :override) for verbatim --json passthrough.
	Raw json.RawMessage `json:"-"`
}

// UsageBudgetsResponse mirrors hub.UsageBudgetsResponse for project, user, and /me budget reads.
type UsageBudgetsResponse struct {
	Scope   UsageScopeRef      `json:"scope"`
	AsOf    time.Time          `json:"as_of"`
	Budgets []UsageBudgetEntry `json:"budgets"`

	// Raw is the exact JSON body returned by the Hub for verbatim --json passthrough.
	Raw json.RawMessage `json:"-"`
}

// GroupBudgetMemberView mirrors hub.GroupBudgetMemberView for pooled-group member breakdown.
type GroupBudgetMemberView struct {
	UserID         string `json:"user_id"`
	Email          string `json:"email,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	TokensSummable int64  `json:"tokens_summable"`
	Calls          int64  `json:"calls"`
	Events         int64  `json:"events"`
}

// GroupBudgetItem mirrors hub.GroupBudgetItem for pooled-group raw-token budgets.
type GroupBudgetItem struct {
	ID                 string                  `json:"id,omitempty"`
	LimitDefinitionID  string                  `json:"limit_definition_id"`
	LimitName          string                  `json:"limit_name"`
	ScopeKind          string                  `json:"scope_kind"`
	ScopeID            string                  `json:"scope_id"`
	ProjectScopeID     string                  `json:"project_scope_id,omitempty"`
	WindowStartUTC     time.Time               `json:"window_start_utc"`
	WindowEndUTC       time.Time               `json:"window_end_utc"`
	Cap                *int64                  `json:"cap"`
	TokenAction        string                  `json:"token_action"`
	Consumed           int64                   `json:"consumed"`
	State              string                  `json:"state"`
	ThresholdsNotified int64                   `json:"thresholds_notified"`
	OverrideUntil      *time.Time              `json:"override_until"`
	StateVersion       int64                   `json:"state_version"`
	AsOf               time.Time               `json:"as_of"`
	NonAdditive        bool                    `json:"non_additive"`
	Members            []GroupBudgetMemberView `json:"members,omitempty"`
}

// GroupUsageBudgetsResponse mirrors hub.GroupUsageBudgetsResponse.
type GroupUsageBudgetsResponse struct {
	Scope       UsageScopeRef     `json:"scope"`
	GroupName   string            `json:"group_name,omitempty"`
	NonAdditive bool              `json:"non_additive"`
	AsOf        time.Time         `json:"as_of"`
	Budgets     []GroupBudgetItem `json:"budgets"`

	// Raw is the exact JSON body returned by the Hub for verbatim --json passthrough.
	Raw json.RawMessage `json:"-"`
}

// GroupBudgetsQueryOptions configures query parameters for GET /api/v1/groups/{id}/usage/budgets.
type GroupBudgetsQueryOptions struct {
	ProjectID string
}

// OverrideUsageBudgetRequest is the payload for POST .../usage/budgets/{budgetId}:override.
// OverrideUntil deliberately omits `omitempty` so a nil pointer serializes as
// `"override_until": null` to clear an active override.
type OverrideUsageBudgetRequest struct {
	OverrideUntil *time.Time `json:"override_until"`
	StateVersion  *int64     `json:"state_version,omitempty"`
	Reason        string     `json:"reason,omitempty"`
}

func (s *usageService) GetProjectBudgets(ctx context.Context, projectID string) (*UsageBudgetsResponse, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("project ID is required")
	}
	path := fmt.Sprintf("/api/v1/projects/%s/usage/budgets", url.PathEscape(projectID))
	return s.getBudgets(ctx, path)
}

func (s *usageService) GetMeBudgets(ctx context.Context) (*UsageBudgetsResponse, error) {
	return s.getBudgets(ctx, "/api/v1/me/usage/budgets")
}

func (s *usageService) GetUserBudgets(ctx context.Context, userID string) (*UsageBudgetsResponse, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("user ID is required")
	}
	path := fmt.Sprintf("/api/v1/users/%s/usage/budgets", url.PathEscape(userID))
	return s.getBudgets(ctx, path)
}

func (s *usageService) GetGroupBudgets(ctx context.Context, groupIDOrSlug string, opts *GroupBudgetsQueryOptions) (*GroupUsageBudgetsResponse, error) {
	if strings.TrimSpace(groupIDOrSlug) == "" {
		return nil, errors.New("group ID or slug is required")
	}
	path := fmt.Sprintf("/api/v1/groups/%s/usage/budgets", url.PathEscape(groupIDOrSlug))
	var query url.Values
	if opts != nil && strings.TrimSpace(opts.ProjectID) != "" {
		query = url.Values{"project_id": {strings.TrimSpace(opts.ProjectID)}}
	}
	resp, err := s.c.getWithQuery(ctx, path, query, nil)
	if err != nil {
		return nil, err
	}
	raw, err := apiclient.DecodeRequired[json.RawMessage](resp)
	if err != nil {
		return nil, err
	}
	if raw == nil || bytes.Equal(bytes.TrimSpace(*raw), []byte("null")) {
		return nil, errors.New("empty or null group usage budgets response")
	}
	var out GroupUsageBudgetsResponse
	if err := json.Unmarshal(*raw, &out); err != nil {
		return nil, fmt.Errorf("failed to decode group usage budgets response: %w", err)
	}
	out.Raw = *raw
	return &out, nil
}

func (s *usageService) OverrideProjectBudget(ctx context.Context, projectID, budgetID string, req *OverrideUsageBudgetRequest) (*UsageBudgetEntry, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("project ID is required")
	}
	if strings.TrimSpace(budgetID) == "" {
		return nil, errors.New("budget ID is required")
	}
	path := fmt.Sprintf("/api/v1/projects/%s/usage/budgets/%s:override", url.PathEscape(projectID), url.PathEscape(budgetID))
	return s.postBudgetOverride(ctx, path, req)
}

func (s *usageService) OverrideUserBudget(ctx context.Context, userID, budgetID string, req *OverrideUsageBudgetRequest) (*UsageBudgetEntry, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("user ID is required")
	}
	if strings.TrimSpace(budgetID) == "" {
		return nil, errors.New("budget ID is required")
	}
	path := fmt.Sprintf("/api/v1/users/%s/usage/budgets/%s:override", url.PathEscape(userID), url.PathEscape(budgetID))
	return s.postBudgetOverride(ctx, path, req)
}

func (s *usageService) getBudgets(ctx context.Context, path string) (*UsageBudgetsResponse, error) {
	resp, err := s.c.get(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	raw, err := apiclient.DecodeRequired[json.RawMessage](resp)
	if err != nil {
		return nil, err
	}
	if raw == nil || bytes.Equal(bytes.TrimSpace(*raw), []byte("null")) {
		return nil, errors.New("empty or null usage budgets response")
	}
	var out UsageBudgetsResponse
	if err := json.Unmarshal(*raw, &out); err != nil {
		return nil, fmt.Errorf("failed to decode usage budgets response: %w", err)
	}
	out.Raw = *raw
	return &out, nil
}

func (s *usageService) postBudgetOverride(ctx context.Context, path string, req *OverrideUsageBudgetRequest) (*UsageBudgetEntry, error) {
	if req == nil {
		return nil, errors.New("override request is required")
	}
	resp, err := s.c.post(ctx, path, req, nil)
	if err != nil {
		return nil, err
	}
	raw, err := apiclient.DecodeRequired[json.RawMessage](resp)
	if err != nil {
		return nil, err
	}
	if raw == nil || bytes.Equal(bytes.TrimSpace(*raw), []byte("null")) {
		return nil, errors.New("empty or null usage budget override response")
	}
	var out UsageBudgetEntry
	if err := json.Unmarshal(*raw, &out); err != nil {
		return nil, fmt.Errorf("failed to decode usage budget override response: %w", err)
	}
	out.Raw = *raw
	return &out, nil
}
