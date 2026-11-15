//go:build unix

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

package commands

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agenthome"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/stagedsecrets"
)

// homeSeedFixture points the seed path at a temp seed and returns it, the
// home and the workspace, with chowns recorded instead of applied.
func homeSeedFixture(t *testing.T, euid int) (seed, home, workspace string, chowned *[]string) {
	t.Helper()
	root := t.TempDir()
	seed, home, workspace = filepath.Join(root, "seed"), filepath.Join(root, "home"), filepath.Join(root, "workspace")
	for _, d := range []string{seed, home, workspace} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	origPath, origEUID, origChown := homeSeedMountPath, homeSeedGeteuid, homeSeedLchown
	homeSeedMountPath = seed
	homeSeedGeteuid = func() int { return euid }
	homeSeedLchown = func(name string, uid, gid int) error {
		got = append(got, name)
		return nil
	}
	t.Cleanup(func() { homeSeedMountPath, homeSeedGeteuid, homeSeedLchown = origPath, origEUID, origChown })
	t.Setenv(api.HomeSeedEnvVar, seed)
	return seed, home, workspace, &got
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSeedEphemeralHome_CopiesAndUnsets: the seed tree lands in the home,
// the variable is unset (a re-exec does not seed again), and as root the
// workspace and home roots are chowned to the scion user first.
func TestSeedEphemeralHome_CopiesAndUnsets(t *testing.T) {
	seed, home, workspace, chowned := homeSeedFixture(t, 0)
	writeFile(t, filepath.Join(seed, ".claude", "settings.json"), "{}")
	writeFile(t, filepath.Join(seed, ".bashrc"), "# rc")
	if err := seedEphemeralHome(home, workspace, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("seedEphemeralHome: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); err != nil || string(b) != "{}" {
		t.Errorf("seeded settings = %q, %v", b, err)
	}
	if _, ok := os.LookupEnv(api.HomeSeedEnvVar); ok {
		t.Errorf("%s still set after seeding", api.HomeSeedEnvVar)
	}
	if strings.Join(*chowned, ",") != workspace+","+home {
		t.Errorf("chowned %v, want the workspace and the home", *chowned)
	}
}

// TestSeedEphemeralHome_NoChownUnlessRoot: not running as root, nothing is
// chowned (there is nothing init could change).
func TestSeedEphemeralHome_NoChownUnlessRoot(t *testing.T) {
	_, home, workspace, chowned := homeSeedFixture(t, 1000)
	if err := seedEphemeralHome(home, workspace, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if len(*chowned) != 0 {
		t.Errorf("chowned %v as non-root", *chowned)
	}
}

func TestSeedEphemeralHome_Unset(t *testing.T) {
	_, home, workspace, chowned := homeSeedFixture(t, 0)
	_ = os.Unsetenv(api.HomeSeedEnvVar)
	if err := seedEphemeralHome(home, workspace, 1000, 1000); err != nil || len(*chowned) != 0 {
		t.Fatalf("without the variable: err %v, chowned %v; want a no-op", err, *chowned)
	}
}

// TestSeedEphemeralHome_OtherPathRefused: only the runtime's mount path is
// accepted, so a value from elsewhere cannot copy another tree into $HOME.
func TestSeedEphemeralHome_OtherPathRefused(t *testing.T) {
	_, home, workspace, chowned := homeSeedFixture(t, 0)
	t.Setenv(api.HomeSeedEnvVar, "/etc")
	if err := seedEphemeralHome(home, workspace, 1000, 1000); err == nil || len(*chowned) != 0 {
		t.Fatalf("seed from /etc: err %v, chowned %v; want a refusal before any change", err, *chowned)
	}
}

// TestSeedEphemeralHome_LinksNotFollowed: a link in the seed is skipped,
// never followed, so its target's content does not reach the home.
func TestSeedEphemeralHome_LinksNotFollowed(t *testing.T) {
	seed, home, workspace, _ := homeSeedFixture(t, 1000)
	secret := filepath.Join(t.TempDir(), "secret")
	writeFile(t, secret, "root-only")
	if err := os.Symlink(secret, filepath.Join(seed, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(secret), filepath.Join(seed, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := seedEphemeralHome(home, workspace, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link", "dirlink"} {
		if _, err := os.Lstat(filepath.Join(home, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s in the home: %v, want it skipped", name, err)
		}
	}
}

// TestSeedEphemeralHome_Limits: a seed over the copy limits fails the start.
func TestSeedEphemeralHome_Limits(t *testing.T) {
	seed, home, workspace, _ := homeSeedFixture(t, 1000)
	orig := agenthome.ProvisionedHomeLimits
	agenthome.ProvisionedHomeLimits = agenthome.Limits{MaxFiles: 2, MaxBytes: 1 << 20, MaxDepth: 4}
	t.Cleanup(func() { agenthome.ProvisionedHomeLimits = orig })
	for _, n := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(seed, n), n)
	}
	var limit *agenthome.LimitError
	if err := seedEphemeralHome(home, workspace, os.Getuid(), os.Getgid()); !errors.As(err, &limit) {
		t.Fatalf("seedEphemeralHome over the limits = %v, want a LimitError", err)
	}
}

// TestSeedEphemeralHome_BeforeStagedSecrets: the seed runs before the
// staged secrets are written, so a seed file at a secret target is
// replaced by the secret; init calls it before stagedsecrets.Write.
func TestSeedEphemeralHome_BeforeStagedSecrets(t *testing.T) {
	src, err := os.ReadFile("init.go")
	if err != nil {
		t.Fatal(err)
	}
	seedAt := strings.Index(string(src), "seedEphemeralHome(agentHome")
	writeAt := strings.Index(string(src), "stagedsecrets.Write(agentHome")
	cloneAt := strings.Index(string(src), "runGitCloneWorkspace(targetUID")
	preStartAt := strings.Index(string(src), "lifecycleManager.RunPreStart()")
	if seedAt < 0 || writeAt < 0 || cloneAt < 0 || preStartAt < 0 {
		t.Fatalf("call sites not found: seed %d, write %d, clone %d, pre-start %d", seedAt, writeAt, cloneAt, preStartAt)
	}
	if seedAt > writeAt || seedAt > cloneAt || seedAt > preStartAt {
		t.Errorf("RunInit seeds the home after the staged secrets, the clone or the pre-start hooks")
	}

	seed, home, workspace, _ := homeSeedFixture(t, 1000)
	writeFile(t, filepath.Join(seed, ".config", "token"), "from-seed")
	if err := seedEphemeralHome(home, workspace, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	staged := &stagedsecrets.Staged{FileSecrets: []stagedsecrets.FileSecret{{Name: "token", Target: filepath.Join(home, ".config", "token"), Value: base64.StdEncoding.EncodeToString([]byte("from-secret"))}}}
	if err := stagedsecrets.Write(home, staged); err != nil {
		t.Fatalf("stagedsecrets.Write: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".config", "token")); string(b) != "from-secret" {
		t.Errorf("secret target = %q, want the staged secret", b)
	}
}

// TestSeedEphemeralHome_ChownBestEffort: a refused chown of a volume root
// is logged and the seed still runs (a 1777 root works without it).
func TestSeedEphemeralHome_ChownBestEffort(t *testing.T) {
	seed, home, workspace, _ := homeSeedFixture(t, 0)
	homeSeedLchown = func(string, int, int) error { return os.ErrPermission }
	writeFile(t, filepath.Join(seed, ".bashrc"), "# rc")
	if err := seedEphemeralHome(home, workspace, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("seedEphemeralHome with a refused chown: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); err != nil {
		t.Errorf("home not seeded: %v", err)
	}
}

// TestSeedEphemeralHome_ResetMarkerNotCopied: the broker's reset marker on
// the home leaf stays out of $HOME.
func TestSeedEphemeralHome_ResetMarkerNotCopied(t *testing.T) {
	seed, home, workspace, _ := homeSeedFixture(t, 1000)
	writeFile(t, filepath.Join(seed, homeSeedResetMarker), "ephemeral-0123456789abcdef")
	writeFile(t, filepath.Join(seed, ".bashrc"), "# rc")
	if err := seedEphemeralHome(home, workspace, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(home, homeSeedResetMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reset marker in $HOME: %v, want it left out", err)
	}
}
