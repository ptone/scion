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

package hub

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

const (
	// trajectoryArtifactUploadSigDomain and V2 domain-separate L1 proxied upload
	// HMAC capability URLs from L0 segment uploads and skill downloads.
	trajectoryArtifactUploadSigDomain   = "trajectory-artifact-upload"
	trajectoryArtifactUploadSigDomainV2 = "trajectory-artifact-upload-v2"

	trajectoryArtifactChunkIDMaxLen       = 128
	trajectoryArtifactParserVersionMaxLen = 128
	trajectoryArtifactFirstErrorMaxLen    = 2048

	// Bounds for decompressing pending L1 objects during commit stamping so a
	// crafted gzip bomb cannot exhaust Hub memory.
	trajectoryUsageRecordsMaxDecompressedBytes = 128 * 1024 * 1024
	trajectoryATIFMaxDecompressedBytes         = 256 * 1024 * 1024
	trajectoryUsageRecordMaxLineBytes          = 1 * 1024 * 1024
)

// forbiddenUsageRecordKeys are prompt/tool payload fields that must never
// appear in Tier A PII-free usage_records rows.
var forbiddenUsageRecordKeys = map[string]struct{}{
	"prompt":         {},
	"prompts":        {},
	"message":        {},
	"messages":       {},
	"content":        {},
	"tool_calls":     {},
	"tool_results":   {},
	"steps":          {},
	"thinking":       {},
	"reasoning_text": {},
}

// usageRecordClientDimensionKeys are attribution/dimension fields owned by the
// Hub and stripped before stamping from run_attribution so client-supplied
// values can never survive into Tier A analytics.
var usageRecordClientDimensionKeys = []string{
	"hub_id",
	"origin_hub_id",
	"project_id",
	"billing_user_id",
	"agent_id",
	"agent_slug",
	"parent_agent_id",
	"run_id",
	"launch_id",
	"harness",
	"parser_version",
	"harness_config",
	"harness_config_digest",
	"template",
	"template_digest",
	"labels",
	"release_seq",
	"chunk_id",
	"row_ordinal",
}

func (s *Server) maxTrajectoryArtifactBytes(artifactType string) int64 {
	if artifactType == telemetrycontract.TrajectoryArtifactTypeATIF {
		return telemetrycontract.DefaultTrajectoryArtifactATIFMaxBytes
	}
	return telemetrycontract.DefaultTrajectoryArtifactMaxBytes
}

func trajectoryArtifactContentType(contentEncoding string) string {
	if contentEncoding == telemetrycontract.TrajectoryContentEncodingGzip {
		return trajectoryContentTypeGzip
	}
	return trajectoryContentTypeJSON
}

func computeTrajectoryArtifactUploadHMAC(key []byte, agentID, artifactID string, size int64, sha256Hex string, exp int64) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(trajectoryArtifactUploadSigDomain + "\n" + agentID + "\n" + artifactID + "\n" +
		strconv.FormatInt(size, 10) + "\n" + sha256Hex + "\n" + strconv.FormatInt(exp, 10)))
	return mac.Sum(nil)
}

func computeTrajectoryArtifactUploadHMACv2(key []byte, kid, agentID, artifactID string, size int64, sha256Hex string, exp int64) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(trajectoryArtifactUploadSigDomainV2 + "\n" + kid + "\n" + agentID + "\n" + artifactID + "\n" +
		strconv.FormatInt(size, 10) + "\n" + sha256Hex + "\n" + strconv.FormatInt(exp, 10)))
	return mac.Sum(nil)
}

func (s *Server) signTrajectoryArtifactUploadURL(agentID, artifactID string, size int64, sha256Hex string, expiry time.Time) (string, error) {
	if !s.downloadKeys.configured() {
		return "", errors.New("capability signing key is not configured")
	}
	key, versioned, err := s.downloadKeys.issue()
	if err != nil {
		return "", fmt.Errorf("issue capability signing key: %w", err)
	}
	exp := expiry.Unix()
	expPart := skillFileExpParam + "=" + strconv.FormatInt(exp, 10) + "&"
	if versioned {
		sig := base64.RawURLEncoding.EncodeToString(
			computeTrajectoryArtifactUploadHMACv2(key.Bytes(), key.ID, agentID, artifactID, size, sha256Hex, exp))
		return skillFileVersionParam + "=" + skillFileVersion2 + "&" +
			skillFileKeyIDParam + "=" + url.QueryEscape(key.ID) + "&" +
			expPart + skillFileSigParam + "=" + url.QueryEscape(sig), nil
	}
	sig := base64.RawURLEncoding.EncodeToString(
		computeTrajectoryArtifactUploadHMAC(key.Bytes(), agentID, artifactID, size, sha256Hex, exp))
	return expPart + skillFileSigParam + "=" + url.QueryEscape(sig), nil
}

