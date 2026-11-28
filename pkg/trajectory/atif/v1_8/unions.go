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

// Package v1_8 provides Go types and JSON Schema validation for Harbor's
// ATIF-v1.8 (Agent Trajectory Interchange Format) specification.
//
//go:generate go run gen_types.go
package v1_8

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MessageContent represents ATIF's `str | list[ContentPart]` union used on
// Step.message and ObservationResult.content.
type MessageContent struct {
	Text  string
	Parts []ContentPart
	// IsParts selects the multimodal array representation when true.
	IsParts bool
}

// TextMessage returns a plain-string MessageContent.
func TextMessage(s string) MessageContent {
	return MessageContent{Text: s}
}

// PartsMessage returns a multimodal ContentPart-slice MessageContent.
func PartsMessage(parts []ContentPart) MessageContent {
	return MessageContent{Parts: parts, IsParts: true}
}

// MarshalJSON encodes MessageContent as either a JSON string or a JSON array of
// ContentPart objects.
func (m MessageContent) MarshalJSON() ([]byte, error) {
	if m.IsParts {
		parts := m.Parts
		if parts == nil {
			parts = []ContentPart{}
		}
		return json.Marshal(parts)
	}
	return json.Marshal(m.Text)
}

// UnmarshalJSON decodes a JSON string or array of ContentPart into MessageContent.
func (m *MessageContent) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*m = MessageContent{}
		return nil
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*m = MessageContent{Text: s}
		return nil
	case '[':
		var parts []ContentPart
		if err := json.Unmarshal(trimmed, &parts); err != nil {
			return err
		}
		*m = MessageContent{Parts: parts, IsParts: true}
		return nil
	default:
		return fmt.Errorf("atif MessageContent: expected JSON string or array, got %c", trimmed[0])
	}
}

// ReasoningEffort represents ATIF's `str | float` union on Step.reasoning_effort.
type ReasoningEffort struct {
	String *string
	Number *float64
}

// MarshalJSON encodes ReasoningEffort as either a JSON string or a JSON number.
func (r ReasoningEffort) MarshalJSON() ([]byte, error) {
	if r.String != nil {
		return json.Marshal(*r.String)
	}
	if r.Number != nil {
		return json.Marshal(*r.Number)
	}
	return []byte("null"), nil
}

// UnmarshalJSON decodes a JSON string or number into ReasoningEffort.
func (r *ReasoningEffort) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*r = ReasoningEffort{}
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*r = ReasoningEffort{String: &s}
		return nil
	}
	var n float64
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return fmt.Errorf("atif ReasoningEffort: expected string or number: %w", err)
	}
	*r = ReasoningEffort{Number: &n}
	return nil
}

// MediaSource represents ATIF's `ImageSource | AudioSource` union on
// ContentPart.source.
type MediaSource struct {
	Image *ImageSource
	Audio *AudioSource
}

// MarshalJSON encodes MediaSource as either an ImageSource or AudioSource.
func (s MediaSource) MarshalJSON() ([]byte, error) {
	if s.Image != nil {
		return json.Marshal(s.Image)
	}
	if s.Audio != nil {
		return json.Marshal(s.Audio)
	}
	return []byte("null"), nil
}

// UnmarshalJSON decodes an ImageSource or AudioSource by inspecting media_type.
func (s *MediaSource) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*s = MediaSource{}
		return nil
	}
	var peek struct {
		MediaType string `json:"media_type"`
	}
	if err := json.Unmarshal(trimmed, &peek); err != nil {
		return fmt.Errorf("atif MediaSource: %w", err)
	}
	switch {
	case strings.HasPrefix(peek.MediaType, "image/"):
		var img ImageSource
		if err := json.Unmarshal(trimmed, &img); err != nil {
			return err
		}
		*s = MediaSource{Image: &img}
		return nil
	case strings.HasPrefix(peek.MediaType, "audio/"):
		var aud AudioSource
		if err := json.Unmarshal(trimmed, &aud); err != nil {
			return err
		}
		*s = MediaSource{Audio: &aud}
		return nil
	default:
		return errors.New("atif MediaSource: media_type must start with image/ or audio/")
	}
}
