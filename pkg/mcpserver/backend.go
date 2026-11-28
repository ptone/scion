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

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/google/uuid"
)

const defaultMaxPrincipalCacheEntries = 1024

// HubBackend is the narrow interface over the Hub REST API used by MCP tools,
// resources, events, and front door. One implementation serves both embedded
// and standalone modes; only the underlying http.RoundTripper differs.
type HubBackend interface {
	Me(ctx context.Context, c Credential) (Principal, error)
	ListAgents(ctx context.Context, c Credential, project string) ([]AgentRef, error)
	SendToAgent(ctx context.Context, c Credential, project, agent, text string) (SendResult, error)
	SendToConversation(ctx context.Context, c Credential, convID, text string) (SendResult, error)
	ListConversations(ctx context.Context, c Credential, opts ListConvOpts) (ConvPage, error)
	GetConversation(ctx context.Context, c Credential, convID string) (*ConversationSummary, error)
	GetMessage(ctx context.Context, c Credential, convID, msgID string) (*MessageDTO, error)
	Watch(ctx context.Context, c Credential, convID string, o hubclient.WatchOptions) (*hubclient.WatchResult, error)
	ExperimentEnabled(ctx context.Context, c Credential, name string) (bool, error)
}

// ExperimentChecker resolves whether a named server experiment is enabled.
// In embedded mode this wraps hubSrv.ExperimentEnabled directly; in standalone
// mode (P4) it queries GET /api/v1/experiments with caching.
type ExperimentChecker func(ctx context.Context, c Credential, name string) (bool, error)

// BackendOptions configures NewBackend.
type BackendOptions struct {
	// HubBaseURL is the base URL passed to hubclient.New (e.g., "http://in-process"
	// in embedded mode or the remote hub URL in standalone mode).
	HubBaseURL string
	// Transport is the base http.RoundTripper (InProcessTransport for embedded
	// mode, network RoundTripper for standalone mode).
	Transport http.RoundTripper
	// Version is the server version stamped into User-Agent: scion-mcp/<ver>.
	Version string
	// ExperimentChecker resolves hub experiments per request.
	ExperimentChecker ExperimentChecker
	// DisableTransportAuth suppresses hubclient's automatic SCION_TRANSPORT_MODE
	// wrapping. Set true for embedded mode; P4 leaves it false for standalone.
	DisableTransportAuth bool
}

type hubBackend struct {
	baseURL              string
	transport            http.RoundTripper
	expCheck             ExperimentChecker
	disableTransportAuth bool
}

// NewBackend creates a HubBackend backed by hubclient and the given transport.
// The transport is always wrapped in AllowlistTransport so only allowlisted
// headers can ever reach the Hub.
func NewBackend(opts BackendOptions) HubBackend {
	baseURL := opts.HubBaseURL
	if baseURL == "" {
		baseURL = "http://in-process"
	}
	rt := &AllowlistTransport{
		Base:    opts.Transport,
		Version: opts.Version,
	}
	return &hubBackend{
		baseURL:              baseURL,
		transport:            rt,
		expCheck:             opts.ExperimentChecker,
		disableTransportAuth: opts.DisableTransportAuth,
	}
}

func (b *hubBackend) newHTTPClient() *http.Client {
	return &http.Client{
		Transport: b.transport,
		// Audit L1: never follow redirects from the Hub backend.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (b *hubBackend) clientFor(c Credential) (hubclient.Client, error) {
	hc := b.newHTTPClient()
	opts := []hubclient.Option{
		hubclient.WithHTTPClient(hc),
		hubclient.WithAuthenticator(&credentialAuthenticator{cred: c}),
	}
	if b.disableTransportAuth {
		opts = append(opts, hubclient.WithTransportAuth(nil, 0))
	}
	return hubclient.New(b.baseURL, opts...)
}

// validatePathSegment rejects empty and dot-segment path inputs ("", ".", "..")
// so caller-supplied identifiers cannot steer URL path resolution to sibling
// Hub routes (Audit L1).
func validatePathSegment(field, value string) error {
	if value == "" || value == "." || value == ".." {
		return fmt.Errorf("invalid %s: must not be empty, '.', or '..'", field)
	}
	return nil
}

func (b *hubBackend) Me(ctx context.Context, c Credential) (Principal, error) {
	cl, err := b.clientFor(c)
	if err != nil {
		return Principal{}, err
	}
	u, err := cl.Auth().Me(ctx)
	if err != nil {
		return Principal{}, err
	}
	// /api/v1/auth/me does not expose an identity kind field, so leave Kind
	// empty rather than hard-coding "user" (which would mislabel visitors).
	return Principal{
		ID:      u.ID,
		Email:   u.Email,
		Display: u.DisplayName,
	}, nil
}

func (b *hubBackend) ListAgents(ctx context.Context, c Credential, project string) ([]AgentRef, error) {
	if err := validatePathSegment("project", project); err != nil {
		return nil, err
	}
	cl, err := b.clientFor(c)
	if err != nil {
		return nil, err
	}
	projectID, err := resolveProjectID(ctx, cl, project)
	if err != nil {
		return nil, err
	}
	resp, err := cl.ProjectAgents(projectID).List(ctx, nil)
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Agents) == 0 {
		return []AgentRef{}, nil
	}
	out := make([]AgentRef, 0, len(resp.Agents))
	for _, a := range resp.Agents {
		name := a.Slug
		if name == "" {
			name = a.Name
		}
		display := a.Name
		if display == "" {
			display = a.Slug
		}
		pid := a.ProjectID
		if pid == "" {
			pid = projectID
		}
		out = append(out, AgentRef{
			ID:        a.ID,
			Name:      name,
			ProjectID: pid,
			Phase:     a.Phase,
			Display:   display,
		})
	}
	return out, nil
}

