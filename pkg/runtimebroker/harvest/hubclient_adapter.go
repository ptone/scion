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

package harvest

import (
	"context"
	"errors"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

// HubClientAdapter adapts hubclient.BrokerHarvestClient to the small harvest.HubClient
// interface for a single brokerID.
type HubClientAdapter struct {
	client   hubclient.BrokerHarvestClient
	brokerID string
}

var _ HubClient = (*HubClientAdapter)(nil)

// NewHubClientAdapter constructs a HubClient backed by client and brokerID.
func NewHubClientAdapter(client hubclient.BrokerHarvestClient, brokerID string) *HubClientAdapter {
	return &HubClientAdapter{
		client:   client,
		brokerID: brokerID,
	}
}

func (a *HubClientAdapter) PrepareTrajectorySegment(ctx context.Context, agentID string, req PrepareSegmentRequest) (*telemetrycontract.TrajectorySegmentPrepareResponse, error) {
	if a == nil || a.client == nil {
		return nil, errors.New("broker harvest adapter: hub client not configured")
	}
	return a.client.PrepareBrokerTrajectorySegment(ctx, a.brokerID, agentID, &req)
}

func (a *HubClientAdapter) UploadTrajectorySegment(ctx context.Context, inst telemetrycontract.TrajectorySegmentUploadInstruction, compressed []byte) error {
	if a == nil || a.client == nil {
		return errors.New("broker harvest adapter: hub client not configured")
	}
	_, err := a.client.UploadBrokerTrajectorySegment(ctx, inst, compressed)
	return err
}

func (a *HubClientAdapter) CommitTrajectorySegment(ctx context.Context, agentID, segmentID string) (*telemetrycontract.TrajectorySegmentCommitResponse, error) {
	if a == nil || a.client == nil {
		return nil, errors.New("broker harvest adapter: hub client not configured")
	}
	return a.client.CommitBrokerTrajectorySegment(ctx, a.brokerID, agentID, &telemetrycontract.TrajectorySegmentCommitRequest{
		SegmentID: segmentID,
	})
}

func (a *HubClientAdapter) IngestUsageEvents(ctx context.Context, agentID string, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
	if a == nil || a.client == nil {
		return nil, errors.New("broker harvest adapter: hub client not configured")
	}
	return a.client.PostBrokerAgentUsageEvents(ctx, a.brokerID, agentID, &req)
}

func (a *HubClientAdapter) ReportTrajectoryHarvest(ctx context.Context, agentID string, report Report) error {
	if a == nil || a.client == nil {
		return errors.New("broker harvest adapter: hub client not configured")
	}
	_, err := a.client.ReportTrajectoryHarvest(ctx, a.brokerID, agentID, &report)
	return err
}
