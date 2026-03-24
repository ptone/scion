package fswatcher

import "testing"

func TestIsTempFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{".file.swp", true},
		{".file.tmp", true},
		{"file~", true},
		{".hidden", true},
		{"normal.go", false},
		{"dir/.backup.swp", true},
		{"dir/file.swo", true},
		{"Makefile", false},
	}

	for _, tt := range tests {
		got := isTempFile(tt.path)
		if got != tt.want {
			t.Errorf("isTempFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
