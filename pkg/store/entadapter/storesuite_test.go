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

//go:build !no_sqlite

package entadapter

import (
	"context"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/storetest"
	"github.com/stretchr/testify/require"
)

// entSuiteStore adapts the Ent-backed schedule, message, and maintenance stores
// to the full store.Store interface so the shared CRUD-parity oracle can drive
// them. Only the methods exercised by the ported domains are implemented; the
// embedded nil store.Store satisfies the rest of the interface and is never
// called by those domains.
type entSuiteStore struct {
	store.Store
	sched *ScheduleStore
	maint *MaintenanceStore
	msg   *MessageStore
}

// CreateProject is a no-op: the Ent schedule/scheduled_event schemas model
// project_id as a plain UUID with no foreign key, so the parity domains' call to
// ensureProject has nothing to create on this backend.
func (e *entSuiteStore) CreateProject(context.Context, *store.Project) error { return nil }

// Schedule sub-interface.
func (e *entSuiteStore) CreateSchedule(ctx context.Context, sc *store.Schedule) error {
	return e.sched.CreateSchedule(ctx, sc)
}
func (e *entSuiteStore) GetSchedule(ctx context.Context, id string) (*store.Schedule, error) {
	return e.sched.GetSchedule(ctx, id)
}
func (e *entSuiteStore) ListSchedules(ctx context.Context, f store.ScheduleFilter, o store.ListOptions) (*store.ListResult[store.Schedule], error) {
	return e.sched.ListSchedules(ctx, f, o)
}
func (e *entSuiteStore) UpdateSchedule(ctx context.Context, sc *store.Schedule) error {
	return e.sched.UpdateSchedule(ctx, sc)
}
func (e *entSuiteStore) UpdateScheduleStatus(ctx context.Context, id, status string) error {
	return e.sched.UpdateScheduleStatus(ctx, id, status)
}
func (e *entSuiteStore) DeleteSchedule(ctx context.Context, id string) error {
	return e.sched.DeleteSchedule(ctx, id)
}

// ScheduledEvent sub-interface.
func (e *entSuiteStore) CreateScheduledEvent(ctx context.Context, ev *store.ScheduledEvent) error {
	return e.sched.CreateScheduledEvent(ctx, ev)
}
func (e *entSuiteStore) GetScheduledEvent(ctx context.Context, id string) (*store.ScheduledEvent, error) {
	return e.sched.GetScheduledEvent(ctx, id)
}
func (e *entSuiteStore) ListScheduledEvents(ctx context.Context, f store.ScheduledEventFilter, o store.ListOptions) (*store.ListResult[store.ScheduledEvent], error) {
	return e.sched.ListScheduledEvents(ctx, f, o)
}
func (e *entSuiteStore) UpdateScheduledEventStatus(ctx context.Context, id, status string, firedAt *time.Time, errMsg string) error {
	return e.sched.UpdateScheduledEventStatus(ctx, id, status, firedAt, errMsg)
}

// Message sub-interface.
func (e *entSuiteStore) CreateMessage(ctx context.Context, m *store.Message) error {
	return e.msg.CreateMessage(ctx, m)
}
func (e *entSuiteStore) GetMessage(ctx context.Context, id string) (*store.Message, error) {
	return e.msg.GetMessage(ctx, id)
}
func (e *entSuiteStore) ListMessages(ctx context.Context, f store.MessageFilter, o store.ListOptions) (*store.ListResult[store.Message], error) {
	return e.msg.ListMessages(ctx, f, o)
}
func (e *entSuiteStore) MarkMessageRead(ctx context.Context, id string) error {
	return e.msg.MarkMessageRead(ctx, id)
}

// Maintenance sub-interface.
func (e *entSuiteStore) CreateMaintenanceRun(ctx context.Context, r *store.MaintenanceOperationRun) error {
	return e.maint.CreateMaintenanceRun(ctx, r)
}
func (e *entSuiteStore) GetMaintenanceRun(ctx context.Context, id string) (*store.MaintenanceOperationRun, error) {
	return e.maint.GetMaintenanceRun(ctx, id)
}
func (e *entSuiteStore) UpdateMaintenanceRun(ctx context.Context, r *store.MaintenanceOperationRun) error {
	return e.maint.UpdateMaintenanceRun(ctx, r)
}
func (e *entSuiteStore) ListMaintenanceRuns(ctx context.Context, opKey string, limit int) ([]store.MaintenanceOperationRun, error) {
	return e.maint.ListMaintenanceRuns(ctx, opKey, limit)
}

func entSuiteFactory(t *testing.T) store.Store {
	t.Helper()
	client, err := entc.OpenSQLite("file:"+t.Name()+"?mode=memory&cache=shared", entc.PoolConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	require.NoError(t, entc.AutoMigrate(context.Background(), client))
	return newEntSuiteStore(client)
}

func newEntSuiteStore(client *ent.Client) *entSuiteStore {
	return &entSuiteStore{
		sched: NewScheduleStore(client),
		maint: NewMaintenanceStore(client),
		msg:   NewMessageStore(client),
	}
}

// TestEntAdapter_CRUDParity_PortedDomains drives the shared CRUD-parity oracle
// against the Ent-backed schedule, scheduled-event, message, and maintenance-run
// stores. This is the same oracle that RunStoreSuite applies to the production
// CompositeStore, proving the ported domains are parity-ready ahead of the
// Postgres backend landing.
func TestEntAdapter_CRUDParity_PortedDomains(t *testing.T) {
	storetest.RunDomain(t, entSuiteFactory, storetest.ScheduleDomain())
	storetest.RunDomain(t, entSuiteFactory, storetest.ScheduledEventDomain())
	storetest.RunDomain(t, entSuiteFactory, storetest.MessageDomain())
	storetest.RunDomain(t, entSuiteFactory, storetest.MaintenanceRunDomain())
}
