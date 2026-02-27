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

// Package runtimebroker provides the Scion Runtime Broker API server.
// The Runtime Broker API exposes agent lifecycle management over HTTP,
// allowing the Scion Hub to remotely manage agents on this compute node.
package runtimebroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ptone/scion-agent/pkg/agent"
	"github.com/ptone/scion-agent/pkg/brokercredentials"
	"github.com/ptone/scion-agent/pkg/config"
	"github.com/ptone/scion-agent/pkg/hubclient"
	"github.com/ptone/scion-agent/pkg/runtime"
	"github.com/ptone/scion-agent/pkg/util"
	"github.com/ptone/scion-agent/pkg/templatecache"
	"github.com/ptone/scion-agent/pkg/util/logging"
)

// ServerConfig holds configuration for the Runtime Broker API server.
type ServerConfig struct {
	// Port is the HTTP port to listen on.
	Port int
	// Host is the address to bind to (e.g., "0.0.0.0" or "127.0.0.1").
	Host string
	// ReadTimeout is the maximum duration for reading the entire request.
	ReadTimeout time.Duration
	// WriteTimeout is the maximum duration before timing out writes.
	WriteTimeout time.Duration

	// HubEndpoint is the Hub API endpoint for reporting (optional).
	HubEndpoint string
	// ContainerHubEndpoint overrides HubEndpoint when injecting the Hub URL
	// into agent containers. Used for local development where containers
	// need a bridge address (e.g. host.containers.internal) instead of localhost.
	ContainerHubEndpoint string

	// BrokerID is a unique identifier for this runtime broker.
	BrokerID string
	// BrokerName is a human-readable name for this runtime broker.
	BrokerName string

	// CORS settings
	CORSEnabled        bool
	CORSAllowedOrigins []string
	CORSAllowedMethods []string
	CORSAllowedHeaders []string
	CORSMaxAge         int

	// Debug enables verbose debug logging.
	Debug bool

	// Hub integration settings
	// HubEnabled indicates whether this Runtime Broker should connect to a Hub
	// for template hydration and other centralized services.
	HubEnabled bool
	// HubToken is the authentication token for the Hub API.
	HubToken string

	// Template cache settings
	// TemplateCacheDir is the directory for caching templates fetched from the Hub.
	// Defaults to ~/.scion/cache/templates if not specified.
	TemplateCacheDir string
	// TemplateCacheMaxSize is the maximum size of the template cache in bytes.
	// Defaults to 100MB if not specified.
	TemplateCacheMaxSize int64

	// Broker credentials settings
	// BrokerCredentialsPath is the path to the broker credentials file.
	// If set, HMAC authentication will be used instead of bearer tokens.
	// Defaults to ~/.scion/broker-credentials.json if not specified.
	BrokerCredentialsPath string

	// InMemoryCredentials allows injecting credentials directly without a file.
	// Used for co-located Hub+RuntimeBroker mode where credentials are generated
	// in-memory and shared between the Hub and RuntimeBroker in the same process.
	// Takes precedence over BrokerCredentialsPath if set.
	InMemoryCredentials *brokercredentials.BrokerCredentials

	// BrokerAuthEnabled enables HMAC verification for incoming requests from the Hub.
	BrokerAuthEnabled bool
	// BrokerAuthStrictMode, when true, requires all requests to be authenticated.
	// When false (default), unauthenticated requests are allowed for transition periods.
	BrokerAuthStrictMode bool

	// Heartbeat settings
	// HeartbeatEnabled enables periodic heartbeats to the Hub.
	HeartbeatEnabled bool
	// HeartbeatInterval is the time between heartbeats.
	// Defaults to 30 seconds if not specified.
	HeartbeatInterval time.Duration

	// Control channel settings
	// ControlChannelEnabled enables the WebSocket control channel to the Hub.
	// This allows NAT traversal for brokers behind firewalls.
	ControlChannelEnabled bool

	// Workspace sync settings
	// StorageBucket is the GCS bucket name for workspace storage.
	// Used when workspace sync requests don't specify a bucket.
	StorageBucket string
	// WorktreeBase is the base directory for agent worktrees.
	// Used as a fallback when resolving workspace paths.
	WorktreeBase string
}

