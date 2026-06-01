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

package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ensureProject idempotently creates the project that owns FK-bound entities
// (schedules, scheduled events). Backends whose table enforces a project/grove
// foreign key (SQLite) need the row to exist before the child insert; backends
// without the FK (the Ent adapter) treat it as a harmless no-op. Domains call
// this from their Create/Seed closures so they stay backend-agnostic.
func ensureProject(ctx context.Context, s store.Store, projectID string) error {
	err := s.CreateProject(ctx, &store.Project{
		ID:         projectID,
		Name:       "storetest project",
		Slug:       "storetest-" + projectID[:8],
		Visibility: store.VisibilityPrivate,
	})
	if errors.Is(err, store.ErrAlreadyExists) {
		return nil
	}
	return err
}

// ScheduleDomain describes the recurring-schedule entity for the CRUD-parity
// oracle. Schedules are hard-deleted and FK-bound to a project.
func ScheduleDomain() Domain[store.Schedule] {
	return Domain[store.Schedule]{
		Name: "schedule",
		Make: func(seq int) *store.Schedule {
			next := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			return &store.Schedule{
				ID:        uuid.NewString(),
				ProjectID: uuid.NewString(),
				Name:      fmt.Sprintf("schedule-%d", seq),
				CronExpr:  "0 9 * * 1-5",
				EventType: "message",
				Payload:   `{"message":"standup"}`,
				NextRunAt: &next,
				CreatedBy: "tester",
			}
		},
		GetID: func(sc *store.Schedule) string { return sc.ID },
		Create: func(ctx context.Context, s store.Store, sc *store.Schedule) error {
			if err := ensureProject(ctx, s, sc.ProjectID); err != nil {
				return err
			}
			return s.CreateSchedule(ctx, sc)
		},
		Get: func(ctx context.Context, s store.Store, id string) (*store.Schedule, error) {
			return s.GetSchedule(ctx, id)
		},
		List: func(ctx context.Context, s store.Store, opts store.ListOptions) (*store.ListResult[store.Schedule], error) {
			return s.ListSchedules(ctx, store.ScheduleFilter{}, opts)
		},
		VerifyEqual: func(t *testing.T, want, got *store.Schedule) {
			assert.Equal(t, want.ID, got.ID)
			assert.Equal(t, want.Name, got.Name)
			assert.Equal(t, want.CronExpr, got.CronExpr)
			assert.Equal(t, want.EventType, got.EventType)
			assert.Equal(t, store.ScheduleStatusActive, got.Status)
			assert.False(t, got.CreatedAt.IsZero(), "CreatedAt should be set")
		},
		Mutate: func(sc *store.Schedule) {
			sc.Name = "Renamed " + sc.Name
			sc.CronExpr = "0 0 * * 0"
		},
		Update: func(ctx context.Context, s store.Store, sc *store.Schedule) error {
			return s.UpdateSchedule(ctx, sc)
		},
		VerifyMutated: func(t *testing.T, got *store.Schedule) {
			assert.Contains(t, got.Name, "Renamed ")
			assert.Equal(t, "0 0 * * 0", got.CronExpr)
		},
		Delete: func(ctx context.Context, s store.Store, id string) error {
			return s.DeleteSchedule(ctx, id)
		},
		// Schedules are hard-deleted (no SoftDelete spec).
		Filters: []FilterCase[store.Schedule]{
			{
				Name: "ByStatus",
				Seed: func(t *testing.T, ctx context.Context, s store.Store) {
					projectID := uuid.NewString()
					require.NoError(t, ensureProject(ctx, s, projectID))

					active := &store.Schedule{
						ID: uuid.NewString(), ProjectID: projectID, Name: "active",
						CronExpr: "0 9 * * *", EventType: "message", Payload: "{}",
					}
					require.NoError(t, s.CreateSchedule(ctx, active))

					paused := &store.Schedule{
						ID: uuid.NewString(), ProjectID: projectID, Name: "paused",
						CronExpr: "0 9 * * *", EventType: "message", Payload: "{}",
					}
					require.NoError(t, s.CreateSchedule(ctx, paused))
					require.NoError(t, s.UpdateScheduleStatus(ctx, paused.ID, store.ScheduleStatusPaused))
				},
				List: func(ctx context.Context, s store.Store) (*store.ListResult[store.Schedule], error) {
					return s.ListSchedules(ctx, store.ScheduleFilter{Status: store.ScheduleStatusPaused}, store.ListOptions{})
				},
				WantCount: 1,
			},
		},
	}
}

