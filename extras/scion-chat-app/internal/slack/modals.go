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
	"strings"

	slackapi "github.com/slack-go/slack"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/chatapp"
)

// renderModal converts a platform-agnostic Dialog to a Slack ModalViewRequest.
func renderModal(dialog *chatapp.Dialog) slackapi.ModalViewRequest {
	var blocks slackapi.Blocks

	for _, field := range dialog.Fields {
		switch field.Type {
		case "text":
			input := slackapi.NewPlainTextInputBlockElement(
				slackapi.NewTextBlockObject("plain_text", field.Placeholder, false, false),
				field.ID,
			)
			block := slackapi.NewInputBlock(
				field.ID,
				slackapi.NewTextBlockObject("plain_text", field.Label, false, false),
				nil,
				input,
			)
			block.Optional = !field.Required
			blocks.BlockSet = append(blocks.BlockSet, block)

		case "textarea":
			input := slackapi.NewPlainTextInputBlockElement(
				slackapi.NewTextBlockObject("plain_text", field.Placeholder, false, false),
				field.ID,
			)
			input.Multiline = true
			block := slackapi.NewInputBlock(
				field.ID,
				slackapi.NewTextBlockObject("plain_text", field.Label, false, false),
				nil,
				input,
			)
			block.Optional = !field.Required
			blocks.BlockSet = append(blocks.BlockSet, block)

		case "select":
			var options []*slackapi.OptionBlockObject
			for _, opt := range field.Options {
				options = append(options, slackapi.NewOptionBlockObject(
					opt.Value,
					slackapi.NewTextBlockObject("plain_text", opt.Label, false, false),
					nil,
				))
			}
			sel := slackapi.NewOptionsSelectBlockElement(
				"static_select", nil, field.ID, options...,
			)
			block := slackapi.NewInputBlock(
				field.ID,
				slackapi.NewTextBlockObject("plain_text", field.Label, false, false),
				nil,
				sel,
			)
			block.Optional = !field.Required
			blocks.BlockSet = append(blocks.BlockSet, block)

		case "checkbox":
			var options []*slackapi.OptionBlockObject
			for _, opt := range field.Options {
				options = append(options, slackapi.NewOptionBlockObject(
					opt.Value,
					slackapi.NewTextBlockObject("plain_text", opt.Label, false, false),
					nil,
				))
			}
			cb := slackapi.NewCheckboxGroupsBlockElement(field.ID, options...)
			block := slackapi.NewInputBlock(
				field.ID,
				slackapi.NewTextBlockObject("plain_text", field.Label, false, false),
				nil,
				cb,
			)
			block.Optional = !field.Required
			blocks.BlockSet = append(blocks.BlockSet, block)
		}
	}

	mvr := slackapi.ModalViewRequest{
		Type:       "modal",
		Title:      slackapi.NewTextBlockObject("plain_text", dialog.Title, false, false),
		CallbackID: dialog.Submit.ActionID,
		Blocks:     blocks,
	}
	if dialog.Submit.Label != "" {
		mvr.Submit = slackapi.NewTextBlockObject("plain_text", dialog.Submit.Label, false, false)
	}
	if dialog.Cancel.Label != "" {
		mvr.Close = slackapi.NewTextBlockObject("plain_text", dialog.Cancel.Label, false, false)
	}
	return mvr
}

// extractModalValues flattens the nested view.state.values structure from a
// Slack modal submission into a simple map keyed by action ID (and block ID
// as fallback), matching the ChatEvent.DialogData format expected by the
// CommandRouter.
func extractModalValues(state *slackapi.ViewState) map[string]string {
	if state == nil {
		return nil
	}
	result := make(map[string]string)
	for blockID, blockValues := range state.Values {
		for actionID, action := range blockValues {
			var val string
			switch action.Type {
			case "plain_text_input":
				val = action.Value
			case "static_select":
				val = action.SelectedOption.Value
			case "checkboxes":
				var vals []string
				for _, opt := range action.SelectedOptions {
					vals = append(vals, opt.Value)
				}
				val = strings.Join(vals, ",")
			default:
				val = action.Value
			}
			result[actionID] = val
			if _, exists := result[blockID]; !exists {
				result[blockID] = val
			}
		}
	}
	return result
}