// DefaultServerConfig returns the default server configuration.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Port:               9800,
		Host:               "0.0.0.0",
		ReadTimeout:        30 * time.Second,
		WriteTimeout:       120 * time.Second,
		CORSEnabled:        true,
		CORSAllowedOrigins: []string{"*"},
		CORSAllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		CORSAllowedHeaders: []string{"Authorization", "Content-Type", "X-Scion-Broker-Token", "X-API-Key", "X-Scion-Broker-ID", "X-Scion-Timestamp", "X-Scion-Nonce", "X-Scion-Signature", "X-Scion-Signed-Headers"},
		CORSMaxAge:         3600,
	}
}

// Server is the Runtime Broker API HTTP server.
type Server struct {
	config     ServerConfig
	manager    agent.Manager
	runtime    runtime.Runtime
	httpServer *http.Server
	mux        *http.ServeMux
	mu         sync.RWMutex
	startTime  time.Time
	version    string

	// Hub connections (replaces single hubClient, heartbeat, controlChannel, etc.)
	hubConnections map[string]*HubConnection // keyed by connection name
	hubMu          sync.RWMutex

	// Shared template cache (content-addressed, hub-neutral)
	cache *templatecache.Cache

	// Multi-key auth middleware
	brokerAuthMiddleware *MultiKeyBrokerAuthMiddleware

	// Credential watching (watches MultiStore directory)
	multiCredStore  *brokercredentials.MultiStore
	credLastScan    time.Time
	credWatcherStop chan struct{}

	// Pending env-gather state: agents waiting for env var submission.
	// Keyed by agent name (used as agent identifier on the broker).
	pendingEnvGather   map[string]*pendingAgentState
	pendingEnvGatherMu sync.Mutex
}

// pendingAgentState holds the partial state for an agent waiting on env-gather.
type pendingAgentState struct {
	Request   *CreateAgentRequest
	MergedEnv map[string]string
	CreatedAt time.Time
}

// New creates a new Runtime Broker API server.
func New(cfg ServerConfig, mgr agent.Manager, rt runtime.Runtime) *Server {
	// Enable util debug logging when broker debug mode is on,
	// so that debug messages from pkg/agent (which use util.Debugf)
	// are visible in the broker's logs.
	if cfg.Debug {
		util.EnableDebug()
	}

	srv := &Server{
		config:         cfg,
		manager:        mgr,
		runtime:        rt,
		mux:            http.NewServeMux(),
		startTime:      time.Now(),
		version:        "0.1.0", // TODO: Get from build info
		hubConnections: make(map[string]*HubConnection),
		pendingEnvGather: make(map[string]*pendingAgentState),
	}

	// Initialize Hub integration if enabled
	if cfg.HubEnabled && (cfg.HubEndpoint != "" || cfg.InMemoryCredentials != nil) {
		if err := srv.initHubIntegration(); err != nil {
			slog.Warn("Failed to initialize Hub integration", "error", err)
		}
	}

	srv.registerRoutes()

	return srv
}