func (s *Server) verifyTrajectoryArtifactUploadSignature(r *http.Request, agentID, artifactID string, size int64, sha256Hex string, now time.Time) bool {
	if !s.downloadKeys.configured() {
		return false
	}
	q := r.URL.Query()
	if len(q[skillFileSigParam]) != 1 || len(q[skillFileExpParam]) != 1 {
		return false
	}
	kv, hasKV := q[skillFileVersionParam]
	kids, hasKid := q[skillFileKeyIDParam]
	kid := ""
	switch {
	case !hasKV && !hasKid:
	case len(kv) == 1 && len(kids) == 1 && kv[0] == skillFileVersion2 && capabilityKeyIDPattern.MatchString(kids[0]):
		kid = kids[0]
	default:
		return false
	}
	expStr := q.Get(skillFileExpParam)
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || strconv.FormatInt(exp, 10) != expStr {
		return false
	}
	nowUnix := now.Unix()
	if nowUnix > exp {
		return false
	}
	maxTTL := s.trajectoryUploadURLTTLDuration() + trajectoryUploadURLMaxClockSkew
	if exp > nowUnix+int64(maxTTL/time.Second) {
		return false
	}
	got, err := base64.RawURLEncoding.Strict().DecodeString(q.Get(skillFileSigParam))
	if err != nil {
		return false
	}
	for _, k := range s.downloadKeys.candidates(kid) {
		var want []byte
		if kid == "" {
			want = computeTrajectoryArtifactUploadHMAC(k.Bytes(), agentID, artifactID, size, sha256Hex, exp)
		} else {
			want = computeTrajectoryArtifactUploadHMACv2(k.Bytes(), kid, agentID, artifactID, size, sha256Hex, exp)
		}
		if hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

// handleAgentTrajectoryArtifactsPrepare serves
// POST /api/v1/agents/{id}/trajectory/artifacts:prepare.
func (s *Server) handleAgentTrajectoryArtifactsPrepare(w http.ResponseWriter, r *http.Request, id string) {
	if !s.experimentEnabled(experiments.TokenomicsTrajectoryCapture) {
		NotFound(w, "route")
		return
	}
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, http.MethodPost)
		return
	}

	ctx := r.Context()
	agentIdent := GetAgentIdentityFromContext(ctx)
	if agentIdent == nil {
		Unauthorized(w)
		return
	}
	if agentIdent.ID() != id {
		writeError(w, http.StatusForbidden, ErrCodeForbidden,
			"Agents can only prepare their own trajectory artifacts", nil)
		return
	}
	if s.agentStandingForbidden(ctx, w, id) {
		return
	}

	agent, err := s.store.GetAgent(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if agent == nil {
		NotFound(w, "Agent")
		return
	}

	project, err := s.store.GetProject(ctx, agent.ProjectID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeErrorFromErr(w, err, "")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, trajectorySegmentRequestMaxBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			"Failed to read request body", nil)
		return
	}
	if len(body) > trajectorySegmentRequestMaxBody {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			"Request body exceeds maximum size", nil)
		return
	}

	var req telemetrycontract.TrajectoryArtifactPrepareRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			"Invalid JSON request body", nil)
		return
	}

	// Enforce capture policy matrix before deeper validation (except ensuring
	// artifact_type is known when distinguishing metadata_only refusal of atif).
	policy := s.effectiveTrajectoryCapturePolicy(ctx, project)
	switch policy {
	case telemetrycontract.TrajectoryCapturePolicyOff:
		writeError(w, http.StatusForbidden, telemetrycontract.TrajectoryErrCaptureDisabled,
			"Trajectory capture is disabled by policy", nil)
		return
	case telemetrycontract.TrajectoryCapturePolicyMetadataOnly:
		if req.ArtifactType == telemetrycontract.TrajectoryArtifactTypeATIF {
			writeError(w, http.StatusForbidden, telemetrycontract.TrajectoryErrCaptureMetadataOnly,
				"ATIF trajectory capture is disabled under metadata_only policy", nil)
			return
		}
	case telemetrycontract.TrajectoryCapturePolicyFull:
		// All L1 artifact types allowed.
	default:
		writeError(w, http.StatusForbidden, telemetrycontract.TrajectoryErrCaptureDisabled,
			"Trajectory capture is disabled by policy", nil)
		return
	}

	if err := validateTrajectoryArtifactPrepareRequest(&req); err != nil {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact, err.Error(), nil)
		return
	}

	maxSize := s.maxTrajectoryArtifactBytes(req.ArtifactType)
	if req.Size > maxSize {
		writeError(w, http.StatusRequestEntityTooLarge, telemetrycontract.TrajectoryErrArtifactTooLarge,
			fmt.Sprintf("artifact size %d exceeds maximum %d bytes", req.Size, maxSize), nil)
		return
	}

	attr, err := s.resolveTrajectoryRunAttribution(ctx, agent, presentedAgentTokenRunID(ctx))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
				"unable to resolve run attribution for agent", nil)
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Hub storage is not configured")
		return
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	hubID := s.usageLedgerHubID()
	identityKey := store.TrajectoryArtifactIdentityKey(
		req.ArtifactType, req.ChunkID, req.InventorySeq, req.NativeRelativePath, req.ParsedOffset,
	)

	// Check existing artifact first so idempotent replays of already-committed
	// artifacts succeed even if the run later hit the cap.
	existing, err := s.store.GetTrajectoryArtifactByIdentity(ctx, store.TrajectoryArtifactIdentity{
		RunID:         attr.RunID,
		ArtifactType:  req.ArtifactType,
		ParserVersion: req.ParserVersion,
		IdentityKey:   identityKey,
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeErrorFromErr(w, err, "")
		return
	}
	if existing != nil && existing.Status == telemetrycontract.TrajectoryArtifactStatusCommitted {
		if existing.SHA256 == req.SHA256 {
			writeJSON(w, http.StatusOK, telemetrycontract.TrajectoryArtifactPrepareResponse{
				ArtifactID: existing.ID,
				State:      telemetrycontract.TrajectoryArtifactStateCommitted,
				ObjectPath: existing.ObjectPath,
			})
			return
		}
		writeError(w, http.StatusConflict, telemetrycontract.TrajectoryErrArtifactConflict,
			"artifact identity is already committed with different content", nil)
		return
	}

	if existing != nil && existing.Status == telemetrycontract.TrajectoryArtifactStatusPending {
		contentChanged := existing.SHA256 != req.SHA256 ||
			existing.Size != req.Size ||
			existing.RowCount != req.RowCount ||
			existing.Final != req.Final
		if contentChanged {
			exists, existsErr := stor.Exists(ctx, existing.PendingObjectPath)
			if existsErr != nil {
				slog.ErrorContext(ctx, "trajectory artifact prepare: failed to check existing pending object",
					"agent_id", id, "artifact_id", existing.ID, "error", existsErr)
				RuntimeError(w, "Failed to verify existing trajectory artifact object")
				return
			}
			if exists {
				writeError(w, http.StatusConflict, telemetrycontract.TrajectoryErrArtifactConflict,
					"pending artifact object already exists; cannot change content", nil)
				return
			}
		}
	}

	excludeID := ""
	chargedBytes := req.Size
	if existing != nil {
		excludeID = existing.ID
		chargedBytes = max(chargedBytes, existing.ChargedBytes, existing.Size)
	}

	// L1 bytes count toward the existing per-run 2 GiB charge alongside L0 segments.
	usedRawBytes, err := s.store.SumTrajectoryRunRawBytes(ctx, attr.AgentID, attr.RunID, "")
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	usedArtBytes, err := s.store.SumTrajectoryRunArtifactBytes(ctx, attr.AgentID, attr.RunID, excludeID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if usedRawBytes+usedArtBytes+chargedBytes > s.maxTrajectoryRunRawBytes() {
		writeError(w, http.StatusRequestEntityTooLarge, telemetrycontract.TrajectoryErrRunCapExceeded,
			fmt.Sprintf("run trajectory cap of %d bytes exceeded", s.maxTrajectoryRunRawBytes()), nil)
		return
	}
	usedSegs, err := s.store.CountTrajectoryRunSegments(ctx, attr.AgentID, attr.RunID, "")
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	usedArts, err := s.store.CountTrajectoryRunArtifacts(ctx, attr.AgentID, attr.RunID, excludeID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if usedSegs+usedArts+1 > s.maxTrajectoryRunSegments() {
		writeError(w, http.StatusRequestEntityTooLarge, telemetrycontract.TrajectoryErrRunCapExceeded,
			fmt.Sprintf("run trajectory object count cap of %d exceeded", s.maxTrajectoryRunSegments()), nil)
		return
	}

	artifactID := api.NewUUID()
	pendingPath := telemetrycontract.TrajectoryArtifactPendingObjectPath(hubID, artifactID)
	finalPath := buildTrajectoryArtifactFinalPath(hubID, now, attr, &req)

	art, err := s.store.PrepareTrajectoryArtifact(ctx, &store.TrajectoryArtifact{
		ID:                 artifactID,
		HubID:              hubID,
		ProjectID:          attr.ProjectID,
		AgentID:            attr.AgentID,
		RunID:              attr.RunID,
		Harness:            req.Harness,
		ArtifactType:       req.ArtifactType,
		ParserVersion:      req.ParserVersion,
		IdentityKey:        identityKey,
		ChunkID:            req.ChunkID,
		InventorySeq:       req.InventorySeq,
		Final:              req.Final,
		SessionID:          req.SessionID,
		NativeRelativePath: req.NativeRelativePath,
		ParsedOffset:       req.ParsedOffset,
		RowCount:           req.RowCount,
		ATIFSchemaVersion:  req.ATIFSchemaVersion,
		FileFormat:         req.FileFormat,
		ContentEncoding:    req.ContentEncoding,
		Size:               req.Size,
		ChargedBytes:       chargedBytes,
		SHA256:             req.SHA256,
		PendingObjectPath:  pendingPath,
		ObjectPath:         finalPath,
		Status:             telemetrycontract.TrajectoryArtifactStatusPending,
		CreatedAt:          now,
		PrepareExpiresAt:   now.Add(s.trajectoryPrepareTTLDuration()),
	})
	if err != nil {
		if errors.Is(err, store.ErrTrajectoryArtifactConflict) {
			writeError(w, http.StatusConflict, telemetrycontract.TrajectoryErrArtifactConflict,
				"artifact identity is already committed with different content", nil)
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	if art.Status == telemetrycontract.TrajectoryArtifactStatusCommitted {
		writeJSON(w, http.StatusOK, telemetrycontract.TrajectoryArtifactPrepareResponse{
			ArtifactID: art.ID,
			State:      telemetrycontract.TrajectoryArtifactStateCommitted,
			ObjectPath: art.ObjectPath,
		})
		return
	}

	uploadInst, err := s.buildTrajectoryArtifactUploadInstruction(ctx, r, stor, art, now)
	if err != nil {
		slog.ErrorContext(ctx, "trajectory artifact prepare: failed to issue upload URL",
			"agent_id", art.AgentID, "artifact_id", art.ID, "provider", stor.Provider(), "error", err)
		RuntimeError(w, "Failed to issue trajectory artifact upload URL")
		return
	}

	writeJSON(w, http.StatusOK, telemetrycontract.TrajectoryArtifactPrepareResponse{
		ArtifactID: art.ID,
		State:      telemetrycontract.TrajectoryArtifactStateUploadRequired,
		ObjectPath: art.ObjectPath,
		Upload:     uploadInst,
	})
}

func isSupportedTrajectoryArtifactHarness(harness string) bool {
	return harness == "claude"
}

func isSafeTrajectoryChunkID(chunkID string) bool {
	if chunkID == "" || len(chunkID) > trajectoryArtifactChunkIDMaxLen {
		return false
	}
	if chunkID == "." || chunkID == ".." || strings.Contains(chunkID, "..") {
		return false
	}
	for i := 0; i < len(chunkID); i++ {
		c := chunkID[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}

func validateTrajectoryArtifactPrepareRequest(req *telemetrycontract.TrajectoryArtifactPrepareRequest) error {
	if !telemetrycontract.IsValidTrajectoryArtifactType(req.ArtifactType) {
		return fmt.Errorf("unsupported artifact_type %q", req.ArtifactType)
	}
	if !isSupportedTrajectoryArtifactHarness(req.Harness) {
		return fmt.Errorf("unsupported harness %q", req.Harness)
	}
	if len(req.ParserVersion) > trajectoryArtifactParserVersionMaxLen || !telemetrycontract.IsValidTrajectoryParserVersion(req.ParserVersion) {
		return fmt.Errorf("parser_version must match ^[a-z0-9-]+-[0-9]+$")
	}
	if !telemetrycontract.IsValidTrajectoryContentEncoding(req.ContentEncoding) {
		return fmt.Errorf("unsupported content_encoding %q", req.ContentEncoding)
	}
	if req.Size <= 0 {
		return fmt.Errorf("size must be positive")
	}
	if !isValidHexSHA256(req.SHA256) {
		return fmt.Errorf("sha256 must be a 64-character lowercase hex digest")
	}

	switch req.ArtifactType {
	case telemetrycontract.TrajectoryArtifactTypeUsageRecords:
		if req.ContentEncoding != telemetrycontract.TrajectoryContentEncodingGzip {
			return fmt.Errorf("usage_records requires content_encoding %q", telemetrycontract.TrajectoryContentEncodingGzip)
		}
		if !isSafeTrajectoryChunkID(req.ChunkID) {
			return fmt.Errorf("chunk_id is required and must be a safe identifier")
		}
		if req.InventorySeq < 1 {
			return fmt.Errorf("inventory_seq must be >= 1 for usage_records")
		}
		if req.RowCount < 0 {
			return fmt.Errorf("row_count must be non-negative")
		}
	case telemetrycontract.TrajectoryArtifactTypeRunInventory:
		if req.ContentEncoding != telemetrycontract.TrajectoryContentEncodingIdentity {
			return fmt.Errorf("run_inventory requires content_encoding %q", telemetrycontract.TrajectoryContentEncodingIdentity)
		}
		if req.InventorySeq < 1 {
			return fmt.Errorf("inventory_seq must be >= 1 for run_inventory")
		}
	case telemetrycontract.TrajectoryArtifactTypeATIF:
		if err := telemetrycontract.ValidateTrajectoryRelativePath(req.Harness, req.NativeRelativePath); err != nil {
			return err
		}
		if req.SessionID == "" || len(req.SessionID) > trajectoryMetadataFieldMaxLen || !isPrintableTrajectoryField(req.SessionID) {
			return fmt.Errorf("session_id is required and must be printable UTF-8 <= %d bytes", trajectoryMetadataFieldMaxLen)
		}
		if req.ParsedOffset < 0 {
			return fmt.Errorf("parsed_offset must be non-negative")
		}
		if req.ATIFSchemaVersion != telemetrycontract.TrajectoryATIFSchemaVersionV18 {
			return fmt.Errorf("unsupported atif_schema_version %q", req.ATIFSchemaVersion)
		}
		if req.FileFormat != telemetrycontract.TrajectoryFileFormatJSON {
			return fmt.Errorf("unsupported file_format %q for %s", req.FileFormat, req.ATIFSchemaVersion)
		}
	}
	return nil
}

func buildTrajectoryArtifactFinalPath(hubID string, prepareDate time.Time, attr *store.RunAttribution, req *telemetrycontract.TrajectoryArtifactPrepareRequest) string {
	switch req.ArtifactType {
	case telemetrycontract.TrajectoryArtifactTypeUsageRecords:
		return telemetrycontract.TrajectoryUsageRecordsObjectPath(
			hubID, prepareDate, attr.ProjectID, attr.AgentID, attr.RunID,
			req.ParserVersion, req.InventorySeq, req.ChunkID,
		)
	case telemetrycontract.TrajectoryArtifactTypeRunInventory:
		return telemetrycontract.TrajectoryRunInventoryObjectPath(
			hubID, prepareDate, attr.ProjectID, attr.AgentID, attr.RunID,
			req.ParserVersion, req.InventorySeq,
		)
	case telemetrycontract.TrajectoryArtifactTypeATIF:
		sessionKey := telemetrycontract.TrajectorySessionKey(req.NativeRelativePath)
		return telemetrycontract.TrajectoryATIFObjectPath(
			hubID, attr.ProjectID, attr.AgentID, attr.RunID,
			sessionKey, req.ParserVersion, req.ParsedOffset, req.FileFormat, req.ContentEncoding,
		)
	default:
		return ""
	}
}

func (s *Server) buildTrajectoryArtifactUploadInstruction(
	ctx context.Context,
	r *http.Request,
	stor storage.Storage,
	art *store.TrajectoryArtifact,
	now time.Time,
) (*telemetrycontract.TrajectorySegmentUploadInstruction, error) {
	ttl := s.trajectoryUploadURLTTLDuration()
	expiresAt := now.Add(ttl)
	contentType := trajectoryArtifactContentType(art.ContentEncoding)

	if stor.Provider() == storage.ProviderGCS {
		zero := int64(0)
		signed, err := stor.GenerateSignedURL(ctx, art.PendingObjectPath, storage.SignedURLOptions{
			Method:             http.MethodPut,
			Expires:            ttl,
			ContentType:        contentType,
			ContentLengthRange: &storage.ContentLengthRange{Min: art.Size, Max: art.Size},
			IfGenerationMatch:  &zero,
		})
		if err == nil && signed != nil && signed.URL != "" {
			headers := make(map[string]string, len(signed.Headers)+1)
			for k, v := range signed.Headers {
				headers[k] = v
			}
			if _, ok := headers["Content-Type"]; !ok {
				headers["Content-Type"] = contentType
			}
			exp := signed.Expires
			if exp.IsZero() {
				exp = expiresAt
			}
			return &telemetrycontract.TrajectorySegmentUploadInstruction{
				Method:    http.MethodPut,
				URL:       signed.URL,
				Headers:   headers,
				ExpiresAt: exp.UTC().Format(time.RFC3339),
			}, nil
		}
		slog.WarnContext(ctx, "trajectory artifact prepare: GCS signed URL unavailable; falling back to Hub-proxied PUT",
			"agent_id", art.AgentID, "artifact_id", art.ID, "error", err)
	}

	sigQuery, err := s.signTrajectoryArtifactUploadURL(art.AgentID, art.ID, art.Size, art.SHA256, expiresAt)
	if err != nil {
		return nil, err
	}
	uploadURL := s.trajectoryProxyBaseURL(r) + telemetrycontract.TrajectoryArtifactContentAgentPath(art.AgentID, art.ID) + "?" + sigQuery
	return &telemetrycontract.TrajectorySegmentUploadInstruction{
		Method: http.MethodPut,
		URL:    uploadURL,
		Headers: map[string]string{
			"Content-Type": contentType,
		},
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// handleAgentTrajectoryArtifactContent serves
// PUT /api/v1/agents/{id}/trajectory/artifacts/{artifact_id}/content.
func (s *Server) handleAgentTrajectoryArtifactContent(w http.ResponseWriter, r *http.Request, id, artifactID string) {
	if !s.experimentEnabled(experiments.TokenomicsTrajectoryCapture) {
		NotFound(w, "route")
		return
	}
	if r.Method != http.MethodPut {
		MethodNotAllowed(w, http.MethodPut)
		return
	}

	ctx := r.Context()
	agentIdent := GetAgentIdentityFromContext(ctx)
	if agentIdent == nil {
		Unauthorized(w)
		return
	}
	if agentIdent.ID() != id {
		writeError(w, http.StatusForbidden, ErrCodeForbidden,
			"Agents can only upload their own trajectory artifacts", nil)
		return
	}

	art, err := s.store.GetTrajectoryArtifact(ctx, artifactID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			NotFound(w, "Trajectory artifact")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}
	if art.AgentID != id {
		NotFound(w, "Trajectory artifact")
		return
	}

	now := time.Now().UTC()
	if !s.verifyTrajectoryArtifactUploadSignature(r, id, art.ID, art.Size, art.SHA256, now) {
		writeError(w, http.StatusForbidden, ErrCodeForbidden,
			"Invalid or expired trajectory artifact upload signature", nil)
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Hub storage is not configured")
		return
	}

	vr := newTrajectoryVerifyingReader(r.Body, art.Size, art.SHA256)
	zero := int64(0)
	_, err = stor.Upload(ctx, art.PendingObjectPath, vr, storage.UploadOptions{
		ContentType:       trajectoryArtifactContentType(art.ContentEncoding),
		IfGenerationMatch: &zero,
	})
	if err == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	if errors.Is(err, errTrajectoryUploadSizeMismatch) {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			fmt.Sprintf("uploaded size %d does not match expected size %d", vr.bytesRead, art.Size), nil)
		return
	}
	if errors.Is(err, errTrajectoryUploadHashMismatch) {
		writeError(w, http.StatusUnprocessableEntity, telemetrycontract.TrajectoryErrArtifactVerificationFailed,
			"uploaded sha256 does not match prepared artifact", nil)
		return
	}
	if vr.readErr != nil {
		BadRequest(w, "Failed to read upload body")
		return
	}
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		slog.ErrorContext(ctx, "trajectory artifact upload failed",
			"agent_id", id, "artifact_id", art.ID, "error", err)
		RuntimeError(w, "Failed to store trajectory artifact")
		return
	}

	if !vr.verified {
		if _, drainErr := io.Copy(io.Discard, vr); drainErr != nil {
			if errors.Is(drainErr, errTrajectoryUploadSizeMismatch) {
				writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
					fmt.Sprintf("uploaded size %d does not match expected size %d", vr.bytesRead, art.Size), nil)
				return
			}
			if errors.Is(drainErr, errTrajectoryUploadHashMismatch) {
				writeError(w, http.StatusUnprocessableEntity, telemetrycontract.TrajectoryErrArtifactVerificationFailed,
					"uploaded sha256 does not match prepared artifact", nil)
				return
			}
			BadRequest(w, "Failed to read upload body")
			return
		}
	}

	identical, cmpErr := storageObjectMatchesHash(ctx, stor, art.PendingObjectPath, art.Size, art.SHA256)
	if cmpErr != nil {
		slog.ErrorContext(ctx, "trajectory artifact upload: failed to compare existing object",
			"agent_id", id, "artifact_id", art.ID, "error", cmpErr)
		RuntimeError(w, "Failed to verify existing trajectory artifact")
		return
	}
	if identical {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeError(w, http.StatusPreconditionFailed, telemetrycontract.TrajectoryErrArtifactConflict,
		"trajectory artifact pending object already exists with different bytes", nil)
}

// handleAgentTrajectoryArtifactsCommit serves
// POST /api/v1/agents/{id}/trajectory/artifacts:commit.
func (s *Server) handleAgentTrajectoryArtifactsCommit(w http.ResponseWriter, r *http.Request, id string) {
	if !s.experimentEnabled(experiments.TokenomicsTrajectoryCapture) {
		NotFound(w, "route")
		return
	}
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, http.MethodPost)
		return
	}

	ctx := r.Context()
	agentIdent := GetAgentIdentityFromContext(ctx)
	if agentIdent == nil {
		Unauthorized(w)
		return
	}
	if agentIdent.ID() != id {
		writeError(w, http.StatusForbidden, ErrCodeForbidden,
			"Agents can only commit their own trajectory artifacts", nil)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, trajectorySegmentRequestMaxBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			"Failed to read request body", nil)
		return
	}
	if len(body) > trajectorySegmentRequestMaxBody {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			"Request body exceeds maximum size", nil)
		return
	}

	var req telemetrycontract.TrajectoryArtifactCommitRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			"Invalid JSON request body", nil)
		return
	}

	// Standalone parse_failed reporting via session_meta (no uploaded object).
	if strings.TrimSpace(req.ArtifactID) == "" {
		if req.SessionMeta != nil && req.SessionMeta.NormalizeStatus == telemetrycontract.TrajectorySessionNormalizeStatusParseFailed {
			s.handleAgentTrajectoryParseFailedReport(w, r, id, req.SessionMeta)
			return
		}
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			"artifact_id is required (or session_meta with normalize_status=parse_failed)", nil)
		return
	}

	art, err := s.store.GetTrajectoryArtifact(ctx, req.ArtifactID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			NotFound(w, "Trajectory artifact")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}
	if art.AgentID != id {
		NotFound(w, "Trajectory artifact")
		return
	}

	// Idempotent replay of an already-committed artifact.
	if art.Status == telemetrycontract.TrajectoryArtifactStatusCommitted {
		writeJSON(w, http.StatusOK, telemetrycontract.TrajectoryArtifactCommitResponse{
			State:         telemetrycontract.TrajectoryArtifactStateCommitted,
			ArtifactID:    art.ID,
			ObjectPath:    art.ObjectPath,
			StampedSHA256: art.StampedSHA256,
			StampedSize:   art.StampedSize,
		})
		return
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if now.After(art.PrepareExpiresAt) {
		writeError(w, http.StatusGone, telemetrycontract.TrajectoryErrArtifactExpired,
			"prepared artifact has expired; prepare again", nil)
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Hub storage is not configured")
		return
	}

	pendingBytes, err := readAndVerifyPendingArtifact(ctx, stor, art)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, telemetrycontract.TrajectoryErrArtifactVerificationFailed,
			err.Error(), nil)
		return
	}

	agent, err := s.store.GetAgent(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	attr, err := s.resolveTrajectoryRunAttribution(ctx, agent, art.RunID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	var (
		stampedBytes []byte
		stepCount    int
		contentType  = trajectoryArtifactContentType(art.ContentEncoding)
	)
	switch art.ArtifactType {
	case telemetrycontract.TrajectoryArtifactTypeUsageRecords:
		stampedBytes, err = stampUsageRecordsArtifact(pendingBytes, art, agent, attr)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, telemetrycontract.TrajectoryErrArtifactVerificationFailed,
				err.Error(), nil)
			return
		}
	case telemetrycontract.TrajectoryArtifactTypeRunInventory:
		var incompleteErr *inventoryIncompleteError
		stampedBytes, err = s.verifyAndRewriteRunInventory(ctx, pendingBytes, art)
		if err != nil {
			if errors.As(err, &incompleteErr) {
				writeError(w, http.StatusUnprocessableEntity, telemetrycontract.TrajectoryErrInventoryIncomplete,
					err.Error(), nil)
				return
			}
			writeError(w, http.StatusUnprocessableEntity, telemetrycontract.TrajectoryErrArtifactVerificationFailed,
				err.Error(), nil)
			return
		}
	case telemetrycontract.TrajectoryArtifactTypeATIF:
		stampedBytes, stepCount, err = stampATIFArtifact(pendingBytes, art, attr)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, telemetrycontract.TrajectoryErrArtifactVerificationFailed,
				err.Error(), nil)
			return
		}
	default:
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			fmt.Sprintf("unsupported artifact_type %q", art.ArtifactType), nil)
		return
	}

	stampedSum := sha256.Sum256(stampedBytes)
	stampedSHA256 := hex.EncodeToString(stampedSum[:])
	stampedSize := int64(len(stampedBytes))

	zero := int64(0)
	_, err = stor.Upload(ctx, art.ObjectPath, bytes.NewReader(stampedBytes), storage.UploadOptions{
		ContentType:       contentType,
		IfGenerationMatch: &zero,
	})
	if errors.Is(err, storage.ErrPreconditionFailed) {
		// Retry after a prior commit wrote the final object but failed before
		// committing the DB row: inspect the existing final object's size/hash.
		existSize, existSHA, readErr := hashStorageObject(ctx, stor, art.ObjectPath, s.maxTrajectoryArtifactBytes(art.ArtifactType)+1024*1024)
		if readErr != nil {
			slog.ErrorContext(ctx, "trajectory artifact commit: failed to inspect existing final object",
				"agent_id", id, "artifact_id", art.ID, "error", readErr)
			RuntimeError(w, "Failed to verify existing trajectory artifact object")
			return
		}
		stampedSize = existSize
		stampedSHA256 = existSHA
	} else if err != nil {
		slog.ErrorContext(ctx, "trajectory artifact commit: failed to write final object",
			"agent_id", id, "artifact_id", art.ID, "error", err)
		RuntimeError(w, "Failed to write final trajectory artifact object")
		return
	}

	// Best-effort cleanup of the pending staging object now that the final
	// object is durable.
	if delErr := stor.Delete(ctx, art.PendingObjectPath); delErr != nil && !errors.Is(delErr, storage.ErrNotFound) {
		slog.WarnContext(ctx, "trajectory artifact commit: failed to delete pending object",
			"agent_id", id, "artifact_id", art.ID, "pending_path", art.PendingObjectPath, "error", delErr)
	}

	committed, err := s.store.CommitTrajectoryArtifact(ctx, art.ID, stampedSize, stampedSHA256, now, store.TrajectoryArtifactCommitExpectation{
		Size:     art.Size,
		SHA256:   art.SHA256,
		RowCount: art.RowCount,
		Final:    art.Final,
	})
	if err != nil {
		if errors.Is(err, store.ErrTrajectoryArtifactConflict) {
			writeError(w, http.StatusConflict, telemetrycontract.TrajectoryErrArtifactConflict,
				"artifact identity is already committed with different content", nil)
			return
		}
		if errors.Is(err, store.ErrTrajectoryArtifactModified) {
			writeError(w, http.StatusUnprocessableEntity, telemetrycontract.TrajectoryErrArtifactVerificationFailed,
				"prepared artifact was modified concurrently before commit; re-verify and retry", nil)
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	if committed.ArtifactType == telemetrycontract.TrajectoryArtifactTypeATIF {
		meta := req.SessionMeta
		sessStepCount := stepCount
		var (
			isSubagent      bool
			parentSessionID string
			parseErrorCount int
			firstError      string
		)
		if meta != nil {
			if meta.StepCount > 0 {
				sessStepCount = meta.StepCount
			}
			isSubagent = meta.IsSubagent
			parentSessionID = meta.ParentSessionID
			parseErrorCount = max(0, meta.ParseErrorCount)
			firstError = truncateTrajectoryFirstError(meta.FirstError)
		}
		_, pvNum, _ := telemetrycontract.ParseTrajectoryParserVersion(committed.ParserVersion)
		if _, _, upErr := s.store.UpsertTrajectorySession(ctx, &store.TrajectorySession{
			HubID:              committed.HubID,
			ProjectID:          committed.ProjectID,
			AgentID:            committed.AgentID,
			RunID:              committed.RunID,
			Harness:            committed.Harness,
			SessionID:          committed.SessionID,
			NativeRelativePath: committed.NativeRelativePath,
			IsSubagent:         isSubagent,
			ParentSessionID:    parentSessionID,
			ParserVersion:      committed.ParserVersion,
			ParserVersionNum:   pvNum,
			ParsedOffset:       committed.ParsedOffset,
			ATIFArtifactID:     committed.ID,
			ATIFObjectPath:     committed.ObjectPath,
			ATIFSchemaVersion:  committed.ATIFSchemaVersion,
			FileFormat:         committed.FileFormat,
			StepCount:          sessStepCount,
			NormalizeStatus:    telemetrycontract.TrajectorySessionNormalizeStatusOK,
			ParseErrorCount:    parseErrorCount,
			FirstError:         firstError,
			UpdatedAt:          now,
		}); upErr != nil {
			slog.ErrorContext(ctx, "trajectory artifact commit: failed to upsert trajectory_sessions",
				"agent_id", id, "artifact_id", committed.ID, "error", upErr)
			writeErrorFromErr(w, upErr, "")
			return
		}

		maxOffset := committed.ParsedOffset
		if _, markErr := s.store.MarkTrajectorySegmentsNormalizeStatus(
			ctx, committed.AgentID, committed.RunID, committed.NativeRelativePath,
			&maxOffset, telemetrycontract.TrajectoryNormalizeStatusNormalized,
		); markErr != nil {
			slog.WarnContext(ctx, "trajectory artifact commit: failed to mark L0 segments normalized",
				"agent_id", id, "artifact_id", committed.ID, "error", markErr)
		}
	}

	writeJSON(w, http.StatusOK, telemetrycontract.TrajectoryArtifactCommitResponse{
		State:         telemetrycontract.TrajectoryArtifactStateCommitted,
		ArtifactID:    committed.ID,
		ObjectPath:    committed.ObjectPath,
		StampedSHA256: committed.StampedSHA256,
		StampedSize:   committed.StampedSize,
	})
}

func (s *Server) handleAgentTrajectoryParseFailedReport(w http.ResponseWriter, r *http.Request, id string, meta *telemetrycontract.TrajectorySessionMetaInput) {
	ctx := r.Context()

	agent, err := s.store.GetAgent(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if agent == nil {
		NotFound(w, "Agent")
		return
	}

	project, err := s.store.GetProject(ctx, agent.ProjectID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeErrorFromErr(w, err, "")
		return
	}
	if s.effectiveTrajectoryCapturePolicy(ctx, project) == telemetrycontract.TrajectoryCapturePolicyOff {
		writeError(w, http.StatusForbidden, telemetrycontract.TrajectoryErrCaptureDisabled,
			"Trajectory capture is disabled by policy", nil)
		return
	}

	if !isSupportedTrajectoryArtifactHarness(meta.Harness) {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			fmt.Sprintf("unsupported harness %q", meta.Harness), nil)
		return
	}
	if !telemetrycontract.IsValidTrajectoryParserVersion(meta.ParserVersion) {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			"parser_version must match ^[a-z0-9-]+-[0-9]+$", nil)
		return
	}
	if err := telemetrycontract.ValidateTrajectoryRelativePath(meta.Harness, meta.NativeRelativePath); err != nil {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact, err.Error(), nil)
		return
	}
	if meta.ParsedOffset < 0 {
		writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
			"parsed_offset must be non-negative", nil)
		return
	}

	attr, err := s.resolveTrajectoryRunAttribution(ctx, agent, presentedAgentTokenRunID(ctx))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, telemetrycontract.TrajectoryErrInvalidArtifact,
				"unable to resolve run attribution for agent", nil)
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	_, pvNum, _ := telemetrycontract.ParseTrajectoryParserVersion(meta.ParserVersion)
	if _, _, err := s.store.UpsertTrajectorySession(ctx, &store.TrajectorySession{
		HubID:              s.usageLedgerHubID(),
		ProjectID:          attr.ProjectID,
		AgentID:            attr.AgentID,
		RunID:              attr.RunID,
		Harness:            meta.Harness,
		SessionID:          meta.SessionID,
		NativeRelativePath: meta.NativeRelativePath,
		IsSubagent:         meta.IsSubagent,
		ParentSessionID:    meta.ParentSessionID,
		ParserVersion:      meta.ParserVersion,
		ParserVersionNum:   pvNum,
		ParsedOffset:       meta.ParsedOffset,
		NormalizeStatus:    telemetrycontract.TrajectorySessionNormalizeStatusParseFailed,
		ParseErrorCount:    max(0, meta.ParseErrorCount),
		FirstError:         truncateTrajectoryFirstError(meta.FirstError),
		UpdatedAt:          now,
	}); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	if _, markErr := s.store.MarkTrajectorySegmentsNormalizeStatus(
		ctx, attr.AgentID, attr.RunID, meta.NativeRelativePath,
		nil, telemetrycontract.TrajectoryNormalizeStatusParseFailed,
	); markErr != nil {
		slog.WarnContext(ctx, "trajectory parse_failed report: failed to mark L0 segments parse_failed",
			"agent_id", id, "error", markErr)
	}

	writeJSON(w, http.StatusOK, telemetrycontract.TrajectoryArtifactCommitResponse{
		State: telemetrycontract.TrajectoryArtifactStateCommitted,
	})
}

