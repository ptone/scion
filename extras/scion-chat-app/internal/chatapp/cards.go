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

// Card represents a platform-agnostic rich card message.
type Card struct {
	Header   CardHeader
	Sections []CardSection
	Actions  []CardAction
}

// CardHeader contains the title area of a card.
type CardHeader struct {
	Title    string
	Subtitle string
	IconURL  string
}

// CardSection groups related widgets within a card.
type CardSection struct {
	Header  string
	Widgets []Widget
}

// Widget represents a single UI element within a card section.
type Widget struct {
	Type       WidgetType
	Label      string
	Content    string
	ActionID   string
	ActionData string
	Options    []SelectOption
}

// WidgetType identifies the kind of widget.
type WidgetType string

const (
	WidgetText     WidgetType = "text"
	WidgetKeyValue WidgetType = "key_value"
	WidgetButton   WidgetType = "button"
	WidgetDivider  WidgetType = "divider"
	WidgetImage    WidgetType = "image"
	WidgetInput    WidgetType = "input"
	WidgetCheckbox WidgetType = "checkbox"
)

// CardAction represents a button action attached to a card.
type CardAction struct {
	Label    string
	ActionID string
	Style    string // "primary", "danger", ""
}

// Dialog represents a modal dialog presented to the user.
type Dialog struct {
	Title  string
	Fields []DialogField
	Submit CardAction
	Cancel CardAction
}

// DialogField represents a single input field within a dialog.
type DialogField struct {
	ID          string
	Label       string
	Type        string // "text", "textarea", "select", "checkbox"
	Placeholder string
	Required    bool
	Options     []SelectOption
}

// SelectOption represents a choice in a select or checkbox widget.
type SelectOption struct {
	Label string
	Value string
}
