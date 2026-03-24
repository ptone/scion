package logparser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseLogFile(t *testing.T) {
	// Create a test log file
	entries := []GCPLogEntry{
		{
			InsertID:  "1",
			Timestamp: "2026-03-22T16:30:00.000Z",
			Severity:  "INFO",
			LogName:   "projects/test/logs/scion-agents",
			Labels: map[string]string{
				"agent_id":      "agent-1",
				"scion.harness": "gemini",
			},
			JSONPayload: map[string]any{
				"message": "agent.lifecycle.pre_start",
			},
		},
		{
			InsertID:  "2",
			Timestamp: "2026-03-22T16:30:01.000Z",
			Severity:  "INFO",
			LogName:   "projects/test/logs/scion-agents",
			Labels: map[string]string{
				"agent_id":      "agent-1",
				"scion.harness": "gemini",
			},
			JSONPayload: map[string]any{
				"message": "agent.session.start",
			},
		},
		{
			InsertID:  "3",
			Timestamp: "2026-03-22T16:30:05.000Z",
			Severity:  "INFO",
			LogName:   "projects/test/logs/scion-agents",
			Labels: map[string]string{
				"agent_id":      "agent-1",
				"scion.harness": "gemini",
			},
			JSONPayload: map[string]any{
				"message":   "agent.tool.call",
				"tool_name": "write_file",
				"file_path": "/workspace/src/main.go",
			},
		},
		{
			InsertID:  "4",
			Timestamp: "2026-03-22T16:30:10.000Z",
			Severity:  "INFO",
			LogName:   "projects/test/logs/scion-messages",
			Labels: map[string]string{
				"sender":       "agent:alpha",
				"sender_id":    "agent-1",
				"recipient":    "agent:beta",
				"recipient_id": "agent-2",
				"msg_type":     "instruction",
				"grove_id":     "grove-1",
			},
			JSONPayload: map[string]any{
				"sender":          "agent:alpha",
				"recipient":       "agent:beta",
				"msg_type":        "instruction",
				"message_content": "do something",
				"message":         "message dispatched",
			},
		},
		{
			InsertID:  "5",
			Timestamp: "2026-03-22T16:30:15.000Z",
			Severity:  "INFO",
			LogName:   "projects/test/logs/scion-agents",
			Labels: map[string]string{
				"agent_id":      "agent-2",
				"scion.harness": "claude",
			},
			JSONPayload: map[string]any{
				"message": "agent.session.start",
			},
		},
		{
			InsertID:  "6",
			Timestamp: "2026-03-22T16:31:00.000Z",
			Severity:  "INFO",
			LogName:   "projects/test/logs/scion-agents",
			Labels: map[string]string{
				"agent_id":      "agent-1",
				"scion.harness": "gemini",
			},
			JSONPayload: map[string]any{
				"message": "agent.session.end",
			},
		},
	}

	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test-logs.json")
	if err := os.WriteFile(logPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ParseLogFile(logPath)
	if err != nil {
		t.Fatalf("ParseLogFile failed: %v", err)
	}

	// Verify manifest
	if result.Manifest.Type != "manifest" {
		t.Errorf("expected manifest type, got %s", result.Manifest.Type)
	}

	// Verify agents found
	if len(result.Manifest.Agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(result.Manifest.Agents))
	}

	// Verify agent names resolved from messages
	agentNames := map[string]bool{}
	for _, a := range result.Manifest.Agents {
		agentNames[a.Name] = true
	}
	if !agentNames["alpha"] {
		t.Error("expected agent name 'alpha' from message sender")
	}

	// Files are now empty in manifest (added dynamically via events)
	if len(result.Manifest.Files) != 0 {
		t.Errorf("expected empty files in manifest, got %d", len(result.Manifest.Files))
	}

	// Verify events
	if len(result.Events) == 0 {
		t.Fatal("expected events, got none")
	}

	// Count event types
	typeCounts := map[string]int{}
	for _, e := range result.Events {
		typeCounts[e.Type]++
	}
	if typeCounts["agent_state"] == 0 {
		t.Error("expected agent_state events")
	}
	if typeCounts["message"] != 1 {
		t.Errorf("expected 1 message event, got %d", typeCounts["message"])
	}
	if typeCounts["file_edit"] != 1 {
		t.Errorf("expected 1 file_edit event, got %d", typeCounts["file_edit"])
	}

	// Verify time range
	if result.Manifest.TimeRange.Start != "2026-03-22T16:30:00.000Z" {
		t.Errorf("unexpected start time: %s", result.Manifest.TimeRange.Start)
	}
}

func TestIsFileEditTool(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{"write_file", true},
		{"create_file", true},
		{"Write", true},
		{"edit_file", true},
		{"Edit", true},
		{"patch_file", true},
		{"read_file", false},
		{"Read", false},
		{"run_shell_command", false},
		{"Bash", false},
	}

	for _, tt := range tests {
		if got := isFileEditTool(tt.name); got != tt.expected {
			t.Errorf("isFileEditTool(%q) = %v, want %v", tt.name, got, tt.expected)
		}
	}
}

