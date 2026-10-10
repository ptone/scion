// Copyright 2026 The Scion Authors.

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/harness"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// Synthetic placeholder credentials; never real values.
const (
	failfastAPIKey     = "sk-ant-test-placeholder-0000000000"
	failfastOAuthToken = "oauth-test-placeholder-0000000000"
)

// stripNoAuth removes the top-level no_auth block from the copied claude
// harness-config, giving a harness that does not allow starting without
// credentials.
func stripNoAuth(t *testing.T, hcDir string) {
	t.Helper()
	path := filepath.Join(hcDir, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^no_auth:\n(?:[ \t]+.*\n|\n)*`)
	out := re.ReplaceAll(data, nil)
	if string(out) == string(data) || strings.Contains(string(out), "\nno_auth:") {
		t.Fatal("fixture: no_auth block not removed from claude config.yaml")
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// failfastManager is policyTestManager with a named mock runtime: the file
// part of the check depends on whether the runtime bind-mounts the agent
// home over the container home.
func failfastManager(runs *int, runtimeName string) Manager {
	return NewManager(&runtime.MockRuntime{
		NameFunc: func() string { return runtimeName },
		ListFunc: func(ctx context.Context, labelFilter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{}, nil
		},
		RunFunc: func(ctx context.Context, cfg runtime.RunConfig) (string, error) {
			if runs != nil {
				*runs++
			}
			return "mock-id", nil
		},
	})
}

func brokerStart(t *testing.T, scion, name string) api.StartOptions {
	t.Helper()
	return api.StartOptions{Name: name, ProjectPath: scion, HarnessConfig: "claude", BrokerMode: true}
}

func assertNoAuthSatisfied(t *testing.T, err error, runs int, wantInMsg ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("Start succeeded, want a no-auth-satisfied error")
	}
	if !errors.Is(err, harness.ErrNoAuthSatisfied) {
		t.Fatalf("Start error = %v, want ErrNoAuthSatisfied", err)
	}
	for _, s := range wantInMsg {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not mention %q", err, s)
		}
	}
	for _, secret := range []string{failfastAPIKey, failfastOAuthToken} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks a credential value: %q", err)
		}
	}
	if runs != 0 {
		t.Errorf("container ran %d times, want 0 (fail before launch)", runs)
	}
}

// An explicit auth type with none of its credentials fails the broker start
// before any container is created, naming the harness and its auth types.
func TestStart_BrokerFailsFastWhenExplicitAuthTypeUnsatisfied(t *testing.T) {
	e, _ := newClaudeRestartEnv(t)
	runs := 0
	opts := brokerStart(t, e.scion, "ff-explicit")
	opts.HarnessAuth = "api-key"
	opts.Env = map[string]string{"GOOGLE_CLOUD_LOCATION": "us-east5"} // ambient only
	_, err := failfastManager(&runs, "docker").Start(context.Background(), opts)
	assertNoAuthSatisfied(t, err, runs, "claude", `"api-key"`, "ANTHROPIC_API_KEY", "api-key, auth-file, oauth-token, vertex-ai")
}

// A harness without a no_auth behaviour and no credentials at all fails the
// broker start before any container is created.
func TestStart_BrokerFailsFastWhenNoAuthForbidden(t *testing.T) {
	e, hcDir := newClaudeRestartEnv(t)
	stripNoAuth(t, hcDir)
	runs := 0
	_, err := failfastManager(&runs, "docker").Start(context.Background(), brokerStart(t, e.scion, "ff-forbidden"))
	assertNoAuthSatisfied(t, err, runs, "claude", "does not allow starting without credentials", "api-key, auth-file, oauth-token, vertex-ai")
}

// Each kind of satisfying credential lets the broker start proceed, on a
// harness that does not allow starting without credentials.
func TestStart_BrokerStartsWithEachSatisfyingCredential(t *testing.T) {
	cases := []struct {
		name     string
		explicit string
		env      map[string]string
		secrets  []api.ResolvedSecret
	}{
		{name: "api-key env", env: map[string]string{"ANTHROPIC_API_KEY": failfastAPIKey}},
		{name: "oauth-token env", env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": failfastOAuthToken}},
		{name: "explicit api-key env", explicit: "api-key", env: map[string]string{"ANTHROPIC_API_KEY": failfastAPIKey}},
		{name: "vertex-ai project and region", env: map[string]string{"GOOGLE_CLOUD_PROJECT": "placeholder-project", "GOOGLE_CLOUD_REGION": "us-east5"}},
		{name: "auth-file file secret", secrets: []api.ResolvedSecret{{
			Name: "CLAUDE_AUTH", Type: "file", Target: "/home/scion/.claude/.credentials.json", Value: `{"placeholder":true}`,
		}}},
		{name: "explicit auth-file file secret", explicit: "auth-file", secrets: []api.ResolvedSecret{{
			Name: "CLAUDE_AUTH", Type: "file", Target: "/home/scion/.claude/.credentials.json", Value: `{"placeholder":true}`,
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, hcDir := newClaudeRestartEnv(t)
			stripNoAuth(t, hcDir)
			runs := 0
			opts := brokerStart(t, e.scion, "ff-ok")
			opts.HarnessAuth = tc.explicit
			opts.Env = tc.env
			opts.ResolvedSecrets = tc.secrets
			if _, err := failfastManager(&runs, "docker").Start(context.Background(), opts); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if runs != 1 {
				t.Errorf("container ran %d times, want 1", runs)
			}
		})
	}
}

// A harness whose no_auth behaviour allows it starts without credentials.
func TestStart_BrokerStartsWithoutCredentialsWhenNoAuthAllowed(t *testing.T) {
	e, _ := newClaudeRestartEnv(t) // claude ships no_auth: drop-to-shell
	runs := 0
	if _, err := failfastManager(&runs, "docker").Start(context.Background(), brokerStart(t, e.scion, "ff-noauth")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if runs != 1 {
		t.Errorf("container ran %d times, want 1", runs)
	}
}

// A credential file already in the agent home (written by an earlier
// provision) satisfies an explicit file auth type on restart.
func TestStart_BrokerRestartWithExistingCredentialFile(t *testing.T) {
	e, _ := newClaudeRestartEnv(t)
	mgr := failfastManager(nil, "docker")
	opts := brokerStart(t, e.scion, "ff-file")
	if _, err := mgr.Start(context.Background(), opts); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	home := config.GetAgentHomePath(e.scion, "ff-file")

	restart := opts
	restart.HarnessAuth = "auth-file"
	if _, err := mgr.Start(context.Background(), restart); !errors.Is(err, harness.ErrNoAuthSatisfied) {
		t.Fatalf("fixture: restart without the file = %v, want ErrNoAuthSatisfied", err)
	}

	cred := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"placeholder":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runs := 0
	if _, err := failfastManager(&runs, "docker").Start(context.Background(), restart); err != nil {
		t.Fatalf("restart with the credential file present: %v", err)
	}
	if runs != 1 {
		t.Errorf("container ran %d times, want 1", runs)
	}
}

// Restarting a stopped agent whose restart carries only ambient env (the
// ptone/scion#3810 path) still starts: the recorded credential is restaged
// and satisfies the persisted auth type.
func TestStart_BrokerRestartOfStoppedAgentKeepsRecordedCredential(t *testing.T) {
	e, hcDir := newClaudeRestartEnv(t)
	stripNoAuth(t, hcDir)
	mgr := failfastManager(nil, "docker")
	opts := brokerStart(t, e.scion, "ff-restart")
	opts.Env = map[string]string{"ANTHROPIC_API_KEY": failfastAPIKey, "GOOGLE_CLOUD_LOCATION": "us-east5"}
	if _, err := mgr.Start(context.Background(), opts); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	restart := opts
	restart.Env = map[string]string{"GOOGLE_CLOUD_LOCATION": "us-east5"}
	runs := 0
	if _, err := failfastManager(&runs, "docker").Start(context.Background(), restart); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if runs != 1 {
		t.Errorf("container ran %d times, want 1", runs)
	}
}

// Outside broker mode the check does not run (scope: broker start path).
func TestStart_LocalModeDoesNotFailFast(t *testing.T) {
	e, _ := newClaudeRestartEnv(t)
	runs := 0
	opts := api.StartOptions{Name: "ff-local", ProjectPath: e.scion, HarnessConfig: "claude", HarnessAuth: "api-key"}
	if _, err := failfastManager(&runs, "docker").Start(context.Background(), opts); err != nil {
		t.Fatalf("local Start: %v", err)
	}
	if runs != 1 {
		t.Errorf("container ran %d times, want 1", runs)
	}
}

// A credential file supplied by a volume mounted at the file's target, or at
// an ancestor directory, satisfies an explicit file auth type: the
// provisioner sees the file in the container. A volume elsewhere does not.
func TestStart_BrokerCredentialFileFromVolume(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		dir     bool
		wantErr bool
	}{
		{name: "file volume at target", target: "~/.claude/.credentials.json"},
		{name: "dir volume at ancestor", target: "~/.claude", dir: true},
		{name: "absolute dir volume under container home", target: "/home/scion/.claude", dir: true},
		{name: "volume elsewhere", target: "~/.claude-other", dir: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newClaudeRestartEnv(t)
			src := filepath.Join(t.TempDir(), "creds")
			if tc.dir {
				if err := os.MkdirAll(src, 0o700); err != nil {
					t.Fatal(err)
				}
				src = filepath.Join(src, ".credentials.json")
			}
			if err := os.WriteFile(src, []byte(`{"placeholder":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.dir {
				src = filepath.Dir(src)
			}
			runs := 0
			opts := brokerStart(t, e.scion, "ff-volume")
			opts.HarnessAuth = "auth-file"
			opts.InlineConfig = &api.ScionConfig{Volumes: []api.VolumeMount{{Source: src, Target: tc.target, ReadOnly: true}}}
			_, err := failfastManager(&runs, "docker").Start(context.Background(), opts)
			if tc.wantErr {
				assertNoAuthSatisfied(t, err, runs, "claude", `"auth-file"`)
				return
			}
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if runs != 1 {
				t.Errorf("container ran %d times, want 1", runs)
			}
		})
	}
}