func (b *hubBackend) SendToAgent(ctx context.Context, c Credential, project, agent, text string) (SendResult, error) {
	if err := validatePathSegment("project", project); err != nil {
		return SendResult{}, err
	}
	if err := validatePathSegment("agent", agent); err != nil {
		return SendResult{}, err
	}
	cl, err := b.clientFor(c)
	if err != nil {
		return SendResult{}, err
	}

	projectID, err := resolveProjectID(ctx, cl, project)
	if err != nil {
		return SendResult{}, err
	}

	msg := &messages.StructuredMessage{
		Version: messages.Version,
		Msg:     text,
		Type:    messages.TypeInstruction,
	}
	resp, err := cl.ProjectAgents(projectID).SendStructuredMessageWithOptions(ctx, agent, msg, hubclient.SendMessageOptions{})
	if err != nil {
		// Design §4.4: if the agent name is unknown or ambiguous within the
		// project, include candidate agents from list_agents when readable.
		// Wrap with hubHintError so hubStatusCode retains the Hub HTTP status
		// for audit logs while formatHubToolError surfaces the candidate hint.
		var apiErr *apiclient.APIError
		if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusConflict) {
			candidates, listErr := b.ListAgents(ctx, c, projectID)
			if listErr == nil && len(candidates) > 0 {
				names := make([]string, 0, len(candidates))
				for _, cand := range candidates {
					names = append(names, fmt.Sprintf("%s (%s)", cand.Name, cand.ID))
				}
				return SendResult{}, &hubHintError{
					cause: err,
					msg:   fmt.Sprintf("%s; available agents in project: %s", formatHubToolError(err), strings.Join(names, ", ")),
				}
			}
			if listErr != nil {
				return SendResult{}, &hubHintError{
					cause: err,
					msg:   fmt.Sprintf("%s (could not list candidate agents; listing agents requires the agent:list scope)", formatHubToolError(err)),
				}
			}
		}
		return SendResult{}, err
	}

	res := SendResult{
		MessageID:      resp.MessageID,
		ConversationID: resp.ConversationID,
		Status:         resp.Status,
		Deferred:       resp.Deferred,
		Cursor:         resp.Cursor,
	}
	if resp.Cursor == "" {
		// Hub already accepted/delivered the message (2xx), so never return a
		// tool error here (which would prompt an LLM caller to resend and
		// duplicate the side effect).
		res.Warning = "delivered but not recorded in conversation history; replies cannot be read via this conversation; do not resend"
	}

	return res, nil
}

