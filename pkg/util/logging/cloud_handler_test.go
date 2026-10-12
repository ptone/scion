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

package logging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	gcplog "cloud.google.com/go/logging"
)

func TestSlogLevelToSeverity(t *testing.T) {
	tests := []struct {
		level    slog.Level
		expected gcplog.Severity
	}{
		{slog.LevelDebug, gcplog.Debug},
		{slog.LevelInfo, gcplog.Info},
		{slog.LevelWarn, gcplog.Warning},
		{slog.LevelError, gcplog.Error},
		// Levels between standard ones
		{slog.LevelDebug - 1, gcplog.Debug},
		{slog.LevelInfo + 1, gcplog.Info},
		{slog.LevelWarn + 1, gcplog.Warning},
		{slog.LevelError + 1, gcplog.Error},
	}

	for _, tt := range tests {
		t.Run(tt.level.String(), func(t *testing.T) {
			got := slogLevelToSeverity(tt.level)
			if got != tt.expected {
				t.Errorf("slogLevelToSeverity(%v) = %v, want %v", tt.level, got, tt.expected)
			}
		})
	}
}

func TestAddAttrToMap(t *testing.T) {
	t.Run("simple string", func(t *testing.T) {
		m := make(map[string]any)
		addAttrToMap(m, slog.String("key", "value"))
		if m["key"] != "value" {
			t.Errorf("expected key=value, got %v", m["key"])
		}
	})

	t.Run("simple int", func(t *testing.T) {
		m := make(map[string]any)
		addAttrToMap(m, slog.Int("count", 42))
		if m["count"] != int64(42) {
			t.Errorf("expected count=42, got %v (%T)", m["count"], m["count"])
		}
	})

	t.Run("nested group", func(t *testing.T) {
		m := make(map[string]any)
		addAttrToMap(m, slog.Group("request",
			slog.String("method", "GET"),
			slog.Int("status", 200),
		))

		group, ok := m["request"].(map[string]any)
		if !ok {
			t.Fatalf("expected request to be a map, got %T", m["request"])
		}
		if group["method"] != "GET" {
			t.Errorf("expected method=GET, got %v", group["method"])
		}
		if group["status"] != int64(200) {
			t.Errorf("expected status=200, got %v", group["status"])
		}
	})

	t.Run("inline group (empty key)", func(t *testing.T) {
		m := make(map[string]any)
		addAttrToMap(m, slog.Group("",
			slog.String("a", "1"),
			slog.String("b", "2"),
		))

		// Inline groups merge into parent
		if m["a"] != "1" {
			t.Errorf("expected a=1, got %v", m["a"])
		}
		if m["b"] != "2" {
			t.Errorf("expected b=2, got %v", m["b"])
		}
	})
}

func TestResolveProjectID(t *testing.T) {
	t.Run("SCION_GCP_PROJECT_ID takes priority", func(t *testing.T) {
		t.Setenv(EnvGCPProjectID, "scion-project")
		t.Setenv(EnvGoogleCloudProject, "google-project")

		got := resolveProjectID()
		if got != "scion-project" {
			t.Errorf("resolveProjectID() = %s, want scion-project", got)
		}
	})

	t.Run("falls back to GOOGLE_CLOUD_PROJECT", func(t *testing.T) {
		t.Setenv(EnvGCPProjectID, "")
		t.Setenv(EnvGoogleCloudProject, "google-project")

		got := resolveProjectID()
		if got != "google-project" {
			t.Errorf("resolveProjectID() = %s, want google-project", got)
		}
	})

	t.Run("returns empty when neither set", func(t *testing.T) {
		t.Setenv(EnvGCPProjectID, "")
		t.Setenv(EnvGoogleCloudProject, "")

		got := resolveProjectID()
		if got != "" {
			t.Errorf("resolveProjectID() = %s, want empty", got)
		}
	})
}

func TestResolveLogID(t *testing.T) {
	t.Run("custom log ID", func(t *testing.T) {
		t.Setenv(EnvCloudLoggingLogID, "custom-log")

		got := resolveLogID()
		if got != "custom-log" {
			t.Errorf("resolveLogID() = %s, want custom-log", got)
		}
	})

	t.Run("default log ID", func(t *testing.T) {
		t.Setenv(EnvCloudLoggingLogID, "")

		got := resolveLogID()
		if got != "scion-server" {
			t.Errorf("resolveLogID() = %s, want scion-server", got)
		}
	})
}

