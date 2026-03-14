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

package plugin

import (
	"context"
	"fmt"
	"net/rpc"

	"github.com/GoogleCloudPlatform/scion/pkg/broker"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	goplugin "github.com/hashicorp/go-plugin"
)

// BrokerPluginName is the name used to dispense broker plugins via go-plugin.
const BrokerPluginName = "broker"

// --- RPC argument/response types ---

// ConfigureArgs holds arguments for the Configure RPC call.
type ConfigureArgs struct {
	Config map[string]string
}

// PublishArgs holds arguments for the Publish RPC call.
type PublishArgs struct {
	Topic string
	Msg   *messages.StructuredMessage
}

// SubscribeArgs holds arguments for the Subscribe RPC call.
type SubscribeArgs struct {
	Pattern string
}

// UnsubscribeArgs holds arguments for the Unsubscribe RPC call.
type UnsubscribeArgs struct {
	Pattern string
}

// GetInfoResponse holds the response from GetInfo RPC call.
type GetInfoResponse struct {
	Info PluginInfo
}

// --- Plugin interface (implemented by the plugin binary) ---

// MessageBrokerPluginInterface defines the methods that a broker plugin must implement.
// This is the interface that plugin authors implement on the plugin side.
type MessageBrokerPluginInterface interface {
	Configure(config map[string]string) error
	Publish(ctx context.Context, topic string, msg *messages.StructuredMessage) error
	Subscribe(pattern string) error
	Unsubscribe(pattern string) error
	Close() error
	GetInfo() (*PluginInfo, error)
}

// --- go-plugin Plugin definition ---

// BrokerPlugin implements hashicorp/go-plugin's Plugin interface for broker plugins.
// It defines how to create the RPC client and server for broker plugin communication.
type BrokerPlugin struct {
	// Impl is set only on the plugin side (the server).
	Impl MessageBrokerPluginInterface
}

func (p *BrokerPlugin) Server(*goplugin.MuxBroker) (interface{}, error) {
	return &BrokerRPCServer{Impl: p.Impl}, nil
}

func (p *BrokerPlugin) Client(_ *goplugin.MuxBroker, c *rpc.Client) (interface{}, error) {
	return &BrokerRPCClient{client: c}, nil
}

// --- RPC Server (runs in the plugin process) ---

// BrokerRPCServer wraps a MessageBrokerPluginInterface to serve RPC requests.
// This runs inside the plugin binary.
type BrokerRPCServer struct {
	Impl MessageBrokerPluginInterface
}

func (s *BrokerRPCServer) Configure(args *ConfigureArgs, _ *struct{}) error {
	return s.Impl.Configure(args.Config)
}

func (s *BrokerRPCServer) Publish(args *PublishArgs, _ *struct{}) error {
	return s.Impl.Publish(context.Background(), args.Topic, args.Msg)
}

func (s *BrokerRPCServer) Subscribe(args *SubscribeArgs, _ *struct{}) error {
	return s.Impl.Subscribe(args.Pattern)
}

func (s *BrokerRPCServer) Unsubscribe(args *UnsubscribeArgs, _ *struct{}) error {
	return s.Impl.Unsubscribe(args.Pattern)
}

func (s *BrokerRPCServer) Close(_ struct{}, _ *struct{}) error {
	return s.Impl.Close()
}

func (s *BrokerRPCServer) GetInfo(_ struct{}, resp *GetInfoResponse) error {
	info, err := s.Impl.GetInfo()
	if err != nil {
		return err
	}
	if info != nil {
		resp.Info = *info
	}
	return nil
}

// --- RPC Client (runs in the host process) ---

// BrokerRPCClient implements MessageBrokerPluginInterface by making RPC calls
// to the plugin process. This is used on the host side.
type BrokerRPCClient struct {
	client *rpc.Client
}

func (c *BrokerRPCClient) Configure(config map[string]string) error {
	return c.client.Call("Plugin.Configure", &ConfigureArgs{Config: config}, &struct{}{})
}

func (c *BrokerRPCClient) Publish(_ context.Context, topic string, msg *messages.StructuredMessage) error {
	return c.client.Call("Plugin.Publish", &PublishArgs{Topic: topic, Msg: msg}, &struct{}{})
}

func (c *BrokerRPCClient) Subscribe(pattern string) error {
	return c.client.Call("Plugin.Subscribe", &SubscribeArgs{Pattern: pattern}, &struct{}{})
}

func (c *BrokerRPCClient) Unsubscribe(pattern string) error {
	return c.client.Call("Plugin.Unsubscribe", &UnsubscribeArgs{Pattern: pattern}, &struct{}{})
}

func (c *BrokerRPCClient) Close() error {
	return c.client.Call("Plugin.Close", struct{}{}, &struct{}{})
}

func (c *BrokerRPCClient) GetInfo() (*PluginInfo, error) {
	var resp GetInfoResponse
	err := c.client.Call("Plugin.GetInfo", struct{}{}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp.Info, nil
}

// --- Host-side adapter: wraps BrokerRPCClient as broker.MessageBroker ---

// BrokerPluginAdapter wraps a BrokerRPCClient to satisfy the broker.MessageBroker interface.
// Subscribe's MessageHandler callback is not forwarded to the plugin — inbound messages
// arrive via the hub API instead (see broker-plugins.md design doc).
type BrokerPluginAdapter struct {
	rpcClient *BrokerRPCClient
	subs      map[string]*pluginSubscription
}

// NewBrokerPluginAdapter creates a new adapter wrapping the given RPC client.
func NewBrokerPluginAdapter(client *BrokerRPCClient) *BrokerPluginAdapter {
	return &BrokerPluginAdapter{
		rpcClient: client,
		subs:      make(map[string]*pluginSubscription),
	}
}

func (a *BrokerPluginAdapter) Publish(ctx context.Context, topic string, msg *messages.StructuredMessage) error {
	return a.rpcClient.Publish(ctx, topic, msg)
}

// Subscribe tells the plugin to start listening on the external broker for the given pattern.
// The handler callback is stored locally but not forwarded — inbound delivery happens
// via the hub API endpoint (POST /api/v1/broker/inbound).
func (a *BrokerPluginAdapter) Subscribe(pattern string, handler broker.MessageHandler) (broker.Subscription, error) {
	if err := a.rpcClient.Subscribe(pattern); err != nil {
		return nil, fmt.Errorf("plugin subscribe failed: %w", err)
	}
	sub := &pluginSubscription{
		adapter: a,
		pattern: pattern,
	}
	a.subs[pattern] = sub
	return sub, nil
}

func (a *BrokerPluginAdapter) Close() error {
	return a.rpcClient.Close()
}

// pluginSubscription implements broker.Subscription for plugin brokers.
type pluginSubscription struct {
	adapter *BrokerPluginAdapter
	pattern string
}

func (s *pluginSubscription) Unsubscribe() error {
	if err := s.adapter.rpcClient.Unsubscribe(s.pattern); err != nil {
		return err
	}
	delete(s.adapter.subs, s.pattern)
	return nil
}
