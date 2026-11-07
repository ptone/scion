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

package runtimebroker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	scionrt "github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// servedProjectsRefreshInterval is how often the served-project set is
// reloaded from the hub (substrate v1 design §3.3, "List source in operator
// mode": loaded at start, on every hub reconnect, and every 5 minutes).
const servedProjectsRefreshInterval = 5 * time.Minute

// servedProjectsLoadTimeout bounds one ListProjects call.
const servedProjectsLoadTimeout = 30 * time.Second

// errServedProjectsCold is returned while the served-project set has no
// load to answer from: before the first load of a hub connection, or when
// the hub was unreachable on every attempt so far. An unfiltered substrate
// List turns it into an error rather than an empty result.
var errServedProjectsCold = errors.New("served projects not loaded from the hub yet")

// brokerProjectLister is the one hubclient call the served-project set
// reads: the projects this broker is a registered provider for
// (GET /api/v1/runtime-brokers/{id}/projects). Not /api/v1/broker/projects,
// and never derived from agent records or the heartbeat's project map.
type brokerProjectLister interface {
	ListProjects(ctx context.Context, brokerID string) (*hubclient.ListBrokerProjectsResponse, error)
}

// servedProjectsSource is one hub connection's registration source.
type servedProjectsSource struct {
	name     string // hub connection name
	brokerID string
	lister   brokerProjectLister
}

func (s servedProjectsSource) key() string { return s.name + "\x00" + s.brokerID }

// servedProjectsSnapshot is the last good load of one source.
type servedProjectsSnapshot struct {
	projectIDs []string
	loadedAt   time.Time
	// lastErr is the error of the most recent load attempt after loadedAt,
	// nil when the latest attempt succeeded.
	lastErr error
}

// servedProjects implements scionrt.ProjectSet from the broker's hub
// project registrations. It is the union of one snapshot per hub
// connection. Each snapshot is replaced only by a successful load, so a
// failed refresh keeps the last good set (its age is logged when it is
// used). While any current connection has never loaded, the set is cold
// and ServedProjects returns an error: a union over only the connections
// that have loaded would make an unfiltered List silently partial.
//
// Loads run only on the refresher goroutine (run), so they are serial.
type servedProjects struct {
	sources func() []servedProjectsSource
	now     func() time.Time
	// newTicker returns the refresh tick channel and its stop function;
	// tests substitute a manual channel so no test sleeps.
	newTicker func(d time.Duration) (<-chan time.Time, func())
	log       *slog.Logger

	reloadCh chan struct{}

	mu    sync.Mutex
	snaps map[string]*servedProjectsSnapshot
}

var _ scionrt.ProjectSet = (*servedProjects)(nil)

func newServedProjects(sources func() []servedProjectsSource, log *slog.Logger) *servedProjects {
	if log == nil {
		log = slog.Default()
	}
	return &servedProjects{
		sources: sources,
		now:     time.Now,
		newTicker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
		log:      log,
		reloadCh: make(chan struct{}, 1),
		snaps:    make(map[string]*servedProjectsSnapshot),
	}
}

// requestReload asks the refresher to reload soon. It never blocks;
// requests made while one is pending coalesce. The broker calls it when a
// hub connection's control channel (re)connects.
func (p *servedProjects) requestReload() {
	select {
	case p.reloadCh <- struct{}{}:
	default:
	}
}

// run loads once at start, then on every reload request and every
// servedProjectsRefreshInterval, until ctx ends.
func (p *servedProjects) run(ctx context.Context) {
	p.reload(ctx)
	tick, stop := p.newTicker(servedProjectsRefreshInterval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			p.reload(ctx)
		case <-p.reloadCh:
			p.reload(ctx)
		}
	}
}

