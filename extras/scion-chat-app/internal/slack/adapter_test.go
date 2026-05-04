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

package slack

import (
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/chatapp"
)

// --- Block Kit rendering tests ---

func TestRenderBlocks_HeaderOnly(t *testing.T) {
	card := &chatapp.Card{
		Header: chatapp.CardHeader{
			Title:    "Test Card",
			Subtitle: "A subtitle",
		},
	}

	blocks := renderBlocks(card)

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (header + context), got %d", len(blocks))
	}

	header, ok := blocks[0].(*slackapi.HeaderBlock)
	if !ok {
		t.Fatalf("block 0: expected *HeaderBlock, got %T", blocks[0])
	}
	if header.Text.Text != "Test Card" {
		t.Errorf("header text = %q, want %q", header.Text.Text, "Test Card")
	}

	ctx, ok := blocks[1].(*slackapi.ContextBlock)
	if !ok {
		t.Fatalf("block 1: expected *ContextBlock, got %T", blocks[1])
	}
	if len(ctx.ContextElements.Elements) == 0 {
		t.Fatal("context block has no elements")
	}
}

func TestRenderBlocks_TextWidget(t *testing.T) {
	card := &chatapp.Card{
		Sections: []chatapp.CardSection{
			{
				Widgets: []chatapp.Widget{
					{Type: chatapp.WidgetText, Content: "Hello world"},
				},
			},
		},
	}

	blocks := renderBlocks(card)

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	section, ok := blocks[0].(*slackapi.SectionBlock)
	if !ok {
		t.Fatalf("expected *SectionBlock, got %T", blocks[0])
	}
	if section.Text == nil || section.Text.Text != "Hello world" {
		t.Errorf("section text = %v, want %q", section.Text, "Hello world")
	}
}

func TestRenderBlocks_KeyValueWidget(t *testing.T) {
	card := &chatapp.Card{
		Sections: []chatapp.CardSection{
			{
				Widgets: []chatapp.Widget{
					{Type: chatapp.WidgetKeyValue, Label: "Status", Content: "Running"},
				},
			},
		},
	}

	blocks := renderBlocks(card)

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	section, ok := blocks[0].(*slackapi.SectionBlock)
	if !ok {
		t.Fatalf("expected *SectionBlock, got %T", blocks[0])
	}
	if len(section.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(section.Fields))
	}
	if section.Fields[0].Text != "*Status*" {
		t.Errorf("field 0 = %q, want %q", section.Fields[0].Text, "*Status*")
	}
	if section.Fields[1].Text != "Running" {
		t.Errorf("field 1 = %q, want %q", section.Fields[1].Text, "Running")
	}
}

func TestRenderBlocks_ButtonWidget(t *testing.T) {
	card := &chatapp.Card{
		Sections: []chatapp.CardSection{
			{
				Widgets: []chatapp.Widget{
					{Type: chatapp.WidgetButton, Label: "Click Me", ActionID: "btn.1", ActionData: "value1"},
				},
			},
		},
	}

	blocks := renderBlocks(card)

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	action, ok := blocks[0].(*slackapi.ActionBlock)
	if !ok {
		t.Fatalf("expected *ActionBlock, got %T", blocks[0])
	}
	if len(action.Elements.ElementSet) != 1 {
		t.Fatalf("expected 1 element, got %d", len(action.Elements.ElementSet))
	}
}

func TestRenderBlocks_DividerWidget(t *testing.T) {
	card := &chatapp.Card{
		Sections: []chatapp.CardSection{
			{
				Widgets: []chatapp.Widget{
					{Type: chatapp.WidgetDivider},
				},
			},
		},
	}

	blocks := renderBlocks(card)

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	_, ok := blocks[0].(*slackapi.DividerBlock)
	if !ok {
		t.Fatalf("expected *DividerBlock, got %T", blocks[0])
	}
}

