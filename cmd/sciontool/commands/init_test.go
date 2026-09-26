/*
Copyright 2025 The Scion Authors.
*/

package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hooks"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/supervisor"
)

// hubEnvVars lists the environment variables used by the Hub client.
// Leaking these to a subprocess (e.g., sciontool init) causes the child
// to talk to the real Hub and corrupt agent state. See issue #123.
var hubEnvVars = []string{
	"SCION_HUB_ENDPOINT",
	"SCION_HUB_URL",
	"SCION_AUTH_TOKEN",
	"SCION_AGENT_ID",
	"SCION_AGENT_MODE",
}

// scrubHubEnv clears all Hub-related environment variables for the
// duration of the test, preventing accidental communication with a
// real Hub when tests run inside an agent container.
func scrubHubEnv(t *testing.T) {
	t.Helper()
	for _, key := range hubEnvVars {
		t.Setenv(key, "")
	}
}

// filterHubEnv returns a copy of the environment with all Hub-related
// variables removed. Use when constructing exec.Cmd.Env to prevent
// credential leakage to child processes.
func filterHubEnv(env []string) []string {
	var filtered []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if !slices.Contains(hubEnvVars, key) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// TestInitProjectDataIsolation is a canary test that verifies sciontool source code
// does NOT import the pkg/config package, which contains project path resolution logic.
// This is a compile-time guarantee that in-container code cannot access project data paths.
// If this test fails, it means someone added a pkg/config import to sciontool code,
// which would break the agent isolation model.
func TestInitProjectDataIsolation(t *testing.T) {
	// Use go list to get all transitive dependencies of cmd/sciontool
	cmd := exec.Command("go", "list", "-deps", "./cmd/sciontool/...")
	cmd.Dir = filepath.Join(findRepoRoot(t))
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list failed (may not have full module context): %v", err)
	}

	deps := string(out)
	for _, line := range strings.Split(deps, "\n") {
		line = strings.TrimSpace(line)
		if line == "github.com/GoogleCloudPlatform/scion/pkg/config" {
			t.Fatal("sciontool must NOT import pkg/config (project path resolution). " +
				"In-container code should use the Hub API or agent-local files, " +
				"not filesystem-based project data access.")
		}
	}
}

// findRepoRoot walks up from the test file to find the go.mod directory.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod in any parent directory")
		}
		dir = parent
	}
}

func TestExtractChildCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected []string
	}{
		{
			name:     "single command",
			args:     []string{"bash"},
			expected: []string{"bash"},
		},
		{
			name:     "command with args",
			args:     []string{"tmux", "new-session", "-A"},
			expected: []string{"tmux", "new-session", "-A"},
		},
		{
			name:     "empty args",
			args:     []string{},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractChildCommand(tt.args)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d args, got %d", len(tt.expected), len(result))
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("arg[%d]: expected %q, got %q", i, tt.expected[i], v)
				}
			}
		})
	}
}

func TestInitCommand_Help(t *testing.T) {
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetArgs([]string{"init", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "init") {
		t.Error("help output should mention 'init'")
	}
	if !strings.Contains(output, "grace-period") {
		t.Error("help output should mention 'grace-period' flag")
	}
}

func TestInitCommand_GracePeriodFlag(t *testing.T) {
	// Verify the flag exists and has the expected default
	flag := initCmd.Flags().Lookup("grace-period")
	if flag == nil {
		t.Fatal("grace-period flag not found")
	}
	if flag.DefValue != "10s" {
		t.Errorf("expected default grace-period 10s, got %s", flag.DefValue)
	}
}

// TestInitCommand_Integration performs an integration test with a real subprocess.
// This is skipped in short mode as it involves actual process execution.
func TestInitCommand_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("SCION_INTEGRATION_TEST") == "" {
		t.Skip("skipping integration test: SCION_INTEGRATION_TEST not set")
	}

	// Clear Hub env vars so the subprocess cannot talk to the real Hub
	// and corrupt agent state. See issue #123.
	scrubHubEnv(t)

	// Build sciontool if needed for integration testing
	binPath := filepath.Join(t.TempDir(), "sciontool-test")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", binPath, "../")
	if err := cmd.Run(); err != nil {
		t.Skipf("failed to build sciontool for integration test: %v", err)
	}

	// Test running a simple command — filter Hub env vars from the
	// subprocess environment as belt-and-suspenders protection.
	testCmd := exec.Command(binPath, "init", "--", "echo", "hello")
	testCmd.Env = filterHubEnv(os.Environ())
	output, err := testCmd.CombinedOutput()
	if err != nil {
		t.Errorf("init command failed: %v\nOutput: %s", err, output)
	}
	if !strings.Contains(string(output), "hello") {
		t.Errorf("expected output to contain 'hello', got: %s", output)
	}
}

func TestGitCloneWorkspace_NoCloneURL(t *testing.T) {
	// Ensure SCION_GIT_CLONE_URL is not set
	orig := os.Getenv("SCION_GIT_CLONE_URL")
	_ = os.Unsetenv("SCION_GIT_CLONE_URL")
	defer func() {
		if orig != "" {
			_ = os.Setenv("SCION_GIT_CLONE_URL", orig)
		}
	}()

	tmpWorkspace := t.TempDir()
	t.Setenv("SCION_WORKSPACE_PATH", tmpWorkspace)
	err := gitCloneWorkspace(0, 0, "/tmp")
	if err != nil {
		t.Errorf("expected nil error when SCION_GIT_CLONE_URL is not set, got: %v", err)
	}
}

func TestGitCloneWorkspace_WorkspaceExists(t *testing.T) {
	// Create a temp dir with content to simulate existing workspace
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	if isWorkspaceEmpty(tmpDir) {
		t.Error("expected non-empty workspace to return false for isWorkspaceEmpty")
	}
}

func TestIsWorkspaceEmpty(t *testing.T) {
	t.Run("nonexistent directory", func(t *testing.T) {
		if !isWorkspaceEmpty("/nonexistent/path/12345") {
			t.Error("expected true for nonexistent directory")
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		if !isWorkspaceEmpty(tmpDir) {
			t.Error("expected true for empty directory")
		}
	})

	t.Run("directory with files", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0644)
		if isWorkspaceEmpty(tmpDir) {
			t.Error("expected false for directory with files")
		}
	})

	t.Run("directory with only .scion marker", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(tmpDir, ".scion"), 0755)
		if !isWorkspaceEmpty(tmpDir) {
			t.Error("expected true when workspace contains only .scion marker")
		}
	})

	t.Run("directory with only .scion-volumes", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(tmpDir, ".scion-volumes"), 0755)
		if !isWorkspaceEmpty(tmpDir) {
			t.Error("expected true when workspace contains only .scion-volumes")
		}
	})

	t.Run("directory with .scion and .scion-volumes", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(tmpDir, ".scion"), 0755)
		_ = os.MkdirAll(filepath.Join(tmpDir, ".scion-volumes"), 0755)
		if !isWorkspaceEmpty(tmpDir) {
			t.Error("expected true when workspace contains only .scion and .scion-volumes")
		}
	})

	t.Run("directory with .scion marker and real content", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(tmpDir, ".scion"), 0755)
		_ = os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("content"), 0644)
		if isWorkspaceEmpty(tmpDir) {
			t.Error("expected false when workspace has .scion and real files")
		}
	})

	t.Run("directory with only .agents marker", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(tmpDir, ".agents"), 0755)
		if !isWorkspaceEmpty(tmpDir) {
			t.Error("expected true when workspace contains only .agents marker")
		}
	})

	t.Run("directory with all provisioning markers", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(tmpDir, ".scion"), 0755)
		_ = os.MkdirAll(filepath.Join(tmpDir, ".scion-volumes"), 0755)
		_ = os.MkdirAll(filepath.Join(tmpDir, ".agents"), 0755)
		if !isWorkspaceEmpty(tmpDir) {
			t.Error("expected true when workspace contains only .scion, .scion-volumes, and .agents")
		}
	})

	t.Run("directory with .agents and real content", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(tmpDir, ".agents"), 0755)
		_ = os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
		if isWorkspaceEmpty(tmpDir) {
			t.Error("expected false when workspace has .agents and real files")
		}
	})
}

func TestSanitizeGitOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		token    string
		expected string
	}{
		{
			name:     "replaces token in output",
			output:   "fatal: Authentication failed for 'https://oauth2:ghp_secret123@github.com/org/repo.git/'",
			token:    "ghp_secret123",
			expected: "fatal: Authentication failed for 'https://oauth2:***@github.com/org/repo.git/'",
		},
		{
			name:     "replaces multiple occurrences",
			output:   "token ghp_abc then ghp_abc again",
			token:    "ghp_abc",
			expected: "token *** then *** again",
		},
		{
			name:     "empty token returns output unchanged",
			output:   "some output text",
			token:    "",
			expected: "some output text",
		},
		{
			name:     "no match returns output unchanged",
			output:   "nothing sensitive here",
			token:    "ghp_notpresent",
			expected: "nothing sensitive here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeGitOutput(tt.output, tt.token)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestBuildAuthenticatedURL(t *testing.T) {
	tests := []struct {
		name     string
		cloneURL string
		token    string
		expected string
	}{
		{
			name:     "adds oauth2 credentials to HTTPS URL",
			cloneURL: "https://github.com/org/repo.git",
			token:    "ghp_token123",
			expected: "https://oauth2:ghp_token123@github.com/org/repo.git",
		},
		{
			name:     "no token returns URL unchanged",
			cloneURL: "https://github.com/org/repo.git",
			token:    "",
			expected: "https://github.com/org/repo.git",
		},
		{
			name:     "handles URL without .git suffix",
			cloneURL: "https://github.com/org/repo",
			token:    "ghp_abc",
			expected: "https://oauth2:ghp_abc@github.com/org/repo",
		},
		{
			name:     "handles URL with port",
			cloneURL: "https://github.example.com:8443/org/repo.git",
			token:    "tok",
			expected: "https://oauth2:tok@github.example.com:8443/org/repo.git",
		},
		{
			name:     "schemeless URL gets https prefix added with token",
			cloneURL: "github.com/org/repo",
			token:    "ghp_abc",
			expected: "https://oauth2:ghp_abc@github.com/org/repo",
		},
		{
			name:     "schemeless URL gets https prefix added without token",
			cloneURL: "github.com/org/repo",
			token:    "",
			expected: "https://github.com/org/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildAuthenticatedURL(tt.cloneURL, tt.token)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestBuildAuthenticatedURL_SpecialCharsInToken(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		contains string
	}{
		{
			name:     "token with percent sign",
			token:    "ghp_abc%def",
			contains: "oauth2:ghp_abc%25def@",
		},
		{
			name:     "token with at sign",
			token:    "ghp_abc@def",
			contains: "oauth2:ghp_abc%40def@",
		},
		{
			name:     "token with hash sign",
			token:    "ghp_abc#def",
			contains: "oauth2:ghp_abc%23def@",
		},
		{
			name:     "token with all special characters",
			token:    "ghp_%@#tok",
			contains: "oauth2:ghp_%25%40%23tok@",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildAuthenticatedURL("https://github.com/org/repo.git", tt.token)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("expected result to contain %q, got %q", tt.contains, result)
			}
		})
	}
}

func TestDetectDefaultBranch(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "all")

	// Create a bare repo to serve as the "remote"
	remote := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = remote
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s %v", args, out, err)
		}
	}
	run("init", "--bare", ".")
	// The bare repo's HEAD points to master by default in older git versions,
	// or main in newer ones. Set it explicitly for a deterministic test.
	run("symbolic-ref", "HEAD", "refs/heads/testbranch")

	// Create a local repo that has this bare repo as origin
	local := t.TempDir()
	localRun := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = local
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s %v", args, out, err)
		}
	}
	localRun("init", ".")
	localRun("remote", "add", "origin", remote)

	// We need at least one commit in the remote for ls-remote to work
	// Create a commit directly in the bare repo
	tmpWork := t.TempDir()
	cloneRun := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpWork
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s %v", args, out, err)
		}
	}
	cloneRun("clone", remote, ".")
	cloneRun("config", "user.email", "test@test.com")
	cloneRun("config", "user.name", "Test")
	cloneRun("checkout", "-b", "testbranch")
	cloneRun("commit", "--allow-empty", "-m", "init")
	cloneRun("push", "origin", "testbranch")

	noop := func(cmd *exec.Cmd) {}
	result := detectDefaultBranch(local, noop)
	if result != "testbranch" {
		t.Errorf("expected 'testbranch', got %q", result)
	}
}

func TestSanitizeGitOutput_LongToken(t *testing.T) {
	// Fine-grained GitHub PATs are 93 characters long
	longToken := "github_pat_" + strings.Repeat("A", 82) // 93 chars total
	output := "fatal: Authentication failed for 'https://oauth2:" + longToken + "@github.com/org/repo.git/'"

	result := sanitizeGitOutput(output, longToken)

	if strings.Contains(result, longToken) {
		t.Error("long token should be redacted from output")
	}
	if !strings.Contains(result, "***") {
		t.Error("redacted token should be replaced with ***")
	}
	expected := "fatal: Authentication failed for 'https://oauth2:***@github.com/org/repo.git/'"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestUseDirectPasswdEdit(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		expected bool
	}{
		{
			name:     "no env vars set",
			envVars:  map[string]string{},
			expected: false,
		},
		{
			name:     "container=podman",
			envVars:  map[string]string{"container": "podman"},
			expected: true,
		},
		{
			name:     "container=docker (not podman)",
			envVars:  map[string]string{"container": "docker"},
			expected: false,
		},
		{
			name:     "SCION_ALT_USERMOD set",
			envVars:  map[string]string{"SCION_ALT_USERMOD": "1"},
			expected: true,
		},
		{
			name:     "both set",
			envVars:  map[string]string{"container": "podman", "SCION_ALT_USERMOD": "1"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear both env vars, then set what the test needs
			t.Setenv("container", "")
			t.Setenv("SCION_ALT_USERMOD", "")
			_ = os.Unsetenv("container")
			_ = os.Unsetenv("SCION_ALT_USERMOD")
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			result := useDirectPasswdEdit()
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestIsUIDMapped(t *testing.T) {
	// On a normal (non-namespaced) host, all UIDs should be mapped.
	// /proc/self/uid_map typically shows "0 0 4294967295" or similar.
	// We test the function by verifying our own UID is mapped and
	// that an absurdly large UID is likely not mapped in rootless mode
	// (but may be mapped on a normal host).

	t.Run("current user UID is mapped", func(t *testing.T) {
		uid := os.Getuid()
		if !isUIDMapped(uid) {
			t.Errorf("expected current user UID %d to be mapped", uid)
		}
	})

	t.Run("UID 0 is always mapped", func(t *testing.T) {
		if !isUIDMapped(0) {
			t.Error("expected UID 0 to be mapped")
		}
	})
}

func TestIsAuthError(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		expected bool
	}{
		{
			name:     "authentication failed",
			stderr:   "fatal: Authentication failed for 'https://github.com/org/repo.git/'",
			expected: true,
		},
		{
			name:     "could not read username",
			stderr:   "fatal: could not read Username for 'https://github.com': terminal prompts disabled",
			expected: true,
		},
		{
			name:     "403 forbidden",
			stderr:   "fatal: unable to access 'https://github.com/org/repo.git/': The requested URL returned error: 403",
			expected: true,
		},
		{
			name:     "401 unauthorized",
			stderr:   "fatal: unable to access 'https://github.com/org/repo.git/': The requested URL returned error: 401",
			expected: true,
		},
		{
			name:     "invalid credentials",
			stderr:   "remote: Invalid credentials",
			expected: true,
		},
		{
			name:     "branch not found",
			stderr:   "fatal: Remote branch 'nonexistent' not found in upstream origin",
			expected: false,
		},
		{
			name:     "network error",
			stderr:   "fatal: unable to access 'https://nonexistent.invalid/org/repo.git/': Could not resolve host: nonexistent.invalid",
			expected: false,
		},
		{
			name:     "empty stderr",
			stderr:   "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAuthError(tt.stderr); got != tt.expected {
				t.Errorf("isAuthError(%q) = %v, want %v", tt.stderr, got, tt.expected)
			}
		})
	}
}

func TestFormatCloneError(t *testing.T) {
	t.Run("no token", func(t *testing.T) {
		err := formatCloneError("fatal: could not read Username", "")
		if !strings.Contains(err.Error(), "no GITHUB_TOKEN secret configured") {
			t.Errorf("expected 'no GITHUB_TOKEN' message, got: %v", err)
		}
		if !strings.Contains(err.Error(), "fatal: could not read Username") {
			t.Errorf("expected stderr in error, got: %v", err)
		}
	})

	t.Run("with token", func(t *testing.T) {
		err := formatCloneError("fatal: Authentication failed", "ghp_token123")
		// Auth errors should include guidance about checking credentials
		if !strings.Contains(err.Error(), "GITHUB_TOKEN") {
			t.Errorf("expected GITHUB_TOKEN guidance in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "fatal: Authentication failed") {
			t.Errorf("expected stderr in error, got: %v", err)
		}
	})

	t.Run("unclassified error with token", func(t *testing.T) {
		err := formatCloneError("fatal: disk full", "ghp_token123")
		if strings.Contains(err.Error(), "GITHUB_TOKEN") {
			t.Errorf("unclassified error should not mention GITHUB_TOKEN, got: %v", err)
		}
		if !strings.Contains(err.Error(), "unclassified error") {
			t.Errorf("expected 'unclassified error' in message, got: %v", err)
		}
		if !strings.Contains(err.Error(), "fatal: disk full") {
			t.Errorf("expected stderr in error, got: %v", err)
		}
	})
}

func TestIsClaude(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected bool
	}{
		{name: "claude binary", args: []string{"claude"}, expected: true},
		{name: "claude with args", args: []string{"claude", "--model", "opus"}, expected: true},
		{name: "full path to claude", args: []string{"/usr/local/bin/claude"}, expected: true},
		{name: "claude-code variant", args: []string{"claude-code"}, expected: true},
		{name: "tmux wrapping claude", args: []string{"tmux", "new-session", "-s", "scion", "claude", "--no-chrome"}, expected: true},
		{name: "tmux wrapping claude full path", args: []string{"tmux", "new-session", "-s", "scion", "/usr/local/bin/claude"}, expected: true},
		{name: "tmux with joined cmdline", args: []string{"tmux", "new-session", "-s", "scion", "claude --no-chrome --dangerously-skip-permissions"}, expected: true},
		{name: "tmux with joined cmdline full path", args: []string{"tmux", "new-session", "-s", "scion", "/usr/local/bin/claude --no-chrome"}, expected: true},
		{name: "gemini binary", args: []string{"gemini"}, expected: false},
		{name: "bash command", args: []string{"bash", "-c", "echo hello"}, expected: false},
		{name: "empty args", args: []string{}, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClaude(tt.args); got != tt.expected {
				t.Errorf("isClaude(%v) = %v, want %v", tt.args, got, tt.expected)
			}
		})
	}
}