func TestIsCloudLoggingEnabled(t *testing.T) {
	tests := []struct {
		envVal   string
		expected bool
	}{
		{"true", true},
		{"1", true},
		{"yes", true},
		{"false", false},
		{"", false},
		{"0", false},
		{"no", false},
		{"TRUE", false}, // Case sensitive
	}

	for _, tt := range tests {
		t.Run("value="+tt.envVal, func(t *testing.T) {
			t.Setenv(EnvCloudLogging, tt.envVal)
			got := isCloudLoggingEnabled()
			if got != tt.expected {
				t.Errorf("isCloudLoggingEnabled() = %v for %q, want %v", got, tt.envVal, tt.expected)
			}
		})
	}
}

func TestCloudHandler_Enabled(t *testing.T) {
	h := &CloudHandler{
		level:     slog.LevelInfo,
		component: "test",
	}

	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("should be enabled for Info level")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("should be enabled for Error level")
	}
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("should not be enabled for Debug level when level is Info")
	}
}

func TestCloudHandler_WithAttrs(t *testing.T) {
	h := &CloudHandler{
		level:     slog.LevelInfo,
		component: "test",
	}

	newH := h.WithAttrs([]slog.Attr{slog.String("key", "value")})

	ch, ok := newH.(*CloudHandler)
	if !ok {
		t.Fatal("WithAttrs should return a *CloudHandler")
	}
	if len(ch.attrs) != 1 {
		t.Errorf("expected 1 attr, got %d", len(ch.attrs))
	}
	if ch.component != "test" {
		t.Error("component should be preserved")
	}
	if ch.level != slog.LevelInfo {
		t.Error("level should be preserved")
	}

	// Original should be unchanged
	if len(h.attrs) != 0 {
		t.Error("original handler should not be modified")
	}
}

func TestCloudHandler_WithGroup(t *testing.T) {
	h := &CloudHandler{
		level:     slog.LevelInfo,
		component: "test",
	}

	newH := h.WithGroup("mygroup")

	ch, ok := newH.(*CloudHandler)
	if !ok {
		t.Fatal("WithGroup should return a *CloudHandler")
	}
	if len(ch.groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(ch.groups))
	}
	if ch.groups[0] != "mygroup" {
		t.Errorf("expected group 'mygroup', got %s", ch.groups[0])
	}

	// Original should be unchanged
	if len(h.groups) != 0 {
		t.Error("original handler should not be modified")
	}
}

func TestCloudHandler_WithAttrsChain(t *testing.T) {
	h := &CloudHandler{
		level:     slog.LevelInfo,
		component: "test",
		attrs:     []slog.Attr{slog.String("existing", "attr")},
	}

	newH := h.WithAttrs([]slog.Attr{slog.String("new", "attr")}).(*CloudHandler)

	if len(newH.attrs) != 2 {
		t.Errorf("expected 2 attrs after chain, got %d", len(newH.attrs))
	}
}

func TestCloudHandler_WithGroupChain(t *testing.T) {
	h := &CloudHandler{
		level:     slog.LevelInfo,
		component: "test",
		groups:    []string{"outer"},
	}

	newH := h.WithGroup("inner").(*CloudHandler)

	if len(newH.groups) != 2 {
		t.Errorf("expected 2 groups after chain, got %d", len(newH.groups))
	}
	if newH.groups[0] != "outer" || newH.groups[1] != "inner" {
		t.Errorf("expected [outer, inner], got %v", newH.groups)
	}
}

func TestEnvVarCloudLoggingConstants(t *testing.T) {
	if EnvCloudLogging != "SCION_CLOUD_LOGGING" {
		t.Errorf("EnvCloudLogging = %s, want SCION_CLOUD_LOGGING", EnvCloudLogging)
	}
	if EnvCloudLoggingLogID != "SCION_CLOUD_LOGGING_LOG_ID" {
		t.Errorf("EnvCloudLoggingLogID = %s, want SCION_CLOUD_LOGGING_LOG_ID", EnvCloudLoggingLogID)
	}
	if EnvGCPProjectID != "SCION_GCP_PROJECT_ID" {
		t.Errorf("EnvGCPProjectID = %s, want SCION_GCP_PROJECT_ID", EnvGCPProjectID)
	}
	if EnvGoogleCloudProject != "GOOGLE_CLOUD_PROJECT" {
		t.Errorf("EnvGoogleCloudProject = %s, want GOOGLE_CLOUD_PROJECT", EnvGoogleCloudProject)
	}
}

