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
	"testing"
	"time"
)

func TestGEGoogleExchangeHubNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config GEGoogleExchangeConfig
		want   string
	}{
		{
			name:   "missing clients",
			config: GEGoogleExchangeConfig{Enabled: true},
			want:   "GE Google exchange config: GE Google exchange requires at least one allowed client ID",
		},
		{
			name: "blank client",
			config: GEGoogleExchangeConfig{
				Enabled: true, AllowedClientIDs: []string{"client-id", " \t "},
			},
			want: "GE Google exchange config: GE Google exchange allowed client IDs must not be empty",
		},
		{
			name: "negative token TTL",
			config: GEGoogleExchangeConfig{
				Enabled: true, AllowedClientIDs: []string{"client-id"}, TokenTTL: -time.Second,
			},
			want: "GE Google exchange config: GE Google exchange token TTL must be between 0 and 5m0s",
		},
		{
			name: "token TTL above maximum",
			config: GEGoogleExchangeConfig{
				Enabled: true, AllowedClientIDs: []string{"client-id"}, TokenTTL: MaxGETokenTTL + time.Nanosecond,
			},
			want: "GE Google exchange config: GE Google exchange token TTL must be between 0 and 5m0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultServerConfig()
			cfg.GEGoogleExchange = tt.config

			server, err := New(cfg, nil)
			if server != nil {
				t.Fatal("hub.New returned a server for invalid GE Google exchange configuration")
			}
			if err == nil || err.Error() != tt.want {
				t.Fatalf("hub.New error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestGEGoogleExchangeHubNewAcceptsProductionConfigurations(t *testing.T) {
	tests := []struct {
		name   string
		config GEGoogleExchangeConfig
		active bool
	}{
		{
			name: "enabled with default token TTL",
			config: GEGoogleExchangeConfig{
				Enabled: true, AllowedClientIDs: []string{"client-id"},
			},
			active: true,
		},
		{
			name:   "disabled exchange",
			config: GEGoogleExchangeConfig{Enabled: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := newTestStore(":memory:")
			if err != nil {
				t.Fatalf("newTestStore: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			cfg := DefaultServerConfig()
			cfg.GEGoogleExchange = tt.config
			server, err := New(cfg, store)
			if err != nil {
				t.Fatalf("hub.New: %v", err)
			}
			t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

			if got := server.geExchangeService != nil; got != tt.active {
				t.Fatalf("GE exchange service active = %t, want %t", got, tt.active)
			}
			if tt.active && server.geExchangeService.config.TokenTTL != DefaultGETokenTTL {
				t.Fatalf("GE token TTL = %v, want default %v", server.geExchangeService.config.TokenTTL, DefaultGETokenTTL)
			}
		})
	}
}
