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

package chatapp

// ChatEvent represents a normalized inbound event from a chat platform.
type ChatEvent struct {
	Type       ChatEventType
	Platform   string
	SpaceID    string
	ThreadID   string
	UserID     string
	Text       string
	Command    string
	Args       string
	ActionID   string
	ActionData string
	DialogData map[string]string
}

// ChatEventType identifies the kind of inbound chat event.
type ChatEventType string

const (
	EventMessage      ChatEventType = "message"
	EventCommand      ChatEventType = "command"
	EventAction       ChatEventType = "action"
	EventDialogSubmit ChatEventType = "dialog_submit"
	EventSpaceJoin    ChatEventType = "space_join"
	EventSpaceRemove  ChatEventType = "space_remove"
)