// reload loads every current source once. A failure keeps that source's
// last good snapshot.
func (p *servedProjects) reload(ctx context.Context) {
	for _, src := range p.sources() {
		if ctx.Err() != nil {
			return
		}
		lctx, cancel := context.WithTimeout(ctx, servedProjectsLoadTimeout)
		resp, err := src.lister.ListProjects(lctx, src.brokerID)
		cancel()
		if err == nil && resp == nil {
			err = errors.New("empty response")
		}
		p.mu.Lock()
		snap := p.snaps[src.key()]
		if err != nil {
			if snap != nil {
				snap.lastErr = err
			}
			p.mu.Unlock()
			p.log.Warn("Could not load served projects from the hub",
				"hub", src.name, "error", err, "has_last_good", snap != nil)
			continue
		}
		ids := make([]string, 0, len(resp.Projects))
		for _, pr := range resp.Projects {
			if pr.ProjectID != "" {
				ids = append(ids, pr.ProjectID)
			}
		}
		p.snaps[src.key()] = &servedProjectsSnapshot{projectIDs: ids, loadedAt: p.now()}
		p.mu.Unlock()
		p.log.Debug("Loaded served projects from the hub", "hub", src.name, "count", len(ids))
	}
}

// ServedProjects implements scionrt.ProjectSet: the sorted, de-duplicated
// project IDs this broker is a registered provider for on every current
// hub connection. It returns an error when there is no hub connection or
// any current connection has never loaded.
func (p *servedProjects) ServedProjects(ctx context.Context) ([]string, error) {
	sources := p.sources()
	if len(sources) == 0 {
		return nil, fmt.Errorf("%w: no hub connection", errServedProjectsCold)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := make(map[string]struct{})
	var out []string
	for _, src := range sources {
		snap := p.snaps[src.key()]
		if snap == nil {
			return nil, fmt.Errorf("%w (hub %q)", errServedProjectsCold, src.name)
		}
		if snap.lastErr != nil {
			p.log.Warn("Using the last good served-project set; the latest hub load failed",
				"hub", src.name, "age", p.now().Sub(snap.loadedAt).Round(time.Second), "error", snap.lastErr)
		}
		for _, id := range snap.projectIDs {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// installProjectSet gives rt the broker's served-project set when rt takes
// one (scionrt.ProjectSetReceiver; today the substrate runtime). Every
// runtime that enters the broker passes through here: New, SwapRuntime and
// newAuxiliaryRuntime. A runtime that missed it fails an unfiltered List
// as cold, never as empty.
func (s *Server) installProjectSet(rt scionrt.Runtime) {
	if s.servedProjects == nil {
		return
	}
	if r, ok := rt.(scionrt.ProjectSetReceiver); ok {
		r.SetProjectSet(s.servedProjects)
	}
}

// newAuxiliaryRuntime is the only constructor of a registered
// auxiliaryRuntime entry: it installs the served-project set on rt first.
// served_projects_install_test.go guards that no other code builds one.
func (s *Server) newAuxiliaryRuntime(rt scionrt.Runtime, mgr agent.Manager, rec config.RuntimeBuildRecord) auxiliaryRuntime {
	s.installProjectSet(rt)
	return auxiliaryRuntime{Runtime: rt, Manager: mgr, Record: rec}
}

// servedProjectsSources returns one source per hub connection that is
// started (its heartbeat or control channel runs, which needs valid
// credentials) and not closed. A removed or stopped connection is not a
// source, so it neither contributes projects nor holds the set cold.
func (s *Server) servedProjectsSources() []servedProjectsSource {
	var out []servedProjectsSource
	for _, hc := range s.hubConnectionList() {
		hc.mu.RLock()
		started := hc.Heartbeat != nil || hc.ControlChannel != nil
		closed := hc.closed
		client := hc.HubClient
		name, brokerID := hc.Name, hc.BrokerID
		hc.mu.RUnlock()
		if !started || closed || client == nil || brokerID == "" {
			continue
		}
		out = append(out, servedProjectsSource{name: name, brokerID: brokerID, lister: client.RuntimeBrokers()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// startServedProjectsRefresh starts the served-project refresher: a load
// now, on every hub reconnect (requestReload from the control channel's
// connect callback) and every servedProjectsRefreshInterval, until ctx
// ends.
func (s *Server) startServedProjectsRefresh(ctx context.Context) {
	if s.servedProjects == nil {
		return
	}
	go s.servedProjects.run(ctx)
}
