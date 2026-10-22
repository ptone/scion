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

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedPrivateGitConfig_RedactsAndWritesRegularFile(t *testing.T) {
	agentDir := t.TempDir()
	base := filepath.Join(t.TempDir(), "config")
	body := strings.Join([]string{
		"[core]",
		"\tfilemode = true",
		"[remote \"origin\"]",
		"\turl = https://alice:secret@host.example/o/r.git",
		"[credential]",
		"\thelper = store",
		"",
	}, "\n")
	if err := os.WriteFile(base, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	dest, err := seedPrivateGitConfig(base, agentDir)
	if err != nil {
		t.Fatalf("seedPrivateGitConfig: %v", err)
	}
	if want := filepath.Join(agentDir, "git", "config"); dest != want {
		t.Errorf("dest = %q, want %q", dest, want)
	}

	fi, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		t.Errorf("dest must be a regular file, got mode %v", fi.Mode())
	}
	out, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"secret", "store", "alice:"} {
		if strings.Contains(s, bad) {
			t.Errorf("seeded config leaks %q:\n%s", bad, s)
		}
	}
	if !strings.Contains(s, "https://host.example/o/r.git") {
		t.Errorf("seeded config lost the (userinfo-stripped) origin url:\n%s", s)
	}
}

// Re-seeding replaces any stale file, including a symlink an earlier run or the
// agent could have left in the seed location: the result is always a fresh,
// broker-written regular file (P2/P3).
func TestSeedPrivateGitConfig_ReplacesSymlink(t *testing.T) {
	agentDir := t.TempDir()
	base := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(base, []byte("[core]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Plant a symlink where the seed will be written.
	gitDir := filepath.Join(agentDir, "git")
	if err := os.MkdirAll(gitDir, 0700); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(t.TempDir(), "evil")
	if err := os.WriteFile(evil, []byte("owned"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, filepath.Join(gitDir, "config")); err != nil {
		t.Fatal(err)
	}

	dest, err := seedPrivateGitConfig(base, agentDir)
	if err != nil {
		t.Fatalf("seedPrivateGitConfig: %v", err)
	}
	fi, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("seed must replace a planted symlink with a regular file")
	}
	// The pointed-to file is untouched (rename replaced the link, did not follow it).
	if b, _ := os.ReadFile(evil); string(b) != "owned" {
		t.Error("seeding followed the symlink and overwrote its target")
	}
}

func TestSeedPrivateGitConfig_MissingBaseSeedsEmpty(t *testing.T) {
	agentDir := t.TempDir()
	dest, err := seedPrivateGitConfig(filepath.Join(t.TempDir(), "nope"), agentDir)
	if err != nil {
		t.Fatalf("seedPrivateGitConfig with a missing base should succeed, got: %v", err)
	}
	if b, err := os.ReadFile(dest); err != nil || len(b) != 0 {
		t.Errorf("missing base should seed an empty config, got %q (err %v)", string(b), err)
	}
}