func TestRenderBlocks_ImageWidget(t *testing.T) {
	card := &chatapp.Card{
		Sections: []chatapp.CardSection{
			{
				Widgets: []chatapp.Widget{
					{Type: chatapp.WidgetImage, Content: "https://example.com/img.png", Label: "alt text"},
				},
			},
		},
	}

	blocks := renderBlocks(card)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	img, ok := blocks[0].(*slackapi.ImageBlock)
	if !ok {
		t.Fatalf("expected *ImageBlock, got %T", blocks[0])
	}
	if img.ImageURL != "https://example.com/img.png" {
		t.Errorf("image URL = %q, want %q", img.ImageURL, "https://example.com/img.png")
	}
}

func TestRenderBlocks_InputWidget(t *testing.T) {
	card := &chatapp.Card{
		Sections: []chatapp.CardSection{
			{
				Widgets: []chatapp.Widget{
					{Type: chatapp.WidgetInput, Label: "Your response", ActionID: "agent.respond.test"},
				},
			},
		},
	}

	blocks := renderBlocks(card)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	_, ok := blocks[0].(*slackapi.InputBlock)
	if !ok {
		t.Fatalf("expected *InputBlock, got %T", blocks[0])
	}
}

func TestRenderBlocks_CheckboxWidget(t *testing.T) {
	card := &chatapp.Card{
		Sections: []chatapp.CardSection{
			{
				Widgets: []chatapp.Widget{
					{
						Type:     chatapp.WidgetCheckbox,
						Label:    "Activities",
						ActionID: "filter.1",
						Options: []chatapp.SelectOption{
							{Label: "Error", Value: "ERROR"},
							{Label: "Completed", Value: "COMPLETED"},
						},
					},
				},
			},
		},
	}

	blocks := renderBlocks(card)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	_, ok := blocks[0].(*slackapi.InputBlock)
	if !ok {
		t.Fatalf("expected *InputBlock, got %T", blocks[0])
	}
}

func TestRenderBlocks_CardActions(t *testing.T) {
	card := &chatapp.Card{
		Actions: []chatapp.CardAction{
			{Label: "Start", ActionID: "agent.start.test", Style: "primary"},
			{Label: "Stop", ActionID: "agent.stop.test", Style: "danger"},
			{Label: "Logs", ActionID: "agent.logs.test"},
		},
	}

	blocks := renderBlocks(card)

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	action, ok := blocks[0].(*slackapi.ActionBlock)
	if !ok {
		t.Fatalf("expected *ActionBlock, got %T", blocks[0])
	}
	if len(action.Elements.ElementSet) != 3 {
		t.Fatalf("expected 3 button elements, got %d", len(action.Elements.ElementSet))
	}
}

func TestRenderBlocks_SectionHeader(t *testing.T) {
	card := &chatapp.Card{
		Sections: []chatapp.CardSection{
			{
				Header: "Details",
				Widgets: []chatapp.Widget{
					{Type: chatapp.WidgetText, Content: "info"},
				},
			},
		},
	}

	blocks := renderBlocks(card)

	// Section header + text widget
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	section, ok := blocks[0].(*slackapi.SectionBlock)
	if !ok {
		t.Fatalf("expected *SectionBlock for header, got %T", blocks[0])
	}
	if section.Text == nil || section.Text.Text != "*Details*" {
		t.Errorf("section header text = %v, want %q", section.Text, "*Details*")
	}
}

func TestRenderBlocks_FullCard(t *testing.T) {
	card := &chatapp.Card{
		Header: chatapp.CardHeader{
			Title:    "deploy-agent",
			Subtitle: "Completed | Deployment finished",
		},
		Sections: []chatapp.CardSection{
			{
				Widgets: []chatapp.Widget{
					{Type: chatapp.WidgetText, Content: "All health checks passing."},
				},
			},
		},
		Actions: []chatapp.CardAction{
			{Label: "View Logs", ActionID: "agent.logs.deploy-agent"},
		},
	}

	blocks := renderBlocks(card)

	// header + context + text section + actions
	if len(blocks) != 4 {
		t.Fatalf("expected 4 blocks, got %d", len(blocks))
	}
}

// --- Modal rendering tests ---