func truncateTrajectoryFirstError(s string) string {
	if len(s) <= trajectoryArtifactFirstErrorMaxLen {
		return s
	}
	return s[:trajectoryArtifactFirstErrorMaxLen]
}

func readAndVerifyPendingArtifact(ctx context.Context, stor storage.Storage, art *store.TrajectoryArtifact) ([]byte, error) {
	obj, err := stor.GetObject(ctx, art.PendingObjectPath)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("pending artifact object not found")
		}
		return nil, fmt.Errorf("failed to stat pending artifact object: %w", err)
	}
	if obj.Size != art.Size {
		return nil, fmt.Errorf("pending artifact size %d does not match expected %d", obj.Size, art.Size)
	}

	rc, _, err := stor.Download(ctx, art.PendingObjectPath)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("pending artifact object not found")
		}
		return nil, fmt.Errorf("failed to download pending artifact object: %w", err)
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, art.Size+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read pending artifact object: %w", err)
	}
	if int64(len(data)) != art.Size {
		return nil, fmt.Errorf("pending artifact byte length %d does not match expected %d", len(data), art.Size)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != art.SHA256 {
		return nil, fmt.Errorf("pending artifact sha256 mismatch")
	}
	return data, nil
}

func hashStorageObject(ctx context.Context, stor storage.Storage, objectPath string, maxBytes int64) (int64, string, error) {
	rc, _, err := stor.Download(ctx, objectPath)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(rc, maxBytes+1))
	if err != nil {
		return 0, "", err
	}
	if n > maxBytes {
		return 0, "", fmt.Errorf("existing object exceeds size limit")
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func sortedTrajectoryLabels(m map[string]string) []telemetrycontract.TrajectoryLabelKV {
	if len(m) == 0 {
		return []telemetrycontract.TrajectoryLabelKV{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]telemetrycontract.TrajectoryLabelKV, 0, len(keys))
	for _, k := range keys {
		out = append(out, telemetrycontract.TrajectoryLabelKV{Key: k, Value: m[k]})
	}
	return out
}

func agentParentAgentID(agent *store.Agent) string {
	if agent == nil || len(agent.Ancestry) < 2 {
		return ""
	}
	return agent.Ancestry[len(agent.Ancestry)-1]
}

func stampUsageRecordsArtifact(pendingGzip []byte, art *store.TrajectoryArtifact, agent *store.Agent, attr *store.RunAttribution) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(pendingGzip))
	if err != nil {
		return nil, fmt.Errorf("invalid gzip content: %w", err)
	}
	defer func() { _ = gz.Close() }()

	decompressed, err := io.ReadAll(io.LimitReader(gz, trajectoryUsageRecordsMaxDecompressedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to decompress usage_records: %w", err)
	}
	if int64(len(decompressed)) > trajectoryUsageRecordsMaxDecompressedBytes {
		return nil, fmt.Errorf("decompressed usage_records exceeds maximum size")
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("corrupt gzip trailer: %w", err)
	}

	var outBuf bytes.Buffer
	outGZ := gzip.NewWriter(&outBuf)

	labels := sortedTrajectoryLabels(attr.LabelsAtLaunch)
	parentAgentID := agentParentAgentID(agent)
	dedupePrefix := art.Harness + ":"

	scanner := bufio.NewScanner(bytes.NewReader(decompressed))
	scanner.Buffer(make([]byte, 0, 64*1024), trajectoryUsageRecordMaxLineBytes)

	var rowOrdinal int64
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&rec); err != nil || rec == nil {
			return nil, fmt.Errorf("row %d: invalid JSON object", rowOrdinal)
		}
		if err := validateUsageRecordLine(rec, dedupePrefix, rowOrdinal); err != nil {
			return nil, err
		}

		// Strip any client-supplied dimension fields before stamping Hub-authoritative values.
		for _, k := range usageRecordClientDimensionKeys {
			delete(rec, k)
		}
		rec["hub_id"] = art.HubID
		rec["origin_hub_id"] = art.HubID
		rec["project_id"] = attr.ProjectID
		if attr.BillingUserID != nil {
			rec["billing_user_id"] = *attr.BillingUserID
		} else {
			rec["billing_user_id"] = nil
		}
		rec["agent_id"] = attr.AgentID
		rec["agent_slug"] = attr.AgentSlug
		if parentAgentID != "" {
			rec["parent_agent_id"] = parentAgentID
		} else {
			rec["parent_agent_id"] = nil
		}
		rec["run_id"] = attr.RunID
		rec["launch_id"] = attr.LaunchID
		rec["harness"] = art.Harness
		rec["parser_version"] = art.ParserVersion
		rec["harness_config"] = attr.HarnessConfig
		if attr.HarnessConfigDigest != nil {
			rec["harness_config_digest"] = *attr.HarnessConfigDigest
		} else {
			rec["harness_config_digest"] = nil
		}
		rec["template"] = attr.Template
		if attr.TemplateDigest != nil {
			rec["template_digest"] = *attr.TemplateDigest
		} else {
			rec["template_digest"] = nil
		}
		rec["labels"] = labels
		rec["release_seq"] = art.InventorySeq
		rec["chunk_id"] = art.ChunkID
		rec["row_ordinal"] = rowOrdinal

		encoded, err := json.Marshal(rec)
		if err != nil {
			return nil, fmt.Errorf("row %d: marshal stamped record: %w", rowOrdinal, err)
		}
		if _, err := outGZ.Write(encoded); err != nil {
			return nil, fmt.Errorf("row %d: write gzip: %w", rowOrdinal, err)
		}
		if _, err := outGZ.Write([]byte{'\n'}); err != nil {
			return nil, fmt.Errorf("row %d: write newline: %w", rowOrdinal, err)
		}
		rowOrdinal++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan usage_records: %w", err)
	}
	if rowOrdinal != art.RowCount {
		return nil, fmt.Errorf("usage_records line count %d does not match declared row_count %d", rowOrdinal, art.RowCount)
	}
	if err := outGZ.Close(); err != nil {
		return nil, fmt.Errorf("close gzip writer: %w", err)
	}
	return outBuf.Bytes(), nil
}

