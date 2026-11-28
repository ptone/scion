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
	"fmt"
	"net/http"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"github.com/GoogleCloudPlatform/scion/pkg/trajectory/shipper"
)

// brokerUploader adapts HubClient to shipper.Uploader and shipper.Prober for
// a single (agentID, runID) harvest attempt.
type brokerUploader struct {
	hub     HubClient
	agentID string
	runID   string
}

var (
	_ shipper.Uploader = (*brokerUploader)(nil)
	_ shipper.Prober   = (*brokerUploader)(nil)
)

func (u *brokerUploader) buildPrepareRequest(seg shipper.Segment) PrepareSegmentRequest {
	return PrepareSegmentRequest{
		RunID:              u.runID,
		Harness:            seg.Harness,
		NativeRelativePath: seg.NativeRelativePath,
		ArtifactKind:       string(seg.ArtifactKind),
		Generation:         seg.Generation,
		ByteStart:          seg.ByteStart,
		ByteEnd:            seg.ByteEnd,
		RawSHA256:          seg.RawSHA256,
		CompressedSize:     seg.CompressedSize,
		CompressedSHA256:   seg.CompressedSHA256,
		ContentEncoding:    seg.ContentEncoding,
		IncompleteTail:     seg.IncompleteTail,
		SessionID:          seg.SessionID,
		OriginHubID:        seg.OriginHubID,
	}
}

func (u *brokerUploader) Probe(ctx context.Context, seg shipper.Segment) (shipper.ShipResult, error) {
	if u == nil || u.hub == nil {
		err := errors.New("broker harvest uploader: hub client not configured")
		return shipper.ShipResult{Outcome: shipper.OutcomeRetryable, Message: err.Error()}, err
	}
	prepResp, err := u.hub.PrepareTrajectorySegment(ctx, u.agentID, u.buildPrepareRequest(seg))
	if err != nil {
		return mapBrokerShipError(err)
	}
	return shipper.ShipResult{
		Outcome:   shipper.OutcomeCommitted,
		SegmentID: prepResp.SegmentID,
	}, nil
}

func (u *brokerUploader) Ship(ctx context.Context, seg shipper.Segment) (shipper.ShipResult, error) {
	if u == nil || u.hub == nil {
		err := errors.New("broker harvest uploader: hub client not configured")
		return shipper.ShipResult{Outcome: shipper.OutcomeRetryable, Message: err.Error()}, err
	}

	prepResp, err := u.hub.PrepareTrajectorySegment(ctx, u.agentID, u.buildPrepareRequest(seg))
	if err != nil {
		return mapBrokerShipError(err)
	}

	if prepResp.State == telemetrycontract.TrajectorySegmentStateCommitted {
		return shipper.ShipResult{
			Outcome:   shipper.OutcomeCommitted,
			SegmentID: prepResp.SegmentID,
		}, nil
	}

	if prepResp.Upload == nil || strings.TrimSpace(prepResp.Upload.URL) == "" {
		err := errors.New("trajectory prepare returned upload_required without upload instruction")
		return shipper.ShipResult{
			Outcome: shipper.OutcomeRetryable,
			Message: err.Error(),
		}, err
	}

	// Per wire contract §2, every PUT failure other than 412 (handled inside
	// UploadTrajectorySegment) is retryable. Never route storage PUT errors
	// through the prepare/commit outcome table.
	if err := u.hub.UploadTrajectorySegment(ctx, *prepResp.Upload, seg.Compressed); err != nil {
		return shipper.ShipResult{
			Outcome: shipper.OutcomeRetryable,
			Message: err.Error(),
		}, err
	}

	commitResp, err := u.hub.CommitTrajectorySegment(ctx, u.agentID, prepResp.SegmentID)
	if err != nil {
		return mapBrokerShipError(err)
	}
	if commitResp.State != "" && commitResp.State != telemetrycontract.TrajectorySegmentStateCommitted {
		err := fmt.Errorf("trajectory commit returned unexpected state %q", commitResp.State)
		return shipper.ShipResult{
			Outcome: shipper.OutcomeRetryable,
			Message: err.Error(),
		}, err
	}

	return shipper.ShipResult{
		Outcome:   shipper.OutcomeCommitted,
		SegmentID: commitResp.SegmentID,
	}, nil
}

func extractHTTPStatusAndCode(err error) (status int, code, msg string, ok bool) {
	var herr *HTTPError
	if errors.As(err, &herr) {
		return herr.StatusCode, herr.Code, herr.Message, true
	}
	var aerr *apiclient.APIError
	if errors.As(err, &aerr) {
		return aerr.StatusCode, aerr.Code, aerr.Message, true
	}
	return 0, "", "", false
}

func mapBrokerShipError(err error) (shipper.ShipResult, error) {
	status, code, message, ok := extractHTTPStatusAndCode(err)
	if !ok {
		return shipper.ShipResult{
			Outcome: shipper.OutcomeRetryable,
			Message: err.Error(),
		}, err
	}

	msg := code
	if msg == "" {
		msg = message
	}
	if msg == "" {
		msg = err.Error()
	}

	switch status {
	case http.StatusNotFound:
		return shipper.ShipResult{
			Outcome: shipper.OutcomeCaptureUnavailable,
			Message: msg,
		}, nil

	case http.StatusConflict:
		return shipper.ShipResult{
			Outcome: shipper.OutcomeConflict,
			Message: msg,
		}, err

	case http.StatusBadRequest:
		return shipper.ShipResult{
			Outcome: shipper.OutcomeRejected,
			Message: msg,
		}, err

	case http.StatusRequestEntityTooLarge:
		if code == telemetrycontract.TrajectoryErrRunCapExceeded {
			return shipper.ShipResult{
				Outcome: shipper.OutcomeRunCapExceeded,
				Message: msg,
			}, nil
		}
		return shipper.ShipResult{
			Outcome: shipper.OutcomeRejected,
			Message: msg,
		}, err

	case http.StatusForbidden:
		if code == telemetrycontract.TrajectoryErrCaptureDisabled ||
			code == telemetrycontract.TrajectoryErrCaptureMetadataOnly {
			return shipper.ShipResult{
				Outcome: shipper.OutcomePolicyRefused,
				Message: code,
			}, nil
		}
		return shipper.ShipResult{
			Outcome: shipper.OutcomeRetryable,
			Message: msg,
		}, err

	case http.StatusGone, http.StatusUnprocessableEntity:
		return shipper.ShipResult{
			Outcome: shipper.OutcomeRetryable,
			Message: msg,
		}, err

	default:
		return shipper.ShipResult{
			Outcome: shipper.OutcomeRetryable,
			Message: msg,
		}, err
	}
}
