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
	"embed"
	"encoding/json"
	"net/rpc"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	goplugin "github.com/hashicorp/go-plugin"
)

// HarnessPluginName is the name used to dispense harness plugins via go-plugin.
const HarnessPluginName = "harness"

// --- RPC argument/response types ---

// HarnessGetEnvArgs holds arguments for the GetEnv RPC call.
type HarnessGetEnvArgs struct {
	AgentName    string
	AgentHome    string
	UnixUsername string
}

// HarnessGetCommandArgs holds arguments for the GetCommand RPC call.
type HarnessGetCommandArgs struct {
	Task     string
	Resume   bool
	BaseArgs []string
}

// HarnessProvisionArgs holds arguments for the Provision RPC call.
type HarnessProvisionArgs struct {
	AgentName      string
	AgentDir       string
	AgentHome      string
	AgentWorkspace string
}

// HarnessInjectArgs holds arguments for InjectAgentInstructions/InjectSystemPrompt RPC calls.
type HarnessInjectArgs struct {
	AgentHome string
	Content   []byte
}

// HarnessResolveAuthArgs holds arguments for ResolveAuth RPC call.
// AuthConfig is serialized as JSON since it has many fields.
type HarnessResolveAuthArgs struct {
	AuthConfigJSON []byte
}

// HarnessResolveAuthResponse holds the response from ResolveAuth RPC call.
type HarnessResolveAuthResponse struct {
	ResolvedAuthJSON []byte
}

// HarnessMetadata bundles the simple getter responses to reduce RPC round-trips.
type HarnessMetadata struct {
	Name                 string
	AdvancedCapabilities api.HarnessAdvancedCapabilities
	DefaultConfigDir     string
	SkillsDir            string
	EmbedDir             string
	InterruptKey         string
	TelemetryEnv         map[string]string
	Capabilities         []string // optional interface support: "auth_settings", "telemetry_settings"
}

// HarnessApplyAuthSettingsArgs holds arguments for ApplyAuthSettings RPC call.
type HarnessApplyAuthSettingsArgs struct {
	AgentHome        string
	ResolvedAuthJSON []byte
}

// HarnessApplyTelemetrySettingsArgs holds arguments for ApplyTelemetrySettings RPC call.
type HarnessApplyTelemetrySettingsArgs struct {
	AgentHome     string
	TelemetryJSON []byte
	Env           map[string]string
}

// --- go-plugin Plugin definition ---

// HarnessPlugin implements hashicorp/go-plugin's Plugin interface for harness plugins.
type HarnessPlugin struct {
	// Impl is set only on the plugin side (the server).
	Impl api.Harness
}

func (p *HarnessPlugin) Server(*goplugin.MuxBroker) (interface{}, error) {
	return &HarnessRPCServer{Impl: p.Impl}, nil
}

func (p *HarnessPlugin) Client(_ *goplugin.MuxBroker, c *rpc.Client) (interface{}, error) {
	return &HarnessRPCClient{client: c}, nil
}

// --- RPC Server (runs in the plugin process) ---

// HarnessRPCServer wraps a Harness implementation to serve RPC requests.
type HarnessRPCServer struct {
	Impl api.Harness
}

func (s *HarnessRPCServer) GetMetadata(_ struct{}, resp *HarnessMetadata) error {
	resp.Name = s.Impl.Name()
	resp.AdvancedCapabilities = s.Impl.AdvancedCapabilities()
	resp.DefaultConfigDir = s.Impl.DefaultConfigDir()
	resp.SkillsDir = s.Impl.SkillsDir()
	resp.EmbedDir = s.Impl.GetEmbedDir()
	resp.InterruptKey = s.Impl.GetInterruptKey()
	resp.TelemetryEnv = s.Impl.GetTelemetryEnv()

	if _, ok := s.Impl.(api.AuthSettingsApplier); ok {
		resp.Capabilities = append(resp.Capabilities, "auth_settings")
	}
	if _, ok := s.Impl.(api.TelemetrySettingsApplier); ok {
		resp.Capabilities = append(resp.Capabilities, "telemetry_settings")
	}
	return nil
}

func (s *HarnessRPCServer) GetEnv(args *HarnessGetEnvArgs, resp *map[string]string) error {
	*resp = s.Impl.GetEnv(args.AgentName, args.AgentHome, args.UnixUsername)
	return nil
}

func (s *HarnessRPCServer) GetCommand(args *HarnessGetCommandArgs, resp *[]string) error {
	*resp = s.Impl.GetCommand(args.Task, args.Resume, args.BaseArgs)
	return nil
}

