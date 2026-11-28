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
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

const (
	// brokerTrajectoryUploadTimeout bounds a single compressed segment PUT
	// when the caller's context does not carry a shorter deadline.
	brokerTrajectoryUploadTimeout = 2 * time.Minute

	brokerHarvestMaxResponseBytes = 1 << 20
)

// ErrBrokerTrajectoryPrepareDecode is returned when a 200 prepare response
// fails to decode. Fixed text ensures signed upload URLs never reach logs or
// error strings.
var ErrBrokerTrajectoryPrepareDecode = errors.New("broker trajectory prepare response did not decode")

// BrokerHarvestClient provides broker-authenticated Hub endpoints for
// post-exit / pre-delete / pre-reset harvesting of usage outbox events and raw
// trajectory segments. The hub client implements it; callers type-assert a
// Client to it (following BrokerAttachmentClient).
//
// All requests authenticate via the client's configured authenticator (broker
// HMAC via WithHMACAuth) and are sent once without transport retries so the
// harvest engine owns its retry and deadline budget.
type BrokerHarvestClient interface {
	// PostBrokerAgentUsageEvents sends a batch of harvested usage events to
	// POST /api/v1/brokers/{bid}/agents/{id}/usage/events.
	PostBrokerAgentUsageEvents(ctx context.Context, brokerID, agentID string, req *telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error)

	// PrepareBrokerTrajectorySegment prepares a raw trajectory segment upload
	// via POST /api/v1/brokers/{bid}/agents/{id}/trajectory/segments:prepare.
	PrepareBrokerTrajectorySegment(ctx context.Context, brokerID, agentID string, req *telemetrycontract.TrajectorySegmentPrepareRequest) (*telemetrycontract.TrajectorySegmentPrepareResponse, error)

	// UploadBrokerTrajectorySegment PUTs compressed segment bytes to the URL
	// returned by PrepareBrokerTrajectorySegment, sending every header in
	// inst.Headers verbatim.
	//
	// When inst.URL targets this Hub's proxied broker content endpoint (same
	// origin as the client's BaseURL and path under /api/v1/brokers/), broker
	// HMAC authentication is attached via the client's transport. External
	// signed URLs (such as GCS V4 signed PUTs) are sent with a clean HTTP
	// client without Hub credentials or redirects so broker credentials are
	// never leaked off-origin.
	//
	// HTTP 2xx and HTTP 412 Precondition Failed (immutable object already
	// exists) both return a nil error so the caller proceeds to commit.
	UploadBrokerTrajectorySegment(ctx context.Context, inst telemetrycontract.TrajectorySegmentUploadInstruction, compressed []byte) (int, error)

	// CommitBrokerTrajectorySegment commits a prepared raw trajectory segment
	// via POST /api/v1/brokers/{bid}/agents/{id}/trajectory/segments:commit.
	CommitBrokerTrajectorySegment(ctx context.Context, brokerID, agentID string, req *telemetrycontract.TrajectorySegmentCommitRequest) (*telemetrycontract.TrajectorySegmentCommitResponse, error)

	// ReportTrajectoryHarvest records a broker harvest outcome report via
	// POST /api/v1/brokers/{bid}/agents/{id}/trajectory/harvests.
	ReportTrajectoryHarvest(ctx context.Context, brokerID, agentID string, req *telemetrycontract.TrajectoryHarvestReportRequest) (*telemetrycontract.TrajectoryHarvestReportResponse, error)
}

var _ BrokerHarvestClient = (*client)(nil)

// PostBrokerAgentUsageEvents implements BrokerHarvestClient.
func (c *client) PostBrokerAgentUsageEvents(ctx context.Context, brokerID, agentID string, req *telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
	if req == nil {
		return nil, errors.New("usage ingest request is required")
	}
	path := telemetrycontract.UsageIngestBrokerPath(brokerID, agentID)
	resp, err := c.postNoRetry(ctx, path, req, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeRequired[telemetrycontract.UsageIngestResponse](resp)
}

// PrepareBrokerTrajectorySegment implements BrokerHarvestClient.
func (c *client) PrepareBrokerTrajectorySegment(ctx context.Context, brokerID, agentID string, req *telemetrycontract.TrajectorySegmentPrepareRequest) (*telemetrycontract.TrajectorySegmentPrepareResponse, error) {
	if req == nil {
		return nil, errors.New("trajectory prepare request is required")
	}
	path := telemetrycontract.TrajectorySegmentsPrepareBrokerPath(brokerID, agentID)
	resp, err := c.postNoRetry(ctx, path, req, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, apiclient.CheckResponse(resp)
	}
	defer func() { _ = resp.Body.Close() }()
	var out telemetrycontract.TrajectorySegmentPrepareResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, brokerHarvestMaxResponseBytes)).Decode(&out); err != nil {
		return nil, ErrBrokerTrajectoryPrepareDecode
	}
	return &out, nil
}