// ScheduledEventDomain describes the one-shot scheduled-event entity. Events
// have no per-id delete (they are cancelled or purged) so the Delete category is
// skipped; status transitions are exercised via the Update category.
func ScheduledEventDomain() Domain[store.ScheduledEvent] {
	return Domain[store.ScheduledEvent]{
		Name: "scheduled_event",
		Make: func(seq int) *store.ScheduledEvent {
			fireAt := time.Now().Add(time.Duration(seq) * time.Minute).UTC().Truncate(time.Second)
			return &store.ScheduledEvent{
				ID:        uuid.NewString(),
				ProjectID: uuid.NewString(),
				EventType: "message",
				FireAt:    fireAt,
				Payload:   `{"text":"hi"}`,
				CreatedBy: "tester",
			}
		},
		GetID: func(e *store.ScheduledEvent) string { return e.ID },
		Create: func(ctx context.Context, s store.Store, e *store.ScheduledEvent) error {
			if err := ensureProject(ctx, s, e.ProjectID); err != nil {
				return err
			}
			return s.CreateScheduledEvent(ctx, e)
		},
		Get: func(ctx context.Context, s store.Store, id string) (*store.ScheduledEvent, error) {
			return s.GetScheduledEvent(ctx, id)
		},
		List: func(ctx context.Context, s store.Store, opts store.ListOptions) (*store.ListResult[store.ScheduledEvent], error) {
			return s.ListScheduledEvents(ctx, store.ScheduledEventFilter{}, opts)
		},
		VerifyEqual: func(t *testing.T, want, got *store.ScheduledEvent) {
			assert.Equal(t, want.ID, got.ID)
			assert.Equal(t, want.EventType, got.EventType)
			assert.Equal(t, store.ScheduledEventPending, got.Status)
			assert.False(t, got.CreatedAt.IsZero(), "CreatedAt should be set")
		},
		Mutate: func(e *store.ScheduledEvent) {
			e.Status = store.ScheduledEventFired
		},
		Update: func(ctx context.Context, s store.Store, e *store.ScheduledEvent) error {
			return s.UpdateScheduledEventStatus(ctx, e.ID, e.Status, nil, "")
		},
		VerifyMutated: func(t *testing.T, got *store.ScheduledEvent) {
			assert.Equal(t, store.ScheduledEventFired, got.Status)
		},
		// No per-id Delete on the interface (cancel/purge only) -> Delete skipped.
		Filters: []FilterCase[store.ScheduledEvent]{
			{
				Name: "ByEventType",
				Seed: func(t *testing.T, ctx context.Context, s store.Store) {
					projectID := uuid.NewString()
					require.NoError(t, ensureProject(ctx, s, projectID))
					require.NoError(t, s.CreateScheduledEvent(ctx, &store.ScheduledEvent{
						ID: uuid.NewString(), ProjectID: projectID, EventType: "message",
						FireAt: time.Now().Add(time.Hour), Payload: "{}",
					}))
					require.NoError(t, s.CreateScheduledEvent(ctx, &store.ScheduledEvent{
						ID: uuid.NewString(), ProjectID: projectID, EventType: "status_update",
						FireAt: time.Now().Add(time.Hour), Payload: "{}",
					}))
				},
				List: func(ctx context.Context, s store.Store) (*store.ListResult[store.ScheduledEvent], error) {
					return s.ListScheduledEvents(ctx, store.ScheduledEventFilter{EventType: "status_update"}, store.ListOptions{})
				},
				WantCount: 1,
			},
		},
	}
}

// MessageDomain describes the message entity. Messages have no per-id delete
// (PurgeOldMessages only) so the Delete category is skipped; the read flag is
// exercised via the Update category.
func MessageDomain() Domain[store.Message] {
	return Domain[store.Message]{
		Name: "message",
		Make: func(seq int) *store.Message {
			return &store.Message{
				ID:          uuid.NewString(),
				ProjectID:   uuid.NewString(),
				Sender:      "user:alice",
				SenderID:    fmt.Sprintf("sender-%d", seq),
				Recipient:   "agent:coder",
				RecipientID: fmt.Sprintf("agent-%d", seq),
				Msg:         fmt.Sprintf("message %d", seq),
				Type:        "instruction",
				AgentID:     fmt.Sprintf("agent-%d", seq),
			}
		},
		GetID: func(m *store.Message) string { return m.ID },
		Create: func(ctx context.Context, s store.Store, m *store.Message) error {
			return s.CreateMessage(ctx, m)
		},
		Get: func(ctx context.Context, s store.Store, id string) (*store.Message, error) {
			return s.GetMessage(ctx, id)
		},
		List: func(ctx context.Context, s store.Store, opts store.ListOptions) (*store.ListResult[store.Message], error) {
			return s.ListMessages(ctx, store.MessageFilter{}, opts)
		},
		VerifyEqual: func(t *testing.T, want, got *store.Message) {
			assert.Equal(t, want.ID, got.ID)
			assert.Equal(t, want.ProjectID, got.ProjectID)
			assert.Equal(t, want.Sender, got.Sender)
			assert.Equal(t, want.Msg, got.Msg)
			assert.Equal(t, want.Type, got.Type)
			assert.False(t, got.Read)
			assert.False(t, got.CreatedAt.IsZero(), "CreatedAt should be set")
		},
		Mutate: func(m *store.Message) {
			m.Read = true
		},
		Update: func(ctx context.Context, s store.Store, m *store.Message) error {
			return s.MarkMessageRead(ctx, m.ID)
		},
		VerifyMutated: func(t *testing.T, got *store.Message) {
			assert.True(t, got.Read)
		},
		// No per-id Delete on the interface (purge only) -> Delete skipped.
		Filters: []FilterCase[store.Message]{
			{
				Name: "ByRecipient",
				Seed: func(t *testing.T, ctx context.Context, s store.Store) {
					projectID := uuid.NewString()
					require.NoError(t, s.CreateMessage(ctx, &store.Message{
						ID: uuid.NewString(), ProjectID: projectID, Sender: "user:a",
						Recipient: "agent:one", RecipientID: "agent-1", Msg: "m1", Type: "instruction",
					}))
					require.NoError(t, s.CreateMessage(ctx, &store.Message{
						ID: uuid.NewString(), ProjectID: projectID, Sender: "user:a",
						Recipient: "agent:two", RecipientID: "agent-2", Msg: "m2", Type: "instruction",
					}))
				},
				List: func(ctx context.Context, s store.Store) (*store.ListResult[store.Message], error) {
					return s.ListMessages(ctx, store.MessageFilter{RecipientID: "agent-1"}, store.ListOptions{})
				},
				WantCount: 1,
			},
		},
	}
}