func (s *HarnessRPCServer) HasSystemPrompt(agentHome *string, resp *bool) error {
	*resp = s.Impl.HasSystemPrompt(*agentHome)
	return nil
}

func (s *HarnessRPCServer) Provision(args *HarnessProvisionArgs, _ *struct{}) error {
	return s.Impl.Provision(context.Background(), args.AgentName, args.AgentDir, args.AgentHome, args.AgentWorkspace)
}

func (s *HarnessRPCServer) InjectAgentInstructions(args *HarnessInjectArgs, _ *struct{}) error {
	return s.Impl.InjectAgentInstructions(args.AgentHome, args.Content)
}

func (s *HarnessRPCServer) InjectSystemPrompt(args *HarnessInjectArgs, _ *struct{}) error {
	return s.Impl.InjectSystemPrompt(args.AgentHome, args.Content)
}

func (s *HarnessRPCServer) ResolveAuth(args *HarnessResolveAuthArgs, resp *HarnessResolveAuthResponse) error {
	var authCfg api.AuthConfig
	if err := json.Unmarshal(args.AuthConfigJSON, &authCfg); err != nil {
		return err
	}
	resolved, err := s.Impl.ResolveAuth(authCfg)
	if err != nil {
		return err
	}
	data, err := json.Marshal(resolved)
	if err != nil {
		return err
	}
	resp.ResolvedAuthJSON = data
	return nil
}

func (s *HarnessRPCServer) ApplyAuthSettings(args *HarnessApplyAuthSettingsArgs, _ *struct{}) error {
	applier, ok := s.Impl.(api.AuthSettingsApplier)
	if !ok {
		return nil
	}
	var resolved api.ResolvedAuth
	if err := json.Unmarshal(args.ResolvedAuthJSON, &resolved); err != nil {
		return err
	}
	return applier.ApplyAuthSettings(args.AgentHome, &resolved)
}

func (s *HarnessRPCServer) ApplyTelemetrySettings(args *HarnessApplyTelemetrySettingsArgs, _ *struct{}) error {
	applier, ok := s.Impl.(api.TelemetrySettingsApplier)
	if !ok {
		return nil
	}
	var telemetry api.TelemetryConfig
	if err := json.Unmarshal(args.TelemetryJSON, &telemetry); err != nil {
		return err
	}
	return applier.ApplyTelemetrySettings(args.AgentHome, &telemetry, args.Env)
}

func (s *HarnessRPCServer) GetInfo(_ struct{}, resp *GetInfoResponse) error {
	resp.Info = PluginInfo{
		Name: s.Impl.Name(),
	}
	return nil
}

// --- RPC Client (runs in the host process) ---

// HarnessRPCClient implements api.Harness by making RPC calls to the plugin process.
type HarnessRPCClient struct {
	client   *rpc.Client
	metadata *HarnessMetadata // cached after first call
}

func (c *HarnessRPCClient) getMetadata() (*HarnessMetadata, error) {
	if c.metadata != nil {
		return c.metadata, nil
	}
	var meta HarnessMetadata
	if err := c.client.Call("Plugin.GetMetadata", struct{}{}, &meta); err != nil {
		return nil, err
	}
	c.metadata = &meta
	return c.metadata, nil
}

func (c *HarnessRPCClient) Name() string {
	meta, err := c.getMetadata()
	if err != nil {
		return "unknown"
	}
	return meta.Name
}

func (c *HarnessRPCClient) AdvancedCapabilities() api.HarnessAdvancedCapabilities {
	meta, err := c.getMetadata()
	if err != nil {
		return api.HarnessAdvancedCapabilities{}
	}
	return meta.AdvancedCapabilities
}

func (c *HarnessRPCClient) GetEnv(agentName, agentHome, unixUsername string) map[string]string {
	var resp map[string]string
	err := c.client.Call("Plugin.GetEnv", &HarnessGetEnvArgs{
		AgentName:    agentName,
		AgentHome:    agentHome,
		UnixUsername: unixUsername,
	}, &resp)
	if err != nil {
		return nil
	}
	return resp
}

func (c *HarnessRPCClient) GetCommand(task string, resume bool, baseArgs []string) []string {
	var resp []string
	err := c.client.Call("Plugin.GetCommand", &HarnessGetCommandArgs{
		Task:     task,
		Resume:   resume,
		BaseArgs: baseArgs,
	}, &resp)
	if err != nil {
		return nil
	}
	return resp
}

func (c *HarnessRPCClient) DefaultConfigDir() string {
	meta, err := c.getMetadata()
	if err != nil {
		return ""
	}
	return meta.DefaultConfigDir
}