// CommitBrokerTrajectorySegment implements BrokerHarvestClient.
func (c *client) CommitBrokerTrajectorySegment(ctx context.Context, brokerID, agentID string, req *telemetrycontract.TrajectorySegmentCommitRequest) (*telemetrycontract.TrajectorySegmentCommitResponse, error) {
	if req == nil || strings.TrimSpace(req.SegmentID) == "" {
		return nil, errors.New("segment_id is required")
	}
	path := telemetrycontract.TrajectorySegmentsCommitBrokerPath(brokerID, agentID)
	resp, err := c.postNoRetry(ctx, path, req, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeRequired[telemetrycontract.TrajectorySegmentCommitResponse](resp)
}

// ReportTrajectoryHarvest implements BrokerHarvestClient.
func (c *client) ReportTrajectoryHarvest(ctx context.Context, brokerID, agentID string, req *telemetrycontract.TrajectoryHarvestReportRequest) (*telemetrycontract.TrajectoryHarvestReportResponse, error) {
	if req == nil {
		return nil, errors.New("trajectory harvest report request is required")
	}
	path := telemetrycontract.TrajectoryHarvestsBrokerPath(brokerID, agentID)
	resp, err := c.postNoRetry(ctx, path, req, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeRequired[telemetrycontract.TrajectoryHarvestReportResponse](resp)
}

// UploadBrokerTrajectorySegment implements BrokerHarvestClient.
func (c *client) UploadBrokerTrajectorySegment(ctx context.Context, inst telemetrycontract.TrajectorySegmentUploadInstruction, compressed []byte) (int, error) {
	if strings.TrimSpace(inst.URL) == "" {
		return 0, errors.New("trajectory upload URL is empty")
	}
	method := strings.TrimSpace(inst.Method)
	if method == "" {
		method = http.MethodPut
	}

	reqCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, brokerTrajectoryUploadTimeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(reqCtx, method, inst.URL, bytes.NewReader(compressed))
	if err != nil {
		return 0, fmt.Errorf("create broker trajectory upload request to %s: %w", redactSignedUploadURL(inst.URL), unwrapURLError(err))
	}
	req.ContentLength = int64(len(compressed))
	for k, v := range inst.Headers {
		req.Header.Set(k, v)
	}

	var resp *http.Response
	if c.isHubProxiedBrokerUploadURL(inst.URL) {
		resp, err = c.transport.DoNoRetry(reqCtx, req)
	} else {
		extClient := &http.Client{
			Timeout: brokerTrajectoryUploadTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err = extClient.Do(req)
	}
	if err != nil {
		return 0, fmt.Errorf("send broker trajectory upload request to %s: %w", redactSignedUploadURL(inst.URL), unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if (resp.StatusCode >= 200 && resp.StatusCode < 300) || resp.StatusCode == http.StatusPreconditionFailed {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return resp.StatusCode, nil
	}
	return resp.StatusCode, apiclient.CheckResponse(resp)
}

func (c *client) isHubProxiedBrokerUploadURL(rawURL string) bool {
	hubU, err := url.Parse(c.transport.BaseURL)
	if err != nil || hubU.Scheme == "" || hubU.Host == "" {
		return false
	}
	targetU, err := url.Parse(rawURL)
	if err != nil || targetU.Scheme == "" || targetU.Host == "" {
		return false
	}
	return strings.EqualFold(hubU.Scheme, targetU.Scheme) &&
		strings.EqualFold(hubU.Host, targetU.Host) &&
		strings.HasPrefix(targetU.Path, "/api/v1/brokers/")
}

func redactSignedUploadURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		if idx := strings.IndexAny(rawURL, "?#"); idx >= 0 {
			return rawURL[:idx]
		}
		return "<invalid-url>"
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}

func unwrapURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		return uerr.Err
	}
	return err
}
