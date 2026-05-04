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

package googlechat

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/chatapp"
)

func jsonNumber(s string) json.Number {
	return json.Number(s)
}

func TestNormalizeEvent_UserEmail(t *testing.T) {
	adapter := NewAdapter(Config{}, nil, nil, slog.Default())

	tests := []struct {
		name      string
		raw       rawEvent
		wantEmail string
		wantID    string
	}{
		{
			name: "email populated from user object",
			raw: rawEvent{
				Chat: &rawChatPayload{
					User: &rawUser{
						Name:  "users/12345",
						Email: "alice@example.com",
					},
					AppCommandPayload: &rawAppCommandPayload{
						Space: &rawSpace{Name: "spaces/abc"},
						AppCommandMetadata: &rawAppCommandMetadata{
							AppCommandId: "1",
						},
					},
				},
			},
			wantEmail: "alice@example.com",
			wantID:    "users/12345",
		},
		{
			name: "empty email when user has no email",
			raw: rawEvent{
				Chat: &rawChatPayload{
					User: &rawUser{
						Name: "users/12345",
					},
					AppCommandPayload: &rawAppCommandPayload{
						Space: &rawSpace{Name: "spaces/abc"},
						AppCommandMetadata: &rawAppCommandMetadata{
							AppCommandId: "1",
						},
					},
				},
			},
			wantEmail: "",
			wantID:    "users/12345",
		},
		{
			name: "email populated for message events",
			raw: rawEvent{
				Chat: &rawChatPayload{
					User: &rawUser{
						Name:  "users/67890",
						Email: "bob@example.com",
					},
					MessagePayload: &rawMessagePayload{
						Message: &rawMessage{Text: "hello"},
						Space:   &rawSpace{Name: "spaces/xyz"},
					},
				},
			},
			wantEmail: "bob@example.com",
			wantID:    "users/67890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := adapter.normalizeEvent(&tt.raw)
			if event == nil {
				t.Fatal("normalizeEvent returned nil")
			}
			if event.UserEmail != tt.wantEmail {
				t.Errorf("UserEmail = %q, want %q", event.UserEmail, tt.wantEmail)
			}
			if event.UserID != tt.wantID {
				t.Errorf("UserID = %q, want %q", event.UserID, tt.wantID)
			}
			if event.Platform != PlatformName {
				t.Errorf("Platform = %q, want %q", event.Platform, PlatformName)
			}
		})
	}
}

func TestNormalizeEvent_CommandIDMapping(t *testing.T) {
	adapter := NewAdapter(Config{
		CommandIDMap: map[string]string{
			"1": "scion",
			"2": "scionAdmin",
		},
	}, nil, nil, slog.Default())

	tests := []struct {
		name        string
		commandID   string
		wantCommand string
	}{
		{
			name:        "command ID 1 maps to scion",
			commandID:   "1",
			wantCommand: "scion",
		},
		{
			name:        "command ID 2 maps to scionAdmin",
			commandID:   "2",
			wantCommand: "scionAdmin",
		},
		{
			name:        "unknown command ID falls back to scion",
			commandID:   "99",
			wantCommand: "scion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := adapter.normalizeEvent(&rawEvent{
				Chat: &rawChatPayload{
					User: &rawUser{Name: "users/1", Email: "u@e.com"},
					AppCommandPayload: &rawAppCommandPayload{
						Space:              &rawSpace{Name: "spaces/s"},
						AppCommandMetadata: &rawAppCommandMetadata{AppCommandId: jsonNumber(tt.commandID)},
					},
				},
			})
			if event == nil {
				t.Fatal("normalizeEvent returned nil")
			}
			if event.Command != tt.wantCommand {
				t.Errorf("Command = %q, want %q", event.Command, tt.wantCommand)
			}
		})
	}
}