func (b *hubBackend) SendToConversation(ctx context.Context, c Credential, convID, text string) (SendResult, error) {
	if err := validatePathSegment("conversation_id", convID); err != nil {
		return SendResult{}, err
	}
	cl, err := b.clientFor(c)
	if err != nil {
		return SendResult{}, err
	}

	conv, err := cl.Conversations().Get(ctx, convID)
	if err != nil {
		return SendResult{}, err
	}

	// For a direct conversation between the caller (user) and an agent,
	// deliver via the agent message endpoint so the Hub enforces agent:message
	// and allocates the cursor atomically with the persisted message.
	// Only take this shortcut when the DM key is strictly user<->agent AND the
	// user side matches the authenticated caller (O1): an agent<->agent DM or
	// another user's DM must never silently land in the caller's own DM.
	// Use the front door's already-resolved principal ID for routing only (never
	// for authorization — the Hub still authorizes the outbound POST).
	if conv.Kind == "direct" {
		if userID, agentID, ok := parseUserAgentDM(conv.ExternalRef); ok {
			var callerID string
			if ra, hasAuth := requestAuthFromContext(ctx); hasAuth && ra.Principal.ID != "" {
				callerID = ra.Principal.ID
			} else if caller, meErr := b.Me(ctx, c); meErr == nil {
				callerID = caller.ID
			}
			if callerID != "" && callerID == userID {
				msg := &messages.StructuredMessage{
					Version: messages.Version,
					Msg:     text,
					Type:    messages.TypeInstruction,
				}
				var resp *hubclient.MessageResponse
				if conv.ProjectID != nil && *conv.ProjectID != "" {
					resp, err = cl.ProjectAgents(*conv.ProjectID).SendStructuredMessageWithOptions(ctx, agentID, msg, hubclient.SendMessageOptions{})
				} else {
					resp, err = cl.Agents().SendStructuredMessageWithOptions(ctx, agentID, msg, hubclient.SendMessageOptions{})
				}
				if err != nil {
					return SendResult{}, err
				}
				res := SendResult{
					MessageID:      resp.MessageID,
					ConversationID: resp.ConversationID,
					Status:         resp.Status,
					Deferred:       resp.Deferred,
					Cursor:         resp.Cursor,
				}
				if res.ConversationID == "" {
					res.ConversationID = convID
				}
				if resp.ConversationID != "" && resp.ConversationID != convID {
					// Hub already accepted/delivered the message (2xx); return
					// success with a warning rather than an error so the caller
					// does not resend and duplicate the side effect.
					res.Warning = fmt.Sprintf("delivered to conversation %q instead of requested %q; do not resend", resp.ConversationID, convID)
				} else if res.Cursor == "" {
					res.Warning = "delivered but not recorded in conversation history; replies cannot be read via this conversation; do not resend"
				}
				return res, nil
			}
		}
	}

	// Derive the chat conversation key for POST /api/v1/chat/conversations/{key}/messages:
	// - group conversations carry ExternalRef = "thread:<projectID>:<threadID>";
	//   the chat route expects the bare <threadID> (R1).
	// - direct conversations carry ExternalRef = "dm:<kindA>:<idA>:<kindB>:<idB>";
	//   the chat route expects the dm: key and enforces authorizeDMKeyParticipant (O1).
	chatKey, err := chatSendKeyForConversation(conv)
	if err != nil {
		return SendResult{}, err
	}
	msgID, status, cursor, err := b.postChatConversationMessage(ctx, c, chatKey, text)
	if err != nil {
		return SendResult{}, err
	}

	res := SendResult{
		MessageID:      msgID,
		ConversationID: convID,
		Status:         status,
		Cursor:         cursor,
	}
	if res.Cursor == "" {
		res.Warning = "delivered but cursor was not returned by Hub; do not resend"
	}
	return res, nil
}

// parseUserAgentDM returns (userID, agentID, true) when externalRef is a
// canonical DM key between a user and an agent.
func parseUserAgentDM(externalRef string) (userID, agentID string, ok bool) {
	if externalRef == "" {
		return "", "", false
	}
	kindA, idA, kindB, idB, err := messages.ParseDMKey(externalRef)
	if err != nil {
		return "", "", false
	}
	if kindA == "user" && kindB == "agent" && idA != "" && idB != "" {
		return idA, idB, true
	}
	if kindA == "agent" && kindB == "user" && idA != "" && idB != "" {
		return idB, idA, true
	}
	return "", "", false
}

// chatSendKeyForConversation derives the {key} segment for
// POST /api/v1/chat/conversations/{key}/messages.
func chatSendKeyForConversation(conv *hubclient.ConversationDetail) (string, error) {
	if conv == nil {
		return "", errors.New("conversation not found")
	}
	switch conv.Kind {
	case "group":
		// Native group conversations use ExternalRef = "thread:<projectID>:<threadID>".
		parts := strings.SplitN(conv.ExternalRef, ":", 3)
		if len(parts) == 3 && parts[0] == "thread" && parts[1] != "" && parts[2] != "" {
			if err := validatePathSegment("thread_id", parts[2]); err != nil {
				return "", err
			}
			return parts[2], nil
		}
		return "", fmt.Errorf("sending to group conversation %q (surface=%q) is not supported via MCP: no native chat thread reference", conv.ID, conv.Surface)
	case "direct":
		if strings.HasPrefix(conv.ExternalRef, "dm:") {
			if err := validatePathSegment("dm_key", conv.ExternalRef); err != nil {
				return "", err
			}
			return conv.ExternalRef, nil
		}
		return "", fmt.Errorf("sending to direct conversation %q is not supported via MCP: missing canonical DM reference", conv.ID)
	default:
		return "", fmt.Errorf("sending to conversation %q of kind %q is not supported via MCP", conv.ID, conv.Kind)
	}
}