func validateUsageRecordLine(rec map[string]any, dedupePrefix string, rowOrdinal int64) error {
	for k := range rec {
		if _, forbidden := forbiddenUsageRecordKeys[k]; forbidden {
			return fmt.Errorf("row %d: forbidden payload field %q in usage_records", rowOrdinal, k)
		}
	}
	dedupeKey, _ := rec["dedupe_key"].(string)
	if dedupeKey == "" || !strings.HasPrefix(dedupeKey, dedupePrefix) {
		return fmt.Errorf("row %d: dedupe_key must start with %q", rowOrdinal, dedupePrefix)
	}
	eventTS, _ := rec["event_ts"].(string)
	if eventTS == "" {
		return fmt.Errorf("row %d: event_ts is required", rowOrdinal)
	}
	if _, err := time.Parse(time.RFC3339Nano, eventTS); err != nil {
		return fmt.Errorf("row %d: event_ts must be RFC3339", rowOrdinal)
	}
	status, _ := rec["status"].(string)
	if status != "ok" && status != "error" {
		return fmt.Errorf("row %d: status must be \"ok\" or \"error\"", rowOrdinal)
	}
	calls, ok := jsonNonNegInt(rec["calls"])
	if !ok || calls < 1 {
		return fmt.Errorf("row %d: calls must be an integer >= 1", rowOrdinal)
	}
	for _, field := range []string{"input", "output", "cache_read", "cache_write", "cache_write_1h", "reasoning"} {
		val, exists := rec[field]
		if !exists || val == nil {
			continue
		}
		if _, valid := jsonNonNegInt(val); !valid {
			return fmt.Errorf("row %d: %s must be a non-negative integer or null", rowOrdinal, field)
		}
	}
	return nil
}

