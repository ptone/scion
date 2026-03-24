package fswatcher

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatcher_Debounce(t *testing.T) {
	tmpDir := t.TempDir()
	absPath := filepath.Join(tmpDir, "test.txt")
	_ = os.WriteFile(absPath, []byte("hello"), 0o644)

	var buf bytes.Buffer
	logger := NewLogger(&buf)
	filter, _ := NewFilter(nil, "")
	cfg := Config{Debounce: 50 * time.Millisecond}
	w := NewWatcher(cfg, []string{tmpDir}, filter, nil, logger)

	// Submit multiple events quickly.
	w.submitDebounced("agent-1", absPath, "test.txt", ActionModify)
	w.submitDebounced("agent-1", absPath, "test.txt", ActionModify)
	w.submitDebounced("agent-1", absPath, "test.txt", ActionModify)

	// Wait for debounce.
	time.Sleep(100 * time.Millisecond)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if buf.Len() == 0 {
		t.Errorf("expected events, got none")
		return
	}
	if len(lines) != 1 {
		t.Errorf("expected 1 event after debounce, got %d: %v", len(lines), lines)
	}
}

func TestWatcher_Debounce_DifferentFiles(t *testing.T) {
	tmpDir := t.TempDir()
	abs1 := filepath.Join(tmpDir, "1.txt")
	abs2 := filepath.Join(tmpDir, "2.txt")
	_ = os.WriteFile(abs1, []byte("1"), 0o644)
	_ = os.WriteFile(abs2, []byte("2"), 0o644)

	var buf bytes.Buffer
	logger := NewLogger(&buf)
	filter, _ := NewFilter(nil, "")
	cfg := Config{Debounce: 50 * time.Millisecond}
	w := NewWatcher(cfg, []string{tmpDir}, filter, nil, logger)

	w.submitDebounced("agent-1", abs1, "1.txt", ActionModify)
	w.submitDebounced("agent-1", abs2, "2.txt", ActionModify)

	time.Sleep(100 * time.Millisecond)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 events for different files, got %d: %v", len(lines), lines)
	}
}

func TestWatcher_RenameCoalesce(t *testing.T) {
	tmpDir := t.TempDir()
	absTarget := filepath.Join(tmpDir, "final.txt")
	absTemp := filepath.Join(tmpDir, ".final.txt.swp")
	_ = os.WriteFile(absTarget, []byte("final"), 0o644)

	var buf bytes.Buffer
	logger := NewLogger(&buf)
	filter, _ := NewFilter(nil, "")
	cfg := Config{Debounce: 50 * time.Millisecond}
	w := NewWatcher(cfg, []string{tmpDir}, filter, nil, logger)

	// Simulate editor save: write to temp, then rename temp to target.
	// FAN_MOVED_FROM(temp) -> FAN_MOVED_TO(target)
	w.handleRenameFrom("agent-1", absTemp, ".final.txt.swp")
	w.handleRenameTo("agent-1", absTarget, "final.txt")

	// Wait for debounce.
	time.Sleep(100 * time.Millisecond)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 event (modify) after rename coalesce, got %d: %v", len(lines), lines)
		return
	}

	var ev Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatal(err)
	}

	if ev.Action != ActionModify {
		t.Errorf("expected ActionModify, got %q", ev.Action)
	}
	if ev.Path != "final.txt" {
		t.Errorf("expected path final.txt, got %q", ev.Path)
	}
}

func TestWatcher_RenameNoCoalesce(t *testing.T) {
	tmpDir := t.TempDir()
	abs1 := filepath.Join(tmpDir, "old.txt")
	abs2 := filepath.Join(tmpDir, "new.txt")
	_ = os.WriteFile(abs2, []byte("new"), 0o644)

	var buf bytes.Buffer
	logger := NewLogger(&buf)
	filter, _ := NewFilter(nil, "")
	cfg := Config{Debounce: 50 * time.Millisecond}
	w := NewWatcher(cfg, []string{tmpDir}, filter, nil, logger)

	// Normal rename (not a temp file).
	w.handleRenameFrom("agent-1", abs1, "old.txt")
	w.handleRenameTo("agent-1", abs2, "new.txt")

	// Wait for debounce.
	time.Sleep(100 * time.Millisecond)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 events (rename_from, rename_to) for normal rename, got %d: %v", len(lines), lines)
		return
	}
}
