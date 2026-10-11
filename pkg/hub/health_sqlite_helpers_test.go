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

package hub

import (
	"context"
	"errors"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

var hubInstanceT0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// fakeClockHubInstanceStore wraps a real store and serves ListHubInstances
// from fixed rows and a fixed store clock.
type fakeClockHubInstanceStore struct {
	store.Store
	rows   []store.HubInstance
	now    time.Time
	err    error
	window time.Duration
}

func newHealthSummaryPluginDouble(names ...string) *healthSummaryPluginDouble {
	d := &healthSummaryPluginDouble{
		mockIntegrationManager: newMockIntegrationManager(),
		health:                 map[string]string{},
		message:                map[string]string{},
		details:                map[string]map[string]string{},
		stopped:                map[string]bool{},
	}
	for _, n := range names {
		d.plugins[n] = map[string]string{}
		d.health[n] = "healthy"
	}
	return d
}

func (f *fakeClockHubInstanceStore) ListHubInstances(_ context.Context, window time.Duration) ([]store.HubInstance, time.Time, error) {
	f.window = window
	if f.err != nil {
		return nil, time.Time{}, f.err
	}
	return f.rows, f.now, nil
}

// healthSummaryPluginDouble is a test double for the plugin manager with
// per-plugin health. A plugin listed in stopped fails its info query, as a
// plugin whose process has exited does.
type healthSummaryPluginDouble struct {
	*mockIntegrationManager
	health  map[string]string
	message map[string]string
	details map[string]map[string]string
	stopped map[string]bool
}

func (d *healthSummaryPluginDouble) BrokerInfo(name string) (string, string, []string, error) {
	if d.stopped[name] {
		return "", "", nil, errors.New("plugin process exited: connection refused")
	}
	return "v1.2.3", "chan-secret-id", []string{"send"}, nil
}

func (d *healthSummaryPluginDouble) BrokerHealthCheck(name string) (string, string, map[string]string, error) {
	if d.stopped[name] {
		return "", "", nil, errors.New("plugin process exited")
	}
	return d.health[name], d.message[name], d.details[name], nil
}