func jsonNonNegInt(v any) (int64, bool) {
	num, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	n, err := num.Int64()
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

type inventoryIncompleteError struct {
	msg string
}

func (e *inventoryIncompleteError) Error() string { return e.msg }

func (s *Server) verifyAndRewriteRunInventory(ctx context.Context, pendingJSON []byte, art *store.TrajectoryArtifact) ([]byte, error) {
	var inv telemetrycontract.TrajectoryRunInventory
	if err := json.Unmarshal(pendingJSON, &inv); err != nil {
		return nil, fmt.Errorf("invalid run_inventory JSON: %w", err)
	}
	if inv.SchemaVersion != telemetrycontract.TrajectoryRunInventorySchemaVersion {
		return nil, fmt.Errorf("unsupported run_inventory schema_version %d", inv.SchemaVersion)
	}
	if inv.RunID != art.RunID {
		return nil, fmt.Errorf("run_inventory run_id %q does not match artifact run_id %q", inv.RunID, art.RunID)
	}
	if inv.ParserVersion != art.ParserVersion {
		return nil, fmt.Errorf("run_inventory parser_version %q does not match artifact parser_version %q", inv.ParserVersion, art.ParserVersion)
	}
	if inv.InventorySeq != art.InventorySeq {
		return nil, fmt.Errorf("run_inventory inventory_seq %d does not match artifact inventory_seq %d", inv.InventorySeq, art.InventorySeq)
	}
	if inv.Final != art.Final {
		return nil, fmt.Errorf("run_inventory final=%v does not match artifact final=%v", inv.Final, art.Final)
	}

	if inv.Chunks == nil {
		inv.Chunks = []telemetrycontract.TrajectoryRunInventoryChunk{}
	}

	seen := make(map[string]struct{}, len(inv.Chunks))
	for i := range inv.Chunks {
		ch := &inv.Chunks[i]
		if ch.ChunkID == "" {
			return nil, &inventoryIncompleteError{msg: fmt.Sprintf("chunk[%d] has empty chunk_id", i)}
		}
		if _, dup := seen[ch.ChunkID]; dup {
			return nil, fmt.Errorf("duplicate chunk_id %q in run_inventory", ch.ChunkID)
		}
		seen[ch.ChunkID] = struct{}{}

		committedChunk, err := s.store.GetTrajectoryArtifactByIdentity(ctx, store.TrajectoryArtifactIdentity{
			RunID:         art.RunID,
			ArtifactType:  telemetrycontract.TrajectoryArtifactTypeUsageRecords,
			ParserVersion: art.ParserVersion,
			IdentityKey:   ch.ChunkID,
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, &inventoryIncompleteError{msg: fmt.Sprintf("chunk %q is not committed", ch.ChunkID)}
			}
			return nil, fmt.Errorf("lookup chunk %q: %w", ch.ChunkID, err)
		}
		if committedChunk.Status != telemetrycontract.TrajectoryArtifactStatusCommitted {
			return nil, &inventoryIncompleteError{msg: fmt.Sprintf("chunk %q is not committed (status=%s)", ch.ChunkID, committedChunk.Status)}
		}
		if committedChunk.RowCount != ch.ExpectedRowCount {
			return nil, &inventoryIncompleteError{
				msg: fmt.Sprintf("chunk %q expected_row_count %d does not match committed row_count %d",
					ch.ChunkID, ch.ExpectedRowCount, committedChunk.RowCount),
			}
		}
		if committedChunk.StampedSHA256 == "" {
			return nil, &inventoryIncompleteError{msg: fmt.Sprintf("chunk %q has no stamped sha256", ch.ChunkID)}
		}
		ch.SHA256 = committedChunk.StampedSHA256
	}

	rewritten, err := json.Marshal(inv)
	if err != nil {
		return nil, fmt.Errorf("marshal rewritten run_inventory: %w", err)
	}
	return rewritten, nil
}

func stampATIFArtifact(pendingBytes []byte, art *store.TrajectoryArtifact, attr *store.RunAttribution) ([]byte, int, error) {
	rawJSON := pendingBytes
	if art.ContentEncoding == telemetrycontract.TrajectoryContentEncodingGzip {
		gz, err := gzip.NewReader(bytes.NewReader(pendingBytes))
		if err != nil {
			return nil, 0, fmt.Errorf("invalid gzip ATIF content: %w", err)
		}
		defer func() { _ = gz.Close() }()
		rawJSON, err = io.ReadAll(io.LimitReader(gz, trajectoryATIFMaxDecompressedBytes+1))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to decompress ATIF: %w", err)
		}
		if int64(len(rawJSON)) > trajectoryATIFMaxDecompressedBytes {
			return nil, 0, fmt.Errorf("decompressed ATIF exceeds maximum size")
		}
		if err := gz.Close(); err != nil {
			return nil, 0, fmt.Errorf("corrupt gzip trailer: %w", err)
		}
	}

	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(rawJSON))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, 0, fmt.Errorf("invalid ATIF JSON document")
	}

	schemaVer, _ := doc["schema_version"].(string)
	if schemaVer != art.ATIFSchemaVersion {
		return nil, 0, fmt.Errorf("ATIF schema_version %q does not match declared %q", schemaVer, art.ATIFSchemaVersion)
	}
	sessID, _ := doc["session_id"].(string)
	if sessID == "" {
		return nil, 0, fmt.Errorf("ATIF session_id is required")
	}
	agentObj, ok := doc["agent"].(map[string]any)
	if !ok || agentObj == nil {
		return nil, 0, fmt.Errorf("ATIF agent object is required")
	}
	steps, ok := doc["steps"].([]any)
	if !ok {
		return nil, 0, fmt.Errorf("ATIF steps array is required")
	}

	extra, _ := agentObj["extra"].(map[string]any)
	if extra == nil {
		extra = map[string]any{}
	}
	// Replace agent.extra.scion entirely with Hub-authoritative attribution so
	// client-supplied dimension fields cannot survive.
	scionMeta := map[string]any{
		"hub_id":         art.HubID,
		"origin_hub_id":  art.HubID,
		"project_id":     attr.ProjectID,
		"agent_id":       attr.AgentID,
		"run_id":         attr.RunID,
		"launch_id":      attr.LaunchID,
		"harness_config": attr.HarnessConfig,
		"template":       attr.Template,
		"labels":         sortedTrajectoryLabels(attr.LabelsAtLaunch),
		"parser_version": art.ParserVersion,
	}
	if attr.BillingUserID != nil {
		scionMeta["billing_user_id"] = *attr.BillingUserID
	} else {
		scionMeta["billing_user_id"] = nil
	}
	if attr.HarnessConfigDigest != nil {
		scionMeta["harness_config_digest"] = *attr.HarnessConfigDigest
	} else {
		scionMeta["harness_config_digest"] = nil
	}
	if attr.TemplateDigest != nil {
		scionMeta["template_digest"] = *attr.TemplateDigest
	} else {
		scionMeta["template_digest"] = nil
	}
	extra["scion"] = scionMeta
	agentObj["extra"] = extra

	stampedJSON, err := json.Marshal(doc)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal stamped ATIF: %w", err)
	}

	if art.ContentEncoding == telemetrycontract.TrajectoryContentEncodingGzip {
		var buf bytes.Buffer
		outGZ := gzip.NewWriter(&buf)
		if _, err := outGZ.Write(stampedJSON); err != nil {
			return nil, 0, fmt.Errorf("gzip stamped ATIF: %w", err)
		}
		if err := outGZ.Close(); err != nil {
			return nil, 0, fmt.Errorf("close ATIF gzip writer: %w", err)
		}
		return buf.Bytes(), len(steps), nil
	}
	return stampedJSON, len(steps), nil
}
