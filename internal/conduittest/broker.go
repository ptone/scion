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

package conduittest

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/pkg/agentcontrol"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/conduit"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/runtimebroker"
)

// BrokerOptions configures a fake Conduit broker.
type BrokerOptions struct {
	HubURL          string
	BrokerID        string
	BrokerSecret    []byte
	Version         string
	Incarnation     string
	RPCHandler      conduit.RPCHandler
	Session         conduit.Config
	Rand            func(int64) int64
	HTTPClient      *http.Client
	TLSClientConfig *tls.Config
	// ControlChannelHandler, when non-nil, also connects a real
	// runtimebroker.ControlChannelClient to the Hub's control channel
	// (/api/v1/runtime-brokers/connect) so durable Hub->broker operations such
	// as POST /api/v1/agents/{slug}/message are dispatched over the product
	// control channel alongside the Conduit session.
	ControlChannelHandler http.Handler
}

// Broker is a fake broker wired with the real runtimebroker.ConduitDialer, an
// optional real runtimebroker.ControlChannelClient, and a real
// HMAC-authenticated hubclient.Client (the same HTTP client path
// hubConduitDeliverer calls).
type Broker struct {
	BrokerID       string
	Dialer         *runtimebroker.ConduitDialer
	ControlChannel *runtimebroker.ControlChannelClient
	HubClient      hubclient.Client
	ws             *FaultWSDialer
	ccConnected    atomic.Bool

	stopOnce sync.Once
	stopFn   func() error
}

// StartBroker starts a fake broker dialing opts.HubURL. Call WaitHealthy to
// wait until the conduit command path (and control channel, if configured)
// becomes healthy, and Close to stop it.
func StartBroker(opts BrokerOptions) (*Broker, error) {
	version := opts.Version
	if version == "" {
		version = "test"
	}
	incarnation := opts.Incarnation
	if incarnation == "" {
		incarnation = "broker-inc-" + uuid.NewString()[:8]
	}
	randFn := opts.Rand
	if randFn == nil {
		randFn = func(int64) int64 { return 0 }
	}

	wsDialer := NewFaultWSDialer()
	if opts.TLSClientConfig != nil {
		wsDialer.Dialer().TLSClientConfig = opts.TLSClientConfig
	}
	d, err := runtimebroker.NewConduitDialer(runtimebroker.ConduitDialConfig{
		HubEndpoint: opts.HubURL,
		BrokerID:    opts.BrokerID,
		SecretKey:   opts.BrokerSecret,
		Version:     version,
		Incarnation: incarnation,
		RPCHandler:  opts.RPCHandler,
		Session:     opts.Session,
		WS:          wsDialer.Dialer(),
		Rand:        randFn,
	})
	if err != nil {
		return nil, fmt.Errorf("conduittest: NewConduitDialer: %w", err)
	}

	hcOpts := []hubclient.Option{hubclient.WithHMACAuth(opts.BrokerID, opts.BrokerSecret)}
	if opts.HTTPClient != nil {
		hcOpts = append(hcOpts, hubclient.WithHTTPClient(opts.HTTPClient))
	}
	hc, err := hubclient.New(opts.HubURL, hcOpts...)
	if err != nil {
		return nil, fmt.Errorf("conduittest: hubclient.New: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = d.Run(runCtx)
	}()

	fb := &Broker{
		BrokerID:  opts.BrokerID,
		Dialer:    d,
		HubClient: hc,
		ws:        wsDialer,
	}

	var ccExited chan struct{}
	if opts.ControlChannelHandler != nil {
		ccCfg := runtimebroker.DefaultControlChannelConfig()
		ccCfg.HubEndpoint = opts.HubURL
		ccCfg.BrokerID = opts.BrokerID
		ccCfg.SecretKey = opts.BrokerSecret
		ccCfg.Version = version
		ccCfg.ReconnectInitial = 100 * time.Millisecond
		ccCfg.ReconnectMax = time.Second
		ccCfg.OnConnectionStateChange = func(connected bool) {
			fb.ccConnected.Store(connected)
		}
		cc := runtimebroker.NewControlChannelClient(ccCfg, opts.ControlChannelHandler, nil, "conduittest", slog.Default())
		fb.ControlChannel = cc
		ccExited = make(chan struct{})
		go func() {
			defer close(ccExited)
			_ = cc.Connect(runCtx)
		}()
	}

	fb.stopFn = func() error {
		var stopErr error
		fb.stopOnce.Do(func() {
			wsDialer.Restore()
			cancel()
			wsDialer.CloseCurrent()
			if fb.ControlChannel != nil {
				_ = fb.ControlChannel.Close()
			}
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				stopErr = fmt.Errorf("conduittest: fake broker %s did not stop within 10s", opts.BrokerID)
			}
			if ccExited != nil {
				select {
				case <-ccExited:
				case <-time.After(10 * time.Second):
					if stopErr == nil {
						stopErr = fmt.Errorf("conduittest: fake broker %s control channel did not stop within 10s", opts.BrokerID)
					}
				}
			}
		})
		return stopErr
	}
	return fb, nil
}

// WaitHealthy polls until the broker's conduit command path reaches healthy
// state (and the control channel is connected, if configured) or ctx expires.
func (b *Broker) WaitHealthy(ctx context.Context) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		conduitOK := b.Dialer.CommandPathState() == api.BrokerHealthHealthy
		ccOK := b.ControlChannel == nil || b.ccConnected.Load()
		if conduitOK && ccOK {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("broker %s command path state=%q cc_connected=%v: %w",
				b.BrokerID, b.Dialer.CommandPathState(), b.ccConnected.Load(), ctx.Err())
		case <-tick.C:
		}
	}
}

// DeliverMessage sends a deliver-message flush over the real broker->hub HTTP
// endpoint (POST /api/v1/runtime-brokers/{brokerId}/agents/{agentId}/deliver).
func (b *Broker) DeliverMessage(ctx context.Context, agentID string, req *agentcontrol.DeliverRequest) (int, agentcontrol.DeliverResponse, error) {
	return b.HubClient.RuntimeBrokers().DeliverMessage(ctx, b.BrokerID, agentID, req)
}

// Close stops the fake broker cleanly.
func (b *Broker) Close() error { return b.stopFn() }

// CloseSession abruptly closes the broker's current WebSocket TCP connection.
func (b *Broker) CloseSession() { b.ws.CloseCurrent() }

// SilentDrop stalls reads and writes on the broker's WebSocket TCP conn without
// sending a close frame. Call Restore to resume and deliver pending frames.
func (b *Broker) SilentDrop() { b.ws.SilentDrop() }

// Restore resumes reads and writes on the broker's WebSocket TCP conn.
func (b *Broker) Restore() { b.ws.Restore() }
