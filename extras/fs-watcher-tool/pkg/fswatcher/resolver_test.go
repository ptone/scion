package fswatcher

import "testing"

func TestExtractContainerID(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "cgroup v2 docker scope",
			line: "0::/system.slice/docker-abc123def456abc123def456abc123def456abc123def456abc123def456abcd.scope",
			want: "abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
		},
		{
			name: "cgroup v1 docker path",
			line: "12:memory:/docker/abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
			want: "abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
		},
		{
			name: "containerd scope",
			line: "0::/system.slice/cri-containerd-abc123def456abc123def456abc123def456abc123def456abc123def456abcd.scope",
			want: "abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
		},
		{
			name: "no container",
			line: "0::/user.slice/user-1000.slice/session-1.scope",
			want: "",
		},
		{
			name: "empty",
			line: "",
			want: "",
		},
		{
			name: "short id",
			line: "0::/docker-abc123.scope",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractContainerID(tt.line)
			if got != tt.want {
				t.Errorf("extractContainerID(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

func TestIsHex(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"abc123", true},
		{"ABC123", true},
		{"abcdef0123456789", true},
		{"xyz", false},
		{"", true},
		{"abc-123", false},
	}

	for _, tt := range tests {
		got := isHex(tt.input)
		if got != tt.want {
			t.Errorf("isHex(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