// initHubIntegration initializes the shared template cache and hub connections.
func (s *Server) initHubIntegration() error {
	// 1. Initialize shared template cache
	cacheDir := s.config.TemplateCacheDir
	if cacheDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		cacheDir = filepath.Join(homeDir, ".scion", "cache", "templates")
	}

	maxSize := s.config.TemplateCacheMaxSize
	if maxSize <= 0 {
		maxSize = templatecache.DefaultMaxSize
	}

	cache, err := templatecache.New(cacheDir, maxSize)
	if err != nil {
		return fmt.Errorf("failed to initialize template cache: %w", err)
	}
	s.cache = cache

	// 2. Initialize hub connections map (already done in New)

	// 3. Handle InMemoryCredentials -> "local" connection (co-located mode)
	if s.config.InMemoryCredentials != nil {
		creds := s.config.InMemoryCredentials
		if creds.Name == "" {
			creds.Name = "local"
		}
		conn, err := s.createHubConnection(creds.Name, creds)
		if err != nil {
			slog.Warn("Failed to create local hub connection", "error", err)
		} else {
			// Mark as co-located so heartbeat is handled by the internal DB loop
			// instead of the HTTP heartbeat service.
			conn.IsColocated = true
			s.hubMu.Lock()
			s.hubConnections[creds.Name] = conn
			s.hubMu.Unlock()
			slog.Info("Created local hub connection (co-located mode)", "name", creds.Name, "brokerID", creds.BrokerID)
		}
	}

	// 4. Load MultiStore credentials
	s.multiCredStore = brokercredentials.NewMultiStore("")
	multiCreds, err := s.multiCredStore.List()
	if err != nil {
		slog.Warn("Failed to list multi-store credentials", "error", err)
	}

	for i := range multiCreds {
		c := &multiCreds[i]
		// Skip if already handled by InMemoryCredentials
		if _, exists := s.hubConnections[c.Name]; exists {
			continue
		}
		conn, err := s.createHubConnection(c.Name, c)
		if err != nil {
			slog.Warn("Failed to create hub connection", "name", c.Name, "error", err)
			continue
		}
		s.hubMu.Lock()
		s.hubConnections[c.Name] = conn
		s.hubMu.Unlock()
		slog.Info("Created hub connection from multi-store", "name", c.Name, "brokerID", c.BrokerID)
	}

	// 5. Legacy fallback: if no connections yet (except possibly "local"),
	// try loading from the legacy single-file Store
	if len(s.hubConnections) == 0 || (len(s.hubConnections) == 1 && s.config.InMemoryCredentials != nil) {
		s.tryLegacyCredentials()
	}

	// If we still have no connections, try creating one from config (bearer/dev-auth)
	if len(s.hubConnections) == 0 && s.config.HubEndpoint != "" {
		conn, err := s.createHubConnectionFromConfig()
		if err != nil {
			slog.Warn("Failed to create hub connection from config", "error", err)
		} else {
			name := brokercredentials.DeriveHubName(s.config.HubEndpoint)
			if name == "" {
				name = "default"
			}
			s.hubMu.Lock()
			s.hubConnections[name] = conn
			s.hubMu.Unlock()
		}
	}

	// 6. Build multi-key auth middleware from all connections' secret keys
	s.buildAuthMiddleware()

	// Update BrokerID from first connection if not already set
	if s.config.BrokerID == "" {
		s.hubMu.RLock()
		for _, conn := range s.hubConnections {
			if conn.BrokerID != "" {
				s.config.BrokerID = conn.BrokerID
				break
			}
		}
		s.hubMu.RUnlock()
	}

	slog.Info("Hub integration initialized",
		"connections", len(s.hubConnections),
		"cache", cacheDir,
		"max_size_mb", maxSize/(1024*1024),
	)

	return nil
}

// createHubConnection creates a HubConnection from credentials.
func (s *Server) createHubConnection(name string, creds *brokercredentials.BrokerCredentials) (*HubConnection, error) {
	// Decode secret key
	var secretKey []byte
	if creds.SecretKey != "" {
		var err error
		secretKey, err = base64.StdEncoding.DecodeString(creds.SecretKey)
		if err != nil {
			return nil, fmt.Errorf("failed to decode secret key: %w", err)
		}
	}

	// Determine hub endpoint
	hubEndpoint := creds.HubEndpoint
	if hubEndpoint == "" {
		hubEndpoint = s.config.HubEndpoint
	}

	// Build hub client options
	opts := buildHubClientOpts(creds, secretKey)
	client, err := hubclient.New(hubEndpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Hub client: %w", err)
	}

	// Create hydrator using shared cache
	var hydrator *templatecache.Hydrator
	if s.cache != nil {
		hydrator = templatecache.NewHydrator(s.cache, client)
	}

	conn := &HubConnection{
		Name:        name,
		HubEndpoint: hubEndpoint,
		BrokerID:    creds.BrokerID,
		AuthMode:    creds.AuthMode,
		Credentials: creds,
		SecretKey:   secretKey,
		HubClient:   client,
		Hydrator:    hydrator,
		Status:      ConnectionStatusDisconnected,
	}

	return conn, nil
}

