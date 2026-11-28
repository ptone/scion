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

package entadapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/trajectoryartifact"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/trajectorysession"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

// TrajectoryArtifactStore implements store.TrajectoryArtifactStore using Ent.
type TrajectoryArtifactStore struct {
	client *ent.Client
	inTx   bool
}

// NewTrajectoryArtifactStore creates a new Ent-backed TrajectoryArtifactStore.
func NewTrajectoryArtifactStore(client *ent.Client) *TrajectoryArtifactStore {
	return &TrajectoryArtifactStore{client: client}
}

var _ store.TrajectoryArtifactStore = (*TrajectoryArtifactStore)(nil)
var _ store.TrajectoryArtifactStore = (*CompositeStore)(nil)

func entTrajectoryArtifactToStore(row *ent.TrajectoryArtifact) *store.TrajectoryArtifact {
	if row == nil {
		return nil
	}
	var committedAt *time.Time
	if row.CommittedAt != nil {
		t := row.CommittedAt.UTC()
		committedAt = &t
	}
	return &store.TrajectoryArtifact{
		ID:                 row.ID,
		HubID:              row.HubID,
		ProjectID:          row.ProjectID,
		AgentID:            row.AgentID,
		RunID:              row.RunID,
		Harness:            row.Harness,
		ArtifactType:       row.ArtifactType,
		ParserVersion:      row.ParserVersion,
		IdentityKey:        row.IdentityKey,
		ChunkID:            row.ChunkID,
		InventorySeq:       row.InventorySeq,
		Final:              row.Final,
		SessionID:          row.SessionID,
		NativeRelativePath: row.NativeRelativePath,
		ParsedOffset:       row.ParsedOffset,
		RowCount:           row.RowCount,
		ATIFSchemaVersion:  row.AtifSchemaVersion,
		FileFormat:         row.FileFormat,
		ContentEncoding:    row.ContentEncoding,
		Size:               row.Size,
		ChargedBytes:       row.ChargedBytes,
		SHA256:             row.Sha256,
		StampedSize:        row.StampedSize,
		StampedSHA256:      row.StampedSha256,
		PendingObjectPath:  row.PendingObjectPath,
		ObjectPath:         row.ObjectPath,
		Status:             row.Status,
		CreatedAt:          row.CreatedAt.UTC(),
		CommittedAt:        committedAt,
		PrepareExpiresAt:   row.PrepareExpiresAt.UTC(),
	}
}

func entTrajectorySessionToStore(row *ent.TrajectorySession) *store.TrajectorySession {
	if row == nil {
		return nil
	}
	return &store.TrajectorySession{
		ID:                 row.ID,
		HubID:              row.HubID,
		ProjectID:          row.ProjectID,
		AgentID:            row.AgentID,
		RunID:              row.RunID,
		Harness:            row.Harness,
		SessionID:          row.SessionID,
		NativeRelativePath: row.NativeRelativePath,
		IsSubagent:         row.IsSubagent,
		ParentSessionID:    row.ParentSessionID,
		ParserVersion:      row.ParserVersion,
		ParserVersionNum:   row.ParserVersionNum,
		ParsedOffset:       row.ParsedOffset,
		ATIFArtifactID:     row.AtifArtifactID,
		ATIFObjectPath:     row.AtifObjectPath,
		ATIFSchemaVersion:  row.AtifSchemaVersion,
		FileFormat:         row.FileFormat,
		StepCount:          row.StepCount,
		NormalizeStatus:    row.NormalizeStatus,
		ParseErrorCount:    row.ParseErrorCount,
		FirstError:         row.FirstError,
		UpdatedAt:          row.UpdatedAt.UTC(),
	}
}