func TestWriteEnvFile(t *testing.T) {
	tmpHome := t.TempDir()

	// Set some SCION_ env vars and a non-SCION var
	t.Setenv("SCION_AGENT_NAME", "test-agent")
	t.Setenv("SCION_AUTH_TOKEN", "secret-token-123")
	t.Setenv("SCION_HARNESS", "gemini")
	t.Setenv("NOT_SCION_VAR", "should-not-appear")

	writeEnvFile(tmpHome, 0, 0)

	envPath := filepath.Join(tmpHome, ".scion", "scion-env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("failed to read scion-env file: %v", err)
	}

	content := string(data)

	// Should contain SCION_ vars
	if !strings.Contains(content, `export SCION_AGENT_NAME="test-agent"`) {
		t.Errorf("expected SCION_AGENT_NAME in env file, got:\n%s", content)
	}
	if !strings.Contains(content, `export SCION_AUTH_TOKEN="secret-token-123"`) {
		t.Errorf("expected SCION_AUTH_TOKEN in env file, got:\n%s", content)
	}
	if !strings.Contains(content, `export SCION_HARNESS="gemini"`) {
		t.Errorf("expected SCION_HARNESS in env file, got:\n%s", content)
	}

	// Should NOT contain non-SCION vars
	if strings.Contains(content, "NOT_SCION_VAR") {
		t.Errorf("unexpected NOT_SCION_VAR in env file")
	}

	// Should contain the header comment
	if !strings.Contains(content, "Auto-generated") {
		t.Errorf("expected header comment in env file")
	}
}

func TestWriteEnvFile_IncludesGitHubToken(t *testing.T) {
	tmpHome := t.TempDir()

	t.Setenv("GITHUB_TOKEN", "ghp_test123")

	writeEnvFile(tmpHome, 0, 0)

	envPath := filepath.Join(tmpHome, ".scion", "scion-env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("failed to read scion-env file: %v", err)
	}

	if !strings.Contains(string(data), `export GITHUB_TOKEN="ghp_test123"`) {
		t.Errorf("expected GITHUB_TOKEN in env file, got:\n%s", string(data))
	}
}

func TestWriteEnvFile_ReflectsUpdatedGitHubToken(t *testing.T) {
	tmpHome := t.TempDir()

	t.Setenv("GITHUB_TOKEN", "ghs_initial_token_abc123")

	writeEnvFile(tmpHome, 0, 0)

	envPath := filepath.Join(tmpHome, ".scion", "scion-env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("failed to read scion-env file: %v", err)
	}
	if !strings.Contains(string(data), `export GITHUB_TOKEN="ghs_initial_token_abc123"`) {
		t.Fatalf("expected initial GITHUB_TOKEN in env file, got:\n%s", string(data))
	}

	// Simulate what StartGitHubTokenRefresh does: os.Setenv then OnRefreshed calls writeEnvFile
	t.Setenv("GITHUB_TOKEN", "ghs_refreshed_token_xyz789")

	writeEnvFile(tmpHome, 0, 0)

	data, err = os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("failed to read scion-env file after refresh: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, `export GITHUB_TOKEN="ghs_refreshed_token_xyz789"`) {
		t.Errorf("expected refreshed GITHUB_TOKEN in env file, got:\n%s", content)
	}
	if strings.Contains(content, "ghs_initial_token_abc123") {
		t.Errorf("stale initial token should not appear in env file after refresh")
	}
}