func TestRenderModal_TextFields(t *testing.T) {
	dialog := &chatapp.Dialog{
		Title: "Create Agent",
		Fields: []chatapp.DialogField{
			{ID: "name", Label: "Agent Name", Type: "text", Placeholder: "my-agent", Required: true},
			{ID: "desc", Label: "Description", Type: "textarea", Placeholder: "Describe the agent", Required: false},
		},
		Submit: chatapp.CardAction{Label: "Create", ActionID: "agent.create"},
		Cancel: chatapp.CardAction{Label: "Cancel"},
	}

	modal := renderModal(dialog)

	if modal.Type != "modal" {
		t.Errorf("type = %q, want %q", modal.Type, "modal")
	}
	if modal.Title.Text != "Create Agent" {
		t.Errorf("title = %q, want %q", modal.Title.Text, "Create Agent")
	}
	if modal.Submit.Text != "Create" {
		t.Errorf("submit = %q, want %q", modal.Submit.Text, "Create")
	}
	if modal.Close.Text != "Cancel" {
		t.Errorf("close = %q, want %q", modal.Close.Text, "Cancel")
	}
	if modal.CallbackID != "agent.create" {
		t.Errorf("callback_id = %q, want %q", modal.CallbackID, "agent.create")
	}
	if len(modal.Blocks.BlockSet) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(modal.Blocks.BlockSet))
	}
}

func TestRenderModal_SelectField(t *testing.T) {
	dialog := &chatapp.Dialog{
		Title: "Pick",
		Fields: []chatapp.DialogField{
			{
				ID:    "template",
				Label: "Template",
				Type:  "select",
				Options: []chatapp.SelectOption{
					{Label: "Standard", Value: "std"},
					{Label: "Custom", Value: "custom"},
				},
				Required: true,
			},
		},
		Submit: chatapp.CardAction{Label: "OK", ActionID: "submit"},
	}

	modal := renderModal(dialog)

	if len(modal.Blocks.BlockSet) != 1 {
		t.Fatalf("expected 1 block, got %d", len(modal.Blocks.BlockSet))
	}
}

func TestRenderModal_CheckboxField(t *testing.T) {
	dialog := &chatapp.Dialog{
		Title: "Filter",
		Fields: []chatapp.DialogField{
			{
				ID:    "activities",
				Label: "Activities",
				Type:  "checkbox",
				Options: []chatapp.SelectOption{
					{Label: "Error", Value: "ERROR"},
					{Label: "Completed", Value: "COMPLETED"},
				},
			},
		},
		Submit: chatapp.CardAction{Label: "Save", ActionID: "filter.save"},
	}

	modal := renderModal(dialog)

	if len(modal.Blocks.BlockSet) != 1 {
		t.Fatalf("expected 1 block, got %d", len(modal.Blocks.BlockSet))
	}

	input, ok := modal.Blocks.BlockSet[0].(*slackapi.InputBlock)
	if !ok {
		t.Fatalf("expected *InputBlock, got %T", modal.Blocks.BlockSet[0])
	}
	if !input.Optional {
		t.Error("checkbox field with Required=false should be Optional=true")
	}
}

func TestExtractModalValues_TextInput(t *testing.T) {
	state := &slackapi.ViewState{
		Values: map[string]map[string]slackapi.BlockAction{
			"name": {
				"name": {
					Type:  "plain_text_input",
					Value: "my-agent",
				},
			},
		},
	}

	result := extractModalValues(state)

	if result["name"] != "my-agent" {
		t.Errorf("name = %q, want %q", result["name"], "my-agent")
	}
}

func TestExtractModalValues_SelectInput(t *testing.T) {
	state := &slackapi.ViewState{
		Values: map[string]map[string]slackapi.BlockAction{
			"template": {
				"template": {
					Type: "static_select",
					SelectedOption: slackapi.OptionBlockObject{
						Value: "custom",
					},
				},
			},
		},
	}

	result := extractModalValues(state)

	if result["template"] != "custom" {
		t.Errorf("template = %q, want %q", result["template"], "custom")
	}
}

func TestExtractModalValues_CheckboxInput(t *testing.T) {
	state := &slackapi.ViewState{
		Values: map[string]map[string]slackapi.BlockAction{
			"activities": {
				"activities": {
					Type: "checkboxes",
					SelectedOptions: []slackapi.OptionBlockObject{
						{Value: "ERROR"},
						{Value: "COMPLETED"},
					},
				},
			},
		},
	}

	result := extractModalValues(state)

	if result["activities"] != "ERROR,COMPLETED" {
		t.Errorf("activities = %q, want %q", result["activities"], "ERROR,COMPLETED")
	}
}