func (b *hubBackend) postChatConversationMessage(ctx context.Context, c Credential, key, text string) (messageID, status, cursor string, err error) {
	payload, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return "", "", "", err
	}
	endpoint := strings.TrimRight(b.baseURL, "/") + "/api/v1/chat/conversations/" + url.PathEscape(key) + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if err := (&credentialAuthenticator{cred: c}).ApplyAuth(req); err != nil {
		return "", "", "", err
	}

	resp, err := b.newHTTPClient().Do(req)
	if err != nil {
		return "", "", "", err
	}
	type chatSendResp struct {
		ID            string `json:"id"`
		DispatchState string `json:"dispatchState"`
		Cursor        string `json:"cursor"`
	}
	decoded, err := apiclient.DecodeRequired[chatSendResp](resp)
	if err != nil {
		return "", "", "", err
	}
	st := decoded.DispatchState
	if st == "" {
		st = "sent"
	}
	return decoded.ID, st, decoded.Cursor, nil
}

func (b *hubBackend) ListConversations(ctx context.Context, c Credential, opts ListConvOpts) (ConvPage, error) {
	cl, err := b.clientFor(c)
	if err != nil {
		return ConvPage{}, err
	}
	var projectID string
	if opts.Project != "" {
		if err := validatePathSegment("project", opts.Project); err != nil {
			return ConvPage{}, err
		}
		projectID, err = resolveProjectID(ctx, cl, opts.Project)
		if err != nil {
			return ConvPage{}, err
		}
	}

	listRes, err := cl.Conversations().List(ctx, &hubclient.ListConversationsOptions{
		ProjectID: projectID,
		Limit:     opts.Limit,
	})
	if err != nil {
		return ConvPage{}, err
	}
	if listRes == nil || len(listRes.Conversations) == 0 {
		total := 0
		truncated := false
		if listRes != nil {
			total = listRes.Total
			truncated = listRes.Truncated
		}
		return ConvPage{Conversations: []ConversationSummary{}, Total: total, Truncated: truncated}, nil
	}
	items := make([]ConversationSummary, 0, len(listRes.Conversations))
	for _, conv := range listRes.Conversations {
		items = append(items, projectConversationSummary(conv))
	}
	return ConvPage{
		Conversations: items,
		Total:         listRes.Total,
		Truncated:     listRes.Truncated,
	}, nil
}

func (b *hubBackend) GetConversation(ctx context.Context, c Credential, convID string) (*ConversationSummary, error) {
	if err := validatePathSegment("conversation_id", convID); err != nil {
		return nil, err
	}
	cl, err := b.clientFor(c)
	if err != nil {
		return nil, err
	}
	detail, err := cl.Conversations().Get(ctx, convID)
	if err != nil {
		return nil, err
	}
	summary := projectConversationSummary(*detail)
	return &summary, nil
}

func (b *hubBackend) GetMessage(ctx context.Context, c Credential, convID, msgID string) (*MessageDTO, error) {
	if err := validatePathSegment("conversation_id", convID); err != nil {
		return nil, err
	}
	if err := validatePathSegment("message_id", msgID); err != nil {
		return nil, err
	}
	cl, err := b.clientFor(c)
	if err != nil {
		return nil, err
	}
	msg, err := cl.Conversations().GetMessage(ctx, convID, msgID)
	if err != nil {
		return nil, err
	}
	dto := projectHubMessageDTO(msg, convID)
	return &dto, nil
}

// resolveProjectID resolves a project slug to its UUID via the Hub API using
// the caller's own credential. If project is already a valid UUID it is
// returned directly. If the Hub returns an error, the Hub's error reason is
// returned verbatim; if lookup succeeds with 0 or multiple matches, a clear
// hint is returned.
func resolveProjectID(ctx context.Context, cl hubclient.Client, project string) (string, error) {
	if _, err := uuid.Parse(project); err == nil {
		return project, nil
	}
	list, err := cl.Projects().List(ctx, &hubclient.ListProjectsOptions{Slug: project})
	if err != nil {
		return "", err
	}
	if list != nil && len(list.Projects) == 1 && list.Projects[0].ID != "" {
		return list.Projects[0].ID, nil
	}
	return "", fmt.Errorf("cannot resolve project slug %q: pass the project ID (UUID) instead (slug lookup requires the project:list scope, which is not included in the five standard messaging UAT selectors)", project)
}