// TestWriteEnvFile_RefusesSymlinkAtFinalPath proves writeEnvFile no longer
// writes through a plain os.WriteFile+os.Rename: a workload that has
// replaced $HOME/.scion/scion-env with a symlink must have the write
// refused, with the symlink's target left untouched, instead of root
// following it.
func TestWriteEnvFile_RefusesSymlinkAtFinalPath(t *testing.T) {
	tmpHome := t.TempDir()
	scionDir := filepath.Join(tmpHome, ".scion")
	if err := os.MkdirAll(scionDir, 0755); err != nil {
		t.Fatalf("mkdir .scion: %v", err)
	}

	victim := filepath.Join(scionDir, "victim")
	if err := os.WriteFile(victim, []byte("do-not-touch"), 0600); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	envPath := filepath.Join(scionDir, "scion-env")
	if err := os.Symlink(victim, envPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	t.Setenv("SCION_AGENT_NAME", "test-agent")
	writeEnvFile(tmpHome, 0, 0)

	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(data) != "do-not-touch" {
		t.Errorf("symlink target was modified: %q", data)
	}

	linkInfo, err := os.Lstat(envPath)
	if err != nil {
		t.Fatalf("lstat scion-env: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink at the final path should be untouched")
	}
}

// TestWriteEnvFile_DirChownSurvivesSwapAfterWrite proves the fix for the
// scenario where the workload — which owns $HOME and can observe the
// scion-env write completing (e.g. via inotify on $HOME/.scion) — renames
// $HOME/.scion away and drops a symlink to a victim directory in its place
// before root's directory chown runs. The chown must land on the original
// directory (wherever its entry ended up), never on the victim, because it
// operates on a directory fd resolved before the swap rather than
// re-resolving the path afterward.
//
// The swap is injected through writeEnvFileAfterWriteForTest rather than a
// real race, so this is deterministic: the seam fires at exactly the
// window the real exploit needs (after the file write, before the
// directory chown), which a symlink planted before the call does not
// exercise — the write itself already refuses a pre-existing symlink, so
// only a swap injected in that specific window can distinguish the fix
// from the original path-based chown.
//
// scionDirOwnerUID is also overridden here to report uid 0 (root): without
// this, writeEnvFile's O1 owner-gating skips the chown entirely, because a
// freshly-created .scion directory is owned by the (non-root) test process
// itself, not root — which would make this test pass by doing nothing on
// the chown path at all. Forcing "currently root-owned" is what actually
// drives the fd-based chown this test exists to exercise.
func TestWriteEnvFile_DirChownSurvivesSwapAfterWrite(t *testing.T) {
	tmpHome := t.TempDir()
	victimDir := filepath.Join(tmpHome, "victim-dir")
	if err := os.MkdirAll(victimDir, 0700); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	victimInfoBefore, err := os.Stat(victimDir)
	if err != nil {
		t.Fatalf("stat victim before: %v", err)
	}

	scionDir := filepath.Join(tmpHome, ".scion")
	movedDir := filepath.Join(tmpHome, ".scion.moved")

	var movedStBefore *syscall.Stat_t
	writeEnvFileAfterWriteForTest = func(dir string) {
		if err := os.Rename(dir, movedDir); err != nil {
			t.Errorf("swap: rename %s: %v", dir, err)
			return
		}
		// Snapshot ctime right after the rename, before writeEnvFile's own
		// chown call runs against whatever fd it still holds — this is the
		// "before" baseline the final assertion below needs, captured at
		// movedDir's own final path (it didn't exist under that name before
		// this rename, so there's no earlier point to snapshot it at).
		movedInfoBefore, serr := os.Lstat(movedDir)
		if serr != nil {
			t.Errorf("lstat moved dir right after rename: %v", serr)
			return
		}
		movedStBefore = movedInfoBefore.Sys().(*syscall.Stat_t)
		// A brief settle so the chown call below is guaranteed to bump
		// ctime by a measurable amount — see dirfd's ctimeSettle for why
		// this matters on this test suite's filesystem/clock source.
		time.Sleep(15 * time.Millisecond)

		if err := os.Symlink(victimDir, dir); err != nil {
			t.Errorf("swap: symlink %s -> %s: %v", dir, victimDir, err)
		}
	}
	t.Cleanup(func() { writeEnvFileAfterWriteForTest = nil })

	origOwnerUID := scionDirOwnerUID
	scionDirOwnerUID = func(int) (uint32, error) { return 0, nil }
	t.Cleanup(func() { scionDirOwnerUID = origOwnerUID })

	t.Setenv("SCION_AGENT_NAME", "test-agent")
	writeEnvFile(tmpHome, os.Getuid(), os.Getgid())

	// The victim directory must be completely untouched: same mode, same
	// change time (chowning even to the same uid/gid still bumps ctime, so
	// an unchanged ctime proves chown(2) never ran against it).
	victimInfoAfter, err := os.Stat(victimDir)
	if err != nil {
		t.Fatalf("stat victim after: %v", err)
	}
	victimStBefore := victimInfoBefore.Sys().(*syscall.Stat_t)
	victimStAfter := victimInfoAfter.Sys().(*syscall.Stat_t)
	if victimStAfter.Ctim != victimStBefore.Ctim {
		t.Error("victim directory's change time advanced: it was chowned")
	}

	// ".scion" itself must still be the symlink the swap planted — nothing
	// should have unlinked or replaced it either.
	linkInfo, err := os.Lstat(scionDir)
	if err != nil {
		t.Fatalf("lstat .scion: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Error("expected .scion to still be the symlink the swap planted")
	}

	// The real directory, now at its moved-away path, is the one that
	// should have been chowned (here, to the test's own uid/gid, which is
	// always permitted and still bumps ctime). Asserting the ctime actually
	// advanced (not just that the directory still exists) is what stops
	// this test from passing by doing nothing on the chown path.
	if movedStBefore == nil {
		t.Fatal("hook never captured movedStBefore — test setup is broken")
	}
	movedInfoAfter, err := os.Stat(movedDir)
	if err != nil {
		t.Fatalf("stat moved-away original .scion: %v", err)
	}
	movedStAfter := movedInfoAfter.Sys().(*syscall.Stat_t)
	if movedStAfter.Ctim == movedStBefore.Ctim {
		t.Error("moved-away .scion directory's change time did not advance: it was not chowned")
	}
}

func TestGitCloneWorkspace_DefaultEnvValues(t *testing.T) {
	// Set SCION_GIT_CLONE_URL to trigger the clone path, but use a URL
	// that will cause a predictable early failure (non-existent host).
	// This tests that the env parsing logic runs with correct defaults.
	t.Setenv("SCION_GIT_CLONE_URL", "https://nonexistent.invalid/org/repo.git")
	// Explicitly unset branch and depth to verify defaults
	t.Setenv("SCION_GIT_BRANCH", "")
	t.Setenv("SCION_GIT_DEPTH", "")
	t.Setenv("SCION_AGENT_NAME", "test-agent")
	t.Setenv("GITHUB_TOKEN", "")

	// gitCloneWorkspace will fail at the git clone step, but we can verify
	// the function doesn't panic and returns a meaningful error.
	// uid=0 exercises the scion-user fallback path (the lookup will fail
	// gracefully outside a container where no scion user exists).

	tmpWorkspace := t.TempDir()
	t.Setenv("SCION_WORKSPACE_PATH", tmpWorkspace)
	err := gitCloneWorkspace(0, 0, "/tmp")
	if err == nil {
		t.Fatal("expected error from git clone to nonexistent host")
	}
	// The error may come from git init, git fetch, or git clone depending
	// on how far the function gets before failing.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "git clone failed") && !strings.Contains(errMsg, "git init failed") && !strings.Contains(errMsg, "git remote add failed") && !strings.Contains(errMsg, "failed") {
		t.Errorf("expected a git failure error, got: %v", err)
	}

	// Verify .git/ is removed after clone failure to prevent credential leak (miller79/scion#65).
	if _, err := os.Stat(filepath.Join(tmpWorkspace, ".git")); !os.IsNotExist(err) {
		t.Error(".git/ should be removed after clone failure to prevent credential leak")
	}
}

func TestGitCloneWorkspace_NonZeroUIDChownsWorkspace(t *testing.T) {
	// Verify that gitCloneWorkspace chowns /workspace before cloning when
	// a non-zero uid is provided. We use a temp dir as the workspace and
	// our own uid/gid so the chown succeeds without root.
	tmpDir := t.TempDir()

	// Monkey-patch: override workspacePath by setting clone URL so the
	// function proceeds past the early exit, but it will fail at git clone.
	// The important thing is it doesn't panic on chown.
	t.Setenv("SCION_GIT_CLONE_URL", "https://nonexistent.invalid/org/repo.git")
	t.Setenv("SCION_GIT_BRANCH", "main")
	t.Setenv("SCION_GIT_DEPTH", "1")
	t.Setenv("SCION_AGENT_NAME", "test-chown")
	t.Setenv("GITHUB_TOKEN", "")

	// We can't override the hardcoded /workspace path, so we test that
	// the function proceeds without panic when uid > 0. The chown of
	// /workspace will fail (not writable in test), but the error is logged,
	// not returned, so the function continues to the git clone step.
	uid := os.Getuid()
	gid := os.Getgid()
	_ = tmpDir // workspace path is hardcoded; this confirms the logic flow

	tmpWorkspace := t.TempDir()
	t.Setenv("SCION_WORKSPACE_PATH", tmpWorkspace)
	err := gitCloneWorkspace(uid, gid, "/tmp")
	if err == nil {
		t.Fatal("expected error from git clone to nonexistent host")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "git clone failed") && !strings.Contains(errMsg, "git init failed") && !strings.Contains(errMsg, "git remote add failed") && !strings.Contains(errMsg, "failed") {
		t.Errorf("expected a git failure error, got: %v", err)
	}

	// Verify .git/ is removed after clone failure to prevent credential leak (miller79/scion#65).
	if _, err := os.Stat(filepath.Join(tmpWorkspace, ".git")); !os.IsNotExist(err) {
		t.Error(".git/ should be removed after clone failure to prevent credential leak")
	}
}

func TestConfigureGitCommand_SkipsCredentialOverrideWhenAlreadyRunningAsTargetUser(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "git", "status")

	configureGitCommand(cmd, os.Getuid(), os.Getgid())

	if !slices.Contains(cmd.Env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("expected GIT_TERMINAL_PROMPT=0 to be set")
	}
	if cmd.SysProcAttr != nil {
		t.Fatalf("expected no credential override when already running as target user, got %#v", cmd.SysProcAttr)
	}
}

func TestConfigureGitCommand_SkipsCredentialOverrideForNonRootDifferentTarget(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test only covers non-root behavior")
	}

	cmd := exec.CommandContext(context.Background(), "git", "status")
	configureGitCommand(cmd, os.Getuid()+1, os.Getgid())

	if !slices.Contains(cmd.Env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("expected GIT_TERMINAL_PROMPT=0 to be set")
	}
	if cmd.SysProcAttr != nil {
		t.Fatalf("expected no credential override for non-root process, got %#v", cmd.SysProcAttr)
	}
}

// TestConfigureGitCommand_PropagatesTrustBundleEnv proves the git-clone leg
// of the egress_trust_bundle env-propagation path: init's git clone needs
// GIT_SSL_CAINFO to reach the `git` subprocess. configureGitCommand
// (init.go, ~line 2507) builds cmd.Env as append(os.Environ(),
// "GIT_TERMINAL_PROMPT=0") — a full copy of the process environment, not an
// allowlisted subset — so GIT_SSL_CAINFO (and every other CA-bundle var
// buildActorTemplate sets on the container) reaches the actual `git`
// subprocess whenever it is present in the parent's env, with no code
// change needed here to carry it through.
func TestConfigureGitCommand_PropagatesTrustBundleEnv(t *testing.T) {
	t.Setenv("GIT_SSL_CAINFO", "/run/ate/trust-bundle.pem")

	cmd := exec.CommandContext(context.Background(), "git", "status")
	configureGitCommand(cmd, os.Getuid(), os.Getgid())

	if !slices.Contains(cmd.Env, "GIT_SSL_CAINFO=/run/ate/trust-bundle.pem") {
		t.Errorf("cmd.Env = %v, want it to contain GIT_SSL_CAINFO=/run/ate/trust-bundle.pem", cmd.Env)
	}
}

func TestEnsureWorkspaceOwnership_SkipsChownWhenNonRoot(t *testing.T) {
	chownCalled := false
	chown := func(string, int, int) error {
		chownCalled = true
		return nil
	}

	ensureWorkspaceOwnership("/workspace", 1000, 1000, 1000, chown)

	if chownCalled {
		t.Fatal("expected chown to be skipped when already running as non-root")
	}
}

func TestEnsureWorkspaceOwnership_ChownsWhenRoot(t *testing.T) {
	var gotPath string
	var gotUID, gotGID int
	chown := func(path string, uid, gid int) error {
		gotPath = path
		gotUID = uid
		gotGID = gid
		return nil
	}

	ensureWorkspaceOwnership("/workspace", 1000, 1000, 0, chown)

	if gotPath != "/workspace" || gotUID != 1000 || gotGID != 1000 {
		t.Fatalf("unexpected chown call: path=%q uid=%d gid=%d", gotPath, gotUID, gotGID)
	}
}