func TestExtractModalValues_Nil(t *testing.T) {
	result := extractModalValues(nil)
	if result != nil {
		t.Errorf("expected nil for nil state, got %v", result)
	}
}

// --- Event normalization tests ---

func TestNormalizeSlashCommand(t *testing.T) {
	cmd := slackapi.SlashCommand{
		ChannelID: "C0ABC123",
		UserID:    "U0DEF456",
		Command:   "/scion",
		Text:      "list",
	}

	event := normalizeSlashCommand(cmd)

	if event.Type != chatapp.EventCommand {
		t.Errorf("Type = %q, want %q", event.Type, chatapp.EventCommand)
	}
	if event.Platform != PlatformName {
		t.Errorf("Platform = %q, want %q", event.Platform, PlatformName)
	}
	if event.SpaceID != "C0ABC123" {
		t.Errorf("SpaceID = %q, want %q", event.SpaceID, "C0ABC123")
	}
	if event.UserID != "U0DEF456" {
		t.Errorf("UserID = %q, want %q", event.UserID, "U0DEF456")
	}
	if event.Command != "scion" {
		t.Errorf("Command = %q, want %q", event.Command, "scion")
	}
	if event.Args != "list" {
		t.Errorf("Args = %q, want %q", event.Args, "list")
	}
}

func TestNormalizeSlashCommand_MultipleArgs(t *testing.T) {
	cmd := slackapi.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
		Text:      "status my-agent",
	}

	event := normalizeSlashCommand(cmd)

	if event.Args != "status my-agent" {
		t.Errorf("Args = %q, want %q", event.Args, "status my-agent")
	}
}

func TestNormalizeAppMention(t *testing.T) {
	evt := &slackevents.AppMentionEvent{
		User:            "U0ABC123",
		Text:            "<@U5678BOT> tell deploy-agent hello",
		Channel:         "C0CHANNEL",
		TimeStamp:       "1712345678.123456",
		ThreadTimeStamp: "1712345000.000000",
	}

	event := normalizeAppMention(evt)

	if event.Type != chatapp.EventMessage {
		t.Errorf("Type = %q, want %q", event.Type, chatapp.EventMessage)
	}
	if event.Platform != PlatformName {
		t.Errorf("Platform = %q, want %q", event.Platform, PlatformName)
	}
	if event.SpaceID != "C0CHANNEL" {
		t.Errorf("SpaceID = %q, want %q", event.SpaceID, "C0CHANNEL")
	}
	if event.UserID != "U0ABC123" {
		t.Errorf("UserID = %q, want %q", event.UserID, "U0ABC123")
	}
	if event.Text != "tell deploy-agent hello" {
		t.Errorf("Text = %q, want %q", event.Text, "tell deploy-agent hello")
	}
	if event.ThreadID != "1712345000.000000" {
		t.Errorf("ThreadID = %q, want %q", event.ThreadID, "1712345000.000000")
	}
}

func TestNormalizeAppMention_NoThread(t *testing.T) {
	evt := &slackevents.AppMentionEvent{
		User:      "U0ABC123",
		Text:      "<@U5678BOT> hello",
		Channel:   "C0CHANNEL",
		TimeStamp: "1712345678.123456",
	}

	event := normalizeAppMention(evt)

	if event.ThreadID != "1712345678.123456" {
		t.Errorf("ThreadID should fall back to TimeStamp, got %q", event.ThreadID)
	}
}

func TestNormalizeMessageIM(t *testing.T) {
	evt := &slackevents.MessageEvent{
		User:            "U0ABC123",
		Text:            "  hello bot  ",
		Channel:         "D0DM123",
		TimeStamp:       "1712345678.123456",
		ThreadTimeStamp: "",
	}

	event := normalizeMessageIM(evt)

	if event.Type != chatapp.EventMessage {
		t.Errorf("Type = %q, want %q", event.Type, chatapp.EventMessage)
	}
	if event.Text != "hello bot" {
		t.Errorf("Text = %q, want %q (should be trimmed)", event.Text, "hello bot")
	}
	if event.ThreadID != "1712345678.123456" {
		t.Errorf("ThreadID should fall back to TimeStamp, got %q", event.ThreadID)
	}
}

