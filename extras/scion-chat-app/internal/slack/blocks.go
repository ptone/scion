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
	slackapi "github.com/slack-go/slack"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/chatapp"
)

// renderBlocks converts a platform-agnostic Card to Slack Block Kit blocks.
func renderBlocks(card *chatapp.Card) []slackapi.Block {
	var blocks []slackapi.Block

	if card.Header.Title != "" {
		blocks = append(blocks, slackapi.NewHeaderBlock(
			slackapi.NewTextBlockObject("plain_text", card.Header.Title, false, false),
		))
		if card.Header.Subtitle != "" {
			blocks = append(blocks, slackapi.NewContextBlock("",
				slackapi.NewTextBlockObject("mrkdwn", card.Header.Subtitle, false, false),
			))
		}
	}

	for _, section := range card.Sections {
		if section.Header != "" {
			blocks = append(blocks, slackapi.NewSectionBlock(
				slackapi.NewTextBlockObject("mrkdwn", "*"+section.Header+"*", false, false),
				nil, nil,
			))
		}
		for _, widget := range section.Widgets {
			blocks = append(blocks, renderWidget(&widget)...)
		}
	}

	if len(card.Actions) > 0 {
		var buttons []slackapi.BlockElement
		for _, action := range card.Actions {
			btn := slackapi.NewButtonBlockElement(
				action.ActionID,
				action.ActionID,
				slackapi.NewTextBlockObject("plain_text", action.Label, false, false),
			)
			switch action.Style {
			case "primary":
				btn.Style = slackapi.StylePrimary
			case "danger":
				btn.Style = slackapi.StyleDanger
			}
			buttons = append(buttons, btn)
		}
		blocks = append(blocks, slackapi.NewActionBlock("", buttons...))
	}

	return blocks
}

// renderWidget converts a single Widget to one or more Block Kit blocks.
func renderWidget(w *chatapp.Widget) []slackapi.Block {
	switch w.Type {
	case chatapp.WidgetText:
		return []slackapi.Block{
			slackapi.NewSectionBlock(
				slackapi.NewTextBlockObject("mrkdwn", w.Content, false, false),
				nil, nil,
			),
		}

	case chatapp.WidgetKeyValue:
		return []slackapi.Block{
			slackapi.NewSectionBlock(nil,
				[]*slackapi.TextBlockObject{
					slackapi.NewTextBlockObject("mrkdwn", "*"+w.Label+"*", false, false),
					slackapi.NewTextBlockObject("mrkdwn", w.Content, false, false),
				},
				nil,
			),
		}

	case chatapp.WidgetButton:
		btn := slackapi.NewButtonBlockElement(
			w.ActionID, w.ActionData,
			slackapi.NewTextBlockObject("plain_text", w.Label, false, false),
		)
		return []slackapi.Block{slackapi.NewActionBlock("", btn)}

	case chatapp.WidgetDivider:
		return []slackapi.Block{slackapi.NewDividerBlock()}

	case chatapp.WidgetImage:
		return []slackapi.Block{
			slackapi.NewImageBlock(w.Content, w.Label, "", nil),
		}

	case chatapp.WidgetInput:
		input := slackapi.NewPlainTextInputBlockElement(
			slackapi.NewTextBlockObject("plain_text", w.Label, false, false),
			w.ActionID,
		)
		return []slackapi.Block{
			slackapi.NewInputBlock(
				w.ActionID,
				slackapi.NewTextBlockObject("plain_text", w.Label, false, false),
				nil,
				input,
			),
		}

	case chatapp.WidgetCheckbox:
		var options []*slackapi.OptionBlockObject
		for _, opt := range w.Options {
			options = append(options, slackapi.NewOptionBlockObject(
				opt.Value,
				slackapi.NewTextBlockObject("plain_text", opt.Label, false, false),
				nil,
			))
		}
		checkboxes := slackapi.NewCheckboxGroupsBlockElement(w.ActionID, options...)
		return []slackapi.Block{
			slackapi.NewInputBlock(
				w.ActionID,
				slackapi.NewTextBlockObject("plain_text", w.Label, false, false),
				nil,
				checkboxes,
			),
		}

	default:
		return nil
	}
}