// createHubConnectionFromConfig creates a HubConnection from server config
// (bearer token or dev-auth), without file-based credentials.
func (s *Server) createHubConnectionFromConfig() (*HubConnection, error) {
	var opts []hubclient.Option

	if s.config.HubToken != "" {
		opts = append(opts, hubclient.WithBearerToken(s.config.HubToken))
		slog.Info("Hub client using bearer token authentication")
	} else {
		opts = append(opts, hubclient.WithAutoDevAuth())
		slog.Info("Hub client using auto dev authentication")
	}

	client, err := hubclient.New(s.config.HubEndpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Hub client: %w", err)
	}

	var hydrator *templatecache.Hydrator
	if s.cache != nil {
		hydrator = templatecache.NewHydrator(s.cache, client)
	}

	conn := &HubConnection{
		Name:        "default",
		HubEndpoint: s.config.HubEndpoint,
		BrokerID:    s.config.BrokerID,
		HubClient:   client,
		Hydrator:    hydrator,
		Status:      ConnectionStatusDisconnected,
	}

	return conn, nil
}

// tryLegacyCredentials attempts to load from legacy single-file Store
// and migrate to the MultiStore.
func (s *Server) tryLegacyCredentials() {
	credPath := s.config.BrokerCredentialsPath
	if credPath == "" {
		credPath = brokercredentials.DefaultPath()
	}

	legacyStore := brokercredentials.NewStore(credPath)
	if !legacyStore.Exists() {
		return
	}

	slog.Info("Found legacy credentials file, migrating to multi-store", "path", credPath)

	// Migrate
	if err := s.multiCredStore.MigrateFromLegacy(credPath); err != nil {
		slog.Warn("Failed to migrate legacy credentials", "error", err)

		// Still try to load directly
		creds, err := legacyStore.Load()
		if err != nil {
			slog.Warn("Failed to load legacy credentials", "error", err)
			return
		}

		name := brokercredentials.DeriveHubName(creds.HubEndpoint)
		if name == "" {
			name = "default"
		}
		creds.Name = name

		conn, err := s.createHubConnection(name, creds)
		if err != nil {
			slog.Warn("Failed to create hub connection from legacy credentials", "error", err)
			return
		}
		s.hubMu.Lock()
		s.hubConnections[name] = conn
		s.hubMu.Unlock()
		return
	}

	// Reload from multi-store after migration
	multiCreds, err := s.multiCredStore.List()
	if err != nil {
		slog.Warn("Failed to list credentials after migration", "error", err)
		return
	}

	for i := range multiCreds {
		c := &multiCreds[i]
		if _, exists := s.hubConnections[c.Name]; exists {
			continue
		}
		conn, err := s.createHubConnection(c.Name, c)
		if err != nil {
			slog.Warn("Failed to create hub connection after migration", "name", c.Name, "error", err)
			continue
		}
		s.hubMu.Lock()
		s.hubConnections[c.Name] = conn
		s.hubMu.Unlock()
		slog.Info("Created hub connection from migrated credentials", "name", c.Name, "brokerID", c.BrokerID)
	}
}

// buildAuthMiddleware creates or rebuilds the multi-key auth middleware
// from all hub connections' secret keys.
func (s *Server) buildAuthMiddleware() {
	s.hubMu.RLock()
	var keys []secretKeyEntry
	for _, conn := range s.hubConnections {
		if len(conn.SecretKey) > 0 {
			keys = append(keys, secretKeyEntry{
				hubName:   conn.Name,
				secretKey: conn.SecretKey,
			})
		}
	}
	s.hubMu.RUnlock()

	if !s.config.BrokerAuthEnabled || len(keys) == 0 {
		return
	}

	if s.brokerAuthMiddleware == nil {
		s.brokerAuthMiddleware = NewMultiKeyBrokerAuthMiddleware(
			true,
			5*time.Minute,
			!s.config.BrokerAuthStrictMode,
		)
		if s.config.BrokerAuthStrictMode {
			slog.Info("Broker auth middleware enabled (strict mode)", "keys", len(keys))
		} else {
			slog.Info("Broker auth middleware enabled (permissive mode)", "keys", len(keys))
		}
	}

	s.brokerAuthMiddleware.UpdateKeys(keys)
}

