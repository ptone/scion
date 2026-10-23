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
	"os/exec"
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

	dest, err := seedPrivateGitConfig(base, agentDir, "agent-x")
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
// broker-written regular file.
func TestSeedPrivateGitConfig_ReplacesSymlink(t *testing.T) {
	agentDir := t.TempDir()
	base := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(base, []byte("[core]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Put a symlink where the seed will be written.
	gitDir := filepath.Join(agentDir, "git")
	if err := os.MkdirAll(gitDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(gitDir, "config")); err != nil {
		t.Fatal(err)
	}

	dest, err := seedPrivateGitConfig(base, agentDir, "agent-x")
	if err != nil {
		t.Fatalf("seedPrivateGitConfig: %v", err)
	}
	fi, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("seed must replace a pre-existing symlink with a regular file")
	}
	// The pointed-to file is untouched (rename replaced the link, did not follow it).
	if b, _ := os.ReadFile(outside); string(b) != "original" {
		t.Error("seeding followed the symlink and overwrote its target")
	}
}

func TestSeedPrivateGitConfig_MissingBaseSeedsUpstreamOnly(t *testing.T) {
	agentDir := t.TempDir()
	dest, err := seedPrivateGitConfig(filepath.Join(t.TempDir(), "nope"), agentDir, "agent-x")
	if err != nil {
		t.Fatalf("seedPrivateGitConfig with a missing base should succeed, got: %v", err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	// No base content, but the upstream + autoSetupRemote pre-seed is always present.
	s := string(b)
	if !strings.Contains(s, "autoSetupRemote = true") || !strings.Contains(s, `[branch "agent-x"]`) {
		t.Errorf("missing base should still seed the upstream pre-seed, got %q", s)
	}
	if strings.Contains(s, "[core]") || strings.Contains(s, "[remote") {
		t.Errorf("missing base must not invent base config, got %q", s)
	}
}

// The pre-seeded branch upstream + push.autoSetupRemote must let `git push -u`
// for the agent's own branch exit 0 WITHOUT writing the (read-only) repo config:
// git finds the upstream already set and skips the write. This is the mitigation
// that keeps normal push flows working while the repository config is read-only.
func TestSeedPrivateGitConfig_PushUExitsZeroNoWrite(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gc"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("SCION_HOST_UID", "")
	if err := os.WriteFile(filepath.Join(root, "gc"), []byte("[user]\n\temail=a@b.c\n\tname=A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// A base repo whose .git/config has an origin; seed the private copy for
	// branch agent-x, then install it as the working repo's .git/config.
	bare := filepath.Join(root, "bare.git")
	run(root, "init", "-q", "--bare", bare)
	repo := filepath.Join(root, "repo")
	run(root, "init", "-q", "-b", "agent-x", repo)
	run(repo, "remote", "add", "origin", bare)
	agentDir := t.TempDir()
	dest, err := seedPrivateGitConfig(filepath.Join(repo, ".git", "config"), agentDir, "agent-x")
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seeded), "autoSetupRemote") || !strings.Contains(string(seeded), `[branch "agent-x"]`) {
		t.Fatalf("seed missing upstream/autoSetupRemote:\n%s", seeded)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), seeded, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", ".")
	run(repo, "-c", "core.hooksPath="+os.DevNull, "commit", "-q", "-m", "x")

	before, _ := os.ReadFile(filepath.Join(repo, ".git", "config"))
	cmd := exec.Command("git", "push", "-u", "origin", "agent-x")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git push -u should exit 0 with the upstream pre-seeded, got: %v\n%s", err, out)
	}
	after, _ := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if string(before) != string(after) {
		t.Errorf("push -u modified the repo config despite the pre-seeded upstream:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
