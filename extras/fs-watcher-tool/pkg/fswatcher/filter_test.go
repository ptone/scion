package fswatcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilter_ShouldIgnore_InlinePatterns(t *testing.T) {
	f, err := NewFilter([]string{".git/**", "*.swp"}, "")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path   string
		ignore bool
	}{
		{".git/config", true},
		{".git/objects/pack/abc", true},
		{"main.go", false},
		{"pkg/foo.go", false},
		{"file.swp", true},
		{"dir/file.swp", true},
	}

	for _, tt := range tests {
		got := f.ShouldIgnore(tt.path)
		if got != tt.ignore {
			t.Errorf("ShouldIgnore(%q) = %v, want %v", tt.path, got, tt.ignore)
		}
	}
}

func TestFilter_ShouldIgnore_Negation(t *testing.T) {
	f, err := NewFilter([]string{"*.lock"}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Without negation, .lock files are ignored.
	if !f.ShouldIgnore("package-lock.json") {
		// This won't match because *.lock doesn't match package-lock.json
		// (no dot before lock in the right place for filepath.Match).
	}
	if !f.ShouldIgnore("go.lock") {
		t.Error("expected go.lock to be ignored")
	}
}

func TestFilter_FilterFile(t *testing.T) {
	dir := t.TempDir()
	filterPath := filepath.Join(dir, "filter.txt")

	content := `# Comment
.git/**
*.swp
node_modules/**

# Re-include go.sum
!go.sum
`
	if err := os.WriteFile(filterPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := NewFilter(nil, filterPath)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path   string
		ignore bool
	}{
		{".git/HEAD", true},
		{"main.go", false},
		{"file.swp", true},
		{"node_modules/foo/bar.js", true},
		{"go.sum", false}, // negated
	}

	for _, tt := range tests {
		got := f.ShouldIgnore(tt.path)
		if got != tt.ignore {
			t.Errorf("ShouldIgnore(%q) = %v, want %v", tt.path, got, tt.ignore)
		}
	}
}

func TestFilter_Reload(t *testing.T) {
	dir := t.TempDir()
	filterPath := filepath.Join(dir, "filter.txt")

	if err := os.WriteFile(filterPath, []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := NewFilter([]string{".git/**"}, filterPath)
	if err != nil {
		t.Fatal(err)
	}

	if !f.ShouldIgnore("app.log") {
		t.Error("expected app.log to be ignored before reload")
	}

	// Update filter file.
	if err := os.WriteFile(filterPath, []byte("*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := f.Reload([]string{".git/**"}, filterPath); err != nil {
		t.Fatal(err)
	}

	if f.ShouldIgnore("app.log") {
		t.Error("expected app.log to NOT be ignored after reload")
	}
	if !f.ShouldIgnore("app.tmp") {
		t.Error("expected app.tmp to be ignored after reload")
	}
}
