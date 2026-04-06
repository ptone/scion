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

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/chatapp"
	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/googlechat"
	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/identity"
	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/state"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

func main() {
	configPath := flag.String("config", "scion-chat-app.yaml", "Path to configuration file")
	flag.Parse()

	// Load configuration.
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Initialize structured logging.
	log := initLogger(cfg.Logging)
	log.Info("scion-chat-app starting")

	// Initialize SQLite state database.
	dbPath := cfg.State.Database
	if dbPath == "" {
		dbPath = "scion-chat-app.db"
	}
	store, err := state.New(dbPath)
	if err != nil {
		log.Error("failed to initialize state database", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	log.Info("state database initialized", "path", dbPath)

	// Create Hub admin client.
	var hubOpts []hubclient.Option
	if cfg.Hub.Credentials != "" {
		token, err := os.ReadFile(cfg.Hub.Credentials)
		if err != nil {
			log.Error("failed to read hub credentials", "error", err)
			os.Exit(1)
		}
		hubOpts = append(hubOpts, hubclient.WithBearerToken(strings.TrimSpace(string(token))))
	} else {
		hubOpts = append(hubOpts, hubclient.WithAutoDevAuth())
	}

	adminClient, err := hubclient.New(cfg.Hub.Endpoint, hubOpts...)
	if err != nil {
		log.Error("failed to create hub client", "error", err)
		os.Exit(1)
	}
	log.Info("hub client initialized", "endpoint", cfg.Hub.Endpoint)

	// Create identity mapper.
	idMapper := identity.NewMapper(store, adminClient, cfg.Hub.Endpoint, log.With("component", "identity"))

	// Create broker server with a nil handler; wired to the notification relay below.
	broker := chatapp.NewBrokerServer(nil, log.With("component", "broker"))

	// Start broker plugin RPC server.
	pluginAddr := cfg.Plugin.ListenAddress
	if pluginAddr == "" {
		pluginAddr = "localhost:9090"
	}
	pluginServer, err := broker.Serve(pluginAddr)
	if err != nil {
		log.Error("failed to start broker plugin server", "error", err)
		os.Exit(1)
	}
	defer pluginServer.Close()
	log.Info("broker plugin RPC server started", "address", pluginServer.Addr())

	// Create command router with a nil messenger; set after adapter creation.
	cmdRouter := chatapp.NewCommandRouter(
		adminClient,
		cfg.Hub.Endpoint,
		store,
		idMapper,
		nil, // messenger wired below
		broker,
		log.With("component", "commands"),
	)

	// Initialize the platform messenger (Google Chat adapter for now).
	var messenger chatapp.Messenger

	if cfg.Platforms.GoogleChat.Enabled {
		gcAdapter := googlechat.NewAdapter(
			googlechat.Config{
				ProjectID:     cfg.Platforms.GoogleChat.ProjectID,
				Audience:      cfg.Platforms.GoogleChat.Audience,
				ListenAddress: cfg.Platforms.GoogleChat.ListenAddress,
				Credentials:   cfg.Platforms.GoogleChat.Credentials,
			},
			cmdRouter.HandleEvent,
			nil, // uses http.DefaultClient
			log.With("component", "googlechat"),
		)
		messenger = gcAdapter
		log.Info("google chat adapter initialized",
			"project_id", cfg.Platforms.GoogleChat.ProjectID,
		)
	}

	// Wire the messenger into the command router now that it exists.
	cmdRouter.SetMessenger(messenger)

	// Create notification relay and wire it as the broker's message handler.
	relay := chatapp.NewNotificationRelay(store, messenger, log.With("component", "notifications"))
	broker.SetHandler(relay.HandleBrokerMessage)

	// Load existing space-grove links and request broker subscriptions.
	links, err := store.ListSpaceLinks()
	if err != nil {
		log.Error("failed to load space links", "error", err)
	} else {
		for _, link := range links {
			pattern := fmt.Sprintf("grove.%s.>", link.GroveID)
			if err := broker.RequestSubscription(pattern); err != nil {
				log.Warn("failed to request subscription for grove",
					"grove_id", link.GroveID,
					"error", err,
				)
			}
		}
		log.Info("loaded existing space-grove links", "count", len(links))
	}

	// Start platform servers.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)

	if cfg.Platforms.GoogleChat.Enabled && messenger != nil {
		gcAdapter := messenger.(*googlechat.Adapter)
		listenAddr := cfg.Platforms.GoogleChat.ListenAddress
		if listenAddr == "" {
			listenAddr = ":8443"
		}
		go func() {
			if err := gcAdapter.Start(listenAddr); err != nil {
				errCh <- fmt.Errorf("google chat server: %w", err)
			}
		}()
		log.Info("google chat webhook server starting", "address", listenAddr)
	}

	log.Info("scion-chat-app ready")

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		log.Info("received signal, shutting down", "signal", sig)
	case err := <-errCh:
		log.Error("server error", "error", err)
	}

	// Graceful shutdown with timeout.
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 30*time.Second)
	defer shutdownCancel()

	if cfg.Platforms.GoogleChat.Enabled && messenger != nil {
		gcAdapter := messenger.(*googlechat.Adapter)
		if err := gcAdapter.Stop(shutdownCtx); err != nil {
			log.Error("failed to stop google chat adapter", "error", err)
		}
	}

	log.Info("scion-chat-app stopped")
}

// loadConfig reads and parses the YAML configuration file.
// Environment variables in the form ${VAR} or $VAR are expanded before parsing.
func loadConfig(path string) (*chatapp.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Expand environment variables in the raw YAML.
	expanded := os.ExpandEnv(string(data))

	var cfg chatapp.Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return &cfg, nil
}

// initLogger creates a structured logger from the logging configuration.
func initLogger(cfg chatapp.LoggingConfig) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	return slog.New(handler)
}