func TestResolveLogLevel(t *testing.T) {
	tests := []struct {
		name     string
		debug    bool
		logLevel string
		debugEnv string
		want     slog.Level
	}{
		{name: "debug flag", debug: true, logLevel: "error", want: slog.LevelDebug},
		{name: "env var debug", logLevel: "debug", want: slog.LevelDebug},
		// The floor is clamped at info: request and message logs must keep
		// their Info entries even when the main log is raised to warn/error.
		{name: "env var warn clamps to info", logLevel: "warn", want: slog.LevelInfo},
		{name: "env var error clamps to info", logLevel: "error", want: slog.LevelInfo},
		{name: "components above info clamp to info", logLevel: "error,hub.web=warn", want: slog.LevelInfo},
		{name: "component debug lowers the floor", logLevel: "error,hub.auth=debug", want: slog.LevelDebug},
		{name: "deprecated SCION_DEBUG", debugEnv: "1", want: slog.LevelDebug},
		{name: "default info", want: slog.LevelInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetLevelState(t)
			t.Setenv("SCION_LOG_LEVEL", tt.logLevel)
			t.Setenv("SCION_DEBUG", tt.debugEnv)
			if got := ResolveLogLevel(tt.debug); got != tt.want {
				t.Errorf("ResolveLogLevel(%v) = %v, want %v", tt.debug, got, tt.want)
			}
		})
	}
}

func TestPromoteAttrToLabels(t *testing.T) {
	t.Run("agent_id promoted", func(t *testing.T) {
		labels := map[string]string{}
		promoteAttrToLabels(labels, slog.String(AttrAgentID, "agent-123"))
		if labels[AttrAgentID] != "agent-123" {
			t.Errorf("expected agent_id=agent-123, got %v", labels[AttrAgentID])
		}
	})

	t.Run("project_id promoted", func(t *testing.T) {
		labels := map[string]string{}
		promoteAttrToLabels(labels, slog.String(AttrProjectID, "project-456"))
		if labels[AttrProjectID] != "project-456" {
			t.Errorf("expected project_id=project-456, got %v", labels[AttrProjectID])
		}
	})

	t.Run("empty values not promoted", func(t *testing.T) {
		labels := map[string]string{}
		promoteAttrToLabels(labels, slog.String(AttrAgentID, ""))
		if _, ok := labels[AttrAgentID]; ok {
			t.Error("empty agent_id should not be promoted")
		}
	})

	t.Run("broker_id promoted", func(t *testing.T) {
		labels := map[string]string{}
		promoteAttrToLabels(labels, slog.String(AttrBrokerID, "broker-west-1"))
		if labels[AttrBrokerID] != "broker-west-1" {
			t.Errorf("expected broker_id=broker-west-1, got %v", labels[AttrBrokerID])
		}
	})

	t.Run("unrelated attrs ignored", func(t *testing.T) {
		labels := map[string]string{}
		promoteAttrToLabels(labels, slog.String("other_key", "value"))
		if len(labels) != 0 {
			t.Errorf("expected no labels, got %v", labels)
		}
	})
}

func TestNewCloudHandler_NoProject(t *testing.T) {
	t.Setenv(EnvGCPProjectID, "")
	t.Setenv(EnvGoogleCloudProject, "")

	_, _, err := NewCloudHandler(context.Background(), CloudLoggingConfig{}, slog.LevelInfo)
	if err == nil {
		t.Error("expected error when no project ID available")
	}
}

