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

package mcpserver

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

const (
	// DefaultHubHealthInterval is how often standalone mode probes the Hub's /healthz.
	DefaultHubHealthInterval = 5 * time.Second
	// DefaultHubUnhealthyAfter is how long continuous /healthz failures must
	// persist before standalone /readyz answers 503 (§4.5).
	DefaultHubUnhealthyAfter = 30 * time.Second
)

// NewHubHealthProbe returns a probe function suitable for Options.HubHealthProbe
// in standalone mode. It calls GET <HubBaseURL>/healthz through hubclient (with
// AllowlistTransport and SCION_TRANSPORT_MODE transportauth layering enabled
// unless opts.DisableTransportAuth is set).
func NewHubHealthProbe(opts BackendOptions) func(ctx context.Context) error {
	baseURL := opts.HubBaseURL
	if baseURL == "" {
		baseURL = "http://in-process"
	}
	rt := &AllowlistTransport{
		Base:                      opts.Transport,
		Version:                   opts.Version,
		AllowTransportAuthHeaders: !opts.DisableTransportAuth,
	}
	return func(ctx context.Context) error {
		hc := &http.Client{Transport: rt}
		clOpts := []hubclient.Option{hubclient.WithHTTPClient(hc)}
		if opts.DisableTransportAuth {
			clOpts = append(clOpts, hubclient.WithTransportAuth(nil, 0))
		}
		cl, err := hubclient.New(baseURL, clOpts...)
		if err != nil {
			return err
		}
		_, err = cl.Health(ctx)
		return err
	}
}

type hubHealthTracker struct {
	probe          func(ctx context.Context) error
	interval       time.Duration
	unhealthyAfter time.Duration
	now            func() time.Time

	mu           sync.Mutex
	failingSince time.Time
}

func newHubHealthTracker(probe func(ctx context.Context) error, interval, unhealthyAfter time.Duration, now func() time.Time) *hubHealthTracker {
	if interval <= 0 {
		interval = DefaultHubHealthInterval
	}
	if unhealthyAfter <= 0 {
		unhealthyAfter = DefaultHubUnhealthyAfter
	}
	if now == nil {
		now = time.Now
	}
	return &hubHealthTracker{
		probe:          probe,
		interval:       interval,
		unhealthyAfter: unhealthyAfter,
		now:            now,
	}
}

func (h *hubHealthTracker) checkOnce(ctx context.Context) {
	timeout := h.interval
	if timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	err := h.probe(probeCtx)
	cancel()

	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil {
		h.failingSince = time.Time{}
		return
	}
	if h.failingSince.IsZero() {
		h.failingSince = now
	}
}

func (h *hubHealthTracker) isReady() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failingSince.IsZero() {
		return true
	}
	return h.now().Sub(h.failingSince) < h.unhealthyAfter
}

func (h *hubHealthTracker) run(ctx context.Context) {
	h.checkOnce(ctx)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.checkOnce(ctx)
		}
	}
}
