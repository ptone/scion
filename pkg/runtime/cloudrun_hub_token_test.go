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

//go:build unix

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/stagedsecrets"
)

// crHubTokenCanary is an obviously fake credential. It must reach the
// instance only inside the decoded staged blob.
const crHubTokenCanary = "fake-hub-token-canary-7239-not-a-credential"

// writeCRHubToken writes the token file where pkg/agent writes it, below a
// fresh broker-side agent home, and returns the home.
func writeCRHubToken(t *testing.T, token string) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "agent-home")
	if err := os.MkdirAll(filepath.Join(home, ".scion"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, cloudRunHubTokenRel), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// crCreatedContainer returns the single created instance's container.
func crCreatedContainer(t *testing.T, fake *fakeInstancesClient) *runpb.Container {
	t.Helper()
	if len(fake.createReqs) != 1 {
		t.Fatalf("CreateInstance called %d times, want 1", len(fake.createReqs))
	}
	return fake.createReqs[0].Instance.Containers[0]
}

// assertCanaryOnlyInStagedBlob checks that the canary is in the staged
// blob's HubToken field and in no env value (raw), arg or command.
func assertCanaryOnlyInStagedBlob(t *testing.T, c *runpb.Container) {
	t.Helper()
	var blob string
	for _, ev := range c.Env {
		v, _ := ev.GetValues().(*runpb.EnvVar_Value)
		if v == nil {
			continue
		}
		if strings.Contains(v.Value, crHubTokenCanary) {
			t.Errorf("env var %s carries the raw hub credential", ev.Name)
		}
		if ev.Name == stagedsecrets.EnvVar {
			blob = v.Value
		}
	}
	for i, a := range append(append([]string{}, c.Command...), c.Args...) {
		if strings.Contains(a, crHubTokenCanary) {
			t.Errorf("command/arg %d carries the hub credential", i)
		}
	}
	if blob == "" {
		t.Fatalf("%s not set; the hub credential was not staged", stagedsecrets.EnvVar)
	}
	staged, err := stagedsecrets.Decode(blob)
	if err != nil {
		t.Fatalf("decode %s: %v", stagedsecrets.EnvVar, err)
	}
	if staged.HubToken != crHubTokenCanary {
		t.Errorf("staged HubToken present=%v, want the agent's hub credential", staged.HubToken != "")
	}
	for _, fs := range staged.FileSecrets {
		if strings.HasSuffix(fs.Target, cloudRunHubTokenRel) {
			t.Errorf("hub credential staged as a file secret (target %s)", fs.Target)
		}
	}
}

// With a broker-local home, the hub credential is carried in the staged
// blob's dedicated field, never in an env value of its own or in argv.
func TestCloudRunRun_LocalHome_StagesHubTokenInBlobOnly(t *testing.T) {
	fake := &fakeInstancesClient{getErr: notFoundErr()}
	rt := newFakeCloudRunRuntime(t, fake)
	logs := captureRuntimeLog(t)

	cfg := runConfigForTest()
	cfg.UnixUsername = "scion"
	cfg.HomeDir = writeCRHubToken(t, crHubTokenCanary)
	cfg.CommandArgs = []string{"echo", "hello"}

	if _, err := rt.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertCanaryOnlyInStagedBlob(t, crCreatedContainer(t, fake))
	if strings.Contains(logs.String(), crHubTokenCanary) {
		t.Error("runtime log carries the hub credential")
	}
}

// The token is staged alongside other secrets without disturbing them.
func TestCloudRunRun_LocalHome_StagesHubTokenWithSecrets(t *testing.T) {
	fake := &fakeInstancesClient{getErr: notFoundErr()}
	rt := newFakeCloudRunRuntime(t, fake)

	cfg := runConfigForTest()
	cfg.UnixUsername = "scion"
	cfg.Env = []string{"PRESET_KEY=from-env"}
	cfg.ResolvedSecrets = crTestResolvedSecrets()
	cfg.HomeDir = writeCRHubToken(t, crHubTokenCanary)

	if _, err := rt.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertResolvedSecretsInEnv(t, crInstanceEnv(t, fake))
	assertCanaryOnlyInStagedBlob(t, crCreatedContainer(t, fake))
}

// The instance's home is the agent's NFS home leaf, mounted at
// /home/<user>, in both homes modes: the runtime does not read the homes
// setting, so sciontool init writes the credential to
// /home/<user>/.scion/scion-token on that leaf either way.
func TestCloudRunRun_HomeIsNFSLeafInBothHomesModes(t *testing.T) {
	for _, mode := range []string{"", "local", HomeStorageNFS} {
		t.Run("homes="+mode, func(t *testing.T) {
			fake := &fakeInstancesClient{getErr: notFoundErr()}
			rt := newFakeCloudRunRuntime(t, fake)

			cfg := runConfigForTest()
			cfg.UnixUsername = "scion"
			cfg.HomeStorageBackend = mode
			cfg.HomeDir = writeCRHubToken(t, crHubTokenCanary)

			if _, err := rt.Run(context.Background(), cfg); err != nil {
				t.Fatalf("Run: %v", err)
			}
			inst := fake.createReqs[0].Instance
			var homeVol *runpb.Volume
			for _, v := range inst.Volumes {
				if v.Name == "home" {
					homeVol = v
				}
			}
			if homeVol == nil || homeVol.GetNfs() == nil {
				t.Fatalf("instance has no NFS home volume")
			}
			if got, want := homeVol.GetNfs().GetPath(), "/ws/projects/proj/agents/agent-1/home"; got != want {
				t.Errorf("home volume path = %q, want the agent's home leaf %q", got, want)
			}
			mounted := false
			for _, m := range inst.Containers[0].VolumeMounts {
				if m.Name == "home" && m.MountPath == "/home/"+rt.ExecUser() {
					mounted = true
				}
			}
			if !mounted {
				t.Errorf("home leaf not mounted at /home/%s", rt.ExecUser())
			}
			assertCanaryOnlyInStagedBlob(t, inst.Containers[0])
		})
	}
}

// No broker-side home or no token file: nothing is staged and nothing is
// reported (the start simply has no hub credential).
func TestCloudRunRun_NoHubTokenStagesNothing(t *testing.T) {
	for name, home := range map[string]func(t *testing.T) string{
		"no home":       func(t *testing.T) string { return "" },
		"no token file": func(t *testing.T) string { return t.TempDir() },
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeInstancesClient{getErr: notFoundErr()}
			rt := newFakeCloudRunRuntime(t, fake)
			logs := captureRuntimeLog(t)
			cfg := runConfigForTest()
			cfg.HomeDir = home(t)
			if _, err := rt.Run(context.Background(), cfg); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if _, ok := crInstanceEnv(t, fake)[stagedsecrets.EnvVar]; ok {
				t.Errorf("%s set with no secrets and no hub credential", stagedsecrets.EnvVar)
			}
			if strings.Contains(logs.String(), "hub credential is not staged") {
				t.Error("a start with no token file reported a refused credential")
			}
		})
	}
}