func (c *HarnessRPCClient) SkillsDir() string {
	meta, err := c.getMetadata()
	if err != nil {
		return ""
	}
	return meta.SkillsDir
}

func (c *HarnessRPCClient) HasSystemPrompt(agentHome string) bool {
	var resp bool
	if err := c.client.Call("Plugin.HasSystemPrompt", &agentHome, &resp); err != nil {
		return false
	}
	return resp
}

func (c *HarnessRPCClient) Provision(ctx context.Context, agentName, agentDir, agentHome, agentWorkspace string) error {
	return c.client.Call("Plugin.Provision", &HarnessProvisionArgs{
		AgentName:      agentName,
		AgentDir:       agentDir,
		AgentHome:      agentHome,
		AgentWorkspace: agentWorkspace,
	}, &struct{}{})
}

func (c *HarnessRPCClient) GetEmbedDir() string {
	meta, err := c.getMetadata()
	if err != nil {
		return ""
	}
	return meta.EmbedDir
}

func (c *HarnessRPCClient) GetInterruptKey() string {
	meta, err := c.getMetadata()
	if err != nil {
		return ""
	}
	return meta.InterruptKey
}

// GetHarnessEmbedsFS returns nil for plugin harnesses.
// Plugin harnesses write their embedded files directly during Provision().
func (c *HarnessRPCClient) GetHarnessEmbedsFS() (embed.FS, string) {
	return embed.FS{}, ""
}

func (c *HarnessRPCClient) InjectAgentInstructions(agentHome string, content []byte) error {
	return c.client.Call("Plugin.InjectAgentInstructions", &HarnessInjectArgs{
		AgentHome: agentHome,
		Content:   content,
	}, &struct{}{})
}

func (c *HarnessRPCClient) InjectSystemPrompt(agentHome string, content []byte) error {
	return c.client.Call("Plugin.InjectSystemPrompt", &HarnessInjectArgs{
		AgentHome: agentHome,
		Content:   content,
	}, &struct{}{})
}

func (c *HarnessRPCClient) GetTelemetryEnv() map[string]string {
	meta, err := c.getMetadata()
	if err != nil {
		return nil
	}
	return meta.TelemetryEnv
}

func (c *HarnessRPCClient) ResolveAuth(auth api.AuthConfig) (*api.ResolvedAuth, error) {
	data, err := json.Marshal(auth)
	if err != nil {
		return nil, err
	}
	var resp HarnessResolveAuthResponse
	if err := c.client.Call("Plugin.ResolveAuth", &HarnessResolveAuthArgs{
		AuthConfigJSON: data,
	}, &resp); err != nil {
		return nil, err
	}
	var resolved api.ResolvedAuth
	if err := json.Unmarshal(resp.ResolvedAuthJSON, &resolved); err != nil {
		return nil, err
	}
	return &resolved, nil
}

func (c *HarnessRPCClient) hasCapability(name string) bool {
	meta, err := c.getMetadata()
	if err != nil {
		return false
	}
	for _, cap := range meta.Capabilities {
		if cap == name {
			return true
		}
	}
	return false
}

// ApplyAuthSettings implements api.AuthSettingsApplier if the plugin supports it.
func (c *HarnessRPCClient) ApplyAuthSettings(agentHome string, resolved *api.ResolvedAuth) error {
	if !c.hasCapability("auth_settings") {
		return nil
	}
	data, err := json.Marshal(resolved)
	if err != nil {
		return err
	}
	return c.client.Call("Plugin.ApplyAuthSettings", &HarnessApplyAuthSettingsArgs{
		AgentHome:        agentHome,
		ResolvedAuthJSON: data,
	}, &struct{}{})
}

// ApplyTelemetrySettings implements api.TelemetrySettingsApplier if the plugin supports it.
func (c *HarnessRPCClient) ApplyTelemetrySettings(agentHome string, telemetry *api.TelemetryConfig, env map[string]string) error {
	if !c.hasCapability("telemetry_settings") {
		return nil
	}
	data, err := json.Marshal(telemetry)
	if err != nil {
		return err
	}
	return c.client.Call("Plugin.ApplyTelemetrySettings", &HarnessApplyTelemetrySettingsArgs{
		AgentHome:     agentHome,
		TelemetryJSON: data,
		Env:           env,
	}, &struct{}{})
}

// GetInfo returns plugin metadata.
func (c *HarnessRPCClient) GetInfo() (*PluginInfo, error) {
	var resp GetInfoResponse
	err := c.client.Call("Plugin.GetInfo", struct{}{}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp.Info, nil
}