// A file-type secret whose target is the auth file's target satisfies an
// explicit file auth type even when its name and target make it no auth
// candidate (grok-build's ~/.grok/auth.json is not one of the target
// suffixes OverlayFileSecrets recognises): sciontool writes it to its target
// before the provisioner runs. A file secret elsewhere does not. grok-build's
// home template ships no file at that target (copilot's does, so it cannot
// show a rejection).
func TestStart_BrokerCredentialFileFromFileSecret(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		wantErr      bool
	}{
		{name: "file secret at target", target: "~/.grok/auth.json"},
		{name: "absolute file secret at target", target: "/home/scion/.grok/auth.json"},
		{name: "file secret elsewhere", target: "~/.grok/other.json", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"XAI_API_KEY", "SCION_METADATA_PROJECT_ID"} {
				t.Setenv(k, "")
			}
			// Resolve before newClaudeRestartEnv changes the working directory.
			src, err := filepath.Abs(filepath.Join("..", "..", "harnesses", "grok-build"))
			if err != nil {
				t.Fatal(err)
			}
			e, _ := newClaudeRestartEnv(t)
			if err := os.CopyFS(filepath.Join(e.scion, "harness-configs", "grok-build"), os.DirFS(src)); err != nil {
				t.Fatalf("copy grok-build harness-config: %v", err)
			}
			runs := 0
			opts := api.StartOptions{Name: "ff-filesecret", ProjectPath: e.scion, HarnessConfig: "grok-build", BrokerMode: true,
				HarnessAuth: "auth-file",
				ResolvedSecrets: []api.ResolvedSecret{{
					Name: "grok-settings", Type: "file", Target: tc.target, Value: `{"placeholder":true}`, Source: "user",
				}}}
			_, err = failfastManager(&runs, "docker").Start(context.Background(), opts)
			if tc.wantErr {
				assertNoAuthSatisfied(t, err, runs, "grok", `"auth-file"`)
				return
			}
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if runs != 1 {
				t.Errorf("container ran %d times, want 1", runs)
			}
		})
	}
}