func TestIsFileReadTool(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{"read_file", true},
		{"Read", true},
		{"Grep", true},
		{"Glob", true},
		{"Write", false},
		{"Bash", false},
	}

	for _, tt := range tests {
		if got := isFileReadTool(tt.name); got != tt.expected {
			t.Errorf("isFileReadTool(%q) = %v, want %v", tt.name, got, tt.expected)
		}
	}
}

func TestIsShellTool(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{"Bash", true},
		{"run_shell_command", true},
		{"Read", false},
		{"Write", false},
		{"edit_file", false},
	}

	for _, tt := range tests {
		if got := isShellTool(tt.name); got != tt.expected {
			t.Errorf("isShellTool(%q) = %v, want %v", tt.name, got, tt.expected)
		}
	}
}

func TestTimestampToTime(t *testing.T) {
	ts := "2026-03-22T16:30:00.123456789Z"
	tm, err := TimestampToTime(ts)
	if err != nil {
		t.Fatal(err)
	}
	if tm.Year() != 2026 || tm.Month() != 3 || tm.Day() != 22 {
		t.Errorf("unexpected parsed time: %v", tm)
	}
}

func TestExtractFilesEmpty(t *testing.T) {
	// When no file tool calls, files list should be empty (no placeholders)
	entries := []GCPLogEntry{
		{
			LogName: "projects/test/logs/scion-agents",
			Labels:  map[string]string{"agent_id": "a1"},
			JSONPayload: map[string]any{
				"message": "agent.session.start",
			},
		},
	}
	files := extractFiles(entries)
	if len(files) != 0 {
		t.Errorf("expected empty files when no tool calls found, got %d", len(files))
	}
}

func TestExtractFilesFromReads(t *testing.T) {
	entries := []GCPLogEntry{
		{
			LogName: "projects/test/logs/scion-agents",
			Labels:  map[string]string{"agent_id": "a1"},
			JSONPayload: map[string]any{
				"message":   "agent.tool.call",
				"tool_name": "Read",
				"file_path": "/workspace/config.yaml",
			},
		},
	}
	files := extractFiles(entries)

	fileIDs := map[string]bool{}
	for _, f := range files {
		fileIDs[f.ID] = true
	}
	if !fileIDs["."] {
		t.Error("expected root '.' node")
	}
	if !fileIDs["config.yaml"] {
		t.Error("expected 'config.yaml' from Read tool call")
	}
}

func TestFileReadEvents(t *testing.T) {
	entries := []GCPLogEntry{
		{
			InsertID:  "1",
			Timestamp: "2026-03-22T16:30:00.000Z",
			LogName:   "projects/test/logs/scion-agents",
			Labels:    map[string]string{"agent_id": "a1", "scion.harness": "claude"},
			JSONPayload: map[string]any{
				"message":   "agent.tool.call",
				"tool_name": "Read",
				"file_path": "/workspace/README.md",
			},
		},
	}
	agents := extractAgents(entries)
	events := extractEvents(entries, agents)

	var foundRead bool
	for _, e := range events {
		if e.Type == "file_read" {
			if fe, ok := e.Data.(FileEditEvent); ok {
				if fe.FilePath == "README.md" && fe.Action == "read" {
					foundRead = true
				}
			}
		}
	}
	if !foundRead {
		t.Error("expected file_read event for Read tool call")
	}
}

func TestExtractAgentsFromMessages(t *testing.T) {
	// Agents referenced only in messages (no scion-agents entries) should be discovered
	entries := []GCPLogEntry{
		{
			InsertID:  "1",
			Timestamp: "2026-03-22T16:30:00.000Z",
			LogName:   "projects/test/logs/scion-messages",
			Labels: map[string]string{
				"sender":       "agent:green-agent",
				"sender_id":    "sender-uuid-1",
				"recipient":    "agent:orchestrator",
				"recipient_id": "recipient-uuid-2",
			},
			JSONPayload: map[string]any{
				"sender":          "agent:green-agent",
				"recipient":       "agent:orchestrator",
				"sender_id":       "sender-uuid-1",
				"recipient_id":    "recipient-uuid-2",
				"msg_type":        "state-change",
				"message_content": "test message",
				"message":         "message dispatched",
			},
		},
	}
	agents := extractAgents(entries)
	if len(agents) < 2 {
		t.Fatalf("expected at least 2 agents from messages, got %d", len(agents))
	}
	nameSet := map[string]bool{}
	for _, a := range agents {
		nameSet[a.Name] = true
	}
	if !nameSet["green-agent"] {
		t.Error("expected agent 'green-agent' discovered from message sender")
	}
	if !nameSet["orchestrator"] {
		t.Error("expected agent 'orchestrator' discovered from message recipient")
	}
}