func TestNormalizeMemberJoined(t *testing.T) {
	evt := &slackevents.MemberJoinedChannelEvent{
		User:    "U0BOT",
		Channel: "C0CHANNEL",
	}

	event := normalizeMemberJoined(evt)

	if event.Type != chatapp.EventSpaceJoin {
		t.Errorf("Type = %q, want %q", event.Type, chatapp.EventSpaceJoin)
	}
	if event.SpaceID != "C0CHANNEL" {
		t.Errorf("SpaceID = %q, want %q", event.SpaceID, "C0CHANNEL")
	}
}

func TestNormalizeMemberLeft(t *testing.T) {
	evt := &slackevents.MemberLeftChannelEvent{
		User:    "U0BOT",
		Channel: "C0CHANNEL",
	}

	event := normalizeMemberLeft(evt)

	if event.Type != chatapp.EventSpaceRemove {
		t.Errorf("Type = %q, want %q", event.Type, chatapp.EventSpaceRemove)
	}
}

func TestNormalizeInteraction_BlockActions(t *testing.T) {
	callback := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeBlockActions,
		Channel: slackapi.Channel{
			GroupConversation: slackapi.GroupConversation{
				Conversation: slackapi.Conversation{ID: "C0CHANNEL"},
			},
		},
		User: slackapi.User{ID: "U0USER"},
		ActionCallback: slackapi.ActionCallbacks{
			BlockActions: []*slackapi.BlockAction{
				{
					ActionID: "agent.start.test-agent",
					Value:    "test-agent",
				},
			},
		},
		Message: slackapi.Message{
			Msg: slackapi.Msg{
				Timestamp: "1712345678.123456",
			},
		},
	}

	event := normalizeInteraction(callback)

	if event == nil {
		t.Fatal("expected non-nil event")
	}
	if event.Type != chatapp.EventAction {
		t.Errorf("Type = %q, want %q", event.Type, chatapp.EventAction)
	}
	if event.ActionID != "agent.start.test-agent" {
		t.Errorf("ActionID = %q, want %q", event.ActionID, "agent.start.test-agent")
	}
	if event.ActionData != "test-agent" {
		t.Errorf("ActionData = %q, want %q", event.ActionData, "test-agent")
	}
	if event.ThreadID != "1712345678.123456" {
		t.Errorf("ThreadID = %q, want %q", event.ThreadID, "1712345678.123456")
	}
}

func TestNormalizeInteraction_ViewSubmission(t *testing.T) {
	callback := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeViewSubmission,
		User: slackapi.User{ID: "U0USER"},
		View: slackapi.View{
			CallbackID:      "agent.create",
			PrivateMetadata: "C0CHANNEL",
			State: &slackapi.ViewState{
				Values: map[string]map[string]slackapi.BlockAction{
					"name": {
						"name": {
							Type:  "plain_text_input",
							Value: "new-agent",
						},
					},
				},
			},
		},
	}

	event := normalizeInteraction(callback)

	if event == nil {
		t.Fatal("expected non-nil event")
	}
	if event.Type != chatapp.EventDialogSubmit {
		t.Errorf("Type = %q, want %q", event.Type, chatapp.EventDialogSubmit)
	}
	if event.ActionID != "agent.create" {
		t.Errorf("ActionID = %q, want %q", event.ActionID, "agent.create")
	}
	if event.SpaceID != "C0CHANNEL" {
		t.Errorf("SpaceID = %q, want from private_metadata %q", event.SpaceID, "C0CHANNEL")
	}
	if event.DialogData["name"] != "new-agent" {
		t.Errorf("DialogData[name] = %q, want %q", event.DialogData["name"], "new-agent")
	}
}

func TestNormalizeInteraction_EmptyBlockActions(t *testing.T) {
	callback := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeBlockActions,
		ActionCallback: slackapi.ActionCallbacks{
			BlockActions: []*slackapi.BlockAction{},
		},
	}

	event := normalizeInteraction(callback)
	if event != nil {
		t.Errorf("expected nil event for empty block actions, got %+v", event)
	}
}

