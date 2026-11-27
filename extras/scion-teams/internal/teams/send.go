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

package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/plugin/teamshosts"
)

// ErrRedirectNotAllowed is returned when a Bot Connector HTTP request
// attempts to follow an HTTP redirect.
var ErrRedirectNotAllowed = errors.New("teams service URL redirect not allowed")

func refuseRedirect(*http.Request, []*http.Request) error {
	return ErrRedirectNotAllowed
}

// RetryAfterError indicates the API returned 429 Too Many Requests with
// a Retry-After header. The caller should wait the specified duration.
type RetryAfterError struct {
	RetryAfter time.Duration
}

func (e *RetryAfterError) Error() string {
	return fmt.Sprintf("rate limited: retry after %s", e.RetryAfter)
}

// Sender handles outbound REST API calls to the Bot Connector service.
type Sender struct {
	tokenProvider     *TokenProvider
	httpClient        *http.Client
	log               *slog.Logger
	extraServiceHosts []string
}

// NewSender creates a new Sender.
func NewSender(tokenProvider *TokenProvider, log *slog.Logger) *Sender {
	return &Sender{
		tokenProvider: tokenProvider,
		httpClient: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: refuseRedirect,
		},
		log: log,
	}
}

// SetExtraServiceHosts extends the built-in Bot Framework service URL host
// allowlist with additional hostnames (for sovereign clouds or local emulators).
// It never replaces or empties the built-in allowlist.
func (s *Sender) SetExtraServiceHosts(hosts []string) {
	if len(hosts) == 0 {
		s.extraServiceHosts = nil
		return
	}
	cp := make([]string, len(hosts))
	copy(cp, hosts)
	s.extraServiceHosts = cp
}

// sendActivity sends a new Activity to a conversation.
// POST {serviceUrl}/v3/conversations/{conversationId}/activities
// Returns the created activity ID.
func (s *Sender) sendActivity(ctx context.Context, serviceURL, conversationID string, activity *Activity) (string, error) {
	url := buildAPIURL(serviceURL, conversationID, "")
	return s.doSendRequest(ctx, http.MethodPost, url, activity)
}

// replyToActivity sends a reply to a specific activity in a conversation.
// The activity's ReplyToID is set to the target activity.
func (s *Sender) replyToActivity(ctx context.Context, serviceURL, conversationID, replyToID string, activity *Activity) (string, error) {
	activity.ReplyToID = replyToID
	url := buildAPIURL(serviceURL, conversationID, "")
	return s.doSendRequest(ctx, http.MethodPost, url, activity)
}

// updateActivity updates an existing Activity.
// PUT {serviceUrl}/v3/conversations/{conversationId}/activities/{activityId}
func (s *Sender) updateActivity(ctx context.Context, serviceURL, conversationID, activityID string, activity *Activity) error {
	url := buildAPIURL(serviceURL, conversationID, activityID)
	_, err := s.doSendRequest(ctx, http.MethodPut, url, activity)
	return err
}

// deleteActivity deletes an existing Activity.
// DELETE {serviceUrl}/v3/conversations/{conversationId}/activities/{activityId}
func (s *Sender) deleteActivity(ctx context.Context, serviceURL, conversationID, activityID string) error {
	apiURL := buildAPIURL(serviceURL, conversationID, activityID)

	resp, err := s.doWithRetry(ctx, http.MethodDelete, apiURL, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return parseRetryAfter(resp)
	}
	if resp.StatusCode >= 400 {
		return s.parseErrorResponse(resp)
	}

	return nil
}

// doSendRequest executes a POST or PUT with a JSON body, handling auth
// and 401 retry. Returns the activity ID from the response.
func (s *Sender) doSendRequest(ctx context.Context, method, apiURL string, activity *Activity) (string, error) {
	body, err := json.Marshal(activity)
	if err != nil {
		return "", fmt.Errorf("marshal activity: %w", err)
	}

	resp, err := s.doWithRetry(ctx, method, apiURL, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", parseRetryAfter(resp)
	}

	if resp.StatusCode >= 400 {
		return "", s.parseErrorResponse(resp)
	}

	// Parse the response for the activity ID.
	var result ActivityResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// Some successful responses have no body (204).
		return "", nil
	}

	return result.ID, nil
}

// doWithRetry executes an HTTP request with automatic 401 retry. If the
// first request returns 401, it invalidates the cached token, fetches a
// fresh one, and retries once. The caller owns the returned response body.
func (s *Sender) doWithRetry(ctx context.Context, method, apiURL string, body []byte) (*http.Response, error) {
	resp, err := s.doRequest(ctx, method, apiURL, body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		// Token may have expired — force refresh and retry once.
		s.log.Debug("Got 401, refreshing token and retrying", "url", apiURL)
		resp.Body.Close() // close first response before retrying

		s.tokenProvider.InvalidateToken()

		retryResp, retryErr := s.doRequest(ctx, method, apiURL, body)
		if retryErr != nil {
			return nil, retryErr // no nil resp to close
		}
		return retryResp, nil
	}

	return resp, nil
}

// doRequest validates the target URL against the Bot Framework host allowlist,
// acquires a token, and executes an authorized HTTP request without following
// redirects.
func (s *Sender) doRequest(ctx context.Context, method, rawURL string, body []byte) (*http.Response, error) {
	if err := teamshosts.ValidateServiceURL(rawURL, s.extraServiceHosts...); err != nil {
		var denied *teamshosts.ServiceURLDeniedError
		if errors.As(err, &denied) {
			s.log.Warn("teams service URL denied",
				"event", "teams_service_url_denied",
				"direction", "outbound",
				"reason", denied.Reason,
				"host", denied.Host,
			)
		} else {
			s.log.Warn("teams service URL denied",
				"event", "teams_service_url_denied",
				"direction", "outbound",
				"error", err,
			)
		}
		return nil, err
	}

	token, err := s.tokenProvider.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := s.httpClient
	if client.CheckRedirect == nil {
		cp := *client
		cp.CheckRedirect = refuseRedirect
		client = &cp
	}

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, ErrRedirectNotAllowed) {
			s.log.Warn("teams service URL redirect refused",
				"event", "teams_service_url_denied",
				"direction", "outbound",
				"reason", "redirect_not_allowed",
			)
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// parseRetryAfter extracts the Retry-After duration from a 429 response.
func parseRetryAfter(resp *http.Response) *RetryAfterError {
	retryAfter := 1 * time.Second // Default fallback.

	if v := resp.Header.Get("Retry-After"); v != "" {
		if seconds, err := strconv.Atoi(v); err == nil {
			retryAfter = time.Duration(seconds) * time.Second
		}
	}

	return &RetryAfterError{RetryAfter: retryAfter}
}

// parseErrorResponse reads and formats an error response from the API.
func (s *Sender) parseErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("bot connector API error %d: %s", resp.StatusCode, string(body))
}

// buildAPIURL constructs the Bot Connector REST API URL.
// Path segments are percent-encoded to prevent injection.
func buildAPIURL(serviceURL, conversationID, activityID string) string {
	// Ensure serviceURL doesn't end with a slash.
	serviceURL = strings.TrimRight(serviceURL, "/")

	base := fmt.Sprintf("%s/v3/conversations/%s/activities",
		serviceURL, url.PathEscape(conversationID))
	if activityID != "" {
		base = fmt.Sprintf("%s/%s", base, url.PathEscape(activityID))
	}
	return base
}