func TestBackfillAgentCreateEvents(t *testing.T) {
	// Agents without lifecycle events should get synthetic agent_create events
	entries := []GCPLogEntry{
		{
			InsertID:  "1",
			Timestamp: "2026-03-22T16:30:00.000Z",
			LogName:   "projects/test/logs/scion-agents",
			Labels:    map[string]string{"agent_id": "agent-1", "scion.harness": "gemini"},
			JSONPayload: map[string]any{
				"message": "agent.session.start",
			},
		},
	}
	agents := extractAgents(entries)
	events := extractEvents(entries, agents)

	// Should have a backfilled agent_create event before the session.start
	var foundCreate bool
	for _, e := range events {
		if e.Type == "agent_create" {
			if lce, ok := e.Data.(AgentLifecycleEvent); ok && lce.AgentID == "agent-1" {
				foundCreate = true
				break
			}
		}
	}
	if !foundCreate {
		t.Error("expected backfilled agent_create event for agent without lifecycle events")
	}
}

func TestAgentDestroyFromPreStop(t *testing.T) {
	entries := []GCPLogEntry{
		{
			InsertID:  "1",
			Timestamp: "2026-03-22T16:30:00.000Z",
			LogName:   "projects/test/logs/scion-agents",
			Labels:    map[string]string{"agent_id": "agent-1", "scion.harness": "claude"},
			JSONPayload: map[string]any{
				"message": "agent.lifecycle.pre_start",
			},
		},
		{
			InsertID:  "2",
			Timestamp: "2026-03-22T16:31:00.000Z",
			LogName:   "projects/test/logs/scion-agents",
			Labels:    map[string]string{"agent_id": "agent-1", "scion.harness": "claude"},
			JSONPayload: map[string]any{
				"message": "agent.lifecycle.pre_stop",
			},
		},
	}
	agents := extractAgents(entries)
	events := extractEvents(entries, agents)

	var foundDestroy bool
	for _, e := range events {
		if e.Type == "agent_destroy" {
			if lce, ok := e.Data.(AgentLifecycleEvent); ok && lce.AgentID == "agent-1" {
				if lce.Action != "destroy" {
					t.Errorf("expected action 'destroy', got %q", lce.Action)
				}
				foundDestroy = true
				break
			}
		}
	}
	if !foundDestroy {
		t.Error("expected agent_destroy event from pre_stop lifecycle event")
	}
}

func TestAgentCreatedFromServerLog(t *testing.T) {
	entries := []GCPLogEntry{
		{
			InsertID:  "1",
			Timestamp: "2026-03-22T16:30:00.000Z",
			LogName:   "projects/test/logs/scion-server",
			Labels: map[string]string{
				"agent_id": "agent-uuid-1",
			},
			JSONPayload: map[string]any{
				"message":  "Agent created",
				"agent_id": "agent-uuid-1",
				"name":     "poet-red",
				"slug":     "poet-red",
			},
		},
		{
			InsertID:  "2",
			Timestamp: "2026-03-22T16:30:01.000Z",
			LogName:   "projects/test/logs/scion-agents",
			Labels:    map[string]string{"agent_id": "agent-uuid-1", "scion.harness": "claude"},
			JSONPayload: map[string]any{
				"message": "agent.lifecycle.pre_start",
			},
		},
	}
	agents := extractAgents(entries)

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "poet-red" {
		t.Errorf("expected agent name 'poet-red' from server log, got %q", agents[0].Name)
	}
}

func TestPostStartSetsRunningPhase(t *testing.T) {
	entries := []GCPLogEntry{
		{
			InsertID:  "1",
			Timestamp: "2026-03-22T16:30:00.000Z",
			LogName:   "projects/test/logs/scion-agents",
			Labels:    map[string]string{"agent_id": "agent-1", "scion.harness": "claude"},
			JSONPayload: map[string]any{
				"message": "agent.lifecycle.pre_start",
			},
		},
		{
			InsertID:  "2",
			Timestamp: "2026-03-22T16:30:01.000Z",
			LogName:   "projects/test/logs/scion-agents",
			Labels:    map[string]string{"agent_id": "agent-1", "scion.harness": "claude"},
			JSONPayload: map[string]any{
				"message": "agent.lifecycle.post_start",
			},
		},
	}
	agents := extractAgents(entries)
	events := extractEvents(entries, agents)

	var foundRunning bool
	for _, e := range events {
		if e.Type == "agent_state" {
			if ase, ok := e.Data.(AgentStateEvent); ok && ase.AgentID == "agent-1" && ase.Phase == "running" {
				foundRunning = true
			}
		}
	}
	if !foundRunning {
		t.Error("expected agent_state with phase 'running' from post_start event")
	}
}