func TestMapToCloudHTTPRequest(t *testing.T) {
	m := map[string]any{
		"requestMethod": "POST",
		"requestUrl":    "/api/v1/projects",
		"requestSize":   int64(256),
		"status":        int64(201),
		"responseSize":  int64(128),
		"userAgent":     "scion-cli/0.1.0",
		"remoteIp":      "10.0.0.1:54321",
		"referer":       "https://example.com",
		"latency":       "0.042s",
		"protocol":      "HTTP/1.1",
	}

	req := mapToCloudHTTPRequest(m)
	if req == nil {
		t.Fatal("expected non-nil HTTPRequest")
	}
	if req.Request.Method != "POST" {
		t.Errorf("expected method POST, got %s", req.Request.Method)
	}
	if req.Request.URL.String() != "/api/v1/projects" {
		t.Errorf("expected URL /api/v1/projects, got %s", req.Request.URL.String())
	}
	if req.RequestSize != 256 {
		t.Errorf("expected requestSize=256, got %d", req.RequestSize)
	}
	if req.Status != 201 {
		t.Errorf("expected status=201, got %d", req.Status)
	}
	if req.ResponseSize != 128 {
		t.Errorf("expected responseSize=128, got %d", req.ResponseSize)
	}
	if req.Request.UserAgent() != "scion-cli/0.1.0" {
		t.Errorf("expected userAgent=scion-cli/0.1.0, got %s", req.Request.UserAgent())
	}
	if req.RemoteIP != "10.0.0.1:54321" {
		t.Errorf("expected remoteIp=10.0.0.1:54321, got %s", req.RemoteIP)
	}
	if req.Request.Referer() != "https://example.com" {
		t.Errorf("expected referer=https://example.com, got %s", req.Request.Referer())
	}
	if req.Latency.Milliseconds() != 42 {
		t.Errorf("expected latency=42ms, got %v", req.Latency)
	}
	if req.Request.Proto != "HTTP/1.1" {
		t.Errorf("expected protocol=HTTP/1.1, got %s", req.Request.Proto)
	}
}

func TestMapToCloudHTTPRequest_EmptyMap(t *testing.T) {
	req := mapToCloudHTTPRequest(map[string]any{})
	if req == nil {
		t.Fatal("expected non-nil HTTPRequest even for empty map")
	}
	if req.Request == nil {
		t.Fatal("expected non-nil underlying Request")
	}
	if req.Status != 0 {
		t.Errorf("expected status=0, got %d", req.Status)
	}
}

func TestCloudHandler_HostnameField(t *testing.T) {
	h := &CloudHandler{
		level:     slog.LevelInfo,
		component: "test",
		hostname:  "my-workstation",
	}

	// Verify hostname is preserved through WithAttrs
	newH := h.WithAttrs([]slog.Attr{slog.String("key", "value")}).(*CloudHandler)
	if newH.hostname != "my-workstation" {
		t.Errorf("expected hostname=my-workstation, got %s", newH.hostname)
	}

	// Verify hostname is preserved through WithGroup
	newH = h.WithGroup("grp").(*CloudHandler)
	if newH.hostname != "my-workstation" {
		t.Errorf("expected hostname=my-workstation, got %s", newH.hostname)
	}
}

// handleOne runs one record with attrs through h and returns the payload
// both as stored and as Cloud Logging encodes it (JSON round trip).
func handleOne(t *testing.T, h slog.Handler, entries *[]gcplog.Entry, attrs ...slog.Attr) (map[string]any, map[string]any) {
	t.Helper()
	*entries = nil
	r := slog.NewRecord(time.Now(), slog.LevelWarn, "msg", 0)
	r.AddAttrs(attrs...)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(*entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(*entries))
	}
	payload, ok := (*entries)[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload is %T, want map[string]any", (*entries)[0].Payload)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("payload does not encode to JSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("decoding payload JSON: %v", err)
	}
	return payload, decoded
}

type errLogValuer struct{ err error }

func (v errLogValuer) LogValue() slog.Value { return slog.AnyValue(v.err) }