// SetHubClient sets the Hub client for template hydration.
// This is useful for testing or when the client is configured externally.
func (s *Server) SetHubClient(client hubclient.Client) {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()

	// Update or create the "default" connection
	conn, ok := s.hubConnections["default"]
	if !ok {
		conn = &HubConnection{
			Name:   "default",
			Status: ConnectionStatusDisconnected,
		}
		s.hubConnections["default"] = conn
	}
	conn.HubClient = client
	if s.cache != nil {
		conn.Hydrator = templatecache.NewHydrator(s.cache, client)
	}
}

// SetTemplateCache sets the template cache.
// This is useful for testing or when the cache is configured externally.
func (s *Server) SetTemplateCache(cache *templatecache.Cache) {
	s.cache = cache
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	for _, conn := range s.hubConnections {
		if conn.HubClient != nil {
			conn.Hydrator = templatecache.NewHydrator(cache, conn.HubClient)
		}
	}
}

// GetHydrator returns the template hydrator from the first available connection.
func (s *Server) GetHydrator() *templatecache.Hydrator {
	s.hubMu.RLock()
	defer s.hubMu.RUnlock()
	for _, conn := range s.hubConnections {
		if conn.Hydrator != nil {
			return conn.Hydrator
		}
	}
	return nil
}

// Start starts the HTTP server.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	s.startTime = time.Now()

	handler := s.applyMiddleware(s.mux)

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.config.Host, s.config.Port),
		Handler:      handler,
		ReadTimeout:  s.config.ReadTimeout,
		WriteTimeout: s.config.WriteTimeout,
	}
	s.mu.Unlock()

	slog.Info("Runtime Broker API server starting",
		"host", s.config.Host,
		"port", s.config.Port,
	)
	if s.config.Debug {
		slog.Debug("Broker details",
			"brokerID", s.config.BrokerID,
			"brokerName", s.config.BrokerName,
			"hub_endpoint", s.config.HubEndpoint,
			"hub_connections", len(s.hubConnections),
		)
	}

	// Start all hub connections' services
	s.hubMu.RLock()
	for name, conn := range s.hubConnections {
		if err := conn.Start(ctx, s); err != nil {
			slog.Error("Failed to start hub connection", "name", name, "error", err)
		}
	}
	s.hubMu.RUnlock()

	// Log a summary of all hub connections
	s.logHubConnections()

	// Start credential watcher for dynamic reload
	if s.config.HubEnabled && s.multiCredStore != nil {
		s.startCredentialWatcher(ctx)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return s.Shutdown(context.Background())
	}
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	// Stop credential watcher
	s.mu.RLock()
	srv := s.httpServer
	credWatcherStop := s.credWatcherStop
	s.mu.RUnlock()

	if credWatcherStop != nil {
		slog.Info("Stopping credential watcher...")
		close(credWatcherStop)
	}

	// Stop all hub connections
	s.hubMu.RLock()
	for _, conn := range s.hubConnections {
		conn.Stop()
	}
	s.hubMu.RUnlock()

	if srv == nil {
		return nil
	}

	slog.Info("Runtime Broker API server shutting down...")

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return srv.Shutdown(ctx)
}

// Handler returns the HTTP handler for the server.
// This is useful for testing without starting a listener.
func (s *Server) Handler() http.Handler {
	return s.applyMiddleware(s.mux)
}

// LookupContainerID implements AgentLookup interface.
// It looks up an agent by slug and returns its container ID.
func (s *Server) LookupContainerID(ctx context.Context, slug string) (string, error) {
	if s.manager == nil {
		return "", fmt.Errorf("agent manager not available")
	}

	// Normalize slug to lowercase for case-insensitive lookup
	slug = strings.ToLower(slug)

	// Look up agent using List with filter by name
	agents, err := s.manager.List(ctx, map[string]string{"scion.name": slug})
	if err != nil {
		return "", fmt.Errorf("failed to list agents: %w", err)
	}

	if len(agents) == 0 {
		return "", fmt.Errorf("agent '%s' not found", slug)
	}

	agent := agents[0]

	// Get container ID - prefer label, then ContainerID from runtime, then ID
	containerID := agent.Labels["scion.container.id"]
	if containerID == "" {
		containerID = agent.ContainerID
	}
	if containerID == "" {
		containerID = agent.ID
	}
	if containerID == "" {
		return "", fmt.Errorf("agent '%s' has no container ID", slug)
	}

	return containerID, nil
}