func TestNormalizeInteraction_UnknownType(t *testing.T) {
	callback := slackapi.InteractionCallback{
		Type: "unknown_type",
	}

	event := normalizeInteraction(callback)
	if event != nil {
		t.Errorf("expected nil for unknown interaction type, got %+v", event)
	}
}

// --- stripBotMention tests ---

func TestStripBotMention(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<@U0ABC123> hello world", "hello world"},
		{"<@UBOTID>   tell deploy-agent to start", "tell deploy-agent to start"},
		{"<@U123>", ""},
		{"hello world", "hello world"},
		{"", ""},
		{"<@U0ABC123>hello", "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripBotMention(tt.input)
			if got != tt.want {
				t.Errorf("stripBotMention(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- IconProvider tests ---

func TestRobohashProviderIconURL(t *testing.T) {
	p := &robohashProvider{}
	url := p.IconURL("deploy-agent")
	want := "https://robohash.org/deploy-agent?set=set1&size=48x48"
	if url != want {
		t.Errorf("IconURL = %q, want %q", url, want)
	}
}

func TestRobohashProviderIconURL_SpecialChars(t *testing.T) {
	p := &robohashProvider{}
	url := p.IconURL("agent with spaces")
	if url == "" {
		t.Error("expected non-empty URL")
	}
	// Should be URL-encoded
	if url != "https://robohash.org/agent%20with%20spaces?set=set1&size=48x48" {
		t.Errorf("IconURL = %q, expected URL-encoded agent slug", url)
	}
}

// --- User cache tests ---

func TestUserCache_HitAndMiss(t *testing.T) {
	a := &Adapter{
		userCache: make(map[string]*cachedUser),
	}

	// Cache miss
	if got := a.getCachedUser("U123"); got != nil {
		t.Error("expected cache miss, got non-nil")
	}

	// Cache set
	user := &chatapp.ChatUser{
		PlatformID:  "U123",
		DisplayName: "Alice",
		Email:       "alice@example.com",
	}
	a.setCachedUser("U123", user)

	// Cache hit
	got := a.getCachedUser("U123")
	if got == nil {
		t.Fatal("expected cache hit, got nil")
	}
	if got.Email != "alice@example.com" {
		t.Errorf("cached email = %q, want %q", got.Email, "alice@example.com")
	}
}

func TestUserCache_Expiry(t *testing.T) {
	a := &Adapter{
		userCache: make(map[string]*cachedUser),
	}

	// Set expired entry
	a.userCache["U123"] = &cachedUser{
		user:      &chatapp.ChatUser{PlatformID: "U123"},
		fetchedAt: time.Now().Add(-userCacheTTL - time.Minute),
	}

	if got := a.getCachedUser("U123"); got != nil {
		t.Error("expected cache miss for expired entry, got non-nil")
	}
}

// --- Ephemeral command detection tests ---

func TestEphemeralCommands(t *testing.T) {
	tests := []struct {
		subcommand string
		want       bool
	}{
		{"help", true},
		{"info", true},
		{"register", true},
		{"unregister", true},
		{"list", false},
		{"status", false},
		{"start", false},
		{"stop", false},
		{"message", false},
		{"link", false},
	}

	for _, tt := range tests {
		t.Run(tt.subcommand, func(t *testing.T) {
			if got := ephemeralCommands[tt.subcommand]; got != tt.want {
				t.Errorf("ephemeralCommands[%q] = %v, want %v", tt.subcommand, got, tt.want)
			}
		})
	}
}

// --- Verify tests ---

func TestVerifyRequest_InvalidSecret(t *testing.T) {
	// An empty header set should fail verification
	err := verifyRequest(make(map[string][]string), []byte("body"), "secret")
	if err == nil {
		t.Error("expected error for missing signature headers")
	}
}

// --- Platform name constant test ---

func TestPlatformName(t *testing.T) {
	if PlatformName != "slack" {
		t.Errorf("PlatformName = %q, want %q", PlatformName, "slack")
	}
}
