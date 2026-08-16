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

package hub

import (
	"encoding/json"
	"testing"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
)

// TestBuildSnapshotFromKoanf_NativeChat covers the postgres-mode read path:
// the merged koanf (bootstrap + DB sections) must surface the toggle so
// ApplySnapshot can act on it.
func TestBuildSnapshotFromKoanf_NativeChat(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]interface{}
		want *bool
	}{
		{"absent leaves the toggle unset", map[string]interface{}{}, nil},
		{"explicit false", map[string]interface{}{"server.native_chat.enabled": false}, ptrBool(false)},
		{"explicit true", map[string]interface{}{"server.native_chat.enabled": true}, ptrBool(true)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := koanf.New(".")
			if err := k.Load(confmap.Provider(tc.raw, "."), nil); err != nil {
				t.Fatalf("load koanf: %v", err)
			}

			snap := buildSnapshotFromKoanf(k)
			switch {
			case tc.want == nil && snap.NativeChatEnabled != nil:
				t.Errorf("NativeChatEnabled = %v, want nil", *snap.NativeChatEnabled)
			case tc.want != nil && snap.NativeChatEnabled == nil:
				t.Errorf("NativeChatEnabled = nil, want %v", *tc.want)
			case tc.want != nil && *snap.NativeChatEnabled != *tc.want:
				t.Errorf("NativeChatEnabled = %v, want %v", *snap.NativeChatEnabled, *tc.want)
			}
		})
	}
}

// TestBuildLayer1SnapshotFromFile_NativeChat covers the file-mode read path.
// An absent server.native_chat block must stay nil so the compiled default-on
// behavior survives a reload.
func TestBuildLayer1SnapshotFromFile_NativeChat(t *testing.T) {
	disabled := false

	snap := BuildLayer1SnapshotFromFile(&config.GlobalConfig{
		NativeChat: &config.V1NativeChatConfig{Enabled: &disabled},
	})
	if snap.NativeChatEnabled == nil {
		t.Fatal("NativeChatEnabled = nil, want false")
	}
	if *snap.NativeChatEnabled {
		t.Error("NativeChatEnabled = true, want false")
	}

	if got := BuildLayer1SnapshotFromFile(&config.GlobalConfig{}); got.NativeChatEnabled != nil {
		t.Errorf("NativeChatEnabled = %v for absent config, want nil", *got.NativeChatEnabled)
	}
}

// TestExtractKoanfKeys_NativeChatRoundTrip walks the DB-mode PUT path:
// request → koanf keys → classification → section doc. Before native_chat
// became Layer-1 the toggle was rejected as a bootstrap key, so the admin UI
// omitted it from the payload entirely.
func TestExtractKoanfKeys_NativeChatRoundTrip(t *testing.T) {
	disabled := false
	req := &ServerConfigUpdateRequest{
		Server: &config.V1ServerConfig{
			NativeChat: &config.V1NativeChatConfig{Enabled: &disabled},
		},
	}

	keys := extractKoanfKeysFromRequest(req)
	found := false
	for _, k := range keys {
		if k == "server.native_chat.enabled" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no server.native_chat.enabled key emitted; got %v", keys)
	}

	layer1BySec, layer0Keys, _ := opsettings.ClassifyKeys(keys)
	if len(layer0Keys) > 0 {
		t.Errorf("unexpected Layer-0 keys: %v", layer0Keys)
	}
	if len(layer1BySec["native_chat"]) == 0 {
		t.Fatalf("no native_chat section classified; layer1BySec = %v", layer1BySec)
	}

	rawBody := []byte(`{"server":{"native_chat":{"enabled":false}}}`)
	docs, err := buildSectionDocsFromRequest(req, layer1BySec, rawBody)
	if err != nil {
		t.Fatalf("buildSectionDocsFromRequest: %v", err)
	}
	doc, ok := docs["native_chat"]
	if !ok {
		t.Fatal("native_chat section doc not produced")
	}

	var settings opsettings.NativeChatSettings
	if err := json.Unmarshal(doc, &settings); err != nil {
		t.Fatalf("unmarshal native_chat doc: %v", err)
	}
	if settings.Enabled == nil {
		t.Fatal("native_chat doc dropped the enabled field")
	}
	if *settings.Enabled {
		t.Error("native_chat doc has enabled=true, want false")
	}
}

// TestExtractKoanfKeys_NativeChatOmitted verifies that a payload without the
// toggle emits no native_chat key — an unrelated PUT must not overwrite the
// stored section.
func TestExtractKoanfKeys_NativeChatOmitted(t *testing.T) {
	req := &ServerConfigUpdateRequest{
		Server: &config.V1ServerConfig{NativeChat: &config.V1NativeChatConfig{}},
	}
	for _, k := range extractKoanfKeysFromRequest(req) {
		if k == "server.native_chat.enabled" {
			t.Error("emitted server.native_chat.enabled for an empty native_chat object")
		}
	}
}

func ptrBool(v bool) *bool { return &v }