// TestResolveIsSharedGitWorkspace verifies the compat shim logic for detecting
// a shared-plain git workspace. It covers both the new canonical vars and the
// legacy SCION_SHARED_WORKSPACE fallback path.
func TestResolveIsSharedGitWorkspace(t *testing.T) {
	cases := []struct {
		name          string
		workspaceMode string
		workspaceGit  string
		sharedLegacy  string
		want          bool
	}{
		{
			name:          "new vars: shared-plain + git → true",
			workspaceMode: "shared-plain",
			workspaceGit:  "true",
			want:          true,
		},
		{
			name:          "new vars: clone-per-agent + git → false (not shared)",
			workspaceMode: "clone-per-agent",
			workspaceGit:  "true",
			want:          false,
		},
		{
			name:          "new vars: shared-plain without git → false",
			workspaceMode: "shared-plain",
			workspaceGit:  "",
			want:          false,
		},
		{
			name:          "new vars: worktree-per-agent + git → false (not shared)",
			workspaceMode: "worktree-per-agent",
			workspaceGit:  "true",
			want:          false,
		},
		{
			name:         "legacy: SCION_SHARED_WORKSPACE=true → true (fallback)",
			sharedLegacy: "true",
			want:         true,
		},
		{
			name:         "legacy: SCION_SHARED_WORKSPACE absent → false",
			sharedLegacy: "",
			want:         false,
		},
		{
			name:          "new vars take precedence: mode set + legacy true → follows mode",
			workspaceMode: "clone-per-agent",
			workspaceGit:  "true",
			sharedLegacy:  "true",
			want:          false, // mode=clone-per-agent means not shared-plain
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SCION_WORKSPACE_MODE", tc.workspaceMode)
			t.Setenv("SCION_WORKSPACE_GIT", tc.workspaceGit)
			t.Setenv("SCION_SHARED_WORKSPACE", tc.sharedLegacy)

			got := resolveIsSharedGitWorkspace()
			if got != tc.want {
				t.Errorf("resolveIsSharedGitWorkspace() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseCapSetUID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name: "full capabilities (typical Docker root)",
			// CapEff with all bits set — includes CAP_SETUID (bit 7).
			input: "Name:\tinit\nCapEff:\t000001ffffffffff\n",
			want:  true,
		},
		{
			name: "CAP_SETUID present among limited caps",
			// Bits 0-7 set (0xff) — CAP_SETUID (bit 7) is present.
			input: "Name:\tinit\nCapInh:\t0000000000000000\nCapEff:\t00000000000000ff\n",
			want:  true,
		},
		{
			name: "CAP_SETUID absent (gVisor restricted sandbox)",
			// Only bits 0-6 set (0x7f) — CAP_SETUID (bit 7) is missing.
			input: "Name:\tinit\nCapEff:\t000000000000007f\n",
			want:  false,
		},
		{
			name:  "no capabilities at all",
			input: "Name:\tinit\nCapEff:\t0000000000000000\n",
			want:  false,
		},
		{
			name:  "no CapEff line",
			input: "Name:\tinit\nCapInh:\t0000000000000000\n",
			want:  false,
		},
		{
			name:  "empty input",
			input: "",
			want:  false,
		},
		{
			name:  "malformed hex value",
			input: "CapEff:\tnotahexvalue\n",
			want:  false,
		},
		{
			name: "only CAP_SETUID bit set",
			// Only bit 7 set (0x80).
			input: "CapEff:\t0000000000000080\n",
			want:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCapSetUID(tc.input)
			if got != tc.want {
				t.Errorf("parseCapSetUID() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRequirePrivilegeDropOrFail_SubstrateFailsClosed is the fail-closed
// case: substrate-serve sets RequirePrivilegeDrop, and setupHostUser did
// not actually drop privileges (targetUID stayed 0 — the only way that
// happens on substrate, which always starts the actor as UID 0). RunInit
// must refuse to start the harness rather than run it as root.
func TestRequirePrivilegeDropOrFail_SubstrateFailsClosed(t *testing.T) {
	err := requirePrivilegeDropOrFail(0, true)
	if err == nil {
		t.Fatal("requirePrivilegeDropOrFail(0, true) = nil, want an error — substrate must never start the harness as root")
	}
	if !errors.Is(err, errPrivilegeDropRequired) {
		t.Errorf("requirePrivilegeDropOrFail(0, true) = %v, want errPrivilegeDropRequired", err)
	}
}

// TestRequirePrivilegeDropOrFail_SubstrateSucceedsWhenDropped confirms the
// gate does not fire when the drop actually happened (targetUID != 0) —
// the ordinary, successful case once the actor's capability set and
// SCION_HOST_UID/GID are both in place.
func TestRequirePrivilegeDropOrFail_SubstrateSucceedsWhenDropped(t *testing.T) {
	if err := requirePrivilegeDropOrFail(1000, true); err != nil {
		t.Errorf("requirePrivilegeDropOrFail(1000, true) = %v, want nil", err)
	}
}

// TestRequirePrivilegeDropOrFail_NonSubstrateRootlessUnchanged is the
// non-substrate control: RequirePrivilegeDrop is false (every runtime
// except substrate-serve — the plain `sciontool init` CLI entrypoint never
// sets it), so the generic rootless fallback (e.g. rootless Podman,
// targetUID legitimately staying 0) is completely unaffected by this
// gate, exactly as before this change.
func TestRequirePrivilegeDropOrFail_NonSubstrateRootlessUnchanged(t *testing.T) {
	if err := requirePrivilegeDropOrFail(0, false); err != nil {
		t.Errorf("requirePrivilegeDropOrFail(0, false) = %v, want nil (non-substrate rootless fallback must be unaffected)", err)
	}
}

// TestHarnessSupervisorConfig pins harnessSupervisorConfig's mapping from
// its inputs to supervisor.Config: every field must come through unchanged,
// and WorkingDir in particular must be copied from opts.WorkingDir when set
// and be "" when it is not (the value every caller except substrate-serve's
// InitRunner passes, and what docker/k8s depend on for byte-identical
// behaviour).
func TestHarnessSupervisorConfig(t *testing.T) {
	const gracePeriod = 7 * time.Second
	envOverlay := map[string]string{"FOO": "bar"}
	secretOverrides := map[string]string{"SECRET": "shh"}

	tests := []struct {
		name             string
		opts             InitRunOptions
		want             string // expected WorkingDir
		wantPrivDropDrop bool
	}{
		{name: "WorkingDir set is copied through", opts: InitRunOptions{WorkingDir: "/workspace"}, want: "/workspace"},
		{name: "WorkingDir unset is empty", opts: InitRunOptions{}, want: ""},
		{name: "RequirePrivilegeDrop is copied through", opts: InitRunOptions{RequirePrivilegeDrop: true}, want: "", wantPrivDropDrop: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := harnessSupervisorConfig(tt.opts, gracePeriod, 1000, 1000, false, envOverlay, "enabled", secretOverrides)
			want := supervisor.Config{
				GracePeriod:           gracePeriod,
				UID:                   1000,
				GID:                   1000,
				Username:              "scion",
				Rootless:              false,
				EnvOverlay:            envOverlay,
				NativeTelemetryPolicy: "enabled",
				SecretOverrides:       secretOverrides,
				WorkingDir:            tt.want,
				RequirePrivilegeDrop:  tt.wantPrivDropDrop,
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("harnessSupervisorConfig() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestResolveProjectHookPath(t *testing.T) {
	tests := []struct {
		name                 string
		agentHome            string
		requirePrivilegeDrop bool
		want                 string
	}{
		{
			name:                 "non-enforced mode stays under agentHome",
			agentHome:            "/home/scion",
			requirePrivilegeDrop: false,
			want:                 "/home/scion/.scion/hooks/pre-start.d/30-project-custom",
		},
		{
			name:                 "enforced mode redirects to hooks.EnforcedHooksDir, independent of agentHome",
			agentHome:            "/home/scion",
			requirePrivilegeDrop: true,
			want:                 hooks.EnforcedHooksDir + "/pre-start.d/30-project-custom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveProjectHookPath(tt.agentHome, tt.requirePrivilegeDrop); got != tt.want {
				t.Errorf("resolveProjectHookPath(%q, %v) = %q, want %q", tt.agentHome, tt.requirePrivilegeDrop, got, tt.want)
			}
		})
	}
}

// TestBlockClaudeDebugSymlink_NonEnforced_KeepsHistoricalPathBasedBehaviour
// proves non-substrate runtimes are byte-identical to the pre-fix code: a
// pre-existing symlink at debugDir is followed (os.MkdirAll short-circuits,
// os.Chmod chmods the target) exactly like the original inline
// os.MkdirAll+os.Chmod did. This is deliberate — see blockClaudeDebugSymlink's
// doc comment for why a legitimate non-substrate setup may symlink .claude
// itself (e.g. to a mounted volume), and refusing that would break it.
func TestBlockClaudeDebugSymlink_NonEnforced_KeepsHistoricalPathBasedBehaviour(t *testing.T) {
	tmpHome := t.TempDir()
	victim := t.TempDir()
	if err := os.Chmod(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpHome, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	debugDir := filepath.Join(tmpHome, ".claude", "debug")
	if err := os.Symlink(victim, debugDir); err != nil {
		t.Fatal(err)
	}

	blockClaudeDebugSymlink(debugDir, false)

	info, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o555 {
		t.Errorf("non-enforced mode: victim mode = %o, want 0555 (historical behaviour: chmod follows the symlink)", got)
	}
}

// TestBlockClaudeDebugSymlink_Enforced_RefusesSymlinkAndLeavesVictimUnchanged
// is N2's core regression test: a symlink planted at ~/.claude/debug before
// this runs, pointing at a victim directory, must never be chmod'd through —
// deterministic, no race required, since the symlink already exists when
// this function runs (matching the real exploit: a pre-start hook or
// sidecar plants it before line 836 in RunInit is reached).
func TestBlockClaudeDebugSymlink_Enforced_RefusesSymlinkAndLeavesVictimUnchanged(t *testing.T) {
	tmpHome := t.TempDir()
	victim := t.TempDir()
	if err := os.Chmod(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpHome, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	debugDir := filepath.Join(tmpHome, ".claude", "debug")
	if err := os.Symlink(victim, debugDir); err != nil {
		t.Fatal(err)
	}

	blockClaudeDebugSymlink(debugDir, true)

	info, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("enforced mode must fail closed: victim mode = %o, want unchanged 0700", got)
	}
	linkInfo, err := os.Lstat(debugDir)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Error("expected debugDir to still be the symlink the test planted — nothing should have removed or replaced it")
	}
}

// TestBlockClaudeDebugSymlink_Enforced_CreatesAndChmodsRealDir proves the
// legitimate case still works under enforced mode: when debugDir doesn't
// exist yet (the common case, and $HOME/.claude may not exist yet either,
// since this runs before the harness itself starts), it gets created and
// chmod'd 0555 exactly as intended.
func TestBlockClaudeDebugSymlink_Enforced_CreatesAndChmodsRealDir(t *testing.T) {
	tmpHome := t.TempDir()
	debugDir := filepath.Join(tmpHome, ".claude", "debug")

	blockClaudeDebugSymlink(debugDir, true)

	info, err := os.Stat(debugDir)
	if err != nil {
		t.Fatalf("expected debugDir to be created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o555 {
		t.Errorf("mode = %o, want 0555", got)
	}
}

// TestBlockClaudeDebugSymlink_Enforced_ChmodSurvivesSwapAfterEnsure is T3a's
// core regression test: a workload process that renames debugDir away and
// plants a symlink to a victim directory in its place, in the exact window
// between EnsureDirNoFollow returning its fd and the chmod that follows,
// must not have the chmod land on the victim. The chmod is fd-based
// (d.Chmod, not os.Chmod(debugDir)), so it stays bound to the original
// directory no matter what its entry in the parent becomes afterward.
func TestBlockClaudeDebugSymlink_Enforced_ChmodSurvivesSwapAfterEnsure(t *testing.T) {
	tmpHome := t.TempDir()
	victim := t.TempDir()
	if err := os.Chmod(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	debugDir := filepath.Join(tmpHome, ".claude", "debug")
	movedDir := filepath.Join(tmpHome, ".claude", "debug.moved")

	blockClaudeDebugAfterEnsureForTest = func(dir string) {
		if err := os.Rename(dir, movedDir); err != nil {
			t.Errorf("swap: rename %s: %v", dir, err)
			return
		}
		if err := os.Symlink(victim, dir); err != nil {
			t.Errorf("swap: symlink %s -> %s: %v", dir, victim, err)
		}
	}
	t.Cleanup(func() { blockClaudeDebugAfterEnsureForTest = nil })

	blockClaudeDebugSymlink(debugDir, true)

	victimInfo, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if got := victimInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("victim mode = %o, want unchanged 0700 — chmod followed the swapped-in symlink", got)
	}

	linkInfo, err := os.Lstat(debugDir)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Error("expected debugDir to still be the symlink the swap planted")
	}

	movedInfo, err := os.Stat(movedDir)
	if err != nil {
		t.Fatalf("stat moved-away original debugDir: %v", err)
	}
	if got := movedInfo.Mode().Perm(); got != 0o555 {
		t.Errorf("moved-away original debugDir mode = %o, want 0555 — the held fd's chmod should have landed here", got)
	}
}

// TestCleanGcloudConfigForMetadata_NonEnforced_KeepsHistoricalBehaviour
// proves non-substrate runtimes are byte-identical: entries under gcloudDir
// (except the preserved ADC file) are removed via the historical
// os.ReadDir+os.RemoveAll path, including through a symlinked gcloudDir
// itself — a legitimate non-substrate setup may bind-mount or symlink
// ~/.config/gcloud, and refusing that would break it.
func TestCleanGcloudConfigForMetadata_NonEnforced_KeepsHistoricalBehaviour(t *testing.T) {
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "credentials.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, gcloudConfigKeepFile), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	tmpHome := t.TempDir()
	gcloudDir := filepath.Join(tmpHome, "gcloud-link")
	if err := os.Symlink(real, gcloudDir); err != nil {
		t.Fatal(err)
	}

	cleanGcloudConfigForMetadata(gcloudDir, false)

	if _, err := os.Stat(filepath.Join(real, "credentials.db")); !os.IsNotExist(err) {
		t.Errorf("non-enforced mode: expected credentials.db to be removed through the symlink (historical behaviour), err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(real, gcloudConfigKeepFile)); err != nil {
		t.Errorf("expected %s to be preserved: %v", gcloudConfigKeepFile, err)
	}
}

// TestCleanGcloudConfigForMetadata_Enforced_RefusesSymlinkAndLeavesVictimUnchanged
// is N3's core deterministic regression test: gcloudDir itself is a symlink
// to a victim directory (planted before this runs, matching the real
// exploit — no race required to demonstrate the class), and enforced mode
// must refuse it outright rather than enumerating/deleting through it.
func TestCleanGcloudConfigForMetadata_Enforced_RefusesSymlinkAndLeavesVictimUnchanged(t *testing.T) {
	victim := t.TempDir()
	sentinel := filepath.Join(victim, "sentinel")
	if err := os.WriteFile(sentinel, []byte("do-not-delete"), 0o600); err != nil {
		t.Fatal(err)
	}

	tmpHome := t.TempDir()
	gcloudDir := filepath.Join(tmpHome, ".config", "gcloud")
	if err := os.MkdirAll(filepath.Dir(gcloudDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, gcloudDir); err != nil {
		t.Fatal(err)
	}

	cleanGcloudConfigForMetadata(gcloudDir, true)

	entries, err := os.ReadDir(victim)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "sentinel" {
		t.Errorf("victim directory contents changed: %v", entries)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel must survive: %v", err)
	}
	linkInfo, err := os.Lstat(gcloudDir)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Error("expected gcloudDir to still be the symlink the test planted")
	}
}

// TestCleanGcloudConfigForMetadata_Enforced_CleansRealDir proves the
// legitimate case still works under enforced mode: a real gcloudDir has its
// entries removed except the preserved ADC file.
func TestCleanGcloudConfigForMetadata_Enforced_CleansRealDir(t *testing.T) {
	tmpHome := t.TempDir()
	gcloudDir := filepath.Join(tmpHome, ".config", "gcloud")
	if err := os.MkdirAll(gcloudDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gcloudDir, "credentials.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gcloudDir, gcloudConfigKeepFile), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanGcloudConfigForMetadata(gcloudDir, true)

	if _, err := os.Stat(filepath.Join(gcloudDir, "credentials.db")); !os.IsNotExist(err) {
		t.Errorf("expected credentials.db to be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(gcloudDir, gcloudConfigKeepFile)); err != nil {
		t.Errorf("expected %s to be preserved: %v", gcloudConfigKeepFile, err)
	}
}

// TestCleanGcloudConfigForMetadata_Enforced_MissingDirIsNoop proves the
// "nothing to clean" case is unaffected by the enforced-mode rewrite, and
// specifically that it is NOT misreported as a refused symlink: a missing
// directory must never produce an Error log line, only a genuinely refused
// symlink should. errors.Is(err, os.ErrNotExist) is what tells the two
// apart — os.IsNotExist would not (see readServicesYAML/OpenDirNoFollow's
// own doc comments for the same distinction), so this pins the log-level
// behaviour a mutation back to os.IsNotExist would silently break.
func TestCleanGcloudConfigForMetadata_Enforced_MissingDirIsNoop(t *testing.T) {
	tmpHome := t.TempDir()
	logPath := filepath.Join(tmpHome, "capture.log")
	log.SetLogPath(logPath)
	log.SetQuiet(true)
	t.Cleanup(func() { log.SetQuiet(false) })

	gcloudDir := filepath.Join(tmpHome, ".config", "gcloud")
	cleanGcloudConfigForMetadata(gcloudDir, true)

	// No log call at all is the expected outcome for a genuinely missing
	// directory, so the log file may not even exist yet.
	data, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading captured log: %v", err)
	}
	if strings.Contains(string(data), "ERROR") {
		t.Errorf("a missing gcloud dir must not log an Error line, got: %s", data)
	}
}

// TestCleanGcloudConfigForMetadata_Enforced_SymlinkLogsErrorLine is the
// discriminating half of the test above: a genuinely refused symlink DOES
// log an Error line, proving the missing-dir test isn't just vacuously
// passing because nothing is ever logged at all.
func TestCleanGcloudConfigForMetadata_Enforced_SymlinkLogsErrorLine(t *testing.T) {
	tmpHome := t.TempDir()
	logPath := filepath.Join(tmpHome, "capture.log")
	log.SetLogPath(logPath)
	log.SetQuiet(true)
	t.Cleanup(func() { log.SetQuiet(false) })

	victim := t.TempDir()
	gcloudDir := filepath.Join(tmpHome, ".config", "gcloud")
	if err := os.MkdirAll(filepath.Dir(gcloudDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, gcloudDir); err != nil {
		t.Fatal(err)
	}

	cleanGcloudConfigForMetadata(gcloudDir, true)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading captured log: %v", err)
	}
	if !strings.Contains(string(data), "ERROR") {
		t.Errorf("expected a refused symlink to log an Error line, got: %s", data)
	}
}

// TestChownTreeRootOwned_DirectCall is a thin call-site test proving
// chownTreeRootOwned (N1), in enforced mode, delegates to the shared
// dirfd.ChownTreeNoFollow walk with the root-owned-only filter — the deeper
// symlink-swap race itself is covered once, thoroughly, at the dirfd level
// (TestChownTreeNoFollow_SurvivesIntermediateDirSwapMidWalk).
func TestChownTreeRootOwned_DirectCall(t *testing.T) {
	origFilter := chownTreeRootOwnedFilter
	t.Cleanup(func() { chownTreeRootOwnedFilter = origFilter })
	chownTreeRootOwnedFilter = func(uint32) bool { return true } // simulate root-owned

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	uid, gid := os.Getuid(), os.Getgid()
	walked, changed, err := chownTreeRootOwned(home, uid, gid, true)
	if err != nil {
		t.Fatalf("chownTreeRootOwned: %v", err)
	}
	if walked != 2 { // root dir + "a"
		t.Errorf("walked = %d, want 2", walked)
	}
	if changed != 2 {
		t.Errorf("changed = %d, want 2", changed)
	}
}

// TestChownTreeRootOwned_NonEnforced_UsesPathBasedWalk proves the non-
// enforced branch drives the SAME chownTreeRootOwnedFilter decision through
// the historical filepath.WalkDir+os.Lchown implementation, not the
// fd-based one — both walks visit and chown the same entries here.
func TestChownTreeRootOwned_NonEnforced_UsesPathBasedWalk(t *testing.T) {
	origFilter := chownTreeRootOwnedFilter
	t.Cleanup(func() { chownTreeRootOwnedFilter = origFilter })
	chownTreeRootOwnedFilter = func(uint32) bool { return true }

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	uid, gid := os.Getuid(), os.Getgid()
	walked, changed, err := chownTreeRootOwned(home, uid, gid, false)
	if err != nil {
		t.Fatalf("chownTreeRootOwned: %v", err)
	}
	if walked != 2 {
		t.Errorf("walked = %d, want 2", walked)
	}
	if changed != 2 {
		t.Errorf("changed = %d, want 2", changed)
	}
}

// TestChownTreeRootOwned_MissingRootIsSilentNoop proves R5(a): a missing
// root is a silent no-op (nil, 0, 0) on both branches, restoring the
// historical filepath.WalkDir contract (WalkDir passes the root's own lstat
// error to the callback, which returns nil) rather than surfacing as an
// error.
func TestChownTreeRootOwned_MissingRootIsSilentNoop(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	for _, enforced := range []bool{false, true} {
		walked, changed, err := chownTreeRootOwned(missing, os.Getuid(), os.Getgid(), enforced)
		if err != nil {
			t.Errorf("enforced=%v: err = %v, want nil", enforced, err)
		}
		if walked != 0 || changed != 0 {
			t.Errorf("enforced=%v: walked=%d changed=%d, want 0, 0", enforced, walked, changed)
		}
	}
}

// TestChownTreeRootOwned_NonEnforced_FollowsAncestorSymlink proves R5(b)'s
// gating: on non-enforced runtimes, an ancestor-path symlink is followed
// (the historical filepath.WalkDir behaviour), not refused — a legitimate
// non-substrate setup may symlink an ancestor of the walked root (e.g. from
// a bind-mounted host path), and refusing that would break it.
func TestChownTreeRootOwned_NonEnforced_FollowsAncestorSymlink(t *testing.T) {
	origFilter := chownTreeRootOwnedFilter
	t.Cleanup(func() { chownTreeRootOwnedFilter = origFilter })
	chownTreeRootOwnedFilter = func(uint32) bool { return true }

	real := t.TempDir()
	actual := filepath.Join(real, "actual")
	if err := os.Mkdir(actual, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(actual, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	link := filepath.Join(parent, "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// "home-link" is an ANCESTOR (intermediate) component of root, not
	// root's own leaf — "actual" is the real, walked directory.
	root := filepath.Join(link, "actual")

	uid, gid := os.Getuid(), os.Getgid()
	walked, changed, err := chownTreeRootOwned(root, uid, gid, false)
	if err != nil {
		t.Fatalf("chownTreeRootOwned: %v", err)
	}
	if walked != 2 || changed != 2 {
		t.Errorf("walked=%d changed=%d, want 2, 2 — expected the ancestor symlink to be followed", walked, changed)
	}
}

// TestChownTreeRootOwned_Enforced_RefusesAncestorSymlink proves R5(b)'s
// other half: on substrate (enforced), the same ancestor-path symlink is
// refused rather than followed, so the whole fixup for that root is skipped
// (nothing chowned) instead of silently descending through workload-
// controlled redirection.
func TestChownTreeRootOwned_Enforced_RefusesAncestorSymlink(t *testing.T) {
	origFilter := chownTreeRootOwnedFilter
	t.Cleanup(func() { chownTreeRootOwnedFilter = origFilter })
	chownTreeRootOwnedFilter = func(uint32) bool { return true }

	real := t.TempDir()
	actual := filepath.Join(real, "actual")
	if err := os.Mkdir(actual, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(actual, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	link := filepath.Join(parent, "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(link, "actual")

	uid, gid := os.Getuid(), os.Getgid()
	walked, changed, err := chownTreeRootOwned(root, uid, gid, true)
	if err == nil {
		t.Fatal("expected an error refusing the ancestor symlink in enforced mode")
	}
	if walked != 0 || changed != 0 {
		t.Errorf("walked=%d changed=%d, want 0, 0", walked, changed)
	}
}

// TestChownTreeRootOwned_Enforced_SkipsHardlinkedFile is T3b's core
// regression test: N1's enforced branch chowns specifically root-owned
// entries, so a pre-planted hard link to a root-owned file is exactly what
// it would hand over if the hard-link guard were ever disabled for this
// call site. Forces the root-owned filter, creates a hard-linked pair, and
// asserts the target is left untouched.
func TestChownTreeRootOwned_Enforced_SkipsHardlinkedFile(t *testing.T) {
	origFilter := chownTreeRootOwnedFilter
	t.Cleanup(func() { chownTreeRootOwnedFilter = origFilter })
	chownTreeRootOwnedFilter = func(uint32) bool { return true }

	home := t.TempDir()
	target := filepath.Join(home, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(home, "hardlink")); err != nil {
		t.Fatal(err)
	}
	targetBefore := ownedTargetCtime(t, target)
	time.Sleep(15 * time.Millisecond)

	uid, gid := os.Getuid(), os.Getgid()
	if _, _, err := chownTreeRootOwned(home, uid, gid, true); err != nil {
		t.Fatalf("chownTreeRootOwned: %v", err)
	}
	if ownedTargetCtime(t, target) != targetBefore {
		t.Error("hard-linked target was chowned despite the enforced hard-link guard")
	}
}

func ownedTargetCtime(t *testing.T, path string) syscall.Timespec {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return info.Sys().(*syscall.Stat_t).Ctim
}

func TestIsRootOwned(t *testing.T) {
	if !isRootOwned(0) {
		t.Error("isRootOwned(0) = false, want true")
	}
	if isRootOwned(1000) {
		t.Error("isRootOwned(1000) = true, want false")
	}
}

// scionEnvFileCtime returns the ctime of $HOME/.scion for an owner-gating
// test below, after writeEnvFile has already run once to create it.
func scionEnvFileCtime(t *testing.T, tmpHome string) syscall.Timespec {
	t.Helper()
	info, err := os.Stat(filepath.Join(tmpHome, ".scion"))
	if err != nil {
		t.Fatalf("stat .scion: %v", err)
	}
	return info.Sys().(*syscall.Stat_t).Ctim
}

// TestWriteEnvFile_ChownGating covers all three owner states O1's fstat gate
// distinguishes: root-owned (uid 0) triggers the chown; anything else — the
// target uid itself (the normal steady-state case, once a previous run's
// chown already landed) or any other unexpected uid — is left alone. Each
// case drives scionDirOwnerUID directly rather than needing a real
// differently-owned directory (this test process cannot create one without
// real root).
//
// The "before" ctime is captured via writeEnvFileAfterWriteForTest, firing
// right after the env-file write/rename (which itself bumps .scion's own
// ctime, since that changes the directory's entries) and right before the
// chown gate runs — not before the whole call — so the content-write's own
// ctime bump doesn't get misread as evidence the chown ran.
func TestWriteEnvFile_ChownGating(t *testing.T) {
	tests := []struct {
		name      string
		reportUID uint32
		wantChown bool
	}{
		{name: "root-owned (uid 0) triggers chown", reportUID: 0, wantChown: true},
		{name: "already owned by target uid: no-op skip", reportUID: uint32(os.Getuid()), wantChown: false},
		{name: "unexpected other owner: refuse/skip", reportUID: 424242, wantChown: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpHome := t.TempDir()
			t.Setenv("SCION_AGENT_NAME", "test-agent")

			origOwnerUID := scionDirOwnerUID
			t.Cleanup(func() { scionDirOwnerUID = origOwnerUID })
			scionDirOwnerUID = func(int) (uint32, error) { return tt.reportUID, nil }

			var before syscall.Timespec
			writeEnvFileAfterWriteForTest = func(dir string) {
				before = scionEnvFileCtime(t, tmpHome)
				time.Sleep(15 * time.Millisecond)
			}
			t.Cleanup(func() { writeEnvFileAfterWriteForTest = nil })

			writeEnvFile(tmpHome, os.Getuid(), os.Getgid())
			after := scionEnvFileCtime(t, tmpHome)

			gotChown := after != before
			if gotChown != tt.wantChown {
				t.Errorf("chown occurred = %v, want %v", gotChown, tt.wantChown)
			}
		})
	}
}

// TestReadServicesYAML_NonEnforced_FollowsSymlink proves non-substrate
// runtimes are byte-identical to the historical os.ReadFile: a symlinked
// services config is followed and its content returned.
func TestReadServicesYAML_NonEnforced_FollowsSymlink(t *testing.T) {
	tmpHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpHome, ".scion"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(tmpHome, "real-services.yaml")
	content := []byte("- name: foo\n  command: [\"true\"]\n")
	if err := os.WriteFile(real, content, 0o644); err != nil {
		t.Fatal(err)
	}
	servicesPath := filepath.Join(tmpHome, ".scion", "scion-services.yaml")
	if err := os.Symlink(real, servicesPath); err != nil {
		t.Fatal(err)
	}

	data, err := readServicesYAML(servicesPath, false)
	if err != nil {
		t.Fatalf("readServicesYAML: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("data = %q, want %q", data, content)
	}
}

// TestReadServicesYAML_Enforced_RefusesSymlink is the core regression test:
// a symlink planted at scion-services.yaml (deterministic — the workload
// can plant it any time before this is read) must never be read through in
// enforced mode.
func TestReadServicesYAML_Enforced_RefusesSymlink(t *testing.T) {
	tmpHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpHome, ".scion"), 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(tmpHome, "victim.yaml")
	if err := os.WriteFile(victim, []byte("do-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	servicesPath := filepath.Join(tmpHome, ".scion", "scion-services.yaml")
	if err := os.Symlink(victim, servicesPath); err != nil {
		t.Fatal(err)
	}

	data, err := readServicesYAML(servicesPath, true)
	if err == nil {
		t.Fatalf("expected an error refusing the symlink, got data=%q", data)
	}
	if data != nil {
		t.Errorf("expected no data on refusal, got %q", data)
	}
	linkInfo, err := os.Lstat(servicesPath)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Error("expected scion-services.yaml to still be the symlink the test planted")
	}
}

// TestReadServicesYAML_Enforced_ReadsRealFile proves the legitimate case
// still works: a real, single-link regular file is read normally in
// enforced mode.
func TestReadServicesYAML_Enforced_ReadsRealFile(t *testing.T) {
	tmpHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpHome, ".scion"), 0o755); err != nil {
		t.Fatal(err)
	}
	servicesPath := filepath.Join(tmpHome, ".scion", "scion-services.yaml")
	content := []byte("- name: foo\n  command: [\"true\"]\n")
	if err := os.WriteFile(servicesPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := readServicesYAML(servicesPath, true)
	if err != nil {
		t.Fatalf("readServicesYAML: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("data = %q, want %q", data, content)
	}
}

// TestReadServicesYAML_Enforced_MissingFileIsQuietError proves a missing
// file is reported as an ordinary error (matching os.ReadFile's contract,
// which the caller already treats as "no services to start") WITHOUT being
// logged as a refused symlink — captures real log output and asserts no
// ERROR line, not just err!=nil (a bare err!=nil check can't distinguish
// "quiet ENOENT" from "logged refusal", so it doesn't discriminate the
// errors.Is-vs-os.IsNotExist gate this function relies on).
func TestReadServicesYAML_Enforced_MissingFileIsQuietError(t *testing.T) {
	tmpHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpHome, ".scion"), 0o755); err != nil {
		t.Fatal(err)
	}
	servicesPath := filepath.Join(tmpHome, ".scion", "scion-services.yaml")

	logPath := filepath.Join(tmpHome, "capture.log")
	log.SetLogPath(logPath)
	log.SetQuiet(true)
	t.Cleanup(func() { log.SetQuiet(false) })

	if _, err := readServicesYAML(servicesPath, true); err == nil {
		t.Fatal("expected an error for a missing file")
	}

	data, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading captured log: %v", err)
	}
	if strings.Contains(string(data), "ERROR") {
		t.Errorf("a missing services file must not log an Error line, got: %s", data)
	}
}

// TestReadServicesYAML_Enforced_RefusesHardlink is R2(a): the Nlink check
// is the only defence against a pre-planted hard link to a root-only file
// on the same filesystem — a workload process can hard-link to a file it
// does not own (hard-linking only needs write access to the directory the
// link is created in). Without it, root would read and parse that file's
// content as if it were the services config.
func TestReadServicesYAML_Enforced_RefusesHardlink(t *testing.T) {
	tmpHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpHome, ".scion"), 0o755); err != nil {
		t.Fatal(err)
	}
	servicesPath := filepath.Join(tmpHome, ".scion", "scion-services.yaml")
	victim := filepath.Join(tmpHome, "victim")
	if err := os.WriteFile(victim, []byte("secret: do-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, servicesPath); err != nil {
		t.Fatal(err)
	}

	if data, err := readServicesYAML(servicesPath, true); err == nil {
		t.Errorf("hard-linked services file was read: %q", data)
	}
}

// TestReadServicesYAML_Enforced_RefusesAncestorSymlink proves the no-follow
// resolution applies to every component of the path, not just the leaf: a
// symlinked $HOME/.scion (an ancestor of the services file, not the file
// itself) must be refused, not followed.
func TestReadServicesYAML_Enforced_RefusesAncestorSymlink(t *testing.T) {
	tmpHome, real := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "scion-services.yaml"), []byte("- name: x\n  command: [\"true\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scionDir := filepath.Join(tmpHome, ".scion")
	if err := os.Symlink(real, scionDir); err != nil {
		t.Fatal(err)
	}

	servicesPath := filepath.Join(scionDir, "scion-services.yaml")
	if data, err := readServicesYAML(servicesPath, true); err == nil {
		t.Errorf("ancestor symlink (.scion) was followed: %q", data)
	}
}

// TestReadServicesYAML_Enforced_RefusesFifoWithoutHang is R2(b): proves
// BOTH the S_IFREG check (a FIFO must be refused, not read as if it were a
// regular file) and O_NONBLOCK (the refusal must not require a writer to
// ever show up — a backgrounded pre-start-hook child could hold a FIFO
// open at this exact path and never write to it, which would otherwise
// hang root's init before the harness ever starts). A reader is held open
// (as openLogNoFollow's own FIFO test does) so the O_NONBLOCK open itself
// succeeds instead of failing ENXIO — the point is that the SUBSEQUENT
// fstat/S_IFREG check refuses it, and that neither step ever blocks.
func TestReadServicesYAML_Enforced_RefusesFifoWithoutHang(t *testing.T) {
	tmpHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpHome, ".scion"), 0o755); err != nil {
		t.Fatal(err)
	}
	servicesPath := filepath.Join(tmpHome, ".scion", "scion-services.yaml")
	if err := syscall.Mkfifo(servicesPath, 0o600); err != nil {
		t.Fatal(err)
	}

	rfd, err := syscall.Open(servicesPath, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(rfd) })

	done := make(chan error, 1)
	go func() {
		_, err := readServicesYAML(servicesPath, true)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("FIFO accepted as services file")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readServicesYAML hung on a FIFO")
	}
}

// TestValidateServiceSpecs_DropsInvalidNamesKeepsValidOnes proves the
// authoritative parse-time gate: an invalid Name is dropped from the list
// (logged, never a raw workload-chosen string), while every valid entry —
// regardless of position — passes through unchanged.
func TestValidateServiceSpecs_DropsInvalidNamesKeepsValidOnes(t *testing.T) {
	specs := []api.ServiceSpec{
		{Name: "chrome", Command: []string{"true"}},
		{Name: "../escape", Command: []string{"true"}},
		{Name: "vnc", Command: []string{"true"}},
	}

	got := validateServiceSpecs(specs)

	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	want := []string{"chrome", "vnc"}
	if len(names) != len(want) {
		t.Fatalf("validateServiceSpecs() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("validateServiceSpecs()[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}