// RuntimeCommand implements AgentLookup interface.
// It returns the container runtime command (e.g., "docker", "container").
func (s *Server) RuntimeCommand() string {
	if s.runtime == nil {
		return "docker" // Default fallback
	}
	return s.runtime.Name()
}

// startCredentialWatcher starts a goroutine that watches for credential file changes.
// When credentials change, it reinitializes hub connections as needed.
func (s *Server) startCredentialWatcher(ctx context.Context) {
	if s.multiCredStore == nil {
		slog.Warn("No multi-credential store configured, skipping watcher")
		return
	}

	s.credWatcherStop = make(chan struct{})
	go s.credentialWatchLoop(ctx)
	slog.Info("Credential watcher started", "interval", "10s")
}

// credentialWatchLoop is the main credential watching loop.
func (s *Server) credentialWatchLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.credWatcherStop:
			return
		case <-ticker.C:
			if err := s.checkAndReloadCredentials(ctx); err != nil {
				slog.Error("Error checking credentials", "error", err)
			}
		}
	}
}

// checkAndReloadCredentials checks if multi-store credentials have changed and reloads if necessary.
func (s *Server) checkAndReloadCredentials(ctx context.Context) error {
	if s.multiCredStore == nil {
		return nil
	}

	creds, scanTime, changed, err := s.multiCredStore.LoadAllIfChanged(s.credLastScan)
	if err != nil {
		return fmt.Errorf("failed to check credentials: %w", err)
	}
	if !changed {
		return nil
	}
	s.credLastScan = scanTime

	slog.Info("Credentials changed, reloading", "count", len(creds))

	// Build name -> creds map from new scan
	newCreds := make(map[string]*brokercredentials.BrokerCredentials)
	for i := range creds {
		newCreds[creds[i].Name] = &creds[i]
	}

	s.hubMu.Lock()

	// Detect removals: connections that exist but are not in newCreds
	// (skip "local" connection which comes from InMemoryCredentials)
	for name, conn := range s.hubConnections {
		if name == "local" && s.config.InMemoryCredentials != nil {
			continue
		}
		if _, exists := newCreds[name]; !exists {
			slog.Info("Removing hub connection", "name", name)
			conn.Stop()
			delete(s.hubConnections, name)
		}
	}

	// Detect additions and modifications
	for name, c := range newCreds {
		existingConn, exists := s.hubConnections[name]
		if !exists {
			// New connection
			conn, err := s.createHubConnection(name, c)
			if err != nil {
				slog.Warn("Failed to create new hub connection", "name", name, "error", err)
				continue
			}
			s.hubConnections[name] = conn
			slog.Info("Added new hub connection", "name", name, "brokerID", c.BrokerID)

			// Start services for the new connection
			go func(conn *HubConnection) {
				if err := conn.Start(ctx, s); err != nil {
					slog.Error("Failed to start new hub connection", "name", conn.Name, "error", err)
				}
			}(conn)
		} else {
			// Check if credentials changed
			if existingConn.Credentials == nil ||
				existingConn.Credentials.BrokerID != c.BrokerID ||
				existingConn.Credentials.SecretKey != c.SecretKey ||
				existingConn.Credentials.HubEndpoint != c.HubEndpoint {

				slog.Info("Reinitializing hub connection", "name", name)
				go func(conn *HubConnection, creds *brokercredentials.BrokerCredentials) {
					if err := conn.Reinitialize(ctx, s, creds); err != nil {
						slog.Error("Failed to reinitialize hub connection", "name", conn.Name, "error", err)
					}
				}(existingConn, c)
			}
		}
	}

	s.hubMu.Unlock()

	// Rebuild auth middleware with updated keys
	s.buildAuthMiddleware()

	return nil
}