// On a runtime that does not bind-mount the agent home over the container
// home (Kubernetes here), the container home can hold credential files the
// broker cannot see, so an explicit file auth type is not rejected. An
// env-only auth type still is.
func TestStart_BrokerOtherRuntimeFailsOpenForFileTypes(t *testing.T) {
	t.Run("explicit file type starts", func(t *testing.T) {
		e, _ := newClaudeRestartEnv(t)
		runs := 0
		opts := brokerStart(t, e.scion, "ff-k8s-file")
		opts.HarnessAuth = "auth-file"
		if _, err := failfastManager(&runs, "kubernetes").Start(context.Background(), opts); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if runs != 1 {
			t.Errorf("container ran %d times, want 1", runs)
		}
	})
	t.Run("same start on docker fails", func(t *testing.T) {
		e, _ := newClaudeRestartEnv(t)
		runs := 0
		opts := brokerStart(t, e.scion, "ff-docker-file")
		opts.HarnessAuth = "auth-file"
		_, err := failfastManager(&runs, "docker").Start(context.Background(), opts)
		assertNoAuthSatisfied(t, err, runs, "claude", `"auth-file"`)
	})
	t.Run("explicit env-only type still fails", func(t *testing.T) {
		e, _ := newClaudeRestartEnv(t)
		runs := 0
		opts := brokerStart(t, e.scion, "ff-k8s-env")
		opts.HarnessAuth = "api-key"
		_, err := failfastManager(&runs, "kubernetes").Start(context.Background(), opts)
		assertNoAuthSatisfied(t, err, runs, "claude", `"api-key"`)
	})
}