func TestNormalizeEvent_SlashCommandInMessage(t *testing.T) {
	adapter := NewAdapter(Config{
		CommandIDMap: map[string]string{
			"1": "scion",
			"2": "scionAdmin",
		},
	}, nil, nil, slog.Default())

	tests := []struct {
		name        string
		raw         rawEvent
		wantType    chatapp.ChatEventType
		wantCommand string
		wantArgs    string
	}{
		{
			name: "messagePayload with slashCommand routes as command",
			raw: rawEvent{
				Chat: &rawChatPayload{
					User: &rawUser{Name: "users/1", Email: "u@e.com"},
					MessagePayload: &rawMessagePayload{
						Space: &rawSpace{Name: "spaces/s"},
						Message: &rawMessage{
							ArgumentText: "help",
							SlashCommand: &rawSlashCommand{CommandId: jsonNumber("2")},
						},
					},
				},
			},
			wantType:    chatapp.EventCommand,
			wantCommand: "scionAdmin",
			wantArgs:    "help",
		},
		{
			name: "messagePayload without slashCommand remains a message",
			raw: rawEvent{
				Chat: &rawChatPayload{
					User: &rawUser{Name: "users/1", Email: "u@e.com"},
					MessagePayload: &rawMessagePayload{
						Space:   &rawSpace{Name: "spaces/s"},
						Message: &rawMessage{Text: "hello"},
					},
				},
			},
			wantType:    chatapp.EventMessage,
			wantCommand: "",
			wantArgs:    "",
		},
		{
			name: "appCommandPayload falls back to message slashCommand",
			raw: rawEvent{
				Chat: &rawChatPayload{
					User: &rawUser{Name: "users/1", Email: "u@e.com"},
					AppCommandPayload: &rawAppCommandPayload{
						Space: &rawSpace{Name: "spaces/s"},
						Message: &rawMessage{
							ArgumentText: "info",
							SlashCommand: &rawSlashCommand{CommandId: jsonNumber("2")},
						},
					},
				},
			},
			wantType:    chatapp.EventCommand,
			wantCommand: "scionAdmin",
			wantArgs:    "info",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := adapter.normalizeEvent(&tt.raw)
			if event == nil {
				t.Fatal("normalizeEvent returned nil")
			}
			if event.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", event.Type, tt.wantType)
			}
			if event.Command != tt.wantCommand {
				t.Errorf("Command = %q, want %q", event.Command, tt.wantCommand)
			}
			if tt.wantArgs != "" && event.Args != tt.wantArgs {
				t.Errorf("Args = %q, want %q", event.Args, tt.wantArgs)
			}
		})
	}
}

func TestNormalizeEvent_NilChat(t *testing.T) {
	adapter := NewAdapter(Config{}, nil, nil, slog.Default())
	event := adapter.normalizeEvent(&rawEvent{})
	if event != nil {
		t.Errorf("expected nil event for empty rawEvent, got %+v", event)
	}
}

func TestNormalizeEvent_EventTypes(t *testing.T) {
	adapter := NewAdapter(Config{}, nil, nil, slog.Default())

	tests := []struct {
		name     string
		raw      rawEvent
		wantType chatapp.ChatEventType
	}{
		{
			name: "app command",
			raw: rawEvent{
				Chat: &rawChatPayload{
					User: &rawUser{Name: "users/1", Email: "u@e.com"},
					AppCommandPayload: &rawAppCommandPayload{
						Space:              &rawSpace{Name: "spaces/s"},
						AppCommandMetadata: &rawAppCommandMetadata{AppCommandId: "1"},
					},
				},
			},
			wantType: chatapp.EventCommand,
		},
		{
			name: "added to space",
			raw: rawEvent{
				Chat: &rawChatPayload{
					User:                &rawUser{Name: "users/1", Email: "u@e.com"},
					AddedToSpacePayload: &rawAddedToSpacePayload{Space: &rawSpace{Name: "spaces/s"}},
				},
			},
			wantType: chatapp.EventSpaceJoin,
		},
		{
			name: "removed from space",
			raw: rawEvent{
				Chat: &rawChatPayload{
					User:                    &rawUser{Name: "users/1"},
					RemovedFromSpacePayload: &rawRemovedFromSpacePayload{Space: &rawSpace{Name: "spaces/s"}},
				},
			},
			wantType: chatapp.EventSpaceRemove,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := adapter.normalizeEvent(&tt.raw)
			if event == nil {
				t.Fatal("normalizeEvent returned nil")
			}
			if event.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", event.Type, tt.wantType)
			}
		})
	}
}
