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

package identity

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/state"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

// ChatUserInfo holds basic chat user information for identity mapping.
type ChatUserInfo struct {
	PlatformID  string
	DisplayName string
	Email       string
}

// UserLookup retrieves chat platform user info by ID.
type UserLookup interface {
	GetUser(ctx context.Context, userID string) (*ChatUserInfo, error)
}

// Mapper handles user identity mapping between chat platforms and the Hub.
type Mapper struct {
	store     *state.Store
	hubClient hubclient.Client
	hubURL    string
	log       *slog.Logger
}

// NewMapper creates a new identity mapper.
func NewMapper(store *state.Store, hubClient hubclient.Client, hubURL string, log *slog.Logger) *Mapper {
	return &Mapper{
		store:     store,
		hubClient: hubClient,
		hubURL:    hubURL,
		log:       log,
	}
}

// Resolve looks up the Hub user for a chat platform user.
// Returns the UserMapping or nil if not registered.
func (m *Mapper) Resolve(platformUserID, platform string) (*state.UserMapping, error) {
	return m.store.GetUserMapping(platformUserID, platform)
}

// AutoRegister attempts to register a chat user by matching their email
// to a Hub user. Returns the mapping if successful, nil if no match.
func (m *Mapper) AutoRegister(ctx context.Context, chatUser *ChatUserInfo, platform string) (*state.UserMapping, error) {
	if chatUser.Email == "" {
		return nil, nil
	}

	users, err := m.hubClient.Users().List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("listing hub users: %w", err)
	}

	for _, u := range users.Users {
		if u.Email == chatUser.Email {
			mapping := &state.UserMapping{
				PlatformUserID: chatUser.PlatformID,
				Platform:       platform,
				HubUserID:      u.ID,
				HubUserEmail:   u.Email,
				RegisteredBy:   "auto",
			}
			if err := m.store.SetUserMapping(mapping); err != nil {
				return nil, fmt.Errorf("saving user mapping: %w", err)
			}
			m.log.Info("auto-registered user",
				"platform_user_id", chatUser.PlatformID,
				"platform", platform,
				"hub_user_id", u.ID,
				"hub_email", u.Email,
			)
			return mapping, nil
		}
	}

	return nil, nil
}

// Register creates a manual user mapping.
func (m *Mapper) Register(platformUserID, platform, hubUserID, hubUserEmail string) error {
	mapping := &state.UserMapping{
		PlatformUserID: platformUserID,
		Platform:       platform,
		HubUserID:      hubUserID,
		HubUserEmail:   hubUserEmail,
		RegisteredBy:   "manual",
	}
	return m.store.SetUserMapping(mapping)
}

// Unregister removes a user mapping.
func (m *Mapper) Unregister(platformUserID, platform string) error {
	return m.store.DeleteUserMapping(platformUserID, platform)
}

// ClientFor creates a hubclient.Client authenticated as the mapped Hub user.
func (m *Mapper) ClientFor(ctx context.Context, mapping *state.UserMapping) (hubclient.Client, error) {
	resp, err := m.hubClient.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:   fmt.Sprintf("chat-app-impersonation-%s", mapping.HubUserID),
		Scopes: []string{"agents:read", "agents:write", "groves:read", "messages:write"},
	})
	if err != nil {
		return nil, fmt.Errorf("creating impersonation token: %w", err)
	}

	return hubclient.New(m.hubURL, hubclient.WithBearerToken(resp.Token))
}

// ResolveOrAutoRegister tries to resolve a user, and if not found, attempts auto-registration.
func (m *Mapper) ResolveOrAutoRegister(ctx context.Context, lookup UserLookup, platformUserID, platform string) (*state.UserMapping, error) {
	mapping, err := m.Resolve(platformUserID, platform)
	if err != nil {
		return nil, err
	}
	if mapping != nil {
		return mapping, nil
	}

	chatUser, err := lookup.GetUser(ctx, platformUserID)
	if err != nil {
		m.log.Warn("failed to get chat user for auto-registration",
			"platform_user_id", platformUserID,
			"error", err,
		)
		return nil, nil
	}

	return m.AutoRegister(ctx, chatUser, platform)
}