// MaintenanceRunDomain describes the maintenance operation-run entity. Runs are
// created against a pre-existing operation key; the SQLite backend enforces this
// as a foreign key (the key is seeded by migration), while the Ent backend
// stores it as a plain column. The List/Delete categories are skipped (runs
// have no per-id delete and ListMaintenanceRuns is keyed by operation, not the
// generic ListOptions), but filtering by operation key is verified.
func MaintenanceRunDomain() Domain[store.MaintenanceOperationRun] {
	const seededOpKey = "pull-images"
	return Domain[store.MaintenanceOperationRun]{
		Name: "maintenance_run",
		Make: func(seq int) *store.MaintenanceOperationRun {
			return &store.MaintenanceOperationRun{
				ID:           uuid.NewString(),
				OperationKey: seededOpKey,
				Status:       store.MaintenanceStatusRunning,
				StartedAt:    time.Now().UTC().Truncate(time.Second),
				StartedBy:    fmt.Sprintf("tester-%d", seq),
				Log:          fmt.Sprintf("log %d", seq),
			}
		},
		GetID: func(r *store.MaintenanceOperationRun) string { return r.ID },
		Create: func(ctx context.Context, s store.Store, r *store.MaintenanceOperationRun) error {
			return s.CreateMaintenanceRun(ctx, r)
		},
		Get: func(ctx context.Context, s store.Store, id string) (*store.MaintenanceOperationRun, error) {
			return s.GetMaintenanceRun(ctx, id)
		},
		VerifyEqual: func(t *testing.T, want, got *store.MaintenanceOperationRun) {
			assert.Equal(t, want.ID, got.ID)
			assert.Equal(t, want.OperationKey, got.OperationKey)
			assert.Equal(t, store.MaintenanceStatusRunning, got.Status)
			assert.False(t, got.StartedAt.IsZero(), "StartedAt should be set")
		},
		Mutate: func(r *store.MaintenanceOperationRun) {
			completed := time.Now().UTC().Truncate(time.Second)
			r.Status = store.MaintenanceStatusCompleted
			r.CompletedAt = &completed
			r.Result = `{"ok":true}`
		},
		Update: func(ctx context.Context, s store.Store, r *store.MaintenanceOperationRun) error {
			return s.UpdateMaintenanceRun(ctx, r)
		},
		VerifyMutated: func(t *testing.T, got *store.MaintenanceOperationRun) {
			assert.Equal(t, store.MaintenanceStatusCompleted, got.Status)
			require.NotNil(t, got.CompletedAt)
		},
		// List/Delete categories intentionally omitted (see doc comment).
		Filters: []FilterCase[store.MaintenanceOperationRun]{
			{
				Name: "ByOperationKey",
				Seed: func(t *testing.T, ctx context.Context, s store.Store) {
					require.NoError(t, s.CreateMaintenanceRun(ctx, &store.MaintenanceOperationRun{
						ID: uuid.NewString(), OperationKey: seededOpKey,
						Status: store.MaintenanceStatusRunning, StartedAt: time.Now(),
					}))
					require.NoError(t, s.CreateMaintenanceRun(ctx, &store.MaintenanceOperationRun{
						ID: uuid.NewString(), OperationKey: seededOpKey,
						Status: store.MaintenanceStatusRunning, StartedAt: time.Now(),
					}))
					require.NoError(t, s.CreateMaintenanceRun(ctx, &store.MaintenanceOperationRun{
						ID: uuid.NewString(), OperationKey: "rebuild-web",
						Status: store.MaintenanceStatusRunning, StartedAt: time.Now(),
					}))
				},
				List: func(ctx context.Context, s store.Store) (*store.ListResult[store.MaintenanceOperationRun], error) {
					return listFrom(s.ListMaintenanceRuns(ctx, seededOpKey, 100))
				},
				WantCount: 2,
			},
		},
	}
}