func TestCloudHandler_ErrorAttrsWrittenAsMessage(t *testing.T) {
	var entries []gcplog.Entry
	h := newTestCloudHandler("scion-hub", "v1", slog.LevelInfo, &entries)
	base := errors.New("connection refused")
	wrapped := fmt.Errorf("dialing store: %w", base)

	t.Run("plain", func(t *testing.T) {
		_, got := handleOne(t, h, &entries, slog.Any("error", base))
		if got["error"] != "connection refused" {
			t.Errorf("error = %#v, want %q", got["error"], "connection refused")
		}
	})

	t.Run("wrapped", func(t *testing.T) {
		_, got := handleOne(t, h, &entries, slog.Any("error", wrapped))
		if got["error"] != "dialing store: connection refused" {
			t.Errorf("error = %#v, want wrapped message", got["error"])
		}
	})

	t.Run("in group", func(t *testing.T) {
		_, got := handleOne(t, h, &entries, slog.Group("op", slog.Any("error", wrapped)))
		group, ok := got["op"].(map[string]any)
		if !ok {
			t.Fatalf("op = %#v, want object", got["op"])
		}
		if group["error"] != "dialing store: connection refused" {
			t.Errorf("op.error = %#v, want wrapped message", group["error"])
		}
	})

	t.Run("LogValuer resolving to error", func(t *testing.T) {
		_, got := handleOne(t, h, &entries, slog.Any("error", errLogValuer{base}))
		if got["error"] != "connection refused" {
			t.Errorf("error = %#v, want %q", got["error"], "connection refused")
		}
	})

	t.Run("WithAttrs under WithGroup", func(t *testing.T) {
		hg := h.WithGroup("req").WithAttrs([]slog.Attr{slog.Any("cause", base)})
		_, got := handleOne(t, hg, &entries, slog.Any("error", wrapped))
		group, ok := got["req"].(map[string]any)
		if !ok {
			t.Fatalf("req = %#v, want object", got["req"])
		}
		if group["cause"] != "connection refused" {
			t.Errorf("req.cause = %#v, want %q", group["cause"], "connection refused")
		}
		if group["error"] != "dialing store: connection refused" {
			t.Errorf("req.error = %#v, want wrapped message", group["error"])
		}
	})
}

type opaqueValue struct{ id int }

type opaqueStringer struct{ name string }

func (s opaqueStringer) String() string { return "stringer:" + s.name }

type exportedValue struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestCloudHandler_AttrValuesEncodeOrFallBackToString(t *testing.T) {
	var entries []gcplog.Entry
	h := newTestCloudHandler("scion-hub", "v1", slog.LevelInfo, &entries)

	fallbacks := []struct {
		name string
		val  any
		want string
	}{
		{"unexported fields only", opaqueValue{id: 7}, "{7}"},
		{"pointer to unexported fields", &opaqueValue{id: 7}, "&{7}"},
		{"Stringer with unexported fields", opaqueStringer{name: "x"}, "stringer:x"},
		// A channel fails to marshal; fmt.Sprint writes its address.
		{"marshal failure", make(chan int), "0x"},
	}
	for _, tt := range fallbacks {
		t.Run("fallback/"+tt.name, func(t *testing.T) {
			_, got := handleOne(t, h, &entries, slog.Any("v", tt.val))
			s, ok := got["v"].(string)
			if !ok {
				t.Fatalf("v = %#v (%T), want string", got["v"], got["v"])
			}
			if tt.name == "marshal failure" {
				if !strings.HasPrefix(s, tt.want) {
					t.Errorf("v = %q, want prefix %q", s, tt.want)
				}
				return
			}
			if s != tt.want {
				t.Errorf("v = %q, want %q", s, tt.want)
			}
		})
	}

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var nilPtr *exportedValue
	unchanged := []struct {
		name string
		attr slog.Attr
		want any
	}{
		{"string", slog.String("v", "s"), "s"},
		{"int", slog.Int("v", 3), int64(3)},
		{"bool", slog.Bool("v", true), true},
		{"time", slog.Time("v", now), now},
		{"duration", slog.Duration("v", time.Second), time.Second},
		{"map", slog.Any("v", map[string]any{"a": 1}), map[string]any{"a": 1}},
		{"empty map", slog.Any("v", map[string]any{}), map[string]any{}},
		{"slice", slog.Any("v", []string{"a", "b"}), []string{"a", "b"}},
		{"encodable struct", slog.Any("v", exportedValue{Name: "n", Count: 2}), exportedValue{Name: "n", Count: 2}},
		{"nil", slog.Any("v", nil), nil},
		{"typed nil pointer", slog.Any("v", nilPtr), nilPtr},
	}
	for _, tt := range unchanged {
		t.Run("unchanged/"+tt.name, func(t *testing.T) {
			stored, _ := handleOne(t, h, &entries, tt.attr)
			if want, ok := tt.want.(time.Time); ok {
				if got, ok := stored["v"].(time.Time); !ok || !got.Equal(want) {
					t.Errorf("v = %#v, want %v", stored["v"], want)
				}
				return
			}
			if !reflect.DeepEqual(stored["v"], tt.want) {
				t.Errorf("v = %#v (%T), want %#v (%T)", stored["v"], stored["v"], tt.want, tt.want)
			}
		})
	}
}