// buildGroveFilterForHub builds a grove filter function for a specific hub endpoint.
// In multi-hub mode, each heartbeat should only report groves that belong to its hub.
// In single-hub mode or when only one connection exists, no filtering is applied.
func (s *Server) buildGroveFilterForHub(hubEndpoint string) func(string) bool {
	s.hubMu.RLock()
	connCount := len(s.hubConnections)
	s.hubMu.RUnlock()

	// Single-hub mode: no filtering needed
	if connCount <= 1 {
		return nil
	}

	// Multi-hub mode: build a filter from grove settings
	// Scan groves and check which ones have their hub.endpoint matching this connection
	return func(groveID string) bool {
		// For now, try to find the grove's settings to determine its hub endpoint.
		// This requires the agent manager to provide grove paths.
		// As a simple implementation, we scan agents and check their grove settings.
		if s.manager == nil {
			return true // Can't filter without a manager
		}

		agents, err := s.manager.List(context.Background(), nil)
		if err != nil {
			return true // Allow on error
		}

		for _, ag := range agents {
			agGroveID := ag.GroveID
			if agGroveID == "" {
				agGroveID = ag.Grove
			}
			if agGroveID != groveID {
				continue
			}

			// Found an agent in this grove, check its grove path settings
			if ag.GrovePath == "" {
				continue
			}

			groveSettings, err := config.LoadSettingsFromDir(ag.GrovePath)
			if err != nil {
				continue
			}

			ep := groveSettings.GetHubEndpoint()
			if ep != "" {
				return ep == hubEndpoint
			}
		}

		// If we can't determine the grove's hub, include it (safe default)
		return true
	}
}

// isMultiHubMode returns true if the broker is connected to more than one hub.
func (s *Server) isMultiHubMode() bool {
	s.hubMu.RLock()
	defer s.hubMu.RUnlock()
	return len(s.hubConnections) > 1
}

// isGlobalGrove returns true if the grove is the global grove.
func (s *Server) isGlobalGrove(groveID, grovePath string) bool {
	return groveID == "global" || groveID == "" || grovePath == ""
}

// resolveHydrator resolves the hydrator for a request, routing to the correct
// hub connection based on the X-Scion-Hub-Connection header.
func (s *Server) resolveHydrator(r *http.Request) *templatecache.Hydrator {
	connName := r.Header.Get("X-Scion-Hub-Connection")
	if connName != "" {
		s.hubMu.RLock()
		conn, ok := s.hubConnections[connName]
		s.hubMu.RUnlock()
		if ok && conn.Hydrator != nil {
			return conn.Hydrator
		}
	}

	// Fallback: return first available hydrator
	s.hubMu.RLock()
	defer s.hubMu.RUnlock()
	for _, conn := range s.hubConnections {
		if conn.Hydrator != nil {
			return conn.Hydrator
		}
	}
	return nil
}

// getFirstHeartbeat returns the heartbeat service from the first available connection.
// Used for backward compat with single-hub references (e.g., force heartbeat after stop).
func (s *Server) getFirstHeartbeat() *HeartbeatService {
	s.hubMu.RLock()
	defer s.hubMu.RUnlock()
	for _, conn := range s.hubConnections {
		if conn.Heartbeat != nil {
			return conn.Heartbeat
		}
	}
	return nil
}

// logHubConnections logs a summary of all active hub connections.
func (s *Server) logHubConnections() {
	s.hubMu.RLock()
	defer s.hubMu.RUnlock()

	count := len(s.hubConnections)
	if count == 0 {
		slog.Info("No hub connections configured")
		return
	}

	for _, conn := range s.hubConnections {
		attrs := []slog.Attr{
			slog.String("name", conn.Name),
			slog.String("endpoint", conn.HubEndpoint),
			slog.String("status", string(conn.GetStatus())),
		}

		if conn.AuthMode != "" {
			attrs = append(attrs, slog.String("auth", string(conn.AuthMode)))
		}

		if conn.IsColocated {
			attrs = append(attrs, slog.Bool("colocated", true))
		}

		hasHeartbeat := conn.Heartbeat != nil
		hasControlChannel := conn.ControlChannel != nil
		attrs = append(attrs,
			slog.Bool("heartbeat", hasHeartbeat),
			slog.Bool("control_channel", hasControlChannel),
		)

		slog.LogAttrs(context.Background(), slog.LevelInfo, "Hub connection active", attrs...)
	}

	mode := "single-hub"
	if count > 1 {
		mode = "multi-hub"
	}
	slog.Info("Hub connections summary", "total", count, "mode", mode)
}

