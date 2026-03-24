package fswatcher

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestLogger_Write(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf)

	size := int64(1024)
	ts := time.Date(2026, 3, 24, 14, 32, 1, 3000000, time.UTC)
	ev := Event{
		Timestamp: ts,
		AgentID:   "frontend-refactor",
		Action:    ActionModify,
		Path:      "web/src/client/App.tsx",
		Size:      &size,
	}

	if err := logger.Write(ev); err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %s", err, buf.String())
	}

	if decoded["agent_id"] != "frontend-refactor" {
		t.Errorf("agent_id = %v, want frontend-refactor", decoded["agent_id"])
	}
	if decoded["action"] != "modify" {
		t.Errorf("action = %v, want modify", decoded["action"])
	}
	if decoded["path"] != "web/src/client/App.tsx" {
		t.Errorf("path = %v, want web/src/client/App.tsx", decoded["path"])
	}
	if decoded["size"] != float64(1024) {
		t.Errorf("size = %v, want 1024", decoded["size"])
	}
}

func TestLogger_Write_DeleteOmitsSize(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf)

	ev := Event{
		Timestamp: time.Now().UTC(),
		AgentID:   "agent-a",
		Action:    ActionDelete,
		Path:      "old-file.txt",
	}

	if err := logger.Write(ev); err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}

	if _, ok := decoded["size"]; ok {
		t.Error("expected size to be omitted for delete events")
	}
}

func TestLogger_Write_MultipleEvents(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf)

	for i := range 3 {
		ev := Event{
			Timestamp: time.Now().UTC(),
			AgentID:   "agent",
			Action:    ActionCreate,
			Path:      "file" + string(rune('0'+i)) + ".go",
		}
		if err := logger.Write(ev); err != nil {
			t.Fatal(err)
		}
	}

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}

	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal(line, &decoded); err != nil {
			t.Errorf("invalid JSON line: %v", err)
		}
	}
}