// PrepareTrajectoryArtifact implements store.TrajectoryArtifactStore.
// Uses INSERT ... ON CONFLICT (run_id, artifact_type, parser_version,
// identity_key) DO NOTHING so concurrent prepares are safe on both SQLite and
// Postgres.
func (s *TrajectoryArtifactStore) PrepareTrajectoryArtifact(ctx context.Context, art *store.TrajectoryArtifact) (*store.TrajectoryArtifact, error) {
	if art == nil || art.AgentID == "" || art.RunID == "" || art.Harness == "" ||
		art.ArtifactType == "" || art.ParserVersion == "" || art.PendingObjectPath == "" || art.ObjectPath == "" {
		return nil, fmt.Errorf("%w: artifact identity and object paths are required", store.ErrInvalidInput)
	}
	if art.Size <= 0 || art.SHA256 == "" {
		return nil, fmt.Errorf("%w: size and sha256 are required", store.ErrInvalidInput)
	}

	identityKey := art.IdentityKey
	if identityKey == "" {
		identityKey = store.TrajectoryArtifactIdentityKey(
			art.ArtifactType, art.ChunkID, art.InventorySeq, art.NativeRelativePath, art.ParsedOffset,
		)
	}
	if identityKey == "" {
		return nil, fmt.Errorf("%w: unable to derive artifact identity_key", store.ErrInvalidInput)
	}

	id := art.ID
	if id == "" {
		id = api.NewUUID()
	}
	now := art.CreatedAt
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC().Truncate(time.Microsecond)
	expiresAt := art.PrepareExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(telemetrycontract.DefaultTrajectoryPrepareTTL)
	}
	expiresAt = expiresAt.UTC().Truncate(time.Microsecond)

	status := art.Status
	if status == "" {
		status = telemetrycontract.TrajectoryArtifactStatusPending
	}
	contentEncoding := art.ContentEncoding
	if contentEncoding == "" {
		contentEncoding = telemetrycontract.TrajectoryContentEncodingGzip
	}
	reqCharged := max(art.Size, art.ChargedBytes)

	drv := s.client.Driver()
	query, args := entsql.Dialect(drv.Dialect()).
		Insert(trajectoryartifact.Table).
		Columns(
			trajectoryartifact.FieldID,
			trajectoryartifact.FieldHubID,
			trajectoryartifact.FieldProjectID,
			trajectoryartifact.FieldAgentID,
			trajectoryartifact.FieldRunID,
			trajectoryartifact.FieldHarness,
			trajectoryartifact.FieldArtifactType,
			trajectoryartifact.FieldParserVersion,
			trajectoryartifact.FieldIdentityKey,
			trajectoryartifact.FieldChunkID,
			trajectoryartifact.FieldInventorySeq,
			trajectoryartifact.FieldFinal,
			trajectoryartifact.FieldSessionID,
			trajectoryartifact.FieldNativeRelativePath,
			trajectoryartifact.FieldParsedOffset,
			trajectoryartifact.FieldRowCount,
			trajectoryartifact.FieldAtifSchemaVersion,
			trajectoryartifact.FieldFileFormat,
			trajectoryartifact.FieldContentEncoding,
			trajectoryartifact.FieldSize,
			trajectoryartifact.FieldChargedBytes,
			trajectoryartifact.FieldSha256,
			trajectoryartifact.FieldStampedSize,
			trajectoryartifact.FieldStampedSha256,
			trajectoryartifact.FieldPendingObjectPath,
			trajectoryartifact.FieldObjectPath,
			trajectoryartifact.FieldStatus,
			trajectoryartifact.FieldCreatedAt,
			trajectoryartifact.FieldPrepareExpiresAt,
		).
		Values(
			id,
			art.HubID,
			art.ProjectID,
			art.AgentID,
			art.RunID,
			art.Harness,
			art.ArtifactType,
			art.ParserVersion,
			identityKey,
			art.ChunkID,
			art.InventorySeq,
			art.Final,
			art.SessionID,
			art.NativeRelativePath,
			art.ParsedOffset,
			art.RowCount,
			art.ATIFSchemaVersion,
			art.FileFormat,
			contentEncoding,
			art.Size,
			reqCharged,
			art.SHA256,
			0,
			"",
			art.PendingObjectPath,
			art.ObjectPath,
			status,
			now,
			expiresAt,
		).
		OnConflict(
			entsql.ConflictColumns(
				trajectoryartifact.FieldRunID,
				trajectoryartifact.FieldArtifactType,
				trajectoryartifact.FieldParserVersion,
				trajectoryartifact.FieldIdentityKey,
			),
			entsql.DoNothing(),
		).
		Query()

	var res sql.Result
	if err := drv.Exec(ctx, query, args, &res); err != nil {
		return nil, mapError(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, mapError(err)
	}
	if affected > 0 {
		out := *art
		out.ID = id
		out.IdentityKey = identityKey
		out.ContentEncoding = contentEncoding
		out.ChargedBytes = reqCharged
		out.Status = status
		out.CreatedAt = now
		out.PrepareExpiresAt = expiresAt
		out.CommittedAt = nil
		return &out, nil
	}

	existing, err := s.GetTrajectoryArtifactByIdentity(ctx, store.TrajectoryArtifactIdentity{
		RunID:         art.RunID,
		ArtifactType:  art.ArtifactType,
		ParserVersion: art.ParserVersion,
		IdentityKey:   identityKey,
	})
	if err != nil {
		return nil, err
	}

	if existing.Status == telemetrycontract.TrajectoryArtifactStatusCommitted {
		if existing.SHA256 == art.SHA256 {
			return existing, nil
		}
		return nil, store.ErrTrajectoryArtifactConflict
	}

	highWater := max(existing.ChargedBytes, existing.Size, reqCharged)
	n, err := s.client.TrajectoryArtifact.Update().
		Where(
			trajectoryartifact.IDEQ(existing.ID),
			trajectoryartifact.StatusEQ(telemetrycontract.TrajectoryArtifactStatusPending),
		).
		SetSize(art.Size).
		SetChargedBytes(highWater).
		SetSha256(art.SHA256).
		SetRowCount(art.RowCount).
		SetFinal(art.Final).
		SetSessionID(art.SessionID).
		SetAtifSchemaVersion(art.ATIFSchemaVersion).
		SetFileFormat(art.FileFormat).
		SetContentEncoding(contentEncoding).
		SetPrepareExpiresAt(expiresAt).
		Save(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	refreshed, err := s.GetTrajectoryArtifact(ctx, existing.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 && refreshed.Status == telemetrycontract.TrajectoryArtifactStatusCommitted {
		if refreshed.SHA256 == art.SHA256 {
			return refreshed, nil
		}
		return nil, store.ErrTrajectoryArtifactConflict
	}
	return refreshed, nil
}

// GetTrajectoryArtifact implements store.TrajectoryArtifactStore.
func (s *TrajectoryArtifactStore) GetTrajectoryArtifact(ctx context.Context, artifactID string) (*store.TrajectoryArtifact, error) {
	if artifactID == "" {
		return nil, store.ErrNotFound
	}
	row, err := s.client.TrajectoryArtifact.Query().
		Where(trajectoryartifact.IDEQ(artifactID)).
		Only(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return entTrajectoryArtifactToStore(row), nil
}

// GetTrajectoryArtifactByIdentity implements store.TrajectoryArtifactStore.
func (s *TrajectoryArtifactStore) GetTrajectoryArtifactByIdentity(ctx context.Context, id store.TrajectoryArtifactIdentity) (*store.TrajectoryArtifact, error) {
	row, err := s.client.TrajectoryArtifact.Query().
		Where(
			trajectoryartifact.RunIDEQ(id.RunID),
			trajectoryartifact.ArtifactTypeEQ(id.ArtifactType),
			trajectoryartifact.ParserVersionEQ(id.ParserVersion),
			trajectoryartifact.IdentityKeyEQ(id.IdentityKey),
		).
		Only(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return entTrajectoryArtifactToStore(row), nil
}

// CommitTrajectoryArtifact implements store.TrajectoryArtifactStore.
func (s *TrajectoryArtifactStore) CommitTrajectoryArtifact(ctx context.Context, artifactID string, stampedSize int64, stampedSHA256 string, committedAt time.Time, expect store.TrajectoryArtifactCommitExpectation) (*store.TrajectoryArtifact, error) {
	if artifactID == "" {
		return nil, store.ErrNotFound
	}
	if committedAt.IsZero() {
		committedAt = time.Now()
	}
	committedAt = committedAt.UTC().Truncate(time.Microsecond)

	n, err := s.client.TrajectoryArtifact.Update().
		Where(
			trajectoryartifact.IDEQ(artifactID),
			trajectoryartifact.StatusEQ(telemetrycontract.TrajectoryArtifactStatusPending),
			trajectoryartifact.SizeEQ(expect.Size),
			trajectoryartifact.Sha256EQ(expect.SHA256),
			trajectoryartifact.RowCountEQ(expect.RowCount),
			trajectoryartifact.FinalEQ(expect.Final),
		).
		SetStatus(telemetrycontract.TrajectoryArtifactStatusCommitted).
		SetStampedSize(stampedSize).
		SetStampedSha256(stampedSHA256).
		SetCommittedAt(committedAt).
		Save(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	row, err := s.GetTrajectoryArtifact(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		if row.Status == telemetrycontract.TrajectoryArtifactStatusCommitted {
			if row.Size == expect.Size &&
				row.SHA256 == expect.SHA256 &&
				row.RowCount == expect.RowCount &&
				row.Final == expect.Final {
				return row, nil
			}
			return nil, store.ErrTrajectoryArtifactConflict
		}
		if row.Status == telemetrycontract.TrajectoryArtifactStatusPending {
			return nil, store.ErrTrajectoryArtifactModified
		}
		return nil, fmt.Errorf("unexpected trajectory artifact status %q", row.Status)
	}
	return row, nil
}

// SumTrajectoryRunArtifactBytes implements store.TrajectoryArtifactStore.
func (s *TrajectoryArtifactStore) SumTrajectoryRunArtifactBytes(ctx context.Context, agentID, runID, excludeID string) (int64, error) {
	if agentID == "" || runID == "" {
		return 0, nil
	}

	cb := trajectoryartifact.FieldChargedBytes
	sz := trajectoryartifact.FieldSize
	chargedExpr := "CASE WHEN " + cb + " >= " + sz + " THEN " + cb + " ELSE " + sz + " END"

	drv := s.client.Driver()
	sb := entsql.Dialect(drv.Dialect()).
		Select("COALESCE(SUM(" + chargedExpr + "), 0)").
		From(entsql.Table(trajectoryartifact.Table)).
		Where(entsql.And(
			entsql.EQ(trajectoryartifact.FieldAgentID, agentID),
			entsql.EQ(trajectoryartifact.FieldRunID, runID),
			entsql.In(trajectoryartifact.FieldStatus,
				telemetrycontract.TrajectoryArtifactStatusCommitted,
				telemetrycontract.TrajectoryArtifactStatusPending,
			),
		))
	if excludeID != "" {
		sb.Where(entsql.NEQ(trajectoryartifact.FieldID, excludeID))
	}
	query, args := sb.Query()

	var rows entsql.Rows
	if err := drv.Query(ctx, query, args, &rows); err != nil {
		if errors.Is(mapError(err), store.ErrNotFound) {
			return 0, nil
		}
		return 0, mapError(err)
	}
	defer func() { _ = rows.Close() }()

	var total int64
	if rows.Next() {
		if err := rows.Scan(&total); err != nil {
			return 0, mapError(err)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, mapError(err)
	}
	return total, nil
}

// CountTrajectoryRunArtifacts implements store.TrajectoryArtifactStore.
func (s *TrajectoryArtifactStore) CountTrajectoryRunArtifacts(ctx context.Context, agentID, runID, excludeID string) (int64, error) {
	if agentID == "" || runID == "" {
		return 0, nil
	}

	drv := s.client.Driver()
	sb := entsql.Dialect(drv.Dialect()).
		Select("COUNT(*)").
		From(entsql.Table(trajectoryartifact.Table)).
		Where(entsql.And(
			entsql.EQ(trajectoryartifact.FieldAgentID, agentID),
			entsql.EQ(trajectoryartifact.FieldRunID, runID),
			entsql.In(trajectoryartifact.FieldStatus,
				telemetrycontract.TrajectoryArtifactStatusCommitted,
				telemetrycontract.TrajectoryArtifactStatusPending,
			),
		))
	if excludeID != "" {
		sb.Where(entsql.NEQ(trajectoryartifact.FieldID, excludeID))
	}
	query, args := sb.Query()

	var rows entsql.Rows
	if err := drv.Query(ctx, query, args, &rows); err != nil {
		if errors.Is(mapError(err), store.ErrNotFound) {
			return 0, nil
		}
		return 0, mapError(err)
	}
	defer func() { _ = rows.Close() }()

	var count int64
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return 0, mapError(err)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, mapError(err)
	}
	return count, nil
}

// UpsertTrajectorySession implements store.TrajectoryArtifactStore.
// Uses INSERT ... ON CONFLICT (run_id, native_relative_path) DO NOTHING
// followed by a single atomic conditional UPDATE gated on
// (parser_version_num, parsed_offset) >= stored (parser_version_num,
// parsed_offset), safe across replicas on both SQLite and Postgres.
func (s *TrajectoryArtifactStore) UpsertTrajectorySession(ctx context.Context, sess *store.TrajectorySession) (*store.TrajectorySession, bool, error) {
	if sess == nil || sess.RunID == "" || sess.NativeRelativePath == "" || sess.AgentID == "" || sess.Harness == "" || sess.ParserVersion == "" {
		return nil, false, fmt.Errorf("%w: session run_id, native_relative_path, agent_id, harness, and parser_version are required", store.ErrInvalidInput)
	}

	id := sess.ID
	if id == "" {
		id = api.NewUUID()
	}
	now := sess.UpdatedAt
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC().Truncate(time.Microsecond)

	pvNum := sess.ParserVersionNum
	if pvNum == 0 {
		if _, n, ok := telemetrycontract.ParseTrajectoryParserVersion(sess.ParserVersion); ok {
			pvNum = n
		}
	}
	normStatus := sess.NormalizeStatus
	if normStatus == "" {
		normStatus = telemetrycontract.TrajectorySessionNormalizeStatusOK
	}

	drv := s.client.Driver()
	query, args := entsql.Dialect(drv.Dialect()).
		Insert(trajectorysession.Table).
		Columns(
			trajectorysession.FieldID,
			trajectorysession.FieldHubID,
			trajectorysession.FieldProjectID,
			trajectorysession.FieldAgentID,
			trajectorysession.FieldRunID,
			trajectorysession.FieldHarness,
			trajectorysession.FieldSessionID,
			trajectorysession.FieldNativeRelativePath,
			trajectorysession.FieldIsSubagent,
			trajectorysession.FieldParentSessionID,
			trajectorysession.FieldParserVersion,
			trajectorysession.FieldParserVersionNum,
			trajectorysession.FieldParsedOffset,
			trajectorysession.FieldAtifArtifactID,
			trajectorysession.FieldAtifObjectPath,
			trajectorysession.FieldAtifSchemaVersion,
			trajectorysession.FieldFileFormat,
			trajectorysession.FieldStepCount,
			trajectorysession.FieldNormalizeStatus,
			trajectorysession.FieldParseErrorCount,
			trajectorysession.FieldFirstError,
			trajectorysession.FieldUpdatedAt,
		).
		Values(
			id,
			sess.HubID,
			sess.ProjectID,
			sess.AgentID,
			sess.RunID,
			sess.Harness,
			sess.SessionID,
			sess.NativeRelativePath,
			sess.IsSubagent,
			sess.ParentSessionID,
			sess.ParserVersion,
			pvNum,
			sess.ParsedOffset,
			sess.ATIFArtifactID,
			sess.ATIFObjectPath,
			sess.ATIFSchemaVersion,
			sess.FileFormat,
			sess.StepCount,
			normStatus,
			sess.ParseErrorCount,
			sess.FirstError,
			now,
		).
		OnConflict(
			entsql.ConflictColumns(
				trajectorysession.FieldRunID,
				trajectorysession.FieldNativeRelativePath,
			),
			entsql.DoNothing(),
		).
		Query()

	var res sql.Result
	if err := drv.Exec(ctx, query, args, &res); err != nil {
		return nil, false, mapError(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, false, mapError(err)
	}
	if affected > 0 {
		out := *sess
		out.ID = id
		out.ParserVersionNum = pvNum
		out.NormalizeStatus = normStatus
		out.UpdatedAt = now
		return &out, true, nil
	}

	// Row already exists: conditionally update only when
	// (parser_version_num, parsed_offset) >= stored (parser_version_num, parsed_offset).
	upd := entsql.Dialect(drv.Dialect()).
		Update(trajectorysession.Table).
		Set(trajectorysession.FieldParserVersion, sess.ParserVersion).
		Set(trajectorysession.FieldParserVersionNum, pvNum).
		Set(trajectorysession.FieldParsedOffset, sess.ParsedOffset).
		Set(trajectorysession.FieldNormalizeStatus, normStatus).
		Set(trajectorysession.FieldParseErrorCount, sess.ParseErrorCount).
		Set(trajectorysession.FieldFirstError, sess.FirstError).
		Set(trajectorysession.FieldUpdatedAt, now)

	if sess.SessionID != "" {
		upd.Set(trajectorysession.FieldSessionID, sess.SessionID)
	}
	if sess.IsSubagent {
		upd.Set(trajectorysession.FieldIsSubagent, true)
	}
	if sess.ParentSessionID != "" {
		upd.Set(trajectorysession.FieldParentSessionID, sess.ParentSessionID)
	}
	// Preserve previously committed ATIF pointers when recording a standalone
	// parse_failed status report (which carries no new ATIF artifact).
	if sess.ATIFArtifactID != "" {
		upd.Set(trajectorysession.FieldAtifArtifactID, sess.ATIFArtifactID).
			Set(trajectorysession.FieldAtifObjectPath, sess.ATIFObjectPath).
			Set(trajectorysession.FieldAtifSchemaVersion, sess.ATIFSchemaVersion).
			Set(trajectorysession.FieldFileFormat, sess.FileFormat).
			Set(trajectorysession.FieldStepCount, sess.StepCount)
	}

	upd.Where(entsql.And(
		entsql.EQ(trajectorysession.FieldRunID, sess.RunID),
		entsql.EQ(trajectorysession.FieldNativeRelativePath, sess.NativeRelativePath),
		entsql.Or(
			entsql.LT(trajectorysession.FieldParserVersionNum, pvNum),
			entsql.And(
				entsql.EQ(trajectorysession.FieldParserVersionNum, pvNum),
				entsql.LTE(trajectorysession.FieldParsedOffset, sess.ParsedOffset),
			),
		),
	))

	updQuery, updArgs := upd.Query()
	var updRes sql.Result
	if err := drv.Exec(ctx, updQuery, updArgs, &updRes); err != nil {
		return nil, false, mapError(err)
	}
	updAffected, err := updRes.RowsAffected()
	if err != nil {
		return nil, false, mapError(err)
	}

	row, err := s.GetTrajectorySessionByPath(ctx, sess.RunID, sess.NativeRelativePath)
	if err != nil {
		return nil, false, err
	}
	return row, updAffected > 0, nil
}

// GetTrajectorySessionByPath implements store.TrajectoryArtifactStore.
func (s *TrajectoryArtifactStore) GetTrajectorySessionByPath(ctx context.Context, runID, nativeRelativePath string) (*store.TrajectorySession, error) {
	if runID == "" || nativeRelativePath == "" {
		return nil, store.ErrNotFound
	}
	row, err := s.client.TrajectorySession.Query().
		Where(
			trajectorysession.RunIDEQ(runID),
			trajectorysession.NativeRelativePathEQ(nativeRelativePath),
		).
		Only(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return entTrajectorySessionToStore(row), nil
}