// A symlinked or hard-linked token file, or a linked .scion directory, is
// refused at the broker read: nothing is staged, the start continues, and
// the warning names the condition without any file content.
func TestCloudRunRun_LinkedHubTokenRefused(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, home, other string){
		"symlinked token file": func(t *testing.T, home, other string) {
			if err := os.Symlink(other, filepath.Join(home, cloudRunHubTokenRel)); err != nil {
				t.Fatal(err)
			}
		},
		"hard-linked token file": func(t *testing.T, home, other string) {
			if err := os.Link(other, filepath.Join(home, cloudRunHubTokenRel)); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked .scion directory": func(t *testing.T, home, other string) {
			if err := os.Remove(filepath.Join(home, ".scion")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Dir(other), filepath.Join(home, ".scion")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeInstancesClient{getErr: notFoundErr()}
			rt := newFakeCloudRunRuntime(t, fake)
			logs := captureRuntimeLog(t)

			home := filepath.Join(t.TempDir(), "agent-home")
			if err := os.MkdirAll(filepath.Join(home, ".scion"), 0o700); err != nil {
				t.Fatal(err)
			}
			otherDir := t.TempDir()
			other := filepath.Join(otherDir, "scion-token")
			if err := os.WriteFile(other, []byte(crHubTokenCanary), 0o600); err != nil {
				t.Fatal(err)
			}
			plant(t, home, other)

			cfg := runConfigForTest()
			cfg.HomeDir = home
			if _, err := rt.Run(context.Background(), cfg); err != nil {
				t.Fatalf("Run: %v", err)
			}
			c := crCreatedContainer(t, fake)
			for _, ev := range c.Env {
				if ev.Name == stagedsecrets.EnvVar {
					t.Errorf("%s set: a linked token file was staged", stagedsecrets.EnvVar)
				}
			}
			out := logs.String()
			if !strings.Contains(out, "hub credential is not staged") || !strings.Contains(out, hubTokenCondLinked) {
				t.Errorf("no warning naming the linked-file condition (log length %d)", len(out))
			}
			if strings.Contains(out, crHubTokenCanary) {
				t.Error("warning carries file content")
			}
		})
	}
}

// An oversized token file is refused by condition.
func TestReadCloudRunHubToken_TooLarge(t *testing.T) {
	home := writeCRHubToken(t, strings.Repeat("x", cloudRunHubTokenMaxBytes+1))
	tok, cond := readCloudRunHubToken(home)
	if tok != "" || cond != hubTokenCondTooLarge {
		t.Errorf("got token present=%v condition %q, want none and %q", tok != "", cond, hubTokenCondTooLarge)
	}
}

// The other runtimes' staged payloads are unchanged: the shared helpers
// called without a token produce exactly what serializeSecrets produced
// before, and a nil token leaves the docker path's env untouched.
func TestStagedPayloadUnchangedWithoutHubToken(t *testing.T) {
	secrets := crTestResolvedSecrets()
	before, err := serializeSecrets("/home/scion", secrets)
	if err != nil {
		t.Fatal(err)
	}
	after, err := serializeStaged("/home/scion", secrets, "")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("serializeStaged without a token differs from serializeSecrets")
	}
	staged, err := stagedsecrets.Decode(before)
	if err != nil {
		t.Fatal(err)
	}
	if staged.HubToken != "" {
		t.Error("a payload built without a token carries a HubToken")
	}
	if strings.Contains(before, "hub_token") {
		t.Error("hub_token key emitted without a token")
	}

	empty := RunConfig{UnixUsername: "scion"}
	if err := prepareContainerSecretEnv(&empty); err != nil || len(empty.Env) != 0 {
		t.Errorf("prepareContainerSecretEnv with no secrets changed env (%d entries, err %v)", len(empty.Env), err)
	}
	cfg := RunConfig{UnixUsername: "scion", ResolvedSecrets: []api.ResolvedSecret{{Name: "creds", Type: "file", Target: crTestFileTarget, Value: crTestFileSecretValue}}}
	if _, err := applyResolvedSecretsToEnv(&cfg, cloudRunEnvLimit); err != nil {
		t.Fatal(err)
	}
	for _, e := range cfg.Env {
		if k, v, _ := strings.Cut(e, "="); k == stagedsecrets.EnvVar {
			s, err := stagedsecrets.Decode(v)
			if err != nil || s.HubToken != "" {
				t.Errorf("applyResolvedSecretsToEnv staged a hub token (err %v)", err)
			}
		}
	}
}