// registerRoutes sets up all API routes.
func (s *Server) registerRoutes() {
	// Health endpoints
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/readyz", s.handleReadyz)

	// API v1 routes
	s.mux.HandleFunc("/api/v1/info", s.handleInfo)
	s.mux.HandleFunc("/api/v1/hub-connections", s.handleHubConnections)

	// Agent routes
	s.mux.HandleFunc("/api/v1/agents", s.handleAgents)
	s.mux.HandleFunc("/api/v1/agents/", s.handleAgentByID)

	// Workspace sync routes (for Hub-initiated sync via control channel)
	s.mux.HandleFunc("/api/v1/workspace/upload", s.handleWorkspaceUpload)
	s.mux.HandleFunc("/api/v1/workspace/apply", s.handleWorkspaceApply)
}

// applyMiddleware wraps the handler with middleware.
func (s *Server) applyMiddleware(h http.Handler) http.Handler {
	// Apply middleware in reverse order (last applied runs first)
	h = s.recoveryMiddleware(h)
	h = s.loggingMiddleware(h)
	if s.config.CORSEnabled {
		h = s.corsMiddleware(h)
	}
	// Apply broker auth middleware if configured
	if s.brokerAuthMiddleware != nil {
		h = s.brokerAuthMiddleware.Middleware(h)
	}
	return h
}

// corsMiddleware adds CORS headers.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Check if origin is allowed
		allowed := false
		for _, o := range s.config.CORSAllowedOrigins {
			if o == "*" || o == origin {
				allowed = true
				break
			}
		}

		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(s.config.CORSAllowedMethods, ", "))
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(s.config.CORSAllowedHeaders, ", "))
			w.Header().Set("Access-Control-Max-Age", fmt.Sprintf("%d", s.config.CORSMaxAge))
		}

		// Handle preflight
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs requests.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// Extract contextual metadata for logging
		traceID := r.Header.Get("X-Cloud-Trace-Context")
		if traceID == "" {
			traceID = r.Header.Get("X-Trace-ID")
		}

		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote_addr", r.RemoteAddr),
		}
		if traceID != "" {
			attrs = append(attrs, slog.String(logging.AttrTraceID, traceID))
		}

		if s.config.Debug {
			slog.Debug("Incoming request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("remote_addr", r.RemoteAddr),
				slog.String("query", r.URL.RawQuery),
			)
		}

		next.ServeHTTP(wrapped, r)

		duration := time.Since(start)
		level := slog.LevelInfo
		if wrapped.statusCode >= 500 {
			level = slog.LevelError
		} else if wrapped.statusCode >= 400 {
			level = slog.LevelWarn
		}

		slog.LogAttrs(r.Context(), level, "Request completed",
			append(attrs,
				slog.Int("status", wrapped.statusCode),
				slog.Duration("duration", duration),
			)...,
		)
	})
}

// recoveryMiddleware recovers from panics.
func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("Panic recovered",
					slog.Any("error", err),
					slog.String("path", r.URL.Path),
				)
				InternalError(w)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Helper functions

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

// readJSON reads JSON from request body.
func readJSON(r *http.Request, v interface{}) error {
	if r.Body == nil {
		return fmt.Errorf("empty request body")
	}
	return json.NewDecoder(r.Body).Decode(v)
}

// extractID extracts the ID from a URL path like "/api/v1/agents/{id}".
func extractID(r *http.Request, prefix string) string {
	path := strings.TrimPrefix(r.URL.Path, prefix)
	path = strings.TrimPrefix(path, "/")
	// Remove any trailing path segments
	if idx := strings.Index(path, "/"); idx != -1 {
		path = path[:idx]
	}
	return path
}

// extractAction extracts the action from a URL path like "/api/v1/agents/{id}/start".
func extractAction(r *http.Request, prefix string) (id, action string) {
	path := strings.TrimPrefix(r.URL.Path, prefix)
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 {
		return "", ""
	}
	id = parts[0]
	if len(parts) > 1 {
		action = parts[1]
	}
	return
}