func (b *hubBackend) Watch(ctx context.Context, c Credential, convID string, o hubclient.WatchOptions) (*hubclient.WatchResult, error) {
	if err := validatePathSegment("conversation_id", convID); err != nil {
		return nil, err
	}
	cl, err := b.clientFor(c)
	if err != nil {
		return nil, err
	}
	return cl.Conversations().Watch(ctx, convID, o)
}

func (b *hubBackend) ExperimentEnabled(ctx context.Context, c Credential, name string) (bool, error) {
	if b.expCheck == nil {
		return false, nil
	}
	return b.expCheck(ctx, c, name)
}

// credentialAuthenticator applies a Credential to outbound Hub requests.
type credentialAuthenticator struct {
	cred Credential
}

func (a *credentialAuthenticator) ApplyAuth(req *http.Request) error {
	if req == nil {
		return errors.New("mcpserver: nil request")
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if a.cred.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+a.cred.Bearer)
		return nil
	}
	if a.cred.Federation != "" {
		req.Header.Set(HeaderFederationToken, a.cred.Federation)
		return nil
	}
	return nil
}

func (a *credentialAuthenticator) Refresh() (bool, error) { return false, nil }

// principalCache caches GET /api/v1/auth/me results keyed by sha256(credential)
// for at most ttl (default 60s) to feed audit logs. It never authorizes a Hub
// call: every tool/event request re-presents the credential to the Hub.
// Live entries are bounded by maxEntries (Audit L2).
type principalCache struct {
	mu         sync.RWMutex
	ttl        time.Duration
	maxEntries int
	entries    map[string]principalCacheEntry
}

type principalCacheEntry struct {
	principal Principal
	expiresAt time.Time
}

func newPrincipalCache(ttl time.Duration) *principalCache {
	return newPrincipalCacheWithCap(ttl, defaultMaxPrincipalCacheEntries)
}

func newPrincipalCacheWithCap(ttl time.Duration, maxEntries int) *principalCache {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	if maxEntries <= 0 {
		maxEntries = defaultMaxPrincipalCacheEntries
	}
	return &principalCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[string]principalCacheEntry),
	}
}

func (c *principalCache) get(key string, now time.Time) (Principal, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || now.After(e.expiresAt) {
		return Principal{}, false
	}
	return e.principal, true
}

func (c *principalCache) set(key string, p Principal, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		c.entries[key] = principalCacheEntry{
			principal: p,
			expiresAt: now.Add(c.ttl),
		}
		return
	}
	// Bound memory for long-lived servers: prune expired entries first, then
	// evict live entries if still at the cap (Audit L2).
	if len(c.entries) >= c.maxEntries {
		for k, v := range c.entries {
			if now.After(v.expiresAt) {
				delete(c.entries, k)
			}
		}
		for len(c.entries) >= c.maxEntries {
			for k := range c.entries {
				delete(c.entries, k)
				break
			}
		}
	}
	c.entries[key] = principalCacheEntry{
		principal: p,
		expiresAt: now.Add(c.ttl),
	}
}

func (c *principalCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// hubStatusCode extracts the HTTP status code from a Hub error if present.
func hubStatusCode(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// hubHintError wraps a Hub error (preserving *apiclient.APIError via Unwrap
// for hubStatusCode/audit logging) while carrying an enriched tool-error hint.
type hubHintError struct {
	cause error
	msg   string
}

func (e *hubHintError) Error() string { return e.msg }
func (e *hubHintError) Unwrap() error { return e.cause }

// formatHubToolError formats a Hub error for an MCP tool error result,
// preserving the Hub's error code and message verbatim (e.g. missing UAT
// selector or 403 reason).
func formatHubToolError(err error) string {
	var hintErr *hubHintError
	if errors.As(err, &hintErr) {
		return hintErr.msg
	}
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code != "" {
			return fmt.Sprintf("hub error (%d %s): %s", apiErr.StatusCode, apiErr.Code, apiErr.Message)
		}
		return fmt.Sprintf("hub error (%d): %s", apiErr.StatusCode, apiErr.Message)
	}
	return err.Error()
}
