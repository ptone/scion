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

package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/tmuxdeliver"
)

// writeTool writes an executable script named name into dir that
// records its run by creating marker.
func writeTool(t *testing.T, dir, name, marker string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\n: > '" + marker + "'\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestResolveTool_FixedToolPath: tmux and the probe's shell resolve to
// absolute paths inside ToolPath (S2 review S-1).
func TestResolveTool_FixedToolPath(t *testing.T) {
	if ToolPath != "/usr/bin:/bin" {
		t.Fatalf("ToolPath = %q", ToolPath)
	}
	p, err := ResolveTool("sh")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p) || (filepath.Dir(p) != "/usr/bin" && filepath.Dir(p) != "/bin") {
		t.Fatalf("sh resolved to %q, want an absolute path in %s", p, ToolPath)
	}
}

// TestResolveTool_FailsClosed: a missing tool, a directory, a file with
// no execute bit, a relative directory and a name with a slash are all
// refused.
func TestResolveTool_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "noexec"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := writeTool(t, dir, "good", filepath.Join(dir, "m"))
	if p, err := resolveToolIn([]string{dir}, "good"); err != nil || p != good {
		t.Fatalf("control: resolve good = %q, %v", p, err)
	}
	for _, name := range []string{"missing", "adir", "noexec", "../good", "", "x/good"} {
		if p, err := resolveToolIn([]string{dir}, name); err == nil {
			t.Fatalf("%q resolved to %q", name, p)
		}
	}
	if p, err := resolveToolIn([]string{"relative/dir", dir + "/../" + filepath.Base(dir) + "/nope"}, "good"); err == nil {
		t.Fatalf("resolved %q from a relative or missing directory", p)
	}
}

// TestLocalRunner_PlantedBinaryNotRun: a tmux planted earlier on this
// process's PATH is not run; the runner execs the resolved path only
// (S2 review S-1).
func TestLocalRunner_PlantedBinaryNotRun(t *testing.T) {
	fixed, planted, marks := t.TempDir(), t.TempDir(), t.TempDir()
	fixedMark, plantedMark := filepath.Join(marks, "fixed"), filepath.Join(marks, "planted")
	tmux := writeTool(t, fixed, "tmux", fixedMark)
	writeTool(t, planted, "tmux", plantedMark)
	t.Setenv("PATH", planted+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := LocalRunner{TmuxPath: tmux, ShPath: "/bin/sh"}
	if _, err := r.Run(context.Background(), targetCheckArgv(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(fixedMark); err != nil {
		t.Fatalf("the resolved tmux did not run: %v", err)
	}
	if _, err := os.Stat(plantedMark); err == nil {
		t.Fatal("the planted tmux ran")
	}
}

// TestLocalRunner_PlantedShellNotRunForProbe: the readiness probe runs
// the resolved shell, not an "sh" planted earlier on this process's PATH.
func TestLocalRunner_PlantedShellNotRunForProbe(t *testing.T) {
	fixed, planted, marks := t.TempDir(), t.TempDir(), t.TempDir()
	fixedMark, plantedMark := filepath.Join(marks, "fixed"), filepath.Join(marks, "planted")
	sh := writeTool(t, fixed, "sh", fixedMark)
	writeTool(t, planted, "sh", plantedMark)
	t.Setenv("PATH", planted+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := LocalRunner{TmuxPath: "/nonexistent/tmux", ShPath: sh}
	if _, err := r.Run(context.Background(), tmuxdeliver.ProbeArgv(), nil); err != nil {
		t.Fatalf("Run probe: %v", err)
	}
	if _, err := os.Stat(fixedMark); err != nil {
		t.Fatalf("the resolved sh did not run: %v", err)
	}
	if _, err := os.Stat(plantedMark); err == nil {
		t.Fatal("the planted sh ran")
	}
}

// TestLocalRunner_UnresolvedFailsClosed: without resolved absolute paths
// the runner starts nothing.
func TestLocalRunner_UnresolvedFailsClosed(t *testing.T) {
	planted, marks := t.TempDir(), t.TempDir()
	mark := filepath.Join(marks, "planted")
	writeTool(t, planted, "tmux", mark)
	t.Setenv("PATH", planted+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, r := range []LocalRunner{{}, {TmuxPath: "tmux", ShPath: "sh"}} {
		if _, err := r.Run(context.Background(), targetCheckArgv(), nil); err == nil || !strings.Contains(err.Error(), "no resolved path") {
			t.Fatalf("runner %+v: err %v, want a refusal", r, err)
		}
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("a tmux from PATH ran")
	}
}

// TestRunnerEnv_FixedPath: the child's PATH is ToolPath, never this
// process's PATH, and TMUX is not passed.
func TestRunnerEnv_FixedPath(t *testing.T) {
	t.Setenv("PATH", "/planted:/usr/bin")
	t.Setenv("TMUX", "/tmp/x,1,0")
	env := runnerEnv("scion")
	paths := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			paths++
			if kv != "PATH="+ToolPath {
				t.Fatalf("child %s, want PATH=%s", kv, ToolPath)
			}
		}
		if strings.HasPrefix(kv, "TMUX=") {
			t.Fatalf("child env carries %s", kv)
		}
	}
	if paths != 1 {
		t.Fatalf("%d PATH entries in %q", paths, env)
	}
}
